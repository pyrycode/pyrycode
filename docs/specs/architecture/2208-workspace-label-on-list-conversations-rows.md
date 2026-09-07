# #2208 — project the workspace label onto every `list_conversations` row

## Files read

- `internal/protocol/conversations_read.go` → `ConversationSummary` — the row type gaining the field. `Name`'s doc comment is the shape the new one copies: nullable, deliberately not `omitempty`.
- `internal/relay/handlers/list_conversations.go` → `ConversationLister`, `ListConversations` — the narrow interface to widen, and the tree's only construction site of `ConversationSummary`.
- `internal/relay/handlers/rename_workspace.go` → `WorkspaceLabeler` — the neighbouring workspace-label interface, read to confirm it stays untouched. Its doc comment's disclosure-barrier argument is a property of *that* handler (its reply echoes the requester's own value, so it needs no read door); it does not transfer to a projection whose whole job is to disclose the stored label.
- `internal/conversations/registry.go` → `WorkspaceLabel` — the accessor: `(string, bool)`, byte-exact key match, `("", true)` for a stored empty label vs `("", false)` for absent. Its doc names this slice as a per-conversation caller behind its own narrow interface.
- `internal/protocol/testdata/conversations.json` — the hand-written wire example, gaining the key on both rows.
- `internal/protocol/conversations_read_test.go` → `TestConversationsPayload_RoundTrip` — asserts decoded per-row fields *and* re-marshals. The byte check runs through `Envelope.Payload` (`json.RawMessage`), so it writes the fixture's own payload bytes back and cannot see a struct field the fixture lacks.
- `internal/relay/handlers/list_conversations_test.go` → `TestListConversations_EmptyRegistry`, `TestListConversations_SingleConversation`, `TestListConversations_SurfacesArchivedFlag` — existing coverage. The empty case pins exact payload bytes `{"conversations":[]}` and is unaffected by a per-row key.
- `docs/knowledge/features/protocol-package-types-conversations-read-payloads.md` — records that `ConversationSummary`'s field declaration order matches the fixture's per-row key order; the new field is placed consistently in both.
- `docs/knowledge/features/conversations-registry-crud.md` § workspace-label bullets — #2207 picked `MaxWorkspaceLabelBytes` = 128 *against this consumer*: worst-case JSON escaping is 6 bytes per source byte, ≈790 wire bytes per labelled row inside the 65519-byte v2 application envelope.
- `docs/protocol-mobile.md` § Message types, § Renaming a workspace — the `conversations` row (no field notes today) is where the key gets documented; the rename section already promises a client re-listing on another device "sees the new label through the list".

## Change

`ConversationSummary` gains `WorkspaceLabel *string` tagged `json:"workspace_label"` with **no** `omitempty`, placed directly after `Cwd` — the field it is resolved from, and the position that keeps declaration order matching the fixture's key order. Doc-commented in `Name`'s shape, with the one thing `Name`'s comment cannot say: here the absent `omitempty` is a client-visible contract, not only a round-trip property — pyrycode-desktop#1182's parser fails closed on a missing key the way it already does for a missing `cwd`.

`ConversationLister` gains `WorkspaceLabel(cwd string) (string, bool)`. `*conversations.Registry` satisfies it structurally; the sole production call site (the handler map in `cmd/pyry/relay.go`) already passes the real registry, and no test double in the handlers package implements the interface, so nothing else moves. `ListConversations`'s projection loop calls it once per row with that row's own `conv.Cwd`, byte-for-byte as stored, and derives the pointer **from the accessor's second return, never from the string** — `("", true)` is a stored empty label and must serialize as `""`, not `null`. Nothing else in the loop changes: the list stays unfiltered, so an archived row carries the key exactly like an active one.

`internal/protocol/testdata/conversations.json` gains the key on both rows — a label on row 0, `null` on row 1 — so the hand-written example matches what the daemon now emits.

`docs/protocol-mobile.md`'s `conversations` row in the message-type table gains its first field note, covering the key, its nullability, and that the label is the workspace's rather than the conversation's.

`WorkspaceLabeler` in `rename_workspace.go` is not touched. `RecentWorkspace` is out of scope per the ticket.

## Testing strategy

- `internal/protocol`, in `TestConversationsPayload_RoundTrip`: a decoded assertion per row — row 0 a non-nil pointer to the fixture's label, row 1 nil. These are the only proof the key is on the type; the existing re-marshal byte check is blind to it.
- `internal/relay/handlers`, new tests in `list_conversations_test.go`:
  - **Per-row resolution.** Two rows in *different* workspaces, one labelled and one not, in a single reply: each carries its own value and neither inherits the other's.
  - **Present-and-null before any label exists.** A registry with no label set at all — assert on the raw reply bytes that every row carries `"workspace_label":null`. A decoded-struct check cannot distinguish a nil pointer from an accidentally-`omitempty` dropped key, which is the exact regression the missing tag would cause.
  - **Archived rows are not exempt.** An archived conversation in a labelled workspace carries the label.
  - **Stored empty label serializes as `""`, not `null`.** The assertion that reddens if the pointer is derived from `label != ""` instead of the accessor's `ok`.
