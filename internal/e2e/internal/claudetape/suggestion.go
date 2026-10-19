package claudetape

import (
	"encoding/json"
	"errors"
	"strings"
)

func (t *Tape) RequireNativeSuggestion() error {
	for _, f := range t.Frames {
		if f.Direction != "stdout" {
			continue
		}
		var p struct{ Type, Suggestion string }
		if json.Unmarshal(f.Data, &p) != nil {
			return errors.New("invalid native suggestion source")
		}
		if p.Type == "prompt_suggestion" && strings.TrimSpace(p.Suggestion) != "" {
			return nil
		}
	}
	return errors.New("no nonempty native prompt suggestion source")
}

// IsUserText recognizes only a complete typed daemon-written user text block.
func IsUserText(raw []byte, want string) bool {
	var v struct {
		Type    string
		Message struct {
			Role    string
			Content []struct{ Type, Text string }
		}
	}
	if json.Unmarshal(raw, &v) != nil || v.Type != "user" || v.Message.Role != "user" {
		return false
	}
	for _, b := range v.Message.Content {
		if b.Type == "text" && b.Text == want {
			return true
		}
	}
	return false
}
func (t *Tape) RequireUserText(want string) error {
	for _, f := range t.Frames {
		if f.Direction == "stdin" && IsUserText(f.Data, want) {
			return nil
		}
	}
	return errors.New("required daemon user input missing at seal")
}
