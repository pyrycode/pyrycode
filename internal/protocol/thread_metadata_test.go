package protocol

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestConversationLastShownVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, field string
		version     *uint64
	}{
		{name: "absent"},
		{name: "zero", field: `,"last_shown_version":0`, version: testThreadUint64(0)},
		{name: "shown watermark", field: `,"last_shown_version":53`, version: testThreadUint64(53)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"conversations":[{"id":"conv-a","name":null,"is_promoted":false,"current_session_id":"stored-binding","is_archived":false,"is_muted":false,"read_up_to":17,"latest_entry_id":71,"archived_at":null,"cwd":"/workspace","workspace_label":null,"last_message_ts":"0001-01-01T00:00:00Z","last_used_at":"0001-01-01T00:00:00Z"%s}]}`, tc.field)
			want := ConversationsPayload{Conversations: []ConversationSummary{{ID: "conv-a", CurrentSessionID: "stored-binding", ReadUpTo: 17, LatestEntryID: 71, Cwd: "/workspace", LastShownVersion: tc.version}}}
			_, obj := testThreadPayloadRoundTrip(t, TypeConversations, raw, want)
			row := obj["conversations"].([]any)[0].(map[string]any)
			if _, ok := row["last_shown_version"]; ok != (tc.version != nil) {
				t.Fatalf("watermark presence = %v", ok)
			}
		})
	}
}

func TestEnvelopeSessionMetadataRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, field, session string }{
		{name: "omitted"},
		{name: "no session", field: `,"session_id":null`, session: `null`},
		{name: "producing session", field: `,"session_id":"producer-session"`, session: `"producer-session"`},
	} {
		for _, cleared := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/clear=%v", tc.name, cleared), func(t *testing.T) {
				payload := `{"conversation_id":"conv-a","session_id":"payload-session","revision":23,"suggested_reply":null}`
				flag := ""
				if cleared {
					payload = `{}`
					flag = `,"session_state_cleared":true`
				}
				wire := fmt.Sprintf(`{"id":11,"type":"reply_suggestion","ts":"2026-10-10T06:00:00Z","payload":%s%s%s}`, payload, tc.field, flag)
				var env Envelope
				if err := json.Unmarshal([]byte(wire), &env); err != nil {
					t.Fatal(err)
				}
				if string(env.SessionID) != tc.session || env.SessionStateCleared != cleared {
					t.Fatalf("metadata = %s/%v", env.SessionID, env.SessionStateCleared)
				}
				var decoded any
				if cleared {
					var empty struct{}
					if err := json.Unmarshal(env.Payload, &empty); err != nil {
						t.Fatal(err)
					}
					decoded = empty
				} else {
					var suggestion ReplySuggestionPayload
					if err := json.Unmarshal(env.Payload, &suggestion); err != nil {
						t.Fatal(err)
					}
					if suggestion.ConversationID != "conv-a" || suggestion.SessionID != "payload-session" || suggestion.Revision != 23 || suggestion.SuggestedReply != nil {
						t.Fatalf("payload = %#v", suggestion)
					}
					decoded = suggestion
				}
				body, err := json.Marshal(decoded)
				if err != nil {
					t.Fatal(err)
				}
				env.Payload = body
				out, err := json.Marshal(env)
				if err != nil {
					t.Fatal(err)
				}
				testThreadJSONEqual(t, []byte(wire), out)
				obj := testThreadJSONObject(t, out)
				if _, ok := obj["session_id"]; ok != (tc.session != "") {
					t.Errorf("session metadata presence = %v", ok)
				}
				if _, ok := obj["session_state_cleared"]; ok != cleared {
					t.Errorf("clear flag presence = %v", ok)
				}
				if !cleared && obj["payload"].(map[string]any)["session_id"] != "payload-session" {
					t.Fatal("payload session field changed")
				}
			})
		}
	}
}

func testThreadUint64(value uint64) *uint64 { return &value }
