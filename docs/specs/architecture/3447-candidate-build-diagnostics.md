# Attributable maintained-template preparation failures (#3447)

## Files read
- `internal/apps/candidate_process.go` → `candidateCommandWithCleanup`, `candidateCleanupWithKill`, `candidateGroupLive`: joined command and group-retirement boundary.
- `internal/apps/candidate.go` → `PrepareCandidate`: static feedback discards internal command errors.
- `internal/apps/publication_update_template_test.go` → `TestUpdateTemplateSnapshot`, `TestUpdateTemplateRollback`: initialized update, backup and complete rollback contracts.
- `internal/apps/candidate_test.go` → `TestCandidateHelperProcess`, `testCandidate`: selected-tool candidate fixture with child-output secret sentinel.
- `internal/apps/candidate_process_test.go` → `TestCandidateDescendants`: artifacts produced on descendant termination discriminate cleanup ordering.
- `internal/apps/retirement_errors_test.go` → `TestGenerationRetirementErrors`, `TestPublishServiceRetirementUncertain`: denied retirement preserves recovery evidence.
- `internal/apps/cleanup_probe_test.go` → `TestOwnerCleanupProbeRecovery`: inspection uncertainty differs from denied termination.
- `templates/hosted-app/source/scripts/build.mjs` → service compilation and esbuild operations: build feedback alone cannot identify failure.
- `docs/knowledge/features/apps-package-testing.md` § Checked candidate fixtures and Publication fixtures: unchanged child I/O and complete restored schema/ledger are necessary proof.
- `docs/knowledge/features/apps-package-concurrency-and-persistence.md` → checked candidate ordering and recovery barriers.
- `docs/knowledge/features/development-verification.md` § Captures and live evidence: retain failures and count executed assertions.
- `internal/apps/testdata/3418-macos-results.json` on `origin/feature/3418`: prior failures lack effective internal attribution; passing diagnostics changed child I/O.

## Context
Candidate preparation replaces command and cleanup failures with safe static feedback. The verified native diagnostic overlay observed `candidateGroupLive` returning EPERM after TERM in `TestPublishFailures/health`; the immediate return prevented its expected recovery outcome. The first isolated template run passed all three leaves, so historical build failures remain unattributed. A controlled candidate build will apply this observed inspection failure and require subsequent positive absence. One deliverable: an attributable and repaired preparation failure, with a discriminating fixture and complete native gate. No decision record needed. No concurrent feature branch changes the planned production files; #3418 changes the separate `Owner.cleanup` block.

## Design
Keep command diagnostics private and bounded: fixed command identity, start/wait exit status, cancellation, and retirement/probe errors separately. Never collect child output or copy arbitrary arguments/environment into diagnostics. Preserve ordinary non-version stdout/stderr handling. A private context observer lets fixture tests retain structured outcomes without changing public APIs or global mutable hooks. Retain the original diagnostic overlay as evidence; it prints only daemon-selected test command arguments and wait/cleanup outcomes, with no child output, and changes parent logging timing.

Repair immediate failure on group-inspection uncertainty: keep signalling failures fatal, retain the latest probe error within the existing TERM/KILL windows, and require a later positive absence result before accepting cleanup. Persistent inspection uncertainty remains an error after the final deadline. `candidateRetirementError` and all publication recovery barriers remain unchanged. Add a private cleanup seam accepting a group probe for deterministic candidate-fixture tests; production still calls the unchanged platform probe. Reinspection may disprove uncertainty but never proves the exact kernel state during EPERM.

## Concurrency model
Command observer runs synchronously after joined leader and bounded cleanup; no new goroutines or locks. Existing wait goroutine exits through `cmd.Wait`; cancellation kills the group and joins it. Each context owns its observer; fixture callbacks are concurrency safe. Group cleanup remains bounded independently of caller cancellation so writers cannot escape cancellation.

## State transitions and identity reuse
| Event | Race test |
|---|---|
| Leader exit followed by transient uncertain group inspection then absence | `TestCandidateCleanupProbeRecovery` |
| Group remains uncertain through forced kill/final deadline | `TestCandidateCleanupProbeRecovery` |
| Denied signal followed by apparent absence | `TestCandidateCleanupProbeRecovery` |
| Live descendant remains after joined leader; artifact validation waits for retirement | `TestCandidateDescendants` |
| Cancellation with leader and descendant live | `TestCandidateCancellationDescendants` |
| Consecutive publications/update under the same app identity | `TestUpdateTemplateSnapshot` |
| Candidate migration/startup failure restores predecessor under the same app identity | `TestUpdateTemplateRollback` |

