package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// waitConnOpen waits for a Run-owned snapshot of successful admission.
func waitConnOpen(t *testing.T, mgr *V2SessionManager, connID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for {
		for _, c := range mgr.ActiveConns(ctx) {
			if c.ConnID == connID {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("conn %q did not reach open within deadline", connID)
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func testRecoveryOpen(t *testing.T, p *testHostedPeer, caps []string, last string) *testHostedPeer {
	t.Helper()
	q := &testHostedPeer{t: t, mgr: p.mgr, frames: p.frames, rec: p.rec, conn: "returning", pub: p.pub, events: p.events, ids: map[uint64]bool{1: true}}
	var hello protocol.Envelope
	if err := json.Unmarshal(buildHelloEarlyDataCaps(t, v2TestToken, caps), &hello); err != nil {
		t.Fatal(err)
	}
	if last != "" {
		hello.Payload = replaceObjectField(hello.Payload, "last_event_id", json.RawMessage(last))
	}
	raw, err := json.Marshal(hello)
	if err != nil {
		t.Fatal(err)
	}
	init, err := noise.NewInitiator(v2TestInstallPriv, p.pub)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := init.WriteInit(raw)
	if err != nil {
		t.Fatal(err)
	}
	start := len(p.rec.snapshot())
	q.frames <- wrapInnerFrame(t, q.conn, protocol.TypeNoiseInit, msg)
	waitConnOpen(t, p.mgr, q.conn)
	for i, wire := range p.rec.snapshot()[start:] {
		if wire.ConnID != q.conn {
			continue
		}
		ack, send, recv, err := init.ReadResp(decodeRespFrame(t, wire))
		if err != nil {
			t.Fatal(err)
		}
		var env protocol.Envelope
		if json.Unmarshal(ack, &env) != nil || env.Type != protocol.TypeHelloAck {
			t.Fatal("missing authenticated hello ack")
		}
		q.send, q.recv, q.seen = send, recv, start+i+1
		return q
	}
	t.Fatal("missing handshake response")
	return nil
}

func TestV2Session_LastEventIDIgnored(t *testing.T) {
	for _, isThread := range []bool{false, true} {
		for _, last := range []string{"", "0", "1", "18446744073709551615", `"stale"`, `{}`, `[]`, `null`} {
			t.Run(fmt.Sprintf("thread=%t/last=%s", isThread, last), func(t *testing.T) {
				t.Parallel()
				p := testCatchUpPeer(t, nil, func(c *V2SessionConfig) {
					c.KnownConversation = func(id string) bool { return id == catchUpConversation || id == "other" }
				})
				ring := eventring.New(4)
				for range 4 {
					ring.Append(catchUpConversation, protocol.TypeTurnState, json.RawMessage(`{"conversation_id":"ring-secret"}`), time.Now())
				}
				var cursorCalls atomic.Int32
				p.mgr.SetReplaySource(ring, func() string { cursorCalls.Add(1); return catchUpConversation })
				caps := []string{protocol.CapabilityInteractive, protocol.CapabilityHostedAppsV1}
				if isThread {
					caps = append(caps, protocol.CapabilityThread)
				}
				q := testRecoveryOpen(t, p, caps, last)
				q.request(10, protocol.TypeAppCancel, `{"request_id":999}`)
				e, _ := q.next()
				if e.Type != protocol.TypeAppCancelled || e.InReplyTo == nil || *e.InReplyTo != 10 || cursorCalls.Load() != 0 {
					t.Fatal("obsolete cursor changed handshake delivery", e.Type, cursorCalls.Load())
				}
				for i, conv := range []string{catchUpConversation, "other"} {
					payload := json.RawMessage(fmt.Sprintf(`{"conversation_id":%q,"version":%d}`, conv, i+1))
					env := protocol.Envelope{Type: protocol.TypeThreadItemAdded, Payload: payload}
					if isThread {
						if err := q.mgr.DeliverThread(t.Context(), q.conn, env); err != nil {
							t.Fatal(err)
						}
					} else {
						env.Type = protocol.TypeTurnState
						if err := q.mgr.Push(t.Context(), q.conn, env); err != nil {
							t.Fatal(err)
						}
					}
					e, _ := q.next()
					if e.Type != env.Type || string(e.Payload) != string(payload) {
						t.Fatal("subsequent conversation update lost or reordered", e)
					}
				}
				p.request(11, protocol.TypeAppCancel, `{"request_id":999}`)
				e, _ = p.next()
				if e.Type != protocol.TypeAppCancelled {
					t.Fatal("recovery leaked onto another connection")
				}
				q.frames <- protocol.RoutingEnvelope{ConnID: q.conn, CloseCode: 1000}
				p.request(12, protocol.TypeAppCancel, `{"request_id":999}`)
				p.next()
				q = testRecoveryOpen(t, p, caps, last)
				q.request(13, protocol.TypeAppCancel, `{"request_id":999}`)
				e, _ = q.next()
				if e.Type != protocol.TypeAppCancelled || cursorCalls.Load() != 0 {
					t.Fatal("reused connection identity retained replay state")
				}
			})
		}
	}
}

func TestThreadRecoveryAfterReconnect(t *testing.T) {
	for _, page := range []bool{false, true} {
		t.Run(fmt.Sprint("page=", page), func(t *testing.T) {
			var calls atomic.Int32
			p := testThreadQueryPeer(t, page, func(_ context.Context, req protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
				calls.Add(1)
				if req.ConversationID != catchUpConversation {
					t.Error("recovery substituted active conversation")
				}
				if !page && req.Version > 0 {
					return thread.QueryResult{State: thread.StateUsable, Epoch: "new", Version: 5, ResetRequired: true}, nil
				}
				return thread.QueryResult{State: thread.StateUsable, Epoch: "new", Version: 5, LowerOrder: 1, UpperOrder: 2, Continuation: 1, Items: []thread.Item{{ID: 1, Kind: "future", Order: 1, Rev: 1, Content: json.RawMessage(`{"text":"recovered"}`)}}}, nil
			}, nil)
			q := testRecoveryOpen(t, p, append(append([]string{}, threadCaps...), protocol.CapabilityHostedAppsV1), `{"ignored":true}`)
			q.request(10, protocol.TypeAppCancel, `{"request_id":999}`)
			e, _ := q.next()
			if e.Type != protocol.TypeAppCancelled || calls.Load() != 0 {
				t.Fatal("hello implicitly recovered content")
			}
			if !page {
				testCatchUpRequest(q, 20, 999)
				e, _ = q.next()
				if e.Type != protocol.TypeThreadReset {
					t.Fatal("unusable checkpoint did not reset")
				}
			}
			before := uint64(0)
			if page {
				before = 2
			}
			if page {
				testPageRequest(q, 21, before)
			} else {
				q.request(21, protocol.TypeThreadCatchUp, fmt.Sprintf(`{"conversation_id":%q,"epoch":"","version":0,"limit":1}`, catchUpConversation))
			}
			e, _ = q.next()
			if e.Type != protocol.TypeThreadItems || e.InReplyTo == nil || *e.InReplyTo != 21 {
				t.Fatal("explicit recovery failed", e)
			}
			var body protocol.ThreadItemsPayload
			if json.Unmarshal(e.Payload, &body) != nil || body.Epoch != "new" || len(body.Items) != 1 || body.Items[0].ID != 1 || string(body.Items[0].Content) != `{"text":"recovered"}` {
				t.Fatal("recovered content missing")
			}
			e, _ = q.next()
			if json.Unmarshal(e.Payload, &body) != nil || !body.Final || body.LowerOrder == nil || *body.LowerOrder != 1 || body.Continuation == nil || *body.Continuation != 1 {
				t.Fatal("recovery completion missing")
			}
		})
	}
}
