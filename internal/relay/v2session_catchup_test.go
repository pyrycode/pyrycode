package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

const catchUpConversation = "11111111-1111-4111-8111-111111111111"

func testCatchUpPeer(t *testing.T, provider func(context.Context, protocol.ThreadCatchUpPayload) (thread.QueryResult, error), tweak func(*V2SessionConfig)) *testHostedPeer {
	t.Helper()
	return testHostedStartCaps(t, &testAppDiscovery{}, append(append([]string{}, threadCaps...), protocol.CapabilityHostedAppsV1), func(c *V2SessionConfig) {
		c.ThreadReady = func() bool { return true }
		c.KnownConversation = func(id string) bool { return id == catchUpConversation }
		c.ThreadCatchUp = provider
		if tweak != nil {
			tweak(c)
		}
	})
}
func testCatchUpRequest(p *testHostedPeer, id, version uint64) {
	p.request(id, protocol.TypeThreadCatchUp, fmt.Sprintf(`{"conversation_id":%q,"epoch":"epoch","version":%d,"limit":1}`, catchUpConversation, version))
}
func testCatchUpError(t *testing.T, p *testHostedPeer, id uint64) {
	t.Helper()
	env, _ := p.next()
	if env.Type != protocol.TypeError || env.InReplyTo == nil || *env.InReplyTo != id {
		t.Fatalf("refusal type: %s", env.Type)
	}
	if strings.Contains(string(env.Payload), catchUpConversation) || strings.Contains(string(env.Payload), "secret") {
		t.Fatal("error exposes request/provider content")
	}
	var payload protocol.ErrorPayload
	if json.Unmarshal(env.Payload, &payload) != nil || payload.Code != "thread.query_failed" || payload.Message != "thread query could not complete" || !payload.Retryable {
		t.Fatalf("query refusal: %+v", payload)
	}
}

func TestThreadCatchUpResults(t *testing.T) {
	item := thread.Item{ID: 1, Kind: "future", Order: 8, Rev: 2, EndedOrder: 9, Session: "session", Agent: "codex", NoChild: true, Turn: "turn", Parent: 4, Status: "dropped", Active: true, Shown: false, Summary: "summary", Subtype: "subtype", Content: json.RawMessage(`{"future":{"values":[null,false,0,"text"]}}`)}
	for _, tc := range []struct {
		name    string
		result  thread.QueryResult
		err     error
		kind    string
		version uint64
	}{
		{"usable", thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, FromVersion: 20, Items: []thread.Item{item, {ID: 4, Kind: "agent", Rev: 3, Parent: 5, Content: json.RawMessage(`{}`)}, {ID: 5, Kind: "agent", Rev: 4, Content: json.RawMessage(`{}`)}}}, nil, protocol.TypeThreadItems, 20},
		{"empty", thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, FromVersion: 20}, nil, protocol.TypeThreadItems, 20},
		{"window", thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, LowerOrder: 7, UpperOrder: 11, Continuation: 7, OlderExists: true, Items: []thread.Item{item}}, nil, protocol.TypeThreadItems, 0},
		{"reset", thread.QueryResult{State: thread.StateUsable, Epoch: "new", Version: 100, ResetRequired: true}, nil, protocol.TypeThreadReset, 20},
		{"notloaded", thread.QueryResult{}, nil, protocol.TypeError, 20},
		{"rebuilding", thread.QueryResult{State: thread.StateRebuilding}, nil, protocol.TypeError, 20},
		{"unavailable", thread.QueryResult{State: thread.StateUnavailable}, nil, protocol.TypeError, 20},
		{"provider", thread.QueryResult{}, errors.New("secret"), protocol.TypeError, 20},
		{"result-error", thread.QueryResult{State: thread.StateUsable, Err: errors.New("secret")}, nil, protocol.TypeError, 20},
		{"codec", thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, FromVersion: 20, Items: []thread.Item{{ID: 1, Content: json.RawMessage(`invalid`)}}}, nil, protocol.TypeError, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			p := testCatchUpPeer(t, func(ctx context.Context, req protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
				calls.Add(1)
				if req.ConversationID != catchUpConversation || req.Version != tc.version || req.Limit != 1 {
					t.Errorf("request: %+v", req)
				}
				return tc.result, tc.err
			}, nil)
			if calls.Load() != 0 {
				t.Fatal("handshake queried")
			}
			testCatchUpRequest(p, 40, tc.version)
			var items []protocol.ThreadItem
			for {
				env, _ := p.next()
				if env.InReplyTo == nil || *env.InReplyTo != 40 || env.Type != tc.kind {
					t.Fatalf("reply: %+v", env)
				}
				if env.Type != protocol.TypeThreadItems {
					if env.Type == protocol.TypeThreadReset {
						var r protocol.ThreadResetPayload
						json.Unmarshal(env.Payload, &r)
						if r.Epoch != "new" || r.Version != 100 {
							t.Fatal(r)
						}
					}
					break
				}
				var r protocol.ThreadItemsPayload
				if err := json.Unmarshal(env.Payload, &r); err != nil {
					t.Fatal(err)
				}
				if r.Epoch != tc.result.Epoch || r.Version != tc.result.Version {
					t.Fatal("capture metadata changed")
				}
				if r.Final {
					if len(r.Items) != 0 {
						t.Fatal("items in marker")
					}
					if tc.version > 0 {
						if r.FromVersion == nil || *r.FromVersion != 20 || r.LowerOrder != nil {
							t.Fatal("coverage derived from items")
						}
					} else if r.LowerOrder == nil || *r.LowerOrder != 7 || r.UpperOrder == nil || *r.UpperOrder != 11 || r.Continuation == nil || *r.Continuation != 7 || r.OlderExists == nil || !*r.OlderExists {
						t.Fatal("window bounds")
					}
					break
				}
				if r.FromVersion != nil || r.LowerOrder != nil {
					t.Fatal("batch certifies coverage")
				}
				items = append(items, r.Items...)
			}
			if tc.kind == protocol.TypeThreadItems {
				want := make([]protocol.ThreadItem, 0, len(tc.result.Items))
				for _, i := range tc.result.Items {
					want = append(want, protocol.ThreadItem{ID: i.ID, Kind: i.Kind, Order: i.Order, Rev: i.Rev, EndedOrder: i.EndedOrder, Session: i.Session, Agent: i.Agent, NoChild: i.NoChild, Turn: i.Turn, Parent: i.Parent, Status: i.Status, Active: i.Active, Shown: i.Shown, Summary: i.Summary, Subtype: i.Subtype, Content: i.Content})
				}
				if len(items) != len(want) || len(items) > 0 && !reflect.DeepEqual(items, want) {
					t.Fatalf("items: %+v want %+v", items, want)
				}
			}
			if calls.Load() != 1 {
				t.Fatal("query count")
			}
			p.request(41, protocol.TypeAppCancel, `{"request_id":999}`)
			env, _ := p.next()
			if env.Type != protocol.TypeAppCancelled {
				t.Fatal("extra query completion")
			}
		})
	}
}

