# #2873 — fallback reply source evidence

## Files read

- `internal/e2e/realclaude/interactive_stream_reply_suggestion_test.go` → `installSuggestCLI`, `readSuggestSource`, `suggestWatch`, fallback test: current producer isolation and set/clear contract.
- `internal/e2e/realclaude/reply_suggestion_staging_test.go` → `TestSuggestCLIProducerIsolation`, `TestSuggestCLISourceEvidence`: local transparency and privacy checks.
- `internal/e2e/realclaude/harness_daemon_test.go` → `lockedBuffer`: mutex-protected capture for observing forwarded stdout while the process is running.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` → `startPerConversationHarnessSeeded`: authenticated daemon lifecycle.
- `cmd/pyry/reply_fallback.go` → `replyFallback.run`, `validReplyFallback`: 9.8-second deadline, 4096-byte stdout cap, JSON success and trimmed text validation.
- `cmd/pyry/reply_suggestion.go` → `turnEnded`, `noteDelivered`, `waitReplyNative`, `startFallbackLocked`: two-second native window, delivery eligibility and silent rejection.
- `docs/knowledge/features/e2e-realclaude.md` → reply-suggestion discussion: exclude competing producers and fail absent authenticated proof.
- `docs/knowledge/features/development-verification.md` → Captures and live evidence: preserve content-free observations durably and count repeated runs.
- `/work/Projects/pyrycode-agents/dispatcher/scripts/live-claude-gate.py` → `main`: restricted authentication and targeted counted runs.

## Context

An intermittent baseline absence is unclassified because fallback errors are suppressed. This ticket adds test-only evidence; production policy and deadlines stay unchanged. No decision record is needed. Historical baseline counts are in the issue's refinement comment, not proof from this run.

Sizing: one deliverable, approximately 450–600 written lines including checks and plan, zero exported APIs, four existing helper consumers, four acceptance criteria, fewer than ten diagnostic classifications. No other fetched feature branch touches the two target files.

## Design

Extend the existing private source observer to fallback mode. Persistent stdout is still forwarded unchanged, recording stream/result counts. For a fallback one-shot, the wrapper starts the real CLI in the same process group, inherits stdin/stderr, forwards stdout bytes immediately, retains at most 4097 bytes in memory, waits for exit, and records only invocation/completion counters, start/elapsed milliseconds, exit status and boolean output predicates. The observer mirrors `replyFallback.run`'s UTF-8, JSON field types, success subtype/is_error, stdout cap and trimmed `validReplyFallback` conditions. It never stores output, arguments or environment values.

`readSuggestSource` folds counters and the latest invocation metadata. A fixed-field diagnostic includes persistent completion, elapsed time, observable exit/output status and wire set/clear revisions. Missing sets classify as no observed invocation, incomplete invocation, unsuccessful exit, unusable output, usable output without wire set, or unknown for ambiguous evidence. Incomplete output/exit status remains unknown. Usable source output cannot prove publication eligibility or deadline acceptance.

The fallback test logs this evidence before absence failure and after success. Keep the existing one-exchange and 15-second wait unless repeated observations establish a specific fixture correction. Native proof still refuses one-shots; fallback stream still disables native generation; stand-ins never enter the live proof.

## Concurrency model

The persistent child retains exec/PID behavior and its existing pipe observer in the daemon process group; the observer exits at EOF. The one-shot wrapper waits for its child in the same group, forwarding signal termination rather than reporting success. Production group cancellation kills both. No new Go goroutine is required. Evidence uses append-only single writes; readers tolerate an in-progress trailing record and report incomplete metadata instead of guessing.

## Error handling

Observer setup/reading errors report fixed messages without sensitive exception text. One-shot startup failure and child exit are distinct metadata; killed groups can leave only an invocation start. No invocation does not establish why eligibility/authentication/startup prevented one. No retry, extended production deadline or skip is introduced.

## Testing strategy

Write local checks first and observe failure before implementation. Table-driven stand-ins cover success, nonzero exit, malformed/unsuccessful JSON, output/text bounds and incomplete/group-killed invocations. Assert exact stdin/stdout/stderr preservation, exit/signal behavior, private permissions and sentinel exclusion in evidence and the same diagnostic formatter used live. Retain native isolation checks. Run race-enabled local observer tests, tagged vet/compile plus ordinary vet/build; the full-module and full live gates belong to the verifier/dispatcher.

After committing instrumentation, run the restricted launcher three times with `^TestInteractiveStream_FallbackReplySuggestionSetThenClear$`. Record commit identity, executed/passed/failed/skipped counts and metadata in the PR. A failed proof remains failed and is interpreted from the evidence; demonstrated production defects are follow-up refinement, outside this diff.

## Open questions

- Which source stage explains observed absence? Resolve from repeated live runs; retain uncertainty if no absence is observed.
- Does evidence justify changing the fixture staging or wait? Default is no change.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/e2e-realclaude.md`, the reply-suggestion discussion beginning “A suggestion frame alone cannot prove its source”, describe fallback evidence fields, any revised fixture bounds, observed diagnosis or remaining uncertainty. State that fallback absence is a failed proof, not a skip, and repeated counted runs are needed when comparing an intermittent baseline.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `installSuggestCLI` reduces untrusted stdout to numeric counters and boolean validation predicates; source text never becomes diagnostic text.
- [Tokens] Only the real CLI inherits credentials. Neither evidence nor diagnostic formatting reads environment values. Sentinel checks cover inherited credentials and unrelated environment values.
- [File operations] Evidence is precreated at 0600 inside a test-owned private temporary directory; fixed paths are quoted as Python literals. No untrusted path joins or durable secret files. Trailing partial append is handled as incomplete observation.
- [Subprocesses] Direct argv execution, inherited stdin/stderr, no shell interpretation. One-shot wrapper and real child share the existing process group; local termination checks must prove cancellation has no survivor.
- [Cryptography] No new cryptographic operation or key/nonce handling; existing encrypted wire reader remains unchanged.
- [Network and I/O] No new network endpoint. One-shot retained stdout is capped at 4097 bytes; source evidence reading retains its 64-KiB cap. Existing wire read deadline remains unchanged.
- [Errors/logs] SHOULD FIX: Python exceptions could echo raw output or paths; catch observer failures with fixed diagnostics, and test the shared diagnostic formatter for sentinel exclusion.
- [Concurrency] Observers exit on EOF/group termination; one-shot parent reaps its child. Incomplete evidence is not interpreted as successful completion.
- [Threat model] Test-only observation of untrusted Claude output; no product auth/relay change. Production defect investigation beyond these observations remains outside #2873 and requires follow-up refinement if demonstrated.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-06

