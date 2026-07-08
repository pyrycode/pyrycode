package protocol

import (
	"encoding/json"
	"testing"
)

// TestSetSessionSettingsPayload_RoundTrip pins the presence contract of the
// set_session_settings request: a field explicitly set to its zero value
// (*"" for model/effort, *false for yolo) stays distinguishable from an
// omitted field (nil), and the distinction survives serialization. The two
// fixtures are the two halves of AC #5 — present-at-zero and fully-omitted —
// driven through one assertion structure keyed on wantNil.
func TestSetSessionSettingsPayload_RoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		fixture string
		wantNil bool // true ⇒ all three optional pointers nil (omitted); false ⇒ non-nil at zero value (present)
	}{
		{"present-at-zero", "set_session_settings_full.json", false},
		{"omitted", "set_session_settings_omitted.json", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := readFixture(t, tc.fixture)

			var env Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			if env.Type != TypeSetSessionSettings {
				t.Errorf("Type: got %q, want %q", env.Type, TypeSetSessionSettings)
			}

			var payload SetSessionSettingsPayload
			if err := json.Unmarshal(env.Payload, &payload); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			if payload.SessionID != "sess-a" {
				t.Errorf("SessionID: got %q, want %q", payload.SessionID, "sess-a")
			}

			if tc.wantNil {
				// Omitted: every optional field decodes to nil — an absent yolo
				// never reads as a sent false, an absent model never as a clear.
				if payload.Model != nil {
					t.Errorf("Model: got %q, want nil (omitted)", *payload.Model)
				}
				if payload.Effort != nil {
					t.Errorf("Effort: got %q, want nil (omitted)", *payload.Effort)
				}
				if payload.YOLO != nil {
					t.Errorf("YOLO: got %v, want nil (omitted)", *payload.YOLO)
				}
			} else {
				// Present at zero: non-nil pointers to "" / "" / false, each
				// distinct from omitted.
				if payload.Model == nil || *payload.Model != "" {
					t.Errorf("Model: got %v, want non-nil pointer to \"\"", payload.Model)
				}
				if payload.Effort == nil || *payload.Effort != "" {
					t.Errorf("Effort: got %v, want non-nil pointer to \"\"", payload.Effort)
				}
				if payload.YOLO == nil || *payload.YOLO {
					t.Errorf("YOLO: got %v, want non-nil pointer to false", payload.YOLO)
				}
			}

			// Byte-equal round-trip is the regression guard for the omitempty
			// decision: drop omitempty and the omitted fixture grows null keys;
			// swap the pointers for values and a sent zero can't survive.
			roundTripEnvelope(t, env, payload, raw)
		})
	}
}

// TestSessionSettingsUpdatedPayload_RoundTrip pins the reply's shape: it
// identifies the confirmed session and round-trips byte-for-byte.
func TestSessionSettingsUpdatedPayload_RoundTrip(t *testing.T) {
	t.Parallel()
	raw := readFixture(t, "session_settings_updated.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeSessionSettingsUpdated {
		t.Errorf("Type: got %q, want %q", env.Type, TypeSessionSettingsUpdated)
	}

	var payload SessionSettingsUpdatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.SessionID != "sess-a" {
		t.Errorf("SessionID: got %q, want %q", payload.SessionID, "sess-a")
	}

	roundTripEnvelope(t, env, payload, raw)
}
