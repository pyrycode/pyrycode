package relay

import (
	"bytes"
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestHostedAPIMutationReconnect(t *testing.T) {
	for _, action := range []string{"cancel", "timeout", "disconnect"} {
		t.Run(action, func(t *testing.T) {
			committed, release := make(chan struct{}, 1), make(chan struct{})
			var calls atomic.Int32
			p := testHostedStart(t, &testAppDiscovery{}, func(c *V2SessionConfig) {
				c.hostedInactivity = 250 * time.Millisecond
				c.hostedForwardAPI = func(ctx context.Context, a, b, r string, in apps.APIRequest) (hostedResponse, error) {
					if calls.Add(1) != 1 {
						return testAPIPrepare([]byte("fresh"), 200)(ctx, a, b, r, in)
					}
					if string(in.Body) != "committed" {
						t.Error("mutation bytes changed")
					}
					// A complete fake retains a committed response while ignoring cancellation.
					q, err := testAPIPrepare([]byte("stale"), 200)(context.Background(), a, b, r, in)
					if err != nil {
						return nil, err
					}
					committed <- struct{}{}
					<-ctx.Done()
					select {
					case <-release:
					case <-t.Context().Done():
					}
					return q, nil
				}
			})
			p.request(10, protocol.TypeAppAPIRequest, testAPIPayload([]byte("committed")))
			testHostedAwait(t, committed)
			switch action {
			case "cancel":
				testAssetCancel(p, 20, 10, testAssetApp)
				e, _ := p.next()
				if e.Type != protocol.TypeAppCancelled {
					t.Fatal(e)
				}
			case "timeout":
				e, _ := p.next()
				testAssetError(t, e, 10, "app.timeout", true)
			case "disconnect":
				p.frames <- protocol.RoutingEnvelope{ConnID: p.conn, CloseCode: 1000}
				testHostedGone(t, p)
				p.open([]string{protocol.CapabilityHostedAppsV1})
			}
			p.request(30, protocol.TypeListApps, `{}`)
			e, _ := p.next()
			if e.Type != protocol.TypeApps {
				t.Fatal(e)
			}
			if calls.Load() != 1 {
				t.Fatal("mutation replayed")
			}
			close(release)
			testResourceIdle(t, p.mgr)
			id := uint64(40)
			if action == "disconnect" {
				id = 10
			}
			p.request(id, protocol.TypeAppAPIRequest, testAPIPayload(nil))
			testAssetMeta(t, p, id)
			if string(testAssetChunk(t, p, id, 0)) != "fresh" {
				t.Fatal("stale completion rebound")
			}
			testResourceIdle(t, p.mgr)
			p.request(50, protocol.TypeListApps, `{}`)
			e, _ = p.next()
			if e.Type != protocol.TypeApps || calls.Load() != 2 {
				t.Fatal("late output or extra invocation", e)
			}
		})
	}
}

func TestHostedAPIRetirement(t *testing.T) {
	prepared := make(chan *testAssetResponse, 2)
	p := testHostedStart(t, &testAppDiscovery{}, func(c *V2SessionConfig) {
		c.hostedForwardAPI = func(ctx context.Context, a, b, r string, in apps.APIRequest) (hostedResponse, error) {
			q, err := testAPIPrepare(bytes.Repeat([]byte("x"), 70000), 200)(ctx, a, b, r, in)
			if err == nil {
				prepared <- q.(*testAssetResponse)
			}
			return q, err
		}
	})
	p.request(10, protocol.TypeAppAPIRequest, testAPIPayload([]byte("write")))
	testAssetMeta(t, p, 10)
	testAssetChunk(t, p, 10, 0)
	(<-prepared).Close()
	e, _ := p.next()
	testAssetError(t, e, 10, "app.unavailable", true)
	testResourceIdle(t, p.mgr)
	testAssetCredit(p, 10, 1, testAssetApp)
	p.request(20, protocol.TypeListApps, `{}`)
	e, _ = p.next()
	if e.Type != protocol.TypeApps {
		t.Fatal("retired credit revived response", e)
	}
	p.request(30, protocol.TypeAppAPIRequest, testAPIPayload(nil))
	testAssetMeta(t, p, 30)
	testAssetChunk(t, p, 30, 0)
	testAssetCancel(p, 40, 30, testAssetApp)
	p.next()
	testResourceIdle(t, p.mgr)
}

func TestHostedAPIMixedCapacity(t *testing.T) {
	entered, cleanup := make(chan struct{}, 8), make(chan struct{})
	p := testHostedStart(t, &testAppDiscovery{}, func(c *V2SessionConfig) {
		block := func(ctx context.Context, in apps.AssetRequest) (hostedResponse, error) {
			if !in.Reserve(1000) {
				return nil, apps.ErrResponseCapacity
			}
			defer in.Release(1000)
			entered <- struct{}{}
			<-ctx.Done()
			select {
			case <-cleanup:
			case <-t.Context().Done():
			}
			return nil, ctx.Err()
		}
		c.hostedPrepareAsset = func(ctx context.Context, _, _, _ string, in apps.AssetRequest) (hostedResponse, error) {
			return block(ctx, in)
		}
		c.hostedForwardAPI = func(ctx context.Context, _, _, _ string, in apps.APIRequest) (hostedResponse, error) {
			return block(ctx, apps.AssetRequest{Reserve: in.Reserve, Release: in.Release})
		}
	})
	for id := uint64(10); id < 18; id++ {
		typ, payload := protocol.TypeAppAssetRequest, testAssetPayload("/a")
		if id%2 == 1 {
			typ, payload = protocol.TypeAppAPIRequest, testAPIPayload(make([]byte, 32768))
		}
		p.request(id, typ, payload)
		testHostedAwait(t, entered)
	}
	p.mgr.resourceBudget.mu.Lock()
	held, count := p.mgr.resourceBudget.bytes, p.mgr.resourceBudget.count
	p.mgr.resourceBudget.mu.Unlock()
	if held != 8000+4*32768 || count != 8 {
		t.Fatal("aggregate ownership", held, count)
	}
	p.request(18, protocol.TypeAppAPIRequest, testAPIPayload(nil))
	e, _ := p.next()
	testAssetError(t, e, 18, "app.busy", true)
	for id := uint64(10); id < 18; id++ {
		testAssetCancel(p, id+20, id, testAssetApp)
		p.next()
	}
	p.request(50, protocol.TypeAppAPIRequest, testAPIPayload(nil))
	e, _ = p.next()
	testAssetError(t, e, 50, "app.busy", true)
	close(cleanup)
	testResourceIdle(t, p.mgr)
	p.request(60, protocol.TypeAppAPIRequest, testAPIPayload([]byte("new")))
	testHostedAwait(t, entered)
	testAssetCancel(p, 61, 60, testAssetApp)
	p.next()
	testResourceIdle(t, p.mgr)
}

func TestHostedAPIByteCapacity(t *testing.T) {
	for _, connections := range []int{1, 4} {
		t.Run(fmt.Sprint(connections), func(t *testing.T) {
			entered := make(chan struct{}, 4)
			var calls atomic.Int32
			p := testHostedStart(t, &testAppDiscovery{}, func(c *V2SessionConfig) {
				c.hostedPrepareAsset = func(ctx context.Context, _, _, _ string, in apps.AssetRequest) (hostedResponse, error) {
					n := int64(16 << 20)
					if in.Path == "/last" {
						n -= 32768
					}
					if !in.Reserve(n) {
						return nil, apps.ErrResponseCapacity
					}
					defer in.Release(n)
					entered <- struct{}{}
					<-ctx.Done()
					return nil, ctx.Err()
				}
				c.hostedForwardAPI = func(ctx context.Context, a, b, r string, in apps.APIRequest) (hostedResponse, error) {
					calls.Add(1)
					return testAPIPrepare(nil, 204)(ctx, a, b, r, in)
				}
			})
			peers := []*testHostedPeer{p}
			for i := 1; i < connections; i++ {
				peers = append(peers, p.sibling(fmt.Sprint("mixed-", i)))
			}
			for i, q := range peers {
				path := "/full"
				if i == connections-1 {
					path = "/last"
				}
				q.request(10, protocol.TypeAppAssetRequest, testAssetPayload(path))
				testHostedAwait(t, entered)
			}
			q := peers[len(peers)-1]
			q.request(20, protocol.TypeAppAPIRequest, testAPIPayload(make([]byte, 32768)))
			e, _ := q.next()
			testAssetError(t, e, 20, "app.busy", true)
			// Request ownership fills the exact remaining capacity before the response probe.
			if calls.Load() != 1 {
				t.Fatal("request reserved the wrong decoded size")
			}
			q.request(21, protocol.TypeAppAPIRequest, testAPIPayload(make([]byte, 32766)))
			testAssetMeta(t, q, 21)
			for _, peer := range peers {
				testAssetCancel(peer, 30, 10, testAssetApp)
				peer.next()
			}
			testResourceIdle(t, p.mgr)
			q.request(40, protocol.TypeAppAssetRequest, testAssetPayload("/full"))
			testHostedAwait(t, entered)
			q.request(41, protocol.TypeAppAPIRequest, testAPIPayload([]byte("x")))
			e, _ = q.next()
			testAssetError(t, e, 41, "app.busy", true)
			if calls.Load() != 2 {
				t.Fatal("unreserved request dispatched")
			}
			testAssetCancel(q, 42, 40, testAssetApp)
			q.next()
			testResourceIdle(t, p.mgr)
		})
	}
}
