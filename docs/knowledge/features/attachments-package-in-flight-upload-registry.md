# In-flight upload registry (#1787, #1788, #1795, #1796, #1880, #1881, #1817)

`Registry` (`registry.go`) gives `Accumulator` somewhere to live between
chunks: a map from `uploadKey{connID, attachmentID}` to an
`entry{acc *Accumulator, lastChunkAt time.Time}`, held **by value** so the
stamp can only move by storing into the map under `mu` — an unexported
`insertLocked` core, a thin lock-taking `insert` wrapper kept for tests,
comma-ok `Lookup`, an unexported stamping `lookupAndStamp`, `Release`, its
conn-keyed sibling `ReleaseConn` (#1817, removes every upload one conn holds
in a single pass, for a teardown path that has the conn but never the list of
attachment_ids it was admitted under), and two unexported test-only readers,
`count` and `lastChunkAt` (#1880). `Intake` (#1896, see § "Chunk intake
driver" below) is the registry's caller — `#1744`, the ticket this doc used
to cite, split into #1896 (this driver) and #1897 (the dispatch site). The
registry is reachable from outside `internal/attachments` as of #1897, which
wired `internal/relay`'s `appFrameWorker` to `Intake`.

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
stamp** — a look-up that moved the activity time would let a diagnostic or #1897's dispatch-site read keep a dead upload alive indefinitely, the exact
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
`CodeAttachmentTooManyUploads` (mapped at #1897's dispatch site, not here)
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
ever reach one accumulator. **#1896 landed `Deliver`'s first caller:**
`Intake.Receive` calls it with the same chunk it just fed to `Admit`, looking
the pair back up rather than feeding `Admit`'s returned accumulator directly
— the latter would bypass every release this ticket added and re-open the
lockout for single-chunk transfers. That caller is still internal to this
package; #1897 wires the first caller from outside it.

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
not covered until a fixture tries to violate it.

**Still open in `Deliver`'s own suite.** #1896 pinned the equivalent property
one layer up — `Intake`'s own tests (`intake_test.go`) roll a
banned-string assertion into every refusal row reachable from `Receive`,
`Deliver`'s included, so the property is now covered wherever `Receive` is
the entry point. That is not the same fix as the one parked here: nobody
added the three lines to `registry_test.go` itself, so the same wrap on
`Deliver` measured directly (bypassing `Intake`) is still unmeasured. A
future change to `Deliver` reached through some caller other than `Intake`
would not be caught by #1896's fixture. Parked for whichever of #1897 or the
documentation phase next touches `Deliver` directly — the fix is still the
three lines described above, the idiom
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
  of the driver that composes `Admit`.** #1787/#1788's security review
  required #1744 — since split into #1896 (this driver) and #1897 (the
  dispatch site) — not land ahead of the in-flight entry-count cap: wiring a
  caller first would have gone live with `Admit` bounding one upload's bytes
  but placing no bound on how many uploads may exist, so N distinct
  `attachment_id` values on one conn would yield N entries. That gap was
  closed in order: `maxInFlightUploads` (#1796) landed first, `Intake`
  (#1896) became the package's first production caller, and #1897 is now
  the package's first caller from outside it.

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
