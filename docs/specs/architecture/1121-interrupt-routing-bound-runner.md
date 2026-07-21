# Spec #1121 — Per-conversation interrupt routing (bound runner, not bootstrap supervisor)

**Size:** S · **Security-sensitive:** yes (cross-conversation isolation) · **Blocked-by:** #1120 (merged)

## Files to read first

- `internal/relay/v2session_modal.go:462-478` — `handleInterrupt`: the seam being redirected. Extract its **contract to preserve**: `s.interactive` gate → nil-`Interrupter` guard → best-effort `SendEsc()` (error Warn-logged, tolerated, no reply, no payload/screen logged). This handler does **not** change.
- `internal/relay/v2session_seams.go:34-42` and `:320-324` — the `Interrupter interface{ SendEsc() error }` and its `V2SessionConfig` field. **Unchanged.** Note the interface doc already abstracts `SendEsc` as "claude's own interrupt" (not literally an Esc) — this is why we keep the method name.
- `cmd/pyry/relay.go:535-543` — the `Interrupter: w.sup` wiring line (the bug). This is the one line whose value changes.
- `cmd/pyry/relay.go:176-185` — `relayWiring` struct fields (`active`, `boundHost`, `sup`): where the new adapter field is declared.
- `cmd/pyry/main.go:786-812` — the `boundHost` closure: the **resolution shape to mirror** (`convReg.Get → CurrentSessionID guard → pool.Lookup → sess.<accessor>`). Note it returns `sess.Supervisor()`, which is **nil for a streamsup runner** — that is why interrupt cannot reuse `boundHost` verbatim.
- `cmd/pyry/main.go:1081-1089` and `:1106-1129` — `errNoBoundSession` + `sessionRouter.resolve`: the **LOAD-BEARING empty-`CurrentSessionID` guard**. The comment states it plainly: `Pool.Lookup("")` returns the **bootstrap** session, so an unbound conversation without this guard routes into shared bootstrap claude — the exact isolation break #678 forbids and this ticket must not reintroduce. The new resolver copies this guard.
- `cmd/pyry/main.go:923-940` — the `relayWiring{...}` literal: where the new adapter is constructed and passed.
- `internal/sessions/session.go:234-248` — `Session.Supervisor()`: the accessor the new `Session.Runner()` parallels (but type-preserving — returns the interface, not the concrete).
- `internal/sessions/runner.go:9-46` — the `Runner` interface + doc. **Do not widen it.** Its doc explicitly declares interrupt-style access "speculative surface" (#1077). The interrupt is reached off-interface.
- `cmd/pyry/streamsup_runner.go:26-38` and `:147-150` — the `streamRunner` adapter (wraps `*streamsup.Runner`, lives in `cmd/pyry`) and its `sessions.Runner` assertion. The new `streamRunner.Interrupt()` method lands here.
- `internal/streamsup/runner.go:235-249` — `(*streamsup.Runner).Interrupt()` (#1120, the primitive). Its doc names this ticket: reach it "via its own narrow interface or a type assertion." Returns `ErrNoLiveChild` (retryable) when no child is live — never panics.
- `internal/supervisor/modal.go:69` — `(*supervisor.Supervisor).SendEsc()`: the PTY leaf actuator. Unchanged.
- `internal/sessions/runner_test.go:16-32` — the `fakeRunner` shape (5 `Runner` methods, all trivial). The cmd/pyry AC2 fake extends this shape + one `Interrupt()`/`SendEsc()` method.

## Context

The relay's inbound `interrupt` control frame (`internal/relay` `handleInterrupt`) is wired to the **bootstrap** supervisor: `cmd/pyry/relay.go` sets `Interrupter: w.sup`, where `w.sup = bootstrap.Supervisor()`. But turns are routed to **per-conversation bound runners** (#678). So an interrupt actuates against the idle bootstrap child, not the runner executing the active conversation's turn — a latent mis-routing bug and a cross-conversation isolation gap.

The runner-level primitive this routing consumes landed in #1120 (`(*streamsup.Runner).Interrupt()`) and #726 (`(*supervisor.Supervisor).SendEsc()`). This ticket is the **consumer**: it threads the active conversation into interrupt handling so the frame reaches the runner **bound to the active conversation**, resolved exactly like the follow-active turn/modal streams (`active → CurrentSessionID → Pool.Lookup → runner`).

## The seam decision (the ticket's central question)

The two runner types interrupt through **differently-named methods**:

| Runner (as stored in `Session.sup`) | Interrupt method | Home package |
|---|---|---|
| `*supervisor.Supervisor` (PTY, the nil-`RunnerFactory` default) | `SendEsc() error` | `internal/supervisor` |
| `streamRunner{ *streamsup.Runner }` (stream-json, injected by `newStreamRunnerFactory`) | `(*streamsup.Runner).Interrupt() error` | `internal/streamsup`, **adapted in `cmd/pyry`** |

The decisive fact: **`streamRunner` — the streamsup→`sessions.Runner` adapter — lives in `cmd/pyry`.** `internal/sessions` cannot see it (that would be an import of `main`), so the SendEsc-vs-Interrupt dispatch **cannot** live in `internal/sessions`. `cmd/pyry` is the only package that sees both concrete runner types. This forces the seam:

**Reach the runner concretely and dispatch by type in `cmd/pyry`** — the "reach the runner concretely (as the turn/modal wiring reaches `sess.Supervisor()`)" option the ticket names. Rejected alternatives:

- **Widen `sessions.Runner` with an interrupt method** — forbidden by the ticket and by `runner.go`'s doc ("speculative surface", #1077). Also impossible to satisfy uniformly: the two concretes have different method names, and `internal/sessions` can't reach `streamRunner`'s.
- **`Session.Interrupt()` inside `internal/sessions`** — could only dispatch the `*supervisor.Supervisor` arm; it cannot reach `streamRunner.Interrupt()`. Dead end for the stream-json path.
- **Rename the relay seam `Interrupter.SendEsc()` → `.Interrupt()`** — more honest, but adds `internal/relay/v2session_seams.go` + `v2session_modal.go` + the `fakeInterrupter` test fixture to the change (6 production files, tripping the ≥5-file scope gate) and rewrites the existing interrupt regression tests. **Deferred.** The relay `Interrupter` interface doc already abstracts `SendEsc` as "claude's own interrupt," so keeping the name holds the change to 4 production files and leaves the whole `internal/relay` package — including its interrupt tests — untouched as a regression guard. The dishonesty is contained to one method label; every leaf actuator and the `cmd/pyry` dispatch helper keep honest names.

Nothing in `internal/relay` changes. The fix is entirely in `cmd/pyry` (+ one `internal/sessions` accessor).

## Design

Four production files; no new files.

### 1. `internal/sessions/session.go` — expose the runner

Add an accessor parallel to `Supervisor()`, but **type-preserving**:

```
// Runner exposes the underlying runner as the sessions.Runner interface, so a
// consumer can reach runner-type-specific methods (SendEsc / Interrupt) that are
// deliberately NOT on the narrow Runner interface (#1077). Unlike Supervisor(),
// which returns the concrete *supervisor.Supervisor (nil for a stream-json
// runner), Runner() returns whatever backs sess.sup — total for both runner types.
func (s *Session) Runner() sessions.Runner  // returns s.sup
```

Behavior: returns `s.sup` unchanged. No lock (the `sup` field is set once at construction, same discipline as `Supervisor()`). One-line body.

### 2. `cmd/pyry/streamsup_runner.go` — carry interrupt through the adapter

Add one method to `streamRunner` (mirrors the five existing forwarders):

```
func (a streamRunner) Interrupt() error  // return a.r.Interrupt()
```

This is off the `sessions.Runner` interface (interface stays un-widened) — a concrete method the dispatch reaches by assertion, exactly as `(*supervisor.Supervisor).SendEsc()` is reached.

### 3. `cmd/pyry/main.go` — dispatch, resolution, and the adapter

Three unexported helpers, placed beside `boundHost` / `sessionRouter.resolve`:

**a. Type-dispatch (actuation).**
```
func interruptRunner(r sessions.Runner) error
```
Type-switch, order load-bearing:
- `case interface{ Interrupt() error }:` → `Interrupt()` — matches `streamRunner` and any test fake.
- `case interface{ SendEsc() error }:` → `SendEsc()` — matches `*supervisor.Supervisor`.
- `default:` → `return nil` — an unknown runner is **inert** (fail-safe: no actuation beats wrong actuation).

`*supervisor.Supervisor` has no `Interrupt` method, so it can never match the first case; `streamRunner` has no `SendEsc`, so it can never match the second. The switch is unambiguous.

**b. Resolution (the security-critical guard).**
```
func resolveBoundRunner(convReg *conversations.Registry, pool *sessions.Pool, convID string) (sessions.Runner, bool)
```
Mirrors `boundHost`'s lookup shape and `sessionRouter.resolve`'s guard:
- `convReg.Get(convID)` miss → `(nil, false)`.
- **`conv.CurrentSessionID == ""` → `(nil, false)`** — the LOAD-BEARING guard. This MUST match `sessionRouter.resolve`'s guard (`main.go:1120`): without it, `Pool.Lookup("")` returns the bootstrap session and the interrupt lands on shared bootstrap claude — the isolation break. Never fall through to bootstrap.
- `pool.Lookup(SessionID(conv.CurrentSessionID))` error → `(nil, false)` (dangling id).
- hit → `(sess.Runner(), true)`.

**c. The adapter (satisfies `relay.Interrupter`).**
```
type activeInterrupter struct {
    currentConv   func() string                              // active.CurrentConversation
    resolveRunner func(convID string) (sessions.Runner, bool) // resolveBoundRunner bound to convReg+pool
}
func (a activeInterrupter) SendEsc() error
```
`SendEsc()` (the relay seam's method name — see the seam decision) means "interrupt the active conversation's bound runner":
1. `convID := a.currentConv()`; `convID == ""` → `return nil` (no active conversation — inert).
2. `r, ok := a.resolveRunner(convID)`; `!ok` → `return nil` (unbound / dangling — inert).
3. `return interruptRunner(r)` (best-effort; a runner's no-live-child error propagates and the relay handler Warn-logs+tolerates it).

The struct takes two injected seams (not raw `*Pool`/`*Registry`) purely so the AC2 test can drive it with fakes; production wires `currentConv: active.CurrentConversation` and `resolveRunner: func(id) { return resolveBoundRunner(convReg, pool, id) }`.

### 4. `cmd/pyry/relay.go` — declare the field, flip the wiring

- Add field to the `relayWiring` struct (beside `boundHost`/`sup`): `activeInterrupter relay.Interrupter`.
- Change the one wiring line: `Interrupter: w.sup` → `Interrupter: w.activeInterrupter`.

Construct the `activeInterrupter` in `main.go`'s `relayWiring{...}` literal (where `active`, `convReg`, `pool` are all in scope, beside `boundHost`).

### Data flow

```
inbound interrupt frame
  → V2SessionManager.handleInterrupt(s)          [UNCHANGED: s.interactive gate, nil guard, best-effort]
      → m.cfg.Interrupter.SendEsc()              [now the cmd/pyry adapter, not w.sup]
          → active.CurrentConversation()         ── "" → inert
          → resolveBoundRunner(convReg,pool,id)  ── miss / empty CurrentSessionID / dangling → inert (never bootstrap)
              → sess.Runner()
          → interruptRunner(runner)
              → *supervisor.Supervisor → SendEsc()   (PTY Esc)
              → streamRunner           → Interrupt()  (streamsup control_request, #1120)
              → unknown                → nil (inert)
```

## Concurrency model

No new goroutines, no new locks. `handleInterrupt` runs on the manager's single `Run` dispatch goroutine (unchanged). The adapter reads `active.CurrentConversation()` (mutex-guarded), `convReg.Get` / `pool.Lookup` (both concurrency-safe), and calls `SendEsc()` / `Interrupt()` (both documented "safe from any goroutine"). No lock is held across the actuation. No lock-order concern (`Runner.Interrupt`/`SendEsc` touch only runner-internal state, never `Pool.mu`/`Session.lcMu`).

## Error handling / failure modes

| Condition | Behavior | Why safe |
|---|---|---|
| No active conversation (`currentConv()==""`) | inert, `nil` | AC3 — no panic, no actuation. |
| Active conv unknown, or `CurrentSessionID==""` (unbound) | inert, `nil` | AC3 + isolation — the load-bearing guard; never routes to bootstrap. |
| Bound session id dangling (`Pool.Lookup` error) | inert, `nil` | AC3 — binding changed/evicted between stamp and interrupt. |
| Runner live, no child (`ErrNoLiveSession`/`ErrNoLiveChild`) | error propagates | `handleInterrupt` Warn-logs + tolerates (best-effort, existing contract). |
| Unknown runner type (`default` branch) | inert, `nil` | fail-safe — no actuation beats wrong actuation. |
| `Interrupter` nil (foreground / v1) | handler skips | UNCHANGED relay nil-seam guard. |

No frame carries payload; **no interrupt path logs any conversation content or screen bytes** — logs (all in the unchanged relay handler) carry only `conn_id` / event names.

## Testing strategy

Three focused unit targets (all `cmd/pyry`, stdlib `testing`, table-driven). No real claude child needed — actuation is observed on fakes.

- **`interruptRunner` dispatch** (`cmd/pyry`):
  - fake runner exposing `Interrupt() error` → dispatched to `Interrupt`, recorded once.
  - a value exposing only `SendEsc() error` (e.g. a `*supervisor.Supervisor`-shaped fake) → dispatched to `SendEsc`.
  - fake runner exposing neither → inert (no call, `nil` return).
  - error from the chosen method propagates unchanged.
- **`resolveBoundRunner` guard** (`cmd/pyry`, real `*conversations.Registry` + real/minimal `*sessions.Pool`):
  - unknown conversation → `(nil, false)`.
  - conversation with **empty `CurrentSessionID`** → `(nil, false)` — the isolation guard; assert it does **not** return the bootstrap runner (the fails-before/passes-after core).
  - bound conversation → returns that session's `Runner()`, not the bootstrap's.
- **`activeInterrupter` composition = AC2** (`cmd/pyry`, injected seams):
  - `currentConv` → "A"; `resolveRunner("A")` → a fake bound runner that records interrupt; a separate "bootstrap" fake is wired nowhere. Invoke `SendEsc()`; assert the **bound** fake recorded exactly one interrupt and the bootstrap fake recorded **zero**. This is the AC2 "reaches the bound runner, not the bootstrap supervisor" proof.
  - `currentConv` → "" → inert (AC3).
  - `resolveRunner` → `(nil, false)` → inert (AC3).

Fake runner shape: extend `internal/sessions/runner_test.go`'s `fakeRunner` (5 trivial `Runner` methods) with an `Interrupt() error` (or `SendEsc() error`) counter, in package `main`.

## Open questions

- **Isolation boundary is "active conversation," not "sending conn's conversation."** AC1 and the follow-active turn/modal streams both key off the daemon-global `active` holder (single focused conversation at a time). Routing conn A's interrupt strictly to A's conversation regardless of the global active is a **different, larger** design (per-conn interrupt isolation) not asked for here and divergent from the single-active model the turn/modal streams already use. This spec routes to the active conversation's bound runner — the AC-faithful choice. Flagged for the security review below.
- **Streamsup bound sessions are not yet live in production** (`RunnerFactory` selection is #1081, unmerged). Today every bound session is `*supervisor.Supervisor`, so the `SendEsc` arm is the only one exercised in production. Handling the `Interrupt` arm now is **not speculative**: the ticket's technical notes explicitly require designing for both runner types, and #1081 flipping streamsup on without this arm would silently no-op streamsup interrupts (the `default` branch). The `interruptRunner` unit test exercises both arms with fakes regardless.

## Security review

Ran per the `security-sensitive` label (cross-conversation isolation). Categories walked:

- **Trust boundary — interrupt target.** The whole fix is an isolation correction: the interrupt must never actuate on the **bootstrap** child. The single enforcement point is `resolveBoundRunner`'s `conv.CurrentSessionID == "" → (nil, false)` guard, because `Pool.Lookup("")` returns the bootstrap session (`main.go:1086-1088`, `errNoBoundSession`). This guard is copied verbatim in intent from the already-tested `sessionRouter.resolve` (#678 isolation) — deterministic code, not a stochastic rule (belt-and-suspenders = different fabric). **Enforced at `cmd/pyry/main.go` `resolveBoundRunner`; asserted by the `resolveBoundRunner` empty-`CurrentSessionID` test.** Verdict: covered.
- **Fail-safe on ambiguity.** Every non-resolvable state (no active conv, unbound, dangling, unknown runner type) returns `nil` **without actuating** — never a fallback to bootstrap or any other child. No actuation beats wrong actuation. Verdict: covered.
- **Capability gate preserved.** The relay `handleInterrupt` `s.interactive` gate (non-interactive conns cannot interrupt) is UNCHANGED — this ticket does not touch the relay handler, so the existing inbound capability gate and its tests still hold. Verdict: unchanged, covered.
- **Content confidentiality.** The interrupt frame carries no payload; the design adds no logging. The only logs on this path are the unchanged relay handler's `conn_id`/event-name Warn/Debug lines — no conversation content, no screen bytes, no session id echoed from untrusted input. Verdict: no new surface.
- **No new authorization decision in the primitive.** `interruptRunner` routes whatever runner it is given; authorization (interactive capability) stays in the relay handler. The runner-selection authority is the `active` holder + the binding registry — same authorities the turn/modal streams already trust. No trust is widened. Verdict: covered.
- **Residual (flagged, not a finding).** Per-conn interrupt isolation (A's interrupt provably never affects B when B is the global active) is out of scope — the model is single-active by design (see Open questions). Not an isolation regression: today's code routes to bootstrap regardless of conn; this ticket strictly improves isolation by routing to a real bound runner. No new cross-conversation leak is introduced.

**Verdict: PASS.** The fix reduces the isolation surface (bootstrap mis-routing removed), adds one deterministic enforcement guard mirroring a tested one, introduces no new logging/authorization/content surface, and fails safe on every ambiguous state.
