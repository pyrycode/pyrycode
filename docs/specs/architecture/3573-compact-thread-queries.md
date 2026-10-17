# #3573 Admit and answer compact thread queries

## Files read

- `internal/relay/v2session_handshake.go` → `negotiateCapabilities` (variadic readiness; expansion is `readiness[2]` gated on thread readiness and advertised thread+interactive), the `negotiated` call in `handleNoiseInit`, and the flag set beside `s.threadExpansion`.
- `internal/relay/v2session.go` → `V2Session.threadExpansion`: home of the new `threadCompact` flag.
- `internal/relay/v2session_seams.go` → `V2SessionConfig.ThreadCatchUp`/`ThreadPage`/`ThreadToolBody`/`ThreadChildren`: the optional-func seam pattern the compact providers follow.
- `internal/relay/v2session_catchup.go` → `admitThreadQuery` (gate order, join bound under `hostedMu`), `runThreadQuery` (reply shape, coverage selection, reset), `threadQueryGate` (join recording at final, live withholding), `cancelThreadQuery`, `threadJoin`, `threadQuery`.
- `internal/relay/v2session_children.go` → `runThreadChildren`: drops the four `ChildSummary` header facts today.
- `internal/protocol/thread_query.go` → `ThreadCatchUpPayload.Projection`, `ThreadPagePayload.Projection`/`Epoch`, `ThreadItemsPayload.Projection`/`Headers`, `ThreadResetPayload.Projection`.
- `internal/protocol/thread_query_encoding.go` → `EncodeThreadReply`: already batches `Headers`, repeats projection scope, rejects headers without projection and scoped resets without epoch.
- `internal/protocol/thread_expansion.go` → `ThreadSummary` with `ErrorSummary`, `PublicChildCount`, `HasPublicChildren`, `SubtreeVersion`.
- `internal/thread/compact_queries.go` → `CompactQueryResult`, `CompactNewestWindow`, `CompactHistoryPage` (takes no epoch; caller must compare); `compact_intervals.go` → `CompactCatchUp` (version zero is the newest window; nonzero may return `ResetRequired`).
- `internal/thread/child_pages.go` → `ChildSummary`, `ChildPageResult`.
- Tests: `testCatchUpPeer`, `testCatchUpError`, `testThreadQueryRequestedScope` (`v2session_catchup_test.go`), `testPagePeer` (`v2session_page_test.go`), `testChildrenPeer`, `testChildrenResult`, `TestThreadExpansionHandshake` (`v2session_children_test.go`), `testBodyPayload` (`v2session_tool_body_test.go`), `DeliverThread` live-gate pattern (`v2session_thread_delivery_test.go`).
- `docs/protocol-mobile.md` § "Compact projection (declared)": compact page requires nonempty epoch; selection is per connection and conversation; unknown projection is refused.

Overlap: `origin/feature/3240` touches `v2session.go`/`v2session_handshake.go` and `origin/feature/3296` touches `v2session_catchup_test.go`; edits here stay additive.

## Context

#3559 declared the compact wire contract; #3568/#3569 added Store reads; #3560 added header facts. The relay neither negotiates `thread_compact` nor honours `projection`. This slice adds the query side only. Compact live delivery is #3574; daemon installation is #3563, so production keeps complete delivery until then. No decision record needed.

## Design

**Seams** (`V2SessionConfig`, optional, nil keeps compact withheld):

- `ThreadCompactCatchUp func(context.Context, protocol.ThreadCatchUpPayload) (thread.CompactQueryResult, error)`: version zero is the newest window, nonzero is catch-up (maps onto `Store.CompactCatchUp`).
- `ThreadCompactPage func(context.Context, protocol.ThreadPagePayload) (thread.CompactQueryResult, error)`: maps onto `Store.CompactHistoryPage`.
- `ThreadCompactReady func() bool`: nonblocking attestation that compact query and live publication are installed.

**Negotiation.** `negotiateCapabilities` takes a fourth readiness value; it appends `thread_compact` only when advertised and `thread_expansion` was granted in this call (which already implies thread readiness and advertised thread+interactive) and `readiness[3]` is true. The handshake passes `ThreadCompactCatchUp != nil && ThreadCompactPage != nil && ThreadCompactReady != nil && ThreadCompactReady()`. `s.threadCompact` is set from the negotiated slice beside `s.threadExpansion`; rekey never touches it.

