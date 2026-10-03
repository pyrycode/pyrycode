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

### `claude exited` record: session id and a capped stderr tail (#2723)

Both forms of the record now carry `session=<id>` — the id *this* child was spawned with, not necessarily the live one `AdoptSessionID` can move without a respawn. The runner's logger is pool-wide and untagged, so without this field two crash-looping conversations are indistinguishable in the log; it is what tells them apart.

When the child exited on its own with a non-zero status, the Warn form also carries `stderr=<tail>`: at most the last 5 lines and 1024 bytes of what it wrote to stderr, keeping the end — where claude prints the reason it's exiting, not the start. `spawnAndWait` returns a non-empty tail only on that path: a deliberate kill (`Restart`, `RestartFresh`, or shutdown) has already cancelled the iteration ctx by the time `cmd.Wait` returns, and that ctx is checked before the tail is handed back — before `drainRestart` ever runs — so the clean-exit, deliberate-exit, and crash branches can't be confused with each other. A clean exit (status 0) carries no tail either.

The tail is captured regardless of whether `Config.Stderr` also forwards the child's full stderr elsewhere (tests, live probes) — the capture keeps its own bounded copy either way. On the production path (`Config.Stderr` nil), the child's stderr now goes to the write end of a private `os.Pipe` instead of straight to `/dev/null`; a reader goroutine drains it into the tail and is given a 250ms grace period after `cmd.Wait` returns before the read end is force-closed, for the case where a descendant of claude still holds the write end.

**Why not let `exec` own that pipe, the obvious one-liner:** setting `cmd.Stderr` to anything other than an `*os.File` makes `cmd.Wait` block until every holder of the write end closes, up to `WaitDelay` (5s), and turns a holder lingering past a clean exit into `exec.ErrWaitDelay` instead of nil. claude's own stdio MCP children — including pyry's own `pyry_files` — can inherit its stderr, so that one-liner would have changed how a clean exit is told apart from a crash, and could have slowed every respawn by however long such a child takes to close the handle. Handing the child an `*os.File` pipe end directly, with no exec-owned copier, keeps `Wait`'s old behaviour: it still never waits on anything but claude's own exit.

The tail rides as a `daemonLogOnly` attribute value — never the message, never a key — so `control.SlogTee` lets it reach the daemon's own log output but drops it from the ring that feeds `pyry logs` and the debug bundle; see [control-plane.md § Keeping a value out of the log ring](control-plane.md#keeping-a-value-out-of-the-log-ring-logdaemononly-2723). `MarshalText` routes it through slog's text-quoting path, so the untrusted bytes can't forge a log line or a field.
