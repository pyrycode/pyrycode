//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_ChangeWorkspace proves change_workspace at the daemon boundary: a
// paired phone completes the Noise_IK handshake against a real spawned daemon
// and drives change_workspace over the encrypted channel with the production
// resolveWorkspaceDir resolver wired at cmd/pyry/relay.go:412. It closes the same
// gap shape #949 (promote) and #974–#976 (rename/delete/archive) closed for their
// verbs — a handler nothing exercised end-to-end — but with the security twist
// unique to this verb: change_workspace stores an untrusted FILESYSTEM PATH from a
// network-paired party, so the confinement-to-$HOME containment property is what
// this test certifies over the actual wire, not just at the confiner helper.
//
// RED on main if the handler's TypeChangeWorkspace registration in
// cmd/pyry/relay.go were removed: the verb would fall through to the no-handler
// protocol.unsupported arm and the happy-path `want conversation_updated`
// assertion would fail. The test is a live guard on that registration.
//
// Each subtest owns its own spawn + seed (not a shared daemon) so every on-disk
// assertion reads a pristine, never-cross-contaminated registry.
func TestRelayV2_ChangeWorkspace(t *testing.T) {
	t.Run("v2_enabled_change_workspace_round_trip", testV2DaemonChangeWorkspaceRoundTrip)
	t.Run("v2_enabled_change_workspace_rejected_no_leak", testV2DaemonChangeWorkspaceRejectedNoLeak)
	t.Run("v2_enabled_change_workspace_not_found", testV2DaemonChangeWorkspaceNotFound)
}

