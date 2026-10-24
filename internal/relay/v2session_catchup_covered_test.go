package relay

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestThreadCatchUpCoveredLiveDuringDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, epoch string
		version     uint64
		covered     bool
	}{
		{"older", "epoch", 89, true},
		{"same", "epoch", 90, true},
		{"newer", "epoch", 91, false},
		{"different_epoch", "replacement", 89, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entered, gate := make(chan struct{}), make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(gate) }) }
			var first atomic.Bool
			p := testCatchUpPeer(t, func(context.Context, protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
				return thread.QueryResult{
					State: thread.StateUsable, Epoch: "epoch", Version: 90, FromVersion: 20,
					Items: []thread.Item{{ID: 1, Kind: "assistant_message", Rev: 1, Content: json.RawMessage(`{"text":"retained post"}`)}},
				}, nil
			}, func(c *V2SessionConfig) {
				out := c.HostedAppsSend
				c.HostedAppsSend = func(ctx context.Context, e protocol.RoutingEnvelope) error {
					if e.CloseCode == 0 && first.CompareAndSwap(false, true) {
						close(entered)
						select {
						case <-gate:
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					return out(ctx, e)
				}
			})
			t.Cleanup(release)
			testCatchUpRequest(p, 40, 20)
			testHostedAwait(t, entered)
			// This publication was captured by the reply but reaches the peer's
			// output lane while its encrypted nonfinal prefix is still in flight.
			payload, err := json.Marshal(protocol.ThreadTextAppendPayload{ConversationID: catchUpConversation, Epoch: tc.epoch, Version: tc.version, ItemID: 1, BaseRev: 2, Rev: 3, Text: "new"})
			if err != nil {
				t.Fatal(err)
			}
			err = p.mgr.Push(t.Context(), p.conn, protocol.Envelope{Type: protocol.TypeThreadTextAppend, Payload: payload})
			if err != nil && (tc.covered || !errors.Is(err, ErrConnNotFound)) {
				t.Fatal(err)
			}
			if !tc.covered {
				testHostedGone(t, p)
				p.waitEvent("v2.hosted.delivery_failed", "")
				// The terminal close may already be published, but no reply may be.
				for _, e := range p.rec.snapshot()[1:] {
					if e.CloseCode == 0 {
						t.Fatal("uncaptured progress allowed a canceled reply")
					}
				}
				return
			}
			release()
			prefix, _ := p.next()
			var items protocol.ThreadItemsPayload
			if json.Unmarshal(prefix.Payload, &items) != nil || prefix.Type != protocol.TypeThreadItems || items.Final || len(items.Items) != 1 {
				t.Fatal("missing retained nonfinal prefix")
			}
			final, _ := p.next()
			if json.Unmarshal(final.Payload, &items) != nil || final.Type != protocol.TypeThreadItems || !items.Final || final.InReplyTo == nil || *final.InReplyTo != 40 || final.ID != prefix.ID+1 {
				t.Fatal("covered update interrupted correlated final marker or spent a nonce")
			}
			p.request(41, protocol.TypeAppCancel, `{"request_id":999}`)
			sentinel, _ := p.next()
			if sentinel.Type != protocol.TypeAppCancelled || sentinel.InReplyTo == nil || *sentinel.InReplyTo != 41 || sentinel.ID != final.ID+1 {
				t.Fatal("covered update replayed after final marker")
			}
		})
	}
}
