# `--permission-prompt-tool stdio` protocol spike (#383)

Compatibility findings for `claude --permission-prompt-tool stdio`. The original
spike runs `claude` directly (not via `pyry agent-run`) and writes raw stdout
events to `internal/e2e/realclaude/testdata/permission_protocol_v<version>_<mode>.json`.

## Current finding: Claude Code 2.1.259 (#2342)

The literal prompt-tool value `stdio` is a bidirectional control-protocol endpoint
in Claude Code 2.1.259. `TestRealClaude_StdioPermissionPromptDeny` observed a
`control_request` whose request subtype was `can_use_tool`, with a non-empty
request id, tool name `Bash`, and a non-empty tool-use id before the requested
command executed. It wrote the matching deny `control_response` with
`streamsup.WriteCanUseToolDeny`; Claude completed the turn with a successful
`result` carrying a permission denial for the same Bash tool-use id, and the
test-owned side-effect witness remained absent. The dispatcher's 2026-09-11 live
gate ran this test against `2.1.259 (Claude Code)`; the test passed in 5.16 seconds.

The current argv retains the old spike's stream-json input/output, verbose output,
`--allowed-tools Read`, `--permission-prompt-tool stdio`,
`--permission-mode default`, two-turn cap, and absence of
`--dangerously-skip-permissions`. Its exact differences from the 2.1.143 default
arm are:

```
claude
  --input-format stream-json
  --output-format stream-json
  --verbose
  --permission-prompt-tool stdio
  --mcp-config <generated-config>
  --strict-mcp-config
  --permission-mode default
  --append-system-prompt-file <test-file>
  --model haiku
  --effort low
  --max-turns 2
  --allowed-tools Read
```

- Adds `--mcp-config <generated-config>` and `--strict-mcp-config`; the generated
  config registers the production-shape `pyry_approve` and `pyry_files` servers.
- Adds `--append-system-prompt-file <test-file>` and `--effort low` through
  `streamrunner.BuildClaudeArgs`.
- Changes the model argument from `claude-haiku-4-5` to the `haiku` alias.
- Exercises only `--permission-mode default`, rather than the old six-mode manual
  matrix.

Unlike the historical capture, the test keeps stdin open after the user turn so
it can answer the request. It is an assertion-based compatibility gate, retains
only redacted event counts and subtype censuses, and commits no raw fixture.

## Historical finding: Claude Code 2.1.143 (#383)

The sections below preserve the 2.1.143 capture. Their protocol and design
conclusions are historical; the 2.1.259 finding above supersedes them for current
Claude versions.

### TL;DR

Across all six `--permission-mode` values (`default`, `acceptEdits`, `auto`, `plan`, `dontAsk`, `bypassPermissions`), `claude` v2.1.143 **did not emit any permission-gate event on stdout** when invoked with the argv shape below, and `--allowed-tools Read` did **not** prevent Bash execution. Per the spike's acceptance criteria, no follow-up assertion-based test is filed: no event fired in any mode.

### Observed `claude` version

`2.1.143 (Claude Code)` — full output captured per fixture in `claude_version_raw`.

### Argv used in the spike

```
claude
  --input-format stream-json
  --output-format stream-json
  --verbose
  --allowed-tools Read
  --permission-prompt-tool stdio
  --permission-mode <mode>
  --max-turns 2
  --model claude-haiku-4-5
```

Divergence from `pyry agent-run`'s canonical argv (`cmd/pyry/agent_run.go::buildClaudeArgs`):

- NO `--dangerously-skip-permissions` — that flag suppresses gates and would defeat the spike.
- ADDS `--permission-prompt-tool stdio` and `--permission-mode <mode>`.
- OMITS `--append-system-prompt-file` and `--effort` to keep the input surface minimal across reruns.

Stdin envelope (one line, then EOF):

```json
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Use the Bash tool to run `ls -la` and report the result."}]}}
```

This matches the shape `internal/agentrun/streamrunner` writes in production. The simpler `{"type":"user","content":"..."}` shape was not tested — the production shape was accepted with no stderr error.

### Captured event sequence (every mode, identical shape)

```
[0] system/init                  — claude bootstrap; echoes permissionMode, lists all 27 tools, apiKeySource: ANTHROPIC_API_KEY
[1] assistant {content: thinking}
[2] assistant {content: tool_use(Bash, command: "ls -la")}
[3] user      {content: tool_result(stdout: <real ls output>, is_error: false)}
[4] assistant {content: thinking}
[5] assistant {content: text}
[6] result/success               — is_error: false, permission_denials: []
```

Exit code: `0`. No `context_deadline_tripped`. No stderr output. No `control_request` / `permission_request` / `denial` envelopes. Per-fixture event counts/durations in `stdout_events`/`duration_ms`.

### Findings

#### 1. No permission-gate event fires on stdout in any mode

