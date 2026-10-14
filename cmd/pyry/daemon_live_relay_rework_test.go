package main

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestDaemonLiveRelayPublisherConnectGap(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	send := b.sink.sinkForSessionTag(newStreamSessionTag("a"), "claude")
	entered, release := make(chan struct{}), make(chan struct{})
	emptyCaptured, freshAdmitted := make(chan struct{}), make(chan struct{})
	wait := func(signal <-chan struct{}, phase string) {
		t.Helper()
		select {
		case <-signal:
		case <-time.After(3 * time.Second):
			t.Fatalf("connect-gap fixture did not reach %s", phase)
		}
	}
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	w, _ := testDaemonRelayWire(t, b, relayWiring{}, nil, func(cfg *relay.V2SessionConfig) {
		snapshot := cfg.ThreadLiveState
		calls := 0
		cfg.ThreadLiveState = func() func() (relay.LiveState, bool) {
			cursor := snapshot()
			calls++
			if calls == 1 {
				close(emptyCaptured)
			}
			if calls == 2 {
				send(turnevent.ModelAnnounced{Model: "fresh"})
				close(freshAdmitted)
			}
			return cursor
		}
	}, func(ctx context.Context, mgr *relay.V2SessionManager) func() {
		return startDaemonLivePublisher(ctx, b.sink.live, mgr.ActiveConns, func(ctx context.Context, id string, r relay.LiveState) error {
			if bytes.Contains(r.Envelope.Payload, []byte("old")) {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			return mgr.PushLiveState(ctx, id, r)
		}, func(ctx context.Context, id string) { _ = mgr.RetireLiveState(ctx, id) }, discardLogger())
	})
	w.open("phone", false, protocol.CapabilityThread)
	// The handshake response precedes capture of the supplied live snapshot.
	wait(emptyCaptured, "empty snapshot capture")
	send(turnevent.ModelAnnounced{Model: "old"})
	wait(entered, "held delivery")
	w.open("again", false, protocol.CapabilityThread)
	wait(freshAdmitted, "replacement admission after second snapshot capture")
	unblock()
	seen := map[string]bool{}
	for len(seen) < 2 {
		id, e := w.read()
		if e.Type == protocol.TypeModelAnnounced && !e.SessionStateCleared {
			var p protocol.ModelAnnouncedPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p != (protocol.ModelAnnouncedPayload{ConversationID: "conv", Model: "fresh"}) || string(e.SessionID) != `"a"` {
				t.Fatalf("stale or unsourced reading: %+v", e)
			}
			if id != "phone" && id != "again" {
				t.Fatalf("unexpected recipient: %s", id)
			}
			seen[id] = true
		}
	}
}

type testHeldModalConsume struct {
	*modalbridge.Registry
	consumed, release chan struct{}
}

func (r testHeldModalConsume) Resolve(id string) (modalbridge.Outstanding, bool) {
	out, ok := r.Registry.Resolve(id)
	if ok {
		close(r.consumed)
		<-r.release
	}
	return out, ok
}

func TestDaemonLiveRelayPermissionRetirementGap(t *testing.T) {
	for _, option := range []string{"cancel", string(turnevent.PermissionOptionKindAllowOnce), string(turnevent.PermissionOptionKindRejectOnce)} {
		t.Run(option, func(t *testing.T) {
			bridge, resolver, _, bc := testPromptBridge(t, nil, discardLogger())
			b := testControlBindings(t, testConvID, "a")
			bridge.live = b
			w, _ := testDaemonRelayWire(t, b, relayWiring{}, nil, nil)
			w.open("phone", false, protocol.CapabilityThread)
			req := permbridge.Request{SessionID: "a", ToolName: "Bash", ToolUseID: "permission", Input: json.RawMessage(`{"command":"true"}`)}
			_, id, retire := testPromptSurface(t, bridge, bc, req, time.Minute)
			testDaemonWireReading(t, w, protocol.TypeModalShown, 0)
			gate := testHeldModalConsume{Registry: resolver.reg.(*modalbridge.Registry), consumed: make(chan struct{}), release: make(chan struct{})}
			resolver.reg = gate
			var once sync.Once
			unblock := func() { once.Do(func() { close(gate.release) }) }
			defer unblock()
			resolved := make(chan bool, 1)
			go func() {
				var ok bool
				dev := &devices.Device{TokenHash: "paired"}
				if option == "cancel" {
					_, ok = resolver.ResolveCancel(id, dev)
				} else {
					_, ok = resolver.ResolveAnswerWithAlwaysAllow(id, option, "token", true, dev)
				}
				resolved <- ok
			}()
			<-gate.consumed
			retired := make(chan struct{})
			go func() { retire(); close(retired) }()
			// The remote registry consume has won. Retirement may complete here only
			// if it is independent of the winner's captured source ownership.
			select {
			case <-retired:
			case <-time.After(50 * time.Millisecond):
			}
			bridge.perm.Resolve(req.ToolUseID, permbridge.Deny("timeout"))
			unblock()
			if !<-resolved {
				t.Fatal("winning resolver refused")
			}
			<-retired
			gone := testDaemonWireReading(t, w, protocol.TypeModalDismissed, 0)
			var p protocol.ModalDismissedPayload
			if err := json.Unmarshal(gone.Payload, &p); err != nil {
				t.Fatal(err)
			}
			want := option
			if option == "cancel" {
				want = "cancelled"
			}
			if p.ModalID != id || p.Outcome != want || p.Source != "remote" || string(gone.SessionID) != `"a"` {
				t.Fatalf("winner's dismissal changed: %+v", gone)
			}
			retire()
			if _, found := testLiveReadings(t, b.sink.live, testConvID)[liveReadingKey{protocol.TypeModalShown, id}]; found {
				t.Fatal("consumed prompt remained retained")
			}
		})
	}
}

func TestDaemonLiveRelayModelProjection(t *testing.T) {
	for _, name := range []string{"stream", "requested-multi", "requested-basic"} {
		t.Run(name, func(t *testing.T) {
			requested, multi := name != "stream", name != "requested-basic"
			pool, plan := newModelListTestPool(t)
			sid := string(pool.BootstrapID())
			b := testControlBindings(t, "conv", sid)
			list := sentinelModelList("live")
			plan.arm(pool.BootstrapID(), list)
			saved := twoAgentVocabularyDouble{codex: sentinelCodexModels()}
			wiring := relayWiring{modelListReading: daemonModelListReadingFor(b.reg, pool, saved, b)}
			src := b.capture("conv", sid, false)
			if !requested {
				b.sink.live.acceptEvent(src, list)
			}
			w, _ := testDaemonRelayWire(t, b, wiring, nil, func(cfg *relay.V2SessionConfig) { cfg.MergedModelOptions = pushedModelOptions(saved) })
			w.open("phone", multi, protocol.CapabilityThread)
			if !requested {
				testDaemonModelProjection(t, "phone", testDaemonWireReading(t, w, protocol.TypeModelList, 0), list, sid, multi)
			}
			w.open("again", !multi, protocol.CapabilityThread)
			if !requested {
				_, e := w.read()
				if e.SessionStateCleared {
					_, e = w.read()
				}
				testDaemonModelProjection(t, "again", e, list, sid, !multi)
				b.sink.live.acceptEvent(src, list)
			} else {
				// Establish source evidence without admitting a live reading first.
				b.sink.live.mu.Lock()
				b.sink.live.cacheInventoryLocked(src, list)
				b.sink.live.mu.Unlock()
				testDaemonWireRequest(t, w, 80, protocol.TypeRequestModelList, protocol.RequestModelListPayload{ConversationID: "conv"})
			}
			seen := map[string]bool{}
			reply := !requested
			for len(seen) < 2 || !reply {
				id, e := w.read()
				if e.Type != protocol.TypeModelList || e.SessionStateCleared {
					continue
				}
				testDaemonModelProjection(t, id, e, list, sid, (id == "phone") == multi)
				seen[id] = true
				if e.InReplyTo != nil && *e.InReplyTo == 80 {
					reply = true
				}
			}
			retained := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeModelList, ""}]
			canonical, _ := mapModelList(list, "conv")
			raw, _ := json.Marshal(canonical)
			if !bytes.Equal(retained.Envelope.Payload, raw) {
				t.Fatal("retention contains a recipient projection")
			}
		})
	}
}

