# #1528 — Durably record a pairing token's first redemption

## Files read

- `internal/devices/device.go` → `Device.RedeemBy`, `RedemptionWindow` — the field this slice clears; `omitzero` means a cleared deadline vanishes from disk rather than writing `0001-01-01T00:00:00Z`.
- `internal/devices/auth.go` → `Validate` — the no-I/O contract the persist must not violate; also the SECURITY rules on never logging the plain, the hash, or the device name from this predicate.
- `internal/devices/registry.go` → `Reload`, `reconcileDevices`, `Save`, `UpdatePushRegistration`, `readDevicesFile` — the reconcile semantics the locked region depends on (disk authoritative for membership, memory substituted per matching `TokenHash`), the mutator shape to mirror, and `readDevicesFile`'s "wraps path only, never file bytes" rule.
- `internal/devices/lock.go` → `WithLock`, `ErrLockBusy`, `DefaultLockWait` — the sidecar-inode rationale, the no-nesting rule, "sole acquirer in this package", and the explicit instruction that a request-path caller picks a tighter bound.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit` — the hook point: it already holds `m.cfg.DevicesPath`, already reloads before `Validate`, and its accept tail is where the redemption goes.
- `internal/relay/v2session_seams.go` → `V2SessionConfig.Devices`, `DevicesPath`, `Logger` — everything the write needs is already on the config; this slice adds no config field.
- `internal/relay/handlers/register_push_token.go` → `RegisterPushToken` — the existing (lock-free) `Reload`-then-`Save` writer, and the precedent for which errors are logged wrapped versus path-plus-static-reason.
- `internal/relay/v2session_redeemby_test.go` → `TestV2Session_ExpiredRedeemBy_StillHandshakes` — #1527's inertness pin. It leaves `DevicesPath` unset, so it takes the new no-path branch and stays green untouched.
- `internal/relay/v2session_test.go` → `startManager`, `openModalConn` (in `v2session_modal_test.go`), `v2TestToken`, `genV2Keypair`, `silentLogger` — the handshake harness the relay tests drive.
- `internal/e2e/relay_v2_handshake_test.go` → `testV2ReloadPicksUpNewlyPaired`, `testV2ReloadMalformedFailsClosed` — the only e2e users of `DevicesPath`; their fixtures carry no `RedeemBy`, so both take the no-write path.
- `docs/knowledge/features/devices-registry.md` § "Two-writer clobber guard" — records that reload-at-both-sites narrows the daemon's own window but **cannot** close the cross-process race, and that no writer yet calls `WithLock`. That correction is why this slice routes through the lock instead of copying the push-token precedent.
- `docs/knowledge/features/v2-session-manager-state-machine-noise-init-happy-and-failure-path.md` → step 9a/10 — the numbered handshake walk this slice appends a step to.

## Context

`pyry pair` stamps `Device.RedeemBy` at mint (#1527), but nothing durably records that a token was ever redeemed. Enforcing the deadline (#1529) without that record would lock every paired device out on the first daemon restart, because a restarted daemon reads only what is on disk and disk still shows the deadline.

This slice adds the durable signal and nothing else. It is behaviour-neutral at the perimeter: no token that authenticates today stops authenticating after it lands. A device paired before #1527 has a zero `RedeemBy` and takes the no-write path, grandfathered as never-expiring.

No ADR is warranted. The design is a straight application of #1530's `WithLock` to a fourth writer; ADR 029's correction already records why the `Reload`-then-`Save` precedent is not the pattern to copy.

## Design

Two additions, one per package.

**`internal/devices` — `(*Registry).ClearRedeemBy(tokenHash string) bool`.** Zeroes `RedeemBy` on the device whose `TokenHash` matches, under `r.mu`, mirroring `UpdatePushRegistration`'s shape (indexed loop, in-place element mutation, caller owns `Save`). The return value is the load-bearing part: **true iff a matching device was found AND its `RedeemBy` was non-zero** — i.e. iff memory actually changed and a `Save` is warranted. A hit whose deadline is already clear returns false, so idempotency is decided by the registry rather than re-derived at the call site. Tenth export on `Registry`. `Validate` is untouched; its documented no-I/O contract stays literally true, so its doc comment needs no edit.

**`internal/relay` — `(*V2SessionManager).recordRedemption(connID string, dev devices.Device)`**, in a new file `internal/relay/v2session_redemption.go` alongside a package constant `redemptionLockWait` and an unexported sentinel `errRedemptionReloadFailed`. Signature returns nothing: the write is best-effort by AC-4, so every failure is logged and swallowed rather than propagated into the handshake.

Body contract:

1. Return immediately when `m.cfg.DevicesPath == ""` (no path to persist to) or `dev.RedeemBy.IsZero()` (nothing to clear). This is AC-2's structural enforcement: the lock is never acquired and the sidecar lock file is never created on the no-deadline path.
2. Otherwise run one `devices.WithLock(m.cfg.DevicesPath, redemptionLockWait, fn)` region containing exactly: `Reload` → `ClearRedeemBy` → `Save`.
3. `Reload` failure inside the region returns `errRedemptionReloadFailed` — a static sentinel substituted for the read error — and abandons the write. Committing a `Save` against unknown disk membership is the one thing the lock exists to prevent.
4. `ClearRedeemBy` returning false ends the region with `nil` and no `Save`. This covers both a deadline already cleared by a raced writer and a device that a `pyry pair revoke` removed from disk between the handshake's `Validate` and the lock acquisition — in the latter case the in-region `Reload` drops it from memory, so the redemption write can never resurrect a revoked device.
5. One `Warn` on any non-nil error from `WithLock`, carrying `event`, `conn_id`, `path`, and `err`.

**Call site.** In `handleNoiseInit`'s accept tail, immediately after the `m.send` of `respFrame` and before `s.device = &device`. Three consequences, all deliberate:

- *After the accept envelope.* The phone never waits on a lock; a busy lock delays only the daemon's own state transition, never the handshake the phone observes. This is the "deliberate call" the ticket names.
- *Before `s.state = V2StateOpen`.* The state transition is the happens-before edge tests already synchronise on (`waitConnOpen`), so every relay test can assert on disk with no polling.
- *`s.device` keeps the pre-clear snapshot.* `device` is the value `Validate` returned; it is deliberately not patched after a successful clear, because the session snapshot should record what authentication observed, and no reader consults `RedeemBy` off the session (#1529 enforces at the registry).

**Wait bound.** `redemptionLockWait = 250 * time.Millisecond`, well under `DefaultLockWait`'s 5s, per `WithLock`'s instruction that a request-path caller pick its own. `handleNoiseInit` runs on the session's Run goroutine, so the bound is the worst-case stall for that session's frame processing. The peers it contends with (`pyry pair`, `pyry pair revoke`, `register_push_token`) hold sub-millisecond regions.

**Incidental `LastSeenAt` persistence.** `Save` snapshots the whole in-memory registry, so this write also commits whatever `LastSeenAt` values `Validate` has bumped. That is the same incidental effect `register_push_token` already has, it writes an already-on-disk field, and it does not make `Validate` itself perform I/O.

## Concurrency model

No goroutine is spawned; nothing to shut down. The region runs synchronously on the existing Run goroutine, bounded by `redemptionLockWait` plus the region's own I/O.

Lock ordering is `WithLock`'s file lock → `Registry.mu`, and only ever that way. `Reload`, `ClearRedeemBy`, and `Save` each take and release `r.mu` independently inside the region; none of them acquires the file lock, so `WithLock` remains this package's sole acquirer and its no-nesting rule holds — nothing reachable from `fn` calls `WithLock`.

Cross-process exclusion (AC-5): membership after the in-region `Reload` equals disk's exact set, so the subsequent `Save` writes disk's membership with one field cleared. A `pyry pair` that committed before the acquisition is picked up by that `Reload`; one that would commit during the region is excluded by the flock. Structurally, this write cannot drop a record it did not read.

## Error handling

| Failure | Handshake | On disk | Log |
|---|---|---|---|
| `DevicesPath == ""` | completes | untouched, lock never created | none |
| `dev.RedeemBy` zero | completes | untouched, lock never created | none |
| Lock busy past the wait (`ErrLockBusy`) | completes | untouched | `Warn` `v2.devices.redeem_persist_failed`, wrapped err |
| In-region `Reload` fails (corrupt/unreadable file) | completes | untouched | same event, sentinel text only |
| `ClearRedeemBy` false (raced clear, or device revoked) | completes | untouched | none |
| `Save` fails | completes | pre-existing file intact (rename is the commit point) | same event, wrapped err |

SECURITY, per the ticket's rules and `readDevicesFile`'s contract: the single log line may name `err` unconditionally **because** the `Reload` error is replaced by `errRedemptionReloadFailed` before it escapes. `readDevicesFile`'s error can echo `devices.json` bytes, which may carry a `token_hash`; the sentinel carries none. `WithLock`'s errors name the lock path only by its own documented contract, and `Save`'s wrap the path only. No log line here carries the plain token, the token hash, or the device name.

## Testing strategy

`internal/devices/registry_test.go` — `TestRegistry_ClearRedeemBy`, table-driven, mirroring the `UpdatePushRegistration` tests:

- hit carrying a deadline → true; `List()` shows the field zeroed; `PairedAt` / `Name` / `TokenHash` untouched; a sibling device untouched.
- hit with an already-zero deadline → false, no mutation.
- unknown hash → false; empty hash → false.
- zero-value `Registry` → false, no panic.

`internal/relay/v2session_redemption_test.go` — same-package, driving the real handshake via `startManager` + `openModalConn`, with `DevicesPath` pointed at a seeded `t.TempDir()` file:

- **AC-1** first handshake of a device carrying a deadline → the on-disk record shows no `redeem_by` (asserted both on a fresh `devices.Load` and on the raw JSON bytes, since `omitzero` should drop the key entirely).
- **AC-2a** device with no deadline → `devices.json` is not rewritten, and `<path>.lock` never comes into existence (the sidecar's absence proves `WithLock` was never entered).
- **AC-2b** second and third handshake of a just-redeemed device → not rewritten. Witness: after the first handshake, back-date the file's mtime with `os.Chtimes`; a `Save` commits by renaming a fresh temp file, so an unchanged mtime is a non-vacuous proof that no `Save` ran.
- **AC-3** a fresh `devices.Load` of the same path shows no deadline, and a new manager built over that reloaded registry handshakes to open — the restart, both halves.
- **AC-4** a concurrently held lock (held from a goroutine via `WithLock` with a release channel) → the conn still reaches open and the file is not rewritten.
- **AC-5** `recordRedemption` called directly with the in-memory registry holding `[A]` and disk holding `[A, B]` — the sequencing a full handshake cannot stage deterministically. Asserts disk afterwards holds A with the deadline cleared **and** B intact.
- reload-failure abandonment: corrupt `devices.json` after seeding memory, call `recordRedemption`, assert the corrupt bytes are still on disk — memory was never committed against unknown membership.

Touched-scope gate: `go test -race ./internal/devices/... ./internal/relay/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Open questions

