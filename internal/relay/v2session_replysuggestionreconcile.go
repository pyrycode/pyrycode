package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the connect-time reply-suggestion reconcile (#2830) — the
// eighth Mode B instance after reconcileTurnPhases (#2712) and its six twins —
// and the per-conn revision guard forwardEnvelope applies to every
// reply_suggestion. Its own file for the reason the twins give for theirs: the
// test files are one-per-reconcile.

// reconcileReplySuggestions unicasts the producer's current suggestion state to a
// freshly interactive-open conn (#2830), so a reconnecting client shows the
// suggestion that stands now — and does not restore one another device already
// cleared. It is the turn-phase twin with the payload type substituted; the
// ReplySuggestions seam doc states the seam's contract.
//
// Run-goroutine only (called from handleNoiseInit's success tail), so
// s.interactive / s.connID are read lock-free under the package's single-owner
// invariant.
//
// It enqueues rather than seals, as the twins do: Push only buffers, leaving
// drainOnce to consult Connected before sealing (#874). The drain goes through
// forwardEnvelope, so withheldFromConn withholds a Codex conversation's
// suggestion from a conn without multi_agent, and replySuggestionStale drops a
// snapshot frame that a newer live revision already overtook on this conn.
//
// EventID left nil keeps the frame out of the turn-event replay ring and the
// durable history (this path appends to neither): a reconnect gets the seam's
// current state, never earlier frames.
//
// SECURITY: SuggestedReply is claude-derived text. Nothing here logs a payload,
// err is never echoed on the marshal arm, and nothing is logged on success.
func (m *V2SessionManager) reconcileReplySuggestions(ctx context.Context, s *V2Session) {
	if !s.interactive || m.cfg.ReplySuggestions == nil {
		return
	}
	current := m.cfg.ReplySuggestions()
	if len(current) == 0 {
		return
	}
	ts := time.Now().UTC()
	for _, p := range current {
		payload, err := json.Marshal(p)
		if err != nil {
			// Strings, a uint64 and a *string cannot fail to marshal. Defensive
			// only: never echo err or the payload. Skip this one, keep the rest.
			m.cfg.Logger.Warn("relay: v2 reply_suggestion reconcile marshal failed",
				"event", "v2.replysuggestion.reconcile.marshal_err",
				"conn_id", s.connID,
				"conversation_id", p.ConversationID)
			continue
		}
		env := protocol.Envelope{
			ID:      1, // non-load-bearing; the client correlates on conversation_id.
			Type:    protocol.TypeReplySuggestion,
			TS:      ts,
			Payload: payload,
			// EventID left nil: control state, never part of the replay ring.
		}
		if err := m.Push(ctx, s.connID, env); err != nil {
			// Push returns ctx.Err() or ErrConnNotFound only; the latter is
			// unreachable here (the queue was created earlier in this same
			// handleNoiseInit), hence Debug.
			m.cfg.Logger.Debug("relay: v2 reply_suggestion reconcile push dropped",
				"event", "v2.replysuggestion.reconcile.push_err",
				"conn_id", s.connID,
				"err", err)
			if ctx.Err() != nil {
				return
			}
		}
	}
}

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
// This is what closes the connect-time race: the reconcile reads revision N, the
// producer publishes N+1 and pushes it to the same queue before the snapshot
// drains, N+1 drains first, and N is dropped here. Run-goroutine only.
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
