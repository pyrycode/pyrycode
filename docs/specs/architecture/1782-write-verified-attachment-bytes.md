# #1782 — Write verified attachment bytes into a conversation's attachment directory

## Files to read first

| Read | Symbols | What to extract |
|---|---|---|
| `internal/attachments/storage.go` | `EnsureDir`, `ErrInvalidID`, `ErrNotContained` | The directory this slice is handed and what it already guarantees; the `errors.New("attachments: …")` sentinel style and the doc-comment register the new sentinel must match. Note `ErrNotContained`'s "host paths here are for the operator's log, safe only because they never reach the wire" note — the new sentinel needs the same kind of note with one addition (see § Error handling). |
| `internal/attachments/filename.go` | `SanitizeFilename` | The one and only name transform. Total (no error), returns exactly one path component, never begins with `.`, and is explicitly **not unique**. Do not write a second one and do not re-check its postconditions. |
| `internal/attachments/storage_test.go` | `resolvedInstanceDir`, `wantDir`, `assertEmptyDir`, `TestEnsureDir_DistinctAttachmentIDs` | Reuse the first two helpers verbatim — they are exactly the "build `want` from the resolved root, not from `t.TempDir()` and not from the return value" discipline this ticket's Technical Notes demand. `TestEnsureDir_DistinctAttachmentIDs` is the row that already pins two directories per conversation; this ticket's same-filename test is its consequence one layer up. |
| `internal/keys/store.go` | `Save` | **The closest precedent — copy this one.** It is the only one of the three that writes raw bytes rather than encoding JSON, so its `f.Write(body)` step is the shape needed here. |
| `internal/conversations/registry.go` | `Save` | Second instance of the same recipe; confirms the `defer func() { _ = os.Remove(tmp) }()` placement and the `_ = f.Close()` on every mid-sequence failure. |
| `internal/devices/registry.go` | `Save` | Third instance. Read one of these two to confirm the recipe is house style, not one file's habit. |
| `internal/transport/wssclient.go` | `ErrFatalClose` (its two `fmt.Errorf` uses) | The repo's precedent for `fmt.Errorf("%w …: %w", sentinel, …, cause)` — sentinel and OS cause both wrapped in one call. This is the shape § Error handling prescribes. |
| `internal/protocol/attachments.go` | `MaxAttachmentFilenameBytes` | 255, the bound `SanitizeFilename` already truncates to. Context only — nothing in this slice re-checks it. |
| `docs/knowledge/features/attachments-package.md` | § "Directory resolution and creation (#1781)", § "Sentinels and discard semantics", § "Mutation-testing lessons" | The sentinel family's shape and why the new one is structurally unlike the seven latching ones. From the mutation section, two traps that apply directly here: an overlay mutant that deletes the only use of an import or the sole read of a local **fails the build and scores as a false green**, and `grep -a` is required when reading `go test -v` output. Also the #1781 lesson that a spec can predict a sole-red row its own fixture rules forbid — § Testing strategy names three such rows up front so they are not chased. |
| `docs/protocol-mobile.md` | § Attachments (the `filename` row), § Error codes (`attachment.storage_failed`) | `filename` is "a display string and a sanitiser input, **never a path**"; `attachment.storage_failed` carries a **static** message, never the host path and never the underlying filesystem error. Both are downstream contracts this slice must not make impossible. This slice edits neither doc and picks no wire code. |

## Context

