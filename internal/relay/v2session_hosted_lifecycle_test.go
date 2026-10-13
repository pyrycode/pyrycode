package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func testHostedGone(t *testing.T, p *testHostedPeer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		found := false
		for _, c := range p.mgr.ActiveConns(ctx) {
			found = found || c.ConnID == p.conn
		}
		if !found {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("connection did not retire")
}

func testHostedClose(t *testing.T, p *testHostedPeer) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		for _, e := range p.rec.snapshot() {
			if e.ConnID == p.conn && e.CloseCode != 0 {
				testHostedGone(t, p)
				return
			}
		}
		select {
		case <-deadline:
			t.Fatal("missing terminal close")
		case <-time.After(time.Millisecond):
		}
	}
}

func testHostedBlockedOutput(t *testing.T, d HostedAppsDiscovery) (*testHostedPeer, <-chan struct{}, <-chan struct{}, func()) {
	t.Helper()
	entered, exited, gate := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	p := testHostedStart(t, d, func(c *V2SessionConfig) {
		out := c.Outbound
		c.HostedAppsSend = func(ctx context.Context, e protocol.RoutingEnvelope) error {
			if e.ConnID == v2TestConnID && e.CloseCode == 0 && first.CompareAndSwap(false, true) {
				defer close(exited)
				close(entered)
				select {
				case <-gate:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return out(e)
		}
	})
	t.Cleanup(release)
	return p, entered, exited, release
}

func TestHostedDiscoveryCancellation(t *testing.T) {
	p := testHostedStart(t, testHostedRecords("Host", 17), nil)
	first := testHostedPage(t, p, 10, nil)
	for i, target := range []uint64{10, 10, 999, hostedMaxID} {
		id := uint64(20 + i)
		p.request(id, "app_cancel", fmt.Sprintf(`{"request_id":%d}`, target))
		e, _ := p.next()
		if e.Type != "app_cancelled" || e.InReplyTo == nil || *e.InReplyTo != id || string(e.Payload) != fmt.Sprintf(`{"request_id":%d}`, target) {
			t.Fatalf("idempotent cancellation: %+v", e)
		}
	}
	// Finished pages are no longer pending work; cancelling one preserves its cursor.
	testHostedPage(t, p, 30, first.NextCursor)

	d := &testAppDiscovery{entered: make(chan struct{}, 1), release: make(chan struct{}), blockFirst: true}
	q := testHostedStart(t, d, nil)
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(d.release) }) })
	q.request(40, "list_apps", `{}`)
	testHostedAwait(t, d.entered)
	q.request(41, "app_cancel", `{"request_id":40,"app_id":"22222222-2222-4222-8222-222222222222"}`)
	e, _ := q.next()
	if e.Type != protocol.TypeAppCancelled || e.InReplyTo == nil || *e.InReplyTo != 41 {
		t.Fatalf("mismatched scope acknowledgement: %+v", e)
	}
	other := q.sibling("other-requester")
	other.request(42, "app_cancel", `{"request_id":40}`)
	e, _ = other.next()
	if e.Type != "app_cancelled" {
		t.Fatal(e)
	}
	once.Do(func() { close(d.release) })
	e, _ = q.next()
	if e.Type != "apps" || e.InReplyTo == nil || *e.InReplyTo != 40 {
		t.Fatalf("resource/foreign cancellation retired discovery: %+v", e)
	}
}

func TestHostedDiscoveryQueuedCancellation(t *testing.T) {
	p, entered, _, release := testHostedBlockedOutput(t, testHostedRecords("Host", 17))
	p.request(10, "app_cancel", `{"request_id":999}`)
	testHostedAwait(t, entered)
	p.request(11, "list_apps", `{}`)
	p.waitEvent("v2.hosted.queued", "apps")
	p.request(12, "list_apps", `{}`)
	p.request(13, "app_cancel", `{"request_id":11}`)
	p.waitEvent("v2.hosted.queued", "app_cancelled")
	// A different connection completes discovery while this connection's output blocks.
	other := p.sibling("unblocked-peer")
	testHostedPage(t, other, 14, nil)
	release()
	for i, typ := range []string{"app_cancelled", "error", "app_cancelled"} {
		e, _ := p.next()
		if e.Type != typ || e.InReplyTo == nil || *e.InReplyTo != []uint64{10, 12, 13}[i] {
			t.Fatalf("queued cancellation/order: %+v", e)
		}
	}
	testHostedPage(t, p, 15, nil)
}

