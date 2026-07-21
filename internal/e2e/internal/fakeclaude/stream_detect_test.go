package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// userTurnLine hand-mirrors the inbound stream-json user-turn envelope the daemon
// (internal/streamsup/envelope.go marshalTurnEnvelope) writes to claude's stdin.
// It is written by hand — not imported — because streamsup.userTurn is unexported
// there, and fakeclaude's stream mode likewise mirrors the shape rather than
// importing it. The OUTPUT side is checked below by the REAL streamsup.Parser, so
// a shape bug the fake and this hand-written input would share is still caught on
// the emit side (belt-and-suspenders, different fabric).
func userTurnLine(text string) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":%q}]}}`, text)
}

// parseEmitted feeds the bytes fakeclaude wrote to stdout through the real
// streamsup.Parser — the actual daemon-side consumer — and collects the events it
// maps them to, so the assertions run against the production line→event mapping
// (AC4), not a hand-written decoder.
func parseEmitted(t *testing.T, out []byte) []turnevent.Event {
	t.Helper()
	var events []turnevent.Event
	p := streamsup.NewParser(func(ev turnevent.Event) {
		events = append(events, ev)
	}, nil)
	if _, err := p.Write(out); err != nil {
		t.Fatalf("parser write: %v", err)
	}
	return events
}

// TestRunStreamJSON_SingleTurn drives one user-turn line through runStreamJSON and
// asserts the real parser maps the emitted stdout to exactly TextChunk(echoed
// prompt) then TurnEnd(end_turn) — AC2 (one response per turn) + AC4 (the shapes
// parser.go maps to TextChunk / TurnEnd).
func TestRunStreamJSON_SingleTurn(t *testing.T) {
	t.Parallel()

	const prompt = "hello over stream-json"
	var buf bytes.Buffer
	runStreamJSON(strings.NewReader(userTurnLine(prompt)+"\n"), &buf)

	events := parseEmitted(t, buf.Bytes())
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(events), events)
	}
	tc, ok := events[0].(turnevent.TextChunk)
	if !ok {
		t.Fatalf("event[0] = %T, want turnevent.TextChunk", events[0])
	}
	if tc.Text != prompt {
		t.Errorf("TextChunk.Text = %q, want %q (echo)", tc.Text, prompt)
	}
	if tc.MessageID == "" {
		t.Error("TextChunk.MessageID is empty, want a non-empty minted id")
	}
	te, ok := events[1].(turnevent.TurnEnd)
	if !ok {
		t.Fatalf("event[1] = %T, want turnevent.TurnEnd", events[1])
	}
	if te.Reason != turnevent.TurnEndReasonEndTurn {
		t.Errorf("TurnEnd.Reason = %v, want %v", te.Reason, turnevent.TurnEndReasonEndTurn)
	}
}

// TestRunStreamJSON_MultipleTurns proves "one response per received turn" (AC2):
// two user-turn lines yield TextChunk, TurnEnd, TextChunk, TurnEnd in order, each
// text echoing its own turn, and the two assistant lines carry distinct minted
// ids (guards the per-turn counter).
func TestRunStreamJSON_MultipleTurns(t *testing.T) {
	t.Parallel()

	prompts := []string{"first turn", "second turn"}
	var in strings.Builder
	for _, p := range prompts {
		in.WriteString(userTurnLine(p) + "\n")
	}
	var buf bytes.Buffer
	runStreamJSON(strings.NewReader(in.String()), &buf)

	events := parseEmitted(t, buf.Bytes())
	if len(events) != 2*len(prompts) {
		t.Fatalf("got %d events, want %d: %+v", len(events), 2*len(prompts), events)
	}
	var ids []string
	for i, p := range prompts {
		tc, ok := events[i*2].(turnevent.TextChunk)
		if !ok {
			t.Fatalf("event[%d] = %T, want turnevent.TextChunk", i*2, events[i*2])
		}
		if tc.Text != p {
			t.Errorf("turn %d: TextChunk.Text = %q, want %q", i, tc.Text, p)
		}
		ids = append(ids, tc.MessageID)
		if _, ok := events[i*2+1].(turnevent.TurnEnd); !ok {
			t.Fatalf("event[%d] = %T, want turnevent.TurnEnd", i*2+1, events[i*2+1])
		}
	}
	if ids[0] == ids[1] {
		t.Errorf("assistant ids not distinct across turns: both %q", ids[0])
	}
}

// TestRunStreamJSON_NonUserLinesIgnored confirms a control_request interrupt line,
// a blank line, and an unparsable line each produce NO output — so interrupt
// handling is out of scope and a stray line can't fabricate a turn (AC2).
func TestRunStreamJSON_NonUserLinesIgnored(t *testing.T) {
	t.Parallel()

	const ctrl = `{"type":"control_request","request_id":"r1","request":{"subtype":"interrupt"}}`
	input := ctrl + "\n" + "\n" + "not json at all\n"
	var buf bytes.Buffer
	runStreamJSON(strings.NewReader(input), &buf)

	if buf.Len() != 0 {
		t.Fatalf("non-user lines produced %d bytes of output, want 0: %q", buf.Len(), buf.String())
	}
}

// TestWriteStreamResponse_Shape is a cheap direct check (no parser) that the two
// emitted lines carry the exact byte shape the daemon side asserts against
// (stream_turn_drain_test.go's assistantTextLine / resultLine): an assistant
// message with one text block, then a result{subtype:"success"}.
func TestWriteStreamResponse_Shape(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := writeStreamResponse(&buf, "m1", "echo me"); err != nil {
		t.Fatalf("writeStreamResponse: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), buf.String())
	}

	var asst struct {
		Type    string `json:"type"`
		Message struct {
			ID      string `json:"id"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &asst); err != nil {
		t.Fatalf("assistant line: %v", err)
	}
	if asst.Type != "assistant" {
		t.Errorf("assistant line type = %q, want %q", asst.Type, "assistant")
	}
	if asst.Message.ID != "m1" {
		t.Errorf("assistant id = %q, want %q", asst.Message.ID, "m1")
	}
	if len(asst.Message.Content) != 1 || asst.Message.Content[0].Type != "text" || asst.Message.Content[0].Text != "echo me" {
		t.Errorf("assistant content = %+v, want one text block %q", asst.Message.Content, "echo me")
	}

	var res struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &res); err != nil {
		t.Fatalf("result line: %v", err)
	}
	if res.Type != "result" || res.Subtype != "success" {
		t.Errorf("result line = {type:%q, subtype:%q}, want {result, success}", res.Type, res.Subtype)
	}
}
