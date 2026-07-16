package protocol

// Set-session-settings v2 wire payloads (#844, docs/protocol-mobile.md
// § Session settings). These carry a client's request to change one session's
// per-session model / reasoning effort / YOLO and the daemon's confirmation.
// Wire vocabulary only: pure structs and their (de)serialization. The handler
// that intercepts set_session_settings, gates on the interactive capability,
// validates, persists via sessions.Pool.UpdateSettings (#840), and emits
// session_settings_updated is sibling #845 — NOT here.

// SetSessionSettingsPayload is the body of an Envelope whose Type ==
// TypeSetSessionSettings (docs/protocol-mobile.md § Session settings). Phone →
// binary direction; an inbound v2 control envelope.
//
// The three settings fields are pointers as the *presence contract*: nil means
// "leave unchanged"; a non-nil pointer means "set to this value" — including a
// non-nil *"" (Model/Effort) or *false (YOLO), which are thereby distinguishable
// from omitted. This mirrors sessions.SettingsUpdate{Model, Effort *string; YOLO
// *bool} (#840) that the #845 handler decodes this payload into 1:1, so a nil
// field flows straight through to "leave the stored value untouched".
//
// The omitempty on the three pointer fields is load-bearing and deliberately
// UNLIKE the sibling *string-without-omitempty payload (SessionTransitionPayload)
// whose spec wire shows a literal null sentinel. Here
// an unset field is *absent* from the wire (the minimal shape a client changing
// one setting naturally produces), not null. Go's omitempty treats a non-nil
// pointer as non-empty regardless of the pointee, so a non-nil *false / *""
// still marshals its key — an omitted YOLO can never masquerade as a sent false,
// and an omitted Model can never masquerade as an instruction to clear a stored
// value (AC #2). Do NOT "fix" this by copying the sibling payloads' no-omitempty
// style.
//
// SessionID is the addressing key — it matches sessions.Pool.UpdateSettings(id
// sessions.SessionID, …) and the new_session_id already carried to clients in
// the session_transition marker, so a client already knows it. A plain string,
// always required, no omitempty.
type SetSessionSettingsPayload struct {
	SessionID string  `json:"session_id"`
	Model     *string `json:"model,omitempty"`
	Effort    *string `json:"effort,omitempty"`
	YOLO      *bool   `json:"yolo,omitempty"`
}

// SessionSettingsUpdatedPayload is the body of an Envelope whose Type ==
// TypeSessionSettingsUpdated (docs/protocol-mobile.md § Session settings).
// Binary → phone direction; the daemon's confirmation that a
// set_session_settings was applied.
//
// It identifies only the session the change was applied to; the request↔reply
// correlation rides Envelope.InReplyTo at the handler layer (#845), and the
// client already knows what it sent, so the reply does not echo the settings.
// SessionID is a plain string, always present, no omitempty.
type SessionSettingsUpdatedPayload struct {
	SessionID string `json:"session_id"`
}
