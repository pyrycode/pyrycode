# Reliable held-task identification (#2848)

## Files read

- `internal/e2e/realclaude/stop_background_task_completion_test.go` → `TestRealClaudeStopBackgroundTaskCompletion`: roster predicate, held FIFO and both completion signals.
- `internal/e2e/realclaude/interactive_stream_running_turn_test.go` → `startStreamRunningTurnHarness`: isolated child, bound conversation, authenticated daemon.
- `internal/e2e/realclaude/roster_after_finish_capture_test.go` → `rafcapPrompt`: explicit background request.
- `internal/e2e/realclaude/tool_progress_capture_test.go` → `tpcapHoldFIFO`: rendezvous and bounded, idempotent cleanup.
- `internal/e2e/realclaude/codex_conversation_live_test.go` → `receiveEnvelope`: ordered Noise decoding and bounded reads.
- `internal/protocol/interactive.go` → `ToolUsePayload`, `BackgroundTaskStartedPayload`, `BackgroundTask`: command input and task/tool correlation.
- `docs/knowledge/features/e2e-realclaude-roster-after-finish-capture-test-go.md` → “Stop completion needs a held task and all terminal signals” and “task_started is not the backgrounding signal”: retain roster proof and both terminal signals.
- `docs/knowledge/features/e2e-realclaude.md` and `development-verification.md`: tagged compilation, non-vacuous regression and live counts.

## Context

The #2831 full gate timed out in setup after 120.68 seconds; the same-tree targeted rerun passed in 7.62 seconds through roster omission while held. Neither log recorded rejected candidates or FIFO arrival. They establish a setup failure, not its precise cause or a stop product failure. The full-path description predicate introduced by `7c3f563d` is an investigation lead. No decision record is needed.

## Design

Instrument the existing setup drain with bounded redacted counters and booleans: bound rosters/rows, valid identifiers, type and description matches, observed tool command/background flag, and FIFO arrival. Run the original predicate live to distinguish staging, task identification and product failure. Record evidence on the issue.

If description matching rejects a correctly staged task, identify the exact `cat <rig FIFO>` Bash tool invocation from `ToolUsePayload.Input`, correlate its tool id to a task start or enriched roster, and still require the matching untruncated `local_bash` task in a bound roster before stopping. Descriptions are diagnostics, not command identity. Do not treat a task start alone as backgrounding. If evidence instead requires another design, append a revision before implementation; an upstream dependency uses the documented handback.

Keep the writer held through completion. Accept only the matching stopped update or a subsequent bound roster omitting the held id. Setup, rendezvous and completion keep their existing budgets; no retries or skips. Keep edits within this test family, without production changes unless evidence warrants a minimal revision.

One deliverable; forecast 250–400 written lines, zero exported types, zero consumer migrations, four acceptance criteria and fewer than ten reject branches. No concurrent feature branch touches the target test.

## Concurrency model

The test goroutine owns ordered receives and observations. Reuse `tpcapHoldFIFO`'s writer goroutine, rendezvous channel and idempotent bounded release; no new goroutines.

## Error handling

Setup failures report counters and match booleans, never payloads, commands, paths or identifiers. Invalid decoded target frames fail explicitly. Stop errors and missing terminal evidence remain failures.

## Testing strategy

Write table-driven hermetic scenarios for exact command/task correlation, unrelated conversations/tasks, missing or truncated ids, roster versus start-only evidence and summarized descriptions. Show the diagnosed scenario fails with the original predicate before changing it. Run scoped race tests, tagged package vet/compile, `go vet ./...` and `go build` with output outside the worktree. Run five consecutive non-skipped targeted executions using the approved launcher, with completion signals and counts. The dispatcher owns the full live suite.

## Open questions

- Resolve the setup cause from redacted live observations before selecting the final implementation.

## Documentation handoff

- Pending documentation stage: update `docs/knowledge/features/e2e-realclaude-roster-after-finish-capture-test-go.md` § “Stop completion needs a held task and all terminal signals” with the diagnosed cause and final identity proof.
- Pending dispatcher: full `make e2e-realclaude` gate containing this test; record executed/pass/fail/skip counts and its completion signal. Targeted validation is recorded separately.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: child-authored descriptions cannot establish task identity; compare rig-owned command and bound conversation and reject truncated join keys.
- Tokens and logging: SHOULD FIX in the setup drain: log only counters and match booleans; never serialize candidate payloads, input, descriptions or ids. Use the credential-redacting live launcher.
- File operations: reuse `tpcapHoldFIFO`'s 0600 FIFO inside the isolated test workdir. No arbitrary file reads or writes are added.
- Subprocesses: retain the existing authenticated harness and fixed rig-owned `cat` command; no new shell or credential access.
- Cryptography: reuse `receiveEnvelope` and `sealEnvelope`; ordered Noise nonces stay under one goroutine.
- Network/I/O: reuse bounded fakephone receives and existing frame caps; deadlines remain fixed.
- Concurrency: no new goroutines or shared maps; existing FIFO release executes on success and failure before daemon cleanup.
- Threat model: this change improves test attribution within an isolated harness; production relay authentication, routing and cryptography remain unchanged.

**Reviewer:** builder (self-review)
**Date:** 2026-10-05

## Revisions

- 2026-10-05: resolved the cause with the original predicate plus redacted diagnostics. In `/tmp/builder-2848/diagnosis-3.log`, exact background Bash input was observed at 16:58:49.347Z; the roster had an untruncated local_bash id but no description path match at 16:58:49.408Z; task start and FIFO arrival followed at 16:58:49.411Z. Setup failed at 17:00:42.024Z without a stop request. The preceding two runs matched the description and completed via roster omission. This is task-identification failure, not failed staging or evidence of a stop product bug. Preserve the latest bound roster because it can precede the task start; join the start's untruncated tool/task ids to the exact Bash input. The start alone still cannot pass. The open question is resolved; no upstream dependency or production change is needed.
