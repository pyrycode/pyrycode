package sessions

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// ErrAttachUnavailable is returned by Session.Attach when the session has no
// bridge (foreground mode). The control plane maps this back to the existing
// "daemon may be in foreground mode" wire string for byte-identical client
// output.
var ErrAttachUnavailable = errors.New("sessions: attach unavailable (no bridge)")

// lifecycleState is the per-session two-state machine introduced in 1.2c-A:
// active (claude is, or should be, running) and evicted (no claude process;
// JSONL on disk is frozen and can be reattached on demand).
type lifecycleState uint8

const (
	stateActive  lifecycleState = iota // claude is (or should be) running
	stateEvicted                       // claude exited; JSONL is on disk
)

// String returns the on-disk encoding for a lifecycleState. Used by
// Pool.saveLocked when serializing the registry.
func (s lifecycleState) String() string {
	switch s {
	case stateEvicted:
		return "evicted"
	default:
		return "active"
	}
}

// parseLifecycleState maps the on-disk string back to its in-memory enum.
// Empty input or any unrecognised value defaults to stateActive — old pyry
// binaries write no lifecycle_state field, and unknown future values are
// treated as the conservative "session is live" default.
func parseLifecycleState(s string) lifecycleState {
	if s == "evicted" {
		return stateEvicted
	}
	return stateActive
}

// closedChan returns a chan that is already closed. Used as the initial
// activeCh for sessions that warm-start in stateActive (most of the time);
// Activate's wait on the channel returns immediately.
func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// SessionSettings is the per-session model / reasoning-effort / bypass-permissions
// triple persisted in the registry (#833) and applied to the claude spawn argv.
// The zero value inherits the daemon template for Model/Effort and enforces
// permissions (YOLO off) — the fail-safe default.
//
// Set initially in Pool.New (bootstrap) or Pool.buildSession (minted) and
// mutated post-construction by Pool.UpdateSettings (#840) under Pool.mu (write);
// read under Pool.mu — the same discipline Pool.Rename uses for label.
type SessionSettings struct {
	Model  string
	Effort string
	YOLO   bool
}

// SettingsUpdate is a partial change to a session's SessionSettings. A nil
// field means "leave the stored value untouched"; a non-nil field sets that
// value — including "" for Model/Effort and false for YOLO, which are thereby
// distinguishable from omitted. It is the presence contract shared with the v2
// settings verb (#841), which decodes the wire payload into it.
//
// YOLO is a *bool for the fail-safe: an omitted (nil) YOLO can never enable
// bypass — only an explicit non-nil *true turns --dangerously-skip-permissions
// on, and an explicit *false turns it off.
type SettingsUpdate struct {
	Model  *string
	Effort *string
	YOLO   *bool
}

// claudeSettingsArgs returns the extra claude flags implied by s, in a
// deterministic order (model, effort, bypass) for testability. Empty Model or
// Effort emits no flag (inherit the template). YOLO==true appends
// --dangerously-skip-permissions; YOLO==false appends nothing — the absence of
// the flag is what enforces permissions. The function never emits a
// permission-disabling flag, so a zero value yields nil and callers append
// nothing (byte-identical argv).
func claudeSettingsArgs(s SessionSettings) []string {
	var args []string
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	if s.Effort != "" {
		args = append(args, "--effort", s.Effort)
	}
	if s.YOLO {
		args = append(args, "--dangerously-skip-permissions")
	}
	return args
}

// spawnArgs composes the full claude spawn argv for the given settings: the
// settings-free base (spawnBase) plus claudeSettingsArgs(settings). It is the
// only argv-recompose path outside session construction — the live restart in
// Pool.UpdateSettings (#842) — and, like construction, routes the settings
// suffix through claudeSettingsArgs, so the YOLO fail-safe is enforced in
// exactly one place. Returns a fresh slice that aliases neither spawnBase nor
// the caller's state; a zero-value settings appends nothing (byte-identical to
// the base).
func (s *Session) spawnArgs(settings SessionSettings) []string {
	return append(slices.Clone(s.spawnBase), claudeSettingsArgs(settings)...)
}

