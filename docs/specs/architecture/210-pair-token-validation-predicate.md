# #210 — `internal/devices`: token validation predicate

**Size:** XS (architect-confirmed). One new file `internal/devices/auth.go`
(~25 prod LOC) + co-located `internal/devices/auth_test.go`. One new exported
method (`(*Registry).Validate`); zero new exported types. Zero consumers
today (the WS auth handler is a follow-up Phase-3 ticket). Within all S red
lines (≤3 new files, ≤150 prod lines, ≤5 new exported types, no consumer
cascade) — confirmed XS.

**Status:** ready for development.

**Depends on:** #208 (merged) for `HashToken` / `VerifyToken`; #209 (merged)
for `Registry`, `Registry.mu`, `Registry.devices`. Both files this ticket
touches (`auth.go` is new; same-package access to `mu` and `devices` is
implicit) live in `internal/devices`.

## Files to read first

The developer's turn-1 data load. Each entry is paged in deliberately —
don't grep for them.

- `internal/devices/device.go` (whole file, 50 lines) — the package
  SECURITY doc comment and the `HashToken` / `VerifyToken` primitives
  this predicate composes. The new file lives in the same package; the
  package doc comment is already written and does not need updating.
  - `HashToken` (line 36) — deterministic SHA-256 hex; the predicate
    calls it on the wire-presented plain after the empty-string early-out.
  - `VerifyToken` (line 46) — **not used by this predicate.** The chain
    "hash plain once → byte-exact lookup against the registry" is
    deliberately simpler than "iterate every device and run constant-time
    compare per entry"; see `Open questions` § "Why not iterate
    VerifyToken over all devices?" for the full chain.
- `internal/devices/registry.go` (whole file, 154 lines) — the existing
  `Registry` shape, the `mu sync.Mutex` + `devices []Device` fields,
  and the existing read-only `FindByTokenHash` (lines 145-154). Mirror
  the locking shape: `r.mu.Lock(); defer r.mu.Unlock()`, linear scan,
  byte-exact `==` on `TokenHash`. `Validate` adds one mutation
  (`r.devices[i].LastSeenAt = now`) inside the same critical section,
  not a separate one — see `Concurrency` below.
- `internal/devices/registry_test.go` lines 159-216
  (`TestRegistry_FindByTokenHash`) — table-driven hit/miss layout to
  mirror for the predicate's hit/miss/empty cases.
- `internal/devices/registry_test.go` lines 333-354
  (`TestRegistry_ConcurrentReadWrite`) — the race-detector probe shape
  to mirror for the concurrent-validation test. Uses `sync.WaitGroup`,
  no fixed sleep, asserts post-condition only.
- `docs/specs/architecture/209-pair-devices-registry-crud.md` lines
  225-264 — the established `Registry` concurrency contract: single
  mutex, every method takes it on entry. Validate inherits this
  contract; no new lock, no lock ordering to document.
- `docs/specs/architecture/208-pair-device-entry-and-token-hashing.md`
  § "Security review" — the SECURITY contract this ticket inherits
  (constant-time at plain↔hash; byte-exact at hash↔hash; no plain in
  logs/errors; no token wrapping in error context). Re-reference,
  don't relitigate.
- `docs/lessons.md` § "JSON roundtrip strips monotonic-clock state from
  `time.Time`" (lines 209-...) — `LastSeenAt` is `time.Time`. The
  in-memory mutation under test is wall-clock only (no JSON roundtrip
  in the test path), so `time.Time.Equal` vs `==` doesn't bite this
  ticket — but if the test ever Saves and Loads to verify persistence,
  switch to `Equal`. (The spec keeps persistence out of scope per AC.)
- `CODING-STYLE.md` § "Concurrency" (lines 53-59) — `sync.Mutex` for
  shared state, `go test -race` always. § "Testing" (lines 61-66) —
  table-driven, `t.Parallel()`, stdlib only.
