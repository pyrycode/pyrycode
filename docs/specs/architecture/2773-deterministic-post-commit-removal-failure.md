# #2773 — deterministic post-commit agent-switch removal failure

## Files read

- `cmd/pyry/conversation_agent_switch_test.go` → `TestConversationAgentSwitch_PostCommitRemovalErrorReportsNewID`: the flaky test; the only file this change touches.
- `cmd/pyry/conversation_agent_switch.go` → `conversationAgentSwitcher.Switch`: the post-commit `Pool.Remove(ctx, oldID, …)` whose error the test must observe.
- `internal/sessions/pool.go` → `Pool.Remove`: closes `removedCh`, then returns `Session.Evict(ctx)`'s error.
- `internal/sessions/session.go` → `Session.Evict`: final `select` on `evictedCh` and `ctx.Done()`; the random pick when both are ready is the flake.
- `cmd/pyry/session_model_list_test.go` → `modelListRunner`, `newModelListPlan`: the runner double the test pool uses.
- `cmd/pyry/session_router_test.go` → `stubRunner.Run`: returns as soon as its context is cancelled, which lets `evictedCh` close before `Evict` selects.
- `cmd/pyry/session_revive_test.go` → `runPoolReady`: its cleanup cancels the pool and waits for `Pool.Run`, so the gate must open in a cleanup registered after it (cleanups run LIFO).
- `cmd/pyry/dormant_settings_write_test.go` → `newDormantWritePool`: the fixture the test used; its runner factory is fixed, so the test builds its own pool instead.

## Change

The test builds its pool inline with a runner factory returning a `gatedRunner` (a `modelListRunner` whose `Run` waits for its context to be cancelled and then for a test-owned `release` channel to close). The old session's teardown therefore cannot finish, and `evictedCh` cannot close, while `Switch` runs, so `ctx.Done()` is the only ready case in `Evict`'s `select` and `Remove` always returns the cancelled context's error. A `t.Cleanup` registered after `runPoolReady` closes `release`, so it runs before the pool is cancelled and pool shutdown does not wait on a gated runner. Gating every runner in this pool is safe: nothing else in the test tears a runner down before cleanup. The test drops the dormant-entry fixture and its model-list arm, which this test never read (`Switch` gets nil model and effort, so vocabulary is not consulted for validation). No production code moves; `Session.Evict` keeps reporting success for a removal that has finished, per the ticket's contract decision.

## Testing strategy

The existing assertions stay: `Switch` returns the new ID with a non-nil error, the conversation row holds the new binding with one history entry, and exactly one transition was recorded. Proof of determinism is `go test -race ./cmd/pyry -run '^TestConversationAgentSwitch_PostCommitRemovalErrorReportsNewID$' -count=200`.
