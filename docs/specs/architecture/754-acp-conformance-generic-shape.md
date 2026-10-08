# Spec #754 — Zed conformance check for generic-ACP shape

The proof-of-correctness **capstone** of epic #600. One new test file drives the
whole `pyry acp` surface — `initialize` → `session/new` → `session/prompt` +
scripted turn → a permission round-trip → `session/cancel` — against a scripted
ACP-host harness over the real `internal/acp` transport, and asserts the emitted
wire shape is the **generic Zed/spec dialect (ADR 027)**, not any single host's
aliases. It observes all six ADR 027 divergences in one run and locks the wire
strings against future drift.

**This ticket is test-only: exactly one new file, `cmd/pyry/acp_conformance_test.go`,
and zero production files.** Every surface it exercises has already landed and is
unit-tested per-ticket; the capstone's job is the *integration* (they compose in
one session) plus a *dialect lock* (the wire strings equal the literal generic
values — a guard the per-ticket tests do not provide, because they compare the
emitted frame against the same constant whose value could silently change).

## Context

Epic #600 makes `pyry acp` a thin adapter over the shared remote-head core; ADR 027
is the in-repo mapping contract. Its six divergences are each proven by a
per-ticket test:

| Divergence | Already proven by |
|---|---|
| 1 — end-of-turn is the `session/prompt` **return**, not an update | `TestACPTurnStream_TurnEndMapsAllReasons`, `…ResolvesHeldPromptAfterNotifications` |
| 2 — permission is a blocking agent→client request | `TestACPPermissionStreams_ScriptedModalRoutesSelection` |
| 3, 4 — no busy/queue/stall/snapshot on the wire | `TestACPTurnStreams_StallSurfacesOnStderrNotWire`; busy/queue/snapshot have no `turnevent` type at all |
| 5 — no `fs/*` or `terminal/*` in `initialize` | `TestACP_Initialize_Result` |
| 6 — one interactive claude per session | `TestACP_SessionNew_SpawnsOneInteractiveClaude` |

Because every divergence already has a test, this capstone adds exactly two things
nothing else provides:

1. **Integration.** One scripted host drives the whole protocol against one
   session id over one transport, proving the divergences *compose* — the outbound
   dialect, the blocking permission Call, the held-prompt return, and the interactive
   spawn all coexist in a single session flow.
2. **Dialect lock.** The per-ticket tests compare emitted frames against the
   `acpbridge.SessionUpdate*` / `turnevent.*` **constants** — so renaming a
   constant's *value* to an opencode alias would not fail them. This test asserts
   those values equal the **literal** ADR 027 strings, the actual drift guard
   AC-2 demands ("the generic discriminants … not opencode aliases").

## Design

Two tests share one harness, all in `cmd/pyry/acp_conformance_test.go`.

### Why not `serveACPWithPool`

The full-session drive needs to inject *scripted* turn events and a *scripted*
permission modal, because the fake claude is a sleeping shell script (`acp_test.go:155`)
that writes no `<id>.jsonl` and renders no PTY modal — so the production outbound
path (`streams.start(id)` → `NewTargetSubscriber` over the live `Session.Events()`)
parks forever and never emits. `serveACPWithPool` hard-wires that production path
and does not expose its `*acp.Transport`, so the test cannot attach scripted
outbound to it.

The harness therefore **mirrors `serveACPWithPool`'s register-closure** using the
same handler constructors (a compile-time-checked mirror, not a drift-prone
re-derivation — a constructor signature change breaks the test), with two
substitutions that make scripted injection possible:

- the streams manager is built with **`dir == ""`** so `newSessionHandler`'s
  internal `streams.start(id)` is a no-op (real outbound disabled);
- `session/prompt` resolves to a **`newRecordingDeliverer(false)`**, not the pool
  session — so `WriteUserTurn` commits and the hold stays held until the *scripted*
  `TurnEnd` resolves it (a real fake-claude session would fail delivery and resolve
  the hold with an error first).

