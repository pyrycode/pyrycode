# #1881 — Expire an attachment upload that has gone idle

## Files to read first

Everything this slice touches is in `internal/attachments`. Read in this order.

- `internal/attachments/registry.go` → the `Registry` type doc — the lock contract in full: `mu` is a LEAF, the seven locked methods, the two caller-holds/no-lock methods, and the paragraph beginning "THE CLOCK IS A SEAM". Three of the sentences you must rewrite live in this one comment.
- `internal/attachments/registry.go` → `insertLocked` — the shared critical section `Admit` and `insert` both run, and the home of the `maxInFlightUploads` gate and the admission stamp. **The reap goes at the head of this body.** Extract: why it takes no lock, and why the capacity gate reads `len(r.uploads)` directly rather than calling `count`.
- `internal/attachments/registry.go` → `lookupAndStamp` — `Deliver`'s stamping look-up. **The reap goes at the head of this body too.** Extract: its miss path stores nothing, and it takes `mu` for its whole body.
- `internal/attachments/registry.go` → `Admit`, `Deliver` — the two public paths the reap must run under. Extract: `Deliver`'s "AT MOST TWO acquisitions per delivered chunk" claim, which the reap must not break.
- `internal/attachments/registry.go` → `Lookup`, `count`, `lastChunkAt`, `Release` — the four methods that must **not** reap, and why. Extract: `Lookup`'s "PURE READ", and that `count` and `lastChunkAt` are how the tests observe the reap.
- `internal/attachments/registry.go` → `entry` — the `lastChunkAt` field the reap reads, and the naming argument that guards it.
- `internal/attachments/registry.go` → `newRegistryWithClock` — the three constraints on `now`, and why the seam is unexported and nil-tolerant. The reap inherits all three.
- `internal/attachments/admission.go` → `maxInFlightUploads`, `maxUploadBytes` — the two neighbours the new constant sits beside, their unpublished-receiver-policy posture, and the resident-bytes arithmetic (`maxInFlightUploads × maxUploadBytes` = 64 MiB) the new constant time-bounds without changing.
- `internal/relay/v2session.go` → `idleTimeout` — the derivation anchor. Extract: the 15-minute figure, the "foregrounded-but-momentarily-quiet phone" argument, and why relay made it a `var` (test override, costing `t.Parallel()`) — which this package deliberately does not copy.
- `internal/attachments/accumulator.go` → `reject` — a doc-only edit here. Extract both sentences of the paragraph beginning "Dropping the map at the moment of refusal"; the second one's *claim* survives, only its attribution moves.
- `internal/attachments/registry_test.go` → `fakeClock`, `testClockStart`, `fillRegistry`, `boundChunk`, `withAttachmentID`, `testBoundTotal`, `testBoundFixtureDigest` — every test helper this slice needs already exists. Extract: `fillRegistry` takes the registry as a parameter, so it composes with a clock-injected one.
- `internal/attachments/registry_test.go` → `TestRegistry_AdmitStampsTheClockReading`, `TestRegistry_LookupAndRepeatAdmitLeaveTheStampWhereItIs`, `TestRegistry_DeliverMovesTheStampForwardForThatPairOnly` — the three existing clock tests. Verify (do not modify) that the reap leaves them green; see § Testing strategy.
- `docs/knowledge/features/attachments-package.md` § the upload-registry material — background on the family. **Read-only: the documentation phase owns this file, and its own `#1742` claims are explicitly out of scope for you.**
- `docs/protocol-mobile.md` § Attachments — the "receiver-configured and unpublished — a client learns them by being rejected" sentence the new constant inherits its posture from. No edit here either; the window stays off the wire.

## Context

`Registry` gives an in-flight upload's slot back on every terminal outcome — completion, an `Add` refusal, an integrity mismatch (#1784). The one ending it does not cover is the upload that never ends. A client that admits a transfer and then stops sending holds its entry, its retained bytes, and one of `maxInFlightUploads` slots forever.

That is a memory- and capacity-exhaustion path open to an authenticated but untrusted client. Four transfers that each send one chunk and go silent pin up to `maxInFlightUploads × maxUploadBytes` = 64 MiB resident, and — worse — every other upload on the daemon is then refused `ErrTooManyUploads`, a sentinel whose entire published contract is that it is *transient* and clears by itself as other uploads finish. Without expiry it never clears and the sentinel lies.

