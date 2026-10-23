package scenario

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Wire keeps the real encrypted relay transport in the caller's harness.
type Wire struct {
	Send   func(string, any) uint64
	Next   Next
	Source SourceFacts
}

type Choice struct {
	Chosen   string
	Unchosen []string
}

type QuestionConfig struct {
	Home, ConversationID, SessionID         string
	Prompt, Followup, ExpectedModel         string
	HistorySession                          string
	SurfaceBudget, UpdateBudget, TurnBudget time.Duration
}

// QuestionAnswer exercises a surfaced batch, an in-band model update while it is
// parked, explicit remote answer, continuation, durable answer fact and a fresh
// turn that announces the changed model. Selection always comes from the batch.
func QuestionAnswer(t *testing.T, w Wire, c QuestionConfig) {
	t.Helper()
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "question-trigger", Text: c.Prompt})
	batch := WaitQuestion(t, w.Next, c.ConversationID, c.SurfaceBudget)
	target := "haiku"
	id := w.Send(protocol.TypeSetSessionSettings, protocol.SetSessionSettingsPayload{SessionID: c.SessionID, Model: &target})
	deadline := time.Now().Add(c.UpdateBudget)
	updated := false
	for time.Now().Before(deadline) {
		env := w.Next(deadline)
		alarm(t, env)
		if env.Type == protocol.TypeQuestionDismissed {
			t.Fatal("parked question was resolved by model update")
		}
		if env.Type == protocol.TypeQuestionShown {
			var repeated protocol.QuestionShownPayload
			if json.Unmarshal(env.Payload, &repeated) != nil || repeated.QuestionBatchID != batch.QuestionBatchID || repeated.ConversationID != c.ConversationID {
				t.Fatal("parked question was replaced by model update")
			}
		}
		if env.Type != protocol.TypeSessionSettingsUpdated {
			continue
		}
		var payload protocol.SessionSettingsUpdatedPayload
		if json.Unmarshal(env.Payload, &payload) != nil || env.InReplyTo == nil || *env.InReplyTo != id || payload.SessionID != c.SessionID {
			t.Fatal("model update reply lost request or session correlation")
		}
		updated = true
		break
	}
	if !updated {
		t.Fatal("parked question model update did not complete")
	}
	entries, choice := ChooseAnswers(t, batch)
	after, _ := QuestionBoundary(t, w.Source, c.ConversationID)
	w.Send(protocol.TypeQuestionAnswer, protocol.QuestionAnswerPayload{QuestionBatchID: batch.QuestionBatchID, AnswerToken: "e2e-1987-answer-token", Answers: entries})
	text := AnsweredTurn(t, w.Next, c.ConversationID, batch.QuestionBatchID, c.TurnBudget, after)
	RequireChoice(t, text, choice)
	RequirePromptAnswerHistory(t, c.Home, c.ConversationID, batch.QuestionBatchID, "answer", c.HistorySession)
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "m-after-model-change", Text: c.Followup})
	seen := false
	var model string
	next := func(deadline time.Time) protocol.Envelope {
		env := w.Next(deadline)
		if env.Type == protocol.TypeModelAnnounced && !seen {
			var p protocol.ModelAnnouncedPayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid model announcement")
			}
			if p.ConversationID == c.ConversationID {
				model, seen = p.Model, true
			}
		}
		return env
	}
	CompletedTurn(t, next, c.ConversationID, c.TurnBudget, true, w.Source)
	if !seen || model != c.ExpectedModel {
		t.Fatal("post-question turn did not announce the selected model")
	}
}

func WaitQuestion(t *testing.T, next Next, convID string, budget time.Duration) protocol.QuestionShownPayload {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		env := next(deadline)
		alarm(t, env)
		if env.Type != protocol.TypeQuestionShown || env.SessionStateCleared {
			continue
		}
		var batch protocol.QuestionShownPayload
		if json.Unmarshal(env.Payload, &batch) != nil || batch.ConversationID != convID || batch.QuestionBatchID == "" || len(batch.Questions) == 0 || len(batch.Questions[0].Options) < 2 {
			t.Fatal("question batch is unanswerable or has wrong ownership")
		}
		return batch
	}
	t.Fatal("no question batch surfaced")
	return protocol.QuestionShownPayload{}
}

