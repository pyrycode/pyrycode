# Ticket #2402: Stream-control pairing shared fixture

## Files read

- `internal/e2e/internal/paireddevice/paireddevice.go` → `Config`, `Setup` — defines the pre-daemon credential fixture, its security properties, and returned pairing payload.
- `internal/e2e/relay_v2_stream_announced_reset_test.go` → `TestRelayV2_StreamAnnouncedResetFollowsClaude` — contains one phone setup for announced-reset coverage.
- `internal/e2e/relay_v2_stream_background_task_roster_reconcile_test.go` → `driveLateConnectBackgroundTaskRoster` — contains the three-device setup loop for late roster reconciliation.
- `internal/e2e/relay_v2_stream_interrupt_test.go` → `TestRelayV2_StreamInterruptStopsRunningTurn`, `TestRelayV2_StreamInterruptNamedConversationStopsThatOne` — contains two interrupt setup sites.
- `internal/e2e/relay_v2_stream_mcp_status_test.go` → `TestRelayV2_StreamMCPStatusReachesConnectedPhone` — contains one phone setup for MCP status delivery.
- `internal/e2e/relay_v2_stream_modal_test.go` → `TestRelayV2_StreamModalPermissionRoundTrip` — contains the remote-permission-enabled setup used by its table cases.
- `internal/e2e/relay_v2_stream_posture_gate_test.go` → `TestRelayV2_StreamTurnHeldUntilPostureAck` — contains one phone setup for the posture gate.
- `internal/e2e/relay_v2_stream_queue_drain_test.go` → `TestRelayV2_StreamMidTurnHoldDropAndDrainInOrder` — contains one phone setup for queue-drain coverage.
- `internal/e2e/relay_v2_stream_send_test.go` → `TestRelayV2_StreamSendMessageDrainsTurn` — contains one phone setup for stream-send coverage.
- `internal/e2e/relay_v2_stream_slash_command_list_reconcile_test.go` → `driveLateConnectSlashCommandList` — contains the three-device setup loop for late slash-command reconciliation.
- `docs/knowledge/features/e2e-harness.md` → “Pre-daemon paired-device setup” — establishes `paireddevice.Setup` as the fixture boundary for credentials needed before daemon startup.
- `docs/knowledge/features/development-verification.md` → “Establish the change surface” and “Test execution and artifact survival” — requires checking every setup site and confirming the tagged suite actually executes.
- `docs/protocol-mobile.md` → “Security model” — defines the credential, static-key, relay, and handshake threats that the migrated setup must preserve.
- `docs/specs/architecture/2401-stream-state-pairing-shared-fixture.md` → `Change`, `Testing strategy` — provides the nearest completed migration pattern for ten sibling setup sites.

## Change

Replace each of the ten `RunBareIn` plus `decodePairPayload` setup blocks with a direct `paireddevice.Setup` call. Each call receives the same isolated `home`, instance `test`, existing device name (`phone-a`; `phone-empty`, `phone-a`, and `phone-b` in each reconcile loop), existing remote-permission choice (enabled only in `TestRelayV2_StreamModalPermissionRoundTrip`), and a relay destination equal to the fake relay's `/v2/server` daemon destination. The fake relay therefore moves before setup in each function or table case, while daemon startup remains after successful setup. Existing control-flow, phone-handshake, registry, and protocol assertions remain unchanged in meaning; only credential seeding, ordering, imports, setup-oriented comments, and setup-only failure messages move.

## Testing strategy

- RED: a source check finds all ten public `pair` CLI setup invocations in the nine named files before migration.
- GREEN: the same source check finds none in those files and finds ten `paireddevice.Setup` call sites with explicit configuration.
- Run the race-enabled `e2e`-tagged tests for `internal/e2e`, then `go vet ./...` and build `cmd/pyry`.

## Documentation handoff

No documentation change is requested. The pre-daemon fixture contract is already recorded in `docs/knowledge/features/e2e-harness.md` under “Pre-daemon paired-device setup”.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `paireddevice.Setup` remains the single offline fixture boundary, and the existing phone handshake continues to authenticate its returned payload through unchanged production code.
- [Tokens, secrets, credentials] No findings — `Setup` generates tokens with `crypto/rand`, persists only the token hash, returns plaintext only after `devices.Registry.Save` succeeds, and the migrated failure paths report the error without printing the payload or token.
- [File operations] No findings — every call supplies a `shortHome` absolute temporary home and fixed `test` instance; the migration reuses `Setup` and the existing registry/key writers rather than adding path construction or file operations.
- [Subprocess execution] No findings — the change removes the public pairing CLI subprocess from credential seeding and introduces no replacement command execution; daemon subprocess setup is unchanged.
- [Cryptographic primitives] No findings — credential generation and static-key reuse stay inside `Setup`, which uses `crypto/rand` and the existing `keys.LoadOrCreate` contract; no primitive, key, or nonce handling changes.
- [Network and I/O] No findings — setup stays offline, while every payload records the exact fake-relay `/v2/server` destination later passed to the daemon; socket reads, limits, deadlines, and relay transport are unchanged.
- [Error messages, logs, telemetry] No findings — setup failures include only `Setup`'s contextual error, and existing test diagnostics continue to avoid emitting tokens or decoded pairing payloads.
- [Concurrency] No findings — calls remain sequential and complete before daemon startup, matching `Setup`'s sequential-repeat contract; no goroutine, lock, or shutdown behavior changes.
- [Threat model alignment] No findings — the migration preserves independent random per-device tokens, the shared per-instance static key, device names, remote-permission policy, and Noise handshake inputs. Prompt injection, relay metadata exposure, denial of service, static-key rotation, and other deferred protocol threats are unaffected because no production protocol path changes.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-13
