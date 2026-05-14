# `pyry agent-run` — supervised headless claude turn

The CLI verb that replaces `claude -p` in the dispatcher. Phase A spike (#329) greenlit this verb as the dispatcher's headless entry point; behaviour landed in slices: #337 scaffold, #339 settings file, #342 trust-state pre-population, #332 spawn + PTY-drive. JSONL watch deferred to #333.

## What it does today

1. Parses and validates the full flag set.
2. Pre-populates `projects[<realpath(workdir)>].hasTrustDialogAccepted = true` in `~/.claude.json` (#342) so the supervised claude does not block on the workspace-trust TUI dialog at startup.
3. Writes the per-spawn deny-default settings JSON to the workdir (#339).
4. Prints the resolved settings-file path behind the stable `settings-file: ` marker — the verb's sole stdout contract.
5. Spawns `claude` in a PTY (#332), drives one user-turn (defensive trust-dialog Enter → typed prompt from `--prompt-file`), background-drains PTY output, and waits for the child to exit.
6. On `SIGTERM` / `SIGINT` to pyry: forwards SIGTERM to the child via `cmd.Cancel`; the runtime SIGKILLs after a 5s `WaitDelay` if the child has not exited.

```
$ pyry agent-run --prompt-file p.txt --system-prompt-file s.txt \
    --allowed-tools "Read,Bash" --max-turns 3 --effort medium \
    --model sonnet-4-6 --workdir ./repo --output-format stream-json
settings-file: /abs/path/to/repo/.pyry-agent-run-settings.json
# (claude PTY output is drained into io.Discard, not echoed)
```

The `settings-file:` line is the only stdout line on success. Trust-state pre-population is silent; PTY output is discarded (consumers read claude's JSONL from disk — sibling #333).

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

- `cmd/pyry/agent_run.go` — `agentRunArgs` unexported struct (stable field names: `promptFile`, `systemPromptFile`, `allowedTools []string`, `maxTurns`, `effort`, `model`, `workdir`, `outputFormat`), `parseAgentRunArgs(args) (agentRunArgs, error)`, `splitAllowedTools(raw) []string` pure tokeniser (`strings.FieldsFunc` over `r == ',' || unicode.IsSpace(r)` plus trim + empty drop), `validEfforts` package-level set, `requireRegularFile` / `requireDir` helpers that surface `os.Stat` errors verbatim. `runAgentRun` does: parse → `os.UserHomeDir()` → `agentrun.MarkWorkdirTrusted(home, workdir)` (#342) → `agentrun.WriteSettings(workdir, allowedTools)` (#339) → print `settings-file:` marker → `os.ReadFile(promptFile)` → `signal.NotifyContext(SIGTERM, SIGINT)` → `agentrun.Drive(ctx, …)` (#332). Order is mark-trust → settings → spawn so any prep failure short-circuits before the next step lands artefacts. Errors wrap as `agent-run: <step>: %w` (e.g. `agent-run: read prompt-file:`, `agent-run: drive:`).
- `buildClaudeArgs(parsed, settingsPath) []string` — pure helper assembling the claude argv: `--settings <path> --permission-mode default --model <m> --append-system-prompt-file <sp> --effort <e>`. Two **security invariants** pinned by `TestBuildClaudeArgs_Shape`:
  - `--permission-mode default` MUST appear (the deny-default settings file requires it; `acceptEdits` would silently override and defeat #339's whitelist).
  - `--allowedTools` MUST NOT appear (the settings file is the sole authority; the flag is additive in interactive mode and would silently broaden the allow-list).
  - `--max-turns` and `--output-format` are accepted at the pyry CLI surface but NOT propagated — claude's interactive mode does not honour `--max-turns`, and `stream-json` is `-p`-mode only.
- Test-only env knobs (production never sets them): `PYRY_CLAUDE_BIN` injects a fake-claude binary path; `PYRY_AGENT_RUN_TRUST_DELAY` / `PYRY_AGENT_RUN_PROMPT_DELAY` compress sleeps to ~ms via `parseDurationEnv` (empty / unparseable → zero → DriveConfig defaults).
- `cmd/pyry/main.go` — `case "agent-run": return runAgentRun(os.Args[2:])` in the top-level dispatch switch (next to `runUpdate`; daemon-free verb shape, no `parseClientFlags` since `agent-run` does not dial the control socket); one help-text entry in `printHelp()`; one bullet in the package-doc comment block.
- `cmd/pyry/agent_run_test.go` — table-driven tests on `parseAgentRunArgs` (happy path, every missing-required and bad-value row, all five valid `--effort` values, three `--allowed-tools` shapes, the standalone `splitAllowedTools` contract). End-to-end `TestRunAgentRun_EmitsSettingsFile` redirects stdout via `os.Pipe()` and asserts the on-disk JSON + marker line. `TestRunAgentRun_MarksWorkdirTrusted` (#342) asserts the trust mark in `<HOME>/.claude.json` under the realpath-resolved key. `TestBuildClaudeArgs_Shape` pins argv ordering + the two security invariants. `TestRunAgentRun_DrivesFakeClaude` (#332) drives the full verb against a `TestRunHelperProcess` fake injected via `PYRY_CLAUDE_BIN`, with sleep knobs compressed to ~50ms. The shared `newValidArgsFixture` redirects HOME via `t.Setenv("HOME", t.TempDir())` so no test mutates the developer's real `~/.claude.json`.

## Field stability

`agentRunArgs`'s field names are the contract sibling tickets read against:

- Trust-state pre-population (#342, landed) consumes `workdir` for `MarkWorkdirTrusted(home, workdir)`.
- Settings-file emit (#339, landed) consumes `workdir` and `allowedTools` for `WriteSettings`.
- Spawn + drive (#332, landed) consumes `promptFile` (as PTY-typed bytes), `systemPromptFile`, `model`, `effort` in `buildClaudeArgs`. `maxTurns` and `outputFormat` are accepted but NOT propagated to interactive claude — see `buildClaudeArgs` for why.

If a sibling needs a field rename, file a separate cleanup ticket rather than renaming in a behaviour-adding slice — the freeze on the surface is what lets #337 land before any of the consumers exist.

## Out of scope (deferred to siblings)

- JSONL watch / end-of-turn detection — #333.
- Boot-time schema self-check that the settings file still enforces deny-default against a live claude — #336.
- Pgid-kill semantics for hostile children (current shape is single-PID SIGTERM/SIGKILL, consistent with the existing supervisor) — follow-up if observed.
- A pyry-flag surface (`-pyry-name`, etc.); `agent-run` is standalone like `pyry update`, not daemon-attached.
