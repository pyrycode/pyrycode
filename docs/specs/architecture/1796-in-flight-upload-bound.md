# #1796 — bound how many attachment uploads may be in flight at once

## Files to read first

- `internal/attachments/registry.go` → `Registry` (type doc), `insertLocked`, `insert`, `Admit`, `count`, `uploadKey` — the whole surface this ticket touches. Read the docs, not just the bodies: #1795 wrote three of them specifically to reserve the seam this slice fills, and eight sentences here mention `#1796` and have to be re-pointed.
- `internal/attachments/admission.go` → `maxUploadBytes`, `ErrInvalidDeclaration`, `ErrUploadTooLarge`, `CheckDeclaration`, `CheckDeclaredSize` — the two landed refusals the new one must stay distinct from, the constant whose budget the new one inherits, and the doc paragraphs AC 5 corrects. Note `CheckDeclaration`'s scalars-only signature and *why*: that argument is the template for `capacityRefusal` below.
- `internal/attachments/accumulator.go` → the package doc (its "resource bounds are owned three separate ways" paragraph) and `NewAccumulator` — the package doc's `#1778` sentence is one of AC 5's edits, and `NewAccumulator`'s no-capacity-hint rule is what keeps the work inside `Registry.mu` bounded.
- `internal/attachments/registry_test.go` → `testConnA`, `testAttachmentID`, `testBoundTotal`, `TestRegistry_ConcurrentSamePairInsert_TellsExactlyOneItIsFresh` (fan-out mechanics to copy — **not** its single-round structure), `TestRegistry_ConcurrentMixedOperations_AreSafe` (the one existing test whose *behaviour* changes), `TestRegistry_AdmitRefusedDeclaration_StoresNothing` (the two one-fault-per-row declarations to reuse verbatim, and the never-log assertion shape to copy).
- `internal/attachments/accumulator_test.go` → `newTestAccumulator`, `testChunk`, `testFixtureDigest`, `testTotal` — the fixtures every registry test already keys on.
- `docs/knowledge/features/attachments-package.md` § "Per-upload byte bound (#1777)" — the code-review correction landed as text only: the daemon-wide worst-case **peak** is `2N × bound`, not `(N+1) × bound`. AC 5's last clause is that correction reaching the code.
- `docs/knowledge/features/attachments-package.md` § "Mutation-testing lessons" — the overlay recipe AC 4's measurement runs under, including `grep -a` and the "build failed" / "declared and not used" false-green traps.
- `docs/knowledge/features/attachments-package.md` § "In-flight upload registry (#1787, #1788, #1795)" — one-fixture-per-check discipline, and the sequencing note this ticket discharges.
- `internal/sessions/pool.go` → `Config.ActiveCap` — read the doc: it caps concurrently *active* claude processes and **defaults to uncapped**. That is the finiteness argument for daemon-wide over per-session.
- `internal/relay/v2session.go` → `V2SessionManager.appFrameWorker` — the "no two handlers for the same conn run concurrently" guarantee, which is per-*conn* and is why the peak is `2N × bound`.
- `internal/protocol/codes.go` → `CodeAttachmentTooManyUploads` — the wire code the new sentinel will map to at #1744's dispatch site. This package still imports no codes; do not add the mapping here.

## Context

`internal/attachments`' `Registry` holds one `*Accumulator` per `{conn, attachment_id}` pair. `attachment_id` is client-chosen, so the entry count is unbounded state: a client opens one transfer per id and finishes none. `Admit` caps the *bytes* of one admitted transfer (paired, `CheckDeclaration` + `CheckDeclaredSize` cap it at 373 chunks); nothing caps how many transfers exist at once. #1795 moved the whole admission decision under one acquisition of `Registry.mu` precisely so this slice's gate can be *part* of that decision rather than a check racing it.

Nothing in production calls this package yet — verified: no `attachments.NewRegistry`, `attachments.Registry` or `.Admit(` call site exists outside the package. That is why the family's sequencing note says #1744 must not land ahead of this ticket: the moment the dispatch site is wired, N distinct ids on one conn yield N entries.

This is an in-memory bound only: no disk, no wire dispatch, no wire codes, no logging. The refusal is a Go sentinel; mapping it to `CodeAttachmentTooManyUploads` is #1744's job.

No ADR is warranted. The design decisions here (daemon-wide scope, gate placement, the inherited byte budget) belong in the package overview, which the documentation phase owns.

## Design

