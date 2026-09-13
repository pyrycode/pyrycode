# Ticket #2400: Lifecycle pairing shared fixture

## Files read

- `internal/e2e/internal/paireddevice/paireddevice.go` → `Config`, `Setup` — defines the pre-daemon credential fixture and returned pairing payload.
- `internal/e2e/per_conversation_eviction_test.go` → `TestE2E_PerConversation_IdleEvictsAndReactivates`, `TestE2E_PerConversation_CapEvictsCrossDiscussion` — contains the two eviction setup sites and their shared phone dial path.
- `internal/e2e/relay_v2_daemon_test.go` → `testV2DaemonListConversationsRoundTrip`, `testV2DaemonDefaultEngagesV2` — contains the two daemon setup sites, including the sole `phone-b` case.
- `internal/e2e/relay_v2_history_test.go` → `TestRelayV2_ConversationHistory` — contains the conversation-history setup site.
- `internal/e2e/relay_v2_promote_test.go` → `testV2DaemonPromoteRoundTrip` — contains the promotion setup site.
- `internal/e2e/relay_v2_recent_workspaces_test.go` → `testV2DaemonRecentWorkspacesOrderedDeduped`, `testV2DaemonRecentWorkspacesEmptyRegistry` — contains the two recent-workspace setup sites.
- `internal/e2e/relay_v2_rename_test.go` → `testV2DaemonRenameRoundTrip`, `testV2DaemonRenameNotFound` — contains the two rename setup sites.
- `docs/knowledge/features/e2e-harness.md` → “Pre-daemon paired-device setup” — establishes `paireddevice.Setup` as the fixture boundary for credentials needed before daemon startup.
- `docs/knowledge/features/development-verification.md` → “Establish the change surface” and “Test execution and artifact survival” — requires checking every setup site and confirming the tagged suite actually executes.
- `docs/specs/architecture/2399-mutation-pairing-shared-fixture.md` → `Change`, `Testing strategy` — provides the nearest completed migration pattern for ten sibling setup sites.

## Change

Replace each of the ten `RunBareIn` plus `decodePairPayload` setup blocks with a direct `paireddevice.Setup` call. Each call receives the same isolated `home`, instance `test`, its existing device name (`phone-a` except `phone-b` in `testV2DaemonDefaultEngagesV2`), remote permissions disabled, and a `relayURL` equal to the fake relay's `/v2/server` daemon destination. The fake relay therefore moves before setup in each function, while daemon startup remains after successful setup. Existing daemon, eviction, history, promotion, recent-workspace, rename, phone-handshake, and registry assertions remain unchanged; only credential seeding, ordering, and imports move.

## Testing strategy

- RED: a source check finds all ten public `pair` CLI setup invocations before migration.
- GREEN: the same source check finds none in the six named files and finds ten `paireddevice.Setup` calls with explicit configuration.
- Run the race-enabled `e2e`-tagged tests for `internal/e2e`, then `go vet ./...` and build `cmd/pyry`.

## Documentation handoff

No documentation change is requested. The existing pre-daemon fixture contract is already recorded in `docs/knowledge/features/e2e-harness.md`.
