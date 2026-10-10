package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestThreadKinds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ got, want string }{
		{TypeThreadItemAdded, "thread_item_added"},
		{TypeThreadItemChanged, "thread_item_changed"},
		{TypeThreadTextAppend, "thread_text_append"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("kind = %q, want %q", tc.got, tc.want)
			}
			if err := IsKnownAppType(Envelope{Type: tc.got}); !errors.Is(err, ErrUnknownType) {
				t.Fatalf("outgoing thread kind admitted inbound: %v", err)
			}
		})
	}
}

func TestThreadItemAddedRoundTrip(t *testing.T) {
	t.Parallel()
	kinds := []string{"user_message", "assistant_message", "tool_call", "agent", "session_divider", "compaction", "turn_end", "notice", "future_kind"}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			content := `{"text":"saved text","unknown":{"script":"<script>inert</script>","count":9007199254740991},"nullable":null,"values":[false,0,""]}`
			raw := fmt.Sprintf(`{"conversation_id":"conv-a","epoch":"epoch-b","version":9007199254740991,"item":{"id":101,"kind":%q,"order":202,"rev":303,"ended_order":404,"session":"session-c","agent":"codex","turn":"turn-d","parent":505,"status":"future_status","active":false,"shown":false,"summary":"plain fallback","subtype":"future_subtype","content":%s}}`, kind, content)
			want := ThreadItemAddedPayload{
				ConversationID: "conv-a", Epoch: "epoch-b", Version: 9007199254740991,
				Item: ThreadItem{ID: 101, Kind: kind, Order: 202, Rev: 303, EndedOrder: 404,
					Session: "session-c", Agent: "codex", Turn: "turn-d", Parent: 505,
					Status: "future_status", Active: false, Shown: false, Summary: "plain fallback",
					Subtype: "future_subtype", Content: json.RawMessage(content)},
			}
			_, emitted := testThreadPayloadRoundTrip(t, TypeThreadItemAdded, raw, want)
			item := emitted["item"].(map[string]any)
			if item["active"] != false || item["shown"] != false {
				t.Fatalf("false flags omitted: %#v", item)
			}
		})
	}
}

func TestThreadItemOptionalFacts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, fields, status string
		order                uint64
		session, agent       string
		noChild              bool
	}{
		{name: "claude", fields: `,"order":202,"session":"known-claude","agent":"claude"`, status: "done", order: 202, session: "known-claude", agent: "claude"},
		{name: "codex", fields: `,"order":202,"session":"known-codex","agent":"codex"`, status: "done", order: 202, session: "known-codex", agent: "codex"},
		{name: "known session unknown agent", fields: `,"order":202,"session":"known-session"`, status: "done", order: 202, session: "known-session"},
		{name: "unknown", status: "queued"},
		{name: "no child", fields: `,"no_child":true`, status: "queued", noChild: true},
		{name: "dropped", status: "dropped"},
		{name: "lost", status: "lost"},
		{name: "delivered", fields: `,"order":909`, status: "delivered", order: 909},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"conversation_id":"conv-a","epoch":"epoch-b","version":1001,"item":{"id":101,"kind":"user_message","rev":303,"status":%q,"active":true,"shown":true,"summary":"message","content":{"text":"hi"}%s}}`, tc.status, tc.fields)
			want := ThreadItemAddedPayload{ConversationID: "conv-a", Epoch: "epoch-b", Version: 1001,
				Item: ThreadItem{ID: 101, Kind: "user_message", Order: tc.order, Rev: 303, Status: tc.status,
					Active: true, Shown: true, Summary: "message", Content: json.RawMessage(`{"text":"hi"}`),
					Session: tc.session, Agent: tc.agent, NoChild: tc.noChild}}
			_, emitted := testThreadPayloadRoundTrip(t, TypeThreadItemAdded, raw, want)
			item := emitted["item"].(map[string]any)
			for _, key := range []string{"parent", "ended_order", "turn", "subtype"} {
				if _, ok := item[key]; ok {
					t.Errorf("unset %s emitted: %#v", key, item)
				}
			}
			for key, present := range map[string]bool{"order": tc.order != 0, "session": tc.session != "", "agent": tc.agent != "", "no_child": tc.noChild} {
				if _, ok := item[key]; ok != present {
					t.Errorf("%s presence = %v, want %v", key, ok, present)
				}
			}
		})
	}
}

func TestThreadChangesRoundTrip(t *testing.T) {
	t.Parallel()
	for _, fields := range []string{
		`{}`,
		`{"summary":"","active":false,"shown":false,"order":0,"parent":0,"ended_order":0,"session":"","agent":"","no_child":false,"turn":"","status":"","subtype":"","content":{}}`,
		`{"content":{"replacement":true,"unknown":[null,0,false],"text":"whole new value"}}`,
		`{"content":null,"future_field":0}`,
	} {
		t.Run(fields, func(t *testing.T) {
			var changes map[string]json.RawMessage
			if err := json.Unmarshal([]byte(fields), &changes); err != nil {
				t.Fatal(err)
			}
			raw := fmt.Sprintf(`{"conversation_id":"conv-a","epoch":"epoch-b","version":808,"item_id":101,"base_rev":303,"rev":707,"changes":%s}`, fields)
			want := ThreadItemChangedPayload{ConversationID: "conv-a", Epoch: "epoch-b", Version: 808,
				ItemID: 101, BaseRev: 303, Rev: 707, Changes: changes}
			_, emitted := testThreadPayloadRoundTrip(t, TypeThreadItemChanged, raw, want)
			patch := emitted["changes"].(map[string]any)
			for _, key := range []string{"id", "kind"} {
				if _, ok := patch[key]; ok {
					t.Errorf("stable %s unexpectedly patched", key)
				}
			}
		})
	}
}

func TestThreadTextAppendRoundTrip(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{"next\ntext", ""} {
		raw := fmt.Sprintf(`{"conversation_id":"conv-a","epoch":"epoch-b","version":808,"item_id":101,"base_rev":303,"rev":707,"text":%q}`, suffix)
		want := ThreadTextAppendPayload{ConversationID: "conv-a", Epoch: "epoch-b", Version: 808, ItemID: 101, BaseRev: 303, Rev: 707, Text: suffix}
		testThreadPayloadRoundTrip(t, TypeThreadTextAppend, raw, want)
	}
}

func testThreadPayloadRoundTrip[P any](t *testing.T, kind, raw string, want P) (Envelope, map[string]any) {
	t.Helper()
	wire := fmt.Sprintf(`{"id":11,"type":%q,"ts":"2026-10-10T06:00:00Z","payload":%s}`, kind, raw)
	var env Envelope
	if err := json.Unmarshal([]byte(wire), &env); err != nil {
		t.Fatal(err)
	}
	var got P
	if err := json.Unmarshal(env.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded payload = %#v, want %#v", got, want)
	}
	payload, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	env.Payload = payload
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	testThreadJSONEqual(t, []byte(wire), out)
	return env, testThreadJSONObject(t, payload)
}

func testThreadJSONEqual(t *testing.T, want, got []byte) {
	t.Helper()
	var wantValue, gotValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("emitted JSON = %s, want %s", got, want)
	}
}

func testThreadJSONObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}
