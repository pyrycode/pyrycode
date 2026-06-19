//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Phase 2.0 of EPIC #672: first-message lazy bind makes idle eviction
// load-bearing — daemon RAM scales with *active* discussions, not the total
// ever created. #677 mints+binds a dedicated claude session at
// create_conversation time; #678 routes send_message through the bound
// session's Pool.Activate (the cap-enforcing spawn entry). #680 adds no
// production code: it proves that a *phone-created* discussion's bound session
// is a full citizen of the idle-evict / active-cap machinery the bootstrap
// already participates in.
//
// The two tests below pin the four acceptance criteria at the binary boundary
// using the v1 fakephone/fakerelay harness and fakeclaude in TUI mode:
//
//	Test A (idle):  AC#1 per-discussion idle eviction, AC#2 reactivate-on-send,
//	                AC#4 no cross-bleed when one discussion churns.
//	Test B (cap):   AC#3 cap evicts the LRU active peer (incl. a cross-discussion
//	                victim), AC#4 only the deliberate victim transitions.
//
// What the harness can observe is lifecycle/routing scoping (which session's
// registry entry transitions, which reactivates) — NOT per-file JSONL content,
// because fakeclaude derives its JSONL stem from PYRY_FAKE_CLAUDE_INITIAL_UUID
// (env), not the --session-id argv, so every supervised child shares one file.
// The content-recall half of AC#2/#4 ("prior conversation intact", "turns land
// in its own JSONL" as content) is realclaude's domain and is already covered
// by the existing #677/#678 realclaude round-trip e2e; #680 does not add a
// realclaude test (the two-phone realclaude harness does not exist — #603).

// TestE2E_PerConversation_IdleEvictsAndReactivates exercises a per-discussion
// session through its full active→evicted→active arc:
//
//	AC#1 — two phone-created discussions bind two distinct dedicated sessions;
//	       each idle-evicts (lifecycle_state=="evicted", claude exited) once its
//	       idle window elapses with no attach.
//	AC#2 — a send_message to one evicted discussion reactivates it (respawn
//	       claude --session-id <its own uuid>) and delivers the turn, acked on
//	       the wire.
//	AC#4 — reactivating one discussion's session leaves the other discussion's
//	       evicted session untouched: churn in one does not disturb another.
func TestE2E_PerConversation_IdleEvictsAndReactivates(t *testing.T) {
	home := shortHome(t)

	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	pairPayload := decodePairPayload(t, r.Stdout)

	// Align the sessions dir to the daemon's COMPUTED path (no env override —
	// always <HOME>/.claude/projects/encode(workdir), HOME=home and
	// -pyry-workdir=home) and pre-create <initialUUID>.jsonl so the bootstrap
	// reconciliation has a stable stem. Per-conversation sessions take the
	// nil-resolver delivery path and never read this file. rotation_test pattern.
	sessionsDir := filepath.Join(home, ".claude", "projects", encodeWorkdir(home))
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	initialUUID := "66666666-6666-4666-8666-666666666666"
	if err := os.WriteFile(filepath.Join(sessionsDir, initialUUID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// idle=2s, uncapped: the only transitions are idle-driven, so a previously
	// active per-conversation session evicts ~2s after its last activation.
	startPerConvHarness(t, home, sessionsDir, initialUUID, fr.URL()+"/v1/server", "-pyry-idle-timeout=2s")

	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")

	phone := dialHelloPhone(t, home, fr, pairPayload.Token)

	// AC#4 (binding distinctness): two discussions, two distinct dedicated
	// sessions — neither the bootstrap, neither shared.
	convA := createConversationViaPhone(t, phone, 2)
	boundA := boundSessionID(t, convPath, convA)
	convB := createConversationViaPhone(t, phone, 3)
	boundB := boundSessionID(t, convPath, convB)
	if boundA == boundB {
		t.Fatalf("convA and convB share bound session %s — not distinct dedicated sessions", boundA)
	}

	// AC#1: each per-discussion session idle-evicts. lifecycle_state=="evicted"
	// is written only after the supervisor stops the child, so it faithfully
	// witnesses "claude process exited, RAM freed".
	waitForSessionState(t, regPath, boundA, "evicted", 5*time.Second)
	waitForSessionState(t, regPath, boundB, "evicted", 5*time.Second)

	// AC#2: a send_message to evicted convA reactivates its bound session and
	// delivers the turn. The ack is gated on Activate (respawn --session-id
	// boundA, same uuid ⇒ resumed from its own JSONL) + WaitReady + DeliverPrompt
	// even on the nil-resolver path, so a TypeAck proves end-to-end reactivation.
	const reqID uint64 = 4
	send := protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: convA,
			MessageID:      "m-1",
			Text:           "e2e-680-marker:wake up\n",
		}),
	}
	if err := phone.Send(send); err != nil {
		t.Fatalf("phone send send_message: %v", err)
	}
	// Drain any spinner `message` racing the ack within the documented 15s
	// respawn-latency bound.
	ack := recvEnvelope(t, phone, protocol.TypeAck, 15*time.Second)
	if ack.InReplyTo == nil || *ack.InReplyTo != reqID {
		t.Fatalf("ack InReplyTo: got %v, want pointer to %d", ack.InReplyTo, reqID)
	}
	waitForSessionState(t, regPath, boundA, "active", 3*time.Second)

	// AC#4 (no cross-bleed): convA's reactivation did NOT touch convB. With no
	// cap there is no LRU eviction, and an evicted session has no reason to wake
	// without its own send/attach — so the assertion window is well under the 2s
	// idle re-arm. This pins "churn in one discussion leaves another's session
	// untouched" and "convA's turn landed in convA's session, not convB's".
	assertEvicted(t, regPath, boundB)
}

