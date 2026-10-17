package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

const compactNonMember = "33333333-3333-4333-8333-333333333333"

// testCompactUpdate encodes one compact live frame; kind is item, header or progress.
func testCompactUpdate(t *testing.T, conv, epoch string, version uint64, kind string) protocol.Envelope {
	t.Helper()
	u := protocol.ThreadCompactUpdatePayload{Projection: protocol.ThreadProjectionCompact, ConversationID: conv, Epoch: epoch, FromVersion: version - 1, Version: version}
	switch kind {
	case "item":
		u.Item = &protocol.ThreadItem{ID: 1, Kind: "user", Order: 3, Rev: 2, Status: "done", Shown: true, Summary: "hi", Content: json.RawMessage(`{"text":"hi"}`)}
	case "header":
		u.Header = &protocol.ThreadSummary{ID: 2, Kind: "tool_call", Order: 4, Rev: 3, Status: "done", Headline: "Read", DetailOmitted: true}
	default:
		u.Progress = true
	}
	frames, err := protocol.EncodeThreadUpdate(protocol.Envelope{}, u)
	if err != nil || len(frames) != 1 {
		t.Fatalf("encode compact update: %v", err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(frames[0], &env); err != nil {
		t.Fatal(err)
	}
	return env
}

func testDeliverLive(t *testing.T, p *testHostedPeer, env protocol.Envelope) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.mgr.DeliverThread(t.Context(), p.conn, env) }()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("live delivery stranded")
		return nil
	}
}

// testCompactNext requires the next frame on p to be the compact update at version
// carrying kind, and nothing else in between.
func testCompactNext(t *testing.T, p *testHostedPeer, version uint64, kind string) {
	t.Helper()
	env, _ := p.next()
	var u protocol.ThreadCompactUpdatePayload
	if env.Type != protocol.TypeThreadCompactUpdate || env.InReplyTo != nil || json.Unmarshal(env.Payload, &u) != nil || u.Version != version {
		t.Fatalf("compact live: %s %s", env.Type, env.Payload)
	}
	got := "progress"
	if u.Item != nil {
		got = "item"
	} else if u.Header != nil {
		got = "header"
	}
	if got != kind || (u.Item != nil) != (kind == "item") || (u.Header != nil) != (kind == "header") || u.Progress != (kind == "progress") {
		t.Fatalf("compact live kind %q, want %q", got, kind)
	}
}

// testCompactQuiet proves no frame was queued for p before the sentinel reply,
// and that every earlier frame left the nonce sequence intact.
func testCompactQuiet(t *testing.T, p *testHostedPeer, id uint64) {
	t.Helper()
	p.request(id, protocol.TypeAppCancel, `{"request_id":999}`)
	if e, _ := p.next(); e.InReplyTo == nil || *e.InReplyTo != id {
		t.Fatalf("unexpected frame before sentinel: %s %s", e.Type, e.Payload)
	}
}

func testCompactSibling(p *testHostedPeer, conn string) *testHostedPeer {
	q := &testHostedPeer{t: p.t, mgr: p.mgr, frames: p.frames, rec: p.rec, conn: conn, pub: p.pub, events: p.events}
	q.open(compactCaps)
	return q
}

// testCompactJoin initializes conv on p in projection and returns the final.
func testCompactJoin(t *testing.T, p *testHostedPeer, id uint64, conv, projection string) protocol.ThreadItemsPayload {
	t.Helper()
	testCompactCatchUp(p, id, conv, 0, projection)
	typ, final, _, _ := testCompactReply(t, p, id, projection)
	if typ != protocol.TypeThreadItems {
		t.Fatalf("join %s: %s", conv, typ)
	}
	return final
}

func testThreadViews(t *testing.T, p *testHostedPeer, conv string) map[string]ThreadView {
	t.Helper()
	out := map[string]ThreadView{}
	for _, c := range p.mgr.ActiveThreadConns(t.Context(), conv) {
		out[c.ConnID] = c.View
	}
	return out
}

// testCompactHold blocks only the first call until released or cancelled.
func testCompactHold() (wait func(context.Context) error, entered, release chan struct{}) {
	entered, release = make(chan struct{}), make(chan struct{})
	var n atomic.Int32
	return func(ctx context.Context) error {
		if n.Add(1) != 1 {
			return nil
		}
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return ctx.Err()
	}, entered, release
}

