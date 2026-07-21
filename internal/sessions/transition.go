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
//
// A /clear rotation changed the session id in place, so the owning
// conversation's binding is re-pointed BEFORE the observer fan-out: the
// downstream consumer (#741) resolves session→conversation against the CURRENT
// binding, and driving the rebind ahead of the hand-off makes that ordering
// structural. Eviction keeps its id (NewID == ""), is binding-neutral, and
// skips this branch entirely (AC#2).
func (p *Pool) notifyTransition(t SessionTransition) {
	if t.Reason == ReasonClear {
		p.rebindConversation(t.PreviousID, t.NewID)
	}
	if p.transitionObserver != nil {
		p.transitionObserver(t)
	}
}

// rebindConversation maintains the conversation↔session binding after a /clear
// rotation re-keyed a session (oldID → newID). It is a no-op when no registry
// is wired (test pools, p.convReg == nil) or when no conversation owns oldID
// (AC#4 — Save is skipped so the file mtime stays stable). On a successful
// rebind it persists conversations.json via the registry's atomic Save; a Save
// error is logged at Warn and swallowed — the in-memory rebind is already
// applied and usable, so durability is best-effort, matching
// create_conversation's eager persist and RotateID's non-fatal save.
func (p *Pool) rebindConversation(oldID, newID SessionID) {
	if p.convReg == nil {
		return
	}
	if !p.convReg.RebindSession(string(oldID), string(newID)) {
		return
	}
	if err := p.convReg.Save(p.convRegistryPath); err != nil {
		p.log.Warn("sessions: rebind conversation persist failed",
			"event", "rebind_conversation.persist_failed",
			"session_id", string(newID),
			"previous_session_id", string(oldID),
			"err", err)
	}
}

// RotateForNewSession rotates the session keyed by oldID to a fresh
// daemon-minted id and returns the new id for the caller to feed to
// (*streamsup.Runner).RestartFresh. It is the DIRECT (new_session) analog of
// onRotate: it re-keys the pool entry, registers the new id in the allocated
// skip-set, rebinds the owning conversation, and fires a ReasonClear transition
// so the client sees the fresh-session break — but it MINTS the id and drives
// the rotation itself rather than observing claude's self-rotation.
//
// The skip-set registration is the key asymmetry vs onRotate. In onRotate claude
// already created <newID>.jsonl and the id is deliberately UN-allocated (that is
// how the watcher detects a real self-rotation). Here WE are about to spawn
// claude --session-id <newID> via RestartFresh, so <newID>.jsonl will CREATE-fire
// the watcher; the id MUST be in the skip-set or the watcher double-rotates.
// Registering upholds the RegisterAllocatedUUID invariant that GetOrCreate
// already obeys for caller-supplied --session-id mints.
//
// Errors: a crypto/rand mint failure, or an absent oldID (TOCTOU: the binding may
// vanish between the caller's resolve and this call), returns ("", err) with no
// mutation and no transition. A saveLocked failure is logged at Warn and
// swallowed — the in-memory rotation + rebind are already authoritative, matching
// rebindConversation's / RotateID's best-effort-durability posture. The
// notifyTransition fan-out runs off Pool.mu, the established leaf-callback
// discipline.
func (p *Pool) RotateForNewSession(oldID SessionID) (SessionID, error) {
	newID, err := NewID()
	if err != nil {
		return "", err
	}

	p.mu.Lock()
	if _, ok := p.sessions[oldID]; !ok {
		p.mu.Unlock()
		return "", ErrSessionNotFound
	}
	p.rekeyLocked(oldID, newID)
	p.registerAllocatedUUIDLocked(newID)
	if err := p.saveLocked(); err != nil {
		p.log.Warn("sessions: new_session rotate persist failed",
			"event", "rotate_new_session.persist_failed",
			"session_id", string(newID),
			"previous_session_id", string(oldID),
			"err", err)
	}
	p.mu.Unlock()

	p.notifyTransition(SessionTransition{
		PreviousID: oldID,
		NewID:      newID,
		Reason:     ReasonClear,
		OccurredAt: time.Now().UTC(),
	})
	return newID, nil
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
