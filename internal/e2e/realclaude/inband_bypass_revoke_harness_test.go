//go:build e2e_realclaude

package realclaude

// #1673 — the per-arm Pool harness #1675's live three-arm probe consumes, and the
// proof that all three arms' seeded identity, stored posture and composed spawn
// posture are what that probe assumes, BEFORE any child process exists.
//
// Everything here runs OFFLINE: no live claude, no daemon, no subprocess, no
// credential. finOfflineExecBans enforces that over this file's AST rather than over
// this paragraph. Read the RUN count, never the exit code — `make check` never
// compiles this package, and the suite exits 0 both on a build failure and on a full
// credentials skip.
//
//	go test -tags e2e_realclaude -race -count=1 -v \
//	  -run TestPoolRevokeHarness_ ./internal/e2e/realclaude/
//
// # Why the whole harness is reachable with no child
//
// Pool.New composes the bootstrap argv and runs the RunnerFactory INSIDE New, so the
// factory runs and its RunnerConfig is readable before anything spawns. streamsup.New
// performs no spawn either — the child is exec'd inside Runner.Run, which nothing here
// calls. A pool built through the real factory therefore reaches Pool.BootstrapID,
// Pool.DefaultSettings, Pool.Default().Runner() and the whole captured RunnerConfig
// for free.
//
// # What the argv says since #2065, and what it no longer says
//
// claudeSettingsArgs appends --dangerously-skip-permissions UNCONDITIONALLY: every
// child the daemon spawns launches escalated, and the posture it actually runs in is
// decided by a spawn-time in-band write. So the launch argv no longer discriminates
// the arms, and two RunnerConfig fields carry what it used to:
//
//   - PermissionMode, the canonical stored posture, per arm.
//   - OperatorBypass, provenance: true means the escalation came from the OPERATOR's
//     pass-through claude args and the daemon may NOT walk this child back. A true on
//     the revoke arm would leave #1675's revocation unperformable while every other
//     pre-spawn reading here stayed green.
//
// Those two are the most load-bearing readings this file takes, and they cost two
// comparisons.
//
// # Why the identity reading is not redundant with the posture one
//
// loadRegistry returns (nil, nil) for an absent file, so a total cold start reaches
// canonicalSettings with a zero SessionSettings and comes out {YOLO:false,
// PermissionMode:"default"} — indistinguishable from a correctly seeded
// control_default. A cold start mints a FRESH bootstrap id, so it cannot equal the id
// seedBypassRegistry returned. On that arm the identity reading is the only one of the
// two that can tell a working seed from a cold start.
//
// # Evidence: which assertion carries which proof
//
// Measured 2026-09-03 against d4e3f5a9, applied through
// `go test -overlay=<abs>/overlay.json` so no mutated source was written into the
// worktree. Every measured red set equals the one predicted for it.
//
// M1 — claudeSettingsArgs stops appending bypassPermissionsArg. Sole red is the
// unconditional-flag clause, on ALL THREE arms. control_default's --permission-mode
// pair, both RunnerConfig fields and both AC 2 readings stay green, which is what
// earns asserting that flag unconditionally rather than iff launchYOLO.
//
// M2 — pickBootstrap returns nil, i.e. every arm takes the cold-start path. This is
// the row that earns the identity reading. On the two YOLO arms five assertions
// redden; on CONTROL_DEFAULT exactly ONE does — the BootstrapID comparison — because
// the cold start's canonicalised zero settings are {YOLO:false, PermissionMode:
// "default"} and its argv carries --permission-mode default, i.e. every posture
// reading is byte-identical to a correct seed's. Drop that comparison and a
// control_default arm whose seed the pool never read passes in full.
//
// M3 — operatorBypass returns true unconditionally. Sole red is the OperatorBypass
// clause, on all three arms; nothing else moves. That is the whole point of the
// field: it is invisible in the argv, in the stored posture and in the seeded id.

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// The two permission-mode strings, as LITERALS. internal/sessions exports only the
// escalation (sessions.PermissionModeBypass) and keeps the default unexported, and
// both are claude's own vocabulary rather than this package's — so pinning both as
// literals keeps a rename inside internal/sessions from silently retargeting these
// assertions at whatever the constant became.
const (
	revokeModeBypass  = "bypassPermissions"
	revokeModeDefault = "default"
)

// --- the harness -------------------------------------------------------------

