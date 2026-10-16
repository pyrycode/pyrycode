# Retained expansion work proof (#3473)

## Files read

- `internal/apps/artifact_resolution_test.go` → `TestRetainedRepeatedExpansion`, `testResolutionContext`, `TestRetainedResolutionCancellation`: deadline dependency and deterministic cancellation seam.
- `internal/apps/artifacts.go` → `scanArtifacts`, `artifactScan.walk`, `resolve`, `resolveParts`: filesystem traversal precedes memoized, cancellation-aware graph resolution.
- `internal/apps/artifact_install.go` → `InstallBuild`, `verifyAt`, `VerifyBuild`: copying, sealing, syncs and three scans share installation's context.
- `internal/apps/artifacts_test.go` → `testArtifactTree`, `testArtifactTemp`, `TestRetainedLongLinks`: canonical temporary roots, fixture contents and existing finite-chain/cycle coverage.
- `docs/knowledge/features/apps-package-testing.md` § Retained-build fixtures: cancellation, containment and independent identity assertions must survive.
- `docs/knowledge/features/apps-package-validation-and-stored-identity.md` § Retained artifact boundaries: completed resolutions avoid exponential work; cached paths stay immutable.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: mutation must fail the named proof.

## Context

The baseline gate for #3467 / PR #3469 timed out during installation at
`07b6b35e14aad0de688326fb36d81c54303c691c`. Its full verifier review confirms
the candidate passed this test, but does not establish why the baseline failed.
The current one-second integration deadlines conflate graph complexity with
filesystem and scheduling latency. No production behavior or ADR changes.

## Design

Keep the complete 20-level disk fixture and parallel execution. Walk that actual
fixture into an `artifactScan`, measuring traversal separately. Resolve the cold
deepest consumer first and then every link with the existing test cancellation
context, allowing at most 512 cancellation checkpoints. This bounds graph work
without charging filesystem latency or descheduling against complexity. Assert
the consumer's resolved path, not only successful return.

Run validation, installation and verification under one separate 30-second
overall context. Retain equal build-ID checks, sealed-build cleanup and phase
timings. The larger integration allowance is paired with the deterministic work
proof; it is not the complexity assertion. Existing cancellation tests remain
unchanged. No exported types or consumers change.

## Concurrency model

Each parallel test owns its fixture, scan and context. Resolution checkpoints are
observed synchronously by the calling goroutine; no new goroutine or shared state.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Cold expansion, then repeated cached link resolution in the same scan | `TestRetainedRepeatedExpansion` |
| Cancel during traversal, retry same scan, reject canceled cached traversal | `TestRetainedResolutionCancellation` (unchanged) |
| Validate, install and independently verify identical artifact identity | `TestRetainedRepeatedExpansion` |

## Error handling

Excess resolution work cancels its private context and fails finitely. Traversal,
validation, installation and verification errors fail with phase diagnostics.
The overall deadline remains independent of the package's unchanged timeout.

## Testing strategy

- Measure unchanged/candidate phase durations under identical race contention,
  using `GOMAXPROCS=2`, `GOFLAGS=-p=2`, `-count=1` and default package timeout.
- Use a controlled scheduling delay before resolution to expose the old deadline
  dependency; distinguish that experiment from the unattributed historical event.
- Disable completed-resolution cache reuse in a scratch overlay: the named proof
  must cancel within 512 checkpoints; the unmodified resolver must pass.
- Run at least 20 uncached race repetitions of expansion, cancellation and long
  links. Run the complete touched package with race detection, `go vet ./...`
  and `go build ./cmd/pyry`. Full `make check` belongs to the verifier gate under
  the builder role; record it pending rather than claim a focused pass proves it.
- Record revision, platform, Go version, commands, counts and skips. Required
  macOS matched execution needs an actual apps-capable native route: the currently
  supplied fleet recipes only run platform discovery, memory runtime, Android or
  Desktop checks, none execute apps. A probe is not acceptance evidence.

## Open questions

- Actual checkpoint use and mutation sensitivity: measure before choosing the
  final cap; revise if 512 does not leave reasonable linear headroom.
- Required macOS execution: inspect managed route availability and report any
  missing execution capability explicitly; do not substitute another recipe.

## Sizing and overlap

One proof deliverable, approximately 220 written lines including plan/evidence,
zero production changes, zero new exports, zero consumer updates, four acceptance
criteria, no new reject state machine. All builder limits hold. No other fetched
feature branch touches `internal/apps/artifact_resolution_test.go`.

## Security review

**Verdict:** PASS

- Trust boundaries / files: actual disk fixture feeds the same no-follow walk;
  all paths are test-owned canonical temporary roots. Containment and cycle
  checks remain production-owned and unchanged.
- Tokens / cryptography: no credentials, keys or cryptographic changes; identity
  equality retains independent validation/installation/verification calls.
