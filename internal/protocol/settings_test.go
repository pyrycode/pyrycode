package protocol

import (
	"encoding/json"
	"strings"
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

			// Both fixtures omit permission_mode (#1687), so both pin the ABSENT
			// half of its presence contract — including the present-at-zero row,
			// which proves the new field does not have to ride along with the
			// three that were on the wire before it. There is no
			// "present at zero" row for it: unlike model/effort/yolo, an explicit
			// "" names no posture and is refused at the wire boundary, so the
			// PRESENT half is pinned by set_session_settings_mode.json below.
			if payload.PermissionMode != nil {
				t.Errorf("PermissionMode: got %q, want nil (omitted)", *payload.PermissionMode)
			}

			// Byte-equal round-trip is the regression guard for the omitempty
			// decision: drop omitempty and the omitted fixture grows null keys;
			// swap the pointers for values and a sent zero can't survive.
			roundTripEnvelope(t, env, payload, raw)
		})
	}
}

// TestSetSessionSettingsPayload_PermissionModeRoundTrip pins the PRESENT half of
// the permission mode's presence contract (#1687), the half the two fixtures
// above cannot carry. A frame naming only a mode decodes to a non-nil pointer
// with the three older fields still nil, and re-marshals byte-for-byte — so a
// client changing only the posture sends exactly one settings key, and the mode
// key is emitted only when it was sent.
//
// The fixture names "plan" rather than "default" deliberately: "default" is also
// what canonicalisation lands on downstream, so a bug that dropped the value
// while keeping the pointer could still read as correct against it.
//
// It does NOT pin that yolo is absent as a validity rule — that a frame carrying
// both is refused is a WIRE-BOUNDARY decision, tested where the boundary lives
// (internal/relay's set_session_settings handler). This package defines shape.
func TestSetSessionSettingsPayload_PermissionModeRoundTrip(t *testing.T) {
	t.Parallel()
	raw := readFixture(t, "set_session_settings_mode.json")

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
	if payload.PermissionMode == nil || *payload.PermissionMode != "plan" {
		t.Fatalf("PermissionMode: got %v, want non-nil pointer to %q", payload.PermissionMode, "plan")
	}
	if payload.Model != nil || payload.Effort != nil || payload.YOLO != nil {
		t.Errorf("Model/Effort/YOLO: got %v/%v/%v, want all nil (a mode-only frame changes nothing else)",
			payload.Model, payload.Effort, payload.YOLO)
	}

	roundTripEnvelope(t, env, payload, raw)
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

// TestRequestSessionSettingsPayload_RoundTrip pins the READ request's shape:
// it carries the conversation_id the client is asking about (#1586), and
// round-trips byte-for-byte. It does NOT pin the no-omitempty decision, despite
// being byte-equal: this fixture's id is non-empty, so the key survives omission
// and this test stays green with the tag added (measured under go test
// -overlay). TestRequestSessionSettingsPayload_EmptyConversationID below is the
// sole pin — do not prune it as redundant with this one.
func TestRequestSessionSettingsPayload_RoundTrip(t *testing.T) {
	t.Parallel()
	raw := readFixture(t, "request_session_settings.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeRequestSessionSettings {
		t.Errorf("Type: got %q, want %q", env.Type, TypeRequestSessionSettings)
	}

	var payload RequestSessionSettingsPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ConversationID != "c1" {
		t.Errorf("ConversationID: got %q, want %q", payload.ConversationID, "c1")
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestRequestSessionSettingsPayload_EmptyConversationID pins the empty-id
// boundary and is the SOLE pin on the no-omitempty decision: with no omitempty
// the key stays explicitly on the wire and round-trips back to the empty string,
// and adding the tag reddens this test and only this test (measured under go
// test -overlay; the byte-equal round trip above stays green because its fixture
// id is non-empty). Empty and absent are the same case here
// — "no conversation named", which the daemon answers exactly as it always has
// — so this pins the shape, not a presence contract. Mirrors
// TestSnapshotPayloads_EmptyConversationID.
func TestRequestSessionSettingsPayload_EmptyConversationID(t *testing.T) {
	t.Parallel()
	out, err := json.Marshal(RequestSessionSettingsPayload{ConversationID: ""})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"conversation_id":""`) {
		t.Errorf("empty conversation_id should stay on the wire; got %s", out)
	}
	var back RequestSessionSettingsPayload
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ConversationID != "" {
		t.Errorf("ConversationID round-trip: got %q, want empty", back.ConversationID)
	}
}

// TestSessionSettingsPayload_RoundTrip pins the READ reply's shape: it carries
// the addressing id plus all four reported values plus the two usage integers,
// and round-trips byte-for-byte. This is the payload that replaces the
// screen_snapshot side-load, so its field set must cover everything the run
// configuration UI reads.
func TestSessionSettingsPayload_RoundTrip(t *testing.T) {
	t.Parallel()
	raw := readFixture(t, "session_settings.json")

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != TypeSessionSettings {
		t.Errorf("Type: got %q, want %q", env.Type, TypeSessionSettings)
	}

	var payload SessionSettingsPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.SessionID != "sess-a" {
		t.Errorf("SessionID: got %q, want %q", payload.SessionID, "sess-a")
	}
	if payload.Model != "opus" {
		t.Errorf("Model: got %q, want %q", payload.Model, "opus")
	}
	if payload.Effort != "high" {
		t.Errorf("Effort: got %q, want %q", payload.Effort, "high")
	}
	if payload.YOLO {
		t.Error("YOLO: got true, want false")
	}
	if payload.PermissionMode != "default" {
		t.Errorf("PermissionMode: got %q, want %q", payload.PermissionMode, "default")
	}
	if payload.UsedTokens != 12480 {
		t.Errorf("UsedTokens: got %d, want %d", payload.UsedTokens, 12480)
	}
	if payload.WindowTokens != 200000 {
		t.Errorf("WindowTokens: got %d, want %d", payload.WindowTokens, 200000)
	}

	roundTripEnvelope(t, env, payload, raw)
}

// TestSessionSettingsPayload_EffectiveEffortStatesRoundTrip pins all three
// effective_effort wire states through marshal, decode, and re-marshal. The
// saved effort deliberately differs from the effective effort so swapping the
// two JSON fields cannot pass.
func TestSessionSettingsPayload_EffectiveEffortStatesRoundTrip(t *testing.T) {
	t.Parallel()
	effective := "medium"
	base := SessionSettingsPayload{
		SessionID:      "sess-a",
		Model:          "opus",
		Effort:         "high",
		YOLO:           false,
		PermissionMode: "default",
		UsedTokens:     12480,
		WindowTokens:   200000,
	}
	cases := []struct {
		name        string
		effective   NullableString
		wantPresent bool
		wantValue   *string
		wantJSON    string
	}{
		{
			name:        "confirmed string",
			effective:   NewNullableString(&effective),
			wantPresent: true,
			wantValue:   &effective,
			wantJSON:    `{"session_id":"sess-a","model":"opus","effort":"high","effective_effort":"medium","yolo":false,"permission_mode":"default","used_tokens":12480,"window_tokens":200000}`,
		},
		{
			name:        "reported no parameter",
			effective:   NewNullableString(nil),
			wantPresent: true,
			wantJSON:    `{"session_id":"sess-a","model":"opus","effort":"high","effective_effort":null,"yolo":false,"permission_mode":"default","used_tokens":12480,"window_tokens":200000}`,
		},
		{
			name:     "unavailable or unsupported",
			wantJSON: `{"session_id":"sess-a","model":"opus","effort":"high","yolo":false,"permission_mode":"default","used_tokens":12480,"window_tokens":200000}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			payload := base
			payload.EffectiveEffort = tc.effective
			out, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			if string(out) != tc.wantJSON {
				t.Fatalf("marshal payload: got %s, want %s", out, tc.wantJSON)
			}

			var decoded SessionSettingsPayload
			if err := json.Unmarshal(out, &decoded); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			decodedEffective := decoded.EffectiveEffort
			gotValue, gotPresent := decoded.EffectiveEffort.Value()
			if gotPresent != tc.wantPresent {
				t.Errorf("EffectiveEffort presence: got %v, want %v", gotPresent, tc.wantPresent)
			}
			switch {
			case gotValue == nil && tc.wantValue != nil:
				t.Errorf("EffectiveEffort value: got nil, want %q", *tc.wantValue)
			case gotValue != nil && tc.wantValue == nil:
				t.Errorf("EffectiveEffort value: got %q, want nil", *gotValue)
			case gotValue != nil && *gotValue != *tc.wantValue:
				t.Errorf("EffectiveEffort value: got %q, want %q", *gotValue, *tc.wantValue)
			}

			decoded.EffectiveEffort = NullableString{}
			if decoded != base {
				t.Errorf("existing fields changed: got %+v, want %+v", decoded, base)
			}
			decoded.EffectiveEffort = decodedEffective
			roundTrip, err := json.Marshal(decoded)
			if err != nil {
				t.Fatalf("re-marshal payload: %v", err)
			}
			if string(roundTrip) != tc.wantJSON {
				t.Errorf("re-marshal payload: got %s, want %s", roundTrip, tc.wantJSON)
			}
		})
	}
}

