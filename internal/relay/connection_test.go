package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/transport"
)

// Tests in this file rely on the transport's production reconnect cadence
// (1s initial, ±20% jitter). The 5-second hello_ack deadline is the
// package-var handshakeTimeout which tests substitute via t.Cleanup.

const testServerID = "11111111-1111-4111-8111-111111111111"

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func shortenHandshakeTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := handshakeTimeout
	handshakeTimeout = d
	t.Cleanup(func() { handshakeTimeout = orig })
}

// relayBehavior controls how the test relay handles each accepted conn.
type relayBehavior int

const (
	behaviorHappyPath          relayBehavior = iota // hello → hello_ack
	behaviorSilentNoAck                             // accept, read hello, never reply
	behaviorCloseImmediately4409                    // close with 4409 on accept, before reading
	behaviorDropDuringHandshake                     // read hello, then CloseNow (1006-equivalent)
	behaviorSendBadType                             // read hello, send non-hello_ack envelope
)

// testRelay is a controllable httptest-hosted relay for connection tests.
type testRelay struct {
	server *httptest.Server

	mu       sync.Mutex
	conns    []*websocket.Conn
	headers  []http.Header
	helloEnv []protocol.Envelope

	connectedCh chan struct{}
	connCount   atomic.Int64

	// behavior is the configured response. switchAfter, when > 0,
	// switches behavior after that many accepted conns (used for tests
	// that fail the first conn then succeed on retry).
	mu2          sync.Mutex
	behavior     relayBehavior
	nextBehavior relayBehavior
	switchAfter  int

	// outboundFrames is pushed to by tests; the relay sends each frame
	// (already JSON-encoded) on the live conn after the hello_ack.
	outboundFrames chan []byte
}

func (r *testRelay) URL() string {
	return "ws" + strings.TrimPrefix(r.server.URL, "http")
}

func (r *testRelay) ConnCount() int64 { return r.connCount.Load() }

func (r *testRelay) Headers(i int) http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.headers) {
		return nil
	}
	return r.headers[i]
}

func (r *testRelay) HelloEnv(i int) (protocol.Envelope, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.helloEnv) {
		return protocol.Envelope{}, false
	}
	return r.helloEnv[i], true
}

func (r *testRelay) ForceCloseAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.conns {
		_ = c.CloseNow()
	}
}

func (r *testRelay) Close() {
	r.ForceCloseAll()
	r.server.Close()
}

func (r *testRelay) currentBehavior() relayBehavior {
	r.mu2.Lock()
	defer r.mu2.Unlock()
	idx := int(r.connCount.Load())
	if r.switchAfter > 0 && idx > r.switchAfter {
		return r.nextBehavior
	}
	return r.behavior
}

func newTestRelay(t *testing.T) *testRelay {
	t.Helper()
	r := &testRelay{
		connectedCh:    make(chan struct{}, 16),
		outboundFrames: make(chan []byte, 16),
		behavior:       behaviorHappyPath,
	}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Capture headers before WS upgrade.
		hdr := req.Header.Clone()
		conn, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		r.mu.Lock()
		r.conns = append(r.conns, conn)
		r.headers = append(r.headers, hdr)
		r.mu.Unlock()
		r.connCount.Add(1)
		select {
		case r.connectedCh <- struct{}{}:
		default:
		}
		behavior := r.currentBehavior()
		ctx := req.Context()
		defer func() {
			_ = conn.Close(websocket.StatusNormalClosure, "")
		}()

		if behavior == behaviorCloseImmediately4409 {
			_ = conn.Close(statusServerIDConflict, "server-id conflict")
			return
		}

		// Read the first frame (expected hello).
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err == nil {
			r.mu.Lock()
			r.helloEnv = append(r.helloEnv, env)
			r.mu.Unlock()
		}

		switch behavior {
		case behaviorSilentNoAck:
			// Hold the conn open; do nothing further until ctx done or
			// peer drops.
			<-ctx.Done()
			return
		case behaviorDropDuringHandshake:
			_ = conn.CloseNow()
			return
		case behaviorSendBadType:
			sendWrappedEnvelope(ctx, conn, protocol.Envelope{
				ID:      1,
				Type:    protocol.TypeError,
				TS:      time.Now().UTC(),
				Payload: json.RawMessage(`{}`),
			})
			<-ctx.Done()
			return
		case behaviorHappyPath:
			ackPayload, _ := json.Marshal(protocol.HelloAckPayload{
				ProtocolVersion: "v1",
				ServerID:        testServerID,
				ConnID:          "-",
			})
			one := uint64(1)
			ack := protocol.Envelope{
				ID:        1,
				Type:      protocol.TypeHelloAck,
				TS:        time.Now().UTC(),
				Payload:   ackPayload,
				InReplyTo: &one,
			}
			if err := sendWrappedEnvelope(ctx, conn, ack); err != nil {
				return
			}
			// Spawn a reader goroutine so a conn drop unblocks the
			// outbound loop. Without this, a handler whose conn is dead
			// would keep consuming from r.outboundFrames and starve the
			// next handler (the channel is shared across handlers).
			deadCh := make(chan struct{})
			go func() {
				defer close(deadCh)
				for {
					if _, _, err := conn.Read(ctx); err != nil {
						return
					}
				}
			}()
			for {
				select {
				case <-ctx.Done():
					return
				case <-deadCh:
					return
				case frame := <-r.outboundFrames:
					if err := conn.Write(ctx, websocket.MessageText, frame); err != nil {
						return
					}
				}
			}
		}
	}))
	t.Cleanup(r.Close)
	return r
}

