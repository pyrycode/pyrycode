# Spec #1496 — Class-aware fan-in overflow: reserve slots so a turn-closing envelope is never crowded out

## Files to read first

Symbol names, not line numbers — resolve each with `codegraph_search` / `codegraph_node`.

- `cmd/pyry/stream_turn_drain.go` → `streamTurnSink`, `newStreamTurnSink`, `streamTurnSinkBuf`, `streamTurnEnvelope`, `sinkFor`, `exitFor`, `startStreamTurnDrainV2` — the entire change surface. Extract from the type doc: the channel is **never closed**, has a **documented sole reader**, and has **N concurrent producers**. Those three are the constraints that rule out every "just make it a real queue" design.
- `cmd/pyry/stream_turn_busy.go` → `turnBusyTracker.observe` (the opener/closer switch this spec extracts), `clearForSession`, `setBusy`, `Busy` — extract the **asymmetry**: an open with no close wedges forever; a close with no open is a no-op `delete`. That asymmetry is why reserving capacity for closers alone is sufficient.
- `internal/relay/v2session_modal.go` → `pushQueue.enqueue`, `pushQueueCap`, `queuedEnv` — the precedent this ticket names. Extract *which* trilemma leg it yields (strictly-bounded, via documented soft overflow) and *what bounds the excursion* (#911's per-conn in-flight gate). This spec yields the **other** leg; § Design says why.
- `internal/turnevent/event.go` → `Event`, `TextChunk`, `ThoughtChunk`, `ToolStart`, `ToolUpdate`, `TurnEnd`, `RateLimited`, `Stall`, `ApiRetry`, `Compacting`, `Unrecognized`, `BackgroundTaskStarted`, `BackgroundTaskUpdated`, `BackgroundTaskRoster`, `ThinkingProgress` — the full variant set the new classifier must be total over. Note the set has grown three times since `observe` was written (#1380, #1382, #1404); the classifier's whitelist discipline is what has absorbed that.
- `cmd/pyry/interactive_turn_v2.go` → `eventKind` — the content-free type discriminant for log fields. Reuse it; do not re-derive a second variant-name mapping.
- `cmd/pyry/stream_turn_drain_test.go` → `dropWatcher` (it already forwards **whole records** on its `recs` channel — built for exactly this kind of level+field-set assertion), `TestStreamTurnSink_ExitDropWhenFull` (mirror its assertion shape verbatim for the new Warn test), `feedLines`, `waitDropKind`, `assertNoPush`.
- `cmd/pyry/stream_turn_busy_test.go` → `stubBusyResolve` (the session→conversation stub every tracker test uses), and the `observe` tables — those must stay **green unmodified**, which is the regression pin on the classifier extraction.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory` — where `sinkFor` and `exitFor` are installed. Extract: **one producer goroutine per live runner**, which is what bounds the check-then-send slop in § Concurrency model.
- `internal/relay/v2session.go` → `V2SessionManager.ActiveConns` — the synchronous round-trip to the relay `Run` goroutine that `emitter.Handle` → `transitionTo` makes per emit. This is the evidence the drain really does stall, i.e. the saturated state is reachable rather than theoretical.
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md` § "Backpressure / replay" — the never-drop-control promise plus its 2026-07-10 amendment (which narrowed reconnect reconciliation and left never-drops unchanged).
- `docs/knowledge/features/streamsup-package.md` § "Non-blocking send, drop-newest" — the claim this change amends. **Do not hand-edit**; the documentation phase owns it.

## Context

`sinkFor` does a non-blocking send onto the 256-slot fan-in and, on a full channel, drops the **newest envelope of any class** — `turnevent.TurnEnd` included — at `Debug`. `pushQueue.enqueue`, the precedent its own comment cites, is deliberately class-aware and never drops a control event. The fan-in is therefore the one place in the stream-json path where a `turn_end` can vanish.

That is an ADR 025 violation, not a new policy question. ADR 025 § Backpressure already reads *"control events (`modal_shown`, `turn_end`, `tool_*`) never drop."*

**Why the loss is unrecoverable rather than late.** `turnBusyTracker.observe` opens the per-conversation mark on any of `ThoughtChunk` / `TextChunk` / `ToolStart` / `ToolUpdate` and closes it **only** on `TurnEnd`. There is no `TurnStart`. So a lost `TurnEnd` leaves the mark set with nothing left to clear it: `waitIdleForDelivery` parks every subsequent `send_message` until `streamTurnHoldTimeout`, `msgqueue` eventually gives up with `session_error`, and the emitter's `inTurn` stays true so the next turn reuses the stale `turn_id`. Recovery needs `/clear`, child death, or idle eviction. It also falsifies the "no reachable sequence leaves a conversation reported busy forever" claim `turnBusyTracker`'s own doc records as SATISFIED, whose first closing feed is exactly this `TurnEnd`.

**Why the drain stalls long enough to matter.** `emitter.Handle` → `transitionTo` → `ActiveConns` is a synchronous round-trip onto the relay `Run` goroutine, so the drain blocks per emit. A tool-heavy turn bursting past 256 events while that round-trip is in flight is the filed repro.

## Design

### The class split is defined by loss consequence, not by wire type

Two classes at the fan-in, and only two:

| Class | Members | What losing one costs |
|---|---|---|
| **closing** | `turnevent.TurnEnd`; the `exit` envelope (`streamTurnEnvelope.exit`) | the conversation is wedged busy forever |
| **droppable** | every other `turnevent.Event` variant | one event of transcript fidelity, self-healing on the next event |

This is a **narrower** never-drop class than ADR 025's wire-level one, and the divergence is deliberate. ADR 025 classifies `protocol.Envelope.Type` on the outbound push queue, where `tool_*` is control. Applying that literally here would put `ToolStart` / `ToolUpdate` in the never-drop class — and the filed repro *is* a tool-heavy burst, so the droppable class would be empty exactly when the policy is needed. The fan-in carries `turnevent.Event`, one layer upstream, and the property that matters at this layer is the turn-busy lifecycle. `tool_*` keeps its ADR 025 never-drop status downstream in `pushQueue`, unchanged; this reserve is additive protection at a second queue, not a weakening of the first.

The classification is sound because the tracker's mark is **asymmetric**: `setBusy(open=true)` is idempotent, so losing *some* openers changes nothing; losing *all* of them means the turn never opens and the arriving `TurnEnd` is a no-op `delete`. Only open-without-close wedges. Reserving for closers alone is therefore exactly sufficient.

### Mechanism: reserved tail slots on the existing channel

Keep the channel. Add a droppable-class high-water mark strictly below capacity, so the remaining slots can only ever be taken by a closing-class envelope.

- New unexported const `streamTurnSinkCloseReserve` on `stream_turn_drain.go`.
- `streamTurnSink` gains one unexported field, `droppableCap int`, computed once in `newStreamTurnSink`. **`newStreamTurnSink`'s signature does not change** — no call-site cascade (10 production + test construction sites stay untouched).
- `sinkFor` refuses a droppable-class event once `len(s.ch) >= s.droppableCap`, logging today's `Debug` drop with today's field set. A closing-class event skips that gate and takes the existing non-blocking send against the **full** capacity.
- `exitFor` needs **no code change**: the exit envelope is closing-class and already sends unwatermarked with a `Warn` drop. Its doc comment does need one correction — it currently says "Same NON-BLOCKING send as `sinkFor`," which stops being true for the droppable class.

Reserve arithmetic:

```
reserve     = min(streamTurnSinkCloseReserve, buf/2)   // buf/2 keeps small test bufs workable
droppableCap = buf - reserve                            // >= ceil(buf/2) >= 1 for buf >= 1
```

At the production `buf` of 256 that is a reserve of 32 and 224 droppable slots — the "256 slots absorb a burst" rationale in `streamTurnSinkBuf`'s doc survives intact. At `buf == 1` the reserve degenerates to 0, which is what keeps `TestStreamTurnSink_ExitDropWhenFull` and its `streamsup_runner_exit_test.go` sibling green unmodified and gives the new `Warn` test a one-slot fixture.

Sizing 32: the reserve must cover (a) unprocessed closing envelopes — at most two per live runner, its `TurnEnd` and its `exit` — plus (b) the check-then-send slop in § Concurrency model, at most one droppable per producer goroutine. Both scale with the number of live stream runners, so 32 covers ~10 concurrently-live runners under maximally adversarial interleaving, against a realistic pool of 1–5. Past that, loss is `Warn`-visible rather than silent.

### The trilemma leg this yields, and why it differs from `pushQueue`

ADR 025's trilemma — **bounded ∧ never-drop-control ∧ never-block-producer** — is unsatisfiable here as it is for `pushQueue`, and #911 established the saturated state is reachable rather than theoretical. The two queues resolve it in opposite directions, on purpose:

- `pushQueue` yields **strictly bounded**: it soft-overflows control past nominal cap, keeping never-drop-control absolute. It can afford that because #911's per-conn in-flight gate bounds the excursion to one bundle's chunks.
- The fan-in yields **never-drop-control**, past the documented reserve, keeping strictly-bounded absolute. Its producer is claude's stdout — an unrate-limited source with no analogue of #911's gate — so soft overflow there would be an unbounded-growth vector driven by a runaway or hostile child rather than a bounded excursion. Trading a bounded, `Warn`-visible residual loss for a hard memory bound is the right side of that trade at this layer.

Loss is therefore *not* structurally impossible, so AC5's second leg applies: **every remaining closing-class loss path logs at `Warn`**, event `stream_turn.close_sink_full`, content-free.

**On AC5's "`exitFor`'s content-free field set."** `exitFor` logs exactly `event` + `session_id` and omits `kind` — its own comment gives the reason: *"no `kind` — there is no event to name."* Here there **is** an event to name, so the new record carries `event` + `kind` (`eventKind(ev)`) + `session_id`. That is `exitFor`'s content-free *discipline* — discriminant and session id only, never assistant / thought / tool content — applied to a record that has a discriminant available. The developer's comment on the branch should say this in one sentence so code-review reading AC5 literally has the argument in hand.

### Rejected alternatives

- **Mirror `pushQueue.enqueue` literally.** It is a `[]queuedEnv` under `m.pushMu`, so it can inspect and `slices.Delete` an interior element. A channel producer cannot inspect a queued element without receiving it — which breaks the sole-reader invariant *and* only reveals the element's class after it is already off the channel.
- **A separate control lane, or issuing `clearForSession` from the producer.** Both break **ordering**, which is the actual obstacle. Every one of the ~256 buffered events is an opener under `observe`'s whitelist, so a clear that arrives ahead of them is re-opened immediately behind and the conversation is busy anyway. This is the same argument `streamTurnEnvelope.exit`'s doc already makes for why the exit signal rides the shared FIFO.
- **A spill slice consumed by the drain.** Order-preserving spill needs producers to keep spilling once spilling and the drain to always prefer the spill — a mutex + slice + signal, i.e. the queue-primitive replacement the ticket's size guard routes back to PO. It also inverts the wedge: a spilled closer overtaken by a later opener reports a **live** turn idle.
- **A bounded blocking send for closing class.** Satisfies AC3's letter but stalls claude's stdout forwarder for the timeout, which is the failure `sinkFor` exists to prevent.

### One classifier, two consumers — the drift guard

`sinkFor`'s never-drop set and `observe`'s closer set must agree, or a future variant added to one and not the other silently reintroduces this exact bug. That agreement is made **structural** rather than advisory: extract `observe`'s existing `switch ev.(type)` into one pure classifier that both read.

```go
// turnMark is one fan-in event's effect on turnBusyTracker's per-conversation
// mark. Sole definition of the split; observe and sinkFor both read it.
type turnMark uint8

const (
    turnMarkNone turnMark = iota // no turn-lifecycle meaning
    turnMarkOpen                 // opens the mark
    turnMarkClose                // closes the mark; never dropped at the fan-in
)

func turnMarkFor(ev turnevent.Event) turnMark
```

Home: `cmd/pyry/stream_turn_busy.go`, next to `observe` (same package as `sinkFor`, so no import). `observe` then switches on `turnMarkFor(ev)`; `sinkFor` gates on `turnMarkFor(ev) != turnMarkClose`.

**Move the existing rationale comments onto the classifier, do not delete them** — the opener-set-is-a-whitelist argument, the `Unrecognized`-lands-in-`default` reasoning, and the `DISCHARGED 2026-08-09 (#1404)` note are the evidence for the classification and are now load-bearing for two callers instead of one. `observe`'s own comment keeps only what is about `observe` (nil receiver, resolve-outside-the-lock, the unresolved-session skip).

Whitelist direction stays as `observe` has it: openers and closers are enumerated, everything unknown falls to `turnMarkNone`. For `sinkFor` that means an unknown variant is droppable, which is the safe default — a future variant wrongly reserved costs capacity; the wedge only comes from a *closer* misclassified as droppable, and closers are the enumerated arm.

## Concurrency model

No new goroutines. No new locks. `sinkFor` and `exitFor` continue to hold no lock (channel send only), and the drain remains the sole reader.

**`len(s.ch)` is a racy read, and the race is bounded.** Producer P may read `len < droppableCap`, be preempted, and send after other producers have filled the channel. The consequence is bounded because:

- each producer is a **single goroutine** (`streamsup.Parser` runs on `os/exec`'s stdout forwarder; one per live runner), so a producer has at most one check-then-send in flight at a time;
- the send is still non-blocking, so a slipped droppable either takes one reserve slot or hits `default` and is dropped.

Worst case is therefore **one droppable per live runner** admitted into the reserve — the term (b) the reserve is sized for. A stale read never blocks, never panics, and never admits unboundedly.

**Ordering (AC2) is inherited, not added.** The closer travels the same FIFO channel with the same sole reader, so an envelope queued ahead of it is necessarily received ahead of it. There is no second lane and no reordering step, which is precisely why the reserve mechanism was chosen over spill or a control lane.

**AC3 is preserved structurally.** Every path out of both closures is a non-blocking `select`/`default` or an early return. Neither closure gains a lock, a channel receive, or a callback out, so a stopped drain cannot wedge claude's stdout being read.

## Error handling

Three terminal branches in `sinkFor`, all content-free, none returning an error (the closure's type is `func(turnevent.Event)`):

| Condition | Level | `event` | Fields |
|---|---|---|---|
| droppable at/over `droppableCap` | `Debug` | `stream_turn.sink_full` | `event`, `kind`, `session_id` — **unchanged** |
| closing class, channel genuinely full | `Warn` | `stream_turn.close_sink_full` | `event`, `kind`, `session_id` |
| exit envelope, channel genuinely full | `Warn` | `stream_turn.exit_sink_full` | `event`, `session_id` — **unchanged** |

`Warn` and not `Debug` because the daemon's default level is `LevelInfo` (see the level selection in `runSupervisor`), so a `Debug`-only record is invisible in production — the same argument `exitFor` already makes for its own branch.

No new sentinel errors, no new wire codes, no change to what the drain or the tracker do with an envelope once it is on the channel.

## Testing strategy

Table-driven where the shape allows, stdlib `testing`, `-race`. Scenarios, not code — the developer writes them in this package's idiom, reusing `dropWatcher`, `stubBusyResolve`, `feedLines`, `waitDropKind`.

**AC1 — a turn-closer survives a saturated fan-in** (`stream_turn_drain_test.go`). Construct the sink with a small `buf`. Push `buf` opener events through `sinkFor` for `sess-a` (past the watermark, so the tail is `Debug`-dropped), then one `turnevent.TurnEnd` through `sinkFor`. Start the drain with a tracker resolving `sess-a` → a conversation id. Assert the conversation reports not-busy once the queue is drained (`WaitIdle` under a bounded context, not a sleep). **This must be red on the unmodified tree**: today all `buf` openers are admitted, the channel is full, and the `TurnEnd` is dropped — `Busy` stays true. Verify the red before writing the fix.

**AC2 — the close stays ordered behind what was already queued** (`stream_turn_drain_test.go`). Same fixture, but wrap the tracker's `resolve` so each call records `tracker.Busy(convID)` at that instant. `observe` calls `resolve` before `setBusy`, and the drain is serial, so the recorded sequence is the processing order. Assert: exactly one `false` reading and it is the **first**; every later reading is `true`, including the `TurnEnd`'s own. That is "never idle while an earlier envelope is unprocessed", stated as a deterministic sequence rather than a timing window. (`resolve` is called outside `t.mu`, so reading `Busy` from inside it does not deadlock.)

**AC3 — the producer is never wedged.** Never start the drain. Drive well past capacity through `sinkFor` (mixed classes, several `TurnEnd`s) and `exitFor`, from the test goroutine, guarded by a `select` on a done channel with a bounded timeout so a blocked send fails rather than hangs the suite. Assert every call returned.

**AC4 — droppable-class loss unchanged.** At the watermark, assert the dropped droppable event logs at `Debug`, `event == "stream_turn.sink_full"`, with **exactly** `event` + `kind` + `session_id` and nothing else. Feed an event whose payload carries a recognisable string and assert it appears in no attribute — the content-free pin.

**AC5 — a lost turn-closer is never silent.** `buf == 1` fixture, mirroring `TestStreamTurnSink_ExitDropWhenFull` shape: occupy the single slot, then push a `TurnEnd` through `sinkFor`. Capture the whole record via `dropWatcher{recs: …}` and assert level `Warn`, `event == "stream_turn.close_sink_full"`, `kind == "turn_end"`, the right `session_id`, and exactly three attributes.

**Classifier totality** (`stream_turn_busy_test.go`). One table over **every** `turnevent.Event` variant → expected `turnMark`, so a variant added later has an obviously-missing row. Include `Unrecognized` and at least one recently-added variant (`RateLimited`, `BackgroundTaskStarted`) pinned to `turnMarkNone`, which is the property the #1404 DISCHARGED note records.

**Extraction regression.** `stream_turn_busy_test.go`'s existing `observe` tables and `stream_turn_drain_test.go`'s existing drain tests must stay **green unmodified**. If either needs an edit, the extraction changed behaviour and is wrong.

**Reserve arithmetic.** Small table over `buf` → expected `droppableCap` covering the production 256, the degenerate 1, and one small even value, so the `min` and the `buf/2` clamp are both pinned.

## Open questions

1. **Reserve of 32 is argued, not measured.** The arithmetic in § Design bounds it by live-runner count, and `ActiveCap` defaults to `0` (uncapped), so there is no config value to derive it from. If a future ticket makes the runner count large, the reserve is the knob — and the `Warn` record is the signal that it needs turning. No config surface for it now; a constant with a documented derivation is the honest shape.
2. **A single session spamming `result` lines can crowd the shared reserve** and starve another session's `TurnEnd`. It is a *narrowed* version of today's behaviour, not a new vector (see § Security review), and a per-session reserve would need the per-session accounting this design exists to avoid. Left as-is, deliberately.
3. **`streamTurnSinkBuf`'s doc comment claims "drops the newest event rather than block"** without qualification. It needs one clause added for the closing class. That is an in-file comment on a line this ticket touches, so it is in scope; the evergreen docs listed on the ticket are **not** — the documentation phase owns those.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — the boundary is unmoved and the change is upstream of it.** claude's stdout crosses untrusted→trusted inside `streamsup.Parser`, which already caps the partial line at 4 MiB (#1088) and truncates every claude-derived field at construction (`maxTaskDescription`, `maxTaskPatch`, `maxUnrecognizedRaw`). This spec adds no decode, no parse, and no new field read; `turnMarkFor` switches on the Go **variant type** only, never on a field value, so no hostile stdout content can steer the classification. The `sessionID` tag is the runner's construction-time id from `newStreamRunnerFactory`, never wire- or child-supplied — the property `turnBusyTracker`'s SECURITY note depends on, preserved unchanged.
- **[Network & I/O — resource exhaustion] No findings, and this is the leg deliberately protected.** The fan-in stays **strictly bounded** at `cap(s.ch)`: the reserve partitions existing capacity, it does not add any. A hostile or runaway child cannot grow the channel by one slot. This is the explicit reason § Design yields never-drop-control instead of copying `pushQueue`'s soft overflow — the fan-in has no analogue of #911's per-conn in-flight gate to bound an excursion, so unbounded growth here would be a memory-exhaustion vector reachable from child stdout.
- **[Network & I/O — cross-session starvation] SHOULD FIX (documented, no code) — a session spamming `result` lines can consume the shared reserve.** Closing-class envelopes bypass the watermark, so one session bursting 32+ `TurnEnd`s while the drain is stalled could crowd out another session's `TurnEnd` and wedge *that* conversation. Assessed as **narrowed, not new**: today the same burst crowds out everything including its own closer, so post-change reachability is strictly smaller; memory stays bounded; amplification is bounded by `streamsup.Parser` mapping only `result` lines to `TurnEnd` under the 4 MiB line cap; and every resulting loss is `Warn`-visible where today it is silent at `Debug`. Recorded as Open question 2 rather than defended with a per-session reserve, which would require exactly the per-session accounting this design avoids.
- **[Error messages, logs, telemetry] No findings — the new record is content-free by construction and pinned by test.** `stream_turn.close_sink_full` carries `event` + `eventKind(ev)` + `session_id`. `eventKind` returns the **variant name only** and never `Unrecognized.Kind` (claude's offending type string) — the package rule that nothing derived from claude's output reaches a log. AC4's and AC5's tests assert the exact attribute count, so a later field addition fails rather than leaks. The resolved conversation id is absent for the reason `exitFor` documents: the sink closure holds no resolver, and conversation ids are treated as sensitive routing keys alongside session ids and `workspace_cwd`. **Raising the level to `Warn` raises visibility, not sensitivity** — the same fields, at a level the default `LevelInfo` daemon actually emits.
- **[Concurrency] No findings — no new lock, and the one race is bounded and analysed.** No goroutine is spawned, so no leak is possible; the drain's lifecycle and the channel's never-closed invariant are untouched. The `len(s.ch)` read is racy by design: § Concurrency model bounds the consequence at one droppable per producer goroutine (each producer is single-goroutine, each send non-blocking), which is a sizing term in the reserve rather than an unhandled TOCTOU. No lock is taken in either closure, so no lock-ordering question arises; `turnBusyTracker`'s existing `t.mu` discipline and its resolve-outside-the-lock rule are unchanged. Shutdown safety is unchanged: a post-shutdown send still lands in the non-blocking drop path.
- **[Threat model alignment] No findings — this closes a gap against ADR 025 rather than opening one.** ADR 025 § Backpressure's never-drop-control promise (unchanged by the 2026-07-10 amendment) is currently violated at the fan-in for `turn_end`; this restores it up to a documented, `Warn`-visible headroom. The availability threat it addresses — a conversation permanently undeliverable, every `send_message` rejected with `session_error`, recoverable only by `/clear`, child death, or eviction — is exactly the denial-of-service shape `pushQueue`'s policy exists to prevent. `docs/protocol-mobile.md` § Security model's permission-answering threats are untouched: no wire shape, frame type, or gate changes.
- **[Tokens / File operations / Subprocess / Cryptographic primitives] Not applicable — the change has no such surface.** It touches one in-process channel send and one type switch. No credential is created, stored, compared, or logged; no filesystem path is constructed, opened, or written; no `exec.Command` argument, environment, or signal is affected; no randomness, key, nonce, or comparison is involved.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
