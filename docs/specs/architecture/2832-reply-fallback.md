# #2832 — last-exchange reply fallback

## Files read
- `cmd/pyry/reply_suggestion.go` → `beginWrite`, `noteDelivered`, `suggest`, `Run`: queue identity, eligibility, publication and clears.
- `cmd/pyry/interactive_turn_v2.go` → `HandleFor`: main-agent message identity and turn completion.
- `cmd/pyry/main.go` → `runSupervisor`: configured binary, account and daemon context.
- `cmd/pyry/claude_account.go` → `provider`, `runTokenCommand`: fresh credential reads, bounded child output and group cancellation.
- `cmd/pyry/reply_suggestion_test.go` → `newSuggestionEmitter`: existing lifecycle assertions.
- `internal/e2e/realclaude/interactive_stream_reply_suggestion_test.go` → `suggestWatch`: set/explicit-null live proof.
- `docs/knowledge/features/streamsup-package.md` and `streamsup-package-draining-turnevents-into-the-interactive-emitter.md`: post-result native events and per-conversation attribution.
- `docs/knowledge/features/claude-account-source.md`: cancellation must bound the caller independently of child pipe termination.
- `docs/knowledge/features/development-verification.md`, `e2e-realclaude.md`, `CODING-STYLE.md`: safe text provenance and tagged-test verification.
- Claude CLI reference, headless and environment-variable documentation: bare mode loses subscription authentication; disable discovery separately.

## Context
One deliverable: produce a bounded suggested next reply when native output is absent, through the existing owner. No decision record needed. No overlapping remote feature branches found. Sizing: approximately 760 written lines, zero exported types, two changed consumer sites, five criteria and fewer than ten lifecycle rejection branches; all builder limits hold.

## Design
Retain only bounded delivered `QueuedMessage.Text` and the last main-agent message's chunks keyed by `MessageID`; reject blank final prose. The first 8192 UTF-8 bytes per side end at a code point. On successful completion capture session attribution and start a cancellable two-second native wait. Native output retains its existing unchanged publication, including pending delivery confirmation. Once the window expires, matching confirmation can start one fallback; invalidation cannot be undone.
A private `cmd/pyry` helper runs the configured binary with `haiku`, one fresh print-mode turn and JSON stdin containing only the two exchange sides. Fixed system instructions replace the prompt. Empty setting sources, explicit hook/memory disabling, disabled skills/tools, strict empty MCP config, no session persistence, isolated temporary cwd and disabled attachment expansion exclude workspace/session context while preserving installed login. Scrub inherited feature/model overrides; configured providers replace ambient authentication and are re-read inside the deadline. No bare mode, shell, resume, shared runner API or dependencies.
Parse a successful JSON result and accept only trimmed valid UTF-8, nonblank single-line output without control or Unicode line separators, at most 240 code points and 1024 bytes. Reject rather than truncate.

## Concurrency model
The existing leaf mutex owns eligibility, generation, cancel function and attempt latch. Timer and inference workers never hold it during waits, provider reads or child execution. Every reset cancels and advances generation. Publication rechecks generation and current session; native publication cancels pending inference. Daemon shutdown cancels all workers and joins them. A ten-second context includes provider lookup and startup; process groups receive SIGKILL on cancellation and bounded pipe wait prevents escaped descendants holding completion open.

## Error handling
Missing binary/model/auth, provider refusal, child error, cancellation, timeout or invalid output produce no publication and no retry. Provider reads are selected against cancellation so a non-cooperative reader cannot block the attempt. Exchange, output, credentials and child errors are never logged.

## Testing strategy
Tests first: manually released native-window seam proves zero native calls, one fallback, native priority, late confirmation, invalidation, conversation/session isolation and stale-result suppression. TestHelperProcess validates exact flags, encoded stdin, credential replacement/refusal, input/output bounds and process cancellation. Tagged live tests disable native output for fallback set/clear and refuse fallback in the native-specific test. Run touched-package race tests, module vet, binary build and tagged live-package compile; dispatcher owns executed live gate counts.

