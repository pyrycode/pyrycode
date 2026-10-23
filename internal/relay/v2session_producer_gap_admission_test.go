package relay

import (
	"context"
	"crypto/ecdh"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Pause at a contested hosted lock, then latch retirement before admission.
func testGapRetireAtLock(t *testing.T, m *V2SessionManager, s *V2Session, symbol string, run func() error) {
	t.Helper()
	m.hostedMu.Lock()
	locked := true
	defer func() {
		if locked {
			m.hostedMu.Unlock()
		}
	}()
	done := make(chan error, 1)
	go func() { done <- run() }()
	testGapAwaitBlocked(t, symbol)
	if err := m.RetireLiveState(t.Context(), s.connID); err != nil {
		t.Fatal(err)
	}
	m.hostedMu.Unlock()
	locked = false
	if err := gapDeliveryResult(t, done); err != nil {
		t.Fatal(err)
	}
}

// Route actual binary-received envelopes to two receiving phone sockets.
func testGapPhoneSockets(t *testing.T, p *testHostedTransport) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	registered := make(chan *websocket.Conn, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		registered <- ws
		<-ctx.Done()
	}))
	t.Cleanup(server.Close)
	t.Cleanup(cancel)
	peers := make(map[string]*websocket.Conn)
	phones := make([]*websocket.Conn, 0, 2)
	for _, id := range []string{v2TestConnID, "other"} {
		phone, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		phones = append(phones, phone)
		select {
		case peers[id] = <-registered:
		case <-time.After(3 * time.Second):
			t.Fatal("phone not registered")
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case e := <-p.wires:
				peer := peers[e.ConnID]
				if peer == nil {
					continue
				}
				if e.CloseCode != 0 {
					_ = peer.Close(websocket.StatusCode(e.CloseCode), "")
				} else if err := peer.Write(ctx, websocket.MessageText, e.Frame); err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		for _, phone := range phones {
			_ = phone.CloseNow()
		}
		testHostedAwait(t, done)
	})
	return phones[0], phones[1]
}

