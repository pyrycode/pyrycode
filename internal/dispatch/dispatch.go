// Package dispatch demultiplexes inbound binary↔relay frames by
// RoutingEnvelope.ConnID and routes each frame through a handler table.
// The package is carrier-agnostic: it takes a generic
// <-chan protocol.RoutingEnvelope as its input and exposes its outbound
// frames on a channel the caller forwards to the relay. Wiring lives in
// cmd/pyry; this package imports internal/protocol only.
//
// The handler table is empty until downstream verb slices register
// routes. Frames whose Envelope.Type has no registered handler fall
// through to a protocol.unsupported error reply; encrypted or otherwise
// non-v1 frames are refused via the protocol.IsV1Compatible check and
// map to protocol.unsupported / protocol.unknown_type. Malformed inner
// frames map to protocol.malformed.
//
// Concurrency model: Run is a single demux goroutine that fans frames
// out to one goroutine per active conn_id. Per-conn goroutines read
// their inputs serially (preserving arrival order within a conn) and
// publish replies on a shared outbound channel that the caller drains.
// Bounded backpressure: a slow outbound consumer pauses per-conn
// goroutines, which is the intended flow control.
//
// Security / operational notes (per the spec's Security review, #307):
//
//   - Inbound frame size cap is inherited from internal/transport's WS
//     read path. The dispatcher does not re-enforce; verb slices likewise
//     rely on the transport cap rather than per-handler limits.
//   - Head-of-line blocking on the demux: with an empty handler table no
//     handler runs, so a slow handler cannot stall the demux today. Once
//     verb slices register a long-running handler (e.g. an LLM-touching
//     route), revisit and consider per-conn goroutine offload.
//   - Log policy: dispatcher diagnostics carry conn_id, envelope type,
//     envelope id, and the decode-error class — never the raw frame
//     payload. Verb slices crossing this code path (message bodies,
//     push tokens) must keep the same posture.
//   - Wire-side error envelopes carry only the Code* string plus a
//     static descriptive Message. No decode-error text, stack info, or
//     anything derived from untrusted input is echoed back.
package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Handler processes a single inbound envelope on a phone conn. Returning
// a non-nil error is logged at WARN and otherwise ignored in v1 (no
// close-conn semantics yet — the auth-gate slice (#308) introduces that
// surface). Handlers reply by calling Conn.Reply or Conn.Send; the
// dispatcher does not synthesize a response from a handler return value.
type Handler func(ctx context.Context, c *Conn, env protocol.Envelope) error

// Conn is the per-conn_id state exposed to handlers. It owns the
// monotonic outbound id counter and the conn's outbound send seam.
type Conn struct {
	id       string
	nextID   atomic.Uint64
	outbound chan<- protocol.RoutingEnvelope
}

// ConnID returns the relay-assigned conn_id this Conn dispatches for.
func (c *Conn) ConnID() string { return c.id }

// NextID returns the next monotonic outbound envelope id for this conn.
// Starts at 1 on the first call. Concurrent-safe even though the per-conn
// goroutine is the only writer today — atomic is cheap insurance for
// future fan-out inside a handler.
func (c *Conn) NextID() uint64 { return c.nextID.Add(1) }

