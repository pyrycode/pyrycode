# Spec #609 — Delta coalescing on the bridge (per JSONL message or ~250 ms)

**Ticket:** #609 (EPIC #596 Phase 2 structured streaming). **Size:** S (held; not downgraded — see § Sizing).
**Label:** not `security-sensitive` (re-affirmed at refinement — coalescing sits *upstream* of the capability gate / per-conn fan-out, makes no routing or trust decision, accepts no untrusted input). No security-review pass required.
**Depends on:** #633 (live producer wiring, merged) — the structured stream this layer coalesces is live on `main`.

## Files to read first

Turn-1 reading list. Read these before writing code; the design below references them by the same paths.

- `cmd/pyry/interactive_turn_v2.go` (whole file, ~266 lines) — **the primary edit target.** The passive `interactiveTurnEmitterV2`: the `Handle` type-switch, the `TextChunk` arm (`transitionTo` → `emitMapped` → `seq++`), the non-text arms, `emitMapped`/`emit`, and the "all lifecycle fields are plain — no atomic, no mutex — `Handle` runs only on the producer's single Run goroutine" contract (struct doc, lines 27-64). Coalescing is a buffer + flush layer added here.
- `internal/turnbridge/producer.go:81-118` — `Producer.Run` outer loop + `drain` select (`ctx.Done` / `<-ch`). The ~250 ms timer becomes a **third arm** of this select; `Config`/`Producer` gain the seam (lines 38-73). This is the only file in `internal/` you touch.
- `internal/turnbridge/producer_test.go:48-100` — `&Producer{onEvent:…, log:…}` direct-construction idiom + `collector`. Your producer-level timer test mirrors this; existing tests must still compile (additive zero-value fields).
- `internal/turnbridge/outbound.go:62-104` — `MapEvent`: `turnevent.TextChunk` → `protocol.TypeAssistantDelta` + `AssistantDeltaPayload{ConversationID, TurnID, Seq, Text}`. The flush reconstructs a synthetic `TextChunk{MessageID, Text: concatenated}` and reuses `emitMapped`; **no new payload-construction path.**
- `internal/turnevent/event.go:30-34` — `TextChunk{MessageID string; Text string}`. `MessageID` is the coalescing key (#606).
- `cmd/pyry/interactive_turn_stream_v2.go:45-82` — #633 wiring (`startInteractiveTurnStreamV2`). The `OnEvent: func(ev){ emitter.Handle(ctx, ev) }` closure captures the relay lifecycle ctx; you add the symmetric `FlushSignal`/`OnFlush` wiring here. This is where the ~250 ms window constant is plumbed.
- `cmd/pyry/interactive_turn_v2_test.go` (whole file) — `fakeInteractiveBcast` (records `recordedPush`, per-conn `Push` error, scripted `ActiveConns`), `stubCursor`/`.set(id)`, `testConvID`, `discardLogger`, and the helpers `assistantDeltas(t, pushes)`, `pushTypes`, `pushesFor`, `turnStateValues`. **The existing 10 scenarios must be re-verified against coalescing (see § Testing) — this is real work, not a rubber stamp.**
- `docs/knowledge/codebase/632.md` — the emitter's design, the single-Run-goroutine assumption, the no-app-output-log contract, the envelope-ID policy. Inherited verbatim.
- `docs/protocol-mobile.md:478-485` — `assistant_delta` wire fields. **Already documents `text` as "coalesced (not per token)"** — the wire contract is unchanged; this ticket makes the daemon honor it. **Do not edit this doc.**
- ADR 025 § Phase 2 (`docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md`) — `"AssistantText → coalesced assistant_delta (per JSONL message or ~250 ms, not per token)"`.

## Context

The #632 emitter maps **every** `turnevent.TextChunk` to its own `assistant_delta` envelope (`Handle`'s `TextChunk` arm → `emitMapped` → `seq++`). The #615 producer (live-wired by #633) yields one `TextChunk` per JSONL `assistant` line, and a single streamed assistant message can surface as several same-`MessageID` lines — so the phone receives a burst of tiny deltas for one logical message. ADR 025 § Phase 2 specifies the fix: coalesce deltas per JSONL message **or** ~250 ms, whichever comes first.

This is the **structured (`assistant_delta`) path only**. It does **not** touch #589's coarse `message` path, the capability gate, or the per-conn fan-out in `emit` — those stay exactly as #632 shipped them. Coalescing is a buffer-and-flush layer inserted between `Handle`'s `TextChunk` arm and `emit`.

## Design

### The one real design wrinkle (and why it shapes everything)

The #632 emitter is deliberately **passive**: it spawns no goroutine, owns no queue, and all lifecycle fields are plain (no atomic, no mutex) because `Handle` runs **only** on the producer's single `Run` goroutine. A free-running ~250 ms timer flushing from a *second* goroutine would race those unguarded counters. The flush must therefore stay on the producer's single `Run` goroutine.

AC#2 also requires the timer to be **reset on each flush** (a full window after every flush, so a boundary-flush is not immediately followed by a near-empty timer-flush). Reset-on-flush can only be driven by the component that knows when a flush happened — the **emitter** — because the producer is blind to the emitter's internal message-boundary / non-text flushes.

Reconciling both constraints fixes the design:

> **The emitter owns a `*time.Timer`; the producer's `drain` select reads the timer's channel and routes the fire back into the emitter on the same goroutine.** The emitter arms/stops the timer only from `Handle`/the flush helper (i.e. from inside the producer's `OnEvent`/`OnFlush` callbacks — the producer Run goroutine). No second goroutine, no mutex, no flushing goroutine inside the emitter. This is the "service the timer in the producer's select loop, call the emitter from there" scheme the ticket sanctions.

Go 1.26 (`go.mod`: `go 1.26.2`) gives the modern `time.Timer` semantics: after `Stop`/`Reset` returns, no stale value is received from the channel. So the single-goroutine select + emitter-driven `Reset`/`Stop` is correct **without** the legacy `if !t.Stop() { <-t.C }` channel-drain dance (which would be wrong here — the channel is read by the producer's select, not by the resetting code). The spec relies on this; do not add a manual drain.

### Emitter changes — `cmd/pyry/interactive_turn_v2.go`

New unexported state on `interactiveTurnEmitterV2` (all plain fields, read/written only on the single Run goroutine — same contract as the existing fields):

| field | role |
|---|---|
| `deltaBuf strings.Builder` | accumulated assistant text for the open (un-flushed) delta |
| `deltaMsgID string` | `MessageID` of the buffered text; meaningful only while `deltaBuf.Len() > 0` |
| `deltaConvID string` | conversation cursor captured when buffering began, used by the flush emit |
| `flushTimer *time.Timer` | the ~250 ms latency timer; **owned here, selected by the producer** |

New package-level constant in this file: `coalesceWindow = 250 * time.Millisecond` (the emitter Resets its timer with this; the producer stays window-agnostic).

`newInteractiveTurnEmitterV2` creates the timer **disarmed**: `time.NewTimer(coalesceWindow)` then `Stop()` (Go 1.26 — no drain needed). The emitter exposes a tiny accessor `flushC() <-chan time.Time { return e.flushTimer.C }` for the producer wiring, and the flush entry point `flushDelta(ctx context.Context)` (unexported; the wiring closure, same `package main`, calls it as the producer's `OnFlush`).

**`flushDelta(ctx)` contract** — emit the buffered text as **one** coalesced `assistant_delta` (current `turnID` + `seq`), advance `seq` once, clear the buffer, stop the timer. **No-op when the buffer is empty** (this is what makes a timer fire on an empty buffer harmless). Implementation: reconstruct `turnevent.TextChunk{MessageID: e.deltaMsgID, Text: e.deltaBuf.String()}`, call the existing `emitMapped(ctx, e.deltaConvID, …)`, then `e.seq++`, `e.deltaBuf.Reset()`, clear `deltaMsgID`/`deltaConvID`, `e.flushTimer.Stop()`. Invariant asserted by tests: **`seq` advances once per emitted coalesced delta, never per buffered `TextChunk`.**

**`Handle` `TextChunk` arm — rewritten** (replaces the current `transitionTo` → `emitMapped` → `seq++`):
1. `startTurnIfNeeded` (unchanged guard).
2. **Message-boundary flush:** if `deltaBuf.Len() > 0 && chunk.MessageID != e.deltaMsgID`, call `flushDelta(ctx)` — emits the prior message's delta before starting the new one.
3. `transitionTo(ctx, convID, StateResponding)` — unchanged. *(Note: this only emits a `turn_state` on the first content of a turn, when the buffer is necessarily empty; during a text run the state is already `responding`, so `transitionTo` is a no-op and never emits an envelope ahead of buffered text. Ordering is safe by construction.)*
4. Record `deltaConvID = convID`, `deltaMsgID = chunk.MessageID`; capture `wasEmpty := deltaBuf.Len() == 0` **before** appending; `deltaBuf.WriteString(chunk.Text)`.
5. **Arm the timer only on empty→non-empty:** `if wasEmpty { e.flushTimer.Reset(coalesceWindow) }`. Do **not** re-arm on a subsequent same-message append — the timer measures latency from the *oldest* unflushed chunk, which is what bounds a long-streaming message's delivery to one window.

**`Handle` non-text arms — flush first.** Every arm that emits a non-`TextChunk` envelope (`ThoughtChunk`, `ToolStart`, `ToolUpdate`, `TurnEnd`, `Stall`) calls `flushDelta(ctx)` as its **first** emitting action, *after* its `startTurnIfNeeded`/`inTurn` guard and *before* its `transitionTo`/`emitMapped`. This preserves wire ordering: buffered text emits before the interleaved `turn_state` / `tool_use` / `tool_result` / `turn_end` / `stall` that logically followed it. `flushDelta` is a no-op when the buffer is empty, so arms with no pending text are unaffected. Concretely:
- `ThoughtChunk`: `start` → `flushDelta` → `transitionTo(thinking)`.
- `ToolStart` / `ToolUpdate`: `start` → `flushDelta` → `transitionTo(responding)` → `emitMapped`.
- `TurnEnd`: `inTurn` guard → `flushDelta` → `emitMapped(turn_end)` → `transitionTo(idle)` → `endTurn`. The flush at `TurnEnd` is the turn-boundary flush AC#3 requires (delta before `turn_end`, before the `idle` `turn_state`).
- `Stall`: `flushDelta` → `emitMapped(stall)`. (Stall keeps its no-lifecycle-mutation shape; it only gains the leading flush.)

`endTurn` need not touch the buffer — the `TurnEnd` arm already flushed it. The buffer is only ever non-empty inside an open turn.

### Producer changes — `internal/turnbridge/producer.go`

`Config` gains two **additive, optional** fields (zero values = disabled; default behavior and all existing tests unchanged):

```go
// FlushSignal, if non-nil, is selected in the drain loop; each receive
// invokes OnFlush on the single Run goroutine. The owning consumer drives
// the timer behind this channel (arm/reset/stop), so a "reset on flush"
// policy stays single-goroutine-safe. nil ⇒ no periodic-flush arm.
FlushSignal <-chan time.Time
// OnFlush, if non-nil and FlushSignal fires, runs on the Run goroutine —
// the same goroutine as OnEvent — so a consumer may flush state it mutates
// across OnEvent calls without a lock. nil ⇒ ignored.
OnFlush func()
```

`Producer` gains the matching unexported fields; `New` copies them from `Config`. `drain` adds a third select arm using the nil-channel idiom (a nil `flushSignal` arm never fires, so the disabled path is a single code branch, no duplicated select):

```
select {
case <-ctx.Done():       return
case <-p.flushSignal:     if p.onFlush != nil { p.onFlush() }   // new arm
case ev, ok := <-ch:      … existing …
}
```

The producer stays **generic** — it knows "select a flush signal and call back on the Run goroutine," not "coalesce assistant deltas." All coalescing policy lives in the emitter. This mirrors the existing ctx-less `OnEvent` seam exactly (the emitter bridges ctx via the wiring closure).

### Wiring changes — `cmd/pyry/interactive_turn_stream_v2.go`

In `startInteractiveTurnStreamV2`, after constructing `emitter`, add to the `turnbridge.Config`:
- `FlushSignal: emitter.flushC()`
- `OnFlush: func() { emitter.flushDelta(ctx) }` — captures the relay lifecycle ctx, the symmetric partner of the existing `OnEvent` closure; runs only on the producer Run goroutine.

No other wiring change. The producer build / goroutine / cleanup are untouched.

### Data flow

```
producer.Run (single goroutine)
  └─ drain select:
       ├─ <-ch (tui-driver event) ─→ mapEvent ─→ OnEvent ─→ emitter.Handle(ctx, ev)
       │     TextChunk  : boundary-flush if id changed → buffer text → arm timer if was empty
       │     non-text   : flushDelta(ctx) first → then emit the non-text envelope
       │     TurnEnd     : flushDelta(ctx) → turn_end → idle
       └─ <-flushSignal (emitter.flushTimer.C) ─→ OnFlush ─→ emitter.flushDelta(ctx)
                                                              (emit coalesced delta, seq++, stop timer)
```

The same `flushDelta` is reached from two select arms of **one** goroutine — never concurrently. `emit` (and its capability gate / per-conn `nextID` / fan-out) is reached only via `flushDelta`→`emitMapped`→`emit`, unchanged from #632.

## Concurrency model

- **No new goroutine, no mutex, no atomic.** `flushDelta` is invoked from `Handle` (inside `OnEvent`) and from `OnFlush`, both of which run on the producer's single `Run` goroutine. The `drain` select processes exactly one arm at a time, so `Handle` and the timer-driven flush never overlap. All emitter fields (existing + the four new ones) remain single-goroutine — `go test -race` clean, #632's named single-Run-goroutine assumption preserved verbatim.
- **Timer ownership without a timer goroutine.** The `*time.Timer`'s runtime fire is not "a second emitter goroutine" — the emitter never reads its own channel; the producer's select does. `Reset`/`Stop` are called only from the Run goroutine (via `Handle`/`flushDelta`). Go 1.26 guarantees no stale fire is delivered after `Stop`/`Reset`, so a boundary-flush that stops the timer cannot be followed by a phantom timer-flush on the next select.
- **`Envelope.ID` (`nextID`) unaffected** — still per-conn-per-envelope, session-monotonic, never reset across turns (#632/#589 policy). Coalescing reduces the *count* of `assistant_delta` envelopes but not the counter's monotonicity.
- **Teardown.** On ctx cancel, `Handle`/`flushDelta` see a cancelled ctx → `ActiveConns` returns nil and `Push` returns `ctx.Err()` → `emit` returns early; the timer is stopped via `defer`-free natural GC once the emitter is unreferenced (the timer holds no goroutine). No flush is forced on drain exit (see Open questions).

## Error handling

- **Marshal / Push errors:** unchanged — they live in `emit`, which `flushDelta` reaches through `emitMapped`. The #632 contracts hold (marshal error → DEBUG log without payload/`err.Error()`; per-conn `Push` error → DEBUG log of transport sentinel + `continue`, never aborts the fan-out).
- **No-cursor drop:** `Handle` still drops events on an empty cursor before any buffering, so the buffer never fills without a conversation id. `deltaConvID` is captured from the same non-empty cursor that admitted the chunk.
- **Empty-buffer flush:** any flush trigger (timer, boundary, non-text, turn-end) on an empty buffer is a defined no-op — never an empty `assistant_delta`.
- **No new error path, no new sentinel, no new exported type.**

## Testing strategy

Stdlib `testing`, `-race`, table/scenario-driven. Reuse the existing `interactive_turn_v2_test.go` fakes/helpers; producer-arm test reuses `producer_test.go`'s direct-construction idiom.

**New emitter scenarios** (drive `Handle` with scripted `[]turnevent.Event`; for a "timer fire" use a **direct `e.flushDelta(ctx)` call** to stay deterministic — no real-time waits in emitter tests). Describe inputs + expected pushes; the developer writes them in the file's idiom:
- **Same-id coalesce** — `[Text(m1,"Hel"), Text(m1,"lo"), TurnEnd]` → exactly **one** `assistant_delta{text:"Hello", seq:0}`, emitted before `turn_end`. (per-JSONL-message batching)
- **New-id boundary flush** — `[Text(m1,"A"), Text(m2,"B"), TurnEnd]` → two deltas `{text:"A", seq:0}` then `{text:"B", seq:1}`; the `m2` chunk flushes `m1` before buffering.
- **Timer flush mid-message** — `[Text(m1,"A"), Text(m1,"B")]` then `e.flushDelta(ctx)` then `[Text(m1,"C"), TurnEnd]` → `{text:"AB", seq:0}` then `{text:"C", seq:1}` (a long message split across the window keeps one rising `seq` sequence).
- **Flush before tool / thought / stall** — `[Text(m1,"A"), Tool…]`, `[Text(m1,"A"), Thought…]`, `[Text(m1,"A"), Stall]` → the `assistant_delta{"A"}` precedes the `tool_use` / `turn_state{thinking}` / `stall` in the push order.
- **Flush at turn boundary** — `[Text(m1,"A"), TurnEnd]` → push order `… , assistant_delta{"A"}, turn_end, turn_state{idle}` (delta before `turn_end`, before `idle`).
- **One seq per coalesced delta** — three same-id chunks + `TurnEnd` → one delta with `seq:0`; `seq` did not advance three times.
- **No app-output log leak** (AC#4) — coalesced text with distinctive substrings across the most log-heavy path (per-conn `Push` error fired) → none of the substrings appear in any log line, and the buffered text never appears in a log field.
- **Race-clean** — the new scenarios run under `-race` (single-goroutine by construction).

**Existing 10 scenarios — re-verify and update (real work, budget for it).** Coalescing changes the emitter's observable output: text no longer emits per chunk. The good news — **single-text-chunk scenarios are unchanged**, because flush-before-non-text / flush-at-turn-end emits the delta at the *same wire position* it occupied before. The scenarios that need attention:
- `PerTurnSeqReset` — if its two per-turn `TextChunk`s share a `MessageID` they now coalesce to one delta (seq 0, not 0/1). Give them **distinct** `MessageID`s (so the second flushes the first → seq 0,1) and ensure a trailing flush (the existing `TurnEnd`).
- `InterleaveDeDup` — ends on a `Text` with no trailing flush; its trailing delta would stay buffered. Confirm it asserts `turn_state` order only (unaffected) or add a terminal `TurnEnd`.
- `MonotonicEnvIDAcrossTurns`, `FanOutOnlyInteractive`, `MidTurnJoin`, `PushErrorDoesNotAbortTurn`, `TransitionOrder` — re-run; expected to hold (env-ID monotonicity, fan-out gating, ordering all preserved), but the *count*/position of deltas may shift if a scenario scripted multiple same-id chunks. Adjust expected push lists where they enumerate deltas.

**New producer scenario** (`producer_test.go`): construct `&Producer{onEvent:…, log:…, flushSignal: ch, onFlush: f}` with an injected `ch := make(chan time.Time, 1)`; send one value → assert `onFlush` ran on the Run goroutine (and ordering vs a queued event is well-defined). A nil `flushSignal` (existing tests) never fires the arm — confirm `TestDrain_OnChannelClose` / `TestDrain_OnCtxCancel` still pass unchanged.

`make check` (vet + `-race` + staticcheck) green. No e2e required (the producer→emitter seam is unit-covered; #642 is the structured-receive capstone, blocked on the harness gap).

## Sizing

**Held at S; not downgraded to XS.** Production: 3 files, ~50–60 LOC (emitter buffer/flush/timer ~40, producer arm ~12, wiring ~3). §4 production-file count = **3** (`interactive_turn_v2.go`, `producer.go`, `interactive_turn_stream_v2.go`) — under the ≥5 gate. **Zero new exported types** (two additive `Config` fields, not types). **Zero production edit fan-out** — `turnbridge.New(turnbridge.Config{…})` has exactly one production call site (verified) plus four test sites, all of which compile unchanged against additive zero-value fields. The non-trivial cost that keeps this S rather than XS: the timer/single-goroutine reconciliation **and** the contained test-fixture review (the emitter's behavior change forces re-verification of the existing 10 scenarios — one file, ~3–5 genuine edits + re-runs). Total written work (production + tests + spec) projects well under the 600-LOC ceiling; no reject-branch fan-out (the flush is one helper; the non-text arms each gain a single flush call).

## Open questions

- **No flush on drain exit (deliberate).** When `drain` returns (session restart via channel-close, or ctx cancel), a partial buffer is **not** force-flushed. On teardown a flush would no-op (cancelled ctx). On a mid-turn session restart the turn is already broken and `inTurn` persists across the restart (inherited #632 behavior, unchanged here); the partial text flushes lazily on the next message boundary or the next timer arm after re-subscription. This is intentional scope-limiting — the ACs do not require flush-on-restart, and inventing one would defend an unobserved failure. Flag for the developer: do not add a drain-exit flush.
- **Window value.** `coalesceWindow = 250 ms` matches ADR 025's "~250 ms." It is a single named constant in the emitter file, trivially tunable; not exposed as config in this slice (no AC asks for it).
- **`flushTimer` lifetime.** The timer is created in `newInteractiveTurnEmitterV2` and never explicitly disposed; it holds no goroutine and is GC'd with the emitter. If a future reviewer prefers an explicit stop on relay teardown, that is a one-line addition in the wiring cleanup — out of scope here.
