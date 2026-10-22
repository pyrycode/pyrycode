package scenario

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func NewQuestionWitness(t *testing.T) string {
	t.Helper()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("answer-witness-%x", random)
}
func AwaitWitnessQuestionControl(t *testing.T, next Next, want string, budget time.Duration, prior *strings.Builder, parked *protocol.QuestionShownPayload) protocol.Envelope {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		env := next(deadline)
		alarm(t, env)
		if err := CheckWitnessQuestionControl(env, parked); err != nil {
			t.Fatal(err)
		}
		if env.Type == protocol.TypeAssistantDelta {
			t.Fatal("retired pre-answer text delivered on wire")
		}
		if env.Type == want {
			return env
		}
	}
	t.Fatalf("no %s within %s", want, budget)
	return protocol.Envelope{}
}
func RaiseWitnessQuestion(t *testing.T, w Wire, conv, messageID, prompt string, budget time.Duration, prior *strings.Builder) protocol.QuestionShownPayload {
	t.Helper()
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: conv, MessageID: messageID, Text: prompt})
	env := AwaitWitnessQuestionControl(t, w.Next, protocol.TypeQuestionShown, budget, prior, nil)
	var batch protocol.QuestionShownPayload
	if json.Unmarshal(env.Payload, &batch) != nil || batch.ConversationID != conv || batch.QuestionBatchID == "" || len(batch.Questions) == 0 || len(batch.Questions[0].Options) < 2 {
		t.Fatal("question batch is empty, unanswerable or belongs to another conversation")
	}
	return batch
}
func WitnessAnsweredTurn(t *testing.T, next Next, conv, batch, witness string, budget time.Duration, sources ...SourceFacts) string {
	t.Helper()
	text := AnsweredTurn(t, next, conv, batch, budget, sources...)
	if witness == "" || !strings.Contains(text, witness) {
		t.Fatal("answer lacks its answer-only source witness")
	}
	return text
}

func RequireWitnessAnswerHistory(t *testing.T, home, conv, session string, batch protocol.QuestionShownPayload, answers []protocol.QuestionAnswerEntry) {
	t.Helper()
	RequirePromptAnswerHistory(t, home, conv, batch.QuestionBatchID, "answer", session)
	entries, err := ReadPromptAnswerHistory(os.DirFS(filepath.Join(home, ".pyry", "test", "conversations", conv, "history")))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		var fact struct {
			CorrelationID string `json:"correlation_id"`
		}
		if json.Unmarshal(entry.Payload, &fact) != nil {
			t.Fatal("invalid prompt answer history")
		}
		if fact.CorrelationID != batch.QuestionBatchID {
			continue
		}
		count++
		if err := CheckQuestionAnswerHistory(entry, batch, answers, session); err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatal("answer history does not contain exactly one fact for its original batch")
	}
}
func WitnessModelTurn(t *testing.T, next Next, conv, expected string, budget time.Duration, sources ...SourceFacts) {
	t.Helper()
	seen := false
	model := ""
	checked := func(deadline time.Time) protocol.Envelope {
		env := next(deadline)
		alarm(t, env)
		if env.Type == protocol.TypeModalShown {
			diagnostic := string(env.Payload)
			if len(diagnostic) > 2048 {
				diagnostic = diagnostic[:2048] + "…"
			}
			t.Fatalf("unexpected modal_shown during post-question turn: %q", diagnostic)
		}
		if env.Type == protocol.TypeRateLimited {
			return protocol.Envelope{}
		}
		if env.Type == protocol.TypeModelAnnounced && !seen {
			var p protocol.ModelAnnouncedPayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid model announcement")
			}
			if p.ConversationID == conv {
				model, seen = p.Model, true
			}
		}
		return env
	}
	CompletedTurn(t, checked, conv, budget, true, sources...)
	if !seen {
		t.Fatal("the post-question turn emitted no model_announced frame")
	}
	if model != expected {
		t.Error("post-question model does not match the selected model")
	}

}

type WitnessQuestionConfig struct {
	Home, ConversationID, SessionID, Prompt string
	SurfaceBudget, UpdateBudget, TurnBudget time.Duration
	Witness                                 func() string
}

