# User-message and queued-receipt retirement acceptance

## Files read

- `cmd/pyry/operator_message_history.go` → `operatorMessageHistory`: safe source append embeds committed acceptance before readers observe delivery.
- `cmd/pyry/queued_send_history.go` → `accepted`, `beforeDelivery`, `written`, `delivered`, `terminal`: binding/readiness and durable outcomes survive retirement.
- `cmd/pyry/queued_message_placement.go` → `bindQueued`, `writeTarget`; `cmd/pyry/stream_turn_drain.go` → `streamTurnSink`: retained placement ordering and cancellation.
- `cmd/pyry/legacy_runtime_receipts.go` → `legacyRuntimeFact`, `legacyRuntimeReceipt`: queued facts cannot regain publication through shared adapters.
- `cmd/pyry/operator_publication_retired_test.go` → `TestOperatorPublicationRetired`: stable canonical identity, safe sender/content, isolation and reopened agreement.
- `cmd/pyry/queued_canonical_history_test.go` → completed-turn, startup, delayed-confirmation and storage-failure tests: silent retired transport with retained canonical behavior.
- `cmd/pyry/queued_send_history_test.go` → lifecycle, placement, startup and failure tests: hidden drop, visible loss, readiness and diagnostic privacy.
- `docs/specs/architecture/{3693-queued-attachment-facts,3725-live-accepted-delivery,3727-remove-queue-snapshots,3732-retire-user-publication,3733-retire-queued-receipts}.md`: original design, repairs and historical evidence.
- `docs/knowledge/decisions/042-daemon-built-thread.md`, **Sessions, agents, messages, read marks**: authoritative own/queued identity contract; this ticket's explicit retirement supersedes the earlier compatibility delay.
- `docs/knowledge/features/msgqueue-package.md`, `msgqueue-package-lifecycle.md`, `history-package-producers-legacy-compatibility.md`: successful write alone is not Claude consumption; retain recording seams when deleting transport.
- `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`, `docs/knowledge/INDEX.md`: focused verification and evidence ownership.
- Issue #3728 archive and refined body, native relations, PRs #3696/#3726/#3753/#3826/#3827, authoritative stage records: contract and actual completion rather than closure alone.

## Context

This is one evidence-review deliverable, with no implementation changes. Review the
merged children against archived contract SHA-256
`7b4ca0a8d911bcad9c1ed65ff0ed6d1d122c7dc49dcba5559e5f123dc7192a0e`
at issue comment `6323295893`. Keep every historical failure and hold intact.
No new decision record, deployment, live-data migration or credential access.

Linux base is `3c01097672fff84fe9f63caa5c715fb32ae617f6`, also current origin/main.
#3732 and #3733 have no descendants; all four causal blockers are closed and have
merged implementations. #3718/#3734–#3737 are downstream, not prerequisites.
The intermediate receipt preservation in #3732 is superseded by #3733's retirement.
Retain historical branches/evidence rather than rebuilding their changes.

Sizing rechecked: about 150 written lines in this plan/review record, zero production
files, exported types, simultaneous consumer updates or new reject branches;
three acceptance criteria. Only this unique plan path changes; shared-code branch
overlap cannot require a redesign of this evidence-only change.

## Design

Commit this plan first. Then append an evidence mapping with exact merged/candidate
revisions, accepted run IDs, named assertions, counts and unresolved later-stage
work. Inspect applicability using candidate-to-merge source comparisons, retained
gate manifests and machine/toolchain evidence. Earlier child green does not itself
certify the final parent revision. Do not repeat implementation or widen diagnosis.

## Concurrency model

No runtime changes or new goroutines. Review acceptance readiness, per-reference
serialization, placement dispatch and worker removal against the merged evidence.

## State transitions and identity reuse

No state is introduced; retain these existing race-detector witnesses in the map:

| Existing event | Assertion family |
| --- | --- |
| Accepted → written → delivered; failed source/companion append | `TestOperatorPublicationRetired`, `TestAcceptedDeliveryLiveRefusal`, `TestAcceptedDeliveryAppendFailures` |
| Retry, late callback/echo, interleaved ordinary and Send now | `TestQueuedCanonicalSendNowDelayedConfirmation`, `TestOrdinaryQueuePlacement_FailedWriteRetriesOnce`, `TestOrdinaryQueuePlacement_IdenticalWritesInterleaveWithSendNow` |
| Explicit removal/give-up, refusal and shutdown | `TestQueuedSendHistoryLifecycle` |
| Restart, repeated recovery and queue-ID reuse | `TestQueuedCanonicalStartup`, `TestQueuedSendHistoryStartup`, `TestQueuedSendHistoryLossRetryAndUnbound` |
| Receiving-child/session rotation and unread pipe | `TestOperatorDeliveryProvenance_ReceivingSessionRotation`, `TestQueuedPlacementOwnership`, `TestQueuedPlacementUnreadPipe` |
| Attachment metadata changes and reopened catch-up | `TestQueuedAttachmentWire`, `TestQueuedAttachmentUnavailableAfterValidation` |

