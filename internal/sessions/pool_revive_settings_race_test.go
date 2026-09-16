package sessions

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// gapRunner is a lifecycleRunner that reports the argv it would ACTUALLY spawn
// with, rather than the one it was constructed with. It holds the construction
// argv, lets SetSpawnArgs replace it, and records the current value at Run —
// which is (*streamsup.Runner)'s own semantics, where beginSpawn reads the field
// setArgsLocked installs into.
//
// recordingRunnerFactory cannot stand in for it: that double records
// RunnerConfig.ClaudeArgs at CONSTRUCTION, which is the right subject for every
// argv assertion in this package but the wrong one here. The whole question in
// this file is what a session materialised while a dormant write was in flight
// spawns with, and the answer can only arrive after construction.
//
// Construction is deliberately NOT recorded, so waitArgv cannot return before the
// child exists: the recorder is keyed by working directory and the last write
// wins, so a construction-time record would give a poller something stale to
// find.
type gapRunner struct {
	*lifecycleRunner

	// argMu guards args alone. Named apart from the embedded runner's own mu
	// rather than reusing it: this double records on Run, which the embedded Run
	// holds mu across, and one lock covering both would be held over a child's
	// whole life.
	argMu sync.Mutex
	args  []string
}

func newGapRunner(cfg RunnerConfig) *gapRunner {
	return &gapRunner{
		lifecycleRunner: &lifecycleRunner{workDir: cfg.WorkDir, sessionID: cfg.SessionID},
		args:            append([]string(nil), cfg.ClaudeArgs...),
	}
}

func (r *gapRunner) SetSpawnArgs(args []string) {
	r.argMu.Lock()
	r.args = append([]string(nil), args...)
	r.argMu.Unlock()
	// Forwarded so the embedded double's own install record stays complete for any
	// assertion that reads it.
	r.lifecycleRunner.SetSpawnArgs(args)
}

func (r *gapRunner) Run(ctx context.Context) error {
	r.argMu.Lock()
	argv := append([]string(nil), r.args...)
	r.argMu.Unlock()
	recordArgv(r.workDir, argv)
	return r.lifecycleRunner.Run(ctx)
}

