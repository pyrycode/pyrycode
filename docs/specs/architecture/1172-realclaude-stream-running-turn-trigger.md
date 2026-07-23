# Spec #1172 — realclaude stream running-turn trigger helper

**Ticket:** [#1172](https://github.com/pyrycode/pyrycode/issues/1172) — `test(realclaude/stream): running-turn trigger helper for interrupt/resume specs`
**Size:** S (confirmed; test-only, zero production `.go` files, one new `*_test.go`, ~4 package-internal helpers, no exported types).
**Security:** NOT `security-sensitive` (no label). Keep-a-turn-busy test infra; no modal, no device gate, no untrusted input. Security-review step skipped per label gate.
**Consumed by:** [#1176](https://github.com/pyrycode/pyrycode/issues/1176) (interrupt), which is already natively `blocked-by` this ticket. Part of T9 (#1083) stream real-claude coverage.

## Context

The stream-json interactive runner (`internal/streamsup`) has only two real-claude e2e specs — `interactive_stream_liveness` (#1153, one turn) and `interactive_stream_modal_resolution` (#1154, one gated turn). The interrupt spec (#1176) needs a turn **genuinely in flight** — one that has entered `turn_state{responding}` and will stay there for a bounded window — to prove the interrupt envelope stops a REAL claude turn mid-stream. A fake `sleep` won't do: as the desktop interrupt fix found (`e1fe219`, 2026-07-17), a silent `sleep` gets backgrounded by claude (the turn ends early), and a chatty per-iteration loop floods the frame stream and delays the `turn_state` events the consumer gates on. The working approach drove the turn with a **foreground, bounded, non-chatty shell loop inside a single Bash-tool call**.

This ticket ports that approach into the daemon realclaude stream harness (`internal/e2e/realclaude/`) as **reusable trigger infra** plus a smoke that proves the infra works. Infra only — no interrupt or resume behaviour is asserted here.

### The load-bearing mechanism (why a Bash loop holds `responding`)

The interactive turn emitter derives `turn_state` statefully (`cmd/pyry/interactive_turn_v2.go`):

- `ToolStart` → `transitionTo(StateResponding)` (`interactive_turn_v2.go:199`) — emitted the instant claude issues the Bash tool call, **before** the command executes.
- During the command's execution the emitter emits nothing new (`currentState == responding`, de-duped at `transitionTo`, `:276`).
- `TurnEnd` → `transitionTo(StateIdle)` (`:216`) — the only source of `idle`, emitted once at the very end of the whole turn (after the tool completes and claude replies).

So for a turn that runs one Bash loop of `L` seconds, the wire sequence is: (optional `thinking`) → **`responding`** (at tool start) → [loop runs `L`s, no new turn_state] → `assistant_delta` (the reply) → **`idle`** (at turn end, ~`L`s after `responding`). Observing `responding` and then verifying no `idle` for a window `hold < L` is exactly the running-turn proof. If claude backgrounds the command or refuses to loop, `idle` arrives early → the smoke fails loud (never a silent pass).

Because we do NOT want a permission modal to block the Bash call, the daemon is spawned via **`spawnBootstrapDaemon`** (which passes `--dangerously-skip-permissions`, `interactive_bootstrap_liveness_test.go:398`), NOT `spawnPermissionDaemon`. This mirrors `interactive_stream_liveness` and is the opposite posture from the modal specs (#1030/#1154), which deliberately drop the flag to raise a modal.

## Files to read first

- `internal/e2e/realclaude/interactive_stream_liveness_test.go` (#1153) — **the template.** Transcribe its `TestInteractiveStreamLiveness` setup body (pair → seed → spawn → dial → handshake) into the new harness. Reuse `writeStreamInteractiveConfig` (`:155`) and `drainForCompletedTurn` shape (`:181`) as the drain templates.
- `internal/e2e/realclaude/interactive_turn_state_liveness_test.go:70-126` — `drainForTurnState`. The new `drainForResponding` **mirrors** this (in-order decrypt discipline, non-noise_msg skip-without-decrypt), narrowed to `state == "responding"` and returning the observation time.
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go:120-200` — `startStreamModalResolutionHarness`. Copy its shape for the new harness; swap `spawnPermissionDaemon`→`spawnBootstrapDaemon` and drop `--allow-remote-permissions` from the pair (no answer path here).
- `internal/e2e/realclaude/interactive_bootstrap_liveness_test.go` — helper defs reused verbatim: `sealSendMessage` (`:160`), `driveHandshakeInteractive` (`:247`, returns `(initSend, initRecv *noise.CipherState)`), `spawnBootstrapDaemon` (`:383`, note the `--dangerously-skip-permissions` at `:398`), `seedBootstrapRegistry` (`:529`), `seedBoundConversation` (`:546`).
- `internal/e2e/realclaude/interactive_modal_resolution_test.go:181-213` — `writeFileTrigger` / `raiseRealPermissionModal`: the prompt-crafting idiom to mirror for the busy-loop trigger prompt (per-run nonce, "reply with a single short word" tail).
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go:85,140-146` — `perTurnReplyBudget = 120*time.Second` (reuse for the responding-wait phase) and the `perConvHarness` struct (`{phone, initSend, initRecv, home, workdir}`) the new harness returns.
- `cmd/pyry/interactive_turn_v2.go:162-216` — the `ToolStart→responding` / `TurnEnd→idle` state machine (the load-bearing mechanism above). Read-only; do not touch.
- `internal/protocol/interactive.go:22-25` — `TurnStatePayload{ConversationID, State string}` — the envelope the drains decode.

## Design

**One new file:** `internal/e2e/realclaude/interactive_stream_running_turn_test.go` (build tag `//go:build e2e_realclaude`, package `realclaude`). Placement alone wires the smoke into `make e2e-realclaude` via the build tag + package glob — no Makefile change (AC4), mirroring `interactive_stream_liveness`.

No production code. No edits to any existing file (avoids the feature/363 `fixtures.go` overlap entirely — this spec neither reads nor writes it).

### Constants (fresh names + distinct literals — the package must not redeclare)

- `runningTurnBootstrapUUID` — the bootstrap session POOL id (seeded via `seedBootstrapRegistry`). Suggested `"99999999-9999-4999-8999-999999999999"`.
- `runningTurnConvID` — the driving conversation, bound via `seedBoundConversation`. Suggested `"33333333-3333-4333-8333-333333333333"`.
  Both must be UUID-v4-shaped and distinct from every existing literal in the package (`streamBootstrapUUID`/`streamConvID`, `streamModalBootstrapUUID`/`streamModalConvID`, `liveBootstrapUUID`/`liveConvID`, `liveModalBootstrapUUID`/`liveModalConvID`). Each test owns its own tempdir HOME so literals never collide on disk; distinct values only keep cross-test confusion impossible.
- `runningTurnHold = 20 * time.Second` — the minimum window the turn must remain in `responding` after it is first observed. Chosen ≫ #1176's interrupt round-trip (phone→relay→daemon→claude interrupt is sub-second), with wide margin.
- `runningTurnLoopSlack = 20 * time.Second` — extra Bash-loop wall-clock beyond `hold`, so `idle` cannot race the window boundary (the loop is still running comfortably at `firstResponding + hold`).

**Coupling constraint (state it in a comment):** the Bash loop's wall-clock `L = hold + slack` must satisfy `L > hold` (so no early idle) AND `L < 120s` (the claude Bash-tool default timeout — a loop that hits it ends the turn early). `20+20 = 40s` satisfies both. If #1176 later needs a longer in-flight window, bump `runningTurnHold`, keeping `L < 120s`.

- Reuse the existing `perTurnReplyBudget` (120s) as the timeout for the "wait for `responding`" phase — cold claude spawn + model load, not fakeclaude milliseconds.

### Helpers (all lowercase, package-internal; no exported types)

**1. `startStreamRunningTurnHarness(t *testing.T) (*perConvHarness, string)`** — the reusable stream-bootstrap harness. Transcribe `TestInteractiveStreamLiveness`'s setup body with these exact choices:
- `WithWorktreeAuthenticated(t)` (skips cleanly when claude / creds absent) + isolated `<home>/work` workdir.
- `writeStreamInteractiveConfig(t, home)` **before** spawn (the stream-json toggle — the seam this whole family exists to exercise).
- `runPyry(t, "pair", "-pyry-name=test", "--name=phone-a")` — **WITHOUT** `--allow-remote-permissions` (no answer path; contrast the modal harness).
- `seedBootstrapRegistry(t, home, runningTurnBootstrapUUID)` + `seedBoundConversation(t, home, runningTurnConvID, runningTurnBootstrapUUID, workdir)`.
- `spawnBootstrapDaemon(...)` (**skip-permissions** — Bash runs with no modal).
- `readPersistedServerID` → `waitBinaryHello` → `fakephone.Dial` → `driveHandshakeInteractive`.
- Returns `&perConvHarness{phone, initSend, initRecv, home, workdir}` and `runningTurnConvID`.

Flag it in a doc comment as the reusable seam **#1176 composes** (same package), exactly as #1153 flagged `writeStreamInteractiveConfig` for #1154.

**2. `driveRunningTurn(t *testing.T, h *perConvHarness, reqID uint64, convID string, hold time.Duration)`** — the trigger. Computes `loopSeconds := int((hold + runningTurnLoopSlack).Seconds())`, mints a per-run nonce, and `sealSendMessage(t, h.phone, h.initSend, reqID, convID, fmt.Sprintf("m-%d", reqID), runningTurnPrompt(loopSeconds, nonce))`. Reusable by #1176.

**3. `runningTurnPrompt(loopSeconds int, nonce int64) string`** — the busy-loop prompt (the port of desktop `e1fe219`). Signature + required properties, not a fixed string; the developer writes the exact wording (align with `e1fe219` if the desktop repo is reachable). It MUST:
- force exactly one Bash-tool call running a **bounded, silent shell loop** — `for i in $(seq 1 <loopSeconds>); do sleep 1; done` (a loop, NOT a bare `sleep <L>` which claude backgrounds; silent per-iteration, NOT a chatty echo loop that floods frames);
- instruct claude to **wait for it in the foreground — explicitly "do not run it in the background"**;
- end with "reply with a single short word" (guarantees a terminal turn once the loop ends; the word is never asserted);
- embed `run=<nonce>` to defeat accidental caching.
- No content/echo assertion anywhere — real claude's output is non-deterministic.

**4. `drainForResponding(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, timeout time.Duration) time.Time`** — mirrors `drainForTurnState` (`interactive_turn_state_liveness_test.go:78`) with two changes: it returns on `state == "responding"` **only** (skips `thinking` and every other envelope, decrypting each in receive order to keep the nonce in sync; non-`noise_msg` control frames skipped WITHOUT decrypting), and it returns `time.Now()` at the moment `responding` for `convID` is first observed. On the deadline it `t.Fatalf`s naming the likely cause (the trigger never drove the turn into `responding` — most often a UUID mismatch between the two seeds hanging the drain). Reusable by #1176 (which fires its interrupt right after `responding`).

**5. `assertNoIdleWithin(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, since time.Time, hold time.Duration)`** — smoke-only verifier. Same in-order decrypt discipline. Loop:
- top: if `time.Now()` is at/after `since.Add(hold)` → **return (success)** — the window elapsed with no idle, the turn is still running;
- else `ReceiveBytes(remaining)`; on `ErrReceiveTimeout` → `continue` (re-loops to the success check — a silent loop produces no frames, and that IS the success path);
- on a decoded `turn_state{idle}` for `convID` arriving **before** `since+hold` → `t.Fatalf` (the turn ended early — the loop was backgrounded, refused, or too short; AC3);
- all other envelopes (interleaved `responding`, `assistant_delta`, `tool_use`) are decrypted and skipped.

This is `drainForCompletedTurn`'s structure inverted: there, receive-timeout = failure and `idle` = success; here, `idle` = failure and window-elapsed = success.

### Test

`TestInteractiveStreamRunningTurn(t *testing.T)` — no `t.Parallel` (`WithWorktreeAuthenticated` calls `t.Setenv`). Steps:
1. `h, convID := startStreamRunningTurnHarness(t)`.
2. `driveRunningTurn(t, h, 2, convID, runningTurnHold)` — send id `2`, the first post-handshake message (mirrors the liveness/modal specs).
3. `t0 := drainForResponding(t, h.phone, h.initRecv, convID, perTurnReplyBudget)` — AC1/AC3: `responding` observed, fail-loud if never.
4. `assertNoIdleWithin(t, h.phone, h.initRecv, convID, t0, runningTurnHold)` — AC2/AC3: no terminal `idle` for `runningTurnHold` after `responding` first seen; fail-loud on early idle.

The `h.initRecv` CipherState threads through steps 3→4 sequentially — the nonce stays continuous across the two drains (step 4 resumes exactly where step 3 left off).

## Concurrency model

Single test goroutine drives the phone synchronously; the daemon (child claude under the stream-json runner) runs out-of-process. No goroutines spawned in the test. The one ordering invariant is the receive-nonce discipline (decrypt every `noise_msg` in order, skip non-`noise_msg` control frames without decrypting) — inherited verbatim from `drainForTurnState` / `drainForCompletedTurn`.

## Error handling / failure modes

- **`responding` never observed** → `drainForResponding` `t.Fatalf` at `perTurnReplyBudget` (delivery never reached claude, or a seed UUID mismatch dropped every event).
- **`idle` before the window** → `assertNoIdleWithin` `t.Fatalf` (claude backgrounded/refused the loop, or `L` was miscomputed). AC3.
- **No creds / no claude** → `WithWorktreeAuthenticated` / `exec.LookPath` skip cleanly (a skip exits 0; the `needs-real-claude` label parks the ticket for an operator run — the dispatcher cannot green this).
- **Turn still running at test end** — the smoke returns with the Bash loop mid-flight; `t.Cleanup(d.stop)` SIGTERMs the daemon, which tears down claude and the loop. No leak (same teardown the liveness/modal specs rely on).

## Testing strategy

The file IS the test. Its non-vacuity is structural: `drainForResponding` fails loud if `responding` never appears, and `assertNoIdleWithin` fails loud on an early `idle` — neither greens without a genuinely held real-claude turn. Like #854/#997/#1030/#1153/#1154 this is a standing real-claude liveness gate in preship, not a deterministic RED/GREEN oracle; the deterministic held-turn shape lives on the fake side (#794, `TestRelayV2_StreamInterruptStopsRunningTurn`, which scripts a held turn via a JSONL trigger — real claude can't be scripted, hence the foreground-loop prompt here). Verify locally with `make e2e-realclaude` on an authenticated Mac; without creds it skips (expected in CI/dispatch).

## Open questions

- **Exact `runningTurnHold` for #1176.** Pinned at 20s here (≫ interrupt round-trip). #1176's architect confirms the interrupt window fits comfortably under it; if not, bump `runningTurnHold` keeping `hold + runningTurnLoopSlack < 120s`. Not a blocker for this infra ticket.
- **Prompt wording drift.** `runningTurnPrompt` ports `e1fe219`'s recipe by its properties (bounded foreground silent loop). If claude (haiku) proves flaky at reliably issuing the loop, tighten the wording — the failure mode is a loud AC3 fatal, never a false green, so drift surfaces safely.
