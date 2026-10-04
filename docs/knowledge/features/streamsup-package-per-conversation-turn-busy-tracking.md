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

The opener set is a **whitelist**: `ThoughtChunk`/`ThinkingProgress`/`TextChunk`/`ToolStart`/`ToolUpdate`
add the conversation, `TurnEnd` (either stop reason — `resultTurnEndReason` sends both through one parser
arm) deletes it, and everything else (`Stall`/`ApiRetry`/`Compacting`/`Unrecognized`, and any future
variant) is a no-op. `ThoughtChunk` includes the empty event produced by a valid partial-message
`thinking_delta`; `ThinkingProgress` is the independent numeric observation. Either is authoritative
server evidence that the model has entered an interruptible turn, unlike a client's optimistic message
echo. Unattributed thinking events keep this behavior regardless of the rest of this section: pyrycode
has no attribution field for them today, so a subagent's thinking still opens the main mark.

**Since #2781, `TextChunk`/`ToolStart`/`ToolUpdate` are openers only when they carry no existing
`ParentToolCallID`.** A subagent's own text and tool activity is marked with the spawning call's id by
the producer that built it; `turnMarkFor` and `toolCallDeltaFor` both read that field now (`turnMarkFor`
returns `turnMarkNone` for a parent-attributed event instead of `turnMarkOpen`, and `toolCallDeltaFor`
returns the zero delta instead of recording the child call), so neither can reopen an idle main mark,
change an already-open one, or add a child's tool call to `inflight`. A child `ToolUpdate` whose
`ToolCallID` collides with the spawning call's own id is still inert: the collision is attributed to the
child, not the parent, and does not remove the retained top-level call. The top-level Agent/Task call that
spawns a subagent is itself an empty-parent `ToolStart`/`ToolUpdate` pair, so it keeps opening the turn and
keeps being retained in `inflight` until its own top-level result or the main `TurnEnd` — a background
agent still reads as busy, and only its child's unattributed chatter stops being mistaken for that
busy-ness. Before this, a subagent's events arriving after the main `TurnEnd` reopened the mark with
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

**Shipped unwired.** At #1201's landing no production caller read `Busy`/`WaitIdle` — `observe`'s
`nil`-receiver no-op is what let the 7 pre-existing drain-test call sites take a bare `nil` for the new
parameter instead of each constructing a tracker. That is no longer true: #1199 (below) is the
inbound-delivery consumer both readers were built for. The parameter stays the concrete `*turnBusyTracker`,
never an interface (a typed-nil in an interface field would be non-nil at the interface level and route
past the nil guard into a nil-map read — the `screenSnapshotterOrNil` hazard).

Opening on thinking exposed a second state store that an ordinary busy-clear
test does not exercise: `turnBusyTracker` is per-conversation delivery state,
and `interactiveTurnEmitterV2` holds its own per-conversation published
lifecycle (one `convTurnState` per conversation since #2739, formerly a single
scalar set of fields for whichever conversation was active). Clearing only the
former on child exit leaves clients reporting `thinking` and can let a respawn
reuse a stale turn identity even though delivery is no longer busy. Every
abandonment path must therefore close both views.

A decoded `result` is the ordinary shared closer: every subtype produces one
`TurnEnd`; `error_during_execution` becomes cancelled, and success or another
error keeps its existing terminal reason. The tracker consumes that event before
the event's conversation is resolved for `HandleFor`, while the emitter publishes
`turn_end`, then `turn_state: idle`, and clears that conversation's turn identity.

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
