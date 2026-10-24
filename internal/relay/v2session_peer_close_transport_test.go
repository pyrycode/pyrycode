package relay

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestPeerCloseAcceptedDeliveryRetiresTransport(t *testing.T) {
	p := testHostedRealTransport(t, false)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	init := p.hello(nil)
	_, send, _, err := init.ReadResp(decodeRespFrame(t, p.next()))
	if err != nil {
		t.Fatal(err)
	}
	accepted, release := p.gate.arm(t)
	// Even a peer without hosted-app capability uses acknowledged delivery for
	// an unsupported discovery reply, so teardown must cancel its active writer.
	msg, err := send.Encrypt([]byte(`{"id":10,"type":"list_apps","ts":"2026-10-24T12:00:00Z","payload":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	p.write(wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseMsg, msg))
	testHostedAwait(t, accepted)
	p.write(protocol.RoutingEnvelope{ConnID: v2TestConnID, CloseCode: uint16(websocket.StatusNormalClosure)})
	for len(p.mgr.ActiveConns(ctx)) != 0 {
		if ctx.Err() != nil {
			t.Fatal("peer teardown stalled behind accepted delivery")
		}
	}
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	release()
	select {
	case err := <-p.delivered:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("peer-close delivery = %v, want cancellation", err)
		}
	case <-ctx.Done():
		t.Fatal("accepted delivery did not finish")
	}
	select {
	case <-p.socketDone:
		t.Log("peer close cancelled an accepted encrypted write; binary socket retired")
	case <-ctx.Done():
		t.Fatal("cancelled accepted delivery left binary socket alive")
	}
}
