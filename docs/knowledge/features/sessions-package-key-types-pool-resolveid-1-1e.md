# Pool.ResolveID (1.1e-A)

The typed prefix resolver Phase 1.1e-B's `pyry attach <id>` wire + CLI surface (and any future verb taking a session selector) consumes instead of inlining the same `strings.HasPrefix` walk over `Pool.List`. The natural pairing with `Pool.Lookup`:

| Caller-supplied input | API |
|---|---|
| canonical `SessionID` (or `""`) | `Pool.Lookup(id)` |
| user input string (UUID, prefix, or `""`) | `Pool.ResolveID(arg)` |

```go
var ErrAmbiguousSessionID = errors.New("sessions: ambiguous session id")

func (p *Pool) ResolveID(arg string) (SessionID, error)
```

**Resolution order, all under `Pool.mu` (RLock):**

1. `arg == ""` ⇒ bootstrap id, no error. Same seam as `Pool.Lookup("")`.
2. `arg` is an exact key in the in-memory `p.sessions` map ⇒ that id, no error. Single map lookup; the short-circuit never falls through to the prefix scan, so a full-UUID match always wins over any coincidental prefix overlap with no extra scan cost.
3. Scan `p.sessions`, collect every `*Session` whose `SessionID` has `arg` as a prefix (`strings.HasPrefix`). Exactly one match ⇒ that id. Zero ⇒ `ErrSessionNotFound` (reused). ≥2 ⇒ `ambiguousError(matches)`.

**Sentinel + `fmt.Errorf("%w: …")` over a struct error type.** The AC required "the simpler shape that keeps `errors.Is` matching cheap" and "no new exported types beyond the new typed error." A struct error with `Matches []SessionRef` would expose a second exported type and force every consumer to either type-assert or duplicate the formatting. The chosen shape — `var ErrAmbiguousSessionID = errors.New(...)` plus `fmt.Errorf("%w:\n%s", ErrAmbiguousSessionID, lines)` — gives `errors.Is` one pointer compare via the wrapped chain, lets the CLI consumer `fmt.Fprintln(os.Stderr, err)` and get a human-readable list verbatim, and stays within the AC's exported-surface budget.

**Match list formatting.** Sorted by `SessionID` ascending (same tiebreak as `Pool.List`) so the error message is deterministic and tests can pin the exact substring. Each line is `<uuid> (<label>)`. The synthetic `"bootstrap"` substitution from `Pool.List` is mirrored one-for-one — when the bootstrap entry's on-disk label is empty, the formatter writes `<uuid> (bootstrap)` rather than `<uuid> ()`. Otherwise operators would see one name in `pyry sessions ls` and another in the disambiguation prompt.

**No `Session.lcMu` taken.** `ResolveID` reads only `id` (mutated by `RotateID` under `Pool.mu` write — invariant documented at that site) and `label` (mutated by `Pool.Rename` under `Pool.mu` write). Both sit under `Pool.mu`'s reader set; no new lock-order edges. `lcState` and `lastActiveAt` are not consulted — `ResolveID` does **not** filter by lifecycle state. An evicted session is still a registry entry, and `pyry sessions rename <prefix>` / `pyry sessions rm <prefix>` must still resolve it. Filtering, if any, is a verb-layer policy (e.g. a future `pyry attach` may bounce active-vs-idle differently).

**No minimum prefix length.** A one-character prefix is accepted as long as it is unique. Refusing short prefixes (1- or 2-char) for safety is a CLI-layer guard, not a pool invariant — the same posture as `Pool.Rename` declining to validate `newLabel` and `Pool.Create` declining to validate `label`.

**No whitespace trimming.** The pool primitive accepts whatever string the caller hands it. Trimming is the CLI's responsibility (`flag` already handles positional args; an explicit `strings.TrimSpace` at the CLI layer is one line).

**Returns `SessionID`, not `*Session` / `SessionInfo`.** Smallest possible surface, symmetric with the rest of the wire/CLI flow: 1.1e-B unmarshals an id from the request, calls `ResolveID`, then routes to `Lookup` / `Activate` / `Remove` with the resolved id. Returning `*Session` would tempt callers to short-circuit the second lookup — but the second lookup is the lock-clean way to guard against a session being removed between resolve and use, and saving the second hashmap probe is not worth the sharp edge.

**Concurrency.** `Pool.mu` (RLock) for the whole call. Concurrent `ResolveID + List` share the read lock and run truly concurrently. Concurrent `ResolveID + writer` (`Rename` / `Create` / `Remove` / `RotateID`) blocks briefly behind the writer and observes either pre- or post-write state — the same race-clean shape callers were already prepared for. Concurrent `ResolveID + ResolveID` share the read lock with no contention. The lock-order graph (`Pool.capMu → Pool.mu → Session.lcMu`) is unchanged.

**A session removed mid-resolve** either appears in the scan (caller then races on the second `Lookup` and sees `ErrSessionNotFound`) or doesn't — both outcomes are valid races the consumer was already prepared for. No error wrapping prefix is added (the wrapped sentinel already begins with `sessions: …`; double-wrapping would just produce `sessions: resolve: sessions: …`).

**Out of scope** (handed to 1.1e-B): wire protocol field carrying the resolved/unresolved id, control verb routing, CLI argument parsing, refactoring 47-B / 48-B's inlined prefix resolvers (opportunistic when those callers are next touched).
