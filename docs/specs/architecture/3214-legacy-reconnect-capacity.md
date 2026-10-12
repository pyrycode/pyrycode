# Legacy reconnect snapshot capacity

## Files read

- `internal/relay/v2session_push.go` → `drainOnce`, `forwardEnvelopeSource`: ordinary FIFO handoff and capability/projection gates.
- `internal/relay/v2session_hosted.go` → `hostedEnqueue`, `hostedDeliver`, `handleHostedDone`: ordered plaintext, Run-owned sealing and completion.
- `internal/relay/v2session_hosted_changes.go` → `hostedInbox`, `HostedAppsChanged`, `releaseHosted`: shared reservation and callback retirement.
- `internal/relay/v2session_livestate.go` → `forwardLiveState`, `drainLiveOnce`, `signalLiveDrain`: clear/fresh sequencing and detached readings.
- `internal/relay/v2session_publication.go` → `FlushPushes`: source-queue fence and cancellation.
- `internal/relay/v2session_hosted_transport_test.go` → `testHostedRealTransport`: actual transport acknowledgment barrier.
- PR #3201 source `18a8f70eaadbd2da3b98bd3687120c362fc95d8b` → `TestReadinessLegacyReconnectSnapshotCapacity`: retained counterexample and validation harness.
- `docs/knowledge/features/v2-session-manager-concurrency.md` § Hosted discovery and ordered delivery: reserve callback handoff, queued output and active delivery together; never mix pre-sealed output with ordered plaintext.
- `docs/knowledge/features/development-verification.md`: count executed tests and prove completion barriers.
- `CODING-STYLE.md`, `docs/protocol-mobile.md` § Security model: lock ownership and Noise confidentiality.

## Context

A legal ordinary reconnect batch is larger than the hosted delivery lane. Draining it without destination admission turns transport delay into a 4413 close. One repair preserves ordinary bounded retention while keeping hosted callback overflow terminal. No new exported API or decision record is needed.

Sizing: approximately 600 written lines including tests and plan; zero exported types, zero consumer migrations, four acceptance criteria, fewer than ten new state/reject branches. Production changes are local to relay. Feature #3164 adds overflow guards beside the same push drain; it introduces no required API or function rewrite, so build through the overlap.

## Design

Before removing a queue head, Run reserves one hosted slot under `hostedMu`, separately from `pushMu`. A temporary Run-owned reservation transfers into the next `hostedEnqueue`, without charging twice. If forwarding filters or rejects the head, release the unused reservation. Candidate heads are read again under `pushMu`; producer shedding can change a head but only Run consumes it. FIFO barriers require no slot and cannot pass held predecessors.

A full healthy destination keeps ordinary plaintext upstream. Drain wake eligibility excludes capacity-held sessions. Completion and reservation release wake eligible draining; no polling loop, limit change, transport retry or alternate send path is added. Supplied clear/fresh and detached reading drains use the same one-output reservation so they cannot consume a held ordinary head's capacity or spin behind a full lane. Hosted controls and lifecycle callbacks retain genuine overflow retirement.

## Concurrency model

Run owns all session pointers, temporary reservation ownership, sender IDs and Noise operations. Existing delivery workers keep their deadline/cancellation shutdown path. `pushMu` and `hostedMu` are taken separately, never nested; callbacks reserve atomically under `hostedMu`. No new goroutine is required in production. Test flush goroutines terminate on caller cancellation or connection teardown.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Held handshake fills lane, ordinary FIFO waits, completion resumes without producer push | `TestReadinessLegacyReconnectSnapshotCapacity` |
| Held ordinary fence; another connection and control request progress | `TestHostedPushCapacityHold`, `TestHostedPushCapacityDelivery` |
| Capacity-held supplied fresh reading cannot be overtaken by a FIFO fence | `TestHostedPushCapacityPendingFence` |
| Caller cancellation, disconnect and manager shutdown release held flush | `TestHostedPushCapacityTeardown`, `TestHostedPushCapacityDelivery` |
| Callback competes with ordinary reservation; unused admission returns its slot | `TestHostedPushReservation`, `TestHostedPushReservationTransfer` |
| True callback overflow retires only old generation; same-ID reconnect stays isolated | existing `TestHostedLifecycleOverflow`, `TestHostedLifecycleReconnect` |
| Rekey repeats delivery barrier, queued output remains ordered | existing `TestHostedDiscoveryRekeyOrdering` |

## Error handling

