package streamsup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"time"
)

// idleStall is the default watchdog threshold: how long the child's stdout may
// sit silent *while claude owes an assistant turn* before the watchdog judges
// the stream stalled and emits a stall signal. Set conservatively (240s,
// matching streamrunner): in plain stream-json mode claude emits no stdout
// mid-turn, so a legitimately long assistant turn (extended thinking at
// xhigh/Opus) is indistinguishable from a wedge until the turn completes — the
// threshold must clear a realistic max turn. Type-awareness already excludes the
// big false-positive source: in-flight tool runs happen *after* an assistant
// turn, when claude owes nothing, so their silence never trips the watchdog.
// WatchdogConfig.Idle overrides it (0 → this default); tests shrink it.
const idleStall = 240 * time.Second

// watchdogTick caps the poll interval; minWatchdogTick floors it. The actual
// tick is derived from the idle threshold (watchdogTickFor) so a shrunk test
// threshold still gets a proportionally fine tick without spinning into a
// busy-wait. Both mirror streamrunner.
const (
	watchdogTick    = 5 * time.Second
	minWatchdogTick = 5 * time.Millisecond
)

// watchdogTickFor derives the poll interval from the idle threshold: idle/8,
// clamped to [minWatchdogTick, watchdogTick]. For the 240s production default
// this is 5s; for a 200ms test threshold it is 25ms. Lifted verbatim from
// streamrunner's watchdog.
func watchdogTickFor(idle time.Duration) time.Duration {
	tick := idle / 8
	if tick > watchdogTick {
		tick = watchdogTick
	}
	if tick < minWatchdogTick {
		tick = minWatchdogTick
	}
	return tick
}

// stallTracker is the receive-side type-tracker: an io.Writer that consumes the
// child's stdout line stream and tracks whether claude *owes an assistant turn*
// (awaiting) plus the time of the last activity (lastEvent). It reads only the
// structural top-level `type` of each newline-delimited line — never event
// content, which is neither decoded, retained, nor logged (AC3).
//
// It is the terminal sink for its copy of the stream, NOT a tee: Write fully
// consumes the bytes and reports (len(b), nil), so the caller can fan the child's
// stdout to both the #1088 Parser and this tracker with io.MultiWriter (mirrors
// Parser.Write's full-consume contract).
//
// The #1088 Parser holds no awaiting flag, so this tracker carries its own
// type-tracking state over the same stream. (Until #1385 this said the Parser was
// "deliberately turn-stateless"; it now holds one rate-bound token accumulator.
// The awaiting-flag half — the half this tracker exists for — is unchanged.)
// Unlike
// the Parser (which locks nothing, having no second reader), the tracker locks:
// the poll goroutine reads awaiting/lastEvent via snapshot concurrently with the
// os/exec forwarder goroutine's Write. Same lock discipline as streamrunner's
// streamParser.
type stallTracker struct {
	now    func() time.Time
	log    *slog.Logger
	maxBuf int

	mu        sync.Mutex // the poll goroutine reads state the forwarder goroutine writes
	buf       []byte
	lastEvent time.Time
	awaiting  bool
}

var _ io.Writer = (*stallTracker)(nil)

// newStallTracker returns a tracker using now as its clock seam (nil → time.Now)
// and log for content-free Debug diagnostics (nil → slog.Default). It starts NOT
// awaiting, and arms only on a `user` line from the child or an explicit
// Watchdog.UserTurnSent.
//
// The initial value cannot be true here even though streamrunner's
// newStreamParser sets it (#1504). On this interactive surface the child spawns
// on session activation or RestartFresh and emits its `system`/`init` before any
// user turn exists — a message may not arrive for hours — and `init` is
// activity-only, so a tracker that assumed an owed turn at construction fired a
// false stall against a perfectly healthy idle session. streamrunner's runner *is*
// the send side, so there construction and delivery are the same moment; see the
// divergence paragraph on Watchdog.
func newStallTracker(now func() time.Time, log *slog.Logger) *stallTracker {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &stallTracker{
		now:       now,
		log:       log,
		maxBuf:    defaultMaxParseBuf,
		lastEvent: now(),
	}
}