Three production files change. No new file, no new exported type or interface.

### 1. The constant — `maxInFlightUploads = 4`, in `admission.go`

Declared next to `maxUploadBytes`, not in `registry.go`, even though `registry.go` is where it is enforced. `admission.go` is the file that owns the numbers behind this receiver's refusals, `maxUploadBytes` is already enforced from two other files (`CheckDeclaredSize` here and step 5 of `Add` in `accumulator.go`), and the two constants multiply into one budget — adjacency is what keeps that arithmetic legible and keeps the peak/resident correction next to both numbers. Unexported and untyped, for the reasons `maxUploadBytes`' own closing paragraph gives (receiver policy `docs/protocol-mobile.md` § Attachments deliberately leaves unpublished; different readers want different types).

Its doc has exactly two subjects, and **inherits** rather than re-derives the byte figures — this is the ticket's size lever, and the one line of the estimate that fails first if the fence is ignored:

- **Why daemon-wide, not per session.** One `Registry` for the daemon; the bound counts uploads across every conn. Per-session multiplies the byte product by a session count nothing bounds — `internal/sessions`' `Config.ActiveCap` caps concurrently *active* claude processes rather than pool entries, and is uncapped by default — so a per-session registry has no finiteness story. The known cost is that one conn can occupy every slot and starve the others; acceptable here because peers are the user's own paired devices, the refusal is transient, and #1742's reaper reclaims abandoned slots. A per-conn sub-cap *under* this ceiling is a later refinement, not this family's.
- **The arithmetic it inherits.** Worst-case **resident** attachment bytes is `maxInFlightUploads × maxUploadBytes` = 64 MiB, which is the budget `maxUploadBytes`' doc already names as the one this bound inherits rather than re-derives. Worst-case **peak**, while assemblies are in flight, is `2 × maxInFlightUploads × maxUploadBytes` = 128 MiB. Point at `maxUploadBytes` for the derivation; do not restate it.

Do not add a `#1777`-shaped derivation paragraph. Two paragraphs plus the unexported/untyped sentence is the whole doc.

### 2. The sentinel — `ErrTooManyUploads`, in `admission.go`

```go
var ErrTooManyUploads = errors.New("attachments: too many uploads in flight")
```

A third sentinel, distinct from both `ErrInvalidDeclaration` and `ErrUploadTooLarge`. `ErrUploadTooLarge`'s doc already argues by name for exactly this sibling and why it must not be folded in: retryability *inverts* across the two — this one clears when other uploads finish, that one never clears for that file. One sentence of doc, plus the retryability contrast, plus the same "not in `accumulator.go`'s var block, because neither `Add` nor `Assemble` raises it" note both neighbours carry.

### 3. The message constructor — `capacityRefusal(inFlight int) error`, in `admission.go`

```go
func capacityRefusal(inFlight int) error   // wraps ErrTooManyUploads with inFlight and maxInFlightUploads
```

This is the first error the admission path *formats* — `Admit` constructs none of its own today, every refusal being a check's error verbatim — so it is the first place the never-log discipline has to be kept rather than inherited. Giving the format string its own function whose only parameter is an `int` makes that structural again at the site that matters, exactly the argument `CheckDeclaration`'s scalars-only signature makes: `attachmentID` and `sha256` cannot enter a message from a function that cannot see them. `insertLocked` still holds the id (it is the map key), which is why the format string does not live there.

The message carries **both** the in-flight count and the bound. They are the same number at every refusal today, because the gate runs before every store so `len(r.uploads)` can never exceed the bound — say so in the doc so a later reader does not "simplify" one away, and so no test tries to discriminate them. The bound's presence is AC 1's control: a swallowed message would satisfy both prohibitions vacuously.

### 4. The gate — in `insertLocked`, between the look-up and the store

`insertLocked` gains an error return. Contract:

```go
func (r *Registry) insertLocked(connID, attachmentID string, a *Accumulator) (upload *Accumulator, inserted bool, err error)
```

Behaviour, in order, unchanged except for the new middle step:

1. Pair already held → `(incumbent, false, nil)`. **Before** the gate, which is what makes a held pair exempt from a capacity refusal without any early return in front of the declaration checks — AC 3 is satisfied by construction here, not engineered.
2. `len(r.uploads) >= maxInFlightUploads` → `(nil, false, capacityRefusal(len(r.uploads)))`. Stores nothing.
3. Otherwise store and answer `(a, true, nil)`.

