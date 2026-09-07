package relay

import (
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #2152 inbound request_system_prompt → stored prompt + live-session verdict ---

const (
	// promptReqTextConvID stores text and runs a session spawned with that same
	// text: the "your edit is live" row.
	promptReqTextConvID = "conv-prompt-text"
	// promptReqDriftedConvID stores text and runs a session spawned with a
	// DIFFERENT value — the state this ticket exists to make visible, because the
	// stored value applies only at the conversation's next session start.
	promptReqDriftedConvID = "conv-prompt-drifted"
	// promptReqEmptyConvID stores an EXPLICITLY EMPTY prompt and runs a session
	// spawned with no operator text. Pool.SystemPromptFor collapses both no-bytes
	// states to "", so these MATCH — comparing the stored pointer against the
	// spawned-with string without collapsing first reports them as differing, and
	// that is the single most likely way this verb ships wrong.
	promptReqEmptyConvID = "conv-prompt-empty"
	// promptReqQuietConvID is hosted, stores nothing and runs nothing: the
	// all-quiet row, and the reply every unresolvable request is answered with.
	promptReqQuietConvID = "conv-prompt-quiet"
	// promptReqStoredNoSessionConvID stores text while nothing runs. It separates
	// the two fields: a "no_session" verdict must NOT suppress the stored value.
	promptReqStoredNoSessionConvID = "conv-prompt-stored-quiet"
	// promptReqForeignConvID is not one this daemon hosts — an arbitrary client
	// string as far as every gate is concerned.
	promptReqForeignConvID = "conv-prompt-foreign"
)

// promptStr builds the tri-state SystemPrompt pointer for a fixture.
func promptStr(s string) *string { return &s }

// quietSystemPrompt is the reply for a hosted conversation holding no prompt and
// running nothing — and, by AC #3, the reply an UNHOSTED conversation gets too.
// Named once here because the whole point is that several paths produce exactly
// it; a test that spelled it inline per row could not state that.
var quietSystemPrompt = protocol.SystemPromptPayload{
	SessionPromptStatus: protocol.SystemPromptStatusNoSession,
}

// quietSystemPromptJSON is the same answer as the bytes that actually reach a
// client. Asserted rather than the decoded struct wherever "the system_prompt key
// is ABSENT" is the claim: a decoded nil pointer is identical for an absent key
// and for an explicit null, so only the bytes can tell them apart.
const quietSystemPromptJSON = `{"session_prompt_status":"no_session"}`

// poisonedSystemPrompt is the fixture seam's REFUSAL return: a fully populated
// payload handed back alongside ok == false.
//
// SystemPromptFor's doc says a caller MUST NOT read the payload when the comma-ok
// is false, and the production cmd/pyry resolver happens to zero its refusal
// return — so against a zero-valued refusal a handler that DISCARDED the comma-ok
// would emit the constant reply anyway and every refusal row below would pass
// while proving nothing. Poisoning it makes "fail closed on ok == false" a tested
// property of internal/relay rather than one borrowed from cmd/pyry.
// poisonedModelList is the same device for the model-list seam.
var poisonedSystemPrompt = protocol.SystemPromptPayload{
	SystemPrompt:        promptStr("REFUSED — these bytes must never reach a client"),
	SessionPromptStatus: protocol.SystemPromptStatusDiffers,
}

// promptSeamCounts records how often the resolver was consulted, so a test can
// assert not merely what came back but whether anything was read to say it — the
// difference between "no reply" and "no reply because nothing was consulted",
// which is the whole of the capability gate's authz property.
//
// Atomic rather than a plain int: the closure runs on the manager's Run dispatch
// goroutine while the assertions run on the test goroutine.
type promptSeamCounts struct {
	resolves atomic.Int64
}

// resolveFixtureSystemPrompt is the SystemPromptFor double. It answers the five
// hosted conversations and refuses — poisoned — everything else.
//
// The two fields vary INDEPENDENTLY across the rows on purpose: a stored value
// with no session, and no stored value with a matching session, are both present,
// so a handler that derived one field from the other reddens.
func resolveFixtureSystemPrompt(id string) (protocol.SystemPromptPayload, bool) {
	switch id {
	case promptReqTextConvID:
		return protocol.SystemPromptPayload{
			SystemPrompt:        promptStr("Answer only in haiku."),
			SessionPromptStatus: protocol.SystemPromptStatusMatches,
		}, true
	case promptReqDriftedConvID:
		return protocol.SystemPromptPayload{
			SystemPrompt:        promptStr("Answer only in haiku."),
			SessionPromptStatus: protocol.SystemPromptStatusDiffers,
		}, true
	case promptReqEmptyConvID:
		return protocol.SystemPromptPayload{
			SystemPrompt:        promptStr(""),
			SessionPromptStatus: protocol.SystemPromptStatusMatches,
		}, true
	case promptReqStoredNoSessionConvID:
		return protocol.SystemPromptPayload{
			SystemPrompt:        promptStr("Answer only in haiku."),
			SessionPromptStatus: protocol.SystemPromptStatusNoSession,
		}, true
	case promptReqQuietConvID:
		return quietSystemPrompt, true
	}
	return poisonedSystemPrompt, false
}

// countingSystemPromptSeam wraps the double and counts every consultation.
func countingSystemPromptSeam() (func(string) (protocol.SystemPromptPayload, bool), *promptSeamCounts) {
	c := &promptSeamCounts{}
	return func(id string) (protocol.SystemPromptPayload, bool) {
		c.resolves.Add(1)
		return resolveFixtureSystemPrompt(id)
	}, c
}

// promptReqManagerFor stands up a v2 manager paired for v2TestToken with the given
// resolver. A sibling of modelReqManagerFor rather than a parameter on it, the
// package's established posture: the two verbs wire disjoint seams, and this one
// deliberately wires NO KnownConversation — it does not consult membership.
func promptReqManagerFor(t *testing.T, resolve func(string) (protocol.SystemPromptPayload, bool), logger *slog.Logger) (mgr *V2SessionManager, frames chan protocol.RoutingEnvelope, rec *v2Recorder, respPub []byte) {
	t.Helper()
	var respPriv []byte
	respPriv, respPub = genV2Keypair(t)
	frames = make(chan protocol.RoutingEnvelope, 8)
	rec = &v2Recorder{}
	var stop func()
	mgr, stop = startManager(t, V2SessionConfig{
		Frames:          frames,
		Outbound:        rec.outbound,
		StaticPriv:      respPriv,
		Devices:         v2PairedRegistry(t, v2TestToken),
		ServerID:        v2TestServerID,
		Logger:          logger,
		SystemPromptFor: resolve,
	})
	t.Cleanup(stop)
	return mgr, frames, rec, respPub
}

// sendSystemPromptRequest opens a conn with the given capabilities, sends one
// request_system_prompt carrying rawPayload verbatim, and returns every
// application frame the daemon sent back on that conn.
//
// rawPayload is raw bytes rather than a typed payload so a row can send a
// MALFORMED body, which the typed form cannot express.
func sendSystemPromptRequest(t *testing.T, resolve func(string) (protocol.SystemPromptPayload, bool), caps []string, reqID uint64, rawPayload string) (replies []protocol.Envelope) {
	t.Helper()
	mgr, frames, rec, respPub := promptReqManagerFor(t, resolve, silentLogger())
	send, recv := openModalConn(t, mgr, frames, rec, respPub, "c-int", caps)

	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeRequestSystemPrompt,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(rawPayload),
	})

	// Barrier: the request is enqueued ahead of this conn's noise_init on the same
	// Frames channel, so once the barrier conn is open the request has been fully
	// handled on the Run goroutine.
	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

	for _, msg := range noiseMsgsForConn(t, rec, "c-int") {
		replies = append(replies, decryptAppFrame(t, msg, recv))
	}
	return replies
}

