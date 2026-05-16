# 375 — `agent-run --self-check`: rewrite for stream-json mode

**Status:** draft
**Ticket:** [#375](https://github.com/pyrycode/pyrycode/issues/375)
**Supersedes (mechanism):** #336 (PTY-bridged interactive self-check)
**Preserves (intent):** catch silent Claude Code schema or behaviour
regressions that would let our agents call tools we never allowed.

---

## Files to read first

The developer's turn-1 reading list. Each entry is the minimum slice
needed; do not pre-read more than this.

- `internal/agentrun/selfcheck/selfcheck.go:1-316` — full current
  implementation. The package shrinks: drop trust-dialog wiring,
  system-prompt file, `--session-id` minting, `jsonl/tail` watcher,
  `agentrun.Drive` call.
- `internal/agentrun/selfcheck/selfcheck_test.go:1-374` — current
  PTY-fake test harness. Wholesale replaced.
- `cmd/pyry/agent_run_selfcheck.go:1-117` — operator-facing CLI
  wrapper. Inputs change (no `WriteSettings`, no trust/prompt delay
  env), output strings change (drop `defaultMode`, settings-file
  language).
- `cmd/pyry/agent_run_selfcheck_test.go:1-174` — CLI-level fake-claude
  test harness. Wholesale replaced (same shape as `streamrunner`
  helper pattern).
- `cmd/pyry/agent_run.go:180-267` — `runAgentRun` + `buildClaudeArgs`.
  Source of truth for the `--allowed-tools` /
  `--dangerously-skip-permissions` / `--input-format stream-json` /
  `--output-format stream-json --verbose` argv shape this ticket must
  mirror. Do not invent flag combinations; copy this set.
- `internal/agentrun/streamrunner/runner.go:1-195` — the spawning
  primitive. Reuse `Run` and `Config` verbatim; the self-check is just
  another caller.
- `internal/agentrun/streamrunner/runner_test.go:36-104` plus
  `internal/agentrun/streamrunner/helper_test.go:1-83` — the
  re-exec-the-test-binary fake-claude pattern. Mirror this shape for
  the new self-check fake; the wrapper script + `TestHelperProcess`
  pair from the old self-check is no longer needed because
  streamrunner already accepts `-test.run=...` argv directly.
- `internal/agentrun/jsonl/reader.go:103-262` — the canonical
  stream-json line parser already in tree. Reuse `jsonl.NewReader` /
  `jsonl.Reader.Next`; do not write a second parser. The watcher's
  Bash detector runs on `Event.Raw`, exactly as today.
- `.github/workflows/self-check-daily.yml` — read only; commentary
  there still mentions `permissions.defaultMode`. Out of scope for
  this ticket (ops sibling, per AC); do not edit. Mentioned here so
  the developer knows it exists and resists the urge to drift.

---

## Context

`internal/agentrun/selfcheck/` was introduced in #336 to verify that
`permissions.defaultMode: "deny"` in the per-spawn
`.pyry-agent-run-settings.json` actually denied `Bash` when claude was
spawned via PTY in interactive mode. The mechanism rested on three
load-bearing assumptions:

1. claude reads `--settings` and honours `defaultMode`.
2. The denied tool's absence can be observed by tailing the on-disk
   session JSONL written under `~/.claude/projects/<encoded-cwd>/`.
3. The PTY surface (trust dialog, prompt submission) is the same one
   production code uses.

The #374 stream-json rewrite (children #390 / #391 / #392) made all
three obsolete:

- The per-spawn settings file is no longer produced or consumed; the
  tool allowlist is enforced via `--allowed-tools` plus
  `--dangerously-skip-permissions` (`cmd/pyry/agent_run.go:255-267`).
- claude in `--output-format stream-json --verbose` writes the
  canonical assistant event stream to stdout directly; the on-disk
  JSONL is incidental.
- Interactive PTY is dead code in agent-run; the only production
  spawn path is `streamrunner.Run` over plain pipes.

The conceptual safety net is unchanged: **catch silent Claude Code
schema or behaviour regressions that would let our agents call tools
we never allowed.** This ticket re-implements that safety net against
the post-#374 surface.

The cron CI workflow at `.github/workflows/self-check-daily.yml`
still consumes the `pyry agent-run --self-check` exit-code contract
verbatim and stays out of scope here per ticket AC.

---

## Design

### Package outline

`internal/agentrun/selfcheck/` (one file, `selfcheck.go`) exposes:

- `SelfCheckDenyDefault(ctx context.Context, cfg Config) (Result, error)`
  — single entry point (name unchanged for stability of the CLI
  import).
- `Config` — required: `ClaudeBin`, `WorkDir`. Optional: `Prompt`,
  `Logger`, `OverallTimeout`, `Env`.
- `Result` — `BashInvoked bool`, `Evidence json.RawMessage`,
  `EndOfTurnObserved bool`, `AssistantCount int`.
- `ErrBashInvoked` — preserved verbatim, same message string, same
  meaning (boundary dissolved).
- `ErrTimeout` — preserved verbatim, same message string, same
  meaning (inconclusive, before either end-of-turn or Bash observed).

Removed:

- `ErrUnknownTUI` — PTY-only, no longer reachable. Delete the
  sentinel and any reference.
- `HomeDir` field on `Config` — the watcher no longer reads
  `$HOME/.claude/projects/...`; nothing else in the package needs it.
- `TrustDialogDelay`, `PromptDelay` fields — no PTY, no delays.
- `systemPromptFile` constant + the `os.WriteFile(sysPromptPath, ...)`
  call — `--append-system-prompt-file` is no longer in the argv.
- `newSessionID` helper — `streamrunner` does not require nor accept
  `--session-id` because the watcher reads stdout, not a session
  file. Drop it.
- The `agentrun.MarkWorkdirTrusted` call — `--dangerously-skip-permissions`
  removes the trust dialog entirely (mirrors production agent-run
  semantics, `cmd/pyry/agent_run.go:246-251`).
- `agentrun.SettingsFilename` reference + the
  `agentrun.WriteSettings` call site (in `cmd/pyry/agent_run_selfcheck.go`).
- The `internal/agentrun/jsonl/tail` import. The `jsonl.Reader`
  (not `jsonl/tail`) is still used to parse stdout — those are
  different sub-packages.

### Data flow

```
                cfg.Workdir + cfg.Args  ──┐
                                          │
SelfCheckDenyDefault ──spawn──> streamrunner.Run ──> claude (stream-json)
                                          │              │
       jsonl.NewReader(stdoutReadEnd) <───┤───stdout─────┘
                                          │
       loop: ev, err := reader.Next() ──> bashInvokedInRaw(ev.Raw)
                                          │
                                  ┌───────┴───────┐
                                  │               │
                       hit Bash tool_use     hit EndOfTurn
                          (cancel ctx,         (cancel ctx,
                           Result.BashInvoked   Result.EndOfTurnObserved
                           = true)              = true)
```

`streamrunner.Run` already takes any `io.Writer` for stdout; the
self-check passes an `io.Pipe` writer end. The reader goroutine owns
the `jsonl.Reader` over the pipe's read end and runs until either:

- `ev.EndOfTurn == true` for some assistant event → record + cancel.
- `bashInvokedInRaw(ev.Raw)` returns `(true, nil)` → record + cancel.
- `reader.Next()` returns `io.EOF` (child closed stdout cleanly with
  no end-of-turn nor Bash) → return without recording.
- `ctx` cancelled by the deadline → return.

### Concurrency model

Two goroutines coordinated by an `errgroup.WithContext`:

- **Spawner.** Calls `streamrunner.Run(gctx, runCfg)`. Stdout is the
  pipe write end; stderr is `io.Discard` (see Error handling §). Stdin
  is the canonical user-turn envelope `streamrunner` already writes —
  no per-call wiring needed.
- **Watcher.** Owns the pipe read end. Loops on `reader.Next()`. On
  match (Bash or end-of-turn) calls `cancel()` on the errgroup's
  context, which triggers `streamrunner`'s `SIGTERM` + `WaitDelay`
  teardown.

Pipe-close discipline: the spawner must close the pipe **write end**
when `streamrunner.Run` returns (deferred in the goroutine), so the
watcher's `reader.Next()` returns `io.EOF` and the watcher's
goroutine exits even when claude exits without producing end-of-turn
or Bash. Without this close, `errgroup.Wait()` would block forever.

Shutdown sequence:

1. Whichever goroutine cancels the context first wins.
2. `streamrunner.Run` reacts to ctx cancel by sending `SIGTERM` and
   waiting `killGrace` (5s) before stdlib follows up with `SIGKILL`.
3. When `streamrunner.Run` returns, its goroutine closes the pipe
   write end.
4. Watcher sees `io.EOF` (or finishes its in-flight match), returns.
5. `g.Wait()` collects both; both should return `nil` on the
   intended-cancel path (`streamrunner.Run` documents
   `ctx.Err() != nil` as a clean return).

### Argv shape

The argv passed to `streamrunner.Config.Args` must mirror the
production agent-run argv, less the inputs that don't apply to a
diagnostic verb. From `cmd/pyry/agent_run.go:255-267`:

| Flag                              | Self-check value                    | Rationale |
|-----------------------------------|-------------------------------------|-----------|
| `--input-format stream-json`      | same                                | required by AC mechanism |
| `--output-format stream-json`     | same                                | required by AC mechanism |
| `--verbose`                       | same                                | required for assistant events on stdout |
| `--dangerously-skip-permissions`  | same                                | drops trust dialog; the safety net being verified |
| `--allowed-tools Read`            | literal `"Read"` (single tool)      | the exhibit prompt asks for Bash; only Read is allowed |
| `--model sonnet`                  | hardcoded                           | spike (#329) used this; cheapest reliable model |
| `--effort low`                    | hardcoded                           | spike used this; one short turn is enough |
| `--max-turns 1`                   | hardcoded `1`                       | bounds runaway agent; one turn is sufficient |

Not passed:

- `--append-system-prompt-file` — production requires it; the
  diagnostic verb does not. The exhibit prompt is self-contained.
- `--session-id` — stream-json does not need it; the watcher reads
  stdout, not a session file.
- `--settings` — there is no per-spawn settings file.

The argv assembly lives inline in `SelfCheckDenyDefault`; do not
extract a helper. It is six lines, used once.

### CLI wrapper (`cmd/pyry/agent_run_selfcheck.go`)

Narrow rewrite. The function's shape (returns nil on PASS, returns
the wrapped sentinel on FAIL/inconclusive, returns infrastructure
errors verbatim) is unchanged. Changes:

1. Drop `agentrun.WriteSettings(workdir, []string{"Read"})` — no
   settings file.
2. Drop `parseDurationEnv` calls (`PYRY_AGENT_RUN_TRUST_DELAY`,
   `PYRY_AGENT_RUN_PROMPT_DELAY`) and the corresponding `Config`
   fields. The env-var helper in `cmd/pyry/agent_run.go:275-285`
   stays — it has no other callers either, but removing it is
   orthogonal cleanup; leave it in place (single one-line `_ = ...`
   call to silence "unused" if any survives — likely none).
3. Drop the `HomeDir` field from `Config` construction.
4. Operator-facing strings: the PASS line stays
   (`deny-default whitelist held: N assistant event(s) observed; Bash refused.`).
   The FAIL message body must no longer mention
   `permissions.defaultMode`, `.pyry-agent-run-settings.json`, "per-spawn
   settings file", or PTY. The "What was tested" / "What was observed"
   / "What to check" three-section structure is preserved (operator
   familiarity, pinned by tests below); the wording inside each
   section is updated. Concrete substitutions:

   - "per-spawn settings file at <workdir>/<settings-file> with
     permissions.defaultMode 'deny' and permissions.allow ['Read']"
     → "claude launched with `--allowed-tools \"Read\"
     --dangerously-skip-permissions` in stream-json mode"
   - "The permissions.defaultMode schema may have changed in claude.
     Compare the current claude `--settings` schema docs to the shape
     pyry writes in internal/agentrun/settings.go." → "The
     `--allowed-tools` enforcement contract may have changed in
     claude. Compare the current claude `--allowed-tools` /
     `--dangerously-skip-permissions` behaviour to the argv pyry
     writes in cmd/pyry/agent_run.go's `buildClaudeArgs`."
   - The "#329 (Phase A spike) and #336 (this self-check)" references
     are replaced with "#329 (Phase A spike), #336 (PTY-mode
     predecessor, superseded), #375 (this rewrite)."

   The INCONCLUSIVE message stays substantively the same; the only
   word swap is "end-of-turn signal" stays, no `defaultMode`
   references existed there.

### Error handling

Failure modes the design must surface, in priority order:

1. **Bash invoked** — `Result.BashInvoked = true`, `Result.Evidence`
   = verbatim `Event.Raw` of the offending assistant line. Return
   `fmt.Errorf("%w: tool_use name=%q observed in assistant entry", ErrBashInvoked, "Bash")`.
   Identical to today.
2. **End-of-turn observed** — `Result.EndOfTurnObserved = true`, no
   Bash seen. Return `nil`. Identical to today.
3. **Overall timeout** — neither (1) nor (2) before
   `cfg.OverallTimeout` fires. Return `ErrTimeout`. Identical
   sentinel; mechanism shifts from "no end-of-turn observed in JSONL"
   to "no end-of-turn observed in stdout stream-json", but the
   operator-visible meaning is the same.
4. **Child exited non-zero before producing end-of-turn or Bash** —
   `streamrunner.Run` returns `*exec.ExitError`. Return
   `fmt.Errorf("agentrun: self-check: %w", err)` so the CLI's
   `default:` branch propagates it as an infrastructure error
   (already wired).
5. **`reader.Next()` returns a non-EOF error** — wrap as
   `fmt.Errorf("agentrun: self-check: jsonl read: %w", err)` and
   cancel ctx. Same propagation as (4).
6. **Spawn failure** (claude not on PATH, mkdir, etc.) — bubble
   `streamrunner.Run`'s wrapped error verbatim. The CLI's `default:`
   branch handles it.
7. **Malformed assistant line in the middle of the stream** —
   `jsonl.Reader` already logs-and-skips malformed JSON (see
   `internal/agentrun/jsonl/reader.go:201,213,279-289`). The
   detector preserves the existing "one malformed line must not turn
   a PASS into an inconclusive" contract for free; no extra code.

**SECURITY:** the watcher MUST NOT log `Event.Raw`, claude stdout,
or claude stderr at any layer. The package's existing top-of-file
SECURITY note carries over verbatim; the only exception is
`Result.Evidence` on FAIL, which is the load-bearing operator finding.
Stderr is bound to `io.Discard` rather than forwarded so the
SECURITY contract is enforced structurally, not by convention.

### Testing strategy

Test file: `internal/agentrun/selfcheck/selfcheck_test.go` (wholesale
rewrite). Use the same `TestHelperProcess`-via-re-exec pattern as
`internal/agentrun/streamrunner/helper_test.go:1-83` — pass
`os.Args[0]` as `ClaudeBin` and inject test-mode argv plus
`GO_SELFCHECK_HELPER=1` via `Config.Env`. No `/bin/sh` wrapper
script is needed because `streamrunner` accepts `Args` verbatim
(unlike the deleted PTY drive path, which had to swallow real claude
flags).

Test cases (each ~10-25 lines plus the shared helper):

- **PASS, end-of-turn only.** Fake writes `passLine` (the existing
  `stop_reason:"end_turn"` fixture with a text content block) +
  `\n` to stdout, exits 0. Expect `err == nil`,
  `Result.EndOfTurnObserved == true`, `Result.BashInvoked == false`,
  `Result.AssistantCount == 1`, `Result.Evidence == nil`.
- **FAIL, Bash invoked.** Fake writes `bashLine` (assistant entry
  with `tool_use` content block, `name: "Bash"`), then `passLine`,
  exits 0. Expect `errors.Is(err, ErrBashInvoked)`,
  `Result.BashInvoked == true`, `Result.Evidence` contains
  `"name":"Bash"`. The trailing passLine confirms the detector
  doesn't fall through to PASS when Bash comes first.
- **INCONCLUSIVE, timeout.** Fake writes nothing useful and sleeps
  past `cfg.OverallTimeout` (300ms timeout, 2s sleep). Expect
  `errors.Is(err, ErrTimeout)`, both flags false.
- **Malformed line skipped, then PASS.** Fake writes
  `"{not valid json\n" + passLine + "\n"`, exits 0. Expect
  `err == nil`, `Result.EndOfTurnObserved == true`. Verifies the
  inherited `jsonl.Reader` resilience contract.
- **Bash detection ignores case.** `bashInvokedInRaw` unit table
  test preserved from today (single function, six rows): Bash hit,
  Read no-hit, text-only no-hit, lowercase "bash" no-hit, missing
  name no-hit, invalid JSON returns error. The detector is
  byte-identical to today's; only its caller changes.
- **Config validation.** Empty `ClaudeBin` and empty `WorkDir` each
  return a wrapped error containing `"empty ClaudeBin"` /
  `"empty WorkDir"`. Two cases (no `HomeDir`).

CLI-level test file: `cmd/pyry/agent_run_selfcheck_test.go`
(wholesale rewrite, smaller than today). Same three-mode fake
(`pass`, `bash`, `timeout`) wired via `PYRY_CLAUDE_BIN` pointing at
`os.Args[0]` plus an env var keying the mode. No shell wrapper
script needed — the production code calls `streamrunner` which
accepts argv verbatim, so the test binary's flag parser is satisfied
by prefixing `-test.run=^TestSelfCheckCLIHelperProcess$ --`. Mirror
the `helperRunCfg` shape from `streamrunner/runner_test.go:17-34`.

CLI tests to keep (one each):

- `TestRunAgentRunSelfCheck_PASS` — stdout starts with
  `"pyry agent-run --self-check: PASS\n"`, contains
  `"claude version: fake-claude 0.0.0"`, contains
  `"deny-default whitelist held"`.
- `TestRunAgentRunSelfCheck_FAIL` — `errors.Is(err, selfcheck.ErrBashInvoked)`,
  stdout starts with `"pyry agent-run --self-check: FAIL"`, contains
  verbatim `"name":"Bash"`, contains `#375` and `#336` (history
  trail). **Does NOT contain** `permissions.defaultMode`,
  `.pyry-agent-run-settings.json`, `per-spawn settings file`,
  or `PTY` — these absence-checks pin the operator-string AC.
