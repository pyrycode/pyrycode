package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/thread"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestInteractiveTurnEmitterV2_ChildCompletionBoundary(t *testing.T) {
	t.Parallel()
	testChildCompletionPipeline(t, nil)
}

func TestInteractiveTurnEmitterV2_ChildCompletionSequence(t *testing.T) {
	t.Parallel()
	testChildCompletionPipeline(t, &backgroundCompletionRecord{
		Parent: "agent", ProducingSession: "rig-session", Launch: 0, MainEnd: 3, Final: 6,
		Replay: []string{
			`{"type":"assistant","message":{"id":"launch","role":"assistant","content":[{"type":"tool_use","id":"agent","name":"Agent","input":{"run_in_background":true}}]}}`,
			`{"type":"system","subtype":"task_started","task_id":"task","tool_use_id":"agent","task_type":"local_agent"}`,
			`{"type":"assistant","message":{"id":"main","role":"assistant","content":[{"type":"text","text":"launched"}]}}`,
			`{"type":"result","subtype":"success"}`,
			`{"type":"assistant","parent_tool_use_id":"agent","message":{"id":"wait","role":"assistant","content":[{"type":"tool_use","id":"bash","name":"Bash","input":{"command":"rig-wait"}}]}}`,
			`{"type":"user","parent_tool_use_id":"agent","message":{"content":[{"type":"tool_result","tool_use_id":"bash","content":"rig-release-observed","is_error":false}]}}`,
			`{"type":"assistant","parent_tool_use_id":"agent","message":{"id":"final","role":"assistant","content":[{"type":"text","text":"pyry-3589-finished"}]}}`,
			`{"type":"system","subtype":"task_notification","task_id":"task","status":"completed","summary":"rig-finished"}`,
		},
	})
}

func TestRecordedBackgroundReplyCompletion(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../internal/e2e/realclaude/testdata/background_reply_completion.json")
	if err != nil {
		t.Fatal(err)
	}
	var record backgroundCompletionRecord
	if json.Unmarshal(raw, &record) != nil || len(record.Revision) != 40 || record.ClaudeVersion == "" || record.Parent == "" || record.ProducingSession == "" || len(record.Replay) != record.CapturedLines || len(record.SourceIndices) != record.CapturedLines {
		t.Fatal("invalid captured provenance")
	}
	for i, index := range record.SourceIndices {
		if index != i {
			t.Fatal("replay omitted or reordered captured source lines")
		}
	}
	if record.Launch < 0 || record.MainEnd <= record.Launch || record.ChildStart <= record.Launch || record.ReleaseIndex <= record.MainEnd || record.ChildResult < record.ReleaseIndex || record.ChildResult <= record.ChildStart || record.Final <= record.ChildResult || record.Final >= record.CapturedLines || record.OmittedLines != 0 || record.Loss == nil || record.Loss.LinesOverCap != 0 || record.Loss.BytesOverCap != 0 || record.Loss.PartialsDropped != 0 || record.Loss.UnterminatedPartial != 0 {
		t.Fatal("incomplete background or loss evidence")
	}
	if record.Execution != nil || record.Revision != "9c5de672a041e52dbb82c585758f3545fe008b59" {
		if err := validateBackgroundCompletionCorrelation(&record); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Log("historical pre-witness capture; current physical execution evidence pending dispatcher artifact return")
	}
	testChildCompletionPipeline(t, &record)
}

type backgroundCompletionRecord struct {
	Revision         string                                                                          `json:"revision"`
	ClaudeVersion    string                                                                          `json:"claude_version"`
	SourceIndices    []int                                                                           `json:"source_indices"`
	Replay           []string                                                                        `json:"replay"`
	ChildStart       int                                                                             `json:"child_start"`
	ChildResult      int                                                                             `json:"child_result"`
	ReleaseIndex     int                                                                             `json:"release_index"`
	Launch           int                                                                             `json:"launch"`
	MainEnd          int                                                                             `json:"main_end"`
	Final            int                                                                             `json:"final"`
	Parent           string                                                                          `json:"parent"`
	ProducingSession string                                                                          `json:"producing_session"`
	CapturedLines    int                                                                             `json:"captured_lines"`
	OmittedLines     int                                                                             `json:"omitted_lines"`
	Loss             *struct{ LinesOverCap, BytesOverCap, PartialsDropped, UnterminatedPartial int } `json:"loss"`
	Execution        *struct {
		Operation string `json:"operation_id"`
		Tool      string `json:"tool_use_id"`
	} `json:"execution"`
	OS string `json:"os"`
}

