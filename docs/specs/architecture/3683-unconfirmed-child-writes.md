# Preserve unconfirmed writes across child exit

## Files read
- `cmd/pyry/queued_message_placement.go`: `write`, `takeQueued`, `idle`; readiness currently substitutes for confirmation.
- `cmd/pyry/send_now.go`: `attach`, `echo`, `take`; delayed callbacks must acknowledge retired managed writes.
- `cmd/pyry/stream_turn_drain.go`: `startStreamTurnDrainV2`, `streamTurnEnvelope`; parsed tail precedes exit.
- `cmd/pyry/runtime_boundary.go`: `publishRuntimeBoundaries`, `exitForSessionTag`; teardown is not normal completion.
- `cmd/pyry/streamsup_runner.go`: `newStreamRunnerFactory`; bind child ownership on both output and exit.
- `internal/streamsup/runner.go`, `runner_turns.go`: `setStdin`, `takeStdin`, `turnTargetWithGate`, `WriteUserTurn`; exact selected pipe identifies its child.
- `cmd/pyry/queued_send_history.go`, `operator_message_history.go`: acceptance and intermediate written facts already preserve recovery identity.
- `cmd/pyry/ordinary_queue_placement_test.go`, `operator_delivery_provenance_test.go`, `streamsup_runner_exit_test.go`: delayed callback, identical payload and real factory fixtures.
- `docs/knowledge/features/msgqueue-package-lifecycle.md`: successful-write callback is not a consumption guarantee.
- `docs/knowledge/features/history-package-producers.md`: accepted/written records remain unresolved until placement, startup loss never replays.
- `docs/knowledge/features/streamsup-package.md`, `development-verification.md`, `CODING-STYLE.md`: runner lifecycle, discriminating checks and goroutine cleanup.

## Context
One behaviour: a queued Claude message cannot gain delivery confirmation from death or replacement of its receiving child. Ordinary and Send now are entry paths to this invariant, not separate deliverables. Keep original unresolved acceptance (`queued`) or intermediate Send now (`written`) after abandonment, with no automatic replay. Startup may later record the existing `daemon_restart` loss, meaning missing confirmation rather than proven non-consumption. No decision record is needed.

## Design
Identify each receiving child by its actual stdin pipe object, captured with the target under the runner lock. A context observer hands that opaque identity to placement before the write. Retain the last child's identity after stdin retirement so the factory can stamp parsed events and exit with the same identity. This avoids treating a Runner.Run activation or reused routing ID as a child generation.

Production placement observes ordered envelopes with that identity. Echo matching requires the same child, conversation and digest. Only a normal TurnEnd supplies completion evidence. Ordinary writes commit there; Send now records completion evidence but still waits for its carried-close grace readiness. A readiness waiter cannot confirm without that evidence. Exit retires that child's remaining entries; lifecycle teardown retires the previous source's entries. Retirement removes matching candidates while retaining only callback bookkeeping until OnDelivered acknowledges them. Failed writes keep their existing queue retry behaviour.

Legacy fixtures without child identity remain usable through existing echo/idle entry points; production always stamps the opaque child identity. No wire or history schema changes. Diagnostics remain content-free.

No overlapping feature branch changes were found in the four placement/drain files after fetching origin. Related #3682 watchdog wiring is independent of the factory's child ownership binding.

## Concurrency model
The runner mutex captures stdin and its identity together; no callback runs under it. The stdout forwarder captures ownership before offering output, and OnChildExit runs after its parsed tail. Placement's mutex protects candidates, completion and retirement. It is never held across write completion waits, history I/O or publication. The write outcome channel publishes the captured identity and write result. Existing readiness goroutines exit on idle or daemon cancellation; no additional worker is needed.

## State transitions and identity reuse
| Event | Race coverage |
| --- | --- |
| Ordinary successful unread pipe write, then child exit | `TestQueuedPlacementUnreadPipe` |
| Send now write while busy, then exit (including after completion during grace) | `TestQueuedPlacementUnreadPipe`, `TestQueuedPlacementOwnership` |
| Normal echo or no-echo TurnEnd before exit | `TestQueuedPlacementOwnership`, existing ordinary placement controls |
| Teardown, successor with reused session ID, late predecessor echo/result | `TestQueuedPlacementOwnership` |
| Delayed OnDelivered after retirement, identical payload on successor | `TestQueuedPlacementOwnership` and existing identical-write control |
| Failed write retries; daemon cancellation | existing failed-write control, `TestQueuedPlacementOwnership` |

## Error handling
A failed pipe write removes its registration and returns its original error for queue retry. Successful unconfirmed writes are never re-enqueued. Cancellation abandons confirmation. History remains best effort with its existing safe projection and append failure handling.

