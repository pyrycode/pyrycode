package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testDaemonRelayWire(t *testing.T, b *daemonLiveBindings, wiring relayWiring, usage *contextUsageResolver, setup func(*relay.V2SessionConfig), publisher ...func(context.Context, *relay.V2SessionManager) func()) (*switchWire, *relay.V2SessionManager) {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	paired := &devices.Registry{}
	for _, id := range []string{"phone", "legacy", "again"} {
		paired.Add(devices.Device{TokenHash: devices.HashToken(id + "-token"), Name: "test", PairedAt: time.Now().UTC()})
	}
	wire := &switchWire{t: t, frames: make(chan protocol.RoutingEnvelope, 16), out: make(chan protocol.RoutingEnvelope, 256), phones: map[string]switchPhone{}, pub: key.PublicKey().Bytes()}
	cfg := relay.V2SessionConfig{Frames: wire.frames, Outbound: func(e protocol.RoutingEnvelope) error { wire.out <- e; return nil }, StaticPriv: key.Bytes(), Devices: paired, ServerID: string(identity.NewServerID()), Logger: discardLogger(), KnownConversation: func(id string) bool { _, ok := b.reg.Get(conversations.ConversationID(id)); return ok }}
	wiring.streamSink = b.sink
	installDaemonLiveRelay(&cfg, wiring, usage)
	if cfg.ThreadReady != nil {
		t.Fatal("production thread readiness enabled")
	}
	// Readiness remains deliberately test-only until folded-item publication lands.
	cfg.ThreadReady = func() bool { return true }
	cfg.ThreadLastShownVersion = func(string) (uint64, bool) { return 0, true }
	if setup != nil {
		setup(&cfg)
	}
	mgr, err := relay.NewV2SessionManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var stop func()
	if len(publisher) > 0 {
		stop = publisher[0](ctx, mgr)
	} else {
		stop = startDaemonLivePublisher(ctx, b.sink.live, mgr.ActiveConns, mgr.PushLiveState, func(ctx context.Context, id string) { _ = mgr.RetireLiveState(ctx, id) }, discardLogger())
	}
	go func() { defer close(done); _ = mgr.Run(ctx) }()
	t.Cleanup(func() { cancel(); stop(); <-done })
	return wire, mgr
}

func testDaemonWireReading(t *testing.T, w *switchWire, typ string, correlation uint64) protocol.Envelope {
	t.Helper()
	for {
		id, e := w.read()
		if id != "phone" {
			t.Fatalf("unexpected recipient %s", id)
		}
		if e.SessionStateCleared && !bytes.Equal(e.Payload, []byte(`{}`)) {
			t.Fatalf("clear carries reading payload: %s", e.Payload)
		}
		if e.Type == typ && !e.SessionStateCleared && (correlation == 0 || (e.InReplyTo != nil && *e.InReplyTo == correlation)) {
			return e
		}
	}
}

func testDaemonWireRequest(t *testing.T, w *switchWire, id uint64, typ string, payload any) {
	t.Helper()
	p, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(protocol.Envelope{ID: id, Type: typ, TS: time.Now().UTC(), Payload: p})
	sealed, err := w.phones["phone"].send.Encrypt(raw)
	if err != nil {
		t.Fatal(err)
	}
	w.frames <- w.wrap("phone", protocol.TypeNoiseMsg, sealed)
}

