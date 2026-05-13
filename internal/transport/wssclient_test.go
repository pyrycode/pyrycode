package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// testLogger returns a slog.Logger that discards output unless the test
// is failing (-v shows it implicitly via t.Log).
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newClientForTest builds a Client with shorter cadence constants and a
// deterministic rng so cadence-sensitive assertions run in tens of
// milliseconds, not tens of seconds.
type testOpts struct {
	pingInterval     time.Duration
	pongTimeout      time.Duration
	reconnectInitial time.Duration
	reconnectMax     time.Duration
	stabilityReset   time.Duration
	closeFrameGrace  time.Duration
	seed             int64
	dialFn           func(ctx context.Context) (*websocket.Conn, error)
}

func newClientForTest(t *testing.T, cfg Config, opts testOpts) *Client {
	t.Helper()
	c := New(cfg)
	if opts.pingInterval > 0 {
		c.pingInterval = opts.pingInterval
	}
	if opts.pongTimeout > 0 {
		c.pongTimeout = opts.pongTimeout
	}
	if opts.reconnectInitial > 0 {
		c.reconnectInitial = opts.reconnectInitial
	}
	if opts.reconnectMax > 0 {
		c.reconnectMax = opts.reconnectMax
	}
	if opts.stabilityReset > 0 {
		c.stabilityReset = opts.stabilityReset
	}
	if opts.closeFrameGrace > 0 {
		c.closeFrameGrace = opts.closeFrameGrace
	}
	c.rng = rand.New(rand.NewSource(opts.seed))
	if opts.dialFn != nil {
		c.dialFn = opts.dialFn
	}
	return c
}

// relayCtrl is the test-side handle to a running httptest WS relay. It
// records per-conn ping counts and supports forcing the active conn to
// drop or holding pings without replying.
type relayCtrl struct {
	mu             sync.Mutex
	server         *httptest.Server
	conns          []*websocket.Conn
	pingCount      atomic.Int64
	suppressPongs  atomic.Bool
	echoEnabled    atomic.Bool
	connCount      atomic.Int64
	connectedCh    chan struct{}
	disconnectedCh chan struct{}
}

func (r *relayCtrl) URL() string {
	return "ws" + strings.TrimPrefix(r.server.URL, "http")
}

func (r *relayCtrl) ForceClose() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.conns {
		_ = c.CloseNow()
	}
	r.conns = nil
}

func (r *relayCtrl) PingCount() int64 { return r.pingCount.Load() }
func (r *relayCtrl) ConnCount() int64 { return r.connCount.Load() }

func (r *relayCtrl) Close() {
	r.ForceClose()
	r.server.Close()
}

// newTestRelay stands up an httptest server that accepts WS upgrades.
// The handler counts incoming pings, optionally suppresses pong
// responses, and optionally echoes received frames back.
func newTestRelay(t *testing.T) *relayCtrl {
	t.Helper()
	r := &relayCtrl{
		connectedCh:    make(chan struct{}, 16),
		disconnectedCh: make(chan struct{}, 16),
	}
	r.echoEnabled.Store(true)
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		opts := &websocket.AcceptOptions{
			OnPingReceived: func(ctx context.Context, payload []byte) bool {
				r.pingCount.Add(1)
				return !r.suppressPongs.Load()
			},
		}
		conn, err := websocket.Accept(w, req, opts)
		if err != nil {
			return
		}
		r.mu.Lock()
		r.conns = append(r.conns, conn)
		r.mu.Unlock()
		r.connCount.Add(1)
		select {
		case r.connectedCh <- struct{}{}:
		default:
		}
		defer func() {
			_ = conn.Close(websocket.StatusNormalClosure, "")
			select {
			case r.disconnectedCh <- struct{}{}:
			default:
			}
		}()
		// Echo loop. Exits on read error (client close, force close, ctx).
		ctx := req.Context()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if r.echoEnabled.Load() {
				if err := conn.Write(ctx, typ, data); err != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(r.Close)
	return r
}

// --- tests ---

func TestBackoff_Sequence(t *testing.T) {
	t.Parallel()
	c := newClientForTest(t, Config{Logger: testLogger(t), WriteTimeout: time.Second},
		testOpts{seed: 1})

	cases := []struct {
		attempt int
		base    time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 30 * time.Second},
		{7, 30 * time.Second},
		{8, 30 * time.Second},
		{10, 30 * time.Second},
	}
	for _, tc := range cases {
		got := c.backoff(tc.attempt)
		lower := time.Duration(float64(tc.base) * 0.8)
		upper := time.Duration(float64(tc.base) * 1.2)
		if got < lower || got > upper {
			t.Errorf("backoff(%d) = %v, want in [%v, %v]", tc.attempt, got, lower, upper)
		}
	}
}

