# `internal/eventring` — durable per-conversation event ring

In-memory, daemon-resident, bounded store of the recent structured turn events
the interactive emitter fans out, keyed by a **durable per-conversation event
id**. It is the **replay source** for the mid-turn-reconnect path (#647): a phone
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
  its own per-conversation counter; `nextID` is **not** overloaded.
- **The ring is the only replay source.** `internal/conversations` holds metadata
  only (id, session history, archive state — no message content), and there is no
  `backfill_since` handler. Replay can only come from an in-memory ring the daemon
  maintains as it fans events out.

## Durability boundary (in scope vs out)

The ring is **in-memory and keyed to the `pyry` daemon lifetime**, not the child's:

- **Survives** a supervised claude-**child** respawn and phone reconnects — the
  daemon process stays up across both. The ring is owned by the emitter, which is
  built **once** in `startInteractiveTurnStreamV2` and persists across the
  producer's re-subscriptions and child restarts ([codebase/633.md](../codebase/633.md)
  § single-writer for emitter state). (In pyrycode "supervisor restart" respawns
  the claude child — the daemon does not go down.)
- **Does not survive** a full daemon-process restart — purely in-memory, by
  design. That boundary is handled by #647's resync, triggered by the AC-5
  "you missed some" gap signal (see `After` below). **No disk persistence** — the
  bounded-memory guarantee assumes a purely in-memory structure.

## Exported surface (2 types)

```go
const MaxEventsPerConversation = 1024 // the named per-conversation bound

type Event struct {
    ID      uint64          // durable per-conversation event id (>= 1, strictly increasing)
    Type    string          // protocol.Type* wire type
    Payload json.RawMessage // the already-marshalled envelope payload
    TS      time.Time       // the logical event's timestamp
}

type Ring struct { /* sync.Mutex + map[convID]*convRing */ }

func New(maxPerConversation int) *Ring                                  // panics if < 1
func (r *Ring) Append(convID, typ string, payload json.RawMessage, ts time.Time) uint64
func (r *Ring) After(convID string, afterID uint64) (events []Event, gap bool)
func (r *Ring) NewestID(convID string) uint64                           // nextID-1, or 0 if unknown (#663)
```

`NewestID` ([#663]) returns `nextID - 1` (the highest id ever assigned) for a known
conversation, `0` for an unknown one — mutex-guarded like `After`. It surfaces
`After`'s internal `latestID` caught-up boundary so the #647 reconnect consumer can
**clamp** its per-conn dedup watermark to `min(afterID, NewestID(convID))`, ruling
out an untrusted `last_event_id` beyond the conversation's id space silently muting
the live stream (see [codebase/663.md](../codebase/663.md)). Sound because the
newest event is never evicted (below) and `nextID` advances independent of
retention, so `nextID - 1` is always the highest *retained* id.

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

`After(convID, afterID)` returns `(events, gap)`, distinguishing three outcomes the
#647 consumer must tell apart:

| Outcome | Condition | Return | Consumer action |
|---|---|---|---|
| **Caught up** | `afterID >= latestID` | `(nil, false)` | nothing to send |
| **Gap** | next-expected `afterID+1` fell off the back: `oldestRetainedID > afterID+1` | `(nil, true)` | resync (full backfill) |
| **Replay** | otherwise | `(events with ID > afterID, ascending; false)` | replay these |

`latestID` is `convRing.nextID - 1` (the highest id ever assigned). Caught-up is
checked first. The AC-5 distinction "you missed some" vs "you're caught up" is
exactly **`gap=true` vs `(gap=false, empty events)`**.

- **A missing *middle* delta is not a gap.** Gap is signalled only by the oldest
  *retained* id passing the consumer's cursor (falling off the *back*), never by a
  hole left behind by delta eviction.
- **Unknown conversation:** `afterID == 0` → caught up `(nil, false)` (a fresh
  consumer); `afterID > 0` → gap `(nil, true)` (references events this daemon never
  had — e.g. after a daemon restart wiped the ring). This makes the AC-5 signal
  usable by #647 across the daemon-restart boundary #646 scopes out.
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
field reachable from `startInteractiveTurnStreamV2`. #647 reads it there and hands
it to the v2 manager for the reconnect query — no change to the ring or the
emitter's invariant. No accessor is added in #646 (the field is already reachable
within `package main`).

## Concurrency

- **The ring is the only shared object on the structured path; it is internally
  synchronised by one `sync.Mutex`.** Both `Append` (write) and `After` (read)
  take the lock. It is a **leaf lock** — held only around the map lookup + slice
  ops, never across a channel op or another lock, never nested.
- **The emitter's single-`Run`-goroutine, unguarded-counter invariant is preserved
  unchanged** ([codebase/632.md](../codebase/632.md) / [codebase/633.md](../codebase/633.md)).
  `Append` is called only from `emit()`, which runs only on the producer's single
  `Run` goroutine; all the emitter's *other* fields stay unguarded and
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
├── ring.go        Event, Ring, convRing; MaxEventsPerConversation; New / Append / After / NewestID; evictOldest
└── ring_test.go   id assignment, replay/caught-up/gap, isolation, unknown-conv,
                   delta-first + all-control eviction, no-fabricated-gap, cap-1, New(<1) panic,
                   NewestID (unknown→0, last-assigned, advances-past-eviction, conv-isolation; #663), -race
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

[#663]: https://github.com/pyrycode/pyrycode/issues/663
