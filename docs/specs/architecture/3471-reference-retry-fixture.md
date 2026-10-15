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

## Revisions

### 2026-10-15 — measured fixture cost and negative control

Both design questions are resolved for the local reproduction: excessive padding dominates the retry fixture, and both chunk and FIFO residency must be removed. The design remains unchanged. The exact ten-minute failure was not reproduced on this Linux host; the MacBook's measured 600.309/600.555-second failures remain historical evidence, not a claimed local result.

Host: dispatcher `build-hp-1`, container `6d2b40286902`, Linux x86_64 kernel `7.0.0-38-generic`, Go `go1.26.9 linux/amd64`. CPU quota is eight cores (`800000/100000`); observed system load averages were approximately 7–15. Default Go package/test concurrency was retained. The original complete package and focused profiling runs overlapped; diagnostic and candidate focused runs also overlapped the original full run. These are contended measurements, not isolated microbenchmarks.

| Race-enabled run | Revision/source | Executed / passed / failed / skipped | Package seconds |
| --- | --- | --- | --- |
| Complete thread baseline | `07b6b35e14aad0de688326fb36d81c54303c691c` | 1117 / 1117 / 0 / 0 | 336.458 |
| Original four-family retry matrix with profiles | Same baseline | 33 / 33 / 0 / 0 | 111.939 |
| Original padding with new status/counter/timing assertions | Baseline plus diagnostic test edits | 33 / 33 / 0 / 0 | 100.918 |
| Bounded fixture with profiles | Implementation atop plan commit `7a71edfd` | 33 / 33 / 0 / 0 | 3.714 |

Counts include parent tests and nested subtests once each. All complete-baseline retention/lifecycle tests ran; no deadline or production code changed.

The diagnostic run separates synchronous preparation from later waits. For `TestStoreReferenceRetry/tail/unreadable`, 4096 padding appends took 13.504s, padding catch-up 60.420ms, failure after the delivery 75.260ms, and repaired retry/equality 435.446ms. Its bounded counterpart took 111.775ms, 5.434ms and 4.260ms for the first three phases. All six original nondeferred tail arms spent 11.820–15.305s appending padding. Original replay padding without a follower took 0.438–0.902s per arm. Thus a stack blocked in append represents repeated expensive catch-up during setup, rather than a worker that spent ten minutes waiting for the terminal event.

CPU and synchronization profiles corroborate the phase evidence. Across the original 111.939s retry run, sampled CPU was 109.050s; cumulative `readSegment` CPU was 39.720s, of which `forwardSegment` accounted for 36.610s. Block profiling attributed 76.256s to `AppendWithMetadata` mutex waits; mutex profiling attributed 77.989s of contention delay to unlocks in `forwardSegment`. Those are aggregate profile times, not additive wall-clock phases. The bounded run sampled 2.880s CPU, with 0.680s cumulative `readSegment` CPU. Repeated growing-segment decoding under the history mutex is the demonstrated expensive dependency; other package tests also contribute to the overall 336-second baseline. The evidence does not establish the MacBook's exact time distribution or exclude host contention there.

Negative control: with the strengthened assertions and 256 padding entries but no replay chunk decorator, `TestStoreReferenceRetry/replay/missing` failed after 5.03s because worker status remained usable at version 257. No rendering poll created a false failure. Adding the decorator made all 22 leaf scenarios pass, each recording one failed authoritative target lookup. Tail arms also proved padding reached its exact usable version with zero failures before the meaningful event. Source repair then restored legacy item/revision/version/watermark equality in every arm.

Raw evidence remains outside the worktree in `/tmp/builder-3471/`: `baseline-thread.json`, `baseline-retry.json`, `diagnostic-retry.json`, `no-split-control.json`, `candidate-retry.json`, CPU/mutex/block profiles and their decoded summaries. Earlier MacBook evidence was retrieved through the evidence service for #3453, verifier run `c58c2483aaabdaf904700e131fecb10d21ef15a1514ab134145a5e4ed1197342`; its verdict records both package timeouts and the independent daemon failures. Its artifacts do not supply a MacBook Go version or per-phase timings.

