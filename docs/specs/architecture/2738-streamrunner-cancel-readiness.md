# #2738 — streamrunner cancel tests wait for the child to start

## Files read

- `internal/agentrun/streamrunner/runner.go` → `Run`: a cancelled ctx before `cmd.Start` returns `streamrunner: start: context canceled`; after start, the `ctx.Err() != nil` branch after `Wait` returns nil. The tests need the cancel on the second side.
- `internal/agentrun/streamrunner/helper_test.go` → `TestStreamRunnerHelperProcess` modes `sleep` and `stall_silent`, and `blockUntilSigterm`: where the child installs its SIGTERM handler.
- `internal/agentrun/streamrunner/runner_test.go` → `helperRunCfg`, `TestRun_CtxCancelMidRun`, and its clone `TestRun_CtxCancel_ReapsDescendantGroups`, which carries the same fixed 100 ms cancel.
- `internal/agentrun/streamrunner/watchdog_test.go` → `TestRun_OperatorShutdown_NoSyntheticResult`.

## Change

The helper child writes a `helperReady` marker line to stderr once its SIGTERM handler is installed: in mode `sleep` after `signal.Notify`, and in `blockUntilSigterm` (which `stall_silent` and the other stall modes use) after `signal.Notify`. A test-side stderr sink, `readySink`, buffers what it is written under a mutex and closes its `ready` channel the first time the buffer holds the marker. The cancel tests pass the sink as `cfg.Stderr` and cancel from a goroutine that waits on `ready` (or exits on ctx done, so it never leaks), replacing `time.Sleep(100 * time.Millisecond)`. `helperRunCfg` takes `io.Writer` for stdout and stderr so the sink fits; existing `*bytes.Buffer` callers compile unchanged. All assertions stay as they are; `got SIGTERM` is read from the sink.

`TestRun_CtxCancel_ReapsDescendantGroups` is a clone of `TestRun_CtxCancelMidRun` with the same race, so it gets the same one-line swap. No production code changes.

## Testing strategy

The existing assertions of the three tests are the coverage. Proof of the fix is the acceptance command, `go test -race -count=200 -run 'TestRun_OperatorShutdown_NoSyntheticResult|TestRun_CtxCancelMidRun' ./internal/agentrun/streamrunner/`, plus the package's full `-race` run to show the stderr marker does not disturb the other helper modes.