func TestSessionSettingsPayload_EffectiveEffortRejectsNonString(t *testing.T) {
	t.Parallel()
	var payload SessionSettingsPayload
	if err := json.Unmarshal([]byte(`{"effective_effort":false}`), &payload); err == nil {
		t.Fatal("decode effective_effort boolean: got nil error, want a JSON type error")
	}
	if _, present := payload.EffectiveEffort.Value(); present {
		t.Error("EffectiveEffort became present after a failed decode")
	}
}

// TestSessionSettingsPayload_ZeroFieldsPresent pins the no-omitempty contract
// that makes this payload a full report rather than a diff: every zero value
// stays explicitly on the wire. Each one is a real answer a client acts on —
// session_id "" means "no session to address, treat the controls as read-only",
// model/effort "" mean "inherited default", yolo false means permissions
// enforced, and window_tokens 0 means the usage seam was not wired. Dropping any
// of them would make "the daemon says zero" indistinguishable from "the daemon
// did not say", which is exactly the ambiguity that let the inert-sheet defect
// hide. Mirrors TestScreenSnapshotPayload_ZeroSettingsFieldsPresent.
func TestSessionSettingsPayload_ZeroFieldsPresent(t *testing.T) {
	t.Parallel()
	out, err := json.Marshal(SessionSettingsPayload{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"session_id":""`,
		`"model":""`,
		`"effort":""`,
		`"yolo":false`,
		// permission_mode "" is the one zero here that does NOT name a posture
		// (#1687): a resolved session always reports one of claude's six modes,
		// because the pool normalises the stored value at construction. So ""
		// occurs only in this all-zero reply, alongside session_id "" — it means
		// "nothing resolved", not "running in some unnamed mode". Keeping it on
		// the wire is what lets a client read that pair as one answer.
		`"permission_mode":""`,
		`"used_tokens":0`,
		`"window_tokens":0`,
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("zero-value field %s should stay on the wire; got %s", want, out)
		}
	}
	if strings.Contains(string(out), `"effective_effort"`) {
		t.Errorf("zero-value effective_effort should be omitted; got %s", out)
	}
}
