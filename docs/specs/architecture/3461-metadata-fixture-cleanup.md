# Metadata fixture cleanup

## Files read

- `cmd/pyry/daemon_thread_metadata_test.go` → `testThreadMetadataFixture`, `TestDaemonThreadMetadataAllocations`, `BenchmarkDaemonThreadMetadata`: shared worker ownership and existing allocation/watermark checks.
- `internal/thread/store.go` → `Load`, `Shutdown`: the load parent governs worker lifetime; shutdown cancels and joins workers before returning.
- `docs/knowledge/features/thread-package.md` and `thread-package-background-store.md` § Snapshots and retry: clean retirement requires the original load parent to remain uncancelled.
- `docs/knowledge/features/development-verification.md` and `CODING-STYLE.md`: focused behavioral verification and context ownership conventions.

## Change

Give `testThreadMetadataFixture` a cancellable background context immediately before constructing the store. Register cleanup before loading: defer the owned cancellation inside cleanup, call joined `Store.Shutdown`, and retain shutdown error reporting. Load using the owned context so testing-context cancellation cannot invalidate clean certification. Cleanup still runs after fixture fatal errors. Preserve fixture data, allocation assertions, the unregistered-conversation watermark check and all production behavior. The benchmark uses the same repaired fixture.

One deliverable, approximately 20–30 written lines including this plan; no new exported types, consumer signature updates, acceptance criteria or reject branches. The issue has two acceptance criteria. No other fetched feature branch touches the target file.

## Testing strategy

Reproduce `TestDaemonThreadMetadataAllocations` failing only in parent cleanup, then rerun with `go test -race ./cmd/pyry -run '^TestDaemonThreadMetadataAllocations$' -count=1 -v`: the parent and both allocation subtests must pass with no skips. Run the benchmark once to exercise its cleanup. Run `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build ./cmd/pyry` (write the binary outside the worktree). The dispatcher/verifier owns the complete `make check` acceptance gate.
