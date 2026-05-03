//go:build e2e

package e2e

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// startupMarkerRe captures the PID emitted by TestHelperProcess's echo
// mode on every spawn. The supervisor re-execs the helper on each
// restart, producing a fresh PID per iteration; observing two distinct
// PIDs on the attach PTY is the test's proof of respawn (AC#3).
var startupMarkerRe = regexp.MustCompile(`PYRY_E2E_STARTED pid=(\d+)\n`)

// TestE2E_Attach_SurvivesClaudeRestart asserts the load-bearing
// invariant of the supervisor's restart loop: an attached `pyry attach`
// client remains usable across a supervised claude restart. The
// supervisor's bridge re-binds to the new PTY, the attach client stays
// alive, and bytes flow again.
func TestE2E_Attach_SurvivesClaudeRestart(t *testing.T) {
	a := StartAttach(t, "")

	// The on-startup marker emitted by helper1 races with the attach
	// client connecting — when no client is attached yet, the bridge
	// silently discards the bytes. Probe instead to capture pid1
	// after the attach is wired.
	if _, err := a.Master.Write([]byte("__PID__\n")); err != nil {
		t.Fatalf("write pid probe: %v", err)
	}
	pid1 := readStartupMarker(t, a.Master, 5*time.Second)

	payload1 := []byte("pre-restart-" + tinyNonce() + "\n")
	if _, err := a.Master.Write(payload1); err != nil {
		t.Fatalf("write payload1: %v", err)
	}
	if err := readUntilContains(a.Master, payload1, 5*time.Second); err != nil {
		t.Fatalf("pre-restart round-trip: %v", err)
	}

	if _, err := a.Master.Write([]byte("__EXIT__\n")); err != nil {
		t.Fatalf("write exit trigger: %v", err)
	}

	// Generous deadline: 500ms initial backoff + spawn + first stdout
	// flush. ~10x headroom over observed steady-state.
	pid2 := readStartupMarker(t, a.Master, 5*time.Second)
	if pid2 == pid1 {
		t.Fatalf("respawn produced same pid=%d; supervisor did not restart child", pid1)
	}

	payload2 := []byte("post-restart-" + tinyNonce() + "\n")
	if _, err := a.Master.Write(payload2); err != nil {
		t.Fatalf("write payload2: %v", err)
	}
	if err := readUntilContains(a.Master, payload2, 5*time.Second); err != nil {
		t.Fatalf("post-restart round-trip: %v", err)
	}

	// AC#4: the attach client must not have exited as a side effect of
	// its supervised child crashing and being respawned.
	select {
	case <-a.attachDone:
		exit := -1
		if a.attachCmd.ProcessState != nil {
			exit = a.attachCmd.ProcessState.ExitCode()
		}
		t.Fatalf("attach client exited unexpectedly (exit=%d) after child respawn", exit)
	default:
	}
}

// readStartupMarker reads from r until startupMarkerRe matches in the
// accumulated buffer or timeout elapses, returning the captured PID.
//
// PTY master fds on darwin reject SetReadDeadline, so the timeout is
// enforced caller-side — same shape as readUntilContains. The reader
// goroutine left running on timeout is drained by the harness's
// teardown closing Master.
func readStartupMarker(t *testing.T, r *os.File, total time.Duration) int {
	t.Helper()
	type readResult struct {
		buf []byte
		err error
	}
	ch := make(chan readResult, 1)
	var seen []byte

	read := func() {
		b := make([]byte, 4096)
		n, err := r.Read(b)
		ch <- readResult{buf: b[:n], err: err}
	}

	deadline := time.Now().Add(total)
	go read()
	for {
		select {
		case res := <-ch:
			if len(res.buf) > 0 {
				seen = append(seen, res.buf...)
				if m := startupMarkerRe.FindSubmatch(seen); m != nil {
					pid, perr := strconv.Atoi(string(m[1]))
					if perr != nil {
						t.Fatalf("parse pid %q: %v", m[1], perr)
					}
					return pid
				}
			}
			if res.err != nil {
				t.Fatalf("read startup marker: %v (seen %q)", res.err, seen)
			}
			go read()
		case <-time.After(time.Until(deadline)):
			t.Fatalf("startup marker not seen within %s; seen %d bytes: %q",
				total, len(seen), seen)
			return 0 // unreachable
		}
	}
}
