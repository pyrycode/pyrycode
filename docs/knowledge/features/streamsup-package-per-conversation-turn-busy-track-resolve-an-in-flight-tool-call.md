# Resolve an in-flight tool call to its conversation (#1917)

`turnBusyTracker` gained a third feed: `inflight map[string]map[string]struct{}`, keyed
conversation-then-`tool_use_id`, fed by `toolCallDeltaFor` (a pure classifier reading
`ToolStart`/`ToolUpdate` only, mirroring `turnMarkFor`'s discipline of switching on the Go
variant, never a field value) and applied inside the existing single `setBusy` acquisition.
`ToolCallInFlight(conversationID, toolCallID) bool` answers membership only, extending
`Busy`'s existence-oracle posture to a two-key question so neither id can be inferred from the
other. #1919's `streamApprovalBridge.ApprovalParked` (`cmd/pyry/modal_resolve_v2.go`) is now the
consumer: it snapshots `byModal`'s parked `tool_use_id`s under its own leaf lock, releases the
lock, then asks `ToolCallInFlight` per id — never nesting the two locks. Nothing outside a test
calls `ApprovalParked` itself yet; #1911 (the delivery hold) is next. See
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

**A membership conjunction across two independently-updated stores has an ordering gap its
misattribution fix does not close.** `ApprovalParked` resolves the conversation on read rather
than stamping it at `Surface` time, which removes the race the design was chasing — a message
enqueued for another conversation moving `activeConv()` out from under a parked approval.
It does not remove a second, narrower gap: the approve lands (claude → `pyry mcp-approve` →
control socket, writing `byModal`) and the matching `ToolStart` is observed (child → parser →
sink → drain, writing `inflight`) on two unordered paths, so a caller can read `ApprovalParked`
in the interval where the correlation exists but the tracker hasn't caught up, and get `false`
for a call that *is* about to be genuinely parked. This is inside `ApprovalParked`'s own
precondition — it answers about an *in-flight* tool call — and it is the fail-closed direction,
so it needed no fix; it needs the next reader of the report to know it's a level that can lag by
one poll, not a value that's true for the whole parked window. #1911 (the delivery hold) is that
reader.

**Two negative arms of an existence-oracle-collapse table can share one fixture row when both
lean on the same conversation's state.** `ApprovalParked`'s test table has a never-observed
tool-call id and a correlation with an empty `ToolUseID` as separate arms, but both can be parked
alongside the same positive control on one other conversation — no second fixture conversation is
needed, because what separates the arms is which lookup inside `ToolCallInFlight` returns false,
not which conversation they're attached to.

**A twin's existence-oracle-collapse fixture is not automatically transferable to a sibling
report over the same store — the direction of the difference matters.** `ApprovalAnswerable`
(#1915), added beside `ApprovalParked` on the same `streamApprovalBridge`, needed the *opposite*
empty-id fixture. `ApprovalParked`'s table plants a `byModal` correlation with an empty
`ToolUseID` and gets away with it only because its own lookup (`ToolCallInFlight`) separately
refuses an empty tool-call id. `ApprovalAnswerable` instead scans `byModal`'s values directly by
equality, so the identical plant would *match* the scan and flip its empty-id row positive —
manufacturing the exact oracle the criterion forbids. The invariant that keeps the row negative
lives at the write side (`permbridge.Register` refuses an empty id before the control server ever
reaches `Surface`), not in an `if approvalID == ""` guard on the read side, which would itself be
the id-specific branch the criterion rules out. Before reusing a sibling's existence-oracle-
collapse fixture, trace *why* its trap case was harmless there — often a downstream refusal in a
different lookup — and confirm the new report doesn't skip that same refusal.