// TestE2E_PerConversation_CapEvictsCrossDiscussion drives the active cap purely
// with create_conversation operations (each is a spawning Pool.Activate, the
// cleanest way to push past the cap) and asserts LRU victim selection:
//
//	AC#3 — activating one more session than the cap evicts the LRU active peer
//	       rather than exceeding the cap; the active count is never > cap at any
//	       settled checkpoint. The second eviction targets a *per-conversation*
//	       session (the security-sensitive cross-conversation eviction the PO
//	       flagged): remote activity in discussion C evicts discussion A.
//	AC#4 — only the deliberate LRU victim transitions; the bystander stays
//	       active and each discussion keeps its own distinct bound session.
//
// cap=2, no idle timeout: the only transitions are cap-driven, so the victim
// sequence is deterministic.
func TestE2E_PerConversation_CapEvictsCrossDiscussion(t *testing.T) {
	home := shortHome(t)

	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	pairPayload := decodePairPayload(t, r.Stdout)

	sessionsDir := filepath.Join(home, ".claude", "projects", encodeWorkdir(home))
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	initialUUID := "77777777-7777-4777-8777-777777777777"
	if err := os.WriteFile(filepath.Join(sessionsDir, initialUUID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	startPerConvHarness(t, home, sessionsDir, initialUUID, fr.URL()+"/v1/server", "-pyry-active-cap=2")

	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")

	bootstrapID := waitForBootstrap(t, regPath, 5*time.Second)

	phone := dialHelloPhone(t, home, fr, pairPayload.Token)

	// Create A — active = {bootstrap, A} = 2, exactly at cap, no evict.
	convA := createConversationViaPhone(t, phone, 2)
	boundA := boundSessionID(t, convPath, convA)
	// 50ms gap so lastActiveAt timestamps are distinguishable for pickLRUVictim.
	time.Sleep(50 * time.Millisecond)

	// Create B — activating B = 3 > cap → cap-evicts LRU peer = bootstrap.
	convB := createConversationViaPhone(t, phone, 3)
	boundB := boundSessionID(t, convPath, convB)
	time.Sleep(50 * time.Millisecond)

	// AC#3: bootstrap is the LRU victim; A and B stay active; count back to 2.
	waitForSessionState(t, regPath, bootstrapID, "evicted", 3*time.Second)
	assertActive(t, regPath, boundA)
	assertActive(t, regPath, boundB)

	// Create C — activating C = 3 > cap → cap-evicts LRU peer = boundA, a
	// per-conversation session: discussion C's activity evicts discussion A.
	convC := createConversationViaPhone(t, phone, 4)
	boundC := boundSessionID(t, convPath, convC)

	// AC#3: boundA is the LRU victim; B and C stay active; count never > 2.
	waitForSessionState(t, regPath, boundA, "evicted", 3*time.Second)
	assertActive(t, regPath, boundB)
	assertActive(t, regPath, boundC)

	// AC#4: only the deliberate LRU victim transitioned. The bystander boundB
	// stayed active across C's creation, and each discussion's bound session is
	// its own distinct UUID — no two discussions (or the bootstrap) collide.
	assertDistinctIDs(t, map[string]string{
		"bootstrap": bootstrapID,
		"boundA":    boundA,
		"boundB":    boundB,
		"boundC":    boundC,
	})
	// Bindings are unchanged from capture — eviction does not rebind a session.
	if got := boundSessionID(t, convPath, convA); got != boundA {
		t.Errorf("convA current_session_id changed: got %s, want %s", got, boundA)
	}
	if got := boundSessionID(t, convPath, convB); got != boundB {
		t.Errorf("convB current_session_id changed: got %s, want %s", got, boundB)
	}
	if got := boundSessionID(t, convPath, convC); got != boundC {
		t.Errorf("convC current_session_id changed: got %s, want %s", got, boundC)
	}
}

// startPerConvHarness spawns pyry with fakeclaude-TUI as the supervised child
// and relay wiring, threading arbitrary -pyry-* flags so one caller can pass
// -pyry-idle-timeout and another -pyry-active-cap. A generalization of
// respawn_after_eviction_test.go's startEvictionHarness (which hardcodes the
// idle flag); kept local to this file. The fakeclaude trigger path is never
// created, so the child never rotates — the steady state for these tests.
func startPerConvHarness(t *testing.T, home, sessionsDir, initialUUID, relayURL string, extraFlags ...string) {
	t.Helper()
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	fakeBin := ensureFakeClaudeBuilt(t)
	tmp := t.TempDir()

	flags := append([]string{
		"-pyry-workdir=" + home,
		"-pyry-relay=" + relayURL,
	}, extraFlags...)

	socket, cmd, stdout, stderr, doneCh := spawnWith(t, home, spawnOpts{
		claudeBin:  fakeBin,
		claudeArgs: []string{},
		extraFlags: flags,
		extraEnv: []string{
			"PYRY_ALLOW_INSECURE_RELAY=1",
			"PYRY_FAKE_CLAUDE_SESSIONS_DIR=" + sessionsDir,
			"PYRY_FAKE_CLAUDE_INITIAL_UUID=" + initialUUID,
			"PYRY_FAKE_CLAUDE_TRIGGER=" + filepath.Join(tmp, "rotate.trigger.never-created"),
			"PYRY_FAKE_CLAUDE_STDIN_LOG=" + filepath.Join(tmp, "fakeclaude-stdin.log"),
			"PYRY_FAKE_CLAUDE_TUI=1",
		},
	})

	h := &Harness{
		SocketPath:        socket,
		HomeDir:           home,
		ClaudeSessionsDir: sessionsDir,
		PID:               cmd.Process.Pid,
		Stdout:            stdout,
		Stderr:            stderr,
		cmd:               cmd,
		doneCh:            doneCh,
	}
	t.Cleanup(func() { h.teardown(t) })

	if err := h.waitForReady(); err != nil {
		t.Fatalf("e2e: %v", err)
	}
}

// dialHelloPhone dials a fakephone through fr, completes the hello/hello_ack
// handshake, and returns the ready client. The daemon must already be running
// (its binary leg registered with the relay) and paired (pairToken from a prior
// `pyry pair`). Close is registered for cleanup. Mirrors the dial+hello block
// in respawn_after_eviction_test.go.
func dialHelloPhone(t *testing.T, home string, fr *fakerelay.Server, pairToken string) *fakephone.Client {
	t.Helper()
	serverID := readPersistedServerID(t, home)

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	if !fr.WaitBinary(waitCtx, serverID) {
		t.Fatal("binary connection not registered within 5s")
	}

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelDial()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, pairToken, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

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
	gotHello, err := phone.Receive(3 * time.Second)
	if err != nil {
		t.Fatalf("phone receive hello_ack: %v", err)
	}
	if gotHello.Type != protocol.TypeHelloAck {
		t.Fatalf("hello_ack Type: got %q, want %q", gotHello.Type, protocol.TypeHelloAck)
	}
	return phone
}

// createConversationViaPhone sends an all-null create_conversation (server
// defaults) with envelope id reqID, drains any racing message/spinner envelopes
// to the conversation_created reply, and returns the server-minted conversation
// id. Returns only after the daemon has minted + bound + eagerly persisted the
// dedicated session (the reply is sent after the handler's reg.Save). The 15s
// budget covers the mint+activate spawn (Pool.Activate waits on claude's PTY).
func createConversationViaPhone(t *testing.T, phone *fakephone.Client, reqID uint64) string {
	t.Helper()
	req := protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeCreateConversation,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.CreateConversationPayload{}), // all fields null
	}
	if err := phone.Send(req); err != nil {
		t.Fatalf("phone send create_conversation (id=%d): %v", reqID, err)
	}
	env := recvEnvelope(t, phone, protocol.TypeConversationCreated, 15*time.Second)
	if env.InReplyTo == nil || *env.InReplyTo != reqID {
		t.Fatalf("conversation_created InReplyTo: got %v, want pointer to %d", env.InReplyTo, reqID)
	}
	var p protocol.ConversationCreatedPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal conversation_created payload: %v", err)
	}
	if p.ID == "" {
		t.Fatalf("conversation_created payload has empty id")
	}
	return p.ID
}

