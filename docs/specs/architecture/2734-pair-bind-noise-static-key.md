# #2734 — bind a pairing to the first install's Noise static key

## Files read

- `internal/devices/device.go` → `Device`: the on-disk record; `RedeemBy` shows the `omitzero` precedent and its rationale.
- `internal/devices/auth.go` → `Validate`, `ValidateResult`: the single token predicate; its "a rejected attempt mutates nothing" property (LastSeenAt stamped only on accept) must hold for the new mismatch refusal too.
- `internal/devices/registry.go` → `Reload`/`reconcileDevices` (in-memory struct survives a reload, so an in-memory binding whose Save failed is kept), `ClearRedeemBy`/`SetClientVersion` (the mutate-and-report-change shape to copy), `Remove` (revoke deletes the whole record).
- `internal/devices/lock.go` → `WithLock`: cross-process exclusion around Reload→mutate→Save.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: `s.peerStatic` captured after `ReadInit`; `Validate`, then `checkClientVersion`, then the ack is built (its `workspace_root` depends on the accept decision), then the 4401 arm whose `rejectEvent` selection is the #1529 "identical client-visible reject" pattern; accept tail calls `recordRedemption` and `recordClientVersion`.
- `internal/relay/v2session_handshake.go` → `recordClientVersion`: best-effort locked persist with a static sentinel replacing the Reload error.
- `internal/relay/v2session_redemption.go` → `recordRedemption`: returns early on a zero `RedeemBy`, so it cannot carry the binding for a legacy record (ticket note).
- `internal/relay/v2session_rekey.go` → `handleRekeyInit`: already requires the re-key initiator's static to equal `s.peerStatic`, so a session cannot swap keys mid-life.
- `internal/noise/noise.go` → `Responder.PeerStatic`: a 32-byte copy, valid after `ReadInit`.
- `cmd/pyry/pair.go` → `runPairRevoke`: `Registry.Remove` + `Save` drops the whole record, binding included; no change needed.
- `docs/knowledge/features/devices-package.md` § `RedeemBy`: `omitzero` vs `omitempty` lesson.
- `docs/specs/architecture/1529-pair-reject-elapsed-redemption-window.md`: the reject-identity reasoning this ticket reuses.

No other feature branch touches these files.

## Context

