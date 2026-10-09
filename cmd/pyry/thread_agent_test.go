package main

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/thread"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TestThreadAgentRecordedReplay uses #2191's committed Claude v2.1.259 capture;
// all launch, result, link and completion evidence comes from its recorded frames.
func TestThreadAgentRecordedReplay(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../internal/e2e/realclaude/testdata/parent_tool_use_v2.1.259.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Ticket   string `json:"ticket"`
		Version  string `json:"claude_version"`
		Captured string `json:"captured_at"`
		Capture  bool   `json:"is_capture"`
		Call     string `json:"agent_tool_use_id"`
		Frames   []struct {
			Payload string `json:"payload"`
		} `json:"frames"`
	}
	if json.Unmarshal(raw, &capture) != nil || capture.Ticket != "2191" || capture.Version != "2.1.259 (Claude Code)" || !capture.Capture || capture.Captured != "2026-09-09T21:05:29+03:00" || len(capture.Frames) != 17 {
		t.Fatal("capture provenance changed")
	}
	store := history.New(t.TempDir())
	e := newInteractiveTurnEmitterV2(nil, historyOnlyBroadcaster{}, discardLogger())
	e.hist, e.runtimeFacts = store, true
	source := history.SessionProvenance{Kind: "claude", SessionID: "recorded"}
	parser := streamsup.NewParser(func(ev turnevent.Event) { e.HandleFor(context.Background(), testConvID, ev, source) }, discardLogger())
	for _, frame := range capture.Frames {
		if _, err := parser.Write([]byte(frame.Payload + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	entries := historyEntries(t, store, testConvID)
	full := thread.New(testConvID)
	if err := full.Feed(entries); err != nil {
		t.Fatal(err)
	}
	var agents []thread.Item
	for _, item := range full.Items() {
		if item.Kind == "agent" {
			agents = append(agents, item)
		}
	}
	if len(agents) != 1 {
		t.Fatalf("agents: %#v", agents)
	}
	agent := agents[0]
	var content map[string]json.RawMessage
	if err := json.Unmarshal(agent.Content, &content); err != nil {
		t.Fatal(err)
	}
	var call, task string
	_ = json.Unmarshal(content["tool_use_id"], &call)
	_ = json.Unmarshal(content["task_id"], &task)
	if call != capture.Call || task != "a8eec1cd5e109aa38" || agent.Status != "finished" || agent.Active || agent.EndedOrder == 0 {
		t.Fatalf("agent: %#v %s", agent, agent.Content)
	}
	var input map[string]string
	if json.Unmarshal(content["input"], &input) != nil || input["prompt"] != "Read the file alpha.txt and then read the file beta.txt in the current working directory, then reply with the two marker strings you found." || input["run_in_background"] != "false" {
		t.Fatal("recorded foreground input lost")
	}
	var result struct {
		Summary string `json:"result_summary"`
		Detail  string `json:"result_detail"`
	}
	if json.Unmarshal(content["result"], &result) != nil || !strings.Contains(result.Detail+result.Summary, "pyry-2191-alpha-marker") || !strings.Contains(result.Detail+result.Summary, "pyry-2191-beta-marker") {
		t.Fatal("recorded result markers lost")
	}
	foundCall, foundResult, foundEnding := false, false, false
	for _, entry := range entries {
		var p map[string]json.RawMessage
		_ = json.Unmarshal(entry.Payload, &p)
		var id string
		_ = json.Unmarshal(p["tool_use_id"], &id)
		if entry.Type == "tool_use" && id == capture.Call {
			foundCall = true
			if agent.ID != entry.ID || agent.Order != entry.ID || !reflect.DeepEqual(content["input"], p["input"]) {
				t.Fatal("launch identity/input changed")
			}
		}
		if entry.Type == "tool_result" && id == capture.Call {
			foundResult = true
			var result map[string]json.RawMessage
			_ = json.Unmarshal(content["result"], &result)
			if !reflect.DeepEqual(result["result_detail"], p["result_detail"]) {
				t.Fatal("recorded result lost")
			}
		}
		if entry.Type == "background_task_outcome" && entry.ID == agent.EndedOrder {
			foundEnding = true
		}
	}
	if !foundCall || !foundResult || !foundEnding {
		t.Fatal("missing recorded call, result or task final")
	}
	var children []thread.Item
	for _, item := range full.Items() {
		if item.Parent == agent.ID {
			children = append(children, item)
		}
	}
	if len(children) != 2 {
		t.Fatalf("recorded Read children: %#v", children)
	}
	for i, want := range []struct{ call, file, marker string }{
		{"toolu_01WAR86LLmsQa72SgN3P8reE", "$WORKDIR/alpha.txt", "pyry-2191-alpha-marker"},
		{"toolu_01MBCi2bQ79dotofRkLqJB6m", "$WORKDIR/beta.txt", "pyry-2191-beta-marker"},
	} {
		child := children[i]
		var saved struct {
			Call   string            `json:"tool_use_id"`
			Name   string            `json:"name"`
			Input  map[string]string `json:"input"`
			Result struct {
				Summary string `json:"result_summary"`
				Detail  string `json:"result_detail"`
			} `json:"result"`
		}
		if json.Unmarshal(child.Content, &saved) != nil || saved.Call != want.call || saved.Name != "Read" || saved.Input["file_path"] != want.file || !strings.Contains(saved.Result.Summary+saved.Result.Detail, want.marker) {
			t.Fatalf("recorded child fields lost: %s", child.Content)
		}
		if child.Kind != "tool_call" || child.Status != "done" || child.Active || child.Session != "recorded" || child.Agent != "claude" || child.ID <= agent.ID {
			t.Fatalf("recorded child: %#v", child)
		}
		foundLaunch, foundResult := false, false
		for _, entry := range entries {
			var p struct {
				Call    string            `json:"tool_use_id"`
				Input   map[string]string `json:"input"`
				Summary string            `json:"result_summary"`
				Detail  string            `json:"result_detail"`
			}
			_ = json.Unmarshal(entry.Payload, &p)
			if p.Call != want.call {
				continue
			}
			if entry.Type == "tool_use" {
				foundLaunch = true
				if child.ID != entry.ID || child.Order != entry.ID || !reflect.DeepEqual(saved.Input, p.Input) {
					t.Fatal("child creation identity/input changed")
				}
			}
			if entry.Type == "tool_result" {
				foundResult = true
				if child.Rev != entry.ID || saved.Result.Detail != p.Detail || saved.Result.Summary != p.Summary {
					t.Fatal("child result identity/content changed")
				}
			}
		}
		if !foundLaunch || !foundResult {
			t.Fatal("missing recorded child launch/result")
		}
	}
	for split := 0; split <= len(entries); split++ {
		f := thread.New(testConvID)
		if err := f.Feed(entries[:split]); err != nil {
			t.Fatal(err)
		}
		if err := f.Feed(entries[split:]); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.Items(), full.Items()) || f.Version() != full.Version() {
			t.Fatalf("partition %d", split)
		}
	}
}
