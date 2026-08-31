# Exit lane on the turn-busy fan-in (#1209)

Split from #1207 (itself the last child of the #1203/#1198 crash-clear lineage), alongside open sibling #1210. #1206's `Config.OnChildExit` is explicitly *not* a drain barrier: `cmd.Wait` joins the stdout
copier goroutine and `Parser.emit` calls its sink synchronously (`parser.go:231-235`), so by the time the
callback fires, every event the dead child produced has been **pushed** onto `streamTurnSink.ch` — but not
necessarily **drained** by the separate drain goroutine reading that 256-slot buffer. A clear delivered on
any lane other than that channel could land before the drain processes buffered openers the crashed child
already emitted, re-marking the conversation busy with the clear already spent and no further exit coming.
This slice closes that ordering hole by putting the clear signal **on the fan-in itself**: FIFO with a
single reader, so it cannot be overtaken.

`streamTurnEnvelope` gains an explicit `exit bool` field — never a nil `turnevent.Event` used as a
sentinel, since `eventKind(nil)` returns `"unknown"` rather than failing (`interactive_turn_v2.go:419-421`),
which would make a missed nil-check silent rather than loud, and would make the exit signal a value of the
same type `Handle` accepts, retiring "Handle cannot receive a non-event" as a type-level fact.
`streamTurnSink.exitFor(sessionID string) func()` mirrors `sinkFor`'s non-blocking `select`/`default` send
— same drop-newest-on-full behaviour — but is a deliberately separate closure with its own diagnostic: the
drop is logged at **`Warn`** (`sinkFor`'s is `Debug`) with exactly `event: "stream_turn.exit_sink_full"` and
`session_id` — no `kind`, mirroring the existing content-free `clear_unresolved` shape. The asymmetry is
the point: a dropped ordinary event is a lost delta, invisible at the default `LevelInfo` on purpose; a
dropped exit is a conversation that (once #1210 wires a producer) stays busy forever, which is degraded
operation and must be visible by default.

The drain's `sink.ch` arm handles `env.exit` as its **first** statement —
`busy.clearForSession(env.sessionID); continue` — ahead of `observe`, the active-session gate, and
`emitter.Handle`. Each position is load-bearing: before `observe`, because an exit carries no event to
route through the event path; before the gate, for the same reason the tracker itself is fed before it —
the gate would otherwise drop a background conversation's exit, and background is the common case for a
crash; before `Handle`, which (combined with the explicit field) keeps `Handle` structurally unable to
receive a non-event. `clearForSession` (`stream_turn_busy.go:240`, extended in #1202) is called **as-is** —
no second session→conversation resolution, no second copy of the membership-mutation protocol — so it
inherits the nil-receiver no-op and the fail-closed `clear_unresolved` skip on an unresolvable session for
free; that is why this slice's own drop diagnostic withholds the conversation id (the sink closure holds no
resolver and structurally cannot name one).

**No new goroutine.** The clear runs inline on the drain goroutine — the same single reader/writer
`observe` already uses — so this feed is serialised against the event feed by construction rather than by
the tracker's mutex. A deferred or goroutine-dispatched clear would satisfy the positive ordering test
(`[opener, exit]` → idle) but fail the negative one (`[exit, opener]` → busy): the test that catches it
barriers on a *third*, later envelope rather than on the absence of an effect, since with the exit arriving
first a goroutine-dispatched clear is a harmless no-op regardless of scheduling (see
[codebase/1209.md](../codebase/1209.md) for the mutation-testing writeup).

**Fired in production since #1210.** `newStreamRunnerFactory` assigns
`streamsup.Config.OnChildExit = sink.exitFor(cfg.SessionID)` one line below the `sinkFor` install
(`streamsup_runner.go`), bound from the same `cfg.SessionID` — which is what keeps the two lanes' session
tags identical by construction. A conversation whose claude child dies mid-turn now returns to idle: no
`TurnEnd` for the abandoned turn and no pool transition are involved, the two feeds that are structurally
silent on that path. The tracker itself stays unread by any delivery path and no v2 frame changed — the
lane closes the crash-clear gap, it does not open a consumer. See [codebase/1210.md](../codebase/1210.md)
for the wiring and its structural (no-runtime-check) ordering argument; [codebase/1209.md](../codebase/1209.md)
for the lane itself.
