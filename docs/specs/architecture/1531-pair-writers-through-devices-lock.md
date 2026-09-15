# #1531 — Route the `pyry pair` mint and revoke writers through the `devices.json` lock

## Files read

- `cmd/pyry/pair.go` → `runPairDefault`, `runPairRevoke` — the two unlocked writers this ticket retrofits; `runPairList`, `runPairPreflight` — the read-only verbs AC4 pins as lock-free; `resolveDevicesPath` — the per-instance path both writers already share, which makes the sidecar per-instance for free.
- `internal/devices/lock.go` → `WithLock`, `DefaultLockWait`, `ErrLockBusy` — the primitive, its wait-bound advice ("a caller on a request path should choose a tighter bound"), its no-nesting rule, and its documented "fn's error is returned verbatim, never wrapped" contract, which is what lets a not-found sentinel travel out of the region.
- `internal/relay/v2session_redemption.go` → `recordRedemption`, `redemptionLockWait`, `errRedemptionReloadFailed` — the sole in-tree `WithLock` caller and the shape to copy; its doc comment is also the claim this ticket must not falsify (it asserts `pyry pair` and `pyry pair revoke` "each hold sub-millisecond regions").
- `internal/devices/registry.go` → `Load`, `Save`, `Add`, `Remove`, `readDevicesFile` — the read-modify-write triple that moves inside the region, and the SECURITY rule on `readDevicesFile` about decode errors beside a `token_hash`.
- `internal/devices/lock_test.go` → `holdLock` — the background-holder idiom the CLI contention tests mirror; `flock(2)` contends per open file description, so an in-process second holder is a real contender.
- `cmd/pyry/pair_test.go` → `TestRunPairRevoke_RemovesEntry`, `TestRunPairRevoke_SaveFailure`, `TestRunPairDefault_PopulatesStaticPubkey`, `captureStdout` — the existing behaviour-unchanged coverage (AC5) and the stdout capture helper the mint tests reuse.
- `docs/knowledge/features/devices-registry.md` § "Testing a best-effort, lock-guarded persist" — the two reusable witnesses: back-date mtime with `os.Chtimes` so "no `Save` ran" is non-vacuous (`Save` is idempotent, so bytes alone prove nothing), and the sidecar's absence as the witness for "the region was never entered". § "Two-writer clobber guard" — why reloading *inside* the region, not only before it, is the whole point.

## Context

`~/.pyry/<name>/devices.json` is the paired-device credential store, rewritten whole by `Save`'s temp-file + rename. `Registry.mu` serializes goroutines within one process; the `pyry` CLI and the daemon are separate processes, so it buys nothing across them. Four writers exist. `recordRedemption` locks (#1528). `RegisterPushToken` is #1532's. The two here — mint (`runPairDefault`) and revoke (`runPairRevoke`) — read a snapshot, mutate it, and save it with no cross-process exclusion and no re-read.

Mint carries the widest window in the system: between its `Load` and its `Save` it runs `identity.LoadOrCreate`, `keys.LoadOrCreate` (which mints and persists a fresh keypair on cold start), and a CSPRNG read. Any write landing inside that window is erased when mint saves its stale snapshot.

**This slice does not close the daemon-vs-CLI race.** `RegisterPushToken` stays unlocked until #1532 (closed — see [`features/devices-registry.md`](../../knowledge/features/devices-registry.md) § *Two-writer clobber guard*). What it does close is CLI-vs-CLI in both directions — including the security-relevant one, where a `pyry pair` racing a `pyry pair revoke` resurrects a credential the operator explicitly withdrew — and CLI-vs-`recordRedemption`, since both sides now lock.

No ADR is warranted: ADR 029 already owns the reload-at-handshake decision and carries the 2026-08-19 correction that motivates `WithLock`. This ticket is a call-site retrofit under a decision already recorded.

## Design

One production file, `cmd/pyry/pair.go`. No new exported symbols, no signature changes, no consumer cascade.