// helperPoolInjectedFactory is helperPoolWarmStart with the runner factory as a
// parameter: the same pre-written registry (a bootstrap plus the given entries,
// stamped a second apart so the file order is stable) against a pool whose
// RunnerFactory the caller supplies. helperPoolArgvRecorder hardcodes
// recordingRunnerFactory, and this file's whole seam is a factory that acts.
func helperPoolInjectedFactory(t *testing.T, regPath, tplWorkDir string, factory RunnerFactory, dormant ...registryEntry) (*Pool, SessionID) {
	t.Helper()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	when := time.Now().UTC()
	boot := SessionID("550e8400-e29b-41d4-a716-446655440000")
	entries := []registryEntry{{ID: boot, CreatedAt: when, LastActiveAt: when, Bootstrap: true}}
	for i, e := range dormant {
		if e.CreatedAt.IsZero() {
			e.CreatedAt = when.Add(time.Duration(i+1) * time.Second)
			e.LastActiveAt = e.CreatedAt
		}
		entries = append(entries, e)
	}
	if err := saveRegistryLocked(regPath, &registryFile{Version: 1, Sessions: entries}); err != nil {
		t.Fatalf("pre-write registry: %v", err)
	}
	pool, err := New(Config{
		RunnerFactory: factory,
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sh",
			ClaudeArgs:     argvRecorderTemplate,
			WorkDir:        tplWorkDir,
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
		},
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		RegistryPath: regPath,
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool, boot
}

// TestPool_Revive_CarriesADormantWriteFromInsideTheBuildWindow (AC 1): a
// Pool.UpdateDormantSettings that lands between a revive's settings evaluation
// and materialise's retirement of the entry is CARRIED by the revived session,
// rather than acknowledged and then dropped (#2492).
//
// The interleaving is driven deterministically with no sleep and no polling for a
// race, and needs no production hook. Config.RunnerFactory is mandatory and
// injectable, and materialise reaches it through buildSession — inside the window
// — so a factory that performs the dormant write on its first construction for
// the target lands that write in the gap synchronously. It holds no pool lock
// while doing so: materialise releases its provisional read's RLock before
// buildSession runs, exactly as the pre-fix tree released revivedSettings'.
//
// Red on the pre-fix tree, where Revive evaluates revivedSettings as an ARGUMENT
// to materialise: the write is persisted, the entry is then deleted, and the
// session materialises at "sonnet" — the value read before the write.
//
// The fixture's entry persists yolo:true beside its model, so carrying model and
// effort across the window is asserted NOT to carry the posture with them. That
// keeps #1487's revocation property non-vacuous on the path this ticket opens.
func TestPool_Revive_CarriesADormantWriteFromInsideTheBuildWindow(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	spawnDir := t.TempDir()
	target := helperDormantID(t)

	var (
		poolRef  atomic.Pointer[Pool]
		fired    atomic.Bool
		writeMu  sync.Mutex
		wrote    bool
		writeErr error
	)
	factory := func(cfg RunnerConfig) (Runner, error) {
		// Gated on the target id: the bootstrap is constructed inside New, before
		// the pool pointer below exists.
		if cfg.SessionID == string(target) && fired.CompareAndSwap(false, true) {
			err := poolRef.Load().UpdateDormantSettings(target, SettingsUpdate{
				Model:  ptr("opus"),
				Effort: ptr("high"),
			})
			writeMu.Lock()
			wrote, writeErr = true, err
			writeMu.Unlock()
		}
		return newGapRunner(cfg), nil
	}

	pool, _ := helperPoolInjectedFactory(t, regPath, t.TempDir(), factory,
		registryEntry{ID: target, Label: "conv-1", Model: "sonnet", YOLO: true},
	)
	poolRef.Store(pool)
	ctx, _ := runPoolInBackground(t, pool)

	if _, err := pool.Revive(target, "conv-1", spawnDir); err != nil {
		t.Fatalf("Revive: %v", err)
	}

	writeMu.Lock()
	gotWrote, gotWriteErr := wrote, writeErr
	writeMu.Unlock()
	if !gotWrote {
		t.Fatalf("the injected factory never fired for %q, so no write ever entered the window and this test proves nothing", target)
	}
	if gotWriteErr != nil {
		t.Fatalf("in-window UpdateDormantSettings: %v — the entry is still dormant at that instant, so the write must be accepted; the acknowledgement is what this ticket is about", gotWriteErr)
	}

	got, err := pool.SettingsFor(target)
	if err != nil {
		t.Fatalf("SettingsFor(revived): %v", err)
	}
	if got.Model != "opus" || got.Effort != "high" {
		t.Errorf("revived model/effort = %q/%q, want %q/%q — a write acknowledged inside the window must not be dropped by the revive that retires the entry (#2492)",
			got.Model, got.Effort, "opus", "high")
	}
	if got.YOLO || got.PermissionMode != permissionModeDefault {
		t.Errorf("revived posture = %q (yolo=%v), want %q (yolo=false) — carrying model and effort across the window must not carry the entry's persisted bypass with them (#1487/#2448)",
			got.PermissionMode, got.YOLO, permissionModeDefault)
	}

	// The argv half: what the child spawned by the next Activate actually runs
	// with. waitArgv fatals when either the #943 "--settings" pair or the #2093
	// "--append-system-prompt-file" pair is missing, so a recompose that shortened
	// the argv on its way to the runner is red here too.
	if err := pool.Activate(ctx, target); err != nil {
		t.Fatalf("Activate(revived): %v", err)
	}
	gotArgv := waitArgv(t, spawnDir)
	wantArgv := append([]string{"--session-id", string(target), "--model", "opus", "--effort", "high"},
		alwaysOnPosture(permissionModeDefault)...)
	if !reflect.DeepEqual(gotArgv, wantArgv) {
		t.Errorf("revived child argv = %v, want %v — the spawned child must run on the model the operator was told landed", gotArgv, wantArgv)
	}
}

// TestPool_UpdateDormantSettings_RefusesAnIDMaterialiseHasRetired (AC 2): once
// materialise has retired an entry, the dormant write for that id still answers
// ErrSessionNotFound and leaves the now-live session's settings untouched.
//
// It is the boundary the fix must not buy its way past. Closing the window by
// restoring the retired entry, or by keeping a copy the write can still reach,
// would make the dormant and live halves overlap — and this test is what goes red
// when they do. The refusal is correct rather than merely safe: the settings
// reached nothing, and the operator's next pick lands on the live session through
// Pool.UpdateSettings.
func TestPool_UpdateDormantSettings_RefusesAnIDMaterialiseHasRetired(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	spawnDir := t.TempDir()
	target := helperDormantID(t)

	pool, _ := helperPoolWarmStart(t, regPath, t.TempDir(),
		registryEntry{ID: target, Label: "conv-1", Model: "sonnet", Effort: "low"},
	)
	runPoolInBackground(t, pool)

	if _, err := pool.Revive(target, "conv-1", spawnDir); err != nil {
		t.Fatalf("Revive: %v", err)
	}
	before, err := pool.SettingsFor(target)
	if err != nil {
		t.Fatalf("SettingsFor(revived): %v", err)
	}

	if err := pool.UpdateDormantSettings(target, SettingsUpdate{
		Model:  ptr("opus"),
		Effort: ptr("high"),
	}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("UpdateDormantSettings(retired id) err = %v, want ErrSessionNotFound — a revived id is live, not dormant", err)
	}

	after, err := pool.SettingsFor(target)
	if err != nil {
		t.Fatalf("SettingsFor after the refused write: %v", err)
	}
	if after != before {
		t.Errorf("live settings = %+v, want %+v unchanged — a refused dormant write must reach the live session no more than it reaches the entry", after, before)
	}
	if _, err := pool.DormantSettingsFor(target); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("DormantSettingsFor(retired id) err = %v, want ErrSessionNotFound — the live/dormant partition stays total", err)
	}
}
