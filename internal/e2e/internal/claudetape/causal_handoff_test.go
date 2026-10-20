package claudetape

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/sessions"
)

func handoffControl(note string) (*Tape, *Tape) {
	old := lifecycleSuiteControl()
	copyRole := func(role string) *Tape {
		raw, _ := json.Marshal(old.Roles["bootstrap"].Tape)
		var v Tape
		_ = json.Unmarshal(raw, &v)
		v.Scenario = "clear-reset-" + role
		return &v
	}
	predecessor, successor := copyRole("bound"), copyRole("rotated")
	predecessor.Version = 2
	predecessor.Frames = append(predecessor.Frames, Frame{Direction: ClientCheckpointDirection, Data: json.RawMessage(`{"type":"client_checkpoint","sequence":1,"action":"lifecycle_rotate","conversation_id":"${LIFECYCLE_CONVERSATION}"}`)}, Frame{Direction: "stdin", Data: json.RawMessage(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"the complete original wrapup request"}]}}`)})
	assistant, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"id": "native-wrapup-message", "role": "assistant", "content": []any{map[string]string{"type": "text", "text": note}}}})
	result, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": note})
	predecessor.Frames = append(predecessor.Frames, Frame{Direction: "stdout", Data: assistant}, Frame{Direction: "stdout", Data: result})
	fenced, _ := sessions.FencedHandoffNote(note)
	successor.Files["--append-system-prompt-file"], _ = json.Marshal(map[string]any{"content": "complete literal prefix\n" + fenced + "\ncomplete literal suffix", "mode": 0600})
	return predecessor, successor
}

func TestHandoffContractBindsOnlyItsOwnSuccessfulNativeOutput(t *testing.T) {
	leftOld, left := handoffControl("Native note A with Unicode ä and a complete fact.")
	rightOld, right := handoffControl("Native note B, a different model output with the same causal role.")
	before, _ := json.Marshal(left)
	if SameContract(left, right) == nil {
		t.Fatal("literal distinct full prompts unexpectedly match")
	}
	a, err := lifecycleHandoffContract(left, leftOld)
	if err != nil {
		t.Fatal("first native causal edge", err)
	}
	b, err := lifecycleHandoffContract(right, rightOld)
	if err != nil {
		t.Fatal("second native causal edge", err)
	}
	if SameContract(a, b) != nil {
		t.Fatal("complete self-consistent dependency did not compare")
	}
	after, _ := json.Marshal(left)
	if !bytes.Equal(before, after) {
		t.Fatal("comparison mutated stored literal fixture")
	}
	if _, err := lifecycleHandoffContract(left, rightOld); err == nil {
		t.Fatal("foreign predecessor note bound a successor prompt")
	}
	for _, kind := range []string{"prefix", "suffix", "mode", "request", "duplicate-note", "missing-note", "failed-turn", "missing-end", "extra-user", "wrong-checkpoint", "long-note", "thinking-only"} {
		t.Run(kind, func(t *testing.T) {
			old, next := handoffControl("Native controlled note.")
			var file map[string]any
			_ = json.Unmarshal(next.Files["--append-system-prompt-file"], &file)
			switch kind {
			case "prefix":
				file["content"] = "changed prefix\n" + file["content"].(string)
			case "suffix":
				file["content"] = file["content"].(string) + "changed suffix"
			case "mode":
				file["mode"] = 0644
			case "request":
				next.Frames[0].Data = json.RawMessage(`{"type":"user","message":{"content":"changed original user request"}}`)
			case "duplicate-note":
				file["content"] = file["content"].(string) + file["content"].(string)
			case "missing-note":
				file["content"] = "a prompt without its predecessor note"
			case "failed-turn":
				old.Frames[len(old.Frames)-1].Data = json.RawMessage(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"failure"}`)
			case "missing-end":
				old.Frames = old.Frames[:len(old.Frames)-1]
			case "extra-user":
				old.Frames = append(old.Frames, Frame{Direction: "stdin", Data: json.RawMessage(`{"type":"user","message":{"content":"extra input"}}`)})
			case "wrong-checkpoint":
				old.Frames[2].Data = json.RawMessage(`{"type":"client_checkpoint","sequence":1,"action":"lifecycle_delete","conversation_id":"${LIFECYCLE_CONVERSATION}"}`)
			case "long-note":
				old, next = handoffControl(strings.Repeat("x", sessions.MaxHandoffNoteBytes))
				_ = json.Unmarshal(next.Files["--append-system-prompt-file"], &file)
			case "thinking-only":
				old.Frames[len(old.Frames)-2].Data = json.RawMessage(`{"type":"assistant","message":{"id":"thinking-only","role":"assistant","content":[{"type":"thinking","thinking":"Native controlled note."}]}}`)
			}
			next.Files["--append-system-prompt-file"], _ = json.Marshal(file)
			normalized, err := lifecycleHandoffContract(next, old)
			if err == nil && SameContract(a, normalized) == nil {
				t.Fatal("changed or unproved causal contract accepted")
			}
			// Pure note failures must be rejected before any comparison can proceed.
			if kind != "prefix" && kind != "suffix" && kind != "mode" && kind != "request" && err == nil {
				t.Fatal("unproved dependency was normalized")
			}
		})
	}
}

func TestHandoffSuiteValidatesLiteralCausalEdgeBeforeComparison(t *testing.T) {
	old := lifecycleSuiteControl()
	makeSuite := func(note string) *LifecycleSuite {
		predecessor, successor := handoffControl(note)
		raw, _ := json.Marshal(old.Roles["bootstrap"])
		var bootstrap LifecycleTape
		_ = json.Unmarshal(raw, &bootstrap)
		bootstrap.Tape.Scenario = "clear-reset-bootstrap"
		return &LifecycleSuite{Version: 1, Scenario: "clear-reset", Producer: old.Producer, DaemonSHA256: old.DaemonSHA256, Roles: map[string]*LifecycleTape{"bootstrap": &bootstrap, "bound": {Version: 1, Tape: predecessor, End: "rotate-session"}, "rotated": {Version: 1, Tape: successor, End: "seal"}}}
	}
	a, b := makeSuite("Actual predecessor note A."), makeSuite("Actual predecessor note B.")
	roles := LifecycleProfiles()[a.Scenario].Roles
	raw, _ := json.Marshal(a)
	if _, err := DecodeLifecycleSuite(bytes.NewReader(raw), roles); err != nil {
		t.Fatal("complete literal causal suite", err)
	}
	if err := SameLifecycleSuiteContract(a, b, roles); err != nil {
		t.Fatal("independently valid fresh causal notes", err)
	}
	b.Roles["rotated"].Tape.Files["--append-system-prompt-file"] = a.Roles["rotated"].Tape.Files["--append-system-prompt-file"]
	raw, _ = json.Marshal(b)
	if _, err := DecodeLifecycleSuite(bytes.NewReader(raw), roles); err == nil {
		t.Fatal("suite decoded a prompt with its foreign predecessor note")
	}
}
