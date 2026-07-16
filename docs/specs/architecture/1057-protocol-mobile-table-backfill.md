# Spec #1057 — Backfill the protocol-mobile message table with the ten missing shipped types

**Ticket:** pyrycode/pyrycode#1057 (split from #970) · **Size:** S · **Kind:** docs-only, no code changes · **Security-sensitive:** no

## Files to read first

- `internal/protocol/codes.go:40-143` — the v1 application `Type*` constants (`hello` … `register_push_token`). The per-constant doc comments for the ten new types (`:58-139`) state each type's Direction and reply-pairing **definitively**; they are the source of truth for the Direction and Notes columns (AC2). Transcribe from them — do not guess.
- `docs/protocol-mobile.md:405-450` — the `## Application message types` table. Note the two row formats: **plain** rows for v1 application types (`create_conversation` … `error`, `:417-423`) and **bold `**\`type\`**` "New in v2." rows** for every type from `rekey_request` down (`:424-448`). The ten new rows use the **plain** format.
- `internal/protocol/envelope.go:113-143` — `inboundAppTypeSet`, the closed enumeration of v1 inbound application types. All ten new types are members here, which is *why* they are plain rows, not bold v2 rows.
- `internal/protocol/compat_test.go:143-242` — `v2OnlyTypes` allowlist + `TestTypeConstants_V1V2Partition`. This is the deterministic code-side guarantee that `inboundAppTypeSet ∪ v2OnlyTypes = {all Type* constants}`, and the ten new types are in the **former**. It does NOT check the doc table — the doc-vs-source completeness (AC3) is verified by the developer via the grep in § Testing strategy.

## Context

The 2026-07-15 docs review found the `## Application message types` table in `docs/protocol-mobile.md` silently missing ten shipped message types. The authoritative vocabulary is the `protocol.Type*` constants in `internal/protocol/codes.go`; the table drifted below it. This backfills the ten rows so the table is a complete reference. The ten types already exist on the wire (constants + handlers shipped in prior tickets) — this is a documentation-completeness fix only.

The invariant to restore: **the set of `Type` values in the table = the set of `Type*` wire constants in `codes.go`.** Verified counts: `codes.go` defines **48** `Type*` constants; the table currently lists **38** (13 v1 plain rows + 25 v2 bold rows); the ten additions below make it **48 = 48**.

## Design

### The ten rows to add

All ten are v1-lineage `inboundAppTypeSet` members → **plain** rows (no `**bold**`, no `**New in v2.**` prefix, no `See [...]` v2 section link). This includes the three `binary → phone` reply types. "Carries handshake early-data?" is `no` for all ten — only `hello`/`hello_ack` ride handshake early-data (see the prose at `:407`).

Direction and Notes below are transcribed from the cited `codes.go` doc comments; the developer must confirm each against that comment rather than accept this table on trust (AC2). Notes document each request/reply pairing, mirroring the existing `set_session_settings` → `session_settings_updated` precedent.

| Type | Direction | Early-data? | Notes (derive/confirm from cited comment) | Source |
|---|---|---|---|---|
| `rename_conversation` | phone → binary | no | Renames an existing conversation; replies with the reused `conversation_updated` record. | `codes.go:58-63` |
| `delete_conversation` | phone → binary | no | Permanently removes a conversation (hard delete; the reversible path is `archive_conversation`); replies with `conversation_deleted`. | `codes.go:64-70` |
| `conversation_deleted` | binary → phone | no | Acknowledges a `delete_conversation`, correlated by `in_reply_to`; carries only the deleted conversation's `id` (the record no longer exists, so no name/cwd is projected). | `codes.go:71-76` |
| `archive_conversation` | phone → binary | no | Sets a conversation's durable archived flag (`IsArchived = true`); replies with the reused `conversation_updated` record. Symmetric restore is `unarchive_conversation` (shared payload). | `codes.go:77-84` |
| `unarchive_conversation` | phone → binary | no | Clears a conversation's durable archived flag (restore, `IsArchived = false`); replies with the reused `conversation_updated` record. | `codes.go:85-91` |
| `change_workspace` | phone → binary | no | Moves a conversation to a client-chosen workspace folder (updates its `cwd`, confined to `$HOME`); replies with the reused `conversation_updated` record. | `codes.go:92-104` |
| `create_workspace_folder` | phone → binary | no | Creates a new folder on the daemon host under a client-supplied parent path (confined to `$HOME`); touches no conversation registry. Replies with `workspace_folder_created`. | `codes.go:107-117` |
| `workspace_folder_created` | binary → phone | no | Reply to `create_workspace_folder`, correlated by `in_reply_to`; carries the created folder's canonical (symlink-resolved) absolute path. | `codes.go:118-123` |
| `recent_workspaces` | phone → binary | no | Read verb (like `list_conversations`); requests the distinct set of recently-used workspace folders. Empty request payload. Replies with `recent_workspaces_list`. | `codes.go:124-134` |
| `recent_workspaces_list` | binary → phone | no | Reply to `recent_workspaces`, correlated by `in_reply_to`; carries the distinct workspace paths, most-recent-first, each with its most-recent `last_used_at`. | `codes.go:135-139` |

