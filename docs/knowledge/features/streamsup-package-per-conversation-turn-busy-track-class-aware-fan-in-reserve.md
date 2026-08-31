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

**Loss is narrowed, not made impossible — the residual is `Warn`, not `Debug`.** Past the reserve a
closing-class send can still lose the race (`sinkFor` logs `stream_turn.close_sink_full`; `exitFor`,
unchanged by this ticket, already logged `stream_turn.exit_sink_full`), both at `Warn` — content-free
(`event`, `kind` where there's a discriminant to name, `session_id`) and visible at the daemon's default
`LevelInfo`, where the old `Debug`-only record was not. One documented risk: closing-class sends bypass the
watermark entirely, so a single session bursting more `TurnEnd`s than the reserve while the drain is stalled
could in principle crowd out a *different* session's closer — assessed as a narrowed version of pre-#1496
behaviour (today's bug crowds out everything, including the bursting session's own closer) rather than a new
vector, and left undefended since a fix would need the per-session accounting this design exists to avoid.

**Reconciling the "busy forever" claim.** It now holds up to the documented 32-slot reserve rather than
unconditionally: `turnBusyTracker`'s three closing feeds (`observe`'s `TurnEnd`, #1202's teardown clear, #1210's exit lane) are unchanged by this ticket and still jointly exhaustive over *how* a turn closes — what #1496 fixed is that the fan-in itself no longer discards the first of those three before it can be observed,
short of exhausting the reserve. See [codebase/1496.md](../codebase/1496.md).