Two traps to call out in the body's comments:

- **Read `len(r.uploads)` directly, never `r.count()`.** `count` takes `mu` and `insertLocked` runs with `mu` held; `sync.Mutex` is not reentrant, so that call deadlocks rather than races. `count` stays unexported and test-only.
- **`>=`, not `==`.** Equivalent today for the reason above, and the safe form is written anyway — the same discipline `Add`'s subtraction form records.

`insert` propagates the three values unchanged; its doc gains "the same capacity refusal" alongside the same never-replace answer and the same inverted `inserted`.

`Admit` propagates it too:

```go
upload, _, err := r.insertLocked(connID, attachmentID, NewAccumulator(totalChunks, size, sha256))
if err != nil {
    return nil, err
}
return upload, nil
```

Two consequences worth a clause each in `Admit`'s doc:

- It still constructs no error of its own; the third refusal is `insertLocked`'s error verbatim, and the never-log guarantee is now kept by `capacityRefusal`'s signature rather than by there being no format string at all. Amend that paragraph rather than delete it.
- At capacity, `NewAccumulator` still runs and its result is dropped. That is deliberate and cheap: the alternative — checking capacity in `Admit` before constructing — would have to re-do `insertLocked`'s incumbent look-up to keep a held pair exempt, reaching into `r.uploads` from a method that currently does not. The dropped allocation is bounded for the reason `Admit`'s doc already gives (the chunk map takes no capacity hint), and the whole thing stays inside one acquisition.

### Why the gate is in `insertLocked` and not `Admit`'s body

#1795 pointed here and priced it: a gate between the look-up and the store bounds both callers at once "for the price of an error return on an unexported helper", where a gate in `Admit`'s body alone leaves `insert` ungated. The price is now measured: 7 destructuring `insert` call sites in `registry_test.go` need a one-token arity edit (3 further calls are bare statements and need none), plus the two `insertLocked` callers. The payoff is that the invariant "the registry never holds more than `maxInFlightUploads`" holds through every insertion path in the package, which is what makes `count()`'s meaning uniform and AC 2's "slot accounting is exactly the set of live admitted uploads" a property of the container rather than of one entry point.

## Concurrency model

No new goroutines, no channels, no context. The whole change lives inside the single acquisition of `Registry.mu` that #1795 established.

- **`mu` stays a LEAF.** The gate adds a `len()` on a map and a call to `capacityRefusal`, which is pure formatting — no lock, no I/O, no allocation of attacker-scaled size.
- **Indivisibility is the property, not race-freedom.** A gate that reads the count under one acquisition and admits under another is race-free, passes `-race`, and satisfies every criterion here except AC 4. That shape is the mutant AC 4's test must be the sole red for.
- **Feeding stays off-lock.** Unchanged: the caller takes the `*Accumulator` back and feeds it with `mu` released, sound because the conn is in the key and `appFrameWorker` serialises one conn's frames.

## Error handling

Three refusal branches on the admission path, in this order:

| Order | Branch | Sentinel | Clears? |
|---|---|---|---|
| 1 | `CheckDeclaration` | `ErrInvalidDeclaration` | never, for that declaration |
| 2 | `CheckDeclaredSize` | `ErrUploadTooLarge` | never, for that file |
| 3 | capacity gate | `ErrTooManyUploads` | **yes** — when other uploads finish |

The bound is deliberately **last**. At capacity, a first chunk whose declaration is also invalid answers the declaration sentinel, so a client is told the fault that will never clear ahead of the one that clears by itself. Which of gates 1 and 2 runs first stays not-a-contract, exactly as `Admit`'s doc says; only gate 3's position relative to the pair is pinned.

Nothing blacklists an `attachment_id`. A refused id may start a fresh transfer as soon as a slot is free — immediately for the two declaration refusals, which never took a slot, and on the next `Release` for one refused by the bound. Churn is bounded by the cap at any instant.

## Doc sweep (AC 5)

Six edits, most of them one clause. **Rule for the eight `#1796` mentions in `registry.go`: future tense moves, attribution stays.** This repo cites landed tickets by number (`registry.go` names "#1788's admission entry point"), so a `#1796` that reads as attribution after the fact is correct and stays; only sentences describing this bound as *future work* change. This third sweep is not a mechanical check and must not become one.

