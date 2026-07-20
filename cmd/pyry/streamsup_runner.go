package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// streamRunner adapts *streamsup.Runner to sessions.Runner. The shapes differ in
// exactly one method: sessions.Runner.State returns supervisor.State, but
// internal/streamsup must not import internal/supervisor (its package doc forbids
// it), so *streamsup.Runner declares State() streamsup.State instead. Go has no
// covariant return on interface satisfaction, so the concrete runner cannot
// satisfy the interface directly. This adapter — living in cmd/pyry, which knows
// both types — maps that one method and forwards the other four unchanged. The
// precedent for this covariant-return seam is poolResolver in main.go.
//
// The factory that constructs a streamRunner from a supervisor.Config —
// newStreamRunnerFactory below (#1109 delivered the constructor; #1098 gave it
// the turnevent sink) — the interactive_runner selection that injects it on
// sessions.Config.RunnerFactory is #1081.
type streamRunner struct{ r *streamsup.Runner }

func (a streamRunner) State() supervisor.State { return mapStreamState(a.r.State()) }

func (a streamRunner) WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error {
	return a.r.WriteUserTurn(ctx, conversationID, payload)
}

func (a streamRunner) WaitForPTY(ctx context.Context) error { return a.r.WaitForPTY(ctx) }

func (a streamRunner) Run(ctx context.Context) error { return a.r.Run(ctx) }

func (a streamRunner) Restart(args []string) { a.r.Restart(args) }

// mapStreamState maps streamsup's native lifecycle snapshot to supervisor.State.
// The two types mirror each other field-for-field; Phase maps by a plain string
// conversion because the phase values are identical across the two packages.
func mapStreamState(s streamsup.State) supervisor.State {
	return supervisor.State{
		Phase:        supervisor.Phase(string(s.Phase)),
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
func newStreamRunnerFactory(sink *streamTurnSink) sessions.RunnerFactory {
	return func(cfg supervisor.Config) (sessions.Runner, error) {
		scfg := mapStreamsupConfig(cfg)
		scfg.Stdout = streamsup.NewParser(sink.sinkFor(cfg.SessionID), cfg.Logger)
		r, err := streamsup.New(scfg)
		if err != nil {
			return nil, fmt.Errorf("cmd/pyry: stream runner: %w", err)
		}
		return streamRunner{r: r}, nil
	}
}

// mapStreamsupConfig maps a supervisor.Config to the streamsup.Config that
// streamsup.New consumes. Pure and side-effect-free — every field on the returned
// struct is inspectable, so the no-double-inject invariant is asserted directly
// (see streamsup_runner_test.go).
//
// The PTY-only fields on supervisor.Config are deliberately NOT mapped —
// streamsup owns its own id-flag inversion (buildArgs), has no PTY bridge, no
// transcript binding, and no .cast recorder: ResumeLast, ResolveSessionID,
// Bridge, ValidateConversation, ResolveTranscript, RecordDir, helperEnv. Stdout
// stays nil HERE — the turnevent Parser that plugs into Stdout is a runtime
// object installed one layer up in newStreamRunnerFactory (#1098), keeping this
// mapper pure; Stderr/Env have no supervisor.Config analogue and stay nil.
func mapStreamsupConfig(cfg supervisor.Config) streamsup.Config {
	return streamsup.Config{
		ClaudeBin: cfg.ClaudeBin,
		WorkDir:   cfg.WorkDir,
		// #1108 guarantees SessionID is non-empty at both pool sites; streamsup.New
		// requires it (streamsup owns re-injecting the id flag from this value).
		SessionID:      cfg.SessionID,
		Args:           stripSessionIDFlags(cfg.ClaudeArgs),
		Logger:         cfg.Logger,
		BackoffInitial: cfg.BackoffInitial,
		BackoffMax:     cfg.BackoffMax,
		BackoffReset:   cfg.BackoffReset,
	}
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
// build fails if a method is missing or mis-typed. Mirrors
// var _ Runner = (*supervisor.Supervisor)(nil) in internal/sessions/runner.go.
var _ sessions.Runner = streamRunner{}
