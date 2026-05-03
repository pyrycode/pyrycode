package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// DetachKey is the byte that, when typed after EscapeKey, ends an attachment.
// Pattern: Ctrl-B (0x02) followed by 'd' — same convention as tmux.
const (
	EscapeKey byte = 0x02 // Ctrl-B
	DetachKey byte = 'd'
)

// Attach connects to a running pyry daemon's control socket, performs the
// JSON handshake, and bridges the local terminal to the PTY.
//
// Cols/rows are sent in the handshake so the supervised child knows the
// initial window size. Subsequent live resize events are forwarded as
// VerbResize requests on fresh control connections (see SendResize) by an
// internal SIGWINCH watcher; the supervised child receives the new
// dimensions via the daemon's Bridge.Resize seam.
//
// sessionID selects which session the server should attach to: a full UUID,
// a unique prefix, or empty to mean "the bootstrap session". The string is
// passed through verbatim; resolution rules live in the server's
// Pool.ResolveID.
//
// Local stdin is put into raw mode for the duration of the attach so
// keystrokes pass through unmodified to the child. The terminal is restored
// when Attach returns.
//
// The attach ends when:
//   - The user types EscapeKey + DetachKey (Ctrl-B d): clean detach.
//   - The server closes the connection: returns nil.
//   - The local stdin or socket errors: returns the error.
func Attach(ctx context.Context, socketPath string, cols, rows int, sessionID string) error {
	conn, err := dial(ctx, socketPath)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	if err := json.NewEncoder(conn).Encode(Request{
		Verb:   VerbAttach,
		Attach: &AttachPayload{Cols: cols, Rows: rows, SessionID: sessionID},
	}); err != nil {
		return fmt.Errorf("send handshake: %w", err)
	}

	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return fmt.Errorf("read ack: %w", err)
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	if !resp.OK {
		return errors.New("control: attach ack missing")
	}

	// Live resize forwarding. Installed only after the handshake has
	// succeeded (so resize messages can't reach the server before any session
	// is bound) and torn down before Attach returns (synchronous — see
	// startWinsizeWatcher). The deferred conn.Close() above runs after
	// stopWinsize because defer order is LIFO: stop the SIGWINCH watcher
	// first, then let conn.Close() unblock the output copy goroutine.
	stopWinsize := startWinsizeWatcher(ctx, readTerminalSize, func(ctx context.Context, cols, rows int) error {
		return SendResize(ctx, socketPath, sessionID, cols, rows)
	})
	defer stopWinsize()

	// Connection is now in raw-bytes mode. Bridge to local terminal.
	stdinFd := int(os.Stdin.Fd())
	if term.IsTerminal(stdinFd) {
		oldState, err := term.MakeRaw(stdinFd)
		if err == nil {
			defer func() { _ = term.Restore(stdinFd, oldState) }()
		}
	}

	// Fire and forget the output side: PTY → local stdout. When conn closes
	// (server hangup or our own detach), io.Copy returns; we don't care
	// about the error path.
	go func() { _, _ = io.Copy(os.Stdout, conn) }()

	// Input side with escape detection. Returns nil on clean detach,
	// the underlying error otherwise.
	return copyWithEscape(conn, os.Stdin)
}

// copyWithEscape forwards bytes from src to dst until it sees the
// EscapeKey + DetachKey sequence, at which point it returns nil. Any other
// byte after EscapeKey is treated as a false alarm — both bytes are flushed
// and the state machine resets.
//
// Reads one byte at a time. Slow in absolute terms but correct, and the
// throughput ceiling is human typing speed anyway.
func copyWithEscape(dst io.Writer, src io.Reader) error {
	buf := make([]byte, 1)
	pending := false
	for {
		n, err := src.Read(buf)
		if n == 0 {
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			continue
		}
		b := buf[0]

		if pending {
			pending = false
			if b == DetachKey {
				return nil // clean detach
			}
			// False alarm — flush both bytes, ignore writer hangup.
			if _, werr := dst.Write([]byte{EscapeKey, b}); werr != nil {
				return writerErr(werr)
			}
			continue
		}

		if b == EscapeKey {
			pending = true
			continue
		}

		if _, werr := dst.Write([]byte{b}); werr != nil {
			return writerErr(werr)
		}
	}
}

// writerErr coerces "the server hung up" into nil so an attach that ends
// because the daemon stopped is not surfaced as an error. Any other write
// error (e.g. a crashed socket library) propagates.
func writerErr(err error) error {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
		return nil
	}
	return err
}

// terminalSizeReader reports the current TTY geometry (cols, rows). The
// bool is false when no terminal is attached or when the ioctl fails — in
// that case the watcher emits no resize, mirroring the daemon-side
// resizeOnce guard in internal/supervisor/winsize.go.
type terminalSizeReader func() (cols, rows int, ok bool)

// readTerminalSize is the production reader. It uses os.Stdin directly
// rather than wrapping a raw fd in a fresh *os.File — see
// supervisor/winsize.go:40-48 for the finalizer-induced fd reuse race that
// motivated the same convention there.
func readTerminalSize() (cols, rows int, ok bool) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return 0, 0, false
	}
	size, err := pty.GetsizeFull(os.Stdin)
	if err != nil {
		return 0, 0, false
	}
	return int(size.Cols), int(size.Rows), true
}

// startWinsizeWatcher installs a SIGWINCH handler. On each signal it reads
// the terminal size and emits a VerbResize via send. Returns a stop
// function that:
//
//  1. Calls signal.Stop on the SIGWINCH channel so further signals are
//     no-ops to this handler.
//  2. Closes done so the watcher goroutine breaks out of its select.
//  3. Blocks until the watcher goroutine has actually exited.
//
// Step 3 is the load-bearing guarantee: stop is synchronous. No goroutine
// or signal subscription outlives the call site's defer.
//
// The watcher does NOT prime an initial size at startup — initial geometry
// flows through the handshake AttachPayload.
//
// SendResize already honors ctx and bounds itself by DialTimeout, so a
// misbehaving daemon cannot hang detach past that bound.
func startWinsizeWatcher(
	ctx context.Context,
	read terminalSizeReader,
	send func(ctx context.Context, cols, rows int) error,
) (stop func()) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	done := make(chan struct{})
	gone := make(chan struct{})

	go func() {
		defer close(gone)
		for {
			select {
			case <-sigCh:
				cols, rows, ok := read()
				if !ok {
					continue
				}
				// Best-effort. SendResize errors (transient daemon
				// hiccup, ctx cancelled mid-flight) are silently
				// dropped; the next SIGWINCH retries. This matches
				// the server-side posture (handleResize returns OK
				// even on seam errors) and SendResize's own godoc.
				_ = send(ctx, cols, rows)
			case <-done:
				return
			}
		}
	}()

	return func() {
		signal.Stop(sigCh)
		close(done)
		<-gone
	}
}
