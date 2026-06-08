# Spec #633 — Event-stream bridge: live producer wiring in `startRelayV2`

**Part of EPIC #596** (Phase 2 structured streaming). ADR 025 (`docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md`) § Phase 2.

Wire the #615 **producer** (`internal/turnbridge`) to the #632 **consumer** (`interactiveTurnEmitterV2`) inside `startRelayV2`, so the supervised session's structured events actually flow to the capability-gated phone fan-out, and keep flowing across a `/clear`-rotated JSONL on the next (re)subscription. This is the wiring slice both #615 and #632 deferred. **Not `security-sensitive`** (behaviour-preserving plumbing over the local transcript; the capability gate lives in the already-labelled #632 emitter).

---

## Files to read first

Read these before writing code; line ranges are the load-bearing parts.

- `cmd/pyry/relay.go:260-331` — `startRelayV2`: the seam. Note the existing foreground-gated `startAssistantTurnBridgeV2` (313-319) and the drain-ordering contract (321-330: observer-cleanup **before** `<-mgrDone`). Your wiring mirrors this exactly.
- `cmd/pyry/relay.go:88-237` — `startRelay`: the only caller of `startRelayV2` (line 141). You add one param here and forward it; the `<-waitDone` classifier (210-235) is untouched.
- `cmd/pyry/main.go:481-493` — the only caller of `startRelay`; `claudeSessionsDir` is already in scope (computed at `main.go:417`). You thread it into the `startRelay` call.
- `cmd/pyry/main.go:114-131` — `resolveClaudeSessionsDir` → `sessions.DefaultClaudeSessionsDir`: how `claudeSessionsDir` is derived (the dir the resolver scans). May be `""` (gate on it).
- `internal/turnbridge/producer.go:29-73` — `Subscriber`, `SessionHost`, `Config`, `New`. `New` errors only when `Subscribe == nil`.
- `internal/turnbridge/producer.go:81-118` — `Run`'s outer re-subscribe loop + `drain`: `OnEvent` runs on **this single goroutine, serially** (the #632 named assumption you must honour).
- `internal/turnbridge/producer.go:120-194` — `NewSessionSubscriber`: calls `resolve` **fresh on every (re)subscription**; spawns the `go func(){ sess.Wait(); cancel() }()` watcher only on the success path (the "no leaked goroutine" linchpin — verified, not re-implemented).
- `cmd/pyry/interactive_turn_v2.go:21-78` — `interactiveBroadcaster`, `interactiveTurnEmitterV2`, `newInteractiveTurnEmitterV2(sup cursorReader, bcast interactiveBroadcaster, logger)`. `*relay.V2SessionManager` satisfies `interactiveBroadcaster`; `*supervisor.Supervisor` satisfies `cursorReader`.
- `cmd/pyry/interactive_turn_v2.go:80-130` — `Handle(ctx, ev)`: the `OnEvent` target. The ctx-less seam is bridged by a closure capturing the relay ctx.
- `internal/supervisor/supervisor.go:301-356` — `Session()` and `WaitForPTY()`: `*supervisor.Supervisor` satisfies `turnbridge.SessionHost`. Note `Session()` is nil-safe; `WaitForPTY` blocks until a Session is live (re-keyed per `runOnce`).
- `internal/supervisor/supervisor.go:461-467, 486-620` — `buildClaudeArgs` (`--continue` on every restart) + `runOnce`: the supervisor restarts claude **only on process exit** (`sess.Wait()`); `/clear` does **not** restart it. This is why re-subscription is restart-driven — see § "The `/clear` mechanism".
- `internal/sessions/reconcile.go:50-89` — `mostRecentJSONL`: the rotation-discipline reference (UUID-stem filter, mtime pick, lexicographic tie-break). Your resolver is the same scan but returns `(path, size)` instead of a `SessionID`, and lives in `package main` (do **not** export/reuse this private helper).
- `cmd/pyry/assistant_turn_v2.go` / `cmd/pyry/interactive_turn_v2_test.go` — reuse the `package main` test doubles: `stubCursor`, `fakeInteractiveBcast`, `discardLogger`, `testConvID` (from `assistant_turn_test.go` / `interactive_turn_v2_test.go`, same package).
- tui-driver v1.3.0 `pkg/tuidriver/events.go` (`Session.Events` tails a **fixed** path from a fixed offset; channel closes on ctx-cancel / tail-close / session-end) and `pkg/tuidriver/jsonl.go` (`WaitForSessionJSONL`, `TailJSONL`, `SessionJSONLPath`). Read to confirm: the offset = `startOffset` means "start here"; passing current size = "tail only new lines".
- `docs/knowledge/codebase/615.md`, `632.md`, `589.md` — producer lifecycle, emitter contract + named single-`Run`-goroutine assumption, and the `startRelayV2` foreground-gate + teardown-ordering template, respectively.