// Session is one supervised claude instance plus the bridge that mediates its
// I/O in service mode. As of 1.2c-A each Session owns a lifecycle goroutine
// (the body of Run) that drives the active↔evicted state machine.
type Session struct {
	// id is the session's stable identifier. Guarded by lcMu: written by
	// Pool.RotateID under BOTH Pool.mu (W) and lcMu on a /clear rotation (#866);
	// read off the lifecycle goroutine via currentID(), or directly by
	// Pool.mu-holders (List, ResolveID, Snapshot, saveLocked, Activate).
	id     SessionID
	sup    *supervisor.Supervisor
	bridge *supervisor.Bridge // nil in foreground mode
	log    *slog.Logger

	// Persisted metadata. createdAt and bootstrap are immutable post-New.
	// label is immutable from the lifecycle goroutine's perspective but may
	// be mutated by Pool.Rename under Pool.mu (write); other readers hold
	// Pool.mu (RLock or Lock). lastActiveAt is bumped under lcMu on every
	// state transition.
	label     string
	createdAt time.Time
	bootstrap bool

	// settings holds the per-session model / effort / YOLO applied to the
	// claude spawn argv (#833). Set in Pool.New (bootstrap) or
	// Pool.buildSession (minted) and mutated by Pool.UpdateSettings (#840)
	// under Pool.mu (write); read under Pool.mu by saveLocked (same discipline
	// as label, NOT under lcMu).
	settings SessionSettings

	// spawnBase is the settings-free claude spawn argv: the template args plus
	// any construction-time resume suffix (--session-id <id> for a minted
	// session), but WITHOUT the claudeSettingsArgs suffix. spawnArgs recomposes
	// the full argv from this base plus the live settings when a settings change
	// triggers a live restart (#842). Immutable post-construction, so it is read
	// without a lock. It never contains a YOLO-derived flag — the bypass flag has
	// exactly one origin, claudeSettingsArgs — so no persisted-false state can
	// recompose into a --dangerously-skip-permissions child.
	spawnBase []string

	// pool is the back-pointer used to persist registry changes after a
	// state transition. Set once, in Pool.New.
	pool *Pool

	// idleTimeout is the eviction window. 0 disables eviction entirely
	// (test default and operator escape hatch).
	idleTimeout time.Duration

	// removedCh is closed exactly once by Pool.Remove, after the registry
	// remove commits. A closed removedCh tells the lifecycle goroutine to
	// exit its Run loop cleanly (return nil) instead of re-parking in
	// runEvicted. Write-once: allocated at construction, never reallocated
	// (unlike activeCh/evictedCh, which swap under lcMu). Readers select/read
	// it WITHOUT lcMu — it is therefore deliberately outside the lcMu-guarded
	// block below. A nil channel is a valid "never removed" state (a nil
	// channel is a never-ready select case), used by any test-constructed
	// Session that hand-builds a literal.
	removedCh chan struct{}

	// Lifecycle state, attach bookkeeping, and Activate/Evict signalling.
	// lcMu protects all fields below it.
	lcMu         sync.Mutex
	lcState      lifecycleState
	attached     int           // number of currently-bound bridge clients
	activeCh     chan struct{} // closed when stateActive; replaced when stateEvicted
	evictedCh    chan struct{} // closed when stateEvicted; replaced when stateActive
	activateCh   chan struct{} // buffered(1); Activate sends, runEvicted reads
	evictCh      chan struct{} // buffered(1); Evict sends, runActive reads
	lastActiveAt time.Time
}

// ID returns the session's stable identifier.
func (s *Session) ID() SessionID { return s.currentID() }

// currentID returns s.id under s.lcMu. sess.id is written by Pool.RotateID
// under both Pool.mu (W) and lcMu (#866); a read is race-clean while holding
// either. Lifecycle-goroutine readers (those NOT holding Pool.mu) MUST route
// id reads through this helper; Pool.mu-holders read s.id directly.
func (s *Session) currentID() SessionID {
	s.lcMu.Lock()
	defer s.lcMu.Unlock()
	return s.id
}