### 2026-10-15 — complete candidate gate evidence

Tested implementation: `e4e7cea0a981c335008a73c76b5b5ae8e74d4c28`. The complete race-enabled thread package passed in 251.859s under the full-module gate: 195 top-level tests plus 922 subtests, all 1117 passed, zero failures/skips. This includes the source-cache retention and lifecycle tests; the original complete baseline ran the same 195/922 counts in 336.458s. The focused retry runs each started four top-level tests and 29 subtests (22 leaf scenarios).

The required complete gate ran as `make -k check GO=/tmp/builder-3471/go-json`. The scratch wrapper preserves each recipe's arguments and adds `-json -count=1` to test commands for uncached counted evidence. `-k` requests every stage even on failure; all results below must be assessed individually. Package/test concurrency and the default ten-minute package deadline remain unchanged.

| Check | Result | Elapsed |
| --- | --- | --- |
| Full-module `go vet ./...` | PASS | 3.767s |
| Full-module race suite | PASS: 4991 top-level + 22121 subtest starts; 27089 passed, 0 failed, 23 skipped | 373.330s command wall time |
| Staticcheck | PASS | Included in complete gate wall time |
| Substrate guard | PASS, clean | 0.304s |
| Complete fake-daemon e2e | PASS: 265 top-level + 445 subtest starts; 708 passed, 0 failed, 2 skipped | 193.075s command wall time; package 189.672s |
| `make build` | PASS | 0.114s timed cached build |

Named unit skips and reasons (no failed tests):

- `internal/agentrun/trust`: `TestMarkWorkdirTrusted_CaseMismatchWritesOnDiskKey` — case-sensitive filesystem.
- `internal/canonicalpath`: `TestResolve_CaseMismatchFoldsToOnDiskCase` — case-sensitive filesystem; `TestResolve_DarwinRealpath` — Darwin-only alias behavior.
- `cmd/pyry`: `TestRunAgentRun_RealClaude` — opt-in real-Claude environment absent; `TestMemoryPathsCaseInsensitive` — case-sensitive filesystem.
- `internal/memorycapture`: `TestFilesystemAttacks/owner` — requires root for ownership substitution.
- `internal/memoryruntime`: `TestMacOSInstallCancelled`, `TestMacOSHostGate` — macOS host boundaries.
- `internal/streamsup`: `TestAccountTokenReadDeadline` — production-duration proof belongs to `make test-slow`.
- `internal/apps`: `TestBackupNodeDirectoryIdentity`, `TestBackupNodeFailures/sql`, `TestBackupSQLiteCancellation`, `TestBackupSQLiteStorageErrors`, `TestBackupSQLiteWAL`, `TestCandidateSupportedTemplate`, `TestCommittedTemplateUpdateSnapshot`, `TestOwnerSupportedNode`, `TestPublishTemplate`, `TestUpdateTemplateSnapshot`, `TestUpdateTemplateRollback`, `TestRestoreSQLiteWAL`, `TestRestoreSQLiteGenericAndCancellation`, `TestStartupTemplateRecovery` — supported Node/npm or `HOSTED_APP_NODE` unavailable. These skips establish no supported-runtime acceptance.

The no-test `internal/brokenpyry` package is also reported as a package skip; it is not included in the 23 skipped test/subtest count. No live-Claude or native MacBook candidate run is claimed. The documentation stage owns the pending documentation handoff above; the verifier owns rerunning the standard gate on its host.

The complete `make -k check` exited zero after approximately 592 seconds (19:57:12–20:07:04 UTC). Every requested stage actually ran and passed: vet, full-module race tests, staticcheck, substrate guard and fake-daemon e2e. There were no named failures. The two e2e skips are `internal/e2e.TestInnerFatalChild` (helper runs only under `TestHarness_NoLeakOnFatal`) and `internal/e2e.TestHostedAppsRecoverySQL` (requires `HOSTED_APP_NODE` and `HOSTED_APP_TEMPLATE_BUILD`). Full captured output is `/tmp/builder-3471/check.log`; timed build output is `/tmp/builder-3471/build-timed.log`.
