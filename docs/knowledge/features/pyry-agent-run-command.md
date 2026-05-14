# `pyry agent-run` — supervised headless claude turn (scaffold)

The CLI verb (#337, scaffold-only) that replaces `claude -p` in the dispatcher. Phase A spike (#329) greenlit this verb as the dispatcher's headless entry point; this slice lands ONLY the flag surface so subsequent tickets (trust-state merge, per-spawn settings file, claude spawn, JSONL watch) layer behaviour on top of a tested scaffold.

## What it does today

Parses and validates the full flag set, then exits 0 with one confirmation line on stdout:

```
$ pyry agent-run --prompt-file p.txt --system-prompt-file s.txt \
    --allowed-tools "Read,Bash" --max-turns 3 --effort medium \
    --model sonnet-4-6 --workdir ./repo --output-format stream-json
pyry agent-run: flag set valid; scaffold-only ticket #337 — no spawn yet
```

No file writes, no claude spawn, no trust-state changes. The confirmation line text is not load-bearing; sibling tickets overwrite this entry point as they layer behaviour.

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

- `cmd/pyry/agent_run.go` — `agentRunArgs` unexported struct (stable field names: `promptFile`, `systemPromptFile`, `allowedTools []string`, `maxTurns`, `effort`, `model`, `workdir`, `outputFormat`), `parseAgentRunArgs(args) (agentRunArgs, error)`, `splitAllowedTools(raw) []string` pure tokeniser (`strings.FieldsFunc` over `r == ',' || unicode.IsSpace(r)` plus trim + empty drop), `validEfforts` package-level set, `requireRegularFile` / `requireDir` helpers that surface `os.Stat` errors verbatim so tests assert only the flag-name prefix.
- `cmd/pyry/main.go` — `case "agent-run": return runAgentRun(os.Args[2:])` in the top-level dispatch switch (next to `runUpdate`; daemon-free verb shape, no `parseClientFlags` since `agent-run` does not dial the control socket); one help-text entry in `printHelp()`; one bullet in the package-doc comment block.
- `cmd/pyry/agent_run_test.go` — table-driven tests on `parseAgentRunArgs` directly (not `runAgentRun`). Covers the happy path, every missing-required and bad-value row from AC, each of the five valid `--effort` values, the three `--allowed-tools` shapes (comma / space / mixed), and the standalone `splitAllowedTools` contract (including empty input → empty slice and separators-only → single token).

## Field stability

`agentRunArgs`'s field names are the contract sibling tickets read against:

- Trust-merge ticket (sibling of #337, split from #331) consumes `workdir` to locate `.claude/settings.local.json` for pre-population.
- Settings-file ticket (sibling of #337, split from #331) consumes `allowedTools`, `model`, `effort` to emit the per-spawn settings JSON.
- Spawn ticket (downstream) consumes `promptFile`, `systemPromptFile`, `maxTurns`, `outputFormat` to build the eventual `claude` argv.

If a sibling needs a field rename, file a separate cleanup ticket rather than renaming in a behaviour-adding slice — the freeze on the surface is what lets #337 land before any of the consumers exist.

## Out of scope (deferred to siblings)

- Trust-state pre-population for `--workdir` (#331 split).
- Per-spawn `settings.json` emission consuming `--allowed-tools`, `--model`, `--effort` (#331 split).
- `claude` process spawn with the resolved argv (downstream of #329 Phase B).
- JSONL watch / stream-json frame relay (downstream).
- A pyry-flag surface (`-pyry-name`, etc.); `agent-run` is standalone like `pyry update`, not daemon-attached.
