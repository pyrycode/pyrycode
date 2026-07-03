# Spec #776 — Sign the `pyry update` checksums file (integrity beyond "GitHub served it")

**Ticket:** [#776](https://github.com/pyrycode/pyrycode/issues/776) · **Size:** S · **Label:** `security-sensitive`

## Files to read first

The developer's turn-1 data load. Read these before writing any code.

- `cmd/pyry/update.go:111-209` — `doUpdate`, the whole flow. **The insertion point is between the checksums `FetchAsset` (L167) and `ParseChecksumsFile` (L171).** `updateOptions` struct is L70-84; `runUpdate`'s production defaults are L34-52 (this is where the baked-in key gets wired).
- `internal/update/checksum.go:56-102` — `ParseChecksumsFile` + `VerifySHA256` + the sentinel-error shape (`ErrChecksumMismatch` etc.). **`signature.go` mirrors this file's pure-function + exported-sentinel shape exactly.**
- `internal/update/fetch.go:57-107` — `FetchAsset` + private `get`. A 404 already returns `"GET <url>: unexpected status 404"` (L89-94); `maxAssetBytes` (L74) already caps the sig read. This is the mechanism that makes a *missing* signature fail closed — no new fetch code needed.
- `cmd/pyry/update_test.go:54-85` — `fakeRelease` (L54-65, **stays unchanged**) + `newFakeReleaseServer` (L67-85, **gains a `.sig` route**). L307-332 `restartUpdateOptions` (shared by 5 restart tests); L216-275 `TestUpdate_PinVersion` (inline mux — needs its own `.sig` route).
- `cmd/pyry/update_e2e_test.go:99-160` — `TestUpdate_HappyPath_E2E` fixture wiring; L499-520 `newFetchFailReleaseServer` + L581-662 the fetch/verify-failure e2e shape to mirror for a new missing-signature e2e.
- `.goreleaser.yaml` (whole file, 100 lines) — `checksum:` block is L52-53; a new `signs:` block goes alongside `brews:`. The `HOMEBREW_TAP_TOKEN` threading in `brews:` (L90) is the secret-injection precedent.
- `.github/workflows/release.yml:43-54` — the `goreleaser` step + how `HOMEBREW_TAP_TOKEN` reaches it. The signing key materialization slots in here.
- `docs/knowledge/features/update-package.md` § "Types & errors" (L39-66) — sentinel-error conventions the new errors follow.
- `docs/knowledge/features/pyry-update-command.md` § "Error contract" (L125-138) — where the new signature-error rows slot in (developer does **not** edit this doc; it's owned by the documentation phase — noted here only for context).

## Context

`pyry update` today verifies the downloaded tarball's SHA-256 against a digest parsed out of `checksums.txt`, but nothing verifies `checksums.txt` itself. Integrity rests entirely on "GitHub served both files honestly." A compromised release pipeline or a stolen publishing token can serve a matching `(tarball, checksums.txt)` pair and the SHA-256 check passes against attacker-chosen bytes — arbitrary code executing over the running binary (`AtomicReplace` → restart).

This ticket adds a detached cryptographic signature over `checksums.txt`, verified against a public key **compiled into the pyry binary** (not one GitHub vouches for), gating *before* any digest is parsed. That breaks the "trust whoever published the release" assumption: an attacker who cannot produce a signature from the (separately-custodied) private key cannot get past the gate.

Operator-invoked over HTTPS → **LOW** severity, but the blast radius (arbitrary code over the running binary) justifies a real signature gate. The companion unbounded-read/OOM half of the same finding (F4) was fixed separately via `io.LimitReader` in `internal/update/fetch.go` (`maxAssetBytes`) and is out of scope here.

## Design decision: raw Ed25519 detached signature

**Scheme: a raw 64-byte Ed25519 signature over the exact bytes of `checksums.txt`, published as `checksums.txt.sig`.** Verification is `crypto/ed25519.Verify(pub, checksumsBytes, sig)` — pure standard library, zero new module dependencies.

Rationale and alternatives considered:

- **Why Ed25519 / stdlib.** `crypto/ed25519` is stdlib; verification is three lines. Matches the project's "stdlib over dependencies" principle and the ticket's explicit steer. (`golang.org/x/crypto` is already a direct dep via `internal/noise`, but we don't touch it — no BLAKE2b, no new imports.)
- **Why a bare raw signature, not the minisign file format.** minisign's `.minisig` carries a 2-byte algorithm tag, an 8-byte key id, a trusted-comment, and a global signature; modern minisign *prehashes* with BLAKE2b-512 by default (`ED` algorithm), which would force either a new hash dependency or fighting minisign's mode selection. We control **both ends** of this wire (the same repo produces the release and consumes it — same coupling already accepted for `AssetName` ↔ `.goreleaser.yaml`), so an envelope format buys nothing. A bare raw signature has a smaller attack surface: **no algorithm field means no algorithm-confusion / downgrade vector.** Single algorithm, single key, single trust root.
- **Why sign `checksums.txt` and not the tarball.** `checksums.txt` already binds the tarball by SHA-256; signing the manifest transitively protects every asset it lists. This is the standard "sign the checksums manifest" pattern and matches AC-1 ("verify `checksums.txt` … before any digest is parsed from it").
- **Why OpenSSL in CI (sign side).** GoReleaser's `signs:` runs an arbitrary `cmd`. `openssl pkeyutl -sign -rawin` over an Ed25519 key emits exactly the raw 64-byte RFC 8032 signature that `ed25519.Verify` accepts (OpenSSL 3.x, present on `ubuntu-latest`). This adds **zero new Go files** and no in-repo signing tool. Because Ed25519 signatures are deterministic and canonical (RFC 8032), the OpenSSL output is byte-identical to what `crypto/ed25519.Sign` produces for the same key+message — so the Go tests (which sign fixtures with `ed25519.Sign`) fully exercise the exact verify path the production CI signature will hit. Considered alternative: an in-repo Go signing helper invoked from `signs.cmd` (fully testable, but adds a new `main` package and a 4th new file, tripping the file-count red line) — rejected for scope.

**Fail-closed is structural, not a flag.** The sig-fetch + verify are *unconditional* steps wedged between the checksums fetch and the checksums parse. There is **no code path** from "signature missing, malformed, or non-verifying" to "proceed with the update." A missing signature (404) surfaces as a `FetchAsset` error and aborts exactly like a missing tarball does today; a bad signature aborts at `VerifySignature`. Neither falls through to the old unsigned behaviour.

## Package structure & interfaces

### New: `internal/update/signature.go`

Sibling to `checksum.go`; same pure-function + exported-sentinel shape. No I/O, no goroutines, no `context.Context`.

- `var ErrInvalidSignature = errors.New("checksums signature verification failed")` — the sig does not verify against the key, **or** is not exactly `ed25519.SignatureSize` (64) bytes. One sentinel covers both (a caller only ever branches "signed correctly vs not").
- `var ErrInvalidPublicKey = errors.New("invalid signing public key")` — the supplied public key is not `ed25519.PublicKeySize` (32) bytes. Defensive: guards against a malformed baked-in constant so verification **returns an error instead of `ed25519.Verify` panicking** on a wrong-length key.
- `func VerifySignature(data, sig []byte, pub ed25519.PublicKey) error` — returns `nil` iff `pub` is 32 bytes, `sig` is 64 bytes, and `ed25519.Verify(pub, data, sig)` is true. Behavior contract (developer writes the body ≤ ~15 lines):
  - `len(pub) != ed25519.PublicKeySize` → wrap `ErrInvalidPublicKey`.
  - `len(sig) != ed25519.SignatureSize` → wrap `ErrInvalidSignature`.
  - `!ed25519.Verify(...)` → wrap `ErrInvalidSignature`.
  - else `nil`.
  - The error message must **not** include the signature or key bytes (they're not secret, but keep the message a clean sentinel-carrier per house style).

The invariants are asserted by `signature_test.go`'s table (see Testing strategy).

### Modified: `cmd/pyry/update.go`

1. **Baked-in public key** — a package-level constant in `update.go` (do **not** add a new file; keep the new-file count at 3):
   - `const releaseSigningPublicKeyHex = "<64 hex chars>"` — the raw 32-byte Ed25519 public key, hex-encoded. The developer generates a real keypair (see § Release / operator step) and pastes the public half here.
   - Comment above the const: how it was generated + that its private half lives as the CI signing secret + a pointer to `docs/release-tooling.md`.
2. **`updateOptions` gains one field**: `signingPubKey ed25519.PublicKey`. This is the test seam — production sets it from the baked-in constant; tests inject their own test key (fixtures are signed with a throwaway test key, never the production private key).
3. **`runUpdate`** decodes `releaseSigningPublicKeyHex` (`hex.DecodeString`, length-check → `ed25519.PublicKey`) and sets `signingPubKey`. A decode failure returns a wrapped error (`update: signing key: …`) — loud, and a `signature_test.go`/`update_test.go` unit test asserts the constant decodes to 32 bytes so a typo is caught at test time, not release time.
4. **`doUpdate`** — insert between L167 (`sumsBytes, err := o.fetcher.FetchAsset(ctx, checksumsURL)`) and L171 (`ParseChecksumsFile`):
   - Derive `sigURL := checksumsURL + ".sig"`.
   - `sig, err := o.fetcher.FetchAsset(ctx, sigURL)` → on error `return fmt.Errorf("update: download signature: %w", err)`. **This is the missing-signature fail-closed point** (a 404 lands here).
   - `if err := update.VerifySignature(sumsBytes, sig, o.signingPubKey); err != nil { return fmt.Errorf("update: verify signature: %w", err) }`.
   - Optional progress line mirroring the SHA-256 one, e.g. `==> Verifying signature... ok` (keep it consistent with the existing `==> Verifying SHA-256... ` style; the developer may omit if it complicates the failure-path output ordering — not an AC).
   - **Verify over the raw fetched bytes, before `ParseChecksumsFile`.** Do not trim/normalize `sumsBytes` first — the signature covers the exact file bytes GoReleaser signed.

**Fetch order** stays: tarball → checksums → **signature → verify-signature** → parse-checksums → verify-SHA256 → extract → replace → restart. (Tarball-first is unchanged, so the existing `TestUpdate_FetchFailure_E2E`, whose server 500s the tarball URL, still aborts before any signature logic and needs no test key.)

### Modified: `.goreleaser.yaml`

Add a `signs:` block that signs only the checksum artifact and uploads the `.sig` as a release asset (developer writes the exact YAML; contract shown, not prescribed verbatim):

- `artifacts: checksum` (sign `checksums.txt` only).
- `signature: "${artifact}.sig"` → produces/uploads `checksums.txt.sig` at the standard release-download path `<base>/<tag>/checksums.txt.sig` (the URL `doUpdate` derives).
- `cmd: openssl` with args `pkeyutl -sign -inkey {{ .Env.PYRY_SIGNING_KEY_FILE }} -rawin -in ${artifact} -out ${signature}`.
- Run `goreleaser check` locally to validate syntax (AC-4's testable surface — the developer cannot run a real tagged release).

### Modified: `.github/workflows/release.yml`

Materialize the signing key from a repo secret before GoReleaser runs, and pass its path via env to the goreleaser step:

- A step that writes `${{ secrets.PYRY_RELEASE_SIGNING_KEY }}` (an Ed25519 private key in PEM) to a file under `${{ runner.temp }}`.
- The `goreleaser` step gains `env: PYRY_SIGNING_KEY_FILE: <that path>`.
- Mirror the existing `HOMEBREW_TAP_TOKEN` comment style (point at `docs/release-tooling.md`).

### New: `docs/release-tooling.md` (AC-5)

This doc is **in scope for the developer** — it is a genuine operational deliverable (already referenced by `release.yml:53` but currently missing), it lives outside `docs/knowledge/`, and no downstream phase owns it (the documentation phase writes `docs/knowledge/codebase/<N>.md`, not operational release docs). Keep it tight (~40-60 lines). It must document:

- **Keypair generation** — the exact `openssl genpkey -algorithm ed25519` commands to produce the private PEM and to extract the raw 32-byte public key for the baked-in constant (`openssl pkey -pubout -outform DER | tail -c 32 | xxd -p`).
- **Private-key custody** — the private key is stored as the `PYRY_RELEASE_SIGNING_KEY` **Actions secret** on `pyrycode/pyrycode`, and **must have tighter custody than the release-publishing capability** (see Security review § Secrets). The `HOMEBREW_TAP_TOKEN` PAT and this key are distinct secrets.
- **Keeping public/private matched** — regenerating the keypair means updating `releaseSigningPublicKeyHex` in `cmd/pyry/update.go` in the same change; a mismatch makes every `pyry update` fail closed (safe, but broken).
- **First-signed-release smoke test** — after the first release that emits `checksums.txt.sig`, run `pyry update --check`-then-update once against it to confirm the OpenSSL-produced signature verifies with the baked-in key (this is the only end-to-end validation of the CI signer config, which unit tests can't reach).

## Concurrency model

None new. `pyry update` is single-goroutine, sequential, one `context.Context`. `VerifySignature` is a pure function. The added signature fetch is one more back-to-back `FetchAsset` (same shape as the existing tarball/checksums fetches). No locks, channels, or goroutines.

## Error handling

| Condition | Where | Result |
|---|---|---|
| Signature asset 404 / any fetch failure (attacker deleted it, or upstream down) | `doUpdate` sig `FetchAsset` | `update: download signature: <fetcher error>` — aborts **before** parse/extract/replace. **No unsigned fallback.** (AC-2) |
| Signature present but does not verify (wrong key, tampered checksums, garbage bytes) | `VerifySignature` → `ErrInvalidSignature` | `update: verify signature: checksums signature verification failed` — aborts before extract/replace. (AC-3) |
| Signature wrong length | `VerifySignature` → `ErrInvalidSignature` | same as above (no panic). |
| Baked-in public key malformed (typo in the hex constant) | `runUpdate` decode / `VerifySignature` → `ErrInvalidPublicKey` | `update: signing key: …` — every update fails closed; caught by the decode unit test. |
| Valid signature over a **bogus** checksums body (only possible with the private key — i.e. a genuine but wrong release) | passes sig gate → `VerifySHA256` | falls through to the existing `ErrChecksumMismatch` path unchanged. |

All errors keep the `update: <step>: %w` wrap convention and propagate through `main()`'s `pyry: <err>` wrapper (exit 1). The sig gate adds no new success path.

## Testing strategy

### `internal/update/signature_test.go` (new, table-driven, `t.Parallel`)

Scenarios (developer writes bodies in the project idiom; fixtures via `ed25519.GenerateKey` / `ed25519.NewKeyFromSeed` in-test):
- valid signature over data + correct key → `nil`.
- tampered data (flip a byte after signing) → `ErrInvalidSignature`.
- wrong key (verify with a different public key) → `ErrInvalidSignature`.
- truncated / over-length signature → `ErrInvalidSignature` (**not a panic**).
- wrong-length public key (e.g. 31 bytes) → `ErrInvalidPublicKey` (**not a panic** — this is the guard that matters).
- empty `data` with a valid signature over empty → `nil` (canonical edge).

### `cmd/pyry/update_test.go` (extend)

- **Deterministic test keypair** as package-level vars, e.g. `testSigningPriv/testSigningPub` from `ed25519.NewKeyFromSeed(<fixed 32-byte seed>)`. Never the production key.
- **`newFakeReleaseServer` auto-signs**: sign the `checksums` bytes it's handed with `testSigningPriv` and serve them at `.../checksums.txt.sig`. **Call signature unchanged** → the 8 existing `newFakeReleaseServer` callers don't change. (This keeps `VerifyFailure`/`BrokenNewBinary` working: the server signs whatever checksums it's given, so the sig gate passes and the flow continues to the SHA-256 / restart assertion those tests already make.)
- **`fakeRelease` stays as-is** (returns `(asset, tgz, checksums)`; the signature is derived at the server layer, not here) → its 9 callers don't change.
- **Add `signingPubKey: testSigningPubKey`** to the `updateOptions` literals that reach the sig-verify step: `TestUpdate_Success`, `TestUpdate_PinVersion`, `restartUpdateOptions` (covers the 5 restart tests). The early-abort literals (`AlreadyAtLatest`, `CheckOnly`, `DevBuildSkips`) never reach verification and leave it nil. `TestUpdate_PinVersion`'s inline mux also gains a `.sig` route (sign the checksums it serves). **~4 struct-literal edits + 1 inline-mux route + 1 helper edit — well under the 10-call-site line.**
- **New `TestUpdate_MissingSignature`**: a server (inline mux or a small parallel constructor mirroring `newFetchFailReleaseServer`) that serves tarball + checksums but 404s `checksums.txt.sig` → `doUpdate` returns an error containing `download signature`; the `replace` seam is a `t.Fatalf` sentinel (proves no `AtomicReplace`); success line absent.
- **New `TestUpdate_BadSignature`**: server serves a `.sig` that does not verify (sign with a *different* key, or serve garbage bytes) → `doUpdate` returns an error containing `verify signature`; `replace` sentinel not called.
- **Add `TestReleaseSigningKey_Decodes`**: assert `releaseSigningPublicKeyHex` decodes to exactly 32 bytes — catches a typo/placeholder in the baked-in constant at test time.

### `cmd/pyry/update_e2e_test.go` (extend)

- The `//go:build … e2e_update` `newFakeReleaseServer` is the same helper (same package) — it auto-serves the sig, so `HappyPath`, `VerifyFailure`, `BrokenNewBinary` keep working once their `updateOptions` literals set `signingPubKey: testSigningPubKey`. (`FetchFailure_E2E` aborts at the tarball fetch, needs nothing.)
- **New `TestUpdate_MissingSignature_E2E`**: mirror `TestUpdate_FetchFailure_E2E`'s structure but with a server that serves tarball + checksums and 404s the `.sig`. Assert: `doUpdate` errors before `AtomicReplace`, binary inode unchanged, pre-update daemon still answering, success line absent. This is the integration-level proof of the AC-2 downgrade defense. (Bad-signature is covered thoroughly at the `VerifySignature` + `doUpdate` unit level; one e2e for the fail-closed/downgrade case is sufficient.)

### AC-4 / AC-5 verification

- AC-4 (pipeline emits the signature): `goreleaser check` passes with the new `signs:` block (config syntax); the block + workflow key-materialization are the deliverable. A real signed release is an operator action (first-release smoke test, documented).
- AC-5 (docs): `docs/release-tooling.md` exists and covers generation, custody, matching, and the smoke test.

## Acceptance-criteria mapping

| AC | Satisfied by |
|---|---|
| 1 — fetch a detached sig for `checksums.txt` and verify against a compiled-in key **before** any digest is parsed | `doUpdate` sig-fetch + `VerifySignature` inserted between L167 and L171, gating `ParseChecksumsFile`; `releaseSigningPublicKeyHex` baked into `cmd/pyry`. |
| 2 — missing signature (404) aborts with a distinct error, no fall-through to unsigned | `FetchAsset(sigURL)` error → `update: download signature: …`; structurally no unsigned path. `TestUpdate_MissingSignature` (+ e2e). |
| 3 — non-verifying signature aborts before extract/replace | `VerifySignature` → `ErrInvalidSignature` before `ExtractBinary`/`AtomicReplace`. `TestUpdate_BadSignature`. |
| 4 — release pipeline emits a detached signature | `.goreleaser.yaml` `signs:` block + `release.yml` key materialization; `goreleaser check`. |
| 5 — signing-key procedure documented; baked-in public key matches | `docs/release-tooling.md` + the baked-in constant + its generation comment. |

## Open questions (resolve during implementation)

- **Progress-line wording.** `==> Verifying signature... ok` vs folding into the existing SHA-256 line. Cosmetic; developer's call. Not an AC, no test asserts it (keep failure-path output clean — see the `FAIL` handling around L176-181 for the pattern if a line is added).
- **OpenSSL `-rawin` availability.** Requires OpenSSL 3.0+. `ubuntu-latest` (24.04) satisfies this; if CI ever pins an older image, the first-release smoke test catches a broken signer (fails closed, never insecure). Documented in `release-tooling.md`.
- **Signature file naming.** `checksums.txt.sig` assumed (GoReleaser `signature: "${artifact}.sig"`). If the developer picks a different suffix, `sigURL` in `doUpdate` and the `.goreleaser.yaml` `signature:` template must agree — they're both in this repo (same intentional-coupling as `AssetName` ↔ archive name).

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The untrusted input is the network-fetched `checksums.txt` (attacker-controllable via a compromised release/token). The trust boundary is a **single explicit gate**: `update.VerifySignature(sumsBytes, sig, o.signingPubKey)` in `doUpdate`, run over the raw fetched bytes *before* `ParseChecksumsFile` and far before `ExtractBinary`/`AtomicReplace`. Downstream code only ever parses bytes that a signature over the compiled-in key has already vouched for. The trust root (the baked-in public key) is code, not network data. Documented and testable.
- **[Cryptographic primitives]** No MUST FIX. `crypto/ed25519` (stdlib, RFC 8032); verification only, no hand-rolled crypto. **No algorithm/downgrade vector** — the raw-signature format carries no algorithm field, so there is no "attacker selects a weaker algorithm" path (a concrete advantage over an envelope format like minisign). No key/nonce reuse — a single key used for one purpose (signing the checksums manifest). Ed25519 verification is inherently constant-time in the Go stdlib; no attacker-controlled secret is compared, so `crypto/subtle` is N/A here (the comparison is a signature check, not a secret-equality check). The wrong-length-key guard (`ErrInvalidPublicKey`) prevents `ed25519.Verify` from panicking on malformed key material.
- **[Tokens, secrets, credentials]** SHOULD FIX (documented, not code-gated). The signing **private key** is the load-bearing secret. Its whole value depends on **tighter custody than the release-publishing capability** — if a stolen `HOMEBREW_TAP_TOKEN` (a tap-repo PAT) or a compromised publishing step could also read `PYRY_RELEASE_SIGNING_KEY`, the gate adds little. GitHub Actions secrets are not readable by an exfiltrated PAT, and the key is only injected into the release workflow's goreleaser step. `docs/release-tooling.md` **MUST** state: the key is a repo Actions secret distinct from the publishing token, and must not be exposed to untrusted/third-party workflow steps. The private key is **never** committed and **never** logged (it flows secret → temp file → openssl `-inkey`; it never appears in an error message or on stdout). The public key is non-secret and baked in as source. No token appears in any pyry log or error path.
- **[File operations]** No MUST FIX. The only filesystem write remains `AtomicReplace` (unchanged: temp + fchmod + fsync + rename, mode `0o755`, straggler-cleaned). The signature gate runs strictly *before* it, so a bad/missing signature never reaches a disk mutation. The CI temp key file is written under `${{ runner.temp }}` (ephemeral runner scratch, destroyed with the job); no path traversal (no user-controlled path components). No new TOCTOU (the sig is fetched into memory and verified in one expression; no check-then-use on a filesystem path).
- **[Network & I/O]** No MUST FIX. The signature fetch reuses `Fetcher.get`, which already caps the body at `maxAssetBytes` (512 MiB `io.LimitReader`, `internal/update/fetch.go:74`) and honours ctx cancellation — so a hostile server cannot OOM the process via an oversized `.sig`, and a 64-byte-expected asset that streams unbounded is rejected. A malformed/oversized sig fails `VerifySignature` (wrong length) regardless. No new listener, no new inbound surface — this is client-side outbound HTTPS.
- **[Error messages, logs, telemetry]** No MUST FIX. Signature/key bytes are non-secret but are kept out of error strings (sentinel-carrier style). The private key never enters a pyry-visible surface (it lives only in CI). Error messages name the step (`download signature`, `verify signature`), not any key material.
- **[Subprocess execution]** No MUST FIX. No new subprocess in pyry. The only new exec is CI-side (`openssl`), invoked by GoReleaser with fixed, non-user-controlled args over release-produced files; no `sh -c`, no user input in argv.
- **[Concurrency]** No findings — `pyry update` is single-goroutine and sequential; `VerifySignature` is a pure, stateless function; no locks, no shared mutable state, no new goroutine.
- **[Threat model alignment]** The named threat — "compromised release pipeline or stolen publishing token installs an attacker-built binary" — is addressed: the attacker cannot produce a valid signature without the separately-custodied private key, and the update fails closed at the gate. **Out of scope (named):** (a) source/build-supply-chain compromise that alters the baked-in key or the workflow itself (a higher bar — repo write + review bypass; not this ticket); (b) key rotation/revocation mechanics beyond "regenerate + rebuild + re-release" (documented as a manual procedure in `release-tooling.md`; a proper rotation flow is a future ticket if it's ever needed); (c) production private-key generation/storage, which is the operator step captured by AC-5.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-03
