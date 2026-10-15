# Confined SQLite subprocess cwd

## Files read
- `internal/apps/backup_sqlite.go` → `runBackupNode`, `sqliteSnapshot`, `verifyBackupSQLite`: descriptor-path cwd and SQL paths.
- `internal/apps/backups.go` → `backupDir`, `snapshot`, `verify`: no-follow directory selection, inventory and evidence publication.
- `internal/apps/runtime.go` → `privateDir`, `regularAt`, `supportedNode`: private filesystem and runtime boundaries.
- `internal/apps/owner_test.go` → `TestOwnerSupportedNode`: real supported Node launch and environment isolation assertion.
- `internal/apps/backups_test.go` → `TestBackupSQLiteWAL`: WAL-only rows, ledger, standalone mode and fresh immutable verification.
- `internal/apps/backups_rework_test.go` → `TestBackupSQLiteCancellation`, `TestBackupSQLiteStorageErrors`: joining and explicit failure without evidence.
- `internal/apps/publication_update_template_test.go` → `TestUpdateTemplateRollback`, `updateSQL`: maintained-template publication and saved preferences.
- `origin/feature/3404` → `TestCommittedTemplateUpdateSnapshot`: carry the standalone assertion, without recovery code or its macOS skip.
- `docs/knowledge/features/apps-package.md` § Persistent-data snapshots and `apps-package-testing.md` § Testing: absent data and skips cannot prove initialized SQL behavior.
- `docs/knowledge/features/development-verification.md`: scoped checks and durable evidence.
- `CODING-STYLE.md`: subprocess tests, shutdown and contract comments.

## Context
macOS rejects `/dev/fd/<directory fd>` as an exec cwd. A pathname fallback would lose descriptor confinement. One repair delivers portable initialized snapshot/verification; recovery remains independent. No concurrent feature branch changes the selected production/test files.

## Design
Add a private re-exec bootstrap in `internal/apps/backup_node.go`. `runBackupNode` passes the no-follow-opened directory as child fd 3 through `ExtraFiles`. An exact private argv shape invokes the bootstrap before main: validate the directory, `fchdir`, close the fd and `exec` the selected absolute Node executable with fixed SQL/mode and an empty environment. No shell, descriptor pathname or parent-process chdir is used. Bootstrap and Node failures remain explicit.

Snapshot SQL must not traverse `../data` after entering the work directory: move copied main/sidecar files into the selected work directory with descriptor-relative `renameat`, run SQLite on local filenames, install the standalone result through the existing descriptor-relative rename, and remove work source/sidecars before removing work. The original registration data is never moved. Verification already uses only local filenames.

Estimated total written work: 450–600 lines including plan, tests and evidence; zero exported types/interfaces, two internal launch callers, four acceptance criteria, fewer than ten reject outcomes. Recount before plan commit remains within all five limits.

## Concurrency model
The parent starts and joins one child under the existing 60-second context and WaitDelay. Re-exec becomes Node in the same PID, so cancellation kills/joins the same child. Bootstrap cwd changes occur only in the disposable child. No new parent goroutine or shared state.

## State transitions and identity reuse
| Event | Race-detector test |
| --- | --- |
| Selected directory renamed/replaced or substituted with a symlink at launch | `TestBackupNodeDirectoryIdentity` (snapshot and verify) |
| Cancellation after child start | `TestBackupSQLiteCancellation` |
| Repeated verification after source removal | `TestBackupSQLiteWAL` |
| Initialized maintained-template update | `TestUpdateTemplateSnapshot` |

## Error handling
Runtime probe/unsupported version, bootstrap cwd/exec failure, SQL failure and cancellation return errors. Failed snapshots publish no verification evidence. Existing storage failures retain explicit phase errors. Verification never repairs sealed contents. Interrupted copied-work moves leave an incomplete backup, requiring a new publish ID.

## Testing strategy
Write launch identity tests first and observe their failure against the old path-based launch. Cover renamed directories and symlink/replacement sentinels, unchanged parent cwd, SQL/nonexistent-executable failure, and reused existing cancellation/WAL/storage tests. Carry the retained standalone initialized assertion and add a healthy maintained-template update proving saved preferences and verified pre-migration backup. Run apps race tests, module vet and pyry build. Push the full candidate before requesting native macOS evidence; record exact revision, runtime, commands and executed/pass/fail/skip counts. Full-module race suite belongs to the verifier.

