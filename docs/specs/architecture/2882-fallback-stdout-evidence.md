# #2882 — bounded post-Wait fallback stdout evidence

## Files read

- `cmd/pyry/reply_fallback.go` → `replyFallback.run`, `validReplyFallback`: execution, Wait and production decoding contract.
- `cmd/pyry/claude_account.go` → `cappedBuffer`: retains at most 4097 bytes, never total size.
- `cmd/pyry/reply_fallback_test.go` → `TestReplyFallbackProcessEvidence`, helper process: context, process-group and privacy checks.
- `internal/e2e/realclaude/interactive_stream_reply_suggestion_test.go` → `suggestLifecycle`, `suggestSource.diagnostic`, `readSuggestSource`, named fallback test: safe extraction and unchanged live proof.
- `internal/e2e/realclaude/reply_suggestion_staging_test.go` → `TestSuggest` checks: native isolation, private files, exact I/O, Go-compatible JSON semantics and forwarding synchronization.
- `docs/knowledge/features/e2e-realclaude.md` § “A suggestion frame alone cannot prove its source”: absent authenticated output fails; incomplete wrapper evidence remains unknown.
- `docs/knowledge/features/development-verification.md` § “Captures and live evidence”: explicit zero requires present typed metadata, evidence must survive scratch cleanup.
- `docs/specs/architecture/2873-fallback-source-evidence.md`: historical failures and lifecycle disposition retained without edits.
- `/work/Projects/pyrycode-agents/dispatcher/scripts/live-claude-gate.py` → `main`: restricted authenticated counted launches.

## Context

Historical run 4 at `d416eff6` witnessed the internal deadline with a live parent and wrapper SIGKILL after Wait, but no wrapper completion or real-child output. Add surviving daemon capture observations without claiming arrival timing, pipe EOF, child completion or publication eligibility. No decision record is needed. Codegraph context did not resolve these helpers; focused source reads supplied the map. QMD found the retained investigation and owning topic. No fetched feature branch overlaps the four target files.

Sizing: one evidence deliverable, approximately 400–550 written lines including plan, checks and results; zero exported types/interfaces, zero changed consumer signatures, four acceptance criteria, no new execution reject branches. Rechecked against this plan: all five limits hold.

## Design

Extend the existing deferred lifecycle record, before context cleanup. Only observed Wait allows reading `cappedBuffer` or process state. Preserve the received cancellation-branch Wait error and record safe Wait-success/WaitDelay predicates. Add output-observed, retained byte count, cap-exceeded, UTF-8, JSON-decode, result-success and bounded-text observations. Unobserved/inapplicable values use explicit unknown labels. Saturation at 4097 is a lower bound on received output. Share production JSON decoding semantics with observation; result/text predicates require successful decode. Text validity uses trimmed `validReplyFallback` independently of result success.

`suggestLifecycle` emits parsed fixed scalar fields correlated to the wrapper PID. Missing/malformed fields cannot invent zeros or predicates; Wait/output/decode gates control applicable observations. `readSuggestSource` requires present, correctly typed capture fields before considering output observed; the diagnostic preserves source/wire observations and unknown predicates. The named live test remains the existing real Haiku set/clear proof with one exchange and a 15-second window.

## Concurrency model

No new goroutine, lock or timer. Wait receipt synchronizes writer/process-state observation. Without receipt, neither buffer nor state is inspected. Existing atomic cancellation flag, group termination, bounded Wait and credential lookup paths remain unchanged.

## Error handling

Keep generic unavailable returns and all policy/execution bounds. Fixed labels and numeric/boolean predicates only; never raw errors or child output. Decode failures are validation evidence, not invocation success. Malformed observer metadata remains unknown. Decodable post-Wait bytes prove neither EOF, real-child completion, pre-deadline arrival nor publication eligibility.

## Testing strategy

Extend existing process checks first: zero, incomplete/invalid, valid and saturated received capture, failure and cancellation. Check unknown Wait without buffer access. Extend observer cases for missing/partial/malformed metadata, explicit zero, decode gates and unobserved Wait. Preserve existing isolation, I/O, private-file, JSON null/duplicate semantics and forwarding synchronization tests. Run touched production race suite, tagged offline `TestSuggest` race checks, module/tagged vet and product build; verifier owns full-module gate.

