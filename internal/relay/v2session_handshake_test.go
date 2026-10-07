package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- tests ---

// TestV2Session_HappyPath drives a paired-device handshake through the
// manager and asserts state advances to open, the noise_resp carries a
// decoded hello_ack with ProtocolVersion="v2", and no close envelope is
// emitted.
func TestV2Session_HappyPath(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	frames := make(chan protocol.RoutingEnvelope, 1)
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

	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarlyData(t, v2TestToken))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg)

	envs := waitForEnvelopes(t, rec, 1)
	if len(envs) != 1 {
		t.Fatalf("envs: got %d, want exactly 1", len(envs))
	}
	respRaw := decodeRespFrame(t, envs[0])
	if envs[0].CloseCode != 0 {
		t.Errorf("happy path emitted close_code=%d, want 0", envs[0].CloseCode)
	}

	earlyAck, _, _, err := initiator.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("initiator.ReadResp: %v", err)
	}
	var ackEnv protocol.Envelope
	if err := json.Unmarshal(earlyAck, &ackEnv); err != nil {
		t.Fatalf("decode hello_ack envelope: %v", err)
	}
	if ackEnv.Type != protocol.TypeHelloAck {
		t.Errorf("ack type = %q, want %q", ackEnv.Type, protocol.TypeHelloAck)
	}
	var ackPayload protocol.HelloAckPayload
	if err := json.Unmarshal(ackEnv.Payload, &ackPayload); err != nil {
		t.Fatalf("decode hello_ack payload: %v", err)
	}
	if ackPayload.ProtocolVersion != "v2" {
		t.Errorf("ack ProtocolVersion = %q, want %q", ackPayload.ProtocolVersion, "v2")
	}
	if ackPayload.ServerID != v2TestServerID {
		t.Errorf("ack ServerID = %q, want %q", ackPayload.ServerID, v2TestServerID)
	}
	if ackPayload.ConnID != v2TestConnID {
		t.Errorf("ack ConnID = %q, want %q", ackPayload.ConnID, v2TestConnID)
	}

	// Manager state: the session must be in Open with CipherStates live.
	// Stop the manager so reads of mgr.sessions are race-free.
	stop()
	s := mgr.sessions[v2TestConnID]
	if s == nil {
		t.Fatalf("session for %q missing", v2TestConnID)
	}
	if got := s.State(); got != V2StateOpen {
		t.Errorf("state = %v, want V2StateOpen", got)
	}
	if s.send == nil || s.recv == nil {
		t.Errorf("CipherStates nil after Open: send=%v recv=%v", s.send, s.recv)
	}
}

func TestV2Session_HelloAckWorkspaceRoot(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, "operator-workspace")
	verbatim := home + "/missing/../operator-workspace/"
	empty, relative := "", "relative-workspace"
	for _, tc := range []struct {
		name string
		home string
		base *string
		want string
	}{
		{"legacy default", home, nil, filepath.Join(home, "pyry-workspace")},
		{"home unavailable", "", nil, ""},
		{"home not absolute", "relative-home", nil, ""},
		{"supplied absolute", home, &base, base},
		{"supplied absolute verbatim", home, &verbatim, verbatim},
		{"supplied absolute without home", "", &base, base},
		{"supplied absolute with relative home", "relative-home", &base, base},
		{"explicit empty overrides default", home, &empty, ""},
		{"explicit relative overrides default", home, &relative, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", tc.home)
			if tc.want != "" {
				if _, err := os.Stat(filepath.Clean(tc.want)); !os.IsNotExist(err) {
					t.Fatalf("workspace root before handshake: got err %v, want not-exist", err)
				}
			}
			logger, logs := bufferLogger()
			out := runVersionHello(t, V2SessionConfig{WorkspaceBase: tc.base, Logger: logger}, v2TestToken, "v2-test")
			assertVersionAccepted(t, out)
			if out.ack.WorkspaceRoot != tc.want {
				t.Errorf("WorkspaceRoot = %q, want %q", out.ack.WorkspaceRoot, tc.want)
			}
			if bytes.Contains(out.ackRaw, []byte(`"workspace_root"`)) != (tc.want != "") {
				t.Errorf("workspace_root key presence does not match value %q: %s", tc.want, out.ackRaw)
			}
			if out.ack.ProtocolVersion != "v2" || out.ack.ServerID != v2TestServerID || out.ack.ConnID != v2TestConnID {
				t.Errorf("existing greeting fields changed: got %+v", out.ack)
			}
			if tc.want != "" {
				assertWorkspaceBasePrivate(t, out, logs.String(), tc.want)
				if _, err := os.Stat(filepath.Clean(tc.want)); !os.IsNotExist(err) {
					t.Fatalf("workspace root after handshake: got err %v, want not-exist", err)
				}
			}
		})
	}
}

