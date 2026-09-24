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

// versionOutcome is what one handshake produced, decoded from the initiator's
// side: whether the conn opened, the ack it read out of noise_resp, and the
// sealed error plus close code of a second envelope when one was sent.
type versionOutcome struct {
	open      bool
	envs      []protocol.RoutingEnvelope
	ack       protocol.HelloAckPayload
	closeCode uint16
	hasErr    bool
	errEnv    protocol.Envelope
	errBody   protocol.ErrorPayload
	errRaw    string
}

// runVersionHello drives one real IK handshake whose hello carries token and
// clientVersion against a manager built from cfg. cfg supplies the fields under
// test (MinClientVersions, and optionally Devices, DevicesPath, Logger); the
// transport fields are filled here. Frames is unbuffered and ActiveConns is
// serviced by Run, so by the time it returns handleNoiseInit has finished and
// every envelope it sent is recorded.
func runVersionHello(t *testing.T, cfg V2SessionConfig, token, clientVersion string) versionOutcome {
	t.Helper()
	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope)
	rec := &v2Recorder{}
	cfg.Frames = frames
	cfg.Outbound = rec.outbound
	cfg.StaticPriv = respPriv
	cfg.ServerID = v2TestServerID
	if cfg.Devices == nil {
		cfg.Devices = v2PairedRegistry(t, v2TestToken)
	}
	if cfg.Logger == nil {
		cfg.Logger = silentLogger()
	}
	mgr, stop := startManager(t, cfg)
	t.Cleanup(stop)

	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloIdentityEarlyData(t, token, v2TestDevName, clientVersion))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out versionOutcome
	out.open = len(mgr.ActiveConns(ctx)) == 1
	out.envs = rec.snapshot()
	if len(out.envs) == 0 {
		t.Fatal("no envelope sent; want at least noise_resp")
	}
	if out.envs[0].CloseCode != 0 {
		t.Errorf("noise_resp envelope close_code = %d, want 0", out.envs[0].CloseCode)
	}
	ackBytes, _, initRecv, err := initiator.ReadResp(decodeRespFrame(t, out.envs[0]))
	if err != nil {
		t.Fatalf("initiator.ReadResp: %v", err)
	}
	out.ack = decodeHelloAck(t, ackBytes)
	if len(out.envs) < 2 {
		return out
	}
	out.closeCode = out.envs[1].CloseCode
	if out.envs[1].Frame == nil {
		return out
	}
	plaintext, err := initRecv.Decrypt(decodeNoiseMsg(t, out.envs[1]))
	if err != nil {
		t.Fatalf("decrypt sealed error: %v", err)
	}
	if err := json.Unmarshal(plaintext, &out.errEnv); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if err := json.Unmarshal(out.errEnv.Payload, &out.errBody); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	out.hasErr = true
	out.errRaw = string(out.errEnv.Payload)
	return out
}

// assertVersionAccepted fails unless the handshake opened a session exactly as
// one without any minimum does: one envelope, no close.
func assertVersionAccepted(t *testing.T, out versionOutcome) {
	t.Helper()
	if !out.open {
		t.Fatalf("conn did not open; envelopes %d, close_code %d, error %s", len(out.envs), out.closeCode, out.errRaw)
	}
	if len(out.envs) != 1 {
		t.Errorf("accepted handshake sent %d envelopes, want 1", len(out.envs))
	}
}