- `TestRunAgentRun_SelfCheckShortCircuit` — passing only
  `--self-check` (no production flags) routes to the diagnostic
  verb; unchanged behaviourally.

The `captureClaudeVersion` helper stays as-is. The fake-claude
shell wrapper short-circuits `--version` to a literal echo, exactly
as today (`cmd/pyry/agent_run_selfcheck_test.go:96-99`); that
sub-shape carries over to the new wrapper-less form via a
`-test.run` mode that recognises `--version` as the first argv and
prints `"fake-claude 0.0.0"` before exiting.

### Open questions

- **None blocking implementation.** The shape of every removed
  feature (settings file, trust dialog, JSONL tail) has a direct
  no-op replacement; the shape of every added feature (stdout
  reader, pipe wiring) is already proven in `streamrunner` and
  `jsonl.Reader`.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. There is one trust boundary:
  claude's stdout (untrusted) → `jsonl.Reader` (parser) →
  `bashInvokedInRaw` (structural-typed match) → `Result.Evidence`
  (operator-visible bytes on FAIL only). The boundary is explicit
  (single goroutine, single function), the parser is already in
  tree and audited, and `Result.Evidence` is the package's
  documented SECURITY exception. Downstream callers (the CLI
  wrapper) handle `Result.Evidence` as opaque bytes — no decode, no
  re-parse.

