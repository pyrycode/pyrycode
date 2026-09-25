# #2634 — TestContextUsageResolver_MemoryNeverShadowsLive TempDir cleanup flake

## Files read

- `cmd/pyry/relay_context_usage_test.go` → `TestContextUsageResolver_MemoryNeverShadowsLive` — the flaky test; its second `Get` settles an ok flight whose deferred record writes into the temp dir.
- `cmd/pyry/relay_context_usage_test.go` → `TestContextUsageResolver_SettledFlightRecordsReading` — the poll loop on `conversations.Load(path)` this fix mirrors.
- `cmd/pyry/relay_context_usage_test.go` → `ctxUsageFallbackFor`, `ctxUsageRecorderFor` — return the registry `path` inside `t.TempDir()`.
- `cmd/pyry/relay_context_usage.go` → `contextUsageRecorder.record` — sets the in-memory row, then `reg.Save`; the in-memory row alone is not proof the write finished.
- `internal/conversations/registry.go` → `Registry.Save` — temp file + rename, so once `Load` sees the new reading the rename is done and only a no-op `os.Remove(tmp)` remains.

## Change

`TestContextUsageResolver_MemoryNeverShadowsLive` currently discards the registry path from `ctxUsageFallbackFor`. Keep it, and after the live-reading assertions poll `conversations.Load(path)` (200 × 5ms, same shape as `TestContextUsageResolver_SettledFlightRecordsReading`) until the row's `LastContextUsage` carries the live model `MODEL_2431`; fail if it never does. Polling the file, not the in-memory registry, is what matters: `record` updates memory before `Save`, so a memory poll would still let the test return mid-write. Production code in `cmd/pyry/relay_context_usage.go` is unchanged; the post-`close(f.done)` record ordering in `fly` stays deliberate.

## Testing strategy

No new test. The existing test is the subject; proof is `go test -race -run TestContextUsageResolver_MemoryNeverShadowsLive -count=300 ./cmd/pyry` with 0 failures, quoted in the PR body. The added wait also asserts the ask past the window records the live reading, a behaviour the test already implies.

## Documentation handoff

None.
