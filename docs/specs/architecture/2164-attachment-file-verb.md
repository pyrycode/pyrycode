# #2164 — `attachment.file`: file a claude-named host file under the calling session's conversation

Split from #2083. Siblings: #2165 (the MCP tool claude calls), #2166 (the
`attachment_offered` announcement + the retrieval round trip).

## Files read

- `internal/control/protocol.go` → `VerbMCPApprove`, `ApprovePayload`, `ApproveResult`,
  `Request`, `Response` — the wire shape this verb is added beside, and the camelCase
  tag convention every payload but `ApprovePayload` follows.
- `internal/control/server.go` → `Server` (the optional-dependency fields),
  `SetApprovalRegistry`, `SetApprovalSurfacer`, `handleApprove`, `handleSessionsNew`,
  `handle` — the injected-after-construction shape to copy, the read-seam-under-`mu`-then-release
  discipline, and `handleSessionsNew`'s conn-deadline extension for a verb whose work
  can outrun the handshake window.
- `internal/control/client.go` → `Approve`, `request`, `requestPatient` — which of the two
  round-trip helpers a bounded verb uses (`request`; `requestPatient` exists for the
  human-wait approve path alone).
- `internal/control/sessions_has_id_test.go` → `hasIDResolver` — the in-package fake-resolver
  + real-socket harness pattern the handler tests mirror.
