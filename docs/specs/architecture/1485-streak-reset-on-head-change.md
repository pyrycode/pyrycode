# #1485 — msgqueue: reset the give-up streak when the head changes between attempts

Short plan: one guard in one function, no new type, state or failure mode.

## Files read

- `internal/msgqueue/queue.go` → `Queue.drain` — owns the drain-local `firstFailedAt` streak; the change lands in its delivery-failure branch.
- `internal/msgqueue/queue.go` → `Config.GiveUpAfter` doc — states the per-head contract the fix restores; its comment gains the head-change reset.
- `internal/msgqueue/queue.go` → `Queue.Remove` — a head removal between attempts (no delivery in flight, `deliverCancel` nil) just splices the FIFO; the drain re-peeks and finds a new head.
- `internal/msgqueue/head_advance_test.go` → `giveUpRow`, `newHeadDeliver`, `errWedged`, `recvWithin` — `Config.Pending` is the test-controllable seam between the drain's `dropped` read and the give-up check; the new row reuses it and the helpers.

## Change

In `drain`'s delivery-failure branch the streak starts only when `firstFailedAt.IsZero()`. A `Remove` that lands between attempts (during the retry `sleepCtx`) swaps the head without passing through any reset, so the next head's first failure inherits the old head's start time. Add a drain-local `streakID uint64` naming the head the streak belongs to, and start a fresh streak when `firstFailedAt.IsZero() || streakID != head.id`, setting both. That is the same reset the `dropped` path performs, applied to a head change the drain did not see happen. The other resets (confirmed delivery, `dropped`, `pending`, `giveUp` abandoning nothing) are untouched; a stale `streakID` after one of them is harmless because the zero `firstFailedAt` already forces a fresh start. The `firstFailedAt` comment and `Config.GiveUpAfter` doc gain one clause each naming the head-change reset.

## Testing strategy

New row `TestQueue_GiveUp_HeadChangedBetweenAttempts_FreshStreak` in `internal/msgqueue/streak_reset_test.go`. Backlog `[A,B]`, delivery always failing, `RetryInterval` 50ms, `GiveUpAfter` 300ms. `Config.Pending` (drain goroutine only, returns false every time) counts calls:

- call 3 (A's third failure, elapsed ≈ 100ms, inside the bound): `Remove(A)`. This runs after the `dropped` read and before the sleep, with no delivery in flight, so it is exactly a removal during the retry sleep — deterministic, no sleep races. A removal during the delivery would be caught by `dropped` and pass on `main`.
- call 4 (B's first failure): record `tB`. It runs before the drain sets the streak start, so `tB ≤ firstFailedAt`.

`OnGiveUp` records `tGU` when it fires. Assertions: give-up fires exactly once, `tGU − tB ≥ GiveUpAfter` (with the fix, `tGU ≥ firstFailedAt + GiveUpAfter ≥ tB + GiveUpAfter`, so this holds on any scheduler), the FIFO ends empty and nothing was delivered. On `main` the gap is ≈ 300 − 100 − 50 (+ ≤ one retry) ms, well under 300ms, so the row is red. The reported elapsed in the reason string rounds to whole seconds, so at test scale it reads `0s` either way; the timing bound is the proof that B's reported elapsed counts from B's own first failure.

The existing give-up rows in `queue_test.go` and `head_advance_test.go` pass unmodified (AC-2): a single failing head never changes id, so the guard never fires for them.

## Documentation handoff

Pending for the documentation stage: `docs/knowledge/features/msgqueue-package.md`, the "Per-head, not per-session" bullet — state that the streak also restarts when the head changes between attempts (a head removed during the retry sleep), alongside the reset on a confirmed delivery.
