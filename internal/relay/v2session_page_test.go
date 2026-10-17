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
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func testPagePeer(t *testing.T, provider func(context.Context, protocol.ThreadPagePayload) (thread.QueryResult, error), tweak func(*V2SessionConfig)) *testHostedPeer {
	t.Helper()
	return testCatchUpPeer(t, nil, func(c *V2SessionConfig) {
		c.ThreadPage = provider
		if tweak != nil {
			tweak(c)
		}
	})
}

func testPageRequest(p *testHostedPeer, id, before uint64) {
	p.request(id, protocol.TypeThreadPage, fmt.Sprintf(`{"conversation_id":%q,"before_order":%d,"limit":1}`, catchUpConversation, before))
}

// Reuse the same authenticated race scenarios for both query types. Page bounds
// remain independent of the catch-up coverage used by the original fixture.
func testThreadQueryPeer(t *testing.T, page bool, provider func(context.Context, protocol.ThreadCatchUpPayload) (thread.QueryResult, error), tweak func(*V2SessionConfig)) *testHostedPeer {
	t.Helper()
	if !page {
		return testCatchUpPeer(t, provider, tweak)
	}
	return testPagePeer(t, func(ctx context.Context, req protocol.ThreadPagePayload) (thread.QueryResult, error) {
		r, err := provider(ctx, protocol.ThreadCatchUpPayload{ConversationID: req.ConversationID, Epoch: "epoch", Version: req.BeforeOrder, Limit: req.Limit})
		r.UpperOrder = req.BeforeOrder
		return r, err
	}, tweak)
}

func testThreadQueryRequest(p *testHostedPeer, page bool, id, before uint64) {
	if page {
		testPageRequest(p, id, before)
	} else {
		testCatchUpRequest(p, id, before)
	}
}

func TestThreadPageLiveRace(t *testing.T)         { testThreadQueryLiveRace(t, true) }
func TestThreadPageJoinedLive(t *testing.T)       { testThreadQueryJoinedLive(t, true) }
func TestThreadPageInterrupted(t *testing.T)      { testThreadQueryInterrupted(t, true) }
func TestThreadPageProviderShutdown(t *testing.T) { testThreadQueryProviderShutdown(t, true) }
func TestThreadPageRequestedScope(t *testing.T)   { testThreadQueryRequestedScope(t, true) }

func TestThreadPageLegacyRefusal(t *testing.T) {
	p := testHostedStartCaps(t, nil, nil, func(c *V2SessionConfig) {
		c.HostedAppsSend = nil
		c.OutboundContext = nil
		c.ThreadPage = func(context.Context, protocol.ThreadPagePayload) (thread.QueryResult, error) {
			t.Error("legacy called page provider")
			return thread.QueryResult{}, nil
		}
	})
	testPageRequest(p, 40, 20)
	testCatchUpError(t, p, 40)
}

