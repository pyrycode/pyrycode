package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TestMapStreamState asserts the covariant-return adapter maps every streamsup
// lifecycle field to its sessions.State twin, and that each Phase value maps to
// the identically-named supervisor phase. The build-time assertion in
// streamsup_runner.go (var _ sessions.Runner = streamRunner{}) is AC1's real
// proof; this pins the one non-trivial method the adapter adds.
func TestMapStreamState(t *testing.T) {
	t.Parallel()

	// Every streamsup phase maps to its supervisor twin by string identity.
	phases := []struct {
		in   streamsup.Phase
		want sessions.Phase
	}{
		{streamsup.PhaseStarting, sessions.PhaseStarting},
		{streamsup.PhaseRunning, sessions.PhaseRunning},
		{streamsup.PhaseBackoff, sessions.PhaseBackoff},
		{streamsup.PhaseStopped, sessions.PhaseStopped},
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
	want := sessions.State{
		Phase:        sessions.PhaseRunning,
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
	cfg := sessions.RunnerConfig{
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
		t.Errorf("Stdout = %v, want nil (the #1098 Parser is installed in newStreamRunnerFactory, not the mapper)", got.Stdout)
	}
	if got.Stderr != nil || got.Env != nil {
		t.Errorf("Stderr/Env = %v/%v, want nil (no sessions.RunnerConfig analogue)", got.Stderr, got.Env)
	}
	if !got.RequestInitializeOnSpawn {
		t.Error("RequestInitializeOnSpawn = false, want true — the ask is the interactive daemon's policy and this mapper is the one place that sets it")
	}
}

// TestMapStreamsupConfig_PerSession mirrors the Pool.buildSession shape, where
// ClaudeArgs bake in "--session-id <id>" for the PTY path. The AC-2 no-double-
// inject proof asserted directly on the observable .Args: the mapped argv must
// carry no --session-id, no --resume, and no residual bare id token, so
// streamsup.buildArgs re-injects exactly one id flag from SessionID.
func TestMapStreamsupConfig_PerSession(t *testing.T) {
	t.Parallel()

	cfg := sessions.RunnerConfig{
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
	if !got.RequestInitializeOnSpawn {
		t.Error("RequestInitializeOnSpawn = false, want true — a per-conversation runner asks its children like the bootstrap one does")
	}
}

// TestMapStreamsupConfig_ClaudeSessionsDir pins #1631's derivation: the mapper
// fills ClaudeSessionsDir from THAT runner's own WorkDir, so streamsup's by-id
// transcript probe is live on the production path and keys on the directory
// claude actually writes for that runner's child cwd.
//
// The divergent-workdir pair is the discriminator. A per-conversation runner's
// WorkDir is the phone's confined spawn dir when one was requested
// (Pool.buildSession), so it legitimately differs from the bootstrap workdir —
// and a single pool-global directory derived once from cfg.Bootstrap.WorkDir
// would be right for the bootstrap runner and wrong for exactly the
// phone-created ones. Asserting the two mapped values DIFFER is what fails that
// shape; asserting each equals the recomputed
// DefaultClaudeSessionsDir(ResolveWorkdir(thatWorkDir)) is what fails a
// per-runner derivation built on the wrong transform (the composition is
// #1655's measured one, not a preference). Real t.TempDir()s, because the
// derivation stats the path.
func TestMapStreamsupConfig_ClaudeSessionsDir(t *testing.T) {
	t.Parallel()

	want := func(t *testing.T, workdir string) string {
		t.Helper()
		resolved, err := agentrun.ResolveWorkdir(workdir)
		if err != nil {
			t.Fatalf("agentrun.ResolveWorkdir(%q) error = %v, want nil", workdir, err)
		}
		return sessions.DefaultClaudeSessionsDir(resolved)
	}
	mapped := func(workdir, id string) string {
		return mapStreamsupConfig(sessions.RunnerConfig{
			ClaudeBin: "/opt/claude",
			WorkDir:   workdir,
			SessionID: id,
		}).ClaudeSessionsDir
	}

	bootstrapDir, spawnDir := t.TempDir(), t.TempDir()
	gotBootstrap := mapped(bootstrapDir, "boot-uuid")
	gotSpawn := mapped(spawnDir, "conv-uuid")

	if gotBootstrap == gotSpawn {
		t.Fatalf("both runners mapped to one ClaudeSessionsDir %q — that is a pool-global value, not one derived from each runner's own workdir", gotBootstrap)
	}
	if w := want(t, bootstrapDir); gotBootstrap != w {
		t.Errorf("bootstrap ClaudeSessionsDir = %q, want %q", gotBootstrap, w)
	}
	if w := want(t, spawnDir); gotSpawn != w {
		t.Errorf("per-conversation ClaudeSessionsDir = %q, want %q", gotSpawn, w)
	}
}

// TestMapStreamsupConfig_ClaudeSessionsDirDegrades covers the arms where the
// directory cannot be derived: the mapped field is "", which is exactly what
// keeps streamsup's probe inert and the spawn argv byte-identical to pre-#1630.
// An empty workdir must NOT fall through to the process cwd — that is
// resolveClaudeSessionsDir's deliberate behaviour on the startup reconciliation
// path, and adopting it here would point a probe at a directory the child never
// runs in.
func TestMapStreamsupConfig_ClaudeSessionsDirDegrades(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		workdir string
	}{
		{"empty workdir", ""},
		{"unresolvable path", filepath.Join(t.TempDir(), "no-such-dir")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mapStreamsupConfig(sessions.RunnerConfig{
				ClaudeBin: "/opt/claude",
				WorkDir:   tt.workdir,
				SessionID: "sess-uuid",
			}).ClaudeSessionsDir
			if got != "" {
				t.Errorf("ClaudeSessionsDir = %q, want \"\" (probe inert)", got)
			}
		})
	}
}

// TestStreamClaudeSessionsDir_NoHome is the third degrade arm, split out because
// t.Setenv forbids t.Parallel. With $HOME unresolvable there is no
// ~/.claude/projects to key into, so sessions.DefaultClaudeSessionsDir returns ""
// and the probe stays inert rather than pointing somewhere relative to nothing.
func TestStreamClaudeSessionsDir_NoHome(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("HOME", "")

	if got := streamClaudeSessionsDir(workdir); got != "" {
		t.Errorf("streamClaudeSessionsDir(%q) with HOME unset = %q, want \"\"", workdir, got)
	}
}

// TestWithApprovalArgs is #2065 AC 3 plus the third row of its AC 4, pinned
// hermetically because the live-claude gate cannot see either: the rig there
// builds a streamsup.Config by hand and never calls this function, and its base
// args are empty so nothing there exercises the operator pass-through.
//
// The probe it pins used to be "does the spawn's argv name
// --dangerously-skip-permissions". #2065 puts that flag on EVERY argv, so the
// probe became "will this child keep the bypass it launches with", answered from
// the stored posture and the provenance bit rather than from the argv:
//
//   - a downgraded child → args followed by exactly permissionArgs(false, path),
//     minus that set's own mode pair when args already name a mode (#2043),
//   - a child that keeps its bypass → args UNCHANGED, no duplicate flag.
//
// # Which subtest kills which tree
//
// "a downgraded child still gets the approval set" is the row that reddens on any
// tree keying on the bare presence of the flag: its args CARRY the escalation, as
// every #2065 argv does, and the approval set is injected anyway. On a
// flag-presence tree it returns args unchanged and the daemon's approval gate is
// absent for every session in the fleet.
//
// "the operator's pass-through keeps its bypass" is the row that reddens on any
// tree that deletes the interlock and trusts the stored posture alone. Its args
// and stored mode are IDENTICAL to the row above; only operatorBypass differs, so
// the pair cannot both pass on either mistake.
//
// # Measured 2026-09-03, not predicted
//
// Both mutants applied through `go test -overlay`, so no mutated source was ever
// written into the worktree:
//
//	M3  keyed on slices.Contains(args, "--dangerously-skip-permissions")
//	    RED: "a downgraded child still gets the approval set" — and also the
//	    unrecognised-mode and no-aliasing subtests, since that tree returns args
//	    unchanged for every session
//	M4  the operatorBypass clause deleted
//	    RED: "the operator's pass-through keeps its bypass", alone
//
// It also pins that the input is never aliased or mutated — scfg.Args is freshly
// owned by mapStreamsupConfig and the append runs on a clone.
func TestWithApprovalArgs(t *testing.T) {
	t.Parallel()

	const path = "/tmp/pyry-mcp-approve-xyz.json"
	const bypass = "--dangerously-skip-permissions"

	countSkip := func(args []string) int {
		n := 0
		for _, a := range args {
			if a == bypass {
				n++
			}
		}
		return n
	}

	// The argv every #2065 session composes for the default posture: the
	// unconditional escalation flag, then the mode the daemon writes the child back
	// to. Spelled out rather than imported so this file states the shape it is
	// reasoning about.
	alwaysOn := []string{"--model", "haiku", "--settings", "p", bypass, "--permission-mode", "default"}

	t.Run("a downgraded child still gets the approval set", func(t *testing.T) {
		t.Parallel()
		got := withApprovalArgs(alwaysOn, path, "default", false)

		// permissionArgs' own --permission-mode default pair drops, because the argv
		// already names a mode — #2043's arm, now the common case.
		want := append(slices.Clone(alwaysOn), dropPermissionMode(permissionArgs(false, path))...)
		if !slices.Equal(got, want) {
			t.Fatalf("withApprovalArgs downgraded = %q, want %q", got, want)
		}
		for _, f := range []string{"--permission-prompt-tool", "--mcp-config", "--strict-mcp-config"} {
			if !slices.Contains(got, f) {
				t.Errorf("downgraded args %q missing %q: this child is walked back to default in-band "+
					"and has an approval gate to lose, so the whole enforcement set has to reach claude", got, f)
			}
		}
		if !slices.Contains(got, path) {
			t.Errorf("downgraded args %q missing the mcp-config path %q", got, path)
		}
		if n := countSkip(got); n != 1 {
			t.Errorf("downgraded args %q carry %d escalation flags, want exactly the one already in args", got, n)
		}
	})

	t.Run("the operator's pass-through keeps its bypass", func(t *testing.T) {
		t.Parallel()
		// Byte-identical args and stored mode to the subtest above. Only the
		// provenance differs, and it is the whole decision.
		got := withApprovalArgs(alwaysOn, path, "default", true)

		if !slices.Equal(got, alwaysOn) {
			t.Fatalf("withApprovalArgs operator-bypass = %q, want unchanged %q", got, alwaysOn)
		}
		for _, f := range []string{"--permission-prompt-tool", "--mcp-config", "--strict-mcp-config"} {
			if slices.Contains(got, f) {
				t.Errorf("operator-bypass args %q must not carry approval flag %q: the operator asked for "+
					"a bypass child and nothing downgrades it", got, f)
			}
		}
	})

	t.Run("a stored escalation keeps its bypass", func(t *testing.T) {
		t.Parallel()
		// What claudeSettingsArgs composes for a stored bypassPermissions: the flag
		// ALONE, no mode pair. streamsup's spawn path writes no posture for that
		// mode — non-membership in permissionModeSpawnWritable since #2066, which
		// subtracts the escalation back out of the writer's allow-list — so nothing
		// walks this child back either.
		in := []string{"--model", "haiku", bypass, "--settings", "p"}
		got := withApprovalArgs(in, path, sessions.PermissionModeBypass, false)

		if !slices.Equal(got, in) {
			t.Fatalf("withApprovalArgs stored-escalation = %q, want unchanged %q", got, in)
		}
		if n := countSkip(got); n != 1 {
			t.Errorf("stored-escalation args carry %d escalation flags, want exactly 1", n)
		}
		for _, f := range []string{"--permission-prompt-tool", "--mcp-config", "--strict-mcp-config"} {
			if slices.Contains(got, f) {
				t.Errorf("stored-escalation args %q must not carry approval flag %q", got, f)
			}
		}
	})

	countMode := func(args []string) int {
		n := 0
		for _, a := range args {
			if a == "--permission-mode" || strings.HasPrefix(a, "--permission-mode=") {
				n++
			}
		}
		return n
	}

	// #2043: a session storing a permission mode composes --permission-mode <mode>
	// through sessions.claudeSettingsArgs, and this function runs at runner
	// CONSTRUCTION on top of that — the path a daemon restart takes to rebuild a
	// session out of the registry. Injecting the set unmodified would spawn it as
	// "--permission-mode plan … --permission-mode default", and the last flag wins.
	//
	// The joined form is here because the operator's pass-through can spell the flag
	// either way, which the composed argv never does.
	t.Run("a spawn already naming a mode gets exactly one", func(t *testing.T) {
		t.Parallel()
		for _, in := range [][]string{
			{"--model", "haiku", bypass, "--permission-mode", "plan", "--settings", "p"},
			{"--permission-mode=plan", bypass, "--settings", "p"},
		} {
			got := withApprovalArgs(in, path, "plan", false)

			if n := countMode(got); n != 1 {
				t.Errorf("withApprovalArgs(%q) produced %d --permission-mode flags, want exactly 1: %q", in, n, got)
			}
			// The session's own mode is the one that survives; the injected default
			// is what drops.
			if slices.Contains(got, "default") {
				t.Errorf("withApprovalArgs(%q) kept the injected default mode: %q", in, got)
			}
			// It drops ONLY that pair. Returning args unchanged instead would spawn a
			// mode-carrying session with no permission-prompt tool and no mcp-config —
			// the daemon's approval gate absent, reachable from a stored setting. This
			// is the assertion that forbids that shortcut.
			for _, f := range []string{"--permission-prompt-tool", "--mcp-config", "--strict-mcp-config"} {
				if !slices.Contains(got, f) {
					t.Errorf("withApprovalArgs(%q) = %q dropped the approval flag %q", in, got, f)
				}
			}
			if !slices.Contains(got, path) {
				t.Errorf("withApprovalArgs(%q) = %q dropped the mcp-config path", in, got)
			}
			if n := countSkip(got); n != 1 {
				t.Errorf("withApprovalArgs(%q) = %q carries %d escalation flags, want exactly the one already in args", in, got, n)
			}
		}
	})

	// An unrecognised or empty stored mode falls THROUGH to injection: the fail-safe
	// direction for this consumer is "the approval gate is present". Deliberately not
	// symmetric with streamsup's predicate, which writes nothing for the same value —
	// see withApprovalArgs' doc.
	t.Run("an unrecognised stored mode still gets the approval set", func(t *testing.T) {
		t.Parallel()
		for _, mode := range []string{"", "notAMode"} {
			got := withApprovalArgs([]string{bypass}, path, mode, false)
			for _, f := range []string{"--permission-prompt-tool", "--mcp-config", "--strict-mcp-config"} {
				if !slices.Contains(got, f) {
					t.Errorf("stored mode %q produced %q, which is missing %q; an unexpected mode must "+
						"leave the approval gate present", mode, got, f)
				}
			}
		}
	})

	t.Run("does not mutate or alias the input on the injecting path", func(t *testing.T) {
		t.Parallel()
		in := []string{"--model", "haiku", bypass}
		saved := slices.Clone(in)
		got := withApprovalArgs(in, path, "default", false)

		if !slices.Equal(in, saved) {
			t.Errorf("withApprovalArgs mutated its input: got %q, want %q", in, saved)
		}
		// The append runs on a clone, so the returned slice must not share the
		// input's backing array.
		if len(got) > 0 && len(in) > 0 && &got[0] == &in[0] {
			t.Errorf("withApprovalArgs returned a slice aliasing the input backing array")
		}
	})
}

// TestStreamRunnerFactory_Construct drives the factory with the pool shapes and
// asserts a live *streamsup.Runner comes back wrapped in the streamRunner adapter
// (AC-1, AC-2 "constructs successfully at both sites"). A non-empty mcpApprovePath
// exercises the #1168 approval-arg injection seam; construction must succeed on
// every shape — non-yolo (flags injected) and yolo (nothing injected) alike.
// os.Args[0] is an absolute, resolvable path so exec.LookPath accepts it;
// t.TempDir() is a real work dir.
func TestStreamRunnerFactory_Construct(t *testing.T) {
	t.Parallel()

	factory := newStreamRunnerFactory(newStreamTurnSink(0, slog.Default()), "/tmp/pyry-mcp-approve-test.json")
	shapes := []struct {
		name string
		args []string
	}{
		{"bootstrap", []string{"--settings", "p"}},
		{"per-session", []string{"--session-id", "sess-uuid", "--settings", "p"}},
		{"yolo", []string{"--dangerously-skip-permissions", "--settings", "p"}},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			cfg := sessions.RunnerConfig{
				ClaudeBin:  os.Args[0],
				WorkDir:    t.TempDir(),
				SessionID:  "id-" + s.name,
				ClaudeArgs: s.args,
			}
			runner, err := factory(cfg)
			if err != nil {
				t.Fatalf("newStreamRunnerFactory(...)(%s) error = %v, want nil", s.name, err)
			}
			sr, ok := runner.(streamRunner)
			if !ok {
				t.Fatalf("runner is %T, want streamRunner", runner)
			}
			if sr.r == nil {
				t.Errorf("streamRunner.r is nil, want a constructed *streamsup.Runner")
			}
			// #1840: the factory carries the per-session model hold, and a runner with
			// no child yet reports the unreported state rather than a zero list.
			if sr.models == nil {
				t.Errorf("streamRunner.models is nil, want the hold newSessionParser minted")
			}
			if _, reported := sr.ModelList(); reported {
				t.Errorf("ModelList() ok = true on a freshly constructed runner, want false — no child has reported")
			}
			// #2004: the same for the slash-command retention, the second decorator on
			// the chain newSessionParser builds.
			if sr.commands == nil {
				t.Errorf("streamRunner.commands is nil, want the hold newSessionParser minted")
			}
			if _, reported := sr.SlashCommandList(); reported {
				t.Errorf("SlashCommandList() ok = true on a freshly constructed runner, want false — no child has reported")
			}
			// #2077: the same for the background-task retention, the third decorator.
			// The unreported assertion carries more weight on this variant than on the
			// two above: an empty roster is a REPORTED value here, so a runner with no
			// child answering ok = true would be claiming claude had said nothing is
			// alive rather than that nothing has been said at all.
			if sr.tasks == nil {
				t.Errorf("streamRunner.tasks is nil, want the hold newSessionParser minted")
			}
			if _, reported := sr.BackgroundTaskRoster(); reported {
				t.Errorf("BackgroundTaskRoster() ok = true on a freshly constructed runner, want false — no child has reported")
			}
		})
	}
}

