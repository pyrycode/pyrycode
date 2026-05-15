# Spec — Ticket #391: wire stream-json runner into `pyry agent-run` (replaces PTY drive)

Status: ready for developer
Size: S (1 production file edited, ~80 LOC net delta, 0 consumer call sites — `runAgentRun` keeps its signature)
Scope: rewrite the body of `runAgentRun` and `buildClaudeArgs` in `cmd/pyry/agent_run.go`; rewrite the verb's test fakery to match. No new files. No edits outside `cmd/pyry/agent_run*.go`.

## Files to read first

- `cmd/pyry/agent_run.go:187-369` — current `runAgentRun` body + `buildClaudeArgs`. The whole orchestration tail (settings file, trust mark, session-id mint, errgroup, watcher, streamjson emitter, drive) is what gets ripped out and replaced with one `streamrunner.Run` call.
- `internal/agentrun/streamrunner/runner.go:42-175` — `Config` shape and `Run` contract. Read for: required vs optional fields, the `nil-on-clean / nil-on-ctx-cancel / *exec.ExitError-on-non-zero` return semantics, and the `cmd.Cancel` + `WaitDelay` SIGTERM-grace behaviour. The verb's error-mapping mirrors this directly.
- `cmd/pyry/agent_run_test.go:18-131, 399-671` — current PTY-mode fakery and end-to-end tests. The `configureFakeClaude` shell wrapper, `TestAgentRunFakeClaude` JSONL writer, `TestRunAgentRun_EmitsSettingsFile`, `TestRunAgentRun_MarksWorkdirTrusted`, `TestRunAgentRun_DrivesFakeClaude`, `TestNewSessionUUID_Shape`, and `TestBuildClaudeArgs_Shape` are the tests in scope. The arg-parsing tests (`TestParseAgentRunArgs_*`, `TestSplitAllowedTools`) are out of scope and stay byte-identical.
- `internal/agentrun/streamrunner/runner_test.go:36-104` — pattern for a `TestHelperProcess`-style fake-claude that reads stdin and writes stream-json to stdout; the verb's new fake will mirror this shape (modes: clean / exit1 / sleep) but lives under `cmd/pyry/` because the verb is in `package main`.
- `internal/agentrun/streamrunner/helper_test.go` — the canned stream-json sequence the streamrunner test fake emits (`{"type":"system","subtype":"init"}` → `assistant` → `{"type":"result","subtype":"success"}`). The verb's fake should emit the same shape so a real run's stdout structure is exercised.
- `cmd/pyry/agent_run.go:343-369` — current `buildClaudeArgs`. The function's contract changes substantially; the security invariants the existing tests pin (`--permission-mode default` MUST appear, `--allowedTools` MUST NOT appear) are inverted by this ticket because we're now in `-p`-style stream-json mode where the settings file is gone and `--allowed-tools` is the canonical surface. Read the doc-comment for the *reasoning* the new test assertions will replace.
- `cmd/pyry/agent_run_selfcheck.go` (skim only — out of scope) — note that `agent_run_selfcheck.go` still calls `agentrun.WriteSettings` and reads `agentrun.SettingsFilename`. **Leave it untouched.** Its refactor for the stream-json world is #375. This means `internal/agentrun/{drive,trust,settings}.go` and friends remain compiled and exported — we are removing only the verb's runtime call sites, not the packages.
- `internal/agentrun/streamjson/emitter.go:1-80` — confirm what `streamjson.Emitter` does (re-emits parsed JSONL events + composes a `result` trailer). Once `runAgentRun` switches to streamrunner, claude itself emits the canonical `system init` and `result` events on its own stdout, so the emitter's role disappears entirely from the verb's runtime path. Its package stays compiled (used elsewhere — selfcheck path) but no longer imported here.

## Context

