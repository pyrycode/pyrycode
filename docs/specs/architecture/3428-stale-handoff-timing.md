# Separate stale-handoff classification from expiry timing (#3428)

## Files read

- `cmd/pyry/session_reset_stale_test.go` → `TestResetThenRotate_FailedHandoffIsStale`: all live/dormant classifications, stale-context checks and terminal recovery.
- `cmd/pyry/session_reset_test.go` → `newResetFixture`, `resetOptions`, `resetFixture.answer`: synchronous reply injection and real busy tracking.
- `cmd/pyry/session_reset.go` → `wrapUp`, `wrapUpTextContext`, `writeResetWrapUp`: stale persistence precedes resolution; all phases share one deadline.
- `internal/sessions/handoff.go` → `MarkHandoffNoteStale`: real persistent invalidation consumes deadline time.
- `docs/knowledge/features/sessions-package-key-types-handoffnote-store.md` § No log line carries note text / The wrap-up's write side: preserve secrecy checks and distinguish read/write failures.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: observe intended paths independently of the eventual classification.
- `docs/specs/architecture/3331-thread-index-over-history.md` § Documentation handoff: ADR 045 remains a separate documentation-stage contract.

## Change

This test currently gives immediate failure classifications and intentional expiry the same 35 ms deadline, including real stale-state persistence. Use finite headroom for classification (initially two seconds), with a separate shorter deadline for readiness, idle and completion expiry (initially 500 ms). Retain a finite rotation ceiling (initially three seconds), all existing assertions and the permission-enforcement skip. Measure before finalizing these bounds. No production code, interfaces or caller contracts change. No concurrent feature branch currently changes the test file.

Record elapsed resolution (after stale persistence), first delivery attempt, rotation entry and completion using test-only timestamps. Assert resolution precedes expiry, delivery occurs for every case except activation/idle failures, and activation failure actually executes its injected callback. Idle must remain busy and record interrupt; readiness/completion must enter their intended wait with an unexpired context. Existing classification assertions distinguish terminal, admission and persistence outcomes. Diagnostics contain only phase durations and counts.

Sizing: one deliverable, approximately 120–170 written test/plan lines, zero new exported types/interfaces, zero consumer updates, two acceptance criteria, no new production rejection branches. Below all five limits.

## Concurrency model

Callbacks run synchronously on the resetting test goroutine; timestamps need no synchronization. Existing pool goroutines retain `runPoolReady` cancellation/join cleanup. No new goroutines.

## State transitions and identity reuse

| Event | Race coverage |
| --- | --- |
| Failed reset then one successor rotation, live/dormant | Every existing row of `TestResetThenRotate_FailedHandoffIsStale` |
| Readiness, idle and completion never become ready | Existing expiry rows, with explicit path assertions and finite deadlines |
| Terminal failure then successful reset of the successor | Existing terminal rows and their successful-recovery assertions |

## Error handling

Wrong-path execution fails explicitly even if a shared log classification matches. Preserve bounded rotation, older bytes, stale warning/fence, secret exclusion and recovery checks. Permission-based persistence skips remain visible and are reported, never counted as passes.

## Testing strategy

Add diagnostics/path assertions first and exercise the old deadline. Compile an old-policy race binary; then change only timing and compile the repaired binary. Run four instances of each binary pinned to CPU 0 with identical flags/counts, retaining verbose output converted by `go tool test2json`, phase timings and execution/pass/fail/skip counts under `/tmp/builder-3428/`. Report revisions and exact commands in the PR, including non-reproduction and the limits of this experiment; later passes cannot establish historical cause.

Run `go test -race -json -count=10 -timeout=10m -run '^TestResetThenRotate_FailedHandoffIsStale$' ./cmd/pyry`, uncached `go test -race -json -count=1 -timeout=10m ./cmd/pyry/...`, `go vet ./...` and `go build -o /tmp/builder-3428/pyry ./cmd/pyry`. The verifier owns the full-module gate.

## Open questions

Resolve finite bounds from measured contention before handoff. Historical cause remains unproven by design.

## Documentation handoff

