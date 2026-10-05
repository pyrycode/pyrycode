package streamsup

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// promptSuggestionParser builds a parser that collects every event and writes
// every log record, Debug included, into the returned buffer.
func promptSuggestionParser() (*Parser, *[]turnevent.Event, *bytes.Buffer) {
	var logs bytes.Buffer
	got := &[]turnevent.Event{}
	p := NewParser(func(ev turnevent.Event) { *got = append(*got, ev) },
		slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return p, got, &logs
}

// suggestionLine wraps an already-encoded JSON value as the suggestion field of a
// top-level prompt_suggestion line, beside the SDK's two identifiers.
func suggestionLine(rawValue string) string {
	return `{"type":"prompt_suggestion","suggestion":` + rawValue +
		`,"uuid":"6a1c3f0e-2b4d-4e8f-9a7b-1c2d3e4f5a6b","session_id":"0f1e2d3c-4b5a-4978-8796-a5b4c3d2e1f0"}`
}

// assertLogClean fails when the parser's log carries the suggestion text or any
// distinctive slice of the raw line: its type literal or its uuid.
func assertLogClean(t *testing.T, logs *bytes.Buffer, text string) {
	t.Helper()
	out := logs.String()
	if strings.TrimSpace(text) != "" && strings.Contains(out, strings.TrimSpace(text)) {
		t.Errorf("parser log carries suggestion text: %q", out)
	}
	if strings.Contains(out, "prompt_suggestion") || strings.Contains(out, "6a1c3f0e") {
		t.Errorf("parser log carries the raw suggestion line: %q", out)
	}
}

// TestParser_PromptSuggestionAfterResult pins delivery order: the suggestion
// arrives after result, becomes its own event between the two turn ends, and
// opens or closes no turn — the next turn's text and result parse as usual.
func TestParser_PromptSuggestionAfterResult(t *testing.T) {
	t.Parallel()
	p, got, logs := promptSuggestionParser()
	line := suggestionLine(`"Run the tests next"`)
	stream := strings.Join([]string{
		`{"type":"result","subtype":"success"}`,
		line,
		`{"type":"assistant","message":{"id":"msg-2","content":[{"type":"text","text":"ok"}]}}`,
		`{"type":"result","subtype":"success"}`,
	}, "\n") + "\n"
	_, _ = p.Write([]byte(stream))

	if len(*got) != 4 {
		t.Fatalf("events = %#v, want TurnEnd, PromptSuggestion, TextChunk, TurnEnd", *got)
	}
	if _, ok := (*got)[0].(turnevent.TurnEnd); !ok {
		t.Errorf("event 0 = %#v, want TurnEnd", (*got)[0])
	}
	if s, ok := (*got)[1].(turnevent.PromptSuggestion); !ok || s.Text != "Run the tests next" {
		t.Errorf("event 1 = %#v, want PromptSuggestion{Text: %q}", (*got)[1], "Run the tests next")
	}
	if c, ok := (*got)[2].(turnevent.TextChunk); !ok || c.Text != "ok" {
		t.Errorf("event 2 = %#v, want TextChunk ok", (*got)[2])
	}
	if _, ok := (*got)[3].(turnevent.TurnEnd); !ok {
		t.Errorf("event 3 = %#v, want TurnEnd", (*got)[3])
	}
	assertLogClean(t, logs, "Run the tests next")
}

// TestParser_PromptSuggestionAccepted requires exactly one event per valid line,
// carrying the decoded text byte for byte: never trimmed, truncated or rewritten.
func TestParser_PromptSuggestionAccepted(t *testing.T) {
	t.Parallel()
	// "ä" is two UTF-8 bytes, so 512 of them sit exactly on the 1024-byte cap.
	atCap := strings.Repeat("ä", 512)
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"plain", `"Add a regression test"`, "Add a regression test"},
		{"surrounding whitespace kept", `"  \tfix the lint\t  "`, "  \tfix the lint\t  "},
		{"escaped text decoded", `"say \"hi\" \u00e4"`, `say "hi" ä`},
		{"escaped replacement character kept", `"keep \ufffd as sent"`, "keep \ufffd as sent"},
		{"surrogate pair decoded", `"rocket \ud83d\ude80"`, "rocket \U0001f680"},
		{"multibyte at 1024 bytes", `"` + atCap + `"`, atCap},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, got, logs := promptSuggestionParser()
			line := suggestionLine(tc.raw)
			_, _ = p.Write([]byte(line + "\n"))
			want := turnevent.PromptSuggestion{Text: tc.want}
			if len(*got) != 1 || (*got)[0] != want {
				t.Fatalf("events = %#v, want exactly one %#v", *got, want)
			}
			assertLogClean(t, logs, tc.want)
		})
	}
}