func TestThreadCatchUpAdmission(t *testing.T) {
	base := fmt.Sprintf(`{"conversation_id":%q,"epoch":"","version":0}`, catchUpConversation)
	cases := []string{`{}`, `null`, strings.Replace(base, `"epoch":"",`, "", 1), strings.Replace(base, `"epoch":""`, `"epoch":null`, 1), strings.Replace(base, `"epoch":""`, `"epoch":1`, 1), strings.Replace(base, `,"version":0`, "", 1)}
	for _, v := range []string{"null", "-1", "1.5", "\"0\"", "18446744073709551616"} {
		cases = append(cases, strings.Replace(base, `"version":0`, `"version":`+v, 1))
	}
	for _, id := range []string{`null`, `1`, `"foreign"`, `"../../secret"`, `"22222222-2222-4222-8222-222222222222"`} {
		cases = append(cases, strings.Replace(base, fmt.Sprintf("%q", catchUpConversation), id, 1))
	}
	for _, limit := range []string{"-1", "1.5", "\"1\"", "null"} {
		cases = append(cases, strings.TrimSuffix(base, "}")+`,"limit":`+limit+"}")
	}
	var calls atomic.Int32
	p := testCatchUpPeer(t, func(context.Context, protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
		calls.Add(1)
		return thread.QueryResult{}, nil
	}, nil)
	for i, payload := range cases {
		p.request(uint64(40+i), protocol.TypeThreadCatchUp, payload)
		testCatchUpError(t, p, uint64(40+i))
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request reached provider")
	}
	for _, gate := range []string{"thread", "interactive", "codex", "missing"} {
		t.Run(gate, func(t *testing.T) {
			p := testCatchUpPeer(t, nil, func(c *V2SessionConfig) {
				c.ThreadCatchUp = func(context.Context, protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
					calls.Add(1)
					return thread.QueryResult{}, nil
				}
				switch gate {
				case "thread":
					c.ThreadReady = func() bool { return false }
				case "codex":
					c.CodexConversation = func(string) bool { return true }
				case "missing":
					c.ThreadCatchUp = nil
				case "interactive":
					c.ThreadReady = func() bool { return true }
				}
			})
			if gate == "interactive" {
				q := &testHostedPeer{t: t, mgr: p.mgr, frames: p.frames, rec: p.rec, conn: "noninteractive", pub: p.pub, events: p.events}
				q.open([]string{protocol.CapabilityThread})
				p = q
			}
			testCatchUpRequest(p, 40, 20)
			testCatchUpError(t, p, 40)
		})
	}
	if calls.Load() != 0 {
		t.Fatal("ineligible request reached provider")
	}
	for _, limit := range []int{0, 1, 999} {
		p := testCatchUpPeer(t, func(_ context.Context, r protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
			want, _ := protocol.NormalizeThreadQueryLimit(limit)
			if r.Limit != want {
				t.Errorf("limit %d", r.Limit)
			}
			return thread.QueryResult{}, nil
		}, nil)
		p.request(40, protocol.TypeThreadCatchUp, strings.TrimSuffix(base, "}")+fmt.Sprintf(`,"limit":%d}`, limit))
		testCatchUpError(t, p, 40)
	}
	p.raw(`{"type":"thread_catch_up","payload":{}}`)
	testHostedGone(t, p)
}

