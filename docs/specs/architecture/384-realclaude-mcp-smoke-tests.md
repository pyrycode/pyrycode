# Spec: realclaude MCP server smoke tests (#384)

## Files to read first

The reading list below is the developer's turn-1 data load. Open each in order; the spec assumes you have. Line ranges are illustrative — file may have shifted by a few lines under refactor.

- `internal/e2e/realclaude/fixtures.go` — `RunPyryAgentRun` (line 123), `RunOpts` (line 83), `RunResult` (line 111), `WithWorktree` (line 32), `WithWorktreeAuthenticated` (line 45), `ReadJSONL` (line 59). The four new tests call `RunPyryAgentRun` with the same shape as every other realclaude test; reuse, don't fork.
- `internal/e2e/realclaude/tool_loop_test.go` — full file. Two things to lift, both same-package-visible:
  - `parseContentBlocks` (line 168) + `contentBlock` struct (line 155) — decoder for assistant/user message content arrays.
  - `parseResultTrailer` (line 197) + `resultTrailer` struct (line 185) — decoder for the stream-json trailer.
  - The tool-loop assertion shape (lines 67–121) — assistant `tool_use` → user `tool_result` correlation by `tool_use_id` → subsequent assistant text. Identical structural walk applies to MCP tools; only the expected `name` differs.
- `internal/e2e/realclaude/per_agent_test.go` — full file. Reference for the per-role test pattern. Note that the dispatcher's `dispatcherBaseTools` list at lines 31–53 uses `mcp__context7__*` (no plugin prefix). The actual tool names exposed by the local claude differ — see § "Tool-name pinning" below.
- `internal/e2e/realclaude/resilience_test.go` — `TestRealClaude_BashTool_NonZeroExit` (line 35) for the JSONL tool-loop assertion idiom with `is_error` and content-non-empty checks.
- `internal/e2e/realclaude/prompt_fidelity_test.go` — `jsonlPathFor` (line 79) for failure-diagnostic path construction.
- `cmd/pyry/agent_run.go` — `buildClaudeArgs` (line 254). Confirms that `pyry agent-run` does NOT pass `--mcp-config`; MCP server resolution happens via claude's own config discovery from `$HOME`. This drives the HOME-handling decision below.
- `docs/PROJECT-MEMORY.md` § "Where things live" — what to update under `docs/knowledge/codebase/<ticket>.md` in the documentation phase (out of scope for this developer turn, but informs the test-file location and conventions).

External reference (not in this repo): running `claude mcp list` locally shows the canonical server-name shapes, e.g.:

```
qmd: qmd mcp - ✓ Connected
codegraph: codegraph serve --mcp - ✓ Connected
plugin:context7:context7: npx -y @upstash/context7-mcp - ✓ Connected
plugin:figma:figma: https://mcp.figma.com/mcp (HTTP) - ✓ Connected
```

The line shape and connection-status sentinels (`✓ Connected`, `✗ Failed to connect`, `! Needs authentication`) are the only thing the pre-flight probe parses.

## Context

The five dispatcher agents (po, architect, developer, code-review, documentation) declare `mcp__qmd__*`, `mcp__context7__*`, `mcp__codegraph__*`, and `mcp__plugin_figma_figma__*` in their `--allowed-tools` list. If any of those MCP servers undergoes a protocol break — renamed tool, changed parameter shape, broken stdio handshake at startup — the affected agent silently fails (the model never calls the tool, or the call wedges) or hangs to `max_turns`. Today the only signal that catches this is a post-hoc human noticing degraded agent quality.

Ticket #384 adds four nightly e2e smoke tests, one per MCP server, that drive a haiku turn through `pyry agent-run` to verify each server is reachable, the protocol handshake succeeds, the named tool fires, and a non-empty `tool_result` returns. The four tests are independent top-level functions so the nightly board surfaces four independent pass/fail signals; a regression in any single server is attributable on sight (same pattern as `per_agent_test.go`'s five per-role tests).

Unblocked by #372 (`WithWorktree` + `ReadJSONL`) and #373 (`RunPyryAgentRun`), both landed.

## Design

### Single new file

`internal/e2e/realclaude/mcp_smoke_test.go` — build-tagged `//go:build e2e_realclaude`, package `realclaude`, zero production source changes.

### Tool-name pinning

The four MCP tool names this test pins, exactly as they must appear on `--allowed-tools` and in the JSONL `tool_use` block's `name` field:

