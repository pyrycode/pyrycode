# Reference retry fixture reliability (#3471)

## Files read

- `internal/thread/render_test.go` → `testStoreReferenceRetry`: shared replay/tail failure and repair matrix; current padding depends on the history page bound.
- `internal/thread/render_sends_test.go` → `TestStoreReferenceSendRetry`: outcome references require replay, while acceptance/delivery references also fail on terminal tail.
- `internal/thread/render_endings_test.go` → `TestStoreReferenceEndingRetry`: inherited closure references use the same fixture.
- `internal/thread/store_test.go` → `testStoreReader`, `testStoreAppend`, `testStoreEqual`: real-reader decorators, committed IDs and independent legacy equality including watermarks.
- `internal/thread/store.go` → `Store.run`, `Status`, `Retry`: feed chunk lifetime, worker-owned source, publication and replacement after failure.
- `internal/thread/render.go` → `recentSource.Lookup`, `remember`: chunk precedence and 256-entry/1 MiB FIFO.
- `internal/history/forward.go` → `Walk`, `Tail`, `forwardSegment`: growing active segments are reread under the history mutex on tail catch-up.
- `internal/history/log.go` → `AppendWithMetadata`: ordinary durable history path shares that mutex; preserve its behavior.
- `docs/knowledge/features/thread-package-background-store.md` § Store verification: ignored tails alone cannot trigger dependency failure; armed progress polling must use `Status`.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: prove the intended dependency and lifecycle boundary actually execute.
- `docs/specs/architecture/3094-store-replay-tail-isolation.md`: separate preparation cost from worker and publication waits.

## Context

The MacBook baseline and PR package runs both exhausted 600 seconds, with a padding append active. That stack does not distinguish cumulative package work from a deadlock. Baseline revision is `07b6b35e14aad0de688326fb36d81c54303c691c`; collect complete Linux race results and phase measurements before attributing the timeout. No decision record is needed.

One deliverable: remove incidental fixture work while preserving authoritative-read failure/retry proof. Estimate approximately 300 written lines including plan and evidence, zero exported types, four unchanged helper consumers, three acceptance criteria and no production state-machine changes. Recount before implementation: all five sizing limits hold.

Overlap: `origin/feature/3453` changes retention assertions elsewhere in `render_test.go`; edits here remain local to the retry helper and do not depend on that change.

## Design

Keep real history appends, reader, lookup and Store workers. Replace page-bound padding with the 256 entries needed to evict the recent FIFO. Decorate the real reader's replay callbacks to split chunks at 256 entries, so final replay publication cannot resolve the failed target from the current chunk. Keep production cache and reader limits unchanged.

For every tail arm, wait with `Status` until padding has published before appending the delivery, send terminal or ancestor closure. Require usable status and no injected lookup failure through that boundary. Wait for unavailable worker status before reading a snapshot, and count target misses reaching the authoritative lookup seam. This prevents a polling snapshot from causing the failure itself. Join failed workers before disarming and retrying; repaired replay must equal fresh legacy replay including item revisions, consumed version and last-shown watermark.

Record padding append duration separately from replay, padding catch-up, failure and retry waits. Compare the original and bounded fixtures with the same diagnostics. Preserve all four callers and their existing phase/failure/target combinations.

## Concurrency model

The existing Store worker exclusively owns its fold, reader and recent source. Test lookup arming/counters use atomics; callback timing remains worker-local and is joined before inspection. Test goroutine appends and polls status; no additional goroutine or gate is needed. Existing cleanup shuts down and joins Store workers.

## State transitions and identity reuse

| Event | Race coverage |
| --- | --- |
| Initial replay loses an evicted dependency | All four `TestStoreReference*Retry` families: worker becomes unavailable through authoritative lookup. |
| Padding while failure is armed | Tail arms: usable status reaches the exact padding version with no failed read. |
| Delivery, terminal or ancestor closure follows eviction | Tail arms: worker fails only after the meaningful event, without a rendering poll. |
| Repair and Retry reuse the conversation ID | Every arm: failed worker joined, lookup repaired and fresh legacy equality restored. |

## Error handling

Keep missing and unreadable source injections. Failure exposes only `StateUnavailable`/`ErrUnavailable`, nil items and zero version, watermark and epoch. Status waits retain the existing five-second bound and report the phase. Do not increase package deadlines or alter history durability/cache lifecycle behavior.

## Testing strategy

First strengthen worker/lookup assertions and measure the original fixture under `-race`; then reduce padding and verify replay fails without the chunk decorator as a negative control. Run the complete uncached race-enabled thread package with the unchanged ten-minute timeout, including retention and lifecycle tests. The ticket explicitly requires complete `make check` and `make build`, overriding the ordinary builder restriction on duplicating the full gate. Capture JSON test results using a scratch Go command wrapper without changing recipe flags or test composition; report executed tests/subtests, failures/skips, elapsed times and stages reached. Keep logs/profiles outside the worktree and summarize durable evidence here and in the PR.

## Open questions

- How much baseline time is cumulative suite work, padding append work, reader contention and actual worker waits? Resolve using complete results, instrumented phase durations and profiles; distinguish Linux results from the earlier MacBook evidence.
- Does bounded replay truly miss both source tiers? Resolve with counted authoritative target lookup and a negative control removing chunk splitting.

## Documentation handoff

Pending for documentation stage: `docs/knowledge/features/thread-package-background-store.md` § Store verification — describe bounded retry padding, controlled replay chunks, status-only event ordering and the measured fixture-cost diagnosis.
