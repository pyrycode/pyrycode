# Spec #1000 — Bound the interactive msgqueue drain retry with a give-up seam (engine-side, unwired)

**Size:** S · **Security-sensitive:** no (engine-side seam ships unwired; see § Security posture) · **Split from:** #991 · **Blocks:** #1001 (wire + producer)

## Files to read first

- `internal/msgqueue/queue.go:74-110` — `DeliverFunc` + `ChangeFunc` + `Config`. The seam style to mirror for the new `GiveUpFunc`/`OnGiveUp`; and the `DeliverFunc` contract (payload is opaque transit, never surfaced in errors) that makes `err.Error()` safe to interpolate into the give-up reason.
- `internal/msgqueue/queue.go:158-186` — `New`: the default-resolution + field-copy pattern (`RetryInterval`/`max`/`log`). Mirror it for `GiveUpAfter` and `OnGiveUp`.
- `internal/msgqueue/queue.go:329-336` — `notify` helper (fires off-lock, nil-safe). Mirror it as `notifyGiveUp`.
- `internal/msgqueue/queue.go:381-436` — `drain`. The retry leg (`if err != nil { … will retry … continue }`, lines 409-424) is the exact edit site; `advanceLocked` (440-443) drops a head; `sleepCtx` (463-472) is the ctx-aware wait; the `NEVER log head.text` constraint is at line 412.
- `internal/msgqueue/queue.go:38-48` — package `SECURITY` header (untrusted phone text is opaque, never logged). AC4 is preservation of this.
- `internal/supervisor/supervisor.go:585-592` — backoff defaults: `BackoffInitial=500ms`, `BackoffMax=30s`, `BackoffReset=60s`. The **max backoff window** the give-up bound must exceed (AC2). Note: these are unexported defaults filled in `New`, not exported consts — so a cross-package numeric pin in a msgqueue test is not cleanly available (see § Testing strategy).
- `internal/msgqueue/queue_test.go:20-117` — `fakeDeliver` (`failTimes`, `gates`, `entered`/`completed`, `recvWithin`, `equalStrings`). Extend with a toggleable permanent-failure mode; reuse the sync-without-sleeps pattern.
- `internal/msgqueue/queue_test.go:260-308` — `TestQueue_LosslessRetry_SurvivesRespawn`, the transient-clears baseline the AC2 "no give-up" test extends. **Place the new give-up tests adjacent to this function (mid-file)**, not at end-of-file — see § Test placement.
- `cmd/pyry/main.go:807-828` + `cmd/pyry/queue_state_v2.go:14-27` — read-only reference: how `OnChange` routes seam → channel → producer. This is the pattern #1001 replicates for `OnGiveUp`. **#1000 does NOT touch `cmd/pyry`.**

## Context

`internal/msgqueue`'s `drain` retries a persistently-failing FIFO head every `RetryInterval` **forever** (`queue.go:409-424`): any non-nil `Deliver` error logs `msgqueue: delivery failed, will retry` and `continue`s indefinitely. A claude child that parks at startup (unanswerable dialog, wedged readiness gate, network stall) therefore loops on a head that never drains and never fails — the client is left with a message that neither runs nor errors.

