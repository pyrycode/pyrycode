# Relay handlers: conversation read marks

Part of the [relay handlers overview](relay-package-handlers.md).

## `MarkConversationRead` cannot copy `SetConversationMuted`'s best-effort `Save` (#2780)

`MarkConversationRead` (`mark_conversation_read.go`) raises `Conversation.ReadUpTo` (#2779) through `Registry.AdvanceReadUpTo` ([`conversations-registry-crud.md`](conversations-registry-crud.md) § `AdvanceReadUpTo`), which — unlike every other setter this package calls — persists the advance itself, reverting on a failed save. **The template this handler would naturally copy, `SetConversationMuted`'s "mutate under `mu`, then a separate best-effort `Save`," cannot satisfy this ticket's AC.** If the mark advanced in memory and the caller's own subsequent `Save` then failed, a client retrying the identical request would find the mark already at the value it's asking for and read that as a no-op: it would reply with success and never actually persist or push the advance. The registry method has to own both the compare and the persist, with the lock held across the revert, for a retry to mean anything after a save failure.

Resolution order matters for the same reason `ListConversationsWithAgents` fixed in #2779: `reg.Get` runs **before** `hist.LatestDisplayableEntryID`, so an unknown or empty `conversation_id` never reaches the history store and never reads as `history.unavailable`. The same typed-nil `*history.Store` normalization that handler's constructor applies is repeated here rather than shared — a second one-line guard, not a factored helper, since the two call sites don't share a constructor.

Refusal-to-code mapping has one branch worth citing by name: a *concurrent delete* between `reg.Get`'s success and `AdvanceReadUpTo`'s own scan surfaces as `AdvanceReadUpTo`'s `ErrConversationNotFound`, and the handler re-maps it to the same `conversation.not_found` the initial miss gets — truthful, not a generic "something changed" code, because by the time the reply is built the row genuinely isn't there anymore.

Production list/read share `legacyHistoryReader` and the producer's
`historyEntryShown(type, payload)` content policy (#3463). Explicit `Shown == false`
vetoes content; absent metadata uses the same classifier, and true cannot promote
metadata or unknown types. Unknown types default to non-content. Hidden statuses,
context usage, successful endings, nonvisual info banners and restart/sleep
dividers do not raise the target. Receipt validity establishes accounting only.
Empty/hidden-only logs yield `0`. Mark-read persists
`max(held, min(up_to, latest))`, preserving higher held marks even at latest zero.
Reading the newest visible ID clears unread despite hidden tails; older reads
leave newer content unread. Hidden restart reconciliation preserves that result;
visible interruption/loss notices may raise unread. All IDs remain in the durable
history space. See [watermarks and accounting](history-package-watermarks.md#unread-state-uses-a-separate-lazily-recovered-watermark-2954).

Reply-then-push-then-return-the-reply's-error is the same ordering [`SetConversationMuted`](relay-package-handlers.md#setconversationmuted-reuses-send_messages-auto-naming-push-2572) uses, with one difference: the push is conditional on `advanced`, not unconditional. A no-op request still gets its correlated `conversation_updated` reply — a client that asked always learns the mark actually on disk — but nothing is pushed to anyone else, because nothing changed for them to learn.

**Test lesson: comparing two `ConversationUpdatedPayload` values with `==` fails even when they're logically equal, because `Name` is a `*string`.** Two independently-read copies of the same stored row carry two distinct pointers to equal strings (or two `nil`s, which do compare equal — the trap only bites once a conversation has a name). The handler's tests compare the **marshalled JSON bytes** of the replied and the pushed payload instead, which is also what proves they're reporting the literal value now on disk rather than two slightly different snapshots.
