package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testRetiredInteractiveType(typ string) bool {
	return slices.Contains([]string{protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied, protocol.TypeTurnEnd}, typ)
}

func TestInteractiveContentRetired(t *testing.T) {
	t.Parallel()
	h := history.New(t.TempDir())
	store := thread.NewStore(h)
	t.Cleanup(func() { _ = store.Shutdown() })
	b := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "legacy", Interactive: true}, {ConnID: "thread", Interactive: true, Thread: true}, {ConnID: "other"}}}}
	e := newInteractiveTurnEmitterV2(nil, b, discardLogger())
	e.hist = h
	ctx := t.Context()
	var live []protocol.Envelope
	p := &daemonThreadPublisher{store: store, active: func(context.Context, string) []relay.ActiveConn {
		return []relay.ActiveConn{{ConnID: "thread", Interactive: true, Thread: true}}
	}, push: func(_ context.Context, _ string, env protocol.Envelope) error { live = append(live, env); return nil }, log: discardLogger()}
	for _, conv := range []string{testConvID, testConvIDB} {
		id := conversations.ConversationID(conv)
		if err := store.Load(ctx, id); err != nil {
			t.Fatal(err)
		}
		testShadowWait(t, func() bool { return store.Status(id).State == thread.StateUsable })
		w := &shadowConversation{}
		p.publish(ctx, id, w)
		liveStart := len(live)
		for _, session := range []string{"source-a", "source-b"} {
			source := history.SessionProvenance{Kind: "claude", SessionID: session}
			for _, ev := range []turnevent.Event{
				turnevent.TextChunk{MessageID: "same", Text: conv + session},
				turnevent.ToolStart{ToolCallID: "parent", Title: "Agent"},
				turnevent.TextChunk{MessageID: "same", ParentToolCallID: "parent", Text: "child", TextComplete: true},
				turnevent.ToolStart{ToolCallID: "tool", ParentToolCallID: "parent", Title: "Read"},
				turnevent.ToolUpdate{ToolCallID: "tool", ParentToolCallID: "parent", Status: turnevent.ToolStatusCompleted, Content: turnevent.TextContent{Text: "result"}},
				turnevent.ToolCallDenied{ToolCallID: "parent", ToolName: "Agent", Message: "denied"},
				turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
				turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn},
			} {
				e.HandleFor(ctx, conv, ev, source)
			}
		}
		e.HandleFor(ctx, conv, turnevent.Unrecognized{Site: turnevent.UnrecognizedAssistantBlock, Kind: "future", Raw: `{"type":"future"}`}, history.SessionProvenance{Kind: "claude", SessionID: "source-b"})
		entries := historyEntries(t, h, conv)
		version := entries[len(entries)-1].ID
		testShadowWait(t, func() bool { return store.Status(id).Version == version })
		p.publish(ctx, id, w)
		before := len(live)
		p.publish(ctx, id, w)
		if before == 0 || len(live) != before {
			t.Fatal("missing or repeated canonical publication")
		}
		result, err := store.CatchUp(ctx, id, "", 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		published := live[liveStart:]
		rows := map[uint64]protocol.ThreadItem{}
		for _, env := range published {
			testRetiredThreadUpdate(t, conv, rows, env)
		}
		if len(rows) != len(result.Items) {
			t.Fatalf("live/catch-up rows: %d/%d", len(rows), len(result.Items))
		}
		for _, item := range result.Items {
			testDaemonThreadJSONEqual(t, rows[item.ID], daemonThreadItem(item))
		}
		var texts []string
		var tools, notices int
		parents := map[string]uint64{}
		for _, item := range result.Items {
			if item.Order == 0 || item.Session == "" {
				t.Fatalf("lost ordering/provenance: %+v", item)
			}
			switch item.Kind {
			case "agent":
				parents[item.Session] = item.ID
				if item.Active || item.Status != "denied" || !bytes.Contains(item.Content, []byte(`denied`)) {
					t.Fatalf("lost denial: %+v", item)
				}
			case "assistant_message":
				var content protocol.AssistantDeltaPayload
				if err := json.Unmarshal(item.Content, &content); err != nil {
					t.Fatal(err)
				}
				texts = append(texts, content.Text)
				if item.Active || item.Status != "done" || content.Text == "child" && item.Parent != parents[item.Session] {
					t.Fatalf("lost text completion/parent: %+v", item)
				}
			case "tool_call":
				tools++
				if item.Parent == 0 || item.Active || item.Status != "done" || !bytes.Contains(item.Content, []byte(`result`)) {
					t.Fatalf("lost child tool/result: %+v", item)
				}
			case "notice":
				notices++
				if !bytes.Contains(item.Content, []byte(`future`)) {
					t.Fatalf("supported content became a notice: %+v", item)
				}
			}
		}
		if !reflect.DeepEqual(texts, []string{conv + "source-a", "child", conv + "source-b", "child"}) || tools != 2 || notices != 1 {
			t.Fatalf("canonical content: texts=%v tools=%d notices=%d", texts, tools, notices)
		}
		for _, push := range b.pushes {
			if testRetiredInteractiveType(push.env.Type) || push.env.Type == protocol.TypeBanner {
				t.Fatalf("retired live frame: %s", push.env.Type)
			}
		}
	}
}