// TestNewSessionParser_DecodesAndRetains drives the exact production composition:
// newSessionParser's parser consumes one real control_response initialize line and
// the hold it returned holds the decoded list, with the downstream sink still
// seeing the event. It is the only test in this suite that touches JSON, and what
// it proves is the WIRING — that the parser and the hold are two halves of one
// call — rather than the hold's own contract, which session_model_hold_test.go
// covers. The line's shape is streamsup's modelListLineFixture, copied rather than
// called: that helper is unexported in another package.
func TestNewSessionParser_DecodesAndRetains(t *testing.T) {
	t.Parallel()

	var next recordingSink
	parser, hold, _, _ := newSessionParser(next.sink, discardLogger())

	const line = `{"type":"control_response","response":{"subtype":"success","request_id":"init-1840",` +
		`"response":{"models":[` +
		`{"resolvedModel":"claude-opus-5","value":"opus","displayName":"Opus"},` +
		`{"resolvedModel":"claude-sonnet-5","value":"sonnet","displayName":"Sonnet"}]}}}`
	if _, err := parser.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("parser.Write(initialize reply) error = %v, want nil", err)
	}

	got, ok := hold.ModelList()
	if !ok {
		t.Fatalf("ModelList() ok = false after the initialize reply, want true")
	}
	if len(got.Models) != 2 {
		t.Fatalf("retained list carries %d entries, want the line's 2 — %#v", len(got.Models), got.Models)
	}
	if got.Models[0].Value != "opus" || got.Models[1].Value != "sonnet" {
		t.Errorf("retained values = %q, %q; want %q, %q",
			got.Models[0].Value, got.Models[1].Value, "opus", "sonnet")
	}
	if got.Models[0].ResolvedModel != "claude-opus-5" {
		t.Errorf("Models[0].ResolvedModel = %q, want %q", got.Models[0].ResolvedModel, "claude-opus-5")
	}

	seen := next.events()
	if len(seen) != 1 {
		t.Fatalf("downstream saw %d events, want the 1 ModelList forwarded through the hold", len(seen))
	}
	if _, isList := seen[0].(turnevent.ModelList); !isList {
		t.Errorf("downstream saw %T, want turnevent.ModelList", seen[0])
	}
}

