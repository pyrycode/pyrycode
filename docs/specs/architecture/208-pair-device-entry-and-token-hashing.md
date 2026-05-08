# #208 — `internal/devices` package: `Device` type + token hashing

**Size:** XS (architect-confirmed). Single new package `internal/devices` with
two new files (`device.go`, `device_test.go`). Production code is ~40 lines
(one struct, two pure functions). 3 new exported names: `Device`,
`HashToken`, `VerifyToken`. Zero consumers — registry CRUD and token
minting are sibling Phase 3 tickets. Within all S red lines (≤3 new
files, ≤150 prod lines, ≤5 new exported names, no consumer cascade).

**Status:** ready for development.

**Depends on:** nothing. The package is a leaf — no imports beyond
stdlib (`crypto/sha256`, `crypto/subtle`, `encoding/hex`, `time`).

## Files to read first

The developer's turn-1 data load. Each entry is paged in deliberately —
don't grep for them.

- `internal/config/config.go` (the whole file, 46 lines) — **the
  reference shape** for "new leaf package, stdlib-only, three exports,
  ~40 LOC, no logger, no consumers wired in this slice." Mirror the
  package doc-comment format (`// Package devices ...`), the
  imports-only-stdlib discipline, the test-file co-location.
- `internal/sessions/registry.go:17-29` — `registryEntry`'s field
  layout. Mirror the JSON-tag style: snake_case (`json:"token_hash"`),
  no `omitempty` on required fields, `time.Time` fields tagged the
  same way. The sibling CRUD ticket will load/save `Device`s through
  the same atomic-rename pattern `loadRegistry` / `saveRegistryLocked`
  use; field tags chosen now keep that ticket trivial.
- `docs/protocol-mobile.md:62` — the row that fixes the on-disk
  contract: `device-token` is "256-bit random, hex-encoded ... binary
  stores `sha256(token)` in `devices.json`, never the plaintext." This
  is the threat-model anchor — read it before deciding "why not
  bcrypt?".
- `docs/protocol-mobile.md:97-98` — the runtime call site that will
  eventually call `VerifyToken`. Auth path is "phone sends first frame
  → binary validates device-token → on invalid, send `error` envelope
  with `auth.invalid_token` and ask relay to close." Out of scope here;
  read so the function signature you ship doesn't surprise that future
  ticket.
- `docs/protocol-mobile.md:663` — token visibility rule: "MUST never
  display the device-token in plaintext after initial pairing." This
  is why the package doc comment must call out "never log plain;
  never wrap plain into error context."
- `CODING-STYLE.md` § "Error Handling" — `fmt.Errorf("X: %w", err)`
  shape (not used in this ticket — neither function returns an error
  — but the discipline applies if the spec is wrong and someone adds
  one). § "Testing" — table-driven, stdlib `testing` only,
  `t.Parallel()`, `t.Helper()` for shared assertions, no testify.
