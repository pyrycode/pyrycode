package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"

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

func TestRecordedBackgroundReplyCompletion(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../internal/e2e/realclaude/testdata/background_reply_completion.json")
	if os.IsNotExist(err) {
		t.Skip("pending dispatcher live capture and needs-live-artifacts return")
	}
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Revision          string                                                                          `json:"revision"`
		ClaudeVersion     string                                                                          `json:"claude_version"`
		SourceIndices     []int                                                                           `json:"source_indices"`
		Replay            []string                                                                        `json:"replay"`
		ChildStart        int                                                                             `json:"child_start"`
		ReleasedAfterMain bool                                                                            `json:"released_after_main"`
		CapturedLines     int                                                                             `json:"captured_lines"`
		OmittedLines      int                                                                             `json:"omitted_lines"`
		Loss              *struct{ LinesOverCap, BytesOverCap, PartialsDropped, UnterminatedPartial int } `json:"loss"`
	}
	if json.Unmarshal(raw, &record) != nil || len(record.Revision) != 40 || record.ClaudeVersion == "" || len(record.SourceIndices) != 3 || len(record.Replay) != 3 || !(record.SourceIndices[0] < record.SourceIndices[1] && record.SourceIndices[1] < record.SourceIndices[2]) {
		t.Fatal("invalid captured provenance")
	}
	if !record.ReleasedAfterMain || record.ChildStart <= record.SourceIndices[0] || record.ChildStart >= record.SourceIndices[2] || record.CapturedLines <= record.SourceIndices[2] || record.OmittedLines != record.CapturedLines-3 || record.Loss == nil || record.Loss.LinesOverCap != 0 || record.Loss.BytesOverCap != 0 || record.Loss.PartialsDropped != 0 || record.Loss.UnterminatedPartial != 0 {
		t.Fatal("incomplete background or loss evidence")
	}
	testChildCompletionPipeline(t, record.Replay)
}

func testChildCompletionPipeline(t *testing.T, recorded []string) {
	t.Helper()
	ctx := context.Background()
	_, bcast, e := newParentLaneEmitter(t, testConvID)
	e.hist = history.New(t.TempDir())
	source := history.SessionProvenance{Kind: "claude", SessionID: "producing-session"}
	p := streamsup.NewParser(func(ev turnevent.Event) { e.HandleFor(ctx, testConvID, ev, source) }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Rig-authored shape regression; the separate live capture must establish the background boundary.
	lines := []string{
		`{"type":"assistant","message":{"id":"launch","role":"assistant","content":[{"type":"tool_use","id":"agent","name":"Agent","input":{"run_in_background":true}}]}}`,
		`{"type":"result","subtype":"success"}`,
		`{"type":"assistant","parent_tool_use_id":"agent","message":{"id":"final","role":"assistant","content":[{"type":"text","text":"final reply"}]}}`,
	}
	if recorded != nil {
		lines = recorded
	}
	for _, line := range lines {
		if _, err := p.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	e.HandleFor(ctx, testConvID, turnevent.BackgroundTaskStarted{TaskID: "task", ToolCallID: "agent"}, source)
	e.HandleFor(ctx, testConvID, turnevent.BackgroundTaskUpdated{TaskID: "task", Status: "completed"}, source)
	e.flushAll(ctx)
	if e.inTurn {
		t.Fatal("child completion opened a synthetic main turn")
	}
	entries := historyEntries(t, e.hist, testConvID)
	live := thread.New(testConvID)
	var parent, prose, ending uint64
	var childTurn, mainTurn string
	for _, entry := range entries {
		if entry.Type == protocol.TypeToolUse {
			parent = entry.ID
		}
		if entry.Type == protocol.TypeTurnEnd {
			ending = entry.ID
			var v protocol.TurnEndPayload
			_ = json.Unmarshal(entry.Payload, &v)
			mainTurn = v.TurnID
		}
		if entry.Type == protocol.TypeAssistantDelta {
			prose = entry.ID
			var v map[string]any
			_ = json.Unmarshal(entry.Payload, &v)
			childTurn, _ = v["turn_id"].(string)
			if v["text_complete"] != true || v["text"] != "final reply" || v["parent_tool_use_id"] != "agent" {
				t.Fatalf("completion fact lost: %s", entry.Payload)
			}
		}
		if err := live.Feed([]history.Entry{entry}); err != nil {
			t.Fatal(err)
		}
	}
	if !(parent < ending && ending < prose) || childTurn == "" || childTurn == mainTurn {
		t.Fatalf("wrong boundary parent=%d end=%d prose=%d child=%q main=%q", parent, ending, prose, childTurn, mainTurn)
	}
	var replies int
	for _, item := range live.Items() {
		if item.Kind != "assistant_message" {
			continue
		}
		replies++
		var content struct {
			Text       string                         `json:"text"`
			Completion protocol.AssistantDeltaPayload `json:"completion"`
		}
		if json.Unmarshal(item.Content, &content) != nil || content.Text != "final reply" || content.Completion.Text != content.Text || !content.Completion.TextComplete || content.Completion.TurnID != childTurn || content.Completion.ParentToolUseID != "agent" {
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
}

func TestInteractiveTurnEmitterV2_ChildCompletionSplit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, bcast, e := newParentLaneEmitter(t, testConvID)
	text := strings.Repeat("x", 2*maxDeltaTextBytes+1)
	e.Handle(ctx, turnevent.TextChunk{MessageID: "large", ParentToolCallID: "agent", Text: text, TextComplete: true})
	deltas := assistantDeltas(t, bcast.pushes)
	var joined strings.Builder
	for i, delta := range deltas {
		if delta.Seq != i || delta.TextComplete != (i == len(deltas)-1) || delta.ParentToolUseID != "agent" {
			t.Fatal("split lost completion order")
		}
		joined.WriteString(delta.Text)
	}
	if len(deltas) != 3 || joined.String() != text || e.inTurn {
		t.Fatal("split lost text or changed main lifecycle")
	}
}
