package claudetape

import (
	"encoding/json"
	"testing"
)

func TestNativeSuggestionEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, dir, raw string
		want           bool
	}{
		{"valid", "stdout", `{"type":"prompt_suggestion","suggestion":"Add a unit test"}`, true},
		{"wrong-direction", "stdin", `{"type":"prompt_suggestion","suggestion":"Add a unit test"}`, false},
		{"empty", "stdout", `{"type":"prompt_suggestion","suggestion":"  "}`, false},
		{"missing", "stdout", `{"type":"prompt_suggestion"}`, false},
		{"wire-substitute", "stdout", `{"type":"reply_suggestion","suggestion":"Add a unit test"}`, false},
		{"assistant-substitute", "stdout", `{"type":"assistant","suggestion":"Add a unit test"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tape := Tape{Frames: []Frame{{Direction: tc.dir, Data: json.RawMessage(tc.raw)}}}
			if err := tape.RequireNativeSuggestion(); (err == nil) != tc.want {
				t.Fatalf("native suggestion evidence=%v", err)
			}
		})
	}
}
func TestRequiredNativeInputEvidence(t *testing.T) {
	const text = "clear"
	raw := json.RawMessage(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"clear"}]}}`)
	tape := Tape{Frames: []Frame{{Direction: "stdin", Data: raw}}}
	if err := tape.RequireUserText(text); err != nil {
		t.Fatal(err)
	}
	if IsUserText(raw, "other") {
		t.Fatal("unrelated user text matched")
	}
	if IsUserText([]byte(`{"type":"assistant","message":{"role":"user","content":[{"type":"text","text":"clear"}]}}`), text) {
		t.Fatal("output substituted for user input")
	}
	tape.Frames[0].Direction = "stdout"
	if err := tape.RequireUserText(text); err == nil {
		t.Fatal("output substituted for daemon input")
	}
}
