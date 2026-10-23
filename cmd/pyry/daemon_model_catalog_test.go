package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testCatalogWireRequest(t *testing.T, w *switchWire, id uint64, conv string) {
	t.Helper()
	payload, err := json.Marshal(protocol.RequestModelListPayload{ConversationID: conv})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(protocol.Envelope{ID: id, Type: protocol.TypeRequestModelList, TS: time.Now().UTC(), Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := w.phones["phone"].send.Encrypt(raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("emitted request: %s", raw)
	w.frames <- w.wrap("phone", protocol.TypeNoiseMsg, sealed)
}

// Read authenticated plaintext before decoding so omission cannot collapse into null.
func testCatalogWireRead(t *testing.T, w *switchWire) (protocol.Envelope, []byte) {
	t.Helper()
	routed := w.await()
	if routed.ConnID != "phone" {
		t.Fatalf("unexpected recipient %s", routed.ConnID)
	}
	raw, err := w.phones[routed.ConnID].recv.Decrypt(w.data(routed))
	if err != nil {
		t.Fatal(err)
	}
	var e protocol.Envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	return e, raw
}

func testCatalogWireReply(t *testing.T, w *switchWire, request uint64, conv, source string, want protocol.ModelListPayload, clear bool) protocol.Envelope {
	t.Helper()
	clears := 0
	for {
		e, raw := testCatalogWireRead(t, w)
		if e.Type != protocol.TypeModelList || string(e.ConversationID) != `"`+conv+`"` {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if e.SessionStateCleared {
			if string(e.Payload) != `{}` || e.InReplyTo != nil || string(fields["session_id"]) != source {
				t.Fatalf("invalid catalog clear: %s", raw)
			}
			clears++
			t.Logf("emitted clear: %s", raw)
			continue
		}
		if e.InReplyTo == nil || *e.InReplyTo != request {
			continue
		}
		if (clear && clears != 1) || (!clear && clears != 0) {
			t.Fatalf("catalog clears = %d, want clear = %t", clears, clear)
		}
		if string(fields["session_id"]) != source {
			t.Fatalf("source tag = %s, want %s", fields["session_id"], source)
		}
		var got protocol.ModelListPayload
		if err := json.Unmarshal(e.Payload, &got); err != nil {
			t.Fatal(err)
		}
		wantRaw, _ := json.Marshal(want)
		var normalized protocol.ModelListPayload
		if err := json.Unmarshal(wantRaw, &normalized); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, normalized) {
			t.Fatalf("catalog = %+v, want %+v", got, normalized)
		}
		e.Payload, _ = json.Marshal(got)
		reencoded, err := json.Marshal(e)
		if err != nil || !bytes.Equal(e.Payload, wantRaw) || len(reencoded) > protocol.MaxThreadEnvelopeBytes {
			t.Fatalf("typed catalog round trip: %s (%v)", reencoded, err)
		}
		var roundtrip map[string]json.RawMessage
		if err := json.Unmarshal(reencoded, &roundtrip); err != nil || string(roundtrip["session_id"]) != source {
			t.Fatal("typed re-encoding changed source presence/value")
		}
		t.Logf("emitted reply: %s", raw)
		return e
	}
}

func TestDaemonLiveRelayRequestedCatalog(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(map[bool]string{false: "basic", true: "multi"}[multi], func(t *testing.T) {
			b, pool, runner := testInventoryIngress(t, true)
			sid := string(pool.BootstrapID())
			saved := twoAgentVocabularyDouble{claude: sentinelModelList("persisted"), haveClaude: true, codex: sentinelCodexModels()}
			provider := daemonModelListReadingFor(b.reg, pool, saved, b)
			captured := make(chan daemonLiveReading, 16)
			w, mgr := testDaemonRelayWire(t, b, relayWiring{modelListReading: func(conv string, multi bool) (daemonLiveReading, bool) {
				r, ok := provider(conv, multi)
				captured <- r
				return r, ok
			}}, nil, func(cfg *relay.V2SessionConfig) {
				cfg.MergedModelOptions = pushedModelOptions(saved)
				cfg.ModelListFor = modelListFor(b.reg, pool, saved, b)
			})
			w.open("phone", multi, protocol.CapabilityThread)
			request := func(id uint64, conv, source string, list turnevent.ModelList, clear bool) daemonLiveReading {
				t.Helper()
				testCatalogWireRequest(t, w, id, conv)
				var r daemonLiveReading
				select {
				case r = <-captured:
				case <-time.After(3 * time.Second):
					t.Fatal("model resolver was not called")
				}
				target := b.capture(conv, "", false)
				if r.ConversationID != conv || r.SessionGeneration != target.SessionGeneration || r.Revision == 0 {
					t.Fatalf("newly resolved catalog lost request ordering: %+v, target %+v", r, target)
				}
				want, _ := mapModelList(list, conv)
				if multi {
					want.Models = pushedModelOptions(saved)(want.Models)
				}
				testCatalogWireReply(t, w, id, conv, source, want, clear)
				legacy, ok := modelListFor(b.reg, pool, saved, b)(conv, multi)
				gotRaw, _ := json.Marshal(legacy)
				wantRaw, _ := json.Marshal(want)
				if !ok || !bytes.Equal(gotRaw, wantRaw) {
					t.Fatal("legacy catalog bytes changed")
				}
				return r
			}
			b.reg.Create(conversations.Conversation{ID: "new"})
			request(200, "new", "", saved.claude, true)
			request(201, "conv", "", saved.claude, true)
			source := b.capture("conv", sid, false)
			b.offer(source, protocol.TypeSessionSettings, protocol.SessionSettingsPayload{SessionID: sid, Model: "selected", Effort: "high"}, "")
			b.sink.live.acceptEvent(source, turnevent.ApiRetry{})
			unrelated := testLiveReadings(t, b.sink.live, "conv")
			delete(unrelated, liveReadingKey{protocol.TypeModelList, ""})
			row, _ := b.reg.Get("conv")
			// A real parser/hold source replaces persisted inventory in the same generation.
			parser := streamsup.NewParser(runner.models.Sink, discardLogger())
			if _, err := parser.Write(testInventoryLine(protocol.TypeModelList, "live")); err != nil {
				t.Fatal(err)
			}
			list, evidence, _ := runner.ModelListLive()
			prior := request(202, "conv", `"`+sid+`"`, list, false)
			afterReplacement := testLiveReadings(t, b.sink.live, "conv")
			delete(afterReplacement, liveReadingKey{protocol.TypeModelList, ""})
			afterRow, _ := b.reg.Get("conv")
			if !reflect.DeepEqual(unrelated, afterReplacement) || !reflect.DeepEqual(row, afterRow) {
				t.Fatal("catalog replacement changed settings, session admission or another reading family")
			}
			// A clear is per target, even when another target spells its session identically.
			b.reg.Create(conversations.Conversation{ID: "other", CurrentSessionID: sid})
			other := request(203, "other", `"`+sid+`"`, list, true)
			b.sink.queueBoundary(sessions.SessionTransition{ConversationID: "conv", PreviousID: sessions.SessionID(sid), NewID: sessions.SessionID(sid), NextAgent: "claude"}, func() {}, nil)
			clearReading := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeModelList, ""}]
			if err := mgr.PushLiveState(t.Context(), "phone", daemonRelayReading(clearReading)); err != nil {
				t.Fatal(err)
			}
			fresh := request(204, "conv", `"`+sid+`"`, list, true)
			retained := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{protocol.TypeModelList, ""}]
			if !retained.Envelope.SessionStateCleared || retained.Revision != clearReading.Revision {
				t.Fatal("predecessor catalog installed as target live state")
			}
			_, after, _ := runner.ModelListLive()
			if after != evidence {
				t.Fatal("resolution rewrote cache evidence")
			}
			request(205, "other", `"`+sid+`"`, list, false)
			// Equal-revision correlated replies remain usable; older revision/generation do not.
			pushReply := func(r daemonLiveReading, id uint64) {
				r.Envelope.InReplyTo = &id
				if err := mgr.PushLiveState(t.Context(), "phone", daemonRelayReading(r)); err != nil {
					t.Fatal(err)
				}
			}
			pushReply(fresh, 206)
			want, _ := mapModelList(list, "conv")
			if multi {
				want.Models = pushedModelOptions(saved)(want.Models)
			}
			testCatalogWireReply(t, w, 206, "conv", `"`+sid+`"`, want, false)
			pushReply(prior, 207)
			pushReply(other, 208)
			if err := mgr.FlushPushes(t.Context()); err != nil {
				t.Fatal(err)
			}
			marker := protocol.Envelope{ID: 999, Type: protocol.TypeError, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)}
			if err := mgr.Push(t.Context(), "phone", marker); err != nil {
				t.Fatal(err)
			}
			for {
				e, _ := testCatalogWireRead(t, w)
				if e.InReplyTo != nil && (*e.InReplyTo == 207 || *e.InReplyTo == 208) {
					t.Fatal("late catalog overtook target ordering")
				}
				if e.ID == marker.ID {
					break
				}
			}
		})
	}
}

