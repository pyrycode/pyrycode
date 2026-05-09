//go:build e2e

// E2E regression tests pinning ADR 016: the bootstrap session ignores its
// persisted lifecycle_state on warm-start, while non-bootstrap sessions
// retain theirs. The class — "persisted state needs a driver to exit it on
// warm-start" (lessons.md § "v0.10.1 supervisor startup hangs under non-TTY
// stdin") — is locked into CI here so a future refactor that re-introduces
// parseLifecycleState for the bootstrap path fails on the user-visible
// signature (`Started at: 0001-01-01T00:00:00Z`, `Uptime: 2562047h47m16s`)
// instead of slipping through to a release.

package e2e

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
)

// TestE2E_BootstrapWarmStart_IgnoresEvictedOnDisk drives the v0.10.1
// regression class end-to-end: cold-start a daemon so it picks its own
// bootstrap UUID, stop cleanly, mutate the on-disk lifecycle_state to
// "evicted" (the minimum-fidelity stand-in for "idle eviction had a chance
// to fire"), then warm-start. Pre-fix Pool.New would carry the persisted
// "evicted" through, runEvicted would block forever waiting on an
// activateCh that nothing sends to, and pyry status would report the
// zero-time tell. Post-fix the load layer forces stateActive for the
// bootstrap, Supervisor.Run spawns claude, and Phase reaches running.
func TestE2E_BootstrapWarmStart_IgnoresEvictedOnDisk(t *testing.T) {
	home, regPath := newRegistryHome(t)

	// Phase A — cold start so the daemon picks the bootstrap UUID and
	// writes the registry itself. Mutating an entry the daemon authored
	// (rather than a hand-rolled one) is faithful to the "machine boots →
	// daemon starts → idle evicts → daemon restarts" trigger path.
	h1 := StartIn(t, home)
	bootstrapID := waitForBootstrap(t, regPath, 5*time.Second)
	h1.Stop(t)

	// Phase B — mutate the bootstrap entry's lifecycle_state to "evicted".
	// No concurrent writer (h1 has exited; h2 not yet started), so plain
	// read/edit/write is race-free; saveRegistryLocked's rename dance is
	// daemon-side fixture handling we don't need here.
	reg := readRegistry(t, regPath)
	mutated := false
	for i := range reg.Sessions {
		if reg.Sessions[i].ID == bootstrapID {
			reg.Sessions[i].LifecycleState = "evicted"
			mutated = true
			break
		}
	}
	if !mutated {
		t.Fatalf("bootstrap entry %s not found in registry to mutate\nfile:\n%s",
			bootstrapID, mustReadFile(t, regPath))
	}
	writeRegistry(t, regPath, reg)

	// Phase C — warm start against the mutated registry. Pre-fix this
	// hangs forever in runEvicted; post-fix the bootstrap loads as
	// stateActive and Supervisor.Run spawns /bin/sleep.
	h2 := StartIn(t, home)

	// Poll `pyry status` until Phase reaches running. 5s mirrors the
	// harness's readyDeadline; pre-fix the daemon parks indefinitely so
	// any reasonable deadline fires.
	deadline := time.Now().Add(5 * time.Second)
	var lastStdout []byte
	for time.Now().Before(deadline) {
		r := h2.Run(t, "status")
		lastStdout = r.Stdout
		if r.ExitCode == 0 && bytes.Contains(r.Stdout, []byte("Phase:         running")) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !bytes.Contains(lastStdout, []byte("Phase:         running")) {
		t.Fatalf("supervisor never reached Phase: running after warm-start with persisted evicted bootstrap\nlast status stdout:\n%s\nregistry on disk:\n%s",
			lastStdout, mustReadFile(t, regPath))
	}

	// AC: assert the v0.10.1 failure-mode signatures are absent. The
	// zero-time StartedAt and the math.MaxInt64 uptime sentinel are the
	// "Supervisor.Run was never called" tells documented in lessons.md
	// and ADR 016. buildStatus calls Round(time.Second) on Uptime, but
	// Duration.Round of math.MaxInt64 overflows and returns the input
	// unchanged — so the rendered sentinel is the full
	// `2562047h47m16.854775807s`. Match the prefix so a future change
	// that *does* round (e.g. by clamping pre-Round) still trips the
	// assertion on the recognisable `2562047h47m16…` magnitude.
	if bytes.Contains(lastStdout, []byte("Started at:    0001-01-01T00:00:00Z")) {
		t.Errorf("status reports zero-time StartedAt — Supervisor.Run was never called\nstdout:\n%s",
			lastStdout)
	}
	if bytes.Contains(lastStdout, []byte("Uptime:        2562047h47m16")) {
		t.Errorf("status reports max-int64 Uptime sentinel — supervisor stuck in starting\nstdout:\n%s",
			lastStdout)
	}

	// Cross-check the wire view: the bootstrap should be reported as
	// state=active. Use --json (not the table form) so the assertion
	// rides on the stable wire field rather than tabwriter alignment.
	r := h2.Run(t, "sessions", "list", "--json")
	if r.ExitCode != 0 {
		t.Fatalf("pyry sessions list --json exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	var payload struct {
		Sessions []control.SessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal(r.Stdout, &payload); err != nil {
		t.Fatalf("decode sessions list: %v\nstdout:\n%s", err, r.Stdout)
	}
	var bootstrap *control.SessionInfo
	for i := range payload.Sessions {
		if payload.Sessions[i].Bootstrap {
			bootstrap = &payload.Sessions[i]
			break
		}
	}
	if bootstrap == nil {
		t.Fatalf("no bootstrap session in `pyry sessions list --json`:\n%s", r.Stdout)
	}
	if bootstrap.State != "active" {
		t.Errorf("bootstrap state: got %q want %q\nstdout:\n%s",
			bootstrap.State, "active", r.Stdout)
	}
}

// TestE2E_BootstrapWarmStart_NonBootstrapEvictedPersists pins the carve-out
// boundary from ADR 016: the load-layer special-case is bootstrap-only.
// Non-bootstrap sessions correctly retain "evicted" on warm-load — lazy
// respawn drives the next attach. A future refactor that over-corrects the
// fix into "ignore evicted for *all* sessions" trips this test even though
// the bootstrap-evicted regression test still passes.
func TestE2E_BootstrapWarmStart_NonBootstrapEvictedPersists(t *testing.T) {
	home, regPath := newRegistryHome(t)

	const (
		bootstrapID    = "11111111-1111-4111-8111-111111111111"
		nonBootstrapID = "22222222-2222-4222-8222-222222222222"
	)

	now := time.Now().UTC().Truncate(time.Second)
	pre := registryFile{
		Version: 1,
		Sessions: []registryEntry{
			{
				ID:             bootstrapID,
				CreatedAt:      now,
				LastActiveAt:   now,
				Bootstrap:      true,
				LifecycleState: "active",
			},
			{
				ID:             nonBootstrapID,
				Label:          "evicted-one",
				CreatedAt:      now,
				LastActiveAt:   now,
				LifecycleState: "evicted",
			},
		},
	}
	writeRegistry(t, regPath, pre)

	h := StartIn(t, home)

	// Disk check (canonical): the warm-load reconciliation may rewrite
	// the registry on first start, so absorb that with a small polling
	// envelope. The non-bootstrap "evicted" must survive across that
	// pass — that is the carve-out boundary ADR 016 codifies. Disk is
	// the authoritative observation point because Pool.New only loads
	// the bootstrap into the in-memory pool; non-bootstrap sessions live
	// on disk between minting and the next consumer-driven Activate.
	waitForSessionState(t, regPath, nonBootstrapID, "evicted", 5*time.Second)

	// Wire check on the bootstrap: a future change that "simplifies" the
	// load layer to ignore lifecycle_state for *all* sessions would still
	// leave the bootstrap active here, but would flip the disk-side
	// non-bootstrap entry above to active. Cross-checking the bootstrap
	// stays active confirms the seeded "active" wasn't cross-contaminated
	// by the non-bootstrap "evicted" seed.
	r := h.Run(t, "sessions", "list", "--json")
	if r.ExitCode != 0 {
		t.Fatalf("pyry sessions list --json exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	var payload struct {
		Sessions []control.SessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal(r.Stdout, &payload); err != nil {
		t.Fatalf("decode sessions list: %v\nstdout:\n%s", err, r.Stdout)
	}
	var bootstrap *control.SessionInfo
	for i := range payload.Sessions {
		if payload.Sessions[i].ID == bootstrapID {
			bootstrap = &payload.Sessions[i]
			break
		}
	}
	if bootstrap == nil {
		t.Fatalf("bootstrap session %s missing from sessions list:\n%s",
			bootstrapID, r.Stdout)
	}
	if bootstrap.State != "active" {
		t.Errorf("bootstrap state: got %q want %q — non-bootstrap-evicted seed cross-contaminated bootstrap\nstdout:\n%s",
			bootstrap.State, "active", r.Stdout)
	}
}
