package fakerelay

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

type reconnectLog struct {
	*retirementGate
	waiting chan struct{}
}

func (g *reconnectLog) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "fakerelay: awaiting binary replacement" {
		g.waiting <- struct{}{}
	}
	return g.retirementGate.Handle(ctx, r)
}

func TestReconnectBinaryRetiringRegistration(t *testing.T) {
	gate := &retirementGate{Handler: testLogger(t).Handler(), entered: make(chan struct{}), release: make(chan struct{})}
	log := &reconnectLog{retirementGate: gate, waiting: make(chan struct{}, 1)}
	s := New(slog.New(log))
	t.Cleanup(func() { _ = s.Close() })
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	t.Cleanup(release)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	old, _, err := dialBinary(ctx, t, s, "reconnect")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.CloseNow() })
	if !s.WaitBinary(ctx, "reconnect") {
		t.Fatal("initial registration missing")
	}
	// The helper must retire a live socket and reject its stale registration.
	short, stop := context.WithCancel(ctx)
	defer stop()
	result := make(chan bool, 1)
	go func() { result <- s.ReconnectBinary(short, "reconnect") }()
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("helper did not stop old binary pumps")
	}
	select {
	case <-log.waiting:
	case <-ctx.Done():
		t.Fatal("reconnect wait did not start")
	}
	select {
	case <-result:
		t.Fatal("retiring registration satisfied reconnect")
	default:
	}
	stop()
	select {
	case ok := <-result:
		if ok {
			t.Fatal("retiring registration satisfied reconnect")
		}
	case <-ctx.Done():
		t.Fatal("reconnect ignored cancellation")
	}
	release()
	if !s.WaitBinaryOffline(ctx, "reconnect") {
		t.Fatal("old registration did not retire")
	}
	for range 2 {
		// Exercise both an absent registration and a still-live predecessor.
		go func() { result <- s.ReconnectBinary(ctx, "reconnect") }()
		select {
		case <-log.waiting:
		case <-ctx.Done():
			t.Fatal("replacement wait did not start")
		}
		if !s.WaitBinaryOffline(ctx, "reconnect") {
			t.Fatal("predecessor remained registered")
		}
		select {
		case <-result:
			t.Fatal("reconnect completed before successor registration")
		default:
		}
		premature, response, err := dialPhone(ctx, t, s, "reconnect", "fixture-token", "premature")
		if premature != nil {
			_ = premature.CloseNow()
		}
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		if err == nil || status != http.StatusServiceUnavailable {
			t.Fatalf("dial during binary gap: status %d, error %v", status, err)
		}
		t.Log("direct phone reconnect during binary retirement returned HTTP 503; barrier still pending")
		next, _, err := dialBinary(ctx, t, s, "reconnect")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = next.CloseNow() })
		select {
		case ok := <-result:
			if !ok {
				t.Fatal("successor did not satisfy reconnect")
			}
		case <-ctx.Done():
			t.Fatal("successor wait stalled")
		}
		phone, _, err := dialPhone(ctx, t, s, "reconnect", "fixture-token", "current")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = phone.CloseNow() })
		writeText(ctx, t, phone, []byte(`{"type":"noise_init"}`))
		var routed protocol.RoutingEnvelope
		readJSON(ctx, t, next, &routed)
		if routed.ConnID == "" || string(routed.Frame) != `{"type":"noise_init"}` {
			t.Fatal("successor did not receive current phone frame")
		}
		_ = phone.CloseNow()
	}
}

func TestReconnectBinaryBounds(t *testing.T) {
	for _, mode := range []string{"deadline", "cancelled", "closed"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := &Server{log: testLogger(t), binaries: make(map[string]*binaryConn)}
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				if mode == "cancelled" {
					cancel()
				}
				if mode == "closed" {
					s.closed = true
				}
				start := time.Now()
				if s.ReconnectBinary(ctx, "absent") {
					t.Fatal("reconnect succeeded without successor")
				}
				want := time.Duration(0)
				if mode == "deadline" {
					want = time.Second
				}
				if elapsed := time.Since(start); elapsed != want {
					t.Fatalf("elapsed = %v, want %v", elapsed, want)
				}
			})
		})
	}
}
