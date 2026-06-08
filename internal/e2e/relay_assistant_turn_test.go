//go:build e2e

package e2e

// Note: knownAssistantText below is a test-only marker. Do NOT paste real
// secrets or tokens into it — the message envelope routes through the
// fake relay and is echoed in failure messages.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelay_AssistantTurn_BroadcastsMessageEnvelope drives one inbound
// send_message (which stamps Supervisor.CurrentConversation()), then
// triggers fakeclaude to emit a scripted assistant chunk on stdout. The
// supervisor's PTY-drain goroutine forwards the chunk through
// Bridge.Write; the assistant-turn observer fans out a `message`
// envelope to every active phone conn.
//
// Pinned behaviours: conversation_id round-trip (send_message in →
// message out with the same id), per-conn ID stamping (monotonic above
// the ack), role labelling ("assistant"), MessageID well-formedness.
func TestRelay_AssistantTurn_BroadcastsMessageEnvelope(t *testing.T) {
	const (
		knownConvID        = "55555555-5555-4555-8555-555555555555"
		knownUserText      = "e2e-311-user:hi\n"
		knownAssistantText = "e2e-311-assistant:hello back"
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
	initialUUID := "66666666-6666-4666-8666-666666666666"
	rotateTrigger := filepath.Join(tmp, "rotate.trigger.never-created")
	stdinLog := filepath.Join(tmp, "fakeclaude-stdin.log")
	asstTrigger := filepath.Join(tmp, "assistant.trigger")

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	h := StartRotationWithRelay(t, home, sessionsDir, initialUUID, rotateTrigger,
		stdinLog, fr.URL()+"/v1/server",
		"PYRY_FAKE_CLAUDE_TUI=1",
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

	// 1. hello → hello_ack (id=1).
	hello := protocol.Envelope{
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
	if err := phone.Send(hello); err != nil {
		t.Fatalf("phone send hello: %v", err)
	}
	got, err := phone.Receive(3 * time.Second)
	if err != nil {
		t.Fatalf("phone receive hello_ack: %v", err)
	}
	if got.Type != protocol.TypeHelloAck {
		t.Fatalf("hello_ack Type: got %q, want %q", got.Type, protocol.TypeHelloAck)
	}

	// 2. send_message → ack (id=2). This stamps the supervisor's
	//    CurrentConversation cursor so the assistant-turn bridge can read
	//    it when fakeclaude emits.
	const reqID uint64 = 2
	req := protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: knownConvID,
			MessageID:      "u-1",
			Text:           knownUserText,
		}),
	}
	if err := phone.Send(req); err != nil {
		t.Fatalf("phone send send_message: %v", err)
	}
	// Drain until the ack: in TUI mode fakeclaude emits the thinking-spinner
	// glyph on stdin, forwarded as a `message` envelope (cursor stamped before
	// delivery) that races this synchronous ack.
	ack := recvEnvelope(t, phone, protocol.TypeAck, 3*time.Second)
	if ack.InReplyTo == nil || *ack.InReplyTo != reqID {
		t.Errorf("ack InReplyTo: got %v, want pointer to %d", ack.InReplyTo, reqID)
	}

	// 3. Trigger fakeclaude to emit the scripted assistant chunk on its
	//    stdout. The supervisor's PTY-drain copies it into Bridge.Write,
	//    where the assistant-turn observer fires.
	if err := os.WriteFile(asstTrigger, []byte(knownAssistantText), 0o600); err != nil {
		t.Fatalf("write assistant trigger: %v", err)
	}

	// 4. The phone should observe a `message` envelope with the assistant
	//    text. The supervisor's PTY may produce one or more chunks (a
	//    pre-existing TUI prompt prelude could land first); accept any
	//    number, but require one whose Text contains the marker.
	var matched protocol.Envelope
	var matchedPayload protocol.MessagePayload
	deadline := time.Now().Add(5 * time.Second)
	for {
		remaining := time.Until(deadline)
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
			// Could be prelude (TUI banner / clear); keep draining until
			// the marker chunk arrives or the deadline trips.
			continue
		}
		matched = env
		matchedPayload = p
		break
	}

	if matched.InReplyTo != nil {
		t.Errorf("matched.InReplyTo: got %v, want nil (server-initiated)", matched.InReplyTo)
	}
	if matched.ID < 3 {
		t.Errorf("matched.ID: got %d, want >= 3 (hello_ack=1, ack=2)", matched.ID)
	}
	if matchedPayload.ConversationID != knownConvID {
		t.Errorf("ConversationID: got %q, want %q", matchedPayload.ConversationID, knownConvID)
	}
	if matchedPayload.Role != "assistant" {
		t.Errorf("Role: got %q, want %q", matchedPayload.Role, "assistant")
	}
	if !conversations.ValidID(matchedPayload.MessageID) {
		t.Errorf("MessageID %q is not a valid UUIDv4", matchedPayload.MessageID)
	}
}

