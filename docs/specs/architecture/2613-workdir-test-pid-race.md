# #2613 — TestSetSpawnWorkDir_DoesNotMoveLiveChild reads ChildPID before it is published

## Files read

- `internal/streamsup/runner_workdir_test.go` → `TestSetSpawnWorkDir_DoesNotMoveLiveChild` — the test that records `pidBefore` straight after `waitForMarker`.
- `internal/streamsup/runner.go` → `spawnAndWait` — publishes `ChildPID` through `updateState` only after `cmd.Start`, posture-gate arming and `setStdin`; the helper child writes its cwd marker as soon as it starts, so the marker can land first.
- `internal/streamsup/interface_test.go` → `waitForState` — existing polling helper that returns the matching `State`.

## Change

In the test, replace the bare `r.State().ChildPID` read after `waitForMarker` with `waitForState(t, r, func(st State) bool { return st.ChildPID != 0 }, 5*time.Second).ChildPID`. `pidBefore` is then the published pid of the live child, never the pre-publish zero, and the later comparison only fails on a real restart. Test-only; no production file moves. No in-flight branch touches the test file.

## Testing strategy

The test itself is the proof. Demonstrate the race by temporarily inserting a `time.Sleep` just before the `updateState` that sets `ChildPID` in `spawnAndWait` (not committed): the unfixed test must fail every run, the fixed test pass. Then `go test -race -count=30 -run TestSetSpawnWorkDir_DoesNotMoveLiveChild ./internal/streamsup/` on the committed tree.

## Documentation handoff

None.
