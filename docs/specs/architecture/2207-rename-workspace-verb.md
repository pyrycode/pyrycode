# #2207 — `rename_workspace`: a wire verb that stores a workspace label and replies `workspace_updated`

## Files read

Production surface:

- `internal/relay/handlers/rename_conversation.go` → `RenameConversation`, `ConversationRenamer`,
  `msgRenameConversationMalformed` / `msgRenameConversationEmptyName` / `msgRenameConversationNotFound`
  — the shape this handler copies: consumer-named narrow interface, static message constants
  each carrying the no-echo rationale, `replyError` on every reject, eager best-effort `Save`.
- `internal/relay/handlers/set_system_prompt.go` → `SetSystemPrompt`, `ConversationSystemPromptSetter`
  — the nullable set-and-clear precedent (one `*string` through one door) and the *stricter*
  logging posture this handler adopts: `conn_id` + a per-branch `event` and nothing else.
- `internal/relay/handlers/recent_workspaces.go` → `RecentWorkspaces`, `RecentWorkspacesReader`
  — the variadic `List(filter ...conversations.ListFilter)` narrow interface `*conversations.Registry`
  satisfies structurally, and the one place a blank `Cwd` is treated as "not a workspace".
- `internal/relay/handlers/register_push_token.go` → `replyError` — the file-local reject helper
  every branch here calls.
- `internal/conversations/registry.go` → `WorkspaceLabel`, `SetWorkspaceLabel`, `List`, `Save`
  — the storage contract #2206 landed. `SetWorkspaceLabel`'s doc comment states outright that it
  validates nothing, checks no conversation list, and calls no `Save`; all three are this slice's.
- `internal/protocol/workspace.go` → `RecentWorkspace`, `RecentWorkspacesListPayload`,
  `WorkspaceFolderCreatedPayload` — where the two new structs go, and the reply-side no-`omitempty`
  discipline they inherit.
- `internal/protocol/pairing.go` → `MaxDeviceNameBytes` — the 128-UTF-8-**byte** posture to reuse
  (fail-closed, never truncate, offending bytes never in the error) with a native rationale rather
  than a borrowed constant.
- `internal/protocol/codes.go` → `TypeRecentWorkspaces` / `TypeRecentWorkspacesList` (the `// Workspace.`
  group the two new types join) and `CodeModelListUnavailable` (the convention that a new code group
  is appended last with its own rationale block).
- `internal/protocol/envelope.go` → `inboundAppTypeSet`, `IsKnownAppType` — the app-frame partition
  both new constants join. It holds reply types as well as request types.
- `cmd/pyry/relay.go` → the `V2SessionConfig.Handlers` map literal — the single consumer call site,
  beside the `TypeRenameConversation` entry.

Guards that redden on a new `Type*` / `Code*` constant, each hand-maintained:

- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `excludedTypes` — every type constant must appear
  in exactly one map; in both or neither fails loudly.
- `internal/protocol/compat_test.go` → `TestInboundAppTypeSet_CoversAllExportedTypeConstants`
  (carries a hard `len(all) == 24` pin that must move to 26), `TestTypeConstants_V1V2Partition`,
  and `TestErrorCode_Constants_MatchSpec` (a hand-maintained wire-value pin, *not* a totality guard
  — the `history.*` codes are absent from it, so adding the new code is convention, not a gate).
- `internal/protocol/workspace_test.go` → `TestRecentWorkspacesListPayload_RoundTrip`, `readFixture`,
  `canonical` — the golden-fixture round-trip shape the two new fixtures follow.
- `internal/relay/handlers/rename_conversation_test.go` → `newRenameConvConn`, `newRenameConvReg`,
  `assertRenameConvEnvelopeShape` — the per-handler fixture trio to mirror; `testLogger` lives in
  `register_push_token_test.go`.

Documentation whose content changes how this is built:

