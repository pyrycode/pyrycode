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

    // #702 — authorizes THIS device to mint a pairing for another device and
    // to actuate an MCP server. Since #2605 it no longer gates answering a
    // permission/trust/destructive modal or a question batch — every
    // authenticated device may (Device.MayAnswerPrompt). Name kept.
    AllowRemotePermissions bool `json:"allow_remote_permissions,omitempty"`

    // #1527 — deadline for an UNREDEEMED pairing record; enforced by
    // Registry.Validate since #1529 (see features/devices-registry.md).
    RedeemBy time.Time `json:"redeem_by,omitzero"`

    // #2734 — binds this pairing to one app install: lowercase hex of the
    // Noise_IK device-static public key the first accepted connection
    // presented. Empty = unbound. Written once by Registry.BindStaticKey;
    // Validate refuses a later connection whose key differs (see
    // features/devices-registry-validate.md and
    // features/devices-registry-redemption-and-binding.md).
    StaticKey string `json:"static_key,omitzero"`
}

const RedemptionWindow = 15 * time.Minute // #1527 — mint-time RedeemBy offset

func HashToken(plain string) string
func VerifyToken(plain, hash string) bool

// #702 — the privileged gate: minting a pairing for another device and MCP
// actuation ONLY, since #2605 (in auth.go, beside Validate).
func (d *Device) MayAnswerRemotePermission() bool
func AuthorizeRemotePermission(d *Device, outcome RemotePermissionOutcome) bool
type RemotePermissionOutcome int // OutcomeNoAnswer (zero) | OutcomeAllow | OutcomeDeny | OutcomeTimeout | OutcomeCancel

// #2605 — the answering gate: any authenticated device may answer a permission
// modal or a question batch. AllowRemotePermissions plays no part.
func (d *Device) MayAnswerPrompt() bool
func AuthorizePromptAnswer(d *Device, outcome RemotePermissionOutcome) bool
```

The crypto primitives (`HashToken` / `VerifyToken`) export no errors, no sentinels — `VerifyToken` returns bool by design. Auth-decision-as-error is the caller's concern, not the crypto primitive's.

JSON tags use snake_case. The four identity / lifecycle fields have no `omitempty` — required fields round-trip at their zero value. The optional fields (`Platform`, `PushToken`, added by #282; `AllowRemotePermissions`, added by #702) DO carry `omitempty` so a pre-existing `devices.json` round-trips through load → save without sprouting `"platform": ""` / `"push_token": ""` / `"allow_remote_permissions": false` entries; zero-migration change. Mirrors the `registryEntry` pattern in `internal/sessions/registry.go:17-29`, so the sibling registry CRUD marshals `Device` with stdlib `encoding/json` unchanged.

`RedeemBy` (#1527) is a `time.Time`, so its optional-field tag is `omitzero`, not `omitempty` — `encoding/json`'s `omitempty` only omits empty scalars/maps/slices, never a struct, so it would be a silent no-op on a timestamp and would start writing `"redeem_by":"0001-01-01T00:00:00Z"` into every legacy record on the next `Save`. `omitzero` (Go 1.26.2+) consults `time.Time.IsZero` and keeps the zero off disk, doing for a struct field what `omitempty` does for the three fields above. See [`codebase/1527.md`](../codebase/1527.md) for the full design and why the field is named `RedeemBy` rather than `ExpiresAt`.

`StaticKey` (#2734) is a plain string, so `omitzero` here just means "empty" — the same effect `omitempty` would give a string, used for consistency with `RedeemBy` next to it rather than out of necessity. A string rather than `[]byte` keeps `Device` comparable (used by the race test in [`features/devices-registry-redemption-and-binding.md`](devices-registry-redemption-and-binding.md) § `BindStaticKey`) and the on-disk form human-readable; it is a public key, not a secret, but nothing logs it either. The field binds a pairing to the one install that redeemed it: `pyrycode-mobile#1573` saw a phone and an emulator share one token and be treated as the same device, with the daemon's `UpdatePushRegistration` flipping `Name` and the push address between them on each reconnect. See [`features/devices-registry-redemption-and-binding.md`](devices-registry-redemption-and-binding.md) § `BindStaticKey` for how and when the field is written, and `docs/protocol-mobile.md` § "Static keys — mobile side" for the reversed design decision.

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

## Remote-permission gates (#702, narrowed by #2605)

Two pairs of pure predicates in `auth.go` share one enum. Which pair a caller uses depends on which of two very different questions it is asking:

