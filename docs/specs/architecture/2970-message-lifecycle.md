# Message acceptance and terminal facts (#2970)

## Files read

- `internal/msgqueue/queue.go` → `EnqueueSent`, `Remove`, `SendNow`, `drain`, `giveUp`: insertion bounds, commit arbitration, retry pacing and shutdown join.
- `internal/msgqueue/delivery_context.go` → `queued.message`, `DeliveryMessage`: copied safe projection without composed payload.
- `internal/msgqueue/delivered_test.go` → `TestQueue_OnDelivered_FiresWhenRemoveRacedConfirmedDelivery`: confirmation must remain observable independently of FIFO advance.
- `internal/msgqueue/send_now_test.go` → `TestQueue_SendNow_RefusedHeadStartsNoGiveUpStreak`: taking and reinserting a head must remain a clean cancellation.
- `internal/msgqueue/head_advance_test.go`, `streak_reset_test.go`, `giveup_exempt_test.go`: existing regression gates for head ownership and retry windows.
- `docs/knowledge/features/msgqueue-package.md` → “Delivered notification (#2115)” and “Concurrency model”: safe projection and inherited shutdown observation gap.
- `docs/knowledge/features/msgqueue-package-send-now.md` → `headTaken`: preserve cancellation classification independently of reinsertion.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → decisions 1 and 2: memory-only queue, facts for a later history writer; no new decision record needed.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`, `docs/protocol-mobile.md` → “Security model”: concurrency conventions, non-vacuous tests and trusted projection boundary.

## Context

The queue records client-readable content but cannot identify accepted sends that disappear before delivery. This engine-only contract supplies facts for identity adoption in #2971 and durable recording in #2972. Queue IDs remain per-conversation, per-run identifiers. No authentication, deduplication, persistence or production wiring is added.

Sizing: one deliverable (queue lifecycle observation), five acceptance criteria, about 700 total written lines including this plan and tests, three production files, at most four new exported types, zero consumer migrations and fewer than ten new resolution branches. The #2115 analogue added 287 package lines; ordering and arbitration account for the additional work. No other fetched feature branch overlaps the two existing production files.

## Design

- Add `EnqueueIdentified` with the existing sender/content arguments plus a separate opaque `deviceID`. `EnqueueSent` delegates with empty identity; all other insertion methods continue delegating through it.
- Add `DeviceID` to `QueuedMessage`, including `Snapshot`, `SnapshotAll`, `DeliveryMessage` and `OnDelivered`. Preserve the existing snapshot policy for other sender/attachment fields.
- Optional `Config.OnAccepted` receives `(convID, QueuedMessage)`. Optional `Config.OnTerminal` receives `(convID, QueuedMessage, TerminalOutcome)`. The typed outcomes are delivered, removed and give-up. `SentNow` remains true for successful send-now confirmation. Neither projection contains delivery bytes or resolved attachment paths.
- Give each internal message shared lifecycle state under `q.mu`: acceptance completion, one claimed terminal outcome, removal intent and whether an idle attempt is outstanding. The resolution claim and FIFO mutation share the lock. A completed outcome is immutable and emitted once, either by its resolver or by the enqueue caller after acceptance completes.
- Enqueue copies attachment IDs before insertion; rejection creates no acceptance and consumes no ID. After unlocking it invokes acceptance, marks acceptance complete under the lock, and publishes any deferred terminal notification. Ordinary delivery can proceed during acceptance, but terminal and legacy delivered callbacks wait for acceptance completion.
- `Remove` still immediately removes/cancels the waiting head. When an idle attempt is outstanding it defers the terminal claim until that attempt returns. Successful confirmation wins even without a commit claim; an error resolves explicit removal. A removal between attempts or of a non-head resolves directly.
- Send-now success and actual give-up claim their terminal outcomes. Failed/refused operations and stale give-up claims do not resolve anything. Preserve legacy notification APIs; `OnDelivered` fires before the new delivered terminal callback, using separately copied projections.

## Concurrency model

Keep the single queue mutex and existing per-conversation drains, with no new goroutine or observer lock. All callbacks execute off-lock and may re-enter `Snapshot`/`SnapshotAll`; observers must be concurrency-safe and return promptly. Acceptance runs on the enqueue caller; a deferred terminal and `OnDelivered` may also run there. Outstanding removal resolves on the drain. `Run` retains its drain join, FIFO pacing and spawn/close synchronization. Lifecycle state dies with the message rather than accumulating in a registry.

## Error handling

Existing delivery errors, pending exemptions and per-head retry windows remain unchanged. Capacity rejection returns zero. Shutdown alone emits no terminal fact and retains the documented confirmed-write observation gap. Observer callbacks have no error return, as with existing seams; durable writer recovery belongs to #2972.

## Testing strategy

Write tests first and observe failure for the missing API. Use injected seams and gated callbacks to prove acceptance finishes before terminal/legacy delivery, including enqueue-before-Run, immediate delivery and concurrent removal. Table scenarios cover delivery across retries, removal with and without outstanding attempts, gate-claimed refusals, send-now success/failure/refusal, give-up and stale give-up, plus shutdown leaving unresolved messages. Compare all metadata across projections, mutate input/observation attachment slices, and re-enter both snapshot methods from callbacks. Join test Run goroutines before final counts. Run `go test -race ./internal/msgqueue/...`, `go vet ./...` and `go build -o /tmp/builder-2970/pyry ./cmd/pyry`; the verifier owns the full-module gate.

## Open questions

None. Exact API spelling and callback scheduling are specified above; production wiring and durability remain later tickets.

## Documentation handoff

Satisfied in [the package overview](../../knowledge/features/msgqueue-package.md), whose “Exported surface”, “Delivered notification (#2115)” and “Concurrency model” sections link to [the API](../../knowledge/features/msgqueue-package-api.md) and [lifecycle/concurrency](../../knowledge/features/msgqueue-package-lifecycle.md). These document the additive identity/lifecycle API, empty identity for legacy callers, copied safe projections, acceptance-before-terminal ordering, the three outcomes and delivery winning a removal race. The shutdown observation gap is retained, and per-run queue IDs are distinguished from durable acceptance linkage owned by the history writer.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `EnqueueIdentified` treats device identity as opaque metadata, never authorization or deduplication; `queued.message` omits composed delivery payload and clones attachments. Identity provenance is OUT OF SCOPE here and owned by #2971.
- [Tokens, secrets, credentials] No credential generation/storage is added; device ID must be an identifier, not a token. New code never logs it or client content.
- [File operations] No filesystem access: attachment IDs are copied strings and host paths remain private delivery bytes.
- [Subprocesses] Existing injected delivery seams remain unchanged; no process launch or shell interpretation is added.
- [Cryptography] Identity is copied rather than minted or compared, and existing transport authentication is unaffected.
- [Network and I/O] No new network surface; the existing per-conversation rejection cap remains the insertion boundary. Observers receive bounded queue facts, not a second retained backlog.
- [Errors, logs, telemetry] Terminal outcomes use fixed constants, without forwarding delivery errors, payloads or paths. Existing content-free logging remains intact.
- [Concurrency] SHOULD FIX: claim outcomes while holding the same mutex that clears outstanding attempts, or a Remove after confirmation could report a false drop. Defer callback publication until acceptance completes; clone each observer's attachment slice. Tests force both windows.
- [Threat model] Prompt content remains opaque transit under the existing protocol threat model. No new authentication/replay guarantee is implied. Durable recovery and shutdown reconciliation are OUT OF SCOPE, owned by #2972.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-08

## Revisions

- 2026-10-08: Build review made confirmed FIFO advance part of the attempt-completion/outcome-claim critical section. Acceptance can finish concurrently and publish a deferred `OnDelivered`, so advancing afterward could expose the confirmed item in its re-entrant snapshot. Shutdown is decided in that same section before advance; the documented confirmed-write gap remains. Final written work is about 766 lines, with three new exported types and no consumer migrations, within all sizing limits.
