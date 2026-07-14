//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_Promote proves the #949 fix at the daemon boundary: a paired phone
// completes the Noise_IK handshake against a real spawned daemon and round-trips
// promote_conversation → conversation_updated over the encrypted channel, and
// the promotion lands in the on-disk registry (AC #4).
//
// RED on main: before the handler was registered in V2SessionConfig.Handlers,
// promote_conversation fell through the no-handler arm to a protocol.unsupported
// error reply, so the `want conversation_updated` assertion fails. GREEN after
// the registration + handler.
func TestRelayV2_Promote(t *testing.T) {
	t.Run("v2_enabled_promote_conversation_round_trip", testV2DaemonPromoteRoundTrip)
}

// testV2DaemonPromoteRoundTrip drives a spawned daemon with the v2 switch
// enabled through a real Noise_IK handshake and a promote_conversation →
// conversation_updated round-trip over the encrypted channel, then reads the
// registry back off disk to prove the promotion persisted.
func testV2DaemonPromoteRoundTrip(t *testing.T) {
	const (
		scratchConvID = "88888888-8888-4888-8888-888888888888"
		channelName   = "my-channel"
	)
	home := shortHome(t)

	// Pair a device: yields the bearer token and the responder static pubkey the
	// phone pins. The daemon loads the same static key on startup.
	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	payload := decodePairPayload(t, r.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Seed one unpromoted scratch conversation the promote will target. Its cwd
	// is the channel's inherited workspace (Option B) — the reply must echo it.
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[{"id":"` + scratchConvID +
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

	// Seal and send promote_conversation. The payload carries a cwd (spec-required
	// on the wire) that the daemon deliberately does NOT consume — the promoted
	// channel inherits the seeded scratch cwd (Option B).
	const reqID uint64 = 41
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypePromoteConversation,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.PromoteConversationPayload{
			ConversationID: scratchConvID,
			Name:           channelName,
			Cwd:            home,
		}),
	})
	if err != nil {
		t.Fatalf("marshal request envelope: %v", err)
	}
	ciphertext, err := initSend.Encrypt(reqEnv)
	if err != nil {
		t.Fatalf("seal request envelope: %v", err)
	}
	sendNoiseMsg(t, phone, ciphertext)

	reply := decryptInnerEnvelope(t, readInnerFrame(t, phone, 3*time.Second), initRecv)
	if reply.Type != protocol.TypeConversationUpdated {
		t.Fatalf("reply Type = %q, want %q (payload=%s)",
			reply.Type, protocol.TypeConversationUpdated, string(reply.Payload))
	}
	if reply.InReplyTo == nil || *reply.InReplyTo != reqID {
		t.Errorf("InReplyTo = %v, want pointer to %d", reply.InReplyTo, reqID)
	}
	var updated protocol.ConversationUpdatedPayload
	if err := json.Unmarshal(reply.Payload, &updated); err != nil {
		t.Fatalf("decode conversation_updated payload: %v", err)
	}
	if updated.ID != scratchConvID {
		t.Errorf("reply ID = %q, want %q", updated.ID, scratchConvID)
	}
	if !updated.IsPromoted {
		t.Errorf("reply IsPromoted = false, want true")
	}
	if updated.Name == nil || *updated.Name != channelName {
		t.Errorf("reply Name = %v, want pointer to %q", updated.Name, channelName)
	}
	if updated.Cwd != home {
		t.Errorf("reply Cwd = %q, want seeded %q (inherited scratch cwd)", updated.Cwd, home)
	}

	// Registry state: the daemon eager-Saves before replying, so the on-disk row
	// is promoted with the requested name by the time the reply lands.
	raw, err := os.ReadFile(convPath)
	if err != nil {
		t.Fatalf("read conversations.json back: %v", err)
	}
	var onDisk struct {
		Conversations []struct {
			ID         string  `json:"id"`
			Name       *string `json:"name"`
			Cwd        string  `json:"cwd"`
			IsPromoted bool    `json:"is_promoted"`
		} `json:"conversations"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("decode on-disk registry: %v", err)
	}
	if len(onDisk.Conversations) != 1 {
		t.Fatalf("on-disk rows = %d, want 1 (raw=%s)", len(onDisk.Conversations), string(raw))
	}
	row := onDisk.Conversations[0]
	if row.ID != scratchConvID {
		t.Errorf("on-disk ID = %q, want %q", row.ID, scratchConvID)
	}
	if !row.IsPromoted {
		t.Errorf("on-disk IsPromoted = false, want true (promotion must persist)")
	}
	if row.Name == nil || *row.Name != channelName {
		t.Errorf("on-disk Name = %v, want pointer to %q", row.Name, channelName)
	}
	if row.Cwd != home {
		t.Errorf("on-disk Cwd = %q, want seeded %q (payload cwd must not be stored)", row.Cwd, home)
	}
}
