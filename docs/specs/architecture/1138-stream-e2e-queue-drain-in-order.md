# Spec — #1138: Stream e2e — queued sends drain in submission order under the `interactive_runner` toggle

**Size:** S (confirmed — 1 production source file touched: the `fakeclaude` test binary; 1 new `_test.go`)
**Security-sensitive:** No (test-only; no production `internal/` surface added — see § Security)
**Split from:** #1082. **Rides:** #1140 (fakeclaude stream-json mode) + #1141 (`StartStreamInteractiveWithRelay` harness helper + first live stream send proof).

---

## Files to read first

| Path (line range) | What to extract |
|---|---|
| `internal/e2e/relay_v2_stream_send_test.go` (whole, 218 lines) | **The primary template.** `TestRelayV2_StreamSendMessageDrainsTurn` — pair → `seedBoundConversation` → `StartStreamInteractiveWithRelay` → `driveHandshakeToOpenDaemonInteractive` → send → ack → drain milestones. Copy this skeleton; extend from 1 send to 3 ordered sends + a hold trigger. |
| `internal/e2e/relay_v2_queue_drain_test.go` (whole, 349 lines) | **The PTY sibling to model the flow on** (per ticket). Lift its structure: come-up-busy → accumulate → release trigger → drain-in-order, and its **vacuity-guard discipline** (positives gate before the ordering assertion; `t.Fatal` on the harness-produced-no-queue mode). Also the ASCII-marker header note (no secrets in markers). AC3 requires this file stays **untouched**. |
| `internal/e2e/harness.go:388-435` | `StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL, extraEnv ...string)` — writes the `interactive_runner:"stream-json"` config toggle + `PYRY_FAKE_CLAUDE_STREAM_JSON=1`, and forwards `extraEnv` to the spawned child. The hold-trigger env rides `extraEnv`; **no harness change needed.** |
| `internal/e2e/internal/fakeclaude/main.go:460-471` | The stream-mode branch in `main()` — where the startup hold `if` goes, before `runStreamJSON`. `PYRY_FAKE_CLAUDE_*` envs propagate to the child (daemon inherits + passes them). |
| `internal/e2e/internal/fakeclaude/main.go:540-560, 645` | The existing trigger-poll pattern (`os.Stat(trig)` + `time.Sleep(pollInterval)`). Mirror it for the hold wait. Confirm `pollInterval` const. |
| `internal/e2e/internal/fakeclaude/main.go:1259-1303` | `writeStreamResponse` / `writeAssistantEcho` / `writeInterruptedResult` — the per-turn write helpers. The startup hold does **not** touch these (it holds before `runStreamJSON` runs at all). |
| `internal/streamsup/envelope.go:126-172` | `WriteTurn` — writes the user envelope to the held-open stdin and **returns immediately**; no commit block, no idle wait. The load-bearing production fact below. |
| `cmd/pyry/main.go:854-899` | msgqueue wiring: `send_message` enqueues, **one serial drain goroutine per conversation** delivers FIFO. All 3 test sends must target the **same** conversation for the serial drain to order them. |
| `cmd/pyry/main.go:1413-1446` | `newInboundDeliver` — `WriteUserTurn` written with the raw ctx; the comment claims "return nil only on a confirmed commit," but on the stream path `WriteTurn` returns on write. This gap is why a msgqueue backlog can't form on the stream path (§ Open questions). |
| `cmd/pyry/interactive_turn_v2.go:170-234` | Emitter state machine: `TextChunk` → `responding` + buffered delta; `TurnEnd` → `flushDelta` + `turn_end` + `idle` + `endTurn`. Each `(assistant, result)` pair ⟹ one clean `responding→delta→turn_end→idle` cycle; a fresh chunk after `idle` opens a **new** turn (`startTurnIfNeeded` mints a new turn id). This is why 3 ordered deltas ⟹ 3 ordered turn cycles. |
| `cmd/pyry/stream_turn_drain_test.go:100, 183-220` | `assistantTextLine` / `resultLine` shapes + `TestStreamTurnDrainV2_FullSingleTurn`'s expected envelope order (`responding, assistant_delta, turn_end, idle`) for one turn. |

