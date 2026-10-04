# #2751 — Exit cleanly after an operator stop signal

## Files read

- `cmd/pyry/main.go` → `runSupervisor`, `fatalCause`: signal-parent propagation, first-cause-wins shutdown, and INFO/error exit selection.
- `cmd/pyry/fatal_cause_test.go` → `TestFatalCause`: existing classifications; its synthetic parent cancellation misses the real signal cause.
- `internal/e2e/relay_test.go` → `TestRelay_OperatorStopExitsZero`, `TestRelay_4409_PersistentExitsNonZero`: daemon-level exit assertions and connected-relay setup.
- `internal/e2e/harness.go` → `StartInWithEnv`, `Harness.Done`, `Harness.ExitCode`: isolated daemon startup, bounded shutdown, and captured logs.
- `docs/knowledge/features/cli-verb-dispatch.md`: daemon errors propagate through the CLI entry point.
- `docs/knowledge/features/relay-package.md` → supervisor wiring: preserve terminal server-ID-conflict errors; current code supersedes the older cancellation description.
- `docs/knowledge/features/e2e-harness.md` → Invocation and Isolation Strategy: tagged tests run actual daemons with isolated homes and no credentials.
- `docs/knowledge/features/development-verification.md` → Prove that tests distinguish the change: exercise real signals and fatal-first ordering independently.

## Change

Pass the signal parent to `fatalCause(ctx, sigCtx)`. In addition to nil and `context.Canceled`, treat a cause inherited from that parent as an operator stop. Match the cause itself rather than checking whether the parent is cancelled: a relay fatal cause recorded before a later signal must remain fatal. Keep the existing INFO clean-stop and ERROR fatal-stop branches, shutdown ordering, and goroutine lifetimes. Correct the comments that currently describe signals as nil-cause cancellation. No concurrent feature branch touches the planned files.

Sizing: one shutdown-classification deliverable, two acceptance criteria, approximately 200 written lines including tests and this plan, no new exported types, at most eight consumers updated, and no new state-machine reject branches; all builder limits hold.

## Testing strategy

Retain the existing classification assertions, rename the synthetic parent-cancel case accurately, and add isolated test-helper subprocess cases using `signal.NotifyContext` and real SIGTERM/SIGINT. Test both operator-only cancellation and a relay conflict recorded before the signal; assert preservation of the conflict sentinel. Extend the daemon relay integration coverage with real SIGTERM and SIGINT cases that assert exit 0, an INFO `pyrycode stopped` record, and absence of `pyrycode fatal shutdown`. Run the new tests before implementation to demonstrate the signal-cause regression. Then run race tests for `cmd/pyry`, targeted tagged race tests for the daemon stop and persistent-4409 cases, `go vet ./...`, and `go build ./cmd/pyry` with its output in scratch storage. The verifier owns the full-module gate.
