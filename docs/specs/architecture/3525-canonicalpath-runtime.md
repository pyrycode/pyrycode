# Canonical path runtime repair (#3525)

## Files read

- `internal/canonicalpath/path.go` → `Resolve`, `canonicalCase`, `matchEntry`: existing errors and portable casing rules.
- `internal/canonicalpath/path_test.go` → `TestResolve_Permissions`, `TestMatchEntry`: permission fallback and portable ambiguous selection.
- `docs/knowledge/features/canonicalpath-package.md` → Path and error contract, Testing: resolution is not confinement or a stable handle; preserve portable selection tests.
- `docs/knowledge/features/development-verification-test-execution-and-artifact-survival.md` → Diagnosing startup recovery runtime: matched complete-suite timings, active interval versus cumulative cost.
- `cmd/pyry/memory_transcript_reply_ownership_test.go` → `testReplyOwnership`: retain all terminal/ownership/chunk partitions unchanged.
- `cmd/pyry/reply_fallback_timing_test.go`, `reply_fallback_cancellation_test.go` → fallback deadlines, evidence and joined child cleanup remain unchanged.
- `Makefile`, `scripts/test-admission.py`, `scripts/check-restoration.py`: finite budgets, host admission, mandatory restoration policy and deliberate slow separation.
- External `tui-driver/pkg/tuidriver/cwd_darwin.go` → `canonicalisePath`: F_GETPATH analogue, but ordinary blocking open and unchecked close are unsuitable here.

## Context

Repeated sorted ancestor enumeration amplifies filesystem fanout on macOS. The retained unchanged-source diagnostic scanned a private ancestor 16 times, returning 560,016 entries after adding 35,000 siblings. A package timeout stack corroborates sorting a wide ancestor. This establishes avoidable repeated work, not complete attribution for every historical timeout. Required apps repair #3524 is merged in base `70c295a2`. No decision record is needed.

## Design

Keep the public `Resolve(string) (string, error)` contract. After Abs/EvalSymlinks succeeds, macOS asks the kernel for the current descriptor path using F_GETPATH. Open with O_EVTONLY and O_CLOEXEC: metadata access must neither wait on FIFO peers nor read file contents. Check close even when the probe fails; any native failure falls back to the existing component walk. Other platforms always use that portable walk. No cache or exported API is added. A private read-directory parameter allows operation-count tests without mutable global hooks. Selection remains exact first, then unique EqualFold, otherwise unchanged.

Fetch/feature diff review found no unmerged canonicalpath changes. #3504 consumes this repair; its checkpoint work is not a dependency or part of this diff.

## Concurrency model

Synchronous per-call descriptors, closed before return. No shared cache, locks, goroutines or cwd changes. Existing command child-process cleanup remains untouched.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Repeated resolution before/after adding 35,000 siblings | `TestResolveAncestorFanout` |
| Case-only rename | `TestResolveFreshIdentity` |
| Ancestor replacement under reused pathname | `TestResolveFreshIdentity` |
| Symlink retargeting and missing target | `TestResolveFreshIdentity` |
| Repeated successful and failing descriptor probes | `TestNativePathDescriptors` |
| FIFO/socket leaves without peer | `TestResolveSpecialLeaves` |

## Error handling

Abs/EvalSymlinks preserve wrapped error identities. Native open, descriptor query, invalid/empty returned path or close failure declines the optimization, retaining best-effort casing and permission behavior. No new resolution errors or logging.

## Testing strategy

First retain the old operation-count failure with the unchanged-source overlay, then require the same wide fixture to pass with the repair. Existing portable casing and permissions tests remain mandatory. Add freshness, special-leaf and descriptor-lifecycle coverage, including injected native failures and close errors. Compare complete ordinary command suites with identical Go/environment, fanout and admission; retain JSON per-test timings and finite package budgets. Run race checks on canonicalpath and cmd/pyry, vet and build. The dispatcher owns full-module gates; retain required full-corpus/full-gate acceptance as a handoff when not run in this builder stage. No live Claude or Linux checks.

## Open questions

