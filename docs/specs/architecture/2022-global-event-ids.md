# #2022 — daemon-wide unique event ids in `internal/eventring`

Make `eventring.Ring` assign event ids from **one monotonic counter per ring**
instead of one per conversation, so the per-connection replay watermark
`V2Session.replayThrough` can never be at or above an id a future event in a
*different* conversation will carry. `forwardEnvelope`'s dedup guard becomes
sound by construction and is not touched.

## Files read

- `internal/eventring/ring.go` → `Ring`, `convRing`, `Append`, `evictOldest`,
  `After`, `NewestID` — the whole behaviour under change. `convRing.nextID` is
  both the id source and the `latestID` classification boundary; splitting those
  two roles is the core of this ticket.
- `internal/eventring/ring_test.go` → `TestAppend_IDsStrictlyIncreasePerConversation`,
  `TestNewestID`, `TestAfter_GapWhenOldestFellOff`, `TestAppend_AllControlHardBound`,
  `TestAfter_MiddleDeltaEvictionNoGap`, `TestAppend_CapOne`,
  `TestAppend_EvictsDeltasFirst` — the classification cases the new gap test must
  keep green, and the two tests that assert the old semantics and must invert.
- `internal/relay/v2session_replay.go` → `replayMissed` (the `NewestID`/`After`
  read pair and the `min` clamp), `emitResync`, `drainReplayOnce` (the trailing
  watermark advance) — the sole consumer of the id space's shape.
- `internal/relay/v2session.go` → `forwardEnvelope` — the drop site whose comment
  carries the soundness argument this ticket replaces. **Not changed behaviourally.**
- `internal/relay/v2session_replay_test.go` → `reconnectOpenLive`,
  `appendRingEvents`, `buildHelloEarlyDataReplay`,
  `TestV2Session_Reconnect_ClearRotation_LiveStreamDelivered`,
  `TestV2Session_Reconnect_SameConversation_DedupPreserved` — the harness AC-2's
  regression test is built from, and the two tests that pin the clamp end to end.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2` (the `ring`
  field's doc block) and `emit` (the `Append` call site's doc block) — the two
  live-path phrases asserting per-conversation counting.
- `cmd/pyry/interactive_turn_v2_test.go` → `TestInteractiveTurnEmitterV2_RingIDIsPerEventNotPerConn`
  and the emitter test above it — both `ring ids not 1..N` assertions use a single
  `testConvID` against a fresh emitter, so they still hold. Confirmed, not assumed
  (Technical Notes).
- `internal/protocol/envelope.go` → `Envelope.EventID` doc; `internal/protocol/handshake.go`
  → `HelloClientPayload.LastEventID` doc — the two wire-type doc blocks that state
  the id is per-conversation.
- `docs/protocol-mobile.md` → the `event_id` and `envelope-id` field-table rows,
  § *Interactive events*' "Replay cursor" paragraph, § `hello`'s `last_event_id`
  paragraph, § *Reconnect replay & resync*' "Beyond the id space" bullet, and
  § `slash_command_list`'s "What is *not* a loss point" paragraph — the published
  contract carrying the same defect.
- `docs/knowledge/features/eventring-package.md` § "`After` — the three-way replay
  contract" — the table that documents the aged-out branch as an *inference from
  contiguity* (`oldestRetainedID > afterID+1`). That framing is what breaks under
  a global id space and is why § Design replaces the branch's basis rather than
  leaving it alone. (Read-only; the documentation phase owns that file.)

## Context

`eventring.Ring.Append` keys its counter by conversation, so every conversation's
ids start at 1 and the id spaces overlap. `replayMissed` resolves exactly one
conversation from `cursor()` at handshake and stores
`min(afterID, NewestID(convID))` into the untagged per-connection scalar
`V2Session.replayThrough`. `interactiveTurnEmitterV2.emit` fans out to every
interactive connection regardless of conversation. So a connection that
handshakes with an in-range cursor for conversation A, and then sees the daemon
rotate to conversation B, has every B event with id ≤ that watermark dropped
silently by `forwardEnvelope` — no frame, no error, no resync.

The direction is decided in the ticket: fix the **id space**, not the guard. The
same scalar-cursor-against-per-conversation-ids category error exists in the
published client contract (§ *Reconnect replay & resync* tells clients to dedup
by `event_id`), and only a global id space fixes both ends with no
`pyrycode-mobile` change. Making `forwardEnvelope` conversation-aware is
explicitly out of scope — it would need the conversation plumbed through `Push`
to the drop site, and it would be a second mechanism for a failure the first one
already removes.

No ADR is warranted: this is a correction inside an existing decision (ADR 025
§ Backpressure / replay), not a new one. `docs/knowledge/features/eventring-package.md`
needs its `After` contract table updated by the documentation phase — the
`oldestRetainedID > afterID+1` row is the statement this ticket invalidates.

## Sizing — one deliberate overage

Re-counted against this written plan, not against the sketch: **6 production
source files** where the size-S ceiling is 5. Every other line of the table holds
with room — ~520 lines of total written work against 800, 0 new exported types, 0
consumer call sites (`Append`'s signature is unchanged), 4 acceptance criteria, 0
new reject branches.

The overage is not split, because the only available cut is (a) the ring
behaviour plus its tests and (b) the comment and published-contract corrections,
and (b) is a one-consumer slice: it has no observable behaviour, no gate that
reddens, and its sole consumer is (a). AC-4 exists precisely so the published
contract does not outlive the change it describes. The floor rule takes
precedence over the ceiling here — a ticket that cannot be verified on its own is
the failure no resume fixes, whereas this overage is three files of one phrase
each. Stated, and built.

## Design

### The id space

`Ring` gains the counter; `convRing` keeps a per-conversation *view* of it.

```go
type Ring struct {
    mu         sync.Mutex
    nextID     uint64 // the next id to assign, ring-wide; starts at 1
    maxPerConv int
    convs      map[string]*convRing
}