- `docs/knowledge/features/conversations-registry-crud.md` § *`WorkspaceLabel` / `SetWorkspaceLabel`*
  — **"Downstream size bound deferred, not decided … the bound has to be picked at #2207 against N
  labels in one reply, not one label in isolation."** That is a direct instruction to this ticket and
  it is discharged in § Design below, in the constant's own rationale.
- `docs/knowledge/features/relay-package.md` § *Handlers* — sub-package isolation (`handlers` imports
  `conversations`, `dispatch`, `protocol`, never `internal/relay`) and the per-branch logging table.
- `docs/protocol-mobile.md` § *Application message types*, § *Setting a conversation's system prompt*
  (the section shape to copy: request example, a value table for the nullable field, the reply
  sentence, a refusal table), § *Error codes*.

## Context

An operator's workspace is a folder, and folders are named for the filesystem, not for the person.
#2206 landed the storage half — a per-workspace label keyed by the exact `cwd` string, living in
its own top-level `workspace_labels` map on the conversations registry, deliberately unvalidated
and deliberately non-persisting. Nothing writes it. This slice is the inbound verb that does:
`rename_workspace` in, `workspace_updated` back to the requester, with the validation and the eager
persist that #2206 named as this handler's to own.

Two siblings in the family are explicitly *not* this slice and must not be waited on. #2209 fans the
change out to other connected clients; #2208 lands the read projection so a re-list shows the new
label. This one replies to the requester only.

**No ADR is warranted.** The design decision worth recording — that a workspace's name travels with
the workspace rather than with the client — was Juhana's ruling on 2026-09-06 and is already carried
by #2206's spec and the registry doc comment. This slice adds a wire verb in the established
`rename_conversation` shape; there is no rejected alternative that a future reader would need
explaining.

### Sizing note — the 800-line ceiling is exceeded deliberately

Total written work lands near ~1000 lines, above the 800-line size-S boundary. Every other line of
the table holds with room: **5** production files, **3** new exported types, **1** consumer call
site, **5** acceptance criteria, **4** reject branches.

The only seam available is #2125's own commit boundary — declare the constants and payload structs
in one slice, answer the verb in another. The first half has **no observable behaviour and exactly
one consumer, the second half**. That is the floor case, and the floor beats the ceiling: a slice
nothing outside the family calls cannot be verified on its own, and no continuation leg repairs
that, while a budget miss costs one leg. Built as one ticket, in two commits, the way #2125 shipped
the identical shape. The refiner reached the same conclusion independently in the ticket's estimate.

## Design

### 1. `internal/protocol/codes.go` — two type constants, one error code

Two constants join the existing `// Workspace.` group, after `TypeRecentWorkspacesList`:

- `TypeRenameWorkspace = "rename_workspace"` — phone → binary `dispatch.Route` write verb. Sets or
  clears the operator-chosen display name of a workspace, keyed by the workspace's `cwd`. Keyed by
  **workspace, not by conversation** — N conversations share one `cwd` and a workspace has no row of
  its own, which is why the reply cannot be the reused `conversation_updated` record the way
  `change_workspace`'s is.
- `TypeWorkspaceUpdated = "workspace_updated"` — binary → phone reply, correlated via `in_reply_to`.
  A **new type rather than a new arm on an existing one** precisely so an un-upgraded client drops
  it as an unknown frame instead of mis-rendering a record it does not understand.

Both are `inboundAppTypeSet` members (§ 3), not v2 control frames.

One new error code, appended as its own group after the model-list one, following that group's
convention of a rationale block:

- `CodeWorkspaceNotFound = "workspace.not_found"` — the requested path matches no stored
  conversation's `cwd`. **Not** `CodeConversationNotFound`: the request names no conversation, and
  reusing the conversation code would tell a client to go look for a row that was never asked about.
  Non-retryable — the same path fails identically until a conversation is created there.

### 2. `internal/protocol/workspace.go` — two payload structs and the bound

