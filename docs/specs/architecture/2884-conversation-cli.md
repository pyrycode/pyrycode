# #2884 — CLI conversation creation

## Files read

- `cmd/pyry/main.go` → `runArgs`, `helpText`, `runSupervisor`: dispatch, advertising, and installation independent of relay configuration.
- `cmd/pyry/main.go` → `sessionMinter.Create`, `agentModelVocabulary`, `validateModelVocabulary`, `validateEffortVocabulary`: one defaults snapshot validates and mints, before workspace side effects.
- `cmd/pyry/channel.go` → `channelCreator`, `runChannelNew`: cwd, persistence, announcement, and exit conventions; legacy channel callers remain intact.
- `cmd/pyry/channel_test.go` → `installIdentityTrustMark`, `newChannelTestRegistry`: reuse confinement and persistence fixtures.
- `cmd/pyry/create_conversation_settings_test.go` → `TestSessionMinter_Settings`: existing membership, effective-model, and empty-model proofs.
- `cmd/pyry/dormant_settings_write_test.go` → `newDormantWritePool`: a real pool with controllable model vocabulary.
- `internal/control/client.go`, `protocol.go`, `server.go` → `ConversationNew`, `ConversationPayload`, `SetConversationCreator`, `handleConversationNew`: merged control contract preserves optional settings.
- `internal/relay/handlers/create_conversation.go` → `CreateConversation`: shape validation and refusal mapping.
- `internal/e2e/channel_new_test.go`, `harness.go` → `runVerbIn`, `waitForConversation`, `StartIn`: real binary and socket under isolated HOME.
- `docs/knowledge/features/conversation-session-binding-create.md` § Requested model and effort: validate and mint from one snapshot; nil inherits and empty resets only that field.
- `docs/knowledge/features/control-plane.md` § Conversation: create: independent creator hook and static error contract.
- `docs/knowledge/features/cli-verb-dispatch.md`: preserve the supervisor fallback for unknown top-level verbs.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: refusal ordering needs a distinguishable competing workspace failure.
- `docs/knowledge/features/e2e-harness.md`: isolated subprocess environment and socket readiness.
- `CODING-STYLE.md`, `docs/protocol-mobile.md` § Security model: structured logging, errors, and trust boundaries.

## Context

Shell operators can create promoted channels but cannot prepare unnamed chats or
choose initial settings. #2883 supplies the control transport. This ticket adds
one creation behavior through that seam; no decision record is needed.
Feature #2873 also touches `main.go`, but only reply-fallback wiring in a separate
block; there is no dependency and our changes remain local.

## Design

Add `cmd/pyry/conversation.go` with a `conversationCreator` using the existing
`handlers.SessionCreator` interface. The closure accepts cwd, name, type and
optional model/effort. Validate shape with `relay.ValidModel`/`ValidEffort`, then
call `sessionMinter.Create` for Claude. It performs membership checks, confinement,
trust marking and minting once, returning the resolved cwd. Store a bound row:
chat names default to nil; channel names default to the resolved folder basename.
Persist best-effort, then announce the stored row with its workspace label when
the existing hook is present. Install it beside the legacy channel creator.

The CLI uses shared instance/socket parsing and a dedicated flag parser. Preserve
model/effort pointers through `FlagSet.Visit`; a present empty value stays present.
Default type is chat, and any other type including empty is a syntax error.
Read `os.Getwd`, call `control.ConversationNew` with a 30-second context, and print
only id plus newline. Advertise type, name, model and effort in help.

Sizing: approximately 700 written lines including plan, tests and implementation;
zero new exported types/interfaces, zero changed consumer signatures, four ACs,
at most ten creator rejection branches. Recount before committing: within all limits.

## Concurrency model

No new goroutines. Control calls run through the existing server workers; pool,
vocabulary and registry retain their own locking. No lock spans a hook invocation.
Mint defaults are composed, checked and passed to mint by the existing minter.

## Error handling

Syntax errors exit 2 with usage; cwd lookup, transport, settings and workspace
failures exit 1 with no stdout. Creator refusals use static input-free messages.
Distinguish unavailable model vocabulary from an offered-list absence. Log generic
settings refusals without values. Workspace/mint errors stay daemon-side. Save
failure is logged but creation succeeds. No row or announcement on mint refusal.

## Testing strategy

Write tests first: parser omission/empty and syntax tables; creator type/name,
resolved cwd, eager save, read-back announcement, best-effort save and error cases.
Reuse the real minter with a pool to prove accepted settings and refusal before
trust marking/directory creation, including a competing invalid cwd. Extend the
existing minter table for empty effort and both empty fields. Fake-daemon e2e
covers real dispatch/socket wiring without relay/client, both types, settings,
selectors, id-only output, syntax/runtime exit codes, and help. Run scoped race
checks for `cmd/pyry` and tagged e2e, `go vet ./...`, and build the binary into scratch.
The verifier owns the full-module gate.

## Open questions

None.

## Documentation handoff

- Pending documentation stage: `README.md`, beside the paragraph beginning
  `pyry channel new, run inside a project folder`: document `pyry conversation new`,
  default chat/type options, cwd/name defaults, model/effort validation and reset
  semantics, id output, and a shell example.
- Pending documentation stage: `docs/knowledge/features/control-plane.md`,
  `Conversation: create (conversation.new)`: production wiring and creation without
  a configured relay.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `conversationCreator` validates type/cwd and settings shape; `sessionMinter.Create` checks retained vocabulary before `resolveSpawnDir` confines and trust-marks. CLI validation alone cannot bypass daemon checks.
- [Tokens, secrets, credentials] No credential inputs or changes. Names/settings never enter logs; UUID generation reuses `conversations.NewID` and pool minting.
- [File operations] Reuse the existing confined realpath and atomic registry saves; no raw caller cwd is stored. Existing deferred-spawn symlink-swap risk remains the accepted local-home threat documented in the binding overview.
- [Subprocesses] Mint only, without activation. Values use existing shape/membership checks and existing runner argument construction; no shell evaluation is introduced.
- [Cryptography] Reuse crypto-random conversation/session ids; no new primitive or key lifecycle.
- [Network and I/O] Reuse the Unix socket server's bounded request reader and deadlines; client context bounds transport. No new remote endpoint.
- [Errors, logs, telemetry] Static refusal messages distinguish settings classes without values. Wrapped filesystem errors remain daemon-side; announcement uses a registry read-back.
- [Concurrency] One minter settings snapshot avoids validation/mint drift. Existing synchronized registry/pool operations remain; no added goroutines or lock order.
- [Threat model] This local control verb prepares Claude sessions without messages or permission escalation. Relay authentication, credentials, and protocol security remain at existing boundaries.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-06

## Revisions

- 2026-10-06 — Verifier finding 1: `parseClientFlags` can consume a following
  flag as a selector value before conversation option validation. `runConversation`
  now checks only leading selector tokens, skipping explicit or separate values,
  and rejects a dash-prefixed separate value with usage and exit 2 before cwd
  lookup or transport. Dash-prefixed values remain available through `=value`;
  shared parsing for other verbs is unchanged. Hermetic regression cases cover
  both reported commands, both dash prefixes, later selectors and verb flags.
  Successful creation checks preserve separate/inline values, empty instance
  names and explicit socket precedence. Security review remains PASS: the new
  guard introduces no I/O, credentials, goroutines or daemon trust-boundary changes.
