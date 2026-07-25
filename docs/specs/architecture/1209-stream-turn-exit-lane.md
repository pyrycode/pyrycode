# #1209 — stream-json: an exit lane on the turn-busy fan-in

**Size:** S (confirming PO). One production file carries the behaviour
(`cmd/pyry/stream_turn_drain.go`); a second carries a bounded doc-comment
amendment (`cmd/pyry/stream_turn_busy.go`). 0 new exported types, 0 new files,
0 call-site cascade (the one envelope construction site is a keyed literal),
5 acceptance criteria, 1 new reject branch. All citations verified against
`870be1c` — `main` at the time of writing.

---

## Files to read first

| Path | What to extract |
|---|---|
| `cmd/pyry/stream_turn_drain.go:18-26` | `streamTurnEnvelope` — the struct that gains the discriminant, and the doc explaining what the `sessionID` tag means |
| `cmd/pyry/stream_turn_drain.go:41-60` | `streamTurnSink` + `newStreamTurnSink` — the never-closed channel, the `buf <= 0` default, the nil-logger fallback |
| `cmd/pyry/stream_turn_drain.go:62-82` | `sinkFor` — the exact shape the exit closure mirrors: closure over `sessionID`, `select`/`default` non-blocking send, content-free `Debug` drop |
| `cmd/pyry/stream_turn_drain.go:130-153` | the drain's `select`; the `sink.ch` arm at `:134-149` is where the new arm goes, ahead of `busy.observe` at `:139` |
| `cmd/pyry/stream_turn_busy.go:154-211` | `clearForSession` — the method the exit arm calls. Read the nil-receiver clause (`:160-167`), the synchronous-execution clause (`:169-177`), the session-id-not-conversation-id clause (`:179-184`), and the fail-closed `clear_unresolved` skip (`:194-208`). **The doc amendment below lands here.** |
| `cmd/pyry/stream_turn_busy.go:200-206` | `clear_unresolved` — the existing event-less, content-free log AC1 says the new drop log is shaped after |
| `cmd/pyry/stream_turn_busy.go:213-239` | `setBusy` — the close-and-replace protocol that makes `WaitIdle` wake; this is what the positive test's barrier rides |
| `cmd/pyry/stream_turn_busy.go:262-280` | `WaitIdle` — note it returns nil **immediately** when already idle. That is the vacuity hazard in the AC2 test (see Testing strategy) |
| `cmd/pyry/stream_turn_busy.go:27-35` | the `KNOWN GAP` block — **do not edit** (out of scope, still true after this slice) |
| `cmd/pyry/session_transition_v2.go:266-282` | the existing (#1202) `clearForSession` caller; the exit arm becomes the second one, which is what the doc amendment records |
| `cmd/pyry/stream_turn_drain_test.go:67-96` | `dropWatcher` — the shared handler to widen; note it filters on `event == "stream_turn.not_active"` and forwards only `kind` |
| `cmd/pyry/stream_turn_drain_test.go:112-117` | `feedLines` — pushes through a real `streamsup.Parser`, so it can never produce an exit |
| `cmd/pyry/stream_turn_drain_test.go:121-134`, `:153-175` | `collectEnvs`, `waitDropKind`, `assertNoPush` — the three barriers every drain test already uses |
| `cmd/pyry/stream_turn_busy_test.go:452-488` | `TestStreamTurnDrainV2_BusyFedBeforeActiveGate` — the closest template: non-active session, observe-then-gate, `waitDropKind` barrier, `stubBusyResolve` |
| `cmd/pyry/stream_turn_busy_test.go:417-439` | `ImportsStayMinimal` — pins `stream_turn_busy.go`'s import set to exactly four; the doc amendment must not add one |
| `cmd/pyry/interactive_turn_v2.go:410-421` | `eventKind` — its `default: return "unknown"` is why nil-as-sentinel fails silently rather than loudly |
| `internal/streamsup/runner.go:105-143` | `Config.OnChildExit` — the `func()` signature the exit closure must return, and the "NOT a drain barrier" clause that motivates this whole design |
| `internal/streamsup/runner.go:480-490` | the fire site (unwired). Context only — **nothing here changes** |
| `internal/streamsup/parser.go:231-235` | `Parser.emit` calls `p.sink` synchronously, no goroutine, no buffer — the happens-before premise the sibling slice relies on |
| `cmd/pyry/streamsup_runner.go:105` | `sink.sinkFor(cfg.SessionID)` — where `sink.exitFor(cfg.SessionID)` will eventually be assigned (#1210, **not here**) |
| `cmd/pyry/relay.go:730-748` | the tracker + drain construction; shows `busy` is a local and the drain is started exactly once |
| `cmd/pyry/interactive_runner_test.go:55-75` | a test that reads `sink.ch` directly and asserts on `env.sessionID` — proof the keyed literal keeps it compiling unchanged |

---

## Context

`turnBusyTracker` (`cmd/pyry/stream_turn_busy.go`) has two clear feeds today:
a turn's `TurnEnd` arriving on the fan-in (`observe`, #1201) and a pool teardown
transition reaching `clearForSession` (#1202). A child that **crashes mid-turn**
is invisible to both: the resumed child emits no `result` line for the abandoned
turn, so no `TurnEnd` is ever parsed (`internal/streamsup/parser.go:152-157`),
and the pool entry is untouched, so no `SessionTransition` fires. That is the
`KNOWN GAP` at `stream_turn_busy.go:27-35`.

Closing it needs a producer (the runner's child exit) *and* a lane that orders
the resulting clear correctly. This ticket is the lane only.

**Why a lane on the fan-in and not a direct call from the exit seam.** `#1206`'s
`Config.OnChildExit` is explicitly *not* a drain barrier
(`internal/streamsup/runner.go:136-142`). By the time it fires, every event the
dead child produced has been **pushed** onto `streamTurnSink.ch` — `cmd.Stdout`
is the Parser, os/exec runs a copier goroutine, `cmd.Wait` joins it, and
`Parser.emit` calls its sink synchronously (`parser.go:231-235`) — but not
necessarily **drained**: `startStreamTurnDrainV2` is a separate goroutine reading
a 256-slot buffer. A clear delivered on any lane *other than that channel* can
land before the drain processes openers the crashed child already emitted; the
drain then re-marks the conversation busy with the clear already spent and no
further exit coming. That is a conversation reported busy forever — precisely the
wedge the gap is about. The fan-in is FIFO with a single reader, so a clear that
rides it cannot be overtaken.

**Ships unfired.** Nothing in production sets `OnChildExit` after this slice
(verified: `grep -rn 'OnChildExit' cmd/pyry/ --include='*.go'` returns **0** on
`main` today, tests included). The gap stays open, no delivery path consults the
tracker, and no frame on the v2 wire changes. Precedent for a lane whose only
caller is a test: `startStreamTurnDrainV2` itself shipped that way in #1098
(`stream_turn_drain.go:109-112`).

---

## Design

Three production changes, all in `cmd/pyry/stream_turn_drain.go`, plus one
comment-only amendment in `cmd/pyry/stream_turn_busy.go`.

### 1. An explicit discriminant on `streamTurnEnvelope`

```go
type streamTurnEnvelope struct {
    sessionID string
    ev        turnevent.Event
    exit      bool // child-exit signal for sessionID; ev is unset and never read
}
```

`streamTurnEnvelope` has exactly **one** construction site
(`stream_turn_drain.go:72`) and it is a keyed composite literal, so the field is
additive with zero call-site cascade — verified by grep across all `*.go`
(5 hits, all in `stream_turn_drain.go`).

**A boolean, never `ev == nil` as the sentinel.** `ev` is a `turnevent.Event`
interface. A nil sentinel would have to be re-checked at every consumer, and it
would make `eventKind(nil)` reachable — that returns `"unknown"` rather than
failing (`interactive_turn_v2.go:419-421`), so a missed check would be *silent*.
More to the point: with a nil sentinel the exit signal IS a value of the type
`interactiveTurnEmitterV2.Handle` accepts, so "Handle cannot receive a
non-event" stops being a type-level fact. With a separate field it stays one —
that is AC4's second half, and it is satisfied by the field, not by a runtime
branch. The review check is therefore *"no consumer anywhere branches on
`env.ev == nil`"*, which is grep-able.

### 2. An exit-closure constructor on `streamTurnSink`

Contract, mirroring `sinkFor` (`:62-82`):

```go
// exitFor returns the per-runner child-exit closure for the runner constructed
// with sessionID. Same non-blocking send as sinkFor: drops the newest on a full
// channel rather than block the caller.
func (s *streamTurnSink) exitFor(sessionID string) func()
```

Behaviour, in one line each:

- Sends `streamTurnEnvelope{sessionID: sessionID, exit: true}` in a
  `select`/`default`; `ev` is left zero and is never read on this path.
- On the `default` branch, logs at **`Warn`** (not `Debug`):
  `"relay: stream-turn exit drop; sink full"` with exactly
  `"event", "stream_turn.exit_sink_full"` and `"session_id", sessionID`.
  **No `kind`** — there is no event to name — and no other field. This is the
  shape of `clear_unresolved` (`stream_turn_busy.go:204-206`), the existing
  event-less content-free log.

Two details that are decisions, not accidents:

- **The return type is `func()`, not `func(sessionID string)`.** It is exactly
  `Config.OnChildExit`'s type (`internal/streamsup/runner.go:143`), so the
  wiring slice's assignment is `scfg.OnChildExit = sink.exitFor(cfg.SessionID)`
  one line from `scfg.Stdout = streamsup.NewParser(sink.sinkFor(cfg.SessionID),
  …)` (`streamsup_runner.go:105`). Binding both keys at the same construction
  point is what keeps the two lanes' session tags identical by construction.
- **Do not factor the `select` out of `sinkFor` into a shared helper.** The
  shared part is one `select` statement; the divergent part is the entire
  diagnostic (level, message, field set). Parameterising the divergence costs
  more than it saves and obscures the deliberate `Warn`/`Debug` asymmetry. Left
  as two small closures — note this in a comment so review does not read it as
  copy-paste.

**Why `Warn` where the siblings are `Debug`.** A dropped exit is a permanently
wedged conversation once #1210 wires the producer and #1199 consults the signal;
a dropped event is a lost delta. The siblings' `Debug` is invisible at the
daemon's default `LevelInfo` (`main.go:734-737` — `LevelDebug` only under
`-pyry-verbose`), which is the correct treatment for a lost delta and the wrong
one for degraded operation. The level is the whole diagnostic value of this
branch, so it is load-bearing, not decoration.

### 3. An exit arm in the drain's `sink.ch` case

Inside `case env := <-sink.ch:` (`stream_turn_drain.go:134`), **as the first
statement of the arm**:

```go
if env.exit {
    busy.clearForSession(env.sessionID)
    continue
}
```

Placement is the contract, and each of the three things it precedes matters:

- **Before `busy.observe`** (`:139`) — an exit carries no event; `observe` would
  reach its `switch ev.(type)` `default:` and return, but routing a non-event
  through the event path is exactly the confusion the explicit field exists to
  prevent.
- **Before the active-session gate** (`:141-149`) — for the identical reason the
  tracker is fed before it (`:135-138`): the gate drops every event whose
  producing session is not the *active* conversation's. An exit filtered there
  would never clear a background conversation, which is the common case for a
  crash (the crashed runner need not be the one the user is looking at). The
  positive test below is built so that this placement is load-bearing.
- **Before `emitter.Handle`** (`:150`) — AC4's second half. Combined with the
  explicit field, `Handle` cannot receive a non-event by construction.

`clearForSession` is called **as-is**, with no second session→conversation
resolution and no second copy of the membership-mutation protocol
(`stream_turn_busy.go:216-219`). It is a nil-receiver no-op, so the drain's
existing tests that pass `busy = nil` keep working unchanged, and it already
handles the unresolvable-session case fail-closed with its own `clear_unresolved`
log (`:194-208`) — which is why AC1 constrains only the *drop* diagnostic: the
sink closure holds no resolver and is structurally unable to name a conversation.

### 4. Doc amendment on `clearForSession` (comment only)

`clearForSession`'s doc currently names one caller. After this slice there are
two, and a comment that says "the teardown feed" where two feeds exist is a false
comment. Two bounded edits in `cmd/pyry/stream_turn_busy.go`, **no code change in
that file**:

- `:154-158` — record that there are now two callers: the pool's
  `TransitionObserver` (#1202) and the drain's exit arm (#1209, unfired until
  #1210 supplies a producer). Keep the existing sentence about *why* these turns
  need a second feed.
- `:169-177` — the "runs SYNCHRONOUSLY on the goroutine that fired the
  transition" paragraph: add that the drain caller likewise runs it inline, where
  the must-not-block requirement is the drain's own (a blocked clear stalls the
  whole fan-in, including the flush timer), and that on the drain goroutine this
  feed is serialised against `observe` by construction rather than by `t.mu`.

**Do not touch** the `KNOWN GAP` block at `:27-35`, `relay.go:743-744`, or
`internal/streamsup/runner.go:113`. All three claims remain **true** after this
slice — the lane exists but nothing fires it — and #1210 owns their rewrite in
the same diff as the behaviour change. Their ticket *numbers* are stale (#1203
and #1207 are closed split parents), but that staleness pre-dates this slice.

`ImportsStayMinimal` (`stream_turn_busy_test.go:417-439`) pins that file's import
set to exactly four; a comment-only edit keeps it green.

### Data flow

```
runner Run goroutine            stdout copier goroutine
(#1210 — not wired here)        (per session)
        │                               │
   exitFor(S)()                   sinkFor(S)(ev)
        │                               │
        └──────────► streamTurnSink.ch ◄┘      (256 slots, never closed,
                            │                   drop-newest on full)
                            ▼
                  startStreamTurnDrainV2  ── single reader, FIFO
                            │
              ┌─────────────┴──────────────┐
        env.exit == true            env.exit == false
              │                             │
   busy.clearForSession(S)          busy.observe(S, ev)
              │                             │
          continue                    active-session gate
                                            │
                                     emitter.Handle
```

---

## Concurrency model

**No new goroutine.** AC3 forbids one: a clear handed to a goroutine would
satisfy AC2 and fail AC3, and it would reintroduce exactly the overtaking hazard
the fan-in design exists to remove.

**Producers.** The exit closure is called from the runner's `Run` goroutine; the
event closures from os/exec's stdout-copier goroutines — different goroutines,
one shared buffered channel. Go gives FIFO delivery for sends that are
happens-before-ordered, which is all this slice claims. *Establishing* that
ordering for the exit relative to the dead child's events — `cmd.Wait` joins the
copier, `Parser.emit` is synchronous (`parser.go:231-235`), so every event push
happens-before the `OnChildExit` call — is the wiring slice's argument, together
with the "after the dead child's events and before any of the respawned child's"
half that follows from the fire site sitting above the backoff wait
(`runner.go:480-490`).

**Consumer.** The drain goroutine remains the single reader and, with this
change, the single caller of `clearForSession` *from the stream path*. The
teardown feed (`session_transition_v2.go:280`) still calls it from the pool's
lifecycle goroutine. Both reach `setBusy`, which serialises under `t.mu`
(`stream_turn_busy.go:220-239`). Two concurrent clears for the same conversation
are idempotent and same-direction; there is no interleaving that produces a
spurious *open*.

**Lock order is unchanged.** `clearForSession` resolves the session **outside**
`t.mu` (`:192-193`), so the registry mutex and the tracker mutex are never
nested — the identical discipline `observe` documents at `:128-135`. The exit arm
adds no lock of its own.

**Blocking discipline.** The clear runs inline on the drain goroutine, which is
the same goroutine that drives `emitter.flushDelta` on the coalescing timer. Its
work is one registry-mutex slice-header read, one map delete and one `close` —
no channel receive, no I/O, no callback out. The pool's lifecycle goroutine
already pays a full atomic write including fsync one line before its own call to
this method, so the inline cost is not a new class of work for either caller.

---

## Error handling

| Failure | Behaviour | Where |
|---|---|---|
| Sink channel full when the exit is pushed | Drop the newest (the exit), log at `Warn` with `event` + `session_id` only | new `exitFor` `default` branch |
| Session resolves to no conversation | `clearForSession` skips fail-closed and logs `clear_unresolved` at `Debug` | existing, `stream_turn_busy.go:194-208` |
| `busy == nil` (PTY mode, or the drain's pre-#1201 tests) | Nil-receiver no-op | existing, `stream_turn_busy.go:188-190` |
| Conversation already idle when the exit arrives | Idempotent no-op; no mutation, no broadcast | existing, `setBusy` `:224-227` |
| Exit for a session that never opened a turn | Same as above | — |

**The failure direction is asymmetric and stays that way.** A dropped exit is a
missed clear — a wedge. It is never a spurious clear. That asymmetry is the one
that matters downstream: under the consuming slice (#1199) a spurious clear would
release a mid-turn send into a running conversation, whereas a wedge only
withholds delivery. The exit rides the sink's existing drop-newest path, which is
*already* the tracker's exposure — a `TurnEnd` dropped there wedges the
conversation today — so this adds no new failure **mode**, only a new occupant of
an existing one.

---

## Testing strategy

Tier: `cmd/pyry` unit, alongside the existing drain tests. Scenarios as bullets;
the developer writes them in the file's established idiom (`t.Parallel()`,
`stubActiveSession`, `stubBusyResolve`, `chanBcast`, cancel-then-join cleanup).

### Shared scaffolding: widen `dropWatcher` (`stream_turn_drain_test.go:71-96`)

- Add one optional field, `recs chan slog.Record`, forwarded in a
  `select`/`default` alongside the existing `kinds` forward. Send `r.Clone()` —
  a retained `slog.Record` is not safe to hold otherwise.
- Leave the `event == "stream_turn.not_active"` → `kinds` branch untouched.
- A **nil** channel inside a `select` with a `default` takes the default, so every
  existing `dropWatcher{kinds: drops}` construction keeps compiling and behaving
  identically. No cascade.
- No `events chan string` field is needed: no scenario below barriers on the exit
  drop itself, so the event-name filtering happens test-side on the received
  records.

> **Hazard — the exit drop is emitted on the *sink's* logger, not the drain's.**
> Every existing drain test hands the watcher to `startStreamTurnDrainV2` and
> gives `newStreamTurnSink` a `discardLogger()` (`stream_turn_busy_test.go:469-472`).
> Copying that scaffolding verbatim installs the watcher on the one logger the
> exit drop can never reach, and the assertion then fails in a way that looks
> like a missing feature. Scenario D below constructs the sink as
> `newStreamTurnSink(1, slog.New(dropWatcher{recs: …}))`.

> **Hazard — an exit envelope is barrier-less by construction.** Its arm
> `continue`s before `observe`, before the gate, *and* before `Handle`, so it
> produces neither a push (`collectEnvs`) nor a not-active drop (`waitDropKind`).
> "Has the drain processed my exit yet?" has no existing answer and the naive fix
> is a sleep, which is flaky under `-race`. Every scenario below names its
> barrier explicitly.

### Scenario A — AC2: `[opener for S, exit for S]` leaves S idle

- Gate on conversation **B** (`active.set("sess-b")`, cursor on `testConvIDB`);
  tracker resolves `sess-a → testConvID`, `sess-b → testConvIDB`. Drain gets the
  `dropWatcher`; sink gets `discardLogger()`.
- `feedLines(sink, "sess-a", assistantTextLine(…))` — one opener, **no result
  line**, so no `TurnEnd` for this turn. No transition observer anywhere in the
  test.
- **Barrier 1:** `waitDropKind(t, drops, "text_chunk")` — the not-active drop is
  logged after `observe` on the same goroutine.
- **Assert `busy.Busy(testConvID)` is true here.** This assertion is not
  decoration — see the vacuity note below.
- `sink.exitFor("sess-a")()`.
- **Barrier 2:** `busy.WaitIdle(ctx, testConvID)` against a deadline ctx (2s,
  matching `collectEnvs`); want `nil`. The clear's `setBusy` closes `t.changed`,
  which is exactly what wakes it. `WaitIdle` inside a `_test.go` file does not
  violate AC5 — that grep excludes test files.
- Assert `busy.Busy(testConvID)` is false.

> **Vacuity hazard.** `WaitIdle` returns nil **immediately** when the
> conversation is already idle (`stream_turn_busy.go:269-271`). Without the
> mid-test `Busy` assertion, a test that pushes `[opener, exit]` and then calls
> `WaitIdle` passes even if the exit lane does nothing at all — the drain may not
> have processed the opener yet, so the conversation is idle for the wrong
> reason. Establish busy first, then push the exit.

> **Why the non-active session.** It makes the *placement* load-bearing: if the
> exit arm sat after the active-session gate, A's exit would be dropped there and
> `WaitIdle` would time out. A test run entirely on the active session cannot
> tell the two placements apart.

### Scenario B — AC4: an exit produces no emitter traffic, and a nil tracker is a no-op

- Gate on **B**; `busy = nil` (as the pre-#1201 drain tests already pass). Drain
  gets the `dropWatcher`.
- `sink.exitFor("sess-a")()` — an exit against a nil tracker.
- `feedLines(sink, "sess-b", assistantTextLine(…))` where `sess-b` is *also* not
  admitted (set `active` to a third id, or leave it unset for `ok = false`), so
  it drops at the gate.
- **Barrier:** `waitDropKind(t, drops, "text_chunk")`. The drain is serial and
  FIFO, so the trailing event's own drop proves the exit envelope was already
  fully processed. This is the trick `dropWatcher`'s doc records at
  `stream_turn_drain_test.go:67-70`, and it is the **only** option for proving a
  no-op exit was processed.
- Assert `assertNoPush(t, bcast.pushed)` and that the test did not panic.

### Scenario C — AC3: `[exit for S, opener for S]` leaves S busy

- Same wiring as Scenario A (gate on B, real tracker resolving `sess-a`).
- `sink.exitFor("sess-a")()` **first**.
- `feedLines(sink, "sess-a", assistantTextLine(…))` — the trailing opener.
- **Barrier:** `waitDropKind(t, drops, "text_chunk")` — FIFO, so the exit was
  processed before this drop was logged.
- Assert `busy.Busy(testConvID)` is **true**: the exit did not clear a turn
  opened after it.
- Add a second, bounded assertion that catches the *deferred*-clear design
  specifically: `busy.WaitIdle(shortCtx, testConvID)` with a ~250ms deadline,
  wanting `context.DeadlineExceeded`. A goroutine-dispatched clear could land
  either side of the FIFO barrier, so the `Busy` check alone catches it only
  sometimes. **The flake direction is safe:** a correct implementation always
  times out, so machine slowness makes this test more likely to pass, never to
  fail spuriously.

### Scenario D — AC1: the drop log's level and field set

- `sink := newStreamTurnSink(1, slog.New(dropWatcher{recs: recs}))` — buffer of
  1. `newStreamTurnSink` only replaces `buf` when it is `<= 0` (`:50-52`), so 1
  survives. **Start no drain** — with no reader, the fill is deterministic and
  timing-free.
- `sink.exitFor("sess-a")()` twice. The first occupies the slot; the second
  drops.
- From the captured record, assert: `Level == slog.LevelWarn`;
  `event == "stream_turn.exit_sink_full"`; `session_id == "sess-a"`; and that the
  attribute set contains **no `kind` key** and no third field beyond `event` and
  `session_id`. The negative half is the AC1 clause with teeth.

### Regression surface (unchanged, must stay green)

- Every existing test in `stream_turn_drain_test.go` and `stream_turn_busy_test.go`
  — the envelope field is additive on a keyed literal and the widened watcher is
  nil-channel-safe.
- `cmd/pyry/interactive_runner_test.go:65-72`, which reads `sink.ch` directly and
  asserts on `env.sessionID`.
- The five `internal/e2e/relay_v2_stream_*_test.go` files (interrupt, modal,
  new_session, queue_drain, send) — no frame on the v2 wire changes.

### AC5 — the unfired-ness greps, with their predicted counts

Run these and check them against the numbers, not just "looks empty" — a
name-shaped grep that under-counts in the safe-looking direction is the failure
mode #1201 hit:

| Command | Expected | Verified on `main` at `870be1c` |
|---|---|---|
| `grep -rn 'OnChildExit' cmd/pyry/ --include='*.go' \| grep -v '_test.go'` | 0 | 0 (0 even including tests) |
| `grep -rn '\.Busy(\|\.WaitIdle(' cmd/pyry/ internal/ --include='*.go' \| grep -v '_test.go'` | 0 | 0 |
| `git diff --name-only main...HEAD` | exactly `cmd/pyry/stream_turn_drain.go`, `cmd/pyry/stream_turn_busy.go`, `cmd/pyry/stream_turn_drain_test.go`, `cmd/pyry/stream_turn_busy_test.go` (test placement is the developer's call), and this spec | — |

The third row is the name-independent check and the one that actually catches an
over-reaching diff.

### Gate

`make check` (`vet test staticcheck substrate-guard e2e`). Note that an aborted
target silently skips the rest of the chain — if `test` fails, `e2e` never runs;
re-run the tail after fixing.

---

## Open questions

1. **Does `staticcheck` flag `exitFor` as unused?** It has no production caller
   after this slice. `make staticcheck` runs `staticcheck ./...`
   (`Makefile:96-102`), which analyses test files and counts test usage as usage,
   and #1098's precedent is a whole drain *function* that shipped green with only
   a unit-test caller (`stream_turn_drain.go:109-112`). Expected green — but
   confirm rather than assume, and if it does fire, escalate rather than
   restructure the lane to dodge it.

2. **Which test file gets the new scenarios?** `stream_turn_drain_test.go` owns
   the lane mechanics and the `dropWatcher` helper; `stream_turn_busy_test.go`
   owns the drain-tier tracker tests (`:441` onward). Scenarios A and C are
   tracker-state assertions and read naturally next to
   `TestStreamTurnDrainV2_BusyFedBeforeActiveGate`; B and D are lane mechanics.
   Either split or a single home is acceptable — the developer's call, with no
   bearing on any AC.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — and the design narrows the boundary rather
  than crossing it.** The exit lane carries **no data from the child**: one
  daemon-side session id and one boolean. The session id's provenance is the
  runner's construction-time `cfg.SessionID` (`streamsup_runner.go:105`), a pool
  id, never a value parsed from stream bytes or the v2 wire — the same provenance
  as `sinkFor`'s tag. `clearForSession` then resolves session→conversation
  daemon-side through the injected closure (`stream_turn_busy.go:193`), so
  `stream_turn_busy.go`'s SECURITY note ("the key is never taken from the wire")
  holds for this feed by the same argument it holds for the other two. A hostile
  or confused child can affect only its **own** conversation's membership, and
  only in the idle direction.

- **[Trust boundaries] No findings — the `clearConversation` shortcut is
  foreclosed and stays foreclosed.** Keying the lane on a conversation id would
  be a shorter call chain and would quietly retire that invariant
  (`stream_turn_busy.go:179-184`). Under this design the tempting shortcut and
  the correct call are the same length, so there is no pressure toward it. The
  review check is that `exitFor`'s parameter is named and typed as a session id
  and that no new `clearConversation`-shaped method appears.

- **[Error messages, logs, telemetry] No findings on content; one deliberate
  visibility change, checked against precedent.** The new drop record carries
  exactly `event` + `session_id` — no event content, no `kind`, and no
  conversation id. Withholding the conversation id is not an oversight but a
  structural fact: the sink closure holds no resolver and cannot name one, which
  is the same posture `clear_unresolved` takes explicitly (`:200-206`). The one
  genuine change is that `Warn` is visible at the daemon's default `LevelInfo`
  where the sibling `Debug` drops are not, so a session id now reaches the
  default-level log on this path. Verified as consistent with established
  practice rather than asserted: `session_id` is already logged above `Debug` at
  `internal/sessions/transition.go:84` and `:129`, `internal/sessions/pool.go:690`,
  `internal/relay/v2session_settings.go:111`/`:129`/`:146`, and
  `internal/relay/handlers/create_conversation.go:232`. No finding.

- **[Concurrency] No findings on lock order — no new edge is introduced.**
  `clearForSession` resolves outside `t.mu` (`:192-193`), so the exit arm never
  nests the conversations-registry mutex inside the tracker mutex. The arm itself
  takes no lock. The method now has two concurrent callers (the pool lifecycle
  goroutine and the drain goroutine); both funnel through `setBusy`, whose single
  lock acquisition covers the check, the mutation and the close-and-replace
  (`:220-239`), so there is no check-then-mutate gap and no lost-wakeup window.
  Two concurrent clears for one conversation are idempotent and same-direction —
  no interleaving yields a spurious *open*.

- **[Concurrency] No findings on goroutine lifecycle — none is created.** AC3
  forbids a deferred clear, and Scenario C's bounded `WaitIdle` deadline is the
  assertion that a goroutine-dispatched design fails. No goroutine, no leak, no
  shutdown ordering question.

- **[Concurrency] No findings on blocking.** The clear runs inline on the drain
  goroutine, which also drives `emitter.flushDelta`. Its work is one
  registry-mutex slice-header read, one map delete and one `close` — bounded, no
  I/O, no channel receive, no callback out. A stalled clear cannot wedge the
  fan-in because there is nothing in it that can stall.

- **[Network & I/O / resource exhaustion] No findings, with the rate bound named.**
  A hostile child cannot invoke the lane: only the runner's `Run` loop calls
  `OnChildExit`, once per supervision iteration, and a crash-loop is rate-limited
  by the backoff ladder (500ms initial → 30s max, `runner.go:105-107`). The
  worst case is therefore one channel send and one `Warn` line per ≥500ms, which
  is neither a channel-starvation vector (an exit that cannot fit is itself the
  thing dropped) nor a log-flood vector. This bound belongs to the wiring slice
  in practice — nothing fires here — and is recorded so #1210 need not re-derive
  it.

- **[The exploitable direction] No findings within this slice; the residual
  vector is named and assigned.** The dangerous failure is a **spurious clear**,
  because under #1199 it would release a mid-turn send into a running
  conversation; a missed clear only withholds delivery. Three routes to a
  spurious clear were walked: (a) a wrong session id — foreclosed, the id is
  daemon-side and a mismatch requires a wiring bug, not hostile input; (b) an
  exit clearing a turn opened *after* it — foreclosed by the synchronous in-arm
  clear, and AC3/Scenario C is the test that fails if a deferred design is
  chosen; (c) an exit arriving late relative to the **respawned** child's events.
  Route (c) is real and is **explicitly out of scope**: it depends on the fire
  site's position in `Run` (`internal/streamsup/runner.go:480-490`, above the
  backoff wait and above the next `spawnAndWait`) and on the key-binding
  invariant, both of which #1210 owns. It cannot be exercised here because
  nothing in production fires the lane after this slice — AC5 is the guard that
  keeps that true.

- **[Tokens, secrets, credentials]** Not applicable, with the reason: this slice
  introduces no credential, no token, and no persisted state. The only new state
  is one boolean field on an in-memory struct.

- **[File operations]** Not applicable: no path is constructed, opened, stat'd or
  written. `ImportsStayMinimal` (`stream_turn_busy_test.go:417-439`) mechanically
  enforces that `stream_turn_busy.go` gains no `os`/`io` import, and
  `stream_turn_drain.go`'s import set (`context`, `log/slog`, `turnevent`) needs
  no addition for this change.

- **[Subprocess / external command execution]** Not applicable: no
  `exec.Command`, no argument construction, no environment handling. The seam
  this lane will eventually consume (`Config.OnChildExit`) is unchanged in
  signature and unwired.

- **[Cryptographic primitives]** Not applicable: no randomness, no comparison
  against a secret, no hashing.

- **[Threat model alignment] No findings.** The relevant posture is the
  existence-oracle discipline `turnBusyTracker` records at
  `stream_turn_busy.go:20-25` (#1101, mirroring `screenSnapshotterOrNil` at
  `relay.go:395-410`): absent key ≡ idle ≡ unknown ≡ unbound ≡ never seen,
  through one map lookup. The exit lane only ever *removes* a key, so it cannot
  widen `Busy` into a "does conversation X exist" oracle. `Busy`'s signature is
  untouched, and AC5 keeps it unread by any delivery path.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-25