```go
const MaxWorkspaceLabelBytes = 128

type RenameWorkspacePayload struct {
    Path  string  `json:"path"`
    Label *string `json:"label"`
}

type WorkspaceUpdatedPayload struct {
    Path  string  `json:"path"`
    Label *string `json:"label"`
}
```

Two structs rather than one shared type, mirroring `CreateWorkspaceFolderPayload` /
`WorkspaceFolderCreatedPayload`: they are structurally identical today and each is free to move
without dragging the other across the wire boundary.

`Label` is `*string` on both, and **neither carries `omitempty`**. Nullability is the whole point on
the request — `null` is how "clear" is said, distinctly from `""` — and on the reply the AC requires
a cleared label to arrive as an explicit `"label": null`, which `omitempty` would erase into an
absent key. That matches `RecentWorkspace`'s stated reply-side discipline.

**`MaxWorkspaceLabelBytes = 128`, and the rationale is native rather than borrowed.** The posture is
`MaxDeviceNameBytes`': UTF-8 **bytes, not runes**; refuse fail-closed rather than truncate; keep the
offending bytes out of the error. The *number* is re-derived here, because a workspace label and a
device name are unrelated fields that happen to share it. Two anchors:

- A label is one human-typed line rendered as a list row beside a folder path. 128 bytes is far past
  any hand-typed folder nickname while keeping one row unwrapped.
- The envelope arithmetic the registry doc asked this ticket to do — *"the bound has to be picked at
  #2207 against N labels in one reply, not one label in isolation."* #2208 embeds one label per
  conversation in a `list_conversations` reply against the 65519-byte v2 application-envelope cap.
  Worst-case JSON escaping is 6 bytes per source byte (a control byte becomes `\u00XX`), so a
  128-byte label costs at most **768 bytes** on the wire, plus ~18 for its key — call it ~790 bytes
  per labelled row on top of that row's existing cost. The cap therefore admits on the order of
  **80 fully-escaped worst-case labelled rows**, and a realistic label (ASCII, ~30 bytes) costs ~50.
  That is the number #2208 must budget against; it is recorded in the constant's doc comment so the
  arithmetic does not have to be re-derived there. **Picking a larger bound here would move that
  decision into a ticket that cannot revisit it**, which is why 128 is chosen with the list-shaped
  consumer in view rather than the single-field one.
- Stated in the constant's comment, transferred from `MaxAttachmentIDBytes` via `MaxDeviceNameBytes`:
  **a length ceiling is not a safety property.** 128 bytes holds an ANSI escape run or a newline
  injection several times over. The constant bounds size and nothing else; containment is § 4's job.

### 3. `internal/protocol/envelope.go` — partition membership

Both constants are added to `inboundAppTypeSet`. The set holds reply types alongside request types
(`TypeConversationUpdated` and `TypeRecentWorkspacesList` are both members), so `TypeWorkspaceUpdated`
belongs in it too. `TestInboundAppTypeSet_CoversAllExportedTypeConstants` carries a hard length pin
that moves 24 → 26, and `TestTypeConstants_V1V2Partition`'s v1 list gains both names.

### 4. `internal/relay/handlers/rename_workspace.go` — the handler

```go
type WorkspaceLabeler interface {
    List(filter ...conversations.ListFilter) []conversations.Conversation
    SetWorkspaceLabel(cwd string, label *string)
    Save(path string) error
}

func RenameWorkspace(reg WorkspaceLabeler, registryPath string, logger *slog.Logger) dispatch.Handler
```

A fresh consumer-named narrow interface, per the package convention; `*conversations.Registry`
satisfies it structurally with no adapter. The `List` line is copied verbatim from
`RecentWorkspacesReader` so structural matching holds against the variadic signature.

**The interface deliberately has no `WorkspaceLabel` read method.** The reply's label is projected
from the request value, which *is* the stored value by `SetWorkspaceLabel`'s verbatim-store contract,
so no read is needed — and lacking the method makes it structurally impossible for this handler to
reply with a label the requester did not itself supply. A projection that cannot reach a value is a
stronger guarantee than a handler that declines to fetch one. Do not widen it.

