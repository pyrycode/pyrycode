# 2396 — Offline paired-device fixture

## Files read

- `internal/identity/store.go` → `LoadOrCreate` — defines the persisted per-instance server identity and warm-load contract.
- `internal/keys/store.go` → `LoadOrCreate` — validates daemon names before filesystem access and owns secure static-key persistence and reuse.
- `internal/keys/static_key.go` → `PublicKey` — provides the public X25519 point for the pairing payload without exposing private key material.
- `internal/devices/device.go` → `Device`, `RedemptionWindow`, `HashToken`, `VerifyToken` — defines the stored credential shape, label fallback input, redemption timing, and plain-token verification boundary.
- `internal/devices/registry.go` → `Load`, `Registry.Add`, `Registry.Save`, `Registry.List` — supplies independent load snapshots and atomic registry persistence.
- `internal/pair/payload.go` → `Payload`, `Encode` — defines the returned phone bootstrap tuple and the encoded secret whose absence is asserted on failure.
- `cmd/pyry/pair.go` → `mintDevice`, `runPairDefault` — establishes the existing 256-bit token, single timestamp, generated-label, persist-before-egress, and payload composition pattern.
- `internal/e2e/internal/fakephone/fakephone.go` → `Dial` — shows the visibility-fenced, importable e2e-helper package pattern this fixture follows.
- `docs/knowledge/features/pyry-pair-command.md` → “`pyry pair` (bare) — operation order” and “Token visibility” — records the security invariants the fixture must preserve while bypassing the CLI.
- `docs/knowledge/features/devices-registry.md` → “Save concurrency” and “Testing a best-effort, lock-guarded persist” — documents atomic-save behavior and failure-proof expectations.
- `docs/knowledge/features/development-verification.md` → fixture validation guidance — requires field-level assertions and non-vacuous fixture checks.

## Context

Integration suites need a valid phone credential before the daemon under test starts. Once the operator-facing pairing command becomes a live-daemon client, invoking that binary can no longer seed pre-daemon state. A visibility-fenced package under `internal/e2e/internal/` will compose the existing production persistence and cryptographic contracts directly, without adding an offline production path or executing a subprocess.

The size remains within one ticket: one production-language source file in a test-only package, one test file, about 450 total written lines including this plan, two new exports, no existing consumer updates, three acceptance criteria, and no state-machine reject fan-out.

## Design

Add package `internal/e2e/internal/paireddevice` with a small public surface:

```go
type Config struct {
    Home, InstanceName, Relay, DeviceName string
    AllowRemotePermissions bool
}

func Setup(Config) (pair.Payload, error)
```

`Home` is the isolated user home, not the `.pyry` directory. `Setup` requires it to be absolute and requires a non-empty relay before writing. It derives `<home>/.pyry/<instance>/` using `filepath.Join`.

The operation runs in this order:

1. Reject a non-absolute home or empty relay with a zero payload.
2. Call `keys.LoadOrCreate(<home>/.pyry, instance)` before interpolating the instance into any other path. Its daemon-name allowlist is therefore the instance trust boundary: invalid or traversal-shaped names fail before any instance-derived filesystem access.
3. Load or create the server identity at the validated instance directory.
4. Read 32 bytes from `crypto/rand.Reader`, hex-encode the plaintext token, hash it through `devices.HashToken`, and apply the existing `device-` plus eight-character hash-prefix fallback when the label is empty.
5. Read one UTC timestamp and set `PairedAt` and `RedeemBy = PairedAt + devices.RedemptionWindow`; leave `LastSeenAt` at its standard zero value and copy the permission choice.
6. Load the current registry, append one device, and call `Registry.Save`. Because `Load` creates an independent snapshot and `Save` commits by atomic rename, repeats preserve prior entries and a failed save leaves the previous file unchanged.
7. Only after persistence succeeds, construct and return `pair.Payload` from the stored identity, requested relay, plaintext token, and base64-encoded static public key.

The package does not call `pair.Encode`, render a QR code, execute `pyry`, acquire daemon state, or expose any production CLI entry point. A private `setup` helper accepts the random reader and registry-save function used by focused same-package tests. The exported `Setup` fixes those dependencies to `crypto/rand.Reader` and `Registry.Save`; callers cannot weaken randomness or bypass persistence.

