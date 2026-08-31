# interactive_session_control_liveness_test.go
- `interactive_session_control_liveness_test.go` (#1031) — first real-`claude`
  coverage of the **session-control respawn verbs**, and the last of the #963
  families: `new_session` (rotate via `/clear`) and `set_session_settings`
  (respawn via a live restart) both tear down and re-establish the live
  claude child, so the operator-facing risk is the reply bridge failing to
  rebind past the respawn — the same failure class #854 guards on a cold
  bootstrap session. `TestInteractiveSessionControlLiveness` drives a
  sequential spine (the #1028 shape) on one daemon / one seeded bound
  conversation over one encrypted channel: turn 1 (binds the reply bridge +
  gives claude a transcript) → `new_session` rotate → turn 2 (proves the
  rebind past rotation) → `set_session_settings{Model:"haiku"}` respawn →
  turn 3 (proves the rebind past the restart). Each control verb pairs its
  liveness send with a deterministic on-disk `sessions.json` bootstrap-row
  anchor so the assertion is non-vacuous — a silently no-op'd respawn would
  answer from the un-rotated/un-restarted session and pass otherwise: the
  `new_session` rotate is a bounded ~1 s-cadence re-send loop (mirrors
  #1004) asserted against a pre-frame baseline id, and
  `set_session_settings` is asserted against the persisted `Model` field
  (mirrors #1005, matched by `Bootstrap==true` so it's robust to the
  restart itself rotating the id). `Model: "haiku"` is the credential-safe
  settings value — `claudeSettingsArgs` appends `--model haiku` after the
  daemon's own base `--model haiku` (last-wins), so the respawn never risks
  a different real model's credentials/rate limits in a pre-ship gate, while
  `"" → "haiku"` is still a genuine on-disk change that triggers a real
  restart. Real claude rotates its transcript on **every** `/clear` (unlike
  fakeclaude's one-shot `PYRY_FAKE_CLAUDE_CLEAR_ROTATES`), so the new
  `waitBootstrapIDSettled` helper waits for the bootstrap id to stop
  changing before each post-control `send_message` — otherwise a straggler
  rotation from a re-sent frame could tear down that turn's in-flight
  claude mid-stream. Reuses the #854/#1028 harness
  (`spawnBootstrapDaemon`, `driveHandshakeInteractive`, `sealSendMessage`,
  `drainForAssistantReply`) and the #997 generalised control-frame helpers
  (`sealEnvelope`, `drainForReply`) unchanged; zero production files
  touched. The only new code is the `bootstrapRow`/`readBootstrapRow`/
  `waitBootstrapID`/`waitBootstrapIDSettled`/`waitBootstrapModel` on-disk
  `sessions.json` reader family (mirrors #1028's
  `readConversationIDsOnDisk` and #1005's `settingsRow`/
  `readBootstrapSettings`). The fake tier (`relay_v2_new_session_test.go`
  #1004, `relay_v2_settings_test.go` #1005) owns the verbs' detailed shape
  and the reject-invalid path (out of scope here) — this test is
  liveness/observable-state shaped only, proving the real interactive stack
  survives both respawns. Last child of #963. See
  [`codebase/1031.md`](../codebase/1031.md).

- `interactive_stream_liveness_test.go` (#1153) — first real-`claude` coverage
  of the **stream-json interactive runner** (`interactive_runner:
  "stream-json"`, #1081); every prior interactive-relay real-claude test
  (#854/#997/#1028/#1030/#1031) runs under the default PTY runner.
  `TestInteractiveStreamLiveness` transcribes #854's daemon body (pair → seed
  bootstrap registry + bound conversation → spawn → handshake) with three
  deltas: the new `writeStreamInteractiveConfig(t, home)` helper flips the
  production config toggle (writes `<home>/.pyry/config.json =
  {"interactive_runner":"stream-json"}` before spawn — `resolveConfigPath`
  reads it once at startup) before `spawnBootstrapDaemon`; it drives **one**
  turn (AC parity with #1141) instead of #854's two; and it drains to
  completion via the new `drainForCompletedTurn` helper — the two-milestone
  drain #1141's fake-side spec requires (non-empty `assistant_delta` **then**
  terminal `turn_state{idle}`), where #854's `drainForAssistantReply` stops
  at the first delta. No content/echo assertion on M1 (real claude's words
  are non-deterministic, unlike #1141's fakeclaude echo). The config-writer
  is deliberately a standalone helper, not folded into a spawn wrapper, so
  the permission-flow rider #1154 (blocked-by this ticket) can compose it
  with `spawnPermissionDaemon` instead. Fresh package-private seed constants
  (`streamBootstrapUUID`/`streamConvID`) — #854's `liveBootstrapUUID`/
  `liveConvID` are file-private and would redeclare in the same package/tag
  namespace. Zero production files touched. See
  [`codebase/1153.md`](../codebase/1153.md).

- `interactive_stream_modal_resolution_test.go` (#1154) — the stream-json
  **sibling** of #1030's `TestInteractiveModalResolution`, not a replacement:
  #1030 proves the same answer round-trip under the default PTY runner; this
  is the desktop#483 scenario on the real stream stack (PTY-red vs.
  stream-green). `TestInteractiveStreamModalResolution` is #1030 Phase A
  (answer-only — Phase B cancel is out of scope) composed with #1153's
  stream-json seams: `startStreamModalResolutionHarness` is an inline copy of
  #1030's `startModalResolutionHarness` with exactly one inserted line,
  `writeStreamInteractiveConfig(t, home)` before `spawnPermissionDaemon` (the
  stream-vs-PTY differentiator), plus fresh distinct seed constants
  (`streamModalBootstrapUUID`/`streamModalConvID`, valid UUIDv4 shape, no
  redeclaration vs. #1030's or #1153's names). `raiseRealPermissionModal`
  (#1030, reused verbatim) still owns the AC1/AC2 non-vacuity gate —
  `modal_shown{Class:"permission"}` + non-empty `ModalID` asserted before the
  answer is sealed — and the turn-completion assertion upgrades from #1030's
  `drainForAssistantReply` (M1 only) to #1153's `drainForCompletedTurn` (M1
  non-empty `assistant_delta` **then** M2 terminal `turn_state{idle}`), so
  "modal answered but the turn never resumed" fails loud rather than greening
  vacuously. Zero production files touched; inlining over parameterizing
  #1030's shipped harness follows the #1153 precedent (avoids a file-overlap
  edit to another ticket's test file). Split from #1083, blocked-by #1153.
  See [`codebase/1154.md`](../codebase/1154.md).

- `interactive_stream_running_turn_test.go` (#1172) — reusable trigger infra
  that holds a live claude turn in `turn_state{responding}` for a bounded
  window, plus the smoke (`TestInteractiveStreamRunningTurn`) that proves it.
  Ports the desktop `e1fe219` fix (2026-07-17): a silent `sleep` gets
  backgrounded by claude (turn ends early), a chatty per-iteration loop
  floods the frame stream (delays `turn_state` delivery) — the working
  approach drives the turn with a bounded, silent, foreground shell loop
  inside one Bash-tool call. Load-bearing mechanism: the turn emitter
  (`cmd/pyry/interactive_turn_v2.go`) fires `responding` at `ToolStart`
  (before the command runs) and `idle` only once at `TurnEnd`, so a turn
  running an `L`-second loop holds `responding` for all of `L` — observing
  `responding` then verifying no `idle` for `hold < L` is the running-turn
  proof, and it fails loud on an early `idle` rather than passing silently.
  `startStreamRunningTurnHarness` transcribes #1153's setup verbatim (pairs
  **without** `--allow-remote-permissions`, spawns via `spawnBootstrapDaemon`
  so no permission modal blocks the Bash call — opposite posture from the
  modal specs #1030/#1154). No content/echo assertion — real claude's output
  is non-deterministic; only `turn_state` transitions and elapsed time are
  asserted. `startStreamRunningTurnHarness` + `driveRunningTurn` +
  `drainForResponding` are the reusable seam #1176 (interrupt, already
  natively `blocked-by` this ticket) composes. Zero production files
  touched. See [`codebase/1172.md`](../codebase/1172.md).

- `interactive_stream_multiturn_continuity_test.go` (#1173) — the first
  real-`claude` coverage of **multi-turn continuity** on the stream-json
  runner; every prior stream spec (#1153/#1154/#1172) drives exactly one
  turn, so none proves the runner's core purpose — holding one live
  `claude` child's stdin open across many turns with context intact.
  `TestInteractiveStreamMultiTurnContinuity` transcribes #1153's setup
  verbatim, then drives a **3-entry turn plan** strictly sequentially over
  one held-open session instead of a single send: (1) **plant** — claude is
  told to remember a per-run-unique `PYRY<hex nonce>` token; (2)
  **filler** — an intervening turn with no bearing on the token, load-bearing
  because a bare 2-turn plant→recall wouldn't prove a turn ran *between* them
  without respawn; (3) **recall** — asks for the token back. Continuity is
  asserted by content — `strings.Contains(strings.ToUpper(recallText),
  token)` — rather than pid inspection (the harness exposes no child pid, and
  a memory-less respawned child cannot produce the token, so content memory
  is the stronger "no respawn" observable). New helper
  `drainForCompletedTurnText` is a text-capturing superset of #1153's
  `drainForCompletedTurn`: byte-identical M1/M2 milestone semantics, plus
  accumulating every matching `assistant_delta.Text` into the return value
  instead of stopping at the first delta — kept as a separate helper rather
  than parameterizing the shared drain, since editing the shared one would
  touch the two already-merged sibling call sites (#1153, #1154). Fresh seed
  constants (`streamMultiTurnBootstrapUUID`/`streamMultiTurnConvID`). Zero
  production files touched. Split from #1083; siblings #1174 (new-session
  rotation) and #1175 (permission DENY) are out of scope here. See
  [`codebase/1173.md`](../codebase/1173.md).

- `interactive_stream_interrupt_test.go` (#1176) — the real-`claude`
  counterpart of the fakeclaude interrupt proof
  (`TestRelayV2_StreamInterruptStopsRunningTurn`, #1136), closing the
  fake-green/real-red gap (#949) on the interrupt path.
  `TestInteractiveStreamInterruptStopsRunningTurn` composes #1172's seam
  verbatim (`startStreamRunningTurnHarness` + `driveRunningTurn` +
  `drainForResponding`) to put a genuinely-running live turn in flight, sends
  a payload-less `TypeInterrupt` envelope (routes via the active cursor →
  `resolveBoundRunner` → the running turn's bound runner), and asserts the
  turn stops **cancelled** via a new drain, `drainForCancelledTurnEnd` — its
  vacuous-pass guard is the reason it exists as its own helper rather than a
  generic type-targeted drain: the first `turn_end` for the conversation must
  carry `StopReason == "cancelled"`, since the 40s running-turn loop *will*
  complete naturally (`"end_turn"`) if the interrupt no-ops. A trivial fourth
  turn drained via #1153's `drainForCompletedTurn` proves the session stays
  usable afterwards. Deliberately bootstrap-bound rather than minting a
  second conversation — AC only requires the interrupt reach the *running
  turn's* bound runner (proven here), not cross-conversation isolation
  (unit-owned deterministically by #1121). No new package-level constants,
  zero production files touched. Split from #1083. See
  [`codebase/1176.md`](../codebase/1176.md).