**A shared wait bound.** A package-level `pairLockWait`, initialised to `devices.DefaultLockWait`. `WithLock`'s doc reserves a tightened bound for request-path callers and points everyone else at the default; an operator-invoked one-shot is not a request path. It is a `var` rather than a `const` so the timeout-path test can shrink it — a contention test that waits the real bound out costs the suite five seconds. Unexported; production never reassigns it, and the doc comment says so.

**A not-found sentinel.** `errPairDeviceNotFound`, unexported, package-level. `runPairRevoke` currently calls `os.Exit(1)` between its `Load` and its `Save`; leaving that inside the region would skip `WithLock`'s deferred unlock and close. The kernel releases every `flock` a process holds at termination, so the lock itself cannot leak — but any *other* deferred cleanup inside the region would, and `WithLock` returns `fn`'s error verbatim, so signalling out of the region and exiting outside it costs one sentinel.

**Mint —** `runPairDefault`. Flag parse, `config.Load`, relay resolution, `identity.LoadOrCreate`, `keys.LoadOrCreate`, the CSPRNG read, `HashToken`, and the device-name default all stay where they are, outside the region. Only the read-modify-write moves inside:

```go
err = devices.WithLock(devicesPath, pairLockWait, func() error {
    registry, err := devices.Load(devicesPath)   // read INSIDE the region
    if err != nil {
        return err
    }
    registry.Add(devices.Device{ /* fields unchanged */ })
    return registry.Save(devicesPath)
})
if err != nil {
    return fmt.Errorf("pair: %w", err)
}
```

The `Load` must move *after* key generation, not merely be wrapped where it sits: acquiring the lock around the existing `Save` alone would leave the stale-snapshot bug fully intact. Keeping `identity.LoadOrCreate` and `keys.LoadOrCreate` outside is what keeps `redemptionLockWait`'s shipped claim about sub-millisecond peer regions true; neither touches `devices.json`. The CSPRNG read stays outside too — there is no reason to hold a cross-process lock across a possible entropy stall.

**Revoke —** `runPairRevoke`. Same shape, with the sentinel carrying not-found out:

```go
err = devices.WithLock(devicesPath, pairLockWait, func() error {
    registry, err := devices.Load(devicesPath)
    if err != nil {
        return err
    }
    if !registry.Remove(parsed.deviceName) {
        return errPairDeviceNotFound
    }
    return registry.Save(devicesPath)
})
switch {
case errors.Is(err, errPairDeviceNotFound):  // stderr line + os.Exit(1), outside
case err != nil:                             // fmt.Errorf("pair revoke: %w", err)
}
```

Discrimination is by sentinel value under `errors.Is`, never by matching the printed string, so a device whose `Name` happens to read like the not-found line cannot spoof the branch. A busy lock arrives as `ErrLockBusy`, not the sentinel, so it falls to the error branch and never prints "no device named".

**List and preflight are untouched.** `runPairList` and `runPairPreflight` never call `Add`, `Remove`, or `Save`; taking the lock in a reader would let a writer block a listing for the full bound and would create the sidecar as a side effect. Their absence of a `WithLock` call is the mechanism behind AC4, and the sidecar's non-existence is its witness.

**Imports:** `errors` is added to `cmd/pyry/pair.go`. Nothing is removed.

## Concurrency model

No goroutines are spawned in production code. The concurrency is entirely the cross-process file lock.

**Lock ordering: file lock → `Registry.mu`.** `WithLock` acquires the sidecar's `flock` first; `Load`, `Add`, `Remove`, and `Save` take `Registry.mu` internally, strictly inside. This is the same order `recordRedemption` establishes and the only order reachable, because `WithLock` is the sole acquirer in the `devices` package and `Load`/`Save`/`Reload` never take it. No inversion is possible.

**No nesting.** `WithLock`'s doc makes nesting on the same path a contract violation — a nested call opens a second file description and, since `flock(2)` contends per description, would deadlock against its own caller until the bound elapsed. Nothing inside either closure calls `WithLock`: `Load`, `Add`, `Remove`, and `Save` are all lock-free with respect to the sidecar.