// soleSystemPromptReply asserts exactly one reply came back, that it is a
// system_prompt correlated to reqID and carrying no event_id, and returns its raw
// payload bytes.
//
// It returns BYTES rather than a decoded payload because the decoded form cannot
// express the distinction several rows below turn on: an absent system_prompt key
// and an explicit null both decode to a nil pointer.
func soleSystemPromptReply(t *testing.T, replies []protocol.Envelope, reqID uint64) json.RawMessage {
	t.Helper()
	if len(replies) != 1 {
		t.Fatalf("got %d app frame(s), want exactly 1 reply — this verb answers every request it accepts, exactly once", len(replies))
	}
	reply := replies[0]
	if reply.Type != protocol.TypeSystemPrompt {
		t.Fatalf("reply Type = %q, want %q — this verb has no error-frame path at all", reply.Type, protocol.TypeSystemPrompt)
	}
	if reply.InReplyTo == nil || *reply.InReplyTo != reqID {
		t.Errorf("reply InReplyTo = %v, want pointer to %d — the reply carries no conversation_id, so correlation is the client's only route back to its request", reply.InReplyTo, reqID)
	}
	if reply.EventID != nil {
		t.Errorf("reply EventID = %d, want absent — a correlated reply must never enter the replay ring or advance a client's cursor", *reply.EventID)
	}
	return reply.Payload
}