## Error handling
Command exits and retirement errors stay independently inspectable internally. External `CandidateFailure`, `PublishFailure` and lifecycle messages remain static and do not unwrap diagnostics. Denied signals and final uncertain probes refuse candidate recording; publication keeps its recovery barriers. No fallback treats EPERM as absence.

## Testing strategy
Write the controlled candidate cleanup regression first and demonstrate its failure before the repair. Cover transient, persistent, denied-signal and still-live outcomes, plus separate command/cleanup diagnostics and safe public errors. Preserve existing descendant/artifact/immutable-build tests. Run complete uncached `go test -race -json ./internal/apps -count=1` with supported real Node/npm and normal concurrency, `go vet ./...`, and build `cmd/pyry` into scratch. Retain every attempted diagnostic/package run under `internal/apps/testdata/3447-*`: revision/dirty overlay hashes, OS/tools, exact command, supplied/resolved TMPDIR, concurrency, exit, separate parent/leaf counts, skips and logs. The verifier owns the full-module race suite; no live Claude needed.

## Open questions
- Historical template failures cannot be conclusively attributed without a captured failing command; retain this limit even if the complete gate passes. Controlled candidate-build failure and native cleanup observation establish the repaired branch.
- Complete native gate outcome: record all failures, including unrelated ones.

## Documentation handoff
- Pending documentation stage: `docs/knowledge/features/apps-package-testing.md`, “Checked candidate fixtures”: document the demonstrated cause, internal-versus-public diagnostic boundary, regression and counted macOS evidence link. State attribution limits and any unrelated remaining gate failures explicitly.

## Security review
**Verdict:** PASS

**Findings:**
- [Trust boundaries/errors/telemetry] SHOULD FIX: arbitrary command args and child output can contain secrets. Use fixed command labels and bounded numeric/error outcomes only; public feedback remains static and non-unwrapping. Tests retain a secret sentinel assertion.
- [Tokens/cryptography] No credential or cryptographic changes. Candidate environment retains its explicit private allowlist and does not inherit host secrets.
- [Files] Existing no-follow staging, private temporary directories, immutable artifact checks and retention stay intact. Evidence contains only redacted test outcomes; no app payloads or child output.
- [Subprocesses] MUST FIX resolved in design: uncertainty never establishes retirement. Joined leader plus positive group absence are required; denied signals and persistent errors remain failures, preserving recovery evidence.
- [Network/I/O] No new network surface or child-output pipe. Existing health and npm fixture behavior remain unchanged.
- [Concurrency] Observer is per context, synchronous, and immutable after installation; no global hook or new goroutine. Existing registry → owner lock order stays intact.
- [Threat model] Trusted local app commands are unsandboxed by contract. This repair neither broadens their authority nor attempts to isolate malicious same-user code.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-15

Sizing: approximately 650 written lines including tests, diagnostics, plan and evidence metadata; 0 exported types/interfaces, 0 consumer migrations, 3 acceptance criteria, fewer than 10 distinct rejection branches. Recount before implementation remains within the builder ceiling.

## Revisions
- 2026-10-15: The diagnostic package captured `TestPublishTemplate` failing its actual npm build with wait success and cleanup EPERM after TERM. This attributes one maintained-template build failure to inspection uncertainty, rather than compilation; the earlier #3418 failures still lack command-level attribution. The controlled pre-repair build test and three probe-sequence arms failed on immediate error return, while persistent/denied/live safety arms passed. No change to the planned repair contract.
- 2026-10-15 validation: native baseline also captured successful build waits followed by inspection EPERM in the snapshot and both rollback targets. The clean `e3623103` complete race run executed/passed all three target leaves with no skips, including initialized activation/backup and the intended migration/startup failures followed by full restoration and old-release health. Package verdict remains FAIL: parents 105 run/104 pass/1 fail/0 skip; leaves 912 run/906 pass/2 fail/4 skip. `TestPublishRetirement/health/remove` reports denied signalling and `/health/intent_cycle` retains retirement metadata; this existing retirement family is tracked by #3418. The latter record alone does not identify its precise signal/probe cause. Preserve these failures and do not introduce a reverse dependency or broaden this repair. The four skips are filesystem rejection of invalid UTF-8 names. Evidence, all six attempts and attribution limits are retained in `internal/apps/testdata/3447-macos-results.json`; this is not a green full-package claim. Focused repair 22/22 leaves and diagnostic boundary 3/3 leaves passed without skips; vet and build passed. Open questions are resolved to the stated attribution limits and failing package verdict. Documentation and the verifier's full-module gate remain later-stage work.