// poolRevokeHarness is one arm's live Pool plus every handle a later live driver
// needs to observe it. Pool.Run is NOT called on it: the session lifecycle
// goroutine's only job here would be to run the supervisor, and skipping it also
// skips the conversations sweep, the idle timer and the settings-file reaper, none of
// which anything below consults. One consequence to be aware of and not to "fix": the
// per-session settings file is removed when Pool.Run returns, so an un-Run pool leaves
// it behind — harmless, because it lives under this arm's own t.TempDir().
type poolRevokeHarness struct {
	// arm is the row this harness was built from.
	arm poolRevokeArm

	pool *sessions.Pool

	// runner is the runner the factory built, held CONCRETE rather than as
	// sessions.Runner so a live driver reaches Stdin() and the embedded runner's own
	// State(). newPoolRevokeHarness proves it is the runner Pool.Default() reports.
	runner inbandRunner

	// runnerCfg is the whole RunnerConfig the factory received — the pre-spawn
	// posture, captured upstream of anything the factory does with it.
	runnerCfg sessions.RunnerConfig

	tap          *revokeTap
	spawns       func() int
	notDelivered func() []string

	// registryPath is this arm's own seeded sessions.json, and seededID is the id
	// seedBypassRegistry minted into it.
	registryPath string
	seededID     sessions.SessionID
}

// newPoolRevokeHarness seeds one arm's registry at its own fresh path, wires the tap
// and the log recorder into a real streamsup runner through the real Pool factory
// seam, and returns the pool with every observation handle. No child is spawned.
//
// claudeBin and workdir are PARAMETERS rather than this package's own helpers because
// #1675 supplies a live pair — resolveClaudeBin's result and a workdir under
// WithWorktreeAuthenticated's pinned $HOME — and this file may reach neither name.
// Both are load-bearing at construction: streamsup.New refuses an unresolvable
// ClaudeBin AND a WorkDir that is not on disk (it runs the path through
// filepath.EvalSymlinks), so a caller that inherits this shape without supplying a
// real directory gets an error out of sessions.New rather than a pool.
//
// The launch posture reaches the pool through the seeded registry entry and NOWHERE
// else. revokeBaseArgs stays empty: a flag baked into the base spawn argv survives
// Session.spawnArgs' recompose, so a later run would measure a child nobody revoked.
func newPoolRevokeHarness(t *testing.T, arm poolRevokeArm, claudeBin, workdir string) poolRevokeHarness {
	t.Helper()

	// A fresh directory per call. t.TempDir returns a unique path on every call
	// against the same *testing.T, which is what makes the arms' registry paths —
	// and therefore their composed --settings paths — distinct by construction
	// rather than by convention.
	registryPath := filepath.Join(t.TempDir(), "sessions.json")

	// BEFORE sessions.New: the warm-start seam reads the file at construction time.
	seededID := seedBypassRegistry(t, registryPath, arm.launchYOLO)

	tap := newRevokeTap()
	logHandler, spawns, notDelivered := newRevokeLogRecorder()

	// The factory captures BOTH halves: the runner, so a driver can address the very
	// object the pool holds, and the whole RunnerConfig, which is the pre-spawn
	// posture this file asserts on. It runs synchronously inside sessions.New, on
	// this goroutine, so neither capture needs a lock.
	//
	// This mirrors #1622's factory exactly, and that includes what it does NOT set:
	// streamsup.Config.SpawnPermissionMode and PostureGate, which cmd/pyry's
	// production factory does wire (the gate is minted by the turnevent parser, whose
	// slot the tap occupies here, and streamsup.New refuses a mode with a nil gate).
	// Nothing this file asserts is affected — every reading is taken from the config
	// the factory RECEIVED. #1675 must decide that pairing deliberately: since every
	// arm now launches escalated, its control_default is a control by virtue of the
	// spawn-time in-band write and not by virtue of its argv.
	var (
		captured    inbandRunner
		capturedCfg sessions.RunnerConfig
	)
	factory := func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
		r, err := streamsup.New(streamsup.Config{
			ClaudeBin: cfg.ClaudeBin,
			WorkDir:   cfg.WorkDir,
			SessionID: cfg.SessionID,
			Args:      cfg.ClaudeArgs,
			Stdout:    tap,
			Logger:    cfg.Logger,
		})
		if err != nil {
			return nil, fmt.Errorf("#1673: stream runner for arm %q: %w", arm.name, err)
		}
		captured = inbandRunner{Runner: r}
		capturedCfg = cfg
		return captured, nil
	}

	pool, err := sessions.New(sessions.Config{
		Bootstrap: sessions.SessionConfig{
			ClaudeBin:  claudeBin,
			WorkDir:    workdir,
			ClaudeArgs: revokeBaseArgs,
		},
		RegistryPath:  registryPath,
		RunnerFactory: factory,
		Logger:        slog.New(logHandler),
	})
	if err != nil {
		t.Fatalf("#1673: sessions.New for arm %q over registry %s: %v", arm.name, registryPath, err)
	}

	// Instrument check A, for EVERY arm — #1622 ran it on the true arm only. Without
	// it the harness can hand a later driver a bystander runner while every reading
	// below still passes, because none of them consult the runner.
	if got := pool.Default().Runner(); got != sessions.Runner(captured) {
		t.Fatalf("#1673: arm %q: pool.Default().Runner() is not the runner the factory built "+
			"(%T vs %T); a driver handed this harness would observe a different child than "+
			"the pool drives", arm.name, got, captured)
	}

	return poolRevokeHarness{
		arm:          arm,
		pool:         pool,
		runner:       captured,
		runnerCfg:    capturedCfg,
		tap:          tap,
		spawns:       spawns,
		notDelivered: notDelivered,
		registryPath: registryPath,
		seededID:     seededID,
	}
}

