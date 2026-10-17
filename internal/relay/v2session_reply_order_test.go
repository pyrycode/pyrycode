package relay

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// replyHandoffLog holds the off-Run handoff, leaving pushes and control work free.
type replyHandoffLog struct {
	slog.Handler
	once                     sync.Once
	entered, release, exited chan struct{}
}

func TestV2Session_HeldReplyRetirement(t *testing.T) {
	for _, action := range []string{"disconnect", "cancel"} {
		t.Run(action, func(t *testing.T) {
			priv, pub := genV2Keypair(t)
			rec := &v2Recorder{}
			frames := make(chan protocol.RoutingEnvelope, 8)
			hold := newReplyHandoffLog(t)
			finished := make(chan error, 1)
			handler := func(ctx context.Context, c *dispatch.Conn, req protocol.Envelope) error {
				err := c.Reply(ctx, req, protocol.TypeAck, json.RawMessage(`{"ok":true}`))
				if req.ID == 71 {
					finished <- err
				}
				return err
			}
			mgr, stop := startManager(t, V2SessionConfig{Frames: frames, Outbound: rec.outbound, StaticPriv: priv, Devices: v2PairedRegistry(t, v2TestToken), ServerID: v2TestServerID, Logger: slog.New(hold), Handlers: map[string]dispatch.Handler{protocol.TypeListConversations: handler}})
			t.Cleanup(stop)
			t.Cleanup(func() { hold.once.Do(func() { close(hold.release) }) })
			send, _ := openModalConn(t, mgr, frames, rec, pub, "requester", nil)
			frames <- sealAppFrameConn(t, send, "requester", protocol.Envelope{ID: 71, Type: protocol.TypeListConversations, Payload: json.RawMessage(`{}`)})
			testHostedAwait(t, hold.entered)
			if action == "disconnect" {
				frames <- protocol.RoutingEnvelope{ConnID: "requester", CloseCode: 1000}
			} else {
				stopped := make(chan struct{})
				go func() { stop(); close(stopped) }()
				testHostedAwait(t, stopped)
			}
			// No test release: teardown/cancellation must end the held send itself.
			testHostedAwait(t, hold.exited)
			select {
			case err := <-finished:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("retired send error = %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("retired handler stranded")
			}
			if got := noiseMsgsForConn(t, rec, "requester"); len(got) != 0 {
				t.Fatalf("retired reply emitted: %d", len(got))
			}
			if action == "disconnect" {
				// This helper scans only the new generation's handshake response.
				p := &testHostedPeer{t: t, mgr: mgr, frames: frames, rec: rec, conn: "requester", pub: pub}
				p.open(nil)
				p.request(72, protocol.TypeListConversations, `{}`)
				env := decryptAppFrame(t, waitForConnNoiseMsg(t, rec, "requester", 1)[0], p.recv)
				if env.Type != protocol.TypeAck || env.InReplyTo == nil || *env.InReplyTo != 72 || string(env.Payload) != `{"ok":true}` {
					t.Fatalf("replacement received stale or corrupt reply: %+v", env)
				}
			}
		})
	}
}

func TestV2Session_ReplyReceipt(t *testing.T) {
	for _, action := range []string{"process", "disconnect", "cancel"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			m := &V2SessionManager{appReply: make(chan appReplyMsg), cfg: V2SessionConfig{Logger: silentLogger()}}
			s := &V2Session{connID: "receipt", done: make(chan struct{})}
			result := make(chan bool, 1)
			go func() { result <- m.forwardToRun(ctx, s, protocol.RoutingEnvelope{}) }()
			var reply appReplyMsg
			select {
			case reply = <-m.appReply:
			case <-time.After(3 * time.Second):
				t.Fatal("reply handoff stranded")
			}
			select {
			case <-result:
				t.Fatal("Send completed on handoff rather than processing")
			default:
			}
			switch action {
			case "process":
				close(reply.processed)
			case "disconnect":
				close(s.done)
			case "cancel":
				cancel()
			}
			select {
			case ok := <-result:
				if ok != (action == "process") {
					t.Fatalf("completion=%v action=%s", ok, action)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("reply receipt wait stranded")
			}
		})
	}
}

func newReplyHandoffLog(t *testing.T) *replyHandoffLog {
	t.Helper()
	h := &replyHandoffLog{Handler: silentLogger().Handler(), entered: make(chan struct{}), release: make(chan struct{}), exited: make(chan struct{})}
	t.Cleanup(func() { h.once.Do(func() { close(h.release) }) })
	return h
}

func (h *replyHandoffLog) Enabled(context.Context, slog.Level) bool { return true }
func (h *replyHandoffLog) Handle(ctx context.Context, r slog.Record) error {
	event, conn := "", ""
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "event" {
			event = a.Value.String()
		}
		if a.Key == "conn_id" {
			conn = a.Value.String()
		}
		return true
	})
	if event == "v2.app_reply.ready" && conn == "requester" {
		select {
		case <-h.entered:
			return nil
		default:
		}
		close(h.entered)
		defer close(h.exited)
		select {
		case <-h.release:
		case <-ctx.Done():
		}
	}
	return nil
}

