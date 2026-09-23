package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the inbound CONVERSATION SYSTEM-PROMPT READ interception
// (#2152): the handler behind dispatchAppFrame's TypeRequestSystemPrompt case. It
// is the read half of the #2151 cluster, which shipped write-only — a client that
// did not itself perform the write had no way to learn what a conversation holds,
// and no way at all to learn that the child it is typing at predates the edit.
//
// ITS OWN FILE, for the reason v2session_modelrequest.go states about its own: a
// verb that is emphatically NOT the neighbouring settings verb should not live in
// a settings-named file. The confusion here is worse than one letter — the
// session-keyed set_session_settings / request_session_settings pair and this
// conversation-keyed prompt pair both talk about "what a session runs under", and
// they are different verbs with different keys and different lifetimes.
//
// IT IS NOT LAID OUT LIKE ITS OWN WRITE HALF, and that is deliberate.
// internal/relay/handlers/set_system_prompt.go is a dispatch.Route handler with NO
// capability gate, and says so in its own words — the interactive capability is
// read only in the outbound ActiveConns fan-out there. This verb is on the v2
// INTERCEPTION path, where handleRequestSessionSettings and handleRequestModelList
// both open with `if !s.interactive { return }`, and that gate is the whole of the
// inertness this ticket owes. Copying the write half's layout would drop it.
//
// WHERE IT RUNS: INLINE ON THE RUN DISPATCH GOROUTINE, beside its shape twin
// handleRequestSessionSettings rather than with the arms that hand off to the
// conn's appFrameWorker. Those hash bytes, read files off disk, or marshal a page
// against the envelope cap; this one does a registry lookup, one pool map read and
// one small marshal.
//
// THAT PLACEMENT IS A CONSTRAINT ON FUTURE EDITS, not just a description. The
// reply is sealed by Run under the single-owner send CipherState, so emitting from
// any other goroutine would be a concurrent Encrypt — a NONCE REUSE, a real break
// rather than a race annoyance. If this arm is ever moved to enqueueAppFrame, the
// emit must switch from forwardEnvelope to forwardToRun in the SAME change, and
// the SystemPromptFor seam must stay a bounded in-memory read until then.
//
// IT MINTS NO WIRE CODE AND HAS NO ERROR-FRAME PATH. Every unresolvable case — a
// conversation this daemon does not host, one bound to nothing, a request naming
// none, a payload that will not decode, an unwired seam — is answered with the one
// constant reply below. That is handleRequestSessionSettings' always-answer
// posture, and it is where this verb departs from handleRequestModelList, whose
// two codes exist only because an empty models array cannot stand in for
// "unknown".

// quietSystemPromptReply is the answer for every case that resolves nothing: no
// stored prompt to report (a nil pointer, so the wire key is ABSENT rather than
// null) and no running session to compare against.
//
// A function rather than a package-level var because the value is handed to a
// caller that may not modify it — a shared var holding a pointer field is one
// stray write away from every reply reporting one conversation's prompt. There is
// no pointer in it today; the function is what keeps that from becoming a thing to
// remember.
//
// IT IS ALSO WHAT MAKES AN UNHOSTED CONVERSATION UNDETECTABLE. A hosted
// conversation holding no prompt and running nothing resolves to exactly this
// value, so the two answers are byte-identical and the verb cannot be used to
// probe which conversations this daemon carries.
func quietSystemPromptReply() protocol.SystemPromptPayload {
	return protocol.SystemPromptPayload{
		SessionPromptStatus: protocol.SystemPromptStatusNoSession,
	}
}

