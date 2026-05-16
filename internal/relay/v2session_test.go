package relay

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
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

// --- open-state dispatch tests (#446) ---

// openSession bundles the artefacts a test needs after driving a v2
// handshake to V2StateOpen: the manager, the initiator's CipherStates
// (initSend encrypts phone→binary, initRecv decrypts binary→phone), the
// frames channel for further input, and the recorder.
type openSession struct {
	mgr      *V2SessionManager
	rec      *v2Recorder
	frames   chan protocol.RoutingEnvelope
	initiator *noise.Initiator
	initSend *noise.CipherState
	initRecv *noise.CipherState
	respPub  []byte
	initPriv []byte
	stop     func()
}

// driveToOpen runs a paired-device handshake through cfg and returns
// the artefacts needed for open-state assertions. Asserts the handshake
// emitted exactly one envelope (the noise_resp) and that the manager
// reached V2StateOpen.
func driveToOpen(t *testing.T, cfg V2SessionConfig, frames chan protocol.RoutingEnvelope, rec *v2Recorder, respPub, initPriv []byte) *openSession {
	t.Helper()
	mgr, stop := startManager(t, cfg)
	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		stop()
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarlyData(t, v2TestToken))
	if err != nil {
		stop()
		t.Fatalf("WriteInit: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg)

	envs := waitForEnvelopes(t, rec, 1)
	if len(envs) != 1 {
		stop()
		t.Fatalf("handshake: got %d envelopes, want 1", len(envs))
	}
	respRaw := decodeRespFrame(t, envs[0])
	_, initSend, initRecv, err := initiator.ReadResp(respRaw)
	if err != nil {
		stop()
		t.Fatalf("ReadResp: %v", err)
	}
	return &openSession{
		mgr:       mgr,
		rec:       rec,
		frames:    frames,
		initiator: initiator,
		initSend:  initSend,
		initRecv:  initRecv,
		respPub:   respPub,
		initPriv:  initPriv,
		stop:      stop,
	}
}

// sealAppFrame AEAD-seals env under cs, wraps as noise_msg, and returns
// the routing envelope ready for the manager's Frames channel.
func sealAppFrame(t *testing.T, cs *noise.CipherState, env protocol.Envelope) protocol.RoutingEnvelope {
	t.Helper()
	envBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ciphertext, err := cs.Encrypt(envBytes)
	if err != nil {
		t.Fatalf("seal app envelope: %v", err)
	}
	return wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseMsg, ciphertext)
}

// decryptAppFrame decodes a captured noise_msg routing envelope and
// returns the inner Envelope after AEAD-decrypting under cs.
func decryptAppFrame(t *testing.T, env protocol.RoutingEnvelope, cs *noise.CipherState) protocol.Envelope {
	t.Helper()
	ciphertext := decodeNoiseMsg(t, env)
	plaintext, err := cs.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("decrypt app frame: %v", err)
	}
	var inner protocol.Envelope
	if err := json.Unmarshal(plaintext, &inner); err != nil {
		t.Fatalf("decode inner envelope: %v", err)
	}
	return inner
}

// TestV2Session_OpenState_EncryptedRoundTrip drives a paired-device
// handshake to open, registers a stub handler keyed by
// TypeListConversations that replies with a known payload, sends an
// AEAD-sealed request and asserts the encrypted reply round-trips
// through the existing handler chain.
func TestV2Session_OpenState_EncryptedRoundTrip(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	const replyText = "test-conversations-payload"
	echoPayload, err := json.Marshal(map[string]string{"text": replyText})
	if err != nil {
		t.Fatalf("marshal echo payload: %v", err)
	}
	handlers := map[string]dispatch.Handler{
		protocol.TypeListConversations: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
			return c.Reply(ctx, env, protocol.TypeConversations, echoPayload)
		},
	}

	frames := make(chan protocol.RoutingEnvelope, 2)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		Handlers:   handlers,
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	const reqID uint64 = 17
	frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})

	envs := waitForEnvelopes(t, rec, 2)
	if len(envs) != 2 {
		t.Fatalf("envs: got %d, want exactly 2 (noise_resp + reply)", len(envs))
	}
	reply := envs[1]
	if reply.CloseCode != 0 {
		t.Errorf("reply CloseCode = %d, want 0 (no close)", reply.CloseCode)
	}
	inner := decryptAppFrame(t, reply, sess.initRecv)
	if inner.Type != protocol.TypeConversations {
		t.Errorf("inner.Type = %q, want %q", inner.Type, protocol.TypeConversations)
	}
	if inner.InReplyTo == nil || *inner.InReplyTo != reqID {
		t.Errorf("inner.InReplyTo = %v, want pointer to %d", inner.InReplyTo, reqID)
	}
	var gotPayload map[string]string
	if err := json.Unmarshal(inner.Payload, &gotPayload); err != nil {
		t.Fatalf("decode reply payload: %v", err)
	}
	if gotPayload["text"] != replyText {
		t.Errorf("reply payload text = %q, want %q", gotPayload["text"], replyText)
	}

	// Session remains in V2StateOpen after the reply.
	sess.stop()
	s := sess.mgr.sessions[v2TestConnID]
	if s == nil {
		t.Fatalf("session for %q missing after reply", v2TestConnID)
	}
	if got := s.State(); got != V2StateOpen {
		t.Errorf("state after reply = %v, want V2StateOpen", got)
	}
}