// boundSessionID reads conversations.json and returns the current_session_id
// bound to convID, failing if absent or empty. The create_conversation reply is
// sent after the handler's eager reg.Save (atomic rename), so the row is on disk
// by the time the phone observes the reply; the short poll absorbs any
// cross-process visibility lag.
func boundSessionID(t *testing.T, convPath, convID string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if reg, err := conversations.Load(convPath); err == nil {
			if conv, ok := reg.Get(conversations.ConversationID(convID)); ok && conv.CurrentSessionID != "" {
				return conv.CurrentSessionID
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("conversation %s never had a non-empty current_session_id within 2s\nfile:\n%s",
				convID, mustReadFile(t, convPath))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// assertEvicted checks regPath right now for id and fails if its
// lifecycle_state is not "evicted". The evicted-side counterpart of
// assertActive (cap_test.go): a one-shot "X must be evicted at this exact
// moment" bystander checkpoint, distinct from waitForSessionState's polling.
func assertEvicted(t *testing.T, regPath, id string) {
	t.Helper()
	reg := readRegistry(t, regPath)
	for _, e := range reg.Sessions {
		if e.ID == id {
			if e.LifecycleState != "evicted" {
				t.Fatalf("expected session %s evicted, but lifecycle_state=%q", id, e.LifecycleState)
			}
			return
		}
	}
	t.Fatalf("session %s not present in registry\nfile:\n%s", id, mustReadFile(t, regPath))
}

// assertDistinctIDs fails if any two named ids collide or any is empty. Pins the
// AC#4 invariant that each discussion (and the bootstrap) owns a distinct
// dedicated session UUID. The name map gives a readable collision message.
func assertDistinctIDs(t *testing.T, ids map[string]string) {
	t.Helper()
	seen := make(map[string]string, len(ids))
	for name, id := range ids {
		if id == "" {
			t.Fatalf("session id for %q is empty", name)
		}
		if prev, ok := seen[id]; ok {
			t.Fatalf("session id collision: %q and %q both bind %s", prev, name, id)
		}
		seen[id] = name
	}
}
