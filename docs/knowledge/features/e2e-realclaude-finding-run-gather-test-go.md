# finding_run_gather_test.go
- `finding_run_gather_test.go` (#1281, `PyryExited`/`ClaudeState` promoted
  #1302, trailer-sighting carrier added #1309, carrier's miss bound proven
  #1312, carrier's four decoded scalars proven to come from the full-line
  decode #1313, published record's bound proven to be the classified
  sighting's #1316) — **parameterises
  `trailRigGather` (#1268) on the two inputs it hardcoded.** That rig passes
  a `nil` literal as the reap-log stderr and keys attribution on the test
  process's own process group; under those two hardcodings,
  `trailAdmitProof` — and with it `trailOutcomeRunningAtTrailer`, the only
  outcome that is a finding — is structurally unreachable, so a probe built
  on it would report a clean negative forever with no symptom.
  `finGatherReadings(in finGatherInputs) (trailRunReadings,
  finAttributeRecord, finSighting)` takes `Stdout`, `Needles`, `Stderr` and
  `Pinned` as fields and, driven offline from synthetic stdout/stderr,
  reaches both the finding and a genuine negative
  (`trailOutcomeNoRowMatched`, never a `run-void-*`) through its own
  composition, both at `MatchCount == 0` under a certifying gate —
  demonstrating rather than describing that Step 2 outranks the match-count
  arms. `Pinned` is `[]int`, never `[]reachProc`, continuing #1280's
  credential-channel-closed-by-signature pattern; the `[]reachProc` →
  `[]int` conversion is left to #1282's call site by design. The trailer
  observation is a function-local and never returned, which is what keeps
  `trailScanResult.Trailer`/`.Line` structurally out of the caller's reach.
  A recursive forbidden-key walk (lowercased keys, two named exact-key
  exemptions) closes the flat-only-key-scan gap #1280 left open for nested
  records. `finGatherInputs.PyryExited`/`.ClaudeState` (#1302) are the same
  struct's remaining two fields — copied into the readings whole, no
  default, no repair — and are exercised by two more top-level tests: one
  varying `PyryExited` alone across an identical stdout/needle pair to prove
  the outcome moves (`trailOutcomeNoRowMatched` ↔
  `trailOutcomeVoidPyryDidNotExit`), one carrying a documented verdict, an
  undocumented one, and `""` through unchanged. The third return, `finSighting`
  (#1309), is what the classified poll *measured* — scan state, the bound and
  its discriminator, staleness, a carries-a-decoded-trailer discriminator and
  the four decoded scalars (`Subtype`/`IsError`/`TerminalReason`/
  `StopReason`) — filled from the same `trailWaitForTrailer` call that fills
  `BoundFrom`, so no second scan is needed to recover what the sighting saw.
  It reaches none of `trailObservation`, `trailScanResult` or `resultTrailer`
  (proven by walking types, reusing `finRecordInputReaches` rather than a
  second traversal), so `.Line` and the decoded `*resultTrailer` stay exactly
  as unreachable as before; #1320 moved `finTrailerBuild` onto this carrier,
  via a fixture-side helper (`finTrailerSighting`) that is a copy of this
  file's fill and inherits its agreement obligation.
  Purely additive, zero production change, zero consumer call sites; nine
  top-level tests, 0 SKIP on `-run '^TestFinGather'`. #1312 adds the row #1309
  shipped without: `TestFinGatherSightingReportsTheMissBound` leaves the
  buffer unseeded (this file's first row to do so, and its first to cost wall
  clock — ~600ms), appends the trailer past two poll ticks on a spawned
  goroutine's sibling, and proves `BoundFrom` reports `trailBoundFromMiss` —
  the discriminator that actually bounds something, as opposed to
  `trailBoundFromStart`, which every pre-seeded row reaches and whose own doc
  says it BOUNDS NOTHING. A second, direct `trailWaitForTrailer` call over the
  same buffer supplies the contrast (`trailBoundFromStart`), with only its
  discriminator ever bound to a variable — never the observation itself, which
  carries the two things the carrier exists to keep unreachable. #1313 is #1312's
  sibling half of the #1310 split: a standalone test on a 585-byte over-cap
  fixture (`trailPaddedTrailer(200)`) proves the same carrier's four decoded
  scalars come from `trailScanResult.Trailer` — the full-line decode — and
  never from a re-read of the capped `.Line`, which fails to decode wholesale
  on a syntax error rather than losing fields one at a time. The precondition
  pins the bare `"terminal_reason"` **key** (never the `"max_turns"` value,
  which survives every cap via `subtype`'s `error_max_turns`), asserted so a
  fixture edit that collapses the disagreement fails loudly instead of the row
  going quietly vacuous. #1316 adds this file's second and last row that costs
  wall clock, `TestFinGatherRecordPublishesTheMeasuredMissBound`, placed
  directly after #1312's row: it builds a `finTrailerRecord` from the
  composition's classified sighting over the same unseeded-buffer/delayed-append
  idiom, and puts it beside a record built over the same frozen bytes from a
  second, direct `trailWaitForTrailer` call — joining the record tier (which
  pinned `Bounded` with the discriminator handed in) to the carrier tier
  (#1312, which measured the discriminator but stopped short of publishing it),
  separated by measurement rather than by `finTrailerBuild`'s input type.
  **#1452** adds a seventh field, `RunnerPath string` — the runner-path
  reading, **already reduced by the caller** — so `finGatherReadings` no
  longer types `trailRunnerUnread()` into the gate's `RunnerPath` at its own
  call site. The reduction happens at the call site and never inside the
  gather (`Pinned []int`'s own doctrine, applied to a string):
  `finExitRunProbe` (#1337, below) now passes `RunnerPath:
  tdnRunnerFromArgv(h.Pin.ClaudeCommand)`, so verbatim argv never enters
  `finGatherInputs`. A new pure helper, `finGatherRunnerPath(reading string)
  string`, maps an unstaged `""` to `trailRunnerUnread()` before the gate
  sees it — total and information-preserving (`trailRunnerUnread()` **is**
  `tdnRunnerFromArgv("")`), and a **publication** fix rather than a
  classification repair: `finRecordRunnerLabel("")` already returns `""`, so
  `""` and `trailRunnerUnread()` reach the identical gate decision; what
  changes is whether `trailGateResult.RunnerPath`'s `omitempty` silently
  drops the field. The gather's needle set is untouched — `RunnerPath` is
  never appended to `in.Needles`, proven by a fourth, deliberately abusive
  row (`RunnerPath: os.Args[0]`, itself a matching needle) against a
  `t.TempDir()` baseline rather than a cross-row `os.Args[0]` comparison,
  which this package's 29 `t.Parallel()` calls and five re-exec sites would
  make flaky. A live run reaches `trailGateAbsentOwesNone` — #1417's value,
  unreachable from any shipped gather until now — for the first time; the
  ptyrunner-presence row keeps #1337's recorded finding classified the same
  way. Neither `trailGateCases()` nor `finGatherCases()` grew; the new
  coverage lives in its own table.
  **#1458** adds an eighth field, `PinnedPid pinStateOutcome` — the other half
  of #1440's pinned-pid sighting route, staged the same way `RunnerPath` is:
  reduced at the caller and carried through the gather whole. `finExitRunProbe`
  takes a second `pinReadState` call, over the first entry of `h.Pin.PGIDs`
  (never `h.ClaudePID`, which is claude's own pid and answers the wrong
  route) and after the exit wait rather than at gather time, guarded so an
  empty pinned set (a failed scan) takes no read at all rather than calling
  `pinReadState(0)` — which would answer `pinStateInstrumentFailed` and so
  claim an instrument ran. The reading lands on `PinnedPid` and never
  `Liveness`, proven by a dedicated test rather than left to the type
  checker, since `pinReadState`'s return type matches `Liveness`'s element
  type exactly and an `append` there would compile. Because the classifier's
  `Ordering.Value == ""` guard is single-sided and, at the time,
  `finGatherInputs` carried no field the ordering could arrive on at all, no
  outcome moves: a live run still answers `run-void-sighting-route-not-staged`,
  now from a genuinely half-staged pair rather than a hypothetical one —
  asserted by a second new test comparing whole `trailRunOutcome` values.
  Thirteen shipped comment sites whose truth or stated basis rested on no
  gather staging this input were swept in the same commit, two of them now
  naming **#1459** — the gather-tier captured-bytes sweep this route's opened
  channel still owes — as the ticket that updates them again once that sweep
  lands.
  **#1462** later adds the ninth field, `Ordering trailOrderResult` —
  appended after `PinnedPid`, never mid-struct, so the file's in-body bare
  `:NNN` citation tail (the tax #1452's insertion paid in full) takes no
  displacement — carrying the sighting route's *other* input, produced only
  by `trailCertifyOrdering` at the call site and taken **whole**, never
  narrowed to `.Value`: a hand-built `trailOrderResult{Value:
  trailOrderCertified}` would let everything downstream pass against a
  certification that certifies nothing. **No live caller fills it, and none
  is wired here by design** — the premise `trailCertifyOrdering` needs,
  `holdHeld`, is a fact about a FIFO the *caller* holds, and the one live
  sighting call (`finGatherReadings`'s own) reports its sighting only on its
  third return, after the point a certification would need it; recovering
  the premise by sighting the trailer a second time at the call site would
  degrade the gather's *own* sighting to `trailBoundFromStart`, a
  discriminator whose own doc says it BOUNDS NOTHING. So the half-staged
  pair #1458 shipped is still what every live run brings; driven offline
  through the shipped gather and classifier instead, a certified ordering
  (`trailCertifyOrdering(true, true, true)`, never a literal) beside a
  still-running pinned-pid read reaches `trailOutcomeAliveAtSightingByOrdering`
  for the first time from a gather rather than a hand-built classifier
  fixture, and beside a refuting read reaches
  `trailOutcomeVoidPinnedPidDidNotEstablish` under that refutation's own
  published reason — three new rows, each cross-checked against
  `trailEstablishSighting`'s own answer taken over the readings the gather
  produced, so a row can't agree with a classifier that hardcoded a reason
  the predicate no longer emits. Ten shipped comment sites across both files
  carrying variants of "no gather stages the ordering" were split rather
  than flipped: the carriage half goes false and is corrected, the
  no-live-caller half stays true and is kept, and all five stale **#1457**
  attributions (#1457 is CLOSED, the parent this ticket split from) are
  repaired to name the carriage as landed and the staging as still unowned —
  never repointed to "#1462 stages it," and never a placeholder ticket
  number for the wiring, since none exists yet. No C10 contract check added;
  the no-C10 note's basis is restated, not replaced. Split from #1457;
  security-sensitive (architect self-review PASS); one code-review round,
  PASS with two non-blocking NITs. Purely additive to this file, zero
  production files touched. See [`codebase/1281.md`](../codebase/1281.md),
  [`codebase/1302.md`](../codebase/1302.md),
  [`codebase/1309.md`](../codebase/1309.md),
  [`codebase/1312.md`](../codebase/1312.md),
  [`codebase/1313.md`](../codebase/1313.md),
  [`codebase/1316.md`](../codebase/1316.md),
  [`codebase/1452.md`](../codebase/1452.md),
  [`codebase/1458.md`](../codebase/1458.md) and
  [`codebase/1462.md`](../codebase/1462.md) for the full implementation and
  the mutation-tested lessons.
