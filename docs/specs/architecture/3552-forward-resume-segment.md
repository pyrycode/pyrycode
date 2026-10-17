# Position forward history readers at the resume segment

## Files read

- `internal/history/forward.go` → `Forward`, `Walk`, `forwardSegments`, `forwardSegment`: scalar progress, per-segment reads and tail registration.
- `internal/history/lookup.go` → `Lookup`, `segmentFirstID`, `lookupBound`: size-checked discovery and actual buffered I/O accounting.
- `internal/history/log.go` → `load`, `AppendWithMetadata`, `readSegment`, `resolveDir`: bounds maintenance, segment ceilings and containment.
- `internal/history/segment.go` → `listSegments`, `decodeSegment`: regular-file membership and torn-tail tolerance.
- `internal/history/forward_test.go`, `forward_storage_test.go`, `lookup_test.go`: retry, ordering, tail and selective-read fixtures.
- `docs/knowledge/features/history-package.md`, `history-package-shape.md`: never cache paths; separate delivery IDs from stored-order boundaries.
- `docs/knowledge/decisions/045-thread-index-over-history.md` → Decision: resume history independently of thread restoration.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: bounded files and controlled regressions.

## Context

Nonzero Forward resumes currently decode the entire prefix. Reuse Lookup's
ephemeral segment bounds without changing the format or consumers. One deliverable;
estimated 650 written lines including tests and plan, zero new exports or consumer
updates, four acceptance criteria and fewer than ten rejection categories. No
overlapping feature branches touch the planned production files. No new ADR needed.

## Design

Keep Forward construction free of I/O. On the first useful Walk of a reader created
with nonzero V, discover current nonempty segment ranges and choose the last first
ID <= V, or the earliest range when V precedes all entries. A gap uses its
predecessor. Beyond-end resumes retain the final range for future appends.

Extract Lookup's regular-file/size-checked bound refresh into a shared helper.
Selective walks refresh bounds for their remaining segments, validate increasing
nonzero first IDs, and carry the next nonempty first ID as an exclusive upper
bound for full reads. Initial positioning seeds boundaryID from the predecessor's
first ID, never from V. Subsequent walks retain the existing validated stored-order
boundary. Zero-start readers retain sequential reading and validation.

Discovery decodes at most the header and first entry, including actual buffered
read-ahead in readStats. Full selected reads validate all IDs, including filtered
entries, and reject overlap into the next discovered range before delivery. Old
unselected data beyond the first entry is deliberately not integrity checked.
Retain only scalar reader state, one decoded segment and a bounded delivery chunk;
per-call segment lists/bounds contain no decoded payloads.

## Concurrency model

No new goroutines. Store.mu protects discovery, bounds and read counters. Release
it before callbacks and between full reads. Reader methods remain serial. Tail
continues registering before catch-up and unregistering on every exit.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Resume at boundary, gap, before start or beyond end | `TestForwardResumeRanges` |
| Repeated Walk and failed callback retry | `TestForwardPositionedContinuity`, existing ordering/retry tests |
| Append before Tail and during read-to-wait | `TestForwardPositionedContinuity`, `TestForwardReplayToTail`, `TestForwardTailReadToWait` |
| Unknown/resized/removed ranges on repeated reads | `TestForwardResumeFreshness`, existing membership/read tests |
| Cancellation and rejected stored order across calls | `TestForwardStops`, `TestForwardOrderingAcrossCalls`, `TestForwardResumeRejects` |

## Error handling

Preserve ErrInvalidID, ErrNotContained, ErrUnknownVersion, ErrCorruptSegment and
filesystem errors. Empty/missing logs create nothing. Torn final lines remain
ignored. Discovery or validation failure never advances delivery progress.

## Testing strategy

Write range, corruption and real readStats regression tests first and observe red.
Use segments larger than read-ahead; measure construction plus Walk on an unprimed
reopened Store, then warm bounds separately. Assert exact bytes/opens and payload
metadata, not callback counts as I/O. Run admitted race tests for internal/history,
go vet ./... and go build ./cmd/pyry on Linux. The verifier owns the full hermetic gate.

## Open questions

None.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/history-package-shape.md`,
“Forward consumption” and “Lookup verification”, to describe exclusive resume
positioning, cold prefix discovery versus selected full reads, and actual read
accounting. Keep the unchanged segment format and retry/tail guarantees explicit;
distinguish consumed-range validation from prefix discovery.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `Forward` validates ID shape; authenticated conversation ownership remains its caller's precondition. `segmentFirstID` and full-read order checks validate file-derived selection bounds.
- Tokens, cryptography: no credentials, randomness or cryptographic operations enter this change.
- File operations: reuse `resolveDir` equality containment and current regular-file/size checks; cache no paths. Read-only operations preserve the existing daemon-owned-directory check/open window. No new writes or permission modes.
- Subprocesses, network: no subprocess or network operations. Prefix and full reads retain segmentCeiling limits, including buffered read accounting.
- Errors, logs, telemetry: preserve existing errors without payload contents; no new logging or telemetry.
- Concurrency: one Store mutex, no nested locks or new goroutines; callbacks remain outside it. Retry advances only successful chunks.
- Threat model: scope is authenticated local history reads. No relay/mobile protocol or authorization change; unselected ranges intentionally receive prefix validation only.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17

## Revisions

- 2026-10-17: `load` can remember a zero first ID from an invalid complete
  entry, so size equality alone cannot certify an empty range. The shared bound
  helper rediscovers zero bounds larger than the header; `TestForwardResumeRejects`
  covers recovery followed by positioning. This preserves torn-first-entry tolerance.
- 2026-10-17, verifier finding 1: `lookupBound` must distinguish discovery-certified
  empty ranges from recovery-derived zero first IDs. Reuse a discovery certificate
  while regular-file membership and size remain valid; recovery-derived zero bounds
  still require discovery. `TestForwardResumeTornFirstEntryReads` proves cold/warm
  byte and open counts for Forward and Lookup, plus rejection when resizing completes
  a zero-ID entry. The certificate stays under `Store.mu`; no new goroutines or
  retained decoded data. The security review's containment and validation contracts
  remain unchanged.
