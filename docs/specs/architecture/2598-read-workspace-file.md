# #2598 — read a markdown file live from a conversation's workspace

## Files read

- `internal/relay/v2session_attachment_request.go` → `handleRequestAttachment`, `attachmentStreamAborted`, `rejectAttachmentRequest`, `rejectAttachmentNotFound`, `rejectStreamAborted` — the handler this one mirrors step for step, and the reject vocabulary it reuses unchanged.
- `internal/relay/v2attachmentstream.go` → `StreamAttachment`, `attachmentEnvelopes` — the chunk builder and Push loop. `StreamAttachment` reopens by path with `os.ReadFile`, so it must not be called here (it would discard `readChecked`'s identity check).
- `internal/relay/v2session.go` → `dispatchAppFrame` (the `TypeRequestAttachment` case), `appFrameKind`, `appFrameWorker` — where the new frame is intercepted and routed off Run.
- `internal/relay/v2session_seams.go` → `V2SessionConfig.AttachmentResolve`, `KnownConversation` — the seam shape the new field sits beside.
- `cmd/pyry/attach_file.go` → `confineFile`, `readChecked`, `maxAttachFileBytes`, `fileAttacher` — the confinement and checked read this path reuses as is; `fileAttacher` reads `conv.Cwd` at call time, the same rule applies here.
- `cmd/pyry/relay.go` → the `attachmentResolve` closure and the `KnownConversation` closure in the `V2SessionConfig` literal — where the new reader is wired.
- `internal/protocol/attachments.go` → `RequestAttachmentPayload`; `internal/protocol/codes.go` → `TypeRequestAttachment` — the declarations the new type and payload sit beside.
- `internal/protocol/compat_test.go`, `cmd/pyry/relay_guard_test.go` — the classification guards every new inbound type must be filed in (IsKnownAppType rejection row, the exhaustive type set, `switch-intercepted`).
- `internal/relay/v2session_attachment_request_test.go` → `startRetrievalConn`, `waitForReplies`, `assertRejectFrame`, `expectNoReply` — the real-handshake harness the new relay tests reuse.
- `cmd/pyry/attach_file_test.go` → `newAttachFixture`, `writeFile`, `TestFileAttacher_Confinement` — the refusal matrix already proven; new tests only show this path goes through it.
- `internal/conversations/id.go` → `NewID` — the lowercase UUIDv4 minter.

## Context

The clients' in-app markdown reader must show the file as it is on the host now. `request_attachment` serves a stored copy, so it cannot. This adds one inbound verb, `read_workspace_file {conversation_id, path}`, answered with a live read of a markdown file inside that conversation's recorded workspace, streamed as `attachment_chunk` frames.

It is the first wire path whose filesystem path a paired client names. That is worth an ADR-grade note for the documentation stage: "client-named paths are confined to the conversation workspace through `confineFile`/`readChecked`, restricted to markdown, with one undistinguished refusal".

**File count overage, stated.** Eight production files (codes.go, attachments.go, v2session.go, v2session_seams.go, v2attachmentstream.go, new handler file, new cmd/pyry reader file, relay.go) against a ceiling of five. The protocol type, the seam and the reader each have this handler as their only consumer, so the floor rule forbids splitting them off; the ticket's estimate already named the overage. Lines stay under 800.

In-flight overlap: `origin/feature/449` (last touched 2026-05-17) edits `codes.go` and `v2session.go`; no dependency. Edits there are additive.

## Design

### Protocol (`internal/protocol`)

- `codes.go`: `TypeReadWorkspaceFile = "read_workspace_file"` — phone → binary, inbound v2 control, switch-intercepted.
- `attachments.go`, beside `RequestAttachmentPayload`: `ReadWorkspaceFilePayload{ConversationID string "conversation_id"; Path string "path"}`. No omitempty, no MarshalJSON. Both fields unverified claims.

### Relay seam (`internal/relay/v2session_seams.go`)

```go
// WorkspaceFile is one live read answered by the WorkspaceFileRead seam.
type WorkspaceFile struct {
    AttachmentID string // daemon-minted lowercase UUIDv4, this transfer's key only
    Filename     string // base name of the RESOLVED file
    Data         []byte
}

// V2SessionConfig field, beside AttachmentResolve:
WorkspaceFileRead func(conversationID, path string) (WorkspaceFile, bool)
```

Comma-ok like `AttachmentResolve`: every refusal cause collapses into `false` inside cmd/pyry, so the handler cannot branch on what it must not distinguish and never holds a path-bearing error. nil ⇒ the frame is consumed and inert.

### Stream core (`internal/relay/v2attachmentstream.go`)

Extract `(*V2SessionManager).streamAttachmentBytes(ctx, connID, attachmentID, filename string, blob []byte, inReplyTo uint64) error` — `attachmentEnvelopes` + the Push loop + the existing debug line. `StreamAttachment` becomes read-then-call, behaviour unchanged (its existing tests pin that).

### Dispatch (`internal/relay/v2session.go`)

- `case protocol.TypeReadWorkspaceFile:` beside `TypeRequestAttachment`, enqueuing `appFrameJob{kind: appFrameWorkspaceFileRead}`.
- New `appFrameKind` member `appFrameWorkspaceFileRead` (appended last) and a worker arm calling `handleReadWorkspaceFile`.

### Handler (`internal/relay/v2session_workspace_file.go`, new)

`func (m *V2SessionManager) handleReadWorkspaceFile(ctx context.Context, s *V2Session, plaintext []byte)`, in `handleRequestAttachment`'s order:

1. `WorkspaceFileRead == nil` → debug log, return (consumed, inert).
2. Envelope decode (unreachable failure) → warn, return.
3. Payload decode failure → reject not_found.
4. `KnownConversation` nil or false → reject not_found (conversation id never logged).
5. Seam `false` → reject not_found.
6. `streamAttachmentBytes` error → `attachmentStreamAborted(err)` ? stream_aborted : not_found.
7. Success → info log with conn id, minted attachment id, in_reply_to.

Rejects go through a sibling of `rejectAttachmentRequest` with its own event names (`v2.workspace_file.refused` etc.), reusing `rejectAttachmentNotFound` / `rejectStreamAborted` and `attachmentReplyError`. Reasons are daemon constants. Never logged: path, filename, conversation id, bytes, size.

### Reader (`cmd/pyry/workspace_file.go`, new)

`workspaceFileReader(convReg *conversations.Registry, maxBytes int64) func(conversationID, path string) (relay.WorkspaceFile, bool)`:

1. `isMarkdownName(filepath.Base(path))` false → refuse. **First statement, before any registry or filesystem access.**
2. `convReg.Get(id)` miss → refuse (the handler's gate already ran; this is the registry read for `Cwd`, at request time).
3. `conv.Cwd == ""` → refuse.
4. `confineFile(conv.Cwd, path)` → refuse on error.
5. `isMarkdownName(filepath.Base(resolved))` false → refuse. **The resolved leaf is checked too**: a symlink `notes.md → .env` inside the workspace passes confinement and would otherwise serve `.env`.
6. `readChecked(resolved, checked, maxBytes)` → refuse on error.
7. `conversations.NewID()` → refuse on error; else return `{id, filepath.Base(resolved), data}, true`.

`isMarkdownName(name string) bool`: `strings.ToLower(filepath.Ext(name))` is `.md` or `.markdown`. Pure.

Wired in `cmd/pyry/relay.go`'s config literal beside `AttachmentResolve`: `WorkspaceFileRead: workspaceFileReader(w.convReg, maxAttachFileBytes)`. The bound is the existing unpublished 16 MiB receiver policy, which also keeps `attachmentEnvelopes`' "at most 16 MiB" arithmetic assumption true.

## Concurrency model

No new goroutines. The handler runs on the conn's existing `appFrameWorker` (FIFO, one per conn, so one read in flight per conn — the concurrency bound). Chunks leave via `Push` (safe from any goroutine); rejects via `attachmentReplyError` → `forwardToRun`, so `s.send` stays single-owner. The registry read is `Registry.Get` (mutex-guarded).

## Error handling

Every refusal is `attachment.not_found`, non-retryable, static message `"attachment not found"`, correlated by `in_reply_to` — identical bytes whatever the cause. A Push failure after emission may have begun is `attachment.stream_aborted`, via the existing `attachmentStreamAborted`. Nothing on the store is written.

## Testing strategy

Relay (`internal/relay/v2session_workspace_file_test.go`, real handshake via `startRetrievalConn`-style helper with the new seam wired):
- Serves: multi-chunk markdown blob from a fake seam → chunks correlated to the request; `ReassembleAttachment` recovers it; filename and minted id carried.
- Refusals table: undecodable payload, unknown conversation (seam never called), seam false → each exactly one `attachment.not_found` frame, static message, non-retryable; log buffer contains neither the path nor the conversation id.
- nil seam → consumed, no reply.

cmd/pyry (`cmd/pyry/workspace_file_test.go`, real registry + temp workspace):
- Reads a `.md` relative and absolute; `.MARKDOWN` accepted; returns resolved base name and a `conversations.ValidID` id.
- Live: write, read, rewrite, read → second read returns new bytes; two reads mint different ids; nothing under any attachment store (reader takes no instance dir).
- `change_workspace` effect: update `Cwd` via `Registry.Update` → next read resolves against the new root.
- Refusals: wrong extension (`.env`, `x.md.txt`), extension check precedes registry lookup (unknown conversation + `.env` does not touch the registry — shown by the pure `isMarkdownName` table plus a wrong-extension read against a registry with no conversation), empty Cwd, unknown conversation, missing file, traversal `../outside.md`, symlink-out `link.md → ../outside/x.md`, symlink-in-tree `notes.md → .env` (resolved-leaf check), directory `dir.md/`, over the bound.
- `isMarkdownName` table.

Guards: add `TypeReadWorkspaceFile` to `compat_test.go`'s rejection rows / known set / exhaustive list, and `relay_guard_test.go` as `switch-intercepted`; a wire-key pin for `ReadWorkspaceFilePayload` in `attachments_test.go`.

## Open questions

- Does any existing test enumerate `appFrameKind` members or `V2SessionConfig` fields exhaustively? **Resolved in Phase B:** no. The only exhaustive enumerations are the type guards already listed (`compat_test.go`, `relay_guard_test.go`), and both are updated. No design change.

## Documentation handoff

Pending for the documentation stage, from the ticket: publish `read_workspace_file` in `docs/protocol-mobile.md` — a message-table row beside `request_attachment` and a section beside `#### request_attachment` under § Attachments stating the two fields, the markdown-only rule (final component `.md`/`.markdown`, any case — checked on both the requested and the resolved leaf), live-read semantics (no stored copy), the minted per-transfer `attachment_id`, the single `attachment.not_found` refusal that does not say why, and that `conversation_id` is a lookup key, not authorization.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — two explicit boundaries. The handler `handleReadWorkspaceFile` gates the client's conversation id through `KnownConversation` before anything resolves; the client's path crosses into the filesystem only inside `workspaceFileReader`, through `confineFile` (canonicalised root and target, `withinDir` containment) and `readChecked`. The seam's comma-ok shape means the relay side never holds a filesystem error or a resolved path.
- [File operations — markdown rule bypass] Addressed in design (was the finding that shaped step 5): a symlink named `notes.md` resolving to `.env` inside the workspace passes confinement, so a check on the requested leaf alone would serve a non-markdown file. The reader re-checks `isMarkdownName` on the resolved leaf before `readChecked` opens anything. A test pins it.
- [File operations — TOCTOU] No findings — `readChecked` opens with `O_NOFOLLOW|O_NONBLOCK` and compares the descriptor to the checked stat with `os.SameFile`; the swap-between-check-and-read case is already proven by `TestConfineFile_SwapBetweenCheckAndRead`. `StreamAttachment` (which re-reads by path) is deliberately not used; the handler streams the bytes `readChecked` returned.
- [File operations — FIFO / non-regular] No findings — `confineFile` refuses non-regular files before any open, and `O_NONBLOCK` covers a post-check swap, so the conn's worker cannot wedge.
- [File operations — hard links] OUT OF SCOPE (accepted residual) — a hard link named `x.md` inside the workspace to a file elsewhere on the same filesystem is indistinguishable from a regular file. Creating one needs write access to the workspace, which claude has and which already lets it read the target. No ticket; stated so it reads as a decision.
- [File operations — writes] No findings — nothing is written; the reader takes no instance directory, so it cannot reach the attachment store.
- [Tokens / crypto] No findings — the per-transfer id is `conversations.NewID` (`crypto/rand` UUIDv4). It is a correlation key, not a capability; nothing accepts it back.
- [Network & I/O — size / exhaustion] No findings — the read is bounded by `maxAttachFileBytes` (16 MiB, twice-checked by `readChecked`), and the per-conn FIFO worker bounds a conn to one read in flight. Both unpublished, per #1751.
- [Error messages / logs] No findings — one static reject for every cause; reasons are daemon constants. The path, the filename, the conversation id, the bytes and the size are never logged; the success line carries only conn id, the minted id and in_reply_to. Tests scan the log buffer for the path and the conversation id.
- [Existence oracle — timing] OUT OF SCOPE — refusal causes differ in latency (extension check vs. a stat), so timing could distinguish "wrong extension" from "missing file". The wrong-extension answer is decidable by the client from its own input, and `request_attachment` carries the same property; no ticket.
- [Concurrency] No findings — no goroutines added; reject emission stays on `forwardToRun`, so the single-owner send CipherState is untouched.
- [Threat model — breadth] OUT OF SCOPE (stated residual, from the ticket) — any paired client can read any markdown file in any hosted conversation's workspace, including a workspace of `/`. Pairing is the authorization, as for every verb. Widening beyond markdown is a later ticket.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-24