// Write consumes b: it appends to the line buffer, processes every complete
// '\n'-delimited line, and keeps the partial remainder for the next Write. It
// always reports (len(b), nil) — the tracker is the terminal sink of its stream
// copy, so it has no downstream short-write to propagate.
func (t *stallTracker) Write(b []byte) (int, error) {
	t.feed(b)
	return len(b), nil
}

// feed appends the bytes to the line accumulator and consumes every complete
// newline-delimited line. The partial remainder is kept for the next Write; if
// it grows past maxBuf without a newline (pathological / hostile child) it is
// dropped rather than buffered unbounded — bounded memory. Mirrors the #1088
// Parser / streamrunner streamParser feed mechanics.
func (t *stallTracker) feed(b []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.buf = append(t.buf, b...)
	rest := t.buf
	for {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			break
		}
		t.consumeLine(rest[:i])
		rest = rest[i+1:]
	}
	if len(rest) > t.maxBuf {
		t.log.Debug("streamsup: watchdog dropping oversized partial line", "bytes", len(rest))
		rest = nil
	}
	// Copy the remainder into a fresh slice so the (possibly large) backing array
	// of t.buf is released.
	t.buf = append([]byte(nil), rest...)
}

// consumeLine records the line as activity and, if it parses as a JSON object
// with a top-level `type`, transitions awaiting. Caller holds t.mu. No field
// beyond `type` is ever decoded, retained, or logged (AC3).
func (t *stallTracker) consumeLine(line []byte) {
	// Every complete line is activity, even one that fails to parse.
	t.lastEvent = t.now()

	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var lt struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &lt); err != nil {
		return
	}
	switch lt.Type {
	case "assistant":
		// claude produced a turn; it now runs a tool or completes — the silence
		// that follows is expected, so stop awaiting.
		t.awaiting = false
	case "user", "tool_result":
		// a tool result came back; claude owes the next assistant turn.
		t.awaiting = true
	case "result":
		// the turn is done.
		t.awaiting = false
	}
	// "system" (init, notices), "rate_limit_event", and any other type: activity
	// only. (No sawResult / kill trailer — this slice emits, never kills.)
}

// turnSent records that a user turn's bytes reached the child: claude owes an
// assistant turn from this moment. The third writer of awaiting, alongside
// consumeLine's arms; the contract for when a caller may invoke it lives on
// Watchdog.UserTurnSent.
//
// Stamping lastEvent is load-bearing, not tidiness: an idle child emits nothing,
// so lastEvent may be hours stale when the turn finally lands, and arming alone
// would satisfy shouldFire on the next tick.
func (t *stallTracker) turnSent() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.awaiting = true
	t.lastEvent = t.now()
}

// childExited voids whatever the departed child owed and restarts the idle
// clock, so the tracker meets the respawned child in the state a freshly
// constructed one has. The fourth writer of awaiting.
//
// It deliberately does NOT drop the partial-line remainder. A dead child's
// trailing partial concatenates with the new child's first line, but the result
// is one unparseable line, which consumeLine already treats as activity-only — it
// cannot flip awaiting to a wrong value, and the first line after a spawn is
// `system`/`init`, activity-only anyway. Dropping it would defend a failure mode
// never observed, and the sibling question for the #1088 Parser is still open.
func (t *stallTracker) childExited() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.awaiting = false
	t.lastEvent = t.now()
}

// snapshot returns the current awaiting flag and last-activity time.
func (t *stallTracker) snapshot() (awaiting bool, last time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.awaiting, t.lastEvent
}

// shouldFire is the pure stall predicate: claude owes an assistant turn AND the
// stream has been silent longer than the idle threshold. An in-flight tool run
// (awaiting=false) never fires however long the silence — the type-aware core
// (AC1).
func shouldFire(awaiting bool, last, now time.Time, idle time.Duration) bool {
	return awaiting && now.Sub(last) > idle
}

