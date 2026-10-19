//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// The dispatcher runs these with credentials; compiling or skipping is not proof.
func TestInteractiveStreamBypassQuestions(t *testing.T) {
	for _, stdio := range []bool{false, true} {
		for _, operator := range []bool{false, true} {
			t.Run(fmt.Sprintf("stdio=%v/operator=%v", stdio, operator), func(t *testing.T) {
				observations, configure, _ := startPermissionObserver(t)
				t.Setenv("PYRY_PERMISSION_OBSERVER_QUESTIONS", "1")
				prepare := func(home string) {
					if operator {
						return
					}
					// Seed the persisted bypass before the daemon loads its registry.
					path := filepath.Join(home, ".pyry", "test", "sessions.json")
					raw, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					seed := strings.Replace(string(raw), `"bootstrap":true`, `"bootstrap":true,"yolo":true`, 1)
					if seed == string(raw) {
						t.Fatal("bootstrap seed was not updated")
					}
					if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				h, convID, _ := startObservedPermissionHarnessWithOperatorBypass(t,
					askQuestionCaptureModel, stdio, configure, operator, prepare)
				nextID := uint64(2)
				var prior strings.Builder
				for _, multi := range []bool{false, true} {
					selection := "allow only a single choice (multiSelect false)"
					if multi {
						selection = "allow multiple selections (multiSelect true)"
					}
					prompt := fmt.Sprintf("Before doing anything else, use AskUserQuestion to ask exactly one question "+
						"about cache eviction policy. Offer four distinct short labels, explicitly include multiSelect, and %s. "+
						"After I answer, confirm your interpretation of my selections and include the supplied answer witness verbatim. "+
						"Do not use another tool. run=%d", selection, time.Now().UnixNano())
					batch := raiseRealQuestionBatch(t, h, nextID, convID, prompt, &prior)
					nextID++
					if batch.ConversationID != convID || len(batch.Questions) != 1 || batch.Questions[0].MultiSelect != multi {
						t.Fatal("real batch did not match the requested conversation/selection variant")
					}
					entries, choice := chooseQuestionAnswers(t, batch, multi)
					if err := checkQuestionWitness(choice.witness, batch, prompt, prior.String()); err != nil {
						t.Fatal(err)
					}
					sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
						ID: nextID, Type: protocol.TypeQuestionAnswer, TS: time.Now().UTC(),
						Payload: mustJSON(t, protocol.QuestionAnswerPayload{
							QuestionBatchID: batch.QuestionBatchID, AnswerToken: fmt.Sprintf("bypass-answer-%d", nextID),
							Answers: entries,
						}),
					})
					nextID++
					text := drainAnsweredQuestionTurn(t, h, convID, batch.QuestionBatchID, choice.witness, perTurnReplyBudget)
					prior.WriteString(text)
					selectedCount := 1
					if multi {
						selectedCount = 2
					}
					options := batch.Questions[0].Options
					for _, option := range options[len(options)-selectedCount:] {
						value := option.Label
						if !strings.Contains(strings.ToLower(text), strings.ToLower(value)) {
							t.Fatalf("resumed reply omitted an answer; reply=%q", truncateString(text, questionTextLogCap))
						}
					}
					requirePromptAnswerHistory(t, h, convID, batch.QuestionBatchID, "answer", true, func(e history.Entry) error {
						return checkQuestionAnswerHistory(e, batch, entries)
					})
				}

				// This tool must actually run. A tool-free reply cannot satisfy the witness.
				witness := fmt.Sprintf("bypass-witness-%d", time.Now().UnixNano())
				sealSendMessage(t, h.phone, h.initSend, nextID, convID, "bypass-ordinary-tool",
					fmt.Sprintf("Use Bash once to run exactly this command: printf '%%s' '%s' > bypass-witness.txt\nThen reply done. Do not ask a question.", witness))
				nextID++
				drainForCompletedTurn(t, h.phone, h.initRecv, convID, perTurnReplyBudget)
				raw, err := os.ReadFile(filepath.Join(h.workdir, "bypass-witness.txt"))
				if err != nil || string(raw) != witness {
					t.Fatal("ordinary bypass tool did not write the witness")
				}
				settings := requestSessionSettings(t, h, convID, nextID)
				if settings.SessionID != streamModalBootstrapUUID || settings.PermissionMode != runningModeBypass || !settings.YOLO {
					t.Fatal("live child lost bypass mode or settings session ownership")
				}

				var tape claudetape.Tape
				for {
					select {
					case observed := <-observations:
						raw, err := json.Marshal(observed)
						if err != nil {
							t.Fatal(err)
						}
						tape.Frames = append(tape.Frames, claudetape.Frame{Direction: "stdout", Data: raw})
					default:
						if err := tape.RequireBypassQuestions(); err != nil {
							t.Fatal(err)
						}
						return
					}
				}
			})
		}
	}
}