- Subprocesses / network: no new subprocess or network path in the shipped test.
- Errors / logs: timings and checkpoint counts contain no user payload or secrets.
- Concurrency: the counting context is confined to synchronous graph resolution;
  it must not be shared across goroutines. Existing parallel fixture isolation
  and cleanup remain intact.
- Threat model: excessive acyclic expansion is the threat under test; a finite
  operation cap must reject disabled cache reuse. No isolation boundary changes.

**Reviewer:** builder (self-review). **Date:** 2026-10-15.

## Revisions

- 2026-10-15: Retain the controlled experiment as
  `internal/apps/testdata/3473-expansion-experiment.py` so the required native
  comparison can reproduce both overlays and all failed attempts. It compiles
  race binaries, runs each baseline/candidate pair concurrently, and records
  commands, platform, counts and logs outside the checkout. No new runtime
  behavior. Linux children share two CPUs; Darwin reports no affinity control.
  The measured graph uses 133 checkpoints; keep the planned 512 cap.
- 2026-10-16: Recovery runs directly on macOS arm64 with Go 1.26.2. Local
  native execution resolves the earlier missing-route question; no fleet task
  or second-OS run is needed. Keep the design and implementation unchanged.
  Retain native timings, expected failures and checks in
  `internal/apps/testdata/3473-macos-results.json`. The native evidence below
  supersedes the earlier macOS-blocked handoff, while the full-module gate
  remains verifier-owned.

## Investigation and validation evidence

Baseline: `4bc9790a35943200b86946a2968e10914e74b8c7`; candidate:
`9c0610c8` (test implementation). `git diff` against the historical baseline
`07b6b35e14aad0de688326fb36d81c54303c691c` is empty for the resolution test,
resolver and installer. The #3467 verifier evidence was retrieved using
`fleet-task.py evidence 3467`; its final PR #3469 review reports the original
installation timeout and explicitly leaves historical attribution unproven.

Environment: Linux amd64, Go 1.26.9, `GOMAXPROCS=2`, `GOFLAGS=-p=2`.
The historical machine instead used macOS arm64 and Go 1.26.2. These are not
interchangeable native results. The experiment runs two race binaries
concurrently on the same two Linux CPUs, with identical settings for each pair.
No other builder-owned check overlapped the final comparison.

Reproduction:
`python3 internal/apps/testdata/3473-expansion-experiment.py /tmp/builder-3473/final-experiment`.
The helper records its exact compile/execution commands in `results.json`;
each normal binary runs the three named tests 20 times with `-test.count=20`,
`-test.v`, and the unchanged `-test.timeout=10m`. Compiled binary execution has
no Go result cache. Delayed variants run each test once.

| Experiment | Executed | Passed | Failed | Skipped |
| --- | ---: | ---: | ---: | ---: |
| Unchanged test, 20 repetitions | 60 | 60 | 0 | 0 |
| Repaired test, 20 repetitions | 60 | 60 | 0 | 0 |
| Unchanged test, controlled installation delay | 3 | 2 | 1 | 0 |
| Repaired test, same controlled delay | 3 | 3 | 0 | 0 |
| Repaired proof, cache reuse disabled | 1 | 0 | 1 | 0 |

No focused skip reasons. The last two failing rows are expected diagnostic
failures, retained rather than discarded. The injected context sleeps 1.1s on
the first installation cancellation checkpoint, before resolution. The unchanged
test fails with the historical installation error text at 1.102729044s; candidate
installation succeeds at 1.108917336s. This demonstrates a scheduling dependency
outside graph resolution, not the exact cause of the historical failure.

Normal phase ranges in milliseconds:

| Phase | Unchanged min–max | Candidate min–max |
| --- | --- | --- |
| Validation | 2.567–12.792 | 2.311–11.753 |
| Installation | 9.193–25.304 | 8.191–17.626 |
| Verification | 1.850–4.101 | 1.525–6.101 |
| Fixture filesystem walk | Not separately instrumented | 0.808–13.402 |
| In-memory resolution | Not separately instrumented | 0.111–0.719 |

All timings include scheduling. Graph work is independently counted: 133/512
checkpoints on every candidate iteration, including the delayed case. Disabling
only the completed-cache lookup with a Go source overlay fails at exactly 512
checkpoints in 1.064958ms with `context canceled`, before integration starts.
No production file is modified on disk by these experiments. Normal unchanged
tests did not reproduce the historical timeout.

Initial preliminary comparisons had the same outcomes (60/60 passes for each
normal arm, one expected delayed-baseline failure, one expected mutation failure).
One sandboxed setup attempt executed zero tests because the Go cache was read
only; the authorized escalated rerun passed. It is not counted as test evidence.