// TestNewSessionParser_DecodesAndRetainsSlashCommands is the #2004 twin of the test
// above and proves the same thing for the second link of the chain: the parser
// newSessionParser returned consumes one real control_response initialize line and the
// slash-command hold it returned holds the decoded inventory, with the downstream sink
// still seeing the event.
//
// The line is COMMANDS-ONLY — no `models` key — so it takes the producer's
// commands-only rung and exercises this variant in isolation, rather than coupling
// this ticket's fixture to #1840's. Its keys are commandEntryLine's, claude's camelCase
// `argumentHint` among them, which is the one that differs from the daemon's own name
// for the field.
func TestNewSessionParser_DecodesAndRetainsSlashCommands(t *testing.T) {
	t.Parallel()

	var next recordingSink
	parser, _, hold, _ := newSessionParser(next.sink, discardLogger())

	const line = `{"type":"control_response","response":{"subtype":"success","request_id":"init-2004",` +
		`"response":{"commands":[` +
		`{"name":"clear","argumentHint":"","description":"Clear the transcript","aliases":["reset","new"]},` +
		`{"name":"compact","argumentHint":"[instructions]","description":"Compact the transcript"}]}}}`
	if _, err := parser.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("parser.Write(initialize reply) error = %v, want nil", err)
	}

	got, ok := hold.SlashCommandList()
	if !ok {
		t.Fatalf("SlashCommandList() ok = false after the initialize reply, want true")
	}
	if len(got.Commands) != 2 {
		t.Fatalf("retained list carries %d entries, want the line's 2 — %#v", len(got.Commands), got.Commands)
	}
	if got.Commands[0].Name != "clear" || got.Commands[1].Name != "compact" {
		t.Errorf("retained names = %q, %q; want %q, %q",
			got.Commands[0].Name, got.Commands[1].Name, "clear", "compact")
	}
	if !slices.Equal(got.Commands[0].Aliases, []string{"reset", "new"}) {
		t.Errorf("Commands[0].Aliases = %q, want the line's [reset new]", got.Commands[0].Aliases)
	}
	if got.Commands[1].ArgumentHint != "[instructions]" {
		t.Errorf("Commands[1].ArgumentHint = %q, want %q", got.Commands[1].ArgumentHint, "[instructions]")
	}

	seen := next.events()
	if len(seen) != 1 {
		t.Fatalf("downstream saw %d events, want the 1 SlashCommandList forwarded through the chain", len(seen))
	}
	if _, isList := seen[0].(turnevent.SlashCommandList); !isList {
		t.Errorf("downstream saw %T, want turnevent.SlashCommandList", seen[0])
	}
}

