package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

type forwardingPauseLog struct {
	*admissionLogCapture
	ctx     context.Context
	entered chan struct{}
	resume  chan struct{}
}

func (h *forwardingPauseLog) Handle(ctx context.Context, r slog.Record) error {
	if err := h.admissionLogCapture.Handle(ctx, r); err != nil {
		return err
	}
	if r.Message == "relay: malformed routing envelope; dropping" {
		select {
		case h.entered <- struct{}{}:
		case <-h.ctx.Done():
			return nil
		}
		select {
		case <-h.resume:
		case <-h.ctx.Done():
		}
	}
	return nil
}

func TestReconnectWhileForwardingPaused(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"", testAdmissionToken} {
		t.Run(fmt.Sprintf("supplied=%v", token != ""), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			_, logs := testAdmissionLogger()
			pause := &forwardingPauseLog{admissionLogCapture: logs, ctx: ctx,
				entered: make(chan struct{}), resume: make(chan struct{})}
			var requests atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertAdmissionRequest(t, r, token)
				if requests.Add(1) == 1 {
					http.Error(w, testAdmissionDiagnostic, http.StatusServiceUnavailable)
					return
				}
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Errorf("Accept: %v", err)
					return
				}
				defer ws.CloseNow()
				_, raw, err := ws.Read(ctx)
				if err != nil {
					return
				}
				if err := ws.Write(ctx, websocket.MessageText, raw); err != nil {
					t.Errorf("echo: %v", err)
					return
				}
				if err := ws.Write(ctx, websocket.MessageText, []byte(`{`)); err != nil {
					t.Errorf("pause frame: %v", err)
					return
				}
				_, _, _ = ws.Read(ctx) // Keep the socket live until explicitly dropped.
			}))
			defer srv.Close()
			c, err := Connect(ctx, testAdmissionConfig(srv.URL+"/v1/server?probe=public", token, slog.New(pause)))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				cancel() // Release a held diagnostic before joining forwarding.
				_ = c.Close()
				assertAdmissionSafeError(t, c.Wait())
				logs.assertSafe(t)
			}()
			frames := c.Frames()
			for socket := 0; socket < 3; socket++ {
				select {
				case <-c.Reconnected():
				case <-ctx.Done():
					t.Fatalf("missing reconnect notification: socket=%d requests=%d connected=%v", socket, requests.Load(), c.Connected())
				}
				want := protocol.RoutingEnvelope{ConnID: "phone-public", Frame: json.RawMessage(`{"probe":"public"}`)}
				if err := c.SendContext(ctx, want); err != nil {
					t.Fatalf("SendContext: %v", err)
				}
				select {
				case got, ok := <-frames:
					if !ok || got.ConnID != want.ConnID || string(got.Frame) != string(want.Frame) {
						t.Fatal("application frame changed across replacement")
					}
				case <-ctx.Done():
					t.Fatal("missing application frame")
				}
				select {
				case <-pause.entered:
				case <-ctx.Done():
					t.Fatal("forwarding never reached barrier")
				}
				if socket == 2 {
					break
				}
				old := c.client.Generation()
				if old == nil {
					t.Fatal("socket retired before controlled drop")
				}
				c.client.DropConn()
				tick := time.NewTicker(time.Millisecond)
				for g := c.client.Generation(); g == nil || g == old; g = c.client.Generation() {
					select {
					case <-tick.C:
					case <-ctx.Done():
						tick.Stop()
						t.Fatal("transport did not install replacement while forwarding paused")
					}
				}
				tick.Stop()
				t.Logf("socket=%d replacement installed while forwarding paused; requests=%d", socket, requests.Load())
				select {
				case pause.resume <- struct{}{}:
				case <-ctx.Done():
					t.Fatal("could not release forwarding")
				}
			}
			if requests.Load() != 4 {
				t.Fatalf("requests=%d, want one 503 and three upgrades", requests.Load())
			}
		})
	}
}

func TestReconnectForwardingShutdown(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"close", "cancel"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			written := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Errorf("Accept: %v", err)
					return
				}
				defer ws.CloseNow()
				if err := ws.Write(ctx, websocket.MessageText, []byte(`{"conn_id":"public","frame":{}}`)); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
				close(written)
				_, _, _ = ws.Read(ctx)
			}))
			defer srv.Close()
			c := newTestConnection(t, ctx, "ws"+srv.URL[len("http"):])
			defer c.Close()
			bound, stop := context.WithTimeout(t.Context(), 8*time.Second)
			defer stop()
			select {
			case <-written:
			case <-bound.Done():
				t.Fatal("server did not write frame")
			}
			// No application consumer: shutdown must release forwarding backpressure.
			if method == "close" {
				_ = c.Close()
			} else {
				cancel()
			}
			select {
			case <-c.done:
				_ = c.Wait()
			case <-bound.Done():
				t.Fatal("shutdown did not join forwarding")
			}
			if _, ok := <-c.Frames(); ok {
				t.Fatal("Frames remained open after shutdown")
			}
		})
	}
}
