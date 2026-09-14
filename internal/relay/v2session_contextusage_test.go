package relay

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	ctxUsageKnownConv   = "REMOTE_CONVERSATION_2431"
	ctxUsageUnknownConv = "REMOTE_UNKNOWN_2431"
)

// ctxUsageFixture carries a value in every field a mutant could zero, and its
// strings are deliberately conspicuous: the handler forwards the payload byte for
// byte and logs none of it, so a test that asserted only the frame's type would
// stay green against a handler that rebuilt the payload from scratch.
var ctxUsageFixture = protocol.ContextUsagePayload{
	ConversationID: ctxUsageKnownConv,
	Model:          "REMOTE_MODEL_2431",
	TotalTokens:    31337,
	MaxTokens:      200000,
	Percentage:     16,
	Categories: []protocol.ContextUsageCategory{
		{Name: "REMOTE_CATEGORY_2431", Tokens: 21000},
		{Name: "second", Tokens: 10337},
	},
	DroppedCategories: 3,
	MCPTools: []protocol.ContextUsageMCPTool{
		{Name: "REMOTE_TOOL_2431", ServerName: "REMOTE_SERVER_2431", Tokens: 900},
	},
	DroppedMCPTools: 2,
	MemoryFiles: []protocol.ContextUsageMemoryFile{
		{Path: "../../REMOTE_MEMORY_PATH_2431", Type: "project", Tokens: 100},
	},
	DroppedMemoryFiles: 1,
}

// poisonedContextUsage is what a comma-ok-false seam returns in the tests that pin
// "MUST NOT read the payload on false". Production's resolver zeroes its refusal
// return, so a handler that read it anyway would stay green against that producer —
// the poison is what makes the contract checked rather than borrowed.
var poisonedContextUsage = protocol.ContextUsagePayload{
	ConversationID: "POISONED_CONVERSATION_2431",
	Model:          "POISONED_MODEL_2431",
	TotalTokens:    99999,
}

type ctxUsageSeamCounts struct {
	known    atomic.Int64
	resolves atomic.Int64
}

func ctxUsageManagerFor(
	t *testing.T,
	known func(string) bool,
	resolve func(context.Context, string) (protocol.ContextUsagePayload, bool),
	logger *slog.Logger,
) (mgr *V2SessionManager, frames chan protocol.RoutingEnvelope, rec *v2Recorder, respPub []byte) {
	t.Helper()
	respPriv, respPub := genV2Keypair(t)
	frames = make(chan protocol.RoutingEnvelope, 16)
	rec = &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           v2PairedRegistry(t, v2TestToken),
		ServerID:          v2TestServerID,
		Logger:            logger,
		KnownConversation: known,
		ContextUsageFor:   resolve,
	})
	t.Cleanup(stop)
	return mgr, frames, rec, respPub
}

func sendContextUsageRequest(t *testing.T, frames chan protocol.RoutingEnvelope, send *noise.CipherState, connID string, id uint64, payload string) {
	t.Helper()
	frames <- sealAppFrameConn(t, send, connID, protocol.Envelope{
		ID:      id,
		Type:    protocol.TypeRequestContextUsage,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(payload),
	})
}

func waitContextUsageReply(t *testing.T, rec *v2Recorder, connID string, recv *noise.CipherState, index int) protocol.Envelope {
	t.Helper()
	waitForConnNoiseMsg(t, rec, connID, index+1)
	msgs := noiseMsgsForConn(t, rec, connID)
	return decryptAppFrame(t, msgs[index], recv)
}

func assertContextUsageError(t *testing.T, env protocol.Envelope, requestID uint64, code string, retryable bool) {
	t.Helper()
	if env.Type != protocol.TypeError {
		t.Fatalf("reply type = %q, want %q", env.Type, protocol.TypeError)
	}
	if env.InReplyTo == nil || *env.InReplyTo != requestID {
		t.Fatalf("in_reply_to = %v, want pointer to %d", env.InReplyTo, requestID)
	}
	if env.EventID != nil {
		t.Errorf("event_id = %v, want nil for a requester-only reply", env.EventID)
	}
	var got protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &got); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if got.Code != code || got.Retryable != retryable {
		t.Errorf("error = (%q, retryable=%v), want (%q, retryable=%v)", got.Code, got.Retryable, code, retryable)
	}
	if got.Message == "" {
		t.Error("error message is empty; replies must use a static message")
	}
}