Healthy capacity exhaustion is a hold, not an error/close. Retired or overflowed inboxes still follow existing closure. Filtered or invalid ordinary output releases unused capacity. Failed transport completion remains terminal to prevent a nonce gap. Teardown closes queued FIFO markers; caller cancellation releases its wait without altering already accepted work.

## Testing strategy

Adapt and enable the #3201 counterexample using 64 distinct retained model lists and an interactive-only hello. Hold transport acknowledgment until handshake completion, prove the flush is held, then release and verify exact FIFO payloads, increasing IDs, no duplicate/trailing output and no Claude invocation. Increase only fixture observation-channel capacity to observe the batch. Run this test counted under race before production changes and retain its failure evidence.

Add focused hold/resume, reservation-race and teardown cases; run `go test -race ./internal/relay/...`, `go vet ./...`, and `go build ./cmd/pyry`. The verifier owns the full-module gate.

After hermetic proof, create an isolated validation checkout at harness revision `18a8f70eaadbd2da3b98bd3687120c362fc95d8b` and apply only this repair's production diff. Launch `live-claude-gate.py` selecting `^TestInteractiveStreamQueuedTurnReadiness$/(immediate-prefix|dependent-prefix)$`; record both source revisions and selected leaf executed/passed/failed/skipped counts. #3071 owns diagnostic artifacts; the full host gate and unchanged #2010 mobile gate remain dispatcher-owned.

## Open questions

None. Verify the candidate admission and callback race through focused tests before handoff.

## Documentation handoff

Pending for documentation stage: update `docs/knowledge/features/v2-session-manager-concurrency.md`, **Hosted discovery and ordered delivery**, to state that ordinary bounded pushes wait upstream for ordered-lane capacity and resume on completion, while lifecycle callback overflow retains the shared 16-slot retirement policy. Clarify the existing `FlushPushes` paragraph's capacity hold and that a source-queue fence is not transport receipt.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `forwardEnvelopeSource` retains open-session, capability and projection checks; reservation grants capacity, never authorization.
- Tokens/secrets: no credential generation/storage changes. `hostedAuthorized` still checks paired-device revocation before delivery.
- Files/subprocesses: no production filesystem or subprocess operations are added; authenticated validation uses the restricted launcher.
- Cryptography: no outstanding findings. held ordinary pushes remain unsealed, and only `hostedDeliver` on Run allocates hosted IDs/nonces. No nonce-consuming probe or send bypass.
- Network/I/O: existing upstream byte/count bounds, shared 16-slot destination bound and delivery timeout stay intact. A stalled client holds only its own bounded queue.
- Errors/logs: capacity holds expose no payload, token or ciphertext; existing content-free diagnostics remain.
- Concurrency: no outstanding findings. reserve atomically against `HostedAppsChanged`, transfer once or release once, and never nest queue/inbox locks. Callback overflow may still retire a reserved generation.
- Threat model: protocol threats 2/3 preserve per-generation Noise isolation; threat 7 retains bounded destination admission. Prompt injection and client compromise policy are unchanged by this internal handoff.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-12

## Revisions

- 2026-10-12: the focused `TestHostedPushCapacityPendingFence` exposed a marker overtaking a supplied reading's fresh half while capacity was held. Keep that connection's queued heads gated until `livePending` is admitted, and exclude those markers from wake eligibility. Reservation release also wakes the drain through `releaseHosted`, covering delivery completion and cancelled queued pages without adding a separate completion path. Production touches five relay files; total written work remains below 800 lines.

- 2026-10-12: clarified the security verdict wording: the nonce and atomic-reservation concerns were resolved in the original design before its commit; they are not outstanding MUST FIX findings. The contract and PASS verdict are unchanged.

- 2026-10-12: authenticated prefixes still closed with 4413 when an ordinary inline reply competed with a capacity-held reconnect batch. `TestHostedPushCapacityInlineEnvelope` reproduced the same full-lane retirement. `hostedEnvelope` now reserves inline ordinary admission too, or parks already-projected plaintext in the same byte-bounded FIFO. A ready tag prevents projection/revision checks from being applied twice; a deferred supplied clear queues its fresh reading behind it. Rekey and hosted controls retain their terminal admission policy. `pushQueue.enqueueSource` owns metadata at insertion, preserving delta shedding and byte accounting. Production additionally touches `internal/relay/v2session_modal.go` → `queuedEnv`, `enqueueSource`; no exported API or consumer migration is added.