## Open questions
- Native task recipes must provide the required apps tests, rather than just platform availability; inspect actual returned artifacts before claiming proof.

## Revisions
- 2026-10-15: The native package run exposed `TestOwnerSupportedNode` assuming exactly eight runtime environment keys. A separate real Node process launched with an explicitly empty environment still reports macOS-generated `__CF_USER_TEXT_ENCODING`. Adjust this test only: seed a hostile parent value and permit a different synthesized value on Darwin before checking the exact remaining environment. The parent value must still be excluded, and production environment construction is unchanged.
- 2026-10-15: Native macOS execution is available in the resumed builder worktree. The initialized snapshot, WAL and maintained-template update passed on the first run; directory-identity arms rejected the fixture's symlinked `/var` temp prefix before reaching launch. Use the existing `testRoot` helper for no-follow-opened test directories, including launch-failure fixtures. This canonicalizes fixture setup only; production directory selection remains unchanged. Re-run the required native family and touched-package checks before handoff.
- 2026-10-14: The retained assertion is carried in `internal/apps/committed_recovery_template_test.go`, its original #3404 file, without importing recovery or the macOS skip. #3404 is an additive overlap for this assertion; its merge must keep one unskipped copy of `TestCommittedTemplateUpdateSnapshot`.
- 2026-10-14: Inspection of `native_recipe` in the dispatcher's `fleet-support.py` resolves the native recipe question: `platform-probe` explicitly executes no candidate tests, while the executed recipes cover memoryruntime or Android. The required native apps run cannot be supplied by these recipes. The managed route probe is tracked separately and must not be reported as test proof. Native macOS acceptance remains incomplete builder work until an apps-capable route executes the named tests and returns evidence.

## Documentation handoff
- Pending documentation stage: `docs/knowledge/features/apps-package.md`, “Persistent-data snapshots”: portable subprocess-directory mechanism and no-follow confinement/failure guarantees.
- Pending documentation stage: `docs/knowledge/features/apps-package-testing.md`, “Testing”: initialized macOS SQL/update proof; absent-data publications and skipped arms do not establish it.

## Security review
**Verdict:** PASS
- Trust boundaries: private bootstrap accepts only its exact argv shape, fixed mode and inherited directory; selected Node remains checked by `supportedNode`.
- Tokens/secrets: no credentials enter either subprocess; environment stays empty.
- File operations: MUST FIX addressed in design: `../data` could follow a substituted parent. Snapshot uses local work filenames after descriptor-relative moves. Directory replacement cannot redirect `fchdir`; directories/files retain 0700/0600 and existing no-follow opens. Arbitrary same-account mutation of files within an already selected private tree is outside the existing consistency threat model.
- Subprocesses: no shell or unchecked cwd path; executable is made absolute before cwd changes. Bootstrap exits on failed fchdir/exec; parent captures its error and joins.
- Cryptography: no new cryptography or secret comparisons.
- Network/I/O: no network boundary; subprocess timeout and joining remain bounded.
- Errors/logs: bootstrap diagnostics contain operation errors, no data/credentials; existing error contract remains explicit.
- Concurrency: no parent cwd mutation, added locks or goroutines; cancellation owns the single child PID.
- Threat model: preserve no-follow directory identity under launch substitution; no expansion to account-level attacker authentication of backup evidence.

**Reviewer:** builder (self-review)
**Date:** 2026-10-14

## Validation evidence

Linux amd64, Linux 7.0.0-38-generic, Go 1.26.9; real Node 24.15.0 and npm 11.12.1 from the official Node distribution. Counts include named subtests.

At code revision `9e88a3a743b8e7a224733cb0622ce3b493482c66`:
`HOSTED_APP_NODE=/tmp/builder-3415/node-v24.15.0-linux-x64/bin/node go test -race -count=1 -json -run '^(TestCommittedTemplateUpdateSnapshot|TestBackupSQLiteWAL|TestBackupNodeDirectoryIdentity)$' ./internal/apps`
executed 7, passed 7, failed 0, skipped 0. The retained assertion has no platform skip. The two bootstrap-rejection arms and retained assertion also executed 4/passed 4/failed 0/skipped 0 after the test-file move.