// TestV2Session_OpenState_TamperedNoiseMsg_4421 drives the happy path to
// open then feeds a noise_msg whose ciphertext has one byte flipped. The
// manager must close at 4421 (Frame=nil) AND drop the session entry so
// AC #3 (local cleanup on AEAD-failure close) is satisfied.
func TestV2Session_OpenState_TamperedNoiseMsg_4421(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	var handlerCalled atomic.Bool
	handlers := map[string]dispatch.Handler{
		protocol.TypeListConversations: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
			handlerCalled.Store(true)
			return nil
		},
	}

	frames := make(chan protocol.RoutingEnvelope, 2)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		Handlers:   handlers,
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	// Produce a valid sealed envelope, then flip a byte in the ciphertext
	// before wrapping. Anything past the Noise tag offset works; pick the
	// first byte for determinism.
	envBytes, err := json.Marshal(protocol.Envelope{
		ID:      99,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ciphertext, err := sess.initSend.Encrypt(envBytes)
	if err != nil {
		t.Fatalf("seal envelope: %v", err)
	}
	ciphertext[0] ^= 0xff
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseMsg, ciphertext)

	envs := waitForEnvelopes(t, rec, 2)
	if len(envs) != 2 {
		t.Fatalf("envs: got %d, want exactly 2 (noise_resp + close)", len(envs))
	}
	closing := envs[1]
	if closing.CloseCode != uint16(StatusProtocolMismatch) {
		t.Errorf("close_code = %d, want %d", closing.CloseCode, StatusProtocolMismatch)
	}
	if closing.Frame != nil {
		t.Errorf("Frame = %s, want nil (close-only at 4421)", string(closing.Frame))
	}
	if handlerCalled.Load() {
		t.Error("handler invoked on tampered noise_msg; handler chain MUST be unreachable on AEAD failure")
	}

	// AC #3: the session entry MUST be gone (closeWith deletes from
	// m.sessions on the AEAD-failure teardown path).
	sess.stop()
	if got, ok := sess.mgr.sessions[v2TestConnID]; ok {
		t.Errorf("sessions[%q] = %v, want absent after AEAD-failure close", v2TestConnID, got)
	}
}

