# trailer_terminal_reason_test.go
- `trailer_terminal_reason_test.go` (#1366) — **offline instrument, not a
  probe**; consumes #1357's `KeyNames` reading and answers a question neither
  it nor the decoded scalar can answer alone: what a trailer's
  `terminal_reason` *means*, given the runner path the run was observed to
  take. `trailReasonAgainstPath(runnerReading string, keyNames []string,
  decodedReason string) trailReasonResult` maps onto a closed six-value
  `reason-`-prefixed set — the cross product of {absent, present-and-empty,
  present-and-non-empty} × {streamrunner, ptyrunner, indeterminate}, since
  absence means opposite things on the two runner paths (ptyrunner's trailer
  is pyry's own and owes a non-empty reason by construction; streamrunner
  passes claude's bytes through unchanged, `streamrunner/runner.go:177-179`,
  so a healthy run's trailer is claude's own and owes none at all). Presence
  is read from `keyNames` alone, never from `decodedReason != ""` — taking it
  from the scalar would merge "absent" and "present-and-empty" on the
  owes-one path, since both decode to `""`. The reading is reduced with the
  shipped `finRecordRunnerLabel` (`reachRunnerPathFromArgv` is deliberately
  not used — it keys on a flag both argv builders pass and has no
  `streamrunner` answer at all); the default arm
  (`trailReasonPathUnnamed`) is not the fall-through catch-all
  `trailGate`/`trailAdmitAttribution`/`trailClassifyRun` each refuse, because
  its meaning — "the path reading names no runner" — is true of every
  non-runner label without exception, so there is no out-of-contract case
  left to guard against. The present-on-owes-none value
  (`trailReasonPresentOwesNone`) carries a stated claim limit: the line is
  not that path's documented healthy shape, and *never* that pyry wrote it,
  since claude can produce the same reading through the passthrough. No
  input byte interpolates into any `Detail`, at all — stronger than the
  AC requires, made structural rather than disciplined. Purely additive, one
  new 643-line file, zero production files touched, zero callers on landing —
  **#1420 is its first decision-path caller**, from `trailGate`'s absence
  branch, and **#1433 its second**, from the presence branch; nine
  cross-product rows plus a three-variant
  indeterminate-outranks-a-qualifying-shape sub-test, 0 SKIP on
  `-run '^TestTrail'`. Code review PASS with one non-blocking SHOULD FIX left
  unfixed — the table's closure-reached-set loop keys off the *expected*
  value rather than `got.Value`, so its "catches an unhandled sixth value"
  doc comment overstates what it does (the per-row assertion already covers
  that mutation class). See [`codebase/1366.md`](../codebase/1366.md) for the
  full implementation, the AC-by-AC proof, and both code-review findings.

- `finding_key_name_containment_test.go` (#1362) — **offline instrument, not
  a probe**; proves end to end, over the **files the artifact writer actually
  wrote**, that no value from a trailer line reaches the artifact through
  #1363's published `trailer_keys` field — the gap left by #1364, which
  proved the field's bounds bite but stopped at the reader's own return.
  Four checks, no one subsuming another, driven through a shared
  `finContainRender` helper that scans a fixture line, substitutes it as
  `finWriteInputs().Trailer`, writes the artifact under `t.TempDir()`, and
  returns only the rendered files plus the reader's `[]string` key names —
  never the `trailScanResult` or `*resultTrailer` a first draft returned,
  fixed as a security-review MUST FIX so no failure message in the file can
  print `scan.Line`. AC1 sweeps every written file for a needle planted past
  the 512-byte cap inside `result` and as `session_id`'s whole value,
  excluding the four positions (`subtype`, `is_error`, `terminal_reason`,
  `stop_reason`) the record publishes verbatim by design — planting there
  would fail a correct build. AC2 sweeps the published names field alone
  against a needle in all five string-valued positions (reusing
  `trailKeyNamesNeedledTrailer` at pad 0, the deliberate inversion of the
  reader-tier check's pad 600), the only check that can catch a builder
  copying one of the four by-design positions into the names field. AC3 walks
  both carriers with `finRecordInputReaches` banning
  `map[string]json.RawMessage`, paired with a control reaching `[]string`.
  AC4 plants a needle as a top-level key name and asserts it reaches
  `trailer_keys` but no Detail anywhere in the artifact — discharging the
  names half of the Detail prohibition #1364 left open (the count half has no
  instrument and stays stated-unproven). Also repairs
  `finding_trailer_evidence_test.go:200-204`, false since #1364 cut the
  fixture it described; five comment lines for five, line-count-neutral.
  Purely additive, one new 650-line file, zero production files touched.
  Four mandated mutants plus two run beyond the mandate (a schema-edit
  overlay, since M4 has no one-line form) all RED in the direction the
  matrix predicts; PASS on review with two non-blocking SHOULD FIX (a
  precondition that preempts AC1's sweep on M1 rather than letting the sweep
  itself fire — same fix shape as AC2 already uses; a projected Detail-size
  figure the shipped fixture's own measurement superseded). See
  [`codebase/1362.md`](../codebase/1362.md) for the full implementation, the
  mutation table, and both lessons learned.

- `finding_run_record_test.go` (#1291) — **the assembled run record.**
  `finRecordRun` is the record one probe run publishes: pyry's exit code,
  every matched row reduced to `finRecordProc` (`PID`/`PPID`/`PGID` — three
  `int` fields, reflection-asserted, nothing else), the per-pid liveness
  verdicts (`[]pinStateOutcome`, carried whole), the reap-log attribution
  (`finAttributeRecord`, #1280) and the trailer sub-record
  (`finTrailerRecord`, #1290), both embedded whole rather than re-derived,
  and the runner path. The runner path is recorded **as observed**: the
  env reading (`reachRunnerPathFromEnv`) is carried as documentation, not
  corroboration, alongside an independent argv reading
  (`tdnRunnerFromArgv`), reduced to a three-valued `RunnerAgreement` —
  `agree` / `disagree` / `indeterminate` — decided on the **label** each
  reading's leading token, never the whole string, because the two
  producers append their own free-text reasons and the full strings are
  therefore never equal even when both name the same runner.
  `finRecordInputs` uses named fields rather than positional parameters
  specifically because two adjacent same-typed strings
  (`RunnerFromEnv`/`ClaudeCommand`) sit on opposite sides of the argv
  prohibition, and it carries neither a `trailObservation` nor a
  `trailScanResult` field, which is what keeps the discriminated-optional
  trailer pointer out of reach. Purely additive, one new file, 1061 lines,
  zero production change, zero consumer call sites; five top-level tests,
  all offline. One code-review SHOULD FIX left non-blocking: the
  attribution sub-record's "carried whole" claim is pinned by a single
  nested scalar rather than `reflect.DeepEqual` (the trailer half's
  pattern), so a future partial-carriage regression there would pass
  unnoticed — deferred to #1286. See [`codebase/1291.md`](../codebase/1291.md)
  for the full implementation and the mutation-tested lessons.

- `finding_artifact_write_test.go` (#1286) — **rendering the run record into
  a pasteable artifact, and proving the directory it lands in leaks no
  captured bytes.** `finWriteArtifacts(t, dir, rec finRecordRun)` takes the
  built record and nothing else — no raw process-table bytes, no second
  `[]byte` parameter — and writes exactly two files: `run.json`
  (`json.MarshalIndent`) and `run.md` (a fixed safety-claim constant, the
  same bytes fenced, one summary line built from derived scalars only). The
  signature *is* the design: `writeReachArtifacts` (`background_reach_probe_
  test.go:823`) is the cautionary precedent it deliberately does not
  reuse — that writer's unexported `rawPS` field produces a second file,
  `reach.ps.txt`, carrying the verbatim process table beside a clean
  `reach.json`; `finRecordRun` has no unexported field, so there is nothing
  raw in this writer's reach to write. Four tests measure what was
  **written**, not what was built: a set-equality census of every JSON
  *path* the record declares against every path the artifact renders
  (path-based rather than name-based after a code-review MUST FIX — four of
  the family's key names are shared across types, and `matched_rows[]`'s
  three keys are shared with `pinStateOutcome`'s, so a name-based census
  covered that slice not at all); a `trailNeedle` sweep over
  every file `os.ReadDir` returns (planted only in inputs the pipeline
  reduces or drops — a matched row's argv, the claude argv, a reap
  outcome's stderr — never in the four fields the record carries whole),
  with a mandated pair of applied-and-reverted
  mutations (one inside the Detail format, one adding an undeclared third
  file) both observed RED before the sweep shipped; a recursive
  forbidden-key scan with two exact-key exemptions (`tool_stderr`, carried
  whole and permitted; `runner_from_argv`, a closed three-constant set with
  no input byte in reach); and a structural + behavioural pair proving
  `resultTrailer` has no `result` member and that the four decoded trailer
  scalars cross into the artifact verbatim while the needle beside them does
  not. The sweep shipped with a fourth channel, a trailer-scan-line plant
  landing **inside** `reachCapCommand`'s 512-byte cap (pad `0`, needle at
  byte 104–146) — `trailNeedle`'s own comment claims it is placed past the
  cap, which this ticket measured to be false against the fixture the
  family actually reuses; the comment was left uncorrected as a sibling
  file, out of scope here. **#1326 retired that fourth channel**: since
  #1320 `finTrailerBuild` takes the sighting carrier, and the needle in the
  scanned line is consumed at fixture-construction time by
  `finTrailerSighting` — which never reads `.Line` — so it never enters
  `finRecordInputs` and the writer performs no reduction there. The in-cap
  fixture (`finWriteTrailerPad = 0`) was kept, not deleted: it still backs
  a diagnosis-and-guard pair relocated onto the pre-build clean check for
  the embedded trailer sub-record (a prospective guard against a future
  builder that starts reading the line) and the four-scalar-vs-needle
  pairing in the verbatim-output test, which rests on the weaker claim that
  the *wire* line carries the needle at every pad regardless of the cap and
  so needs no cap guard of its own. The retired in-cap claim itself now
  holds one tier down, at `TestFinGatherReturnsNoCapturedBytes`
  (`finding_run_gather_test.go`), which sweeps the carrier. Purely
  additive, one new file, 966 lines then trimmed by #1326's prose-and-guard
  rewrite, zero production change, zero consumer call sites. **The fixed
  safety-claim constant (`finWriteSafetyClaim`) was repaired by #1363** when
  the trailer's key names — claude-authored strings, not "a string this rig
  authored" — became a published field: the sentence now names that field
  explicitly and states it is safe because it is bounded and value-free, never
  because it is rig-authored, while keeping the note's two forbidden
  review-caveat strings absent. See
  [`codebase/1286.md`](../codebase/1286.md) for the full implementation, the
  path-vs-name census MUST FIX, and the stale-comment lesson,
  [`codebase/1326.md`](../codebase/1326.md) for the channel retirement, and
  [`codebase/1363.md`](../codebase/1363.md) for the safety-claim repair.
