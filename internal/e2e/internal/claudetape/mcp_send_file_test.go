package claudetape

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2emcp"
)

// These explicitly controlled frames test validation and byte bindings only;
// they are never saved as provider recordings or counted as scenario coverage.
func mcpSendFileControl() (*MCPExchange, *Tape) {
	tape := lifecycleSuiteControl().Roles["bootstrap"].Tape
	tape.Args = append(tape.Args, "--permission-mode", "default", "--permission-prompt-tool", "mcp__pyry_approve__approve", "--strict-mcp-config", "--mcp-config", "${CHILD_FILE:--mcp-config}")
	tape.Files["--mcp-config"] = json.RawMessage(`{"content":{"mcpServers":{"pyry_approve":{"command":"${CHILD_DAEMON}","args":["mcp-approve","-pyry-socket","${CHILD_SOCKET}"]},"pyry_files":{"command":"${CHILD_DAEMON}","args":["mcp-files","-pyry-socket","${CHILD_SOCKET}"]}}},"mode":384}`)
	id, tool := "12345678-1234-4123-8123-123456789012", "toolu_CONTROLLED1"
	text := mcpAttachmentText(id)
	nativeResult, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": tool, "content": []any{map[string]any{"type": "text", "text": text}}, "is_error": false}}}, "tool_use_result": []any{map[string]any{"type": "text", "text": text}}, "unknown": map[string]any{"preserved": "<>&literal"}})
	tape.Frames = []Frame{
		{"stdin", json.RawMessage(`{"type":"user","message":{"role":"user","content":"controlled input"}}`)},
		{"stdout", json.RawMessage(`{"type":"system","subtype":"init","permissionMode":"default","unknown":"retained init metadata"}`)},
		{"stdout", json.RawMessage(`{"type":"assistant","parent_tool_use_id":null,"message":{"role":"assistant","content":[{"type":"tool_use","id":"` + tool + `","name":"mcp__pyry_files__send_file","input":{"path":"` + MCPSendFileName + `"}}]}}`)},
		{"stdout", nativeResult},
		{"stdout", json.RawMessage(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done"}]}}`)},
	}
	start, _ := time.Parse(time.RFC3339Nano, tape.Producer.CapturedAt)
	stamp := "time=" + start.Format(time.RFC3339Nano) + " "
	x := &MCPExchange{Version: 1, Stderr: map[string]string{
		"mcp-approve": stamp + `level=INFO msg="mcp-approve: serving MCP approve tool over stdio"` + "\n" + stamp + `level=INFO msg="mcp-approve: verdict" tool_use_id=` + tool + ` behavior=allow` + "\n",
		"mcp-files":   stamp + `level=INFO msg="mcp-files: serving MCP send_file tool over stdio"` + "\n" + stamp + `level=INFO msg="mcp-files: handed over" attachment_id=` + id + "\n",
	}}
	appendEvent := func(kind string, d e2emcp.Direction, raw string, input, user, output int) {
		x.Events = append(x.Events, e2emcp.RPCEvent{Sequence: len(x.Events) + 1, Connection: kind, Direction: d, Raw: raw, NativeInputCount: input, UserOrdinal: user, NativeStdoutCount: output})
	}
	for _, kind := range []string{"mcp-approve", "mcp-files"} {
		appendEvent(kind, e2emcp.ClientToServer, ` {"jsonrpc":"2.0","id":0,"method":"initialize","params":{"unknown":"literal <>&"}} `, 0, 0, 0)
		appendEvent(kind, e2emcp.ServerToClient, `{"jsonrpc":"2.0","id":0,"result":{"unknown":true}}`, 0, 0, 0)
		appendEvent(kind, e2emcp.ClientToServer, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, 0, 0, 0)
	}
	appendEvent("mcp-approve", e2emcp.ClientToServer, ` {"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"approve","arguments":{"tool_name":"mcp__pyry_files__send_file","input":{"path":"`+MCPSendFileName+`"},"tool_use_id":"`+tool+`"},"unknown":"request <>&"}} `, 1, 1, 2)
	verdict := `{"behavior":"allow","updatedInput":{"path":"` + MCPSendFileName + `"}}`
	result, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": verdict}}, "isError": false})
	appendEvent("mcp-approve", e2emcp.ServerToClient, `{"jsonrpc":"2.0","id":1,"result":`+string(result)+`}`, 1, 1, 2)
	appendEvent("mcp-files", e2emcp.ClientToServer, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_file","arguments":{"path":"`+MCPSendFileName+`"}}}`, 1, 1, 2)
	encodedText, _ := json.Marshal(text)
	appendEvent("mcp-files", e2emcp.ServerToClient, ` { "jsonrpc":"2.0", "id":1,"result":{"content":[{"type":"text","text":`+string(encodedText)+`}],"isError":false,"unknown":{"literal":"<>&\u0041"}} } `, 1, 1, 2)
	return x, tape
}
func TestMCPSendFileSourceRejectsForeignBusinessJoinsAndLogs(t *testing.T) {
	for _, mutation := range []string{"valid", "foreign-tool-id", "foreign-input", "wrong-native-tool", "child-native-tool", "native-early-result", "RPC-before-native-tool", "refused-result", "unknown-uuid-carrier", "foreign-stderr-id", "additional-log", "stale-timestamp", "missing-native-result", "bypass-native-init", "missing-native-init"} {
		t.Run(mutation, func(t *testing.T) {
			x, tape := mcpSendFileControl()
			replaceEvent := func(i int, old, new string) { x.Events[i].Raw = strings.Replace(x.Events[i].Raw, old, new, 1) }
			switch mutation {
			case "foreign-tool-id":
				replaceEvent(6, "toolu_CONTROLLED1", "toolu_FOREIGN")
			case "foreign-input":
				replaceEvent(6, MCPSendFileName, "foreign.txt")
			case "wrong-native-tool":
				tape.Frames[2].Data = bytes.Replace(tape.Frames[2].Data, []byte(mcpSendFileTool), []byte("Bash"), 1)
			case "child-native-tool":
				tape.Frames[2].Data = bytes.Replace(tape.Frames[2].Data, []byte(`"parent_tool_use_id":null`), []byte(`"parent_tool_use_id":"foreign-child"`), 1)
			case "native-early-result":
				x.Events[9].NativeStdoutCount = 3
			case "RPC-before-native-tool":
				for i := 6; i < len(x.Events); i++ {
					x.Events[i].NativeStdoutCount = 0
				}
			case "refused-result":
				replaceEvent(9, "the file was handed", "the file was refused")
			case "unknown-uuid-carrier":
				tape.Frames[3].Data = bytes.Replace(tape.Frames[3].Data, []byte(`"\u003c\u003e\u0026literal"`), []byte(`"12345678-1234-4123-8123-123456789012"`), 1)
			case "foreign-stderr-id":
				x.Stderr["mcp-files"] = strings.ReplaceAll(x.Stderr["mcp-files"], "12345678-1234-4123-8123-123456789012", "ffffffff-ffff-4fff-8fff-ffffffffffff")
			case "additional-log":
				x.Stderr["mcp-files"] += "unknown log\n"
			case "stale-timestamp":
				x.Stderr["mcp-files"] = strings.ReplaceAll(x.Stderr["mcp-files"], "time=", "time=2000-01-01T00:00:00Z-")
			case "missing-native-result":
				tape.Frames = append(tape.Frames[:3], tape.Frames[4:]...)
			case "bypass-native-init":
				tape.Frames[1].Data = bytes.Replace(tape.Frames[1].Data, []byte(`"permissionMode":"default"`), []byte(`"permissionMode":"bypassPermissions"`), 1)
			case "missing-native-init":
				tape.Frames[1].Data = json.RawMessage(`{"type":"system","subtype":"unknown"}`)
			}
			_, e := ValidateMCPSendFile(x, tape)
			if (e == nil) != (mutation == "valid") {
				t.Fatal("business source join boundary mismatch", e)
			}
		})
	}
}
func seedMCPUnitEffect(t *testing.T, home, conv, id string) {
	t.Helper()
	if os.Chmod(home, 0700) != nil {
		t.Fatal("private unit HOME")
	}
	for _, dir := range []string{filepath.Join(home, "work"), filepath.Join(home, ".pyry", "test", "conversations", conv, "attachments", id)} {
		if os.MkdirAll(dir, 0700) != nil {
			t.Fatal("unit setup")
		}
	}
	for _, path := range []string{filepath.Join(home, "work", MCPSendFileName), filepath.Join(home, ".pyry", "test", "conversations", conv, "attachments", id, MCPSendFileName)} {
		if os.WriteFile(path, []byte(MCPSendFileBody), 0600) != nil {
			t.Fatal("unit setup")
		}
	}
}
func TestMCPSendFileRequiresRealCurrentCopyAndPreservesUnrelatedJSONBytes(t *testing.T) {
	const current = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	const conv = "55555555-5555-4555-8555-555555555555"
	for _, mutation := range []string{"valid", "missing-copy", "wrong-bytes", "wrong-conversation", "unknown-response-change", "duplicate-source-id-carrier"} {
		t.Run(mutation, func(t *testing.T) {
			x, tape := mcpSendFileControl()
			facts, e := ValidateMCPSendFile(x, tape)
			if e != nil {
				t.Fatal(e)
			}
			home := t.TempDir()
			if mutation != "missing-copy" {
				seedMCPUnitEffect(t, home, conv, current)
			}
			if mutation == "wrong-bytes" {
				if os.WriteFile(filepath.Join(home, ".pyry", "test", "conversations", conv, "attachments", current, MCPSendFileName), []byte("foreign"), 0600) != nil {
					t.Fatal("mutation")
				}
			}
			event := x.Events[facts.FileResponseSequence-1]
			actual, e := e2emcp.BindJSONString([]byte(event.Raw), []string{"result", "content", "0", "text"}, mcpAttachmentText(facts.AttachmentID), mcpAttachmentText(current))
			if e != nil {
				t.Fatal(e)
			}
			if mutation == "unknown-response-change" {
				actual = bytes.Replace(actual, []byte(`"<>&\u0041"`), []byte(`"foreign"`), 1)
			}
			if mutation == "duplicate-source-id-carrier" {
				actual = bytes.Replace(actual, []byte(`"<>&\u0041"`), []byte(`"`+facts.AttachmentID+`"`), 1)
			}
			driving := conv
			if mutation == "wrong-conversation" {
				driving = LifecycleBootstrapSession
			}
			got, e := MatchMCPSendFileResponse(event, actual, facts, home, driving)
			if (e == nil) != (mutation == "valid") {
				t.Fatal("actual response/copy boundary mismatch", e)
			}
			if mutation != "valid" {
				return
			}
			if got != current || !bytes.Contains(actual, []byte(`"<>&\u0041"`)) || actual[0] != ' ' || actual[len(actual)-1] != ' ' {
				t.Fatal("full literal RPC bytes changed")
			}
			transformed, e := ReplayMCPSendFileOutput(tape.Frames[3].Data, 3, facts, current)
			if e != nil || bytes.Contains(transformed, []byte(facts.AttachmentID)) || !bytes.Contains(transformed, []byte(current)) || !bytes.Contains(transformed, []byte(`"unknown":{"preserved":"\u003c\u003e\u0026literal"}`)) {
				t.Fatal("typed native binding changed unrelated fields", e)
			}
			unchanged, e := ReplayMCPSendFileOutput(tape.Frames[4].Data, 4, facts, current)
			if e != nil || !bytes.Equal(unchanged, tape.Frames[4].Data) {
				t.Fatal("unrelated output changed")
			}
		})
	}
}
