package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	log        *slog.Logger

	mu       sync.Mutex
	listener net.Listener
	closed   bool
}

// NewServer constructs a Server. The socket is not opened until Listen.
func NewServer(socketPath string, state StateProvider, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		socketPath: socketPath,
		state:      state,
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

// handle services a single client connection: decode one Request, write one
// Response, close. A short read deadline keeps a misbehaving client from
// pinning a goroutine indefinitely.
func (s *Server) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
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
