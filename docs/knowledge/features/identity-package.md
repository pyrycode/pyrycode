# `internal/identity` — typed routing identifiers

Home for typed identifiers that span subsystems. Today: `ServerID`, the public routing identifier for one pyrycode-binary instance — surfaced in QR pairing payloads and the relay handshake's `x-pyrycode-server` upgrade header. Future: potential `DeviceID`, `PairedDeviceID`.

Pure types and validation. No I/O, no state, no goroutines. Foundation slice for Phase 3 (mobile + relay) work; no consumers wired yet.

## Surface

```go
type ServerID string

func NewServerID() ServerID
func ParseServerID(s string) (ServerID, error)

var ErrInvalidServerID = errors.New("identity: invalid server id")
```

Three exports. Construct `ServerID` only via `NewServerID` or `ParseServerID` — direct `ServerID(rawString)` outside the package is a review-enforced anti-pattern (Go's type system can't prevent it; the `internal/` boundary contains the exposure to the pyrycode module itself).

The empty `ServerID ("")` is the unset sentinel; never a valid generated id.

## Canonical form

UUIDv4, lowercase hex, 36 chars, dashes at positions 8/13/18/23, version-4 nibble (`4`) at position 14, RFC 4122 variant nibble (`8`/`9`/`a`/`b`) at position 19.

```
550e8400-e29b-41d4-a716-446655440000
        ^    ^    ^    ^
        8    13   18   23   ← dash positions
                  ^
                  position 14 must be '4' (version)
                       ^
                       position 19 must be 8/9/a/b (variant)
```

`protocol-mobile.md` pins this as the wire shape: server-id is "UUIDv4 (canonical hex form)" minted by the binary on first run, surfaced in QR codes and unencrypted on WS upgrade. ~122 bits of entropy; unguessability is the security model.

## `NewServerID` — generation

Reads 16 bytes from `crypto/rand`, sets the version (`0x40`) and variant (`0x80`) nibbles, formats as canonical UUIDv4. Returns `ServerID` directly — no error.

`crypto/rand.Read` is documented as infallible on supported platforms (Go 1.24+). If the system rng is unavailable we panic — silently degrading to a zero-entropy id would break the unguessability the relay-routing security model depends on. **Never** fall back to `math/rand`.

## `ParseServerID` — validation

Returns `(ServerID, error)`. Validates canonical UUIDv4 shape (36 chars, lowercase hex, dashes at fixed positions, version + variant nibbles). Rejects:

- empty string
- wrong length (35 or 37 chars)
- uppercase hex (`550E8400-...`)
- wrong version nibble (`-11d4-` v1, `-21d4-` v2, ...)
- wrong variant nibble (`-7716-`, `-c716-`, ...)
- non-hex characters
- missing or misplaced dashes

Returns `ErrInvalidServerID` on any failure — caller-supplied input is **not** embedded in the error message (avoids needless log-injection vector for relay-supplied input). Callers that need richer context wrap themselves via `fmt.Errorf`. Use `errors.Is(err, ErrInvalidServerID)` for branching.

Use this at every wire/disk boundary that accepts an externally-supplied server-id (persistence load, pairing payload unmarshal, relay handshake).

## Three deliberate divergences from `sessions.NewID` / `sessions.ValidID`

The spec mirrors the sessions-id pattern almost verbatim. Three divergences are forced by the AC:

1. **`NewServerID` returns `ServerID` (no error).** AC #2. The defensive panic on `rand.Read` failure is a runtime-abort, not a returned error. Document with one short comment.
2. **`ParseServerID` returns `(ServerID, error)`, not a bool.** AC #3 specifies parser semantics. Validation logic is identical to `sessions.ValidID`; the wrapper differs.
3. **No code reuse between packages.** `internal/identity` does NOT import `internal/sessions`. The dependency direction is wrong: sessions are an implementation detail that should be free to import identity later, not the reverse. The duplication is six lines of obvious switch/case — accepted intentionally.

## Why a separate package from `internal/sessions`

`sessions.ServerID` would suggest a per-session id, which is wrong — there is one server-id per binary instance, independent of session lifecycle. The split also keeps `internal/sessions` focused on the supervised-claude lifecycle and frees `internal/identity` to grow with future identifier types (device-id, paired-device-id) without bloating the sessions package.

## Concurrency

Stateless. All three exported names are safe for concurrent use by definition (they own no shared state). `crypto/rand.Read` is goroutine-safe per its package docs.

## Tests

`internal/identity/server_id_test.go`, same-package, table-driven, `t.Parallel()` everywhere. Four tests:

- `TestNewServerID_Format` — generate one id; assert `len == 36` and matches the canonical regexp `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$` (tighter than `sessions/id_test.go`'s pattern because `ParseServerID` enforces version + variant).
- `TestNewServerID_Unique` — 1000 iterations, no duplicates. Catches a constant-zero rng wiring bug.
- `TestParseServerID` — table covering valid (variants 8/9/a/b), empty, wrong length, uppercase, wrong version, wrong variant, non-hex, missing dash, dash at wrong position. Negative assertions use `errors.Is(err, ErrInvalidServerID)` to verify the sentinel is reachable.
- `TestNewServerID_RoundTripsParseServerID` — generate → parse → equal. Direct expression of AC #4.

## Out of scope (deferred to follow-up tickets)

- **Persistence** — sibling ticket loads/writes the raw string from `~/.pyry/<name>/server-id` (or similar) and feeds it through `ParseServerID` on read.
- **JSON round-trip tests** — `encoding/json` on a string newtype is library behavior; covered by the persistence ticket's disk-format test.
- **Human label suffix** — `protocol-mobile.md` notes a "may have a human label suffix in the QR for UX" possibility. That's a QR-encoding concern, not an id-type concern; defer to whichever Phase 3 ticket builds the QR.
- **CLI surface** (`pyry server-id` to print the value) — defer.
- **Pairing payload / relay handshake wiring** — Phase 3 tickets.

## Related

- `internal/sessions/id.go` — the precedent (UUIDv4-shaped string newtype, `crypto/rand` generation, canonical-shape validator). Mirrored almost verbatim with three AC-forced divergences above.
- `docs/protocol-mobile.md:61` — wire contract: server-id shape and routing role.
- `docs/protocol-mobile.md:575-583` — security framing: ~122 bits of entropy, `crypto/rand` is not optional.
- [`features/config-package.md`](config-package.md) — Phase 3 sibling foundation slice (`internal/config`, also no consumers wired in its own slice).