Commit/push a declaration naming a fixed instrumentation commit before exactly six sequential authenticated launches of `^TestInteractiveStream_FallbackReplySuggestionSetThenClear$` through the restricted launcher. No retries or replacements. Retain each result path, commit, E/P/F/S, source, PID-correlated daemon/output and wire revisions here. Builder owns this batch; dispatcher owns full live gate. Failed proof returns for refinement with observed stage and evidence-backed follow-up; no execution/policy/fixture fix in this slice. Six passes mean “not reproduced in this bounded batch; historical failure cause remains unknown.” Skips, zero tests or interruption do not satisfy acceptance.

## Open questions

- What received-output validation accompanies each observed lifecycle stage? Resolve only from the fixed six-run batch; missing/simultaneous evidence remains ambiguous and underlying child state unknown.

## Documentation handoff

Pending documentation stage: `docs/knowledge/features/e2e-realclaude.md`, “A suggestion frame alone cannot prove its source”: extend with bounded post-Wait daemon stdout count and validation predicates. Distinguish unknown capture from zero received bytes, saturation from total size, JSON validity from EOF/child completion, and post-Wait observation from pre-deadline arrival. Record counted batch, observed failure stage, unchanged staging/window and uncertainty. Absence fails rather than skips; non-reproduction establishes no fix; timeout alone establishes no defect. Carry forward #2873/#2881 requirements on source/lifecycle PID correlation, counted historical batches and historical missing witnesses.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `replyFallback.run` reduces untrusted stdout to bounded numeric/boolean observations; observer parsing never echoes unknown strings. SHOULD FIX: require typed presence before treating missing source counts as zero capture.
- [Tokens] Existing credential lookup/inheritance unchanged. Neither observation nor logging reads credentials/environment; sentinel privacy checks cover accidental exposure.
- [File operations] Existing private 0600 source files and temporary directory remain unchanged. Trailing partial append is unknown. No new child path or capture file.
- [Subprocesses] Existing direct argv, isolation flags, process group, 9.8-second context and 100-ms Wait remain unchanged; no shell or stand-in in live proof.
- [Cryptography] No key, nonce, comparison or cryptographic operation changes.
- [Network and I/O] No new endpoint; retained stdout cap 4097 and source read cap 64 KiB remain. Validation never claims EOF.
- [Errors/logs] Only fixed labels, parsed numbers/booleans and unknown labels; no generated text, raw stdout/stderr/errors, argv, child paths, environment values or credentials.
- [Concurrency] Wait guard must precede every capture/process-state read; cancellation flag remains atomic. Existing goroutine shutdown unchanged.
- [Threat model] Content-free daemon subprocess diagnostics only; relay/mobile authorization unchanged. Execution/policy/fixture diagnosis beyond this evidence belongs to later refinement, as required by #2882.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-06

## Revisions

- 2026-10-06: Grounding also read `docs/knowledge/features/cli-verb-dispatch.md` → `runSupervisor` shutdown and `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md` § “One isolated Haiku attempt”: existing generic error/isolation/termination contract preserved. Implementation uses explicit unknown exit values when Wait/state is unobserved. No design, execution or live-fixture change.

## Fixed six-run batch declaration (2026-10-06)

Instrumentation is fixed at `238a04a8579a19e6d2760bf91e084ebd157c0fd4`. This declaration adds documentation only; its resulting pushed commit is the frozen checkout for **exactly six sequential authenticated launches**, numbered 1–6, of `^TestInteractiveStream_FallbackReplySuggestionSetThenClear$` using `/work/Projects/pyrycode-agents/dispatcher/scripts/live-claude-gate.py go --tests` from this product worktree. No code or checkout changes during the batch. Record that checkout commit for every run. Reports are `/tmp/builder-2882/bounded-six/run-1.log` through `run-6.log`. No retry or replacement, including failure, skip, zero tests or interruption. Complete all six; append content-free source, lifecycle/output and wire evidence here regardless of result.

Before declaration: production package race suite passed; final focused fallback race checks and tagged offline TestSuggest race checks passed; module vet, tagged vet and product build passed. Full-module verifier gate and dispatcher full live gate remain pending. No live launch has occurred in this run.

## Six-run results and refinement disposition (2026-10-06)

Exactly six sequential authenticated launches completed at pushed checkout `5ea02fb68bbd6465e14d03f6cdfe18a68b3af7d6`, with fixed instrumentation `238a04a8579a19e6d2760bf91e084ebd157c0fd4`. No retries, replacements, skips, zero-test launches or interruptions. The checkout stayed fixed and clean throughout. E/P/F/S is executed/passed/failed/skipped. Safe observations below survive scratch-file cleanup.

