# #1622 — prove pyry's own Pool delivers a bypass revocation onto a live child without respawning it

**Size:** S (confirmed, not overridden). Zero production source files. One new test file in
`internal/e2e/realclaude`, one signature widening in a sibling test file with zero call-site edits.

---

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`. **The ticket
body's line citations into `interactive_stream_inband_model_test.go` are stale by roughly seven
lines** (it cites `inbandRunner` at `:374`; it is at 381 today, and every other `:NNN` in that
paragraph is off by the same amount). Do not chase them — the names below are the contract.

| Where | Symbol | What to extract |
|---|---|---|
| `internal/e2e/realclaude/interactive_stream_inband_model_test.go` | `TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel` | The whole drive shape this ticket re-points: factory → instrument check → `Run` goroutine → wait-for-child → turn → `UpdateSettings` → settle → turn → assert |
| same | `inbandRunner` | The `sessions.Runner` adapter. **Needs no change** — it embeds `*streamsup.Runner`, which already carries `RevokeBypass` |
| same | `inbandWaitForChild`, `inbandSendTurn`, `inbandWaitResults` | Reused verbatim except for `inbandSendTurn`'s parameter type (see § The two edits) |
| same | `inbandTapRecorder`, its `Write` and `consume` | The line-splitting `io.Writer` shape and the `maxPartial` / `skipUntilNewline` discipline. **Read it as the pattern; do not reuse the type** — its field set is models + result count |
| same | `newInbandSpawnCounter`, `inbandSpawnHandler` | The message-discriminating `slog.Handler` shape. **Read it as the pattern; do not reuse it** — it drops every record but `"spawning claude"`, including the one AC 3 exists to surface |
| same | `inbandBaseArgs` | The trap AC 2 is about: it is literally `--dangerously-skip-permissions`. Copying it defeats this ticket twice over |
| same | `inbandMaxPartial`, `inbandSpawnWait`, `inbandPoll`, `inbandTurnBudget`, `inbandResendAfter`, `inbandRunExitWait` | Package-level constants reused as-is |
| `internal/e2e/realclaude/set_permission_mode_probe_test.go` | `setModeRecorder` and its `add` | **The instrument AC 3 needs, already written.** Captures `control_response` verbatim, `permissionMode` off every `system`/`init`, result count and turn boundaries. Takes a whole line, so it needs a splitter in front of it |
| same | `snapshotControlResponses`, `controlResponseCount`, `snapshotInitModes`, `resultCount`, `snapshotLines`, `nonJSONCount` | The accessors the new test reads through |
| same | `setModeWaitFor` | Already takes a `func() int`. Needs no widening; the control-response wait uses it directly |
| `internal/sessions/pool.go` | `Pool.UpdateSettings` | The no-change early return, the persist, the `SetSpawnArgs` install, the branch |
| same | `inBandDeliverable` | Why a revoke (`YOLO` non-nil and false) returns true and an enable returns false |
| same | `Pool.deliverSettingsInBand` | The `RevokeBypass` call and the `Info` record whose message literal is `"sessions: in-band settings command not delivered"` — note the `sessions: ` prefix |
| same | `Pool.New` | The warm-start branch: `pickBootstrap` selects the entry, `SessionSettings{Model, Effort, YOLO}` is loaded from it, and `bootstrapArgs` is base + `claudeSettingsArgs(settings)` |
| same | `Pool.Default`, `Pool.DefaultSettings` | The two accessors the instrument checks read |
| `internal/sessions/registry.go` | `registryEntry`, `loadRegistry`, `pickBootstrap` | The exact on-disk JSON keys to seed, and the three fail-closed paths that yield a non-bypass child |
| `internal/sessions/session.go` | `claudeSettingsArgs`, `Session.spawnArgs` | The single site that turns `YOLO: true` into `--dangerously-skip-permissions` |
| `internal/sessions/id.go` | `NewID`, `ValidID` | The seeded entry's `id` must be a canonical UUIDv4 — `writeMCPSettings` hard-errors otherwise |
| `internal/streamsup/runner.go` | `Runner.RevokeBypass`, `Runner.nextControlID` | The request id is minted from an internal atomic counter and is **not readable from the test** |
| same | `Runner.spawnAndWait` | `cmd.Stdout = r.cfg.Stdout` — the fact that settles "does the tap see `control_response`" |
| same | `buildArgs` | The fixed `--input-format/--output-format/--verbose` prefix and the `--session-id` / `--resume` suffix wrapped around the pool's argv |
| `docs/knowledge/features/set-permission-mode-inband-probe.md` | — | #1595's live record: the `success` ack, `bypassPermissions` → `default` on the next init, no respawn. This is the borrowed evidence every expected value below rests on |

**One frozen note is wrong and the ticket already settled it.** `docs/knowledge/codebase/1595.md`
explains #1595's divergence from #1582 as needing "the raw `control_response`, which that seam sits
downstream of". `Runner.spawnAndWait` assigns `cmd.Stdout = r.cfg.Stdout`, so `Config.Stdout` **is**
the child's raw stdout sink and carries every line claude writes, `control_response` included. Do not
redesign around the parenthetical. `docs/knowledge/codebase/` is read-only, so this stays recorded
here.

---

## Context

#1595 proved the bytes: a `set_permission_mode` control request carrying `mode: "default"`,
hand-written onto a running child's stdin, drops the bypass posture with no respawn. That test owned
its four children through `exec.CommandContext` and wrote the control line itself.

#1604 built the composed path — `Pool.UpdateSettings` → `inBandDeliverable` → `deliverSettingsInBand`
→ `sup.RevokeBypass()` — and proved it hermetically through a fake runner. That proves the daemon
*asks*. Nothing has yet proved pyry emits those bytes, on the stream it already holds open, to a real
claude.

The gap matters because the failure is silent in both directions. A child that keeps auto-approving
after a revocation is indistinguishable in the daemon log from one that was never in bypass; and
delivery is fire-and-forget — `deliverSettingsInBand` logs a failed `RevokeBypass` at `Info` and
swallows it, so `UpdateSettings` returns `nil` either way.

**Scope boundary, restated so the developer does not widen it.** This ticket asserts that the
revocation *reached* the child and that nothing was torn down. It does **not** claim behavioural
enforcement — an echoed permission mode is claude's report of its own posture. No behavioural probe,
no control arms, no committed fixture: those are the sibling ticket that consumes this harness. No
production code changes (AC 5).

---

## Design

One new file, `internal/e2e/realclaude/interactive_stream_inband_bypass_revoke_test.go`, same
package and same `//go:build e2e_realclaude` tag, so every helper above is callable without copying.

