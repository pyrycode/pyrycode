# Immutable text publication

## Files read
- `internal/thread/text.go` → `growText`, `appendText`, `textEnrichment`: private intrinsic/derived JSON storage and counted geometric growth.
- `internal/thread/publication.go` → `publicationCapture.capture`: immutable borrowing boundary and previous items used for suffix differences.
- `internal/thread/child.go` → `restoreChild`, `resolveChildren`: intrinsic restoration and cached provisional closure through reopening.
- `internal/thread/main.go` → `foldWork`, `setContent`: append dispatch and non-text enrichment replacement.
- `internal/thread/render.go` → `renderShared`, `render`: internal borrowing versus detached public output.
- `internal/thread/queries.go` → `newPublicationIndex`, `captureQuery`, `window`: retained roots and detached query content.
- `internal/thread/store.go` → `publish`, `Snapshot`: serialized producer and concurrent readers outside the store lock.
- `internal/thread/publication_test.go` → `TestAffectedPublicationOracle`, `TestAffectedPublicationOwnership`: unchanged oracle and the pre-initialization ownership coverage gap.
- `internal/thread/text_test.go` → `TestTextAppendChildren`, `TestTextAppendScaling`, `TestFoldIngestionScaling`: lifecycle and ingestion-work contracts.
- `docs/knowledge/features/thread-package-main-thread-folding.md` → Main-work source entries; Scoped joins, pending outcomes and closure; Validation and testing: preserve exact bytes, incremental summaries and provisional intrinsic evidence.
- `docs/knowledge/features/development-verification.md` → Prove that tests distinguish the change: reproduce against the old implementation.
- `CODING-STYLE.md`: concurrency, contract comments and testing conventions.

## Context
An initialized text buffer can overwrite closing JSON bytes already borrowed by an internal publication. Prior snapshots then cease to represent their revision, suffix comparison breaks, and queries can race with Feed. One ownership repair restores the immutable publication contract; no decision record is needed.

Sizing: about 320 written lines, no exported types, one internal capture hook, three acceptance criteria and no new error branches. This is larger than #3058's 99-line ownership repair because it covers initialized buffers and concurrent retained roots, still within all builder limits.

Overlap: #3390 edits publication bookkeeping in `publication.go`; this repair adds a local ownership hook without depending on its query-index changes. #3390 already waits for this repair.

## Design
Add a shared flag to each `textBuffer`. Successful capture freezes both intrinsic and derived buffers of each exposed assistant-text row, conservatively protecting both sides of provisional closure. Capture continues borrowing the same Content allocation without copying bytes.

`growText` must allocate fresh private storage before writing a shared buffer, even when capacity suffices. Preserve its capacity when detaching; grow geometrically only when required. Copy only the existing prefix, then append the escaped suffix and fixed tail as today. Clear sharing on the new allocation. Count allocation, copying and ownership-hook work in ingestion counters. Unpublished buffers remain reusable, so batching appends retains incremental ingestion and summary updates.

New enrichment buffers start private. Reusing a cached provisional buffer preserves its sharing state. Neither query-index maintenance nor public detachment changes.

## Concurrency model
The existing fold worker serializes Feed and capture. Only that worker accesses sharing flags. Published Item pointers and bytes remain immutable while query readers run outside the store lock. No production goroutines or locks are added. Test readers run a finite loop and are joined before their subtest ends.

## State transitions and identity reuse
| Event | Race-detector coverage |
| --- | --- |
| Capture after append-buffer initialization; repeat capture and append under the same item ID | `TestAffectedPublicationOwnership` |
| Shared storage has spare capacity; subsequent private appends reuse capacity and eventually grow | `TestTextBufferPublicationOwnership` |
| Child intrinsic text becomes provisionally closed, receives more text, then reopens and appends again | `TestAffectedPublicationTextLifecycle` |
| Retained query roots are read concurrently with later Feed/capture | `TestAffectedPublicationTextLifecycle`, `TestAffectedPublicationOwnership` |
| Public query/snapshot mutation after capture | `TestAffectedPublicationOwnership`, `TestAffectedPublicationTextLifecycle` |

## Error handling
No new errors or reject branches. Freeze only after rendering succeeds, before exposing pointers. Allocation failure retains Go's existing runtime behavior. Existing malformed-fact and partial-Feed contracts remain unchanged.

## Testing strategy
Write regressions first and confirm failure on the old implementation. Retain internal publications and query roots after initialization, compare every retained item against detached expected values, validate JSON, and check pointer borrowing. Exercise main and child content through provisional closure/reopening with finite concurrent readers. A focused buffer test proves capacity reuse/growth, repeated sharing and counted detachment work.

Keep `TestAffectedPublicationOracle` and existing goldens unchanged. Add explicit suffix-without-Content-replacement checks for pure appends. Run `go test -race ./internal/thread`, `go vet ./...`, and `go build -o /tmp/builder-3425/pyry ./cmd/pyry`. The verifier owns the full-module gate.

## Open questions
None.

## Documentation handoff
Pending documentation stage: update `docs/knowledge/features/thread-package-main-thread-folding.md`, **Main-work source entries** and **Scoped joins, pending outcomes and closure**, to explain that mutable private append buffers cannot overwrite any Content bytes already shared with an internal publication, including provisional child content. Describe the implemented ownership boundary while retaining the incremental text/summary guarantees.

## Security review
**Verdict:** PASS

**Findings:**
- [Trust boundaries] `publicationCapture.capture` freezes producer buffers before internal sharing. Recorded text remains inert validated JSON; no input acceptance changes.
- [Concurrency] Shared buffers detach before any suffix/tail write, including spare-capacity writes. Child-derived storage carries its own sharing flag through reuse; retained-root race coverage exercises both lanes.
- [Tokens / cryptography] No credentials, keys or nonce operations enter the changed text-storage path.
- [File operations / subprocesses / network and I/O] No production file, subprocess or transport operations change; `growText` only allocates memory. Capacity remains geometric, with full detachment work counted.
- [Errors / logs / telemetry] No new logging or error output; content remains confined to existing query/publication results.
- [Threat model] This repair enforces the existing internal immutable-publication boundary. Relay authorization and wire security remain unchanged; no new deferred security findings.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-15
