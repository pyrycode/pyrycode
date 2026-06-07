//go:build e2e

package e2e

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_Daemon proves the #549 cutover at the daemon boundary: a real
// spawned pyry binary, not the inline V2SessionManager harness from
// relay_v2_handshake_test.go. The two subtests pin both sides of the
// PYRY_MOBILE_V2 switch:
//
//   - v2_enabled_list_conversations_round_trip — switch on, a paired phone
//     completes the Noise_IK handshake against the daemon and round-trips
//     list_conversations → conversations over the encrypted channel (AC#1,
//     AC#4; the successful round-trip also exercises AC#3's handler table).
//   - v2_disabled_does_not_engage_v2 — switch unset (default), the v1
//     first-frame auth gate handles a noise_init exactly as today and replies
//     hello_ack; no noise_resp is ever produced (AC#2).
func TestRelayV2_Daemon(t *testing.T) {
	t.Run("v2_enabled_list_conversations_round_trip", testV2DaemonListConversationsRoundTrip)
	t.Run("v2_disabled_does_not_engage_v2", testV2DaemonDisabledDoesNotEngageV2)
}

// driveHandshakeToOpenDaemon mirrors relay_v2_handshake_test.go's
// driveHandshakeToOpen but takes the responder static pubkey directly (decoded
// from the pair payload) instead of reading it off the inline-harness struct.
// Returns the initiator's CipherStates ready for open-state dispatch (initSend
// encrypts phone→binary, initRecv decrypts binary→phone).
func driveHandshakeToOpenDaemon(t *testing.T, phone *fakephone.Client, pubKey []byte, token string) (*noise.CipherState, *noise.CipherState) {
	t.Helper()
	initPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("phone keygen: %v", err)
	}
	initiator, err := noise.NewInitiator(initPriv.Bytes(), pubKey)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarly(t, token))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	sendNoiseInit(t, phone, initMsg)

	inner := readInnerFrame(t, phone, 3*time.Second)
	if inner.Type != protocol.TypeNoiseResp {
		t.Fatalf("handshake: got inner type %q, want %q", inner.Type, protocol.TypeNoiseResp)
	}
	respRaw, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatalf("decode noise_resp data: %v", err)
	}
	_, initSend, initRecv, err := initiator.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("initiator.ReadResp: %v", err)
	}
	return initSend, initRecv
}

// waitBinaryHello blocks until the binary's relay connection registers for
// serverID (5s deadline) so phone→binary routing doesn't race the WS upgrade.
func waitBinaryHello(t *testing.T, fr *fakerelay.Server, serverID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !fr.WaitBinary(ctx, serverID) {
		t.Fatal("binary connection not registered within 5s")
	}
}

