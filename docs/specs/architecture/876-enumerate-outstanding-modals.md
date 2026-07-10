# Spec #876 — Enumerate outstanding modals as re-emittable `modal_shown` payloads

**Ticket:** [#876](https://github.com/pyrycode/pyrycode/issues/876) · **Size:** XS · **Security-sensitive:** no
**Consumer follow-up:** #877 (connect-time reconnect producer) — out of scope here.

## Files to read first

- `internal/modalbridge/modal.go:75-93` — `Outstanding` struct + `Registry` struct. The load-bearing fact: `Outstanding` holds **exactly** the six `ModalShownPayload` fields (`ModalID, Class, Title, Prompt, Options, DefaultOptionID`).
- `internal/modalbridge/modal.go:145-164` — `Record`. Note it stores `Outstanding` from the *already-built* payload `p` (`p.Class`, `p.Title`, … , `slices.Clone(p.Options)`, plus the minted id). The stored `Outstanding` is a flattened snapshot of the payload — this is why enumeration is a direct field-copy, not a re-derivation.
- `internal/modalbridge/modal.go:166-205` — `Lookup` / `Resolve` (the leaf-lock read/consume idiom to mirror) and `buildPayload` (what the technical note calls the "inverse" — but see Design: you do **not** call it).
- `internal/protocol/messaging.go:95-125` — `ModalOption` and `ModalShownPayload` field/JSON contract (the return type).
- `internal/modalbridge/modal_test.go:13-39` — existing test helpers `optionIDs`, `slicesEqual` to reuse; `conversations.ValidID` usage at :117 is the id-shape check pattern.
- `internal/modalbridge/modal_test.go:103-139, 276-299` — `TestRecord_MintsAndStores` and `TestResolve`: the record→read and record→resolve→miss flows the new tests build on.

## Context

The reconnect direction (#829, ADR 025) reconciles a client to **current control truth** on every (re)connection instead of replaying history: a still-pending modal is re-sent under its original stable id so a reconnecting client can match-and-replace rather than duplicate; an already-resolved modal is silently absent.

`modalbridge.Registry` retains every outstanding modal (`Record` writes, `Resolve` retires) but exposes only **id-keyed** reads (`Lookup`, `Resolve`). Nothing can ask *"what is outstanding right now?"*. This ticket adds that one read seam and nothing else. The producer that consumes it and re-sends over the relay is **#877** — no producer, wire, or session-manager change here.

## Design

Add one exported method to `Registry` in `internal/modalbridge/modal.go`:

```go
// Snapshot returns a marshal-ready ModalShownPayload for every currently-
// outstanding modal, each stamped with its original modal_id. Pure read: mints
// no id, retires nothing, mutates no registry state. Map-walk order is
// unspecified — callers reconcile by modal_id, never by position.
func (r *Registry) Snapshot() []protocol.ModalShownPayload
```

**Behavior (contract):**

- Walk `r.outstanding` under `r.mu` (whole walk inside one lock acquisition, mirroring `Lookup`/`Resolve` — this is a leaf lock, no nesting).
- For each `Outstanding o`, build `protocol.ModalShownPayload{ModalID: o.ModalID, Class: o.Class, Title: o.Title, Prompt: o.Prompt, Options: slices.Clone(o.Options), DefaultOptionID: o.DefaultOptionID}`. This is a **direct field copy** — `Outstanding` already *is* the built payload's fields (see `Record`). **Do not** call `buildPayload` or reconstruct via `PermissionRequest`: that would need to un-map `Kind`, `titleByClass`, and `denyByClass`, is lossy, and re-derives data the registry already stores verbatim. The technical note's "reverses `buildPayload`" describes the net effect, not the mechanism.
- **Clone `Options`** per payload (`slices.Clone`, already imported). The stored `Outstanding.Options` slice is registry-internal state; returning an alias would let a caller mutate it and corrupt a later `Lookup`/`Resolve`. This is what keeps the "leaves registry state unchanged" invariant true even under a mutating consumer. Mirrors `Record`'s clone-on-write.
- Never mint or rotate an id; `newModalID` is not touched. The one-time-nonce and #717 deny-on-timeout semantics are unaffected — enumeration is a read.

**Return shape:** preallocate `make([]protocol.ModalShownPayload, 0, len(r.outstanding))` while holding the lock. An empty registry yields a non-nil, zero-length slice (avoids nil/empty ambiguity for the consumer; `len == 0` regardless).

**Naming:** `Snapshot` — noun for a point-in-time read of shared state, aligns with the "current-truth" framing and reads well beside `Lookup`/`Resolve`/`Record`. (Alternative considered: `OutstandingPayloads`; rejected as longer with no clarity gain given the typed return.) The developer may keep `Snapshot`.

No new types, no new files, no signature change to any existing symbol. Additive method only.

## Concurrency model

Single leaf mutex `r.mu`, held for the full O(n) map walk — identical discipline to `Lookup`/`Resolve`. `n` is the count of outstanding modals (realistically 0 or 1). No RWMutex (out of scope; contention is nil and the existing type uses a plain `sync.Mutex`). `slices.Clone` runs inside the lock; it is cheap and touches no other lock, so the leaf-lock invariant holds. Two real goroutines already touch this registry (surfacer via `Record`; #717 relay-dispatch via `Lookup`/`Resolve`); `Snapshot` is a third read path guarded by the same lock — `go test -race` covers it.

## Error handling

None. `Snapshot` has no failure mode: no RNG (the only error source in `Record`), no I/O, no allocation that can fail meaningfully. Return `[]protocol.ModalShownPayload` with **no** error — do not add an `error` return "for symmetry".

## Testing strategy

Add to `internal/modalbridge/modal_test.go`, reusing `optionIDs` / `slicesEqual` and the `New()` + `PermissionRequestForClass` + `Record` setup already in the file. Scenarios (bullet-level; developer writes the bodies in the file's table-driven idiom):

- **Empty registry → zero payloads.** `New().Snapshot()` returns `len == 0`. (AC2)
- **Recorded-then-resolved → zero payloads.** `Record` one modal, `Resolve` it, then `Snapshot()` returns `len == 0` — retired modals never re-surface. (AC3)
- **Single outstanding modal → equivalent payload.** `Record` returns `payload`; `Snapshot()` returns exactly one entry whose `ModalID`, `Class`, `Title`, `Prompt`, `DefaultOptionID` equal `payload`'s and whose `optionIDs` match `payload`'s. This is the round-trip-equivalence AC. (AC4)
- **Multiple outstanding → all present, keyed by id, order-independent.** `Record` two modals (permission + trust); `Snapshot()` returns both. Assert by building a `map[modalID]ModalShownPayload` from the result and checking each expected id is present with the right `Class` — **do not assert slice order** (map-walk order is unspecified; an order-dependent assertion is flaky).
- **Pure read — state undisturbed.** After `Snapshot()`, a subsequent `Lookup(id)` still hits and `Resolve(id)` still succeeds for a modal that was in the snapshot; and mutating the returned payload's `Options` slice does not affect a later `Lookup` of the same modal (proves the clone). (AC5)

Run `go test -race ./internal/modalbridge/` and `go vet ./...`.

## Open questions

- **Ordering guarantee.** Left unspecified (map-walk order). #877 reconciles by `modal_id`, so no order is needed. If a future consumer ever needs stable output, sort by `ModalID` at that call site — not here (YAGNI). Flagged only so the developer writes order-independent tests.
