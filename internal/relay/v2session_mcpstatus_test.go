package relay

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	mcpStatusKnownConv   = "REMOTE_CONVERSATION_2381"
	mcpStatusUnknownConv = "REMOTE_UNKNOWN_2381"
	mcpStatusBlockedConv = "REMOTE_BLOCKED_2381"
	mcpStatusOtherConv   = "REMOTE_OTHER_2381"
)

var mcpStatusFixture = protocol.MCPStatusPayload{
	ConversationID: mcpStatusKnownConv,
	Servers: []protocol.MCPServerStatus{
		{Name: "REMOTE_SERVER_NAME_2381", Status: "connected", Error: "", Scope: "project", Version: "9.8.7"},
		{Name: "second", Status: "failed", Error: "REMOTE_SERVER_ERROR_2381", Scope: "local", Version: "1.2.3"},
	},
	DroppedServers: 4,
}

var poisonedMCPStatus = protocol.MCPStatusPayload{
	ConversationID: "POISONED_CONVERSATION_2381",
	Servers: []protocol.MCPServerStatus{{
		Name: "POISONED_SERVER_2381", Status: "connected", Scope: "user", Version: "0.0.1",
	}},
	DroppedServers: 99,
}

type mcpStatusSeamCounts struct {
	known    atomic.Int64
	resolves atomic.Int64
}

func mcpStatusManagerFor(
	t *testing.T,
	known func(string) bool,
	resolve func(context.Context, string) (protocol.MCPStatusPayload, bool),
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
		MCPStatusFor:      resolve,
	})
	t.Cleanup(stop)
	return mgr, frames, rec, respPub
}

func sendMCPStatusRequest(t *testing.T, frames chan protocol.RoutingEnvelope, send *noise.CipherState, connID string, id uint64, payload string) {
	t.Helper()
	frames <- sealAppFrameConn(t, send, connID, protocol.Envelope{
		ID:      id,
		Type:    protocol.TypeMCPStatusRequest,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(payload),
	})
}

func waitMCPStatusReply(t *testing.T, rec *v2Recorder, connID string, recv *noise.CipherState, index int) protocol.Envelope {
	t.Helper()
	waitForConnNoiseMsg(t, rec, connID, index+1)
	msgs := noiseMsgsForConn(t, rec, connID)
	return decryptAppFrame(t, msgs[index], recv)
}

