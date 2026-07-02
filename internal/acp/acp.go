// Package acp is the inbound transport floor for the Agent Client Protocol
// (ACP, Zed-stewarded): line-delimited JSON-RPC 2.0 over an io.Reader
// (inbound) and io.Writer (outbound), plus a method dispatch table. The host
// writes one JSON object per line; the agent replies and streams
// notifications one JSON object per line; diagnostics go only to a structured
// logger (stderr in production), never onto the writer.
//
// This package owns framing, ids, and error codes — it knows nothing about
// any concrete ACP method (session/*). Later tickets register handlers against
// the dispatch table as pure registrations. It defines its own JSON-RPC wire
// types and imports only the standard library; it is deliberately NOT coupled
// to the mobile-wire envelope types in internal/protocol.
//
// Hard cost invariant (stated here, enforced by later tickets): when a
// subsequent ticket wires claude behind an ACP method, pyry acp MUST drive a
// real INTERACTIVE claude session billed under the interactive subscription —
// never the non-interactive `claude -p` path and never the metered Agent SDK.
// This ticket ships no claude driving and cannot violate the invariant; the
// constraint is recorded here so it stays visible at the transport layer.
//
// Batch decision: a top-level JSON array (a JSON-RPC batch) is rejected with a
// single Invalid Request (-32600, id null). Batching is not supported — ACP
// does not use it, so this deviation from the spec's per-element batch
// handling is inert in practice and keeps the transport a strict
// one-frame-per-line reader.
//
// Diagnostics MUST NOT log line bytes or params content: an ACP request's
// params may carry user prompt material. The transport logs only method names,
// error kinds, and structural facts — never the frame body.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
)

// maxLineBytes caps a single buffered line. Mirrors internal/agentrun/jsonl
// sizing: real host frames are far smaller, and 16 MiB bounds memory against a
// pathologically broken writer. A longer line surfaces as bufio.ErrTooLong and
// breaks the stream.
const maxLineBytes = 16 << 20

// initialBufCap is the starting capacity of the scanner's line buffer.
const initialBufCap = 8192

// Handler is the contract every later ACP-method ticket registers against. It
// receives the raw JSON-RPC params (may be nil) and returns either a result to
// marshal into the JSON-RPC "result", or an error. A notification handler
// returns (nil, nil) — "nothing". A returned *Error controls the wire error
// code and message; any other error maps to CodeInternalError with a generic
// message, the detail logged to stderr and never leaked to the client.
type Handler func(ctx context.Context, params json.RawMessage) (result any, err error)

// Transport owns the line-delimited JSON-RPC 2.0 framing over one io.Reader
// (inbound) plus one io.Writer (outbound) and the method dispatch table.
type Transport struct {
	r   io.Reader
	w   io.Writer
	log *slog.Logger

	// handlers is written only by Register (before Serve) and read only
	// during Serve on the single read-loop goroutine, so it needs no lock;
	// the started flag turns "register only before Serve" into an enforced
	// invariant.
	handlers map[string]Handler
	started  atomic.Bool

	// writeMu serialises all writes to w through enc. Redundant under today's
	// single-goroutine dispatch, but it is the seam #757's outbound-request
	// writer shares — adding an agent→client caller then needs no rework of
	// the write path. Leaf lock: never held across a handler call or a read.
	writeMu sync.Mutex
	enc     *json.Encoder
}

// New constructs a Transport reading JSON-RPC frames from r and writing them
// to w. r and w are required (panics if nil — programmer error). log is
// optional (nil → slog.Default()); diagnostics go only to log, never to w.
func New(r io.Reader, w io.Writer, log *slog.Logger) *Transport {
	if r == nil {
		panic("acp.New: reader is required, got nil")
	}
	if w == nil {
		panic("acp.New: writer is required, got nil")
	}
	if log == nil {
		log = slog.Default()
	}
	enc := json.NewEncoder(w)
	// Protocol content is not HTML; keep <, >, & verbatim on the wire.
	enc.SetEscapeHTML(false)
	return &Transport{
		r:        r,
		w:        w,
		log:      log,
		handlers: make(map[string]Handler),
		enc:      enc,
	}
}

