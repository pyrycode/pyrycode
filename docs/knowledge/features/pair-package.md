# `internal/pair` — QR pairing payload encoding + render

The `{server, relay, token, server_static_pubkey}` tuple a paired mobile device needs to authenticate the binary via Noise_IK and connect through the relay back to it, encoded as a single ASCII string suitable for embedding in a QR symbol or for one-time paste-fallback display, plus a `Render` surface that draws both the QR symbol, an 8-byte BLAKE2s fingerprint line for visual verification, and the paste-fallback line to a writer in one shot. Pure transforms; the only I/O is a caller-supplied `io.Writer`. Phase 3 (mobile + relay) foundation, extended in #432 for Mobile Protocol v2 (Noise_IK).

Stdlib (`bytes`, `encoding/base64`, `encoding/hex`, `encoding/json`, `errors`, `fmt`, `io`, `strings`) plus `internal/identity`, `github.com/mdp/qrterminal/v3` (added in #212; pure-Go, MIT, built on `rsc.io/qr`), and `golang.org/x/crypto/blake2s` (added in #432; same hash mandated by `Noise_IK_25519_ChaChaPoly_BLAKE2s` per [ADR 024](../decisions/024-noise-ik-mobile-e2e.md)). No goroutines, no logger, no `Config`, no `context.Context`. Concurrent callers with distinct writers are safe by construction.

## Surface

```go
type Payload struct {
    Server             identity.ServerID `json:"server"`
    Relay              string            `json:"relay"`
    Token              string            `json:"token"`
    ServerStaticPubkey string            `json:"server_static_pubkey"`
}

func Encode(p Payload) string
func Decode(s string) (Payload, error)
func Render(p Payload, w io.Writer) error
func Fingerprint(pubkey [32]byte) string

var ErrInvalidPayload = errors.New("pair: invalid payload")
```

Six exports. `Payload` is the value type; `Encode` is the wire-producer; `Decode` is the parse-and-validate inverse; `Render` is the user-visible display surface; `Fingerprint` is the pure formatter for the 8-byte BLAKE2s fingerprint string `Render` displays under the QR; `ErrInvalidPayload` is the sentinel for `errors.Is` branching.

The encoder and the decoder are the two ends of the same contract — same `Payload`, same wire string, scanned-or-pasted into the phone identically. The decoder exists primarily for the round-trip test (the phone is the production decoder and lives outside this Go module); round-trip parity inside the binary's tests is how we prove the encoding is well-formed without mocking the phone.

## Wire format

JSON, then base64url with the URL-safe alphabet and **no padding** (`base64.RawURLEncoding`).

The unencoded shape is `{"server":"…","relay":"…","token":"…","server_static_pubkey":"…"}` — field names and order pinned by `docs/protocol-mobile.md` § *Pairing flow* (lines 135-150) and § *Appendix* (lines 635-654). Field order is fixed; do not reorder.

`RawURLEncoding` is the stdlib alias for "URL-safe alphabet, no padding" — chosen for QR alphanumeric-mode compatibility and for paste-fallback robustness (no `+/=` to break URL contexts). Do not use `URLEncoding` (that's WITH padding) or `StdEncoding` (that's the `+/=` alphabet) for the outer envelope. **Note the asymmetry:** the outer envelope uses `RawURLEncoding`, but the `server_static_pubkey` value *inside* the envelope is encoded with `base64.StdEncoding` (padded, standard alphabet) — same alphabet used by `internal/keys`' on-disk JSON so the same 32-byte public point has the same string form in the QR payload and on disk, simplifying cross-system debugging.

Round-trip is byte-stable: the JSON marshaller emits fields in struct-declaration order, so re-encoding the same `Payload` produces a byte-identical string — the QR symbol scanned twice is the same QR symbol.

## `Encode` — production

```go
func Encode(p Payload) string {
    b, err := json.Marshal(p)
    if err != nil {
        panic(fmt.Errorf("pair: json.Marshal of Payload failed: %w", err))
    }
    return base64.RawURLEncoding.EncodeToString(b)
}
```

`json.Marshal` of a struct whose fields are all stdlib string types with no `MarshalJSON` methods cannot fail. Surfacing the impossible case via `panic` rather than returning `(string, error)` keeps the AC's pure-function signature and makes the impossibility loud — silently returning a corrupt string would let an unreachable bug ship.

`Encode` does **not** validate `p`. The encoder's contract is "marshal what you're given" — empty fields produce a string `Decode` will reject; the round-trip surfaces the error. The minter (sibling ticket) is responsible for handing in a `ServerID` produced by `identity.NewServerID`, a token from `crypto/rand`, and a relay URL from `internal/config`. `Encode` does not log, redact, persist, or transform the token; pure function.

## `Decode` — validation

Seven rejection categories, applied in order, returning the first failure:

1. **Base64.** `base64.RawURLEncoding.DecodeString(s)` — already strict (rejects characters outside the URL-safe alphabet, padding chars `=`, and trailing bits not aligned to a byte). No extra check needed at the base64 layer.
2. **JSON.** `json.Decoder.Decode(&p)` against a `bytes.Reader` of the decoded bytes.
3. **Trailing bytes.** A second `Decoder.Decode(&trailing)` MUST return `io.EOF`. `json.Unmarshal` silently consumes only the first JSON value; trailing bytes are lost. The `Decoder`-then-second-`Decode`-must-be-EOF idiom catches `{"server":…}garbage` and `{"server":…}{"another":1}` — both manifest as concatenated payloads, the usual symptom of a malformed paste. Same idiom `internal/control`'s JSON framing already uses.
4. **Field presence.** `Server`, `Relay`, `Token`, `ServerStaticPubkey` all non-empty strings. The error names the JSON tag (`server`, `relay`, `token`, `server_static_pubkey`) — those are public protocol vocabulary, not secrets. The field VALUE is never echoed.
5. **Server-id shape.** `identity.ParseServerID(string(p.Server))` re-validates the canonical UUIDv4 form. The package contract is "decode returns a `Payload` whose `Server` field is parse-validated."
6. **Pubkey base64 shape.** `base64.StdEncoding.DecodeString(p.ServerStaticPubkey)` must succeed. Reject text is `"invalid server_static_pubkey encoding"` — category only, never the decoded bytes nor the input string.
7. **Pubkey length.** Decoded bytes must be exactly 32 (X25519 public key width). Reject text is `"server_static_pubkey wrong length"` — does not include the observed length value; the category alone is enough for the operator.

Relay URL is **not** parsed/validated here. The protocol contract is "the relay's domain is part of the pairing payload and trust-on-first-use from the phone's POV"; the binary doesn't dial it from this code path. Validation belongs to whichever caller actually dials. Token is **not** length- or alphabet-checked — the protocol fixes the token at "256-bit random, hex-encoded" but that's the minter's contract, not the encoding layer's. `Decode` rejects empty; that's the contract this slice owns.

On any error, the returned `Payload` is the zero value — matches the `config.Load` discipline ("on any error the returned Config is the zero value"). Callers that ignore `err` see empty fields and break loudly rather than working with partial data.

## `Render` — display

`Render(p Payload, w io.Writer) error` writes five sections to `w` in order (#432 added §4):

1. A QR symbol of `Encode(p)`, drawn with UTF-8 half-block code points (`▀`, `▄`, `█`, space) via `qrterminal.GenerateHalfBlock(encoded, qrterminal.M, &tw)`.
2. A blank line.
3. `Encode(p)` on its own line.
4. The fingerprint line: `Static-key fp: aa:bb:cc:dd:ee:ff:11:22  (verify this matches the fingerprint shown on your phone)`. Computed via `Fingerprint(pub)` on the base64-decoded `p.ServerStaticPubkey`; if that decoding fails or yields a wrong-length value (only reachable via hand-built unvalidated `Payload`), the fingerprint slot reads `<invalid>` instead — non-panicking fallback for the Render-accepts-unvalidated-input contract.
5. The fixed instruction line: `Scan the QR with the Pyrycode mobile app, or paste the string above into the app's pairing screen.`

That's the entire output contract. No banner, no server-id summary, no relay summary, no warning copy. No ANSI color codes, no emoji, no terminal control sequences — paste-fallback users in non-TTY contexts (logs piped to a file) get clean output. No terminal-width sizing — `qrterminal/v3` produces a symbol whose width is fixed by the QR version (encoded length); narrower terminals wrap, that is the user's concern.

### `Fingerprint([32]byte) string` — exported pure formatter

```go
// Fingerprint returns the verifying-string form of pubkey:
// BLAKE2s-256(pubkey) truncated to 8 bytes, formatted as
// colon-separated lowercase hex ("aa:bb:cc:dd:ee:ff:11:22").
// Exactly 23 characters (8*2 hex + 7 colons).
func Fingerprint(pubkey [32]byte) string
```

Pure, deterministic, no I/O, no globals. Takes `[32]byte` (not `[]byte`) so wrong-length inputs are a compile error, not a runtime error. **The 8-byte / 64-bit truncation is load-bearing security** per `docs/protocol-mobile.md` § *Security review* → *[Cryptographic primitives]*: a 32-bit fingerprint is brute-forceable (≈ 2³² preimage search ≈ a few CPU-hours), so the spec standardises 64 bits everywhere the fingerprint appears. Do not narrow it. Reused by `Render`'s fingerprint line and available for any future operator-facing UI that needs to display the same canonical fingerprint form.

### Why `GenerateHalfBlock` and not `Generate`

`qrterminal/v3` exposes two QR drawers. `Generate` uses one terminal cell per QR module (`█` and space) — symbol becomes very tall (each row taller than wide in typical terminal fonts). `GenerateHalfBlock` packs two QR rows per terminal row using `▀`/`▄`/`█`/space — symbol comes out roughly square at typical 2:1 terminal cell aspect ratios and scans more reliably with phone cameras precisely because the printed shape matches QR's expected aspect ratio. The AC explicitly mentions "the half-block variants emitted by `qrterminal/v3`."

### Error-correction level: M

`qrterminal.M` (medium, ~15% recovery) is the library default and a good fit for the payload size (~120-140 base64 characters → version 5–6 QR symbol, well within an 80-column terminal). `L` shrinks the symbol slightly but is fragile to terminal font glitches; `Q`/`H` enlarge the symbol and risk wrapping in 80-column terminals. Locking `M` keeps the symbol predictable across phone-camera + terminal-font combinations. The level is a literal at the call site — no package-level var, no config knob, no exposing the choice in the function signature.

### `errTrackingWriter` — error propagation through a no-error-return library

`qrterminal.GenerateHalfBlock` has signature `func(text string, l Level, w io.Writer)` — no error return. Internally it calls `w.Write` and discards errors. To honor AC #3 ("returns an error if the writer fails; does not panic on writer failure; does not partially-render-and-swallow"), `Render` passes a small unexported `errTrackingWriter` wrapper that captures the first non-nil error and short-circuits all subsequent `Write` calls (returning `(0, t.err)` once errored). After the four ordered writes, `Render` returns `tw.err`.

The "natural error-tracking adapter" pattern matches what stdlib does internally (cf. `bufio.Writer.flush` short-circuit on `b.err`). `TestRender_DoesNotPanicOnBrokenWriter` is the belt-and-suspenders proof: a writer that panics if called more than once is never reached past the first error, because both qrterminal's subsequent writes and the trailing `fmt.Fprintln`s short-circuit through the trap.

The returned error is **the first underlying writer error, raw — not wrapped**. `errors.Is(err, io.ErrShortWrite)` matches in the test because the bare error is propagated. The AC doesn't ask for context-wrapping; the stdlib idiom for "you handed me a writer; here's what your writer told me" is the bare error.

### `Render` does NOT

- Validate `p` (same posture as `Encode`; a zero `Payload` will produce a string `Decode` and the phone reject, surfacing on scan, not on render).
- Call `Decode` on the encoded string before writing (round-trip is `payload.go`'s invariant; re-running it here would couple this surface to that test for no protocol benefit).
- Call `Encode(p)` more than once (the encoded bytes are cached in a local — `Encode` is deterministic, but one call is one call).
- Persist anything. No file I/O, no logging, no copy-to-anywhere. Pure in-memory transform fed to one writer.

### Render tests

`internal/pair/render_test.go`, same package, stdlib `testing` only:

- `TestRender_Format_Happy` — non-empty buffer, contains at least one of `{█, ▀, ▄}`, contains `Encode(p)` as a substring, contains the byte-exact fingerprint substring `Static-key fp: 32:0b:5e:a9:9e:65:3b:c2` (the fingerprint of 32 zero bytes used by `samplePayload`), contains the verify hint substring, and contains the exact instruction string.
- `TestRender_FieldOrder` — split the buffer at the `Encode(p)` substring; assert at least one QR block code point in the prefix, the instruction line in the suffix, **the fingerprint line strictly between the encoded payload and the instruction line** (`encoded(idx) < fingerprint(fpIdx) < instruction(instrIdx)`), and at least one blank line between the last QR row and the encoded-payload line.
- `TestRender_InvalidPubkey_DoesNotPanic` — hand-built `Payload` with `ServerStaticPubkey = "!!!"`; asserts `Render` returns nil and output contains `Static-key fp: <invalid>`. Pins the non-panicking fallback for the `Render`-accepts-unvalidated-input contract.
- `TestRender_Deterministic` — Render the same `Payload` twice into two buffers; assert `bytes.Equal`. A phone re-scanning shouldn't see a different symbol; pinned via JSON marshal determinism (`payload.go`), `qrterminal/v3`'s stateless `Generate*`, and `Fingerprint`'s pure-function shape.
- `TestRender_WriterError` — a writer whose every `Write` returns `io.ErrShortWrite`; assert `errors.Is(err, io.ErrShortWrite)` (bare error propagated, not wrapped).
- `TestRender_DoesNotPanicOnBrokenWriter` — a writer that panics if called more than once after the first error; pins the `errTrackingWriter` short-circuit.

Test failure messages do NOT echo `Encode(p)` or buffer contents — same `TestDecode_Errors` discipline (no input bytes in error strings) extended into the render tests, hardening the muscle for when this code path runs against real tokens in the future `pyry pair` integration test.

### Token-secrecy contract continues at the render layer

Render's output **contains the plaintext device-token** (visible inside the QR symbol and verbatim on the paste-fallback line). The function doc-comment instructs callers explicitly: never log the writer's destination, never capture this output into any persisted context (CI logs, telemetry, error reports), treat the calling goroutine's stdout as the only intended sink. The future `pyry pair` ticket will pass `os.Stdout`; tests pass a `bytes.Buffer` that goes out of scope at function exit. The QR-screenshot exposure surface is the user's risk (auto-uploaded cloud backup is the documented threat in `protocol-mobile.md:603`); the render layer does not introduce a *third* exposure surface beyond the two already documented.

## Error wrapping shape

Sentinel + wrap, matching `internal/identity`'s `ErrInvalidServerID`:

```go
return Payload{}, fmt.Errorf("%w: invalid base64url encoding", ErrInvalidPayload)
```

`errors.Is(err, ErrInvalidPayload)` is the only matcher callers should use. The textual suffix is a bounded-length category string for human reading in CLI output and logs — never input bytes, never decoded field values. Field NAMES (`server`, `relay`, `token`) are safe; field VALUES are not.

## Token visibility (SECURITY)

The `Token` field carries the **plaintext** bearer the mobile client will present back on subsequent connections. Three discipline points enforce the protocol's "visibility ends when the QR symbol is dismissed" rule (`docs/protocol-mobile.md:608-609`, `:663`):

- **`Decode` errors never echo input or decoded fields.** The error message is a category string (`"invalid base64url encoding"`, `"invalid JSON"`, `"trailing data after JSON object"`, `"missing field: <name>"`, `"invalid server id"`). A test in `payload_test.go` asserts the error string contains no character of the input.
- **The package doc comment instructs callers to never log `Payload`, `Encode` output, or `Decode` errors in any context that may persist user-visible output.** The plain token appears at exactly two sites: pairing (QR + paste-fallback string) and per-WS-connect (phone presents). Outside those, only `sha256(token)` exists on disk and in memory (see [`features/devices-package.md`](devices-package.md)).
- **`Encode` does not log, redact, or persist anything.** Pure function. Leaks can only originate in callers; code review enforces the discipline on any future caller that holds a `Payload`.

The QR screenshot itself is the user's risk (auto-uploaded cloud backup is the documented threat); this package contributes the encoding layer that does not introduce a *second* exposure surface.

## Tests (`payload_test.go`)

`internal/pair/payload_test.go`, same package, table-driven, `t.Parallel()` everywhere, stdlib `testing` only:

- `TestEncode_DecodeRoundTrip` — 100 iterations against fresh `identity.NewServerID()` ids; assert `Decode(Encode(p)) == p`. Payload literal includes `ServerStaticPubkey: testStaticPubkeyB64` (base64 of 32 zero bytes).
- `TestEncode_Format` — Encoded string is non-empty, contains only base64url-safe chars (`[A-Za-z0-9_-]`), has no `=` padding, decodes via `base64.RawURLEncoding` to a valid JSON object whose keys include `server`, `relay`, `token`, `server_static_pubkey`. Pins the wire format so a future encoder swap can't pass round-trip silently.
- `TestEncode_Stable` — encode the same payload twice, assert byte-identical. Pins JSON marshal determinism for QR-image determinism.
- `TestDecode_Errors` — table-driven. Cases: empty; non-base64 (`"!!!"`); valid base64 of `"not json"`; valid base64 of `"{}"`; missing each of the four fields individually; each field present-but-empty (four rows); invalid server-id (`"not-a-uuid"`); uppercase server-id (rejected by `ParseServerID`); valid JSON with trailing JSON object; valid JSON with trailing garbage; non-base64 `server_static_pubkey` (`"!!!"`); too-short pubkey (31 bytes); too-long pubkey (33 bytes); sentinel-token rows that carry a structurally-valid pubkey field to reach other checks. Each row asserts `errors.Is(err, ErrInvalidPayload)` AND that the error string contains no character of the input — the token-leakage defense.
- `TestDecode_ZeroPayloadOnError` — for each rejected input, the returned `Payload` is the zero value. Pins the no-partial-data discipline matching `config.Load`.
- `TestDecode_AcceptsTrailingWhitespace` — `json.Decoder` consumes trailing whitespace before returning `io.EOF`, so `Decode` accepts payloads with trailing whitespace inside the base64-decoded JSON.
- `TestFingerprint_FixedVector` — hardcoded vector: `Fingerprint([32]byte{}) == "32:0b:5e:a9:9e:65:3b:c2"`. Expected value computed once out-of-band and pinned as a literal — **not** recomputed at test time from `blake2s.Sum256`, which would be tautological. Pins the hash primitive (BLAKE2s-256) and the 8-byte truncation width.
- `TestFingerprint_LengthAndShape` — non-zero `[32]byte`; asserts `len(got) == 23` and regex `^[0-9a-f]{2}(:[0-9a-f]{2}){7}$`. Pins the format independent of hash value.

No fixtures, no `t.TempDir`, no PTY. Pure-function tests run under `go test -race ./...` in milliseconds.

## Why a separate `internal/pair` package

Pairing is its own concern, owned by neither `internal/identity` (typed identifiers, no transport encoding) nor `internal/devices` (on-disk device entries + token-hash) nor `internal/config` (operator-editable settings). The name mirrors the user-visible verb (`pyry pair`); future siblings land naturally — the `pyry pair` CLI implementation, the `pyry pair list` surface — without bloating an unrelated package.

## Out of scope (deferred)

- **Phone-side handling of `server_static_pubkey`** — pyrycode-mobile track. Binary now emits the field; phone team consumes it in the Noise_IK initiator path.
- **Static-key rotation / re-pair flow** — v3 per [ADR 024](../decisions/024-noise-ik-mobile-e2e.md). Payload shape would not need to change (same field name), but the operator-facing CLI and the mobile UX both need design work.
- **On-disk persistence of paired devices** — owned by `internal/devices` (token hashing) and `internal/devices.Registry` (load/save).
- **Encrypted inner-payload envelope.** Noise_IK is layered above this package by `internal/relay` + the future Noise wrapper (#433); `pair.Payload` carries only the pre-handshake trust anchor.
- **Relay URL syntax validation** — owned by whichever caller dials. The QR contract is "round-trip the string a phone scans"; semantic validation layers above.
- **Token alphabet/length checks** — the minter owns format; `Decode` only rejects empty.

## Related

- [`features/identity-package.md`](identity-package.md) — `Payload.Server` is `identity.ServerID`; `Decode` calls `identity.ParseServerID`.
- [`features/devices-package.md`](devices-package.md) — Phase 3 sibling; on-disk hash of the same plaintext token this package transports.
- [`features/config-package.md`](config-package.md) — Phase 3 sibling; the relay URL surfaced in `Payload.Relay` comes from `Config.RelayURL`.
- [`features/keys-package.md`](keys-package.md) — provider of the X25519 public point that `Payload.ServerStaticPubkey` transports; `StaticKey.PublicKey()` is the producer-side accessor used by `cmd/pyry/pair.go`.
- [ADR 024](../decisions/024-noise-ik-mobile-e2e.md) — Mobile Protocol v2 (Noise_IK) parent decision; § *Pairing flow* pins the field naming/order; § *Security review* pins the 8-byte fingerprint width.
- `docs/protocol-mobile.md` § *Pairing flow* (lines 135-150) — canonical JSON shape including `server_static_pubkey`.
- `docs/protocol-mobile.md` § *Appendix: example flow* (lines 635-654) — byte-exact desktop output the CLI must produce.
- `docs/protocol-mobile.md` § *UX implications* (lines 540-547) — fingerprint placement under the QR + one-line hint requirement.
- `docs/protocol-mobile.md` § *Security review* (lines 720-724) — 64-bit fingerprint width as load-bearing security.
- `docs/protocol-mobile.md:567-609` — security framing (paste-fallback one-time-only, never display token after pairing, QR-screenshot threat).
- `docs/specs/architecture/211-pair-qr-payload-encoding.md` — architect's spec for `Payload` + `Encode`/`Decode`.
- `docs/specs/architecture/212-pair-qr-render.md` — architect's spec for `Render`.
- [`docs/specs/architecture/432-pair-server-static-pubkey.md`](../../specs/architecture/432-pair-server-static-pubkey.md) — architect's spec for the `server_static_pubkey` + fingerprint extension.
