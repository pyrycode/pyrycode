package sessions

import "time"

// TransitionReason is an internal/sessions-local vocabulary for a session
// lifecycle transition. It is deliberately NOT protocol's wire reason — this
// package must not import internal/protocol (import cycle). The cmd/pyry
// consumer (#657) maps it onto the wire {clear, idle_evict, workspace_change}.
type TransitionReason string

const (
	// ReasonClear is a /clear rotation: the session's id changed in place.
	ReasonClear TransitionReason = "clear"
	// ReasonEviction is an eviction (idle timeout OR cap policy — the two are
	// collapsed; #657 maps both onto the wire "idle_evict"). There is no
	// successor session id.
	ReasonEviction TransitionReason = "eviction"
)

// SessionTransition is one observed lifecycle transition. NewID is empty for
// eviction (no successor session). OccurredAt is stamped by internal/sessions
// at the moment the transition fires.
type SessionTransition struct {
	PreviousID SessionID
	NewID      SessionID
	Reason     TransitionReason
	OccurredAt time.Time
}

// TransitionObserver is notified of clear/eviction transitions. It is invoked
// SYNCHRONOUSLY from the goroutine that owns the transition (the lifecycle
// goroutine for eviction, the rotation-watcher goroutine for clear) with NO
// session or pool lock held. The implementation MUST NOT block — hand the
// signal off to a buffered channel and return. A nil observer is disabled.
type TransitionObserver func(SessionTransition)

// SetTransitionObserver installs the pool's transition observer. It must be
// called before Pool.Run: the field is then read-only, and the concurrent
// reads from the lifecycle and watcher goroutines (both spawned by Run) are
// race-free via Run's goroutine-creation happens-before edge. Calling it after
// Run has started is a programming error the race detector will flag. A nil
// observer (the zero value, or an explicit nil) disables signalling.
func (p *Pool) SetTransitionObserver(obs TransitionObserver) {
	p.transitionObserver = obs
}

// notifyTransition invokes the observer if one is wired. Takes no lock and is
// always called with no Pool.mu/Session.lcMu held (a leaf, off-lock callback —
// see docs/lessons.md "Lock order with callback into the host").
func (p *Pool) notifyTransition(t SessionTransition) {
	if p.transitionObserver != nil {
		p.transitionObserver(t)
	}
}

// onRotate performs a /clear rotation and, on success, fires a ReasonClear
// transition. It is the clear surfacing seam wired into Pool.Run's rotation
// watcher (the OnRotate callback). The RotateID error is returned verbatim and
// no signal fires on the error path — a failed/no-op rotation emits nothing
// (the watcher already logs and continues on an OnRotate error).
func (p *Pool) onRotate(oldID, newID SessionID) error {
	if err := p.RotateID(oldID, newID); err != nil {
		return err
	}
	p.notifyTransition(SessionTransition{
		PreviousID: oldID,
		NewID:      newID,
		Reason:     ReasonClear,
		OccurredAt: time.Now().UTC(),
	})
	return nil
}
