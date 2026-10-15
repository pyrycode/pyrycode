# Startup recovery runtime and combined race gate

## Files read
- `cmd/pyry/agent_session_endings_test.go` → `TestAgentSessionEndings_Startup`, `testSessionEndings`: legacy/modern recovery, raw paging and repeated recovery.
- `cmd/pyry/startup_history.go` → `reconcileStartupHistory`: three complete raw-log readers before closure and divider append.
- `cmd/pyry/startup_agent_history.go` → `readStartupAgentWork`: 128-entry pages, scoped identities and saved closure references.
- `internal/history/log.go` → `AppendWithMetadata`, `Page`, `MaxSegmentBytes`: roll after reaching 1 MiB; pages decode whole segments.
- `internal/history/segment.go` → `listSegments`, `segmentName`: real segment naming and read boundaries.
- `docs/knowledge/features/history-package-producers-legacy-compatibility.md` § Close background work after main-turn completion and partial recovery: reopened reads and reused IDs must remain covered.
- `docs/knowledge/features/development-verification.md` § Test execution and artifact survival: uncached execution, named counts and durable evidence.
- `docs/specs/architecture/3364-large-thread-page-reply-wait.md` § Revisions: four pinned prebuilt race binaries provide bounded contention; its repaired large-reply evidence remains authoritative.
- `docs/specs/architecture/3331-thread-index-over-history.md` § Documentation handoff: preserve the ADR 045 handoff without expanding this fixture repair into storage redesign.
- `CODING-STYLE.md`, `Makefile`: test conventions and complete gate tiers.

## Context
The historical 600.269-second package timeout at `7332d538` does not prove startup deadlock: startup was active for only 53 seconds, modern for 19. Later passes likewise do not diagnose it. The fixture encodes 140 × 8192 NUL characters as JSON escapes, about 6.9 MB before entry metadata; recovery and repeated reopened reads scan that padding several times under race instrumentation.

One deliverable is a measured reduction of unnecessary startup-fixture work with explicit preservation of recovery coverage and a complete combined gate. The sibling repairs #3428, #3429 and #3430 are merged in baseline `3ec72c34342ee8a7f0b3319ccbf1d91ede43170d`; all native blockers are closed. Fetched feature branches have no overlap with the planned startup files. Estimate: under 400 written lines including tests, plan and evidence; zero exported types, zero consumer migrations, four acceptance criteria, no new production reject branches. This is within all five builder limits. No new architectural decision is needed; ADR 045 remains owned by #3331 / #3348.

## Design
First instrument the existing fixture with elapsed construction, raw paging, recovery and subsequent-read phases. Count encoded bytes and real segment sizes before recovery, and assert at least two nonempty raw pages at the production agent reader's 128-entry size and at least two nonempty real segments. Compare baseline and candidate under ordinary scheduling and the identical bounded four-process, one-CPU workload.

If no runtime defect reproduces, retain 140 padding entries but replace escaped NUL padding with 8192 printable ASCII characters. This should retain just over 1 MiB and a real rollover while reducing encoded bytes by roughly sixfold. Actual on-disk assertions decide whether the candidate is sufficient. Do not change storage APIs, persistence formats, production timeout, test parallelism or recovery behavior without new evidence and a revision.

Strengthen the existing two-ending assertion to require distinct linked-task and reused-call endings, exact call/parent/source/lifetime attribution, occurrence time, shown metadata, truncated-ID exclusion and no invented legacy lifetime. Capture the modern emitter's lifetime for exact comparison. Reopen for recovery, ending reads and latest-displayable reads. Check the restart divider is exactly the latest displayable receipt target and repeated recovery adds no endings.

## Concurrency model
Keep `t.Parallel` and existing synchronous fixture/recovery calls. Timing and counters are local to each subtest. No goroutines, locks or background product work are added. Contention uses four bounded child test processes pinned to one allowed CPU, all joined before ordinary package validation.