// TestNewSessionParser_DecodesAndRetainsBackgroundTaskRoster is the #2077 member of the
// pair above and proves the same thing for the third link of the chain: the parser
// newSessionParser returned consumes one real system/background_tasks_changed line and
// the background-task hold it returned holds the decoded roster, with the downstream
// sink still seeing the event.
//
// The line is the committed capture's own (internal/e2e/realclaude/testdata/
// dropped_lines_v2.1.220.json, its background_tasks_changed record), its keys copied
// rather than invented — including the two the decode target deliberately does not
// declare, uuid and session_id. Carrying them is the point: they are present in every
// real line, and a retention that somehow surfaced either would be caught here rather
// than downstream.
//
// It is also the only test in this package that drives the empty-roster path through
// the REAL decoder. The hold's own unit test constructs that state directly, which
// cannot show that the producer really does emit for an empty tasks array rather than
// returning early the way emitSlashCommandList does for its zero-length list — the
// difference AC 2 rests on.
func TestNewSessionParser_DecodesAndRetainsBackgroundTaskRoster(t *testing.T) {
	t.Parallel()

	var next recordingSink
	parser, _, _, hold := newSessionParser(next.sink, discardLogger())

	const line = `{"type":"system","subtype":"background_tasks_changed","tasks":[` +
		`{"task_id":"bybi8g8i8","task_type":"local_bash","description":"cat $FIFO"}],` +
		`"uuid":"702eb3a1-a939-43d8-b47d-e200e77712ae","session_id":"sess-2077"}`
	if _, err := parser.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("parser.Write(background_tasks_changed) error = %v, want nil", err)
	}

	got, ok := hold.BackgroundTaskRoster()
	if !ok {
		t.Fatalf("BackgroundTaskRoster() ok = false after the roster line, want true")
	}
	if len(got.Tasks) != 1 {
		t.Fatalf("retained roster carries %d entries, want the line's 1 — %#v", len(got.Tasks), got.Tasks)
	}
	if got.Tasks[0].TaskID != "bybi8g8i8" || got.Tasks[0].TaskType != "local_bash" {
		t.Errorf("retained entry = %q/%q, want the line's %q/%q",
			got.Tasks[0].TaskID, got.Tasks[0].TaskType, "bybi8g8i8", "local_bash")
	}
	if got.Tasks[0].Description != "cat $FIFO" {
		t.Errorf("Tasks[0].Description = %q, want the line's %q", got.Tasks[0].Description, "cat $FIFO")
	}
	if got.DroppedTasks != 0 {
		t.Errorf("DroppedTasks = %d for a one-entry line, want 0", got.DroppedTasks)
	}

	// The empty roster through the real producer: claude reports that the task
	// finished, and the retention must read back REPORTED-and-empty rather than
	// reverting to unreported.
	const emptyLine = `{"type":"system","subtype":"background_tasks_changed","tasks":[],` +
		`"uuid":"9f1c0b22-1c53-4b1a-9f5e-2a0d6d1b4e77","session_id":"sess-2077"}`
	if _, err := parser.Write([]byte(emptyLine + "\n")); err != nil {
		t.Fatalf("parser.Write(empty background_tasks_changed) error = %v, want nil", err)
	}
	empty, ok := hold.BackgroundTaskRoster()
	if !ok {
		t.Fatalf("BackgroundTaskRoster() ok = false after an empty roster line, want true — " +
			"the producer emits for an empty tasks array on purpose")
	}
	if len(empty.Tasks) != 0 {
		t.Errorf("retained roster carries %d entries after the empty line, want 0", len(empty.Tasks))
	}

	seen := next.events()
	if len(seen) != 2 {
		t.Fatalf("downstream saw %d events, want the 2 rosters forwarded through the chain", len(seen))
	}
	for i, ev := range seen {
		if _, isRoster := ev.(turnevent.BackgroundTaskRoster); !isRoster {
			t.Errorf("downstream event %d is %T, want turnevent.BackgroundTaskRoster", i, ev)
		}
	}
}

