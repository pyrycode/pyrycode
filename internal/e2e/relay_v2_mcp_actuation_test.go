//go:build e2e

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	mcpActInitialUUID = "24200000-0000-4000-8000-000000000001"
	mcpActConvID      = "24200000-0000-4000-8000-000000000002"
	mcpActWarmupText  = "e2e-mcp-actuate:warmup\n"
	mcpActCausalText  = "e2e-mcp-actuate:causal\n"
	mcpActWarmupID    = uint64(24200)
	mcpActReconnectID = uint64(24201)
	mcpActToggleID    = uint64(24202)
	mcpActRefusedID   = uint64(24203)
	mcpActCausalID    = uint64(24204)
	// mcpActServerName is the row the fake child's SECOND and later mcp_status answers
	// carry. The startup report is the first, so by the time a phone actuates, this is
	// the name the membership read will find — a request naming anything else exercises
	// the unknown-server refusal instead.
	mcpActServerName = "pyry_mcp_fresh"
)

// sendMCPActTurn drives one ordinary turn so the daemon's active-conversation cursor
// exists before anything is actuated: a reply that wrongly entered the shared turn sink
// then has a live fan-out target, so "no copy on the second client" is a real assertion
// rather than one that passes because nothing could have been delivered anywhere.
func sendMCPActTurn(t *testing.T, phone mcpQueryPhone, requestID uint64, text string) {
	t.Helper()
	sendSealedEnvelope(t, phone.client, phone.send, protocol.Envelope{
		ID:   requestID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: mcpActConvID,
			MessageID:      "message-mcp-actuate",
			Text:           text,
		}),
	})
}

// drainMCPActTurn runs one turn to its end and reports how many mcp_status frames
// reached this phone on the way. Copied in shape from drainMCPQueryTurn rather than
// shared with it, because that helper is bound to its own conversation id.
func drainMCPActTurn(t *testing.T, phone mcpQueryPhone, text string) (statuses int) {
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

// awaitMCPActuationReply reads until the correlated answer to one actuation arrives and
// returns the fresh status it carries. An error frame fails loudly with its code: the
// accepted arms of this test must never see one, and the refused arm reads the reply
// itself rather than through here.
func awaitMCPActuationReply(t *testing.T, phone mcpQueryPhone, inReplyTo uint64) protocol.MCPStatusPayload {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		env, ok := nextAttachmentEnvelope(t, phone.client, phone.recv, deadline)
		if !ok {
			t.Fatalf("no correlated mcp_status for actuation %d", inReplyTo)
		}
		if env.Type == protocol.TypeError {
			t.Fatalf("actuation %d was refused: %s", inReplyTo, env.Payload)
		}
		if env.Type != protocol.TypeMCPStatus {
			continue
		}
		if env.InReplyTo == nil || *env.InReplyTo != inReplyTo {
			t.Fatalf("mcp_status in_reply_to = %v, want %d", env.InReplyTo, inReplyTo)
		}
		if env.EventID != nil {
			t.Fatalf("actuation reply event_id = %v, want nil: it is a reply, not a push", env.EventID)
		}
		var payload protocol.MCPStatusPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			t.Fatalf("decode fresh mcp_status: %v", err)
		}
		return payload
	}
}