#1880 landed the bookkeeping: `entry` holds each upload's `lastChunkAt`, stamped at admission in `insertLocked` behind both refusal branches and moved forward by every delivered chunk in `lookupAndStamp`, with `Lookup` staying a pure read. Nothing reads that stamp as policy. This slice is the policy — the window, and the reaping.

The two neighbouring cases are placed elsewhere and stay there. Releasing a dropped conn's uploads is **#1817**. The post-restart case is already pinned at this layer: a chunk for a pair the registry does not hold answers `ErrUnknownUpload` and creates nothing, which `TestRegistry_DeliverUnheldPair_RefusesAndCreatesNothing` asserts.

**No ADR is warranted.** Every decision below is local to one package and is stated at the symbol that carries it; there is no cross-cutting choice for the documentation phase to record separately.

## Design

### The shape: a lazy reap, not a sweeper goroutine

**Decision: lazy.** The reap runs inline at the head of the two locked bodies that gate on the map's contents, and this package spawns nothing.

The eager alternative — a background sweeper — is refused for four reasons, in descending weight:

1. **It would invent a lifecycle nothing here has.** This package owns no goroutine and has no shutdown path, and `NewRegistry` has *no production caller at all* today; its only callers are this package's tests. An eager sweeper needs `Start`/`Stop` or a `context.Context`, wired by a composition root that does not yet construct a `Registry`. That is dead lifecycle code with no owner.
2. **It would need a different, larger seam.** The clock #1880 landed is a `func() time.Time`, which a sweeper cannot block on. A sweeper needs a ticker or a `<-chan time.Time` injected instead — a wholly new seam — and AC 5 ("the tests advance time rather than sleeping") is satisfiable against a `func() time.Time` and awkward against a ticker.
3. **Nothing needs the promptness.** A slot is only *interesting* at the moment someone wants it, and both moments — an admission that may be at capacity, and a chunk that may be resuming a dead transfer — run through a locked body the reap can sit in front of. A slot reclaimed a millisecond before nobody asks for it buys nothing.
4. **The sweep is trivially cheap.** `maxInFlightUploads` is 4, so a full pass is at most four `time.Time` comparisons under a mutex already held. That is also why no per-entry data structure — a heap keyed on `lastChunkAt`, a sorted list, an expiry index — is warranted: it would be more state to keep consistent than the thing it indexes.

### The constant

Add to `admission.go`, beside its two neighbours:

```go
const uploadIdleTimeout = 15 * time.Minute
```

**`const`, not `var`.** Relay makes its `idleTimeout` a package `var` purely so its own tests can substitute a sub-second value, and pays for that with tests that cannot call `t.Parallel()`. This package has #1880's clock seam instead: a test advances a fake clock rather than shrinking the bound, so every test below keeps `t.Parallel()`. Do not make this a `var`, and do not add a test-only override.

**Unexported**, for `maxInFlightUploads`' own stated reason: it is receiver policy that `docs/protocol-mobile.md` § Attachments deliberately leaves unpublished, so "a client learns them by being rejected". Nothing about the window goes on the wire, and no new wire code is introduced — an expired upload's next chunk is refused with the existing `ErrUnknownUpload`.

**Typed** (`time.Duration`), unlike its two untyped neighbours. Their untypedness is stated at each as serving readers that want different types (`int64` in comparisons, `int` in a `make`); this one has exactly one reader and one type.

The doc comment must state, and must not merely assert:

- **What it inherits.** The number comes from `internal/relay`'s `idleTimeout` rather than being re-derived, the same way `maxInFlightUploads` inherits its arithmetic from `maxUploadBytes` rather than restating it. Relay's derivation — long enough not to tear down a foregrounded-but-momentarily-quiet phone mid-read, short enough that a silently-gone phone's state does not linger — is a statement about the same client population sending these same chunks, so restating it here would be a second copy free to drift.
- **Why the two bounds are not redundant.** A client that keeps its session noisy while abandoning one upload never trips relay's bound. That bound measures silence on the *session*; this one measures silence on the *transfer*. Neither implies the other.
- **Why equality is the right pick given they are independent.** Shorter would reap a transfer whose session relay still considers live and could still resume. Longer would let an abandoned upload outlive the transport session that carried its chunks — relay tears that session down at its own 15 minutes — so the extra time is dead time in which no chunk can arrive anyway. Equal is the value at which this window neither pre-empts a live session nor outlasts a dead one.
- **What it does and does not bound, stated as a residual rather than as a win.** It does not change the resident-bytes product `maxUploadBytes`' doc fixes at 64 MiB; it bounds how long that worst case can persist *under silence*. It does **not** bound a client that trickles: one chunk per window against a 373-chunk declaration holds a slot for hundreds of hours, and four such conns hold the whole product. Closing that would need a cap on a transfer's *total* lifetime measured from admission — a different policy from this one, and one AC 3 explicitly forbids folding in here, since this window is defined to run from the last chunk and not from admission. Say so plainly in the doc: this constant closes the silent-abandonment path and leaves the slow-drip path open. It is a tolerable residual because the sender is a paired device inside the user's own trust domain (`docs/protocol-mobile.md` § Security model), which is the same reason `maxInFlightUploads`' doc already accepts one conn starving its peers — not because trickling is benign.
- **That it does not subsume #1817.** This window releases a window after the last chunk, whether or not the conn is alive. #1817 releases at the drop. Today, with #1817 unlanded, this window happens to also close the dropped-conn case at a 15-minute latency — that is a side effect, not a replacement, and must not be written as one.
- **That no timer exists.** The release is lazy; see § The reap. A reader who sees `Timeout` will look for a timer, and the doc must tell them there is none.

### The reap

One new unexported method on `Registry`. Contract:

```go
// Caller MUST hold r.mu. Deletes every entry idle longer than
// uploadIdleTimeout, measured against ONE reading of r.now(). Deletes
// nothing else. Calls nothing on Accumulator.
func (r *Registry) reapExpiredLocked()
```

Body: read `r.now()` **once** into a local, then a single `range` over `r.uploads` deleting each key whose entry is expired. Deleting from a Go map during a `range` over it is defined behaviour — an entry deleted before the iteration reaches it is simply not produced — so one pass suffices and no key-collection slice is needed.

Five properties the doc must carry, each of which a reader would otherwise have to re-derive:

- **`Locked` suffix, caller holds `mu`.** Same convention as `insertLocked` and sessions' `saveLocked`. It takes no lock itself, which is what keeps `mu` a leaf while two locked bodies share it. It is the **third** method in this type that takes no lock, joining `insertLocked` (caller-holds) and `Deliver` (composes two locked methods off-lock) — see § Doc rewrites, which has to fix a "TWO methods" count in the type doc.
- **One clock reading for the whole pass, not one per entry.** Two reasons. It keeps the reap to a single call out to the caller-supplied `now` under `mu`, which the type doc's clock paragraph accounts for. And it makes the sweep evaluate every entry against one instant, so its outcome cannot depend on Go's randomized map iteration order — a per-entry read against an advancing clock could reap entry A and spare entry B on one iteration order and the reverse on another.
- **It calls nothing on `Accumulator`. In particular, never `reject`.** Dropping the map entry drops the registry's last reference and the bytes are collectable; `reject` is an *unlocked* mutator (`a.chunks = nil`) on a type that carries no mutex by design. The reap holds `mu`, but `mu` does not cover an accumulator — a pointer to it may already be held off-lock by a goroutine that got it from `Admit` or `Lookup`, so a `reject` from here is a data race, not merely a leaf-breach. The prohibition is the same one `Deliver`'s doc states from the other side ("`mu` is never held across a call into `Accumulator` — not `Add`, not `Assemble`") and this method must not be the first to break it.
- **It calls nothing on `Registry` either, `Release` included.** `Release` takes `mu` and `sync.Mutex` is not reentrant, so building the reap on it deadlocks rather than races. The reap `delete`s directly, exactly as `Release` does under its own acquisition.
- **It is silent, and that is deliberate.** The obvious instinct is to log what was reaped, and the map key *is* the client-chosen `attachment_id` — one of the four strings `protocol.AttachmentChunkPayload`'s SECURITY block bans from ever reaching a log or an error string. There is no format string in this body and no logger, which keeps that rule structural here rather than a discipline. The reap also returns nothing: nothing needs a count, and a return value would invite a caller to branch on it.

**The comparison is strict.** An entry is expired iff `now.Sub(e.lastChunkAt) > uploadIdleTimeout`. At *exactly* the window the upload is still in flight — a bound on how long silence may last, not on how long it may be approached, which is the polarity `CheckDeclaredSize` already argues for its own `>`. State it at the comparison; § Testing strategy pins it.