// WatchdogConfig configures a Watchdog. The zero value is usable except that a
// real watchdog supplies OnStall (a nil OnStall degrades to detection-only).
type WatchdogConfig struct {
	// Idle is the stall threshold: how long the stream may sit silent while
	// claude owes an assistant turn before a stall is emitted. Zero → idleStall
	// (240s).
	Idle time.Duration

	// PendingPermission is the content-free timing hook: it reports whether a
	// permission approval is currently outstanding. The watchdog consults it at
	// fire time and carries its value on the emitted signal — it does NOT gate
	// whether the stall is emitted (both a genuine wedge and an approval-wait
	// emit; the distinction lives in the argument). Nil → treated as always-false
	// (no permission pending — the genuine-wedge path). The hook's producer (the
	// approval flow) is out of scope for this slice (#1079/#1080).
	PendingPermission func() bool

	// OnStall is the stall signal, called on the rising edge of a stall episode
	// (once per episode, on the poll goroutine) with the pending-permission value
	// at fire time. Nil → no-op guard. The watchdog makes no promise beyond
	// "called serially, on the poll goroutine, once per stall episode"; the
	// consumer owns any synchronization it needs. It NEVER kills — this callback
	// is the entire effect of a fire.
	OnStall func(pendingPermission bool)

	// Logger is used for content-free diagnostics only (idle_seconds, the
	// pending_permission bool, byte counts). Nil → slog.Default.
	Logger *slog.Logger

	// now is an unexported clock seam for tests, threaded into both the tracker
	// (lastEvent) and the poll's elapsed check so they never read two different
	// clocks. Nil → time.Now.
	now func() time.Time
}

// Watchdog is the receive-side idle watchdog. It owns a stallTracker (wired as
// Config.Stdout via Writer) and a single poll goroutine that, while claude owes
// an assistant turn, emits a stall signal when the stream sits silent past the
// threshold.
//
// DIVERGENCE from streamrunner's one-shot watchdog, which KILLS on idle stall:
// this watchdog EMITS and NEVER kills. A pending permission approval is unbounded
// owed-silence (T1 spike #1075: claude blocks synchronously until the approval
// tool answers), so killing on awaiting && idle would destroy a legitimately
// blocked turn. Emit-not-kill is enforced STRUCTURALLY, not just by convention:
// the Watchdog holds no context.CancelFunc and no process handle, so it has no
// way to kill — WatchdogConfig has no cancel field to wire one, and no synthetic
// `result` trailer is composed.
//
// SECOND DIVERGENCE from streamrunner (#1504): that runner *is* the send side, so
// it may assume an owed turn the moment its parser is constructed. This watchdog
// is constructed away from the send side and cannot, so the owed-turn fact is
// supplied by UserTurnSent and ChildExited rather than assumed. The asymmetry is
// unavoidable rather than stylistic: claude does not echo the delivered prompt
// back as a `user` line on this surface (measured — see the streamsup feature
// doc), so the stdout stream alone can never tell the watchdog that a turn was
// sent, and a tracker left to arm from stdout would turn today's false positive
// into a silent false negative on the wedge this component exists to catch.
type Watchdog struct {
	cfg     WatchdogConfig
	tracker *stallTracker
	done    chan struct{}
}

