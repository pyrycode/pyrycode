package transport

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type testAcceptedSendGate struct {
	slog.Handler
	accepted chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (g *testAcceptedSendGate) Enabled(context.Context, slog.Level) bool { return true }
func (g *testAcceptedSendGate) Handle(ctx context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "event" && a.Value.String() == "transport.send.accepted" {
			g.once.Do(func() {
				close(g.accepted)
				<-g.release
			})
		}
		return true
	})
	return g.Handler.Handle(ctx, r)
}

func testAcceptedAwait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("accepted send fixture stalled")
		var zero T
		return zero
	}
}

// testAcceptedPropagation holds child cancellation after the parent is cancelled.
// Value hides the native cancel context so children use the AfterFunc contract.
type testAcceptedPropagation struct {
	context.Context
	registered chan struct{}
	started    chan struct{}
	release    chan struct{}
	done       chan struct{}
}

func (*testAcceptedPropagation) Value(any) any { return nil }

func (c *testAcceptedPropagation) AfterFunc(f func()) func() bool {
	var finished sync.Once
	finish := func() { finished.Do(func() { close(c.done) }) }
	stop := context.AfterFunc(c.Context, func() {
		defer finish()
		close(c.started)
		<-c.release
		f()
	})
	close(c.registered)
	return func() bool {
		stopped := stop()
		if stopped {
			finish()
		}
		return stopped
	}
}

func TestClientSendContextAcceptedCancellation(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			frames := make(chan []byte, 8)
			retired := make(chan struct{})
			registered := make(chan struct{})
			var generations atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				if generations.Add(1) == 1 {
					defer close(retired)
					close(registered)
				}
				for {
					_, frame, err := conn.Read(r.Context())
					if err != nil {
						return
					}
					frames <- frame
				}
			}))
			t.Cleanup(server.Close)
			gate := &testAcceptedSendGate{Handler: testLogger(t).Handler(), accepted: make(chan struct{}), release: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(gate.release) }) }
			c := newClientForTest(t, Config{URL: "ws" + strings.TrimPrefix(server.URL, "http"), Logger: slog.New(gate), WriteTimeout: 3 * time.Second}, testOpts{reconnectInitial: time.Millisecond, reconnectMax: time.Millisecond})
			pumpCtx, stopPump := context.WithCancel(t.Context())
			connectedDone := make(chan error, 1)
			go func() { connectedDone <- c.Connect(pumpCtx) }()
			t.Cleanup(func() {
				release()
				stopPump()
				_ = c.Close()
				testAcceptedAwait(t, connectedDone)
			})
			testAcceptedAwait(t, c.Connected())
			// Accept can return after the client has already observed its upgrade.
			// Pin retirement to this peer before a send can trigger a reconnect.
			testAcceptedAwait(t, registered)
			original := c.Generation()
			if original == nil {
				t.Fatal("missing original generation")
			}
			callerCtx, cancelCaller := context.WithCancel(t.Context())
			if mode == "deadline" {
				cancelCaller()
				callerCtx, cancelCaller = context.WithTimeout(t.Context(), 100*time.Millisecond)
			}
			defer cancelCaller()
			var propagation *testAcceptedPropagation
			if mode != "shutdown" {
				propagation = &testAcceptedPropagation{Context: callerCtx, registered: make(chan struct{}), started: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
				callerCtx = propagation
				t.Cleanup(func() {
					cancelCaller()
					close(propagation.release)
					select {
					case <-propagation.registered:
						testAcceptedAwait(t, propagation.done)
					default:
					}
				})
			}
			returned := make(chan error, 1)
			senderDone := make(chan struct{})
			go func() {
				defer close(senderDone)
				returned <- c.SendContext(callerCtx, []byte("must be discarded"))
			}()
			t.Cleanup(func() { release(); testAcceptedAwait(t, senderDone) })
			testAcceptedAwait(t, gate.accepted)
			want := error(context.Canceled)
			switch mode {
			case "cancel":
				cancelCaller()
			case "deadline":
				testAcceptedAwait(t, callerCtx.Done())
				want = context.DeadlineExceeded
			case "shutdown":
				stopPump()
			}
			if propagation != nil {
				testAcceptedAwait(t, propagation.started)
			}
			select {
			case <-returned:
				t.Fatal("sender returned before accepted request disposal")
			default:
			}
			release()
			err := testAcceptedAwait(t, returned)
			if err == nil || mode != "shutdown" && !errors.Is(err, want) {
				t.Errorf("completion = %v, want %v", err, want)
			}
			testAcceptedAwait(t, retired)
			select {
			case <-frames:
				t.Fatal("cancelled accepted frame reached peer")
			default:
			}
			if mode != "shutdown" {
				testAcceptedAwait(t, c.Connected())
				if fresh := c.Generation(); fresh == nil || fresh == original {
					t.Fatal("reconnect did not establish a fresh generation")
				}
				if err := c.SendContext(t.Context(), []byte("new generation")); err != nil {
					t.Fatal(err)
				}
				if frame := testAcceptedAwait(t, frames); string(frame) != "new generation" {
					t.Fatal("old request resumed on reconnect")
				}
			}
		})
	}
}

func TestClientSendContextPreAdmissionCancellation(t *testing.T) {
	r := newTestRelay(t)
	c := New(Config{URL: r.URL(), Logger: testLogger(t)})
	pumpCtx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.Connect(pumpCtx) }()
	t.Cleanup(func() { stop(); _ = c.Close(); testAcceptedAwait(t, done) })
	testAcceptedAwait(t, c.Connected())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.SendContext(ctx, []byte("discard")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := c.SendContext(t.Context(), []byte("healthy")); err != nil {
		t.Fatal(err)
	}
	readCtx, stopRead := context.WithTimeout(t.Context(), time.Second)
	defer stopRead()
	if frame, err := c.Receive(readCtx); err != nil || string(frame) != "healthy" || r.ConnCount() != 1 {
		t.Fatalf("healthy generation: received=%t err=%v connections=%d", string(frame) == "healthy", err, r.ConnCount())
	}
}