type convRing struct {
    latestID       uint64 // highest id assigned to THIS conversation; 0 if none
    evictedThrough uint64 // highest id removed from the FRONT of this window; 0 if none
    events         []Event
}
```

`New` initialises `nextID: 1`. `Append`'s signature, return type and eviction
policy are unchanged; it takes `id := r.nextID`, increments the ring counter, and
records `c.latestID = id`. Ids are therefore strictly increasing in ring-wide
append order, unique across conversations, still `>= 1`, and still strictly
increasing within each conversation (a conversation's ids are a subsequence of a
strictly increasing sequence) — but ascending-not-contiguous within one.

`NewestID` returns `c.latestID` (0 for an unknown conversation), preserving its
exact contract: the highest id ever assigned to that conversation, advancing
independent of retention, always equal to the highest *retained* id because the
newest event is never evicted.

### `After`'s aged-out branch — the one real behaviour change

The current gap test is `c.events[0].ID > afterID+1`. It is an **inference from
contiguity**: "the consumer's next id is `afterID+1`; the oldest retained id is
past it, so the event it wants fell off the back." Under a global id space a
conversation's ids are sparse, so the inference misfires in exactly the shape
AC-3 names. With conversation B holding ids `{41, 42, 55}` and nothing evicted, a
cursor of 0 or 41 is perfectly replayable, yet `41 > 1` reports a gap — a
spurious `resync`, i.e. a full client reload.

Replace the inference with the fact it was inferring. `evictOldest` already knows
whether it is removing from the front (`idx == 0`, the all-control / oldest-delta-
is-oldest-event case) or from the middle (a delta with older events still
retained). Record the front case:

- `evictOldest`: when `idx == 0`, set `c.evictedThrough = c.events[0].ID` before
  the removal. Middle removals leave it alone — they are delta evictions, which
  the ring's contract has always said are **not** a gap.
- `After`: the branch becomes `if afterID < c.evictedThrough { return nil, true }`.

`evictedThrough` is monotonic: position 0 always holds the smallest retained id,
and the slice stays ascending, so successive front removals remove ascending ids.
Every id of this conversation `<= evictedThrough` is gone; nothing above it has
been removed except middle deltas.

The other three branches are unchanged, in the same order: `afterID > latestID` →
gap (#1494, now reading `c.latestID`); `afterID == latestID` → caught up;
otherwise the ascending-scan replay.

**Where this differs from today, for a single contiguous conversation.** Only
after a middle delta eviction whose hole the front has since slid past: old code
reads the hole as a back-of-ring gap once `events[0].ID` climbs above it, new
code does not. That is the ring's own documented policy ("A missing middle delta
is NOT a gap") applied consistently, and it can only *deliver more*, never mute.
Every other single-conversation case is bit-identical — checked by hand against
all seven existing `After`/eviction tests before writing this, and pinned by the
table-driven test below.

### What does not change

- `Append`'s signature — no consumer call sites (Technical Notes).
- `forwardEnvelope`'s guard — one line, still `env.EventID != nil && *env.EventID
  <= s.replayThrough`. Only its justifying comment changes.
- `replayMissed`'s `min(afterID, newest)` clamp, its read ordering, its gap→resync
  branch, `emitResync`, `drainReplayOnce`'s trailing watermark advance.
- `MaxEventsPerConversation`, the eviction policy, and every statement about
  **retention** being per-conversation. Those stay true and stay written.

## Concurrency model

No new goroutines, no new locks, no lock-ordering change. `Ring.nextID`,
`convRing.latestID` and `convRing.evictedThrough` are all read and written under
the existing `Ring.mu` leaf lock, exactly like `convRing.nextID` is today. The
counter moving from the per-conversation struct to the ring struct does not
change what is guarded or for how long — both were already inside the same
critical section.

`replayMissed`'s `NewestID`-then-`After` read pair stays two separate lock
acquisitions, so the interleaving window it documents still exists and the `min`
clamp still covers it. The staleness direction is unchanged: a concurrent
`Append` can only leave `newest` low, which lowers the watermark, which delivers
more.

## Error handling

Unchanged. `Append` and `After` still have no failure path; `New` still panics on
`maxPerConversation < 1`. `uint64` overflow of the ring-wide counter is not a new
concern in any practical sense — it needs 2^64 appends in one daemon lifetime —
and the counter was already unbounded per conversation.

The one *classification* failure mode this touches is the spurious gap, and its
blast radius is why it gets its own table-driven test: a spurious gap is a
`resync`, i.e. a full client reload, on a connection that needed no reload.

## Testing strategy

`internal/eventring/ring_test.go`:

- **Two existing tests invert** (Technical Notes).
  `TestAppend_IDsStrictlyIncreasePerConversation`'s "conv B first id … want 1
  (independent counter)" and `TestNewestID`'s "conversations are independent"
  subtest both assert the removed semantics. Rewrite each to assert the new
  guarantee rather than deleting it, and rename the first to name what it now
  pins.
- **AC-1, ids unique daemon-wide.** Append in an interleaved order across three
  conversations; assert the full return sequence is strictly increasing, that no
  id repeats, that every id is `>= 1`, and that each conversation's own
  subsequence is strictly increasing. Interleave rather than append per
  conversation in blocks — the block order is the case a per-conversation counter
  would also pass.
- **AC-3, `After` against a sparse id space** — table-driven, one row per
  `(afterID, want events, want gap)` over a fixture whose first id is well above
  1: a fresh cursor (0), a cursor at the conversation's first id, one at its
  latest (caught up), one past its latest and `math.MaxUint64` (gap), one landing
  on an id belonging to *another* conversation between two of this one's, and —
  with eviction forced — one at and one below `evictedThrough`. This is the
  branch the Technical Notes single out.
- **The `evictedThrough` bookkeeping under mixed eviction:** a fixture that
  middle-evicts a delta and then front-evicts past it, asserting the surviving
  cursor is still replayable. This is the one case where behaviour differs from
  today, so it is asserted rather than left implicit.

`internal/relay/v2session_replay_test.go`:

- **AC-2, the reported drop, red before the change.** Append 3 events for
  conversation A; handshake advertising `last_event_id = 3` — **in range**, so
  `replayMissed` reaches the clamp (`min(3, 3) = 3`) and emits no resync, which
  is what separates this from `TestV2Session_Reconnect_ClearRotation_LiveStreamDelivered`
  (that one lands in the #1494 gap branch and returns before the clamp). Then
  `Append` one event for conversation B and `Push` it as a live structured
  envelope stamped with the returned id. Assert it reaches the wire carrying that
  id. Pre-change B's id is 1, `1 <= 3`, the frame is dropped and the envelope wait
  times out; post-change it is 4. The red run goes in the PR body.
- **AC-3's "unchanged for every case that already works" is covered by the
  existing suite** — `ReplaysMissedTail`, `CaughtUp_NoReplay`,
  `BeyondNewest_EmitsResync`, `Gap_EmitsResync`, `OutOfRangeLastEventID_LiveStreamDelivered`,
  `ClearRotation_LiveStreamDelivered`, `SameConversation_DedupPreserved`,
  and the paced-replay set. They are single-conversation or already reach the gap
  branch, so they must stay green untouched. Any edit to one of them would be a
  signal the change broke something, not a fixture update.

Gate: `go test -race ./internal/eventring/... ./internal/relay/... ./cmd/pyry/...`,
`go vet ./...`, `go build ./cmd/pyry`.

## Open questions

1. **Does `evictedThrough` need to account for a middle eviction that is also the
   conversation's oldest *retained* control event?** Resolved during design: no.
   `idx == 0` is precisely "the removed event is the oldest retained", whatever
   its class; any `idx > 0` leaves an older event in place, so the back of the
   window has not moved. Recorded here because the branch reads as class-based
   and is not.
2. **Do the `cmd/pyry` emitter tests need fixture changes?** Confirmed no — both
   `ring ids not 1..N` assertions use one conversation against a fresh emitter.
   If a `cmd/pyry` test reddens in Phase B, the plan was wrong and the departure
   gets a `## Revisions` entry.
