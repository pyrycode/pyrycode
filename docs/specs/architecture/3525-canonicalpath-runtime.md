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

Keep the public `Resolve(string) (string, error)` contract. After Abs/EvalSymlinks succeeds, macOS asks the kernel for the current descriptor path using F_GETPATH. Open with O_EVTONLY, O_NONBLOCK and O_CLOEXEC: metadata access must neither wait on FIFO peers nor read file contents. Check close even when the probe fails; any native failure falls back to the existing component walk. Other platforms always use that portable walk. No cache or exported API is added. A private read-directory parameter allows operation-count tests without mutable global hooks. Selection remains exact first, then unique EqualFold, otherwise unchanged.

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

Satisfied in [canonicalpath's Path and error contract](../../knowledge/features/canonicalpath-package.md#path-and-error-contract) and [Testing](../../knowledge/features/canonicalpath-package.md#testing): implemented macOS resolution, portable fallback, checked descriptor lifecycle, rename/replacement/retarget freshness, wide-directory operation-count proof and filesystem-dependent skip limits. Exact/unique/ambiguous casing rules and the limitations on confinement and stable handles remain explicit. The verifier's additional evidence handoff is satisfied below.

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

The builder separately completed `GOMAXPROCS=4 GOFLAGS=-json make test-exhaustive TEST_TIMEOUT=10m`. The verifier subsequently completed the configured macOS gate and its own full corpus invocation, as recorded below. Linux, live Claude and installation are outside this ticket's verification scope.

## Completed verifier gate

[Verifier PASS](https://github.com/pyrycode/pyrycode/pull/3543#issuecomment-6227455003) covers native macOS arm64 at `9f2ac65ef703d596ee1db4778b426f17e0f3ea1f`. The dispatcher completed every `make check` component and subsequent `make build`; its receipt records completion at `2026-10-17T00:34:36.761Z`. These were individually configured steps, rather than a single aggregate `make check` command:

| Step | Command | Result |
| --- | --- | --- |
| 1 | `$AGENTS_REPO_PATH/bin/pyrycode-tool cite-guard` | exit 0 |
| 2 | `$AGENTS_REPO_PATH/bin/pyrycode-tool docs-guard` | exit 0 |
| 3 | `make vet` | exit 0 |
| 4 | `make test-tools` | exit 0; 11 Python tests passed |
| 5 | `python3 scripts/test-admission.py -- go test -timeout=30m -race -json ./...` | exit 0 |
| 6 | `python3 scripts/check-restoration.py` | exit 0; full sweep correctly declined by dependency selection |
| 7 | `make staticcheck` | exit 0 |
| 8 | `make substrate-guard` | exit 0 |
| 9 | `python3 scripts/test-admission.py -- go test -timeout=30m -tags e2e -race -count=1 -json ./internal/e2e/...` | exit 0 |
| 10 | `make build` | exit 0 |

The verifier then ran `GOMAXPROCS=4 GOFLAGS=-json /usr/bin/time -p make test-exhaustive TEST_TIMEOUT=10m`, using host admission. This discharged the full-corpus requirement without changing conditional restoration selection. It passed all 201 distinct cuts and 50,101 immediate/suffix comparisons, with package time 232.791 seconds and wall time 233.18 seconds. The configured gate's build preceded this separate corpus invocation. No green configured gate was rerun.

Recounted raw JSON results include named parents and subtests; skips are included in the executed column:

| Run | Executed | Passed | Failed | Skipped | Package outcomes |
| --- | ---: | ---: | ---: | ---: | --- |
| Dispatcher race unit gate | 28,087 | 28,057 | 0 | 30 | 55 passed; one package has no tests |
| Dispatcher fake-daemon e2e | 716 | 714 | 0 | 2 | seven passed |
| Verifier full checkpoint corpus | 202 | 202 | 0 | 0 | one passed |

There are no cache hits or unfinished named tests in those streams. Unit JSON spans 350.670 seconds from first to last event; e2e spans 203.223 seconds. These spans exclude any pre-stream build/admission wait. The resolver package passed in 10.315 seconds (38 named passes/two skips), and `cmd/pyry` passed in 350.646 seconds (16,657 named passes/four skips). `TestResolveAncestorFanout`, `TestResolveFreshIdentity`, `TestNativePathDescriptors`, `TestResolveSpecialLeaves` and every portable `TestMatchEntry` arm executed and passed. Command execution includes `TestMemoryPathValidation` (0.41s), `TestWithWorkdirReadFolder` (0.08s), `TestMemoryTranscriptReplyOwnershipControls` (0.47s) and `TestReplyFallbackProcessEvidence`, including `own_deadline` (29.91s). Guards, vet, staticcheck and build have successful nonverbose outcomes, without named Go-test counts. Individual wall times for those nonverbose steps and dispatcher concurrency/environment settings were not recorded in the receipt; builder settings are not inferred for them.

Unit skips: three case-differing-sibling prerequisites, one deleted-cwd prerequisite, one root-only ownership probe, one Linux-only parent-death probe, fourteen supported-Node/SQL prerequisites, four EILSEQ filename prerequisites, three intentional slow-tier proofs, one opt-in heap measurement, one live-Claude opt-in and the exhaustive corpus opt-in. The separate exhaustive invocation discharges the last omission. E2e skips: child-only fatal helper and supported-Node/template SQL acceptance. These skips establish none of the omitted behavior; exact names and reasons are retained in the raw streams and `verifier/gate-skips.json`.

The [committed verifier evidence archive](3525-verifier-evidence.tar.gz), SHA256 `4eb419226f5b088fc24fa9738728a6556caa0ccea520e46f6032ca575a56010e`, retains the full stdout/stderr pairs for all ten steps, the pass/prefix receipts, verifier verdict, exhaustive stdout/stderr/exit status and a recounted `summary.json`. Its `SHA256SUMS.json` verifies all 28 other members. Primary raw-log hashes:

| Archive member | SHA256 |
| --- | --- |
| `gate/verifier-gate_#3525_5.log` | `e46d935956859f3fcf841b094ed11c79fc6f54fe772b8f93c79683adc5e006c0` |
| `gate/verifier-gate_#3525_9.log` | `dfd1cdd8d964ccd6f7af093ae522d064275a2cc8d1ca1936bcae1b544c515f38` |
| `verifier/exhaustive.json` | `b507c7205f4792dd1c7c3ab55febe39453d4d0f2ea853fa848783822dba91995` |
| `verifier/exhaustive.stderr` | `a2ac56726b354404d3f2cb3708db6ee8cdaa562034bd9a6d27df8643ff5292d2` |

The dispatcher also retains verifier bundle `ff21cfa633438031f582bd35e71fc33bab47eb485b57127b588dd132a7e9c715`; its large gate logs are tails, whereas the committed archive above preserves the complete streams. Failed native attempts and unchanged-source/optimization-disabled controls remain in the builder archive linked above. The original historical timeout attempts remain failures, not passes inferred from later runs. Historical timeout causation remains unproven. The existing separate slow tier was not run; its assertions and routing are unchanged. Live Claude and installation were not run, and Linux is outside this macOS ticket's verification scope.
