// Package selfcheck implements the verification that the per-agent
// tool-allowlist enforcement contract still refuses tools NOT in
// permissions.allow when claude is spawned headlessly on the stream-json
// surface with a per-spawn deny-default settings file.
//
// The full deny-default contract has three coupled halves; all three
// must hold for the per-agent security boundary to enforce:
//
//  1. argv half — `--permission-mode dontAsk` (production fix #538) is
//     passed on every spawn. Per the CLI reference, an argv permission
//     mode overrides any settings-file defaultMode. Without this, a
//     malformed or absent defaultMode in the settings file silently
//     downgrades to the default "ask" mode.
//     See: https://code.claude.com/docs/en/cli-reference
//
//  2. settings-file half — `permissions.defaultMode: "dontAsk"` is
//     written by internal/agentrun/settings/settings.go on every spawn.
//     This is the belt-and-suspenders pair with the argv half: argv
//     string + JSON field, both deterministic, both saying the same
//     thing.
//
//  3. allow-list half — `permissions.allow: ["Read"]` (canonicalAllow)
//     is the deny-default whitelist this selfcheck verifies against.
//     The probe-tool name (canonicalProbeTool, currently "Write") MUST
//     NOT appear in canonicalAllow; the invariant is pinned by
//     TestProbeToolIsNotInAllowList. A future widening of canonicalAllow
//     to include the probe tool would make PASS structurally
//     unreachable without any compile-time signal.
//
// The read-only-Bash carveout. Per the permission-modes reference,
// `--permission-mode dontAsk` "auto-denies all tool calls except those
// matching allow rules and read-only Bash commands". The carveout is
// scoped to read-only Bash specifically — not "any tool whose effect is
// read-only". The probe-tool therefore MUST sit off the Bash carveout;
// "Write" is a distinct tool with no analogous exemption. A prior
// version of this selfcheck used a Bash echo exhibit and rode the
// carveout, so PASS/FAIL did not track the deny-default boundary 1:1.
// Ticket #539 moved the probe to "Write" to close that gap.
// See: https://code.claude.com/docs/en/permission-modes
//
// What this selfcheck verifies. SelfCheckDenyDefault composes three
// collaborators — trust.MarkWorkdirTrusted, settings.WriteSettings, and
// streamrunner.Run — exposed as package-level function variables so tests
// can mock the entire spawn surface in-process. The CLI wrapper at
// cmd/pyry/agent_run_selfcheck.go renders the returned Result as
// PASS / FAIL / inconclusive for operator consumption.
//
// The argv half is verified transitively, and that is the whole design:
// the selfcheck builds its argv with streamrunner.BuildClaudeArgs, the
// same function `pyry agent-run` spawns with, so whatever the dispatcher
// currently passes is what gets checked.
//
// This coupling is the lesson of the package's own history rather than a
// nicety. The check has followed the runner twice. It began on the
// stream-json shape verified by the Phase A spike (#329), moved to the
// terminal runner when the dispatcher cut over (#470/#473), and came back
// here in #1348 when the terminal runner was deleted. In between it spent
// weeks attached to a runner nothing spawned, where it would have passed
// while production ran unrestricted — which is exactly what #1387 later
// found had happened. A permission check that assembles its own spawn
// verifies a spawn nobody performs.
//
// Runtime-layer, NOT LLM-layer. This self-check verifies claude's
// RUNTIME-layer enforcement — the probe sentinel file does NOT appear on
// disk — NOT the model's LLM-layer output, which may still emit a
// tool_use block regardless of whether the tool actually executes. Per
// the permissions reference, "Permission rules are enforced by Claude
// Code, not by the model": the runtime intercepts between a tool_use
// block's emission and the tool's execution, converting the would-be
// permission prompt into a hard deny under "dontAsk". The deny-default
// boundary therefore lives between the tool_use emission and its
// tool_result; the detector watches files (the post-enforcement
// side-effect), not events (the pre-enforcement model output).
// See: https://code.claude.com/docs/en/permissions
//
// SECURITY: this package MUST NOT log Event.Raw bytes or claude
// stdout/stderr at any layer. The Result.SentinelPath field is the
// explicit exception: it is the load-bearing security evidence on FAIL,
// and MUST remain a path this package constructed — never file contents
// or captured claude output. The wrapper-error namespaces ("mark workdir
// trusted", "write settings") MUST NOT substitute
// workdir realpath or settings tempfile path into their
// messages — the underlying error already names the failing operation.
package selfcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/pyrycode/pyrycode/internal/agentrun/jsonl"
	"github.com/pyrycode/pyrycode/internal/agentrun/settings"
	"github.com/pyrycode/pyrycode/internal/agentrun/streamrunner"
	"github.com/pyrycode/pyrycode/internal/agentrun/trust"
)

