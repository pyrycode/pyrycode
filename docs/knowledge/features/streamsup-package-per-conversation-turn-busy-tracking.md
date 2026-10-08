# Per-conversation turn-busy tracking (#1201)

`cmd/pyry/stream_turn_busy.go`'s `turnBusyTracker` (`newTurnBusyTracker(resolve, logger) *turnBusyTracker`,
`Busy(conversationID string) bool`, `WaitIdle(ctx, conversationID) error`) is a self-synchronised,
per-conversation set of conversations with an open turn on the stream-json path — the answer the delivery
path will need ("is a turn running for conversation X?") that the emitter's own lifecycle fields
structurally cannot give: those are unguarded (single-Handle-goroutine only), scalar rather than
per-conversation, and populated only for the conversation the cursor points at.

**Fed from `startStreamTurnDrainV2`'s `sink.ch` arm, one line before the event's own conversation is
resolved for `HandleFor`** — a `busy.observe(env.sessionID, env.ev)` call, unconditional, ahead of the
drop that applies when the session resolves to no conversation at all. `observe` resolves
`sessionID → conversationID` via an injected closure (production passes `conversationForSession(w.convReg,
sid)` — since #2739 the *same* resolver the drain itself uses to attribute the event to `HandleFor`, and
the one `session_transition` frames use), keyed by conversation (not
session) so a `/clear`-rotated session's late events still land under `SessionHistory`'s match. An
unresolvable or empty-string conversation id is simply not tracked (never under an empty key — that would
both wedge and collide with the "unknown conversation" answer). Before #2739, when the drain fed a
*different* gate — `activeSession()`, the active conversation's bound session — ordering mattered more
sharply: feeding `observe` ahead of that gate was what let a turn on a non-active conversation still
report busy, while the emitter it fed only ever saw the active one's events. Now both lanes resolve
through the same function, so the two agree on which conversations exist; `observe` still runs first so
its own unbound-session accounting stays independent of the drain's `stream_turn.no_conversation` drop,
each diagnosing its own lane rather than one silently standing in for the other.

The opener set is a **whitelist**: empty-parent `ThoughtChunk`/`ThinkingProgress`/`TextChunk`/`ToolStart`/`ToolUpdate`
add the conversation, `TurnEnd` (either stop reason — `resultTurnEndReason` sends both through one parser
arm) deletes it, and everything else (`Stall`/`ApiRetry`/`Compacting`/`Unrecognized`, and any future
variant) is a no-op. `ThoughtChunk` includes the empty event produced by a valid partial-message
`thinking_delta`; `ThinkingProgress` is the independent numeric observation. Either is authoritative
server evidence that the main model has entered an interruptible turn when its parent is empty,
unlike a client's optimistic message echo. Empty-parent thinking remains an opener, including
the first thinking event of a notification-started main turn; the task notification itself is
lifecycle-neutral, and the main `TurnEnd` still closes that turn.

