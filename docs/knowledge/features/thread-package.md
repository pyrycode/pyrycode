# `internal/thread` — conversation-owned history fold

`Fold` builds deterministic items from supplied raw `history.Entry` values for
one conversation. `Store` owns background replay and tailing for independently
loaded conversations. Both implement the history-backed storage design of
[ADR 042](../decisions/042-daemon-built-thread.md). The daemon publishes usable
fold observations and serves bounded catch-up/pages to eligible apps that
negotiate `thread`. Apps initialize each open conversation with an explicit
named catch-up request; handshake and listing do not query items.
Use [raw history and its metadata](history-package-shape.md#shape), including
hidden facts; legacy receipt projections discard facts needed by the fold.

| Topic | Contents |
| --- | --- |
| [Background store](thread-package-background-store.md) | Loading, bounded replay/tail handoff, snapshot readiness, retry, isolation and joined lifecycle. |
| [Main-thread folding](thread-package-main-thread-folding.md) | Item identity/order/version, standalone entries, accepted sends, main work, visibility and recorded provenance. |
| [Agents and background work](thread-package-agents-and-background-work.md) | Agent and shell lifecycles, scoped joins, recovery references, parent repair and offline evidence. |

## Shadow lifecycle and evidence

[Daemon publication, lifecycle and capture evidence](thread-package-shadow-lifecycle-and-evidence.md#shadow-lifecycle-and-evidence)
covers discovery, query/live ordering, retirement and recorded regression corpora.

### Reusable fold regression corpus

[Generated, shadow and recorded corpus proofs](thread-package-shadow-lifecycle-and-evidence.md#reusable-fold-regression-corpus).

### Full-feed benchmark

[Full-feed workload and measurements](thread-package-shadow-lifecycle-and-evidence.md#full-feed-benchmark).

## Cache and epochs

[Cache durability, recovery and epochs](thread-package-cache-and-epochs.md#cache-and-epochs)
covers scalar certificates, incremental checkpoint transactions, retry authority,
Store replay, public touches and catch-up continuity. Worker scheduling and final
Store checkpoint certification are owned by #3501; Store loading still replays history.

### Send now before consumption (#3508)

[Written-send lifecycle](thread-package-cache-and-epochs.md#send-now-before-consumption-3508).
