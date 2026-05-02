//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
)

// TestE2E_IdleEviction_EvictsBootstrap starts pyry with a short idle timeout
// and asserts the bootstrap session is observed in stateEvicted on disk and
// that `pyry status` reports a non-running phase.
func TestE2E_IdleEviction_EvictsBootstrap(t *testing.T) {
	home, regPath := newRegistryHome(t)
	h := StartIn(t, home, "-pyry-idle-timeout=1s")

	// Poll the registry file for the bootstrap entry's lifecycle_state ==
	// "evicted". 5s deadline accommodates the 1s timer plus the
	// runActive→transitionTo→saveLocked tail.
	waitForBootstrapState(t, regPath, "evicted", 5*time.Second)

	// Cross-check via the control plane: the supervisor for an evicted
	// session is in PhaseStopped, so status should NOT report
	// "Phase:         running". Asserting negation keeps the test
	// decoupled from which non-running phase shows up.
	r := h.Run(t, "status")
	if r.ExitCode != 0 {
		t.Fatalf("pyry status exit=%d stderr=%s", r.ExitCode, r.Stderr)
	}
	if bytes.Contains(r.Stdout, []byte("Phase:         running")) {
		t.Errorf("status reports Phase: running for an evicted session\nstdout:\n%s", r.Stdout)
	}
}

// TestE2E_IdleEviction_LazyRespawn waits for the bootstrap to evict, issues
// a raw VerbAttach over the control socket to trigger lazy respawn, and
// asserts the registry returns to active and the supervisor reaches
// Phase: running. The conn is held open so the freshly-respawned session
// stays attached for the duration of the assertions.
func TestE2E_IdleEviction_LazyRespawn(t *testing.T) {
	home, regPath := newRegistryHome(t)
	h := StartIn(t, home, "-pyry-idle-timeout=1s")

	// Phase A — wait for the bootstrap to evict.
	waitForBootstrapState(t, regPath, "evicted", 5*time.Second)

	// Phase B — issue a raw VerbAttach over the control socket. handleAttach
	// calls Session.Activate before binding the bridge; on success we receive
	// Response{OK: true}. The conn is held open for the rest of the test
	// (defer Close) so the session stays attached and the next idle eviction
	// is deferred while we assert.
	conn, err := net.Dial("unix", h.SocketPath)
	if err != nil {
		t.Fatalf("dial control socket: %v", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(control.Request{
		Verb:   control.VerbAttach,
		Attach: &control.AttachPayload{Cols: 80, Rows: 24},
	}); err != nil {
		t.Fatalf("send attach: %v", err)
	}
	var resp control.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode attach ack: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("attach error: %s", resp.Error)
	}
	if !resp.OK {
		t.Fatalf("attach ack missing OK: %+v", resp)
	}

	// Phase C — assert respawn. saveLocked omits lifecycle_state for active
	// sessions (omitempty), so the helper accepts either an empty/missing
	// field or the literal "active".
	waitForBootstrapState(t, regPath, "active", 5*time.Second)

	deadline := time.Now().Add(5 * time.Second)
	var lastStdout []byte
	for time.Now().Before(deadline) {
		r := h.Run(t, "status")
		lastStdout = r.Stdout
		if r.ExitCode == 0 && bytes.Contains(r.Stdout, []byte("Phase:         running")) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("supervisor never reached Phase: running after lazy respawn\nlast status stdout:\n%s",
		lastStdout)
}

// waitForBootstrapState polls regPath until the bootstrap entry's
// lifecycle_state matches want ("evicted" or "active"). "active" matches
// either an empty/missing field (omitempty default for stateActive) or the
// literal string "active" — the test stays decoupled from the omitempty
// toggle so a future change to write the field explicitly doesn't flake it.
func waitForBootstrapState(t *testing.T, regPath, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		reg := readRegistry(t, regPath)
		for _, e := range reg.Sessions {
			if !e.Bootstrap {
				continue
			}
			got := e.LifecycleState
			if want == "active" && (got == "" || got == "active") {
				return
			}
			if want == "evicted" && got == "evicted" {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("bootstrap lifecycle_state never became %q within %s\nfile:\n%s",
		want, timeout, mustReadFile(t, regPath))
}
