# Relay handlers: conversation list history

Part of the [relay handlers overview](relay-package-handlers.md).

## `ListConversations` gains a third, required parameter: the history dependency is not optional (#2779)

`ListConversationsWithAgents(reg ConversationLister, harnessFor SessionHarnessFunc,
hist historyLatestReader)` requires history; `ListConversations(reg, hist)` delegates
with a nil harness reader. A nil harness is a legitimate no-pool answer, but a
missing history reader cannot establish unread state and fails the whole reply
with `history.unavailable`. `startRelayV2` supplies the same `legacyHistoryReader`
over `w.hist` to list and mark-read, keeping their visible-content targets aligned.

**`historyLatestReader` requires `LatestDisplayableEntryID`, not raw `LatestEntryID`, through a consumer-owned, one-method interface** — the plan's own revision from the architect's first sketch. `*history.Store` satisfies it structurally; existing handler tests built around a synthetic registry keep using small fakes that return whatever id they want (including a non-canonical one, since the interface itself doesn't reject it — only the real store's `ValidID` check does), which is what keeps 17 pre-existing tests from having to stand up a real `history.Store` just to pass the new parameter.

**A nil `*history.Store` boxed into a `historyLatestReader` is not a nil interface — this is the trap the PR's own lessons-learned flagged, and the same one `history-package.md`'s "the store is nil-tolerant and a concrete pointer, never an interface" note already warns about for `appendConversationHistory`.** `ListConversationsWithAgents` normalizes it explicitly: `if store, ok := hist.(*history.Store); ok && store == nil { hist = nil }` runs before the handler closure is even built, so a daemon that wires a typed-nil store — including for an otherwise-empty registry that would never reach the per-row lookup — still answers `history.unavailable` rather than silently succeeding. Without this normalization, `hist != nil` (the interface-level check) would be true for a typed nil, and the handler would call a method on a nil receiver or, worse, read a success it shouldn't report.

Missing, empty and hidden-only history yields `latest_entry_id: 0`; the shared
producer content policy and explicit-false veto match [`MarkConversationRead`](relay-package-handlers-mark-conversation-read.md).
Raw store watermark APIs and legacy receipt delivery/accounting remain separate.

**The filtered lookup runs only after `multi_agent` filtering, and the whole reply is built before anything is sent.** A row a non-negotiating client's filter already dropped (an `AgentCodex` conversation) is never looked up at all, so a corrupt or unreadable history log behind a filtered-out row cannot fail a list that client was always going to receive in full. On a surviving row, a lookup error returns `history.unavailable` (retryable, the fixed message `msgListHistoryUnavailable`) immediately — there is no partially-built `ConversationsPayload` sent before or after the failing row, because the `out` slice is only marshalled and sent once the loop finishes without error.
