package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

const compactOther = "22222222-2222-4222-8222-222222222222"

var compactCaps = append(slices.Clone(expansionCaps), protocol.CapabilityThreadCompact)

// testCompactCalls counts every query provider so refusals can prove zero calls.
type testCompactCalls struct {
	complete, page, compact, compactPage atomic.Int32
	lastCatchUp                          atomic.Pointer[protocol.ThreadCatchUpPayload]
	lastPage                             atomic.Pointer[protocol.ThreadPagePayload]
}

func (c *testCompactCalls) total() int32 {
	return c.complete.Load() + c.page.Load() + c.compact.Load() + c.compactPage.Load()
}

func testCompactResult(conv string, from uint64) thread.CompactQueryResult {
	failed, count, has, subtree := "exit 1", uint64(2), true, uint64(7)
	return thread.CompactQueryResult{State: thread.StateUsable, ConversationID: conversations.ConversationID(conv), Projection: protocol.ThreadProjectionCompact, Epoch: "epoch", Version: 90,
		Items:      []thread.Item{{ID: 1, Kind: "user", Order: 3, Rev: 1, Status: "done", Summary: "hi", Content: json.RawMessage(`{"text":"hi"}`)}},
		Headers:    []thread.ChildSummary{{ID: 2, Kind: "tool_call", Order: 4, Rev: 2, Status: "failed", Headline: "Read", DetailOmitted: true, ErrorSummary: &failed, PublicChildCount: &count, HasPublicChildren: &has, SubtreeVersion: &subtree}},
		LowerOrder: 3, UpperOrder: 91, Continuation: 3, OlderExists: true, FromVersion: from}
}

func testCompactPeer(t *testing.T, calls *testCompactCalls, caps []string, catchUp func(protocol.ThreadCatchUpPayload) thread.CompactQueryResult, page func(protocol.ThreadPagePayload) thread.CompactQueryResult) *testHostedPeer {
	t.Helper()
	return testHostedStartCaps(t, &testAppDiscovery{}, caps, testCompactConfig(calls, catchUp, page))
}

// testCompactConfig installs counted complete, expansion and compact providers.
func testCompactConfig(calls *testCompactCalls, catchUp func(protocol.ThreadCatchUpPayload) thread.CompactQueryResult, page func(protocol.ThreadPagePayload) thread.CompactQueryResult) func(*V2SessionConfig) {
	return func(c *V2SessionConfig) {
		c.ThreadReady = func() bool { return true }
		c.KnownConversation = func(id string) bool { return id == catchUpConversation || id == compactOther }
		c.ThreadCatchUp = func(_ context.Context, r protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
			calls.complete.Add(1)
			return thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, FromVersion: r.Version, LowerOrder: 1, UpperOrder: 91, Continuation: 1}, nil
		}
		c.ThreadPage = func(_ context.Context, r protocol.ThreadPagePayload) (thread.QueryResult, error) {
			calls.page.Add(1)
			return thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, UpperOrder: r.BeforeOrder}, nil
		}
		c.ThreadToolBody = func(context.Context, protocol.ThreadExpansionRequest) (thread.ToolBodyResult, error) {
			return testBodyResult(), nil
		}
		c.ThreadChildren = func(_ context.Context, r protocol.ThreadExpansionRequest) (thread.ChildPageResult, error) {
			out := testChildrenResult(r)
			out.Items = testCompactResult(catchUpConversation, 0).Headers
			out.Items[0].Parent = r.ItemID
			return out, nil
		}
		c.ThreadCompactCatchUp = func(_ context.Context, r protocol.ThreadCatchUpPayload) (thread.CompactQueryResult, error) {
			calls.compact.Add(1)
			calls.lastCatchUp.Store(&r)
			if catchUp != nil {
				return catchUp(r), nil
			}
			return testCompactResult(r.ConversationID, r.Version), nil
		}
		c.ThreadCompactPage = func(_ context.Context, r protocol.ThreadPagePayload) (thread.CompactQueryResult, error) {
			calls.compactPage.Add(1)
			calls.lastPage.Store(&r)
			if page != nil {
				return page(r), nil
			}
			return testCompactResult(r.ConversationID, 0), nil
		}
		c.ThreadCompactReady = func() bool { return true }
	}
}

