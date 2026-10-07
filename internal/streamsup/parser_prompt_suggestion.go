package streamsup

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// maxPromptSuggestionBytes caps a prompt suggestion's decoded text, in UTF-8 bytes.
const maxPromptSuggestionBytes = 1024

// maxPromptSuggestionRaw is the widest JSON spelling of maxPromptSuggestionBytes:
// six bytes for each byte spelled as an escape, plus the two quotes. A raw value
// longer than this cannot decode within the cap, so it is refused before any Go
// string is built from it.
const maxPromptSuggestionRaw = 6*maxPromptSuggestionBytes + 2

// promptSuggestionLine is the prompt_suggestion decode target. The value stays
// raw so its source bytes can be judged before encoding/json rewrites them, and
// claude's uuid and session_id are not declared: neither names a daemon turn.
type promptSuggestionLine struct {
	Suggestion json.RawMessage `json:"suggestion"`
}

// emitPromptSuggestion publishes one valid suggestion as turnevent.PromptSuggestion
// and drops every other payload in silence: no event, no Unrecognized and no log
// record, on any path. The json error is not logged because encoding/json quotes
// the offending input into its text.
func (p *Parser) emitPromptSuggestion(line []byte) {
	if text, ok := decodePromptSuggestion(line); ok {
		p.emit(turnevent.PromptSuggestion{Text: text})
	}
}

// decodePromptSuggestion returns the suggestion text verbatim when it is a JSON
// string whose source is valid UTF-8 with no unpaired surrogate escape, and whose
// decoded text is non-blank, holds no line break and is at most
// maxPromptSuggestionBytes long. Anything else reports false; nothing is trimmed,
// truncated or repaired to make it pass.
//
// The two source checks run BEFORE string decoding because encoding/json turns an
// invalid byte and an unpaired surrogate escape alike into U+FFFD without error,
// and a replacement character claude never sent must not be accepted as its text.
func decodePromptSuggestion(line []byte) (string, bool) {
	var sl promptSuggestionLine
	if err := json.Unmarshal(line, &sl); err != nil {
		return "", false
	}
	raw := sl.Suggestion
	// Absent, null and every non-string value fail the leading-quote test.
	if len(raw) < 2 || len(raw) > maxPromptSuggestionRaw || raw[0] != '"' {
		return "", false
	}
	if !utf8.Valid(raw) || hasUnpairedSurrogateEscape(raw) {
		return "", false
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", false
	}
	if len(text) > maxPromptSuggestionBytes ||
		strings.TrimFunc(text, unicode.IsSpace) == "" ||
		strings.ContainsFunc(text, isPromptSuggestionLineBreak) {
		return "", false
	}
	return text, true
}

// isPromptSuggestionLineBreak reports CR, LF, NEL, LINE SEPARATOR and PARAGRAPH
// SEPARATOR: the breaks that would let one suggestion render as several lines.
func isPromptSuggestionLineBreak(r rune) bool {
	switch r {
	case '\r', '\n', 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// hasUnpairedSurrogateEscape reports whether a JSON string token spells a UTF-16
// surrogate escape that is not a high surrogate followed directly by a low one.
// The token must already be valid JSON, as one json.RawMessage holds.
func hasUnpairedSurrogateEscape(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		unit, ok := jsonUnicodeEscapeAt(raw, i)
		if !ok {
			// A two-byte escape such as \n or \": skip the escaped byte so an
			// escaped backslash cannot be read as the start of another escape.
			i++
			continue
		}
		switch {
		case unit >= 0xD800 && unit <= 0xDBFF:
			low, ok := jsonUnicodeEscapeAt(raw, i+6)
			if !ok || low < 0xDC00 || low > 0xDFFF {
				return true
			}
			i += 11
		case unit >= 0xDC00 && unit <= 0xDFFF:
			return true
		default:
			i += 5
		}
	}
	return false
}

// jsonUnicodeEscapeAt decodes the six-byte escape \uXXXX starting at raw[i].
func jsonUnicodeEscapeAt(raw []byte, i int) (uint64, bool) {
	if i+6 > len(raw) || raw[i] != '\\' || raw[i+1] != 'u' {
		return 0, false
	}
	unit, err := strconv.ParseUint(string(raw[i+2:i+6]), 16, 16)
	return unit, err == nil
}
