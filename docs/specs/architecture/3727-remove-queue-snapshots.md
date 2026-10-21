# Remove outbound queue snapshots

## Files read

- `cmd/pyry/queue_state_v2.go`: change notifications, snapshot projection and reconnect provider are exclusive legacy publication.
- `cmd/pyry/main.go`, `cmd/pyry/relay.go`: queue composition and producer shutdown; retain lifecycle observers, binding, wakeups and placement synchronization.
- `internal/relay/v2session_seams.go`, `v2session_handshake.go`, `v2session_modal.go`: connect/reconnect reconciliation and queue controls.
- `internal/relay/v2session_thread.go`: negotiated content gating.
- `internal/protocol/messaging.go`, `codes.go`: remove snapshot declarations while preserving command payloads.
- `internal/e2e/internal/scenario/ordinary_queue.go`, `send_now.go` and control tests: held Bash checkpoints, action timing, placement and late duplicates.
- `internal/e2e/relay_v2_stream_queue_drain_test.go`: jointly held backlog, drop before release and surviving FIFO delivery.
- `internal/e2e/realclaude/thread_shadow_schema_test.go`, `thread_shadow_user_test.go`: independent raw/canonical facts and retired legacy projection.
- `docs/knowledge/features/msgqueue-package.md`, `msgqueue-package-api.md`, `thread-package.md`: queue introspection remains useful; thread delivery supplies order, queued items have none.
- `docs/knowledge/decisions/042-daemon-built-thread.md`: own/queued identity and lifecycle contract.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: focused counted race proof and artifact provenance.

## Context

Remove the outbound queue snapshot contract for every app. Queue commands and
the in-memory FIFO remain. This is one observable retirement, including both
change and reconnect producers and its necessary scenario proofs. Raw user and
receipt retirement remains #3728. No new decision record is needed.

## Design

Delete the exclusive emitter, notifier, reconcile provider and configuration.
Remove snapshot protocol declarations, registration, fixtures and positive tests.
Retain queue source/introspection APIs, lifecycle observers, acceptance readiness,
`BindAcceptedItem`, command validation and optional accepted-item bindings.

Placement scenarios negotiate thread and explicitly catch up before observing
live changes. Maintain items by identity and revision; queued active user items
provide `content.queued_msg_id`, and item IDs bind send-now/drop controls.
Keep held-turn barriers, composer sends, delivery ordering, foreign conversation
controls and the late duplicate window. Canonical tool/text/turn observations
replace raw observations only in these affected scenarios. The fake held-drain
test observes all three queued items before drop/release and verifies surviving
FIFO output. No connect-time thread publisher is added.

Remove only queue snapshots from shadow legacy expectations and captures;
preserve raw history, provenance, canonical content, attachments and lifecycle.
Other raw observers stay with their own retirement slices.

Overlaps: #3240 edits other push/handshake blocks; #3722 edits conversation listing
and summary tests. Local removals can merge independently.

Sizing: approximately 750 written lines including this plan, no new exported
types, at most 10 consumers per changed API, two acceptance criteria, no new
production rejection branches. The current ticket is already a grandchild.

## Concurrency model

Remove the snapshot producer goroutine and its notification channel. Existing
queue drains, history callbacks and thread publishers retain their locks,
context cancellation and joins. Test readers consume encrypted frames in order.

## State transitions and identity reuse

| Event | Race proof |
| --- | --- |
| Initial connect and reconnect, with/without thread | `TestQueueSnapshotsRetired` |
| Enqueue, ordinary delivery, dequeue and send-now | `TestQueueSnapshotsRetired`, `TestQueuedItemActions` and scenario controls |
| Held backlog, drop then FIFO drain | `TestRelayV2_StreamMidTurnHoldDropAndDrainInOrder` |
| Foreign conversation, repeated/late delivery frames | `TestOrdinaryQueueControls`, `TestSendNowPlacementControls` |
| Queue-ID reuse and optional durable binding | existing queued-item action and relay dequeue/send-now tests |

## Error handling

No new production failure path. Preserve malformed/mismatching binding rejection
and omitted binding behavior. Scenario readers reject errors, revision gaps and
retired snapshots rather than accepting missing evidence.

## Testing strategy

Write absence and canonical control regressions first and observe the relevant
failure. Run affected race tests through shared test admission on Linux, retain
revision and executed/passed/failed/skipped counts. Compile affected `e2e` and
`e2e_realclaude` packages after deletions. Run vet and binary build. Full gates
and counted `TestRealClaude_OrdinaryQueuedPlacement` /
`TestRealClaude_SendQueuedNowPlacement` acceptance remain dispatcher-owned.

## Open questions

None. Implementation details of canonical scenario accumulation are settled
against the existing wire payloads; revisions below will record contract changes.

## Documentation handoff

Pending documentation stage:

- `docs/protocol-mobile.md`, **Queue (v2)**, application-message table, queue-snapshot examples and **Reconnect / Backfill semantics**: remove outbound/connect-time `queue_state`; explain queued identity/status and recovery through thread items. Retain dequeue/send-now command contracts including optional accepted-item binding. Update reset/queue wording promising an empty snapshot.
- `docs/knowledge/features/msgqueue-package.md`: update introductory queue-publication wording.
- `docs/knowledge/features/msgqueue-package-api.md`, **Introspection, removal, and change notification (#719)**: distinguish retained source/queue-operation APIs from retired app snapshots.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: preserve `queueItemBinding` and conversation validation in queue command handlers; canonical IDs are lookup/binding data, never authorization.
- Tokens/secrets: no credential changes. Removing snapshot publication eliminates an outbound text projection; retain content-free logs.
- File operations: no new production file operations; shadow fixture edits preserve recorded history and capture provenance.
- Subprocesses: only existing helper-process and fake/live harnesses; no new shell interpretation of app content.
- Cryptography: retain Noise transport and in-order frame decryption; no key/nonce changes.
- Network/I/O: retain existing envelope caps, session gates and bounded thread replies. No connect publisher or additional listener.
- Errors/logging: no new payload logging; rejected controls retain generic wire errors.
- Concurrency: remove one producer and channel, retain queue callbacks and thread placement synchronization. Existing cancellation/join paths remain.
- Threat model: paired clients still control only validated conversation/queue bindings; preserve isolation and malformed/mismatching binding tests. Raw message retirement is OUT OF SCOPE, owned by #3728.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-21

## Revisions

- 2026-10-21: Queue observation uses one canonical item accumulator for explicit
  catch-up, additions, patches and appends. Delivery checks bind the accepted
  item ID because a terminal queue handle is no longer an action target. The source
  guard `TestQueueSnapshotsRetired` prevents either publication arm or protocol
  registration returning for any capability; command/lifecycle race tests and
  the held-daemon integration test prove retained behavior. Retained broadcast
  helpers moved out of the deleted snapshot test file. Shadow Legacy filtering
  preserves all other captured JSON bytes, history and provenance.
- 2026-10-21: The ordinary recorded scenario demonstrated that live `turn_state`
  can arrive before a canonical delivery update. Placement therefore compares
  canonical user/tool/assistant items; live phase is not a content ordering key.
  The early-reply adverse control now inserts a canonical assistant item before
  delivery, retaining the supported ordering assertion. The failed capture is
  retained separately from the repaired run.