// TestV2Session_OpenState_FreshNoiseInitAfterAEADClose drives to open,
// tampers a frame, observes 4421+cleanup, then sends a SECOND noise_init
// on the same conn_id and verifies the manager completes a fresh
// handshake with new CipherStates (a frame sealed under the prior
// session's send key must fail decryption against the new recv).
func TestV2Session_OpenState_FreshNoiseInitAfterAEADClose(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	frames := make(chan protocol.RoutingEnvelope, 4)
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

	// Tamper to drive the AEAD-failure close + session cleanup.
	envBytes, err := json.Marshal(protocol.Envelope{
		ID: 1, Type: protocol.TypeListConversations, TS: time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ciphertext, err := sess.initSend.Encrypt(envBytes)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	ciphertext[0] ^= 0xff
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseMsg, ciphertext)
	waitForEnvelopes(t, rec, 2)

	// Stash a stale ciphertext under the OLD CipherState; we'll feed it
	// AFTER a fresh handshake to confirm the new s.recv rejects it.
	staleEnv, err := json.Marshal(protocol.Envelope{
		ID: 2, Type: protocol.TypeListConversations, TS: time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("marshal stale: %v", err)
	}
	staleCipher, err := sess.initSend.Encrypt(staleEnv)
	if err != nil {
		t.Fatalf("seal stale: %v", err)
	}

	// Fresh handshake on the same conn_id. The session map entry is gone,
	// so the manager lazy-creates a new awaitingInit and runs the
	// handshake from scratch.
	initiator2, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator2: %v", err)
	}
	initMsg2, err := initiator2.WriteInit(buildHelloEarlyData(t, v2TestToken))
	if err != nil {
		t.Fatalf("WriteInit2: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg2)

	envs := waitForEnvelopes(t, rec, 3)
	if len(envs) != 3 {
		t.Fatalf("envs: got %d, want exactly 3 (resp1, close, resp2)", len(envs))
	}
	resp2 := envs[2]
	if resp2.CloseCode != 0 {
		t.Errorf("resp2.CloseCode = %d, want 0", resp2.CloseCode)
	}
	respRaw := decodeRespFrame(t, resp2)
	_, initSend2, _, err := initiator2.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("initiator2.ReadResp: %v", err)
	}

	// Confirm the new session is in V2StateOpen with NON-NIL CipherStates
	// distinct from the prior ones. The prior session's pointer was
	// dropped from the map; we cannot compare directly. Instead feed the
	// stale ciphertext and assert it triggers the AEAD-failure close
	// branch on the NEW session — proves the new s.recv is a different
	// CipherState with a different key, not a carry-over.
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseMsg, staleCipher)
	envs = waitForEnvelopes(t, rec, 4)
	staleCloseEnv := envs[3]
	if staleCloseEnv.CloseCode != uint16(StatusProtocolMismatch) {
		t.Errorf("stale-frame close_code = %d, want %d (proves new s.recv is fresh)",
			staleCloseEnv.CloseCode, StatusProtocolMismatch)
	}

	// A round-trip through the NEW CipherStates must work — proves the
	// fresh handshake produced a working channel.
	_ = initSend2

	// Cleanup deletes the session again; that's expected behaviour.
	sess.stop()
	if _, ok := sess.mgr.sessions[v2TestConnID]; ok {
		t.Error("sessions[v2TestConnID] still present after second AEAD-failure close")
	}
}

// TestV2Session_OpenState_UnknownEnvelopeType_SealedUnsupportedReply
// drives the happy path to open with NO handlers registered, feeds a
// well-formed encrypted envelope, and asserts a SEALED
// protocol.unsupported error envelope comes back (state stays open).
func TestV2Session_OpenState_UnknownEnvelopeType_SealedUnsupportedReply(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
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
		// Handlers intentionally nil.
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	const reqID uint64 = 23
	frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})

	envs := waitForEnvelopes(t, rec, 2)
	if len(envs) != 2 {
		t.Fatalf("envs: got %d, want exactly 2 (noise_resp + reply)", len(envs))
	}
	reply := envs[1]
	if reply.CloseCode != 0 {
		t.Errorf("reply CloseCode = %d, want 0", reply.CloseCode)
	}
	inner := decryptAppFrame(t, reply, sess.initRecv)
	if inner.Type != protocol.TypeError {
		t.Errorf("inner.Type = %q, want %q", inner.Type, protocol.TypeError)
	}
	if inner.InReplyTo == nil || *inner.InReplyTo != reqID {
		t.Errorf("InReplyTo = %v, want pointer to %d", inner.InReplyTo, reqID)
	}
	var ep protocol.ErrorPayload
	if err := json.Unmarshal(inner.Payload, &ep); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if ep.Code != protocol.CodeProtocolUnsupported {
		t.Errorf("error code = %q, want %q", ep.Code, protocol.CodeProtocolUnsupported)
	}

	// Session remains open.
	sess.stop()
	s := sess.mgr.sessions[v2TestConnID]
	if s == nil || s.State() != V2StateOpen {
		t.Errorf("session state after unsupported reply: %v, want V2StateOpen", s)
	}
}

