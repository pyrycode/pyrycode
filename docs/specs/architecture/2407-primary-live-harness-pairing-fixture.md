# Ticket #2407: Primary live harness pairing fixture

## Files read

- `internal/e2e/internal/paireddevice/paireddevice.go` → `Config`, `Setup` — defines the pre-daemon credential fixture and returned pairing payload.
- `internal/e2e/realclaude/harness_modal_test.go` → `startModalResolutionHarness` — seeds the permission-capable phone used by the shared modal harness.
- `internal/e2e/realclaude/interactive_change_workspace_test.go` → `TestInteractiveChangeWorkspace` — seeds the phone used by the live workspace-change flow.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` → `startPerConversationHarnessSeeded` — seeds the phone shared by the per-conversation live harnesses.
- `internal/e2e/realclaude/interactive_stream_announced_reset_test.go` → `TestInteractiveStreamAnnouncedResetFollowsLiveClaude` — seeds the phone used by the announced-reset flow.
- `internal/e2e/realclaude/interactive_stream_attachment_read_test.go` → `startAttachmentReadHarness` — seeds the phone used by the attachment-read flow.
- `internal/e2e/realclaude/interactive_stream_liveness_test.go` → `TestInteractiveStreamLiveness` — seeds the phone used by the stream liveness flow.
- `internal/e2e/realclaude/interactive_stream_model_announced_test.go` → `TestInteractiveStreamModelAnnouncedFrame` — seeds the phone used by the model-announcement flow.
- `docs/knowledge/features/e2e-realclaude.md` → “Test infrastructure” and “Make target” — describes the tagged live suite and its execution boundary.
- `docs/knowledge/features/development-verification.md` → “Establish the change surface” and “Test execution and artifact survival” — requires checking every setup site and distinguishing compilation from a vacuous live run.

## Change

Replace each of the seven pre-daemon `runPyry` pairing blocks with `paireddevice.Setup`. Each site will create its fake relay before setup, pass the same `fr.URL()+"/v2/server"` destination to both the fixture and daemon, and configure the isolated home, instance `test`, and device name `phone-a`. `startModalResolutionHarness` will set remote permissions true; the other six will leave them false. The existing payload token, decoded server public key, fake-phone authentication, daemon lifecycle, and all live behavior assertions remain unchanged. Imports move only as required by the substituted setup.

## Testing strategy

- RED: a source assertion rejects the seven named files while they still contain public `pair` CLI invocations.
- GREEN: the source assertion finds no public pairing invocation in those files and confirms one `paireddevice.Setup` call at each named site.
- Compile the tagged package without executing credentialed live tests, then run `go vet ./...` and build `cmd/pyry`. The dispatcher-owned `make e2e-realclaude` gate must report executed tests and validate the unchanged live assertions.

## Documentation handoff

No documentation change is requested. The existing live-suite and shared-fixture contracts already cover this test-only migration.