At implementation revision `60d55f6f3d4155ada166aa6b806144ec916f0ba7` (identical production code):
`HOSTED_APP_NODE=/tmp/builder-3415/node-v24.15.0-linux-x64/bin/node HOSTED_APP_NPM_CLI=/tmp/builder-3415/node-v24.15.0-linux-x64/lib/node_modules/npm/bin/npm-cli.js go test -race -run '^TestUpdateTemplateSnapshot$' -count=1 -v ./internal/apps`
executed 1, passed 1, failed 0, skipped 0, test elapsed 97.13 seconds. It proves initialized-data healthy candidate activation, saved preferences, candidate migration and a freshly verified pre-migration schema/preferences/ledger backup. Runtime output reports Node v24.15.0/npm 11.12.1.

`go vet ./...`, `go build -o /tmp/builder-3415/pyry ./cmd/pyry`, and `GOOS=darwin GOARCH=arm64 go test -c -o /tmp/builder-3415/apps-darwin.test ./internal/apps` passed. Cross-compilation supplies no native execution proof.

The complete touched-package run against the implementation source, `HOSTED_APP_NODE=/tmp/builder-3415/node-v24.15.0-linux-x64/bin/node HOSTED_APP_NPM_CLI=/tmp/builder-3415/node-v24.15.0-linux-x64/lib/node_modules/npm/bin/npm-cli.js go test -race -count=1 -json ./internal/apps/...`, finished in 487.415 seconds: executed 999, passed 999, failed 0, skipped 0. Required parent assertions all passed: directory identity 1.54 seconds, retained initialized snapshot 0.75 seconds, WAL 0.61 seconds, maintained-template update 70.06 seconds. Production code is unchanged between the implementation and assertion-relocation commits. The full-module race gate remains the verifier's responsibility.

Native route task `f4108002713f4bbdad100532cfb3908f8a231d1570b884e0dafb456d4c9821c1` was requested with `platform-probe`, `macos-arm64` and pushed revision `60d55f6f3d4155ada166aa6b806144ec916f0ba7`, using `fleet-task.py request /tmp/builder-3415/native-request.json`. Artifacts were retrieved with `fleet-task.py await <task-id> --out /tmp/builder-3415/native-probe --timeout 55`. The completed result says the route is available and is not candidate test evidence. Exact returned `platform.json` bytes are retained at `internal/apps/testdata/3415-native-platform.json`, SHA-256 `0c89293ba92560bb4eec2e1e159fa36b833f1262ed876df45c48062b2f61ad0c`: Darwin/arm64, `candidate_tests_executed: false`.

**Native acceptance remains blocked on an apps-capable execution recipe.** Required macOS command, with supported real tools selected through `HOSTED_APP_NODE`/`HOSTED_APP_NPM_CLI`:
`go test -race -count=1 -v -run '^(TestCommittedTemplateUpdateSnapshot|TestBackupSQLiteWAL|TestUpdateTemplateSnapshot|TestBackupNodeDirectoryIdentity)$' ./internal/apps`.
Native candidate tests executed 0; no runtime versions or passing SQL/update counts can be claimed from the route probe. This is incomplete builder proof, not a documentation or live-Claude handoff.

### Native macOS validation, 2026-10-15

The resumed builder executes directly on macOS 26.6.2 (25G83), Darwin 25.6.0/arm64, Go 1.26.2. Real supported tools are Node v24.15.0 and npm 11.12.1 from `/tmp/builder-3404/node-v24.15.0-darwin-arm64`, selected explicitly below. This native execution resolves the earlier missing apps-capable recipe; the old platform probe remains historical route evidence only. No live Claude is used.

At revision `8465a7e0cd81847e859d8dea04d86e47a6e443f2`:
`HOSTED_APP_NODE=/tmp/builder-3404/node-v24.15.0-darwin-arm64/bin/node HOSTED_APP_NPM_CLI=/tmp/builder-3404/node-v24.15.0-darwin-arm64/lib/node_modules/npm/bin/npm-cli.js go test -race -count=1 -json -run '^(TestCommittedTemplateUpdateSnapshot|TestBackupSQLiteWAL|TestUpdateTemplateSnapshot|TestBackupNodeDirectoryIdentity|TestBackupNodeFailures|TestBackupNodeBootstrapRejectsInvalidDirectory|TestBackupSQLiteCancellation|TestBackupSQLiteStorageErrors)$' ./internal/apps`
executed 23, passed 23, failed 0, skipped 0; package elapsed 29.301 seconds. Exact JSON events are committed at `internal/apps/testdata/3415-macos-focused.jsonl`, SHA-256 `35918d690179934d5987b987807d3a6f67639418e26bc7250af838ba9d81712e`.

