# `pyry agent-run` — supervised headless claude turn

The CLI verb that replaces `claude -p` in the dispatcher. Phase A spike (#329) greenlit this verb
as the dispatcher's headless entry point. Its runtime path cycled twice — PTY-blind-type drive,
then a stream-json subprocess pipeline (#391), then back to a PTY drive via `ptyrunner` (#470) to
chase billing eligibility — before **#1348 deleted the PTY entry point and its `ptyrunner`
dependency outright**. `runAgentRunStreamRunner` (`cmd/pyry/agent_run.go`) is the only path that
has existed since: `runAgentRun` calls it unconditionally, with no selector branch. See § History
below for what each cycle was for and why it ended.

## What it does today

1. Recognises `--self-check` positionally and short-circuits to `runAgentRunSelfCheck(stdout)`
   (#336) before any flag parsing — the eight required production flags do not apply to the
   diagnostic verb.
2. Parses and validates the full flag set (`parseAgentRunArgs`).
3. Reads `--prompt-file` into memory.
4. Resolves the claude binary path (`PYRY_CLAUDE_BIN` env override → default `"claude"`).
5. Installs `signal.NotifyContext` for `SIGTERM` / `SIGINT`.
6. Calls `runAgentRunStreamRunner` unconditionally:
   - Writes the per-spawn deny-default settings file via
     `settingsWrite = settings.WriteSettingsWithDeny(parsed.allowedTools, parsed.disallowedTools)`
     (see [agentrun-settings-subpackage.md](agentrun-settings-subpackage.md)), registers
     `defer os.Remove(settingsPath)`. This file, delivered via `--settings`, is the tool boundary —
     see § Tool boundary below.
   - Builds argv via `buildStreamRunnerClaudeArgs(parsed, yolo=true, "", settingsPath)`, which calls
     the shared [`streamrunner.BuildClaudeArgs`](streamrunner-package.md) (also used by the
     permission self-check, so the same argv shape is what gets verified).
   - Hands argv + prompt bytes to `streamrunner.Run`, which spawns claude and forwards its
     stream-json stdout byte-for-byte.
7. Maps the return: nil or `context.Canceled` (operator teardown) → exit 0; any other error →
   wrapped `agent-run: %w` → exit 1.

`PYRY_USE_STREAMJSON` is read nowhere in this path any more. Setting it is harmless — the five
dispatcher forks that still carry it in their `.env` do not need editing; the line can be swept out
of them whenever convenient. Pinned by `TestRunAgentRun_SelectorVariableNoLongerDecides`.

```
$ pyry agent-run --prompt-file p.txt --system-prompt-file s.txt \
    --allowed-tools "Read,Bash" --max-turns 3 --effort medium \
    --model sonnet-4-6 --workdir ./repo --output-format stream-json
{"type":"system","subtype":"init","cwd":"/abs/path/to/repo","session_id":"…",…}
{"type":"user","message":{…}}
{"type":"assistant","message":{…,"stop_reason":"end_turn",…}}
{"type":"result","subtype":"success","is_error":false,"duration_ms":…,…}
```

Stdout contract: claude's own stream-json stdout, forwarded byte-for-byte (claude emits the
canonical `system init` and `result` events itself under stream-json mode).

Billing: this surface bills to the Keychain subscription (verified 2026-07-24 via a live run
reporting a subscription five-hour rate-limit window), which retired the #470-era belief that only
a PTY-driven surface was subscription-eligible.

## Tool boundary (the load-bearing mechanism)

`--allowed-tools` on the argv is **not** the enforcement. The per-spawn settings file passed via
`--settings` is:

- `permissions.allow` carries `--allowed-tools`'s tokens; `permissions.deny` carries the optional
  `--disallowed-tools` tokens (#411) — a tool listed in `deny` is removed from the model's surface
  entirely, not denied at call time.
- `permissions.defaultMode: "dontAsk"` auto-denies anything not pre-approved and never waits for
  input. Claude is invoked with `--permission-mode dontAsk` to match (the shared
  `streamrunner.BuildClaudeArgs` default when no override `PermissionArgs` is supplied).

**pyrycode#1387 (measured 2026-08-08):** `--dangerously-skip-permissions` defeats `--allowed-tools`
outright — it outranks every permission mode, including the settings file's own `dontAsk`. Every
agent dispatched between the 2026-07-25 fleet switch and the fix ran unrestricted, silently
returning architect-only web search and subagent spawning, the deliberately-excluded Figma write
tools, and the three human-only tools, to every role. The fix was not "add the settings file back
and keep the skip flag" — it was dropping `--dangerously-skip-permissions` from this path's argv
entirely in favour of `--permission-mode dontAsk` plus `--settings`, which survives the skip flag's
absence because there is no skip flag to survive. `TestBuildStreamRunnerClaudeArgs_Shape` pins
`--dangerously-skip-permissions` as **banned** on every row.

A second, non-YOLO argv shape exists in the same builder: `buildStreamRunnerClaudeArgs(parsed,
yolo=false, mcpConfigPath, …)` emits `--permission-prompt-tool` + `--mcp-config` (routing every tool
use through [`pyry mcp-approve`](pyry-mcp-approve-command.md)) instead of the `dontAsk` default.
`runAgentRunStreamRunner` — agent-run's sole production caller of the builder — always passes
`yolo=true`; the non-YOLO branch is exercised by unit tests here and wired to a live spawn in the
`streamsup` interactive path, not by this verb.

**#1555 → #2553: the stale-comment residue flagged here is fixed.** Code review on #1555 flagged
two pieces of pre-existing residue as out of that ticket's scope (comment-only, one file): `agent_run.go`'s
doc comment on `buildStreamRunnerClaudeArgs` describing the YOLO branch as emitting
`--dangerously-skip-permissions` (contradicted by the code and `TestBuildStreamRunnerClaudeArgs_Shape`
since the #1387 fix), and `agent_run_selfcheck.go`'s doc comments citing "the ptyrunner spawn path" and
"#470 cutover" for behaviour the self-check performs via `streamrunner.BuildClaudeArgs`. #2553 corrected
both, plus the parallel drift in `internal/agentrun/selfcheck/selfcheck.go`'s doc comments and the
`--self-check` FAIL message's operator-facing text (which had pointed at the deleted
`internal/agentrun/ptyrunner/runner.go`). See [agentrun-selfcheck-package.md](agentrun-selfcheck-package.md)
for the self-check package's own history of this drift.

## History — PTY → stream-json → PTY → stream-json-only

- **Pre-#391 (PTY drive, original).** Spawned claude in a PTY and blindly typed the prompt bytes
  after a fixed delay. On 2026-05-14, an invalid `defaultMode: "deny"` value in the per-spawn
  settings file caused claude's interactive startup to queue a `/doctor` template; the drive's
  prompt write concatenated onto that template and every dispatched agent silently reasoned about
  "how to fix the settings file" instead of the actual prompt. The stream-json subprocess pipeline
  structurally eliminates this prompt-mixing class (no blind fixed-delay type).
- **#391 → #469 (stream-json subprocess).** `claude` invoked with `--input-format/--output-format
  stream-json`, prompt delivered as a JSON envelope on stdin, `--allowed-tools` +
  `--dangerously-skip-permissions` on the argv, no settings file, no workspace-trust mark.
- **#470 (PTY drive via `ptyrunner`, since deleted).** Anthropic's 2026-06-15 billing policy named
  "Interactive Claude Code in the terminal or IDE" as subscription-eligible without naming the
  stream-json subprocess surface, so the PTY pivot chased the explicitly-named surface ahead of the
  deadline. `PYRY_USE_STREAMJSON=1` was the operator rollback knob back to the stream-json branch.
- **#1348 (PTY entry point + `ptyrunner` package deleted).** The stream-json surface turned out to
  bill to the subscription too (verified 2026-07-24), so the billing rationale for keeping a second
  drive mechanism evaporated. `runAgentRunPty`, the `PYRY_USE_STREAMJSON` branch, and
  `internal/agentrun/ptyrunner` were removed; `runAgentRunStreamRunner` became the only path.
- **pyrycode#1387 (2026-08-08).** With only one path left, its `--dangerously-skip-permissions`
  flag turned out to have silently disabled the tool allowlist since the 2026-07-25 fleet switch —
  see § Tool boundary above for the fix.
- **#1555 (comment reconciliation).** Six comments in `agent_run.go` still described the deleted
  `ptyrunner` path and a live `PYRY_USE_STREAMJSON` selector, two of them inside the #1387 evidence
  block. Reconciled to the single-path reality without losing the #1387 finding; see § Tool boundary
  above, which restates that finding in full.
- **#2553 (self-check comment reconciliation).** The two pieces of residue #1555's code review flagged
  as out of scope — `buildStreamRunnerClaudeArgs`'s stale skip-flag claim in `agent_run.go`, and
  `agent_run_selfcheck.go` / `internal/agentrun/selfcheck/selfcheck.go`'s ptyrunner/interactive-TUI
  wording, including the operator-facing `--self-check` FAIL message — were corrected. Comments and
  strings only; the spawn, argv and detector are unchanged.

`internal/agentrun/ptyrunner` no longer exists in the tree. `docs/knowledge/features/ptyrunner-package.md`
still describes it as a live sibling package — that doc is stale in the same way this one was before #1555's documentation pass, and is not corrected here (out of scope for this ticket; a sibling #1348-residue ticket owns it).

## Flags

Eight required flags plus one optional (`--disallowed-tools`, #411); each is validated at parse
time with a one-line error that names the offending flag.

| Flag | Validation |
|------|-----------|
| `--prompt-file <path>` | Must exist and be a regular file. |
| `--system-prompt-file <path>` | Must exist and be a regular file. |
| `--allowed-tools "<list>"` | Accepts comma- or whitespace-separated tokens (or any mix); trims each; rejects an empty result. |
| `--disallowed-tools "<list>"` | **Optional.** Tokenised identically to `--allowed-tools`. Absent or empty (`''`) yields a nil deny slice → no `permissions.deny` key (byte-unchanged spawn). Tokens land in `permissions.deny`, removing those tools from the model's surface entirely — this is the only `agent-run` path, so it is unconditionally load-bearing whenever supplied. |
| `--max-turns <int>` | Must be > 0; honoured by claude itself under stream-json, so pyry passes it straight through rather than counting turns of its own. |
| `--effort <enum>` | One of `low`, `medium`, `high`, `xhigh`, `max`. |
| `--model <string>` | Non-empty after trim. |
| `--workdir <dir>` | Must exist and be a directory. |
| `--output-format <stream-json>` | Literal `stream-json` only — any other value rejected. |

The dispatcher passes `--disallowed-tools "AskUserQuestion,EnterPlanMode,ExitPlanMode"` (companion
agent-dispatcher#7) so non-interactive agents are never *offered* claude's human-interaction tools —
a call to one would waste a turn on a guaranteed `dontAsk` runtime denial and arm the
agent-dispatcher#8 permission-denial watchdog.

Errors render via `main()`'s standard wrapper as `pyry: agent-run: --<flag>: <reason>` and exit
non-zero. Trailing positionals are rejected with `agent-run: unexpected positional %q`. `--help`
falls through `flag.ContinueOnError` to the registered `fs.Usage`, printing `agentRunUsageDescription`
— which already states "There is no second runner" and is the wording every other comment in the
file should agree with (this was #1555's whole point).

## Implementation

- `cmd/pyry/agent_run.go` — `agentRunArgs` unexported struct (stable field names:
  `promptFile`, `systemPromptFile`, `allowedTools []string`, `disallowedTools []string` (#411),
  `maxTurns`, `effort`, `model`, `workdir`, `outputFormat`); `parseAgentRunArgs(args) (agentRunArgs,
  error)`; `splitAllowedTools(raw) []string` pure tokeniser (`strings.FieldsFunc` over
  `r == ',' || unicode.IsSpace(r)` plus trim + empty drop — reused verbatim for
  `--disallowed-tools`); `validEfforts` package-level set; `requireRegularFile` / `requireDir`
  helpers.
- `runAgentRun(stdout, args)` — `--self-check` short-circuit → parse → read prompt file → resolve
  `PYRY_CLAUDE_BIN` → `signal.NotifyContext` → `runAgentRunStreamRunner` unconditionally → nil on
  nil or `context.Canceled`, else wrap `agent-run: %w`.
- `runAgentRunStreamRunner(ctx, stdout, parsed, claudeBin, promptBytes)` — writes the settings file,
  builds argv, delegates to `streamrunner.Run`.
- `buildStreamRunnerClaudeArgs(parsed, yolo bool, mcpConfigPath, settingsPath string) []string` — a
  thin wrapper: resolves `permissionArgs(yolo, mcpConfigPath)` (`cmd/pyry/mcp_config.go`) into the
  non-YOLO permission slot when `!yolo`, otherwise leaves it empty so
  [`streamrunner.BuildClaudeArgs`](streamrunner-package.md) falls back to its own
  `--permission-mode dontAsk` default. The argv shape itself lives in `streamrunner` so the
  permission self-check builds and verifies the identical spawn — a self-check assembling its own
  argv would verify a spawn nobody performs.
- `agentRunUsageDescription` constant — the `--help` body, pinned byte-accurate by
  `TestAgentRunUsageDescription` (#359) against stale-disclaimer regressions. States the single-path
  fact and the #1387 enforcement mechanism in prose; #1555 brought the Go comments into agreement
  with it rather than the reverse.
- `cmd/pyry/agent_run_selfcheck.go` — `runAgentRunSelfCheck(stdout)`: materialises a throwaway
  workdir, runs `selfcheck.SelfCheckDenyDefault` (builds its argv via the same
  `streamrunner.BuildClaudeArgs`) against the resolved claude binary, renders PASS / FAIL /
  inconclusive. See [agentrun-selfcheck-package.md](agentrun-selfcheck-package.md) for the algorithm.
- `cmd/pyry/mcp_config.go` — `permissionArgs(yolo, mcpConfigPath)` (the YOLO/non-YOLO switch),
  `renderMCPApproveConfig` / `writeMCPApproveConfig` (the non-YOLO `--mcp-config` document). See
  [pyry-mcp-approve-command.md](pyry-mcp-approve-command.md).
- `internal/agentrun/settings/settings.go` — `WriteSettingsWithDeny(allowedTools, disallowedTools)
  (string, error)`; JSON shape `{"permissions":{"allow":[...],"deny":[...],"defaultMode":"dontAsk"},
  "enableAllProjectMcpServers":true}` (`deny` omitted when empty). See
  [agentrun-settings-subpackage.md](agentrun-settings-subpackage.md).
- `cmd/pyry/main.go` — `case "agent-run": return runAgentRun(os.Stdout, os.Args[2:])` in the
  top-level dispatch switch (daemon-free verb shape, no `parseClientFlags`).

## Tests (`cmd/pyry/agent_run_test.go`)

- `TestParseAgentRunArgs_HappyPath` / `_Errors` / `_EffortValidValues` / `_AllowedToolsForms` /
  `_DisallowedToolsForms`, `TestSplitAllowedTools` — flag-surface pins.
- `TestBuildStreamRunnerClaudeArgs_Shape` — table-driven `slices.Equal` against an explicit `want`
  for YOLO and non-YOLO rows, plus named structural assertions: `--dangerously-skip-permissions`
  banned on every row; YOLO requires `--permission-mode dontAsk` and, when a settings path is
  supplied, `--settings <path>`; non-YOLO requires the `--permission-prompt-tool` / `--mcp-config`
  pair; `--session-id` banned (PTY-only, never emitted here).
- `TestAgentRunUsageDescription` — stale-disclaimer guard plus required-substring pins.
- `TestRunAgentRun_SelectorVariableNoLongerDecides` — pins that `PYRY_USE_STREAMJSON` no longer
  changes dispatch, whatever its value.
- `TestAgentRunStreamJSONFake` / `TestRunAgentRun_StreamJSON_Clean` /
  `TestRunAgentRun_StreamJSON_NonZeroExit` — fake-claude end-to-end: stream-json event order
  (`system init` → `assistant` → `result success`), stdin envelope round-trip, non-zero exit
  wrapping (`errors.As(err, &exitErr)`, `ExitCode() == 1`, `agent-run: ` prefix).
- `TestRunAgentRun_SettingsFailure_NamesSettingsStep`, `TestRunAgentRun_SettingsRemovedOnSuccess` /
  `_OnFailure` — settings-file lifecycle pins (defer-cleanup fires on every exit path).
- `TestRunAgentRun_AllowedToolsPassedToSettings` / `_DisallowedToolsPassedToSettings` (#411) — the
  CLI-to-settings-writer wiring pin for both the allow and deny slices.
- `TestRunAgentRun_RealClaude` — `t.Skip` unless `PYRY_E2E_REAL_CLAUDE=1`; asserts the
  dispatcher-expected wire shape (`system` / `assistant` / `result` events) against a real claude
  spawn, 90s deadline.

The verb-level ctx-cancel test is deliberately omitted: `streamrunner` owns SIGTERM/SIGKILL grace;
the verb's only ctx-cancel logic is the one-line `errors.Is(err, context.Canceled)` check.

## Field stability

`agentRunArgs`'s field names are the contract sibling tickets read against. If a future sibling
needs a field rename, file a separate cleanup ticket rather than renaming in a behaviour-adding
slice.

## Out of scope (deferred)

- Reconciling `agent_run.go`'s `buildStreamRunnerClaudeArgs` doc comment and
  `agent_run_selfcheck.go`'s doc comments to the current `--permission-mode dontAsk` / no-ptyrunner
  reality — flagged by #1555 code review as pre-existing residue in production files, which the
  documentation phase does not edit. Sibling ticket.
- `docs/knowledge/features/ptyrunner-package.md` and its `internal/agentrun/ptyrunner` subject —
  the package was deleted in #1348; the doc was not. Sibling ticket.
- `--permission-prompt-tool stdio` protocol handling for the mobile case — separate ticket once
  mobile design lands.
- Propagating claude's exact non-zero exit code as the pyry process exit code — the dispatcher's
  existing tolerance for "exit 1 on any failure" is the binding constraint, not the exact code.