// TestV2Session_OpenState_MalformedInnerEnvelope_SealedMalformedReply
// drives the happy path to open then AEAD-seals raw garbage (non-JSON).
// The manager must reply with a sealed protocol.malformed error
// envelope; state stays open.
func TestV2Session_OpenState_MalformedInnerEnvelope_SealedMalformedReply(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
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

	garbage, err := sess.initSend.Encrypt([]byte("not json"))
	if err != nil {
		t.Fatalf("seal garbage: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseMsg, garbage)

	envs := waitForEnvelopes(t, rec, 2)
	if len(envs) != 2 {
		t.Fatalf("envs: got %d, want exactly 2", len(envs))
	}
	reply := envs[1]
	if reply.CloseCode != 0 {
		t.Errorf("reply CloseCode = %d, want 0 (no close on malformed)", reply.CloseCode)
	}
	inner := decryptAppFrame(t, reply, sess.initRecv)
	if inner.Type != protocol.TypeError {
		t.Errorf("inner.Type = %q, want %q", inner.Type, protocol.TypeError)
	}
	var ep protocol.ErrorPayload
	if err := json.Unmarshal(inner.Payload, &ep); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if ep.Code != protocol.CodeProtocolMalformed {
		t.Errorf("error code = %q, want %q", ep.Code, protocol.CodeProtocolMalformed)
	}

	sess.stop()
	s := sess.mgr.sessions[v2TestConnID]
	if s == nil || s.State() != V2StateOpen {
		t.Errorf("session state after malformed reply: %v, want V2StateOpen", s)
	}
}

// TestV2Session_OpenState_HandlerAuthDevice verifies the authenticated
// device snapshot is surfaced into *dispatch.Conn via c.Auth() so
// handlers can consult it. Captures auth.Name from inside the handler.
func TestV2Session_OpenState_HandlerAuthDevice(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	var seenName atomic.Value
	handlers := map[string]dispatch.Handler{
		protocol.TypeListConversations: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
			if auth := c.Auth(); auth != nil {
				seenName.Store(auth.Name)
			}
			return c.Reply(ctx, env, protocol.TypeConversations, json.RawMessage(`{}`))
		},
	}

	frames := make(chan protocol.RoutingEnvelope, 2)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		Handlers:   handlers,
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:   7,
		Type: protocol.TypeListConversations,
		TS:   time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})
	waitForEnvelopes(t, rec, 2)

	got, _ := seenName.Load().(string)
	if got != v2TestDevName {
		t.Errorf("c.Auth().Name = %q, want %q", got, v2TestDevName)
	}
}

// --- re-key responder tests (#449) ---

// sealRekeyRequest builds a rekey_request envelope with the given
// payload reason, AEAD-seals it under cs, wraps as noise_msg, and
// returns the routing envelope ready for the manager's Frames channel.
// Mirrors sealAppFrame for the control-envelope path.
func sealRekeyRequest(t *testing.T, cs *noise.CipherState, id uint64, reason string) protocol.RoutingEnvelope {
	t.Helper()
	payload, err := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: reason})
	if err != nil {
		t.Fatalf("marshal rekey_request payload: %v", err)
	}
	return sealAppFrame(t, cs, protocol.Envelope{
		ID:      id,
		Type:    protocol.TypeRekeyRequest,
		TS:      time.Now().UTC(),
		Payload: payload,
	})
}

// captureLogger returns a slog.Logger whose handler writes to a
// shared buffer plus an accessor returning the buffer contents as a
// string. Used by tests that need to substring-assert on log lines
// (e.g. the rekey_request unknown-reason WARN test).
func captureLogger(t *testing.T) (*slog.Logger, func() string) {
	t.Helper()
	var (
		mu  sync.Mutex
		buf strings.Builder
	)
	handler := slog.NewTextHandler(&lockedWriter{mu: &mu, w: &buf}, nil)
	get := func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
	return slog.New(handler), get
}