- The ticket body itself (#210) — six AC bullets. The predicate must
  not call `Save`; the empty plain must not perform a registry
  lookup; no log/error/panic message contains plain, hash, or name.

## Context

#208 delivered the hashing primitives. #209 delivered the `Registry`
with `Add` / `Remove` / `List` / `FindByTokenHash` / `Load` / `Save`.
Neither exposes a single entry point that composes "hash the wire
plain → look it up → record that we saw this device." That entry point
is the auth perimeter on the phone WS path. This ticket adds it.

The seam matters because every inbound phone WS connection runs
through it. The WS-handshake auth handler (sibling Phase-3 ticket) is
the only consumer: one call per connect, returning the matched
`Device` on success or the zero value on miss. Wrapping the three-line
"hash + find + touch" idiom into a single method has two payoffs:

1. The handler is a one-line call (`d, ok := reg.Validate(plain)`),
   which makes "this is the auth check" reviewable at a glance.
2. The empty-plain early-out, the constant-time-vs-byte-exact split,
   and the LastSeenAt mutation under `Registry.mu` are co-located —
   they cannot accidentally drift apart across handler implementations.

The ticket is intentionally read-and-touch only: no `Save`, no
context, no logger, no error path. Disk persistence and observability
are the consumer's concerns.

## Design

### Package placement

The predicate lives in the existing `internal/devices` package next
to `device.go` and `registry.go`. New file `auth.go` (single new
file; the alternative — extending `registry.go` — would mix the
read/write CRUD primitives with the auth-perimeter composition).
Splitting by concern at the file level mirrors `device.go` (token
primitives) vs `registry.go` (storage CRUD); `auth.go` is the third
concern (the WS-perimeter composition).

```
internal/devices/
  device.go          (#208) Device, HashToken, VerifyToken
  device_test.go     (#208)
  registry.go        (#209) Registry, Load, Save, Add, Remove, List, FindByTokenHash
  registry_test.go   (#209)
  auth.go            (this ticket) (*Registry).Validate
  auth_test.go       (this ticket)
```

### Exported surface

```go
// Validate is the WS-perimeter auth predicate. It hashes plain,
// looks up the matching device by hash, and advances that device's
// LastSeenAt to time.Now() in the in-memory registry. Returns the
// matched Device and true on a hit; (Device{}, false) on any miss
// (no device matches, or plain is the empty string).
//
// Validate does NOT persist the LastSeenAt update — disk persistence
// is the caller's responsibility (Validate runs once per WS connect;
// fsync on the auth hot path is undesirable). Callers that want
// LastSeenAt durability schedule a periodic Save (e.g. every N
// minutes, or on graceful shutdown); the in-memory state is the
// source of truth for runtime decisions.
//
// SECURITY: the empty plain returns (Device{}, false) without
// computing HashToken or taking the registry lock. This prevents an
// attacker who omits the token from triggering a registry scan, and
// it defends against the (unreachable today, but cheap-to-defend)
// case of a Device persisted with TokenHash == HashToken("").
//
// SECURITY: Validate never logs the plain, never logs the hash,
// never logs the matched device name, and never returns any of
// these in an error (the predicate has no error path today). The
// returned (Device, bool) is the only signal the caller receives.
//
// Concurrency: the lookup-and-mutate is one critical section under
// Registry.mu — concurrent Validate calls of the same token observe
// a monotonically-non-decreasing LastSeenAt, and the mutation never
// races with Add / Remove / List / FindByTokenHash / Save snapshots.
func (r *Registry) Validate(plain string) (Device, bool)
```

No new types. No new error sentinels. No new package-level helpers.

### Body shape

```go
func (r *Registry) Validate(plain string) (Device, bool) {
    if plain == "" {
        return Device{}, false
    }
    hash := HashToken(plain)
    r.mu.Lock()
    defer r.mu.Unlock()
    for i := range r.devices {
        if r.devices[i].TokenHash == hash {
            r.devices[i].LastSeenAt = time.Now()
            return r.devices[i], true
        }
    }
    return Device{}, false
}
```

Five points of structural discipline:

1. **Empty-plain early-out is first**, before `HashToken` and before
   the lock. The AC requires "no registry lookup"; this is the
   structural enforcement. Skipping `HashToken("")` also avoids
   computing `e3b0c44298fc1c14...`, which the registry could (in
   principle, never in practice) match against.
2. **`HashToken` is computed outside the lock.** SHA-256 over a
   short string is microseconds, but moving it outside the critical
   section keeps the lock held only for the scan-and-mutate window.
   The auth path is the high-frequency reader; minimising lock
   hold time matters more here than in `Save` (which is rare).
3. **`for i := range r.devices`** indexed loop, not value loop.
   Required because `r.devices[i].LastSeenAt = ...` mutates the
   slice element in place; a `for _, d := range r.devices` would
   bind `d` to a copy and the assignment would be a no-op.
4. **Mutation and snapshot inside the lock.** The return value is
   `r.devices[i]` (a value-type copy); evaluation happens before
   the deferred unlock fires, so the returned `Device` is a
   point-in-time snapshot. Caller observation of the returned
   `Device.LastSeenAt` reflects the just-written value.
5. **Linear scan with `==`, not `subtle.ConstantTimeCompare`.** The
   constant-time concern lives at the plain↔hash boundary, owned
   by `HashToken` (deterministic; no early-exit on input) and
   `VerifyToken` (constant-time at the boundary). Once the wire
   plain has been hashed, the resulting 64-char hex string is a
   public derivative — any timing leak from `==` early-exit on a
   prefix mismatch reveals only what the attacker already knows
   (could compute themselves from any candidate plain). Inherits
   #208 and #209's reasoning verbatim; do not relitigate.

### Concurrency

`Validate` takes `Registry.mu` exactly once, holds it across the
scan + mutation + snapshot, releases on return via `defer`. No new
lock, no new ordering, no callbacks, no re-entrance. The single-mutex
contract from #209 is preserved.

| Method            | Critical section                                                  |
|-------------------|-------------------------------------------------------------------|
| `Save` (#209)     | snapshot slice → release lock → atomic-write outside lock         |
| `Add` (#209)      | append                                                            |
| `Remove` (#209)   | linear scan → splice                                              |
| `List` (#209)     | shallow-copy slice                                                |
| `FindByTokenHash` | linear scan → return (Device, bool)                               |
| **`Validate`**    | **scan → mutate `LastSeenAt` → snapshot → return (Device, bool)** |

Two concurrent `Validate` calls of the same token serialize on `mu`.
The first wins the lock, writes `time.Now() = T1`, releases. The
second wins the lock, reads `time.Now() = T2 >= T1` (monotonic
clock), writes `T2`. Final stored `LastSeenAt` is `T2`. This is the
"monotonically-non-decreasing" invariant the AC names.

A concurrent `Save` snapshots under `mu` between the two `Validate`
calls; the saved file may contain `T1` or `T2` (or neither, if the
snapshot races before either Validate). All three are correct; the
durability semantics are "Save reflects whatever is in memory at
its snapshot point," same as #209.

A concurrent `Remove` under `mu` between the two `Validate` calls
removes the device; the second `Validate` returns `(Device{},
false)`. No torn read, no use-after-remove on the slice element —
the second `Validate`'s scan runs after `Remove`'s splice committed.

### What the predicate does NOT do

Named explicitly so the developer doesn't drift:

- **No `Save`.** The AC is unambiguous: disk persistence is the
  caller's job. Validate runs on the WS hot path; an fsync per auth
  is a performance footgun. The follow-up consumer ticket schedules
  Save (periodic ticker or graceful-shutdown hook); not this ticket.
- **No `context.Context`.** The body is hash + lock + scan + mutate
  + snapshot. A few microseconds at p99. ctx-cancel plumbing for
  that window is ceremony without value; if a future ticket needs
  to bound auth latency under load, revisit then.
- **No `*slog.Logger`.** The registry is logger-free per #209.
  Auth-event logging belongs at the WS handler (it has the
  `conn-id`, `remote-host`, etc. that the registry doesn't see).
  The predicate returns `(Device, bool)`; the handler logs.
- **No error path.** AC: this is a `bool` predicate. The constraint
  "no log/error/panic contains plain, hash, or name" stands for any
  future error path — but no error path is added today.
- **No rate limiting / lockout / observability.** Per-token attempt
  counters, IP-level lockout, structured auth metrics — all are
  WS-handler concerns. The predicate is a leaf primitive.

### Why a method on `*Registry` (not a free function)

The AC permits either. Method chosen because:

1. The mutation (`r.devices[i].LastSeenAt = ...`) is a registry-side
   operation; methods on `*Registry` already own the lock and the
   slice. A free function `func Validate(r *Registry, plain string)`
   would either (a) re-export `mu` / `devices` (rejected — encapsulation
   leak) or (b) call an unexported method `r.touchByHash(hash)` —
   adding indirection without simplification.
2. Idiomatic Go: methods on the type that owns the state. The four
   existing read/write methods on `Registry` (`Add`, `Remove`, `List`,
   `FindByTokenHash`) follow this pattern; `Validate` is the fifth.
3. Call-site readability: `reg.Validate(plain)` reads as "ask the
   registry to validate"; `devices.Validate(reg, plain)` reads as
   "ask the package to validate against the registry" — a less
   direct framing of what is fundamentally a registry operation.

### Time source

`time.Now()` directly, no clock injection. The tests verify
`after.After(before)` (wall-clock advancing under real concurrency)
and `!d.LastSeenAt.IsZero()` (the field was actually written) —
neither needs a fake clock. Sibling spec #209 makes the same call
for `PairedAt` (caller fills it, but the registry doesn't take a
clock); this ticket inherits the convention. If a future test needs
deterministic timestamps (e.g. snapshot golden-file diff over
LastSeenAt), introduce a `Clock` interface then.

## Testing strategy

Same-package tests in `internal/devices/auth_test.go`. Each test
`t.Parallel()` (per-test `*Registry` makes them independent). No
external test deps; stdlib `testing`.

### `TestRegistry_Validate_Hit`

```go
when := mustParseTime(t, "2026-05-09T12:34:56.789Z")
r := &Registry{}
r.Add(Device{
    TokenHash:  HashToken("plain-1"),
    Name:       "alice",
    PairedAt:   when,
    LastSeenAt: when,
})

before := time.Now()
got, ok := r.Validate("plain-1")
// Want: ok == true; got.Name == "alice"; got.TokenHash == HashToken("plain-1");
//       got.LastSeenAt.After(before) (or .After(when)) == true.
// And: re-list and confirm the in-memory entry's LastSeenAt advanced too:
listed := r.List()
// listed[0].LastSeenAt.After(when) == true
// listed[0].PairedAt.Equal(when) == true (PairedAt unchanged)
```

Maps to AC: "valid token returns the matching device and advances
LastSeenAt." Asserts both the returned snapshot and the in-memory
mutation (via `List`).

### `TestRegistry_Validate_MissUnknown`

```go
r := &Registry{}
r.Add(Device{TokenHash: HashToken("plain-1"), Name: "alice", PairedAt: when, LastSeenAt: when})

before := r.List() // snapshot for no-mutation assertion
got, ok := r.Validate("never-paired")
// Want: ok == false; got == Device{} (zero value).
after := r.List()
// Want: after[0].LastSeenAt.Equal(before[0].LastSeenAt) == true (no mutation).
```

Maps to AC: "unknown token returns (_, false) with no mutation."

### `TestRegistry_Validate_MissEmpty`

```go
r := &Registry{}
r.Add(Device{TokenHash: HashToken("plain-1"), Name: "alice", PairedAt: when, LastSeenAt: when})

before := r.List()
got, ok := r.Validate("")
// Want: ok == false; got == Device{}.
after := r.List()
// Want: after[0].LastSeenAt.Equal(before[0].LastSeenAt) == true (no mutation).
```

Maps to AC: "empty string returns (_, false) with no mutation." The
"no registry lookup" half of the AC is asserted structurally by the
empty-plain early-out (the body returns before locking); the test
asserts the observable consequence (no mutation), which is the
behavioural guarantee the consumer cares about.

### `TestRegistry_Validate_EmptyRegistry`

```go
r := &Registry{}
got, ok := r.Validate("anything")
// Want: ok == false; got == Device{}.
```

Defends against the regression where Validate panics or errors on a
zero-init `*Registry` (no Add yet). The empty-string-plus-empty-
registry combination is also worth confirming; bundle both into one
test or table.

### `TestRegistry_Validate_ConcurrentSameToken`

Race-detector probe + monotonic-non-decreasing assertion.

```go
r := &Registry{}
when := mustParseTime(t, "2026-05-09T12:34:56.789Z")
r.Add(Device{TokenHash: HashToken("plain-1"), Name: "alice", PairedAt: when, LastSeenAt: when})

const n = 16
var wg sync.WaitGroup
seen := make([]time.Time, n)
for i := 0; i < n; i++ {
    wg.Add(1)
    go func(i int) {
        defer wg.Done()
        d, ok := r.Validate("plain-1")
        if !ok {
            t.Errorf("[%d] ok = false, want true", i)
            return
        }
        seen[i] = d.LastSeenAt
    }(i)
}
wg.Wait()

// All N validations succeeded.
// Final in-memory LastSeenAt is later than the initial PairedAt.
final := r.List()[0].LastSeenAt
// final.After(when) == true.

// (Optional, defensive) sort `seen` and assert each element >= the previous —
// proves the wall-clock readings observed across goroutines form a
// monotonic-non-decreasing series. time.Time.Compare or .Before/.After.
```

Maps to AC: "concurrent validations of the same token observe a
monotonically-non-decreasing LastSeenAt." The race detector
(`go test -race`, mandatory per CODING-STYLE) catches a missing
lock at the slice-element write; the `final.After(when)` assertion
catches a structurally-correct lock that nevertheless skips the
mutation (e.g. value-receiver bug, copy-then-mutate-the-copy).

### Test count and shape

Five tests. Three could collapse into a table-driven `Validate_*`
suite (Hit / MissUnknown / MissEmpty / EmptyRegistry); the
concurrent test stands alone. Whichever shape the developer prefers
is fine — table-driven is preferred per CODING-STYLE if the
fixtures fit cleanly.

## Open questions

Resolved during refinement; recorded so the developer doesn't
relitigate.

- **Why not iterate `VerifyToken` over all devices?** Two reasons.
  (1) Performance: `VerifyToken` recomputes `HashToken(plain)`
  internally per call — N devices means N SHA-256 computations,
  versus one for "hash plain once, then byte-exact-scan". (2) The
  constant-time concern is the plain↔hash boundary, which `HashToken`
  satisfies (deterministic, no early-exit on the input). Once the
  plain is hashed, comparing two 64-char hex strings is byte-exact;
  the timing of `==` on a hash leaks only a public derivative. See
  #208's security review § "Constant-time compare" and #209's
  "Open questions" § "Should `FindByTokenHash` use
  `subtle.ConstantTimeCompare`?" for the full chain. The sibling
  decisions stand.

- **Why not return an `error` instead of `bool`?** The AC explicitly
  specifies `(Device, bool)`. The auth handler distinguishes "valid
  token" (the only success) from "anything else" (the only failure
  surface visible to the phone — phones get a generic 401-equivalent,
  no detail). Three error states ("empty plain," "unknown token,"
  "registry not yet loaded") collapse into a single bool because
  the handler doesn't branch on which one. If a future ticket needs
  per-cause structured errors (e.g. for daemon-side audit logs), it
  adds a sibling `ValidateE(plain) (Device, error)` then; the
  predicate stays a bool today.

- **Should `Validate` accept a `context.Context`?** No. The body is
  microseconds at p99 and never blocks (mu is uncontended in the
  steady state — pairing is rare; auth is solo per-connection
  modulo concurrent connects from the same phone). ctx adds no
  cancellation point that matters. Sibling tickets in the auth path
  (rate limiting, downstream session lookup) can take ctx; this
  predicate doesn't.

- **Should `Validate` log on failure?** No. The registry knows
  nothing about the connection (no `conn-id`, no `remote-host`);
  any log line at this layer would be context-free and unactionable.
  The handler logs (with `conn-id`, `remote-host`, attempt counter),
  redacting plain and hash per the package SECURITY contract.

- **What if `time.Now()` reads identical values across goroutines?**
  The monotonic-clock reading on Go is monotonic per process —
  successive `time.Now()` calls within one process never decrease.
  If two reads return the same value (clock granularity edge), the
  AC's "non-decreasing" invariant is preserved (`==` satisfies
  `>=`). No ceremony needed.

- **Should the in-memory `LastSeenAt` mutation be visible to
  concurrent `List` callers immediately?** Yes. `List` takes `mu`,
  copies the slice, releases. A `Validate` that completed before
  `List`'s lock acquisition is observable in `List`'s output. A
  `Validate` that runs after `List`'s release isn't. This is the
  standard happens-before contract under a single mutex; no
  surprises.

- **Should `FindByTokenHash` be deprecated in favour of `Validate`?**
  No, not in this ticket. `FindByTokenHash` is a read-only lookup
  with one existing test consumer (`registry_test.go:159-216`) and
  one conceptual use case (operator-facing inspection: "is this
  hash in the registry?" without touching `LastSeenAt`). Validate
  is the auth predicate; `FindByTokenHash` is the read-only
  inspector. They're different surfaces. If the latter ever
  acquires zero callers post-Phase-3, revisit.

- **What about a per-device `LastSeenAt` floor (don't overwrite a
  more recent timestamp)?** Not needed. `time.Now()` is monotonic;
  a successful `Validate` always advances. The only way `LastSeenAt`
  could regress is if a process restart reloaded a stale on-disk
  timestamp — that's a #209 Save-cadence concern, not a Validate
  concern.

## Security review

Per CLAUDE.md (architect's pipeline-wide instructions): this ticket
carries the `security-sensitive` label, so the architect runs an
adversarial self-review of the spec before commit. The pass walks
the categories from `agents/architect/security-review.md`.

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The single trust boundary is
  the `plain string` argument — wire-supplied, untrusted, scrubbed
  via two structural defenses: (1) the empty-string early-out
  (returns before any registry interaction), (2) the `HashToken`
  call that turns the untrusted plain into a deterministic 64-char
  hex string, after which all comparisons are against operator-
  controlled hashes from `devices.json`. Downstream callers receive
  a `Device` value (validated, name-authoritative) plus a `bool`;
  there is no leakage of the plain or hash into the return shape.

- **[Tokens, secrets, credentials]** No findings.
  - Generation: not in this ticket (#208 owns mint via `crypto/rand`).
  - Storage: not in this ticket (#209 owns `devices.json` atomic write).
  - Comparison: byte-exact at the hash↔hash boundary, per the chain
    of reasoning inherited from #208 and #209. The plain↔hash
    boundary is owned by `HashToken` (deterministic SHA-256, no
    early-exit on input); the predicate calls it once and never
    re-derives. See "Open questions" § "Why not iterate `VerifyToken`."
  - Logging: zero. The predicate has no logger; the registry has no
    logger; the package SECURITY doc comment in `device.go` already
    forbids plain-token logging across the whole package.
  - Lifecycle: revocation is `Remove(name)` (#209) followed by
    `Save` (#209). After `Remove` returns, a concurrent `Validate`
    of the revoked token's plain returns `(Device{}, false)` because
    the registry's slice no longer contains the matching hash.
    Synchronous: no propagation lag, no observability gap.
  - Lifecycle (token rotation): out of scope — handled by re-pairing
    (revoke + pair). No mid-session re-keying in the current threat
    model.
  - Error context: no error returns today. The AC's "no plain in
    panic / log / error" is satisfied vacuously and remains a
    constraint for any future error path.

- **[File operations]** N/A. The predicate touches no filesystem
  state. `Save` is explicitly forbidden by the AC; the in-memory
  mutation never reaches disk through this code path. The
  consumer's eventual periodic-Save pattern inherits #209's
  atomic-rename discipline; not this ticket's concern.

- **[Subprocess / external command]** N/A. No `os/exec`, no shell-out.

- **[Cryptographic primitives]** No findings.
  - `HashToken` is the only crypto call; SHA-256 via `crypto/sha256`,
    deterministic, no salt (per #208's documented decision over
    256-bit random tokens). Inherited verbatim.
  - No `crypto/rand` use here (rand is #208's mint concern).
  - No constant-time comparison needed at the hash↔hash boundary —
    see "Open questions" and #208's security review for the full
    derivation. Listing this here as a positive design decision,
    not as an absence of analysis.
  - No key reuse (no keys in this ticket — only hash comparison).

- **[Network & I/O]** N/A locally. The predicate never reads from a
  socket; it consumes a `string` already extracted by the WS
  handler. The handler enforces input size caps, header validation,
  and timeouts (handler-ticket scope). The predicate's worst-case
  cost is one SHA-256 (microseconds) plus one mutex-guarded scan
  over O(devices) entries, where devices is operator-controlled
  and expected to stay in single digits.

- **[Error messages, logs, telemetry]** No findings. The predicate
  never logs and returns no error. Caller-side logging (the WS
  handler) is responsible for redacting plain and hash; the package
  SECURITY doc comment already names this constraint and #208's
  security review enforces it via reviewers' checklist.

- **[Concurrency]** No findings.
  - Single mutex (`Registry.mu`); entry/exit on `Validate`. The
    `TestRegistry_Validate_ConcurrentSameToken` race-detector probe
    confirms.
  - Lock hold window: scan + one assignment + return-value
    materialization. `HashToken` is computed outside the lock; no
    I/O ever runs inside.
  - Lock ordering: only one lock in the predicate; nothing to order.
    No callbacks, no re-entrant takes — leaf mutex.
  - Goroutine lifecycle: the predicate spawns no goroutines.
  - Shutdown safety: a SIGKILL mid-Validate leaves the in-memory
    `LastSeenAt` partially-updated only at the assignment-instruction
    granularity (a `time.Time` write is two words; on most archs
    not atomic). This is fine: process death drops in-memory state
    entirely, so partial-update visibility is unobservable. The
    last durably-saved `LastSeenAt` is whatever the most recent
    `Save` (sibling consumer's responsibility) committed. No
    consistency invariant is at risk.
  - TOCTOU on shared state: the find + mutate + snapshot is one
    critical section under `mu`. Concurrent Remove of the same
    device commits before or after Validate's lock acquisition;
    no torn read.

- **[Threat model alignment]**
  - `protocol-mobile.md:97-98` ("binary validates device-token on
    first frame") — **this ticket is the validation primitive
    that handler-side ticket consumes.** The structural defense
    against "phone presents wrong token" is `(Device{}, false)`;
    against "phone presents empty token" is the early-out;
    against "phone presents replayed token from revoked device" is
    `Remove + Save` invalidating the registry entry before the
    next Validate runs.
  - `protocol-mobile.md:62` ("binary stores `sha256(token)` in
    `devices.json`, never the plaintext") — preserved. The plain
    crosses the predicate as an in-memory string only; never
    serialized, never logged.
  - Out of scope and named so: rate-limiting on the validation
    surface (handler ticket); structured auth-event logging
    (handler ticket); auto-revocation after N consecutive misses
    (defer); per-device session-bind enforcement after Validate
    succeeds (handler ticket). Lockout policy after repeated
    misses is intentionally absent — the WS layer's per-IP /
    per-handshake pacing is the place for that, not this leaf
    predicate.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-09
