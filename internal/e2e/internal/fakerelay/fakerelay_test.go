package fakerelay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func newServer(t *testing.T) *Server {
	t.Helper()
	s := New(testLogger(t))
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// dialBinary opens /v1/server. overrideHdr replaces / clears the default
// server-id header; pass an empty string in the value to omit the header
// entirely (header-validation tests rely on this).
func dialBinary(ctx context.Context, t *testing.T, s *Server, serverID string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	hdr := http.Header{}
	if serverID != "" {
		hdr.Set("X-Pyrycode-Server", serverID)
	}
	return websocket.Dial(ctx, s.URL()+"/v1/server", &websocket.DialOptions{HTTPHeader: hdr})
}

// dialPhone opens /v1/client. Any of the three headers may be the empty
// string to test the missing-header rejection paths.
func dialPhone(ctx context.Context, t *testing.T, s *Server, serverID, token, deviceName string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	hdr := http.Header{}
	if serverID != "" {
		hdr.Set("X-Pyrycode-Server", serverID)
	}
	if token != "" {
		hdr.Set("X-Pyrycode-Token", token)
	}
	if deviceName != "" {
		hdr.Set("X-Pyrycode-Device-Name", deviceName)
	}
	return websocket.Dial(ctx, s.URL()+"/v1/client", &websocket.DialOptions{HTTPHeader: hdr})
}

func readJSON(ctx context.Context, t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal %q: %v", string(data), err)
	}
}

func writeText(ctx context.Context, t *testing.T, conn *websocket.Conn, data []byte) {
	t.Helper()
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func dialCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 3*time.Second)
}

func TestURL_IsWebSocketScheme(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	if !strings.HasPrefix(s.URL(), "ws://") {
		t.Fatalf("URL = %q, want ws:// prefix", s.URL())
	}
	if strings.HasSuffix(s.URL(), "/") {
		t.Fatalf("URL = %q, want no trailing slash", s.URL())
	}
}

func TestBinaryUpgrade_RequiresServerHeader(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	ctx, cancel := dialCtx(t)
	defer cancel()

	_, resp, err := dialBinary(ctx, t, s, "")
	if err == nil {
		t.Fatal("expected dial to fail without x-pyrycode-server header")
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %v, want 400; err=%v", statusOf(resp), err)
	}
}

func TestBinaryUpgrade_FirstClaimWins(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	ctx, cancel := dialCtx(t)
	defer cancel()

	connA, _, err := dialBinary(ctx, t, s, "alpha")
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	t.Cleanup(func() { _ = connA.Close(websocket.StatusNormalClosure, "") })

	_, resp, err := dialBinary(ctx, t, s, "alpha")
	if err == nil {
		t.Fatal("expected second dial for same server-id to fail")
	}
	if resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %v, want 409; err=%v", statusOf(resp), err)
	}

	// Release: closing connA must free the server-id immediately
	// (no grace period in the harness per AC).
	_ = connA.Close(websocket.StatusNormalClosure, "bye")

	// Wait for the cleanup to land — server cleanup is async w.r.t. the
	// peer's Close() return.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		connC, _, err := dialBinary(ctx, t, s, "alpha")
		if err == nil {
			_ = connC.Close(websocket.StatusNormalClosure, "")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server-id never released after first holder closed")
}

func TestPhoneUpgrade_RequiresAllHeaders(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	// Bind a binary so the only failure mode under test is a missing
	// phone-side header (not "no binary online").
	ctx, cancel := dialCtx(t)
	defer cancel()
	bin, _, err := dialBinary(ctx, t, s, "alpha")
	if err != nil {
		t.Fatalf("bind binary: %v", err)
	}
	t.Cleanup(func() { _ = bin.Close(websocket.StatusNormalClosure, "") })

	cases := []struct {
		name                        string
		serverID, token, deviceName string
	}{
		{"missing server", "", "tok", "dev"},
		{"missing token", "alpha", "", "dev"},
		{"missing device name", "alpha", "tok", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := dialCtx(t)
			defer cancel()
			_, resp, err := dialPhone(ctx, t, s, tc.serverID, tc.token, tc.deviceName)
			if err == nil {
				t.Fatal("expected dial to fail")
			}
			if resp == nil || resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %v, want 400; err=%v", statusOf(resp), err)
			}
		})
	}
}

func TestPhoneUpgrade_NoBinaryOnline(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	ctx, cancel := dialCtx(t)
	defer cancel()

	_, resp, err := dialPhone(ctx, t, s, "ghost", "tok", "dev")
	if err == nil {
		t.Fatal("expected dial to fail when no binary is bound")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %v, want 503; err=%v", statusOf(resp), err)
	}
}

