# 1532 — hold the devices.json lock across `register_push_token`'s reconcile-and-save

## Files read

- `internal/relay/handlers/register_push_token.go` → `RegisterPushToken`, `msgBinaryBusy`,
  `msgUnauthorized`, `pushFieldIsDisplaySafe` — the one production file this slice edits;
  its branch order and the exact log fields per branch are what AC-3 and AC-4 constrain.
- `internal/relay/v2session_redemption.go` → `recordRedemption`, `redemptionLockWait`,
  `errRedemptionReloadFailed` — the pattern to copy, named as such by the ticket and by
  `devices-registry.md`. Its reload-failure posture is the one deliberate divergence
  (see § Design).
- `internal/devices/lock.go` → `WithLock`, `ErrLockBusy`, `DefaultLockWait` — the sidecar
  is created before `fn` runs (a free "region never entered" witness), `fn`'s error is
  returned verbatim (so `errors.Is` through the wrapper works), and nesting is forbidden.
- `internal/devices/registry.go` → `Reload`, `reconcileDevices`, `UpdatePushRegistration`,
  `Save`, `readDevicesFile` — the ENOENT→empty membership contract, the
  keep-in-memory-survivor reconcile, and `Save`'s wrapped-error vocabulary
  (`registry: create temp`, `registry: rename`) that AC-4's "fixed step word" rests on.
- `cmd/pyry/pair.go` → `runPairRevoke`, `mintDevice`, `pairLockWait` — the #1531 retrofit's
  shape, and `pairLockWait`'s precedent for a `var` bound that a timing test may retune.
- `cmd/pyry/pair_lock_test.go` → `holdPairLock`, `freezeRegistry`, `mintBlockGrace`,
  `TestRunPairRevoke_ReadsSnapshotInsideLock` — the interleaving witness AC-2 asks for,
  verbatim in shape.
- `cmd/pyry/pair_test.go` → `TestRunPairRevoke_SaveFailure` — one of the two hollowed-out
  tests AC-5 repays.
- `internal/relay/v2session_redemption_test.go` → `backdate`, `assertNotRewritten`,
  `TestV2Session_LockBusy_HandshakeStillCompletes` — the back-dated-mtime witness.
- `internal/relay/handlers/register_push_token_test.go` → `assertRejectedNoWrite`,
  `freshRegistryWithDevice`, `newTestConn`, `TestRegisterPushToken_SaveFailure_EmitsServerBinaryBusy`,
  `TestRegisterPushToken_ReloadPreventsClobberOfNewlyPairedDevice` — the suite that must
  stay green, the shared reject assertion the sidecar witness folds into, and the second
  hollowed-out test.
- `internal/relay/v2session_handshake.go` → the `s.device = &device` assignment — confirms
  `Conn.Auth()` hands out a per-connection snapshot that no write updates, which is the
  fact `msgBinaryBusy`'s doc comment currently gets wrong.
- `docs/knowledge/features/devices-registry.md` § *Two-writer clobber guard*,
  § *Testing a best-effort, lock-guarded persist* — names `recordRedemption` as the
  retrofit pattern, and carries the two reusable witnesses: back-date the mtime or the
  unchanged-file assertion is vacuous, and the sidecar's absence beats "no `Save` ran".
  Also the #1531 lesson that **wrapping `Save` in `WithLock` is not the same as moving the
  read inside it**, and that only an interleaving assertion tells those apart.
- `docs/knowledge/features/pyry-pair-command.md` § *Tests* — records the
  `TestRunPairRevoke_SaveFailure` hollow-out as a deferred fix; this slice repays it.

## Context

`RegisterPushToken` is the last `devices.json` writer that does not hold
`devices.WithLock`. It calls `Reload` and then `Save` on the daemon's long-lived
registry with nothing held between them, so a `pyry pair` committing in that window is
erased by the handler's whole-file `Save` — permanently, because `Reload` reconciles
memory *from* disk and a record no longer on disk cannot be recovered. The reverse
direction erases the push registration instead, or resurrects a device
`pyry pair revoke` removed.

