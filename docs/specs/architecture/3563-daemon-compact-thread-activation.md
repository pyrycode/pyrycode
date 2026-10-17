# #3563 Activate compact thread queries and coalesced live delivery

## Files read

- `cmd/pyry/daemon_thread_relay.go` → `installDaemonThreadRelay` (clears then installs providers on the publisher's `*thread.Store`; `ThreadReady` attests the live publisher), `daemonThreadPublisher.publish`/`gap`, `daemonThreadUpdates`, `daemonThreadItem`.
- `cmd/pyry/daemon_thread_demand.go` → `queryDaemonThread`: the cancellable shared demand owner every bounded read already uses; it establishes the live boundary (`publishThread(..., true)`) before capture.
- `cmd/pyry/thread_shadow.go` → `follow` (50 ms tick, the serialized publication owner) and `shadowConversation` cursor fields.
- `internal/thread/compact_queries.go`, `compact_intervals.go` → `CompactNewestWindow`, `CompactHistoryPage`, `CompactCatchUp`, `CompactChanges` (coalesced final root states over `(after, Version]`, no activity extras; blocks only when `after` is current).
- `internal/relay/v2session_seams.go` → `ThreadCompactCatchUp`, `ThreadCompactPage`, `ThreadCompactReady` contracts.
- `internal/relay/v2session_conns.go` → `ActiveConn.View` (`ThreadViewUnjoined/Complete/Compact`) from `ActiveThreadConns`.
- `internal/relay/v2session_catchup.go` → `compactLiveGate`, `cancelThreadQuery`, `threadSummaryFrom` (unexported; the daemon needs its own copy).
- `internal/protocol/thread_compact.go`, `thread_encoding.go` → `ThreadCompactUpdatePayload`, `EncodeThreadUpdate` (exactly one of item/header/progress; `FromVersion < Version`).
- `docs/specs/architecture/3574-compact-live-thread-updates.md`: a live frame cancels only an in-flight load of its own projection; a load of the other projection withholds it.

No in-flight feature branch touches these files.

## Context

#3568/#3569 give the Store compact queries and coalesced compact intervals; #3573/#3574 admit compact queries and route compact live frames per join. Nothing installs them in the daemon, so `thread_compact` is never negotiated. This slice wires the real Store and makes the publisher emit compact live output. No decision record needed.

## Design

**Installation.** `installDaemonThreadRelay` clears `ThreadCompactCatchUp`, `ThreadCompactPage` and `ThreadCompactReady` with the other providers, then installs both reads through `queryDaemonThread` (same demand, retirement wait and publication boundary as complete reads): catch-up → `Store.CompactCatchUp(epoch, version, limit)` (version zero is the newest window), page → `Store.CompactHistoryPage(before_order, limit)`. `ThreadCompactReady` is true only when `ThreadReady()` holds and both expansion reads and both compact reads are installed. A cleared installation therefore leaves no compact callback or readiness.

**Per-recipient selection.** `publish` keeps one shared cursor (`threadEpoch`/`threadVersion`). After capturing the complete observation it partitions the active peers by `View`:

- complete frames go to `Complete` and `Unjoined` peers (unchanged from today for both);
- compact frames go to `Compact` and `Unjoined` peers.

Unjoined peers get both families so that whichever load they have in flight is cancelled by a frame of its own projection (the relay withholds the other family and every compact frame without a compact join). Compact-view peers never receive per-change complete frames, so hidden child events cost them nothing.

**Compact interval.** Only when a compact recipient exists and the observation advanced the same epoch, the publisher reads `CompactChanges(ctx, id, threadEpoch, threadVersion)`. It returns each affected root once, at its final state at the result version: tool/agent roots as headers (no body, no descendant inventory), other roots as complete items. Each row becomes one `thread_compact_update` with interval `(threadVersion, result.Version]`; an interval with no root change becomes one `progress` frame. A reset or unusable compact result is a gap (`p.gap`: retire peers, clear cursor), the existing #3514 recovery; no baseline is pushed.

**Coalescing bound.** Publication runs once per `follow` pass, i.e. at most every 50 ms per conversation plus the pass's own work. Per pass, compact output is at most one frame per affected visible root (or one progress frame), independent of the number of hidden child events. A pass that finds the Store ahead of the cursor always flushes, so the final state is delivered on the next tick after activity stops. Pending work between passes is just the Store's own coalesced root index; the publisher holds no per-event queue.

## Concurrency model

No new goroutines or locks. Publication remains serialized by the conversation's publication gate and counted by `p.wg`, which shutdown joins. Each peer's push is independent; a failed or cancelled push to one peer only skips that peer's frame and never changes the shared cursor or another peer's selection. Frames are encoded once per update and pushed per peer.

## State transitions and identity reuse

| Event | Test (`-race`) |
|---|---|
| Live burst under a closed parent: one final header per root per pass, final header equals Store | `TestDaemonThreadCompactLive` |
| Compact and complete peer on one conversation; each gets its own family | `TestDaemonThreadCompactLive` |
| Compact query/live boundary race: live frame during compact load cancels it; retry converges | `TestDaemonThreadCompactBoundary` |
| Installation cleared / reinstalled (stale readiness) | `TestDaemonThreadCompactInstallation` |

Reconnect, retained-gap and epoch recovery reuse the existing `gap`/retirement path; the compact branch adds only the reset-to-gap edge.

## Error handling

Compact read error or cancellation: abandon this pass without moving the cursor (next pass retries). Reset/unusable: gap. Encode failure: gap (as for complete).

## Testing strategy

Focused tests in new `cmd/pyry/daemon_thread_compact_test.go` over `testDaemonThreadQueryWire` and `testDaemonExpansionFixture`:

- installation and readiness gating, cancelled-context passthrough, clearing;
- authenticated compact newest window: headers only for tool/agent roots, no hidden descendant identities or payload bytes, byte/frame counts logged against a complete window;
- live burst of hidden child events with a compact and a complete peer: compact peer receives one header per affected root per pass, the last header matches the Store, no hidden identities; complete peer still receives per-change updates;
- boundary race: compact load held after capture, live change cancels it, retry converges.

## Open questions

- Full AC4 scale matrix (8333-item history across all query shapes) — attempt within budget; otherwise list as pending in the PR.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md` § "Thread query messages", "Query/live ordering", "Reconnect / Backfill semantics": replace pending-installation wording with production readiness (`thread_compact` granted when query/live and both expansion reads are installed), the coalescing bound (≤ one frame per affected root per 50 ms publication pass, final state flushed on the next pass), invalidation (headers carry rev/subtree version; open content is refreshed through bounded reads) and recovery (reset → retirement, no unsolicited baseline).
- `docs/knowledge/features/thread-package-shadow-lifecycle-and-evidence.md` § "Shadow lifecycle and evidence": real-Store wiring, demand/retirement ownership through `queryDaemonThread`, installed authenticated evidence.

## Security review

**Verdict:** PASS

- [Trust boundaries] Requests reach the providers only after relay admission (`compactThreadRequest` path in `v2session_catchup.go`) has authenticated the conn and checked conversation membership; `queryDaemonThread` additionally refuses unregistered/removed conversations. The publisher sends only daemon-authored Store rows. Per-recipient eligibility stays in the relay (`threadWithheld`, `compactLiveGate`); the daemon's View partition is only a routing hint, so a wrong hint can at worst send a frame the relay withholds.
- [Tokens] No findings: no credential is read; `ActiveConn` client-authored fields are not logged.
- [File operations] No findings: reads go through existing Store renders.
- [Subprocesses] No findings: none.
- [Cryptography] No findings: sealing is unchanged and withheld frames spend no nonce.
- [Network and I/O] Frames keep `EncodeThreadUpdate`'s `MaxThreadEnvelopeBytes` cap. Compact output per pass is bounded by affected roots and by `MaxCatchUpGap` (beyond it the Store returns reset → gap). SHOULD FIX (built): compute `CompactChanges` only when a compact recipient exists, so complete-only deployments pay nothing.
- [Errors and logs] Debug/warn logs carry only event names, never payloads or conn identity.
- [Concurrency] `CompactChanges` blocks when `after` is current; the call is guarded to run only when the observation advanced the same epoch, and runs under the publication context that shutdown cancels and `p.wg` joins.
- [Threat model] View mixing is closed by relay joins. OUT OF SCOPE: compact query starvation on a busy conversation (shared with complete queries); view change from compact to complete mid-connection relies on the client's gap rule (owner: client adoption #2080/#2200).

**Reviewer:** builder (self-review) **Date:** 2026-10-17

## Revisions

### 2026-10-17: unjoined peers get compact frames only when they negotiated `thread_compact`

**What changed.** The Design's per-recipient selection sent compact frames to every `Unjoined` peer. It now sends them only to `Compact` peers and to `Unjoined` peers that negotiated `thread_compact`. Every other unjoined peer gets complete frames only, as before this ticket. `relay.ActiveConn` gains `ThreadCompact`, which `ActiveThreadConns` sets from the session's negotiated capability next to `View`.

**What drove it.** The affected-test run on the salvaged tree failed `TestDaemonThreadStatusInterleaving` and `TestDaemonThreadIdleCommit`. Their complete-only peers are unjoined, so they received a compact frame for every pass. That frame never reaches such a peer, because the relay withholds it. But the daemon still spent `CompactChanges` on every pass of a complete-only deployment, against the security review's SHOULD FIX, and it burned a daemon envelope ID per withheld frame.

**New contract.** A peer that can only load complete views sees exactly today's frames. A compact-capable unjoined peer still gets both families, so `TestDaemonThreadCompactBoundary`'s cancellation of an in-flight compact load still holds. `CompactChanges` runs only when some recipient is compact-capable.

### 2026-10-17: installed recovery and stale-detail evidence

`TestDaemonThreadCompactRecovery` covers the AC3 cases this tree proves. A new connection resumes from a retained compact checkpoint, and its headers equal `Store.CompactCatchUp`. A stale epoch gets a compact-scoped `thread_reset`, never a baseline, and a newest window then converges on the Store. A child page read before a nested change is stale by the root header's `subtree_version`, and a fresh read returns the Store's version. A complete peer and a compact peer on one conversation each get only their own family, with independent checkpoints. Second-conversation isolation and the retirement-driven gap under a compact join are not separately exercised here; the PR lists them as pending.
