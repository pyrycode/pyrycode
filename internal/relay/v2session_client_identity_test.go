package relay

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// csiRun is an ANSI CSI escape run, composed at run time rather than spelled as
// a source literal. cmd/substrate-guard bans the CSI introducer in both of its
// source spellings — hex-escaped and raw — anywhere in the tree, and a test
// feeding one through the handshake is not an exception to that rule: the guard
// is repo-wide by design, and allowlisting a file would exempt it wholesale.
var csiRun = string(rune(0x1b)) + "[31m"

// buildHelloIdentityEarlyData is buildHelloEarlyData with the two self-reported
// identity fields under the test's control — the inputs #2148 retains.
func buildHelloIdentityEarlyData(t *testing.T, token, deviceName, clientVersion string) []byte {
	t.Helper()
	payload, err := json.Marshal(protocol.HelloClientPayload{
		Role:             "client",
		DeviceName:       deviceName,
		ClientVersion:    clientVersion,
		ProtocolVersions: []string{"v2"},
		Token:            token,
	})
	if err != nil {
		t.Fatalf("marshal hello payload: %v", err)
	}
	envBytes, err := json.Marshal(protocol.Envelope{
		ID:      1,
		Type:    protocol.TypeHello,
		TS:      time.Now().UTC(),
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("marshal hello envelope: %v", err)
	}
	return envBytes
}

// openWithIdentity drives one real IK handshake whose hello reports deviceName /
// clientVersion, and returns the manager. A real handshake rather than an
// injected V2Session literal is the point: the retention this ticket adds lives in
// handleNoiseInit's token-OK tail, so a literal would assert nothing about it.
func openWithIdentity(t *testing.T, token, deviceName, clientVersion string) *V2SessionManager {
	t.Helper()
	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    v2PairedRegistry(t, v2TestToken),
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	})
	t.Cleanup(stop)

	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloIdentityEarlyData(t, token, deviceName, clientVersion))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg)
	waitForEnvelopes(t, rec, 1)
	return mgr
}

// TestV2Session_ActiveConns_RetainsClientIdentity (#2148) pins this package's
// half of the contract: it RETAINS what the client reported and judges none of
// it. The control-character and injection-shaped rows are green on purpose —
// refusing them is internal/sessions' admitClient's job, and a second opinion
// here would be a second place for the same decision to drift.
//
// What IS enforced here is the length bound, because it is a resource property
// rather than a display one: without it one conn parks ~64KB per string for its
// lifetime and has it copied into every ActiveConn snapshot the fan-out takes.
func TestV2Session_ActiveConns_RetainsClientIdentity(t *testing.T) {
	t.Parallel()
	overName := strings.Repeat("n", maxRetainedClientNameBytes+1)
	overVersion := strings.Repeat("v", maxRetainedClientVersionBytes+1)

	tests := []struct {
		name        string
		devName     string
		devVersion  string
		wantName    string
		wantVersion string
	}{
		{"ordinary report", "Juhanas-MacBook", "0.4.1", "Juhanas-MacBook", "0.4.1"},
		{"reports nothing", "", "", "", ""},
		{"control characters retained verbatim", "a\nb", "1" + csiRun, "a\nb", "1" + csiRun},
		{"quote retained verbatim", `a"b`, `1"`, `a"b`, `1"`},
		{"name at the bound is kept", strings.Repeat("n", maxRetainedClientNameBytes), "1", strings.Repeat("n", maxRetainedClientNameBytes), "1"},
		{"over-bound name dropped, not truncated", overName, "0.4.1", "", "0.4.1"},
		{"over-bound version dropped, not truncated", "Pixel 8", overVersion, "Pixel 8", ""},
		{"both over bound", overName, overVersion, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mgr := openWithIdentity(t, v2TestToken, tc.devName, tc.devVersion)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			conns := mgr.ActiveConns(ctx)
			if len(conns) != 1 {
				t.Fatalf("ActiveConns() = %d conns, want 1 open", len(conns))
			}
			if got := conns[0].DeviceName; got != tc.wantName {
				t.Errorf("ActiveConn.DeviceName = %q, want %q", got, tc.wantName)
			}
			if got := conns[0].ClientVersion; got != tc.wantVersion {
				t.Errorf("ActiveConn.ClientVersion = %q, want %q", got, tc.wantVersion)
			}
		})
	}
}

// TestV2Session_ActiveConns_RejectedTokenLeavesNoIdentity (#2148) pins the
// placement of the retention: it happens on the token-OK path, before the
// V2StateOpen transition, so an UNAUTHENTICATED peer's self-reported strings are
// never observable through the enumeration. The same gate s.interactive relies on.
func TestV2Session_ActiveConns_RejectedTokenLeavesNoIdentity(t *testing.T) {
	t.Parallel()
	mgr := openWithIdentity(t, "not-the-paired-token", "Attacker-Device", "9.9.9")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if conns := mgr.ActiveConns(ctx); len(conns) != 0 {
		t.Fatalf("ActiveConns() = %v, want none: a rejected token must not enumerate", conns)
	}
}

// TestV2Session_ActiveConns_DeviceTokenHashIsAuthenticatedDevice (#2564) pins
// that the snapshot identifies a conn by the device the handshake AUTHENTICATED,
// not by the name its hello claimed. The push-wake trigger suppresses a device's
// wake on this field; were it derived from the hello name, a phone could silence
// another device's wakes by claiming that device's name.
func TestV2Session_ActiveConns_DeviceTokenHashIsAuthenticatedDevice(t *testing.T) {
	t.Parallel()
	mgr := openWithIdentity(t, v2TestToken, "Someone-Elses-Phone", "0.4.1")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conns := mgr.ActiveConns(ctx)
	if len(conns) != 1 {
		t.Fatalf("ActiveConns() = %d conns, want 1 open", len(conns))
	}
	if got, want := conns[0].DeviceTokenHash, devices.HashToken(v2TestToken); got != want {
		t.Errorf("ActiveConn.DeviceTokenHash = %q, want the authenticated device's hash %q", got, want)
	}
}
