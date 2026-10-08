package sessions

import (
	"errors"
	"time"
)

// TransitionReason preserves the legacy delimiter vocabulary. A lifecycle fact
// without a legacy delimiter has an empty Reason; consumers must reject it.
type TransitionReason string

const (
	// ReasonClear remains the reset, Claude clear and committed switch delimiter.
	ReasonClear TransitionReason = "clear"
	// ReasonEviction remains the idle and capacity eviction delimiter.
	ReasonEviction TransitionReason = "eviction"
)

// LifecycleCause distinguishes the internal facts behind legacy delimiters.
// The zero value means unknown; workspace change currently has no producer.
type LifecycleCause string

const (
	CauseOperatorReset    LifecycleCause = "operator_reset"
	CauseClaudeClear      LifecycleCause = "claude_clear"
	CauseAgentSwitch      LifecycleCause = "agent_switch"
	CauseRecovery         LifecycleCause = "recovery"
	CauseWorkspaceChange  LifecycleCause = "workspace_change"
	CauseIdleSleep        LifecycleCause = "idle_sleep"
	CauseCapacityEviction LifecycleCause = "capacity_eviction"
)

// SessionTransition is a captured lifecycle fact. Empty session IDs mean absent
// prior/successor sessions; OccurredAt is nonzero UTC. ConversationID is captured
// at the binding mutation or eviction decision, never inferred from bootstrap,
// labels or historical IDs. Empty ownership/agent fields explicitly mean unknown.
type SessionTransition struct {
	PreviousID SessionID
	NewID      SessionID
	Reason     TransitionReason
	OccurredAt time.Time
	// AgentSwitch preserves the dedicated committed-switch publication contract.
	AgentSwitch    bool
	Cause          LifecycleCause
	ConversationID string
	PreviousAgent  string
	NextAgent      string
	// ResetHandoffOutcome is an optional caller-supplied classification, not
	// handoff text. Nil means unknown; the supplied value is copied at rotation.
	ResetHandoffOutcome *string
}

// TransitionObserver receives update-only lifecycle facts: creation, unchanged
// IDs, refused mutations, shutdown and crashes retaining the ID emit nothing.
// Recovery is observable internally but has no legacy delimiter or production
// self-heal caller. Notifications run synchronously on the transition's owning
// goroutine with no pool/session/capacity lock held by that goroutine. Observers
// MUST NOT block: hand off to a buffered channel and return. Nil disables them.
type TransitionObserver func(SessionTransition)

// SetTransitionObserver installs the observer before Pool.Run. Its readers are
// Run's descendant goroutines; the field must remain read-only after Run starts.
func (p *Pool) SetTransitionObserver(obs TransitionObserver) {
	p.transitionObserver = obs
}

// notifyTransition fans out a completed fact off-lock. Binding mutations and
// ownership capture have already happened, so delayed delivery cannot rebind.
func (p *Pool) notifyTransition(t SessionTransition) {
	if p.transitionObserver != nil {
		p.transitionObserver(t)
	}
}

// SwitchTransitionMetadata is supplied by the caller that committed the switch,
// before old-session removal loses its ownership and actual agent information.
// Empty fields explicitly mean unknown.
type SwitchTransitionMetadata struct {
	ConversationID string
	PreviousAgent  string
	NextAgent      string
}

// PublishSwitchTransition publishes one committed switch after inactive reset
// status, without rebinding or persisting again. Metadata is optional for existing
// callers. The dedicated publisher may wait with daemon cancellation; ordinary
// observers remain nonblocking. Empty/equal pairs describe no session change.
func (p *Pool) PublishSwitchTransition(oldID, newID SessionID, metadata ...SwitchTransitionMetadata) {
	if oldID == "" || newID == "" || oldID == newID {
		return
	}
	t := SessionTransition{
		PreviousID: oldID, NewID: newID, Reason: ReasonClear,
		AgentSwitch: true, Cause: CauseAgentSwitch, OccurredAt: time.Now().UTC(),
	}
	if len(metadata) > 0 {
		t.ConversationID = metadata[0].ConversationID
		t.PreviousAgent = metadata[0].PreviousAgent
		t.NextAgent = metadata[0].NextAgent
	}
	p.notifyTransition(t)
	if p.switchPublisher != nil {
		p.switchPublisher(t)
	}
}

// SetSwitchTransitionPublisher installs the switch-only delivery owner before
// Run. It receives the observer's signal, which must not also enqueue switch
// wire outcomes. The callback may wait but must honor daemon cancellation.
func (p *Pool) SetSwitchTransitionPublisher(publish func(SessionTransition)) {
	p.switchPublisher = publish
}

