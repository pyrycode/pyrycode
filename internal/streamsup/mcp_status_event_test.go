package streamsup

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestParser_MCPStatusReplaysCapturedFinalReply(t *testing.T) {
	t.Parallel()

	events := collectEvents(capturedMCPReply(t, "mcp_status"))
	if len(events) != 1 {
		t.Fatalf("event count: got %d, want exactly one MCPStatus — %#v", len(events), events)
	}
	got, ok := events[0].(turnevent.MCPStatus)
	if !ok {
		t.Fatalf("event[0] = %T, want turnevent.MCPStatus", events[0])
	}
	want := turnevent.MCPStatus{Servers: []turnevent.MCPServerStatus{
		{Name: "pyry_approve", Status: "connected", Scope: "dynamic", Version: "dev"},
		{Name: "pyry_files", Status: "connected", Scope: "dynamic", Version: "dev"},
		{
			Name:   "pyry_probe_absent",
			Status: "failed",
			Error:  "ENOENT: no such file or directory, posix_spawn '$RUN_LOCAL_TEMP'",
			Scope:  "dynamic",
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("captured MCPStatus:\n got: %#v\nwant: %#v", got, want)
	}

	assertStructFields(t, reflect.TypeOf(got), []string{"Servers", "DroppedServers"})
	assertStructFields(t, reflect.TypeOf(got.Servers[0]),
		[]string{"Name", "Status", "Error", "Scope", "Version"})
}

func TestParser_MCPStatusShapeGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		line        string
		wantStatus  bool
		wantServers int
	}{
		{
			name:        "top-level success with array",
			line:        mcpStatusLineFixture(t, "success", []any{map[string]any{"name": "alpha"}}),
			wantStatus:  true,
			wantServers: 1,
		},
		{
			name:        "present empty array emits an empty report",
			line:        mcpStatusLineFixture(t, "success", []any{}),
			wantStatus:  true,
			wantServers: 0,
		},
		{
			name: "non-success",
			line: mcpStatusLineFixture(t, "error", []any{map[string]any{"name": "must-not-emit"}}),
		},
		{
			name: "absent subtype",
			line: `{"type":"control_response","response":{"response":{"mcpServers":[]}}}`,
		},
		{
			name: "missing payload key",
			line: `{"type":"control_response","response":{"subtype":"success","response":{}}}`,
		},
		{
			name: "null payload",
			line: `{"type":"control_response","response":{"subtype":"success","response":{"mcpServers":null}}}`,
		},
		{
			name: "non-array payload",
			line: mcpStatusLineFixture(t, "success", map[string]any{"name": "not-an-array"}),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			statuses := collectMCPStatuses(collectEvents(tc.line))
			if got := len(statuses); got != boolCount(tc.wantStatus) {
				t.Fatalf("MCPStatus count: got %d, want %d — %#v", got, boolCount(tc.wantStatus), statuses)
			}
			if tc.wantStatus {
				if got := len(statuses[0].Servers); got != tc.wantServers {
					t.Errorf("server count: got %d, want %d", got, tc.wantServers)
				}
				if statuses[0].Servers == nil {
					t.Errorf("Servers is nil for a present array; [] is a positive empty report")
				}
			}
		})
	}
}

func TestParser_MCPStatusNestedControlStringCannotForgeEvent(t *testing.T) {
	t.Parallel()

	control := mcpStatusLineFixture(t, "success", []any{map[string]any{"name": "nested-forgery"}})
	lineBytes, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"id":   "nested-message",
			"role": "assistant",
			"content": []any{map[string]any{
				"type": "text",
				"text": control,
			}},
		},
	})
	if err != nil {
		t.Fatalf("marshal nested control fixture: %v", err)
	}
	events := collectEvents(string(lineBytes))
	if statuses := collectMCPStatuses(events); len(statuses) != 0 {
		t.Fatalf("nested control string emitted MCPStatus: %#v", statuses)
	}
	if len(events) != 1 {
		t.Fatalf("outer assistant line emitted %d events, want its one ordinary text event", len(events))
	}
	text, ok := events[0].(turnevent.TextChunk)
	if !ok || text.Text != control {
		t.Fatalf("outer line event = %#v, want TextChunk carrying the nested string unchanged", events[0])
	}
}

