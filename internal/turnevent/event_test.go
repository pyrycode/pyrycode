package turnevent

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// Each of the five events round-trips its fields by value.
func TestEvents_FieldRoundTrip(t *testing.T) {
	t.Parallel()

	if got := (TextChunk{MessageID: "m1", Text: "hi"}); got.MessageID != "m1" || got.Text != "hi" {
		t.Errorf("TextChunk: got %+v", got)
	}
	if got := (ThoughtChunk{MessageID: "m1", Text: "thinking"}); got.MessageID != "m1" || got.Text != "thinking" {
		t.Errorf("ThoughtChunk: got %+v", got)
	}
	if got := (TurnEnd{Reason: TurnEndReasonMaxTokens}); got.Reason != TurnEndReasonMaxTokens {
		t.Errorf("TurnEnd: got %+v", got)
	}
	if got := (ToolUpdate{ToolCallID: "tc", Status: ToolStatusFailed, Content: TextContent{Text: "boom"}}); got.ToolCallID != "tc" || got.Status != ToolStatusFailed || got.Content != (TextContent{Text: "boom"}) {
		t.Errorf("ToolUpdate: got %+v", got)
	}

	ts := ToolStart{
		ToolCallID: "tc",
		Title:      "Read file",
		Kind:       ToolKindRead,
		RawInput:   json.RawMessage(`{"path":"a.go"}`),
		Locations:  []Location{{Path: "a.go", Line: 12}, {Path: "b.go"}},
	}
	want := ToolStart{
		ToolCallID: "tc",
		Title:      "Read file",
		Kind:       ToolKindRead,
		RawInput:   json.RawMessage(`{"path":"a.go"}`),
		Locations:  []Location{{Path: "a.go", Line: 12}, {Path: "b.go", Line: 0}},
	}
	if !reflect.DeepEqual(ts, want) {
		t.Errorf("ToolStart: got %+v, want %+v", ts, want)
	}
}

// The Event seam the bridge (#608) relies on: an ordered, heterogeneous stream
// of Event whose concrete kind is recovered by type switch.
func TestEvent_StreamTypeSwitch(t *testing.T) {
	t.Parallel()

	stream := []Event{
		TextChunk{MessageID: "m1", Text: "a"},
		ThoughtChunk{MessageID: "m1", Text: "t"},
		ToolStart{ToolCallID: "tc1", Kind: ToolKindRead},
		ToolUpdate{ToolCallID: "tc1", Status: ToolStatusCompleted},
		TurnEnd{Reason: TurnEndReasonEndTurn},
	}
	want := []string{"text", "thought", "tool_start", "tool_update", "turn_end"}
	for i, ev := range stream {
		if got := eventKind(ev); got != want[i] {
			t.Errorf("stream[%d] (%T): got %q, want %q", i, ev, got, want[i])
		}
	}
}

func eventKind(e Event) string {
	switch e.(type) {
	case TextChunk:
		return "text"
	case ThoughtChunk:
		return "thought"
	case ToolStart:
		return "tool_start"
	case ToolUpdate:
		return "tool_update"
	case TurnEnd:
		return "turn_end"
	default:
		return "unknown"
	}
}

// RawInput is opaque: arbitrary bytes round-trip unchanged because the package
// never parses or mutates them — even bytes that are not valid JSON.
func TestToolStart_RawInputOpaque(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{"structured", json.RawMessage(`{"cmd":"ls","args":["-la"],"n":42}`)},
		{"not-json", json.RawMessage(`not even valid json {{{`)},
		{"empty", json.RawMessage(nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ts := ToolStart{ToolCallID: "tc", Kind: ToolKindExecute, RawInput: tc.raw}
			if !bytes.Equal(ts.RawInput, tc.raw) {
				t.Errorf("RawInput: got %q, want %q", ts.RawInput, tc.raw)
			}
		})
	}
}
