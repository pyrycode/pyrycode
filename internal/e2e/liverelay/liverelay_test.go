//go:build e2e_liverelay

// Package liverelay holds the #968 deliverable: one opt-in test that proves the
// daemon ↔ REAL pyrycode-relay pairing with a full round-trip, closing the gap
// that only hand-runs have covered. Structural twin of e2e_realclaude — an
// opt-in build tag, its own package, wired into `preship`, NOT `check`.
//
// Every other relay e2e test runs against the in-process fakerelay
// (internal/e2e/internal/fakerelay). This one swaps only the relay: a locally
// built pyrycode-relay binary from the sibling repo, spawned hermetically on a
// loopback-bound plaintext listener. Everything else — pair, Noise_IK
// handshake, envelope round-trip — is ported 1:1 from
// testV2DaemonListConversationsRoundTrip (internal/e2e/relay_v2_daemon_test.go),
// because the relay is a dumb, stateless router: the handshake and the verb
// round-trip ride end-to-end INSIDE the routing frames, which the relay
// forwards without inspecting.
//
// The daemon/Noise helpers below are TRANSCRIBED from internal/e2e and
// internal/e2e/realclaude rather than shared: those files are build-tag-fenced
// under `e2e` / `e2e_realclaude`, disjoint from this file's `e2e_liverelay`, so
// the duplication ensurePyryBuilt already makes is unavoidable and is the
// blessed pattern (#854/#997).
//
// Because the tag is distinct, this file is invisible to `go build ./...`,
// `go vet ./...`, `make test`, and `make e2e` (-tags e2e) — AC #3 is satisfied
// by tag exclusion alone, no path filter.
package liverelay

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestLiveRelay_ListConversationsRoundTrip drives one full round-trip against a
// REAL relay binary: device identity → WS connect → Noise_IK handshake → a
// list_conversations verb round-trip. ANY stage error, or a reply that does not
// match the seeded conversation, fails the test — this is not a no-panic smoke
// check (AC #1).
func TestLiveRelay_ListConversationsRoundTrip(t *testing.T) {
	const knownConvID = "77777777-7777-4777-8777-777777777777"

	// Stage 0 — resolve (or build) a relay binary; skip loud without the sibling.
	relayBin := ensureRelayBuilt(t)

	// Stage 1 — start the real relay, loopback-bound. Waits for /healthz 200.
	relay := startRelay(t, relayBin)

	// Stage 2 — device identity (ephemeral, under a temp HOME auto-removed by t).
	home := t.TempDir()
	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:         home,
		InstanceName: "test",
		Relay:        relay.baseWS,
		DeviceName:   "phone-a",
	})
	if err != nil {
		t.Fatalf("paireddevice.Setup: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Stage 3 — seed one known conversation row the handler will read back
	// (mirrors testV2DaemonListConversationsRoundTrip verbatim, 0600).
	convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
	convJSON := []byte(`{"conversations":[{"id":"` + knownConvID +
		`","cwd":"` + home +
		`","is_promoted":false,"last_used_at":"2026-01-01T00:00:00Z"}]}`)
	if err := os.WriteFile(convPath, convJSON, 0o600); err != nil {
		t.Fatalf("seed conversations.json: %v", err)
	}

	// Stage 4 — start the daemon with a BARE base relay URL (no path). The
	// daemon's resolveDialURL appends /v1/server — the endpoint the real relay
	// serves. Do NOT copy the fakerelay test's /v2/server suffix: the real relay
	// has no such route (it maps only /v1/server and /v1/client).
	spawnDaemon(t, home, payload.Relay)

	// Stage 5 — wait until the daemon's binary connection is registered with the
	// relay, then dial the phone. See waitBinaryRegistered for why /healthz
	// replaces fakerelay's WaitBinary hook.
	relay.waitBinaryRegistered(t, 10*time.Second)

	serverID := readPersistedServerID(t, home)
	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, payload.Relay, serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	// Stage 6 — Noise_IK handshake to open state.
	initSend, initRecv := driveHandshake(t, phone, pubKey, payload.Token)

	// Stage 7 — verb round-trip + assert (the "reply matches what was sent" gate).
	const reqID uint64 = 21
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("marshal request envelope: %v", err)
	}
	ciphertext, err := initSend.Encrypt(reqEnv)
	if err != nil {
		t.Fatalf("seal request envelope: %v", err)
	}
	sendNoiseMsg(t, phone, ciphertext)

	inner := readInnerFrame(t, phone, 3*time.Second)
	if inner.Type != protocol.TypeNoiseMsg {
		t.Fatalf("reply inner type = %q, want %q", inner.Type, protocol.TypeNoiseMsg)
	}
	replyCipher, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatalf("decode reply data: %v", err)
	}
	replyPlain, err := initRecv.Decrypt(replyCipher)
	if err != nil {
		t.Fatalf("phone decrypt reply: %v", err)
	}
	var replyEnv protocol.Envelope
	if err := json.Unmarshal(replyPlain, &replyEnv); err != nil {
		t.Fatalf("decode reply envelope: %v", err)
	}
	if replyEnv.Type != protocol.TypeConversations {
		t.Fatalf("reply Type = %q, want %q (payload=%s)",
			replyEnv.Type, protocol.TypeConversations, string(replyEnv.Payload))
	}
	if replyEnv.InReplyTo == nil || *replyEnv.InReplyTo != reqID {
		t.Errorf("InReplyTo = %v, want pointer to %d", replyEnv.InReplyTo, reqID)
	}
	var convsPayload protocol.ConversationsPayload
	if err := json.Unmarshal(replyEnv.Payload, &convsPayload); err != nil {
		t.Fatalf("decode conversations payload: %v", err)
	}
	if got, want := len(convsPayload.Conversations), 1; got != want {
		t.Fatalf("conversations rows: got %d, want %d (payload=%s)",
			got, want, string(replyEnv.Payload))
	}
	if got := convsPayload.Conversations[0].ID; got != knownConvID {
		t.Errorf("conversations[0].ID = %q, want %q", got, knownConvID)
	}
}

