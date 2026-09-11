# #2356 — bound context-usage entry lists

## Files read

- `internal/streamsup/parser.go` → `decodeModelWindows`, `truncateField`, `maxModelListEntries` — establishes the package's byte-count convention, stable deterministic cuts, dropped-count semantics, and requirement that returned bounded slices not retain discarded entries.
- `internal/streamsup/parser_test.go` → `TestParser_ModelListEntryCountIsBounded`, `TestParser_ModelListEffortLevelCountIsBounded` — supplies the package's boundary and survivor-order test patterns.
- `internal/e2e/realclaude/testdata/context_usage_v2.1.259.json` → `categories`, `mcpTools`, `memoryFiles` — confirms the future consumers use integer token weights and flat caller-designated string fields.
- `docs/knowledge/features/streamsup-package.md` → “Turn I/O — envelope write + stdout parser” — fixes `internal/streamsup` as the ownership and construction boundary for Claude-authored context-usage data.
- `docs/knowledge/features/development-verification.md` → “Prove that tests distinguish the change” — requires independent boundary and combined-predicate cases rather than length-only assertions.
- `CODING-STYLE.md` → “Testing” and “Comments — Citing Other Code” — requires table-driven stdlib tests and symbol-based citations.

## Context

The upcoming context-usage category and inventory decoders all need the same retained-data contract. Implementing their count, string-size, ordering, ownership, and omission accounting separately would allow the lists to drift. This ticket adds only package-private production infrastructure and its direct unit proof; it does not decode, emit, or log a response.

This is one deliverable. It creates one production source file and one colocated test file, adds no exported type or interface, changes no signature or call site, has three acceptance criteria, and introduces one rejection branch. Estimated written work is about 300–360 lines including this plan and tests, within both the refiner's estimate and the one-ticket boundary.

## Design

Add the package constants `maxContextUsageEntries` (32) and `maxContextUsageStringBytes` (256), shared by every future context-usage breakdown.

Add a generic package-private function with this contract:

```go
func boundContextUsageEntries[T any](
    entries []T,
    tokenWeight func(T) int,
    stringFields func(*T) []*string,
) ([]T, int)
```

For every source entry, the helper shallow-copies the value, asks `stringFields` to designate the string fields on that copy, and rejects the complete entry if any designated string exceeds the byte cap. It clones every designated string on an accepted copy so retained strings do not share the caller's backing storage. The callback deliberately addresses fields on `*T`: future flat decoder structs can name one or several fields without reflection, per-type interfaces, or copy callbacks.

Accepted copies are paired with their token weight and stable-sorted descending. Stability preserves source order for equal weights. The helper then allocates the returned slice at the retained length and copies at most the first 32 values into it. Consequently the returned backing array cannot retain accepted-but-cut values, and rejected values never enter the sortable allocation. The dropped count is always `len(entries) - len(result)`, making string rejection and the count cut additive without separate accounting paths.

Empty input returns a nil slice and zero drops. The helper does not validate token sign or string contents: the ticket defines only ordering and byte length, and later decoders own any semantic validation.

## Concurrency model

The helper is pure with respect to shared state. It creates all working storage per call, starts no goroutines, and mutates only local entry copies. Concurrent callers share only immutable constants.

## Error handling

There is no error return. An overlong caller-designated string is a per-entry omission represented by the dropped count. The callbacks are internal package contracts supplied at static call sites; callback panics or pointers outside the copied entry are programmer errors and receive no speculative recovery path.

## Testing strategy

- Prove an over-cap list retains exactly the 32 heaviest values, ordered descending, while equal weights preserve source order and the input remains byte-for-byte/order unchanged.
- Table-drive designated strings at 256 and 257 bytes, including multiple designated fields, and prove the boundary retains the former and rejects the latter.
- Combine an overlong rejection with more than 32 valid values and assert `dropped == len(input) - len(result)` so neither omission path can overwrite the other.
- Prove output ownership by building designated strings as subslices of mutable byte buffers, calling the helper, mutating the buffers and input slice afterward, and asserting the retained output is unchanged. Also assert the result capacity equals its length so cut entries are unreachable through the returned slice.
- Assert nil and non-nil empty inputs both return no entries and zero drops.
- Run RED before production code, then `go test -race ./internal/streamsup/...`, `go vet ./...`, and `go build ./cmd/pyry`.

## Open questions

None. The future consumer shapes are flat structs, and the acceptance criteria fully specify ordering, byte boundaries, ownership, and omission accounting.

## Revisions

None.
