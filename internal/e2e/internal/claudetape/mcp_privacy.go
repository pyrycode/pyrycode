package claudetape

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/e2emcp"
)

// Private launch capabilities are memory/startup-pipe only. Reject disclosure
// even inside escaped unknown JSON fields; never scrub or replace such bytes.
func mcpPrivateSafe(raw []byte, values ...string) error {
	if len(raw) > MaxBytes || !utf8.Valid(raw) || Safe(raw) != nil {
		return mcpError()
	}
	forbidden := append([]string{e2emcp.SocketEnv, e2emcp.TokenEnv}, values...)
	check := func(text string) bool {
		for _, value := range forbidden {
			if value != "" && strings.Contains(text, value) {
				return false
			}
		}
		return true
	}
	if !check(string(raw)) {
		return mcpError()
	}
	var walk func(any, int) bool
	decode := func(raw []byte) (any, bool, bool) {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
			return nil, false, true
		}
		return value, true, uniqueSuiteKeys(raw)
	}
	walk = func(value any, depth int) bool {
		if depth > 128 {
			return false
		}
		switch current := value.(type) {
		case string:
			if !check(current) {
				return false
			}
			nested, complete, unique := decode([]byte(current))
			if complete {
				return unique && walk(nested, depth+1)
			}
		case map[string]any:
			for key, child := range current {
				if !check(key) || !walk(child, depth+1) {
					return false
				}
			}
		case []any:
			for _, child := range current {
				if !walk(child, depth+1) {
					return false
				}
			}
		}
		return true
	}
	value, complete, unique := decode(raw)
	if complete && (!unique || !walk(value, 0)) {
		return mcpError()
	}
	return nil
}
