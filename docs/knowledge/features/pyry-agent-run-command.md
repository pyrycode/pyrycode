# `pyry agent-run` — supervised headless claude turn

The CLI verb that replaces `claude -p` in the dispatcher. Phase A spike (#329) greenlit this verb as the dispatcher's headless entry point; behaviour landed in slices: #337 scaffold, #339 settings file, #342 trust-state pre-population, #332 spawn + PTY-drive, #348/#349 JSONL line reader + tail watcher, #354 stream-json stdout emitter + result trailer.

## What it does today

1. Parses and validates the full flag set.
2. Pre-populates `projects[<realpath(workdir)>].hasTrustDialogAccepted = true` in `~/.claude.json` (#342) so the supervised claude does not block on the workspace-trust TUI dialog at startup.
3. Writes the per-spawn deny-default settings JSON to the workdir (#339).
4. Prints the resolved settings-file path behind the stable `settings-file: ` marker.
5. Mints a UUIDv4 (#354) and passes it to claude via `--session-id` so pyry knows exactly which `~/.claude/projects/<encoded-cwd>/<sid>.jsonl` to watch.
6. Spawns `claude` in a PTY (#332), drives one user-turn (defensive trust-dialog Enter → typed prompt from `--prompt-file`), and background-drains PTY output.
7. Concurrently runs the JSONL tail watcher (#349) against the minted session file. Each parsed `Event` is re-emitted onto stdout as one stream-json line (`Event.Raw + '\n'`, byte-equivalent to `claude -p --output-format stream-json`), with assistant `Usage` blocks aggregated.
8. The watcher's deterministic end-of-turn callback cancels the parent ctx, which propagates through `errgroup` → claude SIGTERM via `cmd.Cancel` → child exit → both goroutines unwind.
9. Composes and emits a single `type:"result"` trailer line (#354) with `subtype` / `terminal_reason` / `is_error` / `num_turns` / `stop_reason` / `session_id` / aggregated `usage` / `duration_ms`. Classification: clean EOT → `success`/`completed`; non-zero claude exit or watcher I/O failure → `error_during_execution`; `max_turns` is a future #334 plug-point via `streamjson.Emitter.SetExitReason`.
10. On `SIGTERM` / `SIGINT` to pyry: forwards SIGTERM to the child via `cmd.Cancel`; the runtime SIGKILLs after a 5s `WaitDelay` if the child has not exited.

```
$ pyry agent-run --prompt-file p.txt --system-prompt-file s.txt \
    --allowed-tools "Read,Bash" --max-turns 3 --effort medium \
    --model sonnet-4-6 --workdir ./repo --output-format stream-json
settings-file: /abs/path/to/repo/.pyry-agent-run-settings.json
{"type":"system","subtype":"init","cwd":"/abs/path/to/repo",…}
{"type":"user","message":{…}}
{"type":"assistant","message":{…,"stop_reason":"end_turn",…}}
{"type":"result","subtype":"success","is_error":false,"duration_ms":…,"num_turns":1,…,"terminal_reason":"completed"}
```

Stdout contract: the `settings-file:` marker, followed by one stream-json line per parsed JSONL `Event`, followed by exactly one `type:"result"` trailer as the last line. The dispatcher consumes this stream as if it were `claude -p --output-format stream-json` output. PTY output from claude itself is drained into `io.Discard` (consumers read claude's JSONL from disk via the tail watcher, not from PTY).

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

Errors render via `main()`'s standard wrapper as `pyry: agent-run: --<flag>: <reason>` and exit non-zero. Trailing positionals are rejected with `agent-run: unexpected positional %q`. `--help` falls through `flag.ContinueOnError` to the registered `fs.Usage`, printing a one-line synopsis and `fs.PrintDefaults()`.

## Implementation

- `cmd/pyry/agent_run.go` — `agentRunArgs` unexported struct (stable field names: `promptFile`, `systemPromptFile`, `allowedTools []string`, `maxTurns`, `effort`, `model`, `workdir`, `outputFormat`), `parseAgentRunArgs(args) (agentRunArgs, error)`, `splitAllowedTools(raw) []string` pure tokeniser (`strings.FieldsFunc` over `r == ',' || unicode.IsSpace(r)` plus trim + empty drop), `validEfforts` package-level set, `requireRegularFile` / `requireDir` helpers that surface `os.Stat` errors verbatim. `runAgentRun(stdout io.Writer, args []string)` does: parse → `os.UserHomeDir()` → `agentrun.MarkWorkdirTrusted(home, workdir)` (#342) → `agentrun.WriteSettings(workdir, allowedTools)` (#339) → write `settings-file:` marker to `stdout` → `os.ReadFile(promptFile)` → `newSessionUUID()` (#354) → `streamjson.New(Config{Writer: stdout, SessionID: <uuid>})` (#354) → `signal.NotifyContext(SIGTERM, SIGINT)` → `tail.New` with `OnEvent → emitter.Emit` and `OnEndOfTurn → cancel` (#349/#354) → `errgroup.WithContext(ctx)` running `watcher.Run` and `agentrun.Drive` concurrently → `classifyForEmitter(em, g.Wait())` → `emitter.Close()` → wrapped-or-nil return. Order is mark-trust → settings → spawn so any prep failure short-circuits before the next step lands artefacts. Errors wrap as `agent-run: <step>: %w` (e.g. `agent-run: read prompt-file:`, `agent-run: mint session id:`, `agent-run: stream emitter:`, `agent-run: tail watcher:`, `agent-run: drive:`).
- The `stdout` parameter is the load-bearing test seam: `main.go` passes `os.Stdout`; tests pass `*bytes.Buffer{}`. The pattern is recent (#354) and replaces the older `os.Pipe()` redirection ceremony for stdout capture; existing tests with the pipe pattern can migrate when next touched.
- `newSessionUUID() (string, error)` — `crypto/rand` 16 bytes with the version-4 nibble (byte 6 high nibble = `0x4`) and RFC-4122 variant nibble (byte 8 high nibble = `0x8/9/a/b`), formatted `8-4-4-4-12` hex. Mirrors `internal/conversations/id.go:NewID` verbatim; not extracted to a shared helper — this is a leaf call site.
- `classifyForEmitter(em *streamjson.Emitter, err error)` — translates the errgroup result into a `SetExitReason` override. `nil` and `context.Canceled` are NOT overrides (the emitter's `Close` default handles the EOT-observed vs not-observed split: EOT+ctx.Canceled → completion, no-EOT+anything → error). Any other error (`*exec.ExitError`, watcher I/O failure) calls `SetExitReason(ExitReasonError)`. The trailer is best-effort: if `Close` returns an error, it is `slog.Warn`-logged but does not change the verb's exit code.
- `buildClaudeArgs(parsed, settingsPath, sessionID) []string` — pure helper assembling the claude argv: `--settings <path> --permission-mode default --model <m> --append-system-prompt-file <sp> --effort <e> --session-id <uuid>` (#354 added the last pair). Two **security invariants** pinned by `TestBuildClaudeArgs_Shape`:
  - `--permission-mode default` MUST appear (the deny-default settings file requires it; `acceptEdits` would silently override and defeat #339's whitelist).
  - `--allowedTools` MUST NOT appear (the settings file is the sole authority; the flag is additive in interactive mode and would silently broaden the allow-list).
  - `--max-turns` and `--output-format` are accepted at the pyry CLI surface but NOT propagated — claude's interactive mode does not honour `--max-turns`, and `stream-json` is `-p`-mode only.
- Test-only env knobs (production never sets them): `PYRY_CLAUDE_BIN` injects a fake-claude binary path; `PYRY_AGENT_RUN_TRUST_DELAY` / `PYRY_AGENT_RUN_PROMPT_DELAY` compress sleeps to ~ms via `parseDurationEnv` (empty / unparseable → zero → DriveConfig defaults).
- `cmd/pyry/main.go` — `case "agent-run": return runAgentRun(os.Stdout, os.Args[2:])` in the top-level dispatch switch (next to `runUpdate`; daemon-free verb shape, no `parseClientFlags` since `agent-run` does not dial the control socket); one help-text entry in `printHelp()`; one bullet in the package-doc comment block.
- `cmd/pyry/agent_run_test.go` — table-driven tests on `parseAgentRunArgs` (happy path, every missing-required and bad-value row, all five valid `--effort` values, three `--allowed-tools` shapes, the standalone `splitAllowedTools` contract). End-to-end `TestRunAgentRun_EmitsSettingsFile` asserts the on-disk JSON + marker line. `TestRunAgentRun_MarksWorkdirTrusted` (#342) asserts the trust mark in `<HOME>/.claude.json` under the realpath-resolved key. `TestBuildClaudeArgs_Shape` pins argv ordering, the two security invariants, and the `--session-id <uuid>` pair (with UUIDv4-shape regex). `TestNewSessionUUID_Shape` (#354) pins the version-4 nibble + RFC-4122 variant nibble layout. `TestRunAgentRun_DrivesFakeClaude` (#332, extended #354) drives the full verb against a `TestAgentRunFakeClaude` fake injected via `PYRY_CLAUDE_BIN`; the fake writes a canned `end_turn` JSONL line into `<home>/.claude/projects/<encoded(workdir)>/<sid>.jsonl` so the watcher's EOT trigger fires, and the test asserts pyry's stdout contains the `settings-file:` marker, the assistant line byte-equivalent, and a `type:"result"` `subtype:"success"` trailer as the last line. Sleep knobs compressed to ~50ms. The shared `newValidArgsFixture` redirects HOME via `t.Setenv("HOME", t.TempDir())` so no test mutates the developer's real `~/.claude.json`.

## Field stability

`agentRunArgs`'s field names are the contract sibling tickets read against:

- Trust-state pre-population (#342, landed) consumes `workdir` for `MarkWorkdirTrusted(home, workdir)`.
- Settings-file emit (#339, landed) consumes `workdir` and `allowedTools` for `WriteSettings`.
- Spawn + drive (#332, landed) consumes `promptFile` (as PTY-typed bytes), `systemPromptFile`, `model`, `effort` in `buildClaudeArgs`. `maxTurns` and `outputFormat` are accepted but NOT propagated to interactive claude — see `buildClaudeArgs` for why.

If a sibling needs a field rename, file a separate cleanup ticket rather than renaming in a behaviour-adding slice — the freeze on the surface is what lets #337 land before any of the consumers exist.

## Out of scope (deferred to siblings)

- Budget Counter integration — #334 landed the Counter itself; wiring `Counter.Terminate → streamjson.Emitter.SetExitReason(ExitReasonMaxTurns)` (so the trailer reports `max_turns` for budget-driven kills) is a sibling integration ticket.
- Boot-time schema self-check that the settings file still enforces deny-default against a live claude — #336.
- Pgid-kill semantics for hostile children (current shape is single-PID SIGTERM/SIGKILL, consistent with the existing supervisor) — follow-up if observed.
- A pyry-flag surface (`-pyry-name`, etc.); `agent-run` is standalone like `pyry update`, not daemon-attached.
- Final-assistant-text in the trailer's `result` field — always `""` today; v1 dispatcher reads `r.result || ""` so empty is functionally fine. Re-parse the last assistant `Event.Raw` on Close if a consumer ever needs it.