- **[Tokens / secrets / credentials]** No findings. The self-check
  does not handle tokens or secrets. `ANTHROPIC_API_KEY` is consumed
  by claude itself, inherited through the environment via
  `streamrunner.Run`'s `cmd.Env = append(os.Environ(), cfg.Env...)`.
  No new code path touches the variable; production agent-run uses
  the same inheritance.

- **[File operations]** No findings. The new design WRITES no files
  (the system-prompt file, settings file, and trust-marker
  operations are all DELETED). It READS no files (the JSONL tail
  watcher is DELETED). The only filesystem interaction is
  `os.MkdirTemp` in the CLI wrapper for `cfg.WorkDir` — same
  behaviour as today, no path-traversal surface (no user input
  flows into the `os.MkdirTemp` prefix or pattern).

- **[Subprocess / external command execution]** No findings.
  `streamrunner.Run` is the spawn primitive; argv is fully
  hardcoded in `SelfCheckDenyDefault` (no operator-controlled
  values reach `Args`). `cfg.Prompt` defaults to the hardcoded
  `canonicalPrompt` and is delivered to the child via the
  stream-json stdin envelope (`streamrunner` JSON-encodes it, so
  embedded shell metacharacters are inert). `cfg.Env` is appended
  to `os.Environ()`; tests use it to inject helper-mode flags;
  production passes nil. No `shell: true` equivalent (Go has no
  such thing for `exec.CommandContext`). SIGTERM/SIGKILL teardown
  is handled by `streamrunner`'s `cmd.Cancel` + `WaitDelay` wiring,
  preserved verbatim.

