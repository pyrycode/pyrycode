package main

import (
	"log/slog"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// TestMapStreamState asserts the covariant-return adapter maps every streamsup
// lifecycle field to its supervisor.State twin, and that each Phase value maps to
// the identically-named supervisor phase. The build-time assertion in
// streamsup_runner.go (var _ sessions.Runner = streamRunner{}) is AC1's real
// proof; this pins the one non-trivial method the adapter adds.
func TestMapStreamState(t *testing.T) {
	t.Parallel()

	// Every streamsup phase maps to its supervisor twin by string identity.
	phases := []struct {
		in   streamsup.Phase
		want supervisor.Phase
	}{
		{streamsup.PhaseStarting, supervisor.PhaseStarting},
		{streamsup.PhaseRunning, supervisor.PhaseRunning},
		{streamsup.PhaseBackoff, supervisor.PhaseBackoff},
		{streamsup.PhaseStopped, supervisor.PhaseStopped},
	}
	for _, p := range phases {
		if got := mapStreamState(streamsup.State{Phase: p.in}).Phase; got != p.want {
			t.Errorf("mapStreamState phase %q = %q, want %q", p.in, got, p.want)
		}
	}

	// The six numeric/time fields copy through unchanged.
	started := time.Unix(1700000000, 0)
	in := streamsup.State{
		Phase:        streamsup.PhaseRunning,
		ChildPID:     4321,
		StartedAt:    started,
		RestartCount: 7,
		LastUptime:   90 * time.Second,
		NextBackoff:  2 * time.Second,
	}
	got := mapStreamState(in)
	want := supervisor.State{
		Phase:        supervisor.PhaseRunning,
		ChildPID:     4321,
		StartedAt:    started,
		RestartCount: 7,
		LastUptime:   90 * time.Second,
		NextBackoff:  2 * time.Second,
	}
	if got != want {
		t.Errorf("mapStreamState()\n got  = %+v\n want = %+v", got, want)
	}
}

// TestStripSessionIDFlags covers every argv form the factory must neutralise so
// streamsup.buildArgs is the sole injector of --session-id/--resume: the
// two-token form the codebase actually produces, the joined --flag=value form an
// operator template could carry, a dangling flag with no value, and the pass-
// through case where no id flag is present. It also pins that the input slice is
// never mutated — ClaudeArgs is aliased into the pool's spawnBase.
func TestStripSessionIDFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"empty", []string{}, nil},
		{"no id flags", []string{"--model", "m"}, []string{"--model", "m"}},
		{
			"two-token session-id in middle",
			[]string{"--model", "m", "--session-id", "ID", "--settings", "p"},
			[]string{"--model", "m", "--settings", "p"},
		},
		{"two-token resume alone", []string{"--resume", "ID"}, nil},
		{
			"joined session-id",
			[]string{"--session-id=ID", "--model", "m"},
			[]string{"--model", "m"},
		},
		{"joined resume alone", []string{"--resume=ID"}, nil},
		{
			"dangling session-id (no value)",
			[]string{"--model", "m", "--session-id"},
			[]string{"--model", "m"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			saved := slices.Clone(tt.in)
			got := stripSessionIDFlags(tt.in)
			if !slices.Equal(got, tt.want) {
				t.Errorf("stripSessionIDFlags(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if !slices.Equal(tt.in, saved) {
				t.Errorf("stripSessionIDFlags mutated its input: got %q, want %q", tt.in, saved)
			}
		})
	}
}

// TestMapStreamsupConfig_Bootstrap mirrors the Pool.New bootstrap shape: a
// SessionID plus ClaudeArgs that carry no id flag (the PTY path resolves the id
// via ResolveSessionID). Every mapped field must copy through, the strip is a
// no-op on these args, and Stdout stays nil (the #1098 turnevent sink is out of
// scope here).
func TestMapStreamsupConfig_Bootstrap(t *testing.T) {
	t.Parallel()

	logger := slog.Default()
	cfg := supervisor.Config{
		ClaudeBin:      "/opt/claude",
		WorkDir:        "/work",
		SessionID:      "boot-uuid",
		ClaudeArgs:     []string{"--settings", "p"},
		Logger:         logger,
		BackoffInitial: 1 * time.Second,
		BackoffMax:     20 * time.Second,
		BackoffReset:   40 * time.Second,
	}
	got := mapStreamsupConfig(cfg)

	if got.ClaudeBin != "/opt/claude" {
		t.Errorf("ClaudeBin = %q, want %q", got.ClaudeBin, "/opt/claude")
	}
	if got.WorkDir != "/work" {
		t.Errorf("WorkDir = %q, want %q", got.WorkDir, "/work")
	}
	if got.SessionID != "boot-uuid" {
		t.Errorf("SessionID = %q, want %q", got.SessionID, "boot-uuid")
	}
	if !slices.Equal(got.Args, []string{"--settings", "p"}) {
		t.Errorf("Args = %q, want %q (strip is a no-op at bootstrap)", got.Args, []string{"--settings", "p"})
	}
	if got.Logger != logger {
		t.Errorf("Logger = %v, want the passed-in logger", got.Logger)
	}
	if got.BackoffInitial != 1*time.Second || got.BackoffMax != 20*time.Second || got.BackoffReset != 40*time.Second {
		t.Errorf("Backoff = {%v,%v,%v}, want {1s,20s,40s}", got.BackoffInitial, got.BackoffMax, got.BackoffReset)
	}
	if got.Stdout != nil {
		t.Errorf("Stdout = %v, want nil (turnevent sink is #1098)", got.Stdout)
	}
	if got.Stderr != nil || got.Env != nil {
		t.Errorf("Stderr/Env = %v/%v, want nil (no supervisor.Config analogue)", got.Stderr, got.Env)
	}
}

