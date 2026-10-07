# Trust shared path resolver (#2912)

## Files read

- `internal/agentrun/trust/trust.go` → `markWorkdirTrustedIn`, `MarkWorkdirTrusted`: resolve before config access; write and return the same key.
- `internal/agentrun/trust/trust_test.go` → `TestMarkWorkdirTrusted_*`: retain flags, preservation, modes, errors, symlink and case coverage.
- `internal/canonicalpath/path.go` → `Resolve`, `canonicalCase`, `matchEntry`: shared absolute/realpath/on-disk-case and wrapped-error contract.
- `internal/canonicalpath/path_test.go` → `TestResolve_*`, `TestMatchEntry`: relative/empty input, macOS aliases, sibling safety and error identity.
- `internal/agentrun/workdir.go` → `ResolveWorkdir`: compare the old resolver with the merged #2911 implementation.
- `cmd/pyry/main.go` → `runSupervisor`, `resolveSpawnDir`, `sessionMinter.Create`: confinement before trust, returned path passed to bootstrap or mint.
- `internal/sessions/pool.go` → session runner construction: bootstrap/template or explicit spawn directory becomes `RunnerConfig.WorkDir`.
- `internal/streamsup/runner.go` → `New`, `SetSpawnWorkDir`, `beginSpawn`, `spawnAndWait`: re-resolution and snapshot through to `cmd.Dir`.
- `internal/agentrun/selfcheck/selfcheck.go` → `SelfCheckDenyDefault`; `internal/agentrun/streamrunner/runner.go` → `Run`: returned trust path becomes the self-check child cwd.
- `internal/e2e/realclaude/claude_md_external_includes_test.go` → `TestClaudeMdExternalIncludes_SubfolderChildGetsRootImports`: both real-git-root differential arms and their CLAUDE.md vacuity guards.
- `docs/knowledge/features/agentrun-trust-subpackage.md` → Key shape, Consumers, No lock: case canonicalisation is required; confinement belongs to callers and atomicity does not serialize writers.
- `docs/knowledge/features/canonicalpath-package.md` → Path and error contract: exact-case precedence, best-effort casing and metadata-walk limitations.
- `docs/knowledge/features/development-verification.md` → Prove that tests distinguish the change: a second distinguishable flaw proves ordering.
- `CODING-STYLE.md`, `docs/knowledge/INDEX.md`, `docs/protocol-mobile.md` → conventions, topic map and Security model.

## Change

Replace the trust production call and nine test calls to `agentrun.ResolveWorkdir` with `canonicalpath.Resolve`, updating imports and test diagnostics. The merged resolver preserves the existing contract; trust still resolves before accessing `.claude.json`, uses that result as the `projects` key, and returns it unchanged. Trust policy, confinement and spawn wiring stay with their existing owners. No decision record is needed.

One deliverable, approximately 180–220 written lines including plan and regression tests (more than the ~65-line estimate because the security trace and independent path/ordering assertions are included). No exported types, ten replaced calls (one production, nine tests), two acceptance criteria, no new error branches. All sizing limits hold. Remote feature branches checked after fetching: none overlap the two target files. #2911 is merged; no open dependency remains.

## Testing strategy

Migrate test expectations first. Add independent returned-path/key assertions for relative input and exact-case siblings; retain symlink and filesystem-gated wrong-case coverage. Add a missing-workdir/invalid-home fixture asserting empty return and `errors.Is(err, fs.ErrNotExist)`: config stat would instead fail with a different error, proving resolution precedes access. Characterisation tests should already pass because this migration preserves behaviour. Demonstrate relevant assertions fail under temporary overlays that bypass resolution or move config access before it, without modifying production before the plan commit.

Run `go test -race ./internal/agentrun/trust/...`, `go vet ./...`, and `go build -o /tmp/builder-2912/pyry ./cmd/pyry`. The verifier owns `make check` and the full-module suite. The dispatcher owns the live `TestClaudeMdExternalIncludes_SubfolderChildGetsRootImports` gate: both `marked_root_expands_external_import` and `unapproved_root_drops_external_import` must execute and pass; skips are not proof.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/agentrun-trust-subpackage.md`, update “Key shape — realpath, on-disk case, not abspath”, “Error handling”, “Dependency direction” and “Testing” to name the shared resolver and retain the trust-key/error contract.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `markWorkdirTrustedIn` resolves untrusted path spelling with `canonicalpath.Resolve` before config access, writes all three approval flags only under `projects[realpath]`, and returns that same string. `runSupervisor` and `resolveSpawnDir` confine under home before trust; `SelfCheckDenyDefault` intentionally accepts operator paths without confinement.
- [Tokens] `.claude.json` may hold credentials: preserve unknown fields through `UseNumber`, retain existing mode or create at `0600`, and keep contents out of errors/logs. The resolver reads metadata only and receives no token values.
- [File operations] Symlinks are deliberately resolved; exact-case matching prevents a case-differing sibling being trusted. Existing same-directory temporary write, sync, close and rename remain intact. This metadata resolver is not a stable handle: existing same-user replacement windows remain accepted by the caller contract, with no widened trust policy or new write target.
- [Subprocesses] Bootstrap return flows through `Bootstrap.WorkDir` and pool runner configuration into `streamsup.New`; conversation return flows through `sessionMinter.Create` and `MintWith` into the same runner. `New`/`SetSpawnWorkDir` still use the equivalent old resolver; `beginSpawn` snapshots `workDir` and `spawnAndWait` assigns it to `cmd.Dir`. Self-check passes its returned path into `streamrunner.Config.WorkDir`, which `Run` assigns to `cmd.Dir`. No shell interpretation is introduced.
- [Cryptography] No cryptographic operations, secret comparison, randomness or nonce lifecycle changes; the new dependency is a stdlib filesystem resolver.
- [Network and I/O] No socket or HTTP changes; phone-requested paths continue through caller confinement. Config size limits remain the documented same-user trust model; resolution adds no new I/O class.
- [Errors/logs] Keep `%w` wrapping and empty returns on failure; missing workdirs must fail before any config stat/read. No new logging, config bytes or parsed credential fields enter diagnostics.
- [Concurrency] No new goroutines or locks; invocation stays synchronous. Atomic rename protects interrupted writes, while existing cross-process lost updates remain the documented best-effort policy.
- [Threat model] `docs/protocol-mobile.md` Security model treats relay transport as untrusted; this migration changes neither authentication nor remote permissions. Caller-side filesystem confinement remains the path boundary. Live include approval is keyed on the canonical git root for subfolder children; both existing differential arms remain required evidence at the dispatcher gate.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-07
