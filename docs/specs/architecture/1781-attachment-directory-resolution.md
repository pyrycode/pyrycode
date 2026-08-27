# #1781 — Resolve and create a conversation's attachment directory, refusing an escaping one

## Files to read first

| Read | Symbol | What to extract |
|---|---|---|
| `cmd/pyry/main.go` | `confineWorkdirToHomeCreating` | **The whole doc comment, twice.** It records the ordering this design copies: `os.Lstat` ancestor probing so a symlink counts as existing and is *resolved* rather than stepped over; the containment check **before** anything is created; `MkdirAll` only after it passes; a re-`EvalSymlinks` + second check afterwards that also yields the path to return. |
| `cmd/pyry/main.go` | `withinDir` | Read it to understand what this design deliberately does **not** use. Its `filepath.Rel` boundary test answers "is it under the root", which is too weak for AC 4 — see § Design, "Why equality and not `withinDir`". |
| `internal/conversations/id.go` | `ValidID` | The canonical shape both ids must satisfy. Note the alphabet: lowercase hex and dashes only. That is load-bearing beyond traversal (§ Security review, File operations). |
| `internal/attachments/filename.go` | `SanitizeFilename` | The sibling path-component primitive in this package (#1772). Its shape, doc density and comment register are the template for `storage.go`. |
| `internal/attachments/filename_test.go` | `assertComponent` | The table + shared-`t.Helper()`-assertion idiom this package tests in. |
| `internal/attachments/accumulator.go` | the package doc comment at the top of the file, and the sentinel `var` block containing `ErrIndexOutOfRange` | House style for sentinel doc comments (what refuses, what the caller does about it, who maps it to the wire). The package comment also contains the one sentence this ticket must amend — § Design, "The package comment stops being true". |
| `internal/attachments/admission.go` | `ErrInvalidDeclaration`, `ErrUploadTooLarge` | The precedent for *why two sentinels rather than one*: they differ because the client's repair differs. Mirror that reasoning style for this slice's pair. |
| `internal/protocol/attachments.go` | `MaxAttachmentIDBytes`, and the SECURITY block in `AttachmentChunkPayload`'s doc comment | The 64-byte ceiling that is explicitly *not* a defence, and the sentence assigning the canonical-shape check to whoever turns the id into a path component. That sentence is this ticket. |
| `internal/conversations/registry.go` | `Registry.Save` | The `os.MkdirAll(dir, 0o700)` precedent for directories under the instance directory. `internal/devices/registry.go`'s `Registry.Save` is the identical second instance. |
| `docs/knowledge/features/attachments-package.md` | § "Sentinels and discard semantics" | The seven-sentinel table this slice extends to nine, and the standing rule that wire mapping is #1744's, not this package's. |
| `docs/knowledge/features/attachments-package.md` | § "Mutation-testing lessons" | **Read before writing the tests.** Sole-redness in this package is *measured* with `go test -overlay`, never argued from a table. The first bullet — a predicted red set that measured larger than predicted — is the trap this spec's § Testing strategy is written to avoid repeating. |

## Context

An attachment upload arrives as chunks, is reassembled in memory by this package, and then has to land somewhere on the host. This slice decides *where*, creates that directory, and hands its resolved path back. Writing bytes into it is #1782's; dispatching to it and mapping refusals to wire codes is #1744's.

Two things make this more than a `filepath.Join`.

**The layout does not exist yet.** Daemon state under the instance directory is flat today — `sessions.json`, `conversations.json`, `devices.json`, `server-id` all sit directly in it, and `conversations.json` is one file covering every conversation rather than per-conversation state. There is nothing for attachments to sit alongside, so this slice creates the layout and closes the operator's open "one per-conversation directory, or shared per-daemon directory with a per-conversation namespace?" question in favour of the per-conversation directory their own session log already sketched.

**Two client-adjacent values become path components, and they are not equally trusted.** `attachment_id` is chosen by the client on the upload leg; its 64-byte ceiling is documented as explicitly *not* a safety property. The conversation id is daemon-side — the frame deliberately carries no `conversation_id` so a client cannot name one — but it becomes a path component here too, so it is validated here too.

**This slice is the last owner of the attachment-id canonical-shape check.** #1767, #1769 and #1770 each declined it in writing, and #1773 split into this ticket and #1782. If the check is not here, it is nowhere.

**ADR?** No. The layout choice is real but small, and it is fully described by the § Design "On-disk layout" table plus the two paragraphs justifying the anchor. It does not need a numbered decision record. If the documentation phase disagrees after code review, this section is the pointer.

## Design

One new file, `internal/attachments/storage.go`, and a one-sentence amendment to the existing package comment. Nothing else.

### On-disk layout

```
<instanceDir>/                          e.g. ~/.pyry/<daemon-name>/
├── sessions.json                       (existing, flat — unchanged)
├── conversations.json                  (existing, flat — unchanged)
├── devices.json                        (existing, flat — unchanged)
├── server-id                           (existing, flat — unchanged)
└── conversations/                      NEW — a directory; coexists with conversations.json
    └── <conversation-id>/              NEW — one per conversation, 0o700
        └── attachments/                NEW — 0o700
            └── <attachment-id>/        NEW — 0o700; what EnsureDir returns
                └── <filename>          #1782's; not this slice's
```

A `conversations/` **directory** and the existing `conversations.json` **file** coexist under one parent without colliding — different names.

The per-attachment-id component is what lets two attachments in one conversation carry the same client-supplied filename. #1782 tests that end to end; here it falls out of the layout for free.

### The contract

```go
// EnsureDir resolves and creates the directory this conversation's attachment
// is filed under, returning its symlink-resolved absolute path.
func EnsureDir(instanceDir string, conversationID conversations.ConversationID, attachmentID string) (string, error)
```

- `conversationID` is typed and `attachmentID` is not, deliberately. Both are canonical UUIDv4 strings, so a caller that swaps them produces a *valid* but wrong path and no validator can catch it. Typing one of the two makes the swap a compile error at every call site. There is no `AttachmentID` type to use for the other — `protocol.AttachmentChunkPayload`'s field is a plain `string`, and minting a type for it is #1744's contract to make, not this slice's.
- Idempotent: a second call for a pair whose directory already exists returns the same path and no error. `MkdirAll` gives this for free; an exclusive-create would turn a re-delivered upload into a storage failure at #1744.
- The returned path is the `EvalSymlinks` output, not the textually built one. They are provably equal by the time it returns (§ "The algorithm", step 8) — returning the resolved one is the honest expression of AC 1.

### Sentinels

Two new ones, in `storage.go`, in this package's `errors.New("attachments: …")` house style. The doc comments follow `ErrInvalidDeclaration`'s register: what refuses, and why this is a separate sentinel rather than a widening of its neighbour.

| Sentinel | Returned when |
|---|---|
| `ErrInvalidID` | Either id fails canonical-shape validation. Wrapped with `fmt.Errorf` naming *which* of the two failed and its value. |
| `ErrNotContained` | Both ids are canonical, but the destination's symlink-resolved path is not the path this pair maps to beneath the resolved instance directory — whether it lands outside the instance directory entirely or inside it under another conversation. |

**One `ErrInvalidID`, not two.** The distinction the acceptance criteria depend on is id-refusal vs containment-refusal, and the ticket leaves the finer split to this spec. A caller that wants to know *which* id was bad reads the wrapped message; a caller that wants to branch does not, because the repair is identical for both (the client cannot fix either — a bad conversation id is a daemon bug and a bad attachment id is a client bug, and neither is retryable). That is precisely the test `ErrUploadTooLarge`'s doc comment applies to justify *splitting* a sentinel: split when the repair differs. Here it does not.

**Neither sentinel picks a wire code and this file edits no protocol doc.** Mapping is #1744's, at the dispatch site, per the package's standing rule. A Go error here may name the resolved host path for the operator's log — the precedent's does — because `attachment.storage_failed` is contractually a static message and #1744's mapping is what keeps the path off the wire.

### The algorithm

Order is security-load-bearing; it is `confineWorkdirToHomeCreating`'s order with a stricter final comparison.

1. **Validate `conversationID`.** Not canonical → `ErrInvalidID`. **Return before touching the filesystem.**
2. **Validate `attachmentID`.** Same.
3. `filepath.Abs(instanceDir)`, then `os.MkdirAll(abs, 0o700)` — an absent instance directory is created, not an error (§ "Two smaller decisions").
4. `root := filepath.EvalSymlinks(abs)` — **the anchor**, and the only path resolved from the outside in.
5. `want := filepath.Join(root, "conversations", string(conversationID), "attachments", attachmentID)` — built **textually** beneath the resolved root. Nothing under `root` is resolved to build it.
6. **Ancestor walk.** From `want`, probe upward with `os.Lstat` (not `os.Stat`) to the longest existing ancestor, accumulating the not-yet-existing suffix. `EvalSymlinks` that ancestor and re-join the suffix to get `candidate`. Keep the precedent's `parent == existing` root guard.
7. **Containment check #1, pre-creation.** `candidate != want` → `ErrNotContained`, **before any `MkdirAll`**.
8. `os.MkdirAll(want, 0o700)`, then `final := filepath.EvalSymlinks(want)`; **containment check #2**, `final != want` → `ErrNotContained`. Return `final`.

Steps 1–2 before step 3 is what makes AC 2's "the instance directory is left exactly as it was" true in its strongest form: on an id refusal not even the anchor is created.

Step 6's `Lstat` is the load-bearing choice. `os.Stat` follows the link and reports the *target*'s existence, so a symlinked conversation directory would be treated as not-existing and stepped over, `EvalSymlinks` would never see it, and both AC 3 and AC 4 would pass a build that is exploitable.

### Why equality and not `withinDir`

The check at steps 7 and 8 is `candidate == want`, not "is `candidate` under `root`". This is the one place this design departs from its precedent, and it is what keeps both of the last two criteria non-vacuous with a single comparison:

| Fixture | `withinDir(root, candidate)` | `candidate == want` |
|---|---|---|
| `conversations/<conv>` is a symlink pointing **out of** the instance dir | refuses ✓ | refuses ✓ |
| `conversations/<conv>` is a symlink to a **sibling conversation inside** the instance dir | **passes ✗** (AC 4 vacuous) | refuses ✓ |
| `conversations/<conv>/attachments/<aid>` itself is a symlink to anywhere | passes or refuses depending on target | refuses ✓ |
| Nothing is a symlink (the normal case, and every repeat call) | passes ✓ | passes ✓ |

Equality is strictly stronger than containment — a path equal to one built beneath `root` is trivially beneath `root` — so nothing is lost, one comparison replaces two, and no `withinDir` equivalent needs to exist in this package.

The anchor choice is the other half. Resolving the **conversation** directory instead of the instance directory would make row 1 vacuous: the symlink resolves first, and everything beneath the resolved target is then trivially "contained" in it. Resolving the instance directory alone with a containment test would make row 2 vacuous. Resolving the instance directory and comparing to the textual expectation is the combination that refuses both.

Both sides of the comparison are clean absolute paths — `Abs`, `EvalSymlinks` and `Join` all clean — so `==` is well defined, and `EvalSymlinks` does not case-canonicalise (which is exactly why `internal/agentrun.ResolveWorkdir` is not a substitute here: it does, and it requires the path to already exist).

### Two smaller decisions

- **An absent instance directory is created, not an error.** Every other registry under that directory creates it lazily on first write (`Registry.Save` in `internal/conversations/registry.go` and in `internal/devices/registry.go`). Requiring pre-existence would make attachment storage depend on whether some *other* subsystem happened to persist first. It is created before it is resolved because the anchor's resolution is what the containment check compares against, and `EvalSymlinks` fails on a path that does not exist.
- **`MkdirAll` at step 8 is unconditional**, where the precedent guards it with `if rest != ""`. `MkdirAll` on an existing directory is a no-op returning nil, so the guard buys nothing here; and dropping it means a `want` that exists as a *file* rather than a directory is refused by `MkdirAll`'s own error rather than sliding through. The precedent's guard exists because its result is a `chdir` target, not because `MkdirAll` would misbehave.

### The package comment stops being true

`internal/attachments/accumulator.go`'s package comment currently says the package is *"In-memory only. Nothing here touches the filesystem, reads a socket, or emits a wire code, and the package makes zero log calls."* This slice falsifies the filesystem half of that sentence the moment it lands.

**Amend that one sentence in place.** Do **not** add a package comment and do **not** add a `doc.go` — the package already has one, and a second is a duplicate rather than a merge conflict. The amendment says the package is in-memory for accumulation and admission, that `EnsureDir` is the sole filesystem-touching function in it, and leaves the socket / wire-code / zero-log-calls claims intact (all three stay true — `EnsureDir` logs nothing and emits no code).

This is the entire permitted edit to `accumulator.go`. Nothing else in that file changes.

**Leave the stale `#1741 / #1743` pointer in `internal/protocol/attachments.go` alone.** The ticket makes re-pointing it explicitly optional and hands it to #1744. Editing it would add a third production file for no acceptance criterion.

## Concurrency model

No goroutines, no locks, no shared state. `EnsureDir` is a pure function of its arguments plus the filesystem, and is safe for concurrent use by construction: `MkdirAll` is idempotent and races benignly against itself, and two concurrent calls for the same pair both return the same path.

The residual TOCTOU window between check #1 and `MkdirAll`, and between `MkdirAll` and check #2, is the same window `confineWorkdirToHomeCreating` accepts, bounded by the same threat model — see § Security review, File operations.

## Error handling

| Failure | Result |
|---|---|
| Conversation id not canonical | `ErrInvalidID`, wrapped, naming the field. Filesystem untouched. |
| Attachment id not canonical | `ErrInvalidID`, wrapped, naming the field. Filesystem untouched. |
| Resolved destination ≠ expected destination (either check) | `ErrNotContained`, wrapped with both paths. Nothing created beneath the offending symlink. |
| `Abs` / `MkdirAll` / `EvalSymlinks` fails for an OS reason (permissions, ENOSPC, a *dangling* conversation symlink) | Wrapped OS error, neither sentinel. This is honest: a dangling symlink is broken host state, not a containment breach, and #1744 maps anything unrecognised to `attachment.storage_failed`. |

Callers distinguish with `errors.Is`, never by comparing strings.

## Testing strategy

`internal/attachments/storage_test.go`, same package, table-driven where the rows share a body and standalone where the fixture differs. Target the shape of `filename_test.go` (#1772, 182 test lines) rather than `registry_test.go` (#1787, 271) — the fixtures here are chattier, so row discipline matters.

**The fixture rule that decides whether any of this proves anything.** Build every `want` from the `EvalSymlinks`-resolved instance directory, computed once in the fixture helper. Never from the raw `t.TempDir()` — on macOS that is `/var/folders/…`, a symlink to `/private/var/folders/…`, and a `want` joined onto the raw string fails for a reason that has nothing to do with the code. And never from `EnsureDir`'s own return value: a `want` derived from the return passes under *every* mutant, including one that skips step 4 and returns an unresolved path, which is the single thing AC 1 exists to catch. This has bitten the repo before, in `writeMCPSettings` using `Abs` against `streamsup`'s `EvalSymlinks`-resolved workdir.

**Assert the returned path in full**, with `==` against the composed `want`. Not `strings.HasPrefix`, not `strings.Contains` — a fragment check survives a build that drops the per-attachment component entirely, and that component is what AC 1's second sentence is about.

### Rows

Two canonical UUIDv4 constants (`convA`, `convB`) and two attachment-id constants (`aid1`, `aid2`), all satisfying `ValidID`.

**Success (AC 1)**
- `(convA, aid1)` against a fresh instance dir → returned path equals the composed `want`; the directory exists; `Stat().Mode().Perm()` is exactly `0o700`. One `MkdirAll` call creates every missing level with the same mode, so asserting the leaf covers the chain — do not write four permission assertions.
- `(convA, aid2)` in the same conversation → a *different* path from the first, both existing. This is the row that pins the per-attachment component.
- Instance dir absent before the call → succeeds and creates it. Pins the § "Two smaller decisions" choice.
- Called twice with `(convA, aid1)` → same path, nil error both times. Pins idempotency.

**Id refusal (AC 2)** — one table, rows for: empty conv id; conv id with `../`; conv id 35 chars; conv id uppercase-hex; empty attachment id; attachment id `../../etc/passwd`; attachment id of `MaxAttachmentIDBytes` bytes of hex (long but not canonical). Each asserts `errors.Is(err, ErrInvalidID)` **and** `!errors.Is(err, ErrNotContained)`.
- Shared assertion in the row body: **the instance dir path still does not exist** after the call. Point `instanceDir` at a path *under* `t.TempDir()` that the fixture never creates, so `os.Stat` returning `IsNotExist` is the assertion. This is sharper than diffing a directory listing and it is the sole red for a build that resolves the anchor before validating.

**Escaping symlink (AC 3)**
- Fixture: an instance dir, plus a separate `outside` dir from a second `t.TempDir()`. Create `<instance>/conversations/`, then `os.Symlink(outside, <instance>/conversations/<convA>)`. **The symlink target must exist** — `EvalSymlinks` on a dangling link returns an OS error, and the test would then assert the wrong sentinel for the right-looking reason.
- Assert `errors.Is(err, ErrNotContained)` **specifically**, and `!errors.Is(err, ErrInvalidID)` — the ticket's criterion says so in as many words, because an id refusal would otherwise satisfy the row.
- Assert `outside` contains no `attachments/` entry afterwards. "Nothing is created" means nothing *beneath* the pre-existing fixture symlink.

**Sibling-conversation symlink (AC 4)**
- Fixture: create `<instance>/conversations/<convB>/` as a real directory, then `os.Symlink` `<instance>/conversations/<convA>` → `<instance>/conversations/<convB>`. Both ids canonical.
- Assert `errors.Is(err, ErrNotContained)`, `!errors.Is(err, ErrInvalidID)`, and that `<convB>` gained no `attachments/` entry.

**Attachment-dir-itself-is-a-symlink** — one row: real `conversations/<convA>/attachments/`, with `<aid1>` inside it a symlink to `outside`. Falls out of the equality check for free and costs three fixture lines; it is the row that proves the check is on the *full* path and not on the conversation component alone.

### Mutants and their sole reds

Measure with `go test -overlay` against an absolute-path manifest — do not argue sole-redness from this table. It is a prediction, and this package's own history records a prediction that measured *larger* than predicted (§ "Mutation-testing lessons", first bullet); a measured set that is a superset of this one falsifies nothing, an empty one falsifies the row.

| Mutant | Expected sole red |
|---|---|
| Step 6 uses `os.Stat` instead of `os.Lstat` | AC 3 escaping-symlink row, and AC 4's |
| Step 7's check becomes a `withinDir`-style "is it under `root`" | AC 4 sibling row (and *only* it — this is the mutant the third row of § "Why equality" is about) |
| Step 4 anchors on the raw `instanceDir` instead of its `EvalSymlinks` | AC 1 success row, on macOS, via the `want` built from the resolved dir |
| Step 5 joins onto the resolved *conversation* dir instead of `root` | AC 3 escaping-symlink row |
| Step 5 drops the `attachmentID` component | AC 1's two-attachment row, and the full-path equality assertion |
| Steps 1–2 moved after step 3 | AC 2's "instance dir still does not exist" shared assertion |
| Step 8's `MkdirAll` mode becomes `0o755` | AC 1 permission assertion |
| Step 7's check deleted (check #2 retained) | AC 3 and AC 4 both — if either stays green, check #2 is silently doing the work and the pre-creation ordering is unpinned |

That last row is the one worth running first: it is the difference between "refused" and "refused *before creating anything*", which is the whole point of the ordering.

`make check` covers this package. No new build tag, no e2e work, nothing for the live suite.

## Open questions

- **Does `#1744` publish `conversations.ValidID`'s shape verbatim as the attachment id's client-visible contract?** This slice commits the daemon to it; publishing it is #1744's. If #1744 wants a different shape (a 64-hex token, say), the change is confined to the two validation calls in `EnsureDir` — but see § Security review, File operations, for the one property any replacement shape must keep.
- **Should `EnsureDir` also hand #1782 an `*os.Root`?** Returning `(string, error)` is what these criteria require, and a `Root` carries an fd with a lifecycle no criterion here covers. But #1782 opens a *file* beneath the returned directory, and that is where a swapped-symlink write actually matters. Recommendation for #1782, not a change here: open the destination file through `os.OpenRoot` on the returned path rather than a bare `os.Create`.
- **Nothing cleans these directories up.** Eviction, GC and the retention question are explicitly outside this slice and are not filed anywhere yet. Worth a ticket once #1782 is writing real bytes.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary is explicit and singular: `EnsureDir` is the only function that turns either id into a path component, and it validates both as its first two statements, before any filesystem call. Downstream holds a resolved absolute path — a value with no remaining client influence — so #1782 and #1746 inherit no obligation to re-validate. The typed `conversations.ConversationID` on one parameter and the plain `string` on the other is the type-system signal that they are different values; it does not signal trust level, and the doc comment says which is client-chosen.

- **[File operations — path traversal]** No MUST FIX. Neither id reaches `filepath.Join` unvalidated. `ValidID` admits 36 characters of lowercase hex and dashes at fixed offsets and nothing else, so a passing id contains no `/`, no `.`, no NUL and no `..` — it cannot spell any path component other than itself. The 64-byte `MaxAttachmentIDBytes` ceiling is satisfied structurally at 36 and is *not* what provides the defence, per its own doc comment.

- **[File operations — the id shape is load-bearing beyond traversal]** SHOULD FIX, and it lands on **#1744**, not here. `ValidID`'s lowercase-only alphabet is what makes the id→directory mapping injective on a **case-insensitive filesystem**, which APFS is by default. Any replacement shape #1744 publishes that admits mixed case (base64url, hex with uppercase) lets two *distinct* attachment ids resolve to one directory on macOS, and #1782's writes would then cross-contaminate between attachments — a confidentiality failure reached without any symlink at all. Recorded here because this spec is where the shape gets chosen and § Open questions is where #1744 will read it: **any replacement shape must be case-unambiguous.** Not a MUST FIX because the shape this slice ships has the property.

- **[File operations — symlinks]** No MUST FIX. The design does not follow symlinks blindly and does not merely refuse them: it resolves them with `EvalSymlinks` and requires the result to equal the textually expected destination, which refuses an escaping link, a sibling-conversation link, and a link on the leaf component alike (§ "Why equality and not `withinDir`"). `os.Lstat` rather than `os.Stat` in the ancestor walk is what makes a symlink visible to that resolution instead of stepped over; a mutant for it is listed with its sole red.

- **[File operations — TOCTOU]** SHOULD FIX, accepted with a stated bound. Steps 7→8 are a check-then-use: an attacker who can replace `conversations/<conv-id>` with a symlink in the window between check #1 and `MkdirAll` can have the directory created outside the instance directory, and check #2 catches it *after* creation rather than preventing it. Two things bound this. First, the capability required is write access to the daemon's own state directory (`~/.pyry/<name>/`, mode `0o700`), and anyone holding it can already rewrite `devices.json` and `sessions.json` — strictly worse outcomes than an empty misplaced directory. Second, this is the same residual window `confineWorkdirToHomeCreating` accepts for the same reason. `os.OpenRoot` (Go 1.26 is in `go.mod`; the API is unused in this repo today) would close it kernel-side on Linux — and was considered and rejected for this slice, because it permits in-root symlinks and so does not satisfy AC 4 on its own, leaving the equality check to be written anyway. It is the right primitive for #1782's file write, and § Open questions says so. Deferring is per Evidence-Based Fix Selection: no such failure has been observed, and a second mechanism layered over a check that already covers the case is belt-and-suspenders of the same fabric.

- **[File operations — permissions]** No findings. `0o700` on every created directory, matching `Registry.Save` in both `internal/conversations` and `internal/devices`. Stated explicitly in the algorithm, asserted in the tests, and mutant-covered. `0o700` has no group or other bits to lose, so no plausible umask weakens it.

- **[File operations — atomic writes]** Not applicable, and deliberately so. This slice creates directories only and writes no file, so there is no partial-state-on-disk failure mode to make atomic. A half-created directory chain from an interrupted `MkdirAll` is re-entered harmlessly by the next call (idempotency, § "The contract"). #1782 owns the file write and inherits the temp-file-plus-rename question.

- **[Error messages, logs, telemetry]** No MUST FIX. `ErrNotContained` names both the resolved and the expected host path, which discloses the daemon's layout — that is intended and is safe *only* because `attachment.storage_failed` is contractually a static message that carries neither the host path nor the underlying filesystem error, and #1744's mapping is the thing that enforces it. Recorded so #1744 does not wrap-and-forward. `ErrInvalidID` includes the offending id, which is client-supplied: it must not reach a line-oriented log raw, on the log-injection grounds `AttachmentChunkPayload`'s SECURITY block already applies to filenames. This package makes zero log calls and this slice keeps it that way, so the obligation is #1744's at the point where it decides what to log. No attachment *content* and no filename is in scope here at all.

- **[Concurrency]** No findings. No goroutines, no locks, no shared state, no lock ordering to document. `MkdirAll` is idempotent under concurrent calls for the same pair. Mid-signal interruption leaves at worst a partially created directory chain, which the next call completes.

- **[Tokens, secrets, credentials]** Not applicable. This slice generates, stores and compares nothing secret. Worth stating rather than skipping because `attachment_id` looks like a token and is not one: `AttachmentChunkPayload`'s SECURITY block records that it is not secret, not unguessable, and never the only thing between a caller and a file — and this design relies on none of those properties. Containment here comes from the resolution check, never from an id being hard to guess.

- **[Cryptographic primitives]** Not applicable. No RNG, no hashing, no comparison against a secret. `ValidID` compares an id against a *shape*, not against a secret value, so constant-time comparison is not indicated.

- **[Subprocess / external command execution]** Not applicable. No `exec`, no shell, no environment inheritance.

- **[Network & I/O]** Not applicable to this slice. It reads no socket and has no size limit to set: the per-upload byte bound is `maxUploadBytes` (#1777), the in-flight upload count is #1786's, and the frame-level caps are `internal/protocol`'s. This slice's own resource cost is one directory per (conversation, attachment) pair, unbounded in count — which is a real exhaustion surface (inode consumption via repeated uploads) and is **OUT OF SCOPE**: it is gated upstream by #1786's entry-count cap on in-flight uploads and by #1742's expiry of abandoned ones, and cleanup of *landed* attachments has no ticket yet (§ Open questions).

- **[Threat model alignment]** Addressed. `docs/protocol-mobile.md` § Attachments states two threats this slice owns and both are refused by construction: a client-chosen id reaching `filepath.Join` unvalidated (steps 1–2), and a client steering bytes into another conversation's directory (the frame carries no `conversation_id` at all, and step 7's equality check refuses a sibling-conversation redirection even when the on-disk layout tries to perform one). The published rule that the 64-byte ceiling is not the containment mechanism is honoured — the resolution check is.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25
