# #1486 — idle eviction defers on the stream turn-busy signal

## Files read

- `internal/sessions/session.go` → `Session` (the `attached` field, `idleTimeout`), `runActive` (the idle-timer arm), `Evict` docstring — the dead deferral and the docs that describe it.
- `internal/sessions/pool.go` → `Config.IdleTimeout`, `SessionConfig.IdleTimeout`, `New` (bootstrap `&Session{}`), `buildSession` (every other `&Session{}`), `Pool.idleTimeoutDefault` — the two production constructions a new per-session field must reach.
- `cmd/pyry/stream_turn_busy.go` → `turnBusyTracker.Busy`, `newTurnBusyTracker` — the signal; `Busy`'s doc explains why it stays one bool.
- `cmd/pyry/relay.go` → `conversationForSession` — the session→conversation resolver the tracker is already built over.
- `cmd/pyry/main.go` → the `if streamSink != nil` block that builds `turnBusy` immediately above `sessions.New`, and `newInboundDeliver` (Activate → `waitIdleForDelivery` → `openForDelivery` → write) — so a delivered-but-unfinished turn is marked busy from before the write until TurnEnd/exit.
- `internal/sessions/session_test.go` → `helperPoolIdle`, `pollUntil`, `TestSession_IdleEvictionFires` — fixture shape to mirror; the latter is the nil-signal regression cover.
- `internal/sessions/transition_test.go` → `transitionRecorder`, `TestPool_TransitionObserver_IdleEvictionFires` — how a test sees `ReasonEviction`.
- `internal/e2e/per_conversation_eviction_test.go` → `startPerConvHarness`, `dialHelloPhone`; `internal/e2e/relay_v2_stream_queue_drain_test.go` → the `PYRY_FAKE_CLAUDE_STREAM_HOLD` fixture and the "msg2 held in queue_state" vacuity guard; `internal/e2e/cap_test.go` → `waitForSessionState`.

## Context

`runActive` defers an idle eviction only while `Session.attached > 0`, and nothing has written `attached` since the terminal runner was deleted (#1348). With `-pyry-idle-timeout` set, claude is SIGKILLed exactly one window after activation even mid-turn. The daemon already tracks open turns per conversation on the stream path (`turnBusyTracker`); this ticket routes that bit into the idle-timer fire and deletes `attached`.

## Design

**`sessions.Config.TurnBusy func(SessionID) bool`** (new field). Optional. Reports whether the given session currently has a turn open. nil means "no signal" (PTY path, every existing test): the idle timer then evicts unconditionally, as today. Only a bool per session crosses the boundary — no conversation id.

- `Pool` gets a read-only-after-New `turnBusy` field (mirrors `idleTimeoutDefault`); `New` copies it onto the bootstrap `Session`, `buildSession` onto every other.
- `Session` gets `turnBusy func(SessionID) bool` beside `idleTimeout`; `attached` is deleted.
- `runActive`'s timer arm: `if s.turnBusy != nil && s.turnBusy(s.currentID())` → `timer.Reset(s.idleTimeout)`; `continue`. Called with no lock held (it reaches the conversations registry and the tracker mutex). Re-arm-on-fire shape unchanged: eviction may come up to one window after the turn ends.
- The cap-policy `evictCh` arm stays forced — it never consults the signal.

**`cmd/pyry/main.go`**: inside the existing `if streamSink != nil` block, build `sessionTurnBusy := func(id sessions.SessionID) bool { conv, ok := conversationForSession(convReg, string(id)); return ok && turnBusy.Busy(conv) }` and pass it as `TurnBusy`. Declared nil outside the block, so PTY mode passes a nil func (not a closure over a nil tracker). `stream_turn_busy.go` is untouched.

**Doc fixes**: `runActive` and `Evict` docstrings, `SessionConfig.IdleTimeout` ("no attached clients"), `Config.IdleTimeout` (the default is 0 = off; eviction is enabled only by the flag). The e2e comment in `TestE2E_PerConversation_IdleEvictsAndReactivates` that says the timer "resets only while attached > 0" is corrected too.

## Concurrency model

No new goroutines. `turnBusy` is called on the session's lifecycle goroutine with no session lock held. The production closure takes the conversations registry lock (inside `List`) and then the tracker's leaf mutex, neither of which is ever held while waiting on a session lifecycle goroutine, so no lock-order cycle.

## Error handling

The signal cannot fail: an unresolvable session reports not-busy and the session evicts as before. A permanently stuck busy mark would keep a session alive indefinitely; the tracker's exit-lane clear (`clearForExit`) and turn-end clear bound that, and the cap policy still force-evicts.

## Testing strategy

- **Unit (`internal/sessions`)** `TestSession_IdleEviction_DefersWhileTurnBusy`: pool built like `helperPoolIdle` with idle 100ms and a stub `TurnBusy` backed by an atomic bool (starts true) that counts calls and records the id. Run the session; after several windows it is still `stateActive` and the stub was called ≥2 times with the session's id (vacuity guard: the timer fired and deferred). Flip to false; the session reaches `stateEvicted` and a transition observer saw exactly one `ReasonEviction`. Nil-signal behaviour is covered by the existing `TestSession_IdleEvictionFires` and friends.
- **E2E (`internal/e2e`)** `TestE2E_IdleEviction_DefersWhileStreamTurnOpen`: stream-json daemon with `-pyry-idle-timeout=4s`, `PYRY_FAKE_CLAUDE_STREAM_HOLD` set, knownConv bound to the bootstrap. Send msg1 and msg2; wait for a `queue_state` holding msg2 (proves msg1's turn is open — the vacuity guard). Snapshot the count of `session.idle_eviction` records in stderr, then hold for 2.5 windows: the count must not grow and the bootstrap must still be `active`. Drop the trigger; wait for the msg1 and msg2 deltas; then the bootstrap must reach `evicted` within 3 windows with the eviction record count grown. Needs `startPerConvHarness` to accept extra child env — refactored into a variant that takes env, the old name delegating.

## Open questions

- None blocking. Whether e2e timing at 4s is robust against the respawn retry chain is checked during implementation; the window may be widened.

## Documentation handoff

The ticket names no documentation criteria. Pending for the documentation stage: `docs/knowledge/features/sessions-package-key-types-session.md` (the `Attach` bullet still describes bumping `attached`) and `docs/knowledge/features/sessions-package-key-types-transition-observer.md` (the idle path described as `attached==0`) should say idle eviction now defers on `Config.TurnBusy`.

## Revisions

### 2026-09-23 — e2e fixture: interrupt rider, not the startup hold

The plan's e2e held the turn open with `PYRY_FAKE_CLAUDE_STREAM_HOLD` and used "msg2 still queued" as the vacuity guard. In practice the held child never answers the permission-posture control request, so `streamsup`'s posture gate (`Runner.WriteUserTurn`) refuses msg1 with the retryable `ErrNoLiveChild` and no turn ever opens; the guard never fired. The test now uses `PYRY_FAKE_CLAUDE_STREAM_INTERRUPT` (the interrupt spec's fixture): fakeclaude echoes the prompt and withholds the result, so the turn stays open until a `TypeInterrupt` closes it. The vacuity guard is the echoed `assistant_delta`; the turn end is the following `turn_state{idle}`. Window 5s, hold 2 windows, eviction expected within 3 windows after the turn closes. Verified red with `TurnBusy` unwired in `main.go` (one `session.idle_eviction` during the hold) and green with it wired. No production-design change.
