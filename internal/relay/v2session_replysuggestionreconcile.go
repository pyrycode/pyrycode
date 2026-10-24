package relay

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// replySuggestionStale reports whether env, about to be sealed for s, is a
// reply_suggestion whose revision is at or below the highest already delivered
// to s for its conversation (#2830). For any reply_suggestion that decodes it
// also returns the conversation and revision, which forwardEnvelope records
// through recordReplySuggestion once the frame is sent; rev is 0 for every
// other frame, and for a payload that does not decode, which is delivered
// unchanged (the pushedConversationID posture).
//
// Conversations and conns are tracked independently: the map is per conn and
// keyed by conversation_id. The relay only compares revisions; the producer
// assigns them. A zero revision, outside the positive contract, is always stale.
//
// Out-of-order queued revisions are dropped before sealing. Run-goroutine only.
func replySuggestionStale(s *V2Session, env protocol.Envelope) (convID string, rev uint64, stale bool) {
	if env.Type != protocol.TypeReplySuggestion {
		return "", 0, false
	}
	var p struct {
		ConversationID string `json:"conversation_id"`
		Revision       uint64 `json:"revision"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return "", 0, false
	}
	if p.Revision <= s.replySuggestionRevisions[p.ConversationID] {
		return "", 0, true
	}
	return p.ConversationID, p.Revision, false
}

// recordReplySuggestion advances s's watermark for convID to rev. Called by
// forwardEnvelope only after the frame was sent. Run-goroutine only.
func recordReplySuggestion(s *V2Session, convID string, rev uint64) {
	if s.replySuggestionRevisions == nil {
		s.replySuggestionRevisions = make(map[string]uint64)
	}
	s.replySuggestionRevisions[convID] = rev
}