**Admission** (`admitThreadQuery`, catch-up and page only; body/children ignore `projection`):

1. Existing parsing and the existing gate line run unchanged (authentication state, conversation validity, agent/liveWithheld, thread, interactive, hosted authorization, provider presence).
2. Then, if the `projection` key is present: it must decode to exactly `"compact-v1"` (null, non-string, empty and any other string refuse); the session must hold `threadCompact` and both compact providers must be non-nil; a page must carry a nonempty string `epoch`.
3. Under `hostedMu`, beside the join-bound check: a non-initializing request (nonzero catch-up or any page) whose conversation has a recorded join with a different projection refuses. Version-zero catch-up is always admitted (re-initialization). With no recorded join the request goes to the provider.

Every refusal is the existing `threadQueryError` before any provider call and before touching joins.

**threadQuery/threadJoin.** `threadQuery` gains `projection string`; the request payloads carry `Projection` (and page `Epoch`) to the provider. `threadJoin` gains `projection string`.

**Reply** (`runThreadQuery`, compact branch): call the compact provider; refuse on error, `Err`, non-usable state, cancelled context, projection not `compact-v1`, or conversation mismatch. `ResetRequired`, or a page result whose epoch differs from the requested epoch, becomes `ThreadResetPayload{Projection: compact, ...}` (catch-up reset keeps today's behaviour for complete; complete pages still refuse on reset). Otherwise build `ThreadItemsPayload{Projection: compact, Items, Headers, Final: true}` with the same coverage selection as complete (nonzero catch-up → `FromVersion`; window/page → four bounds). Items use the existing item conversion; headers use a shared `threadSummaryFromChild(thread.ChildSummary) protocol.ThreadSummary` that copies all fields including the four facts. `runThreadChildren` uses the same helper.

**Joins and live gate** (`threadQueryGate`): a successful final records `threadJoin{epoch, version, p.Projection}`. Unsolicited complete item updates for a conversation whose join projection is compact return `ErrThreadWithheld` before sealing. A complete live update cancels only a complete in-flight query of its conversation (`threadQueryGate` and `cancelThreadQuery`); it cannot regress a compact view it is not part of, and cancelling would make compact queries on an active conversation fail repeatedly. Body and children finals keep returning before join recording.

## Concurrency model

No new goroutines. Join reads and writes stay under `hostedMu` in admission and `threadQueryGate`. The query still runs on the existing app-frame worker; `threadCompact` is set before `V2StateOpen` and is read-only afterwards, like `threadExpansion`.

## State transitions and identity reuse

| Event | Test |
|---|---|
| Compact join recorded, then complete version-zero re-initializes the same conversation (join flips to complete) | `TestThreadCompactJoins` |
| Nonzero request with projection differing from the recorded join (both directions), and complete page on a compact join | `TestThreadCompactRefusals` |
| Compact join on one conversation, complete on another, same connection; live updates gated per conversation | `TestThreadCompactJoins` |
| Reconnect (new connection, no join) issues nonzero compact; provider decides | `TestThreadCompactJoins` |
| Body/children read on a compact-joined conversation leaves the join unchanged | `TestThreadCompactJoins` |
| Repeated compact queries (catch-up after window) update the join version | `TestThreadCompactReplies` |

All run under `go test -race`.

## Error handling

Every failure is the content-free `thread.query_failed`; no provider error text reaches the client. Reset is the only non-error terminal besides a final.

## Testing strategy

New `internal/relay/v2session_compact_test.go`:

- `TestThreadCompactHandshake`: matrix over advertised compact, expansion prerequisites, providers and readiness; `thread_compact` granted only on the full set and the other grants match the matrix without compact.
- `TestThreadCompactRefusals`: unknown/null/empty/non-string projection, compact without the capability, compact page with missing/empty epoch, projection mismatch against a recorded join; each refuses with zero calls on all four query providers and leaves joins unchanged. A request without projection still calls the complete provider.
- `TestThreadCompactReplies`: window, nonzero catch-up, page, catch-up reset, page epoch mismatch reset; checks projection on every frame, items, headers with four facts, bounds/`from_version`, final, recorded join.
- `TestThreadCompactJoins`: the state-transition rows above.
- Extend `TestThreadChildrenPages` or add a children assertion that the four header facts reach the wire.

## Open questions

- Should complete updates still cancel an in-flight compact query? Decided no (see Design); #3574 owns compact live fencing.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md` § "Compact projection (declared)" under "Thread query messages": describe implemented compact query admission, refusal semantics and per-connection, per-conversation joins. § "Capability negotiation (v2)": state the `thread_compact` prerequisites. Keep production activation pending #3563 and compact live delivery pending #3574.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md` § "Thread delivery and summary projection": record per-conversation projection ownership of joins and that expansion reads never change a join.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The single boundary is `admitThreadQuery`: `projection` is decoded strictly to the one known literal and compact page `epoch` to a nonempty string, after the existing authentication, membership, agent and capability gates and before any provider call. Downstream `runThreadQuery` reads only the parsed `threadQuery`.
- [Trust boundaries] SHOULD FIX: `Store.CompactHistoryPage` takes no epoch, so a stale client epoch would otherwise yield rows from a new epoch under the old scope. `runThreadQuery` must compare the result epoch with the requested one and reply with a scoped reset; tested in `TestThreadCompactReplies`.
- [Trust boundaries] SHOULD FIX: a provider result whose projection or conversation does not match the request must refuse rather than be relabelled; `runThreadQuery` checks both.
- [Tokens] No findings. No credential is read, created or logged; the compact path reuses the session's existing authorization.
- [File operations] No findings. No filesystem access.
- [Subprocesses] No findings. None started.
- [Cryptography] No findings. Sealing and nonces are unchanged; withheld live updates return before sealing in `threadQueryGate`, so a dropped frame spends no nonce.
- [Network and I/O] No findings. Request size stays bounded by the existing frame cap; the join map keeps the `maxThreadJoins` bound for compact requests through the same check; reply frames go through `EncodeThreadReply`, which bounds headers and headline/error text via `ThreadSummary.MarshalJSON`.
- [Errors and logs] No findings. Every refusal is the content-free `threadQueryError`; provider errors and request content never reach the reply. Tests assert the refusal text.
- [Concurrency] No findings. The join-projection check runs under `hostedMu` beside the bound check, and `in.query` admits one query per connection, so no other query can record a join between check and final. `threadCompact` is written once before `V2StateOpen`.
- [Threat model] View-mixing (complete rows applied over a compact checkpoint, or the reverse) is closed by refusing non-initializing requests whose projection differs from the recorded join and by withholding complete live updates on compact joins. OUT OF SCOPE: compact live delivery and its fencing, #3574; daemon installation and production readiness, #3563.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17

## Revisions

### 2026-10-17: complete live rows during a compact load (verifier review of `d4be6fd6`)

- **What changed.** `threadQueryGate` now withholds a complete live item update whose conversation has a compact query in flight, instead of only after that query's final records a compact join. A complete in-flight query is still cancelled, as before. `cancelThreadQuery` is unchanged: it cancels only complete queries.
- **What drove it.** Review finding 2 (SHOULD FIX): with the original design, complete live rows were sealed and delivered between the frames of a compact catch-up on a conversation with no join, or with a complete join being re-initialized to compact. That mixes views before the final, which the threat model claims is closed. Finding 1 (MUST FIX): the no-cancel rule for compact queries had no test.
- **New contract.** From admission of a compact query on a conversation until its terminal frame, and afterwards while its join is compact, complete live item updates for that conversation return `ErrThreadWithheld` before sealing and never cancel the compact query. If the compact query fails, the withheld rows are recovered by the client's next catch-up, as any withheld or missed range is. A complete version-zero re-initialization on a compact-joined conversation is still cancelled by a complete live update, whose row stays withheld because the join is still compact.
- **Tests.** New `TestThreadCompactLiveInFlight`, under `-race`, over both the `DeliverThread` lane and the legacy `Push` path: compact load with no join, compact re-initialization over a complete join, and complete re-initialization over a compact join. It asserts the compact final and its `{epoch, version, compact-v1}` join, the cancelled complete query with its join unchanged then a successful retry, and that no live row reaches the client. Removing either projection condition fails it. `TestThreadCompactJoins` now returns a higher version on the repeated compact catch-up, so the row "repeated compact queries update the join version" moves the join (it is covered there, not in `TestThreadCompactReplies`). `TestThreadCompactNegotiation` also covers a nil `ThreadCompactReady`.
- **Naming.** The plan's `TestThreadCompactHandshake` and `threadSummaryFromChild` were built as `TestThreadCompactNegotiation` and `threadSummaryFrom`.
