//go:build e2e_realclaude

package realclaude

// TestInteractiveBootstrapLiveness is the #854 deliverable: a real-claude
// interactive two-turn liveness test that proves a fresh daemon's bootstrap
// session — the one Desktop and Mobile drive over the relay — actually answers
// a live send.
//
// It stands up the real interactive stack end-to-end: a freshly-spawned pyry
// daemon in an isolated working directory (no prior session on disk), real
// `claude` on `--model haiku`, and a headless phone that drives two turns over
// the encrypted Noise_IK v2 wire. Each turn asserts ONLY liveness — any
// non-empty streamed assistant_delta for the driving conversation within a
// generous timeout — never claude's words (asserting content would be
// non-deterministic and would risk the substrate guard).
//
//   - Turn 1 proves the child creates its transcript and the reply bridge binds.
//   - Turn 2 proves the bridge survives once the session exists (the offset path
//     past the first turn).
//
// RED/GREEN oracle (AC #4): on current `main` the bootstrap-bound conversation's
// reply is resolved by the by-id resolver keyed on the bootstrap POOL id, but the
// bootstrap claude spawns without --session-id and mints its own on-disk uuid, so
// <poolID>.jsonl never appears and the turn stream loops forever — Turn 1 times
// out. After the fix (resolveTarget routes the bootstrap-bound case through the
// PID probe) the reply binds and both turns pass.
//
// This is v2-only by necessity: the structured reply stream
// (startInteractiveTurnStreamV2, the deadlocking producer) exists only on the v2
// leg. A v1 test would never exercise the bootstrap resolver at all.
//
// The daemon harness below is TRANSCRIBED from internal/e2e (relay_v2_daemon_test.go,
// harness.go, relay_v2_handshake_test.go) rather than shared: those files are
// build-tag-fenced under `e2e`, disjoint from this file's `e2e_realclaude`, so the
// same duplication ensurePyryBuilt already makes is unavoidable. The daemon spawn
// routes the control socket through shortSocketPath (the #860 fix) — a
// <home>/pyry.sock under the long authenticated t.TempDir() HOME would overflow
// macOS's 104-byte sun_path limit, bind(2) would return EINVAL, and RED could
// never reach the deadlock it exists to observe.

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/pair"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Fixed identifiers for the seeded state. chosenUUID is the bootstrap session's
// POOL id (pinned deterministically via seedBootstrapRegistry, no post-startup
// adopt-by-mtime read-back). convID is the driving conversation, bound to the
// bootstrap via seedBoundConversation. Real claude still writes its transcript at
// its OWN minted uuid (no --session-id), so chosenUUID != the on-disk stem — which
// is exactly why the reply must bind by PID probe, not by the bound uuid.
const (
	liveBootstrapUUID = "77777777-7777-4777-8777-777777777777"
	liveConvID        = "55555555-5555-4555-8555-555555555555"
)