**Parent-attributed thinking affects neither the main busy mark nor its published lifecycle (#2936).**
`emitAssistant`, `emitStreamEvent`, and `emitThinkingProgress` copy the emitting line's validated
`parent_tool_use_id` into `ThoughtChunk.ParentToolCallID` or `ThinkingProgress.ParentToolCallID`.
Attribution is line-local: missing, invalid, or over-cap values become empty through `parentToolUseID`;
they never inherit a previous line's parent. Partial thinking stays content-free, and numeric progress
keeps its values and rate bound. `turnMarkFor` returns `turnMarkNone` before tracker mutation, so
parented thinking cannot reopen an idle mark, change an open one, or retire
[Send now carry or release grace](streamsup-package-per-conversation-turn-busy-track-send-now-carry.md).
The tracker retains no thinking content, parent id, or timestamp. Independently,
`interactiveTurnEmitterV2.HandleFor` ignores parented thinking before starting a turn, flushing
buffered text, changing phase, or publishing `thinking_progress`. Fixing only busy classification
would leave clients showing a synthetic thinking turn: these are separate state stores.
See the [wire contract](../../protocol-mobile.md#thinking_progress).

**Since #2781, `TextChunk`/`ToolStart`/`ToolUpdate` are openers only when they carry no existing
`ParentToolCallID`.** A subagent's own text and tool activity is marked with the spawning call's id by
the producer that built it; `turnMarkFor` and `toolCallDeltaFor` both read that field now (`turnMarkFor`
returns `turnMarkNone` for a parent-attributed event instead of `turnMarkOpen`, and `toolCallDeltaFor`
returns the zero delta instead of recording the child call), so neither can reopen an idle main mark,
change an already-open one, or add a child's tool call to `inflight`. A child `ToolUpdate` whose
`ToolCallID` collides with the spawning call's own id is still inert: the collision is attributed to the
child, not the parent, and does not remove the retained top-level call. The top-level Agent/Task call that
spawns a subagent is itself an empty-parent `ToolStart`/`ToolUpdate` pair, so it keeps opening the turn and
keeps being retained in `inflight` until its own top-level result or the main `TurnEnd`. The spawning
call still marks the main turn busy; parent-attributed child chatter cannot prolong that mark after
the main turn ends. Before this, a subagent's events arriving after the main `TurnEnd` reopened the mark with
nothing left to close it, holding every later queued message for that conversation; an interrupt made the
hold permanent because interrupting the child produces no further top-level `TurnEnd`.

Opening the busy mark during initial thinking is intentional. `startStreamTurnDrainV2` calls `observe`
before the event's conversation is resolved for `HandleFor`, and so before
`interactiveTurnEmitterV2.HandleFor` publishes `turn_state: thinking`. Inbound delivery therefore parks in
`msgqueue` before a client can react to the state frame, instead of racing a second message into the
child's stdin. A different conversation keeps its own membership and published state.

**The whitelist has now been vindicated by a real case.** `Stall`/`ApiRetry`/`Compacting` are tui-driver
signals this sink's only producer never emits, so they are asserted at the unit tier only, fed directly.
`Unrecognized` is different: it **is** reachable — the parser emits it for any claude output outside the
measured known-ignored list — and it reached this tracker correctly **without one line of change here**,
because a new variant falls to the default. A blacklist ("anything that isn't `TurnEnd` opens a turn")
would be behaviourally identical through the older sink and would have wedged every conversation that met
an unknown message: the turn would open, and no turn end would ever follow, because we could not
understand the message that opened it.

Concurrency: one mutex guards one `map[string]struct{}` plus a `chan struct{}` "generation" broadcast,
closed-and-replaced under the same lock as any membership mutation. `WaitIdle` captures that channel and
re-checks membership under one lock acquisition (splitting the two reintroduces a lost-wakeup race), then
selects on it against `ctx.Done()`. `Busy`/`WaitIdle` are callable from any goroutine; `observe` is called
only from the drain goroutine and inherits its single-writer invariant, though the type is self-synchronised
regardless. Existence-oracle discipline (#1101 posture): `Busy`'s signature is `bool`-only — no error, no
second `found` bool — so a foreign conversation id is indistinguishable from an idle one in both value and
code path.

**Wait observability must use the wait's own membership check (#2782).** A separate `Busy` pre-check
can see idle just before a turn opens, then let the wait park silently. `waitIdle` keeps the membership
read and generation-channel capture under one lock and invokes its optional `onPark` hook once, after
unlocking and before the first `select`. Logging under the lock would stall tracker mutations on slow
log I/O. `waitIdleForDelivery` supplies the
[delivery-hold INFO record](streamsup-package-per-conversation-turn-busy-track-delivery-seam-consumer-mid-turn-hold.md);
`WaitIdle` passes nil, keeping reset and context-usage callers silent.

**Repeated-wakeup tests must let the waiter re-park.** In
`TestTurnBusyTracker_WaitIdleForDeliveryLogsHoldOnce`, opening and closing another conversation and
then clearing the target without a scheduling gap can collapse into one wakeup: the waiter sees idle
and returns, so even a record emitted on every busy iteration passes. The test first observes the hold
record, proving the generation channel was captured, then leaves grace windows between transitions.
Removing the once-guard then produces three records and fails the test. Those windows allow scheduling;
they do not guarantee it under every load. The test pins observable behavior, while source review must
also check that logging uses the wait's membership read: a separate `Busy` pre-check can pass the same
test while retaining the silent-park race. See [the review](https://github.com/pyrycode/pyrycode/pull/2787#issuecomment-5984513630).

**Shipped unwired.** At #1201's landing no production caller read `Busy`/`WaitIdle` — `observe`'s
`nil`-receiver no-op is what let the 7 pre-existing drain-test call sites take a bare `nil` for the new
parameter instead of each constructing a tracker. That is no longer true: #1199 (below) is the
inbound-delivery consumer both readers were built for. The parameter stays the concrete `*turnBusyTracker`,
never an interface (a typed-nil in an interface field would be non-nil at the interface level and route
past the nil guard into a nil-map read — the `screenSnapshotterOrNil` hazard).

Opening on thinking exposed a second state store that an ordinary busy-clear
test does not exercise: `turnBusyTracker` is per-conversation delivery state,
and `interactiveTurnEmitterV2` holds its own per-conversation published
lifecycle (retained by conversation and producing source since #2981, formerly
one `convTurnState` per conversation after #2739). Clearing only the
former on child exit leaves clients reporting `thinking` and can let a respawn
reuse a stale turn identity even though delivery is no longer busy. Every
abandonment path must therefore close both views.

A decoded `result` is the ordinary shared closer: every subtype produces one
`TurnEnd`; `error_during_execution` becomes cancelled, and success or another
error keeps its existing terminal reason. The tracker consumes that event before
the event's conversation is resolved for `HandleFor`, while the emitter publishes
`turn_end` with its producing source and clears that source's turn identity.
The conversation phase preserves or restores another running source, publishing
`turn_state: idle` only when none remains; see
[phase projection](streamsup-package-draining-turnevents-into-the-interactive-emitter.md).

The no-result paths close on the drain that already owns emitter mutation. A
child-exit envelope is FIFO behind that child's events; `clearForExit` returns a
conversation only when its exit epoch passes the stale-exit guard, and only then
does the same drain call `closeForConversation`. Pool teardown first clears the
busy membership, then records the daemon-resolved conversation in a protected,
coalescing pending set and wakes the drain. The set, rather than the bounded wake
token, owns the requests, and the drain applies them before a later child event.
An external close publishes only `turn_state: idle`; it does not invent a
`turn_end` result the child never emitted. A stale or unresolved exit closes
neither view. Result, accepted child exit, and pool teardown are consequently
total over turns opened during thinking without allowing one conversation's
close to mutate another.
