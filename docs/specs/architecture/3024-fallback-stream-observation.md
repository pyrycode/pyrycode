# #3024: bounded fallback stream observation

## Files read

- `cmd/pyry/reply_fallback.go` → `run`, `decodeReplyFallback`, `replyFallbackOutput`: isolation, deadlines and struct result semantics.
- `cmd/pyry/reply_fallback_test.go` → process helpers and output predicates: existing execution/privacy witnesses.
- `cmd/pyry/reply_suggestion.go` → `startFallbackLocked`: publication eligibility and generation guard remain intact.
- `internal/e2e/realclaude/interactive_stream_reply_suggestion_test.go` → `installSuggestCLI`, `readSuggestSource`, `suggestLifecycle`: independent readers and typed evidence gates.
- `internal/e2e/realclaude/reply_suggestion_staging_test.go` → wrapper staging/presence tests: surviving witnesses and exact forwarding.
- `docs/knowledge/features/e2e-realclaude.md` § “A suggestion frame alone cannot prove its source”: receipt does not establish publication or authentication.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md` § “One isolated Haiku attempt”: preserve process/account isolation and timeout.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: bounded evidence and race checks.

## Context

The fallback currently retains a single JSON document. Stream progress must not spend the independent 4096-byte final-result allowance. The retained #2882 branch overlaps these files but adds no needed dependency; do not merge it.
Historical evidence remains unresolved: [PR #2896](https://github.com/pyrycode/pyrycode/pull/2896) and its [complete spec](https://github.com/pyrycode/pyrycode/blob/25d778fbd0e54753bd82a6d4eca9a097c996f6f4/docs/specs/architecture/2882-fallback-stdout-evidence.md). E/P/F/S: earlier #2882 6/4/2/0, #2873 6/5/1/0, #2896 6/3/3/0 (runs 1, 3, 6 failed). None is passing proof for this ticket.

## Design

Use a mutex-owned incremental NDJSON writer, retaining at most 4096 bytes of one envelope and discarding its remainder until the next newline. Complete bounded UTF-8 JSON envelopes recognize only `system/init`, `system/api_retry`, and `result`. EOF after observed Wait may finish a complete final envelope without a newline. Unknown, malformed, partial and oversized frames cannot create progress. A later bounded result remains decodable.
The production result uses `decodeReplyFallback` unchanged for result-field null/duplicate semantics, plus an explicit result type gate. Only successful, trimmed, valid text is returned. Whole-stream receipt saturates at 4097 independently of final-result bytes and predicates. The wrapper mirrors the bounded decoder while forwarding each original chunk unchanged; its persistent producer path remains separate.
Recognized progress tracks a fixed event label, timestamp, init boolean, validated source category and counted decoded retry events. `none` and `ANTHROPIC_API_KEY` are the only reported non-unknown sources. Init/source never prove authentication or readiness. Zero observed retries differs from absent retry metadata. Lifecycle logs carry both current observations and a frozen pre-cancellation snapshot. Consumer metadata requires typed presence and consistent applicability; missing/invalid evidence stays unknown.

## Concurrency model

One writer mutex protects parsing, incremental observations and cancellation snapshots. `cmd.Cancel` freezes once under that mutex before sending the first process-group signal; both the exec context watcher and explicit cancellation use it. Subsequent observations affect only current state. Existing account lookup and Wait goroutines retain their cancellation/shutdown paths; no new goroutine. Process state and completion-dependent predicates remain unread/unknown without Wait receipt.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Chunk split/coalesced frames, skipped frame then later result | `TestReplyFallbackStream` |
| Cancellation before any event then late input/repeated cancellation | `TestReplyFallbackStreamFreeze` |
| Cancellation after progress then late retry/result/repeated cancellation | `TestReplyFallbackStreamFreeze` |
| Unobserved Wait with known incremental progress | `TestReplyFallbackOutputUnknownWait` |
| Wrapper/child correlation and repeated/malformed metadata | `TestSuggestSourceCapturePresence` |

## Error handling

Reject unusable/oversized result envelopes without truncation. Preserve generic fallback failure, 9.8-second context, 100-ms cancellation Wait grace and group kill. Neither incomplete Wait nor result without wire publication proves forwarding loss. Stderr capture (#3025), cancellation stabilization (#3020), and missing-suggestion correction (#2923) remain outside scope.

## Testing strategy

Write synthetic stream/freeze/privacy tests first; adapt existing production and wrapper fixtures to typed stream results. Check result null/duplicate/type errors, source allowlist, long progress, zero/missing retries, metadata gates and unobserved Wait. Run `go test -race ./cmd/pyry/...`, tagged offline suggestion race tests, `go vet ./...`, and `go build ./cmd/pyry`. Verifier owns full hermetic gate. After fixed instrumentation/checks, separately commit and push the declaration before exactly six sequential authenticated targeted launches on its unchanged checkout. Record every outcome and safe correlated observations here; no retries/replacements. Dispatcher owns the separate full live gate.

## Open questions

None. Final-result bounds are independent of receipt saturation; source is reported evidence only.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/e2e-realclaude.md`, “A suggestion frame alone cannot prove its source”, and `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md`, “One isolated Haiku attempt”. State stream format and independent final-result bounds, wrapper versus daemon readers, reported source versus authenticated readiness, frozen pre-cancellation versus post-Wait observations, and unknown versus observed zero. Link this counted batch alongside every unresolved historical failure; all-pass means non-reproduction only. Never include raw output or credentials.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: bounded UTF-8/JSON type gates precede progress/result recognition; wrapper evidence separately requires typed presence and PID correlation.
- Tokens: no new credential handling; selected-account isolation remains in `run`. Only validated source categories leave the decoder.
- Files: preserve private temporary cwd and 0600 metadata, fixed harness paths and 64-KiB evidence limit; no new externally supplied paths.
- Subprocesses: literal CLI arguments and JSON stdin, existing scrubbed environment and process-group cancellation; no shell interpretation added.
- Cryptography/network: no cryptographic or network changes; existing authenticated harness remains responsible for transport.
- I/O: discard oversized envelopes incrementally; memory is bounded independently of stream length.
- Logs: MUST FIX in implementation: never emit envelope contents, raw errors or arbitrary event/source strings; fixed labels/scalars only.
- Concurrency: freeze and writer use one mutex; freeze precedes signal, repeat cancellation cannot replace the snapshot.
- Threat model: child output is untrusted and observation proves neither authentication nor readiness. Missing-suggestion correction belongs to #2923; stderr capture to #3025.

