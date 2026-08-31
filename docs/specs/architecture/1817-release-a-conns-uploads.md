# #1817 — release every in-flight upload held by one conn

**Package:** `internal/attachments` · **Size:** S · **Split from:** #1742
**Ships:** the primitive only. Placing the call is **#1744**'s (this package's first production caller).

## Files to read first

Turn-1 data load. Read these symbols, not whole files.

- `internal/attachments/registry.go` → `Release` — the one-pair sibling this method is modelled on: what it removes, why it returns nothing, and the "#1817 decides only when to call it" clause that this slice must re-point.
- `internal/attachments/registry.go` → `reapExpiredLocked` — **the shape to copy.** Range-and-delete under `mu` in one pass; and four rules stated there that this method inherits verbatim: it calls nothing on `Accumulator` (never `reject`), it calls nothing on `Registry` (`Release` included), it is silent, and its "REMOVE-ONLY IS WHAT MAKES A MID-DELIVER REAP BENIGN" paragraph is the concurrency argument this method rests on.
- `internal/attachments/registry.go` → `Registry` (type doc) — the `mu`-is-a-leaf paragraph and its **"the seven locked methods"** roster, which this slice makes eight. The rule itself does not change.
- `internal/attachments/registry.go` → `insertLocked` — where the `maxInFlightUploads` gate reads `len(r.uploads)`. AC 4's "the freed slots are usable" is a statement about this gate.
- `internal/attachments/admission.go` → `maxInFlightUploads`, `uploadIdleTimeout` — the bound (4) this sweep's cost is bounded by, and the window the no-reap decision is measured against. Also `uploadIdleTimeout`'s "IT DOES NOT SUBSUME #1817" paragraph, which is a **do-not-touch** (see § Cites).
- `internal/attachments/registry_test.go` → `fillRegistry`, `fakeClock`, `testClockStart`, `testConnA`, `testConnB`, `testAttachmentID` — the fixtures every new test reuses. Note `fillRegistry` admits **only under `testConnA`**; AC 4 needs a two-conn fill, so admit directly there rather than widening the helper.
- `internal/attachments/registry_test.go` → `TestRegistry_SameAttachmentIDOnTwoConns_AreSeparateEntries` — the pointer-identity idiom AC 2 reuses, and the existing statement of the same-id-on-two-conns property.
- `internal/attachments/registry_test.go` → `TestRegistry_AdmitAtTheBound_ReleaseReturnsTheSlot` — AC 4's one-pair ancestor; this ticket's AC 4 is that test widened to a conn.
- `internal/attachments/registry_test.go` → `TestRegistry_ConcurrentMixedOperations_AreSafe` — extended by this slice (§ Testing strategy, item 5). Read the workers' post-insert `Lookup` presence assertion before editing; it is why the new sweeper must not target `testConnA`.
- `docs/knowledge/features/attachments-package.md` § "In-flight upload registry" and § "Mutation-testing lessons" — this package measures sole-redness with `go test -overlay` and has recorded traps (unfiltered runs, `grep -a`, `build failed` / `declared and not used`). § Mutants below depends on them.
- `internal/relay/v2session.go` → `closeWith` — read only to see the teardown cluster this primitive is built for and the `pushMu` critical section inside it. **Nothing in this slice edits this file.**

## Context

`Release` removes exactly one `{connID, attachment_id}` pair. A conn that drops mid-upload may hold several — `maxInFlightUploads` is a registry-wide bound and nothing restricts one client to a single attachment below it — and a teardown path holds the conn, never a list of the attachment_ids that conn admitted. Composing `Release` at the call site would require the caller to keep a shadow list of what it admitted, which is this type's own bookkeeping leaking out. So the way in must be keyed on the conn alone.

Cross-conn isolation is the security property, not a convenience. `attachment_id` is client-chosen and `docs/protocol-mobile.md` § Attachments states it is "**Not a capability** — not secret, not unguessable", so two conns can be mid-transfer on the same id at the same moment. Releasing one must leave the other's transfer of that id untouched — which is what makes a conn-scoped predicate load-bearing rather than a tidier way to write "empty the map at teardown".

