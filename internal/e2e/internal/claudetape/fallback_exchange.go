package claudetape

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// FallbackExchange is the exact bounded input production gives its finite
// suggestion worker. The source request and the final main-agent message must
// both come from one completed turn of one owned provider tape.
type FallbackExchange struct {
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}

// FallbackExchanges derives candidates through the production stream parser.
// Nested-agent prose, thinking, prior tool-call messages and failed/cancelled
// turns cannot lend text to a worker. Overlapping user requests are deliberately
// unsupported: a recorder must add an explicit attribution rule before using it.
func FallbackExchanges(tape *Tape) (map[int]FallbackExchange, error) {
	fail := errors.New("unproved fallback exchange dependency")
	if tape == nil || tape.Validate() != nil {
		return nil, fail
	}
	result := map[int]FallbackExchange{}
	sequence := 0
	active, bad := false, false
	user, messageID := "", ""
	var assistant strings.Builder
	parser := streamsup.NewParser(func(event turnevent.Event) {
		if !active {
			return
		}
		switch ev := event.(type) {
		case turnevent.TextChunk:
			if ev.ParentToolCallID != "" {
				return
			}
			if messageID != ev.MessageID {
				messageID = ev.MessageID
				assistant.Reset()
			}
			if len(ev.Text) > MaxBytes-assistant.Len() {
				bad = true
				return
			}
			assistant.WriteString(ev.Text)
		case turnevent.TurnEnd:
			active = false
			if ev.Reason == turnevent.TurnEndReasonEndTurn && ev.Outcome == "success" && !ev.IsError && strings.TrimSpace(assistant.String()) != "" {
				result[sequence] = FallbackExchange{fallbackHead(user, 8192), fallbackAssistant(assistant.String())}
			}
		}
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, frame := range tape.Frames {
		if (frame.Direction == "stdin" || frame.Direction == "stdout") && safeCLIJSON(frame.Data) != nil {
			return nil, fail
		}
		if frame.Direction == "stdin" {
			env, err := object(frame.Data)
			if err != nil {
				return nil, fail
			}
			if env["type"] != "user" {
				continue
			}
			if active {
				return nil, fail
			}
			// These profiles accept one plain text block only. Attachments, multiple
			// blocks and spontaneous/mid-turn input need their own reviewed derivation.
			var request struct {
				Type    string `json:"type"`
				Message struct {
					Role    string `json:"role"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"message"`
			}
			decoder := json.NewDecoder(bytes.NewReader(frame.Data))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Type != "user" || request.Message.Role != "user" || len(request.Message.Content) != 1 || request.Message.Content[0].Type != "text" || request.Message.Content[0].Text == "" {
				return nil, fail
			}
			user = request.Message.Content[0].Text
			sequence++
			active = true
			messageID = ""
			assistant.Reset()
		} else if frame.Direction == "stdout" {
			var compact bytes.Buffer
			if json.Compact(&compact, frame.Data) != nil {
				return nil, fail
			}
			if _, err := parser.Write(append(compact.Bytes(), '\n')); err != nil {
				return nil, fail
			}
		}
	}
	if bad {
		return nil, fail
	}
	return result, nil
}

// These byte limits mirror cmd/pyry's replyExchangePrefix/Assistant policy.
// Source review and fresh capture must check those production functions when
// promoting a worker profile. Root inventory fingerprints cover realclaude
// helpers; the literal actual worker input proves observable policy agreement.
// Invalid UTF-8 is never a valid source edge.
func fallbackHead(text string, n int) string {
	if !utf8.ValidString(text) {
		return ""
	}
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return strings.Clone(text[:n])
}
func fallbackTail(text string, n int) string {
	if !utf8.ValidString(text) {
		return ""
	}
	if len(text) <= n {
		return text
	}
	start := len(text) - n
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return strings.Clone(text[start:])
}
func fallbackAssistant(text string) string {
	if len(text) <= 8192 {
		return text
	}
	const omitted = "\n\n[middle of the reply omitted]\n\n"
	head := fallbackHead(text, 4096)
	return head + omitted + fallbackTail(text, 8192-len(head)-len(omitted))
}

// ValidateFallbackInput keeps the full literal worker input in a fixture and
// replay. It proves only the source dependency; it performs no substitution.
func ValidateFallbackInput(raw []byte, source FallbackExchange) error {
	fail := errors.New("fallback input is not its owned completed exchange")
	if len(raw) > MaxBytes || !uniqueSuiteKeys(raw) || safeCLIJSON(raw) != nil || !utf8.ValidString(source.User) || !utf8.ValidString(source.Assistant) || source.User == "" || strings.TrimSpace(source.Assistant) == "" {
		return fail
	}
	var actual FallbackExchange
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&actual) != nil || d.Decode(new(any)) != io.EOF || actual != source {
		return fail
	}
	canonical, err := json.Marshal(map[string]string{"user": source.User, "assistant": source.Assistant})
	if err != nil || !bytes.Equal(raw, canonical) {
		return fail
	}
	return nil
}
