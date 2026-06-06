//go:build e2e

package e2e

// Note: knownUserText and knownAssistantText below are test-only markers.
// Do NOT paste real secrets or tokens into them — they flow through the
// fake relay and are echoed verbatim in failure messages.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelay_Roundtrip_Appendix drives the appendix-flow happy path from
// docs/protocol-mobile.md on a single phone connection: hello,
// list_conversations, send_message (+ assistant-turn echo),
// register_push_token. Asserts cross-step invariants the per-slice tests
// can't see in isolation: monotonic binary-side ids over a mixed verb
// set, in_reply_to chaining, ts round-trip on the wire, and
// conversation_id stability from send_message through the message echo.
func TestRelay_Roundtrip_Appendix(t *testing.T) {
	const (
		knownConvID        = "77777777-7777-4777-8777-777777777777"
		knownUserText      = "e2e-297-user:hi\n"
		knownAssistantText = "e2e-297-assistant:hello back"
	)

	home := shortHome(t)

	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	pairPayload := decodePairPayload(t, r.Stdout)

	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[{"id":"` + knownConvID +
		`","cwd":"` + home +
		`","is_promoted":false,"last_used_at":"2026-01-01T00:00:00Z"}]}`)
	if err := os.WriteFile(convPath, convJSON, 0o600); err != nil {
		t.Fatalf("seed conversations.json: %v", err)
	}

	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "claude-sessions")
	initialUUID := "88888888-8888-4888-8888-888888888888"
	rotateTrigger := filepath.Join(tmp, "rotate.trigger.never-created")
	stdinLog := filepath.Join(tmp, "fakeclaude-stdin.log")
	asstTrigger := filepath.Join(tmp, "assistant.trigger")

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	h := StartRotationWithRelay(t, home, sessionsDir, initialUUID, rotateTrigger,
		stdinLog, fr.URL()+"/v1/server",
		"PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER="+asstTrigger,
	)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !fr.WaitBinary(ctx, serverID) {
		t.Fatal("binary connection not registered within 5s")
	}

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, pairPayload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	// Collect every received envelope so the end-of-test invariant
	// pass can walk them as one sequence.
	var received []protocol.Envelope

	// Step 1: hello (id=1) → hello_ack [N₁].
	helloEnv := protocol.Envelope{
		ID:   1,
		Type: protocol.TypeHello,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.HelloClientPayload{
			Role:             "client",
			DeviceName:       "phone-a",
			ClientVersion:    "0.0.1-test",
			ProtocolVersions: []string{"v1"},
		}),
	}
	if err := phone.Send(helloEnv); err != nil {
		t.Fatalf("phone send hello: %v", err)
	}
	helloAck, err := phone.Receive(3 * time.Second)
	if err != nil {
		t.Fatalf("phone receive hello_ack: %v", err)
	}
	if helloAck.Type != protocol.TypeHelloAck {
		t.Fatalf("hello_ack Type: got %q, want %q", helloAck.Type, protocol.TypeHelloAck)
	}
	if helloAck.InReplyTo == nil || *helloAck.InReplyTo != helloEnv.ID {
		t.Errorf("hello_ack InReplyTo: got %v, want pointer to %d", helloAck.InReplyTo, helloEnv.ID)
	}
	received = append(received, helloAck)

	// Step 2: list_conversations (id=2) → conversations [N₂].
	listReq := protocol.Envelope{
		ID:      2,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.ListConversationsPayload{}),
	}
	if err := phone.Send(listReq); err != nil {
		t.Fatalf("phone send list_conversations: %v", err)
	}
	convs, err := phone.Receive(3 * time.Second)
	if err != nil {
		t.Fatalf("phone receive conversations: %v", err)
	}
	if convs.Type != protocol.TypeConversations {
		t.Fatalf("conversations Type: got %q, want %q (payload=%s)",
			convs.Type, protocol.TypeConversations, string(convs.Payload))
	}
	if convs.InReplyTo == nil || *convs.InReplyTo != listReq.ID {
		t.Errorf("conversations InReplyTo: got %v, want pointer to %d", convs.InReplyTo, listReq.ID)
	}
	var convsPayload protocol.ConversationsPayload
	if err := json.Unmarshal(convs.Payload, &convsPayload); err != nil {
		t.Fatalf("decode conversations payload: %v", err)
	}
	if got, want := len(convsPayload.Conversations), 1; got != want {
		t.Fatalf("conversations rows: got %d, want %d (payload=%s)", got, want, string(convs.Payload))
	}
	if got := convsPayload.Conversations[0].ID; got != knownConvID {
		t.Errorf("conversations[0].ID: got %q, want %q", got, knownConvID)
	}
	received = append(received, convs)

	// Step 3: send_message (id=3) → ack [N₃]. Stamps the supervisor's
	// CurrentConversation cursor so the assistant-turn bridge can echo.
	sendReq := protocol.Envelope{
		ID:   3,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: knownConvID,
			MessageID:      "u-1",
			Text:           knownUserText,
		}),
	}
	if err := phone.Send(sendReq); err != nil {
		t.Fatalf("phone send send_message: %v", err)
	}
	sendAck, err := phone.Receive(3 * time.Second)
	if err != nil {
		t.Fatalf("phone receive ack: %v", err)
	}
	if sendAck.Type != protocol.TypeAck {
		t.Fatalf("ack Type: got %q, want %q (payload=%s)",
			sendAck.Type, protocol.TypeAck, string(sendAck.Payload))
	}
	if sendAck.InReplyTo == nil || *sendAck.InReplyTo != sendReq.ID {
		t.Errorf("ack InReplyTo: got %v, want pointer to %d", sendAck.InReplyTo, sendReq.ID)
	}
	received = append(received, sendAck)

	// Step 4: trigger the assistant turn; the supervisor's PTY-drain
	// forwards the chunk through Bridge.Write and the assistant-turn
	// observer fans out a `message` envelope. Drain the phone until the
	// marker chunk arrives — the TUI may emit prelude chunks first.
	if err := os.WriteFile(asstTrigger, []byte(knownAssistantText), 0o600); err != nil {
		t.Fatalf("write assistant trigger: %v", err)
	}
	var msgEnv protocol.Envelope
	var msgPayload protocol.MessagePayload
	drainDeadline := time.Now().Add(5 * time.Second)
	for {
		remaining := time.Until(drainDeadline)
		if remaining <= 0 {
			t.Fatal("did not observe a message envelope containing the assistant marker before deadline")
		}
		env, err := phone.Receive(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				t.Fatal("did not observe a message envelope containing the assistant marker before deadline")
			}
			t.Fatalf("phone receive message envelope: %v", err)
		}
		if env.Type != protocol.TypeMessage {
			t.Logf("ignoring non-message envelope type=%q id=%d", env.Type, env.ID)
			continue
		}
		var p protocol.MessagePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("decode message payload: %v", err)
		}
		if !strings.Contains(p.Text, knownAssistantText) {
			continue
		}
		msgEnv = env
		msgPayload = p
		break
	}
	if msgEnv.InReplyTo != nil {
		t.Errorf("message envelope InReplyTo: got %v, want nil (server-initiated)", msgEnv.InReplyTo)
	}
	if msgPayload.ConversationID != knownConvID {
		t.Errorf("message ConversationID: got %q, want %q", msgPayload.ConversationID, knownConvID)
	}
	if msgPayload.Role != "assistant" {
		t.Errorf("message Role: got %q, want %q", msgPayload.Role, "assistant")
	}
	received = append(received, msgEnv)

	// Step 5: register_push_token (id=4) → ack [N₅]. On-disk persistence
	// is pinned by TestRelay_RegisterPushToken_AckAndPersists; here we
	// only assert the envelope-level reply.
	pushReq := protocol.Envelope{
		ID:   4,
		Type: protocol.TypeRegisterPushToken,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.RegisterPushTokenPayload{
			Platform:   "fcm",
			Token:      "fcm-token-xyz",
			DeviceName: "phone-a",
		}),
	}
	if err := phone.Send(pushReq); err != nil {
		t.Fatalf("phone send register_push_token: %v", err)
	}
	pushAck, err := phone.Receive(3 * time.Second)
	if err != nil {
		t.Fatalf("phone receive register_push_token ack: %v", err)
	}
	if pushAck.Type != protocol.TypeAck {
		t.Fatalf("register_push_token ack Type: got %q, want %q (payload=%s)",
			pushAck.Type, protocol.TypeAck, string(pushAck.Payload))
	}
	if pushAck.InReplyTo == nil || *pushAck.InReplyTo != pushReq.ID {
		t.Errorf("register_push_token ack InReplyTo: got %v, want pointer to %d",
			pushAck.InReplyTo, pushReq.ID)
	}
	received = append(received, pushAck)

	// Invariants.
	//
	// 1. Monotonic binary-side ids across the full sequence.
	for i := 1; i < len(received); i++ {
		prev, cur := received[i-1], received[i]
		if cur.ID <= prev.ID {
			t.Errorf("binary-side ID not strictly increasing at index %d: prev=%d (type=%q), cur=%d (type=%q)",
				i, prev.ID, prev.Type, cur.ID, cur.Type)
		}
	}

	// 2. ts round-trip: every received envelope has a non-zero ts that
	//    survives a JSON round-trip with time.Time.Equal (the monotonic-
	//    clock reading is stripped by json.Marshal — see PROJECT-MEMORY.md).
	for i, env := range received {
		if env.TS.IsZero() {
			t.Errorf("received[%d] (type=%q id=%d) has zero TS", i, env.Type, env.ID)
			continue
		}
		rt, err := roundTripTS(env.TS)
		if err != nil {
			t.Fatalf("roundTripTS received[%d]: %v", i, err)
		}
		if !rt.Equal(env.TS) {
			t.Errorf("received[%d] (type=%q id=%d) TS round-trip mismatch: orig=%v rt=%v",
				i, env.Type, env.ID, env.TS, rt)
		}
	}
}

// roundTripTS marshals t through JSON and back. The monotonic-clock
// reading on time.Time is stripped by json.Marshal, so the returned
// value can only be compared with time.Time.Equal — never with ==
// or reflect.DeepEqual.
func roundTripTS(t time.Time) (time.Time, error) {
	type tsWrapper struct {
		TS time.Time `json:"ts"`
	}
	b, err := json.Marshal(tsWrapper{TS: t})
	if err != nil {
		return time.Time{}, err
	}
	var out tsWrapper
	if err := json.Unmarshal(b, &out); err != nil {
		return time.Time{}, err
	}
	return out.TS, nil
}
