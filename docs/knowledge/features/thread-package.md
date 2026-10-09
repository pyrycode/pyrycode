# `internal/thread` — conversation-owned history fold

`Fold` builds deterministic items from supplied raw `history.Entry` values for
one conversation. `Store` owns background replay and tailing for independently
loaded conversations. Both implement the history-backed storage design of
[ADR 042](../decisions/042-daemon-built-thread.md). The daemon runs the store in
shadow; app publication remains downstream.
Use [raw history and its metadata](history-package-shape.md#shape), including
hidden facts; legacy receipt projections discard facts needed by the fold.

| Topic | Contents |
| --- | --- |
| [Background store](thread-package-background-store.md) | Loading, bounded replay/tail handoff, snapshot readiness, retry, isolation and joined lifecycle. |
| [Main-thread folding](thread-package-main-thread-folding.md) | Item identity/order/version, standalone entries, accepted sends, main work, visibility and recorded provenance. |
| [Agents and background work](thread-package-agents-and-background-work.md) | Agent and shell lifecycles, scoped joins, recovery references, parent repair and offline evidence. |

## Shadow lifecycle and evidence

`runSupervisor` composes `thread.NewStore` with the same `history.Store` used by
interactive emission, session transitions, operator sends/queue facts and direct
channel posts. After `reconcileStartupHistory` and completion of `startRelay`'s
runtime-history construction, `threadShadow.start` schedules registered old logs
in the background without waiting for replay. Construction must finish before
shadow quiescence readers enter: `installRuntimeHistory` assigns
`busy.runtimeSink`, which those readers subsequently treat as immutable.
Folding consumes raw entries, including hidden facts and captured source
provenance; it never attributes old entries through the active session cursor.

Background discovery and per-conversation coordination inspect committed history
versions. This covers conversations created after startup and activity after
unload, including `channelDelivery.deliver`, which bypasses
`appendConversationHistory`. Waking only from that helper would miss direct
posts. Loaded workers use the same reader for replay and committed tailing;
settled items/revisions match full replay through the consumed version.
Replay/cache I/O and joins run outside producer/publication paths and gates.

A caught-up usable fold unloads only when `Snapshot.Active` is false and
`shadowQuiescent` finds producer/transition processing quiescent. Active
background or unresolved private work, queue entries, channel writes, pending
runtime boundaries, unconsumed stream output and placement commands prevent
retirement. Idle sleep must publish and fold its closure/divider facts first.
Unload releases items, private joins and pending reports while coordination
retains the consumed version. Later commits reload through cache validation and
fresh log replay, including a direct post or an append racing `Unload`; cached
public items never seed continuation state.

`dropRingOnConversationDelete` retains the registry's single removal observer
for explicit deletion and sweep, including with the relay disabled. It preserves
ring/reply-suggestion cleanup, tombstones the ID, joins shadow coordination and
unloads the store worker. Registry membership checks and tombstones prevent late
commits, callbacks or discovery from reopening removed conversations.

Shutdown seals producer admission and joins/drains writers before final history
versions are established. Shadow contexts are detached from daemon cancellation
so producer drains and error-return cleanup can commit their final facts.
[`Server.Seal`](control-plane-server-and-deadlines.md#handler-admission-and-ownership)
joins admitted control writers while the listener retains ownership through
final cache persistence. Shadow shutdown discovers final new activity, stops
discovery/admission, joins each remaining coordinator after final catch-up, then
calls `Store.Shutdown`. History/fold failures end final waiting; they cannot
certify a clean cache exit even if persistence succeeds. Final discovery must
also establish every remaining registered conversation's boundary: checking only
existing coordinators would overlook a conversation that failed before one was
created. `thread_shadow.clean_exit` requires both complete final folding and
successful `Store.Shutdown` persistence. Fixed failure reasons and validated IDs
are the diagnostic boundary; content, payloads and host paths are excluded.

No app receives thread items yet: shadow advertises no `thread` capability and
sends no thread frames. Nil or failed shadow storage leaves legacy traffic
operational. `TestThreadShadowLegacy` checks an authenticated Noise peer through
the production session manager, comparing live frames, reconnect replay, history
requests and negotiated capabilities with enabled, nil and failed storage.
Fake broadcaster capture or direct ring/pager reads alone miss those client
boundaries. `TestThreadShadowLongReplay` pauses an actual 36,000-entry refold
while another conversation appends and publishes, then checks full-replay
equality; `TestThreadShadowRemoval` pauses actual replay/tail work to prove joined
retirement and continued progress elsewhere. These are deterministic wiring
proofs. Retained authenticated real-log evidence belongs to
[#3068](https://github.com/pyrycode/pyrycode/issues/3068) before protocol migration.

## Cache and epochs

History is the only durable source of truth. Beneath the supplied history
instance directory, `conversations/<conversation-id>/history/` holds
`thread-cache.json` and `thread-recovery.json` beside the history segments.
The cache stores detached items, consumed version, schema and folding-rule
revisions, epoch, complete-progress flag and run/coordinator references. The
recovery marker names the store lifetime that opened the conversation; its
coordinator conversation holds `thread-run-<token>.json`. `EnsureLogDir` can
create an empty contained directory without fabricating history entries.

Every load or retry reconstructs a fresh fold from readable history with bounded
`ForwardReader.Walk` chunks. Cached items are compared with fresh replay at their
consumed version before their epoch can be retained; they never seed the fold.
**Visible items are insufficient continuation state:** `Fold.Items` omits
unresolved children, pending reports and private joins. Full replay restores
unfinished text, queued sends and that hidden evidence before usability. The
same reader then tails history, consuming commits during recovery/handoff once
in order and converging on later commits through the supplied history store's
notifications. See [loading and replay readiness](thread-package-background-store.md#loading-and-replay-readiness).

Each usable `Snapshot.Epoch` is a 128-bit random identifier encoded as 32 hex
characters, separate from deterministic item equality. Across store lifetimes,
reuse requires a compatible complete cache, matching recovery marker, completed
run certificate and matching replayed items. Changed folding rules, an unclean
stop, missing/corrupt/incompatible metadata, mismatched items or a cache ahead of
surviving history require a new epoch. Before replay/publication, the store
establishes an incomplete run certificate and replaces the conversation's
recovery marker, invalidating its previous clean claim. Failure leaves the view
unavailable. Folding semantics changes must increment `foldingRules`.

Recovery remains `StateRebuilding` until replay and a complete checkpoint
succeed. Missing or unusable cache data rebuilds from history; genuinely missing
or empty history gives an empty usable view at version zero. Unreadable history
never yields a cache-only view. History/cache I/O, containment and unsafe-leaf
failures instead leave `StateUnavailable` with generic `ErrUnavailable`;
repair followed by `Retry` creates a new reader and fold. Completed progress is
checkpointed before every publication, including tail updates. Writes use
synced, closed `0600` temporary files and atomic replacement; reads use no-follow
regular-file checks. Each operation validates IDs and re-resolves history's
exact directory containment, rejecting sibling/outside redirects and symlink or
nonregular leaves. Errors contain no content, payloads or host paths, and cache
operations preserve history bytes and its existing append durability contract.

`Unload` checkpoints completed progress where available, cancels/joins the
worker, unregisters tailing and releases items and private fold state, retaining
only scalar lifecycle records. Compatible unload/reload in one store lifetime
preserves the epoch even though that run's certificate remains incomplete;
unload never certifies a clean exit. `Shutdown() error` closes admission, joins
all workers, revalidates/replaces final checkpoints for loaded and previously
unloaded conversations, then completes the shared run certificate last. Only
successful completion permits epoch reuse in a later lifetime. Failed replay,
cancellation before recovery completes or failed/interrupted checkpoint or final
certificate writes cannot certify clean shutdown. Lifecycle methods report
failures explicitly; repeated/concurrent shutdown callers share one result.
See [isolation and lifecycle](thread-package-background-store.md#isolation-and-lifecycle).
