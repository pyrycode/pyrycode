# Spec #830 — Start-new-session (`/clear`) keystroke seam

**Size:** XS (architect downgrade from S — see *Sizing* below).
**Security-sensitive:** No. A fixed-`/clear` mechanical actuator: carries no trust decision, accepts no caller-supplied free text, loosens no control. The authorization gate lives entirely in the future consumer (the `new_session` wire verb, split from #824). Same classification as the #726 keystroke seam it templates from.

## Files to read first

- `internal/supervisor/modal.go:12-32` — `modalKey` enum + `String()`. Add `keyStartNewSession` as a fourth constant here and a matching `String()` case (`"start new session"`, feeding the error prefix). Widen the type doc one line: the enum now carries the `/clear` slash-command actuator too, i.e. an abstract keystroke *intent*, not strictly modal-resolution.
- `internal/supervisor/modal.go:34-66` — the three verb methods (`AcceptTrust` / `Answer` / `SendEsc`). `StartNewSession()` is a new sibling one-liner in the same shape: `return s.sendModalKey(keyStartNewSession, "")`. Copy the shared doc-contract paragraph (no `ctx`, `ErrNoLiveSession` when detached, loud wrapped error on PTY failure).
- `internal/supervisor/modal.go:68-91` — `sendModalKey`: the capture-then-release helper AC-1 names. **Reuse verbatim** — do not duplicate the lock/nil-check/wrap block. It already provides capture-under-`sessMu`, nil-session → wrapped `ErrNoLiveSession` (writes nothing), and the `supervisor: <verb>: %w` wrap.
- `internal/supervisor/modal.go:93-113` — `sendModalKeystroke`, the production `keystrokeFn`. Add a `case keyStartNewSession:` that composes the `/clear` sequence (see § Design). The `default` unknown-key guard stays the last branch.
- `internal/supervisor/supervisor.go:193-199` — `keystrokeFn` field. **Read-only:** its signature `func(sess *tuidriver.Session, k modalKey, choice string) error` is unchanged by a new enum value, so supervisor.go needs **no edit**.
- `internal/supervisor/supervisor.go:565-566` — `New` already wires `s.keystrokeFn = sendModalKeystroke`. The new key routes through it automatically. **No edit.**
- `internal/supervisor/modal_test.go:15-63` — `TestSupervisor_ModalKeystroke_DispatchesAbstractVerb`: extend the table with a start-new-session row. This is the AC-4 dispatch assertion.
- `internal/supervisor/modal_test.go:65-109` — `TestSupervisor_ModalKeystroke_NoLiveSessionFailsLoud`: extend the table with a start-new-session row (AC-2, "writes nothing").
- tui-driver `pkg/tuidriver/session.go:335-398` (module `github.com/pyrycode/tui-driver@v1.9.0`) — `TypePrompt` (types text byte-by-byte with inter-byte delay, settles, then writes a single isolated `\r` commit) and `ClearInputLine` (Ctrl-U line-kill + settle). These are the two calls to compose. Both funnel their PTY writes and return the first non-nil write error, never panic.
- tui-driver `pkg/tuidriver/keys.go:72-99` — `SendKeys` (spike-only hatch, "not intended for production drivers") and the `writeRaw` funnel. Explains why the compose uses `TypePrompt`, not a hand-rolled `SendKeys("/clear\r")`.
- tui-driver `pkg/tuidriver/modal.go:36` + `picker.go:243-256` — `ModalClassSlashPicker` and `isSlashPicker`: the classification that a live `/clear` trips. Context only (the parenthetical in AC-4); classifying it requires a live claude and is out of unit-test scope.
- `docs/lessons.md:54` — `/clear` rotates claude's session UUID even under `--resume`. Confirms the observation side (rotation watcher, `session_transition`) is a **separate** subsystem in `internal/sessions/` (`Pool.RotateID` / `onRotate`) that this ticket does **not** touch.

## Context

The daemon can drive claude's modal keystrokes (`AcceptTrust` / `Answer` / `SendEsc`, the sealed seam from #726) but has no way to trigger `/clear` — the "start a new session" slash command a local user types at the terminal. `Pool.RotateID` / `onRotate` only *observe* the on-disk UUID rotation that `/clear` causes; nothing *drives* it.

This ticket adds that actuator: a `Supervisor.StartNewSession()` method that types `/clear` into the live PTY, sealed exactly like the modal keystroke seam. It ships **unwired** — the remote `new_session` wire verb that consumes it (with its authorization) is the sibling ticket split from #824. This mirrors the codebase's own #726 → #707 factoring: the keystroke actuator is one ticket, the relay verb that consumes it is another.

The primitive is fire-and-forget and carries **no** trust decision. It drives a fixed `/clear`; it accepts no caller-supplied text. Observing the resulting rotation (rotation watcher) and marking the boundary (`session_transition`, #656/#657) are pre-existing, separate machinery in `internal/sessions/` — untouched here.

## Design

Additive edits to one file: `internal/supervisor/modal.go`. `supervisor.go` is not touched (the `keystrokeFn` seam and its `New` wiring already exist and route a new enum value with no signature change).

### Exported contract (`modal.go`)

One new method, a thin sibling of the existing three verbs:

```go
func (s *Supervisor) StartNewSession() error  // → s.sendModalKey(keyStartNewSession, "")
```

Behavior contract (the developer writes the body — a one-liner):

- **Capture-then-release (AC-1):** delegates to the existing `sendModalKey`, which captures `s.sess` under `sessMu`, releases the lock, then actuates on the captured pointer. No lock held across the PTY write. Nothing new to write here — reuse.
- **No live session (AC-2):** `sendModalKey`'s nil-session branch already returns `fmt.Errorf("supervisor: %s: %w", k, ErrNoLiveSession)` and never invokes `keystrokeFn` (writes nothing). Reused sentinel — do **not** add a new one.
- **PTY write error (AC-3):** a compose error from a session torn down mid-write propagates out of `sendModalKeystroke`, and `sendModalKey` wraps it with the stable `supervisor: start new session:` prefix, underlying error preserved for `errors.Is`. Never a panic.
- **No `context.Context` (matches the sibling verbs):** the actuation is a bounded, non-blocking PTY write sequence with nothing to cancel. Adding a ctx would advertise a cancellation contract the method cannot honor. Do not cargo-cult one from `WriteUserTurn`.

### The reliable `/clear` sequence (the core design decision)

Inside `sendModalKeystroke`, the `keyStartNewSession` case composes exactly **two** existing tui-driver calls on the captured session, failing loud on the first error:

1. `ClearInputLine()` — Ctrl-U line-kill. **Required for correctness, not defensive:** the slash-picker only opens when `/` is the first character of the input line. If drafted text sits in the box, `/clear` typed after it renders as literal text (`hello/clear`) and no picker opens. Clearing first guarantees `/` lands at column 0.
2. `TypePrompt("/clear")` — types `/`, `c`, `l`, `e`, `a`, `r` byte-by-byte (each byte opens then filters the slash-picker), settles, then writes an isolated `\r`. The `\r` = Enter = run the highlighted `/clear` entry. This single call *is* the "open-picker → filter → commit" sequence the ticket describes.

The compose is roughly: `if err := sess.ClearInputLine(); err != nil { return err }; return sess.TypePrompt("/clear")` — two statements, no branching, the `choice` argument ignored (the `/clear` literal is fixed in code, satisfying AC-5).

**Why `TypePrompt`, not the alternatives** (record the rationale so it is not "simplified" later):

- **Not `WritePrompt` / bracketed paste.** Paste delivers the text as a literal block; claude's paste path treats `/clear` as prompt text and does **not** open the slash-picker. `TypePrompt`'s byte-spacing is precisely what makes claude register genuine typed input and open (`ModalClassSlashPicker`) then filter the picker. This is the load-bearing reason.
- **Not `SendKeys("/clear\r")`.** `SendKeys` is documented as a spike-only hatch "not intended for production drivers"; a bulk write of the body can trip claude's paste-detection heuristic (the exact failure `TypePrompt` was built to avoid, see its doc / #71). `TypePrompt` is the production primitive for this shape.
- **No explicit extra Enter step.** `TypePrompt`'s trailing `\r` (written as a separate byte after `PromptCommitSettle`) is the picker commit. Typing the full word `clear` filters the picker to claude's exact `/clear` command as the highlighted entry, which `\r` runs. No `Navigate`/arrow step is needed.

### Internal seam (`modal.go`)

Add one constant to the existing enum and one `String()` case — no new type, no new field, no new seam:

```go
keyStartNewSession                // → drives "/clear" via ClearInputLine + TypePrompt
// String(): case keyStartNewSession: return "start new session"
```

The `keystrokeFn` injection seam (`supervisor.go:199`) and its `New` wiring (`supervisor.go:566`) are reused unchanged: the field type does not mention the constant set, so a new enum value needs no signature edit and `sendModalKeystroke` remains the production function `New` already assigns.

### What this ticket deliberately does NOT do

- **No rotation observation.** Does not touch `Pool.RotateID`, `onRotate`, the rotation watcher, or the `session_transition` emitter (all in `internal/sessions/`). Driving the keystroke is the entire scope; observing its effect is pre-existing, separate machinery.
- **No wiring.** No relay handler, no `cmd/pyry` flag, no wire message. Ships unwired; the sibling `new_session` verb consumes it.
- **No authorization / trust decision / audit / logging policy.** The primitive is silent and gate-free; the consumer owns all of that (matches #726).
- **No new sentinel, no new exported type, no `context.Context`, no new goroutine or mutex.**
- **No verification of the rotation.** Fire-and-forget, like the sibling verbs — it does not snapshot-then-`DetectModalClass` to confirm the picker ran. Confirmation is the rotation watcher's job (out of scope) and would require the ctx/timeout this seam deliberately omits.

## Concurrency model

Unchanged from the existing seam. `StartNewSession` runs on an arbitrary consumer-handler goroutine (same as the other verbs and `WriteUserTurn`), serializing on `sessMu` only for the pointer copy inside `sendModalKey`. The two-call compose then runs lock-free on the captured pointer.

`ClearInputLine` and `TypePrompt` each acquire tui-driver's own `writeMu` for the span of their multi-byte write, so a concurrent `AttachInput` or another keystroke cannot interleave *within* either call. The two calls are not one atomic unit — a concurrent writer could land between the line-kill and the typing — but that is acceptable: the fixed `/clear` actuator is not competing with live user typing in the unwired primitive, and the consumer serializes its own invocations. No additional synchronization is introduced.

**Teardown race** is handled exactly as the sibling verbs document: a captured `sess` pointer racing a concurrent `setSession(nil)+Close` writes into tui-driver's teardown-safe PTY-error path (`*os.File.Write` on a closed/nil FD returns a non-nil error, never panics), surfacing here as a loud wrapped error, never a crash or false success.

## Error handling

| Condition | Result |
|---|---|
| No live session (`sess == nil`) | `fmt.Errorf("supervisor: start new session: %w", ErrNoLiveSession)`; `keystrokeFn` not called (writes nothing) — reused `sendModalKey` branch |
| `ClearInputLine` PTY write error | `sendModalKeystroke` returns it; `sendModalKey` wraps with the `supervisor: start new session:` prefix; underlying preserved for `errors.Is` |
| `TypePrompt` PTY write error (e.g. session torn down mid-type) | same wrap-and-preserve path |
| Happy path | `nil` |

## Testing strategy

Extend the existing tables in `internal/supervisor/modal_test.go` (in-package, `t.Parallel()`, `New(helperConfig("exit0"))` + `setSession(&tuidriver.Session{})` + `keystrokeFn` override). Scenarios (developer writes the assertions in the project idiom):

- **Dispatch (AC-4).** Add a row to `TestSupervisor_ModalKeystroke_DispatchesAbstractVerb`: `call = StartNewSession()`, `wantKey = keyStartNewSession`, `wantArg = ""`. Assert the seam was called, recorded `keyStartNewSession` / `""`, and the method returned nil. This proves the supervisor dispatches the start-new-session intent through the injected seam.
- **No live session (AC-2).** Add a row to `TestSupervisor_ModalKeystroke_NoLiveSessionFailsLoud`: assert `errors.Is(err, ErrNoLiveSession)`, the `supervisor: start new session:` prefix is present, and the seam flag stayed false (nothing written).
- **Keystroke error wrap (AC-3).** Either add a case to the existing error-wrap test or assert alongside it: a live session, `keystrokeFn` returns a sentinel `boom`, `StartNewSession()` returns an error with `errors.Is(err, boom)` and the start-new-session prefix.

**Testability boundary (state explicitly; do not try to close it).** The `keyStartNewSession` branch of `sendModalKeystroke` — the real `ClearInputLine` + `TypePrompt("/clear")` compose — cannot be unit-tested against a zero-value `&tuidriver.Session{}`: those methods write to a nil PTY. This is the identical boundary as the existing `AcceptTrust`/`Answer`/`SendEsc` branches, which is exactly why the `keystrokeFn` injection seam exists. The dispatch test proves the correct `modalKey` reaches the seam; tui-driver's own `session_test.go` covers `TypePrompt`/`ClearInputLine` byte behavior. Asserting that a live claude actually renders `ModalClassSlashPicker` and rotates the session UUID needs a spawned claude and belongs to the sibling `new_session` verb's e2e / manual verification — **not** required by this ticket's ACs; do not add a real-spawn test here.

## Sizing

Architect downgrade **S → XS**. The ticket permitted a downgrade "if it collapses"; the `/clear` actuation collapses to a two-call compose inside one existing switch, with no new file and no supervisor.go edit — a strictly smaller footprint than #726 (which was XS and added a whole new file). Tally against the red lines:

| Red line | This ticket |
|---|---|
| New files | 0 (modal.go + modal_test.go both modified) — under 3 |
| Total written LOC | ~15 prod (const + `String()` case + method + switch case) + ~20 test (2–3 table rows) ≈ ~35 — far under 600 |
| New exported types/interfaces | 0 (one new *method*; `modalKey`/`keyStartNewSession` stay unexported) — under 5 |
| Consumer call sites updated simultaneously | 0 — purely additive, unwired |
| Acceptance criteria | 5, but four restate the shared `sendModalKey` contract already implemented; only the `/clear` compose + one dispatch row is net-new work |
| Error/reject branches | reused nil-session + one new switch case — not a state machine |

§4 production-file self-check: **1** production file modified (`modal.go`) — well under the ≥5 gate.

**Action:** relabel `size:s` → `size:xs` on the issue.

## Open questions

- **None blocking.** The one runtime assumption — that typing the full `/clear` filters claude's slash-picker to `/clear` as the highlighted entry so the trailing `\r` runs it — is verified live by the sibling `new_session` verb's integration path (and by a local operator), not by this unit-tested primitive. If a future claude build renames or ambiguates the command, the fix is localized to the single `TypePrompt("/clear")` literal in `sendModalKeystroke`. The method name (`StartNewSession`) is the intent-level verb the consumer will call, one level up from `/clear`, so no consumer-side rename is implied.
