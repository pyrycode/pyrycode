package relay

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- test fixtures ---

const (
	v2TestConnID   = "c-v2-1"
	v2TestServerID = "server-v2"
	v2TestDevName  = "v2-test-phone"
	v2TestToken    = "v2-token-paired-deadbeef"
)

// v2Recorder is a goroutine-safe recorder of outbound routing envelopes
// suitable for V2SessionConfig.Outbound. Each captured envelope's Frame
// is deep-copied so subsequent buffer reuse cannot mutate the recording.
type v2Recorder struct {
	mu  sync.Mutex
	env []protocol.RoutingEnvelope
}

func (r *v2Recorder) outbound(env protocol.RoutingEnvelope) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if env.Frame != nil {
		fcopy := make([]byte, len(env.Frame))
		copy(fcopy, env.Frame)
		env.Frame = fcopy
	}
	r.env = append(r.env, env)
	return nil
}

func (r *v2Recorder) snapshot() []protocol.RoutingEnvelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]protocol.RoutingEnvelope, len(r.env))
	copy(out, r.env)
	return out
}

// silentLogger drops every log line; lets the tests run quietly while
// still satisfying V2SessionConfig.Logger's non-nil contract.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// genV2Keypair returns a fresh X25519 (priv, pub) pair.
func genV2Keypair(t *testing.T) (priv, pub []byte) {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	return k.Bytes(), k.PublicKey().Bytes()
}

// v2PairedRegistry returns an in-memory devices.Registry pre-populated
// with one device matching plain. Distinct from auth_test.go's
// pairedRegistry which uses a v1-specific Pixel device fixture.
func v2PairedRegistry(t *testing.T, plain string) *devices.Registry {
	t.Helper()
	reg := &devices.Registry{}
	reg.Add(devices.Device{
		TokenHash: devices.HashToken(plain),
		Name:      v2TestDevName,
		PairedAt:  time.Now().UTC(),
	})
	return reg
}

