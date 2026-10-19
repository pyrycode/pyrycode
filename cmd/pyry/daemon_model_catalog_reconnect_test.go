package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
)

func testCatalogSnapshotFrame(t *testing.T, e protocol.Envelope, raw []byte, source string) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if e.InReplyTo != nil || e.EventID != nil || e.HistoryEntryID != nil || string(fields["session_id"]) != source {
		t.Fatalf("catalog snapshot metadata: %s, want producer %s", raw, source)
	}
	if e.SessionStateCleared && string(e.Payload) != `{}` {
		t.Fatalf("catalog clear payload: %s", raw)
	}
}

func testCatalogSnapshotRead(t *testing.T, w *switchWire, wants map[string]protocol.ModelListPayload, source string, wantClear bool) {
	t.Helper()
	clears, seen := map[string]int{}, map[string]bool{}
	for len(seen) < len(wants) {
		e, raw := testCatalogWireRead(t, w)
		t.Logf("snapshot frame: %s", raw)
		if e.Type != protocol.TypeModelList {
			continue
		}
		var conv string
		if err := json.Unmarshal(e.ConversationID, &conv); err != nil {
			t.Fatal(err)
		}
		want, ok := wants[conv]
		if !ok {
			t.Fatalf("catalog delivered to inaccessible/unexpected target %q", conv)
		}
		testCatalogSnapshotFrame(t, e, raw, source)
		if e.SessionStateCleared {
			clears[conv]++
			continue
		}
		if seen[conv] || (wantClear && clears[conv] != 1) || (!wantClear && clears[conv] != 0) {
			t.Fatalf("target %s: duplicate catalog or clears=%d", conv, clears[conv])
		}
		wantRaw, _ := json.Marshal(want)
		var got protocol.ModelListPayload
		if err := json.Unmarshal(e.Payload, &got); err != nil {
			t.Fatal(err)
		}
		gotRaw, _ := json.Marshal(got)
		if !bytes.Equal(gotRaw, wantRaw) {
			t.Fatalf("catalog %s = %s, want %s", conv, gotRaw, wantRaw)
		}
		seen[conv] = true
	}
}

func TestDaemonLiveRelayCatalogReconnect(t *testing.T) {
	for _, bootstrap := range []bool{false, true} {
		for _, multi := range []bool{false, true} {
			name := map[bool]string{false: "persisted", true: "bootstrap"}[bootstrap] + map[bool]string{false: "/basic", true: "/multi"}[multi]
			t.Run(name, func(t *testing.T) {
				b, pool, runner := testInventoryIngress(t, true)
				sid := string(pool.BootstrapID())
				b.reg.Create(conversations.Conversation{ID: "new"})
				b.reg.Create(conversations.Conversation{ID: conversations.ConversationID(sid), CurrentSessionID: "new"})
				saved := twoAgentVocabularyDouble{claude: sentinelModelList("persisted"), haveClaude: true, codex: sentinelCodexModels()}
				parser := streamsup.NewParser(runner.models.Sink, discardLogger())
				list, source := saved.claude, ""
				if bootstrap {
					if _, err := parser.Write(testInventoryLine(protocol.TypeModelList, "bootstrap")); err != nil {
						t.Fatal(err)
					}
					list, _, _ = runner.ModelListLive()
					source = `"` + sid + `"`
				}
				provider := daemonModelListReadingFor(b.reg, pool, saved, b)
				w, mgr := testDaemonRelayWire(t, b, relayWiring{convReg: b.reg, modelListReading: provider}, nil, func(cfg *relay.V2SessionConfig) {
					cfg.MergedModelOptions = pushedModelOptions(saved)
					cfg.RetainedModelLists = func(bool) []protocol.ModelListPayload { t.Error("thread used legacy reconciliation"); return nil }
				})
				wants := map[string]protocol.ModelListPayload{}
				for _, id := range []string{"conv", "new", sid} {
					p, _ := mapModelList(list, id)
					if multi {
						p.Models = pushedModelOptions(saved)(p.Models)
					}
					wants[id] = p
				}
				for connection := 0; connection < 2; connection++ {
					t.Logf("connection %d", connection)
					w.open("phone", multi, protocol.CapabilityThread)
					testCatalogSnapshotRead(t, w, wants, source, true)
					if connection == 0 {
						w.frames <- protocol.RoutingEnvelope{ConnID: "phone", CloseCode: 1000}
						ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
						for len(mgr.ActiveConns(ctx)) != 0 && ctx.Err() == nil {
						}
						cancel()
					}
				}
				if bootstrap {
					for _, id := range []string{"new", sid} {
						if r := testLiveReadings(t, b.sink.live, id)[liveReadingKey{protocol.TypeModelList, ""}]; r.Revision != 0 {
							t.Fatal("borrowed inventory retained in target session")
						}
					}
				}
				// A source replacement delivers a whole new catalog without another clear.
				before := testLiveReadings(t, b.sink.live, "conv")
				row, _ := b.reg.Get("new")
				if _, err := parser.Write(testInventoryLine(protocol.TypeModelList, "replacement")); err != nil {
					t.Fatal(err)
				}
				owner := testLiveReadings(t, b.sink.live, "conv")
				fresh, ok := provider("new", true)
				if !ok || !reflect.DeepEqual(owner, testLiveReadings(t, b.sink.live, "conv")) {
					t.Fatal("borrowing modified producing conversation")
				}
				afterRow, _ := b.reg.Get("new")
				if !reflect.DeepEqual(row, afterRow) || reflect.DeepEqual(before, owner) {
					t.Fatal("source replacement lost isolation or did not occur")
				}
				if err := mgr.PushLiveState(t.Context(), "phone", daemonRelayReading(fresh)); err != nil {
					t.Fatal(err)
				}
				// The owner also publishes its fresh catalog; consume both replacements.
				list, _, _ = runner.ModelListLive()
				for _, id := range []string{"conv", "new"} {
					p, _ := mapModelList(list, id)
					if multi {
						p.Models = pushedModelOptions(saved)(p.Models)
					}
					wants[id] = p
				}
				delete(wants, sid)
				testCatalogSnapshotRead(t, w, wants, `"`+sid+`"`, false)
			})
		}
	}
}