- **[Cryptographic primitives]** N/A. No cryptographic operations.
  The `crypto/rand` import (used by the deleted `newSessionID`
  helper) goes away; nothing replaces it.

- **[Network & I/O]** No findings. No sockets opened by this
  package. The child writes to a stdout `io.Pipe`; stderr is bound
  to `io.Discard` so child error chatter cannot leak into pyry's
  logs or stderr. The pipe is unbuffered (`io.Pipe`'s contract),
  bounded structurally by `jsonl.Reader.maxLineBytes` (16 MiB) per
  line — sufficient cap for the single short turn this verb drives.

- **[Error messages, logs, telemetry]** No findings. The SECURITY
  note at the top of the package is preserved verbatim and
  enforced structurally: stderr → `io.Discard`, `Event.Raw` never
  logged (only counts + error types), `Result.Evidence` is the
  single documented exception and surfaces only on FAIL where the
  operator NEEDS to see the offending bytes. Operator-facing FAIL
  output (`writeSelfCheckFailMessage`) renders `Evidence`
  inline as the load-bearing finding; no other path touches it.

- **[Concurrency]** No findings. Two goroutines, single
  `errgroup`-owned ctx, single shared `Result` mutated only by the
  watcher goroutine (the spawner goroutine never touches
  `Result`). No locks taken (no shared mutable state across
  goroutines beyond the ctx cancellation). Pipe close discipline
  is explicit in the spawner's deferred cleanup; that is the
  load-bearing concurrency invariant and is documented in the
  Design § Concurrency.

- **[Threat model alignment]** No findings. The threat the
  self-check guards against is unchanged from #336:
  *silent regression of the per-agent tool-allowlist enforcement
  contract.* The mechanism shifts from `permissions.defaultMode`
  to `--allowed-tools` + `--dangerously-skip-permissions`; both
  are single Anthropic-controlled strings, and a regression of
  either silently dissolves the same per-agent security boundary
  the dispatcher relies on. The spec's exhibit prompt + Bash
  detector are unchanged, so the load-bearing
  "would-claude-actually-call-Bash-when-asked" probe is preserved
  byte-for-byte.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-05-16