---

## Context

`interactive_runner:"stream-json"` is now live end-to-end (#1081 toggle, #1098/#1109 drain, #1140 fake, #1141 first live send). This ticket adds the **queue-ordering** proof: multiple sends submitted while a turn is in flight drain to the client in submission order, under the toggle, against the fakeclaude stream-json harness. It is the stream analog of the PTY sibling `relay_v2_queue_drain_test.go`.

### The load-bearing production fact (the whole design turns on this)

**The stream path does not pace on commit.** `streamsup.WriteTurn` (`envelope.go:149-172`) writes the user-turn envelope to the child's held-open stdin and returns `nil` immediately — no `WaitReady`, no transcript-growth confirm (contrast the PTY path's `supervisor.WriteUserTurn`). Therefore `newInboundDeliver`'s `w.WriteUserTurn(...)` returns on **write**, not on turn completion, and the msgqueue's serial per-conversation drain empties the backlog as fast as it can write bytes into the pipe.

**Consequence for the test design:**

- A msgqueue backlog of N is **not** reliably observable on the stream path (unlike the PTY sibling, whose idle-trigger blocks `WaitReady` and parks the drain so `queue_state` shows `[m1,m2,m3]`). Do **not** assert on `queue_state` depth here — it is racy.
- Submission order is instead preserved by a chain of FIFO/serial stages: **FIFO drain → FIFO held-open stdin pipe → serial claude reader → FIFO stdout → serial parser → serial emitter.** The queued sends accumulate in the child's **stdin pipe**, not the msgqueue. **This chain is what the test proves.**
- To make accumulation deterministic and observable, the fake must be held **before it starts consuming stdin**: the child "comes up busy," the daemon delivers all queued turns into the pipe buffer during the hold, then a release trigger lets the child drain them in FIFO order. This is the faithful, minimal stream analog of the PTY sibling's "come up busy → accumulate → release → drain."

---

## Design

Reuse everything from #1140/#1141. Two deliverables: a **minimal startup-hold addition to the fake** (default byte-identical) and **one new e2e test**.

### 1. Fakeclaude startup hold — `PYRY_FAKE_CLAUDE_STREAM_HOLD`

**Contract:** new env `PYRY_FAKE_CLAUDE_STREAM_HOLD=<trigger-path>` (default-off). When set, the stream-mode child blocks at startup — before it consumes any stdin — until the trigger file exists, then proceeds normally. Unset ⟹ byte-identical to today.

**Placement:** in `main()`'s stream branch (`main.go:460-471`), immediately before the `runStreamJSON(...)` call:

```
if hold := os.Getenv(envStreamHold); hold != "" {
    waitForTriggerFile(hold) // poll os.Stat until it exists; mirror the :540/:645 pattern
}
```

- Add the `envStreamHold` const alongside `envStreamInterrupt` (`main.go:266-267`).
- `waitForTriggerFile(path string)` is a small helper: loop `os.Stat(path)`; on `nil` error return; else `time.Sleep(pollInterval)`. Mirror the existing trigger loop (`main.go:540-560, 645`); reuse the `pollInterval` const. ~8 lines.
- **`runStreamJSON`'s signature and body are unchanged.** The hold lives at the call site, exactly like the #1137 stdin-tee ("the tee lives at the call site so `runStreamJSON` keeps its pure I/O signature"). This deliberately avoids a signature-churn cascade across `stream_detect_test.go`'s 7 `runStreamJSON` call sites — those stay untouched.

**Why startup-hold, not an in-turn hold.** Because the stream drain does not block on commit (above), holding a turn's *response* open cannot create a msgqueue backlog and, if multiple turns were held open at once, would coalesce into one turn at the emitter (no `turn_end` between echoes). Holding the child *before it consumes stdin* is the correct model: the daemon's pipe writes buffer regardless of whether the child reads (`WriteTurn` returns `nil` either way), so all queued turns land in the pipe during the hold, and on release the child emits one clean turn cycle per turn in FIFO order. It also matches real claude: a child slow to start its read loop leaves later turns queued in its stdin pipe.