**The reply's `path`, by contrast, is projected from the matched row's `Cwd`, never from the
request** (§ Security review, *Trust boundaries*). The two are byte-equal by the match condition, so
this changes no byte on the wire — it changes where the bytes come from. The one request-derived
value on a success reply becomes daemon-stored state reached only after the path matched, which is
`set_system_prompt`'s "projected from the STORED record, never from the request" posture. It also
means an unbounded request path can never reach the wire at all.

Four static message constants at the top of the file, each carrying the reason its payload is not
echoed, in the `rename_conversation.go` shape. Branch order:

| # | Condition | Code | Effect |
|---|---|---|---|
| 1 | `json.Unmarshal` into `RenameWorkspacePayload` fails | `protocol.malformed` | nothing stored |
| 2 | `Label != nil` and `strings.TrimSpace(*Label) == ""` | `protocol.malformed` | nothing stored |
| 3 | `Label != nil` and `len(*Label) > MaxWorkspaceLabelBytes` | `protocol.malformed` | nothing stored, **never truncated** |
| 4 | no conversation's `Cwd` byte-equals `Path` | `workspace.not_found` | nothing stored |
| — | otherwise | — | `SetWorkspaceLabel`, best-effort `Save`, reply `workspace_updated` |

All four are non-retryable and all four reply through `replyError` with a fixed static string.

**Validation precedes the existence check**, mirroring `rename_conversation`'s empty-name guard
running before `Update`. Cheap client-fault rejects answer first, so a blank or oversized label
against a path that does not exist reveals nothing about whether it exists. (The verb is not
otherwise an existence oracle worth defending: a paired client can already enumerate every stored
`cwd` through `recent_workspaces`.)

**Both bound checks are skipped when `Label == nil`.** A clear carries no value to bound, and
`strings.TrimSpace` on a nil pointee would panic. The clear path still goes through branch 4 — the
AC's not-found refusal is unqualified, so clearing a label on a path no conversation names is
refused exactly like setting one.

**The existence check** ranges `reg.List()` — unfiltered, so archived rows are included by design
(a workspace is a folder; archiving a conversation does not un-name its folder) — and compares
`conv.Cwd == p.Path` as bytes. `ListFilter` has no cwd field and `List` is the only read surface
over rows, so a scan is the available shape. It returns a copy with the registry mutex taken
internally, so the scan runs lock-free on data this handler owns.

Deliberate divergence from `RecentWorkspaces`, which skips rows whose `Cwd` is blank: **this handler
does not skip them.** That handler *emits* rows and an empty string is not a workspace worth listing;
this one *matches a key*, and the AC specifies byte-equality with no carve-out. A conversation with
a blank `Cwd` is therefore labellable under the `""` key — which is consistent rather than junk,
since #2208 will read the label back by that same conversation's `cwd`.

The label is stored **verbatim, the raw untrimmed wire value**: trimming is used for the blank check
only, exactly as `rename_conversation` treats its title. `SetWorkspaceLabel` copies the pointee into
a fresh local before storing, so the registry map never aliases the decoded payload.

The eager `Save` is best-effort: failure is logged at Error and is non-fatal, because the in-memory
write already happened and is what every subsequent read sees. Same treatment
create / rename / delete / archive / set_system_prompt each give their own `Save`.

**Logging follows `set_system_prompt`'s strict posture, not `rename_conversation`'s.** Every branch
logs `event` and `conn_id` and nothing else; the single exception is `persist_failed`'s `err`, a
filesystem error naming the daemon's own registry path. Never logged: the label (operator content),
the path (a host filesystem path, and attacker-supplied on every reject branch), and the decode
error (`encoding/json` quotes offending input into its message). The cost is accepted and visible:
the record says a label changed and over which conn, not for which workspace.