func sendWrappedEnvelope(ctx context.Context, conn *websocket.Conn, env protocol.Envelope) error {
	inner, err := json.Marshal(env)
	if err != nil {
		return err
	}
	wrapped, err := json.Marshal(protocol.RoutingEnvelope{
		ConnID: "-",
		Frame:  inner,
	})
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, wrapped)
}

// newTestConnection builds a Connection that wraps a transport.Client
// pointed at relayURL (ws://...). Production callers use Connect; tests
// bypass the wss-only scheme check via connectWithClient.
func newTestConnection(t *testing.T, ctx context.Context, relayURL string) *Connection {
	t.Helper()
	cfg := Config{
		ServerID:      identity.ServerID(testServerID),
		RelayURL:      relayURL,
		BinaryVersion: "0.10.0-test",
		Logger:        testLogger(t),
	}
	headers := http.Header{}
	headers.Set("x-pyrycode-server", string(cfg.ServerID))
	headers.Set("x-pyrycode-version", cfg.BinaryVersion)
	headers.Set("user-agent", "pyry/"+cfg.BinaryVersion)
	tc := transport.New(transport.Config{
		URL:             relayURL,
		Headers:         headers,
		WriteTimeout:    time.Second,
		Logger:          testLogger(t),
		FatalCloseCodes: []websocket.StatusCode{statusServerIDConflict},
	})
	return connectWithClient(ctx, cfg, tc)
}

// --- tests ---

func TestConnect_HappyPath(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newTestConnection(t, ctx, relay.URL())
	t.Cleanup(func() { _ = c.Close() })

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := relay.HelloEnv(0); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	env, ok := relay.HelloEnv(0)
	if !ok {
		t.Fatal("relay never received hello envelope")
	}
	if env.Type != protocol.TypeHello {
		t.Errorf("env.Type = %q, want %q", env.Type, protocol.TypeHello)
	}
	var payload protocol.HelloServerPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("decode hello payload: %v", err)
	}
	if payload.Role != "server" {
		t.Errorf("payload.Role = %q, want %q", payload.Role, "server")
	}
	if payload.ServerID != testServerID {
		t.Errorf("payload.ServerID = %q, want %q", payload.ServerID, testServerID)
	}
	if payload.BinaryVersion != "0.10.0-test" {
		t.Errorf("payload.BinaryVersion = %q, want %q", payload.BinaryVersion, "0.10.0-test")
	}
	if len(payload.ProtocolVersions) != 1 || payload.ProtocolVersions[0] != "v1" {
		t.Errorf("payload.ProtocolVersions = %v, want [v1]", payload.ProtocolVersions)
	}
}

func TestHeaders_Set(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newTestConnection(t, ctx, relay.URL())
	t.Cleanup(func() { _ = c.Close() })

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if relay.Headers(0) != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	h := relay.Headers(0)
	if h == nil {
		t.Fatal("relay never observed a connection")
	}
	if got := h.Get("x-pyrycode-server"); got != testServerID {
		t.Errorf("x-pyrycode-server = %q, want %q", got, testServerID)
	}
	if got := h.Get("x-pyrycode-version"); got != "0.10.0-test" {
		t.Errorf("x-pyrycode-version = %q, want %q", got, "0.10.0-test")
	}
	if got := h.Get("user-agent"); got != "pyry/0.10.0-test" {
		t.Errorf("user-agent = %q, want %q", got, "pyry/0.10.0-test")
	}
}

