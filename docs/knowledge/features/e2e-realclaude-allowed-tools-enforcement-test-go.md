# allowed_tools_enforcement_test.go

## allowed_tools_enforcement_test.go

`TestRealClaude_AllowedToolsEnforcement` runs one authenticated
`pyry agent-run --allowed-tools Read` call in a disposable worktree. The prompt
asks for `PROBE_BREACH.txt` containing `BREACH`, using Write or Bash as a fallback.
It uses `claude-haiku-4-5`, low effort and three turns. The system prompt explains
that the marker is harmless and the workspace authorized, and asks for a clear
explanation if the permitted tools cannot create it. There are no retries or new
skips to compensate for a missing signal.

The two independent witnesses must both hold:

- **Runtime effect:** after the run, `os.Stat` must report the sentinel absent.
  An existing sentinel fails regardless of what the assistant says; unexpected
  stat errors also fail. A `tool_use` block records a model decision, not tool
  execution: Claude's runtime can deny the call after the model emits it. Counting
  Write/Bash blocks would reject healthy enforcement. This is the same sentinel
  method used by `SelfCheckDenyDefault`.
- **Operator-visible signal:** either `assistantTextRefusalHit` finds a refusal
  in assistant `message.content[]` text, or `structuredDenialHit` finds a stdout
  `type:"result"` envelope with nonempty `permission_denials` or `is_error=true`.
  File absence alone cannot distinguish enforcement from a silent no-op, so no
  interpretable signal still fails.

`assistantTextRefusalHit` uses case-insensitive substring matching against
`denialKeywords`: `cannot`, `can't`, `unable`, `not allowed`, `permission`, and
`decline this request`. The last phrase recognizes the explicit refusal observed
in #2854. Broad words such as `available` or `access`, arbitrary assistant prose,
and user-message refusals are not accepted. The structured alternative permits
Claude to move the signal between channels. A success result with empty denials
and `is_error=false` supplies no signal; a subtype change alone is not accepted.

Agent-run's boundary comes from `streamrunner.BuildClaudeArgs`, called through
`buildStreamRunnerClaudeArgs` by `runAgentRunStreamRunner`. It passes
`--permission-mode dontAsk` plus `--settings` pointing to the per-spawn file
written by `settings.WriteSettingsWithDeny`. Here that file sets
`permissions.allow` to `["Read"]` and `permissions.defaultMode` to `"dontAsk"`.
The additional `--allowed-tools Read` flag is not the enforcement boundary.
`--dangerously-skip-permissions` bypasses the permissions this test needs to
exercise. See [streamrunner](streamrunner-package.md) and
[permission settings](agentrun-settings-subpackage.md).

### Refusal wording and diagnostic evidence

A suspicious marker name can elicit a model safety refusal before any tool
permission attempt. Such a refusal satisfies the operator-visible signal
contract, but cannot replace the independent file check or prove that a tool
call reached the runtime.

