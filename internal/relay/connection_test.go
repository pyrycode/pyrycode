package relay

// Test budget note: tests that exercise transport reconnect use the
// production-default cadence (1s initial, ±20% jitter) per the
// architect's "option 2" decision in the ticket spec — no test-only
// transport mutators are reachable from this package. Each such test
// budgets ~3-4s for the second connect to land.

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
)

const testServerID identity.ServerID = "11111111-2222-4333-8444-555555555555"

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// allowAnyScheme installs a permissive validateRelayScheme for the
// duration of the test so we can dial httptest.NewServer's ws:// URL.
func allowAnyScheme(t *testing.T) {
	t.Helper()
	prev := validateRelayScheme
	validateRelayScheme = func(string) error { return nil }
	t.Cleanup(func() { validateRelayScheme = prev })
}

// shortenHandshakeTimeout substitutes a tighter handshakeTimeout for
// the duration of the test so timeout-driven tests don't take 5s.
func shortenHandshakeTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := handshakeTimeout
	handshakeTimeout = d
	t.Cleanup(func() { handshakeTimeout = prev })
}

// connScript handles a single accepted conn. The script owns the conn
// completely; when it returns the conn is closed by the relay handler.
type connScript func(t *testing.T, conn *websocket.Conn, headers http.Header)

// scriptedRelay is an httptest WS server that dispatches each accepted
// connection to the next script in `scripts`; once exhausted it falls
// back to `defaultScript`. Used by tests that need to choreograph the
// handshake (happy / timeout / drop / conflict / unexpected-frame).
type scriptedRelay struct {
	server *httptest.Server

	mu      sync.Mutex
	scripts []connScript

	defaultScript connScript

	connCount       atomic.Int64
	headersObserved chan http.Header
}

func (r *scriptedRelay) URL() string {
	return "ws" + strings.TrimPrefix(r.server.URL, "http")
}

func (r *scriptedRelay) Close() { r.server.Close() }

func (r *scriptedRelay) nextScript() connScript {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.scripts) == 0 {
		return r.defaultScript
	}
	next := r.scripts[0]
	r.scripts = r.scripts[1:]
	return next
}

func newScriptedRelay(t *testing.T, scripts []connScript, def connScript) *scriptedRelay {
	t.Helper()
	r := &scriptedRelay{
		scripts:         scripts,
		defaultScript:   def,
		headersObserved: make(chan http.Header, 16),
	}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Capture headers BEFORE upgrade strips them.
		hdrs := req.Header.Clone()
		select {
		case r.headersObserved <- hdrs:
		default:
		}
		conn, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		r.connCount.Add(1)
		script := r.nextScript()
		if script != nil {
			script(t, conn, hdrs)
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(r.Close)
	return r
}

// readHello reads the first frame and parses it as an Envelope. Errors
// are returned (not raised via t.Fatalf) because scripts run on a
// non-test goroutine where t.Fatalf would panic.
func readHello(conn *websocket.Conn) (protocol.Envelope, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		return protocol.Envelope{}, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return protocol.Envelope{}, err
	}
	return env, nil
}

// writeAck writes a hello_ack envelope wrapped in a RoutingEnvelope
// with conn_id="-" (matching docs/protocol-mobile.md § Worked example).
func writeAck(conn *websocket.Conn) error {
	ackPayload, err := json.Marshal(protocol.HelloAckPayload{
		ProtocolVersion: "v1",
		ServerID:        string(testServerID),
		ConnID:          "-",
	})
	if err != nil {
		return err
	}
	innerRaw, err := json.Marshal(protocol.Envelope{
		ID:      2,
		Type:    protocol.TypeHelloAck,
		TS:      time.Now().UTC(),
		Payload: ackPayload,
	})
	if err != nil {
		return err
	}
	routingRaw, err := json.Marshal(protocol.RoutingEnvelope{ConnID: "-", Frame: innerRaw})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, routingRaw)
}