// TestRelayV2_MCPActuationGatedAuditedAndAnsweredFresh is the hermetic daemon proof
// (AC-5), plus AC-1 end to end: two real phones against one real daemon and a fake child
// that answers both verbs.
//
// It asserts four things no unit test can. The eligible phone's reconnect and toggle
// each come back as exactly ONE correlated mcp_status carrying the POST-ACKNOWLEDGEMENT
// read — the fake reports `pending` only after it has accepted an actuation, so the
// status returned cannot be the membership read that preceded it. The second phone
// receives no copy of either, measured against the same later-turn causal boundary the
// status-request spec uses. The unprivileged phone's reconnect is refused with the one
// merged code and reaches no child. And the daemon wrote one audit record per action,
// naming the conversation, the server and the device.
func TestRelayV2_MCPActuationGatedAuditedAndAnsweredFresh(t *testing.T) {
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	// phone-a carries the remote-permission bit; phone-b deliberately does not, so the
	// same daemon holds one device the gate admits and one it refuses.
	payloadA, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: true,
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

	seedBoundConversation(t, home, mcpActConvID, mcpActInitialUUID)
	h := StartStreamInteractiveWithRelay(t, home, mcpActInitialUUID, relayURL,
		"PYRY_FAKE_CLAUDE_MCP_STATUS=1")
	t.Cleanup(func() { h.Stop(t) })
	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	// The automatic startup report is the child's first mcp_status request. Waiting for
	// its fixed no-cursor drop makes the membership read below the fake's changed-state
	// answer — the one naming mcpActServerName — rather than racing startup.
	waitForDaemonEvent(t, h, "kind=mcp_status", 10*time.Second)
	phoneA := dialMCPQueryPhone(t, fr, serverID, pubKey, payloadA.Token, "phone-a")
	phoneB := dialMCPQueryPhone(t, fr, serverID, pubKey, payloadB.Token, "phone-b")

	sendMCPActTurn(t, phoneA, mcpActWarmupID, mcpActWarmupText)
	if got := drainMCPActTurn(t, phoneA, mcpActWarmupText); got != 0 {
		t.Fatalf("phone-a warmup MCP statuses = %d, want 0", got)
	}
	if got := drainMCPActTurn(t, phoneB, mcpActWarmupText); got != 0 {
		t.Fatalf("phone-b warmup MCP statuses = %d, want 0", got)
	}

	sendSealedEnvelope(t, phoneA.client, phoneA.send, protocol.Envelope{
		ID:   mcpActReconnectID,
		Type: protocol.TypeMCPReconnect,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.MCPReconnectPayload{
			ConversationID: mcpActConvID,
			ServerName:     mcpActServerName,
		}),
	})
	reconnected := awaitMCPActuationReply(t, phoneA, mcpActReconnectID)
	assertPostAckStatus(t, "reconnect", reconnected)

	sendSealedEnvelope(t, phoneA.client, phoneA.send, protocol.Envelope{
		ID:   mcpActToggleID,
		Type: protocol.TypeMCPToggle,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.MCPTogglePayload{
			ConversationID: mcpActConvID,
			ServerName:     mcpActServerName,
			Enabled:        false,
		}),
	})
	toggled := awaitMCPActuationReply(t, phoneA, mcpActToggleID)
	assertPostAckStatus(t, "toggle", toggled)

	// The unprivileged phone's turn. Its refusal must be the one merged code, which says
	// nothing about whether the named server exists or whether the asker is privileged.
	sendSealedEnvelope(t, phoneB.client, phoneB.send, protocol.Envelope{
		ID:   mcpActRefusedID,
		Type: protocol.TypeMCPReconnect,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.MCPReconnectPayload{
			ConversationID: mcpActConvID,
			ServerName:     mcpActServerName,
		}),
	})
	assertMCPActuationRefused(t, phoneB, mcpActRefusedID)

	// A later turn is the causal read boundary: anything admitted to the shared sink
	// before it must reach both phones before their turn_end.
	sendMCPActTurn(t, phoneA, mcpActCausalID, mcpActCausalText)
	if got := drainMCPActTurn(t, phoneA, mcpActCausalText); got != 0 {
		t.Errorf("phone-a saw %d extra MCP statuses after its two replies, want 0", got)
	}
	if got := drainMCPActTurn(t, phoneB, mcpActCausalText); got != 0 {
		t.Errorf("observer phone MCP statuses = %d, want 0: an actuation answers its "+
			"requester only and is never broadcast", got)
	}

	assertMCPActuationAudit(t, h)
}