func TestBackoff_ResetAfterStableConnection(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)

	// Track dial attempts and their inter-arrival.
	var (
		dialMu     sync.Mutex
		dialTimes  []time.Time
		dialErrors = []bool{true, true, false} // attempts 1,2 fail; 3 succeeds
	)
	cfg := Config{Logger: testLogger(t), WriteTimeout: time.Second}

	// Custom dialFn: first two attempts return error, third dials real.
	var c *Client
	c = newClientForTest(t, cfg, testOpts{
		seed:             1,
		reconnectInitial: 20 * time.Millisecond,
		reconnectMax:     200 * time.Millisecond,
		stabilityReset:   80 * time.Millisecond,
		pingInterval:     500 * time.Millisecond,
		pongTimeout:      500 * time.Millisecond,
		dialFn: func(ctx context.Context) (*websocket.Conn, error) {
			dialMu.Lock()
			idx := len(dialTimes)
			dialTimes = append(dialTimes, time.Now())
			fail := idx < len(dialErrors) && dialErrors[idx]
			dialMu.Unlock()
			if fail {
				return nil, errors.New("synthetic dial failure")
			}
			conn, _, err := websocket.Dial(ctx, relay.URL(), nil)
			if err != nil {
				return nil, err
			}
			conn.SetReadLimit(maxFrameBytes)
			return conn, nil
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connectErr := make(chan error, 1)
	go func() { connectErr <- c.Connect(ctx) }()
	t.Cleanup(func() {
		_ = c.Close()
		<-connectErr
	})

	// Wait for the third dial (success) to register on the relay side.
	select {
	case <-relay.connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("relay never observed a successful dial")
	}
	// Hold connection well beyond stabilityReset (80ms) so attempt count
	// resets on next disconnect.
	time.Sleep(150 * time.Millisecond)
	relay.ForceClose()

	// Wait for the 4th dial attempt to occur.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dialMu.Lock()
		n := len(dialTimes)
		dialMu.Unlock()
		if n >= 4 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	dialMu.Lock()
	defer dialMu.Unlock()
	if len(dialTimes) < 4 {
		t.Fatalf("expected ≥4 dial attempts after reset, got %d", len(dialTimes))
	}
	// Between dial[2] (the success at idx 2) and dial[3] (next attempt),
	// the gap = uptime + backoff(1). uptime ≥ 150ms, backoff(1) ∈
	// [16ms, 24ms]. Since uptime is the dominant component we can't
	// pin backoff(1) by the gap. Instead, we verify the reset by
	// asserting the delay between conn-drop (force close) and the 4th
	// dial is in the attempt-1 bound. ForceClose ran approximately at
	// the same time we noted "start of 150ms sleep + 150ms"; rather
	// than rely on wall-clock subtraction we assert relative to the
	// known-OK reset: dialTimes[3] - dialTimes[2] minus 150ms (uptime
	// floor) should be roughly attempt-1 backoff. We loosen the bound
	// to ≤ reconnectInitial*1.2 + a 50ms slack to account for the
	// ForceClose latency.
	gap := dialTimes[3].Sub(dialTimes[2])
	maxExpected := 150*time.Millisecond + time.Duration(float64(c.reconnectInitial)*1.2) + 50*time.Millisecond
	if gap > maxExpected {
		t.Errorf("4th dial gap = %v, expected ≤ %v (attempt counter did not reset)", gap, maxExpected)
	}
}

func TestPing_FiredAt30s(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("ping cadence test (uses 50ms test interval)")
	}
	relay := newTestRelay(t)
	cfg := Config{
		URL:          relay.URL(),
		Logger:       testLogger(t),
		WriteTimeout: time.Second,
	}
	c := newClientForTest(t, cfg, testOpts{
		seed:             1,
		pingInterval:     50 * time.Millisecond,
		pongTimeout:      500 * time.Millisecond,
		reconnectInitial: 10 * time.Millisecond,
		reconnectMax:     50 * time.Millisecond,
		stabilityReset:   10 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = c.Close()
	})
	connectErr := make(chan error, 1)
	go func() { connectErr <- c.Connect(ctx) }()

	// Wait for connection to establish.
	select {
	case <-relay.connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("connection never established")
	}
	// Wait several ping intervals.
	time.Sleep(200 * time.Millisecond)
	if got := relay.PingCount(); got < 2 {
		t.Errorf("PingCount = %d, want ≥ 2 after 200ms with 50ms interval", got)
	}
}

func TestPongTimeout_TriggersReconnect(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)
	relay.suppressPongs.Store(true)
	cfg := Config{
		URL:          relay.URL(),
		Logger:       testLogger(t),
		WriteTimeout: time.Second,
	}
	c := newClientForTest(t, cfg, testOpts{
		seed:             1,
		pingInterval:     30 * time.Millisecond,
		pongTimeout:      80 * time.Millisecond,
		reconnectInitial: 10 * time.Millisecond,
		reconnectMax:     50 * time.Millisecond,
		stabilityReset:   1 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = c.Close()
	})
	connectErr := make(chan error, 1)
	go func() { connectErr <- c.Connect(ctx) }()

	// First conn should establish.
	select {
	case <-relay.connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("first connection never established")
	}
	// After pong timeout, a second dial should occur.
	select {
	case <-relay.connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("second connection did not occur after pong timeout; ConnCount=%d", relay.ConnCount())
	}
	if got := relay.ConnCount(); got < 2 {
		t.Errorf("ConnCount = %d, want ≥ 2 (dead conn detection + reconnect)", got)
	}
}