**Watchdog is a non-issue.** `internal/streamsup/watchdog.go` fires only *while claude owes an assistant turn* (not at startup, awaiting=false), **emits a Stall — never kills** (`watchdog.go:167`), and its threshold is 240s. The hold here is sub-second (released the instant 3 acks are collected). The test's drain loop ignores any stall frame regardless.

### 2. E2e test — `internal/e2e/relay_v2_stream_queue_drain_test.go` (new file)

`//go:build e2e`, package `e2e`. `TestRelayV2_StreamQueueDrainsInOrder`. Skeleton = #1141's test, extended to 3 ordered sends with a hold trigger.

**Constants:** `initialUUID`, `knownConvID` (both UUIDv4 literals, distinct), three distinct ASCII markers (`e2e-1138-msg1/2/3` — test-only, no secrets, per `relay_v2_queue_drain_test.go`'s header note), three `reqID`s. A `holdPath := filepath.Join(home, "stream-hold.trig")` (not created yet).

**Flow (bullet scenarios — developer writes the Go in-house idiom):**

1. Pair one interactive phone (`RunBareIn ... "pair"`); decode payload + server static pubkey.
2. `seedBoundConversation(t, home, knownConvID, initialUUID)` — same invariant as #1141: binds `knownConvID → bootstrap session` so `sessionRouter.Route` resolves (past the #678 empty-CSID guard) **and** the drain-gate `activeSession()` == the sink tag `initialUUID`. A UUID mismatch drops every turn at the gate and hangs the drain.
3. `StartStreamInteractiveWithRelay(t, home, initialUUID, fr.URL()+"/v2/server", "PYRY_FAKE_CLAUDE_STREAM_HOLD="+holdPath)` — trigger absent ⟹ the child holds at startup. `t.Cleanup(h.Stop)`.
4. `waitBinaryHello`; `fakephone.Dial`; `driveHandshakeToOpenDaemonInteractive` (interactive capability — required for the structured stream).
5. **Submit 3 `send_message` envelopes to `knownConvID`** (distinct texts, distinct reqIDs), sealed, back-to-back. *All to the same conversation* — required for the single serial drain to order them.
6. **[Vacuity positive #1 — gate before release]** Collect **all three acks** (`Type==Ack`, `InReplyTo ∈ {reqID1,2,3}`) before touching the trigger. This proves all three were accepted/enqueued while the child was held (in flight). A missing ack before a ~15s deadline ⟹ `t.Fatal` (enqueue rejected — check the binding; the harness produced no queue, so the ordering assertion would be vacuous).
7. **Release:** `os.WriteFile(holdPath, ...)`. The child begins consuming stdin.
8. **[Vacuity positive #2 → the ordering assertion]** Drain the phone stream on the single test goroutine (mirror #1141's read loop: decode inner frame, skip non-`noise_msg`, decrypt). Collect `assistant_delta` payloads **in arrival order**; ignore `turn_state` / `queue_state` / `stall`. Continue until 3 deltas seen or a ~20s deadline (absorbs spawn + drain latency).
   - Assert each delta's `ConversationID == knownConvID`.
   - Assert the three deltas' texts contain the markers **in submission order**: `msg1`, then `msg2`, then `msg3`. Out-of-order ⟹ `t.Fatal` (this is the real failure the test guards — a reordering drain/pipe/emitter).
   - `< 3` deltas before the deadline ⟹ milestone `t.Fatal` naming how many arrived (the drain never completed end-to-end — UUID mismatch, delivery never reached the child, or the hold never released).
9. **Terminal close:** after the third delta, observe a `turn_state{State:"idle", ConversationID: knownConvID}` (mirrors #1141's M2) — the last turn closed.

**Why delta-order satisfies AC2 ("observes their turn_state transitions in that order").** Each `assistant_delta` is emitted strictly inside its own `responding → … → turn_end → idle` cycle (`interactive_turn_v2.go:183, 208-217`; unit-proven in `TestStreamTurnDrainV2_FullSingleTurn`). A fresh chunk after `idle` opens a new turn. So three deltas carrying `msg1/msg2/msg3` in order ⟺ three turn cycles in submission order. The delta marker is the unambiguous per-turn oracle; a bare `turn_state{responding/idle}` carries no text and cannot be attributed to a send on its own.

---

## Concurrency model

- **Single phone reader:** the test goroutine reads the phone conn serially (same idiom as #1141 / interrupt / new_session specs). No concurrent phone reads → no receive-nonce races.
- **Serial daemon drain:** one per-conversation drain goroutine delivers the 3 turns FIFO into the single-writer/single-reader stdin pipe; the parser and emitter are each single-goroutine. Ordering is **structural**, not timing-dependent.
- **Deterministic release:** a filesystem trigger, not a `time.Sleep`. Accumulation is gated by collecting all 3 acks before writing the trigger; observation is gated by the trigger. No sleeps anywhere in the test.
- **Timing robustness:** even if the daemon delivers `msg2/msg3` into the pipe *after* the release (drain racing the trigger write), the child reads the pipe FIFO, so order holds regardless of exact delivery timing.

---

## Error handling / failure modes

| Condition | Behavior |
|---|---|
| Ack for a send never arrives before release deadline | `t.Fatal` — enqueue rejected (binding/route miss, #678); the ordering proof would be vacuous. |
| `< 3` assistant_deltas before drain deadline | Milestone `t.Fatal` naming the count — delivery never reached the child / UUID mismatch / hold never released. |
| Deltas out of submission order | `t.Fatal` — the FIFO drain/pipe/emitter reordered (the guarded failure). |
| Stray `stall` / `queue_state` frames | Ignored by the drain loop (switch on `env.Type`); harmless. |
| Watchdog | Fires only while claude owes a turn (not at startup); emits Stall, never kills; 240s ≫ sub-second hold. Non-issue. |

---

## Testing strategy

- `make e2e` compiles and runs the new test (`e2e` build tag).
- **AC3:** `relay_v2_queue_drain_test.go` is a different file, not touched; the new test is purely additive.
- **Fake change is default-off:** env unset ⟹ byte-identical. `runStreamJSON` signature/body unchanged ⟹ `stream_detect_test.go` (7 call sites) and the #1136/#1137/#1140/#1141 stream specs are all unaffected.
- Optional white-box unit coverage of `waitForTriggerFile` is **not** required — it is a trivial `os.Stat` poll exercised end-to-end by the e2e; adding a unit test is at the developer's discretion and must not change `runStreamJSON`.

---

## Security

Not security-sensitive (no `security-sensitive` label; the security-review pass is skipped per the architect workflow). Test-only: the queue, the serial drain, the stream turn drain, and the emitter are already-built production code exercised unchanged. The only non-test source touched is the `fakeclaude` test binary, gaining an env-gated, default-off startup hold. No production `internal/` surface, no new wire verb, no new trust decision.

---

## Open questions

1. **Stream path does not confirm commits (production observation, not a blocker).** `newInboundDeliver` documents "DeliverFunc must return nil only on a confirmed commit" (`main.go:1424`), but on the stream path `WriteUserTurn → WriteTurn` returns on write, so the drain paces on write, not turn-completion — and a failed turn is neither retried nor held. This is why a msgqueue backlog can't be observed on the stream path, and this test proves order via the pipe (what production guarantees today) rather than via a msgqueue backlog. **Whether the stream path *should* confirm commits (to enable retry-on-failure + backlog pacing symmetric with the PTY path) is a separate production question — flag as a follow-up issue, do not fix here.** (Same disposition as the separately-tracked stream fail-closed gaps, e.g. #1133.)
2. **Literal "open turn" during the hold is deferred.** To have the client observe turn 1 as `responding` *during* the hold (an in-turn hold that splits echo/result on the first turn), `runStreamJSON` would need a new parameter, cascading across its 7 `stream_detect_test.go` call sites. The startup-hold proves the same submission-ordering with a smaller, lower-conflict footprint and zero change to the reviewed I/O seam. Revisit only if a reviewer requires the in-flight `responding` signal.