## Revisions

- 2026-10-06: Refined lifecycle investigation, planned before implementation. The wrapper can be killed with its group; daemon-owned observations now supplement its evidence. Production execution, deadlines, validation, fallback policy, one-exchange staging and the 15-second wire window remain unchanged.

### Lifecycle design

`replyFallback.run` receives the existing supervisor logger through `runSupervisor`. Every successfully started command emits one INFO `reply_fallback.lifecycle` record on return, before deferred context cleanup. Record wrapper PID, attempt/child elapsed milliseconds, parent canceled/deadline flags, fallback canceled/deadline flags, whether its own 9.8-second deadline elapsed, group-cancellation request, Wait completion and observed numeric exit code/signal. Exit remains unknown unless Wait was received and process state exists. Context observations are sampled separately; simultaneous flags must remain ambiguous, never reduced to a unique cause. Elapsed time alone is not attribution.

The wrapper records its own PID at invocation start. The live diagnostic matches that PID in captured daemon stderr, accepts only the fixed event and complete numeric/boolean fields, and formats parsed scalars rather than echoing stderr. Missing or malformed records remain unknown. Existing source predicates and wire revisions remain alongside daemon observations, including on assertion failures.

Concurrency: existing Wait/context-watcher goroutines and shutdown paths stay intact. An atomic flag records cancellation requests from either watcher or caller. Only receiving Wait permits reading process state; stdout is never consulted by diagnostics. No new goroutine or deadline seam is introduced.

Errors: keep the generic unavailable return and existing bounded Wait. A missing Wait is unknown even if cancellation was requested. Completed failure, parent cancellation/deadline and internal deadline with live parent are independent observations. Output remains unknown if the wrapper never recorded completion.

Files read additionally: `cmd/pyry/main.go` → `runSupervisor`, existing INFO logger composition; `cmd/pyry/reply_fallback_test.go` → process helper and cancellation checks; `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md` → isolated Haiku attempt and generic failure contract; `docs/knowledge/features/cli-verb-dispatch.md` → supervisor shutdown. Codegraph symbol search located fallback helpers; task context lacked their chain, so file reads supplied it. Overlap: #2859 touches different composition blocks in `main.go`; no dependency. #2871 touches none of these files.

Sizing rechecked: one evidence deliverable, four acceptance criteria, zero new exports, one production consumer, no new reject branches. Existing diff is 446 added lines; target total including this rework is at most 800 written lines (roughly 780). No decision record needed.

Testing: race-enabled process cases cover successful/nonzero exits, parent cancellation/deadline, the actual internal 9.8-second timeout with live parent, termination before wrapper completion and content-free metadata. Existing tagged observer/I/O/isolation/privacy checks remain. Run touched-package race tests, tagged vet/compile, module vet and product build.

Predeclared live batch: exactly three sequential authenticated runs at the pushed instrumentation commit, using `live-claude-gate.py go --tests '^TestInteractiveStream_FallbackReplySuggestionSetThenClear$'`. Retain every commit, E/P/F/S count and safe observation, regardless of outcome. No retries beyond this batch. Any failed proof returns for refinement with its observed stage and a concrete next investigation; skips cannot establish resolution. Full live gate stays dispatcher-owned.