// TestThreadCompactLiveRouting pins per-recipient selection: each connection
// receives only its own projection's frames for each conversation, and
// ActiveThreadConns reports that selection.
func TestThreadCompactLiveRouting(t *testing.T) {
	var calls testCompactCalls
	p := testCompactPeer(t, &calls, compactCaps, nil, nil)
	q := testCompactSibling(p, "complete-peer")
	r := testCompactSibling(p, "unjoined-peer")
	testCompactJoin(t, p, 40, catchUpConversation, protocol.ThreadProjectionCompact)
	testCompactJoin(t, p, 41, compactOther, "")
	testCompactJoin(t, q, 40, catchUpConversation, "")
	testCompactJoin(t, r, 40, compactOther, "")

	if got, want := testThreadViews(t, p, catchUpConversation), map[string]ThreadView{p.conn: ThreadViewCompact, q.conn: ThreadViewComplete, r.conn: ThreadViewUnjoined}; !maps.Equal(got, want) {
		t.Fatalf("views on compact conversation: %v", got)
	}
	if got, want := testThreadViews(t, p, compactOther), map[string]ThreadView{p.conn: ThreadViewComplete, q.conn: ThreadViewUnjoined, r.conn: ThreadViewComplete}; !maps.Equal(got, want) {
		t.Fatalf("views on complete conversation: %v", got)
	}
	if got := testThreadViews(t, p, compactNonMember); len(got) != 0 {
		t.Fatalf("non-member eligible: %v", got)
	}
	for _, c := range p.mgr.ActiveConns(t.Context()) {
		if c.View != ThreadViewUnjoined {
			t.Fatalf("ActiveConns reports a view: %+v", c.ConnID)
		}
	}

	for _, kind := range []string{"item", "header", "progress"} {
		update := testCompactUpdate(t, catchUpConversation, "epoch", 91, kind)
		if err := testDeliverLive(t, p, update); err != nil {
			t.Fatalf("compact %s to compact view: %v", kind, err)
		}
		testCompactNext(t, p, 91, kind)
		for _, other := range []*testHostedPeer{q, r} {
			if err := testDeliverLive(t, other, update); err != ErrThreadWithheld {
				t.Fatalf("compact %s to %s: %v", kind, other.conn, err)
			}
		}
	}
	complete := testCompactItemChanged(catchUpConversation, 91)
	if err := testDeliverLive(t, q, complete); err != nil {
		t.Fatalf("complete to complete view: %v", err)
	}
	if env, _ := q.next(); env.Type != protocol.TypeThreadItemChanged {
		t.Fatalf("complete live: %s", env.Type)
	}
	if err := testDeliverLive(t, p, complete); err != ErrThreadWithheld {
		t.Fatalf("complete to compact view: %v", err)
	}

	// One connection, compact on one conversation and complete on another.
	if err := testDeliverLive(t, p, testCompactUpdate(t, compactOther, "epoch", 91, "item")); err != ErrThreadWithheld {
		t.Fatalf("compact to complete conversation: %v", err)
	}
	if err := testDeliverLive(t, p, testCompactItemChanged(compactOther, 91)); err != nil {
		t.Fatalf("complete to complete conversation: %v", err)
	}
	if env, _ := p.next(); env.Type != protocol.TypeThreadItemChanged {
		t.Fatalf("complete live on other conversation: %s", env.Type)
	}
	for i, peer := range []*testHostedPeer{p, q, r} {
		testCompactQuiet(t, peer, uint64(60+i))
	}
}

