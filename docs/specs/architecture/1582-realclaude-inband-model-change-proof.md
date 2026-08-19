# 1582 — realclaude: prove a running child's model changed in-band, with no respawn

One new test file under `internal/e2e/realclaude`, behind the `e2e_realclaude` build tag. **Zero production
changes.** The test drives one live claude session through `sessions.Pool.UpdateSettings` and reads claude's
own per-turn `system`/`init` announcement off the `streamsup.Config.Stdout` seam, upstream of the parser.

---

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`, then Read.

| File | Symbol | What to extract |
|---|---|---|
| `internal/e2e/realclaude/dropped_line_capture_test.go` | `dropcapRecorder`, `newDropcapArgvHandler`, `dropcapWaitForChild`, and the `streamsup.New` call in `TestDroppedLineCapture` | **The precedent for everything structural here.** An `io.Writer` in `Config.Stdout`, a `slog.Handler` filtering on the `"spawning claude"` message, a poll on `Stdin()`, and an in-process `streamsup.Runner` driven from a realclaude test. Copy the shapes; do not copy the redaction/fixture machinery — none of it applies. |
| `internal/streamsup/runner.go` | `Config` (the `Stdout` field doc), `New`, `Run`, `spawnAndWait`, `buildArgs`, `Stdin`, `WriteUserTurn`, `SetSpawnArgs` | `Stdout` is the tap; `spawnAndWait` re-sets `cmd.Stdout` on **every** spawn, which is what keeps the recorder alive across the pre-change tree's respawn. `Run` logs `"spawning claude"` at Info exactly once per spawn. `Config.onSpawn` is unexported — unusable from this package. |
| `internal/streamsup/state.go` | `State`, `(*Runner).State` | `ChildPID` is the direct "same process" observable. Note the return type is `streamsup.State`, **not** `sessions.State` — that mismatch is why the test needs an adapter. |
| `internal/streamsup/parser.go` | `emitSystemSubtype` | Verified 2026-08-19: four arms (`task_started`, `task_updated`, `background_tasks_changed`, `thinking_tokens`) and a `default: return false`. `init` has no arm, so it is dropped in silence. **This is why the tap must be upstream of the parser** — and why nothing in this ticket may add an arm. |
| `internal/sessions/pool.go` | `UpdateSettings`, `inBandDeliverable`, `deliverSettingsInBand`, `New`, `saveLocked` | The entry point under test, the two traps that would silently route onto the restart path, and the fact that `saveLocked` is a no-op only when `RegistryPath` is empty (we set it, so the persist runs for real). `deliverSettingsInBand`'s model `send` is the mutation site for evidence run B. |
| `internal/sessions/session.go` | `SessionSettings`, `SettingsUpdate`, `claudeSettingsArgs`, `spawnArgs`, `(*Session).Runner`, `(*Session).ID` | The update shape, and the recompose that decides what argv a respawn would run. `spawnArgs` = `spawnBase` + `claudeSettingsArgs(merged)` — **the reason the base argv must carry no `--model`** (see Design § "Why the base argv carries no `--model`"). |
| `internal/sessions/runner.go` | `Runner`, `RunnerFactory` | The five-method interface the test's adapter satisfies, and the mandatory injection seam. |
| `internal/sessions/runnerstate.go` | `RunnerConfig`, `State` | What the factory receives (`SessionID` is the pool-minted bootstrap uuid) and the `sessions.State` the adapter must return. |
| `cmd/pyry/streamsup_runner.go` | `newStreamRunnerFactory`, `mapStreamsupConfig`, `streamRunner` | Production's version of exactly the factory + adapter the test writes. `streamRunner` is the ~10-line adapter to mirror. Note what the test deliberately does **not** copy: the turnevent `Parser` in `Stdout` (the recorder takes that slot) and `withApprovalArgs`. |
| `cmd/pyry/session_router_test.go` | `newRouterTestPool`, `stubRunner` | The compact `sessions.New(...)` call and a minimal `sessions.Runner` implementation, both ~8 lines. |
| `internal/e2e/realclaude/fixtures.go` | `WithWorktreeAuthenticated`, `parseInitSessionID` | The credentials guard AC 5 permits, and the package's idiom for decoding an `init` envelope (anonymous struct + `json.Unmarshal`, skip non-JSON silently). The recorder's line decode mirrors `parseInitSessionID`. |
| `internal/e2e/realclaude/resilience_test.go` | `resolveClaudeBin` | The other permitted skip (claude absent from `PATH`). |
| `docs/knowledge/codebase/1581.md` | § Implementation, § Testing | What the sibling proved hermetically, and the sentence naming this ticket as the live proof it deliberately did not attempt. |

Two analogues for length and prose density (both zero-production-change test tickets):
`interactive_stream_resume_after_eviction_test.go` (407 lines), `interactive_stream_multiturn_continuity_test.go`
(278 lines). Both are daemon-subprocess tests driven over the relay; this one is **in-process**, because the
`Stdout` seam lives inside the daemon and is unreachable from outside it.

---

## Context

#1581 changed how a model/effort-only settings change is delivered: instead of killing the child and respawning
it with a rebuilt argv, `Pool.UpdateSettings` writes `/model <value>` as an ordinary user turn on the stream the
daemon already holds open. That ticket is proven hermetically — its tests observe that no respawn happened and
that the write was issued.

Neither observes claude. A hermetic suite cannot tell "the daemon wrote the right bytes" from "the running child
actually changed model", and the second one is the entire point of the mechanism. This ticket supplies that
proof and nothing else.

### The observable

Claude announces the model on a `system`/`init` line once per turn. That line reaches no client and no daemon
event, and **must not be made to**: `emitSystemSubtype` has arms for four subtypes only, `init` is not among
them, and every other `system` subtype falls through to the `ignoredLineTypes["system"]` branch. Adding an arm,
or a log line carrying the value, is barred by the #833 posture restated in `handleRequestSessionSettings` —
model / effort / YOLO values are never logged at any level.

The line **is** reachable from a test without touching production, because `streamsup.Config.Stdout` is an
`io.Writer` that receives the child's stdout *upstream of the parser*. Production installs
`streamsup.NewParser(...)` there; `dropcapRecorder` already takes that same slot on an in-process
`streamsup.Runner`, for the same reason. That is the precedent this test follows.

### Effort has no such observable

The 2026-08-19 measurement dumped every scalar key on the `init` line: `type`, `subtype`, `cwd`, `session_id`,
`model`, `permissionMode`, `apiKeySource`, `claude_code_version`, `output_style`, `analytics_disabled`,
`product_feedback_disabled`, `uuid`, `fast_mode_state`, `fast_mode_disabled_reason`. **No effort or
thinking-level field, before or after `/effort low`.** An effort assertion read from `init` is not hard, it is
impossible. This test asserts **model** and does not send `/effort` at all — but it carries the finding as a
comment, because the absence is the durable result and a future reader will otherwise re-derive it. (AC 4's
`/effort` clause is conditional; declining to exercise it satisfies the AC and saves a live turn.)

---

## Design

### Shape at a glance

```
sessions.New(Config{ RunnerFactory: testFactory, ... })
        │
        └─ testFactory(RunnerConfig)  ──▶  streamsup.New(Config{ Stdout: recorder, Logger: countingLogger })
                                                    │                    │
                                                    │                    └─ counts "spawning claude" records
                                                    └─ recorder: init models (in order) + result count
        ▼
  go runner.Run(ctx)  ──▶  claude child (one spawn)

  turn 1  ── sup.WriteUserTurn ──▶  init model = START
  change  ── pool.UpdateSettings(id, {Model: &alias}) ──▶ deliverSettingsInBand ──▶ "/model <alias>"
  turn 3  ── sup.WriteUserTurn ──▶  init model = RESOLVED(alias)

  assert: first != last, last == RESOLVED(alias), ChildPID unchanged, spawns == 1
