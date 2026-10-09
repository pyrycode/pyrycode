# #3022 — bounded post-Wait fallback stdout validation

## Files read

- `cmd/pyry/reply_fallback.go` → `replyFallback.run`, `validReplyFallback`: isolation, deadline, Wait synchronization and publication contract.
- `cmd/pyry/reply_fallback_test.go` → `TestReplyFallbackHelperProcess`, `TestReplyFallbackProcessEvidence`: process evidence and privacy assertions.
- `internal/e2e/realclaude/interactive_stream_reply_suggestion_test.go` → `suggestLifecycle`, `suggestSource.diagnostic`: scalar-only daemon consumption and source applicability gates.
- `internal/e2e/realclaude/reply_suggestion_staging_test.go` → `TestSuggestSourceUncertainEvidence`: complete-record and PID matching checks.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md` → “One isolated Haiku attempt”: account selection and cancellation must remain independent of diagnostics.
- `docs/knowledge/features/e2e-realclaude.md` → “A suggestion frame alone cannot prove its source”: wrapper completion and publication are separate evidence.
- `docs/knowledge/features/development-verification.md` → “Captures and live evidence”: missing counts cannot establish explicit zero.
- `CODING-STYLE.md` and shared builder security review: race checks, scalar privacy and symbol citations.

## Context

Recover one diagnostic contract from [PR #2896](https://github.com/pyrycode/pyrycode/pull/2896). The [immutable historical spec](https://github.com/pyrycode/pyrycode/blob/25d778fbd0e54753bd82a6d4eca9a097c996f6f4/docs/specs/architecture/2882-fallback-stdout-evidence.md) remains at the recovery head. Branch from `25d778fb`, merge current main, and narrow through ordinary commits. Preserve the original branch/worktree and historical record. No new decision record is needed.

Historical E/P/F/S counts remain #2873 **6/5/1/0**, earlier #2882 **6/4/2/0**, and PR #2896 **6/3/3/0** (runs 1, 3, 6 FAIL). Those failures remain unresolved. No historical batch or new authenticated batch is ordered here; the dispatcher owns the full live gate.

## Design

Keep print-mode JSON and existing execution/publication policy. `decodeReplyFallback([]byte) (replyFallbackResult, bool)` shares the production Go struct decoder with diagnostics, including null and duplicate-field semantics. `replyFallbackOutput(*cappedBuffer, bool, error) []any` returns only fixed field names and scalars. After received Wait, report retained count, overflow, UTF-8, applicable decode/result/text predicates, Wait success and `exec.ErrWaitDelay`. Before received Wait, do not inspect stdout or process state; use explicit unknown completion fields.

Retain the existing 4096-byte allowance plus one overflow sentinel: 0–4097 bytes, overflow iff 4097. A saturated snapshot is a lower bound, not total output or EOF. Successful decoding independently permits result-success and trimmed-text validity, regardless of exit success or overflow. Diagnostics establish no pre-deadline arrival, real-child completion or publication eligibility.

`suggestLifecycle` consumes a complete matching wrapper-PID daemon record. Only present, typed, consistent fields become observations. Validate count/cap correspondence and Wait status; preserve unknown fields and UTF-8/decode applicability. `suggestSource.diagnostic` adds the same applicability gates without changing wrapper metadata parsing. Wrapper witnesses/correlation belong to #3023, stream adaptation to #3024, stderr to #3025, cancellation-test stabilization to #3020. Narrow all unrelated recovery changes out of this PR, including the historical spec's working-tree copy.

Sizing recheck: one deliverable, approximately 400–500 written lines including tests/plan/narrowing, zero exported types/interfaces, two decoder consumers, three acceptance criteria, no new state machine. All five limits hold. Branch overlap: #2882 is the explicit recovery source; #2873 has no outstanding changes to these files; #3014 touches different files. No new dependency.

## Concurrency model

No new goroutines. Existing buffered Wait channel synchronizes stdout and ProcessState reads. Preserve the received cancellation-arm Wait error. Group SIGKILL and bounded Wait retain their current shutdown path; the caller can return without observing Wait, in which case the waiter owns the buffer and process state. Existing context-aware account lookup remains unchanged.

## State transitions and identity reuse

| Event | Race-detector coverage |
| --- | --- |
| Normal or nonzero helper exit, each fresh PID | `TestReplyFallbackProcessEvidence` |
| Parent cancellation/deadline or internal deadline, bounded Wait received or unknown | `TestReplyFallbackProcessEvidence`, `TestReplyFallbackOutputUnknownWait` |
| Repeated snapshots including null/duplicate decoding and saturation | `TestReplyFallbackOutputPredicates` |
| Missing, partial, malformed or contradictory records; matching versus different PID | `TestSuggestLifecycleOutput`, `TestSuggestSourceUncertainEvidence` |

## Error handling

Preserve generic `errReplyFallback`, fixed argv/model/tools, account isolation, 9.8-second deadline and publication rules. Failed UTF-8 makes decoding unknown; failed decoding makes result/text unknown. A nonzero Wait error remains a scalar failure, never raw error text. Missing/inconsistent consumer evidence remains unknown.

## Testing strategy

Recover relevant tests before implementation and observe failures against main's production/consumer behavior. Exercise zero, partial JSON, invalid UTF-8, valid/saturated captures, unobserved Wait, null and duplicate fields, independent predicates and privacy. Do not require cancellation reaping inside the grace interval; unknown completion is valid evidence.

Run `go test -race ./cmd/pyry/...`, offline tagged focused `TestSuggest` race tests, `go vet ./...`, tagged realclaude vet and `go build ./cmd/pyry` into scratch. The verifier owns the full-module suite and dispatcher owns authenticated execution with test counts.

## Open questions

None. Recovery narrowing and any predicate consistency adjustments are local implementation work under the contract above.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/e2e-realclaude.md`, “A suggestion frame alone cannot prove its source”, to describe the daemon reader's post-Wait snapshot, unknown versus explicit zero, decode applicability, and saturation versus total/EOF. Link the counted historical batches and retain unresolved failures (#2873 6/5/1/0; earlier #2882 6/4/2/0; PR #2896 6/3/3/0, runs 1, 3, 6 FAIL). Never include raw child output or credentials.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] SHOULD FIX: `suggestLifecycle` must reject inconsistent count/cap and Wait-success/delay combinations and gate downstream predicates. Normalize only daemon scalar fields matched to the wrapper PID.
- [Tokens; logs] SHOULD FIX: process and consumer privacy checks must reject prompt/reply/environment/account sentinels. `replyFallbackOutput` emits booleans/counts and fixed unknown labels, never raw bytes or errors; token acquisition and scrubbing in `replyFallback.run` remain unchanged.
- [File operations] No new runtime file paths or writes. Preserve `os.MkdirTemp` private cwd and deferred removal; wrapper evidence changes are #3023's scope.
- [Subprocesses] Preserve fixed argv, JSON stdin, safe-mode exclusions, group SIGKILL, WaitDelay and deadline. No shell or additional process is introduced.
- [Cryptography] No key generation, comparison, storage or primitive changes; this contract concerns bounded process diagnostics.
- [Network/I/O] Preserve `cappedBuffer`'s 4097-byte retention and avoid treating saturation as EOF; no socket/HTTP behavior changes.
- [Concurrency] SHOULD FIX: stdout and ProcessState reads require Wait receipt, including cancellation; test unknown Wait with an inaccessible buffer.
- [Threat model] Content-free diagnostics protect exchange/account privacy. Real-child readiness and stderr attribution remain OUT OF SCOPE for #3023/#3025; authenticated historical failures are not resolved here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-08