## Open questions
None; isolation controls follow documented CLI semantics. Unsupported flags fail silently as unavailability.

## Documentation handoff
Pending documentation stage:
- `docs/protocol-mobile.md`, section `reply_suggestion`: document native-first selection, the two-second native window, one Haiku fallback through existing authentication, silent unavailability, output bounds and unchanged clear semantics.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md`, section `Native reply suggestions after the result`: extend it with final-exchange selection and bounds, late delivery confirmation, cancellation/native priority and the ten-second, no-retry fallback lifecycle.

## Security review
**Verdict:** PASS
**Findings:**
- Trust boundaries: safe delivered text only, encoded stdin and validated child result; model-authored native output preserves its existing untrusted tier.
- Tokens: provider refusal prevents ambient launch; credentials appear only in child environment, never argv/logs.
- File operations: private temporary cwd (0700) has no caller-selected paths; no exchange files or persistent session writes.
- Subprocesses: fixed arguments, no shell/tools/MCP/hooks; group kill, bounded output and cancellable lookup enforce resource limits.
- Cryptography: existing authenticated relay and account source reused; no new cryptographic operations.
- Network/I/O: existing relay publication; 8192-byte input sides and capped result buffer bound retained data.
- Errors/logs: no raw child errors or data are logged.
- Concurrency: one leaf mutex; cancellation and generation checks prevent stale restores; all owned workers join on shutdown.
- Threat model: untrusted suggestion content cannot execute tools or expand attachments; authenticated client input uses existing accept boundary. Client rendering remains the existing protocol responsibility.
**Reviewer:** builder (self-review)
**Date:** 2026-10-05

## Revisions
- 2026-10-05: use documented safe mode in addition to explicit discovery exclusions while retaining OAuth; reserve 200 ms of the ten-second bound for group termination and pipe closure. Empty final chunks reset message eligibility too.
- 2026-10-05 (verifier rework, PR #2850): `noteDelivered` determines nonblank eligibility from full safe delivered text, independently of its retained prefix. `noteAssistantText` accumulates nonblank eligibility across all chunks of the final main-agent message, including chunks after the prefix fills, and resets eligibility at each message boundary. Native and fallback publication must remain eligible when nonblank prose follows 8192 whitespace bytes; only the bounded prefixes reach inference.
- 2026-10-05 (verifier rework, PR #2850): compose `replySuggestions.forget` into `dropRingOnConversationDelete` and pass the owner from `startRelayV2`. The registry has one removal-observer slot, so preserve `Ring.Drop` in the same callback. Both explicit deletion and idle sweeping reach `Registry.Delete`; its callback runs after the registry lock is released. Forgetting cancels the native wait or inference and removes only that conversation's state without waiting on workers. Regression tests use the production callback through deletion/sweeping, then release a late inference result and verify it cannot recreate state.
- Rework files read: `cmd/pyry/relay.go` → `startRelayV2`, `dropRingOnConversationDelete` (production observer wiring); `cmd/pyry/ring_removal_test.go` → `TestDropRingOnConversationDelete` (both removal paths preserve other rings); `internal/conversations/registry.go` → `Delete`, `SetOnDelete` and `internal/conversations/sweep.go` → `Sweep` (shared removal funnel); `docs/knowledge/features/conversations-registry-crud.md` § `SetOnDelete` (single slot and callback lock boundary).
- Security re-review: PASS. Full-text eligibility retains only a boolean beyond the bounded prefixes; no additional exchange data reaches subprocesses, storage or logs. The composed callback takes the owner's leaf mutex after the registry releases its mutex, cancels without joining, and advances generation before discarding state. Native-wait cancellation prevents deleted-conversation launches; in-flight cancellation plus generation/identity checks suppress late results. Authentication, process isolation, input/output bounds and deadline contracts remain as reviewed above.