---

## Context

**What.** #615 shipped the producer — `New`, `Run`'s re-subscribe loop, `NewSessionSubscriber` over `Session.Events`, and an injected JSONL `resolve` closure — deliberately unwired. #632 shipped the consumer — `newInteractiveTurnEmitterV2` + `Handle`, the capability-gated structured emitter — also unwired (tested against an injected event source). Both deferred the production wiring to this slice.

**Why now.** With both halves merged into the tree, the structured interactive stream is one wiring step from going live: construct the emitter over the v2 manager, build the producer with `NewSessionSubscriber` + a JSONL resolver + an `OnEvent` callback bridging to `emitter.Handle`, start the producer goroutine, and order teardown against the existing v2-manager / coarse-bridge cleanup.

**The one non-trivial sub-part: the JSONL resolver.** The supervised relay (bootstrap) session spawns with `--continue`, **not** `--session-id` (`supervisor.go:235` + `buildClaudeArgs`), so pyry holds no stable claude session UUID and `*tuidriver.Session` exposes no path accessor. `tuidriver.SessionJSONLPath(home, cwd, sessionID)` therefore **cannot** be used — there is no `sessionID`. The resolver instead returns the **most-recently-modified `*.jsonl`** in the session's projects dir plus its current size as `startOffset`. Because `NewSessionSubscriber` calls `resolve` fresh on every (re)subscription, returning the newest file each time is what makes a re-subscription pick up a `/clear`-rotated JSONL.

---

## Design

### Package structure