func assertWorkspaceBasePrivate(t *testing.T, out versionOutcome, logs, base string) {
	t.Helper()
	wireJSON, err := json.Marshal(out.envs)
	if err != nil {
		t.Fatalf("marshal recorded wire: %v", err)
	}
	if bytes.Contains(wireJSON, []byte(base)) {
		t.Errorf("workspace base exposed outside encrypted early data: %s", wireJSON)
	}
	if strings.Contains(logs, base) {
		t.Errorf("workspace base exposed in logs: %s", logs)
	}
}

func TestV2Session_HelloAckWorkspaceRoot_Rejected(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ reason, event string }{
		{"unknown token", "v2.handshake.reject.invalid_token"},
		{"expired token", "v2.handshake.reject.redemption_window_elapsed"},
		{"static key mismatch", "v2.handshake.reject.static_key_mismatch"},
		{"version", "v2.handshake.reject.client_update_required"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			t.Parallel()
			base := filepath.Join(t.TempDir(), "private-operator-workspace")
			logger, logs := bufferLogger()
			reg := &devices.Registry{}
			device := devices.Device{TokenHash: devices.HashToken(v2TestToken), Name: v2TestDevName}
			cfg := V2SessionConfig{WorkspaceBase: &base, Devices: reg, Logger: logger}
			token, version := v2TestToken, "v2-test"
			switch tc.reason {
			case "unknown token":
				token = "wrong-token"
			case "expired token":
				device.RedeemBy = time.Now().Add(-time.Minute)
			case "static key mismatch":
				_, key := installKey(t, 1)
				device.StaticKey = key
			case "version":
				cfg.MinClientVersions = map[string]string{protocol.AppMobile: "1.4.0"}
				version = "pyrycode-mobile/1.0.0"
			}
			reg.Add(device)
			out := runVersionHello(t, cfg, token, version)
			if tc.reason == "version" {
				assertVersionRejected(t, out, "1.4.0")
			} else {
				if out.open || len(out.envs) != 2 || out.closeCode != uint16(StatusUnauthorized) {
					t.Fatalf("open=%v, envelopes=%d, close=%d; want false, 2, 4401", out.open, len(out.envs), out.closeCode)
				}
				if !out.hasErr || out.errEnv.Type != protocol.TypeError || out.errEnv.InReplyTo == nil || *out.errEnv.InReplyTo != 1 ||
					out.errBody.Code != protocol.CodeAuthInvalidToken || out.errBody.Message != MsgInvalidToken || out.errBody.Retryable {
					t.Errorf("unexpected encrypted rejection: envelope=%+v, payload=%+v", out.errEnv, out.errBody)
				}
			}
			if out.ack.WorkspaceRoot != "" || bytes.Contains(out.ackRaw, []byte(`"workspace_root"`)) {
				t.Errorf("rejected ack disclosed workspace_root: %s", out.ackRaw)
			}
			if !strings.Contains(logs.String(), tc.event) {
				t.Errorf("rejection did not reach %q: %s", tc.event, logs.String())
			}
			assertWorkspaceBasePrivate(t, out, logs.String(), base)
		})
	}
}