func TestDaemonLiveRelayPublication(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	b.reg.Create(conversations.Conversation{ID: "other", CurrentSessionID: "b"})
	send := b.sink.sinkForSessionTag(newStreamSessionTag("a"), "claude")
	// Retention precedes connect, and inactive statuses are positive snapshot readings.
	send(turnevent.ApiRetry{})
	send(turnevent.Compacting{})
	w, mgr := testDaemonRelayWire(t, b, relayWiring{}, nil, nil)
	w.open("phone", false, protocol.CapabilityThread)
	seen := map[string]bool{}
	for len(seen) < 2 {
		_, e := w.read()
		if e.SessionStateCleared {
			continue
		}
		if string(e.SessionID) != `"a"` {
			t.Fatal("snapshot provenance lost")
		}
		seen[e.Type] = true
	}
	if !seen[protocol.TypeApiRetry] || !seen[protocol.TypeCompacting] {
		t.Fatal("inactive readings missing")
	}
	w.open("legacy", false)
	// Fill the legacy fan-in so admission can no longer enter its queue.
	for i := 0; i < cap(b.sink.ch)+1; i++ {
		send(turnevent.Stall{})
	}
	got := testDaemonWireReading(t, w, protocol.TypeStall, 0)
	if string(got.SessionID) != `"a"` {
		t.Fatal("stream source lost")
	}
	// Actual control producer refuses its legacy queue, yet reaches the wire with unresolved source.
	notify := sessionErrorNotify(make(chan giveUpNotice), discardLogger(), b)
	notify("other", "blocked")
	got = testDaemonWireReading(t, w, protocol.TypeSessionError, 0)
	if len(got.SessionID) != 0 || !bytes.Contains(got.Payload, []byte(`"other"`)) {
		t.Fatal("control source/owner changed")
	}
	if err := mgr.FlushPushes(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The legacy recipient gets only its existing Push bytes, never duplicate live publication.
	want := protocol.Envelope{ID: 99, Type: protocol.TypeStall, TS: time.Unix(1, 0).UTC(), Payload: json.RawMessage(`{"conversation_id":"conv"}`)}
	if err := mgr.Push(t.Context(), "legacy", want); err != nil {
		t.Fatal(err)
	}
	for {
		id, e := w.read()
		if id != "legacy" {
			continue
		}
		raw, _ := json.Marshal(e)
		expected, _ := json.Marshal(want)
		if !bytes.Equal(raw, expected) {
			t.Fatalf("legacy bytes changed: %s", raw)
		}
		break
	}
}

func TestDaemonLiveRelayOrdering(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	src := b.capture("conv", "a", false)
	b.sink.live.acceptEvent(src, turnevent.ModelAnnounced{Model: "snapshot"})
	cursor := daemonRelaySnapshot(b.sink.live)()
	w, mgr := testDaemonRelayWire(t, b, relayWiring{}, nil, nil)
	w.open("phone", false, protocol.CapabilityThread)
	testDaemonWireReading(t, w, protocol.TypeModelAnnounced, 0)
	for i := 0; i < 2; i++ {
		b.sink.queueBoundary(sessions.SessionTransition{ConversationID: "conv", PreviousID: "a", NewID: "a", NextAgent: "claude"}, func() {}, nil)
		fresh := b.capture("conv", "a", false)
		b.sink.live.acceptEvent(fresh, turnevent.ModelAnnounced{Model: "fresh"})
		cleared := false
		for {
			_, e := w.read()
			if e.Type != protocol.TypeModelAnnounced {
				continue
			}
			if e.SessionStateCleared {
				cleared = true
				continue
			}
			if !cleared || !bytes.Contains(e.Payload, []byte("fresh")) {
				t.Fatal("clear-before-fresh broken")
			}
			break
		}
	}
	// A detached captured predecessor cannot restore indicators after reused-ID clears.
	old, _ := cursor()
	if err := mgr.PushLiveState(t.Context(), "phone", old); err != nil {
		t.Fatal(err)
	}
	b.sink.live.acceptEvent(src, turnevent.ModelAnnounced{Model: "stale"})
	barrier := protocol.Envelope{ID: 120, Type: protocol.TypeError, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)}
	if err := mgr.FlushPushes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Push(t.Context(), "phone", barrier); err != nil {
		t.Fatal(err)
	}
	for {
		_, e := w.read()
		if bytes.Contains(e.Payload, []byte("stale")) || bytes.Contains(e.Payload, []byte("snapshot")) {
			t.Fatal("predecessor restored")
		}
		if e.Type == protocol.TypeError {
			break
		}
	}
}