func testCompactJoins(p *testHostedPeer) map[string]threadJoin {
	p.mgr.hostedMu.Lock()
	defer p.mgr.hostedMu.Unlock()
	return maps.Clone(p.mgr.hostedInboxes[p.conn].threadJoins)
}

func testCompactSeed(p *testHostedPeer, joins map[string]threadJoin) {
	p.mgr.hostedMu.Lock()
	defer p.mgr.hostedMu.Unlock()
	p.mgr.hostedInboxes[p.conn].threadJoins = maps.Clone(joins)
}

// testCompactReply reads one correlated reply, requiring the projection on every
// frame, and returns its terminal type, final marker and accumulated rows.
func testCompactReply(t *testing.T, p *testHostedPeer, id uint64, projection string) (string, protocol.ThreadItemsPayload, []protocol.ThreadItem, []protocol.ThreadSummary) {
	t.Helper()
	var items []protocol.ThreadItem
	var headers []protocol.ThreadSummary
	for {
		env, _ := p.nextBefore(time.Now().Add(5 * time.Second))
		if env.InReplyTo == nil || *env.InReplyTo != id {
			t.Fatalf("uncorrelated reply: %+v", env)
		}
		var scope struct {
			Projection string `json:"projection"`
		}
		if env.Type != protocol.TypeError && (json.Unmarshal(env.Payload, &scope) != nil || scope.Projection != projection) {
			t.Fatalf("frame scope %s: %s", env.Type, env.Payload)
		}
		if env.Type != protocol.TypeThreadItems {
			if env.Type == protocol.TypeError {
				t.Logf("query %d refusal: %s", id, env.Payload)
			}
			return env.Type, protocol.ThreadItemsPayload{}, nil, nil
		}
		var r protocol.ThreadItemsPayload
		if err := json.Unmarshal(env.Payload, &r); err != nil {
			t.Fatal(err)
		}
		if r.Final {
			return env.Type, r, items, headers
		}
		items, headers = append(items, r.Items...), append(headers, r.Headers...)
	}
}

func testCompactCatchUp(p *testHostedPeer, id uint64, conv string, version uint64, projection string) {
	if projection != "" {
		projection = fmt.Sprintf(`,"projection":%q`, projection)
	}
	p.request(id, protocol.TypeThreadCatchUp, fmt.Sprintf(`{"conversation_id":%q,"epoch":"epoch","version":%d,"limit":5%s}`, conv, version, projection))
}

