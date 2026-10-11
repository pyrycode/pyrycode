# Queued-send identities on legacy routes

## Files read
- `cmd/pyry/queued_send_history.go` → `accepted`, `delivered`, `terminal`: durable links and acceptance barrier.
- `cmd/pyry/startup_send_history.go` → `closeStartupSends`: history-only restart loss.
- `cmd/pyry/legacy_runtime_receipts.go` → `legacyRuntimeReceipt`, `legacyHistoryReader`: shared projection and conservative watermark fallback.
- `cmd/pyry/conversation_history.go` → `newHistoryPager`: projects one raw page without filling filtered holes.
- `cmd/pyry/operator_message_history.go` → `operatorMessageHistory`: placement appends user then outcome before publishing.
- `cmd/pyry/operator_message_v2.go` → `broadcast`, `Run`: serialized fan-out and bounded nonblocking notification.
- `cmd/pyry/queued_message_placement.go` → `bindQueued`: synchronous stream chronology and delayed confirmation.
- `cmd/pyry/main.go`, `cmd/pyry/relay.go` → `runSupervisor`, `startRelayV2`: producer/emitter composition and recipient gates.
- `cmd/pyry/legacy_runtime_receipts_test.go` → `testLegacyCheckpoint`, `testLegacyListAndMark`: independent mobile accounting and correlated persisted read proof.
- `cmd/pyry/queued_send_history_test.go` → lifecycle, placement, startup and failure fixtures.
- `docs/knowledge/features/history-package-producers.md` § Accepted sends and linked outcomes: safe text differs from host delivery bytes.
- `docs/knowledge/features/history-package-producers-legacy-compatibility.md` § Legacy eligibility and explicit visibility: raw visibility grants no transport authority or sight.
- `docs/knowledge/features/history-package-watermarks.md` § Unread state uses a separate, lazily recovered watermark: raw and legacy targets differ.
- `docs/knowledge/features/msgqueue-package.md`, `development-verification.md`, `CODING-STYLE.md`, `docs/protocol-mobile.md` § Security model: producer and verification constraints.

## Context
Queued acceptance/outcomes occupy durable IDs but leave legacy receipt holes, preventing a displayed reply from reaching a confirmed read. Extend the existing inert receipt contract; no Mobile evidence rules or storage semantics change. This is one accounting repair, estimated at 700 total written lines (170 production, 440 tests/helpers, 90 plan), zero exported APIs, fewer than ten changed consumer sites, five acceptance criteria and fewer than ten validation reject branches. The written design remains within all five limits. No decision record is needed. Fetched feature branches #2873, #2882 and #3159 do not overlap the production files.

## Design
Recognize the four queued fact types in `legacyRuntimeReceipt`, validating matching conversation, successful nonzero durable identity/timestamp, acceptance time, outcome time and accepted reference, delivery reference, and exact allowed outcome reasons. Optional sender/message fields remain optional. Project only the existing five-field empty info banner. The pager and legacy reader reuse this classifier unchanged.

Add an optional queued receipt notification to `queuedSendHistory`. Acceptance records and enqueues its receipt before releasing `ready`; removal/give-up enqueue after appending. Startup uses the unbound history-only writer. Delivery returns its receipt to the operator commit, which publishes the user followed by the outcome as one serialized batch.

The operator emitter has a bounded receipt mailbox and wake signal. Notification touches only the mailbox mutex and never performs peer I/O. Both its Run loop and synchronous stream broadcast drain the mailbox under the existing fan-out mutex before publishing a user/outcome batch. This ensures accepted receipts cannot be overtaken by placement, even if Run has not consumed the wake. Full mailboxes log a content-free discriminant; durable paging remains recovery. Existing interactive/access/isolation gates and ring identity semantics remain in force.

## Concurrency model
Reuse the operator Run goroutine and its context shutdown/join. The mailbox mutex protects pending receipts; the fan-out mutex protects draining, ring publication and envelope IDs. Fan-out acquires mailbox briefly; producers acquire mailbox only, never fan-out. No queue callback waits for peer Push. Acceptance enqueues before closing `ready`; placement waits only for that completed durable acceptance. No new goroutine or storage counter.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Ordinary/send-now delivery, repeated confirmation | `TestQueuedSendHistoryLifecycle` with live/page identity assertions |
| Echo/idle before acceptance and delayed confirmation | `TestQueuedSendHistoryPlacement` with receipt ordering assertions |
| Removal/give-up, repeated removal | `TestQueuedSendHistoryLifecycle` |
| Startup loss, restart and reused queue ID | `TestQueuedSendHistoryStartup`, `TestQueuedLegacyReceipts_Startup` |
| Warm/reopened log, repeated/overlapping raw pages and replay | `TestQueuedLegacyReceipts_CompletedTurn`, `TestQueuedLegacyReceipts_Pages` |
| Peer Push stalled while acceptance and placement proceed | `TestQueuedLegacyReceipts_NonblockingPublication` |
| Nil/failed storage | `TestQueuedSendHistoryFailures`, `TestQueuedLegacyReceipts_StorageFailure` |

