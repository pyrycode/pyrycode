package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestReadinessThreadReconnectSnapshotCapacity(t *testing.T) {
	p := testHostedRealTransport(t, true)
	payloads := make([]protocol.ModelListPayload, 4*hostedQueueLimit)
	totalBytes := 0
	for i := range payloads {
		payloads[i] = sampleModelListPayload(fmt.Sprintf("retained-%d", i), "sonnet")
		raw, err := json.Marshal(payloads[i])
		if err != nil {
			t.Fatal(err)
		}
		totalBytes += len(raw)
	}
	if len(payloads) >= pushQueueCap || totalBytes >= pushQueueByteCeiling {
		t.Fatal("fixture exceeds ordinary bounds")
	}
	p.mgr.cfg.KnownConversation = func(string) bool { return true }
	entered, release := p.gate.arm(t)
	init := p.hello([]string{protocol.CapabilityInteractive})
	testHostedAwait(t, entered)
	// ActiveConns is a Run barrier after all handshake reconciliation pushes.
	waitConnOpen(t, p.mgr, v2TestConnID)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for _, payload := range payloads {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.mgr.Push(ctx, v2TestConnID, protocol.Envelope{Type: protocol.TypeModelList, Payload: raw}); err != nil {
			t.Fatal(err)
		}
	}
	flushed := make(chan error, 1)
	go func() { flushed <- p.mgr.FlushPushes(ctx) }()
	select {
	case err := <-flushed:
		t.Fatalf("flush overtook capacity-held snapshots: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	_, _, recv, err := init.ReadResp(decodeRespFrame(t, p.next()))
	if err != nil {
		t.Fatal(err)
	}
	var previous uint64 = 1
	for i, want := range payloads {
		wire := p.next()
		if wire.CloseCode != 0 {
			t.Fatalf("legacy reconnect closed with %d before snapshot %d/%d", wire.CloseCode, i, len(payloads))
		}
		env := decryptAppFrame(t, wire, recv)
		var got protocol.ModelListPayload
		if env.Type != protocol.TypeModelList || json.Unmarshal(env.Payload, &got) != nil || !reflect.DeepEqual(got, want) || env.ID <= previous {
			t.Fatalf("snapshot %d: id=%d previous=%d type=%s got=%#v want=%#v", i, env.ID, previous, env.Type, got, want)
		}
		previous = env.ID
	}
	if err := <-flushed; err != nil {
		t.Fatal(err)
	}
	// A receipt after the source fence proves no duplicate or terminal close trails it.
	if err := p.conn.SendContext(ctx, protocol.RoutingEnvelope{ConnID: "capacity-barrier"}); err != nil {
		t.Fatal(err)
	}
	if wire := p.next(); wire.ConnID != "capacity-barrier" {
		t.Fatalf("extra output: %+v", wire)
	}
}
