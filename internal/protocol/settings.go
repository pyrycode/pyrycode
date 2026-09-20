package protocol

import (
	"encoding/json"
	"fmt"
)

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
// PermissionMode names one of claude's permission modes (#1687), and it JOINS
// YOLO rather than superseding it — the mobile client speaks yolo and the wire
// types are not drifted without a matching change on every client. Removing yolo
// is a later ticket, filed when every client has moved.
//
// Two rules make this field unlike its three siblings, and both are enforced at
// the wire boundary (internal/relay's set_session_settings handler), not here —
// this package defines shape:
//
//   - Present-at-"" is NOT a settable value. For Model and Effort "" means "run
//     at claude's own default", a real value the presence contract can carry. The
//     default posture is a NAMEABLE mode ("default"), so an explicit "" names
//     nothing and is refused. The pointer's job here is purely absent-vs-present.
//   - A frame carrying BOTH this field and YOLO is refused as malformed. They are
//     two spellings of one posture — yolo:true is bypassPermissions, yolo:false is
//     default — so a frame carrying both is redundant or contradictory. Refusal
//     rather than precedence means there is no rule for a client author or a
//     reviewer to get wrong, and no client sends both.
//
// The accepted vocabulary is claude's five NON-ESCALATING modes (default,
// acceptEdits, plan, auto, dontAsk), measured live by #2041. bypassPermissions is
// refused on this field: the escalation stays reachable only through YOLO, so it
// keeps exactly one spelling on the wire. That refusal is a wire-vocabulary
// decision and not a statement about what the daemon can deliver — since #2066 it
// delivers the escalation in band too, spelled as the bit — so widening this field
// to six needs its own argument rather than following from the daemon's.
type SetSessionSettingsPayload struct {
	SessionID      string  `json:"session_id"`
	Model          *string `json:"model,omitempty"`
	Effort         *string `json:"effort,omitempty"`
	YOLO           *bool   `json:"yolo,omitempty"`
	PermissionMode *string `json:"permission_mode,omitempty"`
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

// RequestSessionSettingsPayload is the body of an Envelope whose Type ==
// TypeRequestSessionSettings (docs/protocol-mobile.md § Session settings).
// Phone → binary direction; a client asking what the current run configuration
// is and which session to address a change to.
//
// This is an inbound v2 *control* envelope, structurally like
// RequestSnapshotPayload (snapshot.go): the v2 session manager intercepts it at
// the dispatch boundary before dispatch.Route is called, so there is NO
// dispatch.Route handler for it.
//
// ConversationID names the conversation the client is asking about (#1586), and
// since #1610 it SELECTS the session the reply describes. Before it the frame was
// genuinely bare, so a client had no way to say which conversation it meant. It
// is untrusted network input used for exactly one thing — an in-memory resolution
// through the handler's conversation-keyed run-configuration seam — and never
// reaches a log line, an error string, a filesystem path, or the reply.
//
// NO omitempty, matching RequestSnapshotPayload and deliberately UNLIKE the
// sibling SetSessionSettingsPayload above, whose per-field pointers encode a
// presence contract. There is no presence contract here: absent and empty are
// the SAME case — "no conversation named", which names no session and so is
// answered with the zero-valued reply — so nothing needs to tell them apart.
// Keeping the field always on the wire lets a fixture pin the full shape.
//
// Scope: naming a conversation the daemon does not host, naming one bound to no
// known live or dormant session, or naming none at all are answered with a
// zero-valued SessionSettingsPayload, never an error frame and never another
// session's values. Naming a hosted, bound conversation reports THAT session's
// id, stored model and stored effort together (see SessionSettingsPayload below).
// Its permission pair comes only from the current child's last confirmation;
// when no confirmation or no current child exists, that pair remains zero while
// the resolved stored fields remain present.
type RequestSessionSettingsPayload struct {
	ConversationID string `json:"conversation_id"`
}

// SessionSettingsPayload is the body of an Envelope whose Type ==
// TypeSessionSettings (docs/protocol-mobile.md § Session settings). Binary →
// phone direction; the daemon's answer to a request_session_settings.
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
// The original fields have no omitempty, matching ScreenSnapshotPayload
// (snapshot.go) and deliberately UNLIKE the sibling SetSessionSettingsPayload
// above. They form a full report of saved state, so every original field is
// always on the wire and a zero value is a real answer, not an absence.
// EffectiveEffort is the one additive exception: its zero value is omitted when
// no applied reading is available. Specifically:
//
//   - SessionID "" means the daemon could not resolve a session to address.
//     A client MUST treat the settings as read-only rather than sending a
//     set_session_settings with an empty id, which would be rejected.
//   - Model / Effort "" mean "inherited default, no per-session override" —
//     the same meaning they carry on screen_snapshot.
//   - EffectiveEffort is independent from Effort: a string is a confirmed
//     applied level, null means claude reported no effort parameter, and an
//     omitted key means the applied value is unavailable or unsupported.
//   - YOLO false means the current child has not confirmed bypass. It is not, by
//     itself, proof that permissions are enforced: PermissionMode "" means no
//     current-child confirmation is available.
//   - PermissionMode "" can therefore appear beside a non-empty SessionID when a
//     live child has not confirmed a posture yet, or when the resolved session is
//     dormant and has no child. Model and Effort still report stored intent in
//     those cases.
//   - WindowTokens 0 means the daemon has no trustworthy window reading, and
//     covers two cases (#2100): the usage seam was not wired, and the used count
//     came out ABOVE the window the daemon believed, which disproves the belief.
//     Read it with UsedTokens to tell them apart — 0 is the unwired seam, a real
//     figure is the disproved window and is still the true context size. Either
//     way "X of Y" is unavailable. UsedTokens 0 against a NON-zero WindowTokens
//     is a genuine fresh session.
//
// PermissionMode and YOLO always agree because the producer derives both from
// the same current-child confirmation: confirmed bypass reports
// "bypassPermissions" AND yolo true, while an unavailable confirmation reports
// "" AND false. This half can report a posture the WRITE half refuses to accept
// on its permission_mode field — deliberate, and the whole point of the read
// half existing. A client labels its menu from this value and keeps sending yolo
// to change the bypass posture.
//
// Scope: the values are those of the session bound to the conversation the
// request named (RequestSessionSettingsPayload above, #1586/#1610), and SessionID
// names that same session, so a client reads and writes the same place. The whole
// set is keyed by conversation and resolved as ONE value, so no field can describe
// a session another field does not — which is why a client can never read one
// session's values and write its change to another. A request that resolves to no
// session gets every original field at its zero value and omits EffectiveEffort,
// never reporting some other session's values.
type SessionSettingsPayload struct {
	SessionID       string         `json:"session_id"`
	Model           string         `json:"model"`
	Effort          string         `json:"effort"`
	EffectiveEffort NullableString `json:"effective_effort,omitzero"`
	YOLO            bool           `json:"yolo"`
	PermissionMode  string         `json:"permission_mode"`
	UsedTokens      int            `json:"used_tokens"`
	WindowTokens    int            `json:"window_tokens"`
}

// NullableString preserves an optional nullable JSON string's three states.
// Its zero value is unavailable and can be omitted with the omitzero tag;
// NewNullableString(nil) is present JSON null, and a non-nil value is a present
// JSON string. The type stays comparable so payloads containing it remain
// comparable.
type NullableString struct {
	value   *string
	present bool
}

// NewNullableString returns a present nullable string. A nil value represents
// explicit JSON null rather than omission.
func NewNullableString(value *string) NullableString {
	return NullableString{value: value, present: true}
}

// Value returns the nullable string and whether its key was present. A present
// nil value represents explicit JSON null.
func (s NullableString) Value() (*string, bool) {
	return s.value, s.present
}

// IsZero reports whether the nullable string is unavailable and should be
// omitted by encoding/json's omitzero handling.
func (s NullableString) IsZero() bool {
	return !s.present
}

// MarshalJSON preserves a present string or null. The containing field's
// omitzero tag handles the unavailable state before this method is called.
func (s NullableString) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.value)
}

// UnmarshalJSON records that the key was present and accepts only a JSON string
// or null.
func (s *NullableString) UnmarshalJSON(data []byte) error {
	var value *string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("decode nullable string: %w", err)
	}
	s.value = value
	s.present = true
	return nil
}
