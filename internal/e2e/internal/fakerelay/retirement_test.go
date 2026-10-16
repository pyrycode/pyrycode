package fakerelay

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// retirementGate holds the first binary after pump shutdown, before deregistration.
type retirementGate struct {
	slog.Handler
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (g *retirementGate) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "fakerelay: binary pumps stopped" {
		g.once.Do(func() {
			close(g.entered)
			select {
			case <-g.release:
			case <-time.After(5 * time.Second):
			}
		})
	}
	return g.Handler.Handle(ctx, r)
}

func TestWaitBinaryOfflineRetiringRegistration(t *testing.T) {
	gate := &retirementGate{Handler: testLogger(t).Handler(), entered: make(chan struct{}), release: make(chan struct{})}
	s := New(slog.New(gate))
	t.Cleanup(func() { _ = s.Close() })
	var release sync.Once
	unblock := func() { release.Do(func() { close(gate.release) }) }
	t.Cleanup(unblock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	old, _, err := dialBinary(ctx, t, s, "restart")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.CloseNow() })
	if !s.WaitBinary(ctx, "restart") {
		t.Fatal("old registration missing")
	}
	_ = old.CloseNow()
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("old pumps did not stop")
	}
	t.Log("old binary socket closed and both relay pumps joined; deregistration held")
	if !s.WaitBinary(ctx, "restart") {
		t.Fatal("expected stale membership while retirement is held")
	}
	phone, _, err := dialPhone(ctx, t, s, "restart", "fixture-token", "premature")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = phone.CloseNow() })
	t.Log("membership-only readiness admitted phone upgrade against retiring binary")
	if err := phone.Write(ctx, websocket.MessageText, []byte(`{"type":"noise_init"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := phone.Read(ctx); err == nil {
		t.Fatal("premature phone unexpectedly received a handshake response")
	} else {
		t.Logf("premature handshake read failed: %v", err)
	}
	bound, cancelBound := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancelBound()
	if s.WaitBinaryOffline(bound, "restart") {
		t.Fatal("retiring registration satisfied offline barrier")
	}
	unblock()
	if !s.WaitBinaryOffline(ctx, "restart") {
		t.Fatal("retired registration did not disappear")
	}
	t.Log("retirement released; old registration absent before successor dial")
	next, _, err := dialBinary(ctx, t, s, "restart")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.CloseNow() })
	if !s.WaitBinary(ctx, "restart") {
		t.Fatal("successor registration missing")
	}
	current, _, err := dialPhone(ctx, t, s, "restart", "fixture-token", "current")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.CloseNow() })
	writeText(ctx, t, current, []byte(`{"type":"noise_init"}`))
	var routed protocol.RoutingEnvelope
	readJSON(ctx, t, next, &routed)
	if routed.ConnID == "" || string(routed.Frame) != `{"type":"noise_init"}` {
		t.Fatalf("successor did not receive current phone frame: %v", routed)
	}
	routed.Frame = json.RawMessage(`{"type":"noise_resp"}`)
	routed.Token = ""
	raw, err := json.Marshal(routed)
	if err != nil {
		t.Fatal(err)
	}
	writeText(ctx, t, next, raw)
	var response struct{ Type string }
	readJSON(ctx, t, current, &response)
	if response.Type != "noise_resp" {
		t.Fatalf("replacement response type = %q", response.Type)
	}
	t.Log("same-ID successor received init and routed response to current phone")
}

func TestWaitBinaryOfflineBounds(t *testing.T) {
	for _, mode := range []string{"absent", "deadline", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := &Server{binaries: make(map[string]*binaryConn)}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if mode != "absent" {
					s.binaries["restart"] = &binaryConn{}
				}
				if mode == "cancelled" {
					cancel()
				}
				start := time.Now()
				if got := s.WaitBinaryOffline(ctx, "restart"); got != (mode == "absent") {
					t.Fatalf("offline = %v for %s", got, mode)
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
