# 2266 — publish a refused turn with no fallback on the mobile wire

## Files read

- `internal/turnevent/event.go` → `ModelRefusalNoFallback` — the bounded daemon
  event, its four publishable values, excluded explanation and identity fields,
  and producer report-token vocabulary.
- `internal/streamsup/parser.go` → `emitModelRefusalNoFallback` — the single cap
  site and ordered daemon report tokens: two dropped identifiers and two
  truncated prose fields.
- `internal/protocol/codes.go` → `TypeModelRefusalFallback` — the outbound-only
  v2 naming and classification precedent this sibling frame follows.
- `internal/protocol/interactive.go` → `ModelRefusalFallbackPayload` — the
  conversation scope, untrusted-text contract, explicit fields, and report-array
  null semantics shared by the new payload.
- `internal/protocol/interactive_test.go` →
  `TestModelRefusalFallbackPayload_RoundTrip` and
  `TestModelRefusalFallbackPayload_EmptyReports_RoundTrip` — fixture-backed exact
  key, explicit-null, and marshal-after-decode proofs.
- `internal/protocol/compat_test.go` → `v2OnlyTypes` and
  `TestTypeConstants_V1V2Partition` — the exhaustive outbound-only type guards.
- `internal/turnbridge/outbound.go` → `MapEvent` and
  `modelRefusalFallbackReportKeys` — the event-to-wire mapping seam and the
  filtered, copied report translation pattern.
- `internal/turnbridge/outbound_test.go` →
  `TestMapEvent_ModelRefusalFallbackCarriesNothingElseAndDoesNotMutate` — the
  exact-key, exclusion, ordering, nil, mutation, and aliasing proof to mirror.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.Handle`,
  `emit`, and `eventKind` — ordered delta flushing, the single interactive
  capability gate, lifecycle state, and content-free event logging.
- `cmd/pyry/interactive_turn_v2_test.go` →
  `TestInteractiveTurnEmitterV2_ModelRefusalFallbackFansOutToInteractiveOnly`
  and `TestInteractiveTurnEmitterV2_ModelRefusalFallbackEventKindIsContentFree`
  — the integration proof for ordering, fan-out, lifecycle, and log safety.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes` — the exhaustive inbound or
  push classification required for each application type constant.
- `docs/knowledge/features/protocol-package-interactive-event-payloads.md` —
  interactive frames are outbound-only, explicit fields do not use `omitempty`,
  and claude-authored prose remains untrusted through the client render boundary.
- `docs/knowledge/features/turnbridge-package.md` → `MapEvent` — bridge
  transformations must not mutate daemon events or leak unpublished report names.
- `docs/knowledge/features/development-verification.md` → protocol boundaries —
  raw JSON inspection distinguishes absent keys, `null`, and empty arrays, while
  every new event variant needs separate mapping, handling, and logging proofs.
- `docs/specs/architecture/2265-model-refusal-fallback-wire.md` — the nearest
  shipped end-to-end analogue across the same four production files.

## Context

`emitModelRefusalNoFallback` now maps claude's
`system/model_refusal_no_fallback` line into a distinct, bounded
`turnevent.ModelRefusalNoFallback`, but `MapEvent` and the interactive emitter do
not publish it. A remote client therefore sees a turn produce no content without
the explanation that claude refused it and performed no retry.

This ticket publishes that fact as its own conversation-scoped frame. It does
not invent request or message identity, forward a second refusal explanation,
retry the turn, or change the current-model authority retained by
`ModelAnnouncedPayload`. No ADR is warranted: the work applies the shipped
`model_refusal_fallback` contract at the existing protocol and emitter seams.

## Design

### Wire vocabulary

`internal/protocol/codes.go` adds the outbound-only v2 constant:

```go
const TypeModelRefusalNoFallback = "model_refusal_no_fallback"
```

It remains outside `inboundAppTypeSet`. `v2OnlyTypes`,
`TestTypeConstants_V1V2Partition`, and `excludedTypes` classify it as an
outbound push so an authenticated phone cannot send this daemon-report frame
inbound and neither exhaustive guard silently defaults it.

`internal/protocol/interactive.go` adds `ModelRefusalNoFallbackPayload` with
this contract and declaration order:

| Go field | JSON key | Source |
|---|---|---|
| `ConversationID` | `conversation_id` | bridge context |
| `OriginalModel` | `original_model` | event, verbatim |
| `RefusalCategory` | `refusal_category` | event, open string, verbatim |
| `Banner` | `banner` | event, claude-authored prose |
| `TruncatedFields` | `truncated_fields` | filtered event report |
| `DroppedFields` | `dropped_fields` | filtered event report |

No field uses `omitempty`, and the type has no `MarshalJSON`. Nil report slices
therefore encode as JSON `null`; populated slices retain daemon order. The
closed struct is also the exact-key allowlist: it has no `turn_id`, `request_id`,
refused message UUID, `api_refusal_explanation`, or `refusal_explanation`.

All event values are claude-authored and already bounded by the producer, but
not sanitized. `OriginalModel` is an opaque identifier, and
`RefusalCategory` is an open classification string that remains claude's
assertion rather than a daemon finding or actuator. `Banner` may echo refused
user text; clients must render it as inert, claude-attributed text and must not
feed it to an HTML sink, URL, command, or shell. Receiving the frame neither
retries nor changes the authoritative current model.

### Event mapping and report translation

`MapEvent` adds a `turnevent.ModelRefusalNoFallback` arm returning
`TypeModelRefusalNoFallback` and the new payload. It injects only
`tc.ConversationID`; `tc.TurnID` and `tc.Seq` have no honest destination because
the event carries no message identity that can join the assistant-delta stream.
The three published event values cross byte-for-byte without a second cap, and
`RefusalExplanation` is deliberately excluded because `Banner` is the sole
refusal prose clients render.

