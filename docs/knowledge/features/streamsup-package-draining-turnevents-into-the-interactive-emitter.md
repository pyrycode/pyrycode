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
    conversationFor func(sessionID string) (conversationID string, ok bool),
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
included — see [Class-aware fan-in reserve](streamsup-package-per-conversation-turn-busy-track-class-aware-fan-in-reserve.md) for why that
was an ADR 025 violation and how it's fixed. Today only the droppable class is refused once the channel
reaches `droppableCap`, and only that drop Debug-logs content-free (`event`, `kind`, `session_id` only —
never `ev`'s assistant/thought/tool content). The channel is **never closed** (a Parser may outlive the
drain during shutdown; the drain stops on `ctx`, not on channel close, so a send-on-closed panic is
structurally impossible).

Closing events can still exhaust the reserve: `TurnEnd` then drops with a
content-free Warn. Actual exits and confirmed runner stops survive in retained
state, with successful enqueue positions ordering them after preceding queued
output and before later output. The wake channel coalesces; it does not own
the close. See [retained stop transport](streamsup-package-per-conversation-turn-busy-track-exit-lane-on-the-turn-busy-fan.md).

**Per-event conversation attribution, not an active-conversation gate (#2739).** The drain goroutine
resolves `conversationFor(env.sessionID)` and forwards to `emitter.HandleFor(ctx, convID, ev)` under
**that** conversation's id — never the cursor's. Only an event whose session resolves to no conversation
at all is dropped, logged content-free as `stream_turn.no_conversation` (`kind` + `session_id` only).
Every conversation's events reach its own history, ring and clients, whichever conversation currently
holds the daemon's cursor; a background conversation's connection receives its own frames exactly as the
active one does.

**A side effect, decided on rather than filtered out: a child's startup frames now reach history too.**
A frame like `mcp_status` or `model_list` describes the child, not a turn (`HandleFor`'s arms for both
open, transition and close nothing), and the bootstrap session is bound to the bootstrap conversation
from startup — so the bootstrap child's own report now reaches that conversation's history and ring the
moment it is admitted, even though no client has routed a message to it yet. Before #2739 this exact case
was dropped, not because of anything about startup specifically, but as a side effect of the
active-conversation gate: no message routed meant no active conversation, which meant every event was
"not active" and dropped. The fix kept this behaviour rather than special-casing it back in — the frame
is still a true report about that conversation's own child — which is why four e2e tests that synced on
the drop this produced had to be reworked to sync on the history entry instead (see [`request_history`'s
wire contract](../../protocol-mobile.md#mcp_status) for the client-visible shape of this change).

This replaced an earlier design, until #2739: the drain compared the producing session against
`activeSession()`, the active conversation's bound session, and dropped every other session's event
before `Handle` was ever called (logged `stream_turn.not_active`, Debug) — so a background conversation's
turn tail was lost for good. The emitter's own `Handle` paired that gate with a follow-active switch
(#1062): a cursor move closed the *prior* conversation's turn outright. #2739 removed both halves. The
emitter now keeps one `convTurnState` per conversation (see below) rather than one scalar set of
lifecycle fields, so a conversation's turn stays open, and its buffered delta stays buffered, regardless
of which conversation the cursor points at or how many other conversations' events arrive in between.
`Handle(ctx, ev)` is kept as a thin wrapper — `e.HandleFor(ctx, e.sup.CurrentConversation(), ev)` — purely
for its 182 pre-existing unit-test call sites, which drive one conversation through a stub cursor;
production never calls it.

**The emitter's turn state is per conversation (#2739), not scalar.** `interactiveTurnEmitterV2` embeds a
`*convTurnState` (`inTurn`, `turnID`, `turnConvID`, `seq`, `currentState`, `childLanes`, and the
`deltaBuf`/`deltaMsgID`/`deltaParent`/`deltaConvID` coalescing group) and keeps `turns map[string]*convTurnState`,
one entry per conversation with a turn open or text buffered. `selectConversation(convID)` re-points the
embedded pointer at that conversation's own state, creating it on first sight — called at the top of
`HandleFor` and of `closeForConversation`, only from the drain goroutine. Because the state is embedded
rather than copied into a map of structs, every method below keeps reading `e.inTurn`, `e.seq`,
`e.deltaBuf` and meaning "the selected conversation's" — the same ~100 test assertions that read those
fields after driving one conversation through `Handle` keep compiling unchanged; moving the fields into
the map directly would have forced rewriting every one of them. `releaseConversation(convID)`, deferred at
the end of `HandleFor`, deletes the entry once it holds no open turn and no buffered text, so `turns`
stays bounded to conversations with work in flight rather than growing for the daemon's life.
`closeForConversation` (the drain's lifecycle-close and child-exit paths) selects only the named
conversation's state and returns its turn to idle without touching any other conversation's.

**The coalescing timer is shared across every conversation, and must flush all of them.** `flushTimer` is
armed, per the invariant its own doc comment states, iff *some* conversation's `deltaBuf` is non-empty —
not iff the selected one's is. Arming re-arms only from an empty→non-empty transition when no other
conversation already holds buffered text (`anyBuffered()`), so the latency window always runs from the
oldest unflushed chunk across every conversation, not from whichever one buffered most recently.
`flushDelta` (reached from `Handle`'s per-kind arms, flushing the selected conversation only) stops the
timer only once `anyBuffered()` is false — if it stopped unconditionally, a second conversation's text
would sit buffered forever once the first one's flush ran. The drain's `flushC()` case does not call
`flushDelta` directly; it calls `flushAll(ctx)`, which flushes every conversation with buffered text (each
conversation's own frames stay in order; order *across* conversations is unspecified, which is the only
order a client can observe anyway).

**`turnPhaseSnapshot` holds one entry per conversation with a running turn (#2739), not one scalar pair.**
Before #2739 the emitter held at most one open turn, so the connect-time turn-phase reconcile's producer
(`cmd/pyry/interactive_turn_v2.go`'s `turnPhaseSnapshot`, see [its own
doc](v2-session-manager-state-machine-connect-time-turn-phase-reconcile-running.md)) was a single
`(conversationID, state)` pair. It is now a mutex-guarded `map[string]turnbridge.TurnState`: `publish`
sets or (on `StateIdle`) deletes a conversation's entry, `clear` deletes it unconditionally, and
`running()` returns one `protocol.TurnStatePayload` per entry, sorted by conversation id for determinism,
so a freshly-connected conn is reconciled on every conversation with a turn running, not only the last one
touched.

**Fixed (#1133): a session rotation no longer drops a turn's worth of delivery.** Until #1133, `sinkFor`'s
session tag was captured once, at runner construction, by `newStreamRunnerFactory` (see [Constructing a
streamRunner](streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md));
`Pool.rekeyLocked` (`RotateForNewSession`) re-keyed the pool entry and rebound the conversation **in
place**, while the surviving runner's already-bound sink kept its construction-time tag — a mapping gone
stale at rekey (#2010, surfaced by the `slash_command_list`/`model_list` docs re-derivation), not a narrow
race window. `newStreamRunnerFactory` now mints an atomic-backed `streamSessionTag` that `RestartFresh`
rotates through `Config.OnSessionRotate`, and both fan-in lanes (`sinkForTag`/`exitForTag`, which `sinkFor`
now delegates to) read that tag once per event rather than a captured constant — see [Session rotation
notification](streamsup-package-session-rotation-notification-onsessionrotate.md). #2739's `conversationFor`
resolution (`conversationForSession`, which also checks `SessionHistory`) means a tail from a session that
has *already* rotated away still resolves to its conversation rather than needing the tag fix at all; the
two fixes are complementary, not redundant — #1133 keeps the *producing* session's own tag current, #2739
is what lets a late tail from an *earlier* session in that same conversation's history still land.

**Single-writer invariant.** Only the drain goroutine ever calls `HandleFor`/`flushDelta`/`flushAll` — same
single-Run-goroutine assumption the PTY producer relies on, `-race`-tested by feeding two sessions'
Parsers concurrently. The drain also selects `emitter.flushC()` (the emitter arms its own coalescing
timer inside `Handle`/`HandleFor` but does not select it — a driver must) and calls `flushAll` on the same
goroutine, so there's no cross-goroutine timer race.

**Published completion, not an idle snapshot, releases durable posts (#2811).**
The drain gates tracker observation and `HandleFor` together under the
`channelDelivery` mutex, clearing published activity only after completion has
been handled. An accepted exit similarly calls `closeForConversation` to flush
buffered text and close lifecycle before releasing activity. Early eviction
holds survive completion until confirmed producer stop; stale exits preserve
newer reservations. Emitter mutation remains on this goroutine throughout.
Without a relay URL, `startRelay` still constructs a history-backed emitter with
`historyOnlyBroadcaster` and starts the same drain and transition observer.
History publication and post release therefore work without attached clients.
See [channel-post boundaries](control-plane-channel-post-live-delivery.md)
and [confirmed teardown](streamsup-package-per-conversation-turn-busy-track-session-teardown-clear.md).

**No transcript on this path (AC3).** `stream_turn_drain.go` imports no fsnotify, resolves no `<uuid>.jsonl`
path — structural, asserted by the test file doing no filesystem setup.

**The emitter is passed in, not built here** — the caller (the unit test in this ticket; #1081's
production wiring, below) owns its construction and replay wiring (`SetReplaySource`). This ticket's
`activeSession` was a plain injected func; #2739 widened it to `conversationFor`, and #1081's wiring now
composes it as `conversationFor := func(sid string) (string, bool) { return conversationForSession(w.convReg, sid) }`
(`cmd/pyry/relay.go`) — the same resolver [the per-conversation turn-busy
tracker](streamsup-package-per-conversation-turn-busy-tracking.md) uses, not `boundSessionIDForActive`.
`boundSessionIDForActive` has no production caller left after #2739; it stays in `relay.go` for its own
unit test only, with its removal called out as a follow-up rather than done in that ticket. See
[codebase/1098.md](../codebase/1098.md).