func TestDaemonLiveRelayCatalogNoProducingSession(t *testing.T) {
	b := testControlBindings(t, "conv", "")
	list := sentinelModelList("no-session")
	src := b.capture("conv", "", true)
	want, _ := mapModelList(list, "conv")
	// Exercise the request adapter with positively captured no-session inventory.
	provider := func(conv string, _ bool) (daemonLiveReading, bool) {
		_, ok, r := liveInventoryResult(b, conv, protocol.TypeModelList, protocol.Envelope{}, func(daemonLiveSource) (protocol.ModelListPayload, daemonLiveSource, bool) {
			return want, src, true
		})
		return r, ok
	}
	w, _ := testDaemonRelayWire(t, b, relayWiring{modelListReading: provider}, nil, nil)
	w.open("phone", false, protocol.CapabilityThread)
	testCatalogWireRequest(t, w, 301, "conv")
	testCatalogWireReply(t, w, 301, "conv", "null", want, true)
}

func TestDaemonLiveRelayCatalogLegacy(t *testing.T) {
	for _, multi := range []bool{false, true} {
		for _, available := range []bool{false, true} {
			t.Run(map[bool]string{false: "basic", true: "multi"}[multi]+map[bool]string{false: "/unavailable", true: "/available"}[available], func(t *testing.T) {
				pool, _ := newModelListTestPool(t)
				b := testControlBindings(t, "conv", "")
				saved := twoAgentVocabularyDouble{}
				if available {
					saved.claude, saved.haveClaude = sentinelModelList("legacy"), true
				}
				w, _ := testDaemonRelayWire(t, b, relayWiring{modelListReading: daemonModelListReadingFor(b.reg, pool, saved, b)}, nil, func(cfg *relay.V2SessionConfig) {
					cfg.ModelListFor = modelListFor(b.reg, pool, saved, b)
					cfg.MergedModelOptions = pushedModelOptions(saved)
				})
				w.open("phone", multi)
				for _, conv := range []string{"conv", "missing"} {
					testCatalogWireRequest(t, w, 401, conv)
					if available && conv == "conv" {
						want, _ := mapModelList(saved.claude, conv)
						if multi {
							want.Models = claudeModelOptions(want.Models)
						}
						testCatalogWireReply(t, w, 401, conv, "", want, true)
						continue
					}
					e, raw := testCatalogWireRead(t, w)
					if e.InReplyTo == nil || *e.InReplyTo != 401 || len(e.ConversationID) != 0 || len(e.SessionID) != 0 || e.SessionStateCleared || e.EventID != nil {
						t.Fatalf("refusal envelope changed: %s", raw)
					}
					code := "model_list.unavailable"
					if conv == "missing" {
						code = "conversation.not_found"
					}
					var refusal protocol.ErrorPayload
					if err := json.Unmarshal(e.Payload, &refusal); err != nil || e.Type != protocol.TypeError || refusal.Code != code || refusal.Retryable != (conv == "conv") {
						t.Fatalf("catalog refusal changed: %s (%v)", raw, err)
					}
				}
			})
		}
	}
}
