# #211 — `internal/pair`: QR payload `Encode` / `Decode`

**Size:** XS (architect-confirmed). One new package `internal/pair`, two new
files (`payload.go`, `payload_test.go`). Production code is ~40 lines: one
struct, two pure functions, one sentinel error. 4 new exported names:
`Payload`, `Encode`, `Decode`, `ErrInvalidPayload`. Zero consumers wired in
this slice — QR rendering and paste-fallback display are sibling #212;
`pyry pair` minting + token plumbing land in later phase-3 tickets. Within
all S red lines (≤3 new files, ≤150 prod LOC, ≤5 new exported names, no
consumer cascade).

**Status:** ready for development.

**Depends on:** nothing wired. Imports `internal/identity` for `ServerID`
type (already on main, #206) and stdlib only otherwise.

## Files to read first

The developer's turn-1 data load. Each entry is paged in deliberately —
don't grep for them.

- `internal/config/config.go` (whole file, 46 lines) — **the reference
  shape** for "new leaf package, stdlib-only, three to four exports, ~40
  LOC, no logger, no consumers wired in this slice." Mirror the package
  doc-comment format (`// Package pair ...`), the imports-only-stdlib
  discipline, the test-file co-location.
- `internal/identity/server_id.go:1-52` — the typed-string newtype this
  spec consumes. `ServerID` is used as the `Server` field's type; do NOT
  redeclare or stringify it elsewhere. Re-validation on `Decode` goes
  through `identity.ParseServerID`; the package contract is "decode
  returns a `Payload` whose `ServerID` field is parse-validated."
- `internal/identity/server_id_test.go` (whole file, 95 lines) — table-
  driven test patterns to mirror (`tt := tt`, `t.Parallel()`, sub-tests
  via `t.Run(tt.name, …)`, `errors.Is` for sentinel checks).
- `docs/protocol-mobile.md:55-62` — wire contract for the three fields:
  - `server-id` — UUIDv4 canonical hex form, the relay's only routing key.
  - `device-token` — 256-bit random, hex-encoded; **plaintext** at this
    transport layer (the binary stores `sha256(token)` separately; #208
    handles hashing).
  - relay URL — domain-of-trust origin from the phone's POV.
- `docs/protocol-mobile.md:705-714` — appendix "first pairing" example:
  the unencoded JSON shape is `{"server":"…","relay":"…","token":"…"}`.
  Field names and order pinned by this example.
- `docs/protocol-mobile.md:567-609` — security framing: "paste-fallback is
  one-time-only," "MUST never display the device-token in plaintext after
  initial pairing," "per-device tokens leak via QR screenshots auto-uploaded
  to cloud backup." Read before writing any `Decode` error path — error
  messages must not echo decoded contents.
- `CODING-STYLE.md` § "Error Handling" (`fmt.Errorf("X: %w", err)` shape,
  sentinel-via-`errors.Is`), § "Testing" (table-driven, stdlib `testing`
  only, `t.Parallel()`).
- The ticket body itself (#211) — six AC bullets, each maps directly
  to one or two test cases in `payload_test.go`.

That's the read budget. The whole package is ~40 production lines.

## Context

Phase 3 (mobile + relay) needs `pyry pair` to print a QR code and a paste-
fallback string carrying three values the phone needs: which server-id to
target on the relay, which relay to target, and which bearer token to
present back. Those three values must come out of one encoder so the QR
and the paste string can never disagree — same input, same string, scanned
or pasted into the phone identically.

This ticket lands the **value type + encode/decode pair only**. Out of
scope: QR rendering, paste-fallback display surface (#212), token
generation, on-disk persistence of paired devices (#208), `pyry pair` CLI
glue (later phase-3 ticket). Pure functions, no I/O.

The decode operation exists primarily for the round-trip test (AC #4) —
the phone is the production decoder, and it lives outside this Go module.
Round-trip parity inside the binary's tests is how the developer proves
the encoding is well-formed without mocking the phone.

## Design

### Package placement

Flat package: `internal/pair`. Per CODING-STYLE: "one package per
concern", "avoid `pkg/`, `util/`, `common/`". Pairing is its own concern —
owned by neither `internal/identity` (typed identifiers, no transport
encoding) nor `internal/devices` (#208 — on-disk device entries +
token-hash) nor `internal/config` (operator-editable settings).

The name `pair` mirrors the user-visible verb (`pyry pair`); future
siblings in the same package land naturally — QR/paste rendering (#212),
the `pyry pair` command implementation, the `pyry pair list` surface.

Two new files:

```
internal/pair/
  payload.go        Payload struct, Encode, Decode, ErrInvalidPayload
  payload_test.go   Same-package tests, table-driven
```

No subpackages. No split into `payload.go` + `errors.go` — the production
file is small enough that splitting is premature.

### Exported surface

```go
// Package pair encodes the {server, relay, token} tuple a paired mobile
// device needs to connect through the relay back to this binary, and
// decodes strings produced by the same encoder. Pure functions; no I/O.
//
// The encoded form is a single ASCII string suitable for embedding in a
// QR symbol or for a one-time paste-fallback display. The wire shape is
// the JSON serialization of Payload, then base64url-encoded with the
// URL-safe alphabet and no padding.
//
// The Token field carries the plaintext bearer the mobile client will
// present back on subsequent connections. Hashing for storage happens
// elsewhere (#208 internal/devices). Callers MUST NOT log Payload or
// any Decode error in a context that may persist user-visible output;
// see the doc comments on Payload and ErrInvalidPayload.
package pair

import "github.com/pyrycode/pyrycode/internal/identity"

// Payload is the {server, relay, token} tuple emitted by `pyry pair` and
// consumed by the paired mobile device.
//
// Field order is fixed by the protocol-mobile.md appendix; do not reorder.
// All three fields are required; encoders SHOULD reject zero values
// upstream, decoders MUST (see Decode).
type Payload struct {
    Server identity.ServerID `json:"server"`
    Relay  string            `json:"relay"`
    Token  string            `json:"token"`
}

// Encode returns the wire-format string for p: JSON marshal, then
// base64url-encode (URL-safe alphabet, no padding). Encode does NOT
// validate p — empty fields produce an encoded string Decode will reject.
// The caller is expected to construct Payload from validated inputs
// (ServerID minted via identity.NewServerID, token via crypto/rand,
// relay URL from internal/config).
func Encode(p Payload) string

// Decode parses a string produced by Encode back into a Payload, applying
// the validation the protocol contract demands.
//
// Errors returned by Decode wrap ErrInvalidPayload (use errors.Is). The
// error message describes the failure category (invalid base64, invalid
// JSON, missing field) but MUST NOT include any portion of the input or
// of the partially-decoded fields — the input may carry a plaintext
// device-token whose visibility is one-time-only. See package doc
// comment.
//
// Decode returns ErrInvalidPayload-wrapped errors for:
//   - input that is not valid base64url (URL-safe alphabet, no padding)
//   - decoded bytes that are not a JSON object
//   - JSON containing trailing bytes after the top-level object
//     (the contract is "single payload per string"; trailing garbage
//     usually means the string was concatenated with another)
//   - server/relay/token field missing or the empty string
//   - server field not parseable by identity.ParseServerID
func Decode(s string) (Payload, error)

// ErrInvalidPayload is the sentinel returned (via wrap) for any input
// Decode rejects. Match with errors.Is; do not match on error strings.
var ErrInvalidPayload = errors.New("pair: invalid payload")
```

### Wire format

JSON, then base64url-no-pad. Pinned by the ticket AC and the protocol-
mobile.md row at line 61. Implementation hooks:

- Encode: `json.Marshal(p)` → `base64.RawURLEncoding.EncodeToString(b)`.
  `RawURLEncoding` is the stdlib alias for "URL-safe alphabet, no
  padding"; do not use `URLEncoding` (that's WITH padding) or
  `StdEncoding` (that's the `+/=` alphabet).
- Decode: `base64.RawURLEncoding.DecodeString(s)` →
  `json.Unmarshal(b, &p)` with strict trailing-byte handling, then
  field-presence checks, then `identity.ParseServerID`.

### Trailing-garbage check

The AC requires Decode to reject "trailing garbage." Two forms:

1. **Trailing bytes after the JSON object** (e.g.
   `{"server":"…","relay":"…","token":"…"}garbage`). `json.Unmarshal`
   silently consumes only the first JSON value; trailing bytes are lost.
   Use `json.Decoder` with `Decode(&p)` followed by a `Decode(&dummy)`
   that must return `io.EOF` — anything else (including another valid
   JSON value) is trailing garbage.

2. **Base64-decoder strictness.** `base64.RawURLEncoding.DecodeString`
   already rejects characters outside the URL-safe alphabet, padding
   chars (`=`), and trailing bits not aligned to a byte. No extra check
   needed at the base64 layer.

The extra-decode-must-be-EOF idiom is the same one `internal/control`'s
JSON framing uses; the pattern is established in the codebase.

### Field-validation order

Decode applies checks in this order, returning the first failure:

1. base64url decode → wrap as `ErrInvalidPayload` ("invalid base64url
   encoding").
2. `json.Decoder.Decode(&p)` — wrap as `ErrInvalidPayload` ("invalid
   JSON").
3. Second `Decoder.Decode` must be `io.EOF` — wrap as `ErrInvalidPayload`
   ("trailing data after JSON object").
4. Field presence: `p.Server`, `p.Relay`, `p.Token` all non-empty
   strings — wrap as `ErrInvalidPayload` ("missing field: <name>")
   where `<name>` is the JSON tag (`server`, `relay`, `token`). The
   field NAME is safe to include; the field VALUE is not.
5. `identity.ParseServerID(string(p.Server))` — wrap the returned
   `ErrInvalidServerID` as `ErrInvalidPayload` ("invalid server id").
   Do not echo the bad id; `identity.ParseServerID` itself doesn't
   include the input in its error message, and we preserve that.

Relay URL is **not** parsed/validated here. The protocol contract is "the
relay's domain is part of the pairing payload and trust-on-first-use from
the phone's POV"; the binary doesn't connect to it from this code path.
Validation belongs to whichever caller actually dials the URL (config
load already does shape-only no-validation; `pyry pair` will dial-test
in its own ticket). This package's contract is "round-trip the string a
phone scans"; semantic validation layers above. Documented as a comment
on `Payload.Relay`.

Token is **not** length- or alphabet-checked here. The protocol fixes the
token at "256-bit random, hex-encoded" but that's the minter's contract
(future ticket), not the encoding layer's. Decode rejects empty; that's
the contract this slice owns.

### What Encode does NOT do

- Does **not** validate `p.Server` against `ParseServerID`. The encoder's
  contract is "marshal what you're given." The minter (future ticket) is
  responsible for handing in a `ServerID` produced by `identity.NewServerID`.
  An unvalidated server-id produces a string Decode will reject — the
  error surfaces on round-trip, not on encode. Documented on `Encode`.
- Does **not** redact, log, or persist the token. Pure function.
- Does **not** add any envelope versioning. The protocol may eventually
  need a v2 (encrypted) payload; when that lands, add a separate
  `EncodeV2` / `DecodeV2` pair or use a versioned wire format. Out of
  scope here.

### Error wrapping shape

Sentinel + wrap, matching `internal/identity`'s `ErrInvalidServerID`:

```go
var ErrInvalidPayload = errors.New("pair: invalid payload")

// inside Decode, on a base64 failure:
return Payload{}, fmt.Errorf("%w: invalid base64url encoding", ErrInvalidPayload)
```

`errors.Is(err, ErrInvalidPayload)` is the only matcher callers should
use. The textual suffix is for human reading in CLI output and logs; it
must never include input bytes or decoded field values.

On any error, return the **zero Payload** (matching the `config.Load`
discipline: "On any error the returned Config is the zero value").

## Concurrency model

None. Pure functions, no shared state, no goroutines. Encode and Decode
are safe for concurrent use trivially.

## Error handling

Already covered above. Two surfaces:

| Operation | Failure mode | Return |
|---|---|---|
| Encode | None — `json.Marshal` of a struct with stdlib types cannot fail in practice. | `string` (no error). |
| Decode | base64 invalid / JSON invalid / trailing garbage / missing field / invalid server-id | `(Payload{}, fmt.Errorf("%w: <category>", ErrInvalidPayload))` |

`json.Marshal` returning an error from this struct shape is impossible
(no functions, no channels, no `MarshalJSON` methods that can fail). We
do not need to surface a return error from `Encode` to defend against a
failure mode that cannot occur — `Encode` returns `string` only.

## Testing strategy

`payload_test.go`, same package, stdlib `testing` only, table-driven where
the case-axis is wide.

| Test | What it pins |
|---|---|
| `TestEncode_DecodeRoundTrip` | AC #4: any Payload built from `identity.NewServerID()`, fixed relay, fixed hex token round-trips through Encode→Decode equal. Run 100 iterations against fresh server-ids. |
| `TestEncode_Format` | Encoded string is non-empty, contains only base64url-safe chars (`[A-Za-z0-9_-]`), has no `=` padding, decodes via `base64.RawURLEncoding` to a valid JSON object. (Pins the wire format AC #3 explicitly so a future refactor that swaps encoder doesn't pass round-trip silently.) |
| `TestDecode_Errors` | Table-driven, AC #5 + #6. Cases: empty string, non-base64 (`"!!!"`), valid base64 of `"not json"`, valid base64 of `"{}"` (missing all fields), valid base64 of `{"relay":"x","token":"y"}` (missing server), `{"server":"<valid>","token":"y"}` (missing relay), `{"server":"<valid>","relay":"x"}` (missing token), each field present-but-empty (`""`), `{"server":"not-a-uuid","relay":"x","token":"y"}` (invalid server-id), valid JSON with trailing garbage (e.g. `{"server":"…","relay":"x","token":"y"}{"x":1}`), padded base64 (must reject — `RawURLEncoding` already does), uppercase server-id (rejected by `ParseServerID`). Each case asserts `errors.Is(err, ErrInvalidPayload)` AND that the error message does NOT contain any character from the original input (defense against AC-aligned token leakage in logs — see security review below). |
| `TestEncode_StableField Order` | Encode the same payload twice, assert the encoded string is byte-identical. Pins JSON marshal determinism for QR-image determinism (a phone re-scanning shouldn't see two different strings). |
| `TestDecode_ZeroPayloadOnError` | For each rejected input from the error table, the returned `Payload` is the zero value. Pins the "no partial data leak" discipline matching `config.Load`. |

No PTY, no fixtures, no `t.TempDir`. Pure-function tests run under `go
test -race ./...` in milliseconds.

## Open questions

None. Everything the developer needs is pinned by ticket AC, by
`docs/protocol-mobile.md`, or by `internal/config` / `internal/identity`
patterns already on main.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The boundary is `Decode`: input is
  untrusted bytes, output is a `Payload` whose `Server` field has been
  re-parsed via `identity.ParseServerID`. The `Relay` and `Token` fields
  are non-empty-string-validated only and remain "string from a wire
  source" — downstream consumers (the relay-dialer ticket, the auth
  ticket) own further validation. The package doc comment documents this.

- **[Tokens, secrets, credentials]** SHOULD FIX (addressed in spec):
  the `Token` field is the plaintext device-token. Three concrete spec
  decisions defend the visibility-once-only protocol rule
  (`docs/protocol-mobile.md:608-609`, `:663`):
  1. `Decode` errors MUST NOT echo input bytes or decoded field values.
     The error message includes the failure category and (for missing-
     field) the JSON tag name only. The spec calls this out at
     "Field-validation order" step 4 and again under "Error wrapping
     shape." A test (`TestDecode_Errors`) asserts the error string
     contains no input characters.
  2. The package doc comment instructs callers to never log `Payload`
     in any persistent context.
  3. `Encode` does not log, persist, or transform the token. Pure
     function.
  Generation, hashing, and on-disk storage of the token are out of scope
  (#208 hashes; a future ticket mints).

- **[File operations]** Not applicable. No file I/O in this package.

- **[Subprocess / external command execution]** Not applicable. No
  subprocess invocation.

- **[Cryptographic primitives]** Not applicable for *generation* — this
  package does not generate the token. For *encoding choice*: base64url
  is a transport encoding, not a security primitive; chosen for QR-symbol
  compatibility (URL-safe alphabet works in the QR alphanumeric mode).
  Not a confidentiality boundary; the QR symbol itself is the
  confidentiality boundary (one-time display).

- **[Network & I/O]** Not applicable. Pure functions; no sockets, no
  file descriptors.

- **[Error messages, logs, telemetry]** No findings beyond the Token
  finding above. Decode error messages are bounded-length category
  strings; field-name leakage (`"server"`, `"relay"`, `"token"`) is
  acceptable — those are public protocol vocabulary, not secrets. The
  package emits no logs (no logger injected); operational logging is
  the caller's responsibility, and the package doc comment warns
  callers off logging the value.

- **[Concurrency]** Not applicable. Pure functions, no shared state.
  Encode/Decode are concurrency-safe trivially.

- **[Threat model alignment]** The relevant
  `docs/protocol-mobile.md` § Security model items for this slice:
  - "Per-device tokens can leak via … QR screenshots auto-uploaded to
    cloud backup" (line 603) — addressed by the token-visibility rule
    enforced via the doc-comment + Decode-error contract above. The QR
    screenshot itself is the user's risk; this package contributes the
    encoding layer that does not introduce a *second* exposure surface.
  - "Paste-fallback is one-time-only" (line 608) — sibling #212 owns
    the display surface; this ticket's contract is just "produce the
    string." Out of scope, named.
  - Relay TLS, server-id unguessability, token randomness — owned by
    other tickets (relay-dialer, #206, future minter respectively).
    Out of scope, named.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-08
