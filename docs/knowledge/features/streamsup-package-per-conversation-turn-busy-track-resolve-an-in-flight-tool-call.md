# Resolve an in-flight tool call to its conversation (#1917)

`turnBusyTracker` gained a third feed: `inflight map[string]map[string]struct{}`, keyed
conversation-then-`tool_use_id`, fed by `toolCallDeltaFor` (a pure classifier reading
`ToolStart`/`ToolUpdate` only, mirroring `turnMarkFor`'s discipline of switching on the Go
variant, never a field value) and applied inside the existing single `setBusy` acquisition.
`ToolCallInFlight(conversationID, toolCallID) bool` answers membership only, extending
`Busy`'s existence-oracle posture to a two-key question so neither id can be inferred from the
other. Nothing reads it yet — #1919 is the intended consumer. See
[Session-teardown clear](streamsup-package-per-conversation-turn-busy-track-session-teardown-clear.md)
and [Exit lane on the turn-busy fan-in](streamsup-package-per-conversation-turn-busy-track-exit-lane-on-the-turn-busy-fan.md)
for the other two feeds this one's close arm rides.

**A distinct-id independence fixture cannot tell nested retention from a flat map — only a
same-id collision across two conversations can.** The shape exists to keep a claude-derived key
(`tool_use_id` is child-supplied) from letting one conversation's child overwrite another's
entry: under a flat `map[toolCallID]conversationID`, a hostile or confused child on
conversation C capturing an id genuinely in flight on A **overwrites** A's entry, flipping
`ToolCallInFlight(A, id)` to false on C's say-so. A test asserting each of several *distinct*
ids reports correctly for its own conversation and not the other passes identically under both
the nested map and the flat one — the collision is the only input that separates them, and
nothing else in the suite supplies one. `TestTurnBusyTracker_CollidingToolCallIDStaysConfinedToItsConversation`
is the fixture that does: two conversations open a call under the *same* id and one conversation's
close leaves the other's answer untouched. Generalizes past this ticket — any per-key-scoped
membership store whose key is supplied by the thing being scoped against needs a same-key,
cross-scope fixture, not an independence fixture over distinct keys.

**`setBusy` applies the tool delta before its early-return, not after.** The membership
comparison (`open == was`) short-circuits on every `ToolStart` after a turn's first, because the
conversation is already busy — so a delta applied after that point would only ever be captured
for one tool call per turn, and claude's parallel `tool_use` blocks make a second concurrent
call routine, not an edge case.

**The empty-tool-call-id refusal lives in `setBusy`, not in `toolCallDeltaFor`, and the
classifier does not normalize around it.** `toolCallDeltaFor(turnevent.ToolStart{})` (no id)
returns `{id: "", started: true}` — not the zero value `toolCallDelta{}` — because the id alone
is the delta's discriminant and `started` carries no meaning without one. The refusal
(`tool.id != ""`) is a single check at the one mutation point, not a normalization the classifier
performs on its own output. This is production-reachable, not defensive: `emitAssistant` copies
a `tool_use` block's `ID` into `ToolCallID` with no non-empty check, so a block omitting `id`
yields exactly this shape. Without the refusal, `ToolCallInFlight(conv, "")` would answer true,
turning the report into a conversation-existence oracle keyed on a value a child controls by
simply omitting a field.

**`toolCallDeltaFor`'s completeness today depends on a different function's classification of
the same variant.** `turnevent.BackgroundTaskStarted` also carries a `ToolCallID` (its own field
doc says so: "the same identifier ToolStart and ToolUpdate already carry under this name"), so a
`case turnevent.BackgroundTaskStarted: return toolCallDelta{id: e.ToolCallID, started: true}`
added to `toolCallDeltaFor` would pass every existing test — it is inert in production only
because `turnMarkFor` classifies that variant `turnMarkNone`, so `observe` returns before
`setBusy` is ever called. `TestToolCallDeltaFor_ClassifiesToolVariantsOnly` now runs against
`turnEventVariants(t)` (the same totality guard `TestTurnMarkFor_TotalOverEveryVariant` uses) so
a future variant is required to declare itself here rather than fall silently into the default
arm — a second classifier reading the same event stream should not rely on a sibling's routing
to stay correct. If `turnMarkFor` ever starts treating `BackgroundTaskStarted` as an opener,
`toolCallDeltaFor`'s handling of it needs revisiting at the same time, not after.
