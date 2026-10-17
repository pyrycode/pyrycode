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

### 2026-10-17: verifier findings 1–4 — projection switches and complete installed evidence

**Design correction (finding 1).** `ActiveConn.PendingView` reports an admitted items query alongside the completed `View`, captured atomically under `hostedMu` by `threadViews`. Expansion reads leave it unjoined. `publish` selects both completed and pending families; only a pending switch incurs both encodings. Complete-only consumers still incur no compact work. Admission after the recipient snapshot cannot capture stale state: `queryDaemonThread` acquires the same publication gate before its Store read. Relay cancellation and sealing remain the authority for per-recipient content.

**Files read.** `internal/relay/v2session_conns.go` → `handleConnSnapshot`/`threadViews`, `internal/relay/v2session_catchup.go` → `admitThreadQuery`/`cancelThreadQuery`/`threadQueryGate`, `cmd/pyry/daemon_thread_demand.go` → `queryDaemonThread` establish admission, snapshot and capture ordering. `internal/thread/observations_store.go` → `retain`/`Changes` and `internal/thread/compact_intervals.go` → `CompactCatchUp` explain why compact recovery can survive loss of the complete publication range. The relay overview's query-completion fencing lesson requires checking capture and final certification separately.

**Evidence correction (findings 2–4).** The formerly pending AC3/AC4 cases are required in this rework. The burst fixture keeps its visible roots fixed, varies hidden children from 5 to 8333, error bodies from 64 to 4096 bytes, and the nested live burst from 3 to 25 children (use plus error-result events). Measure fully stamped bytes and physical envelopes for newest-window, older-page, catch-up and live delivery after publication and ordered-lane drain. Fragment assembly survives drain markers; every continuation contributes a physical frame. Compact output must contain only known root identities, exclude hidden payloads, and have the same bounded frame counts across scales. Headers are compared to the Store. Real prompt producers, the send-message handler and the live-state publisher must deliver questions, approvals, correlated send acknowledgements and main-turn state within three seconds while thread publication is held halfway through the burst.

| Repeated/lifecycle event | Race test |
|---|---|
| Complete→compact and compact→complete, capture before publication and admission after recipient snapshot | `TestDaemonThreadCompactSwitch` |
| Eviction of 65 separately observed complete publication batches while compact-joined, retirement without baseline, explicit compact recovery | `TestDaemonThreadCompactRetirement` |
| Store unload, persisted epoch state removed, actual replacement epoch while compact-joined, retirement and explicit reset/window recovery | `TestDaemonThreadCompactRetirement` |
| Two simultaneous conversations with opposite complete/compact joins on the same peers, independent content/checkpoints | `TestDaemonThreadCompactConversationIsolation` |
| Nested bursts, final flush after activity stops, following idle pass, other live channels during held publication | `TestDaemonThreadCompactWire` |
| Hidden-only consumed interval produces one progress envelope, with no repeated output on an idle pass | `TestDaemonThreadCompactProgress` |

**Security review addendum — PASS.** Pending view metadata is daemon-owned and read with the completed join under one existing mutex. A stale snapshot cannot authorize content: `threadQueryGate` rechecks projection before sealing and cancellation shares its lock; membership remains checked by `threadWithheld`. No new tokens, files, subprocesses, cryptography, goroutines or logging are introduced in production. Network caps and shutdown ownership remain unchanged. The earlier threat-model exclusion for projection switching is withdrawn: both switches are now covered here. No unresolved security finding remains in this design.

**Documentation handoff addendum (pending documentation stage).** In `docs/protocol-mobile.md` § "Compact projection (declared)", "Query/live ordering", "Reconnect / Backfill semantics" and the `thread_compact` capability row, replace pending-activation wording and describe production capability readiness, completed/pending view fencing, one final root header per 50 ms publication pass (or progress), trailing final flush, stale fetched details and explicit recovery without unsolicited baselines. In `docs/knowledge/features/thread-package-shadow-lifecycle-and-evidence.md` § "Shadow lifecycle and evidence", record real-Store wiring, demand/retirement ownership and the installed authenticated recovery/isolation evidence and corrected burst measurements from the PR. Client adoption remains downstream.

**Concurrency addendum.** Production adds no goroutines. The measurement helper drains wire output while its barrier goroutine flushes pushes, avoiding a full synthetic outbound buffer blocking the barrier. That goroutine has a 30-second operation context, is cancelled and joined on every return, and fragment assembly continues across a drain marker.

**Open questions resolved.** All formerly pending AC3/AC4 evidence is implemented in this stage. Only the documentation handoff and dispatcher-owned complete gates remain later-stage work.

**Sizing.** Rework: approximately 700 written lines including replaced lines, no new exported type/interface, one snapshot consumer update, four findings and no new reject branch. No overlapping feature branch touches the edited existing production/test files.

### 2026-10-17: verifier findings on `accd4cb1` — lint and latency proof

Discard the unused fixture item snapshot in `TestDaemonThreadCompactWire` and declare `items` only at the later Store snapshot, removing SA4006. In `testCompactBurstWire`, check the three-second elapsed-time bound immediately after each wire read as well as before it, so a late batch containing every required event cannot pass. Production design and security boundaries are unchanged. Validate with staticcheck on `cmd/pyry` and focused race-enabled `TestDaemonThreadCompactWire` / `TestDaemonThreadCompactLiveChannels` on Linux; complete gates remain dispatcher-owned.