func assertMCPStatusError(t *testing.T, env protocol.Envelope, requestID uint64, code string, retryable bool) {
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

func TestV2Session_MCPStatusRequest_NilResolverConsumesWithoutPayloadDecode(t *testing.T) {
	t.Parallel()
	counts := &mcpStatusSeamCounts{}
	mgr, frames, rec, respPub := mcpStatusManagerFor(t, func(string) bool {
		counts.known.Add(1)
		return true
	}, nil, silentLogger())
	send, _ := openModalConn(t, mgr, frames, rec, respPub, "mcp-inert", []string{protocol.CapabilityInteractive})

	sendMCPStatusRequest(t, frames, send, "mcp-inert", 23810, `{"conversation_id":["REMOTE_PAYLOAD_2381"]}`)
	openModalConn(t, mgr, frames, rec, respPub, "mcp-inert-barrier", []string{protocol.CapabilityInteractive})

	if got := noiseMsgsForConn(t, rec, "mcp-inert"); len(got) != 0 {
		t.Fatalf("nil resolver produced %d application replies, want none", len(got))
	}
	if got := counts.known.Load(); got != 0 {
		t.Errorf("KnownConversation consulted %d times, want 0", got)
	}
}

func TestV2Session_MCPStatusRequest_NonInteractiveIsInert(t *testing.T) {
	t.Parallel()
	counts := &mcpStatusSeamCounts{}
	resolve := func(context.Context, string) (protocol.MCPStatusPayload, bool) {
		counts.resolves.Add(1)
		return mcpStatusFixture, true
	}
	mgr, frames, rec, respPub := mcpStatusManagerFor(t, func(string) bool {
		counts.known.Add(1)
		return true
	}, resolve, silentLogger())
	send, _ := openModalConn(t, mgr, frames, rec, respPub, "mcp-no-cap", nil)

	sendMCPStatusRequest(t, frames, send, "mcp-no-cap", 23811, `{"conversation_id":"`+mcpStatusKnownConv+`"}`)
	openModalConn(t, mgr, frames, rec, respPub, "mcp-no-cap-barrier", []string{protocol.CapabilityInteractive})

	if got := noiseMsgsForConn(t, rec, "mcp-no-cap"); len(got) != 0 {
		t.Fatalf("non-interactive conn produced %d application replies, want none", len(got))
	}
	if got := counts.known.Load(); got != 0 {
		t.Errorf("KnownConversation consulted %d times, want 0", got)
	}
	if got := counts.resolves.Load(); got != 0 {
		t.Errorf("MCPStatusFor consulted %d times, want 0", got)
	}
}

func TestV2Session_MCPStatusRequest_MalformedStopsBeforeDependencies(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{
		`{"conversation_id":2381}`,
		`"REMOTE_PAYLOAD_2381"`,
	} {
		payload := payload
		t.Run(payload, func(t *testing.T) {
			t.Parallel()
			counts := &mcpStatusSeamCounts{}
			resolve := func(context.Context, string) (protocol.MCPStatusPayload, bool) {
				counts.resolves.Add(1)
				return mcpStatusFixture, true
			}
			mgr, frames, rec, respPub := mcpStatusManagerFor(t, func(string) bool {
				counts.known.Add(1)
				return true
			}, resolve, silentLogger())
			send, recv := openModalConn(t, mgr, frames, rec, respPub, "mcp-malformed", []string{protocol.CapabilityInteractive})

			sendMCPStatusRequest(t, frames, send, "mcp-malformed", 23812, payload)
			reply := waitMCPStatusReply(t, rec, "mcp-malformed", recv, 0)
			assertMCPStatusError(t, reply, 23812, protocol.CodeProtocolMalformed, false)
			if got := len(noiseMsgsForConn(t, rec, "mcp-malformed")); got != 1 {
				t.Fatalf("malformed request produced %d replies, want exactly 1", got)
			}

			if got := counts.known.Load(); got != 0 {
				t.Errorf("KnownConversation consulted %d times, want 0", got)
			}
			if got := counts.resolves.Load(); got != 0 {
				t.Errorf("MCPStatusFor consulted %d times, want 0", got)
			}
		})
	}
}

func TestV2Session_MCPStatusRequest_RejectsUnknownAndUnavailable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		conversation  string
		known         bool
		wantCode      string
		wantRetryable bool
		wantResolve   int64
	}{
		{"unknown conversation", mcpStatusUnknownConv, false, protocol.CodeConversationNotFound, false, 0},
		{"hosted without current status", mcpStatusKnownConv, true, protocol.CodeMCPStatusUnavailable, true, 1},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			counts := &mcpStatusSeamCounts{}
			resolve := func(context.Context, string) (protocol.MCPStatusPayload, bool) {
				counts.resolves.Add(1)
				return poisonedMCPStatus, false
			}
			mgr, frames, rec, respPub := mcpStatusManagerFor(t, func(id string) bool {
				counts.known.Add(1)
				return tc.known && id == tc.conversation
			}, resolve, silentLogger())
			send, recv := openModalConn(t, mgr, frames, rec, respPub, "mcp-reject", []string{protocol.CapabilityInteractive})

			sendMCPStatusRequest(t, frames, send, "mcp-reject", 23813, `{"conversation_id":"`+tc.conversation+`"}`)
			reply := waitMCPStatusReply(t, rec, "mcp-reject", recv, 0)
			assertMCPStatusError(t, reply, 23813, tc.wantCode, tc.wantRetryable)
			if got := len(noiseMsgsForConn(t, rec, "mcp-reject")); got != 1 {
				t.Fatalf("rejected request produced %d replies, want exactly 1", got)
			}
			if got := counts.resolves.Load(); got != tc.wantResolve {
				t.Errorf("MCPStatusFor consulted %d times, want %d", got, tc.wantResolve)
			}
			if reply.Type == protocol.TypeMCPStatus {
				t.Fatal("refusal sent mcp_status instead of an error")
			}
		})
	}
}

func TestV2Session_MCPStatusRequest_SuccessIsCorrelatedAndUnicast(t *testing.T) {
	t.Parallel()
	resolve := func(context.Context, string) (protocol.MCPStatusPayload, bool) {
		return mcpStatusFixture, true
	}
	mgr, frames, rec, respPub := mcpStatusManagerFor(t, func(id string) bool {
		return id == mcpStatusKnownConv
	}, resolve, silentLogger())
	sendA, recvA := openModalConn(t, mgr, frames, rec, respPub, "mcp-requester", []string{protocol.CapabilityInteractive})
	_, _ = openModalConn(t, mgr, frames, rec, respPub, "mcp-observer", []string{protocol.CapabilityInteractive})

	sendMCPStatusRequest(t, frames, sendA, "mcp-requester", 23814, `{"conversation_id":"`+mcpStatusKnownConv+`"}`)
	reply := waitMCPStatusReply(t, rec, "mcp-requester", recvA, 0)
	if got := len(noiseMsgsForConn(t, rec, "mcp-requester")); got != 1 {
		t.Fatalf("successful request produced %d replies, want exactly 1", got)
	}

	if reply.Type != protocol.TypeMCPStatus {
		t.Fatalf("reply type = %q, want existing %q", reply.Type, protocol.TypeMCPStatus)
	}
	if reply.InReplyTo == nil || *reply.InReplyTo != 23814 {
		t.Fatalf("in_reply_to = %v, want pointer to 23814", reply.InReplyTo)
	}
	if reply.EventID != nil {
		t.Errorf("event_id = %v, want nil so the reply never enters the event ring", reply.EventID)
	}
	var got protocol.MCPStatusPayload
	if err := json.Unmarshal(reply.Payload, &got); err != nil {
		t.Fatalf("decode mcp_status payload: %v", err)
	}
	if !reflect.DeepEqual(got, mcpStatusFixture) {
		t.Errorf("payload = %#v, want resolver result %#v", got, mcpStatusFixture)
	}
	if observer := noiseMsgsForConn(t, rec, "mcp-observer"); len(observer) != 0 {
		t.Fatalf("observer received %d application frames, want none", len(observer))
	}
}

