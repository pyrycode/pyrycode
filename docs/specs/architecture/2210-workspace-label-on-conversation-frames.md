# #2210 — Carry the workspace label on `conversation_updated` and `conversation_created`

## Files read

- `internal/conversations/registry.go` → `WorkspaceLabel`, `SetWorkspaceLabel` — the accessor this slice consumes. Its doc comment is explicit that a stored empty label reads `("", true)` and an absent one `("", false)`, and that the two stay distinct; it also names #2210 as a consumer that calls it once per conversation behind a narrow interface. It takes `r.mu`, a plain non-reentrant `sync.Mutex` — the deadlock hazard below.
- `internal/relay/handlers/list_conversations.go` → `ConversationLister`, `ListConversations` — #2208's shipped precedent: the narrow interface gains the read method, and the projection derives the pointer from the accessor's second return with the rationale spelled out. This slice mirrors it exactly and reuses the file as the home for the shared helper.
- `internal/protocol/conversations_read.go` → `ConversationSummary.WorkspaceLabel` — the field this slice copies verbatim: `*string`, `json:"workspace_label"`, deliberately no `omitempty`, with the "nullable but never omitted is a client-visible contract" rationale.
- `internal/protocol/conversations_write.go` → `ConversationUpdatedPayload`, `ConversationCreatedPayload` — the two structs gaining the field. The former's doc comment records why it carries no `system_prompt`; that reasoning is the one this slice must distinguish itself from (see Context).
- `internal/relay/handlers/change_workspace.go` → `ChangeWorkspace`, `ConversationWorkspaceUpdater`, `WorkspaceResolver` — builds its payload *inside* `reg.Update`'s callback. Also the AC3 producer: it stores `resolved`, the resolver's confined realpath, not the request's `p.Cwd`.
- `internal/relay/handlers/rename_conversation.go` → `RenameConversation`, `ConversationRenamer` — the second in-callback producer, and the harder one: its `cwd` is only reachable as `cv.Cwd` inside the callback.
- `internal/relay/handlers/archive_conversation.go` → `ArchiveConversation`, `ConversationArchiver` — one function serving archive and unarchive, wired twice; builds after a `Get` with no lock held.
- `internal/relay/handlers/promote_conversation.go` → `PromoteConversation`, `ConversationPromoter` — builds from a `Get` read-back.
- `internal/relay/handlers/set_system_prompt.go` → `SetSystemPrompt`, `ConversationSystemPromptSetter` — builds after `Get` and `Save`. Its test file holds the one test double this change breaks (see Testing strategy).
- `internal/relay/handlers/create_conversation.go` → `CreateConversation`, `ConversationCreator` — the sole `conversation_created` producer; builds after `reg.Create` from the local `cwd`.
- `cmd/pyry/channel.go` → the `announce` call in the `channel new` creator — #2156's unsolicited push. Takes a concrete `*conversations.Registry`, so no interface widens here.
- `cmd/pyry/relay.go` → the handler map — all seven wiring sites pass `w.convReg`, the real `*conversations.Registry`. Confirms zero call sites change.
- `internal/protocol/testdata/conversation_updated.json`, `conversation_created.json` — the hand-written wire examples that drift silently.
- `docs/protocol-mobile.md` § message-type table — the `conversations` row is #2208's documentation template; the `conversation_created` row carries no notes at all today.
- `docs/knowledge/features/conversations-registry-crud.md` § `WorkspaceLabel` / `SetWorkspaceLabel` — records that the 128-byte bound lives on the wire payload, not the registry, and that the registry stores what it is given under the key it is given.

## Context

