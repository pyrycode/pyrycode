# #3369 — Selective history entry lookup

## Files read
- `internal/history/log.go` → `Store`, `convLog`, `AppendWithMetadata`, `load`, `readSegment`, `resolveDir`, `readStats`: mutex, successful-write bookkeeping, bounded reads and containment.
- `internal/history/segment.go` → `listSegments`, `segEntry`, `decodeSegment`: regular membership, opaque decoding and torn-tail tolerance.
- `internal/history/forward.go` → `forwardSegment`: current leaf recheck pattern; forward readers remain unchanged.
- `internal/history/log_test.go`, `metadata_test.go` → `appendN`, `TestMetadataRoundTrip`: roll fixtures and exact metadata assertions.
- `docs/knowledge/features/history-package.md` → Directory resolution and Concurrency: never retain resolved paths, retain the existing single-call check/use boundary.
- `docs/knowledge/features/history-package-failed-write-recovery.md`: empty/header-only and torn segments must preserve preceding complete entries.
- `docs/knowledge/features/history-package-shape.md`, `development-verification.md`, `CODING-STYLE.md`, ADR 045: opaque storage, authenticated conversation precondition, test evidence and target index architecture.
- QMD search for history segment cache: watermark spec and overview reinforce per-call containment.

## Context
The thread will refer to durable entry IDs rather than retaining content. This ticket supplies one independently usable lookup API, with no production adoption or format change. ADR 045 already records the architecture; no additional decision record is needed. No current remote feature branch touches history. Sibling #3370 will share append bookkeeping but supplies no dependency.

Sizing: approximately 750 written lines (320 production, 360 tests, 70 plan); one exported error type, zero updated consumers, four acceptance criteria, at most nine rejection categories. Both sketch and plan meet the five builder limits.

## Design
`Store.Lookup(convID, ids []uint64) ([]Entry, error)` accepts strictly ascending unique IDs, validates conversation shape, returns requested entries in order, and returns no partial result on error. Zero is a missing ID. `MissingEntriesError` carries all absent IDs; invalid ordering has a sentinel error. Empty input returns an empty result after containment recheck, tolerating an absent log without creating directories.

Per-conversation `convLog` keeps segment number to first-ID/size bounds, never paths. Every call resolves containment and lists current regular segments, then rechecks leaf status/size. Unknown or resized segments discover only a bounded header/first-entry prefix; empty or torn first entries have no range. Successful appends update known bounds and rolls introduce the new first ID. Lookup groups IDs by first-ID ranges and decodes only selected segments through `readSegment`, checking stored ID order before searching. Missing ranges never fabricate content.

One Store-wide LRU keyed by conversation, segment number and file size retains decoded rolled segments; the newest segment is always read fresh. Default limit is 8 MiB. Accounting includes capacity of the `segEntry` backing array, payload capacity, type strings, pointed-to provenance struct and its strings, and visibility booleans. Shared time locations and cache/index bookkeeping are excluded from decoded-data accounting. Oversized decoded segments bypass retention. Return values clone payloads and pointed-to metadata. Discovery accounts actual underlying read bytes, including buffered read-ahead, and successful opens; cache hits perform filesystem membership checks but no segment opens/reads.

## Concurrency model
All lookup I/O, bounds, cache operations and append updates use existing `Store.mu`. No goroutines, callbacks or new locks. Lookup returns detached mutable data after unlocking.

## State transitions and identity reuse
| Event | Race-detector coverage |
| --- | --- |
| Rolls, reopening and repeated lookup | `TestLookupRoundTrip` |
| Discovery followed by selective reads and external segment addition | `TestLookupSelectiveReads` |
| Empty/header-only segment gains its first complete entry; append and roll freshness | `TestLookupFreshness` |
| Torn tail followed by restart and append | `TestLookupTornTail` |
| Removed or symlink-replaced file, cold and cached; directory redirect after cache fill | `TestLookupMembership` |
| Same segment/ID across conversations, LRU promotion/eviction and oversized bypass | `TestLookupCache` |
| Cached file is resized or becomes newest after later files are removed | `TestLookupCache` |
| Concurrent appends and repeated lookups | `TestLookupConcurrent` |

## Error handling
Input rejection precedes I/O. Absent logs identify every requested ID as missing. Path/leaf failures, unsupported versions, complete corruption, non-increasing stored IDs and oversized selected segments return errors without partial entries. Torn trailing bytes are ignored exactly as in `decodeSegment`. Error text never contains payload or metadata. Cache state cannot authorize a read after membership or containment fails.

## Testing strategy
Write hermetic lookup tests first and observe failure before implementing. Compare exact stored values, including legacy and optional metadata, across rolls/reopen; assert missing-ID lists and nil partial results. Measure discovery and selective/cache-hit I/O with `readStats`; poison an unselected segment's later body to distinguish selective reads. Use a small cache limit to force cross-conversation eviction and oversized bypass. Exercise cold/warm removal and symlink redirects separately. Run `go test -race ./internal/history/...`, `go vet ./...`, and `go build ./cmd/pyry` (output in scratch). The verifier owns the full-module gate.

## Open questions
None. Constructor signatures remain unchanged; cache limits are private and test-adjustable.

## Documentation handoff
Pending documentation stage: `docs/knowledge/features/history-package-shape.md`, **Shape**: list the implemented lookup signature and describe ordered/all-or-error results, missing-id errors, detached returned data and selective-read/cache semantics. Document the implemented cache byte accounting and 8 MiB default.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: `Lookup` validates ID shape/order; authenticated-conversation selection remains the caller's precondition. `decodeSegment` remains the opaque disk-data boundary.
- Tokens/secrets: no credentials generated or stored; returned history is sensitive content and errors do not disclose it.
- File operations: `resolveDir` and current regular-leaf checks run before cache access. Existing ordinary-open check/use window within daemon-owned 0700 directories remains unchanged; cached paths would enlarge it and are forbidden. Lookup creates/writes nothing.
- Subprocesses/cryptography/network: no new process, cryptographic or network operations.
- I/O: discovery is bounded by `segmentCeiling`; selected full reads reuse its ceiling; retained decoded data has a byte limit.
- Errors/telemetry: only IDs and existing filesystem error context appear; counters aggregate opens/bytes, never payloads.
- Concurrency: one existing mutex covers membership checks and state/cache changes; no goroutines or new shutdown requirements.
- Threat model: internal authenticated-conversation storage API only; relay authorization and later API adoption belong to #3333.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-14

## Revisions
- 2026-10-14: Reviewing Go's JSON timestamp decoding showed non-UTC fixed-offset locations can be allocated per entry. Cache accounting reserves an additional 256 bytes per non-UTC/non-Local timestamp for that location and its zone/transition arrays; shared UTC/Local remain excluded. `TestLookupCacheAccounting` covers capacity, metadata and this reservation. Metadata round-trip fixtures now seal the segment before mutation to prove cache detachment. Buffered discovery fixtures contain six entries so actual read-ahead remains distinguishable from whole-file reads.