## Testing strategy
First add a real-pipe regression through the production factory, placement, queue callbacks, stream drain, durable history and publication. The child blocks on a separate synchronization input and never reads stdin. Assert successful small write and unresolved original accepted identity after exit using a drain barrier. Add positive normal completion/echo and ownership/teardown/grace controls; retain existing delayed callback and identical payload assertions. Run affected Linux race tests, `go vet ./...` and `go build ./cmd/pyry`. Full gates belong to the verifier; no live Claude proof is required.

## Open questions
None. The selected recovery outcome is unresolved accepted/written history without replay.

## Documentation handoff
Pending documentation stage:
- `docs/knowledge/features/history-package-producers.md`, **Accepted sends and linked outcomes (#2972)**: pipe success and child exit cannot establish delivery; abandoned writes retain unresolved accepted/written identity without automatic replay; daemon-restart loss describes missing confirmation rather than proven non-consumption.
- `docs/knowledge/features/msgqueue-package-lifecycle.md`, **Delivered notification (#2115)**: distinguish the queue's successful-write notification from placement's echo/normal-completion confirmation.

## Security review
**Verdict:** PASS
- Trust boundaries: MUST FIX addressed in design: child-authored echoes must match the receiving pipe as well as conversation/digest; successors cannot confirm retired input.
- Tokens/secrets: opaque pipe identity stays in memory, never logs or wire; no credential handling changes.
- File operations: existing safe history projection/append remains unchanged; test files use temporary directories only.
- Subprocesses: production command construction is unchanged; fake child uses the TestHelperProcess pattern and is killed/joined during cleanup.
- Cryptography: existing SHA-256 digest is only payload matching, not authentication; no new cryptography.
- Network/I/O: no new endpoints, readers or unbounded input; existing limits apply.
- Errors/logs: preserve fixed discriminants; never publish composed payloads or host paths.
- Concurrency: outcome channel publishes target/result before drain comparison; retirement and candidate selection share placement's mutex; no lock across history I/O.
- Threat model: this change strengthens remote-history truthfulness and conversation isolation; permission identity #3681 and stall detection #3682 remain separate.
**Reviewer:** builder self-review. **Date:** 2026-10-20.

Sizing recheck: approximately 740 total written lines (240 production, 420 tests, 80 plan), zero new exported types/interfaces, at most six simultaneously updated consumers, three acceptance criteria, fewer than ten reject branches. One useful behaviour under behaviour-slices-v1.

## Revisions
- 2026-10-20 rework: Findings 1 and 2 preserve the existing single confirmation invariant. Capture the accepted-output floor again at the selected stdin observer, before assigning child ownership; output accepted while target selection is pending cannot confirm that write. Retain saturated exits by receiving pipe as well as producer incarnation, so every child's placement retires behind its accepted tail. `TestQueuedPlacementSelectionFloor` covers delayed target selection for identical echoes and normal closes; `TestQueuedPlacementSaturatedChildExits` covers two exits sharing a producer while Send now is in grace. The incremental repair is estimated below 200 lines, with zero exported types or interfaces and three production consumers. No overlapping feature changes were found in these placement/drain files.
- Security recheck for this repair: PASS. Child output remains untrusted confirmation evidence until both its selected-target floor and pipe ownership match. Per-child exit retention uses opaque in-memory writer identity, with no new logging, credentials, wire fields, filesystem access or subprocess construction. Existing placement/offer locks remain short; no I/O occurs under them and no new goroutine is introduced. The named regressions exercise ordering and shutdown. Finding 3 requires diagnosing the named offline recorded scenario while preserving its modal and stored-file assertions; any further design change will be recorded here before implementation.
- 2026-10-20: The ownership interface is `streamsup.WithTurnTarget(ctx, func(io.Writer))` for the exact selected write target and `Runner.ChildTarget() io.Writer` for current/last-child attribution. `turn_target.go` owns these contracts. Runtime boundaries and confirmed stops also capture the target so an old teardown cannot retire a replacement's placement. Boundary capture may take the runner's leaf mutex under `offerMu`; no runner-locked path calls the sink, and observers run outside the runner lock.
- 2026-10-20: Added an accepted-output sequence floor to reject already-queued echoes/completions. A normal same-child result after registration remains sufficient even for a result-only harness. The provenance fixture's Send now idle control now supplies normal completion before releasing readiness. The real pipe fixture waits for helper readiness and proves a different pipe after an actual same-session respawn. Final size is approximately 635 added lines, with no new exported type/interface and no constructor migration.
- 2026-10-20: Retirement inspects the selected target under the placement mutex without waiting for write completion. Waiting on every candidate's outcome would block the stream drain behind an unrelated successor's blocked pipe write. `TestQueuedPlacementRetireDoesNotWaitForSuccessorWrite` covers this shutdown/identity overlap; the outcome channel still gates successful confirmation.
