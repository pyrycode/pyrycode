package sessions

import (
	"errors"
	"time"
)

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
// goroutine for eviction; for clear, whichever goroutine drove the rotation —
// the runner's parse goroutine via AdoptAnnouncedID, or a control-plane
// goroutine via RotateForNewSession) with NO session or pool lock held. The
// implementation MUST NOT block — hand the signal off to a buffered channel and
// return. A nil observer is disabled.
type TransitionObserver func(SessionTransition)

// SetTransitionObserver installs the pool's transition observer. It must be
// called before Pool.Run: the field is then read-only, and the concurrent reads
// from the goroutines Run transitively spawns — the per-session lifecycle
// goroutines, and the runners they in turn start — are race-free via Run's
// goroutine-creation happens-before edge. Calling it after
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
// AdoptAnnouncedID: it re-keys the pool entry, rebinds the owning conversation,
// and fires a ReasonClear transition so the client sees the fresh-session break
// — but it MINTS the id and drives the rotation itself rather than following the
// reset claude announces.
//
// It used to differ on a second axis: it primed the freshly-allocated skip-set,
// because the <newID>.jsonl that RestartFresh was about to create would CREATE-fire
// the rotation watcher, which would otherwise read the daemon's own spawn as a
// self-rotation and double-rotate. #2137 retired the watcher and deleted the
// skip-set, so the mint-and-drive is the whole of the difference now.
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

// ErrSessionIDTaken reports that a rotation's destination id already names a
// DIFFERENT live session. It exists for AdoptAnnouncedID, whose new id comes
// from the supervised child's stdout rather than from this package's own mint,
// and it is a distinct sentinel rather than ErrSessionNotFound's opposite so a
// caller can tell "nothing to rotate" from "rotating would swallow a session".
var ErrSessionIDTaken = errors.New("sessions: session id already in use")

// AdoptAnnouncedID re-keys the session claude ANNOUNCED a reset for onto the
// announced id and, on success, fires a ReasonClear transition. It arrived in
// #2135 as the third sibling of RotateForNewSession and the (now retired, #2137)
// watcher seam onRotate, and the differences from both are the whole of its
// contract:
//
//   - vs RotateForNewSession: it MINTS NOTHING. claude already created
//     <newID>.jsonl and is writing to it; the daemon is following, not driving.
//   - vs onRotate: an equal-id announcement is refused BEFORE the re-key rather
//     than after it. onRotate delegated to RotateID, which checks membership
//     first and only then no-ops on oldID == newID, and then fired a transition
//     unconditionally — so a session announcing the id it already has drew a
//     spurious delimiter. That is exactly what an announcement can carry and this
//     method must not repeat it. RotateID survives the watcher's retirement and
//     keeps that shape, which is why this method still does not delegate to it.
//
// It does NOT delegate to RotateID, and that is the reason the body below repeats
// RotateForNewSession's locked shape rather than composing: the destination-collision
// check has to sit inside the SAME p.mu hold as the mutation, because a check
// outside it is a TOCTOU. What is shared is rekeyLocked, which is where the re-key
// invariant lives — the same sharing RotateForNewSession does.
//
// SECURITY: newID crosses a trust boundary this package has not had before. Until
// #2135 nothing on the supervised child's stdout could mutate the registry; now one
// line does. Two properties bound it. Its SHAPE is already settled upstream —
// streamsup's emitConversationReset gates on transcript.ValidStem, an anchored
// full match over lowercase hex, so the value carries no separator, no traversal
// and no length. Its DESTINATION is settled here: rekeyLocked moves a map entry
// without checking what is already at the destination, so adopting an id that
// names another live session would overwrite that session's entry and silently
// swallow it. ErrSessionIDTaken refuses it.
//
// Errors: ErrSessionNotFound if oldID is unknown. While the rotation watcher ran
// this was the ORDINARY outcome — it observed the same rotation first and had
// already re-keyed — and that is what made "exactly one transition per reset"
// structural rather than merely likely. #2137 retired the watcher, so this call is
// now the one that applies an announced reset and the sentinel marks the exception:
// oldID vanished because a daemon-driven rotation (RotateForNewSession,
// RotateBootstrapForSelfHeal) or a removal got there first. Those rotate onto a
// MINTED id, not this announced one, so unlike the watcher case the session does not
// end up on newID. #2176 revisited the caller-side reading to match: rekeyPool no
// longer collapses this sentinel to success, and the follower declines the
// announcement and unwinds on it. This method's own behaviour is unchanged — the
// sentinel meant "oldID is not here" throughout; only what a caller may conclude from
// it did. ErrSessionIDTaken per above. Both return with no mutation and no
// transition. A saveLocked failure is logged at Warn and swallowed: the in-memory
// rotation is already authoritative and durability is best-effort, matching
// RotateForNewSession and rebindConversation. The notifyTransition fan-out runs off
// p.mu, the established leaf-callback discipline.
func (p *Pool) AdoptAnnouncedID(oldID, newID SessionID) error {
	p.mu.Lock()
	if _, ok := p.sessions[oldID]; !ok {
		p.mu.Unlock()
		return ErrSessionNotFound
	}
	if oldID == newID {
		p.mu.Unlock()
		return nil
	}
	if _, taken := p.sessions[newID]; taken {
		p.mu.Unlock()
		return ErrSessionIDTaken
	}
	p.rekeyLocked(oldID, newID)
	if err := p.saveLocked(); err != nil {
		p.log.Warn("sessions: announced reset persist failed",
			"event", "adopt_announced_id.persist_failed",
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
	return nil
}