func testDaemonModelProjection(t *testing.T, id string, e protocol.Envelope, list turnevent.ModelList, sid string, multi bool) {
	t.Helper()
	want, _ := mapModelList(list, "conv")
	if multi {
		want.Models = pushedModelOptions(twoAgentVocabularyDouble{codex: sentinelCodexModels()})(want.Models)
	}
	raw, _ := json.Marshal(want)
	if e.Type != protocol.TypeModelList || !bytes.Equal(e.Payload, raw) || string(e.SessionID) != `"`+sid+`"` {
		t.Fatalf("%s projection/source: got %s (%s), want %s", id, e.Payload, e.SessionID, raw)
	}
	envelope, _ := json.Marshal(e)
	if len(envelope) > protocol.MaxThreadEnvelopeBytes {
		t.Fatal("projected reading exceeded envelope bound")
	}
}

func TestDaemonLiveRelayCodexOnlyProjection(t *testing.T) {
	pool, _ := newModelListTestPool(t)
	b := testControlBindings(t, "conv", string(pool.BootstrapID()))
	saved := twoAgentVocabularyDouble{codex: sentinelCodexModels()}
	published := make(chan struct{})
	w, mgr := testDaemonRelayWire(t, b, relayWiring{modelListReading: daemonModelListReadingFor(b.reg, pool, saved, b)}, nil, func(cfg *relay.V2SessionConfig) {
		cfg.MergedModelOptions = pushedModelOptions(saved)
	}, func(ctx context.Context, mgr *relay.V2SessionManager) func() {
		return startDaemonLivePublisher(ctx, b.sink.live, mgr.ActiveConns, func(ctx context.Context, id string, r relay.LiveState) error {
			err := mgr.PushLiveState(ctx, id, r)
			if id == "again" {
				close(published)
			}
			return err
		}, func(ctx context.Context, id string) { _ = mgr.RetireLiveState(ctx, id) }, discardLogger())
	})
	w.open("phone", true, protocol.CapabilityThread)
	w.open("again", false, protocol.CapabilityThread)
	testDaemonWireRequest(t, w, 81, protocol.TypeRequestModelList, protocol.RequestModelListPayload{ConversationID: "conv"})
	select {
	case <-published:
	case <-time.After(3 * time.Second):
		t.Fatal("requested inventory was not published")
	}
	if err := mgr.FlushPushes(t.Context()); err != nil {
		t.Fatal(err)
	}
	marker := protocol.Envelope{ID: 99, Type: protocol.TypeError, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)}
	if err := mgr.Push(t.Context(), "again", marker); err != nil {
		t.Fatal(err)
	}
	replied := false
	for {
		id, e := w.read()
		if id == "again" {
			if e.Type == protocol.TypeModelList {
				t.Fatal("unavailable Claude vocabulary reached a basic recipient")
			}
			if e.ID == marker.ID {
				break
			}
		}
		if id == "phone" && e.Type == protocol.TypeModelList && !e.SessionStateCleared {
			want, _ := json.Marshal(protocol.ModelListPayload{ConversationID: "conv", Models: codexModelOptions(saved)})
			if !bytes.Equal(e.Payload, want) {
				t.Fatalf("Codex-only projection: got %s, want %s", e.Payload, want)
			}
			if len(e.SessionID) != 0 {
				t.Fatal("persisted vocabulary acquired session evidence")
			}
			replied = replied || (e.InReplyTo != nil && *e.InReplyTo == 81)
		}
	}
	if !replied {
		testDaemonWireReading(t, w, protocol.TypeModelList, 81)
	}
	if _, ok := modelListFor(b.reg, pool, saved, b)("conv", false); ok {
		t.Fatal("basic legacy provider lost its refusal")
	}
}