The logging requirement is incorporated into Design, resolving the finding before commit.
**Reviewer:** builder (self-review). **Date:** 2026-10-09.

## Revisions

2026-10-09: Result predicates now describe the bounded decoded final envelope; `result_bytes` explicitly separates its 1–4096-byte bound from saturated 0–4097 stream receipt. Invalid/skipped earlier frames cannot poison a later result. Complete unterminated final JSON is examined only after Wait receipt; it cannot populate the frozen pre-cancellation snapshot. Added late-result/repeated-cancellation, source-null/missing and saturated-result metadata assertions. Offline race checks, touched-package race suite, vet and build pass. Final sizing forecast including declaration/results: approximately 750 written lines, no exports, three local consumers, five criteria, fewer than ten reject paths; no sizing exception required. QMD timed out; runtime-authorized repository search supplied the owning docs.

2026-10-09 (verifier finding 1 on `861b6a3a`): `suggestProgressFields` marks contradictory event/init or event/retry observations and their event age unknown for both current and applicable frozen snapshots. Independent init/source or retry observations remain known, including observed zero; missing metadata is never inferred. `TestSuggestStreamProgressGates` covers all four reported contradictions under both prefixes and independent/missing fields. The original six-run declaration and every outcome remain unchanged; no live rerun or missing-suggestion correction belongs to this rework.

## Six-run declaration

Declared 2026-10-09 after instrumentation commit `0e1aea42` and passing offline checks. The commit containing this declaration will be pushed before launch 1 and is the unchanged checkout for exactly six sequential authenticated launches numbered 1–6 through:

`python3 /work/Projects/pyrycode-agents/dispatcher/scripts/live-claude-gate.py go --tests '^TestInteractiveStream_FallbackReplySuggestionSetThenClear$'`

