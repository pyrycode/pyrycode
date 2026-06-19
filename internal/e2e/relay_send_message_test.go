//go:build e2e

package e2e

// Note: knownText below is a test-only marker. Do NOT paste real secrets
// or tokens into it — the fakeclaude stdin log captures it verbatim and
// the test's failure messages echo the file contents.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelay_SendMessage_AckAndPTYDelivery drives a send_message envelope
// from a paired phone through the relay, the binary's dispatcher, the
// send_message handler, the supervisor's WriteUserTurn, and into the
// supervised fakeclaude child's stdin. Asserts both the wire-level ack
// (with matching in_reply_to) and the marker bytes appearing on the
// child's PTY. The conversation_id association is proven implicitly:
// ValidateConversation gates the supervisor write, so observing both the
// ack AND the marker bytes means the supervisor accepted the seeded
// conversation_id before writing.
func TestRelay_SendMessage_AckAndPTYDelivery(t *testing.T) {
	const (
		knownConvID = "33333333-3333-4333-8333-333333333333"
		knownText   = "e2e-323-marker:hello world\n"
	)

	home := shortHome(t)

	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	pairPayload := decodePairPayload(t, r.Stdout)

	// fakeclaude wiring. trigger is a path that is never created — this
	// test does not exercise the rotation path.
	tmp := t.TempDir()
	// Align the sessions dir to the daemon's COMPUTED path (resolveClaudeSessionsDir
	// has no env override — always <HOME>/.claude/projects/encode(workdir), with
	// HOME=home and -pyry-workdir=home) so the #668 growth-confirm resolver scans
	// exactly the file fakeclaude grows on the delivered turn. rotation_test pattern.
	sessionsDir := filepath.Join(home, ".claude", "projects", encodeWorkdir(home))
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	initialUUID := "44444444-4444-4444-8444-444444444444"
	// Pre-create <initialUUID>.jsonl BEFORE the daemon starts so the first
	// growth-confirm baseline resolves at a tiny offset, not racing fakeclaude's open.
	initialJSONL := filepath.Join(sessionsDir, initialUUID+".jsonl")
	if err := os.WriteFile(initialJSONL, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}
	// Bind knownConvID to the bootstrap session (== initialUUID after
	// reconciliation) so sessionRouter.Route resolves under #678's contract.
	seedBoundConversation(t, home, knownConvID, initialUUID)
	trigger := filepath.Join(tmp, "rotate.trigger.never-created")
	stdinLog := filepath.Join(tmp, "fakeclaude-stdin.log")

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	h := StartRotationWithRelay(t, home, sessionsDir, initialUUID, trigger,
		stdinLog, fr.URL()+"/v1/server", "PYRY_FAKE_CLAUDE_TUI=1")
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

	// 1. hello → hello_ack
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
	if got.InReplyTo == nil || *got.InReplyTo != 1 {
		t.Errorf("hello_ack InReplyTo: got %v, want pointer to 1", got.InReplyTo)
	}

	// 2. send_message → ack
	const reqID uint64 = 2
	req := protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: knownConvID,
			MessageID:      "m-1",
			Text:           knownText,
		}),
	}
	if err := phone.Send(req); err != nil {
		t.Fatalf("phone send send_message: %v", err)
	}
	// Drain until the ack: in TUI mode fakeclaude emits the thinking-spinner
	// glyph on stdin, which the supervisor forwards as a `message` envelope
	// (the cursor is stamped before delivery) racing this synchronous ack.
	ack := recvEnvelope(t, phone, protocol.TypeAck, 3*time.Second)
	if ack.InReplyTo == nil || *ack.InReplyTo != reqID {
		t.Errorf("ack InReplyTo: got %v, want pointer to %d", ack.InReplyTo, reqID)
	}
	if ack.ID < 2 {
		t.Errorf("ack ID: got %d, want >= 2 (hello_ack consumed id=1)", ack.ID)
	}
	var ackPayload protocol.AckPayload
	if err := json.Unmarshal(ack.Payload, &ackPayload); err != nil {
		t.Fatalf("decode ack payload: %v", err)
	}

	// 3. The marker bytes must reach fakeclaude's stdin.
	deadline := time.Now().Add(3 * time.Second)
	var logBytes []byte
	for time.Now().Before(deadline) {
		logBytes, _ = os.ReadFile(stdinLog)
		if bytes.Contains(logBytes, []byte(knownText)) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fakeclaude stdin log did not contain marker %q within deadline\nlog %q contents:\n%s",
		knownText, stdinLog, string(logBytes))
}