// lockedWriter is the minimal sync wrapper a slog handler needs when
// reads of the underlying buffer cross goroutine boundaries.
type lockedWriter struct {
	mu *sync.Mutex
	w  *strings.Builder
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// TestV2Session_OpenState_RekeyResponder_HappyPath drives a paired
// handshake to open, runs the phone-initiated re-key responder a second
// time against the SAME initiator static key, and verifies the new
// CipherStates round-trip an application frame end-to-end.
func TestV2Session_OpenState_RekeyResponder_HappyPath(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	const replyText = "rekey-round-trip-payload"
	echoPayload, err := json.Marshal(map[string]string{"text": replyText})
	if err != nil {
		t.Fatalf("marshal echo payload: %v", err)
	}
	handlers := map[string]dispatch.Handler{
		protocol.TypeListConversations: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
			return c.Reply(ctx, env, protocol.TypeConversations, echoPayload)
		},
	}

	frames := make(chan protocol.RoutingEnvelope, 4)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		Handlers:   handlers,
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	// Re-key with the SAME initPriv (peer-static continuity check).
	// Empty early-data per spec § Re-key.
	initiator2, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator2: %v", err)
	}
	rekeyInitMsg, err := initiator2.WriteInit(nil)
	if err != nil {
		t.Fatalf("WriteInit2: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, rekeyInitMsg)

	envs := waitForEnvelopes(t, rec, 2)
	if len(envs) != 2 {
		t.Fatalf("after rekey noise_init: got %d envelopes, want exactly 2", len(envs))
	}
	rekeyResp := envs[1]
	if rekeyResp.CloseCode != 0 {
		t.Errorf("rekey noise_resp CloseCode = %d, want 0", rekeyResp.CloseCode)
	}
	respRaw := decodeRespFrame(t, rekeyResp)
	earlyResp, initSend2, initRecv2, err := initiator2.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("initiator2.ReadResp: %v", err)
	}
	if len(earlyResp) != 0 {
		t.Errorf("rekey early-data = %x, want empty", earlyResp)
	}

	// Round-trip an application frame under the NEW CipherStates to
	// prove the swap landed cleanly on both directions.
	const reqID uint64 = 51
	frames <- sealAppFrame(t, initSend2, protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})
	envs = waitForEnvelopes(t, rec, 3)
	if len(envs) != 3 {
		t.Fatalf("after post-rekey app frame: got %d envelopes, want 3", len(envs))
	}
	reply := envs[2]
	if reply.CloseCode != 0 {
		t.Errorf("post-rekey reply CloseCode = %d, want 0", reply.CloseCode)
	}
	inner := decryptAppFrame(t, reply, initRecv2)
	if inner.Type != protocol.TypeConversations {
		t.Errorf("post-rekey reply Type = %q, want %q", inner.Type, protocol.TypeConversations)
	}
	if inner.InReplyTo == nil || *inner.InReplyTo != reqID {
		t.Errorf("InReplyTo = %v, want pointer to %d", inner.InReplyTo, reqID)
	}

	sess.stop()
	s := sess.mgr.sessions[v2TestConnID]
	if s == nil {
		t.Fatalf("session for %q missing after rekey", v2TestConnID)
	}
	if got := s.State(); got != V2StateOpen {
		t.Errorf("state after rekey = %v, want V2StateOpen", got)
	}
	if s.device == nil {
		t.Error("device snapshot dropped on rekey; want preserved")
	}
}

