# `Validate` — the WS-perimeter auth predicate (#210, widened #1529, #2734)

Split from [`devices-registry.md`](devices-registry.md) (the parent document; see it for `Registry`'s other exports, the on-disk schema, and `Save`/`Load`/`Reload`). This child covers the token-and-key auth predicate every v2 handshake calls.

`Validate(plain string, peerStatic []byte) (Device, ValidateResult)` is the single auth-check entry point on the phone-WS path. The handler calls it once per inbound connection: `d, result := reg.Validate(plain, peerStatic)`. `ValidateResult` is a four-way outcome, not a bool — the v2 handshake needs to tell "no such device" apart from "this device exists but its pairing token was never redeemed and its window elapsed" (AC-4 of #1529) and apart from "this device exists and is bound to a different install" (#2734), even though all three give the *client* the identical `4401` / `auth.invalid_token` close.

```go
type ValidateResult int

const (
    ValidateUnknownToken  ValidateResult = iota // fail-closed zero value: no match, or plain == ""
    ValidateAccepted                            // matched, window not elapsed, key matches (or unbound) — the only result that authenticates
    ValidateWindowElapsed                       // matched, but RedeemBy is set and has passed
    ValidateKeyMismatch                         // matched, but bound to a static key other than peerStatic
)
```

`ValidateUnknownToken` is the `iota` zero, matching the `RemotePermissionOutcome` pattern in `devices-package.md` — a forgotten assignment, or a future early-return that skips deciding, denies rather than accepts. `ValidateUnknownToken` and `ValidateWindowElapsed` return the zero `Device`, never the matched record — a populated `Device` alongside a refusal would invite a caller to read it. `ValidateKeyMismatch` is the one exception: it returns the matched record, because the v2 handshake's reject log line must name the device even though the client gets no hint (see [the noise_init happy-and-failure-path doc](v2-session-manager-state-machine-noise-init-happy-and-failure-path.md)).

Body shape, in `internal/devices/auth.go`:

```go
func (r *Registry) Validate(plain string, peerStatic []byte) (Device, ValidateResult) {
    if plain == "" {
        return Device{}, ValidateUnknownToken
    }
    hash := HashToken(plain)
    key := hex.EncodeToString(peerStatic)
    r.mu.Lock()
    defer r.mu.Unlock()
    for i := range r.devices {
        if r.devices[i].TokenHash == hash {
            if r.devices[i].redemptionWindowElapsed(time.Now()) {
                return Device{}, ValidateWindowElapsed
            }
            if r.devices[i].StaticKey != "" && r.devices[i].StaticKey != key {
                return r.devices[i], ValidateKeyMismatch
            }
            r.devices[i].LastSeenAt = time.Now()
            return r.devices[i], ValidateAccepted
        }
    }
    return Device{}, ValidateUnknownToken
}
```

`redemptionWindowElapsed(now time.Time) bool` is a separate unexported `Device` method (`!d.RedeemBy.IsZero() && !now.Before(d.RedeemBy)`) rather than inlined, so the boundary — a zero deadline never elapses; at exactly `RedeemBy` the record has already stopped being acceptable — is table-testable at a caller-supplied instant without injecting a clock into `Registry`.

**`Validate` checks a binding; it never creates one.** An unbound record (`StaticKey == ""`) accepts whatever key is presented, including no key at all — the empty-key branch of the mismatch condition can never fire against an unbound row. Binding is [`BindStaticKey`](devices-registry-redemption-and-binding.md#bindstatickey--install-binding-2734)'s job, run by the caller only after every other admission check, including the client-version gate, has passed.

Seven points of structural discipline (two added since #1529: the window check already there, the key check added by #2734):

1. **Empty-plain early-out is first** — before `HashToken`, before the lock. The AC requires "no registry lookup on empty input"; this is the structural enforcement. Also defends (cheaply) the unreachable case of a `Device` persisted with `TokenHash == HashToken("")`.
2. **`HashToken` runs outside the lock.** SHA-256 over a short string is microseconds, but moving it outside the critical section keeps the lock held only for the scan-and-mutate window — important because the auth path is the high-frequency reader. `hex.EncodeToString(peerStatic)` runs outside the lock for the same reason.
3. **The window check, then the key check, then the `LastSeenAt` stamp — strictly in that order, inside the same critical section.** This ordering *is* the security property behind AC-1 (#1529) and the #2734 analogue: a rejected attempt, for either reason, mutates nothing at all — not `LastSeenAt`, not any other field — so `pyry pair list` stays an honest witness that a token was never scanned by its bound install. An expired token, or another install's copy of a bound one, that kept refreshing that column would be indistinguishable from a device in daily use. `Validate` also never reaches `Remove` or `BindStaticKey`, so neither refusal deletes a record or changes a binding — `pyry pair revoke` stays the only remover, `BindStaticKey` the only binder.
4. **Indexed loop (`for i := range r.devices`)** so the `LastSeenAt = time.Now()` assignment mutates the slice element in place. A value-loop (`for _, d := range r.devices`) would assign to a copy and the mutation would silently no-op.
5. **Mutation and snapshot both inside the lock.** The returned `Device` is a value-type copy taken before the deferred unlock fires, so callers see the just-written timestamp.
6. **Byte-exact `==` on `TokenHash` and on the hex-encoded key, not `subtle.ConstantTimeCompare`.** Constant-time at the plain↔hash boundary is owned by `HashToken`; once the wire plain has been hashed, comparing two 64-char hex strings is byte-exact (any timing leak reveals only a public derivative). Both static keys are public by the time they are compared, so the same reasoning covers the key check — there is no secret on either side of it. Inherits #208 / #209 reasoning verbatim. Both checks run strictly after the token match, so neither introduces a branch on token content or a new timing signal about which hash matched.
7. **The key comparison treats an empty presented key as a mismatch against any bound record.** `peerStatic` is only empty when `s.peerStatic` was never set, which `handleNoiseInit` only reaches after `Responder.ReadInit` succeeds — unreachable in production, but the predicate still fails closed rather than special-casing it.

### What `Validate` does NOT do

- **No `Save`.** Disk persistence is the caller's concern. Validate runs on the WS hot path; an fsync per auth is a perf footgun. Future consumer schedules `Save` (periodic ticker / graceful-shutdown hook); the in-memory `LastSeenAt` is the source of truth for runtime decisions.
- **No `context.Context`, no `*slog.Logger`, no error path.** Body is hash + lock + scan + mutate + snapshot — microseconds at p99, never blocks. Auth-event logging (with `conn-id`, `remote-host`, attempt counter, and — since #1529 and #2734 — which of the three refusal reasons fired) is the WS handler's concern; the predicate is logger-free. `ValidateResult` carries the *reason*, not a log line.
- **No rate limiting / lockout / observability.** Per-token attempt counters, IP-level lockout, structured auth metrics — all WS-handler concerns. The predicate is a leaf primitive.
- **No binding.** An unbound record authenticates whatever key is presented; `Validate` only ever compares against a key already stored. [`BindStaticKey`](devices-registry-redemption-and-binding.md#bindstatickey--install-binding-2734) is the sole writer of `StaticKey`.

### Concurrency

`Validate` takes `Registry.mu` exactly once across the scan + mutation + snapshot, releases on return. No new lock, no ordering, no callbacks, no re-entrance — the single-mutex contract from #209 is preserved.

Two concurrent `Validate` calls of the same token serialize on `mu`. The first writes `T1`; the second observes `T2 ≥ T1` (Go's `time.Now()` is monotonic per process) and writes `T2`. Final stored `LastSeenAt` is `T2` — the "monotonically-non-decreasing" invariant the AC names. A concurrent `Save` snapshots whatever value sits in memory at its lock-acquisition; a concurrent `Remove` between two `Validate` calls makes the second return `(Device{}, ValidateUnknownToken)` cleanly (scan runs after the splice committed; no torn read). A `ClearRedeemBy` racing a `Validate` on the same token resolves on the same mutex either way: the loser reads one side of the clear, and both orderings are correct (cleared → accept forever; not yet cleared but still in-window → accept). A `BindStaticKey` racing a `Validate` on the same token resolves the same way: whichever runs first under `mu` decides what the other observes, and both orderings are correct (bound-then-validated → key checked; validated-then-bound → the validating connection already passed, since it couldn't have been the one that just lost a bind race).

### Why a method on `*Registry`

The mutation (`r.devices[i].LastSeenAt = ...`) is registry-side. A free function `Validate(r *Registry, plain string)` would either re-export `mu` / `devices` (encapsulation leak) or call an unexported helper for no abstraction win. Methods on the type that owns the state — same shape as `Add` / `Remove` / `List` / `FindByTokenHash`. Call-site reads as "ask the registry to validate."

### Tests

`internal/devices/auth_test.go`, same-package, table-driven, `t.Parallel()`, stdlib only.

- `TestRegistry_Validate_Hit` — valid token returns matching device and `ValidateAccepted`; `LastSeenAt` advanced (asserted both on returned snapshot and via `List()` to pin the in-memory mutation); `PairedAt` unchanged.
- `TestRegistry_Validate_MissUnknown` — unknown token returns `(Device{}, ValidateUnknownToken)`; `List()` shows no mutation.
- `TestRegistry_Validate_MissEmpty` — empty plain returns `(Device{}, ValidateUnknownToken)`; no mutation. The "no registry lookup" half is enforced structurally by the early-out; the test asserts the observable consequence (no mutation), which is what consumers care about.
- `TestRegistry_Validate_EmptyRegistry` — defends against panic on a zero-init `*Registry`.
- `TestRegistry_Validate_ConcurrentSameToken` — race-detector probe (16 goroutines) plus monotonic-non-decreasing assertion: sort the per-goroutine observed `LastSeenAt` values and check each `>=` the previous; `final.After(when)` proves the structurally-correct lock didn't accidentally skip the mutation (e.g. value-receiver bug). Race detector catches a missing lock on the slice-element write.
- `TestRegistry_Validate_RejectsElapsedRedemptionWindow` (#1529) — inverts #1527's `TestRegistry_Validate_IgnoresExpiredRedeemBy` inertness pin: a device whose `RedeemBy` is an hour past now returns `(Device{}, ValidateWindowElapsed)`, `LastSeenAt` is unchanged from its seeded value, and the row is still present in `List()` (the same accessor `pyry pair list` reads) — that last assertion doubles as the AC-5 witness that the reject removed nothing. Keeps the original pin's two non-vacuity assertions (fixture deadline non-zero, genuinely in the past).
- A forward-dated `RedeemBy` yields `ValidateAccepted` and does advance `LastSeenAt`; a zero-value `RedeemBy` on a long-ago-paired record yields `ValidateAccepted` regardless of how long ago it was paired.
- `TestDevice_RedemptionWindowElapsed` — table over the unexported `redemptionWindowElapsed`: zero deadline (never elapses), deadline in the past, deadline in the future, and `now` exactly at the deadline (the exclusive boundary — the exported path can't pin this without a fake clock).
- `TestRegistry_Validate_StaticKey` (#2734, `internal/devices/static_key_test.go`) — table over the key half: an unbound record accepts any key, including no key; a bound record accepts only its own key and refuses every other (including empty) with `ValidateKeyMismatch`; every refusal case asserts `LastSeenAt` unchanged and `StaticKey` unchanged, and the mismatch case asserts the returned `Device.Name` is the matched device's, not zero.
