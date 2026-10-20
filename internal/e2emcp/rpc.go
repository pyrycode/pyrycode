// Package e2emcp contains test-only MCP capture/replay boundary primitives.
// The ordinary daemon does not import it. No profile is registered by this
// package, and protocol-shape validation alone is not process or actuation proof.
package e2emcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxFrameBytes = 8 << 20
const MaxRPCFrames = 4096
const MaxPendingRPCs = 128

type FrameKind string

const (
	Request      FrameKind = "request"
	Notification FrameKind = "notification"
	Response     FrameKind = "response"
)

// RPCFrame retains the entire original frame, including fields inside params,
// result and error that a typed consumer may not inspect. Raw is never rewritten.
// IDKey is for semantic correlation only; it is never emitted onto either pipe.
type RPCFrame struct {
	Kind                      FrameKind
	IDKey, Method             string
	ID, Params, Result, Error json.RawMessage
	Raw                       json.RawMessage
}

func protocolError() error { return errors.New("MCP protocol boundary failed") }

// uniqueJSON rejects duplicate keys at every depth, invalid UTF-8 and extra
// documents before any field may select an operation or satisfy a correlation.
func uniqueJSON(raw []byte) bool { return uniqueJSONDocument(raw, true) }

// Persisted native JSON can be indented. RPC callers separately require one
// literal line; both forms reject duplicate keys, trailing documents and bounds.
func uniqueJSONDocument(raw []byte, singleLine bool) bool {
	if len(raw) == 0 || len(raw) > MaxFrameBytes || !utf8.Valid(raw) || singleLine && bytes.ContainsAny(raw, "\r\n") {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 128 {
			return protocolError()
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			keys := map[string]bool{}
			for decoder.More() {
				token, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := token.(string)
				if !ok || keys[key] {
					return protocolError()
				}
				keys[key] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			token, err = decoder.Token()
			if err != nil || token != json.Delim('}') {
				return protocolError()
			}
		case '[':
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			token, err = decoder.Token()
			if err != nil || token != json.Delim(']') {
				return protocolError()
			}
		default:
			return protocolError()
		}
		return nil
	}
	if value(0) != nil {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}
func rpcID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", protocolError()
	}
	if raw[0] == '"' {
		var id string
		if json.Unmarshal(raw, &id) != nil || id == "" || len(id) > 256 {
			return "", protocolError()
		}
		// SDK-generated string identifiers are opaque, bounded printable ASCII. This
		// excludes invalid surrogate repairs and control characters without treating
		// an identifier as a PID, path, permission or capability.
		for _, char := range id {
			if char < 33 || char > 126 {
				return "", protocolError()
			}
		}
		canonical, _ := json.Marshal(id)
		return "string:" + string(canonical), nil
	}
	number := string(raw)
	if strings.ContainsAny(number, ".eE") || number == "-0" {
		return "", protocolError()
	}
	id, err := strconv.ParseInt(number, 10, 64)
	if err != nil || id < -9007199254740991 || id > 9007199254740991 || strconv.FormatInt(id, 10) != number {
		return "", protocolError()
	}
	return "number:" + number, nil
}
func objectJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{' && uniqueJSON(raw)
}

// ParseRPC checks a single newline-framed JSON-RPC envelope. It makes no tool
// authorization decision and neither removes nor rewrites unknown nested data.
func ParseRPC(raw []byte) (RPCFrame, error) {
	fail := protocolError()
	var out RPCFrame
	if !uniqueJSON(raw) {
		return out, fail
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return out, fail
	}
	for name := range fields {
		if name != "jsonrpc" && name != "id" && name != "method" && name != "params" && name != "result" && name != "error" {
			return out, fail
		}
	}
	var version string
	if json.Unmarshal(fields["jsonrpc"], &version) != nil || version != "2.0" {
		return out, fail
	}
	method, hasMethod := fields["method"]
	id, hasID := fields["id"]
	_, hasResult := fields["result"]
	_, hasError := fields["error"]
	params, hasParams := fields["params"]
	out.Raw = append(json.RawMessage(nil), raw...)
	if hasID {
		key, err := rpcID(id)
		if err != nil {
			return RPCFrame{}, fail
		}
		out.IDKey = key
		out.ID = append(json.RawMessage(nil), id...)
	}
	if hasMethod {
		if hasResult || hasError || json.Unmarshal(method, &out.Method) != nil || out.Method == "" || len(out.Method) > 128 || hasParams && !objectJSON(params) {
			return RPCFrame{}, fail
		}
		for _, char := range out.Method {
			if char < 33 || char > 126 {
				return RPCFrame{}, fail
			}
		}
		out.Params = append(json.RawMessage(nil), params...)
		out.Kind = Request
		if !hasID {
			out.Kind = Notification
		}
		return out, nil
	}
	if !hasID || hasParams || hasResult == hasError {
		return RPCFrame{}, fail
	}
	out.Kind = Response
	out.Result = append(json.RawMessage(nil), fields["result"]...)
	out.Error = append(json.RawMessage(nil), fields["error"]...)
	if hasResult && !objectJSON(out.Result) {
		return RPCFrame{}, fail
	}
	if hasError {
		var value map[string]json.RawMessage
		if !objectJSON(out.Error) || json.Unmarshal(out.Error, &value) != nil {
			return RPCFrame{}, fail
		}
		for key := range value {
			if key != "code" && key != "message" && key != "data" {
				return RPCFrame{}, fail
			}
		}
		var code int
		var message string
		if bytes.Equal(bytes.TrimSpace(value["code"]), []byte("null")) || bytes.Equal(bytes.TrimSpace(value["message"]), []byte("null")) || json.Unmarshal(value["code"], &code) != nil || json.Unmarshal(value["message"], &message) != nil {
			return RPCFrame{}, fail
		}
	}
	return out, nil
}

