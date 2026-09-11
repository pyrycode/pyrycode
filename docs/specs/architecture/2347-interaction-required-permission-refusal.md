# #2347 — Refuse remote answers for interaction-required permission asks

## Files read

- `internal/streamsup/parser.go` → `CanUseToolRequest`, `decodeCanUseTool` — owns the subprocess-stdout trust boundary and already decodes `requires_user_interaction` as a Claude-authored boolean.
- `cmd/pyry/streamsup_runner.go` → `stdioPermissionHandler.handle`, `stdioPermissionHandler.await` — maps the decoded stdio ask into the shared parked request and writes the eventual correlated `control_response`.
- `internal/permbridge/permbridge.go` → `Request`, `Registry.Register`, `Registry.Lookup`, `Registry.Resolve`, `Registry.expire` — carries immutable approval data and owns the fail-closed one-shot deadline.
- `cmd/pyry/modal_resolve_v2.go` → `modalResolverV2.ResolveAnswer`, `streamApprovalBridge.Surface`, `ResolveStream`, `ApprovalAnswerable`, `retire`, `surfaceQuestion` — contains the pre-consume remote-answer gate, modal/question correlations, liveness report, and timeout dismissal path.
- `cmd/pyry/stdio_permission_test.go` → `TestStdioPermissionHandler_AllowAndDeny`, `TestStdioPermissionHandler_AskUserQuestionUsesExistingSurface`, `TestStdioPermissionHandler_UnansweredDenies` — focused stdio response, question exception, and timeout seams.
- `cmd/pyry/stream_approval_test.go` → `TestModalResolverV2_Answer_StreamGateDeniesBeforePermbridge`, `TestStreamApprovalBridge_ApprovalAnswerable_NegativeOnlyWhenNobodyIsConnected`, `TestStreamApprovalBridge_ApprovalAnswerable_CoversAParkedQuestion` — existing proof patterns for reject-before-consume ordering and the shared permission/question liveness report.
- `internal/control/server.go` → `Server.handleApprove` — confirms the toggle-off approval MCP producer constructs the same request with zero-valued stdio-only eligibility data.
- `cmd/pyry/relay.go` → `startRelayV2` — installs `streamApprovalBridge.ApprovalAnswerable` into the registry and the same bridge into `modalResolverV2`.
- `docs/specs/architecture/2343-stdio-permission-prompt.md` → “Stdio approval adapter” and `## Revisions` — establishes origin-writer ownership, timeout authority, and the separate question surface reused here.
- `docs/specs/architecture/2346-permission-ask-context.md` → `Design`, `Security review` — distinguishes Claude-authored stdio ask fields from daemon authorization and logging data.
- `docs/knowledge/features/permbridge-package.md` → “Conditional bound”, “Concurrency model”, “Fail-closed / default-deny” — requires an honest liveness report and preserves one-shot deadline resolution.
- `docs/knowledge/features/development-verification.md` → “Establish the change surface”, “Prove that tests distinguish the change”, “Protocol boundaries” — requires tracing all request builders and proving each side of a combined eligibility predicate independently.
- `CODING-STYLE.md` → “Concurrency”, “Testing”, “Comments — Citing Other Code” — defines leaf-lock, focused race-test, and symbol-citation conventions.

## Context

The stdio permission transport currently copies a decoded `can_use_tool` ask into `permbridge.Request` without its `RequiresUserInteraction` bit. The shared surface consequently treats every permission modal as remotely answerable: `modalResolverV2.ResolveAnswer` can consume it, and `streamApprovalBridge.ApprovalAnswerable` can repeatedly extend its registry deadline while an interactive client is connected.

The flag is a negative eligibility fact, not display context. This change retains it only inside the daemon and makes it authoritative at both decision points. An interaction-required permission remains visible until the existing registry deadline denies Claude and the existing retire closure dismisses the modal. The separate `AskUserQuestion` correlation and answer path deliberately ignore the flag.

