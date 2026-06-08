# Spec #646 — Event-stream replay: per-conversation event ring keyed by a durable event id

**Ticket:** [#646](https://github.com/pyrycode/pyrycode/issues/646) · split from #611 · EPIC #596 (Phase 2 structured streaming) · **size: S** (confirmed) · **not** `security-sensitive`

## Files to read first

- `cmd/pyry/interactive_turn_v2.go:66-105` — `interactiveTurnEmitterV2` struct + `newInteractiveTurnEmitterV2`. The emitter is the single owner of the new ring; the constructor creates it. Note the unguarded-counter / single-`Run`-goroutine contract in the struct doc (lines 35-65).
- `cmd/pyry/interactive_turn_v2.go:290-333` — `emit()`, the **one** place envelopes reach the wire. This is the single append site: the durable id is assigned once here, *before* the per-conn fan-out loop. Note `TS: time.Now().UTC()` is currently computed per-conn inside the loop.
- `internal/protocol/codes.go:99-106` — the six v2 wire-type constants (`TypeTurnState`, `TypeAssistantDelta`, `TypeToolUse`, `TypeToolResult`, `TypeTurnEnd`, `TypeStall`). `TypeAssistantDelta` is the sole droppable class; the ring classifies by `typ == protocol.TypeAssistantDelta`.
- `internal/protocol/envelope.go` — `protocol.Envelope` shape (`ID uint64`, `Type string`, `TS time.Time`, `Payload json.RawMessage`). The ring stores everything an envelope needs *except* the per-conn `ID` (which is meaningless for replay across conns — see § Durable id vs envelope id).
- `cmd/pyry/interactive_turn_v2_test.go:18-112` — existing test doubles (`fakeInteractiveBcast`, `recordedPush`, `pushTypes`, `pushesFor`) and the `package main` helpers `stubCursor`/`testConvID`/`discardLogger` (the latter in `assistant_turn_test.go:38`). Reuse these; the new emitter-integration tests are **added**, the existing ones are unchanged.
- `docs/knowledge/codebase/632.md` § Concurrency model — the emitter's named single-`Run`-goroutine, no-mutex invariant this slice must preserve.
- `docs/knowledge/codebase/633.md` § "single-writer for emitter state" — confirms the emitter instance persists across the producer's re-subscriptions and child restarts (created once in `startInteractiveTurnStreamV2`). This is what makes a ring **owned by the emitter** daemon-resident (AC-1 durability).
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md:128` — § Backpressure / replay: "the binary replays from a bounded per-conversation event ring, or emits a resync marker." This spec builds that ring; the resync wiring is #647.
- `internal/conversations/registry.go` + `devices/registry.go` — the project's mutex-guarded-struct idiom (`sync.Mutex` + map). The ring follows the same shape (`internal/conversations` is the closest analogue: a `sync.Mutex` over a per-id map).

## Context

The goal one level up is: *a phone that reconnects mid-turn catches up without a gap.* That requires a daemon-resident store of recent structured events keyed by an id stable enough to replay from. This slice is **only the storage primitive** — it ships with tests and **no reconnect wiring**. The per-conn `last_event_id` tracking and the on-reconnect replay are #647 (`security-sensitive`), which *consumes* this ring.

Two facts (from the ticket) make this net-new, not reuse:

1. **The existing envelope `id` cannot be the replay key.** `interactiveTurnEmitterV2.nextID` is incremented *per conn per envelope* inside `emit`'s fan-out loop, so the same logical event carries a different `env.ID` on each conn, and the counter is per-emitter-session. A durable, connection-independent, per-conversation id assigned **once per logical event** is required — and must not overload `nextID`.
2. **The ring is the only replay source.** `internal/conversations` holds metadata only (no message content); no `backfill_since` handler exists. Replay can only come from an in-memory ring the daemon maintains as it fans events out.

**Durability boundary (in scope):** the ring is in-memory and daemon-resident. It survives a supervised claude-**child** respawn and phone reconnects (the `pyry` process stays up across both). A full daemon-process restart loses it — **out of scope**, handled by #647's resync via the AC-5 "you missed some" signal. **Do not add disk persistence** (AC-2's bounded-memory framing assumes a purely in-memory structure; in pyrycode "supervisor restart" respawns the claude child while the daemon stays up — the ring is keyed to the daemon lifetime, not the child's).

## Design

### New package: `internal/eventring`

A self-contained, self-synchronised per-conversation ring. It lives in `internal/` (not `cmd/pyry`) because **both** sides import it: the emitter (`cmd/pyry`, append) now, and the #647 reconnect handler (`internal/relay`, query) later. `package main` cannot be imported by `internal/relay`, so the ring must be an internal package. Dependency direction: `cmd/pyry → internal/eventring` and (future) `internal/relay → internal/eventring`; the ring imports only `internal/protocol` (leaf) + stdlib. No cycles.

**Exported surface (2 types):**

```
type Event struct {
    ID      uint64          // durable per-conversation event id (>= 1)
    Type    string          // protocol.Type* wire type
    Payload json.RawMessage // the already-marshalled envelope payload
    TS      time.Time       // the logical event's timestamp
}

type Ring struct { /* sync.Mutex + map[convID]*convRing */ }

const MaxEventsPerConversation = 1024   // the AC-2 named bound; tunable, evidence-based (see Open Questions)

func New(maxPerConversation int) *Ring                                            // panics if maxPerConversation < 1 (programmer error, matches dispatch.New style)
func (r *Ring) Append(convID, typ string, payload json.RawMessage, ts time.Time) uint64
func (r *Ring) After(convID string, afterID uint64) (events []Event, gap bool)
```

`Ring` does **not** store `protocol.Envelope` directly — the envelope's `ID` is the per-conn `nextID`, irrelevant to replay. It stores the durable id + the three replay-relevant fields. #647 reconstructs `protocol.Envelope` per reconnecting conn (fresh per-conn `ID`, the stored `Type`/`TS`/`Payload`).

Internal layout (developer's choice; this is the contract, not the body): a `map[string]*convRing` where each `convRing` holds `nextID uint64` (per-conversation counter, starts at 1) and `events []Event` kept in **ascending id order** (appends are strictly increasing, and middle-deletion of a delta preserves order, so the slice is always sorted — no re-sort needed).

### Durable id assignment point (AC-1)

The id is assigned **once per logical event, before the per-conn fan-out** — `emit()` already maps 1:1 to one logical event (`transitionTo` → one `turn_state`; `emitMapped` → one content envelope; `flushDelta` → one coalesced `assistant_delta`). In `emit()`, after the single `json.Marshal`, before the `ActiveConns` loop:

- Hoist the timestamp out of the loop: `ts := time.Now().UTC()` (one timestamp per logical event; today each conn gets its own `time.Now()` — see § Behaviour change below).
- `e.ring.Append(convID, typ, payloadJSON, ts)` — assigns the durable id and stores the event. The returned id is **unused in #646** (no wire field yet; #647 surfaces it). Append unconditionally, **independent of how many conns are interactive** — including zero — because the ring is the replay source for phones that are *absent right now* and reconnect later.
- Then the existing per-conn loop runs unchanged: `e.nextID++` per conn, `protocol.Envelope{ID: e.nextID, Type: typ, TS: ts, Payload: payloadJSON}`, `Push`.

`startTurnIfNeeded`/`transitionTo`/`endTurn`/`seq` are untouched. `nextID` is untouched (still the per-conn envelope counter — **not** overloaded, per the Technical Notes). The ring's per-conversation counter is the *only* durable id source.

**Behaviour change (intentional, minor):** all conns now receive the same `TS` for one logical event (hoisted out of the loop), instead of a per-conn `time.Now()`. This is strictly more correct (one logical event = one timestamp) and makes the ring's stored `TS` identical to what every conn saw. No existing test asserts per-conn `TS` divergence (verified against `interactive_turn_v2_test.go`).

**Why all six types are appended:** every envelope flows through `emit()` (`turn_state` via `transitionTo`; the other five via `emitMapped`). So the ring records the complete v2 set. Eviction partitions them into droppable (`assistant_delta`) vs retained (the other five) per AC-3.

### Eviction policy (AC-2, AC-3)

The bound is **total events per conversation** (`MaxEventsPerConversation`), a hard cap (AC-2: "memory does not grow without limit"). On `Append`, when the conversation's slice is already at cap, evict **before** appending the new event:

1. Scan front-to-back for the **oldest `assistant_delta`** (smallest id, since the slice is id-ascending). If found, remove it.
2. Else (no delta retained — all control events) remove the **oldest event** (`events[0]`), a control event. This honours the hard memory bound even under all-control pressure.

Then append the new event at the tail (it has the highest id; order preserved). Result: deltas are sacrificed before control events ("control retained in preference to deltas"), and total per conversation never exceeds the cap. The scan is O(cap); cap is small and append is on the single emitter goroutine, so this is not a hot path.

Middle-deletion of a delta leaves the slice id-ascending but **non-contiguous** in id (e.g. retained `{1,3,4}` after delta `2` is dropped). This is by design — deltas are lossy/coalescable per ADR 025; the gap signal (below) is deliberately **not** triggered by a missing middle delta, only by the consumer falling off the *back* of the ring.

### Query contract: `After(convID, afterID)` (AC-4, AC-5)

Returns `(events []Event, gap bool)`. Three outcomes, distinguishable by the caller (#647):

| Outcome | Condition | Return | Consumer action |
|---|---|---|---|
| **Caught up** | `afterID >= latestID` (consumer has everything) | `(nil, false)` | nothing to send |
| **Gap** | the consumer's next-expected event `afterID+1` fell off the back: `oldestRetainedID > afterID + 1` | `(nil, true)` | resync (full backfill) |
| **Replay** | otherwise | `(events with ID > afterID, ascending; false)` | replay these |

where `latestID` = the highest id assigned for the conversation (`convRing.nextID - 1`) and `oldestRetainedID` = `events[0].ID`. Caught-up is checked first. The "missed some vs caught up" distinction AC-5 demands: **gap=true** vs **(gap=false, empty events)**.

Unknown conversation (no `convRing`): `afterID == 0` → caught up `(nil, false)` (a fresh consumer, nothing emitted yet); `afterID > 0` → gap `(nil, true)` (the consumer references events this daemon never had — e.g. after a daemon restart wiped the ring — so resync). This makes the AC-5 signal usable by #647 across the daemon-restart boundary the ticket scopes out here.

`never returns another conversation's events` (AC-4): the per-`convID` partition makes this structural — `After` only reads `convs[convID]`.

### Ownership & wiring (the cascade-avoidance decision)

**The emitter owns the ring; it is created inside `newInteractiveTurnEmitterV2`, not injected as a constructor parameter.**

`codegraph_callers newInteractiveTurnEmitterV2` returns **26 call sites** (1 production: `startInteractiveTurnStreamV2`; 25 tests across `interactive_turn_v2_test.go` and `interactive_turn_stream_v2_test.go`). Adding a required `*eventring.Ring` parameter would force a simultaneous edit of all 26 — exactly the #75-style "mechanical constructor cascade" the architect red lines forbid (>10 consumer call sites → split). The elegant alternative makes the change additive:

- `interactiveTurnEmitterV2` gains an unexported field `ring *eventring.Ring`.
- `newInteractiveTurnEmitterV2` sets `ring: eventring.New(eventring.MaxEventsPerConversation)` — **signature unchanged**, so the 26 call sites stay byte-identical and green.

This is sound: the emitter is the producer and the natural owner of "what I emitted, for replay"; it is created once in `startInteractiveTurnStreamV2` (daemon-lifetime, persists across the producer's re-subscriptions and child respawns — codebase/633.md), satisfying AC-1 durability. The ring's own mutex (not the emitter) handles the future cross-goroutine read.

**#647's hook (not built here, but the seam is ready):** `emitter.ring` is a `package main` field reachable from `startInteractiveTurnStreamV2`/`startRelayV2`. #647 reads it there and hands it to the v2 manager for the reconnect query — no change to the ring or the emitter's invariant required. No accessor is added in #646 (it would be speculative — the field is already reachable within `package main`).

## Concurrency model

- **The ring is the only shared object; it is internally synchronised by a `sync.Mutex`.** `Append` (write) and `After` (read) both take the lock. The lock is a leaf — held only around the map lookup + slice ops, never across a channel op or another lock, never nested.
- **The emitter's invariant is preserved unchanged.** `Append` is called only from `emit()`, which runs only on the producer's single `Run` goroutine (codebase/632/633). All the emitter's *other* fields (`inTurn`/`turnID`/`seq`/`currentState`/`nextID`/`deltaBuf`/…) stay **unguarded** — still single-goroutine. The reconciliation the Technical Notes demand: the cross-goroutine sharing lives **inside the ring's mutex**, not in the emitter; no other goroutine ever reads the emitter's unguarded fields. Belt-and-suspenders, different fabric — the emitter stays lock-free; the ring is a self-contained mutex-guarded object.
- **In #646 only `Append` is exercised in production** (no reconnect caller yet). `After` is built and unit-tested, and the mutex is in place from day one so #647 can wire `After` from the manager's goroutine without touching this slice or the emitter.
- No new goroutine is spawned (the ring is passive; the emitter remains a passive state machine).

## Error handling

- `New(maxPerConversation int)` **panics** if `maxPerConversation < 1` — a programmer error, matching `dispatch.New`/`NewV2SessionManager`'s panic-on-misconfig style for required invariants.
- `Append` has **no failure path** — it always assigns an id and stores (memory only). It does not log (a pure primitive returning a value; logging stays in the emitter, which already owns its branches). The caller (`emit`) only reaches `Append` after a successful `json.Marshal`; on a marshal error the emitter drops the event *before* the ring (no id assigned, no fan-out) — unchanged from today.
- `After` has **no failure path** — it's a pure classification returning `(events, gap)`. No panics on unknown conv (handled as a defined outcome above).
- `json.RawMessage` payloads are treated as **immutable**: `emit` owns `payloadJSON` and does not mutate it after `Append`; the ring stores the reference and `After` returns it without copying the bytes. Document this on `Append`.

## Testing strategy

Two test files. Scenarios are bullets; the developer writes them table-driven in the project idiom (stdlib `testing`, `-race`, `t.Parallel()` where safe).

**`internal/eventring/ring_test.go` (new, `package eventring`):**

- *Id assignment:* `Append` returns strictly increasing ids starting at 1 within a conversation; two conversations each start at 1 independently (per-conversation counter).
- *Replay (AC-4):* after appending ids 1..5 to conv A, `After(A, 2)` returns events `{3,4,5}` in ascending id order; `After(A, 0)` returns all retained.
- *Caught up (AC-5):* `After(A, 5)` and `After(A, 99)` → `(nil, false)`.
- *Gap (AC-5):* with a small cap, append enough control events that the oldest fall off, then query with an `afterID` below the oldest retained → `(nil, true)`. Assert it is distinguishable from caught-up (gap bool differs).
- *Isolation (AC-4):* append to A and B; `After(A, 0)` never returns a B event and vice versa.
- *Unknown conversation:* `After("never-seen", 0)` → `(nil, false)`; `After("never-seen", 5)` → `(nil, true)`.
- *Eviction — deltas first (AC-3):* with cap N, append a mix where deltas exceed headroom; assert every control event (`turn_state`/`tool_use`/`tool_result`/`turn_end`/`stall`) is still retained, the dropped events are all `assistant_delta`, and total == N.
- *Eviction — all-control hard bound (AC-2):* append N+k control events (no deltas); assert total == N and the *oldest* control events were dropped (ascending order preserved, newest retained).
- *Eviction does not fabricate a gap:* after middle-evicting a delta (retained `{1,3,4}`), `After(_, 1)` returns `{3,4}` with `gap=false` (a missing middle delta is not a back-of-ring gap).
- *Concurrency (`-race`):* one goroutine appending while another calls `After` in a loop → no race, no panic. (Exercises the mutex; the only concurrency test needed.)
- *`New(0)` panics* (and `New(-1)`); `New(1)` is the degenerate cap-1 ring (every append evicts the prior).

**`cmd/pyry/interactive_turn_v2_test.go` (additions only — existing tests unchanged):**

- *Append on emit (AC-1):* drive a turn through `Handle` (`[Thought, Text, Tool, TurnEnd]`), then `e.ring.After(testConvID, 0)` returns events whose `Type`s match the emitted sequence (`turn_state`, `turn_state`+`assistant_delta`, `tool_use`, `turn_end`+`turn_state`) with strictly increasing durable ids 1..N.
- *Once per logical event, not per conn (AC-1):* snapshot with **two** interactive conns; assert the ring records **one** entry per logical event (N entries, ids 1..N) — i.e. the durable id is assigned before fan-out, identical regardless of conn count — while `fakeInteractiveBcast.pushes` still shows 2× per envelope.
- *Append even with zero interactive conns:* empty `ActiveConns` snapshot → no pushes, but `e.ring.After(testConvID, 0)` still returns the events (the ring is the replay source for absent phones).
- *No append on empty cursor:* `stubCursor` returning `""` → `Handle` drops before `emit`; `e.ring.After(testConvID, 0)` is empty.

`make check` (vet + `-race` + staticcheck + substrate-guard) and `make build` green. No e2e in this slice — the ring is a pure unit + an additive emitter behaviour; the reconnect path that would need an oracle is #647.

## Open questions

1. **`MaxEventsPerConversation` value.** 1024 is a starting point, not load-tested — ADR 025 § Roadmap flags "the droppable-delta policy … needs a real load test." The architecture does not depend on the exact number; #647's reconnect e2e (or a Phase 2 load test) is the right place to calibrate it. Keep it a named constant so the tuning is one edit.
2. **Per-conversation vs single global counter.** This spec uses a **per-conversation** counter (each conversation's ids start at 1) — it matches AC-1's "per-conversation event id" wording and makes the ring fully partitioned. A single global daemon counter would also satisfy "strictly increases within a conversation" but couples conversations; per-conversation is the cleaner model for the single-operator cursor. If #647's wire format turns out to prefer a global id, that's a #647 decision; the ring's `Append`-returns-id contract is unaffected.
3. **Surfacing the durable id on the wire.** #646 stores the id and does not put it on any envelope. #647 decides the wire mechanism (a new envelope field, or a separate replay frame) and reads `emitter.ring` to drive replay. No seam in #646 needs to anticipate that choice beyond `After` returning the id-stamped `Event`s.

## Scope (S confirmed)

- **2 production files:** `internal/eventring/ring.go` (new, ~130 LOC incl. doc comments), `cmd/pyry/interactive_turn_v2.go` (modified, ~+10 LOC: field + constructor init + the `emit` hoist-and-append). Under the §4 ≥5-file gate.
- **2 new files total** (ring.go, ring_test.go). Under the >3-new-files red line.
- **2 exported types** (`Ring`, `Event`). Under ≤5.
- **0 consumer call sites need simultaneous update** — the `newInteractiveTurnEmitterV2` signature is unchanged by the emitter-owns-the-ring decision. This is the binding constraint that would have tripped the red line under a constructor-injection design (26 call sites); the ownership choice retires it.
- **0 reject/error branches** in the ring (Append always succeeds; After is a pure 3-way classification) — no per-branch log-call multiplier.
- **5 ACs, one cohesive primitive + one append site** — no cross-package coordination beyond the emitter calling into a leaf package it constructs.

Tests scale linearly with the scenario table over a pure data structure (one `-race` test, no flaky concurrency surface) and do not count toward the production budget. No split.
