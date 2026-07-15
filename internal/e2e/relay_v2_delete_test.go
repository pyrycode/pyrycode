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

// TestRelayV2_Delete proves delete_conversation at the daemon boundary: a paired
// phone completes the Noise_IK handshake against a real spawned daemon and
// round-trips delete_conversation over the encrypted channel — the happy path
// (hard delete removes the row, replies conversation_deleted) and the not-found
// path (error reply, registry untouched). This closes the same gap shape #949
// exposed for promote_conversation: a handler nothing exercised end-to-end.
//
// Unlike rename/promote, delete's success reply is conversation_deleted carrying
// only the deleted id (the record is gone — no name/cwd/last_used_at projection),
// not conversation_updated.
//
// RED on main if the handler's TypeDeleteConversation registration in
// cmd/pyry/relay.go were removed: the verb would fall through to the no-handler
// protocol.unsupported arm and the happy-path `want conversation_deleted`
// assertion would fail. The test is a live guard on that registration.
//
// Each subtest owns its own spawn + seed (not a shared daemon) so the not-found
// "conversations.json unchanged" assertion reads a pristine, never-mutated row.
func TestRelayV2_Delete(t *testing.T) {
	t.Run("v2_enabled_delete_conversation_round_trip", testV2DaemonDeleteRoundTrip)
	t.Run("v2_enabled_delete_conversation_not_found", testV2DaemonDeleteNotFound)
}

// testV2DaemonDeleteRoundTrip drives a spawned v2 daemon through a Noise_IK
// handshake and a delete_conversation → conversation_deleted round-trip over the
// encrypted channel, then reads the registry back off disk to prove the hard
// delete removed only the targeted row (AC #1, #2). Two rows are seeded — the
// delete target and a non-targeted survivor — so "row gone" is a real check: a
// soft-delete-that-flags bug would leave 2 rows, a truncate/wipe bug would leave
// 0, and only correct selective hard delete leaves exactly the survivor.
func testV2DaemonDeleteRoundTrip(t *testing.T) {
	const (
		targetConvID   = "11111111-1111-4111-1111-111111111111"
		targetName     = "delete-me"
		survivorConvID = "22222222-2222-4222-2222-222222222222"
		survivorName   = "keep-me"
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

	// Seed two rows: the delete target and a bystander survivor. Asserting the
	// survivor stays after the delete proves the hard delete is selective and the
	// on-disk file is not truncated to an empty array.
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[{"id":"` + targetConvID +
		`","name":"` + targetName +
		`","cwd":"` + home +
		`","is_promoted":true,"last_used_at":"2026-01-01T00:00:00Z"},{"id":"` + survivorConvID +
		`","name":"` + survivorName +
		`","cwd":"` + home +
		`","is_promoted":false,"last_used_at":"2026-01-02T00:00:00Z"}]}`)
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

	// Seal and send delete_conversation. DeleteConversationPayload carries only
	// the target id — no name, no cwd (a hard delete names a row and removes it).
	const reqID uint64 = 61
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeDeleteConversation,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.DeleteConversationPayload{
			ConversationID: targetConvID,
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
	if reply.Type != protocol.TypeConversationDeleted {
		t.Fatalf("reply Type = %q, want %q (payload=%s)",
			reply.Type, protocol.TypeConversationDeleted, string(reply.Payload))
	}
	if reply.InReplyTo == nil || *reply.InReplyTo != reqID {
		t.Errorf("InReplyTo = %v, want pointer to %d", reply.InReplyTo, reqID)
	}
	// The reply carries only the deleted id — no name/cwd/last_used_at, so no
	// time.Time to compare (contrast rename's conversation_updated projection).
	var deleted protocol.ConversationDeletedPayload
	if err := json.Unmarshal(reply.Payload, &deleted); err != nil {
		t.Fatalf("decode conversation_deleted payload: %v", err)
	}
	if deleted.ID != targetConvID {
		t.Errorf("reply ID = %q, want %q", deleted.ID, targetConvID)
	}

	// Registry state: the daemon eager-Saves before replying, so the on-disk file
	// no longer holds the target row by the time the reply lands. Exactly the
	// survivor must remain — not zero rows (a truncate bug) and not two (a
	// soft-delete-that-flags bug).
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
		t.Fatalf("on-disk rows = %d, want 1 (hard delete must remove exactly the target; raw=%s)",
			len(onDisk.Conversations), string(raw))
	}
	row := onDisk.Conversations[0]
	if row.ID != survivorConvID {
		t.Errorf("on-disk ID = %q, want survivor %q (target must be gone, survivor must remain)", row.ID, survivorConvID)
	}
	if row.Name == nil || *row.Name != survivorName {
		t.Errorf("on-disk Name = %v, want pointer to %q (survivor must be untouched)", row.Name, survivorName)
	}
}

// testV2DaemonDeleteNotFound drives a spawned v2 daemon through a Noise_IK
// handshake and a delete_conversation for an id that is NOT in the registry,
// asserting the decrypted reply is an error envelope carrying
// conversation.not_found and that the seeded row is left untouched (AC #3). The
// handler replies before any Save, so the on-disk file must be pristine.
func testV2DaemonDeleteNotFound(t *testing.T) {
	const (
		seededConvID = "33333333-3333-4333-3333-333333333333"
		absentConvID = "44444444-4444-4444-4444-444444444444"
		keepName     = "keep-me"
		seededTS     = "2026-01-01T00:00:00Z"
	)
	home := shortHome(t)

	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	payload := decodePairPayload(t, r.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Seed one row that must survive untouched; the request targets a different,
	// absent id so Delete misses and the handler never Saves.
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[{"id":"` + seededConvID +
		`","name":"` + keepName +
		`","cwd":"` + home +
		`","is_promoted":false,"last_used_at":"` + seededTS + `"}]}`)
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

	const reqID uint64 = 62
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeDeleteConversation,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.DeleteConversationPayload{
			ConversationID: absentConvID,
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

	// The failed delete removed nothing: the seeded row is intact on disk. This
	// is the "no row removed" evidence (AC #3).
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
		t.Errorf("on-disk Name = %v, want pointer to %q (failed delete must not mutate)", row.Name, keepName)
	}
}
