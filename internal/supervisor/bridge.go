package supervisor

import (
	"errors"
	"io"
	"log/slog"
	"sync"
)

// ErrBridgeBusy is returned by Bridge.Attach when another client is already
// attached.
var ErrBridgeBusy = errors.New("supervisor: bridge already has an attached client")

// Bridge mediates I/O between the PTY and a (possibly absent) external
// endpoint. It is the seam that lets the supervisor run in service mode —
// the PTY master lives in the supervisor, and an attaching client (e.g.
// `pyry attach`) can take over input/output on demand.
//
// Lifetime: a single Bridge persists across child restarts. It implements
// io.ReadWriter so the supervisor can use it as a transparent stand-in for
// stdin/stdout. Writes (PTY → bridge) forward to the attached output writer
// or get discarded when no client is attached. Reads (bridge → PTY) block
// until a client attaches and starts feeding bytes through.
//
// At most one attacher at a time. A second Attach call while another client
// is already attached returns ErrBridgeBusy.
type Bridge struct {
	pipeR *io.PipeReader
	pipeW *io.PipeWriter
	log   *slog.Logger

	mu       sync.Mutex
	output   io.Writer // attached client output, or nil = discard
	attached bool
}

// NewBridge constructs an empty bridge. No output is attached yet; PTY writes
// are discarded until the first Attach. log, if nil, falls back to
// slog.Default — used to surface non-EOF input-pump errors so an
// abnormally-closed client doesn't disappear silently.
func NewBridge(log *slog.Logger) *Bridge {
	if log == nil {
		log = slog.Default()
	}
	r, w := io.Pipe()
	return &Bridge{pipeR: r, pipeW: w, log: log}
}

// Read implements io.Reader. The supervisor copies from this into the PTY
// master. Blocks while no client is attached.
func (b *Bridge) Read(p []byte) (int, error) {
	return b.pipeR.Read(p)
}

// Write implements io.Writer. The supervisor copies from the PTY master into
// this. Bytes are forwarded to the attached output writer, or discarded if
// none is attached.
//
// Write NEVER returns an error. This is load-bearing: the supervisor's
// io.Copy(bridge, ptmx) goroutine must keep draining the PTY for the entire
// child lifetime — if it returns, the PTY's master read buffer fills, the
// slave's writes block, and the child wedges. So even when the attached
// client's conn is mid-disconnect (closed faster than the bridge's input
// pump cleared b.output to nil), we silently drop the bytes and report
// success rather than letting the conn error escape to the supervisor.
//
// The discard-on-detach is a minor visible cost — a few bytes of claude
// output that would have shown on the now-departed client get lost. The
// alternative (a wedged daemon that needs SIGKILL to recover) is much worse.
func (b *Bridge) Write(p []byte) (int, error) {
	b.mu.Lock()
	out := b.output
	b.mu.Unlock()
	if out == nil {
		return len(p), nil
	}
	if _, err := out.Write(p); err != nil {
		// See doc comment: bytes lost, daemon stays alive.
		return len(p), nil
	}
	return len(p), nil
}

// Attach binds a client to the bridge: bytes from `in` flow toward the PTY,
// PTY output flows toward `out`. Returns a `done` channel that closes when
// `in` returns EOF or an error (typically: client disconnected). Returns
// ErrBridgeBusy if another client is currently attached.
//
// Exactly one of (returns done, returns err) is non-nil.
//
// The bridge does NOT close the in/out streams — the caller owns their
// lifecycle and is responsible for closing them after `done` fires.
func (b *Bridge) Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error) {
	b.mu.Lock()
	if b.attached {
		b.mu.Unlock()
		return nil, ErrBridgeBusy
	}
	b.attached = true
	b.output = out
	b.mu.Unlock()

	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		// EOF on `in` is the normal detach path (client closed the conn).
		// Anything else (a network reset, an unexpected I/O error) is
		// noteworthy — log it so a vanishing attach doesn't leave operators
		// guessing.
		if _, err := io.Copy(b.pipeW, in); err != nil && !errors.Is(err, io.EOF) {
			b.log.Warn("supervisor: attach input copy ended with error", "err", err)
		}

		b.mu.Lock()
		// Only clear output if we're still the attached client. (Defensive:
		// in the at-most-one model nobody else should have replaced us, but
		// future relaxations might.)
		if b.output == out {
			b.output = nil
		}
		b.attached = false
		b.mu.Unlock()
	}()
	return doneCh, nil
}

// Attached reports whether a client is currently attached.
func (b *Bridge) Attached() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.attached
}
