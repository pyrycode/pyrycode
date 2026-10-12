package relay

import (
	"encoding/json"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestRetiredThreadQueueDoesNotDrain(t *testing.T) {
	t.Parallel()
	up := false
	m, s, rec, recv := liveFixture(t, threadCaps, func(c *V2SessionConfig) {
		c.Connected = func() bool { return up }
	})
	payload, err := json.Marshal(protocol.ThreadTextAppendPayload{ConversationID: gateClaudeConv, Epoch: "epoch", Version: 2, ItemID: 1, BaseRev: 1, Rev: 2, Text: "held"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Push(t.Context(), s.connID, protocol.Envelope{Type: protocol.TypeThreadTextAppend, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	m.drainOnce(t.Context())
	if len(rec.snapshot()) != 1 || len(m.queues[s.connID].items) != 1 {
		t.Fatal("transport hold did not retain the unsealed append")
	}
	if err := m.RetireLiveState(t.Context(), s.connID); err != nil {
		t.Fatal(err)
	}
	up = true
	// Both Run arms are ready. Exercise the drain before handling teardown.
	m.drainOnce(t.Context())
	for _, frame := range rec.snapshot()[1:] {
		plain := threadPlain(t, frame, recv)
		t.Errorf("retired queue delivered authenticated plaintext: %s", plain)
	}
	id := <-m.pushOverflow
	m.handlePushOverflow(t.Context(), id)
	if m.sessions[s.connID] != nil || m.queues[s.connID] != nil {
		t.Fatal("gap teardown retained the connection or its dependent work")
	}
}
