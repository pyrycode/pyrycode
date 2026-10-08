package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

func awaitSwitchPublication(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("switch publication did not complete")
	}
}

type delayedTransitionBroadcaster struct {
	*relay.V2SessionManager
	entered, release chan struct{}
	delayed          atomic.Bool
}

func (b *delayedTransitionBroadcaster) ActiveConns(ctx context.Context) []relay.ActiveConn {
	if b.delayed.CompareAndSwap(false, true) {
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	}
	return b.V2SessionManager.ActiveConns(ctx)
}

func TestRelayAgentSwitchDelayedPublication(t *testing.T) {
	pool, reg, sw := relaySwitchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	paired := &devices.Registry{}
	paired.Add(devices.Device{TokenHash: devices.HashToken("observer-token"), Name: "phone", PairedAt: time.Now().UTC()})
	w := &switchWire{t: t, frames: make(chan protocol.RoutingEnvelope, 16), out: make(chan protocol.RoutingEnvelope, 128), phones: map[string]switchPhone{}, pub: key.PublicKey().Bytes()}
	var down atomic.Bool
	held := make(chan struct{}, 1)
	reconnect := make(chan struct{}, 1)
	agent := func(id string) (string, bool) {
		h, err := pool.HarnessFor(sessions.SessionID(id))
		return h, err == nil
	}
	mgr, err := relay.NewV2SessionManager(relay.V2SessionConfig{
		Frames: w.frames, Outbound: func(e protocol.RoutingEnvelope) error { w.out <- e; return nil },
		StaticPriv: key.Bytes(), Devices: paired, ServerID: string(identity.NewServerID()), Logger: discardLogger(),
		CodexConversation: codexConversation(reg, agent), ConversationAgent: conversationAgent(reg, agent),
		Reconnect: reconnect, Connected: func() bool {
			if down.Load() {
				select {
				case held <- struct{}{}:
				default:
				}
				return false
			}
			return true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sw.resetting.attach(mgr)
	entered, release := make(chan struct{}), make(chan struct{})
	store := history.New(t.TempDir())
	announced := make(chan struct{}, 2)
	announce := newConversationUpdateEmitterV2(mgr, ctx, discardLogger()).announce
	bcast := &delayedTransitionBroadcaster{V2SessionManager: mgr, entered: entered, release: release}
	stop := startSessionTransitionStreamV2WithHarness(ctx, pool, bcast, func(id string) (string, bool) {
		return conversationForSession(reg, id)
	}, sessionHarness(pool), nil, store, discardLogger(), func(id string) {
		announceSwitchedConversation(reg, id, announce)
		announced <- struct{}{}
	})
	done := make(chan struct{})
	go func() { defer close(done); _ = mgr.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done; stop() })
	runPoolReady(t, pool)
	w.open("observer", true)
	adapter := relayAgentSwitcher{switcher: sw}
	result := make(chan relay.AgentSwitchOutcome, 1)
	go func() {
		result <- adapter.SwitchAgent(ctx, protocol.SwitchAgentPayload{ConversationID: switchConvID, Agent: "codex", Model: "luna"})
	}()
	awaitSwitchPublication(t, entered)
	checkExcluded := func() {
		t.Helper()
		got := adapter.SwitchAgent(ctx, protocol.SwitchAgentPayload{ConversationID: switchConvID, Agent: "claude", Model: "opus"})
		if got.State != relay.AgentSwitchRefused || got.Failure != relay.AgentSwitchBusy {
			t.Fatalf("delayed publication allowed next switch: %+v", got)
		}
		if releaseReset, ok := sw.reset.begin(switchConvID); ok {
			releaseReset()
			t.Fatal("ordinary reset entered before switch delivery")
		}
		select {
		case got := <-result:
			t.Fatalf("switch returned before publication: %+v", got)
		default:
		}
	}
	checkExcluded()
	for i := 0; i < 3; i++ {
		_, e := w.read()
		if e.Type != protocol.TypeResetting {
			t.Fatalf("early outcome: %+v", e)
		}
	}
	// Delay sealing separately from the transition consumer. Enqueue completion
	// alone must not let agentTaggedForConn read a subsequent Claude binding.
	down.Store(true)
	close(release)
	select {
	case <-announced:
	case <-time.After(3 * time.Second):
		t.Fatal("row not queued")
	}
	awaitSwitchPublication(t, held)
	checkExcluded()
	down.Store(false)
	reconnect <- struct{}{}
	select {
	case got := <-result:
		if got.State != relay.AgentSwitchCommitted {
			t.Fatalf("outcome=%+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("committed publication stalled")
	}
	row, _ := reg.Get(switchConvID)
	codexID := row.CurrentSessionID
	// Commit the return switch before decrypting the first outcome. Its frames
	// must already have been sealed with the first committed row's Codex agent.
	if got := adapter.SwitchAgent(ctx, protocol.SwitchAgentPayload{ConversationID: switchConvID, Agent: "claude", Model: "opus"}); got.State != relay.AgentSwitchCommitted {
		t.Fatalf("return outcome=%+v", got)
	}
	for i, typ := range []string{protocol.TypeSessionTransition, protocol.TypeConversationUpdated, protocol.TypeResetting, protocol.TypeResetting, protocol.TypeResetting, protocol.TypeSessionTransition, protocol.TypeConversationUpdated} {
		_, e := w.read()
		if e.Type != typ {
			t.Fatalf("frame %d=%s, want %s", i, e.Type, typ)
		}
		if typ == protocol.TypeConversationUpdated {
			var p protocol.ConversationUpdatedPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			want := "codex"
			if i == 6 {
				want = "claude"
			}
			if p.Agent != want {
				t.Fatalf("frame %d agent=%s, want %s", i, p.Agent, want)
			}
		}
		if i == 0 {
			p := decodeSessionTransition(t, e)
			if p.NewSessionID != codexID || p.PreviousSessionID != dormantWriteTargetID {
				t.Fatalf("wrong committed delimiter: %+v", p)
			}
		}
	}
	page, err := store.Page(switchConvID, "", 100)
	if err != nil || len(page.Entries) != 2 {
		t.Fatalf("history cardinality=%d, err=%v", len(page.Entries), err)
	}
	current, _ := reg.Get(switchConvID)
	for i, want := range []history.SessionProvenance{{Kind: "claude", SessionID: current.CurrentSessionID}, {Kind: "codex", SessionID: codexID}} {
		if got := page.Entries[i].Session; got == nil || *got != want {
			t.Fatalf("committed switch provenance=%+v, want %+v", got, want)
		}
	}
}

func TestRelayAgentSwitchPublicationQueuePressure(t *testing.T) {
	pool, reg, sw := relaySwitchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bcast := mixedSnapshot()
	e := newSessionTransitionEmitterV2(bcast, func(id string) (string, bool) { return conversationForSession(reg, id) }, discardLogger())
	e.hist = history.New(t.TempDir())
	e.switched = func(id string) {
		announceSwitchedConversation(reg, id, newConversationUpdateEmitterV2(bcast, ctx, discardLogger()).announce)
	}
	pool.SetTransitionObserver(e.Enqueue)
	entered := make(chan struct{})
	// Exercise the production suggestion wrapper's forwarding of the reliable
	// publisher, while the ordinary observer retains its nonblocking contract.
	suggestionTransitionSink{inner: pool}.SetSwitchTransitionPublisher(func(tr sessions.SessionTransition) {
		close(entered)
		e.publishSwitch(ctx, tr)
	})
	for i := 0; i < sessionTransitionQueueSize; i++ {
		e.Enqueue(sessions.SessionTransition{Reason: sessions.ReasonClear, NewID: "unresolved"})
	}
	runPoolReady(t, pool)
	result := make(chan relay.AgentSwitchOutcome, 1)
	go func() {
		result <- (relayAgentSwitcher{switcher: sw}).SwitchAgent(ctx, protocol.SwitchAgentPayload{ConversationID: switchConvID, Agent: "codex", Model: "luna"})
	}()
	awaitSwitchPublication(t, entered)
	select {
	case got := <-result:
		t.Fatalf("full transition queue lost committed outcome: %+v", got)
	default:
	}
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case got := <-result:
		if got.State != relay.AgentSwitchCommitted {
			t.Fatalf("outcome=%+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queue pressure stranded switch")
	}
	cancel()
	awaitSwitchPublication(t, done)
	if len(bcast.pushes) != 2 || bcast.pushes[0].env.Type != protocol.TypeSessionTransition || bcast.pushes[1].env.Type != protocol.TypeConversationUpdated {
		t.Fatalf("missing ordered outcome: %+v", bcast.pushes)
	}
	page, err := e.hist.Page(switchConvID, "", 100)
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("history cardinality=%d, err=%v", len(page.Entries), err)
	}
}

func TestSessionTransitionSwitchPublicationCancellation(t *testing.T) {
	for _, phase := range []string{"enqueue", "completion"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e := newSessionTransitionEmitterV2(mixedSnapshot(), constResolver(switchConvID, true), discardLogger())
			done := make(chan struct{})
			go func() {
				defer close(done)
				e.publishSwitch(ctx, sessions.SessionTransition{AgentSwitch: true})
			}()
			if phase == "completion" {
				select {
				case <-e.switches: // hold the completion acknowledgement
				case <-time.After(time.Second):
					t.Fatal("publication not handed off")
				}
			}
			// No receiver in enqueue, and no acknowledgement in completion:
			// cancellation is the only ready release path in both cases.
			cancel()
			awaitSwitchPublication(t, done)
		})
	}
}