func TestThreadPageAdmission(t *testing.T) {
	base := fmt.Sprintf(`{"conversation_id":%q,"before_order":0}`, catchUpConversation)
	cases := []string{`{}`, `null`, strings.Replace(base, `,"before_order":0`, "", 1), `{"before_order":0}`}
	for _, v := range []string{"null", "-1", "1.5", `"0"`, "true", "18446744073709551616"} {
		cases = append(cases, strings.Replace(base, `"before_order":0`, `"before_order":`+v, 1))
	}
	for _, id := range []string{`null`, `1`, `"foreign"`, `"../../secret"`, `"22222222-2222-4222-8222-222222222222"`} {
		cases = append(cases, strings.Replace(base, fmt.Sprintf("%q", catchUpConversation), id, 1))
	}
	for _, limit := range []string{"-1", "1.5", `"1"`, "null", "true", "9223372036854775808"} {
		cases = append(cases, strings.TrimSuffix(base, "}")+`,"limit":`+limit+"}")
	}
	var calls atomic.Int32
	provider := func(context.Context, protocol.ThreadPagePayload) (thread.QueryResult, error) {
		calls.Add(1)
		return thread.QueryResult{}, nil
	}
	p := testPagePeer(t, provider, nil)
	for i, payload := range cases {
		p.request(uint64(40+i), protocol.TypeThreadPage, payload)
		testCatchUpError(t, p, uint64(40+i))
	}
	for _, gate := range []string{"thread", "interactive", "codex", "missing", "revoked"} {
		t.Run(gate, func(t *testing.T) {
			p := testPagePeer(t, provider, func(c *V2SessionConfig) {
				switch gate {
				case "thread":
					c.ThreadReady = func() bool { return false }
				case "codex":
					c.CodexConversation = func(string) bool { return true }
				case "missing":
					c.ThreadPage = nil
				}
			})
			if gate == "interactive" {
				q := &testHostedPeer{t: t, mgr: p.mgr, frames: p.frames, rec: p.rec, conn: "noninteractive-page", pub: p.pub, events: p.events}
				q.open([]string{protocol.CapabilityThread})
				p = q
			}
			if gate == "revoked" {
				for _, device := range p.mgr.cfg.Devices.List() {
					p.mgr.cfg.Devices.Remove(device.Name)
				}
			}
			testPageRequest(p, 40, 20)
			if gate == "revoked" {
				testHostedGone(t, p)
				return
			}
			testCatchUpError(t, p, 40)
		})
	}
	if calls.Load() != 0 {
		t.Fatal("invalid/ineligible request reached provider")
	}
	for _, limit := range []string{"", ",\"limit\":0", ",\"limit\":1", ",\"limit\":999"} {
		t.Run("limit"+limit, func(t *testing.T) {
			var calls atomic.Int32
			p := testPagePeer(t, func(_ context.Context, r protocol.ThreadPagePayload) (thread.QueryResult, error) {
				calls.Add(1)
				want := 50
				if strings.HasSuffix(limit, ":1") {
					want = 1
				}
				if strings.HasSuffix(limit, ":999") {
					want = 256
				}
				if r.ConversationID != catchUpConversation || r.BeforeOrder != 0 || r.Limit != want {
					t.Errorf("normalized request: %+v", r)
				}
				return thread.QueryResult{}, nil
			}, nil)
			p.request(40, protocol.TypeThreadPage, strings.TrimSuffix(base, "}")+limit+"}")
			testCatchUpError(t, p, 40)
			if calls.Load() != 1 {
				t.Fatal("provider not called once")
			}
		})
	}
	for _, id := range []string{"", `,"id":null`, `,"id":-1`, `,"id":"1"`} {
		p := testPagePeer(t, provider, nil)
		p.raw(`{"type":"thread_page","payload":{}` + id + `}`)
		testHostedGone(t, p)
	}
}

func TestThreadPageResults(t *testing.T) {
	items := []thread.Item{
		{ID: 1, Kind: "future", Order: 8, Rev: 2, EndedOrder: 9, Session: "session", Agent: "codex", NoChild: true, Turn: "turn", Parent: 4, Status: "dropped", Active: true, Shown: false, Summary: "summary", Subtype: "subtype", Content: json.RawMessage(`{"future":{"values":[null,false,0,"text"]}}`)},
		{ID: 2, Kind: "message", Order: 8, Rev: 3, Parent: 4, Shown: true, Content: json.RawMessage(`{"text":"tied"}`)},
		{ID: 4, Kind: "agent", Order: 2, Rev: 4, Parent: 5, Content: json.RawMessage(`{"text":"shared parent"}`)},
		{ID: 5, Kind: "agent", Order: 1, Rev: 5, Content: json.RawMessage(`{"text":"root parent"}`)},
	}
	short := items[0]
	short.Parent = 0
	for _, tc := range []struct {
		name   string
		result thread.QueryResult
		err    error
	}{
		{"ties-parents", thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, LowerOrder: 8, UpperOrder: 11, Continuation: 8, OlderExists: true, Items: items}, nil},
		{"short-older", thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, LowerOrder: 8, UpperOrder: 11, Continuation: 8, OlderExists: true, Items: []thread.Item{short}}, nil},
		{"short-start", thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, LowerOrder: 8, UpperOrder: 11, Continuation: 8, Items: []thread.Item{short}}, nil},
		{"empty", thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, UpperOrder: 11}, nil},
		{"zero", thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90}, nil},
		{"notloaded", thread.QueryResult{}, nil},
		{"rebuilding", thread.QueryResult{State: thread.StateRebuilding}, nil},
		{"unavailable", thread.QueryResult{State: thread.StateUnavailable}, nil},
		{"provider", thread.QueryResult{}, errors.New("secret")},
		{"result-error", thread.QueryResult{State: thread.StateUsable, Err: errors.New("secret")}, nil},
		{"reset", thread.QueryResult{State: thread.StateUsable, ResetRequired: true}, nil},
		{"codec", thread.QueryResult{State: thread.StateUsable, Items: []thread.Item{{ID: 1, Content: json.RawMessage(`invalid`)}}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testPagePeer(t, func(context.Context, protocol.ThreadPagePayload) (thread.QueryResult, error) {
				return tc.result, tc.err
			}, nil)
			limit := 1
			if strings.HasPrefix(tc.name, "short") {
				limit = 50
			}
			p.request(40, protocol.TypeThreadPage, fmt.Sprintf(`{"conversation_id":%q,"before_order":%d,"limit":%d}`, catchUpConversation, tc.result.UpperOrder, limit))
			if tc.result.State != thread.StateUsable || tc.result.Err != nil || tc.err != nil || tc.result.ResetRequired || tc.name == "codec" {
				testCatchUpError(t, p, 40)
				return
			}
			got, marker := testReadPage(t, p, 40)
			var want []protocol.ThreadItem
			for _, i := range tc.result.Items {
				want = append(want, protocol.ThreadItem{ID: i.ID, Kind: i.Kind, Order: i.Order, Rev: i.Rev, EndedOrder: i.EndedOrder, Session: i.Session, Agent: i.Agent, NoChild: i.NoChild, Turn: i.Turn, Parent: i.Parent, Status: i.Status, Active: i.Active, Shown: i.Shown, Summary: i.Summary, Subtype: i.Subtype, Content: i.Content})
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("items: %#v want %#v", got, want)
			}
			if *marker.LowerOrder != tc.result.LowerOrder || *marker.UpperOrder != tc.result.UpperOrder || *marker.Continuation != tc.result.Continuation || *marker.OlderExists != tc.result.OlderExists {
				t.Fatalf("coverage: %+v", marker)
			}
			p.request(41, protocol.TypeAppCancel, `{"request_id":999}`)
			e, _ := p.next()
			if e.Type != protocol.TypeAppCancelled {
				t.Fatal("extra terminal marker")
			}
		})
	}
}