// assertVersionRejected checks the whole rejection shape: noise_resp, then one
// envelope carrying the sealed client.update_required error and close 4412, a
// session that never opened, and an ack without workspace_root.
func assertVersionRejected(t *testing.T, out versionOutcome, wantMin string) {
	t.Helper()
	if out.open {
		t.Fatal("version-rejected handshake opened a session")
	}
	if len(out.envs) != 2 {
		t.Fatalf("envelopes = %d, want exactly 2 (noise_resp, then error+close)", len(out.envs))
	}
	if out.closeCode != uint16(StatusClientUpdateRequired) {
		t.Errorf("close_code = %d, want %d", out.closeCode, StatusClientUpdateRequired)
	}
	if out.ack.WorkspaceRoot != "" {
		t.Errorf("rejected ack carries workspace_root %q, want it absent", out.ack.WorkspaceRoot)
	}
	if !out.hasErr {
		t.Fatal("second envelope carries no sealed error frame")
	}
	if out.errEnv.Type != protocol.TypeError {
		t.Errorf("error envelope type = %q, want %q", out.errEnv.Type, protocol.TypeError)
	}
	if out.errEnv.InReplyTo == nil || *out.errEnv.InReplyTo != 1 {
		t.Errorf("in_reply_to = %v, want the hello id 1", out.errEnv.InReplyTo)
	}
	if out.errBody.Code != protocol.CodeClientUpdateRequired {
		t.Errorf("code = %q, want %q", out.errBody.Code, protocol.CodeClientUpdateRequired)
	}
	if out.errBody.Retryable {
		t.Error("retryable = true, want false")
	}
	if out.errBody.Message != MsgClientUpdateRequired {
		t.Errorf("message = %q, want %q", out.errBody.Message, MsgClientUpdateRequired)
	}
	if out.errBody.MinClientVersion != wantMin {
		t.Errorf("min_client_version = %q, want %q", out.errBody.MinClientVersion, wantMin)
	}
	if wantMin == "" && strings.Contains(out.errRaw, "min_client_version") {
		t.Errorf("payload carries a min_client_version key, want it omitted: %s", out.errRaw)
	}
}

// over32 is a well-formed-looking mobile version one byte past the 32-byte cap.
var over32 = "pyrycode-mobile/" + "1234567890.1234.0"

// TestV2Session_MinVersion_NoMinimumAcceptsAll is AC-1: with the minimums
// unset, as shipped, every hello is accepted as before this ticket.
func TestV2Session_MinVersion_NoMinimumAcceptsAll(t *testing.T) {
	t.Parallel()

	for _, mins := range []map[string]string{nil, ShippedMinClientVersions()} {
		for _, v := range []string{"1.0", "0.1.0", "", "pyrycode-mobile/0.0.1", over32} {
			t.Run(v, func(t *testing.T) {
				t.Parallel()
				out := runVersionHello(t, V2SessionConfig{MinClientVersions: mins}, v2TestToken, v)
				assertVersionAccepted(t, out)
				if out.ack.WorkspaceRoot != workspaceRoot() {
					t.Errorf("workspace_root = %q, want %q", out.ack.WorkspaceRoot, workspaceRoot())
				}
			})
		}
	}
}

// TestV2Session_MinVersion_Enforced is AC-2 and AC-3: a minimum applies to its
// own app only, equal passes, and once any minimum is set an app-less or
// unparsable version is rejected with min_client_version omitted.
func TestV2Session_MinVersion_Enforced(t *testing.T) {
	t.Parallel()

	mobileOnly := map[string]string{protocol.AppMobile: "1.4.0", protocol.AppDesktop: ""}
	desktopOnly := map[string]string{protocol.AppDesktop: "0.3.0"}
	both := map[string]string{protocol.AppMobile: "1.4.0", protocol.AppDesktop: "0.3.0"}

	cases := []struct {
		name    string
		mins    map[string]string
		version string
		reject  bool
		wantMin string
	}{
		{"mobile below", mobileOnly, "pyrycode-mobile/1.3.9", true, "1.4.0"},
		{"mobile major below", both, "pyrycode-mobile/0.99.99", true, "1.4.0"},
		{"mobile equal", mobileOnly, "pyrycode-mobile/1.4.0", false, ""},
		{"mobile above", mobileOnly, "pyrycode-mobile/1.10.0", false, ""},
		{"desktop below", both, "pyrycode-desktop/0.2.9", true, "0.3.0"},
		{"desktop equal", both, "pyrycode-desktop/0.3.0", false, ""},
		{"desktop not held to mobile minimum", mobileOnly, "pyrycode-desktop/0.0.1", false, ""},
		{"mobile not held to desktop minimum", desktopOnly, "pyrycode-mobile/0.0.1", false, ""},
		{"app with no minimum", both, "pyrycode-android/0.0.1", false, ""},
		{"legacy two-part", mobileOnly, "1.0", true, ""},
		{"legacy three-part", desktopOnly, "0.1.0", true, ""},
		{"empty", both, "", true, ""},
		{"space form", mobileOnly, "pyrycode-mobile 0.1.0", true, ""},
		{"over 32 bytes", mobileOnly, over32, true, ""},
		{"leading zero", mobileOnly, "pyrycode-mobile/01.4.0", true, ""},
		{"pre-release suffix", mobileOnly, "pyrycode-mobile/1.4.0-beta", true, ""},
		{"build suffix", mobileOnly, "pyrycode-mobile/1.4.0+42", true, ""},
		{"overflow", mobileOnly, "pyrycode-mobile/99999999999999999999.0.0", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := runVersionHello(t, V2SessionConfig{MinClientVersions: tc.mins}, v2TestToken, tc.version)
			if tc.reject {
				assertVersionRejected(t, out, tc.wantMin)
				return
			}
			assertVersionAccepted(t, out)
		})
	}
}

