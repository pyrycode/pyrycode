//go:build e2e_realclaude

package realclaude

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	scenariohelpers "github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const modelRejectionBudget = 30 * time.Second

// TestInteractiveStreamRejectsModelAbsentFromCurrentMenu repeats the client
// contract against a real child. The unavailable value is derived after reading
// that child's current menu, so a Claude release adding or removing rows cannot
// turn the negative path into a false positive.
func TestInteractiveStreamRejectsModelAbsentFromCurrentMenu(t *testing.T) {
	runParallel(t)
	h, convID := startStreamModalResolutionHarness(t, permissionDaemonModel)

	sequence := uint64(0)
	wire := scenariohelpers.Wire{Send: func(kind string, payload any) uint64 {
		sequence++
		sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{ID: sequence, Type: kind, TS: time.Now().UTC(), Payload: mustJSON(t, payload)})
		return sequence
	}, Next: scenariohelpers.PollingReceive(t, h.phone, h.initRecv)}
	wire = scenariohelpers.ObserveSource(t, h.home, wire)
	scenariohelpers.ModelRejection(t, wire, convID, streamModalBootstrapUUID, fmt.Sprintf("Reply with one short word and nothing else. run=%d", time.Now().UnixNano()), announcedTargetFor(t, permissionDaemonModel), modelRejectionBudget, perTurnReplyBudget)

}

// drainForCorrelatedEnvelope2281 returns any reply type for one request. The
// settings assertion needs to inspect an expected error rather than treating it
// as a harness failure, while the menu request needs the same receive-nonce-safe
// correlation behavior.
func drainForCorrelatedEnvelope2281(t *testing.T, phone *fakephone.Client, cs *noise.CipherState, reqID uint64, timeout time.Duration) protocol.Envelope {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("no reply to request %d within %s", reqID, timeout)
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				t.Fatalf("no reply to request %d within %s", reqID, timeout)
			}
			t.Fatalf("phone receive: %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame: %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue
		}
		ciphertext, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data: %v", err)
		}
		plain, err := cs.Decrypt(ciphertext)
		if err != nil {
			t.Fatalf("phone decrypt: %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		if env.InReplyTo != nil && *env.InReplyTo == reqID {
			return env
		}
	}
}