// State returns a snapshot of the supervisor's runtime state. Pure delegation
// to (*supervisor.Supervisor).State. Note: in stateEvicted, the supervisor's
// phase is PhaseStopped — that is faithful, since the supervisor really
// isn't running.
func (s *Session) State() supervisor.State { return s.sup.State() }

// WriteUserTurn delegates to the underlying supervisor. Consumed by the
// send_message handler via the handlers.TurnWriter interface. ctx bounds the
// supervisor's ready-gate + commit-confirm delivery; the handler passes a
// timeout-bounded ctx so a busy/wedged claude surfaces as a loud failure
// rather than hanging the per-conn goroutine.
func (s *Session) WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error {
	return s.sup.WriteUserTurn(ctx, conversationID, payload)
}

// Supervisor exposes the underlying supervisor handle. Consumed by the
// assistant-turn bridge in cmd/pyry to read CurrentConversation() at
// broadcast time. Returned pointer is owned by the session; callers must
// not retain it past the session's lifetime.
func (s *Session) Supervisor() *supervisor.Supervisor { return s.sup }

// Bridge exposes the underlying I/O bridge, or nil in foreground mode.
// Consumed by the assistant-turn bridge in cmd/pyry to register an output
// observer on the PTY-drain path.
func (s *Session) Bridge() *supervisor.Bridge { return s.bridge }

// LifecycleState returns a snapshot of the current lifecycle state. Used by
// tests and (eventually) status payloads. Safe from any goroutine.
func (s *Session) LifecycleState() lifecycleState {
	s.lcMu.Lock()
	defer s.lcMu.Unlock()
	return s.lcState
}

// Attach binds a client to this session's bridge. Returns ErrAttachUnavailable
// when the session has no bridge (foreground mode). Otherwise delegates to
// (*supervisor.Bridge).Attach, propagating supervisor.ErrBridgeBusy verbatim.
//
// Bookkeeping: a successful attach increments `attached`; the wrapper
// goroutine spawned here decrements it when the bridge's done channel fires.
// While `attached > 0` the idle timer's eviction is deferred (see runActive).
//
// Contract: callers must Activate the session before Attach. An Attach on an
// evicted session would block on the bridge's pipe forever, since no claude
// is running to drain it. The control plane is the only attach caller and
// always Activates first.
func (s *Session) Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error) {
	if s.bridge == nil {
		return nil, ErrAttachUnavailable
	}
	s.lcMu.Lock()
	s.attached++
	s.lcMu.Unlock()

	bridgeDone, err := s.bridge.Attach(in, out)
	if err != nil {
		s.lcMu.Lock()
		s.attached--
		s.lcMu.Unlock()
		return nil, err
	}

	wrapped := make(chan struct{})
	go func() {
		<-bridgeDone
		s.lcMu.Lock()
		s.attached--
		s.lcMu.Unlock()
		close(wrapped)
	}()
	return wrapped, nil
}

// Resize applies the given window size to the session's PTY via the bridge.
// Returns ErrAttachUnavailable when the session has no bridge (foreground
// mode); the control plane's attach handler swallows that case since
// foreground mode has its own SIGWINCH watcher.
//
// rows-then-cols matches Bridge.Resize and pty.Winsize. No lifecycle locking:
// Resize doesn't bump lastActiveAt or interact with the active↔evicted state
// machine. The bridge's own ptyMu serializes against iteration boundaries.
func (s *Session) Resize(rows, cols uint16) error {
	if s.bridge == nil {
		return ErrAttachUnavailable
	}
	return s.bridge.Resize(rows, cols)
}

