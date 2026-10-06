package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

type switchPhone struct{ send, recv *noise.CipherState }
type switchWire struct {
	t           *testing.T
	frames, out chan protocol.RoutingEnvelope
	phones      map[string]switchPhone
	pub         []byte
}

func (w *switchWire) wrap(id, typ string, data []byte) protocol.RoutingEnvelope {
	w.t.Helper()
	raw, err := json.Marshal(protocol.InnerFrameV2{Version: protocol.V2Version, Type: typ, Data: base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		w.t.Fatal(err)
	}
	return protocol.RoutingEnvelope{ConnID: id, Frame: raw}
}
func (w *switchWire) await() protocol.RoutingEnvelope {
	w.t.Helper()
	select {
	case e := <-w.out:
		return e
	case <-time.After(3 * time.Second):
		w.t.Fatal("missing switch frame")
		return protocol.RoutingEnvelope{}
	}
}
func (w *switchWire) data(e protocol.RoutingEnvelope) []byte {
	w.t.Helper()
	var inner protocol.InnerFrameV2
	if err := json.Unmarshal(e.Frame, &inner); err != nil {
		w.t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		w.t.Fatal(err)
	}
	return data
}
func (w *switchWire) open(id string, capable bool) {
	w.t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		w.t.Fatal(err)
	}
	init, err := noise.NewInitiator(key.Bytes(), w.pub)
	if err != nil {
		w.t.Fatal(err)
	}
	caps := []string{protocol.CapabilityInteractive}
	if capable {
		caps = append(caps, protocol.CapabilityMultiAgent)
	}
	payload, _ := json.Marshal(protocol.HelloClientPayload{Role: "client", DeviceName: "phone", ClientVersion: "test", ProtocolVersions: []string{"v2"}, Token: id + "-token", Capabilities: caps})
	raw, _ := json.Marshal(protocol.Envelope{ID: 1, Type: protocol.TypeHello, TS: time.Now().UTC(), Payload: payload})
	data, err := init.WriteInit(raw)
	if err != nil {
		w.t.Fatal(err)
	}
	w.frames <- w.wrap(id, protocol.TypeNoiseInit, data)
	response := w.await()
	if response.ConnID != id {
		w.t.Fatalf("unexpected handshake frame: %+v", response)
	}
	_, send, recv, err := init.ReadResp(w.data(response))
	if err != nil {
		w.t.Fatal(err)
	}
	w.phones[id] = switchPhone{send, recv}
}
func (w *switchWire) request(id string, p protocol.SwitchAgentPayload) {
	w.t.Helper()
	payload, _ := json.Marshal(p)
	raw, _ := json.Marshal(protocol.Envelope{ID: 2871, Type: protocol.TypeSwitchAgent, TS: time.Now().UTC(), Payload: payload})
	data, err := w.phones[id].send.Encrypt(raw)
	if err != nil {
		w.t.Fatal(err)
	}
	w.frames <- w.wrap(id, protocol.TypeNoiseMsg, data)
}
func (w *switchWire) read() (string, protocol.Envelope) {
	w.t.Helper()
	e := w.await()
	plain, err := w.phones[e.ConnID].recv.Decrypt(w.data(e))
	if err != nil {
		w.t.Fatal(err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(plain, &env); err != nil {
		w.t.Fatal(err)
	}
	return e.ConnID, env
}

type gatedRelaySwitch struct {
	inner   relay.AgentSwitcher
	entered chan context.Context
	release chan struct{}
}

func (g gatedRelaySwitch) SwitchAgent(ctx context.Context, p protocol.SwitchAgentPayload) relay.AgentSwitchOutcome {
	select {
	case g.entered <- ctx:
	case <-ctx.Done():
		return relay.AgentSwitchOutcome{}
	}
	select {
	case <-g.release:
	case <-ctx.Done():
		return relay.AgentSwitchOutcome{}
	}
	return g.inner.SwitchAgent(ctx, p)
}

func TestRelayAgentSwitchEncryptedFrames(t *testing.T) {
	pool, reg, sw := relaySwitchFixture(t)
	// The observer is installed before Run, as in production.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	paired := &devices.Registry{}
	for _, id := range []string{"requester", "observer", "old"} {
		paired.Add(devices.Device{TokenHash: devices.HashToken(id + "-token"), Name: "phone", PairedAt: time.Now().UTC()})
	}
	w := &switchWire{t: t, frames: make(chan protocol.RoutingEnvelope, 16), out: make(chan protocol.RoutingEnvelope, 128), phones: map[string]switchPhone{}, pub: key.PublicKey().Bytes()}
	capture := &questionLogCapture{completed: make(chan struct{}, 1)}
	logger := slog.New(slog.NewTextHandler(capture, &slog.HandlerOptions{Level: slog.LevelDebug}))
	adapter := &relayAgentSwitcher{switcher: sw}
	gate := gatedRelaySwitch{inner: adapter, entered: make(chan context.Context, 1), release: make(chan struct{}, 1)}
	mgr, err := relay.NewV2SessionManager(relay.V2SessionConfig{Frames: w.frames, Outbound: func(e protocol.RoutingEnvelope) error { w.out <- e; return nil }, StaticPriv: key.Bytes(), Devices: paired, ServerID: string(identity.NewServerID()), Logger: logger, AgentSwitcher: gate, CodexConversation: codexConversation(reg, func(id string) (string, bool) {
		h, err := pool.HarnessFor(sessions.SessionID(id))
		return h, err == nil
	}), ConversationAgent: conversationAgent(reg, func(id string) (string, bool) {
		h, err := pool.HarnessFor(sessions.SessionID(id))
		return h, err == nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	sw.resetting.attach(mgr)
	// The adapter and producer hold the same late-bound emitter.
	store := history.New(t.TempDir())
	announce := newConversationUpdateEmitterV2(mgr, ctx, logger).announce
	stopTransitions := startSessionTransitionStreamV2(ctx, pool, mgr, func(id string) (string, bool) { return conversationForSession(reg, id) }, nil, store, logger, func(id string) { announceSwitchedConversation(reg, id, announce) })
	done := make(chan struct{})
	go func() { defer close(done); _ = mgr.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done; stopTransitions() })
	runPoolReady(t, pool)
	w.open("requester", true)
	w.open("observer", true)
	w.open("old", false)
	oldID := dormantWriteTargetID
	for _, p := range []protocol.SwitchAgentPayload{
		{ConversationID: switchConvID, Agent: "codex", Model: "luna"},
		{ConversationID: switchConvID, Agent: "claude", Model: "opus", Effort: switchString("")},
	} {
		w.request("requester", p)
		<-gate.entered
		gate.release <- struct{}{}
		seen := map[string][]protocol.Envelope{}
		// Old peers may see the pre-commit rising edges, but Codex's result is withheld.
		for len(seen["requester"]) < 5 || len(seen["observer"]) < 5 {
			id, e := w.read()
			seen[id] = append(seen[id], e)
		}
		row, _ := reg.Get(switchConvID)
		for _, id := range []string{"requester", "observer"} {
			events := seen[id]
			want := []string{protocol.TypeResetting, protocol.TypeResetting, protocol.TypeResetting, protocol.TypeSessionTransition, protocol.TypeConversationUpdated}
			if len(events) != 5 {
				t.Fatalf("%s: extra outcome frames: %+v", id, events)
			}
			for i, e := range events {
				if e.Type != want[i] || e.InReplyTo != nil {
					t.Fatalf("%s event %d: %+v", id, i, e)
				}
			}
			for i, e := range events[:3] {
				var status protocol.ResettingPayload
				json.Unmarshal(e.Payload, &status)
				if status.ConversationID != switchConvID || status.Active != (i < 2) {
					t.Fatalf("status=%+v", status)
				}
				phase, handoff := "", ""
				if i == 0 {
					phase, handoff = protocol.ResetPhaseWrappingUp, protocol.ResetHandoffPending
				}
				if i == 1 {
					phase, handoff = protocol.ResetPhaseRestarting, protocol.ResetHandoffSkipped
				}
				if status.Phase != phase || status.Handoff != handoff {
					t.Fatalf("status=%+v", status)
				}
			}
			var tr protocol.SessionTransitionPayload
			json.Unmarshal(events[3].Payload, &tr)
			if tr.ConversationID != switchConvID || tr.PreviousSessionID != oldID || tr.NewSessionID != row.CurrentSessionID || tr.Reason != "clear" {
				t.Fatalf("transition=%+v", tr)
			}
			var updated protocol.ConversationUpdatedPayload
			json.Unmarshal(events[4].Payload, &updated)
			if updated.ID != switchConvID || updated.Agent != p.Agent {
				t.Fatalf("updated=%+v", updated)
			}
		}
		if p.Agent == "codex" {
			for _, e := range seen["old"] {
				if e.Type != protocol.TypeResetting {
					t.Fatalf("old peer got committed frame: %+v", e)
				}
			}
		}
		// A barrier push to the legacy peer drains any queued outcome before counting.
		if err := mgr.Push(ctx, "old", protocol.Envelope{ID: 900, Type: protocol.TypeWorkspaceUpdated, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		for {
			id, e := w.read()
			if id != "old" {
				t.Fatalf("extra switch frame: %s %+v", id, e)
			}
			if e.Type == protocol.TypeWorkspaceUpdated {
				break
			}
			seen[id] = append(seen[id], e)
		}
		if p.Agent == "codex" {
			for _, e := range seen["old"] {
				var s protocol.ResettingPayload
				json.Unmarshal(e.Payload, &s)
				if e.Type != protocol.TypeResetting || !s.Active {
					t.Fatalf("legacy post-commit frame: %+v", e)
				}
			}
		} else if len(seen["old"]) < 3 || seen["old"][len(seen["old"])-1].Type != protocol.TypeConversationUpdated {
			t.Fatalf("Claude delivery not restored: %+v", seen["old"])
		}
		settings, err := pool.SettingsFor(sessions.SessionID(row.CurrentSessionID))
		if err != nil {
			t.Fatal(err)
		}
		wantEffort := "high"
		if p.Agent == "claude" {
			wantEffort = ""
		}
		if settings.Model != p.Model || settings.Effort != wantEffort {
			t.Fatalf("settings=%+v", settings)
		}
		persisted, err := conversations.Load(sw.registryPath)
		if err != nil {
			t.Fatal(err)
		}
		saved, _ := persisted.Get(switchConvID)
		if saved.CurrentSessionID != row.CurrentSessionID {
			t.Fatal("unpersisted outcome")
		}
		oldID = row.CurrentSessionID
	}
	// Refusal is correlated and silent apart from its static error.
	w.request("requester", protocol.SwitchAgentPayload{ConversationID: switchConvID, Agent: "codex", Model: "ZZPRIVATEFAILUREMARKERZZ"})
	<-gate.entered
	gate.release <- struct{}{}
	id, e := w.read()
	var refusal protocol.ErrorPayload
	json.Unmarshal(e.Payload, &refusal)
	if id != "requester" || e.Type != protocol.TypeError || e.InReplyTo == nil || *e.InReplyTo != 2871 || refusal.Message != relay.MsgSettingsModelNotOffered || refusal.Retryable {
		t.Fatalf("refusal=%+v / %+v", e, refusal)
	}
	// Persistence fails after wrap-up: all status edges close, with no new row.
	path := sw.registryPath
	adapter.switcher.registryPath = filepath.Join(t.TempDir(), "ZZPRIVATEFAILUREMARKERZZ", "row.json")
	if err := os.WriteFile(filepath.Dir(adapter.switcher.registryPath), []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	w.request("requester", protocol.SwitchAgentPayload{ConversationID: switchConvID, Agent: "codex"})
	<-gate.entered
	gate.release <- struct{}{}
	counts := map[string]int{}
	for counts["requester"] < 4 || counts["observer"] < 3 || counts["old"] < 3 {
		id, e := w.read()
		counts[id]++
		if e.Type == protocol.TypeError {
			var failure protocol.ErrorPayload
			json.Unmarshal(e.Payload, &failure)
			if id != "requester" || failure.Code != protocol.CodeServerBinaryOffline || failure.Message != "agent switch unavailable" || !failure.Retryable || e.InReplyTo == nil || *e.InReplyTo != 2871 {
				t.Fatalf("failure=%+v / %+v", e, failure)
			}
		} else if e.Type != protocol.TypeResetting {
			t.Fatalf("failed switch published %+v", e)
		}
	}
	row, _ := reg.Get(switchConvID)
	if row.CurrentSessionID != oldID {
		t.Fatal("failure changed binding")
	}
	adapter.switcher.registryPath = path
	// A bootstrap-backed old row makes cleanup deterministically refuse removal.
	// A nonempty Switch ID must still publish its committed row, with no refusal.
	reg.Update(switchConvID, func(c *conversations.Conversation) { c.CurrentSessionID = string(pool.BootstrapID()) })
	oldID = string(pool.BootstrapID())
	// Requester-close is processed before a snapshot barrier and before switch work.
	w.request("requester", protocol.SwitchAgentPayload{ConversationID: switchConvID, Agent: "codex", Model: "luna"})
	switchCtx := <-gate.entered
	w.frames <- protocol.RoutingEnvelope{ConnID: "requester", CloseCode: 1000}
	for {
		live := mgr.ActiveConns(ctx)
		closed := true
		for _, c := range live {
			if c.ConnID == "requester" {
				closed = false
			}
		}
		if closed {
			break
		}
	}
	if switchCtx.Err() != nil {
		t.Fatal("requester cancelled daemon switch")
	}
	gate.release <- struct{}{}
	observer := 0
	for observer < 5 {
		id, e := w.read()
		if id == "requester" {
			t.Fatal("closed requester received outcome")
		}
		if id == "observer" {
			observer++
			want := []string{protocol.TypeResetting, protocol.TypeResetting, protocol.TypeResetting, protocol.TypeSessionTransition, protocol.TypeConversationUpdated}
			if e.Type != want[observer-1] {
				t.Fatalf("disconnect outcome=%+v", e)
			}
			if observer == 4 {
				var tr protocol.SessionTransitionPayload
				json.Unmarshal(e.Payload, &tr)
				if tr.PreviousSessionID != oldID || tr.Reason != "clear" {
					t.Fatalf("cleanup transition=%+v", tr)
				}
			}
			if observer == 5 {
				var updated protocol.ConversationUpdatedPayload
				json.Unmarshal(e.Payload, &updated)
				if updated.Agent != "codex" {
					t.Fatalf("cleanup row=%+v", updated)
				}
			}
		}
	}
	capture.mu.Lock()
	logs := capture.buf.String()
	capture.mu.Unlock()
	if strings.Contains(logs, "ZZPRIVATEFAILUREMARKERZZ") {
		t.Fatal("switch diagnostic leaked failure marker")
	}
	page, err := store.Page(switchConvID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range page.Entries {
		if e.Type == protocol.TypeSessionTransition {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("history transitions=%d", count)
	}
}

func TestRelayAgentSwitchProductionWiring(t *testing.T) {
	for source, fragments := range map[string][]string{
		formattedGoFunc(t, "main.go", "runSupervisor"): {"reset := newConversationReset(", "reset: reset,", "agentSwitcher: relayAgentSwitcher{", "history: conversationHistory, resetting: resetting, saved: modelVocabulary"},
		formattedGoFunc(t, "relay.go", "startRelayV2"): {"AgentSwitcher: w.agentSwitcher,", "announceSwitchedConversation(w.convReg, id, announceConversation)"},
	} {
		for _, fragment := range fragments {
			if !strings.Contains(source, fragment) {
				t.Errorf("production wiring lacks %q", fragment)
			}
		}
	}
}
