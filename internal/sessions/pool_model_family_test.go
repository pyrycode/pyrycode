package sessions

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// pinnedOpus are the pinned Opus spellings the 2026-10-07 report turned on: the
// id with no minor version that started a new channel on Opus 5, and the two
// minor-versioned ids pyrybox's saved model list carried. Each must start a
// session as plain opus, whatever path it reaches the spawn by.
var pinnedOpus = []string{"claude-opus-5", "claude-opus-4-7", "claude-opus-4-8"}

// familyPoolOpts configures helperFamilyPool.
type familyPoolOpts struct {
	bootModel    string              // the bootstrap entry's stored model, the "remembered" one
	defaultModel func(string) string // Config.DefaultModel
	operatorArgs []string            // appended to the operator's template argv
	dormant      []registryEntry     // further registry entries, held dormant
}

// helperFamilyPool warm-starts an argv-recording pool, helperPoolArgvRecorder's
// recipe, over a registry it writes first, with the options above.
func helperFamilyPool(t *testing.T, regPath, tplWorkDir string, opts familyPoolOpts) *Pool {
	t.Helper()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	when := time.Now().UTC()
	entries := []registryEntry{{
		ID:           SessionID("550e8400-e29b-41d4-a716-446655440000"),
		CreatedAt:    when,
		LastActiveAt: when,
		Bootstrap:    true,
		Model:        opts.bootModel,
	}}
	for i, e := range opts.dormant {
		e.CreatedAt = when.Add(time.Duration(i+1) * time.Second)
		e.LastActiveAt = e.CreatedAt
		entries = append(entries, e)
	}
	if err := saveRegistryLocked(regPath, &registryFile{Version: 1, Sessions: entries}); err != nil {
		t.Fatalf("pre-write registry: %v", err)
	}
	pool, err := New(Config{
		RunnerFactory: recordingRunnerFactory,
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sh",
			ClaudeArgs:     append(slices.Clone(argvRecorderTemplate), opts.operatorArgs...),
			WorkDir:        tplWorkDir,
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
		},
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		RegistryPath: regPath,
		DefaultModel: opts.defaultModel,
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	if err := pool.SetDaemonInstructions(""); err != nil {
		t.Fatal(err)
	}
	return pool
}

// modelArgs returns every model an argv names, in order, from both the
// two-token and the --model=value spellings.
func modelArgs(argv []string) []string {
	var out []string
	for i := 0; i < len(argv); i++ {
		if argv[i] == "--model" && i+1 < len(argv) {
			out = append(out, argv[i+1])
			i++
		} else if v, ok := strings.CutPrefix(argv[i], "--model="); ok {
			out = append(out, v)
		}
	}
	return out
}

// pinnedDefault answers a Config.DefaultModel that reports model for any workDir,
// the shape pyrybox's ~/.claude/settings.json gave on 2026-10-07.
func pinnedDefault(model string) func(string) string {
	return func(string) string { return model }
}

// TestPool_PinnedModelSpawnsAsItsFamily walks every start path a Claude model
// reaches the spawn by, with each pinned Opus spelling, and asserts the child is
// launched with --model opus and the session holds opus where it holds a model at
// all. The live model change is TestPool_UpdateSettings_PinnedPickStoresAndSendsTheFamily's.
func TestPool_PinnedModelSpawnsAsItsFamily(t *testing.T) {
	t.Parallel()
	for _, pinned := range pinnedOpus {
		t.Run(pinned, func(t *testing.T) {
			t.Parallel()

			// A channel created with a model in its request (create_conversation).
			t.Run("client request at creation", func(t *testing.T) {
				t.Parallel()
				regPath := filepath.Join(t.TempDir(), "sessions.json")
				spawnDir := t.TempDir()
				pool := helperFamilyPool(t, regPath, t.TempDir(), familyPoolOpts{})
				runPoolInBackground(t, pool)
				id, err := pool.MintWith("conv-1", spawnDir, HarnessClaude, SessionSettings{Model: pinned})
				if err != nil {
					t.Fatalf("MintWith: %v", err)
				}
				assertSpawnsAndHolds(t, pool, id, spawnDir, "opus")
			})

			// A conversation whose saved setting predates the family rule, started on
			// its next message after a daemon restart.
			t.Run("saved per-conversation setting", func(t *testing.T) {
				t.Parallel()
				regPath := filepath.Join(t.TempDir(), "sessions.json")
				spawnDir := t.TempDir()
				target := helperDormantID(t)
				pool := helperFamilyPool(t, regPath, t.TempDir(), familyPoolOpts{
					dormant: []registryEntry{{ID: target, Label: "conv-1", Model: pinned}},
				})
				runPoolInBackground(t, pool)
				if got, err := pool.DormantSettingsFor(target); err != nil || got.Model != "opus" {
					t.Errorf("DormantSettingsFor().Model = %q, %v; want opus before the session starts", got.Model, err)
				}
				if _, err := pool.Revive(target, "conv-1", spawnDir); err != nil {
					t.Fatalf("Revive: %v", err)
				}
				assertSpawnsAndHolds(t, pool, target, spawnDir, "opus")
			})

			// A new channel with no model of its own, inheriting the operator's
			// configured model from the bootstrap (Pool.MintDefaults).
			t.Run("remembered model", func(t *testing.T) {
				t.Parallel()
				regPath := filepath.Join(t.TempDir(), "sessions.json")
				tplWorkDir := t.TempDir()
				spawnDir := t.TempDir()
				pool := helperFamilyPool(t, regPath, tplWorkDir, familyPoolOpts{bootModel: pinned})
				runPoolInBackground(t, pool)
				if got := modelArgs(waitArgv(t, tplWorkDir)); !slices.Equal(got, []string{"opus"}) {
					t.Errorf("bootstrap argv models = %q, want [opus]", got)
				}
				if got := pool.MintDefaults(HarnessClaude).Model; got != "opus" {
					t.Errorf("MintDefaults().Model = %q, want opus", got)
				}
				id, err := pool.Mint("conv-1", spawnDir)
				if err != nil {
					t.Fatalf("Mint: %v", err)
				}
				assertSpawnsAndHolds(t, pool, id, spawnDir, "opus")
			})

			// The reported bug: no model chosen anywhere, and Claude Code's own
			// settings file pins one. The session holds no model of its own; the argv
			// names the default's family.
			t.Run("claude default", func(t *testing.T) {
				t.Parallel()
				regPath := filepath.Join(t.TempDir(), "sessions.json")
				tplWorkDir := t.TempDir()
				spawnDir := t.TempDir()
				var asked []string
				pool := helperFamilyPool(t, regPath, tplWorkDir, familyPoolOpts{
					defaultModel: func(workDir string) string {
						asked = append(asked, workDir)
						return pinned
					},
				})
				runPoolInBackground(t, pool)
				if got := modelArgs(waitArgv(t, tplWorkDir)); !slices.Equal(got, []string{"opus"}) {
					t.Errorf("bootstrap argv models = %q, want [opus]", got)
				}
				id, err := pool.Mint("conv-1", spawnDir)
				if err != nil {
					t.Fatalf("Mint: %v", err)
				}
				assertSpawnsAndHolds(t, pool, id, spawnDir, "")
				// Resolved against each child's own working directory, where Claude Code
				// reads its project settings.
				if want := []string{tplWorkDir, spawnDir}; !slices.Equal(asked, want) {
					t.Errorf("DefaultModel asked for %q, want %q", asked, want)
				}
			})

			// An operator who put --model in the daemon's own claude arguments.
			for _, spelling := range []string{"two tokens", "equals"} {
				t.Run("operator --model, "+spelling, func(t *testing.T) {
					t.Parallel()
					regPath := filepath.Join(t.TempDir(), "sessions.json")
					tplWorkDir := t.TempDir()
					args := []string{"--model", pinned}
					if spelling == "equals" {
						args = []string{"--model=" + pinned}
					}
					// A pinned default too: the operator's flag already names a model, so
					// the default must not add a second one.
					pool := helperFamilyPool(t, regPath, tplWorkDir, familyPoolOpts{
						operatorArgs: args,
						defaultModel: pinnedDefault("claude-sonnet-5"),
					})
					runPoolInBackground(t, pool)
					if got := modelArgs(waitArgv(t, tplWorkDir)); !slices.Equal(got, []string{"opus"}) {
						t.Errorf("bootstrap argv models = %q, want [opus]", got)
					}
				})
			}
		})
	}
}

// assertSpawnsAndHolds asserts id's runner was composed in spawnDir with exactly
// one --model, opus, and that the session holds wantHeld as its own model.
func assertSpawnsAndHolds(t *testing.T, pool *Pool, id SessionID, spawnDir, wantHeld string) {
	t.Helper()
	if got := modelArgs(waitArgv(t, spawnDir)); !slices.Equal(got, []string{"opus"}) {
		t.Errorf("spawn argv models = %q, want [opus]", got)
	}
	got, err := pool.SettingsFor(id)
	if err != nil {
		t.Fatalf("SettingsFor: %v", err)
	}
	if got.Model != wantHeld {
		t.Errorf("SettingsFor().Model = %q, want %q", got.Model, wantHeld)
	}
}

// TestPool_DefaultModel_OnlyAPinnedDefaultIsNamed: a default that is already a
// family, or no default at all, leaves the argv exactly as it was, and a model
// the session holds wins over a pinned default.
func TestPool_DefaultModel_OnlyAPinnedDefaultIsNamed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		defaultFn  func(string) string
		held       string
		wantModels []string
	}{
		{"no resolver", nil, "", nil},
		{"no default", pinnedDefault(""), "", nil},
		{"a family default", pinnedDefault("opus"), "", nil},
		{"the default sentinel", pinnedDefault("default"), "", nil},
		{"a held model wins over a pinned default", pinnedDefault("claude-opus-4-7"), "sonnet", []string{"sonnet"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			regPath := filepath.Join(t.TempDir(), "sessions.json")
			spawnDir := t.TempDir()
			pool := helperFamilyPool(t, regPath, t.TempDir(), familyPoolOpts{defaultModel: tc.defaultFn})
			runPoolInBackground(t, pool)
			if _, err := pool.MintWith("conv-1", spawnDir, HarnessClaude, SessionSettings{Model: tc.held}); err != nil {
				t.Fatalf("MintWith: %v", err)
			}
			if got := modelArgs(waitArgv(t, spawnDir)); !slices.Equal(got, tc.wantModels) {
				t.Errorf("spawn argv models = %q, want %q", got, tc.wantModels)
			}
		})
	}
}