`EnsureDir` (#1781) landed on main and returns the symlink-resolved, `0o700`
directory one attachment of one conversation is filed under. Nothing puts bytes
in it. `Accumulator.Assemble` (#1770) produces verified bytes and hands them
nowhere. This slice is the join: bytes in, file on disk, path back.

It is the second and last half of storage. #1744 wires the dispatch site and
maps refusals to wire codes; #1745 (prompt construction) and #1746 (retrieval)
consume what lands here. Nothing in production calls this function when the
ticket closes — that is expected, and matches how every other slice in this
family landed.

No ADR is warranted. The three decisions worth recording (trusts its directory,
copies rather than extracts the atomic-write recipe, one new sentinel) are all
package-local and belong in the package overview, which the documentation phase
owns.

## Design

One production file changes: `internal/attachments/storage.go`, beside
`EnsureDir`. Two new exported symbols, no new types, no new package.

### `ErrWriteFailed`

```go
var ErrWriteFailed = errors.New("attachments: attachment file could not be written")
```

Placed in `storage.go` beside `ErrInvalidID` and `ErrNotContained` as a third
top-level `var`, not folded into `accumulator.go`'s grouped block — that block's
opening sentence scopes it to `Add` and `Assemble`, the same reason
`ErrUploadTooLarge` lives in `admission.go`.

It is **not** named `ErrStorageFailed`. All three of this file's sentinels map
to `attachment.storage_failed` at #1744, so a Go name matching the wire code
would falsely suggest this one is *the* storage-failure sentinel. It names what
failed — the write — not the code it becomes.

Like `ErrInvalidID` and `ErrNotContained`, and unlike the six latching
sentinels, it has no discard semantics: there is no accumulator state here to
latch or drop.

### `Store`

```go
func Store(dir, filename string, data []byte) (string, error)
```

Named `Store`, not `WriteFile`: `attachments.WriteFile(dir, filename, data)`
sits one letter from `os.WriteFile(name, data, perm)` with a different argument
order and a different contract, and the package name already supplies the noun.
`EnsureDir` then `Store` is how #1744 will read.

Behaviour, in one line each:

- Sanitises `filename` through `SanitizeFilename` into one component, joins it
  onto `dir`, writes `data` there atomically, and returns that joined path.
- On any failure returns `("", err)` with `errors.Is(err, ErrWriteFailed)` true.
- Leaves no temporary file in `dir` on any exit path, success or failure.
- Is **idempotent**: a second `Store` with the same `dir` and `filename`
  overwrites with no error. No `O_EXCL` — see § Idempotence below.

**It trusts `dir` and resolves nothing.** Every containment guarantee already
happened in `EnsureDir`; re-deriving the path here forks the check `EnsureDir`
exists to own, and would break the same-filename criterion, since the
per-attachment-id component is the entire reason two identical client filenames
can coexist. State the trust in the doc comment as a precondition — *safe only
when `dir` came from `EnsureDir`* — and then do not re-validate.

Keep the doc comment proportionate: the containment reasoning is already written
above `EnsureDir` and restating it doubles this ticket's line count for no
reader. What the doc comment must carry is the trust precondition, the
idempotence, the "the returned path is `dir` joined with the *sanitised* name,
which is not unique across conversations or attachments" note, and the log
obligation from § Error handling.

### The write sequence

Copy `keys.Save`'s recipe step for step. Do **not** extract a shared helper:
ten packages hand-roll this today and none imports another's; factoring it out
is a cross-package refactor this slice is not.

1. `name := SanitizeFilename(filename)`; `path := filepath.Join(dir, name)`.
2. `os.CreateTemp(dir, ".attachment-*.tmp")` — **in `dir` itself**, which is
   what makes step 7 an intra-filesystem rename and therefore atomic.
3. `tmp := f.Name()`; `defer func() { _ = os.Remove(tmp) }()`.
4. `os.Chmod(tmp, 0o600)`.
5. `f.Write(data)`.
6. `f.Sync()`, then `f.Close()`.
7. `os.Rename(tmp, path)`; return `path, nil`.

Steps 4–6 each close `f` explicitly (`_ = f.Close()`) before returning their
error, exactly as all three precedents do; step 6's own `Close` failure returns
without a second close.

Three notes on why individual steps are there, because two of them are not
falsifiable by any test in this suite and a reader will otherwise assume they
are dead weight:

- **The deferred `os.Remove`** is what makes the fourth acceptance criterion
  hold on every failure path. After a successful rename it fails `ENOENT` and
  the error is deliberately discarded.
- **`os.Chmod(tmp, 0o600)`** is belt-and-suspenders. `os.CreateTemp` already
  opens at `0600`, and umask can only clear bits, so on any ordinary host the
  mode is already right and dropping this line reddens nothing. It stays because
  it is the house recipe and because it makes the mode explicit rather than
  inherited from `CreateTemp`'s undocumented-at-the-call-site default.
- **The dot-prefixed temp pattern cannot collide with a stored attachment.**
  `SanitizeFilename` never returns a component beginning with `.` — it prefixes
  an underscore instead — so `.attachment-*.tmp` is unreachable as an
  attachment's name. That is what makes "the directory holds exactly this one
  file" a clean assertion rather than a fragile one.

### Data flow

```
Accumulator.Assemble ──verified bytes──┐
                                       ├──▶ Store(dir, filename, data) ──▶ path
EnsureDir(instanceDir, convID, aid) ───┘         │
        └── resolved 0o700 dir ────────────┘     └── SanitizeFilename(filename)
```

Both inputs arrive already-checked from elsewhere. `Store` adds no check of its
own and is the sole reader of `Filename` in the package, per the package
overview's division of labour.

## Concurrency model

No goroutines, no locks, no context. `Store` is a leaf function over the
filesystem.

It is safe for concurrent use **on distinct `dir` values**, which is the only
way #1744 can reach it: the directory is keyed by `attachment_id`, and
`Registry`'s `uploadKey{connID, attachmentID}` plus `appFrameWorker`'s
per-conn serialisation mean exactly one goroutine ever holds one upload.

Two concurrent `Store` calls with the same `dir` and the same `filename` are
last-writer-wins, atomically — each writes its own uniquely-named temp file and
the renames serialise in the kernel, so a reader sees one complete file or the
other, never a mixture. No test needs to prove this and none should try; state
it in the doc comment.

## Error handling

Six failure points, one sentinel. Each returns
`fmt.Errorf("%w: <op> %q: %w", ErrWriteFailed, dir, err)` — sentinel and OS
cause both wrapped, per `ErrFatalClose`'s precedent in
`internal/transport/wssclient.go`. `errors.Is` then reaches `ErrWriteFailed` for
#1744's mapping and the underlying `fs` error for the operator.

| Step | `<op>` |
|---|---|
| `os.CreateTemp` | `create temp in` |
| `os.Chmod` | `chmod temp in` |
| `f.Write` | `write temp in` |
| `f.Sync` | `fsync temp in` |
| `f.Close` | `close temp in` |
| `os.Rename` | `rename into` |

**The format string names `dir` and nothing else. Never `path`, never `name`,
never `filename`.** `dir` is built entirely from a conversation id and an
attachment id, both canonical-shape-checked; it carries no client text. `path`
and `name` embed the sanitised client filename, and sanitising removes the
log-injection half of the hazard but not the independent privacy reason
`docs/protocol-mobile.md` § Attachments bans logging a filename for. This is the
same discipline `ErrDigestMismatch` follows when it carries the *computed*
digest and never the declared one.

**The wrapped OS cause from `os.Rename` defeats that rule and cannot be made to
obey it.** `os.Rename` returns `*os.LinkError`, whose `Error()` prints both the
source and the destination path, and the destination is `path`. So a
rename-failure error text does quote the sanitised component regardless of the
format string. Two consequences, both of which the developer needs before
writing tests:

- The doc comment must state the obligation on the consumer: #1744 maps this to
  `attachment.storage_failed`'s static message (already its contract) and, if it
  logs, logs the sentinel and the ids — not the error text. This is
  `ErrNotContained`'s "safe only because they never reach the wire" note plus
  one clause.
- **Do not write an assertion that the error text omits the sanitised
  component.** It is unsatisfiable on the rename path for the reason above, and
  vacuous on the create-temp path where the name is never touched. This is the
  #1781 lesson repeating: a spec's own rules can forbid a row that looks
  obviously testable.

Redacting the `LinkError` (unwrapping to the bare errno) was considered and
rejected: it is a defence for a failure mode nobody has observed, it costs the
operator the one diagnostic that says *which* rename failed, and `errors.Unwrap`
returning `nil` for a non-`LinkError` would make `%w` unsafe.

### Idempotence

A phone that drops mid-upload and reconnects re-sends the same `attachment_id`.
`EnsureDir` answers the same directory; `Store` must answer the same path and no
error, overwriting with the same bytes. `os.Rename` gives this for free.
**Do not add `O_EXCL` or an existence pre-check** — either turns an ordinary
reconnect into `attachment.storage_failed` at #1744, which the wire contract
marks retryable-after-backoff and which the client would then hot-loop against.
No acceptance criterion covers this; § Testing strategy pins it with two extra
assertions so a later reader cannot "harden" it away.

## Testing strategy

All in `internal/attachments/storage_test.go`, all `t.Parallel()`, all reusing
`resolvedInstanceDir` and `wantDir`. Every `want` path is built from
`resolvedInstanceDir`'s **second** return value — never from a raw `t.TempDir()`
(macOS hands back `/var/folders/…`, a symlink to `/private/var/folders/…`) and
never from `Store`'s own return value (which passes under every mutant,
including one that writes the raw client name).

One new helper: `assertDirEntries(t *testing.T, dir string, want ...string)` —
reads `dir` and fails unless the entry-name set is exactly `want`. Sibling to
the existing `assertEmptyDir`; used by both the success test and the
rename-failure test.

**`TestStore_WritesSanitisedComponent`** — table-driven over the success path.
Each row: a client filename, the expected component **written as a string
literal, not as a call to `SanitizeFilename`**, and the bytes. Assert the
returned path equals `filepath.Join(dir, <literal>)`, that reading it yields
exactly the bytes, that its `Mode().Perm()` is `0o600`, and
`assertDirEntries(t, dir, <literal>)`.

- `"report.pdf"` → `"report.pdf"` — the ordinary case, which the sanitiser
  leaves untouched. **On its own this row is vacuous for the traversal
  mutant**, which is why the next row is mandatory.
- `"../../etc/passwd"` → `"_.._.._etc_passwd"` — **required by AC 1.** Separator
  runes become `_` and the leading `.` gains an `_` prefix, so a build that
  joined the raw client name writes two directories up and this row's read-back
  fails.
- `"///"` → `"attachment"` — nothing survives the allowlist, so the fallback
  answers. Pins that `Store` does not special-case an "empty-looking" name.
- One row carries zero-length `data`, to pin that an empty attachment is a
  legitimate file rather than an error.

**`TestStore_SameFilenameTwoAttachments`** — AC 2, end to end. `EnsureDir` twice
with `aid1` and `aid2` under `convA`, `Store` the same `"report.pdf"` with
*different* bytes into each returned directory, then assert the two returned
paths differ, that each equals `filepath.Join(wantDir(root, convA, aidN),
"report.pdf")`, and read both back asserting each set of bytes. Different bytes
per attachment is what makes an overwrite visible; identical bytes would pass
under a build where one clobbered the other.

**`TestStore_Idempotent`** — `Store` the same `dir`/`filename` twice with the
same bytes; assert both calls return the identical path and a nil error, that
the bytes read back, and `assertDirEntries(t, dir, <name>)` still reports one
entry. Pins the no-`O_EXCL` decision above.

**`TestStore_RenameFails`** — AC 4, first fixture. After `EnsureDir`, create a
**directory** at the sanitised name inside it (`os.MkdirAll`), then `Store`.
Root-safe by construction: a `chmod 0o500` fixture proves nothing when the suite
runs as root, and `rename` onto an existing directory fails on both Linux
(`EISDIR`) and macOS (`ENOTDIR`) regardless of privilege or of whether that
directory is empty. Assert `errors.Is(err, ErrWriteFailed)`, that the returned
path is `""`, and `assertDirEntries(t, dir, <name>)` — exactly the pre-existing
directory, no `.attachment-*.tmp` survivor.

**`TestStore_CreateTempFails`** — AC 4, second fixture. `os.RemoveAll` the
directory `EnsureDir` returned, then `Store`. Also root-safe. Assert
`errors.Is(err, ErrWriteFailed)` and an empty returned path. **Do not assert on
the directory's contents here** — the directory does not exist, so there is
nothing to read and `assertDirEntries` would fail on `os.ReadDir` for a reason
unrelated to the code under test. `TestStore_RenameFails` is where the
no-leftover-temp property is pinned.

**`TestStore_SentinelIsDistinct`** — AC 4's `errors.Is` clause. Take the error
from the rename fixture and loop over every other exported sentinel in the
package asserting `!errors.Is(err, other)`: `ErrTotalChunksMismatch`,
`ErrIndexOutOfRange`, `ErrDuplicateIndex`, `ErrSizeMismatch`,
`ErrDigestMismatch`, `ErrIncomplete`, `ErrInvalidDeclaration`,
`ErrUploadTooLarge`, `ErrInvalidID`, `ErrNotContained`. Ten, verified present
on main at spec time.

### Mutants and their predicted sole reds

Measure with `go test -overlay` against an absolute-path JSON manifest, per this
package's convention — no worktree write. Grep the run's output for both
`build failed` and `declared and not used` before trusting any verdict, and use
`grep -a`.

| Mutant | Predicted sole red |
|---|---|
| `filepath.Join(dir, filename)` — raw client name, sanitiser dropped | `TestStore_WritesSanitisedComponent` rows 2 and 3. Row 1 stays green. |
| Delete `defer func() { _ = os.Remove(tmp) }()` | `TestStore_RenameFails` (its `assertDirEntries` call). |
| Return `tmp` instead of `path` | Success rows' path equality; the same-filename test's path equality. |
| Reuse `ErrNotContained` in place of a new sentinel | `TestStore_SentinelIsDistinct`. |
| `os.Rename` before `f.Sync()`/`f.Close()` (reordered recipe) | None expected — the ordering is a crash-durability property, not an observable one. Listed so it is not mistaken for an untested bug. |

Three properties this suite deliberately does **not** pin, stated up front so
the developer does not spend turns chasing a red that cannot exist:

- **Dropping `os.Chmod(tmp, 0o600)`** reddens nothing: `os.CreateTemp` already
  opens at `0600` and umask only clears bits. Making it observable would need
  the test to mutate the process-global umask, which is not compatible with
  `t.Parallel()`.
- **Replacing the whole recipe with `os.WriteFile(path, data, 0o600)`** reddens
  nothing either: the happy path still works, the mode is still `0o600`, the
  directory still holds one entry, and the rename fixture still errors. The
  temp-and-rename recipe is required for crash-atomicity, which no test in a
  hermetic suite observes. Code review should expect this gap rather than read
  it as missing coverage.
- **`os.CreateTemp` in `os.TempDir()` rather than `dir`** reddens nothing on a
  single-filesystem CI box, where the cross-device rename that the
  same-directory rule exists to prevent simply does not occur.

## Open questions

1. **`Store` vs. some other verb.** `Store` is this spec's call. If #1744's
   dispatch site reads better with a different name, renaming a symbol with one
   caller is that ticket's cheap change, not a reason to defer here.
2. **Whether `Store` should also return the sanitised component separately.**
   Not needed by any acceptance criterion and not needed by #1745, which builds
   a prompt reference from the path. Deferred until a consumer asks.
3. **A `0o600` file inside a `0o700` directory owned by the daemon.** That is
   the mode every other on-disk artefact in this repo uses and matches
   `EnsureDir`'s directory mode. If #1746 ever needs a non-daemon reader, it
   changes then, with a threat model to justify it.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] SHOULD FIX — documented, not enforced.** The boundary is
  explicit and single: `dir` is trusted, `filename` and `data` are not. `dir`'s
  trust is *inherited*, not established — `Store` performs no resolution and no
  containment check, by design, because doing so would fork the check `EnsureDir`
  owns and would break the same-filename criterion. Nothing in the type system
  signals this: `dir` is a plain `string`, so a future caller can hand `Store` a
  directory it built itself and silently lose every containment guarantee.
  Mitigation is the doc comment's stated precondition plus the fact that
  `EnsureDir` is the only function in the package that produces such a path.
  Minting a `ResolvedDir` type to carry the trust in the signature was
  considered and deferred: it is #1744's contract to make, exactly as
  `EnsureDir`'s doc comment already says of an `AttachmentID` type, and inventing
  it here would give this slice a second exported type with no consumer.
