# Ticket #2401: Stream-state pairing shared fixture

## Files read

- `internal/e2e/internal/paireddevice/paireddevice.go` → `Config`, `Setup` — defines the pre-daemon credential fixture and returned pairing payload.
- `internal/e2e/relay_v2_stream_model_list_reconcile_test.go` → `driveLateConnectModelList` — contains the two-device model-list reconcile setup, including the only `phone-b` credential.
- `internal/e2e/relay_v2_stream_model_list_test.go` → `driveModelListRespawn` — contains the connected-phone model-list setup.
- `internal/e2e/relay_v2_stream_model_rejection_test.go` → `TestRelayV2_StreamRejectsModelAbsentFromPublishedMenu` — contains the rejected-model setup.
- `internal/e2e/relay_v2_stream_model_window_test.go` → `TestRelayV2_StreamSessionSettingsReportsTheObservedWindow` — contains the observed-window setup.
- `internal/e2e/relay_v2_stream_new_session_named_test.go` → `TestRelayV2_StreamNewSessionNamedConversationRotatesThatOne` — contains the named new-session setup.
- `internal/e2e/relay_v2_stream_new_session_test.go` → `TestRelayV2_StreamNewSessionRotatesAndRestartsFresh` — contains the current-conversation new-session setup.
- `internal/e2e/relay_v2_stream_rate_limit_test.go` → `driveRateLimitTurn` — contains the shared rate-limit setup.
- `internal/e2e/relay_v2_stream_run_config_test.go` → `TestRelayV2_StreamRequestSessionSettings` — contains the run-configuration setup.
- `internal/e2e/relay_v2_stream_session_facts_test.go` → `driveSessionFactsTurn` — contains the shared session-facts setup.
- `docs/knowledge/features/e2e-harness.md` → “Pre-daemon paired-device setup” — establishes `paireddevice.Setup` as the fixture boundary for credentials needed before daemon startup.
- `docs/knowledge/features/development-verification.md` → “Establish the change surface” and “Test execution and artifact survival” — requires checking every setup site and confirming the tagged suite actually executes.
- `docs/specs/architecture/2400-lifecycle-pairing-shared-fixture.md` → `Change`, `Testing strategy` — provides the nearest completed migration pattern for ten sibling setup sites.

## Change

Replace each of the ten `RunBareIn` plus `decodePairPayload` setup blocks with a direct `paireddevice.Setup` call. Each call receives the same isolated `home`, instance `test`, its existing device name (`phone-a`, plus `phone-b` for the second credential in `driveLateConnectModelList`), remote permissions disabled, and a relay destination equal to the fake relay's `/v2/server` daemon destination. The fake relay therefore moves before setup in each function, while daemon startup remains after successful setup. Existing model-list, model-rejection, model-window, new-session, rate-limit, run-configuration, session-facts, phone-handshake, and registry assertions remain unchanged; only credential seeding, ordering, imports, and setup-oriented comments move.

## Testing strategy

- RED: a source check finds all ten public `pair` CLI setup invocations before migration.
- GREEN: the same source check finds none in the nine named files and finds ten `paireddevice.Setup` calls with explicit configuration.
- Run the race-enabled `e2e`-tagged tests for `internal/e2e`, then `go vet ./...` and build `cmd/pyry`.

## Documentation handoff

No documentation change is requested. The pre-daemon fixture contract is already recorded in `docs/knowledge/features/e2e-harness.md` under “Pre-daemon paired-device setup”.
