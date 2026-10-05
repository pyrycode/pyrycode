# Class-aware fan-in reserve (#1496)

`sinkFor`'s drop-newest was, until #1496, blind to class: on a full 256-slot channel it dropped whichever
envelope arrived next, `turnevent.TurnEnd` included. `pushQueue.enqueue` (`internal/relay/v2session_modal.go`)
— the precedent `sinkFor`'s own comment cited — is deliberately class-aware and never drops a control
event, per ADR 025 § Backpressure's *"control events (`modal_shown`, `turn_end`, `tool_*`) never drop."* The
fan-in was the one place in the stream-json path that promise didn't hold, and because `turnBusyTracker.observe`
has no `TurnStart` — only openers and a `TurnEnd` closer — a dropped `TurnEnd` didn't just lose one event of
fidelity, it left the mark permanently open: `waitIdleForDelivery` parks every later `send_message` until
`streamTurnHoldTimeout`, and `msgqueue` eventually gives up with `session_error`. This also meant the "no
reachable sequence leaves a conversation reported busy forever" claim `turnBusyTracker`'s own doc comment
and [codebase/1210.md](../codebase/1210.md)/[codebase/1199.md](../codebase/1199.md) record as SATISFIED was
false at the fan-in the whole time #1201–#1210 were landing — those tickets closed every *clear-side* gap
correctly; the gap #1496 found was upstream of all of them, in whether a `TurnEnd` reached `observe` at all.

**The fix reserves capacity rather than replacing the channel.** A channel producer can't inspect or remove
a queued element without receiving it — mirroring `pushQueue.enqueue`'s slice-and-mutex shape literally was
rejected for exactly that reason (breaks the fan-in's documented sole-reader invariant). Instead,
`newStreamTurnSink` computes an unexported `droppableCap` once (`buf - min(streamTurnSinkCloseReserve, buf/2)`,
32 slots reserved at the production `buf` of 256): the droppable class is refused once `len(s.ch) >=
droppableCap`, and a closing-class envelope always sends against the channel's **full** capacity. The
channel stays strictly bounded — the reserve partitions existing capacity, it adds none — so this is the
opposite trilemma trade from `pushQueue`: `pushQueue` yields strictly-bounded (soft-overflows control,
affordable because #911's per-conn in-flight gate bounds the excursion); the fan-in yields never-drop-control
instead, because its producer is claude's unrate-limited stdout with no analogous gate, and a soft overflow
there would be a memory-exhaustion vector rather than a bounded excursion.

**One classifier, two callers.** `turnMarkFor(ev) (turnMarkNone | turnMarkOpen | turnMarkClose)`
(`stream_turn_busy.go`) is `observe`'s extracted `switch ev.(type)`, now the sole definition of the
open/close split — both `observe` and `sinkFor` read it, so a future `turnevent.Event` variant added to
one side's set and not the other can't silently reintroduce this bug. The closing class is exactly
`turnevent.TurnEnd` and the fan-in's `exit` envelope (`streamTurnEnvelope.exit`, #1209); everything else,
including an unrecognized future variant, falls to droppable — the safe default, since a wrongly-reserved
variant only costs capacity while a wrongly-droppable *closer* is the only misclassification that wedges.

**The reserve does not guarantee a closing slot.** A burst of closing events
can exhaust it. `TurnEnd` still drops and logs `stream_turn.close_sink_full` at
`Warn`, with only event kind and session identity. Ordinary droppable events log
at `Debug`. A retained exit is different: since #2811, a full-queue exit logs
`stream_turn.exit_sink_full` but survives outside the queue, and confirmed
runner stops always use that retained transport. See
[exit-lane retention](streamsup-package-per-conversation-turn-busy-track-exit-lane-on-the-turn-busy-fan.md).

**Retaining a stop needs an output barrier, not just a second queue.** The drain
must publish every successfully enqueued predecessor before closing the old
lifecycle, then apply the stop before later output. Successful enqueue positions
under a leaf sink mutex supply that ordering; queue emptiness and coalesced wake
signals do not. Losing both exit notifications after the reserve fills would
leave durable posts and successor starts held forever. Retention is per
session identity rather than per event, so it preserves a confirmed boundary
without soft-overflowing child-authored event traffic.

Durable posts release after published real completion or an accepted actual
exit/confirmed runner stop that flushes and closes publication. Early pool
eviction instead installs a hold until confirmed shutdown; it no longer
unconditionally clears membership. See
[session teardown](streamsup-package-per-conversation-turn-busy-track-session-teardown-clear.md).
A live child whose `TurnEnd` was dropped may remain held until an actual exit;
stop retention does not synthesize a missing completion or end a live turn.