**Region length.** Mint's region is one file read, one slice append, and one atomic rename. Revoke's is a read, a linear scan, and a rename. Both are file-I/O-bounded and free of key generation, which is what `redemptionLockWait`'s doc comment already promises its peers do.

**Interleavings this closes.** Mint-vs-revoke in both directions (neither can now build on a snapshot the other has replaced); mint-vs-mint; revoke-vs-`recordRedemption`. What it does not close: anything involving `RegisterPushToken`, which is #1532's (shipped — every `devices.json` writer now holds the same lock).

**Crash mid-region.** The kernel releases the `flock` when the process's descriptors close, including on `SIGKILL`, so there is no stale lock and no TTL. `Save` is atomic, so `devices.json` is left either pre- or post-rename, never partial.

## Error handling

| Failure | Surface | Exit |
|---|---|---|
| Lock not acquired within `pairLockWait` | `WithLock`'s `devices: acquire lock <path>.lock: devices: lock busy`, wrapped `pair:` / `pair revoke:`, prefixed `pyry: ` by `main.run` | 1 |
| `devices.Load` fails inside the region (corrupt or unreadable) | Unchanged — `WithLock` returns it verbatim, the existing wrap applies | 1 |
| `registry.Save` fails inside the region | Unchanged, same wrap | 1 |
| Revoke, no device named `<name>` | Sentinel out of the region; the existing `pyry pair revoke: no device named <name>` line, byte-identical | 1 |
| Flag parse / usage | Untouched, before any lock | 2 |

The busy-lock message names the lock path and a static reason, which is what AC3 asks for; no new formatting code is needed to get it. Because `fn` never runs on a failed acquisition, `devices.json` is byte-unchanged — and, more strongly, not even opened.

**Fail-closed on mint.** If acquisition fails after the token has been minted, `plain` is discarded without being persisted or rendered. A token whose hash never reached disk is unusable, so the failure mode is a wasted token, not an unrecorded credential.

**No new echoing surface.** `WithLock` names only the lock path. The sentinel is a static value and is never printed. The one error that can carry decoded registry bytes — `Load`'s — is pre-existing, unchanged, and required unchanged by AC5; see § Security review for why `errRedemptionReloadFailed`'s substitution is not replicated here.

**Two ordering deltas worth naming.** Moving mint's `Load` after key generation means a corrupt `devices.json` now fails *after* `identity.LoadOrCreate` and `keys.LoadOrCreate` have persisted their artefacts rather than before. Both are idempotent, and a static keypair with no paired device grants nobody anything. Separately, `WithLock` creates the instance directory at `0700` on a cold start, a job that previously fell to `identity.LoadOrCreate` and `Save`; same mode, same directory.

## Testing strategy

All in `cmd/pyry/pair_test.go`. A local `holdPairLock` helper mirrors `holdLock` from `internal/devices/lock_test.go` — a background goroutine that enters a `WithLock` region and parks until released, with the release idempotent and registered via `t.Cleanup`. Its wait for the holder to unwind is bounded rather than open-ended, so a failing test reports its own failure instead of hanging the package.

- **Mint reads its snapshot inside the region.** The flagship test, and the one that separates this design from wrapping `Save` alone. Hold the lock; start `runPairDefault` on a goroutine; assert it has *not* completed after a short grace (a completion while another holder has the lock proves no region covers the write); commit a second device to `devices.json` from under it; release. Assert the final registry holds both that device and the minted one. Fails deterministically two ways: with no lock at all, mint completes during the grace; with the lock around `Save` only, mint's pre-lock `Load` misses the second device and its `Save` erases it, leaving one entry instead of two.
- **Revoke reads its snapshot inside the region.** The same interleaving against `runPairRevoke`: a device added while revoke is blocked must survive the revoke of a different one.
- **Mint refuses a busy lock without writing.** Shrink `pairLockWait` so the bound is reachable; hold the lock; run `runPairDefault`; assert a non-nil error naming the sidecar path, and that `devices.json` is unchanged in both bytes *and* mtime — back-dated with `os.Chtimes` first, since `Save` is idempotent and a byte comparison alone passes vacuously.
- **Revoke refuses a busy lock without writing.** Same, against `runPairRevoke`.
- **List and preflight take no lock.** Against an instance directory with a saved registry and no sidecar, run each verb and assert `os.Stat` of the sidecar still returns `fs.ErrNotExist`. Preflight runs against an empty registry so it takes its exit-0 path.