// newInertClaudeStub returns the two live-consumer values this OFFLINE file supplies
// for itself: a claude binary path exec.LookPath resolves, and a workdir that exists.
// One t.TempDir() serves both.
//
// The stub is an EMPTY file at 0755, and both halves of that matter. exec.LookPath
// accepts any regular file with mode&0111 != 0 on a path containing a separator, so
// even a 077 umask leaves the owner execute bit and streamsup.New is satisfied. And
// empty means an accidental exec FAILS rather than runs: nothing here calls
// Runner.Run, but that is a property of today's code rather than something the ban
// table can enforce, so the stub is chosen to fail closed if a future edit adds a
// spawn. Do not "improve" it into a real script, and do not reach for the test binary
// — this package carries a fork-bomb defense against exactly that shape.
func newInertClaudeStub(t *testing.T) (claudeBin, workdir string) {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "claude")
	if err := os.WriteFile(stub, nil, 0o755); err != nil {
		t.Fatalf("#1673: write inert claude stub at %s: %v; streamsup.New resolves ClaudeBin "+
			"through exec.LookPath and errors without it", stub, err)
	}
	return stub, dir
}

// --- reading the composed argv -------------------------------------------------

// revokeArgvFlagIndices returns every index at which flag appears in args. One helper
// backs all of AC 3's argv clauses — exactly-one, present, absent, and
// value-of-the-next-element — so each reads as its own flat assertion rather than as a
// branch nested inside another's guard. A nested assertion is never the sole red for a
// mutant that trips its guard; the guard is (#1651's mutation lesson).
func revokeArgvFlagIndices(args []string, flag string) []int {
	var at []int
	for i, a := range args {
		if a == flag {
			at = append(at, i)
		}
	}
	return at
}

// revokeWantPermissionMode is the canonical posture Pool.New stores for a row.
// canonicalPermissionMode turns a seeded yolo:true into the escalation and ANY
// non-escalating seed — including the zero value a cold start carries — into default.
func revokeWantPermissionMode(arm poolRevokeArm) string {
	if arm.launchYOLO {
		return revokeModeBypass
	}
	return revokeModeDefault
}

// revokeWantSettingsDir is the directory writeMCPSettings writes this arm's settings
// file into, derived from the registry path and from NOTHING the runner reports back.
// Two traps live here, and each would redden all three arms for the wrong reason:
//
//   - The file lands one level DEEPER than the registry directory, in session-settings/.
//   - writeMCPSettings derives that directory with filepath.Abs and NO symlink
//     resolution, while the workdir goes through EvalSymlinks. On macOS t.TempDir()
//     sits under the /var -> /private/var symlink, so the two are different spellings
//     of one directory and a check built from anything the runner resolved is red.
func revokeWantSettingsDir(t *testing.T, registryPath string) string {
	t.Helper()
	dataDir, err := filepath.Abs(filepath.Dir(registryPath))
	if err != nil {
		t.Fatalf("#1673: absolutise the registry directory of %s: %v", registryPath, err)
	}
	return filepath.Join(dataDir, "session-settings")
}

// composedSettingsPath returns the path following this arm's single --settings flag.
func (h poolRevokeHarness) composedSettingsPath(t *testing.T) string {
	t.Helper()
	at := revokeArgvFlagIndices(h.runnerCfg.ClaudeArgs, "--settings")
	if len(at) != 1 || at[0]+1 >= len(h.runnerCfg.ClaudeArgs) {
		t.Fatalf("#1673: arm %q composed %q, which carries no single --settings pair",
			h.arm.name, h.runnerCfg.ClaudeArgs)
	}
	return h.runnerCfg.ClaudeArgs[at[0]+1]
}

