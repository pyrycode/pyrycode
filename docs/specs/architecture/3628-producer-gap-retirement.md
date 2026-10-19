# Distinguish producer-gap retirement from byte overflow

## Files read

- `internal/relay/v2session_livestate.go`: `RetireLiveState`, `pushRetired`, and live drain quarantine.
- `internal/relay/v2session_modal.go`: `pushQueue` and `enqueueSource` own the synchronized terminal latch and byte ceiling.
- `internal/relay/v2session_push.go`: `pushSource`, `handlePushOverflow`, and `drainOnce` bridge producer signals to Run-owned teardown.
- `internal/relay/v2session.go`: close codes, `Run`, `closeWith`, and `teardown` establish single-owner cleanup and nil-frame closing.
- `internal/relay/v2session_livestate_test.go`: `liveFixture` and `TestRetireLiveState` provide authenticated deterministic admission.
- `internal/relay/v2session_thread_retirement_test.go`: `TestRetiredThreadQueueDoesNotDrain` proves drain-before-teardown quarantine.
- `internal/relay/v2session_hosted_capacity_test.go`: `TestHostedPushAdmissionRetirement` forces retirement during admission.
- `internal/relay/v2session_push_test.go`: `TestV2Session_Push_ByteCeilingTearsDownSession` proves actual-byte overflow through Run.
- `internal/relay/v2session_handshake_test.go`: `TestV2CloseCodes_MatchSpec` pins published numeric codes.
- `docs/knowledge/features/relay-package.md`, `v2-session-manager.md`, and the push-queue-byte-ceiling topic: nil-frame teardown avoids spending a Noise nonce on a down transport.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md`: quarantine must precede Run teardown.
- `docs/knowledge/features/v2-session-manager-concurrency-hosted-discovery-and-ordered-delivery.md`: final queue read must preserve quarantine across off-lock hosted admission.
- `docs/knowledge/features/development-verification.md` and `CODING-STYLE.md`: behavioral proof, synchronized ownership, and content-free logging.

## Context

Producer continuity loss currently uses the byte-overflow latch and reports 4413 despite no byte rejection. This ticket supplies one independently useful behavior: accurate terminal classification while preserving immediate quarantine. Publisher continuity repair and diagnostics belong to sibling tickets. No decision record is needed.

Remote branch #3240 overlaps the queue/push files with additive permission observations; no new API or rewritten state-machine block is required from it. Keep edits local.

## Design

Keep the existing `overflowed` terminal quarantine latch so every current drain and snapshot guard remains effective. Add a queue-owned `producerGap` discriminator, set only by `RetireLiveState` when the terminal latch was previously clear. A byte-ceiling rejection retains the default byte-overflow cause. Both fields are immutable once latched. `enqueueSource` ignores subsequent pushes on a latched queue, preserving the first cause and retained accounting.

Keep `pushOverflow` as the shared teardown wake and its existing API. `handlePushOverflow` reads the cause under `pushMu`, then logs and closes on Run: producer gap uses `StatusProducerGap = 4414` and Warn event `v2.push.producer_gap.teardown`; actual overflow retains 4413 and both existing ceiling events. Include `conn_id` and `close_code`; close with a nil frame. Unknown or non-open sessions remain no-ops. Independent hosted-lane capacity closures retain their behavior.

Sizing after plan: approximately 400 total written lines, zero new exported types/interfaces, zero caller API migrations, four acceptance criteria, fewer than ten reject branches. Regression and quarantine tests prove this same behavior and remain together.

## Concurrency model

No new goroutines, locks, or channels. Producers set the latch and cause atomically under `pushMu`; Run alone mutates sessions and Noise state. Release `pushMu` before logging, signaling, or closing. Preserve context-aware retirement notification and nonblocking push notifications. Test Run goroutines are cancelled and joined before session inspection or fixture cleanup.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Open session retires without byte rejection; Run closes 4414 and removes queue/session | `TestPushRetirementCause` producer-gap arm |
| Byte ceiling rejects; Run closes 4413 | `TestPushRetirementCause` overflow arm; `TestV2Session_Push_ByteCeilingTearsDownSession` |
| Overflow then retirement, or retirement then enough pushes to overflow an unretired queue | `TestPushRetirementCause` ordered arms |
| Duplicate/concurrent retirement and notification | `TestPushRetirementCause` duplicate/concurrent arms; require one terminal close and event |
| Drain runs first with queued and dependent live output | `TestRetiredThreadQueueDoesNotDrain`, `TestRetiredLivePendingDoesNotDrain` |
| Retirement arrives during hosted admission | `TestHostedPushAdmissionRetirement` |
| Already-cancelled retirement, absent connection, or retirement after removal | `TestRetireLiveStateErrors`, `TestRetireLiveState` |

The cause belongs to the queue instance and is discarded on teardown; a replacement queue starts with neither latch set. Existing routing-ID signal behavior is unchanged.

## Error handling

Preserve context errors before mutation, `ErrConnNotFound` on missing queues, and cancellation while notifying Run. No new errors escape to producers. Latched queues cannot resume draining even when notifications duplicate. Cleanup and cancellation use existing `closeWith`/`teardown` unchanged.

## Testing strategy

Write classification tests first and observe the current 4413 misclassification. Use authenticated fixtures, actual enqueue byte rejection, JSON slog records, and Run's outbound close, checking literal numeric codes and absent contradictory events. Assert nil close frames, one close, queue/session removal, session-work cancellation, and post-teardown producer errors. Retain drain-first and hosted-admission proofs; add pending live reading/snapshot quarantine and pre-cancelled/missing connection checks.

Run affected relay tests with race detection on Linux through test admission, then `go vet ./...` and `go build ./cmd/pyry`. Full gates belong to the dispatcher/verifier; macOS is outside scope. No live Claude is required.

## Open questions

None. The current refined issue explicitly chooses first-trigger precedence.

## Documentation handoff

Pending for documentation stage:

- `docs/protocol-mobile.md`, “Error codes”, WS close-code table and adjacent recovery prose: 4414 means producer handoff/continuity gap requiring reconnect and reconciliation; 4413 continues to mean actual push-queue byte ceiling. Explain fresh Noise handshake, transient-state reconciliation and explicit thread catch-up after 4414, and include producer-gap teardown among subsequent 4410 session-gone causes.
- `docs/protocol-mobile.md`, “Daemon thread updates (v2, supplied delivery contract)”: replace “existing queue-overflow gap teardown” with producer-gap retirement at 4414.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md`, paragraph “Retirement quarantines before teardown”: update the same classification while preserving the quarantine contract.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: retirement accepts only an existing queue identity through the daemon API; no network parsing or authorization boundary changes.
- Tokens/secrets: cause is a boolean and close code; no credential generation, persistence, rotation, or disclosure changes.
- File operations/subprocesses: neither production path accesses files or starts subprocesses.
- Cryptography: `closeWith` receives nil, so retirement spends no send nonce; all session/cipher mutation remains on Run.
- Network/I/O: existing bounded queue and transport paths remain; latched queues reject new retention rather than growing during teardown delay.
- Errors/logs: static messages, event names, connection routing ID and numeric close code only; no payloads, history, model output, credentials, or raw errors. Tests check structured records and nil close frames.
- Concurrency: latch and discriminator change under the same `pushMu` hold; first trigger cannot be relabelled. No new workers or nested locks; preserve final hosted-admission quarantine check.
- Threat model: this changes diagnostic classification for an already-terminal authenticated session, preserving isolation, fresh-handshake recovery and content-blind relay handling. No new relay trust or retention boundary.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-19

## Revisions

- 2026-10-19: reviewing duplicate notifications exposed that a routing-ID-only wake can outlive its original queue. `handlePushOverflow` must ignore a missing or unlatched queue, so a replacement without a terminal trigger is not classified as overflow. `TestPushRetirementStaleNotification` covers this boundary; the existing synchronization and notification API remain unchanged.
