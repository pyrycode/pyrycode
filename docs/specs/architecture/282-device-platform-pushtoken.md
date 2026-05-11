# Spec: devices — extend `Device` with `Platform` and `PushToken`

**Ticket:** #282
**Size:** XS
**Slice of:** Phase 3 Track C (mobile push). Schema half of #250.

## Files to read first

- `internal/devices/device.go:24-29` — current `Device` struct shape; the two new fields land here, after `LastSeenAt`.
- `internal/devices/registry.go:18-53` — `registryFile` envelope + `Load` semantics. Confirms that decoding via `encoding/json` already tolerates unknown / missing keys (no `DisallowUnknownFields`, no custom unmarshaler). The new fields will deserialise to `""` on pre-existing files without further changes.
- `internal/devices/registry.go:63-107` — `Save`'s atomic write path. **Unchanged by this slice** — confirm by reading; do not edit.
- `internal/devices/device_test.go` — pattern for table-driven tests in this package; the new round-trip tests live here (or in `registry_test.go` if a similar pattern already exists there; check first).
- `internal/protocol/push.go:14-18` — wire-side `RegisterPushTokenPayload` for the exact `Platform` doc-string contract (`"fcm"` / `"apns"`). The new doc comment on `Device.Platform` must mirror this language so the on-disk and wire contracts stay verbatim-aligned.
- `docs/protocol-mobile.md` § `register_push_token` (lines 480–498) — the protocol-level prose this slice ultimately serves. Read for context; no edits.
- Ticket #282 body, "Acceptance Criteria" section — three round-trip assertions are spelled out there; mirror them as test cases.

## Context

