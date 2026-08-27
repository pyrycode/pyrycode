package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
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

// TestBackoff_ResetAfterStableConnection pins both arms of the stability
// reset in the post-serve tail of Client.Connect: an uptime at or beyond
// stabilityReset restarts the backoff ladder at 1, a shorter one keeps
// the counter climbing.
//
// The counter is read off the "transport: connected" log record, never
// off a dial interval. Connect's post-serve path redials immediately —
// the backoff sleep sits only on the dial-failure path — so the interval
// between a healthy disconnect and the next dial carries no term the
// attempt counter controls, and no wall-clock bound over it can tell the
// two arms apart (#1802).
//
// Each row fails its first two dials, so the first connected record must
// read attempt=3. Without that guard a fixture that stopped injecting
// failures would enter the tail at attempt=1 and both arms would look
// alike.
func TestBackoff_ResetAfterStableConnection(t *testing.T) {
	t.Parallel()

	// Dials 1 and 2 fail synthetically; every later dial reaches the relay.
	const failedDials = 2

	tests := []struct {
		name string
		// stabilityReset is the uptime Connect requires before it calls a
		// connection stable; hold is how long the test keeps the
		// connection up before dropping it.
		stabilityReset time.Duration
		hold           time.Duration
		wantSecond     int
	}{
		{
			name:           "stable uptime resets the counter",
			stabilityReset: 20 * time.Millisecond,
			hold:           100 * time.Millisecond,
			wantSecond:     1,
		},
		{
			// Unreachable inside a test run, so the drop below always
			// lands in the "too brief to be stable" arm.
			name:           "brief uptime keeps the counter climbing",
			stabilityReset: 10 * time.Minute,
			hold:           0,
			wantSecond:     failedDials + 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			relay := newTestRelay(t)
			rec := &recordingHandler{}

			var dials atomic.Int64
			c := newClientForTest(t, Config{Logger: slog.New(rec), WriteTimeout: time.Second}, testOpts{
				seed:             1,
				reconnectInitial: 20 * time.Millisecond,
				reconnectMax:     200 * time.Millisecond,
				stabilityReset:   tc.stabilityReset,
				pingInterval:     500 * time.Millisecond,
				pongTimeout:      500 * time.Millisecond,
				dialFn: func(ctx context.Context) (*websocket.Conn, error) {
					if dials.Add(1) <= failedDials {
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

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			connectErr := make(chan error, 1)
			go func() { connectErr <- c.Connect(ctx) }()
			t.Cleanup(func() {
				_ = c.Close()
				<-connectErr
			})

			if got := waitConnectedAttempt(t, rec, 1); got != failedDials+1 {
				t.Fatalf("first connected attempt = %d, want %d (backoff ladder never climbed)",
					got, failedDials+1)
			}
			// ForceClose can only drop conns the relay handler has already
			// registered, so wait for the accept before dropping. The hold
			// starts after the record above, which Connect emits just after
			// stamping the clock it measures uptime against — so scheduler
			// pressure can only lengthen that uptime, never shorten it.
			select {
			case <-relay.connectedCh:
			case <-time.After(10 * time.Second):
				t.Fatal("relay never registered the accepted conn")
			}
			time.Sleep(tc.hold)
			relay.ForceClose()

			if got := waitConnectedAttempt(t, rec, 2); got != tc.wantSecond {
				t.Errorf("second connected attempt = %d, want %d; records: %s",
					got, tc.wantSecond, rec.messageSummary())
			}
		})
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

func TestNew_DefaultsWriteTimeout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		given time.Duration
		want  time.Duration
	}{
		{"unset defaults to 10s", 0, defaultWriteTimeout},
		{"negative defaults to 10s", -1, defaultWriteTimeout},
		{"positive preserved", 3 * time.Second, 3 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New(Config{Logger: testLogger(t), WriteTimeout: tt.given})
			if c.cfg.WriteTimeout != tt.want {
				t.Errorf("WriteTimeout = %v, want %v", c.cfg.WriteTimeout, tt.want)
			}
		})
	}
}

// TestNew_UnsetWriteTimeout_SendSucceedsEndToEnd drives a send over the test
// relay with WriteTimeout left unset. Under the pre-#916 bug a zero
// WriteTimeout makes sendPump's first Write instantly deadline-exceed, so the
// conn drops and no echo ever returns; a successful round-trip proves New's
// default took effect.
func TestNew_UnsetWriteTimeout_SendSucceedsEndToEnd(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)
	cfg := Config{
		URL:    relay.URL(),
		Logger: testLogger(t),
		// WriteTimeout deliberately omitted — this is the whole point.
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
	// Poll Send until setConn lands (until then Send returns ErrNotConnected).
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

// TestClient_IsConnected pins the level-poll seam the v2 push drain uses to
// decide, BEFORE sealing, whether the relay leg is up (#874): false before any
// successful dial, true once a live conn is established, and false after Close
// (gated on closeCh, since Close does not nil c.conn). It must agree with Send's
// own predicate — a live conn means Send does not return ErrNotConnected.
func TestClient_IsConnected(t *testing.T) {
	t.Parallel()
	relay := newTestRelay(t)
	cfg := Config{URL: relay.URL(), Logger: testLogger(t), WriteTimeout: time.Second}
	c := newClientForTest(t, cfg, testOpts{
		seed:             1,
		pingInterval:     50 * time.Millisecond,
		pongTimeout:      500 * time.Millisecond,
		reconnectInitial: 10 * time.Millisecond,
		reconnectMax:     50 * time.Millisecond,
		stabilityReset:   1 * time.Second,
	})

	// Before Connect: no live conn.
	if c.IsConnected() {
		t.Fatal("IsConnected() = true before Connect, want false")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	connectErr := make(chan error, 1)
	go func() { connectErr <- c.Connect(ctx) }()

	select {
	case <-relay.connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("connection never established")
	}
	// setConn lands shortly after the relay's Accept; poll until IsConnected
	// agrees, mirroring the Send-becomes-live poll in TestSmoke_HttptestEchoServer.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !c.IsConnected() {
		time.Sleep(5 * time.Millisecond)
	}
	if !c.IsConnected() {
		t.Fatal("IsConnected() = false after connect, want true")
	}
	// Agrees with Send's predicate: a live conn means Send is not ErrNotConnected.
	if err := c.Send([]byte("x")); errors.Is(err, ErrNotConnected) {
		t.Errorf("Send returned ErrNotConnected while IsConnected()=true — predicates disagree")
	}

	// After Close: reads down via the closeCh gate even though Close leaves
	// c.conn non-nil.
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if c.IsConnected() {
		t.Error("IsConnected() = true after Close, want false")
	}
	select {
	case <-connectErr:
	case <-time.After(1 * time.Second):
		t.Fatal("Connect did not return after Close")
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

	// ForceClose can only drop conns the relay handler has already
	// registered, and newTestRelay's handler registers after
	// websocket.Accept returns — later than the client's own dial, which
	// unblocks on the 101. Gating on Connected alone orders nothing on the
	// relay side, so wait for the accept before dropping.
	select {
	case <-relay.connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("relay never registered the first conn")
	}
	// Draining the first signal keeps the buffer-1 Connected channel empty
	// across the drop, so the read after the reconnect observes the second
	// signal rather than a stale first one.
	select {
	case <-c.Connected():
	case <-time.After(2 * time.Second):
		t.Fatal("Connected did not fire after first connect")
	}
	relay.ForceClose()
	// Split the reconnect from the signal: a timeout here means the redial
	// never reached the relay, which is a harness fault rather than a
	// regression in Client.Connected.
	select {
	case <-relay.connectedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("client never reconnected to the relay")
	}
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
		//
		// Read-and-discard, NOT echo-back: a concurrent conn.Write here would
		// race conn.Close's close-frame write below (two writers on one conn,
		// which coder/websocket forbids), corrupting the frame stream ~1% of
		// the time and RST-ing before the client's recvPump surfaces the peer
		// close — the deep #933 flake. Draining only needs the Read. See #933.
		drainDone := make(chan struct{})
		go func() {
			defer close(drainDone)
			for {
				if _, _, err := conn.Read(ctx); err != nil {
					return
				}
			}
		}()
		select {
		case <-r.triggerClose:
		case <-ctx.Done():
		}
		_ = conn.Close(status, reason)
		<-drainDone
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
	// 15s allowances for -race + parallel self-contention on macOS; see #523.
	// The original 5s budget raced the goroutine's connectErr send against
	// ctx.Done() under -race -count=N parallelism (test-side, not a
	// production-side grace-expiry).
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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

	// On ctx.Done, give the Connect goroutine a small grace to publish its
	// buffered return value — under heavy -race contention the goroutine's
	// send to connectErr can land just after ctx.Done becomes ready, and
	// Go's select then picks ctx.Done in the outer arm even though Connect
	// returned ErrFatalClose successfully (see #523).
	var err error
	select {
	case err = <-connectErr:
	case <-ctx.Done():
		select {
		case err = <-connectErr:
		case <-time.After(500 * time.Millisecond):
			t.Fatal("Connect did not return before ctx deadline")
		}
	}
	if !errors.Is(err, ErrFatalClose) {
		t.Fatalf("Connect returned %v, want wrapping ErrFatalClose", err)
	}
	if status := websocket.CloseStatus(err); status != websocket.StatusCode(4409) {
		t.Errorf("CloseStatus(err) = %d, want 4409", status)
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

// TestRealDial_UpgradeRejected proves realDial classifies a non-101 HTTP
// response (the relay served the URL but never upgraded — e.g. a 404 on a
// wrong path) as ErrUpgradeRejected, carrying the status, using the
// coder/websocket "resp != nil ⟺ server responded" signal.
func TestRealDial_UpgradeRejected(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such path", http.StatusNotFound)
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	c := New(Config{URL: wsURL, Logger: testLogger(t), WriteTimeout: time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.realDial(ctx)
	if err == nil {
		t.Fatal("realDial against a 404 handler returned nil error")
	}
	if !errors.Is(err, ErrUpgradeRejected) {
		t.Errorf("err = %v, want wrapping ErrUpgradeRejected", err)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %q, want containing status 404", err.Error())
	}
}

// TestRealDial_NetworkFailureNotUpgradeRejected proves a network-level dial
// failure (no HTTP response at all) stays a plain "dial:" error and is NOT
// misclassified as an upgrade rejection — the resp == nil branch.
func TestRealDial_NetworkFailureNotUpgradeRejected(t *testing.T) {
	t.Parallel()
	// Reserve then release a port so the dial is refused with no HTTP
	// response (resp == nil).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	c := New(Config{URL: "ws://" + addr, Logger: testLogger(t), WriteTimeout: time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = c.realDial(ctx)
	if err == nil {
		t.Fatal("realDial against a closed port returned nil error")
	}
	if errors.Is(err, ErrUpgradeRejected) {
		t.Errorf("err = %v, must NOT wrap ErrUpgradeRejected (network failure, no HTTP response)", err)
	}
}

// recordingHandler is a slog.Handler that captures every record for
// post-hoc level/message assertions. Safe for concurrent Handle calls.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

// connectedRecordMsg is the successful-dial log line that carries the
// backoff attempt counter — the Info call preceding serve in
// Client.Connect. Renaming it there makes waitConnectedAttempt time out
// loudly rather than let a test pass on an unread counter.
const connectedRecordMsg = "transport: connected"

// connectedAttempt returns the "attempt" value on the nth (1-based)
// connectedRecordMsg record captured so far, and reports whether such a
// record carrying that attr exists yet. A record missing the attr reads
// as not-found, so a renamed attr key cannot pass for attempt 0.
func (h *recordingHandler) connectedAttempt(n int) (int, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := 0
	for _, r := range h.records {
		if r.Message != connectedRecordMsg {
			continue
		}
		seen++
		if seen < n {
			continue
		}
		attempt, found := 0, false
		r.Attrs(func(a slog.Attr) bool {
			if a.Key != "attempt" {
				return true
			}
			attempt, found = int(a.Value.Int64()), true
			return false
		})
		return attempt, found
	}
	return 0, false
}

// messageSummary lists the captured record messages with their counts in
// first-seen order, for failure diagnostics.
func (h *recordingHandler) messageSummary() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	counts := make(map[string]int, len(h.records))
	var order []string
	for _, r := range h.records {
		if counts[r.Message] == 0 {
			order = append(order, r.Message)
		}
		counts[r.Message]++
	}
	if len(order) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(order))
	for _, msg := range order {
		parts = append(parts, fmt.Sprintf("%q x%d", msg, counts[msg]))
	}
	return strings.Join(parts, ", ")
}

// waitConnectedAttempt blocks until the nth (1-based) connectedRecordMsg
// record carrying an "attempt" attr exists, and returns that attempt.
// The deadline is a liveness guard, not a verdict: it turns a wedged
// client — or a renamed message or attr key — into a diagnosable failure
// instead of a hang, and a healthy run never approaches it.
func waitConnectedAttempt(t *testing.T, h *recordingHandler, n int) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if attempt, ok := h.connectedAttempt(n); ok {
			return attempt
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("fewer than %d %q records carrying an %q attr within the deadline; records: %s",
				n, connectedRecordMsg, "attempt", h.messageSummary())
			return 0
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestConnect_LoudOnFirstUpgradeReject proves AC#1's "loud on the first
// failed upgrade, not buried": a sustained run of ErrUpgradeRejected dials
// produces exactly one WARN (carrying the actionable path hint) on the
// first attempt, with subsequent attempts demoted to the INFO backoff line.
func TestConnect_LoudOnFirstUpgradeReject(t *testing.T) {
	t.Parallel()
	rec := &recordingHandler{}
	logger := slog.New(rec)

	var dials atomic.Int64
	c := newClientForTest(t, Config{URL: "wss://example.invalid", Logger: logger, WriteTimeout: time.Second}, testOpts{
		seed:             1,
		reconnectInitial: 10 * time.Millisecond,
		reconnectMax:     20 * time.Millisecond,
		dialFn: func(ctx context.Context) (*websocket.Conn, error) {
			dials.Add(1)
			return nil, fmt.Errorf("%w (HTTP 404): boom", ErrUpgradeRejected)
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	connectErr := make(chan error, 1)
	go func() { connectErr <- c.Connect(ctx) }()

	// Let several attempts accumulate so we observe the first (WARN) and
	// at least one subsequent (INFO) rejection.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if dials.Load() >= 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-connectErr

	if got := dials.Load(); got < 3 {
		t.Fatalf("dial attempts = %d, want ≥3 to exercise first + subsequent", got)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	var warns, backoffInfos int
	var warnMsg string
	for _, r := range rec.records {
		switch r.Level {
		case slog.LevelWarn:
			warns++
			warnMsg = r.Message
		case slog.LevelInfo:
			if strings.Contains(r.Message, "dial failed") {
				backoffInfos++
			}
		}
	}
	if warns != 1 {
		t.Errorf("WARN records = %d, want exactly 1 (loud on first upgrade reject only)", warns)
	}
	if !strings.Contains(warnMsg, "verify the relay URL path") {
		t.Errorf("WARN message = %q, want the actionable path hint", warnMsg)
	}
	if backoffInfos < 1 {
		t.Errorf("INFO backoff records = %d, want ≥1 (subsequent rejects demoted to INFO)", backoffInfos)
	}
}

// --- FatalCloseThreshold (#1072) ---
//
// The 2026-07-16 incident: a pong timeout dropped a healthy conn, the loop
// re-dialed within ~75ms, the relay had not yet noticed the old conn was
// dead, and the resulting one-shot 4409 was treated as fatal — a transient
// self-reconnect race became a multi-day outage. FatalCloseThreshold makes
// a fatal close code terminal only when it persists across N consecutive
// observations, with the standard backoff ladder between attempts.

// scriptedCloseRelay runs a per-accept behavior script: "conflict" closes
// the accepted conn with the configured status immediately; "serve" holds
// the conn open (echoing frames) until force-closed. Accepts beyond the
// script's end repeat the last entry.
type scriptedCloseRelay struct {
	mu        sync.Mutex
	server    *httptest.Server
	script    []string
	conns     []*websocket.Conn
	connCount atomic.Int64
	servedCh  chan struct{}
}

func newScriptedCloseRelay(t *testing.T, status websocket.StatusCode, reason string, script []string) *scriptedCloseRelay {
	t.Helper()
	r := &scriptedCloseRelay{
		script:   script,
		servedCh: make(chan struct{}, 16),
	}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		n := int(r.connCount.Add(1)) - 1
		r.mu.Lock()
		step := r.script[len(r.script)-1]
		if n < len(r.script) {
			step = r.script[n]
		}
		r.mu.Unlock()
		if step == "conflict" {
			_ = conn.Close(status, reason)
			return
		}
		r.mu.Lock()
		r.conns = append(r.conns, conn)
		r.mu.Unlock()
		select {
		case r.servedCh <- struct{}{}:
		default:
		}
		ctx := req.Context()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if err := conn.Write(ctx, typ, data); err != nil {
				return
			}
		}
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *scriptedCloseRelay) URL() string {
	return "ws" + strings.TrimPrefix(r.server.URL, "http")
}

func (r *scriptedCloseRelay) ForceClose() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.conns {
		_ = c.CloseNow()
	}
	r.conns = nil
}

// TestFatalCloseThreshold_TransientConflictRecovers pins the incident
// regression: two consecutive 4409 closes below the threshold must NOT
// terminate Connect — the loop backs off, retries, and the third accept
// serves. The client must reach a live conn.
func TestFatalCloseThreshold_TransientConflictRecovers(t *testing.T) {
	t.Parallel()
	relay := newScriptedCloseRelay(t, websocket.StatusCode(4409), "server-id conflict",
		[]string{"conflict", "conflict", "serve"})
	cfg := Config{
		URL:                 relay.URL(),
		Logger:              testLogger(t),
		WriteTimeout:        time.Second,
		FatalCloseCodes:     []websocket.StatusCode{websocket.StatusCode(4409)},
		FatalCloseThreshold: 3,
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
	case <-relay.servedCh:
	case err := <-connectErr:
		t.Fatalf("Connect terminated with %v before surviving the transient conflict", err)
	case <-time.After(3 * time.Second):
		t.Fatal("client never reached a served conn after transient 4409s")
	}
	if got := relay.connCount.Load(); got != 3 {
		t.Errorf("connCount = %d, want 3 (two conflicts + one served)", got)
	}
	select {
	case err := <-connectErr:
		t.Fatalf("Connect terminated with %v after the served conn", err)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestFatalCloseThreshold_PersistentConflictGoesFatal pins the
// genuine-duplicate case: when every accept 4409-closes, Connect must
// return ErrFatalClose once the consecutive count reaches the threshold —
// no earlier (transient races get their window) and no later (a real
// duplicate still steps aside).
func TestFatalCloseThreshold_PersistentConflictGoesFatal(t *testing.T) {
	t.Parallel()
	relay := newCloseCodeRelay(t, websocket.StatusCode(4409), "server-id conflict")
	cfg := Config{
		URL:                 relay.URL(),
		Logger:              testLogger(t),
		WriteTimeout:        time.Second,
		FatalCloseCodes:     []websocket.StatusCode{websocket.StatusCode(4409)},
		FatalCloseThreshold: 3,
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

	err := c.Connect(ctx)
	if !errors.Is(err, ErrFatalClose) {
		t.Fatalf("Connect returned %v, want wrapping ErrFatalClose", err)
	}
	if status := websocket.CloseStatus(err); status != websocket.StatusCode(4409) {
		t.Errorf("CloseStatus(err) = %d, want 4409", status)
	}
	if got := relay.connCount.Load(); got != 3 {
		t.Errorf("connCount = %d, want 3 (threshold consecutive conflicts)", got)
	}
}

// TestFatalCloseThreshold_CounterResetsOnHealthyConn pins the CONSECUTIVE
// semantics: a cycle that ends without a fatal close resets the counter,
// so two separate transient conflict episodes (2 + 2 with a healthy served
// conn between) never sum to the threshold of 3.
func TestFatalCloseThreshold_CounterResetsOnHealthyConn(t *testing.T) {
	t.Parallel()
	relay := newScriptedCloseRelay(t, websocket.StatusCode(4409), "server-id conflict",
		[]string{"conflict", "conflict", "serve", "conflict", "conflict", "serve"})
	cfg := Config{
		URL:                 relay.URL(),
		Logger:              testLogger(t),
		WriteTimeout:        time.Second,
		FatalCloseCodes:     []websocket.StatusCode{websocket.StatusCode(4409)},
		FatalCloseThreshold: 3,
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
	case <-relay.servedCh:
	case err := <-connectErr:
		t.Fatalf("Connect terminated with %v during first conflict episode", err)
	case <-time.After(3 * time.Second):
		t.Fatal("client never reached the first served conn")
	}
	relay.ForceClose()
	select {
	case <-relay.servedCh:
	case err := <-connectErr:
		t.Fatalf("Connect terminated with %v during second conflict episode (counter did not reset)", err)
	case <-time.After(3 * time.Second):
		t.Fatal("client never reached the second served conn")
	}
	if got := relay.connCount.Load(); got != 6 {
		t.Errorf("connCount = %d, want 6", got)
	}
}

// TestFatalCloseThreshold_DialPathCounts pins that fatal closes surfacing
// from Dial itself (relay closes mid-upgrade) share the same consecutive
// counter and backoff as the post-serve path.
func TestFatalCloseThreshold_DialPathCounts(t *testing.T) {
	t.Parallel()
	cfg := Config{
		URL:                 "wss://example.invalid",
		Logger:              testLogger(t),
		WriteTimeout:        time.Second,
		FatalCloseCodes:     []websocket.StatusCode{websocket.StatusCode(4409)},
		FatalCloseThreshold: 3,
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
	if got := dials.Load(); got != 3 {
		t.Errorf("dialFn invocations = %d, want 3 (threshold consecutive fatal dials)", got)
	}
}
