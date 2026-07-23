# Spec — #1176 realclaude/stream: interrupt stops a running live-claude turn

**Size:** S · **Security-sensitive:** no · **Production code:** none (one new `_test.go`)

## Files to read first

Read these before writing a line. Line ranges are the exact seams this spec composes.

- `internal/e2e/realclaude/interactive_stream_running_turn_test.go` (whole file, ~356 lines) — **the #1172 reusable seam.** Lift `startStreamRunningTurnHarness` (`:132-199`), `driveRunningTurn` (`:208-214`), `runningTurnPrompt` (`:225-230`), `drainForResponding` (`:242-290`), and the constants `runningTurnHold`/`runningTurnLoopSlack` (`:97-100`). The header doc (`:5-50`) explains *why a Bash loop holds `responding`* — that mechanism is what puts a genuine live turn in flight for you to interrupt. You compose these verbatim; you add ONE new drain helper + one test fn on top.
- `internal/e2e/relay_v2_stream_interrupt_test.go` (whole file, ~299 lines) — **the fakeclaude behaviour reference.** The M1→interrupt→M2 shape (`:221-298`), the payload-less interrupt send (`:258-262`), and the vacuous-pass guard (`:59-66`, `:288-296`: assert `turn_end.StopReason == "cancelled"`; a spontaneous end is `"end_turn"`). Your real-claude test mirrors the *guard*, not the fake's "no result on the user turn" causality (real claude WILL end naturally at ~40s if the interrupt no-ops — the reason check catches that).
- `internal/e2e/realclaude/interactive_stream_liveness_test.go:181-260` — `drainForCompletedTurn(t, phone, cs, convID, timeout)`: the two-milestone drain (non-empty `assistant_delta` M1 → terminal `turn_state{idle}` M2). **Reuse it verbatim for AC4** (the post-interrupt health turn). Model your new turn_end drain on its in-order-decrypt structure.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go:81-85` (`perTurnReplyBudget = 120s`), `:140-158` (`type perConvHarness` fields: `phone`, `initSend`, `initRecv`, `home`, `workdir`), `:259-280` (`sealEnvelope(t, phone, cs, env)` — seals+sends a raw envelope; this is how you fire the interrupt).
- `internal/e2e/realclaude/interactive_bootstrap_liveness_test.go:160` — `sealSendMessage(t, phone, cs, id, convID, msgID, text)` signature (used indirectly via `driveRunningTurn`; no direct call needed).
- `internal/protocol/interactive.go:73-78` — `TurnEndPayload{ConversationID, TurnID, StopReason}`; `internal/protocol/codes.go:185` — `TypeTurnEnd = "turn_end"`. `StopReason` carries the `turnevent.TurnEndReason` string verbatim; `"cancelled"` is the interrupt outcome.
- `internal/streamsup/parser.go:154-178` — `error_during_execution → TurnEndReasonCancelled`; every other subtype → `end_turn`. This is the production classification your live test proves against real claude.
- `cmd/pyry/interactive_turn_v2.go:208-217` — the `TurnEnd` emitter arm: emits `turn_end{StopReason}` **then** `turn_state{idle}` **then** `endTurn()` (resets `inTurn`/`currentState`). The state reset is why AC4's subsequent turn is healthy. (Read-only context — do not touch.)
- `internal/protocol/messaging.go` / `codes.go` — `TypeInterrupt` constant (the interrupt envelope Type). Grep it; it is the same constant the fakeclaude analog sends.

## Context

Interrupt-stops-a-running-turn is covered end-to-end only against a scripted fakeclaude (`TestRelayV2_StreamInterruptStopsRunningTurn`, #1136). The production pieces are shipped and unit-tested independently — the `streamsup.Runner.Interrupt()` primitive (#1120), per-conversation routing via `activeInterrupter → resolveBoundRunner` (#1121), and the parser's `error_during_execution → cancelled` classification (#1120) — but whether an interrupt envelope actually stops a **real** claude turn mid-stream, with the parser classifying real claude's interrupt-terminated result as `cancelled`, is unproven live. This is the recurring fake-green/real-red risk (#949) applied to the interrupt path.

#1172 (merged, PR #1178) shipped the reusable running-turn trigger: a harness that holds a live claude turn in `turn_state{responding}` for a bounded window via a silent foreground Bash loop, plus `drainForResponding`. This ticket composes that trigger with the already-shipped interrupt primitive to add the one missing real-claude behaviour: **interrupt cancels a running turn and leaves the session healthy.** Part of #1083 (T9) stream real-claude coverage; natively blocked-by #1172 (now landed).

**No production code changes.** One new `_test.go` in `internal/e2e/realclaude/`, wired into `make e2e-realclaude` by the package glob (no Makefile change), following the `interactive_stream_liveness` / `interactive_stream_running_turn` mold.

## Design

### New file

`internal/e2e/realclaude/interactive_stream_interrupt_test.go`

```
//go:build e2e_realclaude
package realclaude
```

Contents (additive, leaf — reuses every harness symbol from #1172/#1153, redeclares nothing):

1. **One new drain helper** — call it `drainForCancelledTurnEnd` (distinct name; see § Symbol-collision below). Contract:

   `drainForCancelledTurnEnd(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, timeout time.Duration)`

   Reads binary→phone `noise_msg` frames in receive order, decrypting **every** `noise_msg` in sequence (keeps the receive nonce in sync — same discipline as `drainForResponding`); skips non-`noise_msg` inner frames without decrypting; skips every decrypted envelope whose `Type != TypeTurnEnd`. On the **first** `turn_end` for `convID`:
   - `StopReason == "cancelled"` → success, return.
   - `StopReason != "cancelled"` (e.g. `"end_turn"`) → `t.Fatalf` **immediately** — the turn ran to completion, the interrupt did not cancel it. This is the vacuous-pass guard (mirrors the fakeclaude analog `:288-296`): the first terminal event of the running turn must be `cancelled`, not a spontaneous end.
   - On deadline → `t.Fatalf` naming the likely cause (interrupt never routed to the running turn's bound runner, or real claude never emitted `error_during_execution`).

   Model the loop body on `drainForResponding` (`interactive_stream_running_turn_test.go:242-290`) — same inner-frame / decrypt / envelope-type gating — retargeted from `turn_state{responding}` to `turn_end`.

2. **One test fn** — `TestInteractiveStreamInterruptStopsRunningTurn(t *testing.T)`. Composes the seam:

   - `h, convID := startStreamRunningTurnHarness(t)` — stands up the real stack under `interactive_runner:"stream-json"`, spawns via `spawnBootstrapDaemon` (`--dangerously-skip-permissions`, so no modal blocks the Bash loop), returns the seeded bootstrap-bound conversation id. Skips cleanly with no claude/creds.
   - `driveRunningTurn(t, h, 2, convID, runningTurnHold)` — sends one `send_message` (reqID 2) whose prompt forces a silent foreground Bash loop of `L = runningTurnHold + runningTurnLoopSlack = 40s`. The turn enters `responding` at ToolStart and stays there for ~40s. **Reuse `runningTurnHold` as-is** — no new timing constant.
   - `drainForResponding(t, h.phone, h.initRecv, convID, perTurnReplyBudget)` — blocks until `turn_state{responding}` for `convID`. Its return is the AC1 signal: the turn is genuinely mid-stream (Bash tool running), and by in-order-drain construction **no `turn_state{idle}` has been observed yet**.
   - **Immediately** (no intervening wait) send the payload-less interrupt:
     `sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{ID: 3, Type: protocol.TypeInterrupt, TS: time.Now().UTC()})`.
     Payload-less → routes to the **active** conversation (stamped `convID` by the `send_message` above) → `activeInterrupter.SendEsc()` → `resolveBoundRunner(convID)` → the bootstrap runner that is running the live turn → `streamsup.Runner.Interrupt()` → `control_request` to the real claude child. AC2.
   - `drainForCancelledTurnEnd(t, h.phone, h.initRecv, convID, perTurnReplyBudget)` — asserts `turn_end{StopReason:"cancelled"}` for `convID`. AC3.
   - `driveRunningTurn` is NOT reused for the health turn (it would launch another 40s loop). Instead drive a trivial turn: `sealSendMessage(t, h.phone, h.initSend, 4, convID, "m-4", fmt.Sprintf("Reply with a single short word. run=%d", time.Now().UnixNano()))`, then `drainForCompletedTurn(t, h.phone, h.initRecv, convID, perTurnReplyBudget)` — non-empty `assistant_delta` M1 then terminal `turn_state{idle}` M2. AC4. (Match `interactive_stream_liveness_test.go:138-141`'s prompt shape.)

3. **No new package-level constants.** Reuse `runningTurnConvID`/`runningTurnBootstrapUUID` (via the harness return — never redeclare), `runningTurnHold`, `perTurnReplyBudget`. reqIDs 2/3/4 are function-local literals. **No new UUID literal → no cross-file literal collision.**

### Wire sequence (single phone reader, `h.initRecv` threaded continuously)

```
send_message(convID, reqID 2, 40s-loop prompt)
  → real claude issues Bash tool  → turn_state{responding}          ── drainForResponding → t0 (AC1)
  → [Bash loop runs silently ~40s, no new turn_state]