func TestDaemonLiveRelayCatalogSnapshotOvertaken(t *testing.T) {
	for _, event := range []string{"catalog", "transition", "recreate"} {
		for _, afterClear := range []bool{false, true} {
			t.Run(event+map[bool]string{false: "/snapshot", true: "/after_clear"}[afterClear], func(t *testing.T) {
				b, pool, runner := testInventoryIngress(t, true)
				sid := string(pool.BootstrapID())
				b.reg.Create(conversations.Conversation{ID: "new", CurrentSessionID: "target"})
				parser := streamsup.NewParser(runner.models.Sink, discardLogger())
				if _, err := parser.Write(testInventoryLine(protocol.TypeModelList, "captured")); err != nil {
					t.Fatal(err)
				}
				provider := daemonModelListReadingFor(b.reg, pool, nil, b)
				var held relay.LiveState
				mutate := func() {
					switch event {
					case "catalog":
						if _, err := parser.Write(testInventoryLine(protocol.TypeModelList, "newer")); err != nil {
							t.Fatal(err)
						}
						if _, ok := provider("new", true); !ok {
							t.Fatal("replacement unavailable")
						}
					case "transition":
						b.sink.queueBoundary(sessions.SessionTransition{ConversationID: "new", PreviousID: "target", NewID: "target", NextAgent: "claude"}, func() {}, nil)
					case "recreate":
						b.sink.remove("new")
						if !b.reg.Delete("new") {
							t.Fatal("target removal failed")
						}
						b.reg.Create(conversations.Conversation{ID: "new", CurrentSessionID: "target"})
						b.capture("new", "target", false)
					}
				}
				finished := make(chan struct{})
				w, mgr := testDaemonRelayWire(t, b, relayWiring{convReg: b.reg, modelListReading: provider}, nil, func(cfg *relay.V2SessionConfig) {
					snapshot := cfg.ThreadLiveState
					armed := false
					cfg.ThreadLiveState = func() func() (relay.LiveState, bool) {
						next := snapshot()
						return func() (relay.LiveState, bool) {
							r, ok := next()
							if !ok {
								close(finished)
								return r, ok
							}
							if r.ConversationID == "new" && r.Envelope.Type == protocol.TypeModelList {
								held = r
								if afterClear {
									armed = true
								} else {
									mutate()
								}
							}
							return r, ok
						}
					}
					out := cfg.Outbound
					cfg.Outbound = func(e protocol.RoutingEnvelope) error {
						err := out(e)
						if armed {
							armed = false
							mutate()
						}
						return err
					}
				}, func(context.Context, *relay.V2SessionManager) func() { return func() {} })
				w.open("phone", false, protocol.CapabilityThread)
				testAwaitInventory(t, finished)
				if held.Revision == 0 || daemonLiveCurrent(b.sink.live, held) {
					t.Fatal("captured catalog remained current")
				}
				marker := protocol.Envelope{ID: 901, Type: protocol.TypeError, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)}
				if err := mgr.Push(t.Context(), "phone", marker); err != nil {
					t.Fatal(err)
				}
				clears := 0
				for {
					e, raw := testCatalogWireRead(t, w)
					if e.ID == marker.ID {
						break
					}
					if e.Type == protocol.TypeModelList && string(e.ConversationID) == `"new"` {
						testCatalogSnapshotFrame(t, e, raw, `"`+sid+`"`)
						if !e.SessionStateCleared {
							t.Fatal("held snapshot restored stale catalog")
						}
						clears++
					}
				}
				if clears != map[bool]int{false: 0, true: 1}[afterClear] {
					t.Fatalf("clears=%d", clears)
				}
				// Resolve again only after the held capture was rejected; never retag it.
				fresh, ok := provider("new", true)
				if !ok || (event != "catalog" && fresh.SessionGeneration == held.SessionGeneration) {
					t.Fatal("fresh resolution lost current target ordering")
				}
				if err := mgr.PushLiveState(t.Context(), "phone", daemonRelayReading(fresh)); err != nil {
					t.Fatal(err)
				}
				list, evidence, _ := runner.ModelListLive()
				if evidence.ConversationID != "conv" {
					t.Fatal("borrowing rewrote producing evidence")
				}
				want, _ := mapModelList(list, "new")
				testCatalogSnapshotRead(t, w, map[string]protocol.ModelListPayload{"new": want}, `"`+sid+`"`, !afterClear || event != "catalog")
			})
		}
	}
}

