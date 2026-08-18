# Spec #1484 — msgqueue: advance the head by id, not position

**Ticket:** [#1484](https://github.com/pyrycode/pyrycode/issues/1484) · `bug` · `size:s` · `security-sensitive`
**Package:** `internal/msgqueue` (single package, single production file)

---

## Files to read first

| Where | Symbol | What to extract |
|---|---|---|
| `internal/msgqueue/queue.go` | `advanceLocked` | The whole bug: `items = items[1:]` with no guard. This is the edit site. |
| `internal/msgqueue/queue.go` | `Queue.drain` | The two advance call sites and the `dropped` local. Read the whole loop — the ordering of `ctx.Err()` → `err == nil` → `dropped` → `Pending` → retry/give-up is load-bearing and does **not** change. |
| `internal/msgqueue/queue.go` | `Queue.giveUp` | Second advance site. Note the current order (Warn → lock → advance → `draining=false` → unlock → both seams); AC-3 forces that order to change. |
| `internal/msgqueue/queue.go` | `Queue.commitGate` | **The predicate to mirror.** Its reject condition — conv missing, FIFO empty, or front id ≠ the attempted id — is exactly the "is this still my head" test the guard needs. The bug is that this predicate existed at one site and not at the other two. |
| `internal/msgqueue/queue.go` | `Queue.Remove` | Why the head is droppable at all (`idx == 0 && c.committing` gate, #1085), and the `deliverCancel` plumbing. Read its doc comment — it carries the safety argument that this ticket rewrites. |
| `internal/msgqueue/queue.go` | `convQueue.shrinkLocked` | Why `[A]` panics rather than silently misbehaving: it sets `items = nil` at empty, so a later `items[1:]` is out of range. |
| `internal/msgqueue/queue_test.go` | `fakeDeliver`, `newFakeDeliver`, `recvWithin`, `waitSnapshotEmpty`, `equalStrings` | The harness every new test builds on. `setPermaFail` is the "claude wedged" toggle the give-up rows need. |
| `internal/msgqueue/queue_test.go` | `TestQueue_RemoveHeadWaitingForIdle_Droppable` | The closest existing shape to the success-path rows: gate a delivery, `Remove` the head mid-flight, assert the outcome. |
| `internal/msgqueue/queue_test.go` | `TestQueue_GivesUpAfterPersistentFailure` | The give-up harness: `GiveUpAfter: 20 * time.Millisecond`, `OnGiveUp` recorder channel, `setPermaFail`. |
| `internal/msgqueue/queue_test.go` | `TestQueue_GiveUp_NoUntrustedContentLeak` | The `slog` + `bytes.Buffer` log capture AC-3's "no give-up warning names it" assertion reuses — **including its comment about why reading the buffer is race-clean.** |
| `internal/msgqueue/giveup_exempt_test.go` | `exemptPending`, `scriptDeliver` | How `Config.Pending` is injected and driven. The give-up rows use `Pending` as the in-window seam. |
| `docs/knowledge/features/msgqueue-package.md` | § *Introspection…* bullet **"`Remove(convID, id)` — the in-flight-head no-op is the load-bearing rule"**; § *Concurrency model* bullet **"TOCTOU on the FIFO head:"**; § *Bounded give-up…* bullet **"Give-up drops one head and exits the drain"** | The three stale regions AC-5 names. Read all three before editing any. |

---

## Context

`advanceLocked` drops index 0 unconditionally. Both callers decided "the head is still mine" in an *earlier* `q.mu` hold and neither re-checks under the hold that actually splices. Since #1085 (`1192107`) narrowed `Remove`'s head gate from `draining` to `committing`, the head is deliberately droppable for the entire idle-gate wait — so the assumption `advanceLocked` was built on is gone, while `advanceLocked` is unchanged.

Two shapes, both reproduced on `main` at `2e33ffe` (see the ticket for traces):

| Backlog | Outcome today |
|---|---|
| `[A,B]` | `B` is spliced away — never delivered, no `session_error`. Violates the package's **No silent drop** invariant. |
| `[A]` | `shrinkLocked` already set `items = nil`; `items[1:]` panics. **The daemon dies.** |

This is not only a goroutine race: the success path computes `dropped` and then never reads it, so any `Remove` landing during the in-flight delivery reproduces it — precisely the "cancel a message queued behind a running turn" case #487 exists to serve.

---

## Design

One guard, expressed once, applied at both advance sites. No new exported surface, no behaviour change to `Remove`.

### 1. `advanceLocked` takes the id it is advancing past

```go
// advanceLocked drops the head IF it is still the message the caller delivered,
// and reports whether it did. Returns false when a concurrent Remove already
// took that head (empty FIFO, or a different id at the front) — the caller must
// then treat its message as cancelled, not delivered or abandoned.
func (c *convQueue) advanceLocked(id uint64) bool
```

Guard shape — **both conjuncts, in this order**: the FIFO is non-empty **and** `items[0].id == id`. The length test must short-circuit; on `[A]` the id comparison alone panics. Write it as the exact mirror of `commitGate`'s reject condition and say so in the comment — the defect is that this predicate lived at one site and not the others, so the fix should make the three readable as one rule.

On a true return, run `shrinkLocked` as today. On false, mutate nothing.

**Keep the name `advanceLocked`.** Do not rename to `advanceHeadLocked` or similar: the symbol is referenced by name in `docs/knowledge/features/msgqueue-package.md` (six places outside the three regions AC-5 scopes), in `docs/knowledge/INDEX.md` (owned by the documentation phase — out of bounds), and in several historical `docs/knowledge/codebase/<N>.md` files and prior specs, which are records and must not be rewritten. The signature carries the new contract; the name still describes the action.

### 2. Success path

Replace the bare `c.advanceLocked()` in `drain`'s `err == nil` branch with the guarded call, and make the change-notification conditional on it:

- Advanced → reset `firstFailedAt`, fire `notify(convID)`, `continue` (unchanged behaviour).
- Not advanced → the user dequeued this head during its own delivery. Reset `firstFailedAt`, fire **no** `notify` (`Remove` already fired one for the same backlog change), `continue`.

The next loop iteration re-peeks: with `[A,B]` it picks up `B`; with `[A]` it finds the FIFO empty, clears `draining` and returns cleanly. Both ACs fall out of the loop that already exists — no new exit path.

> Detail worth not rediscovering: `Remove` cancels the *delivery* ctx (`deliverCancel`), not the lifecycle ctx. The `ctx.Err()` check ahead of the `err == nil` branch reads the lifecycle ctx, so a mid-delivery `Remove` does not divert the drain into the shutdown branch. The success path really is reachable with the head already gone.

### 3. Give-up path

`giveUp` reports whether it abandoned anything:

```go
// giveUp abandons a head that has failed persistently … Returns false when the
// head is no longer at the front of the FIFO — the user dequeued it during the
// failure window. Nothing is dropped, nothing is reported, draining stays true,
// and the caller keeps draining.
func (q *Queue) giveUp(convID string, c *convQueue, head queued, elapsed time.Duration, err error) bool
```

Ordering changes: the guarded advance moves **ahead of** the Warn line, because AC-3 requires that a dequeued head produce no give-up report at all. The abandon path keeps today's sequence exactly — advance, clear `draining`, unlock, Warn, `notify`, `notifyGiveUp` — with the Warn now inside the abandon branch. (Clearing `draining` before the seams stays: the #1000 argument that a give-up observer can re-`Enqueue` without racing a still-true flag is untouched.)

**Invariant to keep tight: `giveUp` returns true ⟺ `draining` was cleared ⟺ the caller returns.** Clearing `draining` on the not-abandoned path would strand the conversation (a live drain with `draining == false` lets `maybeSpawnDrainLocked` start a second one); leaving it set on the abandon path would deadlock respawn forever. Do not decouple the two.

Call site in `drain`:

- `giveUp(...)` true → `return` (unchanged).
- false → reset `firstFailedAt` and `continue`, the same treatment the existing `dropped` branch gives a cancelled head. With `[A,B]` this delivers `B` immediately rather than waiting for the next `Enqueue` to respawn; with `[A]` the loop finds the FIFO empty and exits cleanly.

`continue` rather than `return` is deliberate. #1000's "exit, don't skip to the next head" rule exists because a startup wedge fails every head identically and continuing would burn one `GiveUpAfter` window per message. That reasoning applies to an *abandonment*; here nothing was abandoned — the head was cancelled, which the loop already handles by continuing with a fresh clock.

### 4. What does not change

- **`Remove` is untouched.** The ticket's alternative direction (hold `committing` true across confirm-to-advance) would narrow what `dequeue_message` can cancel — a behaviour regression against #487/#1085, not a hardening. Rejected.
- **The `dropped` local stays** where it is and keeps its job: skipping the retry-Warn and the give-up accounting for a head already known gone at that point. The guard is the backstop for a `Remove` landing *after* that read; the two are layered, not redundant.
- **The retry-Warn (`"delivery failed, will retry"`) is out of scope.** It fires before the drain can know the head was dequeued, so it may still name a head that a `Remove` is concurrently taking. AC-3 constrains the *give-up* warning only. Tests must assert against the give-up message specifically, not against any occurrence of the id.
- No new goroutine, no new lock, no lock-ordering change, no exported-API change.

---

## Concurrency model

Unchanged in shape: one `sync.Mutex` leaf lock (`q.mu`), one drain goroutine per conversation, seams (`deliver`, `onChange`, `onGiveUp`) never called under the lock.

What changes is *which* lock hold owns the decision. Today the "is this still my head" question is answered in one hold (`drain`'s post-delivery hold, or not at all) and acted on in a later one. After this change the question and the splice happen in the same hold, inside `advanceLocked`, which is the only place `items[0]` is dropped. That makes the check-and-splice atomic with `Remove`, which mutates `items` under the same lock — closing the TOCTOU rather than narrowing its window.

Goroutine lifecycle is unchanged: every path out of `drain` still clears `draining` before returning (loop-top empty exit, shutdown branch, give-up abandon), and `defer q.wg.Done()` still covers all of them, so `Run`'s `wg.Wait` continues to join every drain.

---

## Error handling

No new error values and no new failure modes. The guard converts one panic into a no-op and one silent drop into a correct advance. The classification the drain now draws:

| Post-delivery state | Treatment |
|---|---|
| Delivery succeeded, head still at front | Advance, notify, fresh clock |
| Delivery succeeded, head dequeued | No advance, no notify, fresh clock, continue |
| Delivery failed, head dequeued | Cancellation — no retry, no give-up accounting, fresh clock, continue |
| Delivery failed, head present, `Pending` says hold | Retry same head, fresh clock (#1014) |
| Delivery failed, head present, inside the bound | Retry same head |
| Delivery failed, head present, past the bound | Abandon: advance, clear `draining`, Warn, both seams, exit |
| Delivery failed, past the bound, head dequeued in-window | **New:** nothing abandoned, nothing reported, continue |

A head the user dequeued is *cancelled*, never *abandoned* — the treatment the success path already gives it. That is the whole content of AC-3.

---

## Testing strategy

New file `internal/msgqueue/head_advance_test.go`, `t.Parallel()`, stdlib only, built on `newFakeDeliver` / `recvWithin` / `waitSnapshotEmpty`. **Four test functions — the two success rows must not be collapsed into one.**

The half-guard matrix from the ticket, restated so it is checkable: a guard that tests only non-emptiness is red on `[A,B]` and green on `[A]`; a guard that tests only the id is red on `[A]` (the comparison panics) and green on `[A,B]`. Only the full conjunction greens both. If a reviewer can delete one conjunct and keep both tests green, the rows are wrong.

**Row 1 — success, `[A,B]`, `B` survives.** Enqueue `A` then `B`. The injected `Deliver`, while delivering `A` and holding no lock, calls `Remove` on `A`'s id and returns `nil`. Assert: `B` reaches the delivered order, and the snapshot drains to empty. Fails on `main`: `B` is spliced away and never delivered.

**Row 2 — success, `[A]`, no panic.** Same seam, single message. Assert: no panic; the drain reaches its clean empty-exit; a message enqueued *afterwards* is delivered (this is the positive signal that the drain and the daemon are alive — do not assert only on the absence of a panic). Fails on `main`: `slice bounds out of range [1:0]`.

**Row 3 — give-up, `[A]`, no panic and no give-up reported.** `GiveUpAfter` small (mirror the existing `20 * time.Millisecond`), `RetryInterval: time.Millisecond`, delivery forced to fail. Inject `Config.Pending` as the in-window seam: on its **first** call only, `Remove` the head and clear the forced failure, then return `false` so the drain falls through to the give-up branch. Assert: no panic; `OnGiveUp` never fired; the captured log contains no give-up-warning line for that id; a message enqueued afterwards is delivered.

**Row 4 — give-up, `[A,B]`, `B` queued and deliverable, no give-up reported.** As row 3 with a second message. Assert: `B` is delivered; `OnGiveUp` never fired for `A`; no give-up warning names `A`.

**Synchronisation — read this before writing rows 3 and 4.** The existing give-up tests wait on the `OnGiveUp` channel, which is exactly the event these rows assert never happens; there is nothing to receive. Use a positive signal that happens-after the give-up decision on the same drain goroutine — the delivery of `B` (row 4) or of the follow-up message (row 3) — and check the `OnGiveUp` recorder and the log buffer only after it arrives. `TestQueue_GiveUp_NoUntrustedContentLeak`'s comment states the same happens-before argument for its buffer read; reuse it, don't re-derive it. **No sleeps and no goroutine-timing assumptions** (AC-4): every interleaving is driven by the injected `Deliver` and `Pending` seams.

`Config.Pending` is called on every failure, so the removal must fire once — a counter or `sync.Once` in the test closure. `GiveUpAfter <= 0` resolves to the 2-minute default; it must be small-but-positive.

Gate: `make check` (the package is hermetic; no live-claude surface here).

---

## Documentation correction (AC-5)

`docs/knowledge/features/msgqueue-package.md`, three regions, all stale as of `2e33ffe`:

1. **The `Remove` bullet** ("the in-flight-head no-op is the load-bearing rule"). Two claims are wrong: the head is refused **iff** `c.draining` (it has been `c.committing` since #1085), and "`draining == true` ⟹ no-op on the head, so `advanceLocked` can never drop the wrong message" (the retired safety argument this ticket replaces). Rewrite to: the head is refused only while `committing`, i.e. only once the delivery seam has claimed it via `commitGate`; a merely-waiting head is droppable by design (#487); and head-drop safety now comes from `advanceLocked`'s own id check, not from the `Remove` gate.
2. **The TOCTOU bullet** in § *Concurrency model*. "A concurrent `Enqueue` can only append … so the in-flight head can't be swapped out" is no longer the whole story — `Remove` also races the head, deliberately. Rewrite to state that the drain advances by **id** under the lock that splices, so a concurrent `Remove` of the in-flight head makes the advance a no-op instead of dropping the next message.
3. **The give-up bullet** ("Give-up drops one head and exits the drain"). It describes the drop and both seams as unconditional. Rewrite to record the condition: give-up abandons, reports and exits only when the head is still at the front; a head dequeued during the failure window is cancelled — nothing dropped, nothing reported, the drain continues.

Do **not** touch `docs/knowledge/INDEX.md` (documentation phase is its sole writer), `docs/PROJECT-MEMORY.md`, `docs/lessons.md`, or any historical `docs/knowledge/codebase/<N>.md`. Do **not** write `docs/knowledge/codebase/1484.md` — the documentation phase owns it.

---

## Open questions

None blocking. Two decisions recorded above rather than deferred: `Remove` is not changed (the `committing`-widening alternative is a behaviour regression), and the not-abandoned give-up path `continue`s rather than `return`s (nothing was abandoned, so #1000's exit rationale does not apply).

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The only untrusted value in this package is `queued.text` (phone-supplied), and this change never reads it: the guard compares `uint64` ids minted internally by `Enqueue`, and no new code path touches `text`. `convID` remains a map key only. The boundary is unchanged and still enforced upstream of `Enqueue`.
- **[Tokens, secrets, credentials]** Not applicable — no credential, token or key is created, stored, compared or transported. Queued-message ids are monotonic per-conversation counters, not capabilities: `Remove` already treats an unknown or non-matching id as a `false` no-op, and this change adds no id-derived authority.
- **[File operations]** Not applicable — `internal/msgqueue` is entirely in-memory (its documented daemon-restart loss boundary). No path, no file, no `os` call added.
- **[Subprocess execution]** Not applicable — no `exec`, no environment handling. Delivery reaches the claude child only through the injected `DeliverFunc`, whose contract is unchanged.
- **[Cryptographic primitives]** Not applicable — no randomness, no comparison against a secret. The `items[0].id == id` comparison is between two daemon-generated integers with no secrecy property, so constant-time comparison is not indicated.
- **[Network & I/O]** No findings. No socket, no header, no timeout surface. The inbound resource bound (`MaxQueuedPerConversation`, #869) is enforced at `Enqueue` and is untouched; this change only ever removes items or declines to, so it cannot grow a backlog past the cap.
- **[Error messages, logs, telemetry]** **SHOULD FIX, addressed in-spec.** The give-up Warn and the `reason` string are built from daemon-generated values only and must stay that way — moving the Warn inside the abandon branch (§ Design 3) is a reordering, and the developer must not add `head.text` or any new field while restructuring. The existing `TestQueue_GiveUp_NoUntrustedContentLeak` is the standing check; row 3/row 4's log-buffer assertions extend the same capture. Flagged for code-review rather than gated: the change is a strict reduction in log output (one fewer give-up line on the cancelled path), and no new field is specified.
- **[Concurrency]** No findings — this category *is* the ticket. The pre-change defect is a TOCTOU: check in one `q.mu` hold, mutate in a later one. The fix moves the check into the mutating hold (`advanceLocked`), which is the only site that drops `items[0]`, making check-and-splice atomic against `Remove` under the same leaf lock. No second lock, no nesting, no new goroutine. Two liveness hazards were identified and pinned in the design rather than left implicit: (a) `giveUp`'s return value must stay welded to whether `draining` was cleared — clearing it on the continue path would let `maybeSpawnDrainLocked` start a second drain for one conversation, breaking the serial-delivery invariant; (b) every `drain` exit still clears `draining` and is still covered by `defer q.wg.Done()`, so `Run`'s `wg.Wait` cannot hang and no drain leaks. The seams stay off-lock, so a re-entrant `OnChange`/`OnGiveUp` still cannot deadlock.
- **[Threat model alignment]** The relevant documented property is the package's own **"No silent drop"** invariant — every message delivered, still queued, or explicitly abandoned with a trace. `main` violates it in both directions: `[A,B]` drops `B` with no trace, and `[A]` kills the daemon (a remote-triggerable crash: a phone's `dequeue_message` on a waiting head is enough, so this is availability-relevant, not merely a correctness bug). The fix restores the invariant at both advance sites. No relay/wire-protocol threat in `docs/protocol-mobile.md` § Security model is in scope — this change is engine-internal and alters no frame, no code, and no seam signature visible to a phone. `dequeue_message`'s reachable outcomes are unchanged: `true` (removed) or `false` (no-op).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-18
