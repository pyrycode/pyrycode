package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestResidualContentRetired(t *testing.T) {
	h := history.New(t.TempDir())
	store := thread.NewStore(h)
	t.Cleanup(func() { _ = store.Shutdown() })
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "legacy", Interactive: true}, {ConnID: "thread", Interactive: true, Thread: true}, {ConnID: "other"}}}}
	e := newInteractiveTurnEmitterV2(nil, bcast, discardLogger())
	e.hist = h
	source := history.SessionProvenance{Kind: "claude", SessionID: "asking"}
	e.source = source
	var live []protocol.Envelope
	p := &daemonThreadPublisher{store: store, active: func(context.Context, string) []relay.ActiveConn {
		return []relay.ActiveConn{{ConnID: "thread", Interactive: true, Thread: true}}
	}, push: func(_ context.Context, _ string, env protocol.Envelope) error { live = append(live, env); return nil }, log: discardLogger()}
	for _, conv := range []string{testConvID, testConvIDB} {
		id := conversations.ConversationID(conv)
		if err := store.Load(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		testShadowWait(t, func() bool { return store.Status(id).State == thread.StateUsable })
		w := &shadowConversation{}
		p.publish(t.Context(), id, w)
		e.emit(t.Context(), conv, protocol.TypeBanner, protocol.BannerPayload{ConversationID: conv, Level: "warning", Text: "saved banner"})
		e.recordRuntimeFact(t.Context(), historyTurnOpened, runtimeHistoryFact{ConversationID: conv, TurnID: "reused-turn", OccurredAt: time.Now().UTC()})
		e.recordRuntimeFact(t.Context(), historyTurnInterrupted, runtimeHistoryFact{ConversationID: conv, TurnID: "reused-turn", Cause: "child_exit", OccurredAt: time.Now().UTC()})
		b, _, _, _ := testPromptBridge(t, h, discardLogger())
		b.recordPromptAnswer("reused-answer", promptHistoryOwner{conversationID: conv, sessionID: source.SessionID, source: source, tool: "Bash"}, "allow", permbridge.Verdict{Behavior: "allow"}, nil)
		d := testDelivery(t, t.TempDir(), h, nil)
		d.sessionFor = func(conversations.ConversationID) *history.SessionProvenance { return &source }
		d.complete = newChannelPostEmitterV2(bcast, t.Context(), discardLogger()).complete
		testAccept(t, d, id, "saved post")
		d.drain()
		entries := historyEntries(t, h, conv)
		if len(entries) != 6 {
			t.Fatalf("source count = %d", len(entries))
		}
		for _, entry := range entries {
			if entry.Session == nil || *entry.Session != source || entry.Shown == nil {
				t.Fatal("source metadata lost")
			}
		}
		version := entries[len(entries)-1].ID
		testShadowWait(t, func() bool { return store.Status(id).Version == version })
		start := len(live)
		p.publish(t.Context(), id, w)
		rows := map[uint64]protocol.ThreadItem{}
		for _, env := range live[start:] {
			testRetiredThreadUpdate(t, conv, rows, env)
		}
		catchup, err := store.CatchUp(t.Context(), id, "", 0, 128)
		if err != nil || len(rows) == 0 || len(rows) != len(catchup.Items) {
			t.Fatalf("live/catch-up mismatch: %d/%d: %v", len(rows), len(catchup.Items), err)
		}
		var banner, answer, post bool
		for _, item := range catchup.Items {
			testDaemonThreadJSONEqual(t, rows[item.ID], daemonThreadItem(item))
			raw, _ := json.Marshal(item)
			banner = banner || bytes.Contains(raw, []byte("saved banner"))
			answer = answer || bytes.Contains(raw, []byte("prompt_answer"))
			post = post || bytes.Contains(raw, []byte("saved post"))
		}
		if !banner || !answer || !post {
			t.Fatalf("missing canonical content: banner=%v answer=%v post=%v", banner, answer, post)
		}
		before := len(live)
		p.publish(t.Context(), id, w)
		if len(live) != before {
			t.Fatal("duplicate canonical publication")
		}

		if len(bcast.pushes) != 0 {
			t.Fatal("retired content published or retained for replay")
		}
	}
}

func TestResidualContentRetiredStorage(t *testing.T) {
	for _, mode := range []string{"nil", "failed", "no-clients"} {
		t.Run(mode, func(t *testing.T) {
			bcast := mixedSnapshot()
			if mode == "no-clients" {
				bcast = &fakeInteractiveBcast{}
			}
			var logs bytes.Buffer
			e := newInteractiveTurnEmitterV2(nil, bcast, bufLogger(&logs))
			if mode != "nil" {
				root := t.TempDir()
				if mode == "failed" {
					root += "/occupied"
					if err := os.WriteFile(root, nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
				e.hist = history.New(root)
			}
			e.source = history.SessionProvenance{Kind: "codex", SessionID: "original"}
			e.emit(t.Context(), testConvID, protocol.TypeBanner, protocol.BannerPayload{ConversationID: testConvID, Level: "warning", Text: "PRIVATE_BANNER", StopsTurn: true})
			for _, typ := range []string{historyTurnOpened, historyToolInterrupted, historyTurnInterrupted} {
				e.recordRuntimeFact(t.Context(), typ, runtimeHistoryFact{ConversationID: testConvID, TurnID: "turn", ToolCallID: "tool", Cause: "child_exit", OccurredAt: historyTS})
			}

			if len(bcast.pushes) != 0 {
				t.Fatal("storage mode restored raw publication")
			}
			if mode == "no-clients" {
				entries := historyEntries(t, e.hist, testConvID)
				if len(entries) != 4 {
					t.Fatal("missing append without clients")
				}
				for _, entry := range entries {
					if entry.Session == nil || *entry.Session != e.source || entry.Shown == nil {
						t.Fatal("source metadata lost")
					}
				}
			}
			if mode == "failed" && logs.Len() == 0 {
				t.Fatal("missing failure diagnostics")
			}
			if strings.Contains(logs.String(), "PRIVATE_BANNER") {
				t.Fatal("content exposed in diagnostics")
			}
		})
	}
}