func TestClose_OnContextCancel(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)
	cfg := Config{
		URL:          relay.URL(),
		Logger:       testLogger(t),
		WriteTimeout: time.Second,
	}
	c := newClientForTest(t, cfg, testOpts{
		seed:             1,
		pingInterval:     500 * time.Millisecond,
		pongTimeout:      500 * time.Millisecond,
		reconnectInitial: 10 * time.Millisecond,
		reconnectMax:     50 * time.Millisecond,
		stabilityReset:   1 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	connectErr := make(chan error, 1)
	go func() { connectErr <- c.Connect(ctx) }()

	select {
	case <-relay.connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("connection never established")
	}
	cancel()
	select {
	case err := <-connectErr:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Connect returned %v, want context.Canceled", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Connect did not return promptly after ctx cancel")
	}
}

func TestClose_Idempotent(t *testing.T) {
	t.Parallel()
	c := New(Config{Logger: testLogger(t), WriteTimeout: time.Second})
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestSmoke_HttptestEchoServer(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)
	cfg := Config{
		URL:          relay.URL(),
		Logger:       testLogger(t),
		WriteTimeout: time.Second,
	}
	c := newClientForTest(t, cfg, testOpts{
		seed:             1,
		pingInterval:     50 * time.Millisecond,
		pongTimeout:      500 * time.Millisecond,
		reconnectInitial: 10 * time.Millisecond,
		reconnectMax:     50 * time.Millisecond,
		stabilityReset:   1 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	connectErr := make(chan error, 1)
	go func() { connectErr <- c.Connect(ctx) }()

	select {
	case <-relay.connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("connection never established")
	}
	// Send a frame, expect echo back. Poll because setConn happens
	// shortly after the relay's Accept; until then Send returns
	// ErrNotConnected.
	sendDeadline := time.Now().Add(500 * time.Millisecond)
	var sendErr error
	for time.Now().Before(sendDeadline) {
		sendErr = c.Send([]byte("hello"))
		if sendErr == nil {
			break
		}
		if !errors.Is(sendErr, ErrNotConnected) {
			t.Fatalf("Send: %v", sendErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if sendErr != nil {
		t.Fatalf("Send did not become live: %v", sendErr)
	}
	recvCtx, recvCancel := context.WithTimeout(ctx, 2*time.Second)
	defer recvCancel()
	got, err := c.Receive(recvCtx)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("Receive = %q, want %q", got, "hello")
	}
	// Wait for at least one ping round-trip to land.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if relay.PingCount() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := relay.PingCount(); got < 1 {
		t.Errorf("PingCount = %d, want ≥ 1", got)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-connectErr:
		if !errors.Is(err, ErrClosed) && !errors.Is(err, context.Canceled) {
			t.Errorf("Connect returned %v, want ErrClosed or context.Canceled", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Connect did not return after Close")
	}
}

func TestSend_ReturnsErrNotConnected_BeforeConnect(t *testing.T) {
	t.Parallel()
	c := New(Config{Logger: testLogger(t), WriteTimeout: time.Second})
	t.Cleanup(func() { _ = c.Close() })
	err := c.Send([]byte("x"))
	if !errors.Is(err, ErrNotConnected) {
		t.Errorf("Send before Connect: err = %v, want ErrNotConnected", err)
	}
}

func TestSend_ReturnsErrClosed_AfterClose(t *testing.T) {
	t.Parallel()
	c := New(Config{Logger: testLogger(t), WriteTimeout: time.Second})
	_ = c.Close()
	if err := c.Send([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Errorf("Send after Close: err = %v, want ErrClosed", err)
	}
}

func TestReceive_ReturnsErrClosed_AfterClose(t *testing.T) {
	t.Parallel()
	c := New(Config{Logger: testLogger(t), WriteTimeout: time.Second})
	_ = c.Close()
	_, err := c.Receive(context.Background())
	if !errors.Is(err, ErrClosed) {
		t.Errorf("Receive after Close: err = %v, want ErrClosed", err)
	}
}

func TestNew_PanicsWithoutLogger(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("New(Config{}) did not panic on nil Logger")
		}
	}()
	_ = New(Config{})
}

func TestConnected_FiresOnEveryConnect(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)
	cfg := Config{
		URL:          relay.URL(),
		Logger:       testLogger(t),
		WriteTimeout: time.Second,
	}
	c := newClientForTest(t, cfg, testOpts{
		seed:             1,
		pingInterval:     500 * time.Millisecond,
		pongTimeout:      500 * time.Millisecond,
		reconnectInitial: 10 * time.Millisecond,
		reconnectMax:     50 * time.Millisecond,
		stabilityReset:   1 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = c.Close()
	})
	connectErr := make(chan error, 1)
	go func() { connectErr <- c.Connect(ctx) }()

	select {
	case <-c.Connected():
	case <-time.After(2 * time.Second):
		t.Fatal("Connected did not fire after first connect")
	}
	relay.ForceClose()
	select {
	case <-c.Connected():
	case <-time.After(2 * time.Second):
		t.Fatal("Connected did not fire after reconnect")
	}
}

func TestConnected_DropsWhenObserverSlow(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)
	cfg := Config{
		URL:          relay.URL(),
		Logger:       testLogger(t),
		WriteTimeout: time.Second,
	}
	c := newClientForTest(t, cfg, testOpts{
		seed:             1,
		pingInterval:     500 * time.Millisecond,
		pongTimeout:      500 * time.Millisecond,
		reconnectInitial: 10 * time.Millisecond,
		reconnectMax:     50 * time.Millisecond,
		stabilityReset:   1 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = c.Close()
	})
	connectErr := make(chan error, 1)
	go func() { connectErr <- c.Connect(ctx) }()

	// Let several reconnects happen without reading Connected.
	for i := 0; i < 3; i++ {
		select {
		case <-relay.connectedCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("relay never observed connect %d", i)
		}
		relay.ForceClose()
	}
	// Late observer must still get at least one event without blocking
	// the dial loop.
	select {
	case <-c.Connected():
	case <-time.After(2 * time.Second):
		t.Fatal("Connected delivered no event to late observer")
	}
}

// closeCodeRelay closes every accepted WS upgrade with the configured status.
type closeCodeRelay struct {
	server    *httptest.Server
	connCount atomic.Int64
}

func newCloseCodeRelay(t *testing.T, status websocket.StatusCode, reason string) *closeCodeRelay {
	t.Helper()
	r := &closeCodeRelay{}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		r.connCount.Add(1)
		_ = conn.Close(status, reason)
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *closeCodeRelay) URL() string {
	return "ws" + strings.TrimPrefix(r.server.URL, "http")
}

func TestFatalCloseCodes_HaltsReconnect(t *testing.T) {
	t.Parallel()
	relay := newCloseCodeRelay(t, websocket.StatusCode(4409), "server-id conflict")
	cfg := Config{
		URL:             relay.URL(),
		Logger:          testLogger(t),
		WriteTimeout:    time.Second,
		FatalCloseCodes: []websocket.StatusCode{websocket.StatusCode(4409)},
	}
	c := newClientForTest(t, cfg, testOpts{
		seed:             1,
		pingInterval:     500 * time.Millisecond,
		pongTimeout:      500 * time.Millisecond,
		reconnectInitial: 10 * time.Millisecond,
		reconnectMax:     50 * time.Millisecond,
		stabilityReset:   1 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = c.Close() })

	err := c.Connect(ctx)
	if !errors.Is(err, ErrFatalClose) {
		t.Fatalf("Connect returned %v, want wrapping ErrFatalClose", err)
	}
	if status := websocket.CloseStatus(err); status != websocket.StatusCode(4409) {
		t.Errorf("CloseStatus(err) = %d, want 4409", status)
	}
	if got := relay.connCount.Load(); got != 1 {
		t.Errorf("connCount = %d, want 1 (no reconnect)", got)
	}
}

// TestFatalCloseCodes_HaltsOnDialError exercises the path where the relay
// closes mid-upgrade, so the close status surfaces from Dial directly
// (not from a subsequent serve Read). Without the dial-path check, the
// client would back off and retry, defeating ErrServerIDConflict.
// Deterministic via dialFn injection — no race against upgrade timing.
func TestFatalCloseCodes_HaltsOnDialError(t *testing.T) {
	t.Parallel()
	cfg := Config{
		URL:             "wss://example.invalid",
		Logger:          testLogger(t),
		WriteTimeout:    time.Second,
		FatalCloseCodes: []websocket.StatusCode{websocket.StatusCode(4409)},
	}
	var dials atomic.Int64
	c := newClientForTest(t, cfg, testOpts{
		seed:             1,
		reconnectInitial: 10 * time.Millisecond,
		reconnectMax:     50 * time.Millisecond,
		stabilityReset:   1 * time.Second,
		dialFn: func(ctx context.Context) (*websocket.Conn, error) {
			dials.Add(1)
			ce := websocket.CloseError{
				Code:   websocket.StatusCode(4409),
				Reason: "server-id conflict",
			}
			return nil, fmt.Errorf("dial: %w", ce)
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = c.Close() })

	err := c.Connect(ctx)
	if !errors.Is(err, ErrFatalClose) {
		t.Fatalf("Connect returned %v, want wrapping ErrFatalClose", err)
	}
	if status := websocket.CloseStatus(err); status != websocket.StatusCode(4409) {
		t.Errorf("CloseStatus(err) = %d, want 4409", status)
	}
	if got := dials.Load(); got != 1 {
		t.Errorf("dialFn invocations = %d, want 1 (no retry on fatal close)", got)
	}
}

// racingCloseRelay accepts a single WS upgrade, echoes incoming frames to
// keep sendPump writes draining (so they remain in-flight and likely to
// fail mid-write at close time), then closes the conn with the configured
// status on demand. Subsequent dial attempts are rejected at the HTTP
// layer so a reconnect attempt is observable via connCount > 1.
type racingCloseRelay struct {
	server       *httptest.Server
	connCount    atomic.Int64
	triggerClose chan struct{}
	connectedCh  chan struct{}
}

func newRacingCloseRelay(t *testing.T, status websocket.StatusCode, reason string) *racingCloseRelay {
	t.Helper()
	r := &racingCloseRelay{
		triggerClose: make(chan struct{}, 1),
		connectedCh:  make(chan struct{}, 1),
	}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Reject any dial after the first so a reconnect attempt would
		// show up as a failed dial rather than a fresh accepted conn —
		// but connCount only increments on a successful Accept, so the
		// test's `connCount == 1` assertion catches the reconnect.
		if !r.connCount.CompareAndSwap(0, 1) {
			http.Error(w, "single-shot", http.StatusGone)
			return
		}
		conn, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		select {
		case r.connectedCh <- struct{}{}:
		default:
		}
		ctx := req.Context()
		// Drain client writes until told to close. Without this, sendPump
		// frames pile up in the OS TCP buffer and Writes don't actually
		// block, shrinking the window for an in-flight Write at close.
		echoDone := make(chan struct{})
		go func() {
			defer close(echoDone)
			for {
				typ, data, err := conn.Read(ctx)
				if err != nil {
					return
				}
				if err := conn.Write(ctx, typ, data); err != nil {
					return
				}
			}
		}()
		select {
		case <-r.triggerClose:
		case <-ctx.Done():
		}
		_ = conn.Close(status, reason)
		<-echoDone
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *racingCloseRelay) URL() string {
	return "ws" + strings.TrimPrefix(r.server.URL, "http")
}

func (r *racingCloseRelay) TriggerClose() {
	select {
	case r.triggerClose <- struct{}{}:
	default:
	}
}

// TestFatalCloseCodes_HaltsReconnect_RacingSendError pins the close-status
// preference in serve(): when recvPump and sendPump both error from the
// same peer-close event, the CloseError must be selected regardless of
// which arrived in errCh first, so the FatalCloseCodes check in Connect
// catches the peer's status. Without the preference loop, a sendPump
// "use of closed network connection" error wins half the races and the
// fatal-close halt silently falls through to reconnect.
//
// Determinism: under the fix, the test outcome is invariant across
// scheduler orderings (close-status preference covers all 3 slots). The
// busy-Send priming maximizes the rate of in-flight Writes at close time
// so that a regression (reverting to `return errs[0]`) surfaces on a
// meaningful fraction of -count=10 iterations.
func TestFatalCloseCodes_HaltsReconnect_RacingSendError(t *testing.T) {
	t.Parallel()
	relay := newRacingCloseRelay(t, websocket.StatusCode(4409), "server-id conflict")
	cfg := Config{
		URL:             relay.URL(),
		Logger:          testLogger(t),
		WriteTimeout:    50 * time.Millisecond,
		FatalCloseCodes: []websocket.StatusCode{websocket.StatusCode(4409)},
	}
	c := newClientForTest(t, cfg, testOpts{
		seed:             1,
		pingInterval:     500 * time.Millisecond,
		pongTimeout:      500 * time.Millisecond,
		reconnectInitial: 10 * time.Millisecond,
		reconnectMax:     50 * time.Millisecond,
		stabilityReset:   1 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = c.Close() })

	connectErr := make(chan error, 1)
	go func() { connectErr <- c.Connect(ctx) }()

	select {
	case <-c.Connected():
	case <-ctx.Done():
		t.Fatal("Connected did not fire before ctx deadline")
	}
	select {
	case <-relay.connectedCh:
	case <-ctx.Done():
		t.Fatal("relay did not observe accept before ctx deadline")
	}

	// Busy-Send loop: keep sendPump's Write in flight so the close-frame
	// race lands on a mid-write failure for sendPump, exercising the slot
	// of the preference loop that the existing happy-path test doesn't.
	stopSend := make(chan struct{})
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		for {
			select {
			case <-stopSend:
				return
			default:
			}
			if err := c.Send([]byte("x")); err != nil {
				return
			}
		}
	}()

	relay.TriggerClose()

	select {
	case err := <-connectErr:
		if !errors.Is(err, ErrFatalClose) {
			t.Fatalf("Connect returned %v, want wrapping ErrFatalClose", err)
		}
		if status := websocket.CloseStatus(err); status != websocket.StatusCode(4409) {
			t.Errorf("CloseStatus(err) = %d, want 4409", status)
		}
	case <-ctx.Done():
		t.Fatal("Connect did not return before ctx deadline")
	}

	if got := relay.connCount.Load(); got != 1 {
		t.Errorf("connCount = %d, want 1 (no reconnect)", got)
	}

	close(stopSend)
	<-sendDone
}

// TestAwaitCloseStatus_GraceBranchPreservesCloseError is the deterministic
// regression test for #290: when the first pump error has no close
// status (sendPump mid-write fail, pingLoop ctx.Done), awaitCloseStatus
// must wait up to grace for a subsequent CloseError so the
// FatalCloseCodes check in Connect sees the peer's actual status.
//
// Three cases pin the contract:
//
//  1. CloseError first → no grace wait, returns immediately. (Order A.)
//  2. Non-close error first, CloseError second within grace → returns
//     both, preserving the close status in slot 1. (Order B; the case
//     #290 exists for. Without the grace branch, slot 1 would be drained
//     only AFTER cancel(), at which point coder/websocket's
//     prepareRead.done() override has clobbered the inbound CloseError
//     with ctx.Err().)
//  3. Non-close error first, nothing else within grace → returns just
//     the one error after grace expires. Cancel proceeds as today.
//
// Test #2 is the regression-catching one. Revert serve's grace branch
// (i.e. read once, cancel, drain rest) and the slot-1 CloseError is
// gone — the equivalent of this test against that helper variant fails.
func TestAwaitCloseStatus_GraceBranchPreservesCloseError(t *testing.T) {
	t.Parallel()

	closeErr := fmt.Errorf("recv: %w", websocket.CloseError{Code: websocket.StatusCode(4409), Reason: "x"})
	nonCloseErr := errors.New("send: use of closed network connection")

	t.Run("close_first_skips_grace", func(t *testing.T) {
		t.Parallel()
		errCh := make(chan error, 3)
		errCh <- closeErr
		start := time.Now()
		errs := awaitCloseStatus(errCh, 100*time.Millisecond)
		if elapsed := time.Since(start); elapsed > 20*time.Millisecond {
			t.Errorf("awaitCloseStatus blocked %v with close-first; expected near-instant", elapsed)
		}
		if len(errs) != 1 {
			t.Fatalf("len(errs) = %d, want 1", len(errs))
		}
		if status := websocket.CloseStatus(errs[0]); status != websocket.StatusCode(4409) {
			t.Errorf("CloseStatus(errs[0]) = %d, want 4409", status)
		}
	})

	t.Run("non_close_first_grace_catches_close", func(t *testing.T) {
		t.Parallel()
		errCh := make(chan error, 3)
		errCh <- nonCloseErr
		// CloseError arrives shortly after; the grace window must
		// pick it up so the close status survives into errs[1].
		go func() {
			time.Sleep(5 * time.Millisecond)
			errCh <- closeErr
		}()
		errs := awaitCloseStatus(errCh, 200*time.Millisecond)
		if len(errs) != 2 {
			t.Fatalf("len(errs) = %d, want 2 (grace did not catch close)", len(errs))
		}
		var got websocket.StatusCode = -1
		for _, e := range errs {
			if s := websocket.CloseStatus(e); s != -1 {
				got = s
			}
		}
		if got != websocket.StatusCode(4409) {
			t.Errorf("no slot of errs[] carries close status 4409: %v", errs)
		}
	})

	t.Run("non_close_first_grace_expires", func(t *testing.T) {
		t.Parallel()
		errCh := make(chan error, 3)
		errCh <- nonCloseErr
		start := time.Now()
		errs := awaitCloseStatus(errCh, 10*time.Millisecond)
		elapsed := time.Since(start)
		if elapsed < 10*time.Millisecond {
			t.Errorf("awaitCloseStatus returned in %v, want ≥ 10ms (grace did not wait)", elapsed)
		}
		if len(errs) != 1 {
			t.Fatalf("len(errs) = %d, want 1 (grace expiry path)", len(errs))
		}
	})
}

func TestFatalCloseCodes_EmptyPreservesReconnect(t *testing.T) {
	t.Parallel()
	relay := newCloseCodeRelay(t, websocket.StatusCode(4409), "server-id conflict")
	cfg := Config{
		URL:          relay.URL(),
		Logger:       testLogger(t),
		WriteTimeout: time.Second,
		// FatalCloseCodes intentionally empty.
	}
	c := newClientForTest(t, cfg, testOpts{
		seed:             1,
		pingInterval:     500 * time.Millisecond,
		pongTimeout:      500 * time.Millisecond,
		reconnectInitial: 10 * time.Millisecond,
		reconnectMax:     50 * time.Millisecond,
		stabilityReset:   1 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = c.Close()
	})
	connectErr := make(chan error, 1)
	go func() { connectErr <- c.Connect(ctx) }()

	// Empty FatalCloseCodes → client keeps redialing despite 4409.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if relay.connCount.Load() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := relay.connCount.Load(); got < 2 {
		t.Errorf("connCount = %d, want ≥ 2 (reconnect not preserved)", got)
	}
}

func TestReceive_ReturnsErrDisconnectedOnConnDrop(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)
	cfg := Config{
		URL:          relay.URL(),
		Logger:       testLogger(t),
		WriteTimeout: time.Second,
	}
	c := newClientForTest(t, cfg, testOpts{
		seed:             1,
		pingInterval:     500 * time.Millisecond,
		pongTimeout:      500 * time.Millisecond,
		reconnectInitial: 200 * time.Millisecond,
		reconnectMax:     500 * time.Millisecond,
		stabilityReset:   1 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = c.Close()
	})
	connectErr := make(chan error, 1)
	go func() { connectErr <- c.Connect(ctx) }()

	select {
	case <-c.Connected():
	case <-time.After(2 * time.Second):
		t.Fatal("Connected did not fire")
	}

	type recvResult struct {
		data []byte
		err  error
	}
	resCh := make(chan recvResult, 1)
	go func() {
		data, err := c.Receive(context.Background())
		resCh <- recvResult{data, err}
	}()
	// Give Receive a moment to enter the select.
	time.Sleep(50 * time.Millisecond)
	relay.ForceClose()
	select {
	case res := <-resCh:
		if !errors.Is(res.err, ErrDisconnected) {
			t.Errorf("Receive returned err=%v, want ErrDisconnected", res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Receive did not return after conn drop")
	}
}

func TestReceive_BeforeConnectReturnsErrDisconnected(t *testing.T) {
	t.Parallel()
	c := New(Config{Logger: testLogger(t), WriteTimeout: time.Second})
	t.Cleanup(func() { _ = c.Close() })
	_, err := c.Receive(context.Background())
	if !errors.Is(err, ErrDisconnected) {
		t.Errorf("Receive before Connect: err = %v, want ErrDisconnected", err)
	}
}

func TestDropConn_TriggersReconnect(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)
	cfg := Config{
		URL:          relay.URL(),
		Logger:       testLogger(t),
		WriteTimeout: time.Second,
	}
	c := newClientForTest(t, cfg, testOpts{
		seed:             1,
		pingInterval:     500 * time.Millisecond,
		pongTimeout:      500 * time.Millisecond,
		reconnectInitial: 10 * time.Millisecond,
		reconnectMax:     50 * time.Millisecond,
		stabilityReset:   1 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = c.Close()
	})
	connectErr := make(chan error, 1)
	go func() { connectErr <- c.Connect(ctx) }()

	select {
	case <-c.Connected():
	case <-time.After(2 * time.Second):
		t.Fatal("Connected did not fire on first conn")
	}
	c.DropConn()
	select {
	case <-c.Connected():
	case <-time.After(2 * time.Second):
		t.Fatalf("Connected did not fire after DropConn; ConnCount=%d", relay.ConnCount())
	}
}

func TestDropConn_BeforeConnect(t *testing.T) {
	t.Parallel()
	c := New(Config{Logger: testLogger(t), WriteTimeout: time.Second})
	t.Cleanup(func() { _ = c.Close() })
	// Must not panic when no live conn.
	c.DropConn()
}