**AC5 is carried by the existing suite.** `TestRunPairDefault_PopulatesStaticPubkey`, `TestRunPairDefault_AllowRemotePermissionsPersists`, and `TestRunPairDefault_StampsRedeemBy` pin the mint stdout payload and the persisted record; `TestRunPairRevoke_RemovesEntry` and `TestRunPairRevoke_PreservesRedeemByOnSurvivors` pin `Revoked <name>.` and the survivor's fields. All must stay green unmodified. `TestRunPairRevoke_SaveFailure` also stays green but now fails one step earlier — with the parent directory at `0500`, `WithLock` cannot create the sidecar, so the error is an open-lock failure rather than a `Save` failure; it still carries the `pair revoke:` prefix the test asserts. The exit-1 not-found and exit-2 usage paths call `os.Exit` and are not reachable in-process; they are covered by keeping their strings and ordering byte-identical, not by new tests.

## Open questions

1. **`var` or `const` for `pairLockWait`?** Resolved in favour of `var`: the ticket notes a contention test that waits the real bound out costs the suite the full default, and there is no other seam. Kept unexported with a doc comment stating production never reassigns it.
2. **Should mint's `Load` become `Reload` on a registry loaded earlier, mirroring `recordRedemption`?** Resolved in favour of moving `Load` wholesale. `recordRedemption` reloads because the daemon holds a long-lived `*Registry` it must preserve `LastSeenAt` on; the CLI is a one-shot with no earlier registry to reconcile into, so a plain `Load` inside the region is both simpler and strictly equivalent.
3. **Does the grace period in the interleaving tests make them timing-dependent?** To confirm in Phase B by running the touched package repeatedly. The grace only needs to exceed the uncontended cost of mint's non-lock work (a keypair load and a CSPRNG read); the *green* path does not depend on it at all, since a correct implementation blocks until released.

## Revisions

**2026-09-07 — Open question 3 resolved, no design change.** The interleaving tests are not timing-dependent in practice. `-race -count=8` over both `ReadsSnapshotInsideLock` cases plus the busy-lock and read-verb cases is green with no races, and the grace window is only load-bearing on the red side: a correctly locked verb stays parked until the holder releases, so the green path never consults it.

The discriminating power claimed for `TestRunPairDefault_ReadsSnapshotInsideLock` in § Testing strategy was verified rather than assumed, by running it against a mutant that hoists mint's `devices.Load` back outside the region so the lock wraps `Add` + `Save` only (`go test -overlay`, no worktree writes). The mutant fails on exactly the intended assertion — the device committed while the mint was parked is erased, leaving two entries instead of three — which is the failure mode the ticket warns "would leave the stale-snapshot bug fully intact".

