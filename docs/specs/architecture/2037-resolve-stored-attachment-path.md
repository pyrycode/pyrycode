# #2037 — resolve a stored attachment to its on-host path

`internal/attachments` can put bytes on disk and cannot find them again. This
slice adds the read-side counterpart to `EnsureDir`: a (conversation id,
attachment id) pair in, the path of the file stored under them out, creating
nothing on the way.

## Files read

- `internal/attachments/storage.go` → `EnsureDir` — the containment discipline
  this design copies minus the creation: resolve the instance directory as the
  anchor, build the destination textually beneath the resolved root, compare
  for **equality** rather than "is it under". Its doc block is also where the
  reason equality beats containment lives (a conversation directory symlinked
  at a *sibling* conversation stays inside the instance directory).
- `internal/attachments/storage.go` → `Store` — why AC 4 is reachable: the
  temp file is created inside the destination directory so the rename is
  intra-filesystem and therefore atomic, and its `defer os.Remove` does not
  survive a kill. Also the source of the invariant AC 4's filter is anchored
  on, via `SanitizeFilename`.
- `internal/attachments/storage.go` → `ErrInvalidID`, `ErrNotContained`,
  `ErrWriteFailed` — the house sentinel shape, and the "one sentinel where the
  caller has nothing to branch to" argument this design reuses wholesale.
- `internal/attachments/filename.go` → `SanitizeFilename` — never returns a
  component beginning with `.`, which is what makes a leading-dot filter a
  *contract-anchored* exclusion rather than a pattern match against `Store`'s
  private temp shape.
- `internal/attachments/intake.go` → `Receive` — the caller that deliberately
  discards `Store`'s path, which is why the path cannot be re-derived from the
  id afterwards and this function has to exist.
- `internal/conversations/id.go` → `ValidID` — 36 chars, lowercase hex, dashes
  at fixed offsets. Lowercase-only is load-bearing beyond traversal: it keeps
  the id→directory mapping injective on case-insensitive APFS.
- `cmd/pyry/main.go` → `resolveInstanceDirPath` — `~/.pyry/<sanitized-name>`,
  the `instanceDir` every caller passes.
- `internal/attachments/storage_test.go` → `resolvedInstanceDir`, `wantDir`,
  `assertEmptyDir`, `assertDirEntries`, `convA`/`convB`/`aid1`/`aid2` — the
  fixture vocabulary this slice's tests reuse rather than re-invent. In
  particular `resolvedInstanceDir` returns the `EvalSymlinks`-resolved root
  because macOS `t.TempDir()` is itself a symlink.
- `internal/attachments/registry_test.go` → `packageSentinels` — the declared
  set the "distinct from every other sentinel" assertion quantifies over. Per
  `docs/knowledge/features/attachments-package-mutation-testing-lessons-measured-across.md`,
  a sentinel added and not appended there weakens that claim **silently**, so
  this slice appends to it.
- `docs/protocol-mobile.md` § Error codes → the `attachment.not_found` row —
  the three refusals in AC 3 are **deliberately indistinguishable on the
  wire**, a disclosure decision so the asking verb is not a path-existence
  oracle. This is what forces the sentinel shape chosen below.
- `docs/protocol-mobile.md` § Attachments, § Naming a message's attachments —
  the never-log rule (no client filename, and no path embedding one), and the
  statement that confinement to the message's own conversation, not the id's
  shape or randomness, is the security property.
- `docs/knowledge/features/attachments-package-directory-resolution-and-creation.md`
  — #1781's lesson that a precedent's ordering rationale does not transfer
  unchanged when a caller strengthens the comparison it protects. Re-measure,
  don't inherit.
- `docs/knowledge/features/attachments-package-writing-attachment-bytes.md` —
  #1782's lesson that a doc comment's "the only way a caller can reach this"
  safety argument is a claim about *every* future caller. Two files in one
  attachment directory is exactly that argument coming due, and is AC 5.
- `docs/knowledge/features/attachments-package-sentinels-and-discard-semantics.md`
  — the discard-semantics table does not apply here: like `EnsureDir` and
  `Store`, this function holds no accumulator state to latch or drop.

## Context

