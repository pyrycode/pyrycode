# Ticket #2408: Finish live pairing fixture migration

## Files read

- `internal/e2e/internal/paireddevice/paireddevice.go` → `Config`, `Setup` — defines the pre-daemon credential fixture and returned pairing payload.
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` → `startObservedPermissionHarness` — seeds the permission-capable phone shared by observed permission tests.
- `internal/e2e/realclaude/interactive_stream_multiturn_continuity_test.go` → `TestInteractiveStreamMultiTurnContinuity` — seeds the phone for the held-open multi-turn flow.
- `internal/e2e/realclaude/interactive_stream_new_session_test.go` → `TestInteractiveStreamNewSessionRotatesAndSpawnsFresh` — seeds the phone for the live session-rotation flow.
- `internal/e2e/realclaude/interactive_stream_resume_after_eviction_test.go` → `TestInteractiveStreamResumeAfterEviction` — seeds the phone for the eviction-and-resume flow.
- `internal/e2e/realclaude/interactive_stream_running_turn_test.go` → `startStreamRunningTurnHarness` — seeds the phone shared by running-turn tests.
- `internal/e2e/realclaude/interactive_stream_skill_test.go` → `TestInteractiveStreamSkillInvocationIsSilent` — seeds the phone for the skill-invocation flow.
- `internal/e2e/realclaude/interactive_stream_unrecognized_test.go` → `TestInteractiveStreamNoUnrecognizedOnToolTurn` — seeds the phone for the tool-turn census.
- `internal/e2e/realclaude/harness_daemon_test.go` → `decodePairPayload` — owns the decoder that becomes unused after the seven migrations.
- `docs/knowledge/features/e2e-harness.md` → “Pre-daemon paired-device setup” — establishes `paireddevice.Setup` as the fixture boundary for authenticated phones created before daemon startup.
- `docs/knowledge/features/e2e-realclaude.md` → “Test infrastructure” and “Make target” — describes the tagged live suite and its execution boundary.
- `docs/knowledge/features/development-verification.md` → “Establish the change surface” and “Test execution and artifact survival” — requires checking every setup site and distinguishing compilation from a vacuous live run.

## Change

Replace the seven remaining pre-daemon `runPyry` pairing blocks with `paireddevice.Setup`. Each site will create its fake relay before setup, use one `relayURL` ending in `/v2/server` for both the fixture and daemon, and configure the isolated home, instance `test`, and device name `phone-a`. `startObservedPermissionHarness` will enable remote permissions; the other six sites will disable them. The returned token and server public key continue through the existing fake-phone handshake, all live behavior assertions stay unchanged, and `decodePairPayload` plus imports used only by it are removed after its final caller disappears.

## Testing strategy

- RED: a source check confirms the seven named sites still invoke the public `pair` CLI and therefore fail the migration criterion.
- GREEN: the same check finds no public pairing invocation in those files, exactly one `paireddevice.Setup` call at each site, and no remaining realclaude caller or definition of `decodePairPayload`.
- Compile the tagged package without depending on live credentials, then run `go vet ./...` and build `cmd/pyry`. The dispatcher-owned `make e2e-realclaude` gate must report executed tests covering the migrated sites and preserve their live assertions.

## Documentation handoff

No documentation change is requested. The existing live-suite and shared-fixture documentation already describes this test-only migration.
