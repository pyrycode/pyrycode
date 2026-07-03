package acp

import (
	"encoding/json"
	"fmt"
)

// notification is a JSON-RPC 2.0 notification frame: method + params, NO id and
// no response. request (jsonrpc.go) cannot be reused — its ID is a
// non-omitempty uint64, so it always serialises an "id" member, which a
// notification must never carry. Params is omitted from the wire when nil.
type notification struct {
	Jsonrpc string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Notify writes one JSON-RPC 2.0 notification (method + params, no id) to the
// client. It mirrors Call minus the id / pending-channel machinery: params is
// marshalled first — nil omits the key, and a marshal failure writes nothing
// (no partial frame) — then the frame goes out through writeMessage under
// writeMu, the same leaf lock serialising reply and Call. Notify never awaits a
// response and never blocks on one; it is safe from any goroutine.
//
// The streaming consumer (#750) calls this to emit session/update
// notifications; it is Call's write-only sibling and has no other caller yet.
func (t *Transport) Notify(method string, params any) error {
	var raw json.RawMessage
	if params != nil {
		p, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("acp: marshal notification params: %w", err)
		}
		raw = p
	}
	if err := t.writeMessage(notification{Jsonrpc: "2.0", Method: method, Params: raw}); err != nil {
		return fmt.Errorf("acp: write notification: %w", err)
	}
	return nil
}
