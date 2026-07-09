# Spec #823 — `change_workspace` v2 wire verb

**Ticket:** [#823](https://github.com/pyrycode/pyrycode/issues/823) — `feat(wire): change_workspace v2 wire message`
**Size:** S · **Label:** `security-sensitive`
**Family:** 4th conversation write-verb after `rename_conversation` (#820), `archive`/`unarchive` (#881), `delete_conversation` (#822).

---

## Files to read first

The developer's turn-1 reading list. Read these before writing code; each line says what to extract.

- `internal/relay/handlers/rename_conversation.go` — **the closest template.** Clone its shape: decode → guard → `Registry.Update` closure that mutates one field **and snapshots the `ConversationUpdatedPayload` under the lock** → eager best-effort `Save` → `c.Reply(TypeConversationUpdated, …)`. Note lines 85–96: the snapshot is captured *inside* the closure to avoid a find-then-read TOCTOU; `LastUsedAt` is **not** bumped (a metadata edit is not a "use").
- `internal/relay/handlers/delete_conversation.go:58–104` — **the security-divergence template.** Its malformed branch logs `conn_id` ONLY (no `err`, no `conversation_id`). Copy that discipline; §"Error handling" below extends it with a second divergence unique to this verb.
- `internal/relay/handlers/create_conversation.go:44–190` — the **injected-validator pattern**: `SessionCreator` interface consumed by the handler, `ErrSpawnDirRejected` sentinel (line 51), and the `errors.Is(err, ErrSpawnDirRejected)` → non-retryable `protocol.malformed` mapping (lines 172–179). This verb mirrors that with a `WorkspaceResolver` func + `ErrWorkspaceRejected`. **Beware line 177:** create logs the wrapped `err` on rejection — this verb must NOT (it names the path). See §"Error handling".
- `cmd/pyry/main.go:442–476` — `confineWorkdirToHome` (strict, **non-creating**) + `withinDir`. The confiner to reuse verbatim. Returns the symlink-resolved realpath, or an error naming the offending path when it escapes `$HOME` or is unresolvable.
- `cmd/pyry/main.go:492–514` — `expandTilde`. Must run **before** confine: the phone can't know the daemon's `$HOME`, so a client may send `~/…`. `confineWorkdirToHome` does `filepath.Abs` (no tilde handling), so a raw `~/…` fed straight in mis-resolves.
- `cmd/pyry/main.go:602–644` — `resolveSpawnDir`: the create-path adapter (`expandTilde → confineWorkdirToHomeCreating → trustMark`). This verb's adapter is a **trimmed** variant — see §"The confine adapter" for the two deliberate omissions (creating-variant, trustMark).
- `cmd/pyry/relay.go:174–181` (v1 `d.Register` block) and `:363–372` (v2 `Handlers` map) — the **two wiring sites**. Register the new handler at both, mirroring rename/delete/archive.
- `internal/protocol/conversations_write.go:40–89` — payload-struct patterns (`Promote`/`Rename`/`Delete`/`Archive`). New payload slots in here.
- `internal/protocol/codes.go:50–90` — the `// Conversations.` `Type*` block. `TypeChangeWorkspace` goes after `TypeUnarchiveConversation` (line 90). No new `Code*` constant is needed (§"Protocol vocabulary").
- `internal/protocol/envelope.go:118–140` — `v1TypeSet`. Add one entry.
- `internal/protocol/compat_test.go` — three enumerations (lines 9–20, 101–112, 178–215) + the hardcoded count `21` (line 113). All bump by one.
- `internal/conversations/conversation.go:40–42` — the `Cwd` field. Its doc comment currently claims *"never updated after creation"* — this verb makes that false; fix the comment (§"Stale-comment fix").
- `internal/conversations/registry.go:184–198` — `Update(id, fn) bool` semantics (mutate-under-lock, `false` on miss).
- `internal/relay/handlers/delete_conversation_test.go` — the **test template**: reload-survival, static-message no-echo, and the malformed no-leak assertions (`findLogRecord` → assert no `err`/`conversation_id` field). Reuse its helpers.

---

## Context

A paired client (desktop Workspace Picker sheet; later mobile) needs to move a
conversation to a different workspace folder. Today mobile stubs the action
because the daemon has no wire verb; desktop is building its picker against this
message. Landing it in the shared daemon gives both clients one implementation.

**"Workspace" is the conversation's `Cwd` field.** This codebase has no separate
workspace-id concept, so the target is a filesystem path (mirroring `create` /
`promote` `cwd`), not an id.

**The one material difference from its siblings** — and why this verb is
`security-sensitive` where rename (stored a display string) and delete (stored
nothing) were not: this verb stores an **untrusted filesystem path** supplied by
a network-paired party. That path becomes the conversation's spawn working
directory on its next fresh session, so it must be **confined to `$HOME`** — the
same containment `create_conversation` applies to its spawn `cwd` — **before it
is stored**, fail-closed.

**Out of scope** (keeps it S, both confirmed by the ticket): re-spawning the
conversation's *live* session in the new workspace (`conv.Cwd` is deliberately
decoupled from a running session's captured spawn `WorkDir`, #685/#686 — this
verb changes the *recorded* workspace only; the new folder takes effect on the
next fresh spawn), and live fan-out to other connected clients (the reply goes
only to the requester, like the rest of the family).

---

## Design

### Data flow

```
phone/desktop ──change_workspace{conversation_id, cwd}──▶ dispatch.Route
                                                              │
                                                              ▼
                       handlers.ChangeWorkspace(reg, resolve, path, logger)
   decode ─▶ empty-cwd guard ─▶ resolve(confine to $HOME) ─▶ reg.Update(id):
                                                               cv.Cwd = realpath
                                                               snapshot payload
                                                          ─▶ reg.Save (best-effort)
                                                          ─▶ c.Reply(conversation_updated)
```

The resolver closure is constructed at the `cmd/pyry` boundary
(`resolveWorkspaceDir`, package `main`) and injected, so `internal/relay/handlers`
stays free of `cmd/pyry` imports — identical seam to create's `SessionCreator`.

### Protocol vocabulary (additive only)

- **One new `Type*` constant.** `TypeChangeWorkspace = "change_workspace"` in
  `codes.go`, in the `// Conversations.` block after `TypeUnarchiveConversation`.
  Add a doc comment matching the family's (a phone → binary `dispatch.Route`
  write verb, `v1TypeSet` member, reply reuses `conversation_updated`).