All new code is `package main` in `cmd/pyry` (mirrors #589/#632 — no new exported types, no new package). Two files change, one is new:

| File | Change |
|---|---|
| `cmd/pyry/interactive_turn_stream_v2.go` | **new** — `startInteractiveTurnStreamV2(...)` (the wiring helper) + `resolveLatestSessionJSONL(dir)` (the resolver). |
| `cmd/pyry/relay.go` | `startRelay` + `startRelayV2` each gain a `claudeSessionsDir string` param; `startRelayV2` calls the helper under the foreground+dir gate and orders its cleanup into the drain. |
| `cmd/pyry/main.go` | the `startRelay` call (line 489) forwards `claudeSessionsDir`. |

Edit fan-out is exactly two signatures + two call sites (`startRelay`←`main.go:489`, `startRelayV2`←`relay.go:141`) — well under the 10-call-site line.

### The wiring helper

```
func startInteractiveTurnStreamV2(
    ctx context.Context,
    sup *supervisor.Supervisor,
    mgr *relay.V2SessionManager,
    claudeSessionsDir string,
    logger *slog.Logger,
) (cleanup func())
```

Behaviour (contract, not body — keep the implementation under ~30 LOC):

1. `emitter := newInteractiveTurnEmitterV2(sup, mgr, logger)` — `sup` is the `cursorReader`, `mgr` the `interactiveBroadcaster`.
2. `tr := tuidriver.NewTracker(tuidriver.TrackerOpts{})` — required by `Session.Events`; zero opts → package defaults (drives only the dropped stall arm).
3. `resolve := resolveLatestSessionJSONL(claudeSessionsDir)`.
4. `sub := turnbridge.NewSessionSubscriber(sup, resolve, tr, logger)`.
5. `prod, err := turnbridge.New(turnbridge.Config{Subscribe: sub, OnEvent: func(ev turnevent.Event){ emitter.Handle(ctx, ev) }, Logger: logger})`. `New` can only fail on a nil `Subscribe` (unreachable here) — on error, log at Warn and return a no-op cleanup (defensive, mirrors the daemon's fail-soft posture for an optional surface).
6. Spawn one goroutine: `done := make(chan struct{}); go func(){ defer close(done); if err := prod.Run(ctx); err != nil { logger.Debug("relay: interactive turn stream run returned", "err", err) } }()`.
7. Return `func(){ <-done }`.

The `OnEvent` closure captures `ctx` (the relay lifecycle ctx) — this is the ctx-less-`OnEvent` → ctx-ful-`Handle` seam #632 named. It runs **only** on the producer's single `Run` goroutine (the drain invokes `OnEvent` serially), so the emitter's unguarded counters never race — the #632 named assumption holds by construction.

### The JSONL resolver

```
func resolveLatestSessionJSONL(dir string) func(ctx context.Context) (path string, startOffset int64, err error)
```

Returns a closure capturing `dir`. Each call (contract — keep under ~30 LOC):

- `os.ReadDir(dir)` → on error, return `("", 0, fmt.Errorf("read claude sessions dir %s: %w", dir, err))`.
- Iterate entries; skip directories and names not ending in `.jsonl`. (Optional hardening: also require a canonical UUIDv4 stem, mirroring `reconcile.go`'s `uuidStemPattern` — claude writes only `<uuid>.jsonl` here, so a bare `.jsonl` suffix is sufficient; the UUID filter just rejects stray editor temp files. Developer's call; note whichever you pick in the doc.)
- `os.Stat` each candidate; track the one with the latest `ModTime()`. Tie-break on the lexicographically-larger filename (deterministic for tests — matches `mostRecentJSONL`). Stat failures on individual entries are skipped, not fatal.
- If no candidate found, return `("", 0, errors.New("no session jsonl found in <dir>"))`.
- Otherwise return `(filepath.Join(dir, name), info.Size(), nil)` for the winner.

`startOffset = info.Size()` is load-bearing: the tail starts at EOF so a (re)subscription streams **only new** events — it never re-emits the historical transcript (which would replay old turns to the phone). This is exactly AC#1's "plus its current size as `startOffset`".

**Directory source — design decision (reuse `claudeSessionsDir`, do not recompute via `tuidriver.EncodeCwd`).** The ticket's prose suggests computing `~/.claude/projects/<tuidriver.EncodeCwd(cwd)>/` in the resolver. We instead **reuse the daemon's already-computed `claudeSessionsDir`** (`sessions.DefaultClaudeSessionsDir(abs(workdir))`, `main.go:417`) and thread it down. Rationale:

- **Single source of truth.** `claudeSessionsDir` is the exact dir the live `/clear` rotation watcher (Phase 1.2b-B) watches and startup reconciliation (Phase 1.2b-A) scans. The resolver scanning the *same string* is coherent-by-construction with the rest of the daemon. Recomputing via a *different* encoder (`EncodeCwd` maps every non-alphanumeric byte to `-` and canonicalises symlinks; `sessions.encodeWorkdir` maps only `/` and `.`) would make the resolver point at a different dir than the rotation watcher in edge-case cwds (`_`, spaces, symlinked paths) — an incoherence worse than uniform behaviour.
- **No second cwd-encoder in `cmd/pyry`** → nothing to drift.
- **The resolver becomes pure** — `(dir) → (newest path, size)` — trivially unit-testable against a `t.TempDir()`, with no `$HOME` / cwd / symlink dependency.

The latent `encodeWorkdir`-vs-`EncodeCwd` divergence is a pre-existing property of `sessions.DefaultClaudeSessionsDir` that already governs reconcile + rotation; unifying the two encoders is out of scope here (see Open Questions).

### Wiring + gate in `startRelayV2`

After the existing coarse-bridge block (`relay.go:316-319`), add the structured-stream start under a **two-part gate**:

```
var streamCleanup func()
if bridge != nil && claudeSessionsDir != "" {
    streamCleanup = startInteractiveTurnStreamV2(ctx, sup, mgr, claudeSessionsDir, logger)
}
```

- `bridge != nil` — the foreground gate, identical to the coarse v2 bridge (AC#4). In foreground mode there is no phone-mirroring surface, so the structured stream is off, exactly like #589/#632's coarse path. Inbound paths (`send_message`, etc.) are unaffected.
- `claudeSessionsDir != ""` — a deterministic guard against the resolver perpetually erroring (and Warn-spamming every `subscribeRetryDelay`) when the dir is unresolvable. `""` already disables reconcile + the rotation watcher (per `DefaultClaudeSessionsDir`'s contract), so disabling the producer too is consistent. Log one Info line when skipping for this reason.

`startRelay` threads `claudeSessionsDir` straight through to `startRelayV2`; its own `<-waitDone` 4409/ctx classifier is untouched.

### Data flow

```
supervisor runOnce → tuidriver.Session  (one process; restarts on exit only)
        │ Session.Events(sessCtx, path, off, tr)   [path,off from resolve]
        ▼
turnbridge.Producer.Run  ── drain ──► OnEvent(ev)  [single goroutine]
        │                                  │  closure: emitter.Handle(relayCtx, ev)
        ▼                                  ▼
  re-subscribe loop                interactiveTurnEmitterV2.Handle
  (resolve re-evaluated)                   │  turn_state machine + MapEvent
                                           ▼
                                 mgr.ActiveConns / mgr.Push  (interactive-gated fan-out → phones)
```

---

## Concurrency model

- **One new goroutine:** the producer's `Run` (spawned by the helper). The emitter is a passive state machine (#632) — zero goroutines. The watcher goroutine inside `NewSessionSubscriber` is #615's, not this slice's.
- **Single-writer for emitter state.** `Run` → `drain` → `OnEvent` → `emitter.Handle` all execute on the one `Run` goroutine, serially. The emitter's `inTurn`/`turnID`/`seq`/`currentState`/`nextID` are read/written only there — no atomics, no mutex needed (the #632 named assumption: "the wiring closure must invoke `Handle` from that one goroutine only"). **Honour it: do not call `Handle` from anywhere else.**
- **`ActiveConns`/`Push` funnel through the manager's single Run goroutine**, each with a `ctx.Done` escape arm. A `Handle` in flight during teardown passes a cancelled ctx → `ActiveConns` returns `nil`, `Push` returns `ctx.Err()` → `emit` returns early. The producer never blocks on a winding-down manager.

### Shutdown sequence (the teardown ordering — AC#3)

Extend `startRelayV2`'s returned drain. New order (stop both producers before waiting on the manager):

```
return func() {
    if bridgeCleanup != nil { bridgeCleanup() }   // coarse observer off
    if streamCleanup != nil { streamCleanup() }   // <-producerDone
    <-mgrDone                                       // manager Run exits on closed Frames
}, nil
```

- By the time drain runs, `ctx` is already cancelled (`main.go` defers `relayCleanup` *after* `pool.Run(ctx)` returns). `prod.Run` returns promptly on cancel regardless of where it is blocked: in `drain`'s `select` (`<-ctx.Done()`), in `host.WaitForPTY(ctx)`, in `WaitForSessionJSONL`/`sleepCtx`, or in `sess.Events`. So `streamCleanup()`'s `<-done` unblocks.
- **No watcher leak.** `pool.Run` has already returned by drain time → the supervisor tore down → `sess.Close()` ran at every `runOnce` exit → the current subscription's `sess.Wait()` returned → its watcher's `cancel()` fired and the watcher exited. At most one watcher is ever live (each ends with its session before `Run` re-subscribes). This is #615's guaranteed property — verified here, not re-implemented.
- Stopping the producer **before** `<-mgrDone` means no `emitter.Push` races a dead manager (and even if one did, it returns a sentinel the emitter swallows).

---

## Error handling

| Failure mode | Handling |
|---|---|
| Resolver: dir unreadable / no `*.jsonl` yet | Return an error. `NewSessionSubscriber` logs Warn + retries after `subscribeRetryDelay` (500 ms). For a `--continue` resumed session the JSONL exists immediately, so this window is normally nil; a cold start with no prior transcript Warn-spams until first input (minor; see Open Questions). |
| `claudeSessionsDir == ""` | Producer not started (gate); one Info line. Coherent with reconcile/rotation being disabled in that case. |
| `turnbridge.New` returns error | Unreachable (only nil `Subscribe` triggers it). Defensively: Warn + no-op cleanup. |
| `prod.Run` returns non-ctx error | `Run` only returns `ctx.Err()` per its contract; log at Debug and let the goroutine exit. |
| Per-conn `Push` failure mid-fan-out | Owned by the #632 emitter (DEBUG log + continue; never aborts the turn). Not re-handled here. |
| Foreground mode (`bridge == nil`) | Producer skipped; inbound paths unaffected (AC#4). |

**AC#5 — no application output ever logged.** The wiring and resolver must log **only** lifecycle markers, lengths, ids, and (resolver-side) file paths — never JSONL contents. Concretely: the producer's drain already logs only `ev.Kind` on a drop (#615); the emitter already enforces the no-content contract (#632); this slice's own logs are limited to the helper's start/stop Debug lines and the resolver's Warn (which carries `dir` and a wrapped `os.` error — a path/errno, never file bytes). Do not add any log that interpolates an event payload or a JSONL line. A test asserts this (below).

### The `/clear` mechanism (AC#2) — read carefully

`Session.Events` tails a **fixed** `jsonlPath` from a **fixed** `startOffset`; its merge channel closes only on ctx-cancel, internal tail-close, or session end (`sess.Wait()`). A live `/clear` does **not** exit claude (the same process rotates its on-disk UUID — which is precisely why the fsnotify rotation watcher exists). So **re-subscription is restart-driven**:

1. Subscription N tails JSONL `A` (newest at the time `resolve` ran).
2. The supervised process exits (crash → backoff respawn, or idle-evict → reactivate). `sess.Wait()` returns → the watcher cancels `sessCtx` → the Events channel closes → `drain` returns → `Run` loops.
3. Subscription N+1 calls `resolve` **fresh** → it returns the current newest JSONL `B` (which, if a `/clear` happened, is the rotated file) at offset `size(B)` → the producer now tails `B`. No watcher leak (A's watcher exited in step 2).

So AC#2 is satisfied by **#615's restart-driven re-subscribe loop + the resolver re-evaluating to the newest file** — verified, not re-implemented. The resolver returning the most-recently-modified file *on each call* is the load-bearing half: a captured/static path would keep tailing the stale `A` after a rotation. **Note the boundary:** a `/clear` *not* followed by a restart leaves the producer tailing the pre-`/clear` file until the next restart (an inherited #615 property — the producer has no live rotation signal). Flagged in Open Questions; out of scope here (AC explicitly: loop + watcher "not re-implemented").

---

## Testing strategy

Unit tests are the primary behaviour oracle (matching #615/#632, whose live glue is verified downstream — i.e. here). All `package main`, stdlib `testing`, `-race`, done-channel synchronisation (no sleeps).

**`cmd/pyry/interactive_turn_stream_v2_test.go` (new).**

*Resolver (`resolveLatestSessionJSONL`) — table-driven against `t.TempDir()`:*
- Newest wins: three `<uuid>.jsonl` with staggered mtimes (use `os.Chtimes`) → returns the newest path and its `Size()` as offset.
- Offset is current size: write N bytes, assert returned offset == N.
- Re-evaluation: call the closure, add a newer file, call again → second call returns the new file (proves per-call freshness — the AC#2 mechanism in miniature).
- Empty / no-match dir → non-nil error, empty path.
- Unreadable dir (nonexistent path) → wrapped error.
- Non-`.jsonl` entries and subdirectories ignored.

*Wiring (producer ⇄ emitter) — drive the producer with a FAKE `Subscriber`* (the live `NewSessionSubscriber` needs a real `*tuidriver.Session`, verified by e2e below):
- **Events reach `Handle`:** build a `turnbridge.Producer` via `turnbridge.New` with `Subscribe` = a fake yielding a scripted `chan tuidriver.Event`, and `OnEvent` = the real `emitter.Handle(ctx, …)` closure over a `fakeInteractiveBcast` + `stubCursor`. Feed a `[Thought, Text, Tool, TurnEnd]` JSONL-sourced sequence; assert the broadcaster received the expected `turn_state` / `assistant_delta` / `tool_use` / `turn_end` envelopes in order (re-uses #632's assertions; this test proves the *bridge*, not the mapper).
- **Rotation re-subscribes + re-evaluates:** a fake `Subscriber` that returns channel-1 on call 1 and channel-2 on call 2, backed by a resolve-spy counter. Close channel-1 (simulated session end) → assert the subscriber was called a second time (re-subscription), events fed on channel-2 reach the broadcaster, and the spy shows `resolve` re-evaluated. Assert no goroutine leak across the transition (the call-2 path is the only live one).
- **Teardown is clean (AC#3):** cancel `ctx` → assert `prod.Run` returns and the helper's cleanup `<-done` unblocks within a deadline; no goroutine outlives cleanup (gate on a done channel; `-race` clean).
- **No app-output log leak (AC#5):** feed events carrying distinctive thought/assistant/tool strings through the wiring with a capturing `slog` handler; assert none of those substrings appear in any log line emitted by the helper/resolver/producer path.

**Optional but recommended — e2e (`internal/e2e/relay_v2_daemon_test.go`, build tag `e2e`).** Mirror #589's `TestRelayV2_AssistantTurn_BroadcastsMessageEnvelope` harness (`pair` → seed `conversations.json` → `StartRotationWithRelay(..., "/v2/server", "PYRY_MOBILE_V2=1", assistant trigger)` → handshake → sealed frames) but assert the phone receives structured `turn_state` / `assistant_delta` / `turn_end` envelopes from the live `NewSessionSubscriber` + real resolver over a real claude session. This is the only place the *live* subscriber + resolver are exercised end-to-end. **Environmental caveat (inherited #589/#615):** the sandbox cannot fetch the private `tui-driver` module, so `make e2e-realclaude` / the `e2e` tag are verified manually / in CI, not in the dev sandbox — the unit tests above remain the behaviour oracle.

`make check` (vet + `-race` + staticcheck + substrate-guard) must be green. The substrate-guard scans tests too — fixtures must avoid banned claude-screen literals (use synthetic event values, as #615's fixtures do).

---

## Open questions

1. **Live `/clear` without a restart.** The producer re-subscribes only on a supervisor restart; a `/clear` followed by continued chatting (no restart) leaves it tailing the pre-`/clear` JSONL until the next restart. This is an inherited #615 property (the producer has no live rotation signal — the fsnotify watcher feeds `Pool.RotateID`, not the producer). If product wants gap-free structured streaming across in-process `/clear`, a follow-up could bridge the rotation watcher's signal into a `sessCtx` cancel. **Out of scope here** (AC: loop + watcher not re-implemented). Recommend tracking as a Phase 2 follow-up.
2. **Encoder unification.** `sessions.encodeWorkdir` (used by `DefaultClaudeSessionsDir`, hence `claudeSessionsDir`) maps only `/` and `.`; tui-driver's `EncodeCwd` (v1.3.0) maps every non-alphanumeric byte and canonicalises symlinks — the empirically-correct rule. They diverge for cwds with `_`/spaces/symlinks, which would mis-locate the dir for reconcile, the rotation watcher, **and** this resolver alike. Reusing `claudeSessionsDir` keeps the producer exactly as correct as the shipped machinery; unifying the two encoders (so all three use `EncodeCwd`) is a separate, broader fix. Recommend a follow-up issue.
3. **Cold-start Warn cadence.** On a fresh cwd with no prior transcript, `resolve` errors until claude writes its first JSONL, producing a Warn per `subscribeRetryDelay` (500 ms). Acceptable for now; if it proves noisy in practice, demote the resolver's not-found case to Debug or add a one-shot "waiting for first transcript" log. Not built (evidence-based: no observed failure yet).

---

## Acceptance criteria (restated for the developer)

- [ ] `startRelayV2` constructs the #615 producer with `NewSessionSubscriber` (over `Supervisor.Session()`), the rotation-following JSONL `resolve` closure (newest `*.jsonl` in `claudeSessionsDir` + its size as `startOffset`, re-evaluated per subscription), and an `OnEvent` closure delivering each event to `emitter.Handle(relayCtx, ev)`; `Run` started on a goroutine.
- [ ] On a restart-driven re-subscription after a `/clear`, the producer re-subscribes (via #615's `Run` loop + `sess.Wait()` watcher — verified) and `resolve` re-evaluates to the rotated JSONL; no leaked goroutine across the transition.
- [ ] Teardown ordered: coarse observer off → producer goroutine exits (`<-done`) → `<-mgrDone`; no goroutine leak.
- [ ] Foreground mode (`bridge == nil`) unaffected; gated identically to the coarse v2 bridge; inbound paths still work.
- [ ] No application output logged at any level by the wiring or resolver (only lifecycle markers, lengths, ids, and resolver-side file paths).
