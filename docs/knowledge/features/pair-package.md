# `internal/pair` — QR pairing payload encoding + render

The `{server, relay, token}` tuple a paired mobile device needs to connect through the relay back to this binary, encoded as a single ASCII string suitable for embedding in a QR symbol or for one-time paste-fallback display, plus a `Render` surface that draws both the QR symbol and the paste-fallback line to a writer in one shot. Pure transforms; the only I/O is a caller-supplied `io.Writer`. Phase 3 (mobile + relay) foundation; the `pyry pair` CLI glue that wires `os.Stdout` into `Render` lands later.

Stdlib (`bytes`, `encoding/base64`, `encoding/json`, `errors`, `fmt`, `io`) plus `internal/identity` and `github.com/mdp/qrterminal/v3` (added in #212; pure-Go, MIT, built on `rsc.io/qr`). No goroutines, no logger, no `Config`, no `context.Context`. Concurrent callers with distinct writers are safe by construction.

## Surface

```go
type Payload struct {
    Server identity.ServerID `json:"server"`
    Relay  string            `json:"relay"`
    Token  string            `json:"token"`
}

func Encode(p Payload) string
func Decode(s string) (Payload, error)
func Render(p Payload, w io.Writer) error

var ErrInvalidPayload = errors.New("pair: invalid payload")
```

Five exports. `Payload` is the value type; `Encode` is the wire-producer; `Decode` is the parse-and-validate inverse; `Render` is the user-visible display surface; `ErrInvalidPayload` is the sentinel for `errors.Is` branching.

The encoder and the decoder are the two ends of the same contract — same `Payload`, same wire string, scanned-or-pasted into the phone identically. The decoder exists primarily for the round-trip test (the phone is the production decoder and lives outside this Go module); round-trip parity inside the binary's tests is how we prove the encoding is well-formed without mocking the phone.

## Wire format

JSON, then base64url with the URL-safe alphabet and **no padding** (`base64.RawURLEncoding`).

The unencoded shape is `{"server":"…","relay":"…","token":"…"}` — field names and order pinned by `docs/protocol-mobile.md`'s first-pairing appendix (lines 705-714). Field order is fixed; do not reorder.

`RawURLEncoding` is the stdlib alias for "URL-safe alphabet, no padding" — chosen for QR alphanumeric-mode compatibility and for paste-fallback robustness (no `+/=` to break URL contexts). Do not use `URLEncoding` (that's WITH padding) or `StdEncoding` (that's the `+/=` alphabet).

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

Five rejection categories, applied in order, returning the first failure:

1. **Base64.** `base64.RawURLEncoding.DecodeString(s)` — already strict (rejects characters outside the URL-safe alphabet, padding chars `=`, and trailing bits not aligned to a byte). No extra check needed at the base64 layer.
2. **JSON.** `json.Decoder.Decode(&p)` against a `bytes.Reader` of the decoded bytes.
3. **Trailing bytes.** A second `Decoder.Decode(&trailing)` MUST return `io.EOF`. `json.Unmarshal` silently consumes only the first JSON value; trailing bytes are lost. The `Decoder`-then-second-`Decode`-must-be-EOF idiom catches `{"server":…}garbage` and `{"server":…}{"another":1}` — both manifest as concatenated payloads, the usual symptom of a malformed paste. Same idiom `internal/control`'s JSON framing already uses.
4. **Field presence.** `Server`, `Relay`, `Token` all non-empty strings. The error names the JSON tag (`server`, `relay`, `token`) — those are public protocol vocabulary, not secrets. The field VALUE is never echoed.
5. **Server-id shape.** `identity.ParseServerID(string(p.Server))` re-validates the canonical UUIDv4 form. The package contract is "decode returns a `Payload` whose `Server` field is parse-validated."

Relay URL is **not** parsed/validated here. The protocol contract is "the relay's domain is part of the pairing payload and trust-on-first-use from the phone's POV"; the binary doesn't dial it from this code path. Validation belongs to whichever caller actually dials. Token is **not** length- or alphabet-checked — the protocol fixes the token at "256-bit random, hex-encoded" but that's the minter's contract, not the encoding layer's. `Decode` rejects empty; that's the contract this slice owns.

On any error, the returned `Payload` is the zero value — matches the `config.Load` discipline ("on any error the returned Config is the zero value"). Callers that ignore `err` see empty fields and break loudly rather than working with partial data.

## `Render` — display

`Render(p Payload, w io.Writer) error` writes four sections to `w` in order:

1. A QR symbol of `Encode(p)`, drawn with UTF-8 half-block code points (`▀`, `▄`, `█`, space) via `qrterminal.GenerateHalfBlock(encoded, qrterminal.M, &tw)`.
2. A blank line.
3. `Encode(p)` on its own line.
4. The fixed instruction line: `Scan the QR with the Pyrycode mobile app, or paste the string above into the app's pairing screen.`

That's the entire output contract. No banner, no server-id summary, no relay summary, no warning copy — those belong to the eventual `pyry pair` CLI ticket. No ANSI color codes, no emoji, no terminal control sequences — paste-fallback users in non-TTY contexts (logs piped to a file) get clean output. No terminal-width sizing — `qrterminal/v3` produces a symbol whose width is fixed by the QR version (encoded length); narrower terminals wrap, that is the user's concern.

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

- `TestRender_Format_Happy` — non-empty buffer, contains at least one of `{█, ▀, ▄}`, contains `Encode(p)` as a substring, contains the exact instruction string.
- `TestRender_FieldOrder` — split the buffer at the `Encode(p)` substring; assert at least one QR block code point in the prefix, the instruction line in the suffix, and at least one blank line between the last QR row and the encoded-payload line.
- `TestRender_Deterministic` — Render the same `Payload` twice into two buffers; assert `bytes.Equal`. A phone re-scanning shouldn't see a different symbol; pinned via JSON marshal determinism (`payload.go`) and `qrterminal/v3`'s stateless `Generate*`.
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

`internal/pair/payload_test.go`, same package, table-driven, `t.Parallel()` everywhere, stdlib `testing` only. Five tests:

- `TestEncodeDecodeRoundTrip` — AC #4. 100 iterations against fresh `identity.NewServerID()` ids; assert `Decode(Encode(p)) == p`.
- `TestEncode_Format` — AC #3 explicitly. Encoded string is non-empty, contains only base64url-safe chars (`[A-Za-z0-9_-]`), has no `=` padding, decodes via `base64.RawURLEncoding` to a valid JSON object. Pins the wire format so a future encoder swap can't pass round-trip silently.
- `TestEncode_StableFieldOrder` — encode the same payload twice, assert byte-identical. Pins JSON marshal determinism for QR-image determinism.
- `TestDecode_Errors` — table-driven, AC #5 + #6. Cases: empty, non-base64 (`"!!!"`), valid base64 of `"not json"`, valid base64 of `"{}"`, missing each field individually, each field present-but-empty, invalid server-id (`"not-a-uuid"`), uppercase server-id (rejected by `ParseServerID`), valid JSON with trailing JSON object, valid JSON with trailing garbage, padded base64 (must reject — `RawURLEncoding` already does). Each row asserts `errors.Is(err, ErrInvalidPayload)` AND that the error string contains no character of the input — the token-leakage defense.
- `TestDecode_ZeroPayloadOnError` — for each rejected input, the returned `Payload` is the zero value. Pins the no-partial-data discipline matching `config.Load`.

No fixtures, no `t.TempDir`, no PTY. Pure-function tests run under `go test -race ./...` in milliseconds.

## Why a separate `internal/pair` package

Pairing is its own concern, owned by neither `internal/identity` (typed identifiers, no transport encoding) nor `internal/devices` (on-disk device entries + token-hash) nor `internal/config` (operator-editable settings). The name mirrors the user-visible verb (`pyry pair`); future siblings land naturally — the `pyry pair` CLI implementation, the `pyry pair list` surface — without bloating an unrelated package.

## Out of scope (deferred)

- **`pyry pair` CLI** — later phase-3 ticket. Wires `identity.NewServerID()` + token mint + `config.Load().RelayURL` into a `Payload`, calls `Render` with `os.Stdout`.
- **Token minting** — later phase-3 ticket. `crypto/rand` 256-bit + hex encode.
- **On-disk persistence of paired devices** — sibling #208 owns hashing; the registry-CRUD ticket loads/saves rows.
- **Envelope versioning.** A future v2 (encrypted) payload would land as a separate `EncodeV2` / `DecodeV2` pair or a versioned wire format; out of scope here.
- **Relay URL syntax validation** — owned by whichever caller dials. The QR contract is "round-trip the string a phone scans"; semantic validation layers above.
- **Token alphabet/length checks** — the minter owns format; `Decode` only rejects empty.

## Related

- [`features/identity-package.md`](identity-package.md) — `Payload.Server` is `identity.ServerID`; `Decode` calls `identity.ParseServerID`.
- [`features/devices-package.md`](devices-package.md) — Phase 3 sibling; on-disk hash of the same plaintext token this package transports.
- [`features/config-package.md`](config-package.md) — Phase 3 sibling; the relay URL surfaced in `Payload.Relay` comes from `Config.RelayURL`.
- `docs/protocol-mobile.md:55-62` — wire contract (server-id, device-token, relay URL roles).
- `docs/protocol-mobile.md:705-714` — pairing-flow appendix; pins JSON field names and order.
- `docs/protocol-mobile.md:567-609` — security framing (paste-fallback one-time-only, never display token after pairing, QR-screenshot threat).
- `docs/specs/architecture/211-pair-qr-payload-encoding.md` — architect's spec for `Payload` + `Encode`/`Decode`.
- `docs/specs/architecture/212-pair-qr-render.md` — architect's spec for `Render`.
