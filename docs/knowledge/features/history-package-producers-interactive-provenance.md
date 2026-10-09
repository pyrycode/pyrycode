# `internal/history` — interactive provenance

Part of [history producers](history-package-producers.md#producers-2114-2115).

## Interactive provenance (#2981)

Capture the producer before fan-in and retain it through flush.
`newStreamRunnerFactory` and `newCodexRunnerFactory` supply fixed `claude` and
`codex` kinds to `streamTurnSink.sinkForSessionTag`, which reads the daemon routing tag
once per event into `streamTurnEnvelope.source`. The drain resolves that
envelope's session, including historical bindings, before passing its captured
provenance and runtime incarnation to `interactiveTurnEmitterV2.handleForSource`;
`HandleFor` retains the incarnation-zero entry surface. Unresolved sessions still
drop. Neither the active cursor nor subprocess fields supply attribution.
An append-time `CurrentSessionID` lookup would assign a delayed predecessor's
event to its successor. These IDs are daemon routing IDs, distinct from
conversation IDs and Codex thread IDs.

`convTurnState` retains provenance by value, keyed by conversation, agent kind
and session ID, plus producer incarnation when runtime history is enabled.
Main/child buffered text, child lane identities and lifecycle
state therefore keep their source through timer flush, rotation and closure.
Reused message or parent IDs cannot merge different
sources. Separate buffers alone would still join old-source text across an
intervening successor event and reorder timer output: `HandleFor` flushes the
preceding source before selecting another, leaving at most one text buffer per
conversation. Other conversations' buffers remain independent.

**Project conversation phase separately from source lifecycle.** `turn_end`
keeps its producing source, but `turn_state` describes the most recent retained
main-phase activity in the conversation. Ending another source preserves the
running projection; ending its owner restores the latest remaining running
phase with that source's provenance. Repeated activity in the same phase
updates ownership before conversation-level deduplication. Deduplicating only
within each source would let a delayed predecessor end clear the successor's
reconnect snapshot, while subsequent responding chunks silently skip repair.
The compatibility conversation-wide close processes older phases first and emits
one final idle without inventing a `turn_end`. Runtime exits and boundaries close
only the captured dying source, preserving replacement work and other sources.
See [connect-time phase reconciliation](v2-session-manager-state-machine-connect-time-turn-phase-reconcile-running.md)
and [retained emitter state](streamsup-package-draining-turnevents-into-the-interactive-emitter.md).

`appendConversationHistory` combines optional captured provenance with existing
visibility metadata. Raw `Store.Page` exposes it in warm and reopened stores.
Omitted or unknown source stays absent, never inferred as `none`; legacy entries
remain untagged and are not rewritten. Nil/failed storage still permits eligible
legacy delivery with absent history identity.

Relay correlation identity is the authenticated pairing's bound Noise public
key (`dispatch.Conn.Auth().StaticKey`), in its existing lowercase-hex form,
separate from display name and the verbatim app message id (#2971).
Names can change or coincide; distinct keys distinguish senders with equal
names and app ids, while renaming or reconnecting the same install preserves
identity. Missing authentication or an empty key yields empty identity, with
no fallback to name, connection id, token or token hash. Client-authored fields
cannot override it, and identity and credentials stay out of logs.
`handlers.SendMessage` offers this metadata through the optional
`EnqueueIdentified` path; legacy-only queues still receive `EnqueueSent`.
The production `suggestionEnqueuer` wrapper and daemon/history adoption belong
to [#2972](https://github.com/pyrycode/pyrycode/issues/2972). This seam adds no
deduplication, new wire field or sender identity to legacy event payloads.
See [relay sender metadata](relay-package-handlers.md#send_message-grows-a-ninth-seam-sender-identity-and-tap-time-2704)
and [ADR 042's item model](../decisions/042-daemon-built-thread.md#item-model).

Channel posts are a fourth writer: `channelDelivery.deliver` bypasses the
common seam and calls `AppendWithMetadata` through `channelDeliveryHistory`
for each assistant delta and its `channelPostTurnEndPayload`. It uses the
same `historyVisibilityMetadata` classifier: post text is shown, completion
is hidden. All history writes must succeed before publication; see
[channel delivery and recovery](control-plane-channel-post-live-delivery.md#live-announcements-bounded-replay-and-durable-recovery).

`channelDelivery.accept` captures session provenance once, after `channelPoster`
resolves or creates the conversation (#2984). `channelPostSession` reads the
resolved conversation afresh through `Registry.Get`, so an earlier name-match
row cannot supply a stale binding. A nonempty `Conversation.CurrentSessionID`
is the daemon session routing ID; `Pool.HarnessFor` supplies its `claude` or
`codex` kind from live or dormant state without starting or reviving a child.
This ID is distinct from the conversation ID and Codex thread ID; client
payloads supply none of these provenance facts. See
[session binding](conversation-session-binding.md) and
[the pool's agent lookup](sessions-package-key-types-pool-settingsfor.md#poolharnessfor-2629).

A known empty binding captures explicit `none` with no session ID. Missing
conversation or agent facts, an unavailable lookup, lookup errors and
unsupported agents leave provenance absent/unknown, without refusing the post
or defaulting to Claude. Acceptance copies the snapshot into
`channelDeliveryPost.Session` and persists it with the pending post. Every newly
appended delta chunk and completion uses that same snapshot through deferred
delivery, partial-write retry and restart, even after rebinding or switching
agents. Loading validates a captured kind/ID pair but never looks up the current
binding: older pending records stay absent, and explicit `none` stays `none`.
Raw `Store.Page` exposes the saved metadata in warm and reopened stores; legacy
history remains untagged and is never inferred or rewritten.

**Reconcile payloads independently of metadata.** A stored untagged prefix can
coexist with a pending post's captured snapshot. Requiring provenance equality
would duplicate that prefix. `channelDelivery.deliver` matches chunk and
completion payloads across every raw page, appends only missing entries with
the saved provenance and leaves existing entries unchanged. A fully recorded
post requires cleanup only, with no repeated announcement.
