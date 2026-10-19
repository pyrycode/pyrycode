package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// LiveState binds a detached reading to its source through asynchronous delivery.
// SessionGeneration increases on every transition, including reuse of a session
// ID. Revision increases per (conversation, generation, family, ReadingID).
// Shown/dismissed prompts and session-settings replies share their family.
// Empty ReadingID names a singleton; prompts and tool/task readings use their IDs.
// Envelope.SessionID is supplied provenance, never resolved from current binding.
// InReplyTo may be set for on-demand replies; payload session fields are preserved.
type LiveState struct {
	Envelope          protocol.Envelope
	ConversationID    string
	SessionGeneration uint64
	ReadingID         string
	Revision          uint64
	// Answerable is captured prompt eligibility. False withholds prompt shows;
	// nil leaves supplied eligibility unspecified.
	Answerable *bool
	// ModelListUnprojected marks canonical Claude model rows for per-recipient
	// MergedModelOptions projection. False preserves provider-projected replies.
	ModelListUnprojected bool
}

type liveReadingKey struct{ family, id string }
type liveWatermark struct {
	generation uint64
	cleared    map[string]bool
	revisions  map[liveReadingKey]uint64
}

var errInvalidLiveState = errors.New("relay: invalid supplied live state")

func liveStateFamily(typ string) bool {
	switch typ {
	case protocol.TypeModalShown, protocol.TypeModalDismissed, protocol.TypeQuestionShown, protocol.TypeQuestionDismissed,
		protocol.TypeTurnState, protocol.TypeStall, protocol.TypeApiRetry, protocol.TypeCompacting, protocol.TypeThinkingProgress,
		protocol.TypeToolProgress, protocol.TypeBackgroundTaskProgress, protocol.TypeResetting, protocol.TypeRateLimited,
		protocol.TypeContextUsage, protocol.TypeModelAnnounced, protocol.TypeSessionFacts, protocol.TypeSessionSettings,
		protocol.TypeSessionSettingsUpdated, protocol.TypeMCPStatus, protocol.TypeSlashCommandList, protocol.TypeModelList,
		protocol.TypeReplySuggestion, protocol.TypeSessionError:
		return true
	}
	return false
}

func validLiveTag(tag json.RawMessage) bool {
	if len(tag) == 0 || bytes.Equal(bytes.TrimSpace(tag), []byte("null")) {
		return true
	}
	var id string
	return json.Unmarshal(tag, &id) == nil && id != ""
}

func liveStateClear(e protocol.Envelope) protocol.Envelope {
	switch e.Type {
	case protocol.TypeModalDismissed:
		e.Type = protocol.TypeModalShown
	case protocol.TypeQuestionDismissed:
		e.Type = protocol.TypeQuestionShown
	case protocol.TypeSessionSettingsUpdated:
		e.Type = protocol.TypeSessionSettings
	}
	e.Payload = json.RawMessage(`{}`)
	e.SessionStateCleared = true
	e.InReplyTo = nil
	return e
}

func validateLiveState(r LiveState) error {
	e := r.Envelope
	if !liveStateFamily(e.Type) || r.ConversationID == "" || r.SessionGeneration == 0 || r.Revision == 0 || !validLiveTag(e.SessionID) || !json.Valid(e.Payload) {
		return errInvalidLiveState
	}
	var payload struct {
		ConversationID json.RawMessage `json:"conversation_id"`
	}
	_ = json.Unmarshal(e.Payload, &payload) // Non-object payloads supply no identity.
	for _, identity := range []json.RawMessage{e.ConversationID, payload.ConversationID} {
		if len(identity) == 0 {
			continue
		}
		var id string
		if json.Unmarshal(identity, &id) != nil || id == "" || id != r.ConversationID {
			return errInvalidLiveState
		}
	}
	if len(e.ConversationID) == 0 {
		e.ConversationID, _ = json.Marshal(r.ConversationID)
	}
	if e.SessionStateCleared {
		var p map[string]json.RawMessage
		if json.Unmarshal(e.Payload, &p) != nil || p == nil || len(p) != 0 {
			return errInvalidLiveState
		}
		e = liveStateClear(e)
	}
	// Both the supplied reading and its generated clear must fit before admission.
	e.EventID = nil
	e.HistoryEntryID = nil
	for _, frame := range []protocol.Envelope{e, liveStateClear(e)} {
		raw, err := json.Marshal(frame)
		if err != nil || len(raw) > protocol.MaxThreadEnvelopeBytes {
			return errInvalidLiveState
		}
	}
	return nil
}

