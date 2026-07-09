# Spec #866 — Guard `Session.id` under `lcMu` (RotateID vs unlocked lifecycle readers)

**Ticket:** [#866](https://github.com/pyrycode/pyrycode/issues/866) — data race on `Session.id`: live `/clear` rotation (`RotateID`) vs unlocked lifecycle-goroutine readers.
**Size:** S (2 production files, ~15–20 production lines + one `-race` test). Not security-sensitive (no `security-sensitive` label; id lock-hygiene is a correctness fix, not a trust surface).

## Files to read first

- `internal/sessions/pool.go:549-569` — `RotateID`. The `sess.id = newID` write (559) currently sits **outside** the `lcMu` section (560-562); the fix moves it inside. This is the only post-construction writer of `sess.id`.
- `internal/sessions/pool.go:540-548` — the stale `RotateID` invariant comment ("no concurrent reader exists"). AC #2 requires correcting it.
- `internal/sessions/pool.go:980-995` — `BootstrapID`. The in-repo precedent for reading id-related state race-cleanly against `RotateID`. **Its comment (986-990) also references the now-stale "no concurrent reader of sess.id" invariant and must be updated** (see § Design → Comment corrections).
- `internal/sessions/session.go:124-191` — the `Session` struct + `ID()` accessor. Field `id` (128) carries no lock-discipline comment, unlike its siblings `label` (135-137), `settings` (142-147), `lastActiveAt` (187, guarded by `lcMu` per 178-180). Add one. Note the `lcMu` block (178-188) — `id` will join the fields it guards.
- `internal/sessions/session.go:400-411` — reader site #1: `PreviousID: s.id` in the eviction-transition `notifyTransition` (unguarded, lifecycle goroutine).
- `internal/sessions/session.go:505-524` — reader site #2: `"session_id", string(s.id)` in the idle-eviction warn log (unguarded, lifecycle goroutine). Note the pre-existing `lcMu` acquire/release just above (506-508) for `s.attached`.
- `internal/sessions/pool.go:296-316` — `List`: reads `s.id` (313) under `Pool.mu` (RLock). The exemplar of a **`Pool.mu`-holding** reader that stays untouched (already race-clean via `Pool.mu`). Do **not** convert it.
- `internal/sessions/pool_test.go:319-358` — `TestPool_RotateID_HappyPath` + `helperPoolPersistent` (147-163). The new race test reuses this exact setup (`helperPoolPersistent` → `pool.Default()`).
- `CODING-STYLE.md` § Concurrency + Testing — `go test -race` is mandatory; table/loop tests, stdlib only, `t.Parallel()`.

## Context

`Pool.RotateID` mutates `sess.id = newID` under `Pool.mu` only. Its comment claims callers run "before any lifecycle goroutine begins observing the id" — **stale since #839** wired `RotateID` into the live fsnotify rotation watcher (`Pool.Run` → `OnRotate` (`pool.go:1042`) → `onRotate` (`transition.go:95`) → `RotateID`). `OnRotate` fires on every `/clear`, and the watcher goroutine runs concurrently with the per-session lifecycle goroutines.

Two lifecycle-goroutine sites read `sess.id` with **no lock**:
- `session.go:407` — `PreviousID: s.id` (eviction-transition notify).
- `session.go:521` — `string(s.id)` (idle-eviction warn log).

A `/clear` rotation concurrent with the same session's eviction path is a data race on a two-word string header — a torn read `go test -race` would flag. Production runs without `-race`, so it is latent.

## Design

### The invariant: `sess.id` is guarded by two locks; a read needs either

The fix makes `sess.id` a field written under **both** `Pool.mu` (write) **and** `Session.lcMu`, where **any single one of those locks suffices for a read**. This is the standard "protected by two mutexes — hold both to write, either to read" pattern, and it is exactly what reconciles the two existing reader classes without touching either:

| Reader | Lock it holds | Race-clean against the write because |
|---|---|---|
| `List`, `ResolveID`, `Snapshot`, `saveLocked`, `Activate`/`pickLRUVictim` (`pool.go` 313, 917, 1276, 1369, 1435) | `Pool.mu` (R) | write holds `Pool.mu` (W) — mutually exclusive |
| lifecycle sites (`session.go` 407, 521) + `ID()` (191) | `lcMu` (via `currentID()`) | write holds `lcMu` — mutually exclusive |

Two concurrent readers holding *different* locks never conflict (both reads; no write is in flight, since the writer cannot proceed until it holds **both** locks). The Go race detector accepts this — every write↔read pair synchronizes on a shared lock.

**This is why the `Pool.mu`-holding readers stay untouched.** They are already correct. The single load-bearing change is: the writer must additionally hold `lcMu`, and the currently-unlocked lifecycle readers must take `lcMu`.

### Change 1 — `RotateID`: move the id write inside the existing `lcMu` section

`RotateID` already opens an `lcMu` critical section to bump `lastActiveAt` (`pool.go:560-562`). Move `sess.id = newID` into it — no new lock, no new section, no lock-order change.

Behavior contract (unchanged except the write is now `lcMu`-guarded):
- Still runs entirely under `Pool.mu` (Lock) via the function-level `defer`.
- `lcMu` is acquired and released **before** the map delete/insert, `bootstrap` flip, and `saveLocked` — those stay outside `lcMu`, exactly as today. `saveLocked` reads `s.id` under `Pool.mu` (the writer also holds `Pool.mu`, so that read is race-clean; it does **not** need `lcMu`).
- Lock order preserved: `Pool.capMu → Pool.mu → Session.lcMu`.

Resulting shape (contract sketch, not to be pasted verbatim — reuse the existing early returns):

```
p.mu.Lock(); defer p.mu.Unlock()
sess, ok := p.sessions[oldID]        // ErrSessionNotFound if !ok
if oldID == newID { return nil }     // no-op
sess.lcMu.Lock()
sess.id = newID                      // MOVED inside lcMu
sess.lastActiveAt = time.Now().UTC()
sess.lcMu.Unlock()
delete/insert map; flip bootstrap; return p.saveLocked()
```

### Change 2 — a `currentID()` guard for every lifecycle-goroutine reader

Add one unexported accessor on `*Session`:

- `func (s *Session) currentID() SessionID` — returns `s.id` under `s.lcMu` (Lock/Unlock via `defer`). One-line body. Doc comment states the two-lock invariant and that lifecycle-goroutine readers (those not holding `Pool.mu`) must route through it.

Route all three id reads that do **not** hold `Pool.mu` through it:
- `session.go:407` → `PreviousID: s.currentID(),` — the helper acquires/releases `lcMu` to produce the value; `lcMu` is **not** held across `notifyTransition` (the value is materialized first, then passed). No lock-order concern (`notifyTransition`/`Pool.mu` is never taken under `lcMu`).
- `session.go:521` → `"session_id", string(s.currentID()),` — same materialize-then-log shape. The sibling fields in this log call (`s.idleTimeout`, `s.bootstrap`) are immutable post-construction and stay as bare reads; only `s.id` is racy.
- `session.go:191` → `func (s *Session) ID() SessionID { return s.currentID() }`.

**`ID()` routing is a deliberate architect decision, not scope creep.** `ID()` has zero production callers today (verified: the only in-package `.ID()` reference is a comment in `pool.go:987`; the ticket confirms no production caller). But `ID()` reads the same `id` field the field comment now advertises as `lcMu`-guarded. Leaving it a bare `return s.id` would make the field comment lie and plant a foot-gun for the first future caller that reads it off a goroutine. Cost is one line; benefit is a uniformly enforceable invariant ("every `s.id` read is either under `Pool.mu` or via `currentID()`"). No deadlock risk: `lcMu` is non-reentrant, but no caller of `ID()` (or of the two lifecycle sites) holds `lcMu` when calling — the current `lcMu`-guarded fields are `lcState`/`attached`/channels/`lastActiveAt`, none of which read `id`.

### Change 3 — Comment corrections (AC #2)

1. `RotateID` invariant comment (`pool.go:540-548`): replace the "no concurrent reader exists" claim with a statement that `sess.id` is `lcMu`-guarded — written under both `Pool.mu` (W) and `lcMu`; read by lifecycle goroutines via `currentID()` and by `Pool.mu`-holders directly. Note that the live rotation watcher (#839) is the concurrent reader that made the old invariant stale.
2. `BootstrapID` comment (`pool.go:986-990`): it currently justifies reading `p.bootstrap` by appeal to the same stale "no concurrent reader of sess.id" invariant. Update it: `BootstrapID` still reads `p.bootstrap` under `Pool.mu` (not `sess.id`) because that is the `Pool.mu`-guarded value and it avoids taking `lcMu` on the spawn path — the rationale survives, the invariant phrasing does not.
3. `id` field comment (`session.go:128`): add a lock-discipline note matching its siblings — "guarded by `lcMu`; written by `RotateID` under both `Pool.mu` (W) and `lcMu`; read via `currentID()` off the lifecycle goroutine, or directly under `Pool.mu`."

## Concurrency model

- **No new goroutine, no new mutex, no channel.** Pure lock-discipline tightening.
- **Lock order unchanged:** `Pool.capMu → Pool.mu → Session.lcMu`. The writer takes `Pool.mu` then `lcMu` (nested, order-respecting). Readers take exactly one lock and nest nothing.
- **Writer holds both locks; each reader holds one.** Detailed in the § Design table. This is the crux — do not "simplify" it by converting the `Pool.mu` readers to `lcMu` or vice-versa; the split is intentional and correct.
- **`saveLocked` stays a `Pool.mu`-only reader of `s.id`** and is invoked after `lcMu` is released — no lock-order inversion, no deadlock.

## Error handling

No new error paths. `RotateID`'s error contract (`ErrSessionNotFound`, `oldID==newID` no-op, `saveLocked` error propagation) is byte-identical. `currentID()` cannot fail. AC #4 (no behaviour change outside the guarded read/write) is satisfied structurally: the only semantic change is which lock is held during three field accesses.

## Testing strategy

One new `-race` test in `internal/sessions` (recommended file: `internal/sessions/pool_id_race_test.go`, to keep the RotateID happy-path tests in `pool_test.go` conflict-isolated). Stdlib only.

**`TestPool_RotateID_IDRace` (scenario, not code):**
- Setup: `pool := helperPoolPersistent(t, filepath.Join(t.TempDir(), "sessions.json"))`; `sess := pool.Default()`; `id0 := sess.ID()`; pick a distinct valid-shaped `id1` (a second UUID).
- **Writer goroutine:** loop N times, ping-ponging `pool.RotateID(a, b)` then `a, b = b, a`, starting `a,b = id0,id1`. The writer is the sole rotator, so the `oldID` it passes is always the live id — the same `*Session` pointer survives every rotation (RotateID re-keys the map, it does not replace the struct), so `sess` stays valid throughout.
- **Reader goroutine:** loop N times calling `_ = sess.ID()` — this exercises the shared `currentID()` guard that sites 407 and 521 now route through.
- `sync.WaitGroup` both goroutines; run under `go test -race ./internal/sessions/...`; assert clean (no race report). Do **not** assert on the returned id value (it legitimately flip-flops); the test's subject is synchronization, not value.
- **N sizing:** RotateID calls `saveLocked` (a disk write) each iteration, so keep N modest (~200–500). The race detector fires on the first unsynchronized overlap, so large N is unnecessary; the goal is reliable interleave, which two hammering goroutines achieve quickly.

**Non-vacuity (AC #1 — "produces a race report against the pre-fix code"):** the committed test passes on the fixed tree. The developer MUST confirm it is non-vacuous by a one-time reversion check: revert **either** production guard —
- (a) move `sess.id = newID` back outside `RotateID`'s `lcMu` block, **or**
- (b) change `currentID()`/`ID()` back to a bare `return s.id` —

and re-run `go test -race`; the detector must report a data race on `Session.id`. Restore the guard afterward. Reverting either side alone fires the report, which demonstrates the two-lock write and the `lcMu` read are jointly load-bearing. Record the observed race-report snippet in the PR/knowledge note.

**Why not drive the real `Run` loop to hit 407/521 directly:** both sites fire once per lifecycle (single-shot) and then tear the session down. A single read racing a single write has near-zero overlap probability — a vacuous race test. Because the fix collapses all three id reads onto `currentID()`, hammering `ID()`→`currentID()` in a loop faithfully covers the exact guard 407 and 521 depend on, and reliably interleaves thousands of accesses. This is the tractable, non-flaky way to satisfy AC #1.

**Regression guard:** `TestPool_RotateID_HappyPath` (`pool_test.go:319`) already asserts `Default().ID() == newID` post-rotation and `lastActiveAt` advancement; it exercises the now-`lcMu`-guarded `ID()` and continues to pass. `go test -race ./...` for the whole tree must stay green.

## Open questions

None blocking. Two notes for the developer:
- If `pool_id_race_test.go` as a new file feels heavier than warranted, appending the test to `pool_test.go` is acceptable — no sibling branch currently touches `internal/sessions` test files (verified at spec time), so either placement is conflict-free. The dedicated file is the recommendation, not a requirement.
- The `id` field comment should mirror the phrasing style of the `label`/`settings`/`lastActiveAt` comments already in the struct, for local consistency.

## Scope self-check

Production source files modified: `internal/sessions/pool.go`, `internal/sessions/session.go` — **2** (well under the ≥5 gate). No new exported types/interfaces. No signature changes → zero call-site cascade (`RotateID` and `ID()` keep their signatures; `codegraph_impact` not needed — the `id` field is package-internal and `ID()` has zero production callers). One new test file. Single concern. Stays S.
