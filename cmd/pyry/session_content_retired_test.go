package main

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/thread"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testRetiredSessionContentType(typ string) bool {
	return slices.Contains([]string{protocol.TypeSessionTransition, protocol.TypeModelRefusalFallback,
		protocol.TypeModelRefusalNoFallback, protocol.TypeCompactionBoundary, protocol.TypeUnrecognizedMessage}, typ)
}

// testTransitionFact waits for source recording; its envelope is only a decoder
// container and never evidence of raw publication.
func testTransitionFact(t *testing.T, store *history.Store, conv string) protocol.Envelope {
	t.Helper()
	var entries []history.Entry
	testShadowWait(t, func() bool { entries = historyEntries(t, store, conv); return len(entries) > 0 })
	entry := entries[len(entries)-1]
	return protocol.Envelope{Type: entry.Type, Payload: entry.Payload, TS: entry.TS, HistoryEntryID: &entry.ID}
}

func TestSessionContentRetired(t *testing.T) {
	t.Parallel()
	h := history.New(t.TempDir())
	store := thread.NewStore(h)
	t.Cleanup(func() { _ = store.Shutdown() })
	b := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "legacy", Interactive: true}, {ConnID: "thread", Interactive: true, Thread: true}, {ConnID: "other"}}}}
	e := newInteractiveTurnEmitterV2(nil, b, discardLogger())
	e.hist = h
	tr := newSessionTransitionEmitterV2(b, nil, discardLogger())
	tr.hist = h
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
		rows := map[uint64]protocol.ThreadItem{}
		for n, cause := range []sessions.LifecycleCause{sessions.CauseOperatorReset, sessions.CauseClaudeClear, sessions.CauseAgentSwitch, sessions.CauseRecovery, sessions.CauseIdleSleep, sessions.CauseCapacityEviction} {
			source := history.SessionProvenance{Kind: "claude", SessionID: "reused-source"}
			for _, inTurn := range []bool{false, true} {
				if inTurn {
					e.HandleFor(t.Context(), conv, turnevent.TextChunk{MessageID: "same", Text: "before"}, source)
				}
				turn, phase := e.turnID, e.currentState
				for _, ev := range []turnevent.Event{
					turnevent.ModelRefusalFallback{OriginalModel: "haiku", FallbackModel: "sonnet", Banner: "PRIVATE_NOTICE"},
					turnevent.ModelRefusalNoFallback{OriginalModel: "sonnet", Banner: "PRIVATE_NOTICE"},
					turnevent.CompactionBoundary{Trigger: "auto"},
					turnevent.Unrecognized{Site: turnevent.UnrecognizedLineType, Kind: "future", Raw: `{"type":"future"}`},
				} {
					e.HandleFor(t.Context(), conv, ev, source)
				}
				if e.inTurn != inTurn || e.turnID != turn || e.currentState != phase {
					t.Fatal("notice manufactured a lifecycle edge")
				}
				e.HandleFor(t.Context(), conv, turnevent.TurnEnd{Reason: turnevent.TurnEndReasonEndTurn}, source)
			}
			transition := sessions.SessionTransition{ConversationID: conv, PreviousID: "reused-source", NewID: "successor", PreviousAgent: "claude", NextAgent: "codex", Cause: cause, Reason: sessions.ReasonClear, OccurredAt: time.Unix(int64(n+1), 0).UTC()}
			if cause == sessions.CauseRecovery {
				transition.Reason = ""
			}
			if cause == sessions.CauseIdleSleep || cause == sessions.CauseCapacityEviction {
				transition.NewID, transition.Reason = "", sessions.ReasonEviction
			}
			tr.broadcast(t.Context(), transition)
			entries := historyEntries(t, h, conv)
			version := entries[len(entries)-1].ID
			testShadowWait(t, func() bool { return store.Status(id).Version == version })
			start := len(live)
			p.publish(t.Context(), id, w)
			for _, env := range live[start:] {
				testRetiredThreadUpdate(t, conv, rows, env)
			}
			before := len(live)
			p.publish(t.Context(), id, w)
			if len(live) != before {
				t.Fatal("repeated canonical publication duplicated items")
			}
			catchup, err := store.CatchUp(t.Context(), id, "", 0, 128)
			if err != nil || len(rows) != len(catchup.Items) {
				t.Fatalf("live/catch-up mismatch: %d/%d: %v", len(rows), len(catchup.Items), err)
			}
			var notices, compactions, dividers int
			for _, item := range catchup.Items {
				testDaemonThreadJSONEqual(t, rows[item.ID], daemonThreadItem(item))
				switch item.Kind {
				case "notice":
					notices++
					if item.Session != source.SessionID || item.Agent != source.Kind || item.Active {
						t.Fatalf("notice attribution/lifecycle: %+v", item)
					}
				case "compaction":
					compactions++
				case "session_divider":
					dividers++
				}
			}
			if notices != (n+1)*6 || compactions != (n+1)*2 || dividers != n+1 {
				t.Fatalf("canonical counts: notices=%d compactions=%d dividers=%d", notices, compactions, dividers)
			}
		}
		for _, push := range b.pushes {
			if testRetiredSessionContentType(push.env.Type) || push.env.Type == protocol.TypeBanner {
				t.Fatalf("retired live publication: %s", push.env.Type)
			}
		}
		events, _ := e.ring.After(conv, 0)
		for _, event := range events {
			if testRetiredSessionContentType(event.Type) || event.Type == protocol.TypeBanner {
				t.Fatalf("retired replay publication: %s", event.Type)
			}
		}
	}
}

func TestSessionContentRetiredStorage(t *testing.T) {
	for _, mode := range []string{"nil", "failed", "no-clients"} {
		t.Run(mode, func(t *testing.T) {
			b := mixedSnapshot()
			if mode == "no-clients" {
				b = &fakeInteractiveBcast{}
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
			for _, typ := range []string{protocol.TypeModelRefusalFallback, protocol.TypeModelRefusalNoFallback, protocol.TypeCompactionBoundary, protocol.TypeUnrecognizedMessage} {
				e.emit(t.Context(), testConvID, typ, map[string]string{"text": "PRIVATE_NOTICE"})
			}
			tr := newSessionTransitionEmitterV2(b, nil, bufLogger(&logs))
			tr.hist = e.hist
			tr.runtimeSink = &streamTurnSink{runtimeReplayRing: e.ring}
			var switched bool
			tr.switched = func(string) { switched = true }
			tr.broadcast(t.Context(), sessions.SessionTransition{ConversationID: testConvID, PreviousID: "old", NewID: "new", Cause: sessions.CauseAgentSwitch, Reason: sessions.ReasonClear, AgentSwitch: true, OccurredAt: occurred})
			events, _ := e.ring.After(testConvID, 0)
			if len(b.pushes) != 0 || !switched || len(events) != 0 {
				t.Fatal("storage mode restored publication/replay or lost switch completion")
			}
			if mode == "no-clients" && len(historyEntries(t, e.hist, testConvID)) != 6 {
				t.Fatal("missing single source append without clients")
			}
			if strings.Contains(logs.String(), "PRIVATE_NOTICE") {
				t.Fatal("notice content leaked into diagnostics")
			}
		})
	}
}