```

### The single new file

`internal/e2e/realclaude/interactive_stream_inband_model_test.go`, `//go:build e2e_realclaude`, package
`realclaude`. One test function:

`TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel`

Nothing else in the repo changes.

### The stdout tap

```go
// modelTapRecorder is an io.Writer wired into streamsup.Config.Stdout, where
// production installs the turnevent parser. It extracts exactly two things and
// retains no payloads: the `model` field of every system/init line, in arrival
// order, and a count of top-level `result` lines (the turn boundary).
type modelTapRecorder struct{ /* mu, partial, models, results, dropped, maxPartial */ }

func newModelTapRecorder() *modelTapRecorder
func (r *modelTapRecorder) Write(b []byte) (int, error) // io.Writer; NEVER returns a non-nil error
func (r *modelTapRecorder) initModels() []string        // snapshot copy, arrival order
func (r *modelTapRecorder) results() int
```

Behaviour, one line each:

- `Write` appends to the accumulator, splits on `'\n'`, hands each complete line to an unexported `consume`, and
  keeps the remainder. It returns `len(b), nil` unconditionally — a non-nil error from an `exec.Cmd`'s stdout
  writer aborts the copy and can wedge the child.
- `consume` decodes the line into an anonymous `{type, subtype, model string}` struct (the `parseInitSessionID`
  idiom); a decode failure is skipped silently. `system`/`init` appends `model`; `result` increments the count.
