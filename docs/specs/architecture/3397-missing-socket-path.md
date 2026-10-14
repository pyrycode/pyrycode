# Bound the real missing-socket fixture (#3397)

## Files read
- `internal/control/dial_test.go` → `TestIsTransientStartupError`: real ENOENT fixture and classification table.
- `internal/control/server_test.go` → `shortTempDir`: short `/tmp` allocation with test-owned cleanup; reuse without changing it.
- `internal/control/dial.go` → `isTransientStartupError`: ENOENT/ECONNREFUSED classification remains unchanged.
- `docs/knowledge/features/control-plane.md` → control socket contract; `development-verification.md` → canonical fixture roots and executed-test evidence.

## Change
Resolve the directory returned by `shortTempDir` with `filepath.EvalSymlinks`, then derive the absent socket path. Assert its byte length is below 104 and the real `net.Dial` error matches `syscall.ENOENT` before classification. No production behavior, helper contract, concurrency or lifecycle changes. No other feature branch touches the fixture or helper. One deliverable, about 35 written lines, no exported types or consumer updates, two acceptance criteria, no state-machine branches; within all sizing limits.

## Testing strategy
Add the path-bound and ENOENT assertions first and observe the old fixture fail under long TMPDIR roots. Then reuse the short helper and run the existing six-case table with `go test -race -count=1 ./internal/control -run '^TestIsTransientStartupError$'` in separate processes under a long physical directory and a long symlink alias to it. Record roots, canonical old-fixture paths, commands and results in the PR. Run the control package race suite, `go vet ./...` and build `./cmd/pyry` into scratch storage; the verifier owns the full-module gate.