Two open slices need a stored attachment's path and neither can get it today:
#2038 puts it into claude's prompt, #1746 streams the bytes back to a client.
Without this function both re-derive the storage layout independently, which
is the fork this slice exists to prevent.

`EnsureDir` is not the answer for either, **because it creates**. A lookup
that `MkdirAll`s the directory it failed to find turns every miss into a state
change on disk, and #1746's `attachment.not_found` path would leave an empty
directory behind for every traversal probe it refuses.

No ADR is warranted: this is the read half of a layout `EnsureDir` already
decided, not a new decision.

## Design

One new exported function and one new exported sentinel in
`internal/attachments/storage.go`. Nothing else changes; the package's
existing surface is untouched and no consumer is updated by this slice.

```go
// ResolvePath returns the path of the file stored under one conversation's
// one attachment, or a refusal. It creates nothing.
func ResolvePath(instanceDir string, conversationID conversations.ConversationID, attachmentID string) (string, error)
```

The parameter list mirrors `EnsureDir`'s exactly, including the asymmetry that
`conversationID` is typed and `attachmentID` is not — same rationale, and
keeping the two functions swap-safe in the same way matters more than tidiness
here, since a caller holding both ids can call either.

**PRECONDITION, and it carries the whole security property:
`conversationID` MUST be the conversation the authenticated session is
already on — never one a client asserted.** The doc comment states this in
those terms, because this function cannot check it and a caller that gets it
wrong defeats everything else the function does. § Naming a message's
attachments is explicit that confinement to the message's own conversation,
**not** the id's shape or its randomness, is what keeps `attachment_id` from
becoming the capability this document repeatedly says it is not; and it names
the same reasoning behind `attachment_chunk` carrying no `conversation_id` at
all. Passing a client-supplied conversation id here reads "name any id, get
its bytes" — every containment step below still passes, because the pair
genuinely resolves inside the conversation it named. `Intake` reaches its own
conversation through a resolver callback rather than off the wire, which is
the shape a caller should copy.

Steps, in order:

1. **Validate both ids** with `conversations.ValidID`, conversation first, and
   refuse before any filesystem call. This is what makes AC 2 hold in its
   strongest form on the id-refusal path: not even the anchor is touched.
2. **`filepath.Abs(instanceDir)`**, then **`filepath.EvalSymlinks`** on the
   result. No `MkdirAll` — this is the one line `EnsureDir` has that this
   function must not. An absent instance directory therefore fails to resolve
   and is a refusal, which is AC 2 made **structural** rather than guarded.
3. **Build `want` textually** beneath the resolved root:
   `<root>/conversations/<conversation-id>/attachments/<attachment-id>`.
   Nothing under the root is resolved to build it.
4. **`filepath.EvalSymlinks(want)`**, then compare `== want`. A pair that was
   never stored has no directory, so this step fails and *is* the unknown-id
   refusal — no separate existence probe, and no ancestor walk, because
   nothing is going to be created beneath a missing ancestor. A directory that
   exists but redirects resolves to something else and fails the equality.
   Equality, not a `filepath.Rel` containment test, for `EnsureDir`'s reason.
5. **`os.ReadDir(want)`** and select the **lexicographically smallest** entry
   that is both a regular file and not dot-prefixed. The contract is stated as
   the minimum rather than as "the first one `os.ReadDir` gives back", so it
   does not lean on that function's sort guarantee; the implementation reads
   the minimum off the sorted slice because the two coincide. No `Stat` call
   and no mtime tie-break, which is what makes AC 5 a total order.
6. **No surviving entry** → refusal. This covers an empty directory and a
   directory holding only leftover temp files.
7. Return `filepath.Join(want, name)`.

**The exclusion filter is leading-dot, not `.attachment-*.tmp`.** AC 4 asks
for the temp shape specifically, and a leading-dot rule is a strict superset
of it, but the reason to prefer the superset is *where it is anchored*: a
pattern match would depend on `Store`'s private temp pattern staying what it
is, whereas the leading-dot rule depends on `SanitizeFilename`'s published
contract that a stored name never begins with `.`. The invariant that makes
the exclusion sound is the one about stored names, so the filter should read
that one.

