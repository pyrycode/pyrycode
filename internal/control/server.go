package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// StateProvider is the supervisor view the control server depends on. Defining
// it here (where it is consumed) keeps the supervisor package free of
// control-plane concerns and makes the server trivial to test with a fake.
type StateProvider interface {
	State() supervisor.State
}

// AttachProvider is the supervisor view the control server depends on for
// VerbAttach. Implementations bind a client's input/output streams to the
// supervised PTY for the lifetime of the attachment. Returns the done
// channel that fires when the input source ends (client disconnected),
// or an error if the attach can't proceed (e.g. another client already
// attached).
type AttachProvider interface {
	Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error)
}

// Server listens on a Unix domain socket and answers control requests.
//
// Lifecycle: NewServer → Listen → Serve(ctx) → Close. Listen creates the
// socket file (and any missing parent directory) and returns synchronously,
// so callers can fail fast if the path is unusable. Serve blocks until ctx
// is cancelled or the listener is closed. Close is safe to call multiple
// times and removes the socket file.
type Server struct {
	socketPath string
	state      StateProvider
	logs       LogProvider
	attach     AttachProvider
	shutdown   func()
	log        *slog.Logger

	mu       sync.Mutex
	listener net.Listener
	closed   bool
	closedCh chan struct{} // closed by Close, lets Serve's ctx-watcher exit

	// streamingWG tracks streaming-handler goroutines (currently: the
	// per-attach detach watcher). Serve waits on it before returning so a
	// caller blocked on Serve can be sure no per-conn goroutines are left.
	streamingWG sync.WaitGroup
}

// NewServer constructs a Server. The socket is not opened until Listen.
//
// state must be non-nil — the supervisor is the canonical source of state,
// and VerbStatus would otherwise nil-pointer-panic on the first request.
// Passing a nil state is a programmer error and panics at construction
// time so the failure surfaces immediately rather than on a request from
// a future shell.
//
// logs is optional. When nil, VerbLogs returns an error response.
//
// attach is optional. When nil, VerbAttach returns an error response — the
// daemon is in foreground mode and the supervised child is already bound
// to a local terminal.
//
// shutdown is optional. When nil, VerbStop returns an error response. When
// set, a successful VerbStop invokes it after acknowledging the client —
// typically the signal-driven context's cancel function so a stop request
// walks the same shutdown path as SIGINT/SIGTERM.
func NewServer(socketPath string, state StateProvider, logs LogProvider, attach AttachProvider, shutdown func(), log *slog.Logger) *Server {
	if state == nil {
		panic("control.NewServer: state is required, got nil")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		socketPath: socketPath,
		state:      state,
		logs:       logs,
		attach:     attach,
		shutdown:   shutdown,
		log:        log,
		closedCh:   make(chan struct{}),
	}
}

// SocketPath returns the configured socket path.
func (s *Server) SocketPath() string {
	return s.socketPath
}

// Listen creates the socket file. It is split from Serve so callers can
// surface "socket already in use" or "permission denied" errors before
// starting the supervisor proper.
//
// Stale sockets from a prior crash are removed transparently — net.Listen
// would otherwise fail with "address already in use" since unix sockets
// don't auto-clean.
func (s *Server) Listen() error {
	if dir := filepath.Dir(s.socketPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create socket dir: %w", err)
		}
	}

	// Best-effort cleanup of a stale socket. We deliberately don't check
	// whether something is listening on the path — if a previous pyry
	// crashed without removing it, the file is dead. If a different live
	// pyry is using it, net.Listen below will fail and the user can
	// figure out which.
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", s.socketPath, err)
	}

	// Single-user permissions — only the owner can talk to the daemon.
	// This 0600 chmod is currently the only authentication boundary on the
	// control socket. Any process running as the owning user can connect,
	// send VerbStop, and shut the daemon down. Acceptable for Phase 0
	// (single-user dev/service deployment); revisit before exposing the
	// socket across user boundaries (containers, multi-tenant hosts).
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(s.socketPath)
		return fmt.Errorf("chmod socket: %w", err)
	}

	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()
	return nil
}

// Serve accepts connections and dispatches requests until ctx is cancelled.
// Listen must be called first.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	ln := s.listener
	s.mu.Unlock()
	if ln == nil {
		return errors.New("control: Listen must be called before Serve")
	}

	// Closing the listener unblocks Accept. We wire it to BOTH ctx
	// cancellation and explicit Close so direct callers of Close (without
	// cancelling ctx) don't leave the watcher goroutine alive forever.
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.closedCh:
			// Close already fired; nothing to do.
		}
	}()

	var handleWG sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			// If we're shutting down, this is expected.
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				handleWG.Wait()        // wait for in-flight handlers
				s.streamingWG.Wait()   // wait for active attach detach-watchers
				return nil
			}
			s.log.Warn("control: accept failed", "err", err)
			continue
		}
		handleWG.Add(1)
		go func() {
			defer handleWG.Done()
			s.handle(conn)
		}()
	}
}