// Register binds h to a method name. Must be called before Serve. Panics on a
// duplicate method or if called after Serve has started (both programmer
// errors — mirrors internal/dispatch.Register).
func (t *Transport) Register(method string, h Handler) {
	if t.started.Load() {
		panic(fmt.Sprintf("acp.Register(%q): Serve has already started", method))
	}
	if _, dup := t.handlers[method]; dup {
		panic(fmt.Sprintf("acp.Register(%q): duplicate handler", method))
	}
	t.handlers[method] = h
}

// Serve reads frames until the reader reaches EOF (returns nil) or ctx is
// cancelled between frames (returns ctx.Err()). A structurally broken stream
// (over-long line or read error) returns a wrapped error. Serve never panics
// on malformed input — each malformed line produces a JSON-RPC error frame (or
// a silent drop for a response frame) and the loop continues.
//
// A Read already blocked on a quiet reader cannot be unblocked by ctx alone
// (Go limitation); the ctx check happens between frames. In-memory callers
// terminate naturally at EOF; the subcommand (#756) unblocks a real blocked
// stdin by closing the reader on shutdown.
func (t *Transport) Serve(ctx context.Context) error {
	t.started.Store(true)

	scanner := bufio.NewScanner(t.r)
	scanner.Buffer(make([]byte, 0, initialBufCap), maxLineBytes)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		t.handleLine(ctx, scanner.Bytes())
	}
	if err := scanner.Err(); err != nil {
		// Over-long line / read error: the stream is structurally broken.
		// Log the error kind only — never the buffered bytes.
		t.log.Error("acp: serve: read error", "err", err.Error())
		return fmt.Errorf("acp: serve: %w", err)
	}
	return nil
}

// handleLine classifies one raw line and dispatches or replies accordingly.
// The classification is total by construction (json.Valid gate + first-byte
// peek + typed unmarshal all return errors, never panic), so no recover is
// needed to satisfy the never-panic-on-malformed-input guarantee.
//
// Note: scanner.Bytes() is only valid until the next Scan; every path that
// retains bytes past this call (dispatch → handler via Params) is fed a
// json.RawMessage sliced from a value the scanner does not reuse within a
// single handleLine call, and handlers run inline before the next Scan.
func (t *Transport) handleLine(ctx context.Context, line []byte) {
	trimmed := trimSpace(line)
	if len(trimmed) == 0 {
		return // blank / whitespace-only line: skip, no output, no dispatch
	}
	if !json.Valid(trimmed) {
		t.log.Debug("acp: parse error on non-JSON line")
		t.writeError(nil, CodeParseError, "parse error")
		return
	}
	switch trimmed[0] {
	case '{':
		// object — fall through to shape classification below
	case '[':
		t.log.Debug("acp: rejecting unsupported JSON-RPC batch")
		t.writeError(nil, CodeInvalidRequest, "batch requests are not supported")
		return
	default:
		// valid JSON but not an object (number, string, bool, null)
		t.log.Debug("acp: invalid request: JSON value is not an object")
		t.writeError(nil, CodeInvalidRequest, "invalid request")
		return
	}

	var msg rpcMessage
	if err := json.Unmarshal(trimmed, &msg); err != nil {
		// Well-formed JSON object whose field types do not fit the frame
		// shape (e.g. {"method":5}). Log the kind, never the bytes.
		t.log.Debug("acp: invalid request: unmarshal failed", "err", err.Error())
		t.writeError(nil, CodeInvalidRequest, "invalid request")
		return
	}

	switch {
	case msg.Method != nil && msg.ID != nil:
		t.dispatchRequest(ctx, &msg)
	case msg.Method != nil:
		t.dispatchNotification(ctx, &msg)
	case msg.ID != nil && (msg.Result != nil || msg.Error != nil):
		// A JSON-RPC response frame. Tolerated — dropped with no writer
		// output — so the outbound sibling (#757) can later replace this
		// drop with response routing. Log kind only.
		t.log.Debug("acp: dropping inbound response frame (outbound routing lands in #757)")
	default:
		// Object, valid JSON, but neither request, notification, nor response.
		t.log.Debug("acp: invalid request: object is not a JSON-RPC frame")
		t.writeError(nil, CodeInvalidRequest, "invalid request")
	}
}