- **[File operations] No findings on traversal.** `filename` never reaches a
  path unsanitised: `SanitizeFilename` is total, returns exactly one component,
  and the AC-1 fixture `"../../etc/passwd"` is chosen precisely so a build that
  joined the raw name is red. `Store` performs no `filepath.Clean`, no `..`
  handling and no resolution of its own — a deliberate absence, since the
  sanitiser has already reduced the input to something with no separator to
  clean.
- **[File operations] No findings on TOCTOU.** There is no check-then-use pair.
  `Store` never stats `path` before writing it — the whole design is
  create-temp-then-rename, and `os.Rename` is a single atomic syscall. This is
  the one category where the deliberate absence of an existence check (see
  § Idempotence) is also the security answer: a pre-check would introduce the
  gap that the rename's atomicity closes.
- **[File operations] No findings on symlinks.** The residual symlink window is
  `EnsureDir`'s, already analysed and bounded there: exploiting it needs write
  access to the daemon's own `0o700` state directory, and anyone holding that can
  already rewrite `devices.json`. `Store` opens nothing by a caller-supplied
  path — `os.CreateTemp` creates with `O_EXCL` in a directory that is already
  resolved — and the rename destination is a single non-separator component
  beneath it. A symlink planted at `path` between `EnsureDir` and `Store` would
  be *replaced* by the rename, not followed through, so it cannot redirect the
  write.