// PushLiveState queues either a source-bound push or a correlated reply. Like
// Push it never seals on the producer. Invalid input returns a content-free error.
// Callers may reuse or mutate their input buffers after this call returns.
func (m *V2SessionManager) PushLiveState(ctx context.Context, connID string, r LiveState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateLiveState(r); err != nil {
		return err
	}
	if r.Answerable != nil {
		v := *r.Answerable
		r.Answerable = &v
	}
	r.Envelope.Payload = bytes.Clone(r.Envelope.Payload)
	r.Envelope.SessionID = bytes.Clone(r.Envelope.SessionID)
	r.Envelope.ConversationID = bytes.Clone(r.Envelope.ConversationID)
	if r.Envelope.InReplyTo != nil {
		id := *r.Envelope.InReplyTo
		r.Envelope.InReplyTo = &id
	}
	if r.Envelope.EventID != nil {
		id := *r.Envelope.EventID
		r.Envelope.EventID = &id
	}
	if r.Envelope.HistoryEntryID != nil {
		id := *r.Envelope.HistoryEntryID
		r.Envelope.HistoryEntryID = &id
	}
	return m.push(ctx, connID, r.Envelope, &r)
}

func (m *V2SessionManager) liveWithheld(s *V2Session, conversation string) bool {
	return conversation == "" || !s.interactive || m.cfg.KnownConversation == nil || !m.cfg.KnownConversation(conversation) ||
		(!s.multiAgent && m.cfg.CodexConversation != nil && m.cfg.CodexConversation(conversation))
}

// forwardLiveState sends at most one frame. A generated clear parks the fresh
// reading for another Run pass, preserving transport hold and single-writer crypto.
func (m *V2SessionManager) forwardLiveState(ctx context.Context, s *V2Session, r LiveState) error {
	if !s.thread {
		r.Envelope.ConversationID = nil
		return m.forwardEnvelope(ctx, s.connID, r.Envelope)
	}
	if !r.Envelope.SessionStateCleared && (r.Envelope.Type == protocol.TypeModalShown || r.Envelope.Type == protocol.TypeQuestionShown) && r.Answerable != nil && !*r.Answerable {
		return nil
	}
	if s.state != V2StateOpen || m.liveWithheld(s, r.ConversationID) {
		return nil
	}
	if err := validateLiveState(r); err != nil {
		return err
	}
	if r.ModelListUnprojected && !r.Envelope.SessionStateCleared && !s.multiAgent {
		var inventory protocol.ModelListPayload
		if json.Unmarshal(r.Envelope.Payload, &inventory) == nil && len(inventory.Models) == 0 {
			return nil // Codex-only fallback is unavailable to this recipient.
		}
	}
	if r.Envelope.InReplyTo == nil && m.cfg.LiveStateCurrent != nil && !m.cfg.LiveStateCurrent(r) {
		return nil
	}
	w := s.liveWatermarks[r.ConversationID]
	if w != nil && r.SessionGeneration < w.generation {
		return nil
	}
	if w == nil || r.SessionGeneration > w.generation {
		w = &liveWatermark{generation: r.SessionGeneration, cleared: make(map[string]bool), revisions: make(map[liveReadingKey]uint64)}
	}
	key := liveReadingKey{liveStateClear(r.Envelope).Type, r.ReadingID}
	if r.Envelope.SessionStateCleared {
		if w.cleared[key.family] {
			return nil
		}
	} else if r.Revision < w.revisions[key] || (r.Revision == w.revisions[key] && r.Envelope.InReplyTo == nil) {
		return nil
	}
	e := r.Envelope
	if len(e.ConversationID) == 0 {
		e.ConversationID, _ = json.Marshal(r.ConversationID)
	}
	e.EventID = nil
	e.HistoryEntryID = nil
	needsClear := !e.SessionStateCleared && !w.cleared[key.family]
	if needsClear {
		e = liveStateClear(e)
	}
	if e.SessionStateCleared {
		e = liveStateClear(e)
	}
	if err := m.forwardEnvelopeSource(ctx, s.connID, e, &r); err != nil {
		return err
	}
	if needsClear {
		fresh := r
		if s.hosted != nil && s.hosted.pushDeferred {
			if err := m.PushLiveState(ctx, s.connID, fresh); err != nil {
				return err
			}
		} else {
			s.livePending = &fresh
		}
	}
	if s.liveWatermarks == nil {
		s.liveWatermarks = make(map[string]*liveWatermark)
	}
	s.liveWatermarks[r.ConversationID] = w
	if e.SessionStateCleared {
		w.cleared[key.family] = true
	} else {
		w.revisions[key] = r.Revision
	}
	return nil
}

