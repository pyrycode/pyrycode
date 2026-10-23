package relay

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestProducerGapOrderedClose(t *testing.T) {
	p := testHostedRealTransport(t, false, func(c *V2SessionConfig) {
		c.ThreadReady = func() bool { return true }
		c.KnownConversation = func(string) bool { return true }
	})
	init := p.hello(threadCaps)
	_, _, recv, err := init.ReadResp(decodeRespFrame(t, p.next()))
	if err != nil {
		t.Fatal(err)
	}
	entered, release := p.gate.arm(t)
	if err := p.mgr.Push(t.Context(), v2TestConnID, protocol.Envelope{Type: protocol.TypeTurnState, Payload: json.RawMessage(`{"state":"running"}`)}); err != nil {
		t.Fatal(err)
	}
	testHostedAwait(t, entered)
	if err := p.mgr.Push(t.Context(), v2TestConnID, protocol.Envelope{Type: protocol.TypeTurnState, Payload: json.RawMessage(`{"dependent":true}`)}); err != nil {
		t.Fatal(err)
	}
	if err := p.mgr.RetireLiveState(t.Context(), v2TestConnID); err != nil {
		t.Fatal(err)
	}
	observer := &testHostedPeer{t: t, conn: v2TestConnID, events: p.gate.events}
	observer.waitEvent("v2.push.producer_gap.teardown", "")
	// A Run snapshot fences handling after the retirement log.
	p.mgr.ActiveConns(t.Context())
	if err := p.mgr.RetireLiveState(t.Context(), v2TestConnID); err != nil {
		t.Fatal(err)
	}
	release()
	wire := p.next()
	if wire.CloseCode != 0 {
		t.Fatalf("close overtook admitted output: %+v", wire)
	}
	if e := decryptAppFrame(t, wire, recv); e.Type != protocol.TypeTurnState || string(e.Payload) != `{"state":"running"}` {
		t.Fatalf("admitted output = %+v", e)
	}
	if e := p.next(); e.CloseCode != uint16(StatusProducerGap) || len(e.Frame) != 0 && string(e.Frame) != "null" {
		t.Fatalf("terminal wire = %+v", e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := p.conn.SendContext(ctx, protocol.RoutingEnvelope{ConnID: "barrier"}); err != nil {
		t.Fatalf("shared transport lost: %v", err)
	}
	if e := p.next(); e.ConnID != "barrier" {
		t.Fatalf("quarantined output escaped: %+v", e)
	}
	select {
	case <-p.socketDone:
		t.Fatal("gap retired shared binary socket")
	default:
	}
}

func TestProducerGapDeliveryFailure(t *testing.T) {
	p := testHostedRealTransport(t, false, func(c *V2SessionConfig) {
		c.ThreadReady = func() bool { return true }
		c.KnownConversation = func(string) bool { return true }
	})
	init := p.hello(threadCaps)
	if _, _, _, err := init.ReadResp(decodeRespFrame(t, p.next())); err != nil {
		t.Fatal(err)
	}
	entered, release := p.gate.arm(t)
	if err := p.mgr.Push(t.Context(), v2TestConnID, protocol.Envelope{Type: protocol.TypeTurnState, Payload: json.RawMessage(`{"state":"running"}`)}); err != nil {
		t.Fatal(err)
	}
	testHostedAwait(t, entered)
	if err := p.mgr.RetireLiveState(t.Context(), v2TestConnID); err != nil {
		t.Fatal(err)
	}
	observer := &testHostedPeer{t: t, conn: v2TestConnID, events: p.gate.events}
	observer.waitEvent("v2.push.producer_gap.teardown", "")
	if err := p.conn.Close(); err != nil {
		t.Fatal(err)
	}
	release()
	if err := gapDeliveryResult(t, p.delivered); err == nil {
		t.Fatal("unavailable active send claimed success")
	}
	observer.waitEvent("v2.hosted.close_failed", "")
	if err := gapDeliveryResult(t, p.delivered); err == nil {
		t.Fatal("unavailable terminal send claimed success")
	}
	waitQueueGone(t, p.mgr, v2TestConnID)
}

func gapDeliveryResult(t *testing.T, results <-chan error) error {
	t.Helper()
	select {
	case err := <-results:
		return err
	case <-time.After(6 * time.Second):
		t.Fatal("retirement exceeded delivery bound")
		return nil
	}
}

func TestProducerGapHostedNonceQuarantine(t *testing.T) {
	wires := make(chan protocol.RoutingEnvelope, 4)
	release := make(chan struct{})
	m, s, _, recv := liveFixture(t, threadCaps, nil)
	m.cfg.OutboundContext = func(ctx context.Context, e protocol.RoutingEnvelope) error {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		wires <- e
		return nil
	}
	t.Cleanup(func() { m.teardown(s) })
	if s.hosted == nil {
		m.initHosted(t.Context(), s)
	}
	queryCtx, cancelQuery := context.WithCancel(t.Context())
	defer cancelQuery()
	s.hosted.inbox.query = &threadQuery{conversation: gateClaudeConv, ctx: queryCtx, cancel: cancelQuery}
	first := protocol.Envelope{Type: protocol.TypeTurnState, Payload: json.RawMessage(`{"state":"running"}`)}
	m.hostedEnqueue(t.Context(), s, &hostedOutput{env: &first, callerCtx: queryCtx})
	active := s.hosted.active
	if active == nil {
		t.Fatal("selected write missing")
	}
	nextID := s.nextID
	if err := m.RetireLiveState(t.Context(), s.connID); err != nil {
		t.Fatal(err)
	}
	_ = m.Push(t.Context(), s.connID, protocol.Envelope{Type: protocol.TypeThreadItemAdded, Payload: json.RawMessage(`{"conversation_id":"` + gateClaudeConv + `"}`)})
	if queryCtx.Err() != nil {
		t.Fatal("quarantined live push cancelled the admitted query write")
	}
	dependent := protocol.Envelope{Type: protocol.TypeTurnState, Payload: json.RawMessage(`{"dependent":true}`)}
	m.hostedEnqueue(t.Context(), s, &hostedOutput{env: &dependent})
	m.hostedDeliver(t.Context(), s)
	if s.nextID != nextID || s.hosted.active != active {
		t.Fatal("quarantine selected dependent output")
	}
	close(release)
	if err := gapDeliveryResult(t, active.deliveryDone); err != nil {
		t.Fatal(err)
	}
	m.handleHostedDone(t.Context(), hostedResult{s: s, output: active})
	if e := decryptAppFrame(t, <-wires, recv); e.Type != protocol.TypeTurnState {
		t.Fatal(e.Type)
	}
	select {
	case e := <-wires:
		if e.CloseCode != uint16(StatusProducerGap) {
			t.Fatal(e.CloseCode)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal send missing")
	}
	// After teardown no writer can touch this cipher. A fresh probe must use
	// exactly the next receive nonce; a discarded dependent seal would fail AEAD.
	sealed, err := s.send.Encrypt([]byte("nonce probe"))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := recv.Decrypt(sealed); err != nil || string(plain) != "nonce probe" {
		t.Fatalf("dependent output consumed nonce: %q %v", plain, err)
	}
}
