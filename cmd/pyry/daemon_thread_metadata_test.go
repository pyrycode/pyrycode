package main

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/format"
	"go/token"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func testThreadMetadataFixture(t testing.TB, beforeFold ...func(context.Context, conversations.ConversationID) error) (*threadShadow, *thread.Store, *daemonThreadPublisher, *shadowConversation, relay.V2SessionConfig) {
	t.Helper()
	fixtureStart := time.Now()
	h := history.New(t.TempDir())
	reg, err := conversations.Load("")
	if err != nil {
		t.Fatal(err)
	}
	reg.Create(conversations.Conversation{ID: testConvID})
	raw, err := json.Marshal(map[string]string{"role": "user", "text": strings.Repeat("x", 4096)})
	if err != nil {
		t.Fatal(err)
	}
	for range 1024 {
		if _, err := h.Append(testConvID, "message", raw, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	t.Logf("fixture construction=%s", time.Since(fixtureStart))
	replayStart := time.Now()
	chunks := 0
	progress := make(chan struct{}, 1)
	store := thread.NewStore(h, func(ctx context.Context, id conversations.ConversationID) error {
		chunks++
		t.Logf("replay chunk=%d elapsed=%s", chunks, time.Since(replayStart))
		if len(beforeFold) > 0 {
			if err := beforeFold[0](ctx, id); err != nil {
				return err
			}
		}
		select {
		case progress <- struct{}{}:
		default:
		}
		return nil
	})
	t.Cleanup(func() {
		defer cancel()
		if err := store.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Load(workerCtx, testConvID); err != nil {
		t.Fatal(err)
	}
	readyCtx, readyCancel := context.WithTimeout(t.Context(), time.Minute)
	defer readyCancel()
	if err := testWaitThreadMetadata(readyCtx, store, progress, 10*time.Second); err != nil {
		t.Fatalf("long-thread replay: %v; elapsed=%s status=%+v", err, time.Since(replayStart), store.Status(testConvID))
	}
	t.Logf("replay/readiness=%s", time.Since(replayStart))
	validationStart := time.Now()
	snapshot := store.Snapshot(testConvID)
	if snapshot.State != thread.StateUsable || len(snapshot.Items) != 1024 || snapshot.Version != 1024 || snapshot.LastShownVersion != 1024 {
		t.Fatalf("fixture publication: state=%v items=%d version=%d shown=%d", snapshot.State, len(snapshot.Items), snapshot.Version, snapshot.LastShownVersion)
	}
	t.Logf("snapshot validation=%s", time.Since(validationStart))
	var contentBytes int
	for _, item := range snapshot.Items {
		contentBytes += len(item.Content)
	}
	t.Logf("loaded fixture: %d items, %d content bytes (4096 text bytes/item)", len(snapshot.Items), contentBytes)
	s := newThreadShadow(h, reg, store, discardLogger())
	p := &daemonThreadPublisher{store: store, active: func(context.Context, string) []relay.ActiveConn { return nil }}
	w := &shadowConversation{}
	p.publish(t.Context(), testConvID, w)
	var cfg relay.V2SessionConfig
	installDaemonThreadRelay(&cfg, s)
	return s, store, p, w, cfg
}

type testThreadStatusRace struct {
	*thread.Store
	afterStatus func()
}

func (s *testThreadStatusRace) Status(id conversations.ConversationID) thread.Snapshot {
	status := s.Store.Status(id)
	if s.afterStatus != nil {
		after := s.afterStatus
		s.afterStatus = nil
		after()
	}
	return status
}

func TestDaemonThreadStatusInterleaving(t *testing.T) {
	for _, tc := range []struct {
		name, action       string
		initial, unchanged bool
	}{
		{name: "initial advance", action: "append", initial: true},
		{name: "initial withdrawal", action: "unload", initial: true},
		{name: "changed advance", action: "append"},
		{name: "changed withdrawal", action: "unload"},
		{name: "same epoch reload gap", action: "reload"},
		{name: "unchanged then advance", action: "append", unchanged: true},
		{name: "unchanged then withdrawal", action: "unload", unchanged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, h, reg, store := testShadow(t)
			reg.Create(conversations.Conversation{ID: testConvID})
			testShadowAppend(t, h, testConvID)
			if err := store.Load(t.Context(), testConvID); err != nil {
				t.Fatal(err)
			}
			testShadowWait(t, func() bool { return store.Status(testConvID).Version == 1 })
			var frames []protocol.Envelope
			retired := 0
			raced := false
			wrapped := &testThreadStatusRace{Store: store}
			p := &daemonThreadPublisher{store: wrapped, log: discardLogger(),
				active: func(context.Context, string) []relay.ActiveConn {
					return []relay.ActiveConn{{ConnID: "phone", Interactive: true, Thread: true}}
				},
				push: func(_ context.Context, _ string, e protocol.Envelope) error {
					if raced && tc.action == "reload" && retired != 1 {
						t.Fatal("replacement baseline preceded gap retirement")
					}
					frames = append(frames, e)
					return nil
				},
				retire: func(context.Context, string) error { retired++; return nil },
			}
			w := &shadowConversation{}
			if !tc.initial {
				p.publish(t.Context(), testConvID, w)
				frames = nil
				if !tc.unchanged {
					testShadowAppend(t, h, testConvID)
					testShadowWait(t, func() bool { return store.Status(testConvID).Version == 2 })
				}
			}
			wrapped.afterStatus = func() {
				raced = true
				if tc.action != "append" {
					if err := store.Unload(testConvID); err != nil {
						t.Fatal(err)
					}
				}
				if tc.action == "unload" {
					return
				}
				version := testShadowAppend(t, h, testConvID)
				if tc.action == "reload" {
					if err := store.Load(t.Context(), testConvID); err != nil {
						t.Fatal(err)
					}
				}
				testShadowWait(t, func() bool { return store.Status(testConvID).Version == version })
			}
			p.publish(t.Context(), testConvID, w)
			if tc.unchanged {
				if len(frames) != 0 || w.threadVersion != 1 {
					t.Fatal("unchanged hint changed the cursor or published")
				}
				p.publish(t.Context(), testConvID, w)
			}
			wantRetired := 0
			if !tc.initial && tc.action != "append" {
				wantRetired = 1
			}
			if retired != wantRetired {
				t.Fatalf("retired=%d want %d", retired, wantRetired)
			}
			if tc.action == "unload" {
				if len(frames) != 0 || w.threadEpoch != "" {
					t.Fatal("withdrawn observation published or retained a cursor")
				}
				return
			}
			current := store.Observe(testConvID)
			if w.threadEpoch != current.Epoch || w.threadVersion != current.Version {
				t.Fatalf("cursor=%s/%d want %s/%d", w.threadEpoch, w.threadVersion, current.Epoch, current.Version)
			}
			wantFrames := 1
			if tc.initial || tc.action == "reload" {
				wantFrames = len(current.Items)
			} else if !tc.unchanged {
				wantFrames = 2
			}
			if len(frames) != wantFrames {
				t.Fatalf("updates=%d want %d", len(frames), wantFrames)
			}
			for _, frame := range frames {
				var added protocol.ThreadItemAddedPayload
				if err := json.Unmarshal(frame.Payload, &added); err != nil {
					t.Fatal(err)
				}
				wantVersion := added.Item.Rev
				if tc.initial || tc.action == "reload" {
					wantVersion = current.Version
				}
				if frame.Type != protocol.TypeThreadItemAdded || added.Epoch != current.Epoch || added.Version != wantVersion || added.Item.Rev != added.Item.ID {
					t.Fatalf("mixed observation output: %#v", added)
				}
				if added.Item.ID == 0 || added.Item.ID > uint64(len(current.Items)) || !reflect.DeepEqual(added.Item, daemonThreadItem(current.Items[added.Item.ID-1])) {
					t.Fatalf("observation item mismatch: %#v", added.Item)
				}
			}
		})
	}
}

func TestDaemonThreadMetadataAllocations(t *testing.T) {
	s, store, p, w, cfg := testThreadMetadataFixture(t)
	for name, read := range map[string]func(){
		"unchanged publication": func() { p.publish(t.Context(), testConvID, w) },
		"installed list lookup": func() {
			if v, ok := cfg.ThreadLastShownVersion(testConvID); !ok || v != 1024 {
				t.Fatalf("watermark=%d usable=%v", v, ok)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			if allocs := testing.AllocsPerRun(10, read); allocs != 0 {
				t.Fatalf("metadata copied baseline: %.0f allocations/read", allocs)
			}
		})
	}
	s.registry.Delete(testConvID)
	if v, ok := cfg.ThreadLastShownVersion(testConvID); ok || v != 0 || store.Status(testConvID).State != thread.StateUsable {
		t.Fatal("loaded unregistered conversation supplied a watermark")
	}
}

func TestThreadMetadataReadSources(t *testing.T) {
	for _, tc := range []struct{ file, symbol string }{
		{"thread_shadow.go", "follow"},
		{"daemon_thread_relay.go", "installDaemonThreadRelay"},
	} {
		source := formattedGoFunc(t, tc.file, tc.symbol)
		if !strings.Contains(source, ".Status(") || strings.Contains(source, ".Snapshot(") || strings.Contains(source, ".Observe(") || strings.Contains(source, ".Items") {
			t.Errorf("%s must inspect status without reading/traversing items", tc.symbol)
		}
	}
	source := formattedGoFunc(t, "daemon_thread_relay.go", "publish")
	// Only the publisher's declaration has a store read; the shadow wrapper
	// delegates without inspecting items. Parse the declaration by receiver.
	if !strings.Contains(source, "p.store") {
		file := parseGoFile(t, "daemon_thread_relay.go")
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Name.Name == "publish" && fn.Recv != nil {
				var formatted bytes.Buffer
				if err := format.Node(&formatted, token.NewFileSet(), fn); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(formatted.String(), "p.store") {
					source = formatted.String()
				}
			}
		}
	}
	end := strings.Index(source, "var err error")
	if end < 0 {
		t.Fatal("publisher's changed-publication boundary missing")
	}
	prefix := source[:end]
	if !strings.Contains(prefix, ".Status(") || strings.Contains(prefix, ".Observe(") || strings.Contains(prefix, ".Items") || strings.Contains(prefix, ".Snapshot(") {
		t.Fatal("publisher metadata check reads/traverses baseline items")
	}
}

func BenchmarkDaemonThreadMetadata(b *testing.B) {
	_, store, p, w, cfg := testThreadMetadataFixture(b)
	for name, read := range map[string]func(){
		"UnchangedPublication": func() { p.publish(b.Context(), testConvID, w) },
		"InstalledListLookup":  func() { cfg.ThreadLastShownVersion(testConvID) },
		"ShadowStatus":         func() { store.Status(testConvID) },
		"BaselineSnapshot":     func() { store.Snapshot(testConvID) },
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				read()
			}
		})
	}
}
