# Checked release candidates (#3289)

## Files read
- `internal/apps/create.go` → `Inspect`, `sourceLocked`: live local admission and owned source paths.
- `internal/apps/registry.go` → `changeLocked`: clone/persist/commit under the registry mutex.
- `internal/apps/releases.go` → `RecordRelease`, `Release`: immutable history and metadata validation.
- `internal/apps/runtime.go` → `privateDir`, `regularAt`, `supportedNode`, `selectBuild`: pinned access, runtime range and startup selection.
- `internal/apps/artifacts.go`, `artifact_install.go` → `ValidateArtifacts`, `InstallBuild`, `VerifyBuild`: canonical retained byte identity and sealing.
- `internal/apps/owner.go` → `launch`, `cleanup`: owned process groups and committed service isolation.
- `internal/apps/artifacts_test.go`, `owner_process_test.go`: retained fixtures and controlled-child patterns.
- `docs/knowledge/features/apps-package-validation-and-stored-identity.md`: candidate manifests never imply activation.
- `docs/knowledge/features/apps-package-concurrency-and-persistence.md`, `apps-package-testing.md`: synchronous callbacks, no-follow fixture roots and descendant proof.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Manifest and validation, Runtime, roots and service conventions; ADR 043: fixed commands, trusted app code.
- `templates/hosted-app/source/package.json`: maintained scripts; npm tests own their compilation.

## Context
Expose package-level candidate preparation without activation, migration or persistent-data preparation. Build validation is distinct from publication readiness. No new decision record is needed. Forecast approximately 950 written lines, two exported types, no existing consumer migration, four acceptance criteria and nine orchestration failure phases. The line ceiling is exceeded; native grandchild lineage #3125 → #3260 → #3289 and the one-consumer split floor require continuing, with `needs-human:sizing` recorded. No overlapping feature branches touch the planned files.

## Design
`Registry.PrepareCandidate(ctx, serverID, appID, Toolchain) (Release, error)` admits via `Inspect`, copies owned source into a fresh 0700 staging directory using descriptor-relative no-follow traversal, excluding top-level dist/node_modules, and never writes source. `Toolchain` names absolute selected Node executable and npm CLI JavaScript paths. Invoke npm with that Node directly; prepend a private node alias to PATH for npm scripts. Use only staging HOME/cache/temp and fixed tool PATH, never inherited daemon environment or app data.
Check actual Node/npm versions and staged manifest/package/lock consistency before npm ci. Run ci, typecheck, test, build in that exact order, joining each process group before proceeding. Validate completed artifacts through existing APIs.
The final registry mutation owns numeric progression, current live registration, retry byte comparison, existing-build verification before any installation, installation/reverification and history persistence under one mutex. Only history and its revisions change. Identical retries return original metadata, regardless of current tool versions or newer history. Compare three numeric version components against all history and legacy active release. Fresh retained lock bytes determine SHA-256. A failed write leaves history unchanged; an unrecorded sealed build may remain for retry.
`selectBuild` ignores only private `.install-*` directories, never selects them or accepts partial digest builds. Other malformed entries retain rejection.

## Concurrency model
Build commands use their own process groups and one joined Wait goroutine. Cancellation kills the whole group and joins the leader. After leader exit terminate remaining descendants and confirm no live group work before artifact validation. Linux orphan zombies are inert OS bookkeeping; process-group inspection distinguishes these from live work. Cleanup failure stops the pipeline. Build phases do not hold registry locks; only final validation/install/record does. No Owner lock is acquired and callbacks keep existing non-reentry rules.

## State transitions and identity reuse
| Event | Race-enabled proof |
|---|---|
| Same version retry, newer history then older retry, changed tool versions | `TestCandidateHistory` |
| Concurrent same and differing candidate bytes | `TestCandidateConcurrent` |
| Numeric progression and legacy active version | `TestCandidateHistory`, `TestCandidateLegacy` |
| Recorded build missing, partial or changed; retry cannot repair | `TestCandidateDamagedRetry` |
| Removal/registration change during build | `TestCandidateCurrentRegistration` |
| Cancellation, phase failure, leader exit with descendants | `TestCandidatePhases`, `TestCandidateDescendants` |
| Failure while previous release serves; temporary installation scan | `TestCandidateExistingService`, `TestCandidateInstallSelection` |

## Error handling
`CandidateFailure` exposes only Phase and static actionable Reason. No raw subprocess diagnostics, underlying host-path errors or credentials cross this boundary. Cancellation stops later phases and reports its current phase. Descriptor, validation, cleanup, version and persistence errors are mapped to static reasons. No success before durable commit.

## Testing strategy
Write controlled-node helper tests first and observe missing API failures, then implement. Reuse retained fixtures and maintained template package/lock bytes; helpers compile no app dependency and use no network. Verify ordered argv, private staging, exact metadata, environment/pinning, containment, package/lock maps, current registration, history/revision neutrality on retries/failures, process cleanup and surviving service/data. Run `go test -race ./internal/apps/...`, `go vet ./...`, `go build ./cmd/pyry`; full-module verification belongs to the verifier.

## Open questions
None. npm selection is its CLI script passed to selected Node; trusted scripts are not sandboxed. Local control and daemon selection are owned by #3301/#3266; publication/readiness by #3262.

## Documentation handoff
Pending documentation stage: `docs/hosted-apps.md` § Runtime and layout and Create, publish and update: checked private staging and ordered commands; numeric versions and byte-identical retries; original recorded toolchain metadata and safe phase feedback. Under Retained candidate artifacts link retained contents and canonical inventory. State candidate success proves build validation; publication owns health-confirmed activation/readiness.

## Security review
**Verdict:** PASS
- Trust boundaries: `Inspect` and final `changeLocked` admit local live identities; manifest identity and package/lock checks precede install; canonical artifact APIs prove retained bytes.
- Tokens: no token creation; commands receive an explicit environment and staging-only HOME/temp/cache. Child output is discarded except bounded version responses.
- Files: no-follow pinned components and regular-file checks during copying; source remains untouched, staging 0700/files 0600; retained sealing and atomic durable registry writes reuse existing APIs.
- Subprocesses: fixed argv, no shell construction, checked Node executes npm CLI and private PATH alias pins script Node; process-group cleanup and joined Wait precede validation. Trusted same-account code can escape a group deliberately; ADR 043 excludes OS sandboxing.
- Cryptography: existing canonical SHA-256 helpers identify exact artifacts/lock bytes; random staging names use crypto/rand.
- Network/I/O: no new server or network boundary; cancellation-aware copying and capped version output prevent unbounded diagnostic retention.
- Errors/logs: `CandidateFailure` contains static feedback only; no raw diagnostics, secrets, source contents or host paths returned.
- Concurrency: final version decision and disk commit share registry mutex; subprocess Wait is joined on every path and no Owner locks are acquired.
- Threat model: trusted app install/build code per ADR 043; activation/data rollback remains #3262 and daemon/tool boundary #3266/#3301.
**Reviewer:** builder self-review; **Date:** 2026-10-13.
