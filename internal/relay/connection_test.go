package relay

import (
	"context"
	"encoding/json"
	"errors"
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
// (1s initial, ±20% jitter).

const testServerID = "11111111-1111-4111-8111-111111111111"

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// waitConnCount blocks until the relay has accepted at least n conns or
// the timeout elapses (fatal). It replaces the old hello-envelope poll as
// the readiness signal — under v2 the binary sends no hello, so the conn
// itself is the only observable establishment event.
func waitConnCount(t *testing.T, r *testRelay, n int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if r.ConnCount() >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("ConnCount = %d, want >= %d within %s", r.ConnCount(), n, timeout)
}

// relayBehavior controls how the content-blind test relay handles each
// accepted conn.
type relayBehavior int

const (
	behaviorForward              relayBehavior = iota // register on upgrade, forward frames (no hello, no ack)
	behaviorCloseImmediately4409                      // close with 4409 on accept
	behaviorDropOnConnect                             // CloseNow immediately after upgrade (1006-equivalent)
)

// testRelay is a controllable httptest-hosted relay for connection tests.
type testRelay struct {
	server *httptest.Server

	mu      sync.Mutex
	conns   []*websocket.Conn
	headers []http.Header

	connectedCh chan struct{}
	connCount   atomic.Int64

	// behavior, nextBehavior, and switchAfter are set by tests before
	// Connect is called. The HTTP handler goroutines that read them
	// observe those writes via the happens-before edge from the test
	// goroutine's `go transport.Connect` launch. No mutex needed.
	behavior     relayBehavior
	nextBehavior relayBehavior
	switchAfter  int

	// outboundFrames is pushed to by tests; the relay sends each frame
	// (already JSON-encoded) on the live conn once it is forwarding.
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
		behavior:       behaviorForward,
	}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Capture headers before WS upgrade. The relay registers the
		// server-id from these headers and claims the slot on upgrade —
		// no hello is read, no hello_ack is sent.
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

		switch behavior {
		case behaviorCloseImmediately4409:
			_ = conn.Close(statusServerIDConflict, "server-id conflict")
			return
		case behaviorDropOnConnect:
			_ = conn.CloseNow()
			return
		case behaviorForward:
			// Content-blind forward: the conn is established on upgrade.
			// Forward any frame pushed via outboundFrames to the live
			// conn. A reader goroutine drains inbound bytes so a conn
			// drop unblocks the outbound loop — without it, a handler
			// whose conn is dead would keep consuming from
			// r.outboundFrames and starve the next handler (the channel
			// is shared across handlers).
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

// TestConnect_ReachesForwardingNoAck is the AC #2 regression guard: against
// a relay that registers the server-id from the header and never sends a
// hello_ack, the binary treats the WS upgrade as established and goes
// straight to frame-forwarding — no hello sent, no hello_ack awaited, no
// conn recycle. Proven by (a) a frame pushed by the relay arriving on
// Frames() and (b) the conn count staying at 1 (the old hello_ack-timeout
// path would have dropped this conn and produced a second).
func TestConnect_ReachesForwardingNoAck(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t) // default behaviorForward: never sends a hello_ack
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newTestConnection(t, ctx, relay.URL())
	t.Cleanup(func() { _ = c.Close() })

	waitConnCount(t, relay, 1, 3*time.Second)

	env := protocol.Envelope{
		ID:      2,
		Type:    protocol.TypeMessage,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	}
	innerRaw, _ := json.Marshal(env)
	wrapped, _ := json.Marshal(protocol.RoutingEnvelope{
		ConnID: "c-no-ack",
		Frame:  innerRaw,
	})
	select {
	case relay.outboundFrames <- wrapped:
	case <-time.After(time.Second):
		t.Fatal("could not push outbound frame to relay")
	}
	select {
	case got := <-c.Frames():
		if got.ConnID != "c-no-ack" {
			t.Errorf("ConnID = %q, want %q", got.ConnID, "c-no-ack")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Frames did not deliver; binary did not reach forwarding without an ack")
	}

	// The conn must not have been recycled. The frame already proved
	// forwarding is live on conn #1; a short beat confirms no second conn
	// appears (transport reconnect would take ~1s).
	time.Sleep(300 * time.Millisecond)
	if got := relay.ConnCount(); got != 1 {
		t.Errorf("ConnCount = %d, want 1 (conn recycled despite no ack)", got)
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

// TestTransportDropOnConnect_Reconnects covers the non-fatal early-drop
// path: the first conn is dropped immediately after upgrade (1006-style),
// the transport reconnects, and the second conn forwards. Distinct from
// the fatal 4409 path, which does not reconnect.
func TestTransportDropOnConnect_Reconnects(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)
	relay.behavior = behaviorDropOnConnect
	relay.switchAfter = 1
	relay.nextBehavior = behaviorForward

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := newTestConnection(t, ctx, relay.URL())
	t.Cleanup(func() { _ = c.Close() })

	waitConnCount(t, relay, 2, 4*time.Second)
}

// TestTransportDropPostConnect_Reconnects drops a fully-established conn
// and confirms the transport reconnects and the rebuilt Frames pipeline
// delivers on the new conn.
func TestTransportDropPostConnect_Reconnects(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	c := newTestConnection(t, ctx, relay.URL())
	t.Cleanup(func() { _ = c.Close() })

	// Wait for the first conn to establish, then drop it.
	waitConnCount(t, relay, 1, 3*time.Second)
	relay.ForceCloseAll()

	// After the drop, transport reconnects → second conn.
	waitConnCount(t, relay, 2, 4*time.Second)
	// Let the second conn's handler enter the forward loop (and the
	// force-closed first handler drain off the shared outbound channel)
	// before pushing.
	time.Sleep(100 * time.Millisecond)

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

func TestFrames_AfterConnect_InOrder(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := newTestConnection(t, ctx, relay.URL())
	t.Cleanup(func() { _ = c.Close() })

	waitConnCount(t, relay, 1, 3*time.Second)
	// Give the relay's per-conn goroutine time to enter the forward loop.
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

	waitConnCount(t, relay, 1, 3*time.Second)

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

	waitConnCount(t, relay, 1, 3*time.Second)
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
		name string
		mut  func(*Config)
		want string
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

// TestCloseConn_WireShape pins the marshalled bytes CloseConn emits:
// conn_id + close_code present, token omitted. (Frame appears as
// `"frame":null` per the json.RawMessage zero-value; the consumer-side
// `len(env.Frame) > 0` check is what gates forwarding, so this is
// option (a) from the spec — accept the null on the wire.)
func TestCloseConn_WireShape(t *testing.T) {
	t.Parallel()
	envBytes, err := json.Marshal(protocol.RoutingEnvelope{ConnID: "c-test-7", CloseCode: 4401})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(envBytes)
	if !strings.Contains(s, `"conn_id":"c-test-7"`) {
		t.Errorf("missing conn_id: %s", s)
	}
	if !strings.Contains(s, `"close_code":4401`) {
		t.Errorf("missing close_code: %s", s)
	}
	if strings.Contains(s, `"token"`) {
		t.Errorf("token must be omitted on close-only envelope: %s", s)
	}
	var back protocol.RoutingEnvelope
	if err := json.Unmarshal(envBytes, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ConnID != "c-test-7" || back.CloseCode != 4401 {
		t.Errorf("round-trip: got %+v", back)
	}
}

// TestCloseConn_PropagatesNotConnected pins that CloseConn surfaces
// transport.ErrNotConnected when the underlying conn was never
// established (same contract as transport.Client.Send).
func TestCloseConn_PropagatesNotConnected(t *testing.T) {
	t.Parallel()
	cfg := Config{
		ServerID:      identity.ServerID(testServerID),
		RelayURL:      "ws://example.invalid/v1/server",
		BinaryVersion: "0.10.0-test",
		Logger:        testLogger(t),
	}
	tc := transport.New(transport.Config{
		URL:          "ws://example.invalid/v1/server",
		WriteTimeout: time.Second,
		Logger:       testLogger(t),
	})
	c := &Connection{
		cfg:    cfg,
		client: tc,
		frames: make(chan protocol.RoutingEnvelope),
		closed: make(chan struct{}),
		done:   make(chan struct{}),
	}
	err := c.CloseConn("c-1", 4401)
	if !errors.Is(err, transport.ErrNotConnected) {
		t.Errorf("CloseConn before connect: got %v, want ErrNotConnected", err)
	}
	close(c.done)
}

// TestConfig_AllowInsecureScheme covers the test-only seam that lets
// ws:// pass Connect's scheme check. We don't run the lifecycle here —
// just enough of Connect to prove the validator branch. A dial against
// the bogus URL would fail asynchronously inside the lifecycle goroutine;
// Close cancels the transport before that surfaces.
func TestConfig_AllowInsecureScheme(t *testing.T) {
	t.Parallel()
	cfg := Config{
		ServerID:            identity.ServerID(testServerID),
		RelayURL:            "ws://example.invalid/v1/server",
		BinaryVersion:       "0.10.0",
		Logger:              testLogger(t),
		AllowInsecureScheme: true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("Connect with AllowInsecureScheme=true rejected ws://: %v", err)
	}
	_ = c.Close()
}