A `modelRefusalNoFallbackReportKeys` helper filters and copies both event report
slices. It preserves input order and only admits wire keys the frame publishes:
`original_model`, `refusal_category`, and `banner`. The excluded
`refusal_explanation` token and unknown tokens are dropped. Nil or excluded-only
input returns nil, preserving `null` on the wire. Any retained slice has a fresh
backing array so downstream mutation cannot change the daemon event.

### Interactive emission

`interactiveTurnEmitterV2.Handle` adds a separate
`ModelRefusalNoFallback` arm. It calls `flushDelta` before `emitMapped`, retaining
any buffered assistant text ahead of the refusal frame. It does not call
`startTurnIfNeeded`, `transitionTo`, or `endTurn`; the frame explains why output
stopped without being a lifecycle edge, and it may arrive when no turn is open.

`eventKind` returns only the constant variant name
`"model_refusal_no_fallback"`. It never includes model, category, banner, or
report values. The existing `emit` method remains the single capability gate and
sends the frame only to connections whose echoed capabilities contain
`interactive`.

```text
ModelRefusalNoFallback
        |
        v
MapEvent -- filter/copy reports --> ModelRefusalNoFallbackPayload
        |
        v
Handle -- flush pending delta --> emit --> interactive connections only
```

## Concurrency model

No goroutine, lock, channel, or shared mutable state is added. `MapEvent` and the
report helper are synchronous value transformations. `Handle` stays on the
existing single producer path and uses the existing ordered `flushDelta` and
`emitMapped` calls. Copying retained report tokens prevents slice aliasing across
downstream observers.

## Error handling

The mapping adds no parse, I/O, or recoverable error. It maps even a zero-value
event. Existing marshal, sealing, and push errors remain owned by `emit`. The
relevant failure modes are silent omission, wrong constant classification, and
payload disclosure; exhaustive guards plus discriminating mapper, emitter, and
logging tests make those failures deterministic.

## Testing strategy

RED is established by adding tests and fixtures before production declarations
or switch arms. The initial failures will be undefined
`TypeModelRefusalNoFallback` / `ModelRefusalNoFallbackPayload` and absent mapping
or emission.

- `internal/protocol/interactive_test.go` and two fixtures prove the populated
  exact six-key shape and the nil-report shape. The latter inspects raw JSON for
  literal `null` report values before decoding, then marshals the decoded payload
  back through the envelope.
- `internal/protocol/compat_test.go` proves the constant is v2-only and included
  in the complete type partition.
- `internal/turnbridge/outbound_test.go` adds a table row and a focused test for
  exact JSON keys, exclusion of `refusal_explanation`, report filtering and
  ordering, nil after excluded-only input, no event mutation, and no slice
  aliasing.
- `cmd/pyry/interactive_turn_v2_test.go` passes the event through a live emitter
  with interactive and legacy connections. It asserts pending-delta ordering,
  exact payload values, one interactive-only refusal frame, and unchanged
  lifecycle state. A separate no-cursor logging test uses conspicuous sentinels
  to prove `eventKind` names the variant without leaking content.
- `cmd/pyry/relay_guard_test.go` receives the push classification.
- Verification runs race-enabled tests for `internal/protocol`,
  `internal/turnbridge`, and `cmd/pyry`, followed by `go vet ./...` and
  `go build ./cmd/pyry`.

## Open questions

None. The ticket and `ModelRefusalNoFallback` fix the field set, report
vocabulary, lifecycle posture, and security boundary; the shipped fallback
sibling fixes the implementation pattern.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md` under
“Interactive events (v2, capability-gated)” to document the
`model_refusal_no_fallback` frame, its six fields, report-array filtering/order
and `null` semantics, and how clients distinguish it from
`model_refusal_fallback`. Update the section's spelled-out interactive-envelope
count. Record the deliberately excluded request ID, refused-message UUID, and
`api_refusal_explanation`, and state that `banner` is the sole rendered refusal
prose while the frame neither retries nor changes authoritative model state.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] SHOULD FIX — `ModelRefusalNoFallback` crosses from
  claude's subprocess output into a client-facing frame. The payload contract
  must state that all event values are bounded but unsanitized claude-authored
  data, and that category is an assertion rather than a daemon finding.
- [Tokens, secrets, credentials] No findings — the design creates, stores, and
  accepts no credential. Request and message identifiers are deliberately
  excluded, and a model identifier is inert report data rather than a token.
- [File operations] No findings — no file path, filesystem read, or persistence
  format is introduced.
- [Subprocess execution] No findings — the design maps an already-constructed
  event and never passes its strings to a subprocess or shell.
- [Cryptographic primitives] No findings — no key, nonce, hash, random value,
  or secret comparison is introduced; the existing v2 sealing path is unchanged.
- [Network & I/O] No findings — no new reader or allocation boundary is added.
  `emitModelRefusalNoFallback` bounds every source field before mapping, and the
  design preserves that single cap site.
- [Error messages, logs, telemetry] SHOULD FIX — `eventKind` must return only
  the fixed variant name. Original model, category, banner, and report values
  must never enter logs because the banner may repeat refused user content.
- [Concurrency] No findings — the design adds no goroutine, lock, or shared
  mutable state, and fresh report slices prevent cross-observer mutation.
- [Threat model alignment] SHOULD FIX — a fabricated or misleading
  claude-authored category or banner can impersonate daemon judgment. The type
  must stay outbound-only, the values must never actuate daemon behaviour, and
  clients must treat banner as inert claude-attributed text. Existing encrypted,
  authenticated relay transport is unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-11
