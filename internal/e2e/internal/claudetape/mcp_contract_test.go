package claudetape

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pyrycode/pyrycode/internal/e2emcp"
)

func mcpJoinControl() (*MCPExchange, *Tape) {
	tape := lifecycleSuiteControl().Roles["bootstrap"].Tape
	tape.Frames = []Frame{
		{"stdin", json.RawMessage(`{"type":"control_request","request_id":"control-one","request":{"subtype":"initialize"}}`)},
		{"stdout", json.RawMessage(`{"type":"system","subtype":"init","unknown":{"preserved":true}}`)},
		{"stdin", json.RawMessage(`{"type":"user","message":{"role":"user","content":"controlled input"}}`)},
		{"stdout", json.RawMessage(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"controlled response"}]}}`)},
	}
	x := &MCPExchange{Version: 1, Stderr: map[string]string{"mcp-approve": "complete source stderr A\n", "mcp-files": "complete source stderr B\n"}}
	for _, kind := range []string{"mcp-approve", "mcp-files"} {
		for i, raw := range []string{` {"jsonrpc":"2.0","id":0,"method":"initialize","params":{"unknown":"literal <>&"}} `, `{"jsonrpc":"2.0","id":0,"result":{"unknown":true}}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`} {
			input, user, output := 1, 0, 0
			if kind == "mcp-files" {
				input, user, output = 2, 1, 1
			}
			direction := e2emcp.ClientToServer
			if i == 1 {
				direction = e2emcp.ServerToClient
			}
			x.Events = append(x.Events, e2emcp.RPCEvent{Sequence: len(x.Events) + 1, Connection: kind, Direction: direction, Raw: raw, NativeInputCount: input, UserOrdinal: user, NativeStdoutCount: output})
		}
	}
	return x, tape
}
func TestMCPPrimaryJoinRejectsImpossibleAndForeignAnchors(t *testing.T) {
	for _, mutation := range []string{"valid", "impossible-prefix", "wrong-user-counter", "future-input", "counter-regression", "missing-response", "foreign-connection", "extra-stderr", "missing-stderr", "partial-RPC", "duplicate-native", "duplicate-RPC"} {
		t.Run(mutation, func(t *testing.T) {
			x, tape := mcpJoinControl()
			switch mutation {
			case "impossible-prefix":
				x.Events[0].NativeInputCount = 0
				x.Events[0].NativeStdoutCount = 1
			case "wrong-user-counter":
				x.Events[3].UserOrdinal = 0
			case "future-input":
				x.Events[3].NativeInputCount = 3
			case "counter-regression":
				x.Events[4].NativeInputCount = 1
				x.Events[4].UserOrdinal = 0
				x.Events[4].NativeStdoutCount = 0
			case "missing-response":
				x.Events = x.Events[:4]
			case "foreign-connection":
				x.Events[0].Connection = "mcp-foreign"
			case "extra-stderr":
				x.Stderr["mcp-foreign"] = "foreign\n"
			case "missing-stderr":
				delete(x.Stderr, "mcp-files")
			case "partial-RPC":
				x.Events[0].Raw = `{"jsonrpc":"2.0"`
			case "duplicate-native":
				tape.Frames[0].Data = json.RawMessage(`{"type":"control_request","type":"user"}`)
			case "duplicate-RPC":
				x.Events[0].Raw = `{"jsonrpc":"2.0","jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`
			}
			if (ValidateMCPPrimaryJoin(x, tape) == nil) != (mutation == "valid") {
				t.Fatal("full source join mismatch")
			}
		})
	}
}
func TestMCPCurrentInputMustDeclareExactProductionServersAndEnforcement(t *testing.T) {
	for _, mutation := range []string{"valid", "no-files", "foreign-server", "wrong-command", "wrong-socket", "server-env", "bypass", "no-strict", "duplicate-strict", "wrong-permission-tool", "wrong-mode", "no-config", "public-config", "root-case-alias", "command-case-alias", "args-case-alias", "raw-duplicate"} {
		t.Run(mutation, func(t *testing.T) {
			home := t.TempDir()
			daemon, socket := filepath.Join(home, "pyry"), filepath.Join(home, "socket")
			servers := map[string]any{}
			for name, kind := range map[string]string{"pyry_files": "mcp-files", "pyry_approve": "mcp-approve"} {
				servers[name] = map[string]any{"command": daemon, "args": []string{kind, "-pyry-socket", socket}}
			}
			switch mutation {
			case "no-files":
				delete(servers, "pyry_files")
			case "foreign-server":
				servers["foreign"] = servers["pyry_files"]
			case "wrong-command":
				servers["pyry_files"].(map[string]any)["command"] = filepath.Join(home, "foreign")
			case "wrong-socket":
				servers["pyry_files"].(map[string]any)["args"] = []string{"mcp-files", "-pyry-socket", filepath.Join(home, "foreign")}
			case "server-env":
				servers["pyry_files"].(map[string]any)["env"] = map[string]string{"PYRY_SESSION_ID": "foreign"}
			}
			raw, _ := json.Marshal(map[string]any{"mcpServers": servers})
			switch mutation {
			case "root-case-alias":
				raw = bytes.Replace(raw, []byte(`"mcpServers"`), []byte(`"MCPServers"`), 1)
			case "command-case-alias":
				raw = bytes.Replace(raw, []byte(`"command"`), []byte(`"Command"`), 1)
			case "args-case-alias":
				raw = bytes.Replace(raw, []byte(`"args"`), []byte(`"ARGS"`), 1)
			case "raw-duplicate":
				raw = bytes.Replace(raw, []byte(`"mcpServers":{`), []byte(`"mcpServers":{},"mcpServers":{`), 1)
			}
			args := []string{"--session-id", LifecycleBootstrapSession, "--permission-mode", "default", "--permission-prompt-tool", "mcp__pyry_approve__approve", "--strict-mcp-config"}
			for _, item := range []struct {
				flag string
				raw  []byte
			}{{"--settings", []byte(`{}`)}, {"--append-system-prompt-file", []byte("entire source prompt")}, {"--mcp-config", raw}} {
				path := filepath.Join(home, item.flag[2:])
				if os.WriteFile(path, item.raw, 0600) != nil {
					t.Fatal("source file")
				}
				args = append(args, item.flag, path)
			}
			b, e := NewDaemonBinding(args, home, home, daemon, socket)
			if e != nil {
				t.Fatal(e)
			}
			switch mutation {
			case "bypass":
				b.args = append(b.args, "--dangerously-skip-permissions")
			case "no-strict":
				b.args = append(b.args[:6], b.args[7:]...)
			case "duplicate-strict":
				b.args = append(b.args, "--strict-mcp-config")
			case "wrong-permission-tool":
				b.args[5] = "mcp__foreign__approve"
			case "wrong-mode":
				b.args[3] = "bypassPermissions"
			case "no-config":
				b.args = b.args[:len(b.args)-2]
			case "public-config":
				if os.Chmod(b.filePaths["--mcp-config"], 0644) != nil {
					t.Fatal("mode")
				}
			}
			if (ValidateMCPDaemonInput(b) == nil) != (mutation == "valid") {
				t.Fatal("current production input boundary mismatch")
			}
		})
	}
}
