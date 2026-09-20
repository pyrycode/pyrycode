# Per-conversation turn-busy tracking (#1201)

`cmd/pyry/stream_turn_busy.go`'s `turnBusyTracker` (`newTurnBusyTracker(resolve, logger) *turnBusyTracker`,
`Busy(conversationID string) bool`, `WaitIdle(ctx, conversationID) error`) is a self-synchronised,
per-conversation set of conversations with an open turn on the stream-json path — the answer the delivery
path will need ("is a turn running for conversation X?") that the emitter's own lifecycle fields
structurally cannot give: those are unguarded (single-Handle-goroutine only), scalar rather than
per-conversation, and populated only for the conversation the cursor points at.

**Fed from `startStreamTurnDrainV2`'s `sink.ch` arm, one line before the `activeSession()` gate** — a
`busy.observe(env.sessionID, env.ev)` call, unconditional, ahead of the existing drop-if-not-active check.
Ordering is the entire contract: the gate gets its identity from a *different* place than `emitter.Handle`
does (`env.sessionID`, tagged at parser construction, vs. the cursor `Handle` reads internally), so feeding
before the gate is what lets a turn on a *non-active* conversation still report busy — feeding after it
would make the tracker just as cursor-blind as the emitter it's replacing. `observe` resolves
`sessionID → conversationID` via an injected closure (production passes `conversationForSession(w.convReg,
sid)` — the same resolver `session_transition` frames use), keyed by conversation (not
session) so a `/clear`-rotated session's late events still land under `SessionHistory`'s match. An
unresolvable or empty-string conversation id is simply not tracked (never under an empty key — that would
both wedge and collide with the "unknown conversation" answer).

The opener set is a **whitelist**: `ThoughtChunk`/`ThinkingProgress`/`TextChunk`/`ToolStart`/`ToolUpdate`
add the conversation, `TurnEnd` (either stop reason — `resultTurnEndReason` sends both through one parser
arm) deletes it, and everything else (`Stall`/`ApiRetry`/`Compacting`/`Unrecognized`, and any future
variant) is a no-op. `ThoughtChunk` includes the empty event produced by a valid partial-message
`thinking_delta`; `ThinkingProgress` is the independent numeric observation. Either is authoritative
server evidence that the model has entered an interruptible turn, unlike a client's optimistic message
echo.

Opening the busy mark during initial thinking is intentional. `startStreamTurnDrainV2` calls `observe`
before the active-session gate and before `interactiveTurnEmitterV2.Handle` publishes
`turn_state: thinking`. Inbound delivery therefore parks in `msgqueue` before a client can react to the
state frame, instead of racing a second message into the child's stdin. A different conversation keeps
its own membership and published state.

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
while `interactiveTurnEmitterV2` holds the active conversation's scalar published
lifecycle. Clearing only the former on child exit leaves clients reporting
`thinking` and can let a respawn reuse a stale turn identity even though delivery
is no longer busy. Every abandonment path must therefore close both views.

A decoded `result` is the ordinary shared closer: every subtype produces one
`TurnEnd`; `error_during_execution` becomes cancelled, and success or another
error keeps its existing terminal reason. The tracker consumes that event before
active-session filtering, while the emitter publishes `turn_end`, then
`turn_state: idle`, and clears its turn identity.

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