No retries, replacements, checkout changes, or historical reruns, even on failure/skip/zero execution. Preserve one exchange and the 15-second suggestion window. Count executed/passed/failed/skipped per run and aggregate; authentication/environment failure or zero execution is not proof. Retain only fixed-label wrapper/daemon observations, correlated PIDs/counts/times and wire revisions. All-pass establishes non-reproduction only. Any failed batch remains failed and feeds the evidence-backed #2923 follow-up without implementing correction here. The dispatcher separately owns the counted full live gate; documentation handoff remains pending.

## Six-run results

All six sequential authenticated launcher invocations used unchanged pushed declaration commit `28974c1c9322d7b7b1d2927ada542db52f2289bc`. No retries, replacements or checkout changes. E/P/F/S = **6/2/4/0**. Runs **1, 2, 4 and 6 remain FAIL**; runs 3 and 5 passed the strict set/clear assertions. Separate dispatcher full live gate remains pending.

| Run | Executed | Passed | Failed | Skipped | Launcher exit |
| --- | --- | --- | --- | --- | --- |
| 1 | 1 | 0 | 1 | 0 | 1 |
| 2 | 1 | 0 | 1 | 0 | 1 |
| 3 | 1 | 1 | 0 | 0 | 0 |
| 4 | 1 | 0 | 1 | 0 | 1 |
| 5 | 1 | 1 | 0 | 0 | 0 |
| 6 | 1 | 0 | 1 | 0 | 1 |

Safe boundary observations below are reader-produced scalar metadata, correlated by wrapper PID; wrapper child PID is independently recorded. Whole-stream saturation is not a final-result size. Reported source `none` proves neither authenticated readiness nor cause; zero retries means no decoded retry events observed. No result without publication establishes forwarding loss.

Run 1:

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=0 pid=1617213 elapsed_ms=12975 exit=unknown output={unknown} set_revision=0 clear_revision=0 stage=incomplete invocation (exit/output unknown) child_pid=1617214 spawn=true read={bytes=897 saturated=false} forward={bytes=897 saturated=false}
daemon lifecycle (cause not inferred): pid=1617213 attempt_ms=9804 child_ms=9803 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=true own_deadline_elapsed=true group_cancel_requested=true wait_completed=true exit_observed=true exit_code=-1 exit_signal=9 output_observed=true wait_ok=false wait_delay=false stdout_bytes=4097 stdout_cap_exceeded=true stdout_utf8_ok=true stdout_json_ok=true stdout_result_ok=true stdout_text_ok=true result_bytes=1847 progress_event=result progress_age_ms=481 progress_init=true progress_source=none progress_retries=0 cancel_event=result cancel_age_ms=478 cancel_init=true cancel_source=none cancel_retries=0
```

Run 2:

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=0 pid=1617488 elapsed_ms=12975 exit=unknown output={unknown} set_revision=0 clear_revision=0 stage=incomplete invocation (exit/output unknown) child_pid=1617489 spawn=true read={bytes=897 saturated=false} forward={bytes=897 saturated=false}
daemon lifecycle (cause not inferred): pid=1617488 attempt_ms=9801 child_ms=9800 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=true own_deadline_elapsed=true group_cancel_requested=true wait_completed=true exit_observed=true exit_code=-1 exit_signal=9 output_observed=true wait_ok=false wait_delay=false stdout_bytes=2621 stdout_cap_exceeded=false stdout_utf8_ok=true stdout_json_ok=false stdout_result_ok=unknown stdout_text_ok=unknown result_bytes=unknown progress_event=init progress_age_ms=9341 progress_init=true progress_source=none progress_retries=0 cancel_event=init cancel_age_ms=9340 cancel_init=true cancel_source=none cancel_retries=0
```