// TestThreadCompactLiveFencing pins the join gate: compact live is delivered
// only above the recorded compact join of its epoch, never advances that join,
// and every withheld frame leaves the nonce sequence intact.
func TestThreadCompactLiveFencing(t *testing.T) {
	var calls testCompactCalls
	p := testCompactPeer(t, &calls, compactCaps, nil, nil)
	if final := testCompactJoin(t, p, 40, catchUpConversation, protocol.ThreadProjectionCompact); final.Version != 90 {
		t.Fatal(final.Version)
	}
	want := map[string]threadJoin{catchUpConversation: {"epoch", 90, protocol.ThreadProjectionCompact}}

	unscoped := testCompactUpdate(t, catchUpConversation, "epoch", 91, "item")
	unscoped.Payload = replaceObjectField(unscoped.Payload, "projection", nil)
	foreign := testCompactUpdate(t, catchUpConversation, "epoch", 91, "item")
	foreign.Payload = replaceObjectField(foreign.Payload, "projection", json.RawMessage(`"complete-v1"`))
	reply := testCompactUpdate(t, compactOther, "epoch", 91, "item")
	id := uint64(7)
	reply.InReplyTo = &id
	for name, env := range map[string]protocol.Envelope{
		"equal version":    testCompactUpdate(t, catchUpConversation, "epoch", 90, "item"),
		"older version":    testCompactUpdate(t, catchUpConversation, "epoch", 89, "header"),
		"replaced epoch":   testCompactUpdate(t, catchUpConversation, "other", 91, "item"),
		"no projection":    unscoped,
		"other projection": foreign,
		"unjoined reply":   reply,
		"non-member":       testCompactUpdate(t, compactNonMember, "epoch", 91, "item"),
	} {
		if err := testDeliverLive(t, p, env); err != ErrThreadWithheld {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Different roots consumed at one version are not fenced against each other.
	for _, kind := range []string{"item", "header", "progress"} {
		if err := testDeliverLive(t, p, testCompactUpdate(t, catchUpConversation, "epoch", 91, kind)); err != nil {
			t.Fatalf("%s at shared version: %v", kind, err)
		}
		testCompactNext(t, p, 91, kind)
	}
	if got := testCompactJoins(p); !maps.Equal(got, want) {
		t.Fatalf("live delivery moved the join: %v", got)
	}
	testCompactQuiet(t, p, 50)

	// Without thread_compact a compact join cannot be honoured.
	var plain testCompactCalls
	e := testCompactPeer(t, &plain, expansionCaps, nil, nil)
	testCompactJoin(t, e, 40, catchUpConversation, "")
	testCompactSeed(e, want)
	if err := testDeliverLive(t, e, testCompactUpdate(t, catchUpConversation, "epoch", 91, "item")); err != ErrThreadWithheld {
		t.Fatalf("compact without capability: %v", err)
	}
	testCompactQuiet(t, e, 50)

	// The legacy Push path seals directly without an inbox, so it carries no compact frame.
	var fresh testCompactCalls
	f := testCompactPeer(t, &fresh, compactCaps, nil, nil)
	if err := f.mgr.Push(t.Context(), f.conn, testCompactUpdate(t, catchUpConversation, "epoch", 91, "item")); err != nil {
		t.Fatal(err)
	}
	if err := f.mgr.FlushPushes(t.Context()); err != nil {
		t.Fatal(err)
	}
	testCompactQuiet(t, f, 50)
}

// TestThreadCompactLiveQueryRace pins compact live against in-flight reads of
// its conversation, through DeliverThread and the legacy Push path.
func TestThreadCompactLiveQueryRace(t *testing.T) {
	seeded := map[string]threadJoin{catchUpConversation: {"epoch", 20, protocol.ThreadProjectionCompact}}
	send := func(t *testing.T, p *testHostedPeer, push bool, env protocol.Envelope) error {
		t.Helper()
		if !push {
			return testDeliverLive(t, p, env)
		}
		if err := p.mgr.Push(t.Context(), p.conn, env); err != nil {
			t.Fatal(err)
		}
		return p.mgr.FlushPushes(t.Context())
	}
	for _, push := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact load cancelled push=%v", push), func(t *testing.T) {
			wait, entered, release := testCompactHold()
			defer close(release)
			var calls testCompactCalls
			p := testHostedStartCaps(t, &testAppDiscovery{}, compactCaps, func(c *V2SessionConfig) {
				testCompactConfig(&calls, nil, nil)(c)
				c.ThreadCompactCatchUp = func(ctx context.Context, r protocol.ThreadCatchUpPayload) (thread.CompactQueryResult, error) {
					if err := wait(ctx); err != nil {
						return thread.CompactQueryResult{}, err
					}
					return testCompactResult(r.ConversationID, r.Version), nil
				}
			})
			testCompactJoin(t, p, 30, compactOther, "")
			testCompactSeed(p, map[string]threadJoin{catchUpConversation: seeded[catchUpConversation], compactOther: {"epoch", 90, ""}})
			testCompactCatchUp(p, 40, catchUpConversation, 20, protocol.ThreadProjectionCompact)
			testHostedAwait(t, entered)
			if err := send(t, p, push, testCompactUpdate(t, catchUpConversation, "epoch", 21, "item")); err != nil {
				t.Fatal(err)
			}
			var failed, live bool
			for range 2 {
				e, _ := p.next()
				switch e.Type {
				case protocol.TypeError:
					failed = e.InReplyTo != nil && *e.InReplyTo == 40
				case protocol.TypeThreadCompactUpdate:
					live = true
				default:
					t.Fatalf("race falsely completed: %s", e.Type)
				}
			}
			if !failed || !live {
				t.Fatal("missing cancelled load or live frame")
			}
			if got := testCompactJoins(p)[catchUpConversation]; got != seeded[catchUpConversation] {
				t.Fatalf("cancelled load or live moved the join: %v", got)
			}
			// Final first: the retry records the join and fences the stale update.
			testCompactCatchUp(p, 41, catchUpConversation, 20, protocol.ThreadProjectionCompact)
			if typ, final, _, _ := testCompactReply(t, p, 41, protocol.ThreadProjectionCompact); typ != protocol.TypeThreadItems || final.Version != 90 {
				t.Fatalf("retry: %s", typ)
			}
			if err := send(t, p, push, testCompactUpdate(t, catchUpConversation, "epoch", 90, "item")); err != ErrThreadWithheld && !(push && err == nil) {
				t.Fatalf("stale after final: %v", err)
			}
			if err := send(t, p, push, testCompactUpdate(t, catchUpConversation, "epoch", 91, "progress")); err != nil {
				t.Fatal(err)
			}
			testCompactNext(t, p, 91, "progress")
			testCompactQuiet(t, p, 50)
		})

		t.Run(fmt.Sprintf("complete load withholds push=%v", push), func(t *testing.T) {
			wait, entered, release := testCompactHold()
			var calls testCompactCalls
			p := testHostedStartCaps(t, &testAppDiscovery{}, compactCaps, func(c *V2SessionConfig) {
				testCompactConfig(&calls, nil, nil)(c)
				c.ThreadCatchUp = func(ctx context.Context, _ protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
					if err := wait(ctx); err != nil {
						return thread.QueryResult{}, err
					}
					return thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 90, LowerOrder: 1, UpperOrder: 91, Continuation: 1}, nil
				}
			})
			testCompactJoin(t, p, 30, compactOther, protocol.ThreadProjectionCompact)
			testCompactSeed(p, map[string]threadJoin{catchUpConversation: seeded[catchUpConversation], compactOther: {"epoch", 90, protocol.ThreadProjectionCompact}})
			testCompactCatchUp(p, 40, catchUpConversation, 0, "")
			testHostedAwait(t, entered)
			if err := send(t, p, push, testCompactUpdate(t, catchUpConversation, "epoch", 21, "item")); err != ErrThreadWithheld && !(push && err == nil) {
				t.Fatalf("compact live during complete load: %v", err)
			}
			close(release)
			// An interleaved compact row fails here as an uncorrelated frame.
			if typ, _, _, _ := testCompactReply(t, p, 40, ""); typ != protocol.TypeThreadItems {
				t.Fatalf("complete load did not complete: %s", typ)
			}
			if got := testCompactJoins(p)[catchUpConversation]; got != (threadJoin{"epoch", 90, ""}) {
				t.Fatalf("complete join: %v", got)
			}
			testCompactQuiet(t, p, 50)
		})

		t.Run(fmt.Sprintf("body read neither cancelled nor joined push=%v", push), func(t *testing.T) {
			wait, entered, release := testCompactHold()
			var calls testCompactCalls
			p := testHostedStartCaps(t, &testAppDiscovery{}, compactCaps, func(c *V2SessionConfig) {
				testCompactConfig(&calls, nil, nil)(c)
				c.ThreadToolBody = func(ctx context.Context, _ protocol.ThreadExpansionRequest) (thread.ToolBodyResult, error) {
					if err := wait(ctx); err != nil {
						return thread.ToolBodyResult{}, err
					}
					return testBodyResult(), nil
				}
			})
			testCompactJoin(t, p, 30, compactOther, "")
			testCompactSeed(p, seeded)
			p.request(42, protocol.TypeThreadReadToolBody, testBodyPayload)
			testHostedAwait(t, entered)
			if err := send(t, p, push, testCompactUpdate(t, catchUpConversation, "epoch", 21, "header")); err != nil {
				t.Fatal(err)
			}
			testCompactNext(t, p, 21, "header")
			close(release)
			testBodyRead(t, p, 42)
			if got := testCompactJoins(p); !maps.Equal(got, seeded) {
				t.Fatalf("body read or live moved joins: %v", got)
			}
			testCompactQuiet(t, p, 50)
		})
	}
}

