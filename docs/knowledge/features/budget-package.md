# `internal/agentrun/budget` — pyry-side `--max-turns` enforcement

`Counter` enforces the per-agent turn budget for `pyry agent-run` by counting assistant JSONL entries and signalling claude when the cap is hit. The leaf unit; [`internal/agentrun/ptyrunner`](ptyrunner-package.md) wires `OnEvent` into the loop that drains `tuidriver.TailJSONL`, calls `OnEndOfTurn` when `tuidriver.IsEndTurn(entry)` fires for that entry, and routes `Terminate` / `Kill` to `cmd.Process.Signal(SIGTERM)` / `SIGKILL` (#479 / #512).

## Why it exists

`claude -p` self-enforces `--max-turns` — it exits at the budget. Interactive `claude` (what `pyry agent-run` spawns) does not; it waits for the next user turn instead. The dispatcher's existing per-agent turn-budget invariant (the `error:max_turns_salvaged` salvage workflow) presumes claude exits at the budget, so pyry must enforce the cap itself to keep the invariant intact across the `claude -p` → interactive migration.

## Surface

```go
type Reason string

const (
    ReasonCompletion Reason = "completion" // natural end_turn first
    ReasonMaxTurns   Reason = "max_turns"  // SIGTERM sent at budget
    // zero value "" — neither has fired (e.g. ctx-cancel teardown)
)

type Config struct {
    MaxTurns    int           // required, must be > 0
    Terminate   func() error  // required; production → cmd.Process.Signal(SIGTERM)
    Kill        func() error  // required; production → cmd.Process.Signal(SIGKILL)
    GracePeriod time.Duration // zero → 5s default (mirrors supervisor.spawnWaitDelay)
    Logger      *slog.Logger  // optional, defaults to slog.Default()
}

func New(cfg Config) (*Counter, error)

func (c *Counter) OnEvent(entry tuidriver.JSONLEntry) // caller invokes per entry from the tuidriver.TailJSONL drain
func (c *Counter) OnEndOfTurn()                       // caller invokes when tuidriver.IsEndTurn(entry) is true, after OnEvent
func (c *Counter) Reason() Reason
func (c *Counter) Stop()                   // cancel pending SIGKILL timer
```

## Counting rule

`OnEvent` filters on `entry.Type == "assistant"` — non-assistant kinds (`user`, `tool_use`, `tool_result`, `system`, `attachment`, `""`) return immediately without incrementing. The whitelist matches the same shape `tuidriver.TailJSONL` surfaces (every well-formed line, classified by `entry.Type`). Each assistant entry counts as one turn, **including** empty-content transitional `end_turn` blocks (validated by parent #329's "Unknown 3 PASS" spike across 1151 real session JSONLs). This filter is defensive — the caller cannot be trusted to pre-filter.

`OnEvent` increments the count first, then branches:

- Already fired — return (idempotent).
- `count < MaxTurns` — return.
- `count >= MaxTurns` — set `fired = true`, set `reason = ReasonMaxTurns`, arm the grace timer via `time.AfterFunc(GracePeriod, killAfterGrace)`, then unlock and invoke `cfg.Terminate()`.

End-of-turn classification is the **caller's** job (#512). The caller invokes `OnEndOfTurn` only when `tuidriver.IsEndTurn(entry)` returns true, after `OnEvent(entry)` has been called for the same entry. The pre-#512 early-return branch on `ev.EndOfTurn` is gone; see § "Reason semantics" below for the boundary-case consequence.

## SIGTERM → SIGKILL escalation

Once `Terminate` fires, a `time.AfterFunc(GracePeriod, killAfterGrace)` timer is armed. `killAfterGrace` re-checks `killTimer != nil` under the lock before nilling it and invoking `cfg.Kill()` — so a `Stop()` call that races a firing timer leaves the system in a consistent state (Kill may or may not have fired; both outcomes are safe).

`Stop()` is the cancellation handle: it nils `killTimer` under the lock then calls `t.Stop()` on the captured timer. Idempotent — calling `Stop` with no pending timer is a no-op. The driver calls it during teardown after `cmd.Wait` returns.

Default `GracePeriod` is 5s, copied from `supervisor.spawnWaitDelay` (not imported — the constant is duplicated to keep the budget package decoupled from the supervisor package; the units are independent).

## Reason semantics — first observed terminal event wins

- `OnEndOfTurn` sets `reason = ReasonCompletion` only if `reason == ""`. If `ReasonMaxTurns` is already set (budget fired on a prior `OnEvent` or the same-entry `OnEvent`), the completion signal does NOT overwrite it.
- **Budget-boundary case is `ReasonMaxTurns`, not `ReasonCompletion` (#512 semantic flip).** When the `MaxTurns`-th assistant entry is itself end-of-turn, the budget wins: the caller invokes `OnEvent(entry)` first (synchronously under `c.mu`, sets `c.reason = ReasonMaxTurns`, fires `Terminate`), and the caller's subsequent `OnEndOfTurn()` is a no-op because the first-terminal-wins guard refuses to overwrite a non-empty reason. The pre-#512 behaviour (boundary classified as `completion`) is gone along with the deleted EOT early-return branch — see [codebase/512.md](../codebase/512.md) for the rationale.
- Zero value (`""`) means neither terminal event fired — typically a context-cancellation teardown.

## Concurrency model

A single `sync.Mutex` guards `count`, `reason`, `fired`, and `killTimer`. All public methods acquire the lock at entry. The only goroutine the Counter spawns is the implicit one inside `time.AfterFunc`; its callback (`killAfterGrace`) acquires the same mutex but releases it before calling `cfg.Kill()` — external callbacks run unlocked to avoid deadlocking a caller that holds the lock indirectly.

In production:

- `OnEvent` / `OnEndOfTurn` fire from the agent-run caller's goroutine that drains the `tuidriver.TailJSONL` channel (the same `ptyrunner.Run` goroutine; see [ptyrunner-package.md](ptyrunner-package.md)).
- The grace timer fires from `time.AfterFunc`'s anonymous goroutine.
- `Stop` / `Reason` fire from the driver goroutine.

At the budget boundary, `OnEvent` then `OnEndOfTurn` are sequential within the same drain goroutine (not concurrent); `OnEvent` grabs `c.reason = ReasonMaxTurns` first under `c.mu`, and the subsequent `OnEndOfTurn` cannot overwrite a non-empty reason.

## Error handling

- `cfg.Terminate()` returning an error (e.g. ESRCH because claude already died): log at Warn and continue. The grace timer is still armed — Kill will follow in `GracePeriod`. Pinned by `TestTerminateError_DoesNotBlockKill`.
- `cfg.Kill()` returning an error: log at Warn (pinned by `TestKillError_IsLogged`). No panic, no surface to the caller — `OnEvent` has no caller to return to (it's a callback).
- Malformed `Config` at `New` time: returns an error immediately. Downstream integration wraps with the `agentrun:` namespace prefix.

## Why a callback interface, not direct `*exec.Cmd` ownership

Two reasons:

1. **Testability.** Unit tests verify "SIGTERM fires exactly at budget" and "SIGKILL fires after grace, not before" by injecting recording stubs (`signalRecorder` in `budget_test.go`). Tests do not spawn real processes; grace periods compress to ~50ms.
2. **Separation.** Process lifecycle stays in `supervisor.SpawnPTY` and `agentrun.Drive`. The Counter is a pure budget enforcer; the wiring layer (downstream integration) translates `Terminate` / `Kill` to `cmd.Process.Signal(syscall.SIGTERM)` / `syscall.SIGKILL`.

The Counter does not import `os/exec`.

## Why a custom grace timer instead of `exec.Cmd.WaitDelay`

`supervisor.SpawnPTY` already configures `cmd.Cancel = SIGTERM` + `cmd.WaitDelay = 5s = SIGKILL grace`. In production the integration could cancel a context and let `exec.Cmd` send both signals. The Counter doesn't, because the AC unit tests pin SIGTERM-at-budget and SIGKILL-after-grace as **Counter** behaviour, not as `exec.Cmd` behaviour — routing through context cancellation makes those tests indirect (asserting the context was cancelled and then trusting exec). The two grace mechanisms are not in conflict; the integration ticket may keep both as belt-and-suspenders.

## Tests

`internal/agentrun/budget/budget_test.go`, table-driven where shapes match. No real processes. `signalRecorder` mutex-wraps call counts and the timestamp of the first call to each signal.

- `TestNew_Validation` — zero / negative `MaxTurns`, nil `Terminate`, nil `Kill` each return an error.
- `TestOnEvent_NonAssistantKindsDoNotCount` — feeds every non-assistant kind; asserts Terminate fires only after `MaxTurns` assistant events arrive, regardless of interleaved non-assistant events.
- `TestOnEvent_SIGTERMFiresExactlyAtBudget` — Terminate not called at budget-1, called exactly once at budget, not called again at budget+1 / budget+2; `Reason()` is `ReasonMaxTurns`.
- `TestOnEvent_SIGKILLFiresAfterGrace` — `GracePeriod=80ms`; Kill not called at grace/2, called exactly once after grace, and the elapsed time between Terminate and Kill is `>= grace`.
- `TestStop_CancelsPendingSIGKILL` — hit budget, call `Stop`, sleep 3×grace; Kill not called. Second `Stop` is a no-op.
- `TestStop_WithoutBudgetHit` — Stop with no pending timer; no signals fire.
- `TestOnEndOfTurn_ReasonCompletion` — fewer than MaxTurns events, caller invokes `OnEndOfTurn`; `Reason() == ReasonCompletion`, no signals.
- `TestOnEndOfTurn_DoesNotOverwriteMaxTurns` — budget hit, then OnEndOfTurn called; `Reason()` stays `ReasonMaxTurns` (first-terminal-wins). (`TestOnEvent_BudgetBoundaryEndOfTurnIsCompletion` was **deleted** by #512 along with the EOT early-return branch it pinned; `TestOnEvent_SIGTERMFiresExactlyAtBudget` already covers the new boundary behaviour.)
- `TestReason_ZeroValueBeforeTerminalEvent` — pre-terminal `Reason()` is `""`.
- `TestTerminateError_DoesNotBlockKill` — Terminate returns ESRCH; Kill still fires after grace.
- `TestKillError_IsLogged` — Kill returns an error; "kill failed" appears in the slog Warn output via a `syncWriter`-wrapped `strings.Builder` (slog handlers may write concurrently from `time.AfterFunc` and the test goroutine).

Grace-timer tests use `time.Sleep` with millisecond-scale `GracePeriod` values; the suite stays sub-second. No `Clock` interface — per the project's evidence-based-fix-selection principle, defer until flakiness is observed.

## Out of scope (this package)

- **On-the-wire `exit_reason` trailer.** `streamjson.Emitter.SetExitReason(ExitReasonMaxTurns)` is the seam; ptyrunner's Terminate callback drives it.
- **End-to-end signal verification.** Covered by ptyrunner's integration tests (the budget-hit path is exercised by `TestRun_BudgetHitBeforeEndOfTurn`).

## Related

- Sibling [ptyrunner-package.md](ptyrunner-package.md) — the consumer that drains `tuidriver.TailJSONL` and calls `OnEvent` / `OnEndOfTurn` per entry; routes `Terminate` / `Kill` to `cmd.Process.Signal`. Post-#512 the Counter has a single in-tree caller.
- Sibling [streamjson-package.md](streamjson-package.md) — `SetExitReason(ExitReasonMaxTurns)` carries the budget classification into the `result` trailer.
- [tui-driver `pkg/tuidriver/jsonl.go`](https://github.com/pyrycode/tui-driver/blob/main/pkg/tuidriver/jsonl.go) — `JSONLEntry` (post-#512 the `OnEvent` parameter shape) and `IsEndTurn(entry)` (the caller's discriminator for invoking `OnEndOfTurn`).
- [codebase/512.md](../codebase/512.md) — migration ticket: signature flip, deleted EOT branch, deleted boundary-completion test, boundary semantic shift to `max_turns`.
- Sibling [pyry-agent-run-command.md](pyry-agent-run-command.md) / [agentrun-package.md](agentrun-package.md) — `--max-turns` flag is parsed and propagated into the ptyrunner Config which constructs the Counter.
- Parent #329 — Phase A spike that established the assistant-entry counting rule empirically.
