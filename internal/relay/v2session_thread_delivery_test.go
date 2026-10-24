package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func testThreadHeld(t *testing.T, tweak func(*V2SessionConfig)) (*testHostedPeer, <-chan struct{}, <-chan struct{}, func()) {
	t.Helper()
	entered, exited, gate := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	p := testHostedStartCaps(t, &testAppDiscovery{}, append(append([]string{}, threadCaps...), protocol.CapabilityHostedAppsV1), func(c *V2SessionConfig) {
		c.ThreadReady = func() bool { return true }
		c.KnownConversation = func(string) bool { return true }
		out := c.HostedAppsSend
		c.HostedAppsSend = func(ctx context.Context, e protocol.RoutingEnvelope) error {
			if e.ConnID == v2TestConnID && e.CloseCode == 0 && first.CompareAndSwap(false, true) {
				defer close(exited)
				close(entered)
				select {
				case <-gate:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return out(ctx, e)
		}
		if tweak != nil {
			tweak(c)
		}
	})
	t.Cleanup(release)
	p.request(10, protocol.TypeAppCancel, `{"request_id":999}`)
	testHostedAwait(t, entered)
	return p, entered, exited, release
}

func testThreadResult(t *testing.T, done <-chan error, want error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatalf("delivery=%v, want %v", err, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("delivery wait stranded")
	}
}

func testThreadQueued(t *testing.T, p *testHostedPeer, used int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.mgr.hostedMu.Lock()
		in := p.mgr.hostedInboxes[p.conn]
		ready := in != nil && in.used == used
		p.mgr.hostedMu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("missing lane admission")
}

func TestThreadDeliveryLateEligibility(t *testing.T) {
	types := []string{protocol.TypeThreadItemAdded, protocol.TypeThreadItemChanged, protocol.TypeThreadTextAppend, protocol.TypeThreadItems, protocol.TypeThreadReset}
	for _, typ := range types {
		for _, loss := range []string{"removed", "codex", "missing", "part", "completion"} {
			t.Run(typ+"/"+loss, func(t *testing.T) {
				var eligible, codex atomic.Bool
				eligible.Store(true)
				p, _, _, release := testThreadHeld(t, func(c *V2SessionConfig) {
					c.KnownConversation = func(string) bool { return eligible.Load() }
					c.CodexConversation = func(string) bool { return codex.Load() }
				})
				reply := uint64(40)
				payload := `{"conversation_id":"known"}`
				if loss == "missing" {
					payload = `{}`
				}
				if loss == "part" || loss == "completion" {
					payload = fmt.Sprintf(`{"conversation_id":"known","continuation":{"index":1,"final":%t},"data":"part"}`, loss == "completion")
				}
				env := protocol.Envelope{Type: typ, InReplyTo: &reply, Payload: json.RawMessage(payload)}
				done := make(chan error, 1)
				go func() { done <- p.mgr.DeliverThread(t.Context(), p.conn, env) }()
				testThreadQueued(t, p, 2)
				select {
				case err := <-done:
					t.Fatalf("handoff reported outcome: %v", err)
				default:
				}
				if loss == "codex" {
					codex.Store(true)
				} else if loss != "missing" {
					eligible.Store(false)
				}
				if err := p.mgr.Push(t.Context(), p.conn, protocol.Envelope{Type: protocol.TypeWorkspaceUpdated, Payload: json.RawMessage(`{}`)}); err != nil {
					t.Fatal(err)
				}
				release()
				testThreadResult(t, done, ErrThreadWithheld)
				first, _ := p.next()
				next, _ := p.next()
				if first.Type != protocol.TypeAppCancelled || next.Type != protocol.TypeWorkspaceUpdated || next.ID != first.ID+1 {
					t.Fatalf("withholding spent sender ID/nonce or stranded output: %+v %+v", first, next)
				}
			})
		}
	}
	// The legacy live producer also needs the late gate after its pre-enqueue check.
	var known atomic.Bool
	known.Store(true)
	p, _, _, release := testThreadHeld(t, func(c *V2SessionConfig) { c.KnownConversation = func(string) bool { return known.Load() } })
	for _, typ := range types[:3] {
		if err := p.mgr.Push(t.Context(), p.conn, protocol.Envelope{Type: typ, Payload: json.RawMessage(`{"conversation_id":"known"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	testThreadQueued(t, p, 4)
	known.Store(false)
	if err := p.mgr.Push(t.Context(), p.conn, protocol.Envelope{Type: protocol.TypeWorkspaceUpdated, Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	release()
	p.next()
	env, _ := p.next()
	if env.Type != protocol.TypeWorkspaceUpdated {
		t.Fatal("queued live content leaked")
	}
}

func TestThreadDeliveryOrderedReply(t *testing.T) {
	p, _, _, release := testThreadHeld(t, nil)
	// Fill the remaining shared lane slots with ordinary output.
	for range hostedQueueLimit - 1 {
		if err := p.mgr.Push(t.Context(), p.conn, protocol.Envelope{Type: protocol.TypeWorkspaceUpdated, Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	testThreadQueued(t, p, hostedQueueLimit)
	items := make([]protocol.ThreadItem, hostedQueueLimit+4)
	for i := range items {
		content, _ := json.Marshal(map[string]string{"text": strings.Repeat("<&界", protocol.MaxThreadEnvelopeBytes/30)})
		items[i] = protocol.ThreadItem{ID: uint64(i + 1), Kind: "assistant_message", Rev: 1, Content: content}
	}
	items[5].Content, _ = json.Marshal(map[string]string{"text": strings.Repeat("<&界", protocol.MaxThreadEnvelopeBytes/2)})
	request, from := uint64(70), uint64(1)
	encoded, err := protocol.EncodeThreadReply(protocol.Envelope{InReplyTo: &request}, protocol.ThreadItemsPayload{ConversationID: "known", Epoch: "epoch", Version: 100, Items: items, Final: true, FromVersion: &from})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) <= hostedQueueLimit {
		t.Fatalf("not past lane cap: %d", len(encoded))
	}
	done := make(chan error, 1)
	go func() {
		for _, raw := range encoded {
			var env protocol.Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				done <- err
				return
			}
			if err := p.mgr.DeliverThread(t.Context(), p.conn, env); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		t.Fatalf("capacity hold resolved: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	other := p.sibling("thread-progress")
	testHostedPage(t, other, 20, nil)
	waitConnOpen(t, p.mgr, p.conn)
	p.mgr.pushMu.Lock()
	q := p.mgr.queues[p.conn]
	bounded := q != nil && len(q.items) <= pushQueueCap && q.bytes <= pushQueueByteCeiling
	p.mgr.pushMu.Unlock()
	if !bounded {
		t.Fatal("upstream bounds exceeded")
	}
	release()
	p.next()
	for range hostedQueueLimit - 1 {
		env, _ := p.next()
		if env.Type != protocol.TypeWorkspaceUpdated {
			t.Fatal(env.Type)
		}
	}
	testThreadResult(t, done, nil)
	var got []protocol.ThreadItem
	var assembly strings.Builder
	var index, offset uint64
	var previous uint64
	for i, expected := range encoded {
		env, obj := p.next()
		raw, _ := json.Marshal(obj)
		if len(raw) > protocol.MaxThreadEnvelopeBytes || env.ID <= previous || env.InReplyTo == nil || *env.InReplyTo != request {
			t.Fatalf("stamp/order: %d %+v", len(raw), env)
		}
		previous = env.ID
		var original protocol.Envelope
		if err := json.Unmarshal(expected, &original); err != nil {
			t.Fatal(err)
		}
		if string(env.Payload) != string(original.Payload) {
			t.Fatalf("lost/reordered payload %d", i)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(env.Payload, &fields); err != nil {
			t.Fatal(err)
		}
		if continuation := fields["continuation"]; len(continuation) > 0 && continuation[0] == '{' {
			var part protocol.ThreadUpdatePart
			if err := json.Unmarshal(env.Payload, &part); err != nil {
				t.Fatal(err)
			}
			if part.Continuation.Index != index || part.Continuation.Offset != offset {
				t.Fatal("assembly order")
			}
			index++
			offset += uint64(len(part.Data))
			assembly.WriteString(part.Data)
			if !part.Continuation.Final {
				continue
			}
			env.Payload = json.RawMessage(assembly.String())
			assembly.Reset()
			index, offset = 0, 0
		}
		var batch protocol.ThreadItemsPayload
		if err := json.Unmarshal(env.Payload, &batch); err != nil {
			t.Fatal(err)
		}
		if batch.Final != (i == len(encoded)-1) {
			t.Fatal("assembly completion mistaken for final marker")
		}
		if batch.Final && (len(batch.Items) != 0 || batch.FromVersion == nil || *batch.FromVersion != from) {
			t.Fatal("final coverage")
		}
		got = append(got, batch.Items...)
	}
	count := 0
	for _, frame := range p.rec.snapshot() {
		if frame.ConnID == p.conn {
			count++
		}
	}
	if count != 1+hostedQueueLimit+len(encoded) {
		t.Fatalf("unexpected output count: %d", count)
	}
	if !reflect.DeepEqual(got, items) {
		t.Fatal("reconstructed content differs")
	}
}

func TestThreadDeliveryInputIsolation(t *testing.T) {
	for _, mode := range []string{"reuse", "admission_snapshot"} {
		t.Run(mode, func(t *testing.T) {
			p, _, _, release := testThreadHeld(t, nil)
			q := &testHostedPeer{t: t, mgr: p.mgr, frames: p.frames, rec: p.rec, conn: "thread-recipient", pub: p.pub, events: p.events}
			q.open(threadCaps)
			const payload = `{ "conversation_id": "known", "items": [], "final": true }`
			const conversation, session = ` "known" `, ` "session" `
			reply, event, history := uint64(40), uint64(50), uint64(60)
			env := protocol.Envelope{Type: protocol.TypeThreadItems, Payload: json.RawMessage(payload),
				ConversationID: json.RawMessage(conversation), SessionID: json.RawMessage(session),
				InReplyTo: &reply, EventID: &event, HistoryEntryID: &history}
			raw, err := json.Marshal(env)
			if err != nil {
				t.Fatal(err)
			}
			var want protocol.Envelope
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- p.mgr.DeliverThread(t.Context(), p.conn, env) }()
			testThreadQueued(t, p, 2)
			if string(env.Payload) != payload || string(env.ConversationID) != conversation || string(env.SessionID) != session || reply != 40 || event != 50 || history != 60 {
				t.Fatal("admission modified caller-owned payload or metadata")
			}
			peers := []*testHostedPeer{p, q}
			if mode == "admission_snapshot" {
				// Admission fences the snapshot; later producer changes cannot alter it.
				env.Payload[0], env.ConversationID[0], env.SessionID[0] = '!', '!', '!'
				reply, event, history = 41, 51, 61
				peers = peers[:1]
			}
			release()
			testThreadResult(t, done, nil)
			p.next()
			for _, peer := range peers {
				if peer == q {
					if err := p.mgr.DeliverThread(t.Context(), peer.conn, env); err != nil {
						t.Fatalf("reuse for another recipient: %v", err)
					}
				}
				frames := p.rec.snapshot()
				var got protocol.Envelope
				if err := json.Unmarshal(threadPlain(t, frames[len(frames)-1], peer.recv), &got); err != nil {
					t.Fatal(err)
				}
				if got.ID <= 1 {
					t.Fatal("missing Run-owned sender ID")
				}
				got.ID = want.ID
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("admitted content changed: got %+v, want %+v", got, want)
				}
			}
		})
	}
}

func TestThreadDeliveryFailure(t *testing.T) {
	for _, cause := range []string{"transport", "down", "encoding", "oversize"} {
		t.Run(cause, func(t *testing.T) {
			var down atomic.Bool
			p := testHostedStartCaps(t, &testAppDiscovery{}, threadCaps, func(c *V2SessionConfig) {
				c.ThreadReady = func() bool { return true }
				c.KnownConversation = func(string) bool { return true }
				c.Connected = func() bool { return !down.Load() }
				if cause == "transport" {
					c.HostedAppsSend = func(context.Context, protocol.RoutingEnvelope) error { return errors.New("send failed") }
				}
			})
			env := protocol.Envelope{Type: protocol.TypeThreadItems, Payload: json.RawMessage(`{"conversation_id":"known"}`)}
			if cause == "down" {
				down.Store(true)
			}
			if cause == "encoding" {
				env.Payload = json.RawMessage(`invalid`)
			}
			if cause == "oversize" {
				env.Payload = json.RawMessage(`{"conversation_id":"known","text":"` + strings.Repeat("x", protocol.MaxThreadEnvelopeBytes) + `"}`)
			}
			err := p.mgr.DeliverThread(t.Context(), p.conn, env)
			if !errors.Is(err, ErrThreadDeliveryFailed) {
				t.Fatalf("failure=%v", err)
			}
			down.Store(false)
			if cause == "down" {
				testHostedClose(t, p)
				p.open(threadCaps)
				env.Type = protocol.TypeThreadReset
				if retry := p.mgr.DeliverThread(t.Context(), p.conn, env); retry != nil {
					t.Fatal(retry)
				}
				recovered, _ := p.next()
				if recovered.Type != protocol.TypeThreadReset {
					t.Fatal("failed output retried after recovery")
				}
			}
		})
	}
}

func TestThreadDeliveryCancellation(t *testing.T) {
	for _, cause := range []string{"caller_queued", "caller_capacity", "disconnect", "manager", "replacement", "failure_capacity", "manager_capacity", "disconnect_capacity", "replacement_capacity", "revoked"} {
		t.Run(cause, func(t *testing.T) {
			p, _, exited, release := testThreadHeld(t, func(c *V2SessionConfig) {
				if cause == "failure_capacity" {
					held := c.HostedAppsSend
					c.HostedAppsSend = func(ctx context.Context, env protocol.RoutingEnvelope) error {
						if err := held(ctx, env); err != nil {
							return err
						}
						return errors.New("transport failed")
					}
				}
			})
			capacity := strings.HasSuffix(cause, "capacity")
			if capacity {
				for range hostedQueueLimit - 1 {
					if err := p.mgr.Push(t.Context(), p.conn, protocol.Envelope{Type: protocol.TypeWorkspaceUpdated, Payload: json.RawMessage(`{}`)}); err != nil {
						t.Fatal(err)
					}
				}
				testThreadQueued(t, p, hostedQueueLimit)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- p.mgr.DeliverThread(ctx, p.conn, protocol.Envelope{Type: protocol.TypeThreadItems, Payload: json.RawMessage(`{"conversation_id":"known"}`)})
			}()
			if !capacity {
				testThreadQueued(t, p, 2)
			}
			switch cause {
			case "caller_queued", "caller_capacity":
				cancel()
				testThreadResult(t, done, context.Canceled)
			case "revoked":
				p.mgr.cfg.Devices.Remove(v2TestDevName)
				testThreadResult(t, done, ErrConnNotFound)
			case "manager", "manager_capacity":
				p.stop()
				testThreadResult(t, done, ErrConnNotFound)
			case "failure_capacity":
				release()
				testThreadResult(t, done, ErrConnNotFound)
			default:
				p.frames <- protocol.RoutingEnvelope{ConnID: p.conn, CloseCode: 1000}
				testHostedAwait(t, exited)
				testThreadResult(t, done, ErrConnNotFound)
				if cause == "replacement" || cause == "replacement_capacity" {
					p.open(append(append([]string{}, threadCaps...), protocol.CapabilityHostedAppsV1))
					release()
					if err := p.mgr.DeliverThread(t.Context(), p.conn, protocol.Envelope{Type: protocol.TypeThreadReset, Payload: json.RawMessage(`{"conversation_id":"known"}`)}); err != nil {
						t.Fatal(err)
					}
					p.next()
				}
			}
			release()
		})
	}
}

func TestThreadDeliveryActiveCancellation(t *testing.T) {
	for _, action := range []string{"cancel", "accept"} {
		t.Run(action, func(t *testing.T) {
			entered, exited, gate := make(chan struct{}), make(chan struct{}), make(chan struct{})
			p := testHostedStartCaps(t, &testAppDiscovery{}, threadCaps, func(c *V2SessionConfig) {
				c.ThreadReady = func() bool { return true }
				c.KnownConversation = func(string) bool { return true }
				out := c.HostedAppsSend
				c.HostedAppsSend = func(ctx context.Context, e protocol.RoutingEnvelope) error {
					if e.CloseCode != 0 {
						return nil
					}
					close(entered)
					defer close(exited)
					select {
					case <-gate:
						return out(ctx, e)
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- p.mgr.DeliverThread(ctx, p.conn, protocol.Envelope{Type: protocol.TypeThreadItems, Payload: json.RawMessage(`{"conversation_id":"known"}`)})
			}()
			testHostedAwait(t, entered)
			select {
			case err := <-done:
				t.Fatalf("transport handoff reported outcome: %v", err)
			default:
			}
			if action == "accept" {
				close(gate)
				testThreadResult(t, done, nil)
				env, _ := p.next()
				if env.Type != protocol.TypeThreadItems {
					t.Fatal(env.Type)
				}
			} else {
				cancel()
				testThreadResult(t, done, context.Canceled)
				testHostedAwait(t, exited)
				testHostedGone(t, p)
				p.waitEvent("v2.hosted.delivery_failed", "")
				if len(p.rec.snapshot()) != 1 {
					t.Fatal("canceled held output published")
				}
			}
		})
	}
}

func TestThreadDeliveryCapabilityGates(t *testing.T) {
	for _, caps := range [][]string{{protocol.CapabilityThread}} {
		p := testHostedStartCaps(t, &testAppDiscovery{}, caps, func(c *V2SessionConfig) {
			c.ThreadReady = func() bool { return true }
			c.KnownConversation = func(string) bool { return true }
		})
		err := p.mgr.DeliverThread(t.Context(), p.conn, protocol.Envelope{Type: protocol.TypeThreadReset, Payload: json.RawMessage(`{"conversation_id":"known"}`)})
		if !errors.Is(err, ErrThreadWithheld) {
			t.Fatalf("capabilities %v: %v", caps, err)
		}
	}
}

func TestThreadDeliveryStampFailure(t *testing.T) {
	for _, cause := range []string{"sender_id", "stamped_size"} {
		t.Run(cause, func(t *testing.T) {
			m, s, _ := testHostedCapacityManager(t)
			s.hosted.inbox.used = 0
			s.thread, s.interactive = true, true
			m.cfg.KnownConversation = func(string) bool { return true }
			m.cfg.Devices = v2PairedRegistry(t, v2TestToken)
			device := m.cfg.Devices.List()[0]
			s.device = &device
			m.cfg.HostedAppsSend = func(_ context.Context, e protocol.RoutingEnvelope) error {
				if e.CloseCode == 0 {
					t.Error("failed encoding reached transport")
				}
				return nil
			}
			s.nextID = hostedMaxID
			env := protocol.Envelope{Type: protocol.TypeThreadItems, Payload: json.RawMessage(`{"conversation_id":"known","text":""}`)}
			if cause == "sender_id" {
				s.nextID++
			} else {
				raw, _ := json.Marshal(env)
				env.Payload = json.RawMessage(`{"conversation_id":"known","text":"` + strings.Repeat("x", protocol.MaxThreadEnvelopeBytes-len(raw)-8) + `"}`)
			}
			done := make(chan error, 1)
			m.hostedEnqueue(t.Context(), s, &hostedOutput{env: &env, outcome: done})
			testThreadResult(t, done, ErrThreadDeliveryFailed)
			if s.state != V2StateClosed {
				t.Fatal("failed output left connection open")
			}
		})
	}
}
