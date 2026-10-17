# Conversation pins in relay handlers

## Files read
- `internal/conversations/registry.go` → `SetPinned`, `Get`, `Save`: atomic eligibility, stable UTC timestamps and persistence contracts.
- `internal/relay/handlers/set_conversation_muted.go` → `SetConversationMuted`: best-effort Save, post-mutation lookup, reply before announcement even on send failure.
- `internal/relay/handlers/list_conversations.go` → `ListConversationsWithAgents`: preserve filtering, ordering and history lookup.
- `internal/relay/handlers/rename_conversation.go` → `RenameConversation`: locked update projection.
- `internal/relay/handlers/autoname.go` → `autoNameConversation`, `ConversationAnnouncer`: stored snapshot and announcement seam.
- `internal/relay/handlers/promote_conversation.go` → `PromoteConversation`: sentinel mapping and read-back projection.
- `internal/relay/handlers/archive_conversation.go` → `ArchiveConversation`: archive and unarchive share a projection.
- `internal/relay/handlers/change_workspace.go` → `ChangeWorkspace`: workspace callback projection.
- `internal/relay/handlers/set_system_prompt.go` → `SetSystemPrompt`: stored projection without exposing prompt content.
- `internal/relay/handlers/mark_conversation_read.go` → `MarkConversationRead`: durable read-mark projection.
- `internal/relay/handlers/set_conversation_muted_test.go`: local registry, connection and announcement fixtures.
- `docs/knowledge/features/relay-package-handlers.md`: workspace label reads must happen outside registry update callbacks; compare payloads by value rather than pointer identity.
- `docs/knowledge/features/development-verification-protocol-boundaries.md`: partially populated decode failures must be inert; assert raw explicit nulls.
- `docs/protocol-mobile.md` § Security model: paired-device trust domain and untrusted content boundaries.
- `CODING-STYLE.md`: consumer-owned interfaces, race tests and metadata mutation conventions.

## Context
Storage #3535 and protocol #3536 are merged. Paired devices need pin writes and complete records preserving pin timestamps. This slice owns handler behavior only; daemon activation and command producers belong to #3538. No decision record is needed.

## Design
Add `ConversationPinner`, exposing `SetPinned(id, pinned, now) error`, `Get`, `Save` and `WorkspaceLabel`, and `SetConversationPinned(reg, registryPath, announce, logger) dispatch.Handler`. Decode the required pointer bool before mutation. Map storage eligibility/not-found sentinels to fixed non-retryable protocol errors. Use host UTC time, then read the stored row, eagerly Save even for no-ops, and project the same record to a correlated reply and announcement. A failed Save retains success in memory; a failed reply still announces. Unexpected storage errors return a fixed internal error without request data.

Add `PinnedAt` from the stored snapshot to all eight existing update builders and the list builder. Leave capability/visibility filtering, history semantics and ordering intact. No existing signatures or consumers change. No overlapping remote feature branches touch the planned handler files.

Sizing, checked against sketch and written plan: one deliverable, four acceptance criteria, approximately 650 total written lines, one exported interface, zero existing consumer updates, at most eight error/reject branches; all within limits.

## Concurrency model
No new goroutines or locks. Registry methods own synchronization; eligibility and mutation share the storage lock. `SetPinned` and `Get` are separate: a vanished row returns not-found before Save/announcement, matching mute. Reply and announcement use one snapshot; a concurrent later edit may supersede it just as with mute. Workspace labels are read outside registry update callbacks.

## State transitions and identity reuse
| Event | Race-enabled proof |
| --- | --- |
| Pin, repeated pin, unpin, repeated unpin, re-pin same ID | `TestSetConversationPinned_Transitions` |
| Archived pin and unpromoted/archived unpin | `TestSetConversationPinned_Eligibility` |
| Rejected partial decode targeting a live ID | `TestSetConversationPinned_Rejects` |
| Deletion between mutation and snapshot | `TestSetConversationPinned_Vanished` |
| Save failure and reply failure after mutation | `TestSetConversationPinned_Failures` |
| Metadata edits preserving the same timestamp | `TestConversationPinProjections` |

## Error handling
Malformed JSON/types or missing/null pinned: fixed `protocol.malformed`. Empty/unknown id or vanished row: fixed `conversation.not_found`. Pinning an unpromoted row: `conversation.not_promoted`, message `conversation not promoted`. Rejections do not Save or announce and log only fixed events and connection identity. Save errors log filesystem errors and remain best effort. Marshal and reply errors propagate; announcement follows the attempted reply.

## Testing strategy
Write handler tests first and observe their failure. Use real registries plus local counting/failure doubles, raw JSON requests and raw wire null assertions. Verify only PinnedAt changes, UTC/current-time bounds, stable repeats, fresh Load persistence, complete reply equality and reply-before-announcement. Table-test all update producers (archive both ways) and list for pinned and unpinned records. Run admitted race tests for `internal/relay/handlers`, `go vet ./...`, and `go build ./cmd/pyry` on macOS. The verifier owns the full-module gate; no live-Claude evidence is required.

## Open questions
None.

## Documentation handoff
- Pending documentation stage: add “Conversation pins (#3537)” to `docs/knowledge/features/relay-package-handlers.md`, describing eligibility, idempotent UTC timestamps, eager best-effort persistence, reply-before-announcement and pin-preserving projections.
- Pending documentation stage: in `docs/protocol-mobile.md`, update the `set_conversation_pinned` table row/section to distinguish implemented package handling from daemon activation still pending #3538.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: `SetConversationPinned` checks the entire decode and required bool before calling `SetPinned`; that storage method owns atomic eligibility. IDs are exact registry keys, not paths.
- Tokens/secrets: no credentials are created, read or changed. Only existing public conversation fields are projected; system prompts remain absent.
- File operations: only the host-configured registry path reaches `Save`; storage uses atomic temp-file/fsync/rename with file mode 0600 and directory mode 0700. Requests cannot select a path.
- Subprocesses: the interface exposes no session or subprocess operations.
- Cryptography: no changes to keys, nonces, randomness or encrypted transport.
- Network/I/O: reuse existing bounded authenticated dispatch and `Conn.Reply`; no new sockets, readers or deadlines.
- Errors/logs: fixed refusal messages and events contain neither supplied IDs nor decode errors. Tests use hostile markers and partially populated requests to prove this. Unexpected mutation errors must also avoid echoing arbitrary seam errors.
- Concurrency: no new goroutines/locks; storage serializes eligibility and mutation. A vanished snapshot gets the existing not-found behavior. Save failures deliberately retain memory state.
- Threat model: pinning stores metadata only and cannot deliver prompts or actuate sessions. Paired devices remain one trust domain. Daemon registration/capability integration is out of scope here and owned by #3538; transport authentication and encryption stay with existing dispatch.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17
