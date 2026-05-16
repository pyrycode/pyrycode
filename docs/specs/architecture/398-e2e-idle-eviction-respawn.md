# Spec #398 — e2e test: `--pyry-idle-timeout` eviction-then-respawn contract

## Context

`pyry --help` documents `--pyry-idle-timeout` as "evict idle claudes after this duration (default 0=disabled; respawn latency 2-15s on next attach)." The respawn half of that contract was uncovered by automation before #396 landed — silent outage observed on pyrybox 2026-05-15 (7.5h between SIGKILL and manual restart). #396 (PR #400, merged 2026-05-15) reified `send_message` as an attach event by adding a `Session.Activate` call (now waiting for PTY readiness via the new `Supervisor.WaitForPTY`) ahead of `WriteUserTurn` in `handlers.SendMessage`. Handler-level coverage exists in `internal/relay/handlers/send_message_test.go`; this ticket fills the e2e gap.

The test drives the full pyry binary across one **eviction → inbound `send_message` → respawn** cycle, asserting every public observable the operator and the protocol promise: the structured eviction WARN log, the supervised PID disappearing, the `send_message.ack` round-trip, and a fresh supervised PID appearing within the documented 15s respawn-latency window.

Scope is one test file under `internal/e2e/`, no production change. The PO body floated a possible <30 LOC harness accessor — not needed. `pyry status` already surfaces `ChildPID` via `StatusPayload` (`internal/control/protocol.go:280`) and `cmd/pyry/main.go:574` prints it as `Child PID:     N`. Tests poll `h.Run(t, "status")` and parse that line.

## Files to read first

- `internal/e2e/idle_test.go:1-125` — sibling that already drives `-pyry-idle-timeout=1s` against `/bin/sleep`. Reuse `newRegistryHome`, `waitForBootstrapState`, `readRegistry`. Adopt the same 5s registry-poll deadline shape, but extend timeout knobs (eviction window is 2s here, respawn budget 15s).
- `internal/e2e/relay_roundtrip_test.go:31-220` — canonical pattern for the full phone→relay→binary→fakeclaude wire: `RunBareIn(... "pair" ...)` + `decodePairPayload`, seed `conversations.json`, `fakerelay.New`, `StartRotationWithRelay`, wait for `fr.LastBinaryHello(serverID)`, `fakephone.Dial`, send hello / send_message, drain envelopes. Lift the same steps; this ticket only needs hello + send_message (no list_conversations, no assistant-turn echo, no register_push_token).
- `internal/e2e/relay_send_message_test.go:32-166` — the minimal send_message→ack drive. Demonstrates conversations.json seeding (lines 50-56), `fakephone.Dial` ergonomics (lines 86-92), the ack-shape assertion (lines 135-148). This ticket's send_message slice is almost identical; the difference is the eviction interleave and the PID-changed assertion that follows the ack.
- `internal/e2e/harness.go:266-359` — `StartRotation` and `StartRotationWithRelay` (the wire-enabled variant). `extraEnv` is variadic and last-wins for flags. Adopting `StartRotationWithRelay` gives the test both a real fakeclaude PID **and** a working relay path.
- `internal/e2e/harness.go:563-601` — `Harness.Run`. `pyry status` exit-and-stdout shape; reuse the `Phase:         running` literal match.
- `internal/sessions/session.go:341-369` — the idle-eviction emit. Locks in the exact slog fields (`event=session.idle_eviction`, `session_id`, `idle_timeout`, `bootstrap`). `s.id` is the supervisor name; with the harness's `-pyry-name=test` that yields `session_id=test`.
- `internal/supervisor/supervisor.go:288-336` — `ChildPID` is set on spawn (line 304) and zeroed at backoff (line 325). The "Child PID:     N" status line disappears between eviction and respawn — both halves of the test rely on that.
- `cmd/pyry/main.go:572-583` — exact `pyry status` print format. Anchor the parse on `Child PID:     ` (4 spaces of alignment) — `Phase:         running` already proven stable by `idle_test.go:90`.
- `internal/control/protocol.go:274-286` — `StatusPayload` shape. Documents `child_pid,omitempty` (= 0 → omitted). Useful background; the e2e test parses CLI output, not the JSON.
- `docs/knowledge/codebase/396.md` — fix shape; clarifies that Activate runs **before** WriteUserTurn and now waits for `WaitForPTY`, so by the time the ack envelope reaches the phone the PTY is bound and `ChildPID` is set. The respawn "within 15s" budget collapses to "by the time we receive the ack" in practice; test still asserts the 15s upper bound for documentation parity.
- `docs/knowledge/decisions/023-activate-waits-pty-readiness.md` — strengthened Activate contract that this test pins end-to-end.

