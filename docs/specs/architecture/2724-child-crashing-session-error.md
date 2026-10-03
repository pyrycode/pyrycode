# #2724 — tell clients within seconds when a conversation's claude keeps crashing at startup

## Files read

- `internal/streamsup/runner.go` → `Run`: the backoff branch (`drainRestart` → `bo.next(uptime)` → `updateState(PhaseBackoff)`) is the one place a crash, and only a crash, passes. `Config.OnChildExit`: fires above the shutdown return and on restarts, so it cannot carry this signal.
- `internal/streamsup/backoff.go` → `backoffTimer`: the default ladder 500ms → 1s → 2s → 4s … capped at 30s, reset after 60s uptime.
- `internal/streamsup/runner_test.go` → `helperRunCfg`, `runInBackground`, `TestRunner_RestartsOnCrash`: the fake-child harness the integration tests reuse (`crash` exits 1 after 20ms; `echo_lines` stays up).
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory`: where `tag`, `sink.exitForTag(tag.ID)` and `OnChildExit` are bound per runner.
- `cmd/pyry/stream_turn_drain.go` → `streamTurnSink`, `exitForTag`, `streamSessionTag`: the per-daemon object every runner's lanes bind to, keyed by the live tag.
- `cmd/pyry/session_error_v2.go` → `giveUpNotice`, `sessionErrorNotify`, `sessionErrorEmitterV2.broadcast`: the existing session_error producer; broadcast stamps a fixed `CodeSessionBlocked`.
- `cmd/pyry/main.go` → `selectInteractiveRunner` call, `sessions.New`, the `giveUps` block, `pool.Run`: composition order. The factory and the pool are built before `giveUps` exists.
- `cmd/pyry/relay.go` → `conversationForSession`: session id → conversation id over `CurrentSessionID` and `SessionHistory`.
- `internal/protocol/codes.go` (code block, `TypeSessionError` block), `internal/protocol/messaging.go` → `SessionErrorPayload`, `internal/protocol/compat_test.go` (pinned code table).
- `docs/knowledge/features/streamsup-package-supervise-loop-run.md` § `claude exited` record (#2723): the deliberate-kill vs crash split is decided before `drainRestart`; this ticket hangs off the same split.

Overlap: no other `feature/*` branch touches these files.

## Context

During the 2026-10-03 crash loop the supervisor knew within a second that claude died on every spawn; clients learned nothing until msgqueue's two-minute give-up dropped the head. This adds an early, non-terminal `session_error` with a new code, sent once per crash episode, so a client can say "claude is failing to start" while the queued message is still kept.

## Design

### streamsup: a fast-exit episode detector

New file `internal/streamsup/crash_loop.go`:

- `const crashLoopFastExits = 4` and `const crashLoopFastUptime = 10 * time.Second`. On the default ladder the 4th fast exit enters backoff after 0.5 + 1 + 2 = 3.5s of total backoff wait (N = 5 would be 7.5s, N = 6 15.5s), inside the 10s bound.
- `type crashEpisode struct{ fast int }` with `observe(uptime time.Duration) (fire bool)`: uptime `< crashLoopFastUptime` increments `fast` and returns true exactly when `fast == crashLoopFastExits`; any other uptime zeroes `fast`. Counting past N without a second flag is what makes it once per episode.

`Config.OnCrashLoop func()`: optional, nil-checked. Fired from `Run`'s backoff branch only, after `bo.next(uptime)` and the `PhaseBackoff` state update, when `episode.observe(uptime)` returns true. Same contract as `OnChildExit`: synchronous on the Run goroutine, no Runner lock held, must not block, must not panic.

Placement consequences, which are the AC:
- Shutdown returns above the branch → never observed.
- `Restart`/`RestartFresh` hit `drainRestart() → continue` above the branch → neither counts nor ends the episode.
- A spawn-setup failure (`started == false`) falls into the branch with ~0 uptime → counts as fast.
- A child up past `BackoffReset` (60s) is also past `crashLoopFastUptime` → ends the episode.
- A restart arriving during the backoff wait cuts the wait short; the iteration was already observed, which is right — it was a crash.

### cmd/pyry: from the runner to the wire

- `streamTurnSink` gains `crashLoop atomic.Pointer[func(sessionID string)]`, `setCrashLoopNotify(fn)` and `crashLoopForTag(tag func() string) func()`. The returned closure loads the pointer at fire time and calls it with `tag()` (the live id, since `RestartFresh` rotates it); unset is a no-op. Hung on the sink because it is the per-daemon object the factory already captures and binds per-runner lanes from by tag, so `newStreamRunnerFactory`'s ten test call sites and `selectInteractiveRunner`'s three keep their signatures. Late-bound and loaded at fire time because the pool (and the bootstrap runner) is built before `giveUps` exists.
- `newStreamRunnerFactory` sets `scfg.OnCrashLoop = sink.crashLoopForTag(tag.ID)`.
- `giveUpNotice` gains `code string`. `sessionErrorNotify` sets `CodeSessionBlocked` explicitly; `broadcast` stamps `n.code`.
- New `childCrashingNotify(ch chan<- giveUpNotice, resolve func(sid string) (string, bool), logger) func(sessionID string)`: resolves the conversation, then a non-blocking send of `{convID, code: CodeSessionChildCrashing, reason: childCrashingMessage}`. Unresolved session or full channel → content-free Warn, drop. `childCrashingMessage` is a fixed constant.
- `main.go`: right after `blocked := sessionErrorNotify(giveUps, logger)`, `streamSink.setCrashLoopNotify(childCrashingNotify(giveUps, conversationForSession(convReg, …), logger))`. Before `pool.Run`, so no runner goroutine fires before it is set; the atomic makes that ordering a non-requirement anyway.

### protocol

- `CodeSessionChildCrashing = "session.child_crashing"` in the Session errors group, with its non-terminal meaning in the line comment.
- `TypeSessionError`'s block and line comment, and `SessionErrorPayload`'s doc and `Code` field comment: the frame reports a conversation-scoped session problem; `code` says whether it is terminal (`session.blocked`) or not (`session.child_crashing`).
- `compat_test.go` pinned code table gains the constant in both maps.

## Concurrency model

No new goroutine. The hook runs on the runner's Run goroutine: one atomic load, one `convReg.List()` (registry mutex held only for the slice copy), one non-blocking channel send. The emitter's existing Run goroutine does the fan-out, and both producers share its channel and its ID counter, so ordering between the two codes for a conversation is send order. The episode counter is Run-goroutine-private.

## Error handling

- Full channel: drop with a content-free Warn (`event`, `conversation_id`), matching `sessionErrorNotify`. One-shot edge, not recovered; the two-minute give-up still follows if delivery never recovers.
- Session not bound to a conversation (e.g. an unbound bootstrap session): Warn with `session_id`, no frame — there is no conversation to attach it to.
- msgqueue is untouched, so the queued head is neither dropped nor reordered and the give-up keeps its timing and code.

## Testing strategy

streamsup:
- `crashEpisode.observe` table: N−1 fast → no fire; Nth → fire; N+1…2N → no fire; a slow exit then N fast → fires again; uptime exactly `crashLoopFastUptime` is not fast; slow exit in the middle restarts the count.
- Constant check: the sum of the first N−1 delays of a default `newBackoffTimer` is ≤ 10s.
- `Run` with `crash` (1ms ladder): hook fires once by the Nth spawn's exit and stays at one across ≥ 2N spawns.
- `Run` with `echo_lines`, `Restart` called N+1 times, each waiting for the respawn: hook never fires.

cmd/pyry:
- `broadcast` stamps the notice's code (existing tests' literals set `CodeSessionBlocked`; one new case for the new code).
- `childCrashingNotify`: resolved → one notice with the new code and exactly `childCrashingMessage`; unresolved → nothing sent; full channel → does not block.
- `crashLoopForTag`: unset → no-op; set → called with the tag's live id, including after `Rotate`.
- `newStreamRunnerFactory` end to end against a crashing fake claude is not added: the factory line is one assignment and the two halves are each covered.

## Open questions

- Should an unresolved session id be Warn or Debug? Warn: it means a crash loop that no client can hear about, which an operator should see at the default level.

## Documentation handoff

Pending for the documentation stage, `docs/protocol-mobile.md`:
- v2 frame table row for `session_error`: replace "terminal session-error frame" with wording that covers both codes — the frame reports a conversation-scoped session problem, and its `code` says whether it is terminal.
- **Error codes**: add a row for `session.child_crashing` — not retryable by the client, not terminal; the daemon's claude child for this conversation keeps exiting at startup and the daemon is still restarting it; queued messages are kept; a later `session.blocked` may follow if delivery never recovers.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The frame's inputs are the daemon-resolved conversation id (from `conversationForSession` over the registry) and the constant `childCrashingMessage`. The child's exit status, stderr and argv never reach the hook: `OnCrashLoop` takes no arguments and `crashEpisode.observe` reads only the uptime. `childCrashingNotify` holds no queue handle, so queued text cannot enter the payload (the #1008 structural guarantee carries over).
- [Tokens] No findings. No secret is created, stored or carried; session and conversation ids are non-secret routing ids already logged elsewhere.
- [File operations] No findings. No file I/O is added.
- [Subprocesses] No findings. The child's spawn, environment and teardown are unchanged; the detector only observes the existing backoff branch.
- [Cryptography] No findings. None used.
- [Network and I/O] No findings. Fan-out reuses `sessionErrorEmitterV2.broadcast` and its interactive-only capability gate; the shared channel stays bounded at `sessionErrorQueueSize` with drop-on-full, so a crash-looping conversation can produce at most one frame per episode, and an episode needs N exits spaced by the backoff ladder — no amplification.
- [Errors, logs] SHOULD FIX: the new drop logs must carry only `event`, `conversation_id` / `session_id`, never the message or anything from the child. Checked while building.
- [Concurrency] No findings. The hook runs on the Run goroutine under no Runner lock; it takes the registry mutex only inside `List()` and never with another lock held. The late-bound notify is an `atomic.Pointer`, so a set racing a fire is well-defined. No goroutine is added.
- [Threat model] OUT OF SCOPE: client handling of the new code is the separate pyrycode-desktop and pyrycode-mobile tickets named in the issue; until they land, clients drop the frame, which is today's behaviour.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-03
