//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
)

// TestRelay_BaseURLAppendsServerPath closes the gap the harness historically
// hid by always passing fr.URL()+"/v1/server" explicitly (#631). Here the
// daemon is given the BARE base relay URL — no /v1/server path — exactly as
// it appears in the shipped default config. It must still connect, which it
// can only do if relay.Connect appends /v1/server before dialing (Option A),
// mirroring the phone's /v1/client convention. No PYRY_RELAY_URL override.
func TestRelay_BaseURLAppendsServerPath(t *testing.T) {
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	home := shortHome(t)
	StartInWithEnv(t,
		home,
		[]string{"PYRY_ALLOW_INSECURE_RELAY=1"},
		"-pyry-relay="+fr.URL(), // bare base, no /v1/server
	)

	serverID := readPersistedServerID(t, home)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if !fr.WaitBinary(ctx, serverID) {
		t.Fatal("binary connection not registered within 4s — daemon did not append /v1/server to the base relay_url")
	}
}
