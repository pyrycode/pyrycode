# #1784 — release an in-flight upload's slot when it completes or is refused

`internal/attachments`. Adds one exported sentinel and one routing method to
`Registry`. No disk, no wire codes, no logging, no new production consumer.

## Files to read first

Symbols, not lines — resolve each with `codegraph_search` / `codegraph_node`.

- `internal/attachments/registry.go` → `Registry` (type doc), `Lookup`, `Release`,
  `Admit`, `insertLocked`, `uploadKey` — the lock posture this slice inherits
  verbatim, the two primitives it drives, and the gate order AC 1 defends.
  `Lookup`'s and `Release`'s doc blocks both name this ticket by number and both
  need that replaced with the symbol that now does the work.
- `internal/attachments/accumulator.go` → `Add`, `Assemble`, `reject`,
  `Accumulator` (field docs) — the three outcomes `Deliver` routes on, and the
  latch/discard semantics `reject` already provides. Read `Add`'s fixed check
  order and its "AttachmentID is not checked because the caller looks this
  accumulator up BY it" paragraph: this slice makes that sentence structurally
  true rather than a caller obligation.
- `internal/attachments/admission.go` → `ErrTooManyUploads`, `capacityRefusal`,
  `maxInFlightUploads`, `maxUploadBytes` — the sentinel-doc house style the new
  sentinel copies, and why a new sentinel is a sibling rather than a widening of
  a neighbour. Also `CheckDeclaration`, and read it **before writing a single
  fixture**: every declaration a `Deliver` test uses has to pass it, because
  `Admit` is the only way to get an entry into the registry, and the rule is
  `totalChunks == max(1, ceil(size / protocol.MaxAttachmentChunkBytes))` with
  that constant at 45000. `newTestAccumulator`'s `(testTotal, len(testFixture))`
  is **not** admissible — `testParts` splits 24 bytes into three chunks, which
  only `NewAccumulator` accepts, never `Admit`. See § "The one admissible
  multi-chunk declaration".
- `internal/attachments/registry_test.go` → `fillRegistry`, `testConnA`,
  `testConnB`, `testAttachmentID`, `testBoundTotal`, `boundChunk`,
  `TestRegistry_AdmitAtTheBound_ReleaseReturnsTheSlot` — every fixture and
  assertion idiom the new tests reuse. `fillRegistry` is the "fill to the bound"
  helper all of AC 1/2/3 needs; do not write a second one.
- `internal/attachments/accumulator_test.go` → `testChunk`, `newTestAccumulator`,
  `testFixture`, `testParts`, `testTotal`, `testFixtureDigest`,
  `testEmptyDigest`, `testBoundFixture`, `testBoundFixtureDigest` — the shared
  fixtures. Note `testChunk` deliberately leaves `AttachmentID` zero; see
  § "One new test helper".
- `internal/protocol/attachments.go` → `AttachmentChunkPayload`,
  `MaxAttachmentChunkBytes` — the eight wire fields, and the SECURITY block
  naming the four strings that must never enter an error or a log
  (`AttachmentID`, `Filename`, `SHA256`, `Data`).
- `docs/knowledge/features/attachments-package.md` § "In-flight upload registry",
  § "Sentinels and discard semantics", § "Mutation-testing lessons" — the parked
  #1796 SHOULD FIX this ticket discharges, the family table AC 3 sweeps, and the
  overlay-mutant recipe with its build-failure traps.

## Context

`Registry` bounds how many uploads may be in flight (`maxInFlightUploads`, gated
inside `insertLocked`), but nothing removes an entry. A bound with no release is
a lockout after N transfers, and a later chunk for an already-admitted pair has
nowhere to go at all. `Lookup` and `Release` shipped in #1787 as primitives with
no driver; this slice is the driver.

Two things end an upload here — it completes, or a chunk (or the assembly) is
refused — and both must give the slot back. Expiry of a stalled upload is
#1742's; the dispatch site that drives this registry and maps its sentinels to
wire codes is #1744's.

No ADR is warranted: this adds no cross-cutting decision, only the second half of
a mechanism `Registry`'s existing type doc already describes.

## Size check

