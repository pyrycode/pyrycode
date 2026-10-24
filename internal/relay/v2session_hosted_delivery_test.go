package relay

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func testHostedAwait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("controlled operation did not progress")
	}
}

func TestHostedDiscoveryRekeyOrdering(t *testing.T) {
	p, entered, _, release := testHostedBlockedOutput(t, &testAppDiscovery{})
	p.request(10, "app_cancel", `{"request_id":999}`)
	testHostedAwait(t, entered)
	p.request(11, "list_apps", `{"cursor":null}`)
	p.waitEvent("v2.hosted.queued", "error")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.mgr.Push(ctx, p.conn, protocol.Envelope{ID: 1, Type: "workspace_updated", TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := p.mgr.FlushPushes(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.mgr.Rekey(ctx, p.conn); err != nil {
		t.Fatal(err)
	}
	init, err := noise.NewInitiator(v2TestInstallPriv, p.pub)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := init.WriteInit(nil)
	if err != nil {
		t.Fatal(err)
	}
	p.frames <- wrapInnerFrame(t, p.conn, protocol.TypeNoiseInit, msg)
	// The sibling handshake is a FIFO barrier for the queued rekey response.
	other := p.sibling("rekey-progress")
	testHostedPage(t, other, 12, nil)
	release()
	for _, typ := range []string{"app_cancelled", "error", "workspace_updated", "rekey_request"} {
		e, _ := p.next()
		if e.Type != typ {
			t.Fatalf("old-key output: %+v", e)
		}
	}
	testHostedRekeyResp(t, p, init)
	testHostedPage(t, p, 13, nil)
	// Repeated rekey preserves sender identity for the entire connection lifetime.
	init, err = noise.NewInitiator(v2TestInstallPriv, p.pub)
	if err != nil {
		t.Fatal(err)
	}
	msg, err = init.WriteInit(nil)
	if err != nil {
		t.Fatal(err)
	}
	p.frames <- wrapInnerFrame(t, p.conn, protocol.TypeNoiseInit, msg)
	testHostedRekeyResp(t, p, init)
	testHostedPage(t, p, 14, nil)
}

func testHostedRekeyResp(t *testing.T, p *testHostedPeer, init *noise.Initiator) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		snap := p.rec.snapshot()
		for p.seen < len(snap) {
			e := snap[p.seen]
			p.seen++
			if e.ConnID != p.conn {
				continue
			}
			ack, send, recv, err := init.ReadResp(decodeRespFrame(t, e))
			if err != nil || len(ack) != 0 {
				t.Fatalf("rekey response: %v %s", err, ack)
			}
			p.send, p.recv = send, recv
			return
		}
		select {
		case <-deadline:
			t.Fatal("missing rekey response")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestHostedDiscoveryOrderedDelivery(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	p := testHostedStart(t, &testAppDiscovery{}, func(cfg *V2SessionConfig) {
		out := cfg.Outbound
		cfg.HostedAppsSend = func(ctx context.Context, e protocol.RoutingEnvelope) error {
			if e.ConnID == v2TestConnID && first.CompareAndSwap(false, true) {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return out(e)
		}
	})
	t.Cleanup(func() { close(release) })
	p.request(10, "app_cancel", `{"request_id":999}`)
	testHostedAwait(t, entered)
	p.request(11, "list_apps", `{"cursor":null}`)
	// Frames is FIFO: this request follows the queued hosted error. FlushPushes
	// then proves the legacy push reached the manager's delivery lane.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	openModalConn(t, p.mgr, p.frames, p.rec, p.pub, "delivery-barrier", nil)
	if err := p.mgr.Push(ctx, p.conn, protocol.Envelope{ID: 1, Type: "workspace_updated", TS: time.Now().UTC(), Payload: json.RawMessage(`{"text":"legacy"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := p.mgr.FlushPushes(ctx); err != nil {
		t.Fatal(err)
	}
	// Release once; cleanup still owns the gate on assertion failures.
	release <- struct{}{}
	ids := map[uint64]bool{1: true}
	for _, typ := range []string{"app_cancelled", "error", "workspace_updated"} {
		e, _ := p.next()
		if e.Type != typ || ids[e.ID] || e.ID > hostedMaxID {
			t.Fatalf("ordered fresh envelope: %+v", e)
		}
		ids[e.ID] = true
	}
	p.request(12, "unknown_legacy_request", `{}`)
	e, _ := p.next()
	if e.Type != "error" || ids[e.ID] || e.InReplyTo == nil || *e.InReplyTo != 12 {
		t.Fatalf("legacy error: %+v", e)
	}
	ids[e.ID] = true
	if err := p.mgr.Rekey(ctx, p.conn); err != nil {
		t.Fatal(err)
	}
	e, _ = p.next()
	if e.Type != "rekey_request" || ids[e.ID] {
		t.Fatalf("rekey ID: %+v", e)
	}
}
