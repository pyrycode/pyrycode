# finding_stage_held_group_test.go
- `finding_stage_held_group_test.go` (#1282) — **fills `finGatherReadings`'
  (#1281) two parameters from a real held command, not hand-passed
  integers.** `finStageHeldGroup` stages `sh -c '"$1" "$2"; exit 0'` over a
  real `cat` held on a real FIFO, in a process group of its own
  (`SysProcAttr{Setpgid: true}` — copying the wrapper subject shape from
  `trail_run_rig_test.go`, not the flip test's bare `exec.Command`, which
  would inherit the test's own group), then pins that group off a real
  `pinScanArgv` (#1280) match set's `.PGID`s. AC1's distinctness guard
  compares the **scanned** pgid against `syscall.Getpgrp()`, never
  `cmd.Process.Pid` — the pid form is vacuous under the dropped-`Setpgid`
  mutation, confirmed green in code review, while the scanned form reddens
  in 0.06s. The teardown adds a third statement (a direct
  `cmd.Process.Kill()`) that the neighbouring rig's two-statement teardown
  doesn't need, because only this file's guard can redden on a path where
  the group kill finds no group to signal — without it, `t.Fatalf`'s
  `runtime.Goexit()` would deadlock the mutation against `holdProbeFIFO`'s
  `t.Cleanup`. Two tests, five arms: the finding and a genuine negative
  (`trailOutcomeMatchedUnattributed`, one step earlier than #1281's
  `trailOutcomeNoRowMatched` because a real command carries the needle),
  plus #1268's two hardcodings trapped at the **`Admit`** layer against a
  same-staging control, each varying exactly one dimension. First `fin*`
  file whose scan matches live rows, so `readings.Liveness` is non-empty
  for the first time — the neighbour's whole-struct-print licence
  (`finding_run_gather_test.go:105-113`) is deliberately not inherited,
  since its proof ran with `Liveness` empty on every row. Purely additive,
  one new file, 617 lines, zero production change; both new tests PASS,
  never SKIP. See [`codebase/1282.md`](../codebase/1282.md) for the full
  implementation and the grade-mutations-per-line lesson.

- `finding_live_pin_test.go` (#1338) — **offline reduction, not a probe**;
  the pure post-filter a later ticket's during-turn `pinScan` (held `cat` on
  a FIFO, pinned mid-turn) is reduced through before it ever reaches the
  staging record — no live scan, no `ps` exec, no `pyry` spawn, no caller.
  `finLivePinReduce(scan pinScan, fifoPath string) finLivePinReading` takes
  membership from `reachMatchedNeedle` over each row's recorded needle list
  (never a re-scan of `.Command`, which the byte cap may have truncated past
  `reachMaxCommandBytes`), returns every FIFO-matched row and its `.PGID`
  raw — unsorted, undeduped, since the consumer `finAttributeFanOut` (#1280)
  dedupes and sorts internally — and reads claude's own argv via
  `tdnClaudeCommand(scan)` over the whole scan, not the FIFO-filtered rows
  (claude's row carries only the claude needle, so filtering first always
  returns `""`). `finLivePinWantRows = 2` names the expected FIFO-row count,
  sourced from #1230's live measurement (the `zsh -c` wrapper plus the
  forked `cat`) and corroborated, not primarily sourced, from #1268's
  rig-staged mutation test; `trail_run_rig_test.go:563` is deliberately not
  cited, since it asserts only `MatchCount > 1`, never `== 2`. Both
  plausible-wrong fills — `scan.MatchCount` (3, since one scan carries both
  the FIFO and claude needles) and the distinct-pgid count of the FIFO rows
  (1, since claude isolates the Bash command into its own group) — are
  pinned as asserted values in `TestFinLivePinCountIsNeitherWrongCandidate`
  and checked pairwise-distinct from the correct count, so a fixture edit
  that collapses two candidates together fails loudly instead of silently
  disarming the trap. The offline trap drives everything over a synthetic
  four-column `ps` table built as **bytes** and turned into a `pinScan`
  through the real `pinMatchArgvExcluding` (a hand-built `pinScan` would skip
  the match-uncapped/store-capped asymmetry the truncation assertion rests
  on); the wrapper row's padding is derived from `reachMaxCommandBytes`
  itself, never a literal 512. Purely additive, one new file, 514 lines,
  zero production change, zero consumer call sites — the driver and record
  tickets that call `finLivePinReduce` for real land later. See
  [`codebase/1338.md`](../codebase/1338.md) for the full implementation, the
  mutation-tested lessons, and why `strings.Contains(s, "")` being `true`
  makes the empty-needle assertion a real second witness for the
  membership-re-scan defect rather than comment-only work.

- `finding_live_staging_test.go` (#1342) — **declarations, not a probe**;
  the run's FIFO name, hold prompt, staged command literal and env delta a
  later live turn stages from, plus one offline trap per declaration. Exists
  because `finOutcomeStagingGate`'s identity arm
  (`finding_staging_gate_test.go:299`) is byte equality between claude's
  verbatim `input.command` and whatever the rig says it staged — get either
  operand wrong and every *correctly*-staged run reports
  `stage-command-not-staged`, one live claude turn burned per attempt.
  `finLiveStageCommand(fifoPath)` splices `probeHeldCommandName` rather than
  re-typing `"cat"` (a rig staging one verb while #1340's liveness check
  looks for another would drift silently; the splice makes a rename a build
  break) and is deliberately bare, never `finOutcomeHoldCommand`'s
  `sh -c … ; exit 0` stand-in shape. `finLiveStagePrompt(fifoPath)` follows
  `probePrompt`'s backtick-delimited form with the *whole* command
  interpolated, not just the path, so the prompt and the staged literal
  derive from one `fmt.Sprintf` instead of being written twice; the offline
  trap recovers the command back out of the prompt by an independent
  delimiter scan (`finLiveStageCommandFromPrompt`) rather than comparing
  against a hand-copied second literal. `finLiveStageFIFOName =
  "fin-live-stage-hold"` is checked both-directions substring-disjoint
  against all eight shipped FIFO name/path constants, referenced **by
  identifier** so a rename breaks the build instead of rotting the taken-set
  list silently — re-derived at `26d83b7` via
  `rg -n 'FIFOName *=|FIFOPath *=' internal/e2e/realclaude/` (the
  `FIFOPath`-inclusive recipe; a `FIFOName`-only search misses #1338's
  `finLivePinFIFOPath`). `finLiveStageEnvDelta()` names
  `BASH_DEFAULT_TIMEOUT_MS=5000` (the settled #1223 trigger) and
  `PYRY_USE_STREAMJSON=0` explicitly — the latter because
  `reachRunnerPathFromEnv` reads the ambient `os.Getenv` first, so an empty
  delta would make the downstream runner reading a reading of the operator's
  shell; its offline trap sets a hostile ambient (`t.Setenv`) to prove the
  claim is non-vacuous rather than accidentally true whenever the variable
  happens to be unset. Purely additive, one new file, 489 lines, zero
  production change, zero live caller — #1340 is the driver that spends a
  real turn on these declarations. See [`codebase/1342.md`](../codebase/1342.md)
  for the full implementation, the mutation-tested lessons, and the
  reachable-red-vs-shadowed-by-Fatalf lesson code review surfaced on the
  extraction round-trip's pass-through guard.

  **#1349 adds a sibling, `finLiveStageStreamEnvDelta()`** — the same two
  keys with `PYRY_USE_STREAMJSON=1`, two independent literals never derived
  from `finLiveStageEnvDelta()` (a clone, append or wrap would defeat the
  property that an edit to either can't silently change the other). Its
  trap, `TestFinLiveStageStreamEnvDeltaNamesTheRunner`, is a sibling of
  `TestFinLiveStageEnvDeltaNamesTheRunner`, never a copy: the hostile
  ambient is `PYRY_USE_STREAMJSON=0` rather than `=1`, and its control's
  honesty is asymmetric because the truthiness rule is one-sided — only the
  exact string `"1"` is truthy, so a `0` ambient is indistinguishable from
  unset and the control excludes an *effective* ambient of `1` without
  establishing non-vacuity by construction the way the shipped trap's does.
  See [`codebase/1349.md`](../codebase/1349.md).

- `finding_live_assembly_test.go` (#1343) — **the join, not a probe**; the one
  function, `finLiveAssembleStaging`, that fills all eight
  `finOutcomeStaging` fields — three read from the run's transcript via
  `finTranscriptFill` (#1304), five supplied by the caller as
  `finLiveAssembleFacts`, `finTranscriptReading`'s mirror image — and returns
  `finOutcomeStagingGate`'s decision (#1284) as returned, never re-derived.
  Exists because nothing previously called both halves together: the only
  thing filling the five caller-side fields was `finTranscriptStagedCaller`,
  a #1304 test fixture whose hardcoded `PinMatchCount: 1, PinWantCount: 1` is
  wrong for the rig, whose real expectation is `finLivePinWantRows = 2`
  (#1338) — an assembly that inherited the `1` would send every
  correctly-staged live run to `finOutcomePinCountUnexpected`, burning a live
  claude turn per attempt. The composite literal is name-for-name with no
  literal on any right-hand side, which is the one rule that keeps both the
  fixture's `1` and the driver's `finLivePinWantRows` out of the assembly's
  body — the counts are forwarded unaltered, neither re-derived nor fixed
  internally. `facts.StagedCommand` is the single source of the staged
  string, closing structurally (rather than by care) the two-consumer drift
  between the gate's identity arm and the fill's own `call.Command == staged`
  guard. `finLiveAssembleContractWant = finLivePinWantRows + 1` backs a
  deliberate contract row over a want no live driver emits — the only row
  that catches an assembly forwarding the match count while fixing the want
  internally — derived rather than written as a literal so it can never
  coincide with the real constant. Test drives four rows over one
  correctly-staged synthetic transcript, written once in the parent, with
  every assertion reading the assembly's return value rather than
  `finOutcomeStagingGate` directly, so it proves the counts travel without
  re-asserting `finOutcomeGateCases`' (#1284) already-shipped count mapping.
  Two mis-assemblies survive every row by construction — a count swap inside
  the literal, and hardcoding the three transcript fields at their staged
  values — and are stated as accepted in the file's own header rather than
  chased with the duplicate rows this ticket's AC forbade reproducing.
  Purely additive, one new file, 428 lines, zero production change, zero live
  caller — #1340 (driver) and #1337 (record/classification) are the tickets
  that call `finLiveAssembleStaging` for real. See
  [`codebase/1343.md`](../codebase/1343.md) for the full implementation, the
  mutation matrix, and the code-review NIT on the assembly's two adjacent
  `time.Duration` parameters.
