# Channel pin wire contract

## Files read

- `internal/protocol/conversations_read.go` → `ConversationSummary`: nullable `ArchivedAt` precedent and list row fields.
- `internal/protocol/conversations_write.go` → `SetConversationMutedPayload`, `ConversationUpdatedPayload`: required boolean presence and update shape.
- `internal/protocol/codes.go`, `envelope.go`, `compat_test.go`: wire constants, application membership and compatibility partitions.
- `internal/protocol/conversations_read_test.go`, `conversations_write_test.go`, `testdata/conversations.json`, `testdata/conversation_updated.json`: fixture assertions and payload re-encoding gap.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes`: declarations awaiting production registration.
- `docs/knowledge/features/protocol-package.md` and its conversations-read/write topics: pure DTO boundary; pointer booleans preserve missing/null; no DTO validation methods.
- `docs/knowledge/features/development-verification-protocol-boundaries.md`: marshal decoded payloads into envelopes, assert literal public keys independently of DTO tags.
- `docs/protocol-mobile.md`: paired-client transport and protocol reference ownership.

## Context

Paired clients need a host-local pin timestamp and pin request vocabulary to merge channel pins across devices and hosts by pin time. This ticket declares that contract; storage (#3535), handler/projection (#3537) and activation (#3538) are separate. No decision record is needed.

One deliverable: additive pin wire vocabulary and its compatibility proof. Estimated total written work: 300 lines including plan/tests/fixtures, one new exported type, zero consumer call sites requiring updates, three acceptance criteria, zero state-machine reject branches. All five sizing limits hold.

Overlap: #3519 adds separate constants and compatibility/relay-guard entries; edits here stay local and additive. It supplies no prerequisite.

## Design

- Add `PinnedAt *time.Time` with JSON key `pinned_at` and no `omitempty` to both `ConversationSummary` and `ConversationUpdatedPayload`. Non-nil values encode RFC3339 timestamps; nil encodes explicit null; absent input decodes to nil. Add no archive timestamp to the update.
- Add `SetConversationPinnedPayload` with `ConversationID string` (`conversation_id`) and `Pinned *bool` (`pinned`), neither omitted. Both keys are required by the consumer contract. A nil boolean represents missing/null, distinguishable from false. Decoding non-booleans fails through `encoding/json`; semantic validation remains the handler's responsibility.
- Add `TypeSetConversationPinned = "set_conversation_pinned"` alongside mute and classify it in `inboundAppTypeSet` and each existing compatibility partition. Add `CodeConversationNotPromoted = "conversation.not_promoted"` beside promotion errors: non-retryable pin refusal for an unpromoted conversation; unpinning remains allowed.
- Mark the request excluded from production dispatch in `excludedTypes`, pending #3538. No registration, persistence, projection, capability or server-sent frame is added. Successful future replies reuse `conversation_updated`.
- Update list/update fixtures to the complete DTO wire shape, preserving existing values and adding timestamp/null pin cases. Re-encode decoded DTOs into the envelope in their round-trip tests. Use an additional unpinned update fixture to cover both values.

## Concurrency model

No goroutines or mutable runtime state are introduced. Application membership remains initialized once and read-only.

## State transitions and identity reuse

None: this slice declares data shapes only; pin/unpin lifecycle and repeated requests belong to #3537/#3538.

## Error handling

The DTO retains missing/null boolean as nil; future handlers must reject it with non-retryable `protocol.malformed` and require a conversation ID. Wrong JSON types return decoding errors. No production error paths are added here. Older hosts without request support report `protocol.unknown_type`; clients read missing timestamps as unpinned.

## Testing strategy

Write failing protocol tests before implementation. Direct marshal/decode tables cover both timestamp fields with timestamp, null and absent inputs, and assert serialized key presence. Literal request tests cover true/false, missing/null, wrong types and both required output keys. Pin the request and error wire strings independently. Typed list/update fixture round trips cover timestamp and null and preserve existing assertions. Existing compatibility and relay guards prove classification without registration.

Run Linux `go test -race ./internal/protocol/...` and `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build ./cmd/pyry` (output outside the worktree). The verifier owns the full-module gate. No live proof is needed.

## Open questions

None.

## Documentation handoff

Pending for the documentation stage in `docs/protocol-mobile.md`:

- “Application message types”: update `conversations`, `conversation_updated`, and add a `set_conversation_pinned` row; update corresponding payload descriptions/examples for the nullable, never-omitted RFC3339 `pinned_at` timestamp and declared request. State production pin handling/projection is pending #3537/#3538.
- Request payload: publish both required keys, `conversation_id` and `pinned`; missing/null `pinned` is `protocol.malformed`.
- “Error codes”: publish `conversation.not_promoted` as non-retryable refusal to pin an unpromoted conversation; unpinning remains allowed.
- Compatibility descriptions: absent `pinned_at` means unpinned; `protocol.unknown_type` identifies a host without pin-request support. No new capability is needed. The pin contract is host-local; clients merge hosts by pin time.
- “Changelog”: record the timestamp and declared request contract with the production handling/projection deferral.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `SetConversationPinnedPayload` is decoded untrusted data, not a validated command. `*bool` preserves missing/null so downstream handlers can reject them. OUT OF SCOPE: authenticated semantic validation and promoted-only pin enforcement belong to #3537/#3538; no handler is installed here.
- [Tokens, secrets, credentials] No credential fields or credential operations are added; pin timestamps and conversation IDs are metadata.
- [File operations] DTOs perform no filesystem operations; persistence belongs to #3535.
- [Subprocesses] No subprocess execution or caller-supplied command construction is added.
- [Cryptography] No encryption/key/nonce operations change; the declared request uses the existing paired-client transport.
- [Network and I/O] No socket reads or transport limits change. `IsKnownAppType` recognizes the discriminator only, and `excludedTypes` explicitly records pending dispatch rather than enabling a mutation path.
- [Errors, logs, telemetry] The new error code is a constant with no user bytes. No logs, payload echoes or telemetry are added.
- [Concurrency] No locks or goroutines are introduced, and the membership map stays immutable after initialization.
- [Threat model] A hostile client cannot mutate pins through a declaration alone. OUT OF SCOPE: paired-client authorization, malformed-request rejection and mutation/projection safety are owned by #3537/#3538 under the protocol security model.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-16
