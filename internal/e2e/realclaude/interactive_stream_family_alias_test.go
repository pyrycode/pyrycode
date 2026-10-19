//go:build e2e_realclaude

package realclaude

import (
	"fmt"
	scenariohelpers "github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"testing"
	"time"
)

const familyAliasMenuBudget = 30 * time.Second

func TestInteractiveStreamPinnedModelFollowsItsFamily(t *testing.T) {
	runParallel(t)
	h, convID := startStreamModalResolutionHarness(t, permissionDaemonModel)
	sequence := uint64(0)
	wire := scenariohelpers.Wire{Send: func(kind string, payload any) uint64 {
		sequence++
		sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{ID: sequence, Type: kind, TS: time.Now().UTC(), Payload: mustJSON(t, payload)})
		return sequence
	}, Next: scenariohelpers.Receive(t, h.phone, h.initRecv)}
	scenariohelpers.ModelFamily(t, wire, convID, streamModalBootstrapUUID, func(int) string {
		return fmt.Sprintf("Reply with one short word and nothing else. run=%d", time.Now().UnixNano())
	}, familyAliasMenuBudget, perTurnReplyBudget)
}