func TestDaemonLiveRelayPrompts(t *testing.T) {
	for _, question := range []bool{false, true} {
		t.Run(map[bool]string{false: "permission", true: "question"}[question], func(t *testing.T) {
			bridge, modal, questions, bc := testPromptBridge(t, nil, discardLogger())
			b := testControlBindings(t, testConvID, "a")
			bridge.live = b
			w, _ := testDaemonRelayWire(t, b, relayWiring{}, nil, func(cfg *relay.V2SessionConfig) { cfg.ModalResolver = modal; cfg.QuestionResolver = questions })
			w.open("phone", false, protocol.CapabilityThread)
			tool, input, family, dismiss := "Bash", json.RawMessage(`{"command":"true"}`), protocol.TypeModalShown, protocol.TypeModalDismissed
			if question {
				tool = "AskUserQuestion"
				input = json.RawMessage(`{"questions":[{"question":"Choose?","header":"Choice","options":[{"label":"A","description":"A"},{"label":"B","description":"B"}],"multiSelect":false}]}`)
				family, dismiss = protocol.TypeQuestionShown, protocol.TypeQuestionDismissed
			}
			req := permbridge.Request{SessionID: "a", ToolName: tool, ToolUseID: "prompt", Input: input, RequiresUserInteraction: question}
			pending, id, retire := testPromptSurface(t, bridge, bc, req, time.Minute)
			shown := testDaemonWireReading(t, w, family, 0)
			if string(shown.SessionID) != `"a"` || !bytes.Contains(shown.Payload, []byte(id)) {
				t.Fatal("prompt owner/id lost")
			}
			if question {
				testDaemonWireRequest(t, w, 44, protocol.TypeQuestionRefused, protocol.QuestionRefusedPayload{QuestionBatchID: id})
			} else {
				testDaemonWireRequest(t, w, 44, protocol.TypeModalAnswer, protocol.ModalAnswerPayload{ModalID: id, OptionID: string(turnevent.PermissionOptionKindAllowOnce)})
			}
			gone := testDaemonWireReading(t, w, dismiss, 0)
			if string(gone.SessionID) != `"a"` || !bytes.Contains(gone.Payload, []byte(id)) || !bytes.Contains(gone.Payload, []byte(`"remote"`)) {
				t.Fatalf("dismissal source/id lost: %s", gone.Payload)
			}
			_ = pending.Await()
			retire()
			retire()
			cursor := daemonRelaySnapshot(b.sink.live)()
			for {
				r, ok := cursor()
				if !ok {
					break
				}
				if r.ReadingID == id {
					t.Fatal("dismissed prompt snapshot")
				}
			}
		})
	}
}