## Concurrency model

The fixture is synchronous and spawns no goroutines. It is intended for pre-daemon setup in an isolated test home. Concurrent calls against the same instance are outside its contract because `identity.LoadOrCreate` and `keys.LoadOrCreate` are themselves documented as non-concurrent; sequential repeats are supported. Registry load, append, and save occur in one call stack, with no in-process lock nesting.

## Error handling

Every failure returns `pair.Payload{}`. Errors add only operation context and wrap existing errors; neither the plaintext token nor a `pair.Encode` result is ever included. Identity and static-key creation can precede a later registry failure, but no usable pairing exists until the token hash is committed. The atomic `Registry.Save` contract preserves the prior registry bytes when persistence fails.

The private dependency seam lets the test force the save boundary to return a sentinel after a known token has been generated. This proves the fixture's failure contract deterministically without changing production persistence APIs or relying on filesystem permission behavior.

## Testing strategy

- Table-driven first-call cases cover explicit and generated labels and both values of `AllowRemotePermissions`. Each case loads the persisted identity, key, and registry; checks the payload fields; verifies the plaintext token against the stored hash; and asserts the standard timestamp relationship.
- A repeat case calls `Setup` twice for one instance, checks stable server identity and public key, verifies both independent tokens against their matching records, and compares the first stored record before and after the append.
- A forced-save-failure case seeds one successful record, snapshots the registry bytes, then invokes the private helper with a deterministic token reader and a failing save function. It asserts the sentinel error, zero payload, unchanged bytes and logical registry, and absence of the known plaintext token and its complete encoded pairing from the error and every persisted file.
- Phase B follows RED then GREEN. The touched-scope gate is `go test -race ./internal/e2e/internal/paireddevice/...`, followed by `go vet ./...` and `go build ./cmd/pyry`.

## Open questions

None.

## Documentation handoff

The issue contains no documentation-only acceptance criterion. The later documentation stage may add the new fixture package to the owning e2e feature overview; no shared documentation is changed by the builder.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `Setup` rejects a relative home and empty relay, while `keys.LoadOrCreate` validates `InstanceName` before any instance-derived path is constructed. Device labels retain the existing pairing contract and are test-authored input inside the e2e-only visibility fence.
- [Tokens, secrets, credentials] No findings — `Setup` uses 256 bits from `crypto/rand.Reader`, persists only `devices.HashToken`, returns the plaintext only after a successful save, and never passes it to an error constructor. Revocation and post-start redemption remain owned by existing daemon and registry paths.
- [File operations] No findings — identity, static key, and registry writes reuse their existing mode-controlled atomic writers. `keys.LoadOrCreate` retains its secure-directory and `O_NOFOLLOW` checks, and `Registry.Save` retains its temp-file, sync, close, and rename commit sequence.
- [Subprocess / external command execution] No findings — the package executes no command and does not invoke the Pyry binary.
- [Cryptographic primitives] No findings — randomness comes from `crypto/rand.Reader`, X25519 key generation remains in `keys.LoadOrCreate`, token hashing and constant-time verification remain in `devices.HashToken` and `devices.VerifyToken`, and no new primitive is introduced.
- [Network & I/O] No findings — the fixture performs no network I/O; `Relay` is carried as an opaque non-empty destination exactly as in `pair.Payload`.
- [Error messages, logs, telemetry] No findings — there is no logger or telemetry. The deterministic failure test knows the generated secret and checks both the error and persisted files for the plaintext and encoded pairing.
- [Concurrency] No findings — no goroutine or nested lock is added. The documented pre-daemon, sequential-call contract matches the concurrency limits of `identity.LoadOrCreate` and `keys.LoadOrCreate`.
- [Threat model alignment] No findings — the design preserves the mobile pairing model's hashed-at-rest bearer token, persistent server identity/static key, finite redemption window, and persist-before-egress properties. An offline operator CLI path is explicitly not added.

**Reviewer:** builder (self-review per the security-review checklist)  
**Date:** 2026-09-12

## Revisions

None.
