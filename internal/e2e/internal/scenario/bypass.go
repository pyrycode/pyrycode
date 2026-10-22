package scenario

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

type BypassQuestionsConfig struct {
	Home, ConversationID, SessionID string
	Prompt                          func(bool) string
	FreeText, Witness               string
	SurfaceBudget                   time.Duration
	Budget                          time.Duration
	CheckWitness                    func()
}

// BypassQuestions preserves single/multi selection, free text, continuation
// ordering, answer history, ordinary-tool completion and confirmed live posture.
// Source evidence is independently checked by the live observer/tape audit.
func BypassQuestions(t *testing.T, w Wire, c BypassQuestionsConfig) {
	t.Helper()
	if c.FreeText == "" || c.Witness == "" {
		t.Fatal("bypass witnesses must be nonempty")
	}
	for i, multi := range []bool{false, true} {
		w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: fmt.Sprintf("bypass-question-%d", i), Text: c.Prompt(multi)})
		batch := WaitQuestion(t, w.Next, c.ConversationID, c.SurfaceBudget)
		if len(batch.Questions) != 1 || batch.Questions[0].MultiSelect != multi {
			t.Fatal("bypass question lost selection variant")
		}
		options := batch.Questions[0].Options
		values := []string{options[len(options)-1].Label}
		if multi {
			if values[0] == options[len(options)-2].Label {
				t.Fatal("multiselect offered indistinguishable labels")
			}
			values = append(values, options[len(options)-2].Label, c.FreeText)
		}
		after, _ := QuestionBoundary(t, w.Source, c.ConversationID)
		w.Send(protocol.TypeQuestionAnswer, protocol.QuestionAnswerPayload{QuestionBatchID: batch.QuestionBatchID, AnswerToken: fmt.Sprintf("bypass-answer-%d", i), Answers: []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: values}}})
		text := AnsweredTurn(t, w.Next, c.ConversationID, batch.QuestionBatchID, c.Budget, after)
		for _, value := range values {
			if !strings.Contains(strings.ToLower(text), strings.ToLower(value)) {
				t.Fatal("bypass continuation omitted an answer value")
			}
		}
		if !multi {
			_, choice := ChooseAnswers(t, batch)
			RequireChoice(t, text, choice)
		} else {
			var unchosen []string
			for _, option := range options[:len(options)-2] {
				unchosen = append(unchosen, option.Label)
			}
			for _, value := range values[:2] {
				RequireChoice(t, text, Choice{Chosen: value, Unchosen: unchosen})
			}
		}
		RequirePromptAnswerHistory(t, c.Home, c.ConversationID, batch.QuestionBatchID, "answer", c.SessionID)
	}
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "bypass-ordinary-tool", Text: fmt.Sprintf("Use Bash once to run exactly this command: printf '%%s' '%s' > bypass-witness.txt\nThen reply done. Do not ask a question.", c.Witness)})
	CompletedTurn(t, w.Next, c.ConversationID, c.Budget, false, w.Source)
	c.CheckWitness()
	id := w.Send(protocol.TypeRequestSessionSettings, protocol.RequestSessionSettingsPayload{ConversationID: c.ConversationID})
	env := Correlated(t, w.Next, id, 15*time.Second)
	var settings protocol.SessionSettingsPayload
	if env.Type != protocol.TypeSessionSettings || json.Unmarshal(env.Payload, &settings) != nil || settings.SessionID != c.SessionID || settings.PermissionMode != "bypassPermissions" || !settings.YOLO {
		t.Fatal("live child lost bypass posture")
	}
}

// RecordedBypassPrompt fixes the complete schema while leaving the response
// selection dependent on the surfaced options, including multi-select/free text.
func RecordedBypassPrompt(multi bool) string {
	prompt := RecordedQuestionPrompt()
	if multi {
		prompt = strings.Replace(prompt, `"multiSelect":false`, `"multiSelect":true`, 1)
	}
	return strings.Replace(prompt, "After I answer, reply with the label of the option I chose.", "After I answer, reply with each answer value exactly as received, including free text, and nothing else.", 1)
}