func TestHostedDiscoveryRetirement(t *testing.T) {
	for _, operation := range []string{"read", "delivery"} {
		for _, action := range []string{"cancel", "disconnect", "revocation", "shutdown", "frames closure"} {
			t.Run(operation+"/"+action, func(t *testing.T) {
				d := &testAppDiscovery{entered: make(chan struct{}, 1), release: make(chan struct{}), blockFirst: true}
				var p *testHostedPeer
				var release func()
				var exited <-chan struct{}
				if operation == "read" {
					p = testHostedStart(t, d, nil)
					var once sync.Once
					release = func() { once.Do(func() { close(d.release) }) }
					t.Cleanup(release)
					p.request(10, "list_apps", `{}`)
					testHostedAwait(t, d.entered)
				} else {
					var entered <-chan struct{}
					p, entered, exited, release = testHostedBlockedOutput(t, &testAppDiscovery{})
					p.request(10, "list_apps", `{}`)
					testHostedAwait(t, entered)
				}
				other := p.sibling("progress-peer")
				testHostedPage(t, other, 9, nil)
				switch action {
				case "cancel":
					p.request(11, "app_cancel", `{"request_id":10}`)
					if operation == "read" {
						e, _ := p.next()
						if e.Type != "app_cancelled" {
							t.Fatal(e)
						}
					} else {
						testHostedClose(t, p)
					}
				case "disconnect":
					p.frames <- protocol.RoutingEnvelope{ConnID: p.conn, CloseCode: 1000}
					testHostedGone(t, p)
				case "revocation":
					p.mgr.cfg.Devices.Remove(v2TestDevName)
					testHostedClose(t, p)
				case "shutdown":
					p.stop()
				case "frames closure":
					close(p.frames)
					p.stop()
				}
				// Delivery must exit on retirement before the test opens its output gate.
				if exited != nil {
					testHostedAwait(t, exited)
				}
				release()
				if operation == "read" {
					p.waitEvent("v2.hosted.reader_exited", "")
				}
				if action != "shutdown" && action != "frames closure" {
					_ = p.mgr.ActiveConns(t.Context())
				}
				for _, e := range p.rec.snapshot()[p.seen:] {
					if e.ConnID == p.conn && len(e.Frame) != 0 {
						t.Fatalf("late completion after retirement: %+v", e)
					}
				}
			})
		}
	}
}

func TestHostedDiscoveryDelivery(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		exited := make(chan struct{})
		p := testHostedStart(t, &testAppDiscovery{}, func(c *V2SessionConfig) {
			out := c.Outbound
			c.HostedAppsSend = func(ctx context.Context, e protocol.RoutingEnvelope) error {
				if e.CloseCode != 0 {
					return out(e)
				}
				<-ctx.Done()
				close(exited)
				return ctx.Err()
			}
		})
		p.request(10, "list_apps", `{}`)
		select {
		case <-exited:
		case <-time.After(hostedDeliveryTimeout + 3*time.Second):
			t.Fatal("delivery did not honor its deadline")
		}
		testHostedClose(t, p)
	})
	t.Run("overflow", func(t *testing.T) {
		p, entered, exited, release := testHostedBlockedOutput(t, &testAppDiscovery{})
		p.request(10, "app_cancel", `{"request_id":999}`)
		testHostedAwait(t, entered)
		for i := 0; i < hostedQueueLimit; i++ {
			p.request(uint64(20+i), "app_cancel", `{"request_id":999}`)
		}
		testHostedClose(t, p)
		testHostedAwait(t, exited)
		release()
		other := p.sibling("after-overflow")
		testHostedPage(t, other, 50, nil)
	})
	t.Run("sealed delivery failure", func(t *testing.T) {
		p := testHostedStart(t, &testAppDiscovery{}, func(c *V2SessionConfig) {
			out := c.Outbound
			c.HostedAppsSend = func(ctx context.Context, e protocol.RoutingEnvelope) error {
				if e.CloseCode == 0 {
					return errors.New("test delivery failure")
				}
				return out(e)
			}
		})
		p.request(10, "list_apps", `{}`)
		testHostedClose(t, p)
	})
}

func TestHostedDiscoveryNoDeliveryAdapter(t *testing.T) {
	for _, raw := range []string{
		`{"id":10,"type":"list_apps","ts":"2026-10-10T18:00:01Z","payload":{}}`,
		`{"id":10,"type":"list_apps","ts":null,"payload":{}}`,
		`{"id":null,"type":"list_apps","ts":"2026-10-10T18:00:01Z","payload":{}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			gate, called := make(chan struct{}), make(chan protocol.RoutingEnvelope, 8)
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(gate) }) })
			p := testHostedStart(t, nil, func(c *V2SessionConfig) {
				c.HostedAppsReady, c.HostedAppsSend = nil, nil
				c.OutboundContext = nil
				out := c.Outbound
				c.Outbound = func(e protocol.RoutingEnvelope) error {
					var inner protocol.InnerFrameV2
					_ = json.Unmarshal(e.Frame, &inner)
					if inner.Type != protocol.TypeNoiseResp {
						called <- e
						<-gate
					}
					return out(e)
				}
			})
			for i := 0; i < 3; i++ {
				if i != 0 {
					p.open([]string{protocol.CapabilityHostedAppsV1})
				}
				p.raw(raw)
				p.waitEvent("v2.hosted.delivery_unavailable", "")
				testHostedGone(t, p)
			}
			p.stop()
			once.Do(func() { close(gate) })
			select {
			case e := <-called:
				t.Fatalf("unsafe legacy delivery invoked: %+v", e)
			default:
			}
		})
	}
}