// TestV2Session_BadToken_AEADErrorThen4401 drives a handshake whose
// hello carries an unpaired token. The manager must emit the noise_resp,
// then a SINGLE routing envelope that carries an AEAD-sealed
// auth.invalid_token error envelope AND CloseCode=4401, in order.
func TestV2Session_BadToken_AEADErrorThen4401(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := &devices.Registry{} // empty: every token rejects

	frames := make(chan protocol.RoutingEnvelope, 1)
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

	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarlyData(t, "wrong-token-xxxx"))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg)

	envs := waitForEnvelopes(t, rec, 2)
	if len(envs) != 2 {
		t.Fatalf("envs: got %d, want exactly 2", len(envs))
	}

	// Envelope 0: noise_resp, no close.
	respRaw := decodeRespFrame(t, envs[0])
	if envs[0].CloseCode != 0 {
		t.Errorf("noise_resp envelope emitted close_code=%d, want 0", envs[0].CloseCode)
	}
	_, _, initRecv, err := initiator.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("initiator.ReadResp: %v", err)
	}

	// Envelope 1: noise_msg + CloseCode=4401 in ONE routing envelope.
	if envs[1].CloseCode != uint16(StatusUnauthorized) {
		t.Errorf("envs[1].CloseCode = %d, want %d", envs[1].CloseCode, StatusUnauthorized)
	}
	if envs[1].Frame == nil {
		t.Fatal("envs[1].Frame is nil; expected AEAD-sealed error frame")
	}
	ciphertext := decodeNoiseMsg(t, envs[1])
	plaintext, err := initRecv.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("initRecv.Decrypt: %v", err)
	}
	var errEnv protocol.Envelope
	if err := json.Unmarshal(plaintext, &errEnv); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if errEnv.Type != protocol.TypeError {
		t.Errorf("error envelope type = %q, want %q", errEnv.Type, protocol.TypeError)
	}
	var ep protocol.ErrorPayload
	if err := json.Unmarshal(errEnv.Payload, &ep); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if ep.Code != protocol.CodeAuthInvalidToken {
		t.Errorf("error code = %q, want %q", ep.Code, protocol.CodeAuthInvalidToken)
	}
}

// TestV2CloseCodes_MatchSpec pins every close code the daemon sends to its wire
// value (docs/protocol-mobile.md § Error codes, close-code table). The constants
// are compared against literals outside this module — the apps' close handlers —
// so a fat-fingered value would be self-consistent in every test that uses the
// constant symbolically. It also asserts the daemon's codes are pairwise distinct
// and disjoint from the other codes in use (1000, 1011, and the relay-sent 4404,
// 4409, 4429), which is what StatusClientUpdateRequired's "a value not in use"
// means as a check (#2576).
func TestV2CloseCodes_MatchSpec(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		got  websocket.StatusCode
		want websocket.StatusCode
	}{
		{"StatusUnauthorized", StatusUnauthorized, 4401},
		{"StatusIdleTimeout", StatusIdleTimeout, 4408},
		{"StatusSessionGone", StatusSessionGone, 4410},
		{"StatusClientUpdateRequired", StatusClientUpdateRequired, 4412},
		{"StatusQueueOverflow", StatusQueueOverflow, 4413},
		{"StatusProtocolMismatch", StatusProtocolMismatch, 4421},
		{"StatusHandshakeFailure", StatusHandshakeFailure, 4426},
	}
	seen := map[websocket.StatusCode]string{
		websocket.StatusNormalClosure: "1000 normal closure",
		websocket.StatusInternalError: "1011 server error",
		4404:                          "4404 relay: no server",
		statusServerIDConflict:        "4409 relay: server-id conflict",
		4429:                          "4429 relay: rate limited",
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
		if other, dup := seen[c.got]; dup {
			t.Errorf("%s = %d collides with %s", c.name, c.got, other)
		}
		seen[c.got] = c.name
	}
}

