# Spec — #1120 streamsup: interrupt send primitive + `error_during_execution` → interrupted turn end

**Size:** S (confirmed; PO sized S). Purely additive, 3 production files modified, 0 new files, ~2 new exported symbols, no consumer cascade.

**Not security-sensitive** (no `security-sensitive` label). The interrupt *routing* sibling (#1121) carries the security review; this ticket delivers only the streamsup-internal primitive it consumes.

## Context

On the stream-json path a turn runs inside a persistent `internal/streamsup.Runner` (`streamsup.New`, constructed & injected per session by the #1109 factory). Interrupt on this path is **not** a keystroke: the T1 spike (#1075, verified live 2026-07-19) established it as a first-class stdin control line — write `{"type":"control_request","request_id":"<id>","request":{"subtype":"interrupt"}}` to the child's held-open stdin. claude acks (~40 ms) with `control_response{subtype:success,request_id:<id>}` and ends the turn with a `result` whose subtype is `error_during_execution` (`is_error:true`).

Two gaps to close, both entirely inside `internal/streamsup`:

1. **Send half.** The runner has no way to send an interrupt control line to its child. Add the primitive.
2. **Receive half.** `Parser.consumeLine` maps **every** `result` line to `TurnEnd{Reason: end_turn}` and never reads `subtype` — the code comment in `parser.go:152-156` explicitly defers "interrupt→cancelled" to this work. Read `subtype`; map `error_during_execution` → `TurnEnd{Reason: cancelled}`, everything else unchanged.

`turnevent.TurnEndReasonCancelled` already exists (`taxonomy.go:42`) and is already carried through the downstream wire mappings (`turnbridge/outbound.go:91` and `cmd/pyry/acp_turn_stream.go:73` both do `string(e.Reason)`, and both already test the `cancelled` value) — so the parser change cascades to **zero** downstream code edits. No new turnevent reason is introduced.

The *routing* of an inbound remote interrupt frame to the correct per-conversation runner is #1121 (blocked-by this ticket). This ticket must **not** widen the `sessions.Runner` interface (`internal/sessions/runner.go:25-31` — `State`/`WriteUserTurn`/`WaitForPTY`/`Run`/`Restart`, no `Interrupt`); `Interrupt` stays a concrete method on `*streamsup.Runner`, mirroring how `*supervisor.Supervisor` encapsulates `SendEsc` (#726) without that method being on the interface. #1121 reaches it via its own consumer-declared narrow interface or a type assertion.

## Files to read first

- `internal/streamsup/runner.go:117-153` — `Runner` struct fields (three leaf mutexes, `stdin`); where the interrupt correlation-id counter field is added.
- `internal/streamsup/runner.go:198-233` — `Stdin()` accessor (returns `io.Writer`, nil between spawns) and `WriteUserTurn` (the one-line wrap of a free `envelope.go` function). `Interrupt()` mirrors this exactly.
- `internal/streamsup/envelope.go:13-116` — `ErrNoLiveChild` sentinel (line 18, **reuse it**), `marshalTurnEnvelope` (pure, structured-encoding single-physical-line invariant, 52-68), and `WriteTurn` (free func: nil-check → sentinel, marshal, single `w.Write`, wrap error, 93-116). The interrupt marshal + write mirror these, minus the turncommit gate.
- `internal/streamsup/parser.go:97-164` — `streamLine`/`streamMessage`/`streamBlock` decode shapes (add `Subtype` to `streamLine`) and `consumeLine` (the `case "result"` branch at 151-156 is the only behavioral edit).
- `internal/turnevent/taxonomy.go:34-43` — `TurnEndReason` enum. `TurnEndReasonCancelled` (line 42) is the mapping target; already exists, do not add.
- `internal/streamsup/parser_test.go:28-152` — `TestParser_LineMapping` table + `collectEvents` helper; add the `error_during_execution` row(s) here.
- `internal/streamsup/interface_test.go:31-47` — `TestRunner_WriteUserTurn_NoLiveChild`; the exact pattern to mirror for `Interrupt` with no live child.
- `internal/streamsup/runner_test.go:239-280` — `TestRunner_HoldsStdinOpen` (spawn a fake child, `onSpawn` signal, `waitForContains`, capture `Stdin()`); the pattern to mirror for the live-capture interrupt test.
- `internal/streamsup/helper_test.go:56-65` — `echo_lines` fake-child mode; **reuse it** for stdin capture (it echoes each stdin line as `ECHO:<line>`), no new helper mode needed.

## Design

Two independent additive seams. Neither touches `Run`, the supervise loop, `New`, or the `sessions.Runner` adapter in `cmd/pyry`.

### Seam A — interrupt send primitive

**`envelope.go` (new, unexported decode shape + two functions):**

```go
// controlRequest is the stream-json control line written to claude's stdin.
// Marshalled structured (never string-concatenated) so it is exactly one
// physical line — same injection-resistance invariant marshalTurnEnvelope holds.
type controlRequest struct {
    Type      string              `json:"type"`        // "control_request"
    RequestID string              `json:"request_id"`  // locally-minted correlation id
    Request   controlRequestInner `json:"request"`
}
type controlRequestInner struct {
    Subtype string `json:"subtype"` // "interrupt"
}

// marshalInterruptEnvelope returns the single newline-terminated control line.
func marshalInterruptEnvelope(requestID string) ([]byte, error)

// WriteInterrupt writes one interrupt control line onto w (the child's held-open
// stdin). Mirrors WriteTurn minus the turncommit gate: a nil w → ErrNoLiveChild
// and zero bytes written; a write failure is wrapped; never closes w.
func WriteInterrupt(w io.Writer, requestID string) error
```

- `marshalInterruptEnvelope` behaviour: build the struct with fixed literals `"control_request"` / `"interrupt"`, `json.Marshal`, append `'\n'`. One physical line by construction (the interrupt has no free-text field, so the injection surface is nil — but the structured-encoding discipline is kept for symmetry and future-proofing).
- `WriteInterrupt` behaviour: `if w == nil { return ErrNoLiveChild }` **first** (mirrors `WriteTurn`'s nil-first ordering, and satisfies AC2's "safe... never a panic and never a partial write"); then marshal; then a single `w.Write(env)`, wrapping any error as `fmt.Errorf("streamsup: write interrupt: %w", err)`. No `context.Context` param — an interrupt is a single non-blocking pipe write with nothing to cancel and no gate to claim (the `SendEsc` precedent: keystroke actuators take no ctx).

**`runner.go` (one new field, one new method, one private helper):**

```go
// added to the Runner struct — zero value is ready, no New() change:
interruptSeq atomic.Uint64   // locally-minted correlation ids for Interrupt

// Interrupt writes a single interrupt control_request line to the live child's
// stdin. A no-op-safe refusal (ErrNoLiveChild) when no child is live. The
// request_id is locally minted (not caller-supplied). Mirrors WriteUserTurn's
// one-line wrap; symmetric to supervisor.SendEsc's encapsulation.
func (r *Runner) Interrupt() error   // returns WriteInterrupt(r.Stdin(), r.nextInterruptID())

func (r *Runner) nextInterruptID() string   // strconv.FormatUint(r.interruptSeq.Add(1), 10)
```

**Correlation-id choice.** A per-`Runner` `atomic.Uint64` counter, stringified. Rationale: (a) unique within the runner's lifetime, which is all a future ack-correlator needs since each runner drives exactly one child stream; (b) no `crypto/rand`/UUID dependency and no import of `internal/sessions` (forbidden by streamsup's dependency direction); (c) this ticket does not read the `control_response` ack at all — the id is effectively write-only here, so the minimal-unique choice is correct (Simplicity First / Evidence-Based: no ack-correlation failure has been observed, so nothing richer is warranted). `atomic.Uint64` because `Interrupt` may be called from a different goroutine than `Run`.

**Why a free `WriteInterrupt` + a thin method** (rather than inlining the write in the method): mirrors the existing `WriteTurn`/`WriteUserTurn` split, and lets the marshal + nil-refusal + wrap logic be unit-tested against a `bytes.Buffer` / nil writer with a fixed id, independent of a live `Runner`.

### Seam B — parser `subtype` mapping

**`parser.go`:**

- Add one field to `streamLine`: `Subtype string \`json:"subtype"\``. (The decode shape already ignores every field it doesn't name; adding `subtype` is inert for all other line types.)
- In `consumeLine`'s `case "result"`, replace the fixed `TurnEnd{Reason: end_turn}` emit with a reason computed from `sl.Subtype`. Extract a pure helper for testability + clarity:

```go
// resultTurnEndReason maps a result line's subtype to its TurnEnd reason.
// error_during_execution is claude's interrupt-terminated turn (spike T1,
// #1075) → cancelled; every other subtype (success, and any unknown) keeps
// today's end_turn (correct for a clean turn, safe default otherwise).
func resultTurnEndReason(subtype string) turnevent.TurnEndReason
```

  The mapping is scoped to `error_during_execution` **only** (AC4): `case "error_during_execution": return TurnEndReasonCancelled; default: return TurnEndReasonEndTurn`.
- Update the deferred-work comment at `parser.go:152-156` to reflect that the interrupt→cancelled mapping now lands here (remove the "land there" / #1089 deferral note for this subtype; `max_tokens`/`refusal` classification remains out of scope and can stay noted as future work).

Segmentation is unchanged: the switch still keys on the **top-level `type` only**, and the nested `subtype` is read only within the already-matched `result` case — a tool result whose text literally contains `error_during_execution` still cannot forge a turn boundary (it is never the top-level `type`).

## Concurrency model

No new goroutines, no new mutex. The interrupt write reuses the exact discipline `WriteTurn` established:

- `Interrupt` captures the live stdin handle via `r.Stdin()` (which takes `r.mu` internally, returns nil between spawns), then writes a single complete line via one `w.Write` **outside** any streamsup lock.
- The control line is small (well under `PIPE_BUF` = 64 KiB on Linux/macOS), so a single `write(2)` to the pipe is atomic: an interrupt line cannot byte-interleave with a concurrent `WriteTurn` user-envelope line on the same stdin. Both are single-`Write`-of-one-complete-line, matching the package's existing single-writer-per-syscall model.
- `interruptSeq` is an `atomic.Uint64`; `nextInterruptID` uses `.Add(1)`, safe from any goroutine.
- The teardown race is already handled: `Stdin()` returns nil once `takeStdin` clears the handle, and a write landing on a just-closed pipe returns a wrapped `EPIPE` (never a panic), identical to `WriteTurn`'s contract.

## Error handling

| Condition | Result |
|---|---|
| No live child (`Stdin() == nil`) | `ErrNoLiveChild` (reused sentinel), zero bytes written — AC2 |
| Marshal failure (not reachable with fixed literals, defensive) | wrapped `fmt.Errorf("streamsup: marshal interrupt: %w", err)` |
| Pipe write failure (e.g. `EPIPE` mid-teardown) | wrapped `fmt.Errorf("streamsup: write interrupt: %w", err)`, no panic |
| `result` line with unknown/absent `subtype` | `TurnEnd{end_turn}` — unchanged default (AC4) |
| `result` line, `subtype: success` | `TurnEnd{end_turn}` — unchanged (AC4) |
| `result` line, `subtype: error_during_execution` | `TurnEnd{cancelled}` (AC3) |

## Testing strategy

Stdlib `testing`, table-driven, `go test -race`. All tests live in `internal/streamsup` (same-package, white-box), reusing existing helpers.

**Seam A — send primitive:**

- **Marshal (unit, pure):** `marshalInterruptEnvelope("fixed-id")` produces byte-exact `{"type":"control_request","request_id":"fixed-id","request":{"subtype":"interrupt"}}` + trailing `'\n'`; assert exactly one `'\n'` and that it round-trips (`json.Unmarshal` back to the struct). A fixed id keeps this assertion deterministic.
- **`WriteInterrupt` nil writer:** `WriteInterrupt(nil, "id")` returns `ErrNoLiveChild` and writes nothing (AC2); no panic.
- **`Interrupt` no live child (integration):** construct a `Runner` (do **not** call `Run`), `r.Interrupt()` → `errors.Is(err, ErrNoLiveChild)`, no panic. Mirror `TestRunner_WriteUserTurn_NoLiveChild`.
- **`Interrupt` live capture (integration, AC5.1):** spawn the `echo_lines` fake child, wait for `READY` + `onSpawn`, call `r.Interrupt()`, `waitForContains(out, "ECHO:")`, then locate the echoed line, strip the `ECHO:` prefix, `json.Unmarshal` it, and assert `type == "control_request"`, `request.subtype == "interrupt"`, and `request_id` non-empty. ("Exact line" for a minted id resolves to exact structure + non-empty id; the byte-exact assertion is the marshal unit test above.)
- **Minted-id monotonicity (optional, cheap):** two `nextInterruptID()` calls return distinct, increasing values — pins "locally-minted, not caller-supplied."

**Seam B — parser mapping (AC5.2):**

- Add rows to `TestParser_LineMapping`:
  - `{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"S"}` → `[]turnevent.Event{TurnEnd{Reason: TurnEndReasonCancelled}}` (AC3).
  - `{"type":"result","subtype":"success","session_id":"S"}` → `TurnEnd{end_turn}` (AC4; the existing `result ends the turn` row already covers a subtype-carrying success — keep it, it now also proves the default branch).
  - A `result` line with **no** `subtype` → `TurnEnd{end_turn}` (AC4 default).
  - Optionally a `result` with an unknown subtype (e.g. `"max_tokens"`) → `TurnEnd{end_turn}` (confirms the change is scoped to `error_during_execution` only, not a general subtype→reason table).
- **`resultTurnEndReason` (unit, pure):** small table — `error_during_execution`→`cancelled`, `success`→`end_turn`, `""`→`end_turn`, unknown→`end_turn`.

Ideally use a real captured `error_during_execution` `result` fixture (the ticket references the vault `Streamrunner Spike/` harness folder). If the exact captured JSON isn't readily transcribable, a hand-authored line carrying the same `type`/`subtype`/`is_error` fields is sufficient — the parser reads only `type` and `subtype`.

## Open questions

- **Reuse `echo_lines` vs. a dedicated capture mode.** The spec recommends reusing `echo_lines` (zero new helper cost). If the developer finds prefix-stripping the echoed JSON awkward, a minimal dedicated `record_stdin` mode (append each stdin line to a capture file, like `crash` does for argv) is an acceptable alternative — developer's call within the existing idiom.
- **`is_error` on the parsed `result`.** The interrupt `result` carries `is_error:true`, but the mapping keys on `subtype` alone (the spike shows `error_during_execution` is the reliable discriminator; a future non-interrupt error subtype could also set `is_error`). Keying on `subtype` only is correct for this ticket; do **not** add an `is_error`-based branch speculatively (Evidence-Based Fix Selection).
- **`max_tokens`/`refusal` result subtypes.** Still unmapped — out of scope here. `resultTurnEndReason`'s `default: end_turn` is the safe placeholder; a later ticket extends the table if those subtypes are observed.