### Run 1

Commit: `5ea02fb68bbd6465e14d03f6cdfe18a68b3af7d6`. Result: `/tmp/builder-2882/bounded-six/run-1.log`. E/P/F/S: **1/1/0/0**.

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=1 pid=59843 elapsed_ms=8220 exit=0 output={bytes=1868 utf8=true json=true result_success=true text_valid=true} set_revision=1 clear_revision=2 stage=wire set observed
daemon lifecycle (cause not inferred): pid=59843 attempt_ms=8258 child_ms=8257 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=false own_deadline_elapsed=false group_cancel_requested=false wait_completed=true exit_observed=true exit_code=0 exit_signal=0 output_observed=true wait_ok=true wait_delay=false stdout_bytes=1868 stdout_cap_exceeded=false stdout_utf8_ok=true stdout_json_ok=true stdout_result_ok=true stdout_text_ok=true
reply_suggestion set: revision=1 length=109 session_id set=true
reply_suggestion clear: revision=2 session_id set=true
```

### Run 2

Commit: `5ea02fb68bbd6465e14d03f6cdfe18a68b3af7d6`. Result: `/tmp/builder-2882/bounded-six/run-2.log`. E/P/F/S: **1/0/1/0**.

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=0 pid=60137 elapsed_ms=12964 exit=unknown output={unknown} set_revision=0 clear_revision=0 stage=incomplete invocation (exit/output unknown)
daemon lifecycle (cause not inferred): pid=60137 attempt_ms=9801 child_ms=9800 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=true own_deadline_elapsed=true group_cancel_requested=true wait_completed=true exit_observed=true exit_code=-1 exit_signal=9 output_observed=true wait_ok=false wait_delay=false stdout_bytes=0 stdout_cap_exceeded=false stdout_utf8_ok=true stdout_json_ok=false stdout_result_ok=unknown stdout_text_ok=unknown
```

### Run 3

Commit: `5ea02fb68bbd6465e14d03f6cdfe18a68b3af7d6`. Result: `/tmp/builder-2882/bounded-six/run-3.log`. E/P/F/S: **1/1/0/0**.

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=1 pid=60948 elapsed_ms=6606 exit=0 output={bytes=1815 utf8=true json=true result_success=true text_valid=true} set_revision=1 clear_revision=2 stage=wire set observed
daemon lifecycle (cause not inferred): pid=60948 attempt_ms=6643 child_ms=6642 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=false own_deadline_elapsed=false group_cancel_requested=false wait_completed=true exit_observed=true exit_code=0 exit_signal=0 output_observed=true wait_ok=true wait_delay=false stdout_bytes=1815 stdout_cap_exceeded=false stdout_utf8_ok=true stdout_json_ok=true stdout_result_ok=true stdout_text_ok=true
reply_suggestion set: revision=1 length=55 session_id set=true
reply_suggestion clear: revision=2 session_id set=true
```

### Run 4

Commit: `5ea02fb68bbd6465e14d03f6cdfe18a68b3af7d6`. Result: `/tmp/builder-2882/bounded-six/run-4.log`. E/P/F/S: **1/1/0/0**.

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=1 pid=61574 elapsed_ms=8106 exit=0 output={bytes=1815 utf8=true json=true result_success=true text_valid=true} set_revision=1 clear_revision=2 stage=wire set observed
daemon lifecycle (cause not inferred): pid=61574 attempt_ms=8144 child_ms=8143 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=false own_deadline_elapsed=false group_cancel_requested=false wait_completed=true exit_observed=true exit_code=0 exit_signal=0 output_observed=true wait_ok=true wait_delay=false stdout_bytes=1815 stdout_cap_exceeded=false stdout_utf8_ok=true stdout_json_ok=true stdout_result_ok=true stdout_text_ok=true
reply_suggestion set: revision=1 length=55 session_id set=true
reply_suggestion clear: revision=2 session_id set=true
```

### Run 5