func TestInteractiveBootstrapLiveness(t *testing.T) {
	skipUnlessPTYGate(t)
	// No t.Parallel: WithWorktreeAuthenticated calls t.Setenv.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("realclaude: claude not on PATH: %v", err)
	}
	home := WithWorktreeAuthenticated(t) // skips cleanly when no creds
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("realclaude: resolve claude: %v", err)
	}

	// Isolated workdir under the authenticated HOME. Load-bearing twice: it
	// guarantees the fresh-daemon state (empty claude sessions dir) AND sidesteps
	// the shared-session-folder collision (#828) by construction.
	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir workdir: %v", err)
	}

	// Pair a device BEFORE the daemon starts (mints the bearer token + the
	// responder static pubkey the phone pins; writes server-id + devices registry
	// the daemon loads at startup).
	exit, stdout, stderr := runPyry(t, "pair", "-pyry-name=test", "--name=phone-a")
	if exit != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	payload := decodePairPayload(t, stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Seed the deterministic bootstrap id + the driving conversation binding
	// BEFORE the daemon starts (the registry is loaded once at startup, no reload).
	seedBootstrapRegistry(t, home, liveBootstrapUUID)
	seedBoundConversation(t, home, liveConvID, liveBootstrapUUID, workdir)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	d := spawnBootstrapDaemon(t, home, workdir, claudeBin, fr.URL()+"/v2/server")
	t.Cleanup(func() { d.stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	initSend, initRecv := driveHandshakeInteractive(t, phone, pubKey, payload.Token)

	// A per-run nonce so Turn 2 differs from Turn 1 and reruns differ — enough to
	// defeat any accidental caching, without asserting on content.
	nonce := time.Now().UnixNano()

	// Turn 1 — proves the child creates its transcript and the reply bridge binds.
	// Generous budget: real claude on a cold PTY session (spawn + model load +
	// first-turn reply + growth-confirm + probe bind), not fakeclaude milliseconds.
	sealSendMessage(t, phone, initSend, 2, liveConvID, "m-1",
		fmt.Sprintf("Reply with a single short word. run=%d turn=1", nonce))
	drainForAssistantReply(t, phone, initRecv, liveConvID, 1, 120*time.Second)

	// Turn 2 — proves the bridge survives once the session exists (the offset path
	// past the first turn).
	sealSendMessage(t, phone, initSend, 3, liveConvID, "m-2",
		fmt.Sprintf("Reply with a single short word. run=%d turn=2", nonce))
	drainForAssistantReply(t, phone, initRecv, liveConvID, 2, 90*time.Second)
}

// --- turn drive + drain -----------------------------------------------------

// sealSendMessage seals a send_message envelope under the phone's send
// CipherState and writes it as an InnerFrameV2(noise_msg).
func sealSendMessage(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, id uint64, convID, msgID, text string) {
	t.Helper()
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:   id,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: convID,
			MessageID:      msgID,
			Text:           text,
		}),
	})
	if err != nil {
		t.Fatalf("marshal send_message envelope: %v", err)
	}
	ciphertext, err := cs.Encrypt(reqEnv)
	if err != nil {
		t.Fatalf("seal send_message: %v", err)
	}
	sendNoiseMsg(t, phone, ciphertext)
}

// drainForAssistantReply reads binary→phone noise_msg frames in receive order —
// the receive nonce is sequential, so every frame MUST be decrypted in order to
// keep the CipherState in sync — decrypts each, and returns once it observes a
// non-empty assistant_delta for convID (the liveness signal). The interleaved
// ack / turn_state / tool_use / turn_end envelopes are decrypted and skipped.
// A per-turn timeout with no such delta is the deadlock RED captures on `main`.
func drainForAssistantReply(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, turn int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("turn %d: no non-empty assistant_delta for %q within %s — the bootstrap turn stream never bound (the fresh-daemon deadlock #854 fixes)",
				turn, convID, timeout)
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				t.Fatalf("turn %d: no non-empty assistant_delta for %q within %s — the bootstrap turn stream never bound (the fresh-daemon deadlock #854 fixes)",
					turn, convID, timeout)
			}
			t.Fatalf("turn %d: phone receive: %v", turn, err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("turn %d: decode inner frame: %v", turn, err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			// A non-noise_msg control frame (e.g. rekey) does not advance the
			// receive nonce — skip without decrypting.
			continue
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("turn %d: decode inner data: %v", turn, err)
		}
		plain, err := cs.Decrypt(cipher)
		if err != nil {
			t.Fatalf("turn %d: phone decrypt (receive-nonce desync?): %v", turn, err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("turn %d: decode envelope: %v", turn, err)
		}
		if env.Type != protocol.TypeAssistantDelta {
			continue // ack, turn_state, tool_use, turn_end, … — keep draining in order
		}
		var p protocol.AssistantDeltaPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("turn %d: decode assistant_delta payload: %v", turn, err)
		}
		if p.ConversationID == convID && strings.TrimSpace(p.Text) != "" {
			t.Logf("turn %d: non-empty assistant_delta (seq=%d, %d bytes) for %q",
				turn, p.Seq, len(p.Text), convID)
			return
		}
	}
}

