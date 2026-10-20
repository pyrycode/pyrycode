package main

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testTurnProgressPhase(t *testing.T, e protocol.Envelope) string {
	t.Helper()
	if e.Type != protocol.TypeTurnState || e.SessionStateCleared {
		return ""
	}
	var p protocol.TurnStatePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.ConversationID != "conv" || string(e.SessionID) != `"a"` {
		t.Fatal("turn reading lost conversation or producing session")
	}
	return p.State
}

func testTurnProgressWait(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("turn progress barrier timed out")
	}
}

func testTurnProgressBarrier(t *testing.T, w *switchWire, mgr *relay.V2SessionManager) []protocol.Envelope {
	t.Helper()
	if err := mgr.FlushPushes(t.Context()); err != nil {
		t.Fatal(err)
	}
	marker := protocol.Envelope{ID: 3711, Type: protocol.TypeError, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)}
	if err := mgr.Push(t.Context(), "phone", marker); err != nil {
		t.Fatal(err)
	}
	var readings []protocol.Envelope
	for {
		id, e := w.read()
		if id != "phone" {
			t.Fatal("unexpected publication recipient")
		}
		if e.Type == marker.Type && e.ID == marker.ID {
			return readings
		}
		readings = append(readings, e)
	}
}

func TestDaemonLiveQueuedTurnProgress(t *testing.T) {
	for _, event := range []struct {
		phase string
		event turnevent.Event
	}{{"responding", turnevent.TextChunk{Text: "fixture"}}, {"thinking", turnevent.ThoughtChunk{Text: "fixture"}}} {
		for _, boundary := range []string{"immediate", "publisher", "relay"} {
			for _, retirement := range []string{"none", "reuse", "recreate"} {
				if boundary == "immediate" && retirement != "none" {
					continue
				}
				t.Run(event.phase+"/"+boundary+"/"+retirement, func(t *testing.T) {
					b := testControlBindings(t, "conv", "a")
					o := b.sink.live
					// Capture before installation so the hold selects progress, not an initial clear.
					src := o.capture("a", 1, history.SessionProvenance{Kind: "claude", SessionID: "a"}, true)
					entered, release := make(chan struct{}), make(chan struct{})
					unblock := sync.OnceFunc(func() { close(release) })
					var once sync.Once
					hold := func(ctx context.Context) {
						once.Do(func() {
							close(entered)
							select {
							case <-release:
							case <-ctx.Done():
							}
						})
					}
					published := make(chan relay.LiveState, 64)
					w, mgr := testDaemonRelayWire(t, b, relayWiring{}, nil, func(cfg *relay.V2SessionConfig) {
						current := cfg.LiveStateCurrent
						cfg.LiveStateCurrent = func(r relay.LiveState) bool {
							if boundary == "relay" && testTurnProgressPhase(t, r.Envelope) == event.phase {
								hold(t.Context())
							}
							return current(r)
						}
					}, func(ctx context.Context, mgr *relay.V2SessionManager) func() {
						return startDaemonLivePublisher(ctx, o, func(ctx context.Context) []relay.ActiveConn {
							if boundary == "publisher" {
								hold(ctx)
							}
							return mgr.ActiveConns(ctx)
						}, func(ctx context.Context, id string, r relay.LiveState) error {
							err := mgr.PushLiveState(ctx, id, r)
							published <- r
							return err
						}, func(ctx context.Context, id string) { _ = mgr.RetireLiveState(ctx, id) }, discardLogger())
					})
					defer unblock()
					w.open("phone", false, protocol.CapabilityThread)
					if err := testDaemonRelayAdmission(t.Context(), mgr, "phone"); err != nil {
						t.Fatal(err)
					}
					o.acceptEvent(src, event.event)
					var phases []string
					if boundary == "immediate" {
						e := testDaemonWireReading(t, w, protocol.TypeTurnState, 0)
						phases = append(phases, testTurnProgressPhase(t, e))
					} else {
						testTurnProgressWait(t, entered)
					}
					o.acceptEvent(src, turnevent.TurnEnd{})
					fresh := src
					switch retirement {
					case "reuse":
						o.transition(sessions.SessionTransition{ConversationID: "conv", PreviousID: "a", NewID: "a", NextAgent: "claude"})
						fresh = o.capture("a", 2, history.SessionProvenance{Kind: "claude", SessionID: "a"}, true)
					case "recreate":
						b.sink.remove(conversations.ConversationID("conv"))
						fresh = o.capture("a", 2, history.SessionProvenance{Kind: "claude", SessionID: "a"}, true)
					}
					if retirement != "none" {
						if fresh.SessionGeneration <= src.SessionGeneration {
							t.Fatal("retirement did not advance generation")
						}
						o.acceptEvent(src, event.event) // A late predecessor cannot re-admit progress.
						o.acceptEvent(fresh, turnevent.TurnEnd{})
					}
					unblock()
					var callbacks []string
					for {
						select {
						case r := <-published:
							phase := testTurnProgressPhase(t, r.Envelope)
							if phase != "" {
								callbacks = append(callbacks, phase)
							}
							if phase == "idle" && r.SessionGeneration == fresh.SessionGeneration {
								goto drained
							}
						case <-time.After(3 * time.Second):
							t.Fatal("publisher did not enqueue terminal idle")
						}
					}
				drained:
					cleared := boundary == "immediate"
					for _, e := range testTurnProgressBarrier(t, w, mgr) {
						if e.Type == protocol.TypeTurnState && e.SessionStateCleared {
							cleared = true
						}
						if phase := testTurnProgressPhase(t, e); phase != "" {
							if !cleared {
								t.Fatal("reading preceded generation clear")
							}
							phases = append(phases, phase)
						}
					}
					want := []string{event.phase, "idle"}
					if retirement != "none" {
						want = []string{"idle"}
					}
					if !reflect.DeepEqual(phases, want) {
						t.Fatalf("decrypted phases %v, want %v", phases, want)
					}
					if retirement == "none" || boundary == "publisher" {
						if !reflect.DeepEqual(callbacks, want) {
							t.Fatalf("publisher callback phases %v, want %v", callbacks, want)
						}
					}
					cursor := daemonRelaySnapshot(o)()
					foundIdle := false
					for r, ok := cursor(); ok; r, ok = cursor() {
						if phase := testTurnProgressPhase(t, r.Envelope); phase != "" && phase != "idle" {
							t.Fatal("fresh snapshot replayed transient progress")
						} else if phase == "idle" {
							foundIdle = true
							if r.SessionGeneration != fresh.SessionGeneration {
								t.Fatal("snapshot retained retired generation")
							}
						}
					}
					if !foundIdle {
						t.Fatal("latest idle missing from fresh snapshot")
					}
				})
			}
		}
	}
}

