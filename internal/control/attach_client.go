package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

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
// initial window size. Subsequent live resize events are not propagated in
// Phase 0 — detach and reattach to update.
//
// Local stdin is put into raw mode for the duration of the attach so
// keystrokes pass through unmodified to the child. The terminal is restored
// when Attach returns.
//
// The attach ends when:
//   - The user types EscapeKey + DetachKey (Ctrl-B d): clean detach.
//   - The server closes the connection: returns nil.
//   - The local stdin or socket errors: returns the error.
func Attach(ctx context.Context, socketPath string, cols, rows int) error {
	conn, err := dial(ctx, socketPath)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	if err := json.NewEncoder(conn).Encode(Request{
		Verb:   VerbAttach,
		Attach: &AttachPayload{Cols: cols, Rows: rows},
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
