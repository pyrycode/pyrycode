# #2346 — Carry permission ask context onto `modal_shown`

## Files read

- `internal/streamsup/parser.go` → `CanUseToolRequest`, `decodeCanUseTool` — defines the Claude-authored ask fields and preserves `decision_reason` as arbitrary JSON.
- `cmd/pyry/streamsup_runner.go` → `stdioPermissionHandler.handle`, `stdioPermissionHandler.await` — converts a stdio ask into the parked `permbridge.Request` whose timeout authority must remain unchanged.
- `internal/permbridge/permbridge.go` → `Request`, `Registry.Register`, `Pending.Await` — owns the parked request and fail-closed verdict timer.
- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge.Surface`, `streamApprovalBridge.broadcast` — turns a parked request into the initial client broadcast and already enforces content-free logging.
- `internal/modalbridge/modal.go` → `Registry.Record`, `Registry.Snapshot`, `Outstanding`, `buildPayload` — stamps modal identity and supplies reconnect reconciliation from stored current truth.
- `internal/relay/v2session_modal.go` → `reconcileModals` — sends `Registry.Snapshot` payloads to a newly opened interactive connection without re-arming timeout state.
- `internal/protocol/messaging.go` → `ModalShownPayload` — declares the outbound v2 wire shape.
- `internal/protocol/messaging_test.go` → `TestModalShownPayload_RoundTrip` and `internal/protocol/testdata/modal_shown.json` — pin the populated JSON shape and canonical round trip.
- `internal/protocol/compat_test.go` → `TestIsKnownAppType` — already pins `modal_shown` as v2-only and must remain unchanged.
- `cmd/pyry/stdio_permission_test.go` → `TestStdioPermissionHandler_AllowAndDeny`, `TestStdioPermissionHandler_UnansweredDenies` — focused seams for source-field mapping and unchanged timeout verdict.
- `cmd/pyry/stream_approval_test.go` → `TestStreamApprovalBridge_Surface_BroadcastsPermissionModal`, `TestStreamApproval_NoBodyLeakInLogs` — initial broadcast, registry storage, MCP-compatible zero-context shape, and logging discipline.
- `internal/e2e/relay_v2_stream_modal_test.go` → `TestRelayV2_StreamModalPermissionRoundTrip` — fake-daemon proof for stdio modal delivery and answer/timeout behavior.
- `internal/e2e/realclaude/harness_modal_test.go` → `raiseRealPermissionModal` and `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` → `TestInteractiveStreamStdioModalResolution` — live stdio permission surface and its non-vacuity gate.
- `docs/knowledge/features/permbridge-package.md` — the registry's leaf-lock and single-timeout-authority constraints.
- `docs/knowledge/features/protocol-package-types-modal-v2-wire-payloads.md` — existing modal wire provenance and compatibility contract.
- `docs/knowledge/features/development-verification.md` and `docs/knowledge/features/e2e-realclaude.md` — fixture, compatibility, and dispatcher-owned live-test evidence rules.

## Context

The stdio permission decoder already retains five display hints from Claude, but `stdioPermissionHandler.handle` currently narrows the ask to tool name, input, and tool-use ID. The permission bridge therefore surfaces only the tool name, and reconnect reconciliation cannot recover context that was never stored.

This change carries those values through existing in-memory state and onto the v2 payload. It does not change what can be approved, the modal nonce or authorization model, the answer mapping, or the registry timer. The approval MCP producer constructs the same zero-context `permbridge.Request` as before and continues through `Registry.Record`, so its serialized payload stays byte-compatible with commit `02baeeab`.

## Design

### Wire and parked request

Extend `protocol.ModalShownPayload` with five trailing optional fields:

- `Reason json.RawMessage` under `reason`, preserving the corresponding arbitrary JSON value without interpreting or re-encoding it.
- `ReasonType`, `BlockedPath`, and `Description` as `omitempty` strings.
- `DefaultToNo` as an `omitempty` boolean and display-selection hint only.

Extend `permbridge.Request` with fields named after the Claude ask (`DecisionReason`, `DecisionReasonType`, `BlockedPath`, `Description`, `DefaultToNo`). `stdioPermissionHandler.handle` copies each field directly from the same-named `streamsup.CanUseToolRequest` field. It never examines `Input` for these values. Existing MCP constructions leave the additions at their zero values.

### Additive modal registry path

Add one exported value type, `modalbridge.PermissionContext`, containing the five optional values, and an additive method:

```go
func (r *Registry) RecordWithContext(
    req turnevent.PermissionRequest,
    wireClass string,
    convID string,
    context PermissionContext,
) (protocol.ModalShownPayload, error)
```

`Registry.Record` remains source-compatible and delegates to the shared internal record path with zero context. `RecordWithContext` builds the same base payload, maps each context field to its corresponding protocol field, then stamps the daemon-minted modal and conversation IDs. Both the returned initial payload and the stored `Outstanding` receive the identical optional context in the same registry operation. `Snapshot` reconstructs those fields from `Outstanding`, preserving `Reason` bytes with a clone so callers cannot mutate registry state.

`streamApprovalBridge.Surface` calls `RecordWithContext` only on its permission-modal arm, constructing `PermissionContext` directly from the parked request. Its question-batch arm remains separate and unchanged. The bridge continues broadcasting the stamped return value rather than a pre-registry payload.

### Data flow

```text
Claude can_use_tool
  → CanUseToolRequest
  → permbridge.Request (direct field-for-field copy)
  → streamApprovalBridge.Surface
  → Registry.RecordWithContext
       ├─ returned ModalShownPayload → initial broadcast
       └─ Outstanding → Registry.Snapshot → reconnect reconcile
