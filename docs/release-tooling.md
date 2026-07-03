# Release tooling

Operational reference for cutting a `pyry` release. The release runs from a
`v*` tag push and is driven by `.github/workflows/release.yml` +
`.goreleaser.yaml`. This doc covers the two secrets those files depend on.

## Secrets used by the release workflow

| Secret | Purpose | Custody |
|---|---|---|
| `PYRY_RELEASE_SIGNING_KEY` | Ed25519 private key (PEM) that signs `checksums.txt` | **Tightest** — see below |
| `HOMEBREW_TAP_TOKEN` | PAT with `repo` scope to push the formula to `pyrycode/homebrew-tap` | Repo Actions secret |

Both are repo Actions secrets on `pyrycode/pyrycode`. They are **distinct**
secrets: a stolen tap token must not be able to read the signing key.

## Checksums signing key

`pyry update` verifies `checksums.txt` against a public key compiled into the
binary (`releaseSigningPublicKeyHex` in `cmd/pyry/update.go`) before it trusts
any digest in the file. A compromised release or a stolen publishing token
therefore cannot install attacker-chosen bytes: without the private key an
attacker cannot produce a signature that verifies. The private key's whole
value rests on custody, so keep it out of reach of the publishing path.

### Generate the keypair

```sh
# Private key (PEM) — this is the CI secret. Never commit it.
openssl genpkey -algorithm ed25519 -out signing_key.pem

# Raw 32-byte public key, hex-encoded — paste into releaseSigningPublicKeyHex.
openssl pkey -in signing_key.pem -pubout -outform DER | tail -c 32 | xxd -p -c 32
```

### Install it

1. Paste the hex public key into `releaseSigningPublicKeyHex` in
   `cmd/pyry/update.go`. The `TestReleaseSigningKey_Decodes` unit test checks
   it decodes to 32 bytes, catching a typo before release.
2. Store the **private** PEM (the full `signing_key.pem` contents, `BEGIN
   PRIVATE KEY` through `END PRIVATE KEY`) as the `PYRY_RELEASE_SIGNING_KEY`
   Actions secret on `pyrycode/pyrycode`.
3. Delete your local copy of `signing_key.pem` once it is in the secret store.

### Custody rules

- **Never commit and never log the private key.** In CI it flows
  secret → temp file (`0600`, under `${RUNNER_TEMP}`, destroyed with the job)
  → `openssl -inkey`. It never appears on stdout or in an error message.
- **Tighter custody than publishing.** Do not expose
  `PYRY_RELEASE_SIGNING_KEY` to untrusted or third-party workflow steps; it is
  only injected into the `goreleaser` step of the release workflow. If the
  publishing token (`HOMEBREW_TAP_TOKEN`) and the signing key were equally
  reachable, the signature gate would add little.
- The public key is not secret — it ships as source.

### Keep public and private matched

Regenerating the keypair means updating `releaseSigningPublicKeyHex` **in the
same change** as rotating the `PYRY_RELEASE_SIGNING_KEY` secret. A mismatch
makes every `pyry update` fail closed (safe, but broken) until a release built
from the new public key ships. Rotation/revocation beyond
"regenerate + rebuild + re-release" is a manual procedure; there is no
key-id or multi-key trust store by design (single algorithm, single trust
root, no downgrade vector).

## First-signed-release smoke test

Unit tests sign fixtures with a throwaway key, so they cannot exercise the
real CI signer config (the OpenSSL invocation + the `PYRY_SIGNING_KEY_FILE`
secret wiring). After the first release that emits `checksums.txt.sig`, run an
update against it once from a machine already on an older signed build:

```sh
pyry update --check   # confirms it reaches the release
pyry update           # must print "==> Verifying signature... ok"
```

If the OpenSSL-produced signature does not verify against the baked-in key,
the update fails closed with `update: verify signature: …` (never insecure).
`openssl pkeyutl -sign -rawin` requires OpenSSL 3.0+, which `ubuntu-latest`
provides; a pinned older image would surface here.
