# `--permission-prompt-tool stdio` protocol spike (#383)

Captured trace of `claude` invoked with `--permission-prompt-tool stdio` to inform the pyrycode-mobile permission-relay design. The spike runs `claude` directly (not via `pyry agent-run`) and writes raw stdout events to `internal/e2e/realclaude/testdata/permission_protocol_v<version>_<mode>.json`.

## TL;DR

Across all six `--permission-mode` values (`default`, `acceptEdits`, `auto`, `plan`, `dontAsk`, `bypassPermissions`), `claude` v2.1.143 **did not emit any permission-gate event on stdout** when invoked with the argv shape below, and `--allowed-tools Read` did **not** prevent Bash execution. Per the spike's acceptance criteria, no follow-up assertion-based test is filed: no event fired in any mode.

## Observed `claude` version

`2.1.143 (Claude Code)` — full output captured per fixture in `claude_version_raw`.

## Argv used in the spike

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

## Captured event sequence (every mode, identical shape)

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

## Findings

### 1. No permission-gate event fires on stdout in any mode

In every fixture (`permission_protocol_v2.1.143_<mode>.json` for the six modes), the stdout stream goes directly `init → thinking → tool_use → tool_result → thinking → text → result`. There is no envelope whose `type` field is `control_request`, `permission_request`, `tool_permission_request`, or anything resembling a gate prompt. The hypothesized `{"type":"control_request","request_id":"...","request":{...}}` shape (the architect's inferred convention) **was not observed**.

### 2. `--allowed-tools Read` is not enforced under this argv

In every mode — including `default` — claude invoked Bash (event `[2]`) and the Bash tool actually executed against the worktree (`tool_result` in event `[3]` contains real `ls -la` output, including the spike-runner's POSIX username). The `result` trailer reports `permission_denials: []` and `is_error: false`.

This is consistent with the production contract documented in `internal/e2e/realclaude/allowed_tools_enforcement_test.go`: that test passes pyry's full argv (which includes `--dangerously-skip-permissions`) and asserts the Bash invocation is suppressed. The spike's argv differs in two ways — it omits `--dangerously-skip-permissions` AND adds `--permission-prompt-tool stdio` — and the result is that `--allowed-tools` is not gating Bash.

The most plausible interpretation (unverified by the spike): `--permission-prompt-tool <tool>` expects the name of a registered tool (typically MCP-prefixed, e.g. `mcp__permissions__approve`) that claude consults synchronously for each non-allowlisted invocation. The literal string `stdio` does not resolve to a known tool, so claude either falls back to "allow everything" or silently no-ops the gate. The `--allowed-tools` allowlist appears to be advisory at the boundary level under this argv shape, not authoritative.

### 3. `permissionMode: "auto"` is echoed back as `"default"` in the init envelope

`--permission-mode auto` is accepted by `--help` but is treated as a synonym for `default` — the init envelope's `permissionMode` field reads `"default"` for that mode. Every other listed mode is echoed verbatim.

### 4. The `init` envelope's `tools` array is the full registry, not the allowlist

In every mode, `tools` contains all 27 tools (`Task`, `AskUserQuestion`, `Bash`, `Edit`, …) regardless of `--allowed-tools Read`. The allowlist is not reflected in the bootstrap event; it is enforced (if at all) at tool-use time.

## Expected response shape on stdin

**Not inferable from this spike.** No request envelope was observed, so there is no captured `request_id` or request shape to mirror. The architect's pre-spike hypothesis — a `{"type":"control_response","request_id":"...","response":{...}}` shape derived from claude's elsewhere-control protocol convention — remains a hypothesis. Determining the actual response shape would require either:

- Re-running the spike with `--permission-prompt-tool` set to a registered MCP tool name and capturing the resulting stdio traffic, OR
- Reading the VS Code Claude Code extension source to see what it sends/receives on the stdio channel.

## Interaction order: `--allowed-tools` vs `--permission-prompt-tool`

Inferred from the captured traces: when `--permission-prompt-tool stdio` is set and `--dangerously-skip-permissions` is absent, `--allowed-tools` does not function as a hard deny gate. Bash invocations bypass the allowlist and execute. The "order" question reduces to: `--permission-prompt-tool` short-circuits the allowlist enforcement.

This is the opposite of what would be useful for the mobile design's deny-by-default posture. The mobile relay will likely need to combine `--dangerously-skip-permissions --allowed-tools <relay-vetted-set>` for hard enforcement, and replicate the prompt-tool flow separately through a registered MCP tool.

## Reproducing the matrix

The test file `internal/e2e/realclaude/permission_protocol_spike_test.go` runs ONE mode (the `permissionMode` constant at the top of the file). To reproduce the full sweep:

1. Set `ANTHROPIC_API_KEY` (the test uses `WithWorktreeAuthenticated` and skips without it).
2. For each mode in `{default, acceptEdits, auto, plan, dontAsk, bypassPermissions}`:
   - Edit the `permissionMode` constant.
   - `go test -tags e2e_realclaude -run TestRealClaude_PermissionProtocol_Spike ./internal/e2e/realclaude/...`
   - Rename `testdata/permission_protocol_v<version>.json` to `_<mode>.json` before the next run (the test always writes the version-only filename).
3. Restore the constant to its committed value (`default`).

Total cost across the six modes was approximately `$0.08` (~`$0.014` per cache-cold run × 6).

## Files

- `internal/e2e/realclaude/permission_protocol_spike_test.go` — the spike test.
- `internal/e2e/realclaude/testdata/permission_protocol_v2.1.143_<mode>.json` — six captured fixtures (one per mode).
- `internal/e2e/realclaude/testdata/permission_protocol_v2.1.143_default.json` — the canonical default-mode fixture referenced by the spike test on rerun.

## Follow-up

Per AC: no follow-up issue is filed. The "filed only if a real event fires" branch is not triggered because no permission event fired in any mode.

If the mobile design later requires probing the stdio protocol against a real registered prompt tool, that is a separate ticket and should be scoped against the VS Code extension's behavior (which is the known consumer of this flag).