func TestThreadCompactNegotiation(t *testing.T) {
	all := []string{protocol.CapabilityThreadCompact, protocol.CapabilityThreadExpansion, protocol.CapabilityThread, protocol.CapabilityInteractive}
	base := []string{protocol.CapabilityInteractive, protocol.CapabilityThread, protocol.CapabilityThreadExpansion}
	for _, tc := range []struct {
		name       string
		advertised []string
		readiness  []bool
		want       []string
	}{
		{"granted", all, []bool{true, false, true, true}, append(slices.Clone(base), protocol.CapabilityThreadCompact)},
		{"compact not ready", all, []bool{true, false, true, false}, base},
		{"readiness omitted", all, []bool{true, false, true}, base},
		{"not advertised", all[1:], []bool{true, false, true, true}, base},
		{"no expansion advertised", []string{protocol.CapabilityThreadCompact, protocol.CapabilityThread, protocol.CapabilityInteractive}, []bool{true, false, true, true}, base[:2]},
		{"expansion not ready", all, []bool{true, false, false, true}, base[:2]},
		{"thread not ready", all, []bool{false, false, true, true}, base[:1]},
		{"no interactive", all[:3], []bool{true, false, true, true}, []string{protocol.CapabilityThread}},
	} {
		if got := negotiateCapabilities(tc.advertised, tc.readiness...); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	for mask := 0; mask < 32; mask++ {
		t.Run(fmt.Sprint(mask), func(t *testing.T) {
			var calls testCompactCalls
			caps := compactCaps
			if mask&8 != 0 {
				caps = expansionCaps
			}
			p := testHostedStartCaps(t, &testAppDiscovery{}, caps, func(c *V2SessionConfig) {
				testCompactConfig(&calls, nil, nil)(c)
				if mask&1 != 0 {
					c.ThreadCompactCatchUp = nil
				}
				if mask&2 != 0 {
					c.ThreadCompactPage = nil
				}
				if mask&4 != 0 {
					c.ThreadCompactReady = func() bool { return false }
				}
				if mask&16 != 0 {
					c.ThreadCompactReady = nil
				}
			})
			granted := slices.Contains(p.ack.Capabilities, protocol.CapabilityThreadCompact)
			for _, c := range []string{protocol.CapabilityInteractive, protocol.CapabilityThread, protocol.CapabilityThreadExpansion} {
				if !slices.Contains(p.ack.Capabilities, c) {
					t.Fatalf("missing %s: %v", c, p.ack.Capabilities)
				}
			}
			if granted != (mask == 0) {
				t.Fatalf("compact grant: %v", p.ack.Capabilities)
			}
			testCompactCatchUp(p, 40, catchUpConversation, 0, protocol.ThreadProjectionCompact)
			if mask == 0 {
				if typ, _, _, _ := testCompactReply(t, p, 40, protocol.ThreadProjectionCompact); typ != protocol.TypeThreadItems {
					t.Fatal(typ)
				}
				return
			}
			testCatchUpError(t, p, 40)
			if calls.total() != 0 {
				t.Fatal("withheld compact reached a provider")
			}
		})
	}
}

func TestThreadCompactRefusals(t *testing.T) {
	compact := map[string]threadJoin{catchUpConversation: {"epoch", 20, protocol.ThreadProjectionCompact}}
	complete := map[string]threadJoin{catchUpConversation: {"epoch", 20, ""}}
	page := func(fields string) string {
		return fmt.Sprintf(`{"conversation_id":%q,"before_order":50,"limit":5%s}`, catchUpConversation, fields)
	}
	catchUp := func(conv string, version uint64, fields string) string {
		return fmt.Sprintf(`{"conversation_id":%q,"epoch":"epoch","version":%d,"limit":5%s}`, conv, version, fields)
	}
	for _, tc := range []struct {
		name, typ, payload string
		joins              map[string]threadJoin
	}{
		{"unknown projection", protocol.TypeThreadCatchUp, catchUp(catchUpConversation, 0, `,"projection":"compact-v2"`), nil},
		{"empty projection", protocol.TypeThreadCatchUp, catchUp(catchUpConversation, 0, `,"projection":""`), nil},
		{"null projection", protocol.TypeThreadCatchUp, catchUp(catchUpConversation, 0, `,"projection":null`), nil},
		{"numeric projection", protocol.TypeThreadPage, page(`,"projection":7,"epoch":"epoch"`), nil},
		{"unknown page projection", protocol.TypeThreadPage, page(`,"projection":"complete","epoch":"epoch"`), nil},
		{"page without epoch", protocol.TypeThreadPage, page(`,"projection":"compact-v1"`), nil},
		{"page empty epoch", protocol.TypeThreadPage, page(`,"projection":"compact-v1","epoch":""`), nil},
		{"page null epoch", protocol.TypeThreadPage, page(`,"projection":"compact-v1","epoch":null`), nil},
		{"unknown conversation", protocol.TypeThreadCatchUp, catchUp("33333333-3333-4333-8333-333333333333", 0, `,"projection":"compact-v1"`), nil},
		{"compact after complete join", protocol.TypeThreadCatchUp, catchUp(catchUpConversation, 20, `,"projection":"compact-v1"`), complete},
		{"complete after compact join", protocol.TypeThreadCatchUp, catchUp(catchUpConversation, 20, ``), compact},
		{"complete page on compact join", protocol.TypeThreadPage, page(``), compact},
		{"compact page on complete join", protocol.TypeThreadPage, page(`,"projection":"compact-v1","epoch":"epoch"`), complete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls testCompactCalls
			p := testCompactPeer(t, &calls, compactCaps, nil, nil)
			testCompactSeed(p, tc.joins)
			p.request(40, tc.typ, tc.payload)
			testCatchUpError(t, p, 40)
			if calls.total() != 0 {
				t.Fatal("refusal reached a provider")
			}
			if got := testCompactJoins(p); !maps.Equal(got, tc.joins) {
				t.Fatalf("joins changed: %v", got)
			}
		})
	}
	t.Run("no capability", func(t *testing.T) {
		var calls testCompactCalls
		p := testCompactPeer(t, &calls, expansionCaps, nil, nil)
		if slices.Contains(p.ack.Capabilities, protocol.CapabilityThreadCompact) {
			t.Fatal("unadvertised compact granted")
		}
		testCompactCatchUp(p, 40, catchUpConversation, 0, protocol.ThreadProjectionCompact)
		testCatchUpError(t, p, 40)
		p.request(41, protocol.TypeThreadPage, page(`,"projection":"compact-v1","epoch":"epoch"`))
		testCatchUpError(t, p, 41)
		if calls.total() != 0 || len(testCompactJoins(p)) != 0 {
			t.Fatal("refusal reached a provider or joins")
		}
		testCompactCatchUp(p, 42, catchUpConversation, 0, "")
		if typ, _, _, _ := testCompactReply(t, p, 42, ""); typ != protocol.TypeThreadItems || calls.complete.Load() != 1 || calls.total() != 1 {
			t.Fatal("complete request changed")
		}
	})
}

