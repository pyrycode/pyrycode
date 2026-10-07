# Package-local source citation check (#1424)

## Files read

- `CODING-STYLE.md` → “Comments — Citing Other Code”: symbols and enclosing-symbol descriptions replace numeric references.
- `docs/knowledge/features/e2e-realclaude.md` → “Build tag” and “Test infrastructure”: targeted tagged tests can be offline; the entire tagged suite is not.
- `docs/knowledge/features/development-verification.md` → “Check searches and citations” and “Test execution and artifact survival”: verify claims against implementations and count executed tests.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `TestFinOfflineFilesReachNoExecHelper`: package-local AST source-check analogue; parsing failures are visible.
- `internal/e2e/realclaude/interactive_background_idle_probe_test.go` → `bgIdleStructuralArgument`, `bgIdleRunnerAttribution`: strings carry stale interactive-path citations.
- `internal/e2e/realclaude/trailer_admissibility_test.go` → `trailGate`, `trailAdmitAttribution`, `TestTrailGate`: bounded explanations and assertion markers must change together.
- `internal/e2e/realclaude/trail_run_outcome_test.go` → `trailClassifyRun`, `trailRunCases`: historical reap ordering and the six scan-produced gate rows.
- `internal/e2e/realclaude/background_reach_probe_test.go` → reap attribution notes: the reaper's exclusions support the process-group claim.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapSpawnShapeDelta`, `dropcapRedactionRationale`, `TestDropcapFixtureIsACapture`: captured prose equality must preserve historical payloads.
- `internal/e2e/realclaude/finding_exit_path_probe_test.go` → exit-path finding logs: distinguish exit code from trailer attribution.
- `internal/e2e/realclaude/finding_key_name_bounds_test.go` → `TestFinBoundKeyNamesAllocatesItsOwnBackingArray`: names are copied before `finTrailerBuild` shares its carrier slice.
- `internal/e2e/realclaude/finding_key_name_containment_test.go` → containment assertions: full-line and diagnostic references identify actual test symbols.
- `internal/e2e/realclaude/finding_run_record_test.go` → `TestFinRecordCarriesEveryMatchedRow`, `TestFinRecordEmbedsTrailerRecordWhole`: PID linkage and bounded struct diagnostics.
- `internal/e2e/realclaude/finding_stage_held_group_test.go` → `TestFinStageRigHardcodingsCannotReachTheFinding`: the rig's nil-stderr attribution leg.
- `internal/e2e/realclaude/finding_trailer_evidence_test.go` → `finTrailerBuild`, scalar tests: key-name assignment, observation bounds and historical field order.
- `internal/e2e/realclaude/long_session_test.go` → Scanner error diagnostic: refer to `parseResultTrailer`'s buffer guidance.
- `internal/e2e/realclaude/teardown_liveness_probe_test.go` → `tdnSeedNotes`: reaper success and shared runner call sites.
- `internal/e2e/realclaude/teardown_liveness_test.go` → `tdnClassifyReapLog`: exclusion and conditional-log explanations.
- `internal/e2e/realclaude/teardown_reap_capture_test.go` → exclusion diagnostics: process-group guards in `ReapDescendantGroups`.
- `internal/e2e/realclaude/trailer_key_names_test.go` → `TestTrailScanResultReachesNoRawMessageMap`: a raw map would leak under struct formatting.
- `internal/e2e/realclaude/trailer_terminal_reason_test.go` → reason/path explanations: streamrunner pass-through and historical emitter blank-reason guard.
- `internal/e2e/realclaude/result_trailer_observation_test.go` → `trailWaitForTrailer`: aborted and deadline arms carry no bound.
- `internal/e2e/realclaude/tool_loop_test.go` → `parseResultTrailer`: Scanner buffer guidance.
- `internal/e2e/realclaude/process_pin_liveness_test.go` → `pinStateOutcome`: PID and PPID provenance.
- `internal/e2e/realclaude/trail_run_rig_test.go` → `trailRigGather`: nil stderr is structural, not a caller parameter.
- `internal/agentrun/reap.go` → `ReapDescendantGroups`: exclusions, successful-kill append and guarded reap log.
- `cmd/pyry/agent_run.go` → `runAgentRun`: signal context and clean/cancelled exit contract; terminal path was deleted.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.HandleFor`, `startTurnIfNeeded`: turn end guard and per-conversation turn start.
- Current streamrunner source and historical deleted terminal runner/emitter: verify pass-through, termination, trailer and reap claims by symbols and their enclosing branches.

## Context

Numeric source references survive in explanatory strings outside the diff-scoped
comment guard. This package needs one persistent offline check, plus conversion
of its existing stock. No production change or general symbol resolver is needed.
No other fetched feature branch touches the existing citation-bearing files.

Sizing: one deliverable, four acceptance criteria, approximately 500–650 written
lines including plan, zero exported types/interfaces, zero production consumers,
and fewer than ten checker failure branches. This fits the 800-line ceiling.

## Design

Add `source_citation_check_test.go` with the existing `e2e_realclaude` tag.
A directory-injected scanner discovers every direct `.go` child, reads and parses
it with comments enabled, and inspects comments plus decoded string literals.
Text matching rejects numeric Go filename references regardless of target/range
validity, standalone bare references and TitleCase/lowerCamelCase-qualified
references, including comma continuations. Report each finding with the citing
source position and offending text; ordinary data stays accepted.
Only string fixtures within specifically named regression-test declarations in
this checker's own file are exempt. Comments and all other declarations remain
covered, including future declarations added to the checker file.
Convert remaining references after checking the claim in current or historical
source. Deleted terminal runner/emitter references explicitly say historical.
Keep classifications, record shapes and detail caps; adjust textual assertion
markers alongside the corresponding explanation. Correct four to six in the
`trailRunCases` comment describing `TestTrailGate`'s scan-produced rows.

## Concurrency model

No goroutines or external processes in the check. Each scan uses its own parser
state and injected directory; temporary-fixture regressions are independent.

## Error handling

Directory discovery, reading and parsing errors fail visibly with file context.
Citation detection never opens the cited target. Findings accumulate so multiple
references are reported together rather than silently stopping at the first.

## Testing strategy

Write regressions first and observe failure before implementing the scanner.
Table-driven synthetic sources cover line/block comments, interpreted/raw strings,
all rejection forms, invalid ranges, multiple references, and positive data.
Temporary directories prove newly added Go files are discovered, nested/non-Go
files are excluded, and unreadable or invalid sources fail visibly. Prove the
fixture exemption does not hide other checker-file declarations or comments.
Run the new check and affected offline tests with `-tags e2e_realclaude -race
-count=1`, reporting executed/passed counts. Run scoped tagged vet, `go vet ./...`
and `go build ./cmd/pyry` (output outside the worktree). The dispatcher owns
`make check`, including the full-module race suite; it does not execute this
new tagged test.

## Open questions

Resolve the exact symbols behind each remaining claim during conversion, including
deleted terminal implementation, without remapping stale numeric positions.

## Documentation handoff

Pending for the documentation stage: in
`docs/knowledge/features/e2e-realclaude.md`, add “Source-citation checks” beside
“Build tag”. State that the package-local offline test checks comments and string
literals for numeric Go-source citations, requires symbol references instead,
and runs without Claude or credentials. Include the actual targeted invocation
`go test -tags e2e_realclaude -race -count=1 -v -run '^TestSourceCitation' ./internal/e2e/realclaude`
and explicitly state that `make check` does not run it.

## Revisions

- 2026-10-07: Claim verification found the deleted implementation in the parent
  of commit `58524e9c`: `ptyrunner.Run` registers trailer close after reap, and
  its budget Terminate hook reaps before signalling; `streamjson.Emitter.Close`
  fills blank reasons and `streamjson.trailer` declares wire order. The remaining
  current claims resolve to `ReapDescendantGroups`, `streamrunner.Run`,
  `trailRigGather`, `trailWaitForTrailer`, `finTrailerBuild`, `pinStateOutcome`,
  `TestFinRecordEmbedsTrailerRecordWhole`, and
  `TestFinWriteArtifactPublishesNoVerbatimModelOutput`. This resolves the open
  symbol-identification question. Describe the pre-removal runner-selection
  argument explicitly as capture-time history.
- 2026-10-07: The full scan also sees Go slices quoted in comments. Exclude a
  colon following an identifier's opening slice bracket, while rejecting
  standalone bracketed citations. Recognize the standalone wildcard-port
  literal as data; a parenthesized zero-line reference remains forbidden.
- 2026-10-07: `TestDropcapFixtureIsACapture` compares historical captured prose
  against current constants. Converting two source citation labels made that
  equality fail without changing any captured payload. Normalize only those
  two parenthesized filename/position labels before comparison; every other
  prose byte stays exact, and committed capture contents remain untouched.
  This small comparator change stays in the existing test file.
