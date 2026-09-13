//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	mcpQueryInitialUUID = "23820000-0000-4000-8000-000000000001"
	mcpQueryConvID      = "23820000-0000-4000-8000-000000000002"
	mcpQueryWarmupText  = "e2e-mcp-query:warmup\n"
	mcpQueryCausalText  = "e2e-mcp-query:causal\n"
	mcpQueryWarmupID    = uint64(23820)
	mcpQueryRequestID   = uint64(23821)
	mcpQueryCausalID    = uint64(23822)
)

type mcpQueryPhone struct {
	client *fakephone.Client
	send   *noise.CipherState
	recv   *noise.CipherState
}

func dialMCPQueryPhone(
	t *testing.T,
	fr *fakerelay.Server,
	serverID string,
	pubKey []byte,
	token, name string,
) mcpQueryPhone {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(ctx, fr.URL(), serverID, token, name)
	if err != nil {
		t.Fatalf("%s dial: %v", name, err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, token)
	return mcpQueryPhone{client: phone, send: send, recv: recv}
}

func sendMCPQueryTurn(t *testing.T, phone mcpQueryPhone, requestID uint64, text string) {
	t.Helper()
	sendSealedEnvelope(t, phone.client, phone.send, protocol.Envelope{
		ID:   requestID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: mcpQueryConvID,
			MessageID:      "message-mcp-query",
			Text:           text,
		}),
	})
}

func drainMCPQueryTurn(t *testing.T, phone mcpQueryPhone, text string) (statuses int) {
	t.Helper()
	sawEcho := false
	deadline := time.Now().Add(30 * time.Second)
	for {
		env, ok := nextAttachmentEnvelope(t, phone.client, phone.recv, deadline)
		if !ok {
			t.Fatalf("turn %q did not finish; echo=%v statuses=%d", strings.TrimSpace(text), sawEcho, statuses)
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error during turn %q: %s", strings.TrimSpace(text), env.Payload)
		case protocol.TypeMCPStatus:
			statuses++
		case protocol.TypeAssistantDelta:
			var delta protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &delta); err != nil {
				t.Fatalf("decode assistant_delta: %v", err)
			}
			sawEcho = sawEcho || strings.Contains(delta.Text, strings.TrimSpace(text))
		case protocol.TypeTurnEnd:
			if !sawEcho {
				t.Fatalf("turn %q ended without its echo", strings.TrimSpace(text))
			}
			return statuses
		}
	}
}

func TestRelayV2_MCPStatusRequestQueriesLiveChildRequesterOnly(t *testing.T) {
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	payloadA, err := paireddevice.Setup(paireddevice.Config{
		Home:         home,
		InstanceName: "test",
		Relay:        relayURL,
		DeviceName:   "phone-a",
	})
	if err != nil {
		t.Fatalf("setup phone-a: %v", err)
	}
	payloadB, err := paireddevice.Setup(paireddevice.Config{
		Home:         home,
		InstanceName: "test",
		Relay:        relayURL,
		DeviceName:   "phone-b",
	})
	if err != nil {
		t.Fatalf("setup phone-b: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	seedBoundConversation(t, home, mcpQueryConvID, mcpQueryInitialUUID)
	h := StartStreamInteractiveWithRelay(t, home, mcpQueryInitialUUID, relayURL,
		"PYRY_FAKE_CLAUDE_MCP_STATUS=1")
	t.Cleanup(func() { h.Stop(t) })
	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	// The first request is the automatic startup report. Waiting for its fixed
	// no-cursor drop makes the next request against this same child the fake's
	// changed-state response rather than racing startup.
	waitForDaemonEvent(t, h, "kind=mcp_status", 10*time.Second)
	phoneA := dialMCPQueryPhone(t, fr, serverID, pubKey, payloadA.Token, "phone-a")
	phoneB := dialMCPQueryPhone(t, fr, serverID, pubKey, payloadB.Token, "phone-b")

	// Establish the active-conversation cursor before querying, so a response that
	// accidentally enters the shared turn sink has a live fan-out and ring target.
	sendMCPQueryTurn(t, phoneA, mcpQueryWarmupID, mcpQueryWarmupText)
	if got := drainMCPQueryTurn(t, phoneA, mcpQueryWarmupText); got != 0 {
		t.Fatalf("phone-a warmup MCP statuses = %d, want 0", got)
	}
	if got := drainMCPQueryTurn(t, phoneB, mcpQueryWarmupText); got != 0 {
		t.Fatalf("phone-b warmup MCP statuses = %d, want 0", got)
	}

	sendSealedEnvelope(t, phoneA.client, phoneA.send, protocol.Envelope{
		ID:   mcpQueryRequestID,
		Type: protocol.TypeMCPStatusRequest,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.MCPStatusRequestPayload{
			ConversationID: mcpQueryConvID,
		}),
	})

	statusCountA := 0
	var fresh protocol.MCPStatusPayload
	deadline := time.Now().Add(10 * time.Second)
	for statusCountA == 0 {
		env, ok := nextAttachmentEnvelope(t, phoneA.client, phoneA.recv, deadline)
		if !ok {
			t.Fatal("requesting phone received no MCP status reply")
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("MCP status request returned error: %s", env.Payload)
		}
		if env.Type != protocol.TypeMCPStatus {
			continue
		}
		statusCountA++
		if env.InReplyTo == nil || *env.InReplyTo != mcpQueryRequestID {
			t.Fatalf("mcp_status in_reply_to = %v, want %d", env.InReplyTo, mcpQueryRequestID)
		}
		if env.EventID != nil {
			t.Fatalf("request reply event_id = %v, want nil", env.EventID)
		}
		if err := json.Unmarshal(env.Payload, &fresh); err != nil {
			t.Fatalf("decode fresh mcp_status: %v", err)
		}
	}

	wantServer := protocol.MCPServerStatus{
		Name:    "pyry_mcp_fresh",
		Status:  "connected",
		Error:   "",
		Scope:   "project",
		Version: "10.0.0-fresh",
	}
	if fresh.ConversationID != mcpQueryConvID || fresh.DroppedServers != 0 ||
		len(fresh.Servers) != 1 || fresh.Servers[0] != wantServer {
		t.Fatalf("fresh status = %+v, want conversation %q and server %+v", fresh, mcpQueryConvID, wantServer)
	}

	// A later turn is the causal read boundary: any query response admitted to the
	// shared sink before this turn must reach both phones before their turn_end.
	sendMCPQueryTurn(t, phoneA, mcpQueryCausalID, mcpQueryCausalText)
	statusCountA += drainMCPQueryTurn(t, phoneA, mcpQueryCausalText)
	statusCountB := drainMCPQueryTurn(t, phoneB, mcpQueryCausalText)
	if statusCountA != 1 {
		t.Fatalf("requesting phone MCP statuses = %d, want exactly 1", statusCountA)
	}
	if statusCountB != 0 {
		t.Fatalf("observer phone MCP statuses = %d, want 0", statusCountB)
	}
}