## Revisions

### 2026-10-09 — verifier finding 2: recovery route

The parked builder work was recovered manually onto then-current main as plan commit `9d48e4ef` followed by implementation commit `f00bfa54`; `774fc51a` subsequently merged main. The recorded recovery head `25d778fb` is not an ancestor of this PR. This actual route supersedes Context's prescribed branch-from-recovery route: it preserves the child's narrow diagnostic/test/spec slice without importing sibling work or the aggregate historical spec into the main-based diff. No Git history is rewritten. `feature/2882` and retained worktree `/work/Projects/.pyrycode-worktrees/pyrycode/builder-2882` remain at `25d778fb`; the immutable historical spec and all historical E/P/F/S declarations above remain unchanged and unresolved.

### 2026-10-09 — verifier finding 1: zero-byte consistency

`suggestLifecycle` now makes the entire output snapshot unknown when a zero retained count claims invalid UTF-8 or successful JSON decoding. A genuine explicit-zero snapshot retains its count with UTF-8 true, JSON false, and inapplicable result/text predicates unknown; Wait observations remain independent. `TestSuggestLifecycleOutput` covers zero with successful decoding, successful decode/result/text predicates, and invalid UTF-8, while asserting every predicate of genuine explicit zero. Its missing-result case uses a nonzero count so the zero-byte guard cannot hide applicability coverage. All three contradiction regressions failed before this guard was added.
