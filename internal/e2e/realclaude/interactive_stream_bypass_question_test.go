//go:build e2e_realclaude

package realclaude

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
				for _, multi := range []bool{false, true} {
					selection := "allow only a single choice (multiSelect false)"
					if multi {
						selection = "allow multiple selections (multiSelect true)"
					}
					prompt := fmt.Sprintf("Before doing anything else, use AskUserQuestion to ask exactly one question "+
						"about cache eviction policy. Offer four distinct short labels, explicitly include multiSelect, and %s. "+
						"After I answer, reply with each answer value exactly as received, including any free text, and nothing else. "+
						"Do not use another tool. run=%d", selection, time.Now().UnixNano())
					batch := raiseRealQuestionBatch(t, h, nextID, convID, prompt)
					nextID++
					if batch.ConversationID != convID || len(batch.Questions) != 1 || batch.Questions[0].MultiSelect != multi {
						t.Fatal("real batch did not match the requested conversation/selection variant")
					}
					options := batch.Questions[0].Options
					values := []string{options[len(options)-1].Label}
					if multi {
						if values[0] == options[len(options)-2].Label {
							t.Fatal("multiselect offered indistinguishable labels")
						}
						values = append(values, options[len(options)-2].Label, fmt.Sprintf("custom-%d", time.Now().UnixNano()))
					}
					sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
						ID: nextID, Type: protocol.TypeQuestionAnswer, TS: time.Now().UTC(),
						Payload: mustJSON(t, protocol.QuestionAnswerPayload{
							QuestionBatchID: batch.QuestionBatchID, AnswerToken: fmt.Sprintf("bypass-answer-%d", nextID),
							Answers: []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: values}},
						}),
					})
					nextID++
					text := drainAnsweredQuestionTurn(t, h, convID, batch.QuestionBatchID, perTurnReplyBudget)
					for _, value := range values {
						if !strings.Contains(strings.ToLower(text), strings.ToLower(value)) {
							t.Fatalf("resumed reply omitted an answer; reply=%q", truncateString(text, questionTextLogCap))
						}
					}
					if !multi {
						_, choice := chooseQuestionAnswers(t, batch)
						requireContinuationNamesChoice(t, text, choice)
					} else {
						var unchosen []string
						for _, option := range options[:len(options)-2] {
							unchosen = append(unchosen, option.Label)
						}
						for _, value := range values[:2] {
							requireContinuationNamesChoice(t, text, questionChoice{chosen: value, unchosen: unchosen})
						}
					}
					requirePromptAnswerHistory(t, h, convID, batch.QuestionBatchID, "answer", false)
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
				if settings.PermissionMode != runningModeBypass || !settings.YOLO {
					t.Fatal("live child lost bypass mode")
				}

				var initSeen bool
				requests, results := 0, 0
				requestIDs := make(map[string]bool)
				for {
					select {
					case observed := <-observations:
						switch observed.Type {
						case "system":
							initSeen = true
							if !slices.Contains(observed.Tools, "AskUserQuestion") || observed.PermissionMode != runningModeBypass {
								t.Fatal("real init did not offer AskUserQuestion in bypass mode")
							}
						case "control_request":
							if observed.Request.ToolName != "AskUserQuestion" {
								t.Fatal("ordinary tool acquired a host permission prompt")
							}
							if observed.RequestID == "" || observed.Request.ToolUseID == "" || requestIDs[observed.RequestID] {
								t.Fatal("question request identity missing or reused")
							}
							requestIDs[observed.RequestID] = true
							requests++
						case "result":
							results++
						}
					default:
						if !initSeen || requests != 2 || results < 3 {
							t.Fatalf("incomplete live evidence: init=%v requests=%d results=%d", initSeen, requests, results)
						}
						t.Logf("bypass live proof: init offered AskUserQuestion; %d actual asks answered and resumed, %d completed results, ordinary tool without host approval", requests, results)
						return
					}
				}
			})
		}
	}
}