func TestProducerGapCancellationAdmission(t *testing.T) {
	p := testHostedRealTransport(t, false)
	phone, otherPhone := testGapPhoneSockets(t, p)
	m, s, rec, recv := liveFixture(t, threadCaps, nil)
	// Admit the unrelated peer on the same manager before retiring either lane.
	const peerToken = "gap-other-device"
	m.cfg.Devices.Add(devices.Device{TokenHash: devices.HashToken(peerToken), Name: "other", PairedAt: time.Now().UTC()})
	key, err := ecdh.X25519().NewPrivateKey(m.cfg.StaticPriv)
	if err != nil {
		t.Fatal(err)
	}
	init, err := noise.NewInitiator(v2TestInstallPriv, key.PublicKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	msg, err := init.WriteInit(buildHelloEarlyDataCaps(t, peerToken, threadCaps))
	if err != nil {
		t.Fatal(err)
	}
	before := len(rec.snapshot())
	m.handleFrame(t.Context(), wrapInnerFrame(t, "other", protocol.TypeNoiseInit, msg))
	_, _, peerRecv, err := init.ReadResp(decodeRespFrame(t, rec.snapshot()[before]))
	if err != nil {
		t.Fatal(err)
	}
	ps := m.sessions["other"]
	if ps == nil || ps.state != V2StateOpen || !ps.thread || !ps.interactive {
		t.Fatal("unrelated peer not authenticated and admitted")
	}
	t.Cleanup(func() { m.teardown(ps) })
	m.cfg.OutboundContext = p.conn.SendContext
	if s.hosted == nil {
		m.initHosted(t.Context(), s)
	}
	if ps.hosted == nil {
		m.initHosted(t.Context(), ps)
	}
	queryCtx, cancelQuery := context.WithCancel(t.Context())
	defer cancelQuery()
	s.hosted.inbox.query = &threadQuery{conversation: gateClaudeConv, ctx: queryCtx, cancel: cancelQuery}
	entered, release := p.gate.arm(t)
	env := protocol.Envelope{Type: protocol.TypeTurnState, Payload: json.RawMessage(`{"state":"running"}`)}
	m.hostedEnqueue(t.Context(), s, &hostedOutput{env: &env, callerCtx: queryCtx})
	active := s.hosted.active
	testHostedAwait(t, entered)
	testGapRetireAtLock(t, m, s, "cancelThreadQuery", func() error {
		return m.Push(t.Context(), s.connID, protocol.Envelope{Type: protocol.TypeThreadItemAdded, Payload: json.RawMessage(`{"conversation_id":"` + gateClaudeConv + `"}`)})
	})
	if err := queryCtx.Err(); err != nil {
		t.Errorf("quarantined push cancelled query: %v", err)
	}
	if err := s.hosted.deliveryCtx.Err(); err != nil {
		t.Errorf("quarantined push cancelled selected delivery: %v", err)
	}
	release()
	if err := gapDeliveryResult(t, active.deliveryDone); err != nil {
		t.Fatalf("selected transport send failed: %v", err)
	}
	m.handleHostedDone(t.Context(), hostedResult{s: s, output: active})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, raw, err := phone.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if e := decryptAppFrame(t, protocol.RoutingEnvelope{Frame: raw}, recv); e.Type != env.Type || string(e.Payload) != string(env.Payload) {
		t.Fatalf("selected phone frame = %+v", e)
	}
	if _, _, err := phone.Read(ctx); websocket.CloseStatus(err) != StatusProducerGap {
		t.Fatalf("phone close = %v, want 4414", err)
	}
	// The unrelated authenticated Noise lane uses the same binary generation.
	m.hostedEnqueue(t.Context(), ps, &hostedOutput{env: &env})
	if err := gapDeliveryResult(t, ps.hosted.active.deliveryDone); err != nil {
		t.Fatal(err)
	}
	m.handleHostedDone(t.Context(), hostedResult{s: ps, output: ps.hosted.active})
	_, raw, err = otherPhone.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if e := decryptAppFrame(t, protocol.RoutingEnvelope{Frame: raw}, peerRecv); e.Type != env.Type {
		t.Fatal(e.Type)
	}
	select {
	case <-p.socketDone:
		t.Fatal("retirement replaced the shared binary socket")
	default:
	}
}

func TestProducerGapSelectionAdmission(t *testing.T) {
	for _, boundary := range []string{"takeHostedChanges", "threadQueryGate", "transportProbe"} {
		t.Run(boundary, func(t *testing.T) {
			m, s, _, recv := liveFixture(t, threadCaps, nil)
			wires := make(chan protocol.RoutingEnvelope, 4)
			m.cfg.OutboundContext = func(_ context.Context, e protocol.RoutingEnvelope) error { wires <- e; return nil }
			if s.hosted == nil {
				m.initHosted(t.Context(), s)
			}
			env := protocol.Envelope{Type: protocol.TypeTurnState, Payload: json.RawMessage(`{"dependent":true}`)}
			s.hosted.queue = []*hostedOutput{{env: &env}}
			s.hosted.inbox.used = 1
			if boundary == "threadQueryGate" {
				// Withholding runs after takeHostedChanges and before the query gate.
				m.cfg.KnownConversation = func(string) bool { m.hostedMu.Lock(); return true }
				env.Type = protocol.TypeThreadItemAdded
				env.Payload = json.RawMessage(`{"conversation_id":"` + gateClaudeConv + `"}`)
			}
			nextID := s.nextID
			if boundary == "transportProbe" {
				entered, release := make(chan struct{}), make(chan struct{})
				m.cfg.Connected = func() bool { close(entered); <-release; return true }
				done := make(chan struct{})
				go func() { m.hostedDeliver(t.Context(), s); close(done) }()
				testHostedAwait(t, entered)
				if err := m.RetireLiveState(t.Context(), s.connID); err != nil {
					t.Fatal(err)
				}
				close(release)
				testHostedAwait(t, done)
			} else if boundary == "threadQueryGate" {
				// The provider holds hostedMu on this same Run before reaching the gate.
				done := make(chan struct{})
				go func() { m.hostedDeliver(t.Context(), s); close(done) }()
				testGapAwaitBlocked(t, "threadQueryGate")
				if err := m.RetireLiveState(t.Context(), s.connID); err != nil {
					t.Fatal(err)
				}
				m.hostedMu.Unlock()
				testHostedAwait(t, done)
			} else {
				testGapRetireAtLock(t, m, s, boundary, func() error { m.hostedDeliver(t.Context(), s); return nil })
			}
			if s.nextID != nextID {
				t.Errorf("retired selection spent sender ID: %d -> %d", nextID, s.nextID)
			}
			m.handlePushOverflow(t.Context(), s.connID)
			select {
			case e := <-wires:
				if e.CloseCode != uint16(StatusProducerGap) {
					t.Errorf("quarantined wire = %+v", e)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("terminal output missing")
			}
			sealed, err := s.send.Encrypt([]byte("nonce probe"))
			if err != nil {
				t.Fatal(err)
			}
			if plain, err := recv.Decrypt(sealed); err != nil || string(plain) != "nonce probe" {
				t.Fatalf("retired selection spent Noise nonce: %q %v", plain, err)
			}
		})
	}
}

func testGapAwaitBlocked(t *testing.T, symbol string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		stack := make([]byte, 1<<20)
		n := runtime.Stack(stack, true)
		for _, g := range strings.Split(string(stack[:n]), "\n\n") {
			if strings.Contains(g, "(*V2SessionManager)."+symbol+"(") && strings.Contains(g, "sync.Mutex.Lock") {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatalf("admission never blocked in %s", symbol)
}

func TestProducerGapCapacityAdmission(t *testing.T) {
	for _, callbackOverflow := range []bool{false, true} {
		name := "capacity"
		if callbackOverflow {
			name = "callback_overflow"
		}
		t.Run(name, func(t *testing.T) {
			m, s, _, _ := liveFixture(t, threadCaps, nil)
			wires := make(chan protocol.RoutingEnvelope, 4)
			m.cfg.OutboundContext = func(_ context.Context, e protocol.RoutingEnvelope) error { wires <- e; return nil }
			if s.hosted == nil {
				m.initHosted(t.Context(), s)
			}
			s.hosted.inbox.used = hostedQueueLimit
			s.hosted.inbox.overflow = callbackOverflow
			env := protocol.Envelope{Type: protocol.TypeTurnState, Payload: json.RawMessage(`{}`)}
			testGapRetireAtLock(t, m, s, "takeHostedChanges", func() error {
				m.hostedEnqueue(t.Context(), s, &hostedOutput{env: &env})
				return nil
			})
			select {
			case e := <-wires:
				if e.CloseCode != uint16(StatusProducerGap) {
					t.Fatalf("earlier gap replaced by capacity: %+v", e)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("terminal output missing")
			}
		})
	}
}
