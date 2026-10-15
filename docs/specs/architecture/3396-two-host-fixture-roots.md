# Canonical two-host fixture roots (#3396)

## Files read

- `internal/apps/registry_test.go` → `TestTwoHosts`, `testRoot`, `testOpen`: canonical fixture helper and host-isolation assertions.
- `internal/apps/storage.go` → `openRegistry`, `snapshotOps.write`: identity/storage sentinels and private directory enforcement.
- `docs/knowledge/features/apps-package.md`, `apps-package-testing.md`: package boundaries and discriminating fixtures.
- `docs/knowledge/features/apps-package-concurrency-and-testing.md` § Testing: canonicalize fixture ancestors so no-follow rejection cannot mask valid operations.
- `docs/knowledge/features/development-verification.md`: exact refusal checks and executed test evidence.
- `CODING-STYLE.md`: error matching and race-test conventions.

## Change

Use existing `testRoot(t)` instead of raw `t.TempDir()` for the second host and invalid-host fixture in `TestTwoHosts`. Require `ErrInvalidStorage` for relative/empty roots and `ErrInvalidIdentity` for the invalid host with a valid canonical root. Preserve distinct-host registrations and both foreign-host refusals. Production path validation stays unchanged. No overlapping feature branches touch the test file. One deliverable; approximately 45 written lines including this plan, no new exported types, consumer updates or production reject branches; two acceptance criteria.

## Testing strategy

Reproduce the existing `TestTwoHosts` failure under a deliberately aliased TMPDIR before edits. Run `go test -race -count=1 -run '^TestTwoHosts$' -v ./internal/apps` with physical and deliberately symlinked TMPDIR roots after edits; record supplied/resolved paths and executed/pass/fail/skip counts in the PR. Run `go test -race ./internal/apps/...`, `go vet ./...` and `go build ./cmd/pyry`. The verifier owns the full-module suite.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `TestTwoHosts` supplies test-owned roots and fixed host IDs; `openRegistry` keeps host/storage boundary checks intact.
- [Tokens, secrets, credentials] No credential flow: the changed arguments are temporary fixture paths and fixed UUIDs.
- [File operations] `testRoot` resolves only test-owned temporary ancestors; production `snapshotOps.write` retains private directory validation and atomic persistence. Deliberately invalid relative/empty inputs remain raw.
- [Subprocesses] No subprocess added to `TestTwoHosts`.
- [Cryptography] No cryptographic operation or key material is involved.
- [Network and I/O] `TestTwoHosts` uses only local registry fixtures, with no socket or server changes.
- [Errors, logs, telemetry] Exact sentinel assertions prevent unrelated storage failures from satisfying identity rejection; failures contain only test fixture diagnostics.
- [Concurrency] Existing `t.Parallel` and per-test roots remain; no goroutine or shared lifecycle state added.
- [Threat model] The local no-follow boundary is unchanged; canonicalizing production inputs is outside this design and is not proposed.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-14