This slice ships the primitive with no production caller, the same way #1788 shipped `Admit` and #1784 shipped `Deliver`. The registry is still unreachable from production until #1744 wires the dispatch site.

**No ADR.** This adds one method to an existing type under rules that type's own doc already states; there is no decision here that outlives the package overview.

## Design

One new exported method on `Registry`, in `registry.go`, beside `Release`.

```go
// ReleaseConn removes every upload the conn holds and nothing else.
func (r *Registry) ReleaseConn(connID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.uploads {
		if key.connID == connID {
			delete(r.uploads, key)
		}
	}
}
```

That body is the whole of the design. What follows are the decisions behind it, each of which belongs in the doc comment in this package's house style.

**Name.** `ReleaseConn` — the custodial register the type already uses (`Admit`, `Lookup`, `Release`, `Deliver`) plus the axis it sweeps on. Not `ReleaseAll`, which reads as "release everything" and is the one thing it must not do; not `Drop`/`Evict`, which would open a second verb family for the same act.

**It takes `mu` itself and deletes directly. It must not compose `Release`.** `sync.Mutex` is not reentrant, so a per-pair loop calling the lock-taking `Release` deadlocks rather than races — and unlike a race, it is not something `-race` reports. This is the rule `reapExpiredLocked`'s doc already records from the other side ("IT CALLS NOTHING ON `Registry` EITHER, `Release` included"); state it here too, since a reader arriving at a conn-wide release is exactly the reader who reaches for the one-pair one.

**No `releaseConnLocked` core.** `insertLocked` and `reapExpiredLocked` are lock-free cores because *two* bodies share each. Nothing shares this one. A `Locked` variant with a single caller would publish a seam no one asked for and would be the third member of a set whose type doc names all three and their opposite reasons.

**It does not reap.** This is the open question the ticket asked to be decided rather than inherited, and the decision is **no**.

- The reap exists to protect a *decision*. `insertLocked` reaps because the capacity gate it is about to run must not count dead entries; `lookupAndStamp` reaps because a stamp-and-resume must not revive an expired transfer. `ReleaseConn` decides nothing — it removes unconditionally — so there is no verdict for a stale entry to corrupt.
- `Release` and `Lookup` set the precedent from the other side: a body that only removes, or only reads, does not reap.
- Nothing is lost. An expired entry `ReleaseConn` walks past is taken by the very next `Admit` or `Deliver`, which are the only two bodies whose correctness depends on it being gone.
- It keeps the clock out of this body. A reap here would be a second call out to the caller-supplied `now` under `mu` in a method that otherwise needs no clock at all.
- And it is what makes AC 4's attribution measurable: under a clock that never advances, no reap can fire anywhere, so a slot that frees up can only have been freed by the release. Test 3 (§ Testing strategy) is the sole red for the mutant that adds the reap.

**It returns nothing.** `Release`'s recorded reason applies unchanged: a presence answer invites a caller to branch on it outside the lock, where it is already stale. A *count* would be injection-free (an `int` cannot carry an `attachment_id`), so the no-return decision here is the branch-outside-the-lock one and not a privacy one — say which argument does the work. If #1744 later wants a released-count for an operator log line, that is the ticket that adds it, when it has a caller asking; adding it now would be speculative.

**It is silent.** No logger, no format string. The map key holds the client-chosen `attachment_id`, one of the four strings `protocol.AttachmentChunkPayload`'s SECURITY block bans from a log or an error string, so the rule stays structural here the way it does in `reapExpiredLocked`. This method is *not* exposed to the unpinned-claim gap code review found on `Deliver` (recorded in the package overview): `Deliver` returns errors a mutant can interpolate into, this one returns nothing at all, so there is no format string a fixture could put back.

**It calls nothing on `Accumulator`, and never `reject`.** Dropping the map entry drops the registry's last reference and the bytes become collectable. `reject` is an unlocked mutator on a type that carries no mutex by design, and `mu` does not cover an accumulator — a pointer may already be held off-lock by a goroutine that took it from `Admit`, `Lookup` or `Deliver` — so a `reject` from here is a data race, not merely a leaf breach. Verbatim the rule `reapExpiredLocked` states; this method must not be the first to break it.

