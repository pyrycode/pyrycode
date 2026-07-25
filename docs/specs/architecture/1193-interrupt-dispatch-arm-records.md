# Spec: Record the interrupt route's dispatch arm and its neither-method inert case (#1193)

**Size:** S (confirmed — see § Scope check). **Security-sensitive** (log hygiene on an
internet-exposed frame route, and this slice is the first to touch the *actuation*
dispatcher; see § Security review).

Split child B of #1190. Unblocked by #1192 (PR #1194, merged 2026-07-25), which landed the
three signature-stable records and the `activeInterrupter.log` field this slice consumes.

## Files to read first

- `cmd/pyry/main.go:1252-1270` — `interruptRunner`'s doc comment + body. **The function this
  slice changes.** Extract: the two-arm type switch, *Interrupt matched first* (load-bearing —
  see § Design D4), and the `default: return nil` inert arm.
- `cmd/pyry/main.go:1322-1352` — `activeInterrupter.SendEsc`: doc comment (`:1322-1334`) and
  body (`:1335-1352`). Extract: the two existing `Info` records and their exact shape, the
  `a.logger()` call form, and the sole call site `return interruptRunner(r)` at `:1351`.
  **`:1330-1334` is one of the two interim caveats AC5 flips.**
- `cmd/pyry/main.go:1300-1320` — the `activeInterrupter` struct (`:1300`), its optional
  `log *slog.Logger` field (`:1306`), and the nil-normalizing `logger()` accessor (`:1315`).
  Extract: **consume `a.logger()`, never `a.log`** — the field is deliberately optional
  because the struct is a constructor-less named-field literal.
- `cmd/pyry/main.go:1272-1291` — `resolveBoundRunner`. Extract: its `(sessions.Runner, bool)`
  return — no session id reaches the caller, which is *why* the new records identify the
  conversation. **Do not touch the `conv.CurrentSessionID == ""` guard at `:1283`** (#678
  cross-conversation isolation enforcement point; `Pool.Lookup("")` returns the bootstrap
  session).
