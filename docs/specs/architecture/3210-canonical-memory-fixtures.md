# Canonical temporary paths in memory CLI fixtures

## Files read

- `cmd/pyry/memory_config_test.go` → `testManagedHome`: shared HOME fixture and exact saved/effective path assertions.
- `cmd/pyry/memory_transcripts_test.go` → `TestMemoryTranscriptStorage`: private publication, interrupted replacement, retry and symlink rejection assertions.
- `cmd/pyry/memory_roots_test.go` → `TestMemoryPathValidation`, `TestMemoryReservedAbsentAncestors`: canonical persistence, credential exclusions and transcript ownership.
- `cmd/pyry/memory_roots.go` → `memoryReservedPath`, `validateMemorySettings`: canonical production roots, including absent descendants.
- `cmd/pyry/memory_transcript_files.go` → `memoryTranscriptDir`, `publishMemoryTranscript`: descriptor-relative no-follow traversal and atomic publication.
- `docs/knowledge/features/cli-verb-dispatch.md` § Memory configuration and effective roots: filesystem identity and reserved-root exclusions must stay intact.
- `docs/knowledge/features/config-package.md` § Managed memory shape: CLI validation precedes atomic persistence.
- `docs/knowledge/features/development-verification.md` § Test execution and artifact survival: count executed results and compare equivalent baseline suites.

## Change

Resolve each existing `t.TempDir()` with `filepath.EvalSymlinks`, fail setup through `testMemoryMust` on resolution errors, and only then derive fixture paths. Apply this to `testManagedHome` before chmod/HOME publication and to the root of `TestMemoryTranscriptStorage` before joining its missing storage descendants. Ancestor aliases such as macOS `/var` → `/private/var` otherwise disagree with canonical persisted paths or trip intentional no-follow traversal. Preserve all existing assertions and production behavior. No new type, lifecycle, concurrency or production failure mode is introduced. Remote feature branches have no overlap with the two touched test files. Sizing: approximately 55 written lines including this security-reviewed plan, zero exports, zero consumer updates, two acceptance criteria, zero new state-machine reject branches.

## Testing strategy

Run the five acceptance tests under `go test -race -json -count=1` with default temporary storage, then with a scratch-only symlinked `TMPDIR`. The unchanged assertions must fail with the alias before the repair and pass afterward; the alias experiment isolates the reported macOS ancestor mechanism without claiming macOS execution. Run `go test -race -json -count=1 ./cmd/pyry/...`, `go vet ./...` and `go build -o /tmp/builder-3210/pyry ./cmd/pyry`. Record executed/passed/skipped counts and reasons in the PR; compare any unrelated package failures against the same full package suite on the pre-change commit. The verifier owns the full-module gate. On macOS, acceptance also requires the five tests with default TMPDIR, the complete race-enabled CLI suite and full `make check`, with counts, skip reasons and equivalent baseline attribution. This Linux runner cannot supply macOS execution evidence; explicitly identify that pending platform verification in the PR.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings: only `testing.T.TempDir` fixture roots are resolved; `validateMemorySettings` remains the production validation boundary.
- [Tokens, secrets, credentials] No findings: `TestMemoryConfigurationCredentials` and root-exclusion assertions remain unchanged; fixture canonicalization adds no credential handling.
- [File operations] No findings: resolve the existing fixture root before deriving absent descendants. Deliberately created test symlinks remain unresolved and rejected by `memoryTranscriptDir`/`publishMemoryTranscript`; private modes, atomic replacement and cancellation assertions remain intact.
- [Subprocesses] No findings: `TestMemoryConfigurationProcess` receives the same managed HOME contract with a canonical spelling; no command/environment construction changes.
- [Cryptography] No findings: no cryptographic code or random generation changes; existing transcript temporary names remain production-owned.
- [Network and I/O] No findings: this modifies local fixture setup only; no sockets, wire parsers or read limits change.
- [Errors, logs, telemetry] No findings: resolution failures go only to `testMemoryMust` in hermetic test setup; production errors and logs remain unchanged.
- [Concurrency] No findings: no goroutines or locks are added; `t.Setenv` continues to own HOME cleanup and `t.TempDir` owns directory cleanup.
- [Threat model] No findings: canonicalize trusted fixture ancestry while preserving hostile leaf/ancestor symlink scenarios and credential boundaries; no production threat-model exception is introduced.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-11
