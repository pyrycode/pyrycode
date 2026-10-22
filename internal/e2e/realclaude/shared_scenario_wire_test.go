//go:build e2e_realclaude

package realclaude

import (
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func sharedHarnessWire(t *testing.T, h *perConvHarness, first uint64) scenario.Wire {
	sequence := first - 1
	return scenario.ObserveSource(t, h.home, scenario.Wire{Send: func(kind string, payload any) uint64 {
		sequence++
		sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{ID: sequence, Type: kind, TS: time.Now().UTC(), Payload: mustJSON(t, payload)})
		return sequence
	}, Next: scenario.PollingReceive(t, h.phone, h.initRecv)})
}
