# #2364 — Publish bounded always-allow rules

## Files read

- `internal/streamsup/parser.go` → `CanUseToolRequest`, `decodeCanUseTool` — already preserves Claude's raw `permission_suggestions` and suppression flag without logging decode errors.
- `cmd/pyry/streamsup_runner.go` → `stdioPermissionHandler.handle`, `stdioPermissionHandler.await` — the stdio-only boundary that converts a decoded ask into a parked `permbridge.Request` while the approval-MCP path remains zero-valued.
- `internal/permbridge/permbridge.go` → `Request`, `Registry.Register`, `Registry.Lookup` — owns log-free parked permission data and is the bounded-parse home named by the ticket.
- `internal/questionbridge/questionbridge.go` → `Parse` — nearest bounded, all-or-nothing parser: cap raw bytes before decode, validate the complete batch, preserve order, and return no partial value.
- `internal/modalbridge/modal.go` → `PermissionContext`, `Outstanding`, `Registry.RecordWithContext`, `Registry.Snapshot` — stores the initial modal truth that reconnect reconciliation re-emits.
- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge.Surface`, `streamApprovalBridge.broadcast` — maps the parked permission request to the initial `modal_shown` and keeps log attributes content-free.
- `internal/relay/v2session_modal.go` → `reconcileModals` — serializes `Registry.Snapshot` without reconstructing or revalidating a modal.
- `internal/protocol/messaging.go` → `ModalShownPayload` — declares the v2-only outbound modal wire shape.
- `internal/protocol/messaging_test.go` → `TestModalShownPayload_RoundTrip`; `internal/protocol/compat_test.go` → `TestIsKnownAppType` — fixture round-trip and unchanged v2-only classification seams.
- `cmd/pyry/stdio_permission_test.go` → `TestStdioPermissionHandler_CarriesAskContextFromCorrespondingFields` — direct source-field mapping proof.
- `cmd/pyry/stream_approval_test.go` → `TestStreamApprovalBridge_Surface_BroadcastsPermissionModal`, `TestStreamApproval_NoBodyLeakInLogs` — initial/snapshot equality, approval-MCP-compatible zero context, and log non-disclosure.
- `internal/e2e/relay_v2_stream_modal_test.go` → `TestRelayV2_StreamModalPermissionRoundTrip` — fake-daemon stdio and approval-MCP permission paths.
- `docs/knowledge/features/permbridge-package.md` — preserves the package's log-free leaf-store and fail-closed timer contract.
- `docs/knowledge/features/protocol-package-types-modal-v2-wire-payloads.md` — modal provenance, reconnect, and v2 compatibility contract.
- `docs/knowledge/features/development-verification.md` and `docs/knowledge/features/e2e-harness.md` — fixture and fake-daemon evidence rules.
- `docs/specs/architecture/2346-permission-ask-context.md` — nearest modal-context carriage analogue and its immutable-copy/logging constraints.

## Context

Claude's stdio `can_use_tool` ask already reaches the daemon with raw permission suggestions and an explicit suppression flag, but the narrowing into `permbridge.Request` discards both. A phone therefore cannot know what Claude's “always allow” choice would add, and reconnect reconciliation has no offer state to restore. This slice validates and publishes that display-only offer while retaining the exact supported update/rule ordering for #2365. It does not grant rules, change answer mapping, trust a client with rule bytes, or alter the approval-MCP producer.

The contract is additive v2 wire vocabulary and does not require an ADR. The later documentation stage owns the protocol-reference update.

## Design

### Bounded permission-suggestion value

Add three exported values to `internal/permbridge`:

```go
type PermissionRule struct { /* tool name and optional rule content */ }
type PermissionUpdate struct { /* addRules / allow plus ordered rules */ }
type AlwaysAllow struct { /* package-owned validated updates and rendered rules */ }

func ParseAlwaysAllow(raw json.RawMessage, suppressed bool) AlwaysAllow
func (a AlwaysAllow) Offered() bool
func (a AlwaysAllow) Rules() []string
func (a AlwaysAllow) Updates() []PermissionUpdate
```

`ParseAlwaysAllow` first rejects suppression and raw input over 16 KiB, then decodes the array into private JSON targets. It accepts only a non-empty array whose updates are all `addRules`/`allow`, each with at least one rule; every rule has a non-empty string `toolName` and an absent-or-string `ruleContent`. Explicit `null` is not a string. Unknown keys are ignored so a vendor extension does not suppress an otherwise supported batch.

Validation walks the complete decoded batch before constructing `AlwaysAllow`. It rejects totals above 16 rules and any rendered rule above 1024 bytes. Rendering is `toolName` for absent content and `toolName(ruleContent)` for present content, preserving both array orders. Every rejection returns the zero value, so callers cannot observe a prefix or truncated text. `AlwaysAllow` owns unexported slices and exposes clones; downstream code cannot mutate the validated parked value.

`permbridge.Request` gains one `AlwaysAllow` field excluded from its JSON shape. `stdioPermissionHandler.handle` calls the parser exactly once from the corresponding `CanUseToolRequest` fields before registration. Approval-MCP constructions leave the zero value, as do absent, null, empty, suppressed, malformed, unsupported, or out-of-bounds stdio suggestions.

### Stored modal truth and wire payload

Add `protocol.AlwaysAllowPayload` with always-present `offered` and `rules` fields, then add an always-present `always_allow` field to `ModalShownPayload`. `modalbridge.buildPayload` initializes the unavailable form with a non-nil empty rules slice, ensuring every producer — including approval MCP and terminal/TUI modals — emits `{offered:false,rules:[]}`.

Extend `modalbridge.PermissionContext` and `Outstanding` with the validated `permbridge.AlwaysAllow` value. `streamApprovalBridge.Surface` copies it from the parked request into `RecordWithContext`. The shared record path derives the initial protocol payload from `Offered` and `Rules`, while storing the same immutable validated value. `Registry.Snapshot` derives the identical shape from the stored value, so `reconcileModals` remains a pure sender and cannot diverge from the initial broadcast. The outstanding entry retains the normalized supported updates and all rules in source order for #2365; unavailable shapes retain the zero value.

```text
Claude can_use_tool raw suggestions + suppression
  → ParseAlwaysAllow (size, shape, count, rendered-byte bounds)
  → permbridge.Request.AlwaysAllow
  → streamApprovalBridge.Surface
  → modalbridge.RecordWithContext
       ├─ AlwaysAllowPayload → initial modal_shown
       └─ Outstanding.AlwaysAllow → Snapshot → reconnect modal_shown

