# teardown_liveness_probe_test.go
- `teardown_liveness_probe_test.go` (#1251) — **live rig, security-sensitive**;
  opt-in behind `PYRY_PROBE_TEARDOWN_LIVENESS=1` on top of the package's normal
  auth skip. The standing check that keeps #1231's hand-verified answer true:
  stages one claude turn around #1223's `BASH_DEFAULT_TIMEOUT_MS` trigger, tears
  pyry down through its real SIGTERM-to-pid path, and records whether the
  backgrounded Bash command survived and by whose hand it died — calling
  #1250's classifier/record, #1235's liveness read, and #1239's FIFO read as
  code, not evidence. **The core idea is `tdnBeforeFault`**: the same
  after-teardown liveness classifier is run at the before-snapshot too, and a
  run whose before reading isn't uniformly `pinStateRunning` voids rather than
  passes — an instrument hard-wired to `dead` cannot pass a live run for free.
  Two structural discriminators separate the three readings that all look like
  "dead after exit": the still-held FIFO (released only in `t.Cleanup`, after
  the SIGTERM/wait/after-snapshot run inside the test body — the
  `background_reach_probe_test.go:355-356` cleanup-ordering trap inverted on
  purpose) rules out the command finishing on its own, and a pgid in the reap
  line rules out dying alongside claude (`reap.go:56-62` skips `ESRCH` before
  the append). `tdnRecord` widened rather than duplicated: `HeldPID` → `HeldPIDs`
  (a slice — #1230's live run matched two rows on one needle), single
  `ArgvScan`/`Liveness`/`FIFO` → paired `Before`/`After *tdnSnapshot`, plus
  `ClaudeVersion`/`TeardownPath`/`RunnerFromEnv`/`RunnerFromArgv` provenance.
  Disposition is a positive allowlist with one red arm
  (`tdnDispositionLeaked`); a content re-match is dispositive only when the
  after-liveness verdict is `pinStateRunning` — otherwise a zombie's
  kernel-blanked argv would misfile as pid reuse. A ninth reject branch beyond
  the spec guards teardown provenance itself: pyry exiting on its own before
  the rig's SIGTERM would still read `dead-by-reaper` correctly but attribute it
  to a `TeardownPath` that never ran. Repairs #1250's inherited SHOULD FIX (a
  reap-classifier fixture row that didn't discriminate its own claimed
  mutation) by swapping a concatenation order, verified red-then-reverted by
  deliberate mutation. Deliberately does **not** reuse
  `reachRunnerPathFromArgv` for the runner label — it keys on
  `--append-system-prompt-file`, which the streamrunner path also emits, so
  reuse would have silently mislabelled every stream-path record; the fresh
  `tdnRunnerFromArgv` discriminates on `--session-id` vs. `--input-format`
  instead. Live test named `TestRealClaude_TeardownLiveness` (not `TestTdn…`)
  so it doesn't join #1250's zero-SKIP offline suite; three offline
  self-checks (`TestTdnRunnerFromArgv`, `TestTdnDecideAfter`, `TestTdnPinHeld`)
  do. 48 subtests, zero SKIP on `^TestTdn`; full package 216 PASS / 44 SKIP.
  Zero production files touched. **The live half has not been run** — no
  claude login in the dispatch environment; ticket carries `needs-real-claude`.
  See [`codebase/1251.md`](../codebase/1251.md) for the full implementation and
  two non-blocking code-review findings (a SHOULD FIX and a NIT, both deferred
  to a future touch on this file).

- `result_trailer_observation_test.go` (#1266) — **offline instrument, not a
  probe**; ships the observation only, no verdict. Answers "when did pyry's
  `{"type":"result",...}` trailer first become visible on stdout, and how late
  might that observation be?" for #1267's downstream liveness classifier.
  Two closed value spaces, neither collapsible into the other's zero value:
  `trailSeen`/`trailAbsent`/`trailAborted` (what a pure scan, `trailScan`,
  found) and `trailBoundFromMiss`/`trailBoundFromStart`/`trailBoundNone` (what
  the staleness bound was measured from — `trailBoundFromStart` names the trap
  where a first-poll match yields a duration that bounds nothing, because the
  write may precede the poll loop entirely). Fixes a gap in the existing
  `parseResultTrailer` (`tool_loop_test.go:216`, untouched, all nine call
  sites keep today's behaviour) without touching it: that function discards
  `scanner.Err()`, so a stdout line past `bufio.Scanner`'s 64 KiB default
  (reachable — the trailer's `result` field is the last assistant message
  verbatim) reads identically to a genuine absence; `trailScan` reads the
  scanner error and reports the new `trailAborted` state instead. The cap
  (`reachCapCommand`, `reachMaxCommandBytes = 512`) is applied only to the
  retained verbatim `Line` copy, never to the bytes decoded into
  `resultTrailer` — `result` sits sixth on the pinned wire order
  (`emitter.go:456-468`) and `terminal_reason` last, so capping the raw line
  would truncate inside `result` and destroy the field #1267 branches on;
  `resultTrailer` has no `result` member, so the decoded value structurally
  cannot leak the assistant payload regardless. `trailWaitForTrailer` polls
  `probeSyncBuffer` on the existing `probePollInterval` (200 ms) and stamps
  `now` **before** reading the buffer each iteration, which is what makes a
  miss-derived bound an over-estimate of the true lateness rather than a
  possible under-estimate wearing a bound's label. Purely additive, one new
  file, zero production files touched; 11 subtests, 0 SKIP on
  `-run '^TestTrail'`. See [`codebase/1266.md`](../codebase/1266.md).

- `trailer_key_names_test.go` (#1357) — **offline instrument, not a
  probe**; adds `trailKeyNames(line []byte) []string`, invoked from
  inside `trailScan`'s existing match return on `scanner.Bytes()` — the
  full line, before `reachCapCommand`'s 512-byte cap, and never
  `trailScanResult.Line` — and lands on a new `trailScanResult.KeyNames
  []string` field (`json:"trailer_keys,omitempty"`). Answers a question
  #1266's fixed eight-field `resultTrailer` decode structurally cannot:
  `TerminalReason` is `omitempty`, so an absent `terminal_reason` and one
  emitted as `""` both decode to `""`, and `terminal_reason` is a pyry
  invention absent entirely from claude's own `result` line on the
  headless stream path. The reader answers *which keys the line carried*
  instead. Ordering is the whole difficulty: a realistic trailer's
  `result` field pushes the line past the cap, so a reader fed the capped
  `Line` returns zero names where the full line returns eleven — proved
  by a red (`TestTrailKeyNamesReadsTheFullLine`'s padded row), not a
  comment. Containment is structural rather than disciplined: the
  signature is `[]byte` in, `[]string` out, no `error` — the decode's
  failure arm returns `nil` and renders no part of the error, because
  `json.SyntaxError`/`json.UnmarshalTypeError` both carry attacker-chosen
  bytes no fixture needle could catch — and the intermediate
  `map[string]json.RawMessage` is discarded inside the function, never
  returned or formatted. `resultTrailer` is unwidened; a reachability
  test bans `map[string]json.RawMessage` from `trailScanResult` (**the
  map type, never `json.RawMessage` itself** — that element type is
  already reachable via `Trailer.PermissionDenials *[]json.RawMessage`,
  so banning it would be red against correct shipped code; the map ban is
  green today and the walk's own control proves it still walks). A
  five-needle containment test is deliberately scoped to `KeyNames`
  alone rather than the neighbouring whole-record-marshal idiom
  (`TestTrailScan`'s padded sub-test) — that idiom would go red against a
  *correct* build here, since `Subtype`/`StopReason`/`TerminalReason` are
  carried through `Trailer` by design and published verbatim downstream;
  bounding that publication surface is #1362's. Key names themselves were
  not bounded at this tier — `trailScanResult` is published by nothing, so
  there was no rendering surface here to bound — and the per-name cap
  landed one tier up instead, at `finSighting`/`finTrailerRecord`
  (`finBoundKeyNames`, #1363). Purely additive, one new 435-line
  file, zero production files touched, two-line extension of #1266's
  file (the field plus the wiring); 0 SKIP on `-run '^TestTrail'`. First
  code-review pass FAILed on an incomplete inbound-citation sweep (bare
  and chained pointers into #1266's file, which this ticket's field
  addition shifted) rather than on the implementation; the implementation
  itself passed clean on both rounds. See
  [`codebase/1357.md`](../codebase/1357.md) and, for the field's carriage
  onto both publishing tiers and its bounds, [`codebase/1363.md`](../codebase/1363.md).