Commit: `5ea02fb68bbd6465e14d03f6cdfe18a68b3af7d6`. Result: `/tmp/builder-2882/bounded-six/run-5.log`. E/P/F/S: **1/0/1/0**.

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=0 pid=61890 elapsed_ms=12969 exit=unknown output={unknown} set_revision=0 clear_revision=0 stage=incomplete invocation (exit/output unknown)
daemon lifecycle (cause not inferred): pid=61890 attempt_ms=9801 child_ms=9800 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=true own_deadline_elapsed=true group_cancel_requested=true wait_completed=true exit_observed=true exit_code=-1 exit_signal=9 output_observed=true wait_ok=false wait_delay=false stdout_bytes=0 stdout_cap_exceeded=false stdout_utf8_ok=true stdout_json_ok=false stdout_result_ok=unknown stdout_text_ok=unknown
```

### Run 6

Commit: `5ea02fb68bbd6465e14d03f6cdfe18a68b3af7d6`. Result: `/tmp/builder-2882/bounded-six/run-6.log`. E/P/F/S: **1/1/0/0**.

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=1 pid=62248 elapsed_ms=6322 exit=0 output={bytes=1822 utf8=true json=true result_success=true text_valid=true} set_revision=1 clear_revision=2 stage=wire set observed
daemon lifecycle (cause not inferred): pid=62248 attempt_ms=6360 child_ms=6359 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=false own_deadline_elapsed=false group_cancel_requested=false wait_completed=true exit_observed=true exit_code=0 exit_signal=0 output_observed=true wait_ok=true wait_delay=false stdout_bytes=1822 stdout_cap_exceeded=false stdout_utf8_ok=true stdout_json_ok=true stdout_result_ok=true stdout_text_ok=true
reply_suggestion set: revision=1 length=62 session_id set=true
reply_suggestion clear: revision=2 session_id set=true
```

**Batch: 6 executed, 4 passed, 2 failed, 0 skipped.** Runs 2 and 5 remain FAIL. Passing runs observed nonempty bounded UTF-8 single-line replies for `suggestConvID`, wire set revision 1 and explicit-null clear revision 2 after accepted input; session ID was present. Their daemon retained counts matched the independently completed source observations. Source diagnostics also appeared at set/clear revisions 1/0 before clear and 1/2 after clear. Failed runs observed no wire set or clear, revisions 0/0.

**Lifecycle stage, independently of output validation:** runs 2 (PID 60137) and 5 (PID 61890) observed fallback/own deadline flags true with both parent flags false, group cancellation requested and wrapper Wait/SIGKILL (exit -1, signal 9). Attempt/child duration was 9801/9800 ms in both. Wrapper completion, real-child exit and real-child output remain unknown; elapsed source observations 12964/12969 ms are time since incomplete invocation start, not completed child durations. The daemon's process state belongs to the wrapper. No simultaneous context ambiguity appeared; simultaneous or missing observations remain ambiguous.

**Received-output validation:** both failed runs observed Wait and a bounded snapshot of exactly zero daemon-captured stdout bytes, cap-exceeded=false, UTF-8=true for the empty snapshot, JSON-decode=false and result/text predicates unknown. Wait-success=false and WaitDelay=false. Zero capture means no bytes received by the daemon, not proof the real child produced none. No usable received completion was observed. Successful snapshots decode and validate, but decodability proves neither pipe EOF, real-child completion, pre-deadline arrival nor publication eligibility. Post-Wait bytes cannot be attributed to arrival before the deadline; saturation would be a lower bound, not total size.

**Return for refinement:** strict current live proof failed twice; no resolution claim or gate waiver. Evidence-backed follow-up: plan a separate content-free witness distinguishing real-child startup/output receipt and wrapper forwarding from absent daemon receipt, using a real-child-started observation and bounded child-read/wrapper-forward counters that survive cancellation. Correlate them with the retained daemon PID/count; missing witnesses remain unknown. This can locate the zero-receipt boundary without assuming child inactivity, network/auth cause, publication rejection or pre-deadline arrival. Execution, credentials, arguments, deadlines, cancellation, fallback policy, native isolation, one-exchange staging, the 15-second window and #2859 release control remain unchanged. Execution/policy/fixture fixes remain outside this slice.

The investigation question is resolved only at the observed daemon receipt boundary: the reproduced stage has zero retained daemon bytes; the underlying child's state and cause remain unknown. Historical failures in the retained #2873 spec remain failures with their original uncertainty; this batch cannot recover their missing witnesses. Historical declared batch at `d416eff6` remains 6/5/1/0, all historical targeted PR runs 18/14/4/0, and the separate exact-base report remains 2/1/1/0. Non-reproduction in a later batch would establish no fix; timeout alone establishes no defect.