// Close shuts the listener and removes the socket file. Idempotent. Safe to
// call from any goroutine, including the ctx-watcher goroutine in Serve.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.closedCh) // wakes Serve's ctx-watcher goroutine
	var firstErr error
	if s.listener != nil {
		if err := s.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			firstErr = err
		}
	}
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// handshakeTimeout caps how long the server waits for a client to send its
// JSON request after connecting. Cleared for streaming verbs (VerbAttach)
// before the ack, since they hold the connection open indefinitely.
const handshakeTimeout = 5 * time.Second

// handle dispatches a single client connection. One-shot verbs reply with one
// JSON Response and close. Streaming verbs (currently just VerbAttach) hand
// off connection ownership to a streaming handler; the deferred close in
// handle is suppressed for them.
//
// TODO: a misbehaving client could open a connection, write a partial JSON
// payload, and hold it. The handshake deadline + per-conn goroutine model
// bounds the damage to ~handshakeTimeout × N concurrent slow clients. With
// the 0600 socket perms the realistic N is "other processes the same user
// runs", which is fine for Phase 0. Revisit if the socket is ever exposed
// beyond that boundary.
func (s *Server) handle(conn net.Conn) {
	closeConn := true
	defer func() {
		if closeConn {
			_ = conn.Close()
		}
	}()
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	var req Request
	if err := dec.Decode(&req); err != nil {
		_ = enc.Encode(Response{Error: fmt.Sprintf("decode request: %v", err)})
		return
	}

	switch req.Verb {
	case VerbStatus:
		_ = enc.Encode(Response{Status: buildStatus(s.state.State())})
	case VerbLogs:
		s.handleLogs(enc)
	case VerbStop:
		s.handleStop(enc)
	case VerbAttach:
		// Streaming verb. handleAttach takes ownership of conn on
		// success and is responsible for closing it.
		if s.handleAttach(conn, enc) {
			closeConn = false
		}
	default:
		_ = enc.Encode(Response{Error: fmt.Sprintf("unknown verb: %q", req.Verb)})
	}
}

// handleLogs serves a VerbLogs request: snapshot the ring buffer, write the
// payload, return.
func (s *Server) handleLogs(enc *json.Encoder) {
	if s.logs == nil {
		_ = enc.Encode(Response{Error: "logs: no log provider configured"})
		return
	}
	_ = enc.Encode(Response{Logs: &LogsPayload{
		Lines:    s.logs.Snapshot(),
		Capacity: s.logs.Cap(),
	}})
}

// handleStop serves a VerbStop request: ack, then invoke the configured
// shutdown callback. The ack is written before shutdown so the client reads
// confirmation even if the listener closes immediately.
func (s *Server) handleStop(enc *json.Encoder) {
	if s.shutdown == nil {
		_ = enc.Encode(Response{Error: "stop: no shutdown handler configured"})
		return
	}
	_ = enc.Encode(Response{OK: true})
	s.log.Info("control: stop requested")
	s.shutdown()
}

// handleAttach serves a VerbAttach request. Returns true iff connection
// ownership has been transferred to the streaming bridge — in which case the
// caller MUST NOT close conn (a goroutine spawned here handles that when the
// attach ends). Returns false on any pre-attach failure (no provider, bridge
// busy, etc.); in those cases the caller continues to own conn and will
// close it normally.
func (s *Server) handleAttach(conn net.Conn, enc *json.Encoder) (handedOff bool) {
	if s.attach == nil {
		_ = enc.Encode(Response{Error: "attach: no attach provider configured (daemon may be in foreground mode)"})
		return false
	}
	// Clear the handshake deadline BEFORE registering the bridge or writing
	// the ack. Once attach starts, both directions are streaming — the
	// bridge's input goroutine reads from conn until EOF, and the supervisor's
	// PTY output goroutine writes to conn at unpredictable times. A handshake
	// deadline still on the conn would mistakenly terminate either side after
	// a quiet window. A successful attach should keep the conn alive
	// indefinitely.
	_ = conn.SetDeadline(time.Time{})

	done, err := s.attach.Attach(conn, conn)
	if err != nil {
		_ = enc.Encode(Response{Error: fmt.Sprintf("attach: %v", err)})
		return false
	}
	s.log.Info("control: client attached")
	_ = enc.Encode(Response{OK: true})

	// Connection ownership transferred. Close it when the bridge's input
	// pump ends (typically: client disconnected). Tracked on streamingWG
	// so Serve waits for it before returning.
	s.streamingWG.Add(1)
	go func() {
		defer s.streamingWG.Done()
		<-done
		_ = conn.Close()
		s.log.Info("control: client detached")
	}()
	return true
}

// buildStatus converts a supervisor.State snapshot into the wire format.
func buildStatus(st supervisor.State) *StatusPayload {
	p := &StatusPayload{
		Phase:        string(st.Phase),
		ChildPID:     st.ChildPID,
		StartedAt:    st.StartedAt.UTC().Format(time.RFC3339),
		Uptime:       time.Since(st.StartedAt).Round(time.Second).String(),
		RestartCount: st.RestartCount,
	}
	if st.LastUptime > 0 {
		p.LastUptime = st.LastUptime.Round(time.Millisecond).String()
	}
	if st.NextBackoff > 0 {
		p.NextBackoff = st.NextBackoff.Round(time.Millisecond).String()
	}
	return p
}
