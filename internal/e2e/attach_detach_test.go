//go:build e2e

package e2e

import (
	"bytes"
	"testing"
	"time"
)

// TestE2E_Attach_DetachesCleanly writes the documented Ctrl-B d detach
// sequence into a live attach session and asserts the triple invariant:
//   1. the attach client exits 0 within a generous deadline,
//   2. the daemon survives (pyry status against the same socket
//      returns exit 0),
//   3. the supervised child is still in Phase: running.
//
// The PTY availability skip lives in StartAttach; this test does not
// re-probe.
func TestE2E_Attach_DetachesCleanly(t *testing.T) {
	a := StartAttach(t, "")

	// Drain the master in the background. Slave writes block when the
	// master buffer fills, so without a continuous reader the attach
	// client's "pyry: detached." stderr write (and tail PTY traffic)
	// can stall and the process never exits. Goroutine ends when
	// teardown closes Master and Read returns an error.
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := a.Master.Read(buf); err != nil {
				return
			}
		}
	}()

	// Detach sequence: Ctrl-B (0x02) then 'd' (0x64).
	if _, err := a.Master.Write([]byte{0x02, 0x64}); err != nil {
		t.Fatalf("write detach sequence: %v", err)
	}

	if exit := a.WaitDetach(t, 5*time.Second); exit != 0 {
		t.Fatalf("attach exit=%d, want 0", exit)
	}

	r := a.Run(t, "status")
	if r.ExitCode != 0 {
		t.Fatalf("daemon dead after detach: pyry status exit=%d stderr=%s",
			r.ExitCode, r.Stderr)
	}
	if !bytes.Contains(r.Stdout, []byte("Phase:         running")) {
		t.Fatalf("supervised child not running after detach\nstdout:\n%s",
			r.Stdout)
	}
}