- `cmd/pyry/main.go` → `withinDir` (the `filepath.Rel` boundary test that keeps
  `/home/userfoo` out of `/home/user`, the #118/#221 gotcha), `resolveInstanceDirPath`
  (the attachment storage anchor), `runSupervisor` (the `control.NewServer` →
  `SetApprovalRegistry` → `SetApprovalSurfacer` → `Listen` wiring run, and where `convReg`
  and `pool` are in scope), `poolResolver` (the type-narrowing seam precedent),
  `resolveSpawnDir` — **not** reused: it is a spawn-site validator with side effects
  (`confineWorkdirToHomeCreating` creates directories, `trustMark` writes `~/.claude.json`).
- `cmd/pyry/relay.go` → `conversationForSession` — the existing session→conversation scan,
  its empty-`sid` guard, and its documented race-safety against a concurrent
  `RebindSession`. Not reused: it also matches `SessionHistory` and returns only the id,
  and this verb needs the row (for `Cwd`) and the `CurrentSessionID` binding alone.
- `internal/agentrun/workdir.go` → `ResolveWorkdir` — `Abs` → `EvalSymlinks` →
  on-disk case folding. The case fold is why both root and target go through it:
  APFS is case-insensitive by default, so a textual compare of two differently-cased
  spellings of one directory is unsound in both directions.
- `internal/conversations/conversation.go` → `Conversation.Cwd` (documented always-present,
  absolute, `$HOME`-confined at every write site), `Conversation.CurrentSessionID`
  (empty when unbound).
- `internal/conversations/id.go` → `NewID`, `ValidID` — the `crypto/rand` UUIDv4 minter and
  the canonical-shape predicate `attachments` validates ids against.
- `internal/attachments/storage.go` → `EnsureDir`, `Store`, `ResolvePath` — the three
  filesystem primitives, and both LOGGING OBLIGATION blocks. **`Store`'s rename failure is
  an `*os.LinkError` whose `Error()` prints the destination, and therefore the sanitised
  filename** — the single most important thing on this reading list.
- `internal/attachments/filename.go` → `SanitizeFilename` — total, pure, one path component,
  not unique, and **not made loggable by sanitising**.
- `internal/attachments/admission.go` → `maxUploadBytes` (16 MiB), `ErrUploadTooLarge` —
  the receiver-configured, unexported, unpublished ceiling this slice's figure mirrors.
- `internal/sessions/pool.go` → `Pool.Lookup` — **`Lookup("")` returns the bootstrap
  session with a nil error.** The empty-id seam, and a trap for this verb (see § Security review).
- `docs/knowledge/features/control-plane-approve-mcp-approve-verb-forward-to-permbridge.md`
  — the guard-order table (nil dependency before payload validation) and the
  "all daemon-originated refusal messages are fixed constants" rule this verb adopts.
- `docs/knowledge/features/attachments-package.md`, `…-per-upload-byte-bound.md`,
  `…-writing-attachment-bytes.md` — the bound's two-rung shape and why the ceiling stays
  unexported.
- `docs/protocol-mobile.md` § Attachments — the never-log-a-filename ban, and #2082's entry
  establishing that receiving an id is not a capability (#2054 re-validates regardless).

## Context

pyrycode-desktop#1028: a file the assistant produced *"reaches the window as nothing at
all."* #2082 declared `attachment_offered`; nothing produces one. `internal/attachments`
stores only what a client uploaded, so no path exists by which a file claude wrote becomes
an attachment.

The operator settled the mechanism on 2026-09-04: **claude calls a tool**, rather than the
daemon sweeping what claude wrote during a turn. This slice is the daemon half — the control
verb that does the work. The MCP tool is #2165; the announcement and retrieval round trip
are #2166.

**The path is this family's new security surface.** Every existing attachment path component
is daemon-minted or sanitised from a client-declared name; this is a filesystem path chosen
by the model, naming a file to read. It is confined, never trusted.

**Sizing overage, stated rather than hidden.** The size-S table's 800-line ceiling is
exceeded (estimated ~1000 lines of total written work), and the reject-branch line is
exceeded on a raw count (~12 refusal branches). Split depth is 1 (parent #2083, no
grandparent), so a split is permitted — and it is declined on the **floor** rule, which wins
when the two disagree. Every available cut produces a child consumed only by its one sibling:
a confinement helper nothing outside this ticket calls; or a storage step with no path check
in front of it, which would ship a window in which the verb reads an unconfined path. The
reject branches are a linear validation chain in two functions, not a state-machine fan-out,
and they carry **zero** per-branch log calls by design (§ Error handling), so the cost driver
that line proxies for is absent here. No `needs-human:sizing` label: that marker is for the
depth-capped case, which this is not.

**No ADR is warranted.** The two decisions that would earn one — the tool-call mechanism over
a sweep, and `conv.Cwd` over the runner's `workDir` — were both settled on the ticket by the
operator and the refiner respectively, and the second is recorded there with the measurement
(`sessions.Runner` widening is ~8 production files against this ticket's 5).

## Design

### Wire — `internal/control/protocol.go`

```go
const VerbAttachFile Verb = "attachment.file"

type AttachFilePayload struct {
	SessionID string `json:"sessionID"`
	Path      string `json:"path"`
}

type AttachFileResult struct {
	AttachmentID string `json:"attachmentID"`
}
```

`Request` gains `AttachFile *AttachFilePayload`; `Response` gains
`AttachFile *AttachFileResult`. camelCase tags, matching every control payload except
`ApprovePayload` (whose snake_case exists to mirror claude's tool-call contract, which
this verb does not share — #2165 owns that translation).

### Seam — `internal/control/server.go`

The dependency is a plain `func`, following `SetApprovalSurfacer` rather than
`SetApprovalRegistry`: the work needs `conversations.Registry`, `sessions.Pool` and the
instance directory, all of which live at `cmd/pyry`'s composition root, and `withinDir`
is unexported in package `main` and unimportable from here.

```go
// SetFileAttacher installs the dependency servicing VerbAttachFile. Nil (never
// called, or v1/foreground) leaves handleAttachFile answering Response.Error.
func (s *Server) SetFileAttacher(attach func(sessionID, path string) (string, error))
```

Stored as a `Server` field guarded by `s.mu`, read once at the top of the handler and the
lock released before the call — `handleApprove`'s leaf-lock discipline, and load-bearing
here because the call reads a file and writes one.

`handle` gains `case VerbAttachFile: s.handleAttachFile(conn, enc, req.AttachFile)`.

**`handleAttachFile` guard order** (nil dependency first, then payload — `handleApprove`'s
order):

| Precondition | Reply |
| --- | --- |
| attacher is nil | `Response{Error: "attachment.file: no file attacher configured"}` |
| `payload == nil \|\| payload.SessionID == ""` | `Response{Error: "attachment.file: missing sessionID"}` |
| `payload.Path == ""` | `Response{Error: "attachment.file: missing path"}` |
| attacher returns an error | `Response{Error: "attachment.file: " + err.Error()}` |
| attacher returns an id | `Response{AttachFile: &AttachFileResult{AttachmentID: id}}` |

Every branch encodes exactly one response and returns; nothing panics, nothing falls through.
Before the attacher call the conn deadline is extended past the handshake window
(`sessionOpTimeout + sessionOpConnGrace`), `handleSessionsNew`'s treatment for a verb whose
work outlives the 5s handshake — a 16 MiB read plus `Store`'s fsync can.

### Client — `internal/control/client.go`

```go
func AttachFile(ctx context.Context, socketPath string, req AttachFilePayload) (*AttachFileResult, error)
```

Built on `request`, not `requestPatient`: this is a bounded round trip, not a human wait.
Shipped here because #2165's subcommand and #2166's e2e both dial it, and without it each
writes its own framing.

### Worker — `cmd/pyry/attach_file.go` (new)

```go
func fileAttacher(
	convReg *conversations.Registry,
	live func(sessions.SessionID) error,
	instanceDir string,
	log *slog.Logger,
) func(sessionID, path string) (string, error)
```

`live` is a one-line adapter over `pool.Lookup` at the call site. It exists for a testing
need, not preemptively: proving AC-4's two-live-sessions destination rule against a real
`*sessions.Pool` would mean spawning claude children.

Ordered contract, each step refusing rather than continuing:

1. `sessionID == ""` → refuse. **Load-bearing**: `Pool.Lookup("")` resolves to bootstrap, and
   a `CurrentSessionID == ""` scan matches an *unbound* conversation.
2. `live(sessionID)` errors → refuse (no live session).
3. Scan `convReg.List()` for the row whose `CurrentSessionID == sessionID`; none → refuse.
   Duplicated cmd-side rather than added to the registry, per PROJECT-MEMORY's
   "Resist over-DRY on duplicated registry primitives" — the reason `conversationForSession`
   already gives. `CurrentSessionID` only; `SessionHistory` is deliberately not matched.
4. `conv.Cwd == ""` → refuse.
5. `root = agentrun.ResolveWorkdir(conv.Cwd)`; error → refuse.
6. If `path` is not absolute, `path = filepath.Join(root, path)` — against the **root**,
   never the daemon's process directory, which is what a bare `filepath.Abs` would use.
7. `resolved = agentrun.ResolveWorkdir(path)`; error → refuse. Same recipe as the root, which
   is what makes the comparison sound on a case-insensitive filesystem.
8. `withinDir(root, resolved)` false → refuse. Boundary-aware, never a prefix compare.
9. `checked = os.Stat(resolved)`; `!checked.Mode().IsRegular()` → refuse. **Before** the open,
   because opening a FIFO blocks until a writer appears.
10. `f = os.OpenFile(resolved, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)`;
    error → refuse. Deferred close.
11. `opened = f.Stat()`; `!os.SameFile(checked, opened)` → refuse. The check-then-use guard.
12. `!opened.Mode().IsRegular()` → refuse (the descriptor's own view).
13. `opened.Size() > maxAttachFileBytes` → refuse, before any byte is read.
14. `io.ReadAll(io.LimitReader(f, maxAttachFileBytes+1))`; over the bound → refuse (growth
    between fstat and read).
15. `id = conversations.NewID()` — `crypto/rand` UUIDv4, lowercase, the published
    `attachment_id` shape. Never taken from the request.
16. `dir = attachments.EnsureDir(instanceDir, conv.ID, string(id))`.
17. `attachments.Store(dir, filepath.Base(resolved), data)` — `Store` sanitises the
    claude-authored name into one path component itself.
18. Return `string(id)`.

`maxAttachFileBytes = 16 << 20`, an unexported untyped constant in this file — receiver
policy, unpublished, learned by being refused (#1751's rule). Declared here rather than
imported because `attachments.maxUploadBytes` is unexported and exporting it is a
cross-package change this slice is not.

### Wiring — `cmd/pyry/main.go`

One call beside `SetApprovalSurfacer`, before `Listen`:

```go
ctrl.SetFileAttacher(fileAttacher(convReg, func(id sessions.SessionID) error {
	_, err := pool.Lookup(id)
	return err
}, resolveInstanceDirPath(*name), logger))
```

"Ships inert" means nothing *calls* the verb yet — #2165 is the MCP tool. The dependency this
slice owns is installed here, exactly as #1104 installed `SetApprovalRegistry` while leaving
#1080's surfacer nil.

## Concurrency model

No goroutines are spawned, so there is nothing to leak and no shutdown path to own.
`s.mu` is taken to read the seam and released before the attacher runs, so no control-server
lock is held across `convReg`'s mutex or across any filesystem call — the lock ordering is
"at most one at a time," which is why there is no ordering to document.

`Registry.List` copies under the registry mutex, so the scan is race-safe against a
concurrent `RebindSession` (the reasoning `conversationForSession` records). `EnsureDir` and
`Store` are documented safe for concurrent use, and `Store`'s temp-plus-rename means a
process killed mid-write leaves either nothing or the complete file.

The verb is re-entrant across sessions: two concurrent calls mint distinct ids, so they touch
distinct directories and cannot collide.

## Error handling

**Every refusal reason is a static sentence.** No path, no filename, no workspace, no session
id, no wrapped error from a filesystem call. Two independent reasons: `docs/protocol-mobile.md`
§ Attachments bans logging a filename and sanitising does not lift the ban, and a host path is
worse than a filename. The reasons stay actionable without content — "the path is outside this
conversation's workspace" tells claude to write the file into the workspace and call again,
which is precisely the remedy an explicit tool call buys over a sweep.

**`attachments.Store`'s and `EnsureDir`'s error text never reaches the wire.** `Store`'s rename
failure is an `*os.LinkError` whose `Error()` prints the destination, and therefore the
sanitised filename; `EnsureDir`'s names the instance directory. Both map to one static
"storing the file failed."

**Logging is one line, on success only:** `conversation_id` and `attachment_id`, both
daemon-minted or shape-checked. Refusals are not logged at all — the reason returns to the
caller, which is the only party that needs it. That is also what keeps twelve refusal branches
from becoming twelve log calls.

Sentinel-wrapping is not warranted: no caller distinguishes refusals programmatically
(#2165 surfaces the reason to claude as text), so `errors.New`/`fmt.Errorf` with a static
string is the whole contract. No `ErrorCode` constants are added.

## Testing strategy

`cmd/pyry/attach_file_test.go` — the bulk:

- **`TestFileAttacher_Confinement`**, one table, nine rows, each asserting the stated outcome:
  relative path inside the tree (accepted), absolute path inside it (accepted), a path outside
  it, a `../` traversal, a symlink whose target is outside the tree, a symlink as the final
  component whose target is **inside** (accepted — the discriminating pair with the row above,
  proving the check runs on the resolved path and not on the string's shape), a sibling
  directory whose name shares the root's prefix, a directory, and a FIFO.
- **`TestFileAttacher_SwapBetweenCheckAndRead`** — the check and the read are separate
  functions so the swap is deterministic rather than raced: check file A, `os.Rename` a
  *pre-created* second file over A's path (a live distinct inode, so inode reuse cannot make
  the assertion vacuous), then read → refused. Control arm with no swap → served.
- **`TestFileAttacher_DestinationIsCallerSession`** — two live sessions bound to two
  conversations with different `Cwd`s; call naming the second; assert the bytes read back
  through `attachments.ResolvePath` under the **second** conversation, and that nothing landed
  under the first. The cursor is not consulted anywhere in the design, so the test's force is
  that the destination follows the named session.
- **`TestFileAttacher_Refusals`** — empty session id, unknown session, session bound to no
  conversation, conversation with an empty `Cwd`, oversized file. Each asserts the reason
  names no path and no filename.
- **`TestFileAttacher_MintedID`** — `conversations.ValidID` holds, and two calls differ.

`internal/control/attach_file_test.go` — the verb, over a real socket on the
`sessions_has_id_test.go` harness: nil attacher (the inert state), nil payload, empty
sessionID, empty path, attacher error passthrough, success, and one `AttachFile` client
round trip.

RED first: every test above is written and observed failing for the right reason before the
production body exists.

Verification is § B2's touched scope — `go test -race ./internal/control/... ./cmd/pyry/...`,
`go vet ./...`, `go build ./cmd/pyry`. The full-module race suite is the verifier's gate.

## Open questions

1. **Does `agentrun.ResolveWorkdir` misname its subject when applied to a file?** Its doc says
   "workdir"; the body is `Abs` → `EvalSymlinks` → case fold, which is path-shaped, not
   directory-shaped. Resolve in Phase B by confirming `canonicalCase` reads the *parent*
   directory for the leaf component (it should — it walks components and `ReadDir`s the
   accumulated prefix). If it does not behave on a file leaf, fall back to `filepath.Abs` +
   `filepath.EvalSymlinks` applied to both root and target, and record the departure under
   `## Revisions`.
2. **Does `syscall.O_NOFOLLOW` exist under that name on both darwin and linux in this
   module's Go version?** If either is missing, drop the flag and keep `O_NONBLOCK` and
   `SameFile` — the guarantee is `SameFile`'s, and the flag is a subordinate kernel-side
   companion (§ Security review, finding 3b). Record either way.
3. **Should the verb refuse a file whose resolved path is the workspace root itself?** Step 9
   already refuses it as a directory, so no separate branch is planned; confirm the table's
   directory row covers it rather than adding a tenth.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** MUST FIX — *addressed in this plan.* Two untrusted inputs cross into
  trusted state inside one function, `fileAttacher`'s returned closure: `sessionID` (from a
  same-user control-socket peer) and `path` (model-chosen). The hole: an empty `sessionID`
  reaches `Pool.Lookup`, which **returns the bootstrap session with a nil error**, and reaches
  the conversation scan, which would match any row whose `CurrentSessionID` is `""` — an
  *unbound* conversation. Either silently files claude's bytes under a conversation that never
  asked. Design revised: step 1 refuses an empty id before both, and `handleAttachFile`
  independently refuses it at the wire boundary. The two guards are deliberately not one — the
  handler's is a wire-shape check, the worker's is what protects `Lookup`'s bootstrap seam from
  any future caller.
- **[Trust boundaries]** No further findings — the boundary is a single named function, and
  everything downstream of step 8 holds a path that has been canonicalised and bounded. The
  conversation is derived from the caller's session through the daemon's own registry; nothing
  in the request payload names a destination, and the follow-active cursor — which
  `streamApprovalBridge` stamps at enqueue and which therefore names whichever chat the operator
  last messaged — is not consulted anywhere in the design.
- **[Tokens, secrets, credentials]** No findings. The attachment id is minted by
  `conversations.NewID` from `crypto/rand`, never taken from the request, and is not a
  credential: #2054 re-validates the id against the conversation binding on retrieval, so
  holding one grants nothing. No token is stored, rotated or compared here. A file inside the
  workspace may hold secrets, but claude can already read one and print it into the transcript,
  so this opens no new exposure tier — the residual the ticket names, restated rather than
  discovered.
- **[File operations]** Four sub-findings, all addressed:
  - **(a) Path traversal.** Both root and target go through the *same* canonicalisation
    (`agentrun.ResolveWorkdir`: `Abs` → `EvalSymlinks` → on-disk case fold) before a
    `filepath.Rel`-based `withinDir` boundary test. The case fold is not cosmetic: APFS is
    case-insensitive by default, so comparing two differently-cased spellings of one directory is
    unsound in both directions. A relative path joins against the **root**, never the daemon's
    process directory — the escape a bare `filepath.Abs` would hand out by default.
    `filepath.Join`'s cleaning of `..` does not hide an escape, because the boundary test runs
    after it, and `EvalSymlinks` runs after that, so a symlinked component inside the root
    pointing out is caught too.
  - **(b) TOCTOU.** The design does `os.Stat` then open — the exact check-then-use shape this
    category names. `os.SameFile` between the checked `FileInfo` and the descriptor's own
    `f.Stat()` closes it, and closes strictly more than a final-component guard would: if any
    component of the resolved chain is swapped after the check, the opened inode differs and the
    call is refused. The only way to pass is to land on the inode that was checked, which is the
    file that passed. A test performs a real swap at the seam rather than racing for one.
  - **(c) SHOULD FIX, folded in — a post-check swap to a FIFO wedges a daemon goroutine
    forever.** The regular-file check at step 9 runs *before* the open, so the ordering is
    load-bearing rather than stylistic; but a swap between check and open would still reach
    `open(2)` on a FIFO, which blocks until a writer appears, and the extended conn deadline does
    not interrupt a blocking open. `syscall.O_NONBLOCK` makes that open return immediately, and
    the step-12 regular-file check on the descriptor then refuses it. A no-op on a regular file.
    Reachable by anyone with write access inside the workspace, which the same-user threat model
    already grants.
  - **(d) SHOULD FIX, folded in — `syscall.O_NOFOLLOW` as different fabric.** `SameFile` is a
    userspace comparison; `O_NOFOLLOW` is a kernel-enforced refusal on the same swap. It cannot
    cause a false refusal: the path opened is `EvalSymlinks`' output and is symlink-free by
    construction, so the legitimate "symlink as the final component" row still resolves and is
    served. Strictly subordinate to `SameFile`, which covers parent-component swaps that
    `O_NOFOLLOW` does not; open question 2 drops it rather than the guarantee if the constant is
    unavailable on either platform.
  - Permissions and atomicity are inherited, not re-derived: `EnsureDir` creates 0700,
    `Store` writes via `os.CreateTemp` at 0600 plus an explicit chmod and an atomic rename.
- **[Subprocess / external command execution]** Not applicable, by design: this verb spawns
  nothing, builds no argv, and reads no environment. The path never reaches an `exec.Command`.
- **[Cryptographic primitives]** No findings. The one primitive is `conversations.NewID`'s
  `crypto/rand` draw. No hand-rolled crypto, no key material, and no comparison against a
  secret, so `crypto/subtle` has no subject here.
- **[Network & I/O]** One finding, deferred. The file is bounded twice — from `Stat` before any
  read, then by `io.LimitReader(f, max+1)` to catch growth between fstat and read — at an
  unexported, unpublished 16 MiB mirroring `attachments.maxUploadBytes`. The request's `path`
  string is **not** bounded on the wire. OUT OF SCOPE and pre-existing rather than introduced:
  the control socket's decoder bounds no verb's payload (`ApprovePayload.Input` is an unbounded
  `json.RawMessage`), the peer is same-user by the socket's 0600 mode, and an absurd path simply
  fails to resolve. A bound on the control-socket decode belongs to whoever revisits that mode.
  Read/write deadlines are inherited: the handshake deadline bounds the request read, and the
  handler extends it past the file read exactly as `handleSessionsNew` does.
- **[Error messages, logs, telemetry]** MUST FIX — *addressed in this plan.* `attachments.Store`
  returns an `*os.LinkError` on the rename path whose `Error()` prints the destination, and
  therefore the sanitised claude-authored filename; propagating it verbatim to `Response.Error`
  would put a filename on the wire and, via any caller that logs the reason, into a log — the
  precise thing § Attachments bans, and which sanitising does not lift. Design revised: `Store`
  and `EnsureDir` errors map to one static "storing the file failed," and every other refusal is
  likewise a static sentence naming no path, filename, workspace or session id. One success log
  line carrying `conversation_id` and `attachment_id` only. No telemetry.
- **[Concurrency]** No findings. No goroutine is spawned, so none can leak. `s.mu` is read-and-
  released before the attacher runs, so no daemon lock is held across the registry mutex or any
  filesystem call and there is no lock order to get wrong. The registry scan is race-safe against
  a concurrent `RebindSession` (`Registry.List` copies under the mutex). A kill mid-write leaves
  either nothing or the complete file, `Store`'s temp-plus-rename property.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Attachments threat 1 —
  claude-authored strings reaching a render surface — does not land in this slice: the filename
  becomes a stored path component and reaches no wire here, because #2166 owns the
  `attachment_offered` announcement that would carry it. Named as out of scope and owned there.
  Two residuals accepted and restated rather than left to be discovered: **confinement does not
  isolate conversations from each other**, since several conversations may share one `cwd` — it
  bars escaping the tree, which is what it is for; and the `conv.Cwd` / live-child-`workDir`
  divergence window (`change_workspace` updates the row, a backoff restart does not re-read it)
  resolves as a refusal carrying the actionable reason, the design's stated remedy rather than a
  special case. #1475 is open on exactly that prose and this slice depends only on `Cwd` being
  present, absolute and confined, which holds under both of its options.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06