// testV2DaemonListConversationsRoundTrip drives a spawned daemon with the v2
// switch enabled through a real Noise_IK handshake and a
// list_conversations → conversations round-trip over the encrypted channel.
func testV2DaemonListConversationsRoundTrip(t *testing.T) {
	const knownConvID = "77777777-7777-4777-8777-777777777777"
	home := shortHome(t)

	// Pair a device: yields the bearer token and the responder static pubkey
	// the phone pins. The daemon loads the same static key on startup.
	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	payload := decodePairPayload(t, r.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Seed one known conversation row the handler will read back.
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[{"id":"` + knownConvID +
		`","cwd":"` + home +
		`","is_promoted":false,"last_used_at":"2026-01-01T00:00:00Z"}]}`)
	if err := os.WriteFile(convPath, convJSON, 0o600); err != nil {
		t.Fatalf("seed conversations.json: %v", err)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	h := StartInWithEnv(t, home,
		[]string{"PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_MOBILE_V2=1"},
		"-pyry-relay="+fr.URL()+"/v2/server",
	)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	initSend, initRecv := driveHandshakeToOpenDaemon(t, phone, pubKey, payload.Token)

	const reqID uint64 = 21
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("marshal request envelope: %v", err)
	}
	ciphertext, err := initSend.Encrypt(reqEnv)
	if err != nil {
		t.Fatalf("seal request envelope: %v", err)
	}
	sendNoiseMsg(t, phone, ciphertext)

	inner := readInnerFrame(t, phone, 3*time.Second)
	if inner.Type != protocol.TypeNoiseMsg {
		t.Fatalf("reply inner type = %q, want %q", inner.Type, protocol.TypeNoiseMsg)
	}
	replyCipher, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatalf("decode reply data: %v", err)
	}
	replyPlain, err := initRecv.Decrypt(replyCipher)
	if err != nil {
		t.Fatalf("phone decrypt reply: %v", err)
	}
	var replyEnv protocol.Envelope
	if err := json.Unmarshal(replyPlain, &replyEnv); err != nil {
		t.Fatalf("decode reply envelope: %v", err)
	}
	if replyEnv.Type != protocol.TypeConversations {
		t.Fatalf("reply Type = %q, want %q (payload=%s)",
			replyEnv.Type, protocol.TypeConversations, string(replyEnv.Payload))
	}
	if replyEnv.InReplyTo == nil || *replyEnv.InReplyTo != reqID {
		t.Errorf("InReplyTo = %v, want pointer to %d", replyEnv.InReplyTo, reqID)
	}
	var convsPayload protocol.ConversationsPayload
	if err := json.Unmarshal(replyEnv.Payload, &convsPayload); err != nil {
		t.Fatalf("decode conversations payload: %v", err)
	}
	if got, want := len(convsPayload.Conversations), 1; got != want {
		t.Fatalf("conversations rows: got %d, want %d (payload=%s)",
			got, want, string(replyEnv.Payload))
	}
	if got := convsPayload.Conversations[0].ID; got != knownConvID {
		t.Errorf("conversations[0].ID = %q, want %q", got, knownConvID)
	}
}

// testV2DaemonDisabledDoesNotEngageV2 starts the daemon with the switch unset
// (v1 default). A phone's noise_init is decoded by the v1 first-frame auth
// gate as an ordinary envelope, the paired token is accepted, and the reply
// is a v1 hello_ack — never a noise_resp, proving the v2 manager is not
// engaged. (The unpaired-token 4401 reject is covered by
// TestRelay_AuthReject_4401; this subtest's distinct value is the v2-off
// signal.)
func testV2DaemonDisabledDoesNotEngageV2(t *testing.T) {
	home := shortHome(t)

	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-b")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	payload := decodePairPayload(t, r.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// Switch unset: no PYRY_MOBILE_V2. Default v1 path, /v1/server route.
	h := StartInWithEnv(t, home,
		[]string{"PYRY_ALLOW_INSECURE_RELAY=1"},
		"-pyry-relay="+fr.URL()+"/v1/server",
	)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-b")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	// The phone attempts a v2 Noise handshake. With v2 disabled, the daemon's
	// v1 dispatcher decodes the noise_init frame as an ordinary first-frame
	// envelope and the auth gate replies hello_ack for the paired token.
	initPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("phone keygen: %v", err)
	}
	initiator, err := noise.NewInitiator(initPriv.Bytes(), pubKey)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarly(t, payload.Token))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	sendNoiseInit(t, phone, initMsg)

	got, err := phone.Receive(3 * time.Second)
	if err != nil {
		t.Fatalf("phone receive reply: %v", err)
	}
	if got.Type == protocol.TypeNoiseResp {
		t.Fatal("got noise_resp: v2 manager engaged with PYRY_MOBILE_V2 unset")
	}
	if got.Type != protocol.TypeHelloAck {
		t.Fatalf("reply Type = %q, want %q (v1 hello_ack)", got.Type, protocol.TypeHelloAck)
	}
}