## State transitions and identity reuse
| Event | Race coverage |
| --- | --- |
| Legacy delimiter then reuse of call ID without source | `TestAgentSessionEndings_Startup/legacy` requires the distinct original task and successor call |
| Modern source/lifetime followed by unattributed reused call | `TestAgentSessionEndings_Startup/modern` checks original attribution and no successor lifetime |
| Reopen after real rollover and raw pagination | Both startup subtests count disk segments and nonempty 128-entry pages before recovery |
| Reconcile twice from reopened stores | Both startup subtests require identical ending IDs after the second reconciliation |

## Error handling
All append, paging, file-stat and payload-decode errors fail immediately. Timings include phase-start diagnostics so timeout stacks can be correlated with the active phase. A timeout retains complete JSON output/stacks and cannot be described as a startup timeout merely because startup was active. No new production failure mode exists.

## Testing strategy
Write explicit coverage assertions and phase instrumentation first; measure the old fixture before changing its padding. A scratch overlay reducing padding below rollover must fail the new disk assertion. Compile diagnostic baseline and repaired race binaries; ordinary runs and four simultaneous `taskset -c 0` runs use the same startup selection and test timeout, retaining each process's JSON and exit status outside the worktree.

Measure a complete uncached baseline package run and candidate package run separately at normal concurrency. Pass the requested three-run focused race command and the complete uncached package race command. This ticket explicitly requires the combined gate, overriding the builder's ordinary scoped-check rule: run uncached `make check` with an external GO wrapper adding only `-count=1 -json` to test calls, then `make build`, without changing package concurrency. Retain commands, revisions, named counts, skip reasons, elapsed times, failures/stacks and tier exit statuses in plan revisions and the PR. No live-Claude proof is required.

## Open questions
- Does the historical timeout reproduce, and which measured phase dominates? Resolve with baseline and candidate measurements; explicitly report non-reproduction.
- Does printable padding retain real rollover? Resolve with on-disk bytes and segment assertions before accepting the reduction.

## Documentation handoff
Pending for the documentation stage: preserve the ADR 045 requirements from #3331 / PR #3348 in `docs/specs/architecture/3331-thread-index-over-history.md` § Documentation handoff: Proposed `docs/knowledge/decisions/045-thread-index-over-history.md` with Context, Decision, Reliability, Rejected options, Consequences, Migration and rollback; `docs/knowledge/decisions/042-daemon-built-thread.md` § The six approved decisions, Storage cross-reference; `docs/knowledge/CATALOG.md` § Decisions, ADR 045 entry. This ticket changes neither the proposed storage architecture nor that handoff's ownership.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: `TestAgentSessionEndings_Startup` uses synthetic fixed IDs and `t.TempDir`; production parsing in `readStartupAgentWork` and `history.Page` is unchanged. Strengthened truncation/source checks protect the existing recovery boundary.
- Tokens/secrets: no credentials are read or created; all fixture payloads are synthetic.
- File operations: test segment inspection stays under its own temporary root, reads only real segment files and does not expose a runtime path API. Storage writes and permissions remain owned by `history.Store`.
- Subprocesses: only bounded validation processes are added outside product code; no user input is shell-interpreted and every contention process is joined.
- Cryptography: no primitives, keys or nonce handling change.
- Network/I/O: no network path changes; fixture size decreases while actual pagination and disk rollover remain asserted.
- Errors/logs: diagnostics contain phase names, counts, bytes and durations only; no fixture prose or host credentials are logged by new diagnostics.
- Concurrency: subtest-local counters/timestamps add no shared mutable state or workers; existing parallel execution remains intact.
- Threat model: no product trust or permission boundary changes; storage architecture and relay security remain outside this test-only repair.

**Reviewer:** builder self-review. **Date:** 2026-10-15.

