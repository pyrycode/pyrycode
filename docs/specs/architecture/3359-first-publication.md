# Health-confirmed first publication (#3359)

## Files read

- `internal/apps/candidate.go` → `PrepareCandidate`: checked preparation changes history, not data or lifecycle.
- `internal/apps/candidate_record.go` → `recordCandidate`: preparation owns legitimate revision changes; identical bytes are reusable.
- `internal/apps/candidate_process.go` → `candidateCommand`: bounded output and whole-group join for commands.
- `internal/apps/artifact_install.go` → `VerifyBuild`: fresh immutable inventory verification.
- `internal/apps/backups.go`, `restores.go` → `SnapshotData`, `VerifyBackup`, `RestoreData`: complete durable backups and repeatable restoration.
- `internal/apps/owner.go`, `health.go`, `reconcile.go` → `Start`, `Stop`, `Shutdown`, `supervise`, `confirmRunning`: generation ownership, health and retirement.
- `internal/apps/runtime.go` → `prepareRelease`: data creation must follow the journal.
- `internal/apps/registry.go`, `lifecycle.go`, `releases.go` → `changeLocked`, `UpdatePublish`, `Remove`: atomic conditional storage and removal currently discarding journals.
- `internal/apps/storage.go` → `snapshotOps.write`: file-sync, rename and directory-sync boundary; post-rename uncertainty forbids later writes.
- `internal/apps/candidate_test.go`, `health_test.go`, `artifacts_test.go`: reuse checked-build and controlled-health fixtures.
- `internal/apps/retirement_errors_test.go` → `TestGenerationRetirementErrors`, `TestPublishServiceRetirementUncertain`: completion error aggregation, target/group retirement and retained preactivation recovery evidence.
- `internal/relay/connection_admission_test.go` → `TestAdmissionHeadersAndReconnect`: ordinary public-admission gate reproduction and safe lifecycle diagnostics.
- `internal/apps/releases_test.go` → `TestMetadataFailures`: removal must retain unresolved journals until their explicit completion.
- `docs/knowledge/features/apps-package.md` and its lifecycle/concurrency/testing topics: lock registry before owner; inspect actual descendants and durable snapshots; skipped Node supplies no evidence.
- `docs/knowledge/features/development-verification.md`: prove ordering directly and read executed outcomes.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Runtime, roots and service conventions / Registration, observed state and safe publication: minimal environment, migration deadline and atomic activation.

## Context

First publication must make a checked source usable without exposing a candidate or losing pre-publication data. Existing primitives provide preparation and storage, but no orchestration. No new decision record is needed. Current issue refinement supersedes the initial 760-line estimate: approximately 1200 written lines, two public result/error types, no existing consumer signature changes, five criteria and at least twelve failure boundaries. Recount against this plan confirms the line/branch overages. Actual parentage is #3125 → #3262 → #3359; the existing `needs-human:sizing` exception requires building. Without the depth exception, conditional journal transitions could be separated from orchestration (consumers: #3360 and #3263). No overlapping remote feature branch touches the planned existing files.

## Design

`Owner.Publish(ctx, serverID, appID, Toolchain)` synchronously returns a public lifecycle projection, identities, candidate version, revision and health/activation/recovery indicators, plus static phase failure feedback. Node must match the owner-selected executable; npm is daemon-selected. Admission under registry then owner locks rejects unknown/foreign/removed registrations, active releases, journals, removal and another publisher before preparation. Accepted publication durably requests available intent. A per-app operation token suppresses Start/reconciliation; intent is never a lock.

Preparation records immutable history. A temporary subscription cancels on manifest/intent/removal changes without entering owner methods; legitimate preparation/pending writes update the operation's expected revision. Fresh exact build verification retains candidate manifest/title privately. Pending/starting is committed once known, retaining the display title and absent active release. Snapshot and fresh verification precede a conditional journal commit with absent Previous and uncommitted decision. Only afterward may prepareRelease create data and a 60-second candidateCommand execute the compiled migrator with minimal identity/data environment. The migration group is joined before launch.

A private candidate generation uses existing supervision and health matching. Candidate health takes a conditional activation branch under registry/owner locks, committing title, active release, available/running, cleared pending/error and activation decision together. Ordinary startup remains unchanged. Existing Target checks prevent access before this commit. Runtime supervision continues on daemon lifetime after Publish returns. Journal clearing is conditional and durable; committed activation with failed finalization returns committed/recovery-pending feedback and never restores data.