### Where the reap runs — and where it must not

**Two call sites, both at the head of the body, both before the map is read.**

| Site | Why here | What it satisfies |
|---|---|---|
| `insertLocked`, before the incumbent look-up | It is the shared core `Admit` and `insert` both run, so one placement covers both callers with one reap per call — the same argument the `maxInFlightUploads` gate made for living here. It must be **ahead of** the capacity gate, which is in this body and reads `len(r.uploads)`. | AC 2 |
| `lookupAndStamp`, before the map read | It must be **ahead of** the look-up: a reap that ran after would find the expired entry, stamp it forward and hand its accumulator back — resuming exactly what the window exists to end. | AC 1 |

**Not in `Admit`.** `Admit` holds `mu` across `insertLocked`, so a reap in both runs twice per `Admit`. `insertLocked` is the correct one of the two, because the thing the reap feeds — the capacity gate — is there.

**Not in `Lookup`.** Its doc calls it a PURE READ and #1744's dispatch site reads through it. Leave the polarity: a look-up that reaps is a look-up with a side effect, and the doc argument that keeps `Lookup` from *stamping* is the same shape. The consequence — `Lookup` may report an entry the next `Admit` or `Deliver` will reap — is real and goes in its doc; it is consistent with the package's stated "MEMBERSHIP CERTIFIES NOTHING" posture, and the authoritative answer to "may this chunk resume" is `Deliver`'s.

**Not in `count` or `lastChunkAt`.** These two are how the tests observe the reap, and a reader that reaps is a reader causing what it reports. This is the trap most likely to bite: **a test that advances the clock and then asserts `count()` has dropped will fail**, because nothing has driven a reap. The fix is to drive `Admit` or `Deliver` first and read `count()` after — never to make `count` reap.

**Not in `Release`.** It gates nothing, and `Deliver` calls it on three of four outcomes, so a sweep there is per-chunk cost for no decision.

### Consequences worth stating in the code

- **A repeat first chunk under an expired pair is a FRESH admission**, because the reap at `insertLocked`'s head runs ahead of the incumbent look-up and the incumbent is gone by the time it looks. That is the right semantics: the expired transfer is over, and it agrees with `Deliver`'s existing "A RE-ADMITTED PAIR IS A NEW TRANSFER" paragraph. Reaping *after* the incumbent look-up would instead hand back a dead accumulator whose every subsequent chunk `Add` refuses — strictly worse for the client and for the slot.
- **`Deliver`'s acquisition count is unchanged.** The reap runs *inside* the acquisition `lookupAndStamp` already takes, so "AT MOST TWO acquisitions per delivered chunk" stays true. Do not add a third by reaping from `Deliver`'s own body.
- **At capacity with an expired entry and an invalid declaration, the client still gets the declaration sentinel.** `Admit` runs both declaration checks before `insertLocked`, so the reap does not disturb the one gate ordering this package makes a contract, and `TestRegistry_AdmitAtTheBound_AnswersTheDeclarationSentinelFirst` stays green.

## Doc rewrites — in scope, and there are eleven, not seven

The ticket body was filed naming four falsified doc claims, then corrected to seven. Re-derived against the merged tree the count is **eleven sentences across nine sites**: the ticket's seven, plus four *enumeration* claims — sentences that count or list things and that adding a method or a clock-read site silently makes wrong. Rewrite all of them. Each rewritten sentence must state what is true of the landed code; do not name a successor ticket by reflex, and do not delete a claim that merely changed tense.

**The ticket's seven.**

