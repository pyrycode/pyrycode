package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
)

type provenanceWriter struct {
	funcWriter
	mu       sync.Mutex
	source   history.SessionProvenance
	activate func(context.Context) error
}

func (w *provenanceWriter) operatorProvenance() history.SessionProvenance {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.source
}
func (w *provenanceWriter) setSource(s history.SessionProvenance) {
	w.mu.Lock()
	w.source = s
	w.mu.Unlock()
}
func (w *provenanceWriter) Activate(ctx context.Context) error {
	if w.activate != nil {
		return w.activate(ctx)
	}
	return nil
}
func awaitProvenanceSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery barrier timed out")
	}
}

func TestOperatorDeliveryProvenance_Placement(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		now, echo  bool
	}{
		{"ordinary_echo", "claude", false, true}, {"ordinary_idle", "claude", false, false},
		{"send_now_echo", "claude", true, true}, {"send_now_idle", "claude", true, false},
		{"codex_confirmation", "codex", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var logs bytes.Buffer
			logger := bufLogger(&logs)
			dir := t.TempDir()
			store := history.New(dir)
			if _, err := store.Append(conversations.ConversationID(testConvID), protocol.TypeMessage, []byte(`{"text":"legacy"}`), time.Now()); err != nil {
				t.Fatal(err)
			}
			idle := make(chan struct{})
			resolveSession := func(string) (string, bool) { return testConvID, true }
			busy := newTurnBusyTracker(resolveSession, logger, withSendNowGrace(time.Hour))
			place := newSendNowPlacement(ctx, resolveSession, func(ctx context.Context, _ string) error {
				select {
				case <-idle:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			sink := newStreamTurnSink(8, logger)
			// The binding has switched kinds before placement selection. The receiving writer still owns the turn.
			place.bindQueued(ctx, sink, store, func(string) bool { return tc.kind != "claude" }, logger)
			place.dispatch = func(commit func()) { commit() }
			published := make(chan operatorMessage, 4)
			sink.setOperatorPublisher(func(m operatorMessage) { published <- m })
			record := newOperatorMessageHistory(store, func(m operatorMessage) { published <- m }, place, logger)
			received := make(chan struct{}, 1)
			writer := &provenanceWriter{source: history.SessionProvenance{Kind: tc.kind, SessionID: "receiving-B"}}
			writer.write = func(_ context.Context, _ string, payload []byte) error {
				if string(payload) != "composed /host/private attachment" {
					t.Error("writer did not receive composed prompt")
				}
				received <- struct{}{}
				return nil
			}
			writerFor := func(string) (handlers.TurnWriter, error) { return writer, nil }
			callback := make(chan struct{}, 1)
			release := make(chan struct{})
			completed := make(chan struct{})
			q, err := msgqueue.New(msgqueue.Config{
				Deliver: newInboundDeliver(writerFor, busy, time.Second, place),
				SendNow: newSendNowDeliver(writerFor, func(string) bool { return true }, busy, place),
				OnDelivered: func(c string, m msgqueue.QueuedMessage) {
					callback <- struct{}{}
					select {
					case <-release:
					case <-ctx.Done():
						return
					}
					record(c, m)
					close(completed)
				}, Logger: logger,
			})
			if err != nil {
				t.Fatal(err)
			}
			tap := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
			id := q.EnqueueSent(testConvID, "client-message", "safe text", "composed /host/private attachment", []string{"attachment-id"}, "phone", "1.2.3", tap)
			if tc.now {
				busy.observe("receiving-B", snOpener)
				sent := make(chan bool, 1)
				go func() { sent <- q.SendNow(testConvID, id) }()
				defer func() { cancel(); <-sent }()
			} else {
				done := make(chan error, 1)
				go func() { done <- q.Run(ctx) }()
				defer func() { cancel(); <-done }()
			}
			awaitProvenanceSignal(t, received)
			awaitProvenanceSignal(t, callback)
			// Rotation and a subsequent agent switch must not change the committed source.
			writer.setSource(history.SessionProvenance{Kind: map[string]string{"claude": "codex", "codex": "claude"}[tc.kind], SessionID: "successor-C"})
			if tc.kind == "codex" {
				close(release)
				awaitProvenanceSignal(t, completed)
			} else if tc.echo {
				place.echo("receiving-B", echoOf("composed /host/private attachment"))
			} else if tc.now {
				close(idle)
			} else {
				place.idle("receiving-B")
			}
			var pushed operatorMessage
			select {
			case pushed = <-published:
			case <-time.After(5 * time.Second):
				t.Fatal("no placed operator publication")
			}
			if tc.kind != "codex" {
				close(release)
				awaitProvenanceSignal(t, completed)
			}
			place.echo("receiving-B", echoOf("composed /host/private attachment"))
			place.idle("receiving-B")
			want := protocol.MessagePayload{ConversationID: testConvID, MessageID: "client-message", QueuedMsgID: id, Role: "user", Text: "safe text", AttachmentIDs: []string{"attachment-id"}, DeviceName: "phone", ClientVersion: "1.2.3", ClientSentAt: tap.Format(time.RFC3339Nano), SentNow: tc.now}
			var got protocol.MessagePayload
			if err := json.Unmarshal(pushed.payload, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("safe projection=%#v want %#v", got, want)
			}
			if pushed.historyEntryID == nil || *pushed.historyEntryID != 2 {
				t.Fatalf("identity=%v", pushed.historyEntryID)
			}
			source := history.SessionProvenance{Kind: tc.kind, SessionID: "receiving-B"}
			for _, reader := range []*history.Store{store, history.New(dir)} {
				page, err := reader.Page(conversations.ConversationID(testConvID), "", 10)
				if err != nil || len(page.Entries) != 2 {
					t.Fatalf("page=%+v err=%v", page, err)
				}
				e := page.Entries[0]
				if e.Session == nil || *e.Session != source {
					t.Fatalf("source=%+v want %+v", e.Session, source)
				}
				if e.Shown == nil || !*e.Shown || !bytes.Equal(e.Payload, pushed.payload) || !e.TS.Equal(pushed.ts) {
					t.Fatalf("metadata or payload changed: %+v", e)
				}
				if page.Entries[1].Session != nil || page.Entries[1].Shown != nil {
					t.Fatal("legacy entry was rewritten")
				}
				latest, err := reader.LatestDisplayableEntryID(conversations.ConversationID(testConvID))
				if err != nil || latest != 2 {
					t.Fatalf("shown watermark=%d err=%v", latest, err)
				}
			}
			digest := sha256.Sum256([]byte("composed /host/private attachment"))
			for _, secret := range []string{"/host/private", "composed", hex.EncodeToString(digest[:]), "receiving-B", "successor-C"} {
				if bytes.Contains(pushed.payload, []byte(secret)) || bytes.Contains(logs.Bytes(), []byte(secret)) {
					t.Fatalf("private delivery data leaked: %q", secret)
				}
			}
			select {
			case <-published:
				t.Fatal("late callback/echo duplicated publication")
			default:
			}
		})
	}
}

func TestOperatorDeliveryProvenance_WaitsAndRetry(t *testing.T) {
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := history.New(t.TempDir())
			logger := discardLogger()
			resolveSession := func(string) (string, bool) { return testConvID, true }
			busy := newTurnBusyTracker(resolveSession, logger)
			place := newSendNowPlacement(ctx, resolveSession, func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() })
			sink := newStreamTurnSink(8, logger)
			place.bindQueued(ctx, sink, store, func(string) bool { return kind == "claude" }, logger)
			completed := make(chan struct{})
			record := newOperatorMessageHistory(store, nil, place, logger)
			failed := make(chan struct{}, 1)
			activated := make(chan struct{}, 1)
			release := make(chan struct{})
			writer := &provenanceWriter{source: history.SessionProvenance{Kind: kind, SessionID: "enqueue-A"}}
			attempts := 0
			writer.activate = func(ctx context.Context) error {
				if attempts != 0 {
					busy.observe("write-B", snOpener)
					activated <- struct{}{}
					return nil
				}
				activated <- struct{}{}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			writer.write = func(context.Context, string, []byte) error {
				attempts++
				if attempts == 1 {
					failed <- struct{}{}
					return errors.New("write refused")
				}
				return nil
			}
			resolved := make(chan struct{}, 2)
			q, err := msgqueue.New(msgqueue.Config{
				Deliver: newInboundDeliver(func(string) (handlers.TurnWriter, error) { resolved <- struct{}{}; return writer, nil }, busy, time.Second, place),
				OnDelivered: func(c string, m msgqueue.QueuedMessage) {
					record(c, m)
					if kind == "claude" {
						place.idle("retry-C")
					}
					close(completed)
				},
				RetryInterval: time.Millisecond, Logger: logger,
			})
			if err != nil {
				t.Fatal(err)
			}
			q.EnqueueDelivery(testConvID, "client-id", "safe retry", "safe retry")
			done := make(chan error, 1)
			go func() { done <- q.Run(ctx) }()
			defer func() { cancel(); <-done }()
			awaitProvenanceSignal(t, resolved)
			awaitProvenanceSignal(t, activated)
			writer.setSource(history.SessionProvenance{Kind: kind, SessionID: "write-B"})
			close(release)
			awaitProvenanceSignal(t, failed)
			awaitProvenanceSignal(t, activated)
			// The second resolve parks at idle, giving the test a deterministic retry boundary.
			awaitProvenanceSignal(t, resolved)
			writer.setSource(history.SessionProvenance{Kind: kind, SessionID: "retry-C"})
			busy.clearForSession("write-B")
			awaitProvenanceSignal(t, completed)
			page, err := store.Page(conversations.ConversationID(testConvID), "", 10)
			if err != nil || len(page.Entries) != 1 {
				t.Fatalf("page=%+v err=%v", page, err)
			}
			want := history.SessionProvenance{Kind: kind, SessionID: "retry-C"}
			if page.Entries[0].Session == nil || *page.Entries[0].Session != want {
				t.Fatalf("retry source=%+v want %+v", page.Entries[0].Session, want)
			}
		})
	}
}