func TestPhoneToBinary_FrameWrappedWithConnID(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	ctx, cancel := dialCtx(t)
	defer cancel()

	bin, _, err := dialBinary(ctx, t, s, "alpha")
	if err != nil {
		t.Fatalf("bind binary: %v", err)
	}
	t.Cleanup(func() { _ = bin.Close(websocket.StatusNormalClosure, "") })

	phone, _, err := dialPhone(ctx, t, s, "alpha", "tok", "dev")
	if err != nil {
		t.Fatalf("dial phone: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close(websocket.StatusNormalClosure, "") })

	frame := []byte(`{"id":1,"type":"hello"}`)
	writeText(ctx, t, phone, frame)

	var got protocol.RoutingEnvelope
	readJSON(ctx, t, bin, &got)
	if got.ConnID != "c-1" {
		t.Errorf("conn_id = %q, want %q", got.ConnID, "c-1")
	}
	if string(got.Frame) != string(frame) {
		t.Errorf("frame = %q, want %q", string(got.Frame), string(frame))
	}
}

func TestBinaryToPhone_FrameUnwrapped(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	ctx, cancel := dialCtx(t)
	defer cancel()

	bin, _, err := dialBinary(ctx, t, s, "alpha")
	if err != nil {
		t.Fatalf("bind binary: %v", err)
	}
	t.Cleanup(func() { _ = bin.Close(websocket.StatusNormalClosure, "") })

	phone, _, err := dialPhone(ctx, t, s, "alpha", "tok", "dev")
	if err != nil {
		t.Fatalf("dial phone: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close(websocket.StatusNormalClosure, "") })

	// First ensure the binary has learned the phone's conn_id. We round-
	// trip a phone→binary frame, read on the binary, then reply.
	writeText(ctx, t, phone, []byte(`{"id":1,"type":"hello"}`))
	var inbound protocol.RoutingEnvelope
	readJSON(ctx, t, bin, &inbound)

	reply := json.RawMessage(`{"id":2,"type":"reply"}`)
	wrapped, err := json.Marshal(protocol.RoutingEnvelope{ConnID: inbound.ConnID, Frame: reply})
	if err != nil {
		t.Fatalf("marshal wrapper: %v", err)
	}
	writeText(ctx, t, bin, wrapped)

	_, got, err := phone.Read(ctx)
	if err != nil {
		t.Fatalf("phone read: %v", err)
	}
	if string(got) != string(reply) {
		t.Errorf("phone got %q, want %q", string(got), string(reply))
	}
}

func TestConnIDIncrementsPerPhone(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	ctx, cancel := dialCtx(t)
	defer cancel()

	bin, _, err := dialBinary(ctx, t, s, "alpha")
	if err != nil {
		t.Fatalf("bind binary: %v", err)
	}
	t.Cleanup(func() { _ = bin.Close(websocket.StatusNormalClosure, "") })

	for i, want := range []string{"c-1", "c-2"} {
		ph, _, err := dialPhone(ctx, t, s, "alpha", "tok", "dev")
		if err != nil {
			t.Fatalf("phone %d dial: %v", i, err)
		}
		// Phone i sends one frame; binary reads it and we assert the
		// conn_id assigned.
		writeText(ctx, t, ph, []byte(`{"id":1,"type":"hello"}`))
		var env protocol.RoutingEnvelope
		readJSON(ctx, t, bin, &env)
		if env.ConnID != want {
			t.Errorf("phone %d: conn_id = %q, want %q", i, env.ConnID, want)
		}
		_ = ph.Close(websocket.StatusNormalClosure, "")
	}
}

func TestPhoneClosedWhenBinaryGoes(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	ctx, cancel := dialCtx(t)
	defer cancel()

	bin, _, err := dialBinary(ctx, t, s, "alpha")
	if err != nil {
		t.Fatalf("bind binary: %v", err)
	}
	phone, _, err := dialPhone(ctx, t, s, "alpha", "tok", "dev")
	if err != nil {
		t.Fatalf("dial phone: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close(websocket.StatusNormalClosure, "") })

	if err := bin.Close(websocket.StatusNormalClosure, "bye"); err != nil {
		t.Fatalf("close binary: %v", err)
	}

	// The relay must close the orphaned phone within a short window.
	readCtx, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readCancel()
	_, _, readErr := phone.Read(readCtx)
	if readErr == nil {
		t.Fatal("expected phone Read to error after binary disconnect")
	}
	if errors.Is(readErr, context.DeadlineExceeded) {
		t.Fatalf("phone Read timed out; harness did not drop the orphaned phone: %v", readErr)
	}
}

func TestServerClose_NoGoroutineLeaks(t *testing.T) {
	// Not Parallel: NumGoroutine() compares to a baseline.
	baseline := runtime.NumGoroutine()

	s := New(testLogger(t))
	ctx, cancel := dialCtx(t)
	defer cancel()

	bin, _, err := dialBinary(ctx, t, s, "alpha")
	if err != nil {
		t.Fatalf("bind binary: %v", err)
	}
	ph1, _, err := dialPhone(ctx, t, s, "alpha", "tok", "dev")
	if err != nil {
		t.Fatalf("phone 1: %v", err)
	}
	ph2, _, err := dialPhone(ctx, t, s, "alpha", "tok", "dev")
	if err != nil {
		t.Fatalf("phone 2: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Server.Close did not return within 1s")
	}

	// Close client-side conns explicitly; coder/websocket leaves their
	// state alive until the caller drops the handle.
	_ = bin.Close(websocket.StatusNormalClosure, "")
	_ = ph1.Close(websocket.StatusNormalClosure, "")
	_ = ph2.Close(websocket.StatusNormalClosure, "")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= baseline+2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutine leak: baseline=%d now=%d", baseline, runtime.NumGoroutine())
}

func TestClose_Idempotent(t *testing.T) {
	t.Parallel()
	s := New(testLogger(t))
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestBinaryHello_GetsHelloAck(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	ctx, cancel := dialCtx(t)
	defer cancel()

	bin, _, err := dialBinary(ctx, t, s, "alpha")
	if err != nil {
		t.Fatalf("bind binary: %v", err)
	}
	t.Cleanup(func() { _ = bin.Close(websocket.StatusNormalClosure, "") })

	hello := protocol.Envelope{
		ID:      42,
		Type:    protocol.TypeHello,
		Payload: json.RawMessage(`{"role":"server","server_id":"alpha","binary_version":"0.10.0","protocol_versions":["v1"]}`),
	}
	raw, err := json.Marshal(hello)
	if err != nil {
		t.Fatalf("marshal hello: %v", err)
	}
	writeText(ctx, t, bin, raw)

	var ackWrap protocol.RoutingEnvelope
	readJSON(ctx, t, bin, &ackWrap)
	if ackWrap.ConnID != "-" {
		t.Errorf("ack conn_id = %q, want %q", ackWrap.ConnID, "-")
	}
	var ack protocol.Envelope
	if err := json.Unmarshal(ackWrap.Frame, &ack); err != nil {
		t.Fatalf("decode inner ack: %v", err)
	}
	if ack.Type != protocol.TypeHelloAck {
		t.Errorf("ack.Type = %q, want %q", ack.Type, protocol.TypeHelloAck)
	}
	if ack.InReplyTo == nil || *ack.InReplyTo != hello.ID {
		t.Errorf("ack.InReplyTo = %v, want %d", ack.InReplyTo, hello.ID)
	}

	// Server-side capture mirrors what the binary sent.
	got, ok := s.LastBinaryHello("alpha")
	if !ok {
		t.Fatal("LastBinaryHello(alpha) not recorded")
	}
	if got.Type != protocol.TypeHello || got.ID != hello.ID {
		t.Errorf("LastBinaryHello: got id=%d type=%q, want id=%d type=%q",
			got.ID, got.Type, hello.ID, protocol.TypeHello)
	}
}

func TestRejectNextBinaryWith4409(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	s.RejectNextBinaryWith4409()

	ctx, cancel := dialCtx(t)
	defer cancel()

	conn, _, err := dialBinary(ctx, t, s, "alpha")
	if err != nil {
		// Some dial paths surface the immediate close as a dial error;
		// that's fine — the assertion below covers the post-accept case.
		return
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })

	_, _, err = conn.Read(ctx)
	if err == nil {
		t.Fatal("expected Read to fail after 4409 close")
	}
	if got := websocket.CloseStatus(err); got != websocket.StatusCode(4409) {
		t.Fatalf("close status = %v, want 4409 (err=%v)", got, err)
	}

	// Flag is one-shot: a follow-up dial succeeds normally.
	conn2, _, err := dialBinary(ctx, t, s, "beta")
	if err != nil {
		t.Fatalf("post-flag-clear dial: %v", err)
	}
	_ = conn2.Close(websocket.StatusNormalClosure, "")
}

func TestForceCloseBinary(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	ctx, cancel := dialCtx(t)
	defer cancel()

	bin, _, err := dialBinary(ctx, t, s, "alpha")
	if err != nil {
		t.Fatalf("bind binary: %v", err)
	}
	t.Cleanup(func() { _ = bin.Close(websocket.StatusNormalClosure, "") })

	if !s.WaitBinary(ctx, "alpha") {
		t.Fatal("binary registration did not complete")
	}

	if !s.ForceCloseBinary("alpha") {
		t.Fatal("ForceCloseBinary returned false for live binary")
	}
	if _, _, err := bin.Read(ctx); err == nil {
		t.Fatal("expected Read to fail after force close")
	}

	if s.ForceCloseBinary("ghost") {
		t.Error("ForceCloseBinary returned true for unknown server-id")
	}
}

func TestWaitBinary(t *testing.T) {
	t.Parallel()

	t.Run("happy path", func(t *testing.T) {
		t.Parallel()
		s := newServer(t)
		ctx, cancel := dialCtx(t)
		defer cancel()

		bin, _, err := dialBinary(ctx, t, s, "alpha")
		if err != nil {
			t.Fatalf("bind binary: %v", err)
		}
		t.Cleanup(func() { _ = bin.Close(websocket.StatusNormalClosure, "") })

		if !s.WaitBinary(ctx, "alpha") {
			t.Fatal("WaitBinary returned false for live binary")
		}
	})

	t.Run("timeout path", func(t *testing.T) {
		t.Parallel()
		s := newServer(t)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()

		if s.WaitBinary(ctx, "ghost") {
			t.Fatal("WaitBinary returned true for unregistered server-id")
		}
	})
}

func statusOf(resp *http.Response) any {
	if resp == nil {
		return "<no response>"
	}
	return resp.StatusCode
}
