# Owned committed-release execution (#3205)

## Files read

- `internal/apps/registry.go` → `Registry.change`, `Record`: durable locked commit and per-record revisions.
- `internal/apps/lifecycle.go` → `UpdateLifecycle`, `applyLifecycle`, `notify`: compatible unconditional updates and persistence-before-notification.
- `internal/apps/manifest.go` → `ValidateManifest`: exact validated local identity and fixed runtime/entry contract.
- `internal/apps/storage.go` → `Open`, `validateSnapshot`: private roots, safe errors and restart normalization.
- `internal/apps/registry_test.go`, `lifecycle_test.go` → existing storage, lifecycle and injected persistence patterns.
- `docs/knowledge/features/apps-package.md` § Manifest updates and retention, Lifecycle and restart state, Concurrency and persistence: candidate manifests never activate releases; callbacks cannot re-enter the registry.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Runtime, roots and service conventions, Registration, observed state and safe publication: normative runtime, data, environment and cleanup.
- `docs/knowledge/decisions/043-hosted-app-runtime-and-isolation.md`: immutable trusted services, not an OS sandbox.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: scoped race evidence and contract comments.

## Context

The registry owns persistence but no processes. Add one locally callable owner for explicit start/stop/shutdown, without daemon wiring, publication, readiness or reconciliation. Native ancestry #3122 → #3203 → #3205 and the existing needs-human:sizing label permit the estimated 1,050–1,250 written lines over the 800-line ceiling. Otherwise the split would separate reusable conditional observations from process ownership. Five acceptance criteria, two new exported types, no existing consumer migrations, and ten failure families (identity/currentness, persistence, closed/cancelled owner, build resolution, runtime, data, reservation, spawn, child exit, group cleanup). No fetched feature branch overlaps internal/apps. No new decision record is needed.

## Design

`Registry.CommitObservation(serverID, appID, expectedRevision, Observation)` returns the committed record, change flag and error. The existing locked change path compares the app revision before applying state/error and optional explicit intent; title, manifests and release fields stay intact. Stale commits return ErrStale with no writes or events. The unconditional UpdateLifecycle API stays unchanged. Internal explicit intent writes preserve current fields under the same lock.

`NewOwner(registry, nodeExecutable)` selects one absolute executable without fallback. `Owner.Start(ctx, appID) error` coalesces concurrent explicit starts and waits until spawn or failure, not readiness. Each generation has cancellation, a start-result barrier, a retirement barrier, revision, process and port. Start commits available/starting first (unpublished records stay stopped), resolves exactly one matching validated build manifest, checks the actual executable version, prepares data and reserves a loopback ephemeral port. A final revision/cancellation check under the registry lock gates spawn. Spawn executes only dist/service/main.js with build cwd. No migrations/install/source/staging commands run.

Filesystem helpers reject symlinks in every directory/file component and nonregular files, bound manifest reads, use no-follow directory/file descriptors for sensitive access, and preserve database bytes while correcting modes to data 0700/database 0600. Build IDs are lowercase SHA-256 names; inventories and immutable publication remain #3125. Published build immutability is a caller contract throughout execution.

Environment is exactly PYRY_APP_ID, PYRY_SERVER_ID, PYRY_APP_RELEASE, PYRY_APP_DATA_DIR, PYRY_APP_DB_PATH, PYRY_APP_BIND_HOST=127.0.0.1, PYRY_APP_PORT and NODE_ENV=production. There are no additional allowed variables. Ports are tracked privately across listener handoff and until retirement; the previous retired port for each app is excluded on its next attempt.

`Owner.Stop(appID) error` cancels first, commits stopped intent against the current record, waits for cleanup, then conditionally observes stopped. Failed intent persistence still retires ownership and returns failure. `Owner.Shutdown() error` closes admission, cancels all generations, joins workers and observes stopped without changing intent. Owner errors retain asynchronous persistence/cleanup failures for shutdown to surface. No endpoint enters registry/discovery.

