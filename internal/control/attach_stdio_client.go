package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// AttachStdio is the no-PTY counterpart to Attach. It performs the same
// VerbAttach handshake, then bridges in→conn and conn→out as raw bytes —
// no raw mode, no SIGWINCH watcher, no escape-key detection. Intended for
// SDK consumers (stream-json, tooling) that exchange line-delimited
// payloads over their own stdin/stdout pipes and need pyry to be a
// transparent byte conduit.
//
// sessionID resolution rules match Attach (full UUID, unique prefix, or
// "" → bootstrap; resolved server-side via Pool.ResolveID).
//
// EOF on `in` ends the attach cleanly; the session itself stays alive
// (lazy-eviction semantics — detach ≠ destroy). Server-initiated close
// (daemon stop, session evicted from under us) returns nil. Any other
// I/O error propagates.
//
// AttachStdio does not touch any tty. It is safe to call when stdin is a
// pipe, a file, /dev/null, or absent — there is no IsTerminal branch.
//
// ctx scopes the dial only; once the conn is established the attach is
// driven by I/O on `in`/`out` and `conn`. Cancel by closing `in`.
func AttachStdio(ctx context.Context, socketPath, sessionID string, in io.Reader, out io.Writer) error {
	conn, err := dial(ctx, socketPath)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	// Cols=0/Rows=0 ride omitempty off the wire — same byte shape as a
	// v0.5.x client that doesn't know the field. The server's
	// `payload.Cols > 0 && payload.Rows > 0` guard skips the resize seam.
	if err := json.NewEncoder(conn).Encode(Request{
		Verb:   VerbAttach,
		Attach: &AttachPayload{SessionID: sessionID},
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

	// Output goroutine: server → caller's `out`. Joined via `done` before
	// AttachStdio returns — the caller gets a deterministic "all server
	// bytes flushed to out" guarantee, useful for SDK consumers reading
	// stdout until the spawned process closes the pipe.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(out, conn)
	}()

	// Input loop: caller's `in` → server. Returns when `in` EOFs (clean
	// detach) or a write to `conn` fails (server hung up → coerced to nil
	// via writerErr; other errors propagate).
	_, copyErr := io.Copy(conn, in)

	// Closing the conn unblocks the output goroutine's Read so the join
	// below completes. The deferred conn.Close above also fires, but we
	// need to wake the goroutine *before* we wait on `done`.
	_ = conn.Close()
	<-done

	return writerErr(copyErr)
}
