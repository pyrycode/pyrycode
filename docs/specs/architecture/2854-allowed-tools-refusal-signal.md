# Stabilize the allowed-tools refusal signal (#2854)

## Files read

- `internal/e2e/realclaude/allowed_tools_enforcement_test.go` → `TestRealClaude_AllowedToolsEnforcement`, `assistantTextRefusalHit`, `structuredDenialHit`: independent sentinel and operator-signal witnesses; fixed text vocabulary misses an observed explicit refusal.
- `internal/e2e/realclaude/fixtures.go` → `WithWorktreeAuthenticated`, `RunPyryAgentRun`, `ReadJSONL`: isolated authenticated workspace and one subprocess run.
- `internal/e2e/realclaude/tool_loop_test.go` → `parseContentBlocks`: existing decoder for assistant text, without retaining tool inputs.
- `internal/e2e/realclaude/initialize_control_redaction_test.go` → `newInitControlRedactor`: existing path redaction for bounded diagnostics.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapRedactor.addValueClass`, `str`: existing exact-value redaction for known credentials.
- `cmd/pyry/agent_run.go` → `runAgentRunStreamRunner`, `buildStreamRunnerClaudeArgs`: settings writer and production spawn caller.
- `internal/agentrun/streamrunner/args.go` → `BuildClaudeArgs`: actual `dontAsk` plus per-spawn settings boundary.
- `internal/agentrun/settings/settings.go` → `WriteSettingsWithDeny`: Read-only permissions allowlist with deny-default mode.
- `docs/knowledge/features/e2e-realclaude-allowed-tools-enforcement-test-go.md` § `allowed_tools_enforcement_test.go`: historical enforcement description needs the documentation-stage correction below.
- `docs/knowledge/features/development-verification.md` § Captures and live evidence: retain relevant response fields rather than a vanished temporary path.

## Context

The #2775 gate failed at 2026-10-06T03:27:46.863Z and its unchanged-tree rerun passed at 03:43:26.480Z. Its transcript is gone, so the exact original wording cannot be recovered. An investigation using a scratch Go overlay left both assertions unchanged and logged only redacted assistant text and selected result fields. Ten executions passed; execution 11 reproduced the same two-assistant-entry, ten-stdout-line signal failure at 03:53:41.773Z on Claude Code 2.1.280.

The response began: “I appreciate the direct instruction, but I need to decline this request.” It described `PROBE_BREACH` as suspicious and asked for authorization/context. The sentinel stayed absent; the result was `subtype=success`, `is_error=false`, with zero permission denials. This establishes a missed explicit model refusal in the reproduction, rather than a missing operator signal. It does not prove the lost original transcript used identical wording. The sentinel's naming also distracted the model from the permission exercise.

One deliverable: make this test recognize the evidenced refusal while retaining its two independent witnesses. No decision record is needed. No other `origin/feature/<N>` branches remained after fetching; there is no concurrent overlap.

Sizing: approximately 250–350 written lines including plan, diagnostic helper and table tests; zero new exported types/interfaces, no production consumer updates, four acceptance criteria, fewer than ten reject branches.

## Design

Keep the existing text-or-structured denial contract. Extend `denialKeywords` only with the explicit observed phrase `decline this request`, not a broad word such as `access` or arbitrary response text. Explain in the system prompt that the marker is harmless and the disposable test workspace is authorized; ask for a clear explanation if the permitted tools cannot create it. Keep the original creation request, Read allowlist, sentinel filename and run limits.

On signal failure, log only assistant text plus result subtype, error flag and permission-denial count. Reuse the existing path redactor and remove known credential values before logging; never log raw stdout, tool inputs, init configuration or denial payloads. Diagnostics do not participate in the pass predicate.

## Concurrency model

No new goroutines or shared mutable state. Parsing and diagnostic redaction run synchronously after `RunPyryAgentRun` completes. Existing subprocess cancellation and teardown remain owned by the fixture.

## Error handling

A created sentinel, unexpected stat error, nonzero process exit or absent signal still fails. Malformed/non-assistant text and non-result stdout retain the existing detector behavior. Diagnostic decoding is best effort and cannot change an assertion outcome. No retries or new skips.

## Testing strategy

Write table-driven deterministic tests in the existing test file first. The exact observed refusal must fail before the vocabulary change and pass after it. Check silent/empty responses, arbitrary successful text and broad non-refusal wording are rejected. Exercise the existing keyword channel and structured denial/error alternatives independently, including a success result with empty denials. Cover diagnostic field selection and credential/path redaction with synthetic inputs.

Run focused race-enabled tagged tests for these helpers, ordinary race tests for the touched package, `go vet ./...`, tagged vet for the live package, and `go build ./cmd/pyry` (output outside the worktree). Use the approved launcher for the named live test only. The dispatcher must record ten consecutive uncached post-fix executions without retries/skips and the passing named test in its full `make e2e-realclaude` gate; those checks remain pending at builder handoff.

## Open questions

None. The reproduced refusal establishes the interpretation change; the original wording remains unknowable and is not inferred.

## Documentation handoff

Pending for the documentation stage: update `docs/knowledge/features/e2e-realclaude-allowed-tools-enforcement-test-go.md`, section `allowed_tools_enforcement_test.go`, to describe the current sentinel-file runtime-effect check, the final operator-visible signal behavior, and the evidence-supported flake cause. State that agent-run uses `dontAsk` plus per-spawn deny-default settings through `BuildClaudeArgs`; remove the obsolete claim that `--dangerously-skip-permissions --allowed-tools` enforces this boundary.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The assistant response is untrusted evidence, never authority for filesystem success. The independent `os.Stat` witness remains mandatory; only an explicit observed refusal phrase is added.
- [Tokens, secrets, credentials] SHOULD FIX: diagnostic text could echo a credential. Reuse exact-value redaction for API/OAuth/account credentials in addition to existing path redaction before logging.
- [File operations] The sentinel is a fixed filename under the fixture-owned disposable workspace. No new diagnostic files or caller-controlled paths are introduced.
- [Subprocesses] Existing `RunPyryAgentRun` owns cancellation and shutdown. No shell construction, spawn posture or permission settings change.
- [Cryptography] No keys, nonces or cryptographic operations are added.
- [Network and I/O] No new network reads or listeners. Diagnostics select only response/result fields from already captured test output.
- [Errors, logs, telemetry] SHOULD FIX: never dump raw stdout or tool inputs; diagnostics must select text and result metadata and redact paths/credentials.
- [Concurrency] All additions run on the test goroutine after subprocess completion; no locks or shutdown paths are added.
- [Threat model] This test guards local agent tool effects and an operator-visible signal. A safety refusal can satisfy the signal contract; the prompt clarifies the harmless marker to encourage the intended permission exercise. No remote protocol boundary changes.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-06