- **[File operations] No findings on permissions or atomicity.** `0o600` on the
  file, explicit `Chmod` rather than inherited from `CreateTemp`, inside
  `EnsureDir`'s `0o700` directory. Temp-plus-rename is the house recipe, verified
  on main in `keys.Save`, `conversations.Registry.Save` and
  `devices.Registry.Save`.
- **[Error messages, logs, telemetry] SHOULD FIX — the one real finding.** A
  rename-failure error text quotes the sanitised client filename, because
  `os.Rename` returns `*os.LinkError` and `LinkError.Error()` prints its
  destination. `docs/protocol-mobile.md` § Attachments bans logging a filename
  for a privacy reason that sanitising does not lift. The spec's response is
  two-part and is in § Error handling: the format string is forbidden from
  naming `path`, `name` or `filename` (enforceable, and the developer's
  obligation), and the doc comment states the consumer obligation on #1744 —
  static wire message, and log the sentinel and the ids rather than the error
  text. Not a MUST FIX: the wire message is static by contract, so nothing
  reaches a remote party; the exposure is confined to the daemon's own log on
  the operator's own host, and #1744 is where a log call would first exist.
  Redaction was considered and rejected as a defence for an unobserved failure
  mode that costs the operator the one diagnostic distinguishing which rename
  failed.
