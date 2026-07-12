//go:build e2e

package e2e

import (
	"context"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
)

// TestRelay_4409 asserts that a WS close code 4409 from the relay
// causes the daemon to log the conflict and exit cleanly (exit code 0
// via ctx cancel; no reconnect loop). Does not go through the harness's
// readiness gate because the daemon may exit before its control socket
// is dialable — startup and shutdown both happen in ~1ms.
//
// This exercises the binary WebSocket close-code path, not the phone
// handshake, so it runs over /v2/server (the surviving route) with the
// v2 default; fakerelay routes /v2/server through the same handleBinary
// and its 4409 hook keys on serverID, so the behavior is identical.
func TestRelay_4409(t *testing.T) {
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	fr.RejectNextBinaryWith4409()

	home := shortHome(t)
	_, cmd, _, stderr, doneCh := spawnWith(t, home, spawnOpts{
		extraEnv: []string{"PYRY_ALLOW_INSECURE_RELAY=1"},
		extraFlags: []string{
			"-pyry-relay=" + fr.URL() + "/v2/server",
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
//
// Like TestRelay_4409 this exercises the binary WebSocket close-code path
// (not the phone handshake), so it runs over /v2/server with the v2
// default; fakerelay's ForceCloseBinary hook keys on serverID.
func TestRelay_1011(t *testing.T) {
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	home := shortHome(t)
	h := StartInWithEnv(t,
		home,
		[]string{"PYRY_ALLOW_INSECURE_RELAY=1"},
		"-pyry-relay="+fr.URL()+"/v2/server",
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