// --- the test ------------------------------------------------------------------

// The five flags no arm may carry. Each would change what a later behavioural
// comparison measures: an approval tool or an MCP config changes how tools are gated,
// and a model or effort pins a machine default the probe deliberately leaves alone.
var revokeForbiddenFlags = []string{
	"--permission-prompt-tool", "--mcp-config", "--strict-mcp-config", "--model", "--effort",
}

// TestPoolRevokeHarness_PinsSeededPostureAndArgvBeforeAnySpawn builds all three arms'
// harnesses in ONE scope — distinctness is a cross-arm comparison three isolated
// parallel subtests could not make — and pins, for every arm and with no child
// process in existence, the seeded identity, the stored posture, the composed
// pre-spawn posture the factory received, and the arms' mutual isolation.
func TestPoolRevokeHarness_PinsSeededPostureAndArgvBeforeAnySpawn(t *testing.T) {
	t.Parallel()

	harnesses := make([]poolRevokeHarness, 0, len(poolRevokeArms))
	for _, arm := range poolRevokeArms {
		claudeBin, workdir := newInertClaudeStub(t)
		harnesses = append(harnesses, newPoolRevokeHarness(t, arm, claudeBin, workdir))
	}

	for _, h := range harnesses {
		t.Run(h.arm.name, func(t *testing.T) {
			h.assertSeededIdentityAndPosture(t)
			h.assertPreSpawnPosture(t)
		})
	}

	t.Run("the three arms are mutually isolated", func(t *testing.T) {
		for i, a := range harnesses {
			for _, b := range harnesses[i+1:] {
				if a.seededID == b.seededID {
					t.Errorf("arms %q and %q were seeded with the same bootstrap id %s; they would "+
						"hand claude one --session-id and share one transcript",
						a.arm.name, b.arm.name, a.seededID)
				}
				if a.registryPath == b.registryPath {
					t.Errorf("arms %q and %q share the registry path %s; the second seed overwrites "+
						"the first and both arms launch under the LAST posture written",
						a.arm.name, b.arm.name, a.registryPath)
				}
				if pa, pb := a.composedSettingsPath(t), b.composedSettingsPath(t); pa == pb {
					t.Errorf("arms %q and %q compose the same --settings path %s; one arm's settings "+
						"file is then whatever the other wrote last", a.arm.name, b.arm.name, pa)
				}
			}
		}
	})
}

// assertSeededIdentityAndPosture is AC 2: the two readings that say the seed reached
// the pool, asserted separately because they fail for different reasons. A red on
// identity is a seed the pool never READ; a red on posture is a seed it read carrying
// the wrong value.
func (h poolRevokeHarness) assertSeededIdentityAndPosture(t *testing.T) {
	t.Helper()

	settings, ok := h.pool.DefaultSettings()

	if got := h.pool.BootstrapID(); got != h.seededID {
		t.Errorf("arm %q: pool.BootstrapID() is %s, want the seeded %s (settings %+v, ok=%v, "+
			"registry %s); the pool minted a FRESH id, so it took the cold-start path and never "+
			"read the seeded entry. On control_default this is the only one of the two readings "+
			"that can tell a working seed from a cold start — loadRegistry returns (nil, nil) for "+
			"an absent file and the zero settings canonicalise to exactly the {false, default} a "+
			"correct seed produces", h.arm.name, got, h.seededID, settings, ok, h.registryPath)
	}

	if !ok {
		t.Fatalf("arm %q: pool.DefaultSettings() reports no bootstrap session (seeded %s at %s); "+
			"there is no stored posture to read at all", h.arm.name, h.seededID, h.registryPath)
	}
	if settings.YOLO != h.arm.launchYOLO {
		t.Errorf("arm %q: pool.DefaultSettings() reports YOLO %t, want %t (settings %+v, seeded %s "+
			"at %s); the arm would launch under the OPPOSITE stored posture",
			h.arm.name, settings.YOLO, h.arm.launchYOLO, settings, h.seededID, h.registryPath)
	}
	if want := revokeWantPermissionMode(h.arm); settings.PermissionMode != want {
		t.Errorf("arm %q: pool.DefaultSettings() reports PermissionMode %q, want %q (settings %+v, "+
			"seeded %s at %s); the posture the daemon asserts to the child at spawn is taken from "+
			"this value", h.arm.name, settings.PermissionMode, want, settings, h.seededID, h.registryPath)
	}
}