1. `registry.go` → `Deliver`'s closing paragraph ("Expiry of a stalled upload is #1742's…"). Both halves of the race it describes stay safe, so only the framing moves: an entry *can* now vanish between this method's look-up and its release — reaped by `lookupAndStamp`'s own head, or by a concurrent `Admit` — and `Release` on a pair no longer held is a documented no-op while a look-up that misses answers `ErrUnknownUpload`. Drop "until it lands".
2. `registry.go` → `Release`'s doc ("#1742 decides only when to call it"). Re-point, do not delete: **#1817** is the live successor for that clause, since the reap does *not* call `Release` (it cannot — `mu` is not reentrant) and deletes directly instead. Say that, so a reader does not expect the reaper to be built on this method.
3. `admission.go` → `maxInFlightUploads`' doc ("#1742's reaper reclaims abandoned slots"). Present tense, naming `uploadIdleTimeout` rather than a ticket. This is the right place to state the two constants' relationship: this one bounds how many slots exist, that one bounds how long one may be held without progress.
4. `accumulator.go` → `reject`'s doc. **First sentence:** "until #1742's reaper runs" → the landed reap, in the present. Its *claim* survives and is still reachable — through `Deliver` no entry outlives a refusal, but `Admit` hands the accumulator back off-lock and a caller that feeds it directly and then stops does park held bytes behind a poisoned accumulator until the reap. **Second sentence:** its claim survives verbatim (a complete-but-corrupt transfer is not a partial upload, `Deliver` releases it on the mismatch, and this line is still the only thing that frees the bytes); only its "#1742 does not cover it" attribution moves to the landed reap. This is the whole of `accumulator.go`'s change — no code.
5. `registry.go` → `entry`'s doc ("an idle window (#1881) exists to close"). Tense only. The hazard it names — a stamping look-up would defeat the window — is made *more* live by landing, not less. Do not weaken the argument.
6. `registry.go` → `Lookup`'s doc ("an idle window (#1881) exists to close"). Same tense fix, **plus** the new consequence: this method does not reap, so a `true` answer may name an entry the next `Admit` or `Deliver` will reap.
7. `registry.go` → `lastChunkAt`'s doc ("shaped for a single-pair read rather than for whatever an idle sweep (#1881) turns out to want"). Decided by landing: the sweep reads `r.uploads` directly under `mu`, exactly as this accessor does, and shares nothing with it. Add that this accessor deliberately does **not** reap, for the trap reason above.

**The four enumeration claims the ticket did not count.**

8. `registry.go` → the `Registry` type doc: "TWO methods here take no lock, for OPPOSITE reasons." It is now **three** — `insertLocked` (caller-holds), `reapExpiredLocked` (caller-holds), `Deliver` (takes it at no point). The "opposite reasons" framing needs the new member folded in, not appended.
9. `registry.go` → the `Registry` type doc, same comment: "it is read WITH `mu` HELD — at the admission store in `insertLocked` and at the delivery stamp in `lookupAndStamp`". That enumeration of clock-read sites is now incomplete; the reap reads it in each of those two bodies as well. The claim it supports — reading and the store it lands in are one critical section — is unaffected.
10. `registry.go` → `Admit`'s doc: "THE WHOLE DECISION RUNS UNDER ONE ACQUISITION of `mu` — declaration verdict, incumbent look-up, capacity, construction, store". The reap joins that list, between construction and the incumbent look-up as the body runs it. A list presented as the whole decision must stay complete.
11. `registry.go` → `count`'s doc: "how many uploads are in flight, which is exactly the set of live admitted uploads". After this window those two diverge — an expired-but-unreaped entry is still counted, because this method does not reap. State that, and state why it is the right trade: this accessor is the tests' observer of the reap, so it must report what the map holds rather than cause the change it is measuring.

`docs/knowledge/features/attachments-package.md` carries the same `#1742` claims and is **out of scope** — the documentation phase owns that file. Do not edit it, and do not add a knowledge-doc deliverable.

## Concurrency model

No goroutine is created, none is joined, and there is no shutdown path — that is the whole point of choosing lazy over eager. The concurrency work is entirely about not breaking the lock contract `Registry`'s type doc already states.

- **`mu` stays a LEAF.** `reapExpiredLocked` takes no lock, calls no method that takes one, and calls nothing on `Accumulator`. Its only outward call is `r.now()`, which `newRegistryWithClock` already documents as constrained to be non-blocking, non-reentrant into the `Registry`, and safe for concurrent use.
- **Lock order is unchanged**: `Registry.mu` → whatever the clock locks, never the reverse. It is a total order only because the clock never calls back in, which is stated at `newRegistryWithClock` and inherited here rather than re-argued.
- **Acquisition counts are unchanged.** The reap adds no acquisition at either site; it runs inside one the host body already holds. `Admit` remains one, `Deliver` remains at most two.
- **The reaped accumulator may still be reachable off-lock**, by a goroutine holding a pointer from an earlier `Admit` or `Lookup`. That is why the reap must not touch it — see § The reap. Dropping the map entry is the entirety of what the registry may do to a reaped upload.
- **The reap and the capacity gate are one critical section, and that is a security property rather than a convenience.** A reap under one acquisition of `mu` followed by a capacity check under another is precisely the split-lock shape `TestRegistry_ConcurrentAdmitAtTheBound_AdmitsExactlyTheFreeSlots` exists to redden: two concurrent admissions could each observe the same reclaimed slot and both take it, putting the registry over `maxInFlightUploads` and past the resident-bytes product that bound exists to hold. Placing the reap at `insertLocked`'s head — inside the acquisition the gate already runs in — is what forecloses that. Do not "tidy" it into a helper that takes `mu` itself.

