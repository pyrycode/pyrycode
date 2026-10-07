# #2913: streamsup shared path resolver

## Files read

- `internal/streamsup/runner.go` → `New`, `Runner.SetSpawnWorkDir`, `Config.WorkDir`, `Runner.workDir`: resolver calls, empty guards, error wrapping and directory-pair locking.
- `internal/streamsup/runner_test.go` → `TestNew_RequiredFields`, `TestNew_WorkDirMustExist`: empty-workdir rejection and wrapped error identity.
- `internal/streamsup/runner_workdir_test.go` → `TestSetSpawnWorkDir_SuccessorSpawnsInInstalledDir`, `TestSetSpawnWorkDir_SwapsClaudeSessionsDirToo`, `TestSetSpawnWorkDir_Rejected`: actual child cwd, supplied transcript directory and fail-closed installs.
- `internal/canonicalpath/path.go` → `Resolve`, `canonicalCase`: merged #2911's absolute, symlink-resolved, best-effort on-disk-case contract and preserved error context.
- `internal/canonicalpath/path_test.go` → `TestResolve_RelativeAndEmpty`, `TestResolve_FilesAndSymlinks`, `TestMatchEntry`: existing independent resolver coverage.
- `internal/agentrun/workdir.go` → `ResolveWorkdir`: existing resolver semantics to preserve.
- `docs/knowledge/features/streamsup-package.md` → introduction, Public API, Dependency direction: process-helper dependency remains.
- `docs/knowledge/features/canonicalpath-package.md` → Path and error contract: empty resolver input means cwd, so consumer guards must remain.
- `docs/knowledge/features/agentrun-package.md` → Consumers: staged migration leaves the old resolver for other tickets.
- `docs/knowledge/features/development-verification.md` → Verify inherited premises: use the merged resolver and existing tests rather than duplicate their proof.
- `CODING-STYLE.md`: wrapped errors, scoped race tests and symbol-based comments.

## Change

Replace the two `agentrun.ResolveWorkdir` calls in `New` and `Runner.SetSpawnWorkDir` with `canonicalpath.Resolve`, add its import and update resolver/dependency comments. Retain agentrun's process helpers. Keep both empty guards, `%w` wrappers, resolution before `restartMu`, and the single locked installation of resolved workdir plus caller-supplied transcript directory. A failure leaves both fields untouched. No API, state, goroutine, transcript-path derivation or failure-mode changes are needed.

The branch scan found no overlap on `runner.go` or `runner_workdir_test.go`; #2911 is merged. One deliverable, approximately 65 written lines including this plan, no new exported types, two internal call-site substitutions, two acceptance criteria and no new reject branches: all sizing limits hold.

## Testing strategy

Run existing constructor/workdir tests before and after migration: `TestNew_RequiredFields`, `TestNew_WorkDirMustExist`, `TestSetSpawnWorkDir_*` and `TestRestartFresh_WithoutInstall_KeepsExistingDir`. This behavior-preserving substitution adds no logic needing new tests. Run `go test -race ./internal/streamsup/...`, `go vet ./...` and `go build ./cmd/pyry` (write the build output outside the worktree). The dispatcher verifier owns `make check`, including the full-module suite. Inspect source to confirm both shared resolver calls and unchanged guards/locking.

## Documentation handoff

- Pending documentation stage: in `docs/knowledge/features/streamsup-package.md`, update the introduction, “Public API” and “Dependency direction” to name `canonicalpath.Resolve` while retaining agentrun's process-helper dependency.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `New` and `Runner.SetSpawnWorkDir` canonicalise caller-supplied paths; canonicalisation grants no trust or confinement authority. Both empty guards stay before `Resolve` so buggy callers cannot silently select the daemon cwd.
- [Tokens, secrets, credentials] Only the resolver import and calls change; `AccountTokenProvider` and child-token handling are untouched and no new credential storage or exposure is introduced.
- [File operations] `Resolve` performs metadata reads and follows symlinks with the same case rules as `ResolveWorkdir`; it creates no files. Resolution is not a stable handle or confinement check. The migration preserves that contract and passes transcript directories through verbatim.
- [Subprocesses] Only `cmd.Dir`'s resolver dependency changes. Existing direct exec, argv/environment construction, cancellation and `agentrun.ReapDescendantGroups` remain intact.
- [Cryptography] Path resolution introduces no keys, randomness, secret comparisons or cryptographic operations.
- [Network and I/O] The changed calls do synchronous filesystem metadata I/O before `restartMu`; no socket/parser limits or network operations change.
- [Errors, logs, telemetry] Existing `%w` wrappers preserve `errors.Is`; existing path diagnostics and the empty-input warning remain. No file contents or credentials enter new diagnostics.
- [Concurrency] `Runner.SetSpawnWorkDir` resolves before locking and writes both fields in the same `restartMu` section. Failure returns before any mutation. No goroutines or lock-order changes are introduced.
- [Threat model] This migration preserves caller-owned workspace policy and process teardown; it does not alter relay authentication, sandbox policy or filesystem authorization.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-07
