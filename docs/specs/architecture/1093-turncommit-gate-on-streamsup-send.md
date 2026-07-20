# Spec: turncommit gate on streamsup turn send (#1093)

## Files to read first

- `internal/streamsup/envelope.go:67-92` — `WriteTurn(w io.Writer, prompt []byte) error`, the send site to gate. Its doc comment describes the nil-writer / write-error contract this spec extends; keep that prose accurate after the signature change.
- `internal/supervisor/supervisor.go:356-379` — `deliverViaSession`: the **exact idiom to mirror**. The inner `deliver` closure claims the gate with `if gate := turncommit.From(ctx); gate != nil && !gate() { return false, turncommit.ErrDropped }` after `WaitReady`, before the write. Copy this shape verbatim (minus the `bool`).
- `internal/turncommit/turncommit.go` (whole file, 57 lines) — `Gate` (`func() bool`), `ErrDropped`, `From(ctx) Gate` (nil when no gate), `With(ctx, gate)` (nil gate → ctx unchanged). `With` is the test-injection entry point.
- `internal/streamsup/envelope_test.go:79-124` — the three existing `WriteTurn` unit tests to update (signature) plus the reusable sinks: `bytes.Buffer` (observe bytes written) and `errWriter` (failing writer). Reuse both for the new gate tests.
- `internal/streamsup/roundtrip_test.go:53-65` — the multi-turn integration call site (`WriteTurn(w, []byte(marker))`) that also needs the new `ctx` argument.

## Context

The turn-I/O slice (#1088, merged) landed `streamsup.WriteTurn` — the stream-json user-envelope writer on the child's held-open stdin. It writes unconditionally today. When turns are queued by `internal/msgqueue`, the head can be **dropped during the wait for claude to become ready**; delivering a dropped head would inject a stale or cancelled turn into the live session.

The PTY path already solves this: `supervisor.deliverViaSession` claims the turncommit gate carried on `ctx` immediately before writing, and aborts with `turncommit.ErrDropped` on a false claim. The stream-json send path needs the **identical** guard. This ticket adds it; the PTY path is untouched.

`WriteTurn` has **zero production callers today** (only 4 test sites — verified via `codegraph_impact WriteTurn`). The eventual runner wiring (T4/T7) will call it from the msgqueue delivery seam, which carries the gate on `ctx`. So this is a pure additive-guard slice with no consumer cascade.

## Design

**Decision: thread `context.Context` into `WriteTurn` as the first parameter** (rather than a separate gated wrapper).

Rationale: the downstream consumer (msgqueue delivery seam) already holds the gate-bearing `ctx` and must pass it to the write. Threading `ctx` mirrors `deliverViaSession(ctx, sess, payload)`'s ctx-first shape, keeps one send function instead of two, and costs only the 4 test call-site updates (no production cascade). A wrapper would add a second exported name for no benefit here.

New signature:

```go
func WriteTurn(ctx context.Context, w io.Writer, prompt []byte) error
```

Body order (three checks, then the unchanged marshal+write):

1. **`w == nil` → `ErrNoLiveChild`** (unchanged, stays first). No claim is consumed on a no-live-child.
2. **Gate claim** — `if gate := turncommit.From(ctx); gate != nil && !gate() { return turncommit.ErrDropped }`. Placed **before** `marshalTurnEnvelope`, so a false claim writes zero bytes.
3. **marshal + write** — unchanged (`marshalTurnEnvelope` → `w.Write`).

**Why the nil-writer check precedes the gate claim** (design contract, not covered by an AC): this is the faithful mirror of the supervisor path, where `WriteUserTurn` rejects a nil `sess` (`ErrNoLiveSession`) *before* entering the gate-claiming `deliverViaSession`. The gate claim's contract (`turncommit.go:24-29`) is "call once, after the wait, before the write, to mark the head un-droppable." Claiming it when there is no live child to write into would prematurely lock a head we cannot yet deliver — during the retryable `ErrNoLiveChild` window the user must still be able to drop it cleanly. Ordering does not change AC3's zero-write guarantee (a dropped head is never written under either order); it only decides which sentinel wins when both conditions hold, and `ErrNoLiveChild` (retryable) is the correct winner.

Two new imports in `envelope.go`: `context` and `github.com/pyrycode/pyrycode/internal/turncommit`.

## Data flow

```
msgqueue delivery seam (T4/T7, future)
  ctx = turncommit.With(base, gate)         // gate claims/reports the queue head
      │
      ▼
WriteTurn(ctx, runner.Stdin(), prompt)
      │  w == nil?            → ErrNoLiveChild  (no claim)
      │  gate!=nil && !gate() → ErrDropped      (zero bytes written)
      │  else                                    → marshal + write one envelope
      ▼
child stdin (held open)
```

Non-queue callers (e.g. a direct single-turn send) pass a plain `ctx` with no gate; `From` returns nil and the turn is written unconditionally (AC2).

## Concurrency model

None new. `WriteTurn` remains a synchronous single-writer call on the caller's goroutine. `turncommit.From` is a lock-free `ctx.Value` read; the gate closure itself does its own locking inside msgqueue (out of scope). No goroutines, channels, or shared state introduced here.

## Error handling

- `w == nil` → `ErrNoLiveChild` (unchanged, retryable outcome per #1088 doc).
- False gate claim → `turncommit.ErrDropped`, **zero bytes written** (returned bare, matching `deliverViaSession` — the queue keys drop-handling on `errors.Is(err, turncommit.ErrDropped)`, so do **not** wrap it).
- Nil gate → no gate consulted, delivery proceeds.
- marshal / write failures → unchanged (`streamsup: marshal turn: %w` / `streamsup: write turn: %w`).

## Testing strategy

Update the 4 existing call sites to pass a context (`context.Background()` for the non-gated tests; the roundtrip integration test passes `context.Background()` too). Then add gate-focused unit tests in `envelope_test.go`:

- **False claim → ErrDropped, zero bytes (AC1 + AC3).** `ctx := turncommit.With(context.Background(), func() bool { return false })`; sink is a `bytes.Buffer`; assert `errors.Is(err, turncommit.ErrDropped)` **and** `buf.Len() == 0` (observe the sink, not just the error). Use `errWriter` as a second sink to prove the write path is never entered (a false claim must not even reach `w.Write`).
- **Nil gate → delivers unconditionally (AC2).** `context.Background()` (no gate); assert the full marshalled envelope is written (reuse the `marshalTurnEnvelope`-equality assertion from `TestWriteTurn_WritesEnvelope`).
- **True claim → delivers, gate consulted exactly once.** Gate is a counter closure returning true; assert the envelope is written **and** the gate was called exactly once (mirrors `deliverViaSession`'s "call it once per delivery attempt"; guards against a double-claim or a skipped-claim regression).
- **Nil writer wins over a false gate (ordering contract).** `w == nil` with a false-returning, call-counting gate; assert `errors.Is(err, ErrNoLiveChild)` (not `ErrDropped`) **and** the gate was never called (pins the nil-check-before-claim order from § Design).

All stdlib `testing`, table-driven where natural, `-race`-clean. No live claude needed — every path is exercised with an in-memory writer and a synthetic gate.

## Open questions

None. The gate contract, the sentinel, and the reference implementation (`deliverViaSession`) are all fixed and merged; this slice only ports the idiom to the stream-json writer.
