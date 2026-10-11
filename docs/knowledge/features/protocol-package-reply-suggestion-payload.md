# Reply-suggestion payload (#2828)

Body of an `Envelope` whose `Type == TypeReplySuggestion`
([wire contract](../../protocol-mobile.md#reply_suggestion)). Binary → phone,
gated on the negotiated `interactive` capability. The daemon's
[suggestion owner](streamsup-package-draining-turnevents-into-the-interactive-emitter-native-reply-suggestions.md#native-reply-suggestions-after-the-result-2831)
publishes this state after eligible successful turns and clears it on accepted
new work or session lifecycle changes (#2831, #2832). Live and connect-time copies
carry no `event_id` and enter neither history nor replay. Native output has a
two-second window before one last-exchange Haiku fallback; unavailability is
silent. Both sources use the same fields, revisions and explicit-null clears.

```go
type ReplySuggestionPayload struct {
    ConversationID string  `json:"conversation_id"`
    SessionID      string  `json:"session_id"`
    Revision       uint64  `json:"revision"`
    SuggestedReply *string `json:"suggested_reply"`
}
```

- **`SuggestedReply` is `*string` WITHOUT `omitempty` — the session-transition payload's `WorkspaceCwd` precedent, reused for a different invariant.** There, `*string`-no-`omitempty` encodes a cross-field "non-null iff some other field says so"; here it encodes a two-state contract standing on its own: `nil` marshals to the literal `null` that **clears** a suggestion, and a pointer to `""` marshals to a present empty string that does **not** clear it. `omitempty` would collapse that second state into an absent key, destroying the distinction this ticket exists to declare. `TestReplySuggestionPayload_EmptyStringIsNotNull` pins the second half; the round-trip fixtures pin the first.
- **`Revision` orders current state, never persisted history.** `uint64`, positive,
  increasing per conversation within one daemon lifetime. `replySuggestions`
  advances it on sets and clears; the relay's
  [per-connection guard](v2-session-manager-state-machine-connect-time-reply-suggestion-reconcile.md)
  drops revisions at or below the highest delivered for that conversation.
  Clients compare revisions for the `conversation_id`/`session_id` pair and
  discard cached state on a fresh handshake because a daemon restart resets
  counters. This DTO validates neither revisions nor text; the native parser
  enforces single-line, non-blank UTF-8 and the 1024-byte text bound.
  `validReplyFallback` additionally rejects output over 240 Unicode code points
  or containing control characters or Unicode line/paragraph separators after
  trimming. These are source checks, not extra wire fields or DTO validation.
- **Routing key is the pair, not `conversation_id` alone.** Unlike every sibling interactive payload in this package, state here is replaced per `conversation_id` **and** `session_id` together — a session rotation on the same conversation must not have its suggestion state confused with the session that preceded it.
- **Not a `turnevent` variant.** This is a conversation/session state frame, with no `turn_id` and no turn-lifecycle role — [`model_list`](protocol-package-model-list-payload.md) and the [session-transition payload](protocol-package-types-session-transition-payload.md) draw the same line for the same reason, and `docs/protocol-mobile.md` keeps this frame out of its "twenty-three turn-stream events" count accordingly.
- **SECURITY.** `SuggestedReply` is Claude-authored, untrusted, inert display text.
  `decodePromptSuggestion` validates source encoding and shape before it crosses
  the subprocess boundary, then carries accepted native text verbatim;
  `replyFallback.run` validates its JSON result and trimmed fallback text.
  The owner never logs it or submits it to Claude automatically; only an
  explicit user send does.
- **Pure DTO: no methods, no constructor, no `Validate()`.** Same posture as every payload in this file's siblings.

Two round-trip tests in `reply_suggestion_test.go`: `TestReplySuggestionPayload_RoundTrip`, table-driven over `reply_suggestion_set.json` (a populated suggestion, revision 7) and `reply_suggestion_clear.json` (revision 8, `"suggested_reply":null`) — each decodes, re-marshals through the shared `roundTripEnvelope` helper, and asserts all four keys are present with the clear fixture's `suggested_reply` a literal `null`; and `TestReplySuggestionPayload_EmptyStringIsNotNull`, which marshals a pointer to `""` and asserts the wire value is `""`, not `null`. Classified v2-only (`v2OnlyTypes`, `TestIsKnownAppType`, `TestTypeConstants_V1V2Partition`) and outbound `push` (`cmd/pyry/relay_guard_test.go`'s `excludedTypes`) alongside every other interactive event.
