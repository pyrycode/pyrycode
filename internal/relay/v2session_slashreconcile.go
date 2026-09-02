package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the connect-time slash-command-list reconcile (#2006) — the
// fifth Mode B instance after reconcileModals (#877), reconcileQueues (#878),
// reconcileModelLists (#1863) and reconcileQuestions (#1979). Its own file for the
// reason the model-list and question reconciles give for theirs: the test files
// were already one-per-reconcile, and a reconcile whose name sits one letter from
// reconcileModals / reconcileModelLists is a readability hazard worth one file to
// avoid.

// reconcileSlashCommandLists unicasts the current retained slash-command-list set
// to a freshly interactive-open conn (#2006) — the command-menu twin of
// reconcileModals / reconcileQueues / reconcileModelLists / reconcileQuestions,
// and reconcileModelLists' direct sibling: same initialize reply, sibling frame.
// The live turn lane is the only path that carries slash_command_list today and
// three independent loss points sit in front of it (the emitter's
// empty-conversation early return, which is unconditional on the bootstrap child
// because the conversation cursor is only ever set by a successful route while the
// initialize ask fires at child spawn; the droppable classification under
// droppableCap; and forwardEnvelope's last_event_id dedup, a reconnect mechanism
// with no fresh-connect backfill), so a client attaching later has no path to the
// list at all and its command menu stays empty until a turn that may never come.
//
// A slash-command list is control state — decoded from one initialize reply,
// session configuration rather than a turn event, and receiving one neither opens
// nor closes a turn — which is exactly the data class docs/protocol-mobile.md
// § Reconnect / Backfill semantics binds to a connect-time snapshot rather than a
// cursor backfill. That split is keyed on the KIND OF DATA, not on how long the
// client was away. The re-send is therefore idempotent by construction: the frame
// is snapshot-shaped full state rather than a delta, so there is no stale-replay
// risk and no special-casing of the away duration.
//
// Run-goroutine only (called from handleNoiseInit's success tail), so
// s.interactive / s.connID are read lock-free under the package's single-owner
// invariant — that guarantee is the caller's, not this function's. It reaches no
// cmd/pyry emitter state: the payloads come from a pure daemon-side read and the
// envelope ID is non-load-bearing (the client correlates on conversation_id), so
// the send is entirely relay-side with a fixed ID — no cross-goroutine coupling, no
// lock added. m.Push takes pushMu internally and is the only lock on the path, and
// it is never held across the marshal or across the seam call. This path also
// mutates nothing it read: it marshals each payload and never writes through the
// producer's backing array.
//
// It enqueues rather than seals, and that is load-bearing rather than incidental:
// Push only buffers, leaving drainOnce to consult Connected before sealing, so no
// Noise send-nonce is burned for a frame that cannot reach the phone (a burned
// nonce gaps the phone's recv nonce and 4421-closes a still-live session, #874).
//
// EventID left nil is load-bearing twice, as in the four twins: it keeps the frame
// out of the turn-event replay ring (so this path adds no per-conversation ring
// memory), and it makes forwardEnvelope's last_event_id dedup inert for the frame —
// the third loss point above cannot re-drop what this path sends. No turn is opened
// either, because nothing here touches turn state: the frame goes straight onto the
// conn's push queue via Push, never through forwardEnvelope, and
// TypeSlashCommandList is not a turn-boundary type. It is not droppable either —
// pushQueue.enqueue marks only TypeAssistantDelta droppable — so being last in the
// handshake tail cannot cost this frame its delivery.
//
// This path applies no bound of its own; the payloads arrive already bounded at
// construction (DroppedCommands on the aggregate, TruncatedFields per entry, over a
// producer cut measured against marshalled bytes). See RetainedSlashCommandLists.
//
// SECURITY: a SlashCommand's Name, ArgumentHint, Description and every string in
// its Aliases are WORKSPACE-authored, untrusted text that crossed the subprocess
// trust boundary — a lower-trust origin than claude-authored, since a command
// defined in a repository was written by whoever wrote that repository — and they
// are NEVER logged (#833). They are also forwarded UNSANITISED, which is
// SlashCommand's own documented decision and not an omission: the render boundary
// owing the sanitisation is the client's, and a silent transform here would present
// altered text to a client as the workspace's own. The marshal branch carries only
// content-free discriminants (event, conn_id, conversation_id — a server-minted
// routing id that already crosses the wire both ways, never a value a client
// chooses) and deliberately does NOT echo err, because encoding/json quotes the
// input bytes and a command name would land in the record. The push branch may echo
// err only because Push returns ctx.Err() or ErrConnNotFound and nothing else — an
// inherited contract, named here so a later change to Push's error values is
// visibly load-bearing. The success path logs nothing at all: a connect-time
// reconcile fires on every handshake, the routine-read cadence
// handleRequestSessionSettings cites when it logs conn_id and nothing else.
func (m *V2SessionManager) reconcileSlashCommandLists(ctx context.Context, s *V2Session) {
	// Capability gate (AC2) + the unwired/foreground opt-out. Mirrors the four
	// twins: s.interactive is the negotiated flag, a nil seam is the nil-resolver
	// posture the other optional control seams share.
	if !s.interactive || m.cfg.RetainedSlashCommandLists == nil {
		return
	}
	retained := m.cfg.RetainedSlashCommandLists()
	if len(retained) == 0 {
		return // nothing retained ⇒ nothing sent (AC2).
	}
	// One timestamp shared by the batch (matches the four twins).
	ts := time.Now().UTC()
	for _, p := range retained {
		payload, err := json.Marshal(p)
		if err != nil {
			// SlashCommandListPayload is a closed struct of string / []SlashCommand /
			// int, SlashCommand is three strings and two []string, and both custom
			// MarshalJSONs delegate to json.Marshal over those closed types, so marshal
			// cannot fail — no test can redden this. Defensive only: NEVER echo err or
			// the payload (either would quote a command name). Skip this one, keep
			// sending the rest.
			m.cfg.Logger.Warn("relay: v2 slash_command_list reconcile marshal failed",
				"event", "v2.slashcommandlist.reconcile.marshal_err",
				"conn_id", s.connID,
				"conversation_id", p.ConversationID)
			continue
		}
		env := protocol.Envelope{
			ID:      1, // non-load-bearing; the client correlates on conversation_id.
			Type:    protocol.TypeSlashCommandList,
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
			m.cfg.Logger.Debug("relay: v2 slash_command_list reconcile push dropped",
				"event", "v2.slashcommandlist.reconcile.push_err",
				"conn_id", s.connID,
				"err", err)
			if ctx.Err() != nil {
				return
			}
		}
	}
}
