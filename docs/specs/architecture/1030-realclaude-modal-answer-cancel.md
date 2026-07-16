# Spec #1030 — realclaude e2e: remote modal answer + cancel against a real claude permission prompt

**Ticket:** `test(e2e-realclaude): remote modal answer + cancel against a real claude permission prompt` (#1030, split from #963)
**Size:** S — one new test file in `internal/e2e/realclaude/`, **0 production files**, ~250 LOC.
**Security-sensitive:** No (labels: `size:s`, no `security-sensitive`). Test-only liveness of a security feature is not itself a security surface — the fake tier (#791/#1003/#993) owns the modal flow's security properties; the parent #963's no-sec-label is authoritative. Security-review step skipped per the label gate.

## Context

The `e2e_realclaude` tier is the pre-ship gate that proves operator-facing happy paths execute against **real** claude, not just the fake tier (2026-07-08 operator policy). The interactive daemon path has grown three real-claude liveness gates — #854 (bootstrap), #997 (per-conversation), #1028 (conversation lifecycle) — but **no modal-resolution verb has ever executed against real claude.** The remote-approval flow (answer/cancel a permission prompt from a phone) is the entire point of driving a session remotely, yet its real-tier coverage is a gap.

The fake tier is closed: `modal_answer` (`internal/e2e/relay_v2_modal_answer_test.go`, #791), `modal_cancel` (`internal/e2e/relay_v2_modal_cancel_test.go`, #1003), trust-class (#993). Those drive a **fakeclaude** that simulates a modal via `PYRY_FAKE_CLAUDE_MODAL_TRIGGER`. This ticket extends the coverage to the **real interactive stack**, where real claude raises an actual TUI permission modal, tui-driver detects it, the daemon surfaces `modal_shown`, and the phone resolves it over the Noise v2 wire.

**This is the hardest of the #963 families** because the trigger is not a test env-var — it is real claude choosing to call a gated tool under a real permission mode. The two design risks the ticket flags (reliable trigger; version-sensitivity of real permission prompts) are addressed in § Reliability determination and § Open questions.

## Reliability determination (the ticket's explicit architect call)

The ticket delegates to the architect: *confirm the current claude reliably raises the interactive modal before committing both branches; if it can't be made reliable, narrow to whichever of answer/cancel is stable and flag it.* **Determination: ship BOTH branches.** The mechanism is sound against the pinned stack:

1. **Version skew is resolved.** `go.mod` pins `tui-driver v1.10.0`, whose `claude-version.lock` is `2.1.199` — the installed claude. (The stale `env-realclaude-pty-blocked-claude2-vs-tuidriver19` memory, 2026-07-10, referenced `tui-driver v1.9.0` vs claude 2.1.199; the v1.10.0 bump re-validated against 2.1.199. See § Open questions for the residual host-block caveat.)
2. **The trigger is deterministic.** Spawned WITHOUT `--dangerously-skip-permissions`, claude runs in default permission mode; the first gated tool call (Bash) blocks on a permission modal — tui-driver's own `cmd/spike-multi-turn/README.md` documents exactly this ("Default `--permission-mode` blocks tool use on a modal"). tui-driver v1.10.0 detects it as `ModalClassPermission` (`pkg/tuidriver/permission.go`, `cmd/spike-permission`).
3. **The daemon already surfaces it with zero production change.** `runSupervisor` trust-marks `-pyry-workdir` at startup (`cmd/pyry/main.go:692`), so the **startup trust modal never fires** — only the per-tool permission modal does. The live `#798` modal surfacer (`cmd/pyry/interactive_modal_stream_v2.go`) is wired unconditionally into `startRelayV2` under the `bridge != nil && claudeSessionsDir != ""` gate the daemon satisfies; it turns a detected `EventKindPtyModalShown{ModalClassPermission}` into a `modal_shown` broadcast to interactive conns.
4. **Both branches have deterministic *positive* observables** (answer: a continuation `assistant_delta`; cancel: a `modal_dismissed{cancelled,remote}` broadcast). Neither relies on a flaky negative.

Narrowing would not materially de-risk: the flake surface, if any, is the **shared trigger** (real claude raising + tui-driver classifying the modal), not either resolution verb. If preship shows trigger flake, the fix is trigger-robustness or deferral — not shipping one branch. Because the trigger is shared, the two branches cost only a second modal cycle to cover both, so both ship.

## Files to read first

- `internal/e2e/realclaude/interactive_bootstrap_liveness_test.go` — **the harness this file reuses wholesale (same package, same build tag).** Extract:
  - `spawnBootstrapDaemon` (:383) + the hardcoded `--dangerously-skip-permissions` at **:398** — the spawn to mirror MINUS that one flag.
  - `bootstrapDaemon` struct (:372) + `waitForReady` (:423) + `stop` (:445) + `shortSocketPath` (:467) + `ensurePyryBuilt` + `lockedBuffer` (:589) — reusable daemon plumbing; the new spawn helper reuses these unchanged.
  - `driveHandshakeInteractive` (:247) — grants the `interactive` capability the modal broadcast rides.
  - `sealSendMessage` (:160), `drainForAssistantReply` (:188) — the send + liveness-drain, including the **receive-nonce-in-lockstep discipline** (decrypt every `noise_msg` in arrival order).
  - `seedBootstrapRegistry` (:528), `seedBoundConversation` (:545), `runPyry`/`decodePairPayload`/`readPersistedServerID`/`waitBinaryHello`/`relayTestLogger` — setup helpers.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` — `startPerConversationHarness` (:151, the closest harness shape); `sealEnvelope` (:262); `drainForReply` (:281, the `InReplyTo`-correlated drain — the broadcast drain this spec adds is a Type-only variant); `perTurnReplyBudget` (:85, 120s).
- `internal/e2e/realclaude/interactive_conversation_lifecycle_test.go` — **the structural precedent for this test's shape:** ONE test function, ONE live session, sequential operations over one encrypted channel with the nonce in lockstep (:66-153). This spec mirrors that shape (answer-phase then cancel-phase on one daemon), not the N-subtest/N-daemon shape.
- `internal/e2e/relay_v2_modal_answer_test.go` — the FAKE answer capstone. `bringUpModalHarness` (:67) **pairs WITH `--allow-remote-permissions`** (:76) — the answer gate; `awaitModalShown` (:189) — the `modal_shown{permission}` drain + vacuous-pass guard to mirror; the answer flow (:224-290). **Mirror the liveness shape, NOT the keystroke-fidelity checks** (`stdin.log` "2\r" oracle at :298-309) — that's fake-tier-only; real claude has no stdin log.
- `internal/e2e/relay_v2_modal_cancel_test.go` — the FAKE cancel capstone. `modal_cancel{modal_id}` is **fire-and-broadcast: no reply is correlated to the cancel** (:25, :57-98); the observable is the `modal_dismissed{cancelled,remote}` broadcast. Mirror the assertion order (raise → assert permission class → cancel → assert dismissal), skip the `stdin.log` ESC oracle.
- `internal/protocol/messaging.go:104-169` — `ModalShownPayload` (Class/ModalID/Options/DefaultOptionID), `ModalAnswerPayload` (**AnswerToken is a client-minted idempotency key, NOT authorization** — an arbitrary constant is fine), `ModalCancelPayload` (ModalID only), `ModalDismissedPayload` (Outcome + Source `{remote,local,timeout}`).
- `internal/protocol/codes.go:275-278` — `TypeModalShown`/`TypeModalAnswer`/`TypeModalCancel`/`TypeModalDismissed`.
- `internal/turnevent/taxonomy.go:50-51` — `PermissionOptionKindAllowOnce` / `PermissionOptionKindAllowAlways` (the answer's `OptionID`; either allow option makes claude proceed).
- `cmd/pyry/main.go:678-695` — `runSupervisor` confines + **trust-marks `-pyry-workdir`** and spawns claude in the trusted realpath. This is WHY dropping `--dangerously-skip-permissions` yields **only** the per-tool permission modal (no startup trust modal).
- `docs/knowledge/features/modalbridge-package.md` § "Live daemon wiring (#798)" — the daemon path (already in production) that detects a permission modal and broadcasts `modal_shown`; confirms **no production change** is needed.
- `docs/knowledge/features/permission-protocol-spike.md` — **read for the CAVEAT only.** #383's null finding ("no permission event on stdout") is the `agent-run` **stream-json** path — a *different surface* from this ticket's interactive-PTY TUI-grid modal. Do not conclude from #383 that real permission prompts don't surface here; they surface via tui-driver grid detection, not stdout events.
- *(reference, outside repo)* `tui-driver@v1.10.0/pkg/tuidriver/permission.go`, `cmd/spike-multi-turn/README.md`, `cmd/spike-permission/main.go` — evidence that default-mode Bash raises a `ModalClassPermission` modal against claude 2.1.199. Skim only if the trigger's reliability is in doubt.

## Design

One new file: **`internal/e2e/realclaude/interactive_modal_resolution_test.go`** (`//go:build e2e_realclaude`, `package realclaude`). No production files. No edits to the three shipped liveness gates.

### Shape: one test, one daemon, two sequential modal cycles

Mirror `interactive_conversation_lifecycle_test.go`: a single `TestInteractiveModalResolution` drives one bound conversation on the daemon's bootstrap session over one encrypted channel. Two phases, each raising ONE real permission modal via the shared trigger scaffold:

- **Phase A (answer):** raise modal → assert `modal_shown{Class:"permission"}` → `modal_answer{allow}` → the session proceeds (a subsequent non-empty `assistant_delta`). AC #1.
- **Phase B (cancel):** raise a second modal → assert `modal_shown{Class:"permission"}` → `modal_cancel` → observe `modal_dismissed{Outcome:"cancelled",Source:"remote"}` broadcast. AC #2.

One daemon + one session halves the expensive cold-claude spawn versus two independent subtests, and matches #1028's precedent (sequential ops on one live session, nonce in lockstep). The "one trigger scaffold" (PO framing) is the `raiseRealPermissionModal` helper, called once per phase.

### Package structure / new helpers (all in the new file)

| Symbol | Signature (contract) | Behavior |
|---|---|---|
| `spawnPermissionDaemon` | `(t, home, workdir, claudeBin, relayURL) *bootstrapDaemon` | Byte-identical to `spawnBootstrapDaemon` EXCEPT the trailing claude args are `"--", "--model", "haiku"` — **no `--dangerously-skip-permissions`**. Reuses `bootstrapDaemon`/`waitForReady`/`stop`/`shortSocketPath`/`ensurePyryBuilt`/`lockedBuffer` unchanged. |
| `startModalResolutionHarness` | `(t) *perConvHarness` (+ bound `convID`) | Mirror of `startPerConversationHarness` with two deltas: (1) pair WITH `--allow-remote-permissions` (the answer gate); (2) spawn via `spawnPermissionDaemon`. Seeds the bootstrap registry AND a bound conversation (`seedBootstrapRegistry` + `seedBoundConversation`) so the trigger drives the bootstrap session directly. |
| `raiseRealPermissionModal` | `(t, h, initSend, initRecv, reqID, nonce) string` | Seals a Bash-triggering `send_message`, drains to `modal_shown`, asserts `Class == "permission"` and non-empty `ModalID`, returns the `ModalID`. The shared scaffold. |
| `drainForControlEvent` | `(t, phone, cs, wantType, deadline) protocol.Envelope` | Broadcast-drain: returns the first envelope whose `Type == wantType` (no `InReplyTo` correlation — `modal_shown`/`modal_dismissed` are `EventID==nil` broadcasts). Decrypts every `noise_msg` in arrival order (nonce discipline); `t.Fatal` on `TypeError` and on deadline, each naming its failure mode. |

`drainForAssistantReply` (existing) is reused verbatim for Phase A's continuation.

### The trigger prompt

`raiseRealPermissionModal` sends (via `sealSendMessage`) a prompt that forces a gated Bash call under default permission mode:

> `Use the Bash tool to run the command: echo pyrycode-<nonce>. After it completes, reply with a single short word.`

- The `<nonce>` (per-run + per-phase) defeats accidental caching and keeps Phase B's prompt distinct from Phase A's.
- "Use the Bash tool to run …" reliably makes haiku call Bash rather than answering from knowledge; Bash is gated → permission modal.
- "After it completes, reply with a single short word" guarantees a non-empty continuation `assistant_delta` once the answer routes (Phase A's liveness signal). claude's actual word is never asserted (substrate-guard safe).

### Data / control flow (Phase A)

```
phone: sealSendMessage(Bash-trigger, convID)              [reqID=2]
  daemon → claude: types prompt; claude calls Bash → real permission modal
  daemon (#798 surfacer): EventKindPtyModalShown{Permission} → modal_shown broadcast
phone: drainForControlEvent(TypeModalShown) → assert Class=="permission", capture modalID
phone: sealEnvelope(modal_answer{modalID, allow_once, token})   [reqID=3]
  daemon: dispatchAppFrame intercept → ResolveAnswer (device-gated) → Answer keystroke → claude proceeds
  claude: runs Bash → replies → assistant_delta continuation
phone: drainForAssistantReply(convID) → non-empty assistant_delta  [liveness, AC #1]
```

Phase B is identical up to `modal_shown`, then:

```
phone: sealEnvelope(modal_cancel{modalID2})               [reqID=5]  (fire-and-broadcast, no reply)
  daemon: dispatchAppFrame intercept → ResolveCancel → SendEsc → claude dismisses the tool
  daemon: broadcastModalDismissed → modal_dismissed{cancelled, remote}
phone: drainForControlEvent(TypeModalDismissed) → assert Outcome=="cancelled", Source=="remote"  [AC #2]
```

Between the phases the session returns idle (Phase A's turn completes), so Phase B's `send_message` delivers cleanly (`WriteUserTurn`'s ready-gate is idle again).

## Concurrency model

Single test goroutine drives the wire; the daemon is a subprocess. The only concurrency contract the test must honor is the **Noise receive-nonce lockstep**: `initRecv` must decrypt every binary→phone `noise_msg` in arrival order across the whole run. `drainForControlEvent`, `drainForAssistantReply`, and `sealEnvelope`/`sealSendMessage` are called strictly sequentially on one stream, and each drain decrypts every `noise_msg` it passes over — so the nonce stays in sync exactly as in #854/#997/#1028. A `modal_shown` / `modal_dismissed` is a `noise_msg` carrying an `EventID==nil` control envelope; it is decrypted (advancing the nonce) and either matched or skipped, never dropped-without-decrypt. Only non-`noise_msg` inner frames (e.g. rekey) are skipped without decrypting.

Daemon lifecycle (`spawnPermissionDaemon` → `t.Cleanup(d.stop)`) and skip-clean-on-no-creds (`WithWorktreeAuthenticated`, `exec.LookPath("claude")`) are inherited unchanged from the existing harness.

## Error handling

- **No creds / no claude** → `t.Skip` (via `WithWorktreeAuthenticated` + `LookPath`), AC #3.
- **Modal never surfaces** (trigger failed to raise/classify a permission modal) → `drainForControlEvent(TypeModalShown)` hits its deadline → `t.Fatal` naming "no permission modal surfaced — trigger did not raise it, or tui-driver did not classify it as permission". This is the non-vacuity guard: the answer/cancel assertions can never pass over a modal that never appeared.
- **Wrong modal class** → `raiseRealPermissionModal` asserts `Class == "permission"` (fail otherwise); a trust-class or slash-picker modal is a hard fail, not a skip.
- **Answer denied at the gate** (missing `--allow-remote-permissions`) → no continuation → `drainForAssistantReply` deadlines with a message pointing at the device gate. (The pairing flag prevents this; the failure message documents the trap.)
- **`TypeError` envelope** at any drain → `t.Fatal` surfacing the payload (e.g. a `session_error`).
- **Generous timeouts:** `modal_shown` / `modal_dismissed` drains use ~30s (cold claude reaching the tool call); the Phase A continuation reuses `perTurnReplyBudget` (120s). No sleeps; every wait is a decrypt-drain against a deadline.

## Testing strategy

The file IS the test. Assertions are liveness / observable-state shaped; the fake tier owns detailed modal-payload shape and keystroke fidelity.

Scenario checklist (one test, two phases):

- **Setup** — pair with `--allow-remote-permissions`; seed bootstrap + a bound conversation; spawn the daemon WITHOUT `--dangerously-skip-permissions`; interactive handshake (grants `interactive` capability).
- **Phase A / answer (AC #1):**
  - Send the Bash-trigger prompt on the bound conversation.
  - Drain to `modal_shown`; assert `Class == "permission"` and non-empty `ModalID` (**non-vacuity gate — asserted BEFORE the answer**).
  - Send `modal_answer{modalID, PermissionOptionKindAllowOnce, "<any-token>"}`.
  - Drain to a non-empty `assistant_delta` for the conversation within `perTurnReplyBudget` → the session proceeded. Claude's words never asserted.
- **Phase B / cancel (AC #2):**
  - Send a second Bash-trigger prompt (distinct nonce).
  - Drain to `modal_shown`; assert `Class == "permission"`, capture `modalID2` (**non-vacuity gate — asserted BEFORE the cancel**).
  - Send `modal_cancel{modalID2}` (fire-and-broadcast; the request ID is cosmetic — no correlated reply).
  - Drain to `modal_dismissed`; assert `ModalID == modalID2`, `Outcome == "cancelled"`, `Source == "remote"`. This is the broadcast/state-shaped observable the ticket mandates — NOT a correlated cancel-reply, which does not exist.
- **AC #3** — the test `t.Skip`s cleanly when `claude` is absent or no credentials are present (inherited).
- **AC #4** — placement under `//go:build e2e_realclaude` wires it into `make e2e-realclaude` (and thus `make preship`) with no Makefile change. Added wall-clock is one cold daemon spawn + two sequential modal cycles (~2–3 min); acceptable for a real-claude gate and comparable to the 3-subtest per-conversation gate.

Not a deterministic RED/GREEN oracle — a standing real-claude liveness gate, like #854/#997/#1028. The deterministic shape/security checks live in the fake tier (#791/#1003).

## Scope self-check

- New/modified **production source files: 0** (one new `*_test.go`, test-only). Well under the ≥5 gate.
- New files: 1. Total LOC ~250. New exported types: 0. Consumer call sites to update: 0 (self-contained; reuses package helpers by reference; no signature changes). ACs: 4. → **S, no split.**
- File-overlap check (2026-07-16, `git fetch --prune` + branch scan of the new file and the two shared-helper files against all `origin/feature/*` branches): **no overlap.** The new file is unique; the design touches no shared file.

## Open questions

1. **Residual host PTY-block on a tui-driver-incompatible claude.** On any host whose installed claude ≠ tui-driver's locked version, the *entire* realclaude PTY suite blocks at `WaitReady` ("unexpected dialog at startup") before any test code runs — a suite-wide **environmental** red, not diff-attributable to this test (see `code-review-realclaude-red-attribution-before-rework`: no-import + test-only-diff + tree==main ⇒ environmental; PASS+flag, do not route to rework). The current stack (tui-driver v1.10.0 ↔ claude 2.1.199) matches, so this test should run; a future claude bump ahead of tui-driver would re-block the whole suite. **Operator validation:** this gate must be exercised once against the pinned/compatible claude in preship before it is trusted — the architect cannot run it in a non-interactive/PTY-blocked worktree (no creds in-session).
2. **Real-modal flake surface.** tui-driver ships `cmd/repro-permission-flake`, i.e. permission-modal classification has a known (bounded) flake history. Mitigations in this spec: generous timeouts, strong non-vacuity ordering, deterministic positive observables. If preship shows the modal intermittently fails to surface, the fix is trigger-robustness (e.g. bounded retry of the raise) or deferral — **not** narrowing to one branch (the flake is in the shared trigger, not either verb). The narrowing lever remains available per the ticket, but is not expected to help.
3. **`ResolveCancel` device gating.** `ResolveCancel(modalID, dev)` takes a device; cancel/deny is fail-safe, so it likely does not require `AllowRemotePermissions`. The harness pairs WITH the flag regardless (the answer branch needs it), so both branches are covered either way — the developer need not disambiguate.
4. **Two allow options.** `PermissionOptionKindAllowOnce` (keystroke "1") vs `AllowAlways` ("2") — either makes claude proceed; the real tier asserts only the continuation, not the keystroke. Pick `AllowOnce` (the minimal grant). The keystroke-fidelity distinction is fake-tier-only.
