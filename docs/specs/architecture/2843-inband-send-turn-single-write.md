# #2843 — inbandSendTurn writes each turn once

## Files read

- `internal/e2e/realclaude/interactive_stream_inband_model_test.go` → `inbandSendTurn`, `inbandResultCounter`, `inbandResendAfter`, `inbandTurnBudget`: the helper being fixed, its result-count oracle and the resend window being removed.
- `internal/e2e/realclaude/interactive_stream_inband_bypass_revoke_test.go` → its five `inbandSendTurn` calls, the M2 paragraph of the file header and the "FIRST and LAST" comments on A2: prose that relies on the resend and goes stale with it.
- `internal/sessions/runner.go` → `Runner`: the seam the helper writes through; a test double embeds it and overrides `WriteUserTurn` only.
- `internal/streamsup` → `ErrNoLiveChild`: the retry that stays.
- `internal/streamsup/runner.go` → `Run`, `spawnAndWait`: `setStdin` lands before `ChildPID` is set, which is why a resend keyed on a child-PID change was rejected (see Design).
- `internal/e2e/realclaude/inband_bypass_revoke_arms_test.go`: precedent for an offline test inside the tagged live package.

## Change

`inbandSendTurn` keeps retrying `WriteUserTurn` while it returns `streamsup.ErrNoLiveChild`, and stops writing after the first successful write. It then waits for the result count to rise above the baseline it read on entry, fataling at `inbandTurnBudget` as before. `inbandResendAfter` is deleted.

Why this gives the guarantee: every call writes its prompt exactly once and returns on exactly one new result. The previous call has already consumed its own result, so nothing from an earlier turn is left in claude's queue, and the result that releases a call is the result of the turn that call wrote.

The resend after a successful write existed for evidence run A and #1622's mutant M2, trees that respawn the child and can lose a write into the outgoing child's stdin. Without it such a tree dies in the helper with "produced no result line", which is still a red. The rejected alternative was to resend only when the child that took the write has gone, keyed on `State().ChildPID`. streamsup publishes the new child's stdin before its PID, so a write can land on the new child while the old PID is still reported, and the helper would then write a duplicate. That reintroduces the bug the ticket removes.

Comments updated: the helper's doc states its actual guarantee, the `inbandTurnBudget` comment drops the resend rationale, the revoke header's M2 paragraph gets a dated correction, and the three "first and last" comments stop citing a resent turn as their reason.

No production code changes.

## Testing strategy

New offline test `TestInbandSendTurn_SlowTurnIsWrittenOnce` in `internal/e2e/realclaude/inband_send_turn_test.go`, behind the package's `e2e_realclaude` tag and needing no claude. A test double implements `WriteUserTurn` and `resultCount` as a serial queue like claude's, with no goroutines: each write is answered `latency` after the previous turn's answer. Table rows:

- The first turn takes 46 s, past the 45 s window the old helper resent at, and later turns take 50 ms. After two calls (`one`, `two`), the writes are exactly `[one two]` and the answered turns are exactly `[one two]`. On the old helper `one` is written twice and the second call returns while the answered turns are `[one one]`, so the test fails there.
- No live child for the first 300 ms (`ErrNoLiveChild`): the retry still delivers, with exactly one successful write per call.

The test runs `t.Parallel()` so its 46 s overlaps other parallel tests. Live verification is the dispatcher's `needs-real-claude` gate.
