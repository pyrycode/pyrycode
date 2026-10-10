# #2923: give the isolated reply fallback time to complete

## Files read

- `cmd/pyry/reply_fallback.go` → `replyFallback.run`, `replyFallbackStream`, `replyFallbackOutput`, `replyFallbackStderrCapture.finish`: deadline, Wait eligibility, bounded parsing and daemon-only stderr.
- `cmd/pyry/reply_fallback_test.go` → `TestReplyFallbackHelperProcess`, `TestReplyFallbackProcessEvidence`, `TestReplyFallbackLifecycle`: subprocess fixtures, cancellation and publication guards.
- `cmd/pyry/reply_fallback_cancellation_test.go` → `testStartReplyFallback`: readiness, held Wait and cleanup independent of return-time evidence.
- `cmd/pyry/reply_suggestion.go` → `startFallbackLocked`, `turnEnded`: one attempt after two-second native priority, session/generation/cancellation guards.
- `internal/e2e/realclaude/interactive_stream_reply_suggestion_test.go` → `TestInteractiveStream_FallbackReplySuggestionSetThenClear`, `installSuggestCLI`, `suggestLifecycle`: strict one-exchange source proof and safe PID correlation.
- `internal/e2e/realclaude/reply_suggestion_staging_test.go` → lifecycle/privacy gates: preserve historical synthetic observations.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md` § “One isolated Haiku attempt”: preserve configured-account isolation and successful completion requirement.
- `docs/knowledge/features/e2e-realclaude-test-infrastructure.md` § “A suggestion frame alone cannot prove its source”: wire set/explicit-null clear and observation/authentication distinction.
- `docs/knowledge/features/streamsup-package.md`, `docs/knowledge/features/e2e-realclaude.md`, `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`: package map and verification conventions.
- `docs/specs/architecture/3024-fallback-stream-observation.md` § “Six-run results”: retained deadline/result/Wait evidence. QMD lexical search found the isolated-attempt and cancellation plans; slow MCP query was replaced by the authorized CLI search.

## Context

The code-traced cause is the 9.8-second deadline covering inference AND successful CLI completion. `run` requires successful Wait receipt and a still-live context, correctly refusing a result from a killed process. [#3024 PID-correlated evidence](https://github.com/pyrycode/pyrycode/blob/main/docs/specs/architecture/3024-fallback-stream-observation.md#six-run-results) records run 1 wrapper PID 1617213: attempt 9804 ms, result recognized approximately 9323 ms, frozen result age 478 ms, deadline/group kill, Wait receipt and SIGKILL, no wire set. Runs 2/4/6 (PIDs 1617488/1618060/1618594) reached the deadline with init but no decoded result. Preserve [PR #2896](https://github.com/pyrycode/pyrycode/pull/2896) and every historical failed batch. Increasing only the phone wait cannot fix the production cutoff.

One deliverable: correct the bounded attempt timing and prove it. Forecast: under 500 written lines including six-run evidence, zero exported types, two timing consumers, four criteria, no new reject branches. The finished plan meets all five limits. Retained branch #2882 overlaps the same files but its older observation changes are already superseded on main; no needed dependency and edits remain local. No new architectural decision record is needed.

## Design

Define a finite 30-second total bound and a 29.8-second attempt deadline, started before credential lookup. The remaining 200 ms reserves the existing 100-ms cancellation Wait grace/pipe cleanup; stderr cancellation adds no drain grace, and successful stderr drain is capped at 100 ms. Preserve group SIGKILL, WaitDelay, cleanup and generic unavailability. A valid result is eligible only after successful process completion before the new context expires. Parent cancellation or an earlier deadline remains authoritative.

Use the same attempt constant for `own_deadline_elapsed`. The fallback live window becomes 35 seconds after idle: two seconds of native priority, up to 30 seconds for the attempt including termination, three seconds transport/scheduling margin. Keep native probe timing independent. CLI flags, one Haiku invocation, account selection, UTF-8/result/text validation, native priority and conversation/session/generation guards remain unchanged.

## Concurrency model

No new production goroutine or lock. Existing account lookup selects against context cancellation; Wait uses a buffered completion channel and bounded cancellation grace, with exec closing held pipes after WaitDelay. Stderr reader joins after EOF or forced pipe close. Tests reuse the subprocess/readiness/Wait cleanup helper and join workers independently of the return snapshot.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Valid result before old deadline, successful exit after it | `TestReplyFallbackTimingBudget/result before old deadline` |
| First valid result after old deadline, successful exit | `TestReplyFallbackTimingBudget/result after old deadline` |
| Hung child and held Wait reach own deadline; cleanup joins after release | `TestReplyFallbackProcessEvidence/own deadline` |
| Lookup never yields until own deadline; no child launch | `TestReplyFallbackLookupBudget` |
| Earlier parent cancel/deadline, escaped/in-group descendants, held Wait | `TestReplyFallbackProcessCancellation`, `TestReplyFallbackCancellationHeldWait`, existing cancellation family |
| Native wins, accepted input/new generation, changed session, deletion and shutdown invalidate work | `TestReplyFallbackLifecycle`, `TestReplyFallbackRegistryRemovalCancellation` |

## Error handling

No new failure modes: invalid result, unsuccessful process, timeout, canceled or stale work remains unavailable and publishes nothing. The deadline expansion never rescues a failed exit or bypasses cancellation. Lifecycle evidence uses fixed labels/scalars; generated text, prompts, raw stdout, credentials and raw errors remain excluded. Bounded stderr tail remains daemon-local and excluded from control/wire/test evidence.

## Testing strategy

Add subprocess delay scenarios before production edits and observe both fail on the old deadline. Emit a result immediately then delay exit past 9.8 seconds; separately delay the first result beyond 9.8 seconds. Assert successful reply and PID-correlated result/Wait timing under the correction. Test lookup and hung-child total bounds, update the existing own-deadline timeout, and retain privacy/source observers. Run touched command-package race tests, tagged offline suggestion/cancellation/privacy tests, `go vet ./...`, tagged live-package vet and `go build ./cmd/pyry` with output in scratch. Verifier owns full-module suite.

After implementation/instrumentation and checks are fixed, commit and push a declaration of exactly six sequential authenticated targeted launches through the approved live launcher, identifying the unchanged checkout commit before launch 1. No retry, replacement or checkout change. Durably record E/P/F/S for each launch and aggregate plus safe PID timing/source evidence and wire revisions in this spec and PR. Six executed passes are required for this ticket; observation alone is not a correction. Dispatcher owns the separate full live gate.

## Open questions

None. Thirty seconds is the ticket's permitted upper bound and gives inference/completion about three times the previous budget without adding retries or changing eligibility.

## Documentation handoff

Pending documentation stage:
- Update `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md`, “One isolated Haiku attempt”, with the chosen attempt/termination bound and completion eligibility.
- Update `docs/knowledge/features/e2e-realclaude-test-infrastructure.md`, “A suggestion frame alone cannot prove its source”, with the aligned live window, correction and counted proof.
- Retain historical failed batches and the distinction between corrected deterministic behavior, live non-reproduction and observation alone; preserve the daemon-only stderr privacy contract.

## Security review

**Verdict:** PASS

**Findings:**
- Trust boundaries: `replyFallbackStream` keeps the 4096-byte UTF-8/JSON envelope gate; `validReplyFallback` and successful Wait still control eligibility.
- Credentials: `run` replaces ambient credentials with the selected provider token; no new credential reader or diagnostics. Lookup remains inside the attempt deadline.
- Files: existing private temporary cwd and bounded 0600 observer metadata are unchanged; no externally supplied path is added.
- Subprocesses: fixed argv/JSON stdin, scrubbed environment, one Haiku attempt and bounded process-group SIGKILL/Wait cleanup remain. Longer finite inference time is the intended resource-budget change.
- Cryptography: no cryptographic changes; authenticated Noise transport is reused.
- Network/I/O: no network handling changes; stdout envelope/receipt and stderr storage remain bounded independently of time.
- Diagnostics: fixed lifecycle timing and predicate fields only; daemon-only stderr tail cannot enter the counted test evidence or control/wire output.
- Concurrency: no added production workers; parent cancellation still wins before return/publication, and tests join child/Wait cleanup separately.
- Threat model: child text stays untrusted, native/source observations never establish authentication. Publication/thread-delivery redesign is outside this ticket, with no newly discovered defect requiring deferral.

**Reviewer:** builder (self-review per the security-review checklist). **Date:** 2026-10-10.

## Revisions

2026-10-10: Both new subprocess regressions failed at approximately 9.81 seconds on the unchanged production deadline before implementation. The existing held-Wait race witness is `TestReplyFallbackCancellationEvidence` (the planning table used an incorrect shorthand). The production correction changes only the named budget/deadline and its lifecycle threshold; the live fallback wait is 35 seconds. Hung-child lifecycle assertions now check the 29.8-second deadline and return before 30 seconds.

## Offline results

Both delayed-result/completion regressions failed on the original 9.8-second production deadline (2 executed / 0 passed / 2 failed / 0 skipped). After correction, the complete touched `cmd/pyry` race suite passed (102.392 seconds), including both timing cases, 29.8-second lookup expiry, hung-child/held-Wait cleanup, existing cancellation, validation, lifecycle and privacy witnesses. Tagged offline `TestSuggest` race tests passed (27.103 seconds). `go vet ./...`, tagged live-package vet, binary build to scratch and `git diff --check` passed. Full-module hermetic and live gates remain dispatcher-owned.
