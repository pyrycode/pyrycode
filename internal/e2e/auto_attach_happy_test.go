//go:build e2e

package e2e

import (
	"bytes"
	"testing"
	"time"
)

// TestE2E_ForegroundAutoAttach_AttachesWhenDaemonHasSession proves
// that `pyry --session-id <uuid> …` invoked while a daemon hosts that
// UUID dispatches to control.AttachStdio (no claude spawn). Asserts:
//
//  1. Bytes written to the foreground pyry's stdin pipe round-trip
//     through control socket → bridge → supervisor PTY → echo helper
//     and back through the foreground pyry's stdout pipe.
//  2. The foreground pyry process has zero direct children
//     (process-tree inspection via pgrep -P). Auto-attach is a
//     stdio-bridge — no exec, no PTY, no goroutine that forks.
//
// Skips when os.Pipe() is unavailable or pgrep is missing (matches
// the harness's existing skip discipline).
func TestE2E_ForegroundAutoAttach_AttachesWhenDaemonHasSession(t *testing.T) {
	c := startForegroundAutoAttach(t, "auto-attach-happy")

	payload := []byte("pyry-auto-attach-" + tinyNonce() + "\n")
	if _, err := c.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	seen, err := c.ReadUntil(payload, 5*time.Second)
	if err != nil {
		t.Fatalf("did not observe payload back: %v\nstderr:\n%s",
			err, c.Stderr.String())
	}
	if !bytes.Contains(seen, payload) {
		t.Fatalf("ReadUntil returned without payload: %q", seen)
	}

	// Process-tree assertion runs AFTER the round-trip so we know
	// the foreground pyry is steady-state in AttachStdio's I/O loop.
	// If runSupervisor's supervised-spawn path had fired instead of
	// auto-attach, a claude child would already be in the tree.
	children, err := pgrepChildren(c.Pid)
	if err != nil {
		t.Skipf("e2e: pgrep unavailable: %v", err)
	}
	if len(children) > 0 {
		t.Fatalf("foreground pyry pid=%d has children %v; expected zero (auto-attach should not spawn)\nstderr:\n%s",
			c.Pid, children, c.Stderr.String())
	}
}
