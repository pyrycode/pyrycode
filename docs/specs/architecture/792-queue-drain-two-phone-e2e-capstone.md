# Spec #792 — Live e2e capstone: inbound queue drains in order + dequeue a queued message

**Part of EPIC #597** (Phase 3 live exit gate). Split from #708. Mirrors the Phase 2 capstone #642 and the #723 deterministic dequeue test. `security-sensitive`.

## Verdict up front: reuse the harness, one small fakeclaude extension, ship as one S ticket

This is a **test/harness-only** capstone. The queued-backlog production code already shipped and was deterministically proved upstream: #705 (wire types), #721 (`send_message`→queue daemon wiring), #722 (`queue_state` producer), #723 (`dequeue_message` handler + the existing sleep-claude dequeue e2e). This ticket lands the **live** confirmation over one daemon + a real (fake) claude that goes **busy → free** so the backlog actually **drains**, and a queued message is **dropped** before it drains. It confirms; it does not re-prove.

**Single flow — do not split on the "+".** The dequeue (AC3) is exercised *inside* the drain flow (populate → drop one → drain the survivors); its "dropped message never reaches claude" negative is meaningless unless the queue provably populated and drained in the same run (the vacuous-pass guard). There is no clean seam to split along — this is the [[po-plus-title-capstone-is-one-flow]] shape the PO already flagged. One test, one ticket.

**What is missing over #723.** The existing `TestRelayV2_DequeueMessage_RemovesQueuedBeforeDrain` (`internal/e2e/relay_v2_dequeue_test.go`) uses `/bin/sleep 99999` as the child — claude is **never idle**, so the queue **never drains**. It proves enqueue + dequeue-of-a-non-head + surviving-order, but the head is *perpetually* in flight and no message ever reaches claude. #792 must prove the **drain**: claude busy → messages queue → claude frees → survivors reach claude **in send order**, and the dropped one never does. That requires a child that transitions **busy → idle on command** and records what it receives. The `/bin/sleep` stand-in can't; fakeclaude can, with one tiny additive extension (below).

**AC4 (flight recorder) is scoped out of the CI test — see § AC4.** The flight recorder lives in `internal/agentrun/ptyrunner` and is unreachable from the supervisor-hosted mobile-relay path. Same disposition as the sibling #791's AC5. The CI test asserts AC1/AC2/AC3.

## Files to read first