- **`v1TypeSet` gains one entry** (`envelope.go`). This is a v1 `dispatch.Route`
  write verb, **not** a v2-only control frame — same classification as
  #820/#822: "v2 wire message" in the title names the encrypted v2 *transport*,
  not the v1/v2 *type partition*. `dispatchAppFrame` only intercepts v2 control
  types; a request/reply verb reaches its handler via the `Handlers` map.
- **`compat_test.go`:** add `TypeChangeWorkspace` to all three v1 enumerations
  and bump the hardcoded `len(all)` count `21 → 22`. `TestTypeConstants_V1V2Partition`
  stays balanced because the constant is added to both `v1TypeSet` and the `all`
  list.
- **No new `Code*` constant, no new reply type, no new reply payload.** Reject
  branches reuse `CodeProtocolMalformed` and `CodeConversationNotFound`; the
  reply reuses `TypeConversationUpdated` / `ConversationUpdatedPayload` (the
  record still exists and only `cwd` changed — `ConversationUpdatedPayload.Cwd`
  already carries it). This verb is even leaner than delete, which needed a new
  reply type.

### Request payload (`conversations_write.go`)

```go
type ChangeWorkspacePayload struct {
    ConversationID string `json:"conversation_id"`
    Cwd            string `json:"cwd"`
}
```

Both fields value-typed and required (mirrors `PromoteConversationPayload`).
Deliberately **not** a reuse of `PromoteConversationPayload` (which also carries
a required `Name`) — semantic coupling / false dependency, exactly the rationale
the sibling payloads document.

**Wire field name** (`cwd` vs `workspace`): the family uses a `cwd` json tag and
"workspace" *is* the cwd here, so this spec uses `cwd`. Per the ticket this is
the single integration-reconcile point with the desktop client — a one-line
change to the tag if desktop settled on `workspace`, not a design blocker. See
§"Open questions".

### The handler (`internal/relay/handlers/change_workspace.go`)

