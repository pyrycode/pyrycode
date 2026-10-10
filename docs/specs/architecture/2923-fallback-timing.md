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

Completed documentation stage:

- Updated [the stream supervision overview](../../knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md), “One isolated Haiku attempt”, with the 29.8-second deadline, 30-second total lookup/termination bound, successful completion eligibility and separate result/exit timing witnesses.
- Updated [the live test infrastructure overview](../../knowledge/features/e2e-realclaude-test-infrastructure.md), “A suggestion frame alone cannot prove its source”, with the 35-second live window, deterministic correction and counted six-run proof.
- Both overviews retain historical failed batches, distinguish corrected deterministic behavior from live non-reproduction and observation alone, and preserve the daemon-only stderr privacy contract.

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

## Six-run declaration

Declared 2026-10-10 after corrected production/instrumentation commit `5b68a14577ab2ad2a6f8f1825944aaf51c914704` and green builder-owned offline checks. The commit containing this declaration will be pushed and its exact hash announced before launch 1. It is the unchanged checkout for exactly six sequential authenticated launches numbered 1–6 of:

`python3 /work/Projects/pyrycode-agents/dispatcher/scripts/live-claude-gate.py go --tests '^TestInteractiveStream_FallbackReplySuggestionSetThenClear$'`

No retries, replacements, checkout changes or edits during the batch, regardless of failure, skip or zero execution. Each invocation retains one completed exchange, native disabled on the persistent child, a bounded nonempty fallback for `suggestConvID`, then an explicit-null clear at a higher revision after accepted input. Record E/P/F/S and launcher outcome for every invocation, plus source/lifecycle scalar observations selected by wrapper PID and wire revisions. Source category is reported metadata, never authentication proof; the launcher's preflight separately checks authenticated login. Six executed passes are required, and a skipped or retried pass is not acceptance. Historical failed batches remain unchanged. The dispatcher separately owns the full live gate.

## Six-run results

Exactly six sequential authenticated launcher invocations used unchanged pushed declaration commit `1918d75612d61bc05fde9086da0af271136df24a`. The tree remained clean at that hash through launch 6. All preflight checks and launcher exits succeeded. No retries, replacements, skips or checkout changes. Aggregate E/P/F/S = **6/6/0/0**. Each launch executed the named test once, with one completed exchange, disabled persistent-child native suggestions, one successful isolated fallback, a bounded nonempty reply for `suggestConvID`, and explicit-null clear revision 2 above set revision 1 after accepted input.

| Run | Executed | Passed | Failed | Skipped | Launcher exit | Wrapper / child PID | Attempt ms | Approx. result receipt ms | Set / clear revisions |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | 1 | 1 | 0 | 0 | 0 | 88381 / 88382 | 11506 | 10971 | 1 / 2 |
| 2 | 1 | 1 | 0 | 0 | 0 | 89414 / 89417 | 8288 | 7831 | 1 / 2 |
| 3 | 1 | 1 | 0 | 0 | 0 | 89832 / 89833 | 6628 | 6146 | 1 / 2 |
| 4 | 1 | 1 | 0 | 0 | 0 | 90736 / 90737 | 8982 | 8501 | 1 / 2 |
| 5 | 1 | 1 | 0 | 0 | 0 | 91323 / 91324 | 11598 | 11101 | 1 / 2 |
| 6 | 1 | 1 | 0 | 0 | 0 | 91724 / 91725 | 9136 | 8618 | 1 / 2 |

Result receipt time is approximate, derived from the daemon's attempt time minus recognized result age. Runs 1 and 5 recognized valid results after the old 9800-ms cutoff and completed successfully under the corrected bound. Every result stayed within the 4096-byte envelope limit despite whole-stream saturation at 4097; Wait was observed and successful, with no deadline or group cancellation.

