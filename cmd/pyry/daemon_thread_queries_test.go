package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestDaemonThreadQueriesReadiness(t *testing.T) {
	s, _, reg, store := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	b := testControlBindings(t, testConvID, "a")
	cfg := relay.V2SessionConfig{}
	installDaemonLiveRelay(&cfg, relayWiring{streamSink: b.sink}, nil)
	installDaemonThreadRelay(&cfg, s)
	if cfg.ThreadCatchUp == nil || cfg.ThreadPage == nil {
		t.Fatal("bounded query providers not installed")
	}
	stop := startDaemonThreadPublisher(t.Context(), s, &relay.V2SessionManager{})
	defer stop()
	if !cfg.ThreadReady() {
		t.Fatal("complete installation not ready")
	}
	for _, remove := range []func(){func() { cfg.ThreadCatchUp = nil }, func() { cfg.ThreadPage = nil }, func() { cfg.ThreadLiveState = nil }, func() { cfg.LiveStateCurrent = nil }, func() { cfg.ThreadLastShownVersion = nil }} {
		saved := cfg
		remove()
		if cfg.ThreadReady() {
			t.Fatal("incomplete installation granted thread")
		}
		cfg = saved
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := cfg.ThreadCatchUp(ctx, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("request context lost: %v", err)
	}
	if _, err := cfg.ThreadPage(ctx, protocol.ThreadPagePayload{ConversationID: testConvID, Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("page context lost: %v", err)
	}
	// The bounded store APIs remain nonloading; the daemon wrapper now owns demand.
	for _, query := range []func() (thread.QueryResult, error){func() (thread.QueryResult, error) {
		return store.CatchUp(t.Context(), conversations.ConversationID(testConvID), "", 0, 1)
	}, func() (thread.QueryResult, error) {
		return store.HistoryPage(t.Context(), conversations.ConversationID(testConvID), 0, 1)
	}} {
		got, err := query()
		if err != nil || got.State != thread.StateNotLoaded || got.Items != nil || got.Epoch != "" {
			t.Fatalf("nonusable result changed: %#v %v", got, err)
		}
	}
	if store.Snapshot(testConvID).State != thread.StateNotLoaded {
		t.Fatal("query loaded conversation")
	}
	installDaemonThreadRelay(&cfg, nil)
	if cfg.ThreadReady != nil || cfg.ThreadCatchUp != nil || cfg.ThreadPage != nil || cfg.ThreadLastShownVersion != nil {
		t.Fatal("stale installation retained")
	}
}

func testDaemonThreadQueryWire(t *testing.T, s *threadShadow, tweak func(*relay.V2SessionConfig)) (*switchWire, *relay.V2SessionManager) {
	t.Helper()
	b := testControlBindings(t, testConvID, "a")
	b.reg = s.registry
	return testDaemonRelayWire(t, b, relayWiring{}, nil, func(c *relay.V2SessionConfig) {
		installDaemonThreadRelay(c, s)
		send := c.Outbound
		c.OutboundContext = func(ctx context.Context, e protocol.RoutingEnvelope) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return send(e)
		}
		c.HistoryPage = newHistoryPager(s.history, discardLogger())
		if tweak != nil {
			tweak(c)
		}
	}, func(ctx context.Context, m *relay.V2SessionManager) func() {
		return startDaemonThreadPublisher(ctx, s, m)
	})
}

func testDaemonThreadRequest(t *testing.T, w *switchWire, peer string, id uint64, typ string, p any) {
	t.Helper()
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(protocol.Envelope{ID: id, Type: typ, TS: time.Now().UTC(), Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := w.phones[peer].send.Encrypt(raw)
	if err != nil {
		t.Fatal(err)
	}
	w.frames <- w.wrap(peer, protocol.TypeNoiseMsg, sealed)
}

func testDaemonThreadReply(t *testing.T, w *switchWire, id uint64, expectedPeer ...string) (protocol.ThreadItemsPayload, string) {
	t.Helper()
	recipient := "phone"
	if len(expectedPeer) > 0 {
		recipient = expectedPeer[0]
	}
	items := map[uint64]protocol.ThreadItem{}
	fragments := map[string]string{}
	var epoch string
	var version uint64
	started := time.Now()
	// Provider rendering and whole-result encoding precede the first frame.
	// One budget covers them, every fragment and the separate coverage marker.
	deadline := started.Add(60 * time.Second)
	frames := 0
	for {
		routing, err := testDaemonThreadFrame(w, deadline)
		if err != nil {
			t.Fatalf("query %d incomplete after %v: %d frames, %d rows, %d unfinished fragments: %v", id, time.Since(started), frames, len(items), len(fragments), err)
		}
		peer, e := w.decode(routing)
		frames++
		if frames == 1 {
			t.Logf("query %d first reconstructed frame: %v", id, time.Since(started))
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("query %d expired during decryption after %v (%d frames)", id, time.Since(started), frames)
		}
		raw, err := json.Marshal(e)
		if err != nil || len(raw) > protocol.MaxThreadEnvelopeBytes || e.TS.IsZero() || peer != recipient || e.InReplyTo == nil || *e.InReplyTo != id {
			t.Fatalf("invalid correlated envelope: %s %#v %v", peer, e, err)
		}
		if e.Type == protocol.TypeError {
			return protocol.ThreadItemsPayload{}, e.Type
		}
		if e.Type == protocol.TypeThreadReset {
			var reset protocol.ThreadResetPayload
			if err := json.Unmarshal(e.Payload, &reset); err != nil {
				t.Fatal(err)
			}
			return protocol.ThreadItemsPayload{ConversationID: reset.ConversationID, Epoch: reset.Epoch, Version: reset.Version}, e.Type
		}
		if e.ID == 0 {
			t.Fatal("query reply lacks relay stamp")
		}
		if e.Type != protocol.TypeThreadItems {
			t.Fatalf("unexpected query output: %s", e.Type)
		}
		var part protocol.ThreadUpdatePart
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(e.Payload, &fields); err != nil {
			t.Fatal(err)
		}
		if c := bytes.TrimSpace(fields["continuation"]); len(c) > 0 && c[0] == '{' {
			if err := json.Unmarshal(e.Payload, &part); err != nil {
				t.Fatal(err)
			}
		}
		if part.Continuation != nil {
			c := part.Continuation
			if uint64(len(fragments[c.UpdateID])) != c.Offset {
				t.Fatal("fragment offset")
			}
			fragments[c.UpdateID] += part.Data
			if !c.Final {
				continue
			}
			if uint64(len(fragments[c.UpdateID])) != c.TotalBytes {
				t.Fatal("fragment length")
			}
			e.Payload = json.RawMessage(fragments[c.UpdateID])
			delete(fragments, c.UpdateID)
		}
		var p protocol.ThreadItemsPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if epoch == "" {
			epoch, version = p.Epoch, p.Version
		}
		if p.Epoch != epoch || p.Version != version || p.ConversationID != testConvID {
			t.Fatal("mixed captured query publications")
		}
		if p.Final {
			if len(p.Items) != 0 || len(fragments) != 0 {
				t.Fatal("completion was not a separate empty marker")
			}
			for _, i := range items {
				p.Items = append(p.Items, i)
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("query %d expired during reconstruction after %v (%d frames)", id, time.Since(started), frames)
			}
			t.Logf("query %d complete reconstruction: %v (%d frames)", id, time.Since(started), frames)
			return p, e.Type
		}
		if p.FromVersion != nil || p.LowerOrder != nil || p.UpperOrder != nil || p.Continuation != nil || p.OlderExists != nil {
			t.Fatal("fragment advanced coverage")
		}
		for _, i := range p.Items {
			if _, ok := items[i.ID]; ok {
				t.Fatal("duplicate row")
			}
			items[i.ID] = i
		}
	}
}

func testDaemonThreadRows(t *testing.T, got []protocol.ThreadItem, want []thread.Item) {
	t.Helper()
	actual := map[uint64]protocol.ThreadItem{}
	expected := map[uint64]protocol.ThreadItem{}
	for _, i := range got {
		actual[i.ID] = i
	}
	for _, i := range want {
		expected[i.ID] = daemonThreadItem(i)
	}
	// Compare JSON values so map key ordering inside inert content is irrelevant.
	a, _ := json.Marshal(actual)
	b, _ := json.Marshal(expected)
	var av, bv any
	json.Unmarshal(a, &av)
	json.Unmarshal(b, &bv)
	if len(actual) != len(got) || !reflect.DeepEqual(av, bv) {
		t.Fatalf("full rows differ: got %s want %s", a, b)
	}
	for _, i := range got {
		if i.Parent != 0 {
			if _, ok := actual[i.Parent]; !ok {
				t.Fatal("incomplete parent closure")
			}
		}
	}
}

func testDaemonThreadAppend(t *testing.T, h *history.Store, typ, payload string) uint64 {
	t.Helper()
	id, err := h.Append(testConvID, typ, json.RawMessage(payload), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func testDaemonThreadLoad(t *testing.T, store *thread.Store, version uint64) thread.QueryResult {
	t.Helper()
	if err := store.Load(context.Background(), testConvID); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	deadline := started.Add(120 * time.Second)
	for time.Now().Before(deadline) {
		q, err := store.CatchUp(t.Context(), testConvID, "", 0, 1)
		if err != nil {
			t.Fatal(err)
		}
		if q.State == thread.StateUsable && q.Version == version {
			t.Logf("recovery/publication preparation: %v", time.Since(started))
			return q
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("store recovery did not become usable")
	return thread.QueryResult{}
}

func TestDaemonThreadQueriesRecovery(t *testing.T) {
	testDaemonThreadQueriesRecovery(t, nil)
}

func testDaemonThreadQueriesRecovery(t *testing.T, tweak func(*relay.V2SessionConfig)) {
	t.Helper()
	s, h, reg, store := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	for _, e := range []struct{ typ, payload string }{
		{"tool_use", `{"turn_id":"t","tool_use_id":"parent","name":"Agent"}`},
		{"tool_use", `{"turn_id":"t","parent_tool_use_id":"missing","tool_use_id":"nested","name":"Task"}`},
		{"assistant_delta", `{"turn_id":"t","parent_tool_use_id":"nested","text":` + string(mustQueryJSON(t, strings.Repeat("<\\\"\n", 24000))) + `}`},
		{"turn_end", `{"turn_id":"t","parent_tool_use_id":"nested","stop_reason":"end_turn"}`},
		{"unknown", `{}`},
	} {
		testDaemonThreadAppend(t, h, e.typ, e.payload)
	}
	saved := testDaemonThreadLoad(t, store, 5)
	for _, e := range []struct{ typ, payload string }{
		{"tool_result", `{"turn_id":"t","parent_tool_use_id":"parent","tool_use_id":"nested","is_error":false}`},
		{"send_accepted", `{"text":"queued","device_id":"phone","message_id":"queued","accepted_at":"2026-01-01T00:00:00Z"}`},
		{"assistant_delta", `{"turn_id":"t","text":"first"}`},
		{"assistant_delta", `{"turn_id":"t","text":" second"}`},
		{"message", `{"role":"user","text":"newest"}`},
	} {
		testDaemonThreadAppend(t, h, e.typ, e.payload)
	}
	testDaemonThreadLoad(t, store, 10)
	if err := store.Shutdown(); err != nil {
		t.Fatal(err)
	}
	reopened := thread.NewStore(h)
	s.store = reopened
	q := testDaemonThreadLoad(t, reopened, 10)
	if q.Epoch != saved.Epoch {
		t.Fatal("clean restart changed epoch")
	}
	started := time.Now()
	w, mgr := testDaemonThreadQueryWire(t, s, func(c *relay.V2SessionConfig) {
		testDaemonThreadQueryTrace(t, c, started)
		if tweak != nil {
			tweak(c)
		}
	})
	w.open("phone", false, protocol.CapabilityThread)
	t.Logf("phone handshake completed: %v", time.Since(started))
	w.open("legacy", false)
	admissionCtx, cancelAdmission := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancelAdmission()
	if len(mgr.ActiveConns(admissionCtx)) != 2 {
		t.Fatal("query peers not admitted")
	}
	t.Logf("both peers admitted: %v", time.Since(started))
	request := protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: saved.Epoch, Version: 5, Limit: 1}
	testDaemonThreadRequest(t, w, "phone", 40, protocol.TypeThreadCatchUp, request)
	got, typ := testDaemonThreadReply(t, w, 40)
	want, err := reopened.CatchUp(t.Context(), testConvID, saved.Epoch, 5, 1)
	if err != nil || typ != protocol.TypeThreadItems || got.FromVersion == nil || *got.FromVersion != 5 || got.Version != 10 || got.Epoch != saved.Epoch {
		t.Fatalf("saved checkpoint not delivered: %#v %s %v", got, typ, err)
	}
	testDaemonThreadRows(t, got.Items, want.Items)
	late := false
	for _, i := range got.Items {
		if i.ID == 3 {
			late = i.Rev < 5
		}
	}
	if !late || len(got.Items) <= 1 {
		t.Fatal("old-revision first appearance or untruncated catch-up missing")
	}
	for i, before := range []uint64{4, 1, 0, 100} {
		testDaemonThreadRequest(t, w, "phone", uint64(41+i), protocol.TypeThreadPage, protocol.ThreadPagePayload{ConversationID: testConvID, BeforeOrder: before, Limit: 1})
		p, typ := testDaemonThreadReply(t, w, uint64(41+i))
		want, err := reopened.HistoryPage(t.Context(), testConvID, before, 1)
		if err != nil || typ != protocol.TypeThreadItems || p.LowerOrder == nil || *p.LowerOrder != want.LowerOrder || p.UpperOrder == nil || *p.UpperOrder != before || p.Continuation == nil || *p.Continuation != want.LowerOrder || p.OlderExists == nil || *p.OlderExists != want.OlderExists {
			t.Fatalf("page bounds lost: %#v", p)
		}
		testDaemonThreadRows(t, p.Items, want.Items)
	}
	testDaemonThreadRequest(t, w, "phone", 50, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 1})
	newest, typ := testDaemonThreadReply(t, w, 50)
	want, _ = reopened.CatchUp(t.Context(), testConvID, "", 0, 1)
	if typ != protocol.TypeThreadItems || newest.FromVersion != nil || newest.UpperOrder == nil || *newest.UpperOrder != 11 {
		t.Fatal("newest-window coverage")
	}
	testDaemonThreadRows(t, newest.Items, want.Items)
	testDaemonThreadRequest(t, w, "legacy", 51, protocol.TypeRequestHistory, protocol.RequestHistoryPayload{ConversationID: testConvID, Limit: 1})
	peer, e := w.read()
	if peer != "legacy" || e.Type != protocol.TypeHistoryPage || e.InReplyTo == nil || *e.InReplyTo != 51 {
		t.Fatal("legacy raw history lost")
	}
}

func mustQueryJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDaemonThreadQueriesReset(t *testing.T) {
	for _, scenario := range []string{"epoch", "ahead", "gap1025", "gap1024", "missing ID", "suppression", "not loaded"} {
		t.Run(scenario, func(t *testing.T) {
			s, h, reg, store := testShadow(t)
			reg.Create(conversations.Conversation{ID: testConvID})
			after := uint64(1)
			switch scenario {
			case "not loaded":
			case "suppression":
				testDaemonThreadAppend(t, h, "send_accepted", `{"text":"accepted","device_id":"phone","message_id":"m"}`)
				testDaemonThreadAppend(t, h, "message", `{"role":"user","text":"delivery"}`)
				testDaemonThreadAppend(t, h, "send_delivered", `{"accepted_entry_id":1,"delivery_entry_id":2,"reason":"delivered"}`)
				after = 2
			case "missing ID":
				for range 4 {
					testDaemonThreadAppend(t, h, "message", `{"role":"user","text":"row"}`)
				}
				dir, err := h.EnsureLogDir(testConvID)
				if err != nil {
					t.Fatal(err)
				}
				paths, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
				if err != nil || len(paths) != 1 {
					t.Fatalf("segments: %v %v", paths, err)
				}
				raw, err := os.ReadFile(paths[0])
				if err != nil {
					t.Fatal(err)
				}
				var kept []byte
				removed := false
				for _, line := range bytes.Split(raw, []byte{'\n'}) {
					if len(line) == 0 {
						continue
					}
					var entry history.Entry
					if json.Unmarshal(line, &entry) == nil && entry.ID == 2 {
						removed = true
						continue
					}
					kept = append(kept, line...)
					kept = append(kept, '\n')
				}
				if !removed {
					t.Fatal("missing-ID fixture removed nothing")
				}
				if err := os.WriteFile(paths[0], kept, 0600); err != nil {
					t.Fatal(err)
				}
			default:
				for range thread.MaxCatchUpGap + 2 {
					testDaemonThreadAppend(t, h, "unknown", `{}`)
				}
				if scenario == "gap1024" {
					after = 2
				}
			}
			var q thread.QueryResult
			if scenario != "not loaded" {
				v, err := h.LatestEntryID(testConvID)
				if err != nil {
					t.Fatal(err)
				}
				q = testDaemonThreadLoad(t, store, v)
			}
			request := protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: q.Epoch, Version: after, Limit: 1}
			if scenario == "epoch" {
				request.Epoch = "different"
			}
			if scenario == "ahead" {
				request.Version = q.Version + 1
			}
			w, _ := testDaemonThreadQueryWire(t, s, nil)
			w.open("phone", false, protocol.CapabilityThread)
			testDaemonThreadRequest(t, w, "phone", 70, protocol.TypeThreadCatchUp, request)
			result, typ := testDaemonThreadReply(t, w, 70)
			want := protocol.TypeThreadReset
			if scenario == "gap1024" {
				want = protocol.TypeThreadItems
			}
			if scenario == "not loaded" && (result.Epoch == "" || result.Version != 0) {
				t.Fatal("cold empty recovery lost its certified zero checkpoint")
			}
			if scenario != "not loaded" && typ == protocol.TypeThreadReset && (result.Epoch != q.Epoch || result.Version != q.Version) {
				t.Fatal("reset publication metadata lost")
			}
			if typ != want {
				t.Fatalf("%s reached %s, want %s", scenario, typ, want)
			}
		})
	}
}

func TestDaemonThreadQueriesBounded(t *testing.T) {
	s, h, reg, store := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	started := time.Now()
	for i := 1; i <= 36000; i++ {
		typ, payload := "unknown", `{}`
		if i%6 == 0 {
			typ, payload = "message", `{"role":"user","text":"row"}`
		}
		switch i {
		case 1:
			typ, payload = "send_accepted", `{"text":"old queued","device_id":"phone","message_id":"old"}`
		case 35001:
			typ, payload = "tool_use", `{"turn_id":"t","tool_use_id":"parent","name":"Agent"}`
		case 35002:
			typ, payload = "tool_use", `{"turn_id":"t","parent_tool_use_id":"parent","tool_use_id":"nested","name":"Task"}`
		case 35003:
			typ, payload = "assistant_delta", `{"turn_id":"t","parent_tool_use_id":"nested","text":"old active child"}`
		}
		testDaemonThreadAppend(t, h, typ, payload)
	}
	t.Logf("committed history preparation: %v", time.Since(started))
	q := testDaemonThreadLoad(t, store, 36000)
	var cfg relay.V2SessionConfig
	installDaemonThreadRelay(&cfg, s)
	// Provider selection stays bounded independently of wire encoding. Rendering
	// selected references may decode a whole segment; allocation counts include I/O.
	for _, run := range []func() (thread.QueryResult, error){
		func() (thread.QueryResult, error) {
			return cfg.ThreadCatchUp(t.Context(), protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: q.Epoch, Version: 35990, Limit: 1})
		},
		func() (thread.QueryResult, error) {
			return cfg.ThreadCatchUp(t.Context(), protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 1})
		},
		func() (thread.QueryResult, error) {
			return cfg.ThreadPage(t.Context(), protocol.ThreadPagePayload{ConversationID: testConvID, BeforeOrder: 18001, Limit: 1})
		},
	} {
		got, err := run()
		if err != nil || got.State != thread.StateUsable {
			t.Fatal(got, err)
		}
		if len(got.Items) > 8 {
			t.Fatalf("unbounded installed query: %d rows", len(got.Items))
		}
	}
	w, _ := testDaemonThreadQueryWire(t, s, nil)
	w.open("phone", false, protocol.CapabilityThread)
	for i, after := range []uint64{35900, 0} {
		id := uint64(80 + i)
		testDaemonThreadRequest(t, w, "phone", id, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: q.Epoch, Version: after, Limit: 2})
		p, typ := testDaemonThreadReply(t, w, id)
		want, err := store.CatchUp(t.Context(), testConvID, q.Epoch, after, 2)
		if err != nil || typ != protocol.TypeThreadItems || p.Version != 36000 {
			t.Fatal("long catch-up failed")
		}
		testDaemonThreadRows(t, p.Items, want.Items)
	}
	for i, before := range []uint64{18001, 36001} {
		id := uint64(82 + i)
		testDaemonThreadRequest(t, w, "phone", id, protocol.TypeThreadPage, protocol.ThreadPagePayload{ConversationID: testConvID, BeforeOrder: before, Limit: 2})
		p, typ := testDaemonThreadReply(t, w, id)
		want, err := store.HistoryPage(t.Context(), testConvID, before, 2)
		if err != nil || typ != protocol.TypeThreadItems || p.LowerOrder == nil || *p.LowerOrder != want.LowerOrder || p.Continuation == nil || *p.Continuation != want.LowerOrder || p.OlderExists == nil || !*p.OlderExists {
			t.Fatal("long page failed")
		}
		testDaemonThreadRows(t, p.Items, want.Items)
	}
	// The production installation must call only the existing bounded APIs.
	source := formattedGoFunc(t, "daemon_thread_relay.go", "installDaemonThreadRelay")
	for _, call := range []string{"return store.CatchUp(ctx, conversations.ConversationID(r.ConversationID), r.Epoch, r.Version, r.Limit)", "return store.HistoryPage(ctx, conversations.ConversationID(r.ConversationID), r.BeforeOrder, r.Limit)"} {
		if !strings.Contains(source, call) {
			t.Fatalf("bounded API bypassed: %s", call)
		}
	}
	source = source[strings.Index(source, "cfg.ThreadCatchUp = func"):strings.Index(source, "cfg.ThreadReady = func")]
	for _, call := range []string{"store.Load(", "store.Snapshot(", "store.Observe(", "s.history.", "store.NewestWindow("} {
		if strings.Contains(source, call) {
			t.Fatalf("request installation contains unbounded work: %s", call)
		}
	}
}

func TestDaemonThreadQueriesJoin(t *testing.T) {
	s, h, reg, store := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	testDaemonThreadAppend(t, h, "assistant_delta", `{"turn_id":"t","text":"first"}`)
	saved := testDaemonThreadLoad(t, store, 1)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	calls := 0
	w, mgr := testDaemonThreadQueryWire(t, s, func(c *relay.V2SessionConfig) {
		provider := c.ThreadCatchUp
		c.ThreadCatchUp = func(ctx context.Context, r protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
			result, err := provider(ctx, r)
			calls++
			if calls == 2 {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			return result, err
		}
	})
	// Establish the real publisher cursor before admitting this reconnecting app.
	cursor := &shadowConversation{}
	s.publisher.publish(t.Context(), testConvID, cursor)
	w.open("phone", false, protocol.CapabilityThread)
	w.open("legacy", false)
	request := protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 1}
	testDaemonThreadRequest(t, w, "phone", 100, protocol.TypeThreadCatchUp, request)
	initial, typ := testDaemonThreadReply(t, w, 100)
	if typ != protocol.TypeThreadItems {
		t.Fatal("initialization failed")
	}
	rows := map[uint64]protocol.ThreadItem{}
	for _, i := range initial.Items {
		rows[i.ID] = i
	}
	checkpoint := initial.Version
	request.Epoch, request.Version = saved.Epoch, checkpoint
	testDaemonThreadRequest(t, w, "phone", 101, protocol.TypeThreadCatchUp, request)
	awaitSwitchPublication(t, entered)
	testDaemonThreadAppend(t, h, "assistant_delta", `{"turn_id":"t","text":" second"}`)
	testDaemonThreadLoad(t, store, 2)
	s.publisher.publish(t.Context(), testConvID, cursor)
	unblock()
	failed, liveCount := false, 0
	for !failed || liveCount < 2 {
		peer, e := w.read()
		if peer != "phone" {
			t.Fatal("thread query/live output leaked to legacy")
		}
		if e.Type == protocol.TypeError {
			if e.InReplyTo == nil || *e.InReplyTo != 101 {
				t.Fatal("uncorrelated race failure")
			}
			failed = true
			continue
		}
		if e.Type == protocol.TypeThreadItemChanged || e.Type == protocol.TypeThreadTextAppend {
			liveCount++
		}
		if e.InReplyTo != nil {
			var p protocol.ThreadItemsPayload
			json.Unmarshal(e.Payload, &p)
			if p.Final {
				t.Fatal("racing query falsely completed")
			}
		}
	}
	if checkpoint != 1 {
		t.Fatal("failure advanced completed checkpoint")
	}
	testDaemonThreadRequest(t, w, "phone", 102, protocol.TypeThreadCatchUp, request)
	retry, typ := testDaemonThreadReply(t, w, 102)
	if typ != protocol.TypeThreadItems || retry.Version != 2 {
		t.Fatal("retry did not complete")
	}
	for _, i := range retry.Items {
		rows[i.ID] = i
	}
	checkpoint = retry.Version
	testDaemonThreadAppend(t, h, "assistant_delta", `{"turn_id":"t","text":" third"}`)
	testDaemonThreadLoad(t, store, 3)
	s.publisher.publish(t.Context(), testConvID, cursor)
	for range 2 {
		peer, e := w.read()
		if peer != "phone" || e.InReplyTo != nil {
			t.Fatal("live recipient/correlation")
		}
		var changed protocol.ThreadItemChangedPayload
		var appended protocol.ThreadTextAppendPayload
		switch e.Type {
		case protocol.TypeThreadItemChanged:
			if err := json.Unmarshal(e.Payload, &changed); err != nil {
				t.Fatal(err)
			}
			prior := rows[changed.ItemID]
			if prior.Rev != changed.BaseRev {
				t.Fatal("query/live change regression")
			}
			var fields map[string]json.RawMessage
			json.Unmarshal(mustQueryJSON(t, prior), &fields)
			for k, v := range changed.Changes {
				fields[k] = v
			}
			fields["rev"] = mustQueryJSON(t, changed.Rev)
			if err := json.Unmarshal(mustQueryJSON(t, fields), &prior); err != nil {
				t.Fatal(err)
			}
			rows[prior.ID] = prior
		case protocol.TypeThreadTextAppend:
			if err := json.Unmarshal(e.Payload, &appended); err != nil {
				t.Fatal(err)
			}
			prior := rows[appended.ItemID]
			if prior.Rev != appended.BaseRev {
				t.Fatal("query/live text regression")
			}
			var fields map[string]json.RawMessage
			json.Unmarshal(prior.Content, &fields)
			var text string
			json.Unmarshal(fields["text"], &text)
			fields["text"] = mustQueryJSON(t, text+appended.Text)
			prior.Content = mustQueryJSON(t, fields)
			prior.Rev = appended.Rev
			rows[prior.ID] = prior
		default:
			t.Fatalf("unexpected live update: %s", e.Type)
		}
	}
	var actual []protocol.ThreadItem
	for _, i := range rows {
		actual = append(actual, i)
	}
	testDaemonThreadRows(t, actual, store.Snapshot(testConvID).Items)
	if checkpoint != 2 {
		t.Fatal("live fragments advanced query checkpoint")
	}
	legacy := protocol.Envelope{Type: protocol.TypeAssistantDelta, TS: time.Now().UTC(), Payload: mustQueryJSON(t, protocol.AssistantDeltaPayload{ConversationID: testConvID, TurnID: "t", Text: " third"})}
	for _, peer := range []string{"phone", "legacy"} {
		if err := mgr.Push(t.Context(), peer, legacy); err != nil {
			t.Fatal(err)
		}
	}
	if err := mgr.FlushPushes(t.Context()); err != nil {
		t.Fatal(err)
	}
	peer, e := w.read()
	if peer != "legacy" || e.Type != protocol.TypeAssistantDelta {
		t.Fatal("simultaneous legacy stream changed")
	}
}
