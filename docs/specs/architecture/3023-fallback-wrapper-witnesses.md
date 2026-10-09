# Surviving fallback wrapper witnesses (#3023)

## Files read
- `internal/e2e/realclaude/interactive_stream_reply_suggestion_test.go` → `installSuggestCLI`, `suggestSource`, `readSuggestSource`, `suggestLifecycle`, fallback set/clear: wrapper evidence and consuming diagnostics; preserve merged daemon applicability gates.
- `internal/e2e/realclaude/reply_suggestion_staging_test.go` → `TestSuggestCLIFallbackEvidence`, `TestSuggestCLIFallbackIncomplete`, privacy/isolation checks: exact I/O and cancellation proof.
- `cmd/pyry/reply_fallback.go` → `replyFallback.run`, `replyFallbackOutput`: unchanged print-mode JSON, group cancellation and post-Wait daemon snapshot.
- `docs/knowledge/features/e2e-realclaude.md` → “A suggestion frame alone cannot prove its source”: unknown differs from zero; readiness can precede forwarding; predicates require applicability gates.
- `docs/knowledge/features/development-verification.md` → evidence presence and test execution: missing fields must not become explicit zero; count actual executed checks.
- `CODING-STYLE.md`: contract comments, same-package tests and bounded process cleanup.