// TestV2Session_OpenState_RekeyResponder_OldKeyFrameRejected_4421 drives
// a re-key on an open session, stashes a ciphertext sealed under the
// OLD initiator CipherState, then feeds the stale ciphertext after the
// swap. The new s.recv must reject it through the inherited #446
// tampered-frame branch — close at 4421 with session cleanup. AC #2.
func TestV2Session_OpenState_RekeyResponder_OldKeyFrameRejected_4421(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	frames := make(chan protocol.RoutingEnvelope, 4)
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

	// Stash a ciphertext sealed under the OLD initSend.
	staleEnv, err := json.Marshal(protocol.Envelope{
		ID:      99,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("marshal stale envelope: %v", err)
	}
	staleCipher, err := sess.initSend.Encrypt(staleEnv)
	if err != nil {
		t.Fatalf("seal stale: %v", err)
	}

	// Drive the re-key with the SAME initPriv.
	initiator2, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator2: %v", err)
	}
	rekeyInitMsg, err := initiator2.WriteInit(nil)
	if err != nil {
		t.Fatalf("WriteInit2: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, rekeyInitMsg)
	envs := waitForEnvelopes(t, rec, 2)
	if envs[1].CloseCode != 0 {
		t.Fatalf("rekey noise_resp emitted close=%d, want 0", envs[1].CloseCode)
	}
	if _, _, _, err := initiator2.ReadResp(decodeRespFrame(t, envs[1])); err != nil {
		t.Fatalf("initiator2.ReadResp: %v", err)
	}

	// Feed the stale ciphertext AFTER the swap. The new s.recv must
	// fail decryption → inherited 4421 tampered-frame branch.
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseMsg, staleCipher)
	envs = waitForEnvelopes(t, rec, 3)
	closing := envs[2]
	if closing.CloseCode != uint16(StatusProtocolMismatch) {
		t.Errorf("stale-frame close_code = %d, want %d", closing.CloseCode, StatusProtocolMismatch)
	}
	if closing.Frame != nil {
		t.Errorf("Frame = %s, want nil (close-only at 4421)", string(closing.Frame))
	}

	sess.stop()
	if _, ok := sess.mgr.sessions[v2TestConnID]; ok {
		t.Errorf("sessions[%q] still present after stale-frame close, want absent",
			v2TestConnID)
	}
}

// TestV2Session_OpenState_RekeyResponder_DifferentPeerStatic_4426 drives
// to open with keypair A, then re-keys with a DIFFERENT keypair B. The
// peer-static continuity check must reject at 4426 (StatusHandshakeFailure)
// and clean up the session. This is the security-load-bearing test for
// Threat #3 (relay-MITM): without the check, a relay operator could
// re-key over an authenticated conn and assume the device snapshot.
func TestV2Session_OpenState_RekeyResponder_DifferentPeerStatic_4426(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPrivA, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	frames := make(chan protocol.RoutingEnvelope, 4)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	}, frames, rec, respPub, initPrivA)
	t.Cleanup(sess.stop)

	// Re-key with a DIFFERENT initiator keypair.
	initPrivB, _ := genV2Keypair(t)
	initiatorB, err := noise.NewInitiator(initPrivB, respPub)
	if err != nil {
		t.Fatalf("NewInitiator(B): %v", err)
	}
	rekeyInitMsg, err := initiatorB.WriteInit(nil)
	if err != nil {
		t.Fatalf("WriteInit(B): %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, rekeyInitMsg)

	envs := waitForEnvelopes(t, rec, 2)
	closing := envs[1]
	if closing.CloseCode != uint16(StatusHandshakeFailure) {
		t.Errorf("close_code = %d, want %d (peer-static mismatch)",
			closing.CloseCode, StatusHandshakeFailure)
	}
	if closing.Frame != nil {
		t.Errorf("Frame = %s, want nil (close-only at 4426)", string(closing.Frame))
	}

	sess.stop()
	if _, ok := sess.mgr.sessions[v2TestConnID]; ok {
		t.Errorf("sessions[%q] still present after peer-static-mismatch close, want absent",
			v2TestConnID)
	}
}

// TestV2Session_OpenState_RekeyResponder_BadIKMessage_4426 drives to
// open, then feeds a noise_init with random bytes as data. ReadInit must
// fail at MAC verification; the close-code matches the initial
// handshake's IK-failure posture (4426). AC #4.
func TestV2Session_OpenState_RekeyResponder_BadIKMessage_4426(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	frames := make(chan protocol.RoutingEnvelope, 4)
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

	garbage := make([]byte, 96)
	if _, err := rand.Read(garbage); err != nil {
		t.Fatalf("rand: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, garbage)

	envs := waitForEnvelopes(t, rec, 2)
	closing := envs[1]
	if closing.CloseCode != uint16(StatusHandshakeFailure) {
		t.Errorf("close_code = %d, want %d", closing.CloseCode, StatusHandshakeFailure)
	}
	if closing.Frame != nil {
		t.Errorf("Frame = %s, want nil", string(closing.Frame))
	}

	sess.stop()
	if _, ok := sess.mgr.sessions[v2TestConnID]; ok {
		t.Errorf("sessions[%q] still present after rekey IK-failure close, want absent",
			v2TestConnID)
	}
}

// TestV2Session_OpenState_RekeyRequest_NotRoutedToHandler drives to
// open, registers a handler for protocol.TypeRekeyRequest whose body
// stores true in an atomic.Bool, feeds an AEAD-sealed rekey_request,
// and asserts the handler was never called AND no outbound envelope
// was emitted (the control-envelope discriminator short-circuits before
// dispatch.Route runs). AC #3.
func TestV2Session_OpenState_RekeyRequest_NotRoutedToHandler(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	var handlerCalled atomic.Bool
	handlers := map[string]dispatch.Handler{
		protocol.TypeRekeyRequest: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
			handlerCalled.Store(true)
			return nil
		},
	}

	frames := make(chan protocol.RoutingEnvelope, 2)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
		Handlers:   handlers,
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	frames <- sealRekeyRequest(t, sess.initSend, 42, "scheduled")

	// Give the manager a beat to process the frame. waitForEnvelopes would
	// fatal here (since no new envelope is expected) — poll on the
	// manager's session map being still V2StateOpen instead, then assert
	// envelope-count unchanged.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		if handlerCalled.Load() || len(rec.snapshot()) != 1 {
			break
		}
	}

	envs := rec.snapshot()
	if len(envs) != 1 {
		t.Fatalf("after rekey_request: got %d envelopes, want exactly 1 (initial noise_resp)",
			len(envs))
	}
	if handlerCalled.Load() {
		t.Error("rekey_request reached the handler chain; AC #3 requires the discriminator short-circuit")
	}

	sess.stop()
	s := sess.mgr.sessions[v2TestConnID]
	if s == nil || s.State() != V2StateOpen {
		t.Errorf("session state after rekey_request: %v, want V2StateOpen", s)
	}
}