| Server display name (in `claude mcp list`) | Tool name to pass / assert |
| --- | --- |
| `qmd` | `mcp__qmd__query` |
| `codegraph` | `mcp__codegraph__codegraph_search` |
| `plugin:context7:context7` | `mcp__plugin_context7_context7__resolve-library-id` |
| `plugin:figma:figma` | `mcp__plugin_figma_figma__get_metadata` |

The plugin-prefixed names (`plugin_context7_context7`, `plugin_figma_figma`) differ from the dispatcher's `per_agent_test.go:dispatcherBaseTools` which uses unprefixed `mcp__context7__*`. The unprefixed names happen to be accepted in the dispatcher's allowed-tools today because that test never actually triggers the server (the role smoke prompts are too simple to compel a context7 call), so the mismatch is invisible. For this ticket, the developer pins the **claude-reported names** (with the plugin prefix) so the test actually fires the tool. A follow-up to align `dispatcherBaseTools` with the real names is noted in § Open questions — out of scope here.

### HOME handling

Existing tests use `WithWorktree(t)`, which pins `HOME=<t.TempDir()>` so JSONL writes are hermetic. That isolation breaks for MCP smoke tests: claude reads its MCP server registry from `$HOME/.claude.json` plus `$HOME/.claude/plugins/...`, and an empty tempdir HOME has zero servers configured. With no servers configured, claude refuses every `mcp__*` tool name in `--allowed-tools` as unknown — the prompt produces no `tool_use` and the test fails for the wrong reason (config absence, not protocol drift).

**Resolution: the four MCP smoke tests do NOT pin HOME.** They allocate a `t.TempDir()` workdir and leave `HOME` pointing at the outer operator's home, letting claude discover the same MCP server set the operator has configured. Trade-off: nightly e2e runs leave per-test JSONL files under the operator's `~/.claude/projects/<encoded-tempdir>/`. Acceptable because (a) the encoded paths use t.TempDir which is unique per run, (b) `WithWorktreeAuthenticated` already leaks `ANTHROPIC_API_KEY` from outer environment, so the suite is not fully hermetic today, (c) CI runners have their own controlled HOME so the leak is operationally bounded.

Concretely, no helper is added to `fixtures.go`. The four tests inline the workdir allocation. See § "Helper: file-local pre-flight" for the only shared helper.

### Auth precondition

Each test calls `RunPyryAgentRun` with model `claude-haiku-4-5`, which exercises the real Anthropic API. The four tests need `ANTHROPIC_API_KEY` set in the outer environment, same as `WithWorktreeAuthenticated`. Each test starts with an inline check:

```
if os.Getenv("ANTHROPIC_API_KEY") == "" { t.Skipf("...named-variable skip message...") }
```

Mirrors `fixtures.go:46-53`'s skip-message shape. Do NOT call `WithWorktreeAuthenticated` itself — that pins HOME, which defeats MCP discovery.

### Pre-flight prerequisite probe

A single same-package-private helper:

- `mcpServerHealthy(t, displayName string) (skip bool, reason string)` — runs `claude mcp list` once, parses each line for the prefix `<displayName>:` and the connection-status sentinel that follows ` - ` at the end of the line. Returns `(false, "")` if the line carries `✓ Connected`; returns `(true, "<reason>")` otherwise (server missing, `✗ Failed to connect`, `! Needs authentication`). Caller pattern: `if skip, reason := mcpServerHealthy(t, "qmd"); skip { t.Skipf(...) }`.

Notes:
- The probe runs `claude` from the **outer** environment (PATH-resolved binary, outer HOME). Does NOT honor `PYRY_CLAUDE_BIN` (which has fork-bomb defenses elsewhere and is reserved for stubbed pyry tests, not for the real claude shelled out here).
- One `exec.Command` per test (not cached) — `claude mcp list` is fast and the cost is bounded by 4 invocations per nightly run.
- Parse contract: split stdout by lines, trim each, match the leading `<displayName>:` literal (with the colon), then check whether the suffix contains `✓ Connected`. The bundled health glyphs are stable claude CLI output; if claude renames them this test fails and that's an intended early warning for the operator.
- "Configured but unhealthy" (e.g. `! Needs authentication` for figma) → skip with the reason string. "Configured and connected" → run.

### Codegraph index seeding

The codegraph test additionally needs a `.codegraph/` index in the workdir; an MCP server with no index returns empty results regardless of protocol health. Sequence inside `TestRealClaude_MCP_CodeGraph`:

