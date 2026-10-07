# Package-local source citation check (#1424)

## Files read

- `CODING-STYLE.md` → “Comments — Citing Other Code”: symbols and enclosing-symbol descriptions replace numeric references.
- `docs/knowledge/features/e2e-realclaude.md` → “Build tag” and “Test infrastructure”: targeted tagged tests can be offline; the entire tagged suite is not.
- `docs/knowledge/features/development-verification.md` → “Check searches and citations” and “Test execution and artifact survival”: verify claims against implementations and count executed tests.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `TestFinOfflineFilesReachNoExecHelper`: package-local AST source-check analogue; parsing failures are visible.
- `internal/e2e/realclaude/interactive_background_idle_probe_test.go` → `bgIdleStructuralArgument`, `bgIdleRunnerAttribution`: strings carry stale interactive-path citations.
- `internal/e2e/realclaude/trailer_admissibility_test.go` → `trailGate`, `trailAdmitAttribution`, `TestTrailGate`: bounded explanations and assertion markers must change together.
- `internal/e2e/realclaude/trail_run_outcome_test.go` → `trailClassifyRun`, `trailRunCases`: historical reap ordering and the six scan-produced gate rows.
- `internal/e2e/realclaude` → remaining filename-citing explanatory strings in probe, capture, trailer and finding tests: replace references without changing classifications or artifact fields.
- `internal/agentrun/reap.go` → `ReapDescendantGroups`: exclusions, successful-kill append and guarded reap log.
- `cmd/pyry/agent_run.go` → `runAgentRun`: signal context and clean/cancelled exit contract; terminal path was deleted.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.HandleFor`, `startTurnIfNeeded`: turn end guard and per-conversation turn start.
- Current streamrunner source and historical deleted terminal runner/emitter: verify pass-through, termination, trailer and reap claims by symbols and their enclosing branches.

## Context

Numeric source references survive in explanatory strings outside the diff-scoped
comment guard. This package needs one persistent offline check, plus conversion
of its existing stock. No production change or general symbol resolver is needed.
No other fetched feature branch touches the existing citation-bearing files.

Sizing: one deliverable, four acceptance criteria, approximately 550–650 written
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