## Design

### File and build tag

- New file: `internal/e2e/respawn_after_eviction_test.go`.
- Header: `//go:build e2e` (same as `idle_test.go`, `relay_roundtrip_test.go`).
- Package: `e2e`. Reuses every helper in the package (`newRegistryHome` / `waitForBootstrapState` / `decodePairPayload` / `mustJSON` / `relayTestLogger` / `readPersistedServerID` / `shortHome`). No new helper export.

### One test

`TestE2E_IdleEviction_RespawnsOnSendMessage`. Single function. Five linear phases. No subtests — splitting into table form would obscure the temporal ordering that is the *point* of the test.

### Harness wiring

```
home  := shortHome(t)                                  // not newRegistryHome — pair writes under ~/.pyry/test/
RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")  // emits pair token to stdout

seed conversations.json under <home>/.pyry/test/      // ValidateConversation gates WriteUserTurn

fr := fakerelay.New(relayTestLogger())
t.Cleanup(fr.Close)

h := StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverTrigger, stdinLog,
        fr.URL()+"/v1/server", "-pyry-idle-timeout=2s")
//                                  ^^^^^^^^^^^^^^^^^^^^^^^^^^^
// extraEnv is variadic; last entry is treated as a flag override.
```

There is one wiring subtlety the test must thread: `StartRotationWithRelay`'s **variadic tail is `extraEnv ...string`**, not extra flags. The test needs to pass `-pyry-idle-timeout=2s` as a *flag*, not an env var. Two options — pick (B):

- (A) Add a new harness variant that takes both extraFlags and relay wiring. Out of scope per the PO body's "no new production code" framing; the harness already supports this combination via spawnOpts and we'd be inventing a third caller-facing variant for a one-off.
- (B) **Use `StartInWithEnv` directly**, passing the full env block and flag block. `StartInWithEnv` is the most general public Start variant (`internal/e2e/harness.go:227-250`). The test mirrors what `StartRotationWithRelay` does inline:
  - `fakeBin := ensureFakeClaudeBuilt(t)` — public-enough (lowercase, same package); the test already lives in package `e2e`.
  - `os.MkdirAll(sessionsDir, 0o700)`.
  - Pass `-pyry-claude=<fakeBin>`, `-pyry-workdir=<home>`, `-pyry-relay=<URL>`, `-pyry-idle-timeout=2s` (last-wins overrides the spawn default `=0`), plus the `--` claude-args.
  - extraEnv: `PYRY_ALLOW_INSECURE_RELAY=1`, the three `PYRY_FAKE_CLAUDE_*` vars, and `PYRY_FAKE_CLAUDE_STDIN_LOG=<path>`.

  This is the same wiring `StartRotationWithRelay` does, inlined. ~15 lines of harness plumbing — clear and local. The alternative — extending `StartRotationWithRelay` to take both extraEnv and extraFlags — is a harness-shape change for one caller and belongs in a separate ticket if it ever has a second caller.

  **Open question for the developer:** if the inline plumbing exceeds ~20 lines, fold it into a small *test-file-local* helper `startEvictionHarness(t, home, ...)` — keep it private to this file, no new exports from `harness.go`.

The supervised child is fakeclaude, not `/bin/sleep`, because we need it to (a) be a real process whose PID we can probe and (b) honour the `PYRY_FAKE_CLAUDE_*` env so the stdin-log assertion in #396's send_message test idiom is available if useful for debugging. The test does NOT assert on the stdin log — the ack envelope reaching the phone is the proof.

### Phases

1. **Capture initial PID.** Poll `pyry status` until `Phase:         running` AND a `Child PID:     N` line is present; parse N. 3s deadline. Define a small file-local helper:

   ```go
   func childPIDFromStatus(t *testing.T, h *Harness) (int, bool)
   ```

   Returns `(pid, true)` if the running-phase + child-PID lines are both present; `(0, false)` otherwise. Implemented as a regexp / bytes.Split walk over `r.Stdout`. Used in phases 1 and 5.