```

The approval MCP path enters at `permbridge.Request` with all context fields zero and uses the unchanged zero-context behavior.

## Concurrency model

No goroutine or synchronization primitive is added. The five fields are immutable values copied before registration. `permbridge.Registry` continues to protect pending entries and own the sole verdict timer. `modalbridge.Registry.RecordWithContext` and `Snapshot` use the registry's existing leaf mutex; `Reason` is cloned on storage and snapshot to prevent aliasing across goroutines. Initial broadcast and reconnect reconciliation retain their existing goroutine ownership and push queues.

## Error handling

- A malformed `can_use_tool` request still fails at the existing decoder boundary; no new parsing occurs downstream.
- A modal-ID RNG failure follows the existing fail-closed path: no modal or correlation is published, and the permission request reaches the unchanged deny timeout.
- Zero strings, false, and a nil/empty raw reason serialize without the new keys. Unknown non-empty `reason_type` strings pass through verbatim.
- Context values do not enter errors or logs. Existing bridge warnings remain content-free.
- `default_to_no` does not participate in option construction, answer classification, or timer decisions.

## Testing strategy

- Extend `TestModalShownPayload_RoundTrip` and the `modal_shown` fixture to pin all five populated keys, including structured JSON under `reason` and an unknown future `reason_type`.
- Add a protocol serialization case proving all five zero values are omitted and the zero-context payload matches the commit-`02baeeab` shape. Leave the existing v1 compatibility row untouched.
- Add modalbridge coverage proving `RecordWithContext` returns and snapshots identical context, clones `Reason`, and `Record` emits no optional context.
- Add stdio handler coverage proving direct field-for-field mapping from `CanUseToolRequest` into the surfaced parked request, with conflicting lookalikes inside `Input` to prove no derivation occurs.
- Extend the stream approval surface test to compare the initial broadcast with the registry snapshot, covering the reconnect source. Keep an MCP-style zero-context surface assertion byte-compatible.
- Retain the existing short-timeout test as the deterministic proof that `default_to_no` cannot alter the deny verdict.
- Extend the fake stdio round trip with populated source fields and assert the surfaced context without changing its verdict assertions.
- Extend the live stdio modal test's existing non-vacuity gate to require non-empty `ReasonType`. The dispatcher runs this build-tagged suite; the builder does not run it locally.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md`, section “Modal (v2)”, with the five optional `modal_shown` fields; state their Claude-authored provenance, `reason` as open-shape JSON, `reason_type` as an open string vocabulary, and `default_to_no` as a client-selection hint only.

## Open questions

None. The ticket fixes the source mapping, omission rules, open vocabulary, and timeout semantics.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No finding — `decodeCanUseTool` remains the single subprocess-JSON boundary and `CanUseToolRequest` marks every field Claude-authored. The new downstream path only copies those typed fields; it does not promote them to daemon-authored routing or authorization data.
- [Tokens, secrets, credentials] No finding — modal and tool-use correlation identifiers are minted/stored/resolved exactly as before. The new fields never influence nonce generation, lookup, authorization, or expiry.
- [File operations] Not applicable — `blocked_path` is display-only and no new path is opened, joined, normalized, or persisted.
- [Subprocess / external command execution] No finding — the values come from subprocess output but are never passed to command construction or environment variables. In particular, nothing derives context by interpreting raw tool `Input`.
- [Cryptographic primitives] Not applicable — `newModalID` and its `crypto/rand` behavior are unchanged.
- [Network & I/O] No finding — the existing stream parser buffer and encrypted v2 frame path remain the size and transport boundaries. This ticket adds no reader, connection, or timeout and does not make the added values actionable on receipt.
- [Error messages, logs, telemetry] No finding — `streamApprovalBridge.Surface` retains content-free warnings, and tests scan logs for Claude-authored sentinels. None of the five fields is added to structured logging or errors.
- [Concurrency] No finding — immutable copies flow through existing registries; `Reason` is cloned at modal registry write and snapshot boundaries, while existing leaf-lock ordering remains unchanged. No goroutine or shutdown path is added.
- [Threat model alignment] No finding — paired-device permission authorization still depends on the daemon-minted `modal_id` plus the existing device gate. The new data is display context only and cannot select a verdict or extend the timeout.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-11
