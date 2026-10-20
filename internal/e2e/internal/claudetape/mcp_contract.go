package claudetape

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/pyrycode/pyrycode/internal/e2emcp"
)

// MCPExchange retains whole literal source lines and whole server stderr. It
// carries no environment, proxy capability or process authority. Shape/causal
// validation alone is deliberately insufficient to register a scenario.
type MCPExchange struct {
	Version int               `json:"version"`
	Events  []e2emcp.RPCEvent `json:"events"`
	Stderr  map[string]string `json:"stderr"`
}

func mcpError() error { return errors.New("MCP source exchange or current binding changed") }
func validateMCPFrame(_ e2emcp.Direction, raw []byte) error {
	if Safe(raw) != nil {
		return mcpError()
	}
	_, e := e2emcp.ParseRPC(raw)
	return e
}

// ValidateMCPPrimaryJoin proves every MCP barrier is an actual prefix of the
// complete primary source order. Independent monotone axes are insufficient:
// for example (input=0,stdout=1) cannot exist when source stdin preceded stdout.
// This check is required before a replay scheduler may drive a single primary
// pump. All source frames and all per-connection RPC transitions are validated.
func ValidateMCPPrimaryJoin(x *MCPExchange, tape *Tape) error {
	if x == nil || x.Version != 1 || tape == nil || tape.Validate() != nil || len(x.Events) == 0 || len(x.Events) > e2emcp.MaxRPCFrames || len(x.Stderr) != 2 {
		return mcpError()
	}
	type barrier struct{ input, user, output int }
	prefixes := map[barrier]int{{}: 0}
	current := barrier{}
	for i, f := range tape.Frames {
		if !uniqueSuiteKeys(f.Data) || safeCLIJSON(f.Data) != nil {
			return mcpError()
		}
		switch f.Direction {
		case "stdin":
			current.input++
			if lifecycleTimelineUser(f.Data) {
				current.user++
			}
		case "stdout":
			current.output++
		default:
			return mcpError()
		}
		prefixes[current] = i + 1
	}
	sessions := map[string]*e2emcp.RPCSession{}
	total, position := 0, 0
	for i, event := range x.Events {
		if event.Connection != "mcp-approve" && event.Connection != "mcp-files" {
			return mcpError()
		}
		total += len(event.Raw)
		next, ok := prefixes[barrier{event.NativeInputCount, event.UserOrdinal, event.NativeStdoutCount}]
		if !ok || next < position || event.Sequence != i+1 || total > MaxBytes || validateMCPFrame(event.Direction, []byte(event.Raw)) != nil {
			return mcpError()
		}
		position = next
		session := sessions[event.Connection]
		if session == nil {
			session = e2emcp.NewRPCSession()
			sessions[event.Connection] = session
		}
		if _, e := session.Observe(event.Direction, []byte(event.Raw)); e != nil {
			return mcpError()
		}
	}
	if len(sessions) != 2 {
		return mcpError()
	}
	for kind, session := range sessions {
		if _, e := session.Finish(); e != nil {
			return mcpError()
		}
		stderr, ok := x.Stderr[kind]
		if !ok || len(stderr) == 0 || len(stderr) > MaxBytes || Safe([]byte(stderr)) != nil {
			return mcpError()
		}
	}
	return nil
}

// ValidateMCPDaemonInput checks the current source's entire declared command
// document and enforcing flags before replay may start its own MCP clients.
// Otherwise manually starting a server could hide a removed production flag,
// wrong command/socket, skipped permissions, foreign server or altered config.
func ValidateMCPDaemonInput(b *DaemonBinding) error {
	if b == nil {
		return mcpError()
	}
	seen := map[string]int{}
	for i := 0; i < len(b.args); i++ {
		a := b.args[i]
		seen[a]++
		switch a {
		case "--dangerously-skip-permissions":
			return mcpError()
		case "--permission-mode", "--permission-prompt-tool", "--mcp-config":
			if i+1 >= len(b.args) {
				return mcpError()
			}
			want := map[string]string{"--permission-mode": "default", "--permission-prompt-tool": "mcp__pyry_approve__approve", "--mcp-config": "${CHILD_FILE:--mcp-config}"}[a]
			if b.args[i+1] != want {
				return mcpError()
			}
			i++
		}
	}
	for _, a := range []string{"--permission-mode", "--permission-prompt-tool", "--mcp-config", "--strict-mcp-config"} {
		if seen[a] != 1 {
			return mcpError()
		}
	}
	raw, e := readMCPCurrentConfig(b.filePaths["--mcp-config"])
	if e != nil {
		return e
	}
	root, e := mcpRawObject(raw)
	if e != nil || len(root) != 1 {
		return mcpError()
	}
	servers, e := mcpRawObject(root["mcpServers"])
	if e != nil || len(servers) != 2 {
		return mcpError()
	}
	for name, kind := range map[string]string{"pyry_approve": "mcp-approve", "pyry_files": "mcp-files"} {
		server, e := mcpRawObject(servers[name])
		if e != nil || len(server) != 2 || mcpString(server["command"]) != b.daemon {
			return mcpError()
		}
		var args []string
		if json.Unmarshal(server["args"], &args) != nil || !reflect.DeepEqual(args, []string{kind, "-pyry-socket", b.socket}) {
			return mcpError()
		}
	}
	files, e := b.Files()
	if e != nil {
		return mcpError()
	}
	var wrapped struct {
		Content json.RawMessage `json:"content"`
		Mode    uint32          `json:"mode"`
	}
	normalized, e := b.normalizePaths(raw, false)
	if e != nil || json.Unmarshal(files["--mcp-config"], &wrapped) != nil || wrapped.Mode != 0600 || !bytes.Equal(physicalCanonicalJSON(normalized), physicalCanonicalJSON(wrapped.Content)) {
		return mcpError()
	}
	return nil
}
