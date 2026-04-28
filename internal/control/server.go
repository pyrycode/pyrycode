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
}

// NewServer constructs a Server. The socket is not opened until Listen.
//
// state must be non-nil — the supervisor is the canonical source of state.
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

	// Closing the listener unblocks Accept. We wire it to ctx so callers
	// can stop the server simply by cancelling the context.
	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()

	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			// If we're shutting down, this is expected.
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				wg.Wait()
				return nil
			}
			s.log.Warn("control: accept failed", "err", err)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handle(conn)
		}()
	}
}

// Close shuts the listener and removes the socket file. Idempotent.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
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

// handle services a single client connection: decode one Request, write
// one Response (and possibly stream raw bytes for VerbAttach), close.
//
// VerbAttach is the only verb that holds the connection open beyond a single
// JSON ack. After the ack, the connection is "upgraded" — both sides switch
// to raw bytes flowing between the client's terminal and the PTY. The
// connection closes when either side disconnects.
func (s *Server) handle(conn net.Conn) {
	closeConn := true
	defer func() {
		if closeConn {
			_ = conn.Close()
		}
	}()
	// Short deadline for the JSON handshake. Cleared before raw-byte streaming
	// so the attach connection can stay open indefinitely.
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

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
		if s.logs == nil {
			_ = enc.Encode(Response{Error: "logs: no log provider configured"})
			return
		}
		lines := s.logs.Snapshot()
		capacity := 0
		if r, ok := s.logs.(*RingBuffer); ok {
			capacity = r.Cap()
		}
		_ = enc.Encode(Response{Logs: &LogsPayload{Lines: lines, Capacity: capacity}})
	case VerbStop:
		if s.shutdown == nil {
			_ = enc.Encode(Response{Error: "stop: no shutdown handler configured"})
			return
		}
		// Acknowledge first, then trigger shutdown. The Response is in
		// the kernel's socket buffer by the time shutdown() returns, so
		// even if the listener closes immediately the client still reads
		// its OK.
		_ = enc.Encode(Response{OK: true})
		s.log.Info("control: stop requested")
		s.shutdown()
	case VerbAttach:
		if s.attach == nil {
			_ = enc.Encode(Response{Error: "attach: no attach provider configured (daemon may be in foreground mode)"})
			return
		}
		// Bridge bytes for the lifetime of the attachment. The Response
		// ack ({OK:true}) tells the client to switch to raw-byte mode.
		// After that, conn → PTY input, PTY output → conn.
		done, err := s.attach.Attach(conn, conn)
		if err != nil {
			_ = enc.Encode(Response{Error: fmt.Sprintf("attach: %v", err)})
			return
		}
		s.log.Info("control: client attached")
		_ = enc.Encode(Response{OK: true})

		// Clear the handshake deadline — raw-byte streaming has no a priori
		// upper bound on how long a session lasts.
		_ = conn.SetDeadline(time.Time{})

		// Hand off the connection: don't close it from this goroutine. The
		// AttachProvider's `done` fires when the client's input ends; at
		// that point we close the conn to release any blocked PTY → conn
		// writes.
		closeConn = false
		go func() {
			<-done
			_ = conn.Close()
			s.log.Info("control: client detached")
		}()
	default:
		_ = enc.Encode(Response{Error: fmt.Sprintf("unknown verb: %q", req.Verb)})
	}
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