func TestThreadCompactReplies(t *testing.T) {
	wantItems := []protocol.ThreadItem{{ID: 1, Kind: "user", Order: 3, Rev: 1, Status: "done", Summary: "hi", Content: json.RawMessage(`{"text":"hi"}`)}}
	for _, tc := range []struct {
		name    string
		typ     string
		version uint64
		mutate  func(*thread.CompactQueryResult)
		reply   string
	}{
		{"window", protocol.TypeThreadCatchUp, 0, nil, protocol.TypeThreadItems},
		{"catch-up", protocol.TypeThreadCatchUp, 20, nil, protocol.TypeThreadItems},
		{"page", protocol.TypeThreadPage, 0, nil, protocol.TypeThreadItems},
		{"reset", protocol.TypeThreadCatchUp, 20, func(r *thread.CompactQueryResult) {
			*r = thread.CompactQueryResult{State: thread.StateUsable, ConversationID: r.ConversationID, Projection: r.Projection, Epoch: "new", Version: 100, ResetRequired: true}
		}, protocol.TypeThreadReset},
		{"page reset", protocol.TypeThreadPage, 0, func(r *thread.CompactQueryResult) { r.ResetRequired = true }, protocol.TypeThreadReset},
		{"page epoch moved", protocol.TypeThreadPage, 0, func(r *thread.CompactQueryResult) { r.Epoch = "new" }, protocol.TypeThreadReset},
		{"mislabelled", protocol.TypeThreadCatchUp, 0, func(r *thread.CompactQueryResult) { r.Projection = "" }, protocol.TypeError},
		{"other conversation", protocol.TypeThreadCatchUp, 0, func(r *thread.CompactQueryResult) { r.ConversationID = compactOther }, protocol.TypeError},
		{"unusable", protocol.TypeThreadCatchUp, 20, func(r *thread.CompactQueryResult) { *r = thread.CompactQueryResult{State: thread.StateRebuilding} }, protocol.TypeError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls testCompactCalls
			mutate := func(r thread.CompactQueryResult) thread.CompactQueryResult {
				if tc.mutate != nil {
					tc.mutate(&r)
				}
				return r
			}
			p := testCompactPeer(t, &calls, compactCaps, func(r protocol.ThreadCatchUpPayload) thread.CompactQueryResult {
				return mutate(testCompactResult(r.ConversationID, r.Version))
			}, func(r protocol.ThreadPagePayload) thread.CompactQueryResult {
				return mutate(testCompactResult(r.ConversationID, 0))
			})
			if tc.typ == protocol.TypeThreadPage {
				p.request(40, tc.typ, fmt.Sprintf(`{"conversation_id":%q,"before_order":50,"limit":5,"projection":"compact-v1","epoch":"epoch"}`, catchUpConversation))
			} else {
				testCompactCatchUp(p, 40, catchUpConversation, tc.version, protocol.ThreadProjectionCompact)
			}
			if tc.reply == protocol.TypeError {
				testCatchUpError(t, p, 40)
				if len(testCompactJoins(p)) != 0 {
					t.Fatal("refusal recorded a join")
				}
				return
			}
			typ, final, items, headers := testCompactReply(t, p, 40, protocol.ThreadProjectionCompact)
			if typ != tc.reply || calls.compact.Load()+calls.compactPage.Load() != 1 || calls.complete.Load()+calls.page.Load() != 0 {
				t.Fatalf("reply %s calls %d", typ, calls.total())
			}
			if tc.typ == protocol.TypeThreadPage {
				if r := calls.lastPage.Load(); r.Projection != protocol.ThreadProjectionCompact || r.Epoch != "epoch" || r.BeforeOrder != 50 || r.Limit != 5 {
					t.Fatalf("page request: %+v", r)
				}
			} else if r := calls.lastCatchUp.Load(); r.Projection != protocol.ThreadProjectionCompact || r.Epoch != "epoch" || r.Version != tc.version || r.Limit != 5 {
				t.Fatalf("catch-up request: %+v", r)
			}
			if typ == protocol.TypeThreadReset {
				if len(testCompactJoins(p)) != 0 {
					t.Fatal("reset recorded a join")
				}
				return
			}
			if !reflect.DeepEqual(items, wantItems) || len(headers) != 1 {
				t.Fatalf("rows: %+v %+v", items, headers)
			}
			h := headers[0]
			if h.ID != 2 || h.Kind != "tool_call" || h.Headline != "Read" || !h.DetailOmitted || h.ErrorSummary == nil || *h.ErrorSummary != "exit 1" || h.PublicChildCount == nil || *h.PublicChildCount != 2 || h.HasPublicChildren == nil || !*h.HasPublicChildren || h.SubtreeVersion == nil || *h.SubtreeVersion != 7 {
				t.Fatalf("header facts: %+v", h)
			}
			if len(final.Items) != 0 || len(final.Headers) != 0 || final.Epoch != "epoch" || final.Version != 90 {
				t.Fatalf("final marker: %+v", final)
			}
			if tc.version > 0 {
				if final.FromVersion == nil || *final.FromVersion != 20 || final.LowerOrder != nil {
					t.Fatal("catch-up coverage")
				}
			} else if final.LowerOrder == nil || *final.LowerOrder != 3 || *final.UpperOrder != 91 || *final.Continuation != 3 || !*final.OlderExists || final.FromVersion != nil {
				t.Fatal("window bounds")
			}
			if got := testCompactJoins(p); !maps.Equal(got, map[string]threadJoin{catchUpConversation: {"epoch", 90, protocol.ThreadProjectionCompact}}) {
				t.Fatalf("join: %v", got)
			}
		})
	}
}

