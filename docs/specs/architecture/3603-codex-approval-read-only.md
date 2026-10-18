# #3603: Read-only posture for live Codex approval turns

This is a retrospective design and security record for the existing test repair.
The chronology and missed planning requirement are recorded under Revisions.

## Files read

- `internal/e2e/realclaude/codex_conversation_live_test.go` → `TestCodexConversationLive`, `startCodexLiveConversation`, `runCodexTurn`, `requireCodexCapture`, `startCodexConversationHarness`: settings sequencing, modal/file assertions, dedicated credential home and cleanup.
- `internal/e2e/realclaude/interactive_stream_running_permission_settings_test.go` → `requestSessionSettings`: correlated protocol read-back for the conversation's session.
- `internal/sessions/session.go` → `canonicalPermissionMode`, `canonicalSettings`: an unset mode becomes `default` before the runner reads it.
- `cmd/pyry/codex_settings.go` → `codexTurnOverrides`: `plan` maps to granular approval/read-only/user review; `default` maps to workspace-write.
- `docs/knowledge/features/e2e-realclaude-codex-conversation-live-test-go.md` → opening scenario and “Lessons that outlive this ticket”: canonicalization and shared-sign-in lessons determine the required explicit posture and serialized live check.
- `docs/knowledge/features/e2e-realclaude.md` → build tags and source-citation checks: offline tagged checks do not prove live execution.
- `docs/knowledge/features/development-verification.md` → inherited premises and change surface: verify the actual production mapping before repairing a test assumption.
- Issue #3602 and its two original #3589 gate reports → `TestCodexConversationLive` failed on branch `bb1a3848` and base `fef0f53f` with the declined file present and no modal; the failure is observed, rather than only predicted by static inspection.
- PR #3605 verifier review of `57fea4db` → finding 1: the existing code is correct, but its design/security record and planning chronology are missing.

## Change

After the existing plain streamed turn, `TestCodexConversationLive` sends
`set_session_settings` with `permission_mode: plan` for its current session,
waits for the correlated update acknowledgement, then reads settings through
`requestSessionSettings` and requires both the exact session ID and stored
`plan` mode before either write turn. Each request advances the existing request
counter. Both writes must still raise real permission modals: rejecting leaves
the declined file absent and accepting creates the accepted file.
`runCodexTurn` retains remote dismissal attribution and handles repeated modals.
The explicit model/low effort, multi-agent client, credential-home isolation,
existing skip conditions and production permission mapping remain intact.
No further implementation is needed for this rework.

This is one behaviour: establishing the supported read-only precondition for the
existing approval round trip. The repair and its live regression belong together.
Sizing recheck: 32 implementation lines added/deleted plus about 100 record lines,
0 new exported types/interfaces, 0 consumer signature changes, 3 acceptance
criteria and 1 new combined rejection guard (wrong session or mode). All five
ceilings hold. The feature-branch overlap scan found no other branch touching
the test or this record.

## Testing strategy

- Preserve the original #3589 branch/base failures as the observed regression evidence; this record does not turn them into passes.
- On macOS, run affected tagged-package offline race checks for `TestSourceCitation*` and `TestCodexVersionBelow`, plus tagged-package vet. These compile the changed test without using credentials or claiming live acceptance.
- Run the role-owned `go vet ./...` and build `./cmd/pyry` with output outside the worktree. Complete race and fake-daemon gates remain verifier-owned.
- Pending dispatcher-owned macOS acceptance: run `TestCodexConversationLive` in its isolated daemon harness with race instrumentation, retaining per-test executed/passed/failed/skipped counts; require a nonzero executed count with zero failures/skips. This proves the plain turn, settings transition on the same session, rejection, acceptance, repeated modal handling and remote dismissal attribution.
- Serialize the dedicated Codex sign-in across `TestCodexConversationLive`, `TestCaptureLive` and `TestCodexApprovalLive`. The existing harness owns subprocess/network cleanup; the repair creates no goroutine, lock, identifier reuse or shutdown path.
- Linux is outside this ticket's verification scope. No builder live run is required because final live proof is explicitly assigned to the dispatcher.

## Documentation handoff

Pending documentation stage:

- Update `docs/knowledge/features/e2e-realclaude-codex-conversation-live-test-go.md`, opening scenario, to say the approval scenario explicitly confirms `plan`/read-only before its write turns; unset still canonicalizes to `default`/workspace-write.
- Update the same file's “Lessons that outlive this ticket” with that explicit confirmation and canonicalization contract, reconcile the static-only posture discussion with the original #3589 branch/base failures, and preserve the shared-sign-in serialization guidance.

## Revisions

- 2026-10-18, verifier finding 1: implementation commit `c562b2fb` was committed at 09:59:40 UTC before any #3603 plan or required security review was committed. Reviewed head `57fea4db` also lacked both records. This missed the builder's plan-before-code and `security-sensitive` review requirements. This document is written retrospectively during rework, after those commits and the verifier FAIL; it is not a pre-implementation plan. No history is rewritten and no additional implementation precedes this record. The existing implementation contract is retained; the corrective work supplies the missing review and chronology before further implementation.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The permission selection uses the existing authenticated client protocol; `TestCodexConversationLive` requires a correlated acknowledgement and an exact-session settings read-back before requesting writes. `runCodexTurn` still requires real permission options and checks observed dismissal attribution as remote.
- [Tokens, secrets, credentials] No findings. `requireCodexCapture` resolves the dedicated capture home and rejects an operator `~/.pyry` descendant before HOME pinning. The existing harness links that dedicated home rather than copying renewal credentials. The new settings request carries only the session ID and literal `plan`; no credential is read or logged by the repair. Live execution must retain shared-sign-in serialization.
- [File operations] No findings. Write targets are test-generated nonce filenames under the isolated workspace, checked with `os.Stat`. The repair adds no path input, secret file, persistent format or atomic-write requirement; credential-home symlink validation remains in `requireCodexCapture`.
- [Subprocesses] No findings. No process construction or environment inheritance changes. `touchPrompt` receives test-generated filenames, and the write runs through the existing Codex approval path. Existing harness cleanup still stops the daemon and closes the phone/relay.
- [Cryptography] No findings. Existing Noise transport and paired-device setup are reused; the repair adds no key material, random security value, comparison or cryptographic primitive. The filename nonce is only test uniqueness.
- [Network and I/O] No findings. Existing fake relay and authenticated phone transport are reused. The added settings acknowledgement uses `codexSettingsBudget`; `requestSessionSettings` has its existing bounded reply wait. No listener, framing limit or connection policy changes.
- [Errors, logs, telemetry] No findings. A mismatched session or posture fails before write prompts. New diagnostics contain only test session/mode values. Missing modals and incorrect file outcomes remain fatal; no assertion is weakened and no skip is added.
- [Concurrency] No findings. The single test sequences settings acknowledgement, read-back and turns with increasing request IDs. It adds no goroutine, shared mutable state or lock. The dedicated credential home must not be exercised concurrently by other live tests.
- [Threat model] No findings. This test checks user approval across the existing daemon/client boundary in an isolated authenticated harness. It explicitly tightens to read-only for the approval scenario and preserves production authorization semantics; no new production capability or deferred security repair is introduced.

**Reviewer:** builder (self-review per the security-review checklist)

**Date:** 2026-10-18