type waitingBoundWriter struct {
	boundSession
	activated chan struct{}
	release   <-chan struct{}
}

func (w waitingBoundWriter) Activate(ctx context.Context) error {
	w.activated <- struct{}{}
	select {
	case <-w.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestOperatorDeliveryProvenance_ReceivingSessionRotation(t *testing.T) {
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			pool := newRouterTestPool(t)
			runPoolReady(t, pool)
			receiving, err := pool.MintAs("receiving", "", kind)
			if err != nil {
				t.Fatal(err)
			}
			reg := &conversations.Registry{}
			reg.Create(conversations.Conversation{ID: conversations.ConversationID(testConvID), CurrentSessionID: string(pool.Default().ID())})
			router := sessionRouter{pool: pool, convReg: reg}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := history.New(t.TempDir())
			sink := newStreamTurnSink(8, discardLogger())
			resolveSession := func(string) (string, bool) { return testConvID, true }
			place := newSendNowPlacement(ctx, resolveSession, func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() })
			place.bindQueued(ctx, sink, store, func(string) bool { return kind == "claude" }, discardLogger())
			activated := make(chan struct{}, 1)
			release := make(chan struct{})
			callback := make(chan struct{}, 1)
			confirm := make(chan struct{})
			completed := make(chan struct{})
			record := newOperatorMessageHistory(store, nil, place, discardLogger())
			q, err := msgqueue.New(msgqueue.Config{
				Deliver: newInboundDeliver(func(c string) (handlers.TurnWriter, error) {
					w, err := router.resolve(c)
					if err != nil {
						return nil, err
					}
					return waitingBoundWriter{boundSession: w.(boundSession), activated: activated, release: release}, nil
				}, nil, time.Second, place),
				OnDelivered: func(c string, m msgqueue.QueuedMessage) {
					callback <- struct{}{}
					select {
					case <-confirm:
					case <-ctx.Done():
						return
					}
					record(c, m)
					place.idle("rotated-at-write")
					close(completed)
				}, Logger: discardLogger(),
			})
			if err != nil {
				t.Fatal(err)
			}
			q.Enqueue(testConvID, "safe")
			// Queue under A, resolve B, then rotate that exact receiving session while activation waits.
			reg.Update(conversations.ConversationID(testConvID), func(c *conversations.Conversation) { c.CurrentSessionID = string(receiving) })
			done := make(chan error, 1)
			go func() { done <- q.Run(ctx) }()
			defer func() { cancel(); <-done }()
			awaitProvenanceSignal(t, activated)
			if err := pool.RotateID(receiving, "rotated-at-write"); err != nil {
				t.Fatal(err)
			}
			reg.Update(conversations.ConversationID(testConvID), func(c *conversations.Conversation) { c.CurrentSessionID = string(pool.Default().ID()) })
			close(release)
			awaitProvenanceSignal(t, callback)
			if err := pool.RotateID("rotated-at-write", "rotated-after-write"); err != nil {
				t.Fatal(err)
			}
			close(confirm)
			awaitProvenanceSignal(t, completed)
			page, err := store.Page(conversations.ConversationID(testConvID), "", 10)
			if err != nil || len(page.Entries) != 1 {
				t.Fatalf("page=%+v err=%v", page, err)
			}
			want := history.SessionProvenance{Kind: kind, SessionID: "rotated-at-write"}
			if page.Entries[0].Session == nil || *page.Entries[0].Session != want {
				t.Fatalf("source=%+v want %+v", page.Entries[0].Session, want)
			}
		})
	}
}