- **Does `Validate`'s doc comment go stale?** Resolved at design time: no. It says persistence is the caller's responsibility, which is exactly what the new call site does, and it makes no claim that no caller persists. `auth.go` is not edited.
- **Should the wait bound be a `V2SessionConfig` field so tests can shrink it?** Resolved at design time: no. A package constant keeps the config surface flat, and the only test that pays the full 250ms is the lock-busy one. Revisit if a second call site wants a different bound.
- **Does `s.device` need patching after a successful clear?** Resolved at design time: no, per the Design section. If implementation finds a reader of `s.device.RedeemBy`, that reverses and lands in `## Revisions`.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings — the untrusted→trusted crossing for this path is `helloPayload.Token` at `Validate`, and the new code sits strictly downstream of it: `recordRedemption` takes the `devices.Device` that `Validate` returned, so the only credential material it ever holds is a `TokenHash` the daemon computed and matched against its own store. The plain token is structurally out of reach — it is not a parameter, not a capture, and not derivable from anything passed in. The second boundary is file→memory at the in-region `Reload`; see the Tokens finding for how its error is contained.
- **[Trust boundaries]** No new finding on `conn_id` — the new `Warn` logs the same `conn_id` every existing branch of `handleNoiseInit` already logs, so it adds no field class and no new log-injection surface.
- **[Tokens]** No findings on log surfaces, **but the containment is load-bearing and must survive edits.** The single `Warn` names `err` unconditionally, which is only safe because the in-region `Reload` error is replaced by `errRedemptionReloadFailed` before it can escape `fn`: `readDevicesFile` wraps a `json.Unmarshal` failure that can echo `devices.json` bytes carrying a `token_hash`. The other two error sources were audited and carry no registry content — `WithLock` names the lock path only by its own documented contract, and every `Save` wrap names a path, a fixed step word, or (for the encode step) a `time.Time` marshal failure that names a year. `recordRedemption`'s doc comment must state this constraint so a future editor who adds an error path to the region knows the log line is only safe as long as no wrapped error carries decoded file content.
- **[Tokens — revocation]** No finding, and the in-region `Reload` is what makes it so. It is not merely a clobber guard: a `pyry pair revoke` that commits between the handshake's pre-`Validate` reload and the lock acquisition removes the device from disk, the in-region `Reload` drops it from memory (disk owns membership), `ClearRedeemBy` returns false, and no `Save` runs. The counterfactual design — clear-then-save without reloading inside the region — would write the revoked device back to disk and **resurrect a revoked credential**. This preserves the per-device revocation named as the mitigation for `docs/protocol-mobile.md` § Security model threat 4 ("Token leak via phone").
- **[Tokens — lifecycle]** SHOULD FIX (document, do not defend): a `Save` failure leaves memory cleared and disk not, and because `ClearRedeemBy` already mutated memory — and `reconcileDevices` keeps the in-memory struct across every later `Reload` — no retry happens for the life of the daemon. It self-heals on the next daemon restart, which re-reads the deadline from disk and retries on the next handshake. If the restart happens after the window has passed, #1529 will reject the device and the operator must re-pair. That is a fail-closed operational outcome with a `Warn` naming it, not an exploitable state, and a retry mechanism would be a defense for a failure mode nobody has observed. Phase B records the consequence in `recordRedemption`'s doc comment so #1529's author is not surprised by it.
- **[File operations]** No findings. No path is built from untrusted input: `DevicesPath` is daemon config and `WithLock` derives its sidecar from that same value. The check-then-use gap (`Reload` reads, `Save` writes) is the whole subject of the design — the flock closes the cross-process half and the in-region reload closes the pre-acquisition half. No new file is created by this slice, so modes are inherited unchanged: `Save` at dir `0700` / file `0600` with an explicit pre-encode chmod, `WithLock`'s sidecar at `0600`. Atomicity is inherited from `Save`'s temp-fsync-rename, and a kill mid-region leaves the file either pre- or post-rename with the kernel releasing the flock, so there is no stale lock and no partial file.
- **[Subprocess]** Not applicable, and structurally so: the design adds no `exec.Command`, no shell invocation, and no environment read or scrub. The whole change is registry mutation plus file I/O on one daemon-owned path.
- **[Cryptographic primitives]** No findings. No primitive is introduced. `ClearRedeemBy` compares two `TokenHash` values with `==` rather than `subtle.ConstantTimeCompare`, matching `FindByTokenHash` and `UpdatePushRegistration` — and the exposure here is strictly weaker than either, because the hash it is handed is not attacker-supplied at all: it comes off a device `Validate` already matched, not off the wire.
- **[Network & I/O — resource exhaustion]** No finding, after checking the amplification case explicitly. A phone reconnecting in a loop cannot force repeated fsyncs: the first handshake clears `RedeemBy` in memory, and every subsequent connect fails the `dev.RedeemBy.IsZero()` guard before the lock is acquired. A restart does not re-arm it either, since disk now carries the cleared record. The deadline cannot be re-armed by any wire operation — only `pyry pair` mints one, and re-pairing mints a *new* `TokenHash`. So the write is once per pairing record, ever.
- **[Concurrency — lock ordering]** No findings. Exactly one order exists (`WithLock`'s file lock → `Registry.mu`) and nothing reachable from `fn` acquires the file lock, so `WithLock` stays the package's sole acquirer and its no-nesting rule holds untouched.
- **[Concurrency — TOCTOU on shared state]** No finding, by construction rather than by luck. The `dev.RedeemBy.IsZero()` guard reads a snapshot taken *before* the lock, so it is deliberately only a fast-path filter; correctness is re-decided inside the region by `ClearRedeemBy`'s return. Both directions of a stale guard are safe: a stale non-zero proceeds and finds nothing to clear (no `Save`), and a stale zero skips a write that the concurrent winner already performed.
- **[Concurrency — goroutines and shutdown]** No findings. No goroutine is spawned, so there is nothing to leak and no shutdown path to add. The Run goroutine blocks for at most `redemptionLockWait`; an attacker able to hold that lock is a local process that already has write access to the credential store, so the 250ms-per-handshake stall is not a privilege it did not already have.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model threat 4 ("Token leak via phone", mitigated by per-device revocation) is the relevant entry: a pairing token intercepted before it is scanned stays usable indefinitely today. The mitigation is split across #1527 (stamp the deadline), this slice (record the redemption durably), and #1529 (enforce it) — **this slice enforces nothing**, and the revocation mitigation the threat names is preserved rather than weakened, per the Tokens—revocation finding. Threats 1, 2, 3 and 5 are untouched: no prompt text, no routing key, no relay-visible byte, and no crypto primitive changes here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07