A pairing token authenticates as its device from any install that holds it (pyrycode-mobile#1573: a phone and an emulator shared one token, and push registration and wakes flipped between them). Noise_IK already authenticates a per-install device-static key on every connection. Binding the record to the first accepted connection's key makes a reused or leaked code unable to become the same device. This reverses the "binary does not persist the mobile static key" decision in `docs/protocol-mobile.md`; the documentation stage may want a decision record for that reversal.

## Design

### `devices.Device.StaticKey`

```go
StaticKey string `json:"static_key,omitzero"`
```

Lowercase hex of the 32-byte X25519 public key. A string (not `[]byte`) keeps `Device` comparable and the on-disk form readable; `omitzero` keeps unbound records (every record predating the field) free of the key. Empty = unbound.

### `Registry.Validate(plain string, peerStatic []byte) (Device, ValidateResult)`

Gains the presented key. New result `ValidateKeyMismatch`: the token matches a record whose `StaticKey` is set and differs from `hex(peerStatic)`. Checked after the window check, before the LastSeenAt stamp, so a mismatch mutates nothing. Unlike the other refusals it returns the matched record, because the caller must name the device in its log line; the caller still takes the 4401 arm on every non-accepted result. Validate does not bind: an unbound record is accepted as before.

### `Registry.BindStaticKey(tokenHash string, peerStatic []byte) BindResult`

The atomic bind-if-unbound, one critical section under `Registry.mu`:

- `BindUnknownDevice` (zero value, fail closed): no record has `tokenHash`, or `peerStatic` is empty.
- `BindMatched`: already bound to this key — nothing to persist.
- `BindNewlyBound`: was unbound, now bound — caller persists.
- `BindKeyMismatch`: bound to a different key (a racing connection won).

### Handshake (`handleNoiseInit`)

1. `Validate(token, s.peerStatic)`.
2. `checkClientVersion` as today (only on accept).
3. New: when the token is accepted and the version admitted, `BindStaticKey(device.TokenHash, s.peerStatic)`. `BindKeyMismatch` turns the result into `ValidateKeyMismatch` (reason `bind_race_lost`); `BindUnknownDevice` into `ValidateUnknownToken`. This runs before the ack is built, so a refused connection's ack carries no `workspace_root`, exactly as an unknown token's does. Binding only after the version gate means a connection refused at 4412 binds nothing — only an accepted connection binds.
4. 4401 arm: `ValidateKeyMismatch` selects event `v2.handshake.reject.static_key_mismatch` and adds `device_name` and `reason` (`bound_to_other_key` from Validate, `bind_race_lost` from Bind) to the same Warn. Same sealed `auth.invalid_token`, same 4401, same frame order — only the log differs.
5. Accept tail: when `BindNewlyBound`, `recordStaticKey` persists before `recordRedemption`.

### `recordStaticKey(connID string, dev devices.Device)` (relay)

Same contract as `recordClientVersion`: best effort, logged and swallowed. Under `WithLock(redemptionLockWait)`: Reload (error replaced by a static sentinel), then `FindByTokenHash` — Save only if the record is still present and carries the key (a revoke between bind and persist drops it; nothing is resurrected). Reload keeps the in-memory struct, so the binding survives the reload. Warn event `v2.devices.static_key_persist_failed` on error.

### Revoke

`pyry pair revoke` removes the record, binding included; re-pair mints a new record with a new token hash, unbound. No code change; covered by a test.

## Concurrency model

No new goroutines. Two handshakes for one token run on different session goroutines; `BindStaticKey` decides the race under `Registry.mu`, so exactly one gets `BindNewlyBound` and the other `BindKeyMismatch`. Disk persistence is serialised against other processes by `WithLock` and against in-process writers by `Registry.mu` inside each method. Lock order unchanged: file lock outside, `Registry.mu` inside, never nested the other way.

## Error handling

- Mismatch / race lost / record gone: 4401 with the unknown-token body.
- Persist failure: Warn, handshake stays accepted; memory stays bound for this daemon's lifetime (reconcile keeps the struct, and any later Save — redemption, client version, push registration — writes it). A restart before any Save re-binds to whichever install connects first: the pre-change behaviour for one window, announced by the Warn.
- Empty `peerStatic` (unreachable after `ReadInit`): Validate treats it as a mismatch on a bound record; Bind returns `BindUnknownDevice` → 4401.

## Testing strategy

devices (table/unit):
- Validate: unbound accepts; bound+same key accepts and stamps; bound+other key → `ValidateKeyMismatch`, returns the record, LastSeenAt unchanged.
- BindStaticKey: unknown hash, empty key, unbound → NewlyBound, same → Matched, other → KeyMismatch.
- Race: N goroutines bind distinct keys on one unbound record → exactly one NewlyBound.
- Round trip: bound record saves `static_key`, an unbound one omits the key and a legacy JSON loads unbound.
- Revoke: Remove + Add a new record → a different key binds.

relay (handshake):
- First connection binds and persists to `devices.json` (legacy record with no `RedeemBy` too).
- Second connection, same token, different key → sealed `auth.invalid_token`, close 4401, same frames as unknown token; one Warn `v2.handshake.reject.static_key_mismatch` with `device_name` and `reason`; log contains no token, hash or key hex.
- Same key reconnect accepted.
- Version-refused (4412) connection binds nothing.

Existing relay/e2e tests that reconnect one token with a fresh initiator key must be adjusted to reuse the key; that is the intended behaviour change.

## Open questions

- Do existing tests reconnect with fresh keys per connection? Fix each to reuse the device's key.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md` § "Static keys — mobile side": replace the paragraph saying the binary does not persist or retain the mobile device-static public key, and the "This is deliberate" paragraph after it. New wording: the daemon stores the public key in `devices.json` (`static_key`) on the first accepted connection; a later connection with the same token and a different key is refused with the same reject as an unknown token. Say why the "not persisted" decision changed: one token was shared by two installs on pyrycode-mobile#1573, and rotation is already tied to re-pairing. Keep the "Rotation" bullet and say that revoke drops the stored key.
- `docs/knowledge/features/devices-registry.md` / `devices-package.md`: the new field, `ValidateKeyMismatch`, `BindStaticKey`.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The presented key enters at `Responder.ReadInit`, which MAC-verifies and decrypts it before `s.peerStatic` is set; the token enters through `Validate`. The bind decision lives in one place, `Registry.BindStaticKey`, called only from `handleNoiseInit` after both the token and the client version are admitted.
- [Trust boundaries] OUT OF SCOPE (accepted by the ticket): a record that is unbound at upgrade time, or a fresh pairing inside its redemption window, binds to whichever holder connects first. A leaked token that reaches the daemon before the real install takes the record, and the real install is refused until re-pair. Showing the binding in `pyry pair list` so the operator can see this is explicitly out of scope in the ticket.
- [Tokens] No findings. The stored value is a public key, not a secret; it is hex text on the existing `0600` atomic `devices.json`. Revocation is `Registry.Remove` in `runPairRevoke`, which deletes the record with its binding. Downgrading to a binary without the field re-Saves records without `static_key` and fails open to today's behaviour — the same deliberate non-defence `Device.RedeemBy` documents (local write access already beats it).
- [Tokens / oracle] SHOULD FIX: a refused-by-key connection must be indistinguishable from an unknown token. `ValidateKeyMismatch` skips `checkClientVersion` (no 4412 oracle), and `BindStaticKey` must run before the ack payload is built so `workspace_root` stays empty on the race-loser path. Test: the mismatch reject's frames equal an unknown token's, ack without `workspace_root`.
- [File operations] No findings. No new path; persistence reuses `Registry.Save` (temp file, `0600`, fsync, rename) under `WithLock`.
- [Subprocesses] Not applicable: no process is spawned.
- [Cryptography] No findings. No new primitive or randomness. The key comparison is a plain string compare: both sides are public keys, so constant time buys nothing.
- [Network and I/O] No findings. No new reads; the key is the fixed 32 bytes `PeerStatic` returns.
- [Errors, logs] SHOULD FIX: the mismatch Warn carries `event`, `conn_id`, `close_code`, `device_name`, `reason` only — never the token, its hash, or either key. The persist-failure Warn replaces the in-region Reload error with a static sentinel (a decode error can echo `devices.json` bytes). A test asserts the log carries neither the token, the hash nor either key's hex.
- [Concurrency] No findings. Bind-if-unbound is one critical section under `Registry.mu`; the race test asserts exactly one winner. The file lock is taken outside `Registry.mu`, as in `recordRedemption`. A persist that fails leaves memory bound (reconcile keeps the struct); a crash before any Save re-opens the first-come window for that one record, logged by the persist Warn.
- [Threat model] `docs/protocol-mobile.md` § Security model: this narrows "stolen device token" to a token not yet bound. Phone loss with key extraction is unchanged (the key lives in the Keystore).

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-03

## Revisions

### 2026-10-03 — build: one install per token in the test harnesses

The open question resolved as expected: about eighty relay tests and every e2e handshake helper drew a fresh initiator key per connection while reusing one token, which is now a second install and refused. No production design changed. The test-side fixes:

- `internal/relay` tests: `v2TestInstallPriv`, one fixed install key, used by the shared conn-opening helpers (`openModalConn`, `openVersionConn`, `runVersionHello`) and by the multi-conn tests that passed per-conn keys. `redemptionFixture` is pre-bound to that install (`v2TestInstallKey`), so the redemption and client-version tests' "no lock, no write" assertions still isolate their own write. `runVersionHello` became a wrapper over `runHelloFrom`, which takes the install key, for the new tests.
- e2e harnesses (`internal/e2e`, `realclaude`, `liverelay`): new `fakephone.InstallKey(token)` derives the install key from the token (SHA-256), so every helper in every package presents the same key for the same token without threading it through. Unknown-token reject helpers keep their random keys.