func TestDaemonLiveRelayReplies(t *testing.T) {
	for _, name := range []string{"context", "mcp", "model", "settings", "update"} {
		t.Run(name, func(t *testing.T) {
			pool, models := newModelListTestPool(t)
			var mcps *mcpStatusQueryPlan
			if name == "mcp" {
				pool, mcps = newMCPStatusQueryTestPool(t)
				mcps.arm(pool.BootstrapID(), sentinelMCPStatus("reply"))
			}
			sid := string(pool.BootstrapID())
			b := testControlBindings(t, "conv", sid)
			src := b.capture("conv", sid, false)
			list := turnevent.ModelList{Models: []turnevent.ModelOption{{Value: "sonnet"}}}
			models.arm(sessions.SessionID(sid), list)
			wiring := relayWiring{runSettings: runSettingsFor(b.reg, pool, nil, b), modelListReading: daemonModelListReadingFor(b.reg, pool, nil, b), mcpStatusReading: daemonMCPStatusReadingFor(b.reg, pool, b), settings: liveSettingsUpdater{settingsUpdaterAdapter: settingsUpdaterAdapter{pool, nil}, live: b}}
			query := newFakeContextUsageQuerier(true)
			usage := newContextUsageResolver(t.Context(), func(string) (contextUsageQuerier, conversations.ConversationID, bool) { return query, "conv", true }, nil, nil)
			usage.live = b
			if name == "model" {
				b.sink.live.acceptEvent(src, list)
			}
			var enrichment atomic.Int32
			updater := &testDaemonSettingsUpdater{liveSettingsUpdater: liveSettingsUpdater{settingsUpdaterAdapter: settingsUpdaterAdapter{pool, nil}, live: b}}
			wiring.settings = updater
			w, _ := testDaemonRelayWire(t, b, wiring, usage, func(cfg *relay.V2SessionConfig) {
				cfg.EffectiveEffortFor = func(context.Context, string) (*string, bool) { enrichment.Add(1); v := "low"; return &v, true }
			})
			w.open("phone", name == "settings", protocol.CapabilityThread)
			if name == "model" {
				testDaemonWireReading(t, w, protocol.TypeModelList, 0)
			}
			typ, want := protocol.TypeRequestContextUsage, protocol.TypeContextUsage
			var request any = protocol.RequestContextUsagePayload{ConversationID: "conv"}
			switch name {
			case "mcp":
				typ, want, request = protocol.TypeMCPStatusRequest, protocol.TypeMCPStatus, protocol.MCPStatusRequestPayload{ConversationID: "conv"}
			case "model":
				typ, want, request = protocol.TypeRequestModelList, protocol.TypeModelList, protocol.RequestModelListPayload{ConversationID: "conv"}
			case "settings":
				typ, want, request = protocol.TypeRequestSessionSettings, protocol.TypeSessionSettings, protocol.RequestSessionSettingsPayload{ConversationID: "conv"}
			case "update":
				empty := ""
				typ, want, request = protocol.TypeSetSessionSettings, protocol.TypeSessionSettingsUpdated, protocol.SetSessionSettingsPayload{SessionID: sid, Model: &empty}
			}
			for _, id := range []uint64{61, 62} {
				testDaemonWireRequest(t, w, id, typ, request)
				e := testDaemonWireReading(t, w, want, id)
				if string(e.SessionID) != `"`+sid+`"` {
					t.Fatalf("%s source lost: %s", name, e.SessionID)
				}
			}
			if name == "update" && updater.calls.Load() != 2 {
				t.Fatal("request repeated settings mutation")
			}
			if name == "settings" && enrichment.Load() != 2 {
				t.Fatal("settings enrichment repeated")
			}
			if name == "context" && query.calls.Load() != 1 {
				t.Fatal("collapsed request repeated query")
			}
			if name == "mcp" && mcps.callCount(sessions.SessionID(sid)) != 2 {
				t.Fatal("request duplicated MCP query")
			}
			if name == "model" {
				// The actual cached provider returns predecessor evidence after same-ID transition.
				b.sink.live.transition(sessions.SessionTransition{ConversationID: "conv", PreviousID: sessions.SessionID(sid), NewID: sessions.SessionID(sid), NextAgent: "claude"})
				r, ok := wiring.modelListReading("conv", false)
				if !ok || r.SessionGeneration != src.SessionGeneration {
					t.Fatal("valid stale provider result became unavailable or successor")
				}
				retained := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeModelList, ""}]
				if !retained.Envelope.SessionStateCleared {
					t.Fatal("cached predecessor retained")
				}
			}
		})
	}
}

