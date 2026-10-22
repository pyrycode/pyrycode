//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func runTapeWitnessQuestion(t *testing.T, c recordedScenarioContext) {
	scenario.WitnessQuestionAnswer(t, c.Wire, scenario.WitnessQuestionConfig{Home: c.Home, ConversationID: tapeConversation, SessionID: tapeSession, Prompt: scenario.RecordedWitnessQuestionPrompt(false), SurfaceBudget: 180 * time.Second, UpdateBudget: 30 * time.Second, TurnBudget: 120 * time.Second, Witness: func() string { return "answer-witness-2c1c7e934e274f70b8ce3b138653c169" }})
	c.Wire.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: tapeConversation, MessageID: "m-after-model-change", Text: "Reply with one short word. run=tape-witness after-model-change"})
	scenario.WitnessModelTurn(t, c.Wire.Next, tapeConversation, "claude-haiku-4-5-20251001", 120*time.Second, c.Wire.Source)
}
func runTapeWitnessBypass(t *testing.T, c recordedScenarioContext) {
	scenario.WitnessBypassQuestions(t, c.Wire, scenario.WitnessBypassConfig{Home: c.Home, ConversationID: tapeConversation, SessionID: tapeSession, Prompt: scenario.RecordedWitnessQuestionPrompt, SurfaceBudget: 180 * time.Second, TurnBudget: 120 * time.Second,
		Witness:     func(i int) string { return fmt.Sprintf("answer-witness-07221e0f93ca40c383544052c1a14b0%d", i) },
		ToolWitness: func() string { return "bypass-witness-06281a7fd35440ad8aa1b4b52bd9130b" },
		CheckToolWitness: func(witness string) {
			if c.Mode == "capture" {
				raw, err := os.ReadFile(filepath.Join(c.Workdir, "bypass-witness.txt"))
				if err != nil || string(raw) != witness {
					t.Fatal("fresh bypass tool did not write its exact witness")
				}
			}
		},
	})
}