**One pass, no key-collection slice.** Deleting from a map while ranging over it is defined in Go: an entry deleted before the iteration reaches it is simply not produced. `reapExpiredLocked` is the precedent.

**The predicate is `key.connID == connID`, exact equality, and it reads only the conn half of the key.** No trimming, no case folding, no prefix match, no validation of `connID`. Two reasons, and both are load-bearing:

- A fold or prefix match would let one conn's teardown remove another conn's entries — the exact isolation AC 2 exists to defend, defeated in the predicate rather than at the key.
- **`ReleaseConn` must accept exactly what `Admit` accepts.** `Admit` stores the `connID` it was given, verbatim and unvalidated. A `ReleaseConn` that validated or normalised it would refuse to remove entries `Admit` had happily stored — a permanently unreleasable entry, which is a slot leak. Symmetry with the store is the requirement; validating the conn namespace is not this type's job (`connID` is the receiver's own name for the conn and never appears on the wire).

**The sweep is bounded by `maxInFlightUploads`.** It is O(len(uploads)) over the whole map, and #1796's gate makes "the registry never holds more than `maxInFlightUploads`" a property of the container rather than of one entry point — so the pass is at most four comparisons and `mu` is held for bounded work no matter how many entries the conn holds. This is a real dependency of this design on that gate, worth stating: a later ticket that lifts the cap lifts this sweep's cost under the only mutex with it.

### Cites this slice must fix (both in `registry.go`, both named by the ticket)

1. `Registry`'s type doc — "the **seven** locked methods — `Admit`, `insert`, `Lookup`, `lookupAndStamp`, `Release`, `count` and `lastChunkAt`" is wrong once `ReleaseConn` exists. Make it eight and add the name. **The rule itself does not change** — "no method takes `mu` and then calls another that takes it" was phrased to survive exactly this, and `ReleaseConn` satisfies it. The "THREE methods here take no lock" count is also unchanged: `ReleaseConn` takes `mu`.
2. `Release`'s doc — "#1817, which releases a dropped conn's uploads, decides only when to call it" is now false: this ticket does not call `Release`, it adds a sibling beside it. Re-point rather than delete. The replacement should say that the conn-wide release is `ReleaseConn`, that it deletes directly under its own acquisition for the same non-reentrancy reason the reap does rather than looping over this method, and that **#1744** is the caller that decides when either is called.

### Cites this slice must NOT touch

- `admission.go` → `uploadIdleTimeout`'s "IT DOES NOT SUBSUME #1817 … that it also happens to close the dropped-conn case today, at a 15-minute latency, is a side effect and not a replacement." Both sentences stay **true** after this slice: the two releases still have different triggers, and with no production caller the window really is still what closes the dropped-conn case in production. #1744 is what makes that paragraph stale, and #1744 is editing this area anyway. Named here so code review reads the omission as a decision.
- `reapExpiredLocked`'s "IT CALLS NOTHING ON `Registry` EITHER, `Release` included" — still true, and widening it to name `ReleaseConn` too buys nothing.
- `docs/knowledge/features/attachments-package.md` carries the same seven-method roster. **That file belongs to the documentation phase, not to this slice.** Do not edit it.

## Concurrency model

No goroutine is spawned; the package still owns none, and has no ticker and no shutdown path.

`mu` stays a **leaf**: `ReleaseConn` takes it for its whole body, calls no method that takes it, and is never held across a call into `Accumulator`. It joins the locked roster as the eighth member.

The property this slice actually introduces, and the one the doc comment must state:

**`ReleaseConn` is the first remover meant to run on a goroutine other than the one feeding that conn's accumulators — and that is the point.** A teardown path that had to join the conn's frame worker first would need a lifecycle this package does not own. It is safe for `reapExpiredLocked`'s already-recorded reason: the method is **remove-only**. A `Deliver` sitting between its look-up and its release holds a pointer this may drop from the map; its `Assemble` still returns integrity-verified bytes or none, and its own `Release` is a documented no-op on an already-absent key. Nothing here touches the accumulator, so the one-`appFrameWorker`-per-conn precondition — which is about who *feeds* an accumulator — is neither used nor weakened.

