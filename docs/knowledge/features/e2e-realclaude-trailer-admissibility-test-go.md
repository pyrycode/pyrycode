# trailer_admissibility_test.go
- `trailer_admissibility_test.go` (#1270) — **offline instrument, not a
  probe**; two pure predicates that decide whether #1266's trailer scan and
  #1253's reap-log attribution can support a claim, so #1271's downstream
  classifier never has to. `trailGate(trailGateInput) trailGateResult` maps
  onto a seven-value positive allowlist (`trailGateUsable`/`NoTrailer`/
  `ScanAborted`/`BudgetFired`/`AbsentOwesNone`/`PresentOwesNone`/`OutOfContract`)
  and certifies a non-empty terminal reason on the two arms that carry one.
  **#1373** widened the
  input from a bare `trailScanResult` to `trailGateInput{Scan, RunnerPath}`
  so the gate's input can carry which runner produced the trailer line
  (`terminal_reason` means different things on ptyrunner vs. streamrunner);
  `RunnerPath` is echoed onto a fourth `trailGateResult` field. Until #1420
  no arm read it; since #1420 exactly one does — the absence arm. What
  `TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt` proves is the
  per-row declaration (9 rows × 5 readings): a row that does not declare
  it has a byte-identical `Detail` across them, and the declaring row's
  variance is proved positively by the same test. See
  [`codebase/1373.md`](../codebase/1373.md). **#1419** split the
  out-of-contract arm that fires on an empty `terminal_reason` into two: the
  key absent from the line, and the key present with a blank value —
  discriminated by scan-produced `KeyNames` membership
  (`slices.Contains(keyNames, trailReasonKeyName)`), never by the decoded
  value or by cardinality, both of which collapse the two shapes. Both
  arms still answer `trailGateOutOfContract` with an empty `Reason`, so at
  #1419 the five-value gate allowlist and every downstream consumer were
  unchanged; only the published `Detail` — and which of the gate's ten
  return sites (#1420) a given input reaches — changed. See
  [`codebase/1419.md`](../codebase/1419.md). **#1417** then diverges exactly
  one of the three absence sub-cases — absent `terminal_reason` on a path
  the observed reading reduces to `streamrunner`, which owes none — to
  `trailGateAbsentOwesNone`, a sixth gate value that certifies no reason
  (`Reason` stays empty) but is no longer `trailGateOutOfContract`: a
  healthy headless `PYRY_USE_STREAMJSON=1` run's trailer is claude's own
  `result` line by construction (`streamrunner.Run` passthrough,
  `internal/agentrun/streamrunner/runner.go:177-179`; watchdog-only
  synthesis, `:250-253`), so filing it as an out-of-contract caller bug was
  the defect. The other two absence sub-cases (owes-one, path-unnamed) and
  present-and-empty keep `trailGateOutOfContract`, so at #1417 `trailGate`
  still had ten return sites, five of them `trailGateOutOfContract` (was
  six). `trailClassifyRun` (#1271, below) gains a matching step-1 arm rather
  than falling through: `trailOutcomeVoidPathOwesNoReason`, a twelfth run
  outcome and a void — not `trailOutcomeVoidNoTrailer` (a trailer *was*
  written on this path) and not `trailOutcomeOutOfContract` (this is a
  genuine reading, not a caller's bug). At #1417 this was unreachable from
  either shipped live gather — both filled `RunnerPath` with
  `trailRunnerUnread()` — so the value was reachable only from fixtures; no
  comment added by that ticket claimed the gate decides against the path a
  live run took. **#1452** closes that gap for the finding gather (below) —
  it no longer holds. See [`codebase/1417.md`](../codebase/1417.md).
  **#1433** then reads the runner
  path on the **presence** side — the complementary half of #1420's absence
  split, and the gate's second decision-path caller of
  `trailReasonAgainstPath`. A `terminal_reason` that IS on the line, present
  and non-empty, from a run whose observed path reduces to `streamrunner`
  (owes none), reaches a new return site answering the shipped
  `trailGateOutOfContract` and certifying nothing — placed *after* the
  budget arm (a `max_turns` trailer on such a path keeps reaching the budget
  arm; the budget void is structural and outranks every reap-side void) and
  *before* the present-and-empty branch (which stays path-invariant
  deliberately, since the reduction's one absorbing answer can't express the
  `NO LIVE REPRO EXISTS` distinction between a blank key and an absent one).
  `trailGate` now has **eleven** return sites, **six** of them
  `trailGateOutOfContract` (was five). The Detail cites the 32 B
  `trailReasonPresentOwesNone` constant rather than embedding the
  reduction's 395 B Detail (445 B measured, against a 470 B ceiling); the
  runner-path sweep's `pathVaries` exemption widens to cover the certified
  `Reason` too, since the usable row is now the first arm whose
  certification itself moves with the reading — repaid by a positive
  per-reading companion sub-test rather than left as withdrawn coverage. No
  closed set grows; promoting the shape to a gate value of its own,
  mirroring #1417's move for absence, is **#1434**. See
  [`codebase/1433.md`](../codebase/1433.md). **#1434** then makes that
  promotion: the presence arm answers `trailGatePresentOwesNone` — a
  seventh gate value — instead of `trailGateOutOfContract`, and still
  certifies nothing (`Reason` stays empty). `trailGateOutOfContract`'s
  enumerated sub-cases drop from six to five; `trailGate` still has eleven
  return sites, but only five answer `trailGateOutOfContract` (was six). The
  Detail keeps citing the case constant rather than the reduction's Detail
  (427 B measured, against the same 470 B ceiling — down from #1433's 445 B
  after the closing "a value of its own is #1434" clause came out).
  `trailClassifyRun` (#1271, below) gains a matching step-1 arm,
  `trailOutcomeVoidReasonNotOwedByPath` — a thirteenth run outcome and a
  void, distinct from its `trailOutcomeVoidPathOwesNoReason` sibling (same
  void-ness, opposite reading: absent-on-owes-none is that path's healthy
  shape, present-on-owes-none is not) and from `trailOutcomeOutOfContract`
  (a genuine reading, not a caller's bug). Both closed sets grew in one
  commit, since step 1's switch has no default arm and an unhandled value
  would fall through to steps 3-8 and award a scan-side answer about pyry
  from a record the gate says certifies nothing. Two code-review FAIL/PASS
  rounds on the family's repeat failure mode — bare `(:NNN)` cites resolved
  by last-named-file instead of by symbol, both correct on `main` and moved
  anyway — fixed by re-deriving bare cites from their named symbol first.
  See [`codebase/1434.md`](../codebase/1434.md).
  `trailAdmitAttribution(tdnReapOutcome, certified string) trailAdmitResult`
  maps the reap attribution onto a seven-value allowlist — one admissible
  value (`trailAdmitProof`, requiring verdict `tdnReapHeldPGIDKilled`,
  exactly one reap line, and a non-`max_turns` reason) plus five named voids
  plus an out-of-contract value. Both open with a contract block ahead of
  every real arm, so out-of-contract is a guard at the top, never a
  fall-through default. The reap-side voids are outranked by the
  budget-fired void (structural: on that path the reap ran before the
  trailer, so the reap record's contents are irrelevant), which is itself
  outranked by the contract block (a caller's bug must surface regardless of
  path). `trailGateResult` is trap-free by construction — no `*resultTrailer`
  reachable from it, directly or through an embedded field — even though the
  gate cannot be the pointer trap's last consumer (`trailObservation` embeds
  `trailScanResult`, so `.Trailer` is still reachable by promotion elsewhere).
  `trailBudgetTerminalReason = "max_turns"` is a string literal with no
  executable pin to `emitter.go`'s unexported `wireFields`; a production
  rename would silently turn a budget-fired void into a false proof — named
  as a known limit, not fixed, since fixing it needs either a production
  change or a live budget-fired fixture, both out of scope for this
  probe-family ticket. Purely additive, one new file, zero production files
  touched; 50 subtests, 0 SKIP on `-run '^TestTrail'`. One code-review
  SHOULD FIX (an uncontracted `certified` parameter that lets `""` read as
  `trailAdmitProof`) shipped as a named, un-fixed gap — see
  [`codebase/1270.md`](../codebase/1270.md) for the full implementation, the
  ordering arguments, and the deferred findings.