### 5. `cmd/pyry/relay.go` — one table entry

```go
protocol.TypeRenameWorkspace: handlers.RenameWorkspace(w.convReg, resolveConversationsRegistryPath(w.instanceName), logger),
```

Placed beside the `TypeRenameConversation` entry. No session, pool or runner surface is wired,
because the verb touches no session: like `set_system_prompt`, the handler cannot restart or
interrupt anything, since it holds no seam through which it could.

### 6. `cmd/pyry/relay_guard_test.go` — classification

`"TypeRenameWorkspace": "map-dispatched"` in `inboundTypes`; `"TypeWorkspaceUpdated": "reply"` in
`excludedTypes`. The reply's entry notes that #2209 adds an unsolicited-push producer, at which
point it becomes this map's second dual-classified entry alongside `TypeConversationUpdated` — the
label is a review string no assertion parses, so that is a comment change, not a red gate.

### 7. `docs/protocol-mobile.md`

- Two rows in the § *Application message types* table, beside the existing workspace rows.
- A `### Renaming a workspace` section after § *Reading a conversation's system prompt*, in that
  section's shape: a request example, the nullable-`label` value table (`null`/omitted = clear,
  `""` = rejected as blank, any string = stored verbatim up to **128 bytes**), the correlated-reply
  sentence, and the refusal table with all four conditions and their codes.
- One row in § *Error codes* for `workspace.not_found`.

## Concurrency model

No goroutine is spawned and none is needed; the handler runs synchronously on the dispatcher's
call. All shared state is behind `*conversations.Registry`'s own `r.mu`.

Three separately-locked registry operations run in sequence: the `List` scan, `SetWorkspaceLabel`,
and `Save`. There is **no lock held across them and no attempt to hold one** — the registry exposes
no compound door and inventing one is out of scope.

The one observable interleaving is a concurrent `delete_conversation` removing the last conversation
at that `cwd` between the scan and the store. The result is a label stored for a workspace that no
longer has a conversation: an **orphan**, which the registry doc already records as the accepted
steady state (nothing garbage-collects an orphaned key, and an orphan is invisible because no client
shows a workspace with no conversations). It is not a correctness failure and needs no compound lock.

`Save` serialises internally and copies the label map element-wise inside `r.mu` — the discipline
#2206 established because a map-header copy would make a concurrent write a fatal
`concurrent map iteration and map write` throw rather than a detectable race. Nothing here shares a
map across that boundary: the handler passes a `*string` and never sees the map.

## Error handling

Four reject branches, all non-retryable, all replying `replyError` with a fixed static string, none
storing anything, none echoing a byte of the request payload. Table in § 4.

Two failures that are *not* reject branches:

- **`Save` failure** — logged at Error with `err`, then the success reply is sent anyway. The
  in-memory write is real and usable; durability is best-effort. Answering an error here would tell
  the client the rename did not happen when it did.
- **`json.Marshal` of the reply payload failing** — returned as a wrapped error
  (`fmt.Errorf("marshal workspace_updated payload: %w", err)`) to the dispatcher, matching every
  sibling. Unreachable in practice (two plain string-shaped fields) and not special-cased.

No `panic` anywhere; the nil-pointee hazard on `*Label` is closed by the `Label != nil` guard on
branches 2 and 3.

## Testing strategy

**RED first**: the handler tests are written and watched fail before `rename_workspace.go` exists.

`internal/relay/handlers/rename_workspace_test.go`, mirroring `rename_conversation_test.go`'s
fixture trio (`newRenameWsConn`, `newRenameWsReg`, `assertRenameWsEnvelopeShape`) with `testLogger`:

- **Success stores verbatim and replies** — a label with surrounding whitespace and a non-ASCII rune
  is stored **untrimmed**, the reply carries the same path and label, `in_reply_to` names the
  request, and `reg.WorkspaceLabel(path)` returns the raw value.
