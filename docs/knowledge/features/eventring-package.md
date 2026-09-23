# `internal/eventring` — durable, daemon-wide-unique event ring

In-memory, daemon-resident, bounded store of the recent structured turn events
the interactive emitter fans out, keyed by a **durable, daemon-wide-unique event
id** (retention is still per-conversation — only the id space is shared; #2022).
It is the **replay source** for the mid-turn-reconnect path (#647): a phone
that reconnects mid-turn catches up from the ring without a gap, or is told to
resync. Landed in #646 (EPIC #596 Phase 2 structured streaming, ADR 025
§ Backpressure / replay).

This slice is the **storage primitive only** — it ships with **no reconnect
wiring**. The per-connection `last_event_id` tracking and the on-reconnect query
are #647 (`security-sensitive`), which *consumes* `After`.

- Decision anchor: [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md)
  § Backpressure / replay ("replays from a bounded per-conversation event ring,
  or emits a resync marker").
- Spec: [`specs/architecture/646-event-ring-durable-id.md`](../../specs/architecture/646-event-ring-durable-id.md).
- Ticket record: [codebase/646.md](../codebase/646.md).

## Why a new id, why a new store

Two facts make this net-new work, not reuse of anything already on the structured
path:

- **The envelope `id` cannot be the replay key.** The emitter's `nextID`
  ([codebase/632.md](../codebase/632.md) § Envelope-ID policy) is incremented
  *per connection per envelope* inside `emit`'s fan-out loop — the same logical
  event carries a *different* id on each connection, and the counter resets on
  every reconnected session. A replay key must be **connection-independent**,
  assigned **once per logical event**, and survive a reconnect. So the ring keeps
  its own counter, unique daemon-wide since #2022; `nextID` is **not** overloaded.
- **The ring is the only replay source.** `internal/conversations` holds metadata
  only (id, session history, archive state — no message content); there is no
  message-history store, and the v1 `backfill_since` wire flow that could have read
  from one was dead code (zero emitters, zero handlers) and was removed in #967 (see
  [codebase/967.md](../codebase/967.md)). Replay can only come from an in-memory ring
  the daemon maintains as it fans events out.

## Durability boundary (in scope vs out)

The ring is **in-memory and keyed to the `pyry` daemon lifetime**, not the child's:

- **Survives** a supervised claude-**child** respawn and phone reconnects — the
  daemon process stays up across both. The ring is owned by the emitter, which is
  built **once** in `startRelayV2` and persists across the drain goroutine's
  re-subscriptions and child restarts ([codebase/633.md](../codebase/633.md)
  § single-writer for emitter state, describing the now-deleted PTY-path
  driver — the invariant it documents still holds under the stream-json drain).
  (In pyrycode "supervisor restart" respawns the claude child — the daemon does
  not go down.)
- **Does not survive** a full daemon-process restart — purely in-memory, by
  design. That boundary is handled by #647's resync, triggered by the AC-5
  "you missed some" gap signal (see `After` below). **No disk persistence** — the
  bounded-memory guarantee assumes a purely in-memory structure.

## Exported surface (2 types)

```go
const MaxEventsPerConversation = 1024 // the named per-conversation bound

type Event struct {
    ID      uint64          // durable event id, unique ring-wide (>= 1, strictly increasing in append order; #2022)
    Type    string          // protocol.Type* wire type
    Payload json.RawMessage // the already-marshalled envelope payload
    TS      time.Time       // the logical event's timestamp
}

type Ring struct { /* sync.Mutex + ring-wide nextID counter + map[convID]*convRing */ }

func New(maxPerConversation int) *Ring                                  // panics if < 1
func (r *Ring) Append(convID, typ string, payload json.RawMessage, ts time.Time) uint64
func (r *Ring) After(convID string, afterID uint64) (events []Event, gap bool)
func (r *Ring) Drop(convID string)                                      // frees one conversation's entry; unknown id is a no-op (#1502)
func (r *Ring) NewestID(convID string) uint64                           // nextID-1, or 0 if unknown (#663)
```

`Drop` (#1502) deletes `convID`'s entry outright — the only mutation this package
exposes besides `Append`. It exists so daemon memory does not grow with every
conversation that has ever streamed: without it, a removed conversation's up-to-
1024 retained events (several MB with coalesced `assistant_delta` payloads) stay
pinned until the daemon restarts. The ring-wide `nextID` counter (#2022) is left
untouched, so a later `Append` for the same conversation id — the entry can be
recreated, see Concurrency below — gets an id above every one issued before; no
scalar cursor can alias a dropped id. After `Drop`, the conversation reads exactly
like one the ring never saw: `NewestID` → 0, `After(convID, 0)` → caught up,
`After(convID, n>0)` → gap. That gap is correct, not a defect: the conversation no
longer exists, so a reconnecting phone should resync rather than replay.

`NewestID` ([#663]) returns the highest id ever assigned to a known conversation
(`convRing.latestID`), `0` for an unknown one — mutex-guarded like `After`. It
surfaces `After`'s internal classification boundary so the #647 reconnect consumer
can **clamp** its per-conn dedup watermark to `min(afterID, NewestID(convID))` (see
[codebase/663.md](../codebase/663.md)). Since #1494 an untrusted `last_event_id`
beyond the conversation's id space classifies as a **gap** and returns before that
clamp, so ruling out the silent mute is now the gap branch's job, not the clamp's;
what the clamp still covers is the window *between* the consumer's `NewestID` and
`After` calls, where a concurrent `Append` can leave the read one or more ids stale.
Sound because the newest event is never evicted (below) and `latestID` advances
independent of retention, so it is always the highest *retained* id.

Before #2022, `NewestID` read `nextID - 1` off a **per-conversation** counter, so
every conversation's ids started at 1 and the id spaces overlapped. That let a
per-**connection** cursor (`V2Session.replayThrough`) taken from one conversation's
watermark silently suppress a *different* conversation's live events whose ids
happened to fall at or below it — no frame, no error, no resync, for the life of
the connection. #2022 moved the counter onto `Ring` itself: ids are now unique and
strictly increasing across the whole ring, ascending-but-not-contiguous within any
one conversation, so no future event in any conversation can ever carry an id a
watermark has already passed. The per-connection guard
(`internal/relay/v2session.go`'s `forwardEnvelope`) needed no change — see
[Reconnect replay](v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md).

`Ring` deliberately does **not** store `protocol.Envelope`: the envelope's `ID`
is the per-conn `nextID`, meaningless for replay across connections. It stores the
durable id plus the three replay-relevant fields (`Type`, `Payload`, `TS`). #647
reconstructs a fresh `protocol.Envelope` per reconnecting conn (new per-conn `ID`,
the stored `Type`/`TS`/`Payload`). `Payload` is treated as **immutable** — the
appender owns the bytes and never mutates them after `Append`; `After` returns the
reference without copying.

## Where the durable id is assigned (the load-bearing point)

The id is assigned **once per logical event, before the per-conn fan-out**, in the
emitter's `emit()` ([codebase/632.md](../codebase/632.md) § `emit`) — the one
place every envelope reaches the wire, and a 1:1 map to one logical event. After
the single `json.Marshal`, before the `ActiveConns` loop:

- The timestamp is **hoisted out of the loop** (`ts := time.Now().UTC()`) — one
  timestamp per logical event, shared by every conn and by the ring (previously
  each conn got its own `time.Now()`; the change is intentional and strictly more
  correct: one logical event = one timestamp).
- `eventID := e.ring.Append(convID, typ, payloadJSON, ts)` records the event
  **unconditionally — independent of how many conns are interactive, including
  zero**, because the ring is the replay source for phones that are *absent right
  now* and reconnect later. The returned id was **discarded in #646**; **#649
  surfaces it on the wire** — `emit` captures it and stamps it on every per-conn
  envelope as `Envelope.EventID` so a reconnecting phone can advertise it as
  `last_event_id` (see [codebase/649.md](../codebase/649.md)). The inbound consumer
  that accepts and replays from it is #647.
- The per-conn loop then runs almost as before — `e.nextID++` per conn, build the
  envelope, `Push` — with one addition (#649): `EventID: &eventID` on the envelope
  literal (`&eventID` is a loop-invariant local shared by reference across the
  fan-out, so all conns get the identical durable id with no per-conn allocation).

All six v2 wire types flow through `emit()`, so the ring records the complete set.

## Eviction policy (bounded memory, deltas sacrificed first)

The bound is **total events per conversation** (`MaxEventsPerConversation`), a hard
cap. When a conversation is at the bound, `Append` evicts **before** appending:

1. Scan front-to-back for the **oldest `assistant_delta`** (smallest id, since the
   slice is id-ascending). If found, remove it.
2. Else (all retained events are control-class) remove the **oldest event overall**
   — honouring the hard bound even under all-control pressure.

`assistant_delta` is the **sole droppable class**; the other five — `turn_state`,
`tool_use`, `tool_result`, `turn_end`, `stall` — are control-class and retained in
preference (deltas are lossy/coalescable per ADR 025). Those six are the complete
v2 wire-type set ([`internal/protocol/codes.go`](protocol-package.md)), so the
partition leaves no event class unaccounted for.

Middle-deletion of a delta leaves the slice **id-ascending but non-contiguous**
(e.g. `{1,3,4}` after delta `2` is dropped). This is by design and **does not**
fabricate a gap — see below.

## `After` — the three-way replay contract

`After(convID, afterID)` returns `(events, gap)`, distinguishing three outcomes the #647 consumer must tell apart:

| Outcome | Condition | Return | Consumer action |
|---|---|---|---|
| **Gap** (beyond the id space) | `afterID > latestID` (#1494) | `(nil, true)` | resync (full backfill) |
| **Caught up** | `afterID == latestID` | `(nil, false)` | nothing to send |
| **Gap** (aged out) | `afterID < evictedThrough` (#2022) | `(nil, true)` | resync (full backfill) |
| **Replay** | otherwise | `(events with ID > afterID, ascending; false)` | replay these |

`latestID` is `convRing.latestID` (the highest id ever assigned to this
conversation). The two `latestID` comparisons are checked first, in the order
shown — beyond-the-id-space before caught-up — and both precede the aged-out
check. The AC-5 distinction "you missed some" vs "you're caught up" is exactly
**`gap=true` vs `(gap=false, empty events)`**.

**The aged-out test used to *infer* the back edge from contiguity; #2022 replaced
the inference with the fact it was inferring.** Before #2022, `oldestRetainedID >
afterID+1` was sound only because a conversation's own ids ran contiguously from 1
— "the consumer's next id is `afterID+1`; if the oldest retained id is past it,
that id fell off the back." Once ids are unique ring-wide, a conversation's
retained ids are sparse by construction (another conversation's appends land
between them), and the inference misfires exactly where it looks most
innocuous: a conversation whose retained window is *complete* but starts at, say,
41 reads `41 > afterID+1` as true for a perfectly replayable `afterID` of 0 or 40,
and reports a spurious gap — a spurious `resync`, i.e. a full client reload, on a
connection that needed no reload. `convRing.evictedThrough` now tracks the actual
fact directly: `evictOldest` records it only when it removes from position 0
(the oldest *retained* event, whatever its class), so it names the true back edge
regardless of how sparse the id space is. The arithmetic on the untrusted cursor
also disappears with it: the old branch's `afterID+1` wrapped to `0` at
`math.MaxUint64`, so a hostile-max cursor reached its gap classification through a
wrapped intermediate rather than through the `afterID > latestID` branch above it
that was actually meant to catch it (see Security review, #2022 spec). **The
lesson generalizes:** a boundary computed from "what the id sequence implies" is
only as sound as the assumption that ids are contiguous — check that assumption
explicitly before reusing this shape elsewhere in the ring.

- **A missing *middle* delta is not a gap.** Gap is signalled only by the oldest
  *retained* id passing the consumer's cursor (falling off the *back*), never by a
  hole left behind by delta eviction.
- **Unknown conversation:** `afterID == 0` → caught up `(nil, false)` (a fresh
  consumer); `afterID > 0` → gap `(nil, true)` (references events this daemon never
  had — e.g. after a daemon restart wiped the ring). This makes the AC-5 signal
  usable by #647 across the daemon-restart boundary #646 scopes out.
- **Beyond the id space is a gap for a *known* conversation too (#1494).** Same
  reasoning as the bullet above — `afterID > latestID` names an id this daemon
  never issued — and the same restart shape: the ring is wiped, per-conversation
  ids restart at 1, and another conn drives a turn before the phone reconnects, so
  the conversation is populated again and the empty-ring branch no longer catches
  it. Classifying that caught-up muted the phone, which dedups durably on
  `event_id` and would drop every live event until the ids climbed past its cursor.
- **Isolation is structural** — `After` only reads `convs[convID]`, so it can never
  return another conversation's events.

## Ownership & wiring (why the constructor signature is unchanged)

**The emitter owns the ring; it is created inside `newInteractiveTurnEmitterV2`,
not injected.** Adding a required `*eventring.Ring` constructor parameter would
force a simultaneous edit of all 26 `newInteractiveTurnEmitterV2` call sites (1
production + 25 test) — the constructor-cascade red line. Instead the emitter gains
an unexported `ring` field initialised to `eventring.New(MaxEventsPerConversation)`;
the **signature is unchanged**, the 26 call sites stay byte-identical. The emitter
is the producer and the natural owner of "what I emitted, for replay".

**#647's hook (seam ready, not built here):** `emitter.ring` is a `package main`
field reachable from `startRelayV2`, which registers it as the replay source via
`SetReplaySource(emitter.ring, ...)` right after construction. #647 reads it
there and hands it to the v2 manager for the reconnect query — no change to the
ring or the emitter's invariant. No accessor is added in #646 (the field is
already reachable within `package main`).

**Removal wiring (#1502):** the ring does not import `conversations` and has no
way to learn a conversation was removed on its own — `cmd/pyry` connects the two
through a small seam rather than a new ring-side dependency. `relay.go`'s
`dropRingOnConversationDelete(reg *conversations.Registry, ring *eventring.Ring)`
installs `func(id) { ring.Drop(string(id)) }` as `conversations.Registry`'s
`SetOnDelete` observer (see [`conversations-registry-crud.md`](conversations-registry-crud.md)
§ `SetOnDelete`), called once, in the same stream-mode branch as `SetReplaySource`
and right after it — the one place both the freshly-built ring and the registry
are in scope. `Registry.Delete` is the single funnel both removal paths
(`delete_conversation` and the idle sweep) go through, so this one seam drops the
ring entry for both. A nil registry leaves nothing wired, matching the postures
where no conversations registry exists.

## Concurrency

- **The ring is the only shared object on the structured path; it is internally
  synchronised by one `sync.Mutex`.** Both `Append` (write) and `After` (read)
  take the lock. It is a **leaf lock** — held only around the map lookup + slice
  ops, never across a channel op or another lock, never nested.
- **The emitter's single-`Run`-goroutine, unguarded-counter invariant is preserved
  unchanged** ([codebase/632.md](../codebase/632.md) / [codebase/633.md](../codebase/633.md)).
  `Append` is called only from `emit()`, which runs only on the single drain
  goroutine `startStreamTurnDrainV2` spawns; all the emitter's *other* fields stay unguarded and
  single-goroutine. The cross-goroutine sharing the future query path needs lives
  **inside the ring's mutex**, not in the emitter — the Technical-Notes
  reconciliation. *Belt-and-suspenders, different fabric:* the emitter stays
  lock-free; the ring is a self-contained mutex-guarded object.
- **No goroutine is spawned** — the ring is passive; the emitter remains a passive
  state machine.
- In #646 only `Append` runs in production (no reconnect caller yet). `After` is
  built, unit-tested (incl. a `-race` append-vs-query test), and the mutex is in
  place from day one so #647 can wire `After` from the manager's goroutine without
  touching this slice.
- **Accepted benign race on `Drop` (#1502):** an emitter `Append` for the
  just-dropped conversation can be in flight (a turn still streaming at the
  moment of deletion) and land after `Drop` releases the lock, recreating a small
  entry under the same id. That entry is bounded by `MaxEventsPerConversation`
  like any other and is only reachable under the deleted conversation's id. A
  tombstone set was considered and rejected: it would itself grow without bound,
  trading one unbounded map for another.

## Error handling

- `New` **panics** if `maxPerConversation < 1` — a programmer error, matching the
  panic-on-misconfig style of `dispatch.New` / `V2SessionManager`.
- `Append` has **no failure path** — it always assigns an id and stores (memory
  only). It does not log; the caller (`emit`) only reaches `Append` after a
  successful `json.Marshal` (a marshal error drops the event *before* the ring, so
  no id is assigned and no fan-out happens).
- `After` has **no failure path** — a pure 3-way classification; the unknown-conv
  case is a defined outcome, not an error.

## Files

```
internal/eventring/
├── ring.go        Event, Ring, convRing; MaxEventsPerConversation; New / Append / After / Drop / NewestID; evictOldest
└── ring_test.go   id assignment, replay/caught-up/gap, isolation, unknown-conv,
                   delta-first + all-control eviction, no-fabricated-gap, cap-1, New(<1) panic,
                   NewestID (unknown→0, last-assigned, advances-past-eviction, conv-isolation; #663),
                   Drop (NewestID→0, After caught-up/gap, other conversations unaffected, id counter
                   not reset, unknown-id no-op; #1502), -race
```

~130 LOC of production code (one new package), plus the ~+10 LOC emitter edit
(`ring` field + constructor init + the `emit` hoist-and-append). The append-site
integration tests live in `cmd/pyry/interactive_turn_v2_test.go` (additions only).

## Related

- [codebase/646.md](../codebase/646.md) — ticket record (patterns + lessons).
- [codebase/632.md](../codebase/632.md) — the emitter that owns the ring; its
  `emit` fan-out, `nextID` envelope-id policy, and single-`Run`-goroutine /
  unguarded-counter invariant this slice appends into and preserves.
- [codebase/633.md](../codebase/633.md) — the live producer wiring; why the
  emitter (and therefore the ring) is daemon-resident across child respawns.
- [features/turnbridge-package.md](turnbridge-package.md) /
  [features/turnevent-package.md](turnevent-package.md) — the producer + neutral
  model upstream of the emitter.
- [features/protocol-package.md](protocol-package.md) — the six v2 wire-type
  constants the eviction policy partitions on.
- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — § Phase 2
  structured streaming, § Backpressure / replay.
- **Consumer (deferred — none wired in #646):** #647 — per-conn `last_event_id`
  tracking + on-reconnect replay/resync; the `security-sensitive` slice that reads
  `After`.
- [codebase/663.md](../codebase/663.md) — adds `NewestID` and consumes it to clamp
  the #647 caught-up watermark to `min(afterID, NewestID)`, closing a trust-boundary
  silent-suppression defect.
- [`specs/architecture/2022-global-event-ids.md`](../../specs/architecture/2022-global-event-ids.md)
  — #2022: moves the id counter from `convRing` onto `Ring`, closing the
  cross-conversation silent-drop defect described in
  [Reconnect replay](v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md).
- [`features/conversations-registry-crud.md`](conversations-registry-crud.md) §
  `SetOnDelete` — the removal observer `cmd/pyry` wires to `Drop`, and
  [`features/conversations-registry.md`](conversations-registry.md) — `Registry.Delete`,
  the single funnel both removal paths go through.
- [`specs/architecture/1502-eventring-drop-on-conversation-delete.md`](../../specs/architecture/1502-eventring-drop-on-conversation-delete.md)
  — #1502: adds `Drop` and the removal-observer wiring so a removed
  conversation's retained events stop pinning daemon memory.

[#663]: https://github.com/pyrycode/pyrycode/issues/663
