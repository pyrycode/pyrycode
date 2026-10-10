# Draining turnevents into the interactive emitter (#1098)

The turn I/O parser (#1088) emits neutral `turnevent.Event`s from its sink callback, but that sink is
fixed where the runner is **constructed** (`newStreamRunnerFactory`), which sees only a
`supervisor.Config` — no handle on `interactiveTurnEmitterV2`, which is built later and separately on the
relay leg. `cmd/pyry/stream_turn_drain.go` lines the two lifetimes up with a late-bound, daemon-singleton
fan-in, and reproduces the PTY path's `startInteractiveTurnStreamV2` `OnEvent`/`FlushSignal`/`OnFlush`
triple **without `turnbridge`** — the Parser already emits `turnevent.Event`, so there is nothing to
un-map back to a `tuidriver.Event` for `turnbridge` to re-map.

```go
type streamTurnEnvelope struct {
    live        *liveStreamCapture // optional source-bound readings and shared turn/lane IDs
    sessionID   string
    source      history.SessionProvenance
    incarnation uint64 // daemon-local producer activation; absent from wire/storage provenance
    ev          turnevent.Event
}

type streamTurnSink struct { /* one buffered chan streamTurnEnvelope */ }

func newStreamTurnSink(buf int, logger *slog.Logger) *streamTurnSink
func (s *streamTurnSink) sinkFor(sessionID string) func(turnevent.Event) // non-blocking send; frozen-tag form, delegates to sinkForTag
func (s *streamTurnSink) sinkForTag(tag func() string, kind ...string) func(turnevent.Event)

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
`newStreamRunnerFactory` invocation) push captured source, incarnation and event onto the one buffered channel (256 slots, the
`pushQueueCap` precedent); `startStreamTurnDrainV2` spawns the sole reader goroutine. There is no
subscription registry and no subscribe/unsubscribe — the per-conn fan-out stays entirely inside the
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

**Per-event conversation attribution, not an active-conversation gate (#2739, #3088).** With the live owner installed,
the drain uses the conversation captured before fan-in; compatibility callers without
that capture resolve `conversationFor(env.sessionID)` on the drain. It forwards to `emitter.handleForSource` with captured provenance and incarnation under
**that** conversation's id — never the cursor's. Only an event whose session resolves to no conversation
at all is dropped, logged content-free as `stream_turn.no_conversation` (`kind` + `session_id` only).
Every conversation's events reach its own history, ring and clients, whichever conversation currently
holds the daemon's cursor; a background conversation's connection receives its own frames exactly as the
active one does.

**Capture history provenance before fan-in (#2981).** Claude and Codex factories
pass their fixed kind to `sinkForSessionTag`. Its single tag read supplies both routing
and `source`, so rotation after enqueue cannot substitute the successor. The
drain passes this captured value only after the existing resolution gate.
Omitted kinds keep provenance absent; no cursor, conversation binding at append
time, Codex thread ID or subprocess field supplies the source. See
[history producers](history-package-producers.md#producers-2114-2115).

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
emitter now keeps `convTurnState` by conversation and producing source (see below) rather than one scalar set of
lifecycle fields, so a conversation's turn stays open, and its buffered delta stays buffered, regardless
of which conversation the cursor points at or how many other conversations' events arrive in between.
`Handle(ctx, ev)` is kept as a thin wrapper — `e.HandleFor(ctx, e.sup.CurrentConversation(), ev)` — purely
for its 182 pre-existing unit-test call sites, which drive one conversation through a stub cursor;
production never calls it.

**The emitter retains state per conversation and producing source (#2739, #2981).** `interactiveTurnEmitterV2` embeds a
`*convTurnState` (`inTurn`, `turnID`, `turnConvID`, `seq`, `currentState`, `childLanes`,
`launcherTurns`, `childToolTurns`, and the
`deltaBuf`/`deltaMsgID`/`deltaParent`/`deltaConvID` coalescing group) and keeps `turns map[string]*convTurnState`,
one entry per conversation/kind/session ID/producer incarnation with a turn open,
text buffered, child attribution retained or a sealed predecessor's closed turn address.
Absent-source callers retain the conversation-only key. `source` is retained by value.
`selectConversation` re-points the embedded pointer at that source's own state,
creating it on first sight in `handleForSource`. `closeForConversation` selects
each retained source for the conversation. Both run only on the drain goroutine.
Because the state is embedded
rather than copied into a map of structs, every method below keeps reading `e.inTurn`, `e.seq`,
`e.deltaBuf` and meaning "the selected source's" — the same ~100 test assertions that read those
fields after driving one conversation through `Handle` keep compiling unchanged; moving the fields into
the map directly would have forced rewriting every one of them. `endTurn` clears only main lifecycle
fields: child text lane IDs/sequences and launcher/tool origins survive main closure and later turns.
`releaseConversation(key)`, deferred after handling and called after flushes, deletes
the entry only when it holds no open turn, buffered text, retained child attribution
or sealed closed turn address.
The compatibility `closeForConversation` path flushes pending
content, closes every retained main turn and clears child attribution for only that conversation.
Older main phases close first, preserving the conversation projection until one final idle.
It also clears retained attribution when already idle, emitting no idle transition or synthetic
`turn_end`. Other conversations' state is untouched.

**Runtime retirement follows the dying producer (#3013).** `closeRuntimeSource`
replaces conversation-wide exit cleanup in the runtime-enabled daemon. It flushes
the predecessor, records shown interrupted ordinary main tools and one main-turn
ending, then publishes the divider. A same-session exit uses `child_exit` without
a divider and closes only work preceding its exit epoch. A stale exit cannot
release a successor reservation, even before that successor emits output.
The retiring source retained by `streamSessionTag.Rotate` identifies an old
child after its live routing tag or last-output source has advanced.

Idle sleep and capacity eviction retain the routing ID on reactivation, so each
Claude/Codex `Run` gets a fresh daemon-local incarnation before producer startup.
Sealing, accepted-output watermarks, retained stops and emitter state use that
incarnation; permanent routing-ID sealing would suppress fresh main-turn openings
and terminal idle. `Rotate` and successful `CompareAndSwap` register aliases
under the boundary-capture lock before output, preserving another live tag's
destination on refusal. First-output registration alone misses chained rotations
and pre-output evictions, letting predecessor work reopen or waiting for the
wrong stop forever. Incarnations never alter wire or durable source identities;
original `HandleFor` and unbound callers retain zero.

Runtime boundaries wait for predecessor accepted output and, on eviction, its
consumed stop including late parsed tails. Ordinary callbacks retain facts and
signal a coalescing wake under a short lock without publication I/O. Channel
holds survive dequeue until publication finishes under the post gate; removing
the hold at dequeue lets a post overtake the divider. Confirmation-only operator
placement uses the drain with a whole-queue fence. Delivery release checks open
turns, since a denial may open one without a running phase. Sealed predecessor
events keep legacy provenance and bounded text sequencing but bypass successor
busy/idle mutation. See [runtime history facts and ordering](history-package-producers-runtime-lifecycle.md#runtime-boundaries-and-main-work-closure-3013)
for visibility, closure exclusions and focused regression tests.

**Child attribution must be independent of main lifecycle (#2960).** Guarding only
`startTurnIfNeeded` would still lose child identities at `endTurn` or idle release,
and could reattribute ongoing background work to a later main turn. Parent-bearing
`TextChunk`, `ToolStart` and `ToolUpdate` bypass main opening and phase transitions;
known child `ToolProgress` and `ToolCallDenied` resolve through `childToolTurns`
without gaining a parent wire field. `rememberLauncher` retains observed `Agent`/`Task`
origins, including nested launchers. `emitChildTool` fixes each child call's origin
on first observation, using the launcher's retained origin or `ensureDeltaLane`'s
parent-keyed fallback when unknown. Child prose uses its own lane ID and sequence.
All keys belong to the producing conversation and session, never the active cursor, and
publication still uses the common `emit` history/ring/fan-out path. See the
[wire attribution contract](../../protocol-mobile.md#tool_use) and
[ADR 042's agent boundary](../decisions/042-daemon-built-thread.md#sessions-agents-messages-read-marks).

**Child-only tests must deliver text without fabricating a main result.** Ending
a child-only fixture with `TurnEnd` can hide a synthetic main turn by closing it.
Use an explicit flush or the real coalescing timer, then assert no main lifecycle
frames or phase changes. `TestInteractiveTurnEmitterV2_BackgroundChildAcrossMainEnd`
also checks repeated flushes and exact ordered live/ring/history payload equality;
`TestStreamTurnDrainV2_AttributedTextExcludesThinkingAndSignature` receives only the
timer-delivered child delta and joins the drain before reading its state.

**Source changes must flush preceding text (#2981).** Keeping separate source
buffers without flushing at a source change would join old-source text across
an intervening successor event and reorder timer output. `HandleFor` flushes
another source's pending text in the same conversation before selecting the
incoming source, retaining lifecycle and child identities. At most one source
per conversation holds text; reused main/child message and parent IDs cannot
coalesce across sessions. Timer and lifecycle flushes select the retained source
before emitting, preserving its provenance.

**The coalescing timer is shared across every conversation, and must flush all of them.** `flushTimer` is
armed, per the invariant its own doc comment states, iff *some* conversation's `deltaBuf` is non-empty —
not iff the selected one's is. Arming re-arms only from an empty→non-empty transition when no other
conversation already holds buffered text (`anyBuffered()`), so the latency window always runs from the
oldest unflushed chunk across every conversation, not from whichever one buffered most recently.
`flushDelta` (reached from `Handle`'s per-kind arms, flushing the selected source only) stops the
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

**Conversation phase is a projection across retained sources (#2981).**
`runningPhase` chooses the running source with the most recent main-phase
activity; child-only activity and an open turn with no phase do not contribute.
`transitionTo` updates ownership even on repeated same-phase activity, then
deduplicates the projected conversation phase. A delayed predecessor end keeps
the successor's phase and reconnect snapshot. Ending the owner restores the
latest remaining running phase with that source's captured provenance; only
ending the last running phase publishes idle and removes the snapshot.
`endTurn` clears only the selected source, while `turn_end` keeps that source's
provenance. Deduplicating per source alone would let a predecessor end erase
a successor's snapshot that later responding chunks never repair.

**Fixed (#1133): a session rotation no longer drops a turn's worth of delivery.** Until #1133, `sinkFor`'s
session tag was captured once, at runner construction, by `newStreamRunnerFactory` (see [Constructing a
streamRunner](streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md));
`Pool.rekeyLocked` (`RotateForNewSession`) re-keyed the pool entry and rebound the conversation **in
place**, while the surviving runner's already-bound sink kept its construction-time tag — a mapping gone
stale at rekey (#2010, surfaced by the `slash_command_list`/`model_list` docs re-derivation), not a narrow
race window. `newStreamRunnerFactory` now mints an atomic-backed `streamSessionTag` that `RestartFresh`
rotates through `Config.OnSessionRotate`. Production event callbacks use
`sinkForSessionTag` to read the live tag; `exitForSessionTag` also retains the
dying predecessor across rotation. `sinkForTag` / `exitForTag` remain compatibility
entry surfaces rather than production attribution sources — see [Session rotation
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

**Queue placement waits for the writer, never its callback (#2820).**
Echo handling can wait for `sendNowPlacement.write`'s outcome while the drain
holds the `channelDelivery` post-publication mutex. Signal that outcome before
write-reservation cleanup tries to acquire the same mutex; reversing these
steps deadlocks the writer and drain. `OnDelivered` may be withheld past the
answering turn's end, so it cannot be a stream-placement barrier.
The placement mutex guards bookkeeping only, never the writer or commit.
Echo commits and cancellation-aware fallback commands run on this drain;
ordinary no-echo commits precede the closing event's idle release, while
send-now retains its carry grace. `startRelayV2` binds synchronous operator
publication to the shared replay ring before starting the drain.
The history-only drain needs no live publisher. See
[history producers](history-package-producers.md#producers-2114-2115) for safe content
and the history/live/replay ordering guarantee.

**Published completion, not an idle snapshot, releases durable posts (#2811).**
The drain gates tracker observation and `HandleFor` together under the
`channelDelivery` mutex, clearing published activity only after completion has
been handled. An accepted runtime exit similarly calls `closeRuntimeSource` to flush
buffered text and close the dying source before releasing activity;
compatibility callers retain `closeForConversation`. Early eviction
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
