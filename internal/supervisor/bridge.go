package supervisor

import (
	"errors"
	"io"
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

	mu       sync.Mutex
	output   io.Writer // attached client output, or nil = discard
	attached bool
}

// NewBridge constructs an empty bridge. No output is attached yet; PTY writes
// are discarded until the first Attach.
func NewBridge() *Bridge {
	r, w := io.Pipe()
	return &Bridge{pipeR: r, pipeW: w}
}

// Read implements io.Reader. The supervisor copies from this into the PTY
// master. Blocks while no client is attached.
func (b *Bridge) Read(p []byte) (int, error) {
	return b.pipeR.Read(p)
}

// Write implements io.Writer. The supervisor copies from the PTY master into
// this. Bytes are forwarded to the attached output writer, or discarded if
// none is attached. Discard always reports a successful write so io.Copy
// keeps draining the PTY (otherwise the child would block on a full stdout
// buffer).
func (b *Bridge) Write(p []byte) (int, error) {
	b.mu.Lock()
	out := b.output
	b.mu.Unlock()
	if out == nil {
		return len(p), nil
	}
	return out.Write(p)
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
		_, _ = io.Copy(b.pipeW, in)

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
