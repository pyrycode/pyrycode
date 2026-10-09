package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func testSendHistory(store *history.Store) *queuedSendHistory {
	return newQueuedSendHistory(store, discardLogger(), func(conversations.ConversationID) *history.SessionProvenance {
		return &history.SessionProvenance{Kind: "claude", SessionID: "accepted-source"}
	})
}

func testSendFact(t *testing.T, e history.Entry) queuedSendFact {
	t.Helper()
	var p queuedSendFact
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func testAllSendEntries(t *testing.T, store *history.Store) []history.Entry {
	t.Helper()
	page, err := store.Page(conversations.ConversationID(testConvID), "", history.MaxPageEntries)
	if err != nil || !page.AtStart {
		t.Fatalf("raw page: %v %+v", err, page)
	}
	slices.Reverse(page.Entries)
	return page.Entries
}

func TestSuggestionEnqueuerIdentity(t *testing.T) {
	t.Parallel()
	store := history.New(t.TempDir())
	h := testSendHistory(store)
	q, err := msgqueue.New(msgqueue.Config{Deliver: func(context.Context, string, []byte) error { return nil }, OnAccepted: h.accepted, OnTerminal: h.terminal, MaxQueuedPerConversation: 3})
	if err != nil {
		t.Fatal(err)
	}
	suggestions := newReplySuggestions(discardLogger())
	wrapper := suggestionEnqueuer{inner: q, s: suggestions}
	suggestions.beginWrite(testConvID, 1)
	sent := time.Unix(7, 123).UTC()
	for _, device := range []string{"device-a", "device-b"} {
		if wrapper.EnqueueIdentified(testConvID, "same-app", "safe", "/private/host", []string{"attachment"}, device, "display", "app/1", sent) == 0 {
			t.Fatal("rejected")
		}
	}
	if !suggestions.entry(testConvID, false).invalidated {
		t.Fatal("identified acceptance did not invalidate suggestions")
	}
	wrapper.EnqueueSent(testConvID, "legacy", "legacy text", "private", nil, "", "", time.Time{})
	suggestions.beginWrite(testConvID, 3)
	if wrapper.EnqueueIdentified(testConvID, "rejected", "bad", "bad", nil, "device-c", "", "", sent) != 0 {
		t.Fatal("overflow accepted")
	}
	if suggestions.entry(testConvID, false).invalidated {
		t.Fatal("rejection invalidated suggestion")
	}
	legacy := suggestionEnqueuer{inner: stubEnqueuer{id: 9}}
	if legacy.EnqueueIdentified(testConvID, "legacy", "text", "bytes", nil, "ignored", "", "", time.Time{}) != 9 {
		t.Fatal("legacy adapter rejected")
	}
	all := historyEntries(t, store, testConvID)
	if len(all) != 3 {
		t.Fatalf("acceptances: %v", all)
	}
	for i, device := range []string{"device-a", "device-b", ""} {
		p := testSendFact(t, all[i])
		if p.DeviceID != device || p.ConversationID != testConvID || p.AcceptedAt.IsZero() || all[i].Session.SessionID != "accepted-source" || all[i].Shown == nil || !*all[i].Shown {
			t.Fatalf("acceptance: %+v %+v", p, all[i])
		}
		if i < 2 && (p.MessageID != "same-app" || p.Text != "safe" || p.ClientSentAt != formatClientSentAt(sent) || !reflect.DeepEqual(p.AttachmentIDs, []string{"attachment"})) {
			t.Fatalf("fields: %+v", p)
		}
		if strings.Contains(string(all[i].Payload), "private") {
			t.Fatal("delivery leaked")
		}
	}
	if page := newHistoryPager(store, discardLogger())(testConvID, "", 100); len(page.Entries) != 0 {
		t.Fatalf("legacy facts: %+v", page)
	}
}

func TestQueuedSendHistoryLifecycle(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"delivered", "send_now", "removed", "give_up", "retry", "refused", "shutdown"} {
		t.Run(outcome, func(t *testing.T) {
			store := history.New(t.TempDir())
			h := testSendHistory(store)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			terminal := make(chan struct{}, 1)
			pushes := make(chan operatorMessage, 2)
			delivered := operatorMessageHistory(store, func(m operatorMessage) { pushes <- m }, nil, discardLogger(), h)
			attempts := make(chan struct{}, 1)
			var q *msgqueue.Queue
			tries := 0
			cfg := msgqueue.Config{Logger: discardLogger(), OnAccepted: h.accepted, OnDelivered: delivered, OnTerminal: func(c string, m msgqueue.QueuedMessage, o msgqueue.TerminalOutcome) {
				h.terminal(c, m, o)
				terminal <- struct{}{}
			}, RetryInterval: time.Millisecond, GiveUpAfter: time.Millisecond,
				Deliver: func(ctx context.Context, _ string, _ []byte) error {
					select {
					case attempts <- struct{}{}:
					default:
					}
					tries++
					if outcome == "retry" && tries == 1 {
						return errors.New("retry")
					}
					if outcome == "delivered" && !q.Remove(testConvID, 1) {
						t.Error("removal race not requested")
					}
					if outcome == "give_up" {
						return errors.New("fixed failure")
					}
					if outcome == "shutdown" {
						<-ctx.Done()
						return ctx.Err()
					}
					return nil
				}, SendNow: func(context.Context, string, uint64, []byte) error {
					if outcome == "refused" {
						return errors.New("refused")
					}
					return nil
				}}
			var err error
			q, err = msgqueue.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			id := q.Enqueue(testConvID, "hello")
			h.source = func(conversations.ConversationID) *history.SessionProvenance {
				return &history.SessionProvenance{Kind: "codex", SessionID: "drop-source"}
			}
			switch outcome {
			case "refused":
				if q.SendNow(testConvID, id) || q.Remove(testConvID, id+1) {
					t.Fatal("refusal succeeded")
				}
			case "removed":
				if !q.Remove(testConvID, id) || q.Remove(testConvID, id) {
					t.Fatal("removal")
				}
			case "send_now":
				if !q.SendNow(testConvID, id) || q.SendNow(testConvID, id) {
					t.Fatal("send-now")
				}
			default:
				done := make(chan error, 1)
				go func() { done <- q.Run(ctx) }()
				defer func() { cancel(); <-done }()
				if outcome == "shutdown" {
					<-attempts
					cancel()
					break
				}
			}
			if outcome != "shutdown" && outcome != "refused" {
				select {
				case <-terminal:
				case <-time.After(3 * time.Second):
					t.Fatal("terminal missing")
				}
			}
			entries := historyEntries(t, store, testConvID)
			want := 2
			if outcome == "delivered" || outcome == "send_now" || outcome == "retry" {
				want = 3
			}
			if outcome == "shutdown" || outcome == "refused" {
				want = 1
			}
			if len(entries) != want {
				t.Fatalf("entries: %+v", entries)
			}
			if want == 1 {
				return
			}
			last := entries[len(entries)-1]
			p := testSendFact(t, last)
			if p.AcceptedEntryID != entries[0].ID || p.OccurredAt.IsZero() {
				t.Fatalf("link: %+v", p)
			}
			if want == 3 {
				if last.Type != historySendDelivered || p.DeliveryEntryID != entries[1].ID || p.Reason != "delivered" {
					t.Fatalf("delivery: %+v", p)
				}
				if len(pushes) != 1 {
					t.Fatal("push count")
				}
			} else {
				if last.Type != historySendDropped || *last.Shown || p.Reason != outcome || last.Session.SessionID != "drop-source" {
					t.Fatalf("drop: %+v %+v", last, p)
				}
				watermark, err := store.LatestDisplayableEntryID(conversations.ConversationID(testConvID))
				if err != nil || watermark != entries[0].ID {
					t.Fatalf("watermark %d %v", watermark, err)
				}
			}
		})
	}
}

