# #2819 — Name the queued entry on delivered user messages

## Files read

- `internal/protocol/messaging.go` → `MessagePayload`, `QueuedItem`: additive optional ID with the existing queue-state integer type.
- `internal/protocol/messaging_test.go` → `TestMessagePayload_RoundTrip`: legacy assistant fixture; decoded payload must actually be re-marshalled to exercise omission.
- `cmd/pyry/operator_message_history.go` → `newOperatorMessageHistory`: one serialization supplies the stored entry and live push, including deferred send-now commits.
- `cmd/pyry/operator_message_history_test.go` → ordinary retry/push, send-now echo, and zero-entry scenarios: extend existing proofs.
- `cmd/pyry/send_now.go` → `sendNowPlacement.attach`, `echo`: queue ID already matches deferred commits; timing stays as shipped.
- `cmd/pyry/main.go` → `runDaemon` queue wiring: the producer is installed on `OnDelivered`.
- `internal/msgqueue/queue.go` → `EnqueueSent`, `notifyDelivered`: daemon-generated per-conversation IDs start at one and survive delivery projection.
- `docs/knowledge/features/protocol-package.md` and `protocol-package-types-messaging-payloads.md`: optional-field conventions; historical no-live-push claims are superseded by current producer code.
- `docs/knowledge/features/msgqueue-package.md` → Delivered notification: use the client-safe projection rather than the composed delivery prompt.
- `docs/knowledge/features/development-verification.md` → Protocol boundaries: round-trip decoded DTOs and inspect emitted keys.
- `CODING-STYLE.md`; `docs/protocol-mobile.md` → Security model: Go conventions and existing remote-control threat model.

## Change

Add `MessagePayload.QueuedMsgID uint64` with JSON tag `queued_msg_id,omitempty`, matching `QueuedItem.QueuedMsgID`. Set it from `msgqueue.QueuedMessage.ID` in `newOperatorMessageHistory`. This names the daemon's queued entry, scoped by `conversation_id`, independently of the non-unique client `message_id`. Zero means no queue entry and omits the field, including assistant messages and legacy payloads. No signature, state, delivery timing, authorization or goroutine changes. No overlapping feature branches touch the planned files.

Sizing after planning: one deliverable; approximately 140 written lines including this plan and tests, zero new exported types/interfaces, one producer assignment, two acceptance criteria and zero new error/reject branches. This remains within all five limits and the XS estimate's scale.

## Testing strategy

Extend the real queue ordinary-delivery push test to compare its returned nonzero ID with the raw JSON integer, while retaining byte equality against stored history. Extend the send-now echo test to assert ID 4, then deliver an ordinary entry with the same `message_id` and ID 5; prove distinct IDs and identical stored/live bytes for both. Extend the no-entry producer case to assert omission. Add table-driven protocol cases for populated integer IDs (including uint64 precision), zero omission, legacy user and assistant messages; marshal the decoded payload and inspect keys. Run focused tests red before implementation, then `go test -race ./internal/protocol/... ./cmd/pyry/...`, `go vet ./...`, and `go build ./cmd/pyry` (binary directed to scratch). The verifier owns the full-module gate.

## Documentation handoff

- Pending for documentation stage: `docs/protocol-mobile.md`, the `message` field description and Queue (v2): document the optional integer and identity as `conversation_id` plus `queued_msg_id`; older daemons omit it and clients retain current inference as fallback. Add a dated changelog line.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. `EnqueueSent` allocates the ID under the queue lock; `notifyDelivered` projects it and the producer copies that integer, never the client's `message_id`. The ID is a conversation-scoped correlation value, not authorization.
- [Tokens, secrets, credentials] No findings. The counter is already public in `queue_state`; no credentials or secret material enters the added field.
- [File operations] No findings. The existing history append receives the same serialized payload as the push; the integer is never a path component and no file-operation API changes.
- [Subprocesses] No findings. This producer receives the safe delivered projection; no command, environment or child-lifecycle change.
- [Cryptography] No findings. The design adds plaintext payload vocabulary inside existing encrypted transport; no keys, nonces or cryptographic primitives change.
- [Network and I/O] No findings. No new receiver or unbounded content: the additional field is a fixed-width uint64 using the existing message transport.
- [Errors, logs, telemetry] No findings. Serialization and content-free error logging remain in `newOperatorMessageHistory`; the field adds no payload logging.
- [Concurrency] No findings. ID is copied into payload bytes before the existing commit closure is deferred; no shared mutable state, lock or goroutine is added.
- [Threat model] No findings. Remote-control authorization, Noise transport and prompt-injection exposure in the protocol Security model are unchanged by naming the already-public queue entry; no new capability is introduced.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-05

## Revisions

- 2026-10-05: Correct the Files read symbol for `cmd/pyry/main.go`: the queue composition root is `runSupervisor`, not `runDaemon`. No design or contract change.