// TestPool_UpdateSettings_ClearModel_RestartsOnTheDefaultsFamily: clearing a
// session's model means "run at claude's own default", applied by a restart. When
// that default is pinned, the restart argv names its family, so the clear does not
// put the session back on a superseded model.
func TestPool_UpdateSettings_ClearModel_RestartsOnTheDefaultsFamily(t *testing.T) {
	t.Parallel()
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	pool := helperFamilyPool(t, regPath, t.TempDir(), familyPoolOpts{
		bootModel:    "sonnet",
		defaultModel: pinnedDefault("claude-opus-5"),
	})
	runPoolInBackground(t, pool)
	id := pool.Default().ID()
	runner := runnerDouble(t, pool, id)
	waitRunning(t, runner)

	if err := pool.UpdateSettings(id, SettingsUpdate{Model: ptr("")}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	restarts := runner.restartArgs()
	if len(restarts) != 1 {
		t.Fatalf("Restart called %d times, want exactly 1: %v", len(restarts), restarts)
	}
	want := append([]string{"--model", "opus"}, alwaysOnPosture(permissionModeDefault)...)
	if got := installedArgv(t, restarts[0]); !reflect.DeepEqual(got, want) {
		t.Errorf("restart argv = %v, want %v", got, want)
	}
	if got := runner.modelRequests(); len(got) != 0 {
		t.Errorf("clearing the model sent a control request: %q", got)
	}
}

// TestPool_PinnedModel_CodexUnchanged: a Codex session's model is never read as a
// Claude id, and Claude Code's default model never reaches a Codex spawn.
func TestPool_PinnedModel_CodexUnchanged(t *testing.T) {
	t.Parallel()
	for _, model := range []string{"gpt-5.6-terra", "terra", ""} {
		t.Run("model "+model, func(t *testing.T) {
			t.Parallel()
			regPath := filepath.Join(t.TempDir(), "sessions.json")
			spawnDir := t.TempDir()
			pool := helperFamilyPool(t, regPath, t.TempDir(), familyPoolOpts{defaultModel: pinnedDefault("claude-opus-5")})
			runPoolInBackground(t, pool)
			id, err := pool.MintWith("conv-1", spawnDir, "codex", SessionSettings{Model: model})
			if err != nil {
				t.Fatalf("MintWith: %v", err)
			}
			var want []string
			if model != "" {
				want = []string{model}
			}
			if got := modelArgs(waitArgv(t, spawnDir)); !slices.Equal(got, want) {
				t.Errorf("codex spawn argv models = %q, want %q", got, want)
			}
			if got, err := pool.SettingsFor(id); err != nil || got.Model != model {
				t.Errorf("SettingsFor().Model = %q, %v; want %q", got.Model, err, model)
			}
		})
	}
}

// TestClaudeSettingsModel walks the sources ClaudeSettingsModel reads, in
// precedence order, against files it writes itself, never the host's own.
func TestClaudeSettingsModel(t *testing.T) {
	t.Parallel()
	write := func(t *testing.T, path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	noEnv := func(string) string { return "" }
	tests := []struct {
		name  string
		env   map[string]string
		local string // <workDir>/.claude/settings.local.json
		proj  string // <workDir>/.claude/settings.json
		user  string // <userDir>/settings.json
		want  string
	}{
		{name: "nothing names a model", want: ""},
		{name: "the user file, as on pyrybox", user: `{"permissions":{"defaultMode":"auto"},"model":"claude-opus-5"}`, want: "claude-opus-5"},
		{name: "the shared project file beats the user file", proj: `{"model":"sonnet"}`, user: `{"model":"claude-opus-5"}`, want: "sonnet"},
		{name: "the local project file beats both", local: `{"model":"haiku"}`, proj: `{"model":"sonnet"}`, user: `{"model":"claude-opus-5"}`, want: "haiku"},
		{name: "the environment beats every file", env: map[string]string{"ANTHROPIC_MODEL": "claude-opus-4-7"}, local: `{"model":"haiku"}`, want: "claude-opus-4-7"},
		{name: "a file naming no model falls through", proj: `{"permissions":{}}`, user: `{"model":"claude-opus-4-8"}`, want: "claude-opus-4-8"},
		{name: "a malformed file is skipped", proj: `{"model":`, user: `{"model":"claude-opus-4-8"}`, want: "claude-opus-4-8"},
		{name: "a non-string model is skipped", proj: `{"model":{"id":"x"}}`, user: `{"model":"opus"}`, want: "opus"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			workDir, userDir := t.TempDir(), t.TempDir()
			if tc.local != "" {
				write(t, filepath.Join(workDir, ".claude", "settings.local.json"), tc.local)
			}
			if tc.proj != "" {
				write(t, filepath.Join(workDir, ".claude", "settings.json"), tc.proj)
			}
			if tc.user != "" {
				write(t, filepath.Join(userDir, "settings.json"), tc.user)
			}
			getenv := noEnv
			if tc.env != nil {
				getenv = func(k string) string { return tc.env[k] }
			}
			if got := claudeSettingsModel(getenv, userDir, workDir); got != tc.want {
				t.Errorf("claudeSettingsModel = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("an oversized file is skipped", func(t *testing.T) {
		t.Parallel()
		workDir, userDir := t.TempDir(), t.TempDir()
		big := `{"model":"claude-opus-5","pad":"` + strings.Repeat("x", maxClaudeSettingsFile) + `"}`
		write(t, filepath.Join(workDir, ".claude", "settings.json"), big)
		write(t, filepath.Join(userDir, "settings.json"), `{"model":"sonnet"}`)
		if got := claudeSettingsModel(noEnv, userDir, workDir); got != "sonnet" {
			t.Errorf("claudeSettingsModel = %q, want sonnet from the user file", got)
		}
	})
}
