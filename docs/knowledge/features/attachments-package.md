# `internal/attachments` — inbound attachment-chunk accumulation

Package (#1769, #1770, #1772, #1776, #1777, #1787, #1781, #1788, #1782, #1795, #1796, #1784, #1880, #1817), fourteen slices of the family split from #1741/#1766:
holds one inbound
attachment upload's chunks in memory, addressed by index, refuses a stream
whose framing contradicts what the transfer declared at admission (#1769),
and — once the transfer is complete — checks the assembled bytes against the
transfer's declared length and lowercase-hex sha256 before yielding a single
byte (#1770). Bounds the retained bytes of one upload to a
receiver-configured, unpublished ceiling, refusing at either the declared
size or the accumulated total (#1777, see § "Per-upload byte bound" below),
and holds the uploads currently in flight in a conn-keyed `Registry` (#1787),
whose only exported way in runs both declaration checks, the incumbent
look-up, and a daemon-wide entry-count ceiling under one lock acquisition
before admitting anything (`Admit`, #1788, #1795, #1796, see § "In-flight
upload registry" below). `Registry.Deliver` (#1784) is the other half of that
mechanism: it routes a later chunk to the admitted entry and gives the slot
back on completion or refusal, so `maxInFlightUploads` bounds *live* transfers
rather than a lifetime quota.
Accumulation and admission stay in-memory — no disk, no wire codes, no
logger — but the package is no longer in-memory-only end to end: `EnsureDir`
(#1781, see § "Directory resolution and creation" below) resolves and creates
the on-host directory one attachment is filed under, and `Store` (#1782, see
§ "Writing attachment bytes" below) writes the verified bytes into it — the
package's only two functions that touch a filesystem. `Accumulator` itself
still carries no lock; synchronisation lives in `Registry` alone.

`SanitizeFilename` (#1772, `filename.go`) is unrelated in shape but shares the
package: a pure, stateless function turning a client-supplied
`AttachmentChunkPayload.Filename` into one safe host-side path component —
never called on `AttachmentID`, whose contract is a canonical-shape check that
*rejects* rather than this treatment. **The returned component is not
unique** — `a/b` and `a_b` both answer `a_b`, every unusable name answers the
fixed fallback, and APFS folds case — so #1782 must key stored files by
`attachment_id`, not by this component (`EnsureDir`, #1781, already keys the
directory that way), and #1782's file-write code should be the only caller
that reads `Filename` at all. Sanitising also does
not make a name loggable: it removes the log-injection shape but not the
independent privacy reason § Attachments already bans logging a filename for.

- Wire contract: [`protocol-package.md`](protocol-package.md) § "v2 attachment-chunk vocabulary" (`AttachmentChunkPayload`, `MaxAttachmentChunkBytes`).
- Design reference this package deliberately diverges from: [`v2-session-manager.md`](v2-session-manager.md) § "Debug-bundle streaming" — `ReassembleBundle`.
- Ticket record: [codebase/1769.md](../codebase/1769.md) does not exist — this is a lessons-only package overview per the documentation phase's per-ticket-file retirement (2026-08-19); read the ticket/spec/PR directly for implementation detail.

## What it copies from `ReassembleBundle`, and what it doesn't

`ReassembleBundle` (`internal/relay/v2bundlestream.go`) is the nearest existing
receiver for a payload that splits across frames, but it runs the other
direction (daemon → client) and is a pure oracle with no inbound production
caller. `Accumulator` copies exactly one property from it and diverges on two:

- **Copies** the all-or-nothing posture — `Assemble` either yields the
  complete bytes or fails cleanly, never partial or corrupted output.
- **Does not copy the ordering rule.** `ReassembleBundle` demands strict `Seq`
  succession; the published attachment contract stores each chunk **by
  index**, so chunks may arrive in any order. Copying the neighbouring rule
  was the specific mistake the contract warns against by name — see the
  scenario-1 fixture below.
- **Does not copy the signature.** `ReassembleBundle` takes every frame at
  once and returns once, so its "incomplete" is terminal. `Accumulator`'s
  incomplete is resumable: a later `Add` can turn the same instance into a
  complete one. That difference is why this is a stateful type, not a
  function.

## Admission layer (#1776)

`CheckDeclaration` (`admission.go`) is the first-chunk `total_chunks`/`size`
cross-check, run before an `Accumulator` exists so a claim is refused before
either number sizes anything. It is pure and stateless — no lock, no
goroutine — unlike `Accumulator`, which is fed serially by one session's
`appFrameWorker`. It lives in this package rather than `internal/protocol`
because the check is receiver policy: `protocol` ships wire vocabulary with
no validator, by design.

- **The negative-`size` guard cannot be folded into the equality check** —
  measured, not assumed: `max(1, ceil(-1 / bound))` is 1 under either natural
  ceiling form, so the pair `size` −1 / `total_chunks` 1 passes the
  cross-check unaided. It needs its own guard ahead of the equality, sharing
  `ErrInvalidDeclaration` but pinned by a separate test row.
- **The ceiling must be division-then-remainder, never `(size + bound - 1) /
  bound`.** That numerator wraps for every `size` within `bound`-1 of
  `math.MaxInt64`, and `max(1, …)` launders the wrapped negative result back
  to 1 — silently admitting the largest declaration the wire can carry
  (measured: wraps to −204963823041216 where the correct count is
  204963823041218). Also compare `int64(totalChunks) != want`, never
  `int(want) != totalChunks` — narrowing re-introduces the same class of wrap
  on a platform where `int` is 32 bits.
- **`ErrInvalidDeclaration` is a distinct sentinel from `ErrTotalChunksMismatch`
  on purpose.** The latter means a chunk disagrees with the count the
  transfer was already admitted under; `CheckDeclaration` runs before any
  count is admitted, so there is nothing yet to disagree with.
- **Admission runs in front of `Accumulator` and does not replace its own
  guards.** `NewAccumulator` stays exported and constructible without passing
  through `CheckDeclaration`, so `Assemble`'s `totalChunks < 1` clause is
  still load-bearing — it is not dead code made redundant by this layer.
- **An arithmetically-conforming but enormous declaration is admitted here on
  purpose** (`size` `math.MaxInt64` with its honest matching count) — this
  layer checks consistency between the two claimed numbers, not their
  magnitude. The per-upload byte bound is a separate concern (#1777); adding
  an absolute size cap here would be scope creep this ticket deliberately
  declined.

## Per-upload byte bound (#1777)

Two rungs enforce one constant, `maxUploadBytes` (16 MiB, `admission.go`), and
share one sentinel, `ErrUploadTooLarge`: `CheckDeclaredSize` refuses a
declared `size` above the bound on the first chunk, before any bytes are
held; `Add`'s step 5 — last in its fixed check order, after the duplicate
check, immediately before the store — refuses the chunk that would take the
held total past it, routed through the existing `reject` latch. The crossing
chunk's own bytes are the one transient "slack," bounded by the transport's
65519-byte envelope cap and never retained.

- **The declared rung is a sibling of `CheckDeclaration`, not a fold-in** —
  measured, not assumed, same as the admission-layer decisions above.
  `TestCheckDeclaration`'s `math.MaxInt64` row is the only row that pins the
  ceiling arithmetic's exact value; folding a magnitude check into
  `CheckDeclaration` would refuse that pair and destroy the sole pin on the
  wrapping form that silently admits the wire's largest declaration. Two
  sentinels with two different client-visible repairs — re-chunk vs. shrink
  the file — is also a worse contract from one function than from two.
- **`CheckDeclaredSize` admits a negative `size` on purpose.** Magnitude is
  its whole subject; `CheckDeclaration` already owns the negative-`size`
  refusal, and a second owner would make which sentinel a caller sees depend
  on call order. Both checks must run, on the first chunk, before
  `NewAccumulator` — `Registry.Admit`'s obligation (#1788), not either
  function's, since neither can enforce the other's presence: `NewAccumulator`
  stays exported and constructible without passing through `Admit`, so a
  second caller could still run one check or neither.
- **The pairing is what bounds the key space, not either check alone.**
  `CheckDeclaration` bounds `total_chunks` from above, `CheckDeclaredSize`
  bounds `size` from above; together an admitted transfer declares at most
  373 chunks. `CheckDeclaredSize` alone admits a declaration of 2³¹−1 declared
  zero-byte chunks, which no byte bound can refuse, since Σ `len(Data)` stays
  0 regardless of how many chunks are declared.
- **The accumulated rung is subtraction, not a sum**: `len(chunk.Data) >
  maxUploadBytes - a.received`, never `received + len > bound`. The
  invariant `0 <= received <= maxUploadBytes` is what keeps the subtraction's
  right operand non-negative; the addend is a materialised slice length
  rather than a claimed number, so the sum form's wrap isn't reachable in
  practice either, but the safe form is written anyway so a later reader
  doesn't "simplify" it back or read the discipline as cargo cult.
- **Code review correction: the daemon-wide worst-case *peak* is `2N ×
  bound`, not `(N+1) × bound`.** The landed doc's "Retained is not peak"
  paragraph states the worst case as one bound of retained memory across `N`
  concurrent uploads plus a single extra bound for one in-flight `Assemble`
  call — true only if `Assemble` calls across sessions were serialized. They
  are not: `appFrameWorker` (`internal/relay/v2session.go`) documents "no two
  handlers for **the same conn** run concurrently," which is a per-session
  guarantee, not a daemon-wide one — one worker is spawned per session, so
  `N` concurrent sessions can each be inside their own `Assemble` call at the
  same moment. The correct worst-case peak is `N` bounds retained plus up to
  `N` more in flight — 128 MiB at a concurrency of 4, not 80 MiB. The
  *resident* (non-peak) figure the constant's doc states — `bound ×
  concurrency` — is correct and is what `maxInFlightUploads` (#1796) inherits
  as its budget; it is only the peak-during-assembly refinement layered on
  top that undercounted. #1796 landed the correction in the code comment
  itself (128 MiB peak, citing `appFrameWorker` by symbol), closing the gap
  this entry originally flagged between this doc and the code comment.

The entry-count cap landed as `maxInFlightUploads` (#1796); see § "In-flight
upload registry" below for the gate and its shape.

## In-flight upload registry (#1787, #1788, #1795, #1796, #1880, #1881, #1817)

`Registry` (`registry.go`) gives `Accumulator` somewhere to live between
chunks: a map from `uploadKey{connID, attachmentID}` to an
`entry{acc *Accumulator, lastChunkAt time.Time}`, held **by value** so the
stamp can only move by storing into the map under `mu` — an unexported
`insertLocked` core, a thin lock-taking `insert` wrapper kept for tests,
comma-ok `Lookup`, an unexported stamping `lookupAndStamp`, `Release`, its
conn-keyed sibling `ReleaseConn` (#1817, removes every upload one conn holds
in a single pass, for a teardown path that has the conn but never the list of
attachment_ids it was admitted under), and two unexported test-only readers,
`count` and `lastChunkAt` (#1880). Still unreachable from production — nothing
calls it until #1744 wires the dispatch site, and #1744 lands last in the
family.

**Each entry carries `lastChunkAt`, the clock's reading at admission and at
the last delivered chunk (#1880).** The clock is a nil-tolerant seam —
`newRegistryWithClock(now func() time.Time)`, unexported, with `NewRegistry()`
delegating `newRegistryWithClock(nil)` — copied verbatim from
`newStallTracker`'s `if now == nil { now = time.Now }` idiom already used by
streamsup and streamjson's emitter, so `NewRegistry`'s 19 existing call sites,
all test-only, are untouched. `now` is read with `mu` held, the package's
first caller-supplied call under its only mutex; the constructor's doc states
the three constraints that keep that lock order total rather than cyclic — the
function must not block, must not call back into the `Registry`, and must be
safe for concurrent use. `insertLocked` stamps at the one store both `Admit`
and `insert` share, behind both refusal branches, so **no refusal ever
stamps** and a repeat `Admit` under an already-held pair leaves the
incumbent's stamp untouched. Delivery stamps through the new unexported
`lookupAndStamp`, which `Deliver`'s step 1 now calls in place of `Lookup` — on
a hit it moves `lastChunkAt` forward and returns the accumulator, on a miss it
stores nothing. **`Lookup` itself stays a pure read and deliberately does not
stamp** — a look-up that moved the activity time would let a diagnostic or #1744's dispatch-site read keep a dead upload alive indefinitely, the exact
exhaustion path this family is closing.

**#1881 landed the policy that reads the stamp: `uploadIdleTimeout` (15
minutes, `admission.go`) and the unexported `reapExpiredLocked`.** The reap is
lazy — no goroutine, no ticker, no shutdown path — and runs inline at the head
of the two locked bodies that gate on the map's contents, `insertLocked` and
`lookupAndStamp`, deleting every entry whose `lastChunkAt` is more than the
window behind one reading of `r.now()`. An eager sweeper was considered and
rejected: this package owns no goroutine and `NewRegistry` has no production
caller, so a sweeper would need a lifecycle (`Start`/`Stop` or a `context`)
nothing here has yet, and a different seam — a ticker rather than #1880's
`func() time.Time` clock. Nothing needs the promptness either, since both
places a pair's liveness is actually asked about (`insertLocked`'s capacity
gate, `lookupAndStamp`'s map read) already run inside a lock the lazy reap can
sit in front of. An expired upload's next chunk answers the existing
`ErrUnknownUpload` rather than a new sentinel — a distinct one would let a
client binary-search the window's length from outside, exactly the receiver
policy `docs/protocol-mobile.md` § Attachments keeps unpublished for this
constant's two neighbours. Two properties worth carrying forward to any future
policy added at this layer:

- **The reap and the gate it feeds must share one lock acquisition, not two.**
  `insertLocked` runs the reap ahead of the capacity check inside the single
  acquisition `Admit` already takes for its whole decision — a reap under one
  acquisition of `mu` followed by a capacity check under another would let two
  concurrent admissions each observe the same reclaimed slot and both take
  it, the exact split-lock shape
  `TestRegistry_ConcurrentAdmitAtTheBound_AdmitsExactlyTheFreeSlots` exists to
  redden.
- **A reader that reaps is a reader that causes what it reports.** `count`
  and `lastChunkAt` are the tests' only observers of expiry and deliberately
  do not reap, so a test asserting expiry must drive `Admit` or `Deliver`
  first and read one of those after — reading either one before driving a
  locked body sees no change, because nothing has reaped yet.

`Admit` is the registry's only exported way in, and now runs three checks
under one acquisition of `mu` before admitting anything: `CheckDeclaration`,
`CheckDeclaredSize`, and — last, inside `insertLocked`, behind the incumbent
look-up — a daemon-wide entry-count gate against `maxInFlightUploads` (4,
`admission.go`, #1796). Through #1788 the two declaration checks ran off-lock
and handed a nil answer to the lock-taking `insert`; #1795 moved the
acquisition to cover the whole decision — both checks, the incumbent
look-up, construction, and the store — specifically so #1796's capacity gate
could be added as part of that one acquisition rather than a check racing
it, and #1796 is that addition. `Admit` takes `mu` itself and calls the
unlocked `insertLocked` directly, never the lock-taking `insert`, whose own
`Lock` would deadlock under an already-held `mu` (`sync.Mutex` is not
reentrant — see the `mu`-is-a-leaf paragraph below). A declaration refusal
returns the check's error verbatim — no wrapping, no added context. The
capacity refusal is the first error the admission path *formats*:
`capacityRefusal(inFlight int)` wraps `ErrTooManyUploads` with the in-flight
count and the bound, and its int-only signature is what keeps the never-log
rule structural rather than a discipline at the one place on this path that
holds the client-chosen `attachmentID` and needs a format string —
`attachmentID` and `sha256` never enter a message from a function that
cannot see them, the same argument `CheckDeclaration`'s scalars-only
signature makes. All three sentinels stay reachable with `errors.Is`.

`ErrTooManyUploads` is deliberately a third sentinel rather than a widening
of `ErrUploadTooLarge`: retryability inverts across them. The capacity
refusal clears **by itself** once other uploads finish — `Release` frees a
slot the moment a held pair's transfer completes or is discarded — while
`ErrUploadTooLarge` never clears for that file. `internal/protocol`'s
`CodeAttachmentTooManyUploads` (mapped at #1744's dispatch site, not here)
carries that transience on the wire; folding the two sentinels together
would tell a client either to loop-retry an oversized file or to give up on
a refusal that was about to clear on its own.

**The capacity gate is the *last* of the three refusals, behind both
declaration checks and the incumbent look-up, and that position is a
contract.** At capacity, a first chunk whose declaration is also invalid
answers the declaration sentinel, not the capacity one — the client is told
the fault that will never clear ahead of the one that clears by itself. And
because the gate sits behind the incumbent look-up (below), a pair the
registry already holds reaches its incumbent before the gate ever sees it,
exempting it from a capacity refusal without short-circuiting the
declaration checks that run ahead of that look-up.

**A gate that is architecturally guaranteed by the code's shape is not
covered until a test tries to violate the shape.** Code review measured this
directly: an overlay mutant that hoists the capacity check ahead of the
incumbent look-up inside `insertLocked` — so a held pair *would* be refused
at capacity — passed green across all 134 tests in the package, because no
fixture exercises a held pair while the registry is at
`maxInFlightUploads`. The spec's claim that this exemption is "satisfied by
construction" was true of the code as shipped, but a true-by-construction
claim about ordering is exactly the kind of property a later one-statement
reordering can silently invert — construction is not a substitute for a
test defending it against future edits. **#1784 discharged this gap**: the
same mutant, re-run unfiltered against the same package, now reddens exactly
one assertion — the repeat-`Admit`-under-a-held-pair clause of
`TestRegistry_DeliverAtTheBound_ChargesAHeldPairOnlyOnce` — and nothing else,
confirming the fix landed as a sole red rather than incidentally alongside
other coverage.

**`Registry.Deliver` (#1784)** is the routing half `lookupAndStamp` and
`Release` are primitives for (#1880 swapped the look-up step from `Lookup` to
`lookupAndStamp`; see above): given a conn and a chunk, it looks the `{conn,
attachment_id}` pair up and stamps it, feeds the chunk to the accumulator's
`Add` off-lock, then asks `Assemble` — releasing the entry on every outcome
except `ErrIncomplete`, which is the one answer that keeps it. A miss refuses
with `ErrUnknownUpload` (below) and creates nothing. **At most two
acquisitions of `mu` per delivered chunk, never three:** one
(`lookupAndStamp`) on a miss or an `ErrIncomplete` outcome, two
(`lookupAndStamp` then `Release`) on every releasing path. This corrects the
doc's own prior "three separate acquisitions" claim, which #1880 re-derived
and found was never true of any single delivered chunk — its likely origin
was counting `Admit` + `Lookup` + `Release` across a whole single-chunk
transfer, not what "per delivered chunk" means for a method that never calls
`Admit`. Never one held across `Add` or `Assemble` — the same off-lock-feed
posture `Admit` already hands its
accumulator back under, safe for the same reason: the conn is in the key and
`appFrameWorker` serialises one conn's frames, so exactly one goroutine can
ever reach one accumulator. `Deliver` still has no production caller; #1744 is expected to call it with the same chunk it just fed to `Admit`,
looking the pair back up rather than feeding `Admit`'s returned accumulator
directly — the latter would bypass every release this ticket added and
re-open the lockout for single-chunk transfers.

**A structural no-format-string claim still needs a test that puts a format
string back.** Code review's one SHOULD FIX on #1784 (not blocking): `Deliver`
is the only function in this package holding all four strings
`AttachmentChunkPayload`'s SECURITY block bans from messages — `AttachmentID`,
`Filename`, `SHA256`, `Data` — and its doc block argues the never-log rule is
*structural* because it formats nothing. Measured, that argument is unpinned:
wrapping the unknown-pair miss or the `Add` refusal in a `fmt.Errorf` that
interpolates the client's `attachment_id` (and, on the `Add` leg, the declared
digest) is **green across all 147 tests**, because the AC 5 fixture asserts
sentinel identity via `errors.Is`, which a wrap still satisfies. Same shape as
the gate-ordering gap just above: a property guaranteed by the current code is
not covered until a fixture tries to violate it. Parked for whichever of #1744
or the documentation phase next touches `Deliver` — the fix is three lines,
asserting `err.Error()` carries neither the id nor the digest, the idiom
`TestRegistry_AdmitAtTheBound_RefusesANewPair` already uses for the capacity
refusal.

Both checks run **ahead of** the incumbent look-up, so a repeat under a held
pair answers the incumbent only when its declaration is admissible; an
invalid repeat answers its own declaration sentinel instead, and the
incumbent is left untouched
(`TestRegistry_AdmitRefusedRepeatUnderAHeldPair_KeepsTheIncumbent`). Through #1788 that was an accident of statement order — no landed test read anything
into it either way, by design — and #1795 turned it into a decision, because
the obvious restructure (look the incumbent up first, return early) would
have flipped it silently and nothing would have caught the flip. Which check
runs first is still deliberately not a contract — a doubly-bad declaration
gets whichever sentinel runs first, and no test may read an order out of
that.

- **Pairing two checks that answer different sentinels into one entry point
  needs one fixture per check, never a doubly-bad one.** A declaration both
  `CheckDeclaration` and `CheckDeclaredSize` would refuse reddens under
  dropping either guard and so proves neither is load-bearing, and it would
  additionally pin a cross-check order nothing has decided (the two
  sentinels differ, so an order asserted here would be an accident promoted
  to a contract). The fix used here: one row that's admissible by
  arithmetic but oversized, one that's within the byte bound but
  arithmetically wrong — each is a sole red for exactly one guard. #1795
  reused the same two declarations verbatim for a second test guarding the
  held-pair repeat path, rather than deriving a fresh pair — one fewer place
  for the discrimination property to drift out of sync.
- **`insert`'s never-replace behaviour was pinned once at the unexported
  layer (#1787) and had to be re-pinned through the exported one.** The
  #1787 test drives `insert` directly, so it stays green against an `Admit`
  that released the pair before inserting, or that installed a freshly
  built accumulator by some other route — the property a caller actually
  depends on only holds if the exported path is tested too, not inherited
  from coverage one layer down.
- **Sequencing constraint discharged: #1796 landed the entry-count cap ahead
  of #1744.** #1787/#1788's security review required #1744 (the dispatch
  site, and this package's first production caller) not land ahead of the
  in-flight entry-count cap — wiring the dispatch site first would have gone
  live with `Admit` bounding one upload's bytes but placing no bound on how
  many uploads may exist, so N distinct `attachment_id` values on one conn
  would yield N entries. That gap is closed: `maxInFlightUploads` (#1796) is
  the entry-count bound, and #1744 remains the package's first production
  caller once it lands.

- **Keyed by conn-and-attachment_id, not the bare id this doc used to sketch.**
  `docs/protocol-mobile.md` § Attachments documents `attachment_id` as "not a
  capability" — not secret, not unguessable. Keyed alone, a phone that drops
  mid-upload and reconnects re-sends the same id while its old session may not
  have torn down, colliding two conns onto one `*Accumulator`, which carries no
  mutex by design: a data race and a path for one conn's bytes into another's
  file. A concatenated string key (`connID+attachmentID`) reintroduces the same
  collision (`"ab"+"c"` and `"a"+"bc"` are one key); only a struct key forecloses
  it structurally.
- **One `sync.Mutex`, taken once per operation and held for the whole body —
  never once to read, again to write.** That is what lets the same-pair
  concurrent-insert case tell exactly one caller it got a fresh insert, and
  it's the shape #1796's count-then-admit gate needed: a registry that locks
  its read and its write separately passes `-race` and every other criterion
  here, but can't support the indivisible look-up-then-count-then-insert AC 4
  required — this is the mutant #1796's concurrency test (below) is built to
  catch. Through #1788, "per operation" covered only the lookup-and-store;
  `Admit` ran its two declaration checks off-lock and took no lock of its
  own. #1795 moved the acquisition to cover `Admit`'s whole decision —
  declaration verdict, incumbent look-up, construction, store — so #1796
  could add its count check inside that same acquisition rather than
  wrapping a second lock around it (`Registry`'s type doc cites `sessions`'
  `Pool.capMu` specifically to reject that wrap). `insertLocked`'s
  never-replace behaviour is a security property, not hygiene — it stops a
  second first-chunk for a live pair from swapping the declared
  `size`/`sha256` out from under the bytes already accumulated.
- **A lock-contention mutant's detection rate does not transfer between
  tests, even in the same package.** #1787's split-lock mutant on `insert`
  needed ~75 rounds of 32 goroutines for ≈99.9% detection (see § "Mutation-
  testing lessons" below). #1796's count-then-admit mutant — hoisting
  `Admit`'s capacity read ahead of `mu.Lock()` — measured red 5/5 at
  `-count=5 -race` with 200 short rounds of 8 goroutines each, under a
  second of wall clock. Different mutant shape, different fixture, so the
  round count that works for one is not evidence for the other; each new
  concurrency pin needs its own measured rate.
- **Off-lock feeding is sound only because the conn is in the key.** The
  registry hands the `*Accumulator` back and the caller feeds it with the lock
  released; that's safe because `V2SessionManager.appFrameWorker` serialises
  every frame for one conn, so exactly one goroutine can ever reach one
  accumulator. #1784 (routing a later chunk into an admitted upload) inherits
  this posture rather than re-deriving it.
- `insert` stays unexported and, since #1795, test-only; `Admit` is the only
  production way in. Through #1788, `Registry`'s type doc read "no method of
  `Registry` calls another," re-scoped once `Admit` started composing with
  `insert` to "no method takes `mu` and then calls another that takes `mu`"
  — because `Admit` took no lock of its own at the time. #1795 removed that
  composition entirely: `Admit` now takes `mu` itself and calls the unlocked
  `insertLocked` directly, never `insert`. The rule is back to its simpler,
  original form — at that point five locked methods (`Admit`, `insert`,
  `Lookup`, `Release`, `count`), none calling another — with `insertLocked`
  as the one shared, lock-free core that two of them wrap. A rule correction
  chasing a composition that the next slice would go on to remove is why the
  type doc now states the rule in the form least likely to need re-scoping
  again: "no method takes `mu` and then calls another that takes it," which
  is true whether or not any method currently composes with another. **#1880
  grew the roster to seven** (`Admit`, `insert`, `Lookup`, `lookupAndStamp`,
  `Release`, `count`, `lastChunkAt`) without touching the rule itself —
  exactly what the re-scope-proof phrasing was written to survive. **#1817
  grew it to eight** (`ReleaseConn`), and is the first addition with no
  lock-free `Locked` core of its own — `insertLocked` and `reapExpiredLocked`
  are cores because two bodies share each; nothing shares `ReleaseConn`'s, so
  it takes `mu` and deletes directly rather than publishing a seam no caller
  asked for. It also had to reject the shape that would have looked like
  reuse: composing `ReleaseConn` from a per-pair loop over the lock-taking
  `Release` looks like tidy DRY and is instead a deadlock — `sync.Mutex` is
  not reentrant, and unlike a race, a deadlock is not something `-race`
  catches, so the rejection has to be a documented constraint rather than
  something a test could be trusted to surface.

## Directory resolution and creation (#1781)

`EnsureDir` (`storage.go`) resolves and creates the on-host directory one
attachment of one conversation is filed under —
`conversations/<conversation-id>/attachments/<attachment-id>` beneath the
daemon instance directory, every level `0o700` — and returns its
`EvalSymlinks`-resolved path. Writing bytes into it is #1782's; dispatching to
it and mapping refusals to wire codes is #1744's. It carries two sentinels of
its own, `ErrInvalidID` and `ErrNotContained`, structurally unlike the seven
below: `EnsureDir` has no accumulator state to latch or discard, so the
discard-semantics table doesn't apply to it.

Both ids are validated against `conversations.ValidID` before any filesystem
call — `attachment_id` is client-chosen and its 64-byte wire ceiling is
explicitly not a defence, so containment comes entirely from the resolution
check, never from the id being hard to guess. The check itself is
`candidate == want` (full-path equality against a destination built textually
beneath the `EvalSymlinks`-resolved instance directory), not a
`filepath.Rel`-style "is it under the root" test — equality is what refuses a
conversation directory symlinked to a *sibling* conversation, which a
containment-only test would pass.

- **A precedent's ordering rationale doesn't transfer unchanged when a caller
  strengthens the comparison it protects — measured, not assumed.**
  `confineWorkdirToHomeCreating` (`cmd/pyry/main.go`) is this design's
  precedent, and its ancestor walk probes with `os.Lstat` rather than
  `os.Stat` so a symlink is resolved instead of stepped over. `EnsureDir`
  copied that probe, but re-measuring it under the *stronger* comparison this
  design uses (equality, not the precedent's `filepath.Rel` containment test)
  found the swap reddens **nothing** in the suite: the two probes diverge only
  for a *dangling* link, and on one, `Stat` steps over it so the pre-creation
  check passes vacuously, but `MkdirAll` then fails `EEXIST` on the symlink
  name — landing in the same wrapped-OS-error class the `Lstat` build reaches
  by failing to resolve it. `Lstat` was kept anyway (refusal should come from
  the resolution step rather than incidentally from directory creation, and
  the precedent's weaker containment test genuinely does depend on the probe),
  with the code comment rewritten to state the measured fact rather than the
  inherited one. Anyone copying a piece of `confineWorkdirToHomeCreating` into
  a design that changes the comparison it was written for should re-measure
  that piece, not inherit its justification.
- **A spec's own fixture rule can make its own predicted sole-red row
  unsatisfiable.** The `os.Stat` mutant above was predicted (in the
  architect's mutant table) to redden the escaping- and sibling-conversation
  symlink tests. Both of those fixtures are required to use targets that
  *exist* — `EvalSymlinks` on a dangling link errors, so a dangling-target
  fixture would assert the wrong sentinel for a right-looking reason — which
  is exactly the fixture the `Stat`/`Lstat` divergence needs to be visible at
  all. No test satisfying those two criteria can ever be the mutant's sole
  red. When a mutant table names a row its own fixture constraints forbid,
  treat the row as unsatisfiable rather than an unwritten test to chase; a
  dangling-symlink case still deserves its own test (`EnsureDir` returns a
  wrapped OS error and neither sentinel for one — broken host state is not a
  containment breach), just not as that mutant's proof.

## Writing attachment bytes (#1782)

`Store` (`storage.go`) takes the directory `EnsureDir` returned, a
client-supplied filename, and already-verified bytes, and writes them there
with the house temp-and-rename recipe (temp file created *in* `dir`, `Chmod`
`0o600`, write, `Sync`, `Close`, `Rename`), returning the joined path. It
trusts `dir` completely and adds no resolution or containment check of its
own — re-deriving one would fork the check `EnsureDir` exists to own, and
would break the reason two attachments carrying the same client filename can
coexist: the per-`attachment_id` directory component disambiguates them, not
the sanitised name, which `SanitizeFilename` (#1772) documents as explicitly
not unique. `ErrWriteFailed` is a third top-level sentinel in `storage.go`,
beside `ErrInvalidID`/`ErrNotContained` — not folded into `accumulator.go`'s
grouped block, whose opening sentence scopes it to `Add`/`Assemble`. `Store`
is idempotent (`Rename`, not `O_EXCL`), which is what lets a phone's
reconnect-and-resend overwrite with the same bytes instead of failing. No
production caller yet; #1744 wires the dispatch site.

- **A privacy rule stated as one property across several failure branches
  needs checking branch by branch, not as a whole.** The spec banned a
  rename-failure error from naming the sanitised client filename — correct,
  since `os.Rename` returns `*os.LinkError`, whose `Error()` prints its
  destination unconditionally — and then extended the same reasoning to rule
  out a test on the create-temp branch too, calling it "vacuous on the
  create-temp path where the name is never touched." Code review measured
  it: an overlay mutant touching *only* the create-temp branch's `%q`
  operand (`dir` → `path`) leaked the sanitised filename into the error text
  and the full suite — six tests, ten subtests — stayed green. The
  create-temp branch never names `path` in the correct build, but nothing
  had asserted it couldn't in a wrong one. The reasoning that made one branch
  unpinnable didn't actually apply to the other five; it was accepted for
  all six because it was true for one. When a spec calls an assertion
  vacuous across several code paths, verify the claim against each path the
  assertion would cover, not just the one that prompted the reasoning.
- **An already-extracted, same-shaped atomic-write helper can still be the
  wrong copy target.** `internal/update.AtomicReplace` is an exported,
  doc-commented version of this exact recipe whose signature would have
  dropped in almost verbatim. It was correctly not used: its rename-failure
  message formats the destination path into the text unconditionally (making
  the branch-privacy gap above permanent instead of one CR finding from
  closed), and it collapses five distinguishable failure points into one
  message, losing the per-step `<op>` this package's error strings need for
  the operator's log. The Technical Notes' "ten packages hand-roll this
  recipe, none imports another's — don't extract a shared helper" is
  guidance against building a *new* one; it isn't a green light to reach for
  an existing one without reading what that helper puts in its own errors.

## Sentinels and discard semantics

Eight exported sentinels, `errors.New("attachments: …")` house style, in
three families plus two non-discarding outliers. Six discard the transfer
(latched on first refusal — every later `Add` and `Assemble` returns the
identical wrapped error, and the held chunk bytes are dropped at the moment
of refusal so a poisoned accumulator holds no memory past that point).
`ErrIncomplete` is the outlier and does **not** latch or discard — it is the
resumable answer a later chunk can turn into bytes.

| Sentinel | Family | Latches? |
|---|---|---|
| `ErrTotalChunksMismatch` | framing | yes |
| `ErrIndexOutOfRange` | framing | yes |
| `ErrDuplicateIndex` | framing | yes |
| `ErrSizeMismatch` (#1770) | integrity | yes |
| `ErrDigestMismatch` (#1770) | integrity | yes |
| `ErrUploadTooLarge` (#1777) | resource | yes (from `Add`); n/a (from `CheckDeclaredSize` — no `Accumulator` exists yet) |
| `ErrTooManyUploads` (#1796) | resource | n/a — refused before any `Accumulator` exists, by `Registry`'s admission gate rather than `Add`/`Assemble` |
| `ErrIncomplete` | — | no |
| `ErrUnknownUpload` (#1784) | — | n/a — no `Accumulator` is looked up for the pair; refused by `Registry.Deliver` itself before either `Add` or `Assemble` runs |

`ErrUploadTooLarge` is the one sentinel not scoped to `accumulator.go`'s var
block, whose opening sentence reads "Sentinel errors returned by `Add` and
`Assemble`" — it is also returned by an admission function that runs before
an `Accumulator` exists, so it lives in `admission.go` beside
`ErrInvalidDeclaration` instead.

Mapping these to the wire is #1744's job at the dispatch site — this package
emits no wire codes and does not import `codes.go`. The mapping is
deliberately many-to-one within the framing and integrity families: all
three framing sentinels answer `CodeAttachmentInvalidChunk`, both integrity
sentinels answer `CodeAttachmentIntegrityFailed`. `ErrUploadTooLarge` is
one-to-one — `CodeAttachmentTooLarge` — and permanent for that file, in
contrast to `ErrTooManyUploads`'s `CodeAttachmentTooManyUploads` (#1796),
which is marked transient because it clears once other uploads finish; the
mapping itself is still #1744's to make. Distinguishing
sentinel from sentinel is wanted in-process, for this package's own tests and
for the daemon's logs, not on the wire — a single corrupt transfer must still
resolve to exactly one wire code, which is why `reject` (the shared latch
primitive every discarding family calls) has to run for a resource refusal
too: without the latch, a chunk sent after an `ErrUploadTooLarge` reject on
an already-refused transfer would fall through to a framing check, and one
transfer would emit two different wire codes at #1744.

`Assemble`'s two integrity comparisons (#1770) both read the assembled `out`
slice itself — never a proxy computed from the chunk map or the per-chunk
lengths — so a future regression in the assembly loop above can't slip past
a green check. That's also why `Assemble` must keep allocating a fresh slice
on every call rather than fast-pathing a single-chunk transfer to
`chunks[0]`: with a fresh copy, the bytes verified are the bytes returned, and
no later mutation of the accumulator's stored chunks — or of a caller's own
copy of a previous return — can retroactively change what an already-returned
slice contains. `internal/update`'s `VerifySHA256` is the nearest existing
"bytes against a declared hex digest" comparison in the repo and folds case
with `strings.EqualFold`; that's correct where it lives (a digest already
lowercased by its own file's parser) and wrong here — the published contract
in `docs/protocol-mobile.md` calls a case-insensitive or prefix comparison a
hole, in the same sentence that resolves the seeming tension with an
uppercase sender by requiring clients to send lowercase. The comparison here
is plain `!=` against `hex.EncodeToString`'s (lowercase) output — no
normalisation, no `subtle.ConstantTimeCompare` either, since both the bytes
and the claimed digest are attacker-supplied, so timing leaks nothing the
attacker doesn't already hold.

Neither integrity error string carries the declared `sha256` itself — it's an
attacker-chosen, JSON-decoded string headed for #1744's line-oriented log, the
same hazard `AttachmentChunkPayload`'s `Data` and `Filename` are already
banned from error strings for. `ErrDigestMismatch`'s message instead carries
the **computed** digest (daemon-authored, fixed shape, one-way) and
`len(a.sha256)` as a diagnostic for a truncated or empty claim. `len` on a Go
string is a **byte** count, not a rune count, and the message currently calls
it "characters" — code review flagged this as a NIT (not fixed, since the
value itself is still bounded and injection-free): a 64-emoji claim would
report 256. Worth remembering before reaching for `len(str)` on any other
attacker-supplied, JSON-decoded string in this codebase — say "bytes" or use
`utf8.RuneCountInString`, not both loosely.

**Presence comes from map-key membership, never from the stored value.**
`Add`'s duplicate check is a two-value lookup (`_, dup := a.chunks[i]`); a
`chunks[i] != nil` check would silently accept a re-sent zero-byte chunk (both
`nil` `Data` and an empty non-nil slice are legitimate "received" values). The
zero-byte-attachment criterion exists specifically to catch this class of bug.

## Mutation-testing lessons (measured across #1769, #1770, #1772, #1787, #1795, and #1796)

This package's sole-redness claims are measured with `go test -overlay`
(mutants applied via an absolute-path JSON manifest, no worktree write), not
argued from the table alone. Five traps found that way, in the order they
surfaced:

- **A mutant that perturbs a shared constant reddens more rows than a
  hand-derived table predicts, because moving the constant moves every
  boundary at once.** #1776's admission-layer table predicted its
  wrong-bound mutant (reading 32768 in place of `MaxAttachmentChunkBytes`)
  would redden the three rows placed *at* 45000; measured, it reddened five
  — the extra two were rows whose *distance* from the boundary changed sign
  once the bound moved, not rows sitting on it. The predicted set was a
  subset, so nothing was falsified, but the right way to predict a
  shared-constant mutant's red set is "every row whose arithmetic reads the
  constant, re-run under the new value" — not "every row placed at the
  constant, by inspection".

- **A presence check needs a value indistinguishable from absent.** #1769's
  spec credited the *identical-bytes* duplicate row as the sole red test for
  writing the duplicate check as `chunks[i] != nil` instead of the two-value
  map lookup. Measured, that row stayed **green**: it re-sends ten non-empty
  bytes, so a non-nil value is still parked at the key and the buggy check
  still reports the duplicate correctly. The mutant only surfaces where the
  stored value is legitimately nil — a **zero-byte** re-send — which is why
  `TestAccumulator_DuplicateZeroByteChunk` exists as the actual sole red for
  it. Generalization: a row aimed at "presence must come from the key, not the
  value" has to use the value that is *indistinguishable from absent* for the
  type in play; any other value can leave a presence-from-value mutant alive
  while looking like coverage. #1770's `size`/`sha256` checks were built with
  this trap in mind — see the next point.
- **A value-based digest assertion can't pin what the digest is computed
  over when the value doesn't vary.** #1770's zero-byte round-trip row (empty
  bytes, empty-input digest) is credited with nothing about the digest's
  input, because the empty digest is the same whether it's computed over the
  real assembly or over nothing — only the non-empty rows (flip a byte in the
  *last* chunk, not the first, so a "digest over `chunks[0]`" mutant can't
  survive it) pin that.
- **An overlay mutant that deletes a check can also delete the only use of an
  import, and that fails the *build*, not a test — and `go test` exits 1
  either way.** A mutation harness that reads the exit code, or counts
  `--- FAIL:` lines, scores a build failure as *green* ("no test covers
  this"), which is the opposite of what happened. Two of #1770's eight mutant
  claims first measured green for exactly this reason. Fix: keep the deleted
  check's import alive in the mutant (e.g. `sum := sha256.Sum256(out); _ =
  hex.EncodeToString(sum[:])` with the comparison itself removed), and grep
  the overlay run's output for `"build failed"` before trusting any verdict —
  a passing `go test` on a broken build looks identical to a passing one on
  working code from the exit code alone. The same trap has a second shape,
  found in #1772: a mutant that removes the sole **read** of a local variable
  (not just an import) breaks the build the same way — replacing an
  input-derived `if !kept` with a result-derived check left `kept` assigned
  and unread. Go's unused-variable rule makes a local at least as likely a
  casualty as an import, so grep the overlay run's output for both
  `"build failed"` and `"declared and not used"` before trusting a verdict,
  and give a mutant that deletes a read somewhere to keep it alive (`_ =
  kept`) so the measurement is honest.
- **A subtest name built from raw attacker-shaped input can contain a NUL,
  and that turns `go test -v | grep` output into "binary" data** (#1772,
  `t.Run(tt.in, …)` fed a row whose input was `"a\x00b"`). `grep` without
  `-a` collapses the per-row red-line output to a single `Binary file
  (standard input) matches`, which reads as "one red row" rather than as a
  tooling failure — silently weakening every sole-redness measurement taken
  against that suite. The naming convention itself (subtest name = raw input)
  is worth keeping, since it's what makes the classic `report.txt\x00.exe`
  row legible in test output at all; the fix is `grep -a`, not a different
  naming scheme. #1795 hit the same failure mode through a different door —
  non-UTF8 fixture bytes landing directly in `go test -v` output rather than
  in a subtest name — which is why `grep -a` belongs in the overlay-mutant
  recipe unconditionally, not only when a subtest name is attacker-shaped.
- **A behaviour-preserving refactor's characterization test is green before
  and after the change, so the diff alone can't show the test is
  load-bearing — only a mutant that would have broken the old code can**
  (#1795, `TestRegistry_AdmitRefusedRepeatUnderAHeldPair_KeepsTheIncumbent`).
  The new test passed against unmodified `registry.go`, correctly, since the
  ticket preserved the pinned behaviour rather than changing it; the only
  evidence the test caught anything came from the mutant that hoists the
  incumbent look-up ahead of both checks, red on exactly the two new rows and
  green on the rest of the package. Where a refactor's spec asks a new test
  to guard against one specific silent flip, running that flip as a mutant is
  the RED half of red-green, not an optional extra.
- **A mutant run filtered with `-run` can't support a "nothing else reddens"
  claim — the filter is exactly what would make the claim look true
  regardless of whether it is.** #1795's first pass scoped the run to
  `-run 'TestRegistry_|TestAccumulator_|TestCheck'`, which happened to cover
  the whole package but was never checked to. The unfiltered re-run (128
  tests) is what actually backs the mutant table's "also reddens: nothing
  else" column — any claim of that shape needs the filter off.
- **The mutant that only reddens a helper's own direct test, and nothing
  reached through the public surface, is the one that justifies the direct
  test's existence rather than merely adding to its coverage** (#1772: a
  plain `s[:maxBytes]` in place of `truncateToBytes`'s rune-safe loop
  reddened `TestTruncateToBytes` alone — nothing in `SanitizeFilename`'s own
  table went red). When an earlier pipeline step structurally constrains a
  later one (here: the allowlist step leaves the string pure ASCII before
  truncation ever runs), no fixture driven through the public function can
  falsify the later step's more careful implementation — only a test calling
  the unexported helper directly can. Treat that as a signal to add the
  direct test, not as a reason to skip it as redundant.
- **An unbounded caller-supplied parameter can still be a fine tradeoff
  purely because nothing can reach it** — flagged, not fixed, in #1772's code
  review: `truncateToBytes` with a negative `maxBytes` doesn't panic or
  return early, it spins forever (`utf8.DecodeLastRuneInString("")` returns
  `size == 0`, so the shrink-by-`size` loop stops shrinking while its `len(s)
  > maxBytes` guard stays true). Left as an informational NIT because the
  helper is unexported with one caller passing a positive constant — but the
  failure mode if that ever changes is a hang, not a crash, which is the
  detail worth carrying forward to whoever next gives this helper a
  caller-supplied bound.
- **A lock-contention mutant can be probabilistic, and the fix is more
  rounds, not more goroutines.** #1787's split-lock mutant on
  `Registry.insert` (look the pair up under one acquisition of `mu`, unlock,
  reacquire to store) reddens the spec's own same-pair concurrent-insert test
  at only ~9% per iteration; run at the spec's own prescribed `-count=5`, that
  recipe is a false green roughly 62% of the time (measured at `-count=500`:
  45/500 red). Raising the fan-out from 32 to 256 goroutines barely moved
  detection (9% → 13.8%) — the collision window exists only during the first
  insert burst, so more contenders barely widen it. What worked was more
  *rounds*: a fresh `Registry` per round inside a loop, ~75 rounds for ≈99.9%
  detection. Before trusting a `-count=N` recipe on a lock-contention pin,
  measure its actual per-iteration hit rate rather than assuming N repeats
  compounds toward certainty — and prefer scaling round count over goroutine
  count when it doesn't.
- **An injected split-lock mutant has to replace the whole critical section,
  not prepend an extra check ahead of it.** The first attempt at #1787's
  split-lock mutant added an unlocked-then-relocked pre-check before the
  original lookup-then-store body; the original body's own atomic check still
  ran underneath it, so the mutant stayed green — which looks like "the
  criterion can't be pinned" but is actually an under-built mutant. Delete and
  replace the section, don't layer around it.
- **A "returns a fresh copy" test must mutate its own copy of the fixture,
  not the shared package-level one.** `TestAccumulator_SingleChunk_ReturnsAFreshCopy`
  has to feed the accumulator a copy of `testFixture`, then mutate the
  *returned* slice to prove the accumulator's copy is independent. Feeding
  `testFixture` directly and mutating the return would scribble on the
  package-level fixture, reddening every other parallel test in the file
  under the fast-path mutant instead of just this row — a sole-redness
  measurement taken against that setup would report the wrong thing about a
  mutant that is genuinely caught.
- **Deleting a struct field's `+=` update is not the same hazard as deleting
  a local's sole read or an import** (#1777, refining the build-failure trap
  above). Dropping `a.received += int64(len(chunk.Data))` compiles cleanly —
  a field that's assigned but never read doesn't trip Go's unused-variable
  check the way a local does — so the mutant measured the crossing test's
  real coverage with no `_ = a.received` prop needed. The "grep for `build
  failed`" trap is specific to locals and imports; a field-update deletion
  needs no such guard before trusting its verdict.
- **A property true "by construction" still needs its own mutant, or it
  ships unpinned** (#1796, code review SHOULD FIX). The spec argued AC 3 —
  a held pair is never refused by the capacity gate — was satisfied by
  construction, because the incumbent look-up runs ahead of the gate in
  `insertLocked`. True of the code as written, but no fixture drove a held
  pair while the registry was at `maxInFlightUploads`, so an overlay mutant
  hoisting the gate ahead of the look-up passed **green across all 134**
  tests in the package. A by-construction argument describes the current
  shape of the code; it says nothing about whether a later one-line
  reordering would be caught. Treat every "satisfied by construction" claim
  in a spec as a mutant to build and run, not as a reason a row can be
  skipped — this one shipped as a known, tracked gap (see § "In-flight
  upload registry" above) rather than silently.
- **A split-lock control has to satisfy every other row, or "sole red"
  can't be measured.** #1796's first cut of the count-then-admit mutant
  (`Admit` reads `r.count()` before `mu.Lock()`, decides on the stale
  number inside the lock) returned `capacityRefusal` without first running
  `CheckDeclaration`/`CheckDeclaredSize`, so it also reddened the gate-
  ordering test (AC 1) — two rows red, neither one the sole red the AC 4
  measurement needed. The fix duplicated the declaration checks into the
  mutant's capacity branch so it satisfied every row except the
  indivisibility one. A control mutant built to isolate one property has to
  be built to pass every *other* property first, or its redness proves
  nothing about the one row it was meant to isolate.

- **A mutant that drops a release can redden a test whose *subject* is
  something else, if that test happens to end its fixture through the
  dropped path** (#1784). The mutant that skips `Release` after an `Add`
  refusal was predicted to redden only the four `Add`-refusal table rows;
  measured, it also reddened the held-pair-at-the-bound test, whose closing
  assertion re-delivers a chunk to prove the first one landed in the
  incumbent and, as scaffolding rather than as its point, ends that transfer
  through an `Add` refusal. The prediction was a subset, so nothing was
  falsified, but a sole-redness claim written from a test's *name* will
  understate itself wherever the test's fixture walks through the mutated
  path on its way to proving something else. Predict a mutant's red set from
  what each test *executes*, not from what it is named after.
- **A "distinct from every other sentinel in the package" claim needs the
  package's sentinel list to be a fixture, not a hand-picked sample of near
  misses** (#1784, `packageSentinels` in `registry_test.go`, all eight across
  `accumulator.go`/`admission.go`/`storage.go`). Three chosen-by-eye
  candidates would leave the quantifier ("every") unproved while reading as
  coverage; looping `!errors.Is(err, s)` over the actual declared set is what
  proves it. The cost is real and one-directional: a sentinel added later and
  not appended to that slice weakens the claim silently, with no test going
  red to flag the drift.
- **Predicting a mutant's red set means reading whether each assertion is
  absolute or relative, not just which fixture executes the mutated line**
  (#1880). The spec predicted the unstamped-admission mutant (`insertLocked`
  stores a zero `lastChunkAt`) would redden AC 1, AC 2 and AC 4; measured, it
  reddened AC 1, AC 3 and AC 4 instead — AC 2 stayed green. AC 2's
  post-delivery assertions are relational ("the delivered pair's stamp now
  equals the advanced reading; the untouched pair's stamp is what it was"),
  and a zero pre-delivery stamp that stays zero satisfies both comparisons
  unchanged; AC 3 reddens instead because it asserts the stamp equals the
  admission-time reading outright. The mutant was caught either way, so
  nothing about the shipped code was wrong — but a prediction built from
  "which test's fixture touches the mutated line" gets the row wrong where
  "what shape each assertion actually compares against" would not.
- **One mutant built to defend a by-construction claim doesn't inoculate the
  rest of the same doc against the same failure mode** (#1880, code review
  SHOULD FIX). Mutant 4 in this slice's spec exists specifically to pin
  `insertLocked`'s incumbent-before-store ordering — the same by-construction
  gap #1796 shipped unpinned and paid for, two entries above. The very next
  paragraph of that same spec claimed, also by construction and with no
  mutant of its own, that `Admit` and `insert` "inherit the stamp from this
  one shared core." Code review measured it: an overlay mutant moving the
  stamp out of `insertLocked` into `Admit`'s body passes green across all 151
  tests, because `insert` is unexported and test-only and no production path
  can reach it today. Not a behaviour bug in shipped code, but the exact
  invariant `reapExpiredLocked` (#1881) depends on — an entry inserted with a
  zero stamp reads as infinitely idle and is reaped on the very next pass —
  and #1881's own four new tests all drive `Admit`, so this still shipped
  unpinned rather than fixed.
  Having already named the by-construction trap once in a spec is not
  evidence the rest of that spec is safe from it — every by-construction
  sentence needs its own mutant, not just the one already flagged as risky.
- **A sweep-and-delete mutant's red set is a property of each fixture's
  cardinality, not of which AC the test's name cites** (#1817). The spec
  predicted `ReleaseConn`'s stop-after-first-delete mutant (`return`
  immediately after the loop's first `delete`) would redden only the tests
  named for "every upload" and "the freed slots"; measured, a third reddened
  too, because that test's conn happened to hold two entries rather than one.
  Same discipline #1784 already states two entries above ("predict from what
  each test executes, not from what it is named after"), refined for this
  mutant shape specifically: for a sweep over a multi-entry key space, read
  each fixture's admit calls for how many entries the mutated conn holds,
  not the AC label on the test.
- **A fixture helper's own unexported naming scheme is not a citable
  contract** (#1817). `fillRegistry` names its ids with a private `filler-%d`
  pattern; three of `ReleaseConn`'s four new tests admit their pairs directly
  instead of calling it, because each needed either a second conn or to
  `Lookup` an id back out afterward, and copying the helper's naming scheme to
  the call site would cite an implementation detail with no symbol for
  `cite-guard` to anchor — the one rot shape that guard cannot see. Widening
  `fillRegistry` to take a conn parameter was the alternative and is worse:
  several existing capacity tests depend on it staying single-conn.

All three properties #1769 shipped without a test pin are now pinned,
landed alongside #1770's own checks rather than left for a third mutation
pass to rediscover: `Assemble` on a declared `total_chunks == 0` answering
`ErrIncomplete` rather than a vacuous empty success
(`TestAccumulator_ZeroDeclaredCount_IsIncomplete`), a refused transfer's
`chunks` map going `nil` at the moment of refusal (the shared reject-row body
in `TestAccumulator_IntegrityFaults_RejectAndDiscard`), and `Assemble` never
fast-pathing a single-chunk transfer to `chunks[0]`
(`TestAccumulator_SingleChunk_ReturnsAFreshCopy`, the same test the
fixture-copy trap above is about).

## What a successful `Assemble` does not mean

Both #1770 checks pass when the assembled bytes match what the transfer
declared — that's integrity, not authenticity. The same party (the uploading
client) supplies both the bytes and the declared digest, so a match proves
the transfer wasn't corrupted in transit and proves nothing about whether the
content is safe. `protocol.AttachmentChunkPayload`'s doc block states this
directly; it matters here because the consumers of a successful `Assemble`
— #1772's filename sanitiser, #1781/#1782's storage, #1746's retrieval — are the
ones who'd otherwise read a green `Assemble` as a safety verdict rather than
a corruption check.

The declared `sha256` is also deliberately **not** promoted to a lookup key
anywhere in this package: there's no content-addressed retrieval ("know the
hash, fetch the blob"), which is a requirement from
`AttachmentChunkPayload`'s SECURITY block, not an oversight.

## Blocked family (not landed)

- **#1767** — closed, split into this family. The first-chunk
  `total_chunks`/`size` cross-check landed as `CheckDeclaration` (#1776, see
  § "Admission layer" above), the per-upload byte bound landed as
  `CheckDeclaredSize` + `Add`'s step 5 (#1777, see § "Per-upload byte bound"
  above), and the conn-keyed registry of in-flight uploads landed as
  `Registry` (#1787, its admission decision moved under one lock acquisition
  by #1795, and given its entry-count cap by #1796 — refiled after #1786
  closed without shipping it — see § "In-flight upload registry" above).
- **#1742** — closed, split into this family. Expiry/abandonment of a partial
  upload landed as `uploadIdleTimeout` / `reapExpiredLocked` (#1881, see
  § "In-flight upload registry" above). That reap covers *partial* uploads
  only — a complete-but-corrupt transfer that fails one of #1770's integrity
  checks is not partial, so it's #1770's own `reject` latch, not the reap,
  that frees those held bytes.
- **#1773** — split into #1781 and #1782 while this family waited. Resolving
  and creating the attachment directory landed as `EnsureDir` (#1781, see
  § "Directory resolution and creation" above); writing bytes into it landed
  as `Store` (#1782, see § "Writing attachment bytes" above). (#1743 split
  into #1772/#1773 earlier in the same wait; #1772 — filename sanitisation —
  landed, see `SanitizeFilename` above.)
- **#1744** — wires the dispatch site: maps the three framing sentinels to
  `CodeAttachmentInvalidChunk`, the two integrity sentinels to
  `CodeAttachmentIntegrityFailed`, and `ErrTooManyUploads` to
  `CodeAttachmentTooManyUploads`, all via `errors.Is`. Now unblocked and free
  to land — #1796 discharged the family's sequencing constraint that this
  ticket not go first, and #1784 shipped the routing surface (`Registry.Deliver`)
  it is expected to call (see § "In-flight upload registry" above) — and is the
  first production caller of this package. Four things are parked for
  whichever of #1744 or the documentation phase next touches this area: which
  wire code `ErrUnknownUpload` maps to (#1784 deliberately named no
  candidate); the never-log claim on `Deliver`'s doc block, which code review
  found unpinned by any fixture (see § "In-flight upload registry" above);
  and two more #1817's code review flagged. `uploadIdleTimeout`'s doc
  (`admission.go`) reads "IT DOES NOT SUBSUME #1817, which releases a dropped
  conn's uploads AT THE DROP" — true only once #1744 wires that call, not on
  #1817's own merge, since #1817 ships `ReleaseConn` with no production
  caller; #1744 is what makes that sentence describe what actually ships
  rather than a closed ticket by what it didn't. And `Release`/`ReleaseConn`'s
  own docs (`registry.go`) both now name #1744 as the caller that decides
  when either runs — right only if #1744's teardown wiring lands as this
  family has planned it; #1744's own issue body scopes it to
  `dispatchAppFrame`'s frame switch and names no teardown or `closeWith`
  path, so if it refines to the chunk path alone, those two cites need a
  different, as yet unticketed, owner.
