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

	"github.com/pyrycode/pyrycode/internal/devices"
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

	// auth is the matched device snapshot from the first-frame gate's
	// accept verdict. Written exactly once by the dispatcher (via
	// setAuth) on the per-conn goroutine, before the first handler-table
	// dispatch on this conn. Reads occur strictly after the write on the
	// same goroutine, so no synchronisation is required for the dominant
	// (handler-on-per-conn-goroutine) path. Handler-spawned worker
	// goroutines reading Auth() are happens-before-safe because goroutine
	// start synchronises with prior writes on the spawning goroutine.
	auth *devices.Device
}

// ConnID returns the relay-assigned conn_id this Conn dispatches for.
func (c *Conn) ConnID() string { return c.id }

// Auth returns the authenticated device snapshot for this conn, or nil
// if the first-frame gate has not yet accepted on this conn (the
// gate-disabled test path, a pre-accept frame, or a reject/Err path
// where the slot was never populated). Verb handlers MUST nil-check
// the result before dereferencing.
func (c *Conn) Auth() *devices.Device { return c.auth }

// setAuth is the dispatcher-only seam for populating the auth slot.
// Unexported so verb handler closures cannot mutate auth state. Called
// at most once per conn, on the per-conn goroutine, from runGate's
// accept branch.
func (c *Conn) setAuth(d *devices.Device) { c.auth = d }

// NewTestConn constructs a *Conn for verb-handler test fixtures. Test
// fixtures only — do not call from production code. The dispatcher is
// the sole production Conn factory (see routeConn). The constructor
// exists so handler tests in sibling packages can drive a real Conn
// against a caller-supplied outbound channel and a synthesised auth
// snapshot, without depending on a full Dispatcher.
//
// The returned Conn has nextID at zero, so the first NextID() call
// returns 1 (matching the gate-disabled production path). Tests that
// want to simulate the post-hello_ack state — where id=1 has been
// consumed by AuthenticateFirstFrame's reply and the next handler-
// originated reply lands at id=2 — should call c.NextID() once before
// invoking the handler.
func NewTestConn(id string, outbound chan<- protocol.RoutingEnvelope, auth *devices.Device) *Conn {
	return &Conn{id: id, outbound: outbound, auth: auth}
}

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
	//
	// SECURITY: the dispatcher never logs RoutingEnvelope.Token at any
	// level; the gate closure passed via FirstFrame must honor the same
	// rule (the token is plaintext credential material).
	Logger *slog.Logger

	// FirstFrame, if non-nil, is invoked on the FIRST inbound frame for
	// every new conn_id, before normal handler-table dispatch. The
	// dispatcher uses the returned outcome to either (a) forward
	// Response and continue dispatching subsequent frames on this conn
	// normally, (b) forward Response with CloseCode set and stop the
	// per-conn goroutine, or (c) fall through to the malformed-frame
	// error path. Nil disables the gate (every frame goes straight to
	// the handler table — the pre-#308 behavior preserved for tests).
	//
	// CloseCode on inbound frames is ignored: it is a binary→relay-only
	// signal. The gate runs on the inner Frame regardless of any
	// CloseCode the relay-side attacker may have injected on the wire.
	FirstFrame FirstFrameGate
}

// FirstFrameGate is the per-conn first-frame interceptor. Called exactly
// once per conn_id, on the per-conn goroutine, with the inbound envelope.
// Returns the response envelope the dispatcher should forward plus a
// close-or-keep decision.
//
// SECURITY: implementations MUST NOT log env.Token at any level. The
// token is plaintext credential material; only AuthenticateFirstFrame
// is allowed to consume it.
type FirstFrameGate func(ctx context.Context, env protocol.RoutingEnvelope) FirstFrameOutcome

