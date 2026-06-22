# Spec #726 — Safe-answer seam: abstract modal choice → tui-driver keystroke

**Size:** XS (architect downgrade from S — see *Sizing* below).
**Security-sensitive:** No. Mechanical keystroke actuator carrying no trust decision; loosens no control, accepts no untrusted input directly. The gate lives in the consumer (#717).

## Files to read first

- `internal/supervisor/supervisor.go:237-259` — `WriteUserTurn`: the **exact** capture-then-release discipline to mirror (lock `sessMu` → copy `s.sess` → unlock → act on the captured pointer; nil → `ErrNoLiveSession`; wrap with a stable `supervisor: …:` prefix preserving the underlying error for `errors.Is`).
- `internal/supervisor/supervisor.go:172-181` — the `deliverFn` struct field: the production-vs-test **injection seam** this ticket copies one-for-one (`keystrokeFn`).
- `internal/supervisor/supervisor.go:46-57` — `ErrNoLiveSession` / `ErrTurnNotCommitted` sentinels. Reuse `ErrNoLiveSession`; do **not** add a new sentinel.
- `internal/supervisor/supervisor.go:522-549` — `New`: where `s.deliverFn = s.deliverViaSession` is wired. Add `s.keystrokeFn = sendModalKeystroke` immediately after.
- `internal/supervisor/supervisor.go:448-498` — `Session()` / `setSession()`: the `sessMu`/`sess` ownership and the documented teardown-race contract (a captured pointer racing `setSession(nil)+Close` lands in tui-driver's PTY-error path, never a panic).
- `pkg/tuidriver/keys.go:40-60` (module `github.com/pyrycode/tui-driver@v1.3.0`) — `Session.AcceptTrust()` (`"1\r"`), `Session.Answer(choice)` (`choice + "\r"`), `Session.SendEsc()` (`0x1b`). These are the three calls to delegate to. All funnel through the unexported `writeRaw` → `pty.Write`; on a closed PTY they return a non-nil error, **no panic** (see `keys.go:77-94` `AttachInput` doc).
- `internal/supervisor/supervisor_test.go:748-815` — the `deliverFn`-seam test template: `New(helperConfig("exit0"))` → `sup.setSession(&tuidriver.Session{})` → override the fn field → call the method → assert. The modal tests follow this shape exactly. Note `setSession(&tuidriver.Session{})` registers a **zero-value** Session (nil PTY) — calling a real tui-driver keystroke method on it would nil-deref, which is *why* the injection seam exists.

## Context

Phase 3 (epic #597) foundation primitive for the daemon-side modal bridge. tui-driver v1.3.0 (vendored via #619) ships the safe-answer keystroke primitives, but no pyrycode code calls them. This ticket adds the supervisor-side wrapper that captures the live `Session` and drives those keystrokes, so higher layers can resolve a modal without importing tui-driver or reaching into the child PTY.

The primitive carries **no trust decision** — it routes whatever keystroke it is told to. Authorization (per-device gate, nonce validation, idempotency, `option_id` → choice mapping) lives entirely in the consumers:
- #717 (gated `modal_answer`) → `Answer` / `AcceptTrust`;
- #727 (interception/cancel) → `SendEsc`;
- #725 (deny-on-timeout) → the deny keystroke (`Answer` or `SendEsc`).

#717's body already names the three verbs it consumes as **`Answer` / `AcceptTrust` / `SendEsc`** — this spec uses exactly those names (one level up from tui-driver) so no consumer-side rename is needed.

## Design

One new file, two small edits to `supervisor.go`. Purely additive — no existing call site changes.

### Package structure

```
internal/supervisor/
  supervisor.go   (modified) + keystrokeFn struct field; + one wiring line in New
  modal.go        (new)      modalKey enum, sendModalKeystroke (production seam),
                             AcceptTrust/Answer/SendEsc methods, sendModalKey helper
  modal_test.go   (new)      table-driven dispatch + no-session + error-wrap tests
```

### Exported contract (`modal.go`)

Three thin methods on `*Supervisor`, each delegating through one shared helper. **No `context.Context`** — unlike `WriteUserTurn` (which blocks on `WaitReady`+`DeliverPrompt` for seconds), a keystroke is a single non-blocking `pty.Write` with nothing to cancel. Adding a ctx would advertise a cancellation contract the method cannot honor. Document this divergence so it is not cargo-culted from `WriteUserTurn`.

```go
func (s *Supervisor) AcceptTrust() error          // → Session.AcceptTrust()
func (s *Supervisor) Answer(choice string) error  // → Session.Answer(choice)
func (s *Supervisor) SendEsc() error              // → Session.SendEsc()
```

Each is a one-liner: `return s.sendModalKey(keyXxx, choice)`. Behavior contract (the developer writes the bodies):

- **Capture-then-release** (AC-2): `sendModalKey` locks `sessMu`, copies `s.sess`, unlocks, then actuates on the captured pointer. No lock held across the PTY write. Identical to `WriteUserTurn:248-257`.
- **No live session** (AC-3): captured `sess == nil` → return `ErrNoLiveSession` wrapped with a per-verb `supervisor: <verb>:` prefix, and **never invoke `keystrokeFn`** (writes nothing).
- **Keystroke error** → wrap with the same prefix, underlying error preserved for `errors.Is`.

### Internal seam (`modal.go`)

```go
type modalKey int                 // unexported — NOT a new exported type
const ( keyAcceptTrust modalKey = iota; keyAnswer; keyEsc )
func (k modalKey) String() string // "accept trust" / "answer" / "send esc" — feeds the error prefix

// sendModalKeystroke is the production keystrokeFn: routes one abstract key to
// the matching tui-driver call. switch over modalKey → AcceptTrust/Answer/SendEsc;
// default → fmt.Errorf unreachable-programmer-bug error.
func sendModalKeystroke(sess *tuidriver.Session, k modalKey, choice string) error
```

`sendModalKey` (helper) signature: `func (s *Supervisor) sendModalKey(k modalKey, choice string) error` — the capture-then-release block above, calling `s.keystrokeFn(sess, k, choice)`.

### Injection seam (`supervisor.go`)

Mirror `deliverFn` exactly. Add to the `Supervisor` struct (after the `deliverFn` field, ~line 181):

```go
// keystrokeFn actuates one abstract modal keystroke against a captured live
// Session. Set once in New to sendModalKeystroke; overridden only in tests —
// the same unexported-injection seam as deliverFn — because the real
// tui-driver calls nil-deref the PTY on a zero-value Session, so verb dispatch
// cannot otherwise be unit-tested without a live claude.
keystrokeFn func(sess *tuidriver.Session, k modalKey, choice string) error
```

In `New` (after `s.deliverFn = s.deliverViaSession`, line 547): `s.keystrokeFn = sendModalKeystroke`.

### What this ticket deliberately does NOT do

- No `currentConvID` / cursor stamping (these are not user turns; they carry no `conversation_id`). Do not touch `convMu`.
- No `option_id` → abstract-choice mapping (lives in #717).
- No relay, registry, gate, audit, broadcast, logging-policy, or `WaitReady` gate.
- No new error sentinel (reuse `ErrNoLiveSession`).
- No new exported type (`modalKey` stays unexported).

## Concurrency model

No new goroutines, no new mutex. The three methods run on arbitrary consumer-handler goroutines (same as `WriteUserTurn`) and serialize on the existing `sessMu` only for the pointer copy. The capture-then-release means the PTY write happens lock-free on the captured pointer.

**Teardown race** is already handled by tui-driver and matches `WriteUserTurn`'s documented contract: a captured `sess` pointer racing a concurrent `setSession(nil)+Close` writes into tui-driver's teardown-safe PTY-error path (`writeRaw` → `pty.Write` on a closed `*os.File` returns a non-nil error, never panics — see `keys.go` `AttachInput` doc). The torn-down session therefore surfaces here as a loud wrapped error, never a crash or a false success. No additional synchronization needed.

## Error handling

| Condition | Result |
|---|---|
| No live session (`sess == nil`) | `fmt.Errorf("supervisor: %s: %w", k, ErrNoLiveSession)`; `keystrokeFn` not called |
| tui-driver write error (e.g. closed PTY) | `fmt.Errorf("supervisor: %s: %w", k, err)`; underlying preserved for `errors.Is` |
| Unknown `modalKey` (programmer bug, unreachable from exported API) | `sendModalKeystroke` returns an `fmt.Errorf` describing the bad key — return, do not panic (CODING-STYLE: panic is for unreachable code only; this is defensively returned) |
| Happy path | `nil` |

## Testing strategy

New file `internal/supervisor/modal_test.go`, in-package (`package supervisor`), table-driven, `t.Parallel()`. Build via `New(helperConfig("exit0"))` and the `setSession(&tuidriver.Session{})` + fn-override pattern from `supervisor_test.go:748-815`. Scenarios (developer writes the bodies in the project idiom):

- **Verb dispatch (AC-1, AC-4 "correct call fires").** For each of `AcceptTrust()` / `Answer("2")` / `SendEsc()`: register a live session, override `keystrokeFn` to record the `(modalKey, choice)` it receives and return nil, call the method, assert the recorded key/choice match (`keyAcceptTrust`/`""`, `keyAnswer`/`"2"`, `keyEsc`/`""`) and the method returned nil. This proves the supervisor dispatches the right abstract verb to the seam.
- **No live session (AC-3, AC-4 "sends nothing").** For each verb: do **not** register a session, override `keystrokeFn` to flip a "called" flag, call the method, assert `errors.Is(err, ErrNoLiveSession)`, the wrap prefix is present, and the flag stayed false (nothing written).
- **Keystroke error wrap.** Register a session, `keystrokeFn` returns a sentinel `boom`, assert `errors.Is(err, boom)` and the prefix — mirrors `TestSupervisor_WriteUserTurn_DeliverErrorFailsLoud`.
- **Unknown-key guard (optional).** `sendModalKeystroke(&tuidriver.Session{}, modalKey(99), "")` returns the unreachable-key error without panicking.

**Testability boundary (state explicitly, do not try to close it):** the `modalKey → real tui-driver call` switch inside `sendModalKeystroke` cannot be unit-tested against a zero-value `&tuidriver.Session{}` (nil PTY would deref). The dispatch test proves the correct `modalKey` reaches the seam; the one-line switch to the matching tui-driver method, plus tui-driver's own `keys_test.go` byte assertions, cover the rest. This is the identical boundary as `deliverViaSession` (whose real-claude path is only exercised by the heavier real-spawn test). A real-spawn integration test is **not** required by the ACs; do not add one.

## Sizing

Architect downgrade **S → XS**. Tally against the red lines:

| Red line | This ticket |
|---|---|
| New files | 1 production (`modal.go`) + 1 test (`modal_test.go`) — well under 3 |
| Total written LOC | ~60 prod + ~120 test ≈ ~180 — far under 600 |
| New exported types/interfaces | 0 (`modalKey` unexported; 3 methods, no types) |
| Consumer call sites updated simultaneously | 0 — purely additive |
| Acceptance criteria | 4 |
| Error/reject branches | 1 (nil session) + a 3-case verb switch — not a state machine |

§4 production-file self-check: 2 production files (`modal.go` new, `supervisor.go` modified) — under the ≥5 gate.

**Action:** relabel `size:s` → `size:xs` on the issue.

## Open questions

- **None blocking.** The verb names (`AcceptTrust`/`Answer`/`SendEsc`) are pinned by #717's body, so they are fixed, not a design choice. If a future consumer wants a per-verb structured `slog` Debug line on dispatch, add it then — left out here to keep the primitive silent and let the consumer own logging (matches `WriteUserTurn`).
