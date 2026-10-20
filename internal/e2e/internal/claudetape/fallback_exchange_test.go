package claudetape

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func fallbackExchangeControl() *Tape {
	tape := lifecycleSuiteControl().Roles["bootstrap"].Tape
	tape.Frames = nil
	add := func(direction string, v any) {
		raw, _ := json.MarshalIndent(v, "", "  ")
		tape.Frames = append(tape.Frames, Frame{Direction: direction, Data: raw})
	}
	add("stdin", map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": "Please ask me one question."}}}})
	message := func(id, parent, text string) map[string]any {
		return map[string]any{"type": "assistant", "parent_tool_use_id": parent, "message": map[string]any{"id": id, "role": "assistant", "content": []any{map[string]string{"type": "text", "text": text}}}}
	}
	add("stdout", message("earlier", "", "I will inspect a tool."))
	add("stdout", message("nested", "subagent-tool", "Foreign nested-agent prose."))
	add("stdout", message("final", "", "Would you like a Go test?"))
	add("stdout", map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": "Would you like a Go test?"})
	return tape
}
func TestFallbackExchangeUsesFinalMainMessage(t *testing.T) {
	tape := fallbackExchangeControl()
	before, _ := json.Marshal(tape)
	exchanges, err := FallbackExchanges(tape)
	if err != nil || len(exchanges) != 1 || exchanges[1] != (FallbackExchange{"Please ask me one question.", "Would you like a Go test?"}) {
		t.Fatalf("wrong attributed exchange: %v %v", exchanges, err)
	}
	after, _ := json.Marshal(tape)
	if !bytes.Equal(before, after) {
		t.Fatal("derivation mutated native tape")
	}
	input, _ := json.Marshal(map[string]string{"user": exchanges[1].User, "assistant": exchanges[1].Assistant})
	if err := ValidateFallbackInput(input, exchanges[1]); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"user":"Please ask me one question.","assistant":"Would you like a Go test?"}`, `{"user":"different","assistant":"Would you like a Go test?"}`, `{"user":"Please ask me one question.","assistant":"earlier prose"}`, `{"user":"Please ask me one question.","assistant":"Would you like a Go test?","extra":true}`, `{"user":"hidden","user":"Please ask me one question.","assistant":"Would you like a Go test?"}`, string(input) + `{}`} {
		if ValidateFallbackInput([]byte(raw), exchanges[1]) == nil {
			t.Fatal("foreign or ambiguous worker input accepted")
		}
	}
}
func TestFallbackExchangeRequiresSuccessfulCompletedSource(t *testing.T) {
	for _, kind := range []string{"missing-end", "failed", "cancelled", "overlap", "attachment"} {
		t.Run(kind, func(t *testing.T) {
			tape := fallbackExchangeControl()
			switch kind {
			case "missing-end":
				tape.Frames = tape.Frames[:len(tape.Frames)-1]
			case "failed":
				tape.Frames[len(tape.Frames)-1].Data = json.RawMessage(`{"type":"result","subtype":"error_during_execution","is_error":true}`)
			case "cancelled":
				tape.Frames[len(tape.Frames)-1].Data = json.RawMessage(`{"type":"result","subtype":"error_during_execution","is_error":false}`)
			case "overlap":
				tape.Frames = append(tape.Frames[:2], append([]Frame{tape.Frames[0]}, tape.Frames[2:]...)...)
			case "attachment":
				tape.Frames[0].Data = json.RawMessage(`{"type":"user","message":{"role":"user","content":[{"type":"image","source":{}}]}}`)
			}
			exchanges, err := FallbackExchanges(tape)
			if err == nil && len(exchanges) != 0 {
				t.Fatal("unsupported source lent a fallback exchange")
			}
		})
	}
}
func TestFallbackExchangeReadsCompleteNativeFixture(t *testing.T) {
	f, err := os.Open("../../testdata/claude-tapes/clear-reset.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	suite, err := DecodeLifecycleSuite(f, LifecycleProfiles()["clear-reset"].Roles)
	if err != nil {
		t.Fatal(err)
	}
	exchanges, err := FallbackExchanges(suite.Roles["bound"].Tape)
	if err != nil || len(exchanges) != 2 || !strings.Contains(exchanges[1].User, "PYRYA205B064") || exchanges[1].Assistant == "" || exchanges[2].Assistant == "" {
		t.Fatal("actual pretty native frames did not preserve completed exchanges", err)
	}
}
func TestFallbackExchangeClipsAtUnicodeBoundaries(t *testing.T) {
	text := strings.Repeat("a", 4095) + "ä" + strings.Repeat("middle", 1500) + "最後の質問?"
	bounded := fallbackAssistant(text)
	if !utf8.ValidString(bounded) || len(bounded) > 8192 || !strings.HasPrefix(bounded, strings.Repeat("a", 4095)+"\n\n[middle of the reply omitted]\n\n") || !strings.HasSuffix(bounded, "最後の質問?") {
		t.Fatal("opening or closing question lost at UTF-8 clipping boundary")
	}
}