- **A transfer can be reaped mid-`Deliver`, and that is benign in the one direction it can go.** Goroutine 1 sits between `lookupAndStamp` and `Release`, feeding the accumulator off-lock; goroutine 2's `Admit` reaps its entry. Goroutine 1 then holds a pointer the registry no longer knows about: its `Assemble` still runs and, if the transfer completes, still returns *integrity-verified* bytes, and its `Release` is a no-op on an already-absent key. Nothing double-frees and no unverified bytes escape. What makes it safe is that the reap can only **remove** — it never replaces an entry, never writes one, and never touches an accumulator.

- **That safety rests on the one-goroutine-per-conn precondition, which the reap makes one step longer to see.** Goroutine 1's late `Release` deletes *by key*; if the same pair had been re-admitted in the gap, it would delete the successor's entry. That is unreachable because the conn is in the key and relay spawns exactly one `appFrameWorker` per session, so no second goroutine handles that conn's frames — the same precondition `Admit` and `Deliver` already hand their accumulators back under. The reap does not weaken it, but it does introduce a *third party* (another conn's `Admit`) that can now remove your entry, where before only your own conn could. **A future ticket that widens the one-worker-per-conn assumption must revisit this**, and the invariant should be stated at `reapExpiredLocked` so that reader finds it.

- **`Deliver`'s look-up/release gap is still not a TOCTOU hole**, and now for a second reason as well as the first. The first stands, per the precondition above. The second is that the reap can only *remove*, and both consequences of removal on that path are already the documented answers — `Release` on an unheld pair is a no-op, and a look-up that misses answers `ErrUnknownUpload`.

- **Re-admission after a reap opens no new contamination path.** A straggler from a reaped transfer can land in a successor's accumulator, but only within one conn (the conn is in the key), and `Assemble` compares the declared size and digest against the *whole* assembled slice — so the only reachable outcome is that client's own new transfer failing its own integrity check. That is verbatim the case `Deliver`'s "A RE-ADMITTED PAIR IS A NEW TRANSFER" paragraph already covers; the reap adds a new way to *reach* it, not a new outcome.

## Error handling

**No new sentinel, no new wire code, no new error path.** An expired upload's next chunk is refused with the existing `ErrUnknownUpload`, whose doc already covers this case in so many words: "a chunk for a conn-and-`attachment_id` pair the registry does not hold — never admitted, **or admitted and already over**." A reaped upload is over. Deciding which wire code it maps to remains #1744's.

Do not introduce an `ErrUploadExpired` or similar. Three reasons, the last of which is the load-bearing one:

- It would tell a client something it cannot act on differently. The repair for both is identical: start the transfer again.
- It would publish the window's existence, which § The constant deliberately keeps unpublished.
- **It would be an oracle.** Reusing `ErrUnknownUpload` makes "reaped" and "never admitted" answer with the same bare sentinel and the same message, so a client cannot probe for the window's length by timing a chunk against a pair it never admitted. A distinct sentinel would let a caller binary-search `uploadIdleTimeout` from outside, which is exactly the receiver policy the unpublished posture withholds. This is a reason the reuse must be preserved, not merely a reason it is acceptable.

`reapExpiredLocked` returns no error and cannot fail: `delete` on a map cannot fail, and there is no I/O, no allocation and no call-out but the clock.

The one failure mode worth naming is a **clock that goes backwards**. A wall clock stepped backwards by NTP makes `now.Sub(lastChunkAt)` negative, which the strict `>` reads as not-expired — an upload survives a window it should have been reaped in, and is reaped on a later pass once the clock has caught up. That is the safe direction (a live-looking dead entry, bounded by the step, not a reaped live one) and needs no defence: no such failure has been observed, and a monotonic-clock defence would mean storing `time.Time` readings this package already round-trips per `docs/PROJECT-MEMORY.md` § "`time.Time` round-trip discipline". Name it in the constant's doc; do not code against it.

