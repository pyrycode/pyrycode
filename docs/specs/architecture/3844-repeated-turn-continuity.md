# Preserve repeated-turn publication continuity

## Files read
- `cmd/pyry/thread_shadow.go` → `follow`: status precedes publication and quiescence; unconditional unload can retire newer progress.
- `cmd/pyry/daemon_thread_relay.go` → `publishThread`, `daemonThreadPublisher.publish`, `gap`: serialized publisher cursor and fail-closed range acquisition.
- `internal/thread/store.go` → `run`, `publish`, `Unload`: worker-owned progress can advance between coordinator operations.
- `internal/thread/store_checkpoint.go` → `tailCheckpoints`: one worker feeds/captures each borrowed tail chunk, with cancellation and joined forwarding.
- `internal/thread/publication.go` → `publicationCapture.capture`, `internal/thread/observations.go` → `itemDifference`: affected rows and actual withdrawal evidence.
- `internal/thread/observations_store.go` → `Changes`: only retained publication boundaries prove live continuity.
- `cmd/pyry/accepted_delivery_test.go` → `TestAcceptedDeliveryLiveRefusal`: real producer and fold barriers; durable accepted identity stays authoritative.
- `cmd/pyry/daemon_thread_idle_append_test.go` → `TestDaemonThreadIdleAppendKeepsConnections`: clean reload and original complete/compact connections.
- `internal/e2e/recorded_claude_test.go` → `runRecordedClaude`; `recorded_scenario_drivers_test.go` → `runTapeTwoTurn`, `turn`: owned completion and matching acknowledgements.
- `docs/specs/architecture/3725-live-accepted-delivery.md`: accepted delivery establishes identity before publication; do not restore delayed joins.
- `docs/knowledge/features/thread-package-background-store-lifecycle.md`, `thread-package-background-store-replay.md`: joined retirement and exact clean checkpoint boundary are essential; epoch equality alone is insufficient.
- `docs/knowledge/features/thread-package-shadow-lifecycle-and-evidence-publication.md`: recipient gates, explicit recovery and metadata-only acquisition.
- `docs/knowledge/features/e2e-harness.md`, `development-verification.md`, `CODING-STYLE.md`, `docs/protocol-mobile.md` § Security model: focused native proof and authenticated source isolation.

## Context
One behaviour: accepted consecutive turns retain their original publication
connection. The prerequisite merged at `556f9e8277b75e2a1d86b4bc24edf28129201f43`.
The original #3740 failure remains failed; its builder bundle is retained under
`/tmp/builder-3844/original-failure/`. A named recording on this base passed,
which does not resolve the intermittent failure. No decision record is needed.

The inspected coordinator permits a newer consumed boundary to retire after an
older publisher cursor was acquired. Prove that boundary with real history and
follower barriers before implementing. No overlapping feature branch touches
the proposed production files. Estimate: 180 production, 350 tests/evidence and
110 plan lines, below 800 total; zero exported types, one consumer of a new
method, three acceptance criteria, fewer than ten rejection branches.

## Design
Add `Store.UnloadIfCurrent(id, epoch, version) (bool, error)`: retire only a
usable inactive publication matching the supplied boundary, between completed
feed/capture/publication transactions. A mismatch or in-flight chunk returns
false without canceling, dropping ranges or changing identity. Ordinary forced
`Unload` retains its existing contract. Record worker processing under Store.mu
before a chunk can mutate the fold, and clear it when publication completes;
admission and conditional retirement use that same mutex. No I/O holds Store.mu.

The coordinator attempts conditional retirement under its publication gate using
the actual successful publisher cursor (or captured status without a publisher).
If the attempt declines, following publishes pending progress on the next pass.
Only a successful retirement records the idle/unloaded boundary. Existing
query-demand retirement fencing remains in force. Remove the recorded EOF skip;
do not alter completion assertions or resend inputs. If controlled evidence
disproves this cause, revise this plan before production edits.

