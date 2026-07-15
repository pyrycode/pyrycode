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

// TestRelayV2_RecentWorkspaces proves recent_workspaces at the daemon boundary:
// a paired phone completes the Noise_IK handshake against a real spawned daemon
// and reads the recency-ordered, de-duplicated workspace list back over the
// encrypted channel. It closes the same gap shape #949 (promote) and
// #974-#976 / #980 / #981 (rename/delete/archive/change/create) closed for their
// verbs — a handler nothing exercised end-to-end.
//
// Unlike its two sibling workspace verbs (#980 change_workspace, #981
// create_workspace_folder), recent_workspaces consumes no untrusted path and has
// no error path: the request payload is empty, the reply is derived purely from
// server-side registry state, an empty registry yields "workspaces":[] (not an
// error), and a conversation with an empty Cwd contributes no entry. There is
// therefore no not-found subtest and no on-disk reassertion — the verb is a pure
// read that Saves nothing.
//
// RED on main if the handler's TypeRecentWorkspaces registration in
// cmd/pyry/relay.go were removed: the verb would fall through to the no-handler
// protocol.unsupported arm and the `want recent_workspaces_list` assertion would
// fail. The test is a live guard on that registration.
//
// Each subtest owns its own spawn + seed (not a shared daemon), mirroring the
// rename test's per-subtest isolation.
func TestRelayV2_RecentWorkspaces(t *testing.T) {
	t.Run("v2_enabled_recent_workspaces_ordered_deduped", testV2DaemonRecentWorkspacesOrderedDeduped)
	t.Run("v2_enabled_recent_workspaces_empty_registry", testV2DaemonRecentWorkspacesEmptyRegistry)
}

