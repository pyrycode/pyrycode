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
	if e.SessionStateCleared {
		var p map[string]json.RawMessage
		if json.Unmarshal(e.Payload, &p) != nil || p == nil || len(p) != 0 {
			return errInvalidLiveState
		}
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
	r.Envelope.Payload = bytes.Clone(r.Envelope.Payload)
	r.Envelope.SessionID = bytes.Clone(r.Envelope.SessionID)
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
		return m.forwardEnvelope(ctx, s.connID, r.Envelope)
	}
	if s.state != V2StateOpen || m.liveWithheld(s, r.ConversationID) {
		return nil
	}
	if err := validateLiveState(r); err != nil {
		return err
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
	e.EventID = nil
	e.HistoryEntryID = nil
	needsClear := !e.SessionStateCleared && !w.cleared[key.family]
	if needsClear {
		e = liveStateClear(e)
	}
	if e.SessionStateCleared {
		e.Payload = json.RawMessage(`{}`)
	}
	if err := m.forwardEnvelopeSource(ctx, s.connID, e, true); err != nil {
		return err
	}
	if needsClear {
		fresh := r
		s.livePending = &fresh
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
	m.pushMu.Lock()
	queued := false
	for id, q := range m.queues {
		if s := m.sessions[id]; len(q.items) > 0 && s != nil && len(s.replayQueue) == 0 {
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
		if s.livePending != nil || s.liveSnapshot != nil {
			select {
			case m.drainCh <- struct{}{}:
			default:
			}
			return
		}
	}
}

// drainLiveOnce pulls only one detached snapshot reading, after queued live
// updates have had a chance to overtake it. No batch enters the push/outbox queue.
func (m *V2SessionManager) drainLiveOnce(ctx context.Context, pendingOnly bool) {
	if ctx.Err() != nil || m.transportDown() {
		return
	}
	for _, s := range m.sessions {
		var r LiveState
		if s.livePending != nil {
			r = *s.livePending
			s.livePending = nil
		} else if s.liveSnapshot != nil && !pendingOnly {
			var ok bool
			r, ok = s.liveSnapshot()
			if !ok {
				s.liveSnapshot = nil
				m.signalLiveDrain()
				return
			}
		} else {
			continue
		}
		if err := m.forwardLiveState(ctx, s, r); err != nil {
			m.cfg.Logger.Warn("relay: supplied live state dropped", "event", "v2.live_state.invalid", "conn_id", s.connID)
		}
		m.signalLiveDrain()
		return
	}
}
