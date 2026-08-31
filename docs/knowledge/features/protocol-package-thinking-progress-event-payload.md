# Thinking-progress event payload (#1386)

The v2 wire shape for claude's **only mid-turn proof of life** on the stream-json surface. During a long assistant turn nothing else crosses the wire, so a phone showing "thinking" cannot separate a slow answer from a wedged session. `internal/streamsup`'s parser has translated claude's `system/thinking_tokens` line into `turnevent.ThinkingProgress{EstimatedTokens, EstimatedTokensDelta}` since #1385, rate-bounded at one event per 64 accumulated tokens (`streamsup.minThinkingTokensPerEvent`); this ticket (#1386) gave it wire shape **and** wired `internal/turnbridge/outbound.go`'s `MapEvent` in the same slice — unlike the background-task family's #1393/#1394 split, there was no reason to split a single two-int frame with no producer dependency gap.

```go
type ThinkingProgressPayload struct {
    ConversationID       string `json:"conversation_id"`
    EstimatedTokens      int    `json:"estimated_tokens"`
    EstimatedTokensDelta int    `json:"estimated_tokens_delta"`
}
```

- **Conversation-scoped, not turn-scoped — no `turn_id`, matching `StallPayload`/`ApiRetryPayload`.** The bridge supplies `ConversationID`; `turnevent.ThinkingProgress` carries no identity of its own. `tc.TurnID`/`tc.Seq` are read by `MapEvent`'s signature but ignored for this variant, the same posture as `Stall`/`ApiRetry`/`Compacting`/`Unrecognized`.
- **No `session_id`.** claude's session identity is not the daemon's conversation identity, and the parser drops `session_id`/`uuid` before the event exists (#1380/#1385) — the payload has no field capable of carrying either, so the leak this note exists to prevent is structurally impossible, not merely avoided.
- **No `TruncatedFields`, and its absence is a decision, not an omission.** Unlike every text-carrying sibling above (`BackgroundTask*`, `UnrecognizedMessagePayload`), this payload carries **no claude-authored text at all** — both fields are ints, nothing is ever cut, and a permanently-nil field would claim a bound that does not exist. Do not pattern-match the truncation discipline across; the architect's security review flagged this as the trap a spec author could fall into.
- **Both ints cross verbatim, including the zero value `{0,0}`.** A legitimate reading, exactly as `ApiRetryPayload`'s `{0,0}` is "count unknown" — no suppression branch, since a second filter here would silently diverge from the producer's own rate bound.
- **Two consumer hazards are documented once, not twice.** `EstimatedTokens` is not monotonic across a turn (it restarts near zero at every inference-request boundary) and the `EstimatedTokensDelta` values a client receives do not sum to the turn's total (the rate bound drops most lines and no field reports the residue). Both are measured with numbers in `turnevent.ThinkingProgress`'s doc comment, the single source of truth, and restated for a wire consumer in `docs/protocol-mobile.md` § `thinking_progress` — not here, to avoid a third copy drifting from the other two.
- **The wire name is the daemon's variant name, not claude's subtype.** `TypeThinkingProgress = "thinking_progress"`, never `thinking_tokens` or a string derived from it — `TestThinkingProgressType_IsNotClaudesSubtype` pins this with a substring check on the constant (scoped to the type only; the field names legitimately contain `tokens`), not just an exact-literal match, so a rename-by-pattern-matching regresses loudly.
- **Pure DTO: no methods, no constructor, no `Validate()`.** Same posture as every payload in this file.

One golden round-trip test in `interactive_test.go` over `testdata/thinking_progress.json`, plus the anti-drift substring check above. See [codebase/1386.md](../codebase/1386.md).
