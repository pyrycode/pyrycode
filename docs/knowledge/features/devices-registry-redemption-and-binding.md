# Redemption, client-version and install-binding mutators

Split from [`devices-registry.md`](devices-registry.md) (the parent document). This child covers the three best-effort, `Registry.mu`-only mutators the v2 handshake's accept tail calls — `ClearRedeemBy` (#1528), `SetClientVersion` (#2577), and `BindStaticKey` (#2734) — plus the lock-guarded-persist testing lessons that apply to all three's callers.

## `ClearRedeemBy` — redemption clear (#1528)

`ClearRedeemBy(tokenHash string) bool` zeroes `Device.RedeemBy` on the row whose
`TokenHash` matches, under `r.mu`, mirroring `UpdatePushRegistration`'s shape
(indexed loop, in-place element mutation, caller owns `Save`). It exists so
`internal/relay`'s v2 handshake can durably record that a pairing token has
been redeemed — see [ADR 029](../decisions/029-devices-registry-reload-at-handshake.md)'s
sibling concern and [`features/v2-session-manager.md`](v2-session-manager.md)
for the call site.

The return value is the load-bearing part: **true iff a matching device was
found and its `RedeemBy` was non-zero** — i.e. iff memory actually changed and
a `Save` is warranted. A hit whose deadline is already clear returns false, so
a caller that validates the same device three times in a row (the common case)
decides "nothing to persist" from this return alone, with no separate
before/after comparison. `Validate` itself is untouched by this addition —
its no-I/O contract (see [`devices-registry-validate.md`](devices-registry-validate.md) § "What `Validate` does NOT do") stays literally true, since the clear and the
`Save` both live at the caller, one layer up.

**The safety property is a consequence of composing with `Reload`, not
something `ClearRedeemBy` does itself.** A caller that runs `Reload` inside
the same locked region before calling `ClearRedeemBy` gets a for-free
guarantee: if `pyry pair revoke` removed the device from disk between the
caller's last read and the lock acquisition, that `Reload` drops it from
memory first, so `ClearRedeemBy` reports no match and no `Save` runs — the
write cannot resurrect a revoked credential. `ClearRedeemBy` has no opinion
about revocation; it just reports whether the row it was asked about still
exists and still needs clearing.

### Testing a best-effort, lock-guarded persist

Two witnesses worth reusing on any future best-effort `WithLock` consumer,
both from `internal/relay/v2session_redemption_test.go`:

- **A "the file wasn't rewritten" assertion is vacuous if it only compares
  bytes** — `Save` is idempotent, so an unchanged-content write still passes a
  byte comparison. Back-dating the file's mtime with `os.Chtimes` first makes
  the assertion non-vacuous: `Save` commits via temp-file-then-rename, which
  always installs a current mtime, so an unchanged mtime after the call under
  test is proof no `Save` ran.
- **`WithLock`'s sidecar file is a free witness for "the locked region was
  never entered."** It's created before the caller's function runs, so
  `os.Stat(path + ".lock")` returning `fs.ErrNotExist` is a strictly stronger
  claim than "no `Save` ran" — it needs no seam, no counter, and no fake, and
  it is what proves a no-deadline device costs nothing at all (not even a
  lock acquisition).