func TestDaemonLiveRelayRetiredSnapshot(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	b.sink.live.acceptEvent(b.capture("conv", "a", false), turnevent.ModelAnnounced{Model: "removed"})
	w, mgr := testDaemonRelayWire(t, b, relayWiring{}, nil, func(cfg *relay.V2SessionConfig) {
		snapshot := cfg.ThreadLiveState
		cfg.ThreadLiveState = func() func() (relay.LiveState, bool) { cursor := snapshot(); b.sink.remove("conv"); return cursor }
	})
	w.open("phone", false, protocol.CapabilityThread)
	if err := mgr.Push(t.Context(), "phone", protocol.Envelope{ID: 130, Type: protocol.TypeError, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	for {
		_, e := w.read()
		if e.Type == protocol.TypeModelAnnounced {
			t.Fatal("removed detached snapshot published")
		}
		if e.Type == protocol.TypeError {
			break
		}
	}
}

type testDaemonSettingsUpdater struct {
	liveSettingsUpdater
	calls atomic.Int32
}

func (u *testDaemonSettingsUpdater) UpdateLive(id string, update relay.SettingsUpdate, e protocol.Envelope) (daemonLiveReading, error) {
	u.calls.Add(1)
	return u.liveSettingsUpdater.UpdateLive(id, update, e)
}

func TestDaemonLiveRelayInventory(t *testing.T) {
	b, pool, runner := testInventoryIngress(t, true)
	sid := string(pool.BootstrapID())
	parser := streamsup.NewParser(runner.models.Sink, discardLogger())
	// Bootstrap's factory captured ingress before installation; its detached source survives forwarding.
	if _, err := parser.Write(testInventoryLine(protocol.TypeSlashCommandList, "before")); err != nil {
		t.Fatal(err)
	}
	w, _ := testDaemonRelayWire(t, b, relayWiring{}, nil, nil)
	w.open("phone", false, protocol.CapabilityThread)
	e := testDaemonWireReading(t, w, protocol.TypeSlashCommandList, 0)
	if string(e.SessionID) != `"`+sid+`"` || !bytes.Contains(e.Payload, []byte("before")) {
		t.Fatal("bootstrap snapshot source lost")
	}
	if _, err := parser.Write(testInventoryLine(protocol.TypeSlashCommandList, "after")); err != nil {
		t.Fatal(err)
	}
	e = testDaemonWireReading(t, w, protocol.TypeSlashCommandList, 0)
	if string(e.SessionID) != `"`+sid+`"` || !bytes.Contains(e.Payload, []byte("after")) {
		t.Fatal("inventory push source lost")
	}
}

func TestDaemonLiveRelayConnectOvertaken(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	send := b.sink.sinkForSessionTag(newStreamSessionTag("a"), "claude")
	send(turnevent.ModelAnnounced{Model: "snapshot"})
	w, _ := testDaemonRelayWire(t, b, relayWiring{}, nil, func(cfg *relay.V2SessionConfig) {
		snapshot := cfg.ThreadLiveState
		cfg.ThreadLiveState = func() func() (relay.LiveState, bool) {
			cursor := snapshot()
			send(turnevent.ModelAnnounced{Model: "fresh"})
			return cursor
		}
	})
	w.open("phone", false, protocol.CapabilityThread)
	e := testDaemonWireReading(t, w, protocol.TypeModelAnnounced, 0)
	if !bytes.Contains(e.Payload, []byte("fresh")) {
		t.Fatal("captured snapshot overtook connect/live update")
	}
}

func TestDaemonLiveRelayReservedQuerySnapshot(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	src := b.capture("conv", "a", false)
	b.sink.live.acceptEvent(src, turnevent.ContextUsage{Model: "remembered", TotalTokens: 20, MaxTokens: 100})
	w, _ := testDaemonRelayWire(t, b, relayWiring{}, nil, func(cfg *relay.V2SessionConfig) {
		snapshot := cfg.ThreadLiveState
		cfg.ThreadLiveState = func() func() (relay.LiveState, bool) {
			cursor := snapshot()
			// A query reservation that refuses supplies no accepted replacement reading.
			_ = b.sink.live.begin(src, protocol.TypeContextUsage, "")
			return cursor
		}
	})
	w.open("phone", false, protocol.CapabilityThread)
	e := testDaemonWireReading(t, w, protocol.TypeContextUsage, 0)
	if !bytes.Contains(e.Payload, []byte("remembered")) {
		t.Fatal("failed query reservation blanked retained state")
	}
}
