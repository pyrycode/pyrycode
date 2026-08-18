# #1492 — Cancel the daemon ctx before the joins that need it cancelled

**Size:** S · **Type:** bug · **Ticket:** [#1492](https://github.com/pyrycode/pyrycode/issues/1492)

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`.

| File | Symbol | What to extract |
|---|---|---|
| `cmd/pyry/main.go` | `runSupervisor` | The daemon body. Both edit sites live here: the `defer relayCleanup()` registration, and the `pool.Run` → `<-ctrlDone` → `<-qDone` sequence. |
| `cmd/pyry/main.go` | `fatalCause` | The exit classification the fix must still reach, and which AC-4 pins. |
| `cmd/pyry/relay.go` | `startRelay` | What `relayCleanup` closes over: `legCleanup` (conn close + drain) then `<-waitDone`. Also the `conn.Wait()` classifier that calls `w.shutdown(err)` on a persistent 4409. |
| `cmd/pyry/relay.go` | `startRelayV2` | The returned drain closure. The three producer cleanups it calls are wired **unconditionally** — no PTY/stream gate. |
| `cmd/pyry/queue_state_v2.go` | `startQueueStateStreamV2`, `queueStateEmitterV2.Run` | The exact deadlock shape: cleanup is `<-done`; `Run` returns only on `<-ctx.Done()` or a closed input channel that nothing closes. |
| `cmd/pyry/session_error_v2.go` | `startSessionErrorStreamV2` | Same shape. |
| `cmd/pyry/session_transition_v2.go` | `startSessionTransitionStreamV2` | Same shape. |
| `internal/msgqueue/queue.go` | `Queue.Run` | `<-ctx.Done()` sits *before* the drain join, so the `<-qDone` receive cannot complete while the daemon ctx is live. Site 2's blocker. |
| `internal/sessions/pool.go` | `Pool.Run` | `return g.Wait()` — the early non-ctx error source, and the AC-3 mutant site. Note `gctx` is derived from the daemon ctx; cancelling `gctx` does **not** cancel the daemon ctx. |
| `internal/control/server.go` | `Server.Listen`, `ErrInstanceRunning` | The single reachable error return between `startRelay` and `pool.Run`, and the message text AC-1's test asserts on. |
| `internal/e2e/harness.go` | `StartIn`, `StartExpectingFailureIn`, `spawn`, `spawnWith` | AC-1's vehicle. Note `spawnWith` builds `-pyry-socket=` from a freshly-minted path and appends `extraFlags` **after** it (flag parsing is last-wins). |
| `internal/e2e/harness.go` | `ensurePyryBuilt`, `shortHome` | `ensurePyryBuilt` shells out to its own `go build`, so a `-overlay` on `go test` does **not** reach the daemon binary. `PYRY_E2E_BIN` is the seam that does — see § Verification. |
| `internal/e2e/startup_test.go` | `TestE2E_Startup_CorruptRegistryFailsClean` | The file the new test joins, and the house shape for `RunResult` assertions. |
| `internal/e2e/relay_test.go` | `TestRelay_4409_PersistentExitsNonZero` | AC-4's stays-green test. |
| `internal/config/config.go` | `DefaultConfig` | Why the relay leg is on by default (`wss://relay.pyrycode.dev`), and why the new test pins an explicit dead loopback URL instead of inheriting it. |

## Two corrections to the ticket body

Both are small but load-bearing; neither changes the ACs.

1. **The function is `runSupervisor`, not `run`.** `run` is the CLI verb dispatcher near the top of `cmd/pyry/main.go`; every site the ticket cites (`687`, `940`, `960`, `985`) is inside `runSupervisor`. The ticket's "`run()` reaches its exit-code classification" means `runSupervisor` reaching `fatalCause`.

2. **`relay.Connect` never dials synchronously.** It validates config, constructs the transport, launches `go c.run(ctx)`, and returns. Only *config* errors (bad scheme, missing identity, missing device registry) can fail `startRelay`; an unreachable, refusing, or 409-ing relay cannot. This confirms the ticket's "needs only `relayURL != ""`" claim in the strongest form — the deferred cleanup is registered, and the producer goroutines are running, before a single byte reaches the relay. It also unlocks a fully hermetic test vehicle (§ AC-1) that needs no fakerelay and no env var.

## Context

`runSupervisor` builds a two-layer shutdown context (`sigCtx` → `ctx`/`cancelCause`) and registers `defer cancelCause(nil)` immediately. Defers run LIFO, so that cancel is the **last** thing to run — after every later-registered defer, including `defer relayCleanup()`.

Two joins then wait on goroutines whose only exit is `<-ctx.Done()`:

- **Site 1 — the deferred `relayCleanup`.** It calls `conn.Close()` then the drain returned by `startRelayV2`, which joins three producers (`startSessionTransitionStreamV2`, `startQueueStateStreamV2`, `startSessionErrorStreamV2`). Each cleanup is a bare `<-done` over a `Run` loop that selects on `ctx.Done()` and its input channel; nothing closes the input channel. So on any error return between `startRelay` and the end of `runSupervisor` — today, exactly `ctrl.Listen()` — the process blocks forever with the cancel that would unblock it queued *behind* the block. Operator-visible symptom: a second `pyry` start on a live socket hangs silently instead of printing `another pyry instance is already running`.

- **Site 2 — the `<-qDone` join.** Reached whenever `pool.Run` returns before the daemon ctx is cancelled — any early non-ctx error out of `g.Wait()` (a supervised session, the rotation watcher, the conversation sweep loop). `Queue.Run` blocks on `<-ctx.Done()` *before* joining its drains, so the receive never completes. This site is upstream of every defer, so no defer-ordering change can reach it.

The codebase already names the invariant both sites violate: *"cancel-then-join; joining first deadlocks"* (see `streamsup_runner_exit_test.go`).

## Design

The normal shutdown path is already correct and already exercised by every e2e teardown: ctx is cancelled first (SIGTERM / `pyry stop` / a fatal 4409), *then* the joins run. The fix is to make the error paths take that same, already-proven ordering — not to add new teardown machinery.

### Site 1 — compose the cancel into the cleanup defer

Replace the bare `defer relayCleanup()` with a defer that cancels first:

```go
// Defers run LIFO, so the `defer cancelCause(nil)` registered above runs
// AFTER this one — too late to unblock the drains relayCleanup joins.
// Cancel here instead, so an error return between startRelay and the end of
// runSupervisor takes the same cancel-then-join ordering the normal shutdown
// path already takes. First-cause-wins: a 4409 cause already recorded by the
// conn.Wait() classifier survives this nil.
defer func() {
	cancelCause(nil)
	relayCleanup()
}()
```

Why compose rather than register a second adjacent `defer cancelCause(nil)`: the ordering that makes two adjacent defers correct (register the cancel *after* the cleanup so it runs *before* it) reads backwards and invites a future edit to "fix" it. One closure states the sequence in source order.

Why this over the ticket's alternative of wrapping each post-`startRelay` error return with `cancelCause(err)`: there is one such return today and there will be more. A per-return wrap is a rule a future edit silently breaks; the defer covers every return, including panics, structurally.

Why this over hoisting `ctrl.Listen()` above `startRelay`: that ordering change has a genuine bonus (a double-start would stop contesting the live daemon's relay server-id at all), but it moves the control-server construction across the `approvalSurface` dependency, changes `defer ctrl.Close()` vs `defer relayCleanup()` ordering, and — the decisive point — leaves the wedge in place for every *other* error return that may later sit after `startRelay`. Out of scope; note it as a follow-up if the server-id contest ever bites.

### Site 2 — cancel before the joins

Immediately after `runErr := pool.Run(ctx)`, before the control-server stop and the two joins, cancel the daemon ctx:

```go
runErr := pool.Run(ctx)
// pool.Run can return with the daemon ctx still LIVE — any early non-ctx error
// out of its errgroup (g's ctx is derived from ours, so cancelling gctx does
// not cancel ctx). Queue.Run blocks on <-ctx.Done() before joining its drains,
// so the <-qDone join below would never complete. Cancel first; on the normal
// path ctx is already cancelled and this is a no-op.
cancelCause(nil)
```

On the normal shutdown path and on the fatal-4409 path the ctx is already cancelled when this runs, so the call is a no-op and behaviour is byte-identical to today. The only path it changes is the one that previously hung.

### Why `cancelCause(nil)` is safe on both sites — AC-4

`context.WithCancelCause` is first-cause-wins: the cancel func records a cause only if the context is not already cancelled. On the self-initiated fatal path the `conn.Wait()` classifier in `startRelay` has already called `w.shutdown(err)` with `relay.ErrServerIDConflict`, so both new `cancelCause(nil)` calls are no-ops and `fatalCause` still reports the conflict, still exits non-zero, and launchd still restarts the daemon. AC-4 is a property of the standard library, pinned end-to-end by the existing e2e (§ Verification).

### What is *not* changed

- No new exported symbols, no signature changes, no new files in `internal/`.
- `internal/e2e/harness.go` is untouched. The AC-1 test needs no new harness primitive (§ AC-1).
- The producers' own cleanup shape (`<-done` over a ctx-only `Run`) stays as-is. It is correct given a cancelled ctx; the bug was the caller's ordering, not the producers'. Do **not** add close-the-input-channel or timeout escapes to them — that is a defence for a failure mode that does not exist once the ordering is right.

## Concurrency model

No goroutines added or removed. The change is purely one of **ordering** on two paths:

| Path | Today | After |
|---|---|---|
| SIGTERM / `pyry stop` / fatal 4409 | cancel → `pool.Run` returns → joins → defers | unchanged (both new cancels are no-ops) |
| `ctrl.Listen()` error | defers → `relayCleanup` **blocks forever** | cancel → `relayCleanup` → … → `cancelCause(nil)` (no-op) |
| early non-ctx `pool.Run` error | `<-qDone` **blocks forever** | cancel → `<-ctrlDone` → `<-qDone` → classification → exit non-zero |

Shutdown sequence on the newly-unblocked error paths is identical to the normal path: ctx cancel → `conn.Close()` (closes `Frames`, which unblocks the v2 manager's `Run`) → producer cleanups return on `ctx.Done()` → `<-mgrDone` → `<-waitDone`.

## Error handling

- `ctrl.Listen()` failure keeps its current wrap (`control listen: %w`); `main` prints `pyry: control listen: another pyry instance is already running on <socket> — …` and exits 1. The fix changes only *whether that return is reached*, not its text.
- An early non-ctx `pool.Run` error keeps its current wrap (`supervisor: %w`) and its exit code.
- Neither new `cancelCause(nil)` can mask a cause, by first-cause-wins.

## Testing strategy

### AC-1 / AC-2 — the shipped e2e

New test in `internal/e2e/startup_test.go` (the established home for failed-start e2e, alongside the corrupt-registry test).

Scenario, as bullets — write it in the file's existing idiom:

- Allocate a home with `shortHome(t)`.
- Start daemon A with `StartIn(t, home, "-pyry-relay="+<dead loopback URL>)`. Use `wss://127.0.0.1:1/v2/server`: `wss://` passes the scheme validation with no `PYRY_ALLOW_INSECURE_RELAY`, port 1 is unbindable without root so nothing can ever answer, and the dial is asynchronous — the relay leg is fully *wired* (producers running, cleanup registered) while staying hermetic. Do **not** let the test inherit the `DefaultConfig` relay URL: that would dial `relay.pyrycode.dev` from `make check`.
- Start the second process with `StartExpectingFailureIn(t, home, "-pyry-socket="+h.SocketPath, "-pyry-relay="+<same dead URL>)`. `extraFlags` are appended after the harness's own `-pyry-socket=`, and flag parsing is last-wins, so the second process binds A's socket and trips `ErrInstanceRunning`.
- Assert the returned `RunResult` has a non-zero exit code and stderr containing `another pyry instance is already running`.
- Assert daemon A survived: `h.Run(t, "status")` exits 0.

Two things to record in the test's comment, because a reader will otherwise mis-read the helper:

- `StartExpectingFailureIn` polls the socket path *it* minted, not the overridden one, so its "unexpectedly became ready" arm cannot fire here. The load-bearing arms are the exit arm (returns `RunResult`) and the deadline arm (`t.Fatalf("neither exited nor became ready")`) — which is exactly the AC-2 red.
- Both processes share `$HOME` and `-pyry-name=test`, so they read the same server-id and registry. That is deliberate: it is what an operator's double-start actually looks like. The second process returns before `pool.Run`, so it never spawns a child and never rewrites A's state.

**AC-2 red on the unpatched tree** — see § Verification for the exact commands. The red is the 5-second `readyDeadline` exhausting with `e2e: pyry neither exited nor became ready`, not a wrong-value assertion.

### AC-3 — site 2, stated as an outcome plus a named mutant

No live repro exists: nothing in the daemon makes `pool.Run` return an early non-ctx error today (the supervisor retries forever, `Pool.supervise`'s `ErrPoolNotRunning` return is unreachable from `Pool.Run`, and the rotation watcher / sweep loop only fail on genuine faults). So the outcome is stated and demonstrated with a mutant rather than pinned by a shipped test — adding a permanently-vacuous e2e would be worse than none.

**Outcome:** when `pool.Run` returns an early non-ctx error with the daemon ctx still live, `runSupervisor` reaches `fatalCause`/the `supervisor: %w` return and the process exits non-zero, instead of blocking on `<-qDone`.

**Mutant `M1492-early-pool-error`:** in `Pool.Run`, immediately before `return g.Wait()`, add one errgroup member that forces the early return after startup:

```go
g.Go(func() error {
	time.Sleep(3 * time.Second)
	return fmt.Errorf("M1492: forced early pool.Run error")
})
```

The 3-second delay puts the failure comfortably after control-socket readiness (sub-second in practice) so the observation is not racing startup. `fmt` avoids adding an import.

Run it directly against the built binary — no test file needed (§ Verification). Expected: unpatched hangs until the external timeout kills it; patched exits non-zero within ~3s with `pyry: supervisor: M1492: forced early pool.Run error` on stderr. Include the relay flag on both runs so the same invocation also traverses site 1's deferred cleanup.

### AC-4 — the fatal cause survives

`TestRelay_4409_PersistentExitsNonZero` in `internal/e2e/relay_test.go`, unmodified, must stay green. It is non-vacuous for this change: on that path `w.shutdown(err)` cancels with the conflict cause, `pool.Run` then returns, and the run traverses the new `cancelCause(nil)` at site 2 before `fatalCause` reads the cause back. Green there *is* the first-cause-wins proof at the daemon level.

`TestRelay_4409` (transient) must also stay green — it asserts the daemon does *not* shut down, which the fix must not disturb.

### Regression gate

`make check` (which includes `cite-guard` — write symbol names, not `file:NNN`, in every comment the change adds).

## Verification — exact recipe

`ensurePyryBuilt` runs its own `go build`, so a `-overlay` passed to `go test` does **not** reach the daemon binary the harness spawns. `PYRY_E2E_BIN` is the seam: build the variant binary yourself, point the harness at it. No worktree writes on any of these runs.

**AC-2 red (unpatched daemon, patched test):**

```
git show HEAD:cmd/pyry/main.go > $SCRATCH/main-unpatched.go     # pre-change content
# overlay.json: {"Replace": {"<abs>/cmd/pyry/main.go": "$SCRATCH/main-unpatched.go"}}
go build -overlay=$SCRATCH/overlay.json -o $SCRATCH/pyry-unpatched ./cmd/pyry
PYRY_E2E_BIN=$SCRATCH/pyry-unpatched go test -tags e2e ./internal/e2e \
  -run TestE2E_Startup_SecondInstance... -count=1
```

Expect: FAIL with `neither exited nor became ready within 5s`. Then the same `go test` without `PYRY_E2E_BIN` → PASS.

**AC-3 both trees:** two overlays, one build each, run both under an external timeout with a throwaway `$HOME` and the same flag set the harness uses (`-pyry-socket`, `-pyry-name=test`, `-pyry-claude=<sleep-claude stand-in>`, `-pyry-idle-timeout=0`, `-pyry-workdir=$HOME`, `-pyry-relay=wss://127.0.0.1:1/v2/server`, `-- 99999`).

- `mutant-patched.json` replaces `internal/sessions/pool.go` only → expect exit within ~5s, non-zero, stderr names `M1492`.
- `mutant-unpatched.json` replaces `internal/sessions/pool.go` **and** `cmd/pyry/main.go` (with the pre-change copy) → expect the external timeout to fire.

Record both outcomes in the ticket.

## Open questions

- **Should `ctrl.Listen()` move above `startRelay` as a follow-up?** It would stop a double-start from momentarily contesting the live daemon's relay server-id. The relay conn is closed before the wedge today, so nothing is durably contested and there is no observed harm — file it only if the contest is ever seen in the wild.
- **Should `runSupervisor`'s teardown grow a bounded join timeout?** No — not on this ticket. Every join here is unblocked by a cancel the process controls; a timeout would convert a future ordering bug into a silent partial shutdown instead of a loud hang. Revisit only if a join is ever observed to hang with the ctx already cancelled.