Failure retires and joins the candidate group, then freshly verified RestoreData completes before conditional failure/journal clearing. Restoration runs independently of caller cancellation so stop/shutdown can join it. I/O or cleanup/finalization uncertainty retains the journal and backup and bars new publication; static app.io_failed describes restoration failure. Newer stopped intent/metadata is preserved. Builds/backups are retained rather than garbage-collected in this slice.

Registry Remove coordinates with its owner outside registry locks: block fresh admission, stop/cancel and join publication/service, then tombstone only with no unresolved journal. Restoration failure rejects removal while preserving stopped registration/evidence/files. Removal cannot recreate an identity. Owner shutdown joins publisher completion as well as generations.

## Concurrency model

One synchronous caller per admitted app owns preparation, snapshot, migration and restoration. A candidate launch worker and its child waiter are joined through generation done/exited channels. Existing supervision/reconciliation remain daemon workers. Registry callbacks only cancel or wake; all shared transitions lock registry then owner. No lock is held while joining workers or performing migration/restoration. Stop/shutdown cancel publication before joins; removal blocks admission until its durable tombstone or rejection.

## State transitions and identity reuse

| Event | Race-enabled proof |
|---|---|
| First publication / private pending → activation / runtime ownership | `TestPublishSuccess` |
| Simultaneous same-app publication and Start / independent apps | `TestPublishContention` |
| Build failure / unchanged data / healthy retry admission | `TestPublishFailures/build` |
| Fresh recorded-candidate verification / snapshot / fresh backup verification refusal before mutation | `TestPublishPreparationRefusal` |
| Health failure and retry using identical recorded bytes / completed journal | `TestPublishRetry` |
| Migration failure/deadline and whole-group retirement | `TestPublishFailures`, `TestPublishRetirement` |
| Migration leader exits with a surviving descendant | `TestPublishMigrationGroup` |
| Health mismatch/deadline / activation-write failure | `TestPublishFailures` |
| Finalization failure before/after activation / restore failure | `TestPublishFailures` |
| Stop/removal/shutdown during parked build or health / late completion | `TestPublishRetirement` |
| Caller cancellation / external stopped intent / obsolete health completion | `TestPublishRetirement`, `TestPublishStaleActivation` |
| Rejected host/app/toolchain / interrupted health retry on identical bytes | `TestPublishAdmission`, `TestPublishRetry` |
| Durable journal before data preparation / atomic activation | `TestPublishOrdering` |
| Removal with unresolved restoration evidence | `TestPublishRemovalEvidence` |
| Retirement failure / repeated Shutdown / completed or replaced generation / successful later removal | `TestGenerationRetirementErrors`, `TestPublishServiceRetirementUncertain` |

## Error handling

