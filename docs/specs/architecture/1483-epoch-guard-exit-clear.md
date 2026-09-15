# #1483 — Epoch-guard the exit clear against a stale exit envelope

## Files read

- `cmd/pyry/stream_turn_drain.go` → `streamTurnSink`, `streamTurnEnvelope`, `exitForTag`,
  `sinkForTag`, `startStreamTurnDrainV2` — the fan-in that carries the exit signal, the
  non-blocking push sites, and the drain arm that turns an exit into a clear. This is where
  the ordering token has to be minted and stamped.
- `cmd/pyry/stream_turn_busy.go` → `turnBusyTracker`, `setBusy`, `observe`, `clearForSession`,
  `openForDelivery`, `Busy`, `WaitIdle` — the mark, the one lock acquisition every feed shares,
  and the two callers of the clear with opposite ordering needs.
- `cmd/pyry/main.go` → `runSupervisor` (the `newTurnBusyTracker` construction beside
  `selectInteractiveRunner`), `newInboundDeliver` — the composition root where the sink and the
  tracker are both in scope, and the delivery seam that calls `openForDelivery`.
- `cmd/pyry/session_transition_v2.go` → `startSessionTransitionStreamV2` — the teardown feed's
  `clearForSession` call. AC4 says it must acquire no epoch condition; this is the call site
  that must stay byte-identical.
- `cmd/pyry/stream_turn_busy_test.go` → `exitLaneDrain` and the four `#1209` exit-lane tests —
  the fixture the guard has to be armed in, or AC3's regression coverage passes vacuously.
- `cmd/pyry/stream_turn_drain_test.go` → `feedLines`, `waitDropKind`, `startBusyDrainFor`,
  `TestStreamTurnSink_TagRotationRetagsBothLanes` — the barrier idiom the new drain-tier tests
  reuse, and the one place `streamTurnEnvelope` literals are compared (keyed, field-by-field, no
  `reflect.DeepEqual`, so a new field is non-breaking).
- `cmd/pyry/streamsup_runner_exit_test.go` → `waitEnvelope`,
  `TestStreamRunnerFactory_ChildExitInstallsExitFor` — the second `streamTurnEnvelope` consumer
  (also keyed), and the repo's precedent that a composition-root wiring shape is "verified by
  review, not by a test".
- `docs/knowledge/features/streamsup-package-per-conversation-turn-busy-track-exit-lane-on-the-turn-busy-fan.md`
  — the FIFO-cannot-be-overtaken claim this ticket bounds. It is the claim that is true for
  every mark fed *through* the fan-in and false for the one placed beside it.
- `docs/knowledge/features/streamsup-package-per-conversation-turn-busy-track-delivery-seam-consumer-mid-turn-hold.md`
  — why the mark precedes the write, and why `setBusy`'s `changed` return exists. Both are
  load-bearing for where the epoch gets stamped.
- `CODING-STYLE.md` § Naming, § Concurrency — the `…Locked` suffix convention (already used by
  `rekeyLocked`, `advanceLocked`, `saveLocked`) that the shared-core split below follows.

## Context

The fan-in's FIFO argument (#1209) covers every mark `observe` places, because `observe` runs on
the drain goroutine in envelope order: an exit pushed before an `observe` opener is *drained*
before it, so it can never be spent on a turn that did not exist yet.