// --- relay binary resolution ------------------------------------------------

var (
	relayBinOnce sync.Once
	relayBinPath string
	relayBinErr  error
	relayBinSkip string
)

// ensureRelayBuilt resolves a pyrycode-relay binary once per test process:
// PYRY_LIVERELAY_BIN (prebuilt path) → build from the sibling repo (PYRY_RELAY_REPO,
// else ../pyrycode-relay relative to this repo root) via `go build`. If neither
// the binary nor a buildable sibling is present, the test SKIPS with a named
// diagnostic — skip-loud keeps `make preship` green on a machine without the
// sibling (the e2e_realclaude auth-gated-skip posture). A present-but-unbuildable
// sibling is a real failure, not a skip.
func ensureRelayBuilt(t *testing.T) string {
	t.Helper()
	relayBinOnce.Do(func() {
		if env := os.Getenv("PYRY_LIVERELAY_BIN"); env != "" {
			relayBinPath = env
			return
		}
		repo := os.Getenv("PYRY_RELAY_REPO")
		if repo == "" {
			repo = defaultSiblingRelayRepo()
		}
		if repo == "" || !dirExists(filepath.Join(repo, "cmd", "pyrycode-relay")) {
			relayBinSkip = fmt.Sprintf("liverelay: no pyrycode-relay binary and no buildable "+
				"sibling repo. Set PYRY_LIVERELAY_BIN=<path/to/pyrycode-relay> to a prebuilt "+
				"binary, or PYRY_RELAY_REPO=<path> (or place the checkout at %q). Get it with: "+
				"git clone https://github.com/pyrycode/pyrycode-relay",
				defaultSiblingRelayRepo())
			return
		}
		dir, err := os.MkdirTemp("", "pyrycode-relay-*")
		if err != nil {
			relayBinErr = err
			return
		}
		out := filepath.Join(dir, "pyrycode-relay")
		cmd := exec.Command("go", "build", "-o", out, "./cmd/pyrycode-relay")
		cmd.Dir = repo
		cmd.Env = os.Environ() // sibling has its own go.mod; real HOME keeps the module cache warm
		if b, err := cmd.CombinedOutput(); err != nil {
			relayBinErr = fmt.Errorf("go build pyrycode-relay in %s: %w\n%s", repo, err, b)
			return
		}
		relayBinPath = out
	})
	if relayBinSkip != "" {
		t.Skip(relayBinSkip)
	}
	if relayBinErr != nil {
		t.Fatalf("liverelay: %v", relayBinErr)
	}
	return relayBinPath
}

