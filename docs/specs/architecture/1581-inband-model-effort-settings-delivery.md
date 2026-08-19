# 1581 — Deliver a model/effort-only settings change in-band instead of restarting the child

**Ticket:** [#1581](https://github.com/pyrycode/pyrycode/issues/1581) · **Size:** S · **Labels:** `bug`, `security-sensitive`

---

## Files to read first

Symbols, not lines — resolve each with `codegraph_search` / `codegraph_node`, then Read the enclosing declaration.

| File | Symbol | What to extract |
|---|---|---|
| `internal/sessions/pool.go` | `Pool.UpdateSettings` | The whole function **and its doc**. The tail (recompose argv → capture `sup` → release `p.mu` → `sup.Restart`) is the only production site this ticket changes. The doc is one of the six stale-claim sites. |
| `internal/sessions/session.go` | `Session.spawnArgs`, `claudeSettingsArgs`, `SettingsUpdate` | `spawnArgs` is the single argv-recompose path; `claudeSettingsArgs` is where the YOLO fail-safe lives (one origin) and where the model→effort→bypass ordering convention comes from. `SettingsUpdate`'s three pointer fields are the presence contract the partition keys on. |
| `internal/sessions/runner.go` | `Runner` | `SetSpawnArgs`'s interface doc — the swap-without-kill contract #1580 shipped for exactly this caller. **No interface change in this ticket.** |
| `internal/streamsup/runner.go` | `(*Runner).SetSpawnArgs`, `(*Runner).Restart`, `(*Runner).setArgsLocked`, `(*Runner).WriteUserTurn` | The production implementations. `setArgsLocked` is the single assignment site both installers route through. `WriteUserTurn`'s doc names the exact two errors that reach a caller. |
| `internal/streamsup/envelope.go` | `WriteTurn`, `marshalTurnEnvelope`, `ErrNoLiveChild` | `WriteTurn` takes the **raw prompt** and builds the envelope, so the payload this ticket writes is the command text itself. `marshalTurnEnvelope`'s doc carries the single-physical-line injection argument the security review leans on. |
| `internal/streamsup/runner_test.go` | the SetSpawnArgs two-spawn test (the one whose comment reads *"once that child ends, the next spawn re-execs with the newly installed argv"*) | The cross-package proof that an installed argv reaches the next spawn. The sessions-side tests assert the **call**; this asserts the **effect**. Do not re-prove it in `internal/sessions`. |
| `internal/sessions/runner_test.go` | `lifecycleRunner` (esp. its `restarts`, `setArgs` fields and `WriteUserTurn`) | The double every pool test runs against. `WriteUserTurn` returns `nil` and records nothing — trap 2. `SetSpawnArgs` records to `setArgs` and deliberately does **not** call `recordArgv` — trap 3. |
| `internal/sessions/pool_settings_test.go` | `recordingRunnerFactory`, `recordArgv`, `waitArgvRaw`, `spawnMintedWithSettings` | `recordingRunnerFactory` ignores `cfg.ClaudeBin` and returns a `lifecycleRunner` — this is *why* trap 1 (`doneAppears`) is vacuous. |
| `internal/sessions/pool_mcp_settings_test.go` | `waitArgv`, `stripMCPSettings` | `stripMCPSettings` is reusable on a raw `setArgs` entry: every recomposed argv carries the #943 `--settings <path>` pair, so a raw `reflect.DeepEqual` against `["--model","opus"]` will fail. |
| `internal/sessions/pool_update_settings_restart_test.go` | `helperRestartPool`, `mintEvicted`, `clearRecording`, `doneAppears`, and all seven `TestPool_UpdateSettings_*` | The file this ticket edits. Six tests stay byte-identical; the seventh is rewritten. |
| `internal/relay/v2session_settings.go` | `handleSetSessionSettings`, `validModel`, `validEffort` | The only production entry into `Pool.UpdateSettings`, and the sole validating boundary. Two of the six stale-claim sites are in this handler's comments. |
| `cmd/pyry/main.go` | `settingsUpdaterAdapter.UpdateSettings` | Confirms the single production call chain: relay handler → adapter → `Pool.UpdateSettings`. Nothing else calls it. |
| `cmd/pyry/relay.go` | the `SettingsUpdater:` field comment in the v2-manager config literal (in the function that builds it, near the `DebugBundler:` field) | Stale-claim site 3. |
| `docs/knowledge/features/sessions-package.md` | § **Live-restart on a real change (#842)** | Stale-claim site 5. Evergreen — maintained, not frozen. |
| `docs/knowledge/features/v2-session-manager.md` | § **Inbound `set_session_settings` (#845)** | Stale-claim site 6. |
| `docs/knowledge/decisions/031-settings-restart-fire-and-forget.md` | whole file, **read-only** | Frozen record. Its last Consequences bullet already flagged the `--session-id`-on-second-spawn question that this ticket's crash-loop is. **Do not edit it.** |

---

## Context

A client's run-configuration sheet sets model, effort, and permissions posture. `Pool.UpdateSettings` persists all three, then live-applies them by **tearing down and respawning the child** (`sup.Restart(newArgs)`, added by #842).

On a session that has never run a turn, the respawn re-execs with `--resume`, claude answers `No conversation found with session ID` and exits 1, and the daemon retries forever on a widening backoff. The ticket carries the 2026-08-18 log. ADR 031's own final Consequences bullet flagged this shape as an inherited open question; it has now been observed.

**The replacement mechanism, measured 2026-08-19 against claude 2.1.220:** claude accepts `/model <name>` and `/effort <level>` as ordinary user turns on the stream the daemon already holds open, and applies them to the running session with no respawn. The measurement used **the exact envelope `marshalTurnEnvelope` produces** (the block-array `content`, not a bare string) — this matters, because an earlier probe used the bare-string shape, which is not what the daemon's delivery primitive emits.

So the fix is not "repair the restart" — it is "stop restarting for the changes that do not need a restart". That **avoids** the crash-loop rather than fixing it: no resume, no lost transcript, no crash-loop.

**The restart stays** for everything the in-band command cannot express, and all three are reachable from the wire:

1. A YOLO (bypass-posture) change — no measured in-band form. Live-applying YOLO is #1573; deleting the restart once it has no callers is #1574.
2. Clearing the model to `""` — `validModel` accepts `""`, meaning "run at claude's own default", which `claudeSettingsArgs` expresses by *omitting* the flag. No measured `/model` invocation means "revert to default".
3. Clearing the effort to `""` — the same, via `validEffort`.

Because the restart recomposes argv from the **merged** settings, a frame mixing YOLO with a model/effort still carries the new model and effort through the respawn. Clean partition: never both mechanisms for one change, no case left unserved.

---

## Design

### The partition

Key on which fields are **present** on the update, never on merged-vs-previous per field. The wire's own contract is what makes presence meaningful: `SetSessionSettingsPayload`'s three fields are `omitempty` pointers documented as a presence contract, and an unset field is *absent* from the wire — so a client changing only the model sends only `model`. The in-band path is the common case, not a branch that never fires.

Two new unexported symbols in `internal/sessions/pool.go`, plus a five-line branch at the tail of `Pool.UpdateSettings`.

#### `inBandDeliverable(u SettingsUpdate) bool`

A total predicate over any `SettingsUpdate`:

```go
// inBandDeliverable reports whether u's PRESENT fields are exactly a non-empty
// Model and/or a non-empty Effort — the changes claude accepts as /model and
// /effort commands on a stream it is already reading (#1581).
func inBandDeliverable(u SettingsUpdate) bool
```

True iff **all** of:

- `u.YOLO == nil` — any present YOLO, *including one equal to the stored value*, takes the restart. This is the conservative side of the partition and it is deliberate: no such frame has been observed, and refining the rule to compare merged-vs-previous per field is explicitly out of scope.
- `u.Model != nil || u.Effort != nil`
- `u.Model == nil || *u.Model != ""` — a clear-to-default has no in-band form.
- `u.Effort == nil || *u.Effort != ""` — same.

The second clause is redundant *at the one call site* (the all-nil update returns early as a no-op before the tail is reached), but keeping it makes the predicate total and independently table-testable rather than dependent on a caller-side invariant. Free function, not a method: `SettingsUpdate` is declared in `session.go` and this belongs with the decision it serves.

#### `(*Pool).deliverSettingsInBand(id SessionID, sup Runner, u SettingsUpdate)`

```go
// deliverSettingsInBand writes the /model and /effort commands implied by u onto
// id's live child stdin — one ordinary user turn each, model first — as the
// non-restarting live-apply for a model/effort-only change (#1581).
// Fire-and-forget: every write error is logged and swallowed. Returns nothing.
func (p *Pool) deliverSettingsInBand(id SessionID, sup Runner, u SettingsUpdate)
```

Behaviour, in this order:

1. If `u.Model != nil`, write `"/model " + *u.Model`.
2. If `u.Effort != nil`, write `"/effort " + *u.Effort`.

Each write is `sup.WriteUserTurn(context.Background(), "", []byte(cmd))`. Two commands are two **separate** turns, not one payload with an embedded newline: the measurement covers one command per turn, and a two-command message is unmeasured. Model-then-effort is fixed for the same reason `claudeSettingsArgs` fixes model→effort→bypass — determinism buys testability at no cost.

Emit one command per **present** field, not per *changed* field. `{Model:"sonnet"(unchanged), Effort:"high"(new)}` therefore sends a redundant `/model sonnet`, which claude answers and discards. Accepted: it costs one round trip in a case no frame has been observed to produce, and per-field diffing is the refinement the ticket rules out.

`context.Background()` is correct here rather than a shortcut. `WriteTurn` consults ctx **only** for the turncommit gate, and its own doc names the nil-gate case as *"the non-queue paths, e.g. a direct single-turn send"* — exactly this. A ctx would not bound the write either (the write is unconditional once the gate passes), so plumbing one through the `relay.SettingsUpdater` seam would change a cross-package signature for no observable gain.

`conversationID` is `""`: accepted for `sessions.Runner` conformance, unused by the stream runner. It **must not be logged**, and neither may the payload bytes — the runner logs neither today.

#### The tail of `Pool.UpdateSettings`

Unchanged up to and including the `p.mu.Unlock()`. Then:

```go
if inBandDeliverable(update) {
    sup.SetSpawnArgs(newArgs)
    p.deliverSettingsInBand(id, sup, update)
    return nil
}
sup.Restart(newArgs)
return nil
```

Both branches install `newArgs`; only one kills. That symmetry is the point of writing it this way, and it is the guard against the trap below.

**`Restart(args)` is doing double duty, and losing half of it is the failure mode this shape prevents.** It installs the next spawn's argv *and* kills the child. A path that stops calling it loses the argv swap unless it swaps another way — the operator's change would then silently revert on the next crash-respawn or evict→`Activate`. #1580 shipped `SetSpawnArgs` for exactly this caller. Use it; do not re-solve it. Both installers route through `setArgsLocked`, so `r.args` still has exactly one assignment site.

**Swap before write.** The swap is the durable half and is non-blocking; if anything goes wrong afterwards the next spawn still carries the change.

**No busy-gate.** A change arriving mid-turn lands in claude's input behind the running turn. Do not add a gate — no such failure has been observed, and the alternative today is a crash-loop.

### Data flow

```
phone ──set_session_settings──▶ handleSetSessionSettings
                                  │ validModel / validEffort   ← the ONLY validating boundary
                                  ▼
                                settingsUpdaterAdapter.UpdateSettings   (cmd/pyry)
                                  ▼
                                Pool.UpdateSettings
                                  │ merge → no-op? → saveLocked → newArgs := sess.spawnArgs(merged)
                                  │ p.mu.Unlock()
                                  ├─ inBandDeliverable(update) ──▶ sup.SetSpawnArgs(newArgs)
                                  │                                sup.WriteUserTurn(ctx.Background(), "", "/model …")
                                  │                                sup.WriteUserTurn(ctx.Background(), "", "/effort …")
                                  │                                  └─ live child applies it; no respawn
                                  └─ otherwise ─────────────────▶ sup.Restart(newArgs)      (today's path, unchanged)
```

### Not in scope: which session the change is addressed to

`Pool.UpdateSettings` is already per-session — it looks up `p.sessions[id]` and acts on that session's supervisor. Whether the *right* session is chosen is decided upstream and is a separate defect with a separate cause, tracked as **#1577**. This ticket changes *how* a change is delivered, not *which* child receives it. Leave that behaviour exactly as it is.

---

## Concurrency model

No new goroutines. No new locks. No change to lock order.

- The branch runs **after** `p.mu.Unlock()`, in the same position `sup.Restart` occupies today. `update` is a value parameter and `newArgs`/`sup` were captured under `p.mu`, so the branch reads nothing shared.
- `SetSpawnArgs` takes only the runner-internal leaf `restartMu`, writes only `args`, and touches neither `Pool.mu` nor `Session.lcMu` — its own doc states this. Against a racing `beginSpawn` it serialises wholly before or wholly after; both are correct, because the contract is the *next* spawn.
- `WriteUserTurn` takes only the runner-internal stdin-target mutex. Same posture.
- **The two writes are synchronous, and that is deliberate.** They are ≤ ~80-byte writes into a pipe whose buffer is ≥ 16 KiB on both supported platforms, drained continuously by a child in stream-json mode, so the write cannot realistically block the v2 manager's single dispatch goroutine. A timeout ctx would buy nothing (`WriteTurn` consults ctx only for the gate, never for the write). An async fire-and-forget goroutine was considered and rejected: it adds an unsynchronised goroutine with no exit signal and forces every assertion in § Testing to poll instead of read.
- The synchronous shape is also what makes the tests deterministic: by the time `UpdateSettings` returns, the double has recorded everything under its own mutex, so no assertion needs a timeout or a sentinel.

---

## Error handling

**Every write error is logged and swallowed. `UpdateSettings` returns `nil`.** The client sees success.

That is not laxity, it is the same contract `Restart` has had since #842: the settings are already persisted and the next spawn carries them (the `SetSpawnArgs` call above guarantees that), so nothing is lost by a failed delivery. AC #4 asks for exactly this on the no-live-child case, and today's fire-and-forget `Restart` already behaves this way.

**The errors cannot be classified, and do not need to be.** `internal/sessions` must not import `internal/streamsup` — that would invert the `Runner` seam — so `errors.Is(err, streamsup.ErrNoLiveChild)` is unavailable by construction. The two expected errors are `ErrNoLiveChild` (evicted session, between spawns, or a rotation armed by `BeginRotation`) and `turncommit.ErrDropped`; a third is a wrapped write failure such as EPIPE mid-teardown. All three warrant the same response.

**The pool does not pre-check for a live child.** It cannot: `State()` is a racy read and classification is unavailable. Attempt-and-swallow is the design, on every path including evicted.

**Log shape.** One record per failed write, at `Info`, on `p.log`, matching the package's nearest neighbour (`Pool.UpdateSettings`'s sibling `"sessions: self-heal rotate persist failed"` — the `"sessions: "` prefix, no `event` key):

- message: `"sessions: in-band settings command not delivered"`
- `"session", id` — a confirmed-real routing id; `UpdateSettings` already resolved it, and the relay handler logs `session_id` on the same request.
- `"setting", "model"` / `"setting", "effort"` — a **fixed literal** naming which command failed. Never the value.
- `"err", err` — safe: none of the reachable errors quotes a value or a path.

**Never logged, at any level:** the model or effort value, the payload bytes, the conversation id. #833 keeps settings values out of logs and this path does not get an exemption just because the value now travels as command text.

**Why `Info` and not `Warn`.** `CODING-STYLE.md` reserves `Warn` for recovered errors and degraded operation. On the dominant reachable case — an evicted session, or one between spawns — nothing is degraded: there is no child to tell, the change is persisted, and the next spawn carries it. Logging that at `Warn` on every settings change to an evicted session is noise that trains operators to ignore the record. Volume is bounded at two records per update. See § Open questions for the residual.

---

## Testing strategy

### Three traps in the existing harness. All three silently produce assertions that cannot fail.

1. **`doneAppears` is vacuous.** `helperRestartPool` writes `restartRecorderScript` and passes it as `ClaudeBin`, but `recordingRunnerFactory` ignores `cfg.ClaudeBin` and returns a `lifecycleRunner`, whose `Run` execs `/bin/sleep`. Nothing ever writes the `done` sentinel, so `doneAppears` always returns false. Measured 2026-08-19: deleting the no-op early return from `UpdateSettings` (so a no-op update calls `Restart`) leaves `TestPool_UpdateSettings_NoOp_NoRestart` **green** while reddening `TestPool_UpdateSettings_NoOpWritesNothing` — the mutant is live and the assertion is simply blind. **Assert "no respawn" on the double's recorded `Restart` calls. Never on `doneAppears`, in any new or rewritten test.**
2. **`lifecycleRunner.WriteUserTurn` returns `nil` and records nothing**, so an in-band delivery is invisible today. It needs a capture, in the same shape as the existing `restarts` / `setArgs` records.
3. **`SetSpawnArgs` on the double records to `setArgs` and deliberately does not route through `recordArgv`** — which is the channel `waitArgv` reads. #1580 left that as a signpost for its first caller, and this ticket is that caller. An AC #4 assertion written with `waitArgv` will not see the swap on the in-band path; worse, `clearRecording` removes only files, so `argvRecords` still holds the *construction* argv and `waitArgv` returns it immediately — a stale value, not a timeout.

Also: every recomposed argv carries the #943 `--settings <path>` pair, so compare a `setArgs` entry through `stripMCPSettings` (which fatals when the pair is missing, doubling as a #943 regression check on the new path).

### Harness changes (`internal/sessions/runner_test.go`)

- Add a `writes [][]byte` field to `lifecycleRunner`, guarded by the existing `r.mu`, with a field comment in the register of its `restarts`/`setArgs` neighbours explaining that this is the in-band delivery channel #1581 introduced.
- `lifecycleRunner.WriteUserTurn` appends a clone of `payload` and keeps returning `nil`. The double records the write on **every** path including no-live-child — it is the production runner, not the pool, that refuses when there is no child.
- Three accessors, each taking `r.mu` and returning deep copies (the lifecycle goroutine writes while the test goroutine reads; this must be `-race` clean): `restartArgs() [][]string`, `spawnArgSets() [][]string`, `userTurns() []string` (`[]byte`→`string` on read so assertions read as command text).
- One test helper: fetch the double off a session via `Session.Runner()` with a checked type assertion and `t.Fatalf` on failure.

`fakeRunner` needs no change.

### Scenarios

Each is a scenario, not a code listing. All assertions are read directly after `UpdateSettings` returns — synchronous delivery means no polling, no timeout, no sentinel.

**A. Model+effort-only change delivers in-band and does not respawn** — AC #1, plus AC #4's live-child half.

- Running bootstrap via `helperRestartPool` with `{Model:"sonnet"}`; `runPoolInBackground`; `waitArgv` once to sync on the first spawn.
- `UpdateSettings(id, {Model: ptr("opus"), Effort: ptr("high")})` returns nil.
- The double's recorded user turns are exactly `["/model opus", "/effort high"]`, in that order — proves delivery happened *and* that the command text is the measured form.
- The double's recorded `Restart` calls are **empty** — the "no respawn" half, read from the record and not from `doneAppears`.
- The double's recorded `SetSpawnArgs` calls have exactly one entry; through `stripMCPSettings` it equals `["--model","opus","--effort","high"]`.
- `diskSettings` reports `{opus, high, false}`.

**B. `TestPool_UpdateSettings_YOLOAbsent_NoBypassOnRestart`, rewritten** — AC #3.

Today this is a model-only update asserting a respawn, so the new rule necessarily changes it. Same setup (`{Model:"sonnet", YOLO:false}`, update `{Model: ptr("opus")}`), new assertions:

- Recorded `Restart` calls are empty — no respawn.
- Recorded user turns are exactly `["/model opus"]` — one command, no `/effort`.
- The single `SetSpawnArgs` entry, through `stripMCPSettings`, equals `["--model","opus"]` and carries no `--dangerously-skip-permissions`. **The bypass fail-safe is preserved, not deleted** — it is now asserted against the argv the next spawn will use rather than against the argv a respawn did use.
- **Rename it to `..._YOLOAbsent_NoBypassInBand`.** Keeping `OnRestart` in the name of a test that asserts no restart is a comment that lies. Say so in the commit message.

**C. Model-only change on an evicted session** — AC #4's evicted half. A new test; `_Evicted_SwapOnly` carries `YOLO` on its update, so it exercises the restart path and proves nothing about this one.

- `helperPoolArgvRecorder` + `mintEvicted` with `{Model:"sonnet"}` — supervised, never activated, no live child.
- `UpdateSettings(id, {Model: ptr("opus")})` returns **nil** — the no-live-child case reports no error to the client, matching today's evicted-session contract.
- Recorded `Restart` calls are empty; the single `SetSpawnArgs` entry, through `stripMCPSettings`, equals `["--session-id", <id>, "--model", "opus"]`.
- Recorded user turns are `["/model opus"]` — the write is attempted unconditionally; the double accepts it, and it is the production runner that would answer `ErrNoLiveChild`.
- **Do not add a post-`Activate` argv assertion.** `Activate` reuses the existing runner, so `recordArgv` is never re-fed and `waitArgv` would return the stale construction argv. That the installed argv reaches the next spawn is already proven cross-package by the two-spawn `SetSpawnArgs` test in `internal/streamsup/runner_test.go`. Assert the call here; the effect is proven there.

**D. `inBandDeliverable` table test** — the partition itself, cheap and total. This is what makes AC #2's six untouched tests green on purpose rather than by luck.

| `SettingsUpdate` | want |
|---|---|
| `{Model: ptr("opus")}` | true |
| `{Effort: ptr("high")}` | true |
| `{Model: ptr("opus"), Effort: ptr("high")}` | true |
| `{Model: ptr("")}` | false — clear-to-default has no in-band form |
| `{Effort: ptr("")}` | false |
| `{Model: ptr("opus"), Effort: ptr("")}` | false — one empty present field is enough |
| `{Model: ptr("opus"), YOLO: ptr(true)}` | false |
| `{Model: ptr("opus"), YOLO: ptr(false)}` | false — **a present YOLO takes the restart even when it equals the stored value** |
| `{YOLO: ptr(true)}` | false |
| `{}` | false |

**E. Clearing the model keeps the restart** — AC #2's uncovered corner. All six tests AC #2 names carry `YOLO` on their update or are no-op/failure paths, so none of them exercises the clear-to-`""` case the Context names as a reason the restart survives.

- Bootstrap with `{Model:"sonnet"}`; `UpdateSettings(id, {Model: ptr("")})`.
- Recorded `Restart` calls have exactly one entry; through `stripMCPSettings` it is empty (no settings flags at all).
- Recorded user turns are **empty** — no `/model ""` is ever invented.

### These six stay green, **unmodified** (AC #2)

In `internal/sessions/pool_update_settings_restart_test.go`: `TestPool_UpdateSettings_LiveRestart_Bootstrap`, `_LiveRestart_Minted`, `_NoOp_NoRestart`, `_PersistFailure_NoRestart`, `_Evicted_SwapOnly`, `_YOLORevoke_DropsBypassOnRestart`. Each either carries a present `YOLO` (→ restart) or returns before the tail (no-op / persist failure). Do not touch their bodies, names, or comments.

### Gate

`make check`. No new e2e. The live-claude proof that the running child actually changed is **#1582**, blocked by this one; do not attempt it here.

---

## Documentation: the stale "next spawn" claim (AC #5)

Comments today tell a reader that a settings change reaches claude only on the session's **next spawn**, and name the live-restart work as unbuilt future work. #842 shipped 2026-07-08, so they were already wrong before this ticket; this ticket makes them wrong a second way. Anyone reading one while building a client concludes the capability does not exist.

**Correct all six live sites.** Each should say what is now true: a settings change reaches a *running* session immediately — model/effort-only in-band as a `/model` / `/effort` command (#1581), everything else by live restart (#842).

| # | File | Symbol / section | The sentence to correct |
|---|---|---|---|
| 1 | `internal/relay/v2session_settings.go` | `handleSetSessionSettings` doc | *"The change takes effect on the session's NEXT spawn (#833's argv path); making a running session pick it up is the sibling live-restart ticket #842."* |
| 2 | `internal/relay/v2session_settings.go` | `handleSetSessionSettings`, the success-reply comment above the `SessionSettingsUpdatedPayload` marshal | *"the change is persisted atomically and reaches claude on the session's next spawn"* |
| 3 | `cmd/pyry/relay.go` | the `SettingsUpdater:` field comment in the v2-manager config literal | *"applied on the session's next spawn (#833)"* |
| 4 | `internal/sessions/pool.go` | `Pool.UpdateSettings` doc | The whole *"After a successful persist of a real change, the session's supervisor is live-restarted (#842)"* paragraph — this is the doc of the function the ticket changes, so it must describe the partition, the `SetSpawnArgs` obligation on the in-band branch, and the swallowed-error contract. |
| 5 | `docs/knowledge/features/sessions-package.md` | § **Live-restart on a real change (#842)** | The paragraph's claim that a real change triggers a live restart. Keep the `spawnBase` / `spawnArgs` / `claudeSettingsArgs` prose — still true for both branches; add the partition and the in-band half. |
| 6 | `docs/knowledge/features/v2-session-manager.md` | § **Inbound `set_session_settings` (#845)** | *"Change takes effect on the session's **next spawn** (#833's `claudeSettingsArgs` argv path); making a *running* session pick it up immediately is sibling #842."* |

**Explicitly NOT in scope — frozen, point-in-time records. Do not rewrite them:** `docs/specs/architecture/**` (840, 842, 845, 943, 1005, 1031, 1518, 1575, 1580 all carry the claim as a statement of what was true at their build time), `docs/knowledge/codebase/**` (840, 842, 845, 1005, 1031), `docs/knowledge/decisions/031-settings-restart-fire-and-forget.md`, and its one-line summary in `docs/knowledge/INDEX.md`, which remains an accurate summary of that ADR. Editing these would falsify the build record.

Also out of scope: the same `sessions-package.md` paragraph names `Supervisor.Restart (internal/supervisor)`, a package #1348 deleted. That staleness predates this ticket and belongs to a different sweep — leave the sentence alone. See § Open questions.

### The sweep

Sweep the **claim**, not a line — a reader adding a fourth code site later must be found by the same sweep.

```bash
git grep -n -i -F -e "next spawn" -- '*.go' 'docs/knowledge/features/'
```

Notes that make this recipe actually work:

- **`-F`, not `-E`.** `git grep -E '\bnext spawn\b'` matches **nothing** — POSIX ERE has no `\b` — and reads as a clean absence. Use `-F` (or `-P`).
- The pathspec is the exclusion. `'*.go'` and `'docs/knowledge/features/'` already exclude `docs/specs/`, `docs/knowledge/codebase/`, `docs/knowledge/decisions/`, and `INDEX.md`. Do not add `--exclude` clauses; do not use `grep -r`, which false-hits the untracked `.claude/worktrees/`.
- **This sweep must return a non-empty set, and that is the control.** Many legitimate `next spawn` mentions survive — `SetSpawnArgs`' own docs, `RestartFresh`, backoff fields, the rotation watcher. A zero-result sweep means the pattern is broken, not that the work is done. The AC is that **none of the survivors says a settings change reaches claude only on the next spawn**; read each hit and confirm.
- Run it once *before* editing to capture the baseline set (it is the list in the ticket's Context, plus the six sites above), then again after, and diff the two by hand.

---

## Security

The ticket is `security-sensitive`. The adversarial pass is § Security review below. The design-level facts it rests on:

- The untrusted model/effort values are shape-checked at the wire boundary by `validModel` and `validEffort` in `handleSetSessionSettings`, before any persistence. That is the **only** validating boundary, and `handleSetSessionSettings` → `settingsUpdaterAdapter` → `Pool.UpdateSettings` is the **only** production path in (verified: no other non-test caller exists).
- Those validators were written and reviewed as an **argv**-injection defence. This change makes the same bytes reach a **command interpreter** on claude's stdin instead. Whether that is sufficient for the new sink is the question the review answers — not an assumption to carry over.
- `internal/sessions` deliberately does **not** validate: `Pool.UpdateSettings` operates on operator-trusted input and the wire handler owns the untrusted→trusted crossing. This ticket does not move that boundary.

---

## Accepted consequences

Stated here so they are not discovered at review.

- **Claude answers the command in words.** The in-band write is an ordinary user turn, so the daemon's turn-state machine sees a normal busy→idle cycle and the client gets an `assistant_delta` ("Set model to Sonnet 5 for this session only"). The measurement shows that reply carries `model=<synthetic>` rather than a real model id. Inherent to the mechanism, not a defect. Do not try to suppress it.
- **A mid-turn change queues behind the running turn.** No busy-gate — see § Design.
- **A redundant command on a mixed present-but-unchanged frame.** See § Design.
- **The crash-loop is avoided, not fixed.** Its evidence is preserved in the ticket so it can be re-filed if the restart ever gains a caller on the never-run path again. Do not fix the crash-loop instead of changing the mechanism.

---

## Open questions

1. **Log level on a failed in-band write.** `Info` is specified above, because the dominant reachable case (no live child) is not a degradation. The residual: a genuine EPIPE against a *live* child is a real degradation logged at `Info`, and the pool cannot tell the two apart without importing `internal/streamsup` or reading a racy `State()`. If an operator ever needs the discriminator, the cheapest addition is a `"phase", sup.State().Phase` field — one method call, no classification, no import. Not doing it now: no such failure has been observed.
2. **`sessions-package.md` still names `Supervisor.Restart (internal/supervisor)`**, deleted by #1348, in the same paragraph this ticket edits. Left alone deliberately (scope), but it is a real stale reference and worth its own ticket alongside whatever #1574 does to that section.
3. **Two commands, two turns.** If claude turns out to accept `/model X\n/effort Y` as one message applying both, the delivery collapses to one turn. Unmeasured, so not designed for. #1582 is the natural place to probe it.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[1 · Trust boundaries]** No MUST FIX. The boundary is explicit and singular: `validModel` and `validEffort` in `handleSetSessionSettings`, run before any persistence, on the only production path into `Pool.UpdateSettings` (`handleSetSessionSettings` → `settingsUpdaterAdapter.UpdateSettings` → `Pool.UpdateSettings`; verified by enumerating every non-test caller, not by sampling). The spec does **not** move that boundary, and `Pool.UpdateSettings` keeps its documented operator-trusted posture. The genuine change is the **sink**: the same bytes now reach a command interpreter on claude's stdin, not only an `exec` argv. Walked below rather than assumed.
- **[1 · Trust boundaries]** No MUST FIX — the shape check is sufficient for the new sink, and the argument is per-property rather than by inheritance. `validEffort` is a closed enum `{"", low, medium, high, xhigh, max}`, so `/effort <v>` has zero attacker-chosen bytes and no new surface. `validModel` is a shape check, not an allowlist: 1..64 bytes, first byte alphanumeric, every byte in `[A-Za-z0-9._-]`. For the `/model <v>` sink specifically that charset admits **no space or tab** (so the value cannot split into a second argument or a second command), **no newline or control byte** (so it cannot forge a second stream-json line), **no `/`** (so it cannot begin a nested slash-command or a path), and **no leading `-`** (so it cannot pose as a flag). `marshalTurnEnvelope` then JSON-escapes it into a single physical line by construction — structured encoding, never concatenation — which the envelope's own doc establishes as the injection-resistance invariant for arbitrary prompt bytes, a strictly larger input set than this one. Worst case for a hostile-but-shape-valid value: claude receives `/model <garbage>` and answers "unknown model". The daemon's argv is untouched on this path.
- **[2 · Tokens, secrets, credentials]** Not applicable, by design and not by omission: this path mints, stores, compares and transports no secret. It carries a model name, an effort enum, and a session id already resolved by the caller.
- **[3 · File operations]** Not applicable to the new code — it opens, creates and names no file. The persist half is unchanged (`saveLocked`'s existing temp-file-plus-rename), and the spec's ordering keeps it strictly *before* any delivery, so a delivery failure can never leave a partially-written registry.
- **[4 · Subprocess / external command execution]** No MUST FIX, and this category *improves*. The change removes an `exec` from the model/effort path entirely — no respawn, so no argv is composed for execution at all on that branch, only installed for a later spawn via `SetSpawnArgs`. The argv that *is* installed still routes through `Session.spawnArgs` → `claudeSettingsArgs`, so the YOLO fail-safe keeps exactly one origin. No `sh -c` anywhere; no environment change; the spec adds no signal handling and removes the kill this path used to perform.
- **[4 · Subprocess]** No MUST FIX — **the bypass fail-safe survives the mechanism change, and is asserted.** The worry worth naming: the in-band branch no longer respawns, so a naive reading is "the child keeps whatever permissions posture it launched with, forever". It does — and that is correct, because the branch is *unreachable* for any update carrying a present `YOLO` (`inBandDeliverable` requires `u.YOLO == nil`), including a present `false`. A YOLO revoke therefore still kills and relaunches without the flag (`_YOLORevoke_DropsBypassOnRestart`, unmodified), and a model-only change on a non-bypass session still installs a bypass-free argv (scenario B, which preserves the fail-safe assertion rather than deleting it). Scenario D's table pins both YOLO rows as the partition's own regression test. There is no update shape that reaches the in-band branch and could widen a running child's permissions — `/model` and `/effort` carry no permissions semantics.
- **[5 · Cryptographic primitives]** Not applicable — no randomness, no key material, no comparison against a secret anywhere in the change.
- **[6 · Network & I/O]** No MUST FIX. Nothing new is read from a socket, so no size cap is owed. The one new *write* is bounded by construction: at most two envelopes per update, each ≤ ~80 bytes of payload (`validModel` caps the value at 64 bytes, `validEffort` at 4), against a pipe buffer ≥ 16 KiB — so an attacker who can drive `set_session_settings` cannot use this path to exhaust a buffer or wedge the writer. Rate is already bounded upstream by the interactive capability gate. The unbounded-blocking question is answered in § Concurrency: the write cannot realistically block, and the rejected async alternative is recorded there.
- **[7 · Error messages, logs, telemetry]** No MUST FIX, with one explicit rule the developer must not relax. The spec's log record is MUST-log `{event message, session id, a fixed "model"/"effort" literal, err}` and MUST-NOT-log `{the model value, the effort value, the payload bytes, the conversation id}`. This matters more than on the argv path, because the payload now *contains* the untrusted value verbatim — a `slog` call that logged `payload` would put attacker bytes in the daemon log, which #833's "settings values are never logged at any level" rule exists to prevent. `err` is safe to log: the reachable set is `ErrNoLiveChild`, `turncommit.ErrDropped`, and `fmt.Errorf("streamsup: write turn: %w", err)` over a pipe error — none quotes a payload byte or a path, and `json.Marshal` of a `string` field cannot fail, so the marshal branch is unreachable. Nothing new reaches the client: the reply is the existing deterministic `session_settings_updated`, unchanged, and it does not gain a delivery-status field.
- **[7 · Logs]** SHOULD FIX (not gating) — `Info` under-signals a genuine EPIPE against a live child, which is real degraded operation. Recorded as Open question 1 with the cheapest remedy (`"phase", sup.State().Phase`) and the reason for deferring (unobserved). Code-review may reasonably push this to `Warn`; either choice is safe, neither leaks.
- **[8 · Concurrency]** No MUST FIX. No new goroutine, so no lifecycle or leak to reason about — this is the reason the async-write alternative was rejected rather than merely disliked. No new lock, and no change to lock order: the branch runs after `p.mu.Unlock()`, in `sup.Restart`'s exact position, and both `SetSpawnArgs` and `WriteUserTurn` take only runner-internal leaf mutexes, touching neither `Pool.mu` nor `Session.lcMu`.
- **[8 · Concurrency]** No MUST FIX — the check-then-act on the swap is benign and bounded. `SetSpawnArgs` racing `beginSpawn` resolves wholly before (the spawn takes the new argv) or wholly after (that spawn keeps the old argv, the next takes the new); its own doc establishes there is no third position, and it writes only `args` — never `sessionID`, `rotatePending` or `iterCancel` — so it cannot reach the forbidden state `beginSpawn` guards. Worst case is a one-spawn delay in a value that is already persisted on disk. The *security-relevant* corner — could that race leave a bypass flag installed after a revoke? — cannot arise, because a revoke never reaches this branch (see category 4).
- **[8 · Shutdown safety]** No MUST FIX. A kill between `saveLocked` and the writes leaves settings on disk and a child that never heard the command; the next spawn reads the persisted settings, so the state is recoverable and correct. Swap-before-write makes this strictly better than the reverse order, which is why the spec fixes that sequence rather than leaving it to taste.
- **[9 · Threat model alignment]** No MUST FIX. `docs/protocol-mobile.md` § Security model's relevant threat is a paired-but-hostile client driving inbound control verbs; the capability gate, the pre-persist validation, and the no-echo reply discipline are all unchanged by this ticket, and this change strictly *narrows* what a hostile `set_session_settings` can cause (a command claude rejects, instead of a process respawn). **Out of scope, named with owners:** live-applying a YOLO change is #1573; deleting the restart once it has no callers is #1574; which session a change is addressed to is #1577; the live-claude proof that the running child actually changed is #1582.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
