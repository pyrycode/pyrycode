package relay

import (
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestV2Session_ExpiredRedeemBy_StillHandshakes is the handshake leg of the
// inertness pin: the v2 accept path never consults RedeemBy this slice, so a
// device whose deadline has already passed still reaches open. openModalConn
// drives the real handshake and fails the test if the conn never opens, so
// reaching open IS the assertion. The registry is built inline rather than via
// v2PairedRegistry so the shared helper's shape — which other tests in this
// package depend on — stays untouched.
func TestV2Session_ExpiredRedeemBy_StillHandshakes(t *testing.T) {
	t.Parallel()

	const connID = "c-v2-redeemby"

	expired := time.Now().UTC().Add(-time.Hour)
	reg := &devices.Registry{}
	reg.Add(devices.Device{
		TokenHash: devices.HashToken(v2TestToken),
		Name:      v2TestDevName,
		PairedAt:  expired.Add(-devices.RedemptionWindow),
		RedeemBy:  expired,
	})

	respPriv, respPub := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope, 4)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	})
	t.Cleanup(stop)

	openModalConn(t, mgr, frames, rec, respPub, connID, nil)

	// Non-vacuity guard: the accepted device must still carry the back-dated
	// deadline. A fixture that lost it would make reaching open prove nothing.
	dev, ok := reg.FindByTokenHash(devices.HashToken(v2TestToken))
	if !ok {
		t.Fatalf("FindByTokenHash: paired device missing after handshake")
	}
	if !dev.RedeemBy.Equal(expired) {
		t.Fatalf("accepted device RedeemBy = %v, want the back-dated %v", dev.RedeemBy, expired)
	}
}
