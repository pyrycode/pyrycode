//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestE2E_IdleEviction_RespawnsOnSendMessage pins the documented
// --pyry-idle-timeout contract end-to-end: an idle bootstrap session is
// evicted (SIGKILL + WARN log + registry transition), and an inbound
// send_message from a relay-routed phone respawns the supervised child
// and yields a wire-level ack — proving the silent-outage shape from
// pyrybox 2026-05-15 cannot ship again. #396 fixed the production bug;
// this test locks the contract.
//
// Phases:
//  1. Capture initial supervised PID via `pyry status`.
//  2. Wait for eviction: registry → "evicted", session.idle_eviction WARN
//     on stderr, initial PID gone.
//  3. Dial fakephone through fakerelay, complete hello/hello_ack.
//  4. Send send_message; expect TypeAck within the documented 15s
//     respawn-latency upper bound.
//  5. Assert `pyry status` reports a NEW supervised PID.
func TestE2E_IdleEviction_RespawnsOnSendMessage(t *testing.T) {
	// Blocked on #603: since #594, WriteUserTurn delivers via DeliverPrompt
	// behind a WaitReady idle-gate and acks only on a confirmed commit.
	// fakeclaude renders no claude TUI (no idle prompt / spinner), so WaitReady
	// never reaches idle and send_message replies binary_offline, not ack.

	const (
		knownConvID = "55555555-5555-4555-8555-555555555555"
		knownText   = "e2e-398-marker:wake up\n"
	)

	home := shortHome(t)

	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	pairPayload := decodePairPayload(t, r.Stdout)

	// Seed the conversations registry so ValidateConversation accepts
	// knownConvID once the supervisor is back up after respawn. The
	// parent dir already exists from `pyry pair`'s side-effect.
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[{"id":"` + knownConvID +
		`","cwd":"` + home +
		`","is_promoted":false,"last_used_at":"2026-01-01T00:00:00Z"}]}`)
	if err := os.WriteFile(convPath, convJSON, 0o600); err != nil {
		t.Fatalf("seed conversations.json: %v", err)
	}

	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "claude-sessions")
	initialUUID := "66666666-6666-4666-8666-666666666666"
	rotateTrigger := filepath.Join(tmp, "rotate.trigger.never-created")
	stdinLog := filepath.Join(tmp, "fakeclaude-stdin.log")

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	h := startEvictionHarness(t, home, sessionsDir, initialUUID, rotateTrigger,
		stdinLog, fr.URL()+"/v1/server", "2s")

	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")

	// Phase 1 — capture initial supervised PID.
	initialPID, ok := waitForChildPID(t, h, 3*time.Second)
	if !ok {
		t.Fatalf("never observed Child PID in pyry status within 3s")
	}

	// Phase 2 — wait for eviction. Three signals:
	//   (a) registry bootstrap → "evicted"
	//   (b) stderr carries the session.idle_eviction WARN with the
	//       documented fields
	//   (c) initial PID is gone from the kernel
	waitForBootstrapState(t, regPath, "evicted", 5*time.Second)

	// The four fields the operator's runbook depends on (#396). The
	// emitted session_id is the bootstrap session's UUID (s.id, set by
	// the pool), not the supervisor's -pyry-name; pin the key, not the
	// value, so a future ID format change doesn't flake the test.
	wantSubstrings := []string{
		"event=session.idle_eviction",
		"session_id=",
		"idle_timeout=2s",
		"bootstrap=true",
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		stderr := h.Stderr.String()
		if containsAll(stderr, wantSubstrings) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session.idle_eviction WARN missing one or more fields %v\nstderr:\n%s",
				wantSubstrings, stderr)
		}
		time.Sleep(50 * time.Millisecond)
	}

	deadline = time.Now().Add(3 * time.Second)
	for {
		if err := syscall.Kill(initialPID, 0); err == syscall.ESRCH {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("initial supervised PID %d still alive after eviction WARN",
				initialPID)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Phase 3 — open the phone via the relay.
	serverID := readPersistedServerID(t, home)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !fr.WaitBinary(ctx, serverID) {
		t.Fatal("binary connection not registered within 5s")
	}

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, pairPayload.Token, "phone-a")
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

	// Phase 4 — send the inbound and assert the ack arrives within the
	// documented 15s respawn-latency upper bound. The handler calls
	// Activate (which now waits on Supervisor.WaitForPTY per #396) before
	// emitting the ack, so a successful TypeAck proves end-to-end
	// respawn-and-write.
	const reqID uint64 = 2
	req := protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: knownConvID,
			MessageID:      "m-1",
			Text:           knownText,
		}),
	}
	sentAt := time.Now()
	if err := phone.Send(req); err != nil {
		t.Fatalf("phone send send_message: %v", err)
	}
	ack, err := phone.Receive(15 * time.Second)
	if err != nil {
		t.Fatalf("respawn did not complete within 15s after send_message: %v\nstderr:\n%s",
			err, h.Stderr.String())
	}
	respawnLatency := time.Since(sentAt)
	if respawnLatency > 15*time.Second {
		t.Fatalf("respawn latency %s exceeded documented 15s upper bound", respawnLatency)
	}
	if ack.Type != protocol.TypeAck {
		t.Fatalf("ack Type: got %q, want %q (payload=%s)",
			ack.Type, protocol.TypeAck, string(ack.Payload))
	}
	if ack.InReplyTo == nil || *ack.InReplyTo != reqID {
		t.Fatalf("ack InReplyTo: got %v, want pointer to %d", ack.InReplyTo, reqID)
	}

	// Phase 5 — assert the supervised PID changed. The handler emitted
	// the ack only after WriteUserTurn succeeded against the freshly-
	// bound PTY, so a stale ChildPID here would indicate the supervisor
	// never recorded the new child — a regression where Activate
	// returned without actually rebooting the supervisor.
	deadline = time.Now().Add(3 * time.Second)
	var newPID int
	for time.Now().Before(deadline) {
		pid, ok := childPIDFromStatus(t, h)
		if ok && pid != initialPID {
			newPID = pid
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if newPID == 0 {
		t.Fatalf("supervised PID never changed from initial=%d after ack received",
			initialPID)
	}
}

// startEvictionHarness spawns pyry with fakeclaude as the supervised child,
// relay wiring, and a caller-supplied idle-eviction window. Inlines the
// shape StartRotationWithRelay uses, adding a single extra flag for the
// idle timer; lives in this test file because the harness has no other
// caller that needs both extraEnv-free relay wiring AND a flag override.
func startEvictionHarness(t *testing.T, home, sessionsDir, initialUUID, trigger, stdinLog, relayURL, idleTimeout string) *Harness {
	t.Helper()
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	fakeBin := ensureFakeClaudeBuilt(t)

	socket, cmd, stdout, stderr, doneCh := spawnWith(t, home, spawnOpts{
		claudeBin:  fakeBin,
		claudeArgs: []string{},
		extraFlags: []string{
			"-pyry-workdir=" + home,
			"-pyry-relay=" + relayURL,
			"-pyry-idle-timeout=" + idleTimeout,
		},
		extraEnv: []string{
			"PYRY_ALLOW_INSECURE_RELAY=1",
			"PYRY_FAKE_CLAUDE_SESSIONS_DIR=" + sessionsDir,
			"PYRY_FAKE_CLAUDE_INITIAL_UUID=" + initialUUID,
			"PYRY_FAKE_CLAUDE_TRIGGER=" + trigger,
			"PYRY_FAKE_CLAUDE_STDIN_LOG=" + stdinLog,
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
	return h
}

// childPIDLineRE matches the `Child PID:     <n>` line in `pyry status`
// output. Whitespace is tolerated to absorb cosmetic alignment shifts;
// the parse anchor is the prefix + a decimal int.
var childPIDLineRE = regexp.MustCompile(`(?m)^Child PID:\s+(\d+)\s*$`)

// childPIDFromStatus runs `pyry status` once and returns the supervised
// child's PID if the supervisor is in Phase: running AND a Child PID line
// is present. Returns (0, false) otherwise — callers poll until the
// invariant they care about (PID present, PID changed) becomes true.
func childPIDFromStatus(t *testing.T, h *Harness) (int, bool) {
	t.Helper()
	r := h.Run(t, "status")
	if r.ExitCode != 0 {
		return 0, false
	}
	if !bytes.Contains(r.Stdout, []byte("Phase:         running")) {
		return 0, false
	}
	m := childPIDLineRE.FindSubmatch(r.Stdout)
	if m == nil {
		return 0, false
	}
	pid, err := strconv.Atoi(string(m[1]))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// waitForChildPID polls childPIDFromStatus until a PID surfaces or the
// deadline elapses. Used in phase 1 to capture the initial supervised
// PID before the idle timer fires.
func waitForChildPID(t *testing.T, h *Harness, timeout time.Duration) (int, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pid, ok := childPIDFromStatus(t, h); ok {
			return pid, true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, false
}

// containsAll reports whether s contains every substring in want.
// Order-independent so the slog text handler can reorder keys without
// breaking the assertion.
func containsAll(s string, want []string) bool {
	for _, w := range want {
		if !bytes.Contains([]byte(s), []byte(w)) {
			return false
		}
	}
	return true
}
