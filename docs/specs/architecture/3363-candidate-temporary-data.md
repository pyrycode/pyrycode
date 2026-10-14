# Checked template temporary data (#3363)

## Files read
- `internal/apps/candidate.go` → `PrepareCandidate`, `candidateEnvironment`: command sequence, minimal environment and Node pinning.
- `internal/apps/candidate_process.go` → `candidateCommand`: children and process groups are joined before return.
- `internal/apps/candidate_source.go` → `candidateStaging`: source copy and independent staging cleanup.
- `internal/apps/candidate_npm_test.go` → `TestCandidateNPMEnvironment`, `TestCandidateNPMPinning`, `TestCandidateRealNPM`: preserve private npm configuration and lifecycle/shebang precedence.
- `internal/apps/candidate_test.go` → `testCandidate`, `TestCandidatePhases`: controlled preparation and cancellation fixtures.
- `internal/apps/create.go` → `Registry.Create`: fresh maintained-template generation.
- `internal/apps/owner_test.go` → `TestOwnerSupportedNode`: explicit supported executable selection.
- `templates/hosted-app/source/src/service/environment.ts` → `environment`: lexical and canonical unsafe-data rejection stays intact.
- `templates/hosted-app/source/test/service.test.mjs` → `fixture`: test data follows Node's temporary directory.
- `docs/knowledge/features/apps-package.md` § Template toolchain: generated inventory, rather than maintained source alone, must pass.
- `docs/knowledge/features/apps-package-validation-and-stored-identity.md` § Checked preparation inputs: retain Node precedence, separate npm config paths and explicit environment.
- `docs/knowledge/features/apps-package-testing.md` § Testing: skipped supported-Node arms prove nothing.
- `CODING-STYLE.md`: process tests, error values and cleanup conventions.

## Context
Checked preparation places TMPDIR in staging, which makes independent template service data invalid. One deliverable repairs that environment and proves a generated app can become a verified recorded build. Publication remains owned by #3359. No decision record is needed.

## Design
`candidateEnvironment(stage, node)` will return environment, an error-returning cleanup function and error. Allocate a fresh 0700 directory under `/tmp` (supported Linux/macOS), independent of inherited daemon TMPDIR. Resolve its path and reject source/staging/builds/dist components before exposing it as TMPDIR. HOME, cache, config files, Node alias and lifecycle shell remain private to staging. On allocation/resolution failure remove owned temporary storage; on every preparation return join cleanup errors with the outcome. No app persistent-data paths enter the environment.

No feature branch currently changes the planned production/test files. Estimated total written work: approximately 400 lines including tests and this plan; zero exported types, four simultaneous consumer updates, three acceptance criteria, and fewer than ten added reject branches. All sizing limits hold.

## Concurrency model
No new production goroutines. Preparation owns its temporary directory; existing command execution joins child/process-group cleanup before temporary-data removal. Concurrent attempts use independently minted directories.

## State transitions and identity reuse
| Event | Race test |
|---|---|
| Successful preparation removes temporary data | `TestCandidateTemporaryCleanup` success |
| Failed command removes temporary data | `TestCandidateTemporaryCleanup` failure |
| Cancellation joins child then removes temporary data | `TestCandidateTemporaryCleanup` cancellation |
| Repeated environments receive distinct private directories | `TestCandidateNPMEnvironment` |
| Fresh generated identity becomes verified release and survives reopen | `TestCandidateSupportedTemplate` |

## Error handling
Temporary creation, resolution, unsafe placement and cleanup failures reject preparation through existing sanitized `CandidateFailure` feedback. Production path rejection is unchanged. Existing version/command/record failures retain their phases.

## Testing strategy
Write placement and generated-template regressions first and observe failure under the old implementation. Verify TMPDIR privacy/canonical placement, daemon-environment exclusion and repeated allocation; controlled preparation tests observe actual child TMPDIR then prove removal on success/failure/cancellation. Preserve existing real-npm pinning tests. The supported template arm uses HOSTED_APP_NODE and HOSTED_APP_NPM_CLI, wraps the actual npm CLI only to record step results/TAP counts, invokes Registry.Create and Registry.PrepareCandidate, requires nonzero tests with no failures/skips before build, independently VerifyBuild checks the result and reopen checks matching release history. Run it with real Node 24.15.0 and its npm; record counts in the PR. Run scoped apps race tests, go vet and cmd/pyry build. Full-module verifier gate is later-stage work.

## Open questions
None. Supported Node 24.15.0 and bundled npm 11.12.1 are available in scratch storage for required evidence.

## Documentation handoff
Pending documentation stage: update `docs/knowledge/features/apps-package-validation-and-stored-identity.md`, “Checked preparation inputs”, replacing “Staging-only HOME/cache/temp” with private temporary-data placement and cleanup lifetime. State that template test data stays outside source/staging/build trees, and minimal environment, selected Node and production path rejection remain enforced.

## Security review
**Verdict:** PASS
- [Trust boundaries] Trusted app commands retain the existing explicit environment and checked artifact boundary; this is not an OS sandbox.
- [Tokens] No inherited credentials/configuration enter children; no new secret storage or logging.
- [File operations] Fresh 0700 allocation in fixed system temporary storage avoids daemon-controlled TMPDIR and app persistent data. Canonical paths are checked before use; owned storage is removed even on rejected placement. Existing service lexical/resolved checks remain enforced.
- [Subprocesses] Existing selected Node alias/lifecycle shell and private npm config are preserved. Process-group cleanup precedes data removal.
- [Cryptography] No cryptographic changes or new keys.
- [Network and I/O] No production network changes; real npm installation in acceptance uses the maintained lockfile.
- [Errors/logs] Cleanup errors reject through sanitized phase feedback; no child diagnostics are exposed by production.
- [Concurrency] Per-attempt directories share no state; cancellation retains existing child joins.
**Reviewer:** builder self-review; **Date:** 2026-10-14.
