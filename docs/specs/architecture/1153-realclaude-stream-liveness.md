# Spec: Real-claude e2e — stream liveness under `interactive_runner: "stream-json"` (#1153)

## Files to read first

Read these before writing anything. The new file is a composition of two existing
templates: the #854 real-claude daemon-relay body (structure, spawn, seeds, Noise
wire) and the #1141 fake-side stream drain (the two-milestone assertion). Almost
every helper is reused verbatim.

- `internal/e2e/realclaude/interactive_bootstrap_liveness_test.go` (#854) — **THE
  template; read the whole file.** The new test copies this body near-verbatim.
  Reuse these package-private symbols UNCHANGED (same package, same build tag — no
  new export, no edit to this file): `spawnBootstrapDaemon`, `bootstrapDaemon` +
  `stop`/`waitForReady`, `driveHandshakeInteractive`, `buildHelloEarlyInteractive`,
  `sealSendMessage`, `sendNoiseMsg`/`sendNoiseInit`/`readInnerFrame`, `mustJSON`,
  `seedBootstrapRegistry`, `seedBoundConversation`, `readPersistedServerID`,
  `waitBinaryHello`, `decodePairPayload`, `runPyry`, `shortSocketPath`,
  `relayTestLogger`, `lockedBuffer`. Note line 122: `spawnBootstrapDaemon` spawns
  `-- --model haiku --dangerously-skip-permissions` — reused as-is.
- `internal/e2e/relay_v2_stream_send_test.go:149-217` (#1141, fake side) — the
  **two-milestone drain** the new `drainForCompletedTurn` mirrors: M1 = first
  non-empty `assistant_delta` for the conv, M2 = terminal `turn_state{idle}` for
  the conv observed AFTER M1. Extract the loop shape (decrypt-in-order, skip
  `turn_state{responding}` until `sawDelta`, milestone-specific timeout messages).
  **Do NOT copy the `echoNeedle` content check (lines 194-197)** — real claude does
  not echo the prompt; assert only non-empty text.
- `internal/e2e/harness.go:388-406` (#1141 fake harness `StartStreamInteractiveWithRelay`)
  — the exact config-toggle recipe to transcribe into `writeStreamInteractiveConfig`:
  `os.MkdirAll(<home>/.pyry, 0o700)` then
  `os.WriteFile(<home>/.pyry/config.json, []byte(`{"interactive_runner":"stream-json"}`), 0o600)`.
  A raw JSON literal (no `internal/config` import) keeps the realclaude package
  import-lean, mirroring `seedBootstrapRegistry`.
- `cmd/pyry/main.go:667-676` — `selectInteractiveRunner`: confirms the accepted
  toggle string is exactly `"stream-json"` (also `""`/`"pty"` = default PTY). This
  is the production seam being proven end-to-end.
- `cmd/pyry/pair.go:50-58` — `resolveConfigPath`: config is `<home>/.pyry/config.json`,
  **per-user, NOT per-instance** (independent of `-pyry-name=test`). Confirms
  `writeStreamInteractiveConfig` writes to the same path the daemon reads under the
  isolated authenticated HOME.
- `cmd/pyry/streamsup_runner.go:91-160` — `newStreamRunnerFactory` +
  `mapStreamsupConfig` + `stripSessionIDFlags`: confirms the daemon's
  `--model haiku --dangerously-skip-permissions` passthrough survives into
  streamsup (only `--session-id`/`--resume` are stripped; streamsup re-injects the
  stream-json I/O flags). No test code here — read to confirm the spawn is compatible.
- `docs/knowledge/features/e2e-realclaude.md` — suite conventions the new file must
  match: header `//go:build e2e_realclaude` (single tag), `WithWorktreeAuthenticated`
  skip contract, `make e2e-realclaude`, **no CI workflow**, no `-race`, `t.Parallel()`
  NOT called. §"What's there today" for the `WithWorktreeAuthenticated` fixture.
- `internal/protocol/interactive.go:16-70` — `TurnStatePayload` (`.State`,
  `.ConversationID`), `AssistantDeltaPayload` (`.ConversationID`, `.Text`, `.Seq`).
  The envelope fields the drain decodes.

## Context

Per the always-a-real-claude-gate policy (2026-07-08), every operator-facing
happy-path flow needs a real-claude e2e that actually RUNS in the pre-ship gate
before the Mac daemon binary is swapped — the operator must never be the first
real-stack execution. The stream-json interactive runner
(`interactive_runner: "stream-json"`, #1081) is proven against **fakeclaude** by
the #1141 liveness test + the #1136/#1137/#1138/#1139 riders, but has never run the
interactive relay path against **real** claude. This ticket adds that rung: the
real-claude counterpart of `TestRelayV2_StreamSendMessageDrainsTurn` (#1141).

This is the **shared-harness slice** of the #1083 real-claude gate. It introduces
the reusable config-toggle helper (`writeStreamInteractiveConfig`) that the
permission-flow rider **#1154** (blocked-by this ticket, `security-sensitive`)
composes with `spawnPermissionDaemon` (from #1030) instead of `spawnBootstrapDaemon`.
Keeping the toggle a standalone helper — not a spawn wrapper — is what makes it
reusable across both permission modes (see § Design, "Why a config-writer, not a
spawn wrapper").

The only realclaude test that drives the daemon interactive relay path is #854
(and its #997/#1028/#1030/#1031 siblings). They ALL run under the **default PTY**
interactive runner. This is the first realclaude test to flip
`interactive_runner: "stream-json"`.

## Design

Purely additive, test-only. **Zero production source files.** One new file in
`internal/e2e/realclaude/`, two new package-private helpers, reusing every #854
helper verbatim (no edit to `interactive_bootstrap_liveness_test.go` or
`fixtures.go`).

### New file: `internal/e2e/realclaude/interactive_stream_liveness_test.go`

Header: `//go:build e2e_realclaude`, `package realclaude`.

#### Helper 1 — `writeStreamInteractiveConfig(t *testing.T, home string)`

The reusable seam #1154 rides. Contract: writes `<home>/.pyry/config.json`
containing exactly `{"interactive_runner":"stream-json"}` at mode `0o600` (mkdir
`.pyry` at `0o700` first). Must be called BEFORE the daemon spawns —
`resolveConfigPath` reads the file once at startup. Transcribe from
`StartStreamInteractiveWithRelay` (harness.go:388-406). ~8 lines. `t.Helper()`.

#### Helper 2 — `drainForCompletedTurn(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, convID string, timeout time.Duration)`

The stronger drain AC #2 requires ("text delta(s) **followed by turn completion**").
#854's `drainForAssistantReply` stops at the first delta; this one continues to turn
close. Mirrors #1141's drain loop (relay_v2_stream_send_test.go:149-217) with the
real-claude adaptation:

- Single reader on the test goroutine; decrypt every `noise_msg` frame in receive
  order (the receive nonce is sequential — skipping a decrypt desyncs the
  `CipherState`). Non-`noise_msg` inner frames (e.g. rekey) do not advance the
  nonce — `continue` without decrypting. (Same discipline as
  `drainForAssistantReply`, lines 205-221.)
- **M1**: first `assistant_delta` with `ConversationID == convID` and
  `strings.TrimSpace(Text) != ""`. Set `sawDelta = true`. **No content/echo
  assertion** (real claude's words are non-deterministic).
- **M2**: after `sawDelta`, a `turn_state` with `State == "idle"` and
  `ConversationID == convID` → the turn closed → `return`. Ignore
  `turn_state{responding}` (leads the delta) until `sawDelta`.
- Timeout: on deadline, milestone-specific `t.Fatalf` — if `!sawDelta`, "never
  observed a non-empty assistant_delta … the turn never drained end-to-end"; else
  "observed the delta but never terminal turn_state{idle} … the turn opened but
  never closed". Name the likely cause (UUID mismatch between the two seeds → drain
  gate drops every event) as #1141 does.

Use `turn_state{idle}` as the completion milestone (mirrors #1141) rather than
`turn_end`: it is the LAST envelope the emitter produces
(`responding` → deltas → `turn_end` → `idle`), so it is the strongest "closed"
signal and gives the developer a byte-adjacent template. ~55 lines.

#### Test — `TestInteractiveStreamLiveness(t *testing.T)`

Copy #854's `TestInteractiveBootstrapLiveness` body (lines 82-154) with three
deltas:

1. After `WithWorktreeAuthenticated(t)` and the workdir mkdir, and BEFORE
   `spawnBootstrapDaemon`, call `writeStreamInteractiveConfig(t, home)`. (Order
   relative to `pair` doesn't matter; before spawn does.)
2. Drive **ONE** turn (AC #2 says "one real turn"; the fake counterpart #1141
   drives one). Replace #854's two-turn block with a single
   `sealSendMessage(...)` + `drainForCompletedTurn(t, phone, initRecv, liveConvID, 120*time.Second)`.
3. Prompt text: a short deterministic instruction with a per-run nonce, e.g.
   `fmt.Sprintf("Reply with a single short word. run=%d", time.Now().UnixNano())`
   (the nonce defeats accidental caching without asserting on content).

Everything else identical to #854: `exec.LookPath("claude")` skip guard, resolve
`claudeBin`, isolated `workdir` under `home`, `runPyry("pair", …)`,
`seedBootstrapRegistry(t, home, liveBootstrapUUID)` +
`seedBoundConversation(t, home, liveConvID, liveBootstrapUUID, workdir)`,
`fakerelay.New`, `spawnBootstrapDaemon`, `readPersistedServerID` +
`waitBinaryHello`, `fakephone.Dial`, `driveHandshakeInteractive`. Reuse #854's
`liveBootstrapUUID`/`liveConvID` constants? No — they are file-private to #854's
file; declare fresh package-private constants in the new file (distinct UUID
literals to avoid any confusion, though the two tests never run against shared
state). No `t.Parallel()` (`WithWorktreeAuthenticated` calls `t.Setenv`).

### Why the two seeds still gate the drain (the one non-obvious invariant)

The stream runner fans turnevents into `streamTurnSink` tagged by its
construction-time `cfg.SessionID` = the bootstrap pool id, pinned to
`liveBootstrapUUID` by `seedBootstrapRegistry`. The drain gate forwards to the
emitter only when the event tag == `activeSession()`, which resolves `liveConvID`'s
binding = `liveBootstrapUUID` via `seedBoundConversation`. Both seeds ⇒ gate
passes. A UUID mismatch drops every event and hangs the drain → surfaces as the M1
timeout, never a silent pass. (Identical to #1141's invariant, harness comment
lines 46-54. Note the stream path has **no** transcript-resolution deadlock —
that was the PTY-only #854 failure mode; the stream runner reads claude's stdout
directly, so only the gate matters here.)

### Why a config-writer, not a spawn wrapper

Two spawn helpers exist: `spawnBootstrapDaemon` (#854, with
`--dangerously-skip-permissions`) and `spawnPermissionDaemon` (#1030, without it —
the permission trigger). The stream-json toggle is orthogonal (it's `config.json`,
not argv). A spawn wrapper that delegated to `spawnBootstrapDaemon` could not serve
#1154, which MUST spawn without skip-permissions. So the reusable unit is the
config-writer, composed with whichever spawn helper the permission mode needs:
- #1153 (this ticket): `writeStreamInteractiveConfig` + `spawnBootstrapDaemon`.
- #1154 (rider): `writeStreamInteractiveConfig` + `spawnPermissionDaemon`.

## Concurrency model

Unchanged from #854. One spawned real `pyry` daemon (real `claude --model haiku`),
one headless phone. The test goroutine is the single reader of the phone conn
(`drainForCompletedTurn` reads serially; the sequential receive nonce forbids
concurrent reads). Daemon lifecycle: `spawnBootstrapDaemon` blocks until the
control socket is dialable; `t.Cleanup(d.stop)` does SIGTERM → 3s grace → SIGKILL.
`fakerelay` closed via `t.Cleanup`. No goroutines spun by the test itself → `-race`
stays off (matches the suite; the make target has no `-race`).

## Error handling / failure modes

- **No credential** → `WithWorktreeAuthenticated` skips in ms with the named-variable
  diagnostic (both `ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN`) before any
  workdir/daemon allocation. Must **not** time out on the skip path (AC #3). The
  `exec.LookPath("claude")` guard skips (not fatal) when claude is absent, matching
  #854 line 84-86 — CI has neither claude nor a credential, so the file compiles
  and skips cleanly (AC #5).
- **Drain never completes** → per-milestone `t.Fatalf` with the seed-mismatch
  hint (above). The 120s M1 budget absorbs real claude cold-spawn + model-load +
  first-turn latency (generous, mirrors #854's 120s).
- **Daemon fails to start** → `spawnBootstrapDaemon`'s `waitForReady` already
  `t.Fatalf`s with captured stderr.

## Testing strategy

- **Build-tag exclusion (AC #4):** file carries only `//go:build e2e_realclaude`.
  After landing, `make test 2>&1 | grep realclaude` is empty (or `[no test files]`);
  `make check` unchanged. No Makefile edit — `make e2e-realclaude` already globs
  `./internal/e2e/realclaude/...`.
- **Compiles + skips clean in CI (AC #5):** verify `go vet -tags e2e_realclaude
  ./internal/e2e/realclaude/...` builds, and that with no creds the test skips
  (not hangs). Developer runs `make e2e-realclaude` locally in the code-review
  phase — the always-real-claude gate — to confirm ONE real turn drains M1→M2
  against live claude (~$0.01, one haiku turn).
- **No new fixture surface / no production change:** confirm the diff is one new
  `*_test.go` file only.

## Open questions

- **`turn_state{idle}` vs `turn_end` as M2.** Spec picks `turn_state{idle}`
  (mirrors #1141, strongest close signal). If, when the developer runs it, real
  claude's stream path reliably emits `turn_end` but the terminal `turn_state{idle}`
  is flaky/absent, fall back to `turn_end` (`protocol.TypeTurnEnd` /
  `TurnEndPayload`) as the M2 milestone — same drain shape, one envelope earlier.
  Both are AC-valid "turn completion"; decide from the observed real-claude stream.
- **One turn vs two.** Spec drives one (AC + #1141 parity). If the developer wants
  the bridge-survives-past-turn-1 proof for the stream path, a second turn is a
  cheap add — but it is NOT required by the AC and the session-control tests
  (#1031) already cover respawn survival. Leave at one unless code-review requests.