// Send wraps env in a RoutingEnvelope addressed to this conn and pushes
// it onto the dispatcher's outbound channel. Blocks on backpressure;
// returns ctx.Err if ctx is cancelled while blocked. Caller is
// responsible for env.ID and env.TS — use Reply for the
// request/response convenience path.
func (c *Conn) Send(ctx context.Context, env protocol.Envelope) error {
	frame, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	routing := protocol.RoutingEnvelope{ConnID: c.id, Frame: frame}
	select {
	case c.outbound <- routing:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Reply builds a response envelope keyed by NextID with InReplyTo set to
// req.ID and TS set to time.Now().UTC(), then Send's it. This is the
// load-bearing helper for the per-conn "in_reply_to matches request id"
// invariant.
func (c *Conn) Reply(ctx context.Context, req protocol.Envelope, respType string, payload json.RawMessage) error {
	reqID := req.ID
	env := protocol.Envelope{
		ID:        c.NextID(),
		Type:      respType,
		TS:        time.Now().UTC(),
		Payload:   payload,
		InReplyTo: &reqID,
	}
	return c.Send(ctx, env)
}

// Config configures a Dispatcher.
type Config struct {
	// Frames is the inbound RoutingEnvelope stream the dispatcher demuxes.
	// Required. The dispatcher exits when this channel closes.
	Frames <-chan protocol.RoutingEnvelope

	// OutboundBuffer is the buffer size for the dispatcher's outbound
	// channel. Defaults to 32 when zero. Bounded backpressure on a slow
	// consumer pauses per-conn goroutines, which is the desired flow
	// control.
	OutboundBuffer int

	// Logger is required. Used for WARN/DEBUG diagnostics; no info-level
	// lifecycle logging at this layer (the daemon-side wiring logs that).
	Logger *slog.Logger
}

// Dispatcher demultiplexes frames by conn_id and routes each through the
// handler table.
type Dispatcher struct {
	cfg      Config
	handlers map[string]Handler
	outbound chan protocol.RoutingEnvelope

	// started flips to true at the top of Run. Register checks it under
	// no extra synchronisation (atomic) and panics on late registration;
	// this turns the "Register-only-before-Run" contract from a
	// convention into an enforced invariant, eliminating a potential
	// data race on the handlers map.
	started atomic.Bool

	mu    sync.Mutex
	conns map[string]*connState
}

// connState is the per-conn_id internal record. The Conn pointer is what
// handlers see; the input channel and exit signal are dispatcher-internal.
type connState struct {
	conn  *Conn
	input chan protocol.RoutingEnvelope
}

// New constructs a Dispatcher. Panics if cfg.Frames or cfg.Logger is nil
// (programmer error; mirrors transport.New).
func New(cfg Config) *Dispatcher {
	if cfg.Frames == nil {
		panic("dispatch: Config.Frames is required")
	}
	if cfg.Logger == nil {
		panic("dispatch: Config.Logger is required")
	}
	if cfg.OutboundBuffer <= 0 {
		cfg.OutboundBuffer = 32
	}
	return &Dispatcher{
		cfg:      cfg,
		handlers: make(map[string]Handler),
		outbound: make(chan protocol.RoutingEnvelope, cfg.OutboundBuffer),
		conns:    make(map[string]*connState),
	}
}

// Register installs a handler for envType. Must be called before Run;
// panics if Run has already started (the handlers map is otherwise
// lock-free in the read path, so late registration would be a data
// race). Also panics on duplicate registration (programmer error;
// downstream verb slices register one route apiece).
func (d *Dispatcher) Register(envType string, h Handler) {
	if d.started.Load() {
		panic(fmt.Sprintf("dispatch: Register(%q) after Run has started", envType))
	}
	if _, dup := d.handlers[envType]; dup {
		panic(fmt.Sprintf("dispatch: duplicate handler for envelope type %q", envType))
	}
	d.handlers[envType] = h
}

// Outbound returns the channel of binary→relay frames produced by
// handlers (and dispatcher-synthesised error replies). Closes after Run
// returns; callers can range over it.
func (d *Dispatcher) Outbound() <-chan protocol.RoutingEnvelope { return d.outbound }

// Run blocks until cfg.Frames closes or ctx is done. Returns nil for
// Frames-close (normal lifecycle end) and ctx.Err() for cancellation.
// On return, every per-conn goroutine has exited and Outbound is closed.
//
// Run flips started before reading from Frames, locking the handlers
// map against further Register calls.
func (d *Dispatcher) Run(ctx context.Context) error {
	d.started.Store(true)
	var (
		wg      sync.WaitGroup
		runErr  error
		stopped bool
	)

	stop := func(err error) {
		if stopped {
			return
		}
		stopped = true
		runErr = err
		d.mu.Lock()
		for _, st := range d.conns {
			close(st.input)
		}
		d.mu.Unlock()
	}

loop:
	for {
		select {
		case <-ctx.Done():
			stop(ctx.Err())
			break loop
		case env, ok := <-d.cfg.Frames:
			if !ok {
				stop(nil)
				break loop
			}
			st := d.getOrCreateConn(ctx, &wg, env.ConnID)
			select {
			case st.input <- env:
			case <-ctx.Done():
				stop(ctx.Err())
				break loop
			}
		}
	}

	wg.Wait()
	close(d.outbound)
	return runErr
}

// getOrCreateConn looks up the per-conn state for connID and, if
// missing, allocates it AND starts the per-conn goroutine — all inside
// a single d.mu critical section. Splitting lookup, insert, and
// goroutine-start across separate locked sections would open a window
// where two frames for the same conn could create two goroutines or
// the goroutine could observe an input channel that hasn't been
// registered in d.conns yet. Keep this method atomic.
func (d *Dispatcher) getOrCreateConn(ctx context.Context, wg *sync.WaitGroup, connID string) *connState {
	d.mu.Lock()
	defer d.mu.Unlock()
	if st, ok := d.conns[connID]; ok {
		return st
	}
	st := &connState{
		conn: &Conn{
			id:       connID,
			outbound: d.outbound,
		},
		input: make(chan protocol.RoutingEnvelope, 8),
	}
	d.conns[connID] = st
	wg.Add(1)
	go d.runConn(ctx, wg, st)
	return st
}

func (d *Dispatcher) runConn(ctx context.Context, wg *sync.WaitGroup, st *connState) {
	defer wg.Done()
	for routing := range st.input {
		d.handleOne(ctx, st.conn, routing)
	}
}

func (d *Dispatcher) handleOne(ctx context.Context, c *Conn, routing protocol.RoutingEnvelope) {
	var env protocol.Envelope
	if err := json.Unmarshal(routing.Frame, &env); err != nil {
		d.cfg.Logger.Warn("dispatch: malformed inner frame; replying protocol.malformed",
			"conn_id", c.ConnID(), "err", err)
		d.sendError(ctx, c, nil, protocol.CodeProtocolMalformed, "malformed envelope")
		return
	}

	if err := protocol.IsV1Compatible(env); err != nil {
		switch {
		case errors.Is(err, protocol.ErrUnsupported):
			d.sendError(ctx, c, &env.ID, protocol.CodeProtocolUnsupported, "unsupported envelope feature")
		case errors.Is(err, protocol.ErrUnknownType):
			d.sendError(ctx, c, &env.ID, protocol.CodeProtocolUnknownType, "unknown envelope type")
		default:
			d.sendError(ctx, c, &env.ID, protocol.CodeProtocolUnsupported, "unsupported envelope")
		}
		return
	}

	h, ok := d.handlers[env.Type]
	if !ok {
		d.sendError(ctx, c, &env.ID, protocol.CodeProtocolUnsupported, "no handler registered for envelope type")
		return
	}

	if err := h(ctx, c, env); err != nil {
		d.cfg.Logger.Warn("dispatch: handler returned error",
			"conn_id", c.ConnID(), "type", env.Type, "err", err)
	}
}

func (d *Dispatcher) sendError(ctx context.Context, c *Conn, inReplyTo *uint64, code, message string) {
	payload := protocol.ErrorPayload{
		Code:      code,
		Message:   message,
		Retryable: false,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		d.cfg.Logger.Warn("dispatch: marshal error payload",
			"conn_id", c.ConnID(), "code", code, "err", err)
		return
	}
	env := protocol.Envelope{
		ID:        c.NextID(),
		Type:      protocol.TypeError,
		TS:        time.Now().UTC(),
		Payload:   payloadJSON,
		InReplyTo: inReplyTo,
	}
	if err := c.Send(ctx, env); err != nil {
		d.cfg.Logger.Debug("dispatch: send error envelope dropped",
			"conn_id", c.ConnID(), "code", code, "err", err)
	}
}
