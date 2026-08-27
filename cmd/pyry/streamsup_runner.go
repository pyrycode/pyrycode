package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
)

// streamRunner adapts *streamsup.Runner to sessions.Runner. The shapes differ in
// exactly one method: sessions.Runner.State returns supervisor.State, but
// internal/streamsup must not import internal/supervisor (its package doc forbids
// it), so *streamsup.Runner declares State() streamsup.State instead. Go has no
// covariant return on interface satisfaction, so the concrete runner cannot
// satisfy the interface directly. This adapter — living in cmd/pyry, which knows
// both types — maps that one method and forwards the rest unchanged. The
// precedent for this covariant-return seam is poolResolver in main.go.
//
// The factory that constructs a streamRunner from a supervisor.Config —
// newStreamRunnerFactory below (#1109 delivered the constructor; #1098 gave it
// the turnevent sink) — the interactive_runner selection that injects it on
// sessions.Config.RunnerFactory is #1081.
type streamRunner struct{ r *streamsup.Runner }

func (a streamRunner) State() sessions.State { return mapStreamState(a.r.State()) }

func (a streamRunner) WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error {
	return a.r.WriteUserTurn(ctx, conversationID, payload)
}

func (a streamRunner) WaitForPTY(ctx context.Context) error { return a.r.WaitForPTY(ctx) }

func (a streamRunner) Run(ctx context.Context) error { return a.r.Run(ctx) }

func (a streamRunner) Restart(args []string) { a.r.Restart(args) }

// SetSpawnArgs forwards to (*streamsup.Runner).SetSpawnArgs (#1580), installing
// the next spawn's argv without terminating the running child. Unlike Interrupt /
// RestartFresh / BeginRotation below, it is ON the sessions.Runner interface: that
// interface has exactly one production implementation (this adapter) and five test
// doubles, all in this repo, so widening is compile-checked across the whole set —
// whereas a type assertion at a future call site would fail silently at runtime and
// fall back to Restart, which is the one outcome a swap-only caller exists to
// avoid. Dispatched from the in-band branch of Pool.UpdateSettings since #1581.
func (a streamRunner) SetSpawnArgs(args []string) { a.r.SetSpawnArgs(args) }

// RevokeBypass forwards to (*streamsup.Runner).RevokeBypass (#1603), dropping the
// live child's bypass posture via a set_permission_mode control request rather
// than a respawn. Like SetSpawnArgs it is ON the sessions.Runner interface, for
// exactly the fail-open reason stated above: its consumer is Pool.UpdateSettings
// inside internal/sessions, and an assertion there whose unmatched arm silently
// no-ops would leave the posture un-revoked while the update reports success —
// the one outcome a live revocation exists to prevent. Dispatched by #1604.
func (a streamRunner) RevokeBypass() error { return a.r.RevokeBypass() }

// Interrupt forwards to (*streamsup.Runner).Interrupt (#1120), ending the running
// turn via a control_request line. It is OFF the sessions.Runner interface (which
// stays un-widened, #1077) — a concrete method the #1121 interrupt dispatch
// (interruptRunner in main.go) reaches by type assertion.
func (a streamRunner) Interrupt() error { return a.r.Interrupt() }

// RestartFresh forwards to (*streamsup.Runner).RestartFresh (#1124), rotating the
// runner's persistent id to newID so the next spawn uses --session-id <newID> (a
// fresh transcript, no fork). Like Interrupt it is OFF the sessions.Runner
// interface (un-widened, #1077) — a concrete method the #1125 new_session dispatch
// (startFreshRunner in main.go) reaches by type assertion.
func (a streamRunner) RestartFresh(newID string) { a.r.RestartFresh(newID) }

// BeginRotation forwards to (*streamsup.Runner).BeginRotation (#1330), arming the
// rotation gate so no turn accepted once the rotation has begun is written into
// the outgoing child, and returning the disarm for startFreshRunner's rotate-error
// path. It is the THIRD concrete method reached by type assertion off the
// un-widened sessions.Runner (#1077), after Interrupt (#1120) and RestartFresh
// (#1124) — but the only OPTIONAL one: startFreshRunner asserts for it separately
// (beginRotationOrNoop) rather than widening the RestartFresh case, so a runner
// that exposes RestartFresh without a gate keeps today's dispatch exactly.
func (a streamRunner) BeginRotation() func() { return a.r.BeginRotation() }