// startManager wires a V2SessionManager around the supplied channel +
// recorder and runs it in a background goroutine. The returned stop
// closure terminates the Run loop and waits for the goroutine to exit.
func startManager(t *testing.T, cfg V2SessionConfig) (*V2SessionManager, func()) {
	t.Helper()
	mgr, err := NewV2SessionManager(cfg)
	if err != nil {
		t.Fatalf("NewV2SessionManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = mgr.Run(ctx)
	}()
	return mgr, func() {
		cancel()
		<-done
	}
}

// buildHelloEarlyData marshals a v2 hello envelope (with token) suitable
// for embedding in IK message 1 early-data.
func buildHelloEarlyData(t *testing.T, token string) []byte {
	t.Helper()
	payload, err := json.Marshal(protocol.HelloClientPayload{
		Role:             "client",
		DeviceName:       v2TestDevName,
		ClientVersion:    "v2-test",
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

// wrapInnerFrame wraps raw bytes as an InnerFrameV2 + RoutingEnvelope.
func wrapInnerFrame(t *testing.T, connID, frameType string, raw []byte) protocol.RoutingEnvelope {
	t.Helper()
	frameJSON, err := json.Marshal(protocol.InnerFrameV2{
		Version: protocol.V2Version,
		Type:    frameType,
		Data:    base64.StdEncoding.EncodeToString(raw),
	})
	if err != nil {
		t.Fatalf("marshal inner frame: %v", err)
	}
	return protocol.RoutingEnvelope{ConnID: connID, Frame: frameJSON}
}

// waitForEnvelopes polls rec.snapshot() until at least n envelopes are
// recorded or the test deadline expires.
func waitForEnvelopes(t *testing.T, rec *v2Recorder, n int) []protocol.RoutingEnvelope {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		envs := rec.snapshot()
		if len(envs) >= n {
			return envs
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waitForEnvelopes: only got %d, want >= %d", len(rec.snapshot()), n)
	return nil
}

// decodeRespFrame pulls the noise_resp raw bytes out of a captured
// routing envelope's Frame.
func decodeRespFrame(t *testing.T, env protocol.RoutingEnvelope) []byte {
	t.Helper()
	if env.Frame == nil {
		t.Fatalf("decodeRespFrame: envelope has no frame")
	}
	var inner protocol.InnerFrameV2
	if err := json.Unmarshal(env.Frame, &inner); err != nil {
		t.Fatalf("decodeRespFrame: unmarshal inner: %v", err)
	}
	if inner.Type != protocol.TypeNoiseResp {
		t.Fatalf("decodeRespFrame: type %q, want noise_resp", inner.Type)
	}
	raw, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatalf("decodeRespFrame: base64: %v", err)
	}
	return raw
}

// decodeNoiseMsg pulls the AEAD ciphertext out of a captured noise_msg
// inner frame.
func decodeNoiseMsg(t *testing.T, env protocol.RoutingEnvelope) []byte {
	t.Helper()
	if env.Frame == nil {
		t.Fatalf("decodeNoiseMsg: envelope has no frame")
	}
	var inner protocol.InnerFrameV2
	if err := json.Unmarshal(env.Frame, &inner); err != nil {
		t.Fatalf("decodeNoiseMsg: unmarshal inner: %v", err)
	}
	if inner.Type != protocol.TypeNoiseMsg {
		t.Fatalf("decodeNoiseMsg: type %q, want noise_msg", inner.Type)
	}
	raw, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatalf("decodeNoiseMsg: base64: %v", err)
	}
	return raw
}

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

// TestV2Session_NoiseInitAfterOpen_4421 drives the happy path to Open
// then feeds a second noise_init for the same conn_id. Rekey is #435's
// concern; in this slice the second noise_init must be rejected at 4421.
func TestV2Session_NoiseInitAfterOpen_4421(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	frames := make(chan protocol.RoutingEnvelope, 2)
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
	initMsg, err := initiator.WriteInit(buildHelloEarlyData(t, v2TestToken))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg)
	waitForEnvelopes(t, rec, 1)

	// Now feed a second noise_init. A fresh initiator just to produce
	// well-formed bytes — the manager rejects on state, not on Noise.
	initiator2, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator2: %v", err)
	}
	initMsg2, err := initiator2.WriteInit(buildHelloEarlyData(t, v2TestToken))
	if err != nil {
		t.Fatalf("WriteInit2: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg2)

	envs := waitForEnvelopes(t, rec, 2)
	if len(envs) != 2 {
		t.Fatalf("envs: got %d, want exactly 2", len(envs))
	}
	if envs[1].CloseCode != uint16(StatusProtocolMismatch) {
		t.Errorf("close_code = %d, want %d", envs[1].CloseCode, StatusProtocolMismatch)
	}
	if envs[1].Frame != nil {
		t.Errorf("envs[1].Frame = %s, want nil (close-only at 4421)", string(envs[1].Frame))
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
// All paths drop to a 4421 close-only routing envelope.
func TestV2Session_OutOfStateRejections(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		frame json.RawMessage
	}{
		{
			name:  "non_json_frame",
			frame: json.RawMessage(`not json`),
		},
		{
			name:  "wrong_version",
			frame: mustMarshalFrame(t, protocol.InnerFrameV2{Version: 99, Type: protocol.TypeNoiseInit, Data: ""}),
		},
		{
			name:  "unknown_type",
			frame: mustMarshalFrame(t, protocol.InnerFrameV2{Version: protocol.V2Version, Type: "ascii-banana", Data: ""}),
		},
		{
			name:  "bad_base64_data",
			frame: mustMarshalFrame(t, protocol.InnerFrameV2{Version: protocol.V2Version, Type: protocol.TypeNoiseInit, Data: "!!!"}),
		},
		{
			name:  "noise_resp_from_phone",
			frame: mustMarshalFrame(t, protocol.InnerFrameV2{Version: protocol.V2Version, Type: protocol.TypeNoiseResp, Data: base64.StdEncoding.EncodeToString([]byte("x"))}),
		},
		{
			name:  "noise_msg_before_handshake",
			frame: mustMarshalFrame(t, protocol.InnerFrameV2{Version: protocol.V2Version, Type: protocol.TypeNoiseMsg, Data: base64.StdEncoding.EncodeToString([]byte("x"))}),
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
			if envs[0].CloseCode != uint16(StatusProtocolMismatch) {
				t.Errorf("close_code = %d, want %d", envs[0].CloseCode, StatusProtocolMismatch)
			}
			if envs[0].Frame != nil {
				t.Errorf("Frame = %s, want nil", string(envs[0].Frame))
			}
		})
	}
}

func mustMarshalFrame(t *testing.T, f protocol.InnerFrameV2) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	return b
}

// TestNewV2SessionManager_ConfigValidation pins the constructor's input
// validation: missing required fields surface as errors (or panics for
// programmer errors); wrong key length is rejected.
func TestNewV2SessionManager_ConfigValidation(t *testing.T) {
	t.Parallel()

	respPriv, _ := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope)
	out := func(protocol.RoutingEnvelope) error { return nil }

	t.Run("ok", func(t *testing.T) {
		_, err := NewV2SessionManager(V2SessionConfig{
			Frames:     frames,
			Outbound:   out,
			StaticPriv: respPriv,
			Devices:    &devices.Registry{},
			ServerID:   v2TestServerID,
			Logger:     silentLogger(),
		})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("missing_outbound", func(t *testing.T) {
		_, err := NewV2SessionManager(V2SessionConfig{
			Frames:     frames,
			StaticPriv: respPriv,
			Devices:    &devices.Registry{},
			ServerID:   v2TestServerID,
			Logger:     silentLogger(),
		})
		if err == nil {
			t.Fatal("expected error for missing Outbound")
		}
	})

	t.Run("bad_key_length", func(t *testing.T) {
		_, err := NewV2SessionManager(V2SessionConfig{
			Frames:     frames,
			Outbound:   out,
			StaticPriv: []byte("too-short"),
			Devices:    &devices.Registry{},
			ServerID:   v2TestServerID,
			Logger:     silentLogger(),
		})
		if err == nil {
			t.Fatal("expected error for bad key length")
		}
	})

	t.Run("missing_devices", func(t *testing.T) {
		_, err := NewV2SessionManager(V2SessionConfig{
			Frames:     frames,
			Outbound:   out,
			StaticPriv: respPriv,
			ServerID:   v2TestServerID,
			Logger:     silentLogger(),
		})
		if err == nil {
			t.Fatal("expected error for missing Devices")
		}
	})

	t.Run("missing_server_id", func(t *testing.T) {
		_, err := NewV2SessionManager(V2SessionConfig{
			Frames:     frames,
			Outbound:   out,
			StaticPriv: respPriv,
			Devices:    &devices.Registry{},
			Logger:     silentLogger(),
		})
		if err == nil {
			t.Fatal("expected error for missing ServerID")
		}
	})
}
