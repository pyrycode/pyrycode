//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBackgroundReplyReleaseBoundary(t *testing.T) {
	t.Parallel()
	for _, partial := range []string{"", `{"type":"assistant"`} {
		r := newDropcapRecorder()
		_, _ = r.Write([]byte("{\"type\":\"result\",\"subtype\":\"success\"}\n" + partial))
		path := filepath.Join(t.TempDir(), "release")
		boundary, err := backgroundReplyRelease(r, path, "private-witness")
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if partial != "" {
			want++
		}
		b, err := os.ReadFile(path)
		if err != nil || string(b) != "private-witness\n" || boundary != want {
			t.Fatal("release boundary or witness changed")
		}
		if backgroundReplyReleased(0, boundary, boundary-1, boundary-1) {
			t.Fatal("accepted final prose already captured at release")
		}
		if !backgroundReplyReleased(0, boundary, boundary, boundary+1) {
			t.Fatal("rejected post-release successful tool result and final")
		}
	}
}

func TestBackgroundReplyWitness(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, command, tool, parent, output string
		failed, want                        bool
	}{
		{"observed", "/rig/hold", "wait", "agent", "private-witness", false, true},
		{"quoted", "'some equivalent operation'", "wait", "agent", "private-witness", false, true},
		{"mention only", "echo release", "wait", "agent", "private-witness", false, false},
		{"wrong tool", "/rig/hold", "other", "agent", "private-witness", false, false},
		{"wrong child", "/rig/hold", "wait", "other", "private-witness", false, false},
		{"wrong output", "/rig/hold", "wait", "agent", "release", false, false},
		{"failed", "/rig/hold", "wait", "agent", "private-witness", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frames := []string{
				`{"type":"assistant","message":{"id":"launch","role":"assistant","content":[{"type":"tool_use","id":"agent","name":"Agent","input":{"run_in_background":true}}]}}`,
				fmt.Sprintf(`{"type":"assistant","parent_tool_use_id":"agent","message":{"id":"wait-msg","role":"assistant","content":[{"type":"tool_use","id":"wait","name":"Bash","input":{"command":%q}}]}}`, tc.command),
				`{"type":"result","subtype":"success"}`,
				fmt.Sprintf(`{"type":"user","parent_tool_use_id":%q,"message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":%q,"is_error":%t}]}}`, tc.parent, tc.tool, tc.output, tc.failed),
				`{"type":"assistant","parent_tool_use_id":"agent","message":{"id":"final","role":"assistant","content":[{"type":"text","text":"pyry-3589-finished"}]}}`,
			}
			lines := backgroundReplyTestLines(frames)
			witness := &backgroundReplyExecution{Operation: "private-operation", Tool: "wait"}
			if tc.name == "mention only" {
				witness = nil
			}
			_, end, final, _, result, _ := backgroundReplyBoundary(lines, "private-witness", witness)
			if got := backgroundReplyReleased(end, 3, result, final); got != tc.want {
				t.Fatalf("witness accepted=%t want=%t", got, tc.want)
			}
			if tc.want {
				replay, indices, alias, err := backgroundReplyReplay(t, lines, "agent", "private-witness", final, witness)
				if err != nil || len(replay) != len(frames) || len(indices) != len(frames) || strings.Contains(strings.Join(replay, ""), "private-witness") {
					t.Fatalf("sequence scrub failed: %v", err)
				}
				if strings.Contains(strings.Join(replay, ""), tc.command) || !strings.Contains(strings.Join(replay, ""), "rig-synthetic-operation:"+alias(witness.Operation)) {
					t.Fatal("raw command retained or synthetic command unmarked")
				}
				safeWitness := &backgroundReplyExecution{Operation: alias(witness.Operation), Tool: alias(witness.Tool)}
				_, safeEnd, safeFinal, _, safeResult, safeParent := backgroundReplyBoundary(backgroundReplyTestLines(replay), "rig-release-observed", safeWitness)
				if safeEnd != end || safeFinal != final || safeResult != result || safeParent != alias("agent") {
					t.Fatal("scrubbing lost tool correlation")
				}
				// A marker already captured before release cannot be replaced by a
				// later duplicate after a successful witness result.
				frames = append(frames[:3], frames[4], frames[3], frames[4])
				_, end, final, _, result, _ = backgroundReplyBoundary(backgroundReplyTestLines(frames), "private-witness", witness)
				if backgroundReplyReleased(end, 4, result, final) {
					t.Fatal("accepted final already present at release")
				}
			}
		})
	}
}

func TestBackgroundReplyReplayOpenPartial(t *testing.T) {
	t.Parallel()
	frames := []string{
		`{"type":"result","subtype":"success"}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"final"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pyry-3589-finished"}}}`,
		`{"type":"assistant","parent_tool_use_id":"agent","message":{"id":"final","role":"assistant","content":[{"type":"text","text":"pyry-3589-finished"}]}}`,
	}
	if _, _, _, err := backgroundReplyReplay(t, backgroundReplyTestLines(frames), "agent", "token", 4); err == nil {
		t.Fatal("accepted isolated final despite companion suppression in captured sequence")
	}
	// A real block stop changes parser state and must survive the replay.
	frames = append(frames[:4], `{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`, frames[4])
	replay, _, _, err := backgroundReplyReplay(t, backgroundReplyTestLines(frames), "agent", "token", 5)
	if err != nil || len(replay) != 6 {
		t.Fatalf("lost stream block boundary: %v", err)
	}
}