- `TestListConversations_EmptyRegistry` stands unchanged: zero rows, zero keys.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. This slice adds no inbound boundary: `ListConversationsPayload` is `struct{}`, so no byte of the request reaches the lookup. The lookup key is `conv.Cwd`, read back from the registry, and the only untrusted value in play — the label itself — was validated one ticket upstream by `rename_workspace`'s handler (non-blank, ≤128 UTF-8 bytes, path must byte-match a stored conversation's `cwd`). Named for the future: a variant that let a *client* supply the cwd to resolve would become a map-probe oracle ("is a label stored at path P?"); this design cannot, because the key is always a row the same frame is already disclosing.
- **[Tokens, secrets, credentials]** Not applicable — an operator-typed display name participates in no authentication decision, is never compared, and has no lifecycle beyond `SetWorkspaceLabel`. The adjacent question is whether the key widens what an *unauthorized* party sees, and it does not: the field rides the same reply, gated by the same authorization, as `cwd` — a filesystem path on the daemon's host, strictly more sensitive than a name the same operator chose for it.
- **[File operations]** SHOULD FIX (a Phase-B rule, not a plan change): the lookup must pass `conv.Cwd` **verbatim**. `WorkspaceLabel`'s contract is byte-exact — nothing at that layer normalizes, resolves, joins, stats, or opens the key — so canonicalizing here would resolve a row against a key `rename_workspace` never stored, silently yielding `null` for a labelled workspace. The label value itself must never become a path component.
- **[Subprocess / external command execution]** Not applicable — no `exec` on this path; the value is marshalled to JSON and written to a socket, and never becomes an argv element or an environment value.
- **[Cryptographic primitives]** Not applicable — no randomness, no comparison against a secret, no key material. Confidentiality is the Noise_IK channel's, unchanged by an added field.
- **[Network & I/O]** OUT OF SCOPE, and named because it is the one category where a per-row field genuinely multiplies. The reply carries **every** row, and #2207 sized the 128-byte label bound against exactly this frame: ≈790 worst-case wire bytes per labelled row inside the 65519-byte application-envelope cap, so ≈80 fully-escaped labelled rows and ≈50 realistic ones. `list_conversations` has no row cap and had none before this slice, and per-row growth is already dominated by `cwd`, which is bounded only by the host's `PATH_MAX` — worst-case an order of magnitude above a label's. So this ticket neither creates the overflow nor materially advances it. A row or size cap on the `conversations` reply is a pre-existing gap that deserves its own ticket; do not widen this one to cover it.
- **[Error messages, logs, telemetry]** SHOULD FIX (a Phase-B rule): the projection must add **no log call and no error string**. #2207 established that neither the label nor the path reaches a daemon log on any path, and the property holds here for a structural reason worth preserving — `WorkspaceLabel` cannot fail, so there is no error for a label to leak into. The concrete thing not to write is a "label lookup missed" record: it would emit a host filesystem path at info level for every unlabelled row, on the most frequently issued read verb the daemon serves.
- **[Concurrency]** No findings, with the non-atomicity stated deliberately rather than discovered later. The handler holds no lock of its own; `List` takes and releases `r.mu`, then each `WorkspaceLabel` takes and releases it again — never nested, so no new lock-order edge exists to document. A `SetWorkspaceLabel` landing between two rows' lookups therefore yields a reply where one row carries the old label and another the new, **including two rows that share one `cwd`**. That is accepted: a label is per-workspace display text with no invariant tying two rows together, the reply was already non-atomic between `List` and any concurrent mutation, and the alternatives — holding `mu` across the whole projection, or snapshotting the map — reintroduce exactly the range-the-live-map fatal-throw hazard that `WorkspaceLabel`'s per-key signature exists to make structurally impossible. No goroutine is spawned and no shutdown path is touched.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model, threats relevant here: **#3 relay operator MITM** — the field rides inside the AEAD-sealed `noise_msg` payload, so the relay sees ciphertext and the mitigation is unchanged. **#1 prompt injection** — the label is never injected into any prompt by this slice; it is stored on the daemon and projected to clients only, and the spec already states that its byte bound is a size limit rather than a safety property, leaving safe rendering to the client. No other listed threat is engaged by adding a read-only display field to an existing reply.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07