// NewWatchdog constructs a Watchdog. Compose Writer() into Config.Stdout, then
// call Start(ctx) once and Wait() at teardown.
func NewWatchdog(cfg WatchdogConfig) *Watchdog {
	if cfg.Idle == 0 {
		cfg.Idle = idleStall
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return &Watchdog{
		cfg:     cfg,
		tracker: newStallTracker(cfg.now, cfg.Logger),
		done:    make(chan struct{}),
	}
}

// Writer returns the stallTracker for Config.Stdout composition. The caller fans
// the child's stdout to both the #1088 Parser and this writer with
// io.MultiWriter (deferred to the wiring slice).
func (w *Watchdog) Writer() io.Writer { return w.tracker }

// UserTurnSent tells the watchdog that a user turn's bytes reached the child's
// stdin: claude owes an assistant turn, and the idle threshold counts from this
// call rather than from the child's last output (which on an idle session is
// arbitrarily stale).
//
// It means bytes actually DELIVERED, so call it only after a Runner.WriteUserTurn
// that returned nil. That method's two refusals write zero bytes — ErrNoLiveChild
// (no live child, or a BeginRotation gate) and turncommit.ErrDropped (gate deny) —
// and a refused turn owes nothing, so arming on one reintroduces the same false
// stall by another route. The method takes no argument and returns nothing
// precisely so that decision stays at the one call site that can see the error
// value.
//
// Control requests do NOT arm it. Runner.Interrupt and Runner.RevokeBypass write
// `control_request` lines that claude acks in ~40ms (#1075/#1595) and that are not
// user turns; no wedge has been observed on that path. The trigger is "a user turn
// was delivered", not "a write happened".
//
// Call it on the writing goroutine after the write returns. Arming first and
// unwinding on error would leave an armed tracker behind every error path — the
// failure this signal removes. Arming after opens a nanosecond-width reorder in
// which claude's first output could clear awaiting before this sets it; that is
// bounded (microseconds after the write syscall, against claude's tens of
// milliseconds to first output) and self-healing (the next `assistant` or `result`
// line clears awaiting again), so it is named here rather than mechanised.
//
// Safe from any goroutine: it takes only the tracker's leaf mutex, never logs and
// never calls out, so a caller may hold its own locks across it.
func (w *Watchdog) UserTurnSent() { w.tracker.turnSent() }

// ChildExited tells the watchdog that the child it was watching is gone: whatever
// that child owed is void, so a turn abandoned by a crash mid-turn cannot make the
// respawned child's healthy silence look like a wedge. The respawned child is met
// in the state a freshly constructed tracker has — not awaiting, idle clock
// restarted — which is the whole of the reset.
//
// The watchdog itself survives the respawn: one watchdog per runner, not one per
// child, because Config.Stdout is fixed when the Runner is built and the supervise
// loop never re-wires it, so a per-child watchdog could not reach the new child's
// stdout. That also keeps exactly one poll goroutine and one Start/Wait pair for
// the runner's life, which is the contract Start documents.
//
// Same locking properties as UserTurnSent: leaf mutex only, safe from any
// goroutine.
func (w *Watchdog) ChildExited() { w.tracker.childExited() }

// Start launches the single poll goroutine. Call exactly once. The goroutine
// ticks at watchdogTickFor(Idle); on each tick it snapshots the tracker and, on
// the rising edge of a stall (edge-triggered — latched once per episode, re-arms
// when activity or an awaiting flip clears the condition), evaluates the
// pending-permission hook, logs a content-free Warn, and calls OnStall. It never
// kills and never returns early after firing — it keeps polling until ctx is
// cancelled, which is what lets it tolerate an unbounded pending-permission
// window. On ctx.Done it returns and closes done; Wait joins.
func (w *Watchdog) Start(ctx context.Context) {
	tick := watchdogTickFor(w.cfg.Idle)
	go func() {
		defer close(w.done)
		t := time.NewTicker(tick)
		defer t.Stop()
		fired := false // edge-trigger latch: one emit per contiguous stall episode
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				awaiting, last := w.tracker.snapshot()
				if !shouldFire(awaiting, last, w.cfg.now(), w.cfg.Idle) {
					// Activity advanced lastEvent, or awaiting flipped false —
					// re-arm for the next episode.
					fired = false
					continue
				}
				if fired {
					continue
				}
				fired = true
				pending := w.cfg.PendingPermission != nil && w.cfg.PendingPermission()
				w.cfg.Logger.Warn("streamsup: idle stream stall — emitting stall signal (not killing)",
					"idle_seconds", int(w.cfg.Idle.Seconds()),
					"pending_permission", pending)
				if w.cfg.OnStall != nil {
					w.cfg.OnStall(pending)
				}
			}
		}
	}()
}

// Wait blocks until the poll goroutine has exited (after Start's ctx is
// cancelled). Call after Start.
func (w *Watchdog) Wait() { <-w.done }
