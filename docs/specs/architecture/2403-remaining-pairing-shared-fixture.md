# Ticket #2403: Remaining pairing shared fixture

## Files read

- `internal/e2e/internal/paireddevice/paireddevice.go` → `Config`, `Setup` — defines the pre-daemon credential fixture and returned pairing payload.
- `internal/e2e/register_push_token_test.go` → `TestRelay_RegisterPushToken_AckAndPersists` — contains one `phone-a` setup site whose push-token and registry assertions stay intact.
- `internal/e2e/relay_v2_mcp_status_request_test.go` → `TestRelayV2_MCPStatusRequestQueriesLiveChildRequesterOnly` — contains `phone-a` and `phone-b` setup sites for the requester-only MCP-status proof.
- `internal/e2e/relay_v2_mint_pairing_test.go` → `TestRelayV2_MintPairing` — contains privileged `operator-laptop` and unprivileged `watcher-phone` setup sites; its post-start mint, list, and revoke CLI calls remain intentional.
- `internal/e2e/relay_v2_stream_slash_command_list_test.go` → `driveSlashCommandListRespawn` — contains one `phone-a` setup site for the slash-command respawn proof.
- `internal/e2e/relay_v2_stream_unrecognized_test.go` → `TestRelayV2_StreamUnrecognizedMessageReachesPhone` — contains one `phone-a` setup site for the unrecognized-frame proof.
- `docs/knowledge/features/e2e-harness.md` → “Pre-daemon paired-device setup” — establishes `paireddevice.Setup` as the fixture boundary for credentials required before daemon startup.
- `docs/knowledge/features/development-verification.md` → “Establish the change surface” and “Test execution and artifact survival” — requires enumerating setup consumers and proving the tagged suite executes.

## Change

Replace the seven pre-daemon `RunBareIn` plus `decodePairPayload` setup blocks with direct `paireddevice.Setup` calls. Each call receives the isolated `home`, instance `test`, and a relay destination equal to its fake relay's `/v2/server` daemon endpoint. Preserve device labels and privileges exactly: `phone-a`, `phone-b`, and `watcher-phone` remain unprivileged, while `operator-laptop` keeps remote permissions. Where necessary, create the fake relay before seeding credentials but still start the daemon only after setup succeeds. Existing protocol, handshake, registry, and push-token assertions remain unchanged, as do the post-start pairing mint, list, and revoke CLI calls in `TestRelayV2_MintPairing`.

## Testing strategy

- RED: a source check finds exactly seven pre-daemon public `pair` CLI setup invocations in the five named files.
- GREEN: the same source check finds none of those seven invocations, finds seven `paireddevice.Setup` calls, and still finds the intentional post-start `pair`, `pair list`, and `pair revoke` invocations in `TestRelayV2_MintPairing`.
- Run the race-enabled `e2e`-tagged tests for `internal/e2e`, then `go vet ./...` and build `cmd/pyry`.

## Documentation handoff

No documentation change is requested. The existing pre-daemon fixture contract is already recorded in `docs/knowledge/features/e2e-harness.md`.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `paireddevice.Setup` remains the single established boundary that creates trusted pre-daemon fixture credentials; downstream handshakes continue consuming its typed `pair.Payload`.
- [Tokens, secrets, credentials] No findings — `Setup` generates tokens with `crypto/rand`, persists only their hashes through `devices.Registry.Save`, and returns plaintext only after a successful save. The migration removes CLI stdout as an intermediate credential carrier and adds no logging.
- [File operations] No findings — the calls supply the absolute isolated home returned by `shortHome`, fixed instance `test`, and established registry persistence through `Setup`; no caller-controlled path or new file operation is introduced.
- [Subprocess / external command execution] No findings — the migrated setup removes seven subprocess executions. The retained post-start CLI calls in `TestRelayV2_MintPairing` use test-owned fixed arguments and are the behavior under test.
- [Cryptographic primitives] No findings — the fixture reuses the established instance key and identity and relies on `crypto/rand` for fresh tokens; the tests keep their existing Noise handshake proof.
- [Network & I/O] No findings — credential setup performs no network I/O. Fake relay and daemon behavior, bounded waits, and protocol assertions are unchanged.
- [Error messages, logs, telemetry] No findings — setup failures are reported without payload contents, and the existing minted-credential no-log assertion remains unchanged.
- [Concurrency] No findings — all setup calls remain sequential before daemon startup, matching `Setup`'s contract. No goroutine, lock, or shutdown behavior changes.
- [Threat model alignment] No findings — this test-only migration preserves the established relay authentication, per-device privilege, token-hash persistence, and Noise handshake boundaries. It changes only how hermetic prerequisites are seeded.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-13