func testCompactItemChanged(conv string, version uint64) protocol.Envelope {
	return protocol.Envelope{Type: protocol.TypeThreadItemChanged, Payload: json.RawMessage(fmt.Sprintf(`{"conversation_id":%q,"epoch":"epoch","version":%d,"item":{"id":1,"kind":"user","rev":2,"status":"done","active":false,"shown":true,"summary":"","content":{}}}`, conv, version))}
}

func testCompactLive(t *testing.T, p *testHostedPeer, conv string, version uint64) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- p.mgr.DeliverThread(t.Context(), p.conn, testCompactItemChanged(conv, version))
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("live delivery stranded")
		return nil
	}
}

// TestThreadCompactLiveInFlight pins how a complete live item update meets an
// in-flight catch-up of its conversation, through both the ordered DeliverThread
// lane and the legacy Push path (which cancels before queueing). A compact load is
// neither cancelled nor interleaved with complete rows; a complete re-initialization
// over a compact join is still cancelled, and its live row stays withheld.
func TestThreadCompactLiveInFlight(t *testing.T) {
	compactJoin := map[string]threadJoin{catchUpConversation: {"epoch", 20, protocol.ThreadProjectionCompact}}
	for _, push := range []bool{false, true} {
		for _, tc := range []struct {
			name    string
			compact bool
			joins   map[string]threadJoin
		}{
			{"compact load", true, nil},
			{"compact over complete join", true, map[string]threadJoin{catchUpConversation: {"epoch", 20, ""}}},
			{"complete over compact join", false, compactJoin},
		} {
			t.Run(fmt.Sprintf("%s push=%v", tc.name, push), func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var calls testCompactCalls
				var n atomic.Int32
				// Only the first provider call blocks, until released or cancelled.
				wait := func(ctx context.Context) error {
					if n.Add(1) != 1 {
						return nil
					}
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
					}
					return ctx.Err()
				}
				p := testHostedStartCaps(t, &testAppDiscovery{}, compactCaps, func(c *V2SessionConfig) {
					testCompactConfig(&calls, nil, nil)(c)
					c.ThreadCompactCatchUp = func(ctx context.Context, r protocol.ThreadCatchUpPayload) (thread.CompactQueryResult, error) {
						if err := wait(ctx); err != nil {
							return thread.CompactQueryResult{}, err
						}
						return testCompactResult(r.ConversationID, r.Version), nil
					}
					c.ThreadCatchUp = func(ctx context.Context, _ protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
						if err := wait(ctx); err != nil {
							return thread.QueryResult{}, err
						}
						return thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, LowerOrder: 1, UpperOrder: 91, Continuation: 1}, nil
					}
				})
				testCompactSeed(p, tc.joins)
				projection := ""
				if tc.compact {
					projection = protocol.ThreadProjectionCompact
				}
				testCompactCatchUp(p, 40, catchUpConversation, 0, projection)
				testHostedAwait(t, entered)
				if push {
					if err := p.mgr.Push(t.Context(), p.conn, testCompactItemChanged(catchUpConversation, 91)); err != nil {
						t.Fatal(err)
					}
					if err := p.mgr.FlushPushes(t.Context()); err != nil {
						t.Fatal(err)
					}
					testCompactHostedFence(t, p)
				} else if err := testCompactLive(t, p, catchUpConversation, 91); err != ErrThreadWithheld {
					t.Fatalf("complete live during query: %v", err)
				}
				close(release)
				if tc.compact {
					// An interleaved live row fails here as an uncorrelated frame.
					if typ, final, _, _ := testCompactReply(t, p, 40, projection); typ != protocol.TypeThreadItems || final.Version != 90 {
						t.Fatalf("compact load did not complete: %s", typ)
					}
					if got := testCompactJoins(p); !maps.Equal(got, map[string]threadJoin{catchUpConversation: {"epoch", 90, protocol.ThreadProjectionCompact}}) {
						t.Fatalf("compact join: %v", got)
					}
				} else {
					testCatchUpError(t, p, 40)
					if got := testCompactJoins(p); !maps.Equal(got, compactJoin) {
						t.Fatalf("cancelled re-initialization changed joins: %v", got)
					}
					testCompactCatchUp(p, 41, catchUpConversation, 0, "")
					if typ, final, _, _ := testCompactReply(t, p, 41, ""); typ != protocol.TypeThreadItems || !final.Final || final.ConversationID != catchUpConversation || final.Epoch != "epoch" || final.Version != 90 || n.Load() != 2 {
						t.Fatalf("retry: %s final=%+v provider calls=%d", typ, final, n.Load())
					}
					if got := testCompactJoins(p); !maps.Equal(got, map[string]threadJoin{catchUpConversation: {"epoch", 90, ""}}) {
						t.Fatalf("re-initialized join: %v", got)
					}
				}
				// Any reply correlated to this sentinel proves no live row was queued before it.
				p.request(50, protocol.TypeAppCancel, `{"request_id":999}`)
				if e, _ := p.next(); e.InReplyTo == nil || *e.InReplyTo != 50 {
					t.Fatalf("complete live row reached the client: %+v", e)
				}
			})
		}
	}
}

