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

### 2026-10-15 — controlled evidence and resolved mechanism
Environment: macOS arm64, Go 1.26.2. Baseline source revision
`07b6b35e14aad0de688326fb36d81c54303c691c`; final candidate
`9e11b9b3` (code unchanged by subsequent evidence commits). The controlled fault
is a finite scheduling delay at the existing cancellable real-fold seam. It
demonstrates the fixture's total-deadline dependency, not a production failure
or the exact historical scheduling cause. Historical `/tmp/builder-3453-gate-race.log`
and `/tmp/builder-3453-daemon-rechecks.log` were available locally, and the uploaded
#3453 builder bundle was retrieved through `fleet-task.py evidence 3453`.

Initial scratch overlay on the baseline added construction/chunk/readiness/
validation diagnostics and three-second cancellable delays at `NewStore`'s
fold seam. Command: `env GOMAXPROCS=2 go test -race -count=1 -timeout=2m -json -overlay=/tmp/builder-3467/baseline-overlay.json ./cmd/pyry -run '^TestDaemonThreadMetadataAllocations$'`.
One test executed, zero passed, one expected failure, zero skipped: construction
0.979s, chunk admission at 0.128/3.777/7.456s, readiness expiry at 10.001s while
rebuilding with zero published versions. Snapshot validation never began.
The new delayed-replay regression also failed against the original wait:
`env GOMAXPROCS=2 go test -race -count=1 -timeout=2m -json ./cmd/pyry -run '^TestDaemonThreadMetadataReadiness$'`
with three-second delays, one executed/zero passed/one failed/zero skipped.
A preliminary diagnostic-format build failure executed zero tests and was
corrected before that regression run.

An intermediate `9fe27c3b` run used `-count=3 -timeout=3m` and
`-run '^(TestDaemonThreadMetadata(Readiness|ReadinessStalled|Allocations)|TestThreadMetadataReadSources)$'`.
It started 13 tests/subtests: 11 passed, one inactivity failure, one unfinished
at package timeout, zero skips. It overlapped baseline/candidate compilation and
an incidental existing race binary. This failed experiment drove the headroom
revision above; it is not final acceptance evidence.

For matched final binaries, build candidate with
`env GOMAXPROCS=2 go test -c -race -o /tmp/builder-3467/final-candidate.test ./cmd/pyry`.
Build baseline with the same command plus
`-overlay=/tmp/builder-3467/matched-baseline-overlay.json` and output
`final-baseline.test`. That overlay replaces only the fixture source with its
instrumented equivalent using the original ten-second total polling wait; both
binaries include the identical five-second-per-chunk regression from the final
candidate. No production code differs. Per process command:
`go tool test2json -t -p github.com/pyrycode/pyrycode/cmd/pyry BINARY -test.v=test2json -test.count=1 -test.timeout=3m -test.run='^TestDaemonThreadMetadataReadiness$'`.

The clean comparison launches two copies per policy, each with GOMAXPROCS=2,
no CPU affinity, five-second cancellable delay per actual chunk. Join both
baseline processes before launching both candidates. No other builder-owned
workload or compilation overlaps this comparison. Host-wide incidental work
is not isolated. All candidates retain 1,024 items and 4,219,904 content bytes.

| Policy/process | Construction | Replay/readiness | Snapshot validation | Executed/passed/failed/skipped |
| --- | --- | --- | --- | --- |
| Original / 0 | 5.039s | 10.032s, rebuilding | Not entered | 1/0/1/0 |
| Original / 1 | 5.037s | 10.288s, rebuilding | Not entered | 1/0/1/0 |
| Final / 0 | 6.828s | 59.523s | 18.286s | 1/1/0/0 |
| Final / 1 | 7.343s | 59.091s | 18.229s | 1/1/0/0 |

Raw records: `/tmp/builder-3467/clean-{baseline,candidate}-contended-{0,1}.json`;
launcher/status record: `/tmp/builder-3467/final-run.log`. Baseline wall 17.099s,
exits 1/1; candidate wall 111.895s, exits 0/0. Candidate chunk admissions for
process 0 were 1.173/13.876/24.144/32.706/39.536s. Twenty-five seconds of controlled
delay alone exceeds the final twenty-second inactivity interval, proving a
twenty-second total-deadline increase would not pass this regression.

An earlier four-process comparison used identical per-process settings but
overlapped a builder-owned focused batch: all four baselines failed; all four
candidates passed (4/4/0/0), package durations 114.820–116.668s. Its records are
`final-{baseline,candidate}-contended-{0,1,2,3}.json` and `comparison.log` in the
same scratch directory. The overlapping focused batch (`-count=3 -timeout=2m`,
stalled/allocation/source cases) started seven tests/subtests, passed six and
left one unfinished at timeout, with zero skips. Those results are retained
but superseded by the sequential final checks below. All controlled workers
and both timed-out test processes exited before the ordinary suite runs.