func TestDaemonLiveTurnProgressWatermark(t *testing.T) {
	b := testControlBindings(t, "conv", "a")
	o := b.sink.live
	src := o.capture("a", 1, history.SessionProvenance{Kind: "claude", SessionID: "a"}, true)
	w, mgr := testDaemonRelayWire(t, b, relayWiring{}, nil, nil)
	w.open("phone", false, protocol.CapabilityThread)
	if err := testDaemonRelayAdmission(t.Context(), mgr, "phone"); err != nil {
		t.Fatal(err)
	}
	cap := o.acceptEvent(src, turnevent.TextChunk{Text: "fixture"})
	progress := daemonRelayReading(cap.updates[0])
	if phase := testTurnProgressPhase(t, testDaemonWireReading(t, w, protocol.TypeTurnState, 0)); phase != "responding" {
		t.Fatal("missing initial progress")
	}
	// Duplicate the consumed revision before idle, then replay it after newer delivery.
	if err := mgr.PushLiveState(t.Context(), "phone", progress); err != nil {
		t.Fatal(err)
	}
	if got := testTurnProgressBarrier(t, w, mgr); len(got) != 0 {
		t.Fatal("duplicate progress delivered")
	}
	o.acceptEvent(src, turnevent.TurnEnd{})
	if phase := testTurnProgressPhase(t, testDaemonWireReading(t, w, protocol.TypeTurnState, 0)); phase != "idle" {
		t.Fatal("missing terminal idle")
	}
	if err := mgr.PushLiveState(t.Context(), "phone", progress); err != nil {
		t.Fatal(err)
	}
	if got := testTurnProgressBarrier(t, w, mgr); len(got) != 0 {
		t.Fatal("older progress rolled recipient back after idle")
	}
}

func TestDaemonLiveTurnProgressFreshness(t *testing.T) {
	o := testLiveOwner()
	src := o.capture("a", 1, history.SessionProvenance{Kind: "claude", SessionID: "a"}, true)
	for _, tc := range []struct {
		name, typ, payload string
		want               bool
	}{
		{"thinking", protocol.TypeTurnState, `{"state":"thinking"}`, true},
		{"responding", protocol.TypeTurnState, `{"state":"responding"}`, true},
		{"idle", protocol.TypeTurnState, `{"state":"idle"}`, false},
		{"unknown", protocol.TypeTurnState, `{"state":"future"}`, false},
		{"wrong_shape", protocol.TypeTurnState, `{"state":1}`, false},
		{"inventory", protocol.TypeModelList, `{"models":[]}`, false},
		{"other_family", protocol.TypeModelAnnounced, `{"model":"old"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, ok := o.admit(src, protocol.Envelope{Type: tc.typ, Payload: json.RawMessage(tc.payload)}, "")
			if !ok {
				t.Fatal("fixture admission failed")
			}
			if _, ok := o.admit(src, protocol.Envelope{Type: tc.typ, Payload: json.RawMessage(`{}`)}, ""); !ok {
				t.Fatal("replacement admission failed")
			}
			if got := daemonLiveCurrent(o, daemonRelayReading(r)); got != tc.want {
				t.Fatalf("superseded delivery eligibility %t, want %t", got, tc.want)
			}
			for _, revision := range []uint64{0, r.Revision + 100} {
				r.Revision = revision
				if daemonLiveCurrent(o, daemonRelayReading(r)) {
					t.Fatal("zero or future revision admitted")
				}
			}
		})
	}
}
