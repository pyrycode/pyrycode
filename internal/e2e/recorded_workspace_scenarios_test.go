//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func prepareRecordedWorkspace(t *testing.T, name, home string) string {
	switch name {
	case "ordinary-queue-placement", "interrupt-running", "interrupt-thinking", "subagent-text", "hook-banner", "workspace-change", "skill-silence", "confirmed-permission-settings", "default-outside-read", "mcp-reconnect", "permission-context-absent", "permission-context-outside", "native-suggestions":
		dir := filepath.Join(home, "work")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if name == "hook-banner" {
			rig, err := scenario.WriteHookRig(filepath.Join(home, scenario.HookRigDir))
			if err != nil {
				t.Fatal(err)
			}
			if err = scenario.InstallHookUserSettings(home, rig); err != nil {
				t.Fatal(err)
			}
		}
		if name == "skill-silence" {
			scenario.WriteProbeSkill(t, home)
		}
		return dir
	default:
		return home
	}
}
func runTapeWorkspace(t *testing.T, c recordedScenarioContext) {
	dir := filepath.Join(c.Home, "changed-workspace")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	scenario.WorkspaceChange(t, c.Wire, scenario.WorkspaceConfig{Home: c.Home, ConversationID: tapeConversation, Requested: "~/changed-workspace", Resolved: resolved, FirstPrompt: "Reply with a single short word. run=tape-workspace turn=1", SecondPrompt: "Reply with a single short word. run=tape-workspace turn=2", TurnBudget: 120 * time.Second, VerbBudget: 30 * time.Second})
}
func runTapeSkill(t *testing.T, c recordedScenarioContext) {
	scenario.SkillSilence(t, c.Wire, tapeConversation, "Use the pyry-probe skill, then do exactly what it says. run=tape-skill", 180*time.Second, c.DaemonLog)
}
func recordedOutsideReadFixture(t *testing.T, c recordedScenarioContext) (string, string) {
	const token = "70c3e196250bb904a85d624f"
	dir := filepath.Join(c.Home, ".pyry", "test", "handoff-notes")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, tapeConversation+".txt")
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	prompt := "Use the Read tool once to read the file at " + path + ", then reply with its exact contents and nothing else. Do not use any other tool. If the read is denied or fails, do not retry it and reply with the single word blocked."
	if strings.Contains(prompt, token) {
		t.Fatal("outside read prompt disclosed witness")
	}
	return prompt, token
}
func runTapeConfirmedSettings(t *testing.T, c recordedScenarioContext) {
	scenario.ConfirmedPermissionSettings(t, c.Wire, scenario.PermissionSettingsConfig{ConversationID: tapeConversation, SessionID: tapeSession, Model: "haiku", TurnBudget: 120 * time.Second, ReadBudget: 120 * time.Second, ReadFixture: func() (string, string) { return recordedOutsideReadFixture(t, c) }, ChildPID: func() int {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			st, err := control.Status(ctx, c.Socket)
			cancel()
			if err == nil && st.Phase == "running" && st.ChildPID != 0 {
				return st.ChildPID
			}
			time.Sleep(250 * time.Millisecond)
		}
		t.Fatal("no current child for permission settings")
		return 0
	}})
}
func runTapeDefaultRead(t *testing.T, c recordedScenarioContext) {
	prompt, token := recordedOutsideReadFixture(t, c)
	c.Wire.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: tapeConversation, MessageID: "default-outside-read", Text: prompt})
	reply, tools, modal, answered := scenario.OutsideRead(t, c.Wire, tapeConversation, time.Now().Add(120*time.Second))
	scenario.RequireReadWitness(t, reply, token)
	t.Logf("recorded default outside Read: distinct_tools=%d modal=%t answered=%d", len(tools), modal != nil, answered)
}

func runTapeMCPReconnect(t *testing.T, c recordedScenarioContext) {
	command := filepath.Join(c.Home, "reconnect-command")
	scenario.MCPReconnect(t, c.Wire, scenario.MCPReconnectConfig{ConversationID: tapeConversation, TurnBudget: 120 * time.Second, SettleBudget: 150 * time.Second, StatusBudget: 60 * time.Second, ReconnectBudget: 90 * time.Second, Poll: time.Second, Repair: func() {
		if _, err := os.Lstat(command); !os.IsNotExist(err) {
			t.Fatal("MCP target was not deliberately absent")
		}
		if err := os.Symlink(ensurePyryBuilt(t), command); err != nil {
			t.Fatal("MCP target repair failed")
		}
	}})
}

func runTapePermissionContext(t *testing.T, c recordedScenarioContext, outside bool) {
	dir := c.Workdir
	if outside {
		dir = filepath.Join(c.Home, "outside-context")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(dir, "permission-context-witness.txt")
	prompt := "Use the Bash tool once to run exactly `printf hello > permission-context-witness.txt`. Then briefly report the outcome."
	if outside {
		prompt = "Use the Write tool to create a file containing exactly the word hello at " + target + ". Then briefly report the outcome."
	}
	seen := 0
	scenario.PermissionContext(t, c.Wire, scenario.PermissionContextConfig{ConversationID: tapeConversation, Target: target, Prompt: prompt, Outside: outside, SurfaceBudget: 120 * time.Second, TurnBudget: 120 * time.Second, Reconnect: c.Reconnect, RequireAbsent: func() {
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatal("denied permission target exists or absence not verified")
		}
	}, Source: func(kind string) scenario.PermissionContextSource {
		deadline := time.Now().Add(120 * time.Second)
		for time.Now().Before(deadline) {
			rows := c.Source()
			for seen < len(rows) {
				raw := rows[seen].Data
				seen++
				var source scenario.PermissionContextSource
				if json.Unmarshal(raw, &source) != nil {
					t.Fatal("invalid native source observation")
				}
				if source.Type == kind {
					return source
				}
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("required native permission evidence missing")
		return scenario.PermissionContextSource{}
	}})
}

func runTapeNativeSuggestions(t *testing.T, c recordedScenarioContext) {
	prompts := scenario.NativeSuggestionPrompts("tape-native-suggestions")
	scenario.NativeSuggestions(t, c.Wire, scenario.NativeSuggestionConfig{ConversationID: tapeConversation, Prompts: prompts, TurnBudget: 120 * time.Second, Window: 30 * time.Second, CheckNative: func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			source := claudetape.Tape{Frames: c.Source()}
			if source.RequireNativeSuggestion() == nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("native wire suggestion lacked its upstream source")
	}})
}

func runTapeHookBanner(t *testing.T, c recordedScenarioContext) {
	scenario.HookBanner(t, c.Wire, scenario.HookBannerConfig{Home: c.Home, ConversationID: tapeConversation, BlockPrompt: scenario.HookBlockPrompt, LivePrompt: scenario.HookLivePrompt, TurnBudget: 120 * time.Second, SettleBudget: 5 * time.Second, CheckWitness: func(passed bool) {
		if c.Mode != "capture" {
			return
		}
		verdicts := scenario.HookVerdicts(filepath.Join(c.Home, scenario.HookRigDir, "hook-witness.log"))
		if len(verdicts) == 0 || verdicts[0] != "blocked" {
			t.Fatal("hook witness did not begin blocked")
		}
		if passed {
			found := false
			for _, v := range verdicts {
				if v == "passed" {
					found = true
				}
			}
			if !found {
				t.Fatal("unblocked prompt did not pass hook")
			}
		}
	}})
}