`internal/devices.Device` is the on-disk shape under `~/.pyry/<name>/devices.json`. Phase 3 needs the binary to persist per-device push-notification state so it can wake backgrounded phones via APNs/FCM (`docs/protocol-mobile.md` § Phone background behaviour). The wire-side carrier already exists at `internal/protocol.RegisterPushTokenPayload` (shipped via #275); this ticket is the on-disk counterpart.

Per the always-split rule for "registry schema change AND its consumers", #250 (the handler that writes these fields) is sliced off. This ticket lands the fields with `omitempty` so:

- Pre-existing `devices.json` files (no `platform` / `push_token` keys) load → save without sprouting `"platform": ""` / `"push_token": ""` entries.
- The pairing flow continues to create zero-valued `Device` entries; both new fields are absent from the serialised form for unregistered devices.
- #250 can land later as a pure write path with no schema migration.

## Design

### Struct change

In `internal/devices/device.go`, after the existing fields:

```go
type Device struct {
    TokenHash  string    `json:"token_hash"`
    Name       string    `json:"name"`
    PairedAt   time.Time `json:"paired_at"`
    LastSeenAt time.Time `json:"last_seen_at"`

    // Platform is the push notification platform for this device:
    // "fcm" (Android) or "apns" (iOS). Empty for devices that have
    // not registered a push token (e.g. CLI peers, or phones that
    // have not yet completed register_push_token). Matches the
    // contract on protocol.RegisterPushTokenPayload.Platform.
    Platform string `json:"platform,omitempty"`

    // PushToken is the opaque FCM / APNs device token used to wake
    // this device when it is offline. Empty for devices that have
    // not registered. Written by the register_push_token handler
    // (out of scope for this ticket); never marshalled across the
    // wire (the wire form is protocol.RegisterPushTokenPayload).
    PushToken string `json:"push_token,omitempty"`
}
```

**Field ordering rationale.** Append, do not reorder. `encoding/json` is order-insensitive on read, but `Save` re-encodes in struct order; placing the new fields last keeps the on-disk byte ordering for pre-existing key sets unchanged. Token-hash and identity fields stay grouped at the top; push-state fields cluster at the bottom.

**Why `omitempty`.** AC #2 requires that a pre-existing `devices.json` round-trip without gaining `"platform": ""` / `"push_token": ""` keys. Without `omitempty`, an empty string serialises as `"key": ""`. With `omitempty`, an empty string is suppressed — which is exactly the zero-value behaviour the round-trip test will assert. This matches the protocol-side decision to use `string` (not `*string`) for the wire payload; the on-disk side uses the same simple type with `omitempty` because the on-disk file is read by humans and adding empty keys to every device record would be noise.

**Why not pointers (`*string`).** Optional strings could be modelled as `*string` to distinguish "unset" from "empty". We don't need that distinction: the push registration path treats both as "no token registered", and a `nil`-vs-`""` distinction would force every reader to nil-check before use. `string + omitempty` is the idiomatic Go pattern for "absent or present" when the empty string is not itself a meaningful value, and it matches how every other optional string field in this codebase is modelled.

### No other code changes

- `registry.go` is untouched. The `Save` path serialises whatever fields `Device` exposes; the `Load` path tolerates unknown keys by default. Both behaviours fall out of `encoding/json` without intervention.
- `auth.go`, `auth_test.go`, `registry_test.go` are untouched. None of them assert on `Platform` / `PushToken`; adding zero-valued fields to a struct doesn't break existing assertions.
- The pairing flow (outside this package) continues to construct `Device{TokenHash: ..., Name: ..., PairedAt: ..., LastSeenAt: ...}`. Go zero-initialises the two new fields to `""`; with `omitempty` they don't reach the disk. No call-site updates required.

### Concurrency model

No change. `Registry` already serialises Load/Save/Add/Remove through `sync.Mutex`. The struct shape is value-type; widening it does not affect the locking discipline.

### Error handling

No new error paths. `omitempty` is a compile-time tag; it cannot fail at runtime. Decoding a missing key into a `string` field yields `""` (the zero value); decoding into a populated `string` field yields the string. Both are infallible.

## Testing strategy

Two new tests in `internal/devices/device_test.go` (or `registry_test.go` if that file already exercises a Marshal/Unmarshal round-trip — check existing patterns first; do not duplicate scaffolding).

**Test 1: legacy-shape round-trip.** Encode a `Device` with `Platform = ""` and `PushToken = ""` (the zero values produced by the pairing flow today). Marshal to JSON. Assert the serialised bytes contain **neither** the substring `"platform"` **nor** the substring `"push_token"`. Then unmarshal back into a fresh `Device` and assert all four pre-existing fields round-trip exactly.

```go
func TestDevice_LegacyOmitsPushFields(t *testing.T) {
    in := Device{
        TokenHash:  HashToken("abc"),
        Name:       "legacy-device",
        PairedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
        LastSeenAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
    }
    b, err := json.Marshal(in)
    if err != nil {
        t.Fatalf("Marshal: %v", err)
    }
    if bytes.Contains(b, []byte(`"platform"`)) {
        t.Errorf("encoded form leaked empty platform key: %s", b)
    }
    if bytes.Contains(b, []byte(`"push_token"`)) {
        t.Errorf("encoded form leaked empty push_token key: %s", b)
    }
    var out Device
    if err := json.Unmarshal(b, &out); err != nil {
        t.Fatalf("Unmarshal: %v", err)
    }
    if out != in {
        t.Errorf("round-trip mismatch: got %+v, want %+v", out, in)
    }
}
```

**Test 2: populated round-trip.** Encode a `Device` with both new fields set (`Platform = "apns"`, `PushToken = "f0r..."`); marshal, unmarshal, assert exact equality of all six fields. This covers AC #3.

```go
func TestDevice_PopulatedRoundTrip(t *testing.T) {
    in := Device{
        TokenHash:  HashToken("xyz"),
        Name:       "pixel-8",
        PairedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
        LastSeenAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
        Platform:   "apns",
        PushToken:  "f0r-test-fixture-not-a-real-token",
    }
    b, err := json.Marshal(in)
    if err != nil {
        t.Fatalf("Marshal: %v", err)
    }
    var out Device
    if err := json.Unmarshal(b, &out); err != nil {
        t.Fatalf("Unmarshal: %v", err)
    }
    if out != in {
        t.Errorf("round-trip mismatch: got %+v, want %+v", out, in)
    }
}
```

**Test 3 (optional but recommended): legacy-disk decode.** A literal JSON string representing a pre-#282 `devices.json` entry (no `platform`/`push_token` keys at all) decodes into a `Device` with both new fields equal to `""`. This is the "I have an on-disk file from before this change" case spelled out in AC #2.

```go
func TestDevice_DecodeLegacyDiskShape(t *testing.T) {
    legacy := []byte(`{
      "token_hash": "ba7816bf...",
      "name": "legacy",
      "paired_at": "2026-01-01T00:00:00Z",
      "last_seen_at": "2026-01-02T00:00:00Z"
    }`)
    var d Device
    if err := json.Unmarshal(legacy, &d); err != nil {
        t.Fatalf("Unmarshal: %v", err)
    }
    if d.Platform != "" {
        t.Errorf("Platform = %q, want \"\"", d.Platform)
    }
    if d.PushToken != "" {
        t.Errorf("PushToken = %q, want \"\"", d.PushToken)
    }
}
```

The literal `token_hash` in the fixture above is a placeholder — use any 64-char hex string the test pleases; this test asserts on the new fields, not on hash content.

**Imports.** The new tests need `encoding/json` and `bytes` (for `bytes.Contains`). `time` is already imported by the existing test file via the `Device` value path — re-import if `goimports` decides to remove it from prior usage (it shouldn't; existing tests use it transitively through `HashToken` fixtures only, so this spec's new tests will be the first direct `time` imports in the file).

**Existing tests.** `TestHashToken_Deterministic` and `TestVerifyToken` are unaffected (they never construct a full `Device`). `registry_test.go` and `auth_test.go` likewise — do not touch.

**Run command.** `go test -race ./internal/devices/...` should pass with both new tests added and no other changes.

## Open questions

None. The acceptance criteria are unambiguous; the design choices (`omitempty`, `string` not `*string`, append fields at end of struct) all have one obvious answer.

## Out of scope (do not do)

- Do **not** touch `internal/devices/registry.go`. The `Save`/`Load` paths require no changes.
- Do **not** touch the pairing flow, `auth.go`, or anything in `cmd/pyry/`. The zero-value semantics make those call sites correct as-is.
- Do **not** add a `register_push_token` handler — that is #250.
- Do **not** add validation that `Platform` is one of `{"fcm", "apns", ""}`. The on-disk struct is the persistence layer; validation belongs at the wire boundary (the future #250 handler) where invalid input can be rejected with `protocol.malformed`.
- Do **not** add a migration step or schema version bump. The whole point of `omitempty` here is to make this a zero-migration change.
- Do **not** rename existing fields or change their JSON tags.
