// Package fakerelay is an in-process WebSocket server that speaks the
// routing half of the mobile↔relay protocol (docs/protocol-mobile.md
// § Authentication, § Routing envelope). It exists so daemon-side e2e
// tests can exercise the full WS roundtrip — binary ↔ relay ↔ phone —
// without depending on the pyrycode-relay binary or live infrastructure.
//
// The package lives under internal/e2e/internal/ to visibility-fence it
// from non-e2e callers. The sibling fake-phone client (separate ticket)
// and the consuming roundtrip test (third ticket) wire up around it.
//
// # Wire contract
//
// Binary upgrades on /v1/server with header x-pyrycode-server (the
// claimed server-id). First-claim-wins: while a binary holds a server-id,
// further /v1/server upgrades for that id are rejected.
//
// Phone upgrades on /v1/client with headers x-pyrycode-server,
// x-pyrycode-token, x-pyrycode-device-name. The relay does NOT validate
// the token contents; the binary owns that check. If no binary is bound
// to the requested server-id, the upgrade is rejected.
//
// Each accepted phone receives a fresh, monotonically-numbered conn_id
// ("c-1", "c-2", …). The relay wraps every phone→binary frame as
// {"conn_id": "...", "frame": <phone-frame>} and unwraps every
// binary→phone frame, sending only the inner frame onto the phone WS.
//
// # Deviations from the production wire spec (deliberate)
//
//   - Rejections happen pre-upgrade as HTTP 400/409/503 rather than as
//     post-upgrade WS close codes (4409/4404/4401). The AC is satisfied
//     by either form, and HTTP status surfaces directly in the dial
//     error, which is simpler for consumer tests to assert against.
//   - No 30-second grace period on server-id release: when the binary
//     disconnects, its server-id is immediately reusable.
//   - No TLS, no persistence, no rate limiting.
//   - Binary-direct "hello" envelopes (no conn_id) get a synthesized
//     hello_ack reply so real pyry binaries can complete their
//     binary↔relay handshake against the harness. Other binary-direct
//     envelope types are dropped (the dispatcher slice consumes them).
package fakerelay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/coder/websocket"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// maxFrameBytes mirrors internal/transport's per-frame ceiling so the
// harness rejects oversized frames the same way the production binary
// would. Tests never approach this limit.
const maxFrameBytes = 1 << 20

// Server is a running fake relay. Construct with New; shut down with
// Close. Safe for concurrent use across handler goroutines; consumers
// observe behavior through the WS endpoints, not by reaching in.
type Server struct {
	log  *slog.Logger
	http *httptest.Server

	mu       sync.Mutex
	closed   bool
	binaries map[string]*binaryConn // serverID -> binary
	phones   map[string]*phoneConn  // connID    -> phone
	connSeq  uint64

	// rejectNextBinaryWith4409, when true, causes the next /v1/server
	// upgrade to accept the WS handshake and immediately close with WS
	// code 4409 ("server-id already claimed"). The flag clears after
	// one use. Set via RejectNextBinaryWith4409.
	rejectNextBinaryWith4409 bool

	// lastBinaryHello records, per server-id, the most recent
	// binary-direct "hello" envelope observed by binaryRecvPump. Read
	// via LastBinaryHello. Used by e2e tests to assert the handshake
	// payload (role, server_id, binary_version, protocol_versions)
	// without intercepting the WS framing.
	lastBinaryHello map[string]protocol.Envelope
}

type binaryConn struct {
	serverID string
	conn     *websocket.Conn
	sendCh   chan []byte
	done     chan struct{}
	cancel   context.CancelFunc
}

type phoneConn struct {
	serverID string
	connID   string
	conn     *websocket.Conn
	sendCh   chan []byte
	done     chan struct{}
	cancel   context.CancelFunc
}

