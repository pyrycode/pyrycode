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
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_Rename proves rename_conversation at the daemon boundary: a paired
// phone completes the Noise_IK handshake against a real spawned daemon and
// round-trips rename_conversation over the encrypted channel — the happy path
// (rename persists, IsPromoted/Cwd preserved) and the not-found path (error
// reply, registry untouched). This closes the same gap shape #949 exposed for
// promote_conversation: a handler nothing exercised end-to-end.
//
// RED on main if the handler's TypeRenameConversation registration in
// cmd/pyry/relay.go were removed: the verb would fall through to the no-handler
// protocol.unsupported arm and the happy-path `want conversation_updated`
// assertion would fail. The test is a live guard on that registration.
//
// Each subtest owns its own spawn + seed (not a shared daemon) so the not-found
// "conversations.json unchanged" assertion reads a pristine, never-mutated row.
func TestRelayV2_Rename(t *testing.T) {
	t.Run("v2_enabled_rename_conversation_round_trip", testV2DaemonRenameRoundTrip)
	t.Run("v2_enabled_rename_conversation_not_found", testV2DaemonRenameNotFound)
}

// testV2DaemonRenameRoundTrip drives a spawned v2 daemon through a Noise_IK
// handshake and a rename_conversation → conversation_updated round-trip over the
// encrypted channel, then reads the registry back off disk to prove the rename
// persisted with IsPromoted and Cwd unchanged (AC #1, #2). The seeded row is
// deliberately promoted+named so "unchanged" is a real check — a handler that
// dropped IsPromoted or hard-coded false would be caught.
func testV2DaemonRenameRoundTrip(t *testing.T) {
	const (
		convID   = "77777777-7777-4777-7777-777777777777"
		oldName  = "old-name"
		newName  = "new-name"
		seededTS = "2026-01-01T00:00:00Z"
	)
	seededTime, err := time.Parse(time.RFC3339, seededTS)
	if err != nil {
		t.Fatalf("parse seeded last_used_at: %v", err)
	}
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	// Pair a device: yields the bearer token and the responder static pubkey the
	// phone pins. The daemon loads the same static key on startup.
	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Seed one promoted, named conversation the rename will target. is_promoted
	// and cwd must survive the rename untouched.
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[{"id":"` + convID +
		`","name":"` + oldName +
		`","cwd":"` + home +
		`","is_promoted":true,"last_used_at":"` + seededTS + `"}]}`)
	if err := os.WriteFile(convPath, convJSON, 0o600); err != nil {
		t.Fatalf("seed conversations.json: %v", err)
	}

	h := StartInWithEnv(t, home,
		[]string{"PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_MOBILE_V2=1"},
		"-pyry-relay="+relayURL,
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

	// Seal and send rename_conversation. RenameConversationPayload carries no cwd
	// (unlike promote) — a rename neither has nor means a workspace path.
	const reqID uint64 = 51
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeRenameConversation,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.RenameConversationPayload{
			ConversationID: convID,
			Name:           newName,
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
	if updated.ID != convID {
		t.Errorf("reply ID = %q, want %q", updated.ID, convID)
	}
	if updated.Name == nil || *updated.Name != newName {
		t.Errorf("reply Name = %v, want pointer to %q", updated.Name, newName)
	}
	if !updated.IsPromoted {
		t.Errorf("reply IsPromoted = false, want true (rename must preserve it)")
	}
	if updated.IsArchived {
		t.Errorf("reply IsArchived = true, want false (rename must preserve it)")
	}
	if updated.Cwd != home {
		t.Errorf("reply Cwd = %q, want seeded %q (rename must preserve it)", updated.Cwd, home)
	}
	// last_used_at crosses the wire as a real time.Time; a rename is a metadata
	// edit, not a "use", so it is not bumped. Compare with .Equal, never ==.
	if !updated.LastUsedAt.Equal(seededTime) {
		t.Errorf("reply LastUsedAt = %v, want %v (rename must not bump last_used_at)", updated.LastUsedAt, seededTime)
	}

	// Registry state: the daemon eager-Saves before replying, so the on-disk row
	// carries the new name by the time the reply lands.
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
	if row.ID != convID {
		t.Errorf("on-disk ID = %q, want %q", row.ID, convID)
	}
	if row.Name == nil || *row.Name != newName {
		t.Errorf("on-disk Name = %v, want pointer to %q (rename must persist)", row.Name, newName)
	}
	if !row.IsPromoted {
		t.Errorf("on-disk IsPromoted = false, want true (must be unchanged)")
	}
	if row.Cwd != home {
		t.Errorf("on-disk Cwd = %q, want seeded %q (must be unchanged)", row.Cwd, home)
	}
}

// testV2DaemonRenameNotFound drives a spawned v2 daemon through a Noise_IK
// handshake and a rename_conversation for an id that is NOT in the registry,
// asserting the decrypted reply is an error envelope carrying
// conversation.not_found and that the seeded row is left untouched (AC #3). The
// handler replies before any Save, so the on-disk file must be pristine.
func testV2DaemonRenameNotFound(t *testing.T) {
	const (
		seededConvID = "66666666-6666-4666-6666-666666666666"
		absentConvID = "55555555-5555-4555-5555-555555555555"
		keepName     = "keep-me"
		seededTS     = "2026-01-01T00:00:00Z"
	)
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Seed one row that must survive untouched; the request targets a different,
	// absent id so Update misses and the handler never Saves.
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[{"id":"` + seededConvID +
		`","name":"` + keepName +
		`","cwd":"` + home +
		`","is_promoted":false,"last_used_at":"` + seededTS + `"}]}`)
	if err := os.WriteFile(convPath, convJSON, 0o600); err != nil {
		t.Fatalf("seed conversations.json: %v", err)
	}

	h := StartInWithEnv(t, home,
		[]string{"PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_MOBILE_V2=1"},
		"-pyry-relay="+relayURL,
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

	const reqID uint64 = 52
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeRenameConversation,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.RenameConversationPayload{
			ConversationID: absentConvID,
			Name:           "whatever",
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

	errReply := decryptInnerEnvelope(t, readInnerFrame(t, phone, 3*time.Second), initRecv)
	if errReply.Type != protocol.TypeError {
		t.Fatalf("reply Type = %q, want %q (payload=%s)",
			errReply.Type, protocol.TypeError, string(errReply.Payload))
	}
	if errReply.InReplyTo == nil || *errReply.InReplyTo != reqID {
		t.Errorf("InReplyTo = %v, want pointer to %d", errReply.InReplyTo, reqID)
	}
	var errPayload protocol.ErrorPayload
	if err := json.Unmarshal(errReply.Payload, &errPayload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if errPayload.Code != protocol.CodeConversationNotFound {
		t.Errorf("error Code = %q, want %q", errPayload.Code, protocol.CodeConversationNotFound)
	}

	// The failed rename mutated nothing: the seeded row is intact on disk. This
	// is the "no row mutated" evidence (AC #3).
	raw, err := os.ReadFile(convPath)
	if err != nil {
		t.Fatalf("read conversations.json back: %v", err)
	}
	var onDisk struct {
		Conversations []struct {
			ID   string  `json:"id"`
			Name *string `json:"name"`
		} `json:"conversations"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("decode on-disk registry: %v", err)
	}
	if len(onDisk.Conversations) != 1 {
		t.Fatalf("on-disk rows = %d, want 1 (raw=%s)", len(onDisk.Conversations), string(raw))
	}
	row := onDisk.Conversations[0]
	if row.ID != seededConvID {
		t.Errorf("on-disk ID = %q, want %q (seeded row must survive)", row.ID, seededConvID)
	}
	if row.Name == nil || *row.Name != keepName {
		t.Errorf("on-disk Name = %v, want pointer to %q (failed rename must not mutate)", row.Name, keepName)
	}
}