The PTY-drive path that `runAgentRun` uses today is the source of the 2026-05-14 `/doctor` poisoning regression: it sleeps a fixed delay, then *blindly types* the prompt into the PTY. Anything claude's interactive startup queues into the input buffer gets concatenated with the prompt. The fix already shipped at the primitive layer in #390 (`internal/agentrun/streamrunner`); this ticket flips the verb over to it.

Once flipped, large parts of the verb's current orchestration become dead weight in the verb's runtime path:

- The per-spawn settings file (`agentrun.WriteSettings`, `.pyry-agent-run-settings.json`) — replaced by `--allowed-tools` and `--dangerously-skip-permissions` on argv.
- The workspace-trust mark (`agentrun.MarkWorkdirTrusted` writing into `~/.claude.json`) — replaced by `--dangerously-skip-permissions` removing the trust dialog entirely.
- The verb-minted session UUID (`newSessionUUID`) — claude mints and reports its own session id in the stream-json `system init` event under stream-json mode.
- The JSONL tail watcher (`internal/agentrun/jsonl/tail`) — claude writes its events directly to its own stdout in stream-json mode; no need to scrape the on-disk JSONL.
- The streamjson event re-emitter (`internal/agentrun/streamjson`) and its trailer composition — claude itself emits the canonical `result` event as its last stdout line.
- The PTY drive itself (`internal/agentrun/Drive`) and its timing knobs (`PYRY_AGENT_RUN_TRUST_DELAY`, `PYRY_AGENT_RUN_PROMPT_DELAY`).
- The `settings-file: <path>` marker line on stdout (`cmd/pyry/agent_run.go:223`) — its dispatcher-side metadata role is taken over by claude's own `system init` event on stdout.

All of these packages stay in tree per the ticket body — deletion is a follow-up cleanup. This ticket only stops importing them from `cmd/pyry/agent_run.go` and rewrites the verb's tests accordingly.