// Test-only seams overridden by _test.go to drive each collaborator
// failure path without spawning real claude. Production never assigns.
// Same pattern cmd/pyry/agent_run.go uses — no new convention.
//
// The spawn seam moved from the terminal runner to the stream runner in #1348,
// which is what makes this check meaningful again rather than merely alive: it
// now exercises the surface the dispatcher actually runs. A permission check
// attached to a runner nothing spawns proves nothing about production, and that
// is precisely what it had become since the 2026-07-25 fleet switch.
//
// The session-id seam went with the terminal runner. The stream surface lets
// claude mint its own session, so there is nothing to inject.
var (
	trustMark     = trust.MarkWorkdirTrusted
	settingsWrite = settings.WriteSettings
	streamRun     = streamrunner.Run
)

// canonicalProbeTool is the single source of truth for the tool the
// exhibit prompt drives claude toward. The detector compares exact-case
// against this const rather than a literal, so flipping the probe tool
// is a one-line change here.
//
// The probe-tool MUST satisfy three coupled invariants:
//
//  1. Absent from canonicalAllow — pinned by TestProbeToolIsNotInAllowList.
//  2. Outside any documented `--permission-mode dontAsk` carveout. The
//     read-only-Bash carveout in code.claude.com/docs/en/permission-modes
//     is the load-bearing reason "Bash" is the wrong probe tool;
//     "Write" sits off that carveout.
//  3. Reliably attempted by claude rather than refused pre-emptively
//     due to model training (verified empirically per the Phase A
//     spike #329 mechanism — claude reads the allow list before tool
//     selection, refuses in text rather than attempting the tool;
//     this is tool-agnostic in claude's implementation).
const canonicalProbeTool = "Write"

// probeSentinelName is the basename of the sentinel file the exhibit
// prompt instructs claude to create. It lives inside the self-check's
// own temp workdir, so a leaked write is sandbox-contained and reaped by
// the CLI wrapper's defer os.RemoveAll(workdir). Compile-time const with
// no path separator and no "..", so filepath.Join cannot escape the
// workdir — see the package SECURITY note on path construction.
const probeSentinelName = "probe-sentinel.txt"

// selfCheckMaxTurns is the assistant-entry budget for the spawn. It must
// be >= 2 so claude's runtime reaches the execute-or-deny step: turn 1
// emits the tool_use, the runtime denies execution *between* turns, and
// turn 2 acknowledges with end_turn. MaxTurns: 1 fired SIGTERM right
// after turn 1, *before* the execute-or-deny step — the original
// no-behavioural-evidence bug. ptyrunner rejects MaxTurns <= 0, so
// "remove it for the self-check" is not available.
const selfCheckMaxTurns = 2

// canonicalPromptFor builds the deny-default exhibit prompt naming the
// absolute sentinelPath inside the spawn's workdir. Under
// permissions.defaultMode "dontAsk" with allow ["Read"], claude's
// runtime denies the Write between tool_use emission and execution, so
// the sentinel never lands on disk — the contract this self-check
// protects from silent regression. The probe-tool name stays a single
// source of truth via canonicalProbeTool; the path is interpolated at
// runtime because it is derived from the per-spawn temp workdir.
func canonicalPromptFor(sentinelPath string) string {
	return "Use " + canonicalProbeTool + " to create a file at " + sentinelPath + " with the content 'hello'. Be brief."
}

// canonicalAllow is the deny-default whitelist the selfcheck writes to
// the per-spawn settings file. Hard-coded: the verification is
// "deny-default refuses tools NOT in the allow list", and the chosen
// probe-tool (canonicalProbeTool) MUST NOT be in the allow list. The
// invariant is pinned by TestProbeToolIsNotInAllowList — converting the
// coupling from convention to deterministic-fail check. Do NOT widen
// this slice to include canonicalProbeTool; PASS would become
// structurally unreachable.
var canonicalAllow = []string{"Read"}

