# Retire residual daemon content adapters

## Files read

- `cmd/pyry/interactive_turn_v2.go` → `emit`: source append precedes residual banner/receipt publication; constructor remains stable.
- `cmd/pyry/runtime_history.go` → `recordRuntimeFact`, `closeRuntimeSource`: durable lifecycle closure and source identity survive publication retirement.
- `cmd/pyry/legacy_runtime_receipts.go` → `legacyRuntimeReceipt`, `publishLegacyHistory`: remove after their final consumers.
- `cmd/pyry/prompt_answer_history.go` → `recordPromptAnswer`, `boundPromptAnswerProjection`: preserve winning-answer capture and bounded saved context; remove exclusive receipt worker/validation.
- `cmd/pyry/modal_resolve_v2.go` → `resolveCapturedStreamDecision`, `AnswerQuestionDiagnostic`, `RefuseQuestionDiagnostic`: parked registry decides the winning answer before recording.
- `cmd/pyry/channel_delivery.go` → `deliver`, `drain`, `held`: retain chunk reconciliation, completion barrier, recovery, reservations and teardown.
- `cmd/pyry/channel_post_v2.go` → `channelPostTurnEndPayload`, `complete`: completion payload remains durable input; completion hook becomes wake-only.
- `cmd/pyry/relay.go` → `startRelayV2`: retire prompt/post publication installation, preserve constructor results and replay infrastructure owned by later tickets.
- `cmd/pyry/history_projection.go` → `historyVisibilityMetadata`, `historyEntryShown`: classification remains necessary for durable entries.
- `cmd/pyry/prompt_answer_relay_test.go`, `prompt_answer_history_test.go`, `prompt_answer_receipts_test.go`: migrate responsiveness/concurrent-answer proofs away from receipts.
- `cmd/pyry/channel_post_completion_test.go`, `channel_delivery_test.go`, `channel_delivery_session_test.go`, `thread_shadow_legacy_test.go`: preserve completion/recovery/provenance and mixed lifecycle assertions.
- `docs/knowledge/features/history-package-producers.md` and its legacy-compatibility topic: discovery on the answer stack deadlocks relay dispatch; recovery must never infer current provenance.
- `docs/knowledge/features/thread-package.md`, `docs/knowledge/decisions/042-daemon-built-thread.md`: raw content retirement applies to every app; live/catch-up use original sources.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: focused race evidence and tagged compilation are distinct from live acceptance.

## Context

Standalone banners, runtime/prompt-answer receipts and channel posts are the remaining content publication paths after #3734–#3736. Retire them while keeping their source facts and supported controls. ADR 042 already owns this decision; no new ADR is required.

Sizing: three independently useful behaviours would ordinarily split into banner/runtime retirement, prompt-answer retirement and channel-post retirement; adapter removal belongs to its last consumer. Actual native lineage is #3714 → #3718 → #3737. The grandchild exception requires continuing, and `needs-human:sizing` is already applied with those seams. Forecast approximately 900 written lines plus deletions, zero exported types, stable constructors, at most five acceptance criteria and no new reject branches. Rechecked against this plan: exceeds only the written-work ceiling. Feature-branch overlap scan found no other branch touching the planned production files.

## Design

`emit` and `recordRuntimeFact` retain their single marshal/append attempts, values, timestamps, visibility and captured source, then return. They do not consult clients or publish/replay any content. Remove the receipt adapter and exclusive counters. Preserve shared ring allocation/SetReplaySource for #3738/#3739 and ordered live-state/thread publishers.

`recordPromptAnswer` retains its best-effort append after winning authenticated resolution. Remove `promptAnswerPublisher`, its mutex/queue/worker and receipt-only validator. Retain immutable asking-session ownership and the 16 KiB projection bound. Resolution never calls connection discovery for a receipt, including when history fails or context is cancelled.

`channelDelivery.deliver` retains accepted-post carry, chunk order/reconciliation, metadata and durable completion. Retire delta publication hooks. Invoke its completion hook only when a new completion is successfully appended; that hook only triggers the existing asynchronous wake. Cleanup-only retries/reloads do not wake. Keep `newChannelPostEmitterV2` and the relay return signature stable with a no-op announcement seam; remove its exclusive publication fields and worker logic.

## Concurrency model

No new goroutines or locks. Remove the prompt receipt worker. The stream drain remains the sole interactive lifecycle writer. Channel delivery retains its mutex, handler gate, busy/reservation ordering, cancellable consumer and joined teardown. Thread and live-state publication retain their existing ordered owners. Wakes use the existing bounded asynchronous waker.

## State transitions and identity reuse