// --- Noise wire helpers (transcribed from internal/e2e) ---------------------

// driveHandshakeInteractive runs a paired-device Noise_IK handshake from the
// phone side, advertising the interactive capability so the daemon grants it and
// the structured turn stream reaches this conn. Returns the initiator's
// CipherStates (initSend encrypts phone→binary, initRecv decrypts binary→phone).
func driveHandshakeInteractive(t *testing.T, phone *fakephone.Client, pubKey []byte, token string) (*noise.CipherState, *noise.CipherState) {
	t.Helper()
	initPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("phone keygen: %v", err)
	}
	initiator, err := noise.NewInitiator(initPriv.Bytes(), pubKey)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarlyInteractive(t, token))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	sendNoiseInit(t, phone, initMsg)

	inner := readInnerFrame(t, phone, 10*time.Second)
	if inner.Type != protocol.TypeNoiseResp {
		t.Fatalf("handshake: got inner type %q, want %q", inner.Type, protocol.TypeNoiseResp)
	}
	respRaw, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatalf("decode noise_resp data: %v", err)
	}
	earlyAck, initSend, initRecv, err := initiator.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("initiator.ReadResp: %v", err)
	}
	var ackEnv protocol.Envelope
	if err := json.Unmarshal(earlyAck, &ackEnv); err != nil {
		t.Fatalf("decode hello_ack envelope: %v", err)
	}
	if ackEnv.Type != protocol.TypeHelloAck {
		t.Fatalf("early-data type = %q, want %q", ackEnv.Type, protocol.TypeHelloAck)
	}
	var ack protocol.HelloAckPayload
	if err := json.Unmarshal(ackEnv.Payload, &ack); err != nil {
		t.Fatalf("decode hello_ack payload: %v", err)
	}
	if !slices.Contains(ack.Capabilities, protocol.CapabilityInteractive) {
		t.Fatalf("daemon did not grant interactive (hello_ack capabilities=%v); the turn stream would not reach this conn", ack.Capabilities)
	}
	return initSend, initRecv
}