// TestMapStreamsupConfig_PerSession mirrors the Pool.buildSession shape, where
// ClaudeArgs bake in "--session-id <id>" for the PTY path. The AC-2 no-double-
// inject proof asserted directly on the observable .Args: the mapped argv must
// carry no --session-id, no --resume, and no residual bare id token, so
// streamsup.buildArgs re-injects exactly one id flag from SessionID.
func TestMapStreamsupConfig_PerSession(t *testing.T) {
	t.Parallel()

	cfg := supervisor.Config{
		ClaudeBin:  "/opt/claude",
		WorkDir:    "/work",
		SessionID:  "sess-uuid",
		ClaudeArgs: []string{"--session-id", "sess-uuid", "--settings", "p"},
	}
	got := mapStreamsupConfig(cfg)

	if got.SessionID != "sess-uuid" {
		t.Errorf("SessionID = %q, want %q", got.SessionID, "sess-uuid")
	}
	for _, a := range got.Args {
		if a == "--session-id" || a == "--resume" || a == "sess-uuid" {
			t.Errorf("Args %q still carries an id flag/value %q — would double-inject", got.Args, a)
		}
	}
	if !slices.Equal(got.Args, []string{"--settings", "p"}) {
		t.Errorf("Args = %q, want %q", got.Args, []string{"--settings", "p"})
	}
}

// TestStreamRunnerFactory_Construct drives the factory with both pool shapes and
// asserts a live *streamsup.Runner comes back wrapped in the streamRunner adapter
// (AC-1, AC-2 "constructs successfully at both sites"). os.Args[0] is an absolute,
// resolvable path so exec.LookPath accepts it; t.TempDir() is a real work dir.
func TestStreamRunnerFactory_Construct(t *testing.T) {
	t.Parallel()

	shapes := []struct {
		name string
		args []string
	}{
		{"bootstrap", []string{"--settings", "p"}},
		{"per-session", []string{"--session-id", "sess-uuid", "--settings", "p"}},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			cfg := supervisor.Config{
				ClaudeBin:  os.Args[0],
				WorkDir:    t.TempDir(),
				SessionID:  "id-" + s.name,
				ClaudeArgs: s.args,
			}
			runner, err := streamRunnerFactory(cfg)
			if err != nil {
				t.Fatalf("streamRunnerFactory(%s) error = %v, want nil", s.name, err)
			}
			sr, ok := runner.(streamRunner)
			if !ok {
				t.Fatalf("runner is %T, want streamRunner", runner)
			}
			if sr.r == nil {
				t.Errorf("streamRunner.r is nil, want a constructed *streamsup.Runner")
			}
		})
	}
}

// TestStreamRunnerFactory_ErrorPropagation proves AC-3: a streamsup.New failure
// surfaces as (nil runner, non-nil error) with no silent PTY fallback. A missing
// binary fails streamsup.New's exec.LookPath; supervisor.New does NOT LookPath at
// construction, so a fallback to it would return (non-nil, nil) and fail here.
func TestStreamRunnerFactory_ErrorPropagation(t *testing.T) {
	t.Parallel()

	cfg := supervisor.Config{
		ClaudeBin:  "pyry-nonexistent-binary-xyz",
		WorkDir:    t.TempDir(),
		SessionID:  "sess-uuid",
		ClaudeArgs: []string{"--settings", "p"},
	}
	runner, err := streamRunnerFactory(cfg)
	if err == nil {
		t.Fatalf("streamRunnerFactory error = nil, want non-nil for a missing binary")
	}
	if runner != nil {
		t.Errorf("runner = %v, want nil (no silent PTY fallback)", runner)
	}
}
