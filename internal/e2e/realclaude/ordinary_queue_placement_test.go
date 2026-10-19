//go:build e2e_realclaude

package realclaude

import (
	"fmt"
	"testing"
	"time"

	scenariohelpers "github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
)

func TestRealClaude_OrdinaryQueuedPlacement(t *testing.T) {
	runParallel(t)
	h, convID := startStreamRunningTurnHarness(t)
	scenariohelpers.OrdinaryQueuePlacement(t, checkpointHarnessWire(t, h), scenariohelpers.OrdinaryQueueConfig{ConversationID: convID, Prompt: fmt.Sprintf("Use Bash once in the foreground, never background: for i in $(seq 1 15); do sleep 1; done; echo done. Then reply briefly. run=%d", time.Now().UnixNano()), Budget: perTurnReplyBudget})
}
