# Spec: Stream e2e harness helper + `send_message` liveness spec (#1141)

Split from #1135 (harness+spec half). Blocked-by #1140 (fakeclaude stream-json
mode) — **merged**. This ticket is the glue: a harness helper that opts a daemon
into `interactive_runner: "stream-json"`, plus one integrated spec proving a
single `send_message` turn drains end-to-end through a live relay round-trip.

## Files to read first

Read these before writing anything. Each line says what to extract.

- `internal/e2e/harness.go:309-366` — `StartRotationWithRelay`: the relay-leg
  template (insecure-relay env, `-pyry-relay` flag, `seedBootstrapRegistry`,
  `spawnWith`). The new helper mirrors this **minus** the sessions-dir / trigger
  / stdin-log machinery.
- `internal/e2e/harness.go:389-414` — `seedBootstrapRegistry`: raw-JSON registry
  seed that pins the bootstrap pool id to `initialUUID`. Note the doc comment
  explaining why it uses a raw string, not `restart_test.go`'s helper — the file
  compiles under `e2e || e2e_install`, so it may only reference symbols visible
  in **both** builds. Your new helper lives here and inherits that constraint.
- `internal/e2e/harness.go:541-595` — `spawnWith` / `spawnOpts`: how `claudeBin`,
  `claudeArgs`, `extraFlags`, `extraEnv` are threaded to the child. `childEnv`
  (620-633) already sets `HOME=home`.
- `internal/e2e/relay_v2_new_session_test.go:61-180` — the closest spec template:
  pair → `seedBoundConversation` → handshake-interactive → drive one
  `send_message` → await sealed `Ack`. Lift the pairing + handshake + send +
  ack-await verbatim; **replace** the on-disk rotation assertion with the
  event-drain assertion (below).
- `internal/e2e/relay_v2_interrupt_test.go:220-320` — the ordered event-drain
  loop shape (`ReceiveBytes` → `InnerFrameV2` → `decryptInnerEnvelope` →
  type-switch on `env.Type`, two ordered `t.Fatal` milestones). Your drain reuses
  this shape for `assistant_delta` then `turn_state{idle}`.
- `cmd/pyry/main.go:651-677` — `selectInteractiveRunner`: the production toggle
  the helper drives via config. `"stream-json"` → the stream runner factory + the
  turn-event sink; `""`/`"pty"` → nil (PTY, byte-identical). This is why the
  helper writes `config.json` rather than passing a flag.
- `cmd/pyry/pair.go:50-58` — `resolveConfigPath` → `<home>/.pyry/config.json`
  (per-user, not per-instance). This is the file the helper writes.
- `internal/config/config.go:15-35, 46-64` — `Config.InteractiveRunner` field +
  `Load` overlay semantics: a partial `config.json` with only `interactive_runner`
  keeps every other field at its default. The `-pyry-relay` flag overrides the
  config's `relay_url`, so the helper need only write the one field.
- `internal/e2e/internal/fakeclaude/main.go:434-444, 1117-1205` — the merged
  stream mode: gate above `mustEnv` (binds no sessions dir), `runStreamJSON` reads
  `{"type":"user",…}` and emits one assistant text line (echoing the prompt) +
  one `result{subtype:"success"}` line per turn.
- `cmd/pyry/interactive_turn_v2.go:140-234, 281-305` — the emitter's per-event
  mapping. `TextChunk` → `turn_state{responding}` + a coalesced `assistant_delta`;
  `TurnEnd` → `turn_end` + `turn_state{idle}`. This fixes the exact observable
  wire order (see § Data flow).
- `cmd/pyry/stream_turn_drain.go:112-143` — `startStreamTurnDrainV2`: the
  relay-leg drain that gates each event on `activeSession()`. Wired in production
  by #1081 (merged). Read only to understand why the bootstrap pool id must equal
  the bound session id — you don't touch it.
