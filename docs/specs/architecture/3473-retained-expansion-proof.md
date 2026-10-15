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