1. After the auth + mcpServerHealthy gates pass, write a tiny Go source file to `<workdir>/seed.go` with one named function (e.g. `func PyrycodeSeedSentinel384() {}`). The function name is distinctive enough that searching for it returns a deterministic single match.
2. Run `codegraph index .` in the workdir via `exec.Command`. If the command fails or `codegraph` is not on PATH, `t.Skipf(...)` with a named-variable message. Bound by a 30-second timeout via `exec.CommandContext`.
3. Proceed with `RunPyryAgentRun` whose prompt asks claude to call `mcp__codegraph__codegraph_search` for the sentinel function name. Assert the resulting JSONL contains a `tool_use` with `name == "mcp__codegraph__codegraph_search"` and a matching `tool_result` whose `content` is non-empty.

### Figma test target

Use a known-good public Figma Community file URL as the argument to `get_metadata`. The developer picks a stable URL — Figma's published "Material 3 Design Kit" community page or similar — and pins it as a file-level `const figmaTestFileURL = "https://www.figma.com/community/file/..."`. Document the URL choice in a top-of-test comment: *"If this URL stops resolving, replace with another public Figma Community file; the test asserts the protocol round-trip, not any one file's content."*

If `mcpServerHealthy(t, "plugin:figma:figma")` returns "Needs authentication" or "Failed to connect", the test skips cleanly — matches the AC's "skip cleanly when prerequisite is absent".

### Test structure (per test)

Each of the four tests follows the same six-step shape; differences are pinned in the table below.

1. Skip if `ANTHROPIC_API_KEY` unset.
2. Skip if `mcpServerHealthy(t, displayName)` reports unavailable.
3. (codegraph only) Seed a `.codegraph/` index in workdir; skip on indexer failure.
4. `RunPyryAgentRun` with the per-test prompt + allowed-tools.
5. Assert `result.ExitCode == 0` and `result.SessionID != ""`.
6. Walk the JSONL (via `ReadJSONL` + `parseContentBlocks`) and assert:
   - exactly one assistant entry contains `tool_use` with the expected `name`,
   - the matching user entry contains `tool_result` with the same `tool_use_id` and non-empty `content`,
   - no permission denials in the result trailer (`parseResultTrailer`).

Per-test parameters (the only thing that differs across the four tests):

| Test | `displayName` | Allowed tools | Prompt (paraphrased) | Tool-name asserted | Extra steps |
| --- | --- | --- | --- | --- | --- |
| `TestRealClaude_MCP_QMD` | `qmd` | `["mcp__qmd__query","Bash"]` | "Use mcp__qmd__query to search the second-brain collection for the literal `pyrycode`." | `mcp__qmd__query` | none |
| `TestRealClaude_MCP_Context7` | `plugin:context7:context7` | `["mcp__plugin_context7_context7__resolve-library-id","Bash"]` | "Use mcp__plugin_context7_context7__resolve-library-id to resolve the library id for `react`." | `mcp__plugin_context7_context7__resolve-library-id` | none |
| `TestRealClaude_MCP_CodeGraph` | `codegraph` | `["mcp__codegraph__codegraph_search","Bash"]` | "Use mcp__codegraph__codegraph_search to find `PyrycodeSeedSentinel384` in the current directory." | `mcp__codegraph__codegraph_search` | seed + `codegraph index .` |
| `TestRealClaude_MCP_Figma` | `plugin:figma:figma` | `["mcp__plugin_figma_figma__get_metadata","Bash"]` | "Use mcp__plugin_figma_figma__get_metadata on `<figmaTestFileURL>` and tell me the file name." | `mcp__plugin_figma_figma__get_metadata` | none |

`Bash` is added to each allowed-tools list per AC: "passes `--allowed-tools` containing only the MCP tool(s) it exercises plus `Bash` for verification". The verification utility of `Bash` is that the model may use `Bash` once to validate the result (e.g., echoing the parsed value); the test does NOT assert on Bash invocation, only that it is in the allowed list as a passive declaration matching the AC.

All four tests share: `MaxTurns: 3`, `Effort: "low"`, `Model: "claude-haiku-4-5"`. Constraints from AC.

### Helper: file-local pre-flight

Sketched signature (developer writes the body):

```go
// mcpServerHealthy checks `claude mcp list` for displayName followed by
// "✓ Connected". Returns (true, reason) when the server is absent,
// failed, or needs auth — caller invokes t.Skipf with the reason.
func mcpServerHealthy(t *testing.T, displayName string) (skip bool, reason string)
```