1. `accumulator.go` package doc — "how many uploads may be in flight is #1778's" → names `maxInFlightUploads`. Clears the last `#1778`.
2. `admission.go` `maxUploadBytes` doc, the WORST-CASE RESIDENT paragraph — `#1778` → `maxInFlightUploads`; keep the "wanting more concurrency moves one of the two numbers" trade-off sentence.
3. `admission.go` `maxUploadBytes` doc, the "Retained is not peak" paragraph — replace "gains one more bound while an assembly is in flight" with the `2N × bound` peak: `appFrameWorker`'s no-two-handlers guarantee is per-**conn**, so N sessions can each be inside their own `Assemble`; 128 MiB at this concurrency, not 80 MiB. Cite `appFrameWorker` by symbol.
4. **Two different 128 MiBs — do not leave them adjacent.** The resident paragraph currently reads "64 MiB at a concurrency of 4, 128 MiB at 8"; edit 3 introduces a *peak* figure that is also 128 MiB, at a concurrency of 4. Since edit 2 rewrites that sentence anyway, **drop the "128 MiB at 8" hypothetical** — the concurrency is now a named constant, so the hypothetical earns nothing and the trade-off sentence carries what it was there for. A doc that leaves the two 128s side by side invites the exact arithmetic slip edit 3 exists to fix.
5. `admission.go` `ErrUploadTooLarge` doc — "a general resource-limit sentinel #1778 could share" changes **shape, not tense**: it now names the sibling it was arguing for, `ErrTooManyUploads`. The rest of that paragraph, already present-tense about `CodeAttachmentTooManyUploads` being transient, is correct and needs no edit.
6. `registry.go` `uploadKey` doc — "the entry cap (#1786)" → `maxInFlightUploads`. Clears the last `#1786`.

Then the eight `#1796` sentences in `registry.go` (`Registry`'s type doc ×3, `insertLocked`'s doc, `Admit`'s doc ×3, `count`'s doc), re-pointed per the rule above.

**Mechanical acceptance checks** — both must come back empty:

```bash
grep -rn 1778 internal/attachments/
grep -rn 1786 internal/attachments/
```

## Testing strategy

All in `registry_test.go`. Scenarios, not code — write them in the file's existing idiom.

### Helper

One helper that fills a registry to `n` entries through `Admit` (the production path, so slot accounting means what AC 2 says it means), under ids distinct from `testAttachmentID`, `t.Helper()` + `t.Fatalf` on any refusal. **Each filler declares `(totalChunks: 1, size: 0)`** — the cheapest conforming pair, since `max(1, ceil(0 / 45000))` is 1. This matters: the bound is now the *later* of three gates, so a fixture that means to reach it must get past both declaration checks first, or the row goes green having proved nothing.

### New tests

