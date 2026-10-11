# #3184: Short socket path for the Seal ownership test

## Files read

- `internal/control/seal_test.go` → `TestServerSealRetainsOwnership`: socket setup, ownership assertions and deferred server teardown.
- `internal/control/server_test.go` → `shortTempDir`: unique `/tmp` directory with `t.Cleanup` removal independent of `TMPDIR`.
- `docs/knowledge/features/control-plane.md` and `control-plane-testing.md`: control-plane test conventions and short socket paths.
- `docs/knowledge/features/development-verification.md` → Test execution and artifact survival: verify named test execution, not only exit status.
- `docs/specs/architecture/860-e2e-short-socket-path.md` → Cleanup: short socket directories must be removed after teardown.

## Change

Replace `t.TempDir()` with `shortTempDir(t)` in the socket path of `TestServerSealRetainsOwnership`. The existing helper avoids the macOS Unix socket path limit under its default long `TMPDIR`. The admitted-writer, Seal, ownership, late-writer rejection and repeated-Seal assertions remain intact. Deferred server teardown runs before the helper's registered cleanup, including on test failure. No production behavior, types, lifecycle logic or error branches change. Remote feature branches currently have no overlap with `seal_test.go`.

Sizing: one deliverable, one test-line replacement plus this short plan (under 40 written lines), zero new exports, one consumer updated, two acceptance criteria and zero new reject branches; all builder limits hold.

## Testing strategy

Run the existing `TestServerSealRetainsOwnership` under the race detector before and after the change with an intentionally long `TMPDIR`: the baseline must fail at socket bind and the fix must reach all existing assertions. Run it with the host's default `TMPDIR`, then `go test -race ./internal/control/...`, `go vet ./...` and `go build -o /tmp/builder-3184/pyry ./cmd/pyry`. Review the unchanged deferred teardown and helper cleanup ordering. This Linux host cannot execute the macOS default-`TMPDIR` check; macOS validation remains for the verifier. The dispatcher owns the full-module gate.
