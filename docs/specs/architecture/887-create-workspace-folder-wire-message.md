# Spec #887 — `create_workspace_folder` v2 wire message

**Ticket:** [#887](https://github.com/pyrycode/pyrycode/issues/887) — `feat(wire): create_workspace_folder v2 wire message`
**Size:** S · **Label:** `security-sensitive`
**Family:** a v1TypeSet `dispatch.Route` request/reply verb, cousin of the conversation write-verbs (`create_conversation` #685, `change_workspace` #823). Split from the #825 daemon-wire epic; the sibling `recent_workspaces` (#888) is independent.

---

## Files to read first

The developer's turn-1 reading list. Read these before writing code; each line says what to extract.

- `internal/relay/handlers/change_workspace.go` (whole file, ~215 lines) — **the closest template.** Clone its shape at a coarse level: consumer-side injected resolver + `Err*Rejected` sentinel + static message constants + decode→guard→resolve→reply, and especially its SECURITY doc-comment discipline (no-path-in-logs). **Diverge in three ways** (see §Design): this verb takes `{parent, name}` not `{conversation_id, cwd}`, touches **no conversations registry** (no `Update`/`Save`, no `ConversationWorkspaceUpdater`), and replies with a **new** `workspace_folder_created` type (not the reused `conversation_updated`).
- `internal/relay/handlers/create_conversation.go:86–232` — the **reply-with-a-new-type template** (`c.Reply(ctx, env, protocol.TypeConversationCreated, payloadJSON)`, line 231) and the injected-validator seam (`SessionCreator`, `ErrSpawnDirRejected`). Note **line 118–124**: create logs the decode `err` on malformed — this verb must NOT (see §Error handling, divergence).
- `cmd/pyry/main.go:535–600` — **`confineWorkdirToHomeCreating`, the primitive to reuse verbatim.** Extract the two load-bearing guarantees the ACs lean on: **containment check #1 is PRE-creation** (line 575–587: escape → reject *before* any `MkdirAll`, so AC #2 "creates nothing on escape" holds for free); and **`MkdirAll(…, 0o700)` runs only when `rest != ""`** (line 583–587), so an already-existing full path skips creation and returns `EvalSymlinks(candidate)` (AC #5 idempotency, AC #4 canonical realpath).
- `cmd/pyry/main.go:602–644` — `resolveSpawnDir` (the create-path adapter: `expandTilde → confineWorkdirToHomeCreating → trustMark`). This verb's adapter is a **hybrid**: creating like `resolveSpawnDir`, but **no `trustMark`** like `change_workspace`'s `resolveWorkspaceDir` (§The confine adapter).
- `cmd/pyry/relay.go:69–108` — `resolveWorkspaceDir` (#823's adapter). The new `resolveWorkspaceFolder` is co-located here and follows the same "wrap every failure with the handler sentinel via `%w: %v`" contract, swapping the **non-creating** confiner for the **creating** one and adding a `filepath.Join`.
- `cmd/pyry/main.go:492–514` — `expandTilde`. Runs on the **parent** before the join (a paired client can't know the daemon's `$HOME`, so it may send `~/…`).
- `cmd/pyry/relay.go:215–223` (v1 `d.Register` block) and `:405–415` (v2 `Handlers` map) — the **two wiring sites**. Register the new handler at both, mirroring `change_workspace` at `:221` / `:412`.
- `internal/protocol/conversations_write.go:1–45` — payload doc-comment style; `PromoteConversationPayload` (value-typed required string fields — the shape to mirror, since `parent`/`name`/`path` are all required, not spec-optional-null).
- `internal/protocol/conversations_write_test.go:1–60` — the `readFixture(t, "<type>.json")` round-trip pattern. The new payloads get a `workspace_test.go` mirroring `TestCreateConversationPayload_RoundTrip` + two `testdata/*.json` fixtures.
- `internal/protocol/codes.go:50–103` — the `// Conversations.` `Type*` block ending at `TypeChangeWorkspace`. Add the new request + reply consts (§Protocol vocabulary).
- `internal/protocol/envelope.go:118–141` — `v1TypeSet`. Add **two** entries (request + reply), mirroring the `TypeCreateConversation` / `TypeConversationCreated` pair.
- `internal/protocol/compat_test.go:9–21, 101–117, 180–218` — three enumerations + the hardcoded count `22` at line 115. All bump by **two** (22 → 24).
- `cmd/pyry/main.go:481–491` — `withinDir` (the `filepath.Rel` + `..`-boundary containment predicate). Reading it confirms why the confiner rejects `../etc` and does not prefix-confuse `/home/userfoo` with `/home/user`; not edited.

---

## Context

Mobile stubs a create-folder affordance and desktop is building a Create-folder
dialog inside its Workspace Picker sheet, but neither client can act because the
daemon has no wire message for it (mobile UI audit; desktop main note
2026-07-04). Landing the message in the shared daemon lets both clients inherit
it (reference-client discipline).

The daemon already knows how to create a directory confined to the operator's
`$HOME`: `confineWorkdirToHomeCreating` (`cmd/pyry/main.go:535`, from #696/#685)
canonicalises both the candidate and `$HOME` via `EvalSymlinks`, rejects anything
that escapes `$HOME` **before** creating anything, and `MkdirAll`s the directory
if missing. This ticket wires that primitive to a new `create_workspace_folder`
request/reply verb.

**The security centrepiece.** The request carries an untrusted parent path **and**
folder name from a network-paired client, and the handler **writes to the host
filesystem**. Two distinct properties must hold, fail-closed:

1. **The resolved target is confined to `$HOME`** (path traversal / symlink escape)
   — enforced by `confineWorkdirToHomeCreating`, which rejects an escape before any
   `MkdirAll`.
2. **The new folder lands *directly* under the supplied parent, never in a nested
   subpath** — enforced by validating the folder *name* is a single clean path
   element (no separator, no `..`, not absolute) before the join. Confinement alone
   does **not** give this: a name like `sub/dir` stays inside `$HOME` yet is not
   directly under the parent, so it must be rejected independently.

**Out of scope** (keeps it S): starting a conversation in the new folder (that is
`create_conversation`'s job, which re-confines and trust-marks its spawn dir);
trust-marking the folder (deferred to that first spawn — see §The confine adapter);
listing/recent-workspaces (sibling #888).

---

## Design

### Data flow

```
phone/desktop ──create_workspace_folder{parent, name}──▶ dispatch.Route
                                                            │
                                                            ▼
                    handlers.CreateWorkspaceFolder(resolve, logger)
  decode ─▶ empty-parent guard ─▶ name-shape guard ─▶ resolve(parent, name):
                                                        expandTilde(parent)
                                                        Join(parent, name)
                                                        confineWorkdirToHomeCreating
                                                     ─▶ c.Reply(workspace_folder_created{path})
```

The resolver closure is constructed at the `cmd/pyry` boundary
(`resolveWorkspaceFolder`, package `main`) and injected, so
`internal/relay/handlers` stays free of `cmd/pyry` imports — the same seam
`change_workspace` uses for `WorkspaceResolver` and `create_conversation` for
`SessionCreator`. **No new parameter is threaded through `startRelay`/`startRelayV2`**
(the resolver has no external state — `expandTilde` / `confineWorkdirToHomeCreating`
are package-`main` with no `*Pool`/`*Registry` dependency — so `relay.go` references
it directly at both sites, exactly as it does `resolveWorkspaceDir`).

**This handler consumes no registry.** Unlike every conversation write-verb, there
is no conversation row to mutate: it creates a folder and returns its path. So it
takes only the injected `resolve` + `logger` — the thinnest handler in the family.

### Protocol vocabulary (additive only)

- **Two new `Type*` constants** in `codes.go`, in the `// Conversations.` block
  after `TypeChangeWorkspace` (or a fresh `// Workspace.` sub-group — developer's
  call), each with a doc comment matching the family's (a phone → binary
  `dispatch.Route` verb, `v1TypeSet` member; the "v2 wire message" in the title
  names the encrypted v2 *transport*, not the v1/v2 *type partition*):
  - `TypeCreateWorkspaceFolder = "create_workspace_folder"` (request)
  - `TypeWorkspaceFolderCreated = "workspace_folder_created"` (reply)
- **`v1TypeSet` gains two entries** (`envelope.go`) — request and reply, mirroring
  the `TypeCreateConversation` / `TypeConversationCreated` pair. This is a v1
  `dispatch.Route` verb (reached via the `Handlers` map / `d.Register`), **not** a
  v2-only control frame intercepted by `dispatchAppFrame`. AC #6's "mirroring the
  existing conversation write verbs" and "both `d.Register` sites" fix this
  classification.
- **`compat_test.go`:** add both constants to all three enumerations
  (`TestIsV1Compatible`'s `allTypes`, `TestV1TypeSet_Covers…`'s `all`,
  `TestTypeConstants_V1V2Partition`'s `all`) and bump the hardcoded `len(all)` count
  `22 → 24`. The partition test stays balanced because both go into `v1TypeSet`.
- **No new `Code*` constant.** Reject branches reuse `CodeProtocolMalformed`.

### Request + reply payloads (`internal/protocol/workspace.go`, new file)

New file (the protocol package is one-file-per-concern:
`conversations_write.go`, `messaging.go`, `push.go`, `settings.go`, … — a
workspace-folder verb is not a conversation verb, so it earns its own file rather
than crowding `conversations_write.go`).

```go
// CreateWorkspaceFolderPayload — body of a create_workspace_folder frame
// (phone → binary). Both fields required, value-typed (mirrors PromoteConversationPayload).
type CreateWorkspaceFolderPayload struct {
    Parent string `json:"parent"`
    Name   string `json:"name"`
}

// WorkspaceFolderCreatedPayload — body of a workspace_folder_created reply
// (binary → phone, in_reply_to). Path is the canonical symlink-resolved
// absolute path of the created folder.
type WorkspaceFolderCreatedPayload struct {
    Path string `json:"path"`
}
```

Field-name reconciliation (`parent`/`name`/`path`) with the desktop client is the
single integration point — a one-line tag change if desktop settled on different
tags, not a design blocker (see §Open questions), mirroring #823's `cwd`-vs-`workspace`
note.

### The handler (`internal/relay/handlers/create_workspace_folder.go`, new file)

Declares (consumer-side, small-interface Go idiom):

```go
// WorkspaceFolderResolver validates + creates the target folder under an
// untrusted parent path, returning the created folder's realpath confined to
// $HOME. Injected at the cmd/pyry boundary. Any failure (escape, unresolvable,
// mkdir error) is returned wrapping ErrWorkspaceFolderRejected.
type WorkspaceFolderResolver func(parent, name string) (created string, err error)

// ErrWorkspaceFolderRejected marks a deterministic rejection of the target
// folder (escapes $HOME / unresolvable / cannot be created). The cmd/pyry
// adapter wraps every failure with it; the handler maps any non-nil resolver
// error → non-retryable protocol.malformed. Gives tests an errors.Is anchor.
var ErrWorkspaceFolderRejected = errors.New("workspace folder rejected")

func CreateWorkspaceFolder(resolve WorkspaceFolderResolver, logger *slog.Logger) dispatch.Handler
```

Plus four static message constants (`msgCreateWorkspaceFolderMalformed`,
`…EmptyParent`, `…BadName`, `…Rejected`).

Handler body — the fixed order (each step's failure detailed in §Error handling):

1. **Decode** `CreateWorkspaceFolderPayload`; error → malformed reject.
2. **Empty-parent guard** (`strings.TrimSpace(p.Parent) == ""`) → reject. **Must
   precede resolve:** `filepath.Join("", name)` = `name`, then
   `confineWorkdirToHomeCreating` runs `filepath.Abs` = process cwd, which may sit
   inside `$HOME` and *pass* — an empty parent would silently create the folder
   under the daemon's cwd. Same footgun `change_workspace` guards (main.go empty-cwd).
3. **Name-shape guard (AC #3)** — reject when the name is not a single clean path
   element: `strings.TrimSpace(p.Name) == ""` **or** `filepath.IsAbs(p.Name)` **or**
   it contains a path separator (`strings.ContainsRune(p.Name, '/')`; the daemon is
   Linux/macOS-only so `/` is the separator) **or** it contains `..`
   (`strings.Contains(p.Name, "..")`). This is the deterministic guarantee that the
   folder lands *directly* under the parent (§Context property 2). Pure string
   validation — testable without a filesystem, and independent of the confiner's
   escape check (belt-and-suspenders, **different fabric**: name-shape is one
   deterministic code check, confinement is a second).
4. **Resolve** `created, err := resolve(p.Parent, p.Name)`. Any non-nil `err` → the
   rejected branch (§Error handling). Bare `err != nil` — do **not** gate on
   `errors.Is(err, ErrWorkspaceFolderRejected)`: the resolver's contract wraps every
   failure with the sentinel and every failure is deterministic/non-retryable, so a
   bare check is both correct and gap-free. On escape, the resolver's confiner
   creates nothing (AC #2); on an existing folder it returns that folder's realpath
   (AC #5); the returned `created` is the canonical realpath (AC #4).
5. **Reply** `workspace_folder_created` with `WorkspaceFolderCreatedPayload{Path: created}`.

### The confine adapter (`cmd/pyry/relay.go`, package `main`)

Co-located with `resolveWorkspaceDir`. One helper, referenced directly at both
wiring sites — no `startRelay` parameter threaded:

```go
// resolveWorkspaceFolder validates + creates a paired client's requested
// workspace folder for create_workspace_folder: expandTilde(parent) then
// filepath.Join(expandedParent, name) then confineWorkdirToHomeCreating
// (canonical realpath, confined to $HOME, created if missing). Returns the
// created realpath, or an error wrapping handlers.ErrWorkspaceFolderRejected on
// any failure. The caller (handler) has already validated `name` is a single
// clean path element, so the join lands directly under the parent.
func resolveWorkspaceFolder(parent, name string) (string, error)
```

**A hybrid of the two existing adapters — both choices load-bearing:**

- **Uses `confineWorkdirToHomeCreating` (creating), like `resolveSpawnDir` — NOT the
  strict `confineWorkdirToHome`.** This verb's whole purpose is to create the folder
  (AC #1/#5). The creating confiner's containment check #1 runs **before** `MkdirAll`,
  so an escaping target is rejected with nothing created (AC #2); `MkdirAll` is
  idempotent (AC #5); it returns the post-creation `EvalSymlinks` realpath (AC #4).
- **Does NOT call `trustMark`, like `resolveWorkspaceDir` — UNLIKE `resolveSpawnDir`.**
  Creating a folder is not spawning into it. Trust-marking auto-accepts claude's
  workspace-trust modal and is a *spawn* concern; marking an empty folder that may
  never host a session would prematurely auto-trust it. The eventual
  `create_conversation` into this folder re-runs `resolveSpawnDir` →
  `confineWorkdirToHomeCreating` (idempotent on the now-existing realpath) + `trustMark`
  then. Same reasoning #686/#823 used to reuse the confiner rather than `resolveSpawnDir`.

Wrap **all** failures (from `expandTilde` and `confineWorkdirToHomeCreating`) as
`fmt.Errorf("%w: %v", handlers.ErrWorkspaceFolderRejected, err)`. Note this folds a
rare `MkdirAll` filesystem error (EACCES/ENOSPC on an in-`$HOME` path) into the same
non-retryable reject as an escape — this is **exactly what `resolveSpawnDir` already
does** for the create path (main.go:635–638: it wraps every `confineWorkdirToHomeCreating`
failure as `ErrSpawnDirRejected`), so it is the established codebase posture, not a new
compromise. The dominant, security-relevant failure is the escape; a retry won't fix a
full disk anyway.

### Wiring (`relay.go`, both sites)

```go
handlers.CreateWorkspaceFolder(resolveWorkspaceFolder, logger)
```

Registered under `protocol.TypeCreateWorkspaceFolder` in **both** the v1 `d.Register`
block (`:215–223`) and the v2 `Handlers` map (`:405–415`), mirroring `change_workspace`.

---

## Concurrency model

No new goroutines; no shared mutable state. The handler holds no registry and no
lock — each invocation is a self-contained decode → validate → `MkdirAll` → reply on
the per-conn goroutine. `os.MkdirAll` is safe under concurrent identical calls
(idempotent; two clients racing the same `{parent, name}` both succeed and both get
the realpath — AC #5). No idempotency key / nonce is needed: a replayed request
re-creates the same folder (a no-op `MkdirAll`) and re-replies the same path.

---

## Error handling

Four reject branches, all replying a **fixed static string** (never the parent, the
name, the joined/resolved path, or decode-error text). The no-leak logging discipline
is the security co-crux (with confinement) and is **uniform** here — *simpler* than
#823's per-branch divergence, because **there is no safe-to-log structured field
except `conn_id`**: `change_workspace` could log the opaque `conversation_id`, but
this verb's only decoded fields (`parent`, `name`) are *both* attacker-controlled path
components, so neither may be logged.

| # | Condition | Wire code | Static msg | Log fields | Creates? |
|---|-----------|-----------|-----------|------------|----------|
| 1 | Payload undecodable | `protocol.malformed` | `…Malformed` | **`conn_id` ONLY** | no |
| 2 | Empty / whitespace `parent` | `protocol.malformed` | `…EmptyParent` | **`conn_id` ONLY** | no |
| 3 | Bad `name` (empty / abs / separator / `..`) | `protocol.malformed` | `…BadName` | **`conn_id` ONLY** | no |
| 4 | Resolver rejected (escape / unresolvable / mkdir err) | `protocol.malformed` | `…Rejected` | **`conn_id` ONLY** | no (escape rejected pre-`MkdirAll`) |

**The invariant to assert (and to state LOUDLY in the handler's SECURITY doc
comment):** *no log record this handler emits — on any branch, including success —
ever contains `parent`, `name`, a joined/resolved path, or a decode/confine `err`.*
Only `conn_id` and a static `event` name. Two specific traps a developer will hit by
pattern-matching the siblings:

- **Do NOT log the decode `err`** (branch 1) — `create_conversation.go:122` and
  `rename_conversation` do; Go's `json.Unmarshal` errors can embed offending input
  bytes.
- **Do NOT log the confine `err` or the path** (branch 4) — `create_conversation.go:177`
  logs the wrapped confine `err`, which **names the offending path**. This verb must
  not (AC #7). The success branch logs `conn_id` only too — no created path (uniform
  no-path posture; the path is returned on the wire to the requester, which is not a
  leak, but the daemon log stays path-free).

`os.MkdirAll` order guarantees no partial state on a reject: the confiner's
containment check #1 runs before `MkdirAll` (main.go:575–587), so an escape creates
nothing; branches 1–3 return before `resolve` is ever called.

---

## Testing strategy

Hermetic where possible; the handler test injects a **fake `WorkspaceFolderResolver`**
(a closure) so no real filesystem is touched. Scenarios (bulleted inputs → expected
behaviour; the developer writes them table-driven in the project idiom):

**Handler test** (`internal/relay/handlers/create_workspace_folder_test.go`, modelled
on `change_workspace_test.go` — reuse its conn/`assertErrorPayload`/`findLogRecord`
helpers):

- **Success replies the created path.** Valid `{parent, name}` + accept-fake (returns
  `/home/u/proj/app`) → `workspace_folder_created` reply (`in_reply_to` correlated)
  whose `path` equals the fake's returned realpath (AC #1/#4). The resolver was called
  with the exact `parent, name` passed.
- **Resolver rejected → malformed, no leak** (AC #2, AC #7). Valid shape + reject-fake
  returning `("", fmt.Errorf("%w: outside home", ErrWorkspaceFolderRejected))`, with a
  `parent` carrying an injected marker → non-retryable `protocol.malformed`, static
  message (no marker); and the `…rejected` log record carries **no `err`, no
  parent/name/path/marker anywhere**, only `conn_id`. The security-critical assertion.
- **Empty parent → malformed, resolver never called** (AC #7). `parent: ""` (and
  whitespace) → `…EmptyParent`, non-retryable; assert the fake resolver was **not**
  invoked (proves the guard precedes resolve).
- **Bad name → malformed, resolver never called** (AC #3). A table over `name` ∈
  {`"a/b"`, `"../x"`, `"x/.."`, `"/abs"`, `".."`, `""`, `"  "`} → each `…BadName`,
  non-retryable, resolver not invoked.
- **Malformed payload → malformed, no leak** (AC #7). Truncated/garbage payload
  carrying a marker → `…Malformed`; log record has **no `err`**, only `conn_id`.
- **No-echo static messages.** An injected-looking parent/name never appears in any
  reject reply `Message`.

**Resolver test** (`cmd/pyry`, mirrors any existing `resolveSpawnDir`/confine test —
uses a real temp `$HOME` via `t.Setenv("HOME", …)` + `os.MkdirTemp`):

- **Creates + returns realpath** (AC #1/#4). Existing in-`$HOME` parent + fresh name →
  the folder now exists on disk and the returned path is its `EvalSymlinks` realpath.
- **Idempotent on existing** (AC #5). Call twice with the same `{parent, name}` → both
  succeed, both return the same realpath, no error.
- **Escape rejected, creates nothing** (AC #2). Parent resolving outside `$HOME` (e.g.
  a symlink in `$HOME` pointing to an `os.MkdirTemp` outside it) → error satisfying
  `errors.Is(err, handlers.ErrWorkspaceFolderRejected)`, and assert the target folder
  was **not** created.
- **Tilde parent** (parity with `resolveWorkspaceDir`). `parent: "~"` → resolves under
  the real `$HOME`. (The deep symlink/`..` escape matrix is already covered by
  `confineWorkdirToHomeCreating`'s own tests — this test only pins the tilde-expand +
  join + sentinel-wrap wrapper.)

**Protocol round-trip** (`internal/protocol/workspace_test.go` + two `testdata/*.json`
fixtures `create_workspace_folder.json`, `workspace_folder_created.json`), mirroring
`TestCreateConversationPayload_RoundTrip`: decode fixture → assert fields → re-marshal
→ byte-equivalent.

`compat_test.go` gains both constants in its three lists + the `22 → 24` count bump.
`go test -race ./...`, `go vet`, `staticcheck` must stay green.

---

## Scope / size note (documented family false-positive)

Production `.go` files touched: **5** —
`codes.go` (2 consts), `envelope.go` (2 `v1TypeSet` lines), `workspace.go` (2 payload
structs, new file), `create_workspace_folder.go` (new handler), `relay.go`
(`resolveWorkspaceFolder` helper + 2 wiring lines). Three are single-package protocol
one-liners in `internal/protocol/`.

This trips the architect's §4 raw ≥5-file count, but it is the **known, documented
false-positive** for this wire-verb family, and this ticket sits *inside* the boundary,
not over it — for three concrete reasons:

1. **PO already ruled "thin S"** when it split #825 into #887 + #888, having applied the
   "and"/5-AC tests at that split (`po-two-verb-wire-ticket-splits-on-security-posture`).
2. **The direct sibling #823 (`change_workspace`) shipped SIX production files as one S**,
   green (PR #886), at ~half the developer budget — and #887 is **thinner**: it has the
   *same* protocol-vocab spread plus one reply type, but **drops the entire conversations
   registry interaction** (no `Update`, no `Save`, no snapshot-under-lock, no
   stale-comment fix, no `ConversationWorkspaceUpdater`). Its handler is the leanest in
   the family.
3. **It is NOT the #841 "payload + typed reply → split" case.** That rule fires for
   **v2-control-intercept** verbs whose handler child must thread a new seam through
   `startRelay`→`startRelayV2`→`cmd/pyry/main.go` (`set_session_settings` needed
   `Pool.UpdateSettings` reachable). `create_workspace_folder` is a **v1TypeSet
   `dispatch.Route`** verb (AC #6): its resolver lives **inline in `relay.go`** (package
   `main`, no external state), so there is **no threading, no seam param, no handler-child
   heaviness** — the split driver #841 relied on is structurally absent here. The new
   reply type adds one const + one `v1TypeSet` line + one struct **in files already
   touched** — zero new files beyond the handler, zero fan-out.

Against the §1 red lines: **2** new files (≤3 ✓), ~**110** production LOC / ~**480**
total (≤600 ✓), **2** new exported types (`CreateWorkspaceFolderPayload`,
`WorkspaceFolderCreatedPayload`) + 1 exported func-type + 1 sentinel var (≤5 ✓), **0**
consumer-cascade call sites (purely additive — no existing symbol changes signature ✓),
**4** reject branches (≤10 ✓), 7 ACs that are facets of one handler
(`po-v2-control-verb-ac-count-is-facets`). Ships as one `S`. No split.

---

## Open questions

- **`parent`/`name`/`path` json tags.** This spec picks the literal field names from the
  ticket. If the desktop client settled on different tags, it is a one-line change on the
  two structs at integration — resolve with the desktop client, not a design blocker
  (the ticket flags reconcile-with-desktop, mirroring #823's `cwd`-vs-`workspace`).
- **`docs/protocol-mobile.md` § create_workspace_folder.** The wire-doc section is a
  documentation-phase deliverable (written from this spec + the merged diff after the PR
  lands), **not** a developer AC — the developer's worktree mutates only code, tests, and
  this spec file.

---

## Security review

**Verdict:** PASS

This ticket carries the `security-sensitive` label, so this adversarial pass over the
spec is mandatory. `create_workspace_folder` is a **write-to-the-host-filesystem** verb
driven by two untrusted client-supplied fields (`parent`, `name`), so **[File operations]**
is the active centrepiece (as it was for #823), joined by **[Error messages, logs,
telemetry]** as the co-crux. Categories the conversation verbs waved off are re-walked
here because the surface is materially different (a *creating* confiner + a *second*
untrusted field). Each finding cites a real anchor.

**Findings:**

- **[Trust boundaries]** No MUST-FIX. One explicit boundary: the handler's single
  `json.Unmarshal` of the phone-supplied `CreateWorkspaceFolderPayload`. Its two untrusted
  fields are each contained at a named point: `name` at the handler's name-shape guard
  (step 3) which admits only a single clean path element, and `parent`+`name` together at
  `resolveWorkspaceFolder`'s `expandTilde`→`Join`→`confineWorkdirToHomeCreating` before any
  filesystem write. Within a server-id, paired devices are one trust domain (ADR 025
  § Security model): any paired client may create a folder anywhere under the operator's
  `$HOME`, consistent with `create_conversation`'s spawn-dir posture. The tenant boundary is
  the server-id, enforced structurally at the Noise IK handshake (unpaired → 4401, never
  reaches the handler).

- **[Tokens/secrets]** N/A — the verb mints, stores, and compares no token or secret;
  mints no id (no `crypto/rand` surface).

- **[File operations]** No MUST-FIX — **the active surface**, the reason for the label.
  The untrusted `{parent, name}` becomes a real directory on the host, so both the
  *escape* and the *directly-under-parent* properties are enforced fail-closed, before or
  during the single `MkdirAll`. Walked sub-surface by sub-surface:
  - *Path traversal.* `confineWorkdirToHomeCreating` runs `filepath.Abs` →
    `filepath.EvalSymlinks` → `withinDir` (`filepath.Rel` + `..`-boundary check,
    main.go:481) on the resolved candidate, so `../../etc` cleans/resolves outside `$HOME`
    and is rejected, and a sibling like `/home/userfoo` is not confused with `/home/user`
    (the #118/#221 prefix-match gotcha).
  - *Symlink escape.* Containment is tested on the **symlink-resolved** ancestor
    (containment check #1, main.go:575–581, built from `EvalSymlinks(existing)`), and again
    post-creation (check #2, main.go:592–598) — a `parent` whose ancestor symlinks out of
    `$HOME` is rejected. Both `$HOME` and the target are canonicalised before each test, so
    a symlinked home yields no false reject.
  - *Creates-nothing-on-escape (AC #2).* Containment check #1 is **pre-`MkdirAll`**
    (main.go:575–587 gates line 583's `MkdirAll`), so an escaping request is rejected with
    zero filesystem side effect — asserted by the resolver test.
  - *Directly-under-parent (AC #3) — the property confinement does NOT give.* A name like
    `sub/dir` or `../sib` stays inside `$HOME` and would pass confinement, but must not be
    accepted (the folder must land directly under `parent`). This is enforced by the
    handler's independent name-shape guard (step 3): reject on separator, `..`, absolute,
    or empty. **Belt-and-suspenders, different fabric:** name-shape is one deterministic
    string check; confinement is a second deterministic path check — two independent
    deterministic gates, not one gate twice, and neither is a stochastic agent rule.
  - *Idempotency / no clobber (AC #5).* `MkdirAll` on an existing folder is a no-op
    returning success + the realpath; it never truncates or replaces existing content, and
    a phone cannot overwrite a file-that-is-not-a-directory into a directory (`MkdirAll`
    errors if a path component is a non-dir — folded into the reject). The verb creates
    **only** directories (`0o700`), never files.
  - *Empty-parent footgun.* `confineWorkdirToHomeCreating("")`/`Join("", name)` would
    resolve under the daemon's process cwd (possibly inside `$HOME`, thus passing); the
    handler's empty-parent guard (step 2) runs **before** resolve, so a blank parent is a
    clean `protocol.malformed`, never a silent create under the daemon cwd.
  - *Tilde.* `expandTilde` runs on `parent` **before** the join/confine so a client's `~/…`
    (it cannot know the daemon's absolute `$HOME`) anchors at the real home; `~user` is not
    resolved to another user's home — it passes through literally and fails the later
    confine as a deterministic reject.
  - *Permissions / atomicity.* Created dirs are `0o700` (owner-only), inherited from the
    reused primitive. No new file mode, no stat-then-open TOCTOU introduced (the confiner's
    Lstat-walk + double containment check is the reused, reviewed recipe).
  - *TOCTOU (create → later spawn).* The folder created here is trust-marked and re-confined
    only at the eventual `create_conversation` spawn (`resolveSpawnDir` →
    `confineWorkdirToHomeCreating` + `trustMark`, idempotent on the realpath), which re-catches
    a component swapped to escape `$HOME` after creation. This verb deliberately does **not**
    `trustMark` (see §The confine adapter), so it grants no auto-trust to a folder that may
    never host a session.

- **[Subprocess / external command execution]** N/A — the handler executes no subprocess
  and passes no value to `exec.Command`. It is a strict attack-surface *reduction* vs
  `create_conversation` (which spawns claude with a phone-influenced cwd): this verb only
  `MkdirAll`s. The created path reaches an `exec.Command` argument only later, via a fresh
  `create_conversation` that re-runs the full confine + trust-mark — no path this verb
  creates reaches argv un-re-validated.

- **[Cryptographic primitives]** N/A — no crypto in the handler; AEAD framing is the
  unchanged v2 transport's concern.

- **[Network, I/O & DoS]** No MUST-FIX. The handler reads no socket; inbound frame size is
  capped by the v2 transport decoder upstream, transitively bounding `parent` and `name`
  length. No explicit application-level path-length cap — acceptable: the verb performs one
  bounded `MkdirAll` per request and fans out to no one (reply goes only to the requester).
  `EvalSymlinks`/`MkdirAll` walk only real on-disk components within/around `$HOME`, bounded
  by the filesystem, not attacker-inflatable into amplification. A pathological deep `parent`
  is a single `MkdirAll` of a client-typed depth — no worse than the client creating it over
  a shell. No HTTP/WS/TLS surface added.

- **[Error messages, logs, telemetry]** No MUST-FIX — the co-crux with [File operations].
  (1) All **four** reject branches reply a fixed static string constant — no supplied bytes
  (the parent, the name, or a decode/confine-error fragment) reach the wire (AC #7 wire
  half). (2) **Uniform no-path logging**, *stricter* than #823: because both decoded fields
  are path components, **no** log record — reject *or* success — logs `parent`, `name`, the
  joined/resolved path, or the decode/confine `err`; only `conn_id` + a static `event`. Two
  traps called out in the handler's SECURITY doc comment so a future editor does not
  reintroduce a leak by pattern-matching the siblings: the malformed branch must not log the
  decode `err` (embeds input bytes, unlike `create_conversation.go:122`); the rejected branch
  must not log the confine `err` (names the offending path, unlike `create_conversation.go:177`).
  The security test asserts the rejected log record contains no marker anywhere. No telemetry.

- **[Concurrency]** No finding. No new goroutines, no shared mutable state, no lock. The
  handler holds no registry; each call is a self-contained decode→validate→`MkdirAll`→reply on
  the per-conn goroutine. `MkdirAll` is safe and idempotent under concurrent identical calls
  (two clients racing the same `{parent, name}` both succeed and both get the realpath).
  Replay is harmless: a replayed request re-`MkdirAll`s (a no-op) and re-replies the same path
  — no nonce / idempotency key needed (contrast the modal verbs).

- **[Threat model alignment]** Untrusted-client path+name input, cross-tenant, path-traversal,
  symlink-escape, and nested-name (`sub/dir`) threats are in scope and addressed above. Out of
  scope, each named with who picks it up: (1) **starting a conversation in the new folder** —
  that is `create_conversation`, which independently re-confines + trust-marks its spawn dir;
  this verb only creates the directory and grants no trust. (2) **Recent-workspaces / listing**
  — sibling #888. (3) **Live fan-out to other connected clients** — the reply goes only to the
  requester, matching the family.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-09