### The seam that has to be bridged, and how

AC 3's field set already exists in `setModeRecorder`, and #1582's `io.Writer` line splitter already
exists in `inbandTapRecorder.Write`. Neither type can be used where the other lives: `setModeRecorder.add`
takes a whole line (it was fed by a `bufio.Scanner` over a `cmd.StdoutPipe()`), and
`inbandTapRecorder`'s splitter is hard-wired to its own two fields.

**Bridge by embedding, not by re-deriving the classifier.** A new type in the new file:

```go
// revokeTap is the streamsup.Config.Stdout sink: #1582's line splitter in front of
// #1595's field capture. It adds a splitter and nothing else.
type revokeTap struct {
	*setModeRecorder            // control_response, permissionMode, results — reused verbatim
	splitMu          sync.Mutex // guards the three splitter fields below ONLY
	partial          []byte
	skipUntilNewline bool
	dropped          int
	maxPartial       int
}

func newRevokeTap() *revokeTap
func (t *revokeTap) Write(b []byte) (int, error) // splits on '\n', calls t.add per line
func (t *revokeTap) droppedCount() int
```

Behaviour of `Write`: byte-for-byte the discipline in `inbandTapRecorder.Write` — accumulate,
split on `'\n'`, hand each complete line to the embedded `add`, discard the accumulator past
`maxPartial` (set to `inbandMaxPartial`) and set `skipUntilNewline` so the tail fragment is
skipped rather than decoded as half a JSON object, copy the residual so the large backing array is
released, and **never return a non-nil error** (this is an `exec.Cmd` stdout sink; an error there
aborts os/exec's copy and can wedge the child).

Why embedding rather than a callback or a second classifier:

- Every accessor AC 3 needs — `snapshotControlResponses`, `controlResponseCount`,
  `snapshotInitModes`, `resultCount`, `snapshotLines`, `nonJSONCount` — is promoted for free, so
  the test reads them off `tap` directly.
- The promoted `resultCount` makes `*revokeTap` satisfy `inbandSendTurn`'s widened parameter with
  no adapter.
- Zero duplication of the classification the ticket forbids re-deriving.

Two consequences to write down rather than discover:

- **Lock order is outer → inner, always.** `Write` holds `splitMu` and calls `add`, which takes
  `setModeRecorder`'s own mutex. No path goes the other way. Name the outer field `splitMu`, not
  `mu`, so a reader is never guessing which one a selector resolves to.
- **`setModeRecorder` retains every line verbatim in `lines`, unboundedly.** This ticket writes no
  fixture, so that retention is unused. It is accepted rather than trimmed: reusing `add` unmodified
  is worth more than the bytes, the volume is one short two-turn session (the same shape #1595 already
  ran through this type), and `maxPartial` still bounds the only accumulator an adversarial single
  line can grow. Say so in the type's doc comment so it reads as a decision, not an oversight.

### The log recorder — #1582's counter drops exactly the record AC 3 asks for

`newInbandSpawnCounter` returns a handler that counts `"spawning claude"` and writes nothing else.
Reused verbatim it discards `deliverSettingsInBand`'s `Info` record. A new one in the new file:

```go
// newRevokeLogRecorder returns a slog.Handler that WRITES NOTHING, counts spawns, and
// retains the daemon's own account of a failed in-band delivery.
func newRevokeLogRecorder() (h slog.Handler, spawns func() int, notDelivered func() []string)
```

Contract:

- `Enabled` returns true at every level — the target record is `Info`.
- `Handle` discriminates on exact message match, two literals: `"spawning claude"` increments the
  spawn count; `"sessions: in-band settings command not delivered"` appends one formatted line built
  from the record's attrs (`session`, `setting`, `err`) via `Record.Attrs`. Every other record is
  dropped, which is also what keeps the runner's lifecycle lines out of the test output.
- `WithAttrs` / `WithGroup` return the receiver, mirroring `inbandSpawnHandler`. Safe here because
  the pool installs `cfg.Logger` directly with no `.With` chain, and `deliverSettingsInBand` puts
  every attr inline on the record.
- Nothing this handler retains is a secret: `RevokeBypass` takes no mode, so there is no settings
  value the bypass record could carry (`deliverSettingsInBand`'s doc makes that structural claim).

### The stored-posture seam — AC 2

The child's bypass must come from the registry, never from the base argv. Three concrete
requirements, each of which has its own way of silently producing a run that measures a child nobody
revoked:

1. **The base argv carries no bypass flag.** Declare it as a named, deliberately-empty var:

   ```go
   // revokeBaseArgs is EMPTY, and that is the whole point of AC 2. #1582's
   // inbandBaseArgs is []string{"--dangerously-skip-permissions"}; copied here it
   // survives Session.spawnArgs' recompose, so the installed next-spawn argv keeps
   // the bypass and the run measures a child nobody revoked. The bypass posture on
   // this run comes from the seeded registry entry and from nowhere else.
   var revokeBaseArgs []string
   ```

2. **The registry file exists before `sessions.New`.** #1582 points `RegistryPath` at a fresh
   `t.TempDir()` path that does not exist — the cold-start shape, where `loadRegistry` returns
   `(nil, nil)` and settings come out zero-valued. A helper writes it first:

   ```go
   // seedBypassRegistry writes a one-entry sessions.json whose bootstrap entry carries
   // yolo:true, and returns the id it minted.
   func seedBypassRegistry(t *testing.T, path string) sessions.SessionID
   ```

   `registryEntry` is unexported, so marshal an anonymous struct with the json tags
   `registryEntry` declares: `version` at the top level; per entry `id`, `created_at`,
   `last_active_at`, `bootstrap: true`, `yolo: true`. Version is `1`. The id comes from
   `sessions.NewID()` — not a hand-written string: `writeMCPSettings` gates the warm-start id on
   `ValidID` and hard-errors on anything that is not a canonical UUIDv4, and claude receives it as
   `--session-id`. Timestamps are `time.Now().UTC()`. Write at `0600`. `label` and
   `lifecycle_state` are omitted — the first decodes to empty and the second is ignored for the
   bootstrap entry by construction.

3. **The seed is verified before a single token is spent** (see instrument check B below).
   `bootstrap: true` missing → `pickBootstrap` returns nil → cold start → YOLO false. A malformed
   `yolo` value → the whole `loadRegistry` parse fails closed → `sessions.New` errors. A misspelled
   json key → decodes to false. All three land on the same deterministic guard.

### The two edits

**New file** — everything above, plus the test.

**`interactive_stream_inband_model_test.go`** — one widening, zero call-site edits.
`inbandSendTurn` is typed `rec *inbandTapRecorder` but reads only `rec.resultCount()`. Introduce
next to it:

```go
// inbandResultCounter is the one thing inbandSendTurn reads off a recorder: the
// running count of turn boundaries. Both this file's inbandTapRecorder and #1622's
// revokeTap expose it, so the drive helper is shared rather than copied.
type inbandResultCounter interface{ resultCount() int }
```

and change the parameter to `rec inbandResultCounter`. `*inbandTapRecorder` satisfies it directly,
so both existing call sites compile unchanged. Amend the one line of `inbandSendTurn`'s doc comment
that names the concrete type. Nothing else in that file moves — in particular **do not** touch
`inbandBaseArgs`, `inbandTapRecorder` or `newInbandSpawnCounter`.

---

## Data flow

```
seedBypassRegistry ──► sessions.json {bootstrap:true, yolo:true}
                              │
                              ▼  (loadRegistry → pickBootstrap → SessionSettings{YOLO:true})
                        sessions.New
                              │  bootstrapArgs = ["--settings", f] + claudeSettingsArgs(YOLO:true)
                              ▼                                        = + "--dangerously-skip-permissions"
                     RunnerFactory ──► streamsup.New{Stdout: revokeTap}
                              │
                              ▼  spawnAndWait: cmd.Stdout = cfg.Stdout
                         claude child ──── stdout ────► revokeTap.Write ──► setModeRecorder.add
                              ▲                                              │
                              │ stdin                                        ├─► control_response
                              │                                              ├─► init.permissionMode
   Pool.UpdateSettings{YOLO:&false}                                          └─► result / boundaries
        │ merged != stored ✓ (stored is true)
        │ inBandDeliverable ✓ (YOLO present and false)
        ├─► sup.SetSpawnArgs(recomposed, now bypass-free)
        └─► deliverSettingsInBand ─► sup.RevokeBypass() ─► WriteBypassRevocation ──┘
                     │
                     └─(on error)─► p.log.Info "sessions: in-band settings command not delivered"
                                                        └──► newRevokeLogRecorder.notDelivered()
```

---

## The drive sequence

Named `TestInteractiveStream_InBandBypassRevoke_LiveChildReportsDefaultMode`. Steps, in order:

1. `resolveClaudeBin(t)` and `WithWorktreeAuthenticated(t)` — the package's two standard skips.
2. `MkdirAll` a fresh empty non-git workdir under the pinned `$HOME`, same reason as #1582: less
   project context to load, cheaper turns.
3. `seedBypassRegistry(t, filepath.Join(t.TempDir(), "sessions.json"))` — **before** `sessions.New`.
4. Construct the tap and the log recorder.
5. `sessions.New` with `Bootstrap.ClaudeArgs: revokeBaseArgs`, the seeded `RegistryPath`, the
   capturing factory, and `Logger: slog.New(handler)`. `Pool.Run` is deliberately not called, for
   the reason #1582 records: `UpdateSettings` consults neither the conversations sweep, the idle
   timer nor the settings-file reaper.
6. **Instrument check A** — `pool.Default().Runner()` must be the very object the factory captured.
   Fatal otherwise. #1582's argument applies verbatim: a future refactor handing the pool a
   different runner leaves every assertion below reading a bystander.
7. **Instrument check B** — `pool.DefaultSettings()` must report `ok` and `YOLO == true`. Fatal
   otherwise, with a message naming the seed. This is the deterministic half of AC 2 and it fires
   **before any child is spawned**, so all three seed failure modes cost zero tokens.
8. Start `tap.Run(ctx)` on a goroutine; register the cancel-and-wait `t.Cleanup` immediately after,
   so a `t.Fatalf` anywhere below still tears the child down.
9. `inbandWaitForChild`; read `pidBefore` off `State().ChildPID`; fatal on zero.
10. Turn 1 through `inbandSendTurn`.
11. **Instrument check C** — `snapshotInitModes()` must be non-empty and its **first** entry must be
    `"bypassPermissions"`. Fatal otherwise: the child was never in bypass, so AC 2's seam did not
    take and there is nothing to revoke. This is the live half of AC 2, and placing it *before* the
    revocation is what keeps its red from being confused with a delivery failure.
12. Snapshot `controlResponseCount()` as the baseline (expected 0 — nothing else on this run issues
    a control request).
13. `pool.UpdateSettings(pool.Default().ID(), sessions.SettingsUpdate{YOLO: &no})` where `no` is
    `false`. Fatal on a returned error. **YOLO only** — a non-nil `Model` or `Effort` would add a
    `/model`/`/effort` turn and, if empty, would route the whole frame onto the restart path.
14. `setModeWaitFor(tap.controlResponseCount, baseline+1, revokeControlBudget)`. **Tolerated on
    timeout** — `t.Logf` and continue. This is the one wait that must not be fatal: a tree that does
    not deliver produces no response, and the run has to reach its assertions rather than die here.
15. Turn 2 through `inbandSendTurn`.
16. One `t.Logf` evidence block, then the verbatim `control_response` lines and the `notDelivered`
    records, each on its own line.
17. Assertions A1–A4.

### Waiting on the response, not on a result

The revocation is a **control request, not a turn**. Two consequences that a copy of #1582 gets
wrong:

- There is no result line to wait for. #1582 settles on `inbandWaitResults(rec, 2, …)`; here the
  settle is `setModeWaitFor(tap.controlResponseCount, …)`, which needs no widening because
  `setModeWaitFor` already takes a `func() int`.
- It emits **no `init` line of its own**. #1582 measured three init lines because `/model` is an
  ordinary user turn; expect **two** here — turn 1's `bypassPermissions` and turn 2's `default`.
  Assertions still read **first and last, never a fixed index**: `inbandSendTurn`'s resend can add a
  turn, and an extra init line changes no verdict. Do not assert `len(modes) == 2`.

### The response cannot be correlated by id — correlate by window

`Runner.RevokeBypass` mints its `request_id` through `nextControlID`, an unexported per-runner atomic
counter. The test cannot read it, and `setModeResponseIDMatches` is therefore not reusable here. Do
**not** hard-code `"1"` on the reasoning that this is the runner's first control request: that pins a
private counter's start value as a test contract. Correlate by arrival window — baseline before
`UpdateSettings`, strictly greater after — and log the responses verbatim so a reader can check the
id, the subtype and the echoed mode by eye.

### The prompts must not use tools

This is load-bearing and it is the single most likely way this test hangs. After the revocation the
child is in `default` mode with no `--permission-prompt-tool` wired and a `Config.Stdout` occupied by
a recorder that answers nothing. A tool-using prompt on turn 2 would block on an approval that can
never arrive, burn `inbandTurnBudget`, and die in `inbandSendTurn` with a message about a missing
result rather than about permissions. Declare two one-word-reply prompts as this file's own consts
with that constraint in the doc comment — do not reuse `inbandPromptOne`/`inbandPromptTwo`, whose
file has no reason to keep them tool-free.

---

## Error handling and failure modes

| Failure | Where it surfaces | Verdict |
|---|---|---|
| Registry seed did not take (missing file, missing `bootstrap`, misspelled key) | Instrument check B, pre-spawn | Fatal, zero tokens spent |
| Malformed `yolo` value | `sessions.New` returns an error (fail-closed parse) | Fatal at construction |
| Pool hands the test a different runner | Instrument check A | Fatal |
| Child launched without bypass despite a good seed | Instrument check C, after turn 1 | Fatal — names AC 2 |
| Revocation never written (`ErrNoLiveChild`, pipe failure) | `notDelivered()` non-empty **and** A1 red | A1; the log record is the daemon's own account |
| Revocation written, claude never acks | A1 red | A1 |
| Ack arrives but posture does not move | A2 red, response logged verbatim | A2 |
| Child torn down across the change | A3 and A4 red | A3, A4 |
| Over-long stdout line | `droppedCount()` non-zero in the evidence block | Logged, not asserted — tells a reader the init record has a hole |

**`notDelivered()` is logged, never asserted.** Its only reachable non-empty case already reddens A1,
so an assertion there would be a second red for one event. What AC 3 asks for is that the
fire-and-forget swallow become *readable*, and a logged record is exactly that.

**The `control_response` subtype is logged, never asserted.** AC 4 names two assertions — a response
arrived, and the mode moved. An `error`-subtype response satisfies the first and reddens the second,
so the pair is already complete; asserting the subtype adds a third red for a case A2 owns.

---

## Assertions — which one carries which proof

Numbered A1–A4 in the file, in #1582's idiom, each with a failure message that says what it means
rather than what it compared.

- **A1** — a `control_response` arrived after the revocation was issued
  (`controlResponseCount() > baseline`). *The revocation reached the child and claude answered it.*
- **A2** — the last `init.permissionMode` is `"default"`. Given instrument check C already fixed the
  first at `"bypassPermissions"`, this **is** AC 4's `bypassPermissions → default` move; the failure
  message prints the whole slice and both endpoints so the move is legible without re-reading the
  guard.
- **A3** — `pidAfter == pidBefore`. *The same process served both turns.*
- **A4** — `spawns() == 1`. *Exactly one spawn over the whole run.*

They are two independent pairs and neither pair is redundant — the mutant table below is what makes
that a measurement rather than a claim.

---

## Testing strategy

`make check` never compiles this package. The developer's deterministic gate is:

```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
go build -tags e2e_realclaude ./...
make check
```

The live run:

```bash
go test -tags e2e_realclaude -race -v -count=1 \
  -run TestInteractiveStream_InBandBypassRevoke ./internal/e2e/realclaude/
```

**Read the count of tests that executed, never the exit code.** A build failure and a full
credentials skip both exit 0. A skip is not a pass — if credentials are unavailable in the run
environment, say so explicitly and leave the live evidence to the operator under the
`needs-real-claude` label; do not report AC 1 satisfied.

### Required evidence — three live runs

The package convention is a `# Evidence: which assertion carries which proof` section in the file
header, and this ticket is where its rows are produced. Mutate through `go test -overlay=<abs path
to overlay.json>` so no mutated source is ever written into the worktree.

| Run | Mutation | Predicted red set |
|---|---|---|
| Green | none | none — A1–A4 all pass |
| **M1** | drop the `update.YOLO != nil && !*update.YOLO` clause from `Pool.deliverSettingsInBand` | A1, A2 red; A3, A4 green — nothing was written and nothing was torn down |
| **M2** | make `inBandDeliverable` return false for a revoke (the pre-#1604 shape), so `UpdateSettings` falls through to `sup.Restart(newArgs)` | A1, A3, A4 red; **A2 green** — the respawned child reports `default` off its recomposed bypass-free argv |

M2 is the row that earns the assertion set. A2 alone cannot tell a delivered revocation from a
respawn under a rebuilt argv; A1, A3 and A4 are what separate them. M1 is the row that earns A1 and
A2 against a tree that tears nothing down and also does nothing.

Two predictions about wall clock, so a slow red is not mistaken for a hang: on M1 and M2 no
`control_response` ever arrives, so the tolerated wait at step 14 burns its full budget — expect
those runs to take roughly `revokeControlBudget` longer than the green one. On M2, `inbandSendTurn`'s
`ErrNoLiveChild` retry and its resend-after path are what carry turn 2 across the respawn; that is
the reason those two behaviours are reused rather than simplified away.

**Record the measured values, not the predicted ones.** The table above is this spec's prediction. If
a measured red set differs from it, that difference is a finding to write into the file header and
report on the ticket — not a number to reconcile toward the prediction.

### Budgets and constants

Reuse `inbandSpawnWait`, `inbandPoll`, `inbandTurnBudget`, `inbandResendAfter`, `inbandRunExitWait`
and `inbandMaxPartial` unchanged. Declare one new budget:

- `revokeControlBudget` — the wait for the ack at step 14. Use #1595's measured-adequate 45s (its
  `setModeControlBudget`), long enough that an absent response means absence rather than impatience.

Declare no `--model` and no `--max-turns`: the run is two one-word turns on claude's own machine
default, and every extra flag is another thing the recompose has to be reasoned about.

---

## Open questions

1. **Does the init line still report `bypassPermissions` under pyry's full argv?** #1595 measured it
   on a hand-built argv with no `--settings` and no `--session-id`. The pool adds both, plus
   `--resume`/`--session-id` from `buildArgs`. `writeMCPSettings` writes only
   `enableAllProjectMcpServers` and `skipDangerousModePermissionPrompt`, neither of which touches
   the permission mode, and #1582 already ran a bypass-launched pool child through this exact wiring
   — so the expectation is well founded but unmeasured on *this* argv. Instrument check C is where it
   is settled, and its red is informative rather than confusing.
2. **If the composed path does not deliver, that is a finding, not a patch.** AC 5 is absolute. Record
   the measured evidence block in the file header and on the ticket, and route back. Do not adjust
   `internal/sessions` or `internal/streamsup` from inside this ticket.
3. **Out of scope, and flagged because merged prose says otherwise.**
   `Pool.deliverSettingsInBand`'s doc comment states "#1605 measures the in-flight window live" — a
   revoke arriving *during* a dispatched tool call. #1605 carried no such scope and neither this
   ticket nor its behavioural sibling does. Tracked as #1624. It is a different probe shape; do not
   fold it in, and do not correct the comment here (that is a production edit AC 5 forbids).

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. One boundary is added, and it is explicit: claude's stdout
  crosses into test-process state at a single function, `revokeTap.Write`, which hands whole lines to
  the one existing classifier (`setModeRecorder.add`). Nothing parses the stream in a second place.
  Everything downstream of `add` is already-decoded scalars and retained `json.RawMessage`; the test
  makes no trust claim about them beyond comparing two string fields. The `control_response` is read
  for arrival and logged verbatim, never executed, never fed back to the child, and never used to
  select a code path.
- **[Trust boundaries]** No findings on the reverse direction. The only bytes this run writes to the
  child are two hard-coded prompt constants through `WriteUserTurn` and one revocation minted
  entirely inside `WriteBypassRevocation` — the mode is fixed in the writer and this test supplies
  no part of either envelope.
- **[Tokens, secrets, credentials]** No findings. No token is created, stored or compared. The
  credential reaches the child through the environment via `WithWorktreeAuthenticated`; the argv
  carries none, which is why the evidence block may log argv-adjacent facts freely. The one
  correlation value in play — `RevokeBypass`'s `request_id` — is a locally-minted counter and
  explicitly not a security token; the design's refusal to hard-code or assert it is about test
  brittleness, not secrecy. **Note for the developer:** the evidence block logs `control_response`
  lines verbatim. That is claude's own ack (`subtype`, `request_id`, echoed `mode`) and carries no
  operator data — but it goes to test output, so do not extend the verbatim dump to arbitrary stdout
  lines (`snapshotLines`) without re-reading this.
- **[File operations]** No findings, one non-obvious requirement discharged. The one file this ticket
  writes is the seeded `sessions.json`, at `0600`, inside `t.TempDir()`. Its `id` is not
  caller-typed: it comes from `sessions.NewID()` and is consumed by `writeMCPSettings`, which joins
  it into a path and therefore gates it on `ValidID` — a hand-written id containing a separator or a
  `..` segment would be rejected there, and the design routes around that hazard by never producing
  one. No path in this design concatenates test-controlled input into a filesystem path, no
  check-then-use pair exists, and no symlink is followed. The seed is a fresh write to a
  fresh-per-test path, so it needs no temp-and-rename — nothing else can observe a partial state, and
  `sessions.New` reads it exactly once at construction, after the write returns.
- **[Subprocess / external command execution]** No findings. The test constructs no `exec.Command`;
  the argv reaching claude is composed entirely by `Pool.New` and `buildArgs` from a **deliberately
  empty** base plus `claudeSettingsArgs`. That empty base is itself the security-relevant choice: it
  is what makes `--dangerously-skip-permissions` traceable to one place, the seeded `yolo` key, so
  the run cannot silently launch a bypass child by an unaudited second route. No `sh -c`. Teardown is
  the `t.Cleanup` cancel of the `Run` context, which `exec.CommandContext` propagates; the cleanup is
  registered immediately after the goroutine starts precisely so a `t.Fatalf` in any later step
  cannot leave a bypass-launched claude alive.
- **[Cryptographic primitives]** Not applicable, with one thing worth naming rather than skipping:
  the only randomness in the design is the session id, and it is drawn from `crypto/rand` inside
  `sessions.NewID` rather than minted in the test. The design's requirement to call `NewID` rather
  than write a literal keeps it that way.
- **[Network & I/O]** No findings. No sockets. The one unbounded-input surface is the child's stdout,
  and the accumulator is capped at `inbandMaxPartial` with the over-long tail skipped rather than
  decoded. The retained-line slice in `setModeRecorder` has **no** cap — audited and accepted above:
  it is bounded in practice by a two-turn session's output, it is the same exposure #1595 already
  ran, the process is a `go test` binary on the operator's own machine, and the producer is claude
  rather than a hostile peer. If a future ticket points this recorder at an adversarial or long-lived
  stream, that cap becomes required.
- **[Error messages, logs, telemetry]** No findings; the design tightens rather than relaxes the
  #833 posture. The log recorder drops every record but two exact message literals, so the runner's
  and pool's other lines never reach test output. The one retained daemon record cannot carry a
  settings value by construction — `RevokeBypass` takes no mode, which is the structural guarantee
  `deliverSettingsInBand`'s doc already makes. No telemetry, no aggregation, no user-identifiable
  data.
- **[Concurrency]** No findings. Two mutexes are introduced and their order is stated and
  one-directional: `revokeTap.splitMu` → `setModeRecorder.mu`, never the reverse; the distinct field
  name exists so a reader cannot misread which one a selector takes. `Write` is driven by os/exec's
  stdout copier while the test goroutine polls the accessors, which is exactly the race both mutexes
  exist for. One goroutine is spawned (`tap.Run`) and it exits on the `t.Cleanup` cancel, which is
  waited on with a bounded `inbandRunExitWait` and reported as an error if it does not return — no
  leak, and no silent leak either. No shared state is checked-then-mutated outside a lock: the
  control-response baseline is read before `UpdateSettings` and compared after, and a stale read
  there can only make A1 harder to pass, never easier.
- **[Threat model alignment]** No new surface, and the relevant prior analysis is inherited rather
  than re-derived. #1604's own security review recorded the in-band revocation as
  privilege-*reduction* only, behind a relay capability check that already grants the strictly
  stronger power of enabling bypass — and #1595 recorded that claude refuses the escalation direction
  outright, gating it on the launch argv. This ticket adds no writer, no verb and no reachable
  surface: it is a build-tagged test that exercises the merged path. The one threat it does touch is
  its own: a run that measures a child nobody revoked would report a false green on a
  security-relevant mechanism, which is why AC 2's seam gets **two** guards (deterministic check B
  pre-spawn, live check C post-turn-1) rather than a code-reading argument.
- **[In-flight tool call during a revocation]** OUT OF SCOPE — tracked as **#1624**, and named here
  because merged production prose (`Pool.deliverSettingsInBand`'s doc) still points that measurement
  at the closed #1605. A different probe shape; not folded in, and not corrected from inside this
  ticket because AC 5 forbids the production edit.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
