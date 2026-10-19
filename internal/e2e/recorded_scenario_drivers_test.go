//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
	scenariohelpers "github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type recordedScenarioContext struct {
	Checkpoint                  func(claudetape.ClientCheckpoint)
	Source                      func() []claudetape.Frame
	Reconnect                   func()
	Home, Workdir, Socket, Mode string
	Wire                        scenariohelpers.Wire
	Sequence                    *uint64
	DaemonLog                   func() string
}

var recordedScenarioDrivers = map[string]func(*testing.T, recordedScenarioContext){
	"ordinary-queue-placement": runTapeOrdinaryQueue,
	"interrupt-running":        func(t *testing.T, c recordedScenarioContext) { runTapeInterrupt(t, c, false) },
	"interrupt-thinking":       func(t *testing.T, c recordedScenarioContext) { runTapeInterrupt(t, c, true) },
	"subagent-text":            runTapeSubagent,

	"hook-banner":                     runTapeHookBanner,
	"native-suggestions":              runTapeNativeSuggestions,
	"permission-context-absent":       func(t *testing.T, c recordedScenarioContext) { runTapePermissionContext(t, c, false) },
	"permission-context-outside":      func(t *testing.T, c recordedScenarioContext) { runTapePermissionContext(t, c, true) },
	"mcp-reconnect":                   runTapeMCPReconnect,
	"workspace-change":                runTapeWorkspace,
	"skill-silence":                   runTapeSkill,
	"confirmed-permission-settings":   runTapeConfirmedSettings,
	"default-outside-read":            runTapeDefaultRead,
	"family-alias":                    runTapeFamily,
	"attachment-read":                 runTapeAttachment,
	"bypass-questions-mcp-stored":     runTapeBypassQuestions,
	"bypass-questions-mcp-operator":   runTapeBypassQuestions,
	"bypass-questions-stdio-stored":   runTapeBypassQuestions,
	"bypass-questions-stdio-operator": runTapeBypassQuestions,
	"reply":                           runTapeReply,
	"model-announced":                 runTapeModelAnnouncement,
	"model-rejection":                 runTapeModelRejection,
	"tool-turn-clean":                 runTapeToolTurn,
	"send-now-placement":              runTapeSendNowPlacement,
	"question-answer":                 runTapeQuestionAnswer,
	"question-refuse":                 runTapeQuestionRefuse,
	"permission-allow":                runTapePermissionAllow,
	"permission-cancel":               runTapePermissionCancel,
	"two-turn":                        runTapeTwoTurn,
	"permission-deny":                 runTapePermissionDeny,
}

func (c recordedScenarioContext) turn(t *testing.T, text string) {
	t.Helper()
	id := c.Wire.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: tapeConversation, MessageID: fmt.Sprintf("tape-%d", *c.Sequence), Text: text})
	ack := false
	receive := func(deadline time.Time) protocol.Envelope {
		env := c.Wire.Next(deadline)
		if env.Type == protocol.TypeAck && env.InReplyTo != nil && *env.InReplyTo == id {
			ack = true
		}
		return env
	}
	scenariohelpers.CompletedTurn(t, receive, tapeConversation, 150*time.Second, true)
	if !ack {
		t.Fatal("recorded turn lost its matching acknowledgement")
	}
}
func (c recordedScenarioContext) permission(t *testing.T, prompt, option, token string) {
	t.Helper()
	id := c.Wire.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: tapeConversation, MessageID: fmt.Sprintf("tape-%d", *c.Sequence), Text: prompt})
	ack := false
	receive := func(deadline time.Time) protocol.Envelope {
		env := c.Wire.Next(deadline)
		if env.Type == protocol.TypeAck && env.InReplyTo != nil && *env.InReplyTo == id {
			ack = true
		}
		return env
	}
	shown := scenariohelpers.WaitPermission(t, receive, tapeConversation, 150*time.Second)
	if shown.Prompt != "Write" {
		t.Fatal("permission scenario did not surface Write")
	}
	scenariohelpers.ResolvePermission(t, scenariohelpers.Wire{Send: c.Wire.Send, Next: receive}, tapeConversation, shown, option, token, 150*time.Second)
	if !ack {
		t.Fatal("permission trigger lost its matching acknowledgement")
	}
	decision := "deny"
	if option == "allow_once" {
		decision = "allow"
	}
	scenariohelpers.RequirePromptAnswerHistory(t, c.Home, tapeConversation, shown.ModalID, decision, tapeSession)
}