// TestV2Session_MinVersion_RejectRecordsNothing is AC-4's first half: a
// version-rejected handshake clears no redemption deadline and stores no client
// version, on disk or in memory.
func TestV2Session_MinVersion_RejectRecordsNothing(t *testing.T) {
	t.Parallel()

	redeemBy := time.Now().UTC().Add(devices.RedemptionWindow).Truncate(time.Second)
	reg, path := redemptionFixture(t, redeemBy)
	before := backdate(t, path)

	out := runVersionHello(t, V2SessionConfig{
		Devices:           reg,
		DevicesPath:       path,
		MinClientVersions: map[string]string{protocol.AppMobile: "1.4.0"},
	}, v2TestToken, "pyrycode-mobile/1.0.0")
	assertVersionRejected(t, out, "1.4.0")

	assertNotRewritten(t, path, before)
	dev := diskDevice(t, path)
	if !dev.RedeemBy.Equal(redeemBy) {
		t.Errorf("on-disk RedeemBy = %v, want the untouched %v", dev.RedeemBy, redeemBy)
	}
	if dev.ClientVersion != "v2-test" {
		t.Errorf("on-disk ClientVersion = %q, want the seeded %q", dev.ClientVersion, "v2-test")
	}
	mem, ok := reg.FindByTokenHash(devices.HashToken(v2TestToken))
	if !ok {
		t.Fatal("paired device missing from the in-memory registry")
	}
	if mem.RedeemBy.IsZero() || mem.ClientVersion != "v2-test" {
		t.Errorf("in-memory record changed: RedeemBy %v, ClientVersion %q", mem.RedeemBy, mem.ClientVersion)
	}
}

// TestV2Session_MinVersion_BadTokenStill4401 is AC-4's second half: the version
// is examined only after the token is accepted, so a bad token gets
// auth.invalid_token / 4401 whatever client_version says.
func TestV2Session_MinVersion_BadTokenStill4401(t *testing.T) {
	t.Parallel()

	for _, v := range []string{"1.0", "pyrycode-mobile/0.0.1", "pyrycode-mobile/9.9.9"} {
		t.Run(v, func(t *testing.T) {
			t.Parallel()
			out := runVersionHello(t, V2SessionConfig{
				MinClientVersions: map[string]string{protocol.AppMobile: "1.4.0"},
			}, "wrong-token-xxxx", v)
			if out.open {
				t.Fatal("bad token opened a session")
			}
			if out.closeCode != uint16(StatusUnauthorized) {
				t.Errorf("close_code = %d, want %d", out.closeCode, StatusUnauthorized)
			}
			if !out.hasErr || out.errBody.Code != protocol.CodeAuthInvalidToken {
				t.Errorf("error code = %q, want %q", out.errBody.Code, protocol.CodeAuthInvalidToken)
			}
			if out.errBody.MinClientVersion != "" {
				t.Errorf("min_client_version = %q leaked to an unauthenticated peer", out.errBody.MinClientVersion)
			}
		})
	}
}