// testV2DaemonChangeWorkspaceRoundTrip drives a spawned v2 daemon through a
// Noise_IK handshake and a change_workspace → conversation_updated round-trip over
// the encrypted channel, then reads the registry back off disk to prove the new
// workspace persisted (AC #1, #2). The reply Cwd is the CONFINED realpath
// (EvalSymlinks of the target), not the literal path sent — macOS temp dirs sit
// under a symlinked /var, so the two routinely differ; every Cwd assertion
// compares against filepath.EvalSymlinks(target), never the raw request. The
// seeded row is deliberately promoted+named+active so "preserved" is a real check:
// a handler that dropped IsPromoted/IsArchived/Name would be caught. LastUsedAt is
// not bumped (metadata edit) — compared with .Equal, never ==.
func testV2DaemonChangeWorkspaceRoundTrip(t *testing.T) {
	const (
		convID   = "77777777-7777-4777-7777-777777777777"
		convName = "some-name"
		seededTS = "2026-01-01T00:00:00Z"
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

	// Seed one promoted, named conversation whose cwd is the OLD workspace (home).
	// is_promoted / is_archived / name must survive the workspace change untouched.
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[{"id":"` + convID +
		`","name":"` + convName +
		`","cwd":"` + home +
		`","is_promoted":true,"last_used_at":"` + seededTS + `"}]}`)
	if err := os.WriteFile(convPath, convJSON, 0o600); err != nil {
		t.Fatalf("seed conversations.json: %v", err)
	}

	// Create the NEW workspace target under $HOME and compute its confined
	// realpath — the value the handler stores and replies, distinct from newWS on
	// macOS where /var → /private/var. resolveWorkspaceDir is strict/non-creating,
	// so the dir must exist before the send.
	newWS := filepath.Join(home, "projects", "app")
	if err := os.MkdirAll(newWS, 0o700); err != nil {
		t.Fatalf("mkdir new workspace: %v", err)
	}
	wantCwd, err := filepath.EvalSymlinks(newWS)
	if err != nil {
		t.Fatalf("EvalSymlinks(newWS): %v", err)
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

	// Seal and send change_workspace. The payload carries the RAW requested path;
	// the daemon confines it and stores the realpath.
	const reqID uint64 = 61
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeChangeWorkspace,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.ChangeWorkspacePayload{
			ConversationID: convID,
			Cwd:            newWS,
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
	// The stored/replied Cwd is the confined realpath, NOT the literal newWS: a
	// handler that stored the raw path would fail this while passing a naive
	// Cwd == newWS check (the validate==store property).
	if updated.Cwd != wantCwd {
		t.Errorf("reply Cwd = %q, want the confined realpath %q (not raw %q)", updated.Cwd, wantCwd, newWS)
	}
	if !updated.IsPromoted {
		t.Errorf("reply IsPromoted = false, want true (change_workspace must preserve it)")
	}
	if updated.IsArchived {
		t.Errorf("reply IsArchived = true, want false (change_workspace must preserve it)")
	}
	if updated.Name == nil || *updated.Name != convName {
		t.Errorf("reply Name = %v, want pointer to %q (must preserve it)", updated.Name, convName)
	}
	// last_used_at crosses the wire as a real time.Time; a workspace change is a
	// metadata edit, not a "use", so it is not bumped. Compare with .Equal, never ==.
	if !updated.LastUsedAt.Equal(seededTime) {
		t.Errorf("reply LastUsedAt = %v, want %v (must not bump last_used_at)", updated.LastUsedAt, seededTime)
	}

	// Registry state: the daemon eager-Saves before replying, so the on-disk row
	// carries the confined realpath by the time the reply lands.
	raw, err := os.ReadFile(convPath)
	if err != nil {
		t.Fatalf("read conversations.json back: %v", err)
	}
	var onDisk struct {
		Conversations []struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
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
	if row.Cwd != wantCwd {
		t.Errorf("on-disk Cwd = %q, want the confined realpath %q (change must persist)", row.Cwd, wantCwd)
	}
}

// testV2DaemonChangeWorkspaceRejectedNoLeak drives a spawned v2 daemon through a
// Noise_IK handshake and a change_workspace whose target ESCAPES the daemon's
// $HOME, asserting the decrypted reply is a protocol.malformed error carrying the
// static rejection message, that NO attacker-supplied path bytes (raw or resolved)
// leak on the wire (AC #4 — the containment property this ticket exists to prove),
// and that the recorded workspace is left unchanged on disk (AC #3).
//
// The escaping target is an existing, resolvable sibling temp dir OUTSIDE the
// daemon's p-301-* $HOME. Existing + non-empty means it passes the empty-path guard
// and the "resolvable" check, so the reject is caused specifically by escaping
// $HOME — the exact property under test, non-vacuously. The seeded old cwd (home)
// deliberately differs from the escaping path, so a handler that wrongly stored the
// escaping path (validate≠store) flips the on-disk assertion.
func testV2DaemonChangeWorkspaceRejectedNoLeak(t *testing.T) {
	const (
		convID   = "88888888-8888-4888-8888-888888888888"
		seededTS = "2026-01-01T00:00:00Z"
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

	// Seed one row whose cwd is a valid old workspace (home) — the "unchanged"
	// baseline. It differs from the escaping path below, so "state unchanged" is a
	// real check.
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[{"id":"` + convID +
		`","cwd":"` + home +
		`","is_promoted":true,"last_used_at":"` + seededTS + `"}]}`)
	if err := os.WriteFile(convPath, convJSON, 0o600); err != nil {
		t.Fatalf("seed conversations.json: %v", err)
	}

	// An existing, resolvable dir OUTSIDE the daemon's $HOME (siblings under the
	// process temp root, neither ancestor nor descendant of home). The confiner
	// resolves it and finds it escapes $HOME → reject.
	escaping := t.TempDir()
	escapingReal, err := filepath.EvalSymlinks(escaping)
	if err != nil {
		t.Fatalf("EvalSymlinks(escaping): %v", err)
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
		Type: protocol.TypeChangeWorkspace,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.ChangeWorkspacePayload{
			ConversationID: convID,
			Cwd:            escaping,
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
	if errPayload.Code != protocol.CodeProtocolMalformed {
		t.Errorf("error Code = %q, want %q", errPayload.Code, protocol.CodeProtocolMalformed)
	}
	// The static msgChangeWorkspaceRejected (unexported in package handlers, so
	// assert the literal). A regression that echoed the confined path would change
	// this message.
	if errPayload.Message != "workspace directory not allowed" {
		t.Errorf("error Message = %q, want the static rejection message", errPayload.Message)
	}

	// AC #4 — the load-bearing containment assertion: neither the raw escaping path
	// NOR its resolved realpath appears anywhere in the decrypted reply payload. The
	// confine err names the RESOLVED path; the handler drops it and replies a static
	// string, so neither form may surface. A regression that echoed the confine err
	// would fail this.
	replyBytes := string(errReply.Payload)
	if strings.Contains(replyBytes, escaping) {
		t.Errorf("raw escaping path leaked in reply payload: %s", replyBytes)
	}
	if strings.Contains(replyBytes, escapingReal) {
		t.Errorf("resolved escaping path leaked in reply payload: %s", replyBytes)
	}

	// State unchanged: the on-disk cwd is STILL the seeded home, not the escaping
	// path. Non-vacuous because seeded cwd != escaping.
	raw, err := os.ReadFile(convPath)
	if err != nil {
		t.Fatalf("read conversations.json back: %v", err)
	}
	var onDisk struct {
		Conversations []struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
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
	if row.Cwd != home {
		t.Errorf("on-disk Cwd = %q, want seeded %q (rejected change must not mutate)", row.Cwd, home)
	}
}

// testV2DaemonChangeWorkspaceNotFound drives a spawned v2 daemon through a Noise_IK
// handshake and a change_workspace for an id that is NOT in the registry, with a
// VALID in-$HOME target so execution reaches the not_found branch: confinement runs
// BEFORE the registry lookup, so a rejected target would mask not_found with a
// protocol.malformed reply. Asserts the decrypted reply is conversation.not_found
// and that the seeded row is left untouched (AC #5). The handler replies before any
// Save, so the on-disk file must be pristine.
func testV2DaemonChangeWorkspaceNotFound(t *testing.T) {
	const (
		seededConvID = "66666666-6666-4666-6666-666666666666"
		absentConvID = "55555555-5555-4555-5555-555555555555"
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
	// absent id so Update misses and the handler never Saves.
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[{"id":"` + seededConvID +
		`","cwd":"` + home +
		`","is_promoted":false,"last_used_at":"` + seededTS + `"}]}`)
	if err := os.WriteFile(convPath, convJSON, 0o600); err != nil {
		t.Fatalf("seed conversations.json: %v", err)
	}

	// A VALID in-$HOME target: confinement runs before the registry lookup, so the
	// target must pass confinement for execution to reach the !hit not_found branch.
	validTarget := filepath.Join(home, "ws")
	if err := os.MkdirAll(validTarget, 0o700); err != nil {
		t.Fatalf("mkdir valid target: %v", err)
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

	const reqID uint64 = 63
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeChangeWorkspace,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.ChangeWorkspacePayload{
			ConversationID: absentConvID,
			Cwd:            validTarget,
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
	if errPayload.Message != "conversation not found" {
		t.Errorf("error Message = %q, want the static not-found message", errPayload.Message)
	}

	// The failed change mutated nothing: the seeded row is intact on disk (id + cwd
	// unchanged) — the handler replies before any Save.
	raw, err := os.ReadFile(convPath)
	if err != nil {
		t.Fatalf("read conversations.json back: %v", err)
	}
	var onDisk struct {
		Conversations []struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
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
	if row.Cwd != home {
		t.Errorf("on-disk Cwd = %q, want seeded %q (failed change must not mutate)", row.Cwd, home)
	}
}
