# #2735 — say whether the refused turn stalled or kept working

## Files read

- `internal/e2e/realclaude/interactive_stream_question_refusal_test.go` → `settleRefusedTurn`: the post-refusal drain whose deadline message after the dismissal is the one this ticket changes; its `switch env.Type` silently drops frames it does not act on, `api_retry` among them.
- `internal/protocol/interactive.go` → `ApiRetryPayload`: `ConversationID`, `Active` (show/clear edge), `Current`/`Total` (`attempt N/M`, `{0,0}` when unparsed).
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` → `perTurnReplyBudget`: the 2m budget, left unchanged.

## Change

`settleRefusedTurn` gains a small unexported tally of what arrived after it saw the dismissal of our batch: the count of `api_retry` frames for `convID` with the `Current`/`Total` of the last active one, the bytes of `assistant_delta` text for `convID`, and a per-type count of every other envelope received. The deadline `t.Fatalf` that fires after the dismissal appends that tally to its message. When the tally is empty, the message instead says claude sent nothing for the conversation after the refusal, which is how an upstream API stall looks, rather than "neither stopped nor finished". Frames received before the dismissal are not counted, matching the AC's "after the dismissal". Nothing else moves: the timeout is still a `t.Fatalf`, `perTurnReplyBudget` is untouched, and every other assertion and actuating arm is as it was. Test-only, under the `e2e_realclaude` tag.

## Testing strategy

The live test itself is the coverage: its passing path is unchanged and the dispatcher's live gate runs it (`needs-real-claude`). Offline, `go vet -tags e2e_realclaude ./internal/e2e/realclaude/...` proves the package still compiles; the deadline branch cannot be driven without a stalled live claude, and the tally's formatting is a few lines read in review.