## Concurrency model

Owner mutex protects admission, generation map, reservations and accumulated errors; it is never held while waiting for worker retirement. Registry calls serialize on the registry mutex; callbacks must not re-enter either owner or registry. A worker owns each generation and child wait. Cancellation interrupts preparation and triggers process-group cleanup. Cleanup sends SIGTERM, polls group survival for up to the production five seconds, sends SIGKILL to remaining members and joins the leader even if it exited first. Worker retirement releases its port. Shutdown joins all workers; separate apps never share a process group.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Duplicate/concurrent starts | TestOwnerDuplicateAndIsolation |
| Stop during start, repeated stop, restart with fresh generation/port | TestOwnerStartStopRaces |
| Newer title/intent/release commit and stale spawn/exit | TestOwnerStaleAndPersistence |
| Validation or spawn failure followed by explicit retry | TestOwnerReservations |
| Leader exits with descendants, graceful/forced stop, sibling survives | TestOwnerGroupCleanup |
| Shutdown during start, repeated shutdown, post-shutdown start | TestOwnerStartStopRaces |
| Stale observation and unrelated app mutation | TestConditionalObservation |

## Error handling

Safe static app.unavailable metadata describes validation/spawn failure or unexpected exit. No child output, paths or endpoints are persisted. Local returned errors may wrap OS errors; child output is discarded. Failed starting/intent writes launch nothing; stale starting observations cannot spawn. Every error after spawn retires its owned group. Persistence and signal/wait errors are surfaced rather than claiming successful state writes.

## Testing strategy

Write focused tests first and observe missing API failures. Use controlled real child processes and child-written readiness files for timing, with a shortened private grace seam while asserting the default is five seconds. Test committed/candidate selection, filesystem/runtime rejection, data/modes, exact environment, unique/fresh reservations, lifecycle failures, no lost updates and descendant/sibling isolation. Execute TestOwnerSupportedNode using Node 24.15.0 downloaded outside the worktree; assert real cwd/argv/environment/database bytes. Run go test -race ./internal/apps, go vet ./... and go build -o /tmp/builder-3205/pyry ./cmd/pyry. The verifier owns full-module tests; no live Claude check is required.

## Open questions

None. Health/readiness #3206 and automatic reconciliation #3207 consume private generation ownership in this package later.

## Documentation handoff

