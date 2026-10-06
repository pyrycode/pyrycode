# #2881 — finalize retained fallback lifecycle evidence

## Files read
- `docs/specs/architecture/2873-fallback-source-evidence.md` → retained design, security review and every historical/bounded result; append current disposition without rewriting history.
- `cmd/pyry/reply_fallback.go`, `cmd/pyry/main.go` → `replyFallback.run`, `runSupervisor`: pre-cleanup context snapshots, atomic group cancellation and Wait-guarded process state; retain unchanged.
- `cmd/pyry/reply_fallback_test.go` → `TestReplyFallbackProcessEvidence`: process/context/privacy checks, including actual internal deadline with live parent.
- `internal/e2e/realclaude/interactive_stream_reply_suggestion_test.go` → `installSuggestCLI`, `suggestSource.diagnostic`, `readSuggestSource`, `suggestLifecycle`, fallback live test: isolated real Haiku execution, safe PID correlation and strict bounded set/clear.
- `internal/e2e/realclaude/reply_suggestion_staging_test.go` → `TestSuggestCLIFallbackIncomplete`, source/isolation/privacy checks: retain synchronized parent-side forwarding witness.
- `docs/knowledge/features/e2e-realclaude.md`, `development-verification.md` → source isolation, counted evidence and missing-witness limits; QMD searched before planning.

## Change
Continue `68155249` on `feature/2873` in the retained worktree and update existing PR #2875, as explicitly scoped by #2881; do not duplicate its implementation on a new PR. Append a superseding current disposition to the retained spec and correct the PR summary/current handoff/closing reference to #2881. State “reproduced in the bounded batch at the internal-deadline/live-parent stage; real-child output and historical failure causes remain unknown.” Lifecycle evidence is complete; stdout investigation continues in #2882. Preserve all historical results, six result paths/commits/observations/revisions and run 4 as FAIL. No code, scheduling, policy, deadlines, cancellation, one-exchange staging, 15-second window or #2859 release-control changes; no additional live batch. No decision record needed. No fetched feature branch overlaps either spec path.
Sizing checked before writing and again before commit: one disposition deliverable, 750 retained written lines plus at most 50 finalization lines, zero new exports/consumer changes/reject branches, three acceptance criteria. All open questions are resolved by the ticket's bounded-evidence disposition; unknown child output is explicitly deferred to #2882.

## Testing strategy
Reuse retained checks: `go test -race ./cmd/pyry/...`, tagged race-enabled `^TestSuggest` observer/process/privacy checks with counted results, `go vet ./...`, tagged package vet and `go build ./cmd/pyry` with output outside the worktree. Check the final diff preserves every existing production/test byte and historical result. Verifier full-module checks and dispatcher-owned full live gate remain required/pending with `needs-real-claude`; no absence becomes a pass and no failed current gate is waived.

## Documentation handoff
Pending documentation stage: in `docs/knowledge/features/e2e-realclaude.md`, extend “A suggestion frame alone cannot prove its source” with wrapper source fields, daemon lifecycle fields, PID correlation and unknown/ambiguous cases. Record unchanged staging/window, counted batches, the observed internal-deadline/live-parent stage, strict FAIL on absence and the need for counted repeated baseline comparisons. State that historical missing witnesses cannot be recovered by passing reruns, timeout alone does not establish a defect and non-reproduction does not establish a fix. Carry forward older documentation-only criteria from the retained spec.

## Security review
**Verdict:** PASS — builder self-review, 2026-10-06; no MUST FIX or SHOULD FIX findings.
- [Trust boundaries; errors/logs] Existing `suggestLifecycle` parses only fixed numeric/boolean fields; retain content-free observations and never publish generated text, raw stdout/stderr, argv, child paths, environment values, credentials or raw errors.
- [Tokens; subprocesses; network/I/O; cryptography] No new execution, authentication, endpoint or crypto operation; `replyFallback.run` retains its real isolated bounded call and existing caps. No credentials are obtained for this finalization.
- [Files; concurrency] Only plans and intended PR prose change; existing private observer files, cancellation/reaping, synchronized captures and pre-cleanup/Wait-guarded snapshots remain unchanged. No goroutine is added.
- [Threat model] Evidence disposition cannot establish a unique cause from timing/cancellation or recover missing witnesses; output investigation is OUT OF SCOPE in #2882 and authenticated absence remains FAIL.

## Revisions
- 2026-10-06: The operator transferred the retained commits to `feature/2881` and opened PR #2892, closing #2875 as superseded so the dispatcher can track this child. Continue on the assigned branch and active PR; preserve the disposition, every historical result and unchanged fallback implementation. Revalidate offline checks after the dispatcher merge from main; no additional authenticated batch.