func buildHelloEarlyInteractive(t *testing.T, token string) []byte {
	t.Helper()
	payload, err := json.Marshal(protocol.HelloClientPayload{
		Role:             "client",
		DeviceName:       "realclaude-e2e-phone",
		ClientVersion:    "0.0.1-test",
		ProtocolVersions: []string{"v2"},
		Token:            token,
		Capabilities:     []string{protocol.CapabilityInteractive},
	})
	if err != nil {
		t.Fatalf("marshal interactive hello payload: %v", err)
	}
	envBytes, err := json.Marshal(protocol.Envelope{
		ID:      1,
		Type:    protocol.TypeHello,
		TS:      time.Now().UTC(),
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("marshal interactive hello envelope: %v", err)
	}
	return envBytes
}

func sendNoiseInit(t *testing.T, phone *fakephone.Client, msg []byte) {
	t.Helper()
	wireFrame, err := json.Marshal(protocol.InnerFrameV2{
		Version: protocol.V2Version,
		Type:    protocol.TypeNoiseInit,
		Data:    base64.StdEncoding.EncodeToString(msg),
	})
	if err != nil {
		t.Fatalf("marshal noise_init wire frame: %v", err)
	}
	if err := phone.SendBytes(wireFrame); err != nil {
		t.Fatalf("phone send noise_init: %v", err)
	}
}

func sendNoiseMsg(t *testing.T, phone *fakephone.Client, ciphertext []byte) {
	t.Helper()
	wireFrame, err := json.Marshal(protocol.InnerFrameV2{
		Version: protocol.V2Version,
		Type:    protocol.TypeNoiseMsg,
		Data:    base64.StdEncoding.EncodeToString(ciphertext),
	})
	if err != nil {
		t.Fatalf("marshal noise_msg wire frame: %v", err)
	}
	if err := phone.SendBytes(wireFrame); err != nil {
		t.Fatalf("phone send noise_msg: %v", err)
	}
}

func readInnerFrame(t *testing.T, phone *fakephone.Client, timeout time.Duration) protocol.InnerFrameV2 {
	t.Helper()
	raw, err := phone.ReceiveBytes(timeout)
	if err != nil {
		t.Fatalf("phone receive: %v", err)
	}
	var inner protocol.InnerFrameV2
	if err := json.Unmarshal(raw, &inner); err != nil {
		t.Fatalf("decode inner frame: %v", err)
	}
	return inner
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// --- daemon harness (transcribed from internal/e2e/harness.go) --------------

// bootstrapDaemon owns one spawned real-pyry daemon.
type bootstrapDaemon struct {
	socketPath string
	cmd        *exec.Cmd
	doneCh     chan struct{}
	stderr     *lockedBuffer
}

// spawnBootstrapDaemon forks real pyry against the isolated authenticated HOME,
// with real claude as the supervised child on --model haiku, the control socket
// under a short /tmp dir (the #860 sun_path fix), and the relay wired to relayURL.
// Blocks until the control socket is dialable.
func spawnBootstrapDaemon(t *testing.T, home, workdir, claudeBin, relayURL string) *bootstrapDaemon {
	t.Helper()
	bin := ensurePyryBuilt(t) // builds with real HOME (warm cache); runs with isolated HOME
	socket := shortSocketPath(t)
	stderr := &lockedBuffer{}

	args := []string{
		"-pyry-socket=" + socket,
		"-pyry-name=test",
		"-pyry-claude=" + claudeBin,
		"-pyry-idle-timeout=0",
		"-pyry-workdir=" + workdir,
		"-pyry-relay=" + relayURL,
		"--",
		"--model", "haiku",
		"--dangerously-skip-permissions",
	}
	cmd := exec.Command(bin, args...)
	// os.Environ() already carries the isolated HOME and the credential
	// (WithWorktreeAuthenticated t.Setenv's both). Add the relay switches.
	cmd.Env = append(os.Environ(), "PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_MOBILE_V2=1")
	cmd.Stderr = io.MultiWriter(os.Stderr, stderr) // DEBUG tee

	if err := cmd.Start(); err != nil {
		t.Fatalf("realclaude: pyry start: %v", err)
	}
	doneCh := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(doneCh)
	}()

	d := &bootstrapDaemon{socketPath: socket, cmd: cmd, doneCh: doneCh, stderr: stderr}
	if err := d.waitForReady(10 * time.Second); err != nil {
		t.Fatalf("realclaude: daemon not ready: %v\nstderr:\n%s", err, stderr.String())
	}
	return d
}

// waitForReady polls the control socket until Dial succeeds or the daemon exits.
func (d *bootstrapDaemon) waitForReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(d.socketPath); err == nil {
			c, err := net.Dial("unix", d.socketPath)
			if err == nil {
				_ = c.Close()
				return nil
			}
		}
		select {
		case <-d.doneCh:
			return fmt.Errorf("pyry exited before ready")
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("pyry not ready within %s", timeout)
}

// stop tears the daemon down: SIGTERM → grace → SIGKILL, then removes the socket.
// Idempotent-safe under t.Cleanup ordering (a second signal to a dead process is
// a harmless error).
func (d *bootstrapDaemon) stop(t *testing.T) {
	t.Helper()
	if d.cmd == nil || d.cmd.Process == nil {
		return
	}
	_ = d.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-d.doneCh:
	case <-time.After(3 * time.Second):
		_ = d.cmd.Process.Signal(syscall.SIGKILL)
		select {
		case <-d.doneCh:
		case <-time.After(1 * time.Second):
			t.Logf("realclaude: daemon pid=%d did not exit after SIGKILL", d.cmd.Process.Pid)
		}
	}
	_ = os.Remove(d.socketPath)
}

