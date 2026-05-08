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
