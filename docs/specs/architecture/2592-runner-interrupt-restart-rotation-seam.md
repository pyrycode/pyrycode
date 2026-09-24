# #2592 — Interrupt, RestartFresh and BeginRotation on sessions.Runner

## Files read

- `internal/sessions/runner.go` → `Runner` — the interface being widened; its doc paragraph "Consumers that need methods off the concrete runner…" says these three "stay off Runner" and must be rewritten. `BeginTeardown`'s doc is the shape the new method docs mirror (#1513, the nearest analogue).
- `cmd/pyry/main.go` → `interruptArm`, `armInterrupt`, `armNone`, `interruptRunner` — the fail-open `Interrupt() error` assertion and its inert arm.
- `cmd/pyry/main.go` → `activeInterrupter.SendEsc` — emits `v2.interrupt.no_actuator` on `armNone`, then `v2.interrupt.dispatched` with the arm string.
- `cmd/pyry/main.go` → `startFreshRunner`, `beginRotationOrNoop`, `installSpawnDir` — the `RestartFresh(string)` and `BeginRotation() func()` assertions, the rotate-error disarm, and the doc cross-references between them.
- `cmd/pyry/main.go` → `activeSessionStarter.StartNewSession` — the second `RestartFresh(string)` probe that emits `v2.new_session.no_restart`.
- `cmd/pyry/session_reset.go` → `conversationReset.wrapUp` — calls `interruptRunner` and tolerates `err != nil || arm == armNone`, logging `reset.wrapup.interrupt_inert`.
- `cmd/pyry/streamsup_runner.go` → `streamRunner.Interrupt`, `RestartFresh`, `BeginRotation` (delegate docs calling the interface "un-widened") and the later forwards `ModelList`, `SlashCommandList`, `BackgroundTaskRoster`, `ModelWindows`, `ClaudeSessionsDir`, whose docs count themselves as the Nth method "OFF" the interface "after Interrupt, RestartFresh, BeginRotation" and justify their assertion "the way interruptRunner already does".
- `internal/relay/v2session_modal.go` → the `v2.interrupt` handler doc naming `v2.interrupt.no_actuator`.
- Test doubles: `baseRunner` (`cmd/pyry/inbound_deliver_rotation_test.go`), `stubRunner` (`cmd/pyry/session_router_test.go`), `fakeRunner` and `lifecycleRunner` (`internal/sessions/runner_test.go`), `raceRunner` (`internal/sessions/session_evict_race_test.go`). Every other double in both packages embeds `baseRunner` or `fakeRunner`. `inbandRunner` (`internal/e2e/realclaude`, build-tagged) embeds `*streamsup.Runner`, which already has all three methods, so it needs no edit.
- Inert-arm tests to delete: `dispatch_arms_test.go` (`sendEscOnlyRunner`, `startNewSessionOnlyRunner`, their two rows and `TestStartFreshRunner_StartNewSessionRunnerIsInert`), `TestActiveInterrupter_BoundRunnerWithNoInterruptMethod`, the `no_restart` row in `new_session_starter_test.go`, `noInterruptRunner` and its row in `session_reset_test.go`.
- `cmd/pyry/streamsup_runner_exit_test.go` — the `RestartFresh` assertion S1040 would flag once the method is on the interface.

In-flight overlap: none (no remote `feature/*` branch touches any of these files).

## Context

