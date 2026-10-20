package claudetape

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/e2emcp"
)

// Private launch capabilities are memory/startup-pipe only. Reject disclosure
// even inside escaped unknown JSON fields; never scrub or replace such bytes.
func mcpPrivateSafe(raw []byte, values ...string) error {
	if !utf8.Valid(raw) || Safe(raw) != nil {
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
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) == nil {
		var walk func(any) bool
		walk = func(value any) bool {
			switch current := value.(type) {
			case string:
				return check(current)
			case map[string]any:
				for key, child := range current {
					if !check(key) || !walk(child) {
						return false
					}
				}
			case []any:
				for _, child := range current {
					if !walk(child) {
						return false
					}
				}
			}
			return true
		}
		if !walk(value) {
			return mcpError()
		}
	}
	return nil
}
