package sessions

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

// configCapture records every RunnerConfig the pool hands its factory, so a test
// can assert what crossed the runner seam rather than what the argv happens to
// spell. Two of the three things #2065 sends across that seam — PermissionMode
// and OperatorBypass — are not argv tokens at all, so nothing else can see them.
type configCapture struct {
	mu   sync.Mutex
	cfgs []RunnerConfig
}

func (c *configCapture) factory(cfg RunnerConfig) (Runner, error) {
	c.mu.Lock()
	c.cfgs = append(c.cfgs, cfg)
	c.mu.Unlock()
	return &lifecycleRunner{workDir: cfg.WorkDir}, nil
}

func (c *configCapture) snapshot() []RunnerConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.cfgs)
}

// newCapturePool builds a pool whose factory is the capture above, warm-starting
// from a bootstrap entry carrying storedMode VERBATIM — no permissionModeForDisk
// — so a junk value really does reach the registry read. That is the point of the
// helper: canonicalPermissionMode is a normaliser of trusted input, and the only
// way to exercise it is to hand it something a writer would have rejected.
func newCapturePool(t *testing.T, passThrough []string, storedMode string, yolo bool) (*Pool, *configCapture) {
	t.Helper()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	when := time.Now().UTC()
	if err := saveRegistryLocked(regPath, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID:             SessionID("550e8400-e29b-41d4-a716-446655440000"),
			CreatedAt:      when,
			LastActiveAt:   when,
			Bootstrap:      true,
			YOLO:           yolo,
			PermissionMode: storedMode,
		}},
	}); err != nil {
		t.Fatalf("pre-write registry: %v", err)
	}
	cap := &configCapture{}
	pool, err := New(Config{
		Bootstrap: SessionConfig{
			ClaudeBin:  "/bin/sh",
			WorkDir:    t.TempDir(),
			ClaudeArgs: passThrough,
		},
		RegistryPath:  regPath,
		RunnerFactory: cap.factory,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	return pool, cap
}

// TestRunnerConfigPermissionModeIsAlwaysKnown is the security-review addendum
// #2065's plan records, and the reason it exists is a POLARITY INVERSION rather
// than a new bug.
//
// internal/streamsup's spawnAndWait writes nothing when the mode it was handed is
// outside permissionModeSpawnWritable (permissionModeAllowed minus the escalation
// since #2066; the empty and unrecognised values this test is about are outside
// both). Before #2065 that arm meant "the child launched
// with no escalation flag, nothing was written, it runs in default" — benign.
// After #2065 the same arm means "the child launched IN BYPASS, nothing was
// written, its posture gate is open, and its turns flow in bypass". The
// consequence of the arm flipped, and it flipped silently.
//
// It is unreachable through this package: canonicalSettings runs above both
// RunnerConfig construction sites, and Pool.UpdateSettings canonicalises before
// SetSpawnPermissionMode. "Unreachable" is exactly the kind of claim that decays,
// so it is pinned here across all three construction paths, including the one
// that reads a hand-edited registry.
//
// Do NOT respond to a red here by making streamsup's empty-mode arm fail closed.
// That would gate every construction site outside the interactive daemon, which
// is the bricked-session shape #2064 rejected.
func TestRunnerConfigPermissionModeIsAlwaysKnown(t *testing.T) {
	t.Parallel()
	for _, stored := range []string{"", "notAMode", "BYPASSPERMISSIONS", "plan", permissionModeBypass} {
		t.Run("stored "+stored, func(t *testing.T) {
			t.Parallel()
			pool, cap := newCapturePool(t, nil, stored, false)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			go func() { _ = pool.Run(ctx) }()
			<-pool.Ready()

			// Mint and revive, so all three construction paths run in one arm.
			if _, err := pool.Create(ctx, "minted"); err != nil {
				t.Fatalf("Create: %v", err)
			}
			target, err := NewID()
			if err != nil {
				t.Fatalf("NewID: %v", err)
			}
			if _, err := pool.Revive(target, "conv-1", ""); err != nil {
				t.Fatalf("Revive: %v", err)
			}

			cfgs := cap.snapshot()
			if len(cfgs) != 3 {
				t.Fatalf("captured %d RunnerConfigs, want 3 (bootstrap, minted, revived): the fixture did not exercise every construction path", len(cfgs))
			}
			for i, cfg := range cfgs {
				if !permissionModeKnown(cfg.PermissionMode) {
					t.Errorf("RunnerConfig[%d].PermissionMode = %q, which no writer can name; with the escalation flag on every argv (#2065) that child would launch in bypass and never be walked back",
						i, cfg.PermissionMode)
				}
			}
			// The revived session is the #1487 row: it inherits nothing, so its
			// posture is the default one whatever the bootstrap stored.
			if got := cfgs[2].PermissionMode; got != permissionModeDefault {
				t.Errorf("revived RunnerConfig.PermissionMode = %q, want %q — a revived session must be downgraded, never left escalated (#1487)",
					got, permissionModeDefault)
			}
		})
	}
}

// TestRunnerConfigOperatorBypass is #2065's provenance signal at the sessions
// seam: the daemon must be able to tell an escalation it composed itself, and may
// therefore walk back, from one the operator handed it, which it may not.
//
// Both arms drive all three construction paths, because the answer is per-daemon
// rather than per-session — a minted session's spawnBase is built from the same
// SessionConfig.ClaudeArgs the bootstrap's is.
//
// The false arm is the one that reddens on a tree deriving the bit from the
// ASSEMBLED argv instead of the settings-free base: every assembled argv carries
// the escalation flag since #2065, so such a tree answers true everywhere and
// both of the daemon's permission fail-safes invert into their permissive arms.
func TestRunnerConfigOperatorBypass(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		passThrough []string
		want        bool
	}{
		{"no pass-through", nil, false},
		{"a pass-through without the escalation", []string{"--verbose"}, false},
		// The shape cmd/pyry's own install-service example documents:
		// `pyry install-service -- --dangerously-skip-permissions`.
		{"the operator's escalation pass-through", []string{"--dangerously-skip-permissions"}, true},
		{"the escalation beside other pass-through flags", []string{"--verbose", "--dangerously-skip-permissions"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pool, cap := newCapturePool(t, tc.passThrough, permissionModeDefault, false)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			go func() { _ = pool.Run(ctx) }()
			<-pool.Ready()

			if _, err := pool.Create(ctx, "minted"); err != nil {
				t.Fatalf("Create: %v", err)
			}
			target, err := NewID()
			if err != nil {
				t.Fatalf("NewID: %v", err)
			}
			if _, err := pool.Revive(target, "conv-1", ""); err != nil {
				t.Fatalf("Revive: %v", err)
			}

			cfgs := cap.snapshot()
			if len(cfgs) != 3 {
				t.Fatalf("captured %d RunnerConfigs, want 3 (bootstrap, minted, revived)", len(cfgs))
			}
			for i, cfg := range cfgs {
				if cfg.OperatorBypass != tc.want {
					t.Errorf("RunnerConfig[%d].OperatorBypass = %v, want %v for pass-through %q",
						i, cfg.OperatorBypass, tc.want, tc.passThrough)
				}
				// AC 1 at the seam: every child launches with the escalation,
				// whatever the provenance answer is. This is what makes the false
				// arm above a real discrimination rather than a reading of the argv.
				if !slices.Contains(cfg.ClaudeArgs, "--dangerously-skip-permissions") {
					t.Errorf("RunnerConfig[%d].ClaudeArgs = %v, want the escalation flag on every spawn (#2065)", i, cfg.ClaudeArgs)
				}
			}
		})
	}
}

