# Retire queued receipt publication

## Files read
- `cmd/pyry/queued_send_history.go`: `accepted`, `beforeDelivery`, `written`, `delivered`, `terminal` retain durable facts and readiness; retire `receipt`, `notify`, `publish`.
- `cmd/pyry/operator_message_history.go`: `operatorMessageHistory` embeds acceptance before publishing a delivery source to readers.
- `cmd/pyry/operator_message_v2.go`: receipt-only mailbox, worker and transport to remove.
- `cmd/pyry/main.go`, `cmd/pyry/relay.go`: `runSupervisor`, `relayWiring`, `startRelayV2` own exclusive emitter wiring.
- `cmd/pyry/legacy_runtime_receipts.go`: `legacyRuntimeFact`, `legacyRuntimeReceipt` must exclude all queued facts.
- `cmd/pyry/queued_message_placement.go`: `bindQueued`, `writeTarget`, `takeQueued` retain synchronization and receiving provenance.
- `cmd/pyry/queued_send_history_test.go`, `cmd/pyry/queued_legacy_receipts_test.go`: mixed lifecycle, startup, delayed confirmation and failure scenarios require canonical assertions.
- `cmd/pyry/operator_publication_retired_test.go`: preserve canonical identity coverage; remove receipt-only worker tests.
- `cmd/pyry/accepted_delivery_test.go`: retain live follower and failed-companion append regressions unchanged.
- `docs/knowledge/features/msgqueue-package.md`, `msgqueue-package-lifecycle.md`: successful write is distinct from actual Claude placement; removing publication must retain recording seams.
- `docs/knowledge/features/thread-package.md`, `history-package-producers.md`, `history-package-producers-legacy-compatibility.md`: raw facts remain authoritative; legacy eligibility is independent of visibility.
- `docs/knowledge/decisions/042-daemon-built-thread.md`, Sessions, agents, messages, read marks: stable acceptance identity, unordered queued items, ordered actual delivery and hidden drops.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: focused race proof, tagged compilation and content-free logging.

## Context
Queued sends already have authoritative thread items. Receipt publication is redundant
and must stop for every app, without delaying release or a compatibility adapter.
This is one observable retirement, with its necessary lifecycle verification.
No new decision record is needed; ADR 042 defines the retained contract.

Sizing: about 450 added/edited lines plus deletions, zero exported types, three
acceptance criteria and no added state-machine reject branches. Removing
`testQueuedReceiptEmitter` updates 11 consumer declarations, exceeding the raw
10-consumer limit. Confirmed native ancestry is #3733 -> #3728 -> #3717;
`needs-human:sizing` already exists. Split-depth requires building in place.
The potential seams would be live publication retirement and legacy projection
retirement, but both are required for the same queued observation contract.
No fetched feature branch overlaps the six production files.

## Design
Delete the receipt-only emitter, mailbox, worker and exclusive relay wiring.
Remove queue-history publication callbacks and make `delivered` return no value.
Keep its durable outcome append and forgetting behavior. Keep acceptance readiness,
binding, attachment snapshots, written facts, terminal arbitration and source capture.
`operatorMessageHistory` retains its acceptance reference embedded in the message
before append; only its receipt handoff disappears. Keep shared placement capacity
under a placement-owned name if it is still consumed elsewhere.
Exclude queued facts from both legacy classification and projection, leaving other
producer families and the interactive emitter constructor API intact.
Convert mixed tests to canonical item assertions; remove receipt-exclusive tests
and fixtures, including skipped cases if present. Retain shared helpers.