func TestDaemonLiveRelayCatalogReconnectGates(t *testing.T) {
	for _, mode := range []string{"no_vocabulary", "noninteractive", "legacy", "access"} {
		t.Run(mode, func(t *testing.T) {
			b, pool, _ := testInventoryIngress(t, true)
			if mode != "legacy" {
				b.reg.Create(conversations.Conversation{ID: "hidden"})
				b.reg.Create(conversations.Conversation{ID: "unknown"})
			}
			saved := twoAgentVocabularyDouble{}
			if mode != "no_vocabulary" {
				saved.claude, saved.haveClaude = sentinelModelList("available"), true
			}
			provider := daemonModelListReadingFor(b.reg, pool, saved, b)
			finished := make(chan struct{})
			w, mgr := testDaemonRelayWire(t, b, relayWiring{convReg: b.reg, modelListReading: provider}, nil, func(cfg *relay.V2SessionConfig) {
				cfg.CodexConversation = func(id string) bool { return id == "hidden" }
				cfg.KnownConversation = func(id string) bool { return id == "conv" || id == "hidden" }
				cfg.RetainedModelLists = retainedModelLists(b.reg, pool, saved)
				// A legacy provider must not rescue a thread peer lacking vocabulary.
				if mode == "no_vocabulary" {
					cfg.RetainedModelLists = func(bool) []protocol.ModelListPayload { t.Error("thread fell back to legacy"); return nil }
				}
				snapshot := cfg.ThreadLiveState
				cfg.ThreadLiveState = func() func() (relay.LiveState, bool) {
					next := snapshot()
					return func() (relay.LiveState, bool) {
						r, ok := next()
						if !ok {
							close(finished)
						}
						return r, ok
					}
				}
			})
			caps := []string{protocol.CapabilityInteractive, protocol.CapabilityThread}
			if mode == "noninteractive" {
				caps = []string{protocol.CapabilityThread}
			}
			if mode == "legacy" {
				caps = []string{protocol.CapabilityInteractive}
			}
			w.openCapabilities("phone", caps)
			// A marker after snapshot exhaustion proves completion even with no inventory.
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if len(mgr.ActiveConns(ctx)) != 1 {
				t.Fatal("handshake did not complete")
			}
			if mode == "access" {
				want, _ := mapModelList(saved.claude, "conv")
				testCatalogSnapshotRead(t, w, map[string]protocol.ModelListPayload{"conv": want}, "", true)
			}
			if mode == "access" || mode == "no_vocabulary" {
				testAwaitInventory(t, finished)
			}
			// FlushPushes handles legacy reconcile; ThreadLiveState has its own finite drain.
			if err := mgr.FlushPushes(ctx); err != nil {
				t.Fatal(err)
			}
			if mode == "legacy" {
				want, _ := mapModelList(saved.claude, "conv")
				for {
					e, raw := testCatalogWireRead(t, w)
					if e.Type != protocol.TypeModelList {
						continue
					}
					wantRaw, _ := json.Marshal(want)
					if len(e.ConversationID) != 0 || len(e.SessionID) != 0 || e.SessionStateCleared || e.InReplyTo != nil || !bytes.Equal(e.Payload, wantRaw) {
						t.Fatalf("legacy reconcile changed: %s", raw)
					}
					break
				}
			}
			marker := protocol.Envelope{ID: 902, Type: protocol.TypeError, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)}
			if err := mgr.Push(ctx, "phone", marker); err != nil {
				t.Fatal(err)
			}
			for {
				e, raw := testCatalogWireRead(t, w)
				if e.ID == marker.ID {
					break
				}
				if e.Type == protocol.TypeModelList {
					t.Fatalf("withheld catalog leaked: %s", raw)
				}
			}
		})
	}
}