// TestV2Session_RequestContextUsage_AnswersHostedConversation is AC-1: one hosted
// conversation, one detail:"full" reading, delivered as the EXISTING context_usage
// frame and correlated to the request.
//
// It compares the decoded payload against the fixture with reflect.DeepEqual rather
// than spot-checking fields, because the handler's contract is that the payload
// crosses UNTOUCHED — a handler that re-stamped ConversationID from the request or
// recomputed a dropped count from len() would pass any narrower assertion.
func TestV2Session_RequestContextUsage_AnswersHostedConversation(t *testing.T) {
	t.Parallel()
	counts := &ctxUsageSeamCounts{}
	var sawConvID atomic.Value
	resolve := func(_ context.Context, convID string) (protocol.ContextUsagePayload, bool) {
		counts.resolves.Add(1)
		sawConvID.Store(convID)
		return ctxUsageFixture, true
	}
	mgr, frames, rec, respPub := ctxUsageManagerFor(t, func(string) bool {
		counts.known.Add(1)
		return true
	}, resolve, silentLogger())
	send, recv := openModalConn(t, mgr, frames, rec, respPub, "ctxu-ok", []string{protocol.CapabilityInteractive})

	sendContextUsageRequest(t, frames, send, "ctxu-ok", 24310, `{"conversation_id":"`+ctxUsageKnownConv+`"}`)
	reply := waitContextUsageReply(t, rec, "ctxu-ok", recv, 0)

	if reply.Type != protocol.TypeContextUsage {
		t.Fatalf("reply type = %q, want %q — the verb mints no second outbound shape", reply.Type, protocol.TypeContextUsage)
	}
	if reply.InReplyTo == nil || *reply.InReplyTo != 24310 {
		t.Fatalf("in_reply_to = %v, want pointer to 24310", reply.InReplyTo)
	}
	if reply.EventID != nil {
		t.Errorf("event_id = %v, want nil: the reply must not enter the replay ring or advance a client cursor", reply.EventID)
	}
	var got protocol.ContextUsagePayload
	if err := json.Unmarshal(reply.Payload, &got); err != nil {
		t.Fatalf("decode context_usage payload: %v", err)
	}
	if !reflect.DeepEqual(got, ctxUsageFixture) {
		t.Errorf("payload = %+v, want %+v — it must cross untouched", got, ctxUsageFixture)
	}
	if got, want := sawConvID.Load(), ctxUsageKnownConv; got != want {
		t.Errorf("resolver saw conversation %v, want %v", got, want)
	}
	if got := counts.resolves.Load(); got != 1 {
		t.Errorf("resolver consulted %d times, want exactly 1", got)
	}
	if got := len(noiseMsgsForConn(t, rec, "ctxu-ok")); got != 1 {
		t.Fatalf("produced %d replies, want exactly 1", got)
	}
}

// TestV2Session_RequestContextUsage_RejectsUnknownAndUnavailable is AC-3: neither
// refusal is met with silence, and the two arms carry DIFFERENT codes —
// KnownConversation is what separates them.
func TestV2Session_RequestContextUsage_RejectsUnknownAndUnavailable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		known        bool
		resolveOK    bool
		nilResolver  bool
		wantCode     string
		wantRetry    bool
		wantResolves int64
	}{
		{"unhosted conversation", false, false, false, protocol.CodeConversationNotFound, false, 0},
		{"hosted, resolver refuses", true, false, false, protocol.CodeContextUsageUnavailable, true, 1},
		{"hosted, resolver has a reading", true, true, false, "", false, 1},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			counts := &ctxUsageSeamCounts{}
			resolve := func(context.Context, string) (protocol.ContextUsagePayload, bool) {
				counts.resolves.Add(1)
				if !tc.resolveOK {
					// Poisoned: a handler reading the payload on false reddens here.
					return poisonedContextUsage, false
				}
				return ctxUsageFixture, true
			}
			mgr, frames, rec, respPub := ctxUsageManagerFor(t, func(string) bool {
				counts.known.Add(1)
				return tc.known
			}, resolve, silentLogger())
			send, recv := openModalConn(t, mgr, frames, rec, respPub, "ctxu-reject", []string{protocol.CapabilityInteractive})

			sendContextUsageRequest(t, frames, send, "ctxu-reject", 24311, `{"conversation_id":"`+ctxUsageUnknownConv+`"}`)
			reply := waitContextUsageReply(t, rec, "ctxu-reject", recv, 0)

			if tc.wantCode == "" {
				if reply.Type != protocol.TypeContextUsage {
					t.Fatalf("reply type = %q, want %q", reply.Type, protocol.TypeContextUsage)
				}
			} else {
				assertContextUsageError(t, reply, 24311, tc.wantCode, tc.wantRetry)
				var got protocol.ErrorPayload
				if err := json.Unmarshal(reply.Payload, &got); err != nil {
					t.Fatalf("decode error payload: %v", err)
				}
				if strings.Contains(got.Message, "POISONED") || strings.Contains(got.Message, ctxUsageUnknownConv) {
					t.Errorf("reject message %q carries an id or a resolved value; it must be a static constant", got.Message)
				}
			}
			if got := counts.resolves.Load(); got != tc.wantResolves {
				t.Errorf("resolver consulted %d times, want %d", got, tc.wantResolves)
			}
			if got := len(noiseMsgsForConn(t, rec, "ctxu-reject")); got != 1 {
				t.Fatalf("produced %d replies, want exactly 1 — no refusal is met with silence", got)
			}
		})
	}
}