### Placement

Insert all ten rows as a contiguous block **between the `conversation_updated` row (`docs/protocol-mobile.md:420`) and the `register_push_token` row (`:421`)**, in the order listed above. Rationale:

- It groups the six conversation verbs with the existing conversation cluster (`create_conversation` … `conversation_updated`) and keeps the four workspace verbs adjacent — mirroring `codes.go`'s `Conversations` → `Workspace` block adjacency (`:51-139`).
- It keeps each new reply type immediately after its request (`delete_conversation` → `conversation_deleted`; `create_workspace_folder` → `workspace_folder_created`; `recent_workspaces` → `recent_workspaces_list`).
- It leaves `register_push_token`, `ack`, `error`, and the entire bold v2 block untouched — minimal diff.

### The format trap (do not miss)

The rows below `rekey_request` (`:424` onward) are all `**bold**` and begin `**New in v2.**`. That convention is reserved for `v2OnlyTypes` members. The ten new types are **not** v2 — they are `inboundAppTypeSet` members. Render them exactly like `create_conversation`/`promote_conversation`: a plain backticked type in column 1, no bold, no "New in v2." sentence, no v2-section hyperlink. This includes the three `binary → phone` replies (`conversation_deleted`, `workspace_folder_created`, `recent_workspaces_list`) — a reply direction does not make a type v2.

## Concurrency model

N/A — documentation edit, no runtime code.

## Error handling

N/A — no runtime code. The only failure mode is a wrong Direction/Notes transcription or a missed format, caught by the review in § Testing strategy.

## Testing strategy

No new automated test (docs-only; the code-side partition is already pinned by `TestTypeConstants_V1V2Partition`). Verify by:

1. **AC3 — deterministic completeness (set equality).** After the edit, the set of wire values defined as `Type*` constants must equal the set of `Type` values in the table. Concretely, extract both and diff — the result must be empty:
   ```bash
   # constants (wire values):
   grep -oE 'Type[A-Za-z]+ = "[a-z_]+"' internal/protocol/codes.go \
     | grep -oE '"[a-z_]+"' | tr -d '"' | sort -u > /tmp/consts.txt
   # table Type column (first backticked token per row, bold-tolerant):
   sed -n '/## Application message types/,/^Payload shapes/p' docs/protocol-mobile.md \
     | grep -oE '^\| \*{0,2}`[a-z_]+`' | grep -oE '`[a-z_]+`' | tr -d '`' | sort -u > /tmp/table.txt
   comm -3 /tmp/consts.txt /tmp/table.txt   # MUST print nothing
   ```
   (48 constants; 48 table rows after the edit.)
2. **AC2 — per-row correctness.** For each of the ten rows, open the cited `codes.go:NN` comment and confirm the row's Direction (`phone → binary` vs `binary → phone`) and its reply-pairing match the comment. Do not accept the § Design table on trust — it is a transcription aid, the comment is the source of truth.
3. **Format check.** Confirm none of the ten rows are bolded and none contain "New in v2." — they sit visually among the plain v1 rows, not the bold v2 block.
4. **No code touched.** `git diff --name-only` shows only `docs/protocol-mobile.md` (and this spec file). `go build ./...` / `go test ./...` remain green trivially — no `.go` file changed.

## Open questions

None. The ten types, their directions, and their reply-pairings are fully determined by the `codes.go` doc comments; placement and format are specified above.
