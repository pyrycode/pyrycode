# #3200: keep isolated reply inference out of extended thinking

## Files read

- `cmd/pyry/reply_fallback.go` → `replyFallback.run`, `replyFallbackEnv`, `replyFallbackStream.envelope`: environment isolation, deadline/completion eligibility, and progress categories.
- `cmd/pyry/reply_fallback_test.go` → `TestReplyFallbackHelperProcess`, `TestReplyFallbackProcess`, `TestReplyFallbackLifecycle`: subprocess fixtures, configured-account selection and stale-publication guards.
- `cmd/pyry/reply_fallback_timing_test.go` → `TestReplyFallbackTimingBudget`, `TestReplyFallbackLookupBudget`: preserve the existing total budget and completion requirement.
- `cmd/pyry/reply_suggestion.go` → `startFallbackLocked`: one attempt and cancellation/session/generation checks remain authoritative.
- `internal/e2e/realclaude/interactive_stream_reply_suggestion_test.go` → `TestInteractiveStream_FallbackReplySuggestionSetThenClear`, `installSuggestCLI`, `readSuggestSource`, `suggestLifecycle`: native-disabled real-Claude set/clear proof and safe metadata.
- `internal/e2e/realclaude/reply_suggestion_staging_test.go` → subprocess/privacy fixtures: observer forwarding and completion are independently checked.
- `internal/e2e/realclaude/fixtures.go` → `homeEnv`, `authenticatedHome`; `interactive_per_conversation_liveness_test.go` → `startPerConversationHarnessSeeded`: authentication and isolated daemon environment.
- `docs/knowledge/INDEX.md`, `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`, `e2e-realclaude.md`, `streamsup-package.md`: scope, package ownership and counted proof conventions.
- `docs/knowledge/features/e2e-realclaude-test-infrastructure.md` § “A suggestion frame alone cannot prove its source”: result, Wait, authentication and publication are separate; no raw output or thinking text may enter evidence.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter-native-reply-suggestions.md` § “One isolated Haiku attempt”; `docs/specs/architecture/2923-fallback-timing.md` § “Six-run results”: preserve isolation, selected account and 29.8-second attempt inside the 30-second bound; retain historical failures.
- QMD lexical searches for fallback reply/init located #2832, #3020, #3024 and #2923. Installed Claude's `alwaysThinkingEnabled` schema defaults supported models to thinking; its `Qyr` disable check gives `MAX_THINKING_TOKENS` precedence over settings. [Official environment reference](https://code.claude.com/docs/en/env-vars) documents `MAX_THINKING_TOKENS=0` for explicit disable on the Anthropic API.

## Context

One deliverable: fix fallback inference spending its short-reply deadline in default extended thinking, with causal regression and authenticated proof. Existing isolation removes user settings but never disables thinking; Claude enables it when `alwaysThinkingEnabled` is absent. Ambient `MAX_THINKING_TOKENS` also survives `replyFallbackEnv`. Short final output does not bound the preceding thinking work.

The supplied #2939 failure/rerun reached init, then respectively no result/own-deadline kill and a valid result/successful Wait/set revision 1/clear revision 2. Its older instrumentation did not record thinking, so those records alone cannot uniquely diagnose the underlying delay. An unchanged targeted baseline at `7a1daa57bae0c88a0c96b2b4e62c990fa76f47ca` reproduced that signature (wrapper/child 213291/213292; 29806 ms; init; no result; SIGKILL; no set).

A temporary exact-forwarding observer then reproduced it with the missing causal evidence: wrapper 215094, observer 215095, real Claude 215096. Complete `system/thinking_tokens` frames rose from an estimated 50 tokens at 1873 ms to 3750 at 29735 ms, immediately before the production 29801-ms own-deadline/group kill. There was no assistant text/result/completion or publication. The daemon still reported init because `replyFallbackStream.envelope` deliberately recognizes only init, retry and result. This is active extended inference consuming the budget, rather than authenticated readiness established by init or a forwarding/Wait-only stall. Earlier diagnostic successes produced thinking blocks before results at 9002 and 6383 ms, explaining variable latency on unchanged code. No thinking content was inspected or retained.

Forecast and finished-plan count: under 300 written lines including tests and evidence; zero exported types, one environment consumer, four acceptance criteria, no new reject branches. No decision record is needed. The overlap audit found only retained #2882 edits to these files; those historical changes are superseded on main and provide no dependency. Keep edits local.

## Design

`replyFallbackEnv` removes every ambient `MAX_THINKING_TOKENS` entry and supplies exactly one `MAX_THINKING_TOKENS=0`. Use this explicit API disable rather than a settings-only disable, which a positive ambient value overrides, or `CLAUDE_CODE_DISABLE_THINKING`, which only omits the API parameter. Keep Haiku alias, account resolution, isolation flags, fixed prompt, parser bounds, native-priority window, successful Wait and existing deadline unchanged.

Extend the existing helper-process fixture with a thinking scenario: when thinking is not explicitly disabled, emit init and safe synthetic thinking progress and withhold result until cancellation; otherwise emit the usual bounded reply and exit. A focused process test exercises unset, positive and already-zero ambient budgets through the real command environment, requiring one account read, one invocation, valid result and successful completion before a short parent deadline. A table tests duplicate ambient entries and unrelated/authentication variable preservation.

## Concurrency model

No new goroutine or lock. Existing account lookup, exec copier/context watcher, stderr reader and Wait worker keep their cancellation/termination paths. The fixture's withheld result ends when the production process-group cancellation kills it.

## State transitions and identity reuse

None: this change only fixes each newly constructed subprocess environment and adds no lifecycle or identity state. Existing `TestReplyFallbackProcessEvidence`, `TestReplyFallbackProcessCancellation`, `TestReplyFallbackTimingBudget` and `TestReplyFallbackLifecycle` cover repeated attempts, deadline/cancel cleanup, completion eligibility and stale-session/generation suppression under the race detector.

## Error handling

Missing authentication/output, unsuccessful completion, parent cancellation and own timeout remain failures without retry, fake substitution or skip. Thinking disable affects only this isolated helper, never the persistent interactive child. A future Haiku alias/CLI that ignores disable must still satisfy the same bounded live result; no fallback to a different model is added.

## Testing strategy

Write the focused environment/process regression first and observe failure on unchanged production. Apply the override and run `go test -race ./cmd/pyry/...`, tagged offline `TestSuggest` race tests, `go vet ./...`, tagged live-package vet and binary build to scratch. Commit/push the correction before exactly six sequential authenticated exact-selector launcher invocations on one declared unchanged commit. Retain every outcome (E/P/F/S and launcher exit), PID-correlated source/lifecycle scalars and revisions, without replacement of failures. Required aggregate is 6/6/0/0. The dispatcher's full live/hermetic gates remain separate later-stage checks.

## Open questions

Resolved during diagnosis: default thinking is enabled and an actual stalled call continues thinking to its own deadline. Installed CLI plus the official environment contract establish the explicit-disable setting and ambient precedence. Final six-run acceptance remains to be measured after the correction is committed.

## Documentation handoff

Pending for documentation stage:
- Update `docs/knowledge/features/e2e-realclaude-test-infrastructure.md`, “A suggestion frame alone cannot prove its source,” with the established cause, correction and counted evidence link. Preserve historical failures and distinguish non-reproduction from cause proof.
- Update `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter-native-reply-suggestions.md`, “One isolated Haiku attempt,” to describe the resulting explicit no-thinking inference contract. Preserve historical failures and distinguish non-reproduction from cause proof.

## Security review

**Verdict:** PASS

**Findings:**
- [Trust boundaries] `replyFallbackEnv` fixes a child configuration value; JSON stdin stays untrusted data and result eligibility remains at `decodeReplyFallback`/`validReplyFallback` plus successful Wait.
- [Tokens] Account lookup/scrubbing is unchanged. Tests use synthetic credentials; live authentication comes only from the authorized launcher. No credential is recorded.
- [Files] Only private fixture/scratch metadata and the plan are added; evidence contains validated scalars, no raw output. Existing temp-cwd ownership/removal is unchanged.
- [Subprocesses] SHOULD FIX: remove all ambient thinking-budget duplicates before adding zero, so a hostile or mistaken inherited value cannot restore thinking. The command remains fixed argv/JSON stdin, with group kill and bounded Wait.
- [Cryptography] No keys, nonces or cryptographic operations change.
- [Network/I/O] API/account selection and bounded parsing are unchanged; the fix reduces unnecessary generation and adds no network surface.
- [Errors/logs] Keep generic errors and existing scalar diagnostics. No prompts, replies, thinking content, raw stderr or text-derived stderr classifications enter records.
- [Concurrency] No new shared state or goroutine; regression fixture cancellation uses the existing process-group shutdown.
- [Threat model] The helper remains unable to read workspace/history or use tools/MCP/hooks. This configuration correction adds no permissions or model substitution.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-11

## Diagnosis records

All launches below used unchanged baseline `7a1daa57bae0c88a0c96b2b4e62c990fa76f47ca`. The temporary observer forwarded exact stdout, inherited stdin/stderr, stayed in the daemon group and wrote only validated scalar metadata outside the worktree. These diagnostic launches are not the corrected acceptance batch; none is replaced or counted as a correction.

| Launch | E/P/F/S | Launcher exit | Production wrapper / real CLI | Observations |
| --- | --- | --- | --- | --- |
| Initial sandboxed baseline | 0/0/0/0 | 1 | Unobserved | No test executed; not a pass. |
| Escalated baseline | 1/0/1/0 | 1 | 213291 / 213292 | 29806-ms own-deadline kill, init, no result, no publication. |
| Observer diagnosis 1 | 1/1/0/0 | 0 | 214041 / 214043 | Thinking block before real result at 9002 ms; successful Wait and set/clear 1/2; 9600-ms daemon attempt. |
| Observer diagnosis 2 | 1/1/0/0 | 0 | 214604 / 214606 | Thinking block before real result at 6383 ms; successful Wait and set/clear 1/2; 6915-ms daemon attempt. |
| Observer diagnosis 3 | 1/0/1/0 | 1 | 215094 / 215096 | Thinking estimates 50 at 1873 ms → 3750 at 29735 ms; own-deadline kill at 29801 ms, no result/Wait success/publication. |

The observer PID in diagnosis 3 was 215095 (the production wrapper's recorded child); it spawned real Claude 215096. All 26 thinking-progress records before termination were bounded complete system envelopes (193–196 bytes); no result or assistant frame was observed. The daemon's recognized event stayed init. This explains how whole-stream receipt can saturate at 4097 while init remains the last recognized event, without any oversized result or forwarding failure. The historical #2939 records and #2923/#3024 batches remain unchanged. [Cause and limitations recorded on the ticket](https://github.com/pyrycode/pyrycode/issues/3200#issuecomment-6115304462).

## Six-run declaration

After green builder checks, push the commit containing the correction and this declaration and announce its exact hash before launch 1. Run exactly six sequential authenticated invocations of `python3 /work/Projects/pyrycode-agents/dispatcher/scripts/live-claude-gate.py go --tests '^TestInteractiveStream_FallbackReplySuggestionSetThenClear$'` on that unchanged clean checkout. Do not add the temporary diagnostic observer to these launches. Retain all E/P/F/S counts and launcher exits, safe source/lifecycle metadata and wire revisions. No retries, replacements, fake substitution or skips; acceptance requires 6/6/0/0. Each launch must preserve the seeded conversation, native-disabled persistent child, bounded nonempty UTF-8 single-line fallback and explicit-null higher-revision clear. The dispatcher's separate full live gate remains pending.

## Offline validation

The new focused race regression was run before production edits: default/positive thinking cases failed by withholding results until their three-second parent deadlines, the already-disabled case passed, and the environment uniqueness assertion failed (leaf E/P/F/S 4/1/3/0). After correction, all four leaves passed (4/4/0/0; package 4.102 seconds). The complete `go test -race ./cmd/pyry/...` passed in 232.332 seconds, preserving timing, cancellation, configured-account and stale-publication coverage. Tagged offline `TestSuggest` race tests passed in 28.330 seconds. `go vet ./...`, tagged live-package vet, binary build to scratch and `git diff --check` passed. No full-module or dispatcher-owned live gate is claimed passed.
