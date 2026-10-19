package claudetape

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func nativeContractTape(n int) *Tape {
	t := &Tape{Version: 1, Scenario: "native-suggestions", Producer: Producer{Revision: strings.Repeat("a", 40), BinarySHA256: strings.Repeat("b", 64), ClaudeVersion: "test", CapturedAt: "2026-10-19T00:00:00Z"}, Args: []string{"--stream"}}
	add := func(v any) {
		raw, _ := json.Marshal(v)
		t.Frames = append(t.Frames, Frame{Direction: "stdin", Data: raw})
	}
	control := func(id int, sub string) {
		add(map[string]any{"type": "control_request", "request_id": fmt.Sprint(id), "request": map[string]any{"subtype": sub, "detail": "preserved"}})
	}
	user := func(text string) {
		add(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": text}}}})
	}
	control(1, "initialize")
	for i := 0; i < n; i++ {
		user(fmt.Sprintf("coding-%d", i))
		control(i+2, "get_context_usage")
	}
	user("clear")
	t.Frames = append(t.Frames, Frame{Direction: "stdout", Data: json.RawMessage(`{"type":"prompt_suggestion","suggestion":"next"}`)})
	return t
}

func TestNativeCodingPrefixContract(t *testing.T) {
	prompts := []string{"coding-0", "coding-1", "coding-2", "coding-3", "coding-4", "coding-5"}
	for n := 1; n <= 6; n++ {
		a, b, err := SameNativeSuggestionContract(nativeContractTape(1), nativeContractTape(n), prompts, "clear")
		if err != nil || a != 1 || b != n {
			t.Fatalf("prefix %d rejected", n)
		}
	}
	if SameContract(nativeContractTape(1), nativeContractTape(2)) == nil {
		t.Fatal("strict ordered comparison accepted a different count")
	}
	mutations := map[string]func(*Tape){
		"wrong-prompt": func(t *Tape) {
			t.Frames[1].Data = []byte(strings.ReplaceAll(string(t.Frames[1].Data), "coding-0", "other"))
		},
		"skip":    func(t *Tape) { t.Frames[1].Data = t.Frames[3].Data },
		"reorder": func(t *Tape) { t.Frames[1], t.Frames[3] = t.Frames[3], t.Frames[1] },
		"wrong-clear": func(t *Tape) {
			t.Frames[5].Data = []byte(strings.ReplaceAll(string(t.Frames[5].Data), "clear", "other"))
		},
		"wrong-query": func(t *Tape) {
			t.Frames[2].Data = []byte(strings.ReplaceAll(string(t.Frames[2].Data), "get_context_usage", "other"))
		},
		"query-field": func(t *Tape) {
			t.Frames[4].Data = []byte(strings.ReplaceAll(string(t.Frames[4].Data), "preserved", "changed"))
		},
		"init-field": func(t *Tape) {
			t.Frames[0].Data = []byte(strings.ReplaceAll(string(t.Frames[0].Data), "preserved", "changed"))
		},
		"unknown-user-field": func(t *Tape) {
			t.Frames[1].Data = []byte(strings.ReplaceAll(string(t.Frames[1].Data), `"role":"user"`, `"role":"user","new":true`))
		},
		"role": func(t *Tape) {
			t.Frames[1].Data = []byte(strings.ReplaceAll(string(t.Frames[1].Data), `"role":"user"`, `"role":"assistant"`))
		},
		"block-type": func(t *Tape) {
			t.Frames[1].Data = []byte(strings.ReplaceAll(string(t.Frames[1].Data), `"type":"text"`, `"type":"image"`))
		},
		"extra-content": func(t *Tape) {
			t.Frames[1].Data = []byte(strings.ReplaceAll(string(t.Frames[1].Data), `"content":[`, `"content":[{},`))
		},
		"user-type": func(t *Tape) {
			t.Frames[1].Data = []byte(strings.ReplaceAll(string(t.Frames[1].Data), `"type":"user"`, `"type":"assistant"`))
		},
		"empty-id": func(t *Tape) {
			t.Frames[2].Data = []byte(strings.ReplaceAll(string(t.Frames[2].Data), `"request_id":"2"`, `"request_id":""`))
		},
		"reuse-id": func(t *Tape) {
			t.Frames[2].Data = []byte(strings.ReplaceAll(string(t.Frames[2].Data), `"request_id":"2"`, `"request_id":"1"`))
		},
		"missing-query":   func(t *Tape) { t.Frames = append(t.Frames[:2], t.Frames[3:]...) },
		"duplicate-query": func(t *Tape) { t.Frames = append(t.Frames[:2], append([]Frame{t.Frames[2]}, t.Frames[2:]...)...) },
		"no-native":       func(t *Tape) { t.Frames = t.Frames[:len(t.Frames)-1] },
		"seven":           func(t *Tape) { *t = *nativeContractTape(7) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			b := nativeContractTape(2)
			mutate(b)
			if _, _, err := SameNativeSuggestionContract(nativeContractTape(1), b, prompts, "clear"); err == nil {
				t.Fatal("changed complete contract accepted")
			}
		})
	}
}