// TestV2Session_IKReject_4426 feeds random bytes as the noise_init data.
// The Noise responder rejects at ReadInit's MAC step; the manager must
// close with 4426 (no AEAD-sealed error frame, no noise_resp).
func TestV2Session_IKReject_4426(t *testing.T) {
	t.Parallel()

	respPriv, _ := genV2Keypair(t)
	reg := &devices.Registry{}

	frames := make(chan protocol.RoutingEnvelope, 1)
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

	garbage := make([]byte, 96) // IK message 1 is ~96 bytes
	if _, err := rand.Read(garbage); err != nil {
		t.Fatalf("rand: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, garbage)

	envs := waitForEnvelopes(t, rec, 1)
	if len(envs) != 1 {
		t.Fatalf("envs: got %d, want exactly 1", len(envs))
	}
	if envs[0].CloseCode != uint16(StatusHandshakeFailure) {
		t.Errorf("close_code = %d, want %d", envs[0].CloseCode, StatusHandshakeFailure)
	}
	if envs[0].Frame != nil {
		t.Errorf("Frame = %s, want nil (close-only at 4426)", string(envs[0].Frame))
	}
}

// TestV2Session_Gating_NoiseMsgInHandshakeComplete_4401 is the
// load-bearing gating invariant. Setup: run a complete handshake on the
// side, capture both endpoints' CipherStates, then construct a fresh
// V2Session whose state is V2StateHandshakeComplete (CipherStates live,
// token NOT validated). Feed a noise_msg whose AEAD plaintext is a
// non-hello envelope (a send_message stand-in). Manager must reject
// with AEAD-sealed auth.invalid_token + 4401, structurally proving the
// "handler chain unreachable from handshakeComplete" invariant.
func TestV2Session_Gating_NoiseMsgInHandshakeComplete_4401(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)

	// Run a real handshake to obtain a matched (initSend, initRecv) and
	// (respSend, respRecv) pair. The respSend/respRecv are what the
	// V2Session would hold post-WriteResp.
	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	responder, err := noise.NewResponder(respPriv)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	initMsg, err := initiator.WriteInit([]byte("{}"))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	if _, err := responder.ReadInit(initMsg); err != nil {
		t.Fatalf("ReadInit: %v", err)
	}
	respMsg, respSend, respRecv, err := responder.WriteResp([]byte("{}"))
	if err != nil {
		t.Fatalf("WriteResp: %v", err)
	}
	_, initSend, initRecv, err := initiator.ReadResp(respMsg)
	if err != nil {
		t.Fatalf("ReadResp: %v", err)
	}

	frames := make(chan protocol.RoutingEnvelope, 1)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    &devices.Registry{},
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	})
	t.Cleanup(stop)

	// Inject a session directly in handshakeComplete. Done before any
	// frames are fed in so the manager's loop observes it on the first
	// dispatch. The map mutation here races with mgr.Run only if Run
	// processes a frame for this conn_id concurrently — we send the
	// first frame for v2TestConnID AFTER the assignment, on the same
	// goroutine, so the happens-before ordering is: assign → channel-send
	// → Run's channel-recv → Run's map lookup. The channel-send is the
	// synchronization point.
	mgr.sessions[v2TestConnID] = &V2Session{
		connID: v2TestConnID,
		state:  V2StateHandshakeComplete,
		send:   respSend,
		recv:   respRecv,
	}

	// Produce a non-hello envelope, AEAD-seal it under initSend, and
	// feed via noise_msg.
	payload, err := json.Marshal(map[string]string{"text": "should be ignored"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	envBytes, err := json.Marshal(protocol.Envelope{
		ID:      99,
		Type:    protocol.TypeSendMessage,
		TS:      time.Now().UTC(),
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ciphertext, err := initSend.Encrypt(envBytes)
	if err != nil {
		t.Fatalf("initSend.Encrypt: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseMsg, ciphertext)

	envs := waitForEnvelopes(t, rec, 1)
	if len(envs) != 1 {
		t.Fatalf("envs: got %d, want exactly 1", len(envs))
	}
	if envs[0].CloseCode != uint16(StatusUnauthorized) {
		t.Errorf("close_code = %d, want %d", envs[0].CloseCode, StatusUnauthorized)
	}
	if envs[0].Frame == nil {
		t.Fatal("Frame is nil; expected AEAD-sealed error frame")
	}
	sealed := decodeNoiseMsg(t, envs[0])
	plaintext, err := initRecv.Decrypt(sealed)
	if err != nil {
		t.Fatalf("initRecv.Decrypt: %v", err)
	}
	var errEnv protocol.Envelope
	if err := json.Unmarshal(plaintext, &errEnv); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	var ep protocol.ErrorPayload
	if err := json.Unmarshal(errEnv.Payload, &ep); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if ep.Code != protocol.CodeAuthInvalidToken {
		t.Errorf("error code = %q, want %q", ep.Code, protocol.CodeAuthInvalidToken)
	}
}

// TestV2Session_OutOfStateRejections covers the malformed-frame /
// unknown-type / bad-version rows from the spec's transition table.
// All paths drop to a close-only routing envelope; every case closes at
// 4421 except noise_msg_before_handshake, which closes at the retryable
// 4410 so a client still holding cipher state for a session the daemon
// dropped re-handshakes instead of stopping on a fatal code (#2488).
func TestV2Session_OutOfStateRejections(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		frame    json.RawMessage
		wantCode uint16
	}{
		{
			name:     "non_json_frame",
			frame:    json.RawMessage(`not json`),
			wantCode: uint16(StatusProtocolMismatch),
		},
		{
			name:     "wrong_version",
			frame:    mustMarshalFrame(t, protocol.InnerFrameV2{Version: 99, Type: protocol.TypeNoiseInit, Data: ""}),
			wantCode: uint16(StatusProtocolMismatch),
		},
		{
			name:     "unknown_type",
			frame:    mustMarshalFrame(t, protocol.InnerFrameV2{Version: protocol.V2Version, Type: "ascii-banana", Data: ""}),
			wantCode: uint16(StatusProtocolMismatch),
		},
		{
			name:     "bad_base64_data",
			frame:    mustMarshalFrame(t, protocol.InnerFrameV2{Version: protocol.V2Version, Type: protocol.TypeNoiseInit, Data: "!!!"}),
			wantCode: uint16(StatusProtocolMismatch),
		},
		{
			name:     "noise_resp_from_phone",
			frame:    mustMarshalFrame(t, protocol.InnerFrameV2{Version: protocol.V2Version, Type: protocol.TypeNoiseResp, Data: base64.StdEncoding.EncodeToString([]byte("x"))}),
			wantCode: uint16(StatusProtocolMismatch),
		},
		{
			name:     "noise_msg_before_handshake",
			frame:    mustMarshalFrame(t, protocol.InnerFrameV2{Version: protocol.V2Version, Type: protocol.TypeNoiseMsg, Data: base64.StdEncoding.EncodeToString([]byte("x"))}),
			wantCode: uint16(StatusSessionGone),
		},
	}

	respPriv, _ := genV2Keypair(t)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frames := make(chan protocol.RoutingEnvelope, 1)
			rec := &v2Recorder{}
			_, stop := startManager(t, V2SessionConfig{
				Frames:     frames,
				Outbound:   rec.outbound,
				StaticPriv: respPriv,
				Devices:    &devices.Registry{},
				ServerID:   v2TestServerID,
				Logger:     silentLogger(),
			})
			t.Cleanup(stop)

			frames <- protocol.RoutingEnvelope{ConnID: v2TestConnID, Frame: tc.frame}

			envs := waitForEnvelopes(t, rec, 1)
			if len(envs) != 1 {
				t.Fatalf("envs: got %d, want exactly 1", len(envs))
			}
			if envs[0].CloseCode != tc.wantCode {
				t.Errorf("close_code = %d, want %d", envs[0].CloseCode, tc.wantCode)
			}
			if envs[0].Frame != nil {
				t.Errorf("Frame = %s, want nil", string(envs[0].Frame))
			}
		})
	}
}

// TestV2Session_InitialHandshake_CapturesPeerStatic pins the capture
// site in handleNoiseInit: after the initial IK handshake reaches
// V2StateOpen, V2Session.peerStatic must equal the initiator's static
// public key. Consumed by the re-key responder's continuity check
// (#453); inert in this slice.
func TestV2Session_InitialHandshake_CapturesPeerStatic(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, initPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	frames := make(chan protocol.RoutingEnvelope, 2)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	sess.stop()
	s := sess.mgr.sessions[v2TestConnID]
	if s == nil {
		t.Fatalf("session for %q missing after handshake", v2TestConnID)
	}
	if len(s.peerStatic) != noise.KeyLen {
		t.Fatalf("peerStatic len = %d, want %d", len(s.peerStatic), noise.KeyLen)
	}
	if !bytes.Equal(s.peerStatic, initPub) {
		t.Errorf("peerStatic mismatch: got %x, want %x", s.peerStatic, initPub)
	}
}

// --- capability negotiation (#626) tests ---

// TestNegotiateCapabilities is the AC#2/#3 matrix for the pure intersection:
// advertised ∩ supportedV2Capabilities, in supported-set order. Because the
// function iterates the supported set and filters by the advertised one, the
// output is a subset of supported by construction — an unsupported/spoofed
// advertisement is never a candidate, and duplicates collapse.
func TestNegotiateCapabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		advertised []string
		want       []string
	}{
		{"interactive granted", []string{protocol.CapabilityInteractive}, []string{protocol.CapabilityInteractive}},
		{"unsupported dropped", []string{protocol.CapabilityInteractive, "snapshot-unknown"}, []string{protocol.CapabilityInteractive}},
		{"only unsupported yields nil", []string{"snapshot-unknown"}, nil},
		{"nil yields nil", nil, nil},
		{"empty yields nil", []string{}, nil},
		{"duplicates collapse", []string{protocol.CapabilityInteractive, protocol.CapabilityInteractive}, []string{protocol.CapabilityInteractive}},
		// #2020's second supported member. The last two rows advertise the same
		// set in both orders and expect the same output, pinning that the result
		// is ordered by the SUPPORTED set — the client's order never reaches it.
		{"question granted", []string{protocol.CapabilityQuestion}, []string{protocol.CapabilityQuestion}},
		{"both granted", []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion}, []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion}},
		{"both granted, advertised in reverse", []string{protocol.CapabilityQuestion, protocol.CapabilityInteractive}, []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion}},
		// #2172's third supported member, appended after CapabilityQuestion. The
		// last two rows advertise all three in supported order and in reverse and
		// expect the same output, so they fail both if model_list is missing from
		// the supported set and if it is inserted anywhere but the end — the
		// slices.Equal is order-sensitive and the emit order is the supported-set
		// order, not the client's.
		{"model list granted", []string{protocol.CapabilityModelList}, []string{protocol.CapabilityModelList}},
		{"all three granted", []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion, protocol.CapabilityModelList}, []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion, protocol.CapabilityModelList}},
		{"all three granted, advertised in reverse", []string{protocol.CapabilityModelList, protocol.CapabilityQuestion, protocol.CapabilityInteractive}, []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion, protocol.CapabilityModelList}},
		// #2431's fourth supported member, APPENDED after CapabilityModelList. The
		// last row advertises all four in reverse and expects supported order, so it
		// fails both if context_usage is missing from the supported set and if it is
		// inserted anywhere but the end.
		{"context usage granted", []string{protocol.CapabilityContextUsage}, []string{protocol.CapabilityContextUsage}},
		{"all four granted", []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion, protocol.CapabilityModelList, protocol.CapabilityContextUsage}, []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion, protocol.CapabilityModelList, protocol.CapabilityContextUsage}},
		{"all four granted, advertised in reverse", []string{protocol.CapabilityContextUsage, protocol.CapabilityModelList, protocol.CapabilityQuestion, protocol.CapabilityInteractive}, []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion, protocol.CapabilityModelList, protocol.CapabilityContextUsage}},
		// #2643's fifth supported member, APPENDED after CapabilityContextUsage, for
		// the reason the rows above give.
		{"multi agent granted", []string{protocol.CapabilityMultiAgent}, []string{protocol.CapabilityMultiAgent}},
		{"all five granted, advertised in reverse", []string{protocol.CapabilityMultiAgent, protocol.CapabilityContextUsage, protocol.CapabilityModelList, protocol.CapabilityQuestion, protocol.CapabilityInteractive}, []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion, protocol.CapabilityModelList, protocol.CapabilityContextUsage, protocol.CapabilityMultiAgent}},
		{"stop background task granted", []string{"stop_background_task"}, []string{"stop_background_task"}},
		{"all six reversed with duplicate and unknown", []string{"stop_background_task", protocol.CapabilityMultiAgent, protocol.CapabilityContextUsage, protocol.CapabilityModelList, protocol.CapabilityQuestion, protocol.CapabilityInteractive, "stop_background_task", "snapshot-unknown"}, []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion, protocol.CapabilityModelList, protocol.CapabilityContextUsage, protocol.CapabilityMultiAgent, "stop_background_task"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := negotiateCapabilities(tc.advertised); !slices.Equal(got, tc.want) {
				t.Errorf("negotiateCapabilities(%v) = %v, want %v", tc.advertised, got, tc.want)
			}
		})
	}
}

