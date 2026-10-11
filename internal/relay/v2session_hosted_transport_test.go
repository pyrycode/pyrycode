package relay

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/transport"
)

// The gate pauses the pump after acceptance, before acquiring the socket writer.
// Holding a socket writer instead would also block a bypassing direct write.
type testHostedPumpGate struct {
	testHostedLog
	mu      sync.Mutex
	entered chan struct{}
	release chan struct{}
}

func (g *testHostedPumpGate) arm(t *testing.T) (<-chan struct{}, func()) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	g.mu.Lock()
	g.entered, g.release = entered, release
	g.mu.Unlock()
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	return entered, unblock
}

func (g *testHostedPumpGate) Handle(ctx context.Context, r slog.Record) error {
	accepted := false
	r.Attrs(func(a slog.Attr) bool {
		accepted = accepted || a.Key == "event" && a.Value.String() == "transport.send.accepted"
		return true
	})
	if accepted {
		g.mu.Lock()
		entered, release := g.entered, g.release
		g.entered, g.release = nil, nil
		g.mu.Unlock()
		if entered != nil {
			close(entered)
			<-release
		}
	}
	return g.testHostedLog.Handle(ctx, r)
}

type testHostedTransport struct {
	t         *testing.T
	mgr       *V2SessionManager
	conn      *Connection
	socket    *websocket.Conn
	wires     chan protocol.RoutingEnvelope
	gate      *testHostedPumpGate
	entered   chan struct{}
	delivered chan error
	pub       []byte
	stop      func()
}