- **Survives a daemon restart** — after the handler runs, a fresh `conversations.Load` of the same
  path reports the label. This is the assertion that would stay green if the eager `Save` were
  dropped only if the test re-read the *same in-memory registry*, so it reloads from disk.
- **Archived-only workspace is matchable** — the sole conversation at that `cwd` has
  `IsArchived: true`; the rename succeeds. Pins that the scan uses the unfiltered list.
- **Null label clears, and the clear is a distinct state** — set, then send `"label": null`;
  `WorkspaceLabel` reports **absent** (`ok == false`), not a present empty string, and the reply
  carries an explicit `null`. The reply assertion reads the raw JSON for `"label":null` rather than
  the decoded pointer, so an accidental `omitempty` reddens it.
- **Not found leaves the store untouched** — a path no conversation names gets
  `workspace.not_found`, non-retryable, the static message, and a pre-existing label at a *different*
  path is unchanged.
- **Clearing an unknown path is refused too** — `null` label at an unmatched path is
  `workspace.not_found`, not a silent success.
- **Blank label refused, table-driven** over `""`, `"   "`, `"\t\n "` — `protocol.malformed`, and a
  pre-existing label at that path is unchanged (the guard runs before the store).
- **Over-bound label refused, never truncated** — a label of `MaxWorkspaceLabelBytes+1` bytes is
  rejected and nothing is stored; the boundary arm at exactly `MaxWorkspaceLabelBytes` **succeeds**,
  so an off-by-one in either direction reddens. A third arm uses multi-byte runes whose *rune* count
  is under the bound while the *byte* count is over, pinning bytes-not-runes.
- **Malformed payload** — `[]byte("{")` yields `protocol.malformed` with the static message and no
  store.
- **No echo on every reject branch** — an injected-looking path and label
  (`"../../etc/passwd\x00<script>"`) appear in **no** reject reply; asserted against each of the four
  branches' messages, and additionally by scanning the marshalled error envelope's bytes for a
  fragment of the injected string.
- **Save failure is non-fatal** — a registry path whose parent is a regular file makes `Save` fail;
  the handler still replies `workspace_updated` and the in-memory label is set. Copies
  `TestRegisterPushToken_SaveFailure`'s blocker-file technique.

`internal/protocol/workspace_test.go` — two golden round-trips against new fixtures
`testdata/rename_workspace.json` and `testdata/workspace_updated.json`, in
`TestRecentWorkspacesListPayload_RoundTrip`'s shape: decode the envelope, assert the type, assert
`in_reply_to` on the reply, assert **each field's value** (distinct path and label values across the
two fixtures so the assertions discriminate rather than merely re-encoding), then compare
`canonical(out)` against `canonical(raw)`.

`internal/protocol/compat_test.go` — the three hand-maintained lists gain their entries and the
length pin moves 24 → 26.

Verification gate (§ B2): `go test -race` on `./internal/protocol/... ./internal/relay/... ./cmd/pyry/...`,
`go vet ./...`, `go build ./cmd/pyry`. The full-module race suite is the verifier's gate, not run here.

## Open questions

1. **Does the golden fixture for the request use a set or a clear?** Leaning set, with the clear
   covered by the handler test's raw-JSON assertion — a fixture whose `label` is `null` would not
   discriminate an accidental `omitempty` on round-trip, since an absent key also decodes to nil.
   *Resolve while writing the fixtures; record in `## Revisions` if the reply fixture ends up
   carrying the clear instead.*
2. **Does `TestTypeConstants_V1V2Partition` carry a length pin of its own** like its sibling does?
   Only `TestInboundAppTypeSet_CoversAllExportedTypeConstants` was read closely enough to confirm
   one. *Resolve by running the package; the test names what it wants.*
3. **Does any other guard key on the error-code set** beyond `TestErrorCode_Constants_MatchSpec`
   (which the ticket establishes is a convention pin, not a totality gate)? *Resolve by running
   `go test ./internal/protocol/...` after adding the constant.*
