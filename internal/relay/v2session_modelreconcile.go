package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the connect-time model-list reconcile (#1863) — the third
// Mode B instance after reconcileModals (#877) and reconcileQueues (#878),
// which share v2session_modal.go only because #877 landed before the
// monolithic v2session.go was split. Its own file because "modal" and "model"
// differ by one letter and a model-list reconcile living in a modal-named file
// is a readability hazard worth one file to avoid; the test files were already
// one-per-reconcile.

// reconcileModelLists unicasts the current retained model-list set to a freshly
// interactive-open conn (#1863) — the model twin of reconcileModals /
// reconcileQueues. The live turn lane is the only path that carries model_list
// today and three independent loss points sit in front of it (the cmd/pyry
// emitter's empty-conversation early return, which is unconditional on the
// bootstrap child because the conversation cursor is only ever set by a
// successful route while the initialize ask fires at child spawn; the droppable
// classification under droppableCap; and forwardEnvelope's last_event_id dedup,
// a reconnect mechanism with no fresh-connect backfill), so a client attaching
// later has no path to the list at all. model_list is snapshot-shaped full state
// — "a SNAPSHOT of what claude will accept, not a delta ... receiving one neither
// opens nor closes a turn" (ModelListPayload's own doc) — which is exactly the
// data class docs/protocol-mobile.md § Reconnect / Backfill semantics binds to a
// connect-time snapshot rather than a cursor backfill. The re-send is therefore
// idempotent by construction: no stale-replay risk and no special-casing of how
// long the client was away.
//
// Run-goroutine only (called from handleNoiseInit's success tail), so
// s.interactive / s.connID are read lock-free under the package's single-owner
// invariant — that guarantee is the caller's, not this function's. It reaches no
// cmd/pyry emitter state: the payloads come from a pure daemon-side read and the
// envelope ID is non-load-bearing (the client correlates on conversation_id), so
// the send is entirely relay-side with a fixed ID — no cross-goroutine coupling,
// no lock added. m.Push takes pushMu internally and is the only lock on the path.
//
// EventID left nil is load-bearing twice: it keeps the frame out of the
// turn-event replay ring (so this path adds no per-conversation ring memory), and
// it makes forwardEnvelope's last_event_id dedup inert for the frame — the third
// loss point above cannot re-drop what this path sends. No turn is opened either,
// because nothing here touches turn state: the frame goes straight onto the
// conn's push queue via Push, never through forwardEnvelope, and TypeModelList is
// not a turn-boundary type (frozen by #1704/#1705).
//
// This path applies no bound of its own; the payloads arrive already bounded at
// construction (DroppedModels, TruncatedFields). See RetainedModelLists.
//
// SECURITY: ResolvedModel, Value, DisplayName and the effort levels are
// claude-authored, untrusted text and are NEVER logged (#833). The marshal branch
// carries only content-free discriminants (event, conn_id, conversation_id — a
// non-secret routing id that already crosses the wire both ways) and deliberately
// does NOT echo err, because encoding/json quotes the input bytes and a model
// value would land in the record. The push branch may echo err only because Push
// returns ctx.Err() or ErrConnNotFound and nothing else — an inherited contract,
// named here so a later change to Push's error values is visibly load-bearing.
// The success path logs nothing at all: a connect-time reconcile fires on every
// handshake, the routine-read cadence handleRequestSessionSettings cites when it
// logs conn_id and nothing else.
func (m *V2SessionManager) reconcileModelLists(ctx context.Context, s *V2Session) {
	// Capability gate (AC2) + the unwired/foreground opt-out. Mirrors
	// reconcileQueues: s.interactive is the negotiated flag, a nil seam is the
	// nil-resolver posture the other optional control seams share.
	if !s.interactive || m.cfg.RetainedModelLists == nil {
		return
	}
	retained := m.cfg.RetainedModelLists()
	if len(retained) == 0 {
		return // nothing retained ⇒ nothing sent (AC2).
	}
	// One timestamp shared by the batch (matches reconcileModals/reconcileQueues).
	ts := time.Now().UTC()
	for _, p := range retained {
		payload, err := json.Marshal(p)
		if err != nil {
			// ModelListPayload is a closed struct of string / []ModelOption / int and
			// both custom MarshalJSONs delegate to json.Marshal over closed types, so
			// marshal cannot fail — no test can redden this. Defensive only: NEVER
			// echo err or the payload (either would quote model values). Skip this
			// one, keep sending the rest.
			m.cfg.Logger.Warn("relay: v2 model_list reconcile marshal failed",
				"event", "v2.modellist.reconcile.marshal_err",
				"conn_id", s.connID,
				"conversation_id", p.ConversationID)
			continue
		}
		env := protocol.Envelope{
			ID:      1, // non-load-bearing; the client correlates on conversation_id.
			Type:    protocol.TypeModelList,
			TS:      ts,
			Payload: payload,
			// EventID left nil: a control snapshot, never part of the turn-event
			// replay ring (forwardEnvelope's dedup is inert for EventID == nil).
		}
		if err := m.Push(ctx, s.connID, env); err != nil {
			// ctx teardown ⇒ stop (the session is going away and the remaining
			// payloads have nowhere to land); any other sentinel (ErrConnNotFound is
			// unreachable — the push queue was created a few statements earlier in
			// this same handleNoiseInit on this same goroutine, which is why this is
			// Debug rather than Warn) ⇒ skip and continue. NEVER echo payload bytes.
			m.cfg.Logger.Debug("relay: v2 model_list reconcile push dropped",
				"event", "v2.modellist.reconcile.push_err",
				"conn_id", s.connID,
				"err", err)
			if ctx.Err() != nil {
				return
			}
		}
	}
}