func TestHandshake_AckTimeout(t *testing.T) {
	// Not t.Parallel: mutates the package-level handshakeTimeout var
	// which other parallel tests read.
	shortenHandshakeTimeout(t, 200*time.Millisecond)
	relay := newTestRelay(t)
	relay.behavior = behaviorSilentNoAck
	relay.switchAfter = 1
	relay.nextBehavior = behaviorHappyPath

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := newTestConnection(t, ctx, relay.URL())
	t.Cleanup(func() { _ = c.Close() })

	// After the first conn stalls, the relay closes (via DropConn).
	// Transport reconnects (~1s) and the second conn succeeds.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if relay.ConnCount() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := relay.ConnCount(); got < 2 {
		t.Fatalf("ConnCount = %d, want ≥ 2 (timeout did not trigger reconnect)", got)
	}
	// Second hello envelope should be present.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := relay.HelloEnv(1); ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := relay.HelloEnv(1); !ok {
		t.Error("second hello not observed (handshake did not retry)")
	}
}

func TestHandshake_UnexpectedFrame(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)
	relay.behavior = behaviorSendBadType
	relay.switchAfter = 1
	relay.nextBehavior = behaviorHappyPath

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := newTestConnection(t, ctx, relay.URL())
	t.Cleanup(func() { _ = c.Close() })

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if relay.ConnCount() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := relay.ConnCount(); got < 2 {
		t.Fatalf("ConnCount = %d, want ≥ 2 (bad first frame did not trigger reconnect)", got)
	}
}

func TestServerIDConflict_FatalNoReconnect(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)
	relay.behavior = behaviorCloseImmediately4409

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newTestConnection(t, ctx, relay.URL())
	t.Cleanup(func() { _ = c.Close() })

	waitDone := make(chan error, 1)
	go func() { waitDone <- c.Wait() }()

	select {
	case err := <-waitDone:
		if !errors.Is(err, ErrServerIDConflict) {
			t.Errorf("Wait returned %v, want ErrServerIDConflict", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not return after 4409 close")
	}
	if got := relay.ConnCount(); got != 1 {
		t.Errorf("ConnCount = %d, want 1 (no reconnect after fatal close)", got)
	}
}

func TestTransportDropDuringHandshake(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)
	relay.behavior = behaviorDropDuringHandshake
	relay.switchAfter = 1
	relay.nextBehavior = behaviorHappyPath

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := newTestConnection(t, ctx, relay.URL())
	t.Cleanup(func() { _ = c.Close() })

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if relay.ConnCount() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := relay.ConnCount(); got < 2 {
		t.Fatalf("ConnCount = %d, want ≥ 2 (drop did not trigger reconnect)", got)
	}
}

func TestTransportDropPostHandshake_ReHandshakes(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	c := newTestConnection(t, ctx, relay.URL())
	t.Cleanup(func() { _ = c.Close() })

	// Wait for first handshake.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := relay.HelloEnv(0); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := relay.HelloEnv(0); !ok {
		t.Fatal("first hello not observed")
	}
	// Wait for the relay to send hello_ack (allow a moment after the
	// hello arrival).
	time.Sleep(100 * time.Millisecond)
	relay.ForceCloseAll()

	// After drop, transport reconnects → second handshake.
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := relay.HelloEnv(1); ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := relay.HelloEnv(1); !ok {
		t.Fatal("second hello not observed after post-handshake drop")
	}

	// Confirm the rebuilt Frames pipeline delivers a frame.
	payload, _ := json.Marshal(map[string]string{"k": "v"})
	env := protocol.Envelope{
		ID:      2,
		Type:    protocol.TypeMessage,
		TS:      time.Now().UTC(),
		Payload: payload,
	}
	innerRaw, _ := json.Marshal(env)
	wrapped, _ := json.Marshal(protocol.RoutingEnvelope{
		ConnID: "c-after-reconnect",
		Frame:  innerRaw,
	})
	select {
	case relay.outboundFrames <- wrapped:
	case <-time.After(time.Second):
		t.Fatal("could not push outbound frame to relay")
	}
	select {
	case got := <-c.Frames():
		if got.ConnID != "c-after-reconnect" {
			t.Errorf("ConnID = %q, want %q", got.ConnID, "c-after-reconnect")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Frames did not deliver after re-handshake")
	}
}

