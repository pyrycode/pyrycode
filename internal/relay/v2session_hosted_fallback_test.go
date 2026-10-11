package relay

import (
	"context"
	"sync"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestHostedDiscoveryTransportRetirement(t *testing.T) {
	for _, raw := range []string{
		`{"id":10,"type":"list_apps","ts":"2026-10-10T18:00:01Z","payload":{}}`,
		`{"id":10,"type":"list_apps","ts":null,"payload":{}}`,
	} {
		for _, action := range []string{"disconnect", "revocation", "shutdown", "frames closure"} {
			t.Run(raw+"/"+action, func(t *testing.T) {
				entered, exited, gate := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var once sync.Once
				release := func() { once.Do(func() { close(gate) }) }
				t.Cleanup(release)
				p := testHostedStart(t, nil, func(c *V2SessionConfig) {
					c.HostedAppsReady, c.HostedAppsSend = nil, nil
					out := c.Outbound
					c.OutboundContext = func(ctx context.Context, e protocol.RoutingEnvelope) error {
						if e.ConnID == v2TestConnID && e.CloseCode == 0 {
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
				p.raw(raw)
				testHostedAwait(t, entered)
				other := p.sibling("progress-peer")
				other.request(11, "list_apps", `{}`)
				e, _ := other.next()
				testHostedError(t, e, 11, "protocol.unsupported")
				switch action {
				case "disconnect":
					p.frames <- protocol.RoutingEnvelope{ConnID: p.conn, CloseCode: 1000}
					// Inbound frames and snapshots are independent select arms. A new
					// handshake on the same Frames stream proves teardown was processed.
					p.sibling("retirement-barrier")
				case "revocation":
					p.mgr.cfg.Devices.Remove(v2TestDevName)
					testHostedClose(t, p)
				case "shutdown":
					p.stop()
				case "frames closure":
					close(p.frames)
					p.stop()
				}
				testHostedAwait(t, exited)
				release()
				for _, e := range p.rec.snapshot()[p.seen:] {
					if e.ConnID == p.conn && len(e.Frame) != 0 {
						// Authenticate any unexpected bytes, rather than accepting a
						// transport record as evidence of an application reply.
						t.Fatalf("late hosted control: %s", threadPlain(t, e, p.recv))
					}
				}
			})
		}
	}
}

func TestHostedDiscoveryTransportCloseShutdown(t *testing.T) {
	for _, framesClosure := range []bool{false, true} {
		t.Run(map[bool]string{false: "shutdown", true: "frames closure"}[framesClosure], func(t *testing.T) {
			entered, exited, gate := make(chan struct{}), make(chan struct{}), make(chan struct{})
			t.Cleanup(func() { close(gate) })
			p := testHostedStart(t, nil, func(c *V2SessionConfig) {
				c.HostedAppsReady, c.HostedAppsSend = nil, nil
				out := c.Outbound
				c.OutboundContext = func(ctx context.Context, e protocol.RoutingEnvelope) error {
					defer close(exited)
					close(entered)
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-gate:
						return out(e)
					}
				}
			})
			p.raw(`{"id":null,"type":"list_apps","ts":"2026-10-10T18:00:01Z","payload":{}}`)
			testHostedAwait(t, entered)
			if framesClosure {
				close(p.frames)
			}
			p.stop()
			testHostedAwait(t, exited)
			if len(p.rec.snapshot()) != p.seen {
				t.Fatal("terminal close published after manager shutdown")
			}
		})
	}
}