Pending for the documentation stage, preserved under #3331 / PR #3348: `docs/knowledge/decisions/045-thread-index-over-history.md` (Status Proposed; Context, Decision, Reliability, Rejected options, Consequences, Migration and rollback), `docs/knowledge/decisions/042-daemon-built-thread.md` § The six approved decisions / Storage (link proposed ADR 045 without changing epoch policy), and `docs/knowledge/CATALOG.md` § Decisions (Proposed ADR 045 row). The exact requirements remain in #3331's committed plan § Documentation handoff; this timing repair changes none of them and does not certify unfinished historical gates.

## Security review

**Verdict:** PASS. **Reviewer:** builder self-review. **Date:** 2026-10-15.

- Trust boundaries / threat model: only `TestResetThenRotate_FailedHandoffIsStale` changes; existing synthetic subprocess-reply admission and stale-note framing assertions remain authoritative. No network or product boundary changes.
- Tokens / cryptography: no credential access, key generation or crypto changes; fixed secret sentinels remain excluded from captured logs.
- Files: existing `t.TempDir` isolation and chmod restoration remain; retain the root privilege skip. Diagnostics never include temporary paths or note content.
- Subprocesses / network and I/O: existing stub runner only; comparison binaries are test tooling, with finite timeouts. No new sockets, subprocess contract or dependencies.
- Errors / logs: preserve all existing secret-exclusion assertions; new timing diagnostics contain only durations, booleans and counts.
- Concurrency: callbacks remain synchronous; pool cleanup still cancels and joins. No new workers or locks.

## Revisions

- 2026-10-15: Retain the proposed two-second classification, 500 ms expiry and three-second rotation bounds. On Linux amd64 as uid 1001, four race binaries pinned to CPU 0, each repeating the parent ten times, passed with both policies: each policy ran/passed 600 tests/subtests, failed/skipped zero. Old 35 ms classification phases reached resolution in 0.234–23.856 ms, first write in 0.346–24.059 ms, rotation in 0.471–24.400 ms and completion in 1.179–31.742 ms. Repaired ranges were 0.189–12.228 / 0.245–12.371 / 0.311–14.210 / 0.767–18.401 ms respectively. Expiry rotation occurred at 35.281–54.383 ms old and 500.287–509.385 ms repaired; repaired completion stayed within 510.127 ms. Two seconds matches the fixture's default classification allowance; 500 ms gives over twenty times the largest observed pre-delivery delay while expiry remains fast and real. Three seconds allows the entire classification budget plus rotation, and all measured rotations are well inside it. These are finite regression bounds, not production latency promises. Historical failure was not reproduced and its cause remains unproven.
- Comparison source: base `2f698e131c7f48b1360ef02c51d8fb0247f6d2c8` plus plan commit `cc596cc0` and identical diagnostic/path assertions in both binaries; only the deadline selection and rotation ceiling differ. Compile with `go test -race -c -o /tmp/builder-3428/{old-policy,repaired}.test ./cmd/pyry`. For each policy, launch four `taskset -c 0 /tmp/builder-3428/<policy>.test -test.run=^TestResetThenRotate_FailedHandoffIsStale$ -test.count=10 -test.timeout=10m -test.v` processes concurrently, then convert each log with `go tool test2json -t -p github.com/pyrycode/pyrycode/cmd/pyry`. Raw logs/JSON remain in `/tmp/builder-3428/<policy>-{0,1,2,3}.{log,json}`. All persistence rows executed; the existing root skip remains unchanged. No claim is made about historical gate completion or causation.
- Final candidate `1366c40d`: the exact ten-repeat race command above ran/passed 150 tests/subtests, failed/skipped zero. The uncached package race suite passed in 300.476 seconds: 16,460 executed, 16,457 passed, zero failed, three skipped (`TestCodexApprovalLive`, `TestRunAgentRun_RealClaude`, `TestMemoryPathsCaseInsensitive`). The stale-handoff family ran/passed all 15 tests/subtests with no skips. Final `go vet ./...` and the scratch-output binary build passed. Only this evidence line changed after those checks; production/test sources are identical. The verifier owns the full-module gate; the ADR 045 documentation handoff remains pending in its existing stage.
