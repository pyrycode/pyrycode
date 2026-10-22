package main

import (
	"context"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"testing"
)

func TestChannelPostEmitterV2_Retired(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		if cancelled {
			cancel()
		}
		b := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{{ConnID: "legacy", Interactive: true}, {ConnID: "thread", Interactive: true, Thread: true}, {ConnID: "other"}}}}
		e := newChannelPostEmitterV2(b, ctx, discardLogger())
		for range 2 {
			e.announce(protocol.AssistantDeltaPayload{ConversationID: testConvID, TurnID: "same", Text: "private post"})
		}
		if len(b.pushes) != 0 {
			t.Fatal("channel content published")
		}
		cancel()
	}
}
