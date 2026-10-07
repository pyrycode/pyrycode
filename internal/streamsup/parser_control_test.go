package streamsup

import (
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// controlResponseConsumeMsgFixture is the Debug message the control_response arm
// emits, as a LITERAL for harnessNudgeFixture's reason: a fixture built from the
// production string would follow a rewording of it green, and the message is the
// only trace a future ack investigation has to start from.
const controlResponseConsumeMsgFixture = "streamsup: consuming solicited control_response"

// controlResponseRequestIDFixture is the correlation id every ack row below
// carries. Distinctive rather than realistic, because its job is to be searched
// for: the content-free assertion sweeps every emitted record for these bytes, and
// a value like "r1" would collide with unrelated text and pass by accident.
const controlResponseRequestIDFixture = "interrupt-1500-fixture-id"

// TestParser_ControlResponseAckIsConsumedSilently is #1500's proof, and it is one
// table on purpose: the ack rows and the still-alarms row assert opposite things
// about the same switch, so the pair is what separates "the ack is consumed" from
// "unknown types stopped surfacing". Reverting the arm reddens the ack rows alone;
// widening it to drop unknown types generally reddens the last row alone.
//
// The rows are NOT in TestParser_IgnoredLineTypesStaySilent even though the visible
// outcome matches, for the reason #1404 recorded when it moved rate_limit_event's
// row out of that table: a row there asserts LIST MEMBERSHIP, and this type has
// none. Its silence comes from its own case arm in consumeLine.
//
// Shape provenance: the ack envelope is transcribed from the verbatim capture in
// docs/knowledge/features/set-permission-mode-inband-probe.md — `subtype` and
// `request_id` nested UNDER `response`, which inverts the request side, where
// marshalInterruptEnvelope puts request_id top-level. The capture is a
// set_permission_mode ack, not an interrupt one, so what it establishes is the
// control channel's ENVELOPE; the inner response payload is request-specific and
// unmeasured for interrupt, which is why one row carries none and one carries a
// plausible one and both must be answered identically.
//
// CORRECTED 2026-08-26 (#1811): the rows still assert that no ack produces an
// EVENT, which is the property #1500 bought and this ticket does not touch. What
// changed is the record: each row now names the daemon-authored `reason` its rung
// logs, and the NAK row is no longer "consumed indistinguishably" — it is consumed
// with no event, and distinguished in the log. The rows keep asserting the ATTRS
// EXACTLY, so the content-free rule still fails here the day a value is appended.
func TestParser_ControlResponseAckIsConsumedSilently(t *testing.T) {
	t.Parallel()
	const ackID = controlResponseRequestIDFixture
	tests := []struct {
		name string
		line string
		// wantKind empty means the line must produce NO event at all and exactly one
		// consume record carrying wantReason; non-empty means exactly one Unrecognized
		// carrying it, and NO consume record.
		wantKind   string
		wantReason string
		why        string
	}{
		{
			name:       "the interrupt ack, in the captured envelope",
			line:       `{"type":"control_response","response":{"subtype":"success","request_id":"` + ackID + `"}}`,
			wantReason: "ack",
			why:        "the daemon solicited this line itself; surfacing it puts a false parser-gap alarm on every interrupt",
		},
		{
			name:       "an ack carrying an inner response payload",
			line:       `{"type":"control_response","response":{"subtype":"success","request_id":"` + ackID + `","response":{"mode":"default"}}}`,
			wantReason: "ack",
			why:        "the set_permission_mode success, verbatim from the probe capture: an inner payload carrying NEITHER array is still an ack, which is what that keyword narrowed to in #1890 — a payload carrying commands and no models is its own rung now",
		},
		{
			name:       "a NAK, consumed with no event and named in the log (#1811)",
			line:       `{"type":"control_response","response":{"subtype":"error","request_id":"` + ackID + `","error":"Cannot set permission mode to bypassPermissions because the session was not launched with --dangerously-skip-permissions"}}`,
			wantReason: "nak",
			why:        "a response reporting failure carries no payload the daemon reads, and #1500's gap — a NAK logged identically to a success — closed when the decode target it was traded against arrived",
		},
		{
			name:       "an ack with no response object at all",
			line:       `{"type":"control_response"}`,
			wantReason: "nak",
			why:        "an absent wrapper leaves the subtype empty, which is not success, so the total classification lands it on the nak rung rather than falling through",
		},
		{
			name:     "THE DISCRIMINATOR: a genuinely new top-level type still surfaces",
			line:     `{"type":"a_type_invented_next_year","payload":{"anything":1}}`,
			wantKind: "a_type_invented_next_year",
			why:      "the alarm must not be widened to buy the silence above — this is the one frame whose value is meaning \"claude started emitting something new\"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := &logRecorder{}
			var events []turnevent.Event
			p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
			if _, err := p.Write([]byte(tt.line + "\n")); err != nil {
				t.Fatalf("Write err = %v, want nil", err)
			}

			consumes := rec.withMessage(controlResponseConsumeMsgFixture)
			if tt.wantKind == "" {
				if len(events) != 0 {
					t.Errorf("event count: got %d, want 0 (%s) — %#v", len(events), tt.why, events)
				}
				if len(consumes) != 1 {
					t.Fatalf("records with message %q: got %d, want 1 (%s) — all records: %+v",
						controlResponseConsumeMsgFixture, len(consumes), tt.why, rec.all())
				}
				// Type, a daemon keyword and two counts. Never the line, never the
				// request_id, never the NAK's error string — the package's standing
				// content-free logging rule, held here by the same exactly-these-attrs
				// shape the harness-nudge and rate-limit drops use. Every count is 0
				// on every row here: none of them reaches EITHER emitting rung — since
				// #1891 there are two and no definite article picks one out — none
				// carries a commands array, and the attribute set is fixed rather than
				// per-rung.
				wantAttrs := map[string]string{
					"type":             "control_response",
					"reason":           tt.wantReason,
					"models":           "0",
					"dropped":          "0",
					"levels_dropped":   "0",
					"commands":         "0",
					"commands_dropped": "0",
				}
				if !reflect.DeepEqual(consumes[0].attrs, wantAttrs) {
					t.Errorf("consume attrs: got %v, want exactly %v", consumes[0].attrs, wantAttrs)
				}
			} else {
				if len(events) != 1 {
					t.Fatalf("event count: got %d, want 1 (%s) — %#v", len(events), tt.why, events)
				}
				un, ok := events[0].(turnevent.Unrecognized)
				if !ok {
					t.Fatalf("event[0] = %T, want turnevent.Unrecognized (%s)", events[0], tt.why)
				}
				if un.Kind != tt.wantKind {
					t.Errorf("Unrecognized.Kind: got %q, want %q", un.Kind, tt.wantKind)
				}
				if un.Site != turnevent.UnrecognizedLineType {
					t.Errorf("Unrecognized.Site: got %q, want %q", un.Site, turnevent.UnrecognizedLineType)
				}
				if len(consumes) != 0 {
					t.Errorf("the unrecognized path logged %d consume record(s), want 0: %+v", len(consumes), consumes)
				}
			}

			// Sweeps EVERY record the line produced, not only the expected one: the
			// realistic way this rule breaks later is someone appending "line", line or
			// "request_id" to some record on the same path. The Unrecognized row is
			// swept too — its EVENT carries the raw line by design, but no LOG record
			// should.
			for _, r := range rec.all() {
				if strings.Contains(r.msg, ackID) {
					t.Errorf("record message carries the request_id: %q", r.msg)
				}
				for k, v := range r.attrs {
					if strings.Contains(v, ackID) {
						t.Errorf("record %q attr %q carries the request_id; the arm logs the type only", r.msg, k)
					}
					if strings.Contains(v, `"response"`) {
						t.Errorf("record %q attr %q carries the line's bytes; the arm logs the type only", r.msg, k)
					}
				}
			}
		})
	}
}