// New returns a running fake relay bound to a random localhost port.
// The returned Server is ready for connections; Close shuts it down.
// logger is required (Debug-level lifecycle and routing events); nil
// panics at construction.
func New(logger *slog.Logger) *Server {
	if logger == nil {
		panic("fakerelay: logger is required")
	}
	s := &Server{
		log:             logger,
		binaries:        make(map[string]*binaryConn),
		phones:          make(map[string]*phoneConn),
		lastBinaryHello: make(map[string]protocol.Envelope),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/server", s.handleBinary)
	mux.HandleFunc("/v1/client", s.handlePhone)
	s.http = httptest.NewServer(mux)
	return s
}

// URL reports the base ws:// URL (no trailing path). Callers append
// "/v1/server" or "/v1/client".
func (s *Server) URL() string {
	return "ws" + strings.TrimPrefix(s.http.URL, "http")
}

// Close shuts down the listener and all in-flight conns. Idempotent.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	bins := make([]*binaryConn, 0, len(s.binaries))
	for _, b := range s.binaries {
		bins = append(bins, b)
	}
	phs := make([]*phoneConn, 0, len(s.phones))
	for _, p := range s.phones {
		phs = append(phs, p)
	}
	s.mu.Unlock()

	// Force-close every active WS conn. The per-conn pumps observe the
	// closed conn as a read/write error and unwind; their handlers
	// return; httptest.Server.Close below waits for those handlers.
	for _, b := range bins {
		b.cancel()
		_ = b.conn.Close(websocket.StatusNormalClosure, "server closing")
	}
	for _, p := range phs {
		p.cancel()
		_ = p.conn.Close(websocket.StatusNormalClosure, "server closing")
	}
	s.http.Close()
	return nil
}

// --- /v1/server ---

func (s *Server) handleBinary(w http.ResponseWriter, r *http.Request) {
	serverID := r.Header.Get("X-Pyrycode-Server")
	if serverID == "" {
		http.Error(w, "missing x-pyrycode-server", http.StatusBadRequest)
		return
	}

	// Opt-in: simulate the production WS-close-4409 path for the next
	// binary upgrade. Accept the upgrade, then close with code 4409.
	// Existing tests rely on the HTTP-409 first-claim-wins path; this
	// branch is a second mode the e2e suite enables explicitly.
	s.mu.Lock()
	fail4409 := s.rejectNextBinaryWith4409
	if fail4409 {
		s.rejectNextBinaryWith4409 = false
	}
	s.mu.Unlock()
	if fail4409 {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			s.log.Debug("fakerelay: binary accept failed (4409 path)", "err", err)
			return
		}
		_ = conn.Close(websocket.StatusCode(4409), "server-id already claimed")
		return
	}

	// Pre-upgrade claim check. Re-checked after Accept to close the
	// race where two binaries upgrade concurrently for the same id.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		http.Error(w, "server closing", http.StatusServiceUnavailable)
		return
	}
	if _, exists := s.binaries[serverID]; exists {
		s.mu.Unlock()
		http.Error(w, "server-id already claimed", http.StatusConflict)
		return
	}
	s.mu.Unlock()

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.log.Debug("fakerelay: binary accept failed", "err", err)
		return
	}
	conn.SetReadLimit(maxFrameBytes)

	ctx, cancel := context.WithCancel(r.Context())
	bc := &binaryConn{
		serverID: serverID,
		conn:     conn,
		sendCh:   make(chan []byte),
		done:     make(chan struct{}),
		cancel:   cancel,
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		_ = conn.Close(websocket.StatusNormalClosure, "server closing")
		return
	}
	if _, exists := s.binaries[serverID]; exists {
		s.mu.Unlock()
		cancel()
		_ = conn.Close(websocket.StatusPolicyViolation, "server-id claimed")
		return
	}
	s.binaries[serverID] = bc
	s.mu.Unlock()

	s.log.Debug("fakerelay: binary connected", "server_id", serverID)

	s.serveBinary(ctx, bc)

	// Cleanup: drop the binary from the registry and tear down every
	// phone bound to its server-id. Phones whose binary has gone away
	// cannot be routed anywhere, so the harness drops them — matching
	// the production relay's behavior and preventing goroutine leaks
	// in tests where the binary disconnects first.
	s.mu.Lock()
	delete(s.binaries, serverID)
	stranded := make([]*phoneConn, 0, len(s.phones))
	for _, p := range s.phones {
		if p.serverID == serverID {
			stranded = append(stranded, p)
		}
	}
	s.mu.Unlock()
	for _, p := range stranded {
		p.cancel()
		_ = p.conn.Close(websocket.StatusNormalClosure, "binary disconnected")
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
	s.log.Debug("fakerelay: binary disconnected", "server_id", serverID)
}

// --- /v1/client ---

