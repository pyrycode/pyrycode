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
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_CreateWorkspaceFolder proves create_workspace_folder at the daemon
// boundary: a paired phone completes the Noise_IK handshake against a real spawned
// daemon and drives create_workspace_folder over the encrypted channel with the
// production resolveWorkspaceFolder resolver wired at cmd/pyry/`screenSnapshotterOrNil`. It
// closes the same gap shape #949 (promote), #974–#976 (rename/delete/archive), and
// #980 (change_workspace) closed for their verbs — a handler nothing exercised
// end-to-end — carrying #980's security twist: this verb WRITES TO THE HOST
// FILESYSTEM a directory named by two untrusted fields (parent + name) from a
// network-paired party, so what this test certifies over the actual wire is that
// BOTH independent fail-closed guards hold — confinement to $HOME AND the
// name-shape "directly under parent" guard (belt-and-suspenders, different fabric)
// — and that no attacker-supplied bytes leak on the reject replies.
//
// RED on main if the handler's TypeCreateWorkspaceFolder registration in
// cmd/pyry/relay.go were removed: the verb would fall through to the no-handler
// protocol.unsupported arm and the round-trip's `want workspace_folder_created`
// assertion would fail. The test is a live guard on that registration.
//
// Each subtest owns its own spawn (not a shared daemon) so every on-disk assertion
// reads a pristine, never-cross-contaminated temp $HOME. Unlike #980 there is no
// conversations registry to seed — this verb touches none — so every post-condition
// is a filesystem assertion under the temp $HOME.
func TestRelayV2_CreateWorkspaceFolder(t *testing.T) {
	t.Run("v2_enabled_create_workspace_folder_round_trip", testV2DaemonCreateWorkspaceFolderRoundTrip)
	t.Run("v2_enabled_create_workspace_folder_rejected_no_leak", testV2DaemonCreateWorkspaceFolderRejectedNoLeak)
	t.Run("v2_enabled_create_workspace_folder_bad_name", testV2DaemonCreateWorkspaceFolderBadName)
}

// testV2DaemonCreateWorkspaceFolderRoundTrip drives a spawned v2 daemon through a
// Noise_IK handshake and a create_workspace_folder → workspace_folder_created
// round-trip over the encrypted channel, then Stats the created directory off disk
// to prove it exists under the temp $HOME (AC #1, #2). The reply Path is the
// CONFINED realpath (EvalSymlinks of the parent joined with the name), not the
// literal filepath.Join(parent, name) sent — macOS temp dirs sit under a symlinked
// /var, so the two routinely differ; the path assertion compares against
// filepath.Join(EvalSymlinks(parent), name), never the raw join. This verb consumes
// no conversations registry, so nothing is seeded and the post-condition is a
// filesystem os.Stat, not a registry read-back.
func testV2DaemonCreateWorkspaceFolderRoundTrip(t *testing.T) {
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

	// An existing, in-$HOME parent and a clean single-element name. Compute the
	// confined realpath the handler will reply and create: EvalSymlinks the parent
	// (the leaf does not exist yet), then join the name. resolveWorkspaceFolder
	// creates the leaf under the symlink-resolved parent.
	const name = "new-app"
	parent := filepath.Join(home, "workspace")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	parentReal, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatalf("EvalSymlinks(parent): %v", err)
	}
	wantPath := filepath.Join(parentReal, name)

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

	// Seal and send create_workspace_folder. Send the absolute in-$HOME parent (the
	// test knows the daemon's home because it spawned it; the ~-expansion path is
	// covered by the helper test and not re-proven here).
	const reqID uint64 = 71
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeCreateWorkspaceFolder,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.CreateWorkspaceFolderPayload{
			Parent: parent,
			Name:   name,
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
	if reply.Type != protocol.TypeWorkspaceFolderCreated {
		t.Fatalf("reply Type = %q, want %q (payload=%s)",
			reply.Type, protocol.TypeWorkspaceFolderCreated, string(reply.Payload))
	}
	if reply.InReplyTo == nil || *reply.InReplyTo != reqID {
		t.Errorf("InReplyTo = %v, want pointer to %d", reply.InReplyTo, reqID)
	}
	var created protocol.WorkspaceFolderCreatedPayload
	if err := json.Unmarshal(reply.Payload, &created); err != nil {
		t.Fatalf("decode workspace_folder_created payload: %v", err)
	}
	// The replied Path is the confined realpath, NOT the raw filepath.Join(parent,
	// name): a resolver that returned the un-resolved path would pass a naive check
	// but fail this (the confine-and-realpath property).
	if created.Path != wantPath {
		t.Errorf("reply Path = %q, want the confined realpath %q (not raw %q)",
			created.Path, wantPath, filepath.Join(parent, name))
	}

	// AC #2 — the directory actually exists on disk under the temp HOME workspace
	// root. The daemon creates it before replying, so the post-reply Stat is
	// race-free.
	info, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("created folder %q missing on disk: %v", wantPath, err)
	}
	if !info.IsDir() {
		t.Errorf("created path %q is not a directory", wantPath)
	}
}

