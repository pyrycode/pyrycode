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
// The #1088 Parser is deliberately turn-stateless and holds no awaiting flag, so
// this tracker carries its own type-tracking state over the same stream. Unlike
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
// and log for content-free Debug diagnostics (nil → slog.Default). It starts in
// the awaiting state because the caller writes the opening user envelope to
// claude's stdin before the child produces any output — claude owes the first
// assistant turn (matches streamrunner's newStreamParser).
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
		awaiting:  true,
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
