package fakephone

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// serverBehavior controls the scripted server-side loop after upgrade.
type serverBehavior int

const (
	// behaviorEcho reads each frame and writes it back verbatim.
	behaviorEcho serverBehavior = iota
	// behaviorIdle accepts the upgrade and never writes or reads. Useful
	// for timeout and close-during-read tests.
	behaviorIdle
	// behaviorSendOne sends a single pre-canned frame and then idles.
	behaviorSendOne
)

// newWSServer stands up an httptest server whose / handler upgrades and
// then runs the requested behavior. If hdrCapture is non-nil, the
// incoming upgrade-request headers are copied into *hdrCapture before
// the upgrade completes. send is the bytes used for behaviorSendOne.
func newWSServer(t *testing.T, behavior serverBehavior, hdrCapture *http.Header, send []byte) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hdrCapture != nil {
			*hdrCapture = r.Header.Clone()
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Logf("server accept: %v", err)
			return
		}
		conn.SetReadLimit(maxFrameBytes)
		defer conn.Close(websocket.StatusNormalClosure, "")

		ctx := r.Context()
		switch behavior {
		case behaviorEcho:
			for {
				typ, data, err := conn.Read(ctx)
				if err != nil {
					return
				}
				if err := conn.Write(ctx, typ, data); err != nil {
					return
				}
			}
		case behaviorIdle:
			<-ctx.Done()
		case behaviorSendOne:
			if err := conn.Write(ctx, websocket.MessageText, send); err != nil {
				return
			}
			<-ctx.Done()
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func dialCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func sampleEnvelope() protocol.Envelope {
	ts, _ := time.Parse(time.RFC3339Nano, "2026-05-13T10:00:00Z")
	return protocol.Envelope{
		ID:      42,
		Type:    protocol.TypeHello,
		TS:      ts,
		Payload: json.RawMessage(`{}`),
	}
}

func TestDial_ForwardsHeaders(t *testing.T) {
	t.Parallel()
	var captured http.Header
	baseURL := newWSServer(t, behaviorIdle, &captured, nil)

	ctx, cancel := dialCtx(t)
	defer cancel()

	const (
		serverID   = "alpha"
		token      = "secret"
		deviceName = "Juhana's Pixel 8"
	)
	c, err := Dial(ctx, baseURL, serverID, token, deviceName)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if got := captured.Get("X-Pyrycode-Server"); got != serverID {
		t.Errorf("server header = %q, want %q", got, serverID)
	}
	if got := captured.Get("X-Pyrycode-Token"); got != token {
		t.Errorf("token header = %q, want %q", got, token)
	}
	if got := captured.Get("X-Pyrycode-Device-Name"); got != deviceName {
		t.Errorf("device-name header = %q, want %q", got, deviceName)
	}
}

func TestSend_RoundTripsThroughEcho(t *testing.T) {
	t.Parallel()
	baseURL := newWSServer(t, behaviorEcho, nil, nil)

	ctx, cancel := dialCtx(t)
	defer cancel()

	c, err := Dial(ctx, baseURL, "alpha", "tok", "dev")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	sent := sampleEnvelope()
	if err := c.Send(sent); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got, err := c.Receive(2 * time.Second)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if got.ID != sent.ID || got.Type != sent.Type {
		t.Errorf("envelope mismatch: got id=%d type=%q, want id=%d type=%q",
			got.ID, got.Type, sent.ID, sent.Type)
	}
	// Monotonic-clock reading strips on JSON marshal; compare via Equal.
	if !got.TS.Equal(sent.TS) {
		t.Errorf("ts mismatch: got %v, want %v", got.TS, sent.TS)
	}
	if string(got.Payload) != string(sent.Payload) {
		t.Errorf("payload mismatch: got %q, want %q", got.Payload, sent.Payload)
	}
}

func TestReceive_DecodesEnvelope(t *testing.T) {
	t.Parallel()
	sent := sampleEnvelope()
	data, err := json.Marshal(sent)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	baseURL := newWSServer(t, behaviorSendOne, nil, data)

	ctx, cancel := dialCtx(t)
	defer cancel()

	c, err := Dial(ctx, baseURL, "alpha", "tok", "dev")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.Receive(2 * time.Second)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if got.ID != sent.ID || got.Type != sent.Type {
		t.Errorf("envelope mismatch: got id=%d type=%q, want id=%d type=%q",
			got.ID, got.Type, sent.ID, sent.Type)
	}
}

func TestReceive_TimeoutReturnsSentinel(t *testing.T) {
	t.Parallel()
	baseURL := newWSServer(t, behaviorIdle, nil, nil)

	ctx, cancel := dialCtx(t)
	defer cancel()

	c, err := Dial(ctx, baseURL, "alpha", "tok", "dev")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.Receive(50 * time.Millisecond)
	if !errors.Is(err, ErrReceiveTimeout) {
		t.Fatalf("Receive: got %v, want ErrReceiveTimeout", err)
	}
}

func TestClose_UnblocksReceive(t *testing.T) {
	t.Parallel()
	baseURL := newWSServer(t, behaviorIdle, nil, nil)

	ctx, cancel := dialCtx(t)
	defer cancel()

	c, err := Dial(ctx, baseURL, "alpha", "tok", "dev")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var recvErr error
	go func() {
		defer wg.Done()
		_, recvErr = c.Receive(5 * time.Second)
	}()

	// Give Receive a moment to block on Read.
	time.Sleep(20 * time.Millisecond)

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Receive did not unblock within 500ms after Close")
	}

	if !errors.Is(recvErr, ErrClosed) {
		t.Errorf("Receive after Close: got %v, want ErrClosed", recvErr)
	}

	if err := c.Send(sampleEnvelope()); !errors.Is(err, ErrClosed) {
		t.Errorf("Send after Close: got %v, want ErrClosed", err)
	}
	if _, err := c.Receive(50 * time.Millisecond); !errors.Is(err, ErrClosed) {
		t.Errorf("Receive after Close: got %v, want ErrClosed", err)
	}
}

func TestClose_IsIdempotent(t *testing.T) {
	t.Parallel()
	baseURL := newWSServer(t, behaviorIdle, nil, nil)

	ctx, cancel := dialCtx(t)
	defer cancel()

	c, err := Dial(ctx, baseURL, "alpha", "tok", "dev")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestDial_FailsOnHandshakeError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	baseURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := dialCtx(t)
	defer cancel()

	c, err := Dial(ctx, baseURL, "alpha", "tok", "dev")
	if err == nil {
		_ = c.Close()
		t.Fatal("Dial succeeded against 403 handler; want error")
	}
}