// assertPostAckStatus pins that an accepted actuation answered with a read taken AFTER
// the child acknowledged it. The fake reports `pending` only once it has accepted one,
// so `connected` here would mean the membership read was returned instead — and a
// pending row must come back as read rather than being waited out.
func assertPostAckStatus(t *testing.T, verb string, got protocol.MCPStatusPayload) {
	t.Helper()
	if got.ConversationID != mcpActConvID {
		t.Errorf("%s answer conversation = %q, want %q", verb, got.ConversationID, mcpActConvID)
	}
	if len(got.Servers) != 1 {
		t.Fatalf("%s answer servers = %+v, want exactly one row", verb, got.Servers)
	}
	if got.Servers[0].Name != mcpActServerName {
		t.Errorf("%s answer named %q, want %q", verb, got.Servers[0].Name, mcpActServerName)
	}
	if got.Servers[0].Status != "pending" {
		t.Errorf("%s answer status = %q, want pending: the answer must be the read taken "+
			"after the child's acknowledgement, not the membership read", verb, got.Servers[0].Status)
	}
}

// assertMCPActuationRefused reads the unprivileged phone's correlated answer and pins
// that it is the single merged reject rather than a status frame.
func assertMCPActuationRefused(t *testing.T, phone mcpQueryPhone, inReplyTo uint64) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		env, ok := nextAttachmentEnvelope(t, phone.client, phone.recv, deadline)
		if !ok {
			t.Fatal("the gate neither answered nor refused an unprivileged device's actuation")
		}
		if env.Type == protocol.TypeMCPStatus {
			t.Fatal("an unprivileged device received an MCP status in reply to an actuation")
		}
		if env.Type != protocol.TypeError {
			continue
		}
		if env.InReplyTo == nil || *env.InReplyTo != inReplyTo {
			continue
		}
		var errPayload protocol.ErrorPayload
		if err := json.Unmarshal(env.Payload, &errPayload); err != nil {
			t.Fatalf("decode error reply: %v", err)
		}
		if errPayload.Code != protocol.CodeMCPActuationRefused {
			t.Errorf("refusal code = %q, want %q", errPayload.Code, protocol.CodeMCPActuationRefused)
		}
		if errPayload.Retryable {
			t.Error("refusal is retryable; a retryable refusal reads as transient, therefore not " +
				"the gate, re-splitting on one bit what one code deliberately joined")
		}
		return
	}
}

// assertMCPActuationAudit reads the daemon's own log for one record per action.
//
// EVERY MATCH IS A CONTIGUOUS ATTRIBUTE PAIR, never a single key, because audit.Log's
// attribute order is fixed and single keys are not unique to it: `conversation_id=` alone
// matched six lines on the first run of this spec, most of them ordinary relay
// diagnostics. Pairing a key with its neighbour pins the record family, and the
// `modal_id=""` half doubles as the assertion that an actuation names no modal nonce.
//
// IT NEVER PRINTS THE BUFFER, waitForDaemonEvent's rule: this harness runs the daemon at
// debug level, so interpolating its stderr would publish every record it wrote,
// pairing material included.
func assertMCPActuationAudit(t *testing.T, h *Harness) {
	t.Helper()
	log := h.Stderr.String()
	for _, want := range []struct {
		what  string
		match string
		count int
	}{
		{"accepted actuations", "target=" + mcpActServerName + " outcome=allowed", 2},
		{"gate refusals", "target=" + mcpActServerName + " outcome=denied_unauthorized", 1},
		{"reconnect records", "modal_class=" + protocol.TypeMCPReconnect, 2},
		{"toggle records", "modal_class=" + protocol.TypeMCPToggle, 1},
		{"conversation named", "conversation_id=" + mcpActConvID + " target=" + mcpActServerName, 3},
		{"eligible device named", `device_label=phone-a modal_id=""`, 2},
		{"refused device named", `device_label=phone-b modal_id=""`, 1},
	} {
		if got := strings.Count(log, want.match); got != want.count {
			t.Errorf("audit records matching %s = %d, want %d", want.what, got, want.count)
		}
	}
}