func TestThreadPageMultiAgent(t *testing.T) {
	var calls atomic.Int32
	upper := ^uint64(0)
	p := testPagePeer(t, func(_ context.Context, r protocol.ThreadPagePayload) (thread.QueryResult, error) {
		calls.Add(1)
		if r.BeforeOrder != upper {
			t.Errorf("unsigned upper bound: %d", r.BeforeOrder)
		}
		return thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, UpperOrder: upper}, nil
	}, func(c *V2SessionConfig) { c.CodexConversation = func(string) bool { return true } })
	q := &testHostedPeer{t: t, mgr: p.mgr, frames: p.frames, rec: p.rec, conn: "multi-agent-page", pub: p.pub, events: p.events}
	q.open(append(append([]string{}, threadCaps...), protocol.CapabilityMultiAgent))
	testPageRequest(q, 40, upper)
	_, marker := testReadPage(t, q, 40)
	if calls.Load() != 1 || *marker.UpperOrder != upper {
		t.Fatal("eligible Codex page refused")
	}
}

func testReadPage(t *testing.T, p *testHostedPeer, id uint64) ([]protocol.ThreadItem, protocol.ThreadItemsPayload) {
	t.Helper()
	var items []protocol.ThreadItem
	for {
		e, obj := p.next()
		raw, err := json.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > protocol.MaxThreadEnvelopeBytes || e.Type != protocol.TypeThreadItems || e.InReplyTo == nil || *e.InReplyTo != id {
			t.Fatalf("page envelope: %+v", e)
		}
		var body protocol.ThreadItemsPayload
		if err := json.Unmarshal(e.Payload, &body); err != nil {
			t.Fatal(err)
		}
		if body.ConversationID != catchUpConversation || body.Epoch != "epoch" || body.Version != 90 || body.FromVersion != nil {
			t.Fatalf("page identity: %+v", body)
		}
		if body.Final {
			if body.Items == nil || len(body.Items) != 0 || body.LowerOrder == nil || body.UpperOrder == nil || body.Continuation == nil || body.OlderExists == nil {
				t.Fatalf("page marker: %#v", body)
			}
			return items, body
		}
		if body.LowerOrder != nil || body.UpperOrder != nil || body.Continuation != nil || body.OlderExists != nil {
			t.Fatal("batch certified coverage")
		}
		items = append(items, body.Items...)
	}
}

