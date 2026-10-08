# History session and visibility metadata (#2964)

## Files read

- `internal/history/log.go` → `Entry`, `Append`, `LatestEntryID`, `LatestDisplayableEntryID`, `load`, `resolveDir`, `writeSegment`: opaque entries, shared cursor/cache, containment and failed-write recovery.
- `internal/history/segment.go` → `encodeEntry`, `decodeSegment`, `listSegments`: additive JSON fields under the existing version-1 header, bounded reads and torn-tail tolerance.
- `internal/history/log_test.go` → `walkAll`, `appendN`, recovery and containment tests: reusable paging fixtures and existing storage guarantees.
- `internal/history/latest_displayable_entry_test.go` → type, recovery, cache and failure tests: exact legacy fallback and independent raw recovery.
- `internal/history/segment_test.go` → `TestDecodeSegmentArms`: keep the version and incomplete-tail contract unchanged.
- `docs/knowledge/features/history-package.md` → Shape, directory resolution, failed-write recovery and unread watermark: preserve containment on every call and separate lazy filtered recovery from raw recovery.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → Storage, compatibility and dividers: metadata is a durable fact outside payloads; this slice adds no producers or transport filtering.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md` → storage conventions and assertions that distinguish omission from explicit values.

## Context

The thread fold needs session and visibility facts that legacy payloads cannot reliably supply. Add optional facts to the existing log without rewriting old entries or guessing sessions. ADR 042 already records the architectural decision; no new decision record is needed. Producer adoption is #2966 and legacy transport filtering is #2965.

Sizing: one storage contract, three acceptance criteria, approximately 500–600 written lines, two new exported types, no consumer updates, and fewer than ten new rejection branches. The estimate is consistent with #2954 plus metadata round-trip and exhaustion coverage. No other fetched feature branch touches the planned history files.

## Design

- Add `SessionProvenance` with `Kind` (`claude`, `codex`, or `none`) and optional `SessionID`. Known agents require a nonempty session ID; `none` requires an absent ID. Session identifiers remain opaque strings, never filesystem paths.
- Add `Metadata` holding optional `Session *SessionProvenance` and `Shown *bool`, and the same optional fields directly on `Entry`. JSON stores `session: {kind, session_id?}` and `shown` outside `payload`; nil fields are omitted. Absent session means unknown legacy provenance, while `{kind: "none"}` explicitly means no producing child. Visibility is independently absent, true or false.
- `AppendWithMetadata(convID, typ, payload, ts, metadata) (uint64, error)` validates provenance and uses the same locked storage path as `Append`. `Append` keeps its signature and supplies empty metadata. No payload decoding or metadata retained from caller pointers in the cache.
- Keep segment version 1: the existing codec already encodes/decodes additive fields through `Entry`. Old lines decode with nil metadata and remain byte-for-byte unchanged.
- One `displayableEntry` predicate applies explicit `Shown`, then `displayableType` for absence. Both append cache updates and cold filtered recovery use it. Hidden entries remain in raw queries and pages.
- Represent the single raw cursor as the last durable ID instead of next ID. Append refuses `lastID >= 2^53-1` before encoding, rolling or writing; otherwise it allocates `lastID+1`. Recovery stores the last decoded ID without arithmetic, including IDs at/above 2^53 and `MaxUint64`. `LatestEntryID` returns that same cursor, preserving recovered values without overflow or a second counter.

## Concurrency model

No new goroutines or handles. Both append paths and all cursor/cache updates remain under the existing leaf `Store.mu`; successful writes update watermarks, failed writes invalidate recovery as before. Callers must not mutate metadata while the append call is running, just as with payload bytes.

## Error handling

Add `ErrInvalidMetadata` for inconsistent provenance and `ErrIDExhausted` for an exhausted conversation. Invalid provenance is refused before directory creation. Errors contain neither session identifiers nor payloads. Exhaustion returns ID zero and leaves segment bytes and raw/displayable watermarks unchanged, including across reopening. Existing containment, line-size limits, unknown-version errors and partial-write cleanup/tolerance continue through the shared path.

## Testing strategy

Write regression tests first and observe compilation failure for the missing API. Cover all provenance states and all visibility states in persisted bytes and pages, using a literal legacy version-1 fixture; verify absence is omission rather than null and payloads stay opaque. Page across real segment boundaries before/after reopening and assert legacy bytes do not change.

Exercise shown overrides for all five legacy-excluded types and counted/unknown types with warm and reopened caches. Mixed logs include trailing explicit-hidden entries over multiple segments, uncached hidden appends and failure recovery; raw queries and pages retain every entry. Test both append paths near the ID bound, the final successful ID, repeated refusal with byte-for-byte file snapshots and stable watermarks, reopening, per-conversation independence, and recovered IDs at/above the limit including MaxUint64. Reuse existing tests for containment and torn writes and add metadata-path coverage where necessary.

Run `go test -race ./internal/history/...`, `go vet ./...`, and `go build -o /tmp/builder-2964/pyry ./cmd/pyry`. The verifier owns the full-module gate.

## Open questions

None. The representation and append contract above are the public API for this slice.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/history-package.md`, “Shape” and “Unread state uses a separate, lazily recovered watermark”, document the shipped append API and persisted metadata representation, the three provenance states, absent versus explicit visibility and its legacy type fallback, and the strict id bound with exhaustion refusal.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `AppendWithMetadata` shares `Append`'s authenticated-conversation precondition and validates metadata shape; neither session IDs nor payloads authorize or select a path. Producer selection remains #2966's responsibility.
- [Tokens, secrets, credentials] No credentials are introduced. Session IDs are persisted as provenance in existing private history files; no metadata is logged or included in errors.
- [File operations] Reuse `resolveDir` on every operation, private directory modes and `writeSegment`'s 0600/O_NOFOLLOW/O_EXCL discipline. Preserve existing best-effort rollback and torn-tail recovery. No metadata-derived paths or rewrites.
- [Subprocesses] No subprocess creation or environment handling is added.
- [Cryptography] No keys, randomness, secret comparisons or cryptographic operations are added.
- [Network and I/O] No network endpoints are added. Metadata participates in the existing encoded-line bound and bounded segment reads; ID refusal precedes all segment writes.
- [Errors, logs, telemetry] New sentinels report generic reasons without session IDs, types or payloads. No logging or metrics are introduced.
- [Concurrency] The one leaf mutex covers allocation, write and cache updates; decoded entries own their metadata and the cache retains only scalar watermarks. Existing partial-write recovery remains authoritative.
- [Threat model] Remote cursor validation and exact conversation containment remain unchanged. OUT OF SCOPE: producer session binding (#2966) and old-client filtering (#2965) are separate stages; no transport adoption is claimed here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-08
