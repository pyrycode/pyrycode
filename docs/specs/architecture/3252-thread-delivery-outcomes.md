# Queued thread eligibility and ordered delivery outcomes

## Files read
- `internal/relay/v2session_hosted.go`: `hostedDeliver`, `handleHostedDone`, and `retireHosted` own plaintext selection, transport completion and retirement.
- `internal/relay/v2session_hosted_changes.go`: `hostedInbox`, `initHosted`, and `releaseHosted` own the shared 16-slot lane.
- `internal/relay/v2session_thread.go`: `threadWithheld` gates thread content independently of correlation.
- `internal/relay/v2session_appframe.go`: `forwardToRun` reports handoff; existing handler contracts remain unchanged.
- `internal/relay/v2session_push.go`: `drainOnce` preserves bounded upstream capacity and Run-owned sealing.
- `internal/relay/v2session_handshake.go`: `handleNoiseInit` initializes ordered delivery for authenticated sessions.
- `internal/relay/v2session_hosted_capacity_test.go`: `TestHostedPushCapacityDelivery` supplies bounded-lane and progress patterns.
- `internal/relay/v2session_hosted_lifecycle_test.go`: `testHostedBlockedOutput` holds transport with cancellation.
- `internal/relay/v2session_thread_test.go`: `threadConfig` and `threadPlain` supply authenticated fixtures.
- `internal/protocol/thread_query_encoding.go`: `EncodeThreadReply` supplies batches, oversized assembly and the distinct final marker.
- `docs/knowledge/features/v2-session-manager-concurrency.md`: plaintext ordering and session-generation fencing must remain intact.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md`, “Thread delivery and summary projection”: correlation grants no access and withholding must not strand permitted output.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`, and ADR 042 “Update messages”: ownership, authenticated-byte assertions and envelope limits.

## Context
Queued thread output can outlive conversation membership or agent eligibility. Existing handoff and flush signals do not certify transport acceptance. This provides one delivery contract for future query consumers without installing them. No decision record is needed. No other fetched feature branch overlaps the production files.

## Design
Extend thread-content classification to `thread_items` and `thread_reset`, including codec continuations. Recheck the existing gate on the selected unsealed head; remove withheld heads and release their slots before examining later output. Keep open-state and current paired-device checks on Run.

Add `DeliverThread(ctx, connID, envelope) error`, called only off Run. Nil means the ordered transport callback returned success. Sentinel errors distinguish withholding and delivery failure; connection retirement uses `ErrConnNotFound`, and caller/manager cancellation returns cancellation. Admission waits for the existing shared inbox's capacity, then queues detached plaintext with a buffered terminal-result channel. A capacity notification belongs to each inbox, fencing replacement connection IDs. No provider or legacy caller signature changes.

Every delivery-aware envelope uses the same 16-slot reservations as hosted and ordinary traffic. Each admitted envelope is bounded by the thread envelope maximum; Run assigns IDs and checks the fully stamped encoding before Noise sealing. Callers send sequences in order and stop on any non-success before their success marker.

## Concurrency model
Run remains the sole session and Noise owner. Off-Run callers touch only the mutex-protected inbox and wait on capacity/result/cancellation. Capacity notifications broadcast under `hostedMu`; locks are never nested with `pushMu`. The existing transport goroutine exits through its bounded delivery context, also canceled by the caller. Buffered results never block Run. Inbox cancellation releases connection and manager waits.

## State transitions and identity reuse
| Event | Race-detector coverage |
| --- | --- |
| Conversation removal or Codex eligibility change while unsealed | `TestThreadDeliveryLateEligibility` |
| Held transport, full lane, capacity release, oversized assembly and final marker | `TestThreadDeliveryOrderedReply` |
| Transport failure and down/up recovery | `TestThreadDeliveryFailure` |
| Caller cancellation before admission, queued or active | `TestThreadDeliveryCancellation` |
| Disconnect, replacement identity and manager shutdown | `TestThreadDeliveryCancellation` |

## Error handling
Withholding consumes neither sender ID nor nonce and releases capacity. Encoding, sealing or transport failures return non-success and retain existing session teardown policy. Retirement and cancellation never become success on recovery. Detached result channels make late callbacks harmless. Legacy paths retain their existing behavior.

## Testing strategy
First add focused regressions and observe their failure. Use authenticated hosted/thread fixtures without a query handler. Decrypt output, compare reconstructed oversized content and final-marker order, assert stamped envelope bounds and progress on another connection and manager controls. Run `go test -race ./internal/relay/...`, `go vet ./...`, and `go build ./cmd/pyry`; the verifier owns the full-module gate.

## Open questions
None. The delivery contract certifies local transport acceptance, with no client acknowledgement.

## Documentation handoff
- Pending documentation stage: `docs/protocol-mobile.md`, “Thread query messages”: describe ordered delivery outcomes and eligibility rechecks; enqueue/assembly completion alone cannot certify a query. Keep query handlers/provider installation pending #3253/#3236 and pages #3235.
- Pending documentation stage: `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md`, “Thread delivery and summary projection”: queued thread content is rechecked before sealing, including correlated batches/parts/completion/reset; legacy behavior is preserved.

## Security review
**Verdict:** PASS
- Trust boundaries: `threadWithheld` checks source conversation rather than request correlation for every thread-content type. Missing identity fails closed.
- Tokens: `hostedAuthorized` rechecks the paired registry immediately before sealing; no credential generation/storage changes.
- File operations and subprocesses: none in the delivery path.
- Cryptography: only Run stamps and seals selected plaintext; withheld and canceled unsealed output spends no nonce. Ambiguous failed sealed sends retire the connection.
- Network/I/O: shared 16-slot admission and maximum stamped plaintext size bound retained content; existing delivery timeout bounds transport workers.
- Errors/logs: fixed sentinel classifications and content-free existing logs; no payloads or secrets logged.
- Concurrency: inbox-generation cancellation fences reused connection IDs; result delivery cannot block Run, and caller cancellation reaches active transport.
- Threat model: relay sees only authenticated ciphertext; query admission and provider installation remain out of scope for #3253/#3236.
**Reviewer:** builder (self-review)
**Date:** 2026-10-12

Sizing: approximately 700 written lines (230 production, 390 tests, 80 plan), zero new exported types, zero required consumer migrations, four acceptance criteria, eight reject/lifecycle branches. All five limits remain within the builder ceiling.

## Revisions
- 2026-10-12: Existing outbound-only fixtures have no ordered callback. Initialize the thread lane on handshake only with `OutboundContext` (or the existing hosted configuration), preserving those legacy fixtures and paths. `DeliverThread` reports `ErrConnNotFound` when no ordered lane exists. Terminal codec error envelopes can also use the delivery contract, with thread/interactive checks and stamped size enforcement. Manager shutdown reports connection retirement; caller cancellation retains its context error. Added `TestThreadDeliveryActiveCancellation`, `TestThreadDeliveryCapabilityGates`, and `TestThreadDeliveryStampFailure` for the final contract.
