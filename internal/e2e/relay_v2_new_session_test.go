//go:build e2e

package e2e

import (
	"bytes"
	"context"
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

// TestRelayV2_NewSessionRotatesOnDisk is the fake-daemon e2e for the phone-driven
// `new_session` v2 control verb (#831, split from #962). It confirms the wired
// path live: a paired interactive phone handshakes to one daemon over a real
// Noise session, sends a sealed `new_session` frame, and the supervised claude's
// session UUID rotation is observable on disk (the registry's bootstrap id follows
// the fresh JSONL) — the only observable, since new_session has no reply and no
// broadcast:
//
//	phone new_session frame → Noise decrypt → dispatchAppFrame intercept →
//	  handleNewSession (interactive ✓) → SessionStarter.StartNewSession() →
//	    ClearInputLine (Ctrl-U) + TypePrompt("/clear") + "\r" → fakeclaude stdin →
//	  clear-rotate mode: close old <initialUUID>.jsonl, open fresh <uuid>.jsonl →
//	  rotation watcher follows the most-recent JSONL → registry id := <uuid>.
//
// STRUCTURAL-CAUSALITY GUARD (mirrors the interrupt test's "only source of a
// turn_end"). The file trigger points at a path that is never created, so the
// ONLY thing that rotates the JSONL in this test is the /clear keystroke — an
// observed registry id change ⟺ the new_session frame drove /clear to the child.
// A baseline assertion (id == initialUUID before the frame) makes the "id changed"
// assertion below non-vacuous, and a direct stdin-log oracle (fakeclaude's own
// byte record of /clear) is belt-and-suspenders with different fabric than the
// daemon's registry write.
//
// The send is a bounded re-send loop, not a single blind send: new_session is
// fire-and-forget and drops silently (ErrNoLiveSession, Warn-logged) if the
// tui-driver session is not yet attached to the supervisor when the frame is
// processed. fakeclaude's clear-rotate is one-shot, so once the session is live
// the first /clear rotates and every later frame is inert.
func TestRelayV2_NewSessionRotatesOnDisk(t *testing.T) {
	const initialUUID = "55555555-5555-4555-8555-555555555555"

	home := shortHome(t)

	// Pair one interactive device.
	rA := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if rA.ExitCode != 0 {
		t.Fatalf("pyry pair phone-a exit=%d\nstdout:\n%s\nstderr:\n%s", rA.ExitCode, rA.Stdout, rA.Stderr)
	}
	payloadA := decodePairPayload(t, rA.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Align the sessions dir to the daemon's COMPUTED path and pre-create
	// <initialUUID>.jsonl BEFORE the daemon starts. The bootstrap id starts at
	// initialUUID deterministically — seedBootstrapRegistry warm-starts it and the
	// daemon spawns claude with --session-id (#839), no startup scan (the #642 recipe).
	sessionsDir := claudeSessionsDir(home)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	initialJSONL := filepath.Join(sessionsDir, initialUUID+".jsonl")
	if err := os.WriteFile(initialJSONL, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}

	tmp := t.TempDir()
	// The file trigger points at a path that is never created — the /clear
	// keystroke is the ONLY thing that rotates the JSONL in this test.
	neverCreated := filepath.Join(tmp, "rotate.trigger.never-created")
	stdinLog := filepath.Join(tmp, "fakeclaude-stdin.log")

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	h := StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverCreated,
		stdinLog, fr.URL()+"/v2/server",
		"PYRY_MOBILE_V2=1",
		"PYRY_FAKE_CLAUDE_CLEAR_ROTATES=1",
	)
	t.Cleanup(func() { h.Stop(t) })

	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phoneA, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payloadA.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone A dial: %v", err)
	}
	t.Cleanup(func() { _ = phoneA.Close() })
	// Interactive — the capability handleNewSession requires. new_session has no
	// reply, so the recv CipherState is unused.
	sendA, _ := driveHandshakeToOpenDaemonInteractive(t, phoneA, pubKey, payloadA.Token)

	// Baseline (precondition / AC-2 non-vacuity): the daemon reconciled the
	// bootstrap id to initialUUID BEFORE any rotation, so the "id changed"
	// assertion below is non-vacuous.
	pre := waitForBootstrapID(t, regPath, initialUUID, 5*time.Second)

	// Drive new_session until it rotates (AC-1). Re-send every ~250 ms until the
	// registry id rotates away from initialUUID or a ~10 s deadline elapses. Fresh
	// envelope ids per send (the send CipherState nonce increments per Encrypt;
	// the daemon recv nonce follows). The main goroutine is the sole writer of the
	// phone conn — there is no reply to read.
	var reqID uint64 = 1
	var post registryEntry
	rotated := false
	deadline := time.Now().Add(10 * time.Second)
	for !rotated && time.Now().Before(deadline) {
		sendNewSessionFrame(t, phoneA, sendA, reqID)
		reqID++
		poll := time.Now().Add(250 * time.Millisecond)
		for time.Now().Before(poll) {
			if e, ok := readBootstrapIfPresent(regPath); ok && e.ID != "" && e.ID != initialUUID {
				post = e
				rotated = true
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	if !rotated {
		t.Fatalf("AC-1/AC-2: registry bootstrap id never rotated away from %q after new_session within 10s; "+
			"the /clear keystroke did not drive a rotation (the frame may have been dropped on a detached "+
			"session, or the clear-rotate mode is not wired)\nfile:\n%s", initialUUID, mustReadFile(t, regPath))
	}

	// AC-2: assert the rotation on disk — the exact shape rotation_test.go uses.
	if !uuidStemPattern.MatchString(post.ID) {
		t.Errorf("post-rotation id %q does not match UUIDv4 stem pattern", post.ID)
	}
	if !post.LastActiveAt.After(pre.LastActiveAt) {
		t.Errorf("last_active_at did not advance: pre=%s post=%s",
			pre.LastActiveAt.Format(time.RFC3339Nano),
			post.LastActiveAt.Format(time.RFC3339Nano))
	}

	// Direct keystroke oracle (belt-and-suspenders, different fabric): fakeclaude's
	// own stdin record that the frame routed /clear, independent of the daemon's
	// registry write. A short bounded poll closes the cross-process fsync-visibility
	// window (mirrors the interrupt test's stdin-log oracle).
	var logBytes []byte
	clearDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(clearDeadline) {
		logBytes, _ = os.ReadFile(stdinLog)
		if bytes.Contains(logBytes, []byte("/clear")) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !bytes.Contains(logBytes, []byte("/clear")) {
		t.Fatalf("AC-1: fakeclaude's stdin log has no /clear bytes; the new_session frame never routed "+
			"/clear to the child (e.g. a capability-gate regression). log=%q", logBytes)
	}

	// Stable-state check: no background path reverts the rotated id (à la
	// rotation_test.go). 200ms is ~2× the watcher's typical save latency.
	time.Sleep(200 * time.Millisecond)
	after := readBootstrap(t, regPath)
	if after.ID != post.ID {
		t.Errorf("bootstrap id reverted: post=%s after=%s\nfile:\n%s",
			post.ID, after.ID, mustReadFile(t, regPath))
	}
}

// TestRelayV2_NewSessionNonInteractiveInert is the AC-3 negative: a NON-interactive
// conn's new_session is inert (handleNewSession's capability gate short-circuits
// before StartNewSession, so no /clear is typed and no rotation happens).
//
// Non-vacuity of the negative comes from TestRelayV2_NewSessionRotatesOnDisk
// proving the SAME harness + clear-rotate mode DOES rotate for an interactive
// conn. A broken interactive gate (non-interactive also rotates) would surface
// within this test's bounded window as a /clear in the stdin log AND a registry id
// change — so the window catches a real regression, not just idle time. The
// stdin-log absence is the direct proof the gate short-circuited before the
// keystroke; the registry-unchanged is the downstream proof.
func TestRelayV2_NewSessionNonInteractiveInert(t *testing.T) {
	const initialUUID = "44444444-4444-4444-8444-444444444444"

	home := shortHome(t)

	rA := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if rA.ExitCode != 0 {
		t.Fatalf("pyry pair phone-a exit=%d\nstdout:\n%s\nstderr:\n%s", rA.ExitCode, rA.Stdout, rA.Stderr)
	}
	payloadA := decodePairPayload(t, rA.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	sessionsDir := claudeSessionsDir(home)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	initialJSONL := filepath.Join(sessionsDir, initialUUID+".jsonl")
	if err := os.WriteFile(initialJSONL, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("pre-create initial jsonl: %v", err)
	}

	tmp := t.TempDir()
	neverCreated := filepath.Join(tmp, "rotate.trigger.never-created")
	stdinLog := filepath.Join(tmp, "fakeclaude-stdin.log")

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	h := StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverCreated,
		stdinLog, fr.URL()+"/v2/server",
		"PYRY_MOBILE_V2=1",
		"PYRY_FAKE_CLAUDE_CLEAR_ROTATES=1",
	)
	t.Cleanup(func() { h.Stop(t) })

	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payloadA.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	// NON-interactive handshake: the capability gate in handleNewSession must
	// short-circuit before StartNewSession.
	sendA, _ := driveHandshakeToOpenDaemon(t, phone, pubKey, payloadA.Token)

	// Baseline: id == initialUUID before the frame.
	waitForBootstrapID(t, regPath, initialUUID, 5*time.Second)

	// One new_session frame on the non-interactive conn.
	sendNewSessionFrame(t, phone, sendA, 1)

	// Assert inert over a bounded window: the registry id stays initialUUID AND the
	// stdin log never gains /clear.
	inertDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(inertDeadline) {
		if logBytes, _ := os.ReadFile(stdinLog); bytes.Contains(logBytes, []byte("/clear")) {
			t.Fatalf("AC-3: non-interactive new_session routed /clear to the child; the interactive gate "+
				"did not short-circuit. log=%q", logBytes)
		}
		if e := readBootstrap(t, regPath); e.ID != initialUUID {
			t.Fatalf("AC-3: non-interactive new_session rotated the bootstrap id to %q; expected inert (still %q)",
				e.ID, initialUUID)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// sendNewSessionFrame seals a payload-less new_session control envelope with cs
// (the phone's send CipherState) and writes it to phone. new_session is
// fire-and-forget — no reply — so callers observe its effect on disk (the
// registry rotation) or its absence, never a returned envelope. The request id is
// cosmetic (there is no ack); callers bump it per re-send to keep envelope ids
// fresh across a bounded re-send loop.
func sendNewSessionFrame(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, reqID uint64) {
	t.Helper()
	env, err := json.Marshal(protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeNewSession,
		TS:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal new_session envelope: %v", err)
	}
	cipher, err := cs.Encrypt(env)
	if err != nil {
		t.Fatalf("seal new_session envelope: %v", err)
	}
	sendNoiseMsg(t, phone, cipher)
}
