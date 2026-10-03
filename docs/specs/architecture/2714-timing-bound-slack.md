# #2714: loosen two timing bounds that flake under gate load

## Files read

- `internal/control/dial_test.go` → `TestDialWithRetry`: the two timed subtests carry a fixed `+50ms` upper-bound slack; the gate measured 187.8ms against 150ms.
- `internal/control/dial.go` → `dialWithRetry`: with no caller deadline it installs `DialTimeout` (5s, `client.go`), so a loop that ignores `budget` runs to 5s. That is what the upper bound must still catch.
- `internal/agentrun/streamrunner/watchdog_test.go` → `TestRun_SlowTool_NoFire`: `IdleTimeout` 500ms, helper sleep 1500ms.
- `internal/agentrun/streamrunner/helper_test.go` → `slow_tool` mode: prints init and a `tool_use` assistant line, then sleeps `GO_STREAMRUNNER_HELPER_SLEEP_MS`. Before its first line the parser is awaiting the assistant turn, so the idle timeout also covers the helper's startup.

## Change

`TestDialWithRetry`: replace both `+50ms` slacks with one named `schedSlack` of 500ms. The bounds become 530ms and 600ms, an order of magnitude under `DialTimeout`, so a `budget`-ignoring loop still fails. The `elapsed >= budget` lower bound is untouched.

`TestRun_SlowTool_NoFire`: raise `IdleTimeout` to 2s, so a re-exec of the race-built helper has seconds to print its first line, and raise the helper sleep to 5s, 2.5 times the threshold, so a type-blind watchdog still fires during the tool silence. The test is `t.Parallel()`, so the extra wall time overlaps the package's other tests. The 15s context still leaves 10s over the sleep. Production code does not move.

## Testing strategy

The existing assertions are the coverage: `go test -race -run 'TestDialWithRetry' ./internal/control/` and `-run TestRun_SlowTool_NoFire ./internal/agentrun/streamrunner/` with `-count` to confirm stability.
