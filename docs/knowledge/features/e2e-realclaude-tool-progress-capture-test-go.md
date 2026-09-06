## tool_progress_capture_test.go (#2089)

The live half of the `tool_progress` capture: stages a foreground `Bash` call
held open on a FIFO long enough to cross claude's ~30s heartbeat interval, and
commits `testdata/tool_progress_v<version>.json` for
[`internal/streamsup`](streamsup-package-tool-progress-consumed-by-matching.md)'s
offline proof to run inside `make check` against.

### A held FIFO's release must be bounded, and must not close the read end early

`tpcapHoldFIFO` is a from-scratch sibling of this package's `holdProbeFIFO`
(see [finding_stage_held_group_test.go](e2e-realclaude-finding-stage-held-group-test-go.md)),
not a reuse of it, because this probe must let the hold go **mid-test** so the
turn can end rather than pinning release to `t.Cleanup` alone. Building it from
scratch reproduced a bug class worth knowing about anywhere a FIFO is used to
stage a held command in this package:

- **The no-reader release path raced on BSD/XNU.** When `release` fires before
  any reader ever opened the FIFO, the write-side goroutine is parked in
  `open(O_WRONLY)`; unblocking it means opening the read end. A transient
  open-then-close wakes a blocked writer on Linux (`r_counter` latches) but can
  be missed on the BSD/XNU shape, which re-tests `readers == 0` after the
  wakeup and parks again. The fix is to hold the read end open until the
  writer goroutine has actually exited, not to close it immediately after the
  wake.
- **`release`'s wait for that goroutine must itself be bounded, and its open
  error must be reported, not swallowed.** The first draft did neither, and a
  real gate run hung on it: `internal/e2e/realclaude` died at the 20-minute
  `go test` timeout with 939 tests passed and 0 failed — the shape of a
  suite-level crash, not of a capture that found nothing (see the note on
  reading executed-test counts in `CLAUDE.md` § Testing, and the general
  `sync.Once`-cleanup hazard this instance of). A swallowed open error is
  worse than silent: it gets recorded as `foreground_call_observed: false`,
  which blames claude for never running the command when the actual fault is
  the instrument's own `open()` failing.

Any future FIFO-hold probe in this package should check both properties before
trusting a green run: does release ever block unboundedly, and does a failed
`open` on the write side surface as an instrument error rather than as a
finding about claude?

### Gate the probe on the fixture's absence, not on an env var

The seven sibling probes in this package (`background_reach_probe_test.go`,
`teardown_liveness_probe_test.go`, and the rest) are unconditionally
`PYRY_PROBE_*`-gated one-off instruments that write to a tempdir for a human to
carry into the repo. That shape is wrong when the capture is itself an
acceptance criterion: `make e2e-realclaude` — the tier this ticket's live gate
actually runs — never sets a custom `PYRY_PROBE_*` variable, so an env-only
gate makes the probe skip before it reaches the credential check, the run
reports green, and the fixture never lands (the `#1763` failure mode: a
capture that still needs a human to copy a file is a capture that did not
happen).

`TestRealClaude_ToolProgressCapture` instead arms on the fixture's absence and
disarms once `testdata/tool_progress_v<version>.json` exists; the env var
survives only as a forced re-capture at a new claude version. A capture
satisfying every AC is promoted in-repo by the run that produced it
(`fixtureWorthy`), never by a human copying a tempdir file, and the version is
pinned from both ends — the reader fatals on a `claude_version` mismatch, and
`fixtureWorthy` refuses to write under a mismatched filename — so a claude
upgrade forces a loud re-capture rather than a fixture that quietly describes
some other release.

### Staging a duration via a prompted `sleep` proves nothing; a rig-held FIFO does

An earlier staging (a prompted foreground `sleep 75`) produced a turn that
ended in 9s with no `tool_progress` frames and no way to tell why: whether
claude never called Bash, shortened the duration, requested a short tool
timeout, or backgrounded the call all look identical from outside. Staging
`cat <fifo>` instead gives a positive rendezvous signal — the FIFO open is
proof a foreground call actually started — and puts the held duration under
the rig's control instead of the model's. `stagingVerdict` turns the outcome
into a three-way read (never ran / started but cut short / held past the tick
and still nothing) so a zero-frame run says which of those happened instead of
pointing at the marker set by default.

### Related

- [`internal/streamsup`'s `tool_progress` arm](streamsup-package-tool-progress-consumed-by-matching.md) — the consumer this capture proves against, and the wire facts (30s heartbeat interval, guard scope) the capture confirmed.
- [finding_stage_held_group_test.go](e2e-realclaude-finding-stage-held-group-test-go.md) — the package's original `holdProbeFIFO`, staging a held command over a real FIFO in its own process group.
