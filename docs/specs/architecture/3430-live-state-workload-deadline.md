# Complete live-state workload deadline

## Files read
- `internal/relay/v2session_livestate_test.go` → `TestLiveStateRun`, `TestLiveStatePacing`, `liveDrain`: complete snapshot and existing pacing/size proofs.
- `internal/relay/v2session_test.go` → `v2Recorder.snapshot`, `outbound`, `waitForEnvelopes`, `startManager`: header-only snapshots, two-second default and joined cleanup.
- `internal/relay/v2session_agentgate_test.go` → `openGateConn`: authenticated handshake and receive-cipher setup.
- `internal/relay/v2session_thread_test.go` → `threadConfig`, `threadPlain`: fixture capabilities and ordered authenticated decryption.
- `docs/specs/architecture/3364-large-thread-page-reply-wait.md` § Revisions: retain the merged large-page repair and its evidence.
- `docs/knowledge/features/development-verification.md` § Diagnosing reply timeouts: measure progress; do not infer historical attribution or repeated ciphertext copying.
- `docs/knowledge/features/v2-session-manager-concurrency.md` § Supplied current-state delivery: one reading per Run pass; clear and fresh reading take separate passes.
- `docs/knowledge/features/v2-session-manager-test-surface-same-package-unit-tests-internal-relay.md`: same-package harness and cleanup contracts.
- `CODING-STYLE.md`; `docs/protocol-mobile.md` § Security model: bounded concurrency and content-free diagnostics.

## Context
At base `2f698e131c7f48b1360ef02c51d8fb0247f6d2c8`, the workload uses a fixed two-second collection wait followed by unbounded decryption. Historical evidence reports 491/514 routing envelopes, without phase logs establishing its cause. One deliverable: a finite measured budget covering the unchanged complete workload. No production change or decision record is needed. The merged #3364 large-page repair stays intact; #3431 owns the later combined full gate.

Sizing before plan commit: about 220 written lines including evidence, zero exported types, one test consumer, three acceptance criteria and no production reject branches; within all five limits. No fetched feature branch overlaps the test file.

## Design
Keep `waitForEnvelopes` and every other caller unchanged. In `TestLiveStateRun`, establish one absolute deadline before submitting the snapshot-producing handshake. Locally drive the existing Noise handshake, collect and decrypt in order under that same deadline, including checks before buffered frames and after decryption. Expected routing count remains `pushQueueCap*2+2` (handshake, clear, all readings). Retain the active authenticated connection assertion, all sibling pacing/bounds tests and protocol limits.

Record elapsed first transport, halfway transport and final transport, plus decryption progress and completion, with counts only. Start with a provisional 15-second scratch measurement ceiling; choose the committed bound from identical controlled-contention old/repaired measurements with documented headroom. The budget never restarts on progress.

## Concurrency model
The existing manager goroutine owns sending and has `startManager`'s joined stop cleanup. Recorder access remains mutex-protected. Timing callbacks use an immutable start time and atomic count; decryption and its count remain on the test goroutine. One stopped ticker bounds idle polling; no new goroutine is needed.

## State transitions and identity reuse
None: no new lifecycle/identity state. Each `TestLiveStateRun` execution creates a fresh manager and Noise initiator; ordered decryption covers every frame. Existing `TestLiveStateOrdering`, `TestLiveStatePacing`, `TestLiveStateBounds` and retirement tests remain under the relay race suite.

## Error handling
Missing initial output or remaining output fails at the same deadline with elapsed time, received/expected routing count and decrypted/expected application count. Malformed or unauthenticated output fails using existing decoders. Expiry is checked even with buffered frames, including after the final decryption. No skip or reduced count is permitted.

## Testing strategy
Before implementing the repair, add transport diagnostics and reproduce the old wait failure using four race binaries pinned to one CPU. Compare repaired runs with the same process count, CPU and fixture; retain JSON execution/pass/skip counts and exact source revisions. Use scratch overlays to suppress output after a partial prefix and to delay buffered decryption beyond the deadline. Run the requested ten focused race executions, full relay race suite, module vet and binary build. Count every JSON test/subtest and report skips; require `TestThreadPageLargeReply` to execute and pass. Scratch artifacts stay under `/tmp/builder-3430`; durable results go here and in the PR.

## Open questions
- What whole-workload bound provides scheduling headroom over measured contention? Resolve with measurements before handoff.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: `TestLiveStateRun` continues paired-device Noise authentication via existing hello data and `ReadResp`; `threadPlain` still authenticates every application frame.
- Tokens: generated test keypairs and the existing fixture token remain local; new diagnostics contain only durations and counts.
- File operations: only this plan and test source change; measurement overlays/logs live outside the worktree.
- Subprocesses: no product subprocess change; scratch validation uses fixed Go test commands and CPU affinity.
- Cryptography: existing Noise primitives and sequential receive-cipher ownership remain unchanged; no nonce/key reuse is introduced.
- Network/I/O: this is a hermetic manager fixture, with existing queue and envelope limits unchanged; missing completion has one finite deadline.
- Errors/logs: timeout and progress diagnostics never print plaintext, ciphertext, credentials or identifiers.
- Concurrency: immutable timestamps and atomic transport counts avoid races; manager cleanup joins the existing worker and polling ticker is stopped.
- Threat model: protocol confidentiality, pairing and inert-content boundaries are unchanged; this test-only change adds no product trust boundary.