func testHostedRealTransport(t *testing.T, enabled bool) *testHostedTransport {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	p := &testHostedTransport{t: t, wires: make(chan protocol.RoutingEnvelope, 32), entered: make(chan struct{}, 32), delivered: make(chan error, 32)}
	connected := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		connected <- ws
		for {
			_, raw, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var e protocol.RoutingEnvelope
			if json.Unmarshal(raw, &e) != nil {
				return
			}
			select {
			case p.wires <- e:
			case <-ctx.Done():
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	events := make(chan testHostedEvent, 2048)
	p.gate = &testHostedPumpGate{testHostedLog: testHostedLog{events: events}}
	logger := slog.New(p.gate)
	client := transport.New(transport.Config{URL: "ws" + strings.TrimPrefix(server.URL, "http"), Logger: logger, WriteTimeout: time.Second})
	p.conn = connectWithClient(ctx, Config{Logger: logger}, client)
	t.Cleanup(func() {
		cancel()
		_ = p.conn.Close()
		_ = p.conn.Wait()
	})
	select {
	case p.socket = <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("socket did not connect")
	}
	select {
	case <-p.conn.Reconnected():
	case <-time.After(3 * time.Second):
		t.Fatal("transport did not become live")
	}
	priv, pub := genV2Keypair(t)
	p.pub = pub
	cfg := V2SessionConfig{Frames: p.conn.Frames(), Outbound: p.conn.Send, StaticPriv: priv, Devices: v2PairedRegistry(t, v2TestToken), ServerID: v2TestServerID, Logger: logger}
	cfg.OutboundContext = func(ctx context.Context, e protocol.RoutingEnvelope) error {
		p.entered <- struct{}{}
		err := p.conn.SendContext(ctx, e)
		p.delivered <- err
		return err
	}
	if enabled {
		cfg.HostedAppsDiscovery = &testAppDiscovery{}
		cfg.HostedAppsReady = func() bool { return true }
		cfg.HostedAppsSend = cfg.OutboundContext
	}
	p.mgr, p.stop = startManager(t, cfg)
	t.Cleanup(p.stop)
	return p
}

func (p *testHostedTransport) write(e protocol.RoutingEnvelope) {
	p.t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		p.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(p.t.Context(), 3*time.Second)
	defer cancel()
	if err := p.socket.Write(ctx, websocket.MessageText, raw); err != nil {
		p.t.Fatal(err)
	}
}

func (p *testHostedTransport) next() protocol.RoutingEnvelope {
	p.t.Helper()
	select {
	case e := <-p.wires:
		return e
	case <-time.After(3 * time.Second):
		p.t.Fatal("no wire output")
		return protocol.RoutingEnvelope{}
	}
}

func (p *testHostedTransport) hello(caps []string) *noise.Initiator {
	p.t.Helper()
	init, err := noise.NewInitiator(v2TestInstallPriv, p.pub)
	if err != nil {
		p.t.Fatal(err)
	}
	msg, err := init.WriteInit(buildHelloEarlyDataCaps(p.t, v2TestToken, caps))
	if err != nil {
		p.t.Fatal(err)
	}
	p.write(wrapInnerFrame(p.t, v2TestConnID, protocol.TypeNoiseInit, msg))
	waitConnOpen(p.t, p.mgr, v2TestConnID)
	return init
}

func (p *testHostedTransport) push() {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(p.t.Context(), 3*time.Second)
	defer cancel()
	if err := p.mgr.Push(ctx, v2TestConnID, protocol.Envelope{ID: 2, Type: protocol.TypeMessage, TS: time.Now().UTC(), Payload: json.RawMessage(`{"text":"legacy"}`)}); err != nil {
		p.t.Fatal(err)
	}
	if err := p.mgr.FlushPushes(ctx); err != nil {
		p.t.Fatal(err)
	}
}

func TestHostedDiscoveryRealTransportOrdering(t *testing.T) {
	for _, initial := range []bool{false, true} {
		name := "legacy_to_hosted"
		if initial {
			name = "initial_handshake_to_hosted_push"
		}
		t.Run(name, func(t *testing.T) {
			p := testHostedRealTransport(t, initial)
			var init *noise.Initiator
			var recv *noise.CipherState
			var release func()
			if initial {
				entered, unblock := p.gate.arm(t)
				release = unblock
				init = p.hello([]string{protocol.CapabilityHostedAppsV1})
				testHostedAwait(t, entered)
				p.push()
			} else {
				init = p.hello(nil)
				_, send, receive, err := init.ReadResp(decodeRespFrame(t, p.next()))
				if err != nil {
					t.Fatal(err)
				}
				recv = receive
				entered, unblock := p.gate.arm(t)
				release = unblock
				p.push()
				testHostedAwait(t, entered)
				msg, err := send.Encrypt([]byte(`{"id":10,"type":"list_apps","ts":"2026-10-11T09:00:00Z","payload":{}}`))
				if err != nil {
					t.Fatal(err)
				}
				p.write(wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseMsg, msg))
			}
			testHostedAwait(t, p.entered)
			// The callback is outstanding, but Run must still service control work.
			waitConnOpen(t, p.mgr, v2TestConnID)
			select {
			case <-p.delivered:
				// A bypassed writer completes before the paused pump resumes.
			case <-time.After(100 * time.Millisecond):
			}
			release()
			if initial {
				ack, _, receive, err := init.ReadResp(decodeRespFrame(t, p.next()))
				if err != nil {
					t.Fatal(err)
				}
				recv = receive
				var e protocol.Envelope
				if json.Unmarshal(ack, &e) != nil || e.ID != 1 {
					t.Fatalf("hello_ack: %s", ack)
				}
			}
			e := decryptAppFrame(t, p.next(), recv)
			if e.Type != protocol.TypeMessage || e.ID != 2 {
				t.Fatalf("first application frame: %+v", e)
			}
			if !initial {
				e = decryptAppFrame(t, p.next(), recv)
				var errPayload protocol.ErrorPayload
				if json.Unmarshal(e.Payload, &errPayload) != nil || e.ID != 3 || e.InReplyTo == nil || *e.InReplyTo != 10 || errPayload.Code != protocol.CodeProtocolUnsupported {
					t.Fatalf("unsupported reply: %+v", e)
				}
			}
		})
	}
}

func TestHostedDiscoveryRealTransportRetirement(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "disconnect"
		if shutdown {
			name = "shutdown"
		}
		t.Run(name, func(t *testing.T) {
			p := testHostedRealTransport(t, false)
			init := p.hello(nil)
			_, send, recv, err := init.ReadResp(decodeRespFrame(t, p.next()))
			if err != nil {
				t.Fatal(err)
			}
			entered, release := p.gate.arm(t)
			p.push()
			testHostedAwait(t, entered)
			msg, err := send.Encrypt([]byte(`{"id":10,"type":"list_apps","ts":"2026-10-11T09:00:00Z","payload":{}}`))
			if err != nil {
				t.Fatal(err)
			}
			p.write(wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseMsg, msg))
			testHostedAwait(t, p.entered)
			if shutdown {
				p.stop()
			} else {
				p.write(protocol.RoutingEnvelope{ConnID: v2TestConnID, CloseCode: uint16(websocket.StatusNormalClosure)})
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				for len(p.mgr.ActiveConns(ctx)) != 0 {
					if ctx.Err() != nil {
						t.Fatal("disconnect stalled behind the pump")
					}
				}
			}
			select {
			case err := <-p.delivered:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("retired delivery: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("delivery did not retire before pump release")
			}
			release()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			// A later transport acknowledgment proves the retired request did not
			// remain between the already-accepted legacy frame and this marker.
			if err := p.conn.SendContext(ctx, protocol.RoutingEnvelope{ConnID: "barrier"}); err != nil {
				t.Fatal(err)
			}
			if e := decryptAppFrame(t, p.next(), recv); e.Type != protocol.TypeMessage {
				t.Fatalf("legacy output: %+v", e)
			}
			if e := p.next(); e.ConnID != "barrier" {
				t.Fatalf("late retired output: %+v", e)
			}
		})
	}
}