func ChooseAnswers(t *testing.T, batch protocol.QuestionShownPayload) ([]protocol.QuestionAnswerEntry, Choice) {
	t.Helper()
	var entries []protocol.QuestionAnswerEntry
	var choice Choice
	for i, q := range batch.Questions {
		if len(q.Options) == 0 {
			t.Fatal("question offers no options")
		}
		last := len(q.Options) - 1
		entries = append(entries, protocol.QuestionAnswerEntry{QuestionIndex: i, Values: []string{q.Options[last].Label}})
		if i == 0 {
			choice.Chosen = q.Options[last].Label
			for j, o := range q.Options {
				if j != last {
					choice.Unchosen = append(choice.Unchosen, o.Label)
				}
			}
		}
	}
	return entries, choice
}

// AnsweredTurn retains all three milestones regardless of dismissal/idle order.
// Idle preceding the first matching nonempty text cannot close this turn.
func AnsweredTurn(t *testing.T, next Next, convID, batchID string, budget time.Duration, sources ...SourceFacts) string {
	t.Helper()
	deadline := time.Now().Add(budget)
	var turn sourceTurn
	dismissed, idle := false, false
	for time.Now().Before(deadline) {
		env := next(pollDeadline(deadline))
		turn.feed(t, convID, sourceFacts(t, sources, convID))
		alarm(t, env)
		switch env.Type {
		case protocol.TypeQuestionDismissed:
			var p protocol.QuestionDismissedPayload
			if json.Unmarshal(env.Payload, &p) != nil || p.QuestionBatchID != batchID || p.Source != "remote" || p.Outcome != "answered" {
				t.Fatal("question dismissal lost batch identity or remote answer attribution")
			}
			dismissed = true
		case protocol.TypeAssistantDelta:
			t.Fatal("retired question continuation delivered on wire")
		case protocol.TypeTurnState:
			var p protocol.TurnStatePayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid question turn state")
			}
			if turn.idle(t, env, convID) && strings.TrimSpace(turn.text.String()) != "" {
				idle = true
			}
		}
		if dismissed && idle {
			return turn.text.String()
		}
	}
	t.Fatal("answered question lacks remote dismissal, nonempty continuation or terminal idle")
	return ""
}

func RequireChoice(t *testing.T, text string, choice Choice) {
	t.Helper()
	haystack := strings.ToLower(text)
	selected := strings.Index(haystack, strings.ToLower(choice.Chosen))
	if choice.Chosen == "" || selected < 0 {
		t.Fatal("the continuation never names the chosen option")
	}
	for _, other := range choice.Unchosen {
		if strings.EqualFold(other, choice.Chosen) {
			continue
		}
		if at := strings.Index(haystack, strings.ToLower(other)); at >= 0 && at < selected {
			t.Fatal("the continuation names an unchosen option before the chosen option")
		}
	}
}

func alarm(t *testing.T, env protocol.Envelope) {
	t.Helper()
	if env.SessionStateCleared {
		return
	}
	switch env.Type {
	case protocol.TypeError:
		t.Fatal("daemon returned an error during question scenario")
	case protocol.TypeUnrecognizedMessage:
		t.Fatal("question scenario exposed an unrecognized provider message")
	case protocol.TypeRateLimited:
		var p protocol.RateLimitedPayload
		if json.Unmarshal(env.Payload, &p) != nil || p.Status != WarnRateLimitStatus {
			t.Fatal("question scenario exposed an unknown rate-limit status")
		}
	}
}

// RecordedQuestionPrompt fixes the tool's input schema so fresh requests remain
// directly comparable. Assistant prose remains unrestricted apart from reporting
// the choice selected from the surfaced batch.
func RecordedQuestionPrompt() string {
	return fmt.Sprintf("Before writing anything, call AskUserQuestion exactly once with this exact JSON input, preserving all text and option order: %s. After I answer, reply with the label of the option I chose. Do not use other tools.", `{"questions":[{"question":"Which cache eviction policy should be used?","header":"Policy","multiSelect":false,"options":[{"label":"LRU","description":"Evict the least recently used entry."},{"label":"LFU","description":"Evict the least frequently used entry."},{"label":"FIFO","description":"Evict the oldest entry."},{"label":"Random","description":"Evict a random entry."}]}]}`)
}
