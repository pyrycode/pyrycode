# `internal/thread` — conversation-owned history fold

`Fold` builds deterministic items from supplied raw `history.Entry` values for
one conversation. `Store` owns background replay and tailing for independently
loaded conversations. Both implement the history-backed storage design of
[ADR 042](../decisions/042-daemon-built-thread.md); daemon wiring remains downstream.
Use [raw history and its metadata](history-package-shape.md#shape), including
hidden facts; legacy receipt projections discard facts needed by the fold.

| Topic | Contents |
| --- | --- |
| [Background store](thread-package-background-store.md) | Loading, bounded replay/tail handoff, snapshot readiness, retry, isolation and joined lifecycle. |
| [Main-thread folding](thread-package-main-thread-folding.md) | Item identity/order/version, standalone entries, accepted sends, main work, visibility and recorded provenance. |
| [Agents and background work](thread-package-agents-and-background-work.md) | Agent and shell lifecycles, scoped joins, recovery references, parent repair and offline evidence. |

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