- `internal/streamsup/envelope.go:43-68` — `marshalTurnEnvelope`: the daemon's
  outbound user envelope. Byte-for-byte the shape `fakeclaude.userTurnText`
  decodes — confirming the phone→daemon→fakeclaude leg is symmetric by
  construction (proven at unit level by #1140).
- Shared helpers to reuse (do **not** re-implement): `decodePairPayload`
  (`pair_test.go:277`), `driveHandshakeToOpenDaemonInteractive`
  (`relay_two_phone_structured_test.go:501`), `waitBinaryHello` /
  `decryptInnerEnvelope` (`relay_v2_daemon_test.go:77,378`), `readInnerFrame` /
  `sendNoiseMsg` (`relay_v2_handshake_test.go:166,341`), `readPersistedServerID`
  (`harness.go:863`), `claudeSessionsDir` (`rotation_test.go:36`, **e2e-only** —
  callable from the test, not from harness.go).

## Context

The stream interactive runner is fully wired and unit-tested on the daemon side:
the config toggle (`selectInteractiveRunner`, #1081), the runner factory
(`newStreamRunnerFactory`, #1109/#1098), the turn drain
(`startStreamTurnDrainV2` → `interactiveTurnEmitterV2`, #1098), and the
fakeclaude stream-json mode (#1140). Every leg of the wire has been proven in
isolation. **What has never run is all of them together, live, against a real
spawned fakeclaude.** This ticket supplies (a) the harness helper that opts a
daemon into stream mode, and (b) the one spec that closes the loop.

The config-file-injection pattern — writing `interactive_runner: "stream-json"`
into `<home>/.pyry/config.json` before spawn — does not exist anywhere in
`internal/e2e/` yet; this ticket establishes it. The interrupt (#1136),
new_session (#1137), queue (#1138), modal-permission (#1139) stream specs and the
real-claude capstone (#1083) all ride the helper this ticket adds.

## Design

Two deliverables. One new helper in `internal/e2e/harness.go`; one new spec file
`internal/e2e/relay_v2_stream_send_test.go`.

### 1. Harness helper (`internal/e2e/harness.go`)

New exported helper, placed beside `StartRotationWithRelay`:

```go
// StartStreamInteractiveWithRelay starts a daemon under
// interactive_runner:"stream-json" (production toggle via <home>/.pyry/config.json)
// with the stream-json fakeclaude as the supervised child and relay wiring, so a
// spec can drive phone → relay → daemon → stream-runner → fakeclaude and observe
// the turn drain. initialUUID pins the bootstrap pool id (the caller binds its
// conversation to it via seedBoundConversation). relayURL is the /v2/server
// endpoint. extraEnv is appended verbatim for the rider specs (#1136–#1139).
func StartStreamInteractiveWithRelay(t *testing.T, home, initialUUID, relayURL string, extraEnv ...string) *Harness
```

Behavior (contract, not implementation):

1. `os.MkdirAll(<home>/.pyry, 0o700)`, then write `<home>/.pyry/config.json` =
   `{"interactive_runner":"stream-json"}`. Use a **raw JSON string literal** (like
   `seedBootstrapRegistry`), not an `internal/config` import — harness.go is
   `e2e || e2e_install`-tagged and must stay import-lean. Must happen **before**
   spawn: the daemon reads config once at startup via `resolveConfigPath()`.
2. `fakeBin := ensureFakeClaudeBuilt(t)`.
3. `seedBootstrapRegistry(t, home, initialUUID)` — pins the bootstrap pool id to
   `initialUUID` so it equals the drain's bound-session tag (see § Why the ids
   must line up).
4. `spawnWith(t, home, spawnOpts{...})` with:
   - `claudeBin: fakeBin`, `claudeArgs: []string{}`.
   - `extraFlags: {"-pyry-workdir=" + home, "-pyry-relay=" + relayURL}`.
   - `extraEnv:` `PYRY_ALLOW_INSECURE_RELAY=1`, `PYRY_MOBILE_V2=1`,
     `PYRY_FAKE_CLAUDE_STREAM_JSON=1`, then `extraEnv...`.
   - **Do NOT set** `PYRY_FAKE_CLAUDE_SESSIONS_DIR` / `INITIAL_UUID` / `TRIGGER` /
     `STDIN_LOG`. Stream-mode fakeclaude short-circuits above `mustEnv` (#1140),
     so those envs are dead weight and misrepresent the child's needs.
5. Build the `Harness`, register `t.Cleanup(teardown)`, `waitForReady()`, return —
   identical to the tail of `StartRotationWithRelay`. Leave `ClaudeSessionsDir`
   unset (stream mode opens no transcript).

The helper does **not** create the daemon's computed `claudeSessionsDir`
(`claudeSessionsDir` is e2e-only and unreferenceable from harness.go). See
§ Open questions for whether the daemon's rotation watcher needs it; if it does,
the **test** creates it (it is e2e-tagged and can call `claudeSessionsDir`).

### 2. The spec (`internal/e2e/relay_v2_stream_send_test.go`, `//go:build e2e`)

`TestRelayV2_StreamSendMessageDrainsTurn`. Constants: `initialUUID`,
`knownConvID` (distinct UUIDv4 stems), a distinctive `knownUserText`
(e.g. `"e2e-1141-user:ping\n"`), a `sendReqID`.

Setup (lift from `relay_v2_new_session_test.go:69-134`):

1. `home := shortHome(t)`; pair one device; `decodePairPayload`; decode the
   server static pubkey.
2. `seedBoundConversation(t, home, knownConvID, initialUUID)` — so
   `sessionRouter.Route` resolves `knownConvID` → the bootstrap session
   (`initialUUID`) and stamps the active-conversation cursor.
3. *(Defensive, see Open questions)* `os.MkdirAll(claudeSessionsDir(home), 0o700)`.
4. `fr := fakerelay.New(...)`; `h := StartStreamInteractiveWithRelay(t, home,
   initialUUID, fr.URL()+"/v2/server")`; register cleanups.
5. `serverID := readPersistedServerID(t, home)`; `waitBinaryHello(t, fr, serverID)`.
6. Dial `fakephone`; `sendA, recvA := driveHandshakeToOpenDaemonInteractive(...)`
   — interactive is the capability the structured stream requires.

Drive + assert:

7. Marshal + seal one `send_message` (`ConversationID: knownConvID`,
   `Text: knownUserText`); `sendNoiseMsg`. Await the sealed `Ack`
   (`InReplyTo == sendReqID`), 15s deadline. The ack proves the turn was accepted
   and the cursor stamped — it does **not** prove delivery (delivery is async).
8. **Drain loop** (mirror `relay_v2_interrupt_test.go`'s ordered-milestone shape,
   ~20s deadline). Read `phoneA.ReceiveBytes` → decode `InnerFrameV2` → on
   `TypeNoiseMsg`, `decryptInnerEnvelope` → `env`. Two ordered milestones:
   - **M1 — `assistant_delta`:** on `env.Type == protocol.TypeAssistantDelta`,
     decode `AssistantDeltaPayload`; assert `ConversationID == knownConvID` and
     `strings.Contains(payload.Text, "e2e-1141-user:ping")` (the echo carries the
     sent prompt — the non-vacuity guard: it proves the full
     phone→daemon→fakeclaude→daemon→phone round-trip, not merely "some text").
     Set `sawDelta`.
   - **M2 — terminal `turn_state`:** *after* `sawDelta`, on
     `env.Type == protocol.TypeTurnState`, decode `TurnStatePayload`; when
     `State == "idle"` (and `ConversationID == knownConvID`), the turn closed —
     done. A leading `turn_state{responding}` arrives **before** the delta; ignore
     turn_states until `sawDelta` is set.
   Each milestone gets its own `t.Fatal` with a diagnostic naming what failed to
   drain (delivery never reached the child / parser / drain / emitter).

### Data flow (the wire this spec proves)

```
phone send_message(knownConvID, "…ping")
  → dispatch → sessionRouter.Route(knownConvID)      // stamps active cursor = knownConvID
  → newInboundDeliver → boundSession.WriteUserTurn(payload="…ping")
  → streamRunner.WriteUserTurn → streamsup WriteTurn → marshalTurnEnvelope
      → {"type":"user","message":{"role":"user","content":[{"type":"text","text":"…ping"}]}}\n
  → fakeclaude stdin (stream mode): userTurnText → writeStreamResponse
      → {"type":"assistant",…"text":"…ping"}\n   (echo)
      → {"type":"result","subtype":"success",…}\n
  → daemon: streamsup.Parser (runner's Stdout) → turnevent.TextChunk, turnevent.TurnEnd{end_turn}
  → sink.sinkFor(initialUUID) → startStreamTurnDrainV2 → activeSession()==initialUUID (gate ✓)
  → interactiveTurnEmitterV2.Handle
      → turn_state{responding}  → assistant_delta{"…ping"}  → turn_end  → turn_state{idle}
  → sealed push → phone observes: [responding], assistant_delta, [turn_end], idle
```

### Why the ids must line up (the one non-obvious invariant)

The drain gate (`startStreamTurnDrainV2`) forwards an event to the emitter only
when the event's tag equals `activeSession()`. Two ids must be equal:

- The **sink tag** is the runner's construction-time `cfg.SessionID`
  (`newStreamRunnerFactory` → `sink.sinkFor(cfg.SessionID)`), which is the
  bootstrap **pool id** — pinned to `initialUUID` by `seedBootstrapRegistry`.
- `activeSession()` resolves the active conversation → its bound session id
  (#1081's `boundSessionIDForActive`) = `knownConvID`'s binding =
  `initialUUID` via `seedBoundConversation`.

So `seedBootstrapRegistry(initialUUID)` + `seedBoundConversation(knownConvID,
initialUUID)` is what makes the gate pass. Mismatched ids ⇒ every event dropped
at the gate ⇒ the drain hangs and M1 fails — that is the single most likely
failure if a UUID is fat-fingered.

## Concurrency model

None new. The relay-leg drain goroutine (`startStreamTurnDrainV2`), the runner's
Run/spawn loop, and the parser on claude's stdout forwarder are all shipped and
tested; this ticket only observes them. The test's own drain loop runs on the
test goroutine, reading the phone conn serially (single reader) — the same shape
the interrupt/new_session specs use. `make e2e` runs with `-race`; the design
adds no shared state.

## Error handling / failure modes

- **Cold-start no-live-child window.** `streamRunner.WriteUserTurn` returns the
  retryable `ErrNoLiveChild` while the child is between spawn and stdin-ready. The
  daemon's inbound queue retries delivery, so the turn lands once the child is
  live. The ack precedes delivery, so the test must **not** treat the ack as the
  turn — it drains the events (M1/M2) as the real completion signal. The ~20s
  drain deadline absorbs the spawn + first-turn latency.
- **Gate-mismatch hang** — covered above (§ Why the ids must line up); surfaces as
  an M1 timeout with a clear diagnostic, not a silent pass.
- **PTY suite untouched (AC3).** The helper writes config / env only into its own
  isolated `home`; it adds no global state. The stream-json fakeclaude mode is
  env-gated and default-off (#1140), so every existing spec that does not set
  `PYRY_FAKE_CLAUDE_STREAM_JSON` is byte-identical. Verify by running the full
  `make e2e` (existing + new) green.

## Testing strategy

- The one new spec **is** the integration test — there is no unit surface to add
  (the helper is un-provable without a spec; that coupling is why #1135 kept them
  in one ticket).
- Non-vacuity is structural: the `assistant_delta` must carry the **echoed
  prompt** (M1's `Contains`), and the terminal `turn_state` must be `idle` (M2) —
  a hung drain, a wrong-conversation stamp, or a dropped turn all fail loudly.
  Same discipline as the interrupt test's "only source of a turn_end" guard.
- `make e2e` must pass with the new spec compiled in (AC3). Because
  `internal/e2e` is `-tags=e2e`, also sanity-check the default `go build ./...` /
  `go test ./...` are unaffected (the new file is build-tagged out).

## Open questions

- **Does the daemon's rotation watcher require `claudeSessionsDir` to exist at
  startup under stream mode?** The stream drain never tails a transcript, but the
  daemon may still start its fsnotify rotation watcher over the computed dir. The
  spec includes a defensive `os.MkdirAll(claudeSessionsDir(home), 0o700)` in the
  **test** (e2e-scoped). If, on first run, the daemon comes ready without it (the
  watcher tolerates an absent dir), drop that line — it is belt only. If startup
  hangs `waitForReady` without it, keep it. Resolve empirically on first green.
- **Is `PYRY_MOBILE_V2=1` still required** for the `/v2/server` leg, or has V2
  become the default? Both current V2 relay specs set it, so the helper bakes it
  in. If it is now a no-op, it is harmless; leave it for parity with the templates
  unless a build/vet flags it dead.

## Acceptance criteria

- [ ] `StartStreamInteractiveWithRelay` added to `internal/e2e/harness.go`:
  writes `<home>/.pyry/config.json` = `{"interactive_runner":"stream-json"}`
  before spawn, seeds the bootstrap registry at `initialUUID`, spawns the
  stream-json fakeclaude (`PYRY_FAKE_CLAUDE_STREAM_JSON=1`) with the relay leg,
  and sets none of the `SESSIONS_DIR` / `INITIAL_UUID` / `TRIGGER` child envs.
- [ ] `internal/e2e/relay_v2_stream_send_test.go` drives one `send_message` under
  the toggle and asserts the client observes an `assistant_delta` (echoing the
  prompt, `ConversationID == knownConvID`) followed by a `turn_state`
  (`State == "idle"`, `ConversationID == knownConvID`).
- [ ] `make e2e` passes with the new spec included; every existing PTY e2e spec is
  unmodified and green (stream-json fakeclaude is env-gated / default-off, so the
  rollback path stays proven).