func testRetiredThreadUpdate(t *testing.T, conv string, rows map[uint64]protocol.ThreadItem, env protocol.Envelope) {
	t.Helper()
	var meta struct {
		ConversationID string `json:"conversation_id"`
	}
	if json.Unmarshal(env.Payload, &meta) != nil || meta.ConversationID != conv {
		t.Fatal("foreign/invalid canonical update")
	}
	switch env.Type {
	case protocol.TypeThreadItemAdded:
		var p protocol.ThreadItemAddedPayload
		if json.Unmarshal(env.Payload, &p) != nil || rows[p.Item.ID].ID != 0 {
			t.Fatal("invalid or repeated item addition")
		}
		rows[p.Item.ID] = p.Item
	case protocol.TypeThreadItemChanged:
		var p protocol.ThreadItemChangedPayload
		if json.Unmarshal(env.Payload, &p) != nil || rows[p.ItemID].Rev != p.BaseRev {
			t.Fatal("broken canonical change chain")
		}
		raw, err := json.Marshal(rows[p.ItemID])
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		for key, value := range p.Changes {
			fields[key] = value
		}
		fields["rev"], _ = json.Marshal(p.Rev)
		raw, err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		var item protocol.ThreadItem
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal(err)
		}
		rows[p.ItemID] = item
	case protocol.TypeThreadTextAppend:
		var p protocol.ThreadTextAppendPayload
		if json.Unmarshal(env.Payload, &p) != nil || rows[p.ItemID].Rev != p.BaseRev {
			t.Fatal("broken canonical text chain")
		}
		item := rows[p.ItemID]
		var content map[string]json.RawMessage
		if err := json.Unmarshal(item.Content, &content); err != nil {
			t.Fatal(err)
		}
		var text string
		if err := json.Unmarshal(content["text"], &text); err != nil {
			t.Fatal(err)
		}
		content["text"], _ = json.Marshal(text + p.Text)
		item.Content, _ = json.Marshal(content)
		item.Rev = p.Rev
		rows[p.ItemID] = item
	default:
		t.Fatalf("unexpected canonical update %s", env.Type)
	}
}

func TestInteractiveContentRetiredStorage(t *testing.T) {
	for _, mode := range []string{"nil", "failed", "no-clients"} {
		t.Run(mode, func(t *testing.T) {
			b := &fakeInteractiveBcast{}
			if mode != "no-clients" {
				b.snapshots = [][]relay.ActiveConn{{{ConnID: "legacy", Interactive: true}}}
			}
			var logs bytes.Buffer
			e := newInteractiveTurnEmitterV2(nil, b, bufLogger(&logs))
			if mode != "nil" {
				root := t.TempDir()
				if mode == "failed" {
					root += "/blocked"
					if err := os.WriteFile(root, nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
				e.hist = history.New(root)
			}
			for _, typ := range []string{protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied, protocol.TypeTurnEnd,
				protocol.TypeBackgroundTaskStarted, protocol.TypeBackgroundTaskUpdated, protocol.TypeBackgroundTaskRoster} {
				e.emit(t.Context(), testConvID, typ, map[string]string{"text": "PRIVATE_CONTENT"})
			}
			if len(b.pushes) != 0 {
				t.Fatal("storage mode restored raw delivery", pushTypes(b.pushes))
			}
			if mode == "no-clients" && len(historyEntries(t, e.hist, testConvID)) != 8 {
				t.Fatal("missing source append without clients")
			}
			if strings.Contains(logs.String(), "PRIVATE_CONTENT") {
				t.Fatal("content leaked into diagnostics")
			}
		})
	}
}
