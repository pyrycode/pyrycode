# #1795 — Decide an upload's admission under a single lock acquisition

**Ticket:** [#1795](https://github.com/pyrycode/pyrycode/issues/1795) · `size:s` · `security-sensitive`
**Package:** `internal/attachments` · **Production files touched:** 1 (`registry.go`)

## Files to read first

| Read | Symbols | What to extract |
|---|---|---|
| `internal/attachments/registry.go` | whole file (184 lines) | Every production change lands here. Read it end to end — the doc comments *are* most of the work, and AC 3 sweeps four of them. |
| `internal/attachments/registry_test.go` | `TestRegistry_AdmitUnderAHeldPair_KeepsTheIncumbent` | The shape AC 2's new test copies (admit → feed 2 chunks → repeat → feed the rest → assemble), and the doc AC 3 narrows. |
| ″ | `TestRegistry_AdmitRefusedDeclaration_StoresNothing` | The two declarations AC 2 reuses verbatim, and the doc whose sole-redness rationale is stated backwards. |
| ″ | `TestRegistry_ConcurrentSamePairInsert_TellsExactlyOneItIsFresh` | The `got.got != got.own` pointer-identity assertions that rule out passing `insert` declaration scalars. Stays green, unmodified. |
| ″ | `TestRegistry_AdmitAfterARefusal_AdmitsTheSamePair` | Refuses then admits on the *same* registry — the landed deterministic control for a missed unlock on a refusal path (§ Concurrency model). |
| `internal/attachments/admission.go` | `CheckDeclaration`, `CheckDeclaredSize` | Signatures, and from their docs that both are pure and stateless — no lock, no registry read. That is what lets them run inside the critical section. |
| `internal/attachments/accumulator.go` | `NewAccumulator` | That it cannot fail, and that its chunk map is created **without** a capacity hint. That hint-free map is what makes construction-under-lock bounded work (§ Security review, finding 6). |
| `internal/sessions/pool.go` | `saveLocked` | House phrasing for a caller-holds-the-lock precondition: "Caller MUST hold p.mu". |
| ″ | `Pool.capMu` | The precedent `Registry`'s type doc cites. Its doc names the sequence it serialises so a later caller re-uses it rather than wrapping it — the sentence this slice must keep citing correctly. |
| `docs/knowledge/features/attachments-package.md` | § "In-flight upload registry (#1787, #1788)" | The "one `sync.Mutex`, taken once per operation and held for the whole body — never once to read, again to write" rule this slice extends to `Admit`. |
| ″ | § "Mutation-testing lessons" | The `go test -overlay` recipe, and the build-failure trap (a deleted check that orphans an import or a local reads as green). |
| `CODING-STYLE.md` | § "Comments — Citing Other Code" | `make cite-guard` runs inside `make check` and fails on `foo.go:123` in a `//` comment, at any depth, ranges included. Every comment this slice writes cites by symbol. |

## Context

`Registry` holds one `*Accumulator` per `uploadKey{connID, attachmentID}` behind one `sync.Mutex`. The decision that puts an entry there is split across two layers today: `Admit` runs `CheckDeclaration` and `CheckDeclaredSize` off-lock, constructs the `Accumulator`, and hands it to `insert`, which takes `mu` for the map lookup-and-store alone. `Admit` takes no lock of its own.

**This is not a live race today, and the spec must not claim it is.** Both checks are pure functions of their two scalar arguments; neither reads registry state, so there is nothing for the gap between them and `insert`'s acquisition to be a TOCTOU *on*. What the split cannot carry is a **capacity bound**, which does read registry state: deciding "is there room for a new pair" and storing the entry must be indivisible, or two concurrent first chunks for distinct new pairs both read room and both store. `Registry`'s type doc, `count`'s doc, and the package overview all record that shape as the reason the registry locks the way it does.

This slice moves where the acquisition is taken and ships **no bound**. It is the precondition for the entry-count ceiling, **#1796**, which is natively blocked on it.

It also promotes one accidental behaviour to a decision. Today both declaration checks run on *every* first chunk, including a repeat under a pair the registry already holds, purely because they sit above `insert` in statement order. A restructure that looks the incumbent up first and returns early flips that, and **no landed test catches the flip** — `TestRegistry_AdmitUnderAHeldPair_KeepsTheIncumbent` discards the second call's error by design. AC 2 pins it.

No ADR is warranted: this refines a lock discipline that `Registry`'s type doc already states and that the package overview already records. The documentation phase folds the outcome into `docs/knowledge/features/attachments-package.md` § "In-flight upload registry" — **not this slice's file to touch**, which is why AC 3's `#1786` sweep is scoped to `internal/attachments/`.

## Design

### Shape: `Admit` holds the lock over an unlocked core that `insert` also wraps

Extract the existing lookup-and-store body of `insert` into an unlocked core, `insertLocked`. `insert` becomes a three-line lock-taking wrapper over it. `Admit` takes `mu` for its whole body and calls `insertLocked` directly.

```
insertLocked(connID, attachmentID string, a *Accumulator) (upload *Accumulator, inserted bool)
```

- **Caller MUST hold `r.mu`.** Takes no lock itself. Builds the `uploadKey`, returns the incumbent with `false` when the pair is held, otherwise stores `a` and returns `(a, true)`. This is today's `insert` body verbatim, minus the `Lock`/`defer Unlock` pair.
- Keeps the `connID, attachmentID string` parameters rather than a pre-built `uploadKey`, so key construction stays in exactly one place and `Admit` never names `uploadKey`.
- `inserted` keeps its inverted-`LoadOrStore` meaning (`true` means FRESH) — the warning in `insert`'s current doc moves with the body.

`Admit`'s locked body, in this order — **the order is the contract AC 2 pins**:

1. `CheckDeclaration(totalChunks, size)` → return `(nil, err)` verbatim.
2. `CheckDeclaredSize(size)` → return `(nil, err)` verbatim.
3. `insertLocked(connID, attachmentID, NewAccumulator(totalChunks, size, sha256))` → return `(upload, nil)`.

Both checks run **before** the incumbent lookup that step 3 performs. That is what makes an invalid repeat answer its declaration sentinel rather than the incumbent, and it is today's behaviour preserved rather than changed.

`Lookup`, `Release` and `count` are untouched.

Nothing else moves: no signature on the exported surface changes, `Admit`'s parameter list and return types are identical, `insert`'s signature is identical, and no new exported name appears. `.insert(` resolves to **eleven** sites today; after this slice the one in `Admit` becomes `.insertLocked(` and the ten in `registry_test.go` are untouched. **One consumer call site, not eleven.**

### Why this shape, and not the two the ticket names

| Shape | Call sites | Verdict |
|---|---|---|
| `insert` takes declaration **scalars** | 11 | **Rejected.** Moving construction into `insert` makes `TestRegistry_ConcurrentSamePairInsert_TellsExactlyOneItIsFresh`'s `got.got != got.own` inexpressible — the caller no longer holds a pointer to compare. That is a rewrite of a row on the stays-green-unmodified list, not a compiler-caught cluster. |
| `insert` takes a **thunk** | 11 | **Rejected.** Mechanical, but it buys nothing this shape does not. To preserve AC 2's order the thunk must be invoked *before* the incumbent lookup, so `insert` gains a third return value that is `nil` at all ten test call sites, and every one of them wraps a value it already holds in a closure. That is permanent noise in a test-only helper to close a bypass that is unexported and test-only — and it does not even close it, since a test thunk runs no checks. It also puts the ticket one site over the ten-site sizing boundary for no gain. |
| A second, outer `admitMu` | 1 | **Rejected, per the ticket.** `Registry`'s doc cites `Pool.capMu` specifically to say a later caller re-uses the existing serialisation "rather than wrapping it", and #1796 names read-the-count-then-admit-separately as the control its concurrency test must redden. Code that textually matches the named control, defended by a second lock, is a review finding waiting to happen. |
| **`Admit` locks over a shared `insertLocked`** | **1** | **Chosen.** No signature change, no test churn, `mu` stays one lock and a leaf. |

The one cost the ticket names is real: `insert` remains an in-package insertion path that will not pass through #1796's capacity gate if that gate is written into `Admit`'s body. It is unexported and test-only, so it is not a production bypass. **#1796 has the cheaper option and should be told so**: putting the cap gate inside `insertLocked`, between the lookup and the store, gates *both* wrappers at once and costs only an error return on an unexported helper with two callers. Say so in `insertLocked`'s doc; do not build it here.

### What the shared core buys that a duplicated body would not

`TestRegistry_ConcurrentSamePairInsert_TellsExactlyOneItIsFresh` is the package's sole pin on "one acquisition, not one to read and one to write", and it drives `insert`. Because `insert` and `Admit` now share `insertLocked`, that landed test pins **the exact critical-section body `Admit` executes**, rather than a parallel copy of it. Inlining the lookup-and-store into `Admit` instead would leave the two free to drift, with the pinned one not being the one production uses. This is the reason to extract a core rather than duplicate four lines.

## Concurrency model

No goroutines. No channels. No context. One `sync.Mutex`, unchanged in kind and count.

**The rule, restated for the new shape.** `mu` is still a **leaf** and still taken once per operation:

- Five methods take `mu` — `Admit`, `insert`, `Lookup`, `Release`, `count` — each once, for its whole body, and **none of them calls another**.
- `insertLocked` takes no lock and is the one function callable with `mu` held. It is the only shared body.
- Nothing called under `mu` takes another lock: `CheckDeclaration` and `CheckDeclaredSize` are pure and stateless per their own docs, and `NewAccumulator` allocates and returns.
- Feeding is still **off-lock**. `mu` is never held across `Add` or `Assemble` — construction of an empty accumulator under the lock is not a call into a live one.

**`sync.Mutex` is not reentrant, so getting this wrong deadlocks rather than races.** Two shapes are forbidden and both are one keystroke away:

1. **`Admit` calling `insert`** (which locks) while holding `mu`. Hard deadlock on every admission. The `Locked` suffix is the house signal for "caller holds the lock" — see `saveLocked`, `rekeyLocked`, `advanceLocked` — and it is what keeps the two apart at a glance.
2. **Explicit `Unlock` calls in place of `defer`.** `Admit` now has **three** return paths where today it has one: two refusals and a success. A refusal path that returns without unlocking wedges the registry permanently — and a client triggers a refusal at will with one malformed declaration. **Use `defer r.mu.Unlock()` immediately after `Lock`, never paired explicit unlocks.**

Both failure modes are observable, and the second has a landed deterministic control: `TestRegistry_AdmitAfterARefusal_AdmitsTheSamePair` refuses and then admits **on the same registry**, so a missed unlock on the refusal path blocks its second `Admit` forever. That is a hang, not a red assertion — the package test binary dies on the 10-minute timeout. AC 1's observability clause says the same thing about the existing concurrent rows: under `-race` they pass or they hang; they do not fail quietly.

**Lock-hold time is bounded and attacker-independent.** The critical section grows by two integer comparisons and one `NewAccumulator`, whose chunk map is created without a capacity hint precisely so a claimed `total_chunks` of 2³¹−1 cannot pre-allocate. Nothing an attacker supplies scales the work now done under `mu`. See § Security review, finding 6 — this invariant now lives in another file and the design depends on it, so link the two in `Admit`'s doc.

## Error handling

Unchanged in every respect, and that is load-bearing.

- `Admit` **constructs no error of its own.** Each refusal is the check's error verbatim — not wrapped, not annotated. That is what keeps `attachmentID` and `sha256`, the two banned strings this entry point necessarily holds, out of any message: there is no format string for either to enter. `errors.Is` reaches `ErrInvalidDeclaration` and `ErrUploadTooLarge` unchanged.
- No new sentinel, no new constant, no logger. **The first error this file formats belongs to #1796, not here.**
- A refusal constructs nothing and stores nothing, so the registry is left exactly as it was — now enforced by returning before `insertLocked` is reached, inside the same acquisition.
- Which check runs first stays **not a contract**. `CheckDeclaration` runs first because it mirrors file order. No test may read an order out of it — which is why AC 2 forbids a doubly-bad declaration (see § Testing strategy).

The three message assertions in `TestRegistry_AdmitRefusedDeclaration_StoresNothing` — no `attachment_id`, no digest, *does* carry the declared size — stay green unmodified and are the deterministic control that no format string appeared.

## Testing strategy

### The new test (AC 2)

`TestRegistry_AdmitRefusedRepeatUnderAHeldPair_KeepsTheIncumbent` — table-driven, two rows, one fault per row. #1796 can cite it by symbol in its own stays-green clause.

Shared setup per row (copy the shape of `TestRegistry_AdmitUnderAHeldPair_KeepsTheIncumbent`):

- `Admit(testConnA, testAttachmentID, testBoundTotal, maxUploadBytes, testBoundFixtureDigest)` — the one admissible multi-chunk declaration this package has both bytes and a written-out digest for.
- Feed the incumbent chunks `0` and `1` via `boundChunk`.

Rows — both reuse the declarations already in `TestRegistry_AdmitRefusedDeclaration_StoresNothing`:

| Row | Declaration | Answers | Why it is one fault |
|---|---|---|---|
| size one byte above the per-upload bound | `(testBoundTotal, maxUploadBytes+1)` | `ErrUploadTooLarge` | Arithmetically conforming — both sizes need 373 chunks — so `CheckDeclaration` admits it and only the byte bound refuses. |
| count disagreeing with the declared size | `(4, 100000)` | `ErrInvalidDeclaration` | Well within the byte bound, so `CheckDeclaredSize` admits it and only the arithmetic refuses. 100000 bytes is 3 chunks. |

**Never a single doubly-bad declaration.** The surviving check refuses it anyway, so such a row stays *green* under either removal and pins neither; and asserting a specific sentinel to force a red out of it instead pins which check runs first — the one thing `Admit`'s doc says no test may read.

Per-row assertions, as bullet-pointed scenarios:

- `errors.Is(err, tt.want)` — the declaration sentinel, not a nil error.
- The returned accumulator is `nil`.
- `Lookup(testConnA, testAttachmentID)` still answers the **incumbent** pointer, and `count() == 1` — the refusal disturbed nothing.
- The incumbent kept the chunks it already held and still assembles: feed `boundChunk(i)` for `i` from 2 to `testBoundTotal-1`, then `Assemble()` equals `testBoundFixture`.

Pointer identity, never field equality — an accumulator built from the same declaration compares equal field by field and holds no chunks. Both rows `t.Parallel()`, each on its own `NewRegistry()`, per this file's convention. Digest fixture: pass `testBoundFixtureDigest` on the repeat so the row differs from the incumbent only in the declared numbers.

### Mutants — measure, do not argue from the table

This package's sole-redness claims are measured with `go test -overlay` (mutants applied via an absolute-path JSON manifest, no worktree write), never asserted from a table. Three mutants, all against `Admit`:

| Mutant | New row 1 (oversized) | New row 2 (bad arithmetic) | Also reddens |
|---|---|---|---|
| **M1** — delete the `CheckDeclaredSize` guard | RED | green | `AdmitRefusedDeclaration_StoresNothing`'s row 1, `AdmitAfterARefusal_AdmitsTheSamePair` |
| **M2** — delete the `CheckDeclaration` guard | green | RED | `AdmitRefusedDeclaration_StoresNothing`'s row 2 |
| **M3** — hoist the incumbent lookup ahead of both checks (the early-return flip) | RED | RED | **nothing else in the package** |

M1 and M2 establish that the two rows **discriminate**: each is red for its own check's removal and green for the other's. They are not globally unique reds — the sibling rows redden too, which is expected and correct, since those pin the same checks on a *fresh* pair. Do not chase global uniqueness; the property AC 2 asks for is the discrimination between the two new rows.

**M3 is the mutant that justifies the test's existence.** Every landed test stays green under it: the sibling refusal rows use a fresh registry so the lookup misses and the checks still run; `AdmitUnderAHeldPair_KeepsTheIncumbent` repeats the *same, valid* declaration so it reaches the incumbent either way. Write M3 as a **full replacement** of `Admit`'s locked body, not an extra check prepended to it — #1787's split-lock mutant measured green the first time for exactly that reason, because the original body's own logic still ran underneath.

Build-failure trap: none of these three orphans an import or a local. `CheckDeclaration`/`CheckDeclaredSize` are same-package functions, and `totalChunks`/`size` stay live through `NewAccumulator` in every mutant. Still grep the overlay output for `build failed` and `declared and not used` before trusting a verdict — a passing `go test` on a broken build is indistinguishable from a green one by exit code alone.

No new concurrency test. AC 1's single-acquisition property is structural — `insertLocked` takes no lock and has two lock-holding callers — and `TestRegistry_ConcurrentSamePairInsert_TellsExactlyOneItIsFresh` already pins the shared body. Its documented `-count=5` recipe is known to be a false green ~62% of the time; that is a landed limitation of a stays-green test, **not this slice's to re-tune**.

### Gate

`make check` (vet + `-race` + staticcheck + substrate-guard + fake-claude e2e). It includes `make cite-guard`, which fails on any `foo.go:123` in a `//` comment the branch adds or modifies, at any depth, ranges included — and this slice rewrites four comments. No filesystem, no wire, no live-claude surface is touched, so `make check` is the honest gate here.

## The comment sweep (AC 3)

Every comment in `internal/attachments` describing the arrangement this slice changes must be true of the new one. **Read the whole `Registry` type doc rather than grepping for phrases** — its claims wrap across `//` lines (the count word "four" ends one line and "locked methods" begins the next), so a line-based grep for `four locked` returns nothing while the stale claim sits right there.

| Comment | What is now false |
|---|---|
| `Registry` type doc | "the sequence this one serializes is check-the-pair-then-write-the-map" — it is now check-the-declaration-then-check-the-pair-then-write-the-map. "**the four** locked methods take mu for their whole body" — five now. "Admit is the composing method and takes no lock of its own — its one locked step is insert's single acquisition — so the rule is not engaged by it" — `Admit` now takes `mu`; the rule that keeps it safe is that `insertLocked` takes none. "the enforcing check belongs inside the section **insert** already holds" — the section `Admit` holds. `mu` is still a leaf, but say why construction-under-lock does not break that. |
| `Admit` doc | "The two are written as independent guard statements rather than chained, so #1786's concurrency bound can go in front of both as a pure addition" — wrong twice: the ticket is #1796, and the bound goes **behind** both checks and behind the incumbent lookup, not in front. "Admit takes no lock of its own and hands the accumulator back off-lock" (in the feeding precondition) — it takes `mu` for the whole decision and hands the accumulator back off-lock. "A REPEAT under a pair already held answers the INCUMBENT" — true only of a repeat whose declaration is **admissible**; an invalid one answers its sentinel. |
| `insert` doc | Its shape moves. "That one caller constructs the value it passes" is false — `Admit` no longer calls `insert`; its callers are this package's tests. State that it is now the lock-taking wrapper and that `Admit` shares its body via `insertLocked`. |
| `insertLocked` doc (new) | "Caller MUST hold `r.mu`" in the house phrasing of `saveLocked`. Carry over the never-replace security rationale and the inverted-`loaded` warning. Name the seam for #1796: a cap gate placed here, between the lookup and the store, gates both wrappers at once. |
| `count` doc | `#1786` → `#1796`. Its content — an exported `count` would publish the TOCTOU shape — stays true verbatim. |
| `TestRegistry_AdmitUnderAHeldPair_KeepsTheIncumbent` doc | "whether this entry point answers the incumbent or refuses the repeat is not pinned" is exactly what AC 2 falsifies for an invalid repeat. **Narrow it, do not delete it**: this test's repeat carries the *same, admissible* declaration, so it answers the incumbent; the invalid repeat is pinned by `TestRegistry_AdmitRefusedRepeatUnderAHeldPair_KeepsTheIncumbent`. Its **assertions are not this slice's to change** — AC 3 asks for the doc. |
| `TestRegistry_AdmitRefusedDeclaration_StoresNothing` doc | Its rationale is backwards: a doubly-bad declaration does not "redden under either removal and therefore prove neither" — the surviving check refuses it anyway, so it stays **green** under either removal, which is a *better* argument for one fault per row. Its second clause (such a row would pin an undecided cross-check order) is correct as written; keep it. Two lines. Assertions, fixtures and expected values unmodified. |

### The `#1786` count

Run `grep -rn 1786 internal/attachments/` **before starting and again at the end**. It returns **five lines across four comments** on `main` today, all in `registry.go`, and must return exactly **one** when this slice is done: `uploadKey`'s "entry cap" sentence.

**Do not rewrite that one.** It describes what bounds attacker-influenced key bytes rather than where the lock is taken, and it is #1796's to clear when the cap makes it present-tense. Leaving it is this family's convention, not an oversight — `grep -rn 1778 internal/attachments/` still returns four lines across three comments in two files, left by #1787 for the ticket that actually ships that bound.

Of the sentences this slice rewrites, any that still points forward at the unbuilt ceiling names **#1796**, never #1786, which closed without shipping.

### Prose budget

This package runs 4:1 to 7:1 doc-to-code, and #1777 overran its architect's estimate by 42% on doc-paragraph growth. **Two subjects are worth a new paragraph**, and only two:

1. Where the acquisition is taken, and why `mu` is still a leaf.
2. Why the repeat's declaration verdict is now a decision rather than an accident of statement order.

Everything else on the table above is a correction to an existing sentence, not a new paragraph.

## Scope fences

In-memory only: no disk, no wire dispatch, no wire codes, no logging, **no bound**. No new exported surface. No behaviour change beyond the row AC 2 pins.

The entry-count ceiling is #1796 (blocked on this). Routing a later chunk into an admitted upload and releasing on completion or refusal are #1784's. Expiry and abandonment are #1742's. The dispatch site that drives this registry and maps its sentinels to wire codes is #1744's. Storage is #1782's, the id's canonical shape as a path component #1781's, filename sanitising #1772's.

`docs/knowledge/features/attachments-package.md` — including its own two `#1786` mentions — belongs to the documentation phase. **Never this slice's**, which is why AC 3's sweep stops at `internal/attachments/`.

## Open questions

None blocking. Two decisions recorded so the developer does not re-litigate them:

- **`insertLocked` takes strings, not a pre-built `uploadKey`.** Key construction stays in one place and `Admit` never names the type. If the developer prefers the key, it is a wash — do not spend turns on it.
- **`NewAccumulator` is called before the incumbent lookup and discarded on a repeat**, exactly as today. Constructing only on a miss would need a thunk, which is the shape rejected above; the allocation is one empty hint-free map.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. `Admit` remains the single explicit boundary and the only exported way in; its parameter list, return types and the two checks that gate it are unchanged, so what a nil error certifies — the two numbers, and neither the `attachment_id` nor the digest — is untouched. The new `insertLocked` is *narrower* than the `insert` it was extracted from (it demands the lock in addition to a non-nil accumulator), is unexported, and has exactly two in-package callers. No boundary moves; `uploadKey`'s "MEMBERSHIP CERTIFIES NOTHING" block stays accurate as written.
- **[Tokens, secrets, credentials]** Not applicable by design decision, not by absence of thought. The one hash-shaped value in play, the declared `sha256`, is attacker-supplied, is documented as not a capability and deliberately not promoted to a lookup key anywhere in this package, and is copied into the accumulator uninspected. This slice adds no storage, no rotation surface and no expiry.
- **[File operations]** Not applicable — this slice touches no filesystem. `EnsureDir` and `Store`, the package's only two functions that do, are neither called nor modified.
- **[Subprocess]** Not applicable — no `exec` surface anywhere in this package.
- **[Cryptographic primitives]** Not applicable — no primitive is added, chosen or compared here. `Assemble`'s plain `!=` against `hex.EncodeToString` output is untouched.
- **[Network & I/O — resource exhaustion]** **SHOULD FIX.** Moving construction inside `mu` is safe *only because* `NewAccumulator` creates its chunk map without a capacity hint. That is currently justified in `NewAccumulator`'s own doc on independent grounds (protocol's NEVER ALLOCATE FROM A CLAIM rule), and this design now silently depends on it: a future capacity hint derived from the attacker-chosen `totalChunks` would turn a claimed 2³¹−1 into attacker-scaled allocation *inside the registry's only mutex*, which is strictly worse than today, where the same allocation happens off-lock. As designed, the critical section grows by two integer comparisons and one bounded allocation — no amplification. Mitigation is one clause in `Admit`'s doc linking the two facts from the side that now depends on the invariant; already folded into § Concurrency model. Not a MUST FIX: the landed code is safe and `NewAccumulator`'s own doc already forbids the hint.
- **[Error messages, logs, telemetry]** No findings, with a landed deterministic control. `Admit` still constructs no error of its own, so there is no format string for `attachmentID` or `sha256` to enter, and the package has no logger. `TestRegistry_AdmitRefusedDeclaration_StoresNothing`'s three message assertions — no `attachment_id`, no digest, *does* carry the declared size as the anti-vacuity control — stay green unmodified and would redden if a wrapping format string appeared.
- **[Concurrency — deadlock]** **SHOULD FIX**, and the highest-value item in this pass. `Admit` gains three return paths where it has one today, two of them client-triggerable refusals. A refusal path that returns without releasing `mu` wedges the registry permanently for every conn, and one malformed declaration triggers it. The design constraint is `defer r.mu.Unlock()` immediately after `Lock`, never paired explicit unlocks; the second forbidden shape is `Admit` calling the lock-taking `insert` instead of `insertLocked`, which deadlocks on every admission since `sync.Mutex` is not reentrant. Both are stated in § Concurrency model. The safety net is deterministic code rather than a second rule: `TestRegistry_AdmitAfterARefusal_AdmitsTheSamePair` refuses and then admits on the *same* registry, so either mistake blocks its second `Admit` forever and the package test binary dies on the timeout.
- **[Concurrency — TOCTOU]** No findings, and the spec is deliberately careful not to overclaim. Today's split acquisition is not a live TOCTOU: both checks are pure functions of their scalar arguments and read no registry state, so there is nothing for the gap to race. It becomes one the moment #1796's cap check, which does read registry state, is added — which is what this slice exists to make possible. `insertLocked` keeps `insert`'s never-replace behaviour, the security property that stops a second first-chunk from swapping a live transfer's declared `size`/`sha256` out from under bytes already accumulated; it is pinned through both wrappers by `TestRegistry_SecondInsertUnderAHeldPair_KeepsTheIncumbent` and `TestRegistry_AdmitUnderAHeldPair_KeepsTheIncumbent`, and because the two wrappers now share one body, those tests pin the body production actually runs.
- **[Concurrency — starvation]** No findings. Go's `sync.Mutex` hands the lock FIFO to any waiter blocked over 1 ms, so a hostile flood of `Admit` calls cannot starve `Lookup` or `Release` — and the section it holds is O(1) regardless.
- **[Concurrency — goroutines and shutdown]** Not applicable. This slice spawns no goroutine and the new test spawns none; `Registry` is in-memory, process-lifetime, with no persistence to leave partial on a signal.
- **[Threat model alignment]** **OUT OF SCOPE → #1796.** The registry still places no bound on how many uploads may exist, so N distinct `attachment_id` values on one conn yield N entries. This slice changes no live exposure — nothing in the daemon calls this package yet — and it is the precondition for the bound rather than the bound. #1788's own security review's sequencing note stands unchanged and is restated here so it is not lost between tickets: **#1744, the dispatch site and this package's first production caller, must not land ahead of #1796.**

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25
