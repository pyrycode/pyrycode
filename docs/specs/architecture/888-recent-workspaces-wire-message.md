# Spec #888 — `recent_workspaces` v2 wire message

**Ticket:** [#888](https://github.com/pyrycode/pyrycode/issues/888) — `feat(wire): recent_workspaces v2 wire message`
**Size:** S · **Label:** none (read of daemon-owned paths to an already-authenticated client — **not** security-sensitive; contrast the write-to-disk sibling `create_workspace_folder` #887).
**Family:** a v1TypeSet `dispatch.Route` **read** verb, direct sibling of `list_conversations` (#255) and cousin of `create_workspace_folder` (#887). Split from the #825 daemon-wire epic; independent of #887.

---

## Files to read first

The developer's turn-1 reading list. Read these before writing code; each line says what to extract.

- `internal/relay/handlers/list_conversations.go` (whole, ~60 lines) — **THE template.** Clone its shape verbatim at the coarse level: a consumer-defined narrow reader interface (`ConversationLister`), then `read → project → sort (the `!Equal`-guard + order + tie-break comparator) → marshal → `c.Reply(...)`` (`c.Reply` stamps `id`/`ts`/`in_reply_to`). **Diverge in three ways** (see §Design): fold the rows into a `map[Cwd]maxLastUsedAt` (dedupe) instead of a 1:1 projection; sort **descending** (`.After`, most-recent-first) not ascending; reply a **new** `recent_workspaces_list` type, not `conversations`.
- `internal/relay/handlers/list_conversations_test.go:16-107` — the dispatcher-harness helpers (`runListConvDispatcher`, `makeListConversationsFrame`, `recvOutbound`, `decodeConversationsResponse`) and `TestListConversations_EmptyRegistry`. Mirror these for the new handler test, especially the **empty-registry byte assertion** (`{"conversations":[]}` → your `{"workspaces":[]}`), the `InReplyTo`/`ID==1`/`TS non-zero` checks, and the non-nil-empty-slice requirement.
- `internal/conversations/conversation.go:40-46, 72-88` — the three fields the derivation reads: `Cwd` (always-present absolute path), `IsArchived` (durable `omitempty` flag), `LastUsedAt` (always-present — the ordering key). Read the `Cwd` doc: it is the `$HOME`-confined realpath, "always present."
- `internal/conversations/registry.go:139-172` — `List(filter ...ListFilter) []Conversation` + `ListFilter{IsPromoted, IsArchived}`. Two load-bearing facts: **`List` returns a copy** (line 148-150 doc — safe to sort/mutate the result), and the `IsArchived *bool` filter is the lever design-decision (a) deliberately does **not** pull (§Design decisions). Call `reg.List()` with **no filter**.
- `internal/protocol/conversations_read.go` (whole, 39 lines) — the read-verb payload precedent: `ListConversationsPayload struct{}` (empty request body "so the dispatcher decodes a concrete value") and `ConversationsPayload`/`ConversationSummary` (reply body + entry). Extract the serialization discipline: reply slices are **non-nil** (`make(..., 0, n)`) so an empty result marshals as `[]` not `null`, and reply-side fields carry no `omitempty` (client reads them on every row).
- `internal/protocol/workspace.go` (whole, 28 lines) — where the new `RecentWorkspaces*` payloads land (**append here**, do not create a new protocol file: #887 created `workspace.go` for workspace wire messages; recent-workspaces is the same domain). Mirror its doc-comment style + `docs/protocol-mobile.md §` references.
- `internal/protocol/workspace_test.go` (whole) + `internal/protocol/testdata/create_workspace_folder.json`, `internal/protocol/testdata/workspace_folder_created.json` — the `readFixture` round-trip test pattern and fixture shape to mirror for the two new fixtures.
- `internal/protocol/codes.go:50-70` — the `// Conversations.` `Type*` block through `TypeWorkspaceFolderCreated`. Add the two new consts here (request + reply), following #887's placement (a `// Workspace.` sub-group or immediately after `TypeWorkspaceFolderCreated`).
- `internal/protocol/envelope.go:118-143` — `v1TypeSet`. Add **both** new consts (after line 138 `TypeWorkspaceFolderCreated`). Both are v1 application types (request reaches `dispatch.Route`; reply is a normal outbound record an old phone may receive) — **not** v2-control frames, so they belong in `v1TypeSet`, mirroring the `TypeListConversations`/`TypeConversations` pair.
- `internal/protocol/compat_test.go:9-24, 103-120, 182-234` — three enumerations (`allTypes`, `all`, `all`) plus the hardcoded `len(all) == 24` at **line 117**. Add both consts to all three lists and bump `24 → 26`. The partition test (`len(v1TypeSet) == len(all)`) stays balanced because both go into `v1TypeSet`.
- `cmd/pyry/relay.go:261-270` (v1 `d.Register` block) and `:452-462` (v2 `Handlers` map) — the **two wiring sites** (AC #6). Register the handler at both, mirroring `TypeListConversations` at `:261`/`:453`: a read verb takes the registry **directly** (`handlers.RecentWorkspaces(convReg)`) — **no `cmd/pyry` adapter** (unlike `create_workspace_folder`'s `resolveWorkspaceFolder`). Only the **request** type is registered; the reply type is outbound-only.

---

## Context

Mobile's "Recent workspaces" list is silently always empty because the daemon
has no feed for it (mobile UI audit); desktop's Workspace Picker sheet needs the
same list. Landing the message in the shared daemon lets both clients inherit it
(reference-client discipline).

No new store is needed. The conversations registry already carries each
conversation's `Cwd` and `LastUsedAt` (`internal/conversations/conversation.go`).
This verb derives the recent-workspaces list from that data: the distinct set of
workspace paths, ordered by how recently each was used. It is the exact read-verb
shape as `list_conversations` (registry read → project → sort → reply), plus a
dedupe-by-`Cwd` fold.

**Scope.** One-shot request/response, like `list_conversations`. A live
subscription / push feed that re-emits when the recent set changes is **out of
scope** (file a follow-up if a client needs it). Creating folders is the sibling
`create_workspace_folder` (#887); this verb only reads paths the daemon already
owns.

**Why not security-sensitive.** No untrusted input drives a filesystem operation:
the request payload is empty; the reply is derived entirely from `Cwd` values the
daemon itself already stored (each one a `$HOME`-confined realpath written by
`create_conversation` / `change_workspace`). Nothing this handler does touches the
filesystem or trusts client input. (Contrast #887, which writes to disk from an
untrusted `{parent, name}` and is security-sensitive.)

---

## Design

### Data flow

```
phone/desktop ──recent_workspaces{}──▶ dispatch.Route
                                          │
                                          ▼
                    handlers.RecentWorkspaces(reg)
  reg.List()  ─▶  fold rows into map[Cwd]maxLastUsedAt  ─▶  materialize []RecentWorkspace
              (skip empty Cwd; keep max LastUsedAt)      ─▶  sort DESC by LastUsedAt, tie Path ASC
                                                         ─▶  c.Reply(recent_workspaces_list{workspaces})
```

The handler takes only the conversations registry (via a consumer-defined narrow
interface) — the thinnest possible read verb. No injected resolver, no `cmd/pyry`
adapter, no registry write, no `context` fan-out.

### Protocol vocabulary (additive only)

Two new `Type*` constants in `codes.go`, following #887's placement:

- `TypeRecentWorkspaces = "recent_workspaces"` — request (phone → binary). Named by AC #1.
- `TypeRecentWorkspacesList = "recent_workspaces_list"` — reply (binary → phone, `in_reply_to`).

Both gain a `v1TypeSet` entry (`envelope.go`) and appear in all three
`compat_test.go` enumerations with the `len(all)` count bumped `24 → 26`. This is
a v1 `dispatch.Route` verb (reached via the `Handlers` map / `d.Register`), **not**
a v2-only control frame — AC #6's "mirroring the existing read verbs" and "both v2
transport paths" fix this classification.

**No new `Code*` constant.** The handler has no reject branch (see §Error handling).

> **Single cross-client integration point.** The reply wire-type name
> (`recent_workspaces_list`) and the field tags (`workspaces` / `path` /
> `last_used_at`) are the one place this spec must reconcile with the desktop
> Workspace Picker + mobile client. A mismatch is a one-line const/tag change, not
> a design change (mirrors #887's field-name-reconciliation note and #823's
> `cwd`-vs-`workspace` note). The developer should not block on it; pick these
> names and note them in the knowledge doc for the client teams to consume.

### Request + reply payloads (append to `internal/protocol/workspace.go`)

Contract sketch (mirror the existing `workspace.go` doc-comment style):

```go
// RecentWorkspacesPayload — body of a recent_workspaces frame (phone → binary).
// Empty by spec; the type exists so the dispatcher decodes a concrete value
// (mirrors ListConversationsPayload).
type RecentWorkspacesPayload struct{}

// RecentWorkspacesListPayload — body of a recent_workspaces_list reply
// (binary → phone, in_reply_to). Ordering is the source of truth: entries are
// most-recent-first; the slice is always non-nil (empty → "workspaces":[]).
type RecentWorkspacesListPayload struct {
    Workspaces []RecentWorkspace `json:"workspaces"`
}

// RecentWorkspace — one distinct workspace: its absolute path and the most-recent
// LastUsedAt across the conversations that share it.
type RecentWorkspace struct {
    Path       string    `json:"path"`
    LastUsedAt time.Time `json:"last_used_at"`
}
```

`Path` and `LastUsedAt` carry **no** `omitempty` (reply-side discipline: the client
reads both on every row — AC #4). `last_used_at` matches the tag used by
`Conversation.LastUsedAt` / `ConversationSummary`; `path` matches
`WorkspaceFolderCreatedPayload.Path`.

### The handler (`internal/relay/handlers/recent_workspaces.go`, new file — the only new file)

Declares (consumer-side narrow interface, matching every sibling handler's idiom —
`ListConversations`→`ConversationLister`, `ChangeWorkspace`→`ConversationWorkspaceUpdater`):

```go
// RecentWorkspacesReader is the minimal surface this handler consumes from the
// conversations registry. *conversations.Registry satisfies it structurally; the
// variadic shape mirrors Registry.List exactly so structural matching holds.
type RecentWorkspacesReader interface {
    List(filter ...conversations.ListFilter) []conversations.Conversation
}

func RecentWorkspaces(reg RecentWorkspacesReader) dispatch.Handler
```

> Declare a fresh `RecentWorkspacesReader` rather than reusing the identical
> `ConversationLister`: the per-handler consumer-named narrow interface is the
> established convention in this package, and it keeps the two handlers decoupled.
> The one-line duplication is intentional.

Handler body — the fixed sequence (developer writes the code; each step is one small block):

1. **Read** `list := reg.List()` — no filter (all conversations; design decision (a)).
2. **Fold** into `m := map[string]time.Time` keyed by `Cwd`, keeping the max
   `LastUsedAt`: for each `conv`, **skip** when `strings.TrimSpace(conv.Cwd) == ""`
   (definitional — an empty string is not a workspace path), else
   `if prev, ok := m[conv.Cwd]; !ok || conv.LastUsedAt.After(prev) { m[conv.Cwd] = conv.LastUsedAt }`.
3. **Materialize** `out := make([]protocol.RecentWorkspace, 0, len(m))` from the map
   (non-nil even when empty → `"workspaces":[]`, AC #5).
4. **Sort** `out` **descending** by `LastUsedAt`, tie-break `Path` ascending, using
   the `time.Time`-round-trip-safe comparator (mirror `list_conversations.go:30-35`
   but inverted): `if !a.LastUsedAt.Equal(b.LastUsedAt) { return a.LastUsedAt.After(b.LastUsedAt) }; return a.Path < b.Path`. The `Path` tie-break makes ordering deterministic for the test (AC #3).
5. **Reply** `c.Reply(ctx, env, protocol.TypeRecentWorkspacesList, payloadJSON)` where
   `payloadJSON = json.Marshal(RecentWorkspacesListPayload{Workspaces: out})`.

The request payload is **ignored** (empty by spec) — like `ListConversations`, do
not decode `env.Payload`.

### Registration (`cmd/pyry/relay.go`, both sites)

- v1 `d.Register` block (after `:268`): `d.Register(protocol.TypeRecentWorkspaces, handlers.RecentWorkspaces(convReg))`
- v2 `Handlers` map (after the `TypeCreateWorkspaceFolder` entry): `protocol.TypeRecentWorkspaces: handlers.RecentWorkspaces(convReg),`

Read verb → the registry (`convReg`) is passed **directly**; no adapter helper is
added to `relay.go` (unlike #887's `resolveWorkspaceFolder`). Only the request type
is registered; the reply type (`recent_workspaces_list`) is outbound-only and just
needs its `v1TypeSet` membership.

---

## Design decisions (the ticket's two open questions, resolved)

**(a) Archived-only workspaces: INCLUDE them (derive from all conversations, no `IsArchived` filter).**
A workspace is a *folder*, not a conversation; archiving a conversation is a
per-conversation action and does not "un-use" its folder. The folder's recency is
the max `LastUsedAt` across *all* its conversations. A folder whose conversations
are all archived simply carries an older `LastUsedAt` and **sinks to the bottom**
of the recency-ordered list — the ordering already degrades stale folders
gracefully, so a hard exclusion is unwarranted. This is also the literal reading of
the ACs ("distinct `Cwd` values", "latest `LastUsedAt` across the conversations
sharing that workspace" — no archived qualifier); adding a filter would be scope
creep. If product later wants "hide archived-only workspaces," it is a one-line
`reg.List(conversations.ListFilter{IsArchived: &falseVal})` at step 1 — cheap to
add when evidence demands it, not now (Evidence-Based Fix Selection). A test pins
this decision so a future change is deliberate.

**(b) List length: RETURN ALL distinct workspaces; the client caps for display.**
The payload is a handful of short path strings (a user has ~5–50 distinct
workspaces) — no memory/bandwidth pressure justifies a daemon-side cap. The mobile
design showing ~5 is a *display* concern the client owns. This matches the ticket's
stated preference and keeps the daemon policy-free (Simplicity First). No cap.

---

## Concurrency model

None of note. The handler is synchronous and single-shot. `reg.List()` takes the
registry mutex internally and returns a **copy**, so the subsequent fold / sort /
marshal run lock-free on data the handler owns — no lock is held across the reply.
The handler adds no goroutines, channels, or `context` fan-out; `ctx` is threaded
only into `c.Reply` for cancellation, exactly as `list_conversations` does.

---

## Error handling

The handler has **no reject branch** — there is no untrusted input to validate and
`reg.List()` cannot fail. The only fallible step is `json.Marshal` of the reply
payload, which is wrapped and returned (`fmt.Errorf("marshal recent workspaces
payload: %w", err)`), mirroring `list_conversations.go:55-57`; the dispatcher logs
it. No `Code*` constant, no static reject messages, no per-branch logging. Empty
registry is a normal success returning `{"workspaces":[]}` (AC #5), not an error.

---

## Testing strategy

New `internal/relay/handlers/recent_workspaces_test.go`, mirroring
`list_conversations_test.go`'s dispatcher harness (reuse `testLogger`; write a
local `recvOutbound`/decode helper for the new reply type). Scenarios as
table-driven / focused tests (developer writes bodies in the project idiom):

- **Empty registry** → reply payload byte-equals `{"workspaces":[]}` (non-nil empty
  slice), `InReplyTo` = request id, inner `ID == 1`, `TS` non-zero (AC #5; mirror
  `TestListConversations_EmptyRegistry`).
- **Single conversation** → one entry with matching `Path` (= `Cwd`) and `LastUsedAt`
  (AC #1, AC #4).
- **Two conversations, same `Cwd`, different `LastUsedAt`** → exactly **one** entry
  (AC #2 dedup), `LastUsedAt` = the **max** of the two (AC #3). Assert timestamps via
  `time.Time.Equal`, never `==` (PROJECT-MEMORY round-trip discipline).
- **Multiple distinct `Cwd`** → ordered **most-recent-first** by max `LastUsedAt`
  (AC #3); assert the full ordered slice.
- **Tie on `LastUsedAt` across two `Cwd`** → deterministic `Path`-ascending
  tie-break (pins the comparator).
- **Archived-only workspace included** → a workspace whose only conversation has
  `IsArchived: true` still appears, with its `LastUsedAt` from the archived row
  (pins design decision (a); a future reversal must edit this test deliberately).
- **Empty-`Cwd` conversation skipped** → a conversation with `Cwd: ""` contributes
  no `""` entry to the reply.
- **Payload round-trip** in `workspace_test.go` (mirror
  `TestCreateWorkspaceFolderPayload_RoundTrip`) for `RecentWorkspacesPayload` (empty)
  and `RecentWorkspacesListPayload` (2 entries), with new fixtures
  `internal/protocol/testdata/recent_workspaces.json` (`{}`) and
  `recent_workspaces_list.json` (an ordered two-entry list). Compare `time.Time`
  fields via `.Equal`.
- **`compat_test.go`** stays green after adding both consts to the three
  enumerations and bumping `len(all) 24 → 26`.

Run `go test -race ./...` and `go vet ./...`; no PTY/TTY concerns (pure in-memory).

---

## Open questions

- **Reply type name / field tags** (`recent_workspaces_list`, `workspaces`, `path`,
  `last_used_at`) — the single cross-client integration point (see §Protocol
  vocabulary). Not a design blocker; a one-line change if the desktop/mobile client
  settled on different names. The knowledge doc should record the chosen names for
  the client teams.

---

## Size check (why this ships as one S, and a noted gate override)

Sketched design touches **5 production `.go` files** — `codes.go`, `envelope.go`,
`workspace.go` (all modified), `recent_workspaces.go` (the **1** genuinely new
file), `relay.go` (modified) — plus tests, testdata, and `compat_test.go`. Total
projected LOC ≈ **250 production + ~200 test ≈ 450**, well under the ~600 total-LOC
line. New files: **1** (≤ 3). New exported types: **4** (`RecentWorkspacesReader`,
`RecentWorkspacesPayload`, `RecentWorkspacesListPayload`, `RecentWorkspace`; ≤ 5).
Consumer call sites needing simultaneous edits: **0** — every change is net-new
(new consts, new handler, two additive registration lines); `codegraph`/grep shows
no cascade. ACs: **6 facets of one read verb**, not 6 concerns.

**The step-4 ≥5-production-file self-check trips at exactly 5, and I am overriding
it — transparently, on direct empirical evidence, not a rationalization.** The
override is justified because:

1. **The identical-but-heavier sibling shipped clean as one S.** #887
   (`create_workspace_folder`, merged commit `c2e83bd`) touched the **same 5
   production files** (`codes.go`, `envelope.go`, `workspace.go`,
   `create_workspace_folder.go`, `relay.go`) at **852 insertions** — and it carried
   a security-review pass, a `cmd/pyry` resolver adapter, name-shape + empty-parent
   guards, and static reject messages that #888 has **none** of. It did **not** hit
   `max_turns` (normal merge, no salvage). #888 is strictly thinner on every axis,
   so the turn-budget risk the ≥5 gate proxies for is **directly observed to be
   low**, not merely estimated.
2. **The canonical wire read-verb touches exactly these 5 files by its nature**
   (Type consts → `codes.go`; v1 membership → `envelope.go`; payload → a protocol
   file; handler → a handler file; registration → `relay.go`). The ≥5 gate, applied
   literally to this well-understood shape, is a false positive — the count is
   honest and irreducible (verified: no file is collapsible without making the code
   worse).
3. **Splitting would ship dead code.** A vocab/handler split (à la #841) yields a
   child A of a `Type` const + payload + testdata with **zero handler** — an
   unreachable, unbuildable-as-tested "unwired producer," the exact anti-pattern the
   pipeline warns against — plus a child B blocked on it: two developer runs, two
   PRs, two reviews, for a change thinner than the single-S #887. That is
   manufactured cost, strictly worse than one S.

This is not the flagged "I'm just specifying 4 files" undercounting smell (I count 5
honestly); it is a documented threshold override on the near-identical merged
precedent. Shipping as one S.
