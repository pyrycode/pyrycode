package sessions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

// Session is one supervised claude instance plus the bridge that mediates its
// I/O in service mode. As of 1.2c-A each Session owns a lifecycle goroutine
// (the body of Run) that drives the active↔evicted state machine.
type Session struct {
	id     SessionID
	sup    *supervisor.Supervisor
	bridge *supervisor.Bridge // nil in foreground mode
	log    *slog.Logger

	// Persisted metadata. label/createdAt/bootstrap are immutable post-New
	// from the lifecycle goroutine's perspective. lastActiveAt is bumped
	// under lcMu on every state transition.
	label     string
	createdAt time.Time
	bootstrap bool

	// pool is the back-pointer used to persist registry changes after a
	// state transition. Set once, in Pool.New.
	pool *Pool

	// idleTimeout is the eviction window. 0 disables eviction entirely
	// (test default and operator escape hatch).
	idleTimeout time.Duration

	// Lifecycle state, attach bookkeeping, and Activate signalling.
	// lcMu protects all fields below it.
	lcMu         sync.Mutex
	lcState      lifecycleState
	attached     int           // number of currently-bound bridge clients
	activeCh     chan struct{} // closed when stateActive; replaced when stateEvicted
	activateCh   chan struct{} // buffered(1); Activate sends, runEvicted reads
	lastActiveAt time.Time
}

// ID returns the session's stable identifier.
func (s *Session) ID() SessionID { return s.id }

// State returns a snapshot of the supervisor's runtime state. Pure delegation
// to (*supervisor.Supervisor).State. Note: in stateEvicted, the supervisor's
// phase is PhaseStopped — that is faithful, since the supervisor really
// isn't running.
func (s *Session) State() supervisor.State { return s.sup.State() }

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

// Activate moves the session into stateActive if it is currently evicted,
// blocking until the lifecycle goroutine has started the supervisor (or ctx
// is cancelled). No-op when the session is already active. Safe from any
// goroutine; idempotent under concurrent calls.
func (s *Session) Activate(ctx context.Context) error {
	s.lcMu.Lock()
	if s.lcState == stateActive {
		s.lcMu.Unlock()
		return nil
	}
	ch := s.activeCh
	// Buffered(1) — concurrent Activates collapse to one signal; the
	// lifecycle goroutine drains it once when leaving runEvicted, then the
	// shared activeCh wakeup picks up any extra waiters.
	select {
	case s.activateCh <- struct{}{}:
	default:
	}
	s.lcMu.Unlock()

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run blocks until ctx is cancelled, driving the session's active↔evicted
// state machine. The body alternates between runActive (supervisor running,
// idle timer armed) and runEvicted (no supervisor; waiting for an Activate).
// A registry write happens after each transition.
func (s *Session) Run(ctx context.Context) error {
	for {
		switch s.snapshotState() {
		case stateActive:
			if err := s.runActive(ctx); err != nil {
				return err
			}
			if err := s.transitionTo(stateEvicted); err != nil {
				return fmt.Errorf("persist evicted: %w", err)
			}
		case stateEvicted:
			if err := s.runEvicted(ctx); err != nil {
				return err
			}
			if err := s.transitionTo(stateActive); err != nil {
				return fmt.Errorf("persist active: %w", err)
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

// runActive supervises the session while it is active: spawns the supervisor
// on an inner ctx, arms the idle timer, and returns when one of:
//   - outer ctx cancels → returns ctx.Err() (terminal; outer Run propagates)
//   - supervisor exits spontaneously → returns nil (loop will evict)
//   - idle timer fires AND attached==0 → returns nil (loop will evict)
//
// While attached>0, idle eviction is deferred (poll-with-grace: re-arm on
// fire — eviction may overshoot the configured timeout by up to one window).
// A zero idleTimeout disables the timer entirely.
func (s *Session) runActive(ctx context.Context) error {
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
			return ctx.Err()
		case <-runErr:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Supervisor exited on its own. Today this is largely
			// defensive — supervisor.Run only returns on ctx cancel —
			// but treating it as an evict trigger keeps the lifecycle
			// loop consistent if that contract ever loosens.
			return nil
		case <-timerCh:
			s.lcMu.Lock()
			attached := s.attached
			s.lcMu.Unlock()
			if attached > 0 {
				timer.Reset(s.idleTimeout)
				continue
			}
			cancelSup()
			drainSup()
			return nil
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
	case <-s.activateCh:
		return nil
	}
}

// transitionTo flips lcState to newState, bumps lastActiveAt, swaps the
// activeCh as appropriate, and persists the registry. Lock order: this
// function releases lcMu before calling pool.persist so saveLocked's
// per-session lcMu re-acquire doesn't deadlock against us.
func (s *Session) transitionTo(newState lifecycleState) error {
	s.lcMu.Lock()
	s.lcState = newState
	s.lastActiveAt = time.Now().UTC()
	switch newState {
	case stateActive:
		// Wake any Activate waiters. Closing twice would panic; we only
		// close when entering active from evicted, and runEvicted's
		// channel is fresh.
		close(s.activeCh)
	case stateEvicted:
		// Fresh open channel for the next Activate to wait on.
		s.activeCh = make(chan struct{})
	}
	s.lcMu.Unlock()
	if s.pool == nil {
		return nil
	}
	return s.pool.persist()
}