// TestV2Session_MinVersion_RejectLog pins the log discipline: the event and a
// closed-set reason, the app and minimum on below_minimum only, and never the
// raw client_version.
func TestV2Session_MinVersion_RejectLog(t *testing.T) {
	t.Parallel()

	const marker = "zq-marker"
	cases := []struct {
		name, version, reason string
		wantApp               bool
	}{
		{"below_minimum", "pyrycode-mobile/1.0.0", "below_minimum", true},
		{"unparsable", "pyrycode-mobile/1.0.0-" + marker, "unparsable", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger, buf := bufferLogger()
			runVersionHello(t, V2SessionConfig{
				Logger:            logger,
				MinClientVersions: map[string]string{protocol.AppMobile: "1.4.0"},
			}, v2TestToken, tc.version)
			waitForLogContains(t, buf, "v2.handshake.reject.client_update_required")
			got := buf.String()
			if !strings.Contains(got, "reason="+tc.reason) {
				t.Errorf("log lacks reason=%s:\n%s", tc.reason, got)
			}
			if strings.Contains(got, tc.version) || strings.Contains(got, marker) {
				t.Errorf("log carries the raw client_version:\n%s", got)
			}
			if strings.Contains(got, v2TestToken) {
				t.Errorf("log carries the token:\n%s", got)
			}
			hasApp := strings.Contains(got, "app="+protocol.AppMobile) && strings.Contains(got, "min_client_version=1.4.0")
			if hasApp != tc.wantApp {
				t.Errorf("app and minimum logged = %v, want %v:\n%s", hasApp, tc.wantApp, got)
			}
		})
	}
}

// TestNewV2SessionManager_MinClientVersions is AC-5's config half: a malformed
// minimum fails construction rather than reading as "no minimum", and the
// shipped build constants construct cleanly — so a bad constant fails make
// check before it ships.
func TestNewV2SessionManager_MinClientVersions(t *testing.T) {
	t.Parallel()

	respPriv, _ := genV2Keypair(t)
	build := func(mins map[string]string) error {
		_, err := NewV2SessionManager(V2SessionConfig{
			Frames:            make(chan protocol.RoutingEnvelope),
			Outbound:          func(protocol.RoutingEnvelope) error { return nil },
			StaticPriv:        respPriv,
			Devices:           &devices.Registry{},
			ServerID:          v2TestServerID,
			Logger:            silentLogger(),
			MinClientVersions: mins,
		})
		return err
	}

	if err := build(ShippedMinClientVersions()); err != nil {
		t.Fatalf("shipped minimums rejected: %v", err)
	}
	for app, v := range ShippedMinClientVersions() {
		if v == "" {
			continue
		}
		if _, ok := protocol.ParseVersion(v); !ok {
			t.Errorf("shipped minimum for %s = %q does not parse as MAJOR.MINOR.PATCH", app, v)
		}
	}

	good := []map[string]string{
		nil,
		{protocol.AppMobile: ""},
		{protocol.AppMobile: "1.4.0", protocol.AppDesktop: "0.3.0"},
		{"pyrycode-android": "2.0.0"},
	}
	for _, mins := range good {
		if err := build(mins); err != nil {
			t.Errorf("MinClientVersions %v rejected: %v", mins, err)
		}
	}

	bad := []map[string]string{
		{protocol.AppMobile: "1.0"},
		{protocol.AppMobile: "01.0.0"},
		{protocol.AppMobile: "1.0.0-rc"},
		{protocol.AppDesktop: "v1.0.0"},
		{"Pyrycode-Mobile": "1.0.0"},
		{"": "1.0.0"},
		{"pyrycode/mobile": "1.0.0"},
	}
	for _, mins := range bad {
		if err := build(mins); err == nil {
			t.Errorf("MinClientVersions %v accepted, want a construction error", mins)
		}
	}
}
