# #1788 — admit an upload into the registry only after both declaration checks

## Files to read first

- `internal/attachments/registry.go` → `Registry` (the type doc's lock rules), `insert`, `Lookup`, `count` — the locked primitive this entry point composes, and the `PRECONDITION` paragraph that already names this ticket as its one caller.
- `internal/attachments/admission.go` → `CheckDeclaration`, `CheckDeclaredSize` — the two checks, their sentinels, and `CheckDeclaredSize`'s `PAIRING OBLIGATION` paragraph (the one paragraph this ticket edits).
- `internal/attachments/accumulator.go` → `NewAccumulator` — the three-scalar constructor this entry point mirrors; `Add` (its fixed check order, steps 2 and 4) — what a caller feeding a repeat's chunk into a surviving incumbent gets.
- `internal/attachments/accumulator_test.go` → `testBoundFixture`, `testBoundFixtureDigest`, `testChunk`, `testFixtureDigest`, `testFixture` — the fixtures the new tests reuse. Read the comment above `testBoundFixture`: it is package-level and **must not be mutated**, in whole or in a slice.
- `internal/attachments/registry_test.go` → `testConnA`, `testConnB`, `testAttachmentID`, `TestRegistry_SecondInsertUnderAHeldPair_KeepsTheIncumbent` — the constants the new tests reuse, and the test that pins `insert` directly (AC 4 re-pins the same property through the exported path).
- `docs/knowledge/features/attachments-package.md` § "Per-upload byte bound (#1777)" and § "In-flight upload registry (#1787)" — why the two checks are siblings rather than one function, and why `insert` never replaces.
- `docs/knowledge/features/attachments-package.md` § "Mutation-testing lessons" — this package measures sole-redness with `go test -overlay`; read the build-failure trap before running any mutant here.

## Context

#1787 landed the registry with an unexported, never-replacing `insert` and no policy about what may enter. #1776 and #1777 landed `CheckDeclaration` and `CheckDeclaredSize`, two pure functions with no production caller. This slice is the join: one exported entry point on `Registry` that runs both checks, and only on a nil answer from both constructs an `Accumulator` from the declared numbers and hands it to `insert`.

That makes the registry's exported surface admission-only: `insert` stays unexported, so after this lands an upload that is in the registry is an upload both checks admitted. That is a property of the API's shape, not something an in-package test can assert (a test can call `insert` directly), so no acceptance criterion claims it.

No ADR is warranted — this discharges an obligation two landed docs already state, and changes no decision.

## Design

### The entry point

Add one method to `registry.go`:

```go
func (r *Registry) Admit(connID, attachmentID string, totalChunks int, size int64, sha256 string) (*Accumulator, error)
```

Behaviour, in order:

1. `CheckDeclaration(totalChunks, size)` — on a non-nil error, return `(nil, err)`.
2. `CheckDeclaredSize(size)` — on a non-nil error, return `(nil, err)`.
3. `NewAccumulator(totalChunks, size, sha256)`, handed to `r.insert(connID, attachmentID, …)`; return `insert`'s answer with a nil error.

Five points are contract rather than preference.

**Scalars, not `protocol.AttachmentChunkPayload`.** The signature mirrors `NewAccumulator` and `CheckDeclaration`. That is load-bearing here for the same reason `CheckDeclaration`'s doc gives: it keeps `Filename` and `Data` structurally out of this function's scope, so two of the four strings `protocol.AttachmentChunkPayload`'s SECURITY block bans from error strings still cannot reach one. The other two — `attachmentID` and `sha256` — necessarily arrive here, which is what turns the rest of the never-log rule into discipline (see below). Decoding a payload into these five arguments is the dispatch site's job (#1744).

**It constructs no error of its own.** Both refusals return the check's error verbatim — no `fmt.Errorf`, no wrapping, no added context. That is the strongest available form of the never-log rule at a function that holds two banned strings: there is no format string for either to leak into. `errors.Is` therefore reaches `ErrInvalidDeclaration` and `ErrUploadTooLarge` unchanged through this entry point, and the numbers each check wrapped survive verbatim.

**The order of the two checks is deliberately not pinned.** A doubly-bad declaration gets whichever sentinel runs first, and nothing has decided which that should be. `CheckDeclaration` runs first here because it mirrors the file order and the `PAIRING OBLIGATION`'s own phrasing, not because that order is a contract. Say so in the method doc, and **no test may assert which sentinel a doubly-bad declaration gets** — see § Testing strategy on why the fixtures are one-fault each.

**A repeat under a held pair answers the incumbent, with a nil error.** `insert` never replaces and reports the repeat back; `Admit` returns what `insert` answered and discards the accumulator it had just built. Refusing instead would need a sentinel, and the ticket rules one out. The consequence is worth stating in the doc: the caller then feeds the repeat's first chunk into the incumbent, which is latched with the numbers the *first* declaration carried, so `Add` refuses it — `ErrTotalChunksMismatch` when the two declarations disagree on the count, `ErrDuplicateIndex` when they agree — and the transfer is rejected and discarded. The declared numbers can never be swapped underneath bytes already held, which is the security property `insert`'s doc claims and this entry point must not weaken. `Admit` returns `insert`'s answer rather than the accumulator it constructed; those are the same pointer on a fresh pair and different pointers on a repeat, and returning the constructed one double-admits.

**A nil error certifies the two numbers and nothing else.** Not the `attachment_id` — `Registry`'s `uploadKey` doc already states that membership certifies nothing, and the canonical-shape check that must run before the id becomes a path component is `EnsureDir`'s (#1781). Not the digest, which is copied into the accumulator uninspected and compared only by `Assemble`. State this in the method doc so #1744 cannot read a nil answer as a verdict on either string.

**The method doc carries the feeding precondition, because this is the exported surface.** `Accumulator` has no mutex by design, and `Registry`'s type doc explains that handing it back off-lock is sound because the conn is in the key and one `appFrameWorker` serialises a conn's frames. That reasoning was written for an unexported `insert`; `Admit` publishes the same hand-back to every caller in the repo, so the precondition has to be stated where a caller reads it: the accumulator `Admit` returns is fed by one goroutine at a time, and two `Admit` calls for one pair hand back the same pointer.

### Lock posture, and the room #1786 needs

`Admit` takes no lock of its own. Its one locked step is `insert`, which is a single acquisition covering check-the-pair-then-write-the-map, so there is no second acquisition to split and no TOCTOU: the two checks are pure functions of the arguments and touch no shared state. Off-lock feeding is unchanged — `Admit` hands the accumulator back and the caller feeds it with `mu` released, sound for exactly the reason `Registry`'s doc gives (the conn is in the key, and one `appFrameWorker` serialises a conn's frames).

`Registry`'s type doc currently reads "NO METHOD OF `Registry` CALLS ANOTHER". `Admit` falsifies that sentence as written, so the developer must correct it — this is not an adjacent-code refactor, it is the sentence this change makes untrue. Required content of the replacement, at most six lines:

- Re-scope the rule to what it actually guards: no method takes `mu` and then calls another that takes `mu`. The four locked methods still never call one another.
- Name `Admit` as the composing method that exists today, and that it takes no lock of its own — its one locked step is `insert` — so the rule is not engaged by it.
- Keep the #1786 warning, corrected: a count read under one acquisition followed by an `insert` under another is not a deadlock, it is the split lock the paragraph above already forbids. The landed text says "deadlocks"; while rewriting the sentence, write what actually happens.

**Leave room for #1786's gate.** Write steps 1 and 2 as two independent guard statements at the top of the body — `if err := …; err != nil { return nil, err }` twice — never chained into one `else if` and never hoisted into a helper that owns "the top of `Admit`". Inserting a third guard at position 0 must then be a pure addition. A note for #1786, not a design this ticket makes: its bound needs two rungs, because its AC wants the concurrency sentinel to win the *ordering* against an invalid declaration while `Registry`'s doc requires the authoritative count-then-insert decision to be *indivisible*. A cheap read at the top decides ordering; the enforcing check belongs inside the critical section `insert` already holds.

### The `PAIRING OBLIGATION` edit (AC 5)

One paragraph in `admission.go`, on `CheckDeclaredSize`. Change only its closing sentence — everything up to "…because Σ len(Data) stays 0" is untouched. The replacement must:

- Keep "Neither function can enforce the other's presence", and keep the clause naming that admission runs in front of the `Accumulator` rather than gating it.
- Land the obligation on `Registry.Admit` instead of the dispatch site (#1744), naming it as the caller that runs both before constructing an `Accumulator` and as the registry's only exported way in.
- Say that this is one caller that runs both and **not a gate**: `NewAccumulator` stays exported and constructible without passing through `Admit`, so a second caller could still run one check or neither.

Do **not** touch `CheckDeclaration`'s "Admission also runs IN FRONT OF the Accumulator…" paragraph — AC 5 requires it to stay standing as written, and the sentence above is what keeps it true. Do not re-point the four `#1778` mentions in this package (#1786's) or the `#1773` mention in `accumulator.go` (its inheritors').

## Concurrency model

No goroutines, no channels, no shutdown sequence. One mutex acquisition per `Admit`, inside `insert`. Two concurrent `Admit` calls for the same pair are safe and already pinned at the `insert` layer by `TestRegistry_ConcurrentSamePairInsert_TellsExactlyOneItIsFresh`: exactly one is told the insert was fresh and both are handed the same pointer. Production cannot reach that case for one conn — one `appFrameWorker` per session serialises a conn's frames — so `Admit` inherits that posture rather than widening it, and needs no concurrency test of its own.

## Error handling

| Condition | Answer |
|---|---|
| `CheckDeclaration` refuses | `(nil, err)` — the check's error verbatim, wrapping `ErrInvalidDeclaration` |
| `CheckDeclaredSize` refuses | `(nil, err)` — the check's error verbatim, wrapping `ErrUploadTooLarge` |
| Both refuse | whichever ran first; unspecified, untested |
| Pair already held | `(incumbent, nil)` — the freshly built accumulator is dropped |
| Otherwise | `(the accumulator now held, nil)` |

No new sentinel, no wire code, no logger, no `fmt` import added to `registry.go`. A refusal returns before anything is constructed or stored, so the registry is left exactly as it was and nothing about the refused pair is remembered — the same id, re-declared correctly, is admitted immediately afterwards.

## Testing strategy

All in `registry_test.go`. Reuse `testConnA` / `testConnB` / `testAttachmentID`, and `testChunk` / `testBoundFixture` / `testBoundFixtureDigest` / `testFixtureDigest` from `accumulator_test.go`.

**Fixtures.** `(373, maxUploadBytes)` is the only admissible multi-chunk declaration this package already has bytes and a written-out digest for — `max(1, ceil(16777216 / 45000))` is 373, its last chunk 37216 bytes. Use it for the admitted rows rather than deriving a new fixture and a new digest constant. Add to `registry_test.go`: `const testBoundTotal = 373` (with a one-line comment saying it is the count `CheckDeclaration` requires for `maxUploadBytes` at `protocol.MaxAttachmentChunkBytes`) and a helper returning chunk *i* of `testBoundFixture` via `testChunk`. The helper slices the shared fixture and must not mutate it.

**One fixture per check, never a doubly-bad one.** The two shapes, each admitted by one check and refused by the other:

- `size` `maxUploadBytes + 1` with `totalChunks` 373 — its own arithmetically-correct count, so `CheckDeclaration` admits and `CheckDeclaredSize` refuses with `ErrUploadTooLarge`.
- `size` 100000 with `totalChunks` 4 — within the byte bound, so `CheckDeclaredSize` admits and `CheckDeclaration` refuses with `ErrInvalidDeclaration` (the honest count is 3).

A doubly-bad row would redden under either check being dropped, proving neither, and would additionally pin a cross-check order that nothing has decided. Do not write one.

Scenarios:

- **A refused declaration stores nothing** (AC 1, AC 2). Table over the two rows above, each against a fresh `Registry`, each passing `testAttachmentID` and `testFixtureDigest` as the two client-supplied strings. Per row: `errors.Is` reaches that row's sentinel; the returned accumulator is nil; `count()` is 0; `Lookup` reports absent. Then the hygiene assertions on the same error — the message contains neither `testAttachmentID` nor `testFixtureDigest`, and *does* contain the declared `size` as a decimal. That last one is the control: an entry point that swallowed the wrapped message would satisfy the two prohibitions vacuously. Both sizes (16777217, 100000) are distinctive enough not to match incidentally, and neither client string is a substring of the `attachments: ` prefix.
- **A refusal blacklists nothing** (AC 1). One registry: refuse under `testConnA` / `testAttachmentID` with either bad shape, then `Admit` the same pair with the admissible bound declaration — nil error, non-nil accumulator, `count()` 1, `Lookup` answers it.
- **An admitted upload is the one held, latched with the declared numbers** (AC 3). `Admit` the bound declaration; assert nil error, that `Lookup` answers the *same pointer* (identity, not field equality — see the note in `TestRegistry_InsertThenLookup_ReturnsTheSameAccumulator`), and `count()` 1. Feed all `testBoundTotal` chunks and `Assemble`: no error, and the bytes equal `testBoundFixture`. A zero-latched accumulator fails on the first `Add`; a size- or digest-dropped one fails at `Assemble`.
- **A repeat under a held pair keeps the incumbent** (AC 4). `Admit` the bound declaration, take the incumbent from `Lookup`, feed it some but not all of its chunks. Then `Admit` the same conn-and-id again with the same declaration. Assert **nothing about the returned error** — the criterion must hold whichever way that call is shaped. Assert: `Lookup` still answers the incumbent pointer, `count()` is 1, and if the second call's returned accumulator is non-nil it is the incumbent (that holds under a refusing design too, which returns nil, and it is what catches a freshly built one being handed back). Then feed the remaining chunks into the incumbent and `Assemble` — no error, bytes equal `testBoundFixture`, which is what proves the chunks it already held survived.

**Mutants and their red sets** (measure with `go test -overlay`, per the package's mutation-testing lessons; grep the run for `build failed` and `declared and not used` before trusting any verdict):

| Mutant | Reddens | Sole red? |
|---|---|---|
| Drop the `CheckDeclaration` guard | the wrong-count row | yes |
| Drop the `CheckDeclaredSize` guard | the too-large row | yes |
| Construct and insert before the guards | both rows' `count()` / `Lookup` assertions | yes |
| Wrap the refusal with the id or the digest | the hygiene assertions | yes |
| Return the bare sentinel instead of the check's error | the "size still present" control | yes |
| Return the constructed accumulator instead of `insert`'s answer | the repeat scenario | yes |
| `Release` the pair before inserting | the repeat scenario | yes |
| Latch zeros or drop one declared number in `NewAccumulator` | the admitted scenario **and** the repeat scenario | no — both assemble end to end, so neither is sole; the admitted scenario is the primary pin |

The blacklist scenario has no sole red against this design, because a refusal returns before the map is touched. It pins an AC-mandated negative property against a *future* design — one that remembers refused pairs — and the mutant that reddens it alone is exactly that: record the pair on refusal and refuse later admissions of it. Stated here rather than dressed up as coverage of the code as specified.

## Open questions

None blocking. One judgement call recorded rather than deferred: a repeat answers the incumbent with a nil error, and #1786 will have to decide whether that path consumes its slot check. Nothing in this slice constrains that choice.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. `Admit` is the boundary where two client-claimed numbers become latched state, and it is a single function rather than a scattered check. It is deliberately *not* typed as a boundary — it returns a plain `*Accumulator`, not a validated wrapper, because AC 5 requires `NewAccumulator` to stay constructible without it and `Assemble`'s `totalChunks < 1` clause to stay load-bearing. The boundary is therefore documented rather than enforced by the type system, and the design compensates by requiring the method doc to state that a nil error certifies the two numbers only — never the `attachment_id` (whose canonical-shape check is `EnsureDir`'s, #1781) and never the declared digest (compared only by `Assemble`). `uploadKey`'s own "MEMBERSHIP CERTIFIES NOTHING" paragraph already says the same thing from the registry's side.
- **[Tokens, secrets, credentials]** Not applicable, stated rather than assumed: nothing here is a credential. The declared `sha256` is an attacker-supplied claim, not a secret — it is copied into the accumulator uninspected and is never promoted to a lookup key, which `AttachmentChunkPayload`'s SECURITY block requires. `attachment_id` is documented as "not a capability" in `docs/protocol-mobile.md` § Attachments, and this design never treats it as one: the key carries the conn, so one client cannot address another's transfer by guessing an id.
- **[File operations]** Not applicable — in-memory only. `EnsureDir` is the package's sole filesystem function and no path is built here. The finding worth naming is the inverse: an id `Admit` accepted has **not** been checked for use as a path component, so #1782's file write must run `EnsureDir`'s validation itself and must not read registry membership as clearance.
- **[Subprocess]** Not applicable — no `exec`, no environment, no signals.
- **[Cryptographic primitives]** No findings and no new primitive. `Admit` performs no comparison and generates no randomness; the digest comparison stays `Assemble`'s plain `!=`, correct for the documented reason that both operands are attacker-supplied so `subtle.ConstantTimeCompare` would protect nothing.
- **[Network & I/O — resource exhaustion]** OUT OF SCOPE, and it is the sharpest hazard here: `Admit` bounds *one* upload's bytes and places no bound on how many uploads may exist, so N distinct `attachment_id` values on one conn yield N entries, each able to retain up to `maxUploadBytes`. That is #1786's entry cap. Two notes that follow from it and should travel with the family: **#1744 must not land ahead of #1786**, because #1744 is what first makes this reachable from production (nothing calls this package today, so this slice changes no live exposure); and an admitted-but-never-completed transfer retains its bytes indefinitely until #1742's expiry lands. What this slice *does* close is the key-space hole `CheckDeclaredSize`'s doc names — run alone it admits 2³¹−1 declared zero-byte chunks, which no byte bound refuses; paired as `Admit` pairs them, an admitted transfer declares at most 373 chunks, so the accumulator's map is bounded. That obligation moves from prose to code here. `Admit` also allocates nothing from either claimed number: `NewAccumulator`'s map takes no capacity hint.
- **[Error messages, logs, telemetry]** No findings, by construction rather than by discipline where that was achievable. Two of the four strings `AttachmentChunkPayload`'s SECURITY block bans from error strings — `Filename` and `Data` — cannot reach `Admit` at all, because the signature takes scalars. The two that must reach it, `attachmentID` and `sha256`, cannot reach an error string because `Admit` builds no error: it returns each check's error verbatim, so there is no format string for either to enter. The one attacker-influenced value that *does* appear in a message is the declared `size`, rendered as an `int64` with `%d` by `CheckDeclaration` and `CheckDeclaredSize`, which cannot carry a newline into #1744's line-oriented log. AC 2's "still carrying the numbers" assertion is the control against an entry point that satisfied the prohibition by swallowing the message. Informational, for #1744 rather than for this slice: `CheckDeclaredSize`'s message states `maxUploadBytes`, which § Attachments calls receiver policy that is deliberately unpublished, so forwarding these Go strings verbatim into a wire error's message field would publish the exact number. Rejection is the intended channel for a client to learn the bound exists; the exact value need not go with it.
- **[Concurrency]** No findings. One mutex, one acquisition per `Admit`, taken inside `insert` and never held by `Admit` itself, so there is no lock ordering to get wrong and no nesting. No TOCTOU: both checks are pure functions of the arguments and touch no shared state, and the entire map decision lives inside `insert`'s single critical section — the design explicitly forbids the `count()`-then-`insert` shape that would introduce one, and tells #1786 that its enforcing rung belongs inside that section. No goroutines are spawned, so none can leak. The one exposure this slice genuinely widens was found by this pass and fixed in § Design: `Accumulator` carries no mutex, and the hand-back-and-feed-off-lock posture was justified in a doc written for an unexported `insert`; `Admit` publishes it, so the method doc must now carry the precondition that the returned accumulator is fed by one goroutine at a time and that two calls for one pair answer the same pointer. Production cannot reach the racing case — the key carries the conn and one `appFrameWorker` serialises a conn's frames — but that is now a stated precondition of an exported method rather than an internal argument.
- **[Threat model alignment]** No findings against `docs/protocol-mobile.md` § Attachments. Probing is worth one line, since the repeat semantics could have created it: a repeat answers the incumbent with a nil error, indistinguishable at the return value from a fresh admission, and the divergence appears only when the caller feeds the chunk and `Add` refuses it. Because the key carries the conn, that tells a client only about its own transfers, so neither this shape nor the refusing alternative leaks cross-conn presence. Deferred threats are named above with their tickets: the entry cap (#1786), expiry of abandoned uploads (#1742), and path-component validation at storage (#1782, using #1781's `EnsureDir`).

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25