## Revisions
- 2026-10-15: Baseline diagnostics and coverage assertions were committed as `90ecea2f025d8d1f76e99452abd17459e1493606`, on `3ec72c34` with all three sibling repairs. Linux amd64, Go 1.26.9, allowed CPUs 0–7, cgroup quota six CPUs, empty GOFLAGS. Compile with `go test -race -c -o /tmp/builder-3431/baseline.test ./cmd/pyry`. Ordinary measurement executes that binary with `-test.run=^TestAgentSessionEndings_Startup$ -test.count=1 -test.timeout=10m -test.v`; all three named tests/subtests passed without skips. Legacy took 13.57 seconds and modern 14.70 (parent 28.27). Actual fixture bytes were 6,893,719 / 6,896,943; each had seven real segments and two nonempty 128-entry pages (148 / 155 entries). The initial scratch JSON converter incorrectly treated its raw log as an executable after the test passed; corrected conversion reads stdin. Raw logs and converted named results agree; this was an evidence-tool error, not a test failure.
- 2026-10-15: Identical contention policy to #3364: launch four prebuilt race binaries simultaneously using `taskset -c 0 /tmp/builder-3431/baseline.test -test.run=^TestAgentSessionEndings_Startup$ -test.count=1 -test.timeout=10m -test.v`, join all four, retain individual raw logs, JSON converted through `go tool test2json -t -p github.com/pyrycode/pyrycode/cmd/pyry`, and process statuses. Four parents, four legacy and four modern executions all passed: 12 named executions/passes, zero failures/skips, all process exits 0. Legacy took 53.85–53.94 seconds and modern 57.04–57.28; parent 110.88–111.22. No timeout or deadlock reproduced. The controlled workload supports reducing needless fixture scan cost; it does not establish the historical package timeout's cause.

Baseline phase seconds (ordinary value; range across four contended processes):

| Phase | Legacy ordinary | Legacy contention | Modern ordinary | Modern contention |
| --- | --- | --- | --- | --- |
| Construction | 1.257 | 5.053–5.147 | 1.108 | 4.153–4.200 |
| Raw paging/segment inspection | 1.290 | 4.761–4.793 | 1.134 | 4.566–4.605 |
| First recovery | 3.830 | 15.900–15.932 | 3.910 | 16.158–16.500 |
| Reopened endings/receipt reads | 2.096 | 8.286–8.306 | 2.442 | 9.783–9.845 |
| Repeated recovery and ending read | 5.088 | 19.761–19.789 | 6.105 | 22.167–22.300 |

- 2026-10-15: Scratch overlays independently prove the new guards: `go test -race -json -count=1 -timeout=10m -overlay=/tmp/builder-3431/no-rollover.json -run '^TestAgentSessionEndings_Startup$' ./cmd/pyry` changes printable padding to one byte and fails both subtests with `fixture did not roll over`. The analogous `no-pagination.json` uses three 524,288-byte entries, retains real rollover, and fails both subtests with `fixture has only 1 nonempty raw pages`. Each overlay executed three named tests/subtests with three expected failures, no skips, command exit 1. No overlay enters the candidate. These failures precede accepting the printable-padding repair.
- 2026-10-15: The one-line fixture repair replaces NULs with printable ASCII, retaining 140 entries of 8192 characters. Actual bytes become 1,159,109 legacy / 1,162,333 modern (about 83% less); legacy segment sizes are 1,051,518 and 107,591 bytes, preserving the production 1 MiB rollover. The modern fixture also has two real segments; both retain two nonempty pages and their original entry counts. `go test -race -json -count=3 -timeout=10m -run '^TestAgentSessionEndings_Startup$' ./cmd/pyry` passed all nine named executions: three parents, three legacy, three modern, zero failures/skips, exit 0, package 20.685 seconds. These validation runs overlapped the baseline package's ordinary work, so the final isolated ordinary/controlled comparison is recorded separately. No storage or daemon production change is supported or needed by the observations.
