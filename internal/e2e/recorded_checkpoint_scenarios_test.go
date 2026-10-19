//go:build e2e

package e2e

import (
	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"testing"
	"time"
)

const recordedOrdinaryQueuePrompt = "Use Bash once in the foreground, never background: for i in $(seq 1 15); do sleep 1; done; echo done. Then reply briefly. run=tape-ordinary-queue"
const recordedRunningInterruptPrompt = "Use the Bash tool exactly once to run this command and wait for it to finish in the foreground — do NOT run it in the background: for i in $(seq 1 40); do sleep 1; done. After it completes, reply with a single short word. run=tape-interrupt-running"
const recordedInterruptHealthPrompt = "Reply with a single short word. run=tape-interrupt-health"
const recordedSubagentPrompt = "Use the Agent tool exactly once and wait for it in the foreground. Ask the agent to write exactly twelve numbered paragraphs about reliable process supervision, with at least two complete sentences per paragraph, and not to use tools. After it returns, write your own distinct twelve numbered paragraphs on the same topic, again with at least two complete sentences each. Do not use any other tools. run=tape-subagent"

func runTapeOrdinaryQueue(t *testing.T, c recordedScenarioContext) {
	scenario.OrdinaryQueuePlacement(t, c.Wire, scenario.OrdinaryQueueConfig{ConversationID: tapeConversation, Prompt: recordedOrdinaryQueuePrompt, Budget: 120 * time.Second, Checkpoint: func(id uint64) {
		c.Checkpoint(claudetape.ClientCheckpoint{Type: claudetape.ClientCheckpointDirection, Sequence: 1, Action: "queue_admitted", ConversationID: tapeConversation, MessageID: "ordinary-queued", QueuedMsgID: id, Text: scenario.OrdinaryQueuedText})
	}})
}
func runTapeInterrupt(t *testing.T, c recordedScenarioContext, thinking bool) {
	prompt, health := recordedRunningInterruptPrompt, recordedInterruptHealthPrompt
	if thinking {
		prompt = scenario.InitialThinkingPrompt("tape-interrupt-thinking")
		health = ""
	}
	scenario.InterruptTurn(t, c.Wire, scenario.InterruptConfig{ConversationID: tapeConversation, Prompt: prompt, HealthPrompt: health, InitialThinking: thinking, Budget: 120 * time.Second, Checkpoint: func(phase string) {
		c.Checkpoint(claudetape.ClientCheckpoint{Type: claudetape.ClientCheckpointDirection, Sequence: 1, Action: "interrupt_phase", ConversationID: tapeConversation, Phase: phase})
	}})
}
func runTapeSubagent(t *testing.T, c recordedScenarioContext) {
	scenario.SubagentText(t, c.Wire, tapeConversation, recordedSubagentPrompt, 180*time.Second, func() {
		c.Checkpoint(claudetape.ClientCheckpoint{Type: claudetape.ClientCheckpointDirection, Sequence: 1, Action: "main_stream_observed", ConversationID: tapeConversation})
	})
}