// awaitReplyOrAnnouncement observes either the announcer completing or Send
// blocked in the held handoff on the handler's own stack. A scheduling delay
// cannot be mistaken for a synchronous Send boundary.
func awaitReplyOrAnnouncement(t *testing.T, announced <-chan struct{}) bool {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	stack := make([]byte, 64<<10)
	for {
		n := runtime.Stack(stack, true)
		if n == len(stack) {
			stack = make([]byte, 2*len(stack))
			continue
		}
		for _, g := range strings.Split(string(stack[:n]), "\n\n") {
			if strings.Contains(g, "(*replyHandoffLog).Handle(") && strings.Contains(g, "dispatch.(*Conn).Send(") {
				return false
			}
		}
		select {
		case <-announced:
			return true
		case <-deadline.C:
			t.Fatal("neither synchronous Send nor announcement progressed")
		case <-tick.C:
		}
	}
}

func TestV2Session_ReplyBeforeAnnouncement(t *testing.T) {
	for _, ordered := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "ordered"}[ordered], func(t *testing.T) {
			priv, pub := genV2Keypair(t)
			rec := &v2Recorder{}
			frames := make(chan protocol.RoutingEnvelope, 8)
			hold := newReplyHandoffLog(t)
			announced := make(chan struct{})
			finished := make(chan struct{})
			deliveryEntered, deliveryRelease := make(chan struct{}), make(chan struct{})
			var deliveryOnce, releaseOnce sync.Once
			releaseDelivery := func() { releaseOnce.Do(func() { close(deliveryRelease) }) }
			payload := json.RawMessage(`{"conversation_id":"conv","muted":true}`)
			var mgr *V2SessionManager
			handler := func(ctx context.Context, c *dispatch.Conn, req protocol.Envelope) error {
				defer close(finished)
				if err := c.Reply(ctx, req, protocol.TypeAck, payload); err != nil {
					return err
				}
				for _, peer := range mgr.ActiveConns(ctx) {
					if peer.Interactive {
						if err := mgr.Push(ctx, peer.ConnID, protocol.Envelope{Type: protocol.TypeConversationUpdated, Payload: payload}); err != nil {
							return err
						}
					}
				}
				close(announced)
				return nil
			}
			cfg := V2SessionConfig{Frames: frames, Outbound: rec.outbound, StaticPriv: priv, Devices: v2PairedRegistry(t, v2TestToken), ServerID: v2TestServerID, Logger: slog.New(hold), Handlers: map[string]dispatch.Handler{protocol.TypeListConversations: handler}}
			if ordered {
				cfg.HostedAppsSend = func(ctx context.Context, e protocol.RoutingEnvelope) error {
					if e.ConnID == "requester" {
						deliveryOnce.Do(func() { close(deliveryEntered) })
						select {
						case <-deliveryRelease:
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					if ctx.Err() != nil {
						return ctx.Err()
					}
					return rec.outbound(e)
				}
			}
			var stop func()
			mgr, stop = startManager(t, cfg)
			t.Cleanup(stop)
			// Release before manager cleanup on any failed assertion.
			t.Cleanup(func() {
				hold.once.Do(func() { close(hold.release) })
				releaseDelivery()
			})
			caps := []string{protocol.CapabilityInteractive}
			send, recv := openModalConn(t, mgr, frames, rec, pub, "requester", caps)
			_, siblingRecv := openModalConn(t, mgr, frames, rec, pub, "sibling", caps)
			frames <- sealAppFrameConn(t, send, "requester", protocol.Envelope{ID: 71, Type: protocol.TypeListConversations, Payload: json.RawMessage(`{}`)})
			testHostedAwait(t, hold.entered)
			// The handoff is held, not Run: unrelated control must still complete.
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if peers := mgr.ActiveConns(ctx); len(peers) != 2 || ctx.Err() != nil {
				t.Fatalf("control stalled: %v", ctx.Err())
			}
			if awaitReplyOrAnnouncement(t, announced) {
				// On the broken implementation, force the push onto the wire while
				// its correlated reply is still held at the competing handoff.
				releaseDelivery()
				waitForConnNoiseMsg(t, rec, "requester", 1)
			}
			hold.once.Do(func() { close(hold.release) })
			testHostedAwait(t, finished)
			if ordered {
				testHostedAwait(t, deliveryEntered)
				// On the repaired path the announcement completes while the first
				// transport send is held: Reply certifies processing, not receipt.
			}
			releaseDelivery()
			msgs := waitForConnNoiseMsg(t, rec, "requester", 2)
			reply := decryptAppFrame(t, msgs[0], recv)
			broadcast := decryptAppFrame(t, msgs[1], recv)
			if reply.Type != protocol.TypeAck || reply.InReplyTo == nil || *reply.InReplyTo != 71 || string(reply.Payload) != string(payload) {
				t.Fatalf("broadcast preceded correlated reply: %+v", reply)
			}
			if broadcast.Type != protocol.TypeConversationUpdated || broadcast.InReplyTo != nil || string(broadcast.Payload) != string(payload) {
				t.Fatalf("requester broadcast: %+v", broadcast)
			}
			other := decryptAppFrame(t, waitForConnNoiseMsg(t, rec, "sibling", 1)[0], siblingRecv)
			if other.Type != broadcast.Type || other.InReplyTo != nil || string(other.Payload) != string(payload) {
				t.Fatalf("sibling broadcast: %+v", other)
			}
		})
	}
}