// TestV2Session_RequestSystemPrompt_AnswersWithTheStoredPromptAndVerdict is AC #1
// and AC #2: an interactive conn naming a conversation the daemon can answer for
// gets exactly one system_prompt whose payload is the resolver's own, with the
// three stored states and the three verdicts each surviving to the wire.
//
// It asserts the raw BYTES rather than a decoded struct, because that is the only
// form in which "no prompt" and "an explicitly empty prompt" differ: both decode
// to a nil-or-empty reading unless the key's presence is checked. AC #1's
// "distinguishable in the reply" is a statement about bytes.
//
// The empty-prompt row is the one worth reading twice. A conversation storing an
// explicitly empty prompt whose session spawned with no operator text MATCHES,
// because Pool.SystemPromptFor collapses both no-bytes states to "" by design. The
// stored-with-no-session row is the other independence check: a "no_session"
// verdict must not suppress the stored value a client is asking to display.
func TestV2Session_RequestSystemPrompt_AnswersWithTheStoredPromptAndVerdict(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		convID   string
		wantJSON string
	}{
		{
			name:     "stored text, running session spawned with it",
			convID:   promptReqTextConvID,
			wantJSON: `{"system_prompt":"Answer only in haiku.","session_prompt_status":"matches"}`,
		},
		{
			name:     "stored text, running session spawned with something else",
			convID:   promptReqDriftedConvID,
			wantJSON: `{"system_prompt":"Answer only in haiku.","session_prompt_status":"differs"}`,
		},
		{
			name:     "an EXPLICITLY EMPTY prompt against a session spawned with none: matches",
			convID:   promptReqEmptyConvID,
			wantJSON: `{"system_prompt":"","session_prompt_status":"matches"}`,
		},
		{
			name:     "stored text with nothing running: the value still reaches the client",
			convID:   promptReqStoredNoSessionConvID,
			wantJSON: `{"system_prompt":"Answer only in haiku.","session_prompt_status":"no_session"}`,
		},
		{
			name:     "hosted, holding nothing, running nothing",
			convID:   promptReqQuietConvID,
			wantJSON: quietSystemPromptJSON,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			const reqID uint64 = 71
			replies := sendSystemPromptRequest(t, resolveFixtureSystemPrompt,
				[]string{protocol.CapabilityInteractive}, reqID,
				`{"conversation_id":"`+tc.convID+`"}`)

			got := soleSystemPromptReply(t, replies, reqID)
			if string(got) != tc.wantJSON {
				t.Errorf("payload = %s, want %s", got, tc.wantJSON)
			}
		})
	}
}

// TestV2Session_RequestSystemPrompt_NonInteractiveIsInert is AC #3's second half:
// a conn that did not negotiate the interactive capability gets nothing at all —
// no system_prompt, no error, and no consultation of the resolver, so it cannot
// learn whether the named conversation exists or that this verb is implemented.
//
// The seam counter is what makes this discriminating. "Zero frames" alone would
// also pass against a handler that resolved the conversation and then failed to
// send; asserting that NOTHING was consulted is the authz property, and it is why
// the capability gate must stay ahead of both the decode and the resolve.
//
// It names the conversation holding text deliberately — the row that would
// otherwise produce the most revealing reply — so a deleted gate fails loudly.
func TestV2Session_RequestSystemPrompt_NonInteractiveIsInert(t *testing.T) {
	t.Parallel()

	resolve, counts := countingSystemPromptSeam()
	replies := sendSystemPromptRequest(t, resolve, nil, 72,
		`{"conversation_id":"`+promptReqTextConvID+`"}`)

	if len(replies) != 0 {
		t.Errorf("got %d app frame(s), want 0 — a non-interactive conn is answered with nothing at all, not even an error", len(replies))
	}
	if n := counts.resolves.Load(); n != 0 {
		t.Errorf("SystemPromptFor consulted %d time(s), want 0 — the capability gate is the authz boundary and must precede every decode and every resolution", n)
	}
}

