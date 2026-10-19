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

- `interactive_stream_liveness_test.go` — `TestInteractiveStreamLiveness`
  pairs, seeds a bootstrap session and bound conversation, spawns the daemon
  with `writeStreamInteractiveConfig`, then handshakes and sends one brief
  tool-free prompt. `drainForCompletedTurn` requires at least one
  non-whitespace `assistant_delta` for the driving conversation, followed by
  that conversation's terminal `turn_state{idle}`, within 120 seconds.
  It accumulates every matching text fragment and decrypts every Noise frame
  in receive order, including foreign-conversation traffic, to preserve nonce
  synchronization. Empty or whitespace-only text and foreign delta/idle frames
  cannot satisfy the milestones. An idle queued before the first matching
  nonempty delta is ignored: cancellation can leave that idle behind when
  `drainForCancelledTurnEnd` returns at `turn_end`, so a later idle is still
  required. The interrupt test's subsequent healthy turn exercises this path.
  Generated wording, paragraph counts and provider-dependent chunk counts are
  outside basic liveness: a valid response can coalesce into one delta.
  The former two-chunk requirement from #2271 was removed after source audit
  in #3616; no live failure was reproduced. Incremental delivery is proved by
  the [deterministic parser/emitter test](streamsup-package-draining-turnevents-into-the-interactive-emitter-fan-in.md#incremental-delivery-proof).
  See [controls and counted evidence](#stream-liveness-controls-and-evidence).

  **Known gap (found reviewing #2250, not fixed there): the `protocol.TypeRateLimited` arm of
  `drainForCompletedTurn` can false-red since `internal/streamsup`'s rate-limit gate grew a falling
  edge.** The arm accepts only the one non-benign status on record and `Fatalf`s on anything else, on
  the premise that a live run observes at most one `rate_limited` reading. A falling edge — the
  producer publishing a *benign* reading after a non-benign one on the same parser — can now reach it
  across a mid-session child respawn, and a benign status hits the fatal branch with a message that
  points at the wrong cause. Reproducing it needs a warning-band account plus a mid-session respawn, a
  combination no committed capture produces, so this is a named risk rather than an observed failure;
  see [streamsup-package-system-thinking-tokens-turnevent-thinkingpro.md](streamsup-package-system-thinking-tokens-turnevent-thinkingpro.md)
  for the producer-side gate.

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

## Stream liveness controls and evidence

`TestCompletedTurnDrainControls` drives the same `drainForCompletedTurn` used
by live liveness through the local `completedTurnReceiver` interface. Fresh
paired Noise states encrypt in-memory frames; no Claude credentials, daemon or
network are required. Fourteen cases cover single/multiple chunks, empty and
whitespace-only text, foreign deltas and idle, missing closure, queued idle
without/with a later closure, mixed foreign/valid traffic, and whitespace then
idle then text without another idle. Invalid evidence must fail with the
appropriate M1/M2 diagnostic. Bounded helper subprocesses isolate expected
`Fatalf` outcomes and check their diagnostic, rather than accepting any failure.

The remaining controls retain the global `unrecognized_message` alarm and
`rate_limited` alarm. Rate limits have no conversation filter: even a foreign
conversation's unknown status fails. Only the measured `allowed_warning`
status is tolerated; it still needs valid response and closure evidence.

**Shared-helper controls need a caller check.** One-delta controls would stay
green if the live test still called a separate minimum-two-chunk drain.
`TestInteractiveStreamLivenessUsesCompletedTurnDrain` parses the live test and
requires its call to the checked helper. It failed against the old caller,
without pinning generated text. Keep that connection when changing the drain.

Focused credential-free race selectors, from the repository root:

```sh
python3 scripts/test-admission.py -- go test -timeout=30m -tags e2e_realclaude -race -count=1 -run '^(TestCompletedTurnDrainControls|TestInteractiveStreamLivenessUsesCompletedTurnDrain)$' ./internal/e2e/realclaude
python3 scripts/test-admission.py -- go test -timeout=30m -race -count=1 -run '^(TestStreamTurnDrainV2_FlushTimerCoalesces|TestInteractiveTurnEmitterV2_TimerFlushMidMessage|TestInteractiveTurnEmitterV2_FlushAtTurnBoundary)$' ./cmd/pyry
```

The parser/emitter selector is untagged. The drain controls require
`e2e_realclaude` even though they are credential-free. Tagged compilation alone
does not prove live liveness; its named body must execute and pass, with zero
skips. See [live-suite evidence rules](e2e-realclaude.md).

### Counted macOS acceptance for #3616

The [verifier record](https://github.com/pyrycode/pyrycode/pull/3621#issuecomment-6265012109)
binds focused race checks to `c19a849d61a5dd91bae0cd8db61bf298b80724fe` on
`macbook`, macOS 26.6.2 (25G83), Darwin 25.6.0 arm64. Drain controls plus the
caller check passed 16/16 test events (14 cases, their parent and the caller
check), with zero failures/skips. The affected drain/emitter selection passed
13/13, including all three tests in the selector above, with zero failures/skips.
Tagged live-package vet/compilation also passed.

The [dispatcher live report](https://github.com/pyrycode/pyrycode/issues/3616#issuecomment-6265961868)
ran on the macOS `macbook` host against that candidate merged with
`origin/main` at `664305939d73e5a146457503081d8232fcca5407`.
Run `b6c99de33f64bf07f4f3bfb20e5313403780c7b56d1e3c38f89afe379c8e913c`,
output `2026-10-19T10-58-51-851Z`, used
`go test -tags e2e_realclaude -timeout 20m -json ./internal/e2e/realclaude/...`.
The daemon was built from that tested tree. Named outcomes from its Go JSON:

| Test selection | Executed | Passed | Failed | Skipped |
| --- | ---: | ---: | ---: | ---: |
| `TestInteractiveStreamLiveness` (7.34 s) | 1 | 1 | 0 | 0 |
| `TestInteractiveStreamInterruptStopsRunningTurn` (5.59 s) | 1 | 1 | 0 | 0 |
| `TestInteractiveStreamLivenessUsesCompletedTurnDrain` | 1 | 1 | 0 | 0 |
| `TestCompletedTurnDrainControls` cases (excluding parent) | 14 | 14 | 0 | 0 |
| Complete live suite, original run | 2025 | 2023 | 2 | 25 |

The full suite exited 1. The dispatcher accepted the inherited
`TestRealClaude_BackgroundReplyCompletion` failure after same-tree and base
failures, tracked in [#3623](https://github.com/pyrycode/pyrycode/issues/3623).
`TestCodexQuestionLive` failed initially and passed on the same-tree rerun.
Those original failures remain failures; liveness itself executed and passed.
The accepted live-stage record and evidence bundle retain the run identity
above. Linux verification is outside this macOS ticket's scope.