// dispatchRequest routes a request (method + id) to its handler and writes
// exactly one response echoing the request id.
func (t *Transport) dispatchRequest(ctx context.Context, msg *rpcMessage) {
	method := *msg.Method
	h, ok := t.handlers[method]
	if !ok {
		t.writeError(msg.ID, CodeMethodNotFound, "method not found")
		return
	}
	result, err := h(ctx, msg.Params)
	if err != nil {
		var rpcErr *Error
		if errors.As(err, &rpcErr) {
			t.writeErrorWithData(msg.ID, rpcErr.Code, rpcErr.Message, rpcErr.Data)
			return
		}
		// Plain error: internal error. Log the detail; never leak it on the
		// wire.
		t.log.Warn("acp: handler error", "method", method, "err", err.Error())
		t.writeError(msg.ID, CodeInternalError, "internal error")
		return
	}
	t.writeSuccess(msg.ID, result)
}

// dispatchNotification routes a notification (method, no id) to its handler.
// A notification never produces a response, regardless of the handler's return.
func (t *Transport) dispatchNotification(ctx context.Context, msg *rpcMessage) {
	method := *msg.Method
	h, ok := t.handlers[method]
	if !ok {
		t.log.Debug("acp: notification for unregistered method dropped", "method", method)
		return
	}
	if _, err := h(ctx, msg.Params); err != nil {
		t.log.Warn("acp: notification handler error", "method", method, "err", err.Error())
	}
}

// writeSuccess writes a success response, marshalling result into the wire
// "result". A nil result marshals to null. A result that fails to marshal
// (a handler bug) falls back to an internal-error frame so the request still
// gets exactly one reply.
func (t *Transport) writeSuccess(id json.RawMessage, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		t.log.Warn("acp: marshal handler result failed", "err", err.Error())
		t.writeError(id, CodeInternalError, "internal error")
		return
	}
	t.reply(successResponse{Jsonrpc: "2.0", Result: raw, ID: idOrNull(id)})
}

// writeError writes an error response with no data.
func (t *Transport) writeError(id json.RawMessage, code int, message string) {
	t.writeErrorWithData(id, code, message, nil)
}

// writeErrorWithData writes an error response carrying optional structured
// data (only handler-returned *Error values populate data today).
func (t *Transport) writeErrorWithData(id json.RawMessage, code int, message string, data any) {
	t.reply(errorResponse{
		Jsonrpc: "2.0",
		Error:   rpcError{Code: code, Message: message, Data: data},
		ID:      idOrNull(id),
	})
}

// reply writes one frame to w, logging (never leaking on the wire) on failure.
func (t *Transport) reply(frame any) {
	if err := t.writeMessage(frame); err != nil {
		t.log.Warn("acp: write frame failed", "err", err.Error())
	}
}

// writeMessage encodes v as one compact JSON object plus a trailing newline to
// w, serialised under writeMu. This is the shared write seam #757's
// outbound-request writer reuses — hence the returned error even though today's
// only callers log-and-drop it.
func (t *Transport) writeMessage(v any) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return t.enc.Encode(v)
}

// idOrNull returns id when present, or the literal JSON null when the id is
// unrecoverable (parse / invalid-request / batch errors). Per JSON-RPC 2.0 an
// error response always carries an id member.
func idOrNull(id json.RawMessage) json.RawMessage {
	if id == nil {
		return json.RawMessage("null")
	}
	return id
}

// trimSpace strips only the four bytes JSON treats as insignificant whitespace
// (space, tab, CR, LF). bytes.TrimSpace would additionally strip Unicode
// whitespace (e.g. a non-breaking space), silently broadening the accept set to
// framings strict JSON rejects — so the narrower, spec-matching trim is
// deliberate here, mirroring the TrimSuffix-over-TrimSpace discipline in
// internal/identity.
func trimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && isSpace(b[start]) {
		start++
	}
	end := len(b)
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}
