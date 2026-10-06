package streamsup

import (
	"log/slog"
	"maps"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// These identifiers are placeholders for the two reported peer-message commands;
// only one original raw frame was supplied in the refinement evidence.
func commandLifecycleLine(command, stateField string) string {
	line := `{"type":"command_lifecycle","command_uuid":"private-command-` + command +
		`","uuid":"private-frame-` + command + `","session_id":"private-session-` + command + `"`
	if stateField != "" {
		line += "," + stateField
	}
	return line + "}"
}

func assertCommandLifecycleLog(t *testing.T, rec *logRecorder, msg string, attrs map[string]string) {
	t.Helper()
	logs := rec.all()
	if len(logs) != 1 {
		t.Fatalf("logs = %#v, want exactly one record", logs)
	}
	if logs[0].level != slog.LevelDebug || logs[0].msg != msg || !reflect.DeepEqual(logs[0].attrs, attrs) {
		t.Errorf("log = %#v, want Debug %q with exactly %#v", logs[0], msg, attrs)
	}
}

func TestParser_CommandLifecycleKnownStates(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"queued", "started", "completed", "cancelled", "discarded", "refused"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			var got []turnevent.Event
			rec := &logRecorder{}
			p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, slog.New(rec))
			line := commandLifecycleLine("one", `"state":`+strconv.Quote(state))
			if _, err := p.Write([]byte(line + "\n")); err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Errorf("events = %#v, want none", got)
			}
			assertCommandLifecycleLog(t, rec, "streamsup: dropping command_lifecycle", map[string]string{
				"site": "line_type", "type": "command_lifecycle", "state": state,
			})
		})
	}
}

func TestParser_CommandLifecycleRejectedStates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		field string
	}{
		{"missing", ""},
		{"null", `"state":null`},
		{"number", `"state":42`},
		{"boolean", `"state":true`},
		{"object", `"state":{"private-payload":"started"}`},
		{"array", `"state":["started"]`},
		{"empty", `"state":""`},
		{"unknown", `"state":"private-unknown-state"`},
		{"wrong case", `"state":"Started"`},
		{"whitespace", `"state":" started "`},
		{"nested only", `"payload":{"state":"started"}`},
		{"oversized", `"state":"private-unknown-state","private-payload":"` + strings.Repeat("x", maxUnrecognizedRaw*2) + `"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []turnevent.Event
			rec := &logRecorder{}
			p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, slog.New(rec))
			line := commandLifecycleLine("one", tc.field)
			if _, err := p.Write([]byte(line + "\n")); err != nil {
				t.Fatal(err)
			}
			raw, truncated := line, len(line) > maxUnrecognizedRaw
			if truncated {
				raw = line[:maxUnrecognizedRaw]
			}
			want := []turnevent.Event{turnevent.Unrecognized{
				Site: turnevent.UnrecognizedLineType, Kind: "command_lifecycle", Raw: raw, Truncated: truncated,
			}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("events = %#v, want %#v", got, want)
			}
			assertCommandLifecycleLog(t, rec, "streamsup: unrecognized payload", map[string]string{
				"site": "line_type", "type": "command_lifecycle",
				"bytes": strconv.Itoa(len(line)), "truncated": strconv.FormatBool(truncated),
			})
		})
	}
}

// Copy maps so an in-place edit cannot change both sides of the assertion.
func commandLifecycleParserState(p *Parser) []any {
	return []any{
		p.thinkingSinceEmit, p.assistantErrorCategory, p.compacting,
		maps.Clone(p.deniedThisTurn), maps.Clone(p.taskProgressToolUses),
		p.rateLimitNonBenign, p.apiRetryOpen,
		p.streamMessageID, p.streamBlockIndex, p.streamBlockType, p.streamBlockOpen, p.streamBlockTextDelta,
	}
}

func TestParser_CommandLifecycleStateNeutral(t *testing.T) {
	t.Parallel()
	const text = `{"type":"assistant","error":"rate_limit","message":{"id":"msg-one","content":[{"type":"text","text":"hello"}]}}`
	const result = `{"type":"result","subtype":"success"}`
	open := strings.Join([]string{
		text,
		`{"type":"system","subtype":"thinking_tokens","estimated_tokens_delta":1}`,
		`{"type":"system","subtype":"status","status":"compacting"}`,
		`{"type":"system","subtype":"api_retry","attempt":1,"max_retries":3,"retry_delay_ms":1000}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"partial-one"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`,
	}, "\n") + "\n"
	for _, tc := range []struct {
		name  string
		setup string
	}{
		{"idle", ""},
		{"open turn", open},
		{"between turns", open + result + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, state := range []string{"queued", "started", "completed", "cancelled", "discarded", "refused"} {
				t.Run(state, func(t *testing.T) {
					t.Parallel()
					var got, controlEvents []turnevent.Event
					p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, discardLogger())
					control := NewParser(func(ev turnevent.Event) { controlEvents = append(controlEvents, ev) }, discardLogger())
					_, _ = p.Write([]byte(tc.setup))
					_, _ = control.Write([]byte(tc.setup))
					before := commandLifecycleParserState(p)
					count := len(got)
					_, _ = p.Write([]byte(commandLifecycleLine("one", `"state":`+strconv.Quote(state)) + "\n"))
					if len(got) != count || !reflect.DeepEqual(commandLifecycleParserState(p), before) {
						t.Fatalf("lifecycle frame changed events or parser state: events=%#v, state=%#v, before=%#v",
							got[count:], commandLifecycleParserState(p), before)
					}
					follow := result + "\n" + text + "\n" + result + "\n"
					_, _ = p.Write([]byte(follow))
					_, _ = control.Write([]byte(follow))
					if !reflect.DeepEqual(got, controlEvents) {
						t.Errorf("events = %#v, want unchanged normal events %#v", got, controlEvents)
					}
				})
			}
		})
	}
}

func TestParser_CommandLifecyclePeerMessagePairs(t *testing.T) {
	t.Parallel()
	var got []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { got = append(got, ev) }, discardLogger())
	var normal []string
	for _, command := range []string{"one", "two"} {
		text := `{"type":"assistant","message":{"id":"msg-` + command + `","content":[{"type":"text","text":"peer message"}]}}`
		const result = `{"type":"result","subtype":"success"}`
		stream := strings.Join([]string{
			commandLifecycleLine(command, `"state":"started"`), text, result,
			commandLifecycleLine(command, `"state":"completed"`),
		}, "\n") + "\n"
		_, _ = p.Write([]byte(stream))
		normal = append(normal, text, result)
	}
	want := collectEvents(strings.Join(normal, "\n"))
	if len(want) != 4 || !reflect.DeepEqual(got, want) {
		t.Errorf("events = %#v, want only the two normal turns %#v", got, want)
	}
}
