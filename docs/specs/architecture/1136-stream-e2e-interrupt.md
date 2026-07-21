# Spec: Stream e2e — per-conversation interrupt honoured under `interactive_runner:"stream-json"` (#1136)

Split from #1082. Blocked-by the harness ticket #1141 (`StartStreamInteractiveWithRelay`
+ the fakeclaude stream-json mode) — **merged** (PR #1143). This is a **rider** on that
helper: it adds one env-gated behaviour to the shared stream-json fakeclaude (honour an
interrupt) and one e2e spec that drives a live interrupt against a conversation on its
**own minted runner** and observes the turn end **cancelled**.

Not security-sensitive: test-only. The interrupt routing production code (streamsup
primitive #1120 + per-conversation routing #1121) is already built and was
security-reviewed; this spec adds no production surface, only exercises it end-to-end.

## Files to read first

Read these before writing anything. Each line says what to extract.

- `internal/e2e/relay_v2_stream_send_test.go` (whole, 218 lines) — the **direct
  template**: pair → handshake-interactive → drive one `send_message` → await sealed
  `Ack` → ordered-milestone drain loop (`ReceiveBytes` → `InnerFrameV2` →
  `decryptInnerEnvelope` → type-switch). Lift the pairing/handshake/send/ack + the
  drain-loop scaffolding almost verbatim; this ticket swaps the drive/observe steps.
- `internal/e2e/relay_v2_interrupt_test.go:185-345` — the **PTY sibling's** two-phase
  ordered shape you mirror: observe the turn mid-flight FIRST, THEN seal + send the
  `interrupt` frame (no payload — `protocol.Envelope{ID,Type:TypeInterrupt,TS}`), THEN
  observe the turn stop. Extract the "only source of a turn_end" **structural-causality**
  vacuous-pass guard (lines 40-53, 279-292) — this ticket reuses the identical guard on
  the stream path. Note: it does NOT assert `StopReason` (a tui-driver limitation); the
  stream path CAN and DOES (see § The observable).
- `internal/e2e/relay_v2_modal_perconv_test.go:159-218` — the **create_conversation →
  `conversation_created` mint dance** + send-to-minted + ack-await. Lift the mint block
  verbatim (all-null `CreateConversationPayload{}`, drain until `TypeConversationCreated`,
  read `ConversationCreatedPayload.ID`). Line 92-95's comment confirms a minted
  per-conversation child **inherits the daemon's fakeclaude env** — the fact AC3 relies
  on so the minted child comes up in interrupt mode.
- `internal/e2e/internal/fakeclaude/main.go:434-444` — the stream-mode gate in `main()`
  (checked first, above `mustEnv`). The one call site whose argument changes.
- `internal/e2e/internal/fakeclaude/main.go:1111-1205` — `runStreamJSON`,
  `writeStreamResponse`, `writeJSONLine`, and the `outResult{Type,Subtype,SessionID}`
  struct (`:1111`). This is the exact code the interrupt mode extends: `outResult` already
  models the result line; the new mode reuses it with `Subtype:"error_during_execution"`.
- `internal/e2e/internal/fakeclaude/main.go:260-265` — the `env*` const block; the new
  `envStreamInterrupt` const lands beside `envStreamJSON`.
- `internal/e2e/internal/fakeclaude/stream_detect_test.go` (whole, 179 lines) — the unit
  pattern to extend: `userTurnLine` (hand-mirrored inbound envelope), `parseEmitted`
  (drives fakeclaude's stdout through the **real** `streamsup.Parser` — the
  different-fabric check). Its `runStreamJSON(...)` calls (lines 50, 88, 122) are 3 of the
  4 call sites the new bool param touches; each just gains `, false`.
- `internal/streamsup/parser.go:147-180` — `consumeLine`'s `result` arm +
  `resultTurnEndReason`: **`subtype "error_during_execution"` → `TurnEndReasonCancelled`;
  every other subtype → `end_turn`.** This mapping is why the fake must emit
  `error_during_execution` (not `success`) to signal an interrupted turn. Do not touch it.
- `internal/streamsup/envelope.go:70-124` — `controlRequest` / `controlRequestInner`
  (`{"type":"control_request","request_id":…,"request":{"subtype":"interrupt"}}`) and
  `WriteInterrupt`. This is the **exact line the daemon writes to the child's stdin** on an
  interrupt; the fake's new decoder must recognise `type=="control_request"` +
  `request.subtype=="interrupt"`.
- `internal/turnbridge/outbound.go:87-95` — `MapEvent`'s `TurnEnd` arm →
  `TurnEndPayload{StopReason: string(e.Reason)}`. So `TurnEndReasonCancelled` reaches the
  phone as `turn_end` with `StopReason == "cancelled"` (`internal/protocol/interactive.go:73-77`).
- `cmd/pyry/interactive_turn_v2.go:208-217` — the emitter's `TurnEnd` arm: flush delta →
  emit `turn_end` → transition to `turn_state{idle}`. Fixes the observable wire order
  after the interrupt.
- `cmd/pyry/stream_turn_drain.go:112-143` — `startStreamTurnDrainV2`: the **follow-active
  gate** (`env.sessionID != activeSession() → drop before Handle`). Read to understand the
  AC3 observability boundary (§ What AC3 proves): a non-active session's events are masked
  at the phone, so a phone-observable "other conversation unaffected" negative is unsound —
  runner-level isolation is unit-owned by #1121, below.
- `cmd/pyry/interrupt_routing_test.go` (whole) — #1121's **deterministic unit isolation**:
  `TestActiveInterrupter` proves the bound runner is interrupted exactly once and the
  bootstrap runner **zero** times; `TestResolveBoundRunner` proves an unbound conversation
  never resolves to bootstrap. AC3's cross-conversation isolation is owned here (different
  fabric, deterministic); the e2e confirms the routing **target** live.
- `internal/e2e/harness.go:388-437` — `StartStreamInteractiveWithRelay` (as-built): writes
  `config.json interactive_runner:"stream-json"`, seeds the bootstrap registry at
  `initialUUID`, spawns the stream fakeclaude, appends `extraEnv...`. The test passes
  `"PYRY_FAKE_CLAUDE_STREAM_INTERRUPT=1"` as `extraEnv`; `spawnWith` (`:658`) sets it on the
  daemon process env, inherited by both the bootstrap and the minted child.
- Shared helpers to reuse (do **not** re-implement), all e2e-tagged: `decodePairPayload`
  (`pair_test.go`), `driveHandshakeToOpenDaemonInteractive`
  (`relay_two_phone_structured_test.go`), `waitBinaryHello` / `decryptInnerEnvelope`
  (`relay_v2_daemon_test.go`), `readInnerFrame` / `sendNoiseMsg`
  (`relay_v2_handshake_test.go`), `readPersistedServerID` (`harness.go`), `mustJSON`.

## Context

#1141 proved the stream-json interactive runner drains ONE `send_message` end-to-end
against a live fakeclaude. This ticket proves the next behaviour on that path: a
phone-originated **interrupt** ends the running turn, observed live under the toggle.

The production interrupt path is fully built and unit-tested: the streamsup primitive
(`(*streamsup.Runner).Interrupt()` → a `control_request` line, #1120), the per-conversation
routing (`activeInterrupter` → `resolveBoundRunner` → the active conversation's bound
runner, #1121), and the parser's interrupt classification
(`error_during_execution → cancelled`, #1120). What has never run is all of them
**together, live**, with a fakeclaude that is genuinely mid-turn when the interrupt
arrives. This spec supplies (a) the fakeclaude behaviour that models an in-flight turn and
honours the interrupt, and (b) the one spec that closes the loop.

**Rider fake-scoping.** The shared stream-json fakeclaude (#1140/#1141) echoes each user
turn as one assistant line + one `result{success}` line, and **ignores** a
`control_request`. That default is correct for the send/new_session/queue riders and must
stay byte-identical for them. This ticket adds a **default-off, env-gated** interrupt mode
that (i) withholds the `result` on a user turn (the turn stays in flight) and (ii) emits a
`result{error_during_execution}` when the interrupt `control_request` arrives. Gating keeps
every other rider on the untouched default — the same discipline #1140's `envStreamJSON`
gate established.

## The observable (what "interrupted turn_state" means on the wire)

AC2 says "the turn ends interrupted and the client observes the interrupted turn_state."
The turn-state taxonomy has only `thinking` / `responding` / `idle`
(`turnbridge/outbound.go:39-41`) — there is **no distinct "interrupted" state value**. The
interrupted signal is carried by the **`turn_end` envelope's `StopReason`**:

```
fakeclaude result{subtype:"error_during_execution"}
  → streamsup.Parser.consumeLine → turnevent.TurnEnd{Reason: TurnEndReasonCancelled}   (parser.go:157,173-180)
  → interactiveTurnEmitterV2 (TurnEnd arm)                                              (interactive_turn_v2.go:208-217)
  → turnbridge.MapEvent → protocol.TurnEndPayload{StopReason: "cancelled"}              (outbound.go:87-95)
  → sealed push → phone observes turn_end{StopReason:"cancelled"} then turn_state{idle}
```

So the client-observable interrupt proof is **`turn_end` with `StopReason == "cancelled"`**
(scoped to the interrupted conversation). This is strictly stronger than the PTY sibling,
which observes only *that* a turn_end arrived and explicitly does **not** assert `StopReason`
(tui-driver's `EventKindJsonlEndOfTurn` cannot distinguish an interrupt-stop from a clean
end — `relay_v2_interrupt_test.go:55-61`). The stream parser *can* distinguish it, so this
spec asserts `StopReason == "cancelled"` — the distinguishing rigour of the stream path.

## Design

One production-ish file modified (`fakeclaude/main.go`, a test binary), one test file
extended (`stream_detect_test.go`), one new spec file
(`internal/e2e/relay_v2_stream_interrupt_test.go`). No new files under `cmd/` or
`internal/*` production packages; no production surface.

### 1. Fakeclaude stream-json **interrupt mode** (`internal/e2e/internal/fakeclaude/main.go`)

**a. New env const** beside `envStreamJSON` (`:263`):

```
envStreamInterrupt = "PYRY_FAKE_CLAUDE_STREAM_INTERRUPT"
```

**b. Gate call site** (`:441-444`) passes the mode through as a value (keeps
`runStreamJSON` a pure I/O seam, per #1140):

```
runStreamJSON(os.Stdin, os.Stdout, os.Getenv(envStreamInterrupt) != "")
```

**c. `runStreamJSON` gains one bool param** — `honorInterrupt`. Behaviour contract (not a
body): the read loop is unchanged (`bufio.ReadString('\n')`, per-line, resilient); only the
per-line action branches on the mode:

| Inbound line | `honorInterrupt == false` (default, unchanged) | `honorInterrupt == true` (this ticket) |
|---|---|---|
| `{"type":"user",…}` | echo assistant line **+** `result{success}` | echo assistant line **only** (withhold result — turn stays in flight) |
| `{"type":"control_request",…"subtype":"interrupt"}` | ignored | emit `result{subtype:"error_during_execution"}` |
| blank / unparsable / other | ignored | ignored |

**d. Small helpers** (each ~one screen, mirroring `userTurnText` / `writeStreamResponse`):
- Split `writeStreamResponse` so its assistant-echo half is reusable: an inner
  `writeAssistantEcho(w, msgID, text) error` (the assistant text line);
  `writeStreamResponse` = `writeAssistantEcho` + the `result{success}` line (byte-identical
  to today — the default path is unchanged).
- `writeInterruptedResult(w) error` — one `writeJSONLine(w, outResult{Type:"result",
  Subtype:"error_during_execution", SessionID: streamSessionID})`. Reuses the existing
  `outResult` struct (`:1111`).
- `interruptControlRequest(line []byte) bool` — mirrors `userTurnText`: `json.Unmarshal`
  into a minimal `{Type string; Request struct{ Subtype string }}`; return
  `Type=="control_request" && Request.Subtype=="interrupt"`. A non-matching / unparsable
  line returns false (the caller ignores it), preserving the parser's per-line resilience.

The mode is **stateless**: it emits an interrupted result on *each* interrupt
`control_request`. The spec drives exactly one interrupt during one in-flight turn, so this
is sufficient and simplest; no in-flight tracking is added (no AC needs it).

**e. Update the 3 existing `runStreamJSON` call sites** in `stream_detect_test.go` (lines
50, 88, 122) to pass `, false` — they assert the unchanged default mode (including
`TestRunStreamJSON_NonUserLinesIgnored`, which still holds: default mode ignores the
control_request).

### 2. Fakeclaude unit tests (extend `stream_detect_test.go`)

Fast, deterministic, driven through the **real** `streamsup.Parser` (`parseEmitted`) —
different fabric from the fake's writer, the #1140 discipline. Scenarios (as bullets, not
bodies):

- **In-flight user turn (mode on):** one user line → parser maps stdout to exactly ONE
  `TextChunk{Text: echoed prompt}` and **no `TurnEnd`** (result withheld). Proves the turn
  stays open.
- **Interrupt honoured (mode on):** a `control_request` interrupt line → parser maps stdout
  to exactly ONE `TurnEnd{Reason: TurnEndReasonCancelled}`. Proves the interrupt→cancelled
  classification end-to-end at the seam.
- **Full in-flight-then-interrupt sequence (mode on):** user line then control_request →
  `TextChunk` (echo) then `TurnEnd{Cancelled}`, in order. The unit analogue of the e2e.
- **Default mode still ignores control_request:** covered by the existing
  `TestRunStreamJSON_NonUserLinesIgnored` (now `runStreamJSON(..., false)`), unchanged.

### 3. The e2e spec (`internal/e2e/relay_v2_stream_interrupt_test.go`, `//go:build e2e`)

`TestRelayV2_StreamInterruptStopsRunningTurn`. Constants: `initialUUID` (bootstrap),
distinctive `knownUserText` (e.g. `"e2e-1136-user:go\n"`) + its `echoNeedle`, `sendReqID`,
`interruptReqID`. **No `seedBoundConversation`** — the interrupt target is *minted* over the
wire (see § What AC3 proves), so its runner is the bootstrap's, and only its own child
receives the control_request.

Setup:
1. `home := shortHome(t)`; pair one device; `decodePairPayload`; decode server pubkey.
2. `fr := fakerelay.New(...)`;
   `h := StartStreamInteractiveWithRelay(t, home, initialUUID, fr.URL()+"/v2/server",
   "PYRY_FAKE_CLAUDE_STREAM_INTERRUPT=1")` — the one extra env vs #1141. Register cleanups.
3. `serverID := readPersistedServerID`; `waitBinaryHello`; dial `fakephone`;
   `sendA, recvA := driveHandshakeToOpenDaemonInteractive(...)` (interactive — the
   capability the structured stream AND `handleInterrupt` require).

Drive + assert (mirror the modal-perconv mint block, then the interrupt test's two phases):
4. **Mint the target conversation** over the wire: seal a `create_conversation`
   (all-null payload) → drain to `TypeConversationCreated` → `convB := payload.ID`
   (`modal_perconv_test.go:159-190`). The minted session gets its **own** stream runner +
   its **own** stream fakeclaude child (interrupt mode inherited via the daemon env).
5. **Drive one `send_message`** (`ConversationID: convB`, `Text: knownUserText`); await the
   sealed `Ack` (`InReplyTo == sendReqID`, 15s). The ack proves the turn was accepted and
   the active cursor stamped to `convB` — not delivery (async). The send also makes `convB`
   the active conversation, so the interrupt (a payload-less global frame) routes to it.
6. **M1 — turn in flight (AC1):** drain until an `assistant_delta` with
   `ConversationID == convB` whose `Text` contains `echoNeedle`. The echo is the
   non-vacuity guard (full phone→daemon→fake→daemon→phone round-trip). A leading
   `turn_state{responding}` precedes it; ignore turn_states until the delta is seen.
   Observing the delta proves the turn is **running** when we send the interrupt (AC1).
7. **Send the interrupt** (only after M1): seal `protocol.Envelope{ID: interruptReqID,
   Type: protocol.TypeInterrupt, TS: …}` (no payload) with `sendA`; `sendNoiseMsg`.
8. **M2 — interrupted turn end (AC2 + AC3):** drain until a `turn_end` with
   `ConversationID == convB` **and `StopReason == "cancelled"`**. This is the interrupt
   proof: the fake emitted no `result` on the user turn, so the ONLY source of a `turn_end`
   is its response to the interrupt `control_request` (structural causality — the PTY
   test's guard), and `cancelled` (not `end_turn`) confirms it took the
   `error_during_execution` path, not a spontaneous end.

Each milestone gets its own ordered `t.Fatal` naming what failed to drain (turn never
started / interrupt never routed to the bound runner / classification wrong).

### Data flow (the wire this spec proves)

```
phone create_conversation → daemon mints session-B (stream RunnerFactory) → fakeclaude-B (interrupt mode)
phone send_message(convB,"…go") → sessionRouter.Route(convB)  [stamps active = convB]
  → boundSession.WriteUserTurn → streamsup WriteTurn → {"type":"user",…"text":"…go"}
  → fakeclaude-B (interrupt mode): echo {"type":"assistant",…"text":"…go"}   (NO result — turn stays in flight)
  → daemon Parser → TextChunk → drain gate (session-B == active ✓) → emitter
      → turn_state{responding, convB} → assistant_delta{"…go", convB}                    ── M1 (AC1)
phone interrupt (no payload) → handleInterrupt → activeInterrupter.SendEsc()
  → active=convB → resolveBoundRunner(convB) → session-B.Runner() → streamRunner.Interrupt()
  → WriteInterrupt → {"type":"control_request",…"subtype":"interrupt"} → fakeclaude-B stdin
  → fakeclaude-B: {"type":"result","subtype":"error_during_execution"}
  → daemon Parser → TurnEnd{Cancelled} → drain gate (session-B == active ✓) → emitter
      → turn_end{StopReason:"cancelled", convB} → turn_state{idle, convB}                 ── M2 (AC2/AC3)
```

## What AC3 proves (and the honest observability boundary)

AC3: "the interrupt does not affect any other conversation running concurrently under the
same daemon (per-conversation isolation)." The design meets this **soundly** as follows —
and deliberately does **not** ship a defence it cannot observe.

- **The interrupt target is a *minted* conversation on its own (non-bootstrap) runner.**
  This is what makes the e2e exercise #1121's routing. Had the target been a
  bootstrap-bound conversation (as in #1141's send test), `resolveBoundRunner` would return
  the bootstrap runner — indistinguishable from the pre-#1121 bug (interrupt →
  bootstrap). Minting forces the interrupt to reach `session-B`'s own child; the bootstrap
  session is the **other conversation/session running concurrently under the same daemon**,
  and it is provably not the target. Under the pre-#1121 bug the interrupt would hit the
  idle bootstrap (not active) → **no observable `turn_end`** → the test times out. Correct
  routing → `turn_end{cancelled, convB}`. So M2 IS the live isolation proof.

- **Runner-level cross-conversation isolation is unit-owned by #1121** — deterministically:
  `TestActiveInterrupter` asserts the bound runner is interrupted exactly once and a
  separate bootstrap fake **zero** times; `TestResolveBoundRunner` asserts an unbound
  conversation never resolves to bootstrap. This is the belt; the e2e is a different fabric
  confirming the wired target, not a re-derivation.

- **A phone-observable "second in-flight conversation stays untouched" negative would be
  unsound, so it is not written.** `startStreamTurnDrainV2` forwards only the *active*
  session's events (`stream_turn_drain.go:127-135`); a non-active conversation's events —
  including a (buggy) leaked `turn_end` — are dropped **before** the emitter and never reach
  the phone. So no phone-level assertion can distinguish "conversation C was left alone"
  from "conversation C was interrupted but its turn_end was gated." Adding a second driven
  conversation would be theatre that observes nothing new (and would add follow-active-switch
  drain complexity and flake surface). Per "Evidence-Based Fix Selection" and
  "Belt-and-Suspenders = different fabric," the isolation belt is the deterministic #1121
  unit; the e2e's contribution is the live routing-target confirmation above.

## Concurrency model

None new. The relay-leg drain goroutine, the runner Run/spawn loop, the parser on the
child's stdout, and the emitter are all shipped and tested; this spec only observes them.
The minted `session-B` adds a second stream runner + child, both driven by the same shipped
machinery. The test's own drain loop runs on the test goroutine, reading the phone conn
serially (single reader — the send/interrupt/new_session specs' shape). `runStreamJSON`
stays single-reader/single-writer on the fake's `main` goroutine (`-race` clean by
construction, per #1140). `make e2e` runs `-race`; the design adds no shared state.

## Error handling / failure modes

- **Cold-start no-live-child window.** `streamRunner.WriteUserTurn` returns the retryable
  `ErrNoLiveChild` between the minted child's spawn and stdin-ready; the daemon's inbound
  queue retries, so the turn lands once the child is live. The ack precedes delivery — the
  test drains M1 as the real "turn started" signal (~20s deadline absorbs mint + spawn +
  first-turn latency).
- **Interrupt before the child is live.** `(*streamsup.Runner).Interrupt()` →
  `WriteInterrupt` returns `ErrNoLiveChild` on a nil stdin; the relay `handleInterrupt`
  Warn-logs and tolerates it (best-effort, existing contract). The test sends the interrupt
  only **after** M1 (the child has echoed a turn), so the child is live — this window is not
  hit in the happy path, and if it were, the retry/observe structure still converges.
- **Vacuous-pass defence (structural).** The fake emits NO `result` on the user turn, so the
  only source of a `turn_end` is its interrupt response — a `turn_end` ⟺ the interrupt was
  received and honoured. Asserting `StopReason == "cancelled"` (a spontaneous end would be
  `end_turn`) is a second, independent guard. M1-before-interrupt / M2-after ordering is the
  third.
- **Gate-mismatch hang.** If the minted `session-B`'s bound id ever diverged from
  `activeSession()`, every event would drop at the gate and M1 would time out with a clear
  diagnostic — never a silent pass. (Not expected: minting binds `convB.CurrentSessionID`
  to `session-B`'s pool id, and `send_message(convB)` makes `convB` active, so
  `activeSession()` == `session-B` == the runner's sink tag.)
- **PTY suite untouched (AC4).** The interrupt mode is env-gated (`PYRY_FAKE_CLAUDE_STREAM_INTERRUPT`,
  default-off), and the helper writes config/env only into its own isolated `home`. The
  existing PTY interrupt spec (`relay_v2_interrupt_test.go`) sets none of the stream envs
  and is byte-identical. Verify with the full `make e2e` green.

## Testing strategy

- The new e2e spec **is** the integration test. The extended `stream_detect_test.go` units
  give fast, deterministic feedback on the fake's new behaviour through the real parser
  (different fabric) — the #1140 discipline.
- Non-vacuity is structural (withheld result → the only turn_end source is the interrupt)
  and value-checked (`StopReason == "cancelled"`, `ConversationID == convB`,
  `assistant_delta` carries the echo). A hung drain, a mis-routed interrupt, or a wrong
  classification all fail loudly.
- `make e2e` must pass with the new spec compiled in (AC5); the existing PTY interrupt spec
  is unmodified and green (AC4). Because `internal/e2e` is `-tags=e2e`, also sanity-check
  the default `go build ./...` / `go test ./...` (the new file is tagged out; the fakeclaude
  package builds under its own tags — the bool-param change compiles with its unit tests).

## Open questions

- **First e2e exercise of a *minted* stream-json runner.** #1141's send test used the
  bootstrap-bound session; this is the first spec to mint a conversation
  (`create_conversation`) under `interactive_runner:"stream-json"`, so the minted child
  comes up via `newStreamRunnerFactory` rather than the PTY factory. The pool-minting flow
  is shipped and unit-tested (#1109/#1125) and proven live on PTY (`modal_perconv_test.go`),
  and the minted child inherits the stream env, so this is expected to work — but if the
  minted child does not come up (M1 times out at conversation-created or at the first
  delta), the fallback is a **bootstrap-bound** target: `seedBoundConversation(convB,
  initialUUID)` instead of minting (mirrors #1141 exactly). That fallback loses the
  non-bootstrap-routing edge of AC3 (interrupt → bootstrap == bound, so it no longer
  distinguishes the pre-#1121 bug), so keep #1121's unit isolation cited as the isolation
  belt and note the degradation in the test doc. Resolve empirically on first green;
  prefer the minted target.
- **Is `PYRY_MOBILE_V2=1` still required** for the `/v2/server` leg? The helper bakes it in
  (parity with #1141); harmless if it has become a no-op. Not this ticket's concern.

## Acceptance criteria

- [ ] Fakeclaude stream-json **interrupt mode** added (`internal/e2e/internal/fakeclaude/main.go`),
  gated by `PYRY_FAKE_CLAUDE_STREAM_INTERRUPT` (default-off): a user turn emits the assistant
  echo line **only** (result withheld), and an interrupt `control_request` emits
  `result{subtype:"error_during_execution"}`. The default (mode-off) path is byte-identical
  for the send/new_session/queue riders.
- [ ] Fakeclaude unit tests (`stream_detect_test.go`) cover the interrupt mode through the
  real `streamsup.Parser`: in-flight user turn → `TextChunk` and no `TurnEnd`; interrupt
  `control_request` → `TurnEnd{Cancelled}`; the full sequence in order. Existing default-mode
  tests pass unchanged (`runStreamJSON(..., false)`).
- [ ] `internal/e2e/relay_v2_stream_interrupt_test.go` drives, under
  `interactive_runner:"stream-json"`, a turn that is **in flight** when the interrupt
  arrives (M1: `assistant_delta` echoing the prompt, `ConversationID == convB`), sends a
  payload-less `interrupt` frame, and asserts the client observes a `turn_end` with
  `StopReason == "cancelled"` and `ConversationID == convB` (M2). The target conversation is
  minted over the wire (its own runner), so M2 also proves the interrupt reaches the active
  conversation's bound runner, not the shared bootstrap (AC3; runner-level cross-conversation
  isolation is unit-owned by #1121).
- [ ] The existing PTY interrupt spec (`internal/e2e/relay_v2_interrupt_test.go`) is
  untouched and still green.
- [ ] `make e2e` is green with the new spec included.
