# Relay handlers: conversation summaries

Part of the [relay handlers overview](relay-package-handlers.md).

## Registry projection and shown-thread authority

`ListConversationsWithAgents(reg, harnessFor)` projects registry snapshots;
`ListConversations(reg)` delegates with a nil harness reader. Neither takes a
history reader or scans source segments. Empty registries and eligible rows
without shown evidence still produce a correlated `conversations` reply.
`latest_entry_id` and the legacy history dependency are retired (#3722).

`AgentOf` resolves the stored session's harness before `multi_agent` filtering.
Clients without that capability receive only Claude rows and no `agent` key;
a nil harness reader defaults to Claude. Surviving rows are ordered by ascending
`LastUsedAt`, then ascending ID, preserving stored bindings, workspace labels,
pins, archive/mute metadata and durable `read_up_to`. Listing changes no marks.

At delivery, `V2SessionManager.threadSummary` independently resolves each row's
`last_shown_version` through `ThreadLastShownVersion`. Only connections that
negotiated `thread` receive the field. Present zero is numeric `0`; unavailable
evidence removes even a supplied stale value while retaining the row and its
other fields. Absence means unknown unread state. With present evidence, unread
is `last_shown_version > read_up_to`. See
[certificate eligibility](thread-package-cache-and-epochs.md#scalar-certificates-and-unloaded-metadata).

**A list must not borrow the read handler's refusal policy.** Missing evidence
withholds a summary field, whereas
[`MarkConversationRead`](relay-package-handlers-mark-conversation-read.md)
must refuse a write until it can clamp authoritatively. A raw cursor or a
legacy content scan would both introduce a second authority and make metadata
listing depend on source recovery.

`TestListConversationsWithoutLegacyHistory` checks empty/nonempty replies,
correlation, retained marks and absent retired/unknown fields on serialized
payloads. `TestListConversationsDurableReadState` checks reopened marks.
`TestDaemonThreadListShownAuthority` delivers repeated negotiated/non-thread
lists with corrupt source segments, certified nonzero/zero and a cold unknown
row together. Every row and held mark survives, no worker starts and unknown raw
progress stays cold. `TestV2Session_ThreadSummaries` separately checks update
projection, stale-value removal and preservation of unrelated supplied fields.
