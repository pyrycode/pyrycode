# Canonical hosted lifecycle fixture roots (#3394)

## Files read
- `internal/relay/v2session_hosted_changes_test.go` → `testLifecycleRegistry`: shared setup for committed-wire, suppression and host-isolation assertions.
- `internal/apps/registry_test.go` → `testRoot`: existing canonical test-root pattern.
- `internal/apps/storage.go` → `Open`; `internal/apps/runtime.go` → `privateDir`: registry setup and production no-follow boundary.
- `docs/knowledge/features/relay-package.md` § Test surface: exercise real behavior through existing fixtures.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: resolve fixture roots before deriving paths, preserving unsafe leaves.
- `docs/protocol-mobile.md` § Security model: relay trust and authentication boundaries remain unchanged.

## Change
Resolve the existing `t.TempDir()` with `filepath.EvalSymlinks` in `testLifecycleRegistry`, fail with `t.Fatalf` if resolution fails, then derive the apps root from that canonical directory. Existing lifecycle assertions and all production code remain unchanged. No overlapping feature branch touches this file. One deliverable, approximately 40 written lines including this plan, no exported types or consumer updates, one acceptance criterion and one fixture error branch: within all sizing limits.

## Testing strategy
Use the existing `TestHostedLifecycleCommittedWire`, `TestHostedLifecycleCommitSuppression` and `TestHostedLifecycleHostIsolation` assertions as the regression. Observe failure first with a deliberately symlinked TMPDIR, then run the same three tests uncached with the race detector under physical and aliased TMPDIR roots. Required native macOS evidence must include supplied/resolved roots, exact commands and executed/passed/failed/skipped counts (3/3/0/0 per run). Request the managed native route after pushing the full candidate; a platform probe alone cannot establish this proof. Also run `go test -race ./internal/relay/...`, `go vet ./...` and `go build -o /tmp/builder-3394/pyry ./cmd/pyry`. The verifier owns the full-module gate.

## Security review
**Verdict:** PASS

**Findings:**
- [Trust boundaries] `testLifecycleRegistry` resolves only the directory allocated by `t.TempDir`; no phone-supplied app path is canonicalized.
- [Tokens, secrets, credentials] The helper neither creates nor reads credentials; existing synthetic peer credentials are unchanged.
- [File operations] Resolve the existing temporary parent before joining the absent apps child. Production `privateDir` retains component-wise no-follow checks and existing private modes; deliberate unsafe leaves remain unsafe.
- [Subprocesses] No new child process or shell execution in tests; validation uses existing Go tooling.
- [Cryptography] No primitive, key, nonce or comparison changes; the existing encrypted peers are unchanged.
- [Network and I/O] No change to listeners, frame bounds, connection admission or timeouts.
- [Errors, logs, telemetry] Resolution failure terminates fixture setup with local test diagnostics; no production error surface changes.
- [Concurrency] No new goroutine, shared mutable state or lock; existing peer cleanup and concurrent lifecycle assertions remain intact.
- [Threat model] The relay's authentication, ciphertext and host-authority boundaries are unchanged; this is test-owned filesystem setup only.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-14

## Revisions
2026-10-15: Native macOS arm64 is available on this builder, so the required focused checks ran locally against pushed candidate `59e08b3517cc161a41a568b577266ae1e4fdc34b` rather than requesting another platform probe. No design or assertion changes were needed.

Both used `go test -race -count=1 -json -run '^TestHostedLifecycle(CommittedWire|CommitSuppression|HostIsolation)$' ./internal/relay`, first with `TMPDIR=/private/tmp/builder-3394-native/physical`, then with `TMPDIR=/private/tmp/builder-3394-native/alias`. The alias was deliberately created as a symlink to the physical directory; both resolve to `/private/tmp/builder-3394-native/physical`. Each run executed/passed/failed/skipped **3/3/0/0**. Exact commands and native evidence are recorded in PR #3405.