Lives at the bottom of `mcp_smoke_test.go`. Same-package-private. Pseudocode in 6–8 lines of body: `exec.Command("claude", "mcp", "list")` → 10s timeout → scan stdout lines → look for `<displayName>:` → if found check for `✓ Connected` → return classification.

The probe MUST NOT block on a slow MCP server. If `claude mcp list` itself times out (e.g. one server is hung), the helper returns `(true, "claude mcp list timeout after 10s")` and the test skips. This is correct behavior: hung-on-list is operator-environment trouble, not a #384 regression to surface.

### What NOT to add

- Do NOT add a new fixture helper to `fixtures.go`. Keep `mcpServerHealthy` local to the test file. Reason: the helper is one-purpose (MCP-smoke pre-flight), nobody else needs it yet, and minimizing the `fixtures.go` surface keeps it stable for parallel branches (#363 has open changes to that file).
- Do NOT pass `--mcp-config` or `--strict-mcp-config` to claude. That would require a flag-surface change to `cmd/pyry/agent_run.go`, which is out of scope.
- Do NOT thread MCP server discovery through `cmd/pyry`. The test owns the probe; pyry remains MCP-agnostic.
- Do NOT use `WithWorktree(t)` or `WithWorktreeAuthenticated(t)`. Both pin HOME, which disables MCP discovery. See § "HOME handling".
- Do NOT assert on specific `tool_result.content` text. Content is server- and version-dependent (e.g., qmd may return different snippets across runs). Asserting non-emptiness via `len(content) > 0` is sufficient and matches `resilience_test.go:125-127`.

## Concurrency model

The four tests are independent top-level functions; Go's test runner schedules them as it pleases. Each test owns its workdir (`t.TempDir`) and its `RunPyryAgentRun` invocation. No shared mutable state across tests. `t.Parallel()` is NOT called (matches the existing realclaude tests — keeps the nightly cost predictable and avoids contention on the shared MCP server stdio sockets, e.g. two `claude mcp list` calls hitting `npx -y @upstash/context7-mcp` simultaneously).

## Error handling

- `t.Skipf` for: missing `ANTHROPIC_API_KEY`, MCP server absent/failed/needs-auth, `codegraph index` failure or absent binary, `claude` binary absent on PATH.
- `t.Fatalf` for: non-zero `pyry agent-run` exit, empty `SessionID`, missing expected `tool_use`, missing matching `tool_result`, empty `tool_result.content`, JSONL parse error in result trailer.
- Skip messages MUST include the named variable / probe identifier so an operator can act on it from the test log alone. Mirror `fixtures.go:49` and `resilience_test.go:290`.

## Testing strategy

These tests ARE the test. Run them via:

```
ANTHROPIC_API_KEY=... go test -tags=e2e_realclaude -run 'TestRealClaude_MCP_' -v ./internal/e2e/realclaude/
```

The developer's local validation:
1. With operator's full MCP config: all four pass (or skip cleanly per server's availability).
2. With `unset ANTHROPIC_API_KEY`: all four skip with a named-variable message — no Anthropic API spend.
3. With a stubbed-out figma (e.g. unplug figma authentication temporarily): the figma test skips, the other three still pass.
4. Cost ceiling: ~$0.10 across the four tests (haiku × max-turns 3 × ~3 KB context each), matching the AC's "~$0.08 per nightly run" estimate.

## Open questions

- **Dispatcher allowed-tools alignment**: `agents/dispatcher/src/dispatch.ts` declares `mcp__context7__*` and (in this repo) `dispatcherBaseTools` mirrors that unprefixed form. The actual claude tool names are `mcp__plugin_context7_context7__*`. The mismatch is currently invisible because the role smoke tests don't compel context7 calls. Once #384 lands and pins the real names, file a follow-up ticket to align the dispatcher list. NOT in scope for this developer turn.
- **Cost monitoring**: nightly run cost is bounded at the AC level (~$0.08) but not enforced in code. If a future change pushes turns past 3 silently (e.g. a model refusal driving max_turns hits), the cost climbs. Acceptable for now; revisit if the suite grows past 10 MCP-bearing tests.
- **Plugin-server identifier drift**: claude could rename `plugin:context7:context7` → some new format in a future release. The test fails loudly (skip via mcpServerHealthy not finding the prefix), and the operator updates `displayName`. This is acceptable failure mode — it's exactly the protocol-drift signal #384 exists to surface.