- **The privileged gate — `MayAnswerRemotePermission` / `AuthorizeRemotePermission` (#702).** Gates minting a pairing for another device (`pairingMinterV2.MintPairing`) and actuating an MCP server (`mcpActuatorV2.actuate`) — the two verbs whose output multiplies a compromise (a stolen pairing must never be able to mint another, or reach a tool server, by itself). Reads `AllowRemotePermissions`, set **only** locally by `pyry pair --allow-remote-permissions` ([`pyry-pair-command.md`](pyry-pair-command.md)), never over the wire; default OFF.
- **The answering gate — `MayAnswerPrompt` / `AuthorizePromptAnswer` (#2605).** Gates answering a permission/trust/destructive modal (`modalResolverV2`) and answering or refusing a question batch (`questionResolverV2`). **Operator decision 2026-09-24: view-only clients are not a wanted use case.** Before #2605, every device paired without the flag — the default outcome of `pyry pair` and the *only* outcome of the mint path, since a minted device is always unprivileged — was stuck seeing prompts it could not answer, silently. `MayAnswerPrompt` drops `AllowRemotePermissions` from the check entirely: true for any authenticated (non-nil) device. The accepted cost is that a stolen *unprivileged* pairing can now approve a tool call; `pyry pair revoke` remains the remedy. See [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) § "Security model" (amended 2026-09-24) for the full tradeoff.

Both pairs are read off the already-authenticated `*Device` via `dispatch.Conn.Auth()`; neither is a wire capability. Everything else a paired phone does (watch the stream, snapshot, send, interrupt, dequeue) stays ungated regardless of which pair applies.

```go
// The privileged gate (#702). Fail-closed: nil receiver or bit OFF → false.
func (d *Device) MayAnswerRemotePermission() bool { return d != nil && d.AllowRemotePermissions }

// What the caller observed for a surfaced modal/question. The zero value
// (OutcomeNoAnswer) is the safe default and resolves to DENY, so a
// default-constructed call denies. Shared by both predicate pairs.
type RemotePermissionOutcome int
const (
    OutcomeNoAnswer RemotePermissionOutcome = iota // default → DENY
    OutcomeAllow                                    // phone explicitly chose an allow option
    OutcomeDeny
    OutcomeTimeout                                  // deny-on-timeout window elapsed
    OutcomeCancel                                   // phone cancelled / dismissed (ESC)
)

// The privileged decision (#702) — MintPairing, MCP actuation.
func AuthorizeRemotePermission(d *Device, outcome RemotePermissionOutcome) bool {
    return d.MayAnswerRemotePermission() && outcome == OutcomeAllow
}

// The answering gate (#2605). Fail-closed on nil (unauthenticated) only —
// AllowRemotePermissions plays no part.
func (d *Device) MayAnswerPrompt() bool { return d != nil }

// The answering decision (#2605) — modalResolverV2, questionResolverV2.
func AuthorizePromptAnswer(d *Device, outcome RemotePermissionOutcome) bool {
    return d.MayAnswerPrompt() && outcome == OutcomeAllow
}
```

Two design choices make the safe default **structural** rather than a convention a caller must remember, and both apply identically to each pair:

- **An enum, not a bare bool, for the outcome.** A bool would collapse no-answer / timeout / cancel into one indistinguishable input. The enum keeps the only ALLOW branch a single conjunction in one unit-tested place; any future outcome defaults to DENY unless explicitly mapped. This realizes the deny-on-timeout model in deterministic code (the safety-net fabric the security model needs).
- **Two predicates per pair.** The eligibility predicate (`MayAnswer*`) gates "may this device act at all" → reject an ineligible device with an *error envelope* before resolving anything. The decision predicate (`Authorize*`) resolves "given the outcome, did it grant." The decision re-checks eligibility internally, so it is correct standalone *and* composes — defense in depth, not a single bypassable point.

`MayAnswerRemotePermission` was **not renamed** when #2605 split off the answering predicate — a rename would have touched every privileged call site and e2e comment for no behaviour change. Its doc comment carries the new, narrower meaning instead; despite the name, it no longer has anything to do with answering.

All four predicates are **pure** — no logging, no I/O, no token handling — keeping each gate unit-testable in isolation and uncoupled from observability. Audit-writing on a decision is #712's primitive, called by the resolver, deliberately separate. See [`codebase/702.md`](../codebase/702.md) for the original data flow and producer obligations.

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
- ~~**Redemption-deadline enforcement.**~~ Delivered by #1527 (mint-time `RedeemBy` stamp) → #1528 (`ClearRedeemBy`, the relay's redemption persist) → #1529 (`Validate` rejects an unredeemed record past its deadline, and the v2 handshake gives that rejection the same wire shape as an unknown token). See [`features/devices-registry-validate.md`](devices-registry-validate.md).

## Related

- [`features/devices-registry.md`](devices-registry.md) — `~/.pyry/<name>/devices.json` on-disk persistence (#209) for the `Device` rows defined here.
- [`features/identity-package.md`](identity-package.md) — Phase 3 foundation sibling (`internal/identity`, `ServerID` for relay routing). Same "leaf package, stdlib only, no consumers wired in this slice" shape.
- [`features/config-package.md`](config-package.md) — Phase 3 foundation sibling (`internal/config`, `RelayURL`).
- `internal/sessions/registry.go:17-29` — JSON-tag style precedent that `Device` mirrors.
- `docs/protocol-mobile.md:62` — wire contract (binary stores SHA-256 hash, never plaintext).
- `docs/protocol-mobile.md:97-98` — runtime call site (phone presents on first WS frame).
- `docs/protocol-mobile.md:663` — UI rule (never display plain after pairing).