// FirstFrameOutcome carries the gate's verdict back to the dispatcher.
type FirstFrameOutcome struct {
	// Response is the routing envelope to forward to the relay. The
	// dispatcher publishes Response verbatim (its ConnID is expected
	// to match the incoming env.ConnID; the gate owns ID/InReplyTo/TS
	// construction). Required when Err is nil.
	Response protocol.RoutingEnvelope

	// CloseConn, when true, causes the dispatcher to set
	// Response.CloseCode = Code before publishing, and to stop the
	// per-conn goroutine after Response is sent.
	CloseConn bool

	// Code is the WS close code (4401 for auth.invalid_token).
	// Required when CloseConn is true; ignored otherwise.
	Code uint16

	// Err signals a gate-level failure (e.g. the inbound frame was
	// malformed JSON). The dispatcher falls through to its existing
	// protocol.malformed refusal path (no in_reply_to) and does NOT
	// publish Response. The first-frame status is still consumed so a
	// buggy phone cannot retry into the gate forever; subsequent frames
	// flow through the regular handler table.
	Err error

	// Device is the matched device snapshot from the gate. Populated iff
	// Err == nil && !CloseConn (the accept-and-continue branch). The
	// dispatcher MUST NOT propagate Device on the Err or CloseConn
	// branches; even a misbehaving gate that supplies it there leaves
	// Conn.Auth() nil.
	Device *devices.Device
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
//
// gateRun is read/written only on the per-conn goroutine (single-writer);
// no mutex needed. closed is written by the per-conn goroutine just
// before it returns on a close-intent outcome, and read by the demux
// under d.mu so a frame arriving for a dead per-conn goroutine cannot
// block the demux on a channel send with no receiver.
type connState struct {
	conn    *Conn
	input   chan protocol.RoutingEnvelope
	gateRun bool
	closed  bool
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
			st, dropped := d.routeConn(ctx, &wg, env.ConnID)
			if dropped {
				// Per-conn goroutine has exited (auth reject closed
				// the conn). Drop further frames silently; the relay
				// has already been asked to close that phone's WS.
				d.cfg.Logger.Debug("dispatch: drop frame for closed conn",
					"conn_id", env.ConnID, "len", len(env.Frame))
				continue
			}
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

// routeConn looks up the per-conn state for connID and, if missing,
// allocates it AND starts the per-conn goroutine — all inside a single
// d.mu critical section. Splitting lookup, insert, and goroutine-start
// across separate locked sections would open a window where two frames
// for the same conn could create two goroutines or the goroutine could
// observe an input channel that hasn't been registered in d.conns yet.
// Keep this method atomic.
//
// Returns (nil, true) when the per-conn goroutine has already exited on
// a close-intent outcome — the caller drops the frame. The closed-flag
// check happens under the same lock that gates getOrCreate so the demux
// cannot race with the per-conn goroutine's exit-and-mark sequence.
func (d *Dispatcher) routeConn(ctx context.Context, wg *sync.WaitGroup, connID string) (*connState, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if st, ok := d.conns[connID]; ok {
		if st.closed {
			return nil, true
		}
		return st, false
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
	return st, false
}

func (d *Dispatcher) runConn(ctx context.Context, wg *sync.WaitGroup, st *connState) {
	defer wg.Done()
	for routing := range st.input {
		if !st.gateRun {
			st.gateRun = true
			if d.cfg.FirstFrame != nil {
				if d.runGate(ctx, st, routing) {
					// Close-intent: mark closed under d.mu so the demux
					// drops further frames, then exit. wg.Done() fires
					// via defer.
					d.mu.Lock()
					st.closed = true
					d.mu.Unlock()
					return
				}
				continue
			}
		}
		d.handleOne(ctx, st.conn, routing)
	}
}

// runGate invokes the FirstFrame gate and acts on its outcome. Returns
// true iff the per-conn goroutine should exit (close-intent verdict).
// The gate runs synchronously on the per-conn goroutine; a slow gate
// stalls only this conn, never the demux or other conns.
func (d *Dispatcher) runGate(ctx context.Context, st *connState, routing protocol.RoutingEnvelope) bool {
	outcome := d.cfg.FirstFrame(ctx, routing)
	if outcome.Err != nil {
		d.cfg.Logger.Warn("dispatch: first-frame gate err; replying protocol.malformed",
			"conn_id", st.conn.ConnID(), "err", outcome.Err)
		d.sendError(ctx, st.conn, nil, protocol.CodeProtocolMalformed, "malformed envelope")
		return false
	}
	resp := outcome.Response
	if outcome.CloseConn {
		resp.CloseCode = outcome.Code
	} else {
		// Accept-and-continue: populate the per-conn auth slot before
		// any handler runs. The close-intent and Err branches do not
		// reach this assignment even if a buggy gate supplied
		// outcome.Device — defence-in-depth lives on the consumer.
		st.conn.setAuth(outcome.Device)
	}
	select {
	case d.outbound <- resp:
	case <-ctx.Done():
		return outcome.CloseConn
	}
	if !outcome.CloseConn {
		// Advance the per-conn id counter past the hello_ack (id=1)
		// that AuthenticateFirstFrame just emitted, so the next
		// binary-originated frame on this conn (handler reply,
		// sendError, etc.) gets id=2 — per #308 AC #2.
		_ = st.conn.NextID()
	}
	return outcome.CloseConn
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