`s`, unchanged. Production source files touched: 1 (`registry.go`). New exported
symbols: 2 (`ErrUnknownUpload`, `Registry.Deliver`). Consumer call sites needing
simultaneous update: 0 — nothing in `cmd/` or `internal/` outside this package
imports `attachments`, verified by grep across the tree. Acceptance criteria: 5.
Distinct refusal branches in `Deliver`: 3 (unknown pair, `Add` refusal,
`Assemble` refusal). Estimated total written work ≈ 375 lines: ~95 production
(the method plus two sentinel/type-doc blocks at this package's doc density) and
~280 test, with AC 2 and AC 3 sharing one table and AC 1's two clauses sharing
one setup. Nearest analogue #1787 landed at 399 lines across 3 files; #1788 at
280.

## Design

### The routing surface

```go
func (r *Registry) Deliver(connID string, chunk protocol.AttachmentChunkPayload) ([]byte, error)
```

Contract, in the order the body runs it:

1. **Look the pair up** with `Lookup(connID, chunk.AttachmentID)`. On a miss,
   return `nil, ErrUnknownUpload` and store nothing — `Lookup` creates no entry,
   and neither does this path.
2. **Feed off-lock.** Hand the chunk to the accumulator's `Add`. On any non-nil
   error: `Release` the pair, then return `nil` and that error verbatim.
3. **Ask `Assemble`.** Keep the entry when — and only when —
   `errors.Is(err, ErrIncomplete)`; return `nil, err` and touch nothing.
4. **Every other outcome of `Assemble` releases**, the nil-error one included:
   `Release` the pair, then return `Assemble`'s two values verbatim. On success
   that is the assembled bytes and a nil error; on `ErrSizeMismatch` /
   `ErrDigestMismatch` it is nil bytes and the latched refusal.

**The keep case is the allow-list, not the release case, and that polarity is the
contract.** Written the other way — enumerate the refusals that release — a
further latching answer added to `Assemble` later would silently leak a slot per
corrupt transfer, which is the lockout this ticket exists to remove. Written
this way, `ErrIncomplete` is the one documented non-latching answer and everything
else gives the slot back by default. Same polarity on the `Add` leg: every
non-nil answer from `Add` latches (all four are `reject`'s return, and the
already-refused arm returns a previously latched one), so `err != nil` is the
release condition rather than a list of four sentinels.

**`Deliver` calls `Assemble` after every accepted chunk, not only the last.**
That is cheap by construction: `Assemble` returns `ErrIncomplete` from its
completion check before allocating or hashing anything, so only the completing
chunk pays for the copy and the digest.

**Naming.** `Deliver` pairs with `Admit` in the same custodial register the
package already uses (`Admit` / `Lookup` / `Release`); `Add` is taken by
`Accumulator` and reusing it across two types in one package would make every
call site ambiguous.

### The key comes out of the chunk, not out of a second parameter

`Deliver` takes the whole payload and reads `chunk.AttachmentID` for the key. It
does **not** take `attachmentID` as a scalar beside the chunk, which is the
shape `Admit` uses and the wrong one here: two ids in one call can disagree, and
a caller that passes the wrong one routes this chunk's bytes into a different
transfer's accumulator — `Add` reads neither the id nor the declared size or
digest, so nothing downstream would catch it. One id in play forecloses that
structurally, and it is what turns `Add`'s existing claim — "AttachmentID is not
checked because the caller looks this accumulator up BY it, so a foreign chunk
cannot reach here" — from a caller obligation into a property of this package.

`connID` stays a scalar parameter because it is not on the wire: it is the
receiver's own name for the conn the frame arrived on, and it is the half of the
key that makes `attachment_id` safe to key on at all.

### `ErrUnknownUpload`

```go
var ErrUnknownUpload = errors.New("attachments: no upload in flight for this conn and attachment_id")
```

- **Lives in `registry.go`, beside the method that raises it.** Not
  `accumulator.go`, whose var block opens "Sentinel errors returned by Add and
  Assemble" and this is returned by neither; not `admission.go`, whose sentinels
  all refuse a *declaration* at admission, where this one refuses a chunk for a
  transfer that was never admitted or is already over. `storage.go` is the
  precedent for a sentinel living with its raiser rather than in a package-wide
  block.
- **Distinct from every sentinel this package already publishes**, per AC 5, and
  the test asserts that against the whole set rather than against a sample.
  `ErrIncomplete` is the near miss a reader might reach for and is its opposite:
  `ErrIncomplete` says a live transfer is waiting for more chunks, this one says
  there is no transfer.
- **Returned bare — never wrapped.** `Deliver` formats no error of its own, the
  same posture `Admit`'s doc states for the admission path and for the same
  reason, sharper here: `Deliver` is the only function in this package that holds
  all four of the strings `AttachmentChunkPayload`'s SECURITY block bans from
  messages and logs. A body with no format string in it cannot leak one. There is
  nothing to add anyway — the pair is the caller's own two values.
- The doc block must say that mapping it to a wire code is #1744's job and name
  no candidate code. None of the `attachment.*` codes `internal/protocol`
  publishes today means "no live transfer under this pair" —
  `CodeAttachmentNotFound` is the retrieval leg's, and its message is
  deliberately static — so whether this folds into `CodeAttachmentInvalidChunk`
  or wants a code of its own is #1744's decision. Pre-empting it here would
  publish a mapping this package deliberately does not own.

### Doc blocks to amend (all in `registry.go`, one or two sentences each)

These are not cosmetic — each is a statement the new method makes false or stale.

1. `Registry`'s type doc says "insertLocked is the one method here that takes no
   lock." With `Deliver` that is wrong. Amend to name both, for opposite reasons:
   `insertLocked` runs with `mu` held by its caller; `Deliver` never holds `mu`
   at all, composing `Lookup` and `Release` with the accumulator fed between
   them, off-lock. Note explicitly that this does not breach the leaf rule the
   same doc states — "no method takes `mu` and then calls another that takes it"
   is satisfied by a composer that takes no lock.
2. `Lookup`'s doc says "the bool is what #1784 turns into 'chunk for an unknown
   transfer'". Replace the ticket number with `Deliver` and `ErrUnknownUpload`.
3. `Release`'s doc says "It returns nothing because #1784 releases what it just
   looked up". Same: name `Deliver`, and keep the #1742 half as it stands.

Do not rewrite these blocks wholesale. Anything else in them stays.

## Concurrency model

No new goroutines, no new lock. `Deliver` takes `mu` **never**; it composes
`Lookup` and `Release`, each of which takes it for its own whole body, and feeds
the accumulator between them with the lock released. Three acquisitions per
delivered chunk, and `mu` stays a leaf: it is never held across `Add` or
`Assemble`, which is the rule `Registry`'s type doc already states and which this
method must not be the first to break.

**Why the gap between look-up and release is not a TOCTOU hole.** The conn is in
the key, and relay spawns exactly one `appFrameWorker` per session, so exactly
one goroutine can reach any one accumulator — the same precondition `Admit`'s
doc block already states and hands back an accumulator under. This slice inherits
that posture; it does not re-derive it and must not add a second one.

**Interleaving with a future reaper.** When #1742 lands, an entry can vanish
between this method's `Lookup` and its `Release`. Both halves are already safe:
`Release` on a pair the registry no longer holds is a documented no-op, and a
`Lookup` that misses answers `ErrUnknownUpload`, which is the honest answer for a
transfer that has been reaped. Nothing here needs to change for #1742, and this
spec adds no expiry of its own.

## Error handling

| Outcome | What `Deliver` returns | Entry after |
|---|---|---|
| Pair not held | `nil`, bare `ErrUnknownUpload` | none created |
| `Add` refuses (any of the four `Add` sentinels) | `nil`, `Add`'s error verbatim | released |
| `Assemble` says `ErrIncomplete` | `nil`, `ErrIncomplete` | **kept** |
| `Assemble` refuses (`ErrSizeMismatch`, `ErrDigestMismatch`) | `nil`, the refusal verbatim | released |
| `Assemble` succeeds | the assembled bytes, `nil` | released |

Three consequences worth stating in the method's doc block, because each is a
question a reader will otherwise have to re-derive:

- **`reject`'s latch stays load-bearing and must not be touched.** Release
  recovers the entry; `reject` recovers the memory, at the moment of refusal,
  and is the only thing that does. A refused accumulator is simply no longer
  reachable *through the registry* once `Deliver` has released it.
- **A chunk arriving after a refusal answers `ErrUnknownUpload`, not the latched
  sentinel.** That is a change in what a client sees across a transfer's
  lifetime, and it is correct: the latch exists so that one *live* transfer
  cannot produce two contradictory diagnoses of its own bytes, and after release
  there is no transfer left to diagnose. "No such transfer" is a statement about
  the registry, not a second verdict on the bytes.
- **A re-admitted pair is a new transfer, and a straggler chunk from the old one
  cannot corrupt it.** The same `attachment_id` may be admitted again the moment
  its slot is free — no blacklist, per the ticket — so a late chunk can land in
  the successor's accumulator. `Assemble` compares the declared size and digest
  against the whole assembled slice, so the only reachable outcome is that the
  client's own new transfer fails its own integrity check. Cross-client
  contamination is foreclosed a level up, by the conn being in the key.

## Testing strategy

`internal/attachments/registry_test.go`, package-internal, `t.Parallel()`
throughout, existing fixtures only.

### The one admissible multi-chunk declaration

`Admit` runs `CheckDeclaration`, so a test cannot invent a declaration the way
`NewAccumulator` lets `accumulator_test.go` invent one. `testBoundTotal`'s own
doc states the consequence: `(testBoundTotal, maxUploadBytes,
testBoundFixtureDigest)` is **the one admissible multi-chunk declaration this
package has fixtures for**, fed with `boundChunk(i)`. Anything that needs a
transfer to stay incomplete across two deliveries has to use it —
`(testTotal, len(testFixture))` is refused by `CheckDeclaration`, because 24
bytes is one chunk, not three.

Single-chunk transfers are cheap and unrestricted: `(1, len(testFixture),
testFixtureDigest)` fed with `testFixture` as one chunk assembles to the fixture,
and any size at or below 45000 is admissible with a count of 1. Use those wherever
the row does not need to survive a first chunk, and the 16 MiB fixture only where
it does. `testBoundFixture` is package-level and shared: slice it, never mutate
it, never rebuild it.

### One new test helper

`testChunk` deliberately leaves `AttachmentID` zero, and its doc says so — that
absence is the statement that `Add` reads none of those fields. Do not change it,
and do not fork `boundChunk` either. Add one wrapper taking a chunk and an id and
returning the chunk with `AttachmentID` set (a value parameter, so the caller's
copy is untouched); it composes with `testChunk` and `boundChunk` alike, and it
is the single place the id enters a chunk.

### Tests

**AC 1 — a held pair is never charged twice, at the bound.** One test, one
setup: `fillRegistry` to `maxInFlightUploads - 1`, then `Admit` the subject pair
with the bound declaration so the registry is exactly at the bound with the
subject held and its transfer unfinishable in one chunk.

- Clause (a): `Deliver` `boundChunk(0)` → `ErrIncomplete`; assert the answer does
  **not** wrap `ErrTooManyUploads`; `count()` still `maxInFlightUploads`.
- Clause (b): `Admit` the subject pair again with the **same declaration
  verbatim** → the incumbent pointer back, nil error, `count()` unchanged. The
  declaration must be admissible; a refused repeat answers its own declaration
  sentinel by #1795's pinned decision, which would prove nothing about the gate
  order. This clause is the parked #1796 SHOULD FIX; M7 below is what discharges
  it.

**AC 2 + AC 3 — what ends an upload.** One table. Per row: `fillRegistry` to
`maxInFlightUploads - 1`, `Admit` the subject with the row's declaration so the
registry is at the bound, deliver the row's chunks in order — every one but the
last must be accepted (`nil` or `ErrIncomplete`) — and assert the last delivery's
answer. Then assert the slot came back, identically for every row: `count() ==
maxInFlightUploads - 1`, `Lookup` no longer holds the pair, and a **fresh**
`attachment_id` is admitted into the freed slot (the filler declaration
`(1, 0, testFixtureDigest)` `fillRegistry` itself uses) with a nil error and
`count()` back at the bound.

One row per way an upload ends:

| Row | Declaration | Chunks delivered | Want |
|---|---|---|---|
| completes | `1`, `len(testFixture)`, `testFixtureDigest` | `testFixture` at index 0 | `nil`, and the returned bytes equal `testFixture` |
| total_chunks disagrees | `1`, `len(testFixture)`, `testFixtureDigest` | one chunk at index 0 declaring a total of 2 | `ErrTotalChunksMismatch` |
| index out of range | same | one chunk at index 1 carrying the *declared* total, so the range check is what refuses it and not the total check ahead of it | `ErrIndexOutOfRange` |
| duplicate index | the bound declaration | `boundChunk(0)` twice | `ErrDuplicateIndex` |
| chunk crosses the byte bound | the bound declaration | all of `testBoundFixture` at index 0, then a one-byte chunk at index 1 | `ErrUploadTooLarge` |
| assembled length wrong | `1`, a size `testFixture` does not have and `CheckDeclaration` still admits at a count of 1 | `testFixture` at index 0 | `ErrSizeMismatch` |
| digest wrong | `1`, `len(testFixture)`, `testEmptyDigest` | `testFixture` at index 0 | `ErrDigestMismatch` |

Two rows need the bound declaration and cannot be shrunk: a duplicate index is
only reachable on a transfer a first chunk does not complete, and the byte bound
is only crossable by a transfer declared large enough to be admitted at all. The
crossing row's first chunk carries the whole 16 MiB fixture, which `Add` accepts
— its rung is `>`, not `>=` — and the one-byte second chunk is what crosses.
That row is the **`Add` form** of `ErrUploadTooLarge`; the `CheckDeclaredSize`
form is refused inside `Admit` before an accumulator exists, is #1788's, and
never reaches this surface.

**AC 4 — `ErrIncomplete` keeps the entry, and the bytes accumulate in one
place.** `Admit` the subject with the bound declaration on an otherwise empty
registry. Deliver `boundChunk(0)` → `ErrIncomplete`, `count() == 1`, and `Lookup`
returns the **same pointer** `Admit` returned. Deliver `boundChunk(1)` →
`ErrIncomplete`, `count()` still 1. Then deliver the remaining chunks in a loop
and assert the last one returns bytes equal to `testBoundFixture`, with
`count() == 0` after it. The completing round trip is what proves all 373 chunks
reached one accumulator rather than 373 fresh ones; the pointer identity is what
proves the registry did not swap it mid-transfer. This is the suite's only
end-to-end statement through `Deliver`, which is why it earns the 16 MiB the
table's completing row deliberately does not spend.

**AC 5 — a chunk for a pair the registry does not hold.** Table, two rows against
a registry holding the subject pair on `testConnA`:

- an `attachment_id` never admitted on that conn;
- `testAttachmentID` delivered on `testConnB` — held, but by the other conn.

Both want `ErrUnknownUpload`, and both assert `count()` unchanged with `Lookup` of
the delivered pair still absent — nothing was created. The second row is the only
place the conn half of the key is exercised through this method.

AC 5 also asks for a sentinel "distinct from every existing one in this package",
which is a claim over a set and must be asserted over that set rather than over a
sample of it: declare a slice naming every exported sentinel the package
publishes — `accumulator.go`'s block, `admission.go`'s, and `storage.go`'s — and
loop `!errors.Is(err, s)` over it. Three hand-picked near misses would leave the
quantifier unproved while looking like coverage.

### Mutant table

Measured with `go test -overlay` against an absolute-path JSON manifest, no
worktree write, run **unfiltered** across the package — a `-run` filter is
exactly what would make an "also reddens: nothing else" claim look true whether
or not it is. Grep the output for `build failed` and `declared and not used`
before trusting any verdict: deleting the `errors.Is` call in step 3 kills
`registry.go`'s only use of the `errors` import, and `go test` exits 1 on a
broken build the same way it does on a red test.

| # | Mutant | Predicted red |
|---|---|---|
| M1 | `Deliver` gains a capacity gate ahead of the look-up | AC 1 clause (a), **and every row of the ends-an-upload table**, all of which run at the bound by design. A superset rather than a sole red, stated as one: "`Deliver` never consults the bound" is deliberately over-pinned. AC 4 stays green — it runs on a registry holding one entry. |
| M2 | Release on `ErrIncomplete` too (step 3's guard dropped) | AC 4 (`count()` and the second delivery, which would answer `ErrUnknownUpload`), AC 1 clause (a), and the table's duplicate-index and byte-bound rows, whose second delivery would meet an empty registry |
| M3 | Never release after `Assemble` (step 4's release dropped) | the table's completing, wrong-length and wrong-digest rows, and AC 4's closing `count() == 0`; nothing else |
| M4 | Never release after an `Add` refusal (step 2's release dropped) | the table's four `Add`-refusal rows — total_chunks, index, duplicate, byte bound; nothing else |
| M5 | On a miss, build an accumulator from the chunk's own declaration and feed it instead of refusing | both AC 5 rows; nothing else |
| M6 | `Deliver` ignores `connID` and scans `r.uploads` for any entry matching the id | AC 5's cross-conn row alone. The scan must take `mu`, or `-race` reddens the package for an unrelated reason and the measurement is worthless. |
| M7 | Hoist the `maxInFlightUploads` gate ahead of the incumbent look-up in `insertLocked` | AC 1 clause (b) alone. **This is the #1796 SHOULD FIX**: the same mutant measured green across all 134 tests before this ticket. Run it and record the result in the PR — a green here means clause (b) is not actually driving a held pair at the bound, and the gap is still open. |

One property is deliberately left unpinned: that `Deliver` holds no lock across
`Add`/`Assemble`. A mutant that takes `mu` and reads `r.uploads` directly is
race-free and reddens nothing, and the shape that would deadlock (taking `mu`
and then calling `Lookup`) hangs rather than fails a fixture. The leaf rule is
enforced by the type doc and by `Deliver` taking no lock at all, not by a test —
stated here so a reviewer does not read the absence as an oversight.

`make check` is the gate. Nothing here reaches the live-claude suite.

## Open questions

- **Which wire code `ErrUnknownUpload` maps to** is #1744's, and this spec
  deliberately names no candidate (see § "`ErrUnknownUpload`").
- **Whether #1744 feeds the first chunk through `Deliver` or through the
  accumulator `Admit` returned** is #1744's call, but only one of them keeps the
  release policy in one place: `Admit` for the declaration, then `Deliver` with
  the same chunk, which looks the just-admitted pair up again and releases it if
  that first chunk completes or is refused. Feeding `Admit`'s return value
  directly bypasses every release this ticket adds and re-creates the lockout for
  single-chunk transfers. `Admit`'s signature is not changed here.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The boundary is `Deliver`'s parameter list,
  and it is explicit: `chunk` is wholly untrusted (client-authored, JSON-decoded,
  including the `AttachmentID` this method makes a map key), `connID` is
  receiver-authored. Nothing is promoted to trusted by passing through: the id
  becomes a *key* and never a path component — the canonical-shape check before
  it may become one is `EnsureDir`'s — and `uploadKey`'s doc already states in
  capitals that membership certifies nothing. The bytes stay untrusted after a
  successful `Assemble` too; that is integrity, not authenticity, and
  `Assemble`'s own doc block says so to the consumers this method hands them to.
- **[Tokens/secrets]** Not applicable — no credential is generated, stored,
  compared or transported. The declared `sha256` is an attacker-supplied claim
  compared by `Assemble`, not a secret, and `Deliver` neither reads nor
  reproduces it.
- **[File operations]** Not applicable by explicit scope: this slice returns
  assembled bytes and writes nothing. `EnsureDir` and `Store` are landed and are
  deliberately not called from here — a spec that reached for them would put a
  filesystem call inside the one method that holds `Filename`.
- **[Subprocess]** Not applicable — no `exec`, no environment read.
- **[Crypto]** No findings. No primitive is chosen or invoked here; the digest
  comparison stays exactly where it is, inside `Assemble`, over the assembled
  slice. In particular this slice must not fast-path a completion check off
  `received` or the chunk map — `Accumulator`'s `received` field doc bans exactly
  that substitution.
- **[Network & I/O — input bounds]** No findings. Every inbound size is already
  bounded upstream: one chunk by `protocol.MaxAttachmentChunkBytes` at the
  envelope, one transfer by `maxUploadBytes` at `Add`'s step 5, and the number of
  live transfers by `maxInFlightUploads` — which this ticket is what makes a
  ceiling on *live* transfers rather than a lifetime quota. A miss retains
  nothing at all: `Deliver` builds one `uploadKey` for the map probe and drops
  it, so an unbounded or absent `chunk.AttachmentID` costs one hash and no
  memory. Release does open a churn path (admit → refuse → admit again, with no
  blacklist, as the ticket prescribes), and it is bounded: each cycle retains
  nothing once `reject` drops the map and `Deliver` drops the entry, and the
  resident bound at any instant is unchanged. Rate-limiting the cycle is the
  transport's concern, not this package's.
- **[Network & I/O — memory peak]** SHOULD FIX, for #1744 rather than for this
  spec. `Deliver` releases the slot **before** returning the assembled bytes, so
  a freed slot can be refilled by a new transfer while the caller still holds up
  to `maxUploadBytes` of returned bytes. The magnitude is not new — that is the
  `2 × maxInFlightUploads × maxUploadBytes` worst-case peak `maxUploadBytes`'
  doc already derives, 128 MiB at today's constants — but the *window* widens
  from "during an `Assemble` call" to "until the caller is done with the bytes",
  which for #1744 is one `Store` write. Two things make this the right trade
  rather than a hole to close here: holding the slot until a caller declares
  itself finished is a lease API, several times this ticket's size, and its
  failure mode is the lockout this ticket exists to remove. Recorded so #1744
  writes and drops the returned slice promptly rather than parking it. Offsetting
  it, this design also *tightens* one figure: completion releases, so a transfer
  can no longer be re-assembled through the registry, and at most one full-size
  `Assemble` allocation happens per transfer.
- **[Error messages / logs]** No findings, and the design is what makes it
  structural rather than a discipline. `Deliver` holds all four strings
  `AttachmentChunkPayload`'s SECURITY block bans from messages — `AttachmentID`,
  `Filename`, `SHA256`, `Data` — and contains **no format string**:
  `ErrUnknownUpload` is returned bare and every other error is another function's
  verbatim. The package still makes zero log calls. The AC 5 test asserts the
  sentinel's identity; a reviewer wanting the stronger form can also assert the
  message carries neither the id nor the digest, as
  `TestRegistry_AdmitAtTheBound_RefusesANewPair` does for the capacity refusal.
- **[Error messages — disclosure]** No findings. `ErrUnknownUpload` is a
  presence oracle in shape, and it discloses nothing, because the conn is half
  the key: a caller can only ever probe transfers on its own conn, whose
  admissions and refusals it already received. This is why it does not need the
  deliberate indistinguishability `attachment.not_found` carries in
  `docs/protocol-mobile.md` § Attachments — that code merges three outcomes
  because the retrieval verb would otherwise be a path-existence oracle across a
  conversation's directory, where this one answers only about the caller's own
  in-flight state. A future change that made this surface reachable across conns
  would invalidate the argument, which is why the cross-conn AC 5 row and mutant
  M6 exist.
- **[Concurrency]** No findings, one inherited precondition restated. No new lock
  and no new goroutine; `Deliver` takes `mu` never, so the leaf rule and the
  documented lock order are untouched, and there is no second lock to order
  against. The three-acquisition shape (look up, feed off-lock, release) is not a
  TOCTOU hole *only* because the conn is in the key and relay's `appFrameWorker`
  serialises one conn's frames — the same precondition `Admit` already returns
  its accumulator under. Two goroutines feeding one pair would be a data race
  rather than two transfers, which is a property of the existing design this
  slice must inherit rather than re-derive. The check-then-mutate that genuinely
  needs indivisibility — look up, count, insert — is inside `Admit` and is
  untouched here; AC 1 clause (b) plus mutant M7 are what finally defend its
  order against a reordering edit.
- **[Threat model alignment]** Addressed, and nothing published is contradicted.
  `docs/protocol-mobile.md` § Attachments states `attachment_id` is "not a
  capability", and this design keeps that true by taking the id only from the
  chunk and pairing it with the receiver-authored conn. It also states the
  receiver's bounds are "receiver-configured and unpublished — a client learns
  them by being rejected", which is what this ticket makes honest for the
  concurrency bound: before it, the bound was learned once and never cleared.
  The section's abandonment rules — `attachment.stream_aborted`, MUST discard,
  and the no-abort-frame case — bind the **client** as receiver on the retrieval
  leg; nothing published constrains what the daemon does with its own state after
  refusing an inbound chunk, so releasing the entry invents no divergence from
  the contract. Out of scope and named: expiry of a stalled upload (#1742) —
  until it lands, a client that admits a transfer and then stops holds its slot
  until the conn goes away, and that is the one resource-exhaustion path this
  ticket does not close; wire-code mapping and log emission (#1744).

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-26