// mapStreamState maps streamsup's native lifecycle snapshot to supervisor.State.
// The two types mirror each other field-for-field; Phase maps by a plain string
// conversion because the phase values are identical across the two packages.
func mapStreamState(s streamsup.State) sessions.State {
	return sessions.State{
		Phase:        sessions.Phase(string(s.Phase)),
		ChildPID:     s.ChildPID,
		StartedAt:    s.StartedAt,
		RestartCount: s.RestartCount,
		LastUptime:   s.LastUptime,
		NextBackoff:  s.NextBackoff,
	}
}

// newStreamRunnerFactory returns a sessions.RunnerFactory (func(supervisor.Config)
// (sessions.Runner, error)) that constructs a stream-json runner AND installs the
// #1088 turnevent Parser as its Config.Stdout, with the Parser's sink bound to
// sink for the runner's session (#1098). The factory captures the
// daemon-singleton fan-in sink once; each per-session invocation binds a fresh
// Parser to sink.sinkFor(cfg.SessionID), so every runner's turnevents fan into
// the one drain tagged by their pool session id. The install lives HERE, not in
// mapStreamsupConfig, so the mapper stays pure (its Stdout == nil assertion is
// untouched) — the Parser is a runtime object, one layer up.
//
// #1210 installs the SECOND lane onto the same fan-in: sink.exitFor(cfg.SessionID)
// as the runner's child-exit callback, which closes the turn of a conversation
// whose child died mid-turn (no result line for the abandoned turn, and no pool
// transition — the two feeds that are structurally silent on that path). The two
// installs bind from the ONE cfg.SessionID and sit adjacent on purpose: identical
// session tags on both lanes are what let the drain's exit arm clear exactly the
// conversation whose events it is ordered behind, and reading them as a pair is
// the evidence, not a derivation. exitFor is INSTALLED rather than re-derived —
// it owns the non-blocking send and the Warn drop diagnostic the seam's
// must-not-block / must-not-panic contract requires, so a hand-rolled func() here
// would duplicate that contract instead of consuming it.
//
// #1109 constructed the runner via streamsup.New (the first caller tree-wide) and
// deliberately left Config.Stdout nil for this ticket to fill. It is the arm the
// #1081 interactive_runner selection assigns to sessions.Config.RunnerFactory;
// this ticket delivers the factory + the drain it feeds, not the production wiring.
//
// There is NO silent PTY fallback: a streamsup.New error (empty SessionID —
// impossible at the pool sites per #1108; missing binary via exec.LookPath;
// absent WorkDir via agentrun.ResolveWorkdir) is wrapped and returned with a nil
// runner. Substituting supervisor.New here would silently diverge the primary
// interactive session (which `pyry attach` drives) from the operator's stated
// stream-json intent. The error surfaces through the pool's existing
// "sessions: … supervisor: %w" wraps at both construction sites.
//
// mcpApprovePath is the daemon-global --mcp-config file written once at startup
// (runSupervisor, gated on stream-json). withApprovalArgs injects the #1106
// permission-approval flags onto every non-yolo spawn's args (#1168) — this is
// the sole stream-path-specific spawn-construction site and covers both the
// bootstrap runner and per-conversation runners, so a per-conversation stream
// session cannot silently bypass the approval gate. On the "" / "pty" path the
// factory is never built, so mcpApprovePath is "" and unused there. The live
// wire is exercised end-to-end by TestInteractiveStreamModalResolution (#1154).
func newStreamRunnerFactory(sink *streamTurnSink, mcpApprovePath string) sessions.RunnerFactory {
	return func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
		scfg := mapStreamsupConfig(cfg)
		scfg.Args = withApprovalArgs(scfg.Args, mcpApprovePath)
		scfg.Stdout = streamsup.NewParser(sink.sinkFor(cfg.SessionID), cfg.Logger)
		scfg.OnChildExit = sink.exitFor(cfg.SessionID)
		r, err := streamsup.New(scfg)
		if err != nil {
			return nil, fmt.Errorf("cmd/pyry: stream runner: %w", err)
		}
		return streamRunner{r: r}, nil
	}
}

