package scenario

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestWorkspaceProtocolControls(t *testing.T) {
	for _, name := range []string{
		"workspace-valid", "workspace-conversation", "workspace-correlation", "workspace-raw-path", "workspace-persistence", "workspace-foreign-reply",
		"skill-valid", "skill-no-token", "skill-no-drop", "skill-leaked-body", "skill-prompt-leak", "skill-parser-gap",
		"subagent-valid", "subagent-no-agent", "subagent-empty-agent", "subagent-parented-agent", "subagent-duplicate-agent", "subagent-foreign-parent", "subagent-main-sequence", "subagent-child-sequence", "subagent-changing-turn", "subagent-same-turn", "subagent-short-main", "subagent-no-child",
		"mcp-valid", "mcp-missing-target", "mcp-empty-error", "mcp-not-failed", "mcp-still-failed", "mcp-after-incomplete", "mcp-refused", "mcp-uncorrelated",
		"settings-valid", "settings-initial-mode", "settings-initial-model", "settings-initial-session", "settings-update-session", "settings-confirmed-model", "settings-confirmed-effort", "settings-respawn-update", "settings-respawn-read", "settings-no-modal", "settings-wrong-tool", "settings-no-token", "settings-prompt-leak",
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkspaceProtocolHelperProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "PYRY_WORKSPACE_CONTROL="+name)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil || (err == nil) != strings.HasSuffix(name, "-valid") {
				t.Fatalf("workspace protocol acceptance err=%v output=%s", err, out)
			}
		})
	}
}
func TestWorkspaceProtocolHelperProcess(t *testing.T) {
	name := os.Getenv("PYRY_WORKSPACE_CONTROL")
	if name == "" {
		return
	}
	const conv = "conv"
	budget := 40 * time.Millisecond
	home := t.TempDir()
	resolved := filepath.Join(home, "changed")
	if err := os.MkdirAll(resolved, 0700); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(home, ".pyry", "test")
	if err := os.MkdirAll(registry, 0700); err != nil {
		t.Fatal(err)
	}
	frame := func(kind string, p any) protocol.Envelope {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.Envelope{Type: kind, Payload: raw}
	}
	var rows []protocol.Envelope
	seq := uint64(0)
	turns := 0
	updated := false
	delta := func(text, parent, turn string, n int) protocol.Envelope {
		return frame(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: conv, Text: text, ParentToolUseID: parent, TurnID: turn, Seq: n})
	}
	idle := func() protocol.Envelope {
		return frame(protocol.TypeTurnState, protocol.TurnStatePayload{ConversationID: conv, State: "idle"})
	}
	correlate := func(kind string, p any) {
		env := frame(kind, p)
		id := seq
		if name == "workspace-correlation" {
			id++
		}
		env.InReplyTo = &id
		rows = append(rows, env)
	}
	w := Wire{Send: func(kind string, p any) uint64 {
		seq++
		switch kind {
		case protocol.TypeSendMessage:
			turns++
			switch {
			case strings.HasPrefix(name, "workspace-"):
				env := delta("hello", "", "main", 0)
				if turns == 2 && name == "workspace-foreign-reply" {
					env = frame(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: "foreign", Text: "hello"})
				}
				rows = append(rows, env, idle())
			case strings.HasPrefix(name, "skill-"):
				text := SkillReplyToken
				if name == "skill-no-token" {
					text = "unrelated"
				}
				if name == "skill-parser-gap" {
					rows = append(rows, frame(protocol.TypeUnrecognizedMessage, protocol.UnrecognizedMessagePayload{Site: "control", MessageType: "unknown"}))
				}
				rows = append(rows, delta(text, "", "main", 0), idle())
			case strings.HasPrefix(name, "subagent-"):
				tool := protocol.ToolUsePayload{ConversationID: conv, Name: "Agent", ToolUseID: "agent"}
				if name == "subagent-empty-agent" {
					tool.ToolUseID = ""
				}
				if name == "subagent-parented-agent" {
					tool.ParentToolUseID = "outer"
				}
				if name != "subagent-no-agent" {
					rows = append(rows, frame(protocol.TypeToolUse, tool))
				}
				if name == "subagent-duplicate-agent" {
					rows = append(rows, frame(protocol.TypeToolUse, tool))
				}
				first := 0
				if name == "subagent-main-sequence" {
					first = 1
				}
				rows = append(rows, delta("main", "", "main-turn", first))
				if name != "subagent-short-main" {
					turn := "main-turn"
					if name == "subagent-changing-turn" {
						turn = "changed"
					}
					rows = append(rows, delta("more main", "", turn, 1))
				}
				if name != "subagent-no-child" {
					parent, turn, n := "agent", "child-turn", 0
					if name == "subagent-foreign-parent" {
						parent = "unknown"
					}
					if name == "subagent-same-turn" {
						turn = "main-turn"
					}
					if name == "subagent-child-sequence" {
						n = 1
					}
					rows = append(rows, delta("child", parent, turn, n))
				}
				rows = append(rows, idle())
			case strings.HasPrefix(name, "mcp-"):
				rows = append(rows, delta("ready", "", "main", 0), idle())
			case strings.HasPrefix(name, "settings-"):
				if turns == 1 {
					rows = append(rows, delta("ready", "", "main", 0), idle())
				} else {
					if name != "settings-no-modal" {
						rows = append(rows, frame(protocol.TypeModalShown, protocol.ModalShownPayload{ConversationID: conv, ModalID: "read-modal", Class: "permission"}))
					}
					tool := "Read"
					if name == "settings-wrong-tool" {
						tool = "Bash"
					}
					rows = append(rows, frame(protocol.TypeToolUse, protocol.ToolUsePayload{ConversationID: conv, Name: tool, ToolUseID: "read"}))
					text := "file-only-token"
					if name == "settings-no-token" {
						text = "unrelated"
					}
					rows = append(rows, delta(text, "", "main", 0), idle())
				}
			}
		case protocol.TypeChangeWorkspace:
			got := p.(protocol.ChangeWorkspacePayload)
			if got.ConversationID != conv || got.Cwd != "~/changed" {
				t.Fatal("workspace request changed")
			}
			cwd := resolved
			if name == "workspace-raw-path" {
				cwd = got.Cwd
			}
			id := conv
			if name == "workspace-conversation" {
				id = "other"
			}
			correlate(protocol.TypeConversationUpdated, protocol.ConversationUpdatedPayload{ID: id, Cwd: cwd})
			stored := resolved
			if name == "workspace-persistence" {
				stored = "wrong"
			}
			raw, _ := json.Marshal(map[string]any{"conversations": []map[string]string{{"id": conv, "cwd": stored}}})
			if err := os.WriteFile(filepath.Join(registry, "conversations.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
		case protocol.TypeMCPStatusRequest, protocol.TypeMCPReconnect:
			status := "failed"
			if kind == protocol.TypeMCPReconnect {
				status = "connected"
			}
			if name == "mcp-still-failed" {
				status = "failed"
			}
			if name == "mcp-not-failed" && kind == protocol.TypeMCPStatusRequest {
				status = "connected"
			}
			report := protocol.MCPStatusPayload{Servers: []protocol.MCPServerStatus{{Name: MCPApproveServer, Status: "connected"}, {Name: MCPFilesServer, Status: "connected"}, {Name: MCPReconnectTarget, Status: status, Error: "command unavailable"}}}
			if name == "mcp-missing-target" {
				report.Servers = report.Servers[:2]
			}
			if name == "mcp-empty-error" {
				report.Servers[2].Error = ""
			}
			if name == "mcp-after-incomplete" && kind == protocol.TypeMCPReconnect {
				report.Servers = report.Servers[1:]
			}
			if kind == protocol.TypeMCPReconnect {
				if p.(protocol.MCPReconnectPayload).ServerName != MCPReconnectTarget {
					t.Fatal("MCP target was read from untrusted status instead of fixed")
				}
			}
			if name == "mcp-refused" {
				correlate(protocol.TypeError, protocol.ErrorPayload{})
			} else if name == "mcp-uncorrelated" {
				env := frame(protocol.TypeMCPStatus, report)
				id := seq + 1
				env.InReplyTo = &id
				rows = append(rows, env)
			} else {
				correlate(protocol.TypeMCPStatus, report)
			}

		case protocol.TypeRequestSessionSettings:
			out := protocol.SessionSettingsPayload{SessionID: "session", PermissionMode: "bypassPermissions", YOLO: true}
			if updated {
				out.Model = "haiku"
				out.PermissionMode = "default"
				out.YOLO = false
				if name == "settings-confirmed-model" {
					out.Model = "wrong"
				}
				if name == "settings-confirmed-effort" {
					out.Effort = "high"
				}
			} else {
				if name == "settings-initial-mode" {
					out.PermissionMode = "default"
				}
				if name == "settings-initial-model" {
					out.Model = "haiku"
				}
				if name == "settings-initial-session" {
					out.SessionID = "wrong"
				}
			}
			correlate(protocol.TypeSessionSettings, out)
		case protocol.TypeSetSessionSettings:
			got := p.(protocol.SetSessionSettingsPayload)
			if got.SessionID != "session" || got.Model == nil || *got.Model != "haiku" || got.PermissionMode == nil || *got.PermissionMode != "default" || got.Effort != nil {
				t.Fatal("settings update changed contract")
			}
			updated = true
			id := "session"
			if name == "settings-update-session" {
				id = "wrong"
			}
			correlate(protocol.TypeSessionSettingsUpdated, protocol.SessionSettingsUpdatedPayload{SessionID: id})
		case protocol.TypeModalAnswer:
			got := p.(protocol.ModalAnswerPayload)
			if got.ModalID != "read-modal" || got.OptionID != "allow_once" || got.AnswerToken != "e2e-2474-posture-probe" {
				t.Fatal("outside-read decision changed")
			}
		}
		return seq
	}, Next: func(deadline time.Time) protocol.Envelope {
		if len(rows) > 0 {
			env := rows[0]
			rows = rows[1:]
			return env
		}
		time.Sleep(time.Until(deadline))
		return protocol.Envelope{}
	}}
	switch {
	case strings.HasPrefix(name, "workspace-"):
		WorkspaceChange(t, w, WorkspaceConfig{Home: home, ConversationID: conv, Requested: "~/changed", Resolved: resolved, FirstPrompt: "first", SecondPrompt: "second", TurnBudget: budget, VerbBudget: budget})
	case strings.HasPrefix(name, "skill-"):
		prompt := "Use the skill"
		if name == "skill-prompt-leak" {
			prompt += SkillReplyToken
		}
		log := SkillDropMessage
		if name == "skill-no-drop" {
			log = "other"
		}
		if name == "skill-leaked-body" {
			log += SkillBodyMarker
		}
		SkillSilence(t, w, conv, prompt, budget, func() string { return log })
	case strings.HasPrefix(name, "subagent-"):
		SubagentText(t, w, conv, "Use one foreground agent", budget)
	case strings.HasPrefix(name, "mcp-"):
		repairs := 0
		MCPReconnect(t, w, MCPReconnectConfig{ConversationID: conv, TurnBudget: budget, SettleBudget: budget, StatusBudget: budget, ReconnectBudget: budget, Poll: time.Millisecond, Repair: func() { repairs++ }})
		if repairs != 1 {
			t.Fatal("MCP repair did not occur exactly once")
		}

	case strings.HasPrefix(name, "settings-"):
		calls := 0
		ConfirmedPermissionSettings(t, w, PermissionSettingsConfig{ConversationID: conv, SessionID: "session", Model: "haiku", TurnBudget: budget, ReadBudget: budget, ChildPID: func() int {
			calls++
			if name == "settings-respawn-update" && calls == 2 || name == "settings-respawn-read" && calls == 3 {
				return 200
			}
			return 100
		}, ReadFixture: func() (string, string) {
			prompt := "Read outside note"
			if name == "settings-prompt-leak" {
				prompt += "file-only-token"
			}
			return prompt, "file-only-token"
		}})
	}
}