`go vet ./...` and `make build BIN=/tmp/builder-3473/final-pyry` pass on the
committed candidate with the environment above. Build/vet execute zero tests.
The first complete `go test -race -count=1 -json ./internal/apps/...` passed:
1,007 executed, 993 passed, 0 failed, 14 skipped (supported Node/npm absent).
The final committed-source rerun and exact per-test skip reasons are recorded
in `internal/apps/testdata/3473-linux-results.json`: identical counts, 108.896s,
no unrelated failures. A third fresh helper run with explicit count/error checks
also produced 60/60 normal passes on each side, the expected delayed-baseline
failure, and the expected 512-checkpoint mutation failure. Its failed-attempt
logs and counts are retained in the same evidence file. Final written work is
under 500 added lines, zero production files, exports or consumer updates;
the original builder limits still hold.

Required macOS execution remains unperformed: `fleet-support.py` →
`native_recipe` implements only discovery for `platform-probe`, and its native
execution recipes exercise memoryruntime, Android or Desktop. None accepts an
apps test command. Requesting one of those would not supply this ticket's proof.
No irrelevant task was launched or probe claimed as acceptance. An apps-capable
managed native executor is required to close this validation gap.

Complete uncached `make check` remains pending for the verifier-owned full-module
gate under the builder role instructions; it has not been passed or substituted
with the focused result. The historical complete baseline gate's apps failure
and unrelated #3474 e2e failure are recorded in PR #3469's verifier review.

## Native recovery evidence (2026-10-16)

Revision `292138abd31e785fb10a38add6a7ab4cc7dd95a7`, macOS 26.6.2 arm64,
Go 1.26.2, `GOMAXPROCS=2`, `GOFLAGS=-p=2`. The unchanged-test overlay uses
`4bc9790a35943200b86946a2968e10914e74b8c7`; its test, resolver and installer
match the historical baseline. The resolver and installer also match this head.
Run: `python3 internal/apps/testdata/3473-expansion-experiment.py /tmp/builder-3473-macos/comparison-authorized`.
The helper records every exact compile/test command. Each race binary executes
20 repetitions without the Go result cache, retaining the ten-minute package
timeout. Baseline/candidate pairs run concurrently with identical concurrency
settings; Darwin provides no CPU affinity here. No other builder check overlapped
this comparison. Host-wide incidental activity is uncontrolled.

| Native experiment | Executed | Passed | Failed | Skipped |
| --- | ---: | ---: | ---: | ---: |
| Unchanged test, 20 repetitions | 60 | 60 | 0 | 0 |
| Repaired test, 20 repetitions | 60 | 60 | 0 | 0 |
| Unchanged test, controlled installation delay | 3 | 2 | 1 | 0 |
| Repaired test, same controlled delay | 3 | 3 | 0 | 0 |
| Repaired proof, cache reuse disabled | 1 | 0 | 1 | 0 |

All three named tests execute in each normal repetition; there are no focused
skips. Expected failures are retained in the native evidence, together with the
zero-test sandbox setup failure (Go cache access); its authorized rerun succeeded.

| Phase (milliseconds) | Unchanged min–max | Candidate min–max |
| --- | --- | --- |
| Validation | 1.568–3.501 | 1.112–3.328 |
| Installation | 97.668–132.481 | 91.391–138.076 |
| Verification | 1.343–5.253 | 1.513–3.140 |
| Fixture filesystem walk | Not separately instrumented | 0.675–2.145 |
| In-memory resolution | Not separately instrumented | 0.073–0.170 |

Normal and delayed candidate resolution always uses 133/512 checkpoints.
Disabling completed-resolution reuse fails at 512 in 0.292333ms, before
integration. The controlled delay fails baseline installation at 1.101744167s
with the historical error text, while candidate installation passes at
1.203819625s. Timings include scheduling; filesystem phases perform real I/O
and syncs, so these measurements do not isolate individual syscall costs.
They distinguish that elapsed work from counted in-memory resolution. Ordinary
native repetitions did not reproduce the historical timeout, whose cause remains
unproven. The original raw baseline log is available locally and confirms the
1.18s failure; its error wrapper locates deadline observation in `artifactScan.walk`,
without identifying the earlier work that exhausted it. PR #3469's final PASS
review also leaves historical scheduling attribution unknown.

`GOMAXPROCS=2 GOFLAGS=-p=2 go test -race -count=1 -json ./internal/apps/...`
passes in 227.771s: 1,007 executed, 989 passed, zero failed, 18 skipped.
Four skips are filesystem rejection of invalid UTF-8 filenames; fourteen require
supported Node/npm/template inputs. Exact per-test reasons are retained in the
native JSON. There are no unrelated failures or unfinished tests. Vet and build
overlapped this package run after the matched comparison completed.
`GOMAXPROCS=2 GOFLAGS=-p=2 go vet ./...` and
`GOMAXPROCS=2 GOFLAGS=-p=2 make build BIN=/tmp/builder-3473-macos/pyry`
both exit zero (zero tests). Full uncached `make check` remains pending the
verifier's gate, rather than being replaced by these scoped checks. No native
task is open. Earlier Linux results are historical evidence, outside this macOS
recovery's verification scope. Final written work remains below 800 lines,
with zero production changes, new exports or consumer updates.
