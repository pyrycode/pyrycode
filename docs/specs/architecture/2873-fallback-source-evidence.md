# #2873 — fallback reply source evidence

## Files read

- `internal/e2e/realclaude/interactive_stream_reply_suggestion_test.go` → `installSuggestCLI`, `readSuggestSource`, `suggestWatch`, fallback test: current producer isolation and set/clear contract.
- `internal/e2e/realclaude/reply_suggestion_staging_test.go` → `TestSuggestCLIProducerIsolation`, `TestSuggestCLISourceEvidence`: local transparency and privacy checks.
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
