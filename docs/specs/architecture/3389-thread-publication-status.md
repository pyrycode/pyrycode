# Metadata-only thread publication status

## Files read
- `internal/thread/store.go` → `Snapshot`, `publish`, `start`, `Unload`, `Shutdown`: publication mutex, readiness and retirement rules.
- `internal/thread/observations_store.go` → `Observe`: complete baselines must retain their detached items.
- `internal/thread/store_test.go` → `TestStoreFailureRetry`, `TestStoreLifecycle`, `TestStoreReadSecurity`: real failure, replacement, retirement and storage isolation barriers.
- `internal/thread/store_shadow_test.go` → `TestStorePrivateActiveWork`: active private work can coexist with an empty public item list.
- `docs/knowledge/features/thread-package-background-store.md` → **Snapshots and retry**: preserve generic readiness errors and publication metadata.
- `docs/knowledge/features/thread-package.md` → **Shadow lifecycle and evidence**: polling consumers need metadata but baseline publication needs items.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md`: local changes, race checks and behavioural assertions.

## Context
Daemon polling currently copies a complete publication even when only progress
and readiness matter. This ticket adds the store read; consumer migration belongs
to #3391 and #3330. No decision record is needed.

## Design
Add `Store.Status(conversations.ConversationID) Snapshot` beside `Snapshot`.
Capture the worker's immutable snapshot under `Store.mu`, with the same absent
and retiring guard as `Snapshot`, then clear the returned `Items` slice. Return
the captured State, Version, Epoch, Active, LastShownVersion and Err unchanged.
There is no I/O, load, fold access, item traversal or allocation. `Snapshot` and
`Observe` keep their existing complete detached-read behaviour.
Overlaps: #3382 and #3384 change cache/replay/rendering in `store.go`; this adds
only the new read beside `Snapshot` and needs neither branch.
Sizing: approximately 220 written lines including tests and plan, zero new
exported types/interfaces, zero consumer updates, two acceptance criteria and
no new error branches; all five limits remain below their ceilings.

## Concurrency model
No new goroutines or lifecycle state. The existing store mutex serializes status
capture with publication, replacement and withdrawal. Test writers/readers join
before their test exits; existing worker tests retain their cancellation paths.

## State transitions and identity reuse
| Event | Race-detector coverage |
| --- | --- |
| Repeated reads and concurrent publication/replacement of the same ID | `TestStoreStatusConcurrent`, `TestStoreStatusParity` |
| Failed replay/tail and retry replacement | `TestStoreFailureRetry` with status parity assertions |
| Unload while replay/tail is held, then reload the same ID | `TestStoreLifecycle` with status parity assertions |
| Context cancellation and concurrent/repeated shutdown | `TestStoreLifecycle` with status parity assertions |
| Private active work closing without public items | `TestStorePrivateActiveWork` with status parity assertions |

## Error handling
Only unavailable carries `ErrUnavailable`; absent, rebuilding, usable and
retiring/closed reads preserve Snapshot's nil error. No new failure modes.

## Testing strategy
Write tests first and observe the missing-method compilation failure. Table-test
all readiness states and populated/empty/private-active publications against
Snapshot. Read synthetic stores without a history backend and measure zero
allocations for empty, short and 30,000-item publications. Race concurrent reads
against distinct correlated metadata publications, replacement and retirement.
Extend real lifecycle, retry and renamed-storage tests with parity assertions.
Run `go test -race ./internal/thread/...`, `go vet ./...` and
`go build ./cmd/pyry` with output outside the worktree. The verifier owns
`make check` and its full-module suite.

## Open questions
None. Reusing Snapshot with nil Items avoids a redundant metadata type.

## Documentation handoff
Pending for the documentation stage: in
`docs/knowledge/features/thread-package-background-store.md`, **Snapshots and
retry**, describe `Store.Status` as a metadata-only status read with unchanged
readiness rules.