- The accumulator is capped at `modelTapMaxPartial` (mirror `dropcapMaxPartial`, and its stated reason — this is
  claude's stdout in the parent's memory, and the partial is the only unbounded accumulator). Past the cap it is
  dropped, counted in `dropped`, and resynced at the next `'\n'`. `dropped` is reported via `t.Logf` at the end;
  a non-zero value does not fail the test but tells a reader the record has a hole.
- Mutex-guarded: `os/exec` drives `Write` from its own stdout-copier goroutine while the test goroutine reads.
  `dropcapRecorder`'s doc states the same reason.

**Why not reuse `dropcapRecorder`.** It retains every raw line up to 8 MB for a committed fixture and exposes a
one-shot `resultSeen` channel; this test needs three turn boundaries and retains nothing. Reusing it would
couple a forensic capture instrument to an assertion instrument and make both harder to change. Reuse its
*shapes*, not the type.

### The spawn counter

```go
// newSpawnCountHandler returns a slog.Handler that counts records whose message
// is "spawning claude" — one per spawn, emitted by streamsup's Run — and the
// accessor for that count.
func newSpawnCountHandler() (slog.Handler, func() int)
```

Same shape as `newDropcapArgvHandler`, one field lighter. Mutex-guarded for the same reason. Installed as
`sessions.Config.Logger`, which `Pool.New` threads into `RunnerConfig.Logger` and the factory forwards to
`streamsup.Config.Logger`; the handler filters by message, so the pool's own records pass through harmlessly.

`Config.onSpawn` is unexported and unusable from this package — do not plan around it.

### The runner adapter and factory

`sessions.Runner.State()` returns `sessions.State`; `(*streamsup.Runner).State()` returns `streamsup.State`. Go
has no covariant return, so the test needs the same adapter production has:

```go
// tapRunner adapts *streamsup.Runner to sessions.Runner — the field-for-field
// State conversion cmd/pyry's streamRunner performs, and nothing else. The
// other four methods are promoted from the embedded runner.
type tapRunner struct{ r *streamsup.Runner }
func (t tapRunner) State() sessions.State
var _ sessions.Runner = tapRunner{}
```

The factory closure captures the recorder and the concrete `*streamsup.Runner` so the test body can read
`State().ChildPID` and `Stdin()` directly. It maps `RunnerConfig` → `streamsup.Config` by hand (`ClaudeBin`,
`WorkDir`, `SessionID`, `Args` ← `ClaudeArgs`, `Logger`) and sets `Stdout: recorder`. It does **not** call
`withApprovalArgs` and does **not** install a turnevent `Parser` — the recorder owns that slot, and the test's
prompts use no tools.

### Pool construction

```
sessions.New(sessions.Config{
    Bootstrap:     sessions.SessionConfig{ClaudeBin: <resolved>, WorkDir: <fresh empty dir>, ClaudeArgs: baseArgs},
    RegistryPath:  filepath.Join(t.TempDir(), "sessions.json"),
    RunnerFactory: <the capturing factory>,
    Logger:        slog.New(spawnCountHandler),
})
```

- `RegistryPath` is set (not empty) so `saveLocked` inside `UpdateSettings` performs a real write and
  `writeMCPSettings` lands the #943 `--settings` file in the production location. Both are under `t.TempDir()`.
- No registry exists at that path, so `New` cold-starts: a fresh bootstrap uuid and **zero-value
  `SessionSettings`** (`Model: ""`). That is deliberate — see below.
- `baseArgs` is `[]string{"--dangerously-skip-permissions"}`. It carries **no `--model`**, and it suppresses the
  permission modal deterministically. It is the same YOLO interactive shape `dropcapArgs` uses, and it is the
  arm on which production's `withApprovalArgs` injects nothing. It does not affect `inBandDeliverable`, which
  keys on `SettingsUpdate.YOLO`, not on argv.
