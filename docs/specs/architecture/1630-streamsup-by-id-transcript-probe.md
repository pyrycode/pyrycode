# #1630 — streamsup decides a spawn's id flag from a by-id transcript probe, inert until a sessions dir is supplied

## Files to read first

Code:

- `internal/streamsup/runner.go` → `buildArgs` — the current signature and the `firstRun` truth table you are keeping the *shape* of.
- `internal/streamsup/runner.go` → `beginSpawn` — the one production call site of `buildArgs`, and the `restartMu` section this ticket restructures. Read its whole doc comment: it states the #1481 single-acquisition charter you must not break.
- `internal/streamsup/runner.go` → `setArgsLocked` — two facts this design leans on: (a) it clones on the way **in**, so the backing array of `r.args` is never mutated after publication; (b) its last paragraph states the in-repo invariant *"restartMu is a leaf: nothing under it may take another lock or do synchronous I/O"* — that sentence is why the probe goes outside the section.
- `internal/streamsup/runner.go` → `Run` — the `firstRun` latch, the `forceFirst` re-arm, and the `if started { firstRun = false }` gate. All three stay **byte-identical**; this ticket adds no edit here.
- `internal/streamsup/runner.go` → `RestartFresh` — how `sessionID` + `rotatePending` are published; AC 4's fixture drives this.
- `internal/streamsup/runner.go` → `Config` — where the new field goes and the doc-comment register the neighbouring fields are written in.
- `internal/transcript/transcript.go` → `StatByID`, `ValidStem`, `Result.Found` — the probe primitive. Note `StatByID` validates the stem *before* any `filepath.Join`, and that a hit is exactly `err == nil` (see § Error handling).
- `internal/sessions/pool.go` → `Config.ClaudeSessionsDir` — the field whose name, semantics and doc-comment wording the new `streamsup.Config` field mirrors.
- `cmd/pyry/streamsup_runner.go` → `mapStreamsupConfig`, and `internal/sessions/runnerstate.go` → `RunnerConfig` — read together they are the *structural* proof of inertness: the mapper can only set what `RunnerConfig` carries, and `RunnerConfig` has no sessions-directory field. Do not add one (that is #1631).
- `internal/streamsup/runner_test.go` → `helperRunCfg`, `testSessionID`, `rotatedSessionID`, `spawnArgsRecorder`, `idFlagValue`, `idFlagCount` — the fixtures and argv-observing helpers every new test reuses. Both id constants are canonical lowercase-hex UUID stems, so both work as `StatByID` fixture stems unchanged.
- `internal/streamsup/runner_test.go` → `TestRunner_RestartFresh_RotatesThenResumesNewID` — the direct template for AC 4's test.

Docs:

- `docs/knowledge/features/session-transcript-and-resume-probe.md` § "Both `<C>` readings" — the **NO STUB** measurement. It is the fact that makes a by-id probe *converge* rather than latch, and the reason no anti-latch bookkeeping is needed.
- `docs/knowledge/decisions/032-bootstrap-resume-per-spawn-existence-probe.md` § Decision + § "Per-spawn over a one-shot decision" — the rule being carried into this package, and why a construction-time answer is insufficient.
- `docs/knowledge/features/streamsup-package.md` § "`buildArgs` — the id-flag inversion…" and § "`firstRun` gate" — the current documented behaviour. **Read-only**: keeping it accurate is the documentation phase's job, not a deliverable here.

## Context

`buildArgs` picks one spawn's id flag from a `firstRun` bool that the `Run` loop flips as soon as `cmd.Start` succeeded. "claude launched" and "a transcript exists on disk" are different facts, and #1655/#1656 measured the gap end to end against claude 2.1.220: a child that launches and runs no turn writes no `<id>.jsonl`, and `--resume` against an absent `<id>.jsonl` exits 1. Because the latch only moves true→false, that session's every respawn re-emits `--resume <id>` on a widening backoff — a crash-loop with no exit. The same probe also removes ADR 032's second defect in this package: on a daemon restart the transcript survives, so the *first* spawn emits `--session-id` against a live transcript and claude refuses it (~220 ms wasted crash).

This ticket introduces the decision inside the runner and leaves it inert. With no sessions directory supplied — which is every production path today, structurally, because `RunnerConfig` has no such field for `mapStreamsupConfig` to map — the emitted argv is what it is now. #1631 supplies the directory and makes the fix user-visible.

No ADR is warranted: ADR 032 already decided this rule and this ticket applies it to a second package. The documentation phase should extend ADR 032's § Related and § Consequences rather than write a new record.

## Design

One production file: `internal/streamsup/runner.go`. Four changes.

### 1. `Config.ClaudeSessionsDir string`

A new optional field, placed after `SessionID` (the field it qualifies). Semantics and doc-comment register mirror `sessions.Config.ClaudeSessionsDir` deliberately, so the two read as one concept:

> the directory containing claude's `<uuid>.jsonl` files for this `WorkDir`. Empty disables the by-id transcript probe, and every spawn's id flag falls back to the `firstRun` latch — byte-identical to pre-#1630 argv. Never derived from `WorkDir` inside this package (see § Hermeticity).

`New` gains **no** validation for it. The zero value is meaningful, and every bad non-zero value already degrades to the create form through the probe's own fall-back — a constructor check would add a reject branch with no reachable failure behind it.

### 2. `useCreateForm` — the decision, pure

```go
// useCreateForm reports whether this spawn's id flag should be --session-id
// (create) rather than --resume (reattach). Pure; the only side effect is one
// os.Stat.
func useCreateForm(sessionsDir, id string, latchCreate bool) bool
```

Contract:

- `sessionsDir == ""` → return `latchCreate` verbatim. This is the whole of AC 1: the probe is not consulted, no syscall is made, and the caller's latch decides exactly as today.
- otherwise → return `!transcript.StatByID(sessionsDir, id).Found()`. A confirmed hit is the *only* answer that yields `--resume`; absent file, unreadable/missing directory, and a `ValidStem`-rejected id all yield `--session-id`. The probe therefore has no failure mode that can fail a spawn — there is no error return and no third outcome.

**Two things this function must not do.** Both are one edit away from the code the developer will have open, and both are load-bearing rather than stylistic:

- **Call `transcript.StatByID`; never hand-roll `filepath.Join(sessionsDir, id+".jsonl")` + `os.Stat`.** `StatByID` runs its `ValidStem` gate *before* the join. `streamsup.New` checks only that `SessionID` is non-empty and never that it is a canonical stem, and `RestartFresh` re-checks nothing either, so a non-canonical id can reach this function. A hand-rolled join turns such an id into a traversal — an arbitrary-path existence oracle that flips the spawn's id flag. `StatByID` is the boundary; use it and pass the raw id straight through.
- **Never fall back to a directory scan.** `transcript.Newest` sits beside `StatByID` in the same file and answers a superficially similar question. It is the wrong primitive here: #839 deleted `--continue` and the adopt-by-mtime scan specifically to close a confused-deputy gap where a restart could adopt a *different* claude's newer transcript out of the shared sessions dir. The fall-back for every non-hit is the create form, full stop. AC 3's two `<other-id>` rows exist to make a scan fail the suite.

When the directory is supplied the probe decides **outright**: `latchCreate` is ignored, including on the first spawn and including when a `RestartFresh` rotation set `forceFirst`. That is what AC 2's "including on the **first** spawn" asks for, and it is one branch rather than two.

**This settles the out-of-scope collision the ticket flags, without a defense for it.** If a rotated id ever did collide with an existing transcript, ADR 032's rule wins here and the spawn resumes it. That outcome is unreachable from `sessions.NewID`'s minting, has never been observed, and gets no branch, no flag and no test — it is simply what falling out of the single rule produces. Do not add a `forceFirst` override to restore the other answer.

### 3. `buildArgs` — same shape, honest parameter name

```go
func buildArgs(base []string, create bool, sessionID string) []string
```

Signature *shape* and truth table are unchanged: `create == true` → `--session-id <id>`; `create == false` → `--resume <id>`. Only the parameter name and the doc comment change, because "first run" is no longer what the bool means.

The polarity is deliberately **not** flipped to ADR 032's `resume bool`. Flipping it would rewrite six boolean literals by hand across three green tests (`TestBuildArgs`, `TestBuildArgs_DoesNotMutateBase`, `TestBuildArgs_StableIDAcrossFirstAndResume`) for zero behavioural gain. AC 1 permits that mechanical edit; this design does not need it, so those three tests are **untouched** as well as the four integration tests.

Update the sentence in the doc comment that says the flag is chosen by "first spawn / every respawn" — it now says the caller decides create-vs-resume and names `useCreateForm` as the production decider.

### 4. `beginSpawn` — snapshot inside the section, decide and assemble outside

```go
iterCtx, cancel = context.WithCancel(ctx)      // unchanged, still above the section

r.restartMu.Lock()
forceFirst = r.rotatePending
r.rotatePending = false
base, id := r.args, r.sessionID
r.iterCancel = cancel
r.restartMu.Unlock()

create := useCreateForm(r.cfg.ClaudeSessionsDir, id, firstRun || forceFirst)
args = buildArgs(base, create, id)
```

`defer r.restartMu.Unlock()` becomes an explicit `Unlock`, matching `Restart`, `RestartFresh` and `SetSpawnArgs` — all three already close their section and then do post-section work.

**Why the stat is outside.** `setArgsLocked`'s doc states the invariant directly: *restartMu is a leaf; nothing under it may take another lock or do synchronous I/O.* That is why `Run` logs "spawning claude" below the section. A blocking `os.Stat` is the same class of call-out, and the mutex it would stall is the one `liveSessionID` takes on `WriteUserTurn`'s diagnostic path — a hung `$HOME` mount would stall turn writes, not just the spawn.

**Why this is not a re-split of #1481.** The forbidden outcome is *a live child under a pre-rotation id with no live iteration cancel*. The section still reads the spawn inputs **and** publishes `iterCancel` atomically; only the pure assembly moved out. A racer arriving after the unlock finds the published cancel and tears this spawn down, exactly as before. `buildArgs` was moved *into* the section at #1481 because the then-existing `liveArgs`/`nextSpawnID` accessors each took the non-reentrant mutex — not because argv assembly itself must be inside it. The window between the unlock and `cmd.Start` already exists today (the "spawning claude" log and `spawnAndWait`'s setup live in it); the probe widens it by one `stat` and changes nothing about which side of the invariant a racer lands on.

**Why handing `r.args` out of the section is safe.** `setArgsLocked` is the sole assignment to `r.args` and it clones on the way in, so no installer ever mutates a published backing array. The slice *header* is read under the mutex; a later install replaces the header, leaving this spawn's local pointing at the old, immutable array. No clone is needed on the way out — `buildArgs` copies into a fresh slice anyway.

### 5. The package doc comment

`internal/streamsup`'s package comment currently asserts there is "no `<uuid>.jsonl` path resolution anywhere in this package". That becomes false. Amend that one clause to say the package resolves no transcript *path for tailing or binding*, and takes exactly one by-id existence stat per spawn when `Config.ClaudeSessionsDir` is set. Keep the surrounding "no transcript tailing, no fsnotify" claims — they stay true.

### Imports and dependency direction

`internal/transcript` imports stdlib only and is neither `internal/supervisor` nor an `internal/agentrun` subpackage, so it satisfies the dependency rule stated in the package doc. No cycle: `transcript` imports nothing from `streamsup` or `sessions`.

### Hermeticity

Nothing in the runner derives the directory from `WorkDir`. `sessions.DefaultClaudeSessionsDir` maps a workdir to the real `$HOME/.claude/projects/<encoded>`, so deriving it here would point every unit test — whose `WorkDir` is a `t.TempDir()` — at the developer's actual home. The package must reference neither `DefaultClaudeSessionsDir` nor `os.UserHomeDir`; the directory only ever arrives through `Config`.

## Concurrency model

No new goroutines, no new mutex, no change to shutdown. The only change to the locking picture:

- `restartMu`'s critical section in `beginSpawn` **shrinks** — argv assembly leaves it, one stat never enters it. The leaf-mutex charter ("no call-out, no synchronous I/O") is preserved rather than weakened.
- The section keeps its single acquisition and keeps publishing `iterCancel` before returning, so #1481's invariant is unchanged.
- `r.cfg` is immutable after `New`, so reading `r.cfg.ClaudeSessionsDir` outside any lock is race-free (the same access pattern `spawnAndWait` already uses for `r.cfg.ClaudeBin`).
- The probe reads the `id` snapshotted in the same section that consumed `rotatePending`, so there is no id/decision skew — the probe always targets the id this spawn is about to use.

## Error handling

`useCreateForm` returns a bool and no error, on purpose: every non-hit is a legitimate answer, not a failure.

- `StatByID`'s contract is an exact bijection — a hit is `(Result{path,size}, nil)` and every miss is `(Result{}, err)`. So `!res.Found()` already covers the invalid-stem case, the absent-file case and the unreadable-directory case. Do **not** add an `if err != nil` arm beside it: it would be a return site no fixture can reach on its own, and this repo has been bitten by exactly that shape before. Discard the error with the blank identifier and one comment saying absence is the expected answer.
- A wrong directory reads "absent" and yields `--session-id`, which claude refuses when the transcript actually exists elsewhere (ADR 032). That converts a misconfiguration into a crash-loop — and it is precisely why this ticket stays inert. It is #1631's hazard to close with the empirical-vs-recomputed directory comparison #1655 recorded; do not defend against it here.
- **No new log call.** `Run` already logs the composed argv at Info ("spawning claude"), which shows the decided flag on every spawn. A per-spawn Debug naming dir/id/found would only earn its keep once a wrong directory is reachable, i.e. in #1631. Adding it now is a defense for an unobserved failure on a path that cannot execute.

## Testing strategy

Three additions. All fixtures are `t.TempDir()`; `<uuid>.jsonl` files are written directly with `os.WriteFile` (contents are irrelevant — `StatByID` only stats).

**T1 — argv table over `useCreateForm` + `buildArgs` (AC 2, AC 3).** Pure, no child process. Each row supplies a fixture directory state and a `latchCreate` value, composes the two functions, and compares the full argv slice. Rows:

- empty dir, `latchCreate` true → `--session-id <id>`; empty dir, `latchCreate` false → `--resume <id>` (the inert fall-back, both directions).
- dir supplied, `<id>.jsonl` absent, `latchCreate` **false** → `--session-id <id>`. This is the crash-loop fix: the probe overrides a latch that says resume.
- dir supplied, `<id>.jsonl` present, `latchCreate` **true** → `--resume <id>`. This is ADR 032's wasted-crash fix: the probe overrides a latch that says create.
- dir supplied, only a **newer** `<other-id>.jsonl` present → `--session-id <id>`, and `<other-id>` appears nowhere in the argv (a dir scan would emit `--resume <other-id>`).
- dir supplied, `<id>.jsonl` present **and** a newer `<other-id>.jsonl` alongside it → `--resume <id>`, and `<other-id>` appears nowhere (an mtime scan would name `<other-id>`).
- dir supplied but non-existent → `--session-id <id>`.
- dir supplied, id rejected by `ValidStem` (reuse `buildArgs`'s existing non-UUID test value) → `--session-id <id>`.

Every row additionally asserts the argv carries no `--continue`. That clause is a regression pin, not evidence — `--continue` has zero occurrences in the package today, so it cannot fail against a conforming implementation; the two `<other-id>` rows are what discriminate by-id from a scan. To make the newer-`<other-id>` rows meaningful, give `<other-id>.jsonl` a later mtime than `<id>.jsonl` explicitly (`os.Chtimes`) rather than relying on write order.

**T2 — first spawn resumes an existing transcript (AC 2, the wiring pin).** Hand-build a `Runner` (as `TestRunner_SpawnSetupFailureRetainsSessionID` does — no `New`, no child), set `cfg.ClaudeSessionsDir` to a temp dir containing `<testSessionID>.jsonl`, seed `sessionID: testSessionID`, and call `beginSpawn(ctx, true)` — i.e. with the latch saying *first run*. Assert the returned argv carries `--resume testSessionID` and no `--session-id`, then call the returned cancel. T1 proves the decision; this proves `beginSpawn` actually feeds it to `buildArgs` instead of passing `firstRun` through.

**T3 — decided once per spawn, not once at construction (AC 4).** Copy `TestRunner_RestartFresh_RotatesThenResumesNewID`'s structure: `helperRunCfg` in `"crash"` mode, `spawnArgsRecorder` as the logger, a `sync.Once`-guarded `onSpawn` firing `RestartFresh(rotatedSessionID)`. Set `cfg.ClaudeSessionsDir` to a temp dir holding **only** `<testSessionID>.jsonl`. Wait for two spawns and assert:

- spawn 1 → `--resume testSessionID` (present ⇒ resume, on the first spawn).
- spawn 2 → `--session-id rotatedSessionID` (absent ⇒ create), and `testSessionID` appears nowhere in spawn 2's argv.
- each spawn carries exactly one id flag (`idFlagCount`).

The ordering is what makes it load-bearing: a decision computed once at construction would carry spawn 1's "present ⇒ resume" answer into the rotated id and emit `--resume rotatedSessionID`.

Two notes for whoever writes this. Wait for **two** spawns, not three — the fake child writes no transcript, so spawn 3 is also `--session-id rotatedSessionID`, which is correct and convergent but asserts nothing extra. And keep the fixture asymmetric (pre-rotation id on disk, rotated id absent); a fixture where both are absent passes under a per-spawn probe and a construction-time one alike.

**AC 1 needs no new test.** `TestRunner_ResumeIDStableAcrossRestart`, `TestRunner_SpawnSetupFailureRetainsSessionID`, `TestRunner_RestartFresh_RotatesThenResumesNewID` and `TestRunner_LiveRestart` all leave `ClaudeSessionsDir` zero, so they observe today's argv through a real spawn and are the inertness pin. Do not add a separate "is inert" test, and do not edit those four. The three pure `buildArgs` tests are untouched too under this design (§ Design 3).

**AC 5.** `make check` must pass. Hermeticity is a property of the fixtures (`t.TempDir()` everywhere) plus the absence of any `DefaultClaudeSessionsDir` / `os.UserHomeDir` reference in the package — a grep, not a test.

## Open questions

None blocking. Two things resolved above rather than left open, recorded so a reviewer does not reopen them:

1. **Rotated id colliding with an existing transcript** — settled as "resume" by the single rule, not by a branch. Out of scope per the ticket; no defense, no test.
2. **A diagnostic for a wrong sessions directory** — deferred to #1631, where the failure first becomes reachable.

## Security review

**Verdict:** PASS (first pass FAILed on two MUST FIXes, both now folded into § Design 2 and re-walked.)

**Findings:**

- **[Trust boundaries] MUST FIX — fixed.** The design adds one untrusted→trusted crossing: `r.sessionID` becomes a filesystem path component. `streamsup.New` validates `SessionID` for non-emptiness only, never for canonical shape, and `RestartFresh` re-validates nothing, so a non-canonical id reaches the probe. The boundary is a single symbol — `transcript.StatByID`'s `ValidStem` gate, which runs before any `filepath.Join`. The first draft named that property only in the reading list, which left a hand-rolled `filepath.Join(dir, id+".jsonl")` + `os.Stat` as a plausible implementation and would have turned a hostile id into an arbitrary-path existence oracle that flips the spawn's id flag. § Design 2 now states the prohibition where the developer writes the function.
- **[Threat model alignment] MUST FIX — fixed.** #839's confused-deputy gap (a restart adopting a *different* claude's newer transcript out of the shared sessions dir) is the one threat this package's history is built around. `transcript.Newest` sits beside `StatByID` in the file the developer will have open and is exactly the wrong primitive for the fall-back. § Design 2 now forbids it by name; AC 3's two `<other-id>` rows make a scan fail the suite rather than merely being undocumented.
- **[File operations] No findings.** Path traversal: closed by the boundary above. TOCTOU: the design *is* check-then-use — `os.Stat` here, `open` inside claude — and the gap is benign because neither outcome is an authorisation decision and both converge. A transcript deleted in the gap yields `--resume` against an absent id, which exits 1, and #1656's **NO STUB** measurement means the next probe reads absent and falls back to create. A transcript created in the gap yields `--session-id` against a live one, which claude refuses once, and the next probe reads present and resumes. Neither latches. No file is created or written by the runner, so file mode and atomic-write questions do not arise; fixtures live under `t.TempDir()`. Symlinks: `os.Stat` follows, but pyry never opens the file — only claude does, by its own id lookup — so nothing is read through a planted link.
- **[Subprocess execution] No findings.** The id token reaching `exec.Command` is unchanged in value space: `buildArgs` already emitted `r.sessionID` on every spawn, and the probe changes only which flag precedes it. The id can never come from the directory — it is always the snapshotted `r.sessionID` — which is what makes AC 3's "never names `<other-id>`" structural rather than test-enforced. No `sh -c`; `Env` handling untouched; signal/teardown path untouched.
- **[Network & I/O] No findings.** No network. `os.Stat` reads metadata only and never file contents, so a hostile oversized or never-terminating `<id>.jsonl` cannot exhaust memory here (a content read or a `Newest` dir scan would answer differently). Stat rate is one per spawn, bounded by the existing 500 ms→30 s backoff ladder, so no new DoS surface.
- **[Error messages, logs, telemetry] No findings.** `StatByID`'s error — which embeds the sessions directory path — is discarded and never reaches a log line or a wrapped error, and § Error handling deliberately adds no new log call. The daemon log's disclosure surface is therefore byte-unchanged (`Run`'s existing "spawning claude" record already carries `args` and `workdir`).
- **[Concurrency] No findings.** The change *shrinks* `restartMu`'s critical section rather than widening it, honouring the leaf-mutex charter `setArgsLocked` states ("nothing under it may take another lock or do synchronous I/O"). No check-then-mutate split: the id is snapshotted in the same section that consumes `rotatePending`, so the probe cannot skew against a racing rotation, and `iterCancel` is still published inside that section, so #1481's invariant holds unchanged. `defer Unlock` → explicit `Unlock` matches `Restart`/`RestartFresh`/`SetSpawnArgs` and the section contains only field reads and assignments — no call that can panic. No goroutine is added, so there is nothing new to leak.
- **[Tokens, secrets, credentials] Not applicable.** The design generates, stores, compares and logs no credential. A claude session id is not one — it is already emitted in cleartext argv and in the "spawning claude" log record today.
- **[Cryptographic primitives] Not applicable.** No randomness, no hashing, no key material, no comparison against a secret. The probe is one existence stat and a bool.
- **[Hazard handed onward] OUT OF SCOPE — #1631.** A *wrong* sessions directory makes the probe read absent for a session whose transcript exists elsewhere, emitting `--session-id` against a live transcript, which claude refuses (ADR 032) — turning a misconfiguration into a crash-loop. This ticket's inertness is what keeps that off the production path; #1631 owns closing it with the empirical-vs-recomputed directory comparison #1655 recorded.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-20