- **[Error messages, logs, telemetry] No further findings.** `data` — the
  attachment's own bytes, the most sensitive value in play — appears in no error
  message on any of the six failure paths, and the `%q` operand is `dir` in all
  six, which is built from two canonical-shape-checked ids and carries no
  client-authored text. The declared `sha256` is not in scope here at all.
- **[Concurrency] No findings.** No goroutine, no lock, no shared state, so no
  lock-ordering question and no TOCTOU on shared state. Shutdown mid-write
  leaves at worst a `.attachment-*.tmp` orphan in the attachment directory —
  never a partial file at the destination name, which is the property the rename
  buys. That orphan is the only unrecoverable-by-this-slice state; cleanup is
  deliberately deferred (see § Scope below) and #1746's retrieval reads by the
  stored path, so an orphan is inert rather than confusing.
- **[Network & I/O] No findings.** `Store` performs no I/O beyond the local
  filesystem and reads no socket, so the input-size question is upstream:
  `CheckDeclaredSize` and `Add`'s step 5 bound one upload at 16 MiB before any
  of these bytes exist. There is no second cap here and there should not be —
  adding one would be a third owner of a bound two functions already share.
- **[Concurrency / resource exhaustion] OUT OF SCOPE — named, not deferred
  silently.** Nothing bounds how many attachment *files* one conversation may
  accumulate, or their total size on disk. That is the same gap the package
  overview already records for the in-flight *entry* count (#1786), one layer
  further down: `CheckDeclaredSize` bounds one upload, `Store` writes one file,
  and no ticket in this family caps the aggregate. The settled design is
  WhatsApp-style — files stay until the user removes them, with no daemon-side
  eviction — so this is a product decision already taken rather than an
  oversight, but it means a client that uploads N distinct `attachment_id`s
  fills the host's disk at 16 MiB apiece. Cleanup and eviction are explicitly
  deferred by this ticket's scope fence and have no ticket yet; whoever files
  one should inherit this paragraph.
- **[Threat model alignment] No findings.** The two `docs/protocol-mobile.md`
  § Attachments rules that bind this slice are honoured: `filename` is treated
  as "a display string and a sanitiser input, never a path", and
  `attachment.storage_failed`'s static-message rule is preserved by emitting a
  Go sentinel and no wire code, leaving the mapping to #1744.
- **[Tokens, secrets, credentials] Not applicable.** No token, key or credential
  is created, stored, compared or logged. The `attachment_id` in `dir` is
  documented as explicitly not a capability.
- **[Subprocess execution] Not applicable.** No `exec.Command`, no shell, no
  environment manipulation.
- **[Cryptographic primitives] Not applicable.** No randomness that matters for
  security (`os.CreateTemp`'s suffix is collision-avoidance, not a secret — the
  temp file is `0o600` in a `0o700` directory and is renamed away immediately),
  no comparison against a secret, no key material. The digest comparison this
  slice depends on is `Assemble`'s and is not repeated.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25

## Scope

In: `internal/attachments/storage.go` (`ErrWriteFailed`, `Store`) and
`internal/attachments/storage_test.go`.

Out: wire dispatch and error-code mapping (#1744), prompt construction (#1745),
retrieval (#1746), the in-flight entry-count cap (#1786), eviction, cleanup,
listing. No protocol-doc edit. No package doc comment and no `doc.go` — the
package already has one in `accumulator.go`, and a second is a duplicate rather
than a merge conflict.