func TestDaemonLiveRelayBorrowedBootstrapVocabulary(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(map[bool]string{false: "basic", true: "multi"}[multi], func(t *testing.T) {
			b, pool, runner := testInventoryIngress(t, true)
			sid := string(pool.BootstrapID())
			b.reg.Create(conversations.Conversation{ID: "other", CurrentSessionID: "other-session"})
			parser := streamsup.NewParser(runner.models.Sink, discardLogger())
			if _, err := parser.Write(testInventoryLine(protocol.TypeModelList, "bootstrap")); err != nil {
				t.Fatal(err)
			}
			target := b.capture("other", "other-session", false)
			// Foreign and target generation counters can coincide. That coincidence
			// cannot confer retention authority on borrowed producer evidence.
			b.sink.queueBoundary(sessions.SessionTransition{ConversationID: "conv", PreviousID: sessions.SessionID(sid), NewID: sessions.SessionID(sid), NextAgent: "claude"}, func() {}, nil)
			if _, err := parser.Write(testInventoryLine(protocol.TypeModelList, "bootstrap")); err != nil {
				t.Fatal(err)
			}
			list, evidence, have := runner.ModelListLive()
			if !have || evidence.ConversationID != "conv" || evidence.SessionGeneration == 0 || evidence.SessionGeneration != target.SessionGeneration {
				t.Fatal("parser did not supply bootstrap evidence")
			}
			saved := twoAgentVocabularyDouble{codex: sentinelCodexModels()}
			provider := daemonModelListReadingFor(b.reg, pool, saved, b)
			w, mgr := testDaemonRelayWire(t, b, relayWiring{modelListReading: provider}, nil, func(cfg *relay.V2SessionConfig) {
				cfg.MergedModelOptions = pushedModelOptions(saved)
			})
			w.open("phone", multi, protocol.CapabilityThread)
			testDaemonWireReading(t, w, protocol.TypeModelList, 0)
			var predecessor daemonLiveReading
			for phase := 0; phase < 2; phase++ {
				if phase == 1 {
					b.sink.queueBoundary(sessions.SessionTransition{ConversationID: "other", PreviousID: "other-session", NewID: "other-session", NextAgent: "claude"}, func() {}, nil)
				}
				before, retainedBefore := testLiveReadings(t, b.sink.live, "other")[liveReadingKey{protocol.TypeModelList, ""}]
				source := b.capture("other", "other-session", false)
				reading, ok := provider("other", multi)
				if !ok || reading.ConversationID != "other" || reading.SessionGeneration != source.SessionGeneration || reading.Revision == 0 || string(reading.Envelope.SessionID) != `"`+sid+`"` {
					t.Fatalf("invalid borrowed reply ordering/source: %+v", reading)
				}
				if phase == 0 {
					predecessor = reading
				}
				requestID := uint64(90 + phase)
				testDaemonWireRequest(t, w, requestID, protocol.TypeRequestModelList, protocol.RequestModelListPayload{ConversationID: "other"})
				reply := testDaemonWireReading(t, w, protocol.TypeModelList, requestID)
				want, _ := mapModelList(list, "other")
				if multi {
					want.Models = pushedModelOptions(saved)(want.Models)
				}
				raw, _ := json.Marshal(want)
				if !bytes.Equal(reply.Payload, raw) || string(reply.SessionID) != `"`+sid+`"` {
					t.Fatalf("borrowed vocabulary/source changed: %+v", reply)
				}
				encoded, _ := json.Marshal(reply)
				if len(encoded) > protocol.MaxThreadEnvelopeBytes {
					t.Fatal("borrowed reply exceeded envelope limit")
				}
				legacy, ok := modelListFor(b.reg, pool, saved, b)("other", multi)
				legacyRaw, _ := json.Marshal(legacy)
				if !ok || !bytes.Equal(legacyRaw, raw) {
					t.Fatal("legacy fallback bytes changed")
				}
				after, retainedAfter := testLiveReadings(t, b.sink.live, "other")[liveReadingKey{protocol.TypeModelList, ""}]
				if retainedBefore != retainedAfter || (retainedAfter && (!after.Envelope.SessionStateCleared || after.Revision != before.Revision)) {
					t.Fatal("borrowed evidence replaced target retention")
				}
			}
			// Source caches remain paired with their original generation across borrowing.
			_, afterEvidence, _ := runner.ModelListLive()
			if afterEvidence != evidence {
				t.Fatal("borrowing changed bootstrap evidence")
			}
			// A delayed response captured before the reused-ID clear cannot overtake it.
			lateID := uint64(100)
			predecessor.Envelope.InReplyTo = &lateID
			if err := mgr.PushLiveState(t.Context(), "phone", daemonRelayReading(predecessor)); err != nil {
				t.Fatal(err)
			}
			if err := mgr.FlushPushes(t.Context()); err != nil {
				t.Fatal(err)
			}
			marker := protocol.Envelope{ID: 101, Type: protocol.TypeError, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)}
			if err := mgr.Push(t.Context(), "phone", marker); err != nil {
				t.Fatal(err)
			}
			for {
				_, e := w.read()
				if e.InReplyTo != nil && *e.InReplyTo == lateID {
					t.Fatal("borrowed predecessor overtook successor")
				}
				if e.ID == marker.ID {
					break
				}
			}
		})
	}
}