type Direction string

const (
	ClientToServer Direction = "client-to-server"
	ServerToClient Direction = "server-to-client"
)

type Exchange struct {
	Ordinal  int
	Request  RPCFrame
	Response RPCFrame
}

// RPCSession is a sticky, append-only envelope ledger. Observe must be called
// only for successful complete forwards, in the real forwarding order. Its
// exchanges are shape/correlation evidence, never proof that a server executed
// or that a permission was granted. A runtime owner must separately bind actual
// process, source role/epoch, complete pipe EOF and exec.Cmd.Wait.
type RPCSession struct {
	frames                               int
	closed, failed                       bool
	initialized, initializedNotification bool
	seen                                 map[string]bool
	pending                              map[string]RPCFrame
	ordinals                             map[string]int
	exchanges                            []Exchange
}

func NewRPCSession() *RPCSession {
	return &RPCSession{seen: map[string]bool{}, pending: map[string]RPCFrame{}, ordinals: map[string]int{}}
}
func (s *RPCSession) reject() error { s.failed = true; return protocolError() }
func (s *RPCSession) Observe(direction Direction, raw []byte) (RPCFrame, error) {
	if s == nil {
		return RPCFrame{}, protocolError()
	}
	if s.seen == nil {
		s.seen = map[string]bool{}
		s.pending = map[string]RPCFrame{}
		s.ordinals = map[string]int{}
	}
	if s.closed || s.failed || s.frames >= MaxRPCFrames {
		return RPCFrame{}, s.reject()
	}
	frame, err := ParseRPC(raw)
	if err != nil {
		return RPCFrame{}, s.reject()
	}
	if direction != ClientToServer && direction != ServerToClient {
		return RPCFrame{}, s.reject()
	}
	if direction == ClientToServer {
		if frame.Kind == Response {
			return RPCFrame{}, s.reject()
		}
		if frame.Kind == Notification {
			if frame.Method != "notifications/initialized" || !s.initialized || s.initializedNotification || len(frame.Params) != 0 {
				return RPCFrame{}, s.reject()
			}
			s.initializedNotification = true
		} else {
			if s.seen[frame.IDKey] || len(s.pending) >= MaxPendingRPCs {
				return RPCFrame{}, s.reject()
			}
			if frame.Method == "initialize" {
				if s.frames != 0 || s.initialized {
					return RPCFrame{}, s.reject()
				}
			} else {
				if !s.initialized || !s.initializedNotification || frame.Method != "tools/list" && frame.Method != "tools/call" && frame.Method != "ping" {
					return RPCFrame{}, s.reject()
				}
			}
			s.seen[frame.IDKey] = true
			s.pending[frame.IDKey] = frame
			s.ordinals[frame.IDKey] = len(s.seen)
		}
	} else {
		request, ok := s.pending[frame.IDKey]
		if frame.Kind != Response || !ok {
			return RPCFrame{}, s.reject()
		}
		if request.Method == "initialize" {
			if len(frame.Error) > 0 || !objectJSON(frame.Result) {
				return RPCFrame{}, s.reject()
			}
			s.initialized = true
		}
		s.exchanges = append(s.exchanges, Exchange{Ordinal: s.ordinals[frame.IDKey], Request: request, Response: frame})
		delete(s.pending, frame.IDKey)
	}
	s.frames++
	return cloneRPCFrame(frame), nil
}
func (s *RPCSession) Finish() ([]Exchange, error) {
	if s == nil {
		return nil, protocolError()
	}
	if s.closed || s.failed || !s.initialized || !s.initializedNotification || len(s.pending) != 0 {
		return nil, s.reject()
	}
	s.closed = true
	// A deep copy keeps callers from changing retained source evidence through a
	// RawMessage backing slice after closure.
	exchanges := make([]Exchange, len(s.exchanges))
	for i, exchange := range s.exchanges {
		exchanges[i] = Exchange{Ordinal: exchange.Ordinal, Request: cloneRPCFrame(exchange.Request), Response: cloneRPCFrame(exchange.Response)}
	}
	return exchanges, nil
}

func cloneRPCFrame(f RPCFrame) RPCFrame {
	f.Raw = append(json.RawMessage(nil), f.Raw...)
	f.ID = append(json.RawMessage(nil), f.ID...)
	f.Params = append(json.RawMessage(nil), f.Params...)
	f.Result = append(json.RawMessage(nil), f.Result...)
	f.Error = append(json.RawMessage(nil), f.Error...)
	return f
}
