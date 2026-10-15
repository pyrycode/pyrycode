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
- 2026-10-15: Identical contention policy to #3364: launch four prebuilt race binaries simultaneously using `taskset -c 0 /tmp/builder-3431/baseline.test -test.run=^TestAgentSessionEndings_Startup$ -test.count=1 -test.timeout=10m -test.v`, join all four, retain individual raw logs, JSON converted through `go tool test2json -t -p github.com/pyrycode/pyrycode/cmd/pyry`, and process statuses. Four parents, four legacy and four modern executions all passed: 12 named executions/passes, zero failures/skips, all process exits 0. Legacy took 53.85–53.94 seconds and modern 57.04–57.28; parent 110.88–111.23. No timeout or deadlock reproduced. The controlled workload supports reducing needless fixture scan cost; it does not establish the historical package timeout's cause.

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
- 2026-10-15: Complete baseline command at `90ecea2f`: `go test -race -json -count=1 -timeout=10m ./cmd/pyry`, exit 0, package 303.756 seconds, 16,460 named executions / 16,457 passes / zero failures / three skips. Startup became active at package elapsed 264.568 seconds, ran for 31.61 seconds (legacy 15.89, modern 15.72), then the package completed. Thus package elapsed is measured separately from active startup time; no timeout stacks exist because no timeout occurred. The three skips are `TestRunAgentRun_RealClaude` (requires `PYRY_E2E_REAL_CLAUDE=1`), `TestCodexApprovalLive` (requires capture binary/home), and `TestMemoryPathsCaseInsensitive` (host filesystem is case-sensitive). Candidate compilation, focused validation and mutation controls overlapped portions of the baseline package run; no affinity or package-concurrency setting was changed. Slow independent top-level tests include memory credential cancellation (40.22 seconds), runtime opening-child-call permutations (30.57) and reply fallback lookup budget (29.80). These observed costs do not attribute the unavailable historical timeout to any one test.
- 2026-10-15: After that package completed, repeated the baseline binary's ordinary run with the corrected evidence driver: exit 0, three named executions/passes, zero failures/skips, parent 28.48 seconds (legacy 14.22, modern 14.27). Then candidate `a0160185b200e9069ba0d8bce6d8c9b6290ba98f`'s identical ordinary selection passed in 5.78 seconds (legacy 2.95, modern 2.83), with the same three named passes and no skips. Candidate was compiled with `go test -race -c -o /tmp/builder-3431/candidate.test ./cmd/pyry`; ordinary command is `/tmp/builder-3431/candidate.test -test.run=^TestAgentSessionEndings_Startup$ -test.count=1 -test.timeout=10m -test.v`. Candidate contention uses exactly the baseline command/process count/CPU/flags, substituting only the binary path. All four processes exit 0: 12 named executions/passes, zero failures/skips; parent 22.34–22.57 seconds, legacy 11.29–11.46, modern 11.05–11.19. All comparison processes were joined before starting the ordinary candidate package run. No intentional competing workload overlapped these isolated measurements.

Candidate phase seconds (ordinary value; range across four contended processes):

| Phase | Legacy ordinary | Legacy contention | Modern ordinary | Modern contention |
| --- | --- | --- | --- | --- |
| Construction | 0.159 | 0.753–0.775 | 0.201 | 0.641–0.650 |
| Raw paging/segment inspection | 0.273 | 1.084–1.099 | 0.247 | 0.994–1.030 |
| First recovery | 0.861 | 3.466–3.510 | 0.900 | 3.282–3.361 |
| Reopened endings/receipt reads | 0.433 | 1.583–1.598 | 0.396 | 1.675–1.736 |
| Repeated recovery and ending read | 1.219 | 4.380–4.498 | 1.090 | 4.349–4.453 |

- 2026-10-15: The repeated baseline ordinary phase seconds were legacy 1.367 / 1.394 / 4.046 / 2.317 / 5.088 and modern 1.093 / 1.315 / 4.308 / 2.277 / 5.272 in table order. Both ordinary comparisons show about fivefold lower complete startup cost. Candidate modern encoded size varies by a few timestamp bytes (1,162,329–1,162,334 in recorded comparisons); ordinary segment sizes were 1,054,738 / 107,591, and contention measured 1,054,739–1,054,743 / 107,591. The repeated baseline modern fixture measured 6,896,944 bytes. All candidate cases have two real segments and two nonempty pages before recovery. This resolves both planning questions: actual rollover survives the reduction, and neither ordinary nor bounded contention reproduced a runtime defect. The fixture-cost reduction is the supported repair.
- 2026-10-15: Complete candidate package at `a0160185`: `go test -race -json -count=1 -timeout=10m ./cmd/pyry`, exit 0, package 291.762 seconds; 16,460 named executions, 16,457 passes, zero failures and the same three documented skips. Startup passed in 7.89 seconds (legacy 3.61, modern 4.27). Ordinary package concurrency was unchanged and no controlled load overlapped this run. Package runtime remains dominated by other work; the 31.61→7.89-second startup reduction does not imply an equal whole-package reduction. Complete gate evidence follows separately; focused/package passes alone do not certify it.
- 2026-10-15: Combined gate source is `b009cd8405731058f3a3b13f30996b41a55c4493` (production/tests identical to `a0160185`). Command: `make check GO=/tmp/builder-3431/go-uncached`, normal package concurrency, empty GOFLAGS. Module vet passed, and the complete uncached unit race tier passed: 26,788 named executions / 26,767 passes / zero failures / 21 skips. `cmd/pyry` completed in 411.204 seconds under module concurrency, with startup active for 8.35 seconds. Repaired siblings executed and passed: `TestGiveUpExempt_PendingResetsStreak` 0.01 seconds, `TestResetThenRotate_FailedHandoffIsStale` 2.61, `TestLiveStateRun` 0.32, and retained #3364 `TestThreadPageLargeReply` 5.48. No timeout stacks were produced because no test timed out.