func TestQueuedSendHistoryPlacement(t *testing.T) {
	for _, idle := range []bool{false, true} {
		t.Run(map[bool]string{false: "echo", true: "idle"}[idle], func(t *testing.T) {
			store := history.New(t.TempDir())
			h := testSendHistory(store)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sink := newStreamTurnSink(16, discardLogger())
			place := newSendNowPlacement(ctx, func(string) (string, bool) { return testConvID, true }, func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() })
			place.bindQueued(ctx, sink, store, func(string) bool { return true }, discardLogger())
			place.sendHistory = h
			placementEntered := make(chan struct{})
			placementRecord := place.record
			place.record = func(c string, m msgqueue.QueuedMessage, source ...history.SessionProvenance) {
				close(placementEntered)
				placementRecord(c, m, source...)
			}
			pushes := make(chan operatorMessage, 2)
			sink.setOperatorPublisher(func(m operatorMessage) { pushes <- m })
			written := make(chan struct{})
			release := make(chan struct{})
			confirmed := make(chan struct{})
			releaseConfirm := make(chan struct{})
			record := operatorMessageHistory(store, nil, place, discardLogger(), h)
			q, err := msgqueue.New(msgqueue.Config{Logger: discardLogger(), OnAccepted: func(c string, m msgqueue.QueuedMessage) { <-release; h.accepted(c, m) }, OnDelivered: func(c string, m msgqueue.QueuedMessage) { <-releaseConfirm; record(c, m); close(confirmed) }, OnTerminal: h.terminal,
				Deliver: func(ctx context.Context, c string, payload []byte) error {
					return place.write(ctx, c, payload, func() error { close(written); return nil }, &history.SessionProvenance{Kind: "codex", SessionID: "receiving-source"})
				}})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- q.Run(ctx) }()
			defer func() { cancel(); <-done }()
			enqueued := make(chan struct{})
			go func() { q.EnqueueDelivery(testConvID, "app", "safe", "host private"); close(enqueued) }()
			<-written
			placed := make(chan struct{})
			go func() {
				if idle {
					place.idle("session")
				} else {
					place.echo("session", echoOf("host private"))
				}
				close(placed)
			}()
			<-placementEntered
			if entries := historyEntries(t, store, testConvID); len(entries) != 0 {
				t.Fatal("delivery before acceptance")
			}
			close(release)
			<-placed
			if len(historyEntries(t, store, testConvID)) != 3 || len(pushes) != 1 {
				t.Fatal("placement waited for confirmation")
			}
			close(releaseConfirm)
			<-enqueued
			<-confirmed
			entries := historyEntries(t, store, testConvID)
			if len(entries) != 3 || entries[0].Type != historySendAccepted || entries[1].Type != protocol.TypeMessage || entries[2].Type != historySendDelivered {
				t.Fatalf("ordering: %+v", entries)
			}
			for _, e := range entries[1:] {
				if e.Session == nil || e.Session.SessionID != "receiving-source" {
					t.Fatalf("source: %+v", e)
				}
			}
			if len(pushes) != 1 {
				t.Fatal("duplicate publication")
			}
		})
	}
}