// TestThreadCompactLiveScope pins that an epoch reset and a reconnect release
// only their own conversation and connection.
func TestThreadCompactLiveScope(t *testing.T) {
	t.Run("epoch reset", func(t *testing.T) {
		var reset atomic.Bool
		var calls testCompactCalls
		p := testCompactPeer(t, &calls, compactCaps, func(r protocol.ThreadCatchUpPayload) thread.CompactQueryResult {
			out := testCompactResult(r.ConversationID, r.Version)
			if r.ConversationID == catchUpConversation && reset.Load() {
				out.Epoch, out.Version, out.ResetRequired = "new", 100, r.Version > 0
			}
			return out
		}, nil)
		testCompactJoin(t, p, 40, catchUpConversation, protocol.ThreadProjectionCompact)
		testCompactJoin(t, p, 41, compactOther, protocol.ThreadProjectionCompact)
		reset.Store(true)
		testCompactCatchUp(p, 42, catchUpConversation, 90, protocol.ThreadProjectionCompact)
		if typ, _, _, _ := testCompactReply(t, p, 42, protocol.ThreadProjectionCompact); typ != protocol.TypeThreadReset {
			t.Fatalf("reset: %s", typ)
		}
		if err := testDeliverLive(t, p, testCompactUpdate(t, catchUpConversation, "new", 101, "item")); err != ErrThreadWithheld {
			t.Fatalf("new epoch before re-initialization: %v", err)
		}
		if err := testDeliverLive(t, p, testCompactUpdate(t, compactOther, "epoch", 91, "item")); err != nil {
			t.Fatalf("other conversation after reset: %v", err)
		}
		testCompactNext(t, p, 91, "item")
		if final := testCompactJoin(t, p, 43, catchUpConversation, protocol.ThreadProjectionCompact); final.Epoch != "new" {
			t.Fatal(final.Epoch)
		}
		if err := testDeliverLive(t, p, testCompactUpdate(t, catchUpConversation, "epoch", 120, "item")); err != ErrThreadWithheld {
			t.Fatalf("replaced epoch after re-initialization: %v", err)
		}
		if err := testDeliverLive(t, p, testCompactUpdate(t, catchUpConversation, "new", 101, "item")); err != nil {
			t.Fatalf("new epoch after re-initialization: %v", err)
		}
		testCompactNext(t, p, 101, "item")
		want := map[string]threadJoin{catchUpConversation: {"new", 100, protocol.ThreadProjectionCompact}, compactOther: {"epoch", 90, protocol.ThreadProjectionCompact}}
		if got := testCompactJoins(p); !maps.Equal(got, want) {
			t.Fatalf("joins after reset: %v", got)
		}
		testCompactQuiet(t, p, 50)
	})

	t.Run("reconnect", func(t *testing.T) {
		var calls testCompactCalls
		p := testCompactPeer(t, &calls, compactCaps, nil, nil)
		q := testCompactSibling(p, "other-device")
		testCompactJoin(t, p, 40, catchUpConversation, protocol.ThreadProjectionCompact)
		testCompactJoin(t, q, 40, catchUpConversation, protocol.ThreadProjectionCompact)
		p.frames <- protocol.RoutingEnvelope{ConnID: p.conn, CloseCode: 1001}
		deadline := time.Now().Add(3 * time.Second)
		for testThreadViews(t, q, catchUpConversation)[p.conn] == ThreadViewCompact {
			if time.Now().After(deadline) {
				t.Fatal("closed conn still eligible")
			}
			time.Sleep(time.Millisecond)
		}
		// The same conn ID reconnects: a fresh session holds no join.
		p.open(compactCaps)
		testCompactJoin(t, p, 41, compactOther, "")
		if got := testThreadViews(t, q, catchUpConversation); got[p.conn] != ThreadViewUnjoined || got[q.conn] != ThreadViewCompact {
			t.Fatalf("views after reconnect: %v", got)
		}
		update := testCompactUpdate(t, catchUpConversation, "epoch", 91, "item")
		if err := testDeliverLive(t, p, update); err != ErrThreadWithheld {
			t.Fatalf("compact to reconnected conn: %v", err)
		}
		if err := testDeliverLive(t, q, update); err != nil {
			t.Fatalf("other connection after reconnect: %v", err)
		}
		testCompactNext(t, q, 91, "item")
		testCompactJoin(t, p, 42, catchUpConversation, protocol.ThreadProjectionCompact)
		if err := testDeliverLive(t, p, update); err != nil {
			t.Fatalf("compact after re-join: %v", err)
		}
		testCompactNext(t, p, 91, "item")
		if got := testCompactJoins(q); !maps.Equal(got, map[string]threadJoin{catchUpConversation: {"epoch", 90, protocol.ThreadProjectionCompact}}) {
			t.Fatalf("other connection's join: %v", got)
		}
		testCompactQuiet(t, p, 50)
		testCompactQuiet(t, q, 50)
	})
}
