package relay

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestHostedAssetQueuedRetirementSingleWriter(t *testing.T) {
	for _, retirement := range []string{"backend", "timeout"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%v", retirement, fail), func(t *testing.T) {
				// Drive the Run-owned lane synchronously so retirement happens at
				// head selection, before a sweep can remove the queued resource.
				m, s, _ := testHostedCapacityManager(t)
				defer m.retireHosted(s)
				m.cfg.Devices = v2PairedRegistry(t, v2TestToken)
				device := m.cfg.Devices.List()[0]
				s.device = &device
				var recv *noise.CipherState
				s.send, recv = testHostedAssetDeliveryCipher(t)
				m.hostedDeliveryDone = make(chan struct{}, 4)
				entered, release := make(chan struct{}), make(chan struct{})
				wires := make(chan protocol.RoutingEnvelope, 4)
				var sends atomic.Int32
				writerErr := errors.New("held writer failed")
				m.cfg.HostedAppsSend = func(ctx context.Context, e protocol.RoutingEnvelope) error {
					if e.CloseCode == 0 && sends.Add(1) == 1 {
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
							return ctx.Err()
						}
						if fail {
							return writerErr
						}
					}
					select {
					case <-ctx.Done():
						return ctx.Err()
					case wires <- e:
						return nil
					}
				}
				ctx, cancel := context.WithCancelCause(s.hosted.inbox.ctx)
				defer cancel(context.Canceled)
				now := time.Now()
				op := &hostedResource{s: s, inbox: s.hosted.inbox, id: 10, app: testAssetApp, ctx: ctx, cancel: cancel, accepted: now, deadline: now.Add(time.Hour), inactivity: time.Hour}
				code := "app.unavailable"
				if retirement == "backend" {
					done := make(chan struct{})
					close(done)
					op.backendDone = done
				} else {
					op.deadline = now.Add(-time.Second)
					code = "app.timeout"
				}
				resourceOutcome, controlOutcome := make(chan error, 1), make(chan error, 1)
				control := &hostedOutput{typ: protocol.TypeAppCancelled, reply: 20, payload: protocol.AppCancelPayload{RequestID: 999}, outcome: controlOutcome}
				h := s.hosted
				h.resources = map[uint64]*hostedResource{op.id: op}
				h.inbox.resourceUsed, h.inbox.used = 1, 1
				h.queue = []*hostedOutput{{resource: op, typ: protocol.TypeAppResponse, reply: op.id, outcome: resourceOutcome}, control}
				m.hostedDeliver(t.Context(), s)
				if h.active != control || len(h.queue) != 1 || h.queue[0].typ != protocol.TypeError || h.queue[0].reply != op.id || s.nextID != 3 {
					t.Fatalf("retirement replaced the control writer: active=%p want=%p queued=%d nextID=%d", h.active, control, len(h.queue), s.nextID)
				}
				if len(h.resources) != 0 || ctx.Err() == nil || h.inbox.resourceUsed != 0 || h.inbox.used != 2 || !errors.Is(<-resourceOutcome, context.Canceled) {
					t.Fatal("retired resource retained ownership or output capacity")
				}
				testHostedAwait(t, entered)
				m.hostedDeliver(t.Context(), s)
				if sends.Load() != 1 || s.nextID != 3 {
					t.Fatal("held writer allowed another sealed output")
				}
				close(release)
				testHostedAwait(t, m.hostedDeliveryDone)
				m.drainHostedDelivery(t.Context(), s)
				if fail {
					if !errors.Is(<-controlOutcome, ErrThreadDeliveryFailed) || s.state != V2StateClosed || m.sessions[s.connID] != nil || !h.inbox.retired || len(h.queue) != 0 {
						t.Fatal("failed sealed control did not retire the generation")
					}
					select {
					case e := <-wires:
						if e.CloseCode != uint16(StatusSessionGone) || len(e.Frame) != 0 {
							t.Fatalf("output after failed sealed control: %+v", e)
						}
					case <-time.After(time.Second):
						t.Fatal("missing terminal close")
					}
					if sends.Load() != 1 || s.nextID != 3 {
						t.Fatal("terminal error was sealed after delivery failure")
					}
					return
				}
				if err := <-controlOutcome; err != nil {
					t.Fatal(err)
				}
				e := decryptAppFrame(t, <-wires, recv)
				if e.Type != protocol.TypeAppCancelled || e.ID != 2 || e.InReplyTo == nil || *e.InReplyTo != 20 {
					t.Fatalf("control publication: %+v", e)
				}
				testHostedAwait(t, m.hostedDeliveryDone)
				m.drainHostedDelivery(t.Context(), s)
				e = decryptAppFrame(t, <-wires, recv)
				testAssetError(t, e, op.id, code, true)
				if e.ID != 3 || s.state != V2StateOpen || h.active != nil || len(h.queue) != 0 || h.inbox.used != 0 || sends.Load() != 2 {
					t.Fatal("terminal error did not follow successful control delivery")
				}
			})
		}
	}
}

func testHostedAssetDeliveryCipher(t *testing.T) (*noise.CipherState, *noise.CipherState) {
	t.Helper()
	priv, pub := genV2Keypair(t)
	init, err := noise.NewInitiator(v2TestInstallPriv, pub)
	if err != nil {
		t.Fatal(err)
	}
	responder, err := noise.NewResponder(priv)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := init.WriteInit(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := responder.ReadInit(msg); err != nil {
		t.Fatal(err)
	}
	msg, send, _, err := responder.WriteResp(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, recv, err := init.ReadResp(msg)
	if err != nil {
		t.Fatal(err)
	}
	return send, recv
}
