# Ticket #2399: Mutation pairing shared fixture

## Files read

- `internal/e2e/internal/paireddevice/paireddevice.go` → `Config`, `Setup` — defines the pre-daemon credential fixture and returned pairing payload.
- `internal/e2e/relay_v2_archive_test.go` → `testV2DaemonArchiveRoundTrip`, `testV2DaemonArchiveNotFound` — contains the two archive setup sites to migrate without changing mutation or registry assertions.
- `internal/e2e/relay_v2_change_workspace_test.go` → `testV2DaemonChangeWorkspaceRoundTrip`, `testV2DaemonChangeWorkspaceRejectedNoLeak`, `testV2DaemonChangeWorkspaceNotFound` — contains the three workspace-change setup sites.
- `internal/e2e/relay_v2_create_workspace_folder_test.go` → `testV2DaemonCreateWorkspaceFolderRoundTrip`, `testV2DaemonCreateWorkspaceFolderRejectedNoLeak`, `testV2DaemonCreateWorkspaceFolderBadName` — contains the three folder-creation setup sites.
- `internal/e2e/relay_v2_delete_test.go` → `testV2DaemonDeleteRoundTrip`, `testV2DaemonDeleteNotFound` — contains the two delete setup sites.
- `docs/knowledge/features/e2e-harness.md` → “Pre-daemon paired-device setup” — establishes `paireddevice.Setup` as the fixture boundary for credentials needed before daemon startup.
- `docs/knowledge/features/development-verification.md` → “Establish the change surface” and “Test execution and artifact survival” — requires checking all setup sites and proving the tagged suite actually executes.

## Change

Replace each of the ten `RunBareIn` plus `decodePairPayload` setup blocks with a direct `paireddevice.Setup` call. Each call receives the same isolated `home`, instance `test`, device name `phone-a`, remote permissions disabled, and a `relayURL` equal to the fake relay's `/v2/server` daemon destination. The fake relay therefore moves before setup in each function, while daemon startup remains after successful setup. Existing archive, workspace-change, folder-creation, delete, phone-handshake, and registry assertions remain unchanged; only credential seeding, ordering, and imports move.

## Testing strategy

- RED: a source check finds all ten public `pair` CLI setup invocations before migration.
- GREEN: the same source check finds none in the four named files and finds ten `paireddevice.Setup` calls with explicit configuration.
- Run the race-enabled `e2e`-tagged tests for `internal/e2e`, then `go vet ./...` and build `cmd/pyry`.

## Documentation handoff

No documentation change is requested. The existing pre-daemon fixture contract is already recorded in `docs/knowledge/features/e2e-harness.md`.
