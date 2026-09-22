# Reconnect replay (#647) — `hello.last_event_id` → ring replay / resync

> **Note (#663).** #647 shipped (PR #651, merged 2026-06-08) with a code-review
> MUST FIX outstanding: the caught-up branch of `replayMissed` set the dedup
> watermark from the *untrusted* `last_event_id`, silently suppressing the live
> stream after a `/clear`-rotated reconnect or a hostile-large id.
> [#663](../codebase/663.md) resolved it — the caught-up watermark is now clamped
> to `min(afterID, NewestID(convID))`, so the behaviour below is the shipped
> guarantee. (Defect history: [codebase/647.md](../codebase/647.md) § Known issue.)
>
> **Note (#1494).** #663 fixed the daemon side but left the phone side muted: a
> `last_event_id` beyond the conversation's id space still classified as
> *caught-up*, so the phone got no `resync`, kept its cursor, and — dedup'ing
> durably on `event_id` — dropped every live event until the ids climbed past it.
> #1494 reclassifies that input as a **gap**. Caught-up is now `afterID ==
> latestID` exactly.

The inbound **consumer** of mid-turn replay (ADR 025 § Backpressure / replay). It
closes the loop opened by the [#646](../codebase/646.md) event ring (the replay
source) and [#649](../codebase/649.md)'s `event_id` on the outbound wire (the
position a phone learns). A phone
that reconnects mid-turn advertises the last durable `event_id` it saw as
`hello.last_event_id` (`HelloClientPayload.LastEventID *uint64`, omitempty); the
manager replays the missed tail on that conn **before** the live stream resumes,
or emits a `resync` marker if the position aged out of the bounded ring.

- **The replay source is late-bound, not a config field.** `emitter` ↔ `manager`
  is a construction cycle (the emitter takes the manager as its broadcaster; the
  replay path needs the emitter-owned `eventring.Ring`, created *inside* the
  emitter constructor — [#646](../codebase/646.md)). `SetReplaySource(ring, currentConv)` publishes
  the ring + the `func() string` conversation cursor to the manager once during
  wiring, after the emitter exists. As of [#687](../codebase/687.md) the cursor
  is the `cmd/pyry` active-conversation signal (`active.CurrentConversation`), not
  `sup.CurrentConversation` (the #312 bootstrap cursor) — #678 routes turns to
  bound-session supervisors, leaving the bootstrap cursor empty, so the replay
  path re-keys to the same active-conversation signal as the live emitter or it
  re-introduces the empty-cursor drop on the reconnect-replay path. Stored under
  the existing `pushMu` leaf lock; nil ⇒ replay disabled. One call site, in
  [`startInteractiveTurnStreamV2`](../codebase/633.md). (This is the inbound
  mirror of #646's "emitter-owns-the-ring retires the constructor cascade".)
  Note `cursor()` here runs on the **manager's** `Run` goroutine — a distinct
  goroutine from the live emitter's reader; the holder's mutex makes that safe.
- **`replayMissed` classifies inline on `Run`, then hands the tail to a paced
  drain (#777).** At the `handleNoiseInit` success tail — after `noise_resp` is
  sent, `state == V2StateOpen`, and the push queue exists — the hook fires iff
  `helloPayload.LastEventID != nil`. It reads `(ring, cursor)` under `pushMu`,
  resolves `convID := cursor()` (returns early on nil source or empty cursor),
  then classifies via [`eventring.Ring.After`](eventring-package.md):
  - **replay** `(events, false)` → store the tail on the Run-owned
    `s.replayQueue []eventring.Event` and signal `m.replayCh` (cap-1,
    non-blocking); return. `drainReplayOnce` then forwards **one** event per `Run`
    pass — ascending, each carrying its original `EventID`, sealed under the fresh
    session keys via `forwardEnvelope`, advancing `s.replayThrough = ev.ID` per
    frame. **Why paced, not inline (#777):** the old inline loop sealed + forwarded
    the *entire* tail (up to `MaxEventsPerConversation = 1024` events) in one `Run`
    pass, monopolising the dispatch goroutine until the whole batch finished — a
    large replay stalled every other conn's delivery, inbound frames, wakes, and
    snapshots. Pacing mirrors the `drainOnce` push-drain pump so `Run` returns to
    its select between frames (another conn is delayed by at most one seal/forward).
    **Ordering is now preserved by a gate, not by inline completion:** while
    `s.replayQueue` is non-empty, `drainOnce` **skips** this conn (holding its live
    push queue buffered), and `drainReplayOnce` signals `drainCh` to release those
    live events once the tail empties — so replay ids (≤ `newest`, ascending) still
    reach the wire before live ids (> `newest`). The seal stays single-writer:
    every `s.send.Encrypt` is still on `Run`, only the *interleaving* of forwards
    with the select changed (see [codebase/777.md](../codebase/777.md)).
  - **caught-up** `(nil, false)` → `afterID == NewestID(convID)` exactly, or an
    unknown conversation advertising `afterID == 0`. No replay frames; the
    watermark is clamped to `min(afterID, NewestID(convID))` (#663, read before
    `After`), which since #1494 binds only in the concurrent-`Append` window
    between those two reads (see the `replayThrough` bullet).
  - **gap** `(nil, true)` → `emitResync` forwards one `resync` marker
    (`TypeResync`, inline `{conversation_id}` payload, no `EventID`), never a
    partial gap-ful replay, and returns **before** the clamp — so `replayThrough`
    is left untouched at `0` and the `forwardEnvelope` guard stays inert for the
    conn. Reached when the position aged out of the bounded window, **and** (since
    #1494) when `afterID` is beyond the conversation's id space — an id this
    daemon never issued, the post-restart shape. That second class is what stops
    an out-of-range / hostile `last_event_id` muting the subsequent live stream;
    before #1494 it fell into the caught-up branch and relied on the #663 clamp.
- **`replayThrough` per-conn watermark + `forwardEnvelope` guard.** A Run-owned
  `replayThrough uint64` on `V2Session` records the highest `event_id` delivered
  by replay; `forwardEnvelope` drops a live structured envelope whose
  `EventID <= replayThrough`, deterministically de-duplicating the transient
  replay/live overlap (a proven race, not speculative — see [codebase/647.md](../codebase/647.md)
  § Concurrency model). Envelopes with `EventID == nil` (snapshot, error, rekey,
  resync) are never dropped; conns that never advertised `last_event_id` keep
  `replayThrough == 0` and live ids are ≥ 1, so the guard is inert for them. The
  watermark is "different fabric" from the phone's own `event_id` dedup (defence
  in depth). The watermark is only ever set to a *real* ring id: the paced replay
  drain (`drainReplayOnce`, #777) advances it per forwarded frame, and the
  caught-up branch clamps it to `min(afterID, NewestID(convID))` (#663). Since
  #1494 a remote `last_event_id` beyond the conversation's id space never reaches
  that clamp — it takes the gap branch, which returns without writing the
  watermark at all, so it simply stays `0` and the guard is inert. What the clamp
  still defends is the read window: `NewestID` is read before `After`, so an
  `Append` landing between them can leave `newest` one id behind the `latestID`
  `After` classifies against, making an `afterID` that was out of range at the
  first read caught-up at the second; `min` holds the watermark at the
  stale-but-real `newest` so that concurrently appended event is delivered rather
  than muted. Narrow but live, and the clamp's direction is the safe one — it can
  only *lower* the watermark. Both writers run on `Run`, so `replayThrough` keeps
  its single-owner-goroutine regime.
- **The guard is sound by construction, not by scoping (#2022, following a gap found in #2010).** `replayThrough` is a single per-**connection** scalar with no conversation tag, and `forwardEnvelope` compares `*env.EventID <= s.replayThrough` with no conversation check either — the live emitter fans out to every interactive conn regardless of which conversation it currently addresses. Until #2022 that was unsound: `eventring.Ring.Append` assigned ids **per conversation**, each counter starting at 1, so a watermark taken for the one conversation `cursor()` resolved at handshake could sit at or above an id a *different* conversation's live event would later carry, and `forwardEnvelope` dropped that frame — no error, no resync, for the life of the connection. #2022 fixed the **id space**, not the guard: `Append` now draws from one ring-wide counter, so no future event in any conversation can ever carry an id already at or below a watermark taken from another. `forwardEnvelope` was deliberately left untouched — see [eventring-package.md](eventring-package.md) § `After` for why a conversation-aware guard was rejected in favour of fixing the ids. `docs/protocol-mobile.md` states the daemon-wide-unique guarantee.
- **Transport-down hold on the replay drain (#1490), `drainOnce`/#874's twin.**
  `drainReplayOnce` probes `transportDown()` as its first statement: while the
  relay leg is down, a pass pops nothing, seals nothing, leaves `replayThrough`
  untouched, and does **not** re-signal `replayCh` (a down leg must not
  busy-spin `Run`). Before this, a leg that dropped mid-replay left every later
  pass popping the next queued event anyway — `m.send` swallows the `Outbound`
  error, so each pass spent a real Noise send-nonce on a frame that never left
  the daemon, and the phone's next delivered frame failed AEAD, tearing the
  session down for a full re-handshake and re-replay. The probe deliberately
  sits in `drainReplayOnce`, not in `forwardEnvelope`: a transport-down error
  surfacing there would take the existing abandon branch below (`on a forward
  error the conn's remaining tail is abandoned`) and discard the held tail
  outright, the same trap the inline-reply guard's placement note documents
  (see [`dropInlineReplyIfDown`](v2-session-manager-state-machine-inline-reply-seals-gated-behind-transportdow.md)).
  Recovery needs its own wake because the hold consumes the `replayCh` signal
  without re-arming it — `Run`'s [`Reconnect` arm](v2-session-manager-state-machine-immediate-flush-on-reconnect-reconnect.md)
  re-signals `replayCh` alongside `drainCh`, so a held tail resumes on the same
  edge that flushes the push queue, still ahead of any live events `drainOnce`
  gates behind a non-empty `replayQueue`. A `Connected` seam wired without a
  `Reconnect` seam leaves a held tail with no waker until the session ends —
  production always wires both (`cmd/pyry/relay.go`), so this is a test-fixture
  trap, not a reachable production gap. Tests:
  `TestV2Session_Reconnect_ReplayHeldWhileTransportDown` (direct drive: several
  down passes leave the queue length, `replayThrough`, and `replayCh` all
  unchanged, then every frame decrypts starting from the phone's first nonce
  once the probe flips up — proof no nonce was spent while down) and
  `TestV2Session_Reconnect_MidReplayDrop_ResumesOnReconnect` (`Run`-driven: the
  `Outbound` seam itself flips the leg down after K of N replay frames so the
  drop lands deterministically mid-replay with no timer, a live event is
  pushed during the outage, and `Reconnect` alone — no further `Push` —
  resumes all N replay frames in ascending order followed by the live one,
  with no `resync`).
- **Untrusted input.** `last_event_id` is range/shape-validated by the `*uint64`
  decode (a non-integer fails `HelloClientPayload` decode → existing 4421 close),
  bounded by `MaxEventsPerConversation`, and scoped to the daemon-resolved
  `convID` — a phone can never name another conversation (AC-5; pinned by the
  cursor→B / ring-holds-A test). The replay hook sits *after* Noise IK auth + the
  device-token check, so content is only ever served to an authenticated conn.