The retry is deliberately lossless so a message survives an ordinary claude-child respawn/backoff window: the same errors (`ErrNoLiveSession`, `ErrTurnNotCommitted`, PTY write errors) appear transiently during a normal respawn and are **indistinguishable by type** from a persistent wedge. This ticket adds a **bounded give-up** that preserves the survive-respawn property (the bound is wide enough to clear a respawn) while stopping an unbounded wedge, and a **notification seam** (`OnGiveUp`) so a later ticket (#1001) can surface a typed client-visible error over the v2 wire. Per the engine-first rhythm of msgqueue's original landing (#704, mechanism first / wired later), the seam ships **disabled (nil)** — this ticket does not touch `internal/protocol` or `cmd/pyry`.

## Design

Two files: `internal/msgqueue/queue.go` (production) + `internal/msgqueue/queue_test.go` (tests). No cross-package coordination.

### New type + Config surface

A give-up seam that mirrors `ChangeFunc` exactly (off-lock, non-blocking, nil-disables, safe for concurrent invocation):

```go
// GiveUpFunc is the injected give-up-notification seam. It mirrors ChangeFunc:
// invoked NEVER while holding q.mu, with the conversation whose head the drain
// abandoned after persistent delivery failure, plus a daemon-generated
// human-readable reason (elapsed retry window + the last delivery error) that
// NEVER contains the queued message text. MUST NOT block; MUST be safe for
// concurrent invocation. nil disables notification.
type GiveUpFunc func(convID, reason string)
```

Two new `Config` fields (append after `OnChange`, `queue.go:107`):

```go
// GiveUpAfter bounds how long the drain retries a persistently-failing head
// before abandoning it. <= 0 ⇒ defaultGiveUpAfter. Measured as elapsed
// wall-clock since the head's FIRST consecutive delivery failure (a successful
// delivery resets the clock for the next head — per-head, not per-session).
GiveUpAfter time.Duration
// OnGiveUp is the optional give-up-notification seam; nil ⇒ disabled.
OnGiveUp GiveUpFunc
```

One new default const (near `defaultRetryInterval`, `queue.go:64`):

```go
// defaultGiveUpAfter bounds persistent-failure retry. Chosen to exceed the
// supervisor's max backoff window with margin: BackoffMax is 30s and
// BackoffReset 60s (internal/supervisor/supervisor.go), so a transient failure
// spanning a full claude-child respawn/backoff cycle clears well inside this
// bound and never trips give-up (AC2). A tuning knob, not a contract.
const defaultGiveUpAfter = 2 * time.Minute
```

Two new `Queue` fields (`giveUpAfter time.Duration`, `onGiveUp GiveUpFunc`), resolved/copied in `New` using the existing default-resolution idiom (`retry`/`max`/`log` at `queue.go:166-183`): `<= 0 ⇒ defaultGiveUpAfter`.

### `notifyGiveUp` helper

Mirror `notify` (`queue.go:332-336`): nil-safe, caller must have released `q.mu`.

```go
func (q *Queue) notifyGiveUp(convID, reason string) // fires q.onGiveUp iff set; caller holds no lock
```

### `drain` modification (the one behavioural change)

Edit site is the error leg (`queue.go:409-424`). Add a **per-head retry deadline** tracked as a local variable in `drain`, so each head gets a fresh bound and the survive-respawn property is preserved head-by-head.

Behaviour contract (not a code paste — the developer writes the body in-place):

- Declare a local `var firstFailedAt time.Time` in `drain`, before the `for` loop. Zero value ⇒ "the current head has not yet failed."
- **On a delivery error** (existing `if err != nil` leg, after the `ctx.Err()` shutdown check at 401-408 which is unchanged):
  1. If `firstFailedAt.IsZero()`, set `firstFailedAt = time.Now()` (start of this head's retry streak).
  2. Log the existing warn (`queue.go:413-417`) — unchanged, still `conversation_id` + `queued_msg_id` + `queued_at` + `err`, **never** `head.text`.
  3. If `time.Since(firstFailedAt) >= q.giveUpAfter` → **give up** (see below). Otherwise `sleepCtx(ctx, q.retry)` and `continue` exactly as today (including the ctx-cancelled-during-sleep exit at 418-423, unchanged).
- **On a successful delivery** (existing success path, after `advanceLocked`, `queue.go:427-434`): reset `firstFailedAt = time.Time{}` so the next head starts with a fresh bound.

**Give-up action** (per-head latch; drops one head, then the drain exits — AC5):

1. Build `reason` from daemon-controlled values only: the elapsed retry window (`time.Since(firstFailedAt).Round(time.Second)`) and `err.Error()`. **Never** interpolate `head.text` or any `head` field except the daemon-minted `head.id`. Suggested shape (developer's wording): `"delivery failed persistently for <elapsed>; claude session may be wedged (last error: <err>)"`.
2. Log a give-up warn line (distinct message, same field discipline as the retry log: `conversation_id`, `queued_msg_id`, elapsed, `err` — **never** `head.text`).
3. Under `q.mu`: `c.advanceLocked()` (drop the abandoned head, reusing the existing head-drop + backing-array hygiene) and set `c.draining = false`. Unlock.
4. Off-lock, in order: `q.notify(convID)` then `q.notifyGiveUp(convID, reason)`. `notify` fires because the backlog shrank (a head left) — consistent with `ChangeFunc`'s documented "delivery-advance / successful Remove" triggers, and it keeps the wired `queue_state` producer's view correct after the drop. `notifyGiveUp` carries the give-up event to #1001's (currently nil) consumer.
5. `return` — exit the drain goroutine (`defer q.wg.Done()` fires, so no goroutine leak).

**Why exit rather than continue to the next head.** The wedge is a session parked at startup — every head would fail identically, so continuing would burn one full `giveUpAfter` window per head and emit N give-up events for one wedge. Exiting drops only the wedged head, emits **one** give-up notification, and returns the conversation to the clean idle state AC5 requires. Any items still behind the dropped head remain in the FIFO; because `draining == false` and `len(items) > 0` is exactly `maybeSpawnDrainLocked`'s respawn precondition (`queue.go:372-379`), the next `Enqueue` respawns the drain — the same lifecycle as the empty-exit path. Firing `OnGiveUp` **after** clearing `draining` (step 3 before step 4) means a give-up observer can safely re-`Enqueue` without racing a still-`true` `draining` flag.

## Data flow

```
Enqueue → maybeSpawnDrainLocked → drain goroutine
  loop: peek head → Deliver(payload)
    ├─ nil        → advanceLocked, notify(OnChange), reset firstFailedAt, loop
    ├─ ctx.Err()  → leave head queued, clear draining, exit          (unchanged)
    └─ err ≠ nil  → set firstFailedAt if zero; warn-log
                    ├─ elapsed < GiveUpAfter → sleepCtx(retry), loop  (unchanged path)
                    └─ elapsed ≥ GiveUpAfter → GIVE UP:
                          advanceLocked(drop head) + draining=false
                          → notify(OnChange) → notifyGiveUp(convID, reason)
                          → return (drain exits; later Enqueue respawns)
```

## Concurrency model

No new goroutines and no lock-ordering change. `firstFailedAt` is a `drain`-local — one drain goroutine per conversation owns it, so it needs no synchronisation. `advanceLocked` + the `draining = false` write happen under `q.mu` (same lock the peek/advance already use). Both seams (`notify`, `notifyGiveUp`) fire strictly after `q.mu` is released, matching the existing invariant that caller-supplied seams never run under the lock. `defer q.wg.Done()` already covers the new `return` path, so `Run`'s `wg.Wait()` still joins every drain on shutdown (no leak).

## Error handling / failure modes

- **Transient failure that clears within the bound** (ordinary respawn): `firstFailedAt` set on first failure; delivery succeeds before `elapsed ≥ GiveUpAfter`; success resets `firstFailedAt`. No give-up. Preserved by the 2-min default exceeding the 30s `BackoffMax` / 60s `BackoffReset` window.
- **Persistent failure** (startup wedge): retries until `elapsed ≥ GiveUpAfter`, then abandons the head once, notifies, exits clean.
- **Shutdown racing a failing head**: the existing `ctx.Err()` check (401-408) still fires first and leaves the head queued (in-memory loss boundary) — give-up never runs on a cancelled ctx.
- **`OnGiveUp == nil`** (production this ticket): `notifyGiveUp` is a no-op; the bound + head-drop + clean exit still happen (the give-up is real even when unobserved).

## Security posture

Not security-sensitive: the seam is a `Config` callback mirroring the non-sec `OnChange`, ships **disabled (nil)**, adds no inbound-content parse, no outbound wire emission, and no crypto. AC4 is **preservation** of the existing `NEVER log head.text` discipline (`queue.go:38-48`, `:412`), extended to the new give-up log line and to the reason string. The reason is built only from daemon-generated values — the elapsed duration and the delivery `err`, whose `DeliverFunc` contract (`queue.go:74-79`) treats the payload as opaque and never surfaces it in errors. This matches the #991 split classification (engine-side child = not-sec; wire + producer child #1001 = sec) recorded in the PO refinement.

## Testing strategy

Extend `fakeDeliver` with a toggleable permanent-failure mode (small, mutex-guarded): a `permaFail map[string]bool` field + a `setPermaFail(convID string, on bool)` method; `deliver` checks it (returning `errFake`) right where the `failTimes` countdown is evaluated (`queue_test.go:70-73`), before the success append. This lets a test start persistent failure, observe give-up, then flip it off to prove respawn. Reuse `recvWithin`, `entered`/`completed`, `equalStrings`.

New tests (scenarios, not code — developer writes them in the project's table/channel idiom):

- **AC1 + AC3 — give-up on persistent failure.** `permaFail["c"]=true`, `RetryInterval=1ms`, `GiveUpAfter=20ms`, `OnGiveUp` sends `(convID, reason)` to a buffered channel. `Enqueue("c","wedged")`. Assert: `OnGiveUp` fires once within a generous deadline with `convID=="c"` and a non-empty `reason`; `Snapshot("c")` is empty (head dropped); the message was never delivered (`deliveredOrder()` empty). Cancel → `Run` returns `context.Canceled`.
- **AC2 — transient failure clears, no give-up.** `failTimes["c"]=3` (clears on the 4th attempt), `RetryInterval=1ms`, `GiveUpAfter=5s` (comfortably > 3×retry, still fast), `OnGiveUp` records any call. `Enqueue("c","m")`. Assert: `"m"` delivered; `OnGiveUp` **never** fired. (This is `TestQueue_LosslessRetry_SurvivesRespawn` extended with the give-up seam wired to a recorder.)
- **AC2 regression guard — default bound is wide.** A pure unit assertion that `defaultGiveUpAfter >= 2*time.Minute`, with a comment tying it to the supervisor's 30s `BackoffMax` / 60s `BackoffReset`. Rationale for the numeric literal rather than a cross-package reference: those backoff values are unexported defaults filled inside `supervisor.New`, not exported consts, so there is no clean symbol to pin against; the const comment in `queue.go` carries the derivation and this guard prevents a silent narrowing.
- **AC4 — no untrusted-content leak.** `permaFail["c"]=true`, `GiveUpAfter=20ms`, `Logger` writing to a `bytes.Buffer` (slog text handler), `OnGiveUp` captures the reason. `Enqueue("c","SUPER_SECRET_PHONE_TEXT")`. After give-up, assert the captured reason does **not** contain `"SUPER_SECRET_PHONE_TEXT"` and the log buffer does **not** contain it. Non-vacuous: the distinctive marker fails the assertion if the give-up path ever logged or interpolated `head.text`.
- **AC5 — clean teardown + respawn.** `permaFail["c"]=true`, `GiveUpAfter=20ms`. `Enqueue("c","wedged")`; wait for `OnGiveUp`; assert `Snapshot("c")` empty (head dropped, `draining` cleared). Then `setPermaFail("c", false)` and `Enqueue("c","after")`; assert `"after"` is delivered — proving the old drain exited and the new `Enqueue` respawned it. Cancel → `Run` returns `context.Canceled` (proves `wg.Wait` unblocked → no leaked drain goroutine). Sequencing the second `Enqueue` after receiving from the `OnGiveUp` channel is safe because give-up clears `draining` before firing the seam.

Run `go test -race ./internal/msgqueue/`.

### Test placement

Add the new give-up tests **immediately after `TestQueue_LosslessRetry_SurvivesRespawn` (`queue_test.go:308`)** — mid-file, next to the semantically-related retry test — and add the `permaFail` field/method inside the existing `fakeDeliver` block (`queue_test.go:20-77`). Do **not** append them at end-of-file. Rationale: an in-flight branch (#935, for flake #934) edits the trailing function `TestQueue_SnapshotAll_RaceWithEnqueueAndDrain` (end-of-file). That branch's fix is already superseded on `main` (the `drainStop` variant is present at `queue_test.go:716-758`) and it already conflicts with `main`, so it is not a live merge target — but keeping #1000's additions in a disjoint mid-file region makes any residual conflict structurally impossible regardless of how #935 is resolved.

## Open questions

- **`reason` wording** is the developer's call within the AC3/AC4 constraints (daemon-generated; elapsed + `err`; never `head.text`). The suggested string is illustrative.
- **Whether `OnChange` fires on give-up**: this spec says yes (a head left the backlog, so the wired `queue_state` view must drop it). If a reviewer prefers give-up to be silent on `OnChange` and rely solely on `OnGiveUp` + #1001, that is a one-line change — but firing `OnChange` keeps the existing producer correct without waiting on #1001, so it is the recommended default.