// Assert at compile time that streamRunner carries the shape #1857's resolver reaches
// by type assertion. ModelList is deliberately NOT on sessions.Runner — see the
// method's own doc — so this is the only compile-time statement of its existence,
// and the var _ sessions.Runner assertion in streamsup_runner.go is untouched.
// (The number is corrected here from #1867, which is retainedModelLists — the
// enumerator that CALLS that resolver, not the type assertion itself.)
var _ interface {
	ModelList() (turnevent.ModelList, bool)
} = streamRunner{}

// The #2004 twin, for the shape #2005 will reach by type assertion. It stands BESIDE
// the assertion above rather than being folded into one interface literal, so each
// ticket's asserted shape can be read, moved or removed on its own — and so a failure
// names which reader lost its shape.
var _ interface {
	SlashCommandList() (turnevent.SlashCommandList, bool)
} = streamRunner{}

// The #2077 member, for the shape #2079 will reach by type assertion. A third separate
// block for the reason the second one states — and this is the one that most needs it,
// since #2079 opens against a shape nothing else in the tree consumes yet: without this
// statement a roster would be retained that no compiler check says is reachable.
var _ interface {
	BackgroundTaskRoster() (turnevent.BackgroundTaskRoster, bool)
} = streamRunner{}

// TestStreamRunnerFactory_ErrorPropagation proves AC-3: a streamsup.New failure
// surfaces as (nil runner, non-nil error) with no silent PTY fallback. A missing
// binary fails streamsup.New's exec.LookPath; supervisor.New does NOT LookPath at
// construction, so a fallback to it would return (non-nil, nil) and fail here.
func TestStreamRunnerFactory_ErrorPropagation(t *testing.T) {
	t.Parallel()

	cfg := sessions.RunnerConfig{
		ClaudeBin:  "pyry-nonexistent-binary-xyz",
		WorkDir:    t.TempDir(),
		SessionID:  "sess-uuid",
		ClaudeArgs: []string{"--settings", "p"},
	}
	runner, err := newStreamRunnerFactory(newStreamTurnSink(0, slog.Default()), "")(cfg)
	if err == nil {
		t.Fatalf("newStreamRunnerFactory error = nil, want non-nil for a missing binary")
	}
	if runner != nil {
		t.Errorf("runner = %v, want nil (no silent PTY fallback)", runner)
	}
}

