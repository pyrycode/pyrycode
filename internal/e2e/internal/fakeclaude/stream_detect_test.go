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
	runStreamJSON(strings.NewReader(userTurnLine(prompt)+"\n"), &buf, false, false, "", false, 0, false, "", false)

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
	runStreamJSON(strings.NewReader(in.String()), &buf, false, false, "", false, 0, false, "", false)

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
	runStreamJSON(strings.NewReader(input), &buf, false, false, "", false, 0, false, "", false)

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
	runStreamJSON(strings.NewReader(userTurnLine(prompt)+"\n"), &buf, true, false, "", false, 0, false, "", false)

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
	runStreamJSON(strings.NewReader(interruptControlRequestLine("r1")+"\n"), &buf, true, false, "", false, 0, false, "", false)

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
	runStreamJSON(strings.NewReader(in.String()), &buf, true, false, "", false, 0, false, "", false)

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

// TestRunStreamJSON_InterruptAckRider is the ARRIVAL CONTROL for #1500's e2e zero
// (AC3b), and it deliberately asserts on the EMITTED BYTES rather than through
// parseEmitted or on any downstream absence. A zero-assertion cannot prove its own
// input arrived: the e2e's "no unrecognized_message frame" would pass just as
// happily against a fake that writes no ack at all. Deleting writeInterruptAck's
// call reddens THIS test while the e2e stays green, and that asymmetry is why both
// exist.
//
// The line ORDER is part of the assertion, not incidental. The e2e's causality
// argument — turn_end reaching the phone proves the ack already went through the
// parser — rests entirely on the ack preceding the result, so it is pinned where it
// is made. It also matches real claude's order (~40ms ack, then the result).
//
// The envelope checked here is the capture's, per writeInterruptAck's doc: subtype
// and request_id UNDER response, not top-level. Written as a literal decode target
// rather than reusing the writer's map, for the same reason the rate-limit rows use
// literals — a target built from the producer would follow a nesting bug green.
func TestRunStreamJSON_InterruptAckRider(t *testing.T) {
	t.Parallel()

	// A distinctive id, so the echo assertion cannot pass on a value the fake could
	// have minted itself.
	const reqID = "e2e-1500-interrupt-req"
	var buf bytes.Buffer
	runStreamJSON(strings.NewReader(interruptControlRequestLine(reqID)+"\n"), &buf, true, false, "", false, 0, false, "", false)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("line count: got %d, want 2 (control_response ack, then the interrupted result)\n%s",
			len(lines), buf.String())
	}

	var ack struct {
		Type     string `json:"type"`
		Subtype  string `json:"subtype"`
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &ack); err != nil {
		t.Fatalf("unmarshal control_response line: %v\n%s", err, lines[0])
	}
	if ack.Type != "control_response" {
		t.Errorf("line 0 type: got %q, want %q — the ack must come FIRST", ack.Type, "control_response")
	}
	if ack.Response.Subtype != "success" {
		t.Errorf("response.subtype: got %q, want %q", ack.Response.Subtype, "success")
	}
	// The echo. A fake that dropped the id would still satisfy every other row here,
	// and would then be lying about the one field the capture shows claude echoing.
	if ack.Response.RequestID != reqID {
		t.Errorf("response.request_id: got %q, want %q (the daemon's own id, echoed)", ack.Response.RequestID, reqID)
	}
	// The nesting itself, asserted in the negative: the capture puts NEITHER field at
	// the top level, and a top-level subtype is exactly what would make streamsup's
	// streamLine.Subtype decode non-empty and send the daemon down an
	// emitSystemSubtype-shaped path that does not exist for this type.
	if ack.Subtype != "" {
		t.Errorf("top-level subtype: got %q, want empty — the capture nests subtype under response", ack.Subtype)
	}

	// The interrupted result must survive the rider, and must come SECOND.
	if !strings.Contains(lines[1], `"type":"result"`) || !strings.Contains(lines[1], `"subtype":"error_during_execution"`) {
		t.Errorf("line 1: got %s, want the result{error_during_execution}", lines[1])
	}
}