// rotationTransitionLocked captures and rebinds ownership under Pool.mu, in the
// same serialized mutation as the pool rekey. No persistence or callback runs here.
func (p *Pool) rotationTransitionLocked(oldID, newID SessionID, cause LifecycleCause, reason TransitionReason) SessionTransition {
	t := SessionTransition{PreviousID: oldID, NewID: newID, Cause: cause, Reason: reason, OccurredAt: time.Now().UTC()}
	if p.convReg != nil {
		owner, _ := p.convReg.RebindSessionOwner(string(oldID), string(newID))
		t.ConversationID = string(owner)
	}
	return t
}

// persistTransitionBinding runs off pool/session locks. The captured owner
// decides whether to save; a miss never acquires a late binding or rewrites it.
// Persistence is best effort and cannot suppress the authoritative memory fact.
func (p *Pool) persistTransitionBinding(t SessionTransition) {
	if p.convReg == nil || t.ConversationID == "" {
		return
	}
	if err := p.convReg.Save(p.convRegistryPath); err != nil {
		p.log.Warn("sessions: rebind conversation persist failed",
			"event", "rebind_conversation.persist_failed",
			"session_id", string(t.NewID), "previous_session_id", string(t.PreviousID), "err", err)
	}
}

// notifyEviction captures current ownership without rebinding. Removed entries
// are teardown, not eviction facts. Pool.mu protects the ID and membership while
// the registry captures its current binding; the callback runs after unlocking.
func (p *Pool) notifyEviction(s *Session, reason TransitionReason, cause LifecycleCause) {
	p.mu.RLock()
	if p.sessions[s.id] != s {
		p.mu.RUnlock()
		return
	}
	t := SessionTransition{PreviousID: s.id, Reason: reason, Cause: cause, OccurredAt: time.Now().UTC()}
	if p.convReg != nil {
		owner, _ := p.convReg.SessionOwner(string(s.id))
		t.ConversationID = string(owner)
	}
	p.mu.RUnlock()
	p.notifyTransition(t)
}

// RotateForNewSession rekeys a session onto a freshly minted ID, rebinds its
// owner, recomposes its prompt before return/fan-out, and emits an operator reset.
// The signature remains usable as the daemon's existing rotation callback.
func (p *Pool) RotateForNewSession(oldID SessionID) (SessionID, error) {
	return p.RotateForNewSessionWithHandoff(oldID, nil)
}

// RotateForNewSessionWithHandoff optionally captures a caller-supplied outcome.
// Absent IDs and mint failures emit nothing; Save errors retain the in-memory
// mutation and notify. No daemon handoff outcome source is wired here.
func (p *Pool) RotateForNewSessionWithHandoff(oldID SessionID, outcome *string) (SessionID, error) {
	newID, err := NewID()
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	sess, ok := p.sessions[oldID]
	if !ok {
		p.mu.Unlock()
		return "", ErrSessionNotFound
	}
	p.rekeyLocked(oldID, newID)
	t := p.rotationTransitionLocked(oldID, newID, CauseOperatorReset, ReasonClear)
	if outcome != nil {
		value := *outcome
		t.ResetHandoffOutcome = &value
	}
	if err := p.saveLocked(); err != nil {
		p.log.Warn("sessions: new_session rotate persist failed",
			"event", "rotate_new_session.persist_failed", "session_id", string(newID),
			"previous_session_id", string(oldID), "err", err)
	}
	p.mu.Unlock()
	p.persistTransitionBinding(t)
	p.refreshSystemPromptForRotation(sess)
	p.notifyTransition(t)
	return newID, nil
}

// ErrSessionIDTaken means an announced destination belongs to another session.
var ErrSessionIDTaken = errors.New("sessions: session id already in use")

// AdoptAnnouncedID follows Claude's validated reset announcement without minting.
// Missing/colliding IDs refuse mutation and equal IDs are silent no-ops. The
// destination check and rekey share Pool.mu to prevent swallowing another session.
// Upstream transcript stem validation guards the subprocess trust boundary.
// Persistence is best effort; successful in-memory changes always notify.
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
	t := p.rotationTransitionLocked(oldID, newID, CauseClaudeClear, ReasonClear)
	if err := p.saveLocked(); err != nil {
		p.log.Warn("sessions: announced reset persist failed",
			"event", "adopt_announced_id.persist_failed", "session_id", string(newID),
			"previous_session_id", string(oldID), "err", err)
	}
	p.mu.Unlock()
	p.persistTransitionBinding(t)
	p.notifyTransition(t)
	return nil
}
