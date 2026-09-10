# 2265 — publish the refusal model swap on the mobile wire

## Files read

- `internal/turnevent/event.go` → `ModelRefusalFallback` — the bounded daemon
  event, its documentation-derived provenance, the six source fields, and the
  producer report-token vocabulary.
- `internal/streamsup/parser.go` → `emitModelRefusalFallback` — the single cap
  site and the exact ordered report tokens: four dropped token/model fields and
  two truncated prose fields.
- `internal/protocol/codes.go` → `TypeModelAnnounced`, `TypeToolDenied`,
  `TypeBanner` — outbound v2 type naming and classification precedents.
- `internal/protocol/interactive.go` → `ToolDeniedPayload`,
  `ModelAnnouncedPayload`, `BannerPayload` — report-array null semantics,
  model-authority semantics, and claude-authored text safety rules.
- `internal/protocol/interactive_test.go` → `TestToolDeniedPayload_RoundTrip`,
  `TestToolDeniedPayload_EmptyReasons_RoundTrip`, `roundTripEnvelope` — the
  fixture-backed byte proof for exact keys and nil report slices.
- `internal/protocol/compat_test.go` → `v2OnlyTypes`,
  `TestTypeConstants_V1V2Partition` — the total classification guard for new
  outbound-only v2 constants.
- `internal/turnbridge/outbound.go` → `MapEvent`, `deniedReportKeys` — the pure
  event-to-wire mapping seam and the non-aliasing report translation pattern.
- `internal/turnbridge/outbound_test.go` → `TestMapEvent_ToolDeniedDoesNotMutateTheEvent`,
  `TestMapEvent_ToolDeniedCarriesNothingElse` — the mutation and exact-key
  proofs needed when report contents are filtered.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.Handle`,
  `emitMapped`, `emit`, `eventKind` — ordered emission, the single interactive
  capability gate, and content-free logging.
- `cmd/pyry/interactive_turn_v2_test.go` →
  `TestInteractiveTurnEmitterV2_ModelAnnouncedNoLifecycleMutation`,
  `TestInteractiveTurnEmitterV2_ToolDeniedFansOutToInteractiveOnly` — the
  conversation-scoped lifecycle and capability-fan-out proofs.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes` — outbound push
  classification required for every new `Type*` constant.
- `docs/knowledge/features/protocol-package-interactive-event-payloads.md` —
  declaration order controls fixture key order, nil reports must remain `null`,
  and report tokens must use the payload's wire vocabulary.
- `docs/knowledge/features/turnbridge-package.md` → `MapEvent` and its report
  slice lessons — bridge transforms must copy rather than mutate daemon events.
- `docs/knowledge/features/development-verification.md` → protocol boundaries —
  raw-key inspection is required to distinguish absent, `null`, and `[]`.
- `docs/specs/architecture/2233-publish-tool-denial-marker.md` — the nearest
  end-to-end analogue across the same production surface.

## Context

`emitModelRefusalFallback` now turns claude's
`system/model_refusal_fallback` line into a bounded
`turnevent.ModelRefusalFallback`, but `MapEvent` and the interactive emitter do
not publish it. A client can therefore observe a later `model_announced` with a
different label without learning that claude refused the prior attempt and
retried on a fallback model.

This ticket adds a dedicated, conversation-scoped explanation frame. It does
not add request or message identity the daemon does not possess, and it does
not make the refusal frame authoritative for the active model:
`model_announced` retains that role. No ADR is warranted because the design
applies the established `tool_denied` report-array rules and the
`model_announced` lifecycle posture without introducing a new architectural
boundary.

## Design

### Wire vocabulary

`internal/protocol/codes.go` adds the outbound-only v2 constant:

```go
const TypeModelRefusalFallback = "model_refusal_fallback"
```

It remains outside `inboundAppTypeSet`. `v2OnlyTypes`,
`TestTypeConstants_V1V2Partition`, and `excludedTypes` classify it as an
outbound push so neither protocol guard can silently ignore the new constant.