// handleRequestSystemPrompt answers one inbound request_system_prompt with the
// named conversation's stored system prompt and a verdict on whether the running
// session was spawned with it (#2152). Intercepted in dispatchAppFrame before
// dispatch.Route, like handleRequestSessionSettings, and running inline on the Run
// goroutine (see the file header).
//
// It takes the already-probed Envelope rather than the plaintext, because it runs
// on Run: the frame is decoded once by the discriminator and its payload once
// here.
//
// ORDER IS THE DESIGN, and the ordering — not merely the presence of the checks —
// is what carries the security property:
//
//  1. THE CAPABILITY GATE IS THE AUTHZ BOUNDARY. A conn that did not negotiate
//     interactive is fully inert — no decode, no seam call, NO REPLY — so it cannot
//     learn whether the named conversation exists, what it holds, or that this verb
//     is implemented at all. It MUST stay ahead of every step below. This is the
//     gate the write half deliberately does not have; see the file header.
//  2. A PAYLOAD DECODE FAILURE IS TOLERATED, NOT REJECTED, and the reason does NOT
//     generalise. It leaves ConversationID == "", which names no conversation and
//     is refused at step 3 without a second reply shape. handleRequestHistory
//     cannot do this because there the empty id becomes a path component, where
//     filepath.Join(dir, "") is the log ROOT rather than an error; here it reaches
//     a registry lookup and nothing else. NEVER echo or log the error —
//     encoding/json quotes offending input into its error string and those bytes
//     are remote-authored.
//  3. RESOLVE, OR DON'T. Three guards, each deliberate. The empty-id guard keeps
//     "an unnamed request addresses nothing" a property of THIS package, provable
//     against any double rather than inherited from whatever the producer does. The
//     nil-seam guard is the foreground / v1 case. And the comma-ok is HONOURED, not
//     discarded: SystemPromptFor's doc says a caller MUST NOT read the payload on
//     false, so payload is assigned only on true — writing `payload, _ = …` would
//     happen to work today only because the cmd/pyry producer zeroes its refusal
//     return, a property of that package rather than of this contract. There is
//     deliberately NO KnownConversation call: membership would split an answer AC
//     #3 requires merged, and the resolver already refuses an unknown id.
//  4. Reply, always exactly once.
//
// SECURITY — the never-log rule, which is absolute here rather than per-field. THE
// PROMPT REACHES THE LOGGER ON NO ARM AND AT NO LEVEL: it is operator-authored text
// that becomes standing instructions to a claude child holding full tool
// permissions. Neither does its length, the conversation id, the decode error, or
// the VERDICT — which is derived from the prompt, and which no operational question
// needs. Every log call below carries conn_id and an event string and nothing else,
// which is set_system_prompt's own divergence from its neighbours, kept for the
// same reason.
//
// THE REPLY IS UNICAST. forwardEnvelope addresses s.connID, never the broadcast
// push path — the widening the write half's conversation_updated ack avoids by
// carrying no prompt at all.
func (m *V2SessionManager) handleRequestSystemPrompt(ctx context.Context, s *V2Session, env protocol.Envelope) {
	if !s.interactive {
		return // Step 1. Inert: no reply, no decode, no seam consulted.
	}

	var p protocol.RequestSystemPromptPayload
	// Step 2. The error is deliberately discarded rather than checked: a failure
	// leaves ConversationID empty, which the guard below refuses. A bare frame from
	// an un-updated client carries a nil Payload, which Unmarshal rejects while
	// leaving p zeroed, and reaches exactly the same place. NEVER echoed, NEVER
	// logged.
	_ = json.Unmarshal(env.Payload, &p)

	// Step 3. Both fields of the reply come from the ONE resolved payload, so the
	// reported stored value and the reported verdict always describe the same
	// conversation — including in the unresolved case, which the wire contract
	// defines as a real answer rather than a degraded one.
	payload := quietSystemPromptReply()
	if p.ConversationID != "" && m.cfg.SystemPromptFor != nil {
		if got, ok := m.cfg.SystemPromptFor(p.ConversationID); ok {
			payload = got
		}
	}

	m.emitSystemPromptReply(ctx, s, env.ID, payload)
}

// emitSystemPromptReply sends one resolved answer as a system_prompt correlated to
// the request. Split from the gates above so the ordering there reads as one
// enumeration rather than trailing off into the marshal.
//
// EventID IS LEFT NIL, load-bearing for the same two reasons it is on the
// model-list reply: the frame never enters the #647 turn-event replay ring, so this
// path adds no per-conversation ring memory, and forwardEnvelope's last_event_id
// dedup stays inert for it, so a reply can never be dropped as already-seen. No
// turn is opened either — TypeSystemPrompt is not a turn-boundary type.
func (m *V2SessionManager) emitSystemPromptReply(ctx context.Context, s *V2Session, inReplyTo uint64, p protocol.SystemPromptPayload) {
	body, err := json.Marshal(p)
	if err != nil {
		// A closed struct of a *string and a constant; marshal cannot fail — no test
		// can redden this. Defensive only, and it answers NOTHING rather than
		// inventing a reject the client could not act on, matching
		// emitModelListReply. NEVER echo err or the payload: either would quote the
		// operator's prompt into the record.
		m.cfg.Logger.Warn("relay: v2 system_prompt reply marshal failed",
			"event", "v2.systemprompt.request.marshal_err",
			"conn_id", s.connID)
		return
	}

	reply := protocol.Envelope{
		ID:        1, // non-load-bearing; the client correlates on InReplyTo.
		Type:      protocol.TypeSystemPrompt,
		TS:        time.Now().UTC(),
		Payload:   body,
		InReplyTo: &inReplyTo,
	}
	// Probed before the served line, so "reported" still means handed to the seal.
	if m.dropInlineReplyIfDown(s, "v2.systemprompt.request.dropped_transport_down") {
		return
	}
	// Content-free: conn_id and nothing else. Not the prompt, not its length, not
	// the conversation id, and NOT the verdict — it is derived from the operator's
	// text, and "which of three states" is not a question an operator reading a
	// daemon log needs answered. Debug, not Info: this is a routine read that can
	// fire on every prompt-editor open, the cadence handleRequestSessionSettings
	// cites.
	m.cfg.Logger.Debug("relay: v2 system prompt reported",
		"event", "v2.systemprompt.request.served",
		"conn_id", s.connID)
	if err := m.forwardEnvelope(ctx, s.connID, reply); err != nil {
		// Unreachable in practice: s is V2StateOpen on the dispatch goroutine.
		// Logged at debug and dropped — the package's outbound-drop posture.
		//
		// WITHOUT "err", AND THAT IS THIS HANDLER'S ONE DIVERGENCE FROM EVERY
		// NEIGHBOUR that writes "err", err here. forwardEnvelope returns two static
		// sentinels and three wrapped errors, one of them from a json.Marshal of the
		// whole envelope — an error string that can quote payload bytes, which on
		// this path are the operator's prompt. That marshal is unreachable for a
		// payload this handler produced itself, so the neighbours are not wrong; but
		// the rule this verb owes is "the prompt reaches no log on ANY path", and
		// dropping one attribute makes that structurally true instead of argued from
		// unreachability. Do not add it back.
		m.cfg.Logger.Debug("relay: v2 system_prompt reply push dropped",
			"event", "v2.systemprompt.request.push_err",
			"conn_id", s.connID)
	}
}