Checks passed: `go test -race ./cmd/pyry/...`; final focused `go test -race ./cmd/pyry -run '^TestReplyFallback'`; `go test -race -tags e2e_realclaude -count=1 -run '^TestSuggest' ./internal/e2e/realclaude/...`; `go vet ./...`; tagged live-package vet; `go build` of `cmd/pyry` into scratch. Full-module verifier gate and full dispatcher live gate remain pending; keep `needs-real-claude`. Documentation handoff remains pending at the exact path/section above and must include this counted batch and zero-receipt uncertainty.

## Revisions — surviving wrapper boundaries (2026-10-06)

Rework adds three append-only progress records in `installSuggestCLI`: after successful `Popen` before reading, after the first nonempty `os.read` before writing, and after the first successful write/flush. Each carries wrapper and real-child PID; read/forward records carry bounded prefix counts and explicit saturation at 4097. Forward counts use acknowledged writes after successful flush. Completion remains separate, with correlated PIDs and bounded capture. No execution, policy, credentials, argv, timing/window or fixture change; no production publication tracing. Historical failures above and in the #2873 spec remain failures.

`readSuggestSource` accepts complete typed records only, checks PID correlation and stage ordering, and leaves missing/null/malformed/partial/mismatched/contradictory progress unknown or ambiguous. Diagnostics expose independent start/read/forward witnesses alongside existing daemon lifecycle/output and wire revisions. Counts are witnessed prefixes, never totals; absent witnesses do not establish absent progress. Spawn does not establish readiness/authentication, read does not establish forwarding, and forwarding does not establish daemon receipt, EOF, child completion, pre-deadline arrival or publication eligibility.

Concurrency remains the existing wrapper/child and production process group. No new goroutine, subprocess or timer. Offline cancellation tests synchronize on persisted spawn/read/forward boundaries; a full output pipe blocks forwarding for the read boundary, and synchronized capture confirms forwarding independently. Existing exact I/O, cancellation, isolation, JSON semantics and privacy checks remain. Additional parser scenarios cover malformed/partial/PID mismatch, contradictions, bounded counts and the 64 KiB read bound.

Files read additionally: `internal/e2e/realclaude/reply_suggestion_staging_test.go` → `TestSuggestCLIFallbackIncomplete`, `installSuggestStandIn`: offline boundary synchronization; `installSuggestCLI`, `readSuggestSource`: private append and typed extraction. Prior Files read and owning-topic lessons still apply. Fetched feature branches have no overlap on the four implementation/test paths.

Sizing rechecked: one evidence deliverable, approximately 750–800 total written lines including retained changes, zero exported types/interfaces, zero consumer signature changes, four acceptance criteria, no execution reject branch changes. Open question: locate the failure boundary only from a separately declared fixed six-run batch; no inference about cause. Append declaration and results separately, with no retries or replacements; any failed proof returns for refinement.

### Security review of rework

Verdict: PASS (builder self-review, 2026-10-06). Trust boundary: typed scalar presence, PID/order/cap consistency in `readSuggestSource` must gate witnesses; contradictory records invalidate progress. Secrets: unchanged inheritance, fixed labels/numbers/booleans only, sentinel checks retained. Files/I/O: existing 0600 append-only metadata file, 64 KiB reader bound, no raw capture; interrupted append is unknown. Subprocesses/concurrency: unchanged group cancellation and shutdown, no sidecar, counters only after observed read or completed write/flush. Cryptography/network: no new operation or endpoint. Errors/logs: malformed source metadata must never echo content. Threat model: content-free evidence only; execution/policy/live-fixture diagnosis remains outside this slice. These are SHOULD FIX implementation checks, with no MUST FIX or new out-of-scope work.

### Documentation handoff of rework

Pending documentation stage: `docs/knowledge/features/e2e-realclaude.md`, “A suggestion frame alone cannot prove its source”: extend with bounded post-Wait daemon validation and independent spawn/read/forward observations. Distinguish unknown from zero, bounded prefix/saturation from total size, read from forwarding from daemon receipt, spawn from readiness, and post-Wait observation from pre-deadline arrival/EOF/child completion. Record both counted #2882 batches, failure stages, unchanged staging/window and remaining uncertainty. State that authenticated absence fails, non-reproduction establishes no fix and timeout alone establishes no defect. Preserve historical failures and prior handoff requirements.
