# Relay handlers: conversation read marks

Part of the [relay handlers overview](relay-package-handlers.md).

## `MarkConversationRead` cannot copy `SetConversationMuted`'s best-effort `Save` (#2780)

`MarkConversationRead` (`mark_conversation_read.go`) raises `Conversation.ReadUpTo` (#2779) through `Registry.AdvanceReadUpTo` ([`conversations-registry-crud.md`](conversations-registry-crud.md) § `AdvanceReadUpTo`), which — unlike every other setter this package calls — persists the advance itself, reverting on a failed save. **The template this handler would naturally copy, `SetConversationMuted`'s "mutate under `mu`, then a separate best-effort `Save`," cannot satisfy this ticket's AC.** If the mark advanced in memory and the caller's own subsequent `Save` then failed, a client retrying the identical request would find the mark already at the value it's asking for and read that as a no-op: it would reply with success and never actually persist or push the advance. The registry method has to own both the compare and the persist, with the lock held across the revert, for a retry to mean anything after a save failure.

`reg.Get` runs **before** the shown-version callback, so an unknown or empty
`conversation_id` never reaches evidence lookup and never reads as
`history.unavailable`. `startRelayV2` installs the handler after
`installDaemonThreadRelay`, passing the same `ThreadLastShownVersion` callback
used by summary projection. A nil callback or absent reading refuses with
correlated retryable `history.unavailable`, even for an otherwise numerical
no-op; no advance, save or announcement occurs. Present zero is authoritative.

Refusal-to-code mapping has one branch worth citing by name: a *concurrent delete* between `reg.Get`'s success and `AdvanceReadUpTo`'s own scan surfaces as `AdvanceReadUpTo`'s `ErrConversationNotFound`, and the handler re-maps it to the same `conversation.not_found` the initial miss gets — truthful, not a generic "something changed" code, because by the time the reply is built the row genuinely isn't there anymore.

Read marks persist `max(held, min(up_to, last_shown_version))`, preserving higher
held marks even at authoritative zero in the existing durable history-ID space.
Hidden records and completion-only item revisions cannot raise the clamp.
Loaded usable metadata or a fully unloaded clean certificate at independently
known exact consumed V = current raw H supplies authority. Rebuilding,
unavailable and retiring views cannot use older certificates. Lookup starts no
worker, warms no cursor and scans no segments. See
[certificate eligibility and recovery](thread-package-cache-and-epochs.md#scalar-certificates-and-unloaded-metadata).

Mixed list/read fixtures must allow their watermarks to diverge. Lists still
use `legacyHistoryReader`; reusing a legacy list expectation for the read clamp
would let hidden source records remain authoritative despite the provider
change. `testLegacyListAndMark` retains source/list checks while obtaining the
read target from real loaded thread evidence. `TestUnreadThreadHiddenTails`
proves that a completion revision above shown progress cannot raise the clamp,
and a hidden append invalidating unloaded evidence must refuse even a no-op
until authority recovers. `TestMarkConversationReadThreadRestart` distinguishes
a clean certificate from independently known raw progress, including zero,
without loading a worker or changing another conversation's held mark.

Reply-then-push-then-return-the-reply's-error is the same ordering [`SetConversationMuted`](relay-package-handlers.md#setconversationmuted-reuses-send_messages-auto-naming-push-2572) uses, with one difference: the push is conditional on `advanced`, not unconditional. A successful no-op still gets its correlated `conversation_updated` reply carrying the stored mark but saves and pushes nothing. A failed reply cannot suppress an advance's announcement. `threadSummary` independently rechecks shown authority at delivery, replacing or removing only that field while preserving unrelated record fields.

**Test lesson: comparing two `ConversationUpdatedPayload` values with `==` fails even when they're logically equal, because `Name` is a `*string`.** Two independently-read copies of the same stored row carry two distinct pointers to equal strings (or two `nil`s, which do compare equal — the trap only bites once a conversation has a name). The handler's tests compare the **marshalled JSON bytes** of the replied and the pushed payload instead, which is also what proves they're reporting the literal value now on disk rather than two slightly different snapshots.
