# #470 — `pyry agent-run` cutover from streamrunner to ptyrunner with `PYRY_USE_STREAMJSON` fallback

Sub-issue of [#329](https://github.com/pyrycode/pyrycode/issues/329). Blocked on [#478](https://github.com/pyrycode/pyrycode/issues/478) (ptyrunner JSONL tail + stream-json emit) and [#479](https://github.com/pyrycode/pyrycode/issues/479) (ptyrunner pyry-side budget + watchdog). Reuses [#475](https://github.com/pyrycode/pyrycode/issues/475) (`internal/agentrun/trust`) and [#476](https://github.com/pyrycode/pyrycode/issues/476) (`internal/agentrun/settings`). Sibling [#482](https://github.com/pyrycode/pyrycode/issues/482) (ptyrunner-level real-claude byte-equivalence smoke) is a peer check at a different layer; this slice's smoke covers the cmd→helpers→ptyrunner wiring, not the wire-shape equivalence.

## Files to read first

- `cmd/pyry/agent_run.go` (entire file, 283 lines) — the only production file this slice modifies. Pay close attention to the existing `runAgentRun` body (lines 204–248): the env-var branch and the trust/settings/sessionID/ptyrunner wiring slot in here. `agentRunUsageDescription` (lines 40–48) is rewritten by AC #4. `buildClaudeArgs` (lines 270–282) is renamed.
- `cmd/pyry/agent_run_test.go` (entire file, 703 lines) — all existing tests must stay green. `TestAgentRunStreamJSONFake` (lines 43–85) is the fake-claude entry point keyed by `PYRY_AGENT_RUN_FAKE=1`; reused for the streamrunner-path tests. `configureFakeClaude` (lines 116–128) installs the shell wrapper. `newValidArgsFixture` (lines 139–172) is the test fixture. `TestAgentRunUsageDescription` (lines 507–522) extends with a new required substring.
- `internal/agentrun/streamrunner/runner.go:39-79` — `streamrunner.Config` struct shape (`ClaudeBin`, `WorkDir`, `Args`, `PromptBytes`, `Stdout`, `Stderr`, `Env`, `Logger`). The fallback path constructs this verbatim from today's wiring.
- `internal/agentrun/ptyrunner/runner.go:77-163` — `ptyrunner.Config` struct shape (required: `ClaudeBin`, `WorkDir`, `SessionID`, `SettingsPath`, `SystemPrompt`, `Model`, `Effort`, `MaxTurns`, `PromptBytes`, `Stdout`, `Stderr`; optional: `HomeDir`, `Env`, `WatchdogTick`, `WatchdogTrackerOpts`, `Logger`). The default path constructs this from `parsed agentRunArgs` + `trust.MarkWorkdirTrusted`'s realpath return + `settings.WriteSettings`'s tempfile path + a freshly-minted UUID.
- `internal/agentrun/ptyrunner/runner.go:209-242` — `Run`'s required-field validation chain. The cmd layer must populate every required field; missing any one returns `"ptyrunner: <field> required"`. The spec's wiring satisfies all checks.
- `internal/agentrun/trust/trust.go:28-46` — `MarkWorkdirTrusted(workdir string) (realpath string, err error)`. Returns the symlink-resolved absolute path on success — that realpath becomes `ptyrunner.Config.WorkDir` so both ~/.claude.json's `projects[realpath]` key and claude's PTY-allocated working directory agree. Idempotent + atomic temp+rename; safe to call on every spawn.
- `internal/agentrun/settings/settings.go:33-86` — `WriteSettings(allowedTools []string) (path string, err error)`. Returns a `pyry-agent-run-settings-*.json` path under `os.TempDir()`. Caller responsibility: `defer os.Remove(path)` on success. Internal error path already cleans up its own tempfile.
- `internal/sessions/id.go:18-32` — `sessions.NewID() (SessionID, error)`. Returns a canonical UUIDv4 string newtype via `crypto/rand`. Errors only when the system RNG fails. Used here for the `--session-id` value claude consumes; the daemon's session pool is NOT involved.
- `internal/sessions/id.go:43-69` — `sessions.ValidID(s string) bool`. Used by `TestRunAgentRun_PtyPath_SessionIDIsUUIDv4` to assert the minted ID has canonical shape.
- `cmd/pyry/agent_run_selfcheck.go` (full file, 105 lines) — prior art for how `runAgentRunSelfCheck` materialises a workdir, resolves `claudeBin`, and delegates to a sibling agentrun helper. The default-path wiring (`runAgentRunPty`) mirrors the same flat, no-cancel-magic shape.
- `internal/agentrun/selfcheck/selfcheck.go:118-182` — prior art for how an agentrun consumer composes a `streamrunner.Config` against the same fake-claude pattern the streamrunner-path tests reuse.
- `docs/knowledge/codebase/471.md`, `docs/knowledge/codebase/475.md`, `docs/knowledge/codebase/476.md` — read for context on the prereq slices' decisions (test-seam patterns, idempotency contracts, tempfile cleanup conventions). The cmd-layer wiring inherits these contracts verbatim.

## Context

`pyry agent-run` is the headless verb the dispatcher invokes once per agent turn. Today it spawns claude as a stream-json subprocess via `internal/agentrun/streamrunner` and forwards claude's stream-json stdout to the dispatcher byte-for-byte.

The PTY-drive pivot (recon at `📋 Projects/2026-04-10 - Pyrycode/PTY-Drive Recon.md`, status update 2026-05-19) moves the verb onto interactive-TUI claude under a PTY — the surface Anthropic's 2026-06-15 billing policy explicitly names as subscription-eligible. The pivot's three implementation slices have either landed or are in flight:

- [#471](https://github.com/pyrycode/pyrycode/issues/471) (CLOSED) — `ptyrunner` spawn + idle wait + `WritePrompt` + clean shutdown
- [#475](https://github.com/pyrycode/pyrycode/issues/475) (CLOSED) — `internal/agentrun/trust.MarkWorkdirTrusted` workspace-trust pre-write
- [#476](https://github.com/pyrycode/pyrycode/issues/476) (CLOSED) — `internal/agentrun/settings.WriteSettings` deny-default per-spawn settings JSON
- [#478](https://github.com/pyrycode/pyrycode/issues/478) (OPEN) — ptyrunner JSONL tail + stream-json emit + end-of-turn classification
- [#479](https://github.com/pyrycode/pyrycode/issues/479) (OPEN) — ptyrunner pyry-side `MaxTurns` budget + watchdog + shared ctx-cancel teardown

This slice is the cmd-layer cutover: `runAgentRun` reads `PYRY_USE_STREAMJSON` from the environment and branches to the new ptyrunner path (default) or the existing streamrunner path (when the env var equals `"1"`).

Operator decision 2026-05-19: streamrunner stays as a sibling indefinitely so empirical billing-classification comparison is possible after 2026-06-15. `PYRY_USE_STREAMJSON=1` is the operator-facing rollback knob; the historical `PYRY_USE_LEGACY_CLAUDE=1` is obsolete and the new env var is intentionally distinct to avoid operator confusion.

The dispatcher submodule is NOT changed by this ticket. `internal/agentrun/streamjson/emitter.go` produces the same wire shape under both drive modes — the dispatcher's stream-json parser is satisfied by either path. After this merges, the four agents-repo dispatcher forks pick up the new pyry behaviour at their next `pyry update`.

## Design

### Package boundary

Stays at `cmd/pyry/`. One production file modified:

| File | Change |
| --- | --- |
| `cmd/pyry/agent_run.go` | (a) Add `runAgentRun` env-var branch — `os.Getenv("PYRY_USE_STREAMJSON") == "1"` dispatches to a new `runAgentRunStreamRunner` helper (today's body, extracted), anything else dispatches to a new `runAgentRunPty` helper that wires trust + settings + UUID + ptyrunner.Run. (b) Rename `buildClaudeArgs` → `buildStreamRunnerClaudeArgs` (the function is now specific to the streamrunner argv shape; ptyrunner owns its own `buildArgs` in its package). (c) Rewrite `agentRunUsageDescription` prose. (d) Add four package-level function-variable seams (`trustMark`, `settingsWrite`, `ptyRun`, `newSessionID`) so tests can inject failures at each call site without spawning real claude. |

No new production files. No new packages. No new exported types. No signature changes to any existing public API.

Test file changes (both touch the same file because the existing `agent_run_test.go` already mixes wiring + flag-parsing tests; splitting would split related coverage across files for no readability win):

| File | Change |
| --- | --- |
| `cmd/pyry/agent_run_test.go` | (a) Pin `PYRY_USE_STREAMJSON=1` via `t.Setenv` inside `configureFakeClaude` so the two existing fake-claude wiring tests (`TestRunAgentRun_StreamJSON_Clean`, `TestRunAgentRun_StreamJSON_NonZeroExit`) cover the streamrunner branch. (b) Rename `TestBuildClaudeArgs_Shape` → `TestBuildStreamRunnerClaudeArgs_Shape` and update the function reference inside the test body. (c) Extend `TestAgentRunUsageDescription` with a `"PYRY_USE_STREAMJSON"` required-substring assertion. (d) Add ten new tests for the env-var branching, the ptyrunner-path failure surfaces, settings cleanup, session-ID minting, allowed-tools round-trip, and the real-claude env-gated smoke. |

### Environment variable contract — `PYRY_USE_STREAMJSON`

- `PYRY_USE_STREAMJSON=1` → streamrunner path (rollback / billing-comparison mode)
- `PYRY_USE_STREAMJSON=""` (unset) → ptyrunner path (new default)
- `PYRY_USE_STREAMJSON=<anything else>` → ptyrunner path

The contract is intentionally strict: only the exact string `"1"` selects the legacy path. `"true"`, `"yes"`, `"y"`, `"on"`, `"streamjson"` — all silently fall through to the ptyrunner default. This mirrors how `PYRY_AGENT_RUN_FAKE` and `GO_PTYRUNNER_HELPER` work today (boolean-shape switches keyed by `=="1"`) and avoids the "did this string mean true?" ambiguity that bit `PYRY_USE_LEGACY_CLAUDE` historically. A test pins this exact predicate so a future contributor cannot quietly widen the truthy set.

### `runAgentRun` — top-level dispatch

The new shape:

```
runAgentRun(stdout, args):
  --self-check guard      → runAgentRunSelfCheck(stdout)        (unchanged)
  parseAgentRunArgs(args) → parsed                              (unchanged)
  os.ReadFile(promptFile) → promptBytes                         (unchanged)
  resolve claudeBin        (PYRY_CLAUDE_BIN env, else "claude")  (unchanged)
  ctx := signal.NotifyContext(..., SIGTERM, SIGINT)             (unchanged)

  if os.Getenv("PYRY_USE_STREAMJSON") == "1":
      err = runAgentRunStreamRunner(ctx, stdout, parsed, claudeBin, promptBytes)
  else:
      err = runAgentRunPty(ctx, stdout, parsed, claudeBin, promptBytes)

  if err == nil || errors.Is(err, context.Canceled): return nil
  return fmt.Errorf("agent-run: %w", err)
```

The top-level wrap (`fmt.Errorf("agent-run: %w", err)`) is preserved verbatim. Both helper functions return wrapped-but-not-prefixed errors so the `agent-run:` prefix is added in exactly one place — same as today.

### `runAgentRunStreamRunner` — extracted helper

Lifts the existing streamrunner wiring (today's `runAgentRun` body lines 233–247) into a helper of the same shape:

```
runAgentRunStreamRunner(ctx, stdout, parsed, claudeBin, promptBytes) error:
    return streamrunner.Run(ctx, streamrunner.Config{
        ClaudeBin:   claudeBin,
        WorkDir:     parsed.workdir,
        Args:        buildStreamRunnerClaudeArgs(parsed),
        PromptBytes: promptBytes,
        Stdout:      stdout,
        Stderr:      os.Stderr,
    })
```

Byte-equivalent behaviour with today — same Config fields, same argv shape, same error propagation. The only change is the function rename `buildClaudeArgs` → `buildStreamRunnerClaudeArgs`. `streamrunner.Run`'s `context.Canceled` collapse-to-nil is preserved through the top-level guard.

### `runAgentRunPty` — new helper

```
runAgentRunPty(ctx, stdout, parsed, claudeBin, promptBytes) error:
    1. realpath, err := trustMark(parsed.workdir)
       if err != nil: return fmt.Errorf("mark workdir trusted in ~/.claude.json: %w", err)

    2. settingsPath, err := settingsWrite(parsed.allowedTools)
       if err != nil: return fmt.Errorf("write per-spawn settings: %w", err)
       defer os.Remove(settingsPath)

    3. sid, err := newSessionID()
       if err != nil: return fmt.Errorf("mint session id: %w", err)

    4. return ptyRun(ctx, ptyrunner.Config{
           ClaudeBin:    claudeBin,
           WorkDir:      realpath,                              // trust's symlink-resolved path
           SessionID:    string(sid),
           SettingsPath: settingsPath,
           SystemPrompt: parsed.systemPromptFile,
           Model:        parsed.model,
           Effort:       parsed.effort,
           MaxTurns:     parsed.maxTurns,
           PromptBytes:  promptBytes,
           Stdout:       stdout,
           Stderr:       os.Stderr,
       })
```

Five contract notes:

1. **`realpath` is the trust function's symlink-resolved return**, not `parsed.workdir`. Reason: claude resolves the workdir before keying `projects[<realpath>]` in `~/.claude.json` — if pyry passes a symlink path to `--cwd` (via `cmd.Dir`) and the trust pre-write keyed the realpath, claude's modal check would miss the entry and the trust modal would render. Passing `realpath` to `ptyrunner.Config.WorkDir` keeps the two keys aligned and structurally satisfies the modal-skip invariant. (Pinned by `TestRunAgentRun_PtyPath_WorkDirIsTrustResolvedRealpath`.)

2. **`defer os.Remove(settingsPath)` runs before `ptyRun` returns** because Go's defer LIFO fires at function exit. Settings tempfile is cleaned up on every exit path — `ptyRun` success, `ptyRun` error, sessionID error, ctx-cancel. AC #2 ("removed on exit (success or failure)") is satisfied structurally. The defer is registered AFTER the err-check on `settingsWrite` so a failure during settings write doesn't try to remove an empty/non-existent path. Trust-write failure (step 1) returns before `settingsWrite` is called, so no settings-file leak on the trust-failure branch.

3. **Error prefix `agent-run: ` is added once, at the top level** in `runAgentRun`. The helper returns the unprefixed wrapped chain. So a trust failure surfaces as `agent-run: mark workdir trusted in ~/.claude.json: <underlying>` — AC #3 (trust failure mentions `~/.claude.json`). A settings failure surfaces as `agent-run: write per-spawn settings: <underlying>` — AC #3 (settings failure names the settings step). A ptyRun failure surfaces as `agent-run: <ptyrunner error>` — AC #3 (ptyrunner errors propagate through agent-run's existing wrapping).

4. **`newSessionID` failure is defensive.** `sessions.NewID` only fails on `crypto/rand` exhaustion, which is fatal at the process level. We still wrap-and-return rather than `log.Fatal` because the rest of the codebase (e.g. `identity.NewServerID`) treats RNG failure as a returnable error; the cmd-layer surface stays uniform.

5. **No new defers, no new goroutines, no new locks.** The helper is a flat sequence of three I/O calls and one delegated call. ptyrunner.Run owns its own goroutines + cancellation + signal handling internally; the cmd layer adds nothing on top of what already exists in #471 + #478 + #479.

### `buildClaudeArgs` rename + signature unchanged

```
buildStreamRunnerClaudeArgs(parsed agentRunArgs) []string
```

Same body verbatim — only the function name changes. The renamed function is referenced exactly twice: production call site inside `runAgentRunStreamRunner` and the renamed test `TestBuildStreamRunnerClaudeArgs_Shape`. Documents intent: the argv shape with `--input-format stream-json --output-format stream-json --verbose --dangerously-skip-permissions --allowed-tools …` is specific to the streamrunner stream-json subprocess invocation. ptyrunner's argv (`--session-id`, `--settings`, `--permission-mode default`) is owned by `internal/agentrun/ptyrunner/runner.go`'s `buildArgs` and is intentionally NOT exposed at the cmd layer.

### `agentRunUsageDescription` rewrite — AC #4

The new prose (a single multi-line raw string constant, same shape as today):

> Drive a single supervised claude turn headlessly. By default, spawns claude as an interactive-TUI process under a PTY (the surface Anthropic's 2026-06-15 billing policy names as subscription-eligible), pre-marks the workdir as trusted in ~/.claude.json, writes a per-spawn deny-default permissions JSON, delivers the user prompt via a bracketed-paste sequence, tails claude's session JSONL, and re-emits each event as stream-json on stdout for the dispatcher to consume. --max-turns is enforced by pyry (interactive claude does not honour it). --allowed-tools is the load-bearing tool gate, written into the per-spawn settings file as a deny-default allow-list.
>
> Set PYRY_USE_STREAMJSON=1 to fall back to the legacy stream-json subprocess path (claude -p with --output-format stream-json) for billing-classification experimentation. The fallback is operator-facing only; the dispatcher receives the same stream-json wire shape under both modes.

`TestAgentRunUsageDescription` already pins three required substrings (`stream-json`, `--max-turns`, `--allowed-tools`). The new prose contains all three plus a fourth (`PYRY_USE_STREAMJSON`) which the extended test asserts. The "scaffold only" stale-disclaimer check stays as a regression guard; the new prose does not contain that string.

### Test-seam package-level variables

Four package-level variables in `cmd/pyry/agent_run.go`:

```
var (
    trustMark     = trust.MarkWorkdirTrusted
    settingsWrite = settings.WriteSettings
    ptyRun        = ptyrunner.Run
    newSessionID  = sessions.NewID
)
```

Each is the default production-wired function value. Tests override individually via `t.Cleanup` restore-on-exit boilerplate. This is the minimum-surface seam that enables deterministic failure-surface coverage without spawning real or fake claude under PTY (which would otherwise require a TestMain dispatcher in cmd/pyry — a much larger refactor and a maintenance burden parallel to the existing `TestAgentRunStreamJSONFake` pattern).

The four seams are unexported and live next to the package-level constants (`validEfforts`, `agentRunUsageDescription`). Production never assigns to them; only `_test.go` files do. A code-reviewer alert: if a future PR introduces a fifth seam, the architect should pause and ask whether the helper is decomposing too aggressively or whether the test layer needs a different approach.

### Concurrency model

Single-goroutine sequential. The cmd-layer wiring spawns no goroutines itself. The two helper functions (`runAgentRunStreamRunner`, `runAgentRunPty`) execute synchronously, calling their delegated `Run` (streamrunner or ptyrunner) which owns all internal goroutine lifecycle. ctx is the existing `signal.NotifyContext(ctx, SIGTERM, SIGINT)`; SIGTERM/SIGINT cancellation flows through whichever Run was called, and both Runs collapse `context.Canceled` to nil per their package contracts. The cmd layer's existing `errors.Is(err, context.Canceled)` guard at the top-level preserves the operator-shutdown-is-success semantics.

### Error handling

| Failure | Wrap | Result message |
| --- | --- | --- |
| `trustMark` returns err | `fmt.Errorf("mark workdir trusted in ~/.claude.json: %w", err)` | `agent-run: mark workdir trusted in ~/.claude.json: <underlying>` |
| `settingsWrite` returns err | `fmt.Errorf("write per-spawn settings: %w", err)` | `agent-run: write per-spawn settings: <underlying>` |
| `newSessionID` returns err | `fmt.Errorf("mint session id: %w", err)` | `agent-run: mint session id: <underlying>` |
| `ptyRun` returns err | unwrapped, propagated | `agent-run: <ptyrunner error>` |
| `streamrunner.Run` returns err | unwrapped, propagated | `agent-run: <streamrunner error>` |
| `runAgentRun` `ctx.Canceled` | collapsed via `errors.Is(err, context.Canceled)` | nil (operator shutdown is success) |

The three custom-prefix wraps live inside `runAgentRunPty`. The streamrunner-path test coverage for `agent-run: ` prefix presence (`TestRunAgentRun_StreamJSON_NonZeroExit`) is preserved with the env-var pin.

### Dependency direction

`cmd/pyry` already imports `internal/agentrun/streamrunner`. New imports added by this slice:

- `internal/agentrun/ptyrunner` — for `ptyrunner.Config` + `ptyrunner.Run`
- `internal/agentrun/trust` — for `trust.MarkWorkdirTrusted`
- `internal/agentrun/settings` — for `settings.WriteSettings`
- `internal/sessions` — already imported elsewhere in cmd/pyry (sessions verb); reused here for `sessions.NewID`

`cmd/pyry` is the binary's entry-point package; it sits at the top of the dependency DAG. Every `internal/*` package that flows into `cmd/pyry` is upstream-of-nothing inside this binary, so no new dependency cycles are possible. Verify with:

```
go list -deps ./cmd/pyry/... | grep pyrycode/internal/agentrun
```

Expected: lists `internal/agentrun`, `internal/agentrun/ptyrunner`, `internal/agentrun/streamrunner`, `internal/agentrun/trust`, `internal/agentrun/settings`, `internal/agentrun/jsonl`, `internal/agentrun/jsonl/tail`, `internal/agentrun/streamjson`, `internal/agentrun/budget`, `internal/agentrun/selfcheck` — no surprises.

## Testing strategy

All tests live in `cmd/pyry/agent_run_test.go`. The existing fake-claude infrastructure (`TestAgentRunStreamJSONFake`, `configureFakeClaude`) is reused for the streamrunner-branch tests; the ptyrunner-branch tests use the four package-level seams to drive each surface deterministically. The real-claude smoke is env-gated.

### Existing tests — minimal updates

| Test | Update |
| --- | --- |
| `TestRunAgentRun_StreamJSON_Clean` | Add `t.Setenv("PYRY_USE_STREAMJSON", "1")` inside `configureFakeClaude` (single edit covers both consumers). Test logic unchanged. |
| `TestRunAgentRun_StreamJSON_NonZeroExit` | Same `configureFakeClaude` update applies; no per-test change. |
| `TestBuildClaudeArgs_Shape` | Rename to `TestBuildStreamRunnerClaudeArgs_Shape` and update the function reference inside the test body. Test body unchanged. |
| `TestAgentRunUsageDescription` | Add `"PYRY_USE_STREAMJSON"` to the required-substring list. Add `"interactive"` or `"PTY"` to lock the new-default disclosure. |

### Env-var branching — three new tests

Each uses `t.Setenv("PYRY_USE_STREAMJSON", <value>)` to pin the env, and stubs `ptyRun` / `streamRun` (via a fifth optional seam — see below) to trip a `t.Fatal("wrong path")` on the wrong branch.

A fifth seam clarification: the streamrunner branch is exercised via the existing fake-claude pattern, not a seam. For the "wrong branch did NOT run" assertion, the test stubs `ptyRun` to `t.Fatal` (the streamrunner path runs to completion through the fake) — that's enough to pin the branch. We do NOT introduce a `streamRun` seam.

- **`TestRunAgentRun_DispatchesToPtyRunnerByDefault`** — env unset; stub `ptyRun` to capture the Config and return nil. Assert `ptyRun` was called exactly once and the captured Config's required fields are populated.
- **`TestRunAgentRun_EnvSet1DispatchesToStreamRunner`** — `PYRY_USE_STREAMJSON=1`; `configureFakeClaude` + stub `ptyRun` to `t.Fatal("ptyrunner called on streamrunner branch")`. Assert clean exit via the fake-claude `clean` mode.
- **`TestRunAgentRun_EnvNon1ValueDispatchesToPtyRunner`** — table-driven covering `"true"`, `"yes"`, `"on"`, `"streamjson"`, `"0"`, `"false"`, `""` (explicit empty); stub `ptyRun` to count calls. Assert `ptyRun` was called for every value except `"1"`. This locks the "only exact `1` selects legacy" predicate against future contributor drift.

### Ptyrunner-path failure surfaces — three new tests

Each tests a single seam failure in isolation.

- **`TestRunAgentRun_PtyPath_TrustFailure_MentionsClaudeJson`** — stub `trustMark` to return `errors.New("simulated")`; stub `ptyRun` to `t.Fatal("ptyrunner called after trust failure")`. Assert the returned error message contains both `~/.claude.json` and `agent-run:`.
- **`TestRunAgentRun_PtyPath_SettingsFailure_NamesSettingsStep`** — stub `trustMark` to return a valid realpath; stub `settingsWrite` to return `errors.New("simulated")`; stub `ptyRun` to `t.Fatal`. Assert the returned error message contains `settings` and `agent-run:`.
- **`TestRunAgentRun_PtyPath_PtyRunError_Wrapped`** — stub `trustMark` + `settingsWrite` to return valid values; stub `ptyRun` to return `errors.New("simulated ptyrunner failure")`. Assert the returned error starts with `agent-run: ` and contains `simulated ptyrunner failure`.

### Ptyrunner-path settings-cleanup — two new tests

Both stub `trustMark` to return a valid realpath and stub `settingsWrite` to write an actual tempfile (real call into `settings.WriteSettings` via the seam fallthrough — i.e., the test does NOT replace `settingsWrite` for these two cases, exercising the production call into `os.TempDir()`). The test captures the returned path via a wrapped stub:

```
var capturedPath string
settingsWrite = func(tools []string) (string, error) {
    p, err := settings.WriteSettings(tools)
    capturedPath = p
    return p, err
}
```

- **`TestRunAgentRun_PtyPath_SettingsRemovedOnSuccess`** — stub `ptyRun` to return nil; assert `os.Stat(capturedPath)` returns `fs.ErrNotExist` after `runAgentRun` returns.
- **`TestRunAgentRun_PtyPath_SettingsRemovedOnFailure`** — stub `ptyRun` to return `errors.New("boom")`; assert `os.Stat(capturedPath)` returns `fs.ErrNotExist` after `runAgentRun` returns.

### Ptyrunner-path config-wiring — three new tests

Each stubs the three seams (`trustMark`, `settingsWrite`, `ptyRun`) and captures the `ptyrunner.Config` argument passed to `ptyRun`. Assertions on the captured Config:

- **`TestRunAgentRun_PtyPath_WorkDirIsTrustResolvedRealpath`** — stub `trustMark` to return a sentinel realpath `"/sentinel/realpath"`. Assert `capturedCfg.WorkDir == "/sentinel/realpath"`. Pins the realpath-not-parsed-workdir contract from § Design § runAgentRunPty contract note 1.
- **`TestRunAgentRun_PtyPath_SessionIDIsUUIDv4`** — stub `ptyRun` to capture `capturedCfg.SessionID`. Assert `sessions.ValidID(capturedCfg.SessionID)` is true. Pins the UUIDv4 shape against any future "we'll just use a short hash" drift.
- **`TestRunAgentRun_PtyPath_ConfigWiring`** — table-driven over two `agentRunArgs` fixtures (model `sonnet-4-6` / `opus-4-7`, max-turns `3` / `12`, effort `medium` / `max`, distinct allowed-tools and system-prompt paths). For each: assert the captured Config's `ClaudeBin`, `SettingsPath`, `SystemPrompt`, `Model`, `Effort`, `MaxTurns`, `PromptBytes`, `Stdout`, `Stderr` all round-trip from the parsed args byte-for-byte. SessionID + WorkDir are covered by the two prior pins.

### Allowed-tools round-trip — one new test

- **`TestRunAgentRun_PtyPath_AllowedToolsPassedToSettings`** — stub `settingsWrite` to capture the `[]string` slice argument. Run `runAgentRun` with `--allowed-tools "Read, Bash, Edit"`. Assert the captured slice equals `["Read", "Bash", "Edit"]` (byte-for-byte order + content). Pins the deny-default allowlist's load-bearing path from the security review § Threat model alignment.

### Real-claude smoke — env-gated, two subtests

- **`TestRunAgentRun_RealClaude`** — `t.Skip("set PYRY_E2E_REAL_CLAUDE=1 to run")` unless that env var is set. Two `t.Run` subtests:
  - `default_pty_path` — env unset for `PYRY_USE_STREAMJSON`; prompt file content `"Reply with only the literal word OK and nothing else."`; allowed-tools `"Read"`; max-turns `1`; effort `"low"`; model `"sonnet"`; capture stdout via `bytes.Buffer`; assert `runAgentRun` returns nil within a 90s deadline; assert the captured stdout contains at least one line that JSON-decodes with `type:"system"`, one with `type:"assistant"`, and one with `type:"result"` (mirrors the dispatcher's expected wire shape).
  - `fallback_streamrunner_path` — same prompt + budget; `t.Setenv("PYRY_USE_STREAMJSON", "1")`; same assertions.

The smoke is gated by env (not a build tag) so a contributor can run `PYRY_E2E_REAL_CLAUDE=1 go test ./cmd/pyry -run TestRunAgentRun_RealClaude` without flipping build flags. CI's default `go test ./...` skips cleanly. The 90s deadline matches the `selfcheck` package's `defaultSelfCheckTimeout`; one short claude turn fits well inside it.

### Coverage summary

- Existing streamrunner-branch tests: 2 (unchanged behaviour, env-var pinned)
- Renamed test: 1 (`TestBuildStreamRunnerClaudeArgs_Shape`)
- Extended test: 1 (`TestAgentRunUsageDescription`)
- New tests for env-var branching: 3
- New tests for ptyrunner-path failure surfaces: 3
- New tests for settings cleanup: 2
- New tests for config wiring: 3
- New test for allowed-tools round-trip: 1
- New real-claude smoke (env-gated): 1 with 2 subtests

Total: 17 test functions covering this slice's wiring (2 unchanged, 1 renamed, 1 extended, 13 new).

## Out of scope

- ptyrunner internal behaviour (JSONL tail, stream-json emit, end-of-turn classification, budget, watchdog) — [#478](https://github.com/pyrycode/pyrycode/issues/478) + [#479](https://github.com/pyrycode/pyrycode/issues/479).
- ptyrunner-level real-claude byte-equivalence smoke test (validates dispatcher wire-shape contract against streamrunner baseline) — [#482](https://github.com/pyrycode/pyrycode/issues/482). This slice's `TestRunAgentRun_RealClaude` covers the cmd-layer wiring at the cmd boundary; #482's covers the wire-shape equivalence at the ptyrunner boundary. Different scopes by design.
- `pyry agent-run --self-check` adaptation for the ptyrunner default path — [#473](https://github.com/pyrycode/pyrycode/issues/473) (follow-up after this lands).
- streamrunner package deletion — operator decision 2026-05-19, streamrunner stays as a sibling indefinitely for billing-classification comparison.
- tui-driver e2e harness (`pyrycode/tui-driver` #31) — complementary, not blocking.
- **`docs/knowledge/codebase/470.md` is NOT a developer AC.** The ticket body's last AC pushes a knowledge-doc deliverable onto the developer turn budget. The architect's CLAUDE.md (§ Constraints) prohibits including codebase knowledge docs as a developer deliverable — that file is owned by the documentation phase, which writes it from this spec + the merged diff after the PR lands. The developer's worktree should only mutate `cmd/pyry/agent_run.go` and `cmd/pyry/agent_run_test.go`. The knowledge doc still gets written; just not by the developer turn.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings — the cmd-layer call site does not introduce a new trust boundary. `parsed.workdir` is operator-controlled via the `--workdir` CLI flag, validated as a directory by `requireDir` in `parseAgentRunArgs`, and resolved through `filepath.EvalSymlinks` inside `trust.MarkWorkdirTrusted` before being used as the `projects[<realpath>]` key in `~/.claude.json`. The realpath returned by trust then becomes `ptyrunner.Config.WorkDir`, structurally keeping the trust-modal-skip invariant aligned with claude's own resolution. Same as today's streamrunner path (`cmd.Dir = parsed.workdir`).

- **[Tokens, secrets, credentials]** No findings — no tokens generated, stored, or logged by this slice. `~/.claude.json` may contain claude tokens but the trust package's pass-through view (`UseNumber()` + `map[string]any` preservation) ensures we never read, log, or modify any field outside `projects[<realpath>].hasTrustDialogAccepted`. Both `trust.MarkWorkdirTrusted` and `settings.WriteSettings` carry "MUST NOT log file contents" discipline in their package docs (file-locked); the cmd-layer call sites add no log calls.

- **[File operations]** No findings —
  - Path traversal: `parsed.workdir` flows through `filepath.EvalSymlinks` inside trust before becoming a filesystem operand; `settingsPath` is `os.CreateTemp` under `os.TempDir()` (OS-randomized infix, no user component); `parsed.systemPromptFile` is `requireRegularFile`-validated at parse time, passed as an argv value (not opened by us — claude opens it).
  - TOCTOU: `parsed.promptFile` is stat'd by `requireRegularFile` then read by `os.ReadFile` (one-shot CLI invoked by a trusted dispatcher; this gap exists today on the streamrunner path and is not widened here).
  - Permissions: trust preserves existing mode or uses `0o600` for new `~/.claude.json`. Settings uses `os.CreateTemp` default (`0o600`).
  - Atomic writes: trust uses temp+rename. Settings is per-spawn ephemeral.
  - Symlinks: trust uses `EvalSymlinks` intentionally (matches claude's own behaviour); settings uses `os.CreateTemp` (no symlink target).

- **[Subprocess / external command execution]** No findings — claude is invoked via `exec.CommandContext` inside `ptyrunner.Run` (audited in #471). The cmd layer passes `claudeBin` (env-or-default), `parsed.systemPromptFile` (path validated at parse), `parsed.model` / `parsed.effort` (string allowlist validated at parse for effort), `parsed.maxTurns` (int validated > 0), `parsed.allowedTools` (non-empty slice validated at parse), `settingsPath` (OS-randomized tempfile path), `string(sid)` (UUIDv4 from `crypto/rand`). No shell interpretation, no `sh -c`, no string concatenation into argv. The env-var `PYRY_USE_STREAMJSON` is compared against the literal `"1"` only.

- **[Cryptographic primitives]** No findings — `sessions.NewID` uses `crypto/rand` (audited in #155). No other crypto in this slice.

- **[Network & I/O]** N/A — cmd layer does no network I/O.

- **[Error messages, logs, telemetry]** No findings — trust failure message intentionally includes `~/.claude.json` per AC #3 (operator-actionable; the file path is the load-bearing diagnostic). Underlying `trust.MarkWorkdirTrusted` errors never include file contents per the trust package's contract (audited in #475). Settings failure surfaces `agent-run: write per-spawn settings: <underlying>`; `settings.WriteSettings`'s errors include the failing operation name but never the allowedTools slice or the JSON payload (audited in #476). ptyrunner error propagation: ptyrunner's package-level discipline (no PromptBytes content, no buffer substrings) is inherited at the cmd layer because we never inspect ptyrunner's error chain — only propagate it. The cmd layer adds zero `slog` calls.

- **[Concurrency]** No findings — single-goroutine sequential body; no locks, no channels, no `go` statements added. `defer os.Remove(settingsPath)` runs in the same goroutine on return. `ctx` is inherited from `runAgentRun` and threaded to whichever Run is delegated.

- **[Threat model alignment]** No findings — the load-bearing security boundary this slice consumes is the deny-default per-spawn settings file (`internal/agentrun/settings`). If `parsed.allowedTools` is mis-routed or stripped before reaching `settings.WriteSettings`, the resulting deny-default file might allow tools the operator didn't intend. The new test `TestRunAgentRun_PtyPath_AllowedToolsPassedToSettings` is the coverage anchor — it asserts byte-for-byte that the slice handed to `settingsWrite` equals `parsed.allowedTools`. Combined with `parseAgentRunArgs`'s existing parse-validation tests (`TestParseAgentRunArgs_AllowedToolsForms`, `TestParseAgentRunArgs_Errors` "allowed-tools missing" / "allowed-tools empty after split"), the path from `--allowed-tools` CLI argument to deny-default settings JSON is locked end-to-end. The deny-default contract itself (claude's `--allowed-tools` allowlist refusing tools outside the list) is empirically protected by `pyry agent-run --self-check` (#336); this slice's ptyrunner-path equivalent is the next ticket (#473).

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-20