interrupt(reqID 3, no payload)  → active=convID → resolveBoundRunner(convID)
  → streamsup.Runner.Interrupt() → control_request → real claude cancels the Bash tool
  → result{subtype: error_during_execution} → parser: TurnEnd{cancelled}
  → turn_end{StopReason:"cancelled", convID}                        ── drainForCancelledTurnEnd (AC3)
  → turn_state{idle}  (drained-and-skipped by the next drain, before its M1)
send_message(convID, reqID 4, "single short word")
  → turn_state{responding} → assistant_delta(non-empty) → turn_end{end_turn} → turn_state{idle}
                                                                    ── drainForCompletedTurn (AC4)
```

The receive nonce (`h.initRecv`) advances continuously across all three drains: each resumes exactly where the prior left off (identical to how #1172 threads `drainForResponding` into `assertNoIdleWithin`). Any interrupt ack or `turn_state{idle}` leftover from the cancelled turn is a non-`turn_end`/non-milestone envelope → decrypted-and-skipped by the next drain, harmless.

### Routing scope decision (why bootstrap-bound, not a minted target)

The fakeclaude analog **mints** a separate conversation so the interrupt target differs from bootstrap, making its M2 a live *isolation* proof (interrupt must NOT hit the idle bootstrap). This spec deliberately does **not** mint: it composes the #1172 harness, whose driving conversation (`runningTurnConvID`) is bound to the bootstrap pool id, so the running turn executes on the bootstrap runner and the payload-less interrupt routes there via the active cursor.

Rationale: (a) #1172 explicitly flags `startStreamRunningTurnHarness`/`driveRunningTurn`/`drainForResponding` as *the* reusable seam for this ticket — composing them is the intended path, and minting would require a second runner + second real-claude child spawn, a heavier and different harness. (b) AC2 requires the interrupt to reach the *running turn's bound runner* — which it does — not to prove cross-conversation isolation. (c) Cross-conversation routing isolation is unit-owned deterministically by #1121 (`interrupt_routing_test.go`) — that is the belt; this e2e is the different-fabric suspenders confirming the wired path runs live against real claude. This is a documented scope choice, not a gap.

### Interactive capability

The interrupt frame requires the interactive capability (same as the structured stream). `startStreamRunningTurnHarness` completes `driveHandshakeInteractive` and already drives turns, so the capability is established before the interrupt is sent — no extra handshake step. (Confirmed against the fakeclaude analog `relay_v2_stream_interrupt_test.go:112-113`, which uses the same interactive handshake for both send and interrupt.)

### Symbol-collision hazard (cross-branch)

Siblings in-flight in the same package add differently-named files (#1173 `…multiturn_continuity`, #1174 `…new_session`, #1175 `…permission_deny`). Net-new distinct file paths → no git merge conflict. The only merge-time risk is a Go symbol/literal collision when two files land on `main`. Neutralize it:
- New drain helper name **must not** match any sibling drain (`drainForCompletedTurnText` #1173, `drainForControlEvent` #1174, `drainForTurnIdle` #1175). `drainForCancelledTurnEnd` is distinct — verify with `grep -rn "func drainForCancelledTurnEnd" internal/e2e/realclaude/` returns nothing before committing.
- Add **no** new package-level `const`/`var` (reuse the harness's). This sidesteps the UUID-literal-collision class entirely.

## Concurrency model

None introduced. Single phone reader; the in-order `noise_msg` decrypt discipline (decrypt every `noise_msg` to keep the `CipherState` receive nonce in sync; skip non-`noise_msg` inner frames without decrypting) is the only invariant — inherited unchanged from `drainForResponding`/`drainForCompletedTurn`. All timing is wall-clock deadline via `perTurnReplyBudget` / `runningTurnHold`.

## Error handling

Fail-loud drains only (no recovery — this is a liveness gate):

- `drainForResponding` deadline → the trigger never drove `responding` (delivery never reached the child, or a seed UUID mismatch). Inherited message.
- `drainForCancelledTurnEnd`: first `turn_end{convID}` with `StopReason != "cancelled"` → `t.Fatalf` (interrupt did not cancel — turn ran to completion). Deadline → `t.Fatalf` (interrupt never routed to the bound runner, or real claude never emitted `error_during_execution`). Both messages must name the likely cause, per the package convention.
- `drainForCompletedTurn` M1/M2 deadlines → inherited (turn opened but never closed post-interrupt → session unhealthy).
- No-claude / no-creds → `startStreamRunningTurnHarness` SKIPs cleanly (`exec.LookPath` + `WithWorktreeAuthenticated`). AC5.

## Testing strategy

The test IS the deliverable. Verification the developer performs (creds absent locally → the suite SKIPs, so green is proven at compile + SKIP-exit-0, not live green):

- `go build ./... && go vet ./...` clean.
- `go test -race -tags e2e_realclaude ./internal/e2e/realclaude/ -run TestInteractiveStreamInterruptStopsRunningTurn` **compiles and SKIPs** (exit 0) with no claude login — the compile under `e2e_realclaude` proves no redeclaration / signature / missing-symbol error against the whole tagged package (the merge-collision guard).
- `grep -rn "func drainForCancelledTurnEnd" internal/e2e/realclaude/` returns exactly one hit (the new one) — no sibling collision.
- Live green is operator-run under `make e2e-realclaude` with a Claude login; the ticket carries `needs-real-claude` and is operator-gated (AC5). Do not assert green off a sub-second SKIP.

**Never assert on turn content** — real claude's words are non-deterministic. Assertions are on `turn_state`/`turn_end` transitions and `StopReason` only, exactly as #1172/#1153/#1136 established.

Milestone → AC map:
- **AC1** — `drainForResponding` returns (turn `responding`, no `idle` yet by drain ordering); the interrupt is sent immediately after, so the responding→interrupt window is sub-second, deep inside the 40s loop.
- **AC2** — payload-less `TypeInterrupt` → active cursor `convID` → `resolveBoundRunner` → running turn's bound runner.
- **AC3** — `drainForCancelledTurnEnd` asserts `turn_end{cancelled}`, fails on the first non-cancelled `turn_end` (spontaneous-end guard); ordering enforced by sequencing (responding drained → interrupt sent → turn_end drained).
- **AC4** — trivial second turn drains to terminal `idle` via `drainForCompletedTurn`.
- **AC5** — `//go:build e2e_realclaude`, `interactive_runner:"stream-json"` (via `writeStreamInteractiveConfig` inside the harness), SKIP without login, `needs-real-claude`.

## Open questions

- **Real claude's interrupt classification.** The whole point of this live gate is to prove real claude emits `error_during_execution` (→ `cancelled`) when interrupted mid-Bash-tool. If real claude instead emits a different result subtype, `drainForCancelledTurnEnd` fails loud on the first non-`cancelled` `turn_end` — which is the correct signal (surfaces a real-claude-specific gap the parser must then handle), not a test bug. Resolve at operator run-time, not implementation time.
- **Stop latency vs loop length.** `L = 40s` gives enormous margin over the sub-second interrupt round-trip plus real claude's tool-cancellation latency. No hard timing assertion on stop latency is specified (avoids flakiness); the `StopReason == "cancelled"` check is the sufficient oracle — a natural loop completion at ~40s reports `end_turn`, which the guard rejects. If operator runs show real claude's stop latency approaching `L`, bump `runningTurnHold` (keeping `runningTurnHold + runningTurnLoopSlack < 120s`, the Bash-tool default timeout).