Admission and each phase return static safe feedback; child output and filesystem errors are never projected. Before mutation, failures need no restoration. After possible mutation, cleanup and restoration must succeed before clearing evidence. After committed activation, finalization failure preserves the committed decision. Cancellation cannot bypass recovery obligations. No restart recovery (#3263), update (#3360), local control (#3301) or daemon integration (#3266) is implemented.

## Testing strategy

Write failing tests first. Reuse artifact/candidate and health process doubles, with barriers and persistence injection to distinguish journal-before-mutation, activation atomicity, failure restoration and newer intent. Read registry.json without Open normalization, assert actual process/group retirement and absent targets before commit. Run the maintained generated template with supported real Node through checked preparation, migration, publication and Target.Do API request; record executed/pass/fail/skip counts. Run `go test -race ./internal/apps/...`, `go vet ./...`, `go build ./cmd/pyry`; the verifier owns the full-module gate. No live Claude.

## Open questions

None: unresolved removal recovery is rejected with evidence retained; cleanup uses a cancellation-independent context and shutdown joins it.

## Documentation handoff

Pending for documentation stage: `docs/hosted-apps.md`, “Registration and observed state”, “Migrations” and “Create, publish and update”: describe first publication, private candidate readiness, journal-before-data mutation, atomic health-before-activation, initial failure/restoration outcomes, committed-activation/finalization-pending feedback and safe error reporting. State that updates remain #3360 and startup recovery remains #3263 until those slices land.

## Security review

**Verdict:** PASS

- [Trust boundaries] Fresh VerifyBuild binds exact recorded bytes and private manifest; admission checks local identity before executing trusted app code.
- [Tokens] No credentials are created. Candidate commands and service use explicit environments excluding daemon/client secrets.
- [Files] Reuse descriptor-relative no-follow private directories, backups/restores and synced atomic registry snapshots; keep backup/build evidence after uncertain completion. Trusted same-account edits are outside the contract's isolation boundary.
- [Subprocesses] Direct argv only, supported selected Node, 60-second migration deadline, whole-group joining before restoration; no shell-built production commands.
- [Cryptography] Reuse shipped cryptographic UUID generation for publish identity and SHA-256 artifact verification; no new cryptography.
- [Network/I/O] Reuse bounded 4096-byte private health response, request timeout and startup deadline; targets remain generation-bound and require committed matching state.
- [Errors] Static phase/recovery feedback and LastError; raw output/paths never enter public projections.
- [Concurrency] Conditional activation and finalization hold registry then owner; stop/removal cancel ownership and join writers before data replacement or tombstone.
- [Threat model] Trusted local app/service code follows data/side-effect contract. Restart recovery OUT OF SCOPE (#3263), active updates OUT OF SCOPE (#3360), authenticated control boundary OUT OF SCOPE (#3301/#3266).

**Reviewer:** builder self-review. **Date:** 2026-10-14.

### Rework security review (2026-10-14)

**Verdict:** PASS after resolving verifier findings 1 and 2.

- [Subprocesses/recovery] MUST FIX resolved: `candidateRetirementError` distinguishes uncertain migration-group retirement from command failure. `finishPublicationFailure` cannot restore data while a migration writer may live; the backup/journal remains and removal cannot discard it. The EPERM regression inspects the real descendant and persisted evidence.
- [Files/concurrency] MUST FIX resolved: `snapshotOps.write` syncs the registry directory after rename. `errSnapshotUncertain` quarantines the registry against later writes and target exposure. An uncertain activation never restores previous data; a previously durable activation remains the decision after uncertain finalization. Boundary tests execute actual file sync/rename and inject directory failure.
- [Errors/trust boundaries] Static feedback excludes raw cleanup/storage diagnostics. Refusal tests tamper only after checked preparation or snapshot sealing, proving fresh verification rather than trusting cached results. Existing minimal subprocess environment, identity validation and retained private storage remain the contract.
- [Subprocesses/error handoff] MUST FIX on `6950f4ca731f` resolved: `completed` aggregates each ordinary or published generation's cleanup error after group cleanup/leader join, before completion visibility. Shutdown retains it across replacement and repeated calls. Stop/Remove still propagate it; preactivation failure retains the uncommitted journal rather than restoring through retirement uncertainty. `TestGenerationRetirementErrors` and `TestPublishServiceRetirementUncertain` inspect persisted decisions, unavailable targets and actual leader/descendant retirement, including successful later removal.

## Revisions

- 2026-10-14 (retirement handoff validation): The missing aggregation reproduced without fixture races in every published-generation completion arm and in preactivation Shutdown; ordinary generation controls passed. After repair, the focused shared-retirement/migration/error-context run executed/passed 23/23 named checks. The expanded ordinary/published retirement and preactivation family executed/passed 51/51 over three repetitions, and final assertions executed/passed 17/17, including both replacement descendants after successful removal. The full apps race run using real Node v24.15.0/npm 11.11.0 executed/passed 901/901 named tests, 0 failures/skips; publication family 64/64 and `TestPublishTemplate` 1/1, proving compiled migration, matching live health, persisted activation/finalization and committed-target preferences API access. Two later-added removal cases are covered by the expanded/final focused runs rather than included in that package-run count. Full relay race checks executed/passed 1973/1973 named tests, 0 failures/skips. `go vet ./...` and `go build -o /tmp/builder-3359/pyry-retirement ./cmd/pyry` passed. Dispatcher `make check` followed by `make build` on the final pushed candidate, independent review and documentation remain pending; none is certified by these builder results.

- 2026-10-14 (verifier finding 2 on `6950f4ca731f`): Add safe request-count, reconnect-iteration, live-connection and timestamped lifecycle-message diagnostics to `TestAdmissionHeadersAndReconnect`, without headers, tokens, peer text, frames or raw error attributes. Thirty ordinary admitted race-enabled repetitions executed/passed 90 named checks, 0 failures/skips; each supplied/absent-token arm made exactly three requests and observed both successful connection edges. The complete relay package race run executed/passed 1973 named tests, 0 failures/skips. The eight-second gate failure did not reproduce, so no relay production fault is claimed resolved. A fresh complete dispatcher `make check` followed by `make build` on the pushed candidate remains required before independent review; focused package checks are not that gate. Native-task recipes do not provide the general dispatcher gate, and builder instructions prohibit running it directly. Security reassessment [Errors/Network]: PASS; only existing static lifecycle messages and local counters/timestamps are projected by the diagnostic test.

- 2026-10-14 (verifier finding 1 on `6950f4ca731f`): Aggregate generation `cleanupErr` once in `completed`, after either supervisor has finished process-group cleanup and joined the leader, before closing `g.done`. Remove ordinary launch's duplicate aggregation. The owner's accumulated failures survive completion and generation replacement and remain visible to repeated Shutdown. `TestGenerationRetirementErrors` covers ordinary/published shutdown, Stop, rejected Remove, natural completion, replacement and successful later removal, including actual leader/descendant retirement, retired targets and exactly-once aggregation. `TestPublishServiceRetirementUncertain` covers preactivation Stop/Remove/Shutdown, safe feedback, retained uncommitted on-disk journal and no restoration after cleanup uncertainty. Security reassessment [Subprocesses/Concurrency/Errors]: PASS; common completion keeps registry-then-owner locking and makes retirement uncertainty observable without exposing raw diagnostics in public lifecycle feedback. Existing migration-retirement, directory-sync and fresh-verification protections remain in effect.

- 2026-10-14 (finalization rework validation): The delayed finalization arm executed/passed 30/30 race-enabled repetitions, with 0 failures/skips. The complete touched-package race run on Node v24.15.0/npm 11.11.0 executed 886 named tests: 886 passed, 0 failed, 0 skipped; publication family 60/60, failure family 10/10 and maintained `TestPublishTemplate` 1/1. Its compiled migration, matching live health, persisted activation/finalization and committed-target preferences request passed. `go vet ./...` and `go build -o /tmp/builder-3359/pyry ./cmd/pyry` passed. These are builder checks; a fresh complete dispatcher `make check` followed by `make build` and documentation remain pending later-stage work.

- 2026-10-14 (verifier finding 1 on `a22b0417`): Healthy publication fixtures keep the owner's production migration, startup and request budgets. Only `TestPublishFailures/migration deadline`, `TestPublishFailures/health` and the deliberately unhealthy first attempt in `TestPublishRetry` shorten their relevant deadline; retry restores the production startup budget. The finalization arm delays both compiled-entry child launches by 300 ms and requires its injected post-activation write failure and exact finalization feedback before inspecting the persisted committed journal. With the original budgets this regression failed in migration; scoping that budget alone then failed in health, proving both independent fixture traps. Security reassessment: PASS; these changes affect test timing and witnesses only, with no production trust, process-retirement or durability contract changes. The dispatcher must establish a fresh complete `make check` followed by `make build` on the pushed revision; focused builder results cannot certify that later-stage gate.

- 2026-10-14 (rework validation): `go test -race -count=1 -json ./internal/apps/...` with real Node v24.15.0/npm 11.12.1 executed 886 named tests: 886 passed, 0 failed, 0 skipped. Publication family: 60 executed/passed; the three findings' regressions: 17 executed/passed; `TestPublishTemplate`: 1 executed/passed, with compiled migration, matching live health, durable activation/finalization and a committed-target preferences API request. `go vet ./...` and `go build -o /tmp/builder-3359/pyry ./cmd/pyry` passed. Documentation and the verifier's full-module gate remain pending later-stage work.

- 2026-10-14 (verifier finding 3): Correct the coverage table: `TestPublishFailures/build` now calls `Owner.Publish` with a failing build, checks failed lifecycle/safe feedback, unchanged sentinel, absent database/target and no journal/history, then proves a healthy retry is admitted. `TestPublishPreparationRefusal` corrupts a newly recorded build, refuses snapshot copying, and corrupts a sealed backup before fresh verification; each path checks persisted failed fields and an unchanged data tree with no candidate service. Owner uses the existing zero-value `backupOps` implementation for scoped fault injection; its public primitive wrappers and production behavior are unchanged.

- 2026-10-14 (verifier finding 2): `snapshotOps.write` syncs the completed file, renames it and syncs the containing directory before reporting success. Post-rename sync failure returns `errSnapshotUncertain`; `Registry.changeLocked` keeps the last durable memory snapshot and bars all later writes, including no-ops, until recovery. Publication reports recovery pending and targets remain unusable. An uncertain activation write retires candidate service writers but never restores pre-publication data or claims committed activation; the renamed on-disk candidate decision and backup are retained for #3263. Uncertain finalization preserves the already committed activation and forbids restoration. `TestSnapshotDurabilityBoundary` proves real file/rename/directory ordering and injected failures, `TestPublishJournalDirectorySyncOrdering` parks the directory sync before any data preparation, and `TestPublishDurabilityUncertain` covers journal, activation and both finalizations. Security reassessment [Files/Concurrency]: rename alone establishes no crash durability; uncertain writes cannot be followed by writes derived from the old snapshot or destructive restoration. The revised design passes; no restart-recovery implementation is added.

- 2026-10-14 (verifier finding 1): `candidateCommandWithCleanup` preserves a distinct retirement error, and publication carries it through failure completion. A failed migration-group retirement forbids service launch and restoration; the uncommitted journal/verified backup remains, admission/removal stay barred and static recovery-pending feedback is returned. `TestPublishMigrationRetirementUncertain` injects EPERM after a real leader exits, checks its live descendant and retained on-disk evidence, and proves rejected removal preserves the stopped registration. Security reassessment [Subprocesses/Files/Errors]: leader join is insufficient; restoration requires positively established whole-group retirement. Uncertainty retains evidence without exposing raw errors or replacing data. The revised design passes this security check; restart recovery remains #3263.

- 2026-10-14: `TestPublishTemplate` executed twice on real Node v24.15.0/npm 11.12.0 and failed at preparation's test phase. Production `candidateEnvironment` puts TMPDIR beneath staging; maintained `fixture` puts test databases there, and `environment` correctly rejects staging data paths. Diagnostic template tests executed 17 checks: 3 passed, 14 failed, 0 skipped. Filed the inherited checked-preparation defect as #3363 (Backlog, priority:low) and linked it as a blocker. Keep the publication proof skipped with that issue until its repair lands, then remove the skip and run the complete real Node/template arm. Its required successful evidence remains outstanding; no acceptance claim is made from the skip.
- 2026-10-14: Failure-finalization injection showed that journal clearing can fail after restoration. The fallback persists failed/stopped state with the journal retained and static `app.io_failed` where storage permits. Newer stopped intent and unresolved removal recovery retain their evidence; removal is rejected until recovery completes.
- 2026-10-14: The maintained `environment` contract requires a positive reserved port even for migration. A stricter migrator fixture failed the initial zero-port environment. Reserve one endpoint across migration and candidate spawn, and start the startup deadline from actual spawn; release the reservation on failed publication or generation retirement.
- 2026-10-14: Install the publication cancellation subscription inside locked admission (`subscribeLocked`) so external stopped → available intent reuse cannot pass unobserved before preparation. `TestPublishRetirement` covers repeated intent while preserving newer title/state. Candidate health feedback is current live usability, separate from the durable activation decision.
- 2026-10-14: #3363 merged before this resumed build. Remove its unconditional `TestPublishTemplate` skip and assert matching public lifecycle/identity, the persisted activation and cleared journal, and migrated API preferences. The first re-enabled run completed migration, health-confirmed publication and API access but failed test teardown because retained builds are read-only. Apply `testWritableBuild` only during cleanup, after owner shutdown, so temporary-directory cleanup succeeds without weakening retained-build permissions during the proof.
- 2026-10-14: Final supported-toolchain proof on Node v24.15.0/npm 11.12.1: `go test -race -count=1 -json ./internal/apps/...` executed 869 named tests, 869 passed, 0 failed, 0 skipped; publication family executed/passed 48/48, and `TestPublishTemplate` executed/passed 1/1 with no failures or skips. Its compiled migration, matching live health, durable activation/finalization and committed-target preferences request all passed. `go vet ./...` and `go build -o /tmp/builder-3359/pyry ./cmd/pyry` passed. #3363 no longer blocks this ticket; documentation and the verifier's full-module gate remain later-stage handoffs.