- **A busy-lock refusal test proves the lock exists, not that the read moved
  inside it — only an interleaving assertion proves the latter (#1531).** Park
  a lock holder, start the write under test, assert it has *not* completed
  after a short grace (a completion during the grace proves no region covers
  the write), commit a second write from under the holder, release, then
  assert the concurrent write survived. A build that wraps only the existing
  `Save` in `WithLock` passes every "refuses when busy" test — it genuinely
  does refuse when busy — but fails this one, because its `Load` still ran
  before the lock was ever taken, so the concurrent write gets silently erased.
  `cmd/pyry/pair_lock_test.go`'s `TestRunPairDefault_ReadsSnapshotInsideLock`
  is the worked example; its discriminating power against that exact mutant
  was verified with a `go test -overlay` run rather than assumed.
- **A best-effort in-region reload's failure branch needs its own leak
  assertion, not just its own log line (#1532, gap not yet closed).**
  `RegisterPushToken`'s security posture rests on "`Reload`'s error is consumed
  inside the region and never returned from the closure, so it cannot reach a
  log field" — a structural claim about code shape, not a tested one. No test
  writes a malformed `devices.json`, drives the handler, and asserts the
  captured log names `path` but none of the corrupt file's bytes; a future edit
  that returns the reload error instead of swallowing it would change the
  reply *and* reopen the `token_hash`-in-a-decode-error path this design closes
  by construction, and nothing would redden. Worth pinning on the next
  `WithLock` caller with a best-effort (rather than abandon-on-failure) reload.

## `SetClientVersion` — app-version record (#2577)

`SetClientVersion(tokenHash, version string) bool` sets `Device.ClientVersion`
on the row whose `TokenHash` matches, under `r.mu`, in the same indexed-loop
shape as `UpdatePushRegistration` and `ClearRedeemBy`. The return value is the
same "was a `Save` warranted" signal as `ClearRedeemBy`'s: true *iff* a device
matched **and** the stored value actually changed. A device that reconnects
with the same version it last reported — the common case — returns false, so
the caller decides "nothing to persist" from this one bool with no separate
before/after comparison.

Unlike the filter-then-store split elsewhere in this ticket, `SetClientVersion`
does no admission of its own: it stores whatever string it is given, verbatim,
including an empty one. The registry has no opinion on what makes a version
string acceptable — that is `internal/sessions.AdmitClientVersion`'s job (the
same rule `admitClient` applies to the client-identity system prompt, exported
so the check exists once — see
[`features/sessions-package-key-types-writesystemprompt-systemprompttext.md`](sessions-package-key-types-writesystemprompt-systemprompttext.md)).
The caller, `internal/relay`'s `recordClientVersion`, admits before calling.

Consumed the same way `ClearRedeemBy` is: `recordClientVersion` runs
`devices.WithLock` → `Reload` → `SetClientVersion` → conditional `Save`,
immediately after `recordRedemption` on the v2 handshake's accept tail — see
[the noise_init happy-and-failure-path doc](v2-session-manager-state-machine-noise-init-happy-and-failure-path.md)
for the call site, its placement rationale, and the accepted cost of a device
that redeems its token and reports a new version in the same hello.

## `BindStaticKey` — install binding (#2734)

`BindStaticKey(tokenHash string, peerStatic []byte) BindResult` binds the
device whose `TokenHash` matches to `peerStatic` **if it is not bound yet**.
Unlike `ClearRedeemBy` / `SetClientVersion`, the return is not a bool but a
four-way `BindResult`, because the caller (the v2 handshake) must turn three
of the four outcomes into three different reject reasons on the same wire
shape, and the fourth into "nothing to persist":

```go
type BindResult int

const (
    BindUnknownDevice BindResult = iota // no device has tokenHash, or peerStatic is empty
    BindMatched                         // already bound to this key — nothing changed
    BindNewlyBound                      // was unbound, now bound — caller persists
    BindKeyMismatch                     // bound to a different key
)

func (r *Registry) BindStaticKey(tokenHash string, peerStatic []byte) BindResult {
    if len(peerStatic) == 0 {
        return BindUnknownDevice
    }
    key := hex.EncodeToString(peerStatic)
    r.mu.Lock()
    defer r.mu.Unlock()
    for i := range r.devices {
        if r.devices[i].TokenHash == tokenHash {
            switch r.devices[i].StaticKey {
            case "":
                r.devices[i].StaticKey = key
                return BindNewlyBound
            case key:
                return BindMatched
            default:
                return BindKeyMismatch
            }
        }
    }
    return BindUnknownDevice
}
```

**The check and the write are one critical section under `Registry.mu`.** Of
two connections racing to bind one unbound record with different keys,
exactly one observes the `case ""` branch and writes; the loser's scan runs
after that write committed (same mutex), so it falls into `default` and gets
`BindKeyMismatch` — never a torn read, never two winners. `TestRegistry_BindStaticKey_Race`
(16 goroutines, one unbound record, distinct keys) pins this.

**A bound device is never rebound.** Once `StaticKey` is non-empty, every
future call for that `tokenHash` returns either `BindMatched` or
`BindKeyMismatch` — there is no "rebind" branch. The only way to change which
key a token binds to is `pyry pair revoke` removing the whole record (via
`Remove`, unaffected by this ticket) followed by a fresh `pyry pair`, which
mints a new token hash with no `StaticKey` at all. `TestRegistry_StaticKey_RevokeReleases`
pins the release-and-rebind sequence.

**Caller's job, not this method's: deciding *when* to call it, and persisting
on `BindNewlyBound`.** `BindStaticKey` takes no file lock — same posture as
`ClearRedeemBy` and `SetClientVersion`. The v2 handshake calls it only after
the token is accepted and the client-version gate has also admitted the
connection, so a connection refused for either reason binds nothing; see [the
noise_init happy-and-failure-path doc](v2-session-manager-state-machine-noise-init-happy-and-failure-path.md)
for the call site, the ordering rationale, and `recordStaticKey`, the
`WithLock`-guarded persist that mirrors `recordClientVersion`.

**`BindUnknownDevice` covers two different inputs on purpose.** An empty
`peerStatic` and a `tokenHash` with no matching record both fail closed to the
same zero value — in production the first case is unreachable (`handleNoiseInit`
only calls `Validate`/`BindStaticKey` after `Responder.ReadInit` has already
populated `s.peerStatic`), but the predicate does not special-case it, matching
`Validate`'s own "fails closed rather than assume" posture on the same input.

A legacy record — one redeemed before this field existed, `StaticKey == ""`
and no other change — binds exactly like a fresh unbound one:
`TestRegistry_StaticKey_RoundTrip`'s last two assertions seed one by hand
(no `static_key` key in the JSON at all) and bind it. `recordRedemption`'s own
early return on a zero `RedeemBy` (see [the noise_init happy-and-failure-path
doc](v2-session-manager-state-machine-noise-init-happy-and-failure-path.md))
has no bearing here — the static-key persist is a separate `WithLock` region
from the redemption persist, run unconditionally on `BindNewlyBound`.

### Tests

`internal/devices/static_key_test.go`, same-package, table-driven,
`t.Parallel()` everywhere, stdlib only — alongside `TestRegistry_Validate_StaticKey`
(see [`devices-registry-validate.md`](devices-registry-validate.md)):

- `TestRegistry_BindStaticKey` — table: unknown hash → `BindUnknownDevice`;
  empty key against a known hash → `BindUnknownDevice` (never binds); unbound
  + a key → `BindNewlyBound`, stores it; already-bound + same key →
  `BindMatched`, no change; already-bound + a different key →
  `BindKeyMismatch`, stored key unchanged.
- `TestRegistry_BindStaticKey_Race` — 16 goroutines bind distinct keys against
  one unbound record; exactly one `BindNewlyBound`, the rest `BindKeyMismatch`,
  and the stored key is the winner's.
- `TestRegistry_StaticKey_RoundTrip` — a bound record's `Save` writes exactly
  one `"static_key"` key to disk (an unbound sibling writes none); `Load`
  round-trips the bound value and leaves the unbound row empty; a hand-written
  legacy record with no `static_key` field at all loads unbound and both
  `Validate` and `BindStaticKey` treat it as such.
- `TestRegistry_StaticKey_RevokeReleases` — bind, `Remove` the record, confirm
  the old token is now unknown, `Add` a fresh record under a new token, and
  confirm it binds to a different key.
