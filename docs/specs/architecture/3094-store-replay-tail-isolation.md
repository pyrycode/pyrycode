# Replay/tail isolation reliability (#3094)

## Files read

- `internal/thread/store_test.go` → `TestStoreReplayTailIsolation`, `testStoreSignal`, `testStoreWait`, `testThreadStore`: existing barriers, assertions and reverse-order shutdown cleanup.
- `internal/thread/store.go` → `Store.run`, `Store.publish`, `Store.Shutdown`: replay finishes before checkpoint, query preparation and Tail; worker completion joins under lifecycle ownership.
- `internal/thread/cache.go` → `Store.checkpoint`, `writeCacheFile`: complete synced checkpoint precedes publication.
- `internal/thread/fold.go`, `child.go`, `observations.go` → `Fold.Feed`, `resolveChildren`, `recordShown`: per-entry item traversal grows with an all-message replay fixture.
- `internal/thread/fold_test.go` → `testMessage`: every original replay fact creates another public item.
- `internal/history/forward.go` → `ForwardReader.Walk`, `Tail`: bounded real callbacks, captured upper bound and registration-before-catch-up.
- `docs/knowledge/features/thread-package-background-store.md` § Store verification: preserve real readers and exact replay/handoff/tail accounting; preparation differs from bounded request work.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: cleanup must release gates before joining owners; overlay measurements preserve the branch.
- `CODING-STYLE.md`: test concurrency, cancellation and contract comments.

## Context

Baseline triage reported the five-second handoff barrier expiring after replay release. #3086 is merged, including query preparation during publication; its recorded blocker is closed. No other remote feature branch touches the planned test file. A focused baseline passes, but measured first-chunk folding alone takes about 1.52 seconds of its 1.67-second release-to-handoff interval. The original fixture grows 4,098 items and repeats full replay comparisons. Diagnose under the unchanged parallel suite before finalizing the cause; do not increase shared timeouts or change production folding.

One deliverable: reliable isolation proof with actionable finite failures. Expected written work: approximately 230 lines including this plan; zero exported types, zero migrated consumers, three acceptance criteria, fewer than ten test failure phases. No decision record is needed.

## Design

Keep the real history reader and `history.MaxPageEntries + 2` durable replay entries. If parallel phase measurements confirm folding dominates, use a bounded set of actual message items at the beginning and across the chunk boundary, with unsupported durable facts filling the remaining raw entries. Every ID must still be consumed in order exactly once; meaningful messages during replay, handoff and subsequent tail must still match independent full replay. This removes incidental item-growth cost without changing the bounded-reader/progress contract.

Add diagnostics local to `TestStoreReplayTailIsolation`: replay callback progress, replay completion, checkpoint completion and Tail entry. Name each barrier; distinguish a worker that exits from one still processing a named phase. Keep the existing five-second bound. Measure release-to-handoff and its phases in verbose test output. Verify duplicate Load preserves the same worker identity.

Have the append/load isolation goroutine return errors and its appended entry over a buffered result channel rather than calling fatal helpers or mutating the main goroutine's expected-entry slice. Check B's usable publication on the test goroutine while A remains gated. Shared cache/observation helpers retain their behavior.

## Concurrency model

The production worker remains the sole reader/fold owner. A mutex guards test phase evidence and consumed IDs; waiters copy scalar evidence before reporting. Gates use channels and cancellation. Cleanup releases replay and tail gates, cancels the test-owned context, and joins the isolation goroutine before the existing store shutdown joins workers. No sleeps replace barriers.

## State transitions and identity reuse

| Event | Race test and assertion |
| --- | --- |
| Initial replay pauses before feed | `TestStoreReplayTailIsolation`: rebuilding and no premature usable baseline. |
| Duplicate Load during paused replay | Same test: original worker identity retained. |
| A append and B load during pause | Same test: writer finishes and B reaches usable progress before A resumes. |
| Replay releases into checkpoint/publication/Tail | Same test: finite named barrier; handoff snapshot equals only captured replay bound. |
| Appends during replay, handoff and later Tail | Same test: exact ordered IDs in bounded chunks; full replay equality at each boundary. |
| Returned snapshot mutated | Same test: stored item/content remains equal to full replay. |
| Assertion aborts while gated or appending | Same test's cleanup releases gates, cancels contexts and joins goroutines. |

## Error handling

Reader/fold/checkpoint failure must appear as worker completion with the last test-observed phase, not a generic barrier timeout. Timeouts include phase and consumed progress without dumping content. Append/load errors return to the test goroutine. Production error behavior stays unchanged.

## Testing strategy

Measure the original fixture using an external overlay under focused and parallel race runs. Record durations and outcomes, including any exceeded original deadline. After committing this plan, add diagnostic/assertion changes before changing the fixture, then run them to demonstrate the original phase behavior. Run `go test -race ./internal/thread -run '^TestStoreReplayTailIsolation$' -count=20`, `go test -race ./internal/thread ./internal/history`, `go vet ./...` and `go build` for `cmd/pyry` with its output outside the worktree. Preserve parallel tests. The dispatcher owns the standard `make check` gate; full-gate reliability remains pending until that executed gate passes.

## Open questions

