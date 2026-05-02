# Phase 1.1e-A — `Pool.ResolveID` prefix resolver

**Ticket:** [#66](https://github.com/pyrycode/pyrycode/issues/66)
**Size:** XS (one method, one sentinel, one test file).
**Scope:** `internal/sessions` only. No control plane, no CLI, no wire protocol.

## Context

Phase 1.1's per-session CLI verbs (`pyry sessions rename`, `pyry sessions rm`,
soon `pyry attach <id>`) accept either a full UUID or a unique prefix. Without
a shared resolver each consumer would inline the same `strings.HasPrefix` walk
over `Pool.List` against its own local locking discipline. The ticket body
flags this explicitly: 47-B (#63) and 48-B (#65) are the first two consumers,
1.1e-B is the third — and the third caller is the extraction trigger.

`Pool.Lookup(id SessionID)` already exists and resolves a *full* canonical
`SessionID` (with `""` → bootstrap). What's missing is the loose form: an
operator-supplied string that may be a prefix, a full UUID, or empty. This
ticket adds that one method and the typed-error vocabulary it needs.

The natural pairing is:

| Caller-supplied input | API |
|---|---|
| canonical `SessionID` (or `""`) | `Pool.Lookup(id)` (existing) |
| user input string (UUID, prefix, or `""`) | `Pool.ResolveID(arg)` (this ticket) |

Consumers in 1.1e-B and beyond resolve once via `ResolveID`, then pass the
canonical `SessionID` to `Lookup`/`Activate`/`Remove`/`Rename` as today.

## Design

### One new exported method, one new sentinel error

Add to `internal/sessions/pool.go`:

```go
// ErrAmbiguousSessionID is returned by Pool.ResolveID when a non-empty
// non-full-UUID arg matches the prefix of two or more sessions. The wrapped
// error's Error() lists each match as `<uuid> (<label>)` on its own line so
// a CLI consumer can print it verbatim. Matchable via errors.Is.
var ErrAmbiguousSessionID = errors.New("sessions: ambiguous session id")

// ResolveID maps a user-supplied UUID-or-prefix string to the canonical
// SessionID of a session in the pool. Empty arg returns the bootstrap
// session's id (same seam as Pool.Lookup("")).
//
// Resolution order:
//
//  1. arg == "" → bootstrap id, no error.
//  2. arg is an exact key in the in-memory session map → that id, no error.
//     This short-circuit is a single map lookup; it never falls through to
//     the prefix scan, so an exact full-UUID match wins even if the same
//     string would also be a HasPrefix hit on the same session.
//  3. otherwise, scan the in-memory map and collect every session whose
//     SessionID has arg as a prefix (strings.HasPrefix). Exactly one match
//     → that id, no error. Zero matches → ErrSessionNotFound. Two or more
//     → ErrAmbiguousSessionID with a list of matches in the wrapped message.
//
// No minimum prefix length is enforced. A one-character prefix is accepted
// when it is unique; refusing short prefixes is a CLI-layer policy concern,
// not a pool invariant.
//
// Concurrency: takes p.mu (RLock) for the entire resolution. The in-memory
// map is the source of truth — sessions.json is not re-read. Concurrent
// Pool.List/Lookup/Snapshot share the read lock; concurrent writers
// (Rename/Create/Remove/RotateID/saveLocked) serialise behind the write
// lock as today.
//
// Lock order: Pool.mu (RLock) only. Does not take Session.lcMu — see "Why
// no Session.lcMu" below.
func (p *Pool) ResolveID(arg string) (SessionID, error)
```

### Implementation sketch

```go
func (p *Pool) ResolveID(arg string) (SessionID, error) {
    p.mu.RLock()
    defer p.mu.RUnlock()

    if arg == "" {
        return p.bootstrap, nil
    }
    // Exact-match short-circuit. AC #1: full UUID always wins, with no extra
    // scan cost beyond the existing map lookup.
    if _, ok := p.sessions[SessionID(arg)]; ok {
        return SessionID(arg), nil
    }

    var matches []*Session
    for id, s := range p.sessions {
        if strings.HasPrefix(string(id), arg) {
            matches = append(matches, s)
        }
    }
    switch len(matches) {
    case 0:
        return "", ErrSessionNotFound
    case 1:
        return matches[0].id, nil
    default:
        return "", ambiguousError(matches)
    }
}

// ambiguousError formats the match list deterministically (sorted by
// SessionID ascending, same tiebreak as Pool.List) and wraps the
// ErrAmbiguousSessionID sentinel so errors.Is matches.
func ambiguousError(matches []*Session) error {
    sort.Slice(matches, func(i, j int) bool { return matches[i].id < matches[j].id })
    var b strings.Builder
    for i, s := range matches {
        label := s.label
        if s.bootstrap && label == "" {
            label = "bootstrap"
        }
        if i > 0 {
            b.WriteByte('\n')
        }
        fmt.Fprintf(&b, "%s (%s)", s.id, label)
    }
    return fmt.Errorf("%w:\n%s", ErrAmbiguousSessionID, b.String())
}
```

### Why a sentinel + `fmt.Errorf("%w: …")` and not a struct error type

The AC asks for the simpler shape that keeps `errors.Is` matching cheap. Two
candidates:

- **Sentinel + wrap (chosen).** `var ErrAmbiguousSessionID = errors.New(...)`,
  return `fmt.Errorf("%w:\n%s", ErrAmbiguousSessionID, lines)`. `errors.Is`
  walks `Unwrap` to the sentinel — one pointer compare. CLI consumer prints
  `err.Error()` verbatim and gets the human-readable list.
- **Struct error with `Matches []SessionRef`.** Exposes the data structurally
  but adds a new exported type (`SessionRef` or similar), and the AC says
  "No new exported types beyond the new typed error." Also forces every CLI
  consumer to either type-assert or duplicate the formatting.

The sentinel form is strictly simpler (one symbol exported, one error path,
no formatter coupling), preserves the AC's "no new exported types" clause,
and lets the CLI in 1.1e-B do a single `fmt.Fprintln(os.Stderr, err)`.

### Synthetic-bootstrap-label substitution

`Pool.List` (#60) substitutes the synthetic string `"bootstrap"` when the
bootstrap entry's on-disk label is empty. The ambiguous-error formatter does
the same so CLI output reads `<uuid> (bootstrap)` instead of `<uuid> ()`.
This mirrors `List`'s rule one-for-one — if it diverges, operators see one
name in `pyry sessions ls` and another in the disambiguation prompt, which
is exactly the kind of cosmetic inconsistency that surfaces as a bug report.

### Why no `Session.lcMu`

`ResolveID` reads two `Session` fields: `id` and `label`. Both are guarded by
`Pool.mu` per the existing discipline:

- `id` — mutated by `RotateID` under `Pool.mu` (write); the `RotateID`
  docstring spells out the invariant. `ResolveID` holds `Pool.mu` (read), so
  no torn reads.
- `label` — mutated by `Pool.Rename` under `Pool.mu` (write); the `Rename`
  docstring spells out that `label` is "guarded by Pool.mu (the only other
  readers are List and saveLocked, both under Pool.mu)". `ResolveID` joins
  that reader set under the same lock.

`lcState` and `lastActiveAt` (the two fields that *do* need `lcMu`) are not
read. No new lock-order edges; no new lock-order obligations.

### Lifecycle state and resolution

`ResolveID` does **not** filter by `LifecycleState`. An evicted session is
still a registry entry with a UUID, and `pyry sessions rename <prefix>` /
`pyry sessions rm <prefix>` must still resolve it. Filtering belongs (if at
all) at the consumer — for instance, `pyry attach <id>` in 1.1e-B may want
to bounce active-vs-idle differently, but that's a verb-layer policy, not
a pool invariant.

### What `ResolveID` does *not* return

- Not a `*Session`. Returning the canonical `SessionID` keeps the surface
  symmetric with the rest of the wire/CLI flow: 1.1e-B unmarshals an id from
  the request, calls `ResolveID`, then routes to `Lookup`/`Activate`/`Remove`
  with the resolved id. A returned `*Session` would tempt callers to short-
  circuit the second lookup — but the second lookup is the lock-clean way to
  guard against a session being removed between resolve and use, and saving
  the second hashmap probe is not worth the sharp edge.
- Not a `SessionInfo`. Same reasoning, plus `SessionInfo` is the read-side
  shape; using it as the resolver return mixes concerns.

The method takes only a `string` arg and returns `(SessionID, error)`.
Smallest possible surface.

## Concurrency model

- **Lock:** `Pool.mu` (RLock) for the whole call. No `Session.lcMu`.
- **Concurrent `ResolveID` + `List`:** both share `Pool.mu` (RLock) — they
  run truly concurrently. Either both observe the pre-write state or both
  observe the post-write state of any concurrent writer. AC #5's race-clean
  requirement falls out of the existing RWMutex discipline; nothing new is
  introduced.
- **Concurrent `ResolveID` + writer (`Rename`/`Create`/`Remove`/`RotateID`):**
  the writer takes `Pool.mu` (write); `ResolveID` blocks behind it briefly
  and then either sees the new state (if it acquires after the writer) or
  returns based on the old state (if it acquired first and the writer is
  blocked behind it). No torn reads; no observation of partial state. A
  session removed mid-resolve either appears in the scan (caller then races
  on the second `Lookup` and sees `ErrSessionNotFound`) or doesn't — both
  outcomes are valid races the consumer was already prepared for.
- **Concurrent `ResolveID` + `ResolveID`:** both share the read lock. No
  contention beyond the standard RWMutex cost.

No new lock-order edges introduced. The only lock taken is `Pool.mu` (read);
this composes cleanly with every existing lock-order chain
(`Pool.capMu → Pool.mu → Session.lcMu`).

## Error handling

Three exit paths. Two reuse existing sentinels; one is new.

| Outcome | Return | Sentinel new? |
|---|---|---|
| `arg == ""` or unique resolution | `(SessionID, nil)` | — |
| no match (full UUID and prefix scan both miss) | `("", ErrSessionNotFound)` | reused |
| ≥2 prefix matches | `("", fmt.Errorf("%w:\n…", ErrAmbiguousSessionID))` | **new** |

Reusing `ErrSessionNotFound` (already exported from `pool.go:31`) means a
consumer that today does `errors.Is(err, sessions.ErrSessionNotFound)` after
`Pool.Lookup` keeps the same matcher after switching to `ResolveID`. No
churn for 1.1e-B's later refactor of 47-B / 48-B.

The new sentinel `ErrAmbiguousSessionID` is the only exported addition; it
sits next to the existing pool-level sentinels in `pool.go`.

No `ResolveID`-specific wrapping prefix is added. The wrapped sentinel
already begins with `sessions: ambiguous session id:`; double-wrapping
would just produce `sessions: resolve: sessions: …`.

## Testing strategy

New file `internal/sessions/pool_resolve_id_test.go` (file-scoped, same
pattern as `pool_rename_test.go` / `pool_remove_test.go`). Same package,
no test seam. All tests use the existing `helperPool` / `helperPoolPersistent`
harnesses; no new test infrastructure.

The AC's test list maps 1:1 to seven tests:

1. **`TestPool_ResolveID_EmptyReturnsBootstrap`** — `helperPool(t, false)`,
   call `pool.ResolveID("")`, assert the returned id equals
   `pool.Default().ID()` and err is nil. Covers AC #1 first clause.

2. **`TestPool_ResolveID_FullUUID`** — `helperPool(t, false)`, capture the
   bootstrap id, call `pool.ResolveID(string(id))`, assert returned id ==
   id and err is nil. Covers AC #1 second clause.

3. **`TestPool_ResolveID_UniquePrefix`** — `helperPool(t, false)`, capture
   the bootstrap id, slice off a 1-char prefix and an 8-char prefix, call
   `pool.ResolveID(prefix)` for each. Assert each returns the bootstrap id.
   Same test covers AC #6 (1-char prefix accepted when unique). The pool
   only contains bootstrap, so any prefix is automatically unique.

4. **`TestPool_ResolveID_FullUUIDBeatsPrefix`** — synthetic two-session
   pool: build a `Pool` with two `Session` entries whose ids share an 8-char
   prefix, where one id is itself a substring/prefix relationship with the
   other. Since UUIDv4s are fixed-length 36 chars no real-world UUID is a
   prefix of another, but the test exercises the short-circuit by directly
   inserting two `Session` values into `pool.sessions` after `New` (the test
   is in-package, so the unexported field is reachable). Verify that calling
   `pool.ResolveID(string(idA))` returns `idA` even though `idA` is also
   `HasPrefix`-of `idA` (i.e. the scan would also find it). The assertion
   that proves the short-circuit fired: pass `idA` whose prefix would also
   match `idB` — assert `idA` wins, no `ErrAmbiguousSessionID`. Covers AC #1
   third clause.

5. **`TestPool_ResolveID_AmbiguousPrefix`** — same two-session in-package
   construction; choose a prefix shared by both ids. Call
   `pool.ResolveID(prefix)`. Assert err is non-nil,
   `errors.Is(err, sessions.ErrAmbiguousSessionID)` is true, and
   `err.Error()` contains both ids and both labels (or "bootstrap" for an
   empty bootstrap label). Assert lines are sorted by SessionID ascending
   so the test can pin the exact substring. Covers AC #3.

6. **`TestPool_ResolveID_NoMatch`** — `helperPool(t, false)`, call
   `pool.ResolveID("ffffffff-ffff-ffff-ffff-ffffffffffff")` and a clearly-
   non-prefix string like `"zzzz"`. Assert
   `errors.Is(err, sessions.ErrSessionNotFound)` for both. Covers AC #2.

7. **`TestPool_ResolveID_RaceWithList`** — `helperPool(t, false)`. Spawn
   N=16 goroutines: half call `pool.ResolveID(prefix)` in a loop; half
   call `pool.List()` and walk the result. Run for ~100 iterations each.
   Asserts nothing; the value is `go test -race ./...` not firing. Same
   shape as `TestPool_Rename_RaceWithList`. Covers AC #4.

`go test -race ./...` and `go vet ./...` clean (AC #4, AC #6 last clause).

### A note on test #4's setup

`Pool` does not expose a public way to inject a second `Session`. The
existing test helpers (`helperPool`, `helperPoolPersistent`) construct a
single-bootstrap pool. Two options:

- **In-package field write** — the test lives in `package sessions` (no
  `_test` suffix), so it can do `pool.sessions[id2] = &Session{id: id2,
  label: "beta"}` directly. Same shortcut already used in
  `pool_remove_test.go` to set up multi-session scenarios.
- **`Pool.Create`** — would spawn a real claude process. Test infrastructure
  for that exists (`TestHelperProcess`) but is overkill for verifying
  resolver semantics.

Use the in-package field write. It's what the existing siblings do, and
it keeps the resolver test focused on string→id mapping rather than
process supervision.

## Files touched

- `internal/sessions/pool.go` — add `ErrAmbiguousSessionID` sentinel,
  `ResolveID` method, `ambiguousError` helper. Add `strings` to the import
  set if not already present (it isn't — verify with `grep '"strings"'
  internal/sessions/pool.go`). ~50 production lines including doc comments.
- `internal/sessions/pool_resolve_id_test.go` — new file, ~180 test lines
  across 7 tests.

Total: 1 production file modified, 1 test file added, 0 files deleted, 0
consumer call sites to update (consumers are 1.1e-B's job).

Comfortably within XS bounds: 1 new method, 1 new sentinel, 0 new exported
types beyond the sentinel, ~50 production lines, 2 files.

## Open questions

None worth deferring. Two judgment calls the developer may make freely:

- **Sort the matches before formatting?** The sketch sorts by `SessionID`
  ascending. Pro: deterministic error message lets test #5 pin the exact
  substring without map-iteration flakiness. Con: trivial extra cost on
  the (rare) ambiguous path. Recommend keep — same tiebreak as `Pool.List`.
- **Should `ResolveID` accept a leading/trailing whitespace and trim it?**
  No. The pool primitive should accept whatever string the caller hands
  it; trimming is the CLI's responsibility (`flag` already does this for
  positional args, and an explicit `strings.TrimSpace` at the CLI layer
  is one line). Same posture as `Pool.Rename` declining to validate
  `newLabel`.

## Out of scope (per ticket body)

- Wire protocol field carrying the resolved/unresolved id (1.1e-B).
- Control verb routing (1.1e-B).
- CLI argument parsing, `pyry attach <id>` plumbing (1.1e-B).
- Refactoring 47-B / 48-B's inlined prefix resolvers to call `ResolveID`
  (opportunistic; not a precondition for this ticket).
- Minimum prefix length enforcement (CLI-layer policy, if at all).
- Trimming whitespace on `arg` (CLI-layer policy).