// testV2DaemonRecentWorkspacesOrderedDeduped drives a spawned v2 daemon through a
// Noise_IK handshake and a recent_workspaces → recent_workspaces_list round-trip
// over the encrypted channel. It seeds four conversations across two distinct
// workspace folders — two sharing /ws/alpha (proving dedup carries the newer
// LastUsedAt) plus one row with an empty Cwd carrying the NEWEST timestamp
// (proving the empty-Cwd exclusion is non-vacuous: were it not skipped, that row
// would sort first and break both the len==2 and Workspaces[0].Path assertions).
// Covers AC #1 (round-trip), #2 (dedup + ordering), #3 (dedup carries newer), and
// the empty-Cwd facet of #4.
func testV2DaemonRecentWorkspacesOrderedDeduped(t *testing.T) {
	const (
		conv1 = "11111111-1111-4111-1111-111111111111" // /ws/alpha, T1 (dedup, older)
		conv2 = "22222222-2222-4222-2222-222222222222" // /ws/alpha, T3 (dedup, newer wins)
		conv3 = "33333333-3333-4333-3333-333333333333" // /ws/beta,  T2
		conv4 = "44444444-4444-4444-4444-444444444444" // empty Cwd, T4 (newest → excluded)

		tsT1 = "2026-01-01T00:00:01Z"
		tsT2 = "2026-01-01T00:00:02Z"
		tsT3 = "2026-01-01T00:00:03Z"
		tsT4 = "2026-01-01T00:00:04Z"

		wsAlpha = "/ws/alpha"
		wsBeta  = "/ws/beta"
	)
	// LastUsedAt crosses the wire as a real time.Time; compare with .Equal, never
	// == (monotonic-clock reading strips on JSON marshal). alpha carries T3 (the
	// newer of conv-1/conv-2), beta carries T2.
	wantAlphaTS, err := time.Parse(time.RFC3339, tsT3)
	if err != nil {
		t.Fatalf("parse alpha last_used_at: %v", err)
	}
	wantBetaTS, err := time.Parse(time.RFC3339, tsT2)
	if err != nil {
		t.Fatalf("parse beta last_used_at: %v", err)
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

	// Seed four conversations before startup. Cwd values are opaque strings the
	// handler never resolves or confines, so literal /ws/* paths are fine — no
	// MkdirAll, no EvalSymlinks. conv-1/conv-2 share /ws/alpha (dedup); conv-4 has
	// an empty Cwd and the newest timestamp (non-vacuous exclusion).
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[` +
		`{"id":"` + conv1 + `","cwd":"` + wsAlpha + `","last_used_at":"` + tsT1 + `"},` +
		`{"id":"` + conv2 + `","cwd":"` + wsAlpha + `","last_used_at":"` + tsT3 + `"},` +
		`{"id":"` + conv3 + `","cwd":"` + wsBeta + `","last_used_at":"` + tsT2 + `"},` +
		`{"id":"` + conv4 + `","cwd":"","last_used_at":"` + tsT4 + `"}` +
		`]}`)
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

	// Seal and send recent_workspaces. The request payload is empty by spec
	// (RecentWorkspacesPayload{} marshals to {}); the reply is derived purely
	// from the seeded registry state.
	const reqID uint64 = 71
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeRecentWorkspaces,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.RecentWorkspacesPayload{}),
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
	if reply.Type != protocol.TypeRecentWorkspacesList {
		t.Fatalf("reply Type = %q, want %q (payload=%s)",
			reply.Type, protocol.TypeRecentWorkspacesList, string(reply.Payload))
	}
	if reply.InReplyTo == nil || *reply.InReplyTo != reqID {
		t.Errorf("InReplyTo = %v, want pointer to %d", reply.InReplyTo, reqID)
	}

	var list protocol.RecentWorkspacesListPayload
	if err := json.Unmarshal(reply.Payload, &list); err != nil {
		t.Fatalf("decode recent_workspaces_list payload: %v", err)
	}

	// One entry per distinct non-empty Cwd: /ws/alpha deduped to one row, empty-Cwd
	// conv-4 excluded (AC #2 dedup-to-distinct, AC #4 empty-Cwd facet).
	if len(list.Workspaces) != 2 {
		t.Fatalf("len(Workspaces) = %d, want 2 (payload=%s)", len(list.Workspaces), string(reply.Payload))
	}

	// Ordering (AC #2): most-recent-first. alpha carries T3, beta carries T2; T3 > T2.
	if list.Workspaces[0].Path != wsAlpha {
		t.Errorf("Workspaces[0].Path = %q, want %q (most-recent-first)", list.Workspaces[0].Path, wsAlpha)
	}
	if list.Workspaces[1].Path != wsBeta {
		t.Errorf("Workspaces[1].Path = %q, want %q", list.Workspaces[1].Path, wsBeta)
	}

	// Dedup carries the more-recent LastUsedAt (AC #3): /ws/alpha reflects conv-2's
	// T3, not conv-1's older T1. A handler that kept the first-seen or older
	// timestamp fails here.
	if !list.Workspaces[0].LastUsedAt.Equal(wantAlphaTS) {
		t.Errorf("Workspaces[0].LastUsedAt = %v, want %v (newer of conv-1/conv-2)", list.Workspaces[0].LastUsedAt, wantAlphaTS)
	}
	if !list.Workspaces[1].LastUsedAt.Equal(wantBetaTS) {
		t.Errorf("Workspaces[1].LastUsedAt = %v, want %v", list.Workspaces[1].LastUsedAt, wantBetaTS)
	}

	// Belt-and-suspenders on the empty-Cwd exclusion: no entry carries an empty
	// Path (the len==2 check already implies it, but this pins the intent).
	for i, ws := range list.Workspaces {
		if ws.Path == "" {
			t.Errorf("Workspaces[%d].Path is empty; empty-Cwd conversations must contribute no entry", i)
		}
	}
}

// testV2DaemonRecentWorkspacesEmptyRegistry drives a spawned v2 daemon through a
// Noise_IK handshake and a recent_workspaces request against an explicitly empty
// registry. The reply is a SUCCESS recent_workspaces_list envelope carrying an
// empty-but-non-nil slice — proving the empty case is not an error path and that
// the payload marshals "workspaces":[] rather than null (empty-registry facet of
// AC #4).
func testV2DaemonRecentWorkspacesEmptyRegistry(t *testing.T) {
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

	// Seed an explicitly empty registry (deterministic): no conversations, so no
	// workspace folders. Load treats this as a valid, empty registry.
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	if err := os.WriteFile(convPath, []byte(`{"conversations":[]}`), 0o600); err != nil {
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

	const reqID uint64 = 72
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeRecentWorkspaces,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.RecentWorkspacesPayload{}),
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
	// A SUCCESS reply, not TypeError — proves empty → [] is not an error path.
	if reply.Type != protocol.TypeRecentWorkspacesList {
		t.Fatalf("reply Type = %q, want %q (empty registry must yield a success reply, not an error) (payload=%s)",
			reply.Type, protocol.TypeRecentWorkspacesList, string(reply.Payload))
	}
	if reply.InReplyTo == nil || *reply.InReplyTo != reqID {
		t.Errorf("InReplyTo = %v, want pointer to %d", reply.InReplyTo, reqID)
	}

	var list protocol.RecentWorkspacesListPayload
	if err := json.Unmarshal(reply.Payload, &list); err != nil {
		t.Fatalf("decode recent_workspaces_list payload: %v", err)
	}
	// Non-nil AND empty: the payload marshals "workspaces":[], not null. A handler
	// that returned a nil slice would marshal null and fail the non-nil check even
	// though len would still be 0 (pins the always-non-nil contract, workspace.go).
	if list.Workspaces == nil {
		t.Errorf("Workspaces = nil, want non-nil empty slice (must marshal \"workspaces\":[] not null; payload=%s)", string(reply.Payload))
	}
	if len(list.Workspaces) != 0 {
		t.Errorf("len(Workspaces) = %d, want 0 (empty registry; payload=%s)", len(list.Workspaces), string(reply.Payload))
	}
}
