package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestDaemonThreadExpansionInstallation(t *testing.T) {
	s, _, reg, _ := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	cfg := relay.V2SessionConfig{}
	installDaemonThreadRelay(&cfg, s)
	if cfg.ThreadToolBody == nil || cfg.ThreadChildren == nil {
		t.Fatal("expansion providers not installed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := protocol.ThreadExpansionRequest{ConversationID: testConvID}
	if _, err := cfg.ThreadToolBody(ctx, r); !errors.Is(err, context.Canceled) {
		t.Fatal("body context lost", err)
	}
	if _, err := cfg.ThreadChildren(ctx, r); !errors.Is(err, context.Canceled) {
		t.Fatal("children context lost", err)
	}
	installDaemonThreadRelay(&cfg, nil)
	if cfg.ThreadToolBody != nil || cfg.ThreadChildren != nil || cfg.ThreadReady != nil {
		t.Fatal("cleared installation retained expansion callbacks")
	}
}

// Preparation may render the complete oracle; requests below must not do so.
func testDaemonExpansionFixture(t *testing.T, n, descendants, bodyBytes int) (*threadShadow, *thread.Store, []thread.Item) {
	t.Helper()
	s, h, reg, store := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	s.quiet = func(conversations.ConversationID) bool { return false }
	appendRow := func(typ, payload string) { testDaemonThreadAppend(t, h, typ, payload) }
	appendRow("tool_use", `{"turn_id":"t","tool_use_id":"root","name":"Agent","input":{},"future":[1,2]}`)
	appendRow("tool_use", `{"turn_id":"t","tool_use_id":"ordinary","name":"Bash","input":{"command":"echo hi"},"future":true}`)
	appendRow("tool_result", `{"turn_id":"t","tool_use_id":"ordinary","output":"ok","is_error":false,"future_result":42}`)
	appendRow("tool_use", `{"turn_id":"t","parent_tool_use_id":"root","tool_use_id":"nested","name":"Agent","input":{"prompt":"nested"}}`)
	for i := range n {
		appendRow("tool_use", fmt.Sprintf(`{"turn_id":"t","parent_tool_use_id":"root","tool_use_id":"c%d","name":%q,"input":{"secret":"unopened-detail"}}`, i, strings.Repeat("<", 180)))
	}
	for i := range descendants {
		appendRow("tool_use", fmt.Sprintf(`{"turn_id":"t","parent_tool_use_id":"nested","tool_use_id":"d%d","name":"Read","input":{"secret":%s}}`, i, mustQueryJSON(t, strings.Repeat("unopened-descendant", bodyBytes/19+1))))
	}
	appendRow("tool_use", `{"turn_id":"t","tool_use_id":"big","name":"Read","input":{"escaped":`+string(mustQueryJSON(t, strings.Repeat("<\\\"\n", 18000)))+`}}`)
	appendRow("tool_use", `{"turn_id":"t","parent_tool_use_id":"absent","tool_use_id":"private","name":"Read"}`)
	v, err := h.LatestEntryID(testConvID)
	if err != nil {
		t.Fatal(err)
	}
	testDaemonThreadLoad(t, store, v)
	return s, store, store.Snapshot(testConvID).Items
}

func testDaemonExpansionScope(t *testing.T, store *thread.Store, items []thread.Item, name string) protocol.ThreadExpansionRequest {
	t.Helper()
	for _, item := range items {
		var fields map[string]json.RawMessage
		if json.Unmarshal(item.Content, &fields) == nil && string(fields["tool_use_id"]) == string(mustQueryJSON(t, name)) {
			return protocol.ThreadExpansionRequest{ConversationID: testConvID, Epoch: store.Status(testConvID).Epoch, ItemID: item.ID, ExpectedRev: item.Rev, Limit: 2}
		}
	}
	t.Fatalf("fixture has no %s", name)
	return protocol.ThreadExpansionRequest{}
}

func testDaemonExpansionReply(t *testing.T, w *switchWire, peer string, id uint64, typ string) (protocol.Envelope, int, int) {
	t.Helper()
	total, frames := 0, 0
	var fragment string
	var content json.RawMessage
	var rows []protocol.ThreadSummary
	for {
		routing, err := testDaemonThreadFrame(w, time.Now().Add(10*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if routing.CloseCode != 0 && routing.ConnID != peer {
			continue // Epoch replacement retires the previous authenticated peer.
		}
		gotPeer, e := w.decode(routing)
		raw, err := json.Marshal(e)
		if err != nil || gotPeer != peer || e.InReplyTo == nil || *e.InReplyTo != id || e.ID == 0 || e.TS.IsZero() || len(raw) > protocol.MaxThreadEnvelopeBytes {
			t.Fatalf("invalid expansion envelope: %s %+v %v", gotPeer, e, err)
		}
		total += len(raw)
		frames++
		if e.Type == protocol.TypeError {
			if frames != 1 || !bytes.Contains(e.Payload, []byte("thread.query_failed")) {
				t.Fatal("partial or wrong refusal", string(e.Payload))
			}
			return e, total, frames
		}
		if e.Type != typ {
			t.Fatalf("unexpected expansion type %s", e.Type)
		}
		var part protocol.ThreadUpdatePart
		if err := json.Unmarshal(e.Payload, &part); err != nil {
			t.Fatal(err)
		}
		if part.Continuation != nil {
			c := part.Continuation
			if c.Offset != uint64(len(fragment)) {
				t.Fatal("fragment offset")
			}
			fragment += part.Data
			if !c.Final {
				continue
			}
			if c.TotalBytes != uint64(len(fragment)) {
				t.Fatal("fragment length")
			}
			e.Payload, fragment = json.RawMessage(fragment), ""
		}
		if typ == protocol.TypeThreadToolBody {
			var p protocol.ThreadToolBodyPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if !p.Final {
				content = p.Content
				continue
			}
			if len(p.Content) != 0 || len(content) == 0 || fragment != "" {
				t.Fatal("invalid body completion")
			}
			p.Content = content
			e.Payload = mustQueryJSON(t, p)
		} else {
			var p protocol.ThreadChildrenPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if !p.Final {
				if p.More != nil || p.Cursor != nil {
					t.Fatal("data certified completion")
				}
				rows = append(rows, p.Children...)
				continue
			}
			if len(p.Children) != 0 || p.More == nil || (*p.More != (p.Cursor != nil)) || total > protocol.MaxThreadEnvelopeBytes {
				t.Fatal("invalid child completion/cap")
			}
			p.Children = rows
			e.Payload = mustQueryJSON(t, p)
		}
		return e, total, frames
	}
}

func TestDaemonThreadExpansionWire(t *testing.T) {
	s, store, items := testDaemonExpansionFixture(t, 5, 3, 64)
	w, mgr := testDaemonThreadQueryWire(t, s, nil)
	w.open("phone", false, protocol.CapabilityThread, protocol.CapabilityThreadExpansion)
	w.open("legacy", false, protocol.CapabilityThread)
	id := uint64(700)
	for _, name := range []string{"ordinary", "root", "big"} {
		r := testDaemonExpansionScope(t, store, items, name)
		id++
		testDaemonThreadRequest(t, w, "phone", id, protocol.TypeThreadReadToolBody, r)
		e, size, frames := testDaemonExpansionReply(t, w, "phone", id, protocol.TypeThreadToolBody)
		var got protocol.ThreadToolBodyPayload
		if json.Unmarshal(e.Payload, &got) != nil || !got.Final || got.ItemID != r.ItemID || got.Rev != r.ExpectedRev || got.Epoch != r.Epoch {
			t.Fatal("body scope lost", string(e.Payload))
		}
		for _, item := range items {
			if item.ID == r.ItemID && (got.Kind != item.Kind || !reflect.DeepEqual(got.Content, item.Content)) {
				t.Fatal("captured body fields/kind differ")
			}
		}
		if name == "big" && frames <= 2 {
			t.Fatal("escaped fixture did not assemble")
		}
		t.Logf("body=%s delivered_bytes=%d frames=%d", name, size, frames)
	}
	for _, name := range []string{"root", "nested"} {
		r := testDaemonExpansionScope(t, store, items, name)
		seen := map[uint64]bool{}
		for {
			id++
			testDaemonThreadRequest(t, w, "phone", id, protocol.TypeThreadReadChildren, r)
			e, _, _ := testDaemonExpansionReply(t, w, "phone", id, protocol.TypeThreadChildren)
			var p protocol.ThreadChildrenPayload
			if json.Unmarshal(e.Payload, &p) != nil || e.Type == protocol.TypeError || p.ItemID != r.ItemID || p.Rev != r.ExpectedRev || p.Epoch != r.Epoch || len(p.Children) > r.Limit {
				t.Fatal("child scope lost", string(e.Payload))
			}
			for _, row := range p.Children {
				if row.Parent != r.ItemID || !row.DetailOmitted || seen[row.ID] || bytes.Contains(e.Payload, []byte("unopened-")) {
					t.Fatal("child ownership/details/duplicate")
				}
				seen[row.ID] = true
			}
			if !*p.More {
				break
			}
			if len(p.Children) == 0 || p.Cursor.ID != p.Children[len(p.Children)-1].ID {
				t.Fatal("cursor not last delivered row")
			}
			r.Cursor = p.Cursor
		}
		want := 0
		for _, item := range items {
			if item.Parent == r.ItemID {
				want++
				if !seen[item.ID] {
					t.Fatal("missing child")
				}
			}
		}
		if len(seen) != want {
			t.Fatal("descendants included")
		}
	}
	// Expansion completion must not suppress a subsequent full recovery or live update.
	testDaemonThreadRequest(t, w, "legacy", 750, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID})
	full, typ := testDaemonThreadReply(t, w, 750, "legacy")
	if typ != protocol.TypeThreadItems || !full.Final {
		t.Fatal("complete client lost recovery")
	}
	before := store.Status(testConvID).Version
	testDaemonThreadAppend(t, s.history, "message", `{"role":"user","text":"live-after-expansion"}`)
	testDaemonThreadLoad(t, store, before+1)
	for range 2 {
		peer, e := w.read()
		if (peer != "phone" && peer != "legacy") || e.Type != protocol.TypeThreadItemAdded || !bytes.Contains(e.Payload, []byte("live-after-expansion")) {
			t.Fatal("live content/checkpoint suppressed", peer, e.Type)
		}
	}
	if len(mgr.ActiveConns(t.Context())) != 2 {
		t.Fatal("expansion retired complete client")
	}
	other := conversations.ConversationID(testPostID(t))
	s.registry.Create(conversations.Conversation{ID: other})
	if _, err := s.history.Append(other, "tool_use", []byte(`{"turn_id":"other","tool_use_id":"root","name":"Bash","input":{"owner":"second-conversation"}}`), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Load(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool { return store.Status(other).State == thread.StateUsable })
	w.open("again", false, protocol.CapabilityThread, protocol.CapabilityThreadExpansion)
	r := protocol.ThreadExpansionRequest{ConversationID: string(other), Epoch: store.Status(other).Epoch, ItemID: 1, ExpectedRev: 1}
	testDaemonThreadRequest(t, w, "again", 760, protocol.TypeThreadReadToolBody, r)
	e, _, _ := testDaemonExpansionReply(t, w, "again", 760, protocol.TypeThreadToolBody)
	if !bytes.Contains(e.Payload, []byte("second-conversation")) || bytes.Contains(e.Payload, []byte("echo hi")) {
		t.Fatal("named conversation lost")
	}
}

func TestDaemonThreadExpansionRefusals(t *testing.T) {
	s, store, items := testDaemonExpansionFixture(t, 4, 1, 8)
	w, _ := testDaemonThreadQueryWire(t, s, nil)
	w.open("phone", false, protocol.CapabilityThread, protocol.CapabilityThreadExpansion)
	r := testDaemonExpansionScope(t, store, items, "root")
	testDaemonThreadRequest(t, w, "phone", 800, protocol.TypeThreadReadChildren, r)
	e, _, _ := testDaemonExpansionReply(t, w, "phone", 800, protocol.TypeThreadChildren)
	var page protocol.ThreadChildrenPayload
	if json.Unmarshal(e.Payload, &page) != nil || page.Cursor == nil {
		t.Fatal("missing fixture cursor")
	}
	r.Cursor = page.Cursor
	testDaemonThreadAppend(t, s.history, "unknown", `{}`)
	testDaemonThreadLoad(t, store, page.Version+1)
	for i, scenario := range []string{"changed page", "epoch", "missing", "private", "conversation"} {
		bad := r
		typ := protocol.TypeThreadReadToolBody
		switch scenario {
		case "changed page":
			typ = protocol.TypeThreadReadChildren
		case "epoch":
			bad.Epoch = "replacement"
		case "missing":
			bad.ItemID = 999999
		case "private":
			bad.ItemID = store.Status(testConvID).Version - 1
			bad.ExpectedRev = bad.ItemID
		case "conversation":
			bad.ConversationID = testPostID(t)
		}
		if typ == protocol.TypeThreadReadToolBody {
			bad.Cursor = nil
		}
		id := uint64(801 + i)
		testDaemonThreadRequest(t, w, "phone", id, typ, bad)
		e, _, _ := testDaemonExpansionReply(t, w, "phone", id, protocol.TypeThreadToolBody)
		if e.Type != protocol.TypeError {
			t.Fatal("accepted", scenario)
		}
	}
	// Replace the actual Store epoch, rather than only sending a wrong epoch.
	if err := store.Unload(testConvID); err != nil {
		t.Fatal(err)
	}
	dir, err := s.history.EnsureLogDir(testConvID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "thread-certificate.json")); err != nil {
		t.Fatal(err)
	}
	testDaemonThreadLoad(t, store, page.Version+1)
	if store.Status(testConvID).Epoch == r.Epoch {
		t.Fatal("fixture did not replace epoch")
	}
	// The existing publisher correctly retires clients on replacement. A new
	// authenticated client must still refuse the old capture and its cursor.
	w.open("again", false, protocol.CapabilityThread, protocol.CapabilityThreadExpansion)
	testDaemonThreadRequest(t, w, "again", 810, protocol.TypeThreadReadChildren, r)
	e, _, _ = testDaemonExpansionReply(t, w, "again", 810, protocol.TypeThreadChildren)
	if e.Type != protocol.TypeError {
		t.Fatal("old epoch cursor survived replacement")
	}
}

func TestDaemonThreadExpansionPartialInstallation(t *testing.T) {
	for _, missing := range []string{"body", "children"} {
		t.Run(missing, func(t *testing.T) {
			s, store, items := testDaemonExpansionFixture(t, 1, 1, 8)
			w, _ := testDaemonThreadQueryWire(t, s, func(c *relay.V2SessionConfig) {
				if missing == "body" {
					c.ThreadToolBody = nil
				} else {
					c.ThreadChildren = nil
				}
			})
			w.open("phone", false, protocol.CapabilityThread, protocol.CapabilityThreadExpansion)
			r := testDaemonExpansionScope(t, store, items, "root")
			for i, typ := range []string{protocol.TypeThreadReadToolBody, protocol.TypeThreadReadChildren} {
				id := uint64(820 + i)
				testDaemonThreadRequest(t, w, "phone", id, typ, r)
				e, _, _ := testDaemonExpansionReply(t, w, "phone", id, protocol.TypeThreadChildren)
				if e.Type != protocol.TypeError {
					t.Fatal("partial installation granted expansion")
				}
			}
			testDaemonThreadRequest(t, w, "phone", 825, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID})
			p, typ := testDaemonThreadReply(t, w, 825)
			if typ != protocol.TypeThreadItems || !p.Final {
				t.Fatal("partial expansion disabled complete thread")
			}
		})
	}
}

func TestDaemonThreadExpansionBounded(t *testing.T) {
	for _, growth := range []struct{ n, bytes int }{{8, 64}, {512, 4096}} {
		t.Run(fmt.Sprint(growth.n), func(t *testing.T) {
			s, store, items := testDaemonExpansionFixture(t, 300, growth.n, growth.bytes)
			w, _ := testDaemonThreadQueryWire(t, s, nil)
			w.open("phone", false, protocol.CapabilityThread, protocol.CapabilityThreadExpansion)
			r := testDaemonExpansionScope(t, store, items, "root")
			r.Limit = 9999 // relay normalization and Store hard cap
			seen := map[uint64]bool{}
			for id := uint64(900); ; id++ {
				testDaemonThreadRequest(t, w, "phone", id, protocol.TypeThreadReadChildren, r)
				e, size, frames := testDaemonExpansionReply(t, w, "phone", id, protocol.TypeThreadChildren)
				var p protocol.ThreadChildrenPayload
				if json.Unmarshal(e.Payload, &p) != nil || e.Type == protocol.TypeError || len(p.Children) > 256 || frames > 2 {
					t.Fatal("unbounded child response", string(e.Payload))
				}
				for _, row := range p.Children {
					if seen[row.ID] || row.Parent != r.ItemID {
						t.Fatal("skipped/duplicate ownership")
					}
					seen[row.ID] = true
				}
				t.Logf("prepared_descendants=%d body_bytes=%d delivered_rows=%d delivered_bytes=%d frames=%d", growth.n, growth.bytes, len(p.Children), size, frames)
				if id == 900 && (len(p.Children) >= 256 || !*p.More) {
					t.Fatal("fixture did not shorten page by bytes")
				}
				if !*p.More {
					break
				}
				r.Cursor = p.Cursor
			}
			if len(seen) != 301 {
				t.Fatal("byte-shortened pages skipped selected candidates", len(seen))
			}
		})
	}
	source := formattedGoFunc(t, "daemon_thread_relay.go", "installDaemonThreadRelay")
	for _, api := range []string{"store.ToolBody(ctx,", "store.ChildPage(ctx,"} {
		if strings.Count(source, api) != 1 {
			t.Fatal("expansion must select exactly once", api)
		}
	}
	for _, api := range []string{"store.Snapshot(", "store.Observe(", "store.NewestWindow("} {
		if strings.Contains(source, api) {
			t.Fatal("unbounded expansion baseline", api)
		}
	}
}

func TestDaemonThreadExpansionDemand(t *testing.T) {
	s, h, reg, _ := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	testDaemonThreadAppend(t, h, "tool_use", `{"turn_id":"t","tool_use_id":"root","name":"Agent"}`)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	store := thread.NewStore(h, func(ctx context.Context, _ conversations.ConversationID) error {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	s.store = store
	cfg := relay.V2SessionConfig{}
	installDaemonThreadRelay(&cfg, s)
	if cfg.ThreadToolBody == nil || cfg.ThreadChildren == nil {
		t.Fatal("providers missing")
	}
	ctx, cancel := context.WithCancel(t.Context())
	first, second := make(chan error, 1), make(chan error, 1)
	r := protocol.ThreadExpansionRequest{ConversationID: testConvID, Epoch: "unknown-before-load", ItemID: 1, ExpectedRev: 1, Limit: 1}
	go func() { _, err := cfg.ThreadToolBody(ctx, r); first <- err }()
	<-entered
	go func() { _, err := cfg.ThreadChildren(t.Context(), r); second <- err }()
	testShadowWait(t, func() bool {
		s.quietMu.Lock()
		defer s.quietMu.Unlock()
		return s.queryDemands[testConvID] != nil && s.queryDemands[testConvID].count == 2
	})
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if store.Status(testConvID).State != thread.StateRebuilding {
		t.Fatal("canceled shared worker")
	}
	close(release)
	if err := <-second; !errors.Is(err, thread.ErrStaleChildPage) {
		t.Fatal("second did not finish its own read", err)
	}
	testShadowWait(t, func() bool { s.quietMu.Lock(); defer s.quietMu.Unlock(); return len(s.queryDemands) == 0 })
	// Retry after idle reload with the actual persisted scope.
	r.Epoch = store.Status(testConvID).Epoch
	if r.Epoch == "" {
		t.Fatal("shared reader did not load the Store")
	}
	got, err := cfg.ThreadToolBody(t.Context(), r)
	if err != nil || got.State != thread.StateUsable {
		t.Fatal("body read after shared recovery", err)
	}
}

func TestDaemonThreadExpansionIdleAndUnavailable(t *testing.T) {
	s, store, items := testDaemonExpansionFixture(t, 1, 1, 8)
	r := testDaemonExpansionScope(t, store, items, "root")
	cfg := relay.V2SessionConfig{}
	installDaemonThreadRelay(&cfg, s)
	for _, kind := range []string{"body", "children"} {
		if err := store.Unload(testConvID); err != nil {
			t.Fatal(err)
		}
		if store.Status(testConvID).State != thread.StateNotLoaded {
			t.Fatal("fixture still loaded")
		}
		if kind == "body" {
			got, err := cfg.ThreadToolBody(t.Context(), r)
			if err != nil || got.State != thread.StateUsable || got.Kind != "agent" {
				t.Fatal("idle body", err)
			}
		} else {
			got, err := cfg.ThreadChildren(t.Context(), r)
			if err != nil || got.State != thread.StateUsable || len(got.Items) == 0 {
				t.Fatal("idle children", err)
			}
		}
	}
	failed := thread.NewStore(s.history, func(context.Context, conversations.ConversationID) error {
		return errors.New("fixture unavailable history")
	})
	t.Cleanup(func() { _ = failed.Shutdown() })
	// Keep the original worker owner intact; install a separate failed shadow.
	bad := newThreadShadow(s.history, s.registry, failed, discardLogger())
	t.Cleanup(func() { _ = bad.shutdown() })
	installDaemonThreadRelay(&cfg, bad)
	if _, err := cfg.ThreadToolBody(t.Context(), r); !errors.Is(err, thread.ErrUnavailable) {
		t.Fatal("unavailable body", err)
	}
	if _, err := cfg.ThreadChildren(t.Context(), r); !errors.Is(err, thread.ErrUnavailable) {
		t.Fatal("unavailable children", err)
	}
}

func TestDaemonThreadExpansionTeardown(t *testing.T) {
	for _, mode := range []string{"remove", "shutdown", "publisher"} {
		t.Run(mode, func(t *testing.T) {
			s, h, reg, _ := testShadow(t)
			reg.Create(conversations.Conversation{ID: testConvID})
			testShadowAppend(t, h, testConvID)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			store := thread.NewStore(h, func(ctx context.Context, _ conversations.ConversationID) error {
				once.Do(func() { close(entered) })
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			s.store = store
			cfg := relay.V2SessionConfig{}
			installDaemonThreadRelay(&cfg, s)
			if cfg.ThreadToolBody == nil {
				t.Fatal("provider missing")
			}
			stop := startDaemonThreadPublisher(t.Context(), s, &relay.V2SessionManager{})
			defer stop()
			s.publisher.active = func(context.Context, string) []relay.ActiveConn { return nil }
			done := make(chan error, 1)
			go func() {
				_, err := cfg.ThreadToolBody(t.Context(), protocol.ThreadExpansionRequest{ConversationID: testConvID, Epoch: "e", ItemID: 1, ExpectedRev: 1})
				done <- err
			}()
			<-entered
			stopped := make(chan struct{})
			go func() {
				switch mode {
				case "remove":
					reg.Delete(testConvID)
					s.remove(testConvID)
				case "shutdown":
					_ = s.shutdown()
				case "publisher":
					stop()
				}
				close(stopped)
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("retirement did not cancel expansion")
			}
			unblock()
			<-stopped
			s.quietMu.Lock()
			remaining := len(s.queryDemands)
			s.quietMu.Unlock()
			if remaining != 0 {
				t.Fatal("leaked demand")
			}
			if mode == "remove" {
				if _, err := cfg.ThreadChildren(t.Context(), protocol.ThreadExpansionRequest{ConversationID: testConvID, Limit: 1}); !errors.Is(err, thread.ErrUnavailable) {
					t.Fatal("removed conversation reopened", err)
				}
			}
		})
	}
}