- Does measured parallel delay belong to folding, checkpoint or publication? Resolve with phase evidence before the implementation handoff; revise the design if folding is not the cause.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries / threat model] Test-only changes; `Store.run` and `Fold.Feed` retain existing history ownership and decoding checks. No network or CLI input path changes.
- [Tokens / cryptography] No credentials, token generation or epoch rules change; tests use synthetic entries.
- [File operations] Real history/cache operations retain containment, no-follow checks, synced atomic replacement and existing modes. Scratch measurements live outside the worktree.
- [Subprocesses / network and I/O] No subprocess or socket is added to the test; real reader chunks stay bounded by `history.MaxPageEntries`.
- [Errors / logs] Diagnostics report phase, counts and durations only, avoiding history payloads and paths.
- [Concurrency] Gates must be cancellation-aware; cleanup must release before joining. Buffered isolation results avoid a blocked send after an assertion aborts. Phase data and IDs share a mutex; no mutex is held across folding, filesystem work or waits.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-10

## Revisions

### 2026-10-10 — phase measurement and stronger partial replay

Resolved the open question with measured original-fixture evidence: focused overlay folding took 1.52s of a 1.67s handoff; the unchanged parallel thread/history suite took 2.24s folding and 2.37s to handoff. The instrumented original fixture separately measured 2.03s folding, 142ms checkpointing and 3ms publication. These runs completed successfully, so the historical five-second expiry was not reproduced; the evidence identifies incidental fixture folding as the dominant delay, rather than showing worker failure or a broken barrier. `resolveChildren` and `recordShown` traverse growing item collections for each replay fact. Bound the public fixture to four messages while retaining all 4,098 committed raw facts and exact ordered delivery assertions. No timeout increases or production changes are needed.

Move the replay pause after the first successful feed but before its callback returns. This proves an actually partially folded replay still exposes no publication, with meaningful message items on both sides of the raw chunk boundary. Assert that replay delivered at least two bounded chunks. Capture duplicate worker identity explicitly. Snapshot progress waits retain their original finite helper and receive named phase log context; shared helpers remain untouched.

An external overlay forcing A's checkpoint replacement to fail produced the expected `replay to tail handoff: worker exited; phase=checkpointing replay consumed=4098 chunks=2 state=3 version=0` failure and exited normally after cleanup. This verifies diagnostics distinguish checkpoint failure from elapsed folding rather than waiting out the generic barrier deadline.

Final-code failure overlays also verify cleanup: forced checkpoint replacement fails immediately with the checkpoint phase (0.83s total); a reader held until cancellation fails at the finite initial barrier deadline and joins normally (5.59s total, within a 15s process timeout). The final focused race run executed and passed all 20 repetitions without retrying failures. Successful replay handoff phases are now measured in milliseconds rather than seconds.

### 2026-10-10 — verifier finding 1: await successful delivery bookkeeping

`Store.run` publishes Tail progress inside the reader's feed callback, before the test wrapper records successful delivery. Publication therefore does not synchronize the wrapper's final count. A cancellation-aware external overlay pausing after the final successful feed reproduced `consumed 4100, want 4101` while all snapshot assertions passed; cleanup joined normally.

Signal successful bookkeeping by closing and replacing a notification channel under the same mutex as `seen`. After observing the final publication, atomically capture the consumed count and current notification. If the count is incomplete, await that notification using the existing five-second, worker-aware `final tail delivery bookkeeping` barrier before asserting exact count/order. The sole worker has completed every earlier callback before publishing the final chunk, so one notification completes the remaining bookkeeping. The predicate and notification share a mutex to prevent missed wakeups; no lock spans feed, filesystem operations or waits. Successful-feed and bounded-chunk checks remain unchanged.

Validate with a deterministic external overlay that holds final bookkeeping until the test has observed publication and captured the incomplete count; release that gate immediately before the new finite wait. A second overlay holds bookkeeping until cancellation to prove the new deadline and cleanup. Re-run the 20 focused race repetitions, parallel thread/history packages, vet and build. The dispatcher re-runs the standard `make check` gate on the pushed rework; its prior green applies only to `9b349075`.

The forced-gap overlay executed and passed once, explicitly observing `consumed=4100 want=4101` before releasing bookkeeping. The held-gate overlay failed at the named five-second bookkeeping deadline with `phase=paused final tail bookkeeping consumed=4100 chunks=3 state=2 version=4101` and completed cancellation/join cleanup in 7.281s under a 15-second process timeout. These deliberate failure checks are separate from the unmodified passing test runs.

Security review remains PASS: notifications carry no payload, no production input or I/O path changes, and bookkeeping closes channels only while holding its existing mutex. Failure cleanup cancels the worker and joins through store shutdown. Total written work remains below 800 lines; no exports, consumer migrations or acceptance criteria are added.

## Documentation handoff

- Pending documentation stage: update `docs/knowledge/features/thread-package-background-store.md` § Store verification with the bounded public-item fixture retaining 4,098 raw replay facts, pause after successful partial folding, duplicate-worker identity proof, named phase diagnostics and release/cancel/join cleanup.
- Pending documentation stage: carry forward the measurement lesson in that section: distinguish raw reader-bound coverage from incidental growing-item folding cost, and distinguish published progress from completion of test-owned bookkeeping.
