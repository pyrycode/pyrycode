package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

func TestConversationPinnedAt(t *testing.T) {
	t.Parallel()
	stamp := time.Date(2026, 5, 8, 10, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		input string
		want  *time.Time
		wire  string
	}{
		{"timestamp", `{"pinned_at":"2026-05-08T10:30:00Z"}`, &stamp, `"2026-05-08T10:30:00Z"`},
		{"null", `{"pinned_at":null}`, nil, `null`},
		{"absent", `{}`, nil, `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, kind := range []string{"summary", "updated"} {
				t.Run(kind, func(t *testing.T) {
					var payload any
					var got *time.Time
					if kind == "summary" {
						var p ConversationSummary
						if err := json.Unmarshal([]byte(tc.input), &p); err != nil {
							t.Fatal(err)
						}
						got, payload = p.PinnedAt, p
					} else {
						var p ConversationUpdatedPayload
						if err := json.Unmarshal([]byte(tc.input), &p); err != nil {
							t.Fatal(err)
						}
						got, payload = p.PinnedAt, p
					}
					if (got == nil) != (tc.want == nil) || (got != nil && !got.Equal(*tc.want)) {
						t.Fatalf("PinnedAt = %v, want %v", got, tc.want)
					}
					out, err := json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(out, &fields); err != nil {
						t.Fatal(err)
					}
					if got := string(fields["pinned_at"]); got != tc.wire {
						t.Errorf("pinned_at = %q, want %q", got, tc.wire)
					}
				})
			}
		})
	}
}

func TestSetConversationPinnedPayload(t *testing.T) {
	t.Parallel()
	if TypeSetConversationPinned != "set_conversation_pinned" {
		t.Fatalf("request type = %q", TypeSetConversationPinned)
	}
	out, err := json.Marshal(SetConversationPinnedPayload{})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"conversation_id":"","pinned":null}` {
		t.Fatalf("zero request must retain both required keys: %s", out)
	}
	for _, tc := range []struct {
		name    string
		input   string
		present bool
		value   bool
		wantErr bool
	}{
		{"pin", `{"conversation_id":"conv-a","pinned":true}`, true, true, false},
		{"unpin", `{"conversation_id":"conv-a","pinned":false}`, true, false, false},
		{"missing", `{"conversation_id":"conv-a"}`, false, false, false},
		{"null", `{"conversation_id":"conv-a","pinned":null}`, false, false, false},
		{"string", `{"conversation_id":"conv-a","pinned":"false"}`, false, false, true},
		{"number", `{"conversation_id":"conv-a","pinned":0}`, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p SetConversationPinnedPayload
			err := json.Unmarshal([]byte(tc.input), &p)
			if (err != nil) != tc.wantErr {
				t.Fatalf("decode error = %v, want error %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if p.ConversationID != "conv-a" || (p.Pinned != nil) != tc.present || (p.Pinned != nil && *p.Pinned != tc.value) {
				t.Fatalf("decoded request = %#v", p)
			}
			out, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			want := `null`
			if tc.present {
				want = `false`
				if tc.value {
					want = `true`
				}
			}
			if string(out) != `{"conversation_id":"conv-a","pinned":`+want+`}` {
				t.Errorf("request JSON = %s", out)
			}
		})
	}
}
