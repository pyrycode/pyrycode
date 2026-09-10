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

const streamEventCapturePath = "../e2e/realclaude/testdata/stream_event_v2.1.259.json"

type streamEventCapture struct {
	IsCapture          bool           `json:"is_capture"`
	ClaudeVersion      string         `json:"claude_version"`
	MessageIDsObserved []string       `json:"message_ids_observed"`
	DeltaTypeCensus    map[string]int `json:"delta_type_census"`
	Lines              []struct {
		Type            string `json:"type"`
		PayloadEncoding string `json:"payload_encoding"`
		Payload         string `json:"payload"`
	} `json:"lines"`
}

func readStreamEventCapture(t *testing.T) streamEventCapture {
	t.Helper()
	raw, err := os.ReadFile(streamEventCapturePath)
	if err != nil {
		t.Fatalf("reading capture %s: %v", streamEventCapturePath, err)
	}
	var capture streamEventCapture
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decoding capture %s: %v", streamEventCapturePath, err)
	}
	if !capture.IsCapture {
		t.Fatalf("%s: is_capture is false", streamEventCapturePath)
	}
	if capture.ClaudeVersion != "2.1.259 (Claude Code)" {
		t.Fatalf("%s: claude_version = %q, want %q", streamEventCapturePath, capture.ClaudeVersion, "2.1.259 (Claude Code)")
	}
	if len(capture.Lines) == 0 {
		t.Fatalf("%s: lines is empty", streamEventCapturePath)
	}
	for i, line := range capture.Lines {
		if line.PayloadEncoding != "json-string" {
			t.Fatalf("%s: line %d payload_encoding = %q, want json-string", streamEventCapturePath, i, line.PayloadEncoding)
		}
	}
	return capture
}

func TestParser_StreamEventCapture(t *testing.T) {
	t.Parallel()
	capture := readStreamEventCapture(t)
	wantMessageIDs := []string{"msg_011CetKAtZ2R8cmF1haEVNFa", "msg_011CetKB6LSvqZ1Z4hb4Wbkp"}
	if !reflect.DeepEqual(capture.MessageIDsObserved, wantMessageIDs) {
		t.Fatalf("message_ids_observed = %q, want %q", capture.MessageIDsObserved, wantMessageIDs)
	}
	if capture.DeltaTypeCensus["text_delta"] != 7 {
		t.Fatalf("text_delta census = %d, want 7", capture.DeltaTypeCensus["text_delta"])
	}

	var got []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, discardLogger())
	var settledText strings.Builder
	for i, record := range capture.Lines {
		var line streamLine
		if err := json.Unmarshal([]byte(record.Payload), &line); err != nil {
			t.Fatalf("line %d: decoding payload: %v", i, err)
		}
		if line.Type == "assistant" && line.Message != nil {
			for _, rawBlock := range line.Message.Content {
				var block streamBlock
				if err := json.Unmarshal(rawBlock, &block); err != nil {
					t.Fatalf("line %d: decoding assistant block: %v", i, err)
				}
				if block.Type == "text" {
					settledText.WriteString(block.Text)
				}
			}
		}

		before := len(got)
		_, _ = p.Write(append([]byte(record.Payload), '\n'))
		if line.Type != "stream_event" {
			continue
		}
		var outer struct {
			Event struct {
				Type  string `json:"type"`
				Delta struct {
					Type string `json:"type"`
				} `json:"delta"`
			} `json:"event"`
		}
		if err := json.Unmarshal([]byte(record.Payload), &outer); err != nil {
			t.Fatalf("line %d: decoding stream event: %v", i, err)
		}
		added := got[before:]
		if outer.Event.Type == "content_block_delta" && outer.Event.Delta.Type == "text_delta" {
			if len(added) != 1 {
				t.Fatalf("line %d text_delta emitted %d events, want 1", i, len(added))
			}
			if _, ok := added[0].(turnevent.TextChunk); !ok {
				t.Fatalf("line %d text_delta event = %T, want turnevent.TextChunk", i, added[0])
			}
		} else if len(added) != 0 {
			t.Fatalf("line %d %s/%s emitted %#v, want no event", i, outer.Event.Type, outer.Event.Delta.Type, added)
		}
	}

	var text strings.Builder
	var textIDs []string
	toolStarts := 0
	unrecognized := 0
	for _, ev := range got {
		switch ev := ev.(type) {
		case turnevent.TextChunk:
			text.WriteString(ev.Text)
			textIDs = append(textIDs, ev.MessageID)
		case turnevent.ToolStart:
			toolStarts++
		case turnevent.Unrecognized:
			unrecognized++
		}
	}
	if text.String() != settledText.String() {
		t.Errorf("concatenated TextChunk text = %q, want settled assistant text %q", text.String(), settledText.String())
	}
	wantTextIDs := []string{
		wantMessageIDs[0], wantMessageIDs[0], wantMessageIDs[0],
		wantMessageIDs[1], wantMessageIDs[1], wantMessageIDs[1], wantMessageIDs[1],
	}
	if !reflect.DeepEqual(textIDs, wantTextIDs) {
		t.Errorf("TextChunk message IDs = %q, want %q", textIDs, wantTextIDs)
	}
	if toolStarts != 1 {
		t.Errorf("ToolStart count = %d, want 1", toolStarts)
	}
	if unrecognized != 0 {
		t.Errorf("Unrecognized count = %d, want 0", unrecognized)
	}
}

