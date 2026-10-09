# Accepted sends and delivery or loss

## Files read
- `cmd/pyry/main.go` → `runSupervisor`: startup precedes queue and relay producers; owns the single store.
- `cmd/pyry/reply_suggestion.go` → `suggestionEnqueuer`: must preserve identified enqueue and invalidation.
- `cmd/pyry/operator_message_history.go` → `newOperatorMessageHistory`: safe text projection and legacy append/push.
- `cmd/pyry/send_now.go`, `cmd/pyry/queued_message_placement.go` → `attach`, `write`, `takeQueued`: placement can precede acceptance observation and delayed confirmation.
- `cmd/pyry/startup_history.go` → `reconcileStartupHistory`: complete reads before closure and restart divider.
- `cmd/pyry/conversation_history.go`, `cmd/pyry/history_projection.go` → `appendConversationHistory`, `historyEntryShown`: best-effort append and closed legacy vocabulary.
- `cmd/pyry/channel.go` → `channelPostSession`: bound-source observation, explicit none only for an empty binding.
- `internal/msgqueue/lifecycle.go`, `queue.go` → `accept`, `notifyTerminal`, `QueuedMessage`: safe callbacks, delivery wins removal, IDs reset per run.
- `internal/history/log.go` → `Page`, `AppendWithMetadata`: raw backward pages and durable IDs.
- `docs/knowledge/features/history-package-producers.md`, `msgqueue-package.md`: captured receiving source and in-memory durability boundary.
- `docs/knowledge/features/development-verification.md`: late callback assertions require callback completion; placement order must remain on the drain.
- `docs/knowledge/decisions/042-daemon-built-thread.md`: existing log is the durable fact source; no queue replay after restart.

## Context
Record one durable identity per accepted send and its outcome for a future thread. Existing delivered messages remain the delivery-order record. This implements ADR 042 without introducing a fold or protocol. No overlapping feature branch touches the planned files after fetching origin.

## Design
A daemon-local `queuedSendHistory` owns acceptance references keyed by conversation and queue ID. Acceptance writes `send_accepted` with safe text, device/app identity, attachments, queue time and reported send time. Placement waits for acceptance observation to finish (including a failed append), then writes the existing operator message and a `send_delivered` fact linking successful acceptance and operator entry IDs. Removal/give-up writes `send_dropped` linked to acceptance. The map is retired at placement or drop; queue IDs never link across runs. Legacy callbacks without this observer remain compatible.

`send_lost` is startup-only, linked to the original acceptance entry with reason `daemon_restart`. Read all raw pages before any closure; collect resolved durable references and unresolved acceptances, then append losses before the divider. Failed reads infer nothing; failed writes stay unresolved for the next attempt. No new facts enter publication, replay or legacy pages. Acceptance/delivery/loss are shown; drops are hidden.

## Concurrency model
Acceptance and delivery/drop may run on different callers and the stream drain. A mutex protects reference slots; each slot has a completion channel for the synchronous acceptance append. Never hold the observer mutex while waiting or publishing. Blocking placement preserves stream order; acceptance never waits on the stream drain. No new goroutines. Existing placement goroutines retain context shutdown.

## State transitions and identity reuse
| Event | Race-detector coverage |
| --- | --- |
| Acceptance races stream placement; echo/idle precedes confirmation | `TestQueuedSendHistoryPlacement` |
| Ordinary/send-now delivery, removal/refusal, retries/give-up/shutdown | `TestQueuedSendHistoryLifecycle` |
| Restart, reopened store, queue-ID reuse, repeated recovery | `TestQueuedSendHistoryStartup` |
| Acceptance/loss append failure and retry; incomplete read | `TestQueuedSendHistoryFailures` |
| Equal app IDs on different devices and legacy enqueue | `TestSuggestionEnqueuerIdentity` |

## Error handling
Nil/failed storage never changes queue behavior or best-effort legacy push. No acceptance ID means no linked terminal fact. Log append/read failures through existing fixed event/reason discriminants, never raw errors, text, device IDs or paths. Unknown source stays unavailable; an actually empty binding is explicit none.

## Testing strategy
Write real-store queue/placement/reopened-log tests first and observe failure. Assert fields, linked durable IDs, metadata, ordering, exactly-once publication, legacy exclusion and watermark behavior. Run `go test -race ./cmd/pyry/...`, `go vet ./...`, and build `./cmd/pyry` outside the worktree. Full-module gate belongs to verifier.

## Open questions
None. Sizing: one deliverable, at most 800 written lines (about 300 production, 420 tests, 65 plan), no exported types, at most 5 changed wiring call sites, 5 acceptance criteria, fewer than 10 error/reject branches.

## Documentation handoff
Pending documentation stage:
- `docs/knowledge/features/history-package-producers.md`, “Producers (#2114, #2115)”: acceptance fields, durable acceptance/outcome and delivery-order linkage, captured provenance/visibility, legacy exclusion and best-effort write limitations.
- `docs/knowledge/features/msgqueue-package.md`, “Durability boundary (in scope vs out)”: queue remains in memory, IDs reset per daemon run, startup visibly records unresolved surviving sends without re-enqueueing; intentional removal/give-up hidden and successfully recorded loss not repeated.

## Security review
**Verdict:** PASS
**Findings:**
- Trust boundaries: safe `QueuedMessage` excludes delivery bytes; authenticated device identity forwards through `suggestionEnqueuer`, without deduplication or authorization changes.
- Tokens: no credential generation/storage changes; opaque sender IDs enter facts only and never logs.
- File operations: reuse store containment, modes and append semantics; no new paths or separate persistent log.
- Subprocesses and cryptography: no new subprocess, shell, key, nonce or cryptographic operation.
- Network/I/O: no new network reads; existing handler bounds remain. Startup walks bounded raw pages and retains only references/source needed for closure.
- Errors/logs: fixed event/reason discriminants only; malformed new facts fail recovery rather than infer losses from an incomplete interpretation.
- Concurrency: per-slot readiness gates early placement; callback append does not depend on stream progress. Failed writes release readiness too.
- Threat model: existing paired-device authorization and host-path separation remain; durable queue and gap-free crash persistence are outside ADR 042's approved slice.
**Reviewer:** builder (self-review)
**Date:** 2026-10-09