func TestThreadPageContinuation(t *testing.T) {
	var calls atomic.Int32
	p := testPagePeer(t, func(_ context.Context, req protocol.ThreadPagePayload) (thread.QueryResult, error) {
		n := calls.Add(1) - 1
		uppers, lowers := []uint64{11, 8, 4}, []uint64{8, 4, 1}
		if int(n) >= len(uppers) || req.BeforeOrder != uppers[n] {
			t.Fatalf("discontinuous request: %+v", req)
		}
		return thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, LowerOrder: lowers[n], UpperOrder: uppers[n], Continuation: lowers[n], OlderExists: n < 2, Items: []thread.Item{{ID: lowers[n], Order: lowers[n], Kind: "message", Rev: 2, Content: json.RawMessage(`{"text":"older"}`)}}}, nil
	}, nil)
	before := uint64(11)
	for id := uint64(40); id < 43; id++ {
		testPageRequest(p, id, before)
		items, marker := testReadPage(t, p, id)
		if *marker.UpperOrder != before || len(items) != 1 || items[0].Order != *marker.LowerOrder || *marker.OlderExists != (id < 42) {
			t.Fatalf("page continuity: %+v", marker)
		}
		before = *marker.Continuation
	}
}

func TestThreadPageSharedAdmission(t *testing.T) {
	for _, firstPage := range []bool{true, false} {
		for _, secondPage := range []bool{true, false} {
			t.Run(fmt.Sprintf("busy/%t/%t", firstPage, secondPage), func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var calls atomic.Int32
				provider := func(ctx context.Context) (thread.QueryResult, error) {
					if calls.Add(1) == 1 {
						close(entered)
					}
					select {
					case <-release:
					case <-ctx.Done():
					}
					return thread.QueryResult{}, nil
				}
				p := testPagePeer(t, func(ctx context.Context, _ protocol.ThreadPagePayload) (thread.QueryResult, error) {
					return provider(ctx)
				}, func(c *V2SessionConfig) {
					c.ThreadCatchUp = func(ctx context.Context, _ protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
						return provider(ctx)
					}
				})
				t.Cleanup(func() {
					select {
					case <-release:
					default:
						close(release)
					}
				})
				testThreadQueryRequest(p, firstPage, 40, 20)
				testHostedAwait(t, entered)
				testThreadQueryRequest(p, secondPage, 41, 20)
				testCatchUpError(t, p, 41)
				if calls.Load() != 1 {
					t.Fatal("second provider ran while query unfinished")
				}
				close(release)
				testCatchUpError(t, p, 40)
				testThreadQueryRequest(p, secondPage, 42, 20)
				testCatchUpError(t, p, 42)
				if calls.Load() != 2 {
					t.Fatal("failed query retained slot")
				}
			})
		}
	}
	t.Run("join-bound", func(t *testing.T) {
		var calls atomic.Int32
		p := testPagePeer(t, func(context.Context, protocol.ThreadPagePayload) (thread.QueryResult, error) {
			calls.Add(1)
			return thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, UpperOrder: 20}, nil
		}, nil)
		p.mgr.hostedMu.Lock()
		in := p.mgr.hostedInboxes[p.conn]
		in.threadJoins = make(map[string]threadJoin)
		for i := range maxThreadJoins {
			in.threadJoins[fmt.Sprint(i)] = threadJoin{"epoch", 1, ""}
		}
		p.mgr.hostedMu.Unlock()
		testPageRequest(p, 40, 20)
		testCatchUpError(t, p, 40)
		if calls.Load() != 0 {
			t.Fatal("new conversation exceeded join bound")
		}
		p.mgr.hostedMu.Lock()
		delete(in.threadJoins, "0")
		in.threadJoins[catchUpConversation] = threadJoin{"epoch", 1, ""}
		p.mgr.hostedMu.Unlock()
		testPageRequest(p, 41, 20)
		testReadPage(t, p, 41)
		if calls.Load() != 1 {
			t.Fatal("existing join refused at bound")
		}
	})
}

