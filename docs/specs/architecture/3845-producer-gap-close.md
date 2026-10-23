# #3845 Ordered producer-gap close and accepted-turn recovery

## Files read

- `internal/relay/v2session_livestate.go` → `RetireLiveState`, `pushRetired`: first-cause quarantine before Run teardown.
- `internal/relay/v2session_push.go` → `handlePushOverflow`, `drainOnce`: terminal cause and pre-seal admission.
- `internal/relay/v2session_hosted.go` → `hostedDeliver`, `handleHostedDone`, `retireHosted`: one active bounded write and cancellation.
- `internal/relay/v2session.go` → `Run`, `closeWith`, `teardown`: completion wake, timeout sweep and terminal adapter.
- `internal/relay/v2session_hosted_changes.go` → `hostedCloseContext`: replacement-generation cancellation.
- `internal/relay/v2session_catchup.go` → `cancelThreadQuery`, `threadQueryGate`: query cancellation, final marker cuts and their hosted-lock admission boundaries.
- `internal/relay/connection.go` → `SendContext`: ordered transport adapter.
- `internal/transport/wssclient.go` → `sendPump`: admitted cancellation retires the shared socket.
- `internal/relay/v2session_hosted_transport_test.go` → `testHostedPumpGate`: hold acceptance before actual write.
- `internal/relay/v2session_recovery_test.go` → `TestThreadRecoveryAfterReconnect`: explicit reset/window recovery and consistent continuation bounds.
- `internal/e2e/relay_v2_handshake_test.go` → `startV2Harness`: authenticated real-socket fixture.
- `internal/e2e/internal/fakerelay/fakerelay.go` → `binaryRecvPump`, `phoneSendPump`: frame-before-close routing.
- `docs/knowledge/features/transport-package.md` → Test surface: cancellation must still retire a failed sealed stream.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md` → Retirement quarantines before teardown: no dependent drain or nonce spending.
- `docs/knowledge/features/v2-session-manager-concurrency-hosted-discovery-and-ordered-delivery.md` → bounded delivery and replacement fencing.
- `docs/knowledge/features/relay-package.md`, `docs/knowledge/features/e2e-harness.md`, `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`, `docs/protocol-mobile.md`: owning contracts and test conventions.

## Context

The retained #3740 log shows gap retirement followed by binary disconnect and failed terminal delivery. Teardown cancels hosted output; cancellation after transport admission closes the binary socket, stranding the following terminal envelope and unrelated phones. The controlled proof must distinguish that boundary from relay routing. This is one behavior: retiring a genuinely discontinuous consumer while preserving accepted-turn recovery. Its regression and recovery proof stay with the repair. Ordinary continuity repair remains #3844. No decision record is needed.

Size: approximately 700 written lines including plan, tests and local helpers; zero new exported types, zero changed consumer signatures, three acceptance criteria, fewer than ten reject branches. The verification floor keeps close and accepted-turn recovery together. Recount before plan commit gives the same limits. Remote #3240 overlaps `v2session_push.go` in separate instrumentation blocks; build through with local edits. #3740's helper migration is unnecessary for the new local fixture.

## Design

`handlePushOverflow` defers genuine-gap teardown while an already-selected hosted write is still live. The existing deadline and completion wake bound this wait. `hostedDeliver` checks the retirement latch before selecting any further output and resumes terminal handling instead. Completion preserves the latched terminal cause even on delivery failure. The timeout sweep uses that same cause for a retired queue. Actual overflow keeps its immediate 4413 behavior.

After the selected write completes, ordinary `closeWith` removes the session and sends the terminal envelope through `Connection.SendContext`. No transport cancellation semantics, authentication gates, query protocol, or publication continuity changes. Reconnect uses a new Noise session, current transient reconciliation, and explicit named catch-up; accepted input is never replayed by this path.

## Concurrency model

Run continues to own sessions, cipher state and selection. Producers only latch queues under `pushMu`. Existing hosted and terminal sender goroutines keep their deadlines and Run cancellation. Waiting for a selected write never blocks Run or unrelated peers. No new goroutine or lock order is introduced.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Gap while selected output is admitted but unwritten; repeated retirement; pending dependent output | `TestProducerGapOrderedClose` and `TestRelayV2ProducerGapRecovery` |
| Selected write fails or times out; transport unavailable | `TestProducerGapDeliveryFailure` plus existing hosted transport cancellation tests |
| Fresh authenticated reconnect, retained checkpoint and reset/window recovery, no accepted-input replay | `TestRelayV2ProducerGapRecovery` |
| Reused routing ID fences old terminal work | existing `TestHostedLifecycleReconnect` |
| Overflow before gap or gap before overflow | existing `TestPushRetirementCause` |

## Error handling

A failed or expired active delivery permits bounded teardown with the original terminal cause; terminal delivery failure stays a failure. Unavailable transport cannot claim receipt. Replacement handshakes cancel old terminal contexts. Queue quarantine suppresses all subsequent selection, including detached current readings, before encryption.

## Testing strategy

Write a held-pump regression first and retain its failing JSON log on the unchanged production revision. Verify actual phone 4414 receipt, no dependent output, and an unrelated admitted peer using the same binary socket. Exercise explicit catch-up from a completed first checkpoint after one accepted second input, with retained and reset/window recovery, source identity, completion and current idle. Use existing fake fixtures and focused Linux race admission. Run affected relay/transport tests, the focused offline integration, `go vet ./...` and `go build ./cmd/pyry`; the dispatcher owns full acceptance. Retain commands, exact revisions and counted results in the PR/evidence.

## Open questions

Resolve the precise held-write failure in the regression before implementing; resolve fixture source and recovery assertions during the test build without changing the product contract.

## Documentation handoff

- Pending documentation stage: `docs/protocol-mobile.md`, **Daemon thread updates (v2, supplied delivery contract)**: ordered 4414 delivery on available transport, bounded failure on unavailable transport, reconnect/catch-up without resending accepted input.
- Pending documentation stage: `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md`, **Retirement quarantines before teardown**: proved delivery boundary and regression.
- Pending documentation stage: `docs/knowledge/features/v2-session-manager-concurrency-hosted-discovery-and-ordered-delivery.md`, paragraph beginning **`HostedAppsChanged` runs under the registry's commit mutex**: callback admission uses `hostedMu` then `pushMu` to fence cancellation against retirement and preserve callback-first 4413.

## Security review

**Verdict: PASS.** Builder self-review, 2026-10-23.

- Trust boundaries: `hostedAuthorized`, thread eligibility and authenticated Noise dispatch remain prerequisites; the repair admits no new plaintext.
- Tokens/secrets: no credential generation, storage or logging changes. Logs retain content-free discriminants.
- Files/subprocesses: no production changes; fixtures retain existing isolated temporary storage and child cleanup.
- Cryptography: Run remains the only cipher owner. Only the already-selected sealed frame can complete; quarantined output consumes no further nonce. Do not weaken transport cancellation retirement.
- Network/I/O: reuse bounded hosted delivery, terminal deadlines and transport frame limits. A stalled socket cannot create an unbounded graceful wait.
- Errors/logs: unavailable terminal delivery remains logged as failure, never proof of phone receipt.
- Concurrency: completion and timeout both re-enter first-cause handling; replacement terminal contexts remain fenced by `hostedCloseContext`.
- Threat model: untrusted relay still carries only encrypted content; this change neither extends device privileges nor trusts client checkpoint/source assertions. No new security finding is deferred.

## Revisions

- 2026-10-23: The held-pump baseline at production revision `c4eba68b` failed `TestProducerGapOrderedClose` with no wire output. This resolves the delivery-boundary question: cancellation of the selected hosted send retires the shared socket before the close. Keep transport cancellation intact and wait only for the selected bounded send.
- The recovery fixture uses the existing fake-Claude executable with production history and thread Store providers. Two authenticated phone sends append source-owned user facts once; fake-Claude output records completion independently of phone teardown. Reset removes only disposable index certificates after unloading, retaining source history and receiving session. These choices resolve the fixture question.
- `hostedEnqueue` also rejects output after the latch, before capacity handling can replace the terminal cause. `TestProducerGapHostedNonceQuarantine` decrypts a post-teardown nonce probe. `TestProducerGapDeliveryFailure` proves unavailable active and terminal sends return failures. The optional configuration argument in `testHostedRealTransport` is an additive local helper edit overlapping #3740's migration; no migrated helper API is required.
- Final sizing remains below 800 written lines, zero exported types/signature consumers, three criteria and fewer than ten reject branches. Full configured acceptance and the documentation handoff remain pending for their owning stages.
- 2026-10-23: The extended nonce regression failed on `a9327dfe`: a thread update rejected by the latched queue still cancelled its admitted query first. `pushSource` must skip query cancellation for an already-retired queue, preserving normal live/query fencing otherwise. Fixture diagnostics also showed that the initial handshake uses context-free output and that supplied-state configurations withhold unsourced turn readings; the tests use a selected error control and do not await a nonexistent contextual handshake acknowledgment.
- Unavailable-connection proof joins Run after `Frames` closes: the active send fails and terminal delivery is suppressed by Run cancellation, rather than requiring a terminal callback after shutdown. Phone baseline overlay now fails with EOF in both recovery arms; repaired retained and reset/window recovery pass.
- The unavailable fixture now closes its listener before dropping the socket, keeping Run alive while reconnect is unavailable. Run shutdown retains its inert map entries, so queue-removal assertions belong to live retirement. This fixture directly checks failed active/terminal sends and queue cleanup without racing a recovered socket.
- 2026-10-23, verifier findings 1–3: Entry checks do not synchronize retirement with cancellation, selection or capacity rejection. Acquire `hostedMu` before `pushMu` for query cancellation and query-marker cuts, checking the latch under both locks through the bounded context cancellation. Commit actual hosted selection (`active` and queue removal) under `pushMu` after all hosted-lock work; release before encryption, delivery or teardown. A frame selected before the latch remains the sole admitted writer. Hosted capacity rejection latches overflow under `pushMu` without replacing an earlier cause, then uses `handlePushOverflow`, including when consuming callback changes. No path may acquire `hostedMu` while holding `pushMu`.
- Controlled race proofs: `TestProducerGapCancellationAdmission` blocks the producer at `cancelThreadQuery`, retires, and verifies query/selected-send survival, selected frame then exact phone-socket 4414, unchanged binary generation and an unrelated authenticated peer. `TestProducerGapSelectionAdmission` pauses selection in `takeHostedChanges` and in `threadQueryGate`; after retirement it checks sender ID and the next Noise nonce. `TestProducerGapCapacityAdmission` pauses full-inbox enqueue and callback overflow before retirement, then checks exact 4414. Retain red evidence on `9e3e27b1` before applying the repair; repeat existing normal query/live fencing, first-cause, transport and phone recovery checks.
- Security self-review for this amendment: PASS. Authentication, eligibility, frame limits, transport cancellation and generation fencing are unchanged. The only nested lock order is `hostedMu` → `pushMu`; no I/O, encryption, teardown or blocking channel operation occurs under `pushMu`. Context cancellation there is bounded and schedules descendant callbacks asynchronously. Selection remains Run-owned; retirement does not touch cipher state. No new goroutine is introduced in production.
- Repair sizing: the existing branch has 783 added lines; these regressions and synchronization changes will exceed 800 total written lines (approximately 1200 overall). This is the same retirement invariant, with no independently useful behavior to split out. The verification floor in `ticket-scope-policy.md` and `handbacks.md` keeps the repair with its controlled proof, so continue building. Zero exported types, zero changed consumer signatures, three criteria and fewer than ten reject branches remain. The only remote overlap is #3240's separate push instrumentation; edits stay local.
- The selection regression also holds the transport-availability probe after `threadQueryGate`, immediately before the atomic `active` commitment, to prove the final selection boundary independently of the query gate's retirement check. Capacity rejection uses the new local `closeHostedOverflow` latch helper; no queue admission needs to hold `pushMu` through encryption or teardown.
- 2026-10-23, verifier finding 1 on `817fa1aa`: `HostedAppsChanged` can cancel the selected delivery's inbox context after producer retirement. Coordinate callback admission and capacity cancellation with the queue latch under `hostedMu` then `pushMu`. Skip already-retired queues; when a live callback exhausts capacity, latch ordinary overflow before cancellation so a later gap cannot replace its 4413 cause. Release both locks before Run teardown or transport I/O. Extend `TestProducerGapCancellationAdmission`'s real-transport/phone proof through actual `HostedAppsChanged` calls, including a callback blocked at admission before retirement and a callback invoked after retirement. Fill remaining capacity using callbacks while the selected write is admitted but unwritten; require uncancelled contexts, selected phone frame then exact 4414, no additional nonce and an unrelated authenticated peer on the original socket. Add callback-first overflow coverage for 4413. Retain red evidence on `817fa1aa` before repairing production.
- Security self-review for the callback amendment: PASS. Authentication and thread eligibility are unchanged; callbacks still inspect only immutable connection keys and mutex-owned inbox/queue data. The existing `hostedMu` → `pushMu` order fences bounded context cancellation; callbacks perform no I/O, encryption, registry re-entry or worker creation. Transport cancellation, deadlines and replacement-generation fencing remain unchanged. No secrets, files, subprocesses or wire format change. This repairs the same retirement invariant and its regression, so the verification floor keeps it here despite the already-recorded line overage; approximately 80 further written lines, zero exports/changed consumer signatures, three criteria and fewer than ten reject branches. Refreshed remote branches have no overlap with these two source/test files.
- The regression-only baseline `910d250f` retains reviewed production `817fa1aa` unchanged. All three leaf scenarios failed (four test events including their parent): both callback schedules cancelled the admitted send and closed the original binary socket; callback-first overflow became 4414. The final regression adds an explicit generation identity check and a nonce probe to the existing authenticated two-phone fixture. Written callback-repair work totals approximately 104 added lines including this amendment and the documentation handoff; the same-invariant verification floor remains applicable.
