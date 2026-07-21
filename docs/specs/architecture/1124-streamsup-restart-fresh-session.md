# Spec #1124 — streamsup: restart the stream-json runner into a fresh session under a new id

**Size:** S · **Security-sensitive:** no (mechanism only; the routing sibling #1125 carries the `security-sensitive` label)
**Package touched:** `internal/streamsup` only. No interface change, no cross-package edit, no new file.

## Files to read first

- `internal/streamsup/runner.go:339-416` — `Run`: the spawn loop. Line 359, `buildArgs(r.liveArgs(), firstRun, r.cfg.SessionID)`, is the **exact seam** to change. The `firstRun` local (declared 341, flipped 388-390 only `if started`) is the "current id not yet on disk" state the fresh path must re-arm, not bypass.
- `internal/streamsup/runner.go:266-326` — `Restart` / `liveArgs` / `setIterCancel` / `drainRestart`: the `restartMu` + `restartCh` publish/consume/coalesce discipline `RestartFresh` mirrors exactly. Read `Restart`'s doc comment (266-281) — the "hint first, then cancel" ordering and the coalescing rationale carry over verbatim.
- `internal/streamsup/runner.go:507-529` — `buildArgs`: the pure id-flag builder. It stays **byte-identical**; only the *source* of its `sessionID` argument changes. Its three existing tests (`TestBuildArgs*`) are unaffected.
- `internal/streamsup/runner.go:119-160` — `Runner` struct, esp. the `restartMu`-guarded field group (145-154). The two new fields land here.
- `internal/streamsup/runner.go:165-203` — `New`: seed the new mutable id field from `cfg.SessionID` in the returned struct literal (195-202).
- `internal/streamsup/runner.go:418-488` — `spawnAndWait`: read the `started` contract (comment 418-426). A setup failure returns `started == false`, and the fresh path must honor the same "retry with `--session-id`, never `--resume` against an id `--session-id` never created" invariant.
- `internal/streamsup/runner_test.go:398-444` — `spawnArgsRecorder`: a `slog.Handler` that captures the argv of every `"spawning claude"` log record, in order. This is the primary observation seam for the new tests — it sees argv per spawn including spawns that never launch a child.
- `internal/streamsup/runner_test.go:336-394` — `TestRunner_ResumeIDStableAcrossRestart`: the ordered-argv assertion pattern (`--session-id <id>` on spawn 1, `--resume <id>` on spawn 2). The new AC5 test mirrors this shape with a rotation injected in between.
- `internal/streamsup/runner_test.go:41-92` — `helperRunCfg` / `runInBackground` / `waitForContains` + `onSpawn` usage. `onSpawn` fires **on the Run goroutine, after `cmd.Start`, every spawn** — the injection point for a deterministic rotation.
- `internal/streamsup/runner_test.go:454-490` — `TestRunner_SpawnSetupFailureRetainsSessionID`: the `started == false` retry invariant the fresh path must not break.
- `cmd/pyry/streamsup_runner.go:107-152` — `mapStreamsupConfig` + `stripSessionIDFlags`: confirms *why* a plain `Restart(--session-id newID)` cannot work — the id flag is stripped out of `Config.Args` and re-injected by `buildArgs` from `Config.SessionID`. This is the Context section's core constraint; no change here.
- `docs/knowledge/features/streamsup-package.md` — evergreen package doc (context, not code).

## Context

On the stream-json path a session runs inside a persistent `*streamsup.Runner`. Its restart ladder builds argv as `buildArgs(liveArgs, firstRun, cfg.SessionID)`: the **first** spawn passes `--session-id <id>` (establishes a new on-disk transcript under a known id); **every** respawn passes `--resume <id>` (reattach, append, no fork).

A plain `Restart(args)` cannot start a *fresh* session for two structural reasons:

1. The id is read from the private `cfg.SessionID`, never from `Restart`'s args — and `mapStreamsupConfig` id-strips the base args (`stripSessionIDFlags`), so `Restart([--session-id newID])` would only get `--resume <oldID>` appended on top (double-inject / contradiction).
2. `firstRun` is `false` after the first spawn, so any `Restart` respawns with `--resume` — a resume of the same id, never a fresh `--session-id`.

This ticket adds the runner-internal *mechanism* to restart fresh under a caller-supplied new id. The **load-bearing subtlety**: after a fresh restart the runner's *persistent* id must become the new id, or a later crash-respawn's ladder will `--resume` the **old** id — a silent regression to the pre-rotation session.

The *routing* of an inbound `new_session` frame to the right runner, and the pool-side `Pool.RotateID`, are the sibling ticket #1125 (blocked-by this one). This spec delivers only the runner method it consumes.

## Design

The whole change lives in `internal/streamsup/runner.go`. The seam mirrors the existing `Restart` machinery: publish under `restartMu`, hint `restartCh`, cancel the child; the `Run` goroutine consumes at the top of its loop. Two things must cross the goroutine boundary that `Restart` doesn't carry today: **the new id** and **a "re-arm first-run" intent**.

### 1. Two new `restartMu`-guarded fields

Add to the `restartMu` field group (`runner.go:145-154`):

```go
// sessionID is the live claude session id read by each spawn's buildArgs.
// Seeded from cfg.SessionID in New; rotated to a new id by RestartFresh. The
// mutable analogue of the (immutable) cfg.SessionID — that field stays the
// construction-time input and New's non-empty validation source.
sessionID string

// rotatePending is set by RestartFresh and consumed once by the Run loop's
// nextSpawnID: it re-arms first-run form so the next spawn uses
// --session-id <newID> (a fresh transcript), not --resume.
rotatePending bool
```

`New` (`runner.go:195-202`) seeds `sessionID: cfg.SessionID` in the returned literal. `cfg.SessionID` is otherwise read only by `New`'s validation — after construction the live id is `r.sessionID`.

### 2. `RestartFresh` — the new exported method

Contract (mirrors `Restart`, minus the args swap, plus the rotation publish):

```go
// RestartFresh rotates the runner's persistent session id to newID and forces
// the next spawn to use --session-id <newID> (a fresh transcript, no fork),
// re-establishing first-run semantics. A subsequent crash-respawn then
// --resumes newID — never the pre-rotation id. If a child is live it is
// cancelled so Run relaunches immediately (skipping backoff, like Restart);
// with no live child the rotation takes effect on the next spawn. Non-blocking,
// fire-and-forget, safe from any goroutine. An empty newID is a no-op (logged) —
// the runner never spawns --session-id "".
func (r *Runner) RestartFresh(newID string)
```

Body shape (≤ ~12 lines, do **not** expand into a full listing):
- Guard: `if newID == "" { r.log.Warn(...); return }`. Deterministic safety net upholding `New`'s non-empty contract; the validating boundary is the pool/routing layer (#1125), this is last-resort.
- Under `restartMu`: `r.sessionID = newID`; `r.rotatePending = true`; capture `cancel := r.iterCancel`. (Note: `RestartFresh` deliberately leaves `r.args` untouched — args are owned by `Restart`/`UpdateSettings`; only the id rotates here.)
- Send `restartCh` hint non-blockingly (same `select { case r.restartCh <- struct{}{}: default: }` as `Restart`), **then** `if cancel != nil { cancel() }`. Hint-before-cancel ordering is load-bearing — identical rationale to `Restart`'s comment (runner.go:288-289).

### 3. `nextSpawnID` — the consume accessor

Replace the direct `r.cfg.SessionID` read at the `buildArgs` call site with a single `restartMu`-guarded accessor that snapshots the id **and** consumes the pending-fresh flag atomically:

```go
// nextSpawnID snapshots the live session id and whether this spawn must use
// first-run form, consuming the fresh-restart request. Under restartMu.
func (r *Runner) nextSpawnID() (sessionID string, forceFirst bool)
```

Returns `(r.sessionID, r.rotatePending)` and clears `r.rotatePending` to `false`, all under one `restartMu` lock — one consistent snapshot, no TOCTOU between reading the id and the flag.

### 4. The Run-loop change (the only edit to `Run`)

At `runner.go:359`, replace:

```go
args := buildArgs(r.liveArgs(), firstRun, r.cfg.SessionID)
```

with:

```go
sessionID, forceFirst := r.nextSpawnID()
if forceFirst {
    firstRun = true
}
args := buildArgs(r.liveArgs(), firstRun, sessionID)
```

`buildArgs` itself is unchanged. `firstRun` remains a Run-goroutine-local — its ownership model is untouched; `forceFirst` only *re-arms* it to `true` at rotation time. This is why the existing `if started { firstRun = false }` invariant (388-390) keeps working for free:

- **Fresh spawn succeeds** (`started == true`): `firstRun` flips back to `false`, so the next crash-respawn uses `--resume <newID>`. ✔ AC2.
- **Fresh spawn fails setup** (`started == false`): `firstRun` stays `true` (the flip is gated on `started`), so the retry uses `--session-id <newID>` again — the fresh id was never established on disk. ✔ same invariant as `TestRunner_SpawnSetupFailureRetainsSessionID`, now extended to the rotated id.

Clearing `rotatePending` immediately in `nextSpawnID` (even before the spawn outcome is known) is safe precisely because `firstRun` carries the "not yet established" state across setup-failure retries — the re-arm need only fire once.

### Why this seam (and not the alternatives)

- **Not `Restart(args)` with an id in args**: the id is structurally stripped from `Config.Args` and re-injected from `Config.SessionID`; passing it through args double-injects (see Context). The rotation must reach the id source, not the args.
- **Not mutating `cfg.SessionID` from the caller** (the ticket's stated non-preference): `cfg.SessionID` is read on the Run goroutine without a lock; a cross-goroutine write to it would be a data race. The runner owns the mutable id under `restartMu`, symmetric to how it already owns `buildArgs`/`firstRun`.
- **`RestartFresh` is a concrete method, off `sessions.Runner`** — same discipline as `Interrupt` (#1120) and `SendEsc`: the interface stays un-widened (#1077). #1125 reaches it via a narrow interface or type assertion; that decision belongs to the routing sibling, not here.
- **No args parameter on `RestartFresh`**: `new_session` rotates the id, not the model/flags. Args stay owned by `Restart`. If a future need to swap both atomically appears, add `RestartFresh(newID string, args []string)` then — evidence-based, deferred until observed.

## Concurrency model

No new goroutine, no new mutex. The rotation rides the existing `restartMu` + `restartCh` seam:

- **Publish** (any goroutine, in `RestartFresh`): `restartMu` write of `sessionID` + `rotatePending`, then the `restartCh` hint, then the child cancel. Never touches `mu`, `stateMu`, or any pool lock — so the sessions layer can call it after releasing `Pool.mu`, exactly as it calls `Restart`.
- **Consume** (Run goroutine, in `nextSpawnID` at the loop top): one `restartMu` read+clear.
- **`firstRun` stays Run-goroutine-private**: it is never published; `forceFirst` (from `rotatePending`) is the only cross-goroutine signal, and it merely re-arms the local. This preserves today's single-writer model for `firstRun`.
- **Coalescing is correct**, verbatim with `Restart`: two rapid `RestartFresh(idA)` then `RestartFresh(idB)` collapse to one relaunch with `idB` (newest id wins, single buffered token). A `RestartFresh` interleaved with a `Restart(args)` composes cleanly — the args swap and the id rotation are independent fields under the same lock; whichever the loop reads next sees both latest values.
- **Restart-hint drain unchanged**: `RestartFresh` sends the same `restartCh` token `Restart` does, so a fresh restart is likewise treated as "not a crash" — `drainRestart` (394) skips backoff, and a fresh restart during backoff breaks the wait via the existing `case <-r.restartCh` (411).

## Error handling

- **Empty `newID`**: `RestartFresh` no-ops with a `Warn` log — the runner never emits `--session-id ""`. The pool/routing layer (#1125) is the real validating boundary (`ValidID`, per the "caller-supplied id validation at the primitive boundary" project convention); this is a deterministic last-resort guard, not the primary check.
- **No live child** (`iterCancel == nil`): `RestartFresh` publishes the rotation and returns; `cancel` is nil so it is skipped — no panic. The next spawn (first `Run` iteration, or post-backoff) reads the rotated id. ✔ AC4.
- **Setup failure after rotation**: covered by the `started`-gated `firstRun` flip above — retries keep `--session-id <newID>`.
- **Single-id-flag invariant**: `buildArgs` appends exactly one of `--session-id` / `--resume` per spawn, by construction. `nextSpawnID` returns exactly one id. No partial and no duplicated id flag is structurally possible on the spawn path. ✔ AC4/AC5(3).
- **Non-rotated sessions**: `sessionID` is seeded to `cfg.SessionID` and `rotatePending` starts `false`, so a runner that is never `RestartFresh`-ed behaves byte-for-byte as today — `--session-id` on spawn 1, `--resume <sameID>` on every respawn. ✔ AC3.

## Testing strategy

New tests in `internal/streamsup/runner_test.go` (stdlib `testing`, table/scenario style, `-race`). Reuse `helperRunCfg`, `runInBackground`, `spawnArgsRecorder`, and the `onSpawn` hook. Assert on argv via `spawnArgsRecorder.all()` (ordered per-spawn argv). Define a second session id constant alongside `testSessionID` (e.g. `rotatedSessionID`).

Recommend three scenarios (write as focused test functions; the developer chooses the exact idiom):

1. **Fresh restart establishes the new id, then crash-respawn resumes it** (covers AC5(1), AC5(2), AC5(3), and the load-bearing subtlety in one deterministic sequence).
   - Config: `crash` helper mode, tiny backoff, `log: slog.New(rec)`, `onSpawn` that on **exactly the first** spawn (guard with `sync.Once`) calls `r.RestartFresh(rotatedSessionID)`.
   - Run in background; wait until `rec.count() >= 3` (or a short deadline); cancel + join.
   - Assert on `rec.all()` in order:
     - spawn 1 argv contains `--session-id <testSessionID>`, no `--resume`.
     - the next distinct spawn contains `--session-id <rotatedSessionID>` (fresh establish of the new id), no `--resume`, and does **not** reference `testSessionID`.
     - a later spawn contains `--resume <rotatedSessionID>` — **never** `--resume <testSessionID>` (the regression the ticket warns about).
     - **no** spawn's argv contains both `--session-id` and `--resume` (no double-inject).
     - after the `--session-id <rotatedSessionID>` spawn, no argv references `testSessionID` at all.
   - Determinism note: `onSpawn` runs synchronously on the Run goroutine before `cmd.Wait`, so the rotation is published before spawn 1 exits regardless of whether the crash or the cancel wins the race — the argv sequence `[--session-id old, --session-id new, --resume new, --resume new…]` is stable.

2. **RestartFresh with no live child is safe** (covers AC4).
   - Config: `echo_lines` mode (child stays alive), `log: slog.New(rec)`.
   - Call `r.RestartFresh(rotatedSessionID)` **before** `Run` starts (no child exists). Then run in background; wait for the first spawn; cancel + join.
   - Assert: the first spawn's argv contains `--session-id <rotatedSessionID>` (the pre-Run rotation is honored), exactly one id flag, no panic (the test completing is the no-panic proof).

3. **Non-rotated session is unchanged** (covers AC3) — optionally fold into an assertion on an existing test, or a small standalone: a runner never `RestartFresh`-ed still produces `--session-id <testSessionID>` then `--resume <testSessionID>`. `TestRunner_ResumeIDStableAcrossRestart` already asserts exactly this for the un-rotated path; if it still passes untouched, AC3 is covered — call that out rather than duplicating it.

Also add a focused **empty-id guard** assertion (unit-level, no `Run`): construct a `Runner`, call `r.RestartFresh("")`, assert `r.nextSpawnID()` still returns the original id with `forceFirst == false` (the no-op held). This keeps the guard from silently rotting.

## Open questions

- **Method name.** `RestartFresh(newID string)` is the recommendation (pairs naturally with the existing `Restart(args)` and reads as "restart into a fresh session"). If #1125's routing code reads better with `RestartNew` or `RotateAndRestart`, the developer may rename — but keep the "restart" prefix so the `Restart`/`RestartFresh` pairing is discoverable. Non-blocking.
- **Empty-id failure mode.** Spec'd as Warn-and-no-op (deterministic guard, pool validates upstream). If the pipeline later wants `RestartFresh` to surface an error for observability, that is a signature change (`error` return) better decided when #1125 wires the caller — deferred, not needed for these ACs.
