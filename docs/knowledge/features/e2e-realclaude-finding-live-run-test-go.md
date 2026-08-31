# finding_live_run_test.go
- `finding_live_run_test.go` (#1340, parameterised #1349) — **the live
  staging driver, not a probe of pyry itself**;
  `finLiveRunStage(t, envDelta) *finLiveRunHandle` spawns pyry on the
  runner path its caller's `envDelta` selects, holds the rendezvous FIFO, drives the turn to
  the instant a during-turn process pin is meaningful, takes that pin, and
  hands it plus the rig's own facts to #1343's `finLiveAssembleStaging`,
  returning a handle carrying the run's live facts and the staging tier's
  `finOutcomeResult` **as the gate returned it**. Nothing #1338/#1342/#1343
  already shipped is re-derived: the pin reduction and its expected row
  count, the staged command/prompt/FIFO-name/env-delta declarations, and the
  eight-field assembly all cross unchanged. `finLiveRunHandle` is returned as
  a **pointer** — the pyry-exit kill cleanup is registered before `PyryPID`
  exists, so its closure has to read a field written later — and carries
  **no JSON tags**, inheriting the "input only, never published" posture of
  the types it wraps (`Pin.Rows`/`Pin.ClaudeCommand` are verbatim argv off
  the ambient process table). The kill cleanup is registered **before**
  `holdProbeFIFO` so LIFO releases the FIFO first and the kill is
  defence-in-depth rather than the thing that produces the exit — inverting
  that order yields a run that looks identical (green, handle populated)
  while the rig itself produced the exit; copied verbatim, `PyryPID <= 0`
  guard and `// LOAD-BEARING` comment included, from the reach precedent
  (`background_reach_probe_test.go:355-374`), not the comment-less trigger
  copy. **The driver takes its own `tool_use`/`tool_result` wait before
  pinning**, even though the assembly waits internally too — the assembly's
  wait fires strictly after the pin (it takes pin counts as inputs), so
  skipping the driver's own wait pins before the held `cat` exists and fires
  the gate's count arm on a correctly staged run, one live claude turn spent
  finding out. The pin itself is one `ps -axww` scan carrying both needles
  (the FIFO path and `tdnClaudeNeedle`) with two exclusions, handed to
  `finLivePinReduce` unchanged, forwarding `finLivePinWantRows` as the want
  (never `scan.MatchCount`, never `len(Pin.PGIDs)`). No budget-fired run is
  staged — `--max-turns=6` gives the turn room to complete, since a
  budget-fired run's exit code can't discriminate outcomes and its
  `Terminate` hook reaps before the trailer is written. No `ps -E`/`-Eww`
  anywhere; matched rows and claude's argv cross the handle only as the
  already-capped `reachProc.Command`; the file formats no `Detail` and writes
  no artifact. **Ships no test of its own** — its only exercise is
  compilation and `go test`'s vet subset under `make e2e-realclaude`;
  `finLiveRunStage`'s one caller was #1337's live entry point
  (`finding_exit_path_probe_test.go:216`) until **#1353** added a second,
  `finStreamExitRunProbe` — see below. See
  [`codebase/1340.md`](../codebase/1340.md) for the original implementation
  and the code-review SHOULD FIX on a counted `t.Fatalf` claim the shipped
  file falsified.

  **#1349 parameterised the delta and re-derived the doc comment site by
  site.** `envDelta` reaches `spawnProbePyry` verbatim — no default, no
  nil-check, no package-level fallback, since a silent default would hide
  exactly the ambient-environment failure `reachRunnerPathFromEnv`'s own doc
  exists to make visible. The doc comment's argument was ptyrunner-only in
  five places and one was wrong on its own path: the `cmd.Wait` goroutine's
  "claude runs on a PTY" reason covered the one fd claude never shares
  (`cmd.Stderr` is `os.Stderr` on both runner paths, and `creack/pty` fills
  stdio only when nil), not the fd that can actually hold the wait open.
  The replacement separates the rig's own `Wait` (evidence transfers
  unchanged, from the 2026-08-06 live run) from a new stream-path-only
  hazard — pyry's `Wait` on claude's stdout pipe, bounded by
  `cmd.WaitDelay=killGrace` (5s) — whose symptom, if it fires, is a
  distorted `ExitStatus` rather than a hang, for #1353 to meet in the
  comment before it meets it in an exit reading. See
  [`codebase/1349.md`](../codebase/1349.md) for the full site-by-site
  classification, the corrected fd argument, and the stale-citation sweep
  code review caught across all three touched files.

