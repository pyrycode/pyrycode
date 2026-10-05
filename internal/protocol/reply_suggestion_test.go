package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestReplySuggestionPayload_RoundTrip(t *testing.T) {
	t.Parallel()
	reply := "Résume the changes."
	cases := []struct {
		fixture string
		want    ReplySuggestionPayload
	}{
		{"reply_suggestion_set.json", ReplySuggestionPayload{
			ConversationID: "conversation-suggestion", SessionID: "session-suggestion",
			Revision: 7, SuggestedReply: &reply,
		}},
		{"reply_suggestion_clear.json", ReplySuggestionPayload{
			ConversationID: "conversation-suggestion", SessionID: "session-suggestion",
			Revision: 8, SuggestedReply: nil,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()
			raw := readFixture(t, tc.fixture)
			var env Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			if env.Type != TypeReplySuggestion {
				t.Errorf("Type: got %q, want %q", env.Type, TypeReplySuggestion)
			}
			var payload ReplySuggestionPayload
			if err := json.Unmarshal(env.Payload, &payload); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			if !reflect.DeepEqual(payload, tc.want) {
				t.Errorf("payload: got %#v, want %#v", payload, tc.want)
			}
			roundTripEnvelope(t, env, payload, raw)

			out, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(out, &fields); err != nil {
				t.Fatalf("unmarshal fields: %v", err)
			}
			if len(fields) != 4 {
				t.Errorf("payload keys: got %d, want 4: %s", len(fields), out)
			}
			for _, key := range []string{"conversation_id", "session_id", "revision", "suggested_reply"} {
				if _, ok := fields[key]; !ok {
					t.Errorf("missing required key %q: %s", key, out)
				}
			}
			if tc.want.SuggestedReply == nil && string(fields["suggested_reply"]) != "null" {
				t.Errorf("clear must emit explicit null: %s", out)
			}
		})
	}
}

func TestReplySuggestionPayload_EmptyStringIsNotNull(t *testing.T) {
	t.Parallel()
	empty := ""
	out, err := json.Marshal(ReplySuggestionPayload{SuggestedReply: &empty})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatalf("unmarshal fields: %v", err)
	}
	if got := string(fields["suggested_reply"]); got != `""` {
		t.Errorf("suggested_reply: got %s, want empty JSON string", got)
	}
}