func TestV2Session_MCPStatusRequest_BlockedResolverDoesNotStallOtherConnection(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	resolve := func(ctx context.Context, id string) (protocol.MCPStatusPayload, bool) {
		if id == mcpStatusBlockedConv {
			select {
			case entered <- struct{}{}:
			case <-ctx.Done():
				return protocol.MCPStatusPayload{}, false
			}
			select {
			case <-release:
			case <-ctx.Done():
				return protocol.MCPStatusPayload{}, false
			}
		}
		return protocol.MCPStatusPayload{ConversationID: id, Servers: []protocol.MCPServerStatus{}}, true
	}
	mgr, frames, rec, respPub := mcpStatusManagerFor(t, func(id string) bool {
		return id == mcpStatusBlockedConv || id == mcpStatusOtherConv
	}, resolve, silentLogger())
	sendA, recvA := openModalConn(t, mgr, frames, rec, respPub, "mcp-blocked", []string{protocol.CapabilityInteractive})
	sendB, recvB := openModalConn(t, mgr, frames, rec, respPub, "mcp-free", []string{protocol.CapabilityInteractive})

	sendMCPStatusRequest(t, frames, sendA, "mcp-blocked", 23815, `{"conversation_id":"`+mcpStatusBlockedConv+`"}`)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked resolver was not entered")
	}

	sendMCPStatusRequest(t, frames, sendB, "mcp-free", 23816, `{"conversation_id":"`+mcpStatusOtherConv+`"}`)
	freeReply := waitMCPStatusReply(t, rec, "mcp-free", recvB, 0)
	if freeReply.Type != protocol.TypeMCPStatus || freeReply.InReplyTo == nil || *freeReply.InReplyTo != 23816 {
		t.Fatalf("free connection reply = %#v, want correlated mcp_status", freeReply)
	}
	if got := noiseMsgsForConn(t, rec, "mcp-blocked"); len(got) != 0 {
		t.Fatalf("blocked connection replied before release: %d frames", len(got))
	}

	releaseOnce.Do(func() { close(release) })
	blockedReply := waitMCPStatusReply(t, rec, "mcp-blocked", recvA, 0)
	if blockedReply.Type != protocol.TypeMCPStatus || blockedReply.InReplyTo == nil || *blockedReply.InReplyTo != 23815 {
		t.Fatalf("blocked connection eventual reply = %#v, want correlated mcp_status", blockedReply)
	}
	if got := len(noiseMsgsForConn(t, rec, "mcp-blocked")); got != 1 {
		t.Fatalf("blocked connection produced %d replies, want exactly 1", got)
	}
}

func TestV2Session_MCPStatusRequest_LogsContainNoRemoteValues(t *testing.T) {
	logger, logs := bufferLogger()
	resolve := func(context.Context, string) (protocol.MCPStatusPayload, bool) {
		return mcpStatusFixture, true
	}
	mgr, frames, rec, respPub := mcpStatusManagerFor(t, func(id string) bool {
		return id == mcpStatusKnownConv
	}, resolve, logger)
	send, recv := openModalConn(t, mgr, frames, rec, respPub, "mcp-logs", []string{protocol.CapabilityInteractive})

	sendMCPStatusRequest(t, frames, send, "mcp-logs", 23817, `{"conversation_id":"`+mcpStatusKnownConv+`"}`)
	_ = waitMCPStatusReply(t, rec, "mcp-logs", recv, 0)
	sendMCPStatusRequest(t, frames, send, "mcp-logs", 23818, `{"conversation_id":["REMOTE_PAYLOAD_2381"]}`)
	_ = waitMCPStatusReply(t, rec, "mcp-logs", recv, 1)
	sendMCPStatusRequest(t, frames, send, "mcp-logs", 23819, `{"conversation_id":"`+mcpStatusUnknownConv+`"}`)
	_ = waitMCPStatusReply(t, rec, "mcp-logs", recv, 2)

	gotLogs := logs.String()
	for _, secret := range []string{
		mcpStatusKnownConv,
		mcpStatusUnknownConv,
		"REMOTE_PAYLOAD_2381",
		"REMOTE_SERVER_NAME_2381",
		"REMOTE_SERVER_ERROR_2381",
	} {
		if strings.Contains(gotLogs, secret) {
			t.Errorf("logs contain remote-authored value %q: %s", secret, gotLogs)
		}
	}
}