## Error handling
Invalid/unknown facts remain holes. Nil/failed append yields no receipt identity and does not alter eligible user delivery. Preserve append error classification, raw payloads, provenance, visibility, watermarks and allocation. Startup loss has no live/replay backfill. Receipt-only arrival supplies no presentation or read confirmation.

## Testing strategy
Write receipt projection and daemon-boundary regressions first and observe the missing receipt failure. Exercise a real queue/store/operator producer and interactive reply emitter through page/live/replay, independent checkpoints and correlated list/read persistence on warm/reopened logs. Add validation mutation and bounded-page controls, including both visibility values, unknown/malformed entries, receipt-only pages and repeated requests. Extend existing lifecycle/placement fixtures to prove outcome ordering and identity across ordinary/send-now, echo/idle and delayed confirmation. Run focused race tests, then `go test -race ./cmd/pyry/...`, `go vet ./...`, and build `./cmd/pyry` to scratch. The verifier owns `make check` and full daemon gates.

## Open questions
None. Device evidence is owned by the Mobile dispatcher and remains pending.

## Documentation handoff
Pending for the documentation stage:
- `docs/protocol-mobile.md`, “A history entry”, “Joining a page to the live stream” and “Marking a conversation read”: document the four queued-send receipts, validation, shared identities/page bounds and accounting without sight.
- `docs/knowledge/features/history-package-producers.md`, “Accepted sends and linked outcomes (#2972)”, and `docs/knowledge/features/history-package-producers-legacy-compatibility.md`, “Legacy eligibility and explicit visibility (#2965)”: replace total queued-fact legacy exclusion with raw-payload exclusion plus validated receipts.
- `docs/knowledge/features/history-package-watermarks.md`, “Unread state uses a separate, lazily recovered watermark (#2954)”: include queued receipts in the shared legacy target while retaining raw visibility and malformed/unknown barriers.

## Mobile dispatcher handoff
Pending: target the repaired daemon commit from Mobile #2070 / PR #2045 with the unchanged ordered command `ANDROID_GATE_WAIT_SECONDS=2700 timeout 3600 python3 scripts/android-test-gate.py scripted ping`, isolated `PYRYCODE_SRC=/work/Projects/pyrycode` and `PYRYCODE_RELAY_SRC=/work/Projects/pyrycode-relay`. Both `interactiveTurn_seededChannel_durableGapCatchUp` and `interactiveTurn_seededChannel_streamsScriptedPingReplyIntoThread` must execute/pass: 2 executed, 2 passed, zero failures/errors/skips. Retain command, app/daemon/relay revisions, per-method XML counts and artifact paths. Preserve final marker closure, exactly-once chronology, one page per physical pull, no automatic page-arrival catch-up and daemon-confirmed phone/peer reads; no ignored assertions, retries or longer deadlines. Subsequent full Mobile dispatcher gates remain pending.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: validate raw disk facts centrally before projection; require conversation and identity/time/reference/reason fields. Never copy sender text, attachments or raw payload into receipts.
- Tokens/secrets and cryptography: no credential, key or nonce operation; only fixed banner bytes reach existing encrypted transport.
- File operations: reuse `appendConversationHistory` and Store containment/permissions; no new path or file operation.
- Subprocesses: no new subprocess or environment input.
- Network/I/O: reuse recipient authorization and isolation in the broadcaster; bounded mailbox and one raw page cap work, existing transport limits stay in force.
- Errors/logs: existing append discriminants and fixed mailbox overflow event; raw facts and storage paths are never logged by the new projection.
- Concurrency: separate mailbox/fan-out locks prevent slow peer delivery from blocking acceptance; no lock inversion or new goroutine. Failed appends still release acceptance.
- Threat model: replay preserves durable identity and inert semantics; no read authority or sight inferred from arrival. Existing pairing, relay encryption/revocation and network DoS controls remain owned by the transport; this adds no security exception.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-11