## Testing strategy

All in `registry_test.go`, all with `t.Parallel()`, all driving `newRegistryWithClock(clk.now)` with an existing `fakeClock` at `testClockStart`. **No test may sleep and no test may shrink the bound** — the clock is the only lever (AC 5), and it is why the constant stays a `const`. Time comparisons use `Equal`, never `==` or `reflect.DeepEqual`.

Four new test functions, as scenarios:

- **An idle upload's next chunk is refused as unknown (AC 1, plus the boundary).** Admit a bound-declaration transfer. Advance *exactly* `uploadIdleTimeout` and deliver a chunk — it must still be routed (`ErrIncomplete`), proving the comparison is strict at the boundary. Then advance past the window with no delivery and deliver again — `ErrUnknownUpload`, no bytes returned, and the pair absent from `Lookup`. Two mutants this kills: a reap placed *after* `lookupAndStamp`'s map read (which resumes the transfer instead), and `>=` in place of `>` (which reddens the boundary arm alone).
- **Expiry gives the slot back (AC 2).** `fillRegistry` to `maxInFlightUploads`, confirm a new pair is refused wrapping `ErrTooManyUploads`, advance past the window, then admit that same new pair — it must succeed, and `count()` read *after* that admission must show the registry holding only what survived plus the newcomer. Read `count()` after driving `Admit`, never before: `count` does not reap. This is the test that fails if the reap sits in `Admit` instead of ahead of `insertLocked`'s capacity gate.
- **The window runs from the last chunk, not from admission (AC 3).** Admit; advance well inside the window and deliver a chunk (`ErrIncomplete` keeps the entry); advance again by an amount that puts total elapsed *since admission* past `uploadIdleTimeout` while leaving elapsed *since the last chunk* inside it; deliver once more. It must be routed, not refused. Kills a reap that compares against an admission-time field, and a reap on a registry whose deliveries never stamped.
- **The reap is selective (AC 4).** Two pairs — the same `attachment_id` on `testConnA` and `testConnB`, so the claim is about the key and not merely about two unrelated entries. Keep one fed inside the window while the other goes silent past it, then drive a reap and assert the fed pair is still present *with its stamp intact* via `lastChunkAt`, the silent one absent, and `count()` down by exactly one. Kills a reap that `clear`s the map or deletes on the wrong side of the comparison.

**Three existing tests must stay green unmodified** — verify, do not edit:

- `TestRegistry_AdmitStampsTheClockReading` advances an hour with nothing delivered and then reads through `lastChunkAt`, expecting the entry present. It stays green **only because `lastChunkAt` does not reap**. If you find yourself making it reap, this test is the alarm.
- `TestRegistry_LookupAndRepeatAdmitLeaveTheStampWhereItIs` advances two minutes total and does a repeat `Admit`, expecting the incumbent. Two minutes is inside the window, so the reap at `insertLocked`'s head does not fire and the incumbent is answered as before.
- `TestRegistry_DeliverMovesTheStampForwardForThatPairOnly` advances one minute — inside the window.

Everything else in the file uses `NewRegistry()` on the real wall clock and completes in milliseconds, so nothing expires. The concurrency tests are unaffected.

Gate: `make check`. This slice adds no live-claude surface, so `make preship` is not required — but note that it deletes no test file either, which is the condition that would have forced it.

## Open questions

None blocking. Two things deliberately left where they are:

- **Whether `Lookup` should eventually reap** is #1744's to decide when it wires the dispatch site, if it turns out to want an authoritative presence answer. It does not need one today: the two paths that act on presence — `Admit` and `Deliver` — both reap.
- **A per-conn sub-cap under `maxInFlightUploads`**, which would bound one conn's ability to occupy every slot, remains the later refinement `maxInFlightUploads`' doc already names. This window shortens how long such an occupation can last without effort; it does not prevent one.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The reap's inputs are `entry.lastChunkAt` (daemon-authored — a clock reading, never client-supplied) and the map keys. It parses nothing, formats nothing and dispatches on nothing; the only client-influenced value in its reach is `attachment_id`, which `uploadKey`'s doc already establishes is not a capability. The one client-controlled input is *timing* — when a chunk arrives moves the stamp — and that is the intended control surface, not a boundary violation.
- **[Trust boundaries]** SHOULD FIX, applied to the spec. The first draft called a trickling client "the bound working as designed", which understated a residual: one chunk per window against a 373-chunk declaration holds a slot for hundreds of hours, and the window does not close that. § The constant now states the residual plainly, names what would close it (a total-lifetime cap measured from admission), and records that AC 3 forbids folding that in here. Severity is low because the sender is a paired device inside the user's own trust domain per `docs/protocol-mobile.md` § Security model — the same basis on which `maxInFlightUploads`' doc already accepts one conn starving its peers. **No ticket exists for a total-lifetime cap**; it is named here rather than assigned.
- **[Tokens, secrets, credentials]** Not applicable, by design rather than by absence. Nothing is generated, stored, rotated, revoked or compared. The declared sha256 is copied uninspected and compared only by `Assemble`, which the reap never calls.
- **[File operations]** Not applicable, and it is a property rather than an omission: an expired upload's bytes exist only in its `Accumulator`'s chunk map and were never written to disk, so expiry cannot leave a partial file, a stale temp file or a traversal opportunity. `storage.go`'s `EnsureDir` / `SanitizeFilename` leg is not on this path.
- **[Subprocess / external command execution]** Not applicable. No `exec`, no environment read, no signal.
- **[Cryptographic primitives]** No findings. No RNG, no key, no nonce, no constant-time comparison in scope. The adversarial question asked and answered: reaping cannot cause unverified bytes to be accepted, because the reap only removes and nothing assembles a removed entry — see § Concurrency model's mid-`Deliver` case, where a transfer reaped in flight still returns integrity-verified bytes or none.
- **[Network & I/O]** No findings; this ticket *is* the resource-exhaustion fix. The reap is not itself an amplification vector: its cost is bounded by `maxInFlightUploads` (4 comparisons), a constant no client controls, run at most once per `Admit` and once per delivered chunk — negligible beside the base64 decode and `Add` already on that path. No socket, no server, no timeout surface is touched.
- **[Error messages, logs, telemetry]** SHOULD FIX, applied to the spec. The reap is silent and constructs no error, which § The reap justifies structurally (the map key is `attachment_id`, one of the four strings `protocol.AttachmentChunkPayload`'s SECURITY block bans from logs and error strings; there is no format string and no logger in the body). The pass added a stronger reason to reuse `ErrUnknownUpload` than the ergonomic one first written: a distinct `ErrUploadExpired` would be an **oracle**, letting a client binary-search `uploadIdleTimeout` from outside and defeating the unpublished-receiver-policy posture. § Error handling now carries it as a preservation requirement rather than a preference.
- **[Concurrency]** SHOULD FIX, applied to the spec — the highest-risk category here, and the one that produced the most. Three additions to § Concurrency model: (a) the reap sharing `insertLocked`'s single acquisition with the capacity gate is a **security property**, since a split acquisition would let two concurrent admissions both claim one reclaimed slot and put the registry over `maxInFlightUploads` — the exact shape `TestRegistry_ConcurrentAdmitAtTheBound_AdmitsExactlyTheFreeSlots` reddens; (b) the mid-`Deliver` reap case is enumerated and shown benign, resting on the reap being remove-only; (c) the surviving dependence on relay's one-`appFrameWorker`-per-conn precondition is stated explicitly, because the reap introduces a *third party* that can remove an entry where previously only the owning conn could — flagged so a future ticket widening that assumption revisits this. Lock order, leaf-ness, non-reentrancy and the no-goroutine decision were already covered; no goroutine is spawned, so there is no lifecycle or leak to audit.
- **[Threat model alignment]** No findings. The relevant threat is resource exhaustion by an authenticated paired device, which `docs/protocol-mobile.md` § Security model places inside one trust domain — so the realistic case this closes is a phone that drops or a client that stops, and the hostile-paired-device case is the lower-severity residual recorded above. Out-of-scope threats are named with owners: releasing a dropped conn's uploads is **#1817**; a per-conn sub-cap under `maxInFlightUploads` is the later refinement that constant's doc already names; a total-transfer-lifetime cap has **no ticket** and is named unassigned rather than invented here.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-31
