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
// the sole production Conn factory (see routeConn); other production
// callers that own their own per-conn goroutine should use NewConn.
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

// NewConn constructs a *Conn for production callers that own their own
// per-conn goroutine and route envelopes outside Dispatcher.Run (e.g.
// the v2 session manager, which decrypts a noise_msg before dispatching
// the inner envelope through the handler table via Route). The caller
// owns outbound and is responsible for draining it.
//
// Distinct from NewTestConn only in policy: NewTestConn carries the
// "test fixtures only" restriction; NewConn is the production-allowed
// equivalent. The Conn returned has nextID at zero, matching the
// gate-disabled production path.
func NewConn(id string, outbound chan<- protocol.RoutingEnvelope, auth *devices.Device) *Conn {
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
// gateStarted is local to the per-conn goroutine: written and read by the
// same goroutine inside runConn to prevent double-running the gate on
// subsequent loop iterations. Lock-free.
//
// gateCompleted and closed are written by the per-conn goroutine and read
// by ActiveConns callers / the demux on other goroutines. Writes happen
// under d.mu so the cross-goroutine reads are sound. gateCompleted is set
// ONLY AFTER runGate's accept path has emitted hello_ack AND advanced the
// per-conn id counter via NextID() — see ActiveConns for the rationale.
type connState struct {
	conn          *Conn
	input         chan protocol.RoutingEnvelope
	gateStarted   bool // local-only; prevents re-entering the gate path
	gateCompleted bool // under d.mu; gates broadcast eligibility
	closed        bool // under d.mu; gate-reject / close-intent
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

// ActiveConns returns a snapshot of currently-active conns eligible for
// server-initiated outbound (broadcast). The returned slice excludes:
//   - conns marked closed (gate-reject path; routeConn drops further
//     frames for them)
//   - conns whose first-frame gate has not yet RETURNED on the
//     accept-and-continue path — i.e. connState.gateCompleted == false.
//     "Completed" means runGate's `_ = c.NextID()` advance has executed,
//     so the per-conn id counter has moved past the hello_ack's literal
//     id=1 and a broadcast call to c.NextID() will return id >= 2.
//
// The gate-completed filter is load-bearing: relay.AuthenticateFirstFrame
// emits hello_ack with literal ID=1, not via c.NextID(); runGate publishes
// that response onto d.outbound and THEN calls `_ = c.NextID()` so the
// next binary-originated frame on this conn gets id=2. A broadcast that
// races the gate would call c.NextID() first, claim id=1 for its message
// envelope, and collide with hello_ack on the wire (two envelopes both
// stamped id=1). Filtering on gateCompleted — set ONLY after runGate has
// returned on the accept path — closes that race deterministically.
//
// The slice is fresh; callers may retain it. The returned *Conn pointers
// remain safe to call Send on — a conn that closes between snapshot and
// Send is handled by the demux's existing closed-conn drop in routeConn.
//
// Concurrency: holds d.mu briefly to copy the conns map under the same
// lock that guards both closed-flag and gateCompleted mutation; no new
// lock order.
func (d *Dispatcher) ActiveConns() []*Conn {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*Conn, 0, len(d.conns))
	for _, st := range d.conns {
		if st.gateCompleted && !st.closed {
			out = append(out, st.conn)
		}
	}
	return out
}

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
		if !st.gateStarted {
			// gateStarted is local-only: this goroutine is the only
			// writer/reader, so the assignment stays lock-free and just
			// guards against re-entering the gate path on the next loop
			// iteration. Broadcast-eligibility lives on a separate flag
			// (gateCompleted) set AFTER runGate returns so a broadcast
			// cannot race the literal id=1 hello_ack — see ActiveConns.
			st.gateStarted = true
			if d.cfg.FirstFrame != nil {
				if d.runGate(ctx, st, routing) {
					// Close-intent: mark closed under d.mu so the demux
					// drops further frames, then exit. wg.Done() fires
					// via defer. gateCompleted stays false — a rejected
					// conn is never broadcast-eligible.
					d.mu.Lock()
					st.closed = true
					d.mu.Unlock()
					return
				}
				// Gate accepted. runGate has already published hello_ack
				// onto d.outbound AND advanced the per-conn id counter
				// past id=1; publish gateCompleted under d.mu so a
				// broadcast reader on another goroutine observes the
				// post-advance state. Skip handleOne — the gate consumed
				// this inbound envelope.
				d.mu.Lock()
				st.gateCompleted = true
				d.mu.Unlock()
				continue
			}
			// No gate configured — there is no hello_ack on the wire to
			// collide with, so the conn is immediately broadcast-
			// eligible. Publish under d.mu for the cross-goroutine read.
			d.mu.Lock()
			st.gateCompleted = true
			d.mu.Unlock()
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
		sendError(ctx, d.cfg.Logger, st.conn, nil, protocol.CodeProtocolMalformed, "malformed envelope")
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
	Route(ctx, d.cfg.Logger, c, d.handlers, routing.Frame)
}

// Route dispatches a single inbound envelope frame through handlers,
// using the same malformed / IsV1Compatible / unknown-type error-envelope
// paths as Dispatcher's per-conn loop. Suitable for callers that own
// their own per-conn goroutine and only need single-frame handler-table
// dispatch (e.g. the v2 session manager's post-AEAD-decrypt dispatch).
//
// Error replies (malformed envelope JSON, unsupported v1 features,
// unknown envelope type, no registered handler) are emitted via
// conn.Send → conn.outbound. A non-nil error returned from the handler
// itself is logged at WARN; no automatic reply is synthesised (matches
// Dispatcher.Run's posture). handlers may be nil — every envelope then
// falls through to the "no handler registered" reply path.
//
// Route does NOT change conn.outbound's blocking behaviour: the caller
// is responsible for sizing the channel so handler+Route replies fit
// without head-of-line-blocking the dispatch loop.
func Route(ctx context.Context, logger *slog.Logger, conn *Conn, handlers map[string]Handler, frame json.RawMessage) {
	var env protocol.Envelope
	if err := json.Unmarshal(frame, &env); err != nil {
		logger.Warn("dispatch: malformed inner frame; replying protocol.malformed",
			"conn_id", conn.ConnID(), "err", err)
		sendError(ctx, logger, conn, nil, protocol.CodeProtocolMalformed, "malformed envelope")
		return
	}

	if err := protocol.IsV1Compatible(env); err != nil {
		switch {
		case errors.Is(err, protocol.ErrUnsupported):
			sendError(ctx, logger, conn, &env.ID, protocol.CodeProtocolUnsupported, "unsupported envelope feature")
		case errors.Is(err, protocol.ErrUnknownType):
			sendError(ctx, logger, conn, &env.ID, protocol.CodeProtocolUnknownType, "unknown envelope type")
		default:
			sendError(ctx, logger, conn, &env.ID, protocol.CodeProtocolUnsupported, "unsupported envelope")
		}
		return
	}

	h, ok := handlers[env.Type]
	if !ok {
		sendError(ctx, logger, conn, &env.ID, protocol.CodeProtocolUnsupported, "no handler registered for envelope type")
		return
	}

	if err := h(ctx, conn, env); err != nil {
		logger.Warn("dispatch: handler returned error",
			"conn_id", conn.ConnID(), "type", env.Type, "err", err)
	}
}

func sendError(ctx context.Context, logger *slog.Logger, c *Conn, inReplyTo *uint64, code, message string) {
	payload := protocol.ErrorPayload{
		Code:      code,
		Message:   message,
		Retryable: false,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		logger.Warn("dispatch: marshal error payload",
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
		logger.Debug("dispatch: send error envelope dropped",
			"conn_id", c.ConnID(), "code", code, "err", err)
	}
}