func runTapeReply(t *testing.T, c recordedScenarioContext) {
	t.Helper()

	c.Wire.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: tapeConversation, MessageID: "reply", Text: "Without using tools, briefly describe reliable process supervision."})
	scenariohelpers.CompletedTurn(t, c.Wire.Next, tapeConversation, 120*time.Second, false)
}

func runTapeQuestionAnswer(t *testing.T, c recordedScenarioContext) {
	t.Helper()

	scenariohelpers.QuestionAnswer(t, scenariohelpers.Wire{Send: c.Wire.Send, Next: c.Wire.Next}, scenariohelpers.QuestionConfig{Home: c.Home, ConversationID: tapeConversation, SessionID: tapeSession, HistorySession: tapeSession, Prompt: scenariohelpers.RecordedQuestionPrompt(), Followup: "Without using tools, reply with a short greeting after the model change.", ExpectedModel: "claude-haiku-4-5-20251001", SurfaceBudget: 180 * time.Second, UpdateBudget: 30 * time.Second, TurnBudget: 150 * time.Second})
}

func runTapeQuestionRefuse(t *testing.T, c recordedScenarioContext) {
	t.Helper()

	c.Wire.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: tapeConversation, MessageID: "question-refusal-trigger", Text: scenariohelpers.RecordedRefusalPrompt()})
	batch := scenariohelpers.WaitQuestion(t, c.Wire.Next, tapeConversation, 180*time.Second)
	c.Wire.Send(protocol.TypeQuestionRefused, protocol.QuestionRefusedPayload{QuestionBatchID: batch.QuestionBatchID, AnswerToken: "e2e-1995-refusal-token"})
	scenariohelpers.RefusedTurn(t, scenariohelpers.Wire{Send: c.Wire.Send, Next: c.Wire.Next}, tapeConversation, batch.QuestionBatchID, 150*time.Second)
	scenariohelpers.RequirePromptAnswerHistory(t, c.Home, tapeConversation, batch.QuestionBatchID, "refusal", tapeSession)
}

func runTapePermissionAllow(t *testing.T, c recordedScenarioContext) {
	t.Helper()

	c.permission(t, "Use the Write tool exactly once to create tape-allowed.txt in the current directory. Set file_path to its absolute path and content to exactly hello with no newline. Do not use other tools. After the decision, reply with one short sentence.", "allow_once", "tape-allow")
	if c.Mode == "capture" {
		data, e := os.ReadFile(filepath.Join(c.Home, "tape-allowed.txt"))
		if e != nil || string(data) != "hello" {
			t.Fatal("allowed Write did not create the exact live witness")
		}
	}
}

func runTapePermissionCancel(t *testing.T, c recordedScenarioContext) {
	t.Helper()

	c.Wire.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: tapeConversation, MessageID: "permission-cancel-trigger", Text: scenariohelpers.CancelPermissionPrompt})
	shown := scenariohelpers.WaitPermission(t, c.Wire.Next, tapeConversation, 150*time.Second)
	if shown.Prompt != "Write" {
		t.Fatal("cancellation did not surface Write")
	}
	scenariohelpers.CancelPermission(t, scenariohelpers.Wire{Send: c.Wire.Send, Next: c.Wire.Next}, tapeConversation, shown, 30*time.Second, 60*time.Second)
	scenariohelpers.RequirePromptAnswerHistory(t, c.Home, tapeConversation, shown.ModalID, "cancel", tapeSession)
	if c.Mode == "capture" {
		if _, e := os.Stat(filepath.Join(c.Home, scenariohelpers.CancelPermissionWitness)); !os.IsNotExist(e) {
			t.Fatal("cancelled permission created its live witness")
		}
	}
}