**Only regular files are eligible.** `os.ReadDir`'s `DirEntry.Type()` reports
`Lstat` bits, so a symlink inside an attachment directory is not regular and
is skipped rather than followed. This is the leaf-level completion of step 4's
containment: without it, the directory is confined but the file it answers is
not, and #1746 would stream whatever the link points at. It costs one
condition, and the same condition also skips a subdirectory, which `Store`
cannot create.

### Sentinel shape: one, new, and not a reuse

Every refusal wraps a single new sentinel, `ErrNotFound`. Not `ErrInvalidID`
and `ErrNotContained` reused from `EnsureDir`, and not a family of three.

- The contract **requires** one static wire answer for all three of AC 3's
  refusals. One sentinel makes that structural: a consumer cannot branch on
  something it cannot distinguish, so the disclosure decision survives a
  careless dispatch site instead of depending on one.
- Reusing the two existing sentinels is an active hazard, not merely a style
  choice. #1897 already maps `ErrInvalidID` and `ErrNotContained` to
  `CodeAttachmentStorageFailed`; a retrieval dispatch site sharing any part of
  that mapping would answer `storage_failed` where the contract says
  `attachment.not_found`. Distinct sentinels keep the two legs' mappings from
  colliding.
- It matches the package's own precedent: `ErrInvalidID`'s doc block argues
  one sentinel is right precisely where "a caller that wants to branch has
  nothing to branch to."

Diagnosis is not lost — the wrapped message still names which step failed, and
the underlying OS error is still reachable through `errors.Is(err,
fs.ErrNotExist)`. That is for the operator's log; the wire sees one code.

`packageSentinels` in `registry_test.go` gains `ErrNotFound`.

## Concurrency model

No goroutines, no locks, no state. Safe for concurrent use by construction,
including concurrently with `EnsureDir` and `Store` on the same pair: the
worst interleaving answers a path that a concurrent `Store` rename is about to
replace, and rename is atomic, so a reader sees one complete file or the
other — the same last-writer-wins property `Store` already publishes.

**The check-then-use window is real and is accepted, on the same bound
`EnsureDir` states.** The entry is regular and the directory resolves to
`want` at the moment this function looks; a caller opens the returned path
afterwards, and between the two the entry could be replaced with a symlink.
Widening the window to zero would mean opening the file here and answering a
handle rather than a path, which neither consumer wants — #2038 needs a path
as prompt text and never opens it. Exploiting the window needs write access
inside the daemon's own `0o700` state directory, and anyone holding that can
already rewrite `devices.json`, which is strictly worse than redirecting one
attachment read.

## Error handling

Every refusal returns `("", err)` with `err` wrapping `ErrNotFound`. Eight
branches: conversation id shape, attachment id shape, `Abs` failure, instance
directory resolution failure, attachment directory resolution failure,
containment inequality, `ReadDir` failure, no eligible entry.

**Message hygiene, and it is narrower here than in `EnsureDir`.** Directory
paths are built entirely from two canonical-shape-checked ids and carry no
client text, so they are safe in an error the way `ErrNotContained`'s already
are. The **returned file path is not** — its leaf is `SanitizeFilename`'s
output, and § Attachments bans logging a client filename for a privacy reason
sanitising does not lift. No error message this function builds may name the
joined file path or any directory entry's name. That is the one rule the
`ReadDir` branch has to be written around: the natural `fmt.Errorf` there must
name `want`, never an entry.

The two ids may be named in messages: a canonical one is loggable, and a
non-canonical one is `%q`-quoted exactly as `EnsureDir` does it, which escapes
the line-oriented-log injection shape.

## Testing strategy

All in `internal/attachments/storage_test.go`, reusing the existing fixture
vocabulary. Every `want` is built from `resolvedInstanceDir`'s **second**
return and from `wantDir`, never from the function's own answer.

- **Round trip (AC 1).** `EnsureDir` + `Store`, then `ResolvePath`, asserting
  the returned path equals `wantDir(...)/report.pdf` **and** that reading it
  yields the stored bytes. Two rows: an ordinary filename and one the
  sanitiser rewrites, so a build that answered a textually-guessed name rather
  than a directory entry fails the second.
