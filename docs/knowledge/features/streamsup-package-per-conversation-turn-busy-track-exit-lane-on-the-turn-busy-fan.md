# Exit lane on the turn-busy fan-in (#1209)

Split from #1207 (itself the last child of the #1203/#1198 crash-clear lineage), alongside open sibling #1210. #1206's `Config.OnChildExit` is explicitly *not* a drain barrier: `cmd.Wait` joins the stdout
copier goroutine and `Parser.emit` calls its sink synchronously (`parser.go:231-235`), so by the time the
callback fires, every event the dead child produced has been **pushed** onto `streamTurnSink.ch` — but not
necessarily **drained** by the separate drain goroutine reading that 256-slot buffer. A clear delivered on
any lane other than that channel could land before the drain processes buffered openers the crashed child
already emitted, re-marking the conversation busy with the clear already spent and no further exit coming.
This slice closes that ordering hole by putting the clear signal **on the fan-in itself**: FIFO with a
single reader, so it cannot be overtaken.

That guarantee covers every mark fed **through** the fan-in — `observe`'s, and the exit-driven clear
itself, because both run in envelope order on the single drain goroutine. It does not cover a mark placed
directly on `turnBusyTracker` from outside the fan-in: `openForDelivery` (#1199) writes its mark from the
msgqueue drain goroutine, with no envelope of its own, so an exit still queued behind a busy conversation's
event burst could be drained *after* that mark and clear it. #1483 closes that gap with a second, narrower
guarantee — an exit-lane epoch stamped on each exit envelope and checked against the mark before clearing
— rather than widening this one; see [turn-busy-track-delivery-seam-consumer-mid-turn-hold.md](streamsup-package-per-conversation-turn-busy-track-delivery-seam-consumer-mid-turn-hold.md)
for the guard itself.

That guard also caught a fixture bug worth naming: the shared drain-tier test fixture built its tracker
with no fan-in bound to it, so the four incumbent exit-lane regression tests would have kept passing
whether the #1483 guard was armed or absent — a regression assertion that reads green either way. Wiring
the fixture's tracker to its own sink is what makes those tests exercise the guard at all; an unwired
fixture is not a smaller test, it is a vacuous one.

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

The drain's `sink.ch` arm handles `env.exit` as its **first** statement — ahead of `observe`, the
active-session gate, and `emitter.Handle`. Each position is load-bearing: before `observe`, because an
exit carries no event to route through the event path; before the gate, for the same reason the tracker
itself is fed before it — the gate would otherwise drop a background conversation's exit, and background
is the common case for a crash; before `Handle`, which (combined with the explicit field) keeps `Handle`
structurally unable to receive a non-event. The arm originally called `clearForSession` directly; since
\#1483 it calls `clearForExit(env.sessionID, env.exitEpoch)`, the exit-lane-epoch-guarded sibling described
above. Both still resolve the session and inherit the nil-receiver no-op and the fail-closed
`clear_unresolved` skip on an unresolvable session from the same shared core — no second session→conversation
resolution, no second copy of the membership-mutation protocol — which is why this slice's own drop
diagnostic withholds the conversation id (the sink closure holds no resolver and structurally cannot name
one). `clearForSession` itself (`stream_turn_busy.go`, extended in #1202) survives with its unconditional
semantics for its one remaining caller, the pool teardown feed — see the boundary note above.

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
