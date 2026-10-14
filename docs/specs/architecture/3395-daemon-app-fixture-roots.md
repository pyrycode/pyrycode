# Canonical daemon app fixture roots (#3395)

## Files read

- `cmd/pyry/daemon_apps_test.go` → `TestDaemonAppsStorage`, `TestDaemonAppsComposition`, `TestDaemonAppsCompositionFailure`: fixture roots and existing behavior/refusal assertions.
- `cmd/pyry/memory_config_test.go` → `testRealTempDir`: resolves only a test-owned temporary root.
- `cmd/pyry/daemon_apps.go` → `openDaemonApps`, `newDaemonAppsManager`: storage refusal and manager construction contracts.
- `docs/knowledge/features/apps-package.md` and `apps-package-concurrency-and-testing.md` § Testing: canonicalize fixture ancestors while preserving unsafe leaves.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: aliased ancestors can mask valid-state behavior.

## Change

Use `testRealTempDir(t)` for HOME in storage subtests and for the registry parents in both composition tests before deriving paths. Keep the intentionally symlinked apps root and all existing assertions unchanged. No production code, types, state or failure modes change. No overlapping remote feature branch touches this test file. Size: about 40 written lines including this plan, zero exported types or consumer updates, two acceptance criteria, zero new reject branches.

## Testing strategy

Before editing tests, run the three existing tests under a deliberately aliased TMPDIR and confirm valid fixture setup fails. After editing, run `go test -race -count=1 -v ./cmd/pyry -run '^TestDaemonApps(Storage|Composition|CompositionFailure)$'` separately with a physical TMPDIR and its symlink alias. Record commands, supplied/resolved roots and executed/pass/fail/skip counts in the PR. Existing assertions cover successful registration/composition, corrupt/foreign/unreadable/symlink refusal, rejected-byte preservation, safe logs and invalid manager construction. Run the touched package race suite, `go vet ./...` and `go build ./cmd/pyry`; the verifier owns the full-module suite.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `openDaemonApps` and `apps.Open` keep their existing untrusted-storage boundaries; only test-owned ancestors are resolved.
- [Tokens] Existing composition credentials are synthetic test values; no credential handling changes.
- [File operations] Resolve roots before deriving paths; preserve `TestDaemonAppsStorage/symlink` as an actual symlink and retain permission/byte assertions. Production no-follow rules remain unchanged.
- [Subprocesses] This change starts no subprocess and changes no child environment.
- [Cryptography] Composition continues to use its existing X25519/Noise setup; no primitives, keys or nonce handling change.
- [Network and I/O] Existing in-memory composition channels and their assertions remain unchanged; no listener or new input surface.
- [Errors, logs, telemetry] Existing safe-log checks still reject storage-marker and root-path disclosure.
- [Concurrency] Existing composition cancellation, detach and manager-drain cleanup remain unchanged; no new goroutine or shared state.
- [Threat model] No production trust or isolation change; malicious apps-root symlink refusal stays covered by the existing storage test.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-14