`internal/protocol/interactive.go` adds `ModelRefusalFallbackPayload` with this
contract and declaration order:

| Go field | JSON key | Source |
|---|---|---|
| `ConversationID` | `conversation_id` | bridge context |
| `OriginalModel` | `original_model` | event, verbatim |
| `FallbackModel` | `fallback_model` | event, verbatim |
| `Scope` | `scope` | event, open string, verbatim |
| `RefusalCategory` | `refusal_category` | event, open string, verbatim |
| `Banner` | `banner` | event, claude-authored prose |
| `TruncatedFields` | `truncated_fields` | filtered event report |
| `DroppedFields` | `dropped_fields` | filtered event report |

No field uses `omitempty`, and the type has no `MarshalJSON`. Nil report slices
therefore remain JSON `null`; populated slices retain order. The closed struct
is also the exact-key allowlist: there is no `turn_id`, `request_id`, refused or
retracted message UUID, or `api_refusal_explanation`/
`refusal_explanation` field.

The payload contract states that models are claude identifiers carried without
normalisation and that `scope` and `refusal_category` are open strings. The
category is claude's assertion about the request, not a daemon finding and
never an actuator. `banner` is bounded but unsanitized claude-authored prose
that may echo user text; clients must render it as inert, claude-attributed
text. The refusal frame explains a swap but never overrides the active-model
authority of `ModelAnnouncedPayload`.

### Event mapping and report translation

`MapEvent` adds a `turnevent.ModelRefusalFallback` arm returning
`TypeModelRefusalFallback` and the payload above. It injects only
`tc.ConversationID`; `tc.TurnID` and `tc.Seq` are deliberately unused because
the event has no request or message identity that can join it to the mobile
delta stream.

The five published event values cross byte-for-byte and are not re-capped. The
producer in `emitModelRefusalFallback` remains the one cap site.
`RefusalExplanation` is deliberately not copied.

A small `modelRefusalFallbackReportKeys` helper translates both report slices
at the same seam. It preserves, in input order, only tokens naming published
event fields: `scope`, `original_model`, `fallback_model`,
`refusal_category`, and `banner`. The excluded `refusal_explanation` token and
any token outside the frame's vocabulary are omitted. Nil input returns nil;
an input containing only excluded tokens also returns nil so the wire emits
`null`, not `[]`. The helper always builds a fresh backing array when it keeps
tokens, ensuring payload mutation cannot change the daemon event.

### Interactive emission

`interactiveTurnEmitterV2.Handle` adds a separate
`ModelRefusalFallback` arm. It flushes any pending assistant delta before the
refusal frame, then calls `emitMapped`. It does not call
`startTurnIfNeeded`, `transitionTo`, or `endTurn`: the event explains a retry
but neither supplies nor changes turn lifecycle identity. This matches the
observable posture of `ModelAnnounced` and allows the frame to arrive even
when no turn is open.

`eventKind` returns the content-free variant name
`"model_refusal_fallback"`. It never includes either model, the category,
scope, banner, or report tokens. `emit` remains the only capability gate, so
one logical event is appended once to history and fanned out once per active
interactive connection while non-interactive connections receive nothing.

```text
ModelRefusalFallback
        |
        v
MapEvent -- filter/copy reports --> ModelRefusalFallbackPayload
        |
        v
Handle -- flush pending delta --> emit --> interactive connections only
```

## Concurrency model

No goroutine, lock, channel, or shared mutable state is added. `MapEvent` and
the report helper are synchronous pure transformations. `Handle` continues to
run on its existing single producer goroutine; the new arm only uses existing
ordered `flushDelta` and `emitMapped` calls. Copying kept report tokens avoids
aliasing the event's slices across downstream observers.

## Error handling

No new recoverable error is introduced. The mapping has no parse or I/O step
and maps even a zero-value event. Existing JSON marshal and push failures remain
owned by `emit`. The material failure modes are silent omission and wrong
classification; the emitter integration test and the two constant-totality
guards make those deterministic failures.