// TestMapStreamsupConfig_CarriesPermissionMode (#2064): the stored posture crosses
// the mapper as a plain value, and its runtime partner does NOT — the same dividing
// line ClaudeSessionsDir and Stdout already sit on either side of.
//
// bypassPermissions is carried rather than filtered here, and that is deliberate: the
// mapper normalises nothing, and the refusal that matters is the runner's closed
// allow-list at the spawn. Filtering here would be a second vocabulary to keep in step
// with that one.
func TestMapStreamsupConfig_CarriesPermissionMode(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"default", "plan", "bypassPermissions", ""} {
		got := mapStreamsupConfig(sessions.RunnerConfig{
			ClaudeBin:      "/opt/claude",
			WorkDir:        "/work",
			SessionID:      "sess-uuid",
			PermissionMode: mode,
		})
		if got.SpawnPermissionMode != mode {
			t.Errorf("SpawnPermissionMode = %q, want %q", got.SpawnPermissionMode, mode)
		}
		if got.PostureGate != nil {
			t.Errorf("PostureGate = %v, want nil — it is a runtime object installed in newStreamRunnerFactory, not the pure mapper", got.PostureGate)
		}
	}
}

// TestMapStreamsupConfig_CarriesOperatorBypass (#2065): the provenance of the
// escalation crosses the mapper as a plain bool, beside the posture it qualifies.
//
// Both halves have to arrive for the runner's spawn decision to be right, and only
// one of them is derivable from anything else the config carries — hence a test that
// names the pair rather than the field. The bit is NOT re-derived here from
// ClaudeArgs: since #2065 every composed argv carries the flag, so a mapper reading
// the argv would answer true for every session and every child would keep the bypass
// it launched with.
func TestMapStreamsupConfig_CarriesOperatorBypass(t *testing.T) {
	t.Parallel()
	for _, operatorBypass := range []bool{false, true} {
		got := mapStreamsupConfig(sessions.RunnerConfig{
			ClaudeBin:      "/opt/claude",
			WorkDir:        "/work",
			SessionID:      "sess-uuid",
			PermissionMode: "default",
			// The argv shape #2065 composes for every session. It disagrees with the
			// false arm below, which is what makes that arm a real assertion: a mapper
			// reading the argv could not produce it.
			ClaudeArgs:     []string{"--dangerously-skip-permissions", "--permission-mode", "default"},
			OperatorBypass: operatorBypass,
		})
		if got.OperatorBypass != operatorBypass {
			t.Errorf("OperatorBypass = %v, want %v", got.OperatorBypass, operatorBypass)
		}
		if got.SpawnPermissionMode != "default" {
			t.Errorf("SpawnPermissionMode = %q, want %q — the posture must cross beside its provenance", got.SpawnPermissionMode, "default")
		}
	}
}