// writeRoutedEnvelope sends an arbitrary envelope wrapped in a
// RoutingEnvelope with the supplied conn_id. Used for post-handshake
// frame-ordering tests.
func writeRoutedEnvelope(conn *websocket.Conn, connID string, env protocol.Envelope) error {
	innerRaw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	routingRaw, err := json.Marshal(protocol.RoutingEnvelope{ConnID: connID, Frame: innerRaw})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, routingRaw)
}

// scriptHappyPath: read hello, send hello_ack, hold the conn until the
// client closes it. Script-goroutine errors are dropped — the main test
// goroutine detects problems via missing-frame timeouts or ConnCount.
func scriptHappyPath(_ *testing.T, conn *websocket.Conn, _ http.Header) {
	if _, err := readHello(conn); err != nil {
		return
	}
	if err := writeAck(conn); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _ = conn.Read(ctx)
}

func mustConfig(t *testing.T, relayURL string) Config {
	t.Helper()
	return Config{
		ServerID:      testServerID,
		RelayURL:      relayURL,
		BinaryVersion: "test-0.0.1",
		Logger:        testLogger(t),
	}
}

// --- tests ---

func TestConfig_Validation(t *testing.T) {
	base := func() Config {
		return Config{
			ServerID:      testServerID,
			RelayURL:      "wss://relay.example.test/ws",
			BinaryVersion: "test-0.0.1",
			Logger:        testLogger(t),
		}
	}
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"missing logger", func(c *Config) { c.Logger = nil }, "Logger is required"},
		{"missing server id", func(c *Config) { c.ServerID = "" }, "ServerID is required"},
		{"missing relay url", func(c *Config) { c.RelayURL = "" }, "RelayURL is required"},
		{"missing binary version", func(c *Config) { c.BinaryVersion = "" }, "BinaryVersion is required"},
		{"ws scheme", func(c *Config) { c.RelayURL = "ws://relay.example.test/ws" }, "wss"},
		{"http scheme", func(c *Config) { c.RelayURL = "http://relay.example.test/ws" }, "wss"},
		{"unparseable", func(c *Config) { c.RelayURL = "://broken" }, "RelayURL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(&cfg)
			_, err := Connect(context.Background(), cfg)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("err = %v, want errors.Is(_, ErrInvalidConfig)", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err message = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestConnect_HappyPath(t *testing.T) {
	allowAnyScheme(t)
	relay := newScriptedRelay(t, nil, scriptHappyPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Connect(ctx, mustConfig(t, relay.URL()))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// First inspected headers must include the three spec-mandated ones.
	select {
	case hdrs := <-relay.headersObserved:
		if got := hdrs.Get("x-pyrycode-server"); got != string(testServerID) {
			t.Errorf("x-pyrycode-server = %q, want %q", got, testServerID)
		}
		if got := hdrs.Get("x-pyrycode-version"); got != "test-0.0.1" {
			t.Errorf("x-pyrycode-version = %q, want %q", got, "test-0.0.1")
		}
		if got := hdrs.Get("user-agent"); got != "pyry/test-0.0.1" {
			t.Errorf("user-agent = %q, want %q", got, "pyry/test-0.0.1")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relay never received connection")
	}
}

func TestConnect_SendsCorrectHelloPayload(t *testing.T) {
	allowAnyScheme(t)
	helloCh := make(chan protocol.Envelope, 1)
	script := func(_ *testing.T, conn *websocket.Conn, _ http.Header) {
		env, err := readHello(conn)
		if err != nil {
			return
		}
		helloCh <- env
		_ = writeAck(conn)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _, _ = conn.Read(ctx)
	}
	relay := newScriptedRelay(t, []connScript{script}, scriptHappyPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Connect(ctx, mustConfig(t, relay.URL()))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	select {
	case env := <-helloCh:
		if env.Type != protocol.TypeHello {
			t.Errorf("envelope Type = %q, want %q", env.Type, protocol.TypeHello)
		}
		var payload protocol.HelloServerPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if payload.Role != "server" {
			t.Errorf("role = %q, want server", payload.Role)
		}
		if payload.ServerID != string(testServerID) {
			t.Errorf("server_id = %q, want %q", payload.ServerID, testServerID)
		}
		if payload.BinaryVersion != "test-0.0.1" {
			t.Errorf("binary_version = %q, want %q", payload.BinaryVersion, "test-0.0.1")
		}
		if len(payload.ProtocolVersions) != 1 || payload.ProtocolVersions[0] != "v1" {
			t.Errorf("protocol_versions = %v, want [v1]", payload.ProtocolVersions)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relay never received hello")
	}
}

func TestHandshake_AckTimeout(t *testing.T) {
	allowAnyScheme(t)
	shortenHandshakeTimeout(t, 200*time.Millisecond)

	// First conn: read hello, then sit silent until the client gives up
	// and closes the conn (which causes our conn.Read below to return).
	noAck := func(_ *testing.T, conn *websocket.Conn, _ http.Header) {
		_, _ = readHello(conn)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, _ = conn.Read(ctx)
	}
	relay := newScriptedRelay(t, []connScript{noAck}, scriptHappyPath)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	c, err := Connect(ctx, mustConfig(t, relay.URL()))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// Second conn (handshake should succeed via the default script).
	// Use ConnCount as the cheap signal that a reconnect occurred.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if relay.connCount.Load() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := relay.connCount.Load(); got < 2 {
		t.Errorf("ConnCount = %d, want ≥ 2 (no reconnect after ack timeout)", got)
	}
}

func TestHandshake_UnexpectedFirstFrame(t *testing.T) {
	allowAnyScheme(t)
	shortenHandshakeTimeout(t, 500*time.Millisecond)

	// First conn: read hello, send an unrelated frame (e.g. "error"
	// envelope) as the first reply — relay package should reject it
	// and recycle.
	unexpected := func(_ *testing.T, conn *websocket.Conn, _ http.Header) {
		if _, err := readHello(conn); err != nil {
			return
		}
		errPayload, _ := json.Marshal(protocol.ErrorPayload{
			Code: protocol.CodeProtocolMalformed, Message: "synthetic",
		})
		_ = writeRoutedEnvelope(conn, "-", protocol.Envelope{
			ID:      2,
			Type:    protocol.TypeError,
			TS:      time.Now().UTC(),
			Payload: errPayload,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, _ = conn.Read(ctx)
	}
	relay := newScriptedRelay(t, []connScript{unexpected}, scriptHappyPath)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	c, err := Connect(ctx, mustConfig(t, relay.URL()))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if relay.connCount.Load() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := relay.connCount.Load(); got < 2 {
		t.Errorf("ConnCount = %d, want ≥ 2 (no reconnect after unexpected first frame)", got)
	}
}

func TestServerIDConflict_FatalNoReconnect(t *testing.T) {
	allowAnyScheme(t)
	// Close every conn with status 4409 immediately.
	conflict := func(_ *testing.T, conn *websocket.Conn, _ http.Header) {
		_ = conn.Close(websocket.StatusCode(4409), "server-id conflict")
	}
	relay := newScriptedRelay(t, nil, conflict)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Connect(ctx, mustConfig(t, relay.URL()))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	waitDone := make(chan error, 1)
	go func() { waitDone <- c.Wait() }()
	select {
	case err := <-waitDone:
		t.Logf("Wait returned: %v", err)
		if !errors.Is(err, ErrServerIDConflict) {
			t.Errorf("Wait err = %v, want errors.Is(_, ErrServerIDConflict)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not return after 4409 close")
	}
	// Give the loop a moment in case the dial-loop wants to spin.
	time.Sleep(200 * time.Millisecond)
	if got := relay.connCount.Load(); got != 1 {
		t.Errorf("ConnCount = %d, want 1 (no reconnect after fatal close)", got)
	}
}

func TestTransportDropDuringHandshake(t *testing.T) {
	allowAnyScheme(t)

	// First conn: read hello, then transport-level drop (CloseNow).
	drop := func(_ *testing.T, conn *websocket.Conn, _ http.Header) {
		_, _ = readHello(conn)
		_ = conn.CloseNow()
	}
	relay := newScriptedRelay(t, []connScript{drop}, scriptHappyPath)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	c, err := Connect(ctx, mustConfig(t, relay.URL()))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// Reconnect uses production cadence; budget 4s for the second conn.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if relay.connCount.Load() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := relay.connCount.Load(); got < 2 {
		t.Errorf("ConnCount = %d, want ≥ 2 (no reconnect after drop during handshake)", got)
	}
}

func TestFrames_DeliversPostHandshakeInOrder(t *testing.T) {
	allowAnyScheme(t)

	script := func(_ *testing.T, conn *websocket.Conn, _ http.Header) {
		if _, err := readHello(conn); err != nil {
			return
		}
		if err := writeAck(conn); err != nil {
			return
		}
		for i, connID := range []string{"c1", "c2", "c3"} {
			_ = writeRoutedEnvelope(conn, connID, protocol.Envelope{
				ID:      uint64(10 + i),
				Type:    protocol.TypeMessage,
				TS:      time.Now().UTC(),
				Payload: json.RawMessage(`{}`),
			})
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _, _ = conn.Read(ctx)
	}
	relay := newScriptedRelay(t, []connScript{script}, scriptHappyPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Connect(ctx, mustConfig(t, relay.URL()))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	wantIDs := []string{"c1", "c2", "c3"}
	for _, wantID := range wantIDs {
		select {
		case got := <-c.Frames():
			if got.ConnID != wantID {
				t.Errorf("ConnID = %q, want %q", got.ConnID, wantID)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for frame %q", wantID)
		}
	}
}

func TestTransportDropPostHandshake_ReHandshakes(t *testing.T) {
	allowAnyScheme(t)

	// First conn: complete handshake, then transport-level drop.
	// Second conn: complete handshake, then deliver a frame.
	first := func(_ *testing.T, conn *websocket.Conn, _ http.Header) {
		if _, err := readHello(conn); err != nil {
			return
		}
		_ = writeAck(conn)
		time.Sleep(100 * time.Millisecond)
		_ = conn.CloseNow()
	}
	second := func(_ *testing.T, conn *websocket.Conn, _ http.Header) {
		if _, err := readHello(conn); err != nil {
			return
		}
		if err := writeAck(conn); err != nil {
			return
		}
		_ = writeRoutedEnvelope(conn, "after-reconnect", protocol.Envelope{
			ID:      99,
			Type:    protocol.TypeMessage,
			TS:      time.Now().UTC(),
			Payload: json.RawMessage(`{}`),
		})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _, _ = conn.Read(ctx)
	}
	relay := newScriptedRelay(t, []connScript{first, second}, scriptHappyPath)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Connect(ctx, mustConfig(t, relay.URL()))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	select {
	case got := <-c.Frames():
		if got.ConnID != "after-reconnect" {
			t.Errorf("ConnID = %q, want %q", got.ConnID, "after-reconnect")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("never received post-reconnect frame")
	}
}

func TestClose_ShutsDownCleanly(t *testing.T) {
	allowAnyScheme(t)
	relay := newScriptedRelay(t, nil, scriptHappyPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Connect(ctx, mustConfig(t, relay.URL()))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Wait for at least one conn so we know the lifecycle is engaged.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if relay.connCount.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Idempotent.
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- c.Wait() }()
	select {
	case <-waitDone:
		// Result may be nil (Close path) or transport.ErrClosed
		// (transport returned before we did) — both are clean shutdown.
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after Close")
	}
	// Frames channel must close.
	select {
	case _, ok := <-c.Frames():
		if ok {
			t.Error("Frames yielded a value after Close")
		}
	case <-time.After(1 * time.Second):
		t.Error("Frames channel did not close after Close")
	}
}

func TestContextCancel_ShutsDownCleanly(t *testing.T) {
	allowAnyScheme(t)
	relay := newScriptedRelay(t, nil, scriptHappyPath)
	ctx, cancel := context.WithCancel(context.Background())
	c, err := Connect(ctx, mustConfig(t, relay.URL()))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if relay.connCount.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	waitDone := make(chan error, 1)
	go func() { waitDone <- c.Wait() }()
	select {
	case err := <-waitDone:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Wait err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after ctx cancel")
	}
}