Security review of revision: PASS. Trust boundary/logging: only fixed keys and booleans/numbers leave the daemon; no stdout, stderr, argv, paths, environment, credentials or raw errors. Tokens/subprocess/network/crypto: existing isolated invocation and bounds unchanged. Files: existing private evidence permissions unchanged. Concurrency: cancellation flag is atomic; Wait receipt precedes process-state reads; snapshot precedes cleanup cancellation. Parser rejects malformed metadata and never echoes unknown strings. No product authorization change or deferred security finding.

Documentation handoff pending: `docs/knowledge/features/e2e-realclaude.md`, discussion beginning “A suggestion frame alone cannot prove its source”: document wrapper source fields and surviving daemon lifecycle fields, PID correlation, unknown/ambiguous observations, unchanged one exchange/15 seconds, every counted live observation and established failure stage without inferring a defect from timeout alone. Absence fails, never skips; intermittent baseline comparisons need repeated counted runs. This supplements the original Documentation handoff.

- 2026-10-06: Address verifier finding 1 on `TestSuggestCLIFallbackIncomplete`: child readiness alone does not establish wrapper forwarding. Reuse `lockedBuffer` and require both readiness and the full forwarded byte count within the existing five-second context before group cancellation; keep exact stdout/stderr and incomplete-evidence assertions after `Wait`. Deferred cancellation/reaping covers early failures. A temporary 200 ms forwarding delay reproduces the old scheduling failure and exercises the corrected synchronization; the delay is a local regression probe, excluded from the final diff. No observer, live-fixture or production behavior changes.
- 2026-10-06: Repeated the three targeted authenticated runs at final instrumentation commit `16960306437d9844059d46453b5a34e53bf31a40`: 3 executed, 2 passed, 1 failed, 0 skipped. Passing invocations completed in 8829 ms (1872 stdout bytes) and 7349 ms (1842 bytes), exit 0, all output predicates true, wire set/clear revisions 1/2. The failed run again had streams/results/calls = 1/1/1, idle true, zero observed completion, elapsed 12976 ms, exit/output unknown and no set. All six runs together: 6 executed, 4 passed, 2 failed, 0 skipped. The failure stage and uncertainty from the initial runs remain; no fixture-bound or production change is justified.
- 2026-10-06: Local null-duplicate checks exposed a validation mismatch: Go leaves an existing scalar struct field unchanged on JSON null, while converting Python pairs directly to a dictionary overwrites it. The observer now preserves scalar values across null duplicates and folds field names for Go-compatible matching. This changes only test evidence validation, not production or fixture bounds.
- 2026-10-06: Resolved the investigation questions with three authenticated targeted runs at `a35e9b89983819d89a52ccb224c860ea09cb0844`: 3 executed, 2 passed, 1 failed, 0 skipped. Each saw one persistent child/result and one invocation. Passing invocations completed at 6487 ms and 8586 ms with exit 0 and all output predicates true, followed by wire set revision 1 and clear revision 2. The failed proof observed no invocation completion at 12983 ms elapsed and no wire set; exit/output were unknown. This locates the observed failure before an observed usable completion, not at proven publication rejection. Timing is consistent with production's 9.8-second group cancellation, but does not establish timeout, exit or output cause. Keep one exchange and the existing 15-second wire wait: no evidence justifies a staging/wait correction or establishes a production defect. Added local assertions for exact exit/byte counts, ambiguous metadata and partial appends; the observer contract is unchanged.
- 2026-10-06: Disposition of verifier finding 1 from review of `aee249fcd81e598bc3c4efe297184e31f65a19a6`: three fresh sequential authenticated runs of `^TestInteractiveStream_FallbackReplySuggestionSetThenClear$` through the restricted launcher at that same commit yielded **3 executed, 2 passed, 1 failed, 0 skipped**. No test or production code changed for these runs. Each observed streams/results/calls = 1/1/1 and idle=true; per-run content-free observations are below. All nine recorded runs total **9 executed, 6 passed, 3 failed, 0 skipped**. The unsuccessful live acceptance remains unresolved and returns for refinement: define the next scoped lifecycle investigation and disposition of the strict live proof before re-review. The incomplete record does not distinguish production deadline/group cancellation from another unfinished or unobserved child outcome. No evidence justifies a fixture staging, wait or bounded-attempt correction, or establishes a production defect. Preserve the one-exchange staging, 15-second observation window, native isolation and production policy. Documentation handoff remains pending and must include this remaining uncertainty.

| Fresh run at `aee249fc` | Executed/passed/failed/skipped | Completed | Elapsed ms | Exit | Stdout bytes | Output predicates | Set/clear revisions |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | 1/0/1/0 | 0 | 12976 | unknown | unknown | unknown | 0/0 |
| 2 | 1/1/0/0 | 1 | 9537 | 0 | 1822 | UTF-8, JSON, success, text: all true | 1/2 |
| 3 | 1/1/0/0 | 1 | 8481 | 0 | 1818 | UTF-8, JSON, success, text: all true | 1/2 |
