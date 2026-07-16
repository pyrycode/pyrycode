// Package dispatch routes a single inbound relay frame through a handler
// table. Route decodes the inner protocol.Envelope, refuses frames whose
// type is not a known inbound app-frame type via the
// protocol.IsKnownAppType check, and dispatches to the handler
// registered for the envelope Type. Conn is the per-conn_id state a
// handler sees: it owns the monotonic outbound id counter and the conn's
// outbound send seam. The package is carrier-agnostic — it imports
// internal/protocol and internal/devices only.
//
// The demux that owns a goroutine per conn_id and calls Route lives in the
// v2 session manager (internal/relay/v2session.go); this package provides
// the single-frame routing primitive that manager dispatches through.
//
// The handler table is supplied by the caller. Frames whose Envelope.Type
// has no registered handler fall through to a protocol.unsupported error
// reply; encrypted or otherwise unknown-type frames are refused via the
// protocol.IsKnownAppType check and map to protocol.unsupported /
// protocol.unknown_type. Malformed inner frames map to protocol.malformed.
//
// Security / operational notes (per the spec's Security review, #307):
//
//   - Inbound frame size cap is inherited from internal/transport's WS
//     read path. Route does not re-enforce; verb slices likewise rely on
//     the transport cap rather than per-handler limits.
//   - Log policy: Route diagnostics carry conn_id, envelope type, envelope
//     id, and the decode-error class — never the raw frame payload. Verb
//     slices crossing this code path (message bodies, push tokens) must
//     keep the same posture.
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
	"sync/atomic"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Handler processes a single inbound envelope on a phone conn. Returning a
// non-nil error is logged at WARN and otherwise ignored; Route synthesises
// no reply from a handler return value. Handlers reply by calling
// Conn.Reply or Conn.Send.
type Handler func(ctx context.Context, c *Conn, env protocol.Envelope) error

// Conn is the per-conn_id state exposed to handlers. It owns the
// monotonic outbound id counter and the conn's outbound send seam.
type Conn struct {
	id       string
	nextID   atomic.Uint64
	outbound chan<- protocol.RoutingEnvelope

	// auth is the authenticated device snapshot for this conn, supplied by
	// the constructor (NewConn / NewTestConn) — the v2 session manager
	// passes the handshake-matched device. Written once at construction and
	// never mutated, so reads via Auth() need no synchronisation.
	auth *devices.Device
}

// ConnID returns the relay-assigned conn_id this Conn dispatches for.
func (c *Conn) ConnID() string { return c.id }

// Auth returns the authenticated device snapshot for this conn, or nil if
// the conn was constructed without an auth device (a pre-auth path or a
// test fixture that passes nil). Verb handlers MUST nil-check the result
// before dereferencing.
func (c *Conn) Auth() *devices.Device { return c.auth }

// NewTestConn constructs a *Conn for verb-handler test fixtures. Test
// fixtures only — do not call from production code; production callers use
// NewConn. The returned Conn has nextID at zero, so the first NextID()
// call returns 1.
func NewTestConn(id string, outbound chan<- protocol.RoutingEnvelope, auth *devices.Device) *Conn {
	return &Conn{id: id, outbound: outbound, auth: auth}
}

// NewConn constructs a *Conn for production callers that own their own
// per-conn goroutine and dispatch envelopes through the handler table via
// Route (e.g. the v2 session manager, which decrypts a noise_msg before
// dispatching the inner envelope). The caller owns outbound and is
// responsible for draining it.
//
// Distinct from NewTestConn only in policy: NewTestConn carries the "test
// fixtures only" restriction; NewConn is the production-allowed
// equivalent. The Conn returned has nextID at zero, so the first NextID()
// call returns 1.
func NewConn(id string, outbound chan<- protocol.RoutingEnvelope, auth *devices.Device) *Conn {
	return &Conn{id: id, outbound: outbound, auth: auth}
}

// NextID returns the next monotonic outbound envelope id for this conn.
// Starts at 1 on the first call. Concurrent-safe even though the conn's
// owning goroutine is the only writer today — atomic is cheap insurance
// for future fan-out inside a handler.
func (c *Conn) NextID() uint64 { return c.nextID.Add(1) }

// Send wraps env in a RoutingEnvelope addressed to this conn and pushes
// it onto this conn's outbound channel. Blocks on backpressure; returns
// ctx.Err if ctx is cancelled while blocked. Caller is responsible for
// env.ID and env.TS — use Reply for the request/response convenience path.
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

// Route dispatches a single inbound envelope frame through handlers,
// applying the malformed / IsKnownAppType / unknown-type error-envelope
// paths. Suitable for callers that own their own per-conn goroutine and
// only need single-frame handler-table dispatch (e.g. the v2 session
// manager's post-AEAD-decrypt dispatch).
//
// Error replies (malformed envelope JSON, unsupported v1 features,
// unknown envelope type, no registered handler) are emitted via
// conn.Send → conn.outbound. A non-nil error returned from the handler
// itself is logged at WARN; no automatic reply is synthesised. handlers
// may be nil — every envelope then falls through to the "no handler
// registered" reply path.
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

	if err := protocol.IsKnownAppType(env); err != nil {
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