func TestBackgroundReplyReplayTaskContext(t *testing.T) {
	t.Parallel()
	frames := []string{
		`{"type":"assistant","message":{"id":"launch","role":"assistant","content":[{"type":"tool_use","id":"agent","name":"Agent","input":{"run_in_background":true}}]}}`,
		`{"type":"system","subtype":"task_started","task_id":"task","tool_use_id":"agent","task_type":"local_agent","description":"private description"}`,
		`{"type":"result","subtype":"success","duration_ms":17,"num_turns":2,"usage":{"output_tokens":99}}`,
		`{"type":"assistant","parent_tool_use_id":"agent","message":{"id":"final","role":"assistant","content":[{"type":"text","text":"pyry-3589-finished"}]}}`,
		`{"type":"system","subtype":"task_notification","task_id":"task","status":"completed","summary":"private summary"}`,
	}
	if _, _, _, err := backgroundReplyReplay(t, backgroundReplyTestLines(frames), "agent", "token", 3); err != nil {
		t.Fatal(err)
	}
}

func backgroundReplyTestLines(frames []string) []dropcapCaptured {
	lines := make([]dropcapCaptured, len(frames))
	for i, frame := range frames {
		lines[i] = dropcapCaptured{Index: i, Raw: []byte(frame)}
	}
	return lines
}

func TestBackgroundReplyReplayTaskPatch(t *testing.T) {
	for _, patch := range []string{
		`{"status":"completed","end_time":1234567890123}`,
		`{ "end_time" : 1234567890123, "status" : "completed" }`,
	} {
		frames := []string{
			`{"type":"assistant","message":{"id":"launch","role":"assistant","content":[{"type":"tool_use","id":"agent","name":"Agent","input":{"run_in_background":true}}]}}`,
			`{"type":"system","subtype":"task_started","task_id":"task","tool_use_id":"agent","task_type":"local_agent"}`,
			`{"type":"result","subtype":"success"}`,
			`{"type":"assistant","parent_tool_use_id":"agent","message":{"id":"final","role":"assistant","content":[{"type":"text","text":"pyry-3589-finished"}]}}`,
			`{"type":"system","subtype":"task_updated","task_id":"task","patch":` + patch + `}`,
		}
		replay, indices, _, err := backgroundReplyReplay(t, backgroundReplyTestLines(frames), "agent", "token", 3)
		if err != nil || len(replay) != len(frames) || len(indices) != len(frames) {
			t.Fatalf("observed task patch could not retain parser trace: %v", err)
		}
		var safe struct {
			Patch map[string]any `json:"patch"`
		}
		if json.Unmarshal([]byte(replay[4]), &safe) != nil || safe.Patch["status"] != "completed" || safe.Patch["end_time"] != float64(0) || strings.Contains(replay[4], "1234567890123") {
			t.Fatal("task patch did not retain status with a rig-authored timestamp")
		}
	}
}

func TestBackgroundReplyReplayUnknownTaskPatch(t *testing.T) {
	for _, patch := range []string{`{"unknown_key":"private-patch-sentinel"}`, `{"end_time":"private-patch-sentinel"}`} {
		frames := []string{`{"type":"system","subtype":"task_updated","task_id":"task","patch":` + patch + `}`}
		_, _, _, err := backgroundReplyReplay(t, backgroundReplyTestLines(frames), "agent", "token", -1)
		if err == nil || strings.Contains(err.Error(), "private-patch-sentinel") {
			t.Fatal("unrecognized or mistyped patch field did not fail closed privately")
		}
	}
}

func TestBackgroundReplyPatchTraceStructure(t *testing.T) {
	first := `{"first_key":"x"}`
	other := `{"other_key":"x"}`
	if len(first) != len(other) {
		t.Fatal("fixture no longer exercises equal serialized lengths")
	}
	a, err := backgroundReplyPatchTrace(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := backgroundReplyPatchTrace(other)
	if err != nil || reflect.DeepEqual(a, b) {
		t.Fatal("equal serialized lengths hid different patch keys")
	}
	a, err = backgroundReplyPatchTrace(`{"a":1,"b":true}`)
	if err != nil {
		t.Fatal(err)
	}
	b, err = backgroundReplyPatchTrace(`{ "b" : true, "a" : 1 }`)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("JSON key order or whitespace changed patch structure")
	}
	var values []map[string]any
	for _, raw := range []string{"", `null`, `""`, `{}`, `{"a":1}`, `{"a":"1"}`} {
		value, err := backgroundReplyPatchTrace(raw)
		if err != nil {
			t.Fatal(err)
		}
		for _, previous := range values {
			if reflect.DeepEqual(value, previous) {
				t.Fatal("patch absence, JSON kind or field type became indistinguishable")
			}
		}
		values = append(values, value)
	}
	if _, err := backgroundReplyPatchTrace(`{"private-patch-sentinel":`); err == nil || strings.Contains(err.Error(), "private-patch-sentinel") {
		t.Fatal("invalid opaque patch did not fail closed privately")
	}
}
