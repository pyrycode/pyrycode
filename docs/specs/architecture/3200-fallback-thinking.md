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
