# `internal/devices` — paired-device type + token hashing

The on-disk shape for one paired mobile device, plus the two pure functions that hash and verify the device-token. Phase 3 (mobile + relay) foundation; no consumers wired in this slice — registry CRUD and token minting are sibling tickets.

Stdlib only (`crypto/sha256`, `crypto/subtle`, `encoding/hex`, `time`). No I/O, no goroutines, no logger, no `Config`, no `context.Context`. Pure functions; concurrent callers are safe by construction.

## Surface

```go
type Device struct {
    TokenHash  string    `json:"token_hash"`
    Name       string    `json:"name"`
    PairedAt   time.Time `json:"paired_at"`
    LastSeenAt time.Time `json:"last_seen_at"`

    Platform  string `json:"platform,omitempty"`   // "fcm" | "apns" | ""
    PushToken string `json:"push_token,omitempty"` // opaque APNs/FCM token
}

func HashToken(plain string) string
func VerifyToken(plain, hash string) bool
```

Three exports. No exported errors, no sentinels — `VerifyToken` returns bool by design. Auth-decision-as-error is the caller's concern, not the crypto primitive's.

JSON tags use snake_case. The four identity / lifecycle fields have no `omitempty` — required fields round-trip at their zero value. The two push-registration fields (`Platform`, `PushToken`, added by #282) DO carry `omitempty` so a pre-#282 `devices.json` round-trips through load → save without sprouting `"platform": ""` / `"push_token": ""` entries; zero-migration change. Mirrors the `registryEntry` pattern in `internal/sessions/registry.go:17-29`, so the sibling registry CRUD marshals `Device` with stdlib `encoding/json` unchanged.

`Platform`'s doc comment mirrors `protocol.RegisterPushTokenPayload.Platform` verbatim (`"fcm"` Android, `"apns"` iOS) so the on-disk and wire contracts stay aligned. `PushToken` is the opaque platform-supplied wake token; written by the future `register_push_token` handler (#250), never marshalled across the wire (the wire form is `protocol.RegisterPushTokenPayload` from #275).

## Wire contract

`docs/protocol-mobile.md:62` pins it: device-token is "256-bit random, hex-encoded ... binary stores `sha256(token)` in `devices.json`, never the plaintext." `protocol-mobile.md:97-98` is the future call site — phone presents the token on first WS frame; binary computes `sha256(presented)` and constant-time compares against each stored hash. `protocol-mobile.md:663` is the UI rule: "MUST never display the device-token in plaintext after initial pairing."

## `HashToken` — generation

```go
func HashToken(plain string) string {
    sum := sha256.Sum256([]byte(plain))
    return hex.EncodeToString(sum[:])
}
```

Two stdlib calls. Output is always 64 lowercase hex chars (`sha256.Size * 2`). Deterministic — same input always produces same output.

## `VerifyToken` — comparison

```go
func VerifyToken(plain, hash string) bool {
    expected := HashToken(plain)
    return subtle.ConstantTimeCompare([]byte(expected), []byte(hash)) == 1
}
```

`subtle.ConstantTimeCompare` returns 0 (false) when slice lengths differ, in constant time relative to the slice arguments. Empty `hash`, malformed `hash`, or any-length-≠-64 `hash` all fall out via the length-mismatch path. There is intentionally **no early-return guard on `hash == ""`** — the unguarded shape is shorter, makes the constant-time discipline auditable in one line, and the AC bullet "false on empty/malformed hash" is satisfied by `ConstantTimeCompare`'s documented semantics.

`==`, `bytes.Equal`, and `strings.EqualFold` are forbidden on hash material. Code review enforces this.

## Why no bcrypt or salt

Recorded so the next reviewer doesn't relitigate it.

The device-token is 256 bits of `crypto/rand` output (sibling minting ticket). Brute force across 2^256 candidates is infeasible regardless of hash speed; the hash exists only to prevent **plaintext at rest** — if `devices.json` leaks, the attacker holds hashes, not tokens. For that threat model:

- **Bcrypt** is designed for low-entropy human passwords. Slowing the attacker by a constant factor matters when the keyspace is ~50 bits; it's irrelevant at 256 bits. Bcrypt also caps input at 72 bytes; a 64-char hex token fits today, but the cap is a footgun for any future format change. Rejected.
- **Per-token salt** defends against precomputation (rainbow tables) on shared-keyspace inputs (e.g. common passwords). 256-bit random inputs share no keyspace with any other deployment; precomputation is meaningless. A salt would add complexity (column on disk, salt retrieval before verify) for no defensive gain. Rejected.

The protocol spec already commits the binary to plain SHA-256 (`protocol-mobile.md:62`), so this aligns with the documented contract.

## Determinism is intentional

Same plain produces the same hash across runs, machines, processes — what makes verify trivial (compute once, compare). The cost is "two binaries with the same plain token would store identical hashes" — irrelevant because each binary mints its own tokens for its own paired devices; tokens don't cross binaries.

## Caller-side discipline (SECURITY)

The package doc comment names this contract. Future callers MUST:

- **Never log a plain token.** No `slog` field, no `fmt.Printf`, no error message containing the plain.
- **Never wrap a plain token into error context.** A `fmt.Errorf("...%s...: %w", plainToken, err)` chain is a leak.
- **Never pass a plain token across log/slog fields.**

The plain token appears at exactly two sites: pairing (QR + paste-fallback string) and per-WS-connect (phone presents). Outside those, the only on-disk and in-memory representation is the hash. Code review enforces this — the package itself returns no error and logs nothing, so leaks can only originate in callers.

## Tests

`internal/devices/device_test.go`, same-package, table-driven, `t.Parallel()` everywhere.

- `TestHashToken_Deterministic` — same input → same output, length 64, all lowercase hex. Pins `HashToken("abc") == ba7816bf...015ad` (published SHA-256("abc") test vector) as a regression guard against accidental swap to SHA-1 or a different encoding.
- `TestVerifyToken` — table covers AC's four bullets plus three malformed-hash rows: matching token (true), non-matching token (false), empty hash, too-short hash, too-long hash, non-hex hash (`"zzz...zzz"`, 64 chars, proves no accidental hex-decode), uppercase hex hash (documents that on-disk is canonical lowercase; the package does not silently normalise).

No fuzz target — the input space is fully covered by the table. No `-race` test — pure functions, no shared state.

## Out of scope (deferred)

- **Token minting.** Sibling ticket: `crypto/rand`-driven 256-bit token + hex encode + display in QR + paste-fallback string. The package here knows nothing about generation.
- ~~**Registry CRUD.**~~ Delivered by #209 — see [`features/devices-registry.md`](devices-registry.md). Same atomic-rename + `0600` recipe as `saveRegistryLocked`, with a snapshot-then-release Save discipline ([ADR 020](../decisions/020-devices-registry-snapshot-then-write.md)).
- **Auth wiring.** Phase 3: the WS-handshake auth predicate `(*Registry).Validate(plain) (Device, bool)` is delivered by #210 — see [`features/devices-registry.md`](devices-registry.md). The WS handler that calls it (returning `auth.invalid_token` per `protocol-mobile.md:97-98` on a miss, advancing `LastSeenAt` durability via scheduled `Save` on a hit) is a follow-up Phase-3 ticket. `VerifyToken` is intentionally NOT used by `Validate` — see the registry doc and #210's "Why not iterate `VerifyToken` over all devices?" for the reasoning.
- **`pyry pair revoke <name>`.** Per-device revocation falls out of removing the row; structurally supported (each row is independent).
- **`Device.TokenHashPrefix() string` for `pair list` UI.** The display rule lives in `protocol-mobile.md:663`; defer to whichever ticket builds the UI.

## Related

- [`features/devices-registry.md`](devices-registry.md) — `~/.pyry/<name>/devices.json` on-disk persistence (#209) for the `Device` rows defined here.
- [`features/identity-package.md`](identity-package.md) — Phase 3 foundation sibling (`internal/identity`, `ServerID` for relay routing). Same "leaf package, stdlib only, no consumers wired in this slice" shape.
- [`features/config-package.md`](config-package.md) — Phase 3 foundation sibling (`internal/config`, `RelayURL`).
- `internal/sessions/registry.go:17-29` — JSON-tag style precedent that `Device` mirrors.
- `docs/protocol-mobile.md:62` — wire contract (binary stores SHA-256 hash, never plaintext).
- `docs/protocol-mobile.md:97-98` — runtime call site (phone presents on first WS frame).
- `docs/protocol-mobile.md:663` — UI rule (never display plain after pairing).