func TestFrames_DeliversPostHandshakeInOrder(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := newTestConnection(t, ctx, relay.URL())
	t.Cleanup(func() { _ = c.Close() })

	// Wait for handshake.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := relay.HelloEnv(0); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := relay.HelloEnv(0); !ok {
		t.Fatal("handshake did not complete")
	}
	// Give the relay's per-conn goroutine time to enter the
	// outbound-frame loop after sending hello_ack.
	time.Sleep(100 * time.Millisecond)

	type want struct {
		connID string
		id     uint64
	}
	wants := []want{
		{"c-1", 2},
		{"c-2", 3},
		{"c-3", 4},
	}
	for _, w := range wants {
		env := protocol.Envelope{
			ID:      w.id,
			Type:    protocol.TypeMessage,
			TS:      time.Now().UTC(),
			Payload: json.RawMessage(`{}`),
		}
		innerRaw, _ := json.Marshal(env)
		wrapped, _ := json.Marshal(protocol.RoutingEnvelope{
			ConnID: w.connID,
			Frame:  innerRaw,
		})
		relay.outboundFrames <- wrapped
	}
	for i, w := range wants {
		select {
		case got := <-c.Frames():
			if got.ConnID != w.connID {
				t.Errorf("frame %d: ConnID = %q, want %q", i, got.ConnID, w.connID)
			}
			var env protocol.Envelope
			if err := json.Unmarshal(got.Frame, &env); err != nil {
				t.Errorf("frame %d: decode inner: %v", i, err)
				continue
			}
			if env.ID != w.id {
				t.Errorf("frame %d: env.ID = %d, want %d", i, env.ID, w.id)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("frame %d: not delivered", i)
		}
	}
}

func TestClose_ShutsDownCleanly(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := newTestConnection(t, ctx, relay.URL())

	// Wait for handshake to land.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := relay.HelloEnv(0); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := relay.HelloEnv(0); !ok {
		t.Fatal("handshake did not complete")
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Frames channel must close.
	select {
	case _, ok := <-c.Frames():
		if ok {
			t.Error("Frames yielded a value after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Frames did not close after Close")
	}
	if err := c.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("Wait after Close: err = %v, want nil or context.Canceled", err)
	}
	// Idempotent.
	if err := c.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestContextCancel_ShutsDownCleanly(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)

	ctx, cancel := context.WithCancel(context.Background())
	c := newTestConnection(t, ctx, relay.URL())
	t.Cleanup(func() { _ = c.Close() })

	// Wait for handshake.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := relay.HelloEnv(0); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := relay.HelloEnv(0); !ok {
		t.Fatal("handshake did not complete")
	}
	cancel()
	waitDone := make(chan error, 1)
	go func() { waitDone <- c.Wait() }()
	select {
	case err := <-waitDone:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Wait returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after ctx cancel")
	}
}

func TestConfig_Validation_TableDriven(t *testing.T) {
	t.Parallel()
	logger := testLogger(t)
	valid := Config{
		ServerID:      identity.ServerID(testServerID),
		RelayURL:      "wss://example.invalid/v1/server",
		BinaryVersion: "0.10.0",
		Logger:        logger,
	}

	cases := []struct {
		name  string
		mut   func(*Config)
		want  string
	}{
		{"missing Logger", func(c *Config) { c.Logger = nil }, "Logger is required"},
		{"missing ServerID", func(c *Config) { c.ServerID = "" }, "ServerID is required"},
		{"missing RelayURL", func(c *Config) { c.RelayURL = "" }, "RelayURL is required"},
		{"missing BinaryVersion", func(c *Config) { c.BinaryVersion = "" }, "BinaryVersion is required"},
		{"ws scheme rejected", func(c *Config) { c.RelayURL = "ws://example.invalid/" }, "wss"},
		{"http scheme rejected", func(c *Config) { c.RelayURL = "http://example.invalid/" }, "wss"},
		{"unparseable URL", func(c *Config) { c.RelayURL = "://broken" }, "RelayURL parse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			tc.mut(&cfg)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, err := Connect(ctx, cfg)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Connect returned err = %v, want wrapping ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error message %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

// shut up unused-import lint if fmt isn't reached.
var _ = fmt.Sprintf