New file. Declares (consumer-side, small-interface Go idiom):

```go
// WorkspaceResolver validates + canonicalises an untrusted target workspace
// path, returning the realpath confined to $HOME. Injected at the cmd/pyry
// boundary. A path escaping $HOME after symlink resolution, or unresolvable,
// is returned wrapping ErrWorkspaceRejected.
type WorkspaceResolver func(requested string) (resolved string, err error)

// ConversationWorkspaceUpdater is the minimal write surface consumed from the
// registry. *conversations.Registry satisfies it structurally.
type ConversationWorkspaceUpdater interface {
    Update(id conversations.ConversationID, fn func(*conversations.Conversation)) bool
    Save(path string) error
}

// ErrWorkspaceRejected marks a deterministic rejection of the target workspace
// (escapes $HOME / unresolvable). The cmd/pyry adapter wraps every confine
// failure with it; the handler maps any non-nil resolver error → non-retryable
// protocol.malformed. The sentinel documents intent + gives tests an errors.Is
// anchor; the handler need not discriminate on it (see handler step 3).
var ErrWorkspaceRejected = errors.New("conversation workspace directory rejected")

func ChangeWorkspace(reg ConversationWorkspaceUpdater, resolve WorkspaceResolver,
    registryPath string, logger *slog.Logger) dispatch.Handler
```

Plus four static message constants (`msgChangeWorkspaceMalformed`,
`…Empty`, `…Rejected`, `…NotFound`).

