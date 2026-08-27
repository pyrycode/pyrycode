# #1787 — a conn-keyed registry of in-flight attachment uploads

## Files to read first

- `internal/attachments/accumulator.go` → package doc — the "resource bounds are owned three separate ways" paragraph; this slice owns none of them.
- `internal/attachments/accumulator.go` → `Accumulator` (type doc) — the exact sentence AC 4 rewrites, and the mutex-free contract the registry must not widen.
- `internal/attachments/accumulator.go` → `NewAccumulator`, `Add`, `Assemble` — the signatures the registry stores and hands back. It calls none of them.
- `internal/attachments/accumulator_test.go` → `newTestAccumulator`, `testChunk`, `testParts`, `testTotal`, `testFixture` — reuse these; tests are in-package, so `registry_test.go` gets them for free. Do not build a second fixture.
- `internal/permbridge/permbridge.go` → `Registry`, `Registry.Register` — the nearest in-repo shape for this whole slice: `mu sync.Mutex` + map, a leaf-lock doc note, and one critical section spanning check-then-write. Mirror the locking shape; the contract differs (see § Design).
- `internal/modalbridge/modal.go` → `Registry` — second instance of the same house shape, including the "carries a sync.Mutex because two real goroutines touch it" doc framing.
- `internal/sessions/pool.go` → `Pool.capMu` — the precedent the ticket names: a lock whose doc states *the sequence it serializes* and its lock order, which is what let a later caller re-use it rather than wrap it.
- `internal/relay/v2session.go` → `V2SessionManager.appFrameWorker` — "no two handlers for one conn are handled concurrently", the per-conn guarantee that makes off-lock feeding sound.
- `internal/protocol/envelope.go` → `RoutingEnvelope` — `ConnID` is where #1744's conn identity comes from. This package never imports it; read it only to see the shape of the string being passed in.
- `docs/knowledge/features/attachments-package.md` § "Sentinels and discard semantics" — the "presence comes from map-key membership, never from the stored value" lesson, which decides `Lookup`'s signature.
- `docs/knowledge/features/attachments-package.md` § "Blocked family (not landed)" — sketches this registry as `attachment_id → *Accumulator`. That half is superseded by the ticket; the "needs its own lock" half is not. Do not correct the doc — that is the documentation phase's.
- `docs/protocol-mobile.md` § Attachments, the `attachment_id` row — "**Not a capability**", the premise the conn-in-the-key decision rests on.

## Context

`internal/attachments` can build an `Accumulator` per transfer and has nowhere to put one. `NewAccumulator` returns a value the package never stores and no production code calls, so every chunk after the first has nothing to find. This slice ships the container: a map of in-flight uploads that synchronises itself, with an insert that never displaces a live transfer, a lookup, a release and a count.

