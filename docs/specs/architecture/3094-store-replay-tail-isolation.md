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