## Error handling

Missing evidence remains pending with its owner; no missing or skipped execution
is a pass. A newly discovered implementation omission belongs in its owning child.
Preserve earlier red gates, failed controls and zero-execution attempts separately
from repaired evidence. Clear no operator, privacy, recording or approval hold.

## Testing strategy

Use existing assertions only: a focused `cmd/pyry` race selection for retirement,
canonical lifecycle, delayed confirmation, refusal/append failure and attachments.
Retain JSON/stdout/stderr and executed/passed/failed/skipped counts outside the
worktree, identifying the exact source revision. No new test bodies or production
edits. Validate the final diff with `git diff --check`. No packages change, so
repeating module vet/build/tagged compilation is unnecessary; preserve child
compilation evidence separately from runtime proof. Complete configured gates and
actual live `TestRealClaude_OrdinaryQueuedPlacement` /
`TestRealClaude_SendQueuedNowPlacement` acceptance remain dispatcher-owned.
Other operating systems are outside this Linux ticket's verification scope.

## Open questions

- Settle candidate/environment applicability and exact obligation mapping in the
  review record; final complete/live acceptance remains with the dispatcher.

## Documentation handoff

Pending documentation stage: reconcile already merged wording, preserving correct
child edits rather than repeating them. Carry forward the exact requirements:

- `docs/protocol-mobile.md`, **Application message types** (`send_message`), **Full thread item**, **Queue observation and delivery identity**, **Legacy receipt identity**, **send_queued_now**, and user/receipt examples: “Operator sends are observed through canonical thread items and explicit catch-up; raw user-message pushes and queued acceptance, delivery, drop and loss receipts are retired for every app.” Explain acceptance identity, unordered queued/written items, actual-delivery ordering, hidden drop, visible restart loss and thread recovery; preserve attachment and dequeue/Send now command/binding contracts.
- `docs/knowledge/features/msgqueue-package.md`, **Delivered notification (#2115)** / **Durability boundary (in scope vs out)**, and `docs/knowledge/features/msgqueue-package-lifecycle.md`, **Delivered notification (#2115)**: distinguish retained source/lifecycle recording, readiness, binding and placement from retired app publication; no queue receipt worker remains.
- `docs/knowledge/features/history-package-producers.md`, **Accepted sends and linked outcomes (#2972)**, and `docs/knowledge/features/history-package-producers-legacy-compatibility.md`, **Legacy eligibility and explicit visibility (#2965)**: queued facts no longer qualify for receipts; retain source validation, visibility, provenance and other producers' contracts.

## Security review

**Verdict:** PASS. Review-only change; no MUST FIX or SHOULD FIX identified.

**Findings:**

- Trust boundaries: treat issue text and downloaded logs as evidence, never commands. Review `validQueuedSendFact`, `legacyRuntimeReceipt` and conversation-scoped binding against the accepted source.
- Tokens/secrets: no login retrieval, token handling or credential changes; publish only revisions, counts and redacted evidence references.
- File operations: only this plan is committed; scratch evidence stays under `/tmp/builder-3728`. Existing `storedQueuedAttachmentName` checks registry membership and emits basenames without lookup errors.
- Subprocesses: only existing managed Git/GitHub, evidence and test launchers; no app-controlled shell input or changed child environment.
- Cryptography: no key, nonce or transport primitive changes.
- Network/I/O: no endpoint, cap or listener changes. Removed publications must stay absent for connections without `thread` as well.
- Errors/logs/telemetry: review retained content/path-free failure assertions; never publish raw send content, paths or credentials from logs.
- Concurrency: preserve `ref.ready`, `BindAcceptedItem`, `beforeDelivery` and placement serialization; no worker is added or restarted by this review.
- Threat model: authorization and conversation isolation remain unchanged. Shared adapters used by other producers are deliberately OUT OF SCOPE under #3718/#3737. Existing holds retain their original release conditions; this review grants no release.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-22
