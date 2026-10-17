package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestDaemonThreadCompactInstallation(t *testing.T) {
	s, _, reg, _ := testShadow(t)
	reg.Create(conversations.Conversation{ID: testConvID})
	b := testControlBindings(t, testConvID, "a")
	cfg := relay.V2SessionConfig{}
	installDaemonLiveRelay(&cfg, relayWiring{streamSink: b.sink}, nil)
	installDaemonThreadRelay(&cfg, s)
	if cfg.ThreadCompactCatchUp == nil || cfg.ThreadCompactPage == nil || cfg.ThreadCompactReady == nil {
		t.Fatal("compact providers not installed")
	}
	if cfg.ThreadCompactReady() {
		t.Fatal("compact ready without a live publisher")
	}
	stop := startDaemonThreadPublisher(t.Context(), s, &relay.V2SessionManager{})
	s.publisher.active = func(context.Context, string) []relay.ActiveConn { return nil }
	if !cfg.ThreadCompactReady() {
		t.Fatal("complete installation not compact ready")
	}
	for _, remove := range []func(){func() { cfg.ThreadToolBody = nil }, func() { cfg.ThreadChildren = nil }, func() { cfg.ThreadCompactPage = nil }, func() { cfg.ThreadCompactCatchUp = nil }, func() { cfg.ThreadPage = nil }} {
		saved := cfg
		remove()
		if cfg.ThreadCompactReady() {
			t.Fatal("incomplete installation granted compact")
		}
		cfg = saved
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := cfg.ThreadCompactCatchUp(ctx, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: "e", Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("compact catch-up context lost: %v", err)
	}
	if _, err := cfg.ThreadCompactPage(ctx, protocol.ThreadPagePayload{ConversationID: testConvID, Epoch: "e", Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("compact page context lost: %v", err)
	}
	stop()
	if cfg.ThreadCompactReady() {
		t.Fatal("retired publisher left compact ready")
	}
	installDaemonThreadRelay(&cfg, nil)
	if cfg.ThreadCompactCatchUp != nil || cfg.ThreadCompactPage != nil || cfg.ThreadCompactReady != nil {
		t.Fatal("cleared installation retained compact callbacks")
	}
}

func TestDaemonThreadCompactNegotiation(t *testing.T) {
	for _, state := range []string{"ready", "missing-expansion", "stopped"} {
		t.Run(state, func(t *testing.T) {
			s, _, reg, _ := testShadow(t)
			reg.Create(conversations.Conversation{ID: testConvID})
			b := testControlBindings(t, testConvID, "a")
			b.reg = reg
			var stop func()
			w, mgr := testDaemonRelayWire(t, b, relayWiring{}, nil, func(c *relay.V2SessionConfig) {
				installDaemonThreadRelay(c, s)
				if state == "missing-expansion" {
					c.ThreadChildren = nil
				}
			}, func(ctx context.Context, m *relay.V2SessionManager) func() {
				stop = startDaemonThreadPublisher(ctx, s, m)
				return stop
			})
			if state == "stopped" {
				stop()
			}
			w.open("phone", false, protocol.CapabilityThread, protocol.CapabilityThreadExpansion, protocol.CapabilityThreadCompact)
			peers := mgr.ActiveThreadConns(t.Context(), testConvID)
			granted := len(peers) == 1 && peers[0].ThreadCompact
			if granted != (state == "ready") {
				t.Fatalf("authenticated compact grant in %s: %+v", state, peers)
			}
			if !granted {
				testDaemonThreadRequest(t, w, "phone", 1, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Projection: protocol.ThreadProjectionCompact, Limit: 1})
				typ, _ := testCompactReply(t, w, mgr, "phone", 1)
				if typ != protocol.TypeError {
					t.Fatalf("unnegotiated compact query returned %s", typ)
				}
			}
		})
	}
}

// testCompactFrame is one decrypted, reassembled thread frame and its stamped size.
type testCompactFrame struct {
	peer   string
	env    protocol.Envelope
	bytes  int
	frames int // physical envelopes, including every continuation part
}

// testCompactRead reassembles frames for any peer until each listed peer has
// delivered the sentinel error pushed after the publication lane drained.
func testCompactRead(t *testing.T, w *switchWire, mgr *relay.V2SessionManager, peers ...string) []testCompactFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	done := make(chan error, 1)
	go func() {
		if err := mgr.FlushPushes(ctx); err != nil {
			done <- err
			return
		}
		for _, peer := range peers {
			if err := mgr.Push(ctx, peer, protocol.Envelope{Type: protocol.TypeError, Payload: json.RawMessage(`{}`)}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("drain barrier: %v", err)
		}
	}()
	var out []testCompactFrame
	fragments := map[string]string{}
	sizes := map[string]int{}
	counts := map[string]int{}
	waiting := len(peers)
	for waiting > 0 || len(fragments) > 0 {
		routing, err := testDaemonThreadFrame(w, time.Now().Add(30*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		peer, e := w.decode(routing)
		frames := 1
		raw, _ := json.Marshal(e)
		if len(raw) > protocol.MaxThreadEnvelopeBytes {
			t.Fatal("frame over cap")
		}
		if e.Type == protocol.TypeError && e.InReplyTo == nil {
			waiting--
			continue
		}
		var part protocol.ThreadUpdatePart
		var fields map[string]json.RawMessage
		if json.Unmarshal(e.Payload, &fields) == nil {
			if c := bytes.TrimSpace(fields["continuation"]); len(c) > 0 && c[0] == '{' && json.Unmarshal(e.Payload, &part) == nil && part.Continuation != nil {
				key := peer + "/" + part.Continuation.UpdateID
				fragments[key] += part.Data
				sizes[key] += len(raw)
				counts[key]++
				if !part.Continuation.Final {
					continue
				}
				e.Payload, raw = json.RawMessage(fragments[key]), make([]byte, sizes[key])
				frames = counts[key]
				delete(fragments, key)
				delete(sizes, key)
				delete(counts, key)
			}
		}
		out = append(out, testCompactFrame{peer, e, len(raw), frames})
	}
	return out
}

func testCompactQuery(t *testing.T, w *switchWire, mgr *relay.V2SessionManager, peer string, id uint64, typ string, p any) (protocol.ThreadItemsPayload, int, int) {
	t.Helper()
	testDaemonThreadRequest(t, w, peer, id, typ, p)
	var got protocol.ThreadItemsPayload
	size, frames := 0, 0
	for {
		for _, f := range testCompactRead(t, w, mgr, peer) {
			if f.env.InReplyTo == nil || *f.env.InReplyTo != id {
				continue
			}
			if f.env.Type != protocol.TypeThreadItems {
				t.Fatalf("query %d refused: %s %s", id, f.env.Type, f.env.Payload)
			}
			size += f.bytes
			frames += f.frames
			var r protocol.ThreadItemsPayload
			if err := json.Unmarshal(f.env.Payload, &r); err != nil {
				t.Fatalf("invalid reconstructed query payload: %v", err)
			}
			got.Items, got.Headers = append(got.Items, r.Items...), append(got.Headers, r.Headers...)
			if r.Final {
				r.Items, r.Headers = got.Items, got.Headers
				return r, size, frames
			}
		}
	}
}

// Compact recipients get bounded root headers for initial load, pages and a
// barrier-controlled live burst; complete recipients keep per-change items.
func TestDaemonThreadCompactWire(t *testing.T) {
	var measured [][4]int
	for _, tc := range []struct {
		name                       string
		children, descendants, big int
		burst                      int
	}{{"small", 5, 3, 64, 3}, {"large", 8333, 40, 4096, 25}} {
		t.Run(tc.name, func(t *testing.T) {
			s, store, _ := testDaemonExpansionFixture(t, tc.children, tc.descendants, tc.big)
			testDaemonThreadAppend(t, s.history, "tool_result", fmt.Sprintf(`{"turn_id":"t","tool_use_id":"d0","output":%q,"is_error":true}`, strings.Repeat("unopened-error", tc.big/14+1)[:tc.big]))
			v := testDaemonThreadAppend(t, s.history, "message", `{"role":"user","text":"public-top-level"}`)
			testShadowWait(t, func() bool { return store.Status(testConvID).Version == v })
			items := store.Snapshot(testConvID).Items
			w, mgr, notify := testCompactBurstWire(t, s)
			w.open("phone", false, protocol.CapabilityThread, protocol.CapabilityThreadExpansion, protocol.CapabilityThreadCompact)
			w.open("legacy", false, protocol.CapabilityThread)
			roots := map[uint64]bool{}
			for _, i := range items {
				if i.Parent == 0 && i.Order != 0 {
					roots[i.ID] = true
				}
			}
			hidden := func(raw []byte) bool {
				return bytes.Contains(raw, []byte("unopened-")) || bytes.Contains(raw, []byte(`<\\\"`))
			}
			checkRows := func(q protocol.ThreadItemsPayload) {
				t.Helper()
				if hidden(mustQueryJSON(t, q)) {
					t.Fatal("compact query carried hidden payload")
				}
				for _, h := range q.Headers {
					if !roots[h.ID] || h.Parent != 0 || !h.DetailOmitted {
						t.Fatal("compact query carried hidden identity or detail")
					}
				}
				for _, i := range q.Items {
					if !roots[i.ID] || i.Parent != 0 || i.Kind == "agent" || i.Kind == "tool_call" {
						t.Fatal("compact query carried hidden item")
					}
				}
			}
			compact, size, frames := testCompactQuery(t, w, mgr, "phone", 1, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: "e", Limit: 256, Projection: protocol.ThreadProjectionCompact})
			if compact.Projection != protocol.ThreadProjectionCompact || len(compact.Items) != 1 || len(compact.Headers)+len(compact.Items) != len(roots) {
				t.Fatalf("compact window rows: %d items %d headers, %d roots", len(compact.Items), len(compact.Headers), len(roots))
			}
			for _, h := range compact.Headers {
				if !roots[h.ID] || !h.DetailOmitted {
					t.Fatal("compact window carried a hidden identity or detail")
				}
			}
			if hidden(mustQueryJSON(t, compact)) {
				t.Fatal("compact window carried hidden payload")
			}
			checkRows(compact)
			windowWant, err := store.CompactNewestWindow(t.Context(), testConvID, 256)
			if err != nil {
				t.Fatal(err)
			}
			testCompactHeaders(t, compact.Headers, windowWant.Headers)
			testDaemonThreadRows(t, compact.Items, windowWant.Items)
			page, pageSize, pageFrames := testCompactQuery(t, w, mgr, "phone", 2, protocol.TypeThreadPage, protocol.ThreadPagePayload{ConversationID: testConvID, Epoch: compact.Epoch, BeforeOrder: *compact.UpperOrder, Limit: 2, Projection: protocol.ThreadProjectionCompact})
			if len(page.Headers) == 0 || hidden(mustQueryJSON(t, page)) {
				t.Fatal("compact page lost roots or carried hidden payload")
			}
			checkRows(page)
			pageWant, err := store.CompactHistoryPage(t.Context(), testConvID, *compact.UpperOrder, 2)
			if err != nil {
				t.Fatal(err)
			}
			testCompactHeaders(t, page.Headers, pageWant.Headers)
			testDaemonThreadRows(t, page.Items, pageWant.Items)
			full, fullSize, fullFrames := testCompactQuery(t, w, mgr, "legacy", 3, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 256})
			if len(full.Headers) != 0 || full.Projection != "" {
				t.Fatal("complete window carried compact rows")
			}
			if fullFrames <= 3 {
				t.Fatal("fragmented complete query undercounted physical envelopes")
			}
			t.Logf("newest window compact bytes=%d frames=%d complete bytes=%d frames=%d; compact page bytes=%d frames=%d", size, frames, fullSize, fullFrames, pageSize, pageFrames)
			if size >= fullSize {
				t.Fatal("compact window not smaller than complete")
			}

			// Barrier: hold the publication gate across the burst so one pass
			// observes every hidden child event.
			cw := s.workers[testConvID]
			if !cw.lockPublication(t.Context()) {
				t.Fatal("publication gate")
			}
			held := true
			defer func() {
				if held {
					cw.unlockPublication()
				}
			}()
			burst := tc.burst
			for i := range burst {
				testDaemonThreadAppend(t, s.history, "tool_use", fmt.Sprintf(`{"turn_id":"t","parent_tool_use_id":"root","tool_use_id":"burst%d","name":"Read","input":{"secret":"unopened-burst"}}`, i))
				testDaemonThreadAppend(t, s.history, "tool_result", fmt.Sprintf(`{"turn_id":"t","tool_use_id":"burst%d","output":%q,"is_error":true}`, i, strings.Repeat("unopened-burst-error", tc.big/20+1)[:tc.big]))
				if i == burst/2 {
					notify() // all four channels must arrive before the burst resumes
				}
			}
			latest, _ := s.history.LatestEntryID(testConvID)
			testShadowWait(t, func() bool { return store.Status(testConvID).Version >= latest })
			cw.unlockPublication()
			held = false
			testShadowWait(t, func() bool {
				if !cw.lockPublication(t.Context()) {
					return false
				}
				defer cw.unlockPublication()
				return cw.threadVersion >= latest
			})
			live := testCompactRead(t, w, mgr, "phone", "legacy")
			want, err := store.CompactNewestWindow(t.Context(), testConvID, 256)
			if err != nil {
				t.Fatal(err)
			}
			var root uint64
			for _, i := range items {
				var f map[string]json.RawMessage
				if json.Unmarshal(i.Content, &f) == nil && string(f["tool_use_id"]) == `"root"` {
					root = i.ID
				}
			}
			compactLive, compactBytes, completeLive, completeBytes := 0, 0, 0, 0
			seen := map[uint64]int{}
			for _, f := range live {
				switch f.env.Type {
				case protocol.TypeThreadCompactUpdate, protocol.TypeThreadItemAdded, protocol.TypeThreadItemChanged, protocol.TypeThreadTextAppend:
				default:
					continue // live session state travels its established channel
				}
				raw := mustQueryJSON(t, f.env)
				switch {
				case f.peer == "phone" && f.env.Type == protocol.TypeThreadCompactUpdate:
					var u protocol.ThreadCompactUpdatePayload
					if err := json.Unmarshal(f.env.Payload, &u); err != nil {
						t.Fatal(err)
					}
					if hidden(raw) || u.Item != nil && (u.Item.Parent != 0 || !roots[u.Item.ID]) || u.Header != nil && (u.Header.Parent != 0 || !roots[u.Header.ID]) {
						t.Fatal("compact live carried hidden payload or identity")
					}
					if u.Header != nil {
						seen[u.Header.ID]++
						for _, h := range want.Headers {
							if h.ID == u.Header.ID {
								testCompactHeaders(t, []protocol.ThreadSummary{*u.Header}, []thread.ChildSummary{h})
							}
						}
					}
					compactLive += f.frames
					compactBytes += f.bytes
				case f.peer == "phone":
					t.Fatalf("compact recipient got %s", f.env.Type)
				case f.peer == "legacy" && f.env.Type == protocol.TypeThreadCompactUpdate:
					t.Fatal("complete recipient got compact output")
				default:
					completeLive += f.frames
					completeBytes += f.bytes
				}
			}
			if seen[root] != 1 || compactLive > 2 {
				t.Fatalf("burst not coalesced: root headers=%d compact frames=%d", seen[root], compactLive)
			}
			if completeLive < burst {
				t.Fatalf("complete recipient lost per-change items: %d", completeLive)
			}
			t.Logf("live burst of %d hidden children: compact frames=%d bytes=%d, complete frames=%d bytes=%d", burst, compactLive, compactBytes, completeLive, completeBytes)
			catch, catchBytes, catchFrames := testCompactQuery(t, w, mgr, "phone", 4, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: compact.Epoch, Version: compact.Version, Limit: 256, Projection: protocol.ThreadProjectionCompact})
			checkRows(catch)
			interval, err := store.CompactCatchUp(t.Context(), testConvID, compact.Epoch, compact.Version, 256)
			if err != nil || catch.Version != latest || catch.FromVersion == nil || *catch.FromVersion != compact.Version {
				t.Fatalf("catch-up interval: %+v %v", catch, err)
			}
			testCompactHeaders(t, catch.Headers, interval.Headers)
			t.Logf("catch-up compact bytes=%d physical frames=%d", catchBytes, catchFrames)
			measured = append(measured, [4]int{frames, pageFrames, catchFrames, compactLive})
			// A following idle pass must not repeat already-flushed state.
			s.publish(t.Context(), testConvID, cw)
			for _, f := range testCompactRead(t, w, mgr, "phone", "legacy") {
				if f.env.Type == protocol.TypeThreadCompactUpdate {
					t.Fatal("idle pass repeated the final flush")
				}
			}
		})
	}
	if len(measured) == 2 {
		for i := range measured[0] {
			if measured[1][i] > measured[0][i] {
				t.Fatalf("hidden history grew compact frame count: small=%v large=%v", measured[0], measured[1])
			}
		}
	}
}

