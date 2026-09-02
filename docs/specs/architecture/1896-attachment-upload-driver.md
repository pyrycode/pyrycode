# #1896 — Drive one decoded attachment chunk from admission to stored bytes

## Files to read first

- `internal/attachments/registry.go` → `Registry.Admit`, `Registry.Lookup`,
  `Registry.Deliver`, `Registry.ReleaseConn`, `ErrUnknownUpload` — the four
  primitives this slice sequences, and the one sentinel the sequence must not
  be able to raise. `Lookup`'s doc names this dispatch site as its reason to
  exist: a pure, non-stamping, non-reaping read.
- `internal/attachments/storage.go` → `EnsureDir`, `Store`, `ErrInvalidID`,
  `ErrNotContained`, `ErrWriteFailed` — the completion half, its typed
  `conversations.ConversationID` parameter, and the LOGGING OBLIGATION clause
  `Store`'s doc places on its consumer.
- `internal/attachments/accumulator.go` → `ErrIncomplete` — the one non-latching,
  non-discarding sentinel, and the whole reason this seam answers three ways.
- `internal/protocol/attachments.go` → `AttachmentChunkPayload` (the SECURITY
  block's four never-logged strings; the absent `conversation_id` and why),
  `AttachmentStoredPayload` (#1895 — what the success answer may and may not
  name; it rules out the stored path by name).
- `cmd/pyry/relay.go` → `boundSessionIDForActive` — the nearest precedent for
  reading the daemon's conversation cursor, and for refusing the empty and
  unknown cases explicitly instead of falling through to a bootstrap default.
- `internal/relay/v2session_seams.go` → `QueueRemover`, `SettingsUpdate` — the
  shape #1897's seam interface will take, and the reason this slice's exported
  surface stays primitives plus `protocol.AttachmentChunkPayload`.
- `docs/knowledge/features/attachments-package-in-flight-upload-registry.md` —
  the sequencing constraint (call `Deliver` with the same chunk just passed to
  `Admit`, never `Admit`'s returned accumulator), and the recorded gap that a
  structural never-log claim is unpinned until a test puts the banned string
  back.
- `docs/knowledge/features/attachments-package-sentinels-and-discard-semantics.md`
  — which sentinels latch, which discard, and that mapping to wire codes is not
  this package's.
- `docs/protocol-mobile.md` § Attachments — chunks may arrive in **any order**
  (the rule a reader would wrongly copy from `debug_bundle_chunk`'s strict
  `seq`), and the two receiver bounds that stay unpublished.

## Context

`internal/attachments` ships every primitive of the upload leg and has no
production caller at all. Driving them in the right order is nobody's job: the
fork between a first-to-arrive chunk and a later one, the three-way answer
`ErrIncomplete` forces, and the conversation the bytes are filed under are three
decisions rather than sequencing taste. This slice ships that driver as the
package's composite entry point. It maps nothing to the wire — turning these
sentinels into `attachment.*` codes is #1897's, which also declares the seam
interface in `internal/relay` and builds the production conversation resolver.

Following #1817's shape, the primitive lands with its call site in the next
slice; nothing outside this package calls it when this ticket merges.

No ADR is warranted: every decision here is local to one package and is recorded
in the doc blocks and in this spec.

## Design

One new file, `internal/attachments/intake.go`, adding one exported type, one
constructor and one sentinel. No existing file changes; the package needs no new
imports (`errors`, `internal/conversations` and `internal/protocol` are all
already used here).

### The type

`Intake` composes a `Registry` it owns with the two storage functions and a
construction-time conversation resolver:

- `NewIntake(instanceDir string, conversation func() (conversations.ConversationID, bool)) *Intake`
- `func (i *Intake) Receive(connID string, chunk protocol.AttachmentChunkPayload) (attachmentID string, stored bool, err error)`
- `func (i *Intake) ReleaseConn(connID string)`

`Receive` and `ReleaseConn` are the seam #1897 names; the constructor is not part
of it, which is what lets the resolver keep the `conversations.ConversationID`
type while `internal/relay` imports neither `internal/attachments` nor
`internal/conversations`. The typed resolver is deliberate: `EnsureDir` takes
conversation id and attachment id as two strings that are both canonical UUIDs,
so typing one of the two makes a swap at this call site a compile error rather
than a valid-but-wrong path.

`Intake` **owns** its `Registry` rather than taking one, because
`maxInFlightUploads` is a daemon-wide ceiling: one `Intake` per daemon is the
only configuration under which that bound means what it says, and an injected
registry would publish a second way to reach it.

`instanceDir` is captured once. It is the same host directory the other
registries under it persist into, and resolving it per chunk would buy nothing.

### The fork

`Receive` reads `Lookup(connID, chunk.AttachmentID)` first, and admits only on a
miss:

1. **Not in flight** → `Admit(connID, chunk.AttachmentID, chunk.TotalChunks,
   chunk.Size, chunk.SHA256)`. A refusal returns that error verbatim and the
   chunk is dropped. Nothing reads `chunk.Index` here, so the opening chunk of a
   conforming client is admitted whatever index it carries.
2. **In flight, or just admitted** → `Deliver(connID, chunk)` with the *same*
   chunk, which looks the pair back up rather than feeding the accumulator
   `Admit` returned. `Admit`'s answer is discarded with `_` at the call, since
   feeding it directly bypasses every release `Deliver` owns and re-opens the
   lockout for single-chunk transfers.

`Lookup` and not "call `Deliver` and fall back on `ErrUnknownUpload`": the second
shape reinterprets a refusal sentinel as a routing decision, which AC 1 forbids,
and it costs an extra pass through the registry on every first chunk.

`ErrUnknownUpload` is therefore not reachable from `Receive` on any single-feeder
path — a chunk either finds its transfer or has just admitted one. It stays
reachable in two third-party windows, both benign: `ReleaseConn` running on the
teardown goroutine, and `uploadIdleTimeout`'s lazy reap firing inside `Deliver`'s
own look-up between this method's `Lookup` and its `Deliver`. Both mean the
transfer is genuinely gone, and the sentinel travels out verbatim.

### The three answers

| Answer | Shape | Meaning |
|---|---|---|
| stored | `(chunk.AttachmentID, true, nil)` | the completing chunk's verified bytes are on the host |
| accepted | `("", false, nil)` | the chunk was taken and the transfer wants more |
| refused | `("", false, err)` | some layer's own sentinel, `errors.Is`-reachable |

`err != nil` is the caller's whole refusal test, and `stored` separates the other
two, so #1897 never tests for a particular sentinel to decide what to emit.
`ErrIncomplete` is the one error this seam interprets rather than passes on — it
is not a refusal, it is the answer most chunks of a healthy upload get — and it
is converted at exactly one `errors.Is` on `Deliver`'s answer.

### The completing chunk

Only the completing chunk resolves a conversation, and only after `Deliver` has
already released the slot:

1. `conversation()` → on `!ok` (or a nil resolver, or an empty id) refuse with
   the new `ErrNoConversation`. Not by handing `""` to `EnsureDir`, which would
   answer `ErrInvalidID` and report a daemon that is on no conversation as a
   malformed identifier.
2. `EnsureDir(instanceDir, convID, chunk.AttachmentID)` → errors verbatim
   (`ErrInvalidID` for a non-canonical attachment id, `ErrNotContained`, or a
   wrapped filesystem error).
3. `Store(dir, chunk.Filename, data)` → its error verbatim; **its returned path
   is discarded**, deliberately, since it embeds the sanitised filename and
   `AttachmentStoredPayload`'s doc names reaching for it as the natural wrong
   move.
4. answer `(chunk.AttachmentID, true, nil)`.

Two conns uploading under one `attachment_id` into one conversation resolve to
the **same** directory and the same stored name, and are last-writer-wins,
atomically — `Store`'s documented behaviour, whose doc adds "which is the only
way the dispatch site can reach it" on the assumption that dispatch only ever
holds distinct dirs. This seam falsifies that clause and its doc block says so.
It is not a privilege boundary: both conns are already-paired devices of one
account, the id is client-chosen and published as "not a capability", and
separating them would need a per-conn path component that retrieval (#1746,
which addresses by conversation and attachment id) does not have.

Resolving on the completing chunk rather than at admission is forced: `Registry`
has nowhere to stash a per-transfer conversation id and adding one is a different
slice. Two consequences are accepted rather than mitigated — a phone that
switches conversations mid-upload files under the new one, and an upload begun
while the daemon is on no conversation is refused only when its last chunk
arrives. Both are stated in the doc block.

A refusal at any of steps 1–3 arrives **after** the transfer is over: `Deliver`
released the entry and `reject` dropped nothing (the bytes assembled fine). No
slot leaks; the client must re-upload.

## Concurrency model

No goroutine is spawned and none is joined; there is nothing to shut down.

`Receive` inherits the single-feeder precondition `Admit` and `Deliver` both
carry, and its doc must restate it: exactly one goroutine at a time may call it
for a given conn, discharged at #1897 by `appFrameWorker`. Distinct conns are
safe concurrently — the conn is in the registry key, and `EnsureDir` and `Store`
are safe for concurrent use on distinct destinations.

`ReleaseConn` is the documented exception: remove-only, no accumulator touched,
meant for the teardown goroutine, so it needs no such constraint and this seam
adds none.

The registry's mutex stays a leaf. `Receive` takes no lock of its own and holds
none across `EnsureDir` or `Store`, which are the two slowest things on the path.

## Error handling

- Every refusal is another function's error returned **verbatim** — no `%w`, no
  annotation, no reinterpretation — so `errors.Is` reaches `ErrInvalidDeclaration`,
  `ErrUploadTooLarge`, `ErrTooManyUploads`, the six accumulator sentinels,
  `ErrUnknownUpload`, `ErrInvalidID`, `ErrNotContained` and `ErrWriteFailed`
  unchanged.
- One new sentinel, `ErrNoConversation`, bare: `errors.New("attachments: no
  conversation to file this attachment under")`. Distinct from `ErrInvalidID` on
  purpose — that one says a client (or the daemon) named a malformed id, this one
  says the daemon named nothing at all, and only the second can clear by itself
  once a conversation is routed. #1897 maps it; no wire code is named here.
- **There is no format string in this file.** That keeps the never-log rule
  `AttachmentChunkPayload`'s SECURITY block states structural at a function
  holding all four banned strings, exactly as `Deliver`'s body does.
- One pass-through carries an obligation forward rather than discharging it:
  `Store`'s rename leg wraps an `*os.LinkError` whose `Error()` prints the
  destination, hence the sanitised filename. This seam constructs nothing there;
  the doc block repeats `Store`'s LOGGING OBLIGATION so #1897 maps it to the
  static `attachment.storage_failed` message and logs the sentinel and the ids
  rather than the error text.

## Testing strategy

New `internal/attachments/intake_test.go`, reusing the package's fixtures
(`testFixture`, `testFixtureDigest`, `testChunk`) and `storage_test.go`'s
`resolvedInstanceDir`, `wantDir`, `assertDirEntries` and canonical id constants.
A canonical `aid1` is required wherever a test reaches storage, since
`registry_test.go`'s `att-1` fails `EnsureDir`'s shape check.

- **The fork.** A first chunk whose `Index` is not 0 (of a two-chunk transfer) is
  admitted and accepted, and the registry holds exactly one entry — the sole red
  for an admission gated on `index == 0`. A second chunk for the same pair
  completes it without a second entry ever existing.
- **Three answers.** One test asserting each of the three shapes on one transfer:
  the non-completing chunk answers `("", false, nil)`, the completing one
  `(aid1, true, nil)` with the bytes on disk under
  `conversations/<conv>/attachments/<aid>/`, and a refusal answers a non-nil
  error with an empty id.
- **Refusal pass-through**, table-driven, one row per layer, each asserting
  `errors.Is` reaches the layer's own sentinel: an inadmissible declaration
  (`ErrInvalidDeclaration`), an oversized one (`ErrUploadTooLarge`), a fifth
  concurrent pair (`ErrTooManyUploads`), a framing refusal on a later chunk
  (`ErrDuplicateIndex`), an integrity refusal on the completing chunk
  (`ErrDigestMismatch`), a non-canonical attachment id (`ErrInvalidID`).
- **`Admit`'s accumulator is not fed.** A single-chunk transfer stores and leaves
  the registry empty, and a second identical `Receive` stores again — the sole red
  for a build that feeds `Admit`'s return value, which double-adds index 0 and
  leaves the pair locked out.
- **No conversation.** Resolver answering `false`, answering `("", true)`, and a
  nil resolver: each refuses the completing chunk with `ErrNoConversation`,
  writes nothing under the instance directory, and leaves the registry empty.
  A separate row asserts `ErrNoConversation` is distinct from every sentinel in
  `packageSentinels` (extended with it).
- **Conn teardown (AC 4).** Fill the registry to `maxInFlightUploads` across one
  conn with incomplete transfers, assert the next pair is refused, `ReleaseConn`,
  then assert a fresh pair is admitted — capacity actually came back. A second
  case asserts another conn's uploads survive it.
- **Nothing leaks (AC 5), the test that puts the banned string back.** Every
  refusal reachable from `Receive` is driven with a chunk whose `Filename`,
  `SHA256` and `Data` are distinctive non-empty literals, and each error string
  is asserted to contain none of the three. Needles are asserted non-empty first,
  so no row can go vacuous. The success answer needs no such test: it is a
  `string` and a `bool`, and the id is the one value that is allowed.

Mutants worth naming, each expected to be a sole red: gating admission on
`chunk.Index == 0`; feeding `Admit`'s returned accumulator; returning
`Deliver`'s `ErrIncomplete` as a refusal; passing `""` to `EnsureDir` instead of
refusing; returning `Store`'s path as the answer; wrapping any pass-through in a
`fmt.Errorf` that interpolates the filename or the digest.

## Open questions

- **Does an expired transfer's next chunk restart the upload?** Under this fork,
  yes: `Lookup` misses on a reaped entry, so the chunk is treated as
  first-to-arrive and admitted afresh. That is the honest reading of "no transfer
  in flight" and leaks nothing (the reap already returned the slot); the transfer
  then completes only if the client re-sends every chunk. Resolve by stating it
  in the doc block, not by adding a branch.
- **Should `Receive` refuse an attachment id of non-canonical shape at
  admission?** No — explicitly out of scope per the ticket: `docs/protocol-mobile.md`
  § Attachments publishes that nothing checks it at admission today, and #1897
  owns that check and the code to answer with. `EnsureDir`'s `ErrInvalidID` goes
  back unchanged.

## Sizing, measured

Estimated total written work ~815 lines: ~250 production, ~350 test, 215 spec.
The refiner's estimate was ~700 against #1781, whose `feat` commit re-derives to
553 insertions (204 production + 344 test + 7 elsewhere) plus a 239-line spec,
so 792 — the ticket's cite is accurate and this slice sits just above it, at or
slightly over the 800-line size-S ceiling.

It is built rather than split, for two independent reasons. **Depth:** #1896's
parent is #1744 and its grandparent #1684, so the two-level split cap is already
spent. **Floor:** the only seam this design offers — fork-and-deliver without
storage, then storage on top — produces a first child whose sole consumer is its
own sibling, which is the one-consumer shape that must be merged back even when
the ceiling disagrees. There is one deliverable here (one call, one set of
sentinels) and it is not divisible into two things that each land and can be
checked alone. The overage is stated rather than engineered around.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. `Receive` is the single explicit boundary and
  it validates nothing itself, by design: `chunk.TotalChunks` and `chunk.Size`
  are range- and cross-checked by `Admit` before anything is sized (nothing in
  this file allocates from a claim), `chunk.AttachmentID` is shape-checked by
  `EnsureDir` before it becomes a path component, `chunk.Filename` reaches only
  `SanitizeFilename` through `Store`, and `chunk.Data` reaches only `Add`.
  `connID` is daemon-side and never on the wire. A later chunk is judged against
  the declaration its transfer was *admitted* under — `Add` reads neither `Size`
  nor `SHA256` — so a client cannot swap either underneath bytes it already sent.
- [File operations] No findings. This file builds no path and calls no
  `filepath` function: `EnsureDir` owns canonicalisation, both containment checks
  and 0700 creation, and `Store` owns 0600 and temp-plus-rename atomicity, so
  there is no second place for either to drift. The conversation id is
  daemon-side and is *still* shape-checked by `EnsureDir`, which is defence in
  depth this seam inherits rather than weakens. No check-then-use pair is
  introduced; `EnsureDir`'s own two-check residual window is unchanged.
- [Network & I/O — resource exhaustion] No findings on what this slice adds, and
  this is the fork's actual security property: entries are created **only** by
  `Admit`, so `maxInFlightUploads`, `maxUploadBytes` and `CheckDeclaration` are
  on the path of every first-to-arrive chunk whatever index it carries, and there
  is no route into the registry that skips them. `uploadIdleTimeout` and AC 4's
  `ReleaseConn` are what stop held capacity outliving a phone. That one conn may
  take all four daemon-wide slots is #1796's decided policy, inherited here, not
  introduced.
- [Error messages, logs] SHOULD FIX, discharged in Phase B by doc text. This file
  has no format string, which is what keeps the never-log rule structural, and AC
  5's fixture is what pins that against a future wrap. One pass-through can still
  carry a sanitised filename: `Store`'s rename leg wraps an `*os.LinkError` whose
  `Error()` prints its destination. `Receive`'s doc block must repeat `Store`'s
  LOGGING OBLIGATION so #1897 maps it to the static `attachment.storage_failed`
  message and logs the sentinel and the ids, never the error text. `EnsureDir`'s
  refusals can carry the daemon's own conversation id and host paths; both are
  operator-log material that the same static mapping keeps off the wire.
- [Concurrency] No findings, two consequences named. The single-feeder
  precondition travels with the seam and its doc restates it; `ReleaseConn` is
  the documented exception, remove-only and meant for the teardown goroutine.
  `Receive` takes no lock and holds none across `EnsureDir` or `Store`, so
  `Registry.mu` stays a leaf even though this is the first caller to put
  filesystem I/O on the same path. (a) A teardown landing between `Deliver` and
  `Store` still files the bytes — they were authenticated and integrity-checked
  before the conn went away, and cancelling mid-store would need a context this
  seam does not have. (b) Two conns sharing one attachment id in one conversation
  collide last-writer-wins; see § Design, where the falsified clause in `Store`'s
  doc is recorded.
- [Threat model — `docs/protocol-mobile.md` § Attachments] No findings. The
  section's load-bearing property is that `attachment_chunk` carries no
  `conversation_id` so a client cannot steer bytes into another conversation.
  This design discharges it **structurally** rather than by a check: the
  conversation is a construction-time resolver, `Receive` has no conversation
  parameter at all, and no field of the chunk is read into one. The neighbouring
  property — `attachment_id` is not a capability — holds because the conn is in
  the registry key, so possession of an id routes nothing.
- [Tokens, secrets, credentials] Not applicable: this seam mints, stores and
  compares no secret. The declared `sha256` is integrity and never a lookup key,
  and the attachment id is published as non-secret and non-unguessable.
- [Subprocess execution] Not applicable: no `exec`, no shell, no environment.
- [Cryptographic primitives] Not applicable: no RNG and no comparison here. The
  digest comparison is `Assemble`'s exact lowercase-hex `!=`, on attacker-supplied
  bytes against an attacker-supplied claim, so constant time buys nothing.
- [Resource lifecycle] OUT OF SCOPE, and no ticket owns it today: nothing in this
  family deletes a stored attachment or bounds how many one account may
  accumulate, so a client may grow the instance directory one 16 MiB upload at a
  time. `EnsureDir` runs only after integrity passes, so a refused transfer
  creates no directory, and a `Store` failure leaves at most one empty 0700 dir.
  Retention is a policy question for the family owner rather than something this
  seam can answer; flagged here for the refiner.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
