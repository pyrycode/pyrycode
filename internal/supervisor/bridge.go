package supervisor

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/creack/pty"
)

// ErrBridgeBusy is returned by Bridge.Attach when another client is already
// attached.
var ErrBridgeBusy = errors.New("supervisor: bridge already has an attached client")

// inputChunkBufferSize is the high-water mark for buffered input chunks
// between the attached client and the supervisor's input pump. Sized
// generously so a brief stall in the input pump (e.g. during a child
// restart) does not push back on the attach client. Each chunk is up
// to attachReadBufferSize bytes.
const inputChunkBufferSize = 64

// attachReadBufferSize is the buffer the input pump uses to read chunks
// from the attach client before forwarding them to the bridge.
const attachReadBufferSize = 4096

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
// Iteration boundaries: each runOnce iteration calls BeginIteration before
// launching the input pump and EndIteration after the child exits. Read
// returns io.EOF when the iteration ends, allowing the input pump to exit
// cleanly without consuming buffered bytes intended for the next iteration.
// Bytes already in flight on Read are preserved by Go's select semantics —
// an unselected channel receive does not remove the value from the channel.
//
// At most one attacher at a time. A second Attach call while another client
// is already attached returns ErrBridgeBusy.
type Bridge struct {
	log *slog.Logger

	// Input path (attach client → bridge → supervisor → PTY).
	in       chan []byte
	leftover []byte // bytes from a partial Read; drained before next channel recv
	leftMu   sync.Mutex

	cancelMu   sync.Mutex
	iterCancel chan struct{} // closed by EndIteration to make Read return EOF

	// Output path (PTY → bridge → attach client).
	mu       sync.Mutex
	output   io.Writer // attached client output, or nil = discard
	attached bool

	// ptyMu guards ptmx. Held briefly across pty.Setsize so a concurrent
	// SetPTY/ClearPTY can't swap the file mid-call. Leaf-only — never
	// acquired while holding mu, cancelMu, or leftMu.
	ptyMu sync.Mutex
	ptmx  *os.File // current PTY master, or nil between iterations
}

// NewBridge constructs an empty bridge. No output is attached yet; PTY writes
// are discarded until the first Attach. log, if nil, falls back to
// slog.Default — used to surface non-EOF input-pump errors so an
// abnormally-closed client doesn't disappear silently.
func NewBridge(log *slog.Logger) *Bridge {
	if log == nil {
		log = slog.Default()
	}
	return &Bridge{
		log:        log,
		in:         make(chan []byte, inputChunkBufferSize),
		iterCancel: make(chan struct{}),
	}
}

// Read implements io.Reader. The supervisor copies from this into the PTY
// master. Blocks until a buffered chunk is available or EndIteration fires.
//
// Returns io.EOF when the current iteration's cancel signal is closed AND
// no buffered bytes remain. Critically, when cancel fires concurrently with
// a chunk arriving, Go's select non-determinism means we may return either
// the chunk (good) or EOF (also good — the chunk stays in the channel for
// the next iteration's Read to consume). Either way, no bytes are lost.
func (b *Bridge) Read(p []byte) (int, error) {
	b.leftMu.Lock()
	if len(b.leftover) > 0 {
		n := copy(p, b.leftover)
		b.leftover = b.leftover[n:]
		b.leftMu.Unlock()
		return n, nil
	}
	b.leftMu.Unlock()

	b.cancelMu.Lock()
	cancel := b.iterCancel
	b.cancelMu.Unlock()

	select {
	case chunk := <-b.in:
		n := copy(p, chunk)
		if n < len(chunk) {
			b.leftMu.Lock()
			b.leftover = append(b.leftover, chunk[n:]...)
			b.leftMu.Unlock()
		}
		return n, nil
	case <-cancel:
		return 0, io.EOF
	}
}

// BeginIteration prepares the bridge for a new ptmx iteration. Resets the
// per-iteration cancel signal so subsequent Reads block on input again.
// Idempotent if called repeatedly without EndIteration in between, but
// runOnce pairs them.
func (b *Bridge) BeginIteration() {
	b.cancelMu.Lock()
	select {
	case <-b.iterCancel:
		// Previous iteration ended; create a fresh cancel.
		b.iterCancel = make(chan struct{})
	default:
		// Cancel is still open; nothing to do.
	}
	b.cancelMu.Unlock()
}

// EndIteration signals the current Read to return io.EOF. Buffered chunks
// in the input channel are preserved for the next iteration.
func (b *Bridge) EndIteration() {
	b.cancelMu.Lock()
	select {
	case <-b.iterCancel:
		// Already closed; nothing to do.
	default:
		close(b.iterCancel)
	}
	b.cancelMu.Unlock()
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
		buf := make([]byte, attachReadBufferSize)
		for {
			n, rerr := in.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				b.in <- chunk
			}
			if rerr != nil {
				// EOF on `in` is the normal detach path (client closed
				// the conn). Anything else is noteworthy — log it so a
				// vanishing attach doesn't leave operators guessing.
				if !errors.Is(rerr, io.EOF) {
					b.log.Warn("supervisor: attach input copy ended with error", "err", rerr)
				}
				break
			}
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

// SetPTY registers (or clears, when f is nil) the PTY master for the current
// runOnce iteration. Subsequent Resize calls target this fd. runOnce calls
// SetPTY(ptmx) after pty.Start succeeds and SetPTY(nil) before EndIteration
// so a Resize racing with iteration teardown sees nil rather than a closed fd.
func (b *Bridge) SetPTY(f *os.File) {
	b.ptyMu.Lock()
	b.ptmx = f
	b.ptyMu.Unlock()
}

// Resize applies the given window size to the registered PTY master via
// pty.Setsize. Returns nil silently when no PTY is registered (between
// iterations, or in foreground mode where no Bridge exists at all). Errors
// from pty.Setsize are wrapped and returned for the caller to log; the
// control plane does not fail the attach on resize errors.
//
// rows-then-cols matches pty.Winsize field order. The wire protocol uses
// cols-then-rows in AttachPayload; the boundary swap happens at the
// handleAttach call site.
func (b *Bridge) Resize(rows, cols uint16) error {
	b.ptyMu.Lock()
	defer b.ptyMu.Unlock()
	if b.ptmx == nil {
		return nil
	}
	if err := pty.Setsize(b.ptmx, &pty.Winsize{Rows: rows, Cols: cols}); err != nil {
		return fmt.Errorf("pty setsize: %w", err)
	}
	return nil
}