In every fixture (`permission_protocol_v2.1.143_<mode>.json` for the six modes), the stdout stream goes directly `init → thinking → tool_use → tool_result → thinking → text → result`. There is no envelope whose `type` field is `control_request`, `permission_request`, `tool_permission_request`, or anything resembling a gate prompt. The hypothesized `{"type":"control_request","request_id":"...","request":{...}}` shape (the architect's inferred convention) **was not observed**.

#### 2. `--allowed-tools Read` is not enforced under this argv

In every mode — including `default` — claude invoked Bash (event `[2]`) and the Bash tool actually executed against the worktree (`tool_result` in event `[3]` contains real `ls -la` output, including the spike-runner's POSIX username). The `result` trailer reports `permission_denials: []` and `is_error: false`.

This is consistent with the production contract documented in `internal/e2e/realclaude/allowed_tools_enforcement_test.go`: that test passes pyry's full argv (which includes `--dangerously-skip-permissions`) and asserts the Bash invocation is suppressed. The spike's argv differs in two ways — it omits `--dangerously-skip-permissions` AND adds `--permission-prompt-tool stdio` — and the result is that `--allowed-tools` is not gating Bash.

The most plausible interpretation at the time (unverified by the spike) was that
`--permission-prompt-tool <tool>` expected the name of a registered tool and that
the literal string `stdio` did not resolve. Claude Code 2.1.259 disproved that
interpretation: `stdio` now names the control-protocol endpoint and gates the
non-allowlisted Bash call.

#### 3. `permissionMode: "auto"` is echoed back as `"default"` in the init envelope

`--permission-mode auto` is accepted by `--help` but is treated as a synonym for `default` — the init envelope's `permissionMode` field reads `"default"` for that mode. Every other listed mode is echoed verbatim.

#### 4. The `init` envelope's `tools` array is the full registry, not the allowlist

In every mode, `tools` contains all 27 tools (`Task`, `AskUserQuestion`, `Bash`, `Edit`, …) regardless of `--allowed-tools Read`. The allowlist is not reflected in the bootstrap event; it is enforced (if at all) at tool-use time.

### Expected response shape on stdin

**Not inferable from this spike.** No request envelope was observed, so there is no captured `request_id` or request shape to mirror. The architect's pre-spike hypothesis — a `{"type":"control_response","request_id":"...","response":{...}}` shape derived from claude's elsewhere-control protocol convention — remains a hypothesis. Determining the actual response shape would require either:

- Re-running the spike with `--permission-prompt-tool` set to a registered MCP tool name and capturing the resulting stdio traffic, OR
- Reading the VS Code Claude Code extension source to see what it sends/receives on the stdio channel.

### Interaction order: `--allowed-tools` vs `--permission-prompt-tool`

Inferred from the captured traces: when `--permission-prompt-tool stdio` is set and `--dangerously-skip-permissions` is absent, `--allowed-tools` does not function as a hard deny gate. Bash invocations bypass the allowlist and execute. The "order" question reduces to: `--permission-prompt-tool` short-circuits the allowlist enforcement.

This was the opposite of what the mobile design needed at 2.1.143. It is not
current guidance: the 2.1.259 compatibility test proves the direct stdio deny
round trip that the planned permission routing relies on.

### Reproducing the matrix

The test file `internal/e2e/realclaude/permission_protocol_spike_test.go` runs ONE mode (the `permissionMode` constant at the top of the file). To reproduce the full sweep:

1. Set `ANTHROPIC_API_KEY` (the test uses `WithWorktreeAuthenticated` and skips without it).
2. For each mode in `{default, acceptEdits, auto, plan, dontAsk, bypassPermissions}`:
   - Edit the `permissionMode` constant.
   - `go test -tags e2e_realclaude -run TestRealClaude_PermissionProtocol_Spike ./internal/e2e/realclaude/...`
   - Rename `testdata/permission_protocol_v<version>.json` to `_<mode>.json` before the next run (the test always writes the version-only filename).
3. Restore the constant to its committed value (`default`).

Total cost across the six modes was approximately `$0.08` (~`$0.014` per cache-cold run × 6).

### Files

- `internal/e2e/realclaude/permission_protocol_spike_test.go` — the spike test.
- `internal/e2e/realclaude/testdata/permission_protocol_v2.1.143_<mode>.json` — six captured fixtures (one per mode).
- `internal/e2e/realclaude/testdata/permission_protocol_v2.1.143_default.json` — the canonical default-mode fixture referenced by the spike test on rerun.

### Regression contract on the null findings (#418)

The spike test PASSES on any non-structural outcome — it pins the on-disk artefact, not its shape. To prevent a future `claude` release from silently flipping the findings (e.g. starting to emit permission events on stdio, or enforcing `--allowed-tools` under this argv), `internal/e2e/realclaude/permission_protocol_regression_test.go` walks every fixture matching `testdata/permission_protocol_v*_*.json` and asserts the four findings as a regression contract. Pure JSON parse + assert; no API call, no subprocess; excluded from `make check` by the `e2e_realclaude` build tag.

Per fixture, the test asserts:

- No `stdout_events[i].type` is `control_request` / `permission_request` / `tool_permission_request` (finding #1).
- `result.permission_denials` is empty (finding #1/#2).
- `init.tools` contains `"Bash"` (finding #4 — the full registry, not the allowlist).
- `init.permissionMode` echoes the `<mode>` token parsed out of the filename, with the `auto → default` synonym from finding #3 applied at the parser.
- Structural sanity: `exit_code == 0`, `context_deadline_tripped == false`, `len(stdout_events) >= 7`.

The filename glob is the source of truth for matrix coverage — a future spike rerun that adds a new mode (e.g. `--permission-mode foo`) drops a new fixture and the regression test picks it up automatically with no code change. Each failure message names the offending fixture and which finding flipped (e.g. `permission_protocol_v2.1.143_auto.json: init.permissionMode = "auto", want "default" (spike finding #3 flipped)`), so the spike-runner is pointed directly at the section above to revisit. See [`codebase/418.md`](../codebase/418.md) for implementation detail.

### Follow-up

Per AC: no follow-up issue is filed. The "filed only if a real event fires" branch is not triggered because no permission event fired in any mode.

If the mobile design later requires probing the stdio protocol against a real registered prompt tool, that is a separate ticket and should be scoped against the VS Code extension's behavior (which is the known consumer of this flag).