Stdout contract after this change: **claude's stdout, byte-for-byte, forwarded directly** by `streamrunner.Run`. No verb-prepended marker line, no verb-composed trailer. The dispatcher's parser already consumes `claude -p --output-format stream-json` shape (out-of-scope dispatcher work pre-existed; see #354's "the dispatcher's stream-json parser keeps working unchanged" framing). The dispatcher submodule pointer bump that re-enables this end-to-end is explicit out-of-scope here.

## Design

### What `runAgentRun` does after this ticket

1. `--self-check` short-circuit (unchanged) — `slices.Contains(args, "--self-check")` → `runAgentRunSelfCheck(stdout)`.
2. `parseAgentRunArgs(args)` (unchanged).
3. Resolve claude binary: `os.Getenv("PYRY_CLAUDE_BIN")`, default `"claude"` (unchanged pattern; the env knob stays so tests can inject a fake).
4. Read prompt file: `os.ReadFile(parsed.promptFile)` (unchanged).
5. Build argv: `buildClaudeArgs(parsed)` (signature changes — see below).
6. Set up signal context: `ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)` then `defer cancel()` (unchanged).
7. Call `streamrunner.Run(ctx, streamrunner.Config{...})`. Return its error verbatim, wrapped with `fmt.Errorf("agent-run: %w", err)`. If the error is `context.Canceled` (operator teardown), return nil instead.

That's the entire body. ~25 LOC + flag-list + comments. The errgroup, watcher, emitter, settings, trust, session-id, classify-helper, env-knob parser — all gone.

#### Removed identifiers in `cmd/pyry/agent_run.go`

- `newSessionUUID` (and its imports: `crypto/rand`).
- `parseDurationEnv` (and its callers — the two `PYRY_AGENT_RUN_*_DELAY` env vars were drive-only).
- `classifyForEmitter` (no emitter to classify for).
- `time` import (no duration env, no signal timing).
- Imports: `golang.org/x/sync/errgroup`, `internal/agentrun/jsonl`, `internal/agentrun/jsonl/tail`, `internal/agentrun/streamjson`.

The `internal/agentrun` import stays for `agentrun.MarkWorkdirTrusted` / `agentrun.WriteSettings` calls in `agent_run_selfcheck.go` (other file in `package main`); leave it.

Add: `internal/agentrun/streamrunner` import.

### `buildClaudeArgs` — new shape

Signature changes from `buildClaudeArgs(parsed agentRunArgs, settingsPath, sessionID string) []string` to:

```go
func buildClaudeArgs(parsed agentRunArgs) []string
```

Returned argv (one element per token, in this order):

```
--input-format               stream-json
--output-format              stream-json
--verbose
--dangerously-skip-permissions
--append-system-prompt-file  <parsed.systemPromptFile>
--model                      <parsed.model>
--effort                     <parsed.effort>
--max-turns                  <strconv.Itoa(parsed.maxTurns)>
--allowed-tools              <strings.Join(parsed.allowedTools, ",")>
```

Notes on individual flags:

- `--input-format stream-json` — required so claude reads our envelope on stdin.
- `--output-format stream-json --verbose` — required pair; the spike validated that `--verbose` is what causes assistant message events to land on stdout under stream-json (without it, only `result` is emitted).
- `--dangerously-skip-permissions` — replaces the workspace-trust mark + settings-file deny-default. Acceptable in this verb because the dispatcher is the sole caller and operates inside an isolated worktree; the entire scope of the spawn is "run this turn against this prompt and exit." The `--allowed-tools` argv is then the authoritative tool gate (claude honours it under `-p`-style stream-json mode where the settings file would have been); this is the inversion from the old build's `--allowedTools-MUST-NOT-appear` security invariant.
- `--append-system-prompt-file` — unchanged, file path passed verbatim. `parseAgentRunArgs` already validated the file exists and is regular.
- `--model`, `--effort` — unchanged.
- `--max-turns <n>` — added (interactive mode ignored it; stream-json mode honours it). Bug class this prevents: a runaway agent burning turns past the dispatcher's budget intent.
- `--allowed-tools` — comma-joined. The ticket body's "`--allowed-tools <list>`" is canonical claude form. `splitAllowedTools` already accepts comma-or-whitespace-separated input from the operator and stores a clean `[]string`; we re-join with commas here.
- No `--settings`, `--permission-mode`, `--session-id`, or `--permission-prompt-tool`. Each was a load-bearing element of the PTY/settings world; in stream-json mode they are absent.

### `streamrunner.Run` call site

```go
err := streamrunner.Run(ctx, streamrunner.Config{
    ClaudeBin:   claudeBin,
    WorkDir:     parsed.workdir,
    Args:        buildClaudeArgs(parsed),
    PromptBytes: promptBytes,
    Stdout:      stdout,        // pyry stdout — claude's stream-json emerges here verbatim
    Stderr:      os.Stderr,     // pyry stderr — claude diagnostics passthrough
    // Env, Logger left zero — production defaults.
})
```

Stderr forwarding is explicit: the old verb didn't forward claude's stderr (PTY drive merged it into the PTY); the new path makes a deliberate decision to send it to `os.Stderr` so the dispatcher's stderr capture (already wired by it for any subprocess) sees it. This is not new behaviour from the dispatcher's perspective — most pyry verbs already write their own stderr there.

### Exit-code behaviour (AC#4 trace)

- Clean run, claude exits 0 → `streamrunner.Run` returns nil → `runAgentRun` returns nil → `main()` exits 0. ✓
- claude exits non-zero → `streamrunner.Run` returns `*exec.ExitError` → wrapped and returned → `main()` prints `"pyry: agent-run: ..."` and exits 1. The exact non-zero code claude produced is preserved in the wrapped error chain (extractable via `errors.As(&exitErr); exitErr.ExitCode()`) but the verb does not currently propagate it as the process exit code, and the AC's "no changes required on the dispatcher's salvage path" confirms the dispatcher tolerates the existing "exit 1 on any failure" behaviour. **Do not introduce per-claude-exit-code propagation in this ticket** — it is a behaviour change the dispatcher hasn't asked for.
- Operator SIGTERM/SIGINT mid-run → `signal.NotifyContext` cancels ctx → `streamrunner.Run` SIGTERMs the child, waits up to its 5s grace, returns nil → `runAgentRun` returns nil → `main()` exits 0. ✓ (This matches the old behaviour, which also returned nil on `context.Canceled`.)

The only new surface is "claude exited non-zero on its own" producing an `*exec.ExitError`. Mapping: any non-nil `streamrunner.Run` return that is not `context.Canceled` is wrapped as `fmt.Errorf("agent-run: %w", err)`. Tests pin both shapes.

## Concurrency model

A single goroutine: `main`'s. `streamrunner.Run` blocks until the child exits. Stdlib internally manages stdout/stderr forwarder goroutines but the verb owns nothing here — no errgroup, no watcher, no emitter goroutines. Shutdown is entirely stdlib-driven via `cmd.Cancel` + `WaitDelay`.

Compared to the old design's three-actor errgroup (drive + watcher + signal handler racing each other), this is a strict simplification.

## Error handling

Mapping table (verb-side, after `streamrunner.Run` returns):

| `streamrunner.Run` return | Verb action |
|---|---|
| `nil` | return nil. |
| `context.Canceled` (or `errors.Is(err, context.Canceled)`) | return nil — operator teardown is success. |
| `*exec.ExitError` | return `fmt.Errorf("agent-run: %w", err)`. |
| anything else (pre-Start setup failure: stdin pipe, spawn) | return `fmt.Errorf("agent-run: %w", err)`. |

Single helper not warranted — five lines inline. The pattern mirrors `streamrunner.Run`'s own internal `if ctx.Err() != nil { return nil }` tail.

Error messages must NOT include `promptBytes`, `parsed.systemPromptFile` content, or any of `parsed.allowedTools`. The wrapped chain only includes the underlying error from `streamrunner` (which itself logs/returns only the spawn step's name, never prompt content) — there is no place in the verb that would interpolate prompt bytes into an error string. Pinned by absence; no new test specifically guards this since the verb doesn't see prompt bytes after `os.ReadFile`.

## Testing strategy

All in `cmd/pyry/agent_run_test.go`. Stdlib `testing` only.

### Tests to delete

- `TestRunAgentRun_EmitsSettingsFile` — settings file is no longer written.
- `TestRunAgentRun_MarksWorkdirTrusted` — trust mark is no longer written; `--dangerously-skip-permissions` covers it.
- `TestNewSessionUUID_Shape` — `newSessionUUID` is removed.
- `writeFakeClaudeJSONL` helper — fake no longer writes JSONL (claude in real mode does; fake doesn't need to).

### Tests to keep byte-identical

- `TestParseAgentRunArgs_HappyPath`, `TestParseAgentRunArgs_Errors`, `TestParseAgentRunArgs_EffortValidValues`, `TestParseAgentRunArgs_AllowedToolsForms`.
- `TestSplitAllowedTools`.

`parseAgentRunArgs` is unchanged. The `--output-format` flag is still required and still pinned to `"stream-json"`.

### Tests to rewrite

#### `TestBuildClaudeArgs_Shape` — new argv shape

Two table rows (canonical + alternate effort) asserting:

- `slices.Equal(buildClaudeArgs(parsed), want)` for an explicit `want` slice matching the new argv order: `["--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--append-system-prompt-file", <sys>, "--model", <m>, "--effort", <e>, "--max-turns", <n-as-string>, "--allowed-tools", <comma-joined>]`.

Plus named structural assertions (so a re-ordering that preserves `slices.Equal` against a stale `want` still trips a clear-named guard):

- `slices.Contains(got, "--dangerously-skip-permissions")` is true.
- `slices.Contains(got, "--input-format")` and `--output-format` are both present, each followed by `"stream-json"`.
- `--allowed-tools` is present and the next element is `strings.Join(parsed.allowedTools, ",")` (round-trip from the parsed slice).
- `--max-turns` is present and the next element is `strconv.Itoa(parsed.maxTurns)`.
- `--settings` is absent. `--permission-mode` is absent. `--session-id` is absent. (Pin the negation: these are the load-bearing PTY-mode flags that should never reappear.)

#### `TestAgentRunFakeClaude` — new fake

Replace the JSONL-writing fake with a stream-json fake. Behaviour gated on `PYRY_AGENT_RUN_FAKE=1`:

- Drain stdin into a `bytes.Buffer` until EOF (the verb closes stdin after one envelope; reading to EOF avoids deadlock).
- Optionally capture the drained envelope to `GO_AGENT_RUN_FAKE_STDIN_FILE` if set (test can assert the JSON shape end-to-end).
- Optionally capture argv to `GO_AGENT_RUN_FAKE_ARGS_FILE` if set (kept from the existing fake).
- Switch on `GO_AGENT_RUN_FAKE_MODE` (default `clean`):
  - `clean` — write three lines to stdout: `{"type":"system","subtype":"init","session_id":"00000000-0000-4000-8000-000000000000"}`, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`, `{"type":"result","subtype":"success"}`. Exit 0.
  - `exit1` — exit 1 immediately (after stdin drain) without writing to stdout.
  - `sleep` — install a SIGTERM handler that prints `"got SIGTERM"` to stderr and exits 0 within ~50ms; otherwise sleep 30s. Used by the ctx-cancel test.

Use a fresh test name (e.g. `TestAgentRunStreamJSONFake`) so the renamed scope is obvious in `go test -v` output and so `-test.run=^…$` patterns in the wrapper script can pin it.

#### `configureFakeClaude` — simplified

The shell wrapper still exists (test binary's flag parser still rejects unknown claude flags), but it no longer needs to extract `--session-id`. New body:

```sh
#!/bin/sh
exec <test-binary-path> -test.run=^TestAgentRunStreamJSONFake$
```

Drop: `PYRY_AGENT_RUN_TRUST_DELAY` / `PYRY_AGENT_RUN_PROMPT_DELAY` env exports (the underlying knobs are gone). Keep: `PYRY_CLAUDE_BIN` (script path) and `PYRY_AGENT_RUN_FAKE=1`.

#### `TestRunAgentRun_DrivesFakeClaude` — rewrite

Renamed to `TestRunAgentRun_StreamJSON_Clean` (or keep the existing name; up to the developer — the rename is documentation). Asserts:

- `runAgentRun(&stdout, fx.argv)` returns nil within 10s.
- `stdout` lines (split on `\n`, trimmed of trailing empty) contain, in order:
  1. A line with `"type":"system"` and `"subtype":"init"`.
  2. A line with `"type":"assistant"`.
  3. A line with `"type":"result"` and `"subtype":"success"`.
- `stdout` does NOT start with `settings-file: ` (negative assertion: confirms the marker line is gone).
- The fake's captured stdin envelope (via `GO_AGENT_RUN_FAKE_STDIN_FILE`) parses as `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}` — i.e. the prompt file's `"hello"` content was JSON-wrapped and delivered. (Round-trip validation that the verb is using `streamrunner.Run`, not some other path that bypasses the envelope.)
- No `~/.claude/projects/` or `.pyry-agent-run-settings.json` paths are read, written, or stat'd by the verb. (Negative assertion: list `t.TempDir()` and the fake `HOME` after the run; assert the trust file and settings file are absent. Optional but cheap.)

#### Add: `TestRunAgentRun_StreamJSON_NonZeroExit`

Configure fake with `GO_AGENT_RUN_FAKE_MODE=exit1`. Assert `runAgentRun` returns a non-nil error wrapping `*exec.ExitError` with `ExitCode() == 1`. Assertion shape:

- `err != nil`.
- `errors.As(err, &exitErr)` is true.
- `exitErr.ExitCode() == 1`.
- The error message starts with `"agent-run: "` (preserves the verb's prefix convention).

#### Add: `TestRunAgentRun_StreamJSON_CtxCancel`

Configure fake with `GO_AGENT_RUN_FAKE_MODE=sleep`. Spawn `runAgentRun` in a goroutine. After ~100ms, send SIGTERM to the test process via `syscall.Kill(os.Getpid(), syscall.SIGTERM)` — this is the same signal-context surface the production verb registers for via `signal.NotifyContext`. **Caveat:** sending SIGTERM to the test process risks killing the test runner if `signal.NotifyContext`'s registration races with the kill.

Safer alternative (preferred): factor the signal-context creation into a small private hook that the test can override, OR make the test directly exercise the `streamrunner.Run` ctx-cancel path with a cancellable parent context. Concretely: extract the `ctx, cancel := signal.NotifyContext(...)` setup into a package-private variable like `var newSignalContext = signal.NotifyContext` so the test can swap in `context.WithCancel` and call `cancel()` directly. The verb body becomes `ctx, cancel := newSignalContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)`.

Assert:

- `runAgentRun` returns nil within ~6s (SIGTERM grace is 5s; fake exits in ~50ms on SIGTERM).
- The fake's stderr (capture by routing `cmd.Stderr` through a `bytes.Buffer` if practical, or skip this sub-assertion — the streamrunner-package tests already cover SIGTERM delivery; the verb-level test only needs to verify the verb's nil return).

If the signal-context-hook extraction feels heavier than the bug it prevents (one extra package-private var), the developer may instead skip `TestRunAgentRun_StreamJSON_CtxCancel` from the verb's test file and rely on `streamrunner`'s own `TestRun_CtxCancelMidRun` to cover the SIGTERM-grace path. The verb's own ctx-cancel logic is one line (`return nil` if `errors.Is(err, context.Canceled)`); a unit test covering just that line via a stub of `streamrunner.Run` would be over-mocking.

Recommended: **skip** the verb-level ctx-cancel test. The streamrunner package owns the SIGTERM behaviour; the verb is one line of error-mapping. Cite this in the test file as a comment so the omission is intentional, not forgotten.

### JSONL-file smoke check (AC#5 final clause)

The AC asks the verb's tests to verify that `~/.claude/projects/<encoded-cwd>/<session-id>.jsonl` is still produced by claude itself under stream-json mode. The fake claude in this verb's test file does NOT produce that file (and shouldn't — the verb doesn't depend on it any more, and faking it would re-introduce `agentrun.EncodeProjectDir` / session-id coupling we just deleted).

Document the assumption in a comment at the top of the new fake:

```go
// Note: production claude under --output-format stream-json still writes a
// JSONL session file at ~/.claude/projects/<encoded-cwd>/<session-id>.jsonl.
// This fake does NOT produce that file — the verb no longer reads it (the
// JSONL-tail watcher was deleted in #391). Verification of claude's own
// JSONL emission is left to manual smoke against a real `claude` binary;
// see #375 for the self-check refactor that owns that surface.
```

This is the documented-assumption form the AC's parenthetical permits.

### `agent_run_selfcheck_test.go`

Out of scope — `agent_run_selfcheck.go` is unchanged. Its tests stay byte-identical.

### Race and shutdown

`go test -race ./cmd/pyry/...` must pass clean. The verb has no shared state of its own; the new path's only goroutines are stdlib's stdin/stdout/stderr forwarders inside `streamrunner.Run`, which `streamrunner_test`'s race coverage already exercises.

## Open questions

- **Should `--allowed-tools` use comma joining or be repeated as `--allowed-tools tool1 --allowed-tools tool2`?** Spec assumes comma-joining (single flag occurrence) because that's the canonical claude CLI form for this flag. If the spike or claude release notes disagree, the developer should swap to repeated occurrences and update the test's `want` slice; no other call site cares.
- **Should the verb echo a "running" log line at start?** No — `streamrunner` deliberately logs nothing on start; the verb mirrors that. The dispatcher already has full visibility via the stream-json events claude emits.
- **Should we propagate claude's exact exit code as the pyry process exit code?** No, see "Exit-code behaviour" above. AC#4 says "claude's exit code on non-zero exit" but the AC also says "no changes required on the dispatcher's salvage path"; the dispatcher's existing tolerance for "exit 1 on failure" is the binding constraint. File a follow-up if the dispatcher later needs the exact code.

## Acceptance trace

| AC | Where satisfied |
|---|---|
| AC#1 (spawns via `streamrunner.Run`; PTY-bridge no longer invoked) | "What `runAgentRun` does" step 7; "Removed identifiers" list. |
| AC#2 (argv shape: `--input-format`, `--output-format`, `--verbose`, `--dangerously-skip-permissions`, `--append-system-prompt-file`, `--model`, `--effort`, `--max-turns`, `--allowed-tools`; no settings-file flag) | `buildClaudeArgs` new shape; `TestBuildClaudeArgs_Shape` rewrite. |
| AC#3 (no per-spawn settings file; `settings-file:` marker line removed) | "Removed identifiers" + step 1 of `runAgentRun` body (no `WriteSettings` call); `TestRunAgentRun_StreamJSON_Clean` negative assertion that stdout does not begin with `settings-file: `. |
| AC#4 (exit codes: 0 clean / claude's non-zero / signal-mapped on operator teardown; no dispatcher salvage changes) | "Exit-code behaviour" section + new `TestRunAgentRun_StreamJSON_NonZeroExit`. |
| AC#5 (tests updated: fake reads stream-json envelope on stdin, emits canned stream-json on stdout incl. `system init` + `result`; PTY scaffolding migrated; JSONL emission documented assumption) | Whole "Testing strategy" section. |

## Security review

**Verdict:** PASS

This ticket *is* a security fix (it eliminates the PTY blind-typing prompt-injection class). Adversarial walk below confirms no new exploitable surface is introduced and several pre-existing surfaces are removed.

**Findings:**

- **[Trust boundaries]** No findings. Three boundaries:
  - **Operator → claude (stdin):** the prompt file's bytes are JSON-encoded into a `userTurn` envelope by `streamrunner.marshalEnvelope` (`internal/agentrun/streamrunner/runner.go:179-195`) and round-tripped through `cmd.StdinPipe` — no shell interpretation, no PTY input-buffer mixing. This is precisely the surface the PTY drive failed to gate, and the fix sits at the streamrunner layer (already pinned by `TestRun_StdinEnvelopeRoundTrip`).
  - **Operator → claude (argv):** `parseAgentRunArgs` validates each flag's shape (`--effort` against an enum, `--workdir` exists as dir, `--max-turns > 0`, `--output-format == "stream-json"`); operator-controlled string values (`--model`, `--allowed-tools` tokens, file paths) are passed as discrete argv elements via `exec.Command` — never via a shell. No injection vector.
  - **claude → dispatcher (stdout):** the verb forwards verbatim and parses nothing. The dispatcher owns the consumer-side trust decision; that's its existing contract for `claude -p --output-format stream-json` and is unchanged here.

- **[Tokens, secrets, credentials]** No findings. This ticket *removes* the `crypto/rand`-based `newSessionUUID` mint (claude now mints its own session id, reported via `system init` event). Net reduction in credential-handling surface.

- **[File operations]** No findings introduced; one pre-existing risk noted, two surfaces removed.
  - **Removed:** the per-spawn settings file write (`agentrun.WriteSettings` → `<workdir>/.pyry-agent-run-settings.json`).
  - **Removed:** the trust-mark write into `~/.claude.json` (`agentrun.MarkWorkdirTrusted`).
  - **Pre-existing TOCTOU note:** `parseAgentRunArgs` Stats `--prompt-file` / `--system-prompt-file` / `--workdir` at parse time; `os.ReadFile(promptFile)` runs later. A swap-during-the-gap attack is theoretically possible but the operator (dispatcher) controls these paths in its own worktree — same threat model as every other pyry verb. Not a finding for *this* ticket.

- **[Subprocess / external command execution]** SHOULD FIX (documentation, not code change). The most adversarially-loaded change is `--dangerously-skip-permissions`. Audit:
  - **Argv assembly:** `exec.CommandContext(claudeBin, args...)` — slice form, no `shell: true`, no string interpolation. Operator values cannot escape into shell metacharacters.
  - **Why `--dangerously-skip-permissions` is acceptable here:** under `-p`/stream-json mode, `--allowed-tools` is the authoritative gate. Skipping the permissions dialog removes the workspace-trust prompt that the old design defeated by *writing* to `~/.claude.json`; net effect on tool authority is identical (both designs disable the prompt; the new one does it with a flag instead of a filesystem write). The dispatcher already operates inside an isolated worktree with a curated `--allowed-tools` list; the spawn's blast radius is bounded by that list, not by the trust dialog.
  - **Pre-existing env-inheritance:** `streamrunner.Run` is called with `Env: nil`, so the child claude inherits the parent's full environment (potentially including API keys, dispatcher tokens). This matches the old PTY drive's behaviour and matches every other subprocess in pyry; no regression. If the dispatcher later wants env scrubbing, that's a streamrunner-layer change, not a verb-layer one — file as follow-up.
  - **Signal handling:** `signal.NotifyContext(ctx, SIGTERM, SIGINT)` → ctx cancel → `streamrunner` SIGTERMs the child → 5s grace → SIGKILL via `cmd.WaitDelay`. No double-fork escape surface (claude is a single-process child).
  - **SHOULD FIX:** the "why this flag is safe here" reasoning above belongs in `buildClaudeArgs`'s doc-comment so a future reader doesn't have to grep this spec. Developer adds a short comment block on the flag list; code-review checks the wording.

- **[Cryptographic primitives]** N/A — no crypto added; one `crypto/rand` use removed.

- **[Network & I/O]** No new findings; one pre-existing limit noted.
  - **No size cap on `--prompt-file`:** `os.ReadFile(parsed.promptFile)` reads the entire file into memory. A multi-GB prompt file would OOM the verb. Pre-existing behaviour; the operator (dispatcher) controls the file. Not a finding for *this* ticket.
  - **stdout/stderr backpressure:** `streamrunner.Run` lets the stdlib's stdout/stderr forwarders block on the consumer (the dispatcher reading the verb's stdout). If the dispatcher stalls, `cmd.Wait()` blocks. Same posture as every Go subprocess; documented in `streamrunner`'s `Stdout`/`Stderr` doc-comments.

- **[Error messages, logs, telemetry]** No findings.
  - The verb's error chain wraps `streamrunner.Run`'s error, which contains only stage names (`"streamrunner: stdin pipe: ..."`, `"streamrunner: start: ..."`) — never prompt bytes nor argv values.
  - The verb does not interpolate `promptBytes`, `parsed.systemPromptFile` content, or `parsed.allowedTools` into any error string. Pinned by absence (no `fmt.Errorf` in the new path references those fields).
  - **claude's stderr** is forwarded to `os.Stderr`. If claude itself logs sensitive content there in some debug mode, that's a claude-layer concern, not a verb-layer concern; the dispatcher consumes stderr separately and applies its own redaction policy.

- **[Concurrency]** No findings. The verb's goroutine surface *shrinks* — the old design's `errgroup` (drive + watcher) is replaced by a single blocking `streamrunner.Run` call. No locks, no shared state. Shutdown is fully stdlib-driven (`cmd.Cancel` + `WaitDelay`).

- **[Threat model alignment]** No findings. Primary in-scope threat (PTY blind-typing prompt poisoning, the `/doctor` regression) is structurally eliminated by switching to JSON-on-stdin. Named out-of-scope threats from the ticket body remain explicitly out of scope: `--permission-prompt-tool stdio` (mobile case), deletion of dead PTY/trust/settings code (cleanup ticket), dispatcher submodule pointer bump.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-05-15