## Testing strategy

RED is established by adding the tests and fixture references before any
production declaration or switch arm. Expected initial failures are undefined
`TypeModelRefusalFallback`/`ModelRefusalFallbackPayload` and absent mapping or
emission.

- `internal/protocol/interactive_test.go` and two fixtures prove the populated
  exact eight-key shape and the zero-report shape. The latter inspects raw JSON
  for both report keys with `null` values before decoding, so absent keys and
  `[]` cannot pass accidentally. Round-trip comparison exercises the struct
  tags rather than comparing untouched raw payload.
- `internal/protocol/compat_test.go` proves the type is v2-only and participates
  in the complete constant partition.
- `internal/turnbridge/outbound_test.go` adds a `TestMapEventOutbound` row plus
  focused tests that assert exact JSON keys, filtering of
  `refusal_explanation`, preservation and order of published tokens, nil after
  an excluded-only report, and no mutation or slice aliasing.
- `cmd/pyry/interactive_turn_v2_test.go` sends a refusal event through a live
  emitter with one interactive and one non-interactive connection. It asserts
  exactly one refusal envelope, bridge-supplied conversation identity, payload
  values, pending-delta order, and no lifecycle mutation. A no-cursor logging
  test proves `eventKind` names the variant without leaking its content.
- `cmd/pyry/relay_guard_test.go` receives the outbound push classification.
- Verification runs `go test -race ./internal/protocol/...`,
  `go test -race ./internal/turnbridge/...`, `go test -race ./cmd/pyry/...`,
  `go vet ./...`, and `go build ./cmd/pyry`.

## Open questions

None. The ticket fixes the field set, report filtering, capability path, and
relationship to `model_announced`; existing symbols settle lifecycle and null
semantics.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md` under
“Interactive events (v2, capability-gated)” with the
`model_refusal_fallback` frame, its eight keys, open-string and claude-authored
provenance rules, both report arrays, and its distinction from
`model_announced`. Update the section's spelled-out envelope-type count. State
that the wire cannot identify or retract the refused partial because it carries
turn/sequence identity rather than claude message UUIDs, and record the
deliberately omitted `request_id` and `api_refusal_explanation` fields.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] SHOULD FIX — `ModelRefusalFallback` crosses from claude's
  subprocess output into a client-facing frame. The payload comment must state
  that every event value is claude-authored, bounded but unsanitized, and that
  `refusal_category` is claude's assertion rather than a daemon finding.
- [Tokens, secrets, credentials] No findings — the frame creates, stores, or
  accepts no credential. The deliberately excluded request and message IDs are
  absent from the payload, and neither model identifier is a secret token.
- [File operations] No findings — no file path is accepted or constructed and
  no persistence format changes.
- [Subprocess execution] No findings — the design publishes an already-bounded
  event and never passes its strings to a subprocess or shell.
- [Cryptographic primitives] No findings — no key, nonce, random value, hash, or
  comparison is introduced. Existing v2 sealing remains below `emit`.
- [Network & I/O] No findings — no new reader, socket, or allocation boundary is
  added. `emitModelRefusalFallback` already bounds all source fields before the
  event reaches the mapper, and this design preserves that single cap site.
- [Error messages, logs, telemetry] SHOULD FIX — `eventKind` must return only
  the constant variant name. Models, scope, category, banner, and reports must
  not enter logs; in particular the banner may echo refused user text.
- [Concurrency] No findings — no goroutine, lock, or check-then-mutate path is
  added; fresh report slices prevent cross-observer mutation.
- [Threat model alignment] SHOULD FIX — an authenticated but misleading
  claude-authored category or banner can impersonate daemon judgment. The wire
  type must be outbound-only, the category must never drive daemon behaviour,
  and clients must render the banner as inert text attributed to claude. The
  existing encrypted/authenticated relay transport remains unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-11