// defaultSiblingRelayRepo derives ../pyrycode-relay from this source file's
// compile-time location (<repo>/internal/e2e/liverelay/liverelay_test.go).
func defaultSiblingRelayRepo() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	return filepath.Clean(filepath.Join(repoRoot, "..", "pyrycode-relay"))
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// --- relay process ----------------------------------------------------------

// relayProc owns one spawned real pyrycode-relay.
type relayProc struct {
	baseWS   string // ws://127.0.0.1:<port>  — the bare base URL both peers append to
	baseHTTP string // http://127.0.0.1:<port> — for /healthz
	cmd      *exec.Cmd
	doneCh   chan struct{}
	stderr   *lockedBuffer
}

// startRelay spawns the relay bound to LOOPBACK ONLY on a free port and blocks
// until /healthz returns 200. Torn down SIGTERM→grace→SIGKILL in t.Cleanup.
func startRelay(t *testing.T, relayBin string) *relayProc {
	t.Helper()
	// Bind 127.0.0.1 explicitly — never :port / 0.0.0.0. The ephemeral
	// plaintext relay must be unreachable off-box; this is load-bearing for the
	// security review (spec § Stage 1). Free-port-then-close leaves a
	// sub-millisecond TOCTOU window, acceptable for a localhost test.
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	stderr := &lockedBuffer{}
	// --metrics-listen= disables the default 127.0.0.1:9090 metrics bind, which
	// would otherwise risk a port conflict / CheckListenerPorts refusal.
	cmd := exec.Command(relayBin, "--insecure-listen", addr, "--metrics-listen=")
	// Clean the production markers so the relay's boot guards
	// (CheckInsecureListenInProduction, CheckSingleInstance) can never fire from
	// a stray operator env var — this test always runs hermetic + insecure.
	cmd.Env = cleanRelayEnv()
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("liverelay: relay start: %v", err)
	}
	doneCh := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(doneCh)
	}()

	rp := &relayProc{
		baseWS:   "ws://" + addr,
		baseHTTP: "http://" + addr,
		cmd:      cmd,
		doneCh:   doneCh,
		stderr:   stderr,
	}
	t.Cleanup(func() { rp.stop(t) })

	if err := rp.waitHealthz(t, 0, 5*time.Second); err != nil {
		t.Fatalf("liverelay: relay never healthy: %v\nstderr:\n%s", err, stderr.String())
	}
	return rp
}

// waitBinaryRegistered blocks until /healthz reports connected_binaries >= 1,
// i.e. the daemon's relay client has bound the server-id. This replaces
// fakerelay's WaitBinary(serverID) test hook, which the real relay lacks.
//
// A dial-retry loop (the spec's suggestion) does NOT work here: the real relay
// ACCEPTS the /v1/client upgrade first and only then closes with WS code 4404
// when no binary is bound, so fakephone.Dial succeeds regardless of
// registration. /healthz is the relay's own registration signal, is not
// rate-limited (unlike /v1/*), and unambiguously names this one hermetic
// daemon's connection. On a deadline miss the test fails with the relay stderr.
func (rp *relayProc) waitBinaryRegistered(t *testing.T, timeout time.Duration) {
	t.Helper()
	if err := rp.waitHealthz(t, 1, timeout); err != nil {
		t.Fatalf("liverelay: binary never registered with relay: %v\nstderr:\n%s",
			err, rp.stderr.String())
	}
}

// waitHealthz polls /healthz until it returns 200 with connected_binaries >=
// wantBinaries, the relay exits, or the deadline elapses.
func (rp *relayProc) waitHealthz(t *testing.T, wantBinaries int, timeout time.Duration) error {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-rp.doneCh:
			return fmt.Errorf("relay exited before ready")
		default:
		}
		binaries, err := healthzBinaries(client, rp.baseHTTP)
		if err == nil && binaries >= wantBinaries {
			return nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr != nil {
		return fmt.Errorf("within %s: last /healthz error: %w", timeout, lastErr)
	}
	return fmt.Errorf("within %s: connected_binaries never reached %d", timeout, wantBinaries)
}

