# Spec: Record the interrupt route's three signature-stable inert arms (#1192)

**Size:** S (confirmed — see § Scope check). **Security-sensitive** (log hygiene on an
internet-exposed frame route; see § Security review).

Split child A of #1190. Blocks #1193.

## Files to read first

- `internal/relay/v2session_modal.go:434-478` — `handleInterrupt`'s full doc comment + body. Extract: the *order is load-bearing* contract (capability gate first), and the two existing record shapes (`v2.interrupt.inert` at `:467-469` Debug, `v2.interrupt.keystroke_err` at `:473-476` Warn) that the new records must sort with. **Arm 1's emission point is `:463`.**
- `cmd/pyry/main.go:1292-1322` — `activeInterrupter` (struct at `:1299`, `SendEsc` at `:1312`). Extract: the two-field bag-of-injected-seams shape, and the two inert returns at `:1314-1316` (**arm 2**) and `:1317-1320` (**arm 3**).
- `cmd/pyry/main.go:1271-1290` — `resolveBoundRunner`. Extract: its `(sessions.Runner, bool)` return — no session id reaches the caller, which is *why* the records identify the conversation, not the session. **Do not touch the `conv.CurrentSessionID == ""` guard** (#678 isolation enforcement point).
- `cmd/pyry/main.go:984-1012` — the production wiring. Extract: the one production `activeInterrupter{…}` literal at `:996`; `logger` is already in scope there (built at `:743`).
- `cmd/pyry/main.go:735-746` — daemon log level construction. Extract: **default is `slog.LevelInfo`**, Debug only behind `-pyry-verbose` (`:688`). This is what forces the level decision in § Design D2.
- `cmd/pyry/interrupt_routing_test.go:156-221` — `TestActiveInterrupter`, four literals at `:167,189,202,213`. Extract: the two inert subtests (`:187`, `:201`) are the arm-2 / arm-3 vehicles; `SendEsc` is called synchronously on the test goroutine (no polling helper needed).
- `cmd/pyry/modal_resolve_v2_test.go:83-88` — `auditLogger()`. Extract: **JSON**-backed, Debug-level, returns a plain `*bytes.Buffer`. Note `auditRecords()` at `:92` filters on `msg == "audit: remote permission decision"` and is **not** reusable here.
- `internal/relay/v2session_interrupt_test.go:37-90` — `TestV2Session_Interrupt_RoutesEscByCapability`. Extract: the `{interactive → 1 Esc, non-interactive → 0 Esc}` table, `Logger: silentLogger()` at `:70` (the line AC5 swaps), and the **barrier-conn** ordering argument in the doc comment at `:41-43` — that barrier is what makes a negative log assertion sound.
- `internal/relay/v2session_test.go:1613-1655` — `syncLogBuffer`, `bufferLogger()`, `waitForLogContains`. Extract: **Text**-backed (assert `key=value`, not JSON), mutex-guarded because the manager emits on the Run goroutine.
- `internal/relay/v2session.go:187-222` — `V2Session` fields. Extract: `connID` (`:188`), `device` (`:199`), and `peerStatic` (`:210-222`) with its explicit **`MUST NOT appear in any logged field`** SECURITY comment. This is AC3's teeth.
- `cmd/pyry/interactive_turn_v2.go:255-261` — a sibling `cmd/pyry` record. Extract: the house field key is **`"conversation_id"`**, not `conv_id` (also `queue_state_v2.go:93,133,158`; `session_error_v2.go:106,153,175`).
- `cmd/pyry/stream_turn_drain.go:46-60` — `newStreamTurnSink`. Extract: the package-`main` nil-logger normalization idiom (`if logger == nil { logger = slog.Default() }`), which § Design D1 follows.
- `docs/knowledge/codebase/1121.md` — the route's origin: why `activeInterrupter` exists and why `SendEsc` keeps its name.

## Context

On 2026-07-24 a live desktop run (`real-claude-interrupt`, pyrycode-desktop#483) showed
an interactive interrupt that never quiesced the turn. The daemon log recorded two claude
spawns and then 2m10s of nothing.

The route is silent on success and on most failures, so that silence is equally consistent
with four different faults that have four different fixes: the frame never arrived, the
conn was not interactive, the route went silently inert, or the keystroke actuated and
claude ignored it. Removing that ambiguity is the work.

This slice covers the three arms reachable **without changing any function signature**.
The two arms behind `interruptRunner` (successful actuation; runner exposing neither
interrupt method) need a signature change and are #1193's.

**Interim gap the design must preserve as a documented fact:** after this slice an empty
`v2.interrupt.*` log still does **not** prove the frame never arrived — a successful
actuation is also silent until #1193 lands. The route's doc comments must not claim
otherwise, and the tests must not encode a coverage invariant that is false in this
window (see § Testing strategy, "negative assertions name one event").

## Design

Three emission sites, one new struct field, one accessor. No new files, no new exported
types, no control-flow change: every arm returns exactly what it returns today.

### D1 — Getting a logger to `activeInterrupter`

Add a `log *slog.Logger` field plus a nil-normalizing accessor:

```go
type activeInterrupter struct {
	currentConv   func() string
	resolveRunner func(convID string) (sessions.Runner, bool)
	log           *slog.Logger // nil ⇒ slog.Default(), via logger()
}

// logger returns a's logger, falling back to slog.Default() when unset —
// activeInterrupter is a constructor-less bag of injected seams, so nil is a
// reachable state for any literal that does not exercise an inert arm.
func (a activeInterrupter) logger() *slog.Logger
```

Both emission sites call `a.logger()`, never `a.log`.

Why: the struct has no constructor and is built as a **named-field literal** at five sites,
which is exactly what makes `TestActiveInterrupter` readable. The accessor makes the
nil-panic hazard the ticket flags structurally impossible, so a future sixth literal that
omits the field is inert-safe rather than a latent panic on a rarely-hit arm. It follows
package `main`'s existing normalization idiom (`stream_turn_drain.go:53-55`), just moved
from a constructor to the point of use because there is no constructor to put it in.

Ruled out:

- **Constructor `newActiveInterrupter(currentConv, resolveRunner, logger)`** — matches the
  house idiom most literally, but converts five named-field literals into positional calls
  and makes the four test literals *less* legible. The struct's whole value is that a test
  can name the seam it is driving.
- **Mandatory field, no accessor** — five literals must all set it or a rarely-hit arm
  panics at runtime. The ticket itself names this hazard; a five-line accessor removes it.

`*slog.Logger` methods are nil-safe for neither receiver nor handler, so `slog.Default()`
(not a discard handler) is the right fallback: an unwired literal that *does* hit an arm
should still surface the record rather than swallow it.

Wiring at `cmd/pyry/main.go:996` gains one line: `log: logger`.

### D2 — Level: `Info`, not `Debug`

All three records emit at **`Info`**.

The daemon's default level is `slog.LevelInfo` (`main.go:735`); `Debug` requires
`-pyry-verbose`. A `Debug` record leaves the operator exactly where the 2026-07-24 run left
them — reproduce, read the log, see nothing — for an intermittent live failure that may not
reproduce on the re-run. Default-level visibility *is* the deliverable.

Flood bound: all three arms fire only on an inbound `interrupt` frame, a rare
human-initiated event with no loop behind it. One line per interrupt frame is not a spam
vector.

Consistency note: the pre-existing `v2.interrupt.inert` (nil interrupter) stays at `Debug`
and is **not** edited — it reports a foreground/pre-wire *configuration*, not a live-daemon
runtime state, and changing it is outside this ticket. #1193 should emit its two arms at
`Info` too, so the family an operator greps for is uniformly visible.

### D3 — The three records

| # | Arm | File | Event | Fields beyond slog built-ins |
|---|-----|------|-------|------------------------------|
| 1 | non-interactive conn | `internal/relay/v2session_modal.go:463` | `v2.interrupt.non_interactive` | `conn_id` |
| 2 | no active conversation | `cmd/pyry/main.go:1314-1316` | `v2.interrupt.no_active_conv` | *(none)* |
| 3 | binding does not resolve | `cmd/pyry/main.go:1317-1320` | `v2.interrupt.no_bound_runner` | `conversation_id` |

Contracts:

- **Arm 1** carries `conn_id` (`s.connID`) and nothing else — matching the
  `v2.interrupt.inert` record three lines below it in the same function. It emits with the
  whole `*V2Session` in scope; `s.peerStatic` and `s.device` MUST NOT appear (see
  § Security review, and the SECURITY comment at `v2session.go:218-221`).
- **Arm 2** has no conversation id to carry — `convID` is `""` at that point, and emitting
  an empty field is noise. The record's information is its existence: the frame reached
  `SendEsc` and there was no active conversation.
- **Arm 3** carries `conversation_id` — the house key (`interactive_turn_v2.go:260` and
  five other sites), **not** `conv_id`. Widening `resolveBoundRunner` to also surface
  `CurrentSessionID` is explicitly out of scope (it ripples into the injected
  `resolveRunner` seam type and its fakes, for no diagnostic the conversation id doesn't
  already give).

Messages follow the existing shape — a `relay: v2 interrupt …` prefix with a distinct
clause per arm (e.g. `"relay: v2 interrupt inert; conn not interactive"`, mirroring
`"relay: v2 interrupt inert; no interrupter wired"`). `cmd/pyry` uses the same `relay:`
prefix for relay-side concerns (`interactive_turn_v2.go:258`). The `event` field is the
discriminator per AC2; distinct messages are a readability bonus, not the contract.

Each arm's doc comment gains one clause noting it now records. `handleInterrupt`'s
numbered-step comment (`:446-461`) step 1 changes from "is inert (no Esc)" to "is inert
(no Esc) and records `v2.interrupt.non_interactive`".

### Data flow (unchanged control flow, three new taps)

```
inbound interrupt frame
  → V2SessionManager.handleInterrupt(s)
      ├─ !s.interactive ────────────────► [1] Info v2.interrupt.non_interactive {conn_id}  → return
      ├─ Interrupter == nil ────────────► Debug v2.interrupt.inert {conn_id} (unchanged)   → return
      └─ Interrupter.SendEsc()
           → activeInterrupter.SendEsc()
               ├─ convID == "" ─────────► [2] Info v2.interrupt.no_active_conv {}          → nil
               ├─ !resolveRunner(convID) ► [3] Info v2.interrupt.no_bound_runner {conv id}  → nil
               └─ interruptRunner(r) ────► (silent — #1193)
```

## Concurrency model

No new goroutines, no locks, no shutdown sequence.

- **Arm 1** emits on the manager's single Run dispatch goroutine, the same goroutine that
  already reads `s.interactive` lock-free under the package's single-owner invariant. The
  emission reads only `s.connID`, set once at session creation (`v2session.go:639`). No new
  sharing.
- **Arms 2 and 3** emit on whichever goroutine called `SendEsc` — in production that is the
  same Run dispatch goroutine, via `handleInterrupt`. `activeInterrupter` is a value with no
  mutable state; the added field is written once at wiring and read-only thereafter.
- `*slog.Logger` is safe for concurrent use.

The only concurrency consequence is on the **test** side, and it differs per package —
see § Testing strategy.

## Error handling

Emission is the failure path; there is no new failure mode.

- No arm's return value changes. Arm 1 returns; arms 2 and 3 return `nil`. AC5's
  "what each arm actuates is unchanged" is satisfied by construction.
- `a.logger()` cannot return nil, so no arm can panic on an unwired literal.
- `slog` emission errors are swallowed by `slog` itself; nothing to handle.
- Whether an inert arm is *correct* — i.e. whether the route should have actuated — is a
  separate question owned by #1191. This ticket records the arm taken and changes no verdict.

## Testing strategy

Scenarios only; the developer writes them in each package's idiom.

**Handler encoding differs between the two packages and this is the most likely source of
wasted debugging turns:** `internal/relay`'s `bufferLogger()` is a **TextHandler** → assert
`event=v2.interrupt.non_interactive`. `cmd/pyry`'s `auditLogger()` is a **JSONHandler** →
assert `"event":"v2.interrupt.no_active_conv"`.

### `internal/relay/v2session_interrupt_test.go` — `TestV2Session_Interrupt_RoutesEscByCapability`

Edit the existing table test; add no new test function.

- Swap `Logger: silentLogger()` (`:70`) for `bufferLogger()`, keeping the returned buffer.
- Add a per-case expectation for the record (the table already has `wantEsc`; a parallel
  `wantRecord bool` is the natural shape — `true` for the non-interactive case, `false` for
  the interactive one).
- **Non-interactive case:** after the barrier conn opens, the log contains a record with
  `event=v2.interrupt.non_interactive` and `conn_id=c-int`. `waitForLogContains` is the
  right helper (the manager emits on the Run goroutine).
- **Interactive case (the non-vacuity guard):** after the barrier conn opens, the log
  contains **no** `v2.interrupt.non_interactive` record. Without this, an emission placed
  *above* the `!s.interactive` check would still pass the positive assertion. The barrier
  is what makes this negative assertion sound — it proves the interrupt was fully handled
  before the read, so absence is a real absence and not a race. Read `logBuf.String()`
  directly here; you cannot poll for absence.
- **Log-hygiene assertion (AC3):** locate the single matching log line (TextHandler emits
  one line per record) and assert *that line* carries `conn_id=` and does **not** mention
  the peer static key or the device snapshot. Assert on the line, not the whole buffer —
  handshake records elsewhere in the buffer legitimately mention devices. A ~6-line local
  line-finder in this test file is fine; it is not shared infrastructure.
- The two sibling tests (`…_NilInterrupterInert`, `…_SendEscErrorTolerated`) keep
  `silentLogger()` and are untouched — they drive interactive conns, which this slice
  leaves silent.
- New import: `strings`.

### `cmd/pyry/interrupt_routing_test.go` — `TestActiveInterrupter`

`SendEsc` runs synchronously on the test goroutine here, so `auditLogger()`'s plain
`*bytes.Buffer` is race-free and no polling helper is needed. Do **not** reach for
`auditRecords()` — it filters on the audit message and will return zero records; a plain
`strings.Contains` over `buf.String()` is the assertion.

- **`"no active conversation is inert (AC3)"` (literal `:189`):** wire the logger; assert
  the buffer contains `"event":"v2.interrupt.no_active_conv"`, and contains **no**
  `v2.interrupt.no_bound_runner` record. Keep the existing `resolved` short-circuit check.
- **`"unbound/dangling resolution is inert (AC3)"` (literal `:202`):** wire the logger;
  assert the buffer contains `"event":"v2.interrupt.no_bound_runner"` **and**
  `"conversation_id":"A"`, and contains **no** `v2.interrupt.no_active_conv` record.
- **`"interrupt reaches the bound runner…"` (literal `:167`) — arm-exclusivity guard:**
  wire the logger; assert the buffer contains **neither** `v2.interrupt.no_active_conv`
  **nor** `v2.interrupt.no_bound_runner`. This catches an emission placed above the guards.
- **`"a live runner's interrupt error propagates"` (literal `:213`):** leave untouched. It
  resolves successfully, hits no instrumented arm, and its nil `log` is inert by D1.
- New import: `strings` only. `auditLogger()` is package-local and neither `slog` nor
  `bytes` is named at the call site.

**Negative assertions must name the specific event, never the `v2.interrupt.` prefix.**
#1193 adds a record to the success path; a prefix-wide absence assertion here would turn
green today and red the moment #1193 lands. This is the cross-ticket trap in this slice.

### Regression surface

`go test -race ./cmd/pyry/... ./internal/relay/...` must stay green. `TestInterruptRunner_Dispatch`
and `TestResolveBoundRunner` are untouched — neither goes through `activeInterrupter`.

## Scope check

| Red line | Limit | This ticket |
|---|---|---|
| New files | ≤ 3 | **0** |
| Total written LOC (prod + tests + spec edits) | ≤ ~600 | **~130** (~40 prod, ~90 test) |
| New exported types/interfaces | ≤ 5 | **0** |
| Consumer call sites updated simultaneously | ≤ 10 | **6** (5 `activeInterrupter{` literals — verified tree-wide — of which 4 are actually edited, plus 1 relay arm) |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **3** |

Production source files modified: **2** (`internal/relay/v2session_modal.go`,
`cmd/pyry/main.go`) — under the ≥5 self-check.

The 6-site fan-out is the number this slice was drawn to stay under: #1190 split because
its combined fan-out was 11 (`interruptRunner`'s 6 call sites plus the 5 literals). Child B
(#1193) carries the `interruptRunner` half.

**File-overlap check:** `git fetch origin --prune` then a diff of every
`origin/feature/<n>` branch against `origin/main` — no in-flight branch touches
`internal/relay/v2session_modal.go`, `internal/relay/v2session_interrupt_test.go`,
`cmd/pyry/main.go`, or `cmd/pyry/interrupt_routing_test.go`. No `blockedBy` needed.

## Open questions

1. **Does the desktop e2e harness run the daemon at Info or Debug?** `PYRY_E2E_DAEMON_LOG`
   is a pyrycode-desktop harness variable and does not exist in this repo, so it can't be
   answered here. D2 chooses `Info` precisely so the answer does not matter — the records
   are visible at the daemon's default level either way. If the harness turns out to pass
   `-pyry-verbose`, nothing changes.
2. **Should arm 2 carry a `conn_id`?** It cannot: `activeInterrupter` sits behind the
   `relay.Interrupter` seam and sees no connection. Correlating arm 1's `conn_id` with
   arms 2/3 requires widening the seam, which § "Designs already ruled out" in the ticket
   rejects on fan-out grounds (~8 implementors). Deferred; timestamp adjacency is the
   correlation an operator has today.
3. **Does #1193 rename any of these three events?** It should not — AC2's contract is that
   the family sorts together. If #1193's architect wants a shared `v2.interrupt.<outcome>`
   taxonomy, these three names are the fixed points it must build around.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new boundary. All three arms sit *inside* an already-authenticated
  path: the frame reached `handleInterrupt` only after the Noise_IK handshake and the
  token-accept branch (`v2session.go:197-207`). Arm 1 is itself an authorization decision —
  the inbound interactive-capability gate — and this change records that decision without
  moving, weakening, or reordering it. **MUST NOT** change the `conv.CurrentSessionID == ""`
  guard in `resolveBoundRunner` (`main.go:1282`); it is the #678 cross-conversation isolation
  enforcement point (`Pool.Lookup("")` returns the *bootstrap* session). The spec touches
  neither that function nor the order of any check.
- **[Error messages, logs, telemetry]** The load-bearing category, and the one real hazard.
  Arm 1 emits from `internal/relay` with the entire `*V2Session` in scope, which holds
  `peerStatic` — documented at `v2session.go:218-221` as identity-bearing and **MUST NOT
  appear in any logged field** under the package's no-key-in-logs discipline — and `device`,
  the matched device snapshot. The record's field set is therefore pinned to exactly
  `{event, conn_id}` (§ D3), `conn_id` being what the sibling `v2.interrupt.inert` record in
  the same function already uses. Arms 2 and 3 carry an event and, for arm 3, a conversation
  id — both enumerated/opaque identifiers. **No arm has conversation text, prompt bytes, or
  rendered screen content in scope at its emission point**, so AC3's first clause is
  satisfied structurally rather than by developer discipline. Enforced by the per-line
  hygiene assertion in § Testing strategy, not left to review.
- **[Error messages, logs, telemetry — second-order]** Raising these to `Info` (D2) makes
  them visible to anyone who can read the daemon log. That audience already sees `conn_id`
  (existing `v2.interrupt.inert`, `v2.queue.reconcile.push_err`) and `conversation_id`
  (`interactive_turn_v2.go:260`, `session_error_v2.go:106`) at Warn/Debug. No identifier class
  is newly exposed to a new audience.
- **[Network & I/O — log volume as a DoS surface]** Considered and dismissed. Arm 1 is the
  only arm a remote peer can drive directly, by sending `interrupt` frames on a
  non-interactive conn. One `Info` line per inbound frame is bounded by the inbound frame
  rate the manager already accepts and already logs against elsewhere; the record adds no
  allocation-heavy formatting (two string attrs) and no unbounded field. It is not a
  log-amplification primitive.
- **[Tokens, secrets, credentials]** N/A by design: no token, key, or credential is read,
  written, derived, or compared anywhere in this change. The one key-shaped value in scope
  (`peerStatic`) is covered above as a log-hygiene finding, not a secret-handling one.
- **[File operations]** N/A — no filesystem access added.
- **[Subprocess execution]** N/A — no `exec.Command`. The change explicitly does **not**
  reach the actuation (`interruptRunner`), which is where the child process is signalled;
  that is #1193's surface.
- **[Cryptographic primitives]** N/A — no RNG, no comparison, no primitive selection.
- **[Concurrency]** No new goroutines, locks, or shared mutable state. Arm 1 emits on the
  manager's single Run dispatch goroutine under the existing single-owner invariant and
  reads only write-once `s.connID`. The added `activeInterrupter.log` field is written once
  at wiring (`main.go:996`) and read-only thereafter; the value is copied, not shared. The
  nil-normalizing accessor (D1) removes the one crash path a mandatory field would have
  introduced — a nil `*slog.Logger` on an unwired literal panics on first use, which for a
  rarely-hit inert arm would be a latent remote-triggerable panic in the *relay frame path*.
  That is the specific reason D1 is not merely stylistic.
- **[Threat model alignment]** The interrupt route's own threat — an interrupt actuating the
  *wrong* child (the #678/#1121 cross-conversation isolation break) — is untouched: no
  resolution logic, guard, or ordering changes, and the tests that pin it
  (`TestResolveBoundRunner`'s empty-`CurrentSessionID` case) are untouched. Whether an inert
  arm *should* have actuated is **OUT OF SCOPE**, owned by **#1191**. The two remaining
  silent arms are **OUT OF SCOPE**, owned by **#1193**; § Context requires the interim gap
  be stated rather than papered over, so an operator cannot read absence-of-record as
  proof-of-non-arrival.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-25
