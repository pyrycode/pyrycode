# Ticket #2398: Attachment pairing shared fixture

## Files read

- `internal/e2e/internal/paireddevice/paireddevice.go` → `Config`, `Setup` — defines the pre-daemon credential fixture and its returned pairing payload.
- `internal/e2e/relay_v2_attachment_destination_test.go` → `TestRelayV2_AttachmentUploadOnNeverMessagedConversationResolves`, `TestRelayV2_AttachmentUploadNamesConversationNotCursor`, `TestRelayV2_AttachmentUploadUnknownConversationRefused` — contains three CLI-based setup sites to migrate without changing their destination assertions.
- `internal/e2e/relay_v2_attachment_offer_test.go` → `TestRelayV2_AttachmentOfferedRoundTrip` — contains the offered-file round-trip setup site.
- `internal/e2e/relay_v2_attachment_retrieval_test.go` → `TestRelayV2_AttachmentRetrieval` — contains the retrieval setup site.
- `internal/e2e/relay_v2_attachment_upload_test.go` → `TestRelayV2_AttachmentUploadMultiChunk` — contains the multi-chunk upload setup site.
- `internal/e2e/relay_v2_debug_bundle_test.go` → `bundleHarness` — contains the shared debug-bundle setup site.
- `docs/knowledge/features/e2e-harness.md` → “Pre-daemon paired-device setup” — establishes `paireddevice.Setup` as the fixture boundary for credentials needed before daemon startup.
- `docs/knowledge/features/development-verification.md` → “Establish the change surface” and “Test execution and artifact survival” — requires checking construction sites and proving the tagged suite actually executes.

## Change

Replace each of the seven `RunBareIn` plus `decodePairPayload` setup blocks with a direct `paireddevice.Setup` call. Each call receives the same isolated `home`, instance `test`, device name `phone-a`, remote permissions disabled, and a `relayURL` equal to the fake relay's `/v2/server` daemon destination. The fake relay therefore moves before setup where needed, while daemon startup remains after successful setup. Existing handshake, attachment, and debug-bundle behavior stays unchanged; only the credential-seeding boundary and required imports move.

## Testing strategy

- RED: a source check finds all seven public `pair` CLI setup invocations in the five named files before migration.
- GREEN: the same source check finds none after migration and finds seven `paireddevice.Setup` calls with explicit configuration.
- Run the race-enabled `e2e`-tagged tests for `internal/e2e`, then `go vet ./...` and build `cmd/pyry`.

## Documentation handoff

No documentation change is requested. The existing pre-daemon fixture contract is already recorded in `docs/knowledge/features/e2e-harness.md`.