## Context
Recover only wrapper witnesses from [PR #2896's complete historical spec](https://github.com/pyrycode/pyrycode/blob/25d778fbd0e54753bd82a6d4eca9a097c996f6f4/docs/specs/architecture/2882-fallback-stdout-evidence.md), instrumentation `ed5730f9`, recovery head `25d778fb`. Retain that history and `feature/2882`/its worktree; this child's final main-based diff contains only its slice. `feature/3023` is based on main. Port only the needed wrapper and source changes from `origin/feature/2882` (`ed5730f9` and the staging tests at `25d778fb`) with `git show <commit>:<path>` or `git diff`, as ordinary new commits. Never merge, rebase onto or branch from the 2882 lineage: merging main into it deletes `docs/specs/architecture/2882-fallback-stdout-evidence.md`, which automatic approval review refuses. No aggregate merge into main. No decision record needed.

Historical executed/passed/failed/skipped declarations remain #2873 **6/5/1/0**, earlier #2882 **6/4/2/0**, PR #2896 **6/3/3/0**. PR #2896 runs **1, 3 and 6 FAIL**: spawn witnessed, read/forward unknown, independently zero daemon-retained bytes after wrapper Wait/SIGKILL. Cause remains unknown; historical failures are unresolved, never passing proof. Do not rerun these batches or order a targeted authenticated batch. Dispatcher's counted full live gate remains pending.

## Design
`installSuggestCLI` appends fixed spawn/read/forward records after successful creation, first nonempty read, and first acknowledged write plus successful flush. Wrapper/child PIDs correlate every record including completion. Prefix counts are 1–4097 with saturation exactly at 4097; completed retained output is capped at 4097. Forward exact bytes, inherit stdin/stderr and process group, retain 0600 metadata. No change to format, timing, account/isolation/validation/publication or native exclusion.

`readSuggestSource` reads at most 64 KiB. Parse complete objects with typed/present scalar metadata, rejecting duplicate keys and inconsistent ordering/correlation. Partial final appends preserve prior witnesses. Invalid records invalidate claimed progress/completion rather than invent evidence. Invocation, spawn, read, forward and completion form an ordered single-invocation observation; empty output may complete directly after spawn. Completion requires explicit elapsed/exit/count/predicate metadata, correlation and consistency with progress. Diagnostics report unknown where unobserved and retain UTF-8/decode applicability gates. Spawn proves creation only; read/flush prefixes prove neither totals, pre-deadline arrival, daemon receipt, EOF, child exit nor publication eligibility.

Overlap: retained #2882 is the recovery source; #2873 has no current file diff and #3026 touches different files. #3022 is merged. No unmerged dependency.
Sizing: approximately 550–700 written lines including tests/spec, 0 new exported types/interfaces, 0 consumer signature updates, 3 acceptance criteria, at most 10 validation/state rejection groups. Recount final main-based slice before handoff; documentation is owned by the next stage.

## Concurrency model
Wrapper and child remain in the daemon group. The wrapper records synchronously around read/write/flush; SIGKILL may leave an incomplete append. Offline subprocess tests use bounded contexts, group kill and Wait on every cleanup path. Existing stream observer exits at EOF/group termination. No new Go goroutine.

## State transitions and identity reuse
| Event | Race test |
|---|---|
| Spawn then group cancellation, no output/completion | `TestSuggestCLIFallbackIncomplete/spawn` |
| First read then cancellation while stdout pipe blocks flush | `TestSuggestCLIFallbackIncomplete/read` |
| Acknowledged forwarding then group cancellation | `TestSuggestCLIFallbackIncomplete/forward` |
| Repeated invocation, duplicate/out-of-order stages, changed PIDs or completion | `TestSuggestProgressMetadata`, `TestSuggestSourceCapturePresence` |
| Partial append, absent metadata, saturated prefix and completion | `TestSuggestProgressMetadata`, `TestSuggestSourceCapturePresence`, `TestSuggestCLIFallbackEvidence` |

## Error handling
Missing file/field or unfinished append means unknown. Malformed/incorrectly typed/duplicate/out-of-order/mismatched records cannot create observations. Only normalized scalars enter diagnostics; raw parse errors and child output never do. Preserve child exit/signal behavior and fixed wrapper exception label.

## Testing strategy
Recover the three cancellation boundary tests, progress/presence tables and completed exact-I/O checks first; observe failures before implementation. Extend tables for duplicate JSON keys, completion order/presence/correlation and count consistency. Use blocked stdout to deterministically separate read from acknowledged flush; assert process group membership before killing. Run tagged focused offline `TestSuggest` race tests, tagged package vet, module vet and build into scratch. Full-module hermetic suite and counted authenticated live suite belong to verifier/dispatcher.

## Open questions
None. Recovery narrowing must preserve current main's daemon code and tests byte-for-byte; compare final diff before opening the fresh PR, then record its URL on #2896. Later siblings #3024/#3025 own stream/progress format and stderr capture.

## Documentation handoff
Pending documentation stage: update `docs/knowledge/features/e2e-realclaude.md`, “A suggestion frame alone cannot prove its source”, with wrapper creation/read/acknowledged forwarding versus daemon receipt, unknown versus zero, prefix/saturation versus totals and spawn versus readiness. Preserve #3022's daemon snapshot/applicability contract. Link the counted historical batches above without changing failures or claiming a cause. Never include raw output or credentials.

## Security review
**Verdict:** PASS
- Trust boundaries: `readSuggestSource` must validate present typed scalar fields, unique keys, ordering and PID/count correlation before diagnostics trust metadata.
- Tokens/secrets: wrapper inherits the existing production account contract; records exclude argv, environment, credentials and payloads. Privacy sentinels exercise this boundary.
- File operations: test-owned private temporary path, evidence 0600 and wrapper 0700; bounded append records tolerate interruption. No caller paths or persistent secrets; no atomic rename needed for append evidence.
- Subprocesses: literal argument forwarding and inherited stdin/stderr/group; no shell interpolation of prompts; offline helper shell quotes the test executable. Group kill and Wait bound cleanup.
- Cryptography: no keys, nonces, tokens or cryptographic changes.
- Network/I/O: no network changes; metadata read capped at 64 KiB and retained stdout at 4097 while forwarding exact bytes.
- Errors/logs: fixed labels and validated scalars only, fixed Python exception handler; applicability gates prevent misleading decoded claims.
- Concurrency: synchronous records, partial append preserved as unknown; synchronized stdout capture read before cancellation and inspected after Wait.
- Threat model: observer-only evidence, not authorization/readiness. OUT OF SCOPE: stream-format investigation #3024 and stderr-class evidence #3025; historical failure cause unresolved.
**Reviewer:** builder (self-review per checklist)
**Date:** 2026-10-09

## Revisions
- 2026-10-09 builder review: recovery metadata accepted absent elapsed fields and duplicate JSON keys. Require exact present typed scalar fields, a positive invocation PID/start time, and ordered correlated completion; invalid evidence clears completion and reports ambiguous progress. Added `TestSuggestPrefixCorrelation` for 1/4096/4097 prefixes, partial completion, and invalid repeated/correlated metadata. The self-review remains PASS: fixed metadata only, exact inherited I/O/group, 0600 evidence, 64-KiB read bound, no new credential/network/cryptographic path. No daemon changes.
