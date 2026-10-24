# `internal/thread` — daemon thread publication

[Shadow lifecycle overview](thread-package-shadow-lifecycle-and-evidence.md).

## Background discovery and publication

Background discovery and per-conversation coordination inspect committed history
versions. This covers conversations created after startup and activity after
unload, including `channelDelivery.deliver`, which bypasses
`appendConversationHistory`. Waking only from that helper would miss direct
posts. Loaded workers use the same reader for replay and committed tailing;
settled items/revisions match full replay through the consumed version.
Replay/cache I/O, publication and joins run outside history writers and producer
gates. Held peer delivery cannot block history commits.

`startRelayV2` installs `installDaemonThreadRelay` after the retained live-state
providers and starts `startDaemonThreadPublisher` before connection admission.
`installDaemonThreadRelay` binds `ThreadCatchUp`/`ThreadPage` to
`Store.CatchUp`/`Store.HistoryPage` and `ThreadToolBody`/`ThreadChildren` to
`Store.ToolBody`/`Store.ChildPage` on the publisher's actual `thread.Store`.
`ThreadCompactCatchUp`/`ThreadCompactPage` bind `Store.CompactCatchUp`/
`Store.CompactHistoryPage` on that same Store through `queryDaemonThread`;
version-zero compact catch-up selects the newest public root window.
Adapters preserve the request context, named conversation and each captured
result, including body kind, epoch/version and revision. Child requests pass
the normalized limit and every cursor field; no adapter selects twice or reads a
full `Snapshot`/`Observe` baseline. Installation clears all six query callbacks
and `ThreadCompactReady` before checking the Store, preventing stale providers
or readiness after clearing a reused config.
`ThreadReady` requires both complete queries, the authoritative watermark lookup,
`ThreadLiveState`/`LiveStateCurrent` delivery and a running publisher on that
same Store. Expansion additionally requires both expansion providers and client
`thread_expansion`/`thread`/`interactive` capabilities; missing either provider
withholds expansion while complete-thread negotiation still works. Readiness
does not promise that every conversation has completed replay.
`ThreadCompactReady` additionally requires both compact queries and both
expansion reads; production grants `thread_compact` only alongside the granted
`thread`/`interactive`/`thread_expansion` capabilities. A stopped publisher or
missing expansion provider withholds it. Complete history/live delivery remains
the default; compact publication is installed, with Desktop #2080 and Mobile
\#2200 adoption downstream. See the
[expansion wire contract](../../protocol-mobile.md#bounded-expansion-reads).
Authenticated queries load cold or idle registered conversations and wait
cancelably for readiness; unavailable views fail with `thread.query_failed`
without certifying partial items.
`ThreadLastShownVersion` checks
registry membership before any certificate access. Loaded usable conversations
read `Store.Status` without certificate I/O or copying a baseline; rebuilding
and unavailable views supply no watermark. Unloaded registered conversations
use `Store.UnloadedLastShownVersion` only when clean, validated certificate
progress exactly equals independently known current raw history progress
(V = H); see [certificate eligibility](thread-package-cache-and-epochs.md#scalar-certificates-and-unloaded-metadata).
Unregistered or removed IDs, stale/invalid evidence and unknown progress supply no watermark.
Eligible zero is authoritative, including explicit empty evidence at V = H = 0.
The lookup starts no worker, traverses/copies no items and writes nothing;
unloaded reads open at most one certificate and no history segment, regardless
of history size. This bound applies to `ThreadLastShownVersion`; the list
handler's separate [legacy watermark scan](history-package-watermarks.md#unread-state-uses-a-separate-lazily-recovered-watermark-2954)
can still read history. The no-relay composition
continues folding without wire publication; surviving legacy publication retains
its existing access gates, without restoring retired interactive or agent content.

Reconnect skips event-ring replay/resync only for negotiated thread peers.
Each open conversation requires explicit catch-up from its last completed
epoch/version, or a zero-version newest window, with explicit older pages as needed.
Only all assembled batches followed by the separate successful `final: true`
marker certify completion; reset, error, cancellation, disconnect and incomplete
fragments leave the checkpoint unchanged. Live publication racing an unfinished query
can cancel it; retry from the last completed checkpoint to converge. Stable-history idle-reload
fixtures must install the publisher before discovery/initial retirement so its
initial cursor exists before the phone connects. Otherwise first live publication
can cancel the catch-up legitimately, masking the final-response assertion;
`TestDaemonThreadDemandWireAfterIdle` preserves the final, version-1, one-row proof. See
[query/live ordering](../../protocol-mobile.md#querylive-ordering).

Each conversation coordinator owns one publication cursor, distinct from a
client's certified recovery checkpoint.
`daemonThreadPublisher.publish` checks `Store.Status` first; an unchanged usable
epoch/version returns without reading or traversing items/content. Changed
publication follows proven `Store.Changes` ranges. Initial and replacement
acquisition use `Store.Changes` with an empty epoch to capture usable metadata
without copying or broadcasting historical items. Subsequent proven changes
continue live; each client initializes through explicit bounded catch-up and
older paging, including clients already connected at acquisition.
`BaselineRequired`, epoch change, a withdrawn usable view or encoding failure
retires recorded recipients and currently eligible consumers of the affected
conversation through `RetireLiveState`, then forgets and reacquires the boundary.
Tracking only successful live pushes misses clients initialized solely by queries.
Retirement closes the connection before dependent output can escape; existing
interactive/thread and agent eligibility gates still apply. Same-epoch reload
preserves live continuity only with a validated clean checkpoint matching the
saved successful retirement boundary and a contiguous, representable suffix.
The Store reconstructs complete changes and compact root evidence from that
boundary; equality of epochs alone proves no range. Genuine gaps still retire
consumers, which reconnect and explicitly catch up or acquire a newest window
after reset. Acquisition never sends a full live inventory.
Status is only a hint, not continuity evidence or a transaction across reads.
Wire metadata, changes and cursor advancement must all come from the same
captured `Changes` result. Recheck captured usability and retire
consumers before replacement even if withdrawal/reload races the status read.
See [observation readiness](thread-package-background-store-replay.md#loading-and-replay-readiness)
and [relay gap quarantine and recipient gates](v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md#thread-delivery-and-summary-projection).

Idle retirement must preserve the successful publisher boundary, not merely a
usable same-epoch source. A follower can consume the second accepted turn after
publication but before the coordinator finishes checking quiescence. Unconditional
unload then discards still-valid retained changes ahead of the publisher cursor.
Clean reload can retain the epoch while reconstructing only from the newer
retirement boundary; `Store.Changes` rejects the publisher's older range and
`daemonThreadPublisher.publish` retires the original peer with `baseline_required`.
The controlled regression proves this missing-range boundary without requiring
an unusable capture, unproven fold coverage or public-row withdrawal. The gap
reason alone does not establish which boundary failed in another recording.

`threadShadow.unloadIdle` holds the publication gate and requires the captured
status epoch/version to equal the successful publisher cursor before calling
`Store.UnloadIfCurrent`. Without a publisher, the captured status supplies the
boundary. Under `Store.mu`, conditional unload requires that exact inactive,
usable publication and no admitted feed/capture/publication transaction. Snapshot
equality alone is insufficient: a chunk paused before folding still holds the old
snapshot but can advance progress before cancellation. Chunk admission marks
processing under the same mutex, and publication clears it. A declined retirement
keeps the worker and retained ranges; following publishes pending progress before
retrying. Only successful retirement records the idle/unloaded boundary. Query
demand fencing and forced `Store.Unload` keep their existing contracts.

`TestDaemonThreadRepeatedTurnRetirement` drives real accepted-send history,
the follower, Store and daemon publisher through coalesced and separated source
schedules held between publication and quiescence. It proves valid `Changes`
before retirement, then publication through clean idle unload/reload on the
original peer. Applying live rows must match raw-history replay, with stable
item identity/text/revisions and exactly one second accepted-user addition.
`TestStoreConditionalUnload` holds an admitted chunk before folding to reject
retirement even at an equal snapshot; `TestDaemonThreadIdleCommit` covers progress
arriving during held delivery. The retained
[Linux race evidence](../../../cmd/pyry/testdata/repeated_turn/evidence.json)
pins failing-before and passing-after boundaries, schedules, source revisions,
commands and counts without conversation content or credentials. Offline
`TestRecordedClaude/two-turn` retains matching acknowledgements and source/session-owned
terminal idle through `runTapeTwoTurn` and `recordedScenarioContext.turn`, without
the former producer-gap skip. These proofs preserve valid continuity; actual
epoch/range loss, published-row suppression and unavailable-source retirement
remain fail-closed, and reconnect still requires explicit named catch-up.

For journal diagnostics, each `daemonThreadPublisher.gap` invocation emits one
Warn with message `relay: thread gap`, event `daemon.thread.gap` and a fixed
`reason` identifying the rejecting branch:

| Reason | Trigger |
| --- | --- |
| `state_not_usable` | `publish` finds unusable status while a publication cursor exists. |
| `baseline_required` | The captured `Changes` result is unusable or requires a baseline. |
| `compact_reset_required` | The captured `CompactChanges` result is unusable or requires a reset. |
| `encode_failure` | `deliver` cannot encode an update or decode an encoded envelope. |

The record contains only the fixed event/reason fields and normal log metadata
(time, level and static message). It includes no history/content, payload bytes,
credentials, filesystem paths, peer/conversation IDs or raw errors. This warning
replaces `daemon.thread.encode_err`, so encoding failure emits one gap warning.
An unusable status without an existing publication cursor stays silent.
Logging preserves recipient selection, retirement and metadata-only cursor
recovery: `Changes` rejection attempts replacement acquisition in the same
pass; status, compact and encoding gaps leave acquisition to a later pass.
Recovery never broadcasts a historical baseline; clients use authenticated
catch-up and paging as described above.

Recipient projection is per connection and conversation. `ActiveThreadConns`
captures `ActiveConn.View` and `PendingView` together under `hostedMu`, with
the negotiated `ThreadCompact` flag. Complete-only deployments do no
`CompactChanges` work. Unjoined compact-capable peers participate in both
families' query cancellation, but the relay withholds compact frames until a
compact join exists. During a projection switch, both completed and pending
families participate. Selecting only the completed join loses updates: the
other family's frame cannot cancel a stale pending capture, which could then
certify while the publisher cursor moves past the change. A later query
admission crosses the publication gate before Store capture. Relay membership,
projection and cancellation checks remain authoritative immediately before
sealing; the daemon snapshot grants no content access. See
[query/live fencing](../../protocol-mobile.md#querylive-ordering).

For compact recipients, an advanced same-epoch observation permits one
`Store.CompactChanges` call under the operation context. Calling when the
cursor equals the current version would block; status and captured progress
must establish advancement first. Each interval yields one final replacement
per affected public root (tool/agent header or full non-tool/agent item), or
one consumed-progress update if no root changed. `threadShadow.follow`'s
existing 50 ms publication tick is the flush schedule, plus the pass's work.
The following changed pass flushes final state after activity stops, and an
unchanged pass emits nothing. Pending compact work is proportional to affected
roots, with no per-hidden-event queue. Oversized full root items can still
fragment. Complete update content and order remain unchanged.
Compact read errors leave the cursor for retry; unusable/reset results retire
through `gap`/`RetireLiveState`, as do complete continuity failures. Neither
acquisition nor retirement publishes a baseline. Compact recovery remains an
explicit query, even when compact catch-up can survive a lost complete
publication range. The publication gate and publisher wait group own both
families; publisher stop cancels and joins in-flight publication and reads.
Publication prunes disconnected tracked recipients before checking compact
continuity. Compact-gap retirement therefore excludes those already-pruned
peers while still including currently eligible query-only consumers; expecting
every previously tracked peer to retire would change existing recipient behavior.

`daemonThreadUpdates` maps exported fold field names through `ThreadItem`'s JSON
tags. Copying `EndedOrder` or `NoChild` directly would produce unusable wire
patches instead of `ended_order` or `no_child`. A combined field/text change
emits one atomic `thread_item_changed` containing all replacements and complete
`Change.ResultContent`, with the suffix already included. Suffix-only changes
emit one `thread_text_append`. Both use `PreviousRev` as `base_rev` and the actual
resulting `Rev`, requiring held-revision equality and `rev > base_rev`. A patch
followed by an equal-revision append would truncate text for strict clients;
inventing an extra revision would diverge from full-item reads. The
[internal observation contract](thread-package-background-store-replay.md#loading-and-replay-readiness)
still applies `Fields` before `TextAppend`; the wire replacement is already final.
Every logical update uses `EncodeThreadUpdate`, with consecutive continuation
parts before the next update. Discovery and following each wait at most 50 ms;
store batches coalesce text without an additional timer. Ordered updates flush
before dependent non-text changes, adding at most 100 ms of scheduled waiting
within the total 250 ms publication/coalescing budget. This bounds waiting to
enqueue, not replay, encoding or network delivery time. See the
[wire assembly contract](../../protocol-mobile.md#bounded-thread-encoding-and-assembly).