func (s *Server) handlePhone(w http.ResponseWriter, r *http.Request) {
	serverID := r.Header.Get("X-Pyrycode-Server")
	token := r.Header.Get("X-Pyrycode-Token")
	deviceName := r.Header.Get("X-Pyrycode-Device-Name")
	if serverID == "" || token == "" || deviceName == "" {
		http.Error(w, "missing required header", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		http.Error(w, "server closing", http.StatusServiceUnavailable)
		return
	}
	if _, exists := s.binaries[serverID]; !exists {
		s.mu.Unlock()
		http.Error(w, "no binary online for server-id", http.StatusServiceUnavailable)
		return
	}
	s.mu.Unlock()

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.log.Debug("fakerelay: phone accept failed", "err", err)
		return
	}
	conn.SetReadLimit(maxFrameBytes)

	ctx, cancel := context.WithCancel(r.Context())

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		_ = conn.Close(websocket.StatusNormalClosure, "server closing")
		return
	}
	if _, exists := s.binaries[serverID]; !exists {
		// Binary disconnected between pre-check and accept.
		s.mu.Unlock()
		cancel()
		_ = conn.Close(websocket.StatusNormalClosure, "binary disconnected")
		return
	}
	s.connSeq++
	connID := fmt.Sprintf("c-%d", s.connSeq)
	pc := &phoneConn{
		serverID: serverID,
		connID:   connID,
		conn:     conn,
		sendCh:   make(chan []byte),
		done:     make(chan struct{}),
		cancel:   cancel,
	}
	s.phones[connID] = pc
	s.mu.Unlock()

	s.log.Debug("fakerelay: phone connected", "server_id", serverID, "conn_id", connID)

	s.servePhone(ctx, pc)

	s.mu.Lock()
	delete(s.phones, connID)
	s.mu.Unlock()
	_ = conn.Close(websocket.StatusNormalClosure, "")
	s.log.Debug("fakerelay: phone disconnected", "conn_id", connID)
}

// --- per-conn serve loops ---

func (s *Server) serveBinary(ctx context.Context, bc *binaryConn) {
	errCh := make(chan error, 2)
	go func() { errCh <- s.binaryRecvPump(ctx, bc) }()
	go func() { errCh <- s.binarySendPump(ctx, bc) }()
	<-errCh
	bc.cancel()
	<-errCh
	// done is closed AFTER both pumps return so anyone holding bc and
	// waiting on bc.done knows the sendCh receiver is gone.
	close(bc.done)
}

func (s *Server) servePhone(ctx context.Context, pc *phoneConn) {
	errCh := make(chan error, 2)
	go func() { errCh <- s.phoneRecvPump(ctx, pc) }()
	go func() { errCh <- s.phoneSendPump(ctx, pc) }()
	<-errCh
	pc.cancel()
	<-errCh
	close(pc.done)
}

// binaryRecvPump reads binary→phone frames, unwraps the {conn_id, frame}
// envelope, and forwards frame to the matching phone. Malformed input or
// unknown conn_ids are logged at Debug and skipped — the harness drops
// the offending frame and keeps serving so consumer tests fail on a
// missing receive rather than on a relay-side shutdown that masks the
// cause.
func (s *Server) binaryRecvPump(ctx context.Context, bc *binaryConn) error {
	for {
		_, data, err := bc.conn.Read(ctx)
		if err != nil {
			return err
		}
		var env protocol.RoutingEnvelope
		if err := json.Unmarshal(data, &env); err != nil {
			s.log.Debug("fakerelay: binary sent malformed wrapper",
				"server_id", bc.serverID, "err", err)
			continue
		}
		// Binary-direct envelopes (no conn_id) are the binary↔relay
		// handshake / control plane (hello, ack, error). The routing
		// channel is reserved for binary↔phone traffic, which always
		// carries a conn_id. We dispatch hello → hello_ack here so
		// real pyry binaries can complete their handshake against this
		// harness; other binary-direct types are out of scope until the
		// dispatcher slice lands.
		if env.ConnID == "" {
			if err := s.handleBinaryDirect(ctx, bc, data); err != nil {
				return err
			}
			continue
		}
		s.mu.Lock()
		ph, ok := s.phones[env.ConnID]
		s.mu.Unlock()
		if !ok {
			s.log.Debug("fakerelay: binary referenced unknown conn_id",
				"server_id", bc.serverID, "conn_id", env.ConnID)
			continue
		}
		select {
		case ph.sendCh <- env.Frame:
		case <-ctx.Done():
			return ctx.Err()
		case <-ph.done:
			// Phone went away mid-route; drop the frame.
		}
	}
}

