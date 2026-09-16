package conversations

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func strPtr(s string) *string { return &s }

func TestConversation_JSONRoundTrip(t *testing.T) {
	// time.Date avoids the monotonic clock reading that survives in time.Now()
	// values — JSON round-trip drops the monotonic component, and reflect.DeepEqual
	// on a time.Time with monotonic-vs-without will fail.
	ts := time.Date(2026, 5, 9, 12, 34, 56, 0, time.UTC)

	cases := []struct {
		name string
		in   Conversation
	}{
		{
			name: "promoted named with history",
			in: Conversation{
				ID:               "11111111-2222-4333-8444-555555555555",
				Name:             strPtr("general"),
				Cwd:              "/home/user/project",
				CurrentSessionID: "sess-current",
				SessionHistory:   []string{"sess-old-1", "sess-old-2"},
				IsPromoted:       true,
				LastUsedAt:       ts,
			},
		},
		{
			name: "unpromoted unnamed no history",
			in: Conversation{
				ID:               "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
				Name:             nil,
				Cwd:              "/tmp/work",
				CurrentSessionID: "",
				SessionHistory:   nil,
				IsPromoted:       false,
				LastUsedAt:       ts,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var out Conversation
			if err := json.Unmarshal(data, &out); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(tc.in, out) {
				t.Fatalf("round-trip mismatch:\n in: %+v\nout: %+v\nwire: %s", tc.in, out, data)
			}
		})
	}
}

func TestConversation_OmitemptyAbsentForUnpromoted(t *testing.T) {
	c := Conversation{
		ID:         "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		Cwd:        "/tmp/work",
		IsPromoted: false,
		LastUsedAt: time.Date(2026, 5, 9, 12, 34, 56, 0, time.UTC),
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	for _, key := range [][]byte{
		[]byte(`"name"`),
		[]byte(`"current_session_id"`),
		[]byte(`"session_history"`),
	} {
		if bytes.Contains(data, key) {
			t.Errorf("expected key %s to be omitted, got: %s", key, data)
		}
	}

	for _, key := range [][]byte{
		[]byte(`"id"`),
		[]byte(`"cwd"`),
		[]byte(`"is_promoted"`),
		[]byte(`"last_used_at"`),
	} {
		if !bytes.Contains(data, key) {
			t.Errorf("expected key %s to be present, got: %s", key, data)
		}
	}
}

// #2149 AC1: the three prompt states are distinguishable both in memory and on
// disk. nil omits the key entirely; a non-nil pointer to "" still emits it,
// because omitempty on a *string tests the pointer, not the pointee.
func TestConversation_SystemPromptThreeStates(t *testing.T) {
	t.Parallel()
	when := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		prompt    *string
		wantKey   bool
		wantValue string // only consulted when wantKey
	}{
		{name: "none", prompt: nil, wantKey: false},
		{name: "explicitly-empty", prompt: strPtr(""), wantKey: true, wantValue: `"system_prompt":""`},
		{name: "operator-set", prompt: strPtr("be terse"), wantKey: true, wantValue: `"system_prompt":"be terse"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := Conversation{
				ID:           "11111111-2222-4333-8444-555555555555",
				Cwd:          "/home/user/project",
				SystemPrompt: tc.prompt,
				LastUsedAt:   when,
			}
			data, err := json.Marshal(in)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			hasKey := bytes.Contains(data, []byte(`"system_prompt"`))
			if hasKey != tc.wantKey {
				t.Errorf("system_prompt key present = %v, want %v:\n%s", hasKey, tc.wantKey, data)
			}
			if tc.wantKey && !bytes.Contains(data, []byte(tc.wantValue)) {
				t.Errorf("encoding missing %s:\n%s", tc.wantValue, data)
			}

			var out Conversation
			if err := json.Unmarshal(data, &out); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(in, out) {
				t.Errorf("round-trip mismatch:\n in = %+v\nout = %+v", in, out)
			}
		})
	}
}

// #2460 AC1: the stored reading survives a marshal → unmarshal round trip, and a
// row that has never held one omits the key entirely.
//
// time.Date rather than time.Now for TestConversation_JSONRoundTrip's stated
// reason: a monotonic reading does not survive JSON and would break DeepEqual.
func TestConversation_LastContextUsageRoundTrip(t *testing.T) {
	t.Parallel()
	when := time.Date(2026, 9, 16, 8, 30, 15, 0, time.UTC)

	tests := []struct {
		name    string
		reading *ContextUsageReading
		wantKey bool
	}{
		{name: "never reported", reading: nil, wantKey: false},
		{
			name: "reported",
			reading: &ContextUsageReading{
				Model:       "claude-opus-5",
				TotalTokens: 31337,
				MaxTokens:   200000,
				Percentage:  16,
				AsOf:        when,
			},
			wantKey: true,
		},
		{
			// A fresh session reports zeroes, and they are a FACT rather than an
			// absent value — so every inner key must still emit. This is what the
			// outer pointer buys and what omitempty on the inner fields would
			// destroy.
			name: "reported all zero",
			reading: &ContextUsageReading{
				AsOf: when,
			},
			wantKey: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := Conversation{
				ID:               "11111111-2222-4333-8444-555555555555",
				Cwd:              "/home/user/project",
				LastContextUsage: tc.reading,
				LastUsedAt:       when,
			}
			data, err := json.Marshal(in)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if hasKey := bytes.Contains(data, []byte(`"last_context_usage"`)); hasKey != tc.wantKey {
				t.Errorf("last_context_usage key present = %v, want %v:\n%s", hasKey, tc.wantKey, data)
			}
			if tc.wantKey {
				for _, key := range []string{"model", "total_tokens", "max_tokens", "percentage", "as_of"} {
					if !bytes.Contains(data, []byte(`"`+key+`"`)) {
						t.Errorf("reading is missing inner key %q:\n%s", key, data)
					}
				}
			}

			var out Conversation
			if err := json.Unmarshal(data, &out); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(in, out) {
				t.Errorf("round-trip mismatch:\n in = %+v\nout = %+v", in, out)
			}
		})
	}
}

// #2460 AC1: as_of serialises as RFC 3339 with a Z offset. time.Time marshals with
// whatever offset it carries, so this is the observable half of the setter's UTC
// normalisation — a value stored with an offset would encode as +HH:MM here.
func TestContextUsageReading_AsOfEncodesAsRFC3339UTC(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(ContextUsageReading{
		Model: "claude-opus-5",
		AsOf:  time.Date(2026, 9, 16, 8, 30, 15, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `"as_of":"2026-09-16T08:30:15Z"`; !bytes.Contains(data, []byte(want)) {
		t.Errorf("encoding missing %s:\n%s", want, data)
	}
}