func TestParser_MCPStatusBoundsServersAndErrors(t *testing.T) {
	t.Parallel()

	exact := strings.Repeat("e", maxMCPStatusError)
	over := strings.Repeat("o", maxMCPStatusError+1)
	midRune := strings.Repeat("m", maxMCPStatusError-1) + "💥tail"
	servers := make([]map[string]any, maxMCPStatusServers+3)
	for i := range servers {
		errorText := fmt.Sprintf("error-%02d", i)
		switch i {
		case 0:
			errorText = exact
		case 1:
			errorText = over
		case 2:
			errorText = midRune
		}
		servers[i] = map[string]any{
			"name":   fmt.Sprintf("server-%02d", i),
			"status": fmt.Sprintf("status-%02d", i),
			"error":  errorText,
			"scope":  fmt.Sprintf("scope-%02d", i),
			"serverInfo": map[string]any{
				"name":    "excluded-server-info-name",
				"version": fmt.Sprintf("version-%02d", i),
			},
			"config": map[string]any{"secret": "excluded-config"},
			"tools":  []any{map[string]any{"name": "excluded-tool"}},
		}
	}

	status := requireOneMCPStatus(t, mcpStatusLineFixture(t, "success", servers))
	if got := len(status.Servers); got != maxMCPStatusServers {
		t.Fatalf("retained servers: got %d, want %d", got, maxMCPStatusServers)
	}
	if got := cap(status.Servers); got > maxMCPStatusServers {
		t.Errorf("retained server capacity: got %d, want <= %d", got, maxMCPStatusServers)
	}
	if status.DroppedServers != 3 {
		t.Errorf("DroppedServers: got %d, want 3", status.DroppedServers)
	}
	for i, server := range status.Servers {
		if want := fmt.Sprintf("server-%02d", i); server.Name != want {
			t.Errorf("server %d Name: got %q, want %q", i, server.Name, want)
		}
		if len(server.Error) > maxMCPStatusError {
			t.Errorf("server %d Error is %d bytes, want <= %d", i, len(server.Error), maxMCPStatusError)
		}
		if !utf8.ValidString(server.Error) {
			t.Errorf("server %d Error is not valid UTF-8: %q", i, server.Error)
		}
	}
	if status.Servers[0].Error != exact {
		t.Errorf("exact-boundary Error changed: got %d bytes, want %d", len(status.Servers[0].Error), len(exact))
	}
	if got := status.Servers[1].Error; got != over[:maxMCPStatusError] {
		t.Errorf("over-boundary Error = %q, want first %d bytes", got, maxMCPStatusError)
	}
	if got, want := status.Servers[2].Error, strings.Repeat("m", maxMCPStatusError-1); got != want {
		t.Errorf("mid-rune Error = %q (%d bytes), want %q (%d bytes)", got, len(got), want, len(want))
	}
}

func TestParser_NonStatusControlRepliesEmitNoMCPStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
	}{
		{
			name: "model-list reply",
			line: `{"type":"control_response","response":{"subtype":"success","response":{"models":[{"value":"sonnet","resolvedModel":"claude-sonnet","displayName":"Sonnet"}]}}}`,
		},
		{
			name: "permission-mode success",
			line: `{"type":"control_response","response":{"subtype":"success","request_id":"permission-success","response":{"mode":"default"}}}`,
		},
		{
			name: "permission-mode rejection",
			line: `{"type":"control_response","response":{"subtype":"error","request_id":"permission-rejected","error":"mode refused"}}`,
		},
		{
			name: "interrupt acknowledgement",
			line: `{"type":"control_response","response":{"subtype":"success","request_id":"interrupt-ack"}}`,
		},
		{name: "captured reconnect acknowledgement", line: capturedMCPReply(t, "mcp_reconnect")},
		{name: "captured toggle acknowledgement", line: capturedMCPReply(t, "mcp_toggle")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if statuses := collectMCPStatuses(collectEvents(tc.line)); len(statuses) != 0 {
				t.Fatalf("non-status control reply emitted MCPStatus: %#v", statuses)
			}
		})
	}
}

