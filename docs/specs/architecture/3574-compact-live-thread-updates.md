# #3574 Route compact live thread updates per conversation selection

## Files read

- `internal/protocol/thread_compact.go` → `ThreadCompactUpdatePayload`: scope fields (`projection`, `conversation_id`, `epoch`, `version`) and exactly one of `item`/`header`/`progress`.
- `internal/protocol/thread_encoding.go` → `EncodeThreadUpdate`: a compact update that overflows becomes continuation parts whose payload is a `ThreadUpdatePart` repeating `projection`, `conversation_id`, `epoch`, `version`, so the gate's scope parse works on every part.
- `internal/relay/v2session_thread_delivery.go` → `deliverThread` (type admission via `threadContent`, ordered lane), `threadContent`.
- `internal/relay/v2session_thread.go` → `threadItemUpdate`, `threadWithheld` (eligibility checked in both the hosted lane and the legacy `forwardEnvelopeSource` path), `threadReply`.
- `internal/relay/v2session_catchup.go` → `threadQuery.projection` (empty for complete and for body/children reads), `threadJoin`, `cancelThreadQuery` (Push-side fence), `threadQueryGate` (pre-seal fence, join recording at a final).
- `internal/relay/v2session_hosted.go` → `hostedDeliver`: runs `threadWithheld` then `threadQueryGate` on the head before sealing; `retireHosted` drops the inbox and its joins.
- `internal/relay/v2session_push.go` → `pushSource` calls `cancelThreadQuery` before queueing; `forwardEnvelopeSource` seals directly when `s.hosted == nil` and otherwise hands the envelope to the hosted lane.
- `internal/relay/v2session_conns.go` → `ActiveConn`, `ActiveThreadConns`, `handleConnSnapshot` (thread, membership and retirement gates).
- `cmd/pyry/daemon_thread_relay.go` → `daemonThreadPublisher.active` holds `m.ActiveThreadConns` as a func value: its signature must not change.
- Tests: `testCompactConfig`, `testCompactJoins`, `testCompactSeed`, `testCompactReply`, `testCompactCatchUp`, `testCompactLive`, `TestThreadCompactLiveInFlight` (`v2session_compact_test.go`); `testThreadQueryLiveRace`, `testThreadQueryJoinedLive`, `testCatchUpError` (`v2session_catchup_test.go`); `testHostedStartCaps`, `testHostedPeer.open`/`next` (`v2session_hosted_test.go`).
- `docs/specs/architecture/3573-compact-thread-queries.md` § Revisions: a load owns its conversation's view from admission; rows of the other projection are withheld, never cancel it.

No feature branch in flight touches the four production files.

## Context

#3559 declared `thread_compact_update`; #3573 admits compact queries and records `{epoch, version, projection}` joins, withholding complete live rows from compact views. The relay still rejects compact live frames and the daemon cannot tell which projection a connection selected. This slice adds the relay live path. Coalescing, flush scheduling and daemon installation are #3563. No decision record needed.

## Design

**Admission.** `threadContent` admits `protocol.TypeThreadCompactUpdate`, so `DeliverThread` queues it on the existing ordered lane and `hostedDeliver` applies `threadWithheld` and `threadQueryGate` before sealing. `threadItemUpdate` is unchanged (complete only).

**Eligibility** (`threadWithheld`, both paths): a compact update is additionally withheld unless the session negotiated `thread_compact` and has a hosted inbox (`s.hosted != nil`). Without an inbox there is no join, and this is what keeps the legacy direct-seal Push path from delivering compact frames ungated. Existing thread and membership (`liveWithheld`) checks apply as for every thread content type.

**Fence** (`threadQueryGate`, under `hostedMu`, immediately before sealing; regardless of `InReplyTo`, since a compact update is never a reply):

1. Parse scope `{projection, conversation_id, epoch, version}`; parse failure or projection other than `compact-v1` → `ErrThreadWithheld`.
2. In-flight items query (not a body/children read) on the same conversation: complete → `ErrThreadWithheld` without cancelling (a complete load owns its view); compact → `cancel()`, then continue (mirrors complete live today).
3. Join for the conversation must exist with projection `compact-v1`, equal epoch and `version > join.version`; otherwise `ErrThreadWithheld`.

The gate never writes `threadJoins`: only a successful query final sets a join. Updates for different roots sharing a version both pass, because the join does not advance. Progress frames carry the same scope and pass unchanged. Body and children reads are neither cancelled nor touched.

**Push fence** (`cancelThreadQuery`): a compact update cancels an in-flight compact items query of its conversation (complete updates keep cancelling non-compact queries as today).

**Projection visibility.** New exported `ThreadView` (`ThreadViewUnjoined`, `ThreadViewComplete`, `ThreadViewCompact`) and an additive `ActiveConn.View` field. `handleConnSnapshot` fills it only for `ActiveThreadConns`, from the connection's recorded join for that conversation (under `hostedMu`; no inbox or no join → unjoined). Same eligibility gates as today; `ActiveConns` leaves it zero. Callers holding `m.ActiveThreadConns` as a func value compile unchanged.

## Concurrency model

No new goroutines or locks. Join reads in the snapshot take `hostedMu` on the Run goroutine, as `threadQueryGate` already does; the gate's check and cancel stay inside its existing `hostedMu` hold. `s.threadCompact` is written before `V2StateOpen` and read-only afterwards.