Pending documentation stage: update `docs/hosted-apps.md` § Runtime and layout, Startup and health, Registration and observed state, and `docs/knowledge/features/apps-package.md` § Lifecycle and restart state. State implemented runtime and exact eight-variable environment allowlist, persistent data modes, private reservation/process-generation ownership, five-second stop/cleanup behavior, conditional lifecycle commits and safe state/error guarantees. Callable ownership proves only starting; health/readiness (#3206) and automatic recovery (#3207) remain pending. Distinguish daemon wiring, publishing/rollback and resource capability activation.

## Security review

**Verdict:** PASS

- Trust boundaries: validated build manifests select only committed local identity/release; final revision guard prevents stale spawn.
- Tokens/secrets: explicit eight-variable environment inherits nothing; no keys/tokens are generated or exposed.
- File operations: no-follow descriptors and regular-file checks protect manifest/database access; retained bytes and restrictive modes are required. Trusted immutable build publication and inventory verification are OUT OF SCOPE (#3125); services run as trusted same-account code.
- Subprocesses: fixed argv, absolute selected Node, private process groups, discarded output, cancellation and descendant cleanup; no shell.
- Cryptography: no cryptographic operations or secret comparisons.
- Network/I/O: only ephemeral loopback reservation; no service HTTP/client reads or probes (#3206).
- Errors/logs: static safe persisted metadata; local diagnostics do not enter discovery.
- Concurrency: owner locks never span joins; registry revision checks and writes share its mutex; each worker has cancellation and retirement barriers.
- Threat model: no OS sandbox claim; native resource authorization belongs to #3123 and publication to #3125.

**Reviewer:** builder self-review; **Date:** 2026-10-11.

## Revisions

2026-10-11: Concurrent-stop evidence showed that two successful intent writes can share a revision while only the first final observation changes it. Stop now treats a stale final observation as success only when the latest committed record already has stopped intent/state; newer available intent still returns ErrStale. Start admission and final spawn use registry-then-owner lock order so a detached older intent cannot retire a just-created generation. Cancelled start intent is checked inside the registry lock before mutation. Conditional observations clone caller error values.

The final lifecycle matrix also covers TestOwnerInflightCurrentness (cancellation and newer lifecycle during Node validation), TestOwnerReplacement (explicit supersession), TestOwnerConcurrentStop, and TestConditionalObservationIntegrity (preservation, detached values, no-op/reject/persistence integrity). Start performs explicit replacement when its current committed release differs from the owned one; automatic registry reactions remain #3207. Written work is approximately 1,450 lines after expanded controlled-process evidence, retained under the existing split-depth exception, with two new exported types and no consumer migrations.

The supported-Node test is opt-in through HOSTED_APP_NODE when the default runtime is unsupported; its default skip is not acceptance evidence. This build executes the supported arm using actual Node 24.15.0 and records the result. Other tests default to local controlled executable doubles so the verifier's hermetic gate is independent of installed Node versions. On Linux, killed orphan descendants can remain zombies under container init; tests distinguish zombies from live processes while the owner always joins its direct child.

Final recount: approximately 1,580 written lines including plan and test helpers (about 550 production and 940 tests/helpers). The existing grandchild sizing exception applies. TestOwnerPostSpawnPersistenceFailure proves that final stopped-observation failure and asynchronous failed-observation failure surface without leaving a child alive; asynchronous persistence errors are returned by Shutdown. Supported-Node evidence: TestOwnerSupportedNode executed 1, passed 1 with Node 24.15.0, without a skip.

2026-10-11 rework, verifier finding 1: `Owner.Stop` now holds registry-then-owner locks across ownership capture/cancellation and the durable stopped-intent commit. `Registry.changeLocked` and `intentLocked` reuse the existing validated persistence-before-notification path without reacquiring the registry mutex. Both locks are released before joining a worker. This closes the admission gap for absent, retired and active generations; starts admitted after the stopped commit are explicit newer intent and still invalidate the final observation. `TestOwnerStopAdmission` checks cancellation serialization and that a real child is gone before the first stopped observation. `TestOwnerStopCommitAdmission` covers absent and retired ownership during the intent commit. `TestOwnerStartStopRaces` checks retirement immediately after the concurrent operations, without a second stop concealing a surviving generation. Security review: PASS after closing this ownership/intent race; callback restrictions and lock order remain unchanged.

2026-10-11 rework, verifier finding 2: runtime filesystem preparation, executable selection/spawn, listener reservation/handoff and owned-group operations now wrap failures with operation context and preserve `errors.Is`. `cleanup` also retains group-status errors without shortening the grace period, using a private syscall seam initialized to `syscall.Kill`. `TestOwnerPreparationErrorContext` and `TestRuntimeFileErrorContext` check missing build/manifest/entry and descriptor failures, including unchanged safe persisted metadata. `TestOwnerCleanupErrorContext` injects termination/status/kill failures while actually retiring real leaders and forced descendants, proving context and errno propagation through Stop and Shutdown. Security review: PASS; detailed diagnostics stay local and no child output, paths or endpoints enter failure metadata. Final rework checks: supported-Node apps race suite executed 296 tests/subtests, passed 296, skipped 0; the stop/start regression family executed 30, passed 30 across three repetitions; `go vet ./...`, `go build ./cmd/pyry` and diff whitespace checks passed. The verifier owns the full-module gate and documentation remains pending as specified above.