func TestParser_MCPStatusKeepsControlResponseLogContentFree(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		line       string
		wantReason string
		wantEvents int
		leaks      []string
	}{
		{
			name:       "success",
			line:       `{"type":"control_response","response":{"subtype":"success","request_id":"request-success-sentinel","response":{"mcpServers":[{"name":"name-success-sentinel","status":"status-success-sentinel","error":"error-success-sentinel","scope":"scope-success-sentinel","serverInfo":{"version":"version-success-sentinel"},"config":{"raw":"raw-response-sentinel"},"tools":[{"name":"tool-success-sentinel"}]}]}}}`,
			wantReason: "ack",
			wantEvents: 1,
			leaks: []string{
				"request-success-sentinel", "name-success-sentinel", "status-success-sentinel",
				"error-success-sentinel", "scope-success-sentinel", "version-success-sentinel",
				"raw-response-sentinel", "tool-success-sentinel",
			},
		},
		{
			name:       "rejection",
			line:       `{"type":"control_response","response":{"subtype":"error","request_id":"request-rejection-sentinel","error":"rejection-error-sentinel","response":{"mcpServers":[{"name":"name-rejection-sentinel"}]}}}`,
			wantReason: "nak",
			leaks:      []string{"request-rejection-sentinel", "rejection-error-sentinel", "name-rejection-sentinel"},
		},
		{
			name:       "status decode failure",
			line:       `{"type":"control_response","response":{"subtype":"success","request_id":"request-decode-sentinel","response":{"mcpServers":{"decoder-payload-sentinel":"not-an-array"}}}}`,
			wantReason: "ack",
			leaks:      []string{"request-decode-sentinel", "decoder-payload-sentinel", "not-an-array"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &logRecorder{}
			var events []turnevent.Event
			p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, slog.New(rec))
			if _, err := p.Write([]byte(tc.line + "\n")); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if got := len(collectMCPStatuses(events)); got != tc.wantEvents {
				t.Errorf("MCPStatus count: got %d, want %d", got, tc.wantEvents)
			}
			all := rec.all()
			if len(all) != 1 {
				t.Fatalf("log count: got %d, want the existing one control-response record — %+v", len(all), all)
			}
			if all[0].msg != controlResponseConsumeMsgFixture {
				t.Errorf("log message: got %q, want %q", all[0].msg, controlResponseConsumeMsgFixture)
			}
			wantAttrs := map[string]string{
				"type":             "control_response",
				"reason":           tc.wantReason,
				"models":           "0",
				"dropped":          "0",
				"levels_dropped":   "0",
				"commands":         "0",
				"commands_dropped": "0",
			}
			if !reflect.DeepEqual(all[0].attrs, wantAttrs) {
				t.Errorf("log attrs: got %v, want exactly %v", all[0].attrs, wantAttrs)
			}
			for _, record := range all {
				for _, leak := range tc.leaks {
					if strings.Contains(record.msg, leak) {
						t.Errorf("log message leaked %q: %q", leak, record.msg)
					}
					for key, value := range record.attrs {
						if strings.Contains(value, leak) {
							t.Errorf("log attr %q leaked %q: %q", key, leak, value)
						}
					}
				}
			}
		})
	}
}

func capturedMCPReply(t *testing.T, subtype string) string {
	t.Helper()
	raw, err := os.ReadFile(mcpStatusCapturePath)
	if err != nil {
		t.Fatalf("read MCP status capture: %v", err)
	}
	var capture mcpStatusCapture
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decode MCP status capture: %v", err)
	}
	requestID := ""
	for _, request := range capture.Requests {
		if request.Subtype == subtype {
			requestID = request.RequestID
		}
	}
	if requestID == "" {
		t.Fatalf("capture has no %s request", subtype)
	}
	reply := ""
	for _, frame := range capture.Frames {
		if frame.RequestID != requestID {
			continue
		}
		if frame.Type != "control_response" || frame.PayloadEncoding != "json-string" {
			t.Fatalf("%s reply frame has type %q and encoding %q", subtype, frame.Type, frame.PayloadEncoding)
		}
		reply = frame.Payload
	}
	if reply == "" {
		t.Fatalf("capture has no reply for %s request %q", subtype, requestID)
	}
	return reply
}

func mcpStatusLineFixture(t *testing.T, subtype string, servers any) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype": subtype,
			"response": map[string]any{
				"mcpServers": servers,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal MCP status line: %v", err)
	}
	return string(line)
}

func collectMCPStatuses(events []turnevent.Event) []turnevent.MCPStatus {
	var statuses []turnevent.MCPStatus
	for _, event := range events {
		if status, ok := event.(turnevent.MCPStatus); ok {
			statuses = append(statuses, status)
		}
	}
	return statuses
}

func requireOneMCPStatus(t *testing.T, line string) turnevent.MCPStatus {
	t.Helper()
	statuses := collectMCPStatuses(collectEvents(line))
	if len(statuses) != 1 {
		t.Fatalf("MCPStatus count: got %d, want 1 — %#v", len(statuses), statuses)
	}
	return statuses[0]
}

func assertStructFields(t *testing.T, typ reflect.Type, want []string) {
	t.Helper()
	got := make([]string, typ.NumField())
	for i := range got {
		got[i] = typ.Field(i).Name
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s fields: got %v, want exactly %v", typ.Name(), got, want)
	}
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}