func WitnessQuestionAnswer(t *testing.T, w Wire, c WitnessQuestionConfig) {
	t.Helper()
	var prior strings.Builder
	batch := RaiseWitnessQuestion(t, w, c.ConversationID, "m-2", c.Prompt, c.SurfaceBudget, &prior)
	target := "haiku"
	id := w.Send(protocol.TypeSetSessionSettings, protocol.SetSessionSettingsPayload{SessionID: c.SessionID, Model: &target})
	env := AwaitWitnessQuestionControl(t, w.Next, protocol.TypeSessionSettingsUpdated, c.UpdateBudget, &prior, &batch)
	var updated protocol.SessionSettingsUpdatedPayload
	if json.Unmarshal(env.Payload, &updated) != nil || env.InReplyTo == nil || *env.InReplyTo != id || updated.SessionID != c.SessionID {
		t.Fatal("question model update lost request/session correlation")
	}
	witness := ""
	if c.Witness != nil {
		witness = c.Witness()
	} else {
		witness = NewQuestionWitness(t)
	}
	answers := ChooseWitnessAnswers(t, batch, false, witness)
	after, preAnswer := QuestionBoundary(t, w.Source, c.ConversationID)
	prior.WriteString(preAnswer)
	if err := CheckQuestionWitness(witness, batch, c.Prompt, prior.String()); err != nil {
		t.Fatal(err)
	}
	w.Send(protocol.TypeQuestionAnswer, protocol.QuestionAnswerPayload{QuestionBatchID: batch.QuestionBatchID, AnswerToken: "e2e-1987-answer-token", Answers: answers})
	RequireWitnessAnswerHistory(t, c.Home, c.ConversationID, c.SessionID, batch, answers)
	WitnessAnsweredTurn(t, w.Next, c.ConversationID, batch.QuestionBatchID, witness, c.TurnBudget, after)
}

type WitnessBypassConfig struct {
	Home, ConversationID, SessionID string
	Prompt                          func(bool) string
	Witness                         func(int) string
	ToolWitness                     func() string
	CheckToolWitness                func(string)
	SurfaceBudget, TurnBudget       time.Duration
}

func WitnessBypassQuestions(t *testing.T, w Wire, c WitnessBypassConfig) {
	t.Helper()
	if c.Prompt == nil || c.ToolWitness == nil || c.CheckToolWitness == nil {
		t.Fatal("bypass witness callbacks must be explicit")
	}
	var prior strings.Builder
	for i, multi := range []bool{false, true} {
		prompt := c.Prompt(multi)
		batch := RaiseWitnessQuestion(t, w, c.ConversationID, fmt.Sprintf("m-%d", 2+i*2), prompt, c.SurfaceBudget, &prior)
		if len(batch.Questions) != 1 || batch.Questions[0].MultiSelect != multi {
			t.Fatal("question batch lost its exact selection variant")
		}
		witness := ""
		if c.Witness != nil {
			witness = c.Witness(i)
		} else {
			witness = NewQuestionWitness(t)
		}
		answers := ChooseWitnessAnswers(t, batch, multi, witness)
		after, preAnswer := QuestionBoundary(t, w.Source, c.ConversationID)
		prior.WriteString(preAnswer)
		if err := CheckQuestionWitness(witness, batch, prompt, prior.String()); err != nil {
			t.Fatal(err)
		}
		w.Send(protocol.TypeQuestionAnswer, protocol.QuestionAnswerPayload{QuestionBatchID: batch.QuestionBatchID, AnswerToken: fmt.Sprintf("bypass-answer-%d", 3+i*2), Answers: answers})
		text := WitnessAnsweredTurn(t, w.Next, c.ConversationID, batch.QuestionBatchID, witness, c.TurnBudget, after)
		prior.WriteString(text)
		n := 1
		if multi {
			n = 2
		}
		options := batch.Questions[0].Options
		for _, option := range options[len(options)-n:] {
			if !strings.Contains(strings.ToLower(text), strings.ToLower(option.Label)) {
				t.Fatal("resumed reply omitted an offered selection")
			}
		}
		RequireWitnessAnswerHistory(t, c.Home, c.ConversationID, c.SessionID, batch, answers)
	}
	witness := c.ToolWitness()
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "bypass-ordinary-tool", Text: fmt.Sprintf("Use Bash once to run exactly this command: printf '%%s' '%s' > bypass-witness.txt\nThen reply done. Do not ask a question.", witness)})
	CompletedTurn(t, w.Next, c.ConversationID, c.TurnBudget, false, w.Source)
	c.CheckToolWitness(witness)
	id := w.Send(protocol.TypeRequestSessionSettings, protocol.RequestSessionSettingsPayload{ConversationID: c.ConversationID})
	env := Correlated(t, w.Next, id, 15*time.Second)
	var settings protocol.SessionSettingsPayload
	if env.Type != protocol.TypeSessionSettings || json.Unmarshal(env.Payload, &settings) != nil || settings.SessionID != c.SessionID || settings.PermissionMode != "bypassPermissions" || !settings.YOLO {
		t.Fatal("live child lost bypass mode or settings session ownership")
	}
}
func RecordedWitnessQuestionPrompt(multi bool) string {
	prompt := strings.Replace(RecordedQuestionPrompt(), "After I answer, reply with the label of the option I chose.", "My answer also names the cache. After I answer, confirm the selected policies and repeat that supplied cache name verbatim as the answer witness.", 1)
	if multi {
		prompt = strings.Replace(prompt, `"multiSelect":false`, `"multiSelect":true`, 1)
	}
	return prompt
}
