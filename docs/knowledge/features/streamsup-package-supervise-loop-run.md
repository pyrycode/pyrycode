# Supervise loop (`Run`)

Mirrors `supervisor.Run`'s restart/backoff/resume shape, including the live-restart (`restartCh`/`iterCtx`) seam since #1097 (see "Satisfying `sessions.Runner`" below):

```
loop:
  if ctx.Err() != nil → return ctx.Err()               // graceful shutdown, not a crash
  iterCtx, cancel, args, forceFirst := beginSpawn(ctx, firstRun)  // ONE restartMu section: reads the
                                                        //   Restart-swapped argv AND the (possibly
                                                        //   rotated) id, consumes rotatePending,
                                                        //   builds the argv, publishes iterCancel
  if forceFirst → firstRun = true                       // a RestartFresh was consumed: re-arm first-run form
  log "spawning claude"                                 // AFTER the section: no log I/O under a leaf mutex
  started, waitErr := spawnAndWait(iterCtx, args)       // blocks until child exits or a restart cancels iterCtx
  cancel(); clearIterCancel()
  if ctx.Err() != nil → return ctx.Err()                // parent-ctx cancel = teardown, not a crash
  if started → firstRun = false                         // see the firstRun gate below
  if drainRestart() → continue                          // deliberate restart, not a crash: skip backoff
  delay := backoff.next(uptime)
  select { <-time.After(delay) | <-ctx.Done() → return ctx.Err() | <-restartCh → relaunch now }
```

**The single `beginSpawn` acquisition is load-bearing (#1481), not tidiness.** Reading the spawn inputs and publishing `iterCancel` are one `restartMu` section, so a racing `Restart`/`RestartFresh` — which takes that mutex exactly once — is serialised either *wholly before* it (the spawn being set up observes the swapped argv / rotated id) or *wholly after* it (it finds the just-published cancel and tears that spawn down, and `drainRestart` relaunches immediately under the new state). There is no third position, so "a live child under a pre-rotation id **and** no live iteration cancel" is unreachable. Until #1481 this was two sections with a `buildArgs` allocation and a synchronous log write between them, and `iterCancel` was still `nil` from the previous iteration across that gap: a racer landing there wrote its rotation, cancelled **nothing**, and the spawn launched a child under the pre-rotation id for that child's whole lifetime — after which the still-set `rotatePending` made the next crash-respawn `--session-id <newID>`, a fresh transcript, silently discarding every turn since the rotation. `buildArgs` had to move inside the section because `restartMu` is not reentrant (the old `liveArgs()`/`nextSpawnID()` accessors each took it, so a fused section could not call them); both are deleted, and the publish side is narrowed to a no-argument `clearIterCancel` so nothing outside `beginSpawn` can express a publish at all.

Shutdown is detected via **parent-ctx cancellation**, never via the child-exit error value — `spawnAndWait`'s `waitErr` only ever means "crashed" once `ctx.Err()` has been checked and is nil. An `iterCtx`-only cancel (from `Restart`) leaves the parent `ctx.Err()` nil, so the loop falls through and relaunches instead of returning. One goroutine total (the caller's `Run`); `cmd.Wait` blocks it, and os/exec runs its own internal ctx-watcher goroutine that invokes `cmd.Cancel` off-loop — on either a parent-ctx cancel (shutdown) or an `iterCtx` cancel (restart).

### `firstRun` gate: only advances on a successful `cmd.Start` (fix 66cc50e)

`spawnAndWait` returns `(started bool, waitErr error)`. `started` is `false` only when the spawn fails during **setup** (`cmd.StdinPipe()` or `cmd.Start()` erroring) — claude never launched, so `--session-id` never ran and the on-disk session was never established. The original implementation flipped `firstRun = false` unconditionally after every iteration; a transient setup failure (e.g. a momentarily-unavailable binary) would make the *next* attempt respawn with `--resume <id>` against a session that was never created, and claude would error ("no conversation found") on every subsequent attempt — a permanent, unrecoverable crash-loop that defeated the very retry the backoff loop exists for. Fixed by threading `started` through and gating the flip: `if started { firstRun = false }`. A setup failure now correctly retries with `--session-id` until one succeeds. Regression test drives `Run` against a non-existent binary and asserts every retry keeps `--session-id`.

**A second, distinct crash-loop shape existed here: `started == true` does not mean a transcript exists.** The gate above only protects against claude never launching; on its own it does nothing for a claude that launches, is torn down before running a turn, and is respawned with `--resume <id>` against an id that was never written to disk. [#1655](session-transcript-and-resume-probe.md) measured against a live claude (2.1.220) that this second premise **HOLDS** — a `--session-id` launch with no turn leaves no `<id>.jsonl`, both while the child is alive and after a graceful `SIGTERM` exit. [ADR 032](../decisions/032-bootstrap-resume-per-spawn-existence-probe.md)'s by-id-existence rule (already applied to the PTY bootstrap path) is the fix; **#1630** carried it into this package as `useCreateForm` (above), inert until **#1631** armed it on the production path — see `mapStreamsupConfig` / `streamClaudeSessionsDir` below.