func TestThreadPageLargeReply(t *testing.T) {
	items := make([]thread.Item, hostedQueueLimit+4)
	want := make([]protocol.ThreadItem, len(items))
	for i := range items {
		content, _ := json.Marshal(map[string]string{"text": strings.Repeat("<&界", protocol.MaxThreadEnvelopeBytes/20)})
		items[i] = thread.Item{ID: uint64(i + 1), Kind: "future", Order: uint64(i + 5), Rev: 2, Content: content}
		want[i] = protocol.ThreadItem{ID: items[i].ID, Kind: items[i].Kind, Order: items[i].Order, Rev: 2, Content: content}
	}
	items[5].Summary = strings.Repeat("<&界", protocol.MaxThreadEnvelopeBytes/2)
	want[5].Summary = items[5].Summary
	started := time.Now()
	var firstSend atomic.Bool
	p := testPagePeer(t, func(context.Context, protocol.ThreadPagePayload) (thread.QueryResult, error) {
		t.Logf("large page provider ready after %s", time.Since(started))
		return thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, Items: items, LowerOrder: 5, UpperOrder: 100, Continuation: 5, OlderExists: true}, nil
	}, func(c *V2SessionConfig) {
		send := c.HostedAppsSend
		c.HostedAppsSend = func(ctx context.Context, env protocol.RoutingEnvelope) error {
			if firstSend.CompareAndSwap(false, true) {
				t.Logf("large page first transport after %s", time.Since(started))
			}
			return send(ctx, env)
		}
		c.OutboundContext = c.HostedAppsSend
	})
	testPageRequest(p, 40, 100)
	// One deadline covers encoding, delivery and reconstruction of the entire
	// escaped reply under race instrumentation, including its final marker.
	deadline := time.Now().Add(15 * time.Second)
	var got []protocol.ThreadItem
	var assembly strings.Builder
	var index, offset, previous uint64
	count, fragments := 0, 0
	for {
		e, obj := p.nextBefore(deadline)
		raw, err := json.Marshal(obj)
		if err != nil || len(raw) > protocol.MaxThreadEnvelopeBytes || e.ID <= previous || e.Type != protocol.TypeThreadItems || e.InReplyTo == nil || *e.InReplyTo != 40 {
			t.Fatalf("bounded correlated envelope: %d %+v", len(raw), e)
		}
		count++
		previous = e.ID
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(e.Payload, &fields); err != nil {
			t.Fatal(err)
		}
		if c := fields["continuation"]; len(c) > 0 && c[0] == '{' {
			var part protocol.ThreadUpdatePart
			if err := json.Unmarshal(e.Payload, &part); err != nil {
				t.Fatal(err)
			}
			if part.ConversationID != catchUpConversation || part.Epoch != "epoch" || part.Version != 90 || part.ItemID != items[5].ID || part.Rev != items[5].Rev || part.Continuation.Index != index || part.Continuation.Offset != offset {
				t.Fatal("assembly identity/order")
			}
			index++
			fragments++
			offset += uint64(len(part.Data))
			assembly.WriteString(part.Data)
			if !part.Continuation.Final {
				continue
			}
			if offset != part.Continuation.TotalBytes {
				t.Fatal("incomplete assembly")
			}
			e.Payload = json.RawMessage(assembly.String())
			assembly.Reset()
			index, offset = 0, 0
		}
		var batch protocol.ThreadItemsPayload
		if err := json.Unmarshal(e.Payload, &batch); err != nil {
			t.Fatal(err)
		}
		if batch.ConversationID != catchUpConversation || batch.Epoch != "epoch" || batch.Version != 90 || batch.FromVersion != nil {
			t.Fatal("batch identity")
		}
		if batch.Final {
			if assembly.Len() != 0 || batch.Items == nil || len(batch.Items) != 0 || batch.LowerOrder == nil || *batch.LowerOrder != 5 || batch.UpperOrder == nil || *batch.UpperOrder != 100 || batch.Continuation == nil || *batch.Continuation != 5 || batch.OlderExists == nil || !*batch.OlderExists {
				t.Fatal("incorrect final coverage")
			}
			break
		}
		if batch.LowerOrder != nil || batch.UpperOrder != nil || batch.Continuation != nil || batch.OlderExists != nil {
			t.Fatal("fragment/batch certified range")
		}
		got = append(got, batch.Items...)
	}
	if count <= hostedQueueLimit || fragments < 2 || !reflect.DeepEqual(got, want) {
		t.Fatalf("large reply lost bytes: %d envelopes, %d fragments, %d items", count, fragments, len(got))
	}
	t.Logf("large page reconstructed after %s: %d envelopes, %d fragments, %d items", time.Since(started), count, fragments, len(got))
	p.request(41, protocol.TypeAppCancel, `{"request_id":999}`)
	e, _ := p.next()
	if e.Type != protocol.TypeAppCancelled {
		t.Fatal("extra success marker")
	}
}