func TestQueuedSendHistoryStartup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := history.New(dir)
	h := testSendHistory(store)
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: conversations.ConversationID(testConvID), CurrentSessionID: "replacement"})
	h.accepted(testConvID, msgqueue.QueuedMessage{ID: 1, Text: "lost", TS: time.Unix(1, 0)})
	h.accepted(testConvID, msgqueue.QueuedMessage{ID: 2, Text: "removed"})
	h.terminal(testConvID, msgqueue.QueuedMessage{ID: 2}, msgqueue.TerminalRemoved)
	for i := 0; i < 140; i++ {
		appendConversationHistory(store, discardLogger(), "test", testConvID, protocol.TypeMessage, json.RawMessage(`{"role":"user","text":"old"}`), time.Unix(2, 0))
	}
	at := time.Unix(5, 0).UTC()
	reconcileStartupHistory(history.New(dir), reg, discardLogger(), at)
	all := testAllSendEntries(t, history.New(dir))
	last := all[len(all)-2]
	p := testSendFact(t, last)
	if last.Type != historySendLost || p.AcceptedEntryID != all[0].ID || p.Reason != "daemon_restart" || !p.OccurredAt.Equal(at) || last.Session.SessionID != "accepted-source" || !*last.Shown {
		t.Fatalf("loss: %+v %+v", last, p)
	}
	newer := history.New(dir)
	next := testSendHistory(newer)
	next.accepted(testConvID, msgqueue.QueuedMessage{ID: 1, Text: "reused"})
	next.terminal(testConvID, msgqueue.QueuedMessage{ID: 1}, msgqueue.TerminalRemoved)
	before := len(testAllSendEntries(t, newer))
	reconcileStartupHistory(history.New(dir), reg, discardLogger(), at)
	tail := testAllSendEntries(t, history.New(dir))[before:]
	if len(tail) != 1 || tail[0].Type != historySessionDivider {
		t.Fatalf("repeated loss: %+v", tail)
	}
}