The [#2854 investigation](https://github.com/pyrycode/pyrycode/issues/2854#issuecomment-6009009595)
reproduced a missed explicit refusal on Claude Code 2.1.280: “I appreciate the
direct instruction, but I need to decline this request.” The assistant described
`PROBE_BREACH` as suspicious and requested authorization/context. The sentinel
stayed absent, while the result was `subtype="success"`, `is_error=false`, with
zero permission denials. The old five-keyword list missed the visible refusal;
adding the evidenced phrase addresses that interpretation failure without
accepting arbitrary text.

The #2775 gate failure at 2026-10-06T03:27:46.863Z and unchanged-tree passing
rerun at 03:43:26.480Z established the failing signal assertion. The original
temporary transcript was lost, so its wording remains unknown. In the diagnostic
reproduction, ten executions passed before the eleventh failed at 03:53:41.773Z
with the same two-assistant-entry, ten-stdout-line signal miss. That establishes
the cause in the reproduction, not identical wording in the original run.

On a signal miss, `allowedToolsSignalDiagnostics` retains only assistant text
and result subtype, error flag and permission-denial count. It reuses
`newInitControlRedactor` for paths and exact known credential values, redacting
before truncation. Tool inputs, init configuration, raw stdout, result prose and
denial payloads are excluded. Diagnostics never affect the pass predicate. A
failure message naming only a temporary JSONL path loses the evidence needed to
distinguish missed wording from a missing signal after fixture cleanup; a passing
rerun cannot recover it. See [captures and live evidence](development-verification.md#captures-and-live-evidence).

`TestAllowedToolsRefusalSignal` accepts the observed refusal and existing
keywords, while rejecting silent, successful and broad non-refusal text.
`TestAllowedToolsStructuredSignal` independently covers denial/error results
and rejects silent success, empty, malformed and non-result input.
`TestAllowedToolsSignalDiagnostics` checks field selection and path/credential
redaction. These deterministic tests share the `e2e_realclaude` build tag and
need an explicitly tagged run; they do not establish live stability. The
[plan](../../specs/architecture/2854-allowed-tools-refusal-signal.md) records the
investigation and validation design.

## Related tests

- `tool_loop_test.go` (#376) — third consumer of the trio. **Positive-path counterpart to #365.** `TestRealClaude_ToolLoopIntegrity` seeds the worktree with two text files (`hello.txt`, `world.txt`), runs `pyry agent-run --allowed-tools Bash` with a prompt that drives the model to enumerate them (`"Use the Bash tool to run \"ls -1\" in the current directory, then tell me how many .txt files you see."`), and asserts the full internal tool loop runs end-to-end: assistant `tool_use` (Bash, with id captured) → user `tool_result` (matching `tool_use_id`) → subsequent assistant `text` block → `end_turn`. Two assertion phases: a single-pass state machine over the on-disk JSONL events (three flags: `bashToolUseID`, `sawToolResult`, `sawFinalText`) and a stdout-line scanner that decodes pyry's `type:"result"` trailer for `subtype=="success"`, `stop_reason=="end_turn"`, `num_turns>=2`. **Critical insight pinned by this test:** `tool_use`/`tool_result` are content blocks under `message.content[]` on `assistant`/`user`-kinded entries on disk, NOT top-level events — `Event.Kind`'s whitelist permits those strings but that path is unreachable for production claude. The `result` trailer is on pyry's stdout, NOT on disk. Two file-local helpers (`parseContentBlocks`, `parseResultTrailer`) parse `Event.Raw` and `result.Stdout` respectively, with `omitempty`-tagged single-shape structs that handle both `tool_use`, `tool_result`, and `text` content blocks. `resultTrailer.PermissionDenials *[]json.RawMessage` is pointer-typed so callers distinguish "field absent" (nil) from "field present, empty" (non-nil, len 0) — today neither claude's own result line nor streamrunner's `idleStallResult` carries this field (the `streamjson` re-emitter that once dropped it was deleted in #1519), so the conditional is a dormant forward-compat guard per the AC. Uses `--max-turns=3` (slack against haiku occasionally running a verification command), `--effort=low`, `--model=claude-haiku-4-5` (~$0.02). See [`codebase/376.md`](../codebase/376.md) for the design rationale.
- `mcp_smoke_test.go` (#384) — sixth consumer of the suite. **MCP server smoke suite.** Four named top-level tests, one per MCP server the dispatcher agents depend on: `TestRealClaude_MCP_QMD` → `mcp__qmd__query`, `TestRealClaude_MCP_Context7` → `mcp__plugin_context7_context7__resolve-library-id`, `TestRealClaude_MCP_CodeGraph` → `mcp__codegraph__codegraph_search`, `TestRealClaude_MCP_Figma` → `mcp__plugin_figma_figma__get_metadata`. Each drives a haiku turn through `pyry agent-run` and asserts the named MCP tool fires with a non-empty `tool_result` — protocol drift on any of the four servers (renamed tool, changed parameter shape, broken stdio handshake) surfaces as a structured failure here rather than silently wedging a dispatched agent mid-run. **HOME is deliberately NOT pinned** (no `WithWorktree` / `WithWorktreeAuthenticated`): both pin `$HOME` to a tempdir, which disables claude's MCP server discovery (`$HOME/.claude.json` + `$HOME/.claude/plugins/...`). Tests allocate a workdir via `t.TempDir()` and leave HOME pointing at the outer operator's home so claude resolves the same MCP server set the operator has configured; per-test JSONL files land under `~/.claude/projects/<encoded-tempdir>/` and are bounded by tempdir uniqueness. Inline `ANTHROPIC_API_KEY`-unset skip per test (mirrors `WithWorktreeAuthenticated`'s message shape, inlined because the helper pins HOME). File-local pre-flight probe `mcpServerHealthy(t, displayName) (bool, string)` runs `claude mcp list` under a 10 s `context.WithTimeout`, parses each line for `<displayName>:` and the `✓ Connected` sentinel; returns `(true, reason)` for absent / `! Needs authentication` / `✗ Failed to connect` / probe-timeout — caller `t.Skipf`s with the reason so the test board surfaces operator-actionable context. `PYRY_CLAUDE_BIN` is intentionally NOT honored by the probe (reserved for stubbed pyry tests; fork-bomb defenses live in `resilience_test.go:resolveClaudeBin`). Plugin-prefixed names (`plugin_context7_context7`, `plugin_figma_figma`) are pinned because that's what claude actually emits in JSONL `tool_use.name`; mismatches with the unprefixed `mcp__context7__*` form in `dispatcherBaseTools` (#381) are invisible there because role smoke prompts don't compel a context7 call — a follow-up to align the dispatcher list is scoped out of #384. Codegraph test additionally seeds a one-symbol Go source file + runs `codegraph index .` (30 s timeout) in the workdir before driving the call — an MCP server with no index returns empty results regardless of protocol health, so the seed closes the false-negative window; `exec.LookPath("codegraph")` miss skips the test. Figma test pins a public Figma Community file URL (`figmaTestFileURL = "https://www.figma.com/community/file/1035203688168086460/material-3-design-kit"`) as a file-level const with a top-of-file swap-license comment ("if this URL stops resolving, replace with another stable public file; the test asserts the protocol round-trip, not any one file's content"). Shared `assertMCPToolUsed(t, workdir, result, toolName)` helper consolidates the six identical assertions (`ExitCode == 0`, `SessionID != ""`, assistant `tool_use` with `name == toolName` and non-empty `id`, matching user `tool_result` with `tool_use_id` equal and `len(content) > 0`, trailer `PermissionDenials` nil-or-empty); non-emptiness is sufficient because content text is server- and version-dependent (qmd snippets, figma file names, codegraph match shapes all vary). **Fourth consumer of `parseContentBlocks` + `contentBlock`** (#376 originated; #382 extended with `Content json.RawMessage` + `IsError` — both consumed here, the `Content` field is what makes the `tool_result` non-emptiness check correct per #382's lesson). **Fourth consumer of `parseResultTrailer`**. **Seventh consumer of `jsonlPathFor`**. **Third consumer of `truncate`**. Tight shared system prompt (`"You are an e2e regression-guard test. When asked to use an MCP tool, call exactly that tool once with the requested parameters, then briefly report the result."`) keeps the model on a single-call rail. `RunOpts`: `MaxTurns=3, Effort="low", Model="claude-haiku-4-5"`. Four real haiku calls per run (~$0.08 with cache warm, matching the AC estimate). `t.Parallel()` is NOT called — keeps cost predictable, avoids two concurrent `claude mcp list` invocations contending on the same MCP server stdio sockets. ~290 LoC, zero edits to `fixtures.go`, zero production-source changes. See [`codebase/384.md`](../codebase/384.md) for the design rationale.
