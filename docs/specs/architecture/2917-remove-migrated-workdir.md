# #2917 — Remove the migrated agentrun workdir resolver

## Files read

- `internal/agentrun/workdir.go` → `ResolveWorkdir`, `canonicalCase`, `matchEntry`: obsolete resolver implementation to delete.
- `internal/agentrun/workdir_test.go` → all seven `TestResolveWorkdir_*` cases: redundant coverage to delete; no shared test helpers.
- `internal/canonicalpath/path.go` → `Resolve`, `canonicalCase`, `matchEntry`: migrated absolute-path, symlink and on-disk-case contract.
- `internal/canonicalpath/path_test.go` → `TestResolve_*`, `TestMatchEntry`: preserves all seven old scenarios plus portable exact/unique/ambiguous match coverage.
- `internal/agentrun/exitclass.go` → `ExitErrIsBenign`, and `internal/agentrun/reap.go` → `ReapDescendantGroups`: retained parent-package process helpers.
- `internal/agentrun/trust/trust.go` → `MarkWorkdirTrusted`, and `cmd/pyry/attach_file.go` → `confineFile`, `confineToRoot`, `readChecked`: production consumers already use canonicalpath; trust and confinement remain consumer responsibilities.
- `docs/knowledge/INDEX.md`, `CODING-STYLE.md`: package ownership and testing conventions.
- `docs/knowledge/features/agentrun-package.md`, `canonicalpath-package.md`: ownership prose awaits the documentation stage; canonicalpath's portable match tests must survive filesystem-dependent skips.
- `docs/knowledge/features/development-verification.md` § Establish the change surface / Test execution and artifact survival: supplement codegraph with source search and compile tagged consumers after deletion.

## Change

Delete `internal/agentrun/workdir.go` and `internal/agentrun/workdir_test.go`. Path canonicalisation already belongs to `internal/canonicalpath`; no production or external test callers of `ResolveWorkdir` remain. Keep canonicalpath production code and tests and the parent package's process helpers unchanged. This is one removal deliverable: approximately 55 written plan lines and 260 deleted lines, zero new exported types, zero consumer updates, two acceptance criteria and zero new rejection branches. Codegraph callers/impact and a Go source search found only the seven tests being removed. No other fetched `origin/feature/<ticket>` branch touches these files; there is no unmerged dependency.

## Testing strategy

Before deleting, run a scratch structural assertion that both files are absent and that no non-comment Go source contains `ResolveWorkdir`; it must fail on the current tree and pass after removal. No new behavior or test helper is introduced, so reuse existing tests rather than add replacement tests.

Run `go test -race ./internal/agentrun/... ./internal/canonicalpath/...`, `go vet ./...`, `go build -o /tmp/builder-2917/pyry ./cmd/pyry`, and `go vet -tags e2e_realclaude ./internal/e2e/realclaude/...`. Read canonicalpath's named test results and explicit platform/filesystem/permission skip reasons. The seven retained scenarios are Darwin realpath, already resolved, relative path (`TestResolve_RelativeAndEmpty`), missing path, wrong-case correction, correctly cased mixed case and case-sensitive sibling safety.

Pending dispatcher/verifier gates: `make check` and the `make preship` acceptance proof, including the dispatcher-owned full real-Claude live gate. Keep `needs-real-claude`; its final executed/passed/failed/skipped leaf counts and skip reasons must be recorded. All-skipped execution is not proof. The builder does not run these later-stage gates or claim they passed.

## Documentation handoff

Pending for the documentation stage:

- In `docs/knowledge/features/agentrun-package.md`, update the introduction, “Public API”, “Key shape”, “Consumers”, “History — deleted surfaces” and current-API prose: path resolution belongs to `internal/canonicalpath`; the parent retains `ExitErrIsBenign` and `ReapDescendantGroups`. Link `canonicalpath-package.md`.
- In `docs/knowledge/features/canonicalpath-package.md` § Migration, remove the note that the old API/tests await removal.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. This deletion introduces no boundary; `MarkWorkdirTrusted` and `confineToRoot` already call `canonicalpath.Resolve`, while containment remains in the attachment consumer.
- [Tokens, secrets, credentials] No findings. Neither deleted file generates, stores or logs credentials; no replacement code is added.
- [File operations] No findings. Canonicalpath's exact-match-first and unique-fold behavior remains covered by `TestMatchEntry` and `TestResolve_SiblingSafetyCaseSensitive`. Symlink resolution remains metadata-only; `confineToRoot` and `readChecked` retain containment and identity checks. No new writes or permission modes.
- [Subprocesses] No findings. `ExitErrIsBenign` and `ReapDescendantGroups` and their teardown wiring are unchanged; deleted tests contain no shared subprocess helper.
- [Cryptography] No findings. Only duplicate filesystem resolution is deleted; no cryptographic primitive, key or nonce handling changes.
- [Network and I/O] No findings. No network reader, listener, connection limit or timeout changes; migrated resolver calls remain synchronous metadata operations.
- [Errors, logs, telemetry] No findings. `canonicalpath.Resolve` retains wrapped underlying error identity and its existing error context; no new log or telemetry emission.
- [Concurrency] No findings. Deleted resolver code starts no goroutines and holds no locks; retained process helper cancellation paths are unchanged.
- [Threat model] No findings. This deletion preserves the existing path-resolution and consumer-owned confinement contract; it does not introduce a CLI or mobile capability or defer a new security concern.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-07
