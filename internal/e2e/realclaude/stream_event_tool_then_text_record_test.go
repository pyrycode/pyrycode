//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolThenTextCaptureRedaction(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage(`{"type":"system","subtype":"init","messaging_socket_path":"/tmp/cc-socks/123456789.sock"}`),
		json.RawMessage(`{"type":"stream_event","parent_tool_use_id":"tool-real-identity","event":{"type":"message_start","message":{"id":"msg-real-identity"}}}`),
		json.RawMessage(`{"type":"stream_event","parent_tool_use_id":"tool-real-identity","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pyry1311"}}}`),
		json.RawMessage(`{"type":"assistant","message":{"id":"msg-real-identity","content":[{"type":"tool_use","id":"tool-real-identity"}]}}`),
		json.RawMessage(`{"type":"stream_event","event":{"type":"content_block_delta","index":"0","delta":{"type":"future_delta"}}}`),
		json.RawMessage(`{"type":"stream_event","event":{"type":"message_stop"}}`),
		json.RawMessage(`{"type":"control_response","id":"0","cwd":"/","count":10,"index":-1}`),
	}
	red := newDropcapRedactor("", "", "", "", "", 987654321)
	scanner := newDropcapScanner("", "", "")
	got := tttRedactRows(rows, red, &scanner)
	if hits, _ := scanner.scan(rows[0]); len(hits) == 0 || !scanner.applied()[dropcapClassMessagingSocket] {
		t.Fatal("raw socket must arm and fail the dynamic deny-scan")
	}
	for _, row := range got {
		if hits, _ := scanner.scan(row); len(hits) != 0 {
			t.Fatalf("redacted row retains denied classes: %v", hits)
		}
		if !json.Valid(row) || strings.Contains(string(row), "real-identity") {
			t.Fatal("row is invalid or retains an original identity")
		}
	}
	var start, delta, assistant map[string]any
	_ = json.Unmarshal(got[1], &start)
	_ = json.Unmarshal(got[2], &delta)
	_ = json.Unmarshal(got[3], &assistant)
	if start["parent_tool_use_id"] != delta["parent_tool_use_id"] ||
		start["event"].(map[string]any)["message"].(map[string]any)["id"] != assistant["message"].(map[string]any)["id"] {
		t.Fatal("redaction changed identity relationships")
	}
	if !strings.Contains(string(got[2]), `"index":0`) || !strings.Contains(string(got[4]), `"index":"0"`) || strings.Contains(string(got[5]), `"index"`) {
		t.Fatal("redaction changed index presence or JSON type")
	}
	if !strings.Contains(string(got[6]), `"count":10`) || !strings.Contains(string(got[6]), `"index":-1`) {
		t.Fatal("short identities or root paths corrupted JSON numbers")
	}
}

func TestToolThenTextCaptureAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		loss tttLoss
		last string
		want bool
	}{
		{"complete", tttLoss{}, `{"type":"result"}`, true},
		{"line loss", tttLoss{Lines: 1}, `{"type":"result"}`, false},
		{"byte loss", tttLoss{Bytes: 1}, `{"type":"result"}`, false},
		{"partial loss", tttLoss{Partials: 1}, `{"type":"result"}`, false},
		{"tail", tttLoss{Tail: 1}, `{"type":"result"}`, false},
		{"missing result", tttLoss{}, `{"type":"assistant"}`, false},
		{"unsafe row", tttLoss{}, `not json`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := tttRecord{Rows: []json.RawMessage{json.RawMessage(tc.last)}, Loss: tc.loss}
			if got := rec.complete(); got != tc.want {
				t.Fatalf("complete=%v, want %v", got, tc.want)
			}
		})
	}
}