The real fake-claude pool still backs `session/new` / `session/load` /
`session/cancel`, so the **interactive-spawn argv proof (divergence 6 + cost) is
genuine**, and one real session id ties the whole flow together.

### The conformance harness (`newConformanceHarness`)

A struct + constructor assembling the composed transport and a **concurrent frame
classifier** — the scripted ACP host. Contracts only:

- **Composition.** Reuse `newFakeClaudePool(t)`; `pool.Run` on a goroutine, wait
  `pool.Ready()`. Build `holds := newPromptHolds(logger)` and
  `streams := newACPTurnStreams(runCtx, pool, "" /*dir*/, holds.end, logger)`.
  Create `tr := acp.New(hostToAgentR, agentToHostW, logger)` over two `io.Pipe`s,
  `streams.attach(tr)`, register the mirrored handler set (prompt handler's
  `resolve` returns the recording deliverer; cancel handler's `resolve` is
  `resolveCancelTarget(pool, …)`), and run `tr.Serve(runCtx)` on a goroutine.
- **Scripted outbound, bound after `session/new`** (a method, e.g.
  `attachScriptedOutbound(id string)`): build `sink := newACPTurnStream(tr, id,
  func(r){ holds.end(id, r) }, logger)` and a turn producer (`turnbridge.New` with
  `scriptedSubscriber{turnCh}.subscribe` + `sink.Handle`) on a goroutine; build
  `proxy := newACPPermissionProxy(tr, kb, id, generousTimeout, logger)` (caller =
  the real transport, `kb := newSyncKeystroker()`) and
  `runPermissionModalStream(runCtx, scriptedSubscriber{permCh}.subscribe, proxy)`
  on a goroutine. Expose `turnCh`, `permCh`, `kb`, and `sink`.