func TestThreadCompactJoins(t *testing.T) {
	var calls testCompactCalls
	// A nonzero catch-up advances the version, so the repeated query below moves the join.
	p := testCompactPeer(t, &calls, compactCaps, func(r protocol.ThreadCatchUpPayload) thread.CompactQueryResult {
		out := testCompactResult(r.ConversationID, r.Version)
		if r.Version > 0 {
			out.Version = r.Version + 5
		}
		return out
	}, nil)
	testCompactCatchUp(p, 40, catchUpConversation, 0, protocol.ThreadProjectionCompact)
	testCompactReply(t, p, 40, protocol.ThreadProjectionCompact)
	testCompactCatchUp(p, 41, compactOther, 0, "")
	if typ, _, _, _ := testCompactReply(t, p, 41, ""); typ != protocol.TypeThreadItems {
		t.Fatal(typ)
	}
	want := map[string]threadJoin{catchUpConversation: {"epoch", 90, protocol.ThreadProjectionCompact}, compactOther: {"epoch", 90, ""}}
	if got := testCompactJoins(p); !maps.Equal(got, want) {
		t.Fatalf("mixed joins: %v", got)
	}

	if err := testCompactLive(t, p, catchUpConversation, 91); err != ErrThreadWithheld {
		t.Fatalf("complete live on compact join: %v", err)
	}
	if err := testCompactLive(t, p, compactOther, 91); err != nil {
		t.Fatalf("complete live on complete join: %v", err)
	}
	if env, _ := p.next(); env.Type != protocol.TypeThreadItemChanged || env.InReplyTo != nil {
		t.Fatalf("live: %+v", env)
	}

	p.request(42, protocol.TypeThreadReadToolBody, testBodyPayload)
	testBodyRead(t, p, 42)
	p.request(43, protocol.TypeThreadReadChildren, testBodyPayload)
	children, _ := testChildrenRead(t, p, 43)
	if len(children.Children) != 1 || children.Children[0].SubtreeVersion == nil || *children.Children[0].SubtreeVersion != 7 || *children.Children[0].PublicChildCount != 2 || !*children.Children[0].HasPublicChildren || *children.Children[0].ErrorSummary != "exit 1" {
		t.Fatalf("children header facts: %+v", children.Children)
	}
	if got := testCompactJoins(p); !maps.Equal(got, want) {
		t.Fatalf("expansion changed joins: %v", got)
	}

	testCompactCatchUp(p, 44, catchUpConversation, 90, protocol.ThreadProjectionCompact)
	if _, final, _, _ := testCompactReply(t, p, 44, protocol.ThreadProjectionCompact); final.FromVersion == nil || *final.FromVersion != 90 || final.Version != 95 || calls.compact.Load() != 2 {
		t.Fatal("matching compact catch-up")
	}
	want[catchUpConversation] = threadJoin{"epoch", 95, protocol.ThreadProjectionCompact}
	if got := testCompactJoins(p); !maps.Equal(got, want) {
		t.Fatalf("repeated compact join: %v", got)
	}
	testCompactCatchUp(p, 45, catchUpConversation, 0, "")
	testCompactReply(t, p, 45, "")
	want[catchUpConversation] = threadJoin{"epoch", 90, ""}
	if got := testCompactJoins(p); !maps.Equal(got, want) || calls.complete.Load() != 2 {
		t.Fatalf("version-zero re-initialization: %v", got)
	}

	// A fresh connection holds no join; the Store decides between catch-up and reset.
	var fresh testCompactCalls
	q := testCompactPeer(t, &fresh, compactCaps, func(r protocol.ThreadCatchUpPayload) thread.CompactQueryResult {
		return thread.CompactQueryResult{State: thread.StateUsable, ConversationID: conversations.ConversationID(r.ConversationID), Projection: protocol.ThreadProjectionCompact, Epoch: "new", Version: 100, ResetRequired: true}
	}, nil)
	testCompactCatchUp(q, 40, catchUpConversation, 90, protocol.ThreadProjectionCompact)
	if typ, _, _, _ := testCompactReply(t, q, 40, protocol.ThreadProjectionCompact); typ != protocol.TypeThreadReset || fresh.compact.Load() != 1 || len(testCompactJoins(q)) != 0 {
		t.Fatal("reconnect compact catch-up")
	}
}
