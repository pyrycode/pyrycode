package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/questionbridge"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// nextConversation returns the conversation_id of the next frame of type typ
// pushed to bcast, skipping frames of any other type.
func nextConversation(t *testing.T, bcast *chanBcast, typ string) string {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case env := <-bcast.pushed:
			if env.Type != typ {
				continue
			}
			var p struct {
				ConversationID string `json:"conversation_id"`
			}
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode %s: %v", typ, err)
			}
			return p.ConversationID
		case <-deadline:
			t.Fatalf("no %s pushed", typ)
			return ""
		}
	}
}

// newTagBridge builds the bridge as relay.go wires it: questions set, and
// sessionConv resolving through the conversations registry when reg is set.
func newTagBridge(perm *permbridge.Registry, bcast interactiveBroadcaster, cursor func() string, reg *conversations.Registry) *streamApprovalBridge {
	b := newStreamApprovalBridge(perm, modalbridge.New(), bcast, cursor, context.Background(), discardLogger())
	b.questions = questionbridge.New()
	if reg != nil {
		b.sessionConv = func(sid string) (string, bool) { return conversationForSession(reg, sid) }
	}
	return b
}

// startClaudeAsker runs a stream runner in stdio-permission mode over a fake
// child that answers nothing and asks one AskUserQuestion as soon as it has
// read initialize.
func startClaudeAsker(t *testing.T, sessionID string, perm *permbridge.Registry, surface *approvalSurfaceReport) {
	t.Helper()
	dir := t.TempDir()
	ask, err := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": "request-question",
		"request": map[string]any{
			"subtype":                   "can_use_tool",
			"tool_name":                 "AskUserQuestion",
			"tool_use_id":               "tool-use-question",
			"requires_user_interaction": true,
			"input":                     questionInput(t, multiQuestionText, "Write strategy", "rewrite", "replace the file wholesale"),
		},
	})
	if err != nil {
		t.Fatalf("marshal can_use_tool: %v", err)
	}
	askPath := filepath.Join(dir, "ask.json")
	if err := os.WriteFile(askPath, append(ask, '\n'), 0o600); err != nil {
		t.Fatalf("write ask: %v", err)
	}
	childPath := filepath.Join(dir, "fake-claude.sh")
	script := fmt.Sprintf("#!/bin/sh\nIFS= read -r initialize\ncat %q\nexec sleep 3600\n", askPath)
	if err := os.WriteFile(childPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	runner, err := newStreamRunnerFactory(newStreamTurnSink(64, discardLogger()), "", nil, streamApprovalConfig{
		stdio: true, registry: perm, timeout: time.Minute, surface: surface,
	})(sessions.RunnerConfig{ClaudeBin: childPath, WorkDir: dir, SessionID: sessionID, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("newStreamRunnerFactory: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// TestApprovalConversationTag_FollowsParkingSession runs a Claude and a Codex
// session on the fakes, each bound to its own conversation, with the router's
// cursor on the Claude conversation. Each prompt must carry the conversation of
// the session that parked it (#2675), and that tag is what the #2644 gate
// reads to keep a Codex frame from a conn without multi_agent.
func TestApprovalConversationTag_FollowsParkingSession(t *testing.T) {
	const claudeSession = "s-claude"
	codex, _ := newApprovalCodexRunner(t, time.Minute, "")
	codexSession := codex.r.cfg.Tag.ID()

	reg := &conversations.Registry{}
	ts := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	reg.Create(conversations.Conversation{ID: "conv-claude", Cwd: "/w", CurrentSessionID: claudeSession, LastUsedAt: ts})
	reg.Create(conversations.Conversation{ID: "conv-codex", Cwd: "/w", CurrentSessionID: codexSession, LastUsedAt: ts})

	// The session router stamps the cursor on every routed message; the last
	// one went to the Claude conversation.
	cursor := &activeConversation{}
	cursor.set("conv-claude")

	perm := permbridge.New()
	bcast := newChanBcast("c1")
	bridge := newTagBridge(perm, bcast, cursor.CurrentConversation, reg)
	surface := &approvalSurfaceReport{}
	surface.set(bridge.Surface)
	codex.r.cfg.Approvals = newCodexApprovals(perm, time.Minute, surface, codex.r.cfg.Tag)

	codex.run(t)
	codex.bound(t, nil)
	t.Cleanup(func() {
		for _, id := range bridge.parkedToolUseIDs() {
			perm.Resolve(id, permbridge.Deny("test over"))
		}
	})
	codex.turn(t, "[fakecodex:approval]")
	modalConv := nextConversation(t, bcast, protocol.TypeModalShown)

	startClaudeAsker(t, claudeSession, perm, surface)
	questionConv := nextConversation(t, bcast, protocol.TypeQuestionShown)

	if modalConv != "conv-codex" {
		t.Errorf("Codex modal_shown conversation_id = %q, want conv-codex", modalConv)
	}
	if questionConv != "conv-claude" {
		t.Errorf("Claude question_shown conversation_id = %q, want conv-claude", questionConv)
	}

	// The delivery gate withholds a frame from a conn without multi_agent
	// exactly when this seam reports its conversation_id as Codex.
	isCodex := codexConversation(reg, func(sid string) (string, bool) {
		if sid == codexSession {
			return protocol.AgentCodex, true
		}
		return protocol.AgentClaude, true
	})
	if !isCodex(modalConv) {
		t.Errorf("Codex approval tagged %q would reach a conn without multi_agent", modalConv)
	}
	if isCodex(questionConv) {
		t.Errorf("Claude question tagged %q would be withheld from a conn without multi_agent", questionConv)
	}
}

// TestStreamApprovalBridge_ConversationFallsBackToCursor: a request whose
// session resolves to no conversation keeps today's tag, the cursor.
func TestStreamApprovalBridge_ConversationFallsBackToCursor(t *testing.T) {
	t.Parallel()
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "conv-other", Cwd: "/w", CurrentSessionID: "s-other", LastUsedAt: time.Now()})

	cases := []struct {
		name      string
		reg       *conversations.Registry
		sessionID string
	}{
		{"no resolver", nil, "s-other"},
		{"no session", reg, ""},
		{"unbound session such as the bootstrap", reg, "s-bootstrap"},
	}
	for _, tc := range cases {
		for _, surface := range []struct {
			kind, tool, frame string
			input             json.RawMessage
		}{
			{"permission", "Bash", protocol.TypeModalShown, json.RawMessage(`{"command":"ls"}`)},
			{"question", "AskUserQuestion", protocol.TypeQuestionShown, questionInput(t, multiQuestionText, "Write strategy", "rewrite", "replace the file wholesale")},
		} {
			t.Run(tc.name+"/"+surface.kind, func(t *testing.T) {
				t.Parallel()
				bcast := newChanBcast("c1")
				bridge := newTagBridge(permbridge.New(), bcast, func() string { return "conv-cursor" }, tc.reg)
				retire := bridge.Surface(permbridge.Request{
					ToolName: surface.tool, Input: surface.input, ToolUseID: "tool-1", SessionID: tc.sessionID,
				})
				defer retire()
				if got := nextConversation(t, bcast, surface.frame); got != "conv-cursor" {
					t.Errorf("%s conversation_id = %q, want the cursor's conv-cursor", surface.frame, got)
				}
			})
		}
	}
}
