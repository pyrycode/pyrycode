package protocol

// RegisterPushTokenPayload is the body of a register_push_token frame
// (docs/protocol-mobile.md § register_push_token). Phone → binary, sent
// on every WS connect; the binary persists (platform, token, device_name)
// in devices.json and de-duplicates against the stored triple.
//
// Platform is "fcm" (Android) or "apns" (iOS). The wire type stays a
// plain string: an enum would force a converter at every internal call
// site for no observable wire-format gain, and per-spec the dispatcher
// is the validation point.
//
// All three fields are required (no omitempty, no pointers).
type RegisterPushTokenPayload struct {
	Platform   string `json:"platform"`
	Token      string `json:"token"`
	DeviceName string `json:"device_name"`
}
