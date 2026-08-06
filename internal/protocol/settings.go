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
// sessions.SessionID, …). A client learns it from SessionSettingsPayload below,
// the request/response route it can drive at any time, or observes a change to
// it on the unsolicited session_transition marker. The marker ALONE is not a
// sufficient source and this comment used to imply it was: the daemon fires it
// only on a clear or an idle eviction, never on session creation, so a client
// that had done neither never learned an id and could not write. A plain string,
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

// SessionSettingsPayload is the body of an Envelope whose Type ==
// TypeSessionSettings (docs/protocol-mobile.md § Session settings). Binary →
// phone direction; the daemon's answer to a bare request_session_settings.
//
// It is the READ half the #844 cluster never had. A client needs two things to
// drive the run-configuration UI: the current values, and the SessionID to
// address a set_session_settings to. Before this payload the only source of the
// values was screen_snapshot's side-load, and the only source of the id was the
// unsolicited session_transition marker — which the daemon fires ONLY on a clear
// or an idle eviction, never on session creation, so a client that had done
// neither never learned an id at all and could not write. That is the defect
// this payload closes.
//
// NO omitempty on any field, matching ScreenSnapshotPayload (snapshot.go) and
// deliberately UNLIKE the sibling SetSessionSettingsPayload above, whose
// per-field pointers encode a presence contract. Nothing here is optional: this
// is a full report of current state, so every field is always on the wire and a
// zero value is a real answer, not an absence. Specifically:
//
//   - SessionID "" means the daemon could not resolve a session to address.
//     A client MUST treat the settings as read-only rather than sending a
//     set_session_settings with an empty id, which would be rejected.
//   - Model / Effort "" mean "inherited default, no per-session override" —
//     the same meaning they carry on screen_snapshot.
//   - YOLO false means permissions are enforced.
//   - WindowTokens 0 means the usage seam was not wired; UsedTokens 0 against a
//     non-zero WindowTokens is a genuine fresh session.
//
// Scope: the values are the BOOTSTRAP session's, not the requesting
// conversation's, per #848's explicit "do not pre-carve a conversation-keyed
// settings seam". SessionID reports that same bootstrap session, so a client
// reads and writes the same place. Keying the whole set by conversation is a
// deferred follow-up; when it lands, both the values and SessionID move together
// or the read and the write would address different sessions.
type SessionSettingsPayload struct {
	SessionID    string `json:"session_id"`
	Model        string `json:"model"`
	Effort       string `json:"effort"`
	YOLO         bool   `json:"yolo"`
	UsedTokens   int    `json:"used_tokens"`
	WindowTokens int    `json:"window_tokens"`
}
