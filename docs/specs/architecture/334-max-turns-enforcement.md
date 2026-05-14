# Spec: agent-run pyry-side `--max-turns` enforcement + SIGTERM-then-SIGKILL teardown (#334)

## Files to read first

- `internal/agentrun/jsonl/tail/watcher.go:33-65` — `tail.Config.OnEvent` / `OnEndOfTurn` callback signatures the Counter wires into.
- `internal/agentrun/jsonl/tail/watcher.go:242-258` — `drain` shows that OnEvent fires for **every** Reader event (all `Kind`s, not just `assistant`); Counter must filter.
- `internal/agentrun/jsonl/reader.go:45-83` — `Event` shape, in particular `Kind` (whitelist: `assistant`, `user`, `tool_use`, `tool_result`, `system`, `attachment`, `""`) and `EndOfTurn` (deterministic natural-completion signal).
- `internal/agentrun/jsonl/reader.go:171-242` — confirms `Kind == "assistant"` is the right filter for the budget unit (and that `EndOfTurn=true` always implies `Kind=="assistant"`).
- `internal/agentrun/drive.go` — current driver. Does **not** yet integrate the tail watcher or the Counter; that integration is a downstream ticket. This spec adds a leaf unit with no call-site changes.
- `internal/supervisor/spawn.go:27-46` — confirms the `5 * time.Second` grace window we mirror; reuse the value, not a new constant in this package.
- Parent #329 "Unknown 3 PASS" comment — empirical counting rule (each `type=="assistant"` line = one turn, including empty-content transitional `end_turn`).

## Context