// TestV2Session_RequestContextUsage_InertGates pins the two postures that reply
// NOTHING and decode NOTHING. Both are the authz/discovery boundary: a conn that did
// not negotiate interactive, and a daemon with no seam wired, must not be able to
// learn whether the named conversation exists or that this verb is implemented.
func TestV2Session_RequestContextUsage_InertGates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		caps        []string
		nilResolver bool
	}{
		{"nil seam consumes without decoding", []string{protocol.CapabilityInteractive}, true},
		{"non-interactive conn is fully inert", nil, false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			counts := &ctxUsageSeamCounts{}
			var resolve func(context.Context, string) (protocol.ContextUsagePayload, bool)
			if !tc.nilResolver {
				resolve = func(context.Context, string) (protocol.ContextUsagePayload, bool) {
					counts.resolves.Add(1)
					return ctxUsageFixture, true
				}
			}
			mgr, frames, rec, respPub := ctxUsageManagerFor(t, func(string) bool {
				counts.known.Add(1)
				return true
			}, resolve, silentLogger())
			send, _ := openModalConn(t, mgr, frames, rec, respPub, "ctxu-inert", tc.caps)

			// A payload shaped so a decode would be observable if one happened.
			sendContextUsageRequest(t, frames, send, "ctxu-inert", 24313, `{"conversation_id":["REMOTE_PAYLOAD_2431"]}`)
			openModalConn(t, mgr, frames, rec, respPub, "ctxu-inert-barrier", []string{protocol.CapabilityInteractive})

			if got := noiseMsgsForConn(t, rec, "ctxu-inert"); len(got) != 0 {
				t.Fatalf("inert gate produced %d application replies, want none", len(got))
			}
			if got := counts.known.Load(); got != 0 {
				t.Errorf("KnownConversation consulted %d times, want 0", got)
			}
			if got := counts.resolves.Load(); got != 0 {
				t.Errorf("resolver consulted %d times, want 0", got)
			}
		})
	}
}

// TestV2Session_RequestContextUsage_MalformedPayloadMintsNoThirdCode pins the
// tolerate-the-decode decision: a malformed payload leaves an empty id, which the
// membership gate refuses as conversation.not_found. The verb's reject vocabulary is
// TWO codes, and a third appearing here would mean the tolerance was replaced.
func TestV2Session_RequestContextUsage_MalformedPayloadMintsNoThirdCode(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{
		`{"conversation_id":2431}`,
		`"REMOTE_PAYLOAD_2431"`,
		`[]`,
		// Well-formed JSON of the right shape whose value is null: this one does NOT
		// error, it simply leaves the id empty. Both routes must reach the same
		// refusal, which is what "tolerated" has to mean to be worth anything.
		`{"conversation_id":null}`,
	} {
		payload := payload
		t.Run(payload, func(t *testing.T) {
			t.Parallel()
			counts := &ctxUsageSeamCounts{}
			var sawConvID atomic.Value
			sawConvID.Store("<unset>")
			resolve := func(context.Context, string) (protocol.ContextUsagePayload, bool) {
				counts.resolves.Add(1)
				return ctxUsageFixture, true
			}
			// Membership answers honestly: only a non-empty, hosted id passes. An
			// always-true double would hide the very refusal being pinned.
			mgr, frames, rec, respPub := ctxUsageManagerFor(t, func(id string) bool {
				counts.known.Add(1)
				sawConvID.Store(id)
				return id == ctxUsageKnownConv
			}, resolve, silentLogger())
			send, recv := openModalConn(t, mgr, frames, rec, respPub, "ctxu-malformed", []string{protocol.CapabilityInteractive})

			sendContextUsageRequest(t, frames, send, "ctxu-malformed", 24314, payload)
			reply := waitContextUsageReply(t, rec, "ctxu-malformed", recv, 0)

			assertContextUsageError(t, reply, 24314, protocol.CodeConversationNotFound, false)
			if got := sawConvID.Load(); got != "" {
				t.Errorf("membership saw %q, want the empty id a tolerated decode failure leaves", got)
			}
			if got := counts.resolves.Load(); got != 0 {
				t.Errorf("resolver consulted %d times, want 0: membership must precede it", got)
			}
		})
	}
}