approval MCP → zero AlwaysAllow → offered:false, rules:[]
```

## Concurrency model

No goroutine, channel, mutex, or shutdown path is added. Parsing runs synchronously before `Registry.Register`; the resulting value is immutable outside `permbridge`. Existing permbridge and modalbridge leaf locks continue to copy values under their current critical sections. Initial broadcast, timeout ownership, and reconnect pushing keep their existing goroutine ownership.

## Error handling

- Parsing returns only a validated value or the unavailable zero value; it returns no content-bearing error and logs nothing.
- Suppression wins before inspecting suggestions. The raw-size gate runs before JSON decode.
- Malformed JSON, wrong scalar types, explicit null rule content, unsupported update behavior/type, empty arrays, missing required strings, rule-count overflow, and rendered-byte overflow all reject the whole batch.
- Modal-ID RNG failure retains the existing behavior: store and publish nothing, then allow the parked permission to fail closed through its unchanged timeout.
- Protocol marshalling remains infallible for the closed string/bool/slice payload. Existing content-free warning attributes remain unchanged.
- Claude-authored suggestion and rendered-rule strings are display data only. They enter no error, log attribute, routing key, answer mapping, command, path operation, or authorization check.

## Testing strategy

- Add a table-driven `permbridge` parser test covering one ordered multi-update success, bare/content rendering, absent/null/empty, suppression, malformed JSON, unsupported type/behavior, empty rules, missing/wrong-typed rule fields, explicit null content, the 16 KiB gate, 16-rule boundary/overflow, and 1024-byte rendered boundary/overflow. Every reject asserts the zero value and no partial rules or updates.
- Extend stdio-handler coverage to prove the raw suggestions and suppression flag are the sole source of the parked validated value; conflicting lookalikes inside tool input remain inert.
- Extend modalbridge and stream-approval coverage to assert identical populated initial and snapshot payloads, ordered retained updates, defensive clones, unavailable zero-context behavior, and suggestion/rendered sentinel absence from logs.
- Extend the canonical populated `modal_shown` fixture with one bare and one content-bearing rendered rule, and add an unavailable fixture. Round-trip both through `ModalShownPayload`; leave `TestIsKnownAppType` unchanged to pin v2-only classification.
- Extend `TestRelayV2_StreamModalPermissionRoundTrip` with offered and suppressed stdio cases and assertions that all approval-MCP cases publish unavailable. The fake rider supplies the source fields; verdict and dismissal assertions remain unchanged.
- Run `go test -race` for `internal/permbridge`, `internal/modalbridge`, `internal/protocol`, `cmd/pyry`, and `internal/e2e`; then `go vet ./...` and `go build ./cmd/pyry`.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md`, section “Modal (v2)” → `modal_shown`, with `always_allow.offered`, `always_allow.rules`, the `toolName` / `toolName(ruleContent)` rendering syntax, the all-or-nothing supported subset and 16 KiB / 16-rule / 1024-byte bounds, identical reconnect behavior, and the Claude-authored/untrusted provenance of suggestions and rendered rules.

## Open questions

None. The ticket fixes the accepted subset, bounds, rendering, ordering, unavailable shape, storage owner, reconnect semantics, and documentation handoff.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No finding — `ParseAlwaysAllow` is the single subprocess-data validation boundary. Its opaque `AlwaysAllow` result exposes clones only, so downstream modal code cannot construct or mutate a partially validated offer.
- [Tokens, secrets, credentials] No finding — modal nonces, tool-use correlations, answer tokens, and device authorization are unchanged. Suggestions never become a correlation or credential.
- [File operations] Not applicable — rule text may resemble a path but no new code opens, joins, normalizes, persists, or otherwise acts on it.
- [Subprocess / external command execution] No finding — data originates in subprocess stdout but reaches only parked memory and an outbound display payload; it is never passed to command arguments, a shell, stdin, or environment variables by this slice.
- [Cryptographic primitives] Not applicable — `newModalID` and its `crypto/rand` nonce mint remain unchanged.
- [Network & I/O] No finding — the design adds a pre-decode 16 KiB bound, a 16-rule bound, and a 1024-byte rendered-rule bound before data reaches the non-droppable control-frame path. It adds no reader, connection, timeout, or frame-size policy.
- [Error messages, logs, telemetry] No finding — `internal/permbridge` remains structurally log-free; the parser returns no error text, and bridge warnings retain fixed event attributes. Tests include Claude-authored sentinels in raw and rendered values and scan the complete log output.
- [Concurrency] No finding — the validated value is immutable outside its package and exposes cloned slices; existing registry leaf locks and timer/broadcast goroutines are unchanged.
- [Threat model alignment] No finding — this is protocol threat 1's untrusted Claude-authored display text, bounded before publication and still required to be rendered as inert text by clients. The frame remains a report, never an authorization input; #2365 separately owns server-authoritative grant behavior.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-11

## Revisions

None.