2. **Wait for eviction.** Two assertions, both must pass:
   - (a) `waitForBootstrapState(t, regPath, "evicted", 5*time.Second)` — registry-side proof. Reuses the existing helper after switching from `shortHome` to a `regPath` discoverable via `filepath.Join(home, ".pyry", "test", "sessions.json")`. (The pair-then-StartIn sequence creates the same registry path that `newRegistryHome` would compute; no helper change.)
   - (b) `h.Stderr.String()` contains all four substrings: `event=session.idle_eviction`, `session_id=test`, `idle_timeout=2s`, `bootstrap=true`. The slog text handler emits unquoted `key=value` for non-space values. **The four-substring check is order-independent and tolerates additional fields**; a future field added to the WARN doesn't break this test. Poll with the same 5s deadline as (a).
   - (c) `syscall.Kill(initialPID, 0)` returns `syscall.ESRCH` (errno: "no such process"). Poll up to 3s with 50ms gap to absorb the SIGKILL → kernel-reaped window.

3. **Open the phone.** Wait for `fr.LastBinaryHello(serverID)` (5s deadline), then `fakephone.Dial`. Send `protocol.TypeHello`, assert `TypeHelloAck`. Identical to the first 35 lines of `relay_send_message_test.go:94-118`.

4. **Send the inbound and capture the ack.** Record `sentAt := time.Now()` just before `phone.Send(req)`. Construct a `TypeSendMessage` envelope with the seeded `conversationID`, `MessageID: "m-1"`, `Text: knownText`. **Receive deadline = 15s** — the documented respawn-latency upper bound. The handler waits up to 30s on Activate internally, but the test budgets 15s because that's the contract `pyry --help` advertises. Assert:
   - `ack.Type == TypeAck`
   - `ack.InReplyTo != nil && *ack.InReplyTo == req.ID`
   - `time.Since(sentAt) <= 15 * time.Second` — turns the "respawn latency 2-15s" doc line into a runtime invariant. If the assertion ever fires the operator-facing doc has drifted from reality.

5. **Assert a fresh supervised PID.** Poll `childPIDFromStatus(t, h)` until `(pid, true)` and `pid != initialPID`, with a 3s deadline. (15s of the budget is already consumed by phase 4's ack-receive; the remaining wall-clock is bounded by AC's <30s total.) On Linux `os.FindProcess` always succeeds so `syscall.Kill(pid, 0)` is the canonical liveness probe; the status-reported PID is the supervisor's recorded `ChildPID`, which is set during `runOnce`'s `onSpawn` (`internal/supervisor/supervisor.go:300-307`). The test trusts the status line — chasing the kernel's view of the new PID adds nothing.

### Why these signals (and not the alternatives)

- **Stderr substring vs slog recording-handler.** `idle_test.go` reads the registry file; this test must additionally pin the structured log fields because that's the operator's primary signal post-#396. A slog test handler would require restructuring the harness to inject a logger. The stderr-substring check is the lowest-cost lock-in for the four required keys and survives slog format reshuffles within the text handler.
- **`pyry status` polling vs `signal: killed` exit-line counting.** The PO body listed "second `claude exited` for a different PID" as the cleanest respawn signal. `pyry status` exposes the same fact via a stable protocol field and is the public surface the test should anchor on. The supervisor log `claude exited` line is internal text whose key set can shift; ChildPID is the contract.
- **15s ack-receive deadline.** Encodes the documented upper bound. A shorter deadline (e.g. 5s) would be more sensitive but would flake under CI load and would diverge from the operator-facing doc.
- **No `pyry-status` JSON output mode used.** The CLI prints text, not JSON; adopting JSON would require a flag or new verb. Parsing the existing text output keeps this test additive.

## Concurrency model

Single test goroutine drives:

- Two CLI invocations (`pyry pair`, `pyry status` polled).
- One in-process `fakerelay` (its own listener goroutines, owned by the package; idempotent Close via `t.Cleanup`).
- One `fakephone` Dial — Send/Receive serialised in the test goroutine.
- The pyry daemon and its supervised fakeclaude run as child processes; the harness owns their lifecycles.

No new goroutines spawned by the test. All time-bounded operations use deadlines, no infinite blockers.