No ADR is warranted: this narrows an established permission authorization gate using source eligibility already present in the decoded request.

Size re-check: 3 production files, 0 new exported types or interfaces, 0 consumer call sites requiring simultaneous edits, 3 acceptance criteria, and 1 new reject branch. Expected written work is about 390 lines including this plan, focused tests, helpers, and comments. The ticket has one independently checkable deliverable: interaction-required permission asks cannot be remotely answered and therefore expire through the existing fail-closed path. Every one-ticket boundary holds.

## Design

### Eligibility propagation

Add `RequiresUserInteraction bool` to `permbridge.Request` with `json:"-"`. `stdioPermissionHandler.handle` copies it directly from `streamsup.CanUseToolRequest`; it is not derived from tool input. The approval MCP producer leaves the field false, preserving its existing behavior. Excluding it from JSON makes the field explicitly daemon-internal eligibility data rather than a new display or wire field.

Replace `streamApprovalBridge.byModal`'s string value with an unexported correlation value containing the tool-use ID and the immutable eligibility bit captured by `Surface`. The question path continues to use `byQuestion map[string]string`; therefore even an `AskUserQuestion` request whose source bit is true remains remotely answerable through `question_answer`.

### Remote answer gate

Extend the cmd-local `streamApprovalResolver` seam with a read-only eligibility method. Its production implementation returns false only when the supplied ID names a surfaced stream permission whose stored correlation requires user interaction; non-stream/TUI modal IDs return true.

`modalResolverV2.ResolveAnswer` invokes this method after the existing paired-device permission gate but before option classification and, critically, before `modalbridge.Registry.Resolve`. A false result returns an empty dismissal and `false` without consuming the modal, resolving the parked approval, routing a keystroke, auditing an answer, or logging Claude-authored data. Both allow and deny option IDs take the same branch.

### Deadline liveness

Replace the generic permission scan inside `ApprovalAnswerable` with a correlation lookup that distinguishes the two surfaces:

- a matching permission correlation is answerable only when its stored eligibility bit is false;
- a matching question correlation is answerable regardless of that bit;
- an unknown or retired ID remains unanswerable.

The eligibility lookup runs under the existing leaf `streamApprovalBridge.mu` and releases it before `ActiveConns`. An interaction-required permission therefore returns false without a relay-manager round trip even when an interactive client is connected. On the next registry expiry, `Registry.expire` resolves exactly one deny; the stdio waiter writes one deny `control_response`, then invokes the unchanged retire closure to delete the correlation, consume the modal, and broadcast one timeout dismissal.

Data flow:

```text
can_use_tool.requires_user_interaction
  -> permbridge.Request (internal eligibility)
  -> Surface permission correlation
       -> ResolveAnswer pre-consume gate
       -> ApprovalAnswerable deadline report
  -> existing Registry expiry deny
  -> one control_response + existing modal retirement
```

## Concurrency model

No goroutine or lock is added. The eligibility bit is immutable after the parser callback constructs the request. `Surface` copies it into `byModal` under the existing leaf mutex. Eligibility readers take only that mutex for bounded map scans and release it before registry or broadcaster calls, preserving the current no-lock-nesting rule.

The existing per-ask stdio waiter remains the sole response writer and exits when the registry one-shot resolves. `Registry.expire` remains the sole timeout authority; a false liveness report does not itself resolve or retire anything. Races among deadline, child exit, and a remote answer remain settled by the existing registry and modal one-shots. Because the answer gate reads the correlation before modal consume, a pre-deadline remote answer cannot win either one-shot for an interaction-required ask.

## Error handling

- Interaction-required stream permission answer: return unhandled with no state change and no new log or audit record; the existing timeout remains responsible for denial and cleanup.
- Ordinary stream permission, approval MCP request, or non-stream/TUI modal: eligibility reads true and the current allow/deny route is unchanged.
- Question batch: absent from `byModal`, handled only through `byQuestion` and `question_answer`, unchanged even if its source ask carries the flag.
- Unknown, retired, or never-surfaced IDs: retain the existing safe negative/no-op behavior.
- Surface or push failure: no correlation exists, so the registry deadline denies exactly as before.