// TestV2Session_Handshake_CapabilityNegotiation drives a paired-device
// handshake to open for each advertisement, decodes the hello_ack early-data,
// and asserts (a) the ack echoes exactly the negotiated intersection, (b) a
// no-grant negotiation drops the capabilities key entirely (omitempty
// byte-stability, AC#5), and (c) the per-conn negotiated flag surfaced by
// ActiveConns matches the echo (AC#1/#2/#3). A spoofed "god-mode" is never
// echoed nor flagged.
//
// #2020 added the question rows. Its AC#2 — the ack carries interactive and NOT
// question when the client advertised interactive alone — is pinned by the
// pre-existing "advertise interactive" row rather than by a new one: that row's
// slices.Equal against a one-element want already fails against an
// implementation that appends the daemon's supported strings unconditionally
// instead of echoing the intersection.
func TestV2Session_Handshake_CapabilityNegotiation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		advertised []string
		wantAck    []string // expected ack.Capabilities (nil → key absent)
		wantFlag   bool
	}{
		{"advertise interactive", []string{protocol.CapabilityInteractive}, []string{protocol.CapabilityInteractive}, true},
		{"advertise nothing", nil, nil, false},
		{"spoof drops unsupported", []string{protocol.CapabilityInteractive, "god-mode"}, []string{protocol.CapabilityInteractive}, true},
		{"only unsupported granted nothing", []string{"god-mode"}, nil, false},
		{"advertise interactive and question", []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion}, []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion}, true},
		// #2020 AC#3: question grants no interactive access. This is the only row
		// in either capability table that separates the shipped value-specific
		// reduction (slices.Contains(negotiated, CapabilityInteractive)) from an
		// emptiness test — under `len(negotiated) > 0` every other row stays green.
		// It is also the first state in which the ack carries a capabilities key
		// on a non-interactive conn, so assertion (b) below is correctly skipped.
		{"question alone grants no interactive", []string{protocol.CapabilityQuestion}, []string{protocol.CapabilityQuestion}, false},
		{"advertise all three", []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion, protocol.CapabilityModelList}, []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion, protocol.CapabilityModelList}, true},
		// #2172 AC#2, the direct analogue of the question row above: model_list is
		// the second member grantable to a client that stays non-interactive. It
		// reddens if anyone ever reduces the flag derivation in handleNoiseInit
		// from a slices.Contains against CapabilityInteractive to a
		// len(negotiated) > 0 test, which would hand this client the whole
		// interactive stream on the strength of a detection-only string.
		{"model list alone grants no interactive", []string{protocol.CapabilityModelList}, []string{protocol.CapabilityModelList}, false},
		// #2431 AC-5, the echo half: a client advertising the new string has it
		// echoed back when the daemon supports the verb.
		{"advertise all four", []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion, protocol.CapabilityModelList, protocol.CapabilityContextUsage}, []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion, protocol.CapabilityModelList, protocol.CapabilityContextUsage}, true},
		// #2431 AC-5, the third row of the detection-only family, for the reason the
		// two above it give: context_usage is grantable to a client that stays
		// non-interactive, so this reddens on a len(negotiated) > 0 reduction too.
		{"context usage alone grants no interactive", []string{protocol.CapabilityContextUsage}, []string{protocol.CapabilityContextUsage}, false},
		// #2431 AC-5, the OTHER half and the one that would go unwritten: a client
		// advertising interactive WITHOUT the new string is negotiated exactly as it
		// was before the string existed. That is what keeps pyrycode-mobile, which
		// advertises interactive and nothing else, able to use the verb — the
		// handler gates on this flag alone and never on the capability.
		{"interactive without context usage still interactive", []string{protocol.CapabilityInteractive}, []string{protocol.CapabilityInteractive}, true},
		// #2643 AC-1: multi_agent is echoed and grants nothing interactive.
		{"multi agent alone grants no interactive", []string{protocol.CapabilityMultiAgent}, []string{protocol.CapabilityMultiAgent}, false},
		{"advertise all five", []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion, protocol.CapabilityModelList, protocol.CapabilityContextUsage, protocol.CapabilityMultiAgent}, []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion, protocol.CapabilityModelList, protocol.CapabilityContextUsage, protocol.CapabilityMultiAgent}, true},
		{"stop background task alone grants no interactive", []string{"stop_background_task"}, []string{"stop_background_task"}, false},
		{"advertise all six reversed with duplicate and unknown", []string{"stop_background_task", protocol.CapabilityMultiAgent, protocol.CapabilityContextUsage, protocol.CapabilityModelList, protocol.CapabilityQuestion, protocol.CapabilityInteractive, "stop_background_task", "god-mode"}, []string{protocol.CapabilityInteractive, protocol.CapabilityQuestion, protocol.CapabilityModelList, protocol.CapabilityContextUsage, protocol.CapabilityMultiAgent, "stop_background_task"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			respPriv, respPub := genV2Keypair(t)
			initPriv, _ := genV2Keypair(t)
			reg := v2PairedRegistry(t, v2TestToken)
			frames := make(chan protocol.RoutingEnvelope, 1)
			rec := &v2Recorder{}
			sess, earlyAck := driveToOpenCaps(t, V2SessionConfig{
				Frames:     frames,
				Outbound:   rec.outbound,
				StaticPriv: respPriv,
				Devices:    reg,
				ServerID:   v2TestServerID,
				Logger:     silentLogger(),
			}, frames, rec, respPub, initPriv, v2TestToken, tc.advertised)
			t.Cleanup(sess.stop)

			// (a) the ack echoes exactly the negotiated intersection.
			ack := decodeHelloAck(t, earlyAck)
			if !slices.Equal(ack.Capabilities, tc.wantAck) {
				t.Errorf("hello_ack Capabilities = %v, want %v", ack.Capabilities, tc.wantAck)
			}
			// (b) a no-grant negotiation drops the key entirely — not an empty
			// array. The substring can only occur in the payload's capabilities
			// key, so its absence in the whole ack envelope is the byte check.
			if len(tc.wantAck) == 0 && bytes.Contains(earlyAck, []byte("capabilities")) {
				t.Errorf("no-grant hello_ack carries a capabilities key: %s", earlyAck)
			}

			// (c) the per-conn negotiated flag matches the echo.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if got := activeConnFor(t, sess.mgr, ctx, v2TestConnID); got.Interactive != tc.wantFlag {
				t.Errorf("ActiveConns Interactive = %v, want %v", got.Interactive, tc.wantFlag)
			}
		})
	}
}