`pyry agent-run` (#332) spawns interactive `claude`, which — unlike `claude -p` — does **not** self-enforce `--max-turns`. The dispatcher's per-agent turn-budget invariant (its `error:max_turns_salvaged` workflow) presumes claude exits at the budget; that presumption breaks under interactive mode unless pyry enforces the cap itself.

This ticket lands the **leaf unit** that counts assistant entries and signals claude when the budget is hit. Integration into `Drive` (wiring this Counter to a live `tail.Watcher` and to `cmd.Process.Signal`) is a separate, downstream ticket. The deliverable here is a single new package consumable by that future integration, with no call-site changes in this PR.

Why a leaf unit and not the integration: the integration needs a session-ID discovery mechanism (claude's session UUID isn't known at spawn time), which is non-trivial and out of scope. The Counter can be designed, tested, and merged independently because its interface is exactly the `tail.Config` callback shape — wiring is mechanical once the integration ticket has the SessionID in hand.

## Design

### Package layout

New sub-package, sibling to `jsonl/` and `jsonl/tail/`:

```
internal/agentrun/budget/
  budget.go         ~80 lines — Counter + Config + Reason
  budget_test.go    table-driven tests (see § Testing strategy)
```

One new production source file. No call-site changes anywhere in the tree.

### Types

```go
// Reason is the terminal outcome reported by Counter after the agent-run
// driver returns.
type Reason string

const (
    ReasonCompletion Reason = "completion" // claude reached natural end_turn
    ReasonMaxTurns   Reason = "max_turns"  // pyry SIGTERMed at the budget
    // zero value "" means neither has fired yet (e.g. ctx-cancel teardown)
)

// Config configures Counter. All required fields must be set; New returns
// an error otherwise.
type Config struct {
    MaxTurns int // required; must be > 0

    // Terminate is invoked exactly once when the assistant-entry count
    // reaches MaxTurns on a non-end_turn event. Production wires this to
    // cmd.Process.Signal(syscall.SIGTERM); tests inject a recording stub.
    Terminate func() error // required

    // Kill is invoked exactly once when the grace period elapses without
    // Stop having been called. Production wires this to
    // cmd.Process.Signal(syscall.SIGKILL); tests inject a recording stub.
    Kill func() error // required

    // GracePeriod is the SIGTERM→SIGKILL window. Zero means default (5s,
    // matching supervisor.spawnWaitDelay). Tests set this to milliseconds.
    GracePeriod time.Duration

    Logger *slog.Logger // optional; defaults to slog.Default()
}

// Counter counts assistant JSONL entries and enforces the MaxTurns budget
// by invoking Terminate when the count reaches MaxTurns, then escalating
// to Kill after GracePeriod.
//
// Wire (*Counter).OnEvent and (*Counter).OnEndOfTurn to the same-named
// callbacks of tail.Config. Call Reason() after the watcher's Run returns
// to retrieve the outcome. Call Stop() during teardown to cancel any
// pending SIGKILL grace timer.
//
// Safe for concurrent use: OnEvent fires from the watcher's Run goroutine,
// the grace timer fires from time.AfterFunc's goroutine, Stop and Reason
// fire from the driver goroutine.
type Counter struct {
    cfg       Config
    mu        sync.Mutex
    count     int
    reason    Reason
    fired     bool        // true once Terminate has been invoked
    killTimer *time.Timer // non-nil iff a grace timer is pending
}
```

### Constructor

```go
func New(cfg Config) (*Counter, error)
```

Validates `MaxTurns > 0`, non-nil `Terminate`, non-nil `Kill`. Defaults
`GracePeriod` to 5s (matching `supervisor.spawnWaitDelay`; copy the literal,
do not import the constant — the units are independent and we should not
couple the budget package to the supervisor package). Defaults `Logger`.

### Method contracts

`OnEvent(ev jsonl.Event)`:

- Filter: if `ev.Kind != "assistant"`, return immediately. (Defensive even though the spec asserts the watcher fires only on assistant entries — post-#353 it fires for every kind. See `watcher.go:242-258`.)
- Lock the mutex.
- Increment `count`.
- If `ev.EndOfTurn` is true, return. Claude is finishing naturally; `OnEndOfTurn` will set `reason`.
- If `fired` is true, return (idempotent — `Terminate` already invoked on a prior event).
- If `count >= MaxTurns`: set `fired = true`, set `reason = ReasonMaxTurns`, call `cfg.Terminate()` (log at Warn on error), and arm the grace timer via `time.AfterFunc(GracePeriod, c.killAfterGrace)`.

`OnEndOfTurn()`:

- Lock the mutex.
- If `reason == ""`, set `reason = ReasonCompletion`. (If `reason == ReasonMaxTurns` already, do nothing — a budget-boundary natural end is already classified as max_turns under "first observed terminal event wins". See § Open questions.)

`Reason() Reason`:

- Lock-and-read; returns the current `reason` value. Safe to call any time, but only stable after the watcher's Run has returned.

`Stop()`:

- Lock the mutex.
- If `killTimer != nil`, call `killTimer.Stop()` and nil it. Idempotent.

`killAfterGrace()` (internal, fires from `time.AfterFunc`):

- Lock the mutex.
- If `killTimer == nil`, return (Stop won the race).
- Nil `killTimer`.
- Unlock before calling `cfg.Kill()` (don't hold the mutex across an external callback).
- Log at Warn if `Kill` returns an error.

### Data flow (integration sketch, NOT part of this ticket)

```
tail.Watcher.Run goroutine
  ├── reader.Next() → ev
  ├── OnEvent(ev)  ─────────────────────────────►  Counter.OnEvent
  │                                                  ├── count++ (if assistant)
  │                                                  └── if count >= MaxTurns: Terminate(); arm grace timer
  └── if ev.EndOfTurn: OnEndOfTurn()  ──────────►  Counter.OnEndOfTurn
                                                     └── if reason == "": reason = completion

[grace timer fires after 5s] ─────────────────────►  Counter.killAfterGrace
                                                     └── Kill()

driver goroutine (cmd.Wait returns) ──────────────►  Counter.Stop()
                                                     └── cancel pending grace timer
```

### Why a callback interface, not direct `*exec.Cmd` ownership

The Counter must not import `os/exec` or hold a `*exec.Cmd`. Two reasons:

1. **Testability.** Unit tests verify "SIGTERM fires exactly at budget" and "SIGKILL fires after grace, not before" by injecting recording stubs for `Terminate` / `Kill`. Tests must not spawn real processes.
2. **Separation.** Process lifecycle is owned by `supervisor.SpawnPTY` and `Drive`. The Counter is a pure budget enforcer; the wiring layer (downstream integration ticket) translates `Terminate` to `cmd.Process.Signal(syscall.SIGTERM)`.

### Why a custom grace timer instead of relying on `exec.Cmd.WaitDelay`

`SpawnPTY` already configures `cmd.Cancel = SIGTERM` + `cmd.WaitDelay = 5s = SIGKILL grace`. In production it would suffice for the Counter to cancel a context and let `exec.Cmd` send both signals.

We don't do that here because:

- AC#2 / AC#3 unit tests pin the SIGTERM-at-budget and SIGKILL-after-grace timings as **Counter** behaviour, not as exec.Cmd behaviour. Routing through context cancellation makes those tests indirect (you'd assert the context was cancelled and then trust exec).
- The Counter's grace timer and exec.Cmd's WaitDelay are not in conflict in production: the integration ticket may choose to wire Terminate/Kill to direct `cmd.Process.Signal` calls and leave `cmd.Cancel` unset for the budget path, OR keep the cancel mechanism and treat Counter.Kill as a redundant safety. That choice belongs to the integration ticket.

## Concurrency model

Single mutex (`sync.Mutex`) guards `count`, `reason`, `fired`, and `killTimer`. All public methods take the lock at entry. The only goroutine the Counter spawns is the implicit one inside `time.AfterFunc` — its callback acquires the same mutex and releases it before calling `cfg.Kill()` (the external callback runs unlocked to avoid deadlocking a caller that holds it indirectly).

Race outcomes that matter:

- **Stop vs grace-timer fire.** `time.Timer.Stop()` returns `false` if the timer has already fired; the `killAfterGrace` body re-checks `killTimer == nil` under the lock, so a Stop that races with a firing timer leaves the system in a consistent state (Kill may or may not have been called — both are safe outcomes).
- **OnEvent vs OnEndOfTurn at the budget boundary.** The watcher fires OnEvent then OnEndOfTurn from the same goroutine, so they are sequential, not concurrent. If `EndOfTurn=true` on the budget-th event, OnEvent's "return on EndOfTurn" branch fires (no termination), then OnEndOfTurn sets reason=completion. The budget is treated as "natural completion at exactly the budget", not as a max-turns hit. See § Open questions for an alternative.

## Error handling

- `cfg.Terminate()` returning an error: log at Warn and continue. The grace timer is still armed — if Terminate failed (e.g. ESRCH because the process already died), Kill will follow in 5s and will also benignly fail. We do not surface Terminate errors to the caller because there is no caller — OnEvent is invoked from the watcher goroutine.
- `cfg.Kill()` returning an error: log at Warn. Same rationale.
- Malformed config in `New`: return `error` immediately. The caller (downstream integration ticket) wraps with `"agentrun: budget: %w"`.

## Testing strategy

Table-driven tests in `internal/agentrun/budget/budget_test.go`. No spawned processes; `Terminate`/`Kill` are closures over recording slices/counters.

Scenarios (one test function each, or table-driven where shapes match):

- **Counter increments per assistant OnEvent.** Feed N synthetic events with `Kind="assistant"`, `EndOfTurn=false`, MaxTurns=N+1. Assert internal count after each via observable behaviour (no Terminate fires until budget); assert Terminate fires on the (N+1)-th event.
- **Non-assistant kinds do not count.** Feed `Kind` values from the whitelist (`user`, `tool_use`, `tool_result`, `system`, `attachment`, `""`) before each assistant event. Assert the budget is reached exactly when N assistant events have been delivered, regardless of interleaved non-assistant events.
- **SIGTERM fires exactly at budget.** MaxTurns=3. Feed 2 assistant events → assert `Terminate` not yet called. Feed the 3rd → assert called exactly once. Feed a 4th → assert `Terminate` not called again (idempotent).
- **SIGKILL fires after grace, not before.** GracePeriod=50ms (a millisecond-scale value, chosen to keep the suite under a second). After hitting the budget, sleep 25ms → assert `Kill` not yet called. Sleep another 50ms → assert `Kill` called exactly once.
- **Stop cancels pending SIGKILL.** Hit the budget; immediately call `Stop()`. Sleep > GracePeriod. Assert `Terminate` was called (SIGTERM still fires synchronously at the budget) and `Kill` was **not** called.
- **Reason = completion when OnEndOfTurn fires first.** Feed fewer than MaxTurns assistant events, the last with `EndOfTurn=true`. Call `OnEndOfTurn()`. Assert `Reason() == ReasonCompletion`.
- **Reason = max_turns when budget is hit on a non-end_turn event.** Feed MaxTurns assistant events with `EndOfTurn=false`. Assert `Reason() == ReasonMaxTurns`.
- **Budget-boundary natural completion.** Feed MaxTurns-1 assistant events. Feed one more with `EndOfTurn=true`. Assert `Terminate` not called, then OnEndOfTurn → `Reason() == ReasonCompletion`. (See § Open questions for the alternate semantic.)
- **`New` validates required fields.** Sub-cases: `MaxTurns=0`, nil `Terminate`, nil `Kill`. Each returns a non-nil error.

Tests use `time.Sleep` for the grace-timer scenarios. That's acceptable here because the grace period is configurable and we set it to a small value (50ms); the suite stays fast. If flakiness emerges later, swap to a Clock interface in a follow-up — do not pre-build it for a failure mode that has not been observed (per the project's evidence-based-fix-selection principle).

No integration tests in this ticket. End-to-end verification (real claude, real signals) is covered by the downstream integration ticket once the Counter is wired into `Drive`.

## Open questions

- **Budget-boundary natural completion classification.** This spec treats "assistant entry with `EndOfTurn=true` at exactly count==MaxTurns" as `ReasonCompletion`. An alternative is to classify it as `ReasonMaxTurns` (claude used every turn the budget allowed). The dispatcher's salvage workflow distinguishes "ran to budget exhaustion" from "agent declared itself done"; if the future stream-json emitter ticket needs the former semantics, flip this in a follow-up. The Counter's contract is small enough that the switch is one line in `OnEvent`. Default to `ReasonCompletion` here because the natural `end_turn` signal is a stronger statement of completion than the turn count is.
- **Logger key conventions.** Use `slog.Int("count", c.count)`, `slog.Int("max_turns", c.cfg.MaxTurns)`, `slog.String("reason", string(reason))` on relevant Warn/Info lines. No structured key for the signalling error itself — pass via the standard `"err"` field per the repo convention.