- `cmd/pyry/main.go:984-1002` — production wiring. Extract: the one production
  `activeInterrupter{…}` literal at `:996`, already carrying `log: logger` at `:1001`
  (#1192). **This slice adds no wiring here.**
- `cmd/pyry/main.go:735-738` — daemon log level construction: default `slog.LevelInfo`,
  `Debug` only behind `-pyry-verbose` (`:688`). This is what forces the level decision in
  § Design D3 and the AC5 caveat.
- `cmd/pyry/interrupt_routing_test.go:29-51` — the three runner stubs
  (`interruptRunnerStub:32`, `sendEscRunnerStub:42`, `inertRunnerStub:51`). Extract: **all
  three already exist** — no new fakes are needed, they just need to be driven through
  `activeInterrupter` as well as through `interruptRunner`.
- `cmd/pyry/interrupt_routing_test.go:58-96` — `TestInterruptRunner_Dispatch`. Extract: the
  **five** direct call sites at `:63,73,82,89,92` (every one changes shape under D1), and
  the "error from the chosen method propagates" subtest at `:87`.
- `cmd/pyry/interrupt_routing_test.go:157-259` — `TestActiveInterrupter`. Extract: the four
  literals at `:179,209,230,251` (**`:251` is the one with no `log:` — it must gain one, see
  § Testing strategy**), the exact-event constants at `:170-173`, and the arm-exclusivity
  negative loop at `:198-203` that must stay green.
- `cmd/pyry/modal_resolve_v2_test.go:83-88` — `auditLogger()`: **JSON**-backed, Debug-level,
  returns a plain `*bytes.Buffer`. Note `auditRecords()` at `:90-92` filters on
  `msg == "audit: remote permission decision"` and is **not** reusable here.
- `internal/relay/v2session_modal.go:434-492` — `handleInterrupt`. Extract: the interim
  caveat at `:466-468` (**the second caveat AC5 flips**), the `Debug` nil-`Interrupter` arm
  at `:480-485` (the level caveat AC5 requires), and the `v2.interrupt.keystroke_err` `Warn`
  at `:486-491` — which is why the new success-path record does **not** need to carry the
  error (§ Design D2).
- `internal/relay/v2session_interrupt_test.go:38-56` — the sibling negative-assertion
  comment. Extract: it says "#1193 adds a success-path record"; that rule stays **true** and
  the test needs no change (§ Testing strategy, "the two test comments").
- `internal/streamsup/runner.go:250-264` — `Runner.Interrupt`'s contract: one stdin
  control_request line, `ErrNoLiveChild` when no child is live, "safe from any goroutine".
- `internal/supervisor/modal.go:69-71` and `:85-95` — `Supervisor.SendEsc` → `sendModalKey`:
  capture-then-release, `ErrNoLiveSession` when detached, wrapped error on PTY failure.
  Together with the previous entry these are why the record is emitted **after** the arm
  returns (§ Design D2, "ordering").
- `docs/specs/architecture/1192-interrupt-inert-arm-records.md` — the predecessor spec. Its
  § D2 (level), § D3 (record shapes), and § "Open questions" #3 (event-family stability) are
  the fixed points this slice builds around.
- `docs/knowledge/codebase/1121.md` — the route's origin: why `activeInterrupter` exists and
  why `interruptRunner` lives in `cmd/pyry` (the only package that sees both concrete runner
  types).

## Context

On 2026-07-24 a live desktop run (`real-claude-interrupt`, pyrycode-desktop#483) showed an
interactive interrupt that never quiesced the turn on the `interactive_runner: pty` path.
The daemon log recorded two claude spawns and then 2m10s of nothing.

That silence was consistent with four different faults with four different fixes: the frame
never arrived, the conn was not interactive, the route went silently inert, or the keystroke
actuated and claude ignored it. #1192 removed three of those ambiguities. **Two silent paths
remain, and they are the two on the far side of `interruptRunner`:** a successful actuation
and a resolved runner exposing neither interrupt method. Today a successful actuation is
exactly as silent as a runner that was never actuated, and nothing else on the route is.

Closing them buys a second diagnostic for free. Once every path *through* the route emits,
an empty `v2.interrupt.*` log on a wired daemon means the frame never reached the handler —
no separate "frame arrived" record needed. That invariant is **false** today, which is why
two doc comments carry an explicit interim caveat (`cmd/pyry/main.go:1330-1334`,
`internal/relay/v2session_modal.go:466-468`). Flipping both from caveat to invariant is part
of this slice; leaving either behind would leave a doc comment that is actively false on the
exact route this work exists to make legible.

Whether an inert arm is *correct* — whether the route should have actuated — is a separate
question owned by **#1191**. This slice records the arm taken and changes no verdict.

## Design

One new unexported type, one changed signature, two emission sites, two doc-comment flips.
No new files, no new exported types, **no control-flow change**: every arm actuates exactly
what it actuates today and returns exactly what it returns today.

### D1 — Direction: return the arm upward; emit in `SendEsc`

The ticket leaves the direction open. **Return the arm upward.**

`interruptRunner` gains a second return value naming the arm it took, and stays otherwise
byte-identical. `activeInterrupter.SendEsc` — which already owns the route's other two
records and is the only scope holding the conversation id — selects and emits the record.

```go
// interruptArm names which actuation interruptRunner dispatched to. The constant
// VALUES are operator-facing: they are logged verbatim as the `arm` field, so
// they are part of the record contract, not an internal detail.
type interruptArm string

const (
	armInterrupt interruptArm = "interrupt" // streamRunner.Interrupt()
	armSendEsc   interruptArm = "send_esc"  // *supervisor.Supervisor.SendEsc()
	armNone      interruptArm = "none"      // runner exposes neither — inert
)

// interruptRunner returns the arm it dispatched to alongside the chosen method's
// error, unchanged. armNone is always paired with a nil error.
func interruptRunner(r sessions.Runner) (interruptArm, error)
```

Why this direction:

- **`interruptRunner` stays pure.** It remains a total function of its argument with no
  logger, no I/O, and no ambient state — the property that makes `TestInterruptRunner_Dispatch`
  a five-line-per-case table. Pushing a `*slog.Logger` and a `convID` down into it would make
  a pure dispatcher observability-aware and would force a *second* nil-logger normalization
  site alongside the one `logger()` already provides.
- **Emission stays where the context is.** The conversation id is in scope exactly once, in
  `SendEsc`. Returning the arm moves one enumerated value up; pushing the logger down moves
  two values (logger + convID) into five test call sites that care about neither.
- **One emission site, not three.** Emitting inside `interruptRunner` means three near-identical
  `Info` calls, one per case. Emitting in `SendEsc` means one `if`/`else` beside the two
  records already there, so the whole `v2.interrupt.*` family that `cmd/pyry` owns is written
  in one function.
- **The test consequence is a strengthening, not a cost.** `TestInterruptRunner_Dispatch`'s
  five call sites become `arm, err := interruptRunner(…)` and gain an arm assertion — which
  is a *more direct* proof of dispatch than the existing call-count check, with no logger
  plumbed into a test that has no business owning one.

**MUST NOT:** nothing may branch on the returned arm except record selection. The arm is an
observability value. In particular, `armNone` must not trigger any fallback actuation — a
fallback to the bootstrap supervisor is precisely the #678 cross-conversation isolation break
`resolveBoundRunner`'s guard exists to prevent. `armNone` returns `nil` to the caller and the
route stays inert.

Ruled out (in addition to the three the ticket already rules out):

- **Push `log` + `convID` into `interruptRunner`.** Covered above. It does buy one thing this
  design does not — see "ordering" in D2 — and that trade is taken deliberately.
- **`interruptRunner(r) (interruptArm, func() error)`** — return the arm plus an unfired
  thunk, so `SendEsc` can emit *before* actuating with one type switch and a pure function.
  Technically satisfies every constraint; rejected as clever-over-clear (a closure-returning
  dispatcher is not a Go idiom this codebase uses anywhere) for a failure mode with no
  evidence behind it.
- **One event `v2.interrupt.dispatch` with `arm` ∈ {interrupt, send_esc, none}.** One record,
  one emission — but it collapses "actuated" and "went inert" into a single event name,
  against AC2's family convention (every other outcome on this route has its own `event`) and
  against the existing tests' discipline of asserting outcomes by exact event name.

### D2 — The two records

| # | Arm | Event | Fields beyond slog built-ins | Level |
|---|-----|-------|------------------------------|-------|
| 4 | `Interrupt()` or `SendEsc()` dispatched | `v2.interrupt.dispatched` | `conversation_id`, `arm` | `Info` |
| 5 | runner exposes neither | `v2.interrupt.no_actuator` | `conversation_id` | `Info` |

(Numbering continues #1192's arms 1–3: `non_interactive`, `no_active_conv`, `no_bound_runner`.)

Contracts:

- **`v2.interrupt.no_actuator`** completes the family's `no_<thing>` progression —
  no active conv → no bound runner → **no actuator**. It carries `conversation_id` (the
  house key, `interactive_turn_v2.go:260`; **not** `conv_id`) and nothing else.
- **`v2.interrupt.dispatched`** carries `conversation_id` plus `arm`, logged as
  `string(arm)`. **Log the conversion, not the named type** — `slog` renders a named string
  type through the `Any` path, and `internal/relay`'s TextHandler and `cmd/pyry`'s
  JSONHandler do not agree on how that renders. `string(arm)` is a guaranteed `KindString`
  attr in both. (This exact cross-handler divergence is what #1192's spec flagged as its
  most likely source of wasted debugging turns.)
- **`dispatched` is emitted unconditionally once an arm actuated — including when that arm
  returns an error.** It records *which arm was dispatched to*, not that the child quiesced;
  the event name is `dispatched`, not `actuated`, for exactly that reason. The failure is
  already reported one level up by `v2.interrupt.keystroke_err` (`Warn`,
  `v2session_modal.go:486-491`), which carries the error but **not** the arm — so on a failed
  actuation the pair of records tells the operator *which* actuation failed, which neither
  record can say alone. Suppressing `dispatched` on error would delete that.
- **No `err` field on `dispatched`.** `keystroke_err` already carries it; duplicating the
  error string here would double-log it for no gain. AC3 wants identifiers and enumerated
  outcomes; that is exactly what these two records carry.

**Ordering.** The record is written **after** the arm returns, because the arm identity only
becomes available at that point under D1. Consequence, stated so the doc comments do not
over-claim: if an actuation ever *blocked forever*, the route would leave no `dispatched`
record. Both actuations are single small writes with no loop and no context
(`streamsup.Runner.Interrupt` → one stdin line; `Supervisor.SendEsc` → `sendModalKey`, which
releases `sessMu` before writing), and no such hang has been observed — so this is not
designed against, only documented. `SendEsc`'s doc comment says the record is written when
the arm returns; the relay-side invariant is unaffected because a wedged actuation would
also wedge the manager's single Run dispatch goroutine, a far louder symptom than a missing
log line.

### D3 — Level: `Info`, both records

AC2 requires default-verbosity visibility. The daemon's default is `slog.LevelInfo`
(`main.go:735`); `Debug` needs `-pyry-verbose`. A `Debug` record would leave the operator
exactly where the 2026-07-24 run left them — reproduce, read the log, see nothing — for an
intermittent live failure that may not reproduce. This matches #1192's D2 and keeps the whole
`cmd/pyry`-side family uniformly visible.

Flood bound: both records fire only on an inbound `interrupt` frame that already passed the
interactive-capability gate and resolved a bound runner. One line per actuated interrupt,
against an actuation that itself writes to the child. Not a log-amplification primitive
(see § Security review).

### D4 — What must not change

- **The type-switch order.** `Interrupt()` is matched before `SendEsc()` so a future runner
  growing both prefers the stream-json control_request over a PTY Esc. Pinned by
  `TestInterruptRunner_Dispatch`.
- **The inert default.** An unknown runner returns `(armNone, nil)` — "no actuation beats
  wrong actuation".
- **`resolveBoundRunner`** — untouched, including the `conv.CurrentSessionID == ""` guard.
- **The `relay.Interrupter` seam** (`internal/relay/v2session_seams.go:42`) — unchanged, so
  its 8 implementors are untouched.

### D5 — The two doc-comment flips (AC5)

`grep -rn '#1193'` finds four in-code sites. Two go false and must flip; two stay true.

**Flip — `activeInterrupter.SendEsc`, `cmd/pyry/main.go:1330-1334`.** Replace the "successful
actuation is still silent until #1193" sentences with: every arm of `SendEsc` records at
`Info`; combined with `handleInterrupt`'s records, an interrupt reaching the route always
leaves at least one `v2.interrupt.*` record, so on a wired daemon an empty log means the
frame never arrived. Add the ordering clause from D2 (the `dispatched` record is written when
the arm returns). Keep the existing "records identify the CONVERSATION, not the session"
sentence.

**Flip — `handleInterrupt`, `internal/relay/v2session_modal.go:466-468`.** Replace the "still
silent on SUCCESS until #1193 lands" sentences with the same invariant, **plus the level
caveat AC5 requires**: the nil-`Interrupter` arm (step 2, `:481`) records at `Debug`, which is
invisible at the daemon's default `LevelInfo` (`cmd/pyry/main.go:735`, raised only by
`-pyry-verbose`) — hence "on a **wired** daemon". Production always wires the interrupter
(`main.go:996`), so the qualifier is precise rather than weaselly. Also update step 3 of the
numbered list (`:459-462`) to note the actuation now records which arm dispatched.

**Do not flip — `cmd/pyry/interrupt_routing_test.go:164-166` and
`internal/relay/v2session_interrupt_test.go:53-55.`** Both say negative assertions name one
event exactly and never the `v2.interrupt.` prefix, *because* #1193 adds a success-path
record. That rule is permanent and the reason for it is now historical fact rather than a
pending change. Retensing "adds" → "added" is optional; **weakening or deleting the rule is
not** — a prefix-wide absence assertion would go red on the next record added to this family.

`interruptRunner`'s own doc comment (`:1252-1260`) gains one clause naming the returned arm
and stating that the constant values are operator-facing.

### Data flow (unchanged control flow, two new taps)

```
inbound interrupt frame
  → V2SessionManager.handleInterrupt(s)
      ├─ !s.interactive ─────────────────► Info  v2.interrupt.non_interactive {conn_id}   → return
      ├─ Interrupter == nil ─────────────► Debug v2.interrupt.inert {conn_id}             → return
      └─ Interrupter.SendEsc()
           → activeInterrupter.SendEsc()
               ├─ convID == "" ──────────► Info  v2.interrupt.no_active_conv {}           → nil
               ├─ !resolveRunner(convID) ► Info  v2.interrupt.no_bound_runner {conv}      → nil
               └─ arm, err := interruptRunner(r)
                     ├─ armNone ─────────► Info  v2.interrupt.no_actuator  {conv}     [4] → err (nil)
                     └─ otherwise ───────► Info  v2.interrupt.dispatched   {conv, arm}[5] → err
           ← err != nil ─────────────────► Warn  v2.interrupt.keystroke_err {conn_id, err}
```

Every leaf emits. That is the invariant D5 records.

## Concurrency model

No new goroutines, no locks, no shutdown sequence, no new shared state.

- Both records emit on whichever goroutine called `SendEsc` — in production the manager's
  single Run dispatch goroutine, via `handleInterrupt`.
- `interruptRunner` stays a pure function of its argument; the added return value is a stack
  value, not shared state. `activeInterrupter` remains an immutable value copied at wiring.
- `*slog.Logger` is safe for concurrent use.
- Both actuations are already documented safe from any goroutine
  (`streamsup/runner.go:261`, `supervisor/modal.go:85-90`); neither is called differently here.

Test-side: `TestActiveInterrupter` drives `SendEsc` synchronously on the test goroutine, so
`auditLogger()`'s plain `*bytes.Buffer` needs no synchronisation and no polling helper.

## Error handling

Emission is the failure path; there is no new failure mode.

- **The error from the chosen arm is returned unchanged**, in both branches. Write the record,
  then return `err` — do not return a literal `nil` on the `armNone` branch. `armNone` pairs
  with a nil error by construction today, and returning `err` keeps that a property of
  `interruptRunner` rather than a duplicated assumption at the call site.
- `a.logger()` cannot return nil, so no literal can panic on an unwired `log` field. Note
  `interrupt_routing_test.go:251` is the one literal that omits `log` and it is now on an
  emitting path — see § Testing strategy.
- `slog` swallows its own emission errors; nothing to handle.
- Whether an inert arm *should* have actuated is **#1191**'s question, not this ticket's.

## Testing strategy

Scenarios only; the developer writes them in the project's idiom. Everything lives in
`cmd/pyry/interrupt_routing_test.go` — **no relay-side test changes**, because under D1 both
records are emitted in `cmd/pyry` and `internal/relay`'s fake `Interrupter` never reaches
`interruptRunner`.

`auditLogger()` is a **JSONHandler** → assert `"event":"v2.interrupt.dispatched"`. Do **not**
reach for `auditRecords()`; it filters on the audit message and returns zero records here.

### `TestInterruptRunner_Dispatch` — the five call sites

Each existing subtest keeps its call-count assertion and gains an arm assertion:

- `Interrupt()` runner → `(armInterrupt, nil)`, `calls == 1`.
- `SendEsc()` runner → `(armSendEsc, nil)`, `calls == 1`.
- neither method → `(armNone, nil)` — keep the "no actuation beats wrong actuation" message.
- error propagates → both stubs still return the wrapped error **and** their correct arm
  (this is what proves the arm is not derived from the error).

### `TestActiveInterrupter` — the record assertions (AC4)

Add `dispatchedEvent = `"event":"v2.interrupt.dispatched"`` and
`noActuatorEvent = `"event":"v2.interrupt.no_actuator"`` beside the two existing exact-event
constants at `:170-173`.

- **`"interrupt reaches the bound runner, not the bootstrap"` (`:175`)** — now also the
  `Interrupt()`-arm record vehicle. Add: buffer contains `dispatchedEvent`, `"arm":"interrupt"`,
  and `"conversation_id":"A"`. **Keep the existing negative loop at `:198-203` unchanged** —
  it names the two inert events exactly, so a distinct `dispatched` event leaves it green.
- **NEW subtest — the `SendEsc()` arm records `arm: send_esc`.** Wire `auditLogger()`;
  `currentConv` → `"A"`; `resolveRunner` returns a `&sendEscRunnerStub{}`. Assert: the stub's
  `calls == 1`, buffer contains `dispatchedEvent` + `"arm":"send_esc"` + `"conversation_id":"A"`,
  and contains **no** `noActuatorEvent`.
- **NEW subtest — a runner exposing neither method records that it went inert.** Wire
  `auditLogger()`; `resolveRunner` returns an `&inertRunnerStub{}`. Assert: `SendEsc()` returns
  `nil`, buffer contains `noActuatorEvent` + `"conversation_id":"A"`, and contains **no**
  `dispatchedEvent`. This last negative is the non-vacuity guard: it is what proves the two
  records are mutually exclusive rather than both firing on every resolved interrupt.
- **`"a live runner's interrupt error propagates"` (`:249`, literal `:251`)** — **must gain
  `log: auditLogger()`'s logger.** It resolves successfully and now emits, so with `log`
  omitted the record escapes to `slog.Default()` and pollutes test output unasserted. Keep the
  `errors.Is` propagation assertion and add: the buffer still contains `dispatchedEvent` with
  `"arm":"interrupt"`. That pins D2's "emitted unconditionally, including on error" decision —
  the one design choice a future refactor is most likely to silently reverse.

### Log hygiene (AC3) — assert the key set, not a forbidden-substring list

For the `dispatched` record (the one with the most fields), locate its JSON line, unmarshal it
into a `map[string]any`, and assert the key set is **exactly**
`{time, level, msg, event, arm, conversation_id}`.

A closed key-set assertion cannot go vacuous, where a forbidden-substring list can (a
misspelled needle, or a case mismatch against the haystack, silently passes). It also fails
closed on a future field addition, which is the behaviour AC3 wants on a route whose records
are written next to `peerStatic`-bearing scopes. A ~10-line local line-finder + unmarshal in
this test file is fine; it is not shared infrastructure, and `auditRecords()` is not reusable.
New import: `encoding/json`.

Nothing in `activeInterrupter`'s scope is payload- or screen-bearing (it holds a `convID`
string, two funcs, and a `sessions.Runner`), so AC3's first clause is satisfied structurally;
the key-set assertion is what keeps it satisfied.

### Regression surface

`go test -race ./cmd/pyry/... ./internal/relay/...` must stay green. In particular:

- `internal/relay/v2session_interrupt_test.go`'s interactive-case negative assertion
  (`event=v2.interrupt.non_interactive` absent) — unaffected: its `fakeInterrupter` is not an
  `activeInterrupter`, so no `cmd/pyry` record reaches that buffer.
- `TestResolveBoundRunner` — untouched; it never goes through `interruptRunner`.

## Scope check

| Red line | Limit | This ticket |
|---|---|---|
| New files | ≤ 3 | **0** |
| Total written LOC (prod + tests + doc edits) | ≤ ~600 | **~150** (~40 prod, ~110 test) |
| New exported types/interfaces | ≤ 5 | **0** (`interruptArm` + 3 constants are unexported) |
| Consumer call sites updated simultaneously | ≤ 10 | **6** — `main.go:1351` + `interrupt_routing_test.go:63,73,82,89,92`, confirmed by `codegraph_impact interruptRunner` |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **3** arms |

Production source files modified: **2** — `cmd/pyry/main.go` and
`internal/relay/v2session_modal.go` (doc comment only). Under the ≥5 self-check.

The 6-site fan-out is the number this slice was drawn to stay under: #1190 split because its
combined fan-out was 11 (`interruptRunner`'s 6 call sites plus the 5 `activeInterrupter`
literals). #1192 carried the literals; this carries the call sites. The two halves do not
overlap — this slice edits one `activeInterrupter` literal (`:251`), not five.

**File-overlap check:** `git fetch origin --prune` then a diff of every `origin/feature/<n>`
branch against `origin/main` — no in-flight branch touches `cmd/pyry/main.go`,
`cmd/pyry/interrupt_routing_test.go`, `internal/relay/v2session_modal.go`, or
`internal/relay/v2session_interrupt_test.go`. No `blockedBy` needed.

## Open questions

1. **Should `no_actuator` also carry an `arm` field (`arm: none`) for uniform filtering?**
   Deferred to whoever adds a fourth arm. Today `arm` discriminates between two values on one
   event; `no_actuator` is self-describing and an `arm: none` attr would be redundant with its
   own event name. If a future taxonomy wants `arm` on every record, adding it is additive and
   breaks no existing assertion.
2. **Should a stub exposing *both* `Interrupt()` and `SendEsc()` pin the precedence rule
   (D4)?** It would be the direct test of "Interrupt matched first", which today is proven only
   by the doc comment and by each single-method stub. Not added here — it is a pre-existing
   coverage gap, not one this slice creates, and adding a fourth stub type is scope the ticket
   did not ask for. Worth a follow-up ticket if a runner ever grows both methods.
3. **For the documentation phase (not a developer AC):** the `Inbound interrupt` section of
   `docs/knowledge/features/v2-session-manager.md` records the interim silent-arms gap (added
   by #1192's doc sync, 2a6c55c) and goes stale with this slice — specifically the
   "**Interim gap:** … both stay silent until #1193" paragraph anchored at `:966`.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new boundary, but this slice is the first on the route to touch
  the **actuation dispatcher** (#1192 explicitly stopped short of it). The records sit inside
  an already-authenticated path — the frame reached `handleInterrupt` only after the Noise_IK
  handshake, the token-accept branch (`v2session.go:197-207`), and the inbound
  interactive-capability gate. The one real hazard the new return value creates is *upstream
  misuse*: an `interruptArm` visible to the caller invites a "if `armNone`, try something
  else" fallback, and the only other thing to try is the bootstrap supervisor — the exact
  #678 cross-conversation isolation break. **MUST FIX, fixed in-spec:** § Design D1 states as
  a MUST NOT that nothing may branch on the arm except record selection, and § D4 pins the
  inert default. `resolveBoundRunner` and its `conv.CurrentSessionID == ""` guard
  (`main.go:1283`) are untouched, as is `TestResolveBoundRunner`'s empty-binding case.
- **[Error messages, logs, telemetry]** The load-bearing category. `dispatched` carries
  `{event, arm, conversation_id}` and `no_actuator` carries `{event, conversation_id}` — an
  enumerated constant and an opaque identifier. **Nothing payload-, prompt-, or
  screen-bearing is in scope at either emission point**: `activeInterrupter` holds a `convID`
  string, two injected funcs, and a `sessions.Runner` interface value, and neither record
  dereferences the runner. Unlike #1192's arm 1, there is no `peerStatic`/`device` in scope
  here at all, so the hazard class is structurally absent rather than merely avoided.
  Enforced by the closed key-set assertion in § Testing strategy rather than by developer
  discipline or by a substring list that can go vacuous.
- **[Error messages, logs, telemetry — the deliberate non-inclusion]** `dispatched` omits the
  arm's error even though it is in hand. That is a hygiene decision as well as a
  de-duplication one: the actuation errors are wrapped supervisor/streamsup sentinels
  (`ErrNoLiveSession`, `ErrNoLiveChild`, PTY errors) already emitted once by
  `v2.interrupt.keystroke_err` at `Warn` with a `conn_id`. Logging them a second time under a
  different correlation key widens the surface for no diagnostic gain.
- **[Error messages, logs, telemetry — second-order]** `Info` makes both records visible to
  anyone who can read the daemon log. That audience already sees `conversation_id` at Info
  from the two #1192 records in this same function, and `arm` is a two-valued enumeration
  revealing only which runner backs the active conversation — a fact the same log already
  discloses through the spawn records the 2026-07-24 run captured. No identifier class is
  newly exposed to a new audience.
- **[Network & I/O — log volume as a DoS surface]** Considered and dismissed, with a note
  that the calculus differs from #1192's. These two records fire *later* on the route than
  arm 1: a remote peer must hold an interactive paired device **and** the daemon must have an
  active conversation with a resolvable bound runner. Reaching `dispatched` therefore also
  performs a real actuation (a stdin write or a PTY keystroke), so the record is strictly the
  cheaper half of what the peer already triggers — it cannot be the amplifier. Precedent for
  Info-per-inbound-frame on this manager is established (`handleDequeueMessage`,
  `v2session_modal.go:585`; per-frame `Warn` state rejects at `v2session.go:655/672/679`).
- **[Concurrency]** No new goroutines, locks, or shared mutable state. The added return value
  is a stack value. `interruptRunner` remains a pure function callable from any goroutine, and
  both actuations are already documented goroutine-safe (`streamsup/runner.go:261`;
  `supervisor/modal.go:85-90`, capture-then-release with no lock held across the PTY write) —
  neither is invoked differently here. The `activeInterrupter.log` field is written once at
  wiring (`main.go:1001`) and read-only thereafter, through a nil-normalizing accessor, so no
  literal can panic on a remotely-driven path.
- **[Subprocess execution]** Now in scope for the first time on this route: `interruptRunner`
  is where the supervised child is signalled. The change adds **no** new subprocess
  interaction — no `exec.Command`, no new argv, no new bytes written to any child. The bytes
  each arm writes (`streamsup`'s locally-minted control_request; `supervisor`'s fixed `keyEsc`
  literal) are unchanged and remain non-caller-supplied. The type-switch order and the inert
  default are pinned in § D4 and by `TestInterruptRunner_Dispatch`, so "no actuation beats
  wrong actuation" survives the signature change.
- **[Tokens, secrets, credentials]** N/A by design: no token, key, or credential is read,
  written, derived, compared, or in scope anywhere in this change. Unlike #1192's arm 1, no
  key-shaped value (`peerStatic`) is even reachable from the emission points.
- **[File operations]** N/A — no filesystem access added, no path constructed.
- **[Cryptographic primitives]** N/A — no RNG, comparison, or primitive selection. The one
  identifier minted on this path (`streamsup`'s interrupt `request_id`) is an existing atomic
  counter, deliberately local and non-security-bearing (`runner.go:266-272`), and is neither
  changed nor logged.
- **[Threat model alignment]** The interrupt route's own threat — an interrupt actuating the
  *wrong* child (#678/#1121) — is untouched: no resolution logic, guard, or ordering changes.
  Whether an inert arm *should* have actuated is **OUT OF SCOPE**, owned by **#1191**. The
  correlation gap between the relay-side records (`conn_id`) and the `cmd/pyry`-side records
  (`conversation_id`) remains **OUT OF SCOPE** — closing it needs the `relay.Interrupter` seam
  widened across 8 implementors, which the ticket rules out on fan-out grounds; timestamp
  adjacency is the correlation an operator has.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-25