// shortSocketPath returns a control-socket path short enough to stay under
// macOS's 104-byte sun_path limit regardless of the (long, authenticated)
// t.TempDir() HOME. Transcribed verbatim from internal/e2e/harness.go (#860).
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pyry-sock-*")
	if err != nil {
		t.Fatalf("realclaude: MkdirTemp socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "pyry.sock")
}

// runPyry invokes the cached pyry binary with the given args verbatim under the
// isolated HOME (via os.Environ(), which WithWorktreeAuthenticated has pinned),
// captures stdout/stderr, and returns the exit code. Used for the offline `pair`
// verb (no control socket).
func runPyry(t *testing.T, args ...string) (int, []byte, []byte) {
	t.Helper()
	bin := ensurePyryBuilt(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("realclaude: pyry %v timed out\nstdout:\n%s\nstderr:\n%s", args, stdout.String(), stderr.String())
	}
	var exit int
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		exit = 0
	case errors.As(err, &exitErr):
		exit = exitErr.ExitCode()
	default:
		t.Fatalf("realclaude: pyry %v exec failed: %v", args, err)
	}
	return exit, stdout.Bytes(), stderr.Bytes()
}

// readPersistedServerID reads the daemon's server-id, polling for the file.
func readPersistedServerID(t *testing.T, home string) string {
	t.Helper()
	path := filepath.Join(home, ".pyry", "test", "server-id")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			return strings.TrimSpace(string(data))
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server-id file never appeared at %s", path)
	return ""
}

// seedBootstrapRegistry writes sessions.json for the "test" instance with a
// single bootstrap entry at bootstrapUUID, so Pool.New warm-starts the bootstrap
// POOL id at that uuid deterministically (the daemon also spawns claude with
// --session-id <bootstrapUUID>, #839; there is no startup adopt-by-mtime scan).
// Transcribed from internal/e2e/harness.go (#861).
func seedBootstrapRegistry(t *testing.T, home, bootstrapUUID string) {
	t.Helper()
	regDir := filepath.Join(home, ".pyry", "test")
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Fatalf("seed sessions.json: mkdir: %v", err)
	}
	regJSON := []byte(`{"version":1,"sessions":[{"id":"` + bootstrapUUID +
		`","label":"","created_at":"2026-01-01T00:00:00Z","last_active_at":"2026-01-01T00:00:00Z","bootstrap":true,"lifecycle_state":"active"}]}`)
	if err := os.WriteFile(filepath.Join(regDir, "sessions.json"), regJSON, 0o600); err != nil {
		t.Fatalf("seed sessions.json: write: %v", err)
	}
}

// seedBoundConversation writes conversations.json binding convID to
// boundSessionID (the bootstrap pool id), so sessionRouter.Route resolves the
// send to the bootstrap supervisor instead of rejecting an empty binding (#678).
// cwd is the daemon workdir. Transcribed from internal/e2e/harness.go.
func seedBoundConversation(t *testing.T, home, convID, boundSessionID, cwd string) {
	t.Helper()
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[{"id":"` + convID +
		`","cwd":"` + cwd +
		`","current_session_id":"` + boundSessionID +
		`","is_promoted":false,"last_used_at":"2026-01-01T00:00:00Z"}]}`)
	if err := os.WriteFile(convPath, convJSON, 0o600); err != nil {
		t.Fatalf("seed conversations.json: %v", err)
	}
}

// decodePairPayload scans pair's stdout for the encoded payload line.
func decodePairPayload(t *testing.T, stdout []byte) pair.Payload {
	t.Helper()
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if p, err := pair.Decode(line); err == nil {
			return p
		}
	}
	t.Fatalf("no decodable pair payload found in stdout:\n%s", stdout)
	return pair.Payload{}
}

// waitBinaryHello blocks until the binary registers with the relay for serverID.
func waitBinaryHello(t *testing.T, fr *fakerelay.Server, serverID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !fr.WaitBinary(ctx, serverID) {
		t.Fatal("binary connection not registered within 10s")
	}
}

func relayTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// lockedBuffer is a mutex-guarded byte buffer so the test goroutine can read the
// daemon's captured stderr while os/exec's copy goroutine is still writing.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