Implementation carried the plan unchanged otherwise. `internal/devices/registry_test.go` is not `gofmt`-clean on `origin/main`; it is outside this ticket's diff and was left alone.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new findings. The only boundary in scope is file → memory at `readDevicesFile`, which both `Load` calls funnel through and which this ticket does not touch. The change moves *when* mint crosses it (after key generation rather than before), not *how* or *where*. Downstream holders are unchanged: `Add` and `Remove` receive the same `*Registry` type they always did.
- **[Tokens, secrets, credentials]** No MUST FIX. The plaintext token now lives in `runPairDefault`'s frame across a region that can block for up to `pairLockWait`, widening the in-memory window. This adds no disclosure surface: `plain` is never logged, never written to any error, and the process is a short-lived operator one-shot. Generation stays `crypto/rand` and stays outside the region. The failure mode is fail-closed — a mint that cannot acquire discards a token whose hash never reached disk, which is unusable rather than unrecorded. The default device name remains `device-` plus a `TokenHash` prefix, already the surface `renderPairList` prints as `TOKEN-PREFIX`. Revocation granularity is per-device and unchanged; this ticket strengthens it, since a revoke can no longer be undone by a racing mint.
- **[File operations]** No MUST FIX. Path traversal is closed upstream: `resolveDevicesPath` runs `sanitizeName` over `-pyry-name`, and `WithLock` derives the sidecar by appending `.lock` to that already-sanitized path, so a `-pyry-name=../../etc` cannot reach an unintended inode. Modes are `WithLock`'s own — directory `0700`, sidecar `0600` — matching `Save`. The ticket *removes* a TOCTOU rather than adding one: the `Load`-then-`Save` gap is exactly what the region closes. **OUT OF SCOPE:** the sidecar is opened without `O_NOFOLLOW`, so a symlink planted at `devices.json.lock` would be followed; this is `WithLock`'s property, shipped by #1530, and an attacker who can write into a `0700` directory already owns the credential store beside it.
- **[Subprocess / external command execution]** Not applicable — neither verb execs anything, and no path in the diff constructs a command line.
- **[Cryptographic primitives]** No findings. `crypto/rand.Read` and `devices.HashToken` are unchanged and both stay outside the region. No key or nonce is reused: `keys.LoadOrCreate`'s static keypair is untouched and merely reordered ahead of the registry write. No comparison against a secret happens here — `Validate`'s constant-time discipline lives in `internal/devices/auth.go` and is not in scope.
- **[Network & I/O]** Not applicable — a CLI one-shot with no sockets, no server, no reads from untrusted input. The only unbounded-wait risk is lock acquisition, and it is bounded by `pairLockWait`. A local process could hold the sidecar to deny pairing, but that requires write access inside the operator's own `0700` instance directory, and the denial is fail-closed.
- **[Error messages, logs, telemetry]** No MUST FIX. No logging is added; the CLI has none. `WithLock`'s errors name the lock path and a static reason by its own documented contract. `errPairDeviceNotFound` is a static sentinel matched by `errors.Is` and never printed. The one surface that can carry decoded registry bytes is `Load`'s wrapped decode error — and **`errRedemptionReloadFailed`'s substitution is deliberately not replicated here**, for two reasons that should be stated rather than assumed: the sink is the invoking operator's own stderr rather than a persisted daemon log, and that operator already holds read access to `devices.json`; and AC5 requires the existing error surface unchanged, with `TestRunPairPreflight_CorruptRegistry` pinning a sibling verb's version of it. Anyone adding a *new* error or log surface around this region inherits the `readDevicesFile` rule and must substitute.
- **[Concurrency]** No MUST FIX; one design decision recorded because getting it wrong is the trap this ticket walks past. Lock ordering is file lock → `Registry.mu`, consistent across both call sites and matching `recordRedemption`; no inversion is reachable, because `WithLock` is the `devices` package's sole acquirer and `Load`/`Save` never take it. No nesting: nothing inside either closure calls `WithLock`. **Addressed in the design:** `runPairRevoke`'s `os.Exit(1)` sits between its `Load` and its `Save` today, and leaving it inside the region would skip `WithLock`'s deferred unlock and close — survivable, since the kernel releases the `flock` at process termination, but it would establish a pattern in which a future in-region `os.Exit` skips a cleanup that does matter. The sentinel moves the exit outside. Shutdown mid-region leaves `devices.json` pre- or post-rename, never partial. No production goroutines; the test helper's is bounded and released at cleanup.
- **[Threat model alignment]** The CLI-vs-CLI revoke-resurrection direction — a credential the operator explicitly withdrew becoming acceptable again — is closed by this slice, as is CLI-vs-`recordRedemption`. **OUT OF SCOPE at the time of this review, since fixed:** the headline daemon-vs-CLI race, because `RegisterPushToken` remained unlocked until #1532, which shipped it and carries the end-to-end proof. `docs/protocol-mobile.md`'s TOCTOU concern about the registry is addressed by the atomic-rename pattern already in `Save`, unchanged here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07