// healthzBinaries GETs /healthz and returns the connected_binaries count.
func healthzBinaries(client *http.Client, baseHTTP string) (int, error) {
	resp, err := client.Get(baseHTTP + "/healthz")
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("/healthz status %d", resp.StatusCode)
	}
	var body struct {
		ConnectedBinaries int `json:"connected_binaries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, err
	}
	return body.ConnectedBinaries, nil
}

func (rp *relayProc) stop(t *testing.T) {
	t.Helper()
	if rp.cmd == nil || rp.cmd.Process == nil {
		return
	}
	_ = rp.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-rp.doneCh:
	case <-time.After(3 * time.Second):
		_ = rp.cmd.Process.Signal(syscall.SIGKILL)
		select {
		case <-rp.doneCh:
		case <-time.After(time.Second):
			t.Logf("liverelay: relay pid=%d did not exit after SIGKILL", rp.cmd.Process.Pid)
		}
	}
}

// freePort binds 127.0.0.1:0, reads the assigned port, and closes the listener.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("liverelay: reserve free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// cleanRelayEnv returns the current environment minus the production markers
// that would trip the relay's boot guards, so an insecure loopback start always
// succeeds regardless of the operator's shell.
func cleanRelayEnv() []string {
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		if strings.HasPrefix(kv, "FLY_APP_NAME=") || strings.HasPrefix(kv, "PYRYCODE_RELAY_PRODUCTION=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// --- daemon process ---------------------------------------------------------

// daemonProc owns one spawned real pyry daemon.
type daemonProc struct {
	socketPath string
	cmd        *exec.Cmd
	doneCh     chan struct{}
	stderr     *lockedBuffer
}

// spawnDaemon forks real pyry against the isolated HOME with the sleep-claude
// stand-in child, the control socket under a short /tmp dir (the #860 sun_path
// fix), and the relay wired to relayURL. Blocks until the control socket is
// dialable. relayURL is the BARE base URL — resolveDialURL appends /v1/server.
func spawnDaemon(t *testing.T, home, relayURL string) *daemonProc {
	t.Helper()
	bin := ensurePyryBuilt(t)
	socket := shortSocketPath(t)
	claudeBin := writeSleepClaude(t, home)
	stderr := &lockedBuffer{}

	args := []string{
		"-pyry-socket=" + socket,
		"-pyry-name=test",
		"-pyry-claude=" + claudeBin,
		"-pyry-idle-timeout=0",
		// The serve path confines the workdir to $HOME (#670); default it to HOME
		// so the daemon doesn't fail-fast on the confinement check.
		"-pyry-workdir=" + home,
		"-pyry-relay=" + relayURL,
	}
	cmd := exec.Command(bin, args...)
	// childEnv pins HOME=home; PYRY_ALLOW_INSECURE_RELAY=1 lets resolveDialURL
	// accept the ws:// (plaintext, loopback) scheme.
	cmd.Env = append(childEnv(home), "PYRY_ALLOW_INSECURE_RELAY=1")
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("liverelay: pyry start: %v", err)
	}
	doneCh := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(doneCh)
	}()

	d := &daemonProc{socketPath: socket, cmd: cmd, doneCh: doneCh, stderr: stderr}
	t.Cleanup(func() { d.stop(t) })
	if err := d.waitForReady(10 * time.Second); err != nil {
		t.Fatalf("liverelay: daemon not ready: %v\nstderr:\n%s", err, stderr.String())
	}
	return d
}

// waitForReady polls the control socket until Dial succeeds or the daemon exits.
func (d *daemonProc) waitForReady(timeout time.Duration) error {
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

func (d *daemonProc) stop(t *testing.T) {
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
		case <-time.After(time.Second):
			t.Logf("liverelay: daemon pid=%d did not exit after SIGKILL", d.cmd.Process.Pid)
		}
	}
	_ = os.Remove(d.socketPath)
}

// childEnv returns the parent env with HOME replaced and PYRY_NAME stripped so
// the operator's shell alias can't leak into a test daemon. Transcribed from
// internal/e2e/harness.go.
func childEnv(home string) []string {
	src := os.Environ()
	out := make([]string, 0, len(src)+1)
	for _, kv := range src {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "PYRY_NAME=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HOME="+home)
}

// shortSocketPath returns a control-socket path short enough to stay under
// macOS's 104-byte sun_path limit regardless of the (long) HOME. Transcribed
// from internal/e2e/harness.go (#860).
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pyry-sock-*")
	if err != nil {
		t.Fatalf("liverelay: MkdirTemp socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "pyry.sock")
}

// sleepClaudeScript is a claude stand-in that ignores all args and exec()s
// sleep, so the daemon's appended --session-id can't crash-loop a bare sleep
// child. Transcribed from internal/e2e/harness.go.
const sleepClaudeScript = `#!/bin/sh
exec sleep 99999
`

func writeSleepClaude(t *testing.T, home string) string {
	t.Helper()
	path := filepath.Join(home, "sleep-claude.sh")
	if err := os.WriteFile(path, []byte(sleepClaudeScript), 0o755); err != nil {
		t.Fatalf("liverelay: write sleep-claude script: %v", err)
	}
	return path
}

// --- pyry helpers -----------------------------------------------------------

var (
	pyryBinOnce sync.Once
	pyryBinPath string
	pyryBinErr  error
)

// ensurePyryBuilt builds pyry once per test process. PYRY_E2E_BIN short-circuits
// to a pre-built binary. Duplicates internal/e2e/harness.go deliberately —
// disjoint build tags block reuse.
func ensurePyryBuilt(t *testing.T) string {
	t.Helper()
	pyryBinOnce.Do(func() {
		if env := os.Getenv("PYRY_E2E_BIN"); env != "" {
			pyryBinPath = env
			return
		}
		dir, err := os.MkdirTemp("", "pyry-liverelay-*")
		if err != nil {
			pyryBinErr = err
			return
		}
		pyryBinPath = filepath.Join(dir, "pyry")
		cmd := exec.Command("go", "build", "-o", pyryBinPath, "github.com/pyrycode/pyrycode/cmd/pyry")
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			pyryBinErr = fmt.Errorf("go build pyry: %w\n%s", err, out)
		}
	})
	if pyryBinErr != nil {
		t.Fatalf("liverelay: %v", pyryBinErr)
	}
	return pyryBinPath
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

// --- Noise wire helpers (transcribed from internal/e2e) ---------------------

// driveHandshake runs a paired-device Noise_IK handshake from the phone side and
// returns the initiator's CipherStates (initSend encrypts phone→binary,
// initRecv decrypts binary→phone).
func driveHandshake(t *testing.T, phone *fakephone.Client, pubKey []byte, token string) (*noise.CipherState, *noise.CipherState) {
	t.Helper()
	initPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("phone keygen: %v", err)
	}
	initiator, err := noise.NewInitiator(initPriv.Bytes(), pubKey)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHello(t, token))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	sendNoiseInit(t, phone, initMsg)

	inner := readInnerFrame(t, phone, 3*time.Second)
	if inner.Type != protocol.TypeNoiseResp {
		t.Fatalf("handshake: got inner type %q, want %q", inner.Type, protocol.TypeNoiseResp)
	}
	respRaw, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatalf("decode noise_resp data: %v", err)
	}
	_, initSend, initRecv, err := initiator.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("initiator.ReadResp: %v", err)
	}
	return initSend, initRecv
}

// buildHello marshals the phone's early-data hello envelope (no capabilities —
// list_conversations needs none).
func buildHello(t *testing.T, token string) []byte {
	t.Helper()
	payload, err := json.Marshal(protocol.HelloClientPayload{
		Role:             "client",
		DeviceName:       "liverelay-e2e-phone",
		ClientVersion:    "0.0.1-test",
		ProtocolVersions: []string{"v2"},
		Token:            token,
	})
	if err != nil {
		t.Fatalf("marshal hello payload: %v", err)
	}
	envBytes, err := json.Marshal(protocol.Envelope{
		ID:      1,
		Type:    protocol.TypeHello,
		TS:      time.Now().UTC(),
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("marshal hello envelope: %v", err)
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

// --- misc -------------------------------------------------------------------

// lockedBuffer is a mutex-guarded byte buffer so the test goroutine can read a
// child process's captured stderr while os/exec's copy goroutine still writes.
// Transcribed from internal/e2e/realclaude.
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
