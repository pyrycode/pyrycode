# #1655 — Measure whether a turnless `--session-id` launch leaves an `<id>.jsonl`

**Ticket:** [#1655](https://github.com/pyrycode/pyrycode/issues/1655) · **Size:** S · **Labels:** `security-sensitive`, `needs-real-claude`

One new file: `internal/e2e/realclaude/session_transcript_probe_test.go`. **No production files.**

---

## Files to read first

Read in this order. Each entry names the symbol and what to take from it.

| File | Symbol | What to extract |
|---|---|---|
| `internal/e2e/realclaude/interactive_stream_new_session_test.go` | `streamNewSessionTranscriptDir` | The empirical dir finder AC 1 mandates. Signature `(t, home, id string, timeout) string`; polls `<home>/.claude/projects/*/` for `<id>.jsonl`, `t.Fatalf`s with a tree listing on timeout. **Call it. Do not edit it** — `TestInteractiveStreamNewSessionRotatesAndSpawnsFresh` depends on it. Also read `requireTranscriptAppears` and `projectsTree` in the same file for the polling and diagnostic idiom. |
| `internal/e2e/realclaude/set_permission_mode_probe_test.go` | `runSetModeChild` | The working precedent for launching claude directly via `exec.CommandContext` with a stream-json argv, holding stdin, capturing stdout/stderr. Take the launch shape and the "record every outcome, Fatal only on a broken instrument" discipline. **Do not** take its recorder/fixture surface — see § Deliberate non-goals. |
| `internal/e2e/realclaude/set_permission_mode_probe_test.go` | `setModeTurnLine` | The user-turn stream-json envelope the control arm writes. Same package, same build tag — call it, do not re-marshal a second envelope shape. |
| `internal/e2e/realclaude/permission_protocol_spike_test.go` | `captureClaudeVersion`, `truncateString`, `packageDir` | Version capture for the record (returns raw + leading token) and the byte-cap helper for captured streams. This file is also the **nearest size analogue**: a full live-claude evidence spike, fixture writer included, in 276 lines. |
| `internal/e2e/realclaude/fixtures.go` | `WithWorktreeAuthenticated`, `WithWorktree` | Pins `$HOME` to a per-test tempdir via `t.Setenv` and re-pins whichever credential is present. Two consequences the design leans on: the projects scan is hermetic, and **both arms must live in one test function** (a second function gets a different `$HOME`). |
| `internal/transcript/transcript.go` | `StatByID`, `Result.Found`, `ValidStem` | The by-id existence primitive #1630 will consume. Absent file ⇒ `(Result{}, err)` wrapping the raw `os.Stat` error — that is the expected answer here, not a failure. |
| `internal/sessions/reconcile.go` | `DefaultClaudeSessionsDir`, `encodeWorkdir` | The production recomputation AC 1 compares against. `EvalSymlinks` then encode-every-non-alphanumeric; resolves `$HOME` through `os.UserHomeDir`, so it lands under the pinned temp home. |
| `internal/agentrun/workdir.go` | `ResolveWorkdir`, `canonicalCase` | The canonicalisation streamsup applies to `Config.WorkDir` before setting `cmd.Dir`. Applies `canonicalCase` *on top of* `EvalSymlinks` — the asymmetry against `DefaultClaudeSessionsDir` that AC 1's comparison exists to record. |
| `internal/streamsup/runner.go` | `buildArgs`, `spawnAndWait`, `killGrace` | `buildArgs` is the argv shape both arms must mirror on a first spawn. `spawnAndWait` shows the `cmd.Cancel` (SIGTERM + reap) / `cmd.WaitDelay` pairing and its doc comment records that a spontaneous crash never invokes `cmd.Cancel`. `killGrace` is the 5 s window AC 3's termination mirrors. |
| `docs/knowledge/features/set-permission-mode-inband-probe.md` | § "`init.permissionMode` after the change" | The measured fact that **`init` is emitted per turn, not at spawn** — the reason a turnless arm can never use an `init` line as its liveness signal. Also the doc shape AC 4 asks the record to match. |
| `docs/knowledge/features/transcript-package.md` | — | Where the by-id/probe/newest primitives are meant to be used from, and the not-found conventions of the two adapter families. |
| `docs/knowledge/features/e2e-realclaude.md` | — | Package conventions: credential gating, fixed-id reservations, what the suite is allowed to assume. |
| `docs/knowledge/decisions/032-bootstrap-resume-per-spawn-existence-probe.md` | § Context | The rule #1630 carries into streamsup, and one hazard worth knowing: claude **refuses** `--session-id <uuid>` when `<uuid>.jsonl` already exists (~220 ms exit). Both probe ids must therefore be fresh — they are, under a fresh `$HOME`. |

---

## Context

`buildArgs` picks a spawn's id flag from a one-way latch: `--session-id` on the first spawn, `--resume`
on every one after. The latch advances on *launched*, not on *established on disk* — the comment on
`spawnAndWait` says so in as many words. The suspected crash-loop (observed 2026-08-18) is a session
that launches, runs no turn, leaves no transcript, and then gets `--resume`d against an id claude
never created.

That story rests on a claim about real claude this repo has never measured: **a claude launched under
`--session-id <id>` that runs no turn leaves no `<id>.jsonl` on disk.** ADR 032 already decided the
rule that fixes the loop (resume iff the transcript exists, by-id, no dir scan) and applied it to the
PTY bootstrap path; #1630 carries it into streamsup and consumes this measurement.

Three properties make this harder than "stat a file", and they drive the whole design:

1. **The fact is an absence, and every setup failure produces the same absence.** A wrong directory,
   a claude that never launched, a rejected flag — each reads identical to the fact holding. Hence the
   control arm, in the same run and the same directory, and hence the directory being found
   *empirically* rather than recomputed.
2. **The two readings are not interchangeable.** #1630's probe runs at respawn, after the previous
   child is gone. A transcript flushed at process exit reads absent while the child is alive and
   present at respawn — under which #1630's fix would emit `--resume` and loop anyway.
3. **The second reading is only as good as how the child was ended.** A flush-at-exit is not performed
   under `SIGKILL`, so an absence read after a force-kill is an artefact of the signal.

**No ADR.** ADR 032 already carries the decision; this ticket supplies its missing premise. The record
belongs in a feature doc, not a new decision record.

### AC 4's owner is the documentation phase, not the developer

AC 4 puts the record at `docs/knowledge/features/session-transcript-and-resume-probe.md`. The
developer is barred from writing under `docs/knowledge/` (that rule postdates #1595, whose probe doc
did ship inside the developer's commit), and the architect is barred from creating files there. The
only legal writer is the documentation phase.

So AC 4 is met in two halves, and the split is a spec requirement, not a developer judgement call:

- **Developer:** the test emits the entire record as one self-contained, copy-pasteable block
  (§ The record), and the developer pastes that block **verbatim into the PR body**. Nothing in the
  doc requires re-derivation, a second live run, or reading the diff.
- **Documentation phase:** writes `docs/knowledge/features/session-transcript-and-resume-probe.md`
  from that block, to the outline in § What the doc must contain, and appends the `INDEX.md` line.

If the developer has no credentials locally, the block comes from the dispatcher's post-review live
run (this ticket is `needs-real-claude`) — the doc is written from whichever run actually executed.
**Code review: AC 4 is not a developer deliverable. Do not fail the branch for the doc's absence.**

---

## Design

### One file, one live test, one credentials-free test

`internal/e2e/realclaude/session_transcript_probe_test.go`, `//go:build e2e_realclaude`, package
`realclaude`.

- `TestRealClaude_TurnlessSessionIDTranscript` — the live measurement. No `t.Parallel` (the fixture
  calls `t.Setenv`). Both arms run inside it, sequentially, sharing one `$HOME` and one workdir.
- `TestTurnlessTranscriptVerdict` — a table test over the pure classifier. No subprocess, no
  credentials; it passes on a machine with no claude at all. It is the only non-live proof that the
  `SIGKILL` ⇒ INCONCLUSIVE rule is actually wired rather than merely described.

### Fixed identifiers

```go
const (
    transcriptProbeControlID  = "16550000-0000-4000-8000-000000000001" // arm A, driven through one turn
    transcriptProbeTurnlessID = "16550000-0000-4000-8000-000000000002" // arm B, given no turn
)
```

Ticket-prefixed, matching the `10290000-…` / `11770000-…` / `16340000-…` convention already in this
package; both are valid UUIDv4 stems (version nibble `4`, variant nibble `8`) and neither collides
with any fixed id the package already reserves.

### Argv — one builder, both arms

```go
// transcriptProbeArgs returns the argv shape buildArgs emits on a FIRST spawn —
// the fixed stream-json prefix, the caller's base args, then --session-id <id>.
func transcriptProbeArgs(sessionID string) []string
```

Emits `--input-format stream-json --output-format stream-json --verbose --model <model>
--max-turns <n> --session-id <sessionID>`. The `--model` / `--max-turns` pair is the precedent probe's
addition to the shape; nothing else is added, and `-p` / `--print` is never emitted.

**Both arms call this one function.** That is what makes AC 2's liveness argument structural rather
than asserted: the argvs are identical by construction, differing only in the id, so the control arm
having driven a turn through this shape *is* proof that this claude version accepts arm B's flags. An
assertion comparing the two would be comparing a function against itself — do not write one. Record
both argv slices verbatim instead, so a reader can check the claim without trusting it.

### Workdir and the two directories

```
home     := WithWorktreeAuthenticated(t)                     // $HOME pinned; skips cleanly with no creds
workdir  := filepath.Join(home, "session-transcript-probe-work")   // MkdirAll 0o700, empty, not a git repo
childCwd := agentrun.ResolveWorkdir(workdir)                 // EvalSymlinks + canonicalCase — what streamsup does
cmd.Dir   = childCwd                                          // for BOTH arms
```

Setting `cmd.Dir` to the *resolved* path is load-bearing for AC 1: streamsup resolves `Config.WorkDir`
through `ResolveWorkdir` and only then assigns `cmd.Dir`, so a probe that passed the literal path
would be measuring the test's cwd choice rather than production's.

Then the two directories AC 1 compares:

- **Empirical (authoritative):** `dir := streamNewSessionTranscriptDir(t, home, transcriptProbeControlID, …)`.
  Called with the *control* id, it is simultaneously AC 1's non-vacuity guard and the pin every later
  read uses. Its `t.Fatalf` is exactly AC 1's "the run fails rather than recording a verdict".
- **Recomputed (compared-against only):** `sessions.DefaultClaudeSessionsDir(childCwd)`.

Both paths, `childCwd`, and a boolean match go into the record. **A divergence is a recorded outcome,
not a test failure** — it is the finding the follow-up that supplies this directory on the daemon's
production path needs, and it is the reason the comparison belongs in the doc rather than only in a
test log. `ResolveWorkdir` applies `canonicalCase` where `DefaultClaudeSessionsDir` applies
`EvalSymlinks` alone; that asymmetry is the plausible source.

### Sequence

```
   ┌─ arm A (control) ─────────────────────────────────────────────────────┐
   │ start(argv(A), cwd=childCwd)  →  write ONE turn  →  close stdin  →  Wait │
   └───────────────────────────────────────────────────────────────────────┘
                                    │
              dir := streamNewSessionTranscriptDir(home, A)   ← Fatal if absent
              recomputed := DefaultClaudeSessionsDir(childCwd); record both
                                    │
   ┌─ arm B (turnless) ────────────────────────────────────────────────────┐
   │ start(argv(B), cwd=childCwd)   stdin held OPEN, nothing ever written    │
   │   ↓ settle                                                             │
   │   alive? ─── no ──→ Fatal, reporting exit status + stderr   (AC 2)      │
   │   ↓ yes                                                                │
   │   READING 1 := StatByID(dir, B)   ── then re-check alive                │
   │   ↓                                                                     │
   │   SIGTERM ─ grace(5s) ─ SIGKILL ─ hard wait   → termination mode        │
   │   ↓                                                                     │
   │   READING 2 := StatByID(dir, B), polled over a short post-exit settle    │
   └───────────────────────────────────────────────────────────────────────┘
                                    │
                        classify → record → t.Logf
```

Arm A must complete before arm B starts: AC 3 requires the control transcript established before
reading 1, and AC 2's liveness argument requires the control turn to have already proved the argv.

**Arm B's stdin is opened and never written to, and never closed before reading 1.** Holding it open
is what mirrors streamsup, which keeps the handle for the child's whole life; closing it would end the
session and conflate "ended by us" with "ended by EOF", and could itself trigger the flush the second
reading is trying to attribute.

### Termination — explicit, not inferred

Arm B is ended the way the production path ends one, and *which* signal ended it is observed
directly rather than deduced from `os/exec` error semantics:

```go
type terminationMode string // "sigterm" | "sigkill" | "did-not-exit"

// endTurnlessChild sends SIGTERM, waits up to grace, escalates to SIGKILL, then
// waits up to a hard bound. Returns which signal the child actually exited under
// and how long the exit took. Never Fatals: "did-not-exit" is a recordable outcome.
func endTurnlessChild(cmd *exec.Cmd, exited <-chan struct{}, grace, hard time.Duration) (terminationMode, time.Duration)
```

`grace` is `5 * time.Second`, declared as a local const whose comment names streamsup's `killGrace`
(the production value cannot be imported — it is unexported). Do **not** infer the mode from
`errors.Is(waitErr, exec.ErrWaitDelay)`: that couples a recorded fact to a subtle `WaitDelay`
interaction, and the recorded fact is the whole deliverable.

`exec.CommandContext` still wraps arm B with a generous outer deadline as a leak guard. If that outer
context is what kills the child, the mode records as `did-not-exit` — the default context kill is a
`SIGKILL`, so it cannot be read as a graceful exit.

### Readings

```go
type probeReading struct {
    Found     bool   `json:"found"`
    Path      string `json:"path"`
    Size      int64  `json:"size"`
    StatError string `json:"stat_error"` // the raw os.Stat error text; absence is the expected answer
}
```

Both readings go through `transcript.StatByID(dir, transcriptProbeTurnlessID)` — the same primitive
#1630's probe will call, so the evidence and its consumer read with one instrument. Reading 2 polls
`StatByID` over a short post-exit settle and returns on the first hit, so a slow flush is a bounded
wait rather than a race that would misreport FALSIFIED as HOLDS.

### The verdict — a pure classifier

```go
// classifyTurnlessTranscript maps the two readings and the termination mode to
// the one sentence AC 4 requires a reader not to have to infer.
func classifyTurnlessTranscript(aliveFound, afterExitFound bool, ended terminationMode) (verdict, sentence string)
```

| alive reading | after-exit reading | termination | verdict |
|---|---|---|---|
| found | any | any | **FALSIFIED** |
| absent | found | any | **FALSIFIED** |
| absent | absent | `sigterm` | **HOLDS** |
| absent | absent | `sigkill` | **INCONCLUSIVE** |
| absent | absent | `did-not-exit` | **INCONCLUSIVE** |

The two INCONCLUSIVE rows are AC 3's requirement in code: an absence read after a force-kill is an
artefact of the signal, so it must never be recorded as the fact holding. The alive reading itself is
always readable — no signal confound applies to it — so the record carries a per-reading line as well
as the overall verdict:

- alive reading: `present while alive` / `absent while alive`
- after-exit reading: `present after exit` / `absent after a graceful (SIGTERM) exit` /
  `INCONCLUSIVE — the child was force-killed, so a flush-at-exit would not have run`

**A file that DOES appear is a falsification, recorded and passed — not a test failure.** The test
fails only for a broken instrument (§ Error handling).

### The record

One struct, one `json.MarshalIndent`, one `t.Logf`, plus one plain-language verdict line so the
verdict survives a truncated log. Fields, grouped:

- **provenance** — `claude_version_raw`, `claude_version_token`, `child_cwd`
- **directories** — `transcript_dir_empirical`, `transcript_dir_recomputed`, `transcript_dir_match`
- **control arm** — `session_id`, `argv`, `prompt`, `exit_code`, `wait_error`, `transcript_path`,
  `transcript_size`, `stdout`, `stderr`
- **turnless arm** — `session_id`, `argv`, `settle_ms`, `alive_before_reading`, `alive_after_reading`,
  `reading_alive`, `termination_mode`, `term_to_exit_ms`, `exit_code`, `wait_error`,
  `reading_after_exit`, `stdout`, `stderr`
- **verdict** — `verdict`, `verdict_sentence`, `reading_alive_verdict`, `reading_after_exit_verdict`,
  `reading_consumed_by_1630`

`reading_consumed_by_1630` is the constant `"after-exit"`: #1630's probe runs at respawn, i.e. after
the previous child is gone, so the after-exit reading is the one it consumes. AC 4 requires the doc to
say so; putting it in the record means the doc cannot get it wrong.

### Captured streams have exactly one ingress, and it redacts

```go
// recordStream is the ONLY way a captured child stream becomes a string this
// test emits — record field, t.Logf, or t.Fatalf message alike. It redacts,
// then truncates, in that order.
func recordStream(buf *bytes.Buffer) string

// redactCredentials replaces each ANTHROPIC_API_KEY / CLAUDE_CODE_OAUTH_TOKEN
// value with <redacted:NAME>. An EMPTY value is skipped: strings.ReplaceAll with
// an empty old string inserts the replacement between every rune, which would
// corrupt the record rather than protect it. WithWorktreeAuthenticated leaves
// the absent variable unset rather than set-empty, so the empty case is real.
func redactCredentials(s string) string
```

Order matters: redact first, so a truncation boundary cannot slice a token into a form the redactor no
longer matches.

**Single ingress is a hard requirement, not a style preference.** Four capture sites (two arms × two
streams) plus the AC 2 Fatal message are five chances to write `buf.String()` directly, and the Fatal
message is the *highest*-risk one of the five — an argv rejection or auth failure is exactly the
stderr most likely to echo a credential. Route every one of them through `recordStream`; a bare
`buf.String()` anywhere in this file is a review finding.

### Deliberate non-goals

Each of these was considered and is excluded; adding one back is scope the ticket does not carry.

- **No testdata fixtures.** #1595 wrote them because it diffed ~1000 stdout events per arm. Here the
  record is a handful of scalars and the doc is the record.
- **No stdout scanner goroutine, no event parsing.** Neither arm reacts to a stream event. The control
  arm's success signal is `<A>.jsonl` appearing; arm B's is a filesystem stat. Capture both streams
  into `bytes.Buffer`s and be done — but **bound the capture at ingest**, not only at record time.
  `truncateString` runs after the buffer already holds everything, so a looping child would balloon
  memory before anything trimmed it; assign a small `io.Writer` wrapper that stops appending past a
  fixed byte cap (~1 MiB, the cap the precedent probe's reader uses, for the same reason). The
  realistic volume here is one haiku turn under `--max-turns`, so this is a bound, not a burden.
- **No `init`-line liveness check**, on either arm. `init` is emitted per turn, not at spawn — measured
  by #1595 and recorded in its doc — so a turnless arm gated on one can never produce a verdict. The
  precedent probe drives an otherwise-unnecessary second turn for exactly this reason.
- **No assertion tying the test argv to `buildArgs`.** It is unexported and in another package;
  exporting it is production surface this ticket has no mandate to add. See § Open questions.

---

## Concurrency model

Two goroutines total, both scoped to one arm.

- **Per arm, one waiter.** A goroutine runs `cmd.Wait()`, stores its error, and closes an `exited`
  channel. That channel is the single liveness oracle — `select` on it with a zero-length default for
  a point-in-time "is it alive", and with a timer for the termination waits. The wait error is written
  before the close and read only after it, so no mutex is needed and `-race` is clean.
- **The waiter must never touch `*testing.T`.** No `t.Logf`, no `t.Fatalf`, no `t.Helper` inside it.
  On the `did-not-exit` path the goroutine outlives the test function, and a `*testing.T` call after
  the test completes panics with "Log in goroutine after test has completed" — which would destroy the
  very record this ticket exists to produce, on precisely the outcome that is hardest to reproduce.
  The waiter stores and closes; the test function does all reporting.
- **No shared state between arms.** Arm A's waiter has returned before arm B starts.
- **Cleanup.** `t.Cleanup` on **both** arms best-effort `Process.Kill()`s, so a `did-not-exit` outcome
  leaves no orphan behind when the test returns. Arm B's `context.WithTimeout` + `defer cancel()` is
  the outer leak guard.
- **Signal a pid, never a process group.** `cmd.Process.Signal(syscall.SIGTERM)` and
  `cmd.Process.Kill()` target the single child this test started. Do not reach for a negative-pid
  group kill while "mirroring production": under some `go test` invocations the test binary shares the
  child's process group, so a group signal can take out the runner and the record with it.
- **`cmd.Env` stays nil on both arms.** The child must inherit the process environment, because
  `WithWorktree`'s `t.Setenv("HOME", …)` and `WithWorktreeAuthenticated`'s credential re-pin both live
  there. Assigning `cmd.Env` drops the pinned `$HOME` (the child writes into the operator's real
  projects tree, and the empirical scan then finds nothing) *and* drops the credential. The precedent
  probe leaves it nil for the same reason.

## Error handling

The instrument/finding split follows `TestRealClaude_PermissionProtocol_Spike` and the #1595 probe: a
finding is recorded and the test passes; only an instrument that measured nothing fails.

**`t.Fatalf` — the instrument is broken, no verdict is readable:**

| Condition | Why it is fatal |
|---|---|
| `claude` not on `PATH` | `t.Skipf`, actually — no claude, nothing to measure |
| No credentials | `WithWorktreeAuthenticated` skips cleanly; not this test's concern |
| Control child fails to start, or its argv is rejected | The positive control is what makes an absence readable |
| `<A>.jsonl` never appears | AC 1: no absence is readable without the control. `streamNewSessionTranscriptDir` already Fatals with a full tree listing |
| Arm B fails to start | Same |
| **Arm B has exited before reading 1** | AC 2: a turnless child that died contributes no absence. The message must carry its **exit status and stderr** — stderr via `recordStream`, never `buf.String()` — so an argv rejection is distinguishable from a crash |

**Recorded, never fatal:**

| Condition | Recorded as |
|---|---|
| `<B>.jsonl` present at either reading | `FALSIFIED` — the assumption is wrong, which is a result |
| Arm B did not exit within grace + hard bound | `termination_mode: "did-not-exit"` ⇒ after-exit reading INCONCLUSIVE |
| Arm B needed `SIGKILL` | `termination_mode: "sigkill"` ⇒ after-exit reading INCONCLUSIVE |
| Empirical and recomputed directories diverge | `transcript_dir_match: false` plus both paths — the finding for the production-path follow-up |
| `StatByID` returns a non-`ErrNotExist` error | Stored in `stat_error`; the reading's `Found` stays false |
| Control arm exits non-zero after its turn | `exit_code` + `wait_error` in the record; the transcript's appearance is the control's real signal |

`StatByID`'s error on an absent file is the **expected** answer, not a failure — store the text, do not
wrap it, do not branch on it beyond `Result.Found()`.

## Testing strategy

- **The live test is the measurement.** It passes on HOLDS, on FALSIFIED, and on INCONCLUSIVE. Judge a
  run by the `=== RUN` count and by reading the record out of the log, never by the exit code: a suite
  that skips everything exits 0 and so does one whose package failed to build.
- **The classifier table test** covers all five rows above plus the `alive found` short-circuit, with
  no subprocess. It is what stops the SIGKILL rule from degrading into a comment.
- **Non-vacuity of the whole run** rests on three things, in order: the control transcript appearing
  (else Fatal), arm B being alive at reading 1 (else Fatal), and both arms sharing one argv builder.
  Any run missing one of those records no verdict.
- **Gates (AC 5):** `make check` must pass — it never compiles this package, so this is a no-regression
  check. Then run `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` **manually** and report it
  clean. Locally, `go test -tags e2e_realclaude -race -v -run TestRealClaude_TurnlessSessionIDTranscript
  ./internal/e2e/realclaude/` — roughly one cheap haiku turn plus one idle child.

## What the doc must contain (documentation phase)

`docs/knowledge/features/session-transcript-and-resume-probe.md`, shaped like
`set-permission-mode-inband-probe.md`, written from the record block in the PR body:

1. **TL;DR** — the verdict, in the record's own `verdict_sentence`.
2. **claude version measured** — raw string, date, platform, and the test name.
3. **Argv per arm** — both slices verbatim, plus the note that one builder produced both.
4. **The two readings** — a small table: reading, when taken, found/absent, termination mode.
5. **The directory comparison** — `child_cwd`, empirical path, recomputed path, match or divergence,
   and if divergent, that `ResolveWorkdir`'s `canonicalCase` is the asymmetry against
   `DefaultClaudeSessionsDir`'s bare `EvalSymlinks`.
6. **Verbatim output** — the record block.
7. **One explicit verdict sentence** stating the fact **HOLDS**, is **FALSIFIED**, or is
   **INCONCLUSIVE**, and stating that **#1630's probe consumes the after-exit reading**. A reader must
   not have to infer either from a pasted transcript.
8. **How to reproduce** — the `go test -tags e2e_realclaude …` line, the cost, and the reminder that
   `make check` never compiles this package.

No credential value from the run environment appears anywhere in it. The redaction happens upstream in
the test, so transcribing the block verbatim is safe — but check the block before pasting.

---

## Security review

**Verdict:** PASS (first pass FAILed on three MUST FIX findings; all three are fixed inline above and
the checklist was re-walked from the top against the revised spec).

**Findings:**

- **[Trust boundaries] MUST FIX — fixed.** One untrusted→trusted crossing exists: the child's stdout
  and stderr (model output, plus any text claude emits on failure) flow into the record, then a
  `t.Logf`, then a PR body, then a committed doc. The first draft named `redactCredentials` but left
  the boundary *scattered* across four capture sites plus the AC 2 `t.Fatalf` message — and that Fatal
  is the highest-risk site of the five, because it fires precisely on an argv rejection or auth
  failure. Fixed by collapsing the boundary to one function, `recordStream`, declared as the only way a
  captured stream becomes an emitted string; a bare `buf.String()` in this file is now a review
  finding. The other boundary, id → filesystem path, is closed inside `StatByID`, which runs
  `ValidStem` before any `filepath.Join`; both ids here are compile-time constants regardless.
- **[Tokens, secrets, credentials] MUST FIX — fixed.** `redactCredentials` as first drafted would have
  called `strings.ReplaceAll` with whichever of `ANTHROPIC_API_KEY` / `CLAUDE_CODE_OAUTH_TOKEN` is
  unset. `strings.ReplaceAll(s, "", x)` inserts `x` between every rune, so the absent-credential case —
  which is the common case, since `WithWorktreeAuthenticated` needs only one of the two and leaves the
  other unset rather than set-empty — would have corrupted the record instead of protecting it. Fixed
  by an explicit skip on the empty value, stated in the helper's contract. No token is generated,
  stored, rotated or revoked by this design; the credentials are inherited from the operator's
  environment and their lifecycle is out of scope. `argv` carries no credential, so recording it
  verbatim is safe — stderr does not have that property, which is what the redaction exists for.
- **[Concurrency] MUST FIX — fixed.** The per-arm waiter goroutine outlives the test function on the
  `did-not-exit` path. Any `*testing.T` call from it after the test completes panics with "Log in
  goroutine after test has completed", destroying the record on exactly the outcome that is hardest to
  reproduce. Fixed by an explicit rule in § Concurrency model: the waiter stores and closes, and never
  touches `*testing.T`. Beyond that: no locks are taken, so there is no lock-ordering question; the
  `waitErr`-before-`close(exited)` ordering gives the happens-before the reader needs without a mutex;
  and every goroutine's exit is caused by `cmd.Wait()` returning, which the SIGTERM → SIGKILL → outer-
  context chain guarantees for a killable process.
- **[Subprocess / external command execution] SHOULD FIX — addressed.** No user-controlled value
  reaches `exec.Command`: argv is compile-time constants plus two constant UUID stems, and `sh -c` is
  never used. Two traps a developer "mirroring production" could walk into are now stated explicitly in
  § Concurrency model — signal a single pid rather than a negative-pid process group (the test binary
  can share the child's group under some `go test` invocations), and leave `cmd.Env` nil (assigning it
  drops both the `t.Setenv`-pinned `$HOME` and the credential). `claudeBin` comes from
  `exec.LookPath("claude")` on the operator's `$PATH`, unchanged from every other probe in this
  package — not a new surface, and not one this ticket can narrow.
- **[Subprocess — descendant escape] SHOULD FIX — accepted with a stated deviation.** `spawnAndWait`
  reaps descendant process groups because claude detaches each Bash command two levels down and does
  not reap it on a graceful SIGTERM (#565). This probe deliberately does **not** reap: arm B runs no
  turn and so spawns no tool descendants, and arm A drives one no-tool prompt. The residual risk is an
  orphan if claude spawns a helper of its own and the arm is force-killed; the `t.Cleanup` kill on both
  arms bounds it to the direct child. Do not add speculative reaping — that is production behaviour
  this evidence ticket has no mandate to reimplement.
- **[File operations] No findings.** No file is written by the test — removing the fixture writer
  (§ Deliberate non-goals) removed the entire partial-write surface, so temp-file-plus-rename does not
  apply. The one directory created is `0o700`, stated in § Design. Symlinks are followed deliberately
  and that is the measurement (`ResolveWorkdir`, `DefaultClaudeSessionsDir`), inside a private tree.
  The check-then-use gap between `streamNewSessionTranscriptDir` and the later `StatByID` calls is not
  attacker-reachable: both resolve under a `t.TempDir()`-rooted `$HOME` created `0o700` with a random
  suffix, so there is no predictable path for a local user to pre-plant.
- **[Error messages, logs, telemetry] No findings beyond the trust-boundary fix.** MUST-NOT-log is
  exactly one class — credential values — and `recordStream` is the single choke point. MUST-log is the
  record's field list: version, both argvs, both readings, termination mode, both directory forms, and
  the verdict. The record is deliberately verbose because it *is* the deliverable; no telemetry is
  emitted and no user-identifiable data is aggregated.
- **[Filesystem reach / hermeticity] No findings.** Every read is under `$HOME`, pinned by
  `WithWorktree` to a `t.TempDir()`, so the empirical scan cannot see the operator's real projects
  tree and `projectsTree` lists paths only, never contents. Worth flagging for the future rather than
  as a finding: `WithWorktreeAuthenticated` copies the operator's real `~/.claude.json` into that temp
  home at `0o600`. Nothing here reads it — but any later diagnostic that dumps the temp home's
  *contents* rather than its paths would leak it.
- **[Cryptographic primitives] Not applicable, by design rather than omission.** No key, nonce,
  signature or comparison-against-a-secret exists in this design. The two session ids are fixed
  constants on purpose: they are filenames inside a private temp tree, and the package's convention is
  deterministic reserved stems, so `crypto/rand` would be actively wrong here.
- **[Network & I/O] SHOULD FIX — addressed.** No socket, no listener, no TLS. The one I/O-cap question
  that does apply is the unbounded `bytes.Buffer` behind each child stream: `truncateString` runs at
  record time, after the buffer already holds everything. Now bounded at ingest by a fixed byte cap
  (§ Deliberate non-goals). Every wait in the design is explicitly bounded — settle, grace, hard wait,
  and the outer per-arm context — so there is no unbounded blocking read.
- **[Threat model alignment] OUT OF SCOPE, owner named.** No relay surface, so
  `docs/protocol-mobile.md` § Security model does not apply. The CLI-relevant threat that does apply —
  an evidence doc committed with an operator credential in it — is what AC 4 forbids and what the
  redaction closes. The downstream security question, whether resuming by a transcript-existence probe
  can adopt another claude's session (the confused-deputy concern #839 closed and ADR 032 preserved),
  is owned by ADR 032 and implemented by **#1630**; this ticket only supplies that ADR's missing
  premise and changes no production behaviour.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-20

---

## Open questions

1. **Nothing pins the test's argv to `buildArgs`'s real output.** `buildArgs` is unexported and in
   another package, so the two shapes agree by inspection, not by construction. Resolution deferred:
   the fix is either exporting a shape or a golden argv assertion in `internal/streamsup`, and both are
   production surface this ticket has no mandate to add. If the shape drifts, the control arm still
   proves *this* argv works, but the measurement's transfer to streamsup weakens. Worth a follow-up
   ticket if #1630 or its successor needs the guarantee.
2. **How long is "settled" for arm B?** The settle window before reading 1 has to be long enough that
   absence means absence and not impatience, and there is no measurement to size it from — this ticket
   *is* the first measurement. Start generous (a window comparable to the precedent probe's
   control-response budget), record it as `settle_ms`, and let the recorded value be revisited if a
   later run disagrees. A too-short window shows up as a FALSIFIED-that-should-have-been-HOLDS, never
   the reverse, so the error direction is safe.
3. **Whether a `SIGKILL` outcome warrants a rerun.** If the run records `sigkill`, the after-exit
   reading is INCONCLUSIVE and #1630's premise is unmeasured. The doc should say so plainly; whether to
   rerun is an operator call, not something the test should loop on.

---

## Scope check (re-applied to this spec)

| Limit | Boundary | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **0** |
| Total written work | ≤ 400 lines | **~396** (one test file; split below) |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches in a state machine | ≤ 10 | **2 skip + 4 fatal + 6 recorded**, plus the classifier's 5 rows — none of it a state machine |

The line budget is a **spec requirement, not an estimate**: `session_transcript_probe_test.go` should
land at or under ~400 lines including comments. It is reachable because the design reuses
`streamNewSessionTranscriptDir`, `setModeTurnLine`, `captureClaudeVersion`, `truncateString`,
`WithWorktreeAuthenticated` and `transcript.StatByID` rather than reimplementing any of them, and
because § Deliberate non-goals removes the three surfaces that make the big files in this package big
(fixture writers, event recorders, multi-arm correlation). The anchor is
`permission_protocol_spike_test.go`: a complete live-claude evidence spike, fixture writer included, in
276 lines. Split, summing to 396 — header ~20, imports ~15, consts ~15, structs ~22,
`transcriptProbeArgs` ~12, launch helper plus the capped stream writer ~48, control arm ~45, turnless
read ~30, termination ~30, `recordStream` + `redactCredentials` ~22, classifier ~30, record emission
~12, the live test ~70, the classifier table test ~25.

If the file is heading past ~450 lines during implementation, the surface has grown beyond this design
— stop and cut it back to the non-goals list rather than continuing.
