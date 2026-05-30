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
	"testing"
	"time"

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

// waitBinaryHello polls fakerelay until the binary↔relay hello for serverID
// lands (5s deadline) so phone→binary routing doesn't race the WS upgrade.
func waitBinaryHello(t *testing.T, fr *fakerelay.Server, serverID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := fr.LastBinaryHello(serverID); ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("binary↔relay hello not observed within 5s")
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