## State transitions and identity reuse

| Event | Test (`-race`) |
|---|---|
| Compact live races compact query completion (live first: query cancelled, join unchanged; final first: join set, stale update withheld) | `TestThreadCompactLiveQueryRace` |
| Complete re-initialization in flight over a compact join meets compact live: withheld, not cancelled | `TestThreadCompactLiveQueryRace` |
| Body read in flight on a compact-joined conversation meets compact live: completes, join unchanged | `TestThreadCompactLiveQueryRace` |
| Repeated updates at one version for different roots, then stale versions | `TestThreadCompactLiveFencing` |
| Epoch reset on one conversation: new-epoch frames withheld until re-initialization, old-epoch frames withheld after; other conversation unaffected | `TestThreadCompactLiveScope` |
| Reconnect reusing the same conn ID: the new session has no join, frames withheld until it re-joins; another connection's join unaffected | `TestThreadCompactLiveScope` |
| Two connections on one conversation (complete, compact) and one connection compact on one conversation, complete on another | `TestThreadCompactLiveRouting` |

## Error handling

Every refusal is `ErrThreadWithheld` returned to the `DeliverThread` caller before sealing, so no nonce is spent and the connection stays open. Push-path drops are silent (nil), as for complete. Malformed scope is withheld, never delivered.

## Testing strategy

New `internal/relay/v2session_compact_live_test.go`, with joins established by real compact/complete queries where the scenario needs it and seeded otherwise:

- `TestThreadCompactLiveRouting`: compact and complete peers on one conversation each receive only their projection's frames (item, header, progress); one peer compact on A, complete on B; `ActiveThreadConns` reports compact/complete/unjoined per connection and `ActiveConns` reports unjoined.
- `TestThreadCompactLiveFencing`: stale and equal versions withheld, two roots at one new version both delivered, join unchanged afterwards; wrong epoch, wrong/missing projection, no `thread_compact`, non-member conversation withheld; Push of a compact frame without an inbox withheld; a delivered frame after withheld ones decrypts (no nonce spent).
- `TestThreadCompactLiveQueryRace`: the race rows above, over `DeliverThread` and Push.
- `TestThreadCompactLiveScope`: the reset and reconnect rows.

Existing `TestThreadCompactLiveInFlight`, `TestThreadCompactJoins`, catch-up race and delivery tests must keep passing.

## Open questions

- Should a connection with an in-flight compact load but no join report compact in `View`? Decided no: `View` reflects the recorded join only, matching the AC's "complete, compact, or not yet joined"; the gate withholds rows of the other projection during a load either way.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md` § "Query/live ordering": describe compact live fencing (join projection, epoch and version; live never advances a join), scoped withholding, progress frames, and that a compact update cancels an in-flight compact query while a complete load withholds it. Keep production activation pending #3563.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md` § "Thread delivery and summary projection": record per-recipient projection facts (`ActiveConn.View` from `ActiveThreadConns`) and that expansion completion is independent of live joins.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. Content is daemon-authored; the boundary this ticket guards is per-recipient eligibility, enforced in two places that both run before sealing: `threadWithheld` (thread, membership, negotiated `thread_compact`, presence of a hosted inbox) and `threadQueryGate` (join projection, epoch, version, in-flight load). The legacy Push path seals directly only when `s.hosted == nil`, and `threadWithheld` withholds every compact frame there, so no path seals a compact frame without the join check.
- [Trust boundaries] SHOULD FIX: the gate must not trust `InReplyTo` to skip the fence, as the complete branch does for replies. The compact branch keys on the type alone; `TestThreadCompactLiveFencing` delivers a compact frame carrying `in_reply_to` with no join and expects it withheld.
- [Tokens] No findings. No credential is read or created; `ActiveConn.View` is a daemon-authored enum with no client content.
- [File operations] No findings. No filesystem access.
- [Subprocesses] No findings. None started.
- [Cryptography] No findings. Withholding returns before `hostedDeliver` seals, so a withheld frame spends no nonce; the fencing test proves a later frame still decrypts.
- [Network and I/O] No findings. Frames keep the `MaxThreadEnvelopeBytes` cap in `deliverThread` and the bounded ordered lane; the gate adds one small JSON decode per head. Continuation parts of one oversize update are gated independently; a sequence cut short by withholding is never assembled by the consumer, which keys parts by update ID and requires catch-up on gaps, and the `DeliverThread` contract tells the producer to stop on the first error.
- [Errors and logs] No findings. Refusal is the content-free `ErrThreadWithheld`; no payload is logged.
- [Concurrency] No findings. The gate's check, cancel and join read share one `hostedMu` hold, so a final cannot record a join between them. The snapshot takes `hostedMu` on its own, never nested with `pushMu`, matching the existing order where `threadQueryGate` runs under `hostedMu` alone.
- [Threat model] View mixing (compact rows over a complete view or the reverse, or stale/replaced-epoch rows over a checkpoint) is closed by the join gate and by withholding the other projection during a load. OUT OF SCOPE: repeated cancellation of compact queries on a busy conversation (query starvation), which complete queries share today; burst coalescing in #3563. Daemon installation and production activation: #3563.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17
