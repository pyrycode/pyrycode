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

// TestRelayV2_Archive proves archive_conversation / unarchive_conversation at the
// daemon boundary: a paired phone completes the Noise_IK handshake against a real
// spawned daemon and round-trips both verbs over the encrypted channel. The two
// verbs are inverse toggles of one durable is_archived flag through one shared
// handler (the archived bool is baked into each registration), so the happy path
// is a single archive → unarchive round-trip against one seeded row; the
// not-found path (error reply, registry untouched) is its own subtest. This
// closes the same gap shape #949 exposed for promote_conversation: a handler
// nothing exercised end-to-end.
//
// RED on main if either registration in cmd/pyry/relay.go — the ones for
// TypeArchiveConversation and TypeUnarchiveConversation — were removed: the verb
// would fall
// through to the no-handler protocol.unsupported arm and the round-trip's
// `want conversation_updated` assertion would fail. The test is a live guard on
// both lines.
//
// Each subtest owns its own spawn + seed (not a shared daemon) so the not-found
// "conversations.json unchanged" assertion reads a pristine, never-mutated row.
func TestRelayV2_Archive(t *testing.T) {
	t.Run("v2_enabled_archive_unarchive_round_trip", testV2DaemonArchiveRoundTrip)
	t.Run("v2_enabled_archive_not_found", testV2DaemonArchiveNotFound)
}