3. **Which changelog date does the `docs/protocol-mobile.md` entry carry?** The
   merge date is not knowable at write time; use the implementation date, matching
   every existing entry's convention.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new finding, and the existing boundary is *narrowed*
  by this change. `hello.last_event_id` is the untrusted crossing; it enters at
  `handleNoiseInit` and reaches exactly one consumer, `replayMissed`, which uses
  it only as an argument to `ring.After` and `min`. The four properties #1494
  established survive by inspection: **(a)** an id the daemon never issued still
  classifies as a gap — the `afterID > c.latestID` branch is unchanged in position
  and meaning, and under global ids it is *harder* to satisfy accidentally, since
  an id minted for another conversation now exceeds this conversation's `latestID`
  more often rather than colliding with it; **(b)** the untrusted value is still
  never written to per-conn state on the gap path — `emitResync` returns before
  `s.replayThrough` is assigned, and that ordering is untouched; **(c)** work
  stays bounded by ring retention — `After` still returns at most
  `MaxEventsPerConversation` events, and the new branch is a scalar compare that
  adds no iteration; **(d)** no payload bytes reach the logs — this ticket adds no
  log call at all.
- **[Trust boundaries — the new one to name]** The `evictedThrough` comparison is
  the only place attacker-controlled `afterID` meets new state. It is a `uint64`
  compare against a value derived solely from ids the daemon itself minted; there
  is no arithmetic on `afterID` in the new branch. That matters concretely: the
  *old* branch computed `afterID+1`, which wraps to 0 at `math.MaxUint64` and
  would have made the comparison `c.events[0].ID > 0` — trivially true, so the
  hostile-max case reached a gap through a wrapped intermediate rather than
  through the intended `afterID > latestID` branch above it. The new branch
  removes that arithmetic entirely. `TestAfter_GapBeyondIDSpace` keeps
  `math.MaxUint64` pinned, and the AC-3 table adds it against a sparse fixture.
