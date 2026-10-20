# Coordinated main replay acceptance

## Files read

- `internal/e2e/recorded_lifecycle_suite_test.go` → `runRecordedLifecycleSuite`: original root counts and final immutable audit.
- `internal/e2e/internal/claudetape/lifecycle_artifacts.go` → `AuditLifecycleArtifacts`: closed inventories and pinned cleanup digest.
- `internal/e2e/realclaude/lifecycle_recording_test.go` → `finishLifecycleRecording`: live prefix seal and root acknowledgement.
- The existing 3668 and 3673 plans/evidence indexes, current verifier configuration and `docs/testing/daemon-recordings.md`.

## Context

The user requested coordinated manual acceptance and reconciliation of the held flaky-test repairs. Composer PR #3701, session-resume PR #3680, compact synchronization PR #3704 and native wrap-up PR #3676 have merged. Final main is `25e5e50dbc99ce83655f3d1fa8c2c9ed0607c7f0`, tree `60d2b847d99fd6ebdc750dbd7aaf91b24df2330f`. Its complete tree equals integration source `f09dcefaba359f15b982024423ae6d3f2c560cf3`.

The integration tree passed all ten current configured hermetic steps, 28,122 race-unit leaves and 1,928 fake-daemon leaves, including 70 recorded scenarios. Its complete live suite passed 2,248 leaves with zero failures and 27 named skips; the required original bounded wrap-up root passed without a skip. Original failures and unavailable historical measurements remain separate evidence.

The final-main four-scenario run has now passed in context order: bounded wrap-up, session-resume, conversation-system-prompt, conversation-active-switch. Its original roots and complete audits must be independently counted from retained reports. Post-merge overlap repetitions are running; final main's configured shared gate remains pending. This plan records completed verification honestly and prospectively precedes the new evidence artifact and documentation changes. It does not claim to precede the already completed runtime checks or any earlier repair.

## Change

Commit `internal/e2e/testdata/3674-recorded-main-evidence.json` after validating the retained reports. Record exact source/tree and prerequisite merge identities, Linux/toolchain, actual commands/selection/order, source/helper/daemon identities, fixture hashes, per-scenario original/outer counts, complete gate results, raw-report locations/hashes, fresh process/home state and known admission order. Retain original failed sources and unknown historical conditions explicitly.

Also retain the evidence-only resolutions for the overlapping MCP backpressure, compact and startup tickets. Reuse the existing controlled fails-before/passes-after proofs; add no duplicate runtime test or implementation. Recover available historical MCP candidate/base reports and explicitly retain missing normalized-input evidence as unknown.

Complete the existing session-resume and Composer documentation handoffs in `docs/testing/daemon-recordings.md`: per-process executable identity, current config checks, live seal versus audit return, acknowledgement before successful terminal publication, and complete current owned PID witness before cancellation.

No daemon, protocol, fixture, assertion, timeout, matching rule or authorization state changes belong in this evidence/documentation branch. A new failed gate is retained and investigated by exact name; it cannot be replaced by an older or focused pass. Distinct open Composer recurrence and macOS worker-observation tickets remain separate.

## Security review

Verdict: PASS for the planned evidence and documentation changes.

- Evidence is descriptive and supplies no replay or workflow authority.
- Commit only bounded counts, names, public source identities and SHA-256 values; no credentials, argv/environment dumps, native message contents or private raw logs.
- Raw reports and generated captures remain in private retained directories and protected chat workspace archives. No generated capture is discarded or silently substituted into a reviewed fixture.
- Keep original kernel ownership, strict inputs, physical Wait/EOF, pinned inventories and all ordinary gate/approval checks unchanged.
- The earlier rejected BLOCKED handoff remains preserved. Its exact refusal was reviewed and a protected operator continuation recorded; never replay that write or manufacture a pipeline PASS/FAIL.

## Testing and completion

Validate all JSON reports, original-root counts and fixture/report digests. Run the current configured shared gate on final main and retain every step. Verify the evidence/doc-only diff with JSON parsing, whitespace checks and the maintained documentation guard; no test is needed solely to mirror an informational JSON artifact.

After this reviewed artifact lands, reconcile covered tickets through the protected central workflow using fresh revisions and confirmed idle reservations. Preserve distinct failures and unrelated restrictions. Resume daemon admissions gradually only after final main acceptance; keep active runs and user changes intact.


## Completed validation

Final main `25e5e50d` passed all ten current configured shared-gate steps. Fresh race-unit leaves: 28,122 passed, zero failed, 27 skipped. Fake-daemon leaves: 1,928 passed, zero failed, two skipped; all 70 recorded scenarios passed. The four original scenario/daemon-root pairs each passed 1/1 with zero skips and complete final audits. Their retained source-bound reports supply independent original-root counts. Session-resume's pinned prefix and cleanup digests match.

Post-merge checks passed 140 MCP leaves over ten full repetitions, including ten passes of each reported backpressure leaf; the compact selection passed 160 events (100 leaves) over twenty repetitions, including twenty passes of the named admission leaf and both boundary roots. The combined send-file/startup selection passed both original roots with complete audits. Tagged source/publication/capture controls passed 22 leaves with race detection. The already accepted full live source has the identical committed tree and passed 2,248 leaves with zero failures and 27 named skips; its required original bounded wrap-up root passed without a skip.

The 526-line generated evidence record was reviewed against the raw-report hashes, exact merge ancestry and source/helper/daemon/fixture identities. JSON parsing and whitespace checks pass. This branch contains only the evidence artifact, this plan and the existing documentation handoff completion, approximately 600 written lines. Original failures and unmeasured historical input/scheduling phases remain explicitly preserved. Generated native captures were privately retained without replacing final main's reviewed fixtures. No runtime implementation or regression test was added by this acceptance branch.