func TestQueuedSendHistoryFailures(t *testing.T) {
	t.Parallel()
	for _, nilStore := range []bool{true, false} {
		t.Run(map[bool]string{true: "nil", false: "failed"}[nilStore], func(t *testing.T) {
			var store *history.Store
			var logs bytes.Buffer
			var path string
			if !nilStore {
				path = filepath.Join(t.TempDir(), "private-host-path")
				if err := os.WriteFile(path, []byte("block"), 0600); err != nil {
					t.Fatal(err)
				}
				store = history.New(path)
			}
			h := newQueuedSendHistory(store, bufLogger(&logs), nil)
			m := msgqueue.QueuedMessage{ID: 1, Text: "private-message", DeviceID: "private-sender"}
			h.accepted(testConvID, m)
			if !nilStore {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			pushes := 0
			operatorMessageHistory(store, func(m operatorMessage) {
				pushes++
				if (m.historyEntryID == nil) != nilStore {
					t.Error("wrong legacy identity")
				}
			}, nil, bufLogger(&logs), h)(testConvID, m)
			if pushes != 1 {
				t.Fatal("publication lost")
			}
			if !nilStore {
				all := historyEntries(t, store, testConvID)
				if len(all) != 1 || all[0].Type != protocol.TypeMessage {
					t.Fatalf("linked outcome without acceptance: %+v", all)
				}
				if !strings.Contains(logs.String(), "event=send_history.append_err") || !strings.Contains(logs.String(), "reason=write") {
					t.Fatalf("failure discriminant: %s", logs.String())
				}
			}
			for _, secret := range []string{"private-host-path", "private-message", "private-sender"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatal("log leaked")
				}
			}
		})
	}
	dir := t.TempDir()
	store := history.New(dir)
	h := testSendHistory(store)
	h.accepted(testConvID, msgqueue.QueuedMessage{ID: 1})
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: conversations.ConversationID(testConvID)})
	logDir, err := store.LogDir(conversations.ConversationID(testConvID))
	if err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatal(err)
	}
	segment := filepath.Join(logDir, files[0].Name())
	saved, err := os.ReadFile(segment)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(segment, append(saved, []byte("incomplete\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	reconcileStartupHistory(history.New(dir), reg, discardLogger(), time.Now())
	got, err := os.ReadFile(segment)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, append(saved, []byte("incomplete\n")...)) {
		t.Fatal("inferred loss from incomplete read")
	}
}

func TestQueuedSendHistoryLossRetryAndUnbound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := history.New(dir)
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: conversations.ConversationID(testConvID)})
	h := newQueuedSendHistory(store, discardLogger(), channelPostSession(reg, nil))
	h.accepted(testConvID, msgqueue.QueuedMessage{ID: 1, Text: "pending"})
	accepted := historyEntries(t, store, testConvID)
	if accepted[0].Session == nil || accepted[0].Session.Kind != "none" || accepted[0].Session.SessionID != "" {
		t.Fatalf("unbound source: %+v", accepted)
	}
	bad := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(bad, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	closeStartupSends(history.New(bad), discardLogger(), testConvID, accepted, time.Now())
	if len(historyEntries(t, store, testConvID)) != 1 {
		t.Fatal("failed loss became resolved")
	}
	reconcileStartupHistory(history.New(dir), reg, discardLogger(), time.Now())
	all := historyEntries(t, history.New(dir), testConvID)
	if len(all) != 3 || all[1].Type != historySendLost || all[1].Session.Kind != "none" {
		t.Fatalf("retry loss: %+v", all)
	}
	for _, e := range all[:2] {
		if legacyHistoryType(e.Type) || legacyRuntimeFact(e.Type) {
			t.Fatalf("legacy eligible: %s", e.Type)
		}
	}
}