1. **`TestRegistry_AdmitAtTheBound_RefusesANewPair`** (AC 1, AC 2) — fill to `maxInFlightUploads`, then `Admit` a new distinct pair. Assert: `errors.Is(err, ErrTooManyUploads)`; **not** `errors.Is(err, ErrUploadTooLarge)` and **not** `errors.Is(err, ErrInvalidDeclaration)` (the "distinct sentinel" clause is a real assertion, not a naming convention); nil accumulator; `count()` unchanged at the bound; `Lookup` of the refused pair absent. Message: does **not** contain `testAttachmentID`, does **not** contain the declared digest, **does** contain the bound's decimal — that last one is the control, copied from `TestRegistry_AdmitRefusedDeclaration_StoresNothing`'s shape. Finally, an immediate retry of the same id is refused again while the bound is still met.
2. **`TestRegistry_AdmitAtTheBound_AnswersTheDeclarationSentinelFirst`** (AC 1's ordering row) — table, two rows, **one fault per row**, reusing `TestRegistry_AdmitRefusedDeclaration_StoresNothing`'s two declarations verbatim (`(testBoundTotal, maxUploadBytes+1)` → `ErrUploadTooLarge`; `(4, 100000)` → `ErrInvalidDeclaration`). Fill to the bound, then `Admit` a new distinct pair with the row's bad declaration; assert the row's sentinel and **not** `ErrTooManyUploads`, and that `count()` is unchanged. This is the sole red for a gate placed ahead of the declaration checks.
3. **`TestRegistry_AdmitAtTheBound_ReleaseReturnsTheSlot`** (AC 2) — two statements in one test, both about slot accounting: (a) at `maxInFlightUploads - 1`, a *declaration* refusal does not consume the last free slot — a different new pair is still admitted afterwards; (b) at the bound, `Release` one entry and the next new distinct pair is admitted, putting `count()` back at the bound.
4. **`TestRegistry_ConcurrentAdmitAtTheBound_AdmitsExactlyTheFreeSlots`** (AC 4) — the indivisibility pin. **Many short rounds, not one wide fan-out.** Per round: a fresh registry filled to `maxInFlightUploads - 1` (one free slot); K goroutines released together by a closed `start` channel, each `Admit`-ing its own distinct new pair with the cheap conforming declaration; results joined over a channel **buffered to K** (unbuffered parks undrained goroutines for the life of the test binary, per the file's existing convention note). Assert per round: exactly 1 nil error; every other error wraps `ErrTooManyUploads`; `count() == maxInFlightUploads`. Start at ~200 rounds × 8 goroutines and tune by measurement — see below. Copy `TestRegistry_ConcurrentSamePairInsert_TellsExactlyOneItIsFresh`'s fan-out mechanics and **not** its single-round structure; a single round is exactly the shape that measured 0/5 detection on an earlier ticket here.

### AC 4's measurement — required, not optional

The AC calls for a measured detection rate against a named control. Build the control as an overlay mutant of `registry.go` that moves the capacity read *out* of the single acquisition: `Admit` calls `r.count()` before `r.mu.Lock()`, decides on that stale `n` inside the lock (exempting a held pair, so the mutant still satisfies AC 3), and `insertLocked` loses its gate. That mutant satisfies every other row in this file and must be red only on this test.

Run it per the package's overlay recipe: `go test -overlay=<absolute-path JSON> -race -run <TestName> -count=5 ./internal/attachments/`, `grep -a` the output, and check for `build failed` and `declared and not used` before trusting any verdict — a passing `go test` on a broken build is indistinguishable from a green run by exit code alone.

Raise the round count until the mutant is red in **5 of 5** iterations, and keep the test under about a second under `-race`. **Record the measured rate — rounds, goroutines, red iterations out of 5 — in the test's own doc comment**, the way `TestRegistry_ConcurrentSamePairInsert_TellsExactlyOneItIsFresh`'s doc records its own probabilistic redness. If the ceiling on wall clock is reached before 5/5, report the rate actually measured rather than inflating rounds further; a reported 3/5 is a result, an asserted "the test works" is not.

### Existing tests

- **Stays green, unmodified, and AC 3 says so explicitly:** `TestRegistry_AdmitRefusedRepeatUnderAHeldPair_KeepsTheIncumbent`. Being held exempts a pair from the *refusal* only; it does not short-circuit the declaration checks. Also unmodified: `TestRegistry_AdmitRefusedDeclaration_StoresNothing`, `TestRegistry_AdmitAfterARefusal_AdmitsTheSamePair`, `TestRegistry_Admit_HoldsAnAccumulatorLatchedWithTheDeclaration`, `TestRegistry_AdmitUnderAHeldPair_KeepsTheIncumbent` — all go through `Admit` and hold at most two entries.
- **Arity-only edits, 7 sites:** the destructuring `r.insert(...)` calls in `TestRegistry_InsertThenLookup_ReturnsTheSameAccumulator`, `TestRegistry_SameAttachmentIDOnTwoConns_AreSeparateEntries` (×2), `TestRegistry_SecondInsertUnderAHeldPair_KeepsTheIncumbent` (×2), `TestRegistry_ConcurrentSamePairInsert_TellsExactlyOneItIsFresh`, and `TestRegistry_ConcurrentMixedOperations_AreSafe`. At every one of these the registry provably holds fewer than `maxInFlightUploads` entries, so the error is nil by construction; discard it with `_` and state that reason **once**, in the file-level comment block that already explains the concurrency-test conventions — not seven times.
- **One behavioural edit:** `TestRegistry_ConcurrentMixedOperations_AreSafe` fans 32 distinct pairs out on one conn plus a shared pair on another, so it peaks above the bound by design and its "reported a repeat" assertion would now misfire. Change it to accept the two legal outcomes for a distinct pair — inserted, or refused with `ErrTooManyUploads` — treat `!inserted && err == nil` (a repeat) as the failure it already was, guard the `Lookup` assertion on `inserted`, leave `Release` unconditional (releasing an absent pair is already a tested no-op), and keep the final `count() == 0`. Its subject is safety under mixed concurrent operations; the cap is now part of that subject rather than an obstacle to it.

`make check` must be green. `go vet` and `staticcheck` will flag an unused third return value or an unhandled error before the tests do.

## Open questions

- **The number itself is 4, and it is a policy pick, not a derivation.** It comes from `maxUploadBytes`' stated budget ("hold that PRODUCT at or under roughly 64 MiB"), which fixes it once the byte bound is 16 MiB. If operational experience wants more concurrency, the doc already says the answer is to move one of the two numbers.
- **Publishing the bound so a client can pre-flight** is deliberately not this slice's — `#1751` § Open questions 3 anticipates a later ticket that publishes it alongside the byte bound. The constant stays unexported.
- **A per-conn sub-cap under the daemon-wide ceiling** is a later refinement (see `Security review`, Resource exhaustion). Not this family's.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new boundary. The two untrusted values crossing into this package are `connID` (daemon-assigned, not client-chosen) and `attachmentID` (client-chosen, JSON-decoded upstream). Both remain map-key material only: `uploadKey`'s doc already records that membership certifies nothing, and this slice adds no parsing, no canonicalisation and no path use of either. The one *new* untrusted-adjacent value is `len(r.uploads)`, which is derived from daemon state rather than from a client claim — no number a client sends is ever compared against `maxInFlightUploads`, which is what keeps the gate un-spoofable. Decoding a payload into these scalars stays #1744's job.
- **[Tokens, secrets, credentials]** Not applicable by design: this slice creates, stores, compares and expires nothing secret. The only secret-adjacent string on the path is the declared `sha256`, which `Admit` copies into the accumulator uninspected and which `capacityRefusal` structurally cannot see.
- **[File operations]** Not applicable: in-memory only. `EnsureDir` and `Store` are untouched, and nothing in this slice takes a filesystem path. The `attachment_id` → path-component conversion, which is where traversal would live, is #1781's and runs elsewhere.
- **[Subprocess / external command execution]** Not applicable: no `exec`, no environment access, no signals.
- **[Cryptographic primitives]** Not applicable: no RNG, no comparison against a secret. The digest comparison this package does perform is `Assemble`'s and is untouched.
- **[Network & I/O]** No findings, and this slice *is* the resource-exhaustion control the category asks for: it is the missing cap on entry count, complementing the per-upload byte cap. See the next finding for what it does not cover.
- **[Error messages, logs, telemetry]** **The main finding, and the design addresses it structurally.** `insertLocked` is the first place on the admission path that holds the banned `attachmentID` *and* needs a format string, so the never-log rule stated in `protocol.AttachmentChunkPayload`'s SECURITY block would degrade from a structural guarantee to a discipline. The design keeps it structural at the site that matters by giving the format string its own function, `capacityRefusal`, whose only parameter is an `int` — the same argument `CheckDeclaration`'s scalars-only signature makes. Two numbers reach the message and nothing else; the test asserts both prohibitions plus the bound's presence as the control against a swallowed message. This package still makes zero log calls, and the sentinel-to-wire-code mapping stays #1744's.
- **[Concurrency]** No findings on lock ordering: `mu` remains a single leaf lock, no second lock is introduced, and no lock is held across a call into `Accumulator`. The TOCTOU question the category asks is this ticket's AC 4, and it is answered structurally — the gate lives inside `insertLocked`, which runs under the acquisition `Admit` already holds, so look-up, count and store are one critical section. Two hazards are called out in the design because they are the ways a developer would reintroduce the split: calling `r.count()` from `insertLocked` (deadlock, not a race — `sync.Mutex` is not reentrant), and hoisting the count read into `Admit` ahead of the lock (the exact mutant AC 4's test is measured against). No goroutines are spawned, so there is no lifecycle or shutdown exposure; a process killed mid-`Admit` loses only in-memory state that was never durable.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Attachments treats `attachment_id` as "not a capability", which is what makes an entry cap necessary rather than optional, and § Error codes marks `attachment.too_many_uploads` transient with a documented back-off obligation — matched here by keeping the refusal a separate sentinel from the permanent `ErrUploadTooLarge` rather than folding them. **OUT OF SCOPE, named:** a daemon-wide cap lets one conn occupy every slot and starve its peers. Accepted for this slice — peers are the user's own paired devices, the refusal clears transiently, and abandoned slots are reclaimed by #1742's reaper — and a per-conn sub-cap *under* this ceiling is the refinement that would close it. Also out of scope and already sequenced: this ticket blocks #1744, which must not land ahead of it, or the dispatch site would go live with an uncapped entry count.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25