// withApprovalArgs injects claude's permission-approval flags onto a stream
// spawn's args unless the spawn is already in yolo / skip-permissions mode
// (#1168). It is the interactive-stream twin of agent_run.go's non-yolo
// permissionArgs wiring — the first live consumer of permissionArgs on the
// interactive path.
//
// Yolo is expressed to claude as exactly one flag, --dangerously-skip-permissions,
// and both yolo entry points funnel through it: the operator's bootstrap
// pass-through claude args (main.go) and internal/sessions.claudeSettingsArgs
// (which appends it when the per-session YOLO bit is set). So the flag's presence
// in the spawn's args is the single deterministic yolo signal, robust to both
// entry points and evaluated per-spawn here:
//
//   - yolo (flag present) → return args UNCHANGED. The flag is already in base;
//     injecting nothing keeps AC2 byte-identical to today and avoids a duplicate
//     --dangerously-skip-permissions. Do NOT call permissionArgs(true, …) here —
//     that would re-emit the flag already present.
//   - non-yolo → append permissionArgs(false, mcpApprovePath): the
//     --permission-prompt-tool / --mcp-config / --strict-mcp-config /
//     --permission-mode default set that routes every non-allowlisted tool use
//     through the daemon approval registry.
//
// The append runs on a clone so the caller's args (scfg.Args, freshly owned by
// mapStreamsupConfig) is never aliased or mutated.
func withApprovalArgs(args []string, mcpApprovePath string) []string {
	if slices.Contains(args, "--dangerously-skip-permissions") {
		return args
	}
	return append(slices.Clone(args), permissionArgs(false, mcpApprovePath)...)
}

// mapStreamsupConfig maps a supervisor.Config to the streamsup.Config that
// streamsup.New consumes. It mutates nothing, constructs no runtime object, and
// returns a struct every field of which is inspectable, so the no-double-inject
// invariant is asserted directly (see streamsup_runner_test.go). It is no longer
// syscall-free, though: #1631's ClaudeSessionsDir is derived from WorkDir by
// streamClaudeSessionsDir, which stats the path and reads $HOME. Those reads are
// read-only and degrade to "" rather than failing, so the mapper still has no
// error return and no ordering constraint.
//
// The PTY-only fields on supervisor.Config are deliberately NOT mapped —
// streamsup owns its own id-flag inversion (buildArgs), has no PTY bridge, no
// transcript binding, and no .cast recorder: ResumeLast, ResolveSessionID,
// Bridge, ValidateConversation, ResolveTranscript, RecordDir, helperEnv.
// ResolveSessionID stays dropped even now that a sessions DIRECTORY crosses this
// seam: the directory is all that crosses, and streamsup's own per-spawn by-id
// existence probe (useCreateForm) decides the flag from it. No resolver callback
// crosses the seam in either direction, and neither side SCANS the directory —
// the probe is by-id only (#839 deleted --continue and adopt-by-mtime). Stdout
// stays nil HERE — the turnevent Parser that plugs into Stdout is a runtime
// object installed one layer up in newStreamRunnerFactory (#1098), which is the
// line the new field does not cross: it is a plain string derived from one
// RunnerConfig field, not a live object. Stderr/Env have no supervisor.Config
// analogue and stay nil.
func mapStreamsupConfig(cfg sessions.RunnerConfig) streamsup.Config {
	return streamsup.Config{
		ClaudeBin: cfg.ClaudeBin,
		WorkDir:   cfg.WorkDir,
		// #1108 guarantees SessionID is non-empty at both pool sites; streamsup.New
		// requires it (streamsup owns re-injecting the id flag from this value).
		SessionID:         cfg.SessionID,
		ClaudeSessionsDir: streamClaudeSessionsDir(cfg.WorkDir),
		Args:              stripSessionIDFlags(cfg.ClaudeArgs),
		Logger:            cfg.Logger,
		BackoffInitial:    cfg.BackoffInitial,
		BackoffMax:        cfg.BackoffMax,
		BackoffReset:      cfg.BackoffReset,
		// The interactive daemon asks every child, once, what the session knows
		// about itself (#1839). Set HERE rather than in newStreamRunnerFactory
		// because it is a plain bool constant, not a runtime object — the same
		// dividing line ClaudeSessionsDir sits on — and setting it in the pure
		// mapper makes the policy directly assertable with no new scaffolding.
		RequestInitializeOnSpawn: true,
	}
}

