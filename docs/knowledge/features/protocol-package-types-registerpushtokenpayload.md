# `RegisterPushTokenPayload` (#275)

Body of a `register_push_token` frame (`docs/protocol-mobile.md` § Message types → `register_push_token`). Phone → binary, sent on every WS connect; `RegisterPushToken` (`internal/relay/handlers`) persists `(platform, token, device_name)` to `devices.json` and de-duplicates against the stored triple.

```go
type RegisterPushTokenPayload struct {
    Platform   string `json:"platform"`
    Token      string `json:"token"`
    DeviceName string `json:"device_name"`
}
```

- `Platform` is one of `"fcm"` (Android) or `"apns"` (iOS). Stays `string`, not an enum — an enum would force a converter at every internal call site for no observable wire-format gain.
- All three fields are required (no `omitempty`, no pointers). Encode-side absence surfaces as zero-value `""` on the wire.
- Pure DTO: no methods, no constructors, no `Validate()`. Validation is the handler's job, not this type's — `RegisterPushTokenPayload` stays undecorated by design, matching `RenameWorkspacePayload`'s stated package rule that a decode-time check is warranted only for a field with no downstream validator (this one has one: its handler). `DeviceName` and `Platform` are checked for byte length and display-safety in `RegisterPushToken` itself before either reaches `devices.json`, a log line, or an audit record — see [`relay-package-handlers.md` § Display-safety gate](relay-package-handlers.md#display-safety-gate-on-device_name-and-platform-2219) (#2219). `DeviceName`'s bound is `protocol.MaxDeviceNameBytes`, the same constant `MintPairingPayload` publishes for the same label — a reader of this file alone cannot see that bound on the type itself, unlike `MaxWorkspaceLabelBytes`, which sits beside the payload it bounds. Logging `Payload` is forbidden (may contain tokens) per the security posture below.

Golden round-trip test in `push_test.go` decodes the spec example through `Envelope` → `Envelope.Payload` → `RegisterPushTokenPayload` and re-marshals byte-equivalently against `testdata/register_push_token.json`. The decode-from-`Envelope.Payload` path (not decode-from-raw-payload-bytes) exercises the exact composition the dispatcher will use.

This is the first slice of the #256 per-type payload catalog. Sibling slices for the remaining 15 v1 type discriminators land in their own tickets and own `*.go` files.