- `WorkDir` is a fresh empty directory under `WithWorktreeAuthenticated(t)`'s home, deliberately not a git repo
  — less project context for claude to load, so the turns are faster and cheaper.

**`Pool.Run` is deliberately not called.** The session lifecycle goroutine's only job here would be
`go s.sup.Run(subCtx)` (see `runActive`), which the test does directly. Skipping `Pool.Run` also skips the
conversations sweep, the idle timer and the settings-file reaper, none of which this test needs.
`Pool.UpdateSettings` consults none of them — it reads `sess.sup` and calls methods on it.

### Why the base argv carries no `--model`

`spawnArgs(merged)` returns `spawnBase + claudeSettingsArgs(merged)`. If the base carried `--model haiku` and
the update set `Model: "sonnet"`, the recomposed argv would be `… --model haiku … --model sonnet`. On the
current tree that argv is only *installed* (`SetSpawnArgs`), never executed — harmless. But **evidence run A
executes it**: the pre-change tree respawns with it, and whether claude last-wins on a duplicate flag is
unmeasured. A crash there would turn a clean red into a muddy one and would break AC 3's requirement to name
which assertion failed.

With no `--model` in the base, the recompose emits exactly one, on both trees. The cost is that the session's
starting model is claude's own machine default — which the test **reads** from turn 1's `init` line rather than
assuming, and which the target selection below turns into a feature rather than a hazard.

### Choosing the target model

A two-entry table of `{alias, resolvedID}`:

| alias | resolved id (claude 2.1.220, measured 2026-08-19) |
|---|---|
| `haiku` | `claude-haiku-4-5-20251001` |
| `sonnet` | `claude-sonnet-5` |

After turn 1, the test reads `start := rec.initModels()[0]` and picks **the first table entry whose
`resolvedID != start`**. Because the two resolved ids differ, one always qualifies, whatever the machine's
default is — so the test never skips and never asserts a change that was already true. If both entries somehow
match `start` (they cannot, unless the constants are edited to be equal), `t.Fatalf` — that is a broken
instrument, not a skip.

The alias is what `UpdateSettings` carries; `deliverSettingsInBand` writes the value through verbatim, so claude
receives `/model haiku`. The assertion is against `resolvedID`, per AC 1's "equals the requested model's
resolved id".

These two constants are claude-version facts. A family bump (Haiku 5, Sonnet 6) will red the equality assertion.
The comment on the table must say that this failure means *the alias resolves elsewhere now*, not *the mechanism
broke* — otherwise a future operator reads a maintenance failure as a regression.

### The drive sequence

1. Wait for a live child: poll `runner.Stdin()` until non-nil (reuse `dropcapWaitForChild`; it is exactly this
   loop and lives in the same package). `t.Fatalf` on timeout. Record `pidBefore := runner.State().ChildPID`;
   `t.Fatalf` if zero.
2. Turn 1: `"Reply with the single word: one."`, via the send helper below.
3. Read `start` from `rec.initModels()[0]`; `t.Fatalf` if the slice is empty or the value is `""`. Pick the
   alias.
4. `pool.UpdateSettings(pool.Default().ID(), sessions.SettingsUpdate{Model: &alias})` — `t.Fatalf` on a non-nil
   error. **`Model` only: a non-nil `YOLO`, even one equal to the stored value, or a present-but-empty `Model`,
   routes onto the restart path** (`inBandDeliverable`). The requested alias genuinely differs from the stored
   `""`, so `UpdateSettings` does not take its no-op early return.
5. Settle: wait up to `inBandSettleBudget` for the result count to reach 2 — **tolerating a timeout**. On the
   current tree the `/model` turn closes with its own `result`. On a tree that restarts instead, no second
   result ever comes, and the test must still reach its assertions rather than dying here. A one-line comment
   must say so; this wait is the one place a silent timeout is correct.
6. Turn 3: `"Reply with the single word: two."`, via the same send helper.
7. Assert (below), then `t.Logf` the full `initModels()` sequence, the spawn count and `dropped` for the
   operator's record.

```go
// sendTurnAndWaitResult writes prompt through the sessions.Runner seam — the
// same method deliverSettingsInBand uses, so the test's own turns and the
// daemon's in-band command travel one path — and waits for the child to close
// the turn with a `result` line. Retries the write while no new result has
// landed, because a tree that respawns instead of writing in-band leaves the
// stdin handle briefly dead (ErrNoLiveChild) and then live again on a new child.
// Fatals on the overall budget. A duplicate send is harmless: the assertions
// read the FIRST and LAST init model, so an extra turn costs tokens, not truth.
func sendTurnAndWaitResult(t *testing.T, sup sessions.Runner, rec *modelTapRecorder, prompt string)
```