// TestWriteStreamResponse_Shape is a cheap direct check (no parser) that the two
// emitted lines carry the exact byte shape the daemon side asserts against
// (stream_turn_drain_test.go's assistantTextLine / resultLine): an assistant
// message with one text block, then a result{subtype:"success"}.
func TestWriteStreamResponse_Shape(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	// nil modelUsage is the default path (#2107): the result line must stay
	// byte-identical to the pre-rider wire, which the omitempty tag delivers and
	// the no-modelUsage assertion below pins.
	if err := writeStreamResponse(&buf, "m1", "echo me", nil); err != nil {
		t.Fatalf("writeStreamResponse: %v", err)
	}
	if strings.Contains(buf.String(), "modelUsage") {
		t.Errorf("result line carries a modelUsage key with a nil map: %q", buf.String())
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
	runStreamJSON(strings.NewReader(userTurnLine("hello")+"\n"), &buf, false, true, "", false, 0, false, "", false)

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
	runStreamJSON(strings.NewReader(userTurnLine("hi")+"\n"), &off, false, false, "", false, 0, false, "", false)
	runStreamJSON(strings.NewReader(userTurnLine("hi")+"\n"), &on, false, true, "", false, 0, false, "", false)

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

// TestRunStreamJSON_RateLimitRider pins the rate-limit rider at the cheapest tier:
// with a status set, one turn prepends exactly one rate_limit_event line carrying
// the captured rate_limit_info object with that status substituted, and the normal
// reply still arrives intact.
//
// Table-driven over the two statuses the hermetic e2e drives IS the point. The whole
// design of the knob rests on "the benign case and its arrival control differ in
// exactly one string"; this is where that claim is cheapest to check, and a row that
// produced a different shape for one of the two statuses would break the e2e pair's
// only argument for being a controlled comparison.
func TestRunStreamJSON_RateLimitRider(t *testing.T) {
	t.Parallel()

	// The e2e's two statuses: the captured measured-benign value the daemon's gate
	// answers with silence, and the synthetic non-benign control it answers with one
	// frame. Written as literals here for the same reason the e2e writes them as
	// literals — the fake must not import the daemon's unexported constant, so a
	// rename of streamsup.benignRateLimitStatus SHOULD show up as a red test.
	for _, tc := range []struct {
		name   string
		status string
	}{
		{name: "measured-benign status", status: "allowed"},
		{name: "non-benign control status", status: "e2e-not-allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			runStreamJSON(strings.NewReader(userTurnLine("hello")+"\n"), &buf, false, false, tc.status, false, 0, false, "", false)

			lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
			if len(lines) != 3 {
				t.Fatalf("line count: got %d, want 3 (rate_limit_event, echo, result)\n%s",
					len(lines), buf.String())
			}

			var got struct {
				Type string `json:"type"`
				Info struct {
					Status                string `json:"status"`
					ResetsAt              int64  `json:"resetsAt"`
					LimitType             string `json:"rateLimitType"`
					OverageStatus         string `json:"overageStatus"`
					OverageDisabledReason string `json:"overageDisabledReason"`
					IsUsingOverage        bool   `json:"isUsingOverage"`
				} `json:"rate_limit_info"`
				UUID      string `json:"uuid"`
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
				t.Fatalf("unmarshal rate_limit_event line: %v\n%s", err, lines[0])
			}
			if got.Type != "rate_limit_event" {
				t.Errorf("line type: got %q, want %q", got.Type, "rate_limit_event")
			}
			// Verbatim: the knob's value is the gate's sole discriminator, so the fake
			// must not normalise, trim, or fold it on the way through.
			if got.Info.Status != tc.status {
				t.Errorf("rate_limit_info.status: got %q, want %q (verbatim)", got.Info.Status, tc.status)
			}
			// The rest of the object is the capture's, identical across both rows.
			if got.Info.LimitType != rateLimitLimitType {
				t.Errorf("rateLimitType: got %q, want %q", got.Info.LimitType, rateLimitLimitType)
			}
			if got.Info.ResetsAt != rateLimitResetsAt {
				t.Errorf("resetsAt: got %d, want %d", got.Info.ResetsAt, rateLimitResetsAt)
			}
			// The three overage keys the daemon's decode target deliberately omits.
			// Carried so "match the capture" is literally true, and so the line feeds
			// the parser keys it must ignore.
			if got.Info.OverageStatus != "rejected" {
				t.Errorf("overageStatus: got %q, want %q", got.Info.OverageStatus, "rejected")
			}
			if got.Info.OverageDisabledReason != "org_level_disabled" {
				t.Errorf("overageDisabledReason: got %q, want %q",
					got.Info.OverageDisabledReason, "org_level_disabled")
			}
			if got.Info.IsUsingOverage {
				t.Error("isUsingOverage: got true, want false (the capture's value)")
			}
			// The envelope identifiers are the fake's own, not the capture's templated
			// placeholders. Neither reaches the gate.
			if got.UUID != rateLimitUUID {
				t.Errorf("uuid: got %q, want %q", got.UUID, rateLimitUUID)
			}
			if got.SessionID != streamSessionID {
				t.Errorf("session_id: got %q, want %q", got.SessionID, streamSessionID)
			}

			// The real reply must survive the rider untouched.
			if !strings.Contains(lines[1], `"text":"hello"`) {
				t.Errorf("assistant echo: got %s, want the prompt echoed", lines[1])
			}
			if !strings.Contains(lines[2], `"subtype":"success"`) {
				t.Errorf("result line: got %s, want subtype success", lines[2])
			}
		})
	}
}

// TestRunStreamJSON_RateLimitRiderOffIsByteIdentical pins that the rate-limit rider
// is default-off and additive: with an empty status, output is exactly the two lines
// the untouched path always wrote. Empty is the OFF value rather than a status the
// rider forwards, which is why the parser's "absent or empty rate_limit_info" rung is
// unreachable through this seam by construction.
func TestRunStreamJSON_RateLimitRiderOffIsByteIdentical(t *testing.T) {
	t.Parallel()

	var on, off bytes.Buffer
	runStreamJSON(strings.NewReader(userTurnLine("hi")+"\n"), &off, false, false, "", false, 0, false, "", false)
	runStreamJSON(strings.NewReader(userTurnLine("hi")+"\n"), &on, false, false, "allowed", false, 0, false, "", false)

	offLines := strings.Split(strings.TrimSpace(off.String()), "\n")
	onLines := strings.Split(strings.TrimSpace(on.String()), "\n")
	if len(offLines) != 2 {
		t.Fatalf("rider-off line count: got %d, want 2", len(offLines))
	}
	// The rider only PREPENDS one line; the tail must match the untouched output.
	if got, want := strings.Join(onLines[1:], "\n"), strings.Join(offLines, "\n"); got != want {
		t.Errorf("rider changed the normal reply:\n got %s\nwant %s", got, want)
	}
}

// TestRunStreamJSON_RosterRider pins the background-task-roster rider at the cheapest
// tier: with a positive count, one turn prepends exactly one
// system/background_tasks_changed line canning that many rows, and the normal reply
// still arrives intact.
//
// The row assertions are the fixture's PROVENANCE claim, not a shape check. Row 0 must
// be the committed capture's own bytes — `cat $FIFO` included, which is why a rider
// that ever grew a shell or an os.Expand would redden here rather than four layers
// downstream — and every later row must be visibly synthetic, so no reader mistakes a
// generated row for a measured one and no e2e assertion can pass on the wrong row.
func TestRunStreamJSON_RosterRider(t *testing.T) {
	t.Parallel()

	// Nine, the count #2080's e2e drives: over streamsup's eight-entry roster cap, so
	// the daemon's dropped_tasks is non-zero and the e2e's count assertion is a
	// pass-through claim rather than a comparison against a defaulted zero. The fake
	// writes all nine — the CUT is the daemon's, and reproducing it here would hide
	// the very thing the e2e measures.
	var buf bytes.Buffer
	runStreamJSON(strings.NewReader(userTurnLine("hello")+"\n"), &buf, false, false, "", false, 9, false, "", false)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("line count: got %d, want 3 (background_tasks_changed, echo, result)\n%s",
			len(lines), buf.String())
	}

	var got struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Tasks   []struct {
			TaskID      string `json:"task_id"`
			TaskType    string `json:"task_type"`
			Description string `json:"description"`
		} `json:"tasks"`
		UUID      string `json:"uuid"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("decode roster line: %v\n%s", err, lines[0])
	}
	if got.Type != "system" || got.Subtype != "background_tasks_changed" {
		t.Fatalf("line envelope = %q/%q, want system/background_tasks_changed", got.Type, got.Subtype)
	}
	if len(got.Tasks) != 9 {
		t.Fatalf("tasks: got %d rows, want the 9 the rider was asked for — the CAP is the daemon's, "+
			"not the fake's", len(got.Tasks))
	}
	// The capture's row, verbatim. A drift here means the fixture stopped being a
	// transcription and became an invention.
	if got.Tasks[0].TaskID != rosterCapturedTaskID || got.Tasks[0].TaskType != rosterCapturedTaskType {
		t.Errorf("tasks[0] = %q/%q, want the capture's %q/%q",
			got.Tasks[0].TaskID, got.Tasks[0].TaskType, rosterCapturedTaskID, rosterCapturedTaskType)
	}
	if got.Tasks[0].Description != rosterCapturedDescription {
		t.Errorf("tasks[0].description = %q, want the capture's %q — a literal command line, carried "+
			"as inert bytes and never shell-interpreted", got.Tasks[0].Description, rosterCapturedDescription)
	}
	// Every later row distinct and visibly synthetic: the e2e correlates rows by id,
	// so two rows sharing one would let an assertion pass on the wrong row.
	seen := map[string]bool{got.Tasks[0].TaskID: true}
	for i, row := range got.Tasks[1:] {
		if seen[row.TaskID] {
			t.Errorf("tasks[%d].task_id = %q, already used by an earlier row", i+1, row.TaskID)
		}
		seen[row.TaskID] = true
		if !strings.HasPrefix(row.TaskID, rosterSyntheticIDPrefix) {
			t.Errorf("tasks[%d].task_id = %q, want the synthetic prefix %q", i+1, row.TaskID, rosterSyntheticIDPrefix)
		}
	}
	// The two keys the parser's decode target deliberately does NOT declare. They are
	// on every real line, so the fake carries them: a retention that ever surfaced one
	// must fail against a fixture that supplies it, not against one that omits it.
	if got.UUID != rosterUUID || got.SessionID != streamSessionID {
		t.Errorf("envelope ids = %q/%q, want %q/%q", got.UUID, got.SessionID, rosterUUID, streamSessionID)
	}

	// The rider only PREPENDS; the tail must match the untouched output.
	var off bytes.Buffer
	runStreamJSON(strings.NewReader(userTurnLine("hello")+"\n"), &off, false, false, "", false, 0, false, "", false)
	offLines := strings.Split(strings.TrimSpace(off.String()), "\n")
	if len(offLines) != 2 {
		t.Fatalf("rider-off line count: got %d, want 2", len(offLines))
	}
	if gotTail, want := strings.Join(lines[1:], "\n"), strings.Join(offLines, "\n"); gotTail != want {
		t.Errorf("rider changed the normal reply:\n got %s\nwant %s", gotTail, want)
	}
}

// TestRunStreamJSON_RosterRiderOffIsByteIdentical pins that the roster rider is
// default-off and additive at every non-positive count. Zero is the OFF value, and a
// negative one — what strconv.Atoi cannot produce from a typo but a caller can pass
// directly — must be off too rather than looping into a panic or writing an empty
// roster, which would be a POSITIVE claim that nothing is alive.
func TestRunStreamJSON_RosterRiderOffIsByteIdentical(t *testing.T) {
	t.Parallel()

	var base bytes.Buffer
	runStreamJSON(strings.NewReader(userTurnLine("hi")+"\n"), &base, false, false, "", false, 0, false, "", false)
	want := strings.TrimSpace(base.String())
	if len(strings.Split(want, "\n")) != 2 {
		t.Fatalf("rider-off line count: got %d, want 2", len(strings.Split(want, "\n")))
	}

	for _, count := range []int{0, -1} {
		var buf bytes.Buffer
		runStreamJSON(strings.NewReader(userTurnLine("hi")+"\n"), &buf, false, false, "", false, count, false, "", false)
		if got := strings.TrimSpace(buf.String()); got != want {
			t.Errorf("rosterTasks=%d is not byte-identical to the untouched path:\n got %s\nwant %s",
				count, got, want)
		}
	}
}

// TestRunStreamJSON_ModelWindowRider covers the #2107 rider from both sides: off
// by default (the result line carries no modelUsage at all, so every existing
// spec's wire is unchanged), and on it carries the canned two-model map.
//
// The on-arm asserts the 1M entry by its KEY rather than by counting entries: the
// join downstream matches on that string, so the key is what a consumer actually
// needs, and an entry-count check would pass on a map whose keys had drifted.
// Since #2118 the key is the variant-suffixed spelling and differs from the base a
// transcript names, which is the whole reason it is asserted literally here.
func TestRunStreamJSON_ModelWindowRider(t *testing.T) {
	t.Parallel()

	// resultModelUsage returns the modelUsage object on the single result line in
	// out, or nil when the line carries no such key.
	resultModelUsage := func(t *testing.T, out string) map[string]map[string]any {
		t.Helper()
		for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			var decoded struct {
				Type       string                    `json:"type"`
				ModelUsage map[string]map[string]any `json:"modelUsage"`
			}
			if err := json.Unmarshal([]byte(line), &decoded); err != nil {
				t.Fatalf("decode emitted line %q: %v", line, err)
			}
			if decoded.Type == "result" {
				return decoded.ModelUsage
			}
		}
		t.Fatalf("no result line in output: %q", out)
		return nil
	}

	var off bytes.Buffer
	runStreamJSON(strings.NewReader(userTurnLine("hi")+"\n"), &off, false, false, "", false, 0, false, "", false)
	if got := resultModelUsage(t, off.String()); got != nil {
		t.Errorf("rider off: modelUsage = %v, want absent", got)
	}

	var on bytes.Buffer
	runStreamJSON(strings.NewReader(userTurnLine("hi")+"\n"), &on, false, false, "", false, 0, true, "", false)
	got := resultModelUsage(t, on.String())
	entry, ok := got[riderModelWindowSonnetKey]
	if !ok {
		t.Fatalf("rider on: modelUsage has no %q entry: %v", riderModelWindowSonnetKey, got)
	}
	// JSON numbers decode to float64 through an `any`; 1000000 is exactly
	// representable, so the comparison is safe.
	if entry["contextWindow"] != float64(1_000_000) {
		t.Errorf("rider on: %q contextWindow = %v, want 1000000", riderModelWindowSonnetKey, entry["contextWindow"])
	}
	if len(got) != 2 {
		t.Errorf("rider on: modelUsage has %d entries, want 2 — the two-models-at-two-sizes shape is what makes the "+
			"downstream join's answer discriminating: %v", len(got), got)
	}
}

// TestStreamJSON_ResetRiderAnnouncesOnceOnTheFirstTurn pins the #2135 rider: the
// announcement is one top-level conversation_reset line, carrying the two keys
// streamsup's emitConversationReset decodes and the caller's id, written BEFORE the
// echo so a turn_end reaching a client implies the announcement is already through
// the parser.
//
// The SECOND turn is what the test really buys. claude mounts one fresh transcript
// per reset, and a rider that re-announced would make "how many session_transition
// frames did the client see" depend on how many turns a spec happens to drive —
// turning the e2e's exactly-one assertion into a count of turns.
func TestStreamJSON_ResetRiderAnnouncesOnceOnTheFirstTurn(t *testing.T) {
	const announced = "0f1e2d3c-4b5a-4978-8796-a5b4c3d2e1f0"

	var buf bytes.Buffer
	runStreamJSON(strings.NewReader(userTurnLine("one")+"\n"+userTurnLine("two")+"\n"), &buf,
		false, false, "", false, 0, false, announced, false)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	var resets []string
	for _, line := range lines {
		var got struct {
			Type  string `json:"type"`
			NewID string `json:"new_conversation_id"`
		}
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		if got.Type == "conversation_reset" {
			resets = append(resets, got.NewID)
		}
	}
	if len(resets) != 1 {
		t.Fatalf("wrote %d conversation_reset lines over two turns, want exactly 1: %v\n%s",
			len(resets), resets, buf.String())
	}
	if resets[0] != announced {
		t.Errorf("new_conversation_id = %q, want the caller's %q", resets[0], announced)
	}
	// Off by default: an unset rider must leave the stream byte-identical, so every
	// sibling spec that does not opt in is unaffected.
	var off bytes.Buffer
	runStreamJSON(strings.NewReader(userTurnLine("one")+"\n"), &off, false, false, "", false, 0, false, "", false)
	if strings.Contains(off.String(), "conversation_reset") {
		t.Errorf("an unset rider still announced a reset:\n%s", off.String())
	}
}

// TestRunStreamJSON_SessionFactsRider pins the init rider at the cheapest tier: with
// the rider on, one turn prepends exactly one system/init line carrying the captured
// key set, and the normal reply still arrives intact.
//
// THE KEY COUNT IS THE ASSERTION THIS TEST EXISTS FOR. writeSystemInitLine's whole
// argument — interruptMarkerLine's, inherited — is that a presence among twenty-four
// keys is a different claim from a presence among three, because twenty-one of those
// keys are absent from the daemon's decode target and feeding them is what shows the
// frame cannot carry one. A later hand trimming the fixture down to the three keys the
// daemon reads would leave the e2e green and quietly delete that claim; it reddens
// here instead.
func TestRunStreamJSON_SessionFactsRider(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	runStreamJSON(strings.NewReader(userTurnLine("hello")+"\n"), &buf, false, false, "", false, 0, false, "", true)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("line count: got %d, want 3 (system/init, echo, result)\n%s", len(lines), buf.String())
	}

	// Decoded twice, and both readings are load-bearing. The map answers "which keys
	// are on the line", which no struct can; the struct answers "what are the values",
	// which a map/any comparison would answer only through type assertions.
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &keyed); err != nil {
		t.Fatalf("unmarshal init line as an object: %v\n%s", err, lines[0])
	}
	if len(keyed) != 24 {
		t.Errorf("init line top-level key count: got %d, want 24 (the capture's full set — see "+
			"writeSystemInitLine for why the extra twenty-one are the point)\n%s", len(keyed), lines[0])
	}

	var got struct {
		Type              string `json:"type"`
		Subtype           string `json:"subtype"`
		ClaudeCodeVersion string `json:"claude_code_version"`
		PermissionMode    string `json:"permissionMode"`
		Model             string `json:"model"`
		CWD               string `json:"cwd"`
		SessionID         string `json:"session_id"`
		UUID              string `json:"uuid"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal init line: %v\n%s", err, lines[0])
	}
	if got.Type != "system" || got.Subtype != "init" {
		t.Fatalf("line routing keys: got type=%q subtype=%q, want system/init — anything else and the "+
			"daemon routes the line to a different arm entirely", got.Type, got.Subtype)
	}
	// The two the daemon publishes, verbatim: no normalising, no zero-padding, no
	// parse into a semver triple.
	if got.ClaudeCodeVersion != initLineClaudeCodeVersion {
		t.Errorf("claude_code_version: got %q, want %q (the capture's, verbatim)",
			got.ClaudeCodeVersion, initLineClaudeCodeVersion)
	}
	// claude's camelCase spelling. The daemon's snake_case permission_mode belongs on
	// the WIRE, and a fake that wrote it here would feed the parser a key its decode
	// target does not declare — the line would decode, the posture would be empty, and
	// the e2e would fail four layers away from the cause.
	if got.PermissionMode != initLinePermissionMode {
		t.Errorf("permissionMode: got %q, want %q (claude's own spelling)",
			got.PermissionMode, initLinePermissionMode)
	}
	// Carried because the capture carries it, and because it is what makes the line
	// produce a model_announced frame beside the session_facts one — the two-events
	// fan-out runStreamJSON's doc warns a consumer about.
	if got.Model != initLineModel {
		t.Errorf("model: got %q, want %q", got.Model, initLineModel)
	}
	// The identity values are the fake's own substitutes, not the capture's templated
	// placeholders. A `$WORKDIR` surviving here would mean something expanded — or
	// failed to — where writeSystemInitLine promises a plain json.Marshal.
	if got.CWD != initLineCWD {
		t.Errorf("cwd: got %q, want %q (the synthetic substitute)", got.CWD, initLineCWD)
	}
	if got.SessionID != streamSessionID {
		t.Errorf("session_id: got %q, want %q", got.SessionID, streamSessionID)
	}
	if got.UUID != initLineUUID {
		t.Errorf("uuid: got %q, want %q", got.UUID, initLineUUID)
	}

	// The real reply must survive the rider untouched.
	if !strings.Contains(lines[1], `"text":"hello"`) {
		t.Errorf("assistant echo: got %s, want the prompt echoed", lines[1])
	}
	if !strings.Contains(lines[2], `"subtype":"success"`) {
		t.Errorf("result line: got %s, want subtype success", lines[2])
	}
}

