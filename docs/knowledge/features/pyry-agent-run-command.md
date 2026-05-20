# `pyry agent-run` — supervised headless claude turn

The CLI verb that replaces `claude -p` in the dispatcher. Phase A spike (#329) greenlit this verb as the dispatcher's headless entry point; behaviour landed in slices: #337 scaffold, #339 settings file, #342 trust-state pre-population, #332 spawn + PTY-drive, #348/#349 JSONL line reader + tail watcher, #354 stream-json stdout emitter + result trailer, #391 cutover from PTY drive to the [`streamrunner`](streamrunner-package.md) stream-json subprocess pipeline, **#470 cutover back to PTY drive via [`ptyrunner`](ptyrunner-package.md) with `PYRY_USE_STREAMJSON=1` retained as the operator-facing rollback knob**. Anthropic's 2026-06-15 billing policy lists "Interactive Claude Code in the terminal or IDE" as subscription-eligible but does not name the stream-json subprocess surface; the PTY pivot lands the verb on the explicitly-named surface ahead of the deadline. Operator decision 2026-05-19: streamrunner stays as a sibling indefinitely so empirical billing-classification comparison is possible post-deadline.

## What it does today (post-#470)

1. Recognises `--self-check` positionally and short-circuits to `runAgentRunSelfCheck(stdout)` (#336) before any flag parsing — the eight required production flags do not apply to the diagnostic verb. (Adaptation of `--self-check` to exercise the ptyrunner default path is tracked under [#473](https://github.com/pyrycode/pyrycode/issues/473).)
2. Parses and validates the full flag set.
3. Reads `--prompt-file` into memory.
4. Resolves the claude binary path (`PYRY_CLAUDE_BIN` env override → default `"claude"`).
5. Installs `signal.NotifyContext` for `SIGTERM` / `SIGINT`.
6. Branches on `os.Getenv("PYRY_USE_STREAMJSON") == "1"`:
   - **`"1"` → `runAgentRunStreamRunner` (legacy stream-json subprocess path).** Calls `streamrunner.Run` with the assembled argv from `buildStreamRunnerClaudeArgs` and the prompt bytes. Byte-equivalent to the pre-cutover (#391–#469) behaviour. Preserved indefinitely for billing-classification comparison.
   - **Unset / anything else → `runAgentRunPty` (default).** Pre-marks workdir trust via [`trust.MarkWorkdirTrusted`](agentrun-trust-subpackage.md), receives back the symlink-resolved realpath; writes a per-spawn deny-default permissions JSON via [`settings.WriteSettings(parsed.allowedTools)`](agentrun-settings-subpackage.md), registers `defer os.Remove(settingsPath)`; mints a fresh session UUID via `sessions.NewID`; delegates to [`ptyrunner.Run`](ptyrunner-package.md) with the populated `ptyrunner.Config` (realpath as `WorkDir`, minted UUID as `SessionID`, settings tempfile path as `SettingsPath`, plus parsed `Model` / `Effort` / `MaxTurns` / `SystemPrompt` / `PromptBytes`). ptyrunner spawns claude as an interactive-TUI process under a PTY, delivers the prompt via bracketed-paste, tails claude's session JSONL, re-emits each event as stream-json on stdout, enforces `MaxTurns` via the budget Counter, and runs the PTY-heartbeat + spinner-freeze watchdog.
7. Maps the helper's return: nil → exit 0; `context.Canceled` (operator teardown) → exit 0; any other error → wrapped as `agent-run: %w` and exit 1. Error prefixes from the ptyrunner-path helpers (`mark workdir trusted in ~/.claude.json: …`, `write per-spawn settings: …`, `mint session id: …`) compose into the final `agent-run: <step>: <underlying>` shape — AC #3's operator-readable surface.

The `PYRY_USE_STREAMJSON` predicate is intentionally strict: only the exact string `"1"` selects the streamrunner branch. `"true"`, `"yes"`, `"on"`, `"streamjson"`, `"0"`, `"false"`, and the empty string all fall through to the ptyrunner default. Pinned table-driven by `TestRunAgentRun_EnvNon1ValueDispatchesToPtyRunner` so a future contributor cannot quietly widen the truthy set.

```
$ pyry agent-run --prompt-file p.txt --system-prompt-file s.txt \
    --allowed-tools "Read,Bash" --max-turns 3 --effort medium \
    --model sonnet-4-6 --workdir ./repo --output-format stream-json
{"type":"system","subtype":"init","cwd":"/abs/path/to/repo","session_id":"…",…}
{"type":"user","message":{…}}
{"type":"assistant","message":{…,"stop_reason":"end_turn",…}}
{"type":"result","subtype":"success","is_error":false,"duration_ms":…,…}
```

Stdout contract: **line-delimited stream-json events**. Under the streamrunner path, claude's own stdout is forwarded byte-for-byte (claude emits the canonical `system init` and `result` events itself under stream-json mode). Under the ptyrunner default, `streamjson.Emitter` re-emits claude's per-session JSONL events into the same wire shape and composes the `type:"result"` trailer locally (claude does not emit one in interactive mode). The dispatcher's parser is satisfied by either path.

## History — PTY → stream-json → PTY pivot

The verb's drive surface has cycled twice:

- **Pre-#391 (PTY drive, original).** Spawned claude in a PTY and *blindly typed* the prompt bytes after a fixed delay. On 2026-05-14, an invalid `defaultMode: "deny"` value in the per-spawn settings file caused claude's interactive startup to queue a `/doctor` template; the drive's prompt write was concatenated with that template and **every dispatched agent silently reasoned about "how to fix the settings file" instead of the actual prompt**. The stream-json subprocess pipeline structurally eliminates the prompt-mixing class.
- **#391 → #469 (stream-json subprocess).** `claude -p --output-format stream-json` invoked via [`streamrunner`](streamrunner-package.md); prompt delivered as one JSON envelope on stdin; `--allowed-tools` and `--dangerously-skip-permissions` removed the per-spawn settings file and workspace-trust mark; claude mints its own session id and emits its own `system init` / `result` events on stdout.
- **#470 (back to PTY drive, this time via [`ptyrunner`](ptyrunner-package.md)).** Anthropic's 2026-06-15 billing policy names "Interactive Claude Code in the terminal or IDE" as subscription-eligible but does not name the stream-json subprocess surface. The PTY pivot lands the verb on the explicitly-named surface ahead of the deadline. The 2026-05-14 prompt-mixing class is structurally avoided by `ptyrunner.WritePrompt`'s bracketed-paste sequence (no fixed-delay blind type) and the `trust.MarkWorkdirTrusted` pre-write (no settings-file modal → no `/doctor` template).

Mechanisms by mode:

| Mechanism | streamrunner (`PYRY_USE_STREAMJSON=1`) | ptyrunner (default) |
|---|---|---|
| Tool gate | `--allowed-tools` on argv | Per-spawn deny-default `settings.json` written to `os.TempDir()`, claude flag `--settings <path>` |
| Workspace trust | `--dangerously-skip-permissions` | `trust.MarkWorkdirTrusted` pre-writes `~/.claude.json:projects[<realpath>].hasTrustDialogAccepted=true` |
| Session id | Claude mints under `-p` mode | Pyry mints via `sessions.NewID`, passes via `--session-id` |
| Stream-json source | Claude's stdout under `--output-format stream-json` | Pyry tails claude's per-session JSONL at `~/.claude/projects/<realpath>/<sid>.jsonl` and re-emits via `streamjson.Emitter` |
| `--max-turns` enforcement | Claude (`-p` honours it) | Pyry-side `budget.Counter` (interactive claude does not honour `--max-turns`) |
| `result` trailer | Claude emits | `streamjson.Emitter` composes |
| Watchdog | None (subprocess exit is the signal) | `tui-driver` two-arm Tracker (PTY-heartbeat + spinner-freeze) |

Wire shape on stdout is identical across both modes — the dispatcher's stream-json parser is satisfied by either.

## Flags

All eight are required; each is validated at parse time with a one-line error that names the offending flag.

| Flag | Validation |
|------|-----------|
| `--prompt-file <path>` | Must exist and be a regular file. |
| `--system-prompt-file <path>` | Must exist and be a regular file. |
| `--allowed-tools "<list>"` | Accepts comma- or whitespace-separated tokens (or any mix); trims each; rejects an empty result. |
| `--max-turns <int>` | Must be > 0. |
| `--effort <enum>` | One of `low`, `medium`, `high`, `xhigh`, `max`. |
| `--model <string>` | Non-empty after trim. |
| `--workdir <dir>` | Must exist and be a directory. |
| `--output-format <stream-json>` | Literal `stream-json` only — any other value rejected. |

Errors render via `main()`'s standard wrapper as `pyry: agent-run: --<flag>: <reason>` and exit non-zero. Trailing positionals are rejected with `agent-run: unexpected positional %q`. `--help` falls through `flag.ContinueOnError` to the registered `fs.Usage`.

## Implementation

- `cmd/pyry/agent_run.go` — `agentRunArgs` unexported struct (stable field names: `promptFile`, `systemPromptFile`, `allowedTools []string`, `maxTurns`, `effort`, `model`, `workdir`, `outputFormat`), `parseAgentRunArgs(args) (agentRunArgs, error)`, `splitAllowedTools(raw) []string` pure tokeniser (`strings.FieldsFunc` over `r == ',' || unicode.IsSpace(r)` plus trim + empty drop), `validEfforts` package-level set, `requireRegularFile` / `requireDir` helpers that surface `os.Stat` errors verbatim.
- `runAgentRun(stdout io.Writer, args []string)` post-#470 body: `--self-check` short-circuit → parse → `os.ReadFile(promptFile)` → resolve `PYRY_CLAUDE_BIN` → `signal.NotifyContext(SIGTERM, SIGINT)` → branch on `os.Getenv("PYRY_USE_STREAMJSON") == "1"`: `runAgentRunStreamRunner` (legacy) vs `runAgentRunPty` (default) → return nil on nil or `context.Canceled`, else wrap `agent-run: %w`. Both helpers return wrapped-but-not-prefixed chains so the `agent-run:` prefix is added in exactly one place.
- `runAgentRunStreamRunner(ctx, stdout, parsed, claudeBin, promptBytes)` — extracted from the pre-cutover body; byte-equivalent to the #391–#469 behaviour. Calls `streamrunner.Run` with `Config{ClaudeBin, WorkDir: parsed.workdir, Args: buildStreamRunnerClaudeArgs(parsed), PromptBytes, Stdout, Stderr: os.Stderr}`.
- `runAgentRunPty(ctx, stdout, parsed, claudeBin, promptBytes)` — flat sequence of three I/O calls plus one delegated `ptyRun`. Order: `trustMark(parsed.workdir)` → wrap as `"mark workdir trusted in ~/.claude.json: %w"` on error; `settingsWrite(parsed.allowedTools)` → wrap as `"write per-spawn settings: %w"` on error, then `defer func() { _ = os.Remove(settingsPath) }()`; `newSessionID()` → wrap as `"mint session id: %w"` on error; `ptyRun(ctx, ptyrunner.Config{ClaudeBin, WorkDir: realpath, SessionID: string(sid), SettingsPath, SystemPrompt, Model, Effort, MaxTurns, PromptBytes, Stdout, Stderr: os.Stderr})`. **`ptyrunner.Config.WorkDir` is `trust.MarkWorkdirTrusted`'s symlink-resolved realpath, NOT `parsed.workdir`** — claude resolves the workdir before keying `projects[<realpath>]` in `~/.claude.json`, so the realpath-from-trust contract keeps pyry's trust-mark key aligned with claude's lookup key. Defer-LIFO fires on every exit path (sessionID error, ptyrun error, ctx-cancel, success) — settings tempfile cleanup is structural.
- Four package-level **test-only seams** at the top of the file (`var trustMark = trust.MarkWorkdirTrusted` / `settingsWrite = settings.WriteSettings` / `ptyRun = ptyrunner.Run` / `newSessionID = sessions.NewID`). Production never assigns to these; `_test.go` files override via `t.Cleanup` restore-on-exit boilerplate. Documented in a block comment so a future contributor adding a fifth seam pauses to question the decomposition.
- `buildStreamRunnerClaudeArgs(parsed) []string` (renamed from `buildClaudeArgs` in #470) — pure helper, emits exactly:

  ```
  --input-format stream-json
  --output-format stream-json
  --verbose
  --dangerously-skip-permissions
  --append-system-prompt-file <parsed.systemPromptFile>
  --model <parsed.model>
  --effort <parsed.effort>
  --max-turns <strconv.Itoa(parsed.maxTurns)>
  --allowed-tools <strings.Join(parsed.allowedTools, ",")>
  ```

  **Security invariants** pinned by `TestBuildStreamRunnerClaudeArgs_Shape`:

  - `--input-format stream-json` / `--output-format stream-json` / `--verbose` MUST all appear (verbose is required to get assistant message events on stdout under stream-json — without it, only `result` is emitted; spike-verified 2026-05-14).
  - `--dangerously-skip-permissions` MUST appear. Acceptable here because the dispatcher is the sole caller and operates inside an isolated worktree; `--allowed-tools` is the authoritative tool gate under `-p`-style stream-json mode; the spawn's blast radius is bounded by that list, not by the trust dialog.
  - `--settings`, `--permission-mode`, `--session-id` MUST NOT appear on the streamrunner branch (negative pins — these are load-bearing on the ptyrunner default path, where `ptyrunner.buildArgs` emits them, but they must never appear on the legacy `-p`-style argv).

  ptyrunner's argv shape (`--session-id` / `--settings` / `--permission-mode default` / `--append-system-prompt-file` / `--model` / `--effort`; no `--allowed-tools`, no `--dangerously-skip-permissions`, no `--max-turns`) is owned by `internal/agentrun/ptyrunner/runner.go`'s private `buildArgs` and is intentionally NOT exposed at the cmd layer.

- `agentRunUsageDescription` constant (the `--help` body) rewritten in #470 to describe the PTY default and the `PYRY_USE_STREAMJSON=1` rollback knob. Required substrings pinned by `TestAgentRunUsageDescription`: `stream-json`, `--max-turns`, `--allowed-tools`, `PYRY_USE_STREAMJSON`, `PTY`.
- `parseDurationEnv(name)` remains only because `agent_run_selfcheck.go` (#336) still reads `PYRY_AGENT_RUN_TRUST_DELAY` / `PYRY_AGENT_RUN_PROMPT_DELAY` to compress its PTY-Drive sleeps; neither runtime path has timing knobs.
- The `stdout` parameter is the load-bearing test seam: `main.go` passes `os.Stdout`; tests pass `*bytes.Buffer{}`.
- `cmd/pyry/main.go` — `case "agent-run": return runAgentRun(os.Stdout, os.Args[2:])` in the top-level dispatch switch (daemon-free verb shape, no `parseClientFlags`).

## Tests

- `TestParseAgentRunArgs_HappyPath` / `_Errors` / `_EffortValidValues` / `_AllowedToolsForms` and `TestSplitAllowedTools` — pin the flag surface (unchanged across all cutovers).
- `TestBuildStreamRunnerClaudeArgs_Shape` (renamed in #470 from `TestBuildClaudeArgs_Shape`) — two table rows (canonical + alternate effort). Asserts the exact argv via `slices.Equal` against an explicit `want`, plus named structural assertions and negative pins on the three banned legacy flags.
- `TestAgentRunUsageDescription` — scaffold-only stale-disclaimer guard plus required-substring pins: `stream-json`, `--max-turns`, `--allowed-tools`, `PYRY_USE_STREAMJSON`, `PTY`.

### Streamrunner-branch tests (pinned via `configureFakeClaude`)

- `TestAgentRunStreamJSONFake` — fake claude entry point gated by `PYRY_AGENT_RUN_FAKE=1`. Drains stdin (optionally captured to `GO_AGENT_RUN_FAKE_STDIN_FILE`) and switches on `GO_AGENT_RUN_FAKE_MODE`: `clean` (default) emits three canned lines (`system init` / `assistant text` / `result success`) and exits 0; `exit1` exits 1 after draining; `sleep` sleeps 30s.
- `configureFakeClaude(t)` — writes a shell wrapper that re-execs the test binary with `-test.run=^TestAgentRunStreamJSONFake$`. Sets `PYRY_CLAUDE_BIN` + `PYRY_AGENT_RUN_FAKE=1`, **and (post-#470) `PYRY_USE_STREAMJSON=1`** so the wrapped fake-claude tests exercise the streamrunner branch unchanged.
- `TestRunAgentRun_StreamJSON_Clean` — end-to-end fake under the streamrunner branch: asserts stream-json events in order `system init` → `assistant` → `result success`, the stream-json envelope round-trips the prompt-file content onto claude's stdin, and the no-settings-file negative filesystem pins hold.
- `TestRunAgentRun_StreamJSON_NonZeroExit` — pins `errors.As(err, &exitErr)` with `ExitCode() == 1` and `agent-run: ` prefix.

### Ptyrunner-branch tests (pinned via package-level seams)

- `installFakeSeams(t)` — overrides `trustMark` / `settingsWrite` / `ptyRun` / `newSessionID` with no-op success stubs and registers `t.Cleanup` to restore each. Individual tests re-override any seam they need a specific behaviour from.
- `TestRunAgentRun_DispatchesToPtyRunnerByDefault` — env unset; captures the `ptyrunner.Config` and asserts every required field is populated.
- `TestRunAgentRun_EnvSet1DispatchesToStreamRunner` — env=`"1"`; stubs `ptyRun` to `t.Fatal`; clean exit via the fake.
- `TestRunAgentRun_EnvNon1ValueDispatchesToPtyRunner` — table over `"true"`, `"yes"`, `"on"`, `"streamjson"`, `"0"`, `"false"`, `""` ; each routes to ptyrunner. Pins the strict-`"1"` predicate against future contributor drift.
- `TestRunAgentRun_PtyPath_TrustFailure_MentionsClaudeJson` / `_SettingsFailure_NamesSettingsStep` / `_PtyRunError_Wrapped` — AC #3 failure-surface pins. Each stubs a single seam to return `errors.New(...)`, asserts the resulting error string contains the expected operator-readable substring (`~/.claude.json` / `settings` / the underlying ptyrunner message) and starts with `agent-run: `.
- `TestRunAgentRun_PtyPath_SettingsRemovedOnSuccess` / `_SettingsRemovedOnFailure` — exercise the production `settings.WriteSettings` via a capture-wrapped seam (so the path that comes back actually exists at the moment `ptyRun` runs), then assert `os.Stat(capturedPath)` returns `fs.ErrNotExist` after `runAgentRun` returns. Pins the defer-LIFO cleanup contract on both success and failure paths.
- `TestRunAgentRun_PtyPath_WorkDirIsTrustResolvedRealpath` — feeds a sentinel realpath `"/sentinel/realpath"` through `trustMark`, asserts `captured.WorkDir == sentinel` (not `parsed.workdir`).
- `TestRunAgentRun_PtyPath_SessionIDIsUUIDv4` — asserts `sessions.ValidID(captured.SessionID)` (canonical UUIDv4 shape).
- `TestRunAgentRun_PtyPath_ConfigWiring` — table over two fixtures (model `sonnet-4-6` / `opus-4-7`, effort `medium` / `max`, max-turns `3` / `12`); each row asserts every captured Config field round-trips byte-for-byte from `parsed agentRunArgs`.
- `TestRunAgentRun_PtyPath_AllowedToolsPassedToSettings` — pins the deny-default allowlist's load-bearing path from `--allowed-tools` CLI argument through `splitAllowedTools` to the `settings.WriteSettings` argument slice.

### Real-claude smoke (env-gated)

- `TestRunAgentRun_RealClaude` — `t.Skip` unless `PYRY_E2E_REAL_CLAUDE=1`. Two subtests share the same prompt + budget: `default_pty_path` (env unset) and `fallback_streamrunner_path` (`t.Setenv("PYRY_USE_STREAMJSON", "1")`). Both assert the dispatcher-expected wire shape on stdout (at least one `type:"system"`, `type:"assistant"`, `type:"result"`). 90s deadline. Gate is an env var (not a build tag) so CI's default `go test ./...` skips cleanly. Per-PR CI exercises the real-claude byte-equivalence check at the ptyrunner boundary via [#482](https://github.com/pyrycode/pyrycode/issues/482); this smoke covers the cmd→helpers→ptyrunner wiring at a different scope.

The verb-level ctx-cancel test is deliberately omitted: streamrunner and ptyrunner each own their SIGTERM/SIGKILL grace behaviour; the verb's only ctx-cancel logic is one line (`return nil` if `errors.Is(err, context.Canceled)`). The omission is documented inline in the test file so it's intentional, not forgotten.

### Production-claude JSONL emission

Production claude under `--output-format stream-json` still writes a JSONL session file at `~/.claude/projects/<encoded-cwd>/<session-id>.jsonl`. The verb no longer reads it; the fake does not produce it. Verification of claude's own JSONL emission is left to manual smoke against a real `claude` binary — see #375 for the self-check refactor that owns that surface.

## Field stability

`agentRunArgs`'s field names are the contract sibling tickets read against. If a future sibling needs a field rename, file a separate cleanup ticket rather than renaming in a behaviour-adding slice.

## Operator rollback procedure

Set `PYRY_USE_STREAMJSON=1` in the dispatcher's `.env` (or in the per-agent service unit). Pyry dispatches to `streamrunner.Run` for that process's lifetime, byte-equivalent to the #391–#469 behaviour. The dispatcher's stream-json parser is satisfied by either mode — no agents-repo code change required. Unset (or set to anything other than the exact string `"1"`) restores the ptyrunner default at next spawn.

## Out of scope (deferred)

- `--self-check` adaptation for the ptyrunner default path — tracked under [#473](https://github.com/pyrycode/pyrycode/issues/473). Until that lands, `--self-check` exercises the streamrunner-style argv path that #336 / #375 originally targeted.
- streamrunner package deletion — operator decision 2026-05-19: streamrunner stays as a sibling indefinitely for billing-classification comparison post-2026-06-15.
- `--permission-prompt-tool stdio` protocol handling for the mobile case — separate ticket once mobile design lands.
- Propagating claude's exact non-zero exit code as the pyry process exit code — AC#4 says "claude's exit code on non-zero exit" but also "no changes required on the dispatcher's salvage path"; the dispatcher's existing tolerance for "exit 1 on any failure" is the binding constraint.
- Pgid-kill semantics for hostile children — current shape is single-PID SIGTERM/SIGKILL via stdlib `cmd.Cancel` + `cmd.WaitDelay = 5s` on the streamrunner branch, and ptyrunner's own teardown chain on the default branch.