The retry exists for evidence run A, not for the shipped green path, where the first attempt lands. It mirrors
what msgqueue does in production with `ErrNoLiveChild`, so it is idiom, not a workaround. Any error from
`WriteUserTurn` other than `streamsup.ErrNoLiveChild` is a `t.Fatalf`.

### The assertions

`models := rec.initModels()`; require `len(models) >= 2`, `first := models[0]`, `last := models[len(models)-1]`.

| # | Assertion | AC |
|---|---|---|
| A1 | `last != first` | 1 — the child's reported model changed |
| A2 | `last == resolvedID(alias)` | 1 — and changed to the requested model |
| A3 | `runner.State().ChildPID == pidBefore` (and `pidBefore != 0`) | 2 — same process served the last turn |
| A4 | `spawns() == 1` | 2 — exactly one spawn over the whole run |

**First-and-last, never a fixed index.** Whether the `/model` turn emits its own `init` (the 2026-08-19 table
omits the row; the prose says it does, still reporting the *old* model) does not change the verdict, and neither
does a retried duplicate turn. Do not assert `len(models) == 3`.

**Never scan assistant entries for a model.** The `/model` command's own reply carries `model=<synthetic>`,
which is not a real model id. The `init` line is the assertion surface; the JSONL transcript's
`message.model` is optional corroboration at most and is not read by this test — no `interactive_stream_*` test
reads the JSONL today, and adding that route here would blur which observable AC 1 names.

### Which assertion carries which proof

The two assertions are not redundant, and the test's comment must record why — this is AC 3's substance, not
decoration.

| Tree | A1/A2 (model) | A3/A4 (no teardown) |
|---|---|---|
| Current (shipped) | green | green |
| Pre-change `b047b9e^` — `UpdateSettings` ends in an unconditional `sup.Restart(newArgs)` | **green**: the model still changes, via the respawn's recomposed argv | **RED**: a respawn happened |
| Current + mutant: the model `send` removed from `deliverSettingsInBand` | **RED**: no `/model` reached the child | green: nothing was torn down |

Each assertion is the sole red for exactly one mutant. Neither can be dropped.

---

## Concurrency model

No new production goroutines; the test owns two concurrent writers to its own state.

- **One test-owned goroutine:** `go func() { defer close(runDone); _ = runner.Run(ctx) }()`. This is what
  `runActive` does inside the session lifecycle, hoisted into the test.
- **`os/exec`'s stdout copier** drives `modelTapRecorder.Write` while the test goroutine calls `initModels()` /
  `results()`. That is the race `r.mu` exists for; `dropcapRecorder` states the same reason.
- **The slog handler** is called from the `Run` goroutine while the test goroutine reads the count. Guarded by
  its own mutex, captured by the closure — no shared global.
