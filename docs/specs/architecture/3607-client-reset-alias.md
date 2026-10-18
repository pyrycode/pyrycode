# Client `/reset` aliases `/clear` (#3607)

## Files read

- `internal/relay/handlers/send_message.go` → `SendMessage`, `isClearCommand`: existing validation, ack-before-reset and fail-closed boundary.
- `internal/relay/handlers/send_message_test.go` → `TestSendMessage_OnlyTheExactLiteralIsIntercepted` and clear tests: existing matcher, ordering and attachment assertions.
- `cmd/pyry/reply_suggestion.go` → `suggestionResetter.StartNewSession`: invalidation happens only after a successful inner call.
- `cmd/pyry/reply_suggestion_test.go` → `TestReplySuggestions_ClearTriggers`: success/refusal and late-suggestion coverage to retain.
- `cmd/pyry/relay.go` → `startRelayV2`: installs the same suggestion resetter into `handlers.SendMessage`.
- `docs/knowledge/features/relay-package-handlers-send-message.md`: preserve the intercept's position and allocation-free token matching.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter-native-reply-suggestions.md`: refused resets preserve suggestions and other conversations remain isolated.
- `docs/protocol-mobile.md` → Application message types, Clear and invalidation, Security model: published exception and threat boundaries.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: local changes, behavioral tests and focused proof.

## Change

Accept exactly `/clear` and `/reset` as first whitespace-delimited tokens, using two constants and a helper returning only the matched constant or an empty string. Replace `isClearCommand` with `conversationResetCommand`, retaining TrimSpace, fixed-prefix and Unicode boundary checks without splitting or allocating per token. Keep the intercept in its existing location and reuse its ack/reset/error handling. Preserve existing event names and add a `command` field containing only the matched constant to interception, absent-resetter and reset-failure records; make their messages apply to either literal. Shrink touched historical comments to the current contract. No interfaces, reset lifecycle, suggestion production code or client menus change. No overlapping feature branch touches the planned files after fetching origin.

This is one useful behavior: a typed `/reset` performs the established reset. Its matching, ordering, refusal, logging and suggestion assertions prove that behavior rather than separate deliverables. Estimated written work: under 500 lines including plan, tests and removed/replaced comments; zero new exported types, one matcher consumer, four acceptance criteria and no new state-machine rejection branches. The same limits hold on this written plan.

## Testing strategy

Extend `TestSendMessage_OnlyTheExactLiteralIsIntercepted` for `/reset`, mixed case, prefixes, first position and Unicode boundaries. Add focused tests in `send_message_reset_test.go` for both literals across successful, failed and absent resetters: ack first, no enqueue/write/resolution, untouched name/LastUsedAt and literal-only logging. Include route and attachment-count rejection checks. Add a handler-to-`suggestionResetter` integration test for success/refusal and preservation of another conversation's held suggestion; retain `TestReplySuggestions_ClearTriggers`. Run tests before implementation and require alias failures, then focused Linux race tests with executed/passed/failed/skipped counts. Run vet and build checks; complete suites belong to the verifier.

## State transitions and identity reuse

| Event | Race-detector coverage |
| --- | --- |
| Either literal requests a reset, or reset refuses/is absent | `TestSendMessage_ResetAliases` |
| Successful reset clears held state; refused reset preserves it; unrelated conversation is unchanged | `TestReplySuggestions_ResetAliases` |
| Late suggestion after invalidation | Existing `TestReplySuggestions_ClearTriggers` |

The helper is pure and the handler adds no goroutine or shared state. Existing reset lifecycle, repeat/reset-in-progress and identity rotation remain owned by `StartNewSession` unchanged. No unresolved design questions or new error/recovery contracts.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md`, the `send_message` row under “Application message types”, the paragraph beginning “#2456 landed the one exception”, and “Clear and invalidation”. State that `/clear` and `/reset` are the two fixed literal exceptions, independent of `slash_command_list`; either uses the same reset, attachment-dropping and ack contract, and only a successful reset invalidates suggestions.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `SendMessage` still validates payload shape, attachment count and `router.Route` before matching. The helper returns a constant only after an exact case-sensitive token boundary, so `/resetfoo` cannot trigger reset. Pairing remains the authorization boundary; no interactive gate or slash-menu authority is added.
- Tokens, secrets and credentials: no credential generation, storage or lifecycle changes. The new log field comes from a matched constant, never the incoming text or trailing content.
- File operations: the alias stays above `resolveAttachments`, `touchConversation` and `autoNameConversation`; it introduces no client-derived path operations. Existing reset workspace confinement and handoff persistence remain within the unchanged reset implementation.
- Subprocesses: no new command construction, shell or environment handling; only the validated conversation ID reaches the existing `StartNewSession` seam. Alias text and attachments never enter the message queue or child stdin.
- Cryptography: no key, nonce, primitive or comparison against a secret changes. Token matching is public and does not require constant-time comparison.
- Network and I/O: no socket handling or frame bounds change. Fixed-prefix matching and a single boundary decode allocate no token list under the existing transport cap.
- Errors, logs and telemetry: retain the ordinary ack on failed/absent resetters, with no new client error oracle. Verify all three log paths identify only the fixed literal and omit trailing text. Existing reset error logging remains unchanged.
- Concurrency: no new goroutines, locks or mutable matcher state. Ack remains before synchronous reset work; `suggestionResetter` continues to invalidate only on success, scoped to the requested conversation.
- Threat model: protocol Security model threat 1 is relevant because client text can reach a broadly privileged child; exact alias interception removes that reachability for the fixed pair. Existing authentication, Noise encryption and transport protect against threats 2–4 and are unchanged. This change introduces no new surface for those threats or the remaining protocol transport/endpoint threats. No new deferred security finding.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-18
