package acp

import (
	"encoding/json"
	"fmt"
)

// JSON-RPC 2.0 error codes. Handlers may reference these when returning an
// *Error; the transport itself produces CodeParseError, CodeInvalidRequest,
// CodeMethodNotFound, and CodeInternalError. CodeInvalidParams is provided for
// handlers that validate their params.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Error is a JSON-RPC error a handler may return to control the wire error
// code. A handler returning a plain error instead maps to CodeInternalError
// with a generic message (the real error is logged to stderr, never leaked to
// the client).
type Error struct {
	Code    int
	Message string
	// Data is optional structured detail; omitted from the wire when nil.
	Data any
}

// Error implements the error interface.
func (e *Error) Error() string {
	return fmt.Sprintf("jsonrpc: code %d: %s", e.Code, e.Message)
}

// NewError constructs an *Error carrying code and message (no data).
func NewError(code int, message string) *Error {
	return &Error{Code: code, Message: message}
}

// rpcMessage is the single decode-by-shape inbound frame. Field presence is
// detected by nil-ness: an absent JSON key leaves the *string / json.RawMessage
// at nil, while a present key — even a literal null — is non-nil. That single
// distinction is the whole classifier's input (see Transport.handleLine).
type rpcMessage struct {
	Jsonrpc string          `json:"jsonrpc"`
	Method  *string         `json:"method"` // nil ⇒ absent
	Params  json.RawMessage `json:"params"`
	ID      json.RawMessage `json:"id"`     // nil ⇒ absent; non-nil even for literal null
	Result  json.RawMessage `json:"result"` // response-frame detection only
	Error   json.RawMessage `json:"error"`  // response-frame detection only
}

// successResponse is a JSON-RPC 2.0 success reply: result present, error
// absent. ID echoes the request id verbatim.
type successResponse struct {
	Jsonrpc string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	ID      json.RawMessage `json:"id"`
}

// errorResponse is a JSON-RPC 2.0 error reply: error present, result absent.
// ID echoes the request id, or is null when the id is unrecoverable
// (parse / invalid-request / batch errors).
type errorResponse struct {
	Jsonrpc string          `json:"jsonrpc"`
	Error   rpcError        `json:"error"`
	ID      json.RawMessage `json:"id"`
}

// rpcError is the nested error object of an errorResponse.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}
