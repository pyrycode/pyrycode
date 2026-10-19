package claudetape

import (
	"encoding/json"
	"strings"
	"testing"
)

func subagentBoundaryTape() *Tape {
	t := nativeContractTape(1)
	t.Version = 2
	t.Scenario = "subagent-text"
	main := func(text string) Frame {
		return Frame{Direction: "stdout", Data: json.RawMessage(`{"type":"stream_event","parent_tool_use_id":null,"event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"` + text + `"}}}`)}
	}
	t.Frames = []Frame{{Direction: "stdin", Data: json.RawMessage(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"prompt"}]}}`)}, main("first"), {Direction: ClientCheckpointDirection, Data: json.RawMessage(`{"type":"client_checkpoint","sequence":1,"action":"main_stream_observed","conversation_id":"conv"}`)}, main("tail")}
	return t
}
func TestSubagentMainObservationBoundary(t *testing.T) {
	mutations := map[string]func(*Tape){
		"valid": func(*Tape) {},
		"early": func(t *Tape) { t.Frames[1], t.Frames[2] = t.Frames[2], t.Frames[1] },
		"late":  func(t *Tape) { t.Frames[2], t.Frames[3] = t.Frames[3], t.Frames[2] },
		"wrong-conversation": func(t *Tape) {
			t.Frames[2].Data = []byte(strings.ReplaceAll(string(t.Frames[2].Data), "conv", "foreign"))
		},
		"wrong-action": func(t *Tape) {
			t.Frames[2].Data = []byte(strings.ReplaceAll(string(t.Frames[2].Data), "main_stream_observed", "interrupt_phase"))
		},
		"child-only-before": func(t *Tape) {
			t.Frames[1].Data = []byte(strings.ReplaceAll(string(t.Frames[1].Data), "null", `"agent-1"`))
		},
		"child-only-after": func(t *Tape) {
			t.Frames[3].Data = []byte(strings.ReplaceAll(string(t.Frames[3].Data), "null", `"agent-1"`))
		},
		"empty-before":       func(t *Tape) { t.Frames[1].Data = []byte(strings.ReplaceAll(string(t.Frames[1].Data), "first", " ")) },
		"empty-after":        func(t *Tape) { t.Frames[3].Data = []byte(strings.ReplaceAll(string(t.Frames[3].Data), "tail", "")) },
		"output-only-prompt": func(t *Tape) { t.Frames[0].Direction = "stdout" },
		"duplicate-action":   func(t *Tape) { t.Frames = append(t.Frames, t.Frames[2]) },
		"missing-action":     func(t *Tape) { t.Frames = append(t.Frames[:2], t.Frames[3:]...) },
		"extra-user":         func(t *Tape) { t.Frames = append(t.Frames, t.Frames[0]) },
		"foreign-prompt": func(t *Tape) {
			t.Frames[0].Data = []byte(strings.ReplaceAll(string(t.Frames[0].Data), "prompt", "other"))
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			tape := subagentBoundaryTape()
			mutate(tape)
			if (tape.RequireSubagentMainObservation("conv", "prompt") == nil) != (name == "valid") {
				t.Fatal("wrong native checkpoint boundary acceptance")
			}
		})
	}
	tape := subagentBoundaryTape()
	if err := tape.Validate(); err != nil {
		t.Fatal(err)
	}
	p, err := New(tape, tape.Args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Input(tape.Frames[0].Data)
	if err != nil || len(out) != 1 {
		t.Fatal("prefix did not stop after first native main text")
	}
	if p.Complete() == nil {
		t.Fatal("unobserved client action completed")
	}
	out, err = p.Action(tape.Frames[2].Data)
	if err != nil || len(out) != 1 || p.Complete() != nil {
		t.Fatal("observed action did not release exact native tail")
	}
}
