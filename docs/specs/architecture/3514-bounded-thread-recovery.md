# Bounded thread recovery

## Files read

- `cmd/pyry/daemon_thread_relay.go`: `publish`, `gap`, and query installation own live continuity and routing.
- `cmd/pyry/thread_shadow.go`: `follow` owns the conversation cursor and idle retirement.
- `cmd/pyry/daemon_thread_demand.go`: `queryThread` owns cancelable query demand through rendering.
- `cmd/pyry/daemon_thread_relay_test.go`: publication, retirement and idle-commit fixtures.
- `cmd/pyry/daemon_thread_metadata_test.go`: status interleavings and allocation fast path.
- `cmd/pyry/daemon_thread_queries_test.go`: authenticated replies, final markers, paging and live/query joins.
- `cmd/pyry/daemon_thread_relay_rework_test.go`: agent-ineligible recipient preservation.
- `internal/thread/observations_store.go`: `Changes` supplies consistent metadata and requires retained boundaries.
- `internal/relay/v2session_thread_delivery.go`: ordered query delivery and cancellation.
- `internal/relay/v2session_livestate.go`: retirement quarantines a connection before dependent output.
- `docs/knowledge/features/thread-package.md`, Shadow lifecycle and evidence: status is a hint; same epoch does not prove continuity; preserve consumed boundary before held delivery.
- `docs/knowledge/features/protocol-package-thread-messages.md`: successful separate final marker alone certifies recovery.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md`: focused race checks and behavioral evidence.

## Context

Initial and replacement observations currently broadcast every historical row. Acquire a live publisher boundary without rendering old items; clients recover using existing explicit bounded queries. This is one behavioral repair, independent of durable indexing and Desktop batching. No decision record is needed. Fetched feature branches have no overlap with the proposed existing files.

Sizing: five criteria, no exported types/interfaces, no public consumer migration, fewer than ten rejection branches; approximately 750 written lines including this plan, production and fixture repairs/new regressions. Recount before plan commit remains within 800 lines.

## Design

Use `Changes` with an empty epoch to obtain consistent usable publication metadata without copying items. Initial/replacement acquisition advances only the publisher's boundary and emits no historical inventory. Subsequent retained changes use the existing encoding and fanout. Preserve the unchanged status fast path.

On continuity loss, retire both recorded recipients and currently eligible thread connections for the affected conversation, including clients initialized solely by queries. Preserve interactive, thread and agent eligibility. Capture replacement metadata after retirement and never reinterpret it as client-certified progress.

Before capturing a query result, ensure the same conversation coordinator has acquired/published its boundary. Serialize publication with a per-conversation mutex, so a first query cannot complete before a later initial acquisition silently skips intervening commits. Queries enroll a coordinator even for an already-loaded store. Existing relay query gates continue to reject live races and suppress already covered deltas. No wire changes.

## Concurrency model

Existing discovery/follow and relay query workers remain cancelable and joined. A conversation publication mutex serializes query preparation and following; never take it while holding shadow/quiet mutexes. Query demand prevents idle unload through preparation/rendering. Different conversations have independent locks. Publisher shutdown still cancels and joins publication calls before detaching.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Initial populated acquisition and subsequent append | `TestDaemonThreadBoundedRecovery`, `TestDaemonThreadPublication` |
| Idle reload under reused epoch with append during recovery | `TestDaemonThreadBoundedRecovery`, `TestDaemonThreadIdleCommit` |
| Retained range loss and epoch replacement | `TestDaemonThreadBoundedRecovery`, `TestDaemonThreadStatusInterleaving` |
| Status advance/withdrawal between reads | `TestDaemonThreadStatusInterleaving` |
| Query capture races live update; retry then text continuation | `TestDaemonThreadQueriesJoin` |
| Query initializes before first coordinator publication | `TestDaemonThreadQueryBoundary` |
| Connection replacement and scoped gap quarantine | `TestDaemonThreadGapPreservesAgentIneligiblePeer`, `TestDaemonThreadBoundedRecovery` |
| Removal/shutdown during held fanout | `TestDaemonThreadRetirement` |

## Error handling

Nonusable captures leave no acquired boundary. Changes errors/cancellation cannot advance it. Gaps quarantine eligible consumers before replacement. Encoding/push failures retain existing recovery semantics. Query cancellation/error/reset/disconnect never certify a partial result; only the existing successful final marker establishes a client checkpoint.

## Testing strategy

Write failing bounded-baseline regressions before implementation. Count frames and encoded bytes at 128 and 8333 settled rows, with one append across reload and another afterward. Recover via authenticated zero-version catch-up and older paging, compare rows/revisions and metadata with the store, and drain with a FIFO barrier only after final marker and publisher completion. Reuse existing query/live join proof and authorization/legacy/retirement fixtures, updating their initial acquisition to explicit query recovery. Use barriers for recovery commits and query capture; no quiet-time success assertions.

Run focused daemon race regressions with executed/passed/failed/skipped counts, `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build ./cmd/pyry` on Linux. The verifier owns the full hermetic gate; no live Claude proof is required.

## Open questions

None.

## Documentation handoff

- Pending documentation stage: `docs/knowledge/features/thread-package.md`, “Shadow lifecycle and evidence”: replace the initial/replacement full-addition baseline description with the implemented boundary, affected-consumer reconciliation and explicit bounded client recovery.
- Pending documentation stage: `docs/protocol-mobile.md`, “Thread query messages” / “Query/live ordering” and “Reconnect / Backfill semantics”: state that initial acquisition and continuity repair do not broadcast historical inventories; preserve explicit per-conversation requests, reset/retry, final-marker certification and older paging.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: MUST FIX addressed in design: query-only consumers must also be quarantined on a gap; enumerate current conversation-eligible peers rather than relying only on successful pushes. Existing relay authentication, registry membership, interactive and multi-agent gates remain authoritative.
- Tokens/secrets/cryptography: no new credential, key, nonce or comparison; existing Noise transport is unchanged.
- Files/subprocesses: no new file path or process operation. Recovery reuses authorized store queries and their existing containment checks.
- Network/I/O: initial/replacement output has zero historical item frames. Existing bounded query selection, envelope fragmentation and ordered delivery remain unchanged; required active rows/parents are not an absolute byte cap.
- Errors/logs: retain generic wire errors and existing event-only logs; never log history content.
- Concurrency: MUST FIX addressed in design: acquire the publisher boundary before query capture under conversation serialization, preventing a completed query followed by silent cursor advancement. Existing cancellation/join paths remain required and tested.
- Threat model: untrusted history remains inert content behind the existing authenticated encrypted relay boundary; no new execution/rendering or authorization surface. No security work is deferred.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-16