Shutdown: `t.Cleanup` order is reverse-registration: phone.Close → fakerelay.Close → Harness.Stop (SIGTERM → SIGKILL escalation). Already proven by `relay_send_message_test.go`.

## Error handling / failure modes

| Failure | Test outcome |
|---|---|
| Eviction WARN missing one of the four fields | `t.Fatalf` with full stderr dump — pinpoints the regression to a slog key rename. |
| Registry never reaches `evicted` within 5s | `waitForBootstrapState` calls `t.Fatalf` with the registry file contents — existing helper. |
| Initial PID still alive after eviction | `t.Fatalf` with PID — distinguishes "lifecycle flipped but SIGKILL never landed". |
| `send_message.ack` not received within 15s | `t.Fatalf "respawn did not complete within 15s after send_message"` — proves the silent-outage regression has shipped. |
| Ack received but `ChildPID` did not change | `t.Fatalf` showing both PIDs — distinguishes "handler responded but supervisor never respawned" (would indicate a future regression where Activate returns without actually rebooting the child). |
| send_message.ack arrives with `Retryable: true` and an error code | Surface verbatim — distinguishes timeout vs other Activate failure. The handler emits `CodeServerBinaryOffline` on Activate timeout (`internal/relay/handlers/send_message.go:75-100`). The test currently treats only `TypeAck` as success; if the handler ever flips this to an error envelope on the same Type, augment the assertion to inspect `AckPayload`.

No retry loops in the test — every wait is a single bounded poll. If the test flakes in CI, the root cause is one of: (a) eviction window too tight (raise from 2s); (b) cold-start go-build dwarfing the wall budget (the harness already caches via `binOnce` / `fakeClaudeOnce`); (c) a real regression — investigate before raising deadlines.

## Testing strategy

This *is* the test. RED→GREEN evidence for the PR body — the AC requires the developer to demonstrate that reverting #396's production diff (`c974d59`) on a scratch branch makes this test fail. The revert removes the `Activate`-before-`WriteUserTurn` call in `handlers.SendMessage`; on the reverted binary:

- The eviction phases (1, 2) still pass — eviction is unchanged.
- Phase 4 receives an ack (the handler still emits it pre-#396 — that was the silent-failure shape).
- Phase 5 fails: no respawn was triggered, so `ChildPID` stays at 0 / never differs from initialPID, and the 3s wait times out. **This is the test's bite.**

Equally important: after the developer un-reverts and runs the test on current `main`, all five phases pass within ~10-15s wall clock.

Run command:

```
go test -tags=e2e -run TestE2E_IdleEviction_RespawnsOnSendMessage -race ./internal/e2e/...
```

Wall budget target: under 30s end-to-end (PO AC). Realistic breakdown: harness ready ~0.5s; eviction wait ~2.5s; phone hello ~0.5s; send_message respawn-and-ack ~2-5s (Activate → runOnce → setPTY → WriteUserTurn → ack write); status poll ~0.1s; teardown ~0.5s. Total ~6-9s on a warm cache, well under the 30s ceiling.

## Open questions

- **Conversation ID seeding before or after pair?** `relay_send_message_test.go:48-56` seeds *after* `pyry pair` and *before* `StartRotationWithRelay`. Same ordering here. The dir `<home>/.pyry/test/` is created by `pair` as a side effect; the test relies on that.
- **Should the test also assert a `claude exited` log line with `signal: killed`?** Tempting redundancy — the eviction WARN is the operator-facing signal #396 added precisely so operators don't need that correlation. Skip it. Adding it would couple the test to the supervisor's exit-error wording and double-pin the same fact.
- **Does the test need to send a second `send_message` after respawn to prove writeability?** No. The single ack returning to the phone proves end-to-end writeability: the handler calls `WriteUserTurn` *before* emitting the ack (`internal/relay/handlers/send_message.go` order). The PID-changed assertion in phase 5 then proves the writeability was against the *new* child.

## Split proposal

Not needed. Production source file count: **0**. Single new test file, ~120-150 lines. Below every red line.

## Production-file scope self-check

- Production source files (`*.go` excluding `*_test.go`) created or modified by this spec: **0**.
- Test files created: **1** (`internal/e2e/respawn_after_eviction_test.go`).
- Spec stays comfortably within the size-S envelope.