Handler body — the fixed order (each step's failure detailed in §"Error
handling"):

1. **Decode** `ChangeWorkspacePayload`; malformed → reject.
2. **Empty-cwd guard** (`strings.TrimSpace(p.Cwd) == ""`) → reject. **Must
   precede confine:** `confineWorkdirToHome("")` resolves to the process cwd
   (which may sit inside `$HOME` and *pass*), so an empty target would silently
   become the daemon's cwd rather than a clean malformed reject (AC #4).
3. **Confine:** `resolved, err := resolve(p.Cwd)`. Treat **any** non-nil `err` as
   the rejected branch (§"Error handling") — do **not** gate on
   `errors.Is(err, ErrWorkspaceRejected)`. The resolver's contract is that every
   failure wraps the sentinel and every failure is deterministic/non-retryable,
   so a bare `err != nil` is both correct and gap-free: it can never fall through
   to a hang or a spurious success if that wrap-everything contract is ever
   weakened. Pure validation — no filesystem mutation (see adapter below), so no
   partial state on any earlier failure.
4. **Mutate + snapshot under lock:**
   `hit := reg.Update(id, func(cv){ cv.Cwd = resolved; updated = ConversationUpdatedPayload{ID, IsPromoted, IsArchived, Name: cv.Name, Cwd: resolved, LastUsedAt: cv.LastUsedAt} })`.
   `!hit` → `conversation.not_found`. Snapshot captured inside the closure
   (mirrors rename); `LastUsedAt` **not** bumped (metadata edit); `Cwd`/`resolved`
   are value strings and `cv.Name` is a `*string` to an immutable heap string, so
   the copied snapshot is race-consistent after the lock releases.
5. **Eager `Save`** (best-effort; log-and-continue on error — non-fatal, exactly
   as rename/delete/create treat their Save).
6. **Reply** `conversation_updated` with the snapshot.

**Storage decision — store the resolved realpath, not the raw path.** AC #1 says
"updates … `cwd` to the **resolved** target." Three reasons this is the right
call, and a divergence from create (which stores the raw `p.Cwd`) that the
developer must **not** "fix" back:
1. *Validate == store.* For a security-sensitive verb, the persisted value is the
   one that was security-confined — no downstream reader has to trust that a raw
   path "was validated once."
2. *Robust for every downstream consumer.* The next fresh spawn re-runs
   `resolveSpawnDir` → `confineWorkdirToHomeCreating`, and #686's transcript
   resolver re-canonicalises `conv.Cwd` via `confineWorkdirToHome`; both are
   **idempotent** on an already-realpath (`EvalSymlinks(realpath) == realpath`).
   A raw `~/…` stored value, by contrast, would mis-resolve through the
   tilde-unaware `confineWorkdirToHome` those consumers call.
3. *Honest chip.* The desktop workspace chip shows the real location; in the
   common case (an absolute, symlink-free folder from the picker) the realpath
   equals what was sent, so there is no surprising transform.

### The confine adapter (`cmd/pyry`, package `main`)

`relay.go` is package `main`, so it calls `expandTilde` / `confineWorkdirToHome`
directly and injects the closure at both wiring sites — **no new parameter is
threaded** through `startRelay`/`startRelayV2` (contrast create's `SessionCreator`,
which is threaded only because it needs the `*sessions.Pool` from `main.go`'s
scope). Define one helper, co-located with its two call sites in `relay.go`:

```go
// resolveWorkspaceDir validates a paired client's requested workspace path for
// change_workspace: expandTilde then the STRICT confineWorkdirToHome (canonical
// realpath, confined to $HOME). Returns the realpath, or an error wrapping
// handlers.ErrWorkspaceRejected on any failure (escape / unresolvable).
func resolveWorkspaceDir(requested string) (string, error)
```

Two deliberate differences from `resolveSpawnDir`, both load-bearing:

- **Uses the strict `confineWorkdirToHome`, NOT `confineWorkdirToHomeCreating`.**
  This verb does not spawn and must not create a directory as a side effect of a
  metadata edit. A non-existent target is "unresolvable" (`EvalSymlinks` fails)
  → rejected, satisfying AC #3's "or is unresolvable" branch for free. The
  desktop picker offers only existing folders; if the folder is later needed, the
  next fresh spawn's `confineWorkdirToHomeCreating` creates it.
- **Does NOT call `trustMark`.** Trust-marking auto-accepts claude's
  workspace-trust modal and is a *spawn* concern; marking here would prematurely
  auto-trust a dir that may never be spawned into. The next fresh spawn's
  `resolveSpawnDir` trust-marks then. (Same reasoning #686 used to reuse
  `confineWorkdirToHome`, not `resolveSpawnDir`.)

Wrap **all** failures (both `expandTilde` and `confineWorkdirToHome`) as
`fmt.Errorf("%w: %v", handlers.ErrWorkspaceRejected, err)` so the handler sees the
sentinel on every rejection and maps it uniformly to non-retryable — there is no
transient failure mode here (unlike create's pool-mint), so every confine failure
is deterministic and non-retryable.

### Wiring (`relay.go`, both sites)

```go
handlers.ChangeWorkspace(convReg, resolveWorkspaceDir,
    resolveConversationsRegistryPath(instanceName), logger)
```

Registered under `protocol.TypeChangeWorkspace` in **both** the v1 `d.Register`
block (`:174–181`) and the v2 `Handlers` map (`:363–372`), mirroring rename /
delete / archive.

### Stale-comment fix (`conversation.go`)

The `Cwd` field comment reads *"Always present; never updated after creation."*
This verb updates it. Change the second clause to note that `change_workspace`
(#823) updates the recorded workspace. One comment edit — but a real correctness
fix: leaving a false immutability claim is a landmine for any future code that
assumes `Cwd` never changes.

---

## Concurrency model

No new goroutines. `*conversations.Registry` is the **single writer** (mutex-guarded
`Update`/`Save`; no reload-before-save — the daemon owns the file). The
snapshot-under-lock inside the `Update` closure is the only concurrency-sensitive
point and follows the rename precedent exactly: capturing the reply payload while
the row lock is held gives a consistent view even if a concurrent rename/archive
races immediately after. `Save`'s internal snapshot-copy-then-write (registry.go:72)
is unchanged.

---

## Error handling

Four reject branches, all **non-retryable**, all replying a **fixed static
string** (never the supplied path, id, or decode-error text). Logging discipline
is the crux of the security label — two divergences from the create template,
mandated below and enforced by tests.

| # | Condition | Wire code | Static msg | Log fields | Mutates? |
|---|-----------|-----------|-----------|------------|----------|
| 1 | Payload undecodable | `protocol.malformed` | `msgChangeWorkspaceMalformed` | **`conn_id` ONLY** | no |
| 2 | Empty / whitespace `cwd` | `protocol.malformed` | `msgChangeWorkspaceEmpty` | `conn_id` + `conversation_id` | no |
| 3 | Confine rejected (escape/unresolvable) | `protocol.malformed` | `msgChangeWorkspaceRejected` | **`conn_id` + `conversation_id` — NO `err`, NO path** | no |
| 4 | `conversation_id` matches no row | `conversation.not_found` | `msgChangeWorkspaceNotFound` | `conn_id` + `conversation_id` | no |

**Divergence 1 — malformed branch (inherited from delete #822).** On a decode
failure log `conn_id` ONLY. Drop both `err` (Go's `json.Unmarshal` errors embed
offending input bytes) and `conversation_id` (a decode failure leaves the struct
partially populated, so `p.ConversationID` may hold raw attacker bytes). Do NOT
copy rename's malformed branch, which logs both.

**Divergence 2 — confine-rejected branch (NEW to this verb; the load-bearing
one).** `create_conversation.go:177` logs the wrapped `err` on spawn-dir
rejection — and that `err` contains the offending path (`confineWorkdirToHome`'s
error names it). **This verb must NOT**, per AC #5 ("the rejected path is not
written to the daemon log in a way that echoes attacker-controlled bytes"). Log
`conn_id` + `conversation_id` only; never the confine `err`, never the path.
`conversation_id` is safe here (a decode-success, slog-escaped structured field,
consistent with delete's not-found branch). **This is the single most likely spot
for the developer to regress by pattern-matching create — call it out in the
handler's SECURITY doc comment.**

Branches 2–4 are decode-success paths, so `conversation_id` is a properly-decoded
structured field (not partial attacker bytes) and is safe to log — matching the
reviewed rename/delete/create precedent. The success branch and the `Save`-failure
branch log `conversation_id` (proven-real) and, for `Save` failure, the save
`err` (a filesystem error naming the *registry* path, not attacker bytes — safe).

**Ordering guarantees no partial mutation:** confine (step 3, side-effect-free
because it uses the non-creating variant) runs before `Update` (step 4, the only
mutator, whose closure runs only on a hit). Any reject leaves the registry
byte-identical.

---

## Testing strategy

New `internal/relay/handlers/change_workspace_test.go`, modelled on
`delete_conversation_test.go` (reuse its `newDeleteConvConn`-style helpers,
`assertErrorPayload`, `findLogRecord`). The handler test injects a **fake
`WorkspaceResolver`** (a closure) — hermetic, no real filesystem: an "accept"
fake returns a canonical path; a "reject" fake returns
`("", fmt.Errorf("%w: outside home", ErrWorkspaceRejected))`. Scenarios (bulleted
inputs → expected behaviour; the developer writes them table-driven in the
project idiom):

- **Success updates workspace, replies, persists.** Valid id + accept-resolver →
  `conversation_updated` reply (`in_reply_to` correlated) whose `cwd` equals the
  resolver's returned realpath; in-memory `conv.Cwd` updated; a fresh `Load` from
  the same path shows the new `cwd` (restart-survival, AC #1). `LastUsedAt`
  unchanged.
- **`list_conversations` reflects the new workspace** (AC #2) with no list-handler
  change — run `ListConversations` after and assert the row's `cwd` is the new
  value.
- **Confine rejected leaves state unchanged + no path leak** (AC #3, AC #5). Valid
  id + reject-resolver + a payload `cwd` carrying an injected marker → non-retryable
  `protocol.malformed`, static message (no marker), `conv.Cwd` unchanged; and the
  `change_workspace.rejected` log record carries **no `err` field, no path/marker
  anywhere**, only `conn_id` + `conversation_id`. This is the security-critical
  test — assert the log record explicitly, like delete's malformed test.
- **Not-found leaves registry unmodified** (AC #4). Unknown id + accept-resolver
  (so confine passes, then `Update` misses) → `conversation.not_found`,
  non-retryable, seeded row untouched.
- **Malformed no-leak** (AC #4, AC #5). Truncated payload carrying a marker →
  `protocol.malformed`, static message; log record has **no `err`, no
  `conversation_id`**, `conn_id` present; registry untouched.
- **Empty `cwd` → malformed, no mutation** (AC #4). `cwd: ""` → `protocol.malformed`
  (`msgChangeWorkspaceEmpty`), non-retryable, row untouched, confine never invoked.
- **No-echo static messages.** Injected-looking id/path never appears in any reject
  reply `Message`.

Plus a small `cmd/pyry` test for `resolveWorkspaceDir` (mirrors any existing
`resolveSpawnDir`/confine test): an in-`$HOME` existing dir → returns its realpath;
a path escaping `$HOME` (e.g. via `os.MkdirTemp` outside home, or a symlink out) →
error satisfying `errors.Is(err, handlers.ErrWorkspaceRejected)`; a non-existent
target → same rejection (unresolvable). The strict-confine escape/symlink logic
itself is already covered by `confineWorkdirToHome`'s own tests — this test only
pins the tilde-expand + sentinel-wrap wrapper.

`compat_test.go` gains `TypeChangeWorkspace` in its three v1 lists and the count
bump; `go test -race ./...`, `go vet`, `staticcheck` must stay green.

---

## Scope / size note (documented family false-positive)

Production `.go` files touched: **6** —
`codes.go` (1 const), `envelope.go` (1 `v1TypeSet` line), `conversations_write.go`
(1 struct), `conversation.go` (1 comment fix), `change_workspace.go` (new handler),
`relay.go` (the `resolveWorkspaceDir` helper + 2 wiring lines). Three are
single-package protocol one-liners; one is a comment-only correctness fix.

This is the **known, documented false-positive** for the conversation-write-verb
family. Twins #820 (rename) and #822 (delete) each shipped this exact
vocab-spread shape as one `S` and did not exhaust the developer budget. This
verb adds only one reused primitive (`confineWorkdirToHome`, absorbed into
`relay.go`'s `resolveWorkspaceDir`) — the "one material difference." Against the
red lines: 1 new file, ~140 production LOC / ~420 total, 3 new exported types,
**0** consumer-cascade call sites (purely additive — no existing symbol changes
signature), 4 reject branches, 5 ACs that are facets of one handler. Ships as one
`S`, as PO ruled. No split.

---

## Open questions

- **`cwd` vs `workspace` json tag.** This spec uses `cwd` (family convention;
  "workspace" is the cwd). If the desktop client settled on `workspace`, it is a
  one-line tag change on `ChangeWorkspacePayload` at integration — resolve with
  the desktop client, not a design blocker (the ticket flags this explicitly).
- **`docs/protocol-mobile.md` § change_workspace.** The wire-doc section is a
  documentation-phase deliverable (written from this spec + the merged diff after
  the PR lands), **not** a developer AC — the developer's worktree mutates only
  code, tests, and this spec file. The payload doc comment may reference the
  section aspirationally, as the sibling payloads do.

---

## Security review

**Verdict:** PASS

This ticket carries the `security-sensitive` label, so this adversarial pass over
the spec is mandatory. **`change_workspace` is the first verb in the
conversation-write-verb family where untrusted client input reaches a filesystem
path** — the exact `[File operations]` surface both direct twins explicitly marked
**N/A** (#820 rename stored a display string; #822 delete stored nothing). That
category is therefore walked here as an *active* surface, not waved off. The pass
was run against the staged implementation on `feature/823` (PR #886), so each
finding cites the real anchor.

**Findings:**

- **[Trust boundaries]** No MUST-FIX. Single explicit boundary: the handler
  `ChangeWorkspace`, one `json.Unmarshal` of the phone-supplied
  `ChangeWorkspacePayload` (`change_workspace.go:117`). Its two untrusted fields
  are each contained at a named point. `conversation_id` is used *only* as an
  exact-match registry key inside `Update` (byte comparison, no path/argv/query
  construction — a miss is `conversation.not_found`; a phone cannot mint or
  collide an id, only name one that already exists). `cwd` — **the new,
  label-earning field** — crosses from "untrusted network bytes" to "filesystem
  path" through exactly one function, `resolveWorkspaceDir` (`relay.go:98`), before
  it is ever stored; the handler holds no raw path after that call (it stores the
  resolver's returned `resolved` realpath, `change_workspace.go:173/179`). Within a
  server-id, paired devices are one trust domain (ADR 025 § Security model): any
  paired client may change any conversation's workspace, consistent with
  `list`/`create`/`promote`/`rename`/`delete`. The tenant boundary is the
  server-id, enforced structurally at the Noise IK handshake (unpaired → 4401,
  never reaches `dispatchAppFrame`).

- **[Tokens/secrets]** N/A — the verb mints, stores, and compares no token or
  secret; mints no id (no `crypto/rand` surface).

- **[File operations]** No MUST-FIX — **the active surface for this verb**, and the
  reason it carries the label. The untrusted `cwd` becomes the conversation's
  future spawn workdir, so it is confined to `$HOME` **before storage**, fail-closed,
  by `resolveWorkspaceDir` → `confineWorkdirToHome` (`main.go:455`). Walked
  sub-surface by sub-surface:
  - *Path traversal.* `confineWorkdirToHome` runs `filepath.Abs` then
    `filepath.EvalSymlinks` and tests containment with `withinDir` (`main.go:481`),
    which uses `filepath.Rel` + a `..`-boundary check — so `../../etc` cleans and
    resolves to a real path outside `$HOME` and is rejected, and a sibling like
    `/home/userfoo` is not mistaken for inside `/home/user` (no prefix-match bug,
    the #118/#221 gotcha).
  - *Symlink escape.* Containment is tested on the **fully symlink-resolved
    realpath of the target** (`EvalSymlinks`, `main.go:468`), not on the raw
    string — a `~/link-to-etc` resolves to `/etc` and is rejected. Both sides
    (`$HOME` and target) are canonicalised before the test, so a symlinked home
    yields no false reject.
  - *TOCTOU (store → next-spawn).* This is the file-op TOCTOU the twins never had.
    It is closed by two design choices, both implemented: (1) the handler stores
    the **resolved realpath, not the raw path** (`change_workspace.go:173`), so a
    symlink swapped in *after* validation cannot redirect a later reader that
    trusts the stored value; (2) this verb writes **metadata only** — it performs
    no spawn (`resolveWorkspaceDir` uses the strict, *non-creating*
    `confineWorkdirToHome`, never `…Creating`, and never `trustMark`), so the
    confinement here has no filesystem side effect and the *authoritative* gate is
    re-applied at the next fresh spawn, where `resolveSpawnDir` →
    `confineWorkdirToHomeCreating` re-confines the stored realpath (idempotent:
    `EvalSymlinks(realpath) == realpath`; a component swapped to escape `$HOME`
    is re-caught then). Confinement is thus enforced at *both* store time and the
    only dangerous-use time. Moving a **live** session into the new dir is
    explicitly OUT OF SCOPE (see [Threat model alignment]).
  - *Unresolvable / non-existent target.* The strict non-creating confiner makes
    `EvalSymlinks` fail on a missing path → rejected, satisfying AC #3's "or is
    unresolvable" branch for free and guaranteeing the verb never creates a
    directory as a side effect of a metadata edit.
  - *Empty-path footgun.* `confineWorkdirToHome("")` resolves to the daemon's
    process cwd (which may sit inside `$HOME` and *pass*); the handler's
    empty/whitespace guard (`change_workspace.go:132`) runs **before** confine,
    so a blank target is a clean `protocol.malformed` reject, never a silent
    "store the daemon's cwd."
  - *Tilde.* `expandTilde` (`main.go:502`) runs **before** confine so a phone's
    `~/…` (it cannot know the daemon's absolute `$HOME`) anchors at the real home,
    not under the process cwd; `~user` is deliberately *not* resolved to another
    user's home — it passes through literally and fails the later
    confine/existence check as a deterministic reject.
  - *Permissions / atomicity.* This verb creates no file. Persistence reuses
    `conversations.Registry.Save` (atomic temp-file `0600` + `0700` dir + fsync +
    rename), unchanged from the twins — no new stat-then-open, no new file mode.

- **[Subprocess / external command execution]** N/A for this handler — it executes
  no subprocess and passes no value to `exec.Command`. This is a strict
  attack-surface *reduction* versus `create_conversation`, which mints a claude
  session with a phone-influenced cwd; `change_workspace` deliberately does **not**
  spawn (Out of Scope: live respawn). The eventual spawn that consumes the stored
  `Cwd` is a *future fresh session* that re-runs the full `resolveSpawnDir`
  confinement + trust-mark — no path this verb stores reaches an `exec.Command`
  argument un-re-validated.

- **[Cryptographic primitives]** N/A — no crypto in the handler; AEAD framing is
  the unchanged v2 transport's concern.

- **[Network, I/O & DoS]** No MUST-FIX. The handler reads no socket; inbound frame
  size is capped by the v2 transport decoder upstream, transitively bounding both
  `conversation_id` and the `cwd` string length. No explicit application-level
  path-length cap — acceptable because the verb *replaces* one registry field
  rather than accumulating (anti-amplifying), fans out to no one (reply goes only
  to the requester), and a miss is a bounded linear scan over a small per-user
  registry. `EvalSymlinks` on an attacker path walks only real on-disk components
  within/around `$HOME` — bounded by the filesystem, not attacker-inflatable into
  amplification. No HTTP/WS/TLS surface added.

- **[Error messages, logs, telemetry]** No MUST-FIX — the co-crux of the label with
  [File operations]. (1) All **four** reject branches reply with a fixed static
  string constant (`change_workspace.go:20/28/35/40`) — no supplied bytes (the
  path, the id, or a decode-error fragment) reach the wire (AC #5 wire half). (2)
  Two deliberate logging divergences from the create/rename template, both
  implemented and both called out in the handler's SECURITY doc comment so a future
  editor does not "fix" them back by pattern-matching create:
  - *Malformed branch* (inherited from delete #822, `change_workspace.go:121`):
    logs **`conn_id` only** — not the decode `err` (Go's `json.Unmarshal` errors
    can embed offending input bytes) and not `conversation_id` (a decode failure
    leaves the struct at most partially populated, so `p.ConversationID` may hold
    raw attacker bytes).
  - *Confine-rejected branch* (NEW, the load-bearing one, `change_workspace.go:154`):
    `create_conversation.go` logs the wrapped confine `err` on rejection — and that
    `err` **names the offending path**. This handler must NOT, per AC #5; it logs
    `conn_id` + `conversation_id` only, never the confine `err`, never the path.
    `conversation_id` here is a decode-*success*, slog-escaped structured field
    (consistent with delete's not_found branch) and is safe.
  The empty, not_found, save-failure, and success branches log `conversation_id` as
  a structured slog field (proven-decoded, non-secret opaque id) per the reviewed
  rename/delete/create precedent; the save-failure branch additionally logs the
  save `err`, which names the *registry* path (a filesystem error), not attacker
  bytes. No telemetry.

- **[Concurrency]** No finding. No new goroutines. `*conversations.Registry` is the
  single writer (`Update`/`Save` each take `r.mu`; no reload-before-save — the
  daemon owns the file). The `cv.Cwd` set **and** the reply-payload snapshot both
  happen inside one locked `Update` closure (`change_workspace.go:172`), so there is
  no find-then-mutate or find-then-read window and no second lock acquisition (no
  ordering concern). `Save` snapshots under the lock internally, so any interleaving
  with a concurrent rename/archive persists a consistent state. Replay is harmless:
  a replayed `change_workspace` re-sets the identical realpath (`Update` is
  idempotent for a fixed target) — so no nonce/idempotency key is needed (contrast
  the modal verbs). **Shutdown mid-op:** a kill after the in-memory `Update` but
  before `Save` reverts the row to its prior `Cwd` on restart — the accepted
  best-effort durability window shared by rename/delete/create/sweep; AC #1's
  restart-survival is asserted only on the Save-succeeded success path.

- **[Threat model alignment]** Untrusted-phone-path-input, cross-tenant, and
  path-traversal/symlink-escape threats are in scope and addressed above. Two
  concerns are explicitly OUT OF SCOPE, each named with who picks it up: (1)
  **re-spawning the conversation's *live* session in the new workspace** —
  `conv.Cwd` is deliberately decoupled from a running session's captured spawn
  `WorkDir` (#685/#686); this verb changes the *recorded* workspace only, and a
  swap only takes effect on the next fresh spawn (which re-confines). Moving a live
  session (teardown + re-mint + trust-mark the new dir) is a separate, larger
  ticket. (2) **Live fan-out to other connected clients** — the reply goes only to
  the requester, matching the rest of the family; other clients inherit the new
  workspace on their next `list_conversations`. A live broadcast is a future
  ticket.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-09
