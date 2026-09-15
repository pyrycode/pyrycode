# Satisfying `sessions.Runner` (#1097)

`*streamsup.Runner` gained the four methods [`internal/sessions.Runner`](sessions-package.md) requires
beyond `Run`/`Stdin`, so a stream-json session can be driven through the exact seam `*supervisor.Supervisor`
already satisfies (`internal/sessions/runner.go`, introduced by #1077). A fifth,
`SetSpawnPermissionMode(mode string)`, joined it by #2064 — see [Posture
gate](streamsup-package-posture-gate-spawn-permission-mode-ack.md). `send_message` (→
`Session.WriteUserTurn` → `sup.WriteUserTurn`) and `set_session_settings` (→ `Pool.UpdateSettings` →
`sup.Restart`) work unchanged the moment a `streamRunner` exists — no new dispatch wiring, because both
paths already call through the interface rather than the concrete type.

**The covariant snag.** `sessions.Runner.State()` returns `supervisor.State`, but this package's dependency
direction (above) forbids importing `internal/supervisor`. Go has no covariant return on interface
satisfaction, so `*streamsup.Runner` cannot declare that signature directly. Resolved at the seam, not by
relaxing the interface: `*streamsup.Runner` gets a **native** `State() streamsup.State` (new `state.go`,
mirroring `supervisor.State`/`Phase` field-for-field and string-for-string), and a thin adapter —
`cmd/pyry/streamsup_runner.go`'s `streamRunner{ r *streamsup.Runner }` — maps `streamsup.State →
supervisor.State` via `mapStreamState` (`Phase` converts by a plain string cast, the six other fields copy
through) and forwards `WriteUserTurn`/`WaitForPTY`/`Run`/`Restart` unchanged. `streamRunner`, not the
concrete `*streamsup.Runner`, is what satisfies `sessions.Runner`; `var _ sessions.Runner = streamRunner{}`
is the compile-time proof. Same shape as `poolResolver` (`cmd/pyry/main.go`) and the pattern documented in
`docs/lessons.md` § "Interface adapters for covariant returns". The factory that *constructs* a
`streamRunner` from a `supervisor.Config` — `streamRunnerFactory`, in the same file — is #1109 (below); the
`interactive_runner` selection that injects it on `sessions.Config.RunnerFactory` is #1081 (shipped —
see below).

**`State`/`Phase` (`state.go`).** `Phase` is one of `starting`/`running`/`backoff`/`stopped`. `State{Phase,
ChildPID, StartedAt, RestartCount, LastUptime, NextBackoff}` — all six fields are kept faithful because
`cmd/pyry`'s status builder (`buildStatus(supervisor.State)`) reads all six, not just `Phase`. A leaf
`stateMu` (separate from the existing `mu` guarding `stdin`) guards `state`; `updateState(fn)` is called
only by the `Run` goroutine, `State()` is safe from any goroutine. The `Run` loop instruments it exactly
where `supervisor.Run` does: `Starting` at top (once), `Running` + `ChildPID` on spawn, `Backoff` +
`RestartCount++`/`LastUptime`/`NextBackoff` before the crash-path backoff wait (never on a deliberate
restart), `Stopped` in a top-level `defer`.

**Live-restart seam** (previously absent — the old `runner.go` doc explicitly called this out as a gap;
`Run` now has it). A third leaf mutex `restartMu` guards `args` (the live spawn base argv — assigned in
exactly one place, `setArgsLocked`, called by both `Restart` and `SetSpawnArgs`), `iterCancel` (the
current spawn iteration's `context.CancelFunc`), and — since #1124 — the `sessionID`/`rotatePending` pair
`RestartFresh` rotates (see "Fresh-restart under a new id" below). Since #2136, `sessionID` alone has a
second writer, `AdoptSessionID` (below) — the two are not variants of each other: one is a daemon-ordered
fresh restart, the other follows a reset claude already made in-process. Since #1481 `args` and the
`sessionID`/`rotatePending` pair are read, and `iterCancel` is published, by `beginSpawn` in a **single**
section per iteration; `clearIterCancel` drops the cancel once the iteration ends. That teardown accessor
takes no argument deliberately — publishing a non-`nil` cancel outside `beginSpawn`'s section is precisely
the #1481 defect, so the API cannot express it, and a future re-split has to add the parameter back before
it can reintroduce the window. `Restart(args []string)` installs through `setArgsLocked` inside its
existing single `restartMu` section, sends a non-blocking hint on a buffered(1) `restartCh` (coalesces
rapid restarts to one relaunch with the newest args), and cancels the current `iterCancel` — since #1481
that cancel goes live as soon as an iteration's spawn setup runs, before its child necessarily exists, so
a racing `Restart` can also catch a not-yet-launched iteration: the cancel fails that iteration's
`cmd.Start`, and the immediate relaunch that follows observes the swapped `args` — mirroring
`supervisor.Restart` byte-for-byte in shape. `Restart` touches only `restartMu`/`restartCh`/`iterCancel`,
never a `Pool`/`Session` lock, so `Pool.UpdateSettings` can call it after releasing `Pool.mu` with no
lock-order concern. Because `firstRun` is already `false` after the first successful spawn, a plain
restart always respawns via `--resume <sessionID>` — the conversation resumes rather than forking;
`RestartFresh` is the one path that re-arms `firstRun` to force a fresh `--session-id` spawn instead.
(With `Config.ClaudeSessionsDir` set — #1631's production path, not #1630's — `useCreateForm` can
override this too: a `Restart` of a session that launched but never established a transcript reads
absent and creates rather than resumes, regardless of `firstRun`.)

**`SetSpawnArgs(args []string)` — the swap without the kill (#1580).** `Restart` fuses two operations:
installing the next spawn's argv, and ending the live child so `Run` relaunches under it. `SetSpawnArgs`
is the first half alone — it calls `setArgsLocked` under one `restartMu` acquisition and returns, sending
no `restartCh` hint and never touching `iterCancel`, so a running child is left alone and the swap lands
on whichever spawn comes next (a crash-respawn, or an evict → `Activate`). It is the third racer
`beginSpawn`'s single-acquisition doc enumerates, and the weakest: one acquisition like the other two, and
it writes `args` only, so it cannot reach the forbidden state (a live child under a pre-rotation id with
no live iteration cancel) — that state is defined over `sessionID`/`rotatePending`/`iterCancel`, none of
which it touches. Declining to call `Restart` is not a substitute for calling this: it loses the swap
outright, and the next spawn silently re-execs the stale argv. The argv is installed **verbatim** — no
validation, no shaping; that stays upstream in `Session.spawnArgs` (`claudeSettingsArgs` enforces the YOLO
fail-safe there), and construction-time shaping (`stripSessionIDFlags`, `withApprovalArgs`, both
construction-only) is **not** reapplied on this or any post-construction install path. It is on
`sessions.Runner` (unlike `Interrupt`/`RestartFresh`/`BeginRotation`, which stay off it and are reached by
capability type-assertion) because there is exactly one production implementation — `streamRunner` — plus
five test doubles, all in this repo, so widening is compile-checked across the whole set. Its one
production caller is `Pool.UpdateSettings`' in-band branch (#1581), which installs the recomposed argv
through it and then delivers the change as a `/model` / `/effort` command instead of respawning. See
[codebase/1580.md](../codebase/1580.md).

**`AdoptSessionID(newID string)` — `SetSpawnArgs`' mirror image (#2136).** Installs `newID` as the id the
runner's *next* spawn resumes, and does nothing else: one `restartMu` acquisition, writing `sessionID`
alone — the first field of `RestartFresh`'s trio, touching neither `rotatePending` nor `freshSeq` nor
`iterCancel`, and sending no `restartCh` hint. It exists because claude can reset a conversation
in-process (`/clear`): `sessionResetFollower` ([announced reset follower](streamsup-package-announced-reset-follower.md))
moves the pool registry and the sink tag onto the announced id, and without a runner-side counterpart the
runner keeps the pre-reset id and its next crash-respawn `--resume`s the conversation the operator just
cleared. It is not `RestartFresh` with the teardown removed: claude has already reset in-process, so there
is no fresh transcript to arm and no child to relaunch — arming `rotatePending` would make the next spawn
emit `--session-id` against a transcript that already exists, which claude refuses (ADR 032).

An empty `newID` is refused at Warn, the same last-resort guard `RestartFresh` carries on `New`'s
non-empty contract — but note the guard is *borrowed* here, not the reasoning behind it: `RestartFresh`'s
callers hand it a daemon-minted id, while `AdoptSessionID`'s caller hands it a value claude wrote. What
makes the guard sufficient rather than a validator is a different upstream check — `emitConversationReset`
gates on `transcript.ValidStem`, an anchored full match over a canonical UUID stem — and that had to be
verified against this caller specifically rather than inherited from the sibling method. A guard copied
from a sibling is only as sound as its new caller's provenance, and that is worth re-checking every time,
not assumed from the shape matching.

Testing note: an argv assertion that the *next* spawn resumes the adopted id only discriminates if the
fixture directory holds a transcript for the adopted id specifically. `useCreateForm` also answers
`--resume` when `Config.ClaudeSessionsDir` is empty or via the `firstRun` latch — routes that produce the
same argv without the adoption ever having run. Stage the transcript for the id under test, not the
previous one, or the assertion rides the wrong route and stays green on a runner that never adopted.

**`SetSpawnWorkDir(workDir, claudeSessionsDir string) error` / `ClaudeSessionsDir() string` — the
directory pair as a spawn input (#1475).** `workDir` stopped being a construction constant read live off
the `Runner` and joined `args`/`sessionID`/`spawnMode` as a snapshot-only field: seeded in `New`, swapped
by `SetSpawnWorkDir` under one `restartMu` acquisition, and read only through `beginSpawn`'s single
snapshot (threaded to `Run`'s log and `spawnAndWait`'s `cmd.Dir` as a parameter, never a live field read).
It is `SetSpawnArgs`' sibling — writes only the directory pair, touches neither `sessionID` nor
`rotatePending` nor `iterCancel`, so it cannot reach the state `beginSpawn`'s single-acquisition doc
forbids, and a running child is left alone until whichever spawn comes next (a crash-respawn, or the
`RestartFresh` its one production caller pairs it with). `claudeSessionsDir` — the folder `useCreateForm`
probes to pick `--session-id` vs `--resume` — moves in the **same call and the same lock section** as
`workDir`, never separately: left behind, the successor's next crash-respawn would probe the pre-move
folder, find no transcript there (claude wrote it under the new one), choose create form, and re-issue
`--session-id` against a transcript that already exists — which claude refuses (ADR 032) — a **permanent**
respawn loop rather than one wasted spawn, because a non-empty `ClaudeSessionsDir` makes `useCreateForm`
decide outright instead of latching on `firstRun`.

`agentrun.ResolveWorkdir` runs **above** the lock on every install — `restartMu` is a leaf, and
`ResolveWorkdir` stats the path and resolves symlinks — so a resolve failure writes **neither** field:
fail-closed on the directory the runner already had, never half-applied. An empty `workDir` is a
Warn-logged no-op, the same last-resort guard `RestartFresh`'s empty-id check already models.
`ClaudeSessionsDir()` reads the live field under `restartMu` alone, the same treatment `liveSessionID`
gets.

`cmd/pyry`'s `streamRunner` adapter derives the transcript folder the same way `mapStreamsupConfig` does —
`streamClaudeSessionsDir`, one helper, two call sites, so construction and swap can never name different
folders for one workdir — and its own stored `claudeSessionsDir` copy was **removed** in favour of
forwarding to this method: a copy taken at construction would keep answering the pre-move folder after a
rotation, reopening the #2423 "Context: 0%" defect (see
[contextwindow-package.md](contextwindow-package.md)) for exactly the conversations that moved. See [the
`new_session` seam's workspace
re-read](v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md#workspace-re-read-on-rotation-1475)
for the rotation-side caller, ordering and security posture.

**`WriteUserTurn`/`WaitForPTY`.** `WriteUserTurn(ctx, conversationID, payload)` is a one-line wrap of the
already-reviewed `WriteTurn` free function (#1088/#1093) — no new envelope construction, and it inherits
`WriteTurn`'s exact contract (`ErrNoLiveChild` with no live child, `turncommit.ErrDropped` with zero bytes
on a gate deny). `conversationID` is accepted only for interface conformance and future outbound-cursor
wiring (T4/T7); this slice does not track a cursor. `WaitForPTY(ctx) error` is a bare `return nil` — the
stream path has no PTY to wait for, and the no-live-child window is already handled per-turn by
`WriteTurn`'s retryable `ErrNoLiveChild`.

Concurrency model: three **leaf** mutexes on `Runner` (`mu`, `stateMu`, `restartMu`), never nested, each
owned by a different goroutine/concern. See [codebase/1097.md](../codebase/1097.md).

**Testing trap: moving a field off `Config` onto the live `Runner` silently strands any test that builds
a `&Runner{}` literal directly (#1475).** `beginSpawn` reads `useCreateForm`'s directory from the live
`claudeSessionsDir` field now, not `cfg.ClaudeSessionsDir` — but a test that constructs a `Runner` by hand
and sets only the `cfg` half compiles fine and silently probes against `""`, latching on `firstRun`
instead of exercising the create/resume decision it meant to test. This package's convention (already
followed for `cfg.SessionID`/`sessionID`) is to set **both** halves of a config/live pair in a hand-built
literal, but nothing in the type system enforces it. Grep for `&Runner{` before moving any further field
out of `cfg` and onto a live counterpart.
