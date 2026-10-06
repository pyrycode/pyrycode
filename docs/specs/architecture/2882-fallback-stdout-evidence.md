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