`openForDelivery` (#1199) is the one mark placed outside that order. It is written from the
msgqueue drain goroutine, straight into `turnBusyTracker`, with no envelope of its own. So an
exit envelope still queued behind a busy conversation's event burst can be drained *after* that
mark and clear it — releasing the next queued message into a child that is mid-turn, which is
exactly the race #1199's tracker exists to prevent.

The entry point that matters today is a `new_session` rotation: `RestartFresh` rotates
`sessionID` and fires `OnSessionRotate` before it cancels, so the dying child's exit envelope
carries the **new** session id and resolves to the same conversation. The session id therefore
does not discriminate stale from live, and something else has to.

This ticket adds that something: a monotonic position on the fan-in's exit lane, stamped on each
exit envelope at push and read by `openForDelivery` when it places its mark, so the drain's exit
arm can answer "was this exit enqueued before the mark?" and decline when it was.

No ADR is warranted. This narrows an existing invariant inside one package rather than deciding
a boundary; the documentation phase folds the boundary into the exit-lane document (see
**Documentation handoff**).

### Sizing — one boundary exceeded, deliberately

Re-counted against this written plan, not against the sketch: 3 production files (≤ 5), 1 new
type (`turnBusyOption`, ≤ 5), 2 consumer call sites needing simultaneous update (the drain's exit
arm and the `runSupervisor` binding; `openForDelivery` and `newTurnBusyTracker` keep their
signatures, ≤ 10), 4 acceptance criteria (≤ 5), 1 new reject branch (≤ 10). All hold.

**Total written work does not**: ~414 lines of spec plus ~180 of production and ~230 of tests is
≈820 against a 800-line ceiling. The overage is stated rather than split, because every available
split produces a child the floor rule forbids. The only seam is "stamp exit envelopes with a
fan-in position" / "guard the clear on that stamp", and the first slice has exactly one consumer
— the second — so it is part of that ticket, not a ticket of its own, and each child would carry
its own plan and `security-sensitive` review, making the family's total larger rather than
smaller. Where the floor and the ceiling disagree, the floor wins: a slice that cannot be
verified on its own is a defect no continuation leg repairs, while a budget miss costs one leg.

The overage is also concentrated in prose rather than scope. The production change is one
envelope field, one counter, one accessor, one map value type, one locked-core split, two small
methods, one option and two wiring lines — on the order of 45 statements in two files that are
themselves ~75% comment by line.

## Design

### The ordering token

A single `uint64` counter on `streamTurnSink`, incremented once per **exit** push and never for
an event push. Counting exits alone rather than all envelopes is deliberate: only exit stamps are
ever compared, so the event hot path (`sinkForTag`, on claude's stdout forwarder goroutine) needs
no change at all, and the invariant to keep in one's head shrinks to "how many child-exit signals
has this fan-in accepted".

```go
// on streamTurnSink
exits atomic.Uint64                       // NEW field
func (s *streamTurnSink) exitEpoch() uint64   // NEW: s.exits.Load()

// on streamTurnEnvelope
exitEpoch uint64                          // NEW field; set by exitForTag only, zero on events
```

`exitForTag` stamps with `s.exits.Add(1)` **before** the non-blocking send, so the counter has
already advanced by the time any observer could see the envelope in the channel. A dropped exit
still consumes a stamp; that only inflates later stamps, which moves the guard in the declining
direction and is therefore safe.

`atomic.Uint64` rather than a mutex, for the reason `streamSessionTag` already gives on this
exact pair of goroutines: the reader is on the tracker's lock path and the writer is on the
runner's supervision goroutine, and a single atomic word has no lock ordering to state. It also
makes `streamTurnSink` non-copyable under `go vet`'s copylocks, which is already true in practice
— every use in the tree is `*streamTurnSink`.

### The mark carries its epoch

`turnBusyTracker.busy` changes from `map[string]struct{}` to `map[string]uint64`; the value is
the exit-lane position the mark is guarded against. The entry lives and dies with the mark, so no
second map and no second lifetime.

This is the one place the type's "membership and nothing else" paragraph has to be amended rather
than preserved, and the amendment is written into that paragraph in this change (the file's own
precedent — `streamSessionTag`'s note corrects a falsified sentence in the change that falsifies
it, because nothing reddens when a comment goes stale). The honest statement is narrower, not the
same: the value is a daemon-minted ordering token, never derived from anything a child produced
and never returned by any method. `Busy`'s and `ToolCallInFlight`'s signatures are untouched, so
the existence-oracle enforcement those signatures *are* is unchanged and no reader can ask
anything new.

### Where the epoch is read

Under `t.mu`, in the same acquisition that places the mark. Reading it before taking the lock
reintroduces the same bug in miniature: an exit pushed between the read and the mark is genuinely
"before the mark" but would carry a stamp above the recorded epoch and be honoured. Reading it
late is the conservative direction — an epoch that is too high declines a clear that another feed
recovers; an epoch that is too low releases a message into a live turn and nothing recovers that.

The injected source is therefore called with `t.mu` held, and its contract says so: it must be
non-blocking and take no lock. Production hands it `(*streamTurnSink).exitEpoch`, one atomic
load.

### Shared core, three doors

`setBusy`'s body moves to `applyBusyLocked` (requires `t.mu` held), keeping exactly one copy of
the close-and-replace protocol `WaitIdle`'s check-and-subscribe atomicity depends on — the
invariant #1202's extraction was written to protect. Three thin callers take the lock:

```go
// unchanged signature; every incumbent feed, unguarded, stamps epoch 0
func (t *turnBusyTracker) setBusy(conversationID string, open bool, tool toolCallDelta) (changed bool)

// requires t.mu held; the sole copy of the mutation + broadcast protocol
func (t *turnBusyTracker) applyBusyLocked(conversationID string, open bool, tool toolCallDelta, epoch uint64) (changed bool)

// the guarded clear: reports whether the guard declined it. No I/O under the lock.
func (t *turnBusyTracker) clearGuarded(conversationID string, exitEpoch uint64) (declined bool)

// NEW door for the drain's exit arm only
func (t *turnBusyTracker) clearForExit(sessionID string, exitEpoch uint64)
```

`clearForExit` resolves the session outside `t.mu` (the lock-order reason `observe` and
`clearForSession` both document), reuses their `clear_unresolved` skip verbatim, then calls
`clearGuarded`. The declined-clear diagnostic is emitted **after** the unlock, so the lock still
holds no I/O.

`clearForSession` keeps its signature, its unconditional semantics, and its single remaining
caller — the teardown feed in `startSessionTransitionStreamV2`. That call site does not change.
This is AC4 discharged structurally: the epoch condition lives on a method the teardown feed
cannot reach by name.

### Guard predicate

For an exit envelope stamped `e` arriving for a conversation whose mark records epoch `m`:

- mark absent → nothing to clear; `applyBusyLocked` is the incumbent no-op delete.
- `e <= m` → **decline**. The exit was offered to the fan-in at or before the mark was placed.
- `e > m` → clear.

Marks placed by `observe`, by `clearForSession`'s undo path, and by `openForDelivery` on a
tracker with no epoch source all record `0`, and every real exit stamp is `>= 1`, so all three
clear exactly as they do today. For `observe` that is not a concession but the FIFO argument
restated: an exit pushed before an `observe` mark is drained before it exists.

`applyBusyLocked` writes the epoch only on the idle→busy transition, so a mark keeps the epoch of
whoever actually placed it. A later `openForDelivery` on an already-busy conversation returns
`false` and changes nothing, and a later `observe` opener likewise; neither can lower a standing
mark's guard.

### Wiring

`openForDelivery` and `newTurnBusyTracker` both keep their signatures. The epoch source is bound
by one construction-time option:

```go
type turnBusyOption func(*turnBusyTracker)
func withExitEpoch(exitEpoch func() uint64) turnBusyOption
func newTurnBusyTracker(resolve …, logger *slog.Logger, opts ...turnBusyOption) *turnBusyTracker
```

The variadic form is load-bearing rather than stylistic. A required third parameter would touch
38 construction sites across eight test files — over the one-ticket call-site boundary, and the
kind of cascade whose cost is not reduced by each edit being trivial. A post-construction setter
would trade that for a "call before publishing" contract the race detector cannot check. The
option keeps construction atomic and leaves every incumbent call site untouched.

One line changes in `runSupervisor`, where `streamSink` and the `newTurnBusyTracker` call are
already adjacent:

```go
turnBusy = newTurnBusyTracker(resolveClosure, logger, withExitEpoch(streamSink.exitEpoch))
```

### Drain arm

```go
if env.exit {
    busy.clearForExit(env.sessionID, env.exitEpoch)
    continue
}
```

Still the arm's first statement, still synchronous on the drain goroutine, still nil-receiver
safe. #1209's three placement arguments (before `observe`, before the active-session gate, before
`Handle`) are untouched.

## Concurrency model

No new goroutines. Three existing ones meet at the new token:

| Goroutine | Touches | How |
|---|---|---|
| runner supervision (per live runner) | `sink.exits` | `Add(1)` in `exitForTag`, then a non-blocking send |
| msgqueue per-conversation drain | `sink.exits`, `t.busy` | `Load()` under `t.mu` inside `openForDelivery` |
| stream-turn drain (single) | `t.busy` | `clearForExit` under `t.mu` |

The correctness argument is one line of the memory model. `sync/atomic` operations are
sequentially consistent, so if an exit's `Add` returned `e` and its send completed before the
mark, then the mark's `Load` observes at least `e`, giving `e <= m` and a decline. Unrelated
runners' exits only raise stamps, never lower them, so a global counter cannot manufacture a
wrong decline for a conversation whose own exit comes later.

Lock ordering is unchanged: `t.mu` remains a leaf. `resolve` (which takes the conversations
registry's mutex) stays outside it in every path, and the one new call-out under it —
`t.exitEpoch()` — is an atomic load that takes no lock, with that requirement stated on the
field.

## Error handling

One new failure mode, and it is the deliberate one: **the declined clear**.

A decline leaves a mark standing that, in the unlucky case where the guard was wrong, would need
another feed to clear. Three exist — the turn's own `TurnEnd` through `observe`, a pool teardown
through `clearForSession`, and the next exit on that runner — and the residual beyond all three
is a legitimately running turn, bounded by `streamTurnHoldTimeout` per attempt and by msgqueue's
give-up across them, surfacing as a typed `session_error` exactly as #1199 AC3 already specifies.
The opposite failure — honouring a stale clear — releases a queued message into a live turn and
corrupts it, with nothing downstream to recover it. So the guard declines when uncertain.

Diagnostic on the decline: `Debug`, matching `clear_unresolved`'s level and field set, with
`event: "stream_turn.clear_stale_exit"` and `session_id` only. No conversation id (the routing
key the tracker treats as sensitive), no epoch values — the counters would be convenient for
diagnosis but the type's logging posture is deliberately narrow, and convenience is not a strong
enough reason to widen it.

No new error values, no new wrapping, no API surface change.

## Testing strategy

**RED first.** The drain-tier stale-exit test is written and watched fail against the unguarded
clear before any production line is written.

*Unit tier — `cmd/pyry/stream_turn_busy_test.go`*

- **`clearForExit` guard table.** Sub-cases: delivery mark at epoch `k` vs exit `k` → still busy
  (AC1); delivery mark at `k` vs exit `k+1` → idle (AC2); `observe`-placed mark vs exit `1` →
  idle (AC3); tracker with no epoch source, delivery mark, exit `1` → idle (incumbent behaviour
  preserved for every existing construction site); unresolvable session leaves an unrelated busy
  conversation untouched; nil receiver does not panic.
- **Teardown feed ignores the epoch (AC4).** Place a delivery mark at a high epoch, then
  `clearForSession` → idle. Fails for any implementation that pushes the condition into the
  shared path.
- **The mark keeps its placer's epoch.** A delivery mark at `k` followed by an `observe` opener
  still declines exit `k`; an `observe` mark followed by `openForDelivery` (which opens nothing)
  still clears on exit `1`. This is what forbids re-stamping on an already-busy conversation.

*Fan-in tier — `cmd/pyry/stream_turn_drain_test.go`*

- **Stamping.** Two exits and two events through one sink: the exits carry `1` and `2` in push
  order, the events carry `0`, and `exitEpoch()` reports `2`. Mirrors
  `TestStreamTurnSink_TagRotationRetagsBothLanes`' drain-free read-straight-off-the-channel shape,
  so nothing is timing-dependent.

*Drain tier — `cmd/pyry/stream_turn_drain_test.go`*

- **The filed scenario (AC1).** Build sink + tracker wired with `withExitEpoch(sink.exitEpoch)`.
  Push the exit for `sess-a` while **no drain is running** (the fan-in is buffered, so this is
  deterministic and timing-free), then `openForDelivery`, then start the drain, then barrier on a
  trailing unresolvable-session event's not-active drop via `waitDropKind` — which proves the
  drain has already returned from the exit arm. Assert the conversation is still busy.
- **AC2 at drain tier.** Drain running, `openForDelivery` first, then `sink.exitFor("sess-a")()`;
  `WaitIdle` returns nil.
- **AC3 regression, armed.** `exitLaneDrain` is changed to build the sink first and wire the
  tracker with `withExitEpoch(sink.exitEpoch)`, so the four incumbent #1209/#1917 exit-lane tests
  run against the guarded path instead of an unwired one. Without this the AC3 coverage would
  pass vacuously — the guard would never be consulted in the tests that assert it holds.

**Non-vacuity check.** Make `clearForExit` unconditional (drop the `e <= m` arm) and re-run: the
AC1 unit sub-case and the drain-tier filed-scenario test must go red while the AC2/AC3/AC4 cases
stay green. Run once; restore.

**Gate.** `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`. The full-module
race suite is the verifier's gate, not this run's.

**Not covered by a test, by design.** The one-line `runSupervisor` binding is a composition-root
shape with no seam to assert against — the same class as `TestStreamRunnerFactory_ChildExitInstallsExitFor`'s
"code-shape criteria … verified by review, not by a test". The drain-tier tests use the identical
`withExitEpoch(sink.exitEpoch)` binding, so what is unverified is the single call in
`runSupervisor` and nothing about the mechanism.

## Documentation handoff

**Pending for the documentation phase.** Requirement carried verbatim from the ticket's
Documentation handoff section:

- **Path:** `docs/knowledge/features/streamsup-package-per-conversation-turn-busy-track-exit-lane-on-the-turn-busy-fan.md`
- **Section:** the opening paragraph, which states that the fan-in is "FIFO with a single reader,
  so it cannot be overtaken".
- **Requirement:** record the boundary of that claim — it holds for marks fed *through* the
  fan-in (`observe`), and `openForDelivery`'s direct mark is the one outside it, which is what
  this ticket's exit-lane epoch guard covers.

No documentation file is edited by this ticket.

## Open questions

1. **Does arming `exitLaneDrain`'s guard change any incumbent assertion?** Expected no — every
   mark in those four tests is `observe`-placed and therefore epoch 0, so every exit stamp
   exceeds it. Resolve by running them; if one moves, the guard's predicate is wrong, not the
   test.
2. **Does `go vet`'s copylocks flag any `streamTurnSink` use once it holds an `atomic.Uint64`?**
   Expected no — every use in the tree is through a pointer and `newStreamTurnSink` returns
   `&streamTurnSink{…}`. Resolve by running `go vet ./...`.
3. **Is `exitEpoch` the right name for the sink accessor given the envelope field shares it?**
   Resolve during implementation; if the pairing reads badly, rename the accessor rather than the
   field — the field is the one that appears in test literals.

## Revisions

**2026-09-15 — Phase B.** The design landed as planned; the three Open Questions resolved without
changing it.

1. **Arming `exitLaneDrain` moved no incumbent assertion.** All four #1209/#1917 exit-lane tests
   pass against the armed fixture, as predicted: every mark in them is `observe`-placed and
   therefore records 0, which is below every real stamp. The fixture was split into
   `exitLaneDrainDeferred` (drain not yet started) with `exitLaneDrain` delegating to it, so the
   new AC1 drain-tier test can state its ordering instead of racing for it. No incumbent caller
   changed.
2. **`go vet ./...` is clean** with `atomic.Uint64` on `streamTurnSink`; nothing in the tree copies
   the sink by value. `staticcheck`, `gofmt`, `make cite-guard` and `make docs-guard` are clean too.
3. **Naming kept.** `exitEpoch` names the sink accessor, the envelope field and the tracker's
   injected source, because in all three it means the same thing — a position on the fan-in's exit
   lane. One addition the plan did not name: `exitEpochLocked`, a two-line reader on the tracker
   that answers 0 for an unbound source, so `openForDelivery`'s single lock acquisition stays one
   statement and the `…Locked` suffix carries the held-mutex requirement.

The SHOULD FIX from the security review is implemented as specified: the fail-open direction is
stated on the `exitEpoch` field, on `withExitEpoch`, on `exitEpochLocked`, and at the
`runSupervisor` binding, and it is asserted by the "unarmed tracker declines nothing" sub-case.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The new value never crosses a boundary: the exit epoch is
  minted by `exitForTag` from a daemon-owned `atomic.Uint64` and is never parsed from the wire,
  from claude's stdout, or from a `send_message` payload. The one field a child influences —
  *when* its exit fires — is already the child's own lifecycle. Counting **exit** pushes only
  (not event pushes) narrows that influence further: a child flooding the fan-in with events
  cannot move the counter at all. The conversation key the clear applies to is still resolved
  daemon-side from the runner's own session tag, unchanged.
- **[Trust boundaries]** **SHOULD FIX** — the guard **fails open when unwired**. A tracker built
  without `withExitEpoch` stamps every mark `0`, so every exit clears and the behaviour silently
  reverts to the pre-#1483 bug: no test reddens (36 of the 38 construction sites are unwired by
  design) and no log line fires. Phase B must state the fail-open direction explicitly on both
  `withExitEpoch`'s doc and the `exitEpoch` field's, at the point a developer touching the wiring
  would read it. Runtime enforcement was considered and rejected: panicking on a nil source breaks
  every incumbent construction site, and a construction-time `Warn` would fire on ~36 test
  constructions to defend against a failure that has not been observed. The residual is that the
  single `runSupervisor` binding is review-verified, which the Testing strategy already names.
- **[Tokens, secrets, credentials]** Not applicable, and the reason is worth stating rather than
  ticking: the epoch is an **ordering** predicate, not an authorization one. Declining or
  honouring a clear changes no access decision — the conversation the clear resolves to, and the
  two daemon-side gates `openForDelivery`'s key already passes, are untouched by this ticket. The
  counter is monotone and fully predictable by construction, so it must never be reused as a
  nonce or a uniqueness source; the field's doc says so.
- **[File operations]** Not applicable. No path, no file, no mode, no persistence. The change is
  in-memory state on a type that deliberately survives no restart.
- **[Subprocess execution]** Not applicable. Nothing about when a child is spawned, signalled or
  reaped changes; `exitForTag` still fires from the runner's supervision goroutine after the fact.
- **[Cryptographic primitives]** Not applicable — no randomness, no comparison against a secret.
  `crypto/subtle` is irrelevant here: `e <= m` compares two daemon-minted counters, neither of
  which is attacker-controlled, so a timing side channel discloses nothing that the presence of
  the log record would not.
- **[Network & I/O]** No findings. No new I/O and no change to the fan-in's bound: `droppableCap`
  and `streamTurnSinkCloseReserve` are untouched, exits stay closing-class and unwatermarked, and
  the envelope grows 8 bytes (≈2 KB across the whole 256-slot buffer). The `busy` map's value
  widens from zero to 8 bytes per entry while its key set — conversations currently mid-turn —
  is unchanged, so no new growth vector exists. `uint64` overflow needs ~1.8e19 child exits;
  at a sustained 1000 crashes/second that is ~5.8e8 years, so wraparound is not defended against.
- **[Error messages, logs, telemetry]** No findings, by an explicit decision. The decline record
  carries `event` + `session_id` and nothing else — no conversation id (the routing key the
  tracker treats as sensitive alongside session ids and `workspace_cwd`) and, deliberately, **no
  epoch values**: the counter is global across runners, so stamping it on one conversation's
  record would disclose the daemon's total exit volume into a record about a single conversation.
  The operator owns every conversation, so the live leak is nil; the reason to withhold is
  posture-consistency with `clear_unresolved`, which is what keeps this type's logging surface
  auditable at a glance.
- **[Concurrency]** No findings, after walking three specific hazards.
  (a) *TOCTOU on the guard* — the `e <= m` read and the membership delete happen under one
  acquisition of `t.mu` in `clearGuarded`. Split across two acquisitions, a mark placed in the gap
  would be cleared by an exit that predates it, which is the filed bug re-opened one layer down.
  (b) *Lock ordering* — `t.mu` stays a leaf. The one new call-out under it, `t.exitEpoch()`, is
  contracted non-blocking and lock-free; production hands it one atomic load. Passing
  `*atomic.Uint64` instead of `func() uint64` would make that a type-level fact, and was rejected
  because handing out the pointer grants `Add` where only a read is needed — a write capability
  for a documentation win. The opaque-func-plus-contract shape is exactly how `resolve` already
  carries the *opposite* contract (MUST be called outside the lock) in this same type.
  (c) *Stale resolution* — `clearForExit` resolves the session outside `t.mu` and acts under it,
  so the binding can move in between. That window is pre-existing and deliberate (it is the
  lock-order reason `observe` and `clearForSession` both document), and this ticket neither
  widens nor narrows it. No goroutines are added, so no leak is possible; nothing is persisted, so
  a mid-write signal leaves no partial state.
- **[Concurrency — cross-conversation influence]** No findings, and this is the adversarial case
  the design was checked against hardest. The counter is **global**, so a hostile or crash-looping
  child on conversation C does move the stamps conversation A's guard compares. Both directions
  are bounded. C can only *raise* stamps, and a raised stamp makes A's own later exit more likely
  to clear, never less — so C cannot wedge A. C cannot *lower* a stamp or make A's mark record a
  low epoch while a stale exit for A carries a high one, because A's mark reads the counter after
  that exit's `Add` returned. This is the same bound the type's nested-`inflight` paragraph
  already claims: the worst a child achieves is a false statement about itself.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model, three relevant threats.
  **#1 prompt injection (`partial`)** — this ticket *reduces* exposure rather than adding any: the
  bug it closes lets a second queued operator message be written into a child that is mid-turn,
  interleaving two messages into one turn, which is precisely the input-boundary confusion #1's
  mitigations assume does not happen at the daemon. **#7 denial of service (`deferred`)** —
  walked concretely, because a declined clear parks delivery for up to `streamTurnHoldTimeout`
  (15 min per attempt, ≈30 min to msgqueue's give-up). A *wrong* decline would need a mark placed
  after its own child's exit with no respawn; in that case `WriteUserTurn` fails in the same
  statement sequence and `openForDelivery`'s incumbent `undo` clears the mark, so the wedge path
  is already closed and no new DoS surface is opened. **#5 implementation bugs
  (`defense-in-depth`)** — the fix direction is fail-closed by construction and the non-vacuity
  check in the Testing strategy is the mutation evidence for it. No wire frame, no relay surface
  and no protocol version changes, so threats #2, #3, #4, #6 and #8 have no contact with this
  diff.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-15