// TestV2Session_CapabilitySpoof_TokenFail_NeverEnumerated is the security
// proof: a phone that advertises [interactive] but fails the device-token check
// is closed at 4401 and deleted, so its negotiated capability is never
// observable in ActiveConns. The pre-validation ack may echo the negotiated
// capability, which grants nothing, but it must not disclose host paths.
func TestV2Session_CapabilitySpoof_TokenFail_NeverEnumerated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := &devices.Registry{} // empty: every token rejects

	frames := make(chan protocol.RoutingEnvelope, 1)
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

	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarlyDataCaps(t, "wrong-token", []string{protocol.CapabilityInteractive}))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg)

	// noise_resp + the combined error+4401 close. Observing the close envelope
	// guarantees closeWith's delete has happened on Run, so the subsequent
	// snapshot sees the post-delete map.
	envs := waitForEnvelopes(t, rec, 2)
	if envs[1].CloseCode != uint16(StatusUnauthorized) {
		t.Fatalf("close_code = %d, want %d", envs[1].CloseCode, StatusUnauthorized)
	}

	// The ack is sealed before the token rejection so the initiator can decrypt
	// it. Capability negotiation grants no access, while host-only metadata must
	// remain absent until the token has been accepted.
	respRaw := decodeRespFrame(t, envs[0])
	earlyAck, _, _, err := initiator.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("ReadResp: %v", err)
	}
	ack := decodeHelloAck(t, earlyAck)
	if !slices.Equal(ack.Capabilities, []string{protocol.CapabilityInteractive}) {
		t.Errorf("hello_ack Capabilities = %v, want [interactive]", ack.Capabilities)
	}
	if ack.WorkspaceRoot != "" {
		t.Errorf("rejected-token hello_ack WorkspaceRoot = %q, want empty", ack.WorkspaceRoot)
	}
	if bytes.Contains(earlyAck, []byte(`"workspace_root"`)) {
		t.Errorf("rejected-token hello_ack disclosed workspace_root: %s", earlyAck)
	}

	// The grant: the token-failed conn never reaches V2StateOpen, so the
	// negotiated capability is never observable in the enumeration.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, c := range mgr.ActiveConns(ctx) {
		if c.ConnID == v2TestConnID {
			t.Errorf("token-failed conn %q enumerated (interactive=%v); a non-authenticated peer must never be granted", c.ConnID, c.Interactive)
		}
	}
}