4. **Does `replyError` add anything to the wire beyond code / message / retryable?** Every sibling
   reject branch relies on it carrying exactly the static string it is handed. *Resolve by reading
   its body in `register_push_token.go` before the first reject branch is written.*

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] SHOULD FIX — project the reply's `path` from the matched row, not from the
  request.** The boundary is explicit and singular: one `json.Unmarshal` into
  `RenameWorkspacePayload` inside the handler closure, yielding exactly two untrusted fields.
  `Path` is used only as a comparison operand against `conv.Cwd` and as a map key into
  `workspace_labels` — never resolved, joined, stat-ed, opened, or passed to a process — and every
  reject branch answers with a static string, so it reaches the wire on the success path only. That
  echo is *argued* safe (the path byte-equals a stored `cwd`, or the branch would not have been
  reached) rather than *structurally* safe. Since the scan already holds the matched row, projecting
  `WorkspaceUpdatedPayload.Path` from `conv.Cwd` makes the property structural at the cost of one
  local. Folded into § 4 above; the verifier should check it landed. Not a MUST FIX because the two
  values are provably identical, so nothing is exploitable as designed.
- **[Trust boundaries] No further findings — the `Label` field's containment is complete and its
  one inherited assumption holds.** It is stored opaque, echoed only to the requester, never logged,
  never interpolated into an error message, never a path component or argv element.
  `SetWorkspaceLabel`'s doc comment states it assumes a valid-UTF-8 label for round-trip fidelity
  and that the wire path cannot violate that, because `encoding/json` substitutes U+FFFD for invalid
  bytes and unpaired surrogates while decoding into a Go string. This handler is a wire caller, so
  the assumption is satisfied by construction — the reason no UTF-8 branch is needed here even
  though the neighbouring `SetSystemPrompt` raises a sentinel for it.
- **[Tokens, secrets, credentials] No findings — none are minted, stored, compared or logged, and
  the handler holds no seam to any.** Reachability is the authenticated paired Noise session: a
  frame is decrypted under the session's receive state before it reaches dispatch, the same gate
  every conversation write verb already has. `c.Auth()` is deliberately not consulted, matching
  `RenameConversation` and `SetSystemPrompt`. No interactive-capability gate either, and that
  matches `SetSystemPrompt`'s stated reasoning: the capability is read only in the outbound
  fan-out and is not an inbound gate.
- **[File operations] No findings — the single filesystem operation takes a daemon-derived path.**
  `reg.Save(registryPath)` is called with `resolveConversationsRegistryPath(w.instanceName)`, wired
  at the composition root and never client-influenced. No client byte is joined into it. Mode and
  write-atomicity are `Registry.Save`'s and unchanged by this ticket, which adds no new exposure:
  `RenameConversation`, `CreateConversation`, `ArchiveConversation` and `SetSystemPrompt` already
  drive that same call from this same network-reachable path. There is no `os.Stat`-then-`os.Open`
  on any caller-controlled path, so the classic TOCTOU shape does not arise (the check-then-mutate
  that does exist is over in-memory state — see *Concurrency*).
- **[Subprocess / external command execution] Not applicable, and structurally so.**
  `WorkspaceLabeler` names three methods — `List`, `SetWorkspaceLabel`, `Save` — none of which
  reaches a process, so the handler cannot spawn, signal, or recompose the argv of anything even by
  mistake. Same argument `ConversationSystemPromptSetter` makes about restarts, and the reason the
  interface must not be widened.
- **[Cryptographic primitives] Not applicable — no randomness and no comparison against a secret.**
  The one comparison, `conv.Cwd == p.Path`, is against non-secret daemon state: a workspace path is
  not a credential, so `crypto/subtle` is not indicated and `==` is correct. The scan short-circuits
  on first match, so response time weakly correlates with a matched row's position — which leaks
  only the ordering of a list the same client can already fetch verbatim through `recent_workspaces`.