The correction is established by deterministic subprocess failures on the original deadline and passes after the production change. This fixed six-run batch is counted live proof/non-reproduction on the corrected commit, not a claim that earlier failed batches passed or that observation alone fixed anything. [#3024's 6/2/4/0 batch](https://github.com/pyrycode/pyrycode/blob/main/docs/specs/architecture/3024-fallback-stream-observation.md#six-run-results) and [PR #2896](https://github.com/pyrycode/pyrycode/pull/2896) remain untouched. [The code-traced cause and correction were posted on #2923](https://github.com/pyrycode/pyrycode/issues/2923#issuecomment-6096487983).

Safe evidence below contains only source/lifecycle scalar observations, correlated by wrapper PID. Source `none` is reported metadata, not proof of authenticated readiness; authentication was checked separately by each launcher. Zero decoded retry events differs from missing retry metadata. Stderr EOF/reader flags are safe; no daemon-only tail, generated text, prompt, credential, raw stdout or raw error is retained here. Documentation and the dispatcher's separate full-module/live gates remain pending.

Run 1:

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=1 pid=88381 elapsed_ms=11474 exit=0 output={bytes=4097 utf8=true json=true result_success=true text_valid=true result_bytes=1840} set_revision=1 clear_revision=2 stage=wire set observed child_pid=88382 spawn=true read={bytes=895 saturated=false} forward={bytes=895 saturated=false}
daemon lifecycle (cause not inferred): pid=88381 attempt_ms=11506 child_ms=11505 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=false own_deadline_elapsed=false group_cancel_requested=false wait_completed=true exit_observed=true exit_code=0 exit_signal=0 output_observed=true wait_ok=true wait_delay=false stdout_bytes=4097 stdout_cap_exceeded=true stdout_utf8_ok=true stdout_json_ok=true stdout_result_ok=true stdout_text_ok=true result_bytes=1840 progress_event=result progress_age_ms=535 progress_init=true progress_source=none progress_retries=0 cancel_event=unknown cancel_age_ms=unknown cancel_init=unknown cancel_source=unknown cancel_retries=unknown stderr_observed=true stderr_reader_done=true stderr_eof=true stderr_partial=false
```

Run 2:

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=1 pid=89414 elapsed_ms=8235 exit=0 output={bytes=4097 utf8=true json=true result_success=true text_valid=true result_bytes=1830} set_revision=1 clear_revision=2 stage=wire set observed child_pid=89417 spawn=true read={bytes=895 saturated=false} forward={bytes=895 saturated=false}
daemon lifecycle (cause not inferred): pid=89414 attempt_ms=8288 child_ms=8287 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=false own_deadline_elapsed=false group_cancel_requested=false wait_completed=true exit_observed=true exit_code=0 exit_signal=0 output_observed=true wait_ok=true wait_delay=false stdout_bytes=4097 stdout_cap_exceeded=true stdout_utf8_ok=true stdout_json_ok=true stdout_result_ok=true stdout_text_ok=true result_bytes=1830 progress_event=result progress_age_ms=457 progress_init=true progress_source=none progress_retries=0 cancel_event=unknown cancel_age_ms=unknown cancel_init=unknown cancel_source=unknown cancel_retries=unknown stderr_observed=true stderr_reader_done=true stderr_eof=true stderr_partial=false
```

Run 3:

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=1 pid=89832 elapsed_ms=6594 exit=0 output={bytes=4097 utf8=true json=true result_success=true text_valid=true result_bytes=1883} set_revision=1 clear_revision=2 stage=wire set observed child_pid=89833 spawn=true read={bytes=895 saturated=false} forward={bytes=895 saturated=false}
daemon lifecycle (cause not inferred): pid=89832 attempt_ms=6628 child_ms=6627 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=false own_deadline_elapsed=false group_cancel_requested=false wait_completed=true exit_observed=true exit_code=0 exit_signal=0 output_observed=true wait_ok=true wait_delay=false stdout_bytes=4097 stdout_cap_exceeded=true stdout_utf8_ok=true stdout_json_ok=true stdout_result_ok=true stdout_text_ok=true result_bytes=1883 progress_event=result progress_age_ms=482 progress_init=true progress_source=none progress_retries=0 cancel_event=unknown cancel_age_ms=unknown cancel_init=unknown cancel_source=unknown cancel_retries=unknown stderr_observed=true stderr_reader_done=true stderr_eof=true stderr_partial=false
```

Run 4:

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=1 pid=90736 elapsed_ms=8948 exit=0 output={bytes=4097 utf8=true json=true result_success=true text_valid=true result_bytes=1814} set_revision=1 clear_revision=2 stage=wire set observed child_pid=90737 spawn=true read={bytes=895 saturated=false} forward={bytes=895 saturated=false}
daemon lifecycle (cause not inferred): pid=90736 attempt_ms=8982 child_ms=8981 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=false own_deadline_elapsed=false group_cancel_requested=false wait_completed=true exit_observed=true exit_code=0 exit_signal=0 output_observed=true wait_ok=true wait_delay=false stdout_bytes=4097 stdout_cap_exceeded=true stdout_utf8_ok=true stdout_json_ok=true stdout_result_ok=true stdout_text_ok=true result_bytes=1814 progress_event=result progress_age_ms=481 progress_init=true progress_source=none progress_retries=0 cancel_event=unknown cancel_age_ms=unknown cancel_init=unknown cancel_source=unknown cancel_retries=unknown stderr_observed=true stderr_reader_done=true stderr_eof=true stderr_partial=false
```

Run 5:

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=1 pid=91323 elapsed_ms=11563 exit=0 output={bytes=4097 utf8=true json=true result_success=true text_valid=true result_bytes=1871} set_revision=1 clear_revision=2 stage=wire set observed child_pid=91324 spawn=true read={bytes=895 saturated=false} forward={bytes=895 saturated=false}
daemon lifecycle (cause not inferred): pid=91323 attempt_ms=11598 child_ms=11597 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=false own_deadline_elapsed=false group_cancel_requested=false wait_completed=true exit_observed=true exit_code=0 exit_signal=0 output_observed=true wait_ok=true wait_delay=false stdout_bytes=4097 stdout_cap_exceeded=true stdout_utf8_ok=true stdout_json_ok=true stdout_result_ok=true stdout_text_ok=true result_bytes=1871 progress_event=result progress_age_ms=497 progress_init=true progress_source=none progress_retries=0 cancel_event=unknown cancel_age_ms=unknown cancel_init=unknown cancel_source=unknown cancel_retries=unknown stderr_observed=true stderr_reader_done=true stderr_eof=true stderr_partial=false
```

Run 6:

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=1 pid=91724 elapsed_ms=9103 exit=0 output={bytes=4097 utf8=true json=true result_success=true text_valid=true result_bytes=1871} set_revision=1 clear_revision=2 stage=wire set observed child_pid=91725 spawn=true read={bytes=895 saturated=false} forward={bytes=895 saturated=false}
daemon lifecycle (cause not inferred): pid=91724 attempt_ms=9136 child_ms=9135 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=false own_deadline_elapsed=false group_cancel_requested=false wait_completed=true exit_observed=true exit_code=0 exit_signal=0 output_observed=true wait_ok=true wait_delay=false stdout_bytes=4097 stdout_cap_exceeded=true stdout_utf8_ok=true stdout_json_ok=true stdout_result_ok=true stdout_text_ok=true result_bytes=1871 progress_event=result progress_age_ms=518 progress_init=true progress_source=none progress_retries=0 cancel_event=unknown cancel_age_ms=unknown cancel_init=unknown cancel_source=unknown cancel_retries=unknown stderr_observed=true stderr_reader_done=true stderr_eof=true stderr_partial=false
```