- `dropped_line_capture_test.go` (#1260) — **evidence probe,
  security-sensitive**; opt-in behind `PYRY_PROBE_DROPPED_LINE_CAPTURE=1`.
  Answers what four downstream tickets (#1261–#1264) all needed and nobody
  had ever read: the verbatim payload of every stream-json line
  `internal/streamsup/parser.go` drops on the **interactive** surface (not
  headless — #1218 already proved the two surfaces don't share subtype
  rates). Drives `streamsup.Runner` **in process** and installs
  `dropcapRecorder` in the exact `Config.Stdout` slot production gives
  `streamsup.NewParser` (`cmd/pyry/streamsup_runner.go:117`), so "upstream of
  the parser" is structural; `spawn_shape` is observed from production's own
  `buildArgs` output through a discard-`slog.Handler` on the runner's log
  record rather than transcribed, so the recorded argv cannot drift from the
  shape it claims to measure. Reuses #1223's FIFO-hold lever and #1240's
  interactive staging idioms unedited (`holdProbeFIFO`, `bgIdlePrompt`), but
  is deliberately **not** built on #1240's `bgIdleRecordTurn` — that recorder
  reads decrypted phone frames downstream of the parser, so every line this
  ticket needs would be structurally absent from it. Classification asks the
  shipped parser (`parseOne`) rather than mirroring `ignoredLineTypes`, so a
  future parser change can't silently desync the census from what actually
  ships. Three outcomes (`fired`/`did-not-fire`/`instrument-broken`), only
  the first licensing an absence claim, gated by a pre- and post-rendezvous
  `fifoLiveRead` pair. Redaction is two mechanisms with different fabric: a
  declared substitution table applied to every string that enters the
  record (not just payloads — `fifoLiveOutcome.Path`/`.Detail` and every
  `t.Logf` leak a path with no payload involved), and a fail-closed deny-scan
  over the whole marshalled record as the deterministic net behind it,
  `t.Fatalf`-ing on a hit and writing no file. **Result, committed as
  `testdata/dropped_lines_v2.1.220.json`** (`outcome: fired`,
  `absence_claim_valid: true`): 49 lines captured, 39 dropped —
  `system/init` ×1, `system/thinking_tokens` ×33, `system/task_started` ×1,
  `system/task_updated` ×1, `system/background_tasks_changed` ×1,
  `rate_limit_event` ×1, and the suppressed `user`/`text` harness-nudge block
  ×1 (matched `harnessNoOutputNudge` byte-exactly — the second confirmed
  observation #1247's doc comment asks for before promoting that constant to
  a set, deferred as a follow-up). `task_notification` is named explicitly
  as absent, not silently zero. Code review FAILed once on 3 SHOULD FIX (all
  in the deny-scan's base64 arm and the redaction kept-list prose; no MUST
  FIX, nothing in the committed fixture unsafe), fixed before merge with no
  re-capture needed. Zero production files, zero modified files. See
  [`codebase/1260.md`](../codebase/1260.md) for the full implementation, the
  capture's field-level contents, and the code-review lessons (a deny class
  that carries its own needle; a path that leaks in a slug spelling no
  substitution rule enumerated).

- `finding_exit_path_probe_test.go` (#1337) — **the live entry point that
  reads, classifies and publishes**, on top of #1340's driver;
  `TestRealClaude_ExitPathWhileCommandRuns` stages a turn via
  `finLiveRunStage`, waits for pyry's own exit **in the subtest body, never
  in a cleanup** (`finExitPyryExitDeadline`, 120s, deliberately not
  `probePyryExitGrace` — a different wait on a different clock) with the
  rendezvous FIFO still held, and only then gathers, classifies and writes
  the artifact. `finLiveRunHandle` gains one field, `ExitStatus int` — the
  driver's `cmd.Wait` goroutine used to discard it and no consumer could
  recover a reaped child's status — written **before** `close(pyryExited)`
  and initialised to `pinExitStatusUnknown`; that close is the sole
  happens-before edge, so the field is read **inside** the channel receive
  arm and nowhere else, deliberately with no
  `finExitObservedCode(exited, status)`-style helper, because any shape that
  evaluates the field outside the arm races the goroutine. Staging is
  decided **before** the classifier is consulted:
  `finExitClassify(staging, readings) (string, trailRunOutcome, bool)`
  returns the staging value and the **zero** `trailRunOutcome` unconsulted
  on any non-pass-through staging value — an unstaged run's post-trailer
  argv scan still parses a healthy process table and matches nothing, which
  would otherwise reach the classifier's fall-through answer about a run in
  which no command ever existed. The primary evidence is pyry's own reap
  log (`ReapDescendantGroups` logs only the groups it actually killed, which
  on this path is strictly after the trailer write); the claude-still-alive
  read and the per-pid post-trailer reads are corroboration only, recorded
  as **known blind** (the reap runs between the trailer and claude's
  SIGTERM) and **known late** respectively, and neither overrides an
  attribution. The published artifact carries only the trailer sub-record's
  outcome string, so the classifier's full outcome and the staging result
  reach the operator through `t.Logf` alone — `json.Marshal` or field-by-
  field, never a `%v` on a struct or slice, the mechanism that would
  otherwise print verbatim argv from an ordinary-looking debug line. One
  offline table test, `TestFinExitClassifyConsultsTheClassifierOnlyOnThePassThrough`
  (8 rows, ranged from `finOutcomeValues()`), RED-proved by mutation via
  `go test -overlay` rather than worktree edits. In the dispatch environment
  the entry point skips (exit 0) for want of a Claude login, which is the
  expected outcome and not a finding about pyry. **The operator live run has
  since happened, 2026-08-06 on claude 2.1.220, and it produced a finding:
  pyry declared the turn finished while the command it launched was still
  running.** Classified `run-running-at-trailer` on `admit-proof` — pyry's
  own reap log named the held group under terminal reason `completed`, and
  `emitter.Close()` wrote the trailer before the reap defer reached that
  group, so the group was alive when the trailer was written. The trailer
  read `subtype=success is_error=false terminal_reason=completed
  stop_reason=end_turn`, and the exit code was 0 on a run that completed —
  which is the same 0 a budget-terminated run gives, so `terminal_reason` is
  the discriminator, not the exit status. The predicted systematic lateness
  showed up exactly as designed: 2 during-turn matched rows against 0
  post-trailer matches over 893 rows scanned. See
  [`codebase/1337.md`](../codebase/1337.md) for the full implementation, the
  code-review SHOULD FIX on four citations this PR staled in the same file
  it edited, and the finding.