`reapExpiredLocked`'s paragraph names "another conn's `Admit`" as the third party that can remove your entry. This adds a second: the teardown goroutine for *your own* conn. Same shape, same argument, and the residual that paragraph already names is unchanged — a late `Release` deletes **by key** and would take a *successor's* entry had the pair been re-admitted in the gap. That needs the same conn id admitted again during teardown, which is a question about conn-id reuse at the call site and therefore #1744's, not this primitive's.

## Error handling

There is none, and that is the design. `ReleaseConn` has no error path, no refusal branch, and no failure mode: releasing a conn that holds nothing is a no-op (AC 3), and releasing a conn that holds four is four deletes. Nothing it can be handed is invalid, because it validates nothing (§ Design, the predicate paragraph).

The one failure mode that exists lives at the call site and is #1744's: passing a `connID` that differs from what `Admit` was given leaves those entries unreleasable. The deterministic backstop is already shipped — `uploadIdleTimeout` reaps such an entry within 15 minutes — so the residual is a bounded slot delay, not a permanent leak. Named in § Security review with its owner.

## Testing strategy

Four new tests in `registry_test.go` plus one extension of an existing test. **Four and no more** — the nearest analogues landed at 382 (#1880) and 395 (#1881) insertions and this slice is narrower than both; a fifth test function is what would push it over.

Reuse `fillRegistry`, `fakeClock`, `testClockStart`, `testConnA`, `testConnB`, `testAttachmentID` rather than deriving new fixtures. Time comparisons use `time.Time.Equal`, never `==`.

1. **AC 1 — releasing a conn removes every upload it holds.** Admit three distinct attachment_ids under `testConnA` (the `(1, 0)` cheapest-conforming declaration `fillRegistry` uses). `ReleaseConn(testConnA)`. Assert `count()` is 0 and each of the three `Lookup`s reports absent. Three, not two: two would leave a "removes the first and the last" mutant indistinguishable from a "removes all" one on a two-entry map.
2. **AC 2 — another conn's uploads survive, including its transfer of the same attachment_id.** `testConnA` admits `testAttachmentID` plus one other id; `testConnB` admits `testAttachmentID` plus one other id — the shared id across two conns is `uploadKey`'s own shape, so this is a statement about the key and not about two unrelated entries. Keep the `*Accumulator` `Admit` returned for `(testConnB, testAttachmentID)`. `ReleaseConn(testConnA)`. Assert: both of A's are absent; both of B's are present; `count()` is 2; and `Lookup(testConnB, testAttachmentID)` returns **the same pointer** B was admitted with — pointer identity, the `TestRegistry_SameAttachmentIDOnTwoConns_AreSeparateEntries` idiom, which is what proves the survivor is B's own transfer rather than merely that *something* sits at the key.
3. **AC 3 — a conn holding nothing changes nothing, and this method does not reap.** One test carries both, because "changes nothing" is exactly what a reap would violate. Build with `newRegistryWithClock(clk.now)` on a `fakeClock` at `testClockStart`; `fillRegistry(t, r, 2)` (both under `testConnA`); `clk.advance(uploadIdleTimeout + time.Second)` so both entries are now expired but nothing has reaped; `ReleaseConn(testConnB)`. Assert `count()` is still 2 and both `Lookup`s still report present. `count` and `Lookup` do not reap, which is what makes them honest observers here. This is Mutant 4's sole red.
4. **AC 4 — the freed slots are usable, and the release is what freed them.** Fixed `fakeClock`, **never advanced**, so no reap can fire anywhere in the test — the attribution half of the AC. Fill to `maxInFlightUploads` split across two conns: two under `testConnA`, two under `testConnB` (admit directly; `fillRegistry` only fills `testConnA`). Assert a fifth pair is refused with an error wrapping `ErrTooManyUploads`. `ReleaseConn(testConnA)`. Assert `count()` is 2, then that **exactly two** new pairs admit with a nil error and the **third** is refused wrapping `ErrTooManyUploads` — "that many new pairs" is an exact count, so the refusal after the second is as load-bearing as the two successes. Assert both of B's entries are still present at the end.
5. **Extend `TestRegistry_ConcurrentMixedOperations_AreSafe`** — do not add a new concurrency test. Add one goroutine that loops `r.ReleaseConn(testConnB)` a few dozen times, and bump the `done` buffer and the drain count by one. **It must target `testConnB`, not `testConnA`**: the 32 workers assert that a pair they just inserted under `testConnA` is present, and a concurrent sweep of `testConnA` would delete it between the insert and the `Lookup`, failing that assertion for a reason that has nothing to do with what is being tested. `testConnB` has no presence assertion — its readers "assert safety, never ordering" — and the closing `count() == 0` holds either way. This buys `-race` coverage of `ReleaseConn` against concurrent `insert`, `Release`, `Lookup` and `count` for ~8 lines.

### Mutants

This package measures sole-redness with `go test -overlay` (absolute-path JSON manifest, no worktree write) rather than arguing it from a table. Run **unfiltered** — a `-run` filter is exactly what makes a "nothing else reddens" claim look true regardless of whether it is — and `grep -a` the output for `build failed` and `declared and not used` before trusting any verdict. All four mutants below delete or add whole statements, so none should trip the unused-local trap, but check anyway.

| # | Mutant | Predicted red | Sole? |
|---|---|---|---|
| 1 | `return` immediately after the first `delete` | Test 1 and Test 4 | No — Test 4's fixture releases a two-upload conn, so only one slot frees and the second re-admit is refused. Predict from what each test *executes*, not from its name. |
| 2 | Drop the predicate — `delete` unconditionally | Tests 2, 3 and 4 | No |
| 3 | Predicate on the wrong half — `key.attachmentID == connID` | Tests 1, 2 and 4 | No |
| 4 | Add `r.reapExpiredLocked()` at the head of the body | **Test 3 only** | **Yes** — and this is the one that matters. It is the sole red that defends the no-reap decision, which is otherwise a "satisfied by construction" claim of exactly the kind this package has twice shipped unpinned (#1796's gate ordering, #1880's shared-core stamp). Tests 1, 2 and 4 stay green under it because none of them advances a clock past the window. Measure it; do not assert it from this table. |

A fifth shape — building the body on a per-pair `r.Release(...)` — is a **deadlock**, not a redness measurement, so it is not a table row. It is named in § Design as the constraint that makes the direct `delete` mandatory.

## Open questions

- **Should `ReleaseConn` return a released count?** Decided **no** for this slice (§ Design). Re-open only when #1744 has a caller that wants one for an operator log line; the answer would then be an `int`, which is injection-free, so the objection would be purely the branch-outside-the-lock one.
- **Where the call goes, and whether a conn id can be reused across a teardown.** #1744's, both. § Security review names the two constraints that ticket inherits.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings, one decision recorded rather than assumed. Two values meet in `uploadKey` and only one is untrusted: `attachment_id` is client-chosen (`docs/protocol-mobile.md` § Attachments: "Not a capability — not secret, not unguessable"), while `connID` is the receiver's own name for the conn and never appears on the wire. `ReleaseConn` reads **only** the trusted half and never the untrusted one — it neither parses, logs, nor returns an `attachment_id`. The design constraint that keeps that honest is stated in § Design: the predicate is exact `==` with no validation and no normalisation, because **`ReleaseConn` must accept exactly what `Admit` stores**. A validating or folding `ReleaseConn` would either refuse to remove entries `Admit` accepted (slot leak) or remove entries belonging to a different conn (the isolation breach AC 2 exists to defend).
- **[Tokens, secrets, credentials]** Not applicable. No token, key or credential is read, written or compared. The one comparison in the method is between two receiver-authored conn names.
- **[File operations]** Not applicable, and stronger than "this method calls nothing on the filesystem" (which it does not — `EnsureDir` and `Store` are this package's only two functions that touch one and neither is reachable from here). There is also no orphaned-file class to worry about: an entry `ReleaseConn` drops is by definition *in flight*, and `Deliver` releases the entry on the completing chunk, so bytes that reached `Store` are never bytes this method could have been holding.
- **[Subprocess / external command]** Not applicable. No `exec`, no environment read.
- **[Cryptographic primitives]** Not applicable, with one deliberate non-use worth naming: the predicate is a plain `==`, not `subtle.ConstantTimeCompare`. Neither operand is a secret — `connID` is receiver-authored and `attachment_id` is documented as guessable by design — so a constant-time compare would defend nothing and would misrepresent the value as one.
- **[Network & I/O]** One finding, resolved by an existing dependency rather than by new code. The sweep is O(`len(r.uploads)`) under the package's **only** mutex, so an unbounded map would make one conn's teardown stall every other conn's admissions and deliveries — a self-inflicted DoS at the teardown path. It is bounded because #1796's gate in `insertLocked` makes "the registry never holds more than `maxInFlightUploads`" (4) a property of the container rather than of one entry point. **This design depends on that gate**, and § Design says so, so that a later ticket lifting the cap sees what it also lifts.
- **[Error messages, logs, telemetry]** No findings. The method is silent by construction: no logger, no format string, no return value. Unlike `Deliver` — whose structural never-log claim code review measured as *unpinned*, since a `fmt.Errorf` wrap interpolating the `attachment_id` stays green under `errors.Is` assertions (recorded in the package overview) — this method has no error to interpolate into, so the absence of a return value is what enforces the rule rather than a fixture that could be missing.
- **[Concurrency]** No findings against this spec; two constraints inherited by #1744, both named below. Within this slice: `mu` stays a leaf (taken for the whole body, calls no method that takes it, never held across an `Accumulator` call); the body must **not** compose the lock-taking `Release`, which would deadlock on a non-reentrant `sync.Mutex` rather than race — and a deadlock is not something `-race` reports, which is why § Design states it as a hard constraint rather than a preference; and it calls nothing on `Accumulator`, never `reject`, because a pointer may be held off-lock by another goroutine, making that a data race and not merely a leaf breach. The genuinely new property is that this is the first remover meant to run on a goroutine *other than* the one feeding that conn's accumulators; it is safe for `reapExpiredLocked`'s recorded remove-only reason, which § Concurrency model restates from this side. No goroutine is spawned, so there is no lifecycle or leak to reason about.
- **[Concurrency — #1744, SHOULD FIX, owner named]** Two constraints the call site inherits. **(a) Lock ordering:** `closeWith` takes `m.pushMu` around its `m.queues` delete. Placing `ReleaseConn` *inside* that critical section introduces a `pushMu → Registry.mu` nesting that nothing needs. It would still be a total order today — `Registry.mu` is a leaf and calls back into nothing — but the call belongs outside `pushMu`, beside the other per-conn cleanups. **(b) One source for the conn id:** #1744 must pass the same `connID` value to `Admit`/`Deliver` and to `ReleaseConn` (i.e. `s.connID`, read once). A mismatch leaves entries unreleasable. The residual is bounded rather than permanent — `uploadIdleTimeout` reaps such an entry within 15 minutes — which is the deterministic backstop, not a second stochastic rule.
- **[Threat model alignment]** No findings. `docs/protocol-mobile.md` § Security model places a user's paired devices in **one trust domain**, so the realistic case this primitive serves is a phone that drops or backgrounds — precisely the silence the `4408` idle-close row describes — rather than a hostile peer. Cross-conn isolation is nevertheless enforced structurally and not trusted: the predicate reads the key's conn half, so one conn's teardown cannot reach another conn's transfer of the same guessable `attachment_id`, and a client cannot use its own drop to evict a peer's slots. Three neighbouring threats are out of scope with their owners named rather than silently absent: wiring the release to the drop is **#1744**; a per-conn sub-cap beneath `maxInFlightUploads` is the refinement that constant's own doc names and has **no ticket**; a cap on a transfer's total lifetime measured from admission is named unassigned by `uploadIdleTimeout`'s doc and likewise has **no ticket**.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-31
