# Conditional invalid push registration removal (#3436)

## Files read
- `cmd/pyry/push_wake.go`: `wakeAbsent`, `run`, `Trigger`; retain eligibility, ordering and coalescing.
- `cmd/pyry/relay.go`: `startRelayV2`; wire feedback and the instance registry path.
- `cmd/pyry/push_wake_test.go`, `channel_post_completion_test.go`: constructor consumers and existing behavior assertions.
- `internal/relay/connection_feedback.go`: `SendPushWakeFeedback`, `consumeWakeResult`; request-local, generation-scoped notifications.
- `internal/relay/connection_feedback_test.go`: upstream ordering, expiry, capacity, malformed and duplicate proof.
- `internal/devices/registry.go`, `device.go`, `lock.go`: `Reload`, `Save`, `UpdatePushRegistration`, `WithLock`, all-value device snapshots.
- `internal/relay/handlers/register_push_token.go`: locked registration writer; advisory deletion must abandon reload failures instead of self-healing.
- `docs/knowledge/features/relay-package-push-wake.md`: feedback channels close without notifying on retirement/overflow.
- `docs/knowledge/features/relay-package-handlers.md`: reconcile/mutate/save must share one file-lock region.
- `docs/knowledge/features/devices-package.md`, `devices-registry.md`, `devices-registry-reload.md`: membership comes from disk; surviving fields come from memory; Save does not roll back.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`, `docs/protocol-mobile.md`: concurrency, evidence and correlated-result contracts.

## Context
Dead push registrations consume wake budget. Clear only the registration submitted by this daemon for the same pairing, without removing pairings or newer registrations. #3435 is merged. Relay #213 and coordinated operator activation remain later work. No overlapping remote feature branches touch the planned production files. No decision record is needed.

## Design
Use `SendPushWakeFeedback` in the production waker. Capture a device value at submission; its hash is a local request context and the result's contents never identify a registry row. On a successful send, retain existing coalescing behavior and start a channel consumer. Only a value-bearing notification invokes the injected invalidation callback with the captured snapshot. Channel closure or cancellation does nothing.

Add `Registry.ClearPushRegistrationIfMatch(tokenHash, platform, pushToken string) bool`: compare all three and clear only Platform/PushToken under one `Registry.mu` hold. It reports whether Save is necessary, including false for an already-empty registration.

Keep the persistence consumer beside the waker. Inside one bounded (250 ms) `devices.WithLock` region, check cancellation, Reload, conditionally clear and Save. Check cancellation before acquisition too. Log only fixed event/reason strings, never returned errors or identities. Success is logged only after Save succeeds. No retry or rollback.

## Concurrency model
The existing waker goroutine owns coalescing. Each successful submission has a feedback waiter which exits on request channel closure, notification or daemon cancellation. API pending capacity/expiry bounds channel lifetime; overflow channels are already closed. Persistence follows file lock → registry mutex order. Registration's writer shares the file lock; the conditional method independently closes its in-process compare/mutate race.

## State transitions and identity reuse
| Event | Race-enabled proof |
|---|---|
| A/B held replies, registration replacement, repeated/duplicate invalid result | `TestPushWakeFeedbackPersistence` |
| Revocation, reused display name, pair committed while waiting for lock | `TestPushWakeFeedbackReconcile` |
| Registration update before conditional clear | `TestPushWakeFeedbackPersistence`, `TestClearPushRegistrationIfMatch` |
| Disconnect/reconnect and stale reply | `TestPushWakeFeedbackReconnect` |
| Immediate result, malformed/transient/unknown result, legacy silence, overflow | `TestPushWakeFeedbackRejected`, upstream `TestWakeFeedbackResults`, `TestWakeFeedbackWriteOrdering`, `TestWakeFeedbackBounds` |
| Expiry and cancellation | upstream `TestWakeFeedbackBounds`, `TestWakeFeedbackRetirement`; `TestPushWakeFeedbackFailures` |
| Failed send then next trigger | existing `TestPushWaker` send-failure assertions |

## Error handling
Lock failure/cancellation causes no mutation or Save. Reload failure preserves memory/disk and is contained before logging. Missing identity or changed registration causes no Save. Save failure retains the clear in memory and prior atomic disk file, reports fixed failure text, and never retries a consumed notification. Pair/revoke commits are reconciled inside the file lock.

## Testing strategy
Write tests before implementation and observe missing-method/feedback signature failures. Exercise real `relay.Connect` sockets with canonical injected envelopes and temporary disk registries; use barriers for held results and file-lock reconciliation. Compare full device values and fresh loads, and unchanged disk bytes/inodes for no-Save paths. Include lock/reload/Save failures and distinctive secret markers in captured logs. Run focused race tests, touched-package race tests, `go vet ./...` and `go build ./cmd/pyry`; the verifier owns `make check` and the full-module race gate. No live activation claim.

## Open questions
None. The result is advisory; bounded lock failures and Save failures are consumed without retry.

## Documentation handoff
Pending for documentation stage: `docs/knowledge/features/relay-package-push-wake.md`, **Push-wake surface** / **Daemon-side trigger**: document production adoption, conditional removal, persistence-failure semantics and retained pairing/replacement guarantees.

Pending for documentation stage: `docs/protocol-mobile.md`, **Push-wake publication and activation**: link both implementation children of #3423 and relay #213, retaining #3423 as the family reference. Require completed compatible revisions and mixed-version/matching/stale/connection/expiry evidence before operator activation; publication and hermetic tests alone do not establish it.

## Security review
**Verdict: PASS.** Builder self-review, 2026-10-15.
- Trust boundaries: `consumeWakeResult` validates/correlates replies; downstream uses only the captured local device. Supplied identity is ignored.
- Tokens/telemetry: SHOULD FIX during build: suppress all underlying reload/Save/lock errors; decode errors can contain secret bytes. Add marker assertions. No new identity labels or token logs.
- File operations: use the existing trusted instance path, sidecar lock and atomic 0600 Save; no path is taken from replies. No new file-opening primitive or schema.
- Subprocesses/cryptography: no new subprocesses, credentials, crypto or secret authentication comparisons; equality is registration identity matching.
- Network/I/O: reuse the bounded transport and strict upstream result decoder; consumers never parse wire bytes.
- Concurrency: compare/mutate holds Registry.mu; persistence holds file lock first; each waiter exits on API retirement or daemon cancellation. No rollback can erase replacement state.
- Threat model: relay can advise invalidity only for a live correlated request. Pairing credentials, permissions and key bindings remain intact. Live compatibility/activation evidence belongs to coordinated operator work under #3423 and relay #213.

Sizing recheck: one observable deliverable, approximately 700 total written lines, no new exported type, four constructor consumers plus send wiring, five acceptance criteria, five persistence terminal outcomes; all limits satisfied.
