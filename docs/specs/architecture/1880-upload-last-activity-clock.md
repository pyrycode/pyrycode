# #1880 — stamp each in-flight upload with its last activity, from an injected clock

**Size:** S (PO's `size:s` confirmed — re-derived below, § Sizing).
**Package:** `internal/attachments` (one production file: `registry.go`).
**Blocks:** #1881 (idle window + reaper), #1817 (release every upload held by one conn).

## Files to read first

Symbols, not lines. Resolve each with `codegraph_search` / `codegraph_node`.

| File | Symbol | What to extract |
|---|---|---|
| `internal/attachments/registry.go` | `Registry` (type doc) | The `mu`-is-a-leaf paragraph and the "five locked methods" roster — both are rewritten by this slice. Also the "one `*Accumulator` per conn-and-attachment_id pair" sentence. |
| `internal/attachments/registry.go` | `insertLocked` | The store site. This is where the admission stamp lands, and where the incumbent and capacity branches return *before* the store. |
| `internal/attachments/registry.go` | `Admit` | Confirms the whole decision runs under one acquisition of `mu`; unchanged by this slice except through `insertLocked`'s body. |
| `internal/attachments/registry.go` | `Lookup` | The body changes by one expression; the doc gains the load-bearing "deliberately does not stamp" paragraph. |
| `internal/attachments/registry.go` | `Deliver` | Its step-1 call site, its numbered step list, and the acquisition-count paragraph — all three are rewritten. |
| `internal/attachments/registry.go` | `count` | The precedent for an unexported member whose only callers are in-package tests. Copy its doc's shape for the new accessor. |
| `internal/streamsup/watchdog.go` | `newStallTracker` | The repo's nil-tolerant clock seam, verbatim idiom: `if now == nil { now = time.Now }` inside the constructor. Also `stallTracker`'s `now func() time.Time` field placement. |
| `internal/streamsup/watchdog_test.go` | `fakeClock` (its `now` and `advance` methods) | The controllable-clock double to copy into `registry_test.go`. This package has no clock double yet. |
| `internal/attachments/registry_test.go` | `TestRegistry_DeliverIncomplete_KeepsTheEntry` | The exact fixture AC 2 needs: a declaration that survives `Add` and answers `ErrIncomplete`, so the entry is still held after the delivery. |
| `internal/attachments/registry_test.go` | `boundChunk`, `withAttachmentID`, `testBoundTotal`, `testConnA`, `testConnB`, `testAttachmentID` | The fixture helpers every new test must reuse. Do **not** invent a declaration — see § Fixture admissibility. |
| `internal/attachments/registry_test.go` | `TestRegistry_AdmitRefusedRepeatUnderAHeldPair_KeepsTheIncumbent` | The shape AC 3's repeat-`Admit` clause extends: same held-pair fixture, one more assertion. |
| `docs/knowledge/features/attachments-package.md` § "In-flight upload registry" and § "Mutation-testing lessons" | — | Every landed decision about `mu`, the incumbent look-up, and this package's overlay-mutant recipe. Read the "satisfied by construction" and "filtered `-run`" entries before measuring anything. |

## Context

`Registry` holds each in-flight upload's `*Accumulator` under a `{connID, attachmentID}` pair and records nothing about *when* the pair was last touched, so nothing can distinguish a transfer mid-stream from one abandoned an hour ago. #1881 is the policy that reaps an idle entry; it needs a truthful activity time to measure, and it needs to be provable by advancing a clock rather than by sleeping.

This slice ships the bookkeeping and no policy: a clock seam, a per-entry stamp that admission sets and each delivered chunk moves forward, and an unexported test-only reader. No exported surface changes, no signature changes, no behaviour change to any answer an existing caller sees.

**No production reader is the package's established shape, not a concession.** `Lookup` and `Release` landed in #1787 with no production caller; `Deliver` has had none since #1784. #1744's dispatch site is this package's first production consumer and lands last in the family.

**No ADR.** This adds no cross-cutting decision — it copies an existing repo-wide seam (`newStallTracker`) into a third package.

**For the documentation phase:** the package overview (`docs/knowledge/features/attachments-package.md`) carries two claims this slice falsifies — "Three separate acquisitions of `mu` per delivered chunk" in § "In-flight upload registry", and the "five locked methods (`Admit`, `insert`, `Lookup`, `Release`, `count`)" roster in the same section. Both need updating with the numbers § "Claims to rewrite" below re-derives. Not a developer deliverable; that file is the documentation phase's.

## Design

### The map value becomes a struct, held by value

```go
type entry struct {
    acc         *Accumulator
    lastChunkAt time.Time
}
```

`Registry.uploads` becomes `map[uploadKey]entry`.

**Named `entry`, not `upload`.** `upload` is already a named return in `insertLocked` and `insert`, and a local in `Admit`, `Deliver` and a dozen tests. A package-level type by that name is shadowed at exactly the store site that needs it — inside `insertLocked`, where the named return `upload *Accumulator` is in scope — so it would not compile there. `entry` collides with nothing (it appears in this package only inside test error strings) and matches the prose the type already uses: "`Release` removes exactly the pair's entry".

**Held by value, not `*entry`.** A pointer would let an in-package caller hold the entry and read or write `lastChunkAt` with `mu` released. A value forecloses that structurally: the stamp can only be mutated by storing into the map, and the map can only be written under `mu`. Same argument `uploadKey`'s own doc makes for being a struct rather than a concatenated string — foreclose it in the type, don't document a caller obligation. Cost is a 32-byte copy per map read, at a bound of `maxInFlightUploads` (4).

**Field named `lastChunkAt`, not `lastActivity`.** The name is the guard rail. "Activity" invites a later reader to stamp on a look-up or a diagnostic — the exact over-eager stamp that would re-open at this layer the exhaustion path #1881 exists to close. `lastChunkAt` states the only event that may move it.

`Registry.lastChunkAt` (the accessor, below) and `entry.lastChunkAt` (the field) share a name on two different types. That is legal Go — method and field namespaces are per-type — and deliberate: the accessor answers exactly the field. The receiver disambiguates at every use site (`r.` vs a local `entry`).

### The clock seam

`Registry` gains one field, `now func() time.Time`, and one unexported constructor:

```go
// newRegistryWithClock returns an empty Registry reading now as its clock
// (nil → time.Now). Callers are this package's tests.
func newRegistryWithClock(now func() time.Time) *Registry
```

`NewRegistry()` keeps its signature and body-delegates: `return newRegistryWithClock(nil)`. The nil substitution lives in `newRegistryWithClock` alone, so `time.Now` is named in exactly one place and nil-tolerance is a property of the shared core rather than duplicated across two constructors.

**Why optional rather than a required parameter.** `NewRegistry()` has 19 call sites, all in `registry_test.go`, and none outside this package. A required clock parameter forces 19 simultaneous edits and trips the ≤10-consumer-call-site boundary on its own. The nil-tolerant seam is already the repo's shape at `newStallTracker`, `newStreamParser` and `streamjson`'s emitter constructor; adopting it leaves all 19 untouched.

**Why unexported.** Every site that would supply a clock is a test in this package. `count` is the precedent. Exporting it would publish a seam with no production caller and invite a caller outside this package to install a clock the registry's own invariants depend on.

**The seam is read with `mu` held** (both stamping sites, below). That makes three constraints on the injected function, and `newRegistryWithClock`'s doc must state them: it must not block, must not call back into the `Registry`, and must be safe for concurrent use. `time.Now` satisfies all three; the test double satisfies them by taking its own mutex and touching nothing else, so the only lock order in play is `Registry.mu` → clock, never the reverse.

**Why read under `mu` rather than before the acquisition.** `Admit`'s doc already states that the whole decision — declaration verdict, incumbent look-up, capacity, construction, store — runs under one acquisition. A stamp read outside would be the one part of that decision computed on a value that predates the section, and threading a `time.Time` through `insertLocked` would widen a signature to buy nothing: the stamp feeds a bounded idle window, so sub-microsecond staleness is not a property anything measures. It also breaches none of the three rules the type doc states — `mu` is still never held across a call into `Accumulator`, no method that takes `mu` calls another that takes it, and `Deliver` still takes it at no point. `Admit` already calls `NewAccumulator` under `mu` on the same reasoning, with the bounded-work argument written out; this is the same argument for a cheaper call, and the constructor doc is where the bound is now stated for a *caller-supplied* function.

### The two stamping sites

**Admission — `insertLocked`.** The store line becomes a store of an `entry` carrying `r.now()`. Nothing else in the method changes: the incumbent branch still returns `held.acc` before the store, and the capacity branch still returns before the store, so **every refusal still stores nothing and stamps nothing**. Both `Admit` and `insert` inherit the stamp from the one shared core, which is what keeps `insert`-inserted entries from carrying a zero-value stamp that #1881's reaper would treat as infinitely idle.

**Delivery — a new `lookupAndStamp`.**

```go
// lookupAndStamp answers the pair's accumulator comma-ok and, on a hit, moves
// that entry's lastChunkAt to the clock's reading. On a miss it stores nothing.
// Takes mu for its whole body.
func (r *Registry) lookupAndStamp(connID, attachmentID string) (*Accumulator, bool)
```

`Deliver`'s step 1 calls this instead of `Lookup`. On a hit it re-stores the entry with a fresh `lastChunkAt` and returns `u.acc, true`; on a miss it returns `nil, false` and writes nothing, preserving `Deliver`'s existing "NOTHING IS STORED" contract on the unknown-pair path.

**`Lookup` stays a pure read.** Its body changes by one expression (`u.acc` in place of the stored value) and nothing else. A read that stamped would let any later diagnostic or dispatch-site look-up keep a dead upload alive indefinitely.

**Why the delivery stamp sits at the look-up rather than after `Add` succeeds.** The two placements are observationally identical, and the look-up placement adds no `mu` acquisition. Enumerate `Deliver`'s four outcomes:

| Outcome | Entry after | Stamp observable? |
|---|---|---|
| miss | never existed | no |
| `Add` refuses | released | no — `Deliver` releases on *any* non-nil `Add` answer |
| `Assemble` → `ErrIncomplete` | kept | **yes** — and both placements write the same value here |
| complete, or integrity failure | released | no |

The entry survives exactly one outcome, and on that outcome `Add` accepted, so "a chunk arrived and was accepted" and "a chunk arrived" agree. Stamping after `Add` would buy nothing and cost a fourth acquisition.

### Reader for the tests

```go
// lastChunkAt is when a chunk last arrived for the pair, comma-ok.
// Unexported: in-package tests are its only reader. Precedent: count.
func (r *Registry) lastChunkAt(connID, attachmentID string) (time.Time, bool)
```

**It must read `r.uploads` directly under `mu` — never by calling `Lookup` or `lookupAndStamp`.** This is a testability constraint, not style: the mutant that makes `Lookup` stamp (§ Testing) can only be isolated to AC 3's `Lookup` clause if the accessor does not itself route through `Lookup`. An accessor built on `Lookup` would redden under that mutant for its own reason and destroy the sole-red measurement.

### Locked-method roster

Five becomes **seven**: `Admit`, `insert`, `Lookup`, `lookupAndStamp`, `Release`, `count`, `lastChunkAt`. Each takes `mu` for its whole body; none calls another. `insertLocked` remains the one caller-holds-the-lock body, shared by `Admit` and `insert`. `Deliver` still takes `mu` at no point — it composes `lookupAndStamp` and `Release`, each of which takes it for its own whole body, and feeds the accumulator between them off-lock.

### What does not change

No exported symbol is added, removed, or re-signed. `insertLocked`, `insert`, `Admit`, `Lookup`, `Release`, `Deliver` and `count` keep their exact signatures. No existing test changes. Every answer an existing caller sees — every sentinel, every accumulator pointer, every count — is byte-identical to today.

## Claims to rewrite

These are load-bearing prose a reader relies on. **Rewrite exactly these and nothing else** — do not re-word claims this slice does not falsify.

1. **`Registry` type doc, "one `*Accumulator` per conn-and-attachment_id pair."** Now one `entry` per pair, holding that pointer together with the time a chunk last arrived for it. Say why the value is a struct held by value rather than a pointer.
2. **`Registry` type doc, "the five locked methods — `Admit`, `insert`, `Lookup`, `Release` and `count` — take `mu` for their whole body and never call one another."** Seven now, named above. The rule itself is unchanged and stays stated in its re-scope-proof form.
3. **`Registry` type doc — new paragraph on the clock seam.** That `now` is read with `mu` held, and the three constraints that puts on an injected function.
4. **`Deliver`'s numbered step list, step 1.** "Look the pair up" becomes "look the pair up and stamp it"; the "NOTHING IS STORED" clause on the miss path is still true and stays.
5. **`Deliver`'s "It TAKES `mu` AT NO POINT. `Lookup` and `Release` each take it for their own whole body … so three acquisitions per delivered chunk" paragraph.** `Deliver` still takes `mu` at no point, but the look-up is no longer `Lookup`, **and the number three is wrong today.** Re-derive it rather than carrying it across the edit:

   | Path | Acquisitions |
   |---|---|
   | miss | 1 (`lookupAndStamp`) |
   | `Add` refuses | 2 (`lookupAndStamp`, `Release`) |
   | `Assemble` → `ErrIncomplete` | 1 (`lookupAndStamp`) |
   | complete or integrity failure | 2 (`lookupAndStamp`, `Release`) |

   At most two, never three — and that is already true of the shipped code, where the pair is `Lookup` + `Release`. The likely origin of "three" is counting `Admit` + `Lookup` + `Release` across a whole single-chunk transfer, which is not what "per delivered chunk" means in a doc on a method that never calls `Admit`. Write the per-path form.
6. **`Lookup`'s doc, "the bool is what `Deliver` turns into … `ErrUnknownUpload`."** `Deliver` no longer calls `Lookup`; `lookupAndStamp`'s bool is what becomes `ErrUnknownUpload`. Replace with what `Lookup` is *for* now — a pure read for #1744's dispatch site and for diagnostics — and add the load-bearing sentence: **it deliberately does not stamp**, because a look-up that moved the activity time would let a diagnostic or a dispatch-site read keep a dead upload alive indefinitely, which is the exhaustion path this family is closing.
7. **`NewRegistry`'s one-line doc.** It reads the real wall clock and is the only construction path outside this package's tests; name `newRegistryWithClock` as the clock-taking way in.
8. **`insertLocked`'s doc.** One touch: it stores an entry carrying the clock's reading, and the two refusal branches still store nothing — so **no refusal stamps**, and a repeat under a held pair leaves the incumbent's stamp where it is.

## Concurrency model

No goroutines. One mutex, unchanged in role.

- `mu` remains a leaf. Never held across `Add` or `Assemble`. No `mu`-taking method calls another. `insertLocked` is still the one body that runs with `mu` held by its caller.
- The one new kind of call under `mu` is `r.now()`. In production that is `time.Now`; in tests a double that takes only its own mutex. The single lock order is `Registry.mu` → clock. The clock never calls back into the `Registry`, which is what makes that order a total order rather than a cycle waiting to happen; `newRegistryWithClock`'s doc states it as a precondition on the seam.
- Off-lock feeding is unchanged and sound for the same reason as before: the conn is in the key and `appFrameWorker` serialises one conn's frames, so exactly one goroutine can reach any one accumulator.
- The stamp is never read off-lock. Holding the map value by value is what enforces that rather than documenting it.
- Per-entry stamp monotonicity holds because the clock is read inside the same critical section that performs the store, so two concurrent stampers of one pair cannot commit out of order. In production the case cannot arise at all (one goroutine per conn); the property matters only so this package's own concurrency tests can never observe a stamp going backwards.

## Error handling

No new failure mode, no new sentinel, no new reject branch. Every existing refusal path returns before its store and therefore before its stamp. `Deliver`'s answers are unchanged on all four paths.

The never-log rule stays structural: this slice adds no format string. `lookupAndStamp` and `lastChunkAt` take the caller's own two scalars and return either an accumulator or a `time.Time`; neither can see `Filename`, `Data`, or a declared digest.

## Testing strategy

`make check`. Four new test functions plus one clock double, all in `registry_test.go`.

### Fixture admissibility

Do **not** invent a declaration. `Admit` runs `CheckDeclaration` and `CheckDeclaredSize`, so a pair valid via `NewAccumulator` can still be inadmissible via `Admit`. Reuse the one admissible multi-chunk declaration the file already has bytes and a digest for — the `(testBoundTotal, maxUploadBytes, testBoundFixtureDigest)` triple that `TestRegistry_DeliverIncomplete_KeepsTheEntry` uses — and deliver `withAttachmentID(boundChunk(0), testAttachmentID)`, which `Add` accepts and `Assemble` answers `ErrIncomplete` for, leaving the entry held and stamped.

### The clock double

Copy `fakeClock` from `internal/streamsup/watchdog_test.go`: a struct with a `sync.Mutex` and a `time.Time`, methods `now() time.Time` and `advance(d time.Duration)`, both under the mutex. It locks even though the new tests are single-goroutine — the package's existing concurrency tests are one edit away from wanting a fake clock, and a lock-free double would be a `-race` landmine for whoever makes that edit.

Compare `time.Time` values with `Equal`, never `==` or `reflect.DeepEqual` (`docs/PROJECT-MEMORY.md` § "`time.Time` round-trip discipline"). For the real-clock bracket, use `Before`/`After`.

### Scenarios

- **AC 1 — `TestRegistry_AdmitStampsTheClockReading`.** Build over a fake clock at a fixed instant. `Admit` the bound declaration. Assert the pair's `lastChunkAt` equals the clock's reading. Advance the clock and assert the stamp did *not* follow — the stamp is a recorded instant, not a live read of the seam.
- **AC 2 — `TestRegistry_DeliverMovesTheStampForwardForThatPairOnly`.** Two held pairs: the same `testAttachmentID` on `testConnA` and `testConnB` (the shape `TestRegistry_SameAttachmentIDOnTwoConns_AreSeparateEntries` already uses). Record both stamps. Advance the fake clock. `Deliver` chunk 0 to `testConnA`'s pair, expecting `ErrIncomplete` so the entry survives. Assert `testConnA`'s stamp is now the advanced reading and `testConnB`'s is exactly what it was.
- **AC 3 — `TestRegistry_LookupAndRepeatAdmitLeaveTheStampWhereItIs`.** One held pair. Record the stamp. Advance the clock. Then, asserting the stamp is unchanged after each: (a) `Lookup` the pair; (b) `Admit` the same pair again with the same admissible declaration, asserting it still answers the incumbent pointer, exactly as `TestRegistry_AdmitUnderAHeldPair_KeepsTheIncumbent` does today. Advance between (a) and (b) so neither clause can pass on a clock that failed to move.
- **AC 4 — `TestRegistry_NewRegistryReadsTheWallClock`.** No sleeping. Bracket: `before := time.Now()`, `NewRegistry()`, `Admit`, `after := time.Now()`; assert the stamp is neither `Before(before)` nor `After(after)`. This is falsified by a zero-value stamp, by an unset seam, and by any fixed-instant substitute — which is every way the delegation could go wrong. All three values carry monotonic readings, so the comparison is exact.

### Mutants

This package measures sole-redness with `go test -overlay=<abs-path manifest>` rather than arguing it from a table. Run **unfiltered** — a `-run` filter is exactly what would make a "nothing else reddens" claim look true regardless. Pipe through `grep -a`, and grep the output for `build failed` and `declared and not used` before trusting any verdict: a build failure exits 1 and reads as green to a harness counting `--- FAIL:` lines.

| # | Mutant | Predicted red |
|---|---|---|
| 1 | `insertLocked` stores an `entry` with no stamp (zero `time.Time`) | AC 1, AC 2 and AC 4 — **not** a sole red, and stated as such rather than trimmed to look like one. AC 2 reads the pre-delivery stamp, so it reddens too. |
| 2 | `lookupAndStamp` does not stamp (i.e. `Deliver` reverts to `Lookup`) | AC 2 alone |
| 3 | `Lookup` stamps (its body becomes `lookupAndStamp`'s) | AC 3's `Lookup` clause alone — **only if** `lastChunkAt` reads the map directly. Verify that before trusting the verdict. |
| 4 | `insertLocked` stamps in the incumbent branch, before the early return | AC 3's repeat-`Admit` clause alone |
| 5 | `lookupAndStamp` stamps every entry in the map, not just the pair's | AC 2's "other pair unchanged" clause alone |
| 6 | `newRegistryWithClock` substitutes a fixed instant (e.g. `time.Time{}`) for a nil clock | AC 4 alone |

Mutant 4 is the one this package's own history demands. #1796 shipped a by-construction ordering claim about `insertLocked` — that a held pair is exempt from the capacity gate — which a hoisting mutant then passed green across all 134 tests. AC 3's repeat-`Admit` clause is the same shape of claim about the same method, and must not ship on a construction argument alone.

Predict each mutant's red set from what each test *executes*, not from what it is named after (#1784's lesson: a mutant reddened a test whose subject was something else, because that test's fixture walked the mutated path as scaffolding).

## Sizing

Re-derived against the written spec, counting insertions into `internal/attachments/*.go` only, calibrated against the family's landed commits (#1787 = 397/3, #1795 = 206/2, #1796 = 472/4, #1784 = 529/2).

| Boundary | Limit | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **1** (`registry.go`) |
| Total written work | ≤ 400 | **≈ 330** (≈ 155 production incl. doc rewrites, ≈ 175 test incl. the clock double) |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — no signature changes; `NewRegistry`'s 19 sites untouched by the nil-tolerant seam |
| Acceptance criteria | ≤ 5 | **4** |
| Distinct error/reject branches | ≤ 10 | **0 new** |

Nearest analogue is #1795 (206/2): a doc-heavy restructure of `registry.go` plus one new test, no exported-surface change. This slice adds more surface than #1795 did (a type, a constructor, two methods) but touches one production file to #1795's two, and adds no sentinel, no constant, and no fixture machinery. Every line holds; no split.

## Open questions

- **`entry`'s field order and any future third field are #1881's problem, not this slice's.** This slice ships exactly the two fields it needs.
- **Whether #1881's reaper reads through `lastChunkAt` or iterates the map under `mu`** is #1881's call. The accessor here is shaped for a single-pair test read and is not a claim about what the reaper should use.
- **Pre-existing rot, out of scope and not introduced here:** `Store`'s doc in `storage.go` cites `Registry.Save`, which does not exist. Outside this slice's diff, so `make cite-guard` will not flag it and code review should not read it as this ticket's. Worth a follow-up ticket; not an AC.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. This slice adds no boundary and moves no data across one. Both new methods take the caller's own two scalars — `connID`, which is the receiver's own name for the conn and never on the wire, and `attachmentID`, already established by `uploadKey`'s doc as certifying nothing — and return either an `*Accumulator` already reachable through `Lookup` or a daemon-authored `time.Time`. The stamp itself is daemon-authored: it comes from the registry's own clock, never from a client-supplied value, so no attacker-chosen number enters the entry. The one boundary this design *does* touch is the injected `now` function, whose trust is asserted at `newRegistryWithClock` and whose only production supplier is `time.Now`; keeping the constructor unexported is what keeps the set of suppliers inside this package.
- **[Tokens, secrets, credentials]** Not applicable — no credential, token, or key is created, stored, compared, or transported. The `attachmentID` in play is documented as not a capability.
- **[File operations]** Not applicable — this slice touches no filesystem path. `EnsureDir` and `Store` are untouched, and nothing here feeds either.
- **[Subprocess / external command execution]** Not applicable — no `exec`, no environment read.
- **[Cryptographic primitives]** Not applicable — no randomness, no hashing, no comparison against a secret. `Assemble`'s digest comparison is untouched. In particular the stamp is deliberately **not** derived from or mixed into any digest.
- **[Network & I/O]** No findings. No socket, no read loop, no new size limit needed: the stamp is a fixed-width field on an entry already bounded by `maxInFlightUploads`, so the added memory is 4 × 24 bytes daemon-wide and cannot be scaled by any client input.
- **[Error messages, logs, telemetry]** No findings. This slice adds no format string, no logger, and no error. The never-log rule that `protocol.AttachmentChunkPayload`'s SECURITY block states stays structural for the same reason it does today: `lookupAndStamp` and `lastChunkAt` cannot see `Filename`, `Data`, or a declared digest, and neither constructs a message. Noted for #1881 rather than fixed here: an idle-expiry log line is the natural place for a client-chosen `attachment_id` to first enter a message on this path, and it must not.
- **[Concurrency]** SHOULD FIX, addressed in the spec rather than left to the developer. Reading `r.now()` with `mu` held introduces the package's first *caller-supplied* call under its only mutex, which is a new lock-order edge (`Registry.mu` → whatever the clock locks). The design closes it two ways: `newRegistryWithClock` stays unexported so only in-package tests can supply a function, and its doc states the precondition — must not block, must not call back into the `Registry`, must be safe for concurrent use — making the order total rather than a cycle. Code review should check the doc actually says all three. Separately, holding the map value **by value** rather than as `*entry` is what makes "the stamp is never read off-lock" a property of the type instead of a caller obligation; a developer who "simplifies" it to `*entry` to avoid the read-modify-write reopens exactly that. No goroutine is spawned, so no lifecycle question arises.
- **[Threat model alignment]** No findings, with one boundary named. `docs/protocol-mobile.md` § Attachments requires that `attachment_id` never be the only thing between a caller and a file; unchanged, since the conn stays in the key. The threat this family is closing — a client holding in-flight slots open indefinitely and exhausting `maxInFlightUploads` — is addressed here only by making the *measurement* truthful; the reaping that hands the slot back is **out of scope, deferred to #1881**, which is wired blocked-by this ticket. The one way this slice could make that threat *worse* is an over-eager stamp, and AC 3 exists to foreclose it: neither a look-up, nor a diagnostic, nor a repeat `Admit` under a held pair moves the time, so no sequence of calls a client can drive without sending a fresh acceptable chunk keeps an entry alive. Mutants 3 and 4 are the pins on that, and mutant 4 specifically defends the branch whose predecessor (#1796's gate ordering) shipped unpinned on a by-construction argument.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-31