// TestV2Session_RequestSystemPrompt_UnresolvableAnswersTheConstantReply is AC #3's
// first half plus the verb's whole error handling: every case that resolves
// nothing is answered with the SAME constant reply, never an error frame.
//
// The poisoned-refusal row is the load-bearing one: the resolver hands back a
// populated payload alongside ok == false, so a handler that discarded the
// comma-ok leaks "REFUSED — these bytes must never reach a client" onto the wire.
//
// The malformed row sends valid JSON of the WRONG TYPE rather than truncated JSON.
// A json.RawMessage field must itself be valid JSON to marshal, so a body like
// `{"conversation_id":` fails inside the test's own envelope marshal instead of
// inside the handler, and would exercise nothing.
func TestV2Session_RequestSystemPrompt_UnresolvableAnswersTheConstantReply(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		nilSeam     bool
		rawPayload  string
		wantResolve int64
	}{
		{
			name:        "a conversation this daemon does not host: refused, poisoned payload discarded",
			rawPayload:  `{"conversation_id":"` + promptReqForeignConvID + `"}`,
			wantResolve: 1,
		},
		{
			name:        "an empty conversation_id resolves nothing and consults nothing",
			rawPayload:  `{"conversation_id":""}`,
			wantResolve: 0,
		},
		{
			name:        "an absent conversation_id, the shape an un-updated client sends",
			rawPayload:  `{}`,
			wantResolve: 0,
		},
		{
			name:        "a malformed payload is TOLERATED: the empty id it leaves resolves nothing",
			rawPayload:  `{"conversation_id":123}`,
			wantResolve: 0,
		},
		{
			name:        "a bare non-object body",
			rawPayload:  `"conv-prompt-text"`,
			wantResolve: 0,
		},
		{
			name:        "no resolver wired at all (foreground / v1)",
			nilSeam:     true,
			rawPayload:  `{"conversation_id":"` + promptReqTextConvID + `"}`,
			wantResolve: 0,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			const reqID uint64 = 73
			resolve, counts := countingSystemPromptSeam()
			wired := resolve
			if tc.nilSeam {
				wired = nil
			}
			replies := sendSystemPromptRequest(t, wired,
				[]string{protocol.CapabilityInteractive}, reqID, tc.rawPayload)

			got := soleSystemPromptReply(t, replies, reqID)
			if string(got) != quietSystemPromptJSON {
				t.Errorf("payload = %s, want %s — every unresolvable case is answered with the one constant reply, and the system_prompt key is ABSENT rather than null", got, quietSystemPromptJSON)
			}
			if n := counts.resolves.Load(); n != tc.wantResolve {
				t.Errorf("SystemPromptFor consulted %d time(s), want %d", n, tc.wantResolve)
			}
		})
	}
}

// TestV2Session_RequestSystemPrompt_UnhostedIsIndistinguishableFromQuiet is the
// exact wording of AC #3: a request naming a conversation this daemon does not
// host is answered with THE SAME reply a hosted conversation holding no prompt
// gets.
//
// The two replies are compared byte for byte rather than each against a constant,
// because the claim is an equality between two paths and not a property of either
// one. It is what keeps the verb from being a conversation-membership oracle — and
// while a paired client can already enumerate conversations with
// list_conversations, a verb that answered differently would have to be reasoned
// about every time the enumeration surface changed.
func TestV2Session_RequestSystemPrompt_UnhostedIsIndistinguishableFromQuiet(t *testing.T) {
	t.Parallel()

	const reqID uint64 = 74
	foreign := soleSystemPromptReply(t, sendSystemPromptRequest(t, resolveFixtureSystemPrompt,
		[]string{protocol.CapabilityInteractive}, reqID,
		`{"conversation_id":"`+promptReqForeignConvID+`"}`), reqID)
	quiet := soleSystemPromptReply(t, sendSystemPromptRequest(t, resolveFixtureSystemPrompt,
		[]string{protocol.CapabilityInteractive}, reqID,
		`{"conversation_id":"`+promptReqQuietConvID+`"}`), reqID)

	if string(foreign) != string(quiet) {
		t.Errorf("unhosted reply = %s, hosted-but-quiet reply = %s — the two must be byte-identical, so a request cannot tell whether this daemon hosts the conversation it named", foreign, quiet)
	}
}
