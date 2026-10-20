package e2emcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func observeRPC(t *testing.T, s *RPCSession, d Direction, raw string) RPCFrame {
	t.Helper()
	frame, err := s.Observe(d, []byte(raw))
	if err != nil {
		t.Fatal("accepted MCP producer envelope rejected", err)
	}
	return frame
}
func initializedRPC(t *testing.T) *RPCSession {
	t.Helper()
	s := NewRPCSession()
	observeRPC(t, s, ClientToServer, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"controlled-client","version":"1"}}}`)
	observeRPC(t, s, ServerToClient, `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"pyry_files","version":"dev"}}}`)
	observeRPC(t, s, ClientToServer, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return s
}
func TestRPCSessionRetainsWholeLiteralDuplexExchanges(t *testing.T) {
	s := initializedRPC(t)
	first := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	second := `{"jsonrpc":"2.0","id":"call-A","method":"tools/call","params":{"name":"send_file","arguments":{"path":"controlled.txt"},"_meta":{"progressToken":"progress-A"}}}`
	firstResponse := `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"send_file","description":"distinct complete description","inputSchema":{"type":"object","properties":{"path":{"type":"string"}}}}]}}`
	secondResponse := `{"jsonrpc":"2.0","id":"call-A","result":{"content":[{"type":"text","text":"controlled result"}],"isError":false}}`
	frame := observeRPC(t, s, ClientToServer, first)
	frame.Raw[0] = 'x'
	frame.ID[0] = '9'
	frame.Params[0] = 'x'
	observeRPC(t, s, ClientToServer, second)
	// Correlation is independent of completion order, which is retained literally.
	observeRPC(t, s, ServerToClient, secondResponse)
	returned := observeRPC(t, s, ServerToClient, firstResponse)
	returned.Result[0] = 'x'
	returned.Raw[0] = 'x'
	exchanges, err := s.Finish()
	if err != nil || len(exchanges) != 3 {
		t.Fatal("complete duplex RPC ledger", err)
	}
	if exchanges[1].Ordinal != 3 || string(exchanges[1].Request.Raw) != second || string(exchanges[1].Response.Raw) != secondResponse || exchanges[2].Ordinal != 2 || string(exchanges[2].Request.Raw) != first || string(exchanges[2].Response.Raw) != firstResponse {
		t.Fatal("correlation or complete nested/literal payload changed")
	}
	exchanges[2].Request.Raw[0] = 'x'
	if string(s.exchanges[2].Request.Raw) != first {
		t.Fatal("closed evidence aliases caller mutation")
	}
	if _, err := s.Observe(ClientToServer, []byte(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)); err == nil {
		t.Fatal("post-finish frame accepted")
	}
}
func TestRPCParserRejectsAmbiguousOrUnframedData(t *testing.T) {
	for _, raw := range []string{
		`{"jsonrpc":"2.0","id":null,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":true,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":1.0,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":1e1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":-0,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":9007199254740992,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":"","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":"bad\nid","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":"\ud800","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":null}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":[]}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","id":2}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"arguments":{"path":"first","path":"second"}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","unknown":true}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","result":{}}`,
		`{"jsonrpc":"2.0","id":1,"result":{},"error":null}`,
		`{"jsonrpc":"2.0","id":1,"result":{},"params":{}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":null,"message":"invalid"}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":null}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"invalid","extra":true}}`,
		`{"jsonrpc":"2.0","id":1,"error":null}`,
		`{"jsonrpc":"2.0","result":{}}`,
		`{"jsonrpc":"2.0","id":1}`, `[]`, `null`,
		`{"jsonrpc":"2.0","method":"tools/list"}{}`,
		"{\"jsonrpc\":\"2.0\",\n\"id\":1,\"method\":\"tools/list\"}",
	} {
		t.Run(raw[:min(len(raw), 65)], func(t *testing.T) {
			if _, err := ParseRPC([]byte(raw)); err == nil {
				t.Fatal("ambiguous MCP envelope accepted")
			}
		})
	}
	tooLarge := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"text":"` + strings.Repeat("x", MaxFrameBytes) + `"}}`)
	if _, err := ParseRPC(tooLarge); err == nil {
		t.Fatal("oversized MCP frame accepted")
	}
	badUTF8 := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"text":"X"}}`)
	badUTF8[bytes.IndexByte(badUTF8, 'X')] = 0xff
	if _, err := ParseRPC(badUTF8); err == nil {
		t.Fatal("invalid UTF-8 was repaired before selection")
	}
}
func TestRPCSessionRejectsForeignDuplicateAndUnclosedCorrelations(t *testing.T) {
	for _, mutation := range []string{"foreign-response", "duplicate-response", "duplicate-after-response", "wrong-ID-type", "client-response", "server-request", "reinitialize", "repeat-notification", "unknown-method", "pending-at-finish", "sticky-failure"} {
		t.Run(mutation, func(t *testing.T) {
			s := initializedRPC(t)
			observeRPC(t, s, ClientToServer, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
			var err error
			switch mutation {
			case "foreign-response":
				_, err = s.Observe(ServerToClient, []byte(`{"jsonrpc":"2.0","id":2,"result":{}}`))
			case "duplicate-response":
				observeRPC(t, s, ServerToClient, `{"jsonrpc":"2.0","id":1,"result":{}}`)
				_, err = s.Observe(ServerToClient, []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
			case "duplicate-after-response":
				observeRPC(t, s, ServerToClient, `{"jsonrpc":"2.0","id":1,"result":{}}`)
				_, err = s.Observe(ClientToServer, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			case "wrong-ID-type":
				_, err = s.Observe(ServerToClient, []byte(`{"jsonrpc":"2.0","id":"1","result":{}}`))
			case "client-response":
				_, err = s.Observe(ClientToServer, []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
			case "server-request":
				_, err = s.Observe(ServerToClient, []byte(`{"jsonrpc":"2.0","id":9,"method":"tools/list"}`))
			case "reinitialize":
				_, err = s.Observe(ClientToServer, []byte(`{"jsonrpc":"2.0","id":2,"method":"initialize"}`))
			case "repeat-notification":
				_, err = s.Observe(ClientToServer, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
			case "unknown-method":
				_, err = s.Observe(ClientToServer, []byte(`{"jsonrpc":"2.0","id":2,"method":"future-method"}`))
			case "pending-at-finish":
				_, err = s.Finish()
			case "sticky-failure":
				s.Observe(ServerToClient, []byte(`{"jsonrpc":"2.0","id":2,"result":{}}`))
				_, err = s.Observe(ServerToClient, []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
			}
			if err == nil {
				t.Fatal("unclosed or unowned MCP correlation accepted", mutation)
			}
		})
	}
}
func TestRPCSessionDoesNotAliasOriginalSourceOrReturnedFields(t *testing.T) {
	s := initializedRPC(t)
	raw := []byte(`{"jsonrpc":"2.0","id":"current-id","method":"tools/list","params":{"distinct":"payload"}}`)
	before := append([]byte(nil), raw...)
	f, err := s.Observe(ClientToServer, raw)
	if err != nil {
		t.Fatal(err)
	}
	for i := range raw {
		raw[i] = 'x'
	}
	for i := range f.Raw {
		f.Raw[i] = 'y'
	}
	for i := range f.Params {
		f.Params[i] = 'z'
	}
	observeRPC(t, s, ServerToClient, `{"jsonrpc":"2.0","id":"current-id","result":{}}`)
	got, err := s.Finish()
	if err != nil || !bytes.Equal(got[1].Request.Raw, before) {
		t.Fatal("mutable caller changed the retained source")
	}
	var payload map[string]string
	if json.Unmarshal(got[1].Request.Params, &payload) != nil || payload["distinct"] != "payload" {
		t.Fatal("source params alias mutable caller")
	}
}
