package protocol

import (
	"encoding/json"
	"testing"
)

func TestConversationReadStateSerialization(t *testing.T) {
	t.Parallel()
	for _, n := range []uint64{0, 1<<63 + 7} {
		for _, tc := range []struct {
			name  string
			value any
			want  map[string]uint64
		}{
			{"list", ConversationSummary{ReadUpTo: n}, map[string]uint64{"read_up_to": n}},
			{"update", ConversationUpdatedPayload{ReadUpTo: n}, map[string]uint64{"read_up_to": n}},
			{"list zeros", ConversationSummary{}, map[string]uint64{"read_up_to": 0}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				raw, err := json.Marshal(tc.value)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				if _, exists := fields["latest_entry_id"]; exists {
					t.Fatalf("retired watermark serialized: %s", raw)
				}
				for key, want := range tc.want {
					var got uint64
					if err := json.Unmarshal(fields[key], &got); err != nil || got != want {
						t.Fatalf("%s = %s, want %d (%v)", key, fields[key], want, err)
					}
				}
			})
		}
	}
}