- **Creates nothing (AC 2).** Three shapes, each asserting with `os.Stat` /
  `assertEmptyDir` that the probed tree is untouched: an instance directory
  that does not exist at all (asserted absent afterwards); an instance
  directory that exists with no `conversations` beneath it; a conversation
  directory that exists with no attachment directory beneath it.
- **Shape refusal (AC 3).** Table-driven over `EnsureDir`'s own invalid-id
  rows (empty, traversal-spelling, one char short, uppercase hex, at
  `protocol.MaxAttachmentIDBytes`), against a fixture where the *valid* pair
  really is stored — so a row's refusal is attributable to the shape check
  rather than to absence.
- **Unknown id (AC 3).** A stored `aid1` and a lookup for `aid2` in the same
  conversation, plus the cross-conversation row: `convB` asking for a pair
  stored under `convA`.
- **Containment (AC 3).** The three symlink fixtures `EnsureDir`'s suite
  already establishes, ported: conversation directory symlinked out of the
  instance directory; conversation directory symlinked at its *sibling*
  (the row a `filepath.Rel` containment test would pass); attachment
  directory itself symlinked out. Each asserts no path, `ErrNotFound`, and
  `assertEmptyDir` on the target. The sibling row's target is populated with a
  real stored file first, so the refusal is not vacuous against a build that
  would have found nothing to answer anyway.
- **Temp leftover (AC 4).** Two rows sharing one fixture shape: a directory
  holding a stored file *and* a hand-written `.attachment-XXXX.tmp` resolves
  to the stored file — non-vacuous only if the tmp name sorts **before** the
  stored name, which it does by the leading dot; and a directory holding the
  tmp file *alone* refuses.
- **Two files, one directory (AC 5).** Two `Store` calls into one `EnsureDir`
  directory under two different client filenames, then `ResolvePath` called
  repeatedly, asserting the same path each time and that it is one of the two
  written. The assertion is on *stability*, not on which of the two wins:
  the published contract makes that last-writer-wins and explicitly not a
  privilege boundary.
- **Non-regular entry.** A directory whose only entry is a symlink to a real
  file outside refuses rather than answering the link — the leaf half of the
  containment property, which the directory-level symlink rows cannot reach.
- **Sentinel distinctness.** `ErrNotFound` appended to `packageSentinels`, and
  one assertion that a `ResolvePath` refusal wraps neither `ErrInvalidID` nor
  `ErrNotContained` — the two a reader would expect it to reuse, and the two
  whose #1897 mapping makes the reuse wrong.

Mutants worth running before claiming the suite is load-bearing (per this
package's mutation-testing lessons — a "by construction" claim is a mutant to
build, not a row to skip): drop the `IsRegular` condition; drop the leading-dot
condition; swap the equality in step 4 for a `strings.HasPrefix(real, root)`
containment test; reinstate `EnsureDir`'s `MkdirAll` of the instance
directory. Each should redden a named row above and nothing else.

## Open questions

- **Does the leading-dot filter need to exclude an entry that is dot-prefixed
  but not `Store`'s pattern?** Resolved at design time in favour of yes; if
  implementation finds a reason a dot-prefixed stored name is reachable, the
  filter narrows to the temp pattern and this section records it.
- **Should the resolver answer the sanitised leaf name alongside the path?**
  #1746 echoes a `filename` on the wire, but that is the *client's* filename,
  which this package does not retain and `filepath.Base` cannot recover. Out
  of scope: nothing in the five criteria asks for it, and #1746 owns the
  question of where an echoed filename comes from.

## Security review

**Verdict:** PASS (second pass; the first found one MUST FIX, revised inline
above and re-walked from the top)

**Findings:**

- [Trust boundaries] **MUST FIX — fixed before commit.** The first walk found
  the design silently assuming a trustworthy `conversationID`. Every
  containment step passes for a *client-asserted* conversation id, because the
  pair genuinely does resolve inside the conversation it named — so a consumer
  passing one straight off the wire turns `attachment_id` into the capability
  § Naming a message's attachments says confinement, not shape or randomness,
  is what prevents. The function cannot check this and has no session context
  to check it against. Fixed by making it a stated PRECONDITION in the design
  and in the doc comment, in `Store`'s PRECONDITION idiom, naming `Intake`'s
  resolver-callback shape as the one a caller should copy. The other boundary,
  the client-chosen `attachmentID`, is explicit and single: `conversations.ValidID`
  ahead of every filesystem call.