The external wrapper adds only the requested uncached JSON flags to tests; other Go commands and package concurrency are unchanged:

```sh
#!/bin/sh
subcommand=$1
shift
if [ "$subcommand" = test ]; then
    exec /usr/local/go/bin/go test -count=1 -json "$@"
fi
exec /usr/local/go/bin/go "$subcommand" "$@"
```

Unit skip inventory (these capabilities are not certified):
- `cmd/pyry`: the same three live-Claude, live-Codex and case-sensitive-filesystem skips listed above.
- `internal/agentrun/trust`: `TestMarkWorkdirTrusted_CaseMismatchWritesOnDiskKey`, case-sensitive filesystem.
- `internal/canonicalpath`: `TestResolve_DarwinRealpath`, Darwin-only alias; `TestResolve_CaseMismatchFoldsToOnDiskCase`, case-sensitive filesystem.
- `internal/codexsup`: `TestCaptureLive`, requires live capture binary/home.
- `internal/apps`: `TestBackupSQLiteCancellation`, `TestBackupSQLiteStorageErrors`, `TestBackupSQLiteWAL`, `TestCandidateSupportedTemplate`, `TestOwnerSupportedNode`, `TestPublishTemplate`, `TestUpdateTemplateRollback`, `TestRestoreSQLiteWAL`, `TestRestoreSQLiteGenericAndCancellation`, `TestStartupTemplateRecovery`; require configured supported Node/npm, including SQLite-capable runtime for the SQL cases.
- `internal/memorycapture`: `TestFilesystemAttacks/owner`, ownership substitution requires root.
- `internal/memoryruntime`: `TestMacOSInstallCancelled` and `TestMacOSHostGate`, require macOS host (arm64 for install cancellation).
- `internal/streamsup`: `TestAccountTokenReadDeadline`, full production deadline belongs to opt-in `make test-slow`.

- 2026-10-15: The complete sequence finished at gate revision `b009cd8405731058f3a3b13f30996b41a55c4493`: uncached `make check GO=/tmp/builder-3431/go-uncached` exit 0 in 712.224 seconds, then `make build` exit 0 in 1.310 seconds. The build command was `go build -o ./pyry ./cmd/pyry`. No failing or unfinished sequence remains. Only this plan's evidence was edited during the gate; tested production/test sources remain identical. The verifier's later full-module gate and the ADR documentation handoff remain stage-owned follow-up, not unexecuted builder checks.

| Tier | Command emitted by make | Exit | Named executions / passes / failures / skips |
| --- | --- | --- | --- |
| Vet | `/tmp/builder-3431/go-uncached vet ./...` | 0 | Not a test tier |
| Unit race | `/tmp/builder-3431/go-uncached test -race ./...` | 0 | 26,788 / 26,767 / 0 / 21 |
| Staticcheck | `/usr/local/bin/staticcheck ./...` | 0 | Not a test tier |
| Substrate guard | `/tmp/builder-3431/go-uncached run ./cmd/substrate-guard` | 0 | `substrate-guard: clean` |
| Fake-daemon e2e race | `/tmp/builder-3431/go-uncached test -tags e2e -race -count=1 ./internal/e2e/...` | 0 | 703 / 701 / 0 / 2 |
| Build, after complete check | `make build` | 0 | Not a test tier |

- 2026-10-15: E2e root package passed in 188.426 seconds; all seven test-bearing packages passed. Its skips are `TestInnerFatalChild` (helper executed only by `TestHarness_NoLeakOnFatal`) and `TestHostedAppsRecoverySQL` (requires `HOSTED_APP_NODE` and `HOSTED_APP_TEMPLATE_BUILD`). Untagged offline tests in the `realclaude` directory are part of this hermetic run; no authenticated live-Claude run is claimed or required. All logs/statuses remain under `/tmp/builder-3431`, with durable commands, source revisions, counts, skip reasons and outcomes retained here and linked from PR #3438.
- 2026-10-15: For completeness, the standalone candidate startup active interval began at package elapsed 254.545 seconds (7.89 seconds active within 291.762 total). In the combined unit gate it began at 374.735 seconds (8.35 active within 411.204 total). Neither package-wide elapsed time is attributed to the active startup subtest. The historical timeout remains non-reproduced; measured fixture overhead and preserved coverage justify the repair independently of unexplained green reruns.