func (m *V2SessionManager) signalLiveDrain() {
	held := make(map[string]bool)
	for id, s := range m.sessions {
		held[id] = !m.hostedPushAvailable(s)
	}
	m.pushMu.Lock()
	queued := false
	for id, q := range m.queues {
		if s := m.sessions[id]; !q.overflowed && len(q.items) > 0 && s != nil && len(s.replayQueue) == 0 && s.livePending == nil && (!held[id] || q.items[0].barrier != nil) {
			queued = true
			break
		}
	}
	m.pushMu.Unlock()
	if queued {
		select {
		case m.drainCh <- struct{}{}:
		default:
		}
		return
	}
	for _, s := range m.sessions {
		if !held[s.connID] && (s.livePending != nil || s.liveSnapshot != nil) && !m.pushRetired(s.connID) {
			select {
			case m.drainCh <- struct{}{}:
			default:
			}
			return
		}
	}
}

// refreshLiveCatalog resolves an overtaken reconnect reading once. It cannot
// carry a captured catalog into a different target generation or bypass delivery
// validation, projection and freshness checks in forwardLiveState.
func (m *V2SessionManager) refreshLiveCatalog(s *V2Session, r LiveState) LiveState {
	if !s.thread || s.state != V2StateOpen || m.liveWithheld(s, r.ConversationID) ||
		r.Envelope.Type != protocol.TypeModelList || r.Envelope.SessionStateCleared || r.Envelope.InReplyTo != nil ||
		m.cfg.ModelListReadingFor == nil || m.cfg.LiveStateCurrent == nil || m.cfg.LiveStateCurrent(r) {
		return r
	}
	fresh, ok := m.cfg.ModelListReadingFor(r.ConversationID, s.multiAgent)
	if !ok || fresh.ConversationID != r.ConversationID || fresh.SessionGeneration != r.SessionGeneration ||
		fresh.Envelope.Type != protocol.TypeModelList || fresh.Envelope.SessionStateCleared {
		return r
	}
	fresh.Envelope.InReplyTo = nil
	return fresh
}

// drainLiveOnce pulls only one detached snapshot reading, after queued live
// updates have had a chance to overtake it. No batch enters the push/outbox queue.
func (m *V2SessionManager) drainLiveOnce(ctx context.Context, pendingOnly bool) {
	if ctx.Err() != nil || m.transportDown() {
		return
	}
	for _, s := range m.sessions {
		if m.pushRetired(s.connID) {
			continue
		}
		if (s.livePending == nil && (s.liveSnapshot == nil || pendingOnly)) || !m.reserveHostedPush(s) {
			continue
		}
		// Hosted admission can wait while a producer retires the queue.
		if m.pushRetired(s.connID) {
			m.releaseHostedPush(s)
			continue
		}
		var r LiveState
		if s.livePending != nil {
			r = *s.livePending
			s.livePending = nil
		} else if s.liveSnapshot != nil && !pendingOnly {
			var ok bool
			r, ok = s.liveSnapshot()
			if !ok {
				s.liveSnapshot = nil
				m.releaseHostedPush(s)
				m.signalLiveDrain()
				return
			}
		} else {
			continue
		}
		if err := m.forwardLiveState(ctx, s, m.refreshLiveCatalog(s, r)); err != nil {
			m.cfg.Logger.Warn("relay: supplied live state dropped", "event", "v2.live_state.invalid", "conn_id", s.connID)
		}
		m.releaseHostedPush(s)
		m.signalLiveDrain()
		return
	}
}

// RetireLiveState forces reconnect reconciliation after a producer handoff gap.
// The queue latch quarantines pending output until Run handles gap teardown.
func (m *V2SessionManager) RetireLiveState(ctx context.Context, connID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.pushMu.Lock()
	q, ok := m.queues[connID]
	if ok && !q.overflowed {
		q.producerGap = true
		q.overflowed = true
	}
	m.pushMu.Unlock()
	if !ok {
		return ErrConnNotFound
	}
	select {
	case m.pushOverflow <- connID:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// pushRetired checks the queue latch without accessing Run-owned session state.
func (m *V2SessionManager) pushRetired(connID string) bool {
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	q := m.queues[connID]
	return q != nil && q.overflowed
}
