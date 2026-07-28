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
	runStreamJSON(strings.NewReader(userTurnLine(prompt)+"\n"), &buf, false, false)

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
	runStreamJSON(strings.NewReader(in.String()), &buf, false, false)

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
	runStreamJSON(strings.NewReader(input), &buf, false, false)

	if buf.Len() != 0 {
		t.Fatalf("non-user lines produced %d bytes of output, want 0: %q", buf.Len(), buf.String())
	}
}

// interruptControlRequestLine hand-mirrors the inbound interrupt control_request the
// daemon (internal/streamsup/envelope.go marshalInterruptEnvelope) writes to
// claude's stdin on a phone interrupt. Hand-written (not imported —
// streamsup.controlRequest is unexported), same discipline as userTurnLine; the
// OUTPUT side is checked below through the real streamsup.Parser (different fabric).
func interruptControlRequestLine(requestID string) string {
	return fmt.Sprintf(`{"type":"control_request","request_id":%q,"request":{"subtype":"interrupt"}}`, requestID)
}

// TestRunStreamJSON_InterruptMode_UserTurnStaysInFlight proves the interrupt mode
// (runStreamJSON honorInterrupt=true) withholds the result on a user turn: the real
// parser maps the emitted stdout to exactly ONE TextChunk (the echo) and NO TurnEnd,
// so the turn stays open until an interrupt arrives.
func TestRunStreamJSON_InterruptMode_UserTurnStaysInFlight(t *testing.T) {
	t.Parallel()

	const prompt = "in-flight over stream-json"
	var buf bytes.Buffer
	runStreamJSON(strings.NewReader(userTurnLine(prompt)+"\n"), &buf, true, false)

	events := parseEmitted(t, buf.Bytes())
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (result withheld, no TurnEnd): %+v", len(events), events)
	}
	tc, ok := events[0].(turnevent.TextChunk)
	if !ok {
		t.Fatalf("event[0] = %T, want turnevent.TextChunk", events[0])
	}
	if tc.Text != prompt {
		t.Errorf("TextChunk.Text = %q, want %q (echo)", tc.Text, prompt)
	}
}

// TestRunStreamJSON_InterruptMode_InterruptEndsTurnCancelled proves an interrupt
// control_request in interrupt mode emits a result{error_during_execution} that the
// real parser maps to exactly ONE TurnEnd{Cancelled} — the interrupt→cancelled
// classification end-to-end at the seam.
func TestRunStreamJSON_InterruptMode_InterruptEndsTurnCancelled(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	runStreamJSON(strings.NewReader(interruptControlRequestLine("r1")+"\n"), &buf, true, false)

	events := parseEmitted(t, buf.Bytes())
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (TurnEnd only): %+v", len(events), events)
	}
	te, ok := events[0].(turnevent.TurnEnd)
	if !ok {
		t.Fatalf("event[0] = %T, want turnevent.TurnEnd", events[0])
	}
	if te.Reason != turnevent.TurnEndReasonCancelled {
		t.Errorf("TurnEnd.Reason = %v, want %v", te.Reason, turnevent.TurnEndReasonCancelled)
	}
}

// TestRunStreamJSON_InterruptMode_InFlightThenInterrupt is the unit analogue of the
// e2e: a user line then an interrupt control_request yield TextChunk (echo) then
// TurnEnd{Cancelled}, in order, through the real parser.
func TestRunStreamJSON_InterruptMode_InFlightThenInterrupt(t *testing.T) {
	t.Parallel()

	const prompt = "e2e-1136 in-flight"
	var in strings.Builder
	in.WriteString(userTurnLine(prompt) + "\n")
	in.WriteString(interruptControlRequestLine("r1") + "\n")
	var buf bytes.Buffer
	runStreamJSON(strings.NewReader(in.String()), &buf, true, false)

	events := parseEmitted(t, buf.Bytes())
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (TextChunk then TurnEnd{Cancelled}): %+v", len(events), events)
	}
	tc, ok := events[0].(turnevent.TextChunk)
	if !ok {
		t.Fatalf("event[0] = %T, want turnevent.TextChunk", events[0])
	}
	if tc.Text != prompt {
		t.Errorf("TextChunk.Text = %q, want %q (echo)", tc.Text, prompt)
	}
	te, ok := events[1].(turnevent.TurnEnd)
	if !ok {
		t.Fatalf("event[1] = %T, want turnevent.TurnEnd", events[1])
	}
	if te.Reason != turnevent.TurnEndReasonCancelled {
		t.Errorf("TurnEnd.Reason = %v, want %v", te.Reason, turnevent.TurnEndReasonCancelled)
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

// TestRunStreamJSON_BogusRider pins the bogus rider: with it on, one turn emits
// the two shapes the daemon's parser has no mapping for — an invented top-level
// type and an invented assistant block type — ahead of the normal reply, and the
// normal reply still arrives intact.
func TestRunStreamJSON_BogusRider(t *testing.T) {
	var buf bytes.Buffer
	runStreamJSON(strings.NewReader(userTurnLine("hello")+"\n"), &buf, false, true)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("line count: got %d, want 4 (bogus line, bogus block, echo, result)\n%s",
			len(lines), buf.String())
	}

	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("unmarshal bogus line: %v", err)
	}
	if first["type"] != bogusLineType {
		t.Errorf("bogus line type: got %v, want %q", first["type"], bogusLineType)
	}
	if first["detail"] != bogusLineNeedle {
		t.Errorf("bogus line needle: got %v, want %q", first["detail"], bogusLineNeedle)
	}

	if !strings.Contains(lines[1], bogusBlockType) || !strings.Contains(lines[1], bogusBlockNeedle) {
		t.Errorf("bogus block line missing type/needle: %s", lines[1])
	}

	// The real reply must survive the rider untouched.
	if !strings.Contains(lines[2], `"text":"hello"`) {
		t.Errorf("assistant echo: got %s, want the prompt echoed", lines[2])
	}
	if !strings.Contains(lines[3], `"subtype":"success"`) {
		t.Errorf("result line: got %s, want subtype success", lines[3])
	}
}

// TestRunStreamJSON_BogusRiderOffIsByteIdentical pins that the rider is
// default-off and additive: with it off, output is exactly the two lines the
// untouched path always wrote.
func TestRunStreamJSON_BogusRiderOffIsByteIdentical(t *testing.T) {
	var on, off bytes.Buffer
	runStreamJSON(strings.NewReader(userTurnLine("hi")+"\n"), &off, false, false)
	runStreamJSON(strings.NewReader(userTurnLine("hi")+"\n"), &on, false, true)

	offLines := strings.Split(strings.TrimSpace(off.String()), "\n")
	onLines := strings.Split(strings.TrimSpace(on.String()), "\n")
	if len(offLines) != 2 {
		t.Fatalf("rider-off line count: got %d, want 2", len(offLines))
	}
	// The rider only PREPENDS; the tail must match the untouched output.
	if got, want := strings.Join(onLines[2:], "\n"), strings.Join(offLines, "\n"); got != want {
		t.Errorf("rider changed the normal reply:\n got %s\nwant %s", got, want)
	}
}
