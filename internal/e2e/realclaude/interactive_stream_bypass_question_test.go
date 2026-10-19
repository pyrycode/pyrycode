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
	scenariohelpers "github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
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
				sequence := uint64(1)
				wire := scenariohelpers.Wire{Send: func(kind string, payload any) uint64 {
					sequence++
					sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{ID: sequence, Type: kind, TS: time.Now().UTC(), Payload: mustJSON(t, payload)})
					return sequence
				}, Next: scenariohelpers.Receive(t, h.phone, h.initRecv)}
				witness := fmt.Sprintf("bypass-witness-%d", time.Now().UnixNano())
				scenariohelpers.BypassQuestions(t, wire, scenariohelpers.BypassQuestionsConfig{Home: h.home, ConversationID: convID, SessionID: streamModalBootstrapUUID, Prompt: func(multi bool) string {
					selection := "allow only a single choice (multiSelect false)"
					if multi {
						selection = "allow multiple selections (multiSelect true)"
					}
					return fmt.Sprintf("Before doing anything else, use AskUserQuestion to ask exactly one question about cache eviction policy. Offer four distinct short labels, explicitly include multiSelect, and %s. After I answer, reply with each answer value exactly as received, including any free text, and nothing else. Do not use another tool. run=%d", selection, time.Now().UnixNano())
				}, FreeText: fmt.Sprintf("custom-%d", time.Now().UnixNano()), Witness: witness, Budget: perTurnReplyBudget, SurfaceBudget: questionSurfaceBudget, CheckWitness: func() {
					raw, err := os.ReadFile(filepath.Join(h.workdir, "bypass-witness.txt"))
					if err != nil || string(raw) != witness {
						t.Fatal("ordinary bypass tool did not write the witness")
					}
				}})
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
