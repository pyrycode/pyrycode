# finding_stream_exit_path_probe_test.go
- `finding_stream_exit_path_probe_test.go` (#1353) — **the headless
  `PYRY_USE_STREAMJSON=1` structural sibling of #1337, and only that path**:
  the two runners do not write the same trailer and do not present the same
  process tree at trailer time, so the sibling file's central argument is not
  merely different here, it is **inverted** — copying it across would ship a
  false claim. `TestRealClaude_StreamExitPathWhileCommandRuns` stages a turn
  via `finLiveRunStage` under #1349's `finLiveStageStreamEnvDelta`, waits for
  pyry's own exit **in the body, never a cleanup** with the FIFO hold still
  held (`finStreamExitPyryExitDeadline`, 120s, deliberately its own constant
  and not `finExitPyryExitDeadline`, for the same asymmetry argument at the
  same value plus one genuinely path-specific leg — pyry's own `cmd.Wait` on
  claude blocks on claude's stdout **pipe**, not just process exit, bounded by
  `streamrunner`'s `WaitDelay`), takes the claude-still-alive corroboration
  read and a **second**, distinct pinned-pid re-read, hands one
  `finGatherReadings` call the observed runner-path reading (reduced once via
  `tdnRunnerFromArgv`, reused at both the gate and the record so the two
  cannot disagree) and the pinned-pid read, then **certifies #1439's ordering
  after the gather and fills `trailRunReadings.Ordering`** — deliberately
  never `finGatherInputs.Ordering`, which #1462 shipped and left structurally
  unfillable by any live caller; this ticket routes *around* that circularity
  rather than resolving it, so the input field's zero and its "no live
  caller" doc both stay true. `finExitClassify` is **reused from #1337's
  probe, not copied**. The one new symbol, `finStreamCertifyOrdering`, passes
  a **structural `true`** (not a guess) as the ordering's `holdHeld`
  premise — `holdProbeFIFO` releases only in the `t.Cleanup` it registers
  itself, and this rig registers none — pinned by a six-row offline trap
  whose one load-bearing clause is that **no row may answer
  `trailOrderVoidUnheld`**; RED→GREEN proven by three `go test -overlay`
  mutants (`holdHeld`→`false`, `== trailSeen`→`!= ""`, transposed premises).
  Because `streamrunner.Run` reaps only inside `cmd.Cancel`, which its own
  comment says never fires on a clean exit, **a healthy run on this path
  writes no reap log at all** — the evidence leg that carried #1337's entire
  verdict is empty by construction here, not merely late, so what stands in
  its place is #1439's/#1440's pinned-pid sighting route: the strongest
  reachable claim is that the command was still running **when the trailer
  was sighted on pyry's stdout** (`run-alive-at-sighting-by-ordering`), which
  is strictly weaker than #1337's aliveness-at-declared-finish claim because
  no `terminal_reason` is certified here and so no declared-finished instant
  exists — the finding sentence states that gap explicitly rather than
  approximating the stronger claim in weaker words. Seven `t.Logf` sites
  publish what the artifact writer's two files don't (staging result,
  classifier outcome, certified ordering, exit timing, observed path vs. gate
  value, corroboration, the finding), every rendering `json.Marshal` or
  field-by-field, **never `%v` on a struct or slice**. One shipped-file edit,
  comment-only and per-hunk line-neutral, corrects `finding_live_run_test.go`'s
  "only caller" claim now that `finLiveRunStage` has two. Security-sensitive
  (architect self-review PASS); one code-review round, PASS with one
  non-blocking SHOULD FIX (a stale "no live run stages the ordering" doc
  claim at `trail_run_outcome_test.go:1243` the spec's file-clearance argument
  missed) and three NITs. **The live measurement itself has not run as of this
  writing** — unlike #1337, where the codebase note was written before the
  operator's live run and updated once it landed, this ticket's automated
  `needs-real-claude` gate removed the label on the strength of the tagged
  suite's generic 652/652 pass, without noticing that
  `TestRealClaude_StreamExitPathWhileCommandRuns` was itself among that run's
  15 *skipped* tests — a gap in the dispatcher's opt-in-probe handling, not in
  this diff. See [`codebase/1353.md`](../codebase/1353.md) for the full
  implementation, that gap recorded precisely, and the exact invocation still
  owed a live run.

- `interactive_change_workspace_test.go` (#1029) — first real-`claude`
  coverage of `change_workspace`, split from #963 (`change_workspace` had
  fake-tier coverage only, #980). Rescoped during refinement: the original
  ask — proving real claude picks up the changed cwd — is unbuildable, since
  no production path reads a stored `conv.Cwd` and spawns with it (a
  session's spawn workdir is fixed at runner construction, and
  `RestartFresh` never re-reads it on respawn). What ships instead is the
  narrower real-tier-only claim: the verb round-trips correctly against a
  **live** supervised claude child, and does not wedge or kill it — a
  scripted fake cannot regress a real child dying.
  `TestInteractiveChangeWorkspace` drives the #1028/#1031 sequential spine
  on one daemon / one seeded bound conversation: turn 1 (liveness, drained
  to `turn_state{idle}`) → `change_workspace` (target minted directly,
  request sent in **tilde form** `~/ws-<nonce>` so the "resolved realpath,
  not the raw request" assertion discriminates on every platform, not just
  where `$HOME` sits behind a symlink) → assert the `conversation_updated`
  reply's `Cwd` is the confined realpath and that it's persisted on disk →
  turn 2 (liveness, proves the verb left the live child undisturbed). The
  turn-2 delta is produced by the child still running at the conversation's
  **original** cwd — a liveness assertion, never evidence of cwd adoption;
  the test's header states this at length so a later reader doesn't "fix"
  it into a hang. Reuses the restored harness (`harness_daemon_test.go`,
  #1473, restoring what #1348 deleted) and the #997/#1028 control-frame
  helpers (`sealEnvelope`, `drainForReply`, `assertConversationUpdated`)
  verbatim; the only new code is `readConversationCwdOnDisk` (~25 lines,
  mirrors #1028's `readConversationIDsOnDisk`). Zero production files
  touched. The fake tier (`relay_v2_change_workspace_test.go` #980) owns
  the confine-reject-no-leak and not-found paths — not duplicated here.
  See [`codebase/1029.md`](../codebase/1029.md).