No error or log attribute will contain the flag, tool name, input, request ID, decision reason, blocked path, or description.

## Testing strategy

Establish RED in `cmd/pyry` before production edits with a table-driven integration test over allow and deny option IDs. Each row uses the real stdio handler, permission registry, modal registry, bridge, and resolver with an interactive connection. It waits for the modal to surface, attempts the remote answer, and proves before the deadline that no response was written and both registries/correlation remain outstanding. It also proves `ApprovalAnswerable` is false, then waits for exactly one deny `control_response`, no allow response, one timeout dismissal, and complete retirement.

Extend the stdio source-field mapping assertion to pin direct propagation of `RequiresUserInteraction`. Set the flag true in the existing `AskUserQuestion` stdio test so its successful `question_answer` round trip proves the explicit exception. Existing ordinary allow/deny tests continue to pin false/absent behavior; existing stream round trips and control-server tests pin toggle-off MCP and approval MCP behavior without modification.

Touched-scope verification:

- `go test -race ./cmd/pyry/... ./internal/permbridge/...`
- `go vet ./...`
- `go build ./cmd/pyry`

## Open questions

None. The source field, permission-only scope, consume ordering, and existing timeout authority are fixed by the ticket and current bridge contracts.

## Documentation handoff

Pending for the documentation stage: update `docs/knowledge/features/permbridge-package.md`, sections “Domain types”, “Conditional bound”, and “Fail-closed / default-deny”, to record `RequiresUserInteraction` as daemon-internal eligibility, the pre-consume remote-answer refusal, the negative liveness result, and the unchanged `AskUserQuestion` exception. No mobile protocol field is added.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `decodeCanUseTool` remains the sole subprocess-stdout decode boundary. `stdioPermissionHandler.handle` copies the typed boolean without interpreting tool input, and `streamApprovalBridge.Surface` turns it only into a negative eligibility fact. It never becomes device authorization, display content, or a routing key.
- [Tokens, secrets, credentials] No findings — no token, credential, nonce, or secret lifecycle changes. Existing daemon-minted modal IDs, paired-device authorization, and answer-token handling remain intact.
- [File operations] Not applicable — no path is read, written, joined, or authorized. The Claude-authored blocked-path display field remains inert and is not consulted for eligibility.
- [Subprocess / external command execution] No findings — the boolean originates in subprocess output but never reaches argv, environment, a shell, or command construction. Refusal can only prevent a verdict; it cannot cause tool execution.
- [Cryptographic primitives] Not applicable — the change adds no randomness, key material, comparison, or cryptographic operation and leaves modal ID minting unchanged.
- [Network & I/O] No findings — no reader, connection, frame, or size limit changes. The existing authenticated relay path supplies the attempted answer, while the new gate rejects before any modal or approval one-shot is consumed. The stdio response remains encoded by the existing structured writer.
- [Error messages, logs, telemetry] No findings — the refusal adds no log or audit call and returns no Claude-authored value. Focused tests retain distinct sentinels for source data and ensure the flag is never introduced as an attribute.
- [Concurrency] No findings — the immutable bit is stored and read under the existing leaf bridge mutex, which is released before broadcaster or registry calls. Existing registry and modal delete-under-lock one-shots arbitrate terminal races; no goroutine or shutdown path is added.
- [Threat model alignment] No findings — a paired remote device loses, rather than gains, authority for a class of Claude-declared asks. Replay, forged option, unauthenticated-device, and timeout behavior continue through the existing gates. Local tool-specific interaction itself is outside this daemon permission-modal transport; the ticket requires fail-closed timeout rather than adding a new remote interaction protocol.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-11