`mintDevice`, `runPairRevoke` (#1531) and `recordRedemption` (#1528) already read a fresh
snapshot inside the lock, so this handler is the one remaining peer that can commit over
them. Closing it closes the headline daemon-vs-CLI race in both directions.

No ADR is warranted — ADR 029 already owns this boundary and the documentation stage has
the handoff to record that its *Correction (2026-08-19, #1530)* rationale is superseded.

## Design

### The region

The handler keeps its fast paths and gains one locked region:

```
unauth → malformed → 3 display-safety guards → dedupe        (unchanged, no lock)
WithLock(registryPath, pushRegistryLockWait, func() error {
    Reload           (best-effort; a read error logs path + a static reason, then continues)
    UpdatePushRegistration  → no match returns errPushDeviceGone
    Save
})
```

Two departures from today's body, both required by the acceptance criteria:

**The reconcile moves ahead of the mutation.** Today `UpdatePushRegistration` runs first
and survives the reload only because `reconcileDevices` substitutes the in-memory struct.
Reconciling first makes the revoke direction fall out for free: the reload drops a record
`pyry pair revoke` removed, the mutation reports no match, and nothing is saved. It is the
same free guarantee `ClearRedeemBy` gets from the same ordering, and it is the one
deliberate reply change in this slice — a device the reconcile drops is now refused on the
existing non-retryable `auth.invalid_token` / `msgUnauthorized` rather than acked.

**The reload stays best-effort, diverging from `recordRedemption`.** `recordRedemption`
abandons the write on a reload failure; #782's contract for this handler is to log and
still save the known-good in-memory state (self-heal, no worse than the pre-#782 blind
`Save`), and AC-1 pins that. The divergence is deliberate and gets a comment saying so,
because the file otherwise reads as a straight copy of a pattern it departs from in one
place.

The reload is **not** redundant under the lock. The lock excludes a writer from committing
*during* the region but says nothing about one that committed between the handshake's
earlier reload and this acquisition — which is precisely the window `recordRedemption`'s
in-region reload exists for. The #782 comment above the reload justifies it as a clobber
guard and no longer states the true reason; it gets rewritten rather than left stale.

### Contracts introduced

- `var pushRegistryLockWait = 250 * time.Millisecond` — a request-path bound matching
  `redemptionLockWait` and `wireMintLockWait`, not `DefaultLockWait`, per `WithLock`'s own
  doc. A `var` rather than a `const` for `pairLockWait`'s stated reason inverted: the
  interleaving test must *raise* it so the bound outlives the grace it parks for.
  Unexported; production never reassigns it; only sequential (non-`t.Parallel`) tests
  retune it, so no parallel reader ever overlaps a write.
- `var errPushDeviceGone = errors.New(...)` — the only value that escapes the region
  distinguishably. Lock-busy, mkdir/open failure and `Save` failure all map to the same
  retryable `server.binary_busy` reply, so only "device gone" needs to be told apart.
  Matched with `errors.Is` because `WithLock` returns `fn`'s error verbatim.

### Error handling

One `switch` on the region's error, after it returns:

| Region error | Log event | Reply |
|---|---|---|
| `errors.Is(err, errPushDeviceGone)` | `register_push_token.gone_mid_conn` (log line byte-unchanged) | `auth.invalid_token` / `msgUnauthorized`, non-retryable |
| `errors.Is(err, devices.ErrLockBusy)` | `register_push_token.lock_busy` (new; log events are not replies) | `server.binary_busy` / `msgBinaryBusy`, retryable |
| any other non-nil | `register_push_token.save_failed` | `server.binary_busy` / `msgBinaryBusy`, retryable |
| `nil` | `register_push_token.write` (unchanged) | ack |

Every reply is byte-identical to today's except the reconcile-dropped case named above.

**SECURITY — why logging `err` unconditionally stays safe.** `readDevicesFile` wraps a
decode failure that can echo `devices.json` bytes, and a corrupt registry may carry a
`token_hash`. `Reload`'s error is therefore consumed *inside* the region and never
returned from the closure, so it cannot reach a log field at all; the reload-failure
branch logs `path` plus a static reason exactly as it does today. What can escape is
`WithLock`'s own errors (which name only the lock path, by its documented contract),
`Save`'s wraps (a path or a fixed step word), and `errPushDeviceGone` (static). That
closed set is what makes the `err` field on the failure branch safe, and a comment states
it so the next edit inside the region has to keep it true.

`recordRedemption` needs `errRedemptionReloadFailed` because it *returns* the reload
failure; this handler swallows it, which is the stronger form of the same property. No
sentinel is needed for it, and adding one would imply a return path the design forbids.

### Concurrency model

No goroutines. The handler runs on the dispatcher's frame goroutine; the added blocking is
bounded by `pushRegistryLockWait` and refuses rather than queues past it. Lock ordering is
unchanged and singular: the file lock is taken first, `Registry.mu` is taken and released
inside each registry call, and no registry method acquires the file lock — `WithLock`
stays `internal/devices`' sole acquirer, so there is no nesting and no second open file
description contending with its own caller.

### Other edits in the file

- `msgBinaryBusy`'s doc comment is wrong today: it claims the phone's retry dedupes
  because in-memory is already updated. Dedupe compares the payload against `c.Auth()`, a
  per-connection snapshot taken once at handshake (`s.device` in
  `internal/relay/v2session_handshake.go`) that no write updates — so the retry does
  re-attempt the write, which is what makes a busy-lock refusal meaningfully retryable.
  Corrected.
- `RegisterPushToken`'s `Concurrency:` paragraph claims `reg`'s mutex serialises
  `UpdatePushRegistration` and `Save` independently. That was the bug. Rewritten to state
  the region.

## Testing strategy

All new tests live in `internal/relay/handlers/register_push_token_test.go`; same-process
`flock(2)` contention is genuine (locks attach to the open file description, not the
process), so no second OS process and no daemon harness is needed. The lock-timing tests
are deliberately **not** `t.Parallel()` — they retune or read `pushRegistryLockWait` and
use a grace, and Go resumes parallel tests only after the sequential ones finish.

New:

- `TestRegisterPushToken_SurvivesWriteCommittedWhileParkedOnLock` (AC-2, both directions
  in one interleaving): memory and disk hold `[A, C]`. Park a holder on the lock, start
  the handler for A, assert it has *not* completed after a grace, commit `[A, B]` from
  under the holder (adds B, revokes C), release. Assert the ack, and that disk holds A
  carrying the push registration, holds B, and does not hold C. Reddens on the
  narrowed-to-`Save`-only mutant: that build reloads `[A, C]` before the lock is ever
  taken, so its `Save` erases B.
- `TestRegisterPushToken_ReconcileDropsDevice_RefusedNotAcked` (AC-3): memory `[A]`, disk
  `[B]` — A already revoked on disk. No goroutines needed. Assert the non-retryable
  `auth.invalid_token` reply and a back-dated mtime proving no `Save` ran. This is the one
  test that tells the two orderings apart: mutate-then-reconcile acks and writes `[B]`.
- `TestRegisterPushToken_LockBusy_EmitsServerBinaryBusyWithoutWriting` (AC-4): park a
  holder, run the handler, wait the bound out. Assert `server.binary_busy` with
  `Retryable: true`, a back-dated mtime, a distinct `register_push_token.lock_busy` log
  event, and — the no-leak half — that nothing captured from the logger contains the plain
  token or its hash.

Repaired (AC-5), each keeping its existing outcome assertions:

- `TestRegisterPushToken_SaveFailure_EmitsServerBinaryBusy` — replace the
  regular-file-at-the-parent blocker (which now fails at `WithLock`'s `MkdirAll`) with:
  seed `devices.json` in a real directory, pre-create the sidecar at `0600`, then `chmod`
  the directory to `0500`, with the existing root probe/skip. `MkdirAll` and the sidecar
  open both succeed, the reload and mutation run, and `Save`'s `CreateTemp` is what fails.
  Add one assertion that the logged error carries `Save`'s fixed step word, so the test
  cannot silently re-hollow at the sidecar open.
- `cmd/pyry`'s `TestRunPairRevoke_SaveFailure` — pre-create the sidecar at `0600` before
  the `chmod 0500`, and assert the same way.

Extended for AC-1's "a deduped or refused frame creates no sidecar": a shared
`assertNoSidecar` helper, folded into `assertRejectedNoWrite` (covering every
display-safety reject) and called from the dedupe, unauth and malformed tests. The
sidecar's absence is a strictly stronger claim than "no `Save` ran" — `WithLock` creates
it before running anything the caller passed.

`TestRegisterPushToken_ReloadPreventsClobberOfNewlyPairedDevice` keeps passing untouched.

Gate: `go test -race ./internal/relay/... ./internal/devices/... ./cmd/pyry/...`,
`go vet ./...`, `go build ./cmd/pyry`.

## Open questions

1. Does the reconcile-first ordering change the outcome of any currently-green test that
   seeds no `devices.json`? Under ENOENT→empty the reload empties membership, so such a
   frame is now refused rather than acked-with-an-empty-file. Resolve by running the
   package suite; expected to touch only `TestRegisterPushToken_SaveFailure_EmitsServerBinaryBusy`,
   which is being repaired anyway.
2. Does `chmod 0500` on the parent leave the pre-created sidecar openable? `O_CREATE` on an
   existing file needs only `x` on the directory, so it should; confirm empirically rather
   than by reading, since it is the whole mechanism of the AC-5 repair.

## Documentation handoff

Owned by the documentation stage, not this builder. Pending:

- `docs/knowledge/features/devices-registry.md` § *Two-writer clobber guard* — its
  2026-09-07 (#1531) update ends "`register_push_token` is now the one writer left
  unlocked (#1532), so the headline daemon-vs-CLI race is still open". Flip it: every
  `devices.json` writer now holds the lock; say what that closes in both directions.
- `docs/knowledge/features/pyry-pair-command.md` § *Concurrency* and the
  `TestRunPairRevoke_SaveFailure` entry under § *Tests* — the deferred save-failure repair
  is no longer deferred; record what the repaired test exercises.
- `docs/knowledge/decisions/029-devices-registry-reload-at-handshake.md` — the
  § *Correction (2026-08-19, #1530)* rationale and the "the next reload reconciles it"
  consequence are superseded. Record that the reload survives as the in-region read rather
  than as the guard, and why.
- `docs/specs/architecture/1531-pair-writers-through-devices-lock.md` names
  `RegisterPushToken` as the writer left for this ticket; that sentence is now spent.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, by a decision worth stating.** The untrusted→trusted
  boundary is `json.Unmarshal` into `RegisterPushTokenPayload` plus the three
  display-safety guards, all of which stay *ahead* of the region; nothing client-authored
  crosses into it except the already-gated `p.Platform` / `p.Token` / `p.DeviceName`
  values. Moving the reconcile ahead of the mutation does let a remote frame trigger a
  disk-into-memory adoption earlier than before, but the adoption source is `devices.json`
  — an operator-owned 0600 file — and `reconcileDevices` makes membership exactly disk's
  set, so the accept set can only narrow relative to disk, never widen. `dev.TokenHash`
  is the handshake's own snapshot and is never taken from the payload.
- **[Tokens] OUT OF SCOPE — a revoked device is still written back to disk when the
  in-region reload *fails*.** `Reload` leaves memory unchanged on a read error, so the
  best-effort path then saves a snapshot that may still contain a record `pyry pair
  revoke` removed. The behaviour is pre-existing (today's post-mutation reload has the
  same hole), AC-1 explicitly mandates the best-effort posture that keeps it, and reaching
  it requires a corrupt or unreadable `devices.json`, which is not remotely inducible —
  the file is 0600 and written only by temp-file-plus-rename. `recordRedemption` made the
  opposite choice for its own write and is the model if this is ever revisited. Not this
  ticket's to change: doing so would contradict a stated acceptance criterion. Successful
  reloads — the overwhelming case, and the one this slice is about — now refuse instead,
  which is a strict improvement to protocol-mobile § Security model threat 4's per-device
  revocation.
- **[Tokens] No findings on handling.** The plain token never enters the region and is
  never a log field; `dev.TokenHash` is a lookup key only. The lock-busy test asserts
  directly that neither appears anywhere in the handler's log output.
- **[File operations] No findings.** `registryPath` is closure-captured at wiring time from
  `resolveDevicesPath` and is not client-influenced, so there is no traversal surface.
  Modes are `WithLock`'s and `Save`'s (sidecar 0600, directory 0700, registry 0600), both
  unchanged. This slice *removes* a TOCTOU rather than adding one: the reconcile→save gap
  is now inside the region. `WithLock` opens the sidecar without `O_NOFOLLOW`; that is
  pre-existing, shared by all three current callers, and out of scope here.
- **[Subprocess] Not applicable.** The handler executes nothing and touches no environment.
- **[Cryptographic primitives] Not applicable.** No primitive is added or moved. The
  `TokenHash == TokenHash` comparison inside `UpdatePushRegistration` is hash↔hash, which
  `FindByTokenHash` already documents as not requiring constant time; `VerifyToken` owns
  the plain↔hash boundary and is not on this path.
- **[Network & I/O] SHOULD FIX, honoured in Phase B: the new busy branch must not log
  `device_name`.** Every pre-#2219 authenticated branch logs `dev.Name`, but that value
  is the pre-gate residual and may carry control characters; #2219's three reject branches
  deliberately log neither it nor the payload's value. A brand-new branch inherits the
  strict posture, so `register_push_token.lock_busy` logs `event`, `conn_id`, `path` and
  `err` only.
- **[Network & I/O] OUT OF SCOPE — no per-conn rate limit on this frame.** The region adds
  a bounded 250ms stall to the session's frame goroutine, matching `redemptionLockWait`'s
  already-shipped choice on the same goroutine, and refuses rather than queues past it. A
  phone that sends a changed triple repeatedly forces a `Save` per frame and can keep the
  sidecar contended, slowing `pyry pair` (which waits the full `DefaultLockWait` = 5s
  against sub-millisecond regions, so starvation needs sustained saturation). The absence
  of rate-limiting predates this slice and is named as deferred by protocol-mobile
  § Security model threat 7.
- **[Error messages, logs, telemetry] No findings, structurally.** `Reload`'s error — the
  only one that can echo `devices.json` bytes and thus a `token_hash` — is consumed inside
  the region and never returned from the closure, so it cannot reach a log field. The set
  that *can* escape is closed and safe: `WithLock`'s errors name only the lock path by its
  documented contract, `Save`'s wraps name a path or a fixed step word, and
  `errPushDeviceGone` is static. Replies are unchanged and static throughout.
- **[Concurrency] No findings, but the reason is non-obvious and gets a comment.**
  `Registry.mu` is released between `Reload`, `UpdatePushRegistration` and `Save`, so an
  in-process peer can interleave at those boundaries. Every *writer* peer holds the same
  file lock and therefore cannot; the one that does not is the v2 handshake's own
  `Reload`, and it is harmless precisely because `reconcileDevices` keeps the in-memory
  survivor for a hash present in both — it reconciles to the same set the region already
  holds and preserves this handler's mutation. Lock ordering is single-directional (file
  lock, then `r.mu` inside each call) with no nesting, since `WithLock` is
  `internal/devices`' sole acquirer. No goroutine is spawned. A process killed mid-region
  leaves `devices.json` pre- or post-rename and the kernel drops the flock on fd close.
- **[Threat model alignment] Addresses `docs/protocol-mobile.md` § Security model threat 4
  (token leak via phone, mitigated by per-device revocation)** — this is the write that
  could previously resurrect a revoked record or erase a freshly paired one. Threat 7
  (denial of service) stays deferred as that section already records; threats 1, 2, 3, 5,
  6 and 8 are untouched by a change confined to one daemon-side persist.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-15

## Revisions

**2026-09-15 — Open questions resolved, design unchanged.** Recorded rather than
folded in, since neither answer moved a contract.

1. *Did the reconcile-first ordering disturb a currently-green test?* Only
   `TestRegisterPushToken_SaveFailure_EmitsServerBinaryBusy`, which AC-5 was
   repairing regardless. Every other test in the suite seeds a real `devices.json`
   before driving the handler and passed untouched, including
   `TestRegisterPushToken_ReloadPreventsClobberOfNewlyPairedDevice`.
2. *Does `chmod 0500` leave a pre-created sidecar openable?* Yes, confirmed
   empirically rather than by reading. Both repaired tests were mutation-checked by
   deleting the pre-creation: each then fails at `devices: open lock ... permission
   denied`, which is exactly the hollowed-out shape AC-5 names, so the new step-word
   assertions are non-vacuous.

The interleaving test was mutation-checked the same way, against the mutant AC-2
names: with `Reload` and `UpdatePushRegistration` hoisted out so only `Save` runs
under the lock, `TestRegisterPushToken_SurvivesWriteCommittedWhileParkedOnLock`
fails on both directions at once — device B erased, device C resurrected — while its
grace assertion still passes, which is the concrete demonstration that a busy-lock
test could not have caught it.