// TestRunStreamJSON_SessionFactsRiderOffIsByteIdentical pins that the init rider is
// default-off and additive: with emitInit false, output is exactly the two lines the
// untouched path always wrote, and the rider only PREPENDS.
//
// This is what lets the e2e's rider-off half be a controlled comparison rather than a
// different experiment: every fake-daemon suite that does not opt in sees an unchanged
// stream, so a session_facts frame arriving in one of them would be the rider's doing
// and nothing else's.
func TestRunStreamJSON_SessionFactsRiderOffIsByteIdentical(t *testing.T) {
	t.Parallel()

	var on, off bytes.Buffer
	runStreamJSON(strings.NewReader(userTurnLine("hi")+"\n"), &off, false, false, "", false, 0, false, "", false)
	runStreamJSON(strings.NewReader(userTurnLine("hi")+"\n"), &on, false, false, "", false, 0, false, "", true)

	offLines := strings.Split(strings.TrimSpace(off.String()), "\n")
	onLines := strings.Split(strings.TrimSpace(on.String()), "\n")
	if len(offLines) != 2 {
		t.Fatalf("rider-off line count: got %d, want 2\n%s", len(offLines), off.String())
	}
	if strings.Contains(off.String(), `"subtype":"init"`) {
		t.Errorf("an unset rider still wrote an init line:\n%s", off.String())
	}
	if got, want := strings.Join(onLines[1:], "\n"), strings.Join(offLines, "\n"); got != want {
		t.Errorf("rider changed the normal reply:\n got %s\nwant %s", got, want)
	}
}