| Event | Race regression |
| --- | --- |
| Reused source/turn IDs, runtime closure and conversation isolation | `TestRuntimeHistoryEvictionReactivation`, `TestRuntimeHistorySourceClosure`, residual retirement tests |
| Consecutive/duplicate prompt decisions, stale/timeout/teardown, rebinding | `TestPromptAnswerHistoryPermission`, `TestPromptAnswerHistoryQuestion`, `TestPromptAnswerHistoryLosingResolution`, `TestPromptAnswerHistoryRetirement` |
| Stalled delivery/cancellation and next answer | `TestPromptAnswerRelayRemainsResponsive`, `TestPromptAnswerBlockedDeliveryDoesNotBlockNextAnswer` |
| Channel partial write/completion failure, retry, cleanup failure and restart | `TestChannelDelivery_CompletionRequired`, `TestChannelPost_RecordingReplayLiveWakeOrder`, `TestChannelDelivery_SessionRetryRecovery` and existing channel-delivery tests |
| Initial connection, live thread publication and explicit reconnect catch-up | residual content retirement tests and `TestThreadShadowLegacy` |

## Error handling

Absent/failing history never restores raw publication. Existing content-free append warnings remain. A failed channel completion stays pending with no wake; durable completion is the recovery authority. Prompt storage failure cannot re-actuate a resolved approval. Preserve existing authorization/rejection paths and generic diagnostics.

## Testing strategy

Write retirement regressions first and observe failures against old publication. Reuse canonical thread publisher/catch-up comparisons and original source assertions. Replace positive compatibility assertions and compatibility-only skipped tests; retain mixed history, prompt, session, channel, ordering and liveness proofs and shared helpers. Run affected `cmd/pyry` tests with Linux race detection through `scripts/test-admission.py`, plus `go vet ./...`, `go build ./cmd/pyry`, and affected `e2e_realclaude` tagged compilation. Record candidate, commands and executed/passed/failed/skipped counts in PR evidence. Complete configured and applicable real-Claude prompt acceptance remain pending for dispatcher; no full baseline is run here.

## Open questions

None. Retain constructor seams as required; remove exclusive internals. Replay admission/storage retirement remains #3738/#3739; mandatory-thread helper migration remains #3743.

## Documentation handoff

Pending for documentation stage:
- `docs/protocol-mobile.md`: **Application message types**, **Daemon thread updates (v2, supplied delivery contract)** and affected banner/channel-post content sections: all apps stop receiving raw publications/inert receipts; saved facts/completed posts feed authoritative thread items; open prompts, dismissals, controls and live state remain supported.
- `docs/knowledge/features/history-package-producers.md`: **Producers** and **Resolved remote prompt answers**; `docs/knowledge/features/history-package-producers-legacy-compatibility.md`: **Legacy eligibility and explicit visibility**; `docs/knowledge/features/thread-package.md`: **Shadow lifecycle and evidence**: document the same distinction. Do not claim #3738/#3739 replay retirement or #3718 final acceptance is complete.

## Security review

**Verdict:** PASS

**Findings:**
- Trust boundaries: preserve `modalResolverV2`/`questionResolverV2` device authorization and parked registry first-winner arbitration. Source history remains untrusted content folded by existing thread logic; no new route grants access.
- Tokens: retain prompt correlation ownership and existing credential checks; saved projection still excludes tokens, permission inputs and paths. No credential lifecycle changes.
- File operations: reuse history and pending-post stores, their validation and atomic persistence; no new path or mode.
- Subprocesses: preserve child verdict delivery unchanged; no process launch or environment changes.
- Cryptography: no changes to Noise framing, randomness, keys, nonces or comparisons.
- Network/I/O: remove content fan-out; reuse supported bounded relay framing and existing controls. No new sockets, reads or endpoints.
- Errors/logs: retain fixed append event/reason diagnostics; verify canary content does not leak through storage failures.
- Concurrency: SHOULD FIX during build: regress consecutive answers with held discovery and cancelled context, retaining dismissal/winning-result proofs. Remove all receipt delivery and its serialization lock so no connection query remains on the recording stack.
- Threat model: preserve authenticated paired-device answer gates and canonical thread conversation access; removal reduces raw content exposure. Replay admission/storage changes are OUT OF SCOPE in #3738/#3739, and final family/live acceptance belongs to #3718 and dispatcher.

**Reviewer:** builder (self-review)
**Date:** 2026-10-22

## Revisions

- 2026-10-22: `TestLocalPairingProviderWiredFromRelayConstructionToControl` in `relay_guard_test.go` also matched the discarded post callback's local variable name in `runSupervisor`. Update that expected tuple to `_`, keeping all exact pairing-provider/minter wiring assertions. The affected-package run exposed this residual mixed structural assertion; no pairing behavior changes.

- 2026-10-22: Caller compilation exposed the final `runSupervisor` assignment to `channelDelivery.announce` and the `newTestPosterCarrying` test installation. Remove that production assignment while preserving the relay return signature; replace the poster's synthetic fan-out observer with durable chunk inspection. `main.go` → `runSupervisor` and `channel_post_test.go` → `newTestPosterCarrying` extend the files-read map. The interactive counter/ring still serve supported live state and remain for #3738/#3739; keep only that delivery in `interactive_live_publication.go` → `publishLiveState`, after `emit` excludes content, avoiding growth of the already large emitter file. The initial retirement regression failed on old raw/replay publication as intended. Recovery regressions also compare canonical completed post content after partial-write retry and cleanup/restart; all five actual prompt decisions retain canonical notices with asking-session provenance.