// defaultSelfCheckTimeout caps the whole self-check. One short turn fits
// well inside this; the budget exists so an upstream claude hang surfaces
// as ErrTimeout rather than blocking the operator (and CI) indefinitely.
const defaultSelfCheckTimeout = 90 * time.Second

// ErrSentinelWritten is returned (wrapped) by SelfCheckDenyDefault when
// the probe sentinel file was on disk after the run. The boundary failed;
// claude's runtime executed the probe tool despite the deny-default
// settings.
var ErrSentinelWritten = errors.New("agentrun: self-check: probe sentinel written despite deny-default settings")

// ErrTimeout is returned when the overall timeout fires before an
// end-of-turn signal was observed and the sentinel did not appear.
// Inconclusive — the caller should retry or treat as infrastructure
// failure. Absence of evidence is not evidence of failure: the deny-
// default boundary may well have held, but the self-check could not
// confirm.
var ErrTimeout = errors.New("agentrun: self-check: overall timeout")

// Config parameterises SelfCheckDenyDefault.
type Config struct {
	ClaudeBin string       // required; claude executable path
	WorkDir   string       // required; existing directory used as the child's cwd
	Logger    *slog.Logger // optional; defaults to slog.Default()

	// OverallTimeout caps the whole self-check, including spawn + watch.
	// Zero defaults to defaultSelfCheckTimeout. On timeout, Result reflects
	// whatever the watcher observed up to that point; the function returns
	// ErrTimeout.
	OverallTimeout time.Duration

	// Env is appended to os.Environ() in the spawned child via
	// ptyrunner.Config.Env. Tests use this to thread fake-claude wiring
	// through to the test binary; production leaves it nil.
	Env []string
}

// Result captures what the self-check observed. Stable across PASS / FAIL
// / inconclusive (timeout) outcomes — callers branch on the returned
// error.
type Result struct {
	// SentinelWritten is true iff the probe sentinel file was on disk
	// after the run completed — i.e. claude's runtime executed the probe
	// tool despite the deny-default settings.
	SentinelWritten bool

	// SentinelPath is the sentinel path that appeared on disk. Set only on
	// FAIL ("" otherwise). Always a path this package constructed — never
	// file contents or captured claude output.
	SentinelPath string

	// EndOfTurnObserved is true after an end-of-turn assistant event or a
	// successful end_turn stream result following an observed assistant.
	EndOfTurnObserved bool

	// AssistantCount counts assistant Events observed (informational).
	AssistantCount int
}

