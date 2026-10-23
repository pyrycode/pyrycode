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
	encoded := make([]json.RawMessage, len(payloads))
	totalBytes := 0
	for i := range payloads {
		payloads[i] = sampleModelListPayload(fmt.Sprintf("retained-%d", i), "sonnet")
		raw, err := json.Marshal(payloads[i])
		if err != nil {
			t.Fatal(err)
		}
		totalBytes += len(raw)
		encoded[i] = raw
	}
	if len(payloads) >= pushQueueCap || totalBytes >= pushQueueByteCeiling {
		t.Fatal("fixture exceeds ordinary bounds")
	}
	p.mgr.cfg.KnownConversation = func(string) bool { return true }
	exhausted := make(chan struct{})
	p.mgr.cfg.ThreadLiveState = func() func() (LiveState, bool) {
		i := 0
		return func() (LiveState, bool) {
			if i == len(payloads) {
				close(exhausted)
				return LiveState{}, false
			}
			payload := payloads[i]
			raw := encoded[i]
			i++
			return LiveState{ConversationID: payload.ConversationID, SessionGeneration: 1, Revision: 1,
				Envelope: protocol.Envelope{Type: protocol.TypeModelList, Payload: raw}}, true
		}
	}
	entered, release := p.gate.arm(t)
	init := p.hello([]string{protocol.CapabilityInteractive})
	testHostedAwait(t, entered)
	// ActiveConns proves admission while the transport holds snapshot delivery.
	waitConnOpen(t, p.mgr, v2TestConnID)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	select {
	case <-exhausted:
		t.Fatal("snapshot enumeration overtook capacity-held transport")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	_, _, recv, err := init.ReadResp(decodeRespFrame(t, p.next()))
	if err != nil {
		t.Fatal(err)
	}
	var previous uint64 = 1
	for i, want := range payloads {
		clear := decryptAppFrame(t, p.next(), recv)
		if clear.Type != protocol.TypeModelList || !clear.SessionStateCleared || string(clear.Payload) != `{}` || clear.ID <= previous || string(clear.ConversationID) != fmt.Sprintf("%q", want.ConversationID) {
			t.Fatalf("snapshot clear %d: %+v", i, clear)
		}
		previous = clear.ID
		wire := p.next()
		if wire.CloseCode != 0 {
			t.Fatalf("thread reconnect closed with %d before snapshot %d/%d", wire.CloseCode, i, len(payloads))
		}
		env := decryptAppFrame(t, wire, recv)
		var got protocol.ModelListPayload
		if env.Type != protocol.TypeModelList || env.SessionStateCleared || json.Unmarshal(env.Payload, &got) != nil || !reflect.DeepEqual(got, want) || env.ID <= previous {
			t.Fatalf("snapshot %d: id=%d previous=%d type=%s got=%#v want=%#v", i, env.ID, previous, env.Type, got, want)
		}
		previous = env.ID
	}
	testHostedAwait(t, exhausted)
	if err := p.mgr.FlushPushes(ctx); err != nil {
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
