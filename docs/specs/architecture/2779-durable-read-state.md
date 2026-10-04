# #2779 — Durable conversation read state and newest history id

## Files read

- `internal/conversations/conversation.go`, `registry.go` → `Conversation`, `Registry.Update`, `Save`, `Load`: additive scalar persistence; updates mutate existing rows.
- `internal/history/log.go`, `log_test.go` → `Store.load`, `Append`, `resolveDir`, `readSegment`: one append cursor, bounded tail recovery and per-call containment.
- `internal/relay/handlers/list_conversations.go` → `ListConversationsWithAgents`, `AgentOf`: stable ordering and capability filtering before projection.
- `internal/relay/handlers/{rename_conversation,promote_conversation,archive_conversation,change_workspace,set_system_prompt,set_conversation_muted,autoname}.go` → update producers: project the stored row's mark.
- `cmd/pyry/channel.go`, `relay.go` → channel announcement and `startRelayV2`: eighth update producer and history dependency wiring.
- `internal/protocol/conversations_{read,write}.go` and their tests/fixtures → summary and update serialization contracts.
- `internal/relay/v2session_history_request.go` → existing fixed unavailable-history message and retryability.
- `docs/knowledge/features/conversations-package.md`: storage versus wire `omitempty` distinction; scalar copying preserves metadata.
- `docs/knowledge/features/history-package.md` §§ Directory resolution, A cleanup on a failed write, Concurrency: cache the cursor, never the path; recovery must tolerate failed-write residue.
- `docs/knowledge/features/protocol-package.md`, `relay-package.md`, `relay-package-handlers.md`: wire types and dispatch boundaries.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md` §§ Establish the change surface, Protocol boundaries: count test consumers and inspect raw zero-value keys.
- `docs/protocol-mobile.md` § Security model: authenticated encrypted transport remains the disclosure boundary.

## Context

Clients need one host/operator read mark expressed in durable per-conversation history ids. This slice persists and reports that mark plus the newest durable entry; #2780 owns the write request. No registry setter or write verb is added. No decision record is needed beyond this contract.

Sizing before implementation and rechecked against this plan: one usable read contract, approximately 550–650 written lines including tests/plan, zero new exported types/interfaces, four acceptance criteria, fewer than ten reject branches. The two list constructors have 20 existing external calls (19 tests and one daemon call), plus their internal delegation. This exceeds ten simultaneous consumer updates. Per `builder/handbacks.md`'s floor rule, splitting the history dependency plumbing from its sole list consumer would create an unusable one-consumer slice, so keep it together and record the raw overage. No fetched feature branch overlaps planned production files.

## Design

Add `Conversation.ReadUpTo uint64` with zero-default JSON persistence. Add always-present `ReadUpTo` to `ConversationSummary` and `ConversationUpdatedPayload`, and always-present `LatestEntryID uint64` to summaries. Every update producer copies `ReadUpTo` from the same stored conversation supplying its other fields.

Add `Store.LatestEntryID(convID) (uint64, error)`: validate canonical id before filesystem access; acquire the existing store mutex; re-resolve the directory without creating it; missing log returns zero; otherwise reuse `load` and return `nextID - 1`. No second counter or full-log scan. Existing recovery walks backward only until an entry is found, tolerates header-only/empty/incomplete segments and ignores torn final entries. Appends retain their existing numbering and recovery.

Pass the daemon's `*history.Store` explicitly to both list constructors. Nil dependency yields a correlated retryable unavailable-history error even for an empty registry. Look up newest ids only after agent filtering; any returned row's lookup error sends the same fixed error before any list reply. Build the complete response before sending it. Ordering and agent behavior stay as implemented.

## Concurrency model

No goroutines or persistent handles added. History reads and appends share `Store.mu`; registry snapshots and updates use the existing registry lock. Registry and history locks are not nested. A list observes each store at its read time; atomic snapshots across stores are not required.

## Error handling

Storage returns existing wrapped errors. The list handler maps all history lookup failures and nil dependency to `history.unavailable`, `retryable: true`, with the existing fixed history-unavailable message and request correlation. Paths and underlying errors never reach the reply. No partially built list is sent. Failed appends retain existing cursor invalidation.

## Testing strategy

Write failing tests before implementation. Registry tests cover absent-key legacy rows, zero/default and large uint64 round trips, and metadata mutation preservation. Protocol tests inspect zero and distinct nonzero ids on both record types; update existing fixtures with the new required keys. Rename coverage proves the stored nonzero mark survives mutation and reaches its reply.

History tests cover missing/empty logs, repeated cursor reads without segment opens, restart/tail recovery across empty/header-only/incomplete newest segments and torn entries, continued numbering, failed append behavior, invalid ids and containment rechecked after cursor initialization. Use existing read counters and append helpers to prove bounded recovery.

List tests use real history stores: durable mark/latest id across reopen and append, zeros, nil dependency, later-row corruption without partial reply, and corruption on a filtered-out agent row. Existing tests continue to pin ordering and filtering with an explicit empty history store.

Run race tests on `internal/conversations`, `internal/history`, `internal/protocol`, `internal/relay/handlers`, and `cmd/pyry`; run `go vet ./...` and build `./cmd/pyry`. The dispatcher owns the full-module gate.

## Open questions

None. Both constructors require the explicit history dependency; missing wiring fails closed.

## Documentation handoff

Pending for the documentation stage in `docs/protocol-mobile.md`:
- Update `conversations` and `conversation_updated` rows under **Application message types** and the id explanation under **Conversation history (v2)**.
- Document always-present unsigned 64-bit ids, zero defaults, `latest_entry_id` on list rows, `read_up_to` on both records, and unread as `latest_entry_id > read_up_to`.
- State that the mark is per conversation/operator on this host and these fields ship before the write request.
- Document the correlated retryable list failure and add a **Changelog** entry.
- Preserve the distinction between durable history entry ids, envelope ids and replay event ids.

## Security review

**Verdict:** PASS

**Findings:**
- [Trust boundaries] `LatestEntryID` validates ids before touching disk. List ids come from registry rows; agent filtering precedes history lookup. Read marks grant no authority and have no network writer in this slice.
- [Tokens/secrets] No credential lifecycle changes; only numeric metadata is added to existing encrypted records.
- [File operations] Reuse `resolveDir` on every lookup, including a cached cursor, and existing regular-segment filtering in tail recovery. Registry Save retains atomic rename and existing permissions. The existing within-call filesystem race remains under the state-directory threat model; no cached path widens it.
- [Subprocesses/cryptography] No subprocess, nonce, key or cipher changes.
- [Network/I/O] Existing authenticated v2 dispatch and envelope bounds remain in force; reads reuse bounded segment decoding and stop at the first nonempty tail segment.
- [Errors/logs] MUST-FIX prevention in the design: map nil/lookup failures to a fixed path-free message rather than serializing an underlying filesystem error. No payloads or new operational logs are added.
- [Concurrency] Existing store mutex spans cursor recovery/read. Registry snapshot releases its lock before history calls. No new goroutines; existing failed-write cursor invalidation remains authoritative.
- [Threat model] Numeric host/operator metadata is disclosed only through existing paired encrypted conversation records. Write authorization is deferred to #2780, which introduces the write verb.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-04