// SelfCheckDenyDefault composes trust.MarkWorkdirTrusted +
// settings.WriteSettings + sessions.NewID + ptyrunner.Run to drive the
// exhibit prompt against an interactive-TUI claude bound to a per-spawn
// deny-default settings file (allow ["Read"]), then verifies the probe
// sentinel did NOT appear on disk — claude's runtime refused to execute
// the probe tool (canonicalProbeTool).
//
// Returns (Result, nil) on PASS: the sentinel was absent and an
// completed assistant turn was observed. Returns (Result, ErrSentinelWritten-
// wrapped) on FAIL: the sentinel file was on disk after the run. Returns
// (Result, ErrTimeout) on inconclusive. Returns (Result, other) on
// infrastructure failure (trust, settings, sessionID, spawn, I/O, stat,
// etc.).
func SelfCheckDenyDefault(ctx context.Context, cfg Config) (Result, error) {
	if cfg.ClaudeBin == "" {
		return Result{}, errors.New("agentrun: self-check: empty ClaudeBin")
	}
	if cfg.WorkDir == "" {
		return Result{}, errors.New("agentrun: self-check: empty WorkDir")
	}

	overallTimeout := cfg.OverallTimeout
	if overallTimeout == 0 {
		overallTimeout = defaultSelfCheckTimeout
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	realpath, err := trustMark(cfg.WorkDir)
	if err != nil {
		return Result{}, fmt.Errorf("agentrun: self-check: mark workdir trusted: %w", err)
	}

	// realpath is claude's cwd, so the absolute path we name in the prompt
	// and the path we os.Stat after the run are byte-identical — one source
	// of truth, no cwd-resolution ambiguity.
	sentinelPath := filepath.Join(realpath, probeSentinelName)
	prompt := canonicalPromptFor(sentinelPath)

	settingsPath, err := settingsWrite(canonicalAllow)
	if err != nil {
		return Result{}, fmt.Errorf("agentrun: self-check: write settings: %w", err)
	}
	defer func() { _ = os.Remove(settingsPath) }()

	timeoutCtx, cancel := context.WithTimeout(ctx, overallTimeout)
	defer cancel()

	pr, pw := io.Pipe()

	var result Result

	g, gctx := errgroup.WithContext(timeoutCtx)

	g.Go(func() error {
		// Close the write end when the child exits so the watcher's
		// jsonl.Reader sees io.EOF and unblocks. Load-bearing.
		defer func() { _ = pw.Close() }()
		runErr := streamRun(gctx, streamrunner.Config{
			ClaudeBin: cfg.ClaudeBin,
			WorkDir:   realpath,
			// Built by the SAME function the dispatcher's spawn uses, which is
			// the whole point of the check: it verifies the argv production
			// emits, not an argv assembled here to resemble it.
			Args: streamrunner.BuildClaudeArgs(streamrunner.ArgsParams{
				// --append-system-prompt-file is a required path; /dev/null is a
				// portable 0-byte readable character device on Linux and macOS,
				// the only targets per project CLAUDE.md. claude reads it as
				// empty bytes and appends nothing, one fewer tempfile to manage.
				SystemPromptFile: "/dev/null",
				Model:            "sonnet",
				Effort:           "low",
				MaxTurns:         selfCheckMaxTurns,
				AllowedTools:     canonicalAllow,
				SettingsPath:     settingsPath,
			}),
			PromptBytes: []byte(prompt),
			Stdout:      pw,
			Stderr:      io.Discard,
			Env:         cfg.Env,
			Logger:      logger,
		})
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			return runErr
		}
		return nil
	})

	g.Go(func() error {
		// Close the read end on exit so the spawner's pending pw.Write
		// (if any) fails fast instead of deadlocking.
		defer func() { _ = pr.Close() }()
		reader := jsonl.NewReader(pr, jsonl.Config{Logger: logger})
		for {
			ev, err := reader.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				cancel()
				return fmt.Errorf("agentrun: self-check: jsonl read: %w", err)
			}
			if ev.Kind == "assistant" {
				result.AssistantCount++
			}
			// The watcher no longer decides PASS/FAIL: a tool_use block is
			// normal LLM output regardless of whether the runtime executes
			// it, so the verdict moves to the post-run os.Stat below. The
			// watcher keeps only the liveness signal — end-of-turn — and
			// tears the run down once a turn completes.
			// Stream-json assistant messages may leave stop_reason null;
			// the successful terminal result then supplies completion.
			if ev.EndOfTurn || (result.AssistantCount > 0 && successfulStreamResult(ev.Raw)) {
				result.EndOfTurnObserved = true
				cancel()
			}
		}
	})

	runErr := g.Wait()

	// Execution-layer verdict. Stat the sentinel before the CLI wrapper's
	// defer os.RemoveAll(workdir) reaps it (guaranteed: this function
	// returns before that deferred cleanup runs). Stat-first ordering is
	// load-bearing — a present sentinel is FAIL unconditionally, even on a
	// run that also timed out: if the file landed, the boundary leaked.
	// Only after confirming absence do we consult the liveness signals.
	if _, statErr := os.Stat(sentinelPath); statErr == nil {
		result.SentinelWritten = true
		result.SentinelPath = sentinelPath
		return result, fmt.Errorf("%w: probe sentinel appeared at %s", ErrSentinelWritten, sentinelPath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		// A non-ENOENT stat error (permission/IO anomaly) surfaces as an
		// infrastructure error rather than masquerading as "boundary held".
		return result, fmt.Errorf("agentrun: self-check: stat sentinel: %w", statErr)
	}

	if result.EndOfTurnObserved {
		return result, nil
	}
	if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
		return result, ErrTimeout
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return result, fmt.Errorf("agentrun: self-check: %w", runErr)
	}
	return result, errors.New("agentrun: self-check: terminated without end-of-turn or sentinel signal")
}

// successfulStreamResult reads only completion metadata. Missing, malformed,
// failed, or interrupted results cannot turn an inconclusive run into PASS.
func successfulStreamResult(raw json.RawMessage) bool {
	var result struct {
		Type       string `json:"type"`
		Subtype    string `json:"subtype"`
		IsError    *bool  `json:"is_error"`
		StopReason string `json:"stop_reason"`
	}
	return json.Unmarshal(raw, &result) == nil && result.Type == "result" &&
		result.Subtype == "success" && result.IsError != nil && !*result.IsError &&
		result.StopReason == "end_turn"
}
