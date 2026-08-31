# trail_run_outcome_test.go
- `trail_run_outcome_test.go` (#1271) — the **run-level classifier**:
  `trailClassifyRun(trailRunReadings) trailRunOutcome` maps one probe run's raw
  observations onto exactly one of sixteen outcomes (four answers, twelve named
  voids — `trailOutcomeVoidPathOwesNoReason` is #1417's,
  `trailOutcomeVoidReasonNotOwedByPath` — a `terminal_reason` present on a
  runner path that owes none, a genuine reading rather than a caller's bug —
  is #1434's, `trailOutcomeAliveAtSightingByOrdering` — the second
  answer, and the only one from an evidence class other than the reap log —
  is #1446's, `trailOutcomeVoidPinnedPidDidNotEstablish` — the sighting
  route having *measured* a pinned pid without establishing aliveness at the
  trailer's sighting, split off the blanket #1446 left on
  `trailOutcomeVoidPathOwesNoReason` — is #1447's, and
  `trailOutcomeVoidSightingRouteNotStaged` — the route never having been
  staged at all, split off that same blanket, which #1447 left shared between
  a never-staged run and a measured-premise-failure one — is #1448's) so a run
  that measured nothing is recorded as having measured nothing
  rather than falling through to a finding. Consumes #1270's two admissibility
  results; a nine-check contract block (C1–C9) guards the top, calling
  #1270's/#1235's shipped membership predicates rather than re-deriving them,
  so the out-of-contract value is a guard, never a switch default. An
  admissible attribution is consulted *before* any point-in-time reading
  (proof outranks pyry-not-exiting outranks every instrument void), because
  the point-in-time reads are expected to be late and must never be what a
  verdict rests on — the systematic-false-negative case this ticket exists to
  prevent is a regression row in `TestTrailClassifyRun`. Input and outcome
  records carry discriminators and counts only — `BoundFrom` rather than the
  `trailObservation` that embeds `trailScanResult`, `MatchCount`/`RowsScanned`
  rather than `pinScan.Matches`' verbatim argv, no command string anywhere —
  enforced by a marshal-and-search test with a needle in four inputs. Folds in
  #1270's parked SHOULD FIX (an uncontracted `certified` parameter) at both the
  layer it was found and as a composition pair (C4/C5) one layer up. Purely
  additive, one new file plus a ~70-line extension of #1270's own closure test
  (eighteen constants → twenty-nine); 14 `TestTrail`-prefixed functions, 82
  subtests, 0 SKIP on `-run '^TestTrail'`. **#1446** later wires the one arm
  from which no evidence route was reachable — `trailGateAbsentOwesNone` — to
  #1440's `trailEstablishSighting`, consulted from inside the arm rather than
  by falling through to the scan-side steps below it; `trailRunReadings`
  gains `Ordering`/`PinnedPid` (both taken whole, and deliberately
  unvalidated by the contract block — at the time, no shipped gather staged
  either field, so a tenth check would have filed every run that existed then
  as `trailOutcomeOutOfContract`. **#1458** later stages the pin half from the
  one live caller, `finExitRunProbe` — **since #1353, a second live caller
  stages the same pin half, see below**; **#1462** later gives `finGatherInputs`
  a field the ordering can arrive on but wires no live caller to fill it, so
  the ordering half stays unstaged through *that field* on every run that
  exists today — **since #1353, see below**; the field
  remains uncontracted even with a carrying field shipped, since its zero
  already has an argued home inside the route itself — see
  [`codebase/1458.md`](../codebase/1458.md) and
  [`codebase/1462.md`](../codebase/1462.md)), and `trailRunOutcome` gains `Route`
  (`evidence_route`, empty except on the two finding values) so a reader
  never infers which evidence class produced a verdict from the outcome
  value alone; the void arm's Detail exchanges its out-of-contract clause for
  the route's answer rather than growing past its 22-byte headroom. **#1447**
  then takes the route's *measured non-establishment* case off that same
  blanket void: where the route measured a pinned pid and did not establish
  aliveness (`sighting.Value == trailSightingUnestablished`), the arm now
  answers `trailOutcomeVoidPinnedPidDidNotEstablish` rather than
  `trailOutcomeVoidPathOwesNoReason`, and publishes `Route =
  trailRouteSighting` on it — the first time a *void* carries a route, so
  `Route`'s own doc and `trailRouteSighting`'s doc were both corrected from
  "set on the finding alone" to "set wherever the route's own measurement
  decided the value." **#1448** then separates the two cases #1447 still left
  sharing that same blanket: a guard on `readings.Ordering.Value == ""`,
  tested *before* `trailEstablishSighting` is called and deliberately
  single-sided (only the ordering side lacks a documented "unfilled maps
  here" clause in its reason constant's doc — the pid side already has one),
  routes the never-staged case to a value of its own,
  `trailOutcomeVoidSightingRouteNotStaged`, while the remaining fall-through —
  route staged, consulted, measured nothing — keeps
  `trailOutcomeVoidPathOwesNoReason` and now also publishes `Route`. A new
  field, `trailRunOutcome.RouteReason` (`evidence_route_reason`), carries
  `sighting.Reason` through whole on both sighting-void arms so a reader
  tells a measured premise failure apart from an unanswered pid read — the
  two verdicts the route itself returns identically — without parsing the
  Detail; the published invariant is a biconditional,
  `RouteReason != "" ⟺ Route == trailRouteSighting`, since the reap-log route
  has no reason space and can never publish one. No default arm added in
  either ticket: each new branch sits inside the arm's existing total
  coverage rather than growing it by a case. See
  [`codebase/1271.md`](../codebase/1271.md),
  [`codebase/1446.md`](../codebase/1446.md),
  [`codebase/1447.md`](../codebase/1447.md) and
  [`codebase/1448.md`](../codebase/1448.md).

- `trail_run_rig_test.go` (#1268) — **proof-of-wiring rig, not a new
  instrument.** #1266/#1270/#1271 each prove their piece against fixtures and
  synthetic buffers; `trailClassifyRun` is pure, so a fixture proof never
  shows which code path fed it — a rig wired to the wrong path emits the same
  positive as one wired to the right one. This file gathers the classifier's
  inputs through the live producer chain (`trailWaitForTrailer` → `trailGate`,
  `pinScanArgv`, `pinReadState`, `tdnClassifyReapLog` → `trailAdmitAttribution`)
  against a real FIFO and a real `cat`, funnelled through one seam
  (`trailRigGather`) so "no field is hand-assigned and no reap line is
  synthesised" is a property of the file rather than a promise about its call
  sites — `tdnClassifyReapLog` is called over a literal `nil` inside that
  function, never a parameter. Three tests: a pre-subject reading
  (`trailOutcomeNoRowMatched`) flipping to a during-subject reading
  (`trailOutcomeMatchedUnattributed`) across one subject's life, with both
  post-death per-pid states (`pinStateExitedNotReaped` then
  `pinStateNoSuchProcess`) taken deterministically because the subject is a
  direct child; a staleness-bound margin pinned tight enough that a
  start-derived (rather than miss-derived) bound fails it; and a
  shell-wrapped subject staged so more than one row matches, without
  resolving "the" pid. `trailOutcomeRunningAtTrailer` — the finding itself —
  stays deliberately unreachable, twice-stated in the header: it requires a
  reap line this rig must not grow. Purely additive, one new file, 594 lines,
  zero existing call sites changed. See [`codebase/1268.md`](../codebase/1268.md).

- `finding_attribution_fanout_test.go` (#1280) — **the many-to-one reduction**:
  `trailAdmitAttribution` (#1270) takes one held process group; the probe's
  argv scan returns a set, because `pinScanArgv` deliberately refuses to
  resolve "the" pid. `finAttributeFanOut(stderr []byte, pgids []int, certified
  string) finAttributeRecord` reduces that set to the single `trailAdmitResult`
  `trailRunReadings.Admit` (#1271) accepts, under a total order
  (`finAttributeOrder`, proof first, argued in the code) so no group's void
  suppresses another group's proof and no composition of voids manufactures
  one. Two record-level conditions, never selectable values:
  `finAttributeGroupUnreportable` (a `pgid <= 1` group `reap.go:52` skips
  before it ever kills anything — surfaced rather than handed to
  `tdnClassifyReapLog`, which would misattribute the staging fault to the
  instrument) and `finAttributeNoGroups` (no reportable group remained — a
  staging fault, `Selected` left zero rather than filled with either of the
  two publishable falsehoods AC4 prices). The credential channel is closed by
  the **signature** — `pgids []int`, never `[]reachProc` — not a check;
  `certified` crosses verbatim by design (already-shipped, publishable
  behaviour) and the fan-out multiplies its copy count by the distinct-group
  count, each capped at 512 bytes. `finAttributeEntry` carries only `PGID`
  and `Admit` — no `tdnReapOutcome.Line`, no `reachProc.Command`. Purely
  additive, one new file, 767 lines, zero production change, zero consumer
  call sites; four top-level tests, 0 SKIP on `-run '^TestFinAttribute'`. One
  code-review SHOULD FIX, not blocking, deferred to #1281: the no-captured-
  bytes structural check is top-level-key-only over what is now a *nested*
  record, so a future `Command` field added to `finAttributeEntry` would pass
  it unnoticed. See [`codebase/1280.md`](../codebase/1280.md) for the full
  implementation, the selection-order argument, and the mutation-tested
  lessons.

- `finding_staging_gate_test.go` (#1284) — **the tier below the classifier**:
  `trailClassifyRun` (#1271) assumes a run staged — a Bash call issued, the
  rig's hold command, a completed rendezvous — and on an unstaged run its
  argv scan still runs over a healthy process table and matches nothing,
  landing on `trailOutcomeNoRowMatched`: a real answer, published as a false
  negative about a run where no command ever existed. `finOutcomeStagingGate(
  finOutcomeStaging) finOutcomeResult` decides, from synthetic staging
  conditions alone, one of six failure outcomes or the pass-through
  (`finOutcomeReadyToClassify`, deliberately not the zero value — an unfilled
  result must never read as "staged, go classify"), all seven in their own
  `stage-` sub-namespace apart from the fourteen's `run-`. The structural
  closure is the signature itself: neither type mentions `trailRunReadings`,
  so a failure arm holds nothing a classifier call could be made from — the
  forbidden call is unwritable, not discouraged. Two guard conditions close
  reachable pass-through holes (both commands left empty; an unfilled
  match-count want agreeing with an unfilled count at zero). No Detail
  interpolates either command — both the issued command (verbatim model
  output) and the staged one (embeds a `t.TempDir()` path and an
  `exec.LookPath` result) are captured strings on the same footing — and the
  no-captured-bytes test plants `trailNeedle` in both, with a per-row
  headroom assertion against `trailDetail`'s 512-byte cap: house-style Detail
  prose alone was found to eat enough of that cap in the first draft to
  truncate a leaked command's needle away before it could be caught, a
  vacuity distinct from (and the mirror image of) #1278's cap hazard. Purely
  additive, one new file, 790 lines, zero production change, zero consumer
  call sites. See [`codebase/1284.md`](../codebase/1284.md) for the full
  implementation and the mutation-tested lesson on redaction-test vacuity.
