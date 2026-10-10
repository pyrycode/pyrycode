# #3072 — Daemon thread live wire contracts

## Files read

- `internal/protocol/handshake.go` → capability constants: declare vocabulary without changing negotiation.
- `internal/protocol/handshake_test.go` → `TestCapability_Constants_MatchSpec`: pin the literal independently of consumers.
- `internal/protocol/codes.go` → outgoing type constants: keep declarations visible to the existing catalog guard.
- `internal/protocol/envelope.go` → `Envelope`: additive optional metadata must preserve legacy serialization.
- `internal/protocol/conversations_read.go` → `ConversationSummary`: stored binding and durable/legacy watermarks retain their meanings.
- `internal/protocol/compat_test.go` → `v2OnlyTypes`, `TestTypeConstants_V1V2Partition`: classify the new outgoing kinds without admitting v1 requests.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes`, `TestEveryInboundV2TypeHasHandler`: classify declared outgoing kinds as pending publication.
- `internal/thread/fold.go` → `Item`, `addItem`: retain every fact without importing the fold back into protocol.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → Item model, Update messages, Compatibility: open item vocabulary and migration boundaries.
- `docs/knowledge/features/protocol-package.md` and its envelope/conversations-read topics: pure-data boundary and optional-key compatibility.
- `docs/knowledge/features/thread-package.md` and main-thread folding topic: zero order until delivery, recorded no-child versus unknown provenance, no attribution defaults.
- `docs/knowledge/features/development-verification.md` → Protocol boundaries: marshal decoded DTOs into their envelopes and inspect emitted keys, not untouched raw payloads.
- `CODING-STYLE.md`: stdlib tests, contract comments and existing catalog source-file expectations.

## Context

ADR 042 migration step 3 needs one shared item contract for live updates and later catch-up/pages. This ticket declares data only. Negotiation belongs to #3075, publication to #3077, bounded encoding/continuations to #3073 and catch-up/page verbs to #2963. The existing ADR covers the decision; no additional decision record is needed.

One deliverable: the thread live wire contract, including its envelope and summary metadata. Sketch and final plan sizing: approximately 500 total written lines, four new exported structs, zero simultaneous consumer migrations, four acceptance criteria and zero state-machine rejection branches. Five production files include the existing catalog file. No overlapping feature branches were found for the proposed existing files; additive catalog classifications will also be checked before build.

## Design

- Declare `CapabilityThread = "thread"` and `TypeThreadItemAdded`, `TypeThreadItemChanged`, `TypeThreadTextAppend` in the owning constant files. Keep them out of `inboundAppTypeSet`; classify them in compatibility and relay catalog tests only.
- Add `ThreadItem` in `internal/protocol/thread.go`. It mirrors `thread.Item` facts with numeric `uint64` IDs/orders/revisions and string kind/status vocabulary. Identity and kind are stable; producers must supply values in the history-entry range below 2^53. No validation or mapping is introduced here.
- Required item keys: `id`, `kind`, `rev`, `status`, `active`, `shown`, `summary`, `content`. `order`, `ended_order`, `parent` omit zero; `session`, `agent`, `turn`, `subtype` omit empty; `no_child` omits false and carries true for recorded no-child provenance. Unknown attribution omits session/agent without inventing defaults. `Content` is inert `json.RawMessage`, retaining kind-specific and unknown JSON.
- Add `ThreadItemAddedPayload`, `ThreadItemChangedPayload`, `ThreadTextAppendPayload`. All carry `conversation_id`, string `epoch`, numeric `version`. Add carries the full item. Change/append carry numeric `item_id`, `base_rev`, `rev`; append adds required string `text` containing only the suffix.
- `Changes` is `map[string]json.RawMessage`: absent entries mean unchanged, explicit empty/false/zero/null values remain supplied replacements. A `content` change replaces the entire content value. Identity/kind must not be changed; application and validation belong to consumers.
- Add `ConversationSummary.LastShownVersion *uint64` with `omitempty`. Nil omits the field; pointer to zero emits numeric zero. It tracks shown additions/text appends, not the maximum current revision. Other fields retain their current meanings.
- Add `Envelope.SessionID json.RawMessage` with `omitempty`: nil means metadata not supplied, JSON `null` positively means no producing session, and a JSON nonempty string identifies the producer. Raw JSON preserves absence/null on decoding without a custom marshaler or an extra wrapper type. Delivery consumers restrict this metadata to thread-negotiated live state and validate its shape.
- Add `Envelope.SessionStateCleared bool` with `omitempty`: true plus payload `{}` clears the current reading for this kind; ordinary updates omit it. Existing payload session fields remain independent.

## Concurrency model

Pure data declarations; no goroutines, shared mutations, locks or I/O. Consumers own synchronization and the lifetime of raw JSON/maps.

## State transitions and identity reuse

None: serialization declares identity/version fields but neither allocates/reuses identities nor applies state transitions. Race-enabled contract tests cover round trips rather than lifecycle logic.

## Error handling

Use standard `encoding/json` errors. Raw content and patch values must be valid JSON to marshal. Numeric bounds, allowed metadata shapes, clear/payload pairing, patch applicability and immutable identity are producer/consumer contracts, with no fold validation or patch application here.

## Testing strategy

Write tests first and observe the missing declarations fail compilation, then implement. Use envelope fixtures with distinct connection/item/order/base/result/version values. Decode payloads into the new DTOs, compare whole decoded structs, marshal them back into envelopes and compare parsed emitted JSON.

- Cover all eight ADR kinds and an unknown kind/status, retained nested unknown content, Claude/Codex/known-session-unknown-agent/unknown/no-child attribution, explicit false flags and all optional key omissions.
- Cover queued/dropped/lost zero order and main-thread zero parent omissions, delivered order and an ID/revision near 2^53.
- Cover changes with omitted fields versus explicit empty/false/zero/null and full replacement content, plus append suffix and distinct revisions.
- Cover absent/zero/nonzero summary watermark while preserving existing summary keys; existing envelope and summary fixture checks prove unset fields stay byte-compatible.
- Cover session metadata omitted/null/string and clear `{}` round trips, ordinary flag omission and independent existing payload session fields.
- Run `go test -race ./internal/protocol/... ./cmd/pyry/...`, `go vet ./...`, and `go build -o /tmp/builder-3072/pyry ./cmd/pyry`. Full-module tests belong to the verifier gate. No live-Claude test is required for declarations.

## Open questions

None. Raw JSON supplies the required tri-state and patch-presence semantics without new decoding behavior.

## Documentation handoff

Pending documentation stage: update `docs/protocol-mobile.md` sections “Capability negotiation (v2)”, “Message envelope”, the application-message catalog, thread update sections and `conversations` message section. Document `thread`, all full item keys, change/append fields, optional `last_shown_version` and session/clear rules above. Mark the capability and live kinds declared but not yet emitted in both catalog rows and section text. Describe live-state metadata as thread-only at delivery. Preserve `current_session_id` as stored binding, `read_up_to` as durable read mark and `latest_entry_id` as the legacy displayable-entry watermark.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `ThreadItem.Content`, `ThreadItemChangedPayload.Changes` and `Envelope.SessionID` are inert JSON, not validated or trusted state. SHOULD FIX: document allowed shapes and consumer obligations beside these fields and prove unknown content survives without interpretation.
- [Tokens, secrets, credentials] No credential generation/storage/lifecycle is added. Item content can contain user data; these DTOs add no logs or external output path.
- [File operations] No paths, filesystem reads or persistence enter production code; mapping stays outside protocol.
- [Subprocesses] No subprocess invocation or child environment is added.
- [Cryptography] No cryptographic operations are added; epoch generation belongs to downstream producers.
- [Network and I/O] OUT OF SCOPE: frame budgets/continuations belong to #3073, and delivery gating to #3075/#3077. Declarations add no reads, sockets or emission; transport caps remain at existing boundaries.
- [Errors, logs, telemetry] Standard JSON failures only; no payload logging, metrics or additional error reflection.
- [Concurrency] No new goroutines or shared mutable state; callers own raw-byte/map mutation during encoding.
- [Threat model] OUT OF SCOPE: authenticating and authorizing delivery remains in existing transport/negotiation consumers (#3075/#3077). Merely declaring the capability does not advertise it or admit incoming thread frames.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-10