func TestParser_StreamEventRejectsUnsafeInput(t *testing.T) {
	t.Parallel()
	messageStart := `{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg-safe"}}}`
	textStart := `{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}}`
	tests := []struct {
		name   string
		prefix []string
		line   string
	}{
		{name: "unknown event", line: `{"type":"stream_event","event":{"type":"future_event"}}`},
		{name: "undecodable event", line: `{"type":"stream_event","event":42}`},
		{name: "message start without id", line: `{"type":"stream_event","event":{"type":"message_start","message":{}}}`},
		{name: "unknown delta", prefix: []string{messageStart, textStart}, line: `{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"future_delta"}}}`},
		{name: "text delta without message", prefix: []string{textStart}, line: `{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"lost"}}}`},
		{name: "text delta without matching block", prefix: []string{messageStart, textStart}, line: `{"type":"stream_event","event":{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"lost"}}}`},
		{name: "undecodable text", prefix: []string{messageStart, textStart}, line: `{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":42}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got []turnevent.Event
			p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, discardLogger())
			for _, prefix := range tt.prefix {
				_, _ = p.Write([]byte(prefix + "\n"))
			}
			got = nil
			_, _ = p.Write([]byte(tt.line + "\n"))
			want := []turnevent.Event{turnevent.Unrecognized{Site: turnevent.UnrecognizedLineType, Kind: "stream_event", Raw: tt.line}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("events = %#v, want %#v", got, want)
			}
		})
	}
}

func TestParser_StreamEventResultClearsAttribution(t *testing.T) {
	t.Parallel()
	lines := []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg-before-result"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"before"}}}`,
		`{"type":"result","subtype":"success"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"after"}}}`,
	}
	var got []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, discardLogger())
	for _, line := range lines {
		_, _ = p.Write([]byte(line + "\n"))
	}
	if len(got) != 3 {
		t.Fatalf("event count = %d, want 3: %#v", len(got), got)
	}
	if chunk, ok := got[0].(turnevent.TextChunk); !ok || chunk.MessageID != "msg-before-result" || chunk.Text != "before" {
		t.Errorf("event[0] = %#v, want attributed pre-result TextChunk", got[0])
	}
	if _, ok := got[1].(turnevent.TurnEnd); !ok {
		t.Errorf("event[1] = %T, want turnevent.TurnEnd", got[1])
	}
	if _, ok := got[2].(turnevent.Unrecognized); !ok {
		t.Errorf("event[2] = %T, want turnevent.Unrecognized", got[2])
	}
}

func TestParser_StreamEventAssistantWithoutDeltaEmits(t *testing.T) {
	t.Parallel()
	lines := []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg-no-delta"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
		`{"type":"assistant","message":{"id":"msg-no-delta","role":"assistant","content":[{"type":"text","text":"settled only"}]}}`,
	}
	var got []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, discardLogger())
	for _, line := range lines {
		_, _ = p.Write([]byte(line + "\n"))
	}
	want := []turnevent.Event{turnevent.TextChunk{MessageID: "msg-no-delta", Text: "settled only"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}

func TestParser_StreamEventTextIsNotLogged(t *testing.T) {
	t.Parallel()
	const secret = "MODEL-DELTA-MUST-STAY-OFF-LOGS-2270"
	var logs bytes.Buffer
	p := NewParser(func(turnevent.Event) {}, slog.New(slog.NewTextHandler(&logs, nil)))
	for _, line := range []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg-log"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + secret + `"}}}`,
	} {
		_, _ = p.Write([]byte(line + "\n"))
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("parser log contains model text: %q", logs.String())
	}
}
