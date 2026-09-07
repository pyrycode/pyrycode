package relay

import (
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestV2Session_ElapsedRedemptionWindow_Rejects is the handshake leg of the
// enforcement: a device whose deadline has already passed never reaches open,
// it is closed at 4401 like any unrecognised token. waitForConnClose fails the
// test if that close never arrives, so observing the 4401 IS the assertion —
// the inverse of the reaching-open pin #1527 wrote here. The handshake is
// driven inline rather than through openModalConn because that helper blocks
// until the conn opens, which is exactly what must not happen now. The registry
// is likewise built inline rather than via v2PairedRegistry so the shared
// helper's shape — which other tests in this package depend on — stays
// untouched.
func TestV2Session_ElapsedRedemptionWindow_Rejects(t *testing.T) {
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
	_, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	})
	t.Cleanup(stop)

	initPriv, _ := genV2Keypair(t)
	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarlyDataCaps(t, v2TestToken, nil))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	frames <- wrapInnerFrame(t, connID, protocol.TypeNoiseInit, initMsg)

	waitForConnClose(t, rec, connID, uint16(StatusUnauthorized))

	// Non-vacuity guard, doubling as AC-5's witness: the rejected device must
	// still be in the registry, still carrying the back-dated deadline. A
	// fixture that lost it would make the 4401 prove nothing, and a validation
	// that removed the row would make `pyry pair list` stop showing it.
	dev, ok := reg.FindByTokenHash(devices.HashToken(v2TestToken))
	if !ok {
		t.Fatalf("FindByTokenHash: rejected device missing after handshake")
	}
	if !dev.RedeemBy.Equal(expired) {
		t.Fatalf("rejected device RedeemBy = %v, want the back-dated %v", dev.RedeemBy, expired)
	}
}
