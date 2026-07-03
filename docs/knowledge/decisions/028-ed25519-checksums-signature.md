# ADR 028 — Raw Ed25519 detached signature over the `pyry update` checksums file

**Status:** Accepted (2026-07-03, #776) · **Severity of the gap it closes:** LOW (operator-invoked over HTTPS) with a high blast radius (arbitrary code over the running binary).

## Context

`pyry update` downloaded `checksums.txt` from the GitHub release, parsed the expected SHA-256 for the host asset, and verified the downloaded tarball against that digest. Nothing verified `checksums.txt` *itself* — integrity rested **entirely** on "GitHub served both files honestly." A compromised release pipeline or a stolen publishing token could serve a matching `(tarball, checksums.txt)` pair, and the SHA-256 check would pass against attacker-chosen bytes. The blast radius is arbitrary code executing over the running binary (`AtomicReplace` → daemon restart).

Filed from the Cross-Repo Code Review 2026-07-03, finding F4 (integrity half). The companion unbounded-read/OOM half of F4 was fixed separately via `io.LimitReader` (`internal/update/fetch.go` `maxAssetBytes`) and is out of scope here.

## Decision

Add a **detached raw 64-byte Ed25519 signature over the exact bytes of `checksums.txt`**, published as `checksums.txt.sig`, verified with `crypto/ed25519.Verify` against a public key **compiled into the pyry binary** (`releaseSigningPublicKeyHex` in `cmd/pyry/update.go`). The gate runs *before* any digest is parsed from the checksums file. The release pipeline signs `checksums.txt` in CI via `openssl pkeyutl -sign -rawin`.

## Rationale (and alternatives rejected)

- **Ed25519 / stdlib over `sigstore/cosign` or `x/crypto/openpgp`.** Verification is `crypto/ed25519.Verify(pub, data, sig)` — pure standard library, **zero new module dependencies**. Matches the project's "stdlib over dependencies" principle and the ticket's explicit steer.
- **A bare raw signature over the minisign `.minisig` envelope.** minisign carries a 2-byte algorithm tag, an 8-byte key id, a trusted-comment, and a global signature; modern minisign *prehashes* with BLAKE2b-512 by default (`ED` algorithm), which would force a new hash dependency or fighting minisign's mode selection. We control **both ends of this wire** (the same repo produces the release and consumes it — the same intentional coupling already accepted for `AssetName` ↔ `.goreleaser.yaml`), so an envelope format buys nothing. **A raw signature carries no algorithm field, so there is no algorithm-confusion / downgrade vector.** Single algorithm, single key, single trust root.
- **Sign `checksums.txt`, not the tarball.** `checksums.txt` already binds the tarball by SHA-256, so signing the manifest transitively protects every asset it lists. Standard "sign the checksums manifest" pattern; matches AC-1 ("verify `checksums.txt` … before any digest is parsed from it").
- **OpenSSL in CI over an in-repo Go signing helper.** GoReleaser's `signs:` runs an arbitrary `cmd`. `openssl pkeyutl -sign -rawin` over an Ed25519 key emits exactly the raw 64-byte RFC 8032 signature `ed25519.Verify` accepts (OpenSSL 3.x, present on `ubuntu-latest`) — **zero new Go files**, no in-repo signing tool. Because Ed25519 signatures are deterministic and canonical (RFC 8032), the OpenSSL output is byte-identical to `crypto/ed25519.Sign` for the same key+message, so the Go tests (which sign fixtures with `ed25519.Sign`) fully exercise the exact verify path the production CI signature hits. An in-repo Go signing helper is fully testable but adds a 4th new file, tripping the file-count red line — rejected for scope.

## Consequences

- **The trust root is code, not network data.** The baked-in public key ships as source; an attacker without the separately-custodied private key cannot produce a signature that passes the gate.
- **Fail-closed is structural, not a flag.** The sig-fetch + `VerifySignature` are *unconditional* steps wedged between the checksums fetch and the checksums parse. There is **no code path** from "signature missing, malformed, or non-verifying" to "proceed with the update." A missing signature (404) aborts exactly like a missing tarball; a bad signature aborts at `VerifySignature`; neither falls through to the old unsigned behaviour.
- **New operator responsibility.** The production private key lives as the `PYRY_RELEASE_SIGNING_KEY` Actions secret, **distinct from and with tighter custody than** the release-publishing capability (`HOMEBREW_TAP_TOKEN`). `releaseSigningPublicKeyHex` must be kept matched to the CI key. Documented in [`docs/release-tooling.md`](../../release-tooling.md).
- **No key-id / multi-key trust store / rotation flow by design.** Rotation is "regenerate + rebuild + re-release"; a mismatch fails every `pyry update` closed (safe, but broken) until a release built from the new public key ships. A proper rotation/revocation flow is a future ticket if ever needed.
- **The CI signer config is not reachable by unit tests.** The OpenSSL invocation + secret wiring can only be validated by the first-signed-release smoke test — the only end-to-end proof of the CI signer. It fails closed if misconfigured (`update: verify signature: …`), never insecure.
- **Named out of scope:** source/build-supply-chain compromise that alters the baked-in key or the workflow itself (a higher bar — repo write + review bypass); production private-key generation/storage (the operator step, AC-5).

## Related

- [`features/update-package.md`](../features/update-package.md) — the `VerifySignature` primitive + `ErrInvalidSignature` / `ErrInvalidPublicKey` sentinels.
- [`features/pyry-update-command.md`](../features/pyry-update-command.md) — where the gate slots into `doUpdate`'s flow and the error contract.
- [`codebase/776.md`](../codebase/776.md) — the per-ticket implementation summary + lessons.
- [`docs/release-tooling.md`](../../release-tooling.md) — keypair generation, custody, matching, and the first-release smoke test.
- [`docs/specs/architecture/776-sign-update-checksums.md`](../../specs/architecture/776-sign-update-checksums.md) — the build-time spec.
