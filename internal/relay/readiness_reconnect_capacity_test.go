package relay

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestReadinessLegacyReconnectSnapshotCapacity(t *testing.T) {
	t.Skip("blocked on #3214: legacy reconnect snapshots exhaust hosted delivery capacity")
	p := testHostedRealTransport(t, true)
	payloads := make([]protocol.ModelListPayload, 4*hostedQueueLimit)
	for i := range payloads {
		payloads[i] = sampleModelListPayload(fmt.Sprintf("retained-%d", i), "sonnet")
	}
	p.mgr.cfg.RetainedModelLists = func(bool) []protocol.ModelListPayload { return payloads }
	entered, release := p.gate.arm(t)
	init, err := noise.NewInitiator(v2TestInstallPriv, p.pub)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := init.WriteInit(buildHelloEarlyDataCaps(t, v2TestToken, []string{protocol.CapabilityInteractive}))
	if err != nil {
		t.Fatal(err)
	}
	p.write(wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, msg))
	testHostedAwait(t, entered)
	waitConnOpen(t, p.mgr, v2TestConnID)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := p.mgr.FlushPushes(ctx); err != nil {
		t.Fatal(err)
	}
	release()
	_, _, recv, err := init.ReadResp(decodeRespFrame(t, p.next()))
	if err != nil {
		t.Fatal(err)
	}
	for range payloads {
		wire := p.next()
		if wire.CloseCode != 0 {
			t.Fatalf("legacy reconnect closed with %d before all %d retained snapshots", wire.CloseCode, len(payloads))
		}
		env := decryptAppFrame(t, wire, recv)
		if env.Type != protocol.TypeModelList {
			t.Fatalf("snapshot type=%s", env.Type)
		}
	}
}