// testV2DaemonCreateWorkspaceFolderRejectedNoLeak drives a spawned v2 daemon
// through a Noise_IK handshake and a create_workspace_folder whose parent ESCAPES
// the daemon's $HOME, asserting the decrypted reply is a protocol.malformed error
// carrying the static confinement-rejection message, that NO attacker-supplied path
// bytes (raw or resolved) leak on the wire (AC #4 — the containment property this
// ticket exists to prove), and that NO directory is created outside $HOME (AC #3).
//
// The escaping parent is an existing, resolvable sibling temp dir OUTSIDE the
// daemon's p-*-* $HOME. Existing + NON-EMPTY passes the empty-parent guard, and the
// clean single-element name "new-app" passes the name-shape guard, so execution
// reaches the resolver and the reject is caused SPECIFICALLY by escaping $HOME — the
// exact property under test, non-vacuously. A vacuous variant (empty parent or bad
// name) would trip an earlier guard and prove nothing about the $HOME bound.
func testV2DaemonCreateWorkspaceFolderRejectedNoLeak(t *testing.T) {
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

	// An existing, resolvable dir OUTSIDE the daemon's $HOME (siblings under the
	// process temp root, neither ancestor nor descendant of home). The confiner
	// joins the clean name, resolves the parent, finds it escapes $HOME → reject.
	escaping := t.TempDir()
	escapingReal, err := filepath.EvalSymlinks(escaping)
	if err != nil {
		t.Fatalf("EvalSymlinks(escaping): %v", err)
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

	const reqID uint64 = 72
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeCreateWorkspaceFolder,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.CreateWorkspaceFolderPayload{
			Parent: escaping,
			Name:   "new-app",
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
	// The static msgCreateWorkspaceFolderRejected (unexported in package handlers,
	// so assert the literal). A regression that echoed the confine err would change
	// this message.
	if errPayload.Message != "workspace folder not allowed" {
		t.Errorf("error Message = %q, want the static confinement-rejection message", errPayload.Message)
	}
	if errPayload.Retryable {
		t.Errorf("error Retryable = true, want false (confinement reject is deterministic)")
	}

	// AC #4 — the load-bearing containment assertion: neither the raw escaping
	// parent NOR its resolved realpath appears anywhere in the decrypted reply
	// payload. The confine err names the RESOLVED path; the handler drops it and
	// replies a static string, so neither form may surface. A regression that echoed
	// the confine err would fail this.
	replyBytes := string(errReply.Payload)
	if strings.Contains(replyBytes, escaping) {
		t.Errorf("raw escaping parent leaked in reply payload: %s", replyBytes)
	}
	if strings.Contains(replyBytes, escapingReal) {
		t.Errorf("resolved escaping parent leaked in reply payload: %s", replyBytes)
	}

	// AC #3 — nothing is created outside $HOME. confineWorkdirToHomeCreating runs
	// containment check #1 on the symlink-resolved candidate BEFORE any MkdirAll, so
	// the would-be target under the escaping parent provably never exists —
	// deterministic, not racy.
	target := filepath.Join(escapingReal, "new-app")
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Errorf("escaping target %q was created (Stat err = %v), want never created", target, statErr)
	}
}

// testV2DaemonCreateWorkspaceFolderBadName drives a spawned v2 daemon through a
// Noise_IK handshake and a create_workspace_folder whose NAME trips the name-shape
// guard (contains '/'), certifying the SECOND independent fail-closed guard
// end-to-end (belt-and-suspenders, different fabric). It asserts the reply is a
// protocol.malformed error carrying the name-shape guard's OWN static message —
// DISTINCT from the confinement message the previous subtest asserts — that the
// attacker-supplied name marker does not echo on the wire, and that no directory is
// created under the valid in-$HOME parent (the guard runs before the resolver, so
// nothing is joined or created).
//
// Certifying BOTH guards over the wire is the whole security point of this verb: a
// name like "seg/INJECT_MARK" stays inside $HOME (confinement alone would not catch
// it) yet is not directly under the parent, which only the name-shape guard rejects.
func testV2DaemonCreateWorkspaceFolderBadName(t *testing.T) {
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

	// A valid, existing in-$HOME parent — so "no directory created" is inspectable.
	parent := filepath.Join(home, "workspace")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("mkdir parent: %v", err)
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

	// A name that trips the name-shape guard (contains '/') AND carries a distinctive
	// marker, so the no-leak assertion below is meaningful.
	const badName = "seg/INJECT_MARK"
	const reqID uint64 = 73
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeCreateWorkspaceFolder,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.CreateWorkspaceFolderPayload{
			Parent: parent,
			Name:   badName,
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
	// The static msgCreateWorkspaceFolderBadName — DISTINCT from the confinement
	// message asserted in the reject subtest. This is the proof the SECOND,
	// independent guard fired (not the confiner).
	if errPayload.Message != "workspace folder name must be a single path element" {
		t.Errorf("error Message = %q, want the static name-shape message", errPayload.Message)
	}
	if errPayload.Retryable {
		t.Errorf("error Retryable = true, want false (bad-name reject is deterministic)")
	}

	// No leak: the attacker-controlled name marker must not echo on the wire.
	replyBytes := string(errReply.Payload)
	if strings.Contains(replyBytes, "INJECT_MARK") {
		t.Errorf("attacker-supplied name leaked in reply payload: %s", replyBytes)
	}

	// No directory created: the guard runs BEFORE the resolver, so nothing under the
	// parent is joined or created — the "seg" segment of the bad name never lands.
	target := filepath.Join(parent, "seg")
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Errorf("bad-name target %q was created (Stat err = %v), want never created", target, statErr)
	}
}