func testCatchUpLive(t *testing.T, p *testHostedPeer, version uint64) {
	t.Helper()
	payload, _ := json.Marshal(protocol.ThreadTextAppendPayload{ConversationID: catchUpConversation, Epoch: "epoch", Version: version, ItemID: 1, BaseRev: 2, Rev: 3, Text: "new"})
	if err := p.mgr.Push(t.Context(), p.conn, protocol.Envelope{Type: protocol.TypeThreadTextAppend, Payload: payload}); err != nil {
		t.Fatal(err)
	}
}

func TestThreadCatchUpLiveRace(t *testing.T) {
	testThreadQueryLiveRace(t, false)
}

func testThreadQueryLiveRace(t *testing.T, page bool) {
	entered, exited := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	p := testThreadQueryPeer(t, page, func(ctx context.Context, _ protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			close(exited)
			return thread.QueryResult{}, ctx.Err()
		}
		return thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 20, FromVersion: 20}, nil
	}, nil)
	testThreadQueryRequest(p, page, 40, 20)
	testHostedAwait(t, entered)
	// Another connection and manager control remain usable during provider capture.
	legacy := p.sibling("legacy")
	legacy.request(70, protocol.TypeAppCancel, `{"request_id":999}`)
	e, _ := legacy.next()
	if e.Type != protocol.TypeAppCancelled {
		t.Fatal(e)
	}
	if len(p.mgr.ActiveConns(t.Context())) != 2 {
		t.Fatal("manager blocked")
	}
	testCatchUpLive(t, p, 21)
	testHostedAwait(t, exited)
	var failed, live bool
	for range 2 {
		e, _ := p.next()
		switch e.Type {
		case protocol.TypeError:
			failed = e.InReplyTo != nil && *e.InReplyTo == 40
		case protocol.TypeThreadTextAppend:
			live = true
		default:
			t.Fatalf("race falsely completed: %+v", e)
		}
	}
	if !failed || !live {
		t.Fatal("missing explicit failure/live")
	}
	testThreadQueryRequest(p, page, 41, 20)
	e, _ = p.next()
	var result protocol.ThreadItemsPayload
	json.Unmarshal(e.Payload, &result)
	if !result.Final {
		t.Fatal("retry failed")
	}
}

func TestThreadCatchUpJoinedLive(t *testing.T) {
	testThreadQueryJoinedLive(t, false)
}

func testThreadQueryJoinedLive(t *testing.T, page bool) {
	p := testThreadQueryPeer(t, page, func(context.Context, protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
		return thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, FromVersion: 20}, nil
	}, nil)
	testThreadQueryRequest(p, page, 40, 20)
	e, _ := p.next()
	var result protocol.ThreadItemsPayload
	json.Unmarshal(e.Payload, &result)
	if !result.Final {
		t.Fatal("missing successful join")
	}
	testCatchUpLive(t, p, 80)
	testCatchUpLive(t, p, 90)
	testCatchUpLive(t, p, 91)
	testCatchUpLive(t, p, 91) // Different source patches may share one consumed version.
	if err := p.mgr.FlushPushes(t.Context()); err != nil {
		t.Fatal(err)
	}
	p.request(41, protocol.TypeAppCancel, `{"request_id":999}`)
	var versions []uint64
	for len(versions) < 2 {
		e, _ = p.next()
		if e.Type != protocol.TypeThreadTextAppend {
			t.Fatalf("live order: %+v", e)
		}
		var r protocol.ThreadTextAppendPayload
		json.Unmarshal(e.Payload, &r)
		versions = append(versions, r.Version)
	}
	if !reflect.DeepEqual(versions, []uint64{91, 91}) {
		t.Fatal(versions)
	}
	e, _ = p.next()
	if e.Type != protocol.TypeAppCancelled {
		t.Fatal("stale patches leaked")
	}
}