- Measure the full-suite contribution separately from the demonstrated fanout amplification; record historical-attribution limits even if candidate passes.
- Confirm native metadata-open behavior and descriptor closure with macOS tests before handoff.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/canonicalpath-package.md`, “Path and error contract” and “Testing”, to describe the implemented macOS resolution path, fallback and descriptor lifecycle, rename/replacement freshness, wide-directory operation-count proof and filesystem-dependent skip limits. Preserve the exact/unique/ambiguous casing rules and the statement that resolution does not enforce confinement or provide a stable file handle.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `Resolve` consumes caller paths and returns metadata only; consumer trust/confinement checks stay in place. Existing Abs/EvalSymlinks errors remain authoritative.
- [Tokens] No credentials generated, stored or logged.
- [File operations] SHOULD FIX: native opens must use O_EVTONLY/O_CLOEXEC, validate absolute nonempty output, and check close on every opened descriptor; tests cover this. No path cache can retain stale identity. Resolution remains subject to filesystem changes after return, as before.
- [Subprocesses] No production subprocess changes; existing command secrecy and process-reaping assertions remain intact.
- [Cryptography] No cryptography changes.
- [Network and I/O] No network changes; special leaves require metadata-only opens so FIFO/socket resolution cannot wait on a peer.
- [Errors/logs/telemetry] Probe failure must be silent fallback, without exposing a new error or path log.
- [Concurrency] No mutable global test hooks, shared descriptor ownership or goroutines.
- [Threat model] This operation supplies path spelling, not authorization or a stable file handle. Existing consumer confinement remains required.

**Reviewer:** builder (self-review)
**Date:** 2026-10-17

Sizing: one deliverable; forecast <=650 written lines, 0 exported types, 0 consumer updates, 5 acceptance criteria, fewer than 10 native reject branches.

## Revisions

2026-10-17: The first native race run demonstrated that O_EVTONLY alone still waits when opening a FIFO without a peer. Add O_NONBLOCK to the metadata open; preserve the subprocess-bound special-leaf regression. The descriptor inventory also exposed Darwin ReadDir's fstatat failure for `/dev/fd`; enumerate descriptor names without stat instead. Both failures are retained as failed attempts, not attributed to the old resolver. [Apple's open contract](https://github.com/apple/darwin-xnu/blob/main/bsd/man/man2/open.2) specifies O_NONBLOCK for nonwaiting opens.

## Native macOS evidence

Source: base production `70c295a2e054166b6738d74201a1d5633bb3f8d9` (plan-only commit `d1dadfd5`), repaired production/tests `739d09be434d16fea9511e8f29bdcc0cd92df2d6`. macOS 26.6.2 arm64, Go 1.26.2, Node v25.8.0, npm 11.11.0. All test commands use host admission, GOMAXPROCS=4, uncached race instrumentation and explicit finite budgets. Neither complete command run overlapped another admitted test or our vet/build. No synthetic contenders were used; incidental host load was not controlled. Same TMPDIR and ancestor tree: 36,258 versus 36,294 entries in its wide shared ancestor, with identical other ancestor counts.

Durable raw logs, environment snapshots, exit codes, reproducer inputs and summaries are in [the committed evidence archive](../../../internal/canonicalpath/testdata/3525-macos-evidence.tar.gz). Archive SHA256: `fe64c70686328c32e6ec36dd5cda8123e569faf984905e4f4d7b4f41372affed`. Its `SHA256SUMS.json` pins 45 evidence files. `commands.json` records the exact commands/environment; `base-inputs.json` and `candidate-inputs.json` retain ancestor counts. JSON streams are raw stdout, including the make command prefix in `corpus.json`. Exit/stderr files preserve failures, admission waits and wall time. No result below is cached.

| Invocation / archive member | Named pass / fail / skip | Package seconds |
| --- | --- | ---: |
| Unchanged-source fanout diagnostic, `fanout-red.json` | 0 / 1 / 0 (expected failure) | 6.160 |
| First native attempt, `canonical-candidate.json` | 30 / 2 / 2 | 12.941 |
| Corrected native attempt, `canonical-final.json` | 38 / 0 / 2 | 6.611 |
| Optimization-disabled control, `fallback-control-red.json` | 0 / 1 / 0 (expected failure) | 9.344 |
| Final complete resolver suite, `canonical-verified.json` | 38 / 0 / 2 | 9.088 |
| Complete base command suite, `base-cmd.json` | 16,657 / 0 / 4 | 284.523 |
| Complete candidate command suite, `candidate-cmd.json` | 16,657 / 0 / 4 | 240.383 |
| Full checkpoint corpus, `corpus.json` | 202 / 0 / 0 | 198.720 |

Disjoint counts: resolver has six passed parents and 32 passed/two skipped leaves. Each command suite has 1,203 passed parents and 15,454 passed/four skipped leaves. The corpus has one parent and 201 cut leaves, completing 50,101 immediate/suffix comparisons over 30,000 entries. Corpus wall time was 435.53 seconds, including 232 seconds waiting for admission; package time excludes that queue. Vet and build were successful nonverbose checks (2.05 and 1.49 seconds wall time), with no named test counts.

Both command suites ran exactly `GOMAXPROCS=4 GOFLAGS= python3 scripts/test-admission.py -- go test -timeout=10m -race -count=1 -json ./cmd/pyry`. Normalizing only the random `TestMemoryPathValidation` temporary-directory number makes all 16,661 names/outcomes identical (normalized mapping SHA256 `f9d3cb2659daa846d86e2e3e2365d9263c9f07a6da181b6efb40800654c162ad`). No partition, assertion reduction or timeout increase was introduced.

| Named test | Base seconds | Candidate seconds |
| --- | ---: | ---: |
| `TestMemoryPathValidation` | 6.47 | 0.11 |
| `TestWithWorkdirReadFolder` | 1.84 | 0.00 (rounded) |
| `TestMemoryTranscriptReplyOwnershipControls` | 0.20 | 0.20 |
| `TestReplyFallbackProcessEvidence/own_deadline` | 29.91 | 29.91 |

The package improved by 44.14 seconds (15.5%) under these inputs. The last named result preceded the package result by 1.189 seconds on base and 1.178 seconds on candidate: neither run exhibited a cleanup stall. Neither reproduced the historical alarm. The deterministic diagnostic establishes repeated ancestor enumeration, not a complete causal attribution for the historical alarms or unrelated apps/child-memory failures. The unchanged-source diagnostic observed 16 scans/560,016 entries for the wide fixture; the final native regression observed zero scans/entries in both fixture states. Disabling the optimization made the committed regression fail with 16 scans per state, including 560,016 wide entries. Its elapsed times are not a performance comparison: it additionally checks public Resolve identity, and vet overlapped that operation-count-only control.

Failed native attempt: `TestNativePathDescriptors` failed on `/dev/fd` listing, and `TestResolveSpecialLeaves` killed its blocked FIFO helper at ten seconds. Both are repaired and passed in the complete final resolver run. The portable `TestMatchEntry` arms all executed. Resolver skips: case-insensitive volume cannot create case-differing siblings; deleted cwd still resolves and therefore cannot meet the test prerequisite. Command skips: real-Claude opt-in unset; delayed metadata readiness and feedback expiry intentionally belong to `make test-slow`; case-differing siblings unavailable. No new skips were added.

Open questions resolved: native descriptor cleanup, FIFO/socket liveness, freshness and deterministic scan coverage pass; the complete matched ordinary suite passes. Historical attribution remains limited as above. All transcript ownership/terminal/chunking and fallback deadline/cancellation/evidence/secrecy/reaping source assertions are unchanged.

Pending verifier-owned acceptance: complete macOS `make check`, including staticcheck, substrate guard and fake-daemon e2e, followed by `make build`; require `make test-exhaustive` if that gate does not invoke the full corpus. The builder role explicitly reserves the full-module gate for verification. None of those unrun later tiers is reported passed here. The builder separately completed `GOMAXPROCS=4 GOFLAGS=-json make test-exhaustive TEST_TIMEOUT=10m`. Documentation remains pending as specified above. Linux, live Claude and installation are outside this run.