- **Concurrent frame classifier** — the crux of "extend the harness." A reader
  goroutine loops `reader.ReadBytes('\n')` on `agentToHostR`, `json.Unmarshal`s each
  line into a `map[string]any`, and classifies by shape (mirrors
  `acp.handleLine`'s classification, `acp.go:204-218`):
  - **reply** (`id` present, `method` absent) → send on a buffered `replies chan`
    the test reads;
  - **`session/update` notification** (`method=="session/update"`, no `id`) →
    append to a mutex-guarded `notifications []map[string]any`;
  - **inbound request** (`method=="session/request_permission"` with an `id`) →
    capture its `params` (mutex-guarded), then **auto-answer**: write
    `{"jsonrpc":"2.0","id":<that id>,"result":<selectedResp(h.answerOptionID)>}` +
    `"\n"` to `hostToAgentW`. `answerOptionID` is a harness field the test sets
    (e.g. `"reject_once"`).

  An unbuffered `io.Pipe` blocks each write until read, so this **continuous drain
  is mandatory** — the producer's `Notify` and the proxy's `Call` write to
  `agentToHostW` asynchronously and would otherwise deadlock a "send-all-then-read"
  test. The classifier is that real host read loop.
- **`send(frame string)`** writes a host→agent request; **`shutdown()`** closes
  `hostToAgentW`, cancels `runCtx`, joins the Serve + producer + drain + reader
  goroutines (reuse the `served`/`waitClosed` discipline), so post-shutdown reads
  of `notifications`/`kb`/`stderr` are race-free.

Do **not** mutate `acpHarness` in `acp_test.go` — build this as a sibling in the
new file reusing the same package-level primitives. Keeps the change to one file,
zero shared-file edits, zero merge surface.

### Test 1 — `TestACPConformance_FullSessionDrive`

One scripted host, one real session id, five phases. Phases C and D do not
interleave (the permission modal stream and the turn stream are independent
goroutines), which keeps every assertion deterministic:

- **A — handshake (divergence 5).** `send(initialize …)`; read the reply from
  `replies`; assert `protocolVersion == SupportedProtocolVersion` and the result
  bytes contain none of `"fs"`,`"terminal"`,`"readTextFile"`,`"writeTextFile"`
  (reuse the AC-2b substring check from `TestACP_Initialize_Result`).
- **B — session (divergence 6 + cost).** `send(session/new)`; read reply; assert
  `sessions.ValidID(id)` and `assertPinnedModes`; `waitOneClaudeArgv(t, argvFile)`
  == `["--session-id", id]` (the interactive-path / one-claude proof, reused
  verbatim). Then `attachScriptedOutbound(id)`.
- **C — permission round-trip (divergence 2).** Set `answerOptionID = "reject_once"`
  (the 3rd option — proves index→digit is real, not "always 1"). `permCh <-
  permissionShown()`; `kb.waitRouted(t)` must be `"answer:3"`. Assert the captured
  Call params: `sessionId == id`; `Options` equals the four
  `{optionId,name,kind}` in fixed order with kinds
  `allow_once`/`allow_always`/`reject_once`/`reject_always`, each
  `turnevent.PermissionOptionKind(kind).Valid()`.
- **D — turn + prompt return (divergences 1, 3; the generic dialect).**
  `send(promptFrame(t, 2, id, "hi"))` (held: recording deliverer commits). Feed
  `turnCh`: a `thinking` entry, a `text` entry, a `tool_use` entry (via
  `jsonlStreamEvent(streamEntry(...))`, mirroring
  `…ScriptedTurnEmitsOrderedFrames`), a `tuidriver.Event{Kind: EventKindStallDetected}`,
  then `endOfTurnEvent()`; `close(turnCh)`; cancel + join the producer (or drive to
  quiescence). Read the prompt reply from `replies`: assert
  `result.stopReason == "end_turn"`. Assert `notifications`: exactly **three**
  `session/update`, discriminants `agent_thought_chunk`, `agent_message_chunk`,
  `tool_call` in order (the generic dialect; **no** stall/busy/queue frame — div 3);
  the prompt reply arrived only after all three (div 1 — reuse the "result strictly
  after notifications" ordering assertion; because the producer's single Run
  goroutine emits notifications then `onTurnEnd`→`holds.end`, this is structural).
- **E — cancel + cancelled return (divergence 1, `cancelled`).**
  `send(session/prompt id=3)` (a fresh hold — the first was freed in D);
  `send(session/cancel notification)` (accepted, no reply, actuates SendEsc
  best-effort on the real supervisor). The producer is already joined, so feed the
  **sink directly**: `h.sink.Handle(turnevent.TurnEnd{Reason:
  turnevent.TurnEndReasonCancelled})` — race-free, and the only way to produce
  `cancelled` (mapper.go:24-29 forces `end_turn` on the scripted JSONL path). Read
  the reply: assert `result.stopReason == "cancelled"`.

Then `shutdown()`.

### Test 2 — `TestACPConformance_DialectLock`

Pure, no goroutines, no harness — the drift guard. Assert the wire strings equal
the literal ADR 027 generic values (write the wants as string literals, exactly as
`TestACPTurnStream_TurnEndMapsAllReasons` does, "so a future divergence fails
here"):

- `acpbridge.MethodSessionUpdate == "session/update"`; the four
  `acpbridge.SessionUpdate{AgentMessageChunk,AgentThoughtChunk,ToolCall,ToolCallUpdate}`
  equal `"agent_message_chunk"`,`"agent_thought_chunk"`,`"tool_call"`,`"tool_call_update"`.
- The five `string(turnevent.TurnEndReason*)` equal `"end_turn"`,`"max_tokens"`,
  `"max_turn_requests"`,`"refusal"`,`"cancelled"`.
- The four `string(turnevent.PermissionOptionKind*)` equal `"allow_once"`,
  `"allow_always"`,`"reject_once"`,`"reject_always"`, each `.Valid()`.
- The client→agent method names pyry registers are the spec names
  (`initialize`,`authenticate`,`session/new`,`session/load`,`session/prompt`,
  `session/cancel`,`session/set_mode`,`session/set_config_option`) and the one
  agent→client method is `session/request_permission` (`methodSessionRequestPermission`).

A one-line comment on this test states it is the generic-vs-opencode-alias lock the
per-ticket tests (which compare against the same constants) cannot be.

## Concurrency model

- **Goroutines while the drive runs:** the transport `Serve` loop (reads
  `hostToAgentR`), the classifier reader (reads `agentToHostR`, writes answers to
  `hostToAgentW`), the turn producer's `Run`, the permission drain, and — briefly —
  one permission round-trip. All parented on `runCtx`.
- **No deadlock on the unbuffered pipes.** The proxy issues its `Call` on the
  round-trip goroutine, off the Serve loop (`acp.go:284` contract), so Serve stays
  free to read the host's answer. The classifier reader drains `agentToHostW`
  continuously, so producer `Notify` / proxy `Call` writes never block. The
  classifier's answer write to `hostToAgentW` is consumed by the always-reading
  Serve loop.
- **Writer serialization** is the transport's `writeMu` (`acp.go:79`): notifications
  (producer goroutine), the Call (round-trip goroutine), and replies (Serve loop)
  interleave safely.
- **Determinism.** Phases are sequenced, not raced: C completes (Call read +
  answered + `kb.waitRouted`) before D begins; D joins the producer before reading
  `notifications`; E feeds the sink only after the producer is joined. Scripted
  channel sends are the same blocking sync points the turn-stream tests rely on.
- **Shutdown / no leak:** `shutdown()` closes host stdin and cancels `runCtx`;
  every goroutine has a ctx-bounded exit (Serve on pipe-close, producer/drain on
  ctx, round-trip on `cctx`), joined before assertions read shared state.

## Error handling

Test-only; "errors" are `t.Fatalf`. Guard the flakes:

- Every blocking wait (`replies` read, `kb.waitRouted`, goroutine joins) has a
  timeout via `waitClosed` / `select … time.After`, never a bare receive — a wedged
  phase fails as a named timeout, not a hung `make check`.
- The prompt hold **must** be resolved by the scripted `TurnEnd`, not a delivery
  failure: `newRecordingDeliverer(false)` (non-gated) is load-bearing. A gated or
  real-session deliverer would fail `WriteUserTurn` and resolve the hold with
  `CodeInternalError` before the scripted turn-end, breaking div 1.
- `answerOptionID` must be one of the four surfaced ids or the proxy denies
  (`forged_option`) and phase C sees `"esc"` — assert `"answer:3"` catches a wrong id.

## Testing strategy

- Runs under `make check` (`go vet`, `staticcheck`, `go test -race`), no live Zed,
  no network — the fake-claude pool + in-memory pipes are the only I/O. `make check`
  stays green.
- Run `go test -race -run TestACPConformance ./cmd/pyry/` locally, ideally
  `-count=3`, to shake out any ordering flake in the classifier before pushing.
- The two tests may run `t.Parallel()` (each owns its harness / pool).
- Confirm non-vacuity: the div-3 assertion must prove the stall produced **zero**
  extra frames (exactly three notifications), not merely "no frame labelled stall."

## Open questions

- **Fold vs. separate `cancelled` test.** Phase E folds the `cancelled` facet into
  the full-session drive via a direct `sink.Handle`. If the developer finds the
  producer-joined-then-direct-Handle sequencing awkward, splitting it into a tiny
  standalone `TestACPConformance_CancelledStopReason` (buffer-backed sink + held
  prompt + direct `TurnEnd{cancelled}`, mirroring `TurnEndMapsAllReasons`) is an
  acceptable equivalent — same assertion, same technique, no behavioural change.
- **Harness placement.** The spec keeps the conformance harness in the new file to
  hold the change to one file. If a later ACP test wants the concurrent classifier,
  extracting it is a follow-up refactor, out of scope here.
