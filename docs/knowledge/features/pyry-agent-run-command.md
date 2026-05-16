# `pyry agent-run` — supervised headless claude turn

The CLI verb that replaces `claude -p` in the dispatcher. Phase A spike (#329) greenlit this verb as the dispatcher's headless entry point; behaviour landed in slices: #337 scaffold, #339 settings file, #342 trust-state pre-population, #332 spawn + PTY-drive, #348/#349 JSONL line reader + tail watcher, #354 stream-json stdout emitter + result trailer, #391 cutover from PTY drive to the [`streamrunner`](streamrunner-package.md) stream-json subprocess pipeline.

## What it does today (post-#391)

1. Recognises `--self-check` positionally and short-circuits to `runAgentRunSelfCheck(stdout)` (#336) before any flag parsing — the eight required production flags do not apply to the diagnostic verb.
2. Parses and validates the full flag set.
3. Reads `--prompt-file` into memory.
4. Resolves the claude binary path (`PYRY_CLAUDE_BIN` env override → default `"claude"`).
5. Installs `signal.NotifyContext` for `SIGTERM` / `SIGINT`.
6. Calls `streamrunner.Run` with the assembled argv and the prompt bytes. The runner JSON-encodes one stream-json user-turn envelope onto claude's stdin and closes stdin; claude's stdout (the canonical stream-json event stream, including its own `system init` and `result` events) is forwarded byte-for-byte to the verb's stdout; claude's stderr is forwarded to `os.Stderr`.
7. Maps the runner's return: nil → exit 0; `context.Canceled` (operator teardown) → exit 0; `*exec.ExitError` or any other error → wrapped as `agent-run: %w` and exit 1.

```
$ pyry agent-run --prompt-file p.txt --system-prompt-file s.txt \
    --allowed-tools "Read,Bash" --max-turns 3 --effort medium \
    --model sonnet-4-6 --workdir ./repo --output-format stream-json
{"type":"system","subtype":"init","cwd":"/abs/path/to/repo","session_id":"…",…}
{"type":"user","message":{…}}
{"type":"assistant","message":{…,"stop_reason":"end_turn",…}}
{"type":"result","subtype":"success","is_error":false,"duration_ms":…,…}
```

Stdout contract: **claude's stdout, byte-for-byte**. No verb-prepended marker line, no verb-composed trailer — claude emits the canonical `system init` and `result` events itself under stream-json mode. The dispatcher's parser already consumes this shape (existed pre-#391 for the `claude -p --output-format stream-json` path).

## Why the PTY drive was removed (#391)

The pre-#391 path spawned claude in a PTY, then *blindly typed* the prompt bytes after a fixed delay. On 2026-05-14, an invalid `defaultMode: "deny"` value in the per-spawn settings file caused claude's interactive startup to queue a `/doctor` template; the drive's prompt write was concatenated with that template and **every dispatched agent silently reasoned about "how to fix the settings file" instead of the actual prompt**.

The stream-json subprocess pipeline structurally eliminates the prompt-mixing class: the prompt is delivered as one JSON envelope on stdin, not typed into a PTY input buffer.

Five mechanisms left the verb's runtime path in #391 because the stream-json pipeline subsumes them:

| Removed | Replaced by |
|---|---|
| Per-spawn `.pyry-agent-run-settings.json` (`agentrun.WriteSettings`) | `--allowed-tools` on argv (authoritative under `-p`-style stream-json mode) |
| Workspace-trust mark in `~/.claude.json` (`agentrun.MarkWorkdirTrusted`) | `--dangerously-skip-permissions` removes the trust dialog entirely |
| Verb-minted session UUID (`newSessionUUID` + `--session-id`) | Claude mints its own session id under stream-json mode and reports it in the `system init` event |
| JSONL tail watcher + `streamjson.Emitter` (`agentrun/jsonl/tail`, `agentrun/streamjson`) | Claude emits stream-json events directly on stdout |
| `settings-file: <path>` stdout marker line | The `system init` event takes over its dispatcher-metadata role |

The packages themselves stay in the tree — `cmd/pyry/agent_run_selfcheck.go` (#336) still imports `agentrun` for `WriteSettings` / `MarkWorkdirTrusted`. Cleanup of the unused subpackages is deferred to a follow-up; #391 only stops importing them from the verb's runtime path.

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

- `cmd/pyry/agent_run.go` — `agentRunArgs` unexported struct (stable field names: `promptFile`, `systemPromptFile`, `allowedTools []string`, `maxTurns`, `effort`, `model`, `workdir`, `outputFormat`), `parseAgentRunArgs(args) (agentRunArgs, error)`, `splitAllowedTools(raw) []string` pure tokeniser (`strings.FieldsFunc` over `r == ',' || unicode.IsSpace(r)` plus trim + empty drop), `validEfforts` package-level set, `requireRegularFile` / `requireDir` helpers that surface `os.Stat` errors verbatim. `runAgentRun(stdout io.Writer, args []string)` body after #391 is ~30 lines: `--self-check` short-circuit → parse → `os.ReadFile(promptFile)` → resolve `PYRY_CLAUDE_BIN` → `signal.NotifyContext` → `streamrunner.Run(ctx, Config{ClaudeBin, WorkDir: parsed.workdir, Args: buildClaudeArgs(parsed), PromptBytes, Stdout: stdout, Stderr: os.Stderr})` → return nil on nil or `context.Canceled`, else wrap `agent-run: %w`. No errgroup, no watcher, no emitter, no session-id mint.
- `buildClaudeArgs(parsed) []string` — pure helper, emits exactly:

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

  **Security invariants** pinned by `TestBuildClaudeArgs_Shape`:

  - `--input-format stream-json` / `--output-format stream-json` / `--verbose` MUST all appear (verbose is required to get assistant message events on stdout under stream-json — without it, only `result` is emitted; spike-verified 2026-05-14).
  - `--dangerously-skip-permissions` MUST appear. Acceptable here because the dispatcher is the sole caller and operates inside an isolated worktree; `--allowed-tools` is the authoritative tool gate under `-p`-style stream-json mode; the spawn's blast radius is bounded by that list, not by the trust dialog.
  - `--settings`, `--permission-mode`, `--session-id` MUST NOT appear (negative pins — these were load-bearing under the PTY/settings world and must never reappear under the stream-json pipeline).

- `parseDurationEnv(name)` remains only because `agent_run_selfcheck.go` (#336) still reads `PYRY_AGENT_RUN_TRUST_DELAY` / `PYRY_AGENT_RUN_PROMPT_DELAY` to compress its PTY-Drive sleeps; the stream-json runtime path has no timing knobs.
- The `stdout` parameter is the load-bearing test seam: `main.go` passes `os.Stdout`; tests pass `*bytes.Buffer{}`.
- `cmd/pyry/main.go` — `case "agent-run": return runAgentRun(os.Stdout, os.Args[2:])` in the top-level dispatch switch (daemon-free verb shape, no `parseClientFlags`).

## Tests

- `TestParseAgentRunArgs_HappyPath` / `_Errors` / `_EffortValidValues` / `_AllowedToolsForms` and `TestSplitAllowedTools` — pin the flag surface (unchanged across #391).
- `TestBuildClaudeArgs_Shape` — two table rows (canonical + alternate effort). Asserts the exact argv via `slices.Equal` against an explicit `want`, plus named structural assertions (`--dangerously-skip-permissions` present, `--input-format`/`--output-format` followed by `stream-json`, `--verbose` present, `--allowed-tools` followed by comma-joined tools, `--max-turns` followed by `strconv.Itoa`), plus negative pins on the three banned legacy flags.
- `TestAgentRunStreamJSONFake` — fake claude entry point gated by `PYRY_AGENT_RUN_FAKE=1`. Drains stdin (optionally captured to `GO_AGENT_RUN_FAKE_STDIN_FILE`) and switches on `GO_AGENT_RUN_FAKE_MODE`: `clean` (default) emits three canned lines (`system init` / `assistant text` / `result success`) and exits 0; `exit1` exits 1 after draining; `sleep` sleeps 30s (verb-level test doesn't exercise this but mirrors `streamrunner`'s helper shape).
- `configureFakeClaude(t)` — writes a one-line shell wrapper that re-execs the test binary with `-test.run=^TestAgentRunStreamJSONFake$`; the wrapper drops the production argv on the floor because the Go test binary's flag parser would reject real claude flags like `--input-format`. Sets `PYRY_CLAUDE_BIN` + `PYRY_AGENT_RUN_FAKE=1`.
- `TestRunAgentRun_StreamJSON_Clean` — end-to-end: spawns the fake via `runAgentRun`, asserts (a) stream-json events arrive in order `system init` → `assistant` → `result success`, (b) stdout does NOT start with `settings-file: ` (negative pin: the marker is gone), (c) neither `<workdir>/.pyry-agent-run-settings.json` nor `<HOME>/.claude.json` is written (negative filesystem pins), and (d) the prompt file's `"hello"` content was JSON-wrapped into the stream-json user-turn envelope on stdin (`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`).
- `TestRunAgentRun_StreamJSON_NonZeroExit` — pins `errors.As(err, &exitErr)` succeeds with `ExitCode() == 1` and the error string starts with `agent-run: `.

The verb-level ctx-cancel test was deliberately omitted: the streamrunner package owns the SIGTERM/SIGKILL grace behaviour (`TestRun_CtxCancelMidRun`); the verb's only ctx-cancel logic is one line (`return nil` if `errors.Is(err, context.Canceled)`). The omission is documented inline in the test file so it's intentional, not forgotten.

### Production-claude JSONL emission

Production claude under `--output-format stream-json` still writes a JSONL session file at `~/.claude/projects/<encoded-cwd>/<session-id>.jsonl`. The verb no longer reads it; the fake does not produce it. Verification of claude's own JSONL emission is left to manual smoke against a real `claude` binary — see #375 for the self-check refactor that owns that surface.

## Field stability

`agentRunArgs`'s field names are the contract sibling tickets read against. If a future sibling needs a field rename, file a separate cleanup ticket rather than renaming in a behaviour-adding slice.

## Out of scope (deferred)

- Deletion of the unused PTY-drive / trust / settings packages (`internal/agentrun/{drive,trust,settings}.go`, `internal/agentrun/{jsonl,jsonl/tail,streamjson}`) — cleanup ticket once `#375` migrates `--self-check` off them.
- `--permission-prompt-tool stdio` protocol handling for the mobile case — separate ticket once mobile design lands.
- Propagating claude's exact non-zero exit code as the pyry process exit code — AC#4 says "claude's exit code on non-zero exit" but also "no changes required on the dispatcher's salvage path"; the dispatcher's existing tolerance for "exit 1 on any failure" is the binding constraint. File a follow-up if the dispatcher later needs the exact code.
- Pgid-kill semantics for hostile children — current shape is single-PID SIGTERM/SIGKILL via stdlib `cmd.Cancel` + `cmd.WaitDelay = 5s`.
- Final-assistant-text in any trailer — claude emits its own `result` event directly; pyry never synthesises one any more.
