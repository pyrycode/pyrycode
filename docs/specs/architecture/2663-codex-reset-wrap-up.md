# #2663 — a Codex reset runs the wrap-up turn and writes a handoff note

Short plan: no new type, no new state machine, no new failure mode. Three
small wirings make an existing path reachable for Codex.

## Files read

- `cmd/pyry/main.go` → `activeSessionStarter.start` — the wrap-up arm is gated
  on `runner.State().ChildPID != 0`; `resetThenRotate` emits the three
  `resetting` edges around `conversationReset.wrapUp`.
- `cmd/pyry/session_reset.go` → `conversationReset.wrapUp`, `wrapUpCapturer`,
  `resetTarget`, `storeNote` — asserts the capability off the runner, interrupts
  (a refusal is tolerated), arms the capture, writes the prompt, waits for TurnEnd.
- `cmd/pyry/wrapup_capture.go` → `wrapUpCapture` — the sink decorator; its
  placement rule (upstream of the droppable fan-in send and the active-session
  gate) holds for Codex exactly as for Claude.
- `cmd/pyry/streamsup_runner.go` → `streamRunner.BeginWrapUp`,
  `newStreamRunnerFactory` — the pattern mirrored.
- `cmd/pyry/codex_runner.go` → `newCodexRunnerFactory`, `codexRunnerConfig`,
  `codexRunner.State`, `codexRunner.runOnce`, `codexRunner.notify` — the sink
  every translated event goes through, and the only place a client is bound
  and unbound.
- `internal/codexsup/client.go` → `Client`, `Start` — holds the `*exec.Cmd`
  (nil for the in-memory test peer) but exposes no pid.
- `cmd/pyry/resetting_v2_test.go` → `attachedEmitter`, `assertEdges`,
  `newResettingBcast`; `cmd/pyry/session_reset_test.go` → `newFakeNotes` — the
  doubles the new end-to-end row reuses.
- `internal/e2e/internal/fakecodex/main.go` — a plain turn replies
  `fakecodex reply` and completes the turn.

## Change

1. `codexsup.Client.PID() int` — the app-server's pid, 0 for the in-memory
   peer (`cmd == nil`). The pid of a process that has exited is still returned;
   liveness is the caller's concern, as it is for `streamsup`.
2. `codexRunner.runOnce` sets `r.state.ChildPID = client.PID()` in the same
   `r.mu` section that binds `r.client` and sets `PhaseRunning`, and clears it to
   0 in the section that unbinds `r.client` after `Done`/ctx. So `State` reports
   a pid exactly while a client is bound; every other phase (starting, backoff,
   stopped, evicted) reads 0. The `State` doc stops saying ChildPID stays 0.
3. `codexRunnerConfig` gains `WrapUp *wrapUpCapture`; `newCodexRunnerFactory`
   mints `wrapUp := newWrapUpCapture(h.sink.sinkForTag(tag.ID))` and passes
   `Sink: wrapUp.Sink, WrapUp: wrapUp` — the same position in the chain
   `newStreamRunnerFactory` gives it (parser side of the fan-in channel).
4. `func (r *codexRunner) BeginWrapUp() (*wrapUpReply, func(), bool)` returns
   `r.cfg.WrapUp.begin()`; a nil capture refuses, so a runner built without one
   (every existing test literal) is inert, as a hand-built `streamRunner{}` is.

Nothing else moves: `conversationReset.wrapUp` already calls `Interrupt` (a
Codex refusal with no running turn is tolerated there), writes through the
resolved target's `write` (production's inbound deliver, which reaches
`codexRunner.WriteUserTurn`), and waits on the capture's TurnEnd, which
`codexsup.Translator` emits on `turn/completed`.

## Testing strategy

- `internal/codexsup/client_test.go`: `TestClientPID` — a started fake reports a
  pid > 0; the in-memory peer reports 0.
- `cmd/pyry/codex_runner_test.go`: `TestCodexRunner_ChildPIDWhileRunning` (AC 1)
  — 0 before Run, the bound client's `PID()` once bound, 0 after Run returns.
- `cmd/pyry/codex_reset_test.go`: `TestCodexReset_WritesHandoffNote` (AC 2) —
  runner built through the real `newCodexRunnerFactory` over the fake Codex, an
  `activeSessionStarter` whose `reset` is a real `conversationReset` (target
  write = the runner's `WriteUserTurn`, notes = `newFakeNotes`) and whose
  `resetting` is `attachedEmitter`; `StartNewSessionLate` on the conversation
  defers, the outcome arrives nil, the stored note is `fakecodex reply`, and the
  edges are `wrapping_up/pending`, `restarting/written`, falling.

## Documentation handoff

None named by the ticket. Pending for the documentation stage: the Codex
runner's overview may note that a Codex reset now writes a handoff note.