| Required native arm | Executed / passed / failed / skipped, including subtests | Elapsed seconds |
| --- | --- | --- |
| `TestCommittedTemplateUpdateSnapshot` (no platform skip) | 1 / 1 / 0 / 0 | 0.19 |
| `TestBackupSQLiteWAL` | 1 / 1 / 0 / 0 | 0.27 |
| `TestUpdateTemplateSnapshot` | 1 / 1 / 0 / 0 | 25.67 |
| `TestBackupNodeDirectoryIdentity` | 5 / 5 / 0 / 0 | 0.33 |

The update arm proves healthy candidate activation, retained saved preferences, the new candidate migration and a freshly verified backup matching the pre-migration schema/preferences/ledger. The WAL arm verifies committed WAL-only rows and ledger contents, standalone DELETE mode without transient sidecars, and repeated immutable verification after source removal. Launch substitution arms preserve selected-directory identity and outside sentinels while the parent cwd remains unchanged. All required native arms ran without skips.

Earlier native attempts are retained here as failures, not passing evidence: at `ad61e40850c9fd28d838a64b4545d56c89414841`, the required four-family command executed 8/passed 3/failed 5/skipped 0 because the identity family and its four children rejected the fixture's symlinked temp prefix before launch. At `8465a7e0cd81847e859d8dea04d86e47a6e443f2`, the initial complete package run executed 999/passed 994/failed 1/skipped 4 in 330.363 seconds; only `TestOwnerSupportedNode` failed on the macOS-generated environment key. The two test-only corrections are recorded in Revisions. A direct real-Node probe with `subprocess.run([node,'-e','console.log(JSON.stringify(process.env))'],env={},capture_output=True,text=True,check=True)` returned only `__CF_USER_TEXT_ENCODING`, establishing synthesis even with an explicitly empty environment.

At revision `6eb80a038fe22fc831c2bad255be00a15114a634`, the same tool-selection variables with `go test -race -count=1 -json -run '^TestOwnerSupportedNode$' ./internal/apps` executed 1/passed 1/failed 0/skipped 0. This includes the hostile-parent-value assertion. No production code changed during this native continuation.

The final complete native run at `6eb80a038fe22fc831c2bad255be00a15114a634`:
`HOSTED_APP_NODE=/tmp/builder-3404/node-v24.15.0-darwin-arm64/bin/node HOSTED_APP_NPM_CLI=/tmp/builder-3404/node-v24.15.0-darwin-arm64/lib/node_modules/npm/bin/npm-cli.js go test -race -count=1 -json ./internal/apps/...`
passed in 328.754 seconds: executed 999, passed 995, failed 0, skipped 4. Required parent assertions all passed again: retained initialized snapshot 0.18 seconds, WAL 0.27 seconds, maintained-template update 25.87 seconds and directory identity 0.36 seconds. `TestOwnerSupportedNode` passed in 0.10 seconds. The four skipped arms are `TestBackupAliasedTempRoot/states/nested`, `TestBackupStates/nested`, `TestRestoreStates/nested` and `TestRetainedValidation/invalid_UTF8`; each reports `filesystem rejects invalid UTF-8 filename (EILSEQ)`. Those skips supply no filename proof and do not skip any required SQL/update arm. Structured runtime/revision/command/count evidence is committed at `internal/apps/testdata/3415-macos-results.json`.

`go vet ./...` passed at the final code/test revision. `go build -o /tmp/builder-3415-macos/pyry ./cmd/pyry` passed at `8465a7e0cd81847e859d8dea04d86e47a6e443f2`; subsequent changes affect tests/evidence only. `git diff --check` and formatting checks passed. Earlier Linux execution remains recorded above; production code is unchanged. Native builder acceptance is now complete. Documentation remains pending for its own stage, and the verifier owns the full-module race gate.
