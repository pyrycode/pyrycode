# Bound queue actions

## Files read

- `internal/msgqueue/queue.go` → `Remove`, `SendNow`, `EnqueueIdentified`: mutation lock and per-run counters.
- `internal/msgqueue/lifecycle.go` → `messageLifecycle`, `accept`: shared acceptance state and off-lock observers.
- `cmd/pyry/queued_send_history.go` → `accepted`: durable acceptance entry identity.
- `cmd/pyry/main.go` → `runSupervisor`: queue/history installation before traffic starts.
- `cmd/pyry/reply_suggestion.go` → `suggestionQueueSender.SendNow`: preserve invalidation only on success.
- `internal/protocol/messaging.go` → `DequeueMessagePayload`, `SendQueuedNowPayload`: additive wire fields.
- `internal/relay/v2session_modal.go` → queue command handlers: silent control operations.
- `cmd/pyry/queued_send_handle_test.go` → `TestQueuedSendHandleActions`, `TestQueuedSendHandleQueries`: encrypted fake-daemon queue/history seams.
- `cmd/pyry/daemon_thread_queries_test.go` → `testDaemonThreadQueryWire`: thread-capable transport harness.
- `docs/knowledge/features/msgqueue-package-lifecycle.md`: acceptance observers run off-lock; terminal outcomes wait for acceptance.
- `docs/knowledge/features/msgqueue-package-send-now.md`: failed writes reinsert the same lifecycle; preserve that binding.
- `docs/knowledge/features/protocol-package.md`, `relay-package.md`, `thread-package.md`, `development-verification.md`, `CODING-STYLE.md`: package boundaries and validation conventions.

## Context

Queue numbers are unique only within a conversation and daemon run. The durable accepted history entry is also the thread item's ID, so it disambiguates a reused number after restart. #3324 is merged. One deliverable: optionally bind both existing queue actions to that acceptance.

Sizing: approximately 450–600 total written lines including tests and this plan; no new exported types/interfaces; three production consumers updated additively; four acceptance criteria; fewer than ten new rejection branches. All five limits hold. Overlap: #3240 adds permission observation to push queues in `v2session_modal.go`, separate from both queue-action handlers; build through with local edits.

## Design

Both payloads gain optional `item_id` as a raw JSON field to distinguish omission from invalid/null input. Supplied values must be positive uint64 JSON integers, at most 20 encoded bytes. Invalid supplied bindings cause a silent early return, without logs or replies. Omission retains the existing calls.

`Queue.BindAcceptedItem(convID, queueID, itemID)` records a nonzero daemon-held identity once on the live entry's shared lifecycle under `q.mu`. `queuedSendHistory.accepted` calls an optional injected binder after successful append and before publishing acceptance; `runSupervisor` installs the binder before starting the queue. Failed appends leave bound actions unavailable, while legacy actions remain available.

Add `RemoveItem` and `SendNowItem`, sharing existing mutation implementations with legacy wrappers. Their item comparison and mutation occur in the same `q.mu` critical section. No registry of retired bindings exists. Relay handlers use additive narrow capability assertions; absent support never falls back to an unbound action. `suggestionQueueSender` forwards the bound capability and invalidates only on success.

## Concurrency model

No new goroutines. The binder and guarded actions use the existing leaf `q.mu`; callbacks and I/O remain off-lock. Acceptance can race delivery/removal: binding only an entry still in the FIFO is safe, and outcomes retain existing acceptance ordering. A failed send-now retains the shared lifecycle and identity on reinsertion. Existing daemon/queue cancellation and joins stay intact.

## State transitions and identity reuse

| Event | Race-enabled regression |
| --- | --- |
| Restart reuses queue 7: item 100 lost, item 205 accepted | `TestBoundQueueActionsRestart` tests each stale action and each matching action through encrypted thread connections. |
| Delivered/dropped entry receives repeated action | `TestBoundQueueActionsRestart`, `TestQueueBoundActions` assert silence and no replacement mutation. |
| Two devices share message_id, distinct accepted items | `TestBoundQueueActionsDevices` checks only matching identities act. |
| Binding during acceptance, immutable rebinding, concurrent guarded actions | `TestQueueBoundActions` and `TestQueueBindingDuringAcceptance` cover shared lifecycle and single successful mutation. |
| Failed send-now and retry | `TestQueueBoundActions` verifies reinsertion retains binding. |

## Error handling

Missing, mismatching or retired queue entries return false with no mutation/notification. Supplied null, zero, string, negative, fractional, boolean, object, oversized or overflowing IDs are rejected silently. An acceptance append failure produces no binding. Existing write errors reinsert and return false. Nothing echoes or logs the item binding.

## Testing strategy

Write focused hermetic regressions first and confirm failure. Queue tests exercise atomic binding, races and failed writes; relay tests validate malformed bindings, missing capabilities and never-log behavior; fake-daemon tests use actual acceptance history and encrypted queue commands across restarted history. Existing unbound tests prove compatibility and thread transport assertions detect unsolicited queue_state. Run `go test -race` on `internal/msgqueue`, `internal/protocol`, `internal/relay` and `cmd/pyry`, plus `go vet ./...` and `go build -o /tmp/builder-3340/pyry ./cmd/pyry`. The verifier owns `make check`.

## Open questions

None. Wire name is `item_id`, matching the accepted thread item ID. It is a selector, not an authorization credential.

## Documentation handoff

Pending documentation stage: `docs/protocol-mobile.md`, **Queue (v2)**: document optional `item_id` on `dequeue_message` and `send_queued_now`, why clients should always send it, mismatch as a silent no-op, and legacy behavior when omitted.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: relay validates raw `item_id` before any action; only `queuedSendHistory.accepted` supplies the recorded durable identity, before publishing it. Client text, message_id, device and ordering never establish binding.
- Tokens/secrets and cryptography: item ID is public history metadata, not a secret or capability; existing paired Noise authentication remains authoritative. No new credentials or crypto.
- File operations: reuse existing successful history append; no new client-selected paths or persistence.
- Subprocesses: reuse existing SendNow seam; binding supplies no command or process argument.
- Network/I/O: existing encrypted frame admission bounds remain; supplied item encoding is additionally capped at 20 bytes before integer parsing.
- Errors/logs/telemetry: malformed bindings return silently; never log binding bytes or parser errors. Mismatch logs only existing queue metadata, with no reply or broadcast.
- Concurrency: comparison and queue mutation share `q.mu`; no check-then-use window, new nested locks or goroutines. Binding is immutable and tied to live per-run state.
- Threat model: paired devices retain existing conversation-wide control rights; this fixes stale selector confusion without treating device/message_id as authorization. Client use is owned by Mobile #1986 and Desktop #1906.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-14

## Revisions

- 2026-10-14: `TestQueueBindingDuringSendNow` exposed acceptance racing a legacy send-now take and failed reinsertion. A FIFO-only binder cannot find that entry. Retain each accepting lifecycle in `convQueue.accepting` until its callback returns, then release the map entry (and empty map). The binder can therefore update the same lifecycle while it is temporarily outside the FIFO. Guarded actions still match only live FIFO entries, and no retired binding is retained after acceptance completes.
- Final sizing remains below 800 written lines (approximately 750 including plan, tests and replaced comments), with no new exported types/interfaces or changed legacy signatures. All five limits still hold.
