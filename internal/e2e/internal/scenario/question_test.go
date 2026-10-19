package scenario

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestQuestionControls(t *testing.T) {
	for _, name := range []string{"ordered", "idle-before-dismissal", "missing-dismissal", "missing-text", "foreign-text", "wrong-batch", "wrong-source", "wrong-outcome", "unchosen-first"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQuestionHelperProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "PYRY_QUESTION_CONTROL="+name)
			out, err := cmd.CombinedOutput()
			valid := name == "ordered" || name == "idle-before-dismissal"
			if ctx.Err() != nil || (err == nil) != valid {
				t.Fatalf("question evidence acceptance: err=%v output=%s", err, out)
			}
			if valid && !strings.Contains(string(out), "all question milestones verified") {
				t.Fatal("helper did not execute the assertion")
			}
		})
	}
}

func TestQuestionHelperProcess(t *testing.T) {
	name := os.Getenv("PYRY_QUESTION_CONTROL")
	if name == "" {
		return
	}
	frame := func(kind string, payload any) protocol.Envelope {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.Envelope{Type: kind, Payload: raw}
	}
	dis := protocol.QuestionDismissedPayload{QuestionBatchID: "batch", Source: "remote", Outcome: "answered"}
	delta := protocol.AssistantDeltaPayload{ConversationID: "conversation", Text: "Random"}
	if name == "wrong-batch" {
		dis.QuestionBatchID = "foreign"
	}
	if name == "wrong-source" {
		dis.Source = "no_answer"
	}
	if name == "wrong-outcome" {
		dis.Outcome = "unanswered"
	}
	if name == "missing-text" {
		delta.Text = " \t"
	}
	if name == "foreign-text" {
		delta.ConversationID = "foreign"
	}
	if name == "unchosen-first" {
		delta.Text = "LRU, then Random"
	}
	d := frame(protocol.TypeQuestionDismissed, dis)
	text := frame(protocol.TypeAssistantDelta, delta)
	idle := frame(protocol.TypeTurnState, protocol.TurnStatePayload{ConversationID: "conversation", State: "idle"})
	frames := []protocol.Envelope{d, text, idle}
	if name == "idle-before-dismissal" {
		frames = []protocol.Envelope{text, idle, d}
	}
	if name == "missing-dismissal" {
		frames = []protocol.Envelope{text, idle}
	}
	next := func(deadline time.Time) protocol.Envelope {
		if len(frames) == 0 {
			time.Sleep(time.Until(deadline))
			return protocol.Envelope{}
		}
		env := frames[0]
		frames = frames[1:]
		return env
	}
	got := AnsweredTurn(t, next, "conversation", "batch", 40*time.Millisecond)
	RequireChoice(t, got, Choice{Chosen: "Random", Unchosen: []string{"LRU"}})
	t.Log("all question milestones verified")
}