// streamClaudeSessionsDir derives the directory claude writes <uuid>.jsonl into
// for a runner whose WorkDir is workdir — the value streamsup.Config's
// ClaudeSessionsDir field takes, where "" means "run no probe" and every spawn's
// id flag falls back to the Run loop's firstRun latch (#1631 arms what #1630 left
// inert). It is derived per runner rather than once per pool because a
// per-conversation runner's WorkDir is the phone's confined spawn dir when one
// was requested (Pool.buildSession), so it legitimately differs from the
// bootstrap workdir.
//
// The composition is pinned to the transform streamsup.New itself applies: it
// sets the child's cmd.Dir to agentrun.ResolveWorkdir(WorkDir), and claude
// encodes its own resolved cwd into the projects folder name, so the probe must
// key on the SAME resolution. #1655 measured the empirical directory — located by
// finding the transcript claude actually wrote — against
// sessions.DefaultClaudeSessionsDir of a cwd resolved that way, and found them
// equal.
//
// Do NOT substitute resolveClaudeSessionsDir or a bare DefaultClaudeSessionsDir.
// Neither applies canonicalCase, which ResolveWorkdir does and claude does, and
// neither confineWorkdirToHome (bootstrap) nor resolveSpawnDir (phone)
// canonicalises case — so on a case-insensitive filesystem a wrong-cased workdir
// would yield a directory claude never writes. That reads "absent" for a session
// whose transcript exists, spawns --session-id against a live transcript, which
// claude refuses (ADR 032), and — because a supplied directory makes
// useCreateForm decide OUTRIGHT rather than latch — loops permanently instead of
// wasting one spawn.
//
// Every "" arm is pre-#1631 behaviour rather than a new failure mode. An empty
// workdir is unreachable at both pool sites (both carry a confined realpath) but
// must not fall through to the process cwd the way resolveClaudeSessionsDir
// deliberately does; a workdir ResolveWorkdir rejects never produced a runner at
// all, since streamsup.New calls the same function on the same value and returns
// an error; and an unresolvable $HOME degrades inside DefaultClaudeSessionsDir.
func streamClaudeSessionsDir(workdir string) string {
	if workdir == "" {
		return ""
	}
	resolved, err := agentrun.ResolveWorkdir(workdir)
	if err != nil {
		return ""
	}
	return sessions.DefaultClaudeSessionsDir(resolved)
}

// stripSessionIDFlags returns a new slice with every --session-id/--resume flag
// (and its value) removed. It never mutates args — the caller's ClaudeArgs is
// aliased into the pool's spawnBase — so it always allocates a fresh backing
// array.
//
// streamsup.buildArgs re-injects --session-id <id> (first spawn) / --resume <id>
// (respawn) from Config.SessionID itself. Leaving the pool's baked
// "--session-id <id>" (Pool.buildSession) in Args would double-inject
// (--session-id X … --session-id X, or the contradictory --session-id X …
// --resume X on respawn). The strip is required at the per-session site and a
// harmless no-op at the bootstrap site (whose ClaudeArgs carry no id flag) —
// applied uniformly, no site-specific branch.
func stripSessionIDFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		// Two-token form: --session-id <value> / --resume <value>. Drop the flag
		// and skip its value. A dangling flag (no following token) just drops the
		// lone flag — i++ past the end ends the loop without an index.
		if a == "--session-id" || a == "--resume" {
			i++
			continue
		}
		// Joined form: --session-id=<value> / --resume=<value>. One token to drop.
		if strings.HasPrefix(a, "--session-id=") || strings.HasPrefix(a, "--resume=") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// Assert at compile time that streamRunner satisfies sessions.Runner (AC1). The
// build fails if a method is missing or mis-typed. This is the ONLY conformance
// assertion for the interface, not a mirror of one in internal/sessions: that
// package declares no assertion of its own because the sole production
// implementation lives here.
var _ sessions.Runner = streamRunner{}
