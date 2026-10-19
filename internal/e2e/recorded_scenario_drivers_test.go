//go:build e2e

package e2e

import (
	"fmt"
	scenariohelpers "github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type recordedScenarioContext struct {
	Home, Mode string
	Wire       scenariohelpers.Wire
	Sequence   *uint64
}

var recordedScenarioDrivers = map[string]func(*testing.T, recordedScenarioContext){
	"reply":             runTapeReply,
	"question-answer":   runTapeQuestionAnswer,
	"question-refuse":   runTapeQuestionRefuse,
	"permission-allow":  runTapePermissionAllow,
	"permission-cancel": runTapePermissionCancel,
	"two-turn":          runTapeTwoTurn,
	"permission-deny":   runTapePermissionDeny,
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