## Concurrency model
Remove one relay worker and its cleanup. No replacement goroutine is introduced.
Queue drains and placement dispatch retain their cancellation and joining contracts.
`ref.ready` still orders acceptance append/binding before delivery. Per-acceptance
mutexes still serialize written/delivered facts; no peer I/O occurs in these paths.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Acceptance -> ordinary delivery, retry, Send now, removal/give-up, refusal/shutdown | `TestQueuedSendHistoryLifecycle` |
| Echo/idle before acceptance or delayed queue confirmation | `TestQueuedSendHistoryPlacement` |
| Delayed Send now confirmation and repeated late echo | converted delayed-confirmation test |
| Written remains unordered before actual delivery | delayed-confirmation test and existing queued placement selections |
| Restart loss, repeat recovery, queue ID reuse in new run | `TestQueuedSendHistoryStartup`, `TestQueuedSendHistoryLossRetryAndUnbound` |
| Live follower sees delivery before companion outcome, append failure | `TestAcceptedDeliveryLiveRefusal`, `TestAcceptedDeliveryAppendFailures` |

## Error handling
Retain existing append failures, discriminants and content/path-free logs. Missing
acceptance never fabricates a reference; failed delivery never fabricates an outcome.
Failed companion outcomes cannot split acceptance identity. No transport error path
remains for queued receipts. Refusal and shutdown alone resolve nothing.

## Testing strategy
First prove valid queued facts still project legacy receipts with a failing retirement
assertion. Convert existing mixed tests to check thread identity/status/order/visibility
and warm/reopened equality, paired with source facts and silent legacy observation.
Run focused affected `cmd/pyry` Go race selections through shared admission, plus
`go vet ./...`, build to scratch, and tagged `e2e` / `e2e_realclaude` compilation.
Record candidate, selections, reports and counts in the PR. Linux only.
Full configured gates and live `TestRealClaude_OrdinaryQueuedPlacement` /
`TestRealClaude_SendQueuedNowPlacement` are pending dispatcher-owned acceptance.

## Open questions
None.

## Documentation handoff
Pending documentation stage:
- `docs/protocol-mobile.md`, **Queue observation and delivery identity**, **Full thread item**, **Legacy receipt identity**, and affected user/receipt examples: state “Queued sends are observed through canonical thread items and explicit catch-up; queued acceptance, delivery, drop and loss facts produce no legacy receipts.” Describe acceptance identity, unordered queued state, actual-delivery order, hidden drop and visible restart loss. Preserve attachments and dequeue/Send now commands and binding.
- `docs/knowledge/features/msgqueue-package-lifecycle.md`, **Delivered notification (#2115)**, and `docs/knowledge/features/msgqueue-package.md`, **Delivered notification (#2115)** / **Durability boundary (in scope vs out)**: replace the receipt-publication/mailbox contract with retained source recording, readiness, binding, placement and thread recovery; no queue receipt worker remains.
- `docs/knowledge/features/history-package-producers.md`, **Accepted sends and linked outcomes (#2972)**, and `docs/knowledge/features/history-package-producers-legacy-compatibility.md`, **Legacy eligibility and explicit visibility (#2965)**: remove queued facts from receipt eligibility and queued receipt-positive verification guidance; retain source validation, visibility, provenance and other producers' contracts.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: retain `validQueuedSendFact`, safe `MessagePayload`, attachment lookup and conversation-scoped binding. Removing legacy eligibility reduces network exposure; raw visibility cannot restore it.
- Tokens/secrets: no credential access or lifecycle changes; no new sensitive fields.
- File operations: existing history append and `storedQueuedAttachmentName` behavior remains; no new paths, permissions or storage format.
- Subprocesses: no launch or environment change; placement and delivery use existing seams.
- Cryptography: no primitives, keys or nonces involved.
- Network/I/O: receipt transport is deleted; existing connection limits and other producers remain unchanged.
- Errors/logs: preserve event-only append warnings and privacy tests; no content or host paths added to logs.
- Concurrency: readiness and per-reference mutex remain; deletion must not remove binding or the pre-append acceptance reference. Covered by accepted-delivery regressions.
- Threat model: transport retirement preserves existing relay authorization and conversation isolation. No deployment or live-data migration is part of this change.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-22
