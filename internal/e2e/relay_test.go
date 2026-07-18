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

// TestRelay_4409 asserts that a TRANSIENT WS close code 4409 from the
// relay is survived: the daemon logs the below-threshold retry, backs
// off (~1s first step), reconnects, and claims the slot on the next
// accept. This is the #1072 incident regression — a daemon reconnecting
// after a drop can race the relay's dead-conn detection and draw a
// one-shot 4409 from its own stale claim, which must NOT shut it down.
// Only a PERSISTENT conflict (serverIDConflictThreshold consecutive
// closes with the backoff ladder between — a genuine duplicate binary)
// is fatal; that path is pinned at the transport layer
// (TestFatalCloseThreshold_PersistentConflictGoesFatal) where the
// backoff cadence is test-compressed, and the daemon's shutdown wiring
// on the fatal classification is unchanged by #1072.
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
	h := StartInWithEnv(t,
		home,
		[]string{"PYRY_ALLOW_INSECURE_RELAY=1"},
		"-pyry-relay="+fr.URL()+"/v2/server",
	)

	serverID := readPersistedServerID(t, home)

	// The first connect draws the one-shot 4409; the transport retries on
	// the ~1s first backoff step and the second connect must register.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if !fr.WaitBinary(ctx, serverID) {
		t.Fatalf("binary connection not registered after transient 4409\nstderr:\n%s",
			h.Stderr.String())
	}

	// Daemon must still be running — the transient conflict is not fatal.
	select {
	case <-h.Done():
		t.Fatalf("daemon exited after transient 4409 (exit=%d)\nstderr:\n%s",
			h.ExitCode(), h.Stderr.String())
	default:
	}
	if err := syscall.Kill(h.PID, 0); err != nil {
		t.Fatalf("daemon pid %d not reachable: %v", h.PID, err)
	}

	// The below-threshold retry must be observable in the daemon log, and
	// the fatal conflict-shutdown path must NOT have fired.
	stderr := h.Stderr.String()
	if !strings.Contains(stderr, "fatal close code; retrying") {
		t.Errorf("stderr missing transient-4409 retry log line:\n%s", stderr)
	}
	if strings.Contains(stderr, "shutting down daemon") {
		t.Errorf("stderr shows the fatal conflict shutdown on a transient 4409:\n%s", stderr)
	}

	// Control socket still responsive.
	r := h.Run(t, "status")
	if r.ExitCode != 0 {
		t.Errorf("status after transient 4409: exit=%d stderr=%s",
			r.ExitCode, r.Stderr)
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