**Reviewer:** builder self-review. **Date:** 2026-10-15.

## Revisions
- 2026-10-15: Old diagnostics-only revision `45e3e892` failed 4/4 under four concurrent race tests plus 32 CPU-only Python contenders, all test/burner processes pinned to CPU 0. First transport was 223–384 ms, halfway transport (257/514) 1.687–1.872 s; failure elapsed 2.38–2.59 s with 375–386/514 received. Complete decryption was not reached. A preliminary repair with a 15-second measurement ceiling passed 4/4 with identical contention: first transport 218–344 ms, halfway transport 2.367–2.529 s, halfway decryption 2.469–2.659 s, final transport 4.101–4.382 s and complete 513/513 decryption 4.151–4.382 s. This was the implementation in the following commit with only its deadline literal temporarily set to 15 seconds. Choose **8 seconds**, about 83% headroom above the slowest measured completion. This resolves the open question and replaces the provisional ceiling, without resetting on progress. Four tests alone on CPU 0 passed the old wait in 0.40–0.43 s; the heavier controlled contention establishes scheduling headroom, not attribution for the unavailable historical logs.
- Measurement commands: compile the diagnostic baseline with `go test -race -c -o /tmp/builder-3430/old-relay.test ./internal/relay`. For each comparison launch 32 `taskset -c 0 python3 -c 'while True: pass'` processes, then launch four concurrent `go test -race -json -count=1 -timeout=10m -run '^TestLiveStateRun$' -exec 'taskset -c 0' ./internal/relay` commands, each into its own JSON log; wait for all four, then terminate and join all burners. Driver: `python3 /tmp/builder-3430/contend.py old-loaded 32` / `python3 /tmp/builder-3430/contend.py repaired-provisional 32`. JSON named counts: old 4 executions, 0 passes, 4 failures, 0 skips; preliminary repair 4 executions, 4 passes, 0 skips. Timing origin precedes manager startup and connection admission in both variants. No output contains payloads. The #3364/#3368 evidence remains unchanged in its original committed plan.
- 2026-10-15: Final test source `9615b531520bad46c085bd94dcbb26825072acab` passed the same controlled contention (`python3 /tmp/builder-3430/contend.py repaired 32`): four executions, four passes, zero skips. First transport 230–358 ms; halfway transport 2.080–2.198 s; halfway decryption 2.108–2.305 s; final transport 3.749–3.939 s; complete decryption 3.761–3.939 s. Every run received 514/514 routing envelopes and decrypted 513/513 application frames. Baseline source is `45e3e892d2e322f5bce3024201ebf042804e5103`; preliminary repaired source is exactly final source with `started.Add(15 * time.Second)` in place of eight seconds. Correction to the initial no-burner baseline range above: complete decryption actually took 0.390–0.403 s, with four executions/four passes/zero skips.
- 2026-10-15: Scratch overlays against `9615b531` confirmed bounded failure. The partial overlay increments the transport counter first and returns success without recording after `expected/2` captures, leaving the complete source workload and expected count intact. `go test -race -json -count=1 -timeout=10m -overlay=/tmp/builder-3430/partial-overlay.json -run '^TestLiveStateRun$' ./internal/relay` executed once, failed once, skipped zero: first output 11 ms, halfway transport 58 ms, halfway decryption 60 ms, expiry **8.000268264 s**, **257/514 received, 256/513 decrypted**. Progress did not extend the original deadline. The buffered overlay sleeps until deadline plus 10 ms immediately after decrypting the final buffered application frame; the otherwise identical command with `-overlay=/tmp/builder-3430/buffered-overlay.json` executed once, failed once, skipped zero at **8.010169237 s**, **514/514 received, 513/513 decrypted**. Thus completion beyond the budget cannot pass through the buffered-frame path. These expected scratch failures do not enter the candidate.
- 2026-10-15: Candidate `9615b531` passed `go test -race -json -count=10 -timeout=10m -run '^TestLiveStateRun$' ./internal/relay`: **10 executions, 10 passes, zero skips**, complete decryption 0.100–0.353 s. `go test -race -json -count=1 -timeout=10m ./internal/relay/...` passed **1,518/1,518 relay tests/subtests** and **455/455 handler tests/subtests**, **zero skips** (no skip reasons to report). `TestLiveStateRun` completed all decryption in 0.105 s; `TestThreadPageLargeReply` executed once and passed in 1.53 s, retaining the separate #3364 repair. `TestLiveStatePacing`, bounds, ordering and retirement coverage passed in that suite. `go vet ./...`, `go build -o /tmp/builder-3430/pyry ./cmd/pyry` and `git diff --check origin/main...HEAD` passed. Only plan evidence changes after this tested source revision. The verifier's full-module gate and #3431's later combined gate remain later-stage work, not claimed here; this hermetic test needs no live-Claude proof.