// testV2DaemonArchiveRoundTrip drives a spawned v2 daemon through a Noise_IK
// handshake, then over the SAME encrypted channel sends archive_conversation and
// unarchive_conversation for one seeded row — no re-handshake between them
// (nonces stay in lockstep because request/reply strictly alternate). It asserts
// each conversation_updated reply toggles IsArchived (true then false),
// correlates in_reply_to to its request id, and preserves the other fields, then
// reads conversations.json back off disk to prove the flip persisted and the
// toggle reverses (AC #1, #2). The seeded row is deliberately promoted+named so
// "the other fields survive" is a real check — a handler that dropped IsPromoted
// or hard-coded a field would be caught.
func testV2DaemonArchiveRoundTrip(t *testing.T) {
	const (
		convID                = "88888888-8888-4888-8888-888888888888"
		seedName              = "archive-me"
		seededTS              = "2026-01-01T00:00:00Z"
		reqIDArchive   uint64 = 61
		reqIDUnarchive uint64 = 62
	)
	seededTime, err := time.Parse(time.RFC3339, seededTS)
	if err != nil {
		t.Fatalf("parse seeded last_used_at: %v", err)
	}
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

	// Seed one active, promoted, named conversation the toggle will target.
	// is_promoted, name and cwd must survive both flips untouched.
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[{"id":"` + convID +
		`","name":"` + seedName +
		`","cwd":"` + home +
		`","is_promoted":true,"is_archived":false,"last_used_at":"` + seededTS + `"}]}`)
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

	// sendToggle seals one verb frame over the shared transport, reads the single
	// correlated reply, and asserts it is a conversation_updated whose IsArchived
	// equals wantArchived with the other seeded fields preserved. Both verbs share
	// the id-only ArchiveConversationPayload. The handler emits exactly one frame
	// per request (c.Reply, no separate broadcast), so we read exactly one.
	sendToggle := func(reqID uint64, verb string, wantArchived bool) {
		t.Helper()
		reqEnv, err := json.Marshal(protocol.Envelope{
			ID:      reqID,
			Type:    verb,
			TS:      time.Now().UTC(),
			Payload: mustJSON(t, protocol.ArchiveConversationPayload{ConversationID: convID}),
		})
		if err != nil {
			t.Fatalf("marshal %s request envelope: %v", verb, err)
		}
		ciphertext, err := initSend.Encrypt(reqEnv)
		if err != nil {
			t.Fatalf("seal %s request envelope: %v", verb, err)
		}
		sendNoiseMsg(t, phone, ciphertext)

		reply := decryptInnerEnvelope(t, readInnerFrame(t, phone, 3*time.Second), initRecv)
		if reply.Type != protocol.TypeConversationUpdated {
			t.Fatalf("%s reply Type = %q, want %q (payload=%s)",
				verb, reply.Type, protocol.TypeConversationUpdated, string(reply.Payload))
		}
		if reply.InReplyTo == nil || *reply.InReplyTo != reqID {
			t.Errorf("%s InReplyTo = %v, want pointer to %d", verb, reply.InReplyTo, reqID)
		}
		var updated protocol.ConversationUpdatedPayload
		if err := json.Unmarshal(reply.Payload, &updated); err != nil {
			t.Fatalf("decode %s conversation_updated payload: %v", verb, err)
		}
		if updated.ID != convID {
			t.Errorf("%s reply ID = %q, want %q", verb, updated.ID, convID)
		}
		if updated.IsArchived != wantArchived {
			t.Errorf("%s reply IsArchived = %v, want %v", verb, updated.IsArchived, wantArchived)
		}
		if !updated.IsPromoted {
			t.Errorf("%s reply IsPromoted = false, want true (toggle must preserve it)", verb)
		}
		if updated.Name == nil || *updated.Name != seedName {
			t.Errorf("%s reply Name = %v, want pointer to %q (toggle must preserve it)", verb, updated.Name, seedName)
		}
		if updated.Cwd != home {
			t.Errorf("%s reply Cwd = %q, want seeded %q (toggle must preserve it)", verb, updated.Cwd, home)
		}
		// last_used_at crosses the wire as a real time.Time; archive is a metadata
		// edit, not a "use", so it is not bumped. Compare with .Equal, never ==.
		if !updated.LastUsedAt.Equal(seededTime) {
			t.Errorf("%s reply LastUsedAt = %v, want %v (toggle must not bump last_used_at)", verb, updated.LastUsedAt, seededTime)
		}
	}

	// readArchivedOnDisk reads conversations.json back and returns the single
	// row's is_archived. The on-disk key IS omitempty (unlike the wire payload),
	// so after unarchive the key is absent — decode into a plain bool and assert
	// the value, never key presence.
	readArchivedOnDisk := func() bool {
		t.Helper()
		raw, err := os.ReadFile(convPath)
		if err != nil {
			t.Fatalf("read conversations.json back: %v", err)
		}
		var onDisk struct {
			Conversations []struct {
				ID         string `json:"id"`
				IsArchived bool   `json:"is_archived"`
			} `json:"conversations"`
		}
		if err := json.Unmarshal(raw, &onDisk); err != nil {
			t.Fatalf("decode on-disk registry: %v", err)
		}
		if len(onDisk.Conversations) != 1 {
			t.Fatalf("on-disk rows = %d, want 1 (raw=%s)", len(onDisk.Conversations), string(raw))
		}
		if onDisk.Conversations[0].ID != convID {
			t.Errorf("on-disk ID = %q, want %q", onDisk.Conversations[0].ID, convID)
		}
		return onDisk.Conversations[0].IsArchived
	}

	// Archive: reply is_archived == true; the daemon eager-Saves before replying,
	// so the on-disk row already carries the flip when the reply lands (AC #1).
	sendToggle(reqIDArchive, protocol.TypeArchiveConversation, true)
	if !readArchivedOnDisk() {
		t.Errorf("on-disk is_archived = false after archive, want true")
	}

	// Unarchive over the SAME channel: reply is_archived == false and the on-disk
	// flag reverses (AC #2).
	sendToggle(reqIDUnarchive, protocol.TypeUnarchiveConversation, false)
	if readArchivedOnDisk() {
		t.Errorf("on-disk is_archived = true after unarchive, want false")
	}
}

// testV2DaemonArchiveNotFound drives a spawned v2 daemon through a Noise_IK
// handshake and an archive_conversation for an id that is NOT in the registry,
// asserting the decrypted reply is an error envelope carrying
// conversation.not_found and that the seeded row is left untouched (AC #3). The
// handler replies before any Save, so the on-disk file must be pristine.
// archive_conversation is pinned here (AC #3 allows either verb; one
// deterministic choice).
func testV2DaemonArchiveNotFound(t *testing.T) {
	const (
		seededConvID        = "99999999-9999-4999-9999-999999999999"
		absentConvID        = "88888888-8888-4888-8888-888888888888"
		keepName            = "keep-me"
		seededTS            = "2026-01-01T00:00:00Z"
		reqID        uint64 = 63
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

	// Seed one active row that must survive untouched; the request targets a
	// different, absent id so SetArchived misses and the handler never Saves.
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[{"id":"` + seededConvID +
		`","name":"` + keepName +
		`","cwd":"` + home +
		`","is_promoted":false,"is_archived":false,"last_used_at":"` + seededTS + `"}]}`)
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

	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeArchiveConversation,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.ArchiveConversationPayload{ConversationID: absentConvID}),
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

	// The failed archive mutated nothing: the seeded row is intact on disk and
	// still active. This is the "no row mutated" evidence (AC #3).
	raw, err := os.ReadFile(convPath)
	if err != nil {
		t.Fatalf("read conversations.json back: %v", err)
	}
	var onDisk struct {
		Conversations []struct {
			ID         string  `json:"id"`
			Name       *string `json:"name"`
			IsArchived bool    `json:"is_archived"`
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
		t.Errorf("on-disk Name = %v, want pointer to %q (failed archive must not mutate)", row.Name, keepName)
	}
	if row.IsArchived {
		t.Errorf("on-disk is_archived = true, want false (failed archive must not mutate)")
	}
}