- The ticket body itself (#208) — four AC bullets, each maps directly
  to one or two test cases.

That's the read budget. The whole package is ~40 lines.

## Context

Phase 3 (mobile + relay) needs paired devices. The flow:

1. `pyry pair` (sibling ticket) mints a 256-bit random token, displays
   it in a QR code, and stores `(name, sha256(token), paired_at,
   last_seen_at)` in `~/.pyry/<name>/devices.json`.
2. The phone scans the QR, persists the plain token in its keychain,
   and presents it on every WS connect.
3. The binary, on first frame from a phone, computes
   `sha256(presented_token)` and constant-time compares against each
   stored hash. Match → upgrade the connection; no match → close.

This ticket introduces only the type and the two pure functions. There
is intentionally no minting, no on-disk persistence, no registry CRUD,
no auth wiring in this ticket. That keeps the slice small and the
crypto-relevant primitives reviewable on their own.

## Design

### Package placement

Flat package: `internal/devices`. Per CODING-STYLE: "one package per
concern", "avoid `pkg/`, `util/`, `common/`". Devices are their own
concern — owned by neither `sessions` (claude lifecycle) nor `control`
(Unix socket protocol) nor `config` (operator-editable settings).

Two new files:

```
internal/devices/
  device.go        Device struct, HashToken, VerifyToken
  device_test.go   Same-package tests, table-driven
```

No subpackages. No split into `device.go` + `hash.go` — the production
file is small enough that splitting is premature.

### Exported surface

```go
// Package devices defines the on-disk Device type and the token
// hashing/verification primitives used by `pyry pair` and the mobile
// auth path.
//
// SECURITY: callers MUST NOT log a plain device-token, MUST NOT wrap
// a plain token into error context, and MUST NOT pass a plain token
// across log/slog fields. The plain token appears once at pairing
// (QR code, paste-fallback string) and once per WS-connect (the
// phone presents it for verification). Outside those two sites the
// only on-disk and in-memory representation is the SHA-256 hex hash.
package devices

import (
    "crypto/sha256"
    "crypto/subtle"
    "encoding/hex"
    "time"
)

// Device is the on-disk shape for one paired device. Persisted by the
// sibling registry-CRUD ticket; never marshalled across the wire
// (the wire carries plain tokens once at handshake, then routing
// envelopes by server-id).
type Device struct {
    TokenHash  string    `json:"token_hash"`
    Name       string    `json:"name"`
    PairedAt   time.Time `json:"paired_at"`
    LastSeenAt time.Time `json:"last_seen_at"`
}

// HashToken returns the lowercase SHA-256 hex of plain. Output is
// always 64 hex characters (sha256.Size * 2). The same input always
// produces the same output (deterministic, no salt — see "Why no
// bcrypt or salt" below).
func HashToken(plain string) string

// VerifyToken reports whether HashToken(plain) equals hash, in
// constant time relative to hash's length. Returns false for any
// hash whose length differs from the canonical 64-char hex (this
// includes the empty string and any malformed hex). Never panics;
// never logs; never returns the plain or hash in any error.
func VerifyToken(plain, hash string) bool
```

No exported errors, no sentinels (the verify primitive returns bool
by design — auth-decision-as-error is the caller's concern, not the
crypto primitive's). No exported `HashLen` constant — the sibling
CRUD ticket can add it next to a validator if/when needed (YAGNI:
this ticket has no consumer).

### `HashToken` body

```go
func HashToken(plain string) string {
    sum := sha256.Sum256([]byte(plain))
    return hex.EncodeToString(sum[:])
}
```

Two stdlib calls. `hex.EncodeToString` returns lowercase by AC. The
sum array is value-copied; no allocation beyond the returned string.

### `VerifyToken` body

```go
func VerifyToken(plain, hash string) bool {
    expected := HashToken(plain)
    return subtle.ConstantTimeCompare([]byte(expected), []byte(hash)) == 1
}
```

`subtle.ConstantTimeCompare` returns 0 (false) when the two slices
have different lengths, in constant time relative to the slice
arguments. Empty `hash`, malformed `hash`, or any-length-≠-64 `hash`
all fall out with `false` via this length-mismatch path. There is
intentionally no early-return guard on `hash == ""` — the unguarded
shape is shorter, makes the constant-time discipline auditable in
one line, and the AC bullet "false on empty/malformed hash" is
satisfied by `ConstantTimeCompare`'s documented semantics, not by a
manual check.

### Why no bcrypt or salt

Recorded here so the next reviewer doesn't relitigate it.

The token is 256 bits of `crypto/rand` output (sibling minting
ticket). Brute force across 2^256 candidates is infeasible regardless
of hash speed; the hash exists only to prevent **plaintext at rest**
(if `devices.json` leaks, the attacker holds hashes, not tokens). For
that threat model:

- **Bcrypt** is designed for low-entropy human passwords. Slowing the
  attacker by a constant factor matters when the keyspace is ~50
  bits; it's irrelevant at 256 bits. Bcrypt also caps input at 72
  bytes; a 256-bit hex token is 64 chars, fine, but the cap is a
  footgun for any future format change. Rejected.
- **Per-token salt** defends against precomputation (rainbow tables)
  on shared-keyspace inputs (e.g. common passwords). 256-bit random
  inputs share no keyspace with any other deployment; precomputation
  is meaningless. A salt would add complexity (column on disk, salt
  retrieval before verify) for no defensive gain. Rejected.

This decision aligns with `docs/protocol-mobile.md:62` ("binary
stores `sha256(token)` in `devices.json`, never the plaintext") —
the protocol spec already commits the binary to plain SHA-256.

### Determinism is intentional

`HashToken` is deterministic: the same plain produces the same hash
across runs, machines, processes. This is what makes the verify path
trivial (compute once, compare). The cost is "two binaries with the
same plain token would store identical hashes" — irrelevant because
each binary mints its own tokens for its own paired devices; tokens
don't cross binaries.

### Why `Device` carries JSON tags now

The struct definition is in this ticket; the registry CRUD that
marshals it is in a sibling ticket. Adding the tags here keeps that
sibling trivial: it imports `Device` and serialises with stdlib
`encoding/json`, no struct rewrite. Tags are pure addition (compiler
sees them, runtime only sees them via `encoding/json`); the AC
bullet's literal "fields TokenHash string, Name string, PairedAt
time.Time, LastSeenAt time.Time" is satisfied — the tags are
metadata, not new fields. snake_case + no `omitempty` matches the
existing `registryEntry` pattern (see `internal/sessions/registry.go:17-29`).

### Concurrency

Both functions are pure. No package-level state, no goroutines, no
mutexes. Concurrent callers are safe by construction.

### Logger / config / context

None. No `*slog.Logger` parameter (CODING-STYLE prefers it injected,
but neither function logs anything — the SECURITY note in the package
doc comment establishes that). No `context.Context` (no I/O, no
cancellation point). No `Config` (no tunables — SHA-256 is the chosen
primitive, not a switch).

## Testing strategy

Same-package tests in `internal/devices/device_test.go`. Table-driven
where it helps; one direct test for determinism. `t.Parallel()` on
every test (no shared state — pure functions, each writes nothing).

### `TestHashToken_Deterministic`

Pin the deterministic property end-to-end:

```go
func TestHashToken_Deterministic(t *testing.T) {
    t.Parallel()
    const plain = "abc123-fixture-not-a-real-token"
    h1 := HashToken(plain)
    h2 := HashToken(plain)
    if h1 != h2 {
        t.Fatalf("HashToken not deterministic: %q vs %q", h1, h2)
    }
    if got, want := len(h1), 64; got != want {
        t.Errorf("HashToken length = %d, want %d", got, want)
    }
    // Lowercase-hex check: hex.EncodeToString returns lowercase, but
    // pin the property explicitly so a future swap to a different
    // encoder fails this test loudly.
    for _, r := range h1 {
        if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
            t.Errorf("HashToken contains non-lowercase-hex rune %q", r)
            break
        }
    }
}
```

Optionally pin one literal vector (`HashToken("abc") ==
"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"`)
as a regression guard against an accidental swap to SHA-1 or a
different encoding. The known-answer constant is the published
SHA-256("abc") test vector; embed it as a `const`.

### `TestVerifyToken`

Table-driven; covers the three AC bullets at the bottom (true path,
false path, empty/malformed):

| name | plain | hash | want |
|---|---|---|---|
| true path: matching token | `"abc"` | `HashToken("abc")` | `true` |
| false path: non-matching token | `"abc"` | `HashToken("xyz")` | `false` |
| false on empty hash | `"abc"` | `""` | `false` |
| false on too-short hash | `"abc"` | `"ba7816bf"` | `false` |
| false on too-long hash | `"abc"` | `HashToken("abc") + "00"` | `false` |
| false on non-hex hash | `"abc"` | `strings.Repeat("z", 64)` | `false` |
| false on uppercase hex hash | `"abc"` | `strings.ToUpper(HashToken("abc"))` | `false` |

The uppercase-hex row is intentional and documents the contract: the
on-disk hash is always lowercase (`HashToken`'s output), and verify
is byte-exact match. A caller storing uppercase by mistake gets
`false` and will discover the bug at first authentication; the
package does not silently normalise.

The non-hex row uses `"zzzz...zzzz"` (64 chars, no decode); it
proves `VerifyToken` doesn't accidentally hex-decode either side.

No tests for "concurrent verify under -race" — pure functions, no
shared state, race detector has nothing to find. No fuzz target —
the input space is fully covered by the table.

## Open questions

Resolved during refinement; recorded here so they're not relitigated:

- **Should `HashToken` accept `[]byte`?** Considered, rejected. AC
  fixes the signature as `HashToken(plain string) string`. The two
  call sites (mint, verify) both naturally hold strings (mint hex-encodes
  the rand bytes; verify reads from a JSON-decoded string field). A
  `[]byte` overload would tempt callers to write the plain to disk
  in a buffer that's harder to zero on drop.
- **Should the package zero `plain`'s memory after hashing?** Go's
  string immutability and GC make this both impossible (strings are
  not mutable) and pointless (the runtime doesn't expose a "secure
  erase" primitive). Rely on the protocol-level discipline ("plain
  appears once at pairing, once per WS connect") instead.
- **Should `Device` expose a `TokenHashPrefix() string` for `pair
  list` UI?** Out of scope; the sibling UI ticket adds it next to
  `Device` if/when needed. Display rule lives in `protocol-mobile.md:663`.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The package is a pure-function
  leaf with no I/O. The plain↔hash boundary is the function call
  itself: caller hands in `plain` (untrusted from the wire) and
  `hash` (trusted from disk); `VerifyToken` returns a bool that the
  caller turns into an auth decision. Downstream callers only ever
  hold the bool result and the original `*Device` they verified
  against — no transitive trust escalation.
- **[Tokens, secrets, credentials]** No findings.
  - Generation is out of scope (sibling minting ticket; the spec for
    that ticket must use `crypto/rand` for the 256-bit input, per
    `protocol-mobile.md:62`).
  - Storage: hash only, never plaintext on disk. The package doc
    comment names this contract.
  - Logging: neither `HashToken` nor `VerifyToken` logs. The package
    doc comment establishes the caller-side discipline ("MUST NOT
    log a plain device-token, MUST NOT wrap a plain token into error
    context"). Code-review must enforce this on any caller that
    accepts plain — no Pyrycode log line today contains a plain
    token, and this discipline keeps it that way.
  - Lifecycle: this ticket covers "verify-once-per-WS-connect"; rotation
    and revocation are sibling tickets (`pyry pair revoke <name>`),
    operationally implemented as removing the `Device` row from
    `devices.json`. The `Device` struct supports per-device
    revocation by design (each row is independent; one removal does
    not affect siblings). All-or-nothing revocation falls out of
    deleting `devices.json`.
- **[File operations]** N/A. Pure functions, no `os` package import,
  no path handling, no `os.Stat`, no `os.OpenFile`, no symlink
  follow. The sibling registry-CRUD ticket owns these concerns and
  must use the same atomic-rename + `0600` pattern as
  `internal/sessions/registry.go`'s `saveRegistryLocked`.
- **[Subprocess / external command]** N/A. No `os/exec` import.
- **[Cryptographic primitives]** No findings.
  - RNG: not used here; `HashToken` is deterministic. The minting
    ticket uses `crypto/rand`.
  - Hash: stdlib `crypto/sha256`. Fixed-size output, no truncation.
  - Constant-time compare: `crypto/subtle.ConstantTimeCompare`
    everywhere `plain`'s computed hash meets the stored hash. No
    `==`, no `bytes.Equal` on hash bytes, no `strings.EqualFold`.
    The unguarded length-mismatch path goes through
    `ConstantTimeCompare`'s documented constant-time return-0,
    not through an early `if len(hash) != 64`.
  - Key reuse: no keys.
  - No hand-rolled crypto.
- **[Network & I/O]** N/A. No network I/O, no `net` package, no
  `http`, no `io.Reader`/`io.Writer`, no Read size caps relevant.
- **[Error messages, logs, telemetry]** SHOULD FIX (informational,
  no spec change). Neither function returns an error or logs, so
  there's no leak path *in this package*. The package doc comment
  records the caller-side discipline, but enforcement is downstream
  (sibling minting ticket's logger calls; sibling auth-handler's
  error envelopes — `auth.invalid_token` per `protocol-mobile.md:98`
  is a generic code, deliberately non-revealing). Code-review must
  reject any future caller that logs the plain token or wraps it
  into a `fmt.Errorf("...: %w", err)` chain that contains it.
- **[Concurrency]** N/A. Pure functions, no shared state, no
  goroutines, no mutexes. `t.Parallel()` on every test row.
- **[Threat model alignment]** No findings.
  - `protocol-mobile.md:62` ("binary stores `sha256(token)` in
    `devices.json`, never the plaintext") — implemented by the
    `TokenHash` field shape and the hash-only API.
  - `protocol-mobile.md:97-98` (binary validates device-token on
    first frame) — `VerifyToken` is the primitive that future ticket
    will call. Returns bool, no info leak in the false case.
  - `protocol-mobile.md:663` (UI never displays plain after pairing)
    — supported structurally: the `Device` struct holds only the
    hash, so the UI literally cannot retrieve the plain.
  - Out of scope for this ticket and named so in `protocol-mobile.md`
    § "Out of scope (security)": E2E encryption (v2), permission
    scoping, supply-chain auditing, multi-tenant relay isolation,
    Sybil/abuse on a public relay. None affect this primitive.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-05-08