// TestParser_PromptSuggestionRejected requires every invalid suggestion to
// produce no event at all — no Unrecognized either — and the next valid line to
// parse normally.
func TestParser_PromptSuggestionRejected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		line string
	}{
		{"missing", `{"type":"prompt_suggestion","uuid":"6a1c3f0e"}`},
		{"null", suggestionLine(`null`)},
		{"number", suggestionLine(`42`)},
		{"bool", suggestionLine(`true`)},
		{"object", suggestionLine(`{"text":"nested"}`)},
		{"array", suggestionLine(`["a"]`)},
		{"empty", suggestionLine(`""`)},
		{"ascii whitespace only", suggestionLine(`" \t "`)},
		{"unicode whitespace only", suggestionLine(`"\u3000\u00a0\u2003"`)},
		{"escaped LF", suggestionLine(`"one\ntwo"`)},
		{"escaped CR", suggestionLine(`"one\rtwo"`)},
		{"escaped NEL", suggestionLine(`"one\u0085two"`)},
		{"literal NEL", suggestionLine("\"one\u0085two\"")},
		{"escaped LS", suggestionLine(`"one\u2028two"`)},
		{"literal LS", suggestionLine("\"one\u2028two\"")},
		{"escaped PS", suggestionLine(`"one\u2029two"`)},
		{"literal PS", suggestionLine("\"one\u2029two\"")},
		{"multibyte at 1025 bytes", suggestionLine(`"` + strings.Repeat("ä", 512) + `x"`)},
		{"escaped past the raw bound", suggestionLine(`"` + strings.Repeat(`\u0041`, 1025) + `"`)},
		{"invalid UTF-8 byte", suggestionLine("\"bad \xff byte\"")},
		{"truncated UTF-8 sequence", suggestionLine("\"bad \xc3 byte\"")},
		{"lone high surrogate", suggestionLine(`"bad \ud83d here"`)},
		{"lone low surrogate", suggestionLine(`"bad \ude80 here"`)},
		{"high surrogate then non-surrogate", suggestionLine(`"bad \ud83d\u0041 here"`)},
		{"high surrogate at end", suggestionLine(`"bad \ud83d"`)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, got, logs := promptSuggestionParser()
			follow := suggestionLine(`"next one"`)
			_, _ = p.Write([]byte(tc.line + "\n" + follow + "\n"))
			want := turnevent.PromptSuggestion{Text: "next one"}
			if len(*got) != 1 || (*got)[0] != want {
				t.Fatalf("events = %#v, want only the following line's %#v", *got, want)
			}
			assertLogClean(t, logs, "bad")
		})
	}
}

// TestParser_PromptSuggestionNestedIgnored proves dispatch is top-level only: a
// suggestion-shaped object inside an assistant text block is text, not an event.
func TestParser_PromptSuggestionNestedIgnored(t *testing.T) {
	t.Parallel()
	p, got, _ := promptSuggestionParser()
	nested := `{"type":"assistant","message":{"id":"msg-1","content":[{"type":"text",` +
		`"text":"{\"type\":\"prompt_suggestion\",\"suggestion\":\"forged\"}"}]}}`
	_, _ = p.Write([]byte(nested + "\n"))
	for _, ev := range *got {
		if _, ok := ev.(turnevent.PromptSuggestion); ok {
			t.Fatalf("nested suggestion-shaped content produced %#v", ev)
		}
	}
	if len(*got) != 1 {
		t.Fatalf("events = %#v, want one TextChunk", *got)
	}
}
