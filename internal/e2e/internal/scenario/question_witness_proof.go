package scenario

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"strings"
	"testing"
)

func ChooseWitnessAnswers(t *testing.T, batch protocol.QuestionShownPayload, multiple bool, witness string) []protocol.QuestionAnswerEntry {
	t.Helper()
	var entries []protocol.QuestionAnswerEntry
	for i, q := range batch.Questions {
		if len(q.Options) == 0 {
			t.Fatal("question offers no options")
		}
		selected := q.Options[len(q.Options)-1].Label
		values := []string{selected}
		if multiple {
			if !q.MultiSelect || len(q.Options) < 2 || selected == q.Options[len(q.Options)-2].Label {
				t.Fatal("question cannot prove multiple selections")
			}
			values = append(values, q.Options[len(q.Options)-2].Label)
			selected = strings.Join(values, ", ")
		}
		if i == 0 {
			answer := fmt.Sprintf("%s for the cache named %s. In your reply, confirm this selection and include the answer witness verbatim: %s", selected, witness, witness)
			if multiple {
				values = append(values, answer)
			} else {
				values[0] = answer
			}
		}
		entries = append(entries, protocol.QuestionAnswerEntry{QuestionIndex: i, Values: values})
	}
	return entries
}

func CheckQuestionWitness(witness string, batch protocol.QuestionShownPayload, trigger, prior string) error {
	raw, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	if witness == "" || strings.Contains(trigger, witness) || strings.Contains(prior, witness) || strings.Contains(string(raw), witness) {
		return errors.New("answer witness is empty or already present before answering")
	}
	return nil
}

func ObserveWitnessAnswer(env protocol.Envelope, convID, batchID string, text *strings.Builder, dismissed, idle *bool) error {
	switch env.Type {
	case protocol.TypeQuestionDismissed:
		var d protocol.QuestionDismissedPayload
		if err := json.Unmarshal(env.Payload, &d); err != nil {
			return err
		}
		if d.QuestionBatchID != batchID || d.Source != "remote" || d.Outcome != "answered" {
			return errors.New("question dismissal does not match original remote answer")
		}
		*dismissed = true
	case protocol.TypeAssistantDelta:
		var d protocol.AssistantDeltaPayload
		if err := json.Unmarshal(env.Payload, &d); err != nil {
			return err
		}
		if d.ConversationID == convID {
			text.WriteString(d.Text)
			if d.Text != "" {
				*idle = false
			}
		}
	case protocol.TypeTurnState:
		var st protocol.TurnStatePayload
		if err := json.Unmarshal(env.Payload, &st); err != nil {
			return err
		}
		if st.ConversationID == convID {
			*idle = st.State == "idle"
		}
	}
	return nil
}

func WitnessAnswerComplete(witness, text string, dismissed, idle bool) bool {
	return dismissed && idle && witness != "" && strings.Contains(text, witness)
}

func CheckQuestionAnswerHistory(e history.Entry, batch protocol.QuestionShownPayload, answers []protocol.QuestionAnswerEntry, session string) error {
	var fact struct {
		ConversationID string `json:"conversation_id"`
		CorrelationID  string `json:"correlation_id"`
		SessionID      string `json:"session_id"`
		Truncated      bool   `json:"truncated"`
		Context        struct {
			Questions []struct {
				Index       int    `json:"index"`
				Text        string `json:"text"`
				MultiSelect bool   `json:"multi_select"`
				Values      []struct {
					Text string `json:"text"`
				} `json:"values"`
			} `json:"questions"`
		} `json:"context"`
	}
	if err := json.Unmarshal(e.Payload, &fact); err != nil {
		return fmt.Errorf("decode answer context: %w", err)
	}
	if fact.ConversationID != batch.ConversationID || fact.CorrelationID != batch.QuestionBatchID || fact.SessionID != session || fact.Truncated || e.Session == nil || e.Session.Kind != "claude" || e.Session.SessionID != session {
		return errors.New("answer context lost original conversation, batch or asking session")
	}
	if len(fact.Context.Questions) != len(batch.Questions) || len(answers) != len(batch.Questions) {
		return errors.New("answer context question count mismatch")
	}
	for i, q := range batch.Questions {
		saved, sent := fact.Context.Questions[i], answers[i]
		if saved.Index != i || saved.Text != q.Text || saved.MultiSelect != q.MultiSelect || sent.QuestionIndex != i || len(saved.Values) != len(sent.Values) {
			return fmt.Errorf("answer context question %d mismatch", i)
		}
		for j, value := range sent.Values {
			if saved.Values[j].Text != value {
				return fmt.Errorf("answer context question %d value %d mismatch", i, j)
			}
		}
	}
	return nil
}

func CheckWitnessQuestionControl(env protocol.Envelope, parked *protocol.QuestionShownPayload) error {
	if env.Type == protocol.TypeQuestionDismissed {
		return errors.New("question resolved before the answer was sent")
	}
	if parked != nil && env.Type == protocol.TypeQuestionShown {
		var repeated protocol.QuestionShownPayload
		if json.Unmarshal(env.Payload, &repeated) != nil || repeated.QuestionBatchID != parked.QuestionBatchID || repeated.ConversationID != parked.ConversationID {
			return errors.New("parked question was replaced or malformed during model update")
		}
	}
	return nil
}
