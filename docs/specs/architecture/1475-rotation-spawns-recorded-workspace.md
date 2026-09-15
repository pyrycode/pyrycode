# #1475 — `new_session` rotation spawns the successor in the conversation's recorded workspace

## Files read

- `cmd/pyry/main.go` → `resolveSpawnDir`, `expandTilde`, `confineWorkdirToHomeCreating` — the one validator the mint and revive paths share; this ticket adds its third caller.
- `cmd/pyry/main.go` → `sessionRouter.revive` — the only production reader of `Conversation.Cwd` today, and the posture precedent (re-validate a persisted path at its own spawn site).
- `cmd/pyry/main.go` → `sessionMinter.Create` — the *opposite* posture (deferred spawn, deliberately no re-validation) and the `createConversationMintTimeout` precedent for naming a blocking-filesystem consequence honestly.
- `cmd/pyry/main.go` → `resolveBoundSession`, `activeSessionStarter`, `activeSessionStarter.StartNewSession`, `startFreshRunner`, `beginRotationOrNoop` — the rotation dispatch, its inert arms, and the load-bearing arm→rotate→respawn ordering.
- `internal/sessions/transition.go` → `RotateForNewSession` — the pool-side rotation, and where `refreshSystemPromptForRotation` sits relative to the respawn.
- `internal/sessions/systemprompt.go` → `refreshSystemPromptForRotation`, `refreshSystemPrompt` — the #2436 template (re-read stored state at rotation time) and the doc AC-5 names.
- `internal/streamsup/runner.go` → `Runner.workDir`, `Runner.restartMu` (charter), `New`, `SetSpawnArgs`, `setArgsLocked`, `RestartFresh`, `AdoptSessionID`, `beginSpawn`, `Run`, `spawnAndWait`, `useCreateForm`, `buildArgs` — the spawn-input snapshot discipline this change joins.
- `cmd/pyry/streamsup_runner.go` → `streamRunner`, `streamRunner.ClaudeSessionsDir`, `mapStreamsupConfig`, `streamClaudeSessionsDir`, `newStreamRunnerFactory` — where the workdir is mapped into the runner and where the transcript folder is derived from it.
- `cmd/pyry/session_transcript_dir.go` → `sessionTranscriptDir` — the second consumer of the workdir-derived transcript folder; its doc asserts a rotation cannot change it, which this ticket falsifies.
- `internal/relay/handlers/change_workspace.go` → `ChangeWorkspace` — the write side; where `conv.Cwd` acquires its `$HOME`-confined value.
- `internal/sessions/pool_spawndir_test.go` → `helperPoolSpawnDir` — the cwd-recorder pattern (a child writes a marker into its own working directory; the marker's *location* is the proof), which AC-1's "read the spawned process's own working directory" test copies.
- `internal/streamsup/helper_test.go` → `TestMain`, `helperChild` — the fake-claude child the streamsup tests spawn; AC-1's proof needs a new mode here.
- `docs/knowledge/features/sessions-package-key-types-reviving-a-dropped-session-pool-revive.md` — records that `spawnDir` is opaque pool-side, must not be logged, and that `sessionRouter.revive` re-runs `resolveSpawnDir` rather than trusting the recorded value. That last sentence is the posture this ticket inherits.
- `docs/knowledge/features/sessions-package-key-types-per-session-spawn-workdir-createin-getor.md` — why the workdir was never persisted session-side and why `Conversation.Cwd` is the durable home.

## Context

`change_workspace` records a `$HOME`-confined realpath on the conversation and replies `conversation_updated`, but the only production path that reads it back is `sessionRouter.revive`, reached on a `Pool.Lookup` miss after a daemon restart. So moving a conversation is a success reply that changes nothing until the daemon restarts. Four doc claims — `Conversation.Cwd`'s field doc, `ChangeWorkspace`'s doc and its `SECURITY` paragraph, and `docs/knowledge/codebase/823.md` — already promise the new folder reaches a spawn.

The decision recorded on the issue (2026-09-15) is option B: wire it, with a `new_session` rotation counting as a fresh spawn. `(*Pool).refreshSystemPromptForRotation` (#2436) is the template — it re-reads stored conversation state at rotation time so the successor comes up on what the operator saved — and this ticket does the same for the directory. Afterwards rotation and post-restart revive are the two fresh-spawn paths that read `Cwd`; an idle-evict re-activation is a *resume* and deliberately is not.

No ADR is warranted. This ticket adds no new boundary — it makes an existing, documented one true — and the posture it takes (re-validate at the spawn site) is already recorded under `conversation-session-binding.md § Restart scope` for the revive path.

## Design

### The shape of the change

`Runner.workDir` stops being a construction constant and becomes a spawn **input**, alongside `args`, `sessionID`, `spawnMode` and `freshSeq`: seeded in `New`, swapped by an exported swap-only method that takes `restartMu` exactly once, and snapshotted inside `beginSpawn`'s single section. The rotation wiring in `cmd/pyry` re-reads the bound conversation's `Cwd`, re-confines it with `resolveSpawnDir`, and installs the result between the pool-side rotation and `RestartFresh`.

### The transcript-folder coupling — why two fields swap, not one

`Config.ClaudeSessionsDir` is **derived from the workdir**: `mapStreamsupConfig` sets it to `streamClaudeSessionsDir(cfg.WorkDir)`, and `beginSpawn` hands it to `useCreateForm`, which decides `--session-id` vs `--resume` by probing for `<id>.jsonl` there. claude writes that transcript under the projects folder named for the directory it actually resolved.

Swapping `workDir` alone therefore wedges the successor. The rotation spawn itself is unaffected (`forceFirst` is set and the fresh id is absent from either folder), but the **next crash-respawn** probes the stale folder, finds the successor's transcript absent because it was written under the new folder, picks create form, and re-issues `--session-id` against a live transcript — which claude refuses (ADR 032). Because a supplied directory makes `useCreateForm` decide outright rather than latch, that is a permanent respawn loop, not one wasted spawn. `streamClaudeSessionsDir`'s own doc enumerates this exact failure for the mis-derived case.

So the runner gains a mutable `claudeSessionsDir` beside the mutable `workDir` — the same relationship `sessionID` has to `cfg.SessionID` and `spawnMode` to `cfg.SpawnPermissionMode` — and they are written in ONE `restartMu` section so no spawn can observe a directory and a transcript folder that name different places.

`streamRunner.claudeSessionsDir`, the adapter's stored copy that `sessionTranscriptDir` reads, is **removed** and its accessor delegates to the runner. Left stored it would report the pre-move folder after a rotation, re-introducing for moved conversations exactly the "Context: 0%" defect #2423 fixed. Delegating also strengthens what `sessionTranscriptDir`'s doc already asks for — the reader and the spawn probe read one field of one struct — by moving that one field onto the runner, where both now live.

### Key contracts

`internal/streamsup` (new exported methods on the existing `*Runner`; no new types):

```go
// SetSpawnWorkDir installs the directory the NEXT spawn chdirs into, together
// with the transcript folder that spawn's create/resume probe reads.
func (r *Runner) SetSpawnWorkDir(workDir, claudeSessionsDir string) error

// ClaudeSessionsDir reports the folder the live spawn inputs name.
func (r *Runner) ClaudeSessionsDir() string
```

`SetSpawnWorkDir` is `SetSpawnArgs`' sibling: one `restartMu` acquisition, writing only the two directory fields — never `sessionID`, `rotatePending`, `iterCancel` or `args` — so the #1481 single-acquisition property holds by construction and it cannot reach the state `beginSpawn`'s doc forbids. Against a racing `beginSpawn` it serialises wholly before (that spawn observes the swap) or wholly after (that spawn keeps the old directory and the next one takes the new); both are correct, because the contract is the NEXT spawn.

It runs `agentrun.ResolveWorkdir` **above** the lock, for two reasons. `restartMu` is a leaf — no I/O, no second lock — and `ResolveWorkdir` stats and resolves symlinks. And `New` applies it to `cfg.WorkDir`, so skipping it here would break `workDir`'s stated invariant ("resolved absolute path") and, worse, desynchronise the two fields: `streamClaudeSessionsDir` runs `ResolveWorkdir` on the same input, including the `canonicalCase` step `resolveSpawnDir` does not apply. A resolve failure returns an error and writes nothing — neither field moves.

An empty `workDir` is a no-op logged at Warn, the deterministic last-resort guard `RestartFresh`'s empty-id early return already models: the validating boundary is the caller, and the runner never chdirs into "".

`cmd/pyry` adapter:

```go
func (a streamRunner) SetSpawnWorkDir(workDir string) error  // derives via streamClaudeSessionsDir
func (a streamRunner) ClaudeSessionsDir() string             // delegates to a.r
```

The adapter owns the derivation because `mapStreamsupConfig` already does, in the same package, with the same helper — one place, so the two can never name different folders.

`cmd/pyry` dispatch:

```go
func startFreshRunner(r sessions.Runner, oldID sessions.SessionID, spawnDir string,
    rotate func(sessions.SessionID) (sessions.SessionID, error)) error
```

`spawnDir` is the already-resolved, already-confined directory, or `""` for "leave the runner where it is". `activeSessionStarter` gains two seams: `resolveBound` widens to hand back the conversation's recorded `Cwd`, and a new nil-tolerant `spawnDirFor func(recorded string) (string, error)` field (production: `resolveSpawnDir`) does the re-confinement. Nil-tolerant so the existing test literals stay valid and an unwired literal degrades to today's behaviour rather than panicking — the posture `logger()` already takes on this constructor-less struct.

### Data flow

```
new_session frame
  → V2SessionManager Run dispatch goroutine
  → activeSessionStarter.StartNewSession(convID)
      ├─ inert arms unchanged (no active conv / non-canonical id / no bound
      │  session / no RestartFresh / named-with-no-live-child) — NONE of them
      │  reaches the filesystem
      ├─ resolveBound → (runner, oldID, recordedCwd)
      ├─ spawnDirFor(recordedCwd)          ← re-confinement, filesystem I/O
      │     ""  → install nothing
      │     err → Warn + install nothing   ← AC-4
      └─ startFreshRunner(runner, oldID, spawnDir, rotate)
            ├─ abort := beginRotationOrNoop(r)     ← #1330 gate, armed first
            ├─ newID, err := rotate(oldID)         ← pool re-key, rebind,
            │     err → abort(); return err           prompt recompose, fan-out
            ├─ r.(interface{ SetSpawnWorkDir(string) error }).SetSpawnWorkDir(spawnDir)
            └─ v.RestartFresh(newID)               ← cancels the child; Run respawns
```

### Ordering — why the install sits between `rotate` and `RestartFresh`

Same window `refreshSystemPromptForRotation` occupies, for the same two reasons. **After `rotate`**, because a failed rotation must leave the runner untouched: installed before, a rotate error would leave the directory swapped and the *next crash-respawn* would silently move a child no rotation ever replaced — which is precisely the "`change_workspace` alone does not move a running child" promise AC-2 makes. **Before `RestartFresh`**, because `RestartFresh` cancels the live child at once and the Run loop answers on its own goroutine; an install landing after that races the successor's `beginSpawn` and the loser comes up in the pre-move directory.

The resolve itself happens **before** the gate is armed, outside `startFreshRunner`. `resolveSpawnDir` does filesystem I/O; doing it inside the arm→rotate→respawn sequence would hold the #1330 gate across it, and every `WriteUserTurn` on the conversation returns `ErrNoLiveChild` while it is armed.

### What does NOT change

`Pool.Revive`'s path, the idle-evict re-activation (a resume: the runner keeps the directory it has), the live child (never moved), `Conversation.Cwd`'s field doc and `ChangeWorkspace`'s doc (this ticket makes them true as written), and the session's persisted state — the spawn directory stays a spawn-time input, never written to `sessions.json`, exactly as #684/#1487 settled.

## Concurrency model

No new goroutine, no new channel, no new mutex, and no new edge in the daemon's lock order.

- **`restartMu`**: gains two fields (`workDir`, `claudeSessionsDir`) and one writer (`SetSpawnWorkDir`, one acquisition, no call-out, no I/O inside). `beginSpawn` snapshots both in the section it already takes — a read, not a second acquisition, so the one-acquisition-per-spawn-setup charter (#1481) is unchanged. `ClaudeSessionsDir()` takes `restartMu` alone, like `liveSessionID`.
- **Run goroutine**: `Run`'s `"spawning claude"` log and `spawnAndWait`'s `cmd.Dir` both move from `r.workDir` (a live field read, which becomes a data race the moment the field is mutable) to `beginSpawn`'s snapshot, threaded through as a parameter.
- **Return-list ordering** is deliberate: `beginSpawn` returns `(iterCtx, cancel, args, env []string, workDir string, forceFirst bool, freshSeq uint64, spawnMode string)` and `spawnAndWait` takes `(ctx, args, env []string, workDir string, freshSeq uint64, spawnMode string)`. In neither list are two same-typed parameters adjacent, so a transposed `workDir`/`spawnMode` is a compile error rather than a spawn in the wrong directory — the reasoning `boundRunSettings`' doc records for named-field wiring.
- **Dispatch goroutine**: `resolveSpawnDir` and `streamClaudeSessionsDir` run on `V2SessionManager`'s single `Run` dispatch goroutine, synchronously. See Error handling.
- **`sessionTranscriptDir`**: now does `Pool.Lookup` then a `restartMu` read, sequentially, never nested — no new lock-order edge. Its documented TOCTOU widens (see Error handling).

## Error handling

| Failure | Behaviour |
|---|---|
| Recorded `Cwd` is `""` | `resolveSpawnDir` returns `("", nil)`. Nothing installed, no log — an unset workspace is not a refusal. Rotation completes; successor keeps the runner's directory. |
| Recorded `Cwd` refused (deleted, re-pointed outside `$HOME`, unexpandable `~`, trust-mark write failure) | Warn with `event=v2.new_session.spawn_dir_rejected` and `conversation_id`; **no path and no wrapped error text in the record** (see Security review). Nothing installed. Rotation completes; successor keeps the runner's directory. **AC-4.** |
| `ResolveWorkdir` fails inside `SetSpawnWorkDir` (the directory vanished between the confinement and the install) | Returns an error, writes neither field. `startFreshRunner` Warns and continues to `RestartFresh` — the rotation is already committed and the child must still come up. Fail-closed: the old directory, never an unconfined one. |
| `rotate` fails | Unchanged: `abort()`, return the error. No directory install — the swap sits below the error return. |
| Wedged filesystem | **Decided and recorded, not defended against.** `resolveSpawnDir` (realpath, `MkdirAll`, a `~/.claude.json` write via `trustMark`) and `streamClaudeSessionsDir` (stat, `EvalSymlinks`, `$HOME`) block in syscalls on `V2SessionManager`'s single `Run` dispatch goroutine, stalling every inbound frame for as long as the filesystem is wedged. No timeout is added: a deadline cannot interrupt a syscall, so one would claim a protection it does not provide. This is `createConversationMintTimeout`'s precedent applied verbatim, and the same exposure `sessionMinter.Create` already carries on the neighbouring mint path — reached there by `create_conversation` and here by `new_session`, both remotely driven. Narrowed by placement: the resolve sits below every inert arm, so only a frame that will actually rotate pays it. |
| Empty `workDir` reaches `SetSpawnWorkDir` | Warn, no-op, nil error. Unreachable from this dispatch (the `""` case never calls it); the guard upholds `New`'s non-empty contract deterministically, as `RestartFresh`'s empty-id guard does. |
| Runner without `SetSpawnWorkDir` | Optional capability assertion, inert — the runner rotates, just without moving. `beginRotationOrNoop`'s precedent: a second capability is optional rather than widening the first assertion, so a runner that can `RestartFresh` but cannot move still rotates instead of going wholly inert. Both existing probes (`startFreshRunner`'s and `StartNewSession`'s) keep asserting `RestartFresh` **only**, so the inert-arm log keeps matching the dispatch. |

## Testing strategy

`internal/streamsup` (`runner_workdir_test.go`, new file; plus a `record_cwd_block` mode in `helper_test.go`'s `helperChild` that writes a relative marker file containing `os.Getwd()`):

- **The successor spawns in the installed directory — read from the child itself.** Run a runner in dir A, `SetSpawnWorkDir(B)`, `RestartFresh(newID)`, then assert the marker lands **in B**. The marker's *location* is the proof, which is symlink-rewrite-immune on macOS the way `helperPoolSpawnDir` documents. Covers AC-1's "reads the spawned process's own working directory, not a config field" at the runner level.
- **No install, no move**: `RestartFresh` alone leaves the successor's marker in A. Covers AC-4's "leaves the successor in the directory the runner already had".
- **A live child is not moved**: `SetSpawnWorkDir(B)` against a running child leaves that child's marker in A and produces no second marker until the respawn. AC-2 at the runner level.
- **`ClaudeSessionsDir` reports the installed folder**, and a spawn after the install probes it — asserted through the create/resume form the second spawn's argv carries (`record_block`'s argv file), which is what proves the wedge in § Design cannot happen.
- **Rejected input**: a non-existent directory returns an error and moves neither field (the next spawn still lands in A); `""` is a no-op.

`cmd/pyry` (`rotation_spawndir_test.go`, new file) — dispatch-level, with a recording fake runner, mirroring `dispatch_arms_test.go`:

- Ordering: the install is observed **after** `rotate` returned and **before** `RestartFresh`, recorded as one sequence.
- `rotate` error → `abort()` fired, **no** install.
- Refused recorded workspace → no install, rotation still completes, `RestartFresh` still called, and the Warn record carries `conversation_id` and no path. AC-4.
- Empty recorded workspace → no install, no record, rotation completes.
- Each inert arm of `StartNewSession` → the `spawnDirFor` seam is **never called** (counted by the double), so an inert frame does no filesystem I/O.
- A runner exposing `RestartFresh` but not `SetSpawnWorkDir` still rotates.

Existing coverage that must stay green unchanged: `dispatch_arms_test.go`, `inbound_deliver_rotation_test.go` (the #1330 gate ordering), `conversation_spawndir_test.go` (`resolveSpawnDir` itself is untouched).

## Security review

### Posture

This path **re-validates at the spawn site**, taking `sessionRouter.revive`'s posture and explicitly not `sessionMinter.Create`'s.

`sessionMinter.Create` may defer without re-validation because `resolveSpawnDir` returns `trustMark`'s realpath and *that value is frozen onto the session at build time* — no phone-influenced state is re-read between the check and the chdir. That argument does not hold here. `conv.Cwd` is raw persisted bytes in a mutable file, written by `ChangeWorkspace` at an arbitrary earlier moment and re-read at spawn time, possibly across a daemon restart — the identical situation `revive`'s doc describes, where "a path valid then can be turned into an escape before the restart, and this is the spawn site that would otherwise believe the stale check". So `resolveSpawnDir` runs again, at this spawn site, on every rotation.

### Trust boundaries

- **`conversation_id`** (untrusted, client-supplied) — unchanged by this ticket. `activeSessionStarter` remains its trust boundary: `conversations.ValidID` before the registry is touched, and refusals logged at DEBUG through `boundedConvID`.
- **`conv.Cwd`** (untrusted, network-influenced via `change_workspace`) — validated by `resolveSpawnDir`, which is the sole door and is not modified. Confinement: `expandTilde` → `confineWorkdirToHomeCreating` (symlink-resolved, `$HOME`-contained, checked before *and* after creation) → `trustMark`. A value that fails any stage never reaches `SetSpawnWorkDir`.
- **Fail-closed on refusal**: a refused workspace installs **nothing**, leaving the runner's existing confined directory. There is no path on which a refusal, an error, or an empty value produces a spawn in an unconfined directory — the only writer of `workDir` after construction is `SetSpawnWorkDir`, and the only production caller passes `resolveSpawnDir`'s success value.
- **Second-order confinement**: `SetSpawnWorkDir` re-runs `agentrun.ResolveWorkdir` on the confined path. That is canonicalisation, not a weakening — it cannot move a path out of `$HOME`, because `confineWorkdirToHomeCreating` has already symlink-resolved every ancestor and re-checked containment after creation.

### Findings

- **MUST FIX — do not log the path or the wrapped error.** `resolveSpawnDir`'s error wraps `confineWorkdirToHomeCreating`'s detail, which names the resolved path and the `$HOME` boundary. `Pool.Revive`'s documented contract is that a phone-influenced workspace path must not be logged, and `sessionTranscriptDir`'s `SECURITY` paragraph closes the same channel (#833). AC-4 asks for a structured record carrying the conversation id — that is exactly what it gets: `event` + `conversation_id`, and nothing else. *Resolved in § Error handling.* (`channelCreator` logs `err` on the neighbouring path; that is a control-plane verb with an operator-local caller, not this remotely-driven one, and is not a licence to widen here.)
- **MUST FIX — the install must not precede the rotation.** Installed before `rotate`, a failed rotation leaves the directory swapped and the next *crash*-respawn moves a child that was never replaced — a silent violation of AC-2 reachable from a remotely-driven frame that merely lost a race. *Resolved in § Design ordering: the install sits below the error return.*
- **SHOULD FIX — no filesystem I/O on an inert frame.** The resolve creates a directory and writes `~/.claude.json`. Placing it above the inert arms would let a frame naming a conversation with no live child drive `MkdirAll` on the daemon's behalf. *Resolved: the resolve sits below every inert arm; asserted by the seam-never-called test.*
- **SHOULD FIX — the transcript folder must move with the directory.** Beyond the respawn wedge in § Design, a stale folder means the context-usage reader stats the pre-move projects directory, reporting zero used tokens — the #2423 defect, re-introduced for exactly the conversations this ticket exists for. *Resolved: one `restartMu` section writes both fields; the adapter's stored copy is removed in favour of delegation.*
- **ACCEPTED — the dispatch-goroutine stall.** A wedged filesystem blocks every inbound frame. Named honestly rather than defended with a deadline that cannot interrupt a syscall; the same exposure the mint path already carries. See § Error handling.
- **ACCEPTED — widened TOCTOU on `sessionTranscriptDir`.** Its doc currently reasons that a rotation landing in the `Lookup` → `ClaudeSessionsDir` window keeps the working directory, so either reading names the same folder. That stops being true. The residual is a single context-usage reply computed against one of two real folders for one session; the next reply is correct. The doc is corrected rather than the window closed — closing it would mean holding `Pool.mu` across a runner lock, a new lock-order edge for a benign stale read.
- **OUT OF SCOPE — reporting a refused workspace back to the client.** That is #2443, blocked by this ticket. The refusal here is a daemon-side log record only; the rotation still succeeds and the client is told nothing new.
- **OUT OF SCOPE — idle-evict re-activation and live-child moves.** Explicitly excluded by the ticket; a re-activation is a resume and keeps the runner's directory.

**Verdict: PASS.**

## Sizing — the ceiling is exceeded deliberately

Re-counted against this written plan:

| Boundary | Limit | This ticket |
|---|---|---|
| Production source files | ≤ 5 | **5** — `internal/streamsup/runner.go`, `cmd/pyry/streamsup_runner.go`, `cmd/pyry/main.go`, `internal/sessions/systemprompt.go` (doc), `cmd/pyry/session_transcript_dir.go` (doc) |
| Total written work | ≤ 800 | **~960 — EXCEEDED** |
| New exported types/interfaces | ≤ 5 | 0 (two methods on an existing type) |
| Consumer call sites | ≤ 10 | ~9 |
| Acceptance criteria | ≤ 5 | 5 |
| Reject branches | ≤ 10 | 2 |

One line trips. The floor rule overrides it: the only seam a split could cut is the runner-side `SetSpawnWorkDir` / `ClaudeSessionsDir` pair, and its sole consumer anywhere in the repo is the rotation wiring in the same ticket — a child nothing outside the family calls, which cannot be verified on its own. The floor protects against an unverifiable ticket, which no resume fixes; the ceiling protects against a budget miss, which costs one continuation leg. The refiner recorded the same conclusion on the issue (estimate ~900 over 4 production files; nearest analogue #2436 at 1042 lines over 6 files). The fifth file is `session_transcript_dir.go`, a doc-only correction this plan's security review identifies and the refiner's estimate did not anticipate.

## Open questions

1. **Does `SetSpawnWorkDir` need to guard against a directory that vanishes between `resolveSpawnDir` and the install?** Planned answer: no extra guard — `agentrun.ResolveWorkdir` already stats and returns an error, and the error path writes neither field. Confirm during implementation that `ResolveWorkdir` really does reject a non-existent directory (`New`'s `TestNew_WorkDirMustExist` suggests it does) rather than returning a path that fails later at `cmd.Start`.
2. **Does removing `streamRunner.claudeSessionsDir` break any construction site?** Seven `streamRunner{` literals exist; only `newStreamRunnerFactory` appeared to set the field. Confirm by compile; if a test literal sets it, the literal drops the field rather than the field being kept.
3. **Should the rejection Warn fire on `SetSpawnWorkDir`'s own error as well as on `resolveSpawnDir`'s?** Planned answer: yes, as a distinct event — the two are different failures (refused by confinement vs. vanished before install) and collapsing them would make the record ambiguous.

Each is resolved in Phase B; anything that changes the design above is recorded in a `## Revisions` entry.
