package relay

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
		fragmented  bool
	}{
		{"older", "epoch", 89, true, false},
		{"same", "epoch", 90, true, false},
		{"newer", "epoch", 91, false, false},
		{"different_epoch", "replacement", 89, false, false},
		{"fragmented_older", "epoch", 89, true, true},
		{"fragmented_same", "epoch", 90, true, true},
		{"fragmented_newer", "epoch", 91, false, true},
		{"fragmented_different_epoch", "replacement", 89, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := "retained post"
			if tc.fragmented {
				text = strings.Repeat("retained post\"\\\n🌲", 8000)
			}
			content, err := json.Marshal(map[string]string{"text": text})
			if err != nil {
				t.Fatal(err)
			}
			entered, gate := make(chan struct{}), make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(gate) }) }
			var first atomic.Bool
			p := testCatchUpPeer(t, func(context.Context, protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
				return thread.QueryResult{
					State: thread.StateUsable, Epoch: "epoch", Version: 90, FromVersion: 20,
					Items: []thread.Item{{ID: 1, Kind: "assistant_message", Rev: 1, Content: content}},
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
			// Live progress reaches the peer's output lane while the selected
			// reply's encrypted nonfinal prefix is still in flight.
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
			if tc.fragmented {
				firstID := prefix.ID
				var data, updateID string
				for index := uint64(0); ; index++ {
					var part protocol.ThreadUpdatePart
					if prefix.Type != protocol.TypeThreadItems || prefix.InReplyTo == nil || *prefix.InReplyTo != 40 || prefix.ID != firstID+index || json.Unmarshal(prefix.Payload, &part) != nil || part.Continuation == nil {
						t.Fatal("missing authenticated, correlated fragment or consecutive sender ID")
					}
					c := part.Continuation
					if index == 0 {
						updateID = c.UpdateID
					}
					if part.ConversationID != catchUpConversation || part.Epoch != "epoch" || part.Version != 90 || part.ItemID != 1 || part.Rev != 1 || c.UpdateID != updateID || c.Index != index || c.Offset != uint64(len(data)) {
						t.Fatal("fragment scope or ordering changed")
					}
					data += part.Data
					if c.Final {
						digest := sha256.Sum256([]byte(protocol.TypeThreadItems + "\x00" + data))
						if index == 0 || c.TotalBytes != uint64(len(data)) || updateID != fmt.Sprintf("%x", digest) {
							t.Fatal("fragmented first row did not assemble completely")
						}
						prefix.Payload = json.RawMessage(data)
						break
					}
					prefix, _ = p.next()
				}
			}
			var items protocol.ThreadItemsPayload
			if json.Unmarshal(prefix.Payload, &items) != nil || prefix.Type != protocol.TypeThreadItems || prefix.InReplyTo == nil || *prefix.InReplyTo != 40 || items.Final || len(items.Items) != 1 {
				t.Fatal("missing retained nonfinal prefix")
			}
			var retained struct{ Text string }
			if items.Items[0].ID != 1 || json.Unmarshal(items.Items[0].Content, &retained) != nil || retained.Text != text {
				t.Fatal("retained first row changed")
			}
			final, _ := p.next()
			if json.Unmarshal(final.Payload, &items) != nil || final.Type != protocol.TypeThreadItems || !items.Final || len(items.Items) != 0 || items.Epoch != "epoch" || items.Version != 90 || items.FromVersion == nil || *items.FromVersion != 20 || final.InReplyTo == nil || *final.InReplyTo != 40 || final.ID != prefix.ID+1 {
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
