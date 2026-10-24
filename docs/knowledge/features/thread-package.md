# `internal/thread` — conversation-owned history fold

`Fold` builds deterministic items from supplied raw `history.Entry` values for
one conversation. `Store` owns background replay and tailing for independently
loaded conversations. Both implement the history-backed storage design of
[ADR 042](../decisions/042-daemon-built-thread.md). The daemon publishes usable
fold observations and serves bounded catch-up/pages to eligible apps. Every
admitted app session negotiates `thread`; complete queries, older pages and
updates do not require `interactive`. Supplied live state and controls retain
interactive gates, while expansion/compact retain their additional grants and
readiness checks. Missing optional grants never restore legacy delivery. See
[admission and delivery](v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md).
Apps initialize each open conversation with an explicit
named catch-up request; handshake and listing do not query items.
Use [raw history and its metadata](history-package-shape.md#shape), including
hidden facts; legacy receipt projections discard facts needed by the fold.

| Topic | Contents |
| --- | --- |
| [Background store](thread-package-background-store.md) | Loading, bounded replay/tail handoff, snapshot readiness, retry, isolation and joined lifecycle. |
| [Main-thread folding](thread-package-main-thread-folding.md) | Item identity/order/version, standalone entries, accepted sends, main work, visibility and recorded provenance. |
| [Agents and background work](thread-package-agents-and-background-work.md) | Agent and shell lifecycles, scoped joins, recovery references, parent repair and offline evidence. |

## Shadow lifecycle and evidence

[Daemon publication, lifecycle and capture evidence](thread-package-shadow-lifecycle-and-evidence.md)
covers discovery, query/live ordering, retirement and recorded evidence.
Interactive assistant/tool/completion source folding remains authoritative while
its compatibility publication is retired for every app (#3734), including peers
without `thread` and nil/failed history. Supported live state remains separate;
channel-post publication is also retired (#3737).
`TestInteractiveContentRetired` compares actual canonical live revisions with explicit catch-up, checking settled ordered
text, tool/parent attribution and reused identities without raw frames/replay or
substitute receipts. Its storage controls and tagged compilation are
deterministic evidence, not live acceptance.

Background-agent start/update/roster values and neutral attribution/ending facts
also remain source evidence, without raw app frames, new replay copies, synthetic
agent receipts or connect-time roster snapshots for any capability (#3735).
`TestAgentContentRetired` compares canonical live revisions and explicit catch-up
through late links, reported outcomes, saved gone/session endings and reused
identities; task progress and bound-session stop controls remain supported.
The counted Linux live gate passed `TestRealClaudeStopBackgroundTaskCompletion`
and `TestRealClaudeComposerStopPreservesBackgroundTask` on `ad1cb1aec214`;
[the linked evidence](thread-package-shadow-lifecycle-and-evidence-retirement.md#shadow-lifecycle-and-evidence)
distinguishes that child acceptance from offline helpers. The
[integrated acceptance](thread-package-shadow-lifecycle-and-evidence-retirement.md#integrated-retirement-acceptance-3718)
closes #3718's retained lifecycle/source-reader findings with actual envelopes
and counted current-reader live proof. Stop acceptance alone cannot establish
completion.

Session transitions, refusal notices, compaction boundaries and unsupported
content likewise remain retained sources with no raw frames, new replay copies
or substitute transition-divider receipts for any app (#3736). Canonical live
items and explicit catch-up agree; supported live state and switch completion
remain separate. `TestSessionContentRetired` covers session causes, lifecycle
neutrality and source isolation; its storage controls prevent publication from
returning when history fails. The
[counted Linux evidence and ordering lessons](thread-package-shadow-lifecycle-and-evidence-retirement.md#session-and-content-notice-retirement-3736)
include the subsequent deterministic and configured live passes on `9ebba3462074`.
Their child scope is supplemented by the integrated evidence linked above.

Standalone banners, runtime/startup and resolved prompt-answer facts, and channel
post chunks/completion also retain source recording without raw frames, new
content replay copies or inert receipts for any app (#3737). Canonical live
items and explicit catch-up consume saved facts and completed posts; open
prompts, dismissals, controls and ordered live state remain supported.
[Residual retirement evidence](thread-package-shadow-lifecycle-and-evidence-retirement.md#residual-content-retirement-3737)
records prompt responsiveness, completion-before-wake/recovery and counted Linux
prompt/hook acceptance, preserving original failures. Event replay and shared
ring storage/`SetReplaySource` are removed (#3738/#3739); durable source history
and supported live state remain separate. Content recovery uses explicit
per-open-conversation epoch/version catch-up, older pages and a newest window
after `thread_reset`. [Integrated recovery acceptance](v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md#integrated-acceptance)
records the final #3719 verdict and pending slow/release/live checks. See [reconnect recovery](v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md)
and [composed removal](thread-package-shadow-lifecycle-and-evidence-coordination.md).
The daemon compatibility transition in [ADR 042](../decisions/042-daemon-built-thread.md#compatibility)
is complete: replaced publication and replay are retired, thread is mandatory
for admission (#3740), and non-thread forwarding, metadata stripping and legacy
connect recovery are removed (#3741). Supplied attribution/clears and authoritative
summary watermarks survive projection; retained source/live-state contracts and
existing operator/approval holds remain.

A cold complete-thread read needs a genuinely negotiated thread-only fixture.
An interactive fixture stays green when admission, publication recipient selection
or final delivery accidentally requires interactive.
`TestRelayV2_ClientFeaturesSpawnSnapshot` keeps first-post named catch-up, final
completion, prompt attribution, reconnect and eviction assertions in both
`thread-only` and `thread-interactive` cases; `TestThreadCompleteCapabilities`
covers complete pages and later updates without expansion/compact grants.

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
