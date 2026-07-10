# Spec #904 — e2e: a non-empty backlog missed while disconnected is reconciled as `queue_state` exactly once on reconnect

**Ticket:** [#904](https://github.com/pyrycode/pyrycode/issues/904) · **Size:** S · **Security-sensitive:** no (correctness / no-data-loss; the security-property half of the #829 split is its modal twin #903, which carries the `security-sensitive` label — this queue path has no nonce/auth/untrusted-input invariant to falsely certify).

Pure test code. **Zero production files.** One new file: `internal/e2e/relay_v2_queue_reconnect_test.go`, tagged `//go:build e2e`.

---

## Files to read first

- `internal/e2e/relay_v2_queue_drain_test.go` — **the primary pattern.** Copy its bootstrap verbatim (pair → `decodePairPayload` → `seedBoundConversation` → align sessions dir + pre-create `<initialUUID>.jsonl` → `fakerelay.New` → `StartRotationWithRelay` with `PYRY_FAKE_CLAUDE_IDLE_TRIGGER` → `readPersistedServerID` → `waitBinaryHello`). Its lines 177-238 show the enqueue-while-busy → drain `queue_state` until `len(qs.Queued)==N` loop and the exact `protocol.QueueStatePayload` field access (`.ConversationID`, `.Queued[i].Text`, `.Queued[i].QueuedMsgID`). Reuse its `sealSend` / `nextEnv` closure bodies.
- `internal/e2e/relay_v2_modal_reconnect_test.go` (**on `origin/feature/903`, not yet merged — read for structural mirror only, do not import from it**) — the exact twin. Copy the shape of `reconnectModalHarness` → your `reconnectQueueHarness`, `phoneSession` + `openInteractivePhone` (verbatim — frozen, correct), and `assertModalShownFor` → your `assertQueueStateFor`. Note the deliberate-duplication house pattern: everything lives in the one test file, no shared-harness edit.
- `internal/relay/v2session.go:2292` — `reconcileQueues`. The producer under test: fires on every `handleNoiseInit` success tail (fresh interactive Noise handshake), gated on `s.interactive && cfg.OutstandingQueues != nil`, unicasts one `queue_state` per non-empty conversation to `s.connID` only. `ID:1` non-load-bearing; `EventID` nil (not in the replay ring). This is what "reconnect" triggers.
- `internal/e2e/relay_two_phone_structured_test.go:427-456` — `buildHelloEarlyInteractive` (advertises `CapabilityInteractive`, **no** replay cursor → no `#647`/`#777` replay path) and `driveHandshakeToOpenDaemonInteractive` (returns fresh send/recv `CipherState` per handshake — each reconnect gets a new Noise session). Reuse verbatim.
- `docs/knowledge/codebase/878.md` — the producer's contract: `SnapshotAll` skips empty conversations (AC3), snapshot is idempotent full-state, the phone correlates on `conversation_id` + `queued_msg_id`. Confirms "current truth" and the match-and-replace-by-stable-id semantics AC2 asserts.
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md` § "Backpressure / replay" — the contract this test protects.

---

## Context

ADR 025 promises a reconnecting client's view is reconciled to current truth. #878 wired the queue half: on a fresh interactive Noise handshake the daemon re-sends a `queue_state` snapshot for every non-empty backlog. #878 has unit coverage (`internal/relay`, `internal/msgqueue`, `cmd/pyry`), but **no cross-layer proof** that a real fake client, disconnected then reconnected over the full daemon + Noise + relay stack, sees the backlog reconciled exactly once with no loss or duplication. Losing or duplicating a queued message on reconnect is a no-data-loss regression. This ticket is that proof for the queue path (the modal path is #903).

The mechanism is already merged (`reconcileQueues` at `v2session.go:2292`, `OutstandingQueues` wired in `cmd/pyry`). Buildability gate passes — the producer has live callers.

---

## Design

One test file, one top-level test `TestRelayV2_QueueReconcileOnReconnect`, plus three file-local helpers mirroring #903's shape. No production change.

### Harness (`bringUpReconnectQueueHarness`)

Mirror `relay_v2_queue_drain_test.go`'s bootstrap, stopping short of the phone dial (the test owns connect/disconnect/reconnect). Differences from the drain test:

- Pair plainly: `RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")`. **No** `--allow-remote-permissions` — there is no answer path here, only `send_message` enqueue + `queue_state` observation.
- `seedBoundConversation(t, home, knownConvID, initialUUID)` so `send_message` routes and frames enqueue instead of rejecting pre-enqueue.
- Start a **busy** fakeclaude via `PYRY_FAKE_CLAUDE_IDLE_TRIGGER=<idleTrig>` — but **never create `idleTrig`**. Claude comes up busy (WaitReady blocks) and stays busy for the whole test, so the backlog is populated and then held stable across every disconnect/reconnect. No drain, no drain-order oracle.
- Returns the reconnect ingredients (`fr`, `serverID`, `pubKey`, `token`, and `stdinLog` for the optional never-drained fence below). Registers no phone.

Contract: `func bringUpReconnectQueueHarness(t *testing.T) *reconnectQueueHarness`.

### `phoneSession` + `openInteractivePhone`

Copy #903's `phoneSession` struct (`phone`, `sealSend`, `nextEnv`) and `openInteractivePhone(t, h, deviceName) *phoneSession` verbatim (they are harness-independent — dial + `driveHandshakeToOpenDaemonInteractive` + the frozen `sealSend`/`nextEnv` closures). Reconnect is the composed recipe:

```
old.phone.Close(); ps := openInteractivePhone(t, h, "phone-a")   // same token → brand-new conn_id + fresh CipherStates
```

Every `nextEnv` wait is one long deadline with back-to-back reads — never a short poll on a conn we still need (a timed-out `fakephone` Receive closes the WS).

### `assertQueueStateFor`

Drains a `phoneSession` under one ~20s deadline until a `queue_state` for `knownConvID` with the expected backlog arrives; skips intermediate partial snapshots (the #722 on-change producer emits single/partial snapshots as each enqueue lands) and any acks; `t.Fatal` on the deadline (the reconcile-delivered-nothing / harness-produced-no-queue vacuous-pass guard) and on an intervening `TypeError`.

Contract: `func assertQueueStateFor(t, ps *phoneSession, want []queuedItem) protocol.QueueStatePayload`, asserting `ConversationID == knownConvID`, `len(Queued) == len(want)`, and per index `Queued[i].QueuedMsgID` + `.Text` match (FIFO order preserved). Returns the decoded payload so the caller can capture/compare the true `queued_msg_id`s.

### Test flow (`TestRelayV2_QueueReconcileOnReconnect`)

Assertion order is load-bearing — each step is a hard precondition of the next, so no downstream check can pass vacuously.

1. **Populate the backlog (phone #1).** `openInteractivePhone`, then `sealSend` N=2 `send_message` envelopes (distinct ASCII markers, distinct `MessageID`s) while claude is busy. They enqueue and stay queued.
2. **Fence + capture true ids.** `original := assertQueueStateFor(ps1, want2)` — this is the fence that the backlog genuinely exists, and the source of the true `queued_msg_id`s the reconcile must reproduce. (The daemon mints these ids; the test learns them here, not from a guess.)
3. **Disconnect.** `ps1.phone.Close()`. Backlog now present while **no client is connected** — a phone disconnect resolves nothing for the queue, and claude is still busy, so the backlog is unchanged.
4. **Reconnect (fresh attach — the mode under test).** `ps2 := openInteractivePhone(t, h, "phone-a")`. Same token → brand-new conn_id + fresh Noise session → `handleNoiseInit` success tail → `reconcileQueues`.
5. **Reconciled exactly once, current truth (AC1).** `assertQueueStateFor(ps2, want2)` and additionally assert every `Queued[i].QueuedMsgID` equals `original.Queued[i].QueuedMsgID` (same nonce set, not a re-mint; the reconciled snapshot reflects current backlog truth, not a stale/empty view). Then a bounded ~3s "no second `queue_state`" drain on ps2 (`t.Fatal` on a second `TypeQueueState` or any `TypeError`; a clean timeout is the pass — ps2 is discarded next, so the timeout-closes-WS is harmless).
6. **Idempotent re-send (AC2).** `ps2.phone.Close()`; `ps3 := openInteractivePhone(t, h, "phone-a")`. `again := assertQueueStateFor(ps3, want2)` and assert `again.Queued` equals `original.Queued` element-for-element by `QueuedMsgID` + `Text` + order — reconnecting again re-sends the **same** snapshot, no duplication, no corruption (match-and-replace by stable id applied idempotently). Same bounded "no second `queue_state`" drain on ps3.
7. **(Optional) never-drained fence.** Assert `stdinLog` is empty/absent throughout — claude never idled, so nothing was delivered to the PTY; confirms the reconciled backlog is genuinely "missed while disconnected," not one that partially drained. Cheap non-vacuity strengthener; keep if it doesn't fight the harness.

### The AC1 mode-collapse (document in the test's header comment)

AC1 names two reconnect modes: within the relay's 30s grace (same session) and after it (fresh attach). **Over `fakerelay` these collapse to the single fresh-attach path**, which is exactly the mode `reconcileQueues` fires on. `fakerelay` has no 30s grace and mints a fresh conn_id per `/v1/client` upgrade, and the reconnect recipe (`Close` + fresh `Dial` + re-handshake) is inherently a brand-new conn. The literal within-grace/same-session mode is #874/#875's *surviving-conn* hold/flush (the daemon–relay link stays up throughout this test; only the client WS blips), unreachable by this harness and owned by those tickets. Write the one fresh-attach scenario; document the collapse in prose; **do not** fabricate a cosmetic "within grace" subtest over a graceless harness.

---

## Concurrency model

Single fake client connected at any instant (phone #1, then #2, then #3 — each a fresh Noise session over a new conn_id). The daemon–relay server link stays up for the whole run; only the client side disconnects/reconnects. The busy fakeclaude holds one turn open (WaitReady blocked) for the test's duration, freezing the backlog. No goroutine coordination in the test beyond the single-deadline `nextEnv` drains.

---

## Error handling / non-vacuity guards

This test's failure mode is a **false green** (asserting reconciliation over a backlog that was never populated, or a snapshot that was actually a re-broadcast). Guards:

- **Populate fence (step 2)** — `assertQueueStateFor` `t.Fatal`s if the N-item backlog is never observed. A reconcile over an empty/absent backlog cannot pass.
- **Isolation fence (structural, documented)** — phone #2/#3 connect strictly **after** the last backlog change (the enqueues on phone #1's watch). The #722 on-change producer emits only on queue-content change and broadcasts to conns open at change time; a brand-new conn joining with no subsequent change gets nothing from it. So `reconcileQueues` (#878) is the **sole** possible `queue_state` delivery path to phone #2/#3 — a green assertion proves reconcile, not an accidental re-broadcast. This mirrors #903's raise-time-only reasoning exactly.
- **No replay path** — the hello advertises no replay cursor and `queue_state` carries `EventID: nil` (not in the turn-event replay ring), so `#647`/`#777` replay cannot re-deliver it. Reconcile is unambiguously the source.
- **Exactly-once negatives (steps 5, 6)** — bounded "no second `queue_state`" drains; a clean timeout is the pass.
- **Current-truth (step 5)** — asserting the reconciled `queued_msg_id`s equal the captured originals rejects a re-mint or a stale snapshot.

Markers are test-only ASCII (they round-trip through the `queue_state` payload the test echoes in failure messages). Do not paste secrets.

---

## Testing strategy

The test *is* the deliverable. Verify:

- `go test -tags e2e -race -run TestRelayV2_QueueReconcileOnReconnect ./internal/e2e/` passes.
- **Non-vacuity check** (mandatory for an e2e verification ticket — a passing e2e test that asserts nothing is worse than none): temporarily stub `OutstandingQueues` to nil (or point the test at a build where `reconcileQueues`'s call site is commented) and confirm step 5 `t.Fatal`s at the deadline. Revert. This proves the test actually exercises the reconcile producer. (Do this locally; do not commit the stub.)
- `go vet ./...` clean.
- No CI (org policy — e2e runs in the pre-ship gate only).

Because `internal/e2e` env-var lists (`$PKGS`) trip zsh word-splitting, run the package path literally as above, not via a shell variable.

---

## Open questions

- **N (backlog size):** 2 is sufficient (proves order preservation + multi-item, keeps the test fast). The drain test uses 3; 2 is fine here since we don't exercise a middle-drop. Developer's call if 3 reads cleaner.
- **Optional never-drained fence (step 7):** include only if `stdinLog` stays reliably empty on the busy path (it should — WaitReady never returns). Drop it rather than fight a flaky read; steps 2/5/6 already carry the non-vacuity load.
- **Single conversation:** the test uses one `knownConvID`. A two-conversation variant would additionally prove per-conversation keying on reconcile, but that keying is already covered by #878's `internal/relay` unit test; keep this e2e to the single-conversation no-loss/no-dup proof to stay within S.
