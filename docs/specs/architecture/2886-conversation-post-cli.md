# #2886 — CLI user turns by conversation id

## Files read

- `cmd/pyry/conversation.go` → `runConversation`, `conversationCreator`: existing creation and selector parsing.
- `cmd/pyry/main.go` → `runSupervisor`, `sessionRouter.resolve`, `newInboundDeliver`, `helpText`: shared queue wiring, guarded binding/revival, activation and help.
- `cmd/pyry/channel.go` → `channelPostContent`: single-open, cap-plus-one bounded content reader.
- `internal/control/conversation_post_test.go` → existing contract tests: validation, one callback, silent acceptance and transport/refusal behavior already covered.
- `internal/relay/handlers/send_message.go` → `SendMessage`, `touchConversation`: binding validation before queue insertion, last-used only on acceptance.
- `internal/msgqueue/queue.go` → `Enqueue`, `Run`: admission is nonblocking, capacity rejects without mutation, lifecycle owns drains.
- `internal/conversations/registry.go` → `Update`, `Save`: locked mutation and best-effort atomic persistence.
- `internal/e2e/conversation_new_test.go`, `harness.go`, `channel_post_test.go`: real CLI, fake stream child and durable history assertions.
- `docs/knowledge/features/control-plane.md` § Conversation post: submitter errors must contain neither raw id nor text.
- `docs/knowledge/features/cli-verb-dispatch.md` § Conversation option and selector parsing: string flags can consume other flags as values; preserve explicit guard.
- `docs/knowledge/features/conversation-session-binding-routing.md`: empty binding must reject before bootstrap lookup; revival does not activate.
- `docs/knowledge/features/msgqueue-package.md`: ordinary queue owns pacing/retries and delivered user history.
- `docs/knowledge/features/conversations-registry.md`, `e2e-harness.md`, `development-verification.md`, `CODING-STYLE.md`: persistence and behavioral test conventions.
- `docs/protocol-mobile.md` § Security model: local operator input retains the ordinary user-turn authority and prompt threat model.

## Context

Creation and the control contract are merged, but scripts cannot start a user
turn by conversation id. Add one CLI-to-ordinary-inbound-queue deliverable for
both chat and channel. Host-authored `channel post` remains independent.
No decision record is needed. #2873 overlaps `main.go` only in independent
reply-suggestion fallback construction; our additions are local.

## Design

Add `post --id ID (--text TEXT | --file PATH)` dispatch and help in the two
production files above. Parse flag presence separately from content, reject
missing values/unknown flags/positionals with exit 2, and reuse
`channelPostContent` for bounded reads without trimming. Empty content is an
exit-1 error. Call `control.ConversationPost` once under the existing 30-second
CLI budget; return silently on acceptance without polling or resubmission.

`conversationSubmitter` closes over the registry, the stamp-free resolver,
queue admission and registry path/logger. Its contract is `(id, text) error`:
resolve before admission; map unknown and unavailable binding to static
refusals; call ordinary `Queue.Enqueue` exactly once; zero means static full
queue refusal. Only nonzero admission updates `LastUsedAt` and best-effort
saves. It does not activate, create, stamp the client cursor, name a chat,
interpret commands, synthesize frames or publish through channel delivery.
Install via `SetConversationSubmitter` beside the creator.

Sizing before commit: four acceptance criteria; approximately 650 total added
lines including plan/tests, zero new exported types/interfaces, two existing
production wiring sites, fewer than ten reject branches per operation.

## Concurrency model

No new goroutines. Control callbacks use existing registry/queue locks; no lock
is held across resolver, enqueue or persistence. Existing daemon queue lifecycle
cancels and joins drains. Binding changes after acceptance are handled by the
ordinary re-resolving delivery path and cannot revoke acceptance.

## Error handling

Usage errors exit 2. File/content, static daemon refusals and transport errors
exit 1 with stderr only. Queue rejection does not update last-used. Accepted
messages remain accepted despite Save or later delivery failure. Persistence
warnings contain only a static event, never caller id/text or resolver errors.

## Testing strategy

Write tests first and observe missing production symbols/behavior fail. Unit
scenarios use the real router/pool and queue to prove binding refusals, revival,
unchanged queue/last-used on refusal, exact single enqueue and best-effort Save.
Hermetic e2e creates fresh chat/channel with model/effort, posts by CLI with no
relay or paired client, and checks fake-child input/launch settings plus user,
assistant and completion history. Cover silence, selectors, text/file cap,
whitespace preservation, syntax and transport exits; a controlled socket peer
proves no transport resubmission. Reuse #2885 control tests.
Run race tests for `cmd/pyry` and selected `internal/e2e` (e2e tag), existing
control contract tests, `go vet ./...`, and build `cmd/pyry` into scratch.
The dispatcher owns the full-module verifier gate.

## Open questions

None; creation-time settings and no-relay history drain are existing contracts.

## Documentation handoff

Pending for the documentation stage:
- `README.md`, beside conversation-new: runnable command-substitution create
  with model/effort then post-by-id example. State user-turn semantics,
  acceptance-only success, unknown ids never create, bounded 64 KiB file reads.
- `docs/knowledge/features/control-plane.md` § Conversation: post a user message
  by id (conversation.post): replace pending-#2886 text with production routing,
  binding/full-queue refusals and existing channel-post distinction.

## Security review

**Verdict:** PASS

**Findings:**
- [Trust boundaries] Control's existing payload checks bound local socket input;
  `sessionRouter.resolve` validates the stored binding before admission, including
  its empty-binding guard and confined revival. No bootstrap fallback.
- [Tokens/secrets] No credential creation, reads or transmission are introduced.
- [Files] `channelPostContent` opens once and reads at most cap plus one byte;
  operator-selected symlinks are ordinary local file inputs. Registry Save uses
  its existing atomic persistence and permissions; no new stored format.
- [Subprocesses] Input follows `WriteUserTurn`, never shell interpolation. Existing
  supervised runner receives creation settings and owns child shutdown.
- [Cryptography] No cryptographic changes or new randomness.
- [Network/I/O] Reuse capped control decoding and existing request deadlines;
  no retry after an ambiguous transport result. Queue admission remains bounded.
- [Errors/logs] Resolver errors may contain caller-derived data: translate them
  to static unknown/unavailable refusals and do not log them or message content.
  Save warnings use a static event only.
- [Concurrency] Admission/refusal is atomic within the queue; post-admission
  registry mutation is separate and best-effort. Existing drains handle binding
  races, retries and shutdown without changing an already accepted result.
- [Threat model] Same-user control socket authority and ordinary user prompt
  semantics apply. Remote authentication and crypto remain unchanged.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-06