- **[Concurrency]** No finding. Same single leaf mutex, same critical sections,
  no new lock and no nesting. `Ring.nextID` is written only under `mu` in
  `Append`; `convRing.latestID` / `evictedThrough` likewise. The one cross-call
  race that exists — `replayMissed`'s `NewestID`-then-`After` window — is
  unchanged in shape, and its staleness still resolves in the deliver-more
  direction via `min`. `TestRing_ConcurrentAppendAfter` continues to cover the
  append-vs-query race under `-race`.
- **[Error messages, logs, telemetry]** No finding. No log statement is added,
  removed or edited. `emitResync` still logs only `conn_id` and the daemon's own
  resolved `conversation_id`, never `afterID` and never payload bytes.
- **[Network & I/O]** No finding — no new socket read, no new size limit needed.
  Replay volume is still capped by `MaxEventsPerConversation`, and the paced
  drain (`drainReplayOnce`, one frame per `Run` pass) is untouched, so a hostile
  in-range cursor cannot monopolise `Run`.
- **[Threat model alignment]** `docs/protocol-mobile.md` § *Reconnect replay &
  resync* holds that a phone can never address another conversation's events.
  Unchanged and structural: `After` still reads `convs[convID]` only, with
  `convID` resolved from the daemon's own cursor. The global counter does not let
  a cursor reach across conversations — it only removes the *collision* between
  their id spaces. **A residual worth stating rather than hiding:** with global
  ids, a client that observes `event_id`s for conversation A can infer the daemon's
  total structured-event volume across all conversations from the gaps between
  them. The conversations belong to the same single operator on the same daemon,
  the connection is already authenticated and interactive-granted, and no
  content, id or name of another conversation leaks — only a count. Accepted, not
  mitigated.
- **[Tokens / File operations / Subprocess / Cryptographic primitives]** Not
  applicable, and not by omission: this change touches an in-memory `uint64`
  counter and one comparison. No credential, no filesystem path, no `exec`, no
  randomness, no key or nonce is read or written on any path in the diff. The
  Noise send-nonce sequence is the one crypto-adjacent invariant nearby, and it
  is preserved because `forwardEnvelope` is not edited: the guard still drops
  *before* `s.send.Encrypt`, so a dropped frame consumes no nonce.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