// Activate moves the session into stateActive if it is currently evicted,
// blocking until the lifecycle goroutine has started the supervisor AND the
// post-transition registry persist has completed AND the supervisor has
// bound its PTY (or ctx is cancelled). Safe from any goroutine; idempotent
// under concurrent calls.
//
// No early-return for "already active" — callers always wait on activeCh.
// When the session is fully active and persisted, activeCh is already closed
// and the receive returns immediately. When a transition is in flight (state
// flipped, persist still running), the receive correctly blocks until the
// persist completes and transitionTo closes activeCh.
//
// PTY-readiness wait: after the state flip, runOnce takes a brief window
// (~hundreds of ms) to allocate the PTY master and call setPTY. Activate
// waits past that window via supervisor.WaitForPTY so callers that follow
// Activate with WriteUserTurn/Resize observe a live PTY rather than the
// silent-drop-on-nil branch. The relay-routed send_message path depends
// on this guarantee (#396).
func (s *Session) Activate(ctx context.Context) error {
	s.lcMu.Lock()
	ch := s.activeCh
	if s.lcState != stateActive {
		// Buffered(1) — concurrent Activates collapse to one signal; the
		// lifecycle goroutine drains it once when leaving runEvicted, then
		// the shared activeCh wakeup picks up any extra waiters.
		select {
		case s.activateCh <- struct{}{}:
		default:
		}
	}
	s.lcMu.Unlock()

	select {
	case <-ch:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.sup.WaitForPTY(ctx)
}

// Evict moves the session into stateEvicted if it is currently active,
// blocking until the lifecycle goroutine has stopped the supervisor (or ctx
// is cancelled). No-op when the session is already evicted. Safe from any
// goroutine; idempotent under concurrent calls.
//
// Used by the cap-policy spawn path (Phase 1.2c-B): when activating one more
// session would exceed Pool.activeCap, the LRU peer is evicted via this
// primitive before the new spawn proceeds. Force-eviction — unlike the idle
// timer, it does not defer for attached>0. The cap is a hard limit; an
// attached caller will see EOF on its bridge.
func (s *Session) Evict(ctx context.Context) error {
	s.lcMu.Lock()
	ch := s.evictedCh
	if s.lcState != stateEvicted {
		select {
		case s.evictCh <- struct{}{}:
		default:
		}
	}
	s.lcMu.Unlock()

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// touchLastActive bumps lastActiveAt to time.Now().UTC() under lcMu. Called
// by the cap-policy spawn path on an Activate against an already-active
// session so LRU ordering reflects the most recent touch. Not persisted —
// the registry's lastActiveAt is only flushed on state transitions.
func (s *Session) touchLastActive() {
	s.lcMu.Lock()
	s.lastActiveAt = time.Now().UTC()
	s.lcMu.Unlock()
}

// Run blocks until ctx is cancelled, driving the session's active↔evicted
// state machine. The body alternates between runActive (supervisor running,
// idle timer armed) and runEvicted (no supervisor; waiting for an Activate).
// A registry write happens after each transition.
func (s *Session) Run(ctx context.Context) error {
	// On permanent termination — outer ctx cancel (pool shutdown) or Pool.Remove
	// — release any attach input pump parked on the bridge's buffered send so
	// the daemon exits promptly instead of hanging until SIGKILL (#863). This is
	// the only layer that both holds the Bridge and can distinguish shutdown
	// from eviction: eviction stays INSIDE the loop below (runActive→runEvicted),
	// so this defer fires exactly once, only when Run returns for good. Placing
	// it in supervisor.Run would poison the bridge on every evict (supervisor.Run
	// returns on eviction too). The Server layer closes the attached conn to
	// unblock a read-parked pump; Shutdown covers the send-parked pump a conn
	// close cannot reach. Guarded because foreground sessions have no bridge.
	if s.bridge != nil {
		defer s.bridge.Shutdown()
	}
	for {
		switch s.snapshotState() {
		case stateActive:
			reason, err := s.runActive(ctx)
			if err != nil {
				return err
			}
			if err := s.transitionTo(stateEvicted); err != nil {
				// A registry-persist failure on a lifecycle transition is
				// NON-FATAL. transitionTo already advanced the in-memory state
				// and woke waiters before persisting, so memory is
				// authoritative and the next transition re-persists the whole
				// registry (self-healing). Returning here would propagate
				// through the pool's shared error group and tear down EVERY
				// live session and the relay leg over one disk hiccup during a
				// routine idle eviction — a blast radius grossly out of
				// proportion to the trigger.
				s.log.Warn("session: registry persist failed on evict; keeping in-memory state, retrying next transition",
					"event", "session.persist_failed",
					"transition", "evicted",
					"err", err)
			}
			// Fire the eviction signal AFTER transitionTo (post-persist, no
			// lcMu held — the leaf, off-lock callback point). reason == ""
			// is the defensive spontaneous-exit path (<-runErr): the wire has
			// no "crashed" reason, so it signals nothing. s.pool != nil
			// mirrors transitionTo's guard for test-constructed sessions.
			if reason != "" && s.pool != nil {
				s.pool.notifyTransition(SessionTransition{
					PreviousID: s.currentID(),
					Reason:     reason,
					OccurredAt: time.Now().UTC(),
				})
			}
		case stateEvicted:
			if err := s.runEvicted(ctx); err != nil {
				return err
			}
			if s.isRemoved() {
				// Pool.Remove signalled removal: exit the lifecycle goroutine
				// with nil so the pool's shared errgroup does NOT cancel gctx
				// and tear down every other session plus the relay leg. Only a
				// genuine ctx.Done() (pool shutdown) returns ctx.Err() above.
				return nil
			}
			if err := s.transitionTo(stateActive); err != nil {
				// Non-fatal, same reasoning as the evict transition above:
				// memory is already authoritative, the next transition
				// re-persists. Do not tear down the daemon over a persist I/O
				// error on a routine re-activation.
				s.log.Warn("session: registry persist failed on activate; keeping in-memory state, retrying next transition",
					"event", "session.persist_failed",
					"transition", "active",
					"err", err)
			}
		}
	}
}

// snapshotState returns the current lifecycle state under lcMu.
func (s *Session) snapshotState() lifecycleState {
	s.lcMu.Lock()
	defer s.lcMu.Unlock()
	return s.lcState
}

// isRemoved reports whether Pool.Remove has closed removedCh. Non-blocking; no
// lcMu (removedCh is write-once and never reopens, so the read is race-free and
// deterministic). A nil removedCh — a never-removed test literal — is a
// never-ready channel, so the default arm returns false.
func (s *Session) isRemoved() bool {
	select {
	case <-s.removedCh:
		return true
	default:
		return false
	}
}

// runActive supervises the session while it is active: spawns the supervisor
// on an inner ctx, arms the idle timer, and returns when one of:
//   - outer ctx cancels → returns ("", ctx.Err()) (terminal; outer Run propagates)
//   - supervisor exits spontaneously → returns ("", nil) (loop will evict; no
//     signal — the wire has no "crashed" reason)
//   - idle timer fires AND attached==0 → returns (ReasonEviction, nil)
//   - cap-policy evict signal → returns (ReasonEviction, nil)
//
// The first (non-empty) return value is the transition reason Run fires to the
// pool's observer once the eviction is persisted; an empty reason fires
// nothing. While attached>0, idle eviction is deferred (poll-with-grace:
// re-arm on fire — eviction may overshoot the configured timeout by up to one
// window). A zero idleTimeout disables the timer entirely.
func (s *Session) runActive(ctx context.Context) (TransitionReason, error) {
	subCtx, cancelSup := context.WithCancel(ctx)
	defer cancelSup()

	runErr := make(chan error, 1)
	go func() { runErr <- s.sup.Run(subCtx) }()
	drainSup := func() { <-runErr }

	// nil channel never selects — used as the timer placeholder when
	// idleTimeout is zero (eviction disabled).
	var timerCh <-chan time.Time
	var timer *time.Timer
	if s.idleTimeout > 0 {
		timer = time.NewTimer(s.idleTimeout)
		defer timer.Stop()
		timerCh = timer.C
	}

	for {
		select {
		case <-ctx.Done():
			cancelSup()
			drainSup()
			return "", ctx.Err()
		case <-runErr:
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			// Supervisor exited on its own. Today this is largely
			// defensive — supervisor.Run only returns on ctx cancel —
			// but treating it as an evict trigger keeps the lifecycle
			// loop consistent if that contract ever loosens. No reason
			// is surfaced: the wire has no "crashed" transition, so Run
			// fires no signal on this path.
			return "", nil
		case <-timerCh:
			s.lcMu.Lock()
			attached := s.attached
			s.lcMu.Unlock()
			if attached > 0 {
				timer.Reset(s.idleTimeout)
				continue
			}
			// SIGKILL-cause record: pairs with the supervisor-level
			// "claude exited" line that follows. Operators reading logs
			// after an idle eviction see this WARN first and don't have
			// to correlate the generic "signal: killed" exit with the
			// configured idle window. #396 added this signal so a
			// supervision-incomplete state has an explicit log line.
			s.log.Warn("session: idle eviction firing",
				"event", "session.idle_eviction",
				"session_id", string(s.currentID()),
				"idle_timeout", s.idleTimeout,
				"bootstrap", s.bootstrap)
			cancelSup()
			drainSup()
			return ReasonEviction, nil
		case <-s.evictCh:
			// Cap-policy eviction: forced, regardless of attached count.
			cancelSup()
			drainSup()
			return ReasonEviction, nil
		}
	}
}

// runEvicted blocks until either ctx is cancelled (terminal, returns ctx.Err)
// or an Activate call signals on activateCh (returns nil; loop transitions
// back to active). No supervisor is running while we sit here.
func (s *Session) runEvicted(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.removedCh:
		// Pool.Remove closed removedCh. Return nil; Run's isRemoved() check
		// turns this into a clean exit (no errgroup error). A concurrent
		// activateCh signal loses: Run re-checks isRemoved() before
		// re-activating, so a removed session can never resurrect.
		return nil
	case <-s.activateCh:
		return nil
	}
}

// transitionTo flips lcState to newState, bumps lastActiveAt, allocates the
// fresh wake channel for the *opposite* direction, persists the registry,
// then closes the wake channel for the current direction. The persist runs
// between the state flip and the wake so that any Activate/Evict waiter that
// observes the wake also sees a registry on disk consistent with newState.
//
// Lock order: lcMu is released before calling pool.persist so saveLocked's
// per-session lcMu re-acquire doesn't deadlock against us. lcMu is then
// re-acquired (after persist returns and releases Pool.mu) only to serialise
// the close against any concurrent Activate/Evict capturing the channel
// reference. The two lcMu acquisitions are sequential, not nested.
//
// On persist failure the wake channel still closes — a permanently-stuck
// waiter is a worse failure mode than a waiter that wakes to stale disk.
// The persist error propagates up Run, which treats it as fatal.
func (s *Session) transitionTo(newState lifecycleState) error {
	s.lcMu.Lock()
	s.lcState = newState
	s.lastActiveAt = time.Now().UTC()
	switch newState {
	case stateActive:
		// Fresh open channel for the next Evict to wait on. The current
		// direction's activeCh is left open here; closed below after persist.
		s.evictedCh = make(chan struct{})
	case stateEvicted:
		// Fresh open channel for the next Activate to wait on. The current
		// direction's evictedCh is left open here; closed below after persist.
		s.activeCh = make(chan struct{})
	}
	s.lcMu.Unlock()

	var persistErr error
	if s.pool != nil {
		persistErr = s.pool.persist()
	}

	// Wake waiters under lcMu to order the close against any concurrent
	// Activate/Evict capturing the channel reference. Single-shot per
	// direction: transitionTo is called only by the lifecycle goroutine, and
	// Run alternates directions, so each close fires exactly once per fresh
	// channel.
	s.lcMu.Lock()
	switch newState {
	case stateActive:
		close(s.activeCh)
	case stateEvicted:
		close(s.evictedCh)
	}
	s.lcMu.Unlock()

	return persistErr
}
