package scenario

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

const MCPApproveServer = "pyry_approve"
const MCPFilesServer = "pyry_files"
const MCPReconnectTarget = "pyry_e2e_reconnect_target"

func InjectMCPReconnectServer(path, command string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read mcp config: %w", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("decode mcp config: %w", err)
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(doc["mcpServers"], &servers); err != nil {
		return fmt.Errorf("decode mcpServers: %w", err)
	}
	if _, done := servers[MCPReconnectTarget]; done {
		return nil
	}
	twin, ok := servers[MCPFilesServer]
	if !ok {
		return fmt.Errorf("mcp config has no %q entry to clone", MCPFilesServer)
	}
	var spec struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal(twin, &spec); err != nil {
		return fmt.Errorf("decode %q entry: %w", MCPFilesServer, err)
	}
	spec.Command = command
	clone, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("encode cloned entry: %w", err)
	}
	servers[MCPReconnectTarget] = clone
	if doc["mcpServers"], err = json.Marshal(servers); err != nil {
		return fmt.Errorf("encode mcpServers: %w", err)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode mcp config: %w", err)
	}
	// The daemon created this file at 0600 via os.CreateTemp and it already exists,
	// so the mode argument is a statement of what must remain true rather than the
	// mechanism that makes it so.
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("write mcp config: %w", err)
	}
	return nil
}

type MCPReconnectConfig struct {
	ConversationID                                                string
	TurnBudget, SettleBudget, StatusBudget, ReconnectBudget, Poll time.Duration
	Repair                                                        func()
}

func MCPReconnect(t *testing.T, w Wire, c MCPReconnectConfig) {
	t.Helper()
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "mcp-warm", Text: "Reply with the single word ready and nothing else. Do not use any tool."})
	CompletedTurn(t, w.Next, c.ConversationID, c.TurnBudget, false, w.Source)
	receive := func(id uint64, budget time.Duration) protocol.MCPStatusPayload {
		deadline := time.Now().Add(budget)
		for time.Now().Before(deadline) {
			env := w.Next(deadline)
			if env.InReplyTo == nil || *env.InReplyTo != id {
				continue
			}
			if env.Type == protocol.TypeError {
				t.Fatal("MCP request was refused")
			}
			if env.Type != protocol.TypeMCPStatus {
				continue
			}
			var p protocol.MCPStatusPayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid MCP status reply")
			}
			return p
		}
		t.Fatal("MCP request did not receive its correlated reply")
		return protocol.MCPStatusPayload{}
	}
	row := func(p protocol.MCPStatusPayload, name string) (protocol.MCPServerStatus, bool) {
		for _, v := range p.Servers {
			if v.Name == name {
				return v, true
			}
		}
		return protocol.MCPServerStatus{}, false
	}
	require := func(p protocol.MCPStatusPayload, name string) protocol.MCPServerStatus {
		v, ok := row(p, name)
		if !ok {
			t.Fatal("MCP snapshot dropped a required server")
		}
		return v
	}
	deadline := time.Now().Add(c.SettleBudget)
	var before protocol.MCPStatusPayload
	for {
		id := w.Send(protocol.TypeMCPStatusRequest, protocol.MCPStatusRequestPayload{ConversationID: c.ConversationID})
		before = receive(id, c.StatusBudget)
		a, aok := row(before, MCPApproveServer)
		f, fok := row(before, MCPFilesServer)
		if aok && fok && a.Status == "connected" && f.Status == "connected" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("both daemon MCP servers did not become healthy")
		}
		time.Sleep(c.Poll)
	}
	broken := require(before, MCPReconnectTarget)
	if broken.Status != "failed" || strings.TrimSpace(broken.Error) == "" {
		t.Fatal("deliberately broken MCP target did not report a nonempty failure")
	}
	c.Repair()
	id := w.Send(protocol.TypeMCPReconnect, protocol.MCPReconnectPayload{ConversationID: c.ConversationID, ServerName: MCPReconnectTarget})
	after := receive(id, c.ReconnectBudget)
	repaired := require(after, MCPReconnectTarget)
	if repaired.Status == "failed" {
		t.Fatal("MCP reconnect did not change the failed target row")
	}
	require(after, MCPApproveServer)
	require(after, MCPFilesServer)
}
