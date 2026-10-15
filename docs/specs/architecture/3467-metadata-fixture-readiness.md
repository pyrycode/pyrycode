# Metadata fixture readiness (#3467)

## Files read
- `cmd/pyry/daemon_thread_metadata_test.go` → `testThreadMetadataFixture`, allocation/source tests and benchmark: preserve the complete fixture and measurement boundaries.
- `internal/thread/store.go` → `Load`, `run`, `Status`, `Shutdown`: asynchronous bounded replay, cancellable fold barrier and joined retirement.
- `internal/thread/observations_store.go` → `Changes`: does not wait for initial readiness, so cannot replace fixture readiness polling.
- `internal/history/forward.go` → `Walk`: serial replay chunks progress across physical segments.
- `docs/knowledge/features/thread-package-background-store.md` § Loading and replay readiness: usability certifies the captured history bound; worker context must survive clean retirement.
- `docs/knowledge/features/development-verification.md` § Diagnosing startup recovery runtime: matched conditions, phase timings and complete suites are separate evidence.

## Context
The existing ten-second total replay deadline can expire despite serial replay
making progress. Historical #3453 logs show a 25.15-second failure and a 6.05-second
isolated pass, without phase timings. A controlled baseline overlay delays each
real fold chunk by three seconds under race instrumentation, with GOMAXPROCS=2.
This distinguishes an imposed scheduling-delay reproduction from historical cause.
No production change or decision record is needed. No overlapping feature branch
touches the fixture. One deliverable; estimated 240 written lines, zero exported
types, two unchanged consumer sites, three acceptance criteria, three wait exits.

## Design
Keep the 1,024 ordinary messages and 4,096 text bytes per message. Observe serial
replay chunk admission through the existing `beforeFold` seam. A buffered progress
notification refreshes a ten-second inactivity watchdog, while a separate one-minute
context bounds the whole readiness phase. This removes the dependency on all chunks
finishing within one inactivity interval; it does not treat progress as usability.
The fixture returns only after usable status at consumed/shown version 1,024 and
a complete usable snapshot with 1,024 items and both versions 1,024. Log construction,
chunk admission, readiness and snapshot-validation durations outside measured reads.
An optional cancellable fold callback permits matched baseline/candidate delay.

## Concurrency model
The store owns its existing worker. Its serial callback sends a buffered,
nonblocking progress notification; the fixture goroutine owns timers and status
polling. No new persistent goroutine. Cleanup calls `Shutdown` before cancelling
the independently owned load context, including failed waits.

## State transitions and identity reuse
| Event | Race-enabled test |
| --- | --- |
| Multiple delayed replay chunks, then complete publication | `TestDaemonThreadMetadataReadiness` |
| No replay progress, bounded failure, cancellation and joined worker | `TestDaemonThreadMetadataReadinessStalled` |
| Usable fixture consumed by unchanged reads and registry removal | `TestDaemonThreadMetadataAllocations` |
No reload, retry or identity reuse is introduced.

## Error handling
Unavailable status fails immediately. Inactivity or the overall context expires
with an error and last status diagnostics; neither partial progress nor a heartbeat
permits assertions on incomplete items. The existing cleanup joins stalled work
through its cancellable barrier without cancelling the load context prematurely.

## Testing strategy
First run the three-second-per-chunk baseline overlay; retain its expected failure
and phase timings. Add the delayed real-fixture regression and run it against the
old total-deadline wait before implementing the watchdog. Compare candidate using
the same delays, race detector and GOMAXPROCS=2; repeat uncached. Exercise bounded
no-progress failure. Preserve allocation, registry and source assertions and all
benchmark cases. Run complete race-enabled `./cmd/pyry`, the required combined
thread/daemon/relay command, `go vet ./...`, and `go build ./cmd/pyry`. Retain JSON
counts, skips, revisions and phase evidence here. Controlled delay work must be
joined before ordinary suite runs. The verifier owns the full-module test gate.

## Open questions
- Baseline/candidate timings, chunk counts and ordinary suite outcomes will be
  recorded after execution; historical scheduling attribution remains unknown.

## Revisions
### 2026-10-15 — measured single-chunk headroom
The first candidate's ten-second inactivity interval expired during the final
chunk under incidental overlapping compilation and another race-test process:
construction 12.692s; chunk entries at 1.482, 11.298, 19.935, 27.858 and 32.671s;
expiry at 45.675s. A chunk plus final publication can exceed ten seconds even
though earlier chunks progress. Use twenty seconds of inactivity and a two-minute
overall cap. Increase the controlled delay to five seconds on each of five real
chunks: its 25 seconds of imposed delay exceeds even the new inactivity interval,
so simply changing a total deadline to twenty seconds still fails this regression.
The existing one-second stalled-worker proof keeps bounded failure inexpensive.
This is local test scheduling headroom, not a production latency guarantee.
