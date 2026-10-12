# Requested thread catch-up with a safe live join

## Files read
- `internal/relay/v2session_seams.go` → `V2SessionConfig`: additive query seam.
- `internal/relay/v2session.go` → `dispatchAppFrame`: authenticated admission and bounded worker enqueue.
- `internal/relay/v2session_appframe.go` → `appFrameJob`, `appFrameWorker`: off-Run work and connection cancellation.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: thread reconciliation and legacy replay separation.
- `internal/relay/v2session_thread.go` → `threadWithheld`: independent item authorization.
- `internal/relay/v2session_thread_delivery.go` → `DeliverThread`: serial delivery outcomes and generation fencing.
- `internal/relay/v2session_hosted.go` → `hostedDeliver`: last eligibility check and single-owner sealing.
- `internal/relay/v2session_hosted_changes.go` → `hostedInbox`: bounded shared delivery state.
- `internal/relay/v2session_push.go` → `pushSource`: concurrent live-update admission.
- `internal/protocol/thread_query_encoding.go` → `EncodeThreadReply`: authoritative coverage, whole items and separate completion.
- `internal/thread/queries.go` → `QueryResult`: detached usable/reset/nonusable results.
- `cmd/pyry/daemon_thread_relay.go` → `daemonThreadItem`: exhaustive item mapping pattern.
- `internal/relay/v2session_hosted_test.go` → `testHostedStartCaps`: authenticated encrypted fixtures.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md` § Thread delivery and summary projection: late authorization, supplied live-state bypass and outcome requirements.
- `docs/knowledge/features/relay-package.md`, `v2-session-manager-concurrency.md`, `protocol-package-thread-messages.md`, `thread-package-background-store.md`, `development-verification.md`: ownership, detached query coverage and test boundaries.

## Context
Apps explicitly request one registered conversation's changes. Thread peers need authoritative catch-up rather than event-ring replay. Production provider installation stays in #3236; pages stay in #3235. No additional decision record is needed: the existing ADR permits explicit query failure on a live race.

## Design
Add a context-aware CatchUp seam returning the existing detached `thread.QueryResult`. Admission on Run validates required field presence/types, canonical conversation identity, current pairing, thread/interactive capability, registry and agent eligibility, and normalized limit. Correlatable denials return a generic error; absent/invalid envelope identity closes the connection.
One query per connection owns a cancellable token in its generation-specific hosted inbox. Provider, mapping and codec run on the connection worker. Deliver every encoded envelope serially through `DeliverThread`; any rejection stops the sequence and attempts a content-free terminal error. Reset produces only `thread_reset`. Preserve backend coverage independently of item revisions.
A live update admitted during an unfinished query cancels its token. Already queued live updates do the same at the last sealing boundary. No additional live buffer is introduced. At the separate successful marker's sealing boundary, atomically check cancellation and install the captured epoch/version watermark, then release the query token. The existing ordered lane places later updates after the marker. Matching-epoch updates at or below captured version are suppressed, including fragments; later versions remain in source order. Bound watermark state to 256 conversations per connection and reject additional new joins explicitly.
Thread handshakes skip replay/resync while keeping supplied live-state reconciliation. Legacy handshakes/history remain unchanged. #3240 overlaps the manager, handshake and push files only with additive permission-observer instrumentation; no dependency.
Sizing: one deliverable; approximately 320 production + 390 tests + 70 plan lines, no more than one new exported interface, no consumer migrations, five acceptance criteria, ten grouped rejection/lifecycle outcomes.

## Concurrency model
Run owns connection identity, IDs, Noise and late delivery selection. `hostedMu` alone guards query tokens and completed watermarks; never nest it with `pushMu`. Provider cancellation derives from inbox lifetime. Worker and serial delivery escape on connection/manager cancellation. Live producers cancel tokens under `hostedMu` before taking the existing push lock.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Held capture races live admission; retry after failure | `TestThreadCatchUpLiveRace` |
| Between-batch update, including interrupted fragmented assembly | `TestThreadCatchUpInterrupted` |
| Completion cut, represented updates, later same-version multiple patches | `TestThreadCatchUpJoinedLive` |
| Eligibility loss while delivery is queued | `TestThreadCatchUpInterrupted` |
| Transport failure/down-up, disconnect, manager cancellation, conn-ID reuse | `TestThreadCatchUpInterrupted`, existing `TestThreadDeliveryCancellation` and `TestThreadDeliveryFailure` |
| Repeated named queries and empty/reset/nonusable captures | `TestThreadCatchUpResults` |

## Error handling
Malformed/admission, busy/capacity, missing/nonusable/provider error, codec failure, live race, withheld output, cancellation, transport/retirement and malformed uncorrelatable envelope are explicit terminal outcomes. No payload/source error is logged or echoed. A disconnected connection receives no completion.

## Testing strategy
Decrypt authenticated delivery with the existing hosted fixture. Table-drive result/admission cases, assert full fields and independent coverage, and use cancellable provider/transport barriers for races. Reuse delivery capacity/oversized proofs. Run relay race tests, module vet and binary build; the verifier owns the full-module gate.

## Open questions
None. The permitted fail-on-race policy deliberately trades retry frequency for bounded state and a simple ordering contract.

## Documentation handoff
Pending documentation stage: `docs/protocol-mobile.md`, “Thread query messages” and “Reconnect / Backfill semantics”: catch-up admission/limits, terminal failure/reset versus successful completion, explicit requested-conversation scope and query/live ordering; production provider installation pending #3236 and history pages pending #3235.
Pending documentation stage: `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md`, “Thread delivery and summary projection”: thread-only replay/resync bypass, query delivery gates and unchanged legacy behavior.

## Security review
**Verdict:** PASS
- Trust boundaries: Run validates explicit fields and canonical registry membership before provider access; hosted delivery rechecks current eligibility for every part and completion.
- Tokens/credentials: reuse current pairing checks and Noise; no credential generation/storage or exposure is added.
- Files/subprocesses: query seam accepts IDs and detached data only; no filesystem or process operation is introduced.
- Cryptography: IDs and Noise sealing remain exclusively on Run; cancellation after sealing retains existing retirement behavior.
- Network/I/O: reuse bounded inbound frames, worker queue and count/byte-bounded output; joined metadata has a 256-conversation cap.
- Errors/logs: only generic correlated errors; no request/provider content or source error text.
- Concurrency: atomically fence live admission against final selection under hostedMu; do not nest queue locks; cancellation follows generation-specific inbox ownership.
- Threat model: paired authenticated peers alone reach queries; registry/agent gates prevent foreign conversation disclosure. Production-provider storage security remains #3236.
**Reviewer:** builder self-review. **Date:** 2026-10-12.

## Revisions
- 2026-10-12: `ThreadCatchUp(context.Context, protocol.ThreadCatchUpPayload) (thread.QueryResult, error)` is an additive function field, requiring no exported interface or provider migration. The worker's context is already fenced by the generation-specific inbox.
- 2026-10-12: Updated `TestV2Session_ThreadReplay` to require an untouched replay watermark. Its earlier advancement assertion conflicts with this ticket's explicit replay bypass. Added `TestThreadCatchUpRequestedScope` for a gap-producing hello and simultaneous legacy history/resync, plus `TestThreadCatchUpProviderShutdown` for blocked-provider cancellation.
- 2026-10-12: Written work slightly exceeds 800 lines (approximately 820 after this revision). #3253 has parent #3234 and grandparent #2963; the grandchild rule requires building through the overage. Recorded `needs-human:sizing` and the independently checkable replay-bypass/catch-up split proposal on the issue.
- 2026-10-12: Self-review found that initializing an ordered lane solely to refuse a legacy request would close an Outbound-only peer without its owed error. `TestThreadCatchUpLegacyRefusal` covers the existing Run-owned forwarding path for those content-free refusals; accepted query sequences still require cancellable ordered delivery. The interrupted transport case now also reconnects after down/up.
- 2026-10-12: Verifier finding 1: `cmd/pyry/relay_guard_test.go` → `inboundTypes` / `excludedTypes` must classify the implemented `TypeThreadCatchUp` dispatch arm as `switch-intercepted`, regardless of pending production provider installation. Move the entry between the maps; `TestEveryInboundV2TypeHasHandler` covers dispatch registration and disjoint classification. No production design or security boundary changes. Re-run the guard regression, race tests for the touched packages, module vet and binary build; the dispatcher owns the complete gate rerun.
