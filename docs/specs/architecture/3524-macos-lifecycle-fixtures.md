# macOS lifecycle fixture diagnosis

## Files read

- `internal/apps/health_test.go` → `healthFixture`, `healthStart`, `TestHealthHelperProcess`: accelerated startup/request budgets and child readiness.
- `internal/apps/health_failures_test.go` → `testOwnerHealthFailures`: manual ticks already preserve the six-result sequence.
- `internal/apps/reconcile_test.go` → `TestOwnerReconcileMetadata`: healthy identity and retry reset proof.
- `internal/apps/publication_test.go` → `publishFixture`: checked preparation precedes migration and service readiness.
- `internal/apps/publication_recovery_test.go` → `TestPublishMigrationRetirementUncertain`: live migration writer and journal/data barriers.
- `internal/apps/publication_retirement_probe_test.go` → `TestPublishRetirementProbeSequence`: transient versus persistent post-kill evidence.
- `internal/apps/retirement_errors_test.go` → `TestPublishServiceRetirementUncertain`: preactivation stop/remove/shutdown and joined cleanup.
- `internal/apps/health.go` → `supervise`: startup and request expiry are independent of manual runtime ticks.
- `internal/apps/candidate_process.go` → `candidateCommandWithCleanup`, `candidateDiagnostic`: private command/wait/cleanup evidence.
- `docs/knowledge/features/apps-package-testing.md` → Checked candidate fixtures, Publication fixtures, Health-failure observations: require positive retirement and phase-specific proof; isolated green runs do not attribute historical failures.
- `docs/knowledge/features/apps-package-lifecycle-and-restart-state.md` → Publication writers and completion: leader completion cannot authorize restoration.
- `/tmp/verifier-3513/baseline-check.log`, `check.log`: retained native failures span pre-service validation/readiness and already-cancelled/replaced generations.

## Context

Diagnose and repair lifecycle test fixture expiry without changing production health,
ownership or retirement policy. Historical logs lack per-command/per-request traces;
controlled experiments can prove fixture weaknesses, not attribute every old failure.
No decision record is needed. Branch checks found no overlapping edits to planned
test files; #3404 changes production recovery independently and is not a dependency.

## Design

Add a bounded startup delay to the existing health child configuration. Retain a
controlled delayed healthy response in the health-sequence and metadata fixtures,
with production startup/request budgets for these non-expiry scenarios. Expiry
tests keep their deliberate short budgets. Preserve manual runtime ticks and all
1/2/0/1/2/3, target, identity, metadata and retry assertions.

For asynchronous retirement publications, use local fixture helpers to launch with
cancellation and joined cleanup, record private candidate diagnostics, and await
readiness while also observing early Publish failure. Give preparation plus launch
a bounded observation allowance independent of the old five-second file wait.
Retain a controlled delayed-preparation regression that exceeds that old allowance.
Migration uncertainty must explicitly record its injected retirement invocation and
retain the real live writer. No production changes are planned absent new evidence.

Estimated written work: 500–650 lines including plan, evidence summary and tests;
zero exported types, zero production consumers, three acceptance criteria and no
new production reject branches. This is one deliverable: reliable, phase-proven
apps lifecycle fixtures. Recheck these limits before committing implementation.

## Concurrency model

Existing owner workers and subprocess groups retain their shutdown paths. The local
publication launcher cancels and joins Publish before fixture teardown, including
failed assertions. Diagnostic callbacks log fixed command labels and private errors
only; no output, environment or arbitrary arguments are captured.

## State transitions and identity reuse

| Event | Race-enabled coverage |
|---|---|
| Delayed startup then healthy generation | `TestOwnerHealthFailures`, `TestOwnerHealthFailuresDelayedObserver` |
| Repeated manual probes and failure reset/retirement | same two tests, full six-result sequence |
| Metadata revision with same healthy generation | `TestOwnerReconcileMetadata` |
| Slow preparation before preactivation retirement | delayed publication fixture regression and service uncertainty family |
| Leader exit while migration descendant remains live | `TestPublishMigrationRetirementUncertain` |
| Transient/post-kill/persistent uncertainty and rejected retry/removal | `TestPublishRetirementProbeSequence`, `TestPublishServiceRetirementUncertain` |

## Error handling

Readiness reports an early publication outcome instead of hiding its failed phase
behind a missing-file timeout. Private diagnostics distinguish wait, cleanup and
cancellation. Public static feedback, unavailable targets, retained journals/data,
positive retirement proof and persistent uncertainty remain unchanged.

## Testing strategy

First run the delayed fixture scenarios with old budgets and retain their failures,
then apply the fixture repair and run the same controlled scenarios. Retain exact
overlay/command identities where overlays are used. Run the six complete requested
families uncached with race detection, followed by the full apps package at normal
concurrency with supported real Node/npm. Run `go vet ./...` and build the CLI.
Commit redacted compressed raw output and counted `3524-macos-results.json`, including
failed attempts, comparison, revision, environment, timing and disjoint parent/leaf
counts. The verifier owns the full-module gate; #3525 owns final make check/build.

## Open questions

- Which controlled delays discriminate old fixture expiry from retirement errors?
- Does native acceptance reveal any additional cause requiring a scoped revision?

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/apps-package-testing.md`,
“Health-failure observations” and/or “Publication fixtures” with the demonstrated
cause, fixture contract, counted macOS result and remaining historical-attribution
limits. Preserve positive-retirement and persistent-uncertainty distinctions.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `probeHealth` retains strict matching app/release health; fixture delays confer no readiness themselves.
- Tokens/secrets: no credential access. `candidateDiagnostic` excludes subprocess output/environment; retained output must be redacted before commit.
- Files: fixed test-root markers retain private modes; no production path handling changes. Evidence lives under allowed testdata.
- Subprocesses: existing helper argv/environment and owned groups remain; asynchronous fixture work must be cancelled and joined on assertion failure (SHOULD FIX in launcher).
- Cryptography: no cryptographic operations change.
- Network/I/O: existing private loopback HTTP limits/timeouts remain; only non-expiry fixture owner budgets change.
- Errors/logs: public failure sanitization assertions remain; diagnostic records are test-only and private.
- Concurrency: configure delays/budgets before launch; channel completion synchronizes results and cleanup joins workers.
- Threat model: production ownership, unavailable-target and no-restoration-under-uncertainty barriers remain covered; no new product capability.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-17