// TestV2Session_OpenState_RekeyRequest_UnknownReason_WarnNoClose drives
// to open and feeds a rekey_request whose payload.reason is unrecognised.
// The manager must log a WARN under v2.rekey_request.unknown_reason and
// take no transport action. AC #3 unknown-reason tolerance.
func TestV2Session_OpenState_RekeyRequest_UnknownReason_WarnNoClose(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	logger, getLog := captureLogger(t)

	frames := make(chan protocol.RoutingEnvelope, 2)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     logger,
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	frames <- sealRekeyRequest(t, sess.initSend, 71, "surprise")

	// Poll for the log line to appear with a bounded deadline.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if strings.Contains(getLog(), "v2.rekey_request.unknown_reason") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	logged := getLog()
	if !strings.Contains(logged, "v2.rekey_request.unknown_reason") {
		t.Errorf("logs missing v2.rekey_request.unknown_reason event\nlogs: %s", logged)
	}
	if !strings.Contains(logged, "reason=surprise") {
		t.Errorf("logs missing reason=surprise\nlogs: %s", logged)
	}
	if envs := rec.snapshot(); len(envs) != 1 {
		t.Errorf("envelope count = %d, want 1 (no transport action on unknown reason)", len(envs))
	}

	sess.stop()
	s := sess.mgr.sessions[v2TestConnID]
	if s == nil || s.State() != V2StateOpen {
		t.Errorf("session state: %v, want V2StateOpen", s)
	}
}

// TestV2Session_OpenState_RekeyRequest_ScheduledReason_InfoNoClose pins
// the recognised-reason branch: payload.reason = "scheduled" logs at
// INFO under v2.rekey_request and takes no transport action.
func TestV2Session_OpenState_RekeyRequest_ScheduledReason_InfoNoClose(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	logger, getLog := captureLogger(t)

	frames := make(chan protocol.RoutingEnvelope, 2)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     logger,
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	frames <- sealRekeyRequest(t, sess.initSend, 71, "scheduled")

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if strings.Contains(getLog(), "v2.rekey_request") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	logged := getLog()
	if !strings.Contains(logged, "v2.rekey_request") {
		t.Errorf("logs missing v2.rekey_request event\nlogs: %s", logged)
	}
	if strings.Contains(logged, "unknown_reason") {
		t.Errorf("scheduled reason should not log unknown_reason\nlogs: %s", logged)
	}
	if !strings.Contains(logged, "reason=scheduled") {
		t.Errorf("logs missing reason=scheduled\nlogs: %s", logged)
	}
	if envs := rec.snapshot(); len(envs) != 1 {
		t.Errorf("envelope count = %d, want 1 (no transport action)", len(envs))
	}
}