func TestThreadCatchUpInterrupted(t *testing.T) {
	testThreadQueryInterrupted(t, false)
}

func testThreadQueryInterrupted(t *testing.T, page bool) {
	for _, mode := range []string{"live", "live-retired", "failed", "cancelled", "reuse", "down", "withheld"} {
		t.Run(mode, func(t *testing.T) {
			entered, exited, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var sends atomic.Int32
			var eligible, up atomic.Bool
			eligible.Store(true)
			up.Store(true)
			p := testThreadQueryPeer(t, page, func(context.Context, protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
				return thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, FromVersion: 20, Items: []thread.Item{{ID: 1, Kind: "message", Rev: 2, Summary: strings.Repeat("large", 15000), Content: json.RawMessage(`{}`)}}}, nil
			}, func(c *V2SessionConfig) {
				c.KnownConversation = func(id string) bool { return eligible.Load() && id == catchUpConversation }
				c.Connected = func() bool { return up.Load() }
				out := c.HostedAppsSend
				c.HostedAppsSend = func(ctx context.Context, e protocol.RoutingEnvelope) error {
					var frame struct {
						Type string `json:"type"`
					}
					json.Unmarshal(e.Frame, &frame)
					target := int32(2)
					if mode == "withheld" || mode == "down" {
						target = 1
					}
					if frame.Type == protocol.TypeNoiseMsg && sends.Add(1) == target {
						defer close(exited)
						if mode == "withheld" || mode == "down" {
							close(entered)
							select {
							case <-release:
							case <-ctx.Done():
								return ctx.Err()
							}
						} else {
							// Accept the first part and hold its outcome. The next required part is
							// never certified if cancellation/failure interrupts this assembly.
							if err := out(ctx, e); err != nil {
								return err
							}
							close(entered)
							select {
							case <-release:
							case <-ctx.Done():
								return ctx.Err()
							}
							if mode == "failed" {
								return errors.New("secret transport failure")
							}
							return ctx.Err()
						}
					}
					return out(ctx, e)
				}
			})
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			testThreadQueryRequest(p, page, 40, 20)
			testHostedAwait(t, entered)
			switch mode {
			case "withheld":
				eligible.Store(false)
				close(release)
			case "down":
				up.Store(false)
				close(release)
			case "failed":
				close(release)
			case "live", "live-retired":
				payload, _ := json.Marshal(protocol.ThreadTextAppendPayload{ConversationID: catchUpConversation, Epoch: "epoch", Version: 91, ItemID: 1, BaseRev: 2, Rev: 3, Text: "new"})
				env := protocol.Envelope{Type: protocol.TypeThreadTextAppend, Payload: payload}
				if mode == "live-retired" {
					p.mgr.cancelThreadQuery(p.conn, env)
					testHostedGone(t, p)
				}
				// Cancelling a sealed query can retire its queue before push admission.
				err := p.mgr.Push(t.Context(), p.conn, env)
				if err != nil && !errors.Is(err, ErrConnNotFound) {
					t.Fatal(err)
				}
				if mode == "live-retired" && !errors.Is(err, ErrConnNotFound) {
					t.Fatalf("retired push: %v", err)
				}
			case "cancelled", "reuse":
				p.frames <- protocol.RoutingEnvelope{ConnID: p.conn, CloseCode: 1000}
			}
			testHostedAwait(t, exited)
			if mode == "withheld" {
				first, _ := p.next()
				var part protocol.ThreadUpdatePart
				if json.Unmarshal(first.Payload, &part) != nil || part.Continuation.Final {
					t.Fatal("expected incomplete assembly prefix")
				}
				testCatchUpError(t, p, 40)
				p.request(41, protocol.TypeAppCancel, `{"request_id":999}`)
				e, _ := p.next()
				if e.Type != protocol.TypeAppCancelled {
					t.Fatal("completion leaked")
				}
				return
			}
			testHostedGone(t, p)
			// Decrypt the accepted prefix and establish that fragment completion is not
			// present, much less a separate successful query marker.
			for _, wire := range p.rec.snapshot()[p.seen:] {
				if wire.ConnID != p.conn || wire.CloseCode != 0 {
					continue
				}
				raw := threadPlain(t, wire, p.recv)
				var e protocol.Envelope
				json.Unmarshal(raw, &e)
				if e.Type == protocol.TypeThreadItems {
					var body map[string]json.RawMessage
					json.Unmarshal(e.Payload, &body)
					if string(body["final"]) == "true" {
						t.Fatal("false success")
					}
					if body["continuation"] == nil {
						t.Fatal("fixture was not multiframe")
					}
				}
			}
			if mode == "reuse" || mode == "down" {
				if mode == "down" {
					// Session removal precedes asynchronous terminal delivery. Await that
					// delivery before taking the new handshake's recorder boundary.
					testHostedClose(t, p)
				}
				up.Store(true)
				p.open(append(threadCaps, protocol.CapabilityHostedAppsV1))
				testThreadQueryRequest(p, page, 42, 20)
				for {
					e, _ := p.next()
					var r protocol.ThreadItemsPayload
					if json.Unmarshal(e.Payload, &r) == nil && r.Final {
						if *e.InReplyTo != 42 {
							t.Fatal("old query crossed generation")
						}
						break
					}
				}
			}
		})
	}
}