- No channels beyond `runDone`. Every wait is a bounded poll loop (`dropcapWaitForChild`'s shape), which is what
  this package already uses and what keeps a stalled child from parking a goroutine for the rest of the binary.
- **Shutdown**, registered with `t.Cleanup` **immediately after the `Run` goroutine starts**, so a `t.Fatalf`
  anywhere in the drive sequence still tears the child down: `cancel()`, then `select` on `runDone` with a
  bounded wait, `t.Errorf` if `Run` has not returned. `dropcapWaitForChild`'s test registers the identical pair.
- Construction order is load-bearing: recorder and handler exist before `streamsup.New`, which exists before
  `Run` starts, which exists before the cleanup is registered.

---

## Error handling

| Failure | Response |
|---|---|
| `resolveClaudeBin` — claude not on `PATH` | `t.Skipf` (the helper's own). Permitted by AC 5. |
| `WithWorktreeAuthenticated` — no credentials | `t.Skipf` (the helper's own). Permitted by AC 5. |
| `streamsup.New` inside the factory | wrap and return; surfaces as a `sessions.New` error → `t.Fatalf` |
| `sessions.New` | `t.Fatalf` |
| no live child within the spawn budget | `t.Fatalf` naming the budget |
| `pidBefore == 0` | `t.Fatalf` — the instrument cannot answer AC 2 |
| `initModels()` empty, or `models[0] == ""`, after turn 1 | `t.Fatalf` — the tap saw no `init`, so the whole measurement is void |
| both table entries equal `start` | `t.Fatalf` — broken constants, not a skip |
| `UpdateSettings` returns non-nil | `t.Fatalf` |
| `WriteUserTurn` returns `streamsup.ErrNoLiveChild` | retry within the send helper's budget |
| `WriteUserTurn` returns anything else | `t.Fatalf` |
| turn budget exhausted with no new `result` | `t.Fatalf` naming which turn |
| the post-`UpdateSettings` settle wait times out | **tolerated, not fatal** — the pre-change tree has no `/model` turn to close |
| `recorder.Write` | never returns a non-nil error; a failed decode is skipped, an over-cap accumulator is counted |
| `Run` has not returned within the cleanup budget | `t.Errorf` |

**No skips other than the two credential/binary guards.** AC 5 is satisfied structurally: the target-model
selection cannot produce a "nothing to change" condition, so there is no path on which the test declines to run.

---

## Testing strategy

This test *is* the deliverable, so "testing strategy" is the evidence procedure AC 3 demands.

**Compile gate (cheap, run first, run often).** `make check` cannot see this package.

```
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
```

**Green run (current tree).**

```
go test -tags e2e_realclaude -race -v -run TestInteractiveStream_InBandModelChange ./internal/e2e/realclaude/
```

Read the `=== RUN` / `--- PASS` lines, **never the exit code** — a package that fails to build exits 0 through a
shell wrapper, and an unauthenticated machine skips everything and also exits 0.

**Evidence run A — pre-change tree (`b047b9e^`, i.e. `e282de4`).** Confirmed: at that commit
`sessions.Runner` already carries `SetSpawnArgs` (from #1580), so the test file compiles there unchanged; every
API it touches is exported and predates #1581.

```
git worktree add <scratch>/pre-1581 b047b9e^
cp internal/e2e/realclaude/interactive_stream_inband_model_test.go <scratch>/pre-1581/internal/e2e/realclaude/
cd <scratch>/pre-1581 && go test -tags e2e_realclaude -race -v -run TestInteractiveStream_InBandModelChange ./internal/e2e/realclaude/
```

Expected: **A3 and A4 red** (`ChildPID` differs, spawn count 2), **A1 and A2 green** (the respawn's argv carries
`--model <alias>`). Remove the worktree afterwards.

**Evidence run B — mutant on the current tree.** Use `go test -overlay` with an absolute-path JSON map, so
nothing is written into the worktree:

- overlay `internal/sessions/pool.go` with a copy whose `deliverSettingsInBand` drops the
  `if update.Model != nil { send("model", …) }` branch;
- run the same `go test` command with `-overlay=<abs>/overlay.json`.

Expected: **A1 and A2 red** (`last == first`; no `/model` ever reached the child), **A3 and A4 green** (nothing
was torn down).

**Both outcomes go into a comment block in the test file**, naming *which* assertion failed in each case — AC 3
requires the comment, and requires it to distinguish the two, because they are not the same assertion. Record
the observed `initModels()` sequence length from the green run in the same block, so a future reader knows
whether the `/model` turn emitted its own `init` on the measured version.

**Full gate before the PR:** `make preship`. Confirm the live suite's `=== RUN` count is in the expected band
(700s as of August 2026) and that this test appears among them rather than among the by-design skips.

---

## Open questions

1. **Does the `/model` turn emit its own `init` line?** The 2026-08-19 table shows no `init` row for turn 2; the
   prose beside it says the command's own turn does emit one, still reporting the old model. The assertion is
   first-vs-last, so both shapes pass — but the green run should record the observed count in the evidence
   comment rather than leaving the contradiction unresolved in the record.
2. **Turn latency and the settle budget.** `inBandSettleBudget` must be long enough that a healthy `/model` turn
   closes before turn 3 is written (otherwise turn 3 queues behind it — unmeasured behaviour), and short enough
   that evidence run A does not idle for minutes. Start at ~60 s and adjust from the green run's observed
   timings; record the chosen value's justification in a comment.
3. **The resolved-id constants are version-pinned.** Accepted maintenance cost, and the table's comment must say
   that an equality failure means the alias moved, not that the mechanism broke. If this proves annoying in
   practice, the follow-up is a separate ticket to derive the resolution from a cheap probe turn — not to weaken
   the assertion to a substring match, which would stop testing what AC 1 names.
