# interactive_stream_model_announced_test.go
- `interactive_stream_model_announced_test.go` (#1634) — the live proof that a
  real claude's announced model reaches the daemon's **own emitted frame**,
  not just claude's stdout. #1582's `interactive_stream_inband_model_test.go`
  taps `streamsup.Config.Stdout` upstream of the parser, so it stayed green
  through the whole period `turnbridge.MapEvent` had no
  `turnevent.ModelAnnounced` case and would stay green if #1638's two arms
  were reverted; `TestInteractiveStreamModelAnnouncedFrame` instead drains the
  sealed `protocol.TypeModelAnnounced` frame at a connected fakephone and reds
  against a tree missing either arm. Reuses #1582's `inbandModelTargets` table
  as the equality comparand (never a fresh literal) paired with an inequality
  against the spawn alias — the discriminator that tells a future stale-row
  failure (inequality green) apart from claude regressing to announcing the
  bare alias (inequality red). Carries `spawnBootstrapDaemonVerbose`, a
  `spawnBootstrapDaemonWithIdle`-style near-copy of the shared spawner whose
  sole delta is `-pyry-verbose`: both plausible leak sites for the value
  (`streamsup`'s undecodable-line drop, `Handle`'s unknown-event default) log
  at Debug, so at the shared spawner's default `LevelInfo` the no-leak
  assertion would pass vacuously. Drain returns at the turn's
  `turn_state{idle}` close rather than at the frame's arrival — under either
  mutant no frame is ever emitted, so a frame-first drain would burn its full
  budget on every red run; closing on turn-end keeps a mutation proof in this
  package cheap (~5s red vs. the 120s timeout).

  **Two lessons that outlive this ticket:**
  - **`go test -overlay` cannot mutate this package's daemon-side targets.**
    AC-3's mutants live in `turnbridge.MapEvent` and `cmd/pyry`'s
    `interactiveTurnEmitterV2.Handle`, but this test asserts against a
    *separately built* daemon binary — `ensurePyryBuilt` shells out to a
    plain `go build` with no overlay forwarding, and the `cmd/pyry` mutant is
    never compiled into the test binary at all. An `-overlay` passed to
    `go test` reaches neither mutant and both mutant runs come back green — a
    false pass, not a weak one. The route that works: `go build
    -overlay=<abs path json> -o <tmp bin> ./cmd/pyry`, then
    `PYRY_E2E_BIN=<tmp bin>`, which `ensurePyryBuilt` returns unbuilt without
    rebuilding. Any future mutation proof of daemon-side (as opposed to
    test-process-side) code in this package needs this route, not the house
    `-overlay`-into-`go test` shortcut used elsewhere in the suite.
  - **A no-leak haystack can contain the value it's guarding against for an
    unrelated reason.** The daemon's `spawning claude` record logs the argv
    verbatim, `--model haiku` included — an absence assertion searched
    against the spawn alias rather than the drained frame's resolved `Model`
    value fails on every healthy daemon. Search for the value that actually
    crossed the wire, never the value that was asked for.

  Zero production files touched.

- `session_transcript_probe_test.go` (#1655) — measures whether a `claude`
  launched under `--session-id <id>` that runs no turn leaves an `<id>.jsonl`
  on disk, the premise a suspected `streamsup` crash-loop (2026-08-18) rests
  on and [ADR 032](../decisions/032-bootstrap-resume-per-spawn-existence-probe.md)
  needs before #1630 can carry its by-id-existence rule into `streamsup`.
  `TestRealClaude_TurnlessSessionIDTranscript` runs a control arm (one turn;
  the transcript's appearance pins the sessions directory empirically and is
  compared against `sessions.DefaultClaudeSessionsDir`) and a turnless arm
  read twice — while alive, and again after a `SIGTERM`→grace→`SIGKILL`
  termination — through `classifyTurnlessTranscript`, which reads the
  termination mode so a force-killed absence can never be recorded as the
  fact holding. **Measured HOLDS** (claude 2.1.220): full record and
  reproduce steps in
  [`session-transcript-and-resume-probe.md`](session-transcript-and-resume-probe.md).
  Credential-free companion `TestTurnlessTranscriptVerdict` pins the
  classifier's five outcome rows offline.

  **Two lessons that outlive this ticket:**
  - **A `*bytes.Buffer` behind a live `exec.Cmd` cannot be read while the
    child is still running.** The liveness `t.Fatalf` path reads the
    turnless arm's stderr with the child still alive, racing `os/exec`'s own
    copy goroutine under `-race`. A mutex-guarded `boundedBuffer` is needed
    regardless of the separate ingest-cap requirement — a plain capped
    buffer still races on this read.
  - **`agentrun.ResolveWorkdir` returns `(string, error)`, not a bare
    string.** It wraps `fs.ErrNotExist`; a caller that drops the error can
    set `cmd.Dir` on a workdir that no longer exists and silently invalidate
    any directory comparison built on it.

  Zero production files touched.

- `resume_absent_transcript_probe_test.go` (#1656) — measures the other half of
  #1655's premise: how claude answers `--resume <id>` when `<id>.jsonl` is
  absent, the half the suspected `streamsup` crash-loop actually turns on.
  `TestRealClaude_ResumeAbsentTranscript` establishes a real transcript
  `<A>` through one turn, pre- and post-reads a reserved absent id `<C>`
  through the same by-id instrument, then runs both a `--resume <C>` arm and
  a `--resume <A>` control arm — same builder (`resumeProbeArgs`), same
  workdir, same 45 s deadline — through `classifyResumeAbsent`, which reads
  the control **first**: a control that itself rejects a resume of an
  *existing* transcript short-circuits to INCONCLUSIVE regardless of what
  the absent arm did. **Measured HOLDS** (claude 2.1.220): the absent arm
  exited 1 (`No conversation found with session ID: …`, carried on both
  stdout and stderr) while the control sat on stdin past its deadline. Full
  record and reproduce steps in
  [`session-transcript-and-resume-probe.md`](session-transcript-and-resume-probe.md).
  Credential-free companions `TestResumeAbsentVerdict` (all nine
  `{exit 0, exit non-zero, did-not-exit}²` cells) and
  `TestResumeProbeArgsIsRespawnShape` (the respawn argv differs from the
  first-spawn argv only in the trailing id-flag pair) run with no claude at
  all.

  **Two lessons that outlive this ticket, both about classifying a
  terminated child's exit code:**
  - **A did-not-exit outcome needs a liveness guard, not just a code
    comparison.** `snapshotExit` (from #1655) returns `-1` for a child that
    has not exited, and `-1 != 0` — a "was it rejected?" predicate written
    as `ExitCode != 0` alone reads a child still sitting on stdin as a
    rejection. The predicate here is `Exited && ExitCode != 0`, and the
    offline table's did-not-exit rows deliberately use `ExitCode: -1`
    (mirroring `snapshotExit`'s real sentinel) rather than a conveniently
    zeroed field, so a dropped guard shows up as four reds, not zero.
  - **Terminating an arm before snapshotting it manufactures the verdict.**
    `SIGTERM` leaves exit 143 behind, indistinguishable from a rejection at
    read time. The fix is ordering, not a special case: snapshot the
    pre-termination exit code first, call `endTurnlessChild` only on the
    did-not-exit path, and route the post-signal code into a cleanup field
    the classifier never reads. The same hazard applies to any future probe
    in this package that classifies a child's exit code after it may have
    been signalled.

  Zero production files touched.