func TestThreadCatchUpRequestedScope(t *testing.T) {
	testThreadQueryRequestedScope(t, false)
}

func testThreadQueryRequestedScope(t *testing.T, page bool) {
	var calls atomic.Int32
	p := testThreadQueryPeer(t, page, func(_ context.Context, r protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
		calls.Add(1)
		if r.ConversationID != catchUpConversation {
			t.Error("active cursor substituted")
		}
		return thread.QueryResult{}, nil
	}, nil)
	for _, isThread := range []bool{true, false} {
		q := &testHostedPeer{t: t, mgr: p.mgr, frames: p.frames, rec: p.rec, conn: fmt.Sprint("reconnect-", isThread), pub: p.pub, events: p.events, ids: map[uint64]bool{1: true}}
		caps := []string{protocol.CapabilityInteractive}
		if isThread {
			caps = threadCaps
		}
		var hello protocol.Envelope
		json.Unmarshal(buildHelloEarlyDataCaps(t, v2TestToken, caps), &hello)
		hello.Payload = replaceObjectField(hello.Payload, "last_event_id", json.RawMessage(`999999`))
		raw, _ := json.Marshal(hello)
		init, err := noise.NewInitiator(v2TestInstallPriv, p.pub)
		if err != nil {
			t.Fatal(err)
		}
		msg, err := init.WriteInit(raw)
		if err != nil {
			t.Fatal(err)
		}
		start := len(p.rec.snapshot())
		q.frames <- wrapInnerFrame(t, q.conn, protocol.TypeNoiseInit, msg)
		waitConnOpen(t, p.mgr, q.conn)
		for i, wire := range p.rec.snapshot()[start:] {
			if wire.ConnID != q.conn {
				continue
			}
			_, q.send, q.recv, err = init.ReadResp(decodeRespFrame(t, wire))
			if err != nil {
				t.Fatal(err)
			}
			q.seen = start + i + 1
			break
		}
		q.request(38, protocol.TypeListConversations, `{}`)
		e, _ := q.next()
		if e.InReplyTo == nil || *e.InReplyTo != 38 {
			t.Fatal("hello emitted unsolicited recovery")
		}
		if calls.Load() != 0 {
			t.Fatal("handshake queried thread")
		}
	}
	p.request(39, protocol.TypeListConversations, `{}`)
	p.next()
	if calls.Load() != 0 {
		t.Fatal("listing queried")
	}
	testThreadQueryRequest(p, page, 40, 20)
	testCatchUpError(t, p, 40)
	if calls.Load() != 1 {
		t.Fatal("explicit request not served")
	}
}

func TestThreadCatchUpProviderShutdown(t *testing.T) {
	testThreadQueryProviderShutdown(t, false)
}

func testThreadQueryProviderShutdown(t *testing.T, page bool) {
	entered, exited := make(chan struct{}), make(chan struct{})
	p := testThreadQueryPeer(t, page, func(ctx context.Context, _ protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return thread.QueryResult{}, ctx.Err()
	}, nil)
	testThreadQueryRequest(p, page, 40, 20)
	testHostedAwait(t, entered)
	p.stop()
	testHostedAwait(t, exited)
}

func TestThreadCatchUpLegacyRefusal(t *testing.T) {
	p := testHostedStartCaps(t, nil, nil, func(c *V2SessionConfig) {
		c.HostedAppsSend = nil
		c.OutboundContext = nil
		c.ThreadCatchUp = func(context.Context, protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
			t.Error("legacy called provider")
			return thread.QueryResult{}, nil
		}
	})
	testCatchUpRequest(p, 40, 20)
	testCatchUpError(t, p, 40)
}