## Concurrency model
No new goroutines. The Store worker owns fold mutations, and Store.mu serializes
processing admission with retirement. Coordinator publication gate precedes
Store.mu; no worker takes the publication gate. Existing worker/forwarder
cancellation, checkpoint persistence and joins remain responsible for shutdown.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Second accepted delivery coalesced or separated after publication and before idle retirement | `TestDaemonThreadRepeatedTurnRetirement` |
| Chunk admitted but paused before fold while conditional retirement is attempted | `TestStoreConditionalUnload` |
| Matching idle retirement, reload and another turn on the original peer | `TestDaemonThreadRepeatedTurnRetirement`, `TestDaemonThreadIdleAppendKeepsConnections` |
| Genuine range/epoch loss, row withdrawal, unavailable view | existing Store observation and daemon gap safety tests |
| Two recorded accepted turns and owned terminal idle | `TestRecordedClaude/two-turn` |

## Error handling
Conditional refusal is normal retryable progress, not a producer gap. Persistence
and source errors retain generic errors and existing withdrawal/retirement.
No range, epoch, revision or replacement baseline is fabricated. Reconnect still
requires explicit named catch-up. New diagnostics contain schedule, numeric
source/publication boundaries and fixed reasons only, never content or credentials.

## Testing strategy
Commit this plan first. Write barrier-controlled regressions with the real
accepted-send producer, history follower, Store and daemon publisher; retain
pre-repair JSON identifying the rejected boundary and verify coalesced and
separated schedules. Check actual item identity/text against raw-history replay,
one user delivery per acceptance and no gap/replacement baseline. Exercise
conditional Store retirement including a held chunk, matching retirement and
reload. Remove the skip and run the two-turn recording offline with its JSON
scenario selector. Run affected race tests under shared admission, plus vet and
build on Linux. Retain commands, exact revisions and counted results in focused
testdata evidence. Complete configured acceptance belongs to the dispatcher;
other operating systems are outside this ticket's verification scope.

## Open questions
The controlled regression must confirm whether idle retirement loses a range;
resolve before production edits and record evidence in Revisions.

## Documentation handoff
Pending for the documentation stage: update
`docs/knowledge/features/thread-package-shadow-lifecycle-and-evidence-publication.md`,
**Background discovery and publication**, with the demonstrated repeated-turn
continuity boundary and retained regression. Distinguish provable continuity
from genuine gaps requiring explicit catch-up; describe only the proved cause.

## Security review
**Verdict:** PASS
- Trust boundaries: conditional retirement compares daemon-owned Store/publication metadata; network values cannot authorize continuity. #3725 validation remains unchanged.
- Tokens/secrets: no credential operations; regression records contain numeric boundaries and fixed reasons only.
- File operations: reuse contained history and atomic checkpoint persistence; no new paths, permissions or file mechanism.
- Subprocesses: recorded replay retains existing isolated harness and cancellation; no new execution path.
- Cryptography: no changes to keys, nonces or Noise authentication.
- Network/I/O: existing admission, recipient eligibility, query/live fencing and bounded retained ranges remain authoritative. Conditional refusal grants no connection access.
- Errors/logs/telemetry: keep generic source errors and existing fixed gap reasons; never log source content or credentials.
- Concurrency: SHOULD FIX in build: reject retirement throughout an admitted feed/capture transaction, not merely when the current snapshot differs; otherwise unpublished completed progress could still retire ahead of the cursor. Cover this with a held-fold test.
- Threat model: protocol Security model authentication, replay and conversation-isolation gates remain unchanged; no new user-controlled boundary or deferred security finding.

**Reviewer:** builder self-review. **Date:** 2026-10-24.

## Revisions
- 2026-10-24: `TestDaemonThreadRepeatedTurnRetirement` confirms the planned cause on production base `556f9e8277b75e2a1d86b4bc24edf28129201f43` (plan-only head `6a1a749d`). Both schedules have valid `Changes` before retirement. Coalesced: publisher 6, retired source 13, reload 14; separated: publisher 6, retired source 12, reload 13. Both retain the epoch but reject the older retained range and retire the original peer. `/tmp/builder-3844/before.jsonl` retains 3 executed/0 passed/3 failed/0 skipped test events. This resolves the open question for the demonstrated schedule; it does not claim every historical `baseline_required` has this cause.
- 2026-10-24: `TestDaemonThreadIdleCommit` encoded retirement after a held delivery as expected behaviour. Update its assertion to require the now-proven next addition and zero retirement, preserving its explicit catch-up proof. This is the same defect under the delivery barrier rather than the quiet barrier. The implementation needs two production files plus removal of the recording skip; no checkpoint format or fold semantic change is needed.