// handleBinaryDirect handles a binary-direct envelope (no conn_id in the
// outer routing wrapper). Today only "hello" is dispatched: capture it
// for test introspection and reply with a wrapped hello_ack. Other types
// are logged at debug and dropped — the dispatcher slice will take over.
func (s *Server) handleBinaryDirect(ctx context.Context, bc *binaryConn, raw []byte) error {
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		s.log.Debug("fakerelay: binary-direct envelope: decode failed",
			"server_id", bc.serverID, "err", err)
		return nil
	}
	if env.Type != protocol.TypeHello {
		s.log.Debug("fakerelay: binary-direct envelope dropped (no dispatcher yet)",
			"server_id", bc.serverID, "type", env.Type)
		return nil
	}

	s.mu.Lock()
	s.lastBinaryHello[bc.serverID] = env
	s.mu.Unlock()

	helloID := env.ID
	ackPayload, err := json.Marshal(protocol.HelloAckPayload{
		ProtocolVersion: "v1",
		ServerID:        bc.serverID,
		ConnID:          "-",
	})
	if err != nil {
		return fmt.Errorf("marshal hello_ack payload: %w", err)
	}
	ack := protocol.Envelope{
		ID:        1,
		Type:      protocol.TypeHelloAck,
		Payload:   ackPayload,
		InReplyTo: &helloID,
	}
	inner, err := json.Marshal(ack)
	if err != nil {
		return fmt.Errorf("marshal hello_ack envelope: %w", err)
	}
	wrapped, err := json.Marshal(protocol.RoutingEnvelope{
		ConnID: "-",
		Frame:  inner,
	})
	if err != nil {
		return fmt.Errorf("wrap hello_ack: %w", err)
	}
	select {
	case bc.sendCh <- wrapped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-bc.done:
		return nil
	}
}

func (s *Server) binarySendPump(ctx context.Context, bc *binaryConn) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case frame := <-bc.sendCh:
			if err := bc.conn.Write(ctx, websocket.MessageText, frame); err != nil {
				return err
			}
		}
	}
}

// phoneRecvPump reads phone→binary frames, wraps each as a routing
// envelope keyed by the assigned conn_id, and forwards to the bound
// binary. The phone is expected to send well-formed JSON (the wrapper
// places `frame` as a json.RawMessage so it survives marshal); a
// non-JSON frame fails the wrap and tears the phone down.
func (s *Server) phoneRecvPump(ctx context.Context, pc *phoneConn) error {
	for {
		_, data, err := pc.conn.Read(ctx)
		if err != nil {
			return err
		}
		if !json.Valid(data) {
			s.log.Debug("fakerelay: phone sent non-JSON frame", "conn_id", pc.connID)
			return fmt.Errorf("phone %s: non-JSON frame", pc.connID)
		}
		out, err := json.Marshal(protocol.RoutingEnvelope{
			ConnID: pc.connID,
			Frame:  json.RawMessage(data),
		})
		if err != nil {
			return fmt.Errorf("wrap frame for %s: %w", pc.connID, err)
		}
		s.mu.Lock()
		bc, ok := s.binaries[pc.serverID]
		s.mu.Unlock()
		if !ok {
			return fmt.Errorf("phone %s: bound binary gone", pc.connID)
		}
		select {
		case bc.sendCh <- out:
		case <-ctx.Done():
			return ctx.Err()
		case <-bc.done:
			return fmt.Errorf("phone %s: binary disconnected mid-route", pc.connID)
		}
	}
}

func (s *Server) phoneSendPump(ctx context.Context, pc *phoneConn) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case frame := <-pc.sendCh:
			if err := pc.conn.Write(ctx, websocket.MessageText, frame); err != nil {
				return err
			}
		}
	}
}

// --- e2e test hooks ---

// RejectNextBinaryWith4409 arms a one-shot mode: the next /v1/server
// upgrade accepts the WS handshake and immediately closes with WS code
// 4409 ("server-id already claimed"). Subsequent connects follow normal
// logic. Used by e2e tests to drive the daemon's "fatal close → daemon
// shuts down" path without racing against the harness's HTTP-409
// first-claim branch.
func (s *Server) RejectNextBinaryWith4409() {
	s.mu.Lock()
	s.rejectNextBinaryWith4409 = true
	s.mu.Unlock()
}

// LastBinaryHello returns the most recent binary-direct "hello" envelope
// observed from the binary that claimed serverID. The boolean is false
// when no hello has been observed for that server-id yet — callers poll
// in e2e tests to wait for the handshake to land.
func (s *Server) LastBinaryHello(serverID string) (protocol.Envelope, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	env, ok := s.lastBinaryHello[serverID]
	return env, ok
}

// ForceCloseBinary closes the WS conn currently bound to serverID with
// websocket.StatusInternalError (1011), simulating a non-fatal relay-side
// drop. Returns true if a binary was bound and the close was issued;
// false if no binary is currently bound to serverID. Used by e2e tests
// to assert that the daemon's claude child survives a non-fatal close
// (transport reconnects, supervisor stays up).
func (s *Server) ForceCloseBinary(serverID string) bool {
	s.mu.Lock()
	bc, ok := s.binaries[serverID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	_ = bc.conn.Close(websocket.StatusInternalError, "test: force close")
	return true
}
