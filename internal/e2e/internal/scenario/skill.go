package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

const SkillName = "pyry-probe"
const SkillBodyMarker = "pyry-2087-skill-body-marker-do-not-repeat"
const SkillReplyToken = "pyry-2087-ack"
const SkillDropMessage = "streamsup: dropping known harness user block"

func WriteProbeSkill(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "skills", SkillName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir skill dir: %v", err)
	}
	body := "---\n" +
		"name: " + SkillName + "\n" +
		"description: Pyrycode end-to-end probe skill. Use this skill whenever the user asks " +
		"you to use the " + SkillName + " skill. It asks only for a short spoken reply.\n" +
		"---\n\n" +
		"# Pyrycode probe skill\n\n" +
		"This document exists so that an automated test can observe what happens on the\n" +
		"daemon's stream when a skill is loaded. It asks for nothing that touches the\n" +
		"filesystem, the network, or any tool.\n\n" +
		"Internal identifier: " + SkillBodyMarker + "\n\n" +
		"When this skill is invoked, reply with the single word " + SkillReplyToken + " and\n" +
		"nothing else. Never repeat the internal identifier above in your reply.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("realclaude: write SKILL.md: %v", err)
	}
}

// SkillSilence requires both a file-only reply token and the producer drop witness.
func SkillSilence(t *testing.T, w Wire, convID, prompt string, budget time.Duration, log func() string) {
	t.Helper()
	if strings.Contains(prompt, SkillReplyToken) || strings.Contains(prompt, SkillBodyMarker) {
		t.Fatal("skill prompt disclosed a file-only witness")
	}
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: convID, MessageID: "skill-probe", Text: prompt})
	reply := CompletedTurn(t, w.Next, convID, budget, false, w.Source)
	if !strings.Contains(reply, SkillReplyToken) {
		t.Fatal("skill reply did not contain its file-only token")
	}
	raw := log()
	if !strings.Contains(raw, SkillDropMessage) {
		t.Fatal("skill turn had no known-harness drop witness")
	}
	if strings.Contains(raw, SkillBodyMarker) {
		t.Fatal("skill body marker leaked into daemon log")
	}
}
