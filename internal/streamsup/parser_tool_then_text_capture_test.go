package streamsup

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TestParser_ToolThenTextIsolatedCapture proves the retained isolated attempt's
// behavior. Its passing result does not diagnose the separate mobile rejection.
func TestParser_ToolThenTextIsolatedCapture(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"tool_then_text_stream_event_v2.1.280.json", "tool_then_text_wire_stream_event_v2.1.280.json"} {
		t.Run(name, func(t *testing.T) { testToolThenTextCapture(t, name, "", "2.1.280 (Claude Code)") })
	}
}

func testToolThenTextCapture(t *testing.T, name, source, version string) {
	t.Helper()
	data, err := os.ReadFile("../e2e/realclaude/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Source      string                     `json:"capture_source"`
		Ticket      string                     `json:"ticket"`
		Version     string                     `json:"claude_version"`
		Revision    string                     `json:"daemon_revision"`
		Rows        []json.RawMessage          `json:"ordered_stdout"`
		Rejected    []int                      `json:"rejected_rows"`
		ReplayMatch *bool                      `json:"retained_replay_matches_original"`
		Loss        map[string]json.RawMessage `json:"capture_loss"`
		ScanApplied map[string]bool            `json:"credential_scan_applied"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Source != source || rec.Ticket != "2939" || rec.Version != version || len(rec.Revision) != 40 || len(rec.Rejected) != 0 || len(rec.Rows) == 0 {
		t.Fatal("missing or unpinned capture provenance")
	}
	for _, key := range []string{"lines_dropped_over_cap", "bytes_dropped_over_cap", "partials_dropped", "unterminated_tail_bytes"} {
		if n, ok := rec.Loss[key]; !ok || string(bytes.TrimSpace(n)) != "0" {
			t.Fatalf("capture loss %s must be present and zero", key)
		}
	}
	if !rec.ScanApplied["messaging_socket_path"] {
		t.Fatal("capture did not apply messaging-socket deny-scan")
	}
	wireCapture := strings.Contains(name, "_wire_") || source == "mobile-daemon"
	if wireCapture && (rec.ReplayMatch == nil || !*rec.ReplayMatch) {
		t.Fatal("wire capture must pin agreement with the original rejection census")
	}
	var got []turnevent.Event
	var logs bytes.Buffer
	p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	var currentID string
	var wantText, actualText []turnevent.TextChunk
	var settled, streamed strings.Builder
	var wantToolIDs, startIDs, updateIDs []string
	for i, row := range rec.Rows {
		if len(row) > 0 && row[0] == '"' {
			var wire string
			if err := json.Unmarshal(row, &wire); err != nil {
				t.Fatal(err)
			}
			row = json.RawMessage(wire)
			rec.Rows[i] = row
		}
		var line struct {
			Type       string                     `json:"type"`
			Parent     string                     `json:"parent_tool_use_id"`
			Message    *streamMessage             `json:"message"`
			WireInputs map[string]json.RawMessage `json:"wire_tool_inputs"`
			Event      struct {
				Type    string `json:"type"`
				Message struct {
					ID string `json:"id"`
				} `json:"message"`
				Delta struct{ Type, Text string } `json:"delta"`
			} `json:"event"`
		}
		if err := json.Unmarshal(row, &line); err != nil {
			t.Fatal(err)
		}
		if line.Event.Type == "message_start" {
			currentID = line.Event.Message.ID
		}
		if line.Event.Delta.Type == "text_delta" {
			wantText = append(wantText, turnevent.TextChunk{MessageID: currentID, ParentToolCallID: line.Parent, Text: line.Event.Delta.Text})
		}
		if line.Type == "assistant" && line.Message != nil {
			for _, raw := range line.Message.Content {
				var block streamBlock
				if err := json.Unmarshal(raw, &block); err != nil {
					t.Fatal(err)
				}
				switch block.Type {
				case "text":
					settled.WriteString(block.Text)
				case "tool_use":
					wantToolIDs = append(wantToolIDs, block.ID)
					if wireCapture {
						if _, ok := line.WireInputs[block.ID]; !ok {
							t.Fatal("completed tool ID must match its retained wire input key")
						}
					}
				}
			}
		}
		before := len(got)
		if _, err := p.Write(append(append([]byte(nil), row...), '\n')); err != nil {
			t.Fatal(err)
		}
		if line.Event.Delta.Type == "input_json_delta" && len(got) != before {
			t.Fatalf("row %d partial tool JSON emitted an event", i)
		}
	}
	for _, ev := range got {
		switch ev := ev.(type) {
		case turnevent.TextChunk:
			actualText = append(actualText, ev)
			streamed.WriteString(ev.Text)
		case turnevent.ToolStart:
			startIDs = append(startIDs, ev.ToolCallID)
		case turnevent.ToolUpdate:
			if ev.Status != turnevent.ToolStatusCompleted {
				t.Fatal("captured tool did not complete")
			}
			updateIDs = append(updateIDs, ev.ToolCallID)
		case turnevent.ThoughtChunk:
			if ev.Text != "" {
				t.Fatal("stream thinking carried content")
			}
		case turnevent.Unrecognized:
			t.Fatal("captured turn emitted Unrecognized")
		}
	}
	if len(wantText) == 0 || !reflect.DeepEqual(actualText, wantText) || streamed.String() != settled.String() {
		t.Fatal("text was duplicated, reordered or misattributed")
	}
	if len(wantToolIDs) != 1 || !reflect.DeepEqual(startIDs, wantToolIDs) || !reflect.DeepEqual(updateIDs, wantToolIDs) {
		t.Fatal("completed tool must start and finish exactly once")
	}
	if strings.Contains(logs.String(), settled.String()) {
		t.Fatal("model text appeared in application logs")
	}
	var last struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(rec.Rows[len(rec.Rows)-1], &last); err != nil || last.Type != "result" {
		t.Fatal("capture does not end at result")
	}
}