- [Trust boundaries] SHOULD FIX — the returned path is **laundered client
  input at its leaf** (`SanitizeFilename`'s output), not fully daemon-authored,
  and nothing in the type system says so. Phase B adds a LOGGING OBLIGATION
  block to the doc comment in `Store`'s idiom: the caller must not log the
  returned path, because § Attachments bans logging a client filename for a
  privacy reason sanitising does not lift. The verifier must check it landed.
- [Tokens] No findings — no token, secret or credential is read, written or
  compared. `attachment_id` is published as **not** a capability and this
  design does not promote it into one; that is the finding above, and it is
  the reason the precondition rather than the id's entropy is what carries.
- [File operations] No findings on traversal: both path components pass
  `conversations.ValidID`, so neither can spell `..`, a separator or a NUL;
  the destination is built textually beneath an `EvalSymlinks`-resolved
  anchor; and the comparison is full-path **equality**, which refuses the
  sibling-conversation symlink a `filepath.Rel` test would pass. Symlinks are
  not followed at either level — resolved-and-compared at the directory,
  refused by `IsRegular` at the leaf. Permissions and atomic-write are N/A by
  a design property rather than an omission: the function creates nothing and
  writes nothing, which is AC 2 and is asserted by three fixtures.
- [File operations] SHOULD FIX — the check-then-use window between `ReadDir`
  and the caller's `os.Open` is real. Accepted, not closed, and now stated in
  the plan with its bound: exploiting it needs write access inside the
  daemon's `0o700` state directory, and that already permits rewriting
  `devices.json`. Answering an open handle instead of a path would close it
  and is refused because #2038 needs a path as prompt text and never opens it.
- [Subprocess] No findings — nothing is executed. Worth stating because #2038
  puts this path into claude's **prompt**: it is neither argv nor shell input
  here, and its only client-derived component has already been through
  `SanitizeFilename`'s allowlist, so the bytes reaching the prompt are drawn
  from that allowlist rather than from an arbitrary client string.
- [Cryptographic primitives] N/A by design — no randomness is drawn and no
  comparison is against a secret. The one equality compares two daemon-derived
  paths, neither confidential, so `subtle.ConstantTimeCompare` would protect
  nothing; plain `==` matches `EnsureDir`.
- [Network & I/O] OUT OF SCOPE — `os.ReadDir` reads and sorts a whole
  directory, and nothing bounds how many files one attachment directory can
  accumulate: `Store` writes one file per distinct sanitised filename, so a
  paired device re-uploading one `attachment_id` under many names grows it
  without limit and makes every later resolve O(N). Deferred rather than
  fixed, on two grounds. The same device already holds a strictly worse lever
  — unbounded disk against an attachment store with no retention or eviction
  at all — so a bound here would move nothing while that stands; and bounding
  it properly means a retention policy, which is a ticket nobody has filed.
  Named here so whoever files it inherits the measurement rather than
  rediscovering it. Streaming `f.ReadDir(n)` would cap the memory but forfeits
  the sort AC 5's determinism rests on.
- [Error messages, logs] No findings, with one rule the implementation must be
  written around and which the plan's Error handling section states: **no
  error this function builds may name the joined file path or any directory
  entry's name**, since those carry `SanitizeFilename`'s output. Directory
  paths are safe — built from two canonical-shape-checked ids, carrying no
  client text, exactly as `ErrNotContained`'s messages already are. A
  non-canonical id is `%q`-quoted on the shape-refusal branch, `EnsureDir`'s
  precedent, which escapes the line-oriented-log injection shape. This package
  emits no wire code and makes no log call.
- [Error messages, logs] No findings on disclosure — one sentinel for all
  eight refusal branches makes § Error codes' "deliberately indistinguishable"
  requirement structural rather than a discipline the dispatch site has to
  keep, so the retrieval verb cannot become a path-existence oracle even
  through a careless consumer. Reusing `ErrInvalidID`/`ErrNotContained` was
  rejected partly for this and partly because #1897 already maps both to
  `CodeAttachmentStorageFailed`.
- [Concurrency] No findings — no goroutine, no lock, no package state, so
  there is no lock order, no shared-state TOCTOU and nothing to leak. The
  filesystem TOCTOU is the finding two entries above. Nothing is half-written
  if the process dies mid-call, because nothing is written.
- [Threat model alignment] § Security model threat 1 — a client-supplied
  element that becomes both a directory component and prompt content — lands
  squarely on `attachmentID`, and one canonical-shape check answers both
  hazards, which is the check this design runs first. Confinement to the named
  conversation is the second half and is the equality in step 4. Retention and
  eviction of stored attachments is named out of scope above and is owned by
  no ticket yet.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-03
</content>
</invoke>

## Revisions

### 2026-09-03 — two test-side departures, both found by mutant measurement

Neither changes the shipped contract; both change what the suite proves about
it. Recorded here rather than folded silently into the plan above.

- **AC 5 pins which file wins, not only that the answer is stable.** The
  Testing strategy above says "the assertion is on *stability*, not on which of
  the two wins". That is too weak to be worth writing: a selection rule that is
  deterministic but wrong — newest-wins, or last-in-name-order — is stable
  across any number of calls and passes it. The shipped test asserts the exact
  path, and the fixture stores the lexicographically *smaller* name **first**
  so those two neighbouring rules answer a different file. Measured: an overlay
  mutant walking the entries in reverse reddens
  `TestResolvePath_TwoFilesOneAttachmentDirectory` and nothing else in the
  package.

- **AC 3's shape refusal needed a row the plan did not have, because it shipped
  unpinned without one.** The plan's invalid-id table is `EnsureDir`'s own rows,
  and every one of them names a non-canonical id that *also* names a directory
  that does not exist — so each refuses by absence, and the shape check the
  table is named after is never the reason. Measured: an overlay mutant deleting
  **both** `conversations.ValidID` calls passed the entire package green,
  including all seven of those rows. This is precisely the trap this package's
  mutation-testing lessons name — a property true by construction still needs
  its own mutant, or it ships unpinned.

  `TestResolvePath_CaseFoldedID` closes it. A case-folded id is the one
  non-canonical shape that resolves anyway: `EvalSymlinks` deliberately does not
  case-canonicalise, and APFS is case-insensitive by default, so on macOS the
  uppercased pair reaches the directory the lowercase pair stored into and only
  the shape check refuses. It is the mutant's sole red. The package-level test
  ids are all digits and dashes and fold to themselves, so the row carries two
  hex-lettered ids of its own. On a case-sensitive filesystem the row still
  passes, by absence rather than by shape — correct there, just not the sole
  red, which is stated in the test's own comment rather than left to be
  rediscovered.

**Mutants run, unfiltered across the whole package** (no `-run`, per this
package's own lesson that a filter is exactly what would make a
"nothing else reddens" claim look true regardless):

| Mutant | Sole red | Also red |
|---|---|---|
| drop the `IsRegular` condition | `TestResolvePath_NonRegularEntry` (both rows) | nothing |
| drop the leading-dot condition | `TestResolvePath_LeftoverTempFile` (both rows) | nothing |
| `strings.HasPrefix(resolved, root)` in place of the equality | `TestResolvePath_SiblingConversationSymlink` | nothing |
| reinstate `EnsureDir`'s `MkdirAll` of the anchor | `TestResolvePath_CreatesNothing/instance_directory_does_not_exist` | nothing |
| walk the entries in reverse | `TestResolvePath_TwoFilesOneAttachmentDirectory` | nothing |
| delete both `conversations.ValidID` calls | `TestResolvePath_CaseFoldedID` (both rows) | nothing |

The escaping-conversation and attachment-directory symlink rows are
deliberately *not* the prefix mutant's red: those targets land outside the
resolved root, so a containment test refuses them too. Only the sibling row
separates equality from containment, which is what `EnsureDir`'s doc block
already says and what this measurement confirms rather than assumes.