The daemon stores a per-workspace label keyed by the exact `cwd` string (#2206) and #2208 put it on every `list_conversations` row. A client that patches a row in place from a pushed frame still falls back to the folder name until its next full list, because neither conversation frame carries the label. This slice closes that gap on both frames, from every producer.

A client cannot denormalise its way out: `RecentWorkspace` carries no label, and `workspace_updated` fires on a *label* change, not on a *conversation move*. After a `change_workspace` a client holds a new `cwd` and no way to name it — AC3, and the strongest reason the field belongs on the frame.

**Why this is not the `system_prompt` case.** `ConversationUpdatedPayload` deliberately carries no system prompt because that frame is broadcast to every phone on the server-id, and a projection type lacking a field cannot leak it. That reasoning does not transfer. The label is already disclosed to exactly this audience by `list_conversations` — #2208 shipped it to every client permitted to read the row's `cwd`, which is the same set. Adding it here widens no audience; withholding it would only force the client to re-list to learn a value it may already hold.

No ADR is warranted: this repeats a shipped decision rather than making a new one.

**Sizing overage, carried deliberately.** Nine production files against the table's five (the ticket estimated eight; the shared helper's home in `list_conversations.go` is the ninth). Every other line is clear: ~590 lines of total written work against 800, zero new exported types, zero call sites needing simultaneous update, four acceptance criteria, no new reject branch. Split depth is one (parent #2158, no grandparent), so a split is permitted — but the floor rule fires and outranks the ceiling. Each available axis (by payload type, by package, or by halving the six `conversation_updated` producers) yields a child whose only deliverable is consumed by nothing outside the family, and AC1 ("from every producer") and AC2 ("every such frame carries `null`") are literally unverifiable against a partial producer set. `needs-human:sizing` is already on the ticket, so the judgement is visible on the board.

## Design

### Protocol — one field on each payload

Both `ConversationUpdatedPayload` and `ConversationCreatedPayload` gain, positioned after `Cwd` to mirror `ConversationSummary`:

```go
WorkspaceLabel *string `json:"workspace_label"`
```

A pointer with **no `omitempty`**, copying `ConversationSummary.WorkspaceLabel`'s discipline verbatim: nil is "no label stored" and serializes an explicit `null`; a non-nil pointer to `""` is the distinct explicitly-empty label the registry admits. The absent `omitempty` is a client-visible contract, not merely a round-trip property — both shipped clients decode `workspace_label: string | null` and fail closed on a missing key.

### One shared projection helper

The "derive the pointer from the accessor's second return" rule is the single correctness hazard repeated across seven sites. It lives in one place rather than being restated (or silently violated) at each:

- `workspaceLabelReader` — an unexported one-method interface, `WorkspaceLabel(cwd string) (string, bool)`.
- `workspaceLabelFor(r workspaceLabelReader, cwd string) *string` — returns `&label` when the accessor reports present, nil otherwise. Behaviour summary: never compares the string; presence comes only from the second return.

Both live in `list_conversations.go`, the file that already owns this package's workspace-label read and its rationale. Each of the six widened handler interfaces satisfies `workspaceLabelReader` by method-set inclusion, so call sites pass their existing `reg` unchanged.

`ListConversations`'s own inline projection is **deliberately left as it stands**. It is correct, and rewriting working code is outside this ticket.

### Interfaces — six widened, one untouched

`ConversationArchiver`, `ConversationWorkspaceUpdater`, `ConversationPromoter`, `ConversationRenamer`, `ConversationSystemPromptSetter` and `ConversationCreator` each gain `WorkspaceLabel(cwd string) (string, bool)`. `cmd/pyry/channel.go` takes a concrete `*conversations.Registry` and gains nothing.

All seven wiring sites in `cmd/pyry/relay.go` pass `w.convReg`, which satisfies the widened interfaces structurally — **zero call sites change**.

### Producers — two shapes, and the difference is load-bearing

**Shape A — no lock held (five sites).** `ArchiveConversation`, `PromoteConversation`, `SetSystemPrompt` (each after a `Get`), `CreateConversation` (after `Create`, keyed on the local `cwd`), and `cmd/pyry/channel.go`'s announce (after its `Get` read-back). The helper is called in place at the struct literal, keyed on the record's own `Cwd`. `channel.go` cannot reach the unexported helper across the package boundary and inlines the same three lines.

**Shape B — payload built inside `reg.Update`'s callback (two sites).** `Registry.Update` holds `r.mu` for the duration of the callback and `WorkspaceLabel` takes the same non-reentrant mutex, so **calling the accessor inside the callback deadlocks the daemon**. Both sites read the label *after* `Update` returns and *after* its `hit` check, assigning onto the already-populated `updated`:

- `ChangeWorkspace` — `resolved` is already in scope outside the callback; key on it.
- `RenameConversation` — the `cwd` exists only as `cv.Cwd` inside the callback, so capture it into a local there and key on that local afterwards.

Reading after the `hit` check, not before, keeps a not-found reply from performing a pointless registry read.

### Keying — the stored `cwd`, never the requested one

Labels are keyed byte-exactly and the registry normalizes nothing. `ChangeWorkspace` stores the resolver's confined realpath, which is also what it puts on the payload's `Cwd`; looking the label up under `p.Cwd` would pass any test whose resolver is an identity function and diverge the moment one is not. Every site keys on the same value it writes to the payload's `Cwd` field — a rule that is checkable by reading the struct literal alone.

## Concurrency model

No goroutines are added and no shutdown path changes. The whole concurrency content of this slice is the lock-order constraint above: `Registry.Update` holds `r.mu` across its callback, `WorkspaceLabel` acquires `r.mu`, and the mutex is not reentrant, so the accessor is never called from inside a callback. The two in-callback producers are the only sites where that is a live hazard, and both move the read out.

Each read is one lock acquisition, released before the next statement. Under Shape B the record snapshot and the label read are two separate acquisitions rather than one, so a concurrent `SetWorkspaceLabel` between them is reflected in the reply — the truthful current state, matching the existing `Create`→`Get` and `Promote`→`Get` read-back convention this package already documents as truthful rather than atomic.

## Error handling

No new failure mode and no new reject branch. The accessor cannot fail: it returns a value and a presence bool, and absence is a represented state on the wire (`null`), not an error. Every existing refusal path returns before the projection runs, so a refused frame performs no label read and carries no label.

The label is never logged, never interpolated into an error message, and never used as a path component — see Security review.

## Testing strategy

RED first: every assertion below fails against the current tree, either by compile error (the new field does not exist) or by a missing key.

**Per-producer coverage (AC1)** — one case per producer, seven in total: six in `internal/relay/handlers`, one in `cmd/pyry` for the announce path. Each stores a label for the conversation's workspace, drives the handler, and asserts the emitted frame carries it. The `cmd/pyry` case is the only one that proves the unsolicited push, which is a distinct frame from the five replies.

**Explicit `null`, not an omitted key (AC2)** — asserted on **raw bytes**, decoding the payload into `map[string]json.RawMessage` and requiring the key to be present with the literal value `null`. A decoded `*string` is nil in both the absent-key and the null-value cases and structurally cannot tell them apart, so a struct-level assertion here would be vacuous. Covered on both payload types, against a registry where no label has ever been set.

**Destination, not origin (AC3)** — `ChangeWorkspace` with a **non-identity resolver**: distinct labels stored for the request path and for the resolved path, the conversation moved between two labelled workspaces, and the frame required to carry the destination's label. Storing a decoy label under the request path is what makes the case discriminating: with an identity resolver the test passes whether or not the lookup keys off the stored `cwd`.

**Stored-empty stays distinct from absent** — a workspace whose label is stored as `""` yields `"workspace_label":""`, not `null`, pinning that the projection never derives presence from `label != ""`.

**Golden fixtures** — `conversation_updated.json` gains a real label and `conversation_created.json` gains `null`, so both states have a hand-written example. The envelope round-trip tests cannot catch an omission (`Envelope.Payload` is a `json.RawMessage`, so re-marshalling writes the fixture's original bytes back and the byte comparison passes whatever the struct gained); the **decoded per-field assertions** those tests already make are extended instead, exactly as #2208 did.

**Known test cascade — the ticket's note is wrong here.** The ticket states no test in `internal/relay/handlers` implements these interfaces with a double. `recordingSetter` in `set_system_prompt_test.go` does: it implements `ConversationSystemPromptSetter` over a real registry and must gain a forwarding `WorkspaceLabel` method or the package stops compiling. Worse, `TestSetSystemPrompt_TouchesNoSessionSurface` pins the **exact** call sequence (`[]string{"SetSystemPrompt", "Get", "Save"}`) and its doc comment asserts the handler's entire daemon interaction is "three conversations-registry calls". Both the expectation and the prose are updated deliberately to four; the count is the point of that test, so it must move consciously rather than be patched into green.

## Open questions

1. **Helper home.** `list_conversations.go` versus a new shared file in the package. Resolved in favour of the existing file: the package has no `common.go` convention (`replyError` lives in `register_push_token.go`), and this keeps every workspace-label read in one place.
2. **Whether to collapse `ListConversations`'s inline projection onto the new helper.** Resolved: no. It is correct as written and the change is out of scope.
3. **Where the label read sits relative to `Save` in the Shape-A producers.** To be confirmed during implementation that placing it at the struct literal (after `Save`) keeps each handler's registry call sequence readable; the `set_system_prompt` call-sequence test pins the answer.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, and this is the category that had to be walked hardest.** The value is operator-supplied, entering at `RenameWorkspace`'s handler (non-blank check plus the `MaxWorkspaceLabelBytes` bound) and leaving here. The live question is **audience**, because the struct this slice edits carries a doc comment refusing a different field on exactly that ground: `ConversationUpdatedPayload` deliberately omits `system_prompt` because the frame is **broadcast to every phone on this server-id**, and a projection type lacking a field cannot leak it. That reasoning does **not** transfer, and the distinguishing fact is checkable: `system_prompt` is on no read path any other client can reach — not `list_conversations`, not `session_settings` — so putting it on a broadcast would have created disclosure. The workspace label is already disclosed to the identical principal set by #2208, which puts it on **every row** of a `list_conversations` reply available to **any** paired client. The recipients of these two frames are a subset of that set (the reply legs go to the requester; the #2156 push is further narrowed by an interactive-capability gate). Adding the key **widens no audience** — it changes only whether a client learns a value at push time or at its next list.
- **[Errors, logs, telemetry] No findings — verified against the code, not assumed.** No touched producer logs a `cwd` or a label today; the log lines in all seven carry `event`, `conn_id`, `conversation_id` and, for archive, `is_archived` only. No error message can carry the value either: the sole error path is `json.Marshal`, and a `*string` field cannot produce a marshal error — `encoding/json` substitutes U+FFFD for invalid UTF-8 in strings rather than failing, so the wrapped `marshal conversation_updated payload: %w` can never embed label bytes. That substitution is the same one `SetWorkspaceLabel`'s doc comment reasons about, and it applies identically to the shipped `list_conversations` path, so the two read paths cannot disagree about a value.
- **[Concurrency] The one real hazard, addressed by design — and it is a security finding, not merely a correctness one.** `Registry.Update` holds `r.mu` across its callback and `WorkspaceLabel` acquires the same **non-reentrant** `sync.Mutex`. The naive implementation ("fill the field where the literal is built") deadlocks at `ChangeWorkspace` and `RenameConversation`, and a goroutine parked forever holding `r.mu` takes down every registry consumer in the daemon — a client-triggerable, self-inflicted **denial of service** reachable by one ordinary `change_workspace` frame. Design puts both reads after `Update` returns and after its `hit` check (§ Design, Shape B). No new lock-ordering edge is introduced: the documented `saveMu → mu` order is untouched, and every added acquisition happens with no lock held.
- **[File operations] No findings — worth stating because the shape invites the opposite assumption.** The lookup key is a `cwd`, which reads like a path operation and is not one: `WorkspaceLabel` is a map read whose doc comment states it normalizes, resolves, joins, stats and opens nothing. The label itself never becomes a path component, is never written to disk by this slice, and no file is created or opened on any path this change adds. The registry file's existing atomic snapshot→encode→fsync→rename is untouched.
- **[Subprocess execution] No findings.** The label reaches no `exec.Command` argv, no environment variable, and no `sh -c`. Named explicitly because a workspace *display name* flowing into a spawn's working-directory argv would be the obvious exploit shape for this data, and the value's only consumers are a struct field and `json.Marshal`.
- **[Network & I/O] No findings — the size arithmetic is the one that matters and it is the inverse of the `system_prompt` case.** These frames carry **one** conversation, so at most one label, bounded at 128 UTF-8 bytes by the write path. Against the 65519-byte application-envelope cap that is noise. This is precisely why the label is admissible where the prompt was not: a prompt is 8192 bytes and the list reply carries N rows, so a handful of prompted conversations would take the whole reply over the cap. One label on a single-row frame has no such multiplier.
- **[Tokens, secrets, credentials] Not applicable, by the value's definition.** The label is an operator-chosen display name, not a credential: nothing generates, hashes, rotates, expires or revokes it, and no code path compares it against a secret. Note also that the projection performs **no string comparison at all** — presence comes solely from the accessor's second return — so there is no equality check here for a timing question to attach to.
- **[Cryptographic primitives] Not applicable.** No randomness, no key material, no comparison of attacker-controlled bytes to a secret. The frames ride inside the existing AEAD-sealed envelope; this slice adds a struct field and changes no transport.
- **[Threat model alignment]** Threat 3 (*relay sees only ciphertext*): unchanged — the key travels inside the sealed application payload like every sibling field, so the relay never observes it. Threat 4 (*token leak via phone*): unchanged in kind — a compromised paired client learns workspace labels, which it can already enumerate wholesale via `list_conversations`, so this slice grants it nothing new. Threat 1 (*prompt injection*) does **not** land: no field added here is `claude`-authored; the value crosses no subprocess boundary. Rendering the label safely remains the client's job, exactly as `docs/protocol-mobile.md` already states for the `conversations` row — its 128-byte bound is a size limit and **not** a safety property, and this slice must repeat that in the two rows it documents rather than let the new rows imply a sanitized value.
- **[Trust boundaries — deferred, OUT OF SCOPE] The bound is a write-path invariant and this slice must not re-assert it.** `SetWorkspaceLabel` deliberately does not validate; its doc comment states that any future caller inherits an unvalidated door and must bring its own bounds. Today `rename_workspace` is the only writer and it bounds at 128 bytes, so the invariant holds. Re-validating on this read path would be **wrong**, not merely redundant: it would make these frames disagree with `list_conversations` about the same stored value. If a second writer ever lands without a bound, the fix belongs at that writer. No ticket filed — this is the registry's documented, already-shipped contract rather than a gap this slice opens.
- **[Concurrency — accepted, not a finding] Two acquisitions instead of one under Shape B.** The record snapshot and the label read are separate lock acquisitions, so a `rename_workspace` landing between them is reflected in the reply. Both possible values are legitimately-stored operator labels, nothing is escalated by observing either, and the outcome is the truthful current state — the same non-atomic read-back convention `promote_conversation`'s `Promote`→`Get` and `channel new`'s `Create`→`Get` already document as truthful rather than atomic.

**SHOULD FIX (Phase B obligation, verifier-checkable):** no log line, at any of the seven producers, may gain the label — including the `cmd/pyry/channel.go` announce site, which sits directly between a `log.Info` and a `log.Warn` and is the easiest place to add a "helpful" field. The value stays wire-only.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-08
