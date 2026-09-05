# Draining turnevents into the interactive emitter (#1098)

The turn I/O parser (#1088) emits neutral `turnevent.Event`s from its sink callback, but that sink is
fixed where the runner is **constructed** (`newStreamRunnerFactory`, above), which sees only a
`supervisor.Config` — no handle on `interactiveTurnEmitterV2`, which is built later and separately on the
relay leg. `cmd/pyry/stream_turn_drain.go` lines the two lifetimes up with a late-bound, daemon-singleton
fan-in, and reproduces the PTY path's `startInteractiveTurnStreamV2` `OnEvent`/`FlushSignal`/`OnFlush`
triple **without `turnbridge`** — the Parser already emits `turnevent.Event`, so there is nothing to
un-map back to a `tuidriver.Event` for `turnbridge` to re-map.

```go
type streamTurnEnvelope struct {
    sessionID string
    ev        turnevent.Event
}

type streamTurnSink struct { /* one buffered chan streamTurnEnvelope */ }

func newStreamTurnSink(buf int, logger *slog.Logger) *streamTurnSink
func (s *streamTurnSink) sinkFor(sessionID string) func(turnevent.Event) // non-blocking send; frozen-tag form, delegates to sinkForTag

func startStreamTurnDrainV2(
    ctx context.Context,
    sink *streamTurnSink,
    emitter *interactiveTurnEmitterV2,
    activeSession func() (sessionID string, ok bool),
    busy *turnBusyTracker,
    logger *slog.Logger,
) (cleanup func())
```

**Fan-in, not fan-out.** N per-session Parsers (each fixed at runner construction, one per
`newStreamRunnerFactory` invocation) push `{sessionID, ev}` onto the one buffered channel (256 slots, the
`pushQueueCap` precedent); `startStreamTurnDrainV2` spawns the sole reader goroutine. There is no
session-keyed registry and no subscribe/unsubscribe — the per-conn fan-out stays entirely inside the
unchanged emitter, so this is deliberately *not* shared fan-out infrastructure.

**Non-blocking send, class-aware drop since #1496.** `sinkFor`'s closure runs on claude's stdout forwarder
goroutine (the same one `os/exec` drives the Parser's `Write` from); a blocking send on a full channel would
wedge the child. Before #1496 a full channel dropped the newest event of **any** class, `turnevent.TurnEnd`
included — see [§ Class-aware fan-in reserve (#1496)](#class-aware-fan-in-reserve-1496) below for why that
was an ADR 025 violation and how it's fixed. Today only the droppable class is refused once the channel
reaches `droppableCap`, and only that drop Debug-logs content-free (`event`, `kind`, `session_id` only —
never `ev`'s assistant/thought/tool content). The channel is **never closed** (a Parser may outlive the
drain during shutdown; the drain stops on `ctx`, not on channel close, so a send-on-closed panic is
structurally impossible).

**The per-event session gate is AC2's scoping property.** The drain goroutine resolves `activeSession()`
and forwards to `emitter.Handle` only when the producing session equals the active conversation's bound
session; every other session's event is dropped **before** `Handle` is ever called, so a background
conversation's connection never receives it. Gating happens at `Handle` time (in the drain goroutine),
not inside the sink, so the drop decision stays consistent with the cursor `Handle` itself reads via
`CurrentConversation()`. On an active-conversation switch the emitter's own #1062 `turnConvID` guard
flushes the prior conversation's buffered delta and re-mints a fresh turn for the new one — the drain
supplies session-level gating, the emitter's existing follow-active logic does the rest.

**Fixed (#1133): the gate no longer drops a turn's worth of delivery across a session rotation.** Until
\#1133, `sinkFor`'s session tag was captured once, at runner construction, by `newStreamRunnerFactory` (see
[streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md](streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md));
`Pool.rekeyLocked` (`RotateForNewSession`) re-keyed the pool entry and rebound the conversation **in
place**, while the surviving runner's already-bound sink kept its construction-time tag — a mapping gone
stale at rekey (#2010, surfaced by the `slash_command_list`/`model_list` docs re-derivation), not a narrow
race window. `newStreamRunnerFactory` now mints an atomic-backed `streamSessionTag` that `RestartFresh`
rotates through `Config.OnSessionRotate`, and both fan-in lanes (`sinkForTag`/`exitForTag`, which `sinkFor`
now delegates to) read that tag once per event rather than a captured constant — see [Session rotation
notification](streamsup-package-session-rotation-notification-onsessionrotate.md).

**Single-writer invariant.** Only the drain goroutine ever calls `emitter.Handle`/`flushDelta` — same
single-Run-goroutine assumption the PTY producer relies on, `-race`-tested by feeding two sessions'
Parsers concurrently. The drain also selects `emitter.flushC()` (the emitter arms its own coalescing
timer inside `Handle` but does not select it — a driver must) and calls `flushDelta` on the same
goroutine, so there's no cross-goroutine timer race.

**No transcript on this path (AC3).** `stream_turn_drain.go` imports no fsnotify, resolves no `<uuid>.jsonl`
path — structural, asserted by the test file doing no filesystem setup.

**The emitter is passed in, not built here** — the caller (the unit test in this ticket; #1081's
production wiring, below) owns its construction and replay wiring (`SetReplaySource`). This ticket's
`activeSession` is a plain injected func; #1081 composes it as `boundSessionIDForActive(w.active,
w.convReg)` — the relay leg's own follow-active resolver, not `boundHost`. See
[codebase/1098.md](../codebase/1098.md).
