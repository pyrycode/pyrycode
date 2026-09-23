# #2555 — drop the unreachable PTY-mode arm from the relay-wiring and RunnerFactory comments

Short plan: comments only, no new type, state or failure mode.

## Files read

- `cmd/pyry/main.go` → `selectInteractiveRunner`, `selectsStreamRunner` — the ground truth: `""` and `"stream-json"` return a factory and a sink, `"pty"` and anything else fail startup. Their docs are correct and stay.
- `cmd/pyry/main.go` → `runSupervisor` (the `mcpServersPath`, runner-selection, turn-busy tracker and `approvalParked` comments), the `reset` / `resetting` fields of `activeSessionStarter`, `activeSessionStarter.start`, `inboundActivateTimeout`, `streamTurnHoldTimeout`, `approvalParkedReport.parked`, `newInboundDeliver`, `runSessionsRm` — each carries a "PTY mode" / "PTY posture" / `runAttach` aside.
- `cmd/pyry/relay.go` → `relayWiring.streamSink`, `relayWiring.busy`, `boundSessionIDForActive`, `startRelayV2` (question resolver, MCP actuator, context-usage recorder, bridge `toolCallInFlight` guard, `ApprovalAnswerable`, turn drain, session-transition / queue_state / session_error producers).
- `cmd/pyry/snapshot_usage.go` → the by-id resolution doc citing the deleted `interactive_turn_stream_v2.go` (and the equally deleted `resolveBootstrapJSONL`).
- `internal/relay/v2session_seams.go` → `Interrupter` doc (`w.sup` wiring), `LateSessionStarter` doc ("the PTY posture").
- `internal/sessions/pool.go` → `Config.RunnerFactory`, `Pool.newRunner`, `New` — docs still promise a `supervisor.New` default; `New` errors on a nil factory.
- `internal/sessions/runner.go` → `RunnerFactory` — already corrected; tone to match.

## Change

Rewrite each comment so it describes only reachable configuration. After a successful start `streamSink` and the turn-busy tracker are never nil, so every "nil in PTY mode" becomes "nil only in a wiring with no sink (test literals), unreachable from the composition root since #1348". "foreground / PTY" pairs keep the foreground half. `Config.RunnerFactory`, `Pool.newRunner` and the normalisation comment in `New` say the factory is required and a nil one makes `New` return an error; the stale lead-in in `New` is dropped rather than contradicted. The exit-2 doc on `runSessionsRm` stops citing `runAttach`. The `activeInterrupter` / `activeSessionStarter` docs in `main.go` that name "the former … w.sup wiring" already read as history and stay. No nil guard is removed. `Pool.newRunner`'s field doc is not in the ticket's list but makes the same dead `supervisor.New` claim in the same file, so it is corrected with the other two.

## Testing strategy

No new logic, so no new test. `go build`, `go vet ./...` and `go test -race` over `./cmd/pyry/...`, `./internal/relay/...`, `./internal/sessions/...` prove nothing but comments moved; rerunning the ticket's `rg` commands and reading each hit is the AC check.

## Documentation handoff

None — the ticket carries no documentation requirement.

## Revisions

- 2026-09-23, during the build: `New`'s own doc comment also listed `supervisor.New` among its failure sources. It now names the nil-factory refusal and the runner factory instead, the same dead claim fixed in the same file as `Pool.newRunner`. In `snapshot_usage.go` the deleted `resolveBootstrapJSONL` citation went with the `interactive_turn_stream_v2.go` one, since both sat in the same parenthetical.