- **[Network & I/O] No findings, and the unbounded `Path` is considered and declined rather than
  overlooked.** Envelope size is capped upstream at 65519 bytes; within it `Label` is bounded at
  `MaxWorkspaceLabelBytes`. `Path` carries no explicit bound: an oversized one matches no stored
  `cwd`, so it takes the not-found branch, stores nothing and — with the fix above — reaches the
  wire not at all. Go's string `==` compares lengths first, so a long non-matching path costs O(1)
  per row. Adding a length refusal would mint a reject branch the acceptance criteria do not have,
  for no containment gain. No listener, upgrade, TLS config or read deadline is introduced; the
  handler runs inside an already-established session.
- **[Network & I/O — resource exhaustion] No findings, and the bound is load-bearing rather than
  cosmetic.** `workspace_labels` cannot grow without limit: a key is creatable only at a path that
  byte-equals a stored conversation's `cwd`, so the key count is bounded by the number of distinct
  `cwd`s the daemon actually hosts. **The not-found branch is what enforces that bound** — it is a
  containment property, not only a UX nicety, and a future relaxation of it would remove the map's
  only ceiling. The remaining exposure is write amplification (a paired client hot-looping the verb
  drives one whole-registry `Save` per frame), which is not new: every existing write verb has it
  identically, and the pipeline's established answer is that a paired client is authenticated.
- **[Error messages, logs, telemetry] No findings — the strict posture is chosen over the
  permissive sibling deliberately.** All four rejects carry fixed static strings declared at the top
  of the file, each with the comment saying why its payload is not echoed. Logging follows
  `SetSystemPrompt`, not `RenameConversation`: `event` and `conn_id` on every branch and nothing
  else. Never logged are the label (operator content), the path (a host filesystem path, and
  attacker-supplied on every reject branch), and the decode error (`encoding/json` quotes offending
  input into its message). The one exception is `persist_failed`'s `err` — a filesystem error naming
  the daemon's own registry path, the single field on this handler safe to log. The accepted cost is
  that the record says a label changed and over which conn, but not for which workspace.
- **[Concurrency] No findings — the one interleaving produces an already-documented benign state.**
  Only one lock is ever taken (the registry's own) and never nested, so no ordering question arises.
  The scan, the store and the `Save` are three separately-locked operations with no compound door
  held across them, and none is invented here. The check-then-mutate gap admits exactly one
  observable race: a concurrent `delete_conversation` removing the last conversation at that `cwd`
  between the scan and the store, leaving an **orphaned label**. The registry documentation already
  records orphans as the accepted steady state (nothing collects them, and an orphan is invisible
  because no client renders a workspace with no conversations). No goroutine is spawned, so there is
  no lifecycle or leak to reason about. A mid-`Save` signal is `Registry.Save`'s own concern and is
  unchanged.
- **[Threat model alignment] No findings; two threats named out of scope with their owners.**
  § *Security model* of `docs/protocol-mobile.md` treats a paired client as authenticated but not
  fully trusted, requires that wire input not reach the filesystem or a process, and requires that
  error replies not become oracles. All three are addressed above. Cross-client disclosure does not
  arise here because the reply goes only to the requester; the fan-out is **#2209**. The
  list-shaped envelope budget is **#2208**, for which this ticket picks the bound and records the
  arithmetic rather than deferring it.
- **[Threat model alignment] OUT OF SCOPE — rendering safety of a stored label belongs to the
  client, and the byte bound must not be mistaken for a sanitiser.** A label is an opaque display
  string the daemon stores and echoes; 128 bytes accommodates an ANSI escape run or a newline
  injection several times over. `MaxDeviceNameBytes`' warning transfers verbatim and is restated in
  the new constant's own comment so a later reader does not read the ceiling as containment. No
  in-repo consumer is owed a change by this slice; the display surfaces are the mobile clients'.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07
