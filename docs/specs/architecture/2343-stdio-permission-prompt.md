# #2343 — Route interactive permission asks over stdio behind a toggle

## Files read

- `internal/config/config.go` → `Config`, `DefaultConfig`, `Load` — the additive on-disk schema and zero-value/default-overlay contract for a default-off boolean.
- `cmd/pyry/main.go` → `runSupervisor`, `selectInteractiveRunner`, `approvalTimeout`, `approvalParkedReport` — the composition order, shared approval registry/window, and the existing late-bound publication pattern.
- `cmd/pyry/mcp_config.go` → `permissionArgs` — the byte-stable MCP permission argv that remains the default and remains the only shape used by `pyry agent-run`.
- `cmd/pyry/agent_run.go` → `buildStreamRunnerClaudeArgs` — confirms agent-run calls `permissionArgs` independently of the interactive factory and must not inherit this toggle.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory`, `withApprovalArgs`, `newSessionParser` — the sole interactive stream spawn-argv seam and the parser construction window where a non-blocking `can_use_tool` handler can be installed.
- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge.Surface`, `ResolveStream`, `ApprovalAnswerable`, `retire` — the existing modal/question surfacer, answer gate, liveness report, and one-shot cleanup path to reuse unchanged.
- `internal/permbridge/permbridge.go` → `Registry.Register`, `Pending.Await`, `Registry.Resolve`, `Request`, `Verdict` — the shared parked-approval primitive and fail-closed timeout semantics used by both MCP and stdio transports.
- `internal/streamsup/parser.go` → `Parser.SetCanUseToolHandler`, `CanUseToolRequest`, `decodeCanUseTool` — the child-stdout trust boundary and synchronous, non-blocking handler contract established by #2282.
- `internal/streamsup/envelope.go` → `WriteCanUseToolAllow`, `WriteCanUseToolDeny` — the measured, correlated single-line response writers established by #2342.
- `internal/streamsup/runner.go` → `Runner.Stdin`, `Runner.spawnAndWait`, `Config.OnChildExit` — the live-child stdin publication and teardown order used to bind an answer to its originating child.
- `internal/e2e/internal/fakeclaude/main.go` → `loadStdioPermissionRider`, `runStreamJSON` — the default-off rider that emits one `can_use_tool` request and accepts only its correlated response.
- `internal/e2e/relay_v2_stream_modal_test.go` → `TestRelayV2_StreamModalPermissionRoundTrip` — the current fake-daemon MCP allow/deny/disconnect proof and phone-driving pattern.
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` → `TestInteractiveStreamModalResolution`, `startStreamModalResolutionHarness` — the current live MCP allow path and reusable daemon/phone harness.
- `internal/e2e/realclaude/interactive_stream_permission_deny_test.go` → `TestInteractiveStreamPermissionDeny`, `denyModalsUntilIdle` — the current live MCP deny attribution and side-effect proof.
- `internal/e2e/realclaude/stdio_permission_prompt_test.go` → `TestRealClaude_StdioPermissionPromptDeny` — #2342's direct-child compatibility proof and response-shape precedent.
- `docs/knowledge/features/config-package.md` → `Load semantics` — absent boolean fields retain their zero value under overlay decoding.
- `docs/knowledge/features/permbridge-package.md` → `Registry surface`, `Fail-closed / default-deny` — registry concurrency, liveness, and one-shot invariants.
- `docs/knowledge/features/streamsup-package.md` → `Held-open stdin`, `Turn I/O` — the runner/parser lifecycle and held-open child stream contract.
- `docs/knowledge/features/permission-protocol-spike.md` → `Current finding: Claude Code 2.1.259 (#2342)` — the live evidence that makes the stdio route eligible for an opt-in production slice.
- `docs/knowledge/features/development-verification.md` → `Protocol boundaries`, `Test execution and artifact survival` — requires correlated protocol assertions and distinguishes live evidence from offline shape coverage.

## Context

Interactive non-bypass children currently send permission calls through the `pyry_approve` MCP subprocess. Claude Code 2.1.259 now has live evidence for the equivalent stdio `can_use_tool` request/`control_response` exchange, but rollback must remain immediate and deterministic. This change adds one daemon-startup boolean that selects the new transport only for interactive stream children, while keeping the existing registry, client surface, authorization gate, timeout/liveness policy, and MCP config document.

No new mobile protocol or durable architectural decision is introduced. The existing approval bridge remains authoritative; only the Claude-facing transport becomes selectable. The later documentation stage should update the config-package overview rather than create an ADR.

## Design

### Configuration and argv

Add `Config.StdioPermissionPrompt bool` with JSON name `stdio_permission_prompt`. Its absent and explicit-false values are the Go zero value; `DefaultConfig` requires no entry. `runSupervisor` reads the value once and threads it only into `selectInteractiveRunner` and the interactive stream factory.

Keep `permissionArgs` unchanged. `withApprovalArgs` will select the permission-prompt value while preserving the existing ordered flag set:

- toggle off: append `--permission-prompt-tool mcp__pyry_approve__approve` exactly as today;
- toggle on: append `--permission-prompt-tool stdio` while retaining `--mcp-config`, `--strict-mcp-config`, and the existing permission-mode handling;
- bypass posture or operator bypass: return the existing argv unchanged in both toggle positions.

Because `buildStreamRunnerClaudeArgs` continues to call unchanged `permissionArgs`, `pyry agent-run` cannot inherit the interactive toggle.

### Stdio approval adapter

Add an unexported cmd-layer adapter beside `newStreamRunnerFactory`. It owns no new verdict policy. For each decoded `streamsup.CanUseToolRequest` it:

1. Captures the current `Runner.Stdin` immediately on the parser goroutine, converts only `ToolName`, `Input`, and `ToolUseID` into `permbridge.Request`, and registers the ask synchronously in the daemon's shared registry. Richer stdio-only fields are deliberately discarded.
2. Returns from the parser callback without waiting for a person or performing a client broadcast. A dedicated goroutine invokes the existing surfacer, waits on `Pending.Await`, writes one allow or deny with the request's Claude-owned `RequestID`, retires the client surface, and removes its local child-pending entry.
3. On allow, passes `Verdict.UpdatedInput` to `WriteCanUseToolAllow` and sends no updated-permission rules. On deny, passes the fixed or registry-produced message to `WriteCanUseToolDeny` with `interrupt=false`.
4. On malformed correlation input or duplicate registration, emits one fixed deny response asynchronously; it never turns such a request into an allow.

Each runner factory invocation creates its own small pending-ID tracker. The factory chains `Config.OnChildExit` so every ask still registered for that runner is resolved once to a fixed deny before the existing sink exit callback runs. The waiting goroutine therefore terminates and retires its surface even if the child dies. It writes through the writer captured when the ask arrived, never through a later `Runner.Stdin` lookup; a closed old pipe returns an ignored content-free error and can never target the replacement child.

The adapter uses the same daemon-singleton `permbridge.Registry`, approval window, and surface function as the MCP control-server route. `runSupervisor` moves registry/window construction before runner selection and supplies a late-bound surfacer holder. The holder is populated after `startRelay` and before `pool.Run`; goroutine creation at `pool.Run` publishes the one assignment to every parser/approval goroutine. With relay disabled, the holder returns a no-op retire and the registry's existing timeout denies.

### Data flow

```text
Claude child stdout
  -> Parser can_use_tool handler
  -> capture this child's stdin + Registry.Register(tool_use_id)
  -> goroutine: streamApprovalBridge.Surface
       -> modal_shown or question_shown
       -> existing device answer gate / disconnect liveness
       -> Registry.Resolve
  -> Pending.Await
  -> WriteCanUseToolAllow/Deny(captured stdin, Claude request_id)
  -> retire modal/question correlation
```

The MCP route remains in parallel as the rollback transport and consumes the same registry/surfacer without code changes.

## Concurrency model

- The parser's stdout-forwarding goroutine performs only writer capture, `Registry.Register`, and a bounded local-map insertion before launching the waiter; it never waits for a client and never calls the broadcaster.
- One goroutine exists per accepted stdio ask. It exits when the existing registry one-shot resolves from an answer, loss of answerers, timeout, daemon shutdown, or the originating child's exit callback.
- The per-runner tracker has one leaf mutex around map insert/delete/snapshot. It is never held while calling `Registry.Resolve`, the surfacer, `Pending.Await`, a response writer, or the existing sink exit callback.
- `OnChildExit` snapshots under the tracker lock, releases it, then resolves each registry ID. The registry one-shot arbitrates races with a client answer or timeout; only the waiter writes a response.
- The captured `io.Writer` is the specific `*os.File` handle published for the origin child. Teardown may close it before the waiter writes, producing a normal write error, but the handle is never re-resolved to a replacement child.

Shutdown order is unchanged: child exit resolves tracked asks, their waiters retire surfaces, `pool.Run` joins its runners, and daemon cancellation also makes the existing relay liveness report fail closed.

## Error handling

- Empty or duplicate `tool_use_id`: fixed deny, never surface and never allow.
- No live originating stdin at callback time: registration is not allowed to create an unanswerable parked entry; issue one best-effort fixed deny to the captured nil target and return.
- Surface unavailable: use a no-op retire; the existing registry timeout remains the fail-closed bound.
- Modal/question recording or push failure: inherit `streamApprovalBridge.Surface`'s fail-closed behavior and content-free logging.
- Child exit: resolve the registry entry to a fixed deny; a closed captured writer error is ignored or logged only as a content-free discriminant.
- Invalid allow `UpdatedInput` or pipe write failure: write nothing further. Do not retry, because a second response could duplicate a partially observed answer and a retry through current stdin could target another child.
- Unknown or late client answer: inherit the existing modal/question and permbridge one-shots; it is a safe no-op.

No log or returned error may include Claude-authored tool input, tool name, request IDs, question text/options, decision reason, blocked path, or a client-supplied deny message.

## Testing strategy

RED is established before production edits with focused tests for:

- config missing/false/true overlay behavior and the default-off zero value;
- byte-exact `withApprovalArgs` results for toggle off, toggle on, stored modes, YOLO, and operator bypass; existing `permissionArgs` and `buildStreamRunnerClaudeArgs` assertions remain unchanged;
- the adapter's allow, deny, question updated-input, no-answer timeout, duplicate/invalid request, and child-exit race behavior using in-memory writers and the real `permbridge.Registry`/`streamApprovalBridge` surface;
- factory wiring that installs the handler only when the toggle is on and preserves the existing child-exit sink callback.

The fake-daemon e2e uses the existing fakeclaude stdio-permission rider and a paired interactive phone. It proves modal allow and deny each generate exactly one correlated response; `AskUserQuestion` surfaces as `question_shown` and returns the current updated-input shape; disconnect/no eligible client denies; and child exit followed by a late answer neither reaches a replacement child nor duplicates a response. The existing MCP-rider test remains unchanged as the toggle-off regression gate.

The live suite adds toggle-on variants of the existing real interactive permission harness for allow and deny. They retain the existing non-vacuity, remote-dismissal attribution, terminal-idle, and deny-side filesystem witness checks. The current toggle-off live allow/deny tests remain unchanged and continue to exercise the MCP rollback route. Per role policy, the builder does not execute live Claude tests; the dispatcher runs the `needs-real-claude` gate.

Touched-scope verification:

- `go test -race ./internal/config/... ./internal/streamsup/... ./internal/permbridge/... ./cmd/pyry/...`
- `go test -race -tags e2e ./internal/e2e/...`
- `go vet ./...`
- `go build ./cmd/pyry`

## Open questions

None. The transport contract and response shape are fixed by #2342, and this slice deliberately defers richer stdio fields.

## Documentation handoff

Pending for the documentation stage: update `docs/knowledge/features/config-package.md` to document `stdio_permission_prompt`, including its default-off behavior, daemon-restart requirement, `false` as the MCP rollback value, unchanged YOLO and `pyry agent-run` behavior, and the Claude Code 2.1.259 live compatibility result recorded by #2342.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `decodeCanUseTool` remains the single subprocess-stdout decode boundary; the adapter deliberately narrows the decoded request to the three existing `permbridge.Request` fields and does not interpret or forward the richer untrusted fields.
- [Tokens, secrets, credentials] No findings — no token, credential, nonce, or durable secret is created or changed. Existing daemon- and client-minted modal/question tokens retain their current lifecycle and authorization gates.
- [File operations] No findings — the only persisted change is decoding an additive boolean from the existing config file. The MCP config file creation, permissions, cleanup, and path remain unchanged even when stdio is selected.
- [Subprocess execution] No findings — the toggle selects one fixed argv literal, `stdio`; no untrusted request field reaches `exec.Command`, a shell, argv, or the child environment. YOLO and operator-supplied argv retain their existing branches.
- [Cryptographic primitives] No findings — this change adds no cryptography or randomness and reuses the existing modal/question registries for security-relevant IDs.
- [Network & I/O] No findings — no new socket reader or network frame is introduced. Child output remains bounded and decoded by the existing `Parser`; responses use structured single-line encoders whose raw JSON validation prevents line injection.
- [Error messages, logs, telemetry] SHOULD FIX — adapter diagnostics must be content-free. Phase B must ensure no log/error carries tool input, tool name, request IDs, question text/options, blocked paths, decision fields, or deny prose.
- [Concurrency] No findings — registration precedes goroutine launch, a leaf mutex protects only the local pending map, registry resolution happens outside it, the one-shot chooses the winner, and every waiter exits on answer/unanswerable timeout/shutdown/origin-child exit. Capturing the origin writer before launch prevents respawn misrouting.
- [Threat model alignment] No findings — existing device authorization, encrypted relay transport, answer token/idempotency, modal/question one-shots, disconnect liveness, and default-deny registry policy are reused unchanged. Rich permission suggestions and always-allow semantics remain explicitly out of scope for later slices under #2286.

**Reviewer:** builder (self-review per the security-review checklist)  
**Date:** 2026-09-11