func runTapeTwoTurn(t *testing.T, c recordedScenarioContext) {
	t.Helper()

	c.turn(t, "Without using tools, reply with a short greeting.")
	c.turn(t, "Without using tools, reply with a short farewell.")
}

func runTapePermissionDeny(t *testing.T, c recordedScenarioContext) {
	t.Helper()

	c.permission(t, "Use the Write tool exactly once to create a file named tape-denied.txt containing hello. If denied, do not retry and do not use other tools. Reply with one short sentence after the decision.", "reject_once", "tape-deny")
	if c.Mode == "capture" {
		if _, e := os.Stat(filepath.Join(c.Home, "tape-denied.txt")); !os.IsNotExist(e) {
			t.Fatal("denied tool created its file")
		}
	}
}

func runTapeSendNowPlacement(t *testing.T, c recordedScenarioContext) {
	t.Helper()
	scenariohelpers.SendNowPlacement(t, c.Wire, tapeConversation, scenariohelpers.SendNowPrompt("tape-send-now-placement"), 150*time.Second, scenariohelpers.SendNowSettle)
}

const recordedHaikuModel = "claude-haiku-4-5-20251001"

func runTapeModelAnnouncement(t *testing.T, c recordedScenarioContext) {
	t.Helper()
	scenariohelpers.ModelAnnouncement(t, c.Wire, tapeConversation, "Reply with a single short word. run=tape-model-announced", "haiku", recordedHaikuModel, 150*time.Second, c.DaemonLog)
}
func runTapeModelRejection(t *testing.T, c recordedScenarioContext) {
	t.Helper()
	scenariohelpers.ModelRejection(t, c.Wire, tapeConversation, tapeSession, "Reply with one short word and nothing else. run=tape-model-rejection", recordedHaikuModel, 30*time.Second, 150*time.Second)
}
func runTapeToolTurn(t *testing.T, c recordedScenarioContext) {
	t.Helper()
	scenariohelpers.ToolTurn(t, c.Wire, tapeConversation, "Use the Write tool to create a file called probe.txt containing the single word banana, then use the Read tool to read it back. Reply with one short word. run=tape-tool-turn", 180*time.Second)
}

func runTapeFamily(t *testing.T, c recordedScenarioContext) {
	t.Helper()
	scenariohelpers.ModelFamily(t, c.Wire, tapeConversation, tapeSession, func(step int) string {
		return fmt.Sprintf("Reply with one short word and nothing else. run=tape-family-%d", step)
	}, 30*time.Second, 150*time.Second)
}
func runTapeAttachment(t *testing.T, c recordedScenarioContext) {
	t.Helper()
	scenariohelpers.AttachmentRead(t, c.Wire, scenariohelpers.AttachmentReadConfig{Home: c.Home, ConversationID: tapeConversation, AttachmentID: "20390000-0000-4000-8000-000000000003", Filename: "pyrycode-2039-token.txt", Token: "b7ac9d6e1248053f908c7e4a", Prompt: "Read the attached file and reply with its exact contents and nothing else. run=tape-attachment", Budget: 150 * time.Second})
}
func runTapeBypassQuestions(t *testing.T, c recordedScenarioContext) {
	t.Helper()
	const witness = "tape-bypass-witness"
	scenariohelpers.BypassQuestions(t, c.Wire, scenariohelpers.BypassQuestionsConfig{Home: c.Home, ConversationID: tapeConversation, SessionID: tapeSession, Prompt: scenariohelpers.RecordedBypassPrompt, FreeText: "custom-tape-bypass", Witness: witness, Budget: 150 * time.Second, SurfaceBudget: 180 * time.Second, CheckWitness: func() {
		if c.Mode == "capture" {
			raw, err := os.ReadFile(filepath.Join(c.Home, "bypass-witness.txt"))
			if err != nil || string(raw) != witness {
				t.Fatal("ordinary bypass tool did not write the exact live witness")
			}
		}
	}})
}
