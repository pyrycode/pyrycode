package relay

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestThreadCompleteCapabilities(t *testing.T) {
	for _, caps := range [][]string{{protocol.CapabilityThread}, threadCaps} {
		t.Run(strings.Join(caps, "-"), func(t *testing.T) {
			var catchups, pages atomic.Int32
			p := testHostedStartCaps(t, nil, caps, func(c *V2SessionConfig) {
				c.ThreadReady = func() bool { return true }
				c.KnownConversation = func(id string) bool { return id == catchUpConversation }
				c.ThreadCatchUp = func(_ context.Context, req protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
					catchups.Add(1)
					if req.ConversationID != catchUpConversation || req.Version != 0 || req.Projection != "" {
						t.Errorf("catch-up scope: %+v", req)
					}
					return thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 20, LowerOrder: 10, UpperOrder: 10, Continuation: 10, OlderExists: true,
						Items: []thread.Item{{ID: 10, Kind: "user_message", Order: 10, Rev: 1, Session: "supplied-session", Content: json.RawMessage(`{"text":"newest"}`)}}}, nil
				}
				c.ThreadPage = func(_ context.Context, req protocol.ThreadPagePayload) (thread.QueryResult, error) {
					pages.Add(1)
					if req.ConversationID != catchUpConversation || req.BeforeOrder != 10 || req.Projection != "" {
						t.Errorf("page scope: %+v", req)
					}
					return thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 20, LowerOrder: 1, UpperOrder: 1, Continuation: 1,
						Items: []thread.Item{{ID: 1, Kind: "user_message", Order: 1, Rev: 1, Session: "older-session", Content: json.RawMessage(`{"text":"older"}`)}}}, nil
				}
			})
			if len(p.ack.Capabilities) != len(caps) || !slices.Contains(p.ack.Capabilities, protocol.CapabilityThread) || slices.Contains(p.ack.Capabilities, protocol.CapabilityInteractive) != slices.Contains(caps, protocol.CapabilityInteractive) {
				t.Fatalf("negotiated capabilities: %v", p.ack.Capabilities)
			}
			if catchups.Load() != 0 || pages.Load() != 0 {
				t.Fatal("handshake queried content")
			}
			read := func(id, itemID uint64, session string) {
				t.Helper()
				items := 0
				for {
					env, _ := p.next()
					var result protocol.ThreadItemsPayload
					if env.Type != protocol.TypeThreadItems || env.InReplyTo == nil || *env.InReplyTo != id || json.Unmarshal(env.Payload, &result) != nil || result.ConversationID != catchUpConversation || result.Epoch != "epoch" || result.Version != 20 || result.Projection != "" {
						t.Fatalf("complete query reply: %+v", env)
					}
					for _, item := range result.Items {
						items++
						if item.ID != itemID || item.Session != session {
							t.Fatalf("item identity: %+v", item)
						}
					}
					if result.Final {
						if items != 1 || len(result.Items) != 0 {
							t.Fatalf("separate correlated final: %+v, items=%d", result, items)
						}
						return
					}
				}
			}
			testCatchUpRequest(p, 40, 0)
			read(40, 10, "supplied-session")
			testPageRequest(p, 41, 10)
			read(41, 1, "older-session")
			if catchups.Load() != 1 || pages.Load() != 1 {
				t.Fatalf("provider calls: %d/%d", catchups.Load(), pages.Load())
			}
			conns := p.mgr.ActiveThreadConns(t.Context(), catchUpConversation)
			if len(conns) != 1 || conns[0].View != ThreadViewComplete || conns[0].Interactive != slices.Contains(caps, protocol.CapabilityInteractive) {
				t.Fatalf("publication recipients: %+v", conns)
			}
			update := protocol.Envelope{Type: protocol.TypeThreadTextAppend, SessionID: json.RawMessage(`"supplied-session"`), SessionStateCleared: true,
				Payload: json.RawMessage(`{"conversation_id":"` + catchUpConversation + `","epoch":"epoch","version":21,"item_id":10,"base_rev":1,"rev":2,"text":"!"}`)}
			if err := p.mgr.DeliverThread(t.Context(), p.conn, update); err != nil {
				t.Fatal(err)
			}
			frames := waitForEnvelopes(t, p.rec, p.seen+1)
			env := decryptAppFrame(t, frames[p.seen], p.recv)
			if env.Type != update.Type || string(env.SessionID) != string(update.SessionID) || !env.SessionStateCleared || string(env.Payload) != string(update.Payload) {
				t.Fatalf("complete update attribution: %+v", env)
			}
		})
	}
}