func TestDaemonLiveRelayCatalogSnapshotNoProducingSession(t *testing.T) {
	b := testControlBindings(t, "conv", "")
	src := b.capture("conv", "", true)
	want, _ := mapModelList(sentinelModelList("no-session"), "conv")
	provider := func(conv string, _ bool) (daemonLiveReading, bool) {
		_, ok, r := liveInventoryResult(b, conv, protocol.TypeModelList, protocol.Envelope{}, func(daemonLiveSource) (protocol.ModelListPayload, daemonLiveSource, bool) {
			return want, src, true
		})
		return r, ok
	}
	w, _ := testDaemonRelayWire(t, b, relayWiring{convReg: b.reg, modelListReading: provider}, nil, nil)
	w.open("phone", false, protocol.CapabilityThread)
	testCatalogSnapshotRead(t, w, map[string]protocol.ModelListPayload{"conv": want}, "null", true)
}

func TestDaemonLiveRelayCatalogReservedSnapshot(t *testing.T) {
	b, pool, runner := testInventoryIngress(t, true)
	b.reg.Create(conversations.Conversation{ID: "new"})
	parser := streamsup.NewParser(runner.models.Sink, discardLogger())
	if _, err := parser.Write(testInventoryLine(protocol.TypeModelList, "available")); err != nil {
		t.Fatal(err)
	}
	provider := daemonModelListReadingFor(b.reg, pool, nil, b)
	w, _ := testDaemonRelayWire(t, b, relayWiring{convReg: b.reg, modelListReading: provider}, nil, func(cfg *relay.V2SessionConfig) {
		snapshot := cfg.ThreadLiveState
		cfg.ThreadLiveState = func() func() (relay.LiveState, bool) {
			next := snapshot()
			return func() (relay.LiveState, bool) {
				r, ok := next()
				if ok && r.Envelope.Type == protocol.TypeModelList {
					// A refused reservation supplies no replacement catalog.
					b.sink.live.begin(b.capture(r.ConversationID, "", false), protocol.TypeModelList, "")
				}
				return r, ok
			}
		}
	})
	w.open("phone", false, protocol.CapabilityThread)
	list, _, _ := runner.ModelListLive()
	wants := map[string]protocol.ModelListPayload{}
	for _, conv := range []string{"conv", "new"} {
		wants[conv], _ = mapModelList(list, conv)
	}
	testCatalogSnapshotRead(t, w, wants, `"`+string(pool.BootstrapID())+`"`, true)
}