It ships with no admission policy (#1788) and no ceiling on entries (#1786), and that exposes no unbounded state on `main`: nothing reaches this registry until #1744 wires the dispatch site, and #1744 lands last in the family.

No ADR is warranted. The two decisions worth recording — the conn in the key, and the single lock spanning the whole operation — belong in the two type docs the code itself carries, and the documentation phase will fold them into the package overview.

## Design

One new file, `internal/attachments/registry.go`, holding seven declarations. Everything is in-memory: no disk, no wire codes, no logger, no `context.Context` (nothing here blocks).

### The key

```go
// uploadKey ... (doc paragraph #1 — see § Prose budget)
type uploadKey struct {
	connID       string
	attachmentID string
}
```

A comparable struct, unexported, never leaving the package. Both fields are opaque scalars: the registry stores and compares them and parses neither, so no length check, no canonical-shape check, no `AttachmentID` validation (that is #1781's, where the id becomes a path component).

Two consequences a later reader must not get backwards, and both belong in the doc: **membership certifies nothing.** That a pair is in this map says only that something inserted it — not that the id is canonical, bounded, or safe to make a path component. And the key's bytes are attacker-influenced on the `attachmentID` half, so what bounds their total is the entry cap (#1786), not anything here.

Two alternatives are wrong and both are reachable by a well-meaning reader:

- **The bare `attachment_id`** — what the package overview sketches. `docs/protocol-mobile.md` § Attachments says the id is "**Not a capability** — not secret, not unguessable". Keyed by it alone, that string alone decides which `Accumulator` a chunk lands in, across conns. Two conns colliding on one id share one pointer; `Accumulator` carries no mutex by design, so that is a data race *and* a path for one conn's bytes into another conn's file. The collision is not hypothetical: a phone that drops mid-upload and reconnects re-sends the same id while the old session may not have torn down.
- **A concatenated string key** (`connID + attachmentID`, with or without a separator) — reintroduces the collision it was meant to close (`"ab"+"c"` and `"a"+"bc"` are one key) and makes the registry a parser of a value it is documented not to parse. A struct key forecloses both structurally.

**Do not write an in-package test that claims to catch a swapped parameter order.** Both fields are opaque strings used only as key components; a consistent swap satisfies every assertion this package can make. The pin belongs at #1744's single dispatch site, where the two values have different provenance.

### The registry

```go
// Registry ... (doc paragraph #2 — see § Prose budget)
type Registry struct {
	mu      sync.Mutex
	uploads map[uploadKey]*Accumulator
}

func NewRegistry() *Registry
```

`NewRegistry`, not `New` — this package already has `NewAccumulator`, so the bare `New` of `permbridge` and `modalbridge` (single-type packages) does not apply.

`sync.Mutex`, not `sync.RWMutex`: every operation here mutates or must be exclusive-with-a-mutation, an `RWMutex` invites exactly the RLock-read-then-Lock-write shape AC 3 forbids, and the map is bounded and uncontended.

Not `sync.Map`: it gives no atomic count, and its `LoadOrStore` cannot be composed with the count-then-admit gate #1786 needs.

### The four methods

Each takes the lock once, for its whole body, with `defer r.mu.Unlock()`.

| Method | Signature | Behaviour |
|---|---|---|
| insert | `func (r *Registry) insert(connID, attachmentID string, a *Accumulator) (upload *Accumulator, inserted bool)` | Stores `a` under the pair and answers `(a, true)` only when the pair is absent; when it is present, stores nothing and answers `(incumbent, false)`. |
| Lookup | `func (r *Registry) Lookup(connID, attachmentID string) (*Accumulator, bool)` | The transfer in flight under the pair, comma-ok. |
| Release | `func (r *Registry) Release(connID, attachmentID string)` | Deletes exactly the pair's entry. Releasing a pair the registry does not hold is a no-op. |
| count | `func (r *Registry) count() int` | How many uploads are in flight. |

Decisions each of these encodes, in one line apiece:

- **`insert` is unexported.** #1788 adds the admission entry point that runs `CheckDeclaration` and `CheckDeclaredSize` before constructing an `Accumulator`, and is meant to be the only exported way an upload enters. Exporting the raw insert now would ship a bypass around admission that #1788 has to un-ship. Between the two tickets it has no production caller and only tests reach it; `make check` stays green through that window (staticcheck's `unused` merges the test variant's object graph before colouring, and Go never errors on an unused package-level declaration).
- **`inserted` is the inverse of `sync.Map.LoadOrStore`'s `loaded`.** True means *fresh*. Say so in the doc — a reader porting intuition from `sync.Map` inverts it, and the inverted branch is a silent double-admission at #1786.
- **`insert` takes a non-nil `*Accumulator` as a precondition, unguarded.** It is unexported with one in-package caller (#1788) that constructs the value it passes. No nil check, no sentinel; the doc states the precondition.
- **`Lookup` is comma-ok, not nil-means-absent.** This package's own measured lesson — presence comes from map-key membership, never from the stored value (see the package overview § "Sentinels and discard semantics"). The bool is what #1784 turns into "chunk for an unknown transfer".
- **Never-replace is a security property, not just hygiene.** It is what stops a mid-transfer re-declaration: a second first-chunk for a live pair, carrying a different declared `size` or `sha256`, gets its freshly-built `Accumulator` dropped rather than installed, so the numbers a transfer was admitted under cannot be swapped underneath the bytes already held.
- **`Release` returns nothing.** #1784 releases what it just looked up and #1742 decides *when* to call it; neither needs a presence answer, and a bool return invites a caller to branch on it outside the lock. Adding one later, if #1742's reaper wants it, is additive.
- **`count` is unexported.** In-package tests read it (AC 3's "a count a test can read") and `make check` colours it used. Exporting it would publish exactly the TOCTOU shape #1786 must not be built on — `if reg.Count() < cap { reg.Admit(...) }` — and a status verb that ever wants it can export a reader then.

### The `accumulator.go` edit (AC 4)

One sentence in `Accumulator`'s type doc. Today:

> Synchronising the registry of in-flight uploads belongs to the slices that build it (#1778, #1744) and to the release path (#1742), never to this type.

Becomes:

> Synchronising the registry of in-flight uploads belongs to `Registry`, which holds its own mutex across each whole operation, the release path included — never to this type.

All three numbers go. Every one of them was there to defer the same question — who synchronises the registry — and this slice answers it: the registry's own lock does, the release path included, because the release primitive ships here and #1742 only decides when to call it, while #1744 calls the registry's methods rather than synchronising it.

Verify, don't trust the counts — re-run each before and after:

- `grep -rn 1778 internal/attachments/` — five lines across four comments before, four after. The other four (`accumulator.go`'s package doc, `maxUploadBytes` and `ErrUploadTooLarge` in `admission.go`) are #1786's to re-point and are **not** touched here.
- `grep -c 1744 internal/attachments/accumulator.go` and `grep -c 1742 …` — three each before, **two each after**. The survivors are about wire-code mapping and the reaper, are still true, and stay.

This is the opposite instruction from #1786's sweep AC, which keeps `#1744` and `#1742` inside the comments *it* edits. Correct there, wrong here.

## Concurrency model

No goroutines, no channels, no shutdown sequence — the registry is passive state. What it owns is one lock.

**One lock, held across the whole operation.** `mu` is taken once at the top of each method and released by `defer`. That is not merely "race-free": AC 3's same-pair clause is what distinguishes it from a registry that locks its read and its write separately, which passes `-race`, satisfies every other criterion, and is the exact shape #1786 cannot be built on. #1786's bound gate runs in front of the declaration checks, applies only to a pair the registry does not already hold, and needs look-up-then-count-then-insert to be indivisible. The lock chosen here is the one that will have to span it. `permbridge.Registry.Register` is the same critical section shape — check the map, then write it, without dropping the lock; the contract differs only in that a repeat here is an answer, not an error.

**Lock discipline, to state in `Registry`'s doc:** `mu` is a leaf. It is never held across a call into `Accumulator` — not `Add`, not `Assemble` — and never nested with another lock. **No method of `Registry` calls another**, because `sync.Mutex` is not reentrant and every one of the four takes `mu` for its whole body; that is the only deadlock this design admits, and the one a composing method reaches for first. #1786's gate is the concrete case: `Admit` wants count-then-insert, and the correct shape is one critical section reading `len(r.uploads)` and writing the map inline — not `count()` followed by `insert()`, which deadlocks on the first call and, if "fixed" by dropping the lock between them, becomes exactly the split-lock shape AC 3 exists to forbid.

**Feeding happens off-lock, and that is sound *because* the key carries the conn.** The registry hands the `*Accumulator` back and the caller feeds it with the lock released. Relay spawns exactly one `appFrameWorker` per session, and `V2SessionManager.appFrameWorker`'s own doc records that no two handlers for one conn run concurrently — so with the conn in the key, exactly one goroutine can ever reach any one accumulator. This is what preserves `Accumulator`'s mutex-free, fed-serially contract instead of quietly widening it. #1784 inherits this posture rather than picking a second one.

**`Accumulator` gains nothing.** No mutex field; neither `Add` nor `Assemble` acquires anything. After this slice, `sync` is imported by `registry.go` and by no other file in `internal/attachments` — the check is `grep -rln '"sync"' internal/attachments/` returning exactly `internal/attachments/registry.go`, one hit, which is a live control rather than a check that cannot fire.

**Consequence for the tests: `registry_test.go` must not import `sync`.** Coordinate the concurrency tests with channels — a `close(start)` gate and a result channel drained N times. This is not a contortion: the same-pair test needs each goroutine's result anyway, so the channel is the natural shape and the drain is the wait. Buffer the channel to N: an unbuffered one parks every goroutine that has not been drained yet, so an assertion failing mid-drain leaks the rest for the life of the test binary.

## Error handling

**No new sentinels, no new error returns.** Every operation here is total: an insert under a held pair is a normal answer (`inserted == false`), a lookup miss is a normal answer (`ok == false`), a release of an absent pair is a no-op. There is nothing to wrap, nothing to map to a wire code, and nothing to log.

The refusals that *look* like they belong here are all someone else's: what may enter is #1788's (`CheckDeclaration` + `CheckDeclaredSize`, run before an `Accumulator` is constructed); how many may exist is #1786's, whose sentinel maps to `CodeAttachmentTooManyUploads`; a chunk for a transfer the registry has never held is #1784's; expiry is #1742's. This slice adding any of them would move a fence.

The one hazard the design closes by construction rather than by a check: an insert that replaced a live entry would drop an in-flight transfer's accumulated bytes on the floor and silently start it over. `insert`'s never-replace rule is that check.

## Testing strategy

`internal/attachments/registry_test.go`, in-package, reusing `newTestAccumulator`, `testChunk`, `testParts` and `testTotal`. `t.Parallel()` where it applies. Scenarios, not test bodies:

1. **Insert then lookup returns the same accumulator.** Insert `(A, X, acc)`; `Lookup(A, X)` answers `ok` and the *same pointer* (compare pointers, not field values — a fresh `Accumulator` with identical fields is the thing this excludes); `count() == 1`.
2. **The same id on two conns is two entries.** Insert `(A, X, accA)` and `(B, X, accB)`; both report `inserted == true`; `count() == 2`; each lookup answers its own pointer. Then feed `accA` chunk 0 and assert feeding `accB` chunk 0 **succeeds** rather than answering `ErrDuplicateIndex` — the sharpest available statement that neither conn can feed the other's transfer, since a shared pointer would make the second `Add` a duplicate.
3. **A second insert under a held pair keeps the incumbent.** Insert `(A, X, acc1)`; feed `acc1` one chunk; insert `(A, X, acc2)` answers `(acc1, false)`; `count() == 1`; `Lookup` still answers `acc1`; feeding `acc1` the remaining chunks still `Assemble`s to `testFixture` — the chunk it held survived the repeat.
4. **Release removes exactly its entry.** Insert `(A, X)` and `(B, X)`; `Release(A, X)`; `Lookup(A, X)` answers `ok == false`; `count() == 1`; `Lookup(B, X)` still answers its own accumulator. Then `Release(A, X)` a second time: no panic, `count()` unchanged.
5. **Mixed concurrent operations are safe under `-race`.** N goroutines released by one `close(start)`, each inserting, looking up and releasing its own distinct pair, with a few readers hitting a shared pair. Drain N results from a buffered channel; final `count() == 0`. Asserts safety, not ordering.
6. **Concurrent same-pair insert — AC 3's pin, and the only test that has one.** N ≥ 32 goroutines, each constructing its own `Accumulator`, all released by one `close(start)`, each calling `insert(A, X, own)` and reporting `{own, got, inserted}` on a buffered channel. Assert: exactly one `inserted == true`; that winner's `got == own`; **every** goroutine's `got` equals the winner's `own`; `count() == 1`.

**Sole-redness note for code review.** Scenario 6 is the sole red for the split-lock mutant — rewriting `insert` to look up under one acquisition and store under another. Every other scenario stays green under it, and `-race` reports nothing, which is the whole point of the criterion. Its redness is probabilistic rather than certain: measure it with `-count=5` and a start gate, and read the `exactly one inserted` assertion as the failing one. Scenario 3 is the sole red for a mutant that replaces the incumbent; scenario 2 for a mutant that drops `connID` from `uploadKey`; scenario 4 for one that clears the map instead of deleting one key. Per this package's measured mutation lessons, grep any overlay run's output for `build failed` and `declared and not used` before trusting a green verdict.

## Prose budget — the fence this slice is most likely to break

This package's doc-to-code ratio runs 4:1 to 7:1, and the nearest analogue (#1777) overran its line estimate by 42% on doc-paragraph growth alone. The estimate here is ~180 for `registry.go`, ~190 for `registry_test.go`, 3 for the `accumulator.go` edit. If it grows past 400, the growth will be prose.

So the doc budget is fixed in advance:

- **Two paragraph-length docs, ≤ 12 lines each.** `uploadKey`'s — why the conn is in the key. `Registry`'s — why the lock is the registry's own, what sequence it serializes, and that it is a leaf never held across `Add` or `Assemble`.
- **One sentence each** for `NewRegistry`, `Lookup`, `Release` and `count`. `insert` gets three at most: the never-replace rule, the `inserted`-is-not-`loaded` warning, and the non-nil precondition.
- If you find yourself writing a third essay — a derivation of the key space, a re-litigation of the byte bound, a peak-memory calculation — stop. Those already exist in `admission.go` and in the package overview, and re-deriving them here is the overrun.

Cite by symbol in every comment: `Pool.capMu`, `appFrameWorker`, `Accumulator`, `CheckDeclaration`. `make cite-guard` runs inside `make check` and fails the build on a `foo.go:123` in a `//` comment, at any depth, with no range exemption.

## Open questions

- **Per-conn teardown (#1742).** A dropped conn's uploads have to be reaped, and a flat composite key makes that an O(n) scan rather than one delete on a nested `map[connID]map[attachmentID]*Accumulator`. Resolved in favour of the flat key: the map is bounded by #1786's cap (single digits), the nested form needs empty-inner-map cleanup that is its own bug surface, and #1742 can add a sweep method without changing this key. Not an open question for the developer — do not build the nested map.
- **What #1742's reaper needs from this type** — a bulk sweep taking `mu` once, most likely, rather than repeated `Release` calls. Nothing here forecloses it; do not add it. But the off-lock-feeding posture puts one constraint on it that is easier to state now than to debug later: **whatever the reaper reads to decide staleness must live in the registry's own entry, under `mu` — never on `Accumulator`.** Release only deletes a map entry, so a reaper and a session worker can hold the same `*Accumulator` at once; the worker is the sole goroutine that touches it, and a reaper that reads a field like `received` off it to judge idleness breaks that and races the worker's `Add`. The registry entry is where a `lastChunkAt` belongs, which means #1742 widens the map's value type rather than `Accumulator`.
- **A per-conn sub-cap for #1786** — the conn is already in the key, so counting one conn's in-flight uploads is a scan of a cap-bounded map, and a global cap alone lets one conn pin every slot until #1742's expiry frees them. A bare-id key would have foreclosed the option; this one leaves it open. Not this slice's to build.
- **Whether `count` ever becomes exported** — only if a status verb wants an in-flight-upload gauge. Additive when it happens; not now.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] **No findings, after one revision.** Two values cross into the registry with different provenance: `attachmentID` is an attacker-supplied string JSON-decoded from `AttachmentChunkPayload`, `connID` is the relay's own per-connection identity from `RoutingEnvelope`. The registry treats both as opaque — stores, compares, parses neither, logs neither, makes neither a path component. The first pass found the boundary under-stated rather than wrong: the spec framed "no validation here" purely as a scope fence, which leaves a later reader free to read map membership as a validation certificate on the way into #1782's storage. Revised — `uploadKey`'s doc now has to say membership certifies nothing about the id's shape, and that #1781 owns the canonical-shape check that must run before the id becomes a path component.
- [Trust boundaries] **No finding — the never-replace rule is load-bearing here, not hygiene.** A second first-chunk on a live pair, declaring a different `size` or `sha256`, has its freshly-constructed `Accumulator` dropped rather than installed, so a transfer's admitted numbers cannot be swapped underneath the bytes already held. Spec now states this as the security property it is, so a later "simplify" that makes insert overwrite reads as the regression it would be.
- [Tokens, secrets, credentials] **Not applicable, by construction.** Nothing here is a credential: `docs/protocol-mobile.md` § Attachments states `attachment_id` is "**Not a capability**", and this slice mints nothing, stores nothing to disk, and never imports `RoutingEnvelope` — so the `Token` field sitting beside `ConnID`, whose own SECURITY note bans logging it, is not reachable from this package. The registry makes zero log calls; logging an attachment or conn id is #1744's, which `accumulator.go`'s package doc already names as the only site that knows either.
- [File operations] **Not applicable — in-memory only.** No path is constructed, no file opened, no `os.Stat`-then-open. The one live hazard is the confused-developer path into #1782's storage, closed by the trust-boundary revision above.
- [Subprocess execution] **Not applicable.** The package makes no external call of any kind and adds no import beyond `sync`.
- [Cryptographic primitives] **Not applicable — no primitive is used and none is needed.** The registry generates no ids (every key component is caller-supplied), and its comparisons are ordinary map lookups on a value the published contract declares non-secret, so `crypto/subtle` would protect nothing. Timing reveals only whether the caller's own pair is in flight, and never across conns: an attacker on one conn cannot probe another's entries at all, because the key differs.
- [Network & I/O] **OUT OF SCOPE, with owners.** Three exhaustion vectors touch this registry and none is this slice's: how many entries may exist is #1786's, one upload's retained bytes is `maxUploadBytes` (landed, #1777), and entries abandoned by a vanished conn are #1742's expiry. Attacker-influenced key bytes are bounded by the entry cap, so they are #1786's too. This exposes nothing on `main`: verified, not inherited — `grep -rln "internal/attachments" --include='*.go'` outside the package returns nothing, so the daemon cannot reach any of it until #1744 wires the dispatch site, and #1744 lands last in the family.
- [Error messages, logs, telemetry] **No findings.** The design adds no sentinel, no error return and no log call, so there is nothing that could carry an `attachment_id` — the same attacker-supplied, JSON-decoded string this package already bans from `Data` and `Filename` error strings — into #1744's line-oriented log.
- [Concurrency] **SHOULD FIX, folded into the spec.** The core TOCTOU is the ticket's own subject and is closed: one lock, taken once, spanning check-then-mutate, with the split-lock shape named as the thing AC 3 forbids and scenario 6 as its sole red. Two adjacent hazards the first pass found and the revision now states. (a) Every method takes `mu` for its whole body and `sync.Mutex` is not reentrant, so no method may call another — the concrete trap is #1786's `Admit` reaching for `count()` then `insert()`, which deadlocks and, if "fixed" by unlocking between them, silently becomes the forbidden split-lock. (b) `Release` deletes a map entry while a session worker may still hold the same `*Accumulator`, which is safe only while the worker is the sole goroutine touching it — so #1742's reaper must keep staleness metadata in the registry entry under `mu` and must never read a field off `Accumulator` to judge idleness. Both are now written down where #1786 and #1742 will read them. Goroutine lifecycle: none spawned by the design; the tests' fan-out is joined by draining a channel buffered to N, so a mid-drain assertion failure cannot leak the rest.
- [Threat model alignment] **No findings — the design is a direct answer to one published rule.** § Attachments' "not a capability, and never the only thing standing between a caller and a file" is exactly what a bare-`attachment_id` key would violate, by letting one attacker-chosen string decide which mutex-free `Accumulator` a chunk lands in across conns — a data race and a cross-conn byte path in one. Keying by the pair also aligns the registry's isolation unit with the one the crypto layer already uses: § Security model derives keys per handshake so one session's ciphertext cannot replay into another, and `conn_id` is that session's routing identity. Threats deliberately left to named owners: entry-count exhaustion (#1786), per-conn fairness under a global cap (#1786 + #1742), abandoned-upload reclamation (#1742), canonical id shape before path use (#1781), and the key's parameter-order pin, which can only be made at #1744's dispatch site where the two strings have different provenance.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25