Run 3:

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=1 pid=1617755 elapsed_ms=8710 exit=0 output={bytes=4097 utf8=true json=true result_success=true text_valid=true result_bytes=1814} set_revision=1 clear_revision=2 stage=wire set observed child_pid=1617756 spawn=true read={bytes=897 saturated=false} forward={bytes=897 saturated=false}
daemon lifecycle (cause not inferred): pid=1617755 attempt_ms=8742 child_ms=8742 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=false own_deadline_elapsed=false group_cancel_requested=false wait_completed=true exit_observed=true exit_code=0 exit_signal=0 output_observed=true wait_ok=true wait_delay=false stdout_bytes=4097 stdout_cap_exceeded=true stdout_utf8_ok=true stdout_json_ok=true stdout_result_ok=true stdout_text_ok=true result_bytes=1814 progress_event=result progress_age_ms=500 progress_init=true progress_source=none progress_retries=0 cancel_event=unknown cancel_age_ms=unknown cancel_init=unknown cancel_source=unknown cancel_retries=unknown
```

Run 4:

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=0 pid=1618060 elapsed_ms=12973 exit=unknown output={unknown} set_revision=0 clear_revision=0 stage=incomplete invocation (exit/output unknown) child_pid=1618061 spawn=true read={bytes=897 saturated=false} forward={bytes=897 saturated=false}
daemon lifecycle (cause not inferred): pid=1618060 attempt_ms=9803 child_ms=9802 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=true own_deadline_elapsed=true group_cancel_requested=true wait_completed=true exit_observed=true exit_code=-1 exit_signal=9 output_observed=true wait_ok=false wait_delay=false stdout_bytes=2230 stdout_cap_exceeded=false stdout_utf8_ok=true stdout_json_ok=false stdout_result_ok=unknown stdout_text_ok=unknown result_bytes=unknown progress_event=init progress_age_ms=9353 progress_init=true progress_source=none progress_retries=0 cancel_event=init cancel_age_ms=9351 cancel_init=true cancel_source=none cancel_retries=0
```

Run 5:

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=1 pid=1618332 elapsed_ms=6231 exit=0 output={bytes=4097 utf8=true json=true result_success=true text_valid=true result_bytes=1847} set_revision=1 clear_revision=2 stage=wire set observed child_pid=1618333 spawn=true read={bytes=897 saturated=false} forward={bytes=897 saturated=false}
daemon lifecycle (cause not inferred): pid=1618332 attempt_ms=6264 child_ms=6264 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=false own_deadline_elapsed=false group_cancel_requested=false wait_completed=true exit_observed=true exit_code=0 exit_signal=0 output_observed=true wait_ok=true wait_delay=false stdout_bytes=4097 stdout_cap_exceeded=true stdout_utf8_ok=true stdout_json_ok=true stdout_result_ok=true stdout_text_ok=true result_bytes=1847 progress_event=result progress_age_ms=476 progress_init=true progress_source=none progress_retries=0 cancel_event=unknown cancel_age_ms=unknown cancel_init=unknown cancel_source=unknown cancel_retries=unknown
```

Run 6:

```text
fallback source: streams=1 results=1 idle=true calls=1 completed=0 pid=1618594 elapsed_ms=12973 exit=unknown output={unknown} set_revision=0 clear_revision=0 stage=incomplete invocation (exit/output unknown) child_pid=1618595 spawn=true read={bytes=897 saturated=false} forward={bytes=897 saturated=false}
daemon lifecycle (cause not inferred): pid=1618594 attempt_ms=9807 child_ms=9806 parent_canceled=false parent_deadline=false fallback_canceled=false fallback_deadline=true own_deadline_elapsed=true group_cancel_requested=true wait_completed=true exit_observed=true exit_code=-1 exit_signal=9 output_observed=true wait_ok=false wait_delay=false stdout_bytes=2426 stdout_cap_exceeded=false stdout_utf8_ok=true stdout_json_ok=false stdout_result_ok=unknown stdout_text_ok=unknown result_bytes=unknown progress_event=init progress_age_ms=9105 progress_init=true progress_source=none progress_retries=0 cancel_event=init cancel_age_ms=9097 cancel_init=true cancel_source=none cancel_retries=0
```

Evidence-backed follow-up for #2923: run 1 froze a valid result 478 ms after recognition, then observed deadline cancellation/Wait SIGKILL with no publication. Runs 2, 4 and 6 froze init-only progress, observed zero retries, and received no decoded result. Investigate these distinct deadline/completion paths while preserving account isolation, the existing publication guards and strict staging; this slice makes no correction or cause claim. Runs 3 and 5 demonstrate successful publication with saturated stream receipt and bounded final results, but do not resolve any historical or new failure. Documentation and separate dispatcher live gate remain pending.
