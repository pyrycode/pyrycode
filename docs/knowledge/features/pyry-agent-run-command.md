# `pyry agent-run` — supervised headless claude turn

The CLI verb that replaces `claude -p` in the dispatcher. Phase A spike (#329) greenlit this verb as the dispatcher's headless entry point; behaviour lands in slices (#337 scaffold, #339 settings file, #342 trust-state pre-population, downstream spawn + JSONL watch).

## What it does today

Parses and validates the full flag set, pre-populates `projects[<realpath(workdir)>].hasTrustDialogAccepted = true` in `~/.claude.json` (#342) so the supervised claude does not block on the workspace-trust TUI dialog at startup, writes the per-spawn deny-default settings JSON to the workdir (#339), and prints the resolved settings-file path behind a stable marker on stdout:

```
$ pyry agent-run --prompt-file p.txt --system-prompt-file s.txt \
    --allowed-tools "Read,Bash" --max-turns 3 --effort medium \
    --model sonnet-4-6 --workdir ./repo --output-format stream-json
settings-file: /abs/path/to/repo/.pyry-agent-run-settings.json
```

No claude spawn yet. The marker line is the verb's stable stdout contract — sibling #332 will scrape it with `^settings-file: (.+)$` to pass `--settings <path>` to the supervised claude. No other line is printed to stdout on success; trust-state pre-population is silent.

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

- `cmd/pyry/agent_run.go` — `agentRunArgs` unexported struct (stable field names: `promptFile`, `systemPromptFile`, `allowedTools []string`, `maxTurns`, `effort`, `model`, `workdir`, `outputFormat`), `parseAgentRunArgs(args) (agentRunArgs, error)`, `splitAllowedTools(raw) []string` pure tokeniser (`strings.FieldsFunc` over `r == ',' || unicode.IsSpace(r)` plus trim + empty drop), `validEfforts` package-level set, `requireRegularFile` / `requireDir` helpers that surface `os.Stat` errors verbatim so tests assert only the flag-name prefix. After a successful parse, `runAgentRun` resolves `os.UserHomeDir()` (#342), calls `agentrun.MarkWorkdirTrusted(home, parsed.workdir)`, then `agentrun.WriteSettings(parsed.workdir, parsed.allowedTools)`, and prints `settings-file: <path>\n`. Order is mark-trust → settings so a trust-mark failure short-circuits before any per-spawn artefact lands in `workdir`.
- `cmd/pyry/main.go` — `case "agent-run": return runAgentRun(os.Args[2:])` in the top-level dispatch switch (next to `runUpdate`; daemon-free verb shape, no `parseClientFlags` since `agent-run` does not dial the control socket); one help-text entry in `printHelp()`; one bullet in the package-doc comment block.
- `cmd/pyry/agent_run_test.go` — table-driven tests on `parseAgentRunArgs` directly. Covers the happy path, every missing-required and bad-value row from AC, each of the five valid `--effort` values, the three `--allowed-tools` shapes (comma / space / mixed), and the standalone `splitAllowedTools` contract. End-to-end `TestRunAgentRun_EmitsSettingsFile` redirects stdout via `os.Pipe()`, drives `runAgentRun` with a valid argv, and asserts both the on-disk JSON and the exact `settings-file: <abs path>\n` stdout. `TestRunAgentRun_MarksWorkdirTrusted` (#342) asserts the trust mark lands in `<HOME>/.claude.json` under the realpath-resolved key. The shared `newValidArgsFixture` redirects HOME via `t.Setenv("HOME", t.TempDir())` so neither test mutates the developer's real `~/.claude.json`.

## Field stability

`agentRunArgs`'s field names are the contract sibling tickets read against:

- Trust-state pre-population (#342, landed) consumes `workdir` to call `agentrun.MarkWorkdirTrusted(home, workdir)`.
- Settings-file emit (#339, landed) consumes `workdir` and `allowedTools` to write the per-spawn deny-default JSON.
- Spawn ticket (downstream) consumes `promptFile`, `systemPromptFile`, `maxTurns`, `outputFormat`, `model`, `effort` to build the eventual `claude` argv.

If a sibling needs a field rename, file a separate cleanup ticket rather than renaming in a behaviour-adding slice — the freeze on the surface is what lets #337 land before any of the consumers exist.

## Out of scope (deferred to siblings)

- `--settings <path>` argument wiring on the supervised claude (#332 consumes the marker line).
- Boot-time schema self-check that the settings file still enforces deny-default against a live claude (#336).
- `claude` process spawn with the resolved argv (downstream of #329 Phase B).
- JSONL watch / stream-json frame relay (downstream).
- A pyry-flag surface (`-pyry-name`, etc.); `agent-run` is standalone like `pyry update`, not daemon-attached.