// assertPreSpawnPosture is AC 3: the posture the factory received, pinned BY SHAPE.
// Never by equality against a whole-slice literal — the settings path is per-arm and
// per-run, so a literal could only be written by re-deriving what the code did.
func (h poolRevokeHarness) assertPreSpawnPosture(t *testing.T) {
	t.Helper()
	args := h.runnerCfg.ClaudeArgs

	// --settings: exactly one, followed by a path in this arm's OWN settings dir.
	switch at := revokeArgvFlagIndices(args, "--settings"); {
	case len(at) != 1:
		t.Errorf("arm %q composed %d --settings flags in %q, want exactly 1; Pool.New splices one "+
			"pair into the settings-free base and a second would let claude decide which wins",
			h.arm.name, len(at), args)
	case at[0]+1 >= len(args):
		t.Errorf("arm %q composed a trailing --settings with no path in %q", h.arm.name, args)
	default:
		got, want := filepath.Dir(args[at[0]+1]), revokeWantSettingsDir(t, h.registryPath)
		if got != want {
			t.Errorf("arm %q composed --settings %s, whose directory is %s, want %s (registry %s); "+
				"the arms would share a settings file, so one arm's pre-approvals would be "+
				"whatever another arm wrote last", h.arm.name, args[at[0]+1], got, want, h.registryPath)
		}
	}

	// --dangerously-skip-permissions: on EVERY arm, control_default included, and
	// asserted unconditionally rather than iff launchYOLO. Since #2065
	// claudeSettingsArgs appends it to every composition; the flag no longer says
	// which posture a child RUNS in, only that the daemon composed one. Do not
	// "correct" this back to an iff form.
	if at := revokeArgvFlagIndices(args, "--dangerously-skip-permissions"); len(at) == 0 {
		t.Errorf("arm %q composed %q, which carries no --dangerously-skip-permissions; since #2065 "+
			"claudeSettingsArgs appends it UNCONDITIONALLY, so its absence means the composition "+
			"this arm launches from is not the one the daemon ships", h.arm.name, args)
	}

	// --permission-mode default iff the row is not YOLO. A YOLO row's escalation has
	// exactly one spelling — the flag above — and no mode string can produce it.
	modeAt := revokeArgvFlagIndices(args, "--permission-mode")
	switch {
	case h.arm.launchYOLO && len(modeAt) != 0:
		t.Errorf("arm %q composed %q, which carries --permission-mode; a bypass posture reaches the "+
			"argv ONLY as --dangerously-skip-permissions, so a mode pair here means the stored "+
			"posture is not the escalation this arm was seeded with", h.arm.name, args)
	case !h.arm.launchYOLO && len(modeAt) != 1:
		t.Errorf("arm %q composed %d --permission-mode flags in %q, want exactly 1; since #2065 a "+
			"non-YOLO posture is NAMED on the argv, because beside an unconditional escalation "+
			"flag an unnamed posture reads as the escalation", h.arm.name, len(modeAt), args)
	case !h.arm.launchYOLO:
		if got := args[modeAt[0]+1]; got != revokeModeDefault {
			t.Errorf("arm %q composed --permission-mode %q in %q, want %q",
				h.arm.name, got, args, revokeModeDefault)
		}
	}

	for _, flag := range revokeForbiddenFlags {
		if at := revokeArgvFlagIndices(args, flag); len(at) != 0 {
			t.Errorf("arm %q composed %q, which carries %s; every arm must reach claude on the same "+
				"tool-gating and model configuration, or a later behavioural comparison is "+
				"measuring that flag instead", h.arm.name, args, flag)
		}
	}

	if want := revokeWantPermissionMode(h.arm); h.runnerCfg.PermissionMode != want {
		t.Errorf("arm %q: RunnerConfig.PermissionMode is %q, want %q; this is the posture the runner "+
			"asserts to every child it spawns, and since #2065 it — not the argv — is what says "+
			"which posture the child runs in", h.arm.name, h.runnerCfg.PermissionMode, want)
	}

	if h.runnerCfg.OperatorBypass {
		t.Errorf("arm %q: RunnerConfig.OperatorBypass is true over base argv %q, want false; true "+
			"means the escalation reached this spawn from the OPERATOR's pass-through args and the "+
			"daemon may NOT walk this child back — a revocation would then be unperformable while "+
			"every other reading here stayed green", h.arm.name, revokeBaseArgs)
	}
}
