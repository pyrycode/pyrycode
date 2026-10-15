# Publication retirement inspection

## Files read
- `internal/apps/publication_test.go` → `publishFixture`, `TestPublishRetirement`: 30 ms grace and all 18 retirement arms.
- `internal/apps/owner.go` → `cleanup`, `launch`: graceful termination, asynchronous leader join and error retention.
- `internal/apps/candidate_process.go` → `candidateCleanupWithKill`, `candidateGroupLive`: positive group inspection and Linux zombie exclusion.
- `internal/apps/publication_process.go` → `supervisePublication`: two cleanup helpers currently run before leader join.
- `internal/apps/publication_update.go` → `retirePrevious`, `finishUpdateFailure`: errors bar snapshot/restoration.
- `internal/apps/publication.go` → `finishPublicationFailure`: safe metadata and recovery evidence.
- `internal/apps/cleanup_probe_test.go`, `internal/apps/retirement_errors_test.go`, predecessor/migration uncertainty tests: retain denied-signal and persistent-uncertainty assertions.
- `internal/apps/health_test.go`, `internal/apps/owner_process_test.go`: real forced descendants and atomic PID markers.
- `docs/knowledge/features/apps-package-testing.md` § Publication fixtures and `apps-package-lifecycle-and-restart-state.md` § Publication writers and completion: leader exit alone never proves retirement.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: bounded tests, native evidence and attribution limits.

## Context
The unchanged native baseline reproduced health/cancel failure. Two independent cleanup passes can signal a group immediately after SIGKILL, while macOS still reports its exited members. Inspection errors must be allowed to resolve through positive absence without erasing actual signal failures. Establish the exact sequence through a controlled real-fixture probe experiment before claiming attribution. No new public contract or decision record is required. #3415 is merged. #3404 overlaps only owner startup admission; keep cleanup edits local and additive.

## Design
Keep `cleanup(pid) error` and `candidateCleanupWithKill(pid, kill) error` internal contracts. Bound post-kill inspection; only positively absent groups permit success, and preserve every denied termination. Inspect candidate groups before signalling so already-retired groups need no redundant signal. Retry transient inspection uncertainty within the existing bounded retirement window, returning persistent uncertainty. Keep leader join before generation completion. Use the existing publication/health/forced-descendant fixtures for controlled transient and persistent probes and record native group versus individual-member observations. If observation contradicts this sketch, append a revision before changing the design.

Sizing: one deliverable; approximately 400 written lines including evidence summaries, at most three production files, zero exported types, zero consumer signature changes, three acceptance criteria and fewer than ten error outcomes. Rechecked before plan commit: within all five limits.

## Concurrency model
Existing supervisors retain ownership and asynchronous `Cmd.Wait`; no new production goroutines. Cleanup runs without registry/owner locks. `g.done` remains after group retirement and leader join. Test hooks are installed before launch and coordinate through channels/atomics; all test publishers and fixture children are joined.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Build/migration/health canceled by six retirement events | `TestPublishRetirement` |
| Leader exits while forced descendant remains live, then group exits | new controlled publication probe regression |
| Inspection uncertainty resolves after forced kill / remains persistent | `TestOwnerCleanupProbeRecovery` and new controlled regression |
| Denied termination, repeated shutdown, generation replacement/removal | `TestGenerationRetirementErrors`, `TestPublishServiceRetirementUncertain` |
| Previous publication retired before snapshot / migration descendant survives | existing predecessor/migration retirement uncertainty tests |

## Error handling
Persistent inspection errors and denied signals remain errors. Never infer absence from leader exit or EPERM. Existing publication failure handling retains journals/RetirementPending, unavailable targets and barred unsafe restoration/removal/retry. Successful cleanup needs positive absence and generation completion also joins the leader.

## Testing strategy
Commit this plan first, then write discriminating tests and record their failure before implementation. Record controlled native probe observations separately from injected sequencing. Run two consecutive complete `go test -race -json ./internal/apps -run '^TestPublishRetirement$' -count=5` batches with normal concurrency, then `go test -race -json ./internal/apps -count=1` with real supported Node/npm. Keep every attempted batch, exact revision/environment, parent/leaf counts and skip reasons in committed testdata evidence. Run `go vet ./...` and build `./cmd/pyry`; full-module race gate belongs to verifier.

## Open questions
- Which helper produces native EPERM, and does it resolve to ESRCH after actual individual descendants disappear? Resolve with recorded probes; distinguish controlled attribution from historical inference.

## Security review
**Verdict:** PASS
- Trust boundaries: `candidateGroupLive` must positively establish absence; EPERM is uncertainty, never proof.
- Tokens/cryptography: no credentials or cryptographic operations enter cleanup or evidence.
- File operations: existing publication failure paths retain recovery evidence; no confinement changes.
- Subprocesses: SHOULD FIX: discriminate a live forced descendant from exited group bookkeeping before suppressing any inspection error; retain denied signal errors even after absence.
- Network/I/O: no new network operations; existing health fixtures retain HTTP bounds.
- Errors/logs: preserve safe external publication feedback; record only test fixture process identifiers and errors.
- Concurrency: no new production goroutines or lock orders; supervisor completion still joins leader and group cleanup.
- Threat model: trusted same-account services remain confined to existing process groups; escaping process groups is outside the hosted-app contract, unchanged here.
**Reviewer:** builder (self-review)
**Date:** 2026-10-15
