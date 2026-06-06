//go:build e2e

package e2e

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
)

// shortHome allocates a short-pathed temp dir for the daemon's $HOME.
// macOS caps Unix socket paths at 104 bytes, and t.TempDir() under a
// long test name overruns that. The dispatcher's worktree path is
// already long; keeping the test-side suffix tight avoids the bind
// failure that the longer TestName-based path triggers.
func shortHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "p-301-*")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

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

func relayTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestRelay_Hello asserts that a pyry daemon spawned against a ws://
// fakerelay completes the binary↔relay handshake: the binary upgrades
// on /v1/server and sends a "hello" envelope with role=server and the
// persisted ServerID. The relay's hello_ack reply path is exercised
// in the unit tests; e2e only proves the wire arrives.
func TestRelay_Hello(t *testing.T) {
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	home := shortHome(t)
	h := StartInWithEnv(t,
		home,
		[]string{"PYRY_ALLOW_INSECURE_RELAY=1"},
		"-pyry-relay="+fr.URL()+"/v1/server",
	)

	serverID := readPersistedServerID(t, home)

	deadline := time.Now().Add(5 * time.Second)
	var hello any
	for time.Now().Before(deadline) {
		if env, ok := fr.LastBinaryHello(serverID); ok {
			hello = env
			if env.Type == "hello" {
				payloadStr := string(env.Payload)
				if !strings.Contains(payloadStr, `"role":"server"`) {
					t.Errorf("hello payload missing role=server: %s", payloadStr)
				}
				if !strings.Contains(payloadStr, `"server_id":"`+serverID+`"`) {
					t.Errorf("hello payload missing server_id=%s: %s", serverID, payloadStr)
				}
				h.Stop(t)
				return
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.Stop(t)
	t.Fatalf("relay never observed expected binary hello for server-id=%s (got %+v)",
		serverID, hello)
}

// TestRelay_4409 asserts that a WS close code 4409 from the relay
// causes the daemon to log the conflict and exit cleanly (exit code 0
// via ctx cancel; no reconnect loop). Does not go through the harness's
// readiness gate because the daemon may exit before its control socket
// is dialable — startup and shutdown both happen in ~1ms.
func TestRelay_4409(t *testing.T) {
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	fr.RejectNextBinaryWith4409()

	home := shortHome(t)
	_, cmd, _, stderr, doneCh := spawnWith(t, home, spawnOpts{
		extraEnv: []string{"PYRY_ALLOW_INSECURE_RELAY=1"},
		extraFlags: []string{
			"-pyry-relay=" + fr.URL() + "/v1/server",
		},
	})
	t.Cleanup(func() { killSpawned(t, cmd, doneCh) })

	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("daemon did not exit within 5s after 4409\nstderr:\n%s",
			stderr.String())
	}

	if code := cmd.ProcessState.ExitCode(); code != 0 {
		t.Errorf("exit code = %d, want 0\nstderr:\n%s",
			code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "server-id conflict") {
		t.Errorf("stderr missing conflict log line:\n%s", stderr.String())
	}
}

// TestRelay_1011 asserts that a non-fatal WS close (StatusInternalError,
// 1011) is absorbed by the transport's reconnect loop: the daemon stays
// alive and the control socket is still responsive.
func TestRelay_1011(t *testing.T) {
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	home := shortHome(t)
	h := StartInWithEnv(t,
		home,
		[]string{"PYRY_ALLOW_INSECURE_RELAY=1"},
		"-pyry-relay="+fr.URL()+"/v1/server",
	)

	serverID := readPersistedServerID(t, home)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if !fr.WaitBinary(ctx, serverID) {
		t.Fatal("binary connection not registered within 4s")
	}

	if !fr.ForceCloseBinary(serverID) {
		t.Fatal("ForceCloseBinary returned false; daemon never bound a binary conn")
	}

	// Daemon must still be running after the non-fatal close. The
	// transport's reconnect cadence is ~1s base + jitter; give it 2s
	// then assert no exit.
	select {
	case <-h.Done():
		t.Fatalf("daemon exited after non-fatal close (exit=%d)", h.ExitCode())
	case <-time.After(2 * time.Second):
	}

	// PID still reachable.
	if err := syscall.Kill(h.PID, 0); err != nil {
		t.Fatalf("daemon pid %d not reachable: %v", h.PID, err)
	}

	// Control socket still responsive.
	r := h.Run(t, "status")
	if r.ExitCode != 0 {
		t.Errorf("status after non-fatal close: exit=%d stderr=%s",
			r.ExitCode, r.Stderr)
	}
}
