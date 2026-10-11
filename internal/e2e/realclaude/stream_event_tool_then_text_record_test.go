//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestToolThenTextCaptureRedaction(t *testing.T) {
	rows := []string{
		`{"type":"system","subtype":"init","messaging_socket_path":"/tmp/cc-socks/123456789.sock"}`,
		`{"type":"stream_event","parent_tool_use_id":"tool-real-identity","event":{"type":"message_start","message":{"id":"msg-real-identity"}}}`,
		`{"type":"stream_event","parent_tool_use_id":"tool-real-identity","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pyry1311"}}}`,
		`{"type":"assistant","message":{"id":"msg-real-identity","content":[{"type":"tool_use","id":"tool-real-identity"}]},"wire_tool_inputs":{"tool-real-identity":{"command":"echo pyry1311"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":"0","delta":{"type":"future_delta"}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"control_response","id":"0","cwd":"/","count":10,"index":-1}`,
	}
	red := newDropcapRedactor("", "", "", "", "", 987654321)
	scanner := newDropcapScanner("", "", "")
	got := tttRedactRows(rows, red, &scanner)
	if hits, _ := scanner.scan([]byte(rows[0])); len(hits) == 0 || !scanner.applied()[dropcapClassMessagingSocket] {
		t.Fatal("raw socket must arm and fail the dynamic deny-scan")
	}
	for _, row := range got {
		if hits, _ := scanner.scan([]byte(row)); len(hits) != 0 {
			t.Fatalf("redacted row retains denied classes: %v", hits)
		}
		if !json.Valid([]byte(row)) || strings.Contains(string(row), "real-identity") {
			t.Fatal("row is invalid or retains an original identity")
		}
	}
	var start, delta, assistant map[string]any
	_ = json.Unmarshal([]byte(got[1]), &start)
	_ = json.Unmarshal([]byte(got[2]), &delta)
	_ = json.Unmarshal([]byte(got[3]), &assistant)
	if start["parent_tool_use_id"] != delta["parent_tool_use_id"] ||
		start["event"].(map[string]any)["message"].(map[string]any)["id"] != assistant["message"].(map[string]any)["id"] {
		t.Fatal("redaction changed identity relationships")
	}
	toolID := assistant["message"].(map[string]any)["content"].([]any)[0].(map[string]any)["id"].(string)
	if _, ok := assistant["wire_tool_inputs"].(map[string]any)[toolID]; !ok {
		t.Fatal("redaction separated the completed tool ID from its wire input key")
	}
	if !strings.Contains(string(got[2]), `"index":0`) || !strings.Contains(string(got[4]), `"index":"0"`) || strings.Contains(string(got[5]), `"index"`) {
		t.Fatal("redaction changed index presence or JSON type")
	}
	if !strings.Contains(string(got[6]), `"count":10`) || !strings.Contains(string(got[6]), `"index":-1`) {
		t.Fatal("short identities or root paths corrupted JSON numbers")
	}
}

func TestToolThenTextCaptureWireRejections(t *testing.T) {
	for _, tc := range []struct {
		name, block string
		valid       bool
	}{
		{"duplicate index", `{"type":"stream_event","event":{"type":"content_block_start","index":"bad","index":0,"content_block":{"type":"text"}}}`, true},
		{"trailing data", `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}} non-JSON suffix`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := []string{
				`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg-real-identity"}}}`,
				tc.block,
				`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pyry1311"}}}`,
				`{"type":"result"}`,
			}
			_, original := tttReplay(t, rows)
			if !reflect.DeepEqual(original, []int{1, 2}) {
				t.Fatalf("original rejection rows=%v, want [1 2]", original)
			}
			red := newDropcapRedactor("", "", "", "", "", 987654321)
			scanner := newDropcapScanner("", "", "")
			got := tttRedactRows(rows, red, &scanner)
			if string(got[1]) != tc.block {
				t.Fatal("redaction changed rejecting wire shape")
			}
			_, retained := tttReplay(t, got)
			if !reflect.DeepEqual(retained, original) {
				t.Fatalf("retained rejection rows=%v, want %v", retained, original)
			}
			blob, err := json.Marshal(tttRecord{Rows: got, RejectedRows: original, ReplayMatches: true})
			if err != nil {
				t.Fatal("malformed wire rows must survive recovery serialization", err)
			}
			var recovered tttRecord
			if err := json.Unmarshal(blob, &recovered); err != nil || !reflect.DeepEqual(recovered.Rows, got) {
				t.Fatal("recovery changed retained wire rows", err)
			}
			if complete := recovered.complete(); complete != tc.valid {
				t.Fatalf("promotion complete=%v, want %v", complete, tc.valid)
			}
		})
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
			rec := tttRecord{Rows: []string{tc.last}, Loss: tc.loss, ReplayMatches: true}
			if got := rec.complete(); got != tc.want {
				t.Fatalf("complete=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestToolThenTextCaptureRejectsCensusMismatch(t *testing.T) {
	rec := tttRecord{Rows: []string{`{"type":"result"}`}}
	if rec.complete() {
		t.Fatal("capture without a matching original/retained rejection census must not promote")
	}
}
