# Managed OpenAI memory runtime on macOS

## Files read

- `internal/memoryruntime/install.go` → `Install`, `engine.install`, `lock`: target selection and verified permanent-generation lifecycle.
- `internal/memoryruntime/process.go` → `supportedHost`, `provision`, `probe`, `run`: host gate, offline wheel installation, exact compatibility checks and cancellable sanitized children.
- `internal/memoryruntime/process_other.go` → `configureProcess`: macOS process-group cancellation.
- `internal/memoryruntime/storage.go` → `openPrivateHome`, `acquire`, `publish`, `treeSeal`: ownership/confinement, process exclusion, metadata publication and preservation.
- `internal/memoryruntime/artifacts.go` → `download`, `extract`, `validateWheel`: integrity before execution and archive confinement.
- `internal/memoryruntime/install_test.go`, `process_test.go` → reusable lifecycle/security tests and production compatibility cancellation proof.
- `internal/memoryruntime/linux-amd64.lock.json`, `smoke_test.go`, `smoke-linux-amd64.json`: existing pins and executed Linux smoke contract.
- `docs/knowledge/features/memoryruntime-package.md`: preserve wheel basenames, permanent shebang paths and full probes on reuse; canonicalize the macOS temporary home before calling the strict destination boundary.
- `docs/knowledge/decisions/044-memory-runtime-lock-and-generations.md`: reviewed shipped artifact hashes establish integrity; no new decision record needed.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: behavioral tests, artifact survival and executed counts.

## Context

Add one proved macOS OpenAI target to the existing isolated installer without changing daemon wiring or readiness semantics. Advertise Apple Silicon arm64 on macOS >=26.6.2, conservatively using the oldest version executed on this run's host (26.6.2, build 25G83). Older macOS and Intel remain unsupported until separately proved. Linux Ubuntu 24.04 amd64/glibc >=2.39 and its artifact lock/evidence remain unchanged.

Sizing: one deliverable, approximately 450 written lines including plan, lock, tests and evidence; zero new exported types/interfaces or consumer migrations; four acceptance criteria; no added state-machine reject branches. This reuses #3152 rather than repeating its 2215-line lifecycle. No other fetched feature branch currently touches `internal/memoryruntime`.

## Design

Select the embedded exact lock through private `selectPins(goos, arch, mode) (lock, error)`; only Linux/amd64 and Darwin/arm64 OpenAI combinations are accepted. Add an optional minimum-macOS lock field while retaining Linux's canonical lock encoding and pins. Ship `macos-arm64.lock.json` with CPython 3.12.11 from standalone release 20250918, bundled pip 24.3.1 and all 32 existing exact distribution versions, selecting macOS wheels. No index, resolution, source builds or local extra.

`supportedHost` uses the lock's macOS requirement to run `/usr/bin/sw_vers -productVersion` with bounded output and a credential-free environment. A pure numeric version gate rejects malformed/older versions; cancellation/deadline causes propagate. Keep the existing Linux host gate.

The shared engine retains account-scoped flock, hash-verified cache, unique permanent generations, complete probes, tree seal and atomic metadata publication. No migration, pruning or system/user install. Extend `TestSmoke` to canonicalize its temporary home, record macOS version/build and selected target, and verify repeated runtime paths/versions before temporary Milvus Lite open/close. Commit macOS evidence beside the smoke test; preserve Linux evidence and invocation.

## Concurrency model

No new production goroutines. Existing independently cancellable flock waits serialize verification/provision/publication across threads and processes. Managed macOS subprocess groups are killed on context cancellation; completion and bounded output collection join before returning. Unlike Linux, macOS has no parent-death signal contract; a killed installer releases its lock and its unpublished generation is never reused or overwritten.

## State transitions and identity reuse

| Event | Existing race-enabled coverage run on macOS |
|---|---|
| Repeated verified install / replacement / corrupt tree | `TestRepeatAndReplacement` |
| Multiple callers / independently cancelled waiter | `TestConcurrentAndCancelledWaiter` |
| Separate process exclusion / deadline | `TestCrossProcessExclusion` |
| Interrupted holder / cached-download retry | `TestInterruptedRetry`, `TestIncompleteDownloadRetry` |
| Install/probe/cancellation failure and old-generation preservation | `TestFailuresRetry` |
| Production compatibility cancellation/deadline / retry | `TestCompatibilityProbeCancellation` |
| Publication alias / unsafe destination | `TestGenerationSymlinkRejected`, `TestUnsafeDestinations` |

## Error handling

Unsupported mode, architecture or older/malformed host returns recoverable `Failure` wrapping `ErrUnsupported`; host command cancellation preserves context causes. All existing integrity, confinement, bounded error/progress and no-publication-on-failure behavior remains shared. No child output or credentials enter failure text.

## Testing strategy

Write and observe failing target/pin and minimum-version tests before implementation. Add hermetic assertions that both locks retain the full identical component-version closure, macOS artifacts carry shipped SHA-256 identities, and unsupported combinations are rejected. Run all reusable package tests with `go test -race ./internal/memoryruntime/...` on macOS; Linux's parent-death-specific test remains Linux-only. Run tagged `TestSmoke` with no credentials/model downloads, record all named results and counts, and commit the generated record. Run `go vet ./...` and `go build ./cmd/pyry`; the verifier owns the full-module gate.

## Open questions

None. macOS support is deliberately limited to the executed host version and architecture. The actual smoke must prove that the offline closure passes imports, exact versions, CLI and compatibility plus temporary database open/close before support is shipped.

## Documentation handoff

Pending documentation stage: `docs/deployment.md`, “Managed memory runtime”: replace the macOS-pending wording with exact macOS pins/dependency lock, supported architectures/minimum macOS requirements and the executed-platform smoke invocation. Retain Linux support, the private runtime/download caches and resumable setup, and the installation-versus-index-readiness distinction. State that this slice alone neither starts managed memory nor migrates the existing pyrybox watcher; local mode awaits #3154.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `selectPins` allows only shipped target/mode locks; `download` rehashes bytes against shipped reviewed SHA-256 metadata before `provision` executes. Upstream metadata is used to prepare the lock, never fetched as installation-time authority.
- [Tokens] `run` supplies a minimal environment and private home, excluding inherited provider credentials. No credential storage, provider initialization or token creation is introduced.
- [File operations] `openPrivateHome`, `privateDir`, `regularFile`, `extract` and `validateWheel` remain the boundaries for canonical owned destinations, 0700 directories/0600 metadata, no-follow mutable files and rooted archive/link confinement; `publish` only swaps metadata after probes/sealing.
- [Subprocesses] `sw_vers` is an absolute fixed command with bounded output and context cancellation. Runtime commands use explicit arguments, isolated Python and macOS process groups; no shell interpolation or inherited credentials.
- [Cryptography] Artifact/tree SHA-256 and `randomName`'s cryptographic generation remain shared; no keys or nonces are introduced.
- [Network and I/O] Downloads remain capped at 512 MiB with a five-minute HTTP timeout and caller context; wheel/archive contents and command output retain existing caps. No server or listener is added.
- [Errors/logs] `Failure` retains cause classification but exposes only finite stage names; smoke records host/version/check data, never subprocess environment or credentials.
- [Concurrency] One kernel lock covers all mutable installer state; permanent unique generations and atomic publication preserve previous returned locations after interruption/failure. No added goroutine requires shutdown.
- [Threat model] This installer does not expose mobile/relay interfaces or initialize production memory. Existing account-local filesystem trust and reviewed upstream artifacts are the applicable boundary; consumer/provider behavior stays with #3148, local dependencies with #3154.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-11
