# Spec — `pyry pair`: add `server_static_pubkey` + fingerprint to QR payload (#432)

## Files to read first

The developer's turn-1 reading list. Lift these into context before writing any code.

- `internal/pair/payload.go:1-108` — current `Payload` shape (`server`, `relay`, `token`), `Encode`/`Decode` contract, `ErrInvalidPayload` sentinel, the "decode-error-text never contains input bytes" discipline you must preserve when adding the new field.
- `internal/pair/payload_test.go:1-166` — table-driven `TestDecode_Errors` (the existing missing/empty cases are the template for the new pubkey cases) and `TestEncode_DecodeRoundTrip` (extend with the new field; do NOT introduce a new test file for one field).
- `internal/pair/render.go:1-59` — `Render(Payload, io.Writer)`'s QR + paste-fallback + instruction-line shape, the `errTrackingWriter` short-circuit, and the doc-comment SECURITY block about the one-time-display surface (the new fingerprint line falls under the same writer; do NOT add a separate sink).
- `internal/pair/render_test.go:1-159` — pattern for asserting QR substring + encoded payload + instruction line. Extend with assertions for the fingerprint line; reuse `samplePayload()`.
- `cmd/pyry/pair.go:143-203` — `runPairDefault`: the single producer of `pair.Payload`. This is the only call site you wire `keys.LoadOrCreate` into.
- `cmd/pyry/main.go:133-154` — `sanitizeName`. **Used to derive the on-disk path component for `~/.pyry/<sanitized-name>/`.** Pass `sanitizeName(parsed.instanceName)` as the `daemonName` argument to `keys.LoadOrCreate` so the static-key file co-locates with `devices.json` / `server-id`. (Spec #438 explicitly warns that `sanitizeName` is more permissive than `keys.validDaemonName`; that mismatch is intentional and is handled here by surfacing `ErrInvalidDaemonName` as a `pair: ...` wrapped error — no auto-rewrite, no silent fallback.)
- `internal/keys/store.go:34-95` — `keys.LoadOrCreate(baseDir, daemonName) (*StaticKey, error)`. The two-arg constructor; the keys package owns the `<baseDir>/<daemonName>/static_key.json` path mapping.
- `internal/keys/static_key.go:43-65` — `StaticKey.PublicKey() [32]byte` is the raw 32-byte X25519 public point you copy into `Payload.ServerStaticPubkey`. `PrivateKey()` is forbidden output; never touch it from `cmd/pyry/pair.go`.
- `docs/protocol-mobile.md:135-150` — § *Pairing flow*: canonical QR JSON shape including `server_static_pubkey`. Field naming and order locked here.
- `docs/protocol-mobile.md:635-654` — § *Appendix: example flow*: the byte-exact desktop output the CLI must produce (`==> Static-key fp:       aa:bb:cc:dd:ee:ff:11:22`). The eight bytes / seven colons / lowercase-hex form is fixed.
- `docs/protocol-mobile.md:540-547` — § *UX implications*: the requirement that the desktop print the fingerprint immediately under the QR with a "verify on your phone" hint.
- `docs/protocol-mobile.md:720-724` — § *Security review*: explicit FIXED entry pinning 64-bit fingerprint (not 32-bit). The test vector MUST encode this; a 4-byte vector is a spec violation.
- `internal/e2e/pair_test.go:271-284` — `decodePairPayload` helper; the existing e2e flow already round-trips through `pair.Decode`, so once `Decode` enforces the new field the e2e tests fail until the producer in `cmd/pyry/pair.go` is wired. Add one non-empty-pubkey assertion (≤2 lines) so failure mode is loud.

## Context

Mobile Protocol v2 (#430) requires the phone to authenticate the binary via its X25519 static public key, learned out-of-band at pair time (`Noise_IK` initiator step needs the responder's static key before the first message). The v1 QR payload (`{server, relay, token}`) carries no such anchor; v2 grows the same envelope to `{server, relay, token, server_static_pubkey}` per `docs/protocol-mobile.md` § *Pairing flow*, with the binary's persistent keypair sourced from `internal/keys.LoadOrCreate` (#438 core, #439 hardening — both merged).

The same flow displays an **8-byte (64-bit)** fingerprint — `BLAKE2s-256(pubkey)[:8]` formatted as colon-separated lowercase hex — immediately under the QR for visual cross-verification against the phone's "Confirm pairing" screen. The 64-bit truncation is load-bearing: `docs/protocol-mobile.md` § *Security review* item *[Cryptographic primitives]* records that 32 bits is brute-forceable (≈ 2³² preimage search) and standardises 64 bits everywhere the fingerprint appears. The test vector must encode this width.

Out of scope (not addressed in this spec): phone-side handling of the new field (pyrycode-mobile track), static-key rotation / re-pair (deferred to v3 per spec § *Static keys — binary side*), multi-binary fingerprint comparison UI.

## Design

### Surface area touched

Three production source files (well under the XS file cap):

| File | Change |
|---|---|
| `internal/pair/payload.go` | Add `ServerStaticPubkey string` field to `Payload`; extend `Decode` with three new rejection cases; document the base64 alphabet choice in the package doc. |
| `internal/pair/render.go` | Add a package-private `fingerprintLine` writer and a new exported `Fingerprint([32]byte) string` formatter (testable in isolation); `Render` calls `Fingerprint` after the encoded line. |
| `cmd/pyry/pair.go` | Add a `resolveStaticKeyBaseDir() string` helper; call `keys.LoadOrCreate(resolveStaticKeyBaseDir(), sanitizeName(parsed.instanceName))`; copy the pubkey into the `pair.Payload` literal. |

Dependency: add `golang.org/x/crypto/blake2s` via `go get` (one require line in `go.mod` + `go.sum`). Same hash already mandated for Noise_IK by `docs/protocol-mobile.md:85-89`, so the dep is forward-compatible with #433.

No changes to `internal/keys`. No changes to `cmd/pyry/relay.go` (the relay side does not consume the static keypair at this layer; #433 wires it in).

### `Payload` shape

```go
type Payload struct {
    Server             identity.ServerID `json:"server"`
    Relay              string            `json:"relay"`
    Token              string            `json:"token"`
    ServerStaticPubkey string            `json:"server_static_pubkey"`
}
```

- Field appears **last**, after `token`, matching `docs/protocol-mobile.md:139-150` § *Pairing flow* and § *Appendix*. Go's `encoding/json` honours struct field order; the canonical JSON ordering follows automatically.
- Value is the **base64-encoded raw 32-byte X25519 public key**. Use `base64.StdEncoding` (with padding) to match the on-disk encoding in `internal/keys/store.go:206-208` — same 32 bytes have the same string form on disk and in the QR payload, which simplifies cross-system debugging. Document this choice in the package doc-comment.
- Field is REQUIRED. `Decode` must reject zero / missing / wrong-length values via `ErrInvalidPayload`. The existing pattern of empty-string-rejection extends naturally.

### `Decode` extension

Add to the existing missing-field block in `internal/pair/payload.go`, immediately after the `Token == ""` check:

1. `ServerStaticPubkey == ""` → `%w: missing field: server_static_pubkey`.
2. base64-decode of `ServerStaticPubkey` fails → `%w: invalid server_static_pubkey encoding` (NEVER include the input bytes in the wrapped error; pattern already established for `invalid base64url encoding` upstream).
3. decoded length ≠ 32 → `%w: server_static_pubkey wrong length` (no length value in the message; just the category — keeps decode-error-text-discipline).

All three wrap `ErrInvalidPayload`; existing `errors.Is`-based callers continue to work. The decode order means the JSON-shape errors keep firing first; the new errors only fire after structural validity is established.

### `Fingerprint` formatter

Exported, pure, no I/O, no globals — testable on a fixed vector:

```go
// Fingerprint returns the verifying-string form of pubkey:
// BLAKE2s-256(pubkey) truncated to 8 bytes, formatted as
// colon-separated lowercase hex ("aa:bb:cc:dd:ee:ff:11:22").
//
// The 8-byte / 64-bit width is load-bearing per
// docs/protocol-mobile.md § Security review. Do not truncate to 4.
func Fingerprint(pubkey [32]byte) string
```

Implementation note (one line, do not over-think): `sum := blake2s.Sum256(pubkey[:]); fp := sum[:8]; format as %02x with `:` between bytes`. Strict lowercase. Exactly 23 characters (`8*2 + 7`). The signature takes `[32]byte` not `[]byte` so the caller cannot accidentally pass a longer / shorter buffer.

### `Render` extension

`Render` gains one new line, written after the encoded-payload line and before the instruction line, of the byte-exact form:

```
Static-key fp: aa:bb:cc:dd:ee:ff:11:22  (verify this matches the fingerprint shown on your phone)
```

(A single line, not two; the hint fits inline. This matches `docs/protocol-mobile.md:545-547` § *UX implications* which requires "a one-line hint." The desktop example flow in § *Appendix* uses tab-aligned `==>` prefixes for readability but those are illustrative — `Render` writes plain text without prefixes, consistent with the existing instruction line.)

`Render` decodes `p.ServerStaticPubkey` via `base64.StdEncoding.DecodeString` and feeds the resulting 32 bytes into `Fingerprint`. If decoding fails (which `Decode`-rejected payloads cannot reach but `Render` accepts unvalidated `Payload` structs by contract — see existing `Encode` doc-comment), `Render` writes the literal string `Static-key fp: <invalid>` and returns nil; this keeps `Render` non-failing on its writer (the existing error-tracking contract is preserved). **Do NOT panic.** This case is reachable only if a caller hand-builds a `Payload` without going through validated inputs; surfacing `<invalid>` makes the bug visible without breaking the one-time-display surface.

The fingerprint line is written through the same `errTrackingWriter` as the rest of `Render`'s output; no separate sink, no logger, no telemetry. This preserves the existing "one-time display surface" SECURITY discipline in `internal/pair/render.go:23-30`.

### `runPairDefault` wiring (cmd/pyry/pair.go)

One new helper:

```go
// resolveStaticKeyBaseDir returns the parent directory under which the
// keys package places <daemonName>/static_key.json. Mirrors the
// resolveDevicesPath / resolveServerIDPath fallback: ~/.pyry when HOME
// resolves, "" otherwise (keys.LoadOrCreate then writes to "./<name>/").
func resolveStaticKeyBaseDir() string
```

Then in `runPairDefault`, between the existing `identity.LoadOrCreate` call (line 168) and the `crypto/rand` token mint, load the static key:

```go
staticKey, err := keys.LoadOrCreate(resolveStaticKeyBaseDir(), sanitizeName(parsed.instanceName))
if err != nil {
    return fmt.Errorf("pair: %w", err)
}
```

Error path: any `keys.LoadOrCreate` failure (`ErrInvalidDaemonName`, `ErrInsecureKeyDirMode`, `ErrInsecureKeyFileMode`, `ErrCorruptKeyFile`, raw I/O) surfaces as `pair: keys: ...` and `main.run` adds the `pyry: ` prefix → `pyry: pair: keys: insecure key file mode`. Operator-actionable.

Construct the `Payload` (line 194) as:

```go
pub := staticKey.PublicKey() // [32]byte by value
payload := pair.Payload{
    Server:             serverID,
    Relay:              relay,
    Token:              plain,
    ServerStaticPubkey: base64.StdEncoding.EncodeToString(pub[:]),
}
```

Import `encoding/base64` (already present in `internal/pair`; `cmd/pyry/pair.go` does not currently import it — add). Import `github.com/pyrycode/pyrycode/internal/keys`.

**Error / log discipline:** the static-key path is independent of the device-token flow above it. Token-related errors do not include the pubkey; pubkey-related errors (decode, length) do not include the token. The ticket's "do not log or copy the device-token alongside the pubkey" constraint is satisfied by structure — the two values are not co-located in any single `fmt.Errorf` call site this spec introduces. Reviewer should grep the diff for `Token` and `Pubkey` co-occurrence in error messages and reject any.

## Concurrency model

None new. `runPairDefault` is single-goroutine, called once per `pyry pair` invocation. `keys.LoadOrCreate` per its own contract is not safe for concurrent use against the same path; the CLI invocation guarantees single-process single-call. `Render` writes to `os.Stdout` from the main goroutine.

## Error handling

| Failure | Where surfaced | Contract |
|---|---|---|
| `Decode` rejects missing/empty/short-/long-/non-base64 pubkey | `internal/pair/payload.go` | Wrapped `ErrInvalidPayload`; error text is the failure category, never input bytes. |
| `keys.LoadOrCreate` returns any error | `cmd/pyry/pair.go:runPairDefault` | Wrapped `fmt.Errorf("pair: %w", err)`; `main.run` adds `pyry: ` prefix → exit 1. |
| `Render`'s writer fails mid-fingerprint-line | `internal/pair/render.go` | Existing `errTrackingWriter` short-circuit; first error returned, no retry. |
| `Render` called with a `Payload` whose `ServerStaticPubkey` is unparseable | `internal/pair/render.go` | Emit `Static-key fp: <invalid>` literal; do not panic, do not return error. Pre-validated payloads (Decode-accepted, or built from `keys.LoadOrCreate` output) cannot reach this branch. |

## Testing strategy

### `internal/pair/payload_test.go` (extend)

- Extend `TestEncode_DecodeRoundTrip`'s `Payload` literal with a fixed valid pubkey (e.g. all-zeros 32 bytes, base64 of those). Assert struct equality, no special-casing.
- Extend `TestEncode_Format`'s required-keys list to include `"server_static_pubkey"`.
- Extend the `TestDecode_Errors` table with rows:
  - `missing server_static_pubkey field` — JSON omits the key.
  - `empty server_static_pubkey string` — JSON has `"server_static_pubkey":""`.
  - `non base64 server_static_pubkey` — JSON has the field with `"!!!"` as value.
  - `wrong-length server_static_pubkey` — JSON has the field with `base64.StdEncoding.EncodeToString(make([]byte, 31))` (and 33). Both rejected as `wrong length`.
  - Each case asserts `errors.Is(err, ErrInvalidPayload)` AND that no sentinel token / relay value leaks into `err.Error()` (already pattern; extend `validJSON` constant to include the new field; introduce a sentinel pubkey value if desired, but a literal pubkey is not sensitive — only token and relay are tracked in the leak check).
- Extend `TestDecode_AcceptsTrailingWhitespace`'s JSON literal with the new field (needed because the existing testcase becomes invalid after `Decode` starts requiring the field).

### `internal/pair/payload_test.go` (new fingerprint test, same file)

- `TestFingerprint_FixedVector`: pubkey = 32 zero bytes. Assert `Fingerprint(pubkey)` returns the exact 23-char lowercase-hex colon-separated string. **Compute the expected value by hand once** from `blake2s.Sum256(make([]byte, 32))[:8]`, hardcode the byte values, and write them out in the test. Do NOT compute the expected value at test time from the same primitive — that's a tautology, not a test. (Reviewer note: the developer should run `go test -run TestFingerprint_FixedVector` once locally, paste the observed-vs-expected diff if any, and pin the literal.)
- `TestFingerprint_LengthAndShape`: pubkey = some non-zero 32 bytes; assert the returned string matches `^[0-9a-f]{2}(:[0-9a-f]{2}){7}$` and `len == 23`. Pins the format independent of the hash value.
- (No second vector required for an XS ticket; the format-regex test catches arbitrary inputs.)

### `internal/pair/render_test.go` (extend)

- Add a fixed `ServerStaticPubkey` to `samplePayload()` (use base64 of an all-zeros 32-byte slice).
- Extend `TestRender_Format_Happy` with an assertion that the output contains the substring `Static-key fp: ` and the byte-exact fingerprint for the all-zeros pubkey, AND the substring `verify this matches the fingerprint shown on your phone`.
- Extend `TestRender_FieldOrder`: the fingerprint line must appear AFTER the encoded payload line and BEFORE the instruction line. Use `strings.Index` on each of the three substrings and assert the ordering.
- Add `TestRender_InvalidPubkey_DoesNotPanic`: pass a `Payload` with `ServerStaticPubkey = "!!!"` (invalid base64); assert `Render` returns nil AND output contains `Static-key fp: <invalid>`.

### `cmd/pyry/pair_test.go` (one new test)

- `TestRunPairDefault_PopulatesStaticPubkey`: set `t.Setenv("HOME", t.TempDir())`, run `runPairDefault(nil)` (or the lightest existing invocation pattern in the file), capture stdout, decode the rendered payload via `pair.Decode`, assert `ServerStaticPubkey != ""` AND `len(base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)) == 32`. A second `runPairDefault` against the same HOME must produce the **same** `ServerStaticPubkey` (verifies `keys.LoadOrCreate` returns the persisted key on the second call, not a fresh one). Skip the test on Windows (same skip pattern as `TestRunPairRevoke_SaveFailure`).

### `internal/e2e/pair_test.go` (one new assertion in the existing happy-path)

- In each happy-path test that already calls `decodePairPayload`, add `if payload.ServerStaticPubkey == "" { t.Error("payload.ServerStaticPubkey is empty") }`. No new test functions needed; the existing tests will fail-loud if the producer side ever drops the field.

## Open questions

- **Daemon-name validator mismatch with `sanitizeName`.** `cmd/pyry/main.go:sanitizeName` permits `.` and uppercase; `internal/keys.validDaemonName` rejects both. Result: `PYRY_NAME=Foo` produces `~/.pyry/Foo/devices.json` (from existing helpers) but fails at `keys.LoadOrCreate("~/.pyry", "Foo")` with `ErrInvalidDaemonName`. Spec #438 deliberately accepted this mismatch (its `Files to read first` block warns the developer not to clone `sanitizeName` into the keys package). This ticket inherits the mismatch — operators with non-conforming names get a clean `pyry: pair: keys: invalid daemon name "Foo"` error and the fix is to rename their instance. **Not addressed here.** If the operator-friction becomes real, a follow-up ticket should tighten `sanitizeName` to match `keys.validDaemonName`'s charset (lowering, dot→underscore), which is a CLI-wide change touching every `resolve*Path` helper. Out of scope for #432.
- **`Render`'s `<invalid>` fallback is unreachable through `Decode`.** Once `Decode` enforces the new field, the only way to construct a `Payload` with an invalid pubkey is to hand-build it. The fallback is belt-and-suspenders; it does not need test coverage beyond the no-panic check above. If a future refactor splits `Render` into smaller renderers, the fallback should move with the fingerprint writer.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new boundary. `Payload` is already a JSON-decoded value crossing from "operator-controlled stdin / wire input" to "in-process struct" at `internal/pair/payload.go:Decode`. The new field tightens that boundary (one more required-field check). On the producer side, the pubkey enters the payload from `keys.LoadOrCreate`, which is a trusted source (own filesystem, validated mode, verified public-against-private at parse time per `internal/keys/store.go:178`).
- **[Tokens / secrets / credentials]** No findings — the field carries the **public** half only. `StaticKey.PrivateKey()` is never read by this ticket's code paths (grep enforceable: the diff must not mention `PrivateKey`). The doc-comment on `Payload`'s package already names the device-token as the one-time-display secret; the pubkey is explicitly safe-to-emit per `internal/keys/static_key.go:62-65`. The ticket's explicit "do not log or copy the device-token alongside the pubkey in any error message" constraint is satisfied by structural separation — no error site in this spec puts both values in one message; see § *Error handling* for the case matrix.
- **[File operations]** No new file operations introduced by this ticket. `keys.LoadOrCreate` owns its filesystem invariants (#438 + #439): `0700` parent, `0600` file, `O_NOFOLLOW` on read, atomic write via temp-rename. `cmd/pyry/pair.go` passes a `daemonName` derived from `sanitizeName`; the inner `keys.validDaemonName` is the authoritative gate against path traversal. The base-dir argument is operator-private (`~/.pyry`); not attacker-influenced.
- **[Subprocess / external command execution]** N/A — no subprocess wiring.
- **[Cryptographic primitives]** Two crypto choices, both standards-grade:
  - Pubkey is X25519, sourced from `crypto/ecdh` via `internal/keys` (audited stdlib).
  - Fingerprint is BLAKE2s-256 truncated to 8 bytes (64-bit). The truncation width is the load-bearing security parameter: 4 bytes (32-bit) would be brute-forceable in CPU-hours per `docs/protocol-mobile.md` § *Security review* → *[Cryptographic primitives]*. The spec pins 8 bytes; the test vector pins it byte-for-byte. **No hand-rolled crypto.** Constant-time comparison is not relevant here (the fingerprint is displayed for human verification, not compared against a secret).
- **[Network & I/O]** N/A — `Render` writes to `os.Stdout`, no network reads/writes added.
- **[Error messages / logs / telemetry]** Reviewed: the three new `Decode` error categories follow the existing "category only, no input bytes" discipline. `Render`'s `<invalid>` fallback emits a fixed literal, not the input bytes. The `cmd/pyry` side wraps `keys.LoadOrCreate`'s error verbatim; `keys` errors deliberately omit file contents (`internal/keys/store.go:148-185`). The token-and-pubkey-in-one-error class is structurally absent (verifier: `grep -nE 'Token|token' on every new fmt.Errorf and confirm none also mention pubkey`).
- **[Concurrency]** N/A — single-goroutine CLI path; no shared state introduced.
- **[Threat model alignment]** `docs/protocol-mobile.md` § *Security review* item 8 (*Static-key compromise*) is the relevant threat. This ticket does not address rotation (deferred to v3, named in the issue body). The ticket's contribution to the threat model is the fingerprint display path: users with the paranoia + bandwidth to verify the pubkey out-of-band now have the surface to do so, defeating the relay-MITM-with-replaced-pubkey class. The 64-bit width is what makes that verification trustworthy under a polynomial-time attacker.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-05-16