- `internal/e2e/relay_v2_dequeue_test.go` (whole file, ~236 lines) — **the primary template.** #792 is this test with the child swapped from `/bin/sleep` to a **busy-then-free fakeclaude**, three messages instead of two, a real drain after release, and a stdin-log order assertion. Lift verbatim: `sealSend`, `nextEnv` (single-deadline decrypt-drain skipping non-`noise_msg` frames), the `QueueStatePayload` decode + FIFO assertions, and the `t.Fatal` on "no queue_state before deadline". The only structural changes are the harness call (§ Design Part 2) and the post-release drain + stdin-log assertions.
- `internal/e2e/internal/fakeclaude/main.go` — the file to extend. Read: the `main()` poll loop (the `rotated` one-shot `os.Stat(trig)` pattern to mirror), the `tui` startup idle-glyph block (`if tui { writeStdout(idleGlyph) }` — the emission this ticket **defers**), `startStdinReader` + `turnPending` + `appendTurnGrowth` (the transcript-growth commit signal that stays **unchanged** and does the per-turn commit), and the env-const block + package doc (where the new env is documented). Already on the substrate-guard allowlist (#603); this change adds no new glyph.
- `internal/e2e/harness.go:304-360` — `StartRotationWithRelay`: wires fakeclaude as the child, sets `PYRY_FAKE_CLAUDE_STDIN_LOG` (the drain-order oracle), forwards `-pyry-relay=<relayURL>`, and **appends `extraEnv ...string` verbatim** (`:332`). Pass the new idle-trigger env through `extraEnv`; **no harness.go change needed** (the #642 seam).
- `internal/supervisor/supervisor.go:269-328` + `:346-420` — `deliverViaSession` / `confirmViaTranscriptGrowth`: `WaitReady` (gates on `IsIdle`) → baseline → `DeliverPrompt` (writes the prompt to the PTY) → poll for JSONL **growth** = commit. This is why "claude idle + JSONL grows on stdin" is a full turn commit, and why **no spinner is needed** on this (relay/`ResolveTranscript`-set) path.
- `pkg/tuidriver/state.go` (tui-driver v1.6.0, `IsIdle` ~L104-120) — `IsIdle` = `❯` (IdleGlyph) present in the **bottom status region** AND no spinner glyph there. The load-bearing fact: **withholding `❯` keeps claude "busy" (`WaitReady` blocks); emitting `❯` once flips it idle and it stays idle** (nothing overwrites the bottom region — the prompt bytes are not echoed to stdout).
- `docs/specs/architecture/721-route-send-message-through-inbound-queue.md` § "Error handling — the ack contract" and § Concurrency — the drain is **serial per conversation** (one in-flight message at a time, delivered via `WriteUserTurn` with the raw lifecycle ctx, no deliver timeout). This serialization is what makes "drain in send order" hold by construction; the stdin log just witnesses it.
- `internal/msgqueue/queue.go:224-249` (`Remove`) + `queue_introspect_test.go:90` (`TestQueue_Remove_DropsNonHeadEntry_OrderPreserved`) — `Remove` of the **in-flight head** (idx 0 while `draining`) is a no-op; a **non-head** (idx ≥ 1) is always safe and preserves order. #792 drops the **middle** message (idx 1), never the head.
- `docs/specs/architecture/642-structured-receive-two-phone-e2e-capstone.md` — the sessions-dir-alignment + pre-create-`<initialUUID>.jsonl` recipe, the fakeclaude-trigger extension pattern (this ticket mirrors its shape), and the fakephone single-deadline read discipline (603.md/634.md).
- `internal/e2e/relay_test.go` / `pair_test.go` / `harness.go` — shared `e2e`-package helpers reused as-is: `shortHome`, `relayTestLogger`, `readPersistedServerID`, `waitBinaryHello`, `mustJSON`, `RunBareIn`, `decodePairPayload`, `encodeWorkdir` (rotation_test.go), `seedBoundConversation` (harness.go:371), `driveHandshakeToOpenDaemonInteractive` / `sendNoiseMsg` / `decryptInnerEnvelope` (relay_v2_daemon_test.go). `fakephone` / `fakerelay` under `internal/e2e/internal/`.
- `internal/protocol/messaging.go` — `SendMessagePayload`, `DequeueMessagePayload`, `QueueStatePayload{ConversationID, Queued []QueuedItem}`, `QueuedItem{QueuedMsgID, Text, TS}`. Same types #723 decodes.

## Context

Phase 3 of the mobile structured stream. A phone with an interactive session can type while claude is mid-turn; those turns buffer in `internal/msgqueue` and the daemon pushes `queue_state` to the interactive phone as the backlog changes, draining one message at a time as claude reaches idle. Every piece is shipped and unit/integration-proved. What is **not** yet proved is the **live** end-to-end behaviour: over one running daemon and a real supervised child, two-plus messages typed during a busy turn queue (not interleave), drain to claude **in order** once the turn frees, and a queued message dropped from the phone via `dequeue_message` never reaches claude while the surviving order holds. This capstone is that live confirmation. **Test/harness code only** — a surfaced production gap is a separate ticket.

## Design

### Part 1 — fakeclaude: a "busy-until-idle-trigger" mode (~10-15 LOC, additive)

The current fakeclaude is **single-turn**: TUI mode emits the idle glyph `❯` at startup (idle immediately) and the spinner `✻` once on the first stdin bytes (then wedges "thinking" — a second `WaitReady` blocks forever). That gives no controllable busy window and cannot drain a second message.

Add one deterministic mode. New env `PYRY_FAKE_CLAUDE_IDLE_TRIGGER` (path). Contract:

- **When unset (default): byte-identical to today.** Every existing caller is unperturbed.
- **When set:** fakeclaude starts **busy** — it does **not** emit the startup idle glyph and **never** emits the thinking spinner, so tui-driver's `IsIdle` stays false and the supervisor's `WaitReady` blocks (claude is "busy", exactly like the `/bin/sleep` child but under fakeclaude). It watches the path in the existing poll loop; on the file's **first appearance** it emits the idle glyph `❯` **once** (→ `IsIdle` true → `WaitReady` returns) and removes the trigger. Thereafter claude stays idle for every subsequent turn — nothing overwrites the bottom status region (the delivered prompt is not echoed to stdout), so `WaitReady` returns immediately for each queued message and the backlog drains back-to-back.
- **Per-turn commit is unchanged.** The stdin reader (already active because `StartRotationWithRelay` sets `PYRY_FAKE_CLAUDE_STDIN_LOG`, so `logPath != ""`) sets `turnPending` on each delivered turn; the main loop's existing `appendTurnGrowth(f)` grows the JSONL, which the supervisor's `confirmViaTranscriptGrowth` observes as the commit. No spinner is needed on this path (the relay bootstrap sets `ResolveTranscript`, so commit is growth-based, not the chip heuristic).

New helper, **signature + behaviour only** (developer writes the body, mirroring the `rotated` one-shot in `main`):

```
// emitIdleIfTriggered: when path exists, writeStdout(idleGlyph) once and remove
// the trigger; report whether it fired. Gated in main by a one-shot `idled` bool
// exactly like the existing `rotated` gate, so it emits the idle glyph at most once.
func emitIdleIfTriggered(path string) bool
```

Wire it in the `main` poll loop next to the existing `os.Stat(trig)` rotation check and `emitAssistantIfTriggered` / `emitStructuredJSONLIfTriggered` calls, gated on `idleTrig != "" && !idled`. Add the env const to the const block and document it in the package doc (mirror the `PYRY_FAKE_CLAUDE_JSONL_TRIGGER` doc paragraph).

**Do NOT combine with `PYRY_FAKE_CLAUDE_TUI`** — TUI mode's startup `❯` and stdin-spinner would defeat the busy window and wedge the second turn. The two modes are mutually exclusive; this test sets only the idle-trigger.

**Substrate-guard:** the idle glyph `❯` (`idleGlyph`) is already declared in fakeclaude/main.go; no new glyph is introduced, so the allowlist is unchanged. The new test file carries only JSON + ASCII markers — clean.

### Part 2 — the test (`internal/e2e/relay_v2_queue_drain_test.go`, new, `//go:build e2e`)

Lift #723's structure. Three ASCII markers (`e2e-792-msg1/2/3`) chosen so each is a unique substring in the stdin log. Choreography:

1. **Seed the two-phone/relay harness** exactly as #723: `RunBareIn(... "pair" ...)` → `decodePairPayload`; `seedBoundConversation(home, knownConvID, initialUUID)`; align `sessionsDir = filepath.Join(home, ".claude", "projects", encodeWorkdir(home))`, `MkdirAll` it, and **pre-create `<initialUUID>.jsonl`** (`{}\n`) so `reconcileBootstrapOnNew` rotates the bootstrap to `initialUUID`. `fakerelay.New`.
2. **Start with a busy fakeclaude:**
   `StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverRotate, stdinLog, fr.URL()+"/v2/server", "PYRY_MOBILE_V2=1", "PYRY_FAKE_CLAUDE_IDLE_TRIGGER="+idleTrig)` where `neverRotate`/`idleTrig`/`stdinLog` are distinct paths under `home` (`neverRotate` never created). Claude comes up **busy** (no `❯`).
3. `waitBinaryHello`; `fakephone.Dial`; `driveHandshakeToOpenDaemonInteractive` (grants `interactive`, the capability `queue_state` requires).
4. **Enqueue three** `send_message` frames (m1, m2, m3) for `knownConvID`, distinct `MessageID`s and marker texts. Each acks immediately (#721 enqueue-and-ack); the drain picks m1 → `WriteUserTurn(m1)` → `WaitReady` **blocks** (busy). m1 is the in-flight (draining) head; m2, m3 are queued at idx 1, 2.
5. **[Vacuous-pass positive #1] Drain `queue_state` until it shows all three in FIFO order** (skip acks / single-item snapshots, per #723's loop). `t.Fatal` if not observed before a ~20 s deadline (this is the "harness produced no queue" failure mode — fatal). Assert `ConversationID == knownConvID` and `Queued[0..2].Text == msg1/2/3`. Capture `msg2ID := Queued[1].QueuedMsgID`. **Assert `stdinLog` is still empty** — nothing has been delivered to claude (all three "not delivered mid-turn", AC1).
6. **Drop the middle** message: send `dequeue_message{knownConvID, msg2ID}` (m2 is idx 1 — removable; the head m1 is not). Drain `queue_state` until it shows `[m1, m3]` (len 2, m2 absent, order preserved). A `dequeue_message` of a valid request never produces an error envelope (#723 AC-2) — an error here is a failure.
7. **Free claude:** `os.WriteFile(idleTrig, ...)`. fakeclaude emits `❯` → `WaitReady` returns → m1 delivered + committed (growth) → drain advances → m3 delivered + committed. Drain `queue_state` until it shows **empty** (`Queued` len 0).
8. **[Vacuous-pass positive #2 → then the negative] Assert the drain reached claude in order, then the drop held:**
   - Read `stdinLog`. **Require it contains msg1's marker AND msg3's marker, with `Index(msg1) < Index(msg3)`** (AC2: survivors drained in send order). `t.Fatal` if either is absent — the drain did not run, so the m2 negative would be vacuous.
   - **Only then** require `stdinLog` does **NOT** contain msg2's marker (AC3: the dropped message never reached claude).

Assertion order is load-bearing: the two positives (three queued; two drained in order) gate the negative (m2 absent).

## Concurrency / timing model

No new production goroutines (test-only). Pre-existing actors and the deterministic fences:

- **Serial per-conversation drain** (#704/#721): one message in flight at a time; `WriteUserTurn` returns (commit) before the next is peeked. This is what makes "drain in send order" hold structurally — the stdin log only witnesses it. The head is un-removable while draining; idx ≥ 1 removals are safe (`Remove`).
- **Busy → free is a single explicit transition**, not per-turn cycling: `WaitReady(m1)` blocks until the test drops `idleTrig`; after `❯`, claude stays idle, so m1 then m3 drain back-to-back with no further signal. (No spinner, no scroll/redraw emulation — the whole reason this stays ~10 LOC of fakeclaude.)
- **fakephone read discipline (603.md/634.md):** reuse #723's `nextEnv` — a single long deadline with back-to-back decrypt-in-order reads; never a short-timeout poll on a phone conn.
- **fsync visibility:** fakeclaude fsyncs the stdin log per write and the JSONL per growth (existing behaviour) — cross-process visibility on APFS is already handled; the test reads `stdinLog` only after observing the empty `queue_state` (a happens-after fence: the empty snapshot means both commits completed, so both prompts are on disk).

Timing budget: enqueue/queue_state pushes are sub-second; the busy window is bounded only by the test dropping `idleTrig` (deterministic, not a race). A ~20 s populate deadline and ~10 s post-release deadline mirror #723.

## Error handling

- **Harness produced no queue** → the step-5 `t.Fatal` fires (vacuous-pass guard #1).
- **Drain never ran** (claude never idled / commit never confirmed) → step-8 requires msg1 **and** msg3 in the stdin log first; their absence is `t.Fatal` before the m2 negative is evaluated (vacuous-pass guard #2).
- **`dequeue_message` error envelope** → `t.Fatalf` (a valid dequeue must not error, #723).
- Handshake / seal / decrypt errors → `t.Fatalf`, consistent with #723.

## Testing strategy — AC mapping

One new `//go:build e2e` test (name e.g. `TestRelayV2_QueueDrainsInOrder_AfterBusyTurn`), asserting AC1-AC3 in a single run:

- **AC1** (queue while busy, not delivered mid-turn): step 5 — all three in `queue_state` FIFO **and** `stdinLog` empty during the busy window.
- **AC2** (drain to claude in send order once free): step 8 — `stdinLog` contains msg1 then msg3 (`Index(msg1) < Index(msg3)`) after release; `queue_state` reaches empty.
- **AC3** (dropped message never reaches claude; surviving order holds): step 6 (`queue_state` → `[m1, m3]`, order preserved) + step 8 (msg2 marker **absent** from `stdinLog`).
- **AC4** (flight-recorder audit): **not asserted in this CI test** — see § AC4.
- **Vacuous-pass guard:** the two positives (three queued; two drained in order) are hard preconditions of the m2-absent negative, each with a dedicated `t.Fatal` naming its failure mode.

Gates the developer runs green: `go build ./cmd/pyry`, `go vet ./...`, `staticcheck ./...`, `go test -race ./...`, `make substrate-guard`, and `go test -tags=e2e -run TestRelayV2_QueueDrains ./internal/e2e/...` (ideally `-count=3` for determinism, per #603). The `e2e_realclaude` column is untouched.

## AC4 — flight-recorder reachability (scoped out of CI, mirrors #791 AC5)

AC4 as written ("The run is recorded by the flight recorder for the audit trail") is **not assertable by this test in any variant**, and the honest scoping is:

- The flight recorder (`.cast` capture) lives in `internal/agentrun/ptyrunner/runner.go`, gated on `PYRY_RECORD_DIR`, and is reached **only** from `cmd/pyry/agent_run.go` (the `pyry agent-run` / dispatcher path). The queued-backlog flow is a **mobile-relay** flow: claude is hosted by `internal/supervisor` (tui-driver `Session`), which does **not** read `PYRY_RECORD_DIR` or wire `SpawnOpts.RecordTo` (verified: no `RecordTo`/`PYRY_RECORD_DIR` reference in `internal/supervisor` or the relay bootstrap). The two code paths are disjoint; no single run is both "a queue drain" and "recorded by the flight recorder."
- This is the **same reachability finding as the sibling #791**, whose AC5 the PO already scoped to "**live-stack variant only … not applicable to the fakeclaude-replay variant, which does not run claude under the ptyrunner recorder**." #792's AC4 is the un-scoped restatement of that same audit AC. Per the [[po-audit-artifact-ac-check-harness-reachability]] lesson, the architect scopes the AC to the variant whose import reaches the artifact — here, none on the mobile path.
- **Disposition:** the CI test asserts AC1-AC3. AC4 is deferred to the operator live run (`docs/knowledge/features/mobile-live-e2e-runbook.md`), consistent with #791. It is **not** a developer deliverable for this ticket and requires no test code.

This is an architect clarification, not PO rework: the sibling already resolved the identical AC this way, so a round-trip would be redundant. See § Open questions for the deeper production gap.

## Scope (S confirmed)

- **Production source files (`.go`, non-test):** **1** — `internal/e2e/internal/fakeclaude/main.go` (~10-15 LOC: one env const, one `emitIdleIfTriggered` helper, one gated call + `idled` bool in `main`, one package-doc paragraph). Purely additive: the startup idle glyph is already gated on `tui`, which this mode leaves off, so that block is untouched. Far under the §4 ≥5-file gate.
- **New files:** **1** — the e2e test. Under the >3 gate. **No harness.go change** (the `extraEnv` seam already exists).
- **Total LOC:** ~15 (fakeclaude) + ~250 (test) ≈ **265**. Under ~600.
- **New exported types:** 0. **Consumer call sites updated:** 0 (additive env — no signature change, no refactor cascade). **State-machine reject branches:** 0.
- **ACs of work:** 3 asserted in CI (AC1-AC3) + AC4 deferred. Under 5.

All red lines clear with margin. **Single ticket — no split.**

## Open questions

- **Genuine in-CI audit capture** would require wiring `PYRY_RECORD_DIR`/`RecordTo` into the supervisor-hosted path (a production change) — surfaced, **not built** (evidence-based-fix: no observed need, and #791 already deferred the same AC to the live stack). If the operator ever wants the mobile session recorded, that is a separate production ticket; recommend PO track it as an epic-#597 follow-up, not fold it here.
- **fakeclaude/main.go coordination with #791.** #791 (in-flight, `origin/feature/791` currently empty) may also extend fakeclaude (a permission-trigger mode per its Technical Notes). Both changes are **additive** (new env const + new gated call in the poll loop). The branch-overlap check is clean at architect time (no sibling branch touches the file yet); to keep the eventual merge trivial, keep this change confined to a new const, a new helper, and one new poll-loop call — do not reflow the existing const block or `main` loop.
- **msg2 marker collision.** Choose the three markers so none is a substring of another and none appears in the pairing/handshake bytes (e.g. `e2e-792-msg1`, `-msg2`, `-msg3`). Assert via `strings.Contains` / `strings.Index` on the decoded stdin-log bytes; the delivered prompt is bracketed-paste-wrapped (#749), so match the marker substring, never the whole log.

## Security review

**Verdict:** PASS

Test/harness-only; the queued-backlog production code (#705/#721/#722/#723) already shipped and was security-reviewed there. The adversarial question is not "does this introduce a vuln" but **"could this test PASS while the guarantee is broken?"** The guarantee under live confirmation: phone-originated inbound text queues, drains **in order**, and a **dropped** message is genuinely withheld from claude.

**Findings (categories walked):**

- **[Vacuous pass — the headline requirement] No MUST FIX.** Two architect-owned guards, ordered: (1) the queue must be observed populated to all three in FIFO before the drop, with a dedicated `t.Fatal` on the harness-produced-no-queue mode; (2) the survivors (msg1, msg3) must be observed **reaching claude in order** (stdin log, `Index(msg1) < Index(msg3)`) **before** the "msg2 absent" negative is evaluated, again `t.Fatal` on absence. A "dropped message absent" over a queue that never populated or never drained cannot pass vacuously — the positives gate the negative.
- **[Trust boundaries] No MUST FIX.** The boundary is unchanged and off-path for this test: phone-supplied `ConversationID` (lookup key) + `Text` (opaque transit) enter at the shipped `send_message`/`dequeue_message` handlers; the drop authorization is the shipped msgqueue `Remove` (head-vs-non-head safety) and the `interactive` capability gate on `queue_state`. This test exercises those, adds none. The `interactive` grant is pinned by the reused `driveHandshakeToOpenDaemonInteractive`.
- **[Error messages, logs, telemetry — AC-adjacent] No findings.** No new production logging. The msgqueue/handler discipline (never log `Text`; drain warns carry `conversation_id`/`queued_msg_id` only) is inherited unchanged. The test's own diagnostics echo only synthetic markers (`e2e-792-msg*`) — noted in the test header, as #723 does for its markers.
- **[File operations] No findings.** The idle-trigger and stdin-log paths are **test-controlled** temp files under the test `home`, never phone/network-controlled — no traversal, no attacker TOCTOU (same shape as the sanctioned `emitAssistantIfTriggered` / rotation trigger). `emitIdleIfTriggered` writes a fixed glyph and removes a test-owned path; the JSONL stays `0o600`.
- **[Cryptographic primitives] No findings.** Exercises the shipped Noise_IK path (#433); no new crypto. The interactive phone decrypts with its own session `CipherState` — a key mix-up fails loudly.
- **[Subprocess / external command] No findings.** fakeclaude is the shipped e2e stand-in spawned by the harness; the new mode adds no `exec`, no user-controlled args. It emits an already-declared glyph and reads/removes a test-owned trigger.
- **[Tokens / secrets] N/A.** Pairing tokens flow through the shipped handshake unchanged; fixtures are synthetic ASCII markers — no secret material. The stdin-log markers are documented as test-only (do not paste real secrets), mirroring #723's header note.
- **[Network & I/O] No findings.** The test is a client of the shipped server. Phone reads are single-deadline bounded (603.md). fakeclaude's prompt read is bounded by the existing 4 KiB buffer loop; the growth append is the inert `{}\n`.
- **[Concurrency] No findings.** No new production goroutines. fakeclaude's new emission runs only on the main poll goroutine (single-writer-of-`f` and single-writer-of-stdout-`❯` preserved; the stdin reader still only signals `turnPending` and appends the log). The drain's single-in-flight-per-conversation invariant is the shipped engine's; the busy→free transition is a single test-driven edge, no TOCTOU.
- **[Threat model alignment] No findings.** Addresses the epic-#597 inbound-backlog guarantee as the **live** confirmation; the deterministic oracle stays upstream (#722/#723). The inbound-queue unbounded-growth vector (surfaced OUT OF SCOPE in #721 § Security review) is unchanged and remains a separate follow-up — not touched here.

**Reviewer:** architect (self-review; security-sensitive gate)
**Date:** 2026-07-06
