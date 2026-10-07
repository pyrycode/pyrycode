# #1480 — Publish the finding runner from observed argv only

## Files read

- `internal/e2e/realclaude/finding_run_record_test.go` → `finRecordRun`, `finRecordInputs`, `finRecordBuild`, `finRecordRunnerLabel`, `TestFinRecordRunnerAgreement`: assembled contract, ten input construction sites across the family, and shared label reduction.
- `internal/e2e/realclaude/finding_artifact_write_test.go` → `finWriteArtifacts`, `finWriteInputs`, `finWriteReadDir`: JSON/Markdown carriers, summary, declared-field census, and captured-byte containment checks.
- `internal/e2e/realclaude/finding_exit_path_probe_test.go` → `finExitRunProbe`: live record construction and independent argv-based gather gate.
- `internal/e2e/realclaude/finding_stream_exit_path_probe_test.go` → `finStreamExitRunProbe`: second live construction and the same gate contract.
- `internal/e2e/realclaude/teardown_liveness_probe_test.go` → `tdnRunnerFromArgv`: five fixed readings, including distinct indeterminate reasons; teardown contract is outside scope.
- `internal/e2e/realclaude/trailer_terminal_reason_test.go` → `trailReasonAgainstPath`: shared label/indeterminate consumers and explanation citing the retiring agreement helper.
- `internal/e2e/realclaude/trailer_admissibility_test.go` → `trailGate`: independent argv decisions and their offline regressions must remain unchanged.
- `internal/e2e/realclaude/finding_live_staging_test.go` → `TestFinLiveStageEnvDeltaNamesTheRunner`: comment citing the retiring test; staging helpers and deltas stay unchanged.
- `docs/knowledge/INDEX.md`, `docs/knowledge/features/e2e-realclaude.md`: tagged suite is excluded from ordinary checks; executed counts and explicit tagged compilation matter.
- `docs/knowledge/features/e2e-realclaude-trailer-terminal-reason-test-go.md` → assembled-record and artifact-writer entries: path-based field census and containment lessons; obsolete env rationale goes to documentation.
- `docs/knowledge/features/development-verification.md` → change surface, source-search, and protocol boundaries: supplement sparse codegraph results with source reads and inspect raw keys to prove omission.
- `CODING-STYLE.md`: table-driven stdlib tests, race checking, and symbol-only citations.

## Context

After #1348 the runner selector is obsolete, so comparing its env reading against observed argv publishes a misleading disagreement. This ticket removes that comparison from the assembled finding artifact and keeps the observed reading with its reason. No decision record is needed.

The sketch and plan fit the limits: approximately 250 total written lines including tests, comments, and plan; no new exported types, ten input construction sites, two acceptance criteria, and no new reject branches. The #1326 artifact-writer change is the sizing analogue. No numeric feature branch overlaps these files. PR #1478's `origin/fix/retire-vacuous-runner-comparison` deletes staging tests; our only staging-file edit corrects a reference in a comment, without changing its helpers or deltas.

## Design

Remove `RunnerFromEnv` from `finRecordInputs` and `finRecordRun`, and remove `RunnerAgreement` from the record. Delete `finRecordRunnerAgreement` and its agree/disagree constants. Retain `finRecordRunnerIndeterminate` as the independent label constant and retain `finRecordRunnerLabel` for existing trailer/gate consumers.

`finRecordBuild` continues publishing the full `tdnRunnerFromArgv(ClaudeCommand)` return in `runner_from_argv`. Its detail and `finWriteArtifacts`' Markdown summary describe the derived argv label, with no env comparison. JSON marshalling therefore omits both removed keys in `run.json` and the identical JSON fenced in `run.md`. Both probes and synthetic inputs stop supplying the env reading. Correct directly affected comments and scenario names without changing staging behavior or teardown records.

## Concurrency model

No new goroutines, locks, or shutdown paths. The builder remains pure; the existing synchronous writer is unchanged except for summary formatting. Offline scenarios use isolated temporary directories.

## Error handling

Keep the writer's existing marshal/write error reporting. Empty, neither-marker, and both-marker argv retain their distinct indeterminate readings instead of falling back to streamrunner. Detail bounds, safety claims, and captured-byte reduction remain enforced by existing tests.

## Testing strategy

Replace `TestFinRecordRunnerAgreement` with a table covering stream, historical PTY, both markers, neither marker, and empty argv. Assert the builder's full reading and expected label, structural removal of the record/input fields, detail wording, and both written artifacts' raw key absence and full reading. Check Markdown summary wording separately from its embedded JSON. Plant the existing captured-byte needle in nonempty argv and check written bytes for containment.

Run the new test before implementation and observe failure on the obsolete fields/summary. Then run the relevant offline finding-record, writer, containment, argv-reader, trailer/gate, and source-citation tests with `-race -tags e2e_realclaude`, explicitly confirming executed/pass counts with no skips. Compile/vet the tagged package, run `go vet ./...`, and build `./cmd/pyry` to scratch storage. The verifier owns the full-module gate; no live run is needed.

## Open questions

None. Preserve staging helpers/deltas, teardown records, and gate behavior as specified by the ticket.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/e2e-realclaude-trailer-terminal-reason-test-go.md`, update the `finding_run_record_test.go` assembled-record entry and the `finding_artifact_write_test.go` entry. State that the finding record keeps only `runner_from_argv`, derived from the observed Claude argv with its reason; `runner_from_env` and `runner_agreement` are removed, and the Markdown summary describes that one reading. Remove the named-field rationale based on the deleted env input.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. `finRecordBuild` reduces captured `ClaudeCommand` through `tdnRunnerFromArgv`, whose five returns are fixed strings; `finRecordProc` still drops row commands. Preserve those boundaries and the existing containment tests.
- [Tokens, secrets, credentials] No findings. New scenarios use synthetic argv with `trailNeedle` and never resolve Claude or obtain credentials. Only fixed runner labels enter detail/summary.
- [File operations] No findings. Reuse `finWriteArtifacts`' fixed `run.json`/`run.md` names and existing `0600` mode under test-owned temporary directories; no path or persistence behavior changes.
- [Subprocesses] No findings. The new test calls pure builders and the artifact writer only. Existing probes lose an env-reading argument but keep their execution and cleanup paths.
- [Cryptography] No findings. The artifact contract has no cryptographic operation or key lifecycle.
- [Network and I/O] No findings. The new path performs local file I/O only; existing network/read limits are untouched.
- [Errors, logs, telemetry] No findings. Detail and Markdown summary interpolate derived labels and scalars, never raw argv or embedded details. Existing bounded-detail and captured-byte assertions remain active.
- [Concurrency] No findings. No concurrent state or goroutines are introduced; each test scenario owns its directory.
- [Threat model] No findings. The relevant threat is captured subprocess bytes reaching public artifacts; fixed argv readings, row reduction, and containment tests enforce the boundary. Relay/mobile security contracts are not changed.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-07
