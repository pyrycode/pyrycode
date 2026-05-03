//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
)

// sleepClaudeScript is a tiny shell-script claude stand-in. Pool.Create
// appends `--session-id <uuid>` to the configured ClaudeArgs; both BSD and
// GNU sleep(1) reject that, so /bin/sleep can't drive multi-session tests.
// The script ignores all positional args and exec()s sleep instead. The
// bootstrap path also runs through this script (passes "99999" verbatim,
// which the script also ignores).
const sleepClaudeScript = `#!/bin/sh
exec sleep 99999
`

// writeSleepClaude writes the sleep-claude stand-in to home and returns
// its absolute path. See sleepClaudeScript for why the indirection.
func writeSleepClaude(t *testing.T, home string) string {
	t.Helper()
	path := filepath.Join(home, "sleep-claude.sh")
	if err := os.WriteFile(path, []byte(sleepClaudeScript), 0o755); err != nil {
		t.Fatalf("write sleep-claude script: %v", err)
	}
	return path
}

// waitForBootstrap polls regPath until the bootstrap entry appears (Pool
// writes it during init) and returns its ID. Tolerates the registry file
// not yet existing — Pool's first save races the readiness gate.
func waitForBootstrap(t *testing.T, regPath string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(regPath)
		if err == nil {
			var reg registryFile
			if json.Unmarshal(data, &reg) == nil {
				for _, e := range reg.Sessions {
					if e.Bootstrap {
						return e.ID
					}
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no bootstrap entry observed in registry within %s", timeout)
	return ""
}

// waitForSessionState polls regPath until the entry with the given id has
// lifecycle_state matching want ("evicted" or "active"). "active" matches
// either an empty/missing field (omitempty default for stateActive) or
// the literal string "active" — same convention as waitForBootstrapState.
func waitForSessionState(t *testing.T, regPath, id, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		reg := readRegistry(t, regPath)
		for _, e := range reg.Sessions {
			if e.ID != id {
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
	t.Fatalf("session %s lifecycle_state never became %q within %s\nfile:\n%s",
		id, want, timeout, mustReadFile(t, regPath))
}

// assertActive checks regPath right now for the given id and fails the
// test if its lifecycle_state is "evicted". Distinct from
// waitForSessionState(..., "active", ...) — that polls for the first
// observation; assertActive is a one-shot checkpoint for "X must be true
// at this exact moment".
func assertActive(t *testing.T, regPath, id string) {
	t.Helper()
	reg := readRegistry(t, regPath)
	for _, e := range reg.Sessions {
		if e.ID == id {
			if e.LifecycleState == "evicted" {
				t.Fatalf("expected session %s active, but lifecycle_state=%q", id, e.LifecycleState)
			}
			return
		}
	}
	t.Fatalf("session %s not present in registry\nfile:\n%s", id, mustReadFile(t, regPath))
}

// TestE2E_ActiveCap_EvictsLRU spawns pyry with -pyry-active-cap=2, mints
// three sessions via the sessions.new control verb, and asserts the LRU
// cap-evict path picks the right victim each time. Bootstrap is the
// first active session; α/β/γ are minted with 50ms gaps so lastActiveAt
// timestamps are distinguishable for pickLRUVictim.
func TestE2E_ActiveCap_EvictsLRU(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home,
		"-pyry-active-cap=2",
		"-pyry-claude="+claudeBin,
	)

	bootstrapID := waitForBootstrap(t, regPath, 5*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create α — count goes 1 → 2; no cap-evict.
	alpha, err := control.SessionsNew(ctx, h.SocketPath, "alpha")
	if err != nil {
		t.Fatalf("sessions.new alpha: %v", err)
	}

	// 50ms gap so lastActiveAt timestamps are distinguishable.
	time.Sleep(50 * time.Millisecond)

	// Create β — count would be 3; cap-evict LRU = bootstrap.
	beta, err := control.SessionsNew(ctx, h.SocketPath, "beta")
	if err != nil {
		t.Fatalf("sessions.new beta: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Create γ — count would be 3; cap-evict LRU = α.
	gamma, err := control.SessionsNew(ctx, h.SocketPath, "gamma")
	if err != nil {
		t.Fatalf("sessions.new gamma: %v", err)
	}

	waitForSessionState(t, regPath, bootstrapID, "evicted", 3*time.Second)
	waitForSessionState(t, regPath, alpha, "evicted", 3*time.Second)
	waitForSessionState(t, regPath, beta, "active", 3*time.Second)
	waitForSessionState(t, regPath, gamma, "active", 3*time.Second)
}

// TestE2E_ActiveCap_IdleInterleave exercises cap-evict and idle-evict
// against the same registry. With cap=2 and idle=2s, the interleave is:
//
//	T0:        bootstrap active.
//	T0+ε:      Create α — count 2; no cap-evict.
//	T0+1s:     Create β — count would be 3; cap-evict LRU = bootstrap.
//	T0+~2s:    α's idle timer (armed at T0+ε) fires; α evicts.
//	T0+~3s:    β's idle timer (armed at T0+1s) fires; β evicts.
//
// The test asserts the cap-evict (Phase 1) and the idle-evict of α while
// β still active (Phase 2), then lets β evict too as a final wedge check.
func TestE2E_ActiveCap_IdleInterleave(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home,
		"-pyry-active-cap=2",
		"-pyry-idle-timeout=2s",
		"-pyry-claude="+claudeBin,
	)

	bootstrapID := waitForBootstrap(t, regPath, 5*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	alpha, err := control.SessionsNew(ctx, h.SocketPath, "alpha")
	if err != nil {
		t.Fatalf("sessions.new alpha: %v", err)
	}

	time.Sleep(1 * time.Second)

	beta, err := control.SessionsNew(ctx, h.SocketPath, "beta")
	if err != nil {
		t.Fatalf("sessions.new beta: %v", err)
	}

	// Phase 1 — cap-evict observed. Bootstrap goes to evicted while α
	// and β stay active. Window: between β's activation (~T0+1s) and
	// α's idle fire (~T0+ε+2s) — ~1s.
	waitForSessionState(t, regPath, bootstrapID, "evicted", 2*time.Second)
	assertActive(t, regPath, alpha)
	assertActive(t, regPath, beta)

	// Phase 2 — idle-evict of α. β's idle fires ~1s later.
	waitForSessionState(t, regPath, alpha, "evicted", 3*time.Second)
	assertActive(t, regPath, beta)

	// Final cleanup — β idle-evicts too. Pins absence of state-machine wedge.
	waitForSessionState(t, regPath, beta, "evicted", 3*time.Second)
}
