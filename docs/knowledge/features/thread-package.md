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
covers discovery, query/live ordering, retirement and recorded evidence.
Interactive assistant/tool/completion source folding remains authoritative while
its compatibility publication is retired for every app (#3734), including peers
without `thread` and nil/failed history. Supported live state remains separate;
channel-post publication awaits #3737. `TestInteractiveContentRetired` compares
actual canonical live revisions with explicit catch-up, checking settled ordered
text, tool/parent attribution and reused identities without raw frames/replay or
substitute receipts. Its replay/storage controls and tagged compilation are
deterministic evidence, not live acceptance.

Background-agent start/update/roster values and neutral attribution/ending facts
also remain source evidence, without raw app frames, new replay copies, synthetic
agent receipts or connect-time roster snapshots for any capability (#3735).
`TestAgentContentRetired` compares canonical live revisions and explicit catch-up
through late links, reported outcomes, saved gone/session endings and reused
identities; task progress and bound-session stop controls remain supported.
The counted Linux live gate passed `TestRealClaudeStopBackgroundTaskCompletion`
and `TestRealClaudeComposerStopPreservesBackgroundTask` on `ad1cb1aec214`;
[the linked evidence](thread-package-shadow-lifecycle-and-evidence.md#shadow-lifecycle-and-evidence)
distinguishes that acceptance from offline helpers and parent #3718's remaining
family acceptance. Stop acceptance alone cannot establish completion.

### Reusable fold regression corpus

[Generated, shadow and recorded corpus proofs](thread-package-reusable-fold-regression-corpus.md#reusable-fold-regression-corpus).

### Full-feed benchmark

[Full-feed workload and measurements](thread-package-shadow-lifecycle-and-evidence.md#full-feed-benchmark).

## Cache and epochs

[Cache durability, recovery and epochs](thread-package-cache-and-epochs.md#cache-and-epochs)
covers scalar certificates, incremental checkpoint transactions, retry authority,
the 30-second changed-version cadence, joined final certification, public touches
and catch-up continuity. Only item-record appends are incremental; state-file
replacements are complete. Load/Retry restores a complete validated checkpoint
and folds only its surviving suffix. Clean state retains its epoch; dirty state
resumes with a new epoch. Absent/rejected or scalar-only state refolds with a
new epoch, while actual history/persistence failures withhold usability.

### Send now before consumption (#3508)

[Written-send lifecycle](thread-package-cache-and-epochs.md#send-now-before-consumption-3508).
