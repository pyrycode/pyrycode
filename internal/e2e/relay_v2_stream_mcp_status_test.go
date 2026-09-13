//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	mcpStatusInitialUUID = "23750000-0000-4000-8000-000000000001"
	mcpStatusConvID      = "23750000-0000-4000-8000-000000000002"
	mcpStatusFirstText   = "e2e-mcp-status:first\n"
	mcpStatusSecondText  = "e2e-mcp-status:second\n"
	mcpStatusFirstReqID  = uint64(2375)
	mcpStatusSecondReqID = uint64(2376)
)

// TestRelayV2_StreamMCPStatusReachesConnectedPhone proves the process-boundary
// lane whose bootstrap timing unit tests cannot exercise. The first ordinary
// turn routes the seeded conversation before a crash; the replacement child then
// completes initialize and mcp_status while the same interactive phone is open.
func TestRelayV2_StreamMCPStatusReachesConnectedPhone(t *testing.T) {
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	pairing, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(pairing.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	seedBoundConversation(t, home, mcpStatusConvID, mcpStatusInitialUUID)
	h := StartStreamInteractiveWithRelay(t, home, mcpStatusInitialUUID, relayURL,
		"PYRY_FAKE_CLAUDE_MCP_STATUS=1")
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)
	// Drain the bootstrap child's rider before the phone connects. The fixed
	// content-free kind proves that child's status reached Handle while the cursor
	// was empty, so only the later replacement can be observed on the client wire.
	waitForDaemonEvent(t, h, "kind=mcp_status", 10*time.Second)
	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, pairing.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, pairing.Token)

	first := waitForRunnerStatus(t, h, 20*time.Second, "first child running",
		func(s *control.StatusPayload) bool { return s.Phase == "running" && s.ChildPID != 0 })
	var statuses []protocol.MCPStatusPayload
	drainTurn := func(text string, reqID uint64) bool {
		t.Helper()
		sendSealedEnvelope(t, phone, send, protocol.Envelope{
			ID:   reqID,
			Type: protocol.TypeSendMessage,
			TS:   time.Now().UTC(),
			Payload: mustJSON(t, protocol.SendMessagePayload{
				ConversationID: mcpStatusConvID,
				MessageID:      "message-mcp-status",
				Text:           text,
			}),
		})

		sawEcho := false
		deadline := time.Now().Add(30 * time.Second)
		for {
			env, ok := nextAttachmentEnvelope(t, phone, recv, deadline)
			if !ok {
				t.Fatalf("turn %d did not finish; echo=%v statuses=%d", reqID, sawEcho, len(statuses))
			}
			switch env.Type {
			case protocol.TypeError:
				t.Fatalf("unexpected error envelope during turn %d: %s", reqID, env.Payload)
			case protocol.TypeMCPStatus:
				var status protocol.MCPStatusPayload
				if err := json.Unmarshal(env.Payload, &status); err != nil {
					t.Fatalf("decode mcp_status during turn %d: %v", reqID, err)
				}
				statuses = append(statuses, status)
			case protocol.TypeAssistantDelta:
				var delta protocol.AssistantDeltaPayload
				if err := json.Unmarshal(env.Payload, &delta); err != nil {
					t.Fatalf("decode assistant_delta during turn %d: %v", reqID, err)
				}
				sawEcho = sawEcho || strings.Contains(delta.Text, strings.TrimSpace(text))
			case protocol.TypeTurnEnd:
				return sawEcho
			}
		}
	}

	if !drainTurn(mcpStatusFirstText, mcpStatusFirstReqID) {
		t.Fatal("first turn ended without its assistant echo; active-conversation routing was not proved")
	}
	if len(statuses) != 0 {
		t.Fatalf("bootstrap child produced %d visible mcp_status frames, want 0 before replacement", len(statuses))
	}

	killChild(t, first.ChildPID)
	second := waitForRunnerStatus(t, h, 20*time.Second, "replacement child running",
		func(s *control.StatusPayload) bool {
			return s.RestartCount >= 1 && s.ChildPID != 0 && s.ChildPID != first.ChildPID
		})

	statusDeadline := time.Now().Add(30 * time.Second)
	for len(statuses) == 0 {
		env, ok := nextAttachmentEnvelope(t, phone, recv, statusDeadline)
		if !ok {
			t.Fatalf("replacement child %d emitted no mcp_status after child %d", second.ChildPID, first.ChildPID)
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("unexpected error envelope after replacement: %s", env.Payload)
		}
		if env.Type != protocol.TypeMCPStatus {
			continue
		}
		var status protocol.MCPStatusPayload
		if err := json.Unmarshal(env.Payload, &status); err != nil {
			t.Fatalf("decode replacement mcp_status: %v", err)
		}
		statuses = append(statuses, status)
	}

	if !drainTurn(mcpStatusSecondText, mcpStatusSecondReqID) {
		t.Fatal("subsequent ordinary turn ended without its assistant echo")
	}
	if len(statuses) != 1 {
		t.Fatalf("mcp_status frames through subsequent turn = %d, want exactly 1", len(statuses))
	}
	status := statuses[0]
	if status.ConversationID != mcpStatusConvID || status.DroppedServers != 0 {
		t.Errorf("mcp_status identity/count = (%q,%d), want (%q,0)",
			status.ConversationID, status.DroppedServers, mcpStatusConvID)
	}
	if len(status.Servers) != 1 {
		t.Fatalf("mcp_status servers = %+v, want one canned row", status.Servers)
	}
	want := protocol.MCPServerStatus{
		Name:    "pyry_mcp_test",
		Status:  "failed",
		Error:   "canned connection failure",
		Scope:   "local",
		Version: "9.8.7-test",
	}
	if status.Servers[0] != want {
		t.Errorf("mcp_status server = %+v, want %+v", status.Servers[0], want)
	}
}
