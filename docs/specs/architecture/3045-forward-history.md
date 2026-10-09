# Forward committed history consumption

## Files read
- `internal/history/log.go` → `Store`, `AppendWithMetadata`, `LatestEntryID`, `readSegment`, `resolveDir`: shared cursor, serialization, bounded reads and containment.
- `internal/history/segment.go` → `listSegments`, `decodeSegment`: ascending regular-file index and complete-entry recovery.
- `internal/history/log_test.go`, `segment_test.go`: segmented fixtures, read counters and decoder failure contracts.
- `docs/knowledge/features/history-package.md`, `history-package-shape.md`: opaque metadata, authenticated-conversation precondition and re-resolution on every read.
- `docs/knowledge/features/history-package-failed-write-recovery.md`, `history-package-watermarks.md`: tolerate write residue without hiding complete corruption; share existing high-water cursor.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → Storage, Migration order: forward refold prerequisite; reduction belongs to #3048.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: bounded concurrency and synchronized behavioral assertions.

## Context
One deliverable: chronological raw history replay continuing into same-Store tailing. Storage bytes, durability and existing append/page signatures remain unchanged. No new decision record is needed. Other pushed feature branches (#2873, #2882) do not touch history.
Sizing: approximately 230 production + 430 test/helper + 60 plan lines, one exported type, zero consumer migrations, four acceptance criteria and fewer than ten distinct rejection categories. Fits the 800-line ceiling both at sketch and plan review.

## Design
`Store.Forward(convID, afterID) (*ForwardReader, error)` validates the authenticated conversation and constructs a single-caller reader with exclusive resume (zero means beginning). The caller captures H using existing `LatestEntryID`.
`ForwardReader.Walk(ctx, throughID, consume func([]Entry) error) error` delivers IDs in (last consumed, H], increasing across segments; nil return explicitly completes even an empty range. Callback success advances the consumed ID; callback error leaves that chunk retryable. `LastEntryID()` exposes successful progress. Callers keep the same reader for later walks/tailing.
`ForwardReader.Tail(ctx, consume) error` registers a buffered wakeup before catch-up, walks all currently committed entries, then waits. Notifications coalesce; only the log supplies entries. Only successful appends signal, after updating the shared append cursor. Tail unregisters on all exits.
Each walk lists segments, starts at the remembered segment, and reads one bounded segment at a time through existing helpers. Completed earlier segments are never reopened by a positioned reader. Delivered chunks contain at most `MaxPageEntries`; only one decoded segment and one chunk are held, and none remain on the reader when a call returns. Validate increasing nonzero stored IDs rather than silently skipping damaged ordering. Preserve every raw entry field.

## Concurrency model
No package goroutines. The caller executes Walk/Tail synchronously and cancels via context. Store.mu protects reads and append/wakeup registries; release it after listing and each segment read, before callbacks and waits. Notifications use nonblocking sends into capacity-one channels. Check cancellation before every read, callback and wait. ForwardReader methods require serialized caller use; Store methods remain concurrent safe.

## State transitions and identity reuse
| Event | Race-detector coverage |
| --- | --- |
| Snapshot, append beyond H, repeat walk, exclusive resume, reopen | `TestForwardSnapshot` |
| Replay pause, append in same/other conversation, register tail after replay | `TestForwardReplayToTail` |
| Registered catch-up, coalesced appends, append at read-to-wait boundary | `TestForwardTailContinuity` |
| Missing log becomes populated, multiple readers, cancellation and unregister | `TestForwardTailContinuity` |
| Callback failure then retry; cancellation between segments | `TestForwardStops` |
| Duplicate/decreasing segment boundaries across walks, replay-to-tail and notified catch-up; resume and retry within the current segment | `TestForwardOrderingAcrossCalls`, `TestForwardOrderingResumeAndRetry` |

## Error handling
Return validation, containment, listing/open/read, oversized/corrupt segment, unknown-version and callback errors explicitly. Re-resolve before each read and recheck that its listed leaf is regular. Reuse trailing-write tolerance; no fsync guarantee is added. Cancellation returns ctx.Err and drops owned buffers/registrations. External writers and deletion while consuming are unsupported; read disappearance is an error, not successful completion.

## Testing strategy
Write focused tests first and observe missing API failure. Cover chronological raw metadata including hidden/unknown entries; H bounds, zero/empty/missing/reopened logs; bounded chunks/read work; synchronized multi-segment pauses; replay-to-tail and registration/wait races; failed append silence and subscriber cleanup; ordering/corruption/version/size/read/containment failures and torn-tail tolerance. Run `go test -race ./internal/history/...`, `go vet ./...`, `go build ./cmd/pyry`. Full-module race tests belong to verifier.

## Open questions
Resolved: use callbacks with nil completion and synchronous context cancellation, avoiding channel ownership and retained page buffers. Tail notifications are only for the same Store; external writers stay outside scope.

## Documentation handoff
- Pending documentation stage: `docs/knowledge/features/history-package-shape.md`, add “Forward consumption” documenting snapshot bounds, exclusive resume, replay-to-tail continuity, same-Store notification scope, cancellation and read errors; retain the existing durability qualification.
- Pending documentation stage: `docs/knowledge/features/history-package.md`, “Concurrency”, describe the bounded lock scope and state that consumer processing or waiting cannot hold the store mutex or delay append completion.

## Security review
**Verdict:** PASS
**Findings:**
- [Trust boundaries] Forward validates with `conversations.ValidID`; the authenticated-conversation precondition remains required. Stored payloads stay opaque; increasing ID validation refuses damaged ordering.
- [Trust boundaries, verifier finding 1] MUST FIX addressed: retain the last validated stored ID preceding the next segment read across calls. This boundary includes a skipped sealed segment and excludes the segment being reread; it is independent of the caller's exclusive resume ID. Duplicate/decreasing boundaries fail before delivery across walks and tail catch-ups, without reopening completed segments.
- [Tokens/secrets] No credentials created or retained. Consumer payloads are conversation content; no new logging or payload-bearing errors.
- [File operations] `resolveDir` runs before listing and each segment read; `listSegments` filters regular leaves and the read rechecks regularity. Existing private-directory check-then-use threat boundary remains; no files or modes change.
- [Subprocesses/cryptography] No subprocess or cryptographic operations introduced.
- [Network/I/O] No sockets; reuse `segmentCeiling` and cap deliveries at MaxPageEntries. Cancellation between bounded reads prevents whole-log work after stop.
- [Errors/telemetry] Preserve internal sentinels and path-bearing storage errors; future transport adapters must classify errors rather than forwarding their text. No telemetry added.
- [Concurrency] One lock, no nested locks or new goroutines. Register before catch-up and defer removal; nonblocking notifications prevent consumers from stalling append.
- [Threat model] No new relay endpoint or permission capability. Future #3048 consumer owns daemon integration; transport authorization remains in existing authenticated handlers.
**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-09

## Revisions
- 2026-10-09: review made sealed-segment progress explicit, so even a full final segment is not reopened; disappearance after positioning returns a read error. `TestForwardBoundedChunksAndPosition` covers both; `TestForwardTailReadToWait` pins the append after the final read and before waiting.
- 2026-10-09, verifier finding 1: `Walk` reset stored-ID validation after skipping sealed segments across calls. Retain a scalar validation boundary separately from delivery progress: before the current segment while it remains retryable, and through it once sealed and consumed. `TestForwardOrderingAcrossCalls` covers duplicate/decreasing IDs across separate walks, replay-to-tail and notified catch-up; `TestForwardOrderingResumeAndRetry` covers arbitrary resume IDs, callback failure and rereading the current segment. Existing completed-segment read bounds remain unchanged.
