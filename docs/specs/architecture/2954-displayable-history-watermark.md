# #2954 — Displayable history watermark for unread state

## Files read

- `internal/history/log.go` → `convLog`, `Append`, `load`, `LatestEntryID`, `Page`, `readSegment`, `resolveDir`: cursor recovery, successful-write placement, and per-call containment.
- `internal/history/segment.go` → `listSegments`, `decodeSegment`: backwards segment enumeration, bounded decoding, opaque payloads, and torn-tail tolerance.
- `internal/history/latest_entry_test.go`, `log_test.go` → raw cursor recovery assertions and small-segment fixtures.
- `internal/relay/handlers/list_conversations.go` → `historyLatestReader`, `ListConversationsWithAgents`: required dependency, typed-nil normalization, filtering before lookup, all-or-error replies.
- `internal/relay/handlers/mark_conversation_read.go`, `mark_conversation_read_test.go` → `MarkConversationRead`: registry lookup before storage, monotonic persistence, correlated replies and advance-only pushes.
- `internal/relay/handlers/list_read_state_test.go` → `TestListConversationsDurableReadState`: real-store list and persistence fixtures.
- `cmd/pyry/list_conversations_agent_test.go` → `emptyAgentListHistory`: mechanical interface update only.
- `internal/protocol/conversations_read.go` → `ConversationSummary.LatestEntryID`: wire meaning comment.
- `docs/knowledge/INDEX.md`, `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: scope, tests that distinguish behavior, and source-search limitations.
- `docs/knowledge/features/history-package.md` → directory-resolution and failed-write recovery lessons: never cache a resolved path; failed append invalidates recovered state.
- `docs/knowledge/features/relay-package.md`, `relay-package-handlers.md`, `protocol-package.md`, `protocol-package-types-conversations-read-payloads.md`: handler and DTO contracts, explicit zero fields and typed-nil failures.
- `docs/protocol-mobile.md` → Security model: authenticated paired access and existing transport threat boundaries.

## Context

Trailing durable status entries keep the raw newest ID above the ID of the last displayed reply. Unread state therefore survives reading that reply. This change supplies one filtered watermark to both handlers. Raw storage, durable identity, and history pages remain unchanged. No decision record is needed.

One deliverable: correct unread state. Estimated total written work is 400–500 lines, zero new exported types/interfaces, two production reads plus three test-double methods updated, four acceptance criteria, and fewer than ten failure branches. No other fetched feature branch touches the four production files.

## Design

Add `Store.LatestDisplayableEntryID(convID) (uint64, error)`. Return the newest stored ID except types `turn_state`, `stall`, `api_retry`, `compacting`, and `session_transition`. Every other type, including unknown and empty types, counts. Missing, empty and status-only logs return zero. Inspect only entry metadata; payload JSON remains opaque.

Keep the filtered ID and an initialized flag on `convLog`, separately from the raw append cursor. After `load`, the first filtered lookup walks segments newest-first through `listSegments` and `readSegment`, stopping at the first qualifying entry. Cache only a successful result, including zero. Subsequent reads open no segments. A successful displayable append updates and initializes this cache; a status append leaves it unchanged. Cursor recovery after a failed append invalidates the filtered cache. This lazy recovery preserves raw `LatestEntryID`'s existing tail traversal and errors even when older segments behind trailing statuses are corrupt.

Both queries validate IDs, handle a nil store, and re-resolve containment on every call before using cached state. The filtered query creates no directories. Change `historyLatestReader` to require the filtered method; update the two handler reads and three test doubles. Change the DTO comment and mark-read contract comment. No factory signature, request schema, stored entry, or ID allocation changes.

## Concurrency model

Use the existing `Store.mu` for cursor recovery, segment traversal, cache reads, and append updates. No goroutines or additional locks. The first filtered lookup can traverse all segments of a status-only log; memory remains bounded by one segment and subsequent lookups use the cache.

## Error handling

Use existing ID, containment, segment-version, corruption and I/O errors. Do not cache partial results on failure. Reuse existing decoder tolerance for empty/incomplete segments and torn final lines. Keep handler validation, static wire errors, registry rollback, and advance-only announcements unchanged.

## Testing strategy

Write regressions first and observe failure before implementing. Store cases cover each excluded type and representative included/unknown types, missing/empty/status-only logs, separate conversations, warm cached lookups, reopening with statuses spanning segments, incomplete tails, failed append recovery, ID validation and changed symlinks. Assert raw newest IDs, complete pages and subsequent allocated IDs still include statuses.

Real-store handler round trips exercise marking exactly the last displayable ID and overshooting it, then listing as read despite all five trailing statuses, persisting/reloading, and appending new displayable content to restore unread. Include status-only zero and pre-existing higher held marks with no push. Existing handler assertions cover correlation, validation, errors, persistence rollback and advance-only pushes.

Run race tests for `internal/history`, `internal/relay/handlers`, `internal/protocol`, and `cmd/pyry`, then `go vet ./...` and `go build ./cmd/pyry` with output outside the worktree. The dispatcher/verifier owns `make check` and the full-module race suite.

## Open questions

None. The exclusion set and zero behavior are fully specified.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md`, **Application message types** (`conversations` row), **Marking a conversation read**, and **A history entry**: define `latest_entry_id` and the mark-read clamp as the newest entry excluding the five named status types, or `0` when none exists; retain the unread comparison and monotonic clamp formula. History still contains status entries and uses the same durable ID space.
- `docs/knowledge/features/history-package.md`, **`LatestEntryID` shares `Append`'s cursor instead of a second counter**: distinguish the raw query from `LatestDisplayableEntryID` used for unread state, with the exclusion set, lazy backward recovery, cache updates and invalidation after failed writes.
- `docs/knowledge/features/protocol-package-types-conversations-read-payloads.md`, **`ReadUpTo` and `LatestEntryID`** bullet, and `docs/knowledge/features/relay-package-handlers.md`, **`MarkConversationRead`** and **`ListConversations` gains a third, required parameter** sections: point both handlers at the filtered watermark and describe its zero/status-only behavior.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `MarkConversationRead` still resolves an exact registry key before querying history; `ListConversationsWithAgents` reads registry-owned IDs after agent filtering. The new store method validates canonical ID shape before path construction. Unknown stored types count rather than silently hiding future content.
- [Tokens, secrets, credentials] No credential access or lifecycle changes. Payloads remain `json.RawMessage`; only stored type and ID select the watermark.
- [File operations] The new method calls `resolveDir(convID, false)` every time, including warm-cache calls. It creates no files, uses `listSegments`' regular-file filtering and `readSegment`'s size bound, and retains the existing within-call containment window under the private daemon directory. Existing append modes and registry atomic persistence remain unchanged.
- [Subprocesses] No subprocess invocation or environment propagation is introduced.
- [Cryptography] No cryptographic primitives, keys, nonces or comparisons change; replies retain the authenticated transport.
- [Network and I/O] Clients supply no new cursor or size value. Recovery reads at most one bounded segment at a time and caches the first successful scan, including status-only zero, preventing repeated full scans on list requests.
- [Errors, logs, telemetry] No new log calls or payload-bearing errors. Store sentinels reach existing static `history.unavailable` replies; existing handler logging and correlation remain unchanged.
- [Concurrency] The filtered state is accessed only under `Store.mu`; updates occur only after successful writes. Failed writes force recovery and invalidate the filtered cache. No goroutine or lock-order change.
- [Threat model] This change only adjusts unread metadata for already-authorized paired clients. It adds no prompt delivery, relay routing, filesystem disclosure or replay capability; the existing Noise/authentication boundaries remain responsible for the protocol's documented threats.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-08