// decryptInnerEnvelope base64-decodes a binary→phone noise_msg inner frame,
// decrypts it under the phone's receive CipherState (whose nonce is
// sequential — every such frame must be decrypted in capture order), and
// unmarshals the inner application Envelope. Used for both the solicited ack
// and the unsolicited assistant-turn messages in the round-trip below.
func decryptInnerEnvelope(t *testing.T, inner protocol.InnerFrameV2, cs *noise.CipherState) protocol.Envelope {
	t.Helper()
	if inner.Type != protocol.TypeNoiseMsg {
		t.Fatalf("inner frame type = %q, want %q", inner.Type, protocol.TypeNoiseMsg)
	}
	cipher, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatalf("decode inner data: %v", err)
	}
	plain, err := cs.Decrypt(cipher)
	if err != nil {
		t.Fatalf("phone decrypt: %v", err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(plain, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return env
}

// TestRelayV2_AssistantTurn_BroadcastsMessageEnvelope is the v2 analog of
// TestRelay_AssistantTurn_BroadcastsMessageEnvelope (#311): it proves the
// return half of a conversation over the encrypted (Noise) path. A paired
// phone completes the handshake, sends a sealed send_message (which stamps
// the supervisor's CurrentConversation cursor and is acked), then triggers
// fakeclaude to emit a scripted assistant chunk on stdout. The v2
// assistant-turn bridge (#589) taps Bridge.Write, mints a `message`
// envelope, and Pushes it sealed to every open v2 session; the phone
// decrypts it under its session receive key. (AC#1, AC#4.)
func TestRelayV2_AssistantTurn_BroadcastsMessageEnvelope(t *testing.T) {
	const (
		knownConvID        = "88888888-8888-4888-8888-888888888888"
		knownUserText      = "e2e-589-user:hi\n"
		knownAssistantText = "e2e-589-assistant:hello v2"
	)

	home := shortHome(t)

	// Pair: yields the bearer token and the responder static pubkey the phone
	// pins; the daemon loads the same static key on startup.
	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	payload := decodePairPayload(t, r.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Seed the conversation row the cursor will reference.
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[{"id":"` + knownConvID +
		`","cwd":"` + home +
		`","is_promoted":false,"last_used_at":"2026-01-01T00:00:00Z"}]}`)
	if err := os.WriteFile(convPath, convJSON, 0o600); err != nil {
		t.Fatalf("seed conversations.json: %v", err)
	}

	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "claude-sessions")
	initialUUID := "99999999-9999-4999-8999-999999999999"
	rotateTrigger := filepath.Join(tmp, "rotate.trigger.never-created")
	stdinLog := filepath.Join(tmp, "fakeclaude-stdin.log")
	asstTrigger := filepath.Join(tmp, "assistant.trigger")

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// fakeclaude on the v2 leg: PYRY_MOBILE_V2=1 selects the Noise manager;
	// the assistant trigger scripts a finished assistant turn on demand.
	h := StartRotationWithRelay(t, home, sessionsDir, initialUUID, rotateTrigger,
		stdinLog, fr.URL()+"/v2/server",
		"PYRY_MOBILE_V2=1",
		"PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER="+asstTrigger,
	)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	initSend, initRecv := driveHandshakeToOpenDaemon(t, phone, pubKey, payload.Token)

	// Sealed send_message → ack. This stamps CurrentConversation() so the
	// bridge has a cursor when fakeclaude emits.
	const reqID uint64 = 21
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: knownConvID,
			MessageID:      "u-1",
			Text:           knownUserText,
		}),
	})
	if err != nil {
		t.Fatalf("marshal send_message envelope: %v", err)
	}
	ciphertext, err := initSend.Encrypt(reqEnv)
	if err != nil {
		t.Fatalf("seal send_message envelope: %v", err)
	}
	sendNoiseMsg(t, phone, ciphertext)

	ackEnv := decryptInnerEnvelope(t, readInnerFrame(t, phone, 3*time.Second), initRecv)
	if ackEnv.Type != protocol.TypeAck {
		t.Fatalf("reply Type = %q, want %q (payload=%s)",
			ackEnv.Type, protocol.TypeAck, string(ackEnv.Payload))
	}
	if ackEnv.InReplyTo == nil || *ackEnv.InReplyTo != reqID {
		t.Errorf("ack InReplyTo = %v, want pointer to %d", ackEnv.InReplyTo, reqID)
	}

	// Trigger fakeclaude to emit the scripted assistant chunk on stdout.
	if err := os.WriteFile(asstTrigger, []byte(knownAssistantText), 0o600); err != nil {
		t.Fatalf("write assistant trigger: %v", err)
	}

	// Loop-until-marker, decrypting every binary→phone frame in order (the
	// receive CipherState nonce is sequential — the phone cannot skip a
	// frame). Tolerate non-message envelopes and prelude `message` chunks
	// (TUI banner) until the marker chunk arrives.
	var matched protocol.Envelope
	var matchedPayload protocol.MessagePayload
	deadline := time.Now().Add(5 * time.Second)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatal("did not observe a message envelope containing the assistant marker before deadline")
		}
		env := decryptInnerEnvelope(t, readInnerFrame(t, phone, remaining), initRecv)
		if env.Type != protocol.TypeMessage {
			continue
		}
		var p protocol.MessagePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("decode message payload: %v", err)
		}
		if !strings.Contains(p.Text, knownAssistantText) {
			continue // prelude chunk; keep draining
		}
		matched = env
		matchedPayload = p
		break
	}

	if matched.InReplyTo != nil {
		t.Errorf("matched.InReplyTo: got %v, want nil (server-initiated)", matched.InReplyTo)
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
