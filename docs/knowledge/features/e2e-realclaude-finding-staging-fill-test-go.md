# finding_staging_fill_test.go
- `finding_staging_fill_test.go` (#1304) — **fills the staging record from a
  run's own transcript.** `finOutcomeStagingGate` (#1284, above) decides all
  seven staging outcomes from synthetic inputs; this file fills exactly the
  three transcript-side fields (`BashIssued`, `IssuedCommand`, `TriggerFired`)
  a real caller would supply, through a `finTranscript*` composition reading a
  JSONL transcript the test writes at the session's own path. The scan's unit
  is a `finTranscriptBashCall{ToolUseID, Command}` pair rather than a bare
  command: the shipped `probeWaitForBashToolUse` returns the **first** Bash
  `tool_use` regardless of `input.command` (a #1223 code-review SHOULD FIX
  shipped unfixed), and #1230 guarded that value-side caller-side already
  without editing the shared rig — this file generalises the guard and closes
  a second, key-side route to the same defect: a composition that selects the
  staged call for its *command* but keeps the first call's *id* would still
  read the trigger off the decoy's `tool_result`, since the trigger reading is
  `probeWaitForToolResult(<id>)`. The content guard (`finTranscriptSelect`) is
  pure over its input — no `*testing.T` — so its removal (AC2's mutation) runs
  and grades without touching the worktree; the first-match id is bound inside
  an `if` statement in `finTranscriptSelectBash` and goes out of scope
  immediately after, making it unreferenceable rather than merely unused
  below. `TriggerFired` reads `timedOutAfterMs` presence alone, never
  conjoined with the handle and never corroborated by
  `tool_use.input.run_in_background` — that flag marks the model-set
  backgrounding path this probe must exclude (`docs/knowledge/codebase/
  1223.md:87-88`). Nothing on the path trims, unquotes, or canonicalises
  either command; both new types carry no json tags, mirroring
  `finOutcomeStaging`'s own rule (`finding_staging_gate_test.go:141-157`).
  Purely additive, one new file, 602 lines, zero production change, zero
  consumer call sites. See [`codebase/1304.md`](../codebase/1304.md) for the
  full implementation and both mutation-tested rows.

- `finding_trailer_evidence_test.go` (#1290, builder moved onto the sighting
  carrier #1320, published bound proven measured #1316) — **the trailer half
  of the probe's published record.**
  `finTrailerRecord` (ten scalars, no pointer, no embedded observation)
  carries one run's outcome value together with the trailer evidence behind
  it — scan `State`, the `BoundFrom` lateness discriminator with its
  `Bounded` boolean (`== trailBoundFromMiss` and nothing else, never
  `Staleness != 0`) and `Staleness` itself, and the four decoded trailer
  fields (`Subtype`, `IsError`, `TerminalReason`, `StopReason`).
  `finTrailerBuild(outcome string, sighting finSighting) finTrailerRecord`
  is the pure projection: since #1320 it takes the #1309 carrier rather than
  a `trailObservation`, so its input carries no `.Line` and no
  `*resultTrailer` — both the record it returns and the builder itself are
  now trap-free by construction, checked by
  `TestFinSightingReachesNoScanType` rather than asserted in prose. The four
  fields are read from `sighting`'s own scalars under a guard on
  `sighting.CarriesTrailer` (a bool the carrier precomputes — no pointer left
  to guard a dereference of; a no-trailer run returns its void instead of
  panicking, unreachably now rather than through a checked short-circuit);
  `Outcome` is copied from the caller's #1271/#1284 value as handed, never
  re-derived from `State`. On the false arm the four scalars are zeroed
  rather than copied through — under the carrier that is a decision the
  builder makes rather than a consequence of there being no pointer to read,
  pinned in both directions by
  `TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer` over one
  carrier with its one impossible bit flipped. `StopReason` is the one
  exception to trap-free: forwarded from the model's last message uncapped,
  by design, named explicitly so a sweep author doesn't plant a needle in a
  field the record must carry verbatim. No field carries `omitempty` — under
  it a seen trailer with an empty `terminal_reason` would render
  byte-identical to a no-trailer record, the exact collapse the nil-pointer
  design one tier down exists to prevent. `TestFinTrailerRecordCarriesNoCapturedBytes`
  no longer plants `trailNeedle` here — #1325 retired that plant along with the
  test's other `.Line`-dependent checks, since the carrier the builder now takes
  has no `.Line` for a needle to sit in. The in-cap plant (`trailPaddedTrailer(0)`,
  needle inside the 512-byte cap at offset 104–146, chosen over the family's
  habitual past-the-cap pad specifically so a record that kept the capped line
  would still be caught) lives one tier down instead, at
  `TestFinGatherReturnsNoCapturedBytes` (`finding_run_gather_test.go`), which
  sweeps the carrier itself. What remains in this file is two channel-independent
  construction rules on `finTrailerRecord`: the per-row `Detail` headroom
  assertion (#1284's fix, argued as a type-level rule that travels — the record
  embeds whole into `finRecordRun.Trailer` and from there into the artifact, so
  a Detail that ate its own budget would defeat the marshal sweep and the
  artifact's file byte sweep two tiers up) and the flat forbidden-key scan
  (`finTrailerRecord` is ten scalars, so a top-level key scan is exhaustive).
  The shell `TestFinTrailerRecordReadsTheDecodedTrailer` is gone; its one
  surviving row — the four scalars come from the full-line decode rather than
  the capped copy — is promoted to top-level as
  `TestFinTrailerSightingScalarsComeFromTheFullLineDecode`, re-stated onto
  `finTrailerSighting` (the builder reads neither `Trailer` nor `Line`) and
  named to mirror `TestFinGatherSightingScalarsComeFromTheFullLineDecode`, the
  two halves of one agreement obligation that a grep now returns together.
  Purely additive at #1290, one new file, 697 lines, zero production change, zero
  consumer call sites. **#1363 added a fifth trailer *field*, deliberately not a
  fifth decoded scalar** — `KeyNames []string` (`json:"trailer_keys"`, no
  `omitempty`), copied through `finTrailerBuild` from a different reader
  (`trailKeyNames` over the full line) than the four scalars above, bounded by
  `finBoundKeyNames` at the fill sites rather than in this builder (which copies
  and computes nothing). Every "ten scalars" / "the four decoded scalars"
  sentence in this file stays true as written; only the record's total field
  count moved. See [`codebase/1290.md`](../codebase/1290.md) for the
  original implementation, [`codebase/1320.md`](../codebase/1320.md) for the
  move onto the carrier, [`codebase/1325.md`](../codebase/1325.md) for the
  retirement, [`codebase/1316.md`](../codebase/1316.md) for the row that
  joins this file's `Bounded` derivation to a poll that genuinely measured it
  (`finding_run_gather_test.go`'s `TestFinGatherRecordPublishesTheMeasuredMissBound`),
  and [`codebase/1363.md`](../codebase/1363.md) for the key-names field.

- `finding_key_name_bounds_test.go` (#1364) — **offline instrument, not a
  probe**; pins all five clauses of `finBoundKeyNames`' doc comment (#1363),
  none of which shipped pinned. Two tests drive hostile fixtures through the
  shipped builders (`trailScan` → `finTrailerSighting` → `finTrailerBuild`)
  and assert both the unbounded reader output and the published, bounded
  names from shipped code alone; two call the helper directly for the two
  clauses no fixture can reach (nil-not-`[]string{}` on empty input; its own
  backing array on every path, including the under-both-bounds fast path a
  fixture can never exercise). The hostile fixtures are deliberately small —
  a few hundred bytes — because `trailScan`'s `bufio.Scanner` buffer aborts
  rather than truncates past its 64 KiB default, and on the aborted arm the
  names field renders `null`, making "the field is bounded" trivially true
  over a fixture that produced no names at all; the drive helper asserts
  `trailer-seen` as a fatal precondition specifically to catch a future
  fixture that grows into that ceiling. Marker-aware: a truncated name
  carries `reachTruncationMarker` on top of the kept bytes, so
  `len(name) <= finTrailerMaxKeyNameBytes` is red against a correct build.
  Purely additive, one new 382-line file, zero production files touched,
  zero existing test files touched — the AC that no inbound line-number cite
  in `internal/` moves holds by construction rather than by argument. Seven
  mutants of `finBoundKeyNames`, run via `go test -overlay`, all RED; 75 PASS
  / 0 SKIP on `-run '^TestFin|^TestTrail'` (71 before). Explicitly left for
  #1362: the Detail-interpolation prohibition (measured and cut — the
  hostile fixture's names-interpolating mutant reaches 444 of a 470-byte
  headroom budget and would ship green over the violation it claims to
  detect) and the artifact-wide containment sweep. See
  [`codebase/1364.md`](../codebase/1364.md) for the full implementation,
  the mutation matrix, and a stale comment in
  `finding_trailer_evidence_test.go:203` left for #1362 to correct.
