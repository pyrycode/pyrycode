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
	if mcpPrivateSafe(raw) != nil {
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
		if !ok || len(stderr) == 0 || len(stderr) > MaxBytes || mcpPrivateSafe([]byte(stderr)) != nil {
			return mcpError()
		}
	}
	return nil
}

// ValidateMCPDaemonInput checks the current source's entire declared command
// document and enforcing flags before replay may start its own MCP clients.
// Otherwise manually starting a server could hide a removed production flag,
// wrong command/socket, ineffective default mode, foreign server or altered config.
func ValidateMCPDaemonInput(b *DaemonBinding) error {
	if b == nil {
		return mcpError()
	}
	if validateMCPFlags(b.Args()) != nil {
		return mcpError()
	}
	raw, e := readMCPCurrentConfig(b.filePaths["--mcp-config"])
	if e != nil {
		return e
	}
	if validateMCPDocument(raw, b.daemon, b.socket) != nil {
		return mcpError()
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

func validateMCPFlags(args []string) error {
	seen := map[string]int{}
	valued := map[string]bool{"--session-id": true, "--resume": true, "--input-format": true, "--output-format": true, "--model": true, "--effort": true, "--permission-mode": true, "--permission-prompt-tool": true, "--settings": true, "--append-system-prompt-file": true, "--mcp-config": true}
	toggles := map[string]bool{"--verbose": true, "--include-partial-messages": true, "--forward-subagent-text": true, "--replay-user-messages": true, "--prompt-suggestions": true, "--strict-mcp-config": true}
	for i := 0; i < len(args); i++ {
		flag := args[i]
		seen[flag]++
		// The actual current daemon emits one legacy skip marker before its
		// explicit default mode. Preserve that observed argv; a later marker,
		// a duplicate or a missing enforcing default remains forbidden.
		if flag == "--dangerously-skip-permissions" {
			if seen[flag] != 1 || seen["--permission-mode"] != 0 {
				return mcpError()
			}
			continue
		}
		if !valued[flag] && !toggles[flag] {
			return mcpError()
		}
		if valued[flag] {
			if i+1 >= len(args) || args[i+1] == "" {
				return mcpError()
			}
			if want, ok := map[string]string{"--permission-mode": "default", "--permission-prompt-tool": "mcp__pyry_approve__approve", "--mcp-config": "${CHILD_FILE:--mcp-config}"}[flag]; ok && args[i+1] != want {
				return mcpError()
			}
			i++
		}
	}
	for _, flag := range []string{"--permission-mode", "--permission-prompt-tool", "--mcp-config", "--strict-mcp-config"} {
		if seen[flag] != 1 {
			return mcpError()
		}
	}
	return nil
}
func validateMCPDocument(raw []byte, daemon, socket string) error {
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
		if e != nil || len(server) != 2 || mcpString(server["command"]) != daemon {
			return mcpError()
		}
		var args []string
		if json.Unmarshal(server["args"], &args) != nil || !reflect.DeepEqual(args, []string{kind, "-pyry-socket", socket}) {
			return mcpError()
		}
	}
	return nil
}
func ValidateMCPTapeInput(tape *Tape) error {
	if tape == nil || validateMCPFlags(tape.Args) != nil {
		return mcpError()
	}
	wrapped, e := mcpRawObject(tape.Files["--mcp-config"])
	if e != nil || len(wrapped) != 2 {
		return mcpError()
	}
	var mode uint32
	if json.Unmarshal(wrapped["mode"], &mode) != nil || mode != 0600 {
		return mcpError()
	}
	return validateMCPDocument(wrapped["content"], "${CHILD_DAEMON}", "${CHILD_SOCKET}")
}
