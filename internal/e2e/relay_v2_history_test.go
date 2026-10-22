//go:build e2e

package e2e

import (
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"testing"
)

func sendSealedEnvelope(t *testing.T, phone *fakephone.Client, send *noise.CipherState, env protocol.Envelope) {
	t.Helper()
	envBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal %s envelope %d: %v", env.Type, env.ID, err)
	}
	ciphertext, err := send.Encrypt(envBytes)
	if err != nil {
		t.Fatalf("seal %s envelope %d: %v", env.Type, env.ID, err)
	}
	sendNoiseMsg(t, phone, ciphertext)
}