func equalCount(a, b *uint64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// A compact load captured before a nested change is cancelled by the compact
// live frame its unjoined connection receives; the retry converges on the Store.
func TestDaemonThreadCompactBoundary(t *testing.T) {
	s, store, _ := testDaemonExpansionFixture(t, 3, 1, 64)
	captured, release, published := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once, delivered sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	w, mgr := testDaemonThreadQueryWire(t, s, func(c *relay.V2SessionConfig) {
		query := c.ThreadCompactCatchUp
		first := true
		c.ThreadCompactCatchUp = func(ctx context.Context, r protocol.ThreadCatchUpPayload) (thread.CompactQueryResult, error) {
			q, err := query(ctx, r)
			if first {
				first = false
				close(captured)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			return q, err
		}
	})
	push := s.publisher.push
	s.publisher.push = func(ctx context.Context, peer string, e protocol.Envelope) error {
		err := push(ctx, peer, e)
		if e.Type == protocol.TypeThreadCompactUpdate {
			delivered.Do(func() { close(published) })
		}
		return err
	}
	w.open("phone", false, protocol.CapabilityThread, protocol.CapabilityThreadExpansion, protocol.CapabilityThreadCompact)
	request := protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: "e", Limit: 256, Projection: protocol.ThreadProjectionCompact}
	testDaemonThreadRequest(t, w, "phone", 10, protocol.TypeThreadCatchUp, request)
	awaitSwitchPublication(t, captured)
	testDaemonThreadAppend(t, s.history, "tool_use", `{"turn_id":"t","parent_tool_use_id":"root","tool_use_id":"late","name":"Read","input":{}}`)
	awaitSwitchPublication(t, published)
	unblock()
	failed := false
	for _, f := range testCompactRead(t, w, mgr, "phone") {
		if f.env.InReplyTo != nil && *f.env.InReplyTo == 10 {
			if f.env.Type != protocol.TypeError {
				t.Fatal("racing compact load certified a stale capture")
			}
			failed = true
		}
	}
	if !failed {
		t.Fatal("racing compact load was not refused")
	}
	got, _, _ := testCompactQuery(t, w, mgr, "phone", 11, protocol.TypeThreadCatchUp, request)
	want, err := store.CompactNewestWindow(t.Context(), testConvID, 256)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != want.Version || len(got.Headers) != len(want.Headers) {
		t.Fatalf("retry did not converge: version %d/%d headers %d/%d", got.Version, want.Version, len(got.Headers), len(want.Headers))
	}
	for _, h := range want.Headers {
		for _, g := range got.Headers {
			if g.ID == h.ID && (g.Rev != h.Rev || !equalCount(g.SubtreeVersion, h.SubtreeVersion) || !equalCount(g.PublicChildCount, h.PublicChildCount)) {
				t.Fatal("retried header differs from Store")
			}
		}
	}
}

// testCompactReply returns the type and payload correlated with id.
func testCompactReply(t *testing.T, w *switchWire, mgr *relay.V2SessionManager, peer string, id uint64) (string, json.RawMessage) {
	t.Helper()
	for {
		for _, f := range testCompactRead(t, w, mgr, peer) {
			if f.env.InReplyTo != nil && *f.env.InReplyTo == id {
				return f.env.Type, f.env.Payload
			}
		}
	}
}

func testCompactHeaders(t *testing.T, got []protocol.ThreadSummary, want []thread.ChildSummary) {
	t.Helper()
	byID := map[uint64]protocol.ThreadSummary{}
	for _, h := range got {
		byID[h.ID] = h
	}
	if len(byID) != len(got) || len(got) != len(want) {
		t.Fatalf("headers %d, Store %d", len(got), len(want))
	}
	for _, h := range want {
		if a, b := mustQueryJSON(t, byID[h.ID]), mustQueryJSON(t, daemonThreadSummary(h)); !bytes.Equal(a, b) {
			t.Fatalf("header differs from Store: %s %s", a, b)
		}
	}
}

// A compact client recovers only through compact catch-up: a reconnect from its
// retained checkpoint and an epoch reset both converge on the Store's root
// headers, and a child page read before a nested change is detectably stale.
func TestDaemonThreadCompactRecovery(t *testing.T) {
	s, store, items := testDaemonExpansionFixture(t, 3, 1, 64)
	w, mgr := testDaemonThreadQueryWire(t, s, nil)
	caps := []string{protocol.CapabilityThread, protocol.CapabilityThreadExpansion, protocol.CapabilityThreadCompact}
	w.open("phone", false, caps...)
	w.open("legacy", false, protocol.CapabilityThread)
	// Only a negotiated thread_compact makes an unjoined peer a compact recipient.
	conns := mgr.ActiveThreadConns(t.Context(), testConvID)
	if len(conns) != 2 {
		t.Fatalf("thread conns: %d", len(conns))
	}
	for _, c := range conns {
		if c.View != relay.ThreadViewUnjoined || c.ThreadCompact != (c.ConnID == "phone") {
			t.Fatalf("unjoined compact routing facts: %+v", c)
		}
	}
	window := protocol.ThreadCatchUpPayload{ConversationID: testConvID, Epoch: "e", Limit: 256, Projection: protocol.ThreadProjectionCompact}
	base, _, _ := testCompactQuery(t, w, mgr, "phone", 20, protocol.TypeThreadCatchUp, window)
	complete, _, _ := testCompactQuery(t, w, mgr, "legacy", 21, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 256})
	scope := testDaemonExpansionScope(t, store, items, "root")
	testDaemonThreadRequest(t, w, "phone", 22, protocol.TypeThreadReadChildren, scope)
	e, _, _ := testDaemonExpansionReply(t, w, "phone", 22, protocol.TypeThreadChildren)
	var fetched protocol.ThreadChildrenPayload
	if err := json.Unmarshal(e.Payload, &fetched); err != nil || fetched.Version != base.Version {
		t.Fatalf("child page capture: %s %v", e.Payload, err)
	}

	testDaemonThreadAppend(t, s.history, "tool_use", `{"turn_id":"t","parent_tool_use_id":"root","tool_use_id":"late","name":"Read","input":{"secret":"unopened-late"}}`)
	latest, _ := s.history.LatestEntryID(testConvID)
	testShadowWait(t, func() bool { return store.Status(testConvID).Version >= latest })
	cw := s.workers[testConvID]
	testShadowWait(t, func() bool {
		if !cw.lockPublication(t.Context()) {
			return false
		}
		defer cw.unlockPublication()
		return cw.threadVersion >= latest
	})
	// Isolation: each projection keeps its own checkpoint on one conversation.
	var compactTo, completeTo uint64
	for _, f := range testCompactRead(t, w, mgr, "phone", "legacy") {
		switch {
		case f.peer == "phone" && f.env.Type == protocol.TypeThreadCompactUpdate:
			var u protocol.ThreadCompactUpdatePayload
			if json.Unmarshal(f.env.Payload, &u) != nil || u.FromVersion != base.Version || bytes.Contains(f.env.Payload, []byte("unopened-")) {
				t.Fatalf("compact live frame: %s", f.env.Payload)
			}
			compactTo = u.Version
		case f.peer == "legacy" && f.env.Type == protocol.TypeThreadItemAdded:
			var u protocol.ThreadItemAddedPayload
			if json.Unmarshal(f.env.Payload, &u) != nil || u.Version <= complete.Version {
				t.Fatal("complete live frame lost its checkpoint")
			}
			completeTo = u.Version
		case f.peer == "legacy" && f.env.Type == protocol.TypeThreadCompactUpdate, f.peer == "phone" && f.env.Type == protocol.TypeThreadItemAdded:
			t.Fatalf("%s got %s", f.peer, f.env.Type)
		}
	}
	if compactTo != latest || completeTo != latest {
		t.Fatalf("checkpoints: compact %d complete %d, Store %d", compactTo, completeTo, latest)
	}

	// Reconnect: a new connection resumes from the retained compact checkpoint.
	w.open("again", false, caps...)
	resumed := window
	resumed.Epoch, resumed.Version = base.Epoch, base.Version
	got, _, _ := testCompactQuery(t, w, mgr, "again", 23, protocol.TypeThreadCatchUp, resumed)
	want, err := store.CompactCatchUp(t.Context(), testConvID, base.Epoch, base.Version, 256)
	if err != nil || got.FromVersion == nil || *got.FromVersion != base.Version || got.Version != latest {
		t.Fatalf("compact catch-up from checkpoint: %+v %v", got, err)
	}
	testCompactHeaders(t, got.Headers, want.Headers)
	stale := false
	for _, h := range got.Headers {
		if h.ID == scope.ItemID {
			stale = h.SubtreeVersion != nil && *h.SubtreeVersion > fetched.Version
		}
	}
	if !stale {
		t.Fatal("child page read before the nested change is not detectably stale")
	}
	scope.Epoch = got.Epoch
	testDaemonThreadRequest(t, w, "again", 24, protocol.TypeThreadReadChildren, scope)
	e, _, _ = testDaemonExpansionReply(t, w, "again", 24, protocol.TypeThreadChildren)
	var fresh protocol.ThreadChildrenPayload
	if err := json.Unmarshal(e.Payload, &fresh); err != nil || fresh.Version != latest {
		t.Fatalf("fresh child page: %s %v", e.Payload, err)
	}

	// Epoch reset: the reply is a compact-scoped reset, never a baseline, and a
	// newest window then converges on the Store.
	resumed.Epoch = "different"
	testDaemonThreadRequest(t, w, "again", 25, protocol.TypeThreadCatchUp, resumed)
	typ, raw := testCompactReply(t, w, mgr, "again", 25)
	if typ != protocol.TypeThreadReset {
		t.Fatalf("stale epoch reached %s", typ)
	}
	var reset protocol.ThreadResetPayload
	if err := json.Unmarshal(raw, &reset); err != nil || reset.Projection != protocol.ThreadProjectionCompact || reset.Epoch != base.Epoch {
		t.Fatalf("reset scope: %s %v", raw, err)
	}
	got, _, _ = testCompactQuery(t, w, mgr, "again", 26, protocol.TypeThreadCatchUp, window)
	newest, err := store.CompactNewestWindow(t.Context(), testConvID, 256)
	if err != nil || got.Version != newest.Version {
		t.Fatalf("newest window after reset: %d/%d %v", got.Version, newest.Version, err)
	}
	testCompactHeaders(t, got.Headers, newest.Headers)
}
