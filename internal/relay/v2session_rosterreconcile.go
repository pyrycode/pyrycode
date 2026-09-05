package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the connect-time background-task-roster reconcile (#2078) — the
// sixth Mode B instance after reconcileModals (#877), reconcileQueues (#878),
// reconcileModelLists (#1863), reconcileQuestions (#1979) and
// reconcileSlashCommandLists (#2006). Its own file for the reason the four
// reconciles before it give for theirs: the test files were already
// one-per-reconcile, and a reconcile whose name sits one letter from
// reconcileModals / reconcileModelLists is a readability hazard worth one file to
// avoid.

// reconcileBackgroundTaskRosters unicasts the background-task rosters the daemon
// currently holds to a freshly interactive-open conn (#2078) — the sixth Mode B
// instance, structurally the slash-command twin with the payload type substituted.
// Neither recovery mode in docs/protocol-mobile.md § Reconnect / Backfill semantics
// served this family before it: Mode A (cursor replay) needs the client to advertise
// hello.last_event_id and pyrycode-desktop advertises none, whose stated consequence
// is no replay at all, and Mode B did not list this frame. The live turn lane is the
// frame's only other path, and it reaches whoever is connected when claude CHANGES
// the roster, so a client attaching afterwards shows an empty background-task panel
// until a change that may never come.
//
// A roster is snapshot-shaped full state — it reports what is alive at one moment
// rather than what changed, it is conversation-scoped rather than turn-scoped, and
// receiving one neither opens nor closes a turn — which is exactly the data class
// docs/protocol-mobile.md § Reconnect / Backfill semantics binds to a connect-time
// snapshot rather than a cursor backfill. That split is keyed on the KIND OF DATA,
// not on how long the client was away. The re-send is therefore idempotent by
// construction: the frame is full state rather than a delta, so there is no
// stale-replay risk and no special-casing of the away duration.
//
// Run-goroutine only (called from handleNoiseInit's success tail), so s.interactive /
// s.connID are read lock-free under the package's single-owner invariant — that
// guarantee is the caller's, not this function's. It reaches no cmd/pyry emitter
// state: the payloads come from a pure daemon-side read and the envelope ID is
// non-load-bearing (the client correlates on conversation_id), so the send is
// entirely relay-side with a fixed ID — no cross-goroutine coupling, no lock added.
// m.Push takes pushMu internally and is the only lock on the path, and it is never
// held across the marshal or across the seam call. This path also mutates nothing it
// read: it marshals each payload and never writes through the producer's backing
// array.
//
// It enqueues rather than seals, and that is load-bearing rather than incidental:
// Push only buffers, leaving drainOnce to consult Connected before sealing, so no
// Noise send-nonce is burned for a frame that cannot reach the phone (a burned nonce
// gaps the phone's recv nonce and 4421-closes a still-live session, #874).
//
// EventID left nil is load-bearing twice, as in the five twins: it keeps the frame
// out of the turn-event replay ring (so this path adds no per-conversation ring
// memory), and it makes forwardEnvelope's last_event_id dedup inert for the frame. No
// turn is opened either, because nothing here touches turn state: the frame goes
// straight onto the conn's push queue via Push, never through forwardEnvelope, and
// TypeBackgroundTaskRoster is not a turn-boundary type. It is not droppable either —
// pushQueue.enqueue marks only TypeAssistantDelta droppable — so being last in the
// handshake tail cannot cost this frame its delivery.
//
// This path applies no bound of its own; the payloads arrive already bounded at
// construction (DroppedTasks on the aggregate, TruncatedFields per row, over the
// producer's per-entry and per-description caps). See RetainedBackgroundTaskRosters.
//
// SECURITY: all four strings a task row carries — TaskID, TaskType, Description and
// every entry of TruncatedFields — are claude-authored untrusted text and are NEVER
// logged (#833). Description is a literal command line for the local_bash task type,
// which BackgroundTask's own doc grades as the more tempting shape of this family
// because a LIST of command lines invites being fed somewhere structured; it is safe
// to RENDER as inert text and never to execute, re-shell, or feed to an HTML sink, an
// attribute or a URL. The marshal branch carries only content-free discriminants
// (event, conn_id, conversation_id — a server-minted routing id that already crosses
// the wire both ways, never a value a client chooses) and deliberately does NOT echo
// err, because encoding/json quotes the input bytes and a task description would land
// in the record. The push branch may echo err only because Push returns ctx.Err() or
// ErrConnNotFound and nothing else — an inherited contract, named here so a later
// change to Push's error values is visibly load-bearing. The success path logs nothing
// at all: a connect-time reconcile fires on every handshake, the routine-read cadence
// handleRequestSessionSettings cites when it logs conn_id and nothing else.
func (m *V2SessionManager) reconcileBackgroundTaskRosters(ctx context.Context, s *V2Session) {
	// Capability gate (AC2) + the unwired/foreground opt-out. Mirrors the five twins:
	// s.interactive is the negotiated flag, a nil seam is the nil-resolver posture the
	// other optional control seams share.
	if !s.interactive || m.cfg.RetainedBackgroundTaskRosters == nil {
		return
	}
	retained := m.cfg.RetainedBackgroundTaskRosters()
	if len(retained) == 0 {
		return // nothing retained ⇒ nothing sent (AC2).
	}
	// One timestamp shared by the batch (matches the five twins).
	ts := time.Now().UTC()
	for _, p := range retained {
		// NO per-payload emptiness filter, and the absence is deliberate rather than an
		// oversight: this is the one place the twins' answer is wrong here. A payload
		// whose Tasks is empty is a POSITIVE statement that nothing is alive — the
		// #1240 signal — so it is sent as a frame carrying an empty list (AC3), which
		// BackgroundTaskRosterPayload.MarshalJSON renders as "tasks":[] rather than
		// null. The len(retained) == 0 return above is the SEAM being empty, a
		// different thing entirely.
		payload, err := json.Marshal(p)
		if err != nil {
			// BackgroundTaskRosterPayload is a closed struct of string /
			// []BackgroundTask / int, BackgroundTask is three strings and a []string,
			// and the custom MarshalJSON delegates to json.Marshal over that closed
			// alias, so marshal cannot fail — no test can redden this. Defensive only:
			// NEVER echo err or the payload (either would quote a task description).
			// Skip this one, keep sending the rest.
			m.cfg.Logger.Warn("relay: v2 background_task_roster reconcile marshal failed",
				"event", "v2.backgroundtaskroster.reconcile.marshal_err",
				"conn_id", s.connID,
				"conversation_id", p.ConversationID)
			continue
		}
		env := protocol.Envelope{
			ID:      1, // non-load-bearing; the client correlates on conversation_id.
			Type:    protocol.TypeBackgroundTaskRoster,
			TS:      ts,
			Payload: payload,
			// EventID left nil: a control snapshot, never part of the turn-event replay
			// ring (forwardEnvelope's dedup is inert for EventID == nil).
		}
		if err := m.Push(ctx, s.connID, env); err != nil {
			// ctx teardown ⇒ stop (the session is going away and the remaining payloads
			// have nowhere to land); any other sentinel (ErrConnNotFound is unreachable
			// — the push queue was created a few statements earlier in this same
			// handleNoiseInit on this same goroutine, which is why this is Debug rather
			// than Warn) ⇒ skip and continue. NEVER echo payload bytes.
			m.cfg.Logger.Debug("relay: v2 background_task_roster reconcile push dropped",
				"event", "v2.backgroundtaskroster.reconcile.push_err",
				"conn_id", s.connID,
				"err", err)
			if ctx.Err() != nil {
				return
			}
		}
	}
}
