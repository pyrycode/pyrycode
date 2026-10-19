//go:build e2e_realclaude

package realclaude

// The live question gate shares its protocol driver and durable-history checks
// with captured replay. Live runs additionally establish current Claude semantics.

import (
	"fmt"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// The refusal driver uses the same remote-attribution vocabulary as the answer driver.
const wantQuestionSource = "remote"
const wantQuestionOutcome = "answered"

const questionModelUpdateBudget = 30 * time.Second

// questionSurfaceBudget is the budget for a real clarifying-question batch to
// surface after the trigger send: cold claude (spawn + model load) reaching the
// AskUserQuestion call, `pyry mcp-approve` parking it, questionbridge.Parse
// accepting it and surfaceQuestion broadcasting question_shown. Longer than the
// sibling modalSurfaceBudget because this gate runs under a larger model than the
// modal gates do.
const questionSurfaceBudget = 180 * time.Second

// questionAnswerTrigger drives claude to ask. It is askQuestionCapturePrompt's shape
// with #1938's three constraints carried and one addition, and every one of them is
// load-bearing rather than stylistic:
//
//   - IT NAMES THE TOOL, legitimate for #1938's reason: this slice measures the
//     round trip, not claude's spontaneous propensity to reach for the tool.
//   - IT ASKS FOR SINGLE SELECTION, which is what makes claude emit the multiSelect
//     key at all. questionbridge.Parse REJECTS a question whose key is absent, and a
//     rejected batch falls through to a permission modal this test never answers, so
//     the turn would park until the approval window elapsed. Four options rather
//     than the capture's two widen the space a continuation would have to guess
//     from; both counts sit inside Parse's 2-4 bound.
//   - IT CARRIES NO PATH, NO FILENAME AND NO "in this repo". #1938 needed that for
//     its deny-scan; here it keeps a claude-authored question from quoting the
//     worktree path — under /var/folders/ on macOS — into a salvaged run log.
//   - IT NAMES NO OPTION AND NO PREFERENCE. That is AC 4: the choice must be
//     unpredictable from the prompt. What it does add is the continuation
//     instruction, which cannot leak the choice because the labels do not exist
//     until claude writes them.
//
// The nonce keeps the trigger distinct per run (defeats accidental caching) and is
// never asserted on.
func questionAnswerTrigger(nonce int64) string {
	return fmt.Sprintf("Before writing anything, use the AskUserQuestion tool to ask me one "+
		"clarifying question: for an in-memory key-value cache, which eviction policy should it use? "+
		"Give the question a short header, offer four policies as options with a one-line description "+
		"each, and allow only a single choice. After I answer, reply with only the exact label of the "+
		"option I chose and nothing else — do not write code and do not use any other tool. run=%d", nonce)
}

func TestInteractiveStreamQuestionAnswer(t *testing.T) {
	runParallel(t)
	h, convID := startStreamModalResolutionHarness(t, askQuestionCaptureModel)
	driveSharedQuestionAnswer(t, h, convID)
}

func TestInteractiveStreamStdioQuestionAnswer(t *testing.T) {
	runParallel(t)
	h, convID := startStdioModalResolutionHarness(t, askQuestionCaptureModel)
	driveSharedQuestionAnswer(t, h, convID)
}

func driveSharedQuestionAnswer(t *testing.T, h *perConvHarness, convID string) {
	t.Helper()
	sequence := uint64(1)
	wire := scenario.Wire{Next: scenario.Receive(t, h.phone, h.initRecv), Send: func(kind string, payload any) uint64 {
		sequence++
		sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{ID: sequence, Type: kind, TS: time.Now().UTC(), Payload: mustJSON(t, payload)})
		return sequence
	}}
	nonce := time.Now().UnixNano()
	scenario.QuestionAnswer(t, wire, scenario.QuestionConfig{Home: h.home, ConversationID: convID, SessionID: streamModalBootstrapUUID, Prompt: questionAnswerTrigger(nonce), Followup: fmt.Sprintf("Reply with one short word. run=%d after-model-change", nonce), ExpectedModel: announcedTargetFor(t, "haiku"), SurfaceBudget: questionSurfaceBudget, UpdateBudget: questionModelUpdateBudget, TurnBudget: perTurnReplyBudget})
}

// Compatibility helpers retain the existing live callers on the shared checks.
func raiseRealQuestionBatch(t *testing.T, h *perConvHarness, reqID uint64, convID, triggerPrompt string) protocol.QuestionShownPayload {
	t.Helper()
	sealSendMessage(t, h.phone, h.initSend, reqID, convID, fmt.Sprintf("m-%d", reqID), triggerPrompt)
	return scenario.WaitQuestion(t, scenario.Receive(t, h.phone, h.initRecv), convID, questionSurfaceBudget)
}

type questionChoice struct {
	chosen   string
	unchosen []string
}

func chooseQuestionAnswers(t *testing.T, batch protocol.QuestionShownPayload) ([]protocol.QuestionAnswerEntry, questionChoice) {
	t.Helper()
	entries, choice := scenario.ChooseAnswers(t, batch)
	return entries, questionChoice{chosen: choice.Chosen, unchosen: choice.Unchosen}
}

const questionLabelLogCap = 256
const questionTextLogCap = 2048

func drainAnsweredQuestionTurn(t *testing.T, h *perConvHarness, convID, batchID string, timeout time.Duration) string {
	t.Helper()
	return scenario.AnsweredTurn(t, scenario.Receive(t, h.phone, h.initRecv), convID, batchID, timeout)
}

func requireContinuationNamesChoice(t *testing.T, text string, choice questionChoice) {
	t.Helper()
	scenario.RequireChoice(t, text, scenario.Choice{Chosen: choice.chosen, Unchosen: choice.unchosen})
}