// TestRunnerConfigOperatorBypass_ReadsBaseNotAssembledArgv is the mutant guard for
// the test above: it shows the two readings DISAGREE on the same session, so a
// tree that swapped one for the other could not pass both.
//
// Without it, "OperatorBypass is false here" could be read as a claim about a
// signal that happens to be false for an unrelated reason. It is false while the
// assembled argv the very same config carries says otherwise.
func TestRunnerConfigOperatorBypass_ReadsBaseNotAssembledArgv(t *testing.T) {
	t.Parallel()
	pool, cap := newCapturePool(t, nil, permissionModeDefault, false)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = pool.Run(ctx) }()
	<-pool.Ready()

	cfgs := cap.snapshot()
	if len(cfgs) == 0 {
		t.Fatalf("no RunnerConfig captured")
	}
	cfg := cfgs[0]
	if !slices.Contains(cfg.ClaudeArgs, "--dangerously-skip-permissions") {
		t.Fatalf("ClaudeArgs = %v, want the escalation flag: the fixture cannot show a disagreement without it", cfg.ClaudeArgs)
	}
	if cfg.OperatorBypass {
		t.Errorf("OperatorBypass = true for a daemon with no pass-through claude args; the bit was read off the assembled argv (%v), which carries the flag on every session since #2065, rather than off the settings-free base",
			cfg.ClaudeArgs)
	}
}

// TestSpawnArgsOperatorBypassSurvivesRecompose pins the property that lets
// OperatorBypass be a construction-time value rather than a per-spawn read:
// Session.spawnArgs recomposes from the immutable spawnBase, so no settings change
// can add or remove the operator's escalation. Pool.UpdateSettings' two install
// paths (Restart, SetSpawnArgs) both go through it.
func TestSpawnArgsOperatorBypassSurvivesRecompose(t *testing.T) {
	t.Parallel()
	for _, base := range [][]string{
		{"--settings", "/tmp/s.json"},
		{"--dangerously-skip-permissions", "--settings", "/tmp/s.json"},
	} {
		want := operatorBypass(base)
		sess := &Session{spawnBase: base}
		for _, settings := range []SessionSettings{
			{},
			{PermissionMode: permissionModeDefault},
			{PermissionMode: "plan"},
			{YOLO: true, PermissionMode: permissionModeBypass},
			{Model: "opus", Effort: "high", PermissionMode: "dontAsk"},
		} {
			got := sess.spawnArgs(settings)
			if !reflect.DeepEqual(got[:len(base)], base) {
				t.Fatalf("spawnArgs(%+v) = %v, want it to begin with the base %v", settings, got, base)
			}
			if operatorBypass(got[:len(base)]) != want {
				t.Errorf("spawnArgs(%+v) changed the base's provenance answer: base %v, argv %v", settings, base, got)
			}
		}
	}
}