func validateBackgroundCompletionCorrelation(r *backgroundCompletionRecord) error {
	if r.Execution == nil || !strings.HasPrefix(r.Execution.Operation, "rig-id-") || !strings.HasPrefix(r.Execution.Tool, "rig-id-") || r.Execution.Operation == r.Execution.Tool || (r.OS != "linux" && r.OS != "darwin") || r.ReleaseIndex <= r.ChildStart {
		return fmt.Errorf("missing physical execution correlation")
	}
	agents, tools, operations, results := 0, 0, 0, 0
	for i, line := range r.Replay {
		var v struct {
			Type    string `json:"type"`
			Parent  string `json:"parent_tool_use_id"`
			Message struct {
				ID      string `json:"id"`
				Role    string `json:"role"`
				Content []struct {
					Type    string          `json:"type"`
					ID      string          `json:"id"`
					Name    string          `json:"name"`
					Tool    string          `json:"tool_use_id"`
					Content json.RawMessage `json:"content"`
					Failed  bool            `json:"is_error"`
					Input   struct {
						Background bool   `json:"run_in_background"`
						Command    string `json:"command"`
					} `json:"input"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &v) != nil {
			return fmt.Errorf("invalid correlation replay")
		}
		for _, b := range v.Message.Content {
			if v.Type == "assistant" && b.Type == "tool_use" {
				if v.Message.ID == "" || v.Message.Role != "assistant" {
					return fmt.Errorf("invalid tool declaration")
				}
				if b.Name == "Agent" && v.Parent == "" {
					agents++
					if i != r.Launch || b.ID != r.Parent || !b.Input.Background {
						return fmt.Errorf("mismatched background Agent")
					}
				}
				if b.ID == r.Execution.Tool {
					tools++
					if i != r.ChildStart || v.Parent != r.Parent || b.Name != "Bash" || b.Input.Command != "rig-synthetic-operation:"+r.Execution.Operation {
						return fmt.Errorf("mismatched witnessed Bash operation")
					}
				}
				if strings.HasPrefix(b.Input.Command, "rig-synthetic-operation:") {
					operations++
				}
			}
			if v.Type == "user" && b.Type == "tool_result" && b.Tool == r.Execution.Tool {
				results++
				var content string
				if i != r.ChildResult || v.Parent != r.Parent || b.Failed || json.Unmarshal(b.Content, &content) != nil || strings.TrimSpace(content) != "rig-release-observed" {
					return fmt.Errorf("mismatched witnessed result")
				}
			}
		}
	}
	if agents != 1 || tools != 1 || operations != 1 || results != 1 {
		return fmt.Errorf("ambiguous physical execution correlation")
	}
	return nil
}

func testChildCompletionPipeline(t *testing.T, recorded *backgroundCompletionRecord) {
	t.Helper()
	ctx := context.Background()
	_, bcast, e := newParentLaneEmitter(t, testConvID)
	e.hist = history.New(t.TempDir())
	source := history.SessionProvenance{Kind: "claude", SessionID: "producing-session"}
	parentID, expectedText := "agent", "final reply"
	// Rig-authored shape regression; the separate live capture must establish the background boundary.
	lines := []string{
		`{"type":"assistant","message":{"id":"launch","role":"assistant","content":[{"type":"tool_use","id":"agent","name":"Agent","input":{"run_in_background":true}}]}}`,
		`{"type":"result","subtype":"success"}`,
		`{"type":"assistant","parent_tool_use_id":"agent","message":{"id":"final","role":"assistant","content":[{"type":"text","text":"final reply"}]}}`,
	}
	if recorded != nil {
		lines, parentID, expectedText = recorded.Replay, recorded.Parent, "pyry-3589-finished"
		source.SessionID = recorded.ProducingSession
	}
	p := streamsup.NewParser(func(ev turnevent.Event) { e.HandleFor(ctx, testConvID, ev, source) }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	createdAt := map[uint64]int{}
	var consumed int
	for index, line := range lines {
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
		entries := historyEntries(t, e.hist, testConvID)
		for _, entry := range entries[consumed:] {
			createdAt[entry.ID] = index
		}
		consumed = len(entries)
	}
	if recorded == nil {
		e.HandleFor(ctx, testConvID, turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: parentID}, source)
		e.HandleFor(ctx, testConvID, turnevent.BackgroundTaskUpdated{TaskID: "task", Status: "completed"}, source)
	}
	e.flushAll(ctx)
	if e.inTurn {
		t.Fatal("child completion opened a synthetic main turn")
	}
	entries := historyEntries(t, e.hist, testConvID)
	live := thread.New(testConvID)
	var parent, prose, ending uint64
	var childTurn, mainTurn string
	var endings int
	for _, entry := range entries {
		if entry.Type == protocol.TypeToolUse {
			var v protocol.ToolUsePayload
			if json.Unmarshal(entry.Payload, &v) == nil && v.ToolUseID == parentID {
				parent = entry.ID
			}
		}
		if entry.Type == protocol.TypeTurnEnd {
			endings++
			ending = entry.ID
			var v protocol.TurnEndPayload
			_ = json.Unmarshal(entry.Payload, &v)
			mainTurn = v.TurnID
		}
		if entry.Type == protocol.TypeAssistantDelta {
			var v map[string]any
			_ = json.Unmarshal(entry.Payload, &v)
			if v["parent_tool_use_id"] == parentID {
				if prose != 0 || v["text_complete"] != true || v["text"] != expectedText {
					t.Fatal("duplicated child prose or lost completion fact")
				}
				prose = entry.ID
				childTurn, _ = v["turn_id"].(string)
			}
		}
		if err := live.Feed([]history.Entry{entry}); err != nil {
			t.Fatal(err)
		}
	}
	if parent == 0 || !(parent < ending && ending < prose) || childTurn == "" || childTurn == mainTurn || endings != 1 {
		t.Fatalf("wrong boundary parent=%d end=%d prose=%d child=%q main=%q", parent, ending, prose, childTurn, mainTurn)
	}
	if recorded != nil && (createdAt[parent] != recorded.Launch || createdAt[ending] != recorded.MainEnd || createdAt[prose] != recorded.Final) {
		t.Fatal("daemon order no longer matches recorded source boundaries")
	}
	var replies int
	for _, item := range live.Items() {
		if item.Kind != "assistant_message" || item.Parent != parent {
			continue
		}
		replies++
		var content struct {
			Text       string                         `json:"text"`
			Completion protocol.AssistantDeltaPayload `json:"completion"`
		}
		if json.Unmarshal(item.Content, &content) != nil || content.Text != expectedText || content.Completion.Text != content.Text || !content.Completion.TextComplete || content.Completion.TurnID != childTurn || content.Completion.ParentToolUseID != parentID {
			t.Fatal("reply text or completing entry changed")
		}
		if item.ID != prose || item.Order != prose || item.Rev != prose || item.Parent != parent || item.Turn != childTurn || item.Session != source.SessionID || item.Agent != "claude" || item.Status != "done" || item.Active {
			t.Fatalf("wrong completed reply: %+v", item)
		}
	}
	if replies != 1 {
		t.Fatalf("reply count=%d", replies)
	}
	events, gap := e.ring.After(testConvID, 0)
	if gap {
		t.Fatal("catch-up gap")
	}
	assertLogMatchesRing(t, entries, events)
	if len(events) != len(bcast.pushes) {
		t.Fatal("live/catch-up differ")
	}
	for i, ev := range events {
		if !reflect.DeepEqual(ev.Payload, bcast.pushes[i].env.Payload) {
			t.Fatal("live/catch-up payload differs")
		}
	}
	replay := thread.New(testConvID)
	if err := replay.Feed(entries); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(live.Items(), replay.Items()) {
		t.Fatal("live/replay differ")
	}
	if recorded != nil {
		testRecordedCheckpointResume(t, entries)
	}
}

func TestInteractiveTurnEmitterV2_ChildCompletionSplit(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{"x", "\n"} {
		t.Run(fmt.Sprintf("suffix-%q", suffix), func(t *testing.T) {
			testChildCompletionSplit(t, false, strings.Repeat("x", 2*maxDeltaTextBytes)+suffix)
		})
	}
}

func TestInteractiveTurnEmitterV2_SealedChildCompletionSplit(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{"x", "\n"} {
		t.Run(fmt.Sprintf("suffix-%q", suffix), func(t *testing.T) {
			testChildCompletionSplit(t, true, strings.Repeat("x", 2*maxDeltaTextBytes)+suffix)
		})
	}
}

func testChildCompletionSplit(t *testing.T, sealed bool, text string) {
	t.Helper()
	ctx := context.Background()
	_, _, e := newParentLaneEmitter(t, testConvID)
	e.hist, e.runtimeFacts = history.New(t.TempDir()), true
	source := history.SessionProvenance{Kind: "claude", SessionID: "producing-session"}
	e.handleForSource(ctx, testConvID, turnevent.ToolStart{ToolCallID: "agent", Title: "Agent"}, source, 7)
	e.handleForSource(ctx, testConvID, turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "agent"}, source, 7)
	if sealed {
		e.closeRuntimeSource(ctx, testConvID, source.SessionID, "operator_reset", time.Now().UTC(), true, 0, 7)
	} else {
		e.handleForSource(ctx, testConvID, turnevent.TurnEnd{Reason: "end_turn"}, source, 7)
	}
	e.handleForSource(ctx, testConvID, turnevent.TextChunk{MessageID: "large", ParentToolCallID: "agent", Text: text, TextComplete: true}, source, 7)
	if !sealed {
		e.handleForSource(ctx, testConvID, turnevent.BackgroundTaskUpdated{TaskID: "task", Status: "completed"}, source, 7)
	}
	deltas := assistantDeltas(t, testEmitterFacts(t, e))
	if len(deltas) != 3 {
		t.Fatalf("deltas=%d, want 3", len(deltas))
	}
	entries := historyEntries(t, e.hist, testConvID)
	f := thread.New(testConvID)
	var parent, first, last uint64
	for _, entry := range entries {
		if entry.Type == protocol.TypeToolUse {
			parent = entry.ID
		}
		if entry.Type == protocol.TypeAssistantDelta {
			if first == 0 {
				first = entry.ID
			}
			last = entry.ID
		}
		if err := f.Feed([]history.Entry{entry}); err != nil {
			t.Fatal(err)
		}
	}
	var replies int
	for _, item := range f.Items() {
		if item.Kind != "assistant_message" {
			continue
		}
		replies++
		var content struct {
			Text       string
			Completion protocol.AssistantDeltaPayload
		}
		if err := json.Unmarshal(item.Content, &content); err != nil {
			t.Fatal(err)
		}
		if content.Text != text || item.ID != first || item.Order != first || item.Rev != last || item.Parent != parent || item.Session != source.SessionID || item.Turn != deltas[0].TurnID || item.Status != "done" || item.Active || !content.Completion.TextComplete || content.Completion.Text != deltas[2].Text {
			t.Fatalf("split reply lost text, identity or completion: id=%d rev=%d bytes=%d", item.ID, item.Rev, len(content.Text))
		}
	}
	if replies != 1 || e.inTurn {
		t.Fatalf("replies=%d inTurn=%t", replies, e.inTurn)
	}
	for i, delta := range deltas {
		if delta.TextComplete != (i == len(deltas)-1) || delta.TurnID != deltas[0].TurnID || delta.Seq != deltas[0].Seq+i || delta.ParentToolUseID != "agent" {
			t.Fatal("split lost final-piece completion or lane identity")
		}
	}
}