Codex support resumed (S0 spike passed). A second runner implementation is coming (#2585). cmd/pyry reaches three runner operations through structural assertions that fail OPEN: a runner lacking the method is silently inert. With one implementation that was speculative-surface avoidance; with two it is a way for the Codex runner to drop an interrupt or a fresh restart without a build error. The `Runner` doc already states the rule for `SetPermissionMode`/`BeginTeardown`: a method whose absence would be a silent no-op belongs on the interface. This ticket applies it to these three. No behaviour change for Claude: `streamRunner` already implements all three.

No ADR needed; this extends the existing "fail-open assertion → interface method" rule the `Runner` doc records.

## Change

### `internal/sessions/runner.go`

Add to `Runner`, each with a doc stating contract and why it is on the interface:

- `Interrupt() error` — ends the running turn on the live child; returns the runner's retryable no-live-child error when nothing is bound. Not idempotence-guarded; callers tolerate errors.
- `RestartFresh(newID string)` — rotates the runner's persistent id to `newID` and respawns so the successor starts a fresh transcript under that id. Signature kept (Codex mints its own thread id; #2585 revisits).
- `BeginRotation() func()` — arms the rotation write-refusal gate ahead of a rotation and returns a non-nil disarm for the rotate-error path. Distinguished from `BeginTeardown` by its release rule (survives the respawn it precedes' Restart/crash respawn — the implementation owns that).

Replace the "Consumers that need methods off the concrete runner … stay off Runner" paragraph with one saying these three are on the interface because a second runner implementation makes a fail-open assertion a silent drop, the same reason `SetPermissionMode` states; other cmd/pyry-only capabilities (`ModelList`, `SetSpawnWorkDir`, …) remain optional assertions.

### `cmd/pyry/main.go`

- `armNone` deleted. `interruptArm`/`armInterrupt` stay — the value is logged as the `arm` field of `v2.interrupt.dispatched` (#1193 record contract) and the surviving `dispatch_arms_test.go` row drives `interruptRunner`. `interruptRunner` becomes `return armInterrupt, r.Interrupt()`; doc rewritten (no assertion, no inert arm).
- `activeInterrupter.SendEsc`: the `arm == armNone` branch and its `v2.interrupt.no_actuator` record are deleted; the dispatched record is unchanged. Its doc's "and a bound runner exposing no interrupt method at all" clause goes.
- `startFreshRunner(r sessions.Runner, …)`: no assertion. Sequence unchanged: `abort := r.BeginRotation()` → `rotate(oldID)`; on error `abort()` and return; `installSpawnDir`; `r.RestartFresh(newID)`. Doc: the inert-return and "OPTIONAL capability" text goes; the ordering rationale stays.
- `beginRotationOrNoop` deleted (its only caller is `startFreshRunner`).
- `installSpawnDir` doc: drop the reference to beginRotationOrNoop and "both existing capability probes"; `SetSpawnWorkDir` stays an optional assertion (out of scope).
- `activeSessionStarter.StartNewSession`: the `RestartFresh` probe and `v2.new_session.no_restart` record deleted, with the comment above it. The no-live-child guard below it stays.

### `cmd/pyry/session_reset.go`

`wrapUp`: `if err := target.runner.Interrupt(); err != nil { Debug … }`. The event name `reset.wrapup.interrupt_inert` is kept (record name stability; nothing but this site references it); the `arm` field is dropped since there is only one arm, and the message says the interrupt failed. The tolerance (log, continue to `WaitIdle`) is unchanged.

### `cmd/pyry/streamsup_runner.go` (comments only)

Rewrite the three delegate docs to say they satisfy `sessions.Runner`. In the later forwards' docs (`ModelList` … `ClaudeSessionsDir`), drop the now-false "Nth method OFF … after Interrupt, RestartFresh, BeginRotation" counting and the "the way interruptRunner already does" analogy; keep each one's actual rule (consumer in cmd/pyry → optional assertion).

### `internal/relay/v2session_modal.go` (comment only)

Drop the `v2.interrupt.no_actuator` alternative from the `v2.interrupt` handler doc.

### Out of scope, noted

Four other cmd/pyry files justify their own optional assertions "the way interruptRunner reaches Interrupt": `session_background_task_list.go`, `session_model_list.go`, `session_model_window_lookup.go`, `session_slash_command_list.go`. Editing them would take the production-file count to 9 against the ticket's 5. After this change the analogy is inaccurate but the rule each paragraph states (consumer outside internal/sessions → assertion) is still correct. Left for a follow-up comment sweep; named in the PR.

## Concurrency model

Unchanged. The calls are the same concrete methods reached through the interface instead of an assertion.

## Error handling

- `rotate` error: `abort()` still disarms the gate before return (pinned by the rotate-error tests in `new_session_reset_test.go` / `rotation_spawndir_test.go`).
- `resolveBoundRunner` still refuses an unbound conversation (`no_bound_runner` row in `active_interrupter_test.go`, unchanged).
- Reset wrap-up still tolerates an `Interrupt` error, logs it, waits for idle (`TestConversationReset_WrapUp_ToleratesAnInertInterrupt`'s "the interrupt errors" row, unchanged).

## Testing strategy

Refactor with no new logic: RED is the compile break when the interface widens (base doubles lack the methods); GREEN is the existing suite. Test edits are exactly:

- Add `Interrupt() error { return nil }`, `RestartFresh(string) {}`, `BeginRotation() func() { return func() {} }` to `baseRunner`, `stubRunner`, `fakeRunner`, `lifecycleRunner`, `raceRunner`.
- Delete the inert-arm tests listed under Files read (and the now-unused stub types/sentinels, plus `dispatch_arms_test.go`'s header text describing them).
- `streamsup_runner_exit_test.go`: the `RestartFresh` assertion becomes `runner.RestartFresh(rotatedID)`.

Behaviour preserved for doubles that override only `RestartFresh` (e.g. `restartFreshRunner`, `asyncRunner`, `movableRunner`): they inherit `baseRunner`'s no-op `BeginRotation`, exactly what `beginRotationOrNoop` returned for them before. `rotatingRunner`'s override still arms its gate, so `TestInboundDeliver_RotationInProductionOrder_DeliversToFreshChild` still exercises the real arming order.

Gate: `go test -race ./cmd/pyry/... ./internal/sessions/... ./internal/relay/...`, `go vet ./...`, `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` (compiles `inbandRunner` against the widened interface), `go build ./cmd/pyry`, staticcheck on the touched packages for S1040.

## Documentation handoff (pending — documentation stage)

- `docs/knowledge/features/v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md` — remove the `no_restart` capability row.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-interrupt-interrupter-seam-esc.md` — remove the `v2.interrupt.no_actuator` record.
- Text saying these methods stay off `Runner` / are reached by type assertion off the "un-widened" `sessions.Runner`: `sessions-package-key-types-runner-interface-runnerfactory.md`, `streamsup-package-per-conversation-turn-busy-track-rotation-delivery-gate.md`, `streamsup-package-content-blocks-are-held-as-json-rawmessage.md` (all under `docs/knowledge/features/`).

## Open questions

- Keep `interruptRunner`/`interruptArm` at one arm, or inline? Resolved: keep — the arm string is a logged record field and the ticket keeps the `dispatch_arms_test.go` actuation row that drives the function.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the untrusted inputs (phone-supplied conversation ids on `v2.interrupt` / `new_session`) are gated exactly as before: `conversations.ValidID` and `resolveBoundRunner` / `resolveBoundSession` in `activeInterrupter.SendEsc` and `activeSessionStarter.StartNewSession`, including the #678 refusal to fall through to the bootstrap session. This change only removes checks that happen *after* a runner is resolved, and those checks could never fire for the one production runner. The no-live-child guard in `StartNewSession` (the #2099 hazard) stays and sits after the deleted probe, so it now runs on every resolved runner rather than only on ones that passed the probe — which is every production runner anyway.
- [Fail-open → fail-closed] The point of the ticket: a future runner lacking any of the three now fails to compile instead of silently dropping an interrupt, rotating nothing, or rotating ungated (the #1330 lost-turn window). No new fail-open path is introduced.
- [Rotation gate / concurrency] No findings — `startFreshRunner` keeps arm → rotate → (disarm on error) → install → RestartFresh; the gate is still armed before `rotate` fires the transition fan-out and still disarmed on rotate error, so a failed rotation from a remote frame cannot wedge the conversation. The removed "arm below the inert return" hazard disappears with the inert return itself.
- [Error messages, logs] No findings — deleted records carried only a validated conversation id; the reset record drops the constant `arm` field and still does not log the error's payload beyond what it logged before (the same `err`). `SendEsc`'s dispatched record is unchanged. No new fields.
- [Tokens, secrets] N/A — no credential, key or token is touched.
- [File operations] N/A — `installSpawnDir` (the only filesystem-adjacent step) is untouched and keeps its optional assertion; `spawnDir` confinement stays at the caller.
- [Subprocess] No findings — `RestartFresh(newID)` passes the pool-minted id as before; the id never comes from the wire.
- [Crypto, Network & I/O] N/A — no wire, framing, or size-limit change.
- [Threat model] No findings — no protocol-mobile surface changes; interrupt and new_session reject paths for unknown/unbound/invalid ids are unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-24