// TestSessionParser_MintsOneStablePostureGate (#2064) pins the parser half of the
// binding the whole correlation rests on: the gate the runner ARMS must be the very
// object the parser RELEASES, so PostureGate() has to mint one gate and keep handing
// back that same object. Asserting non-nil would pass against a fresh gate per call,
// under which every turn would be refused forever — so the assertion is identity.
//
// It is named for what it pins rather than for the factory, deliberately. The obvious
// name would say newStreamRunnerFactory, but streamsup.Config is not observable from
// the runner the factory returns, so the far half of the binding cannot be asserted
// from this package at all; the comment in the body names what carries it instead.
func TestSessionParser_MintsOneStablePostureGate(t *testing.T) {
	t.Parallel()
	parser, _, _, _ := newSessionParser(func(turnevent.Event) {}, nil)
	gate := parser.PostureGate()
	if gate == nil {
		t.Fatal("newSessionParser's parser minted no posture gate")
	}
	if second := parser.PostureGate(); second != gate {
		t.Error("PostureGate() returned a different object on the second call; the runner and the parser would hold different gates")
	}
	// The gate's own transitions are unexported and belong to internal/streamsup's
	// tests. What this package owns is the BINDING, and its end-to-end proof is the
	// fake-daemon suite: every stream session there now arms this gate, so a factory
	// that handed the runner a gate the parser does not release would refuse every
	// turn in every one of those tests rather than showing up as one missed assertion.
}
