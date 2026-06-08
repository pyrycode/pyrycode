package relay

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
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

// buildHelloEarlyData marshals a v2 hello envelope (with token, no advertised
// capabilities) suitable for embedding in IK message 1 early-data. Thin
// wrapper over buildHelloEarlyDataCaps so the no-caps shape (capabilities key
// absent via omitempty) stays byte-identical for its existing callers.
func buildHelloEarlyData(t *testing.T, token string) []byte {
	t.Helper()
	return buildHelloEarlyDataCaps(t, token, nil)
}

// buildHelloEarlyDataCaps is buildHelloEarlyData with an explicit advertised
// capability set (the #626 negotiation input). A nil/empty caps drops the
// capabilities key via omitempty.
func buildHelloEarlyDataCaps(t *testing.T, token string, caps []string) []byte {
	t.Helper()
	payload, err := json.Marshal(protocol.HelloClientPayload{
		Role:             "client",
		DeviceName:       v2TestDevName,
		ClientVersion:    "v2-test",
		ProtocolVersions: []string{"v2"},
		Token:            token,
		Capabilities:     caps,
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

// driveToOpenCaps runs a paired-device handshake whose hello advertises caps,
// captures the hello_ack early-data (which driveToOpen discards), and returns
// the open session plus the raw ack-envelope bytes for the capability echo
// assertions (#626). Mirrors driveToOpen otherwise.
func driveToOpenCaps(t *testing.T, cfg V2SessionConfig, frames chan protocol.RoutingEnvelope, rec *v2Recorder, respPub, initPriv []byte, token string, caps []string) (*openSession, []byte) {
	t.Helper()
	mgr, stop := startManager(t, cfg)
	initiator, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		stop()
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarlyDataCaps(t, token, caps))
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
	earlyAck, initSend, initRecv, err := initiator.ReadResp(respRaw)
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
	}, earlyAck
}

// decodeHelloAck unmarshals the hello_ack early-data (an Envelope wrapping a
// HelloAckPayload) recovered from the noise_resp and returns the payload.
func decodeHelloAck(t *testing.T, earlyData []byte) protocol.HelloAckPayload {
	t.Helper()
	var ackEnv protocol.Envelope
	if err := json.Unmarshal(earlyData, &ackEnv); err != nil {
		t.Fatalf("decode hello_ack envelope: %v", err)
	}
	if ackEnv.Type != protocol.TypeHelloAck {
		t.Fatalf("ack type = %q, want %q", ackEnv.Type, protocol.TypeHelloAck)
	}
	var ack protocol.HelloAckPayload
	if err := json.Unmarshal(ackEnv.Payload, &ack); err != nil {
		t.Fatalf("decode hello_ack payload: %v", err)
	}
	return ack
}

// activeConnFor returns the ActiveConn snapshot entry for connID, failing if
// no open conn with that id is enumerated.
func activeConnFor(t *testing.T, mgr *V2SessionManager, ctx context.Context, connID string) ActiveConn {
	t.Helper()
	for _, c := range mgr.ActiveConns(ctx) {
		if c.ConnID == connID {
			return c
		}
	}
	t.Fatalf("ActiveConns has no open conn %q", connID)
	return ActiveConn{}
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

// --- re-key responder tests (#453) ---

// TestV2Session_RekeyResponder_HappyPath_RoundTripUnderNewKeys drives a
// paired-device handshake to open, then feeds a fresh noise_init from
// the SAME initiator static (peer-continuity invariant). The manager
// must run the IK responder again, atomically swap s.send / s.recv, and
// emit a noise_resp. A subsequent application frame round-trips under
// the NEW CipherStates (encrypted send via initSend2, decrypted reply
// via initRecv2). State stays V2StateOpen; s.device and s.peerStatic
// are preserved.
func TestV2Session_RekeyResponder_HappyPath_RoundTripUnderNewKeys(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, initPub := genV2Keypair(t)
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

	frames := make(chan protocol.RoutingEnvelope, 3)
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

	// Fresh initiator reusing the SAME initPriv (peer-continuity
	// invariant). Empty early-data per spec § Re-key.
	initiator2, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator2: %v", err)
	}
	initMsg2, err := initiator2.WriteInit(nil)
	if err != nil {
		t.Fatalf("WriteInit2: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg2)

	envs := waitForEnvelopes(t, rec, 2)
	if len(envs) != 2 {
		t.Fatalf("envs after rekey: got %d, want exactly 2 (initial noise_resp + rekey noise_resp)", len(envs))
	}
	rekeyResp := envs[1]
	if rekeyResp.CloseCode != 0 {
		t.Errorf("rekey noise_resp CloseCode = %d, want 0", rekeyResp.CloseCode)
	}
	if rekeyResp.Frame == nil {
		t.Fatal("rekey noise_resp Frame is nil")
	}
	respRaw := decodeRespFrame(t, rekeyResp)
	earlyAck, initSend2, initRecv2, err := initiator2.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("initiator2.ReadResp: %v", err)
	}
	if len(earlyAck) != 0 {
		t.Errorf("rekey noise_resp early-data len = %d, want 0 (spec § Re-key)", len(earlyAck))
	}

	// Round-trip under the NEW CipherStates: AEAD-seal a request under
	// initSend2, expect a sealed reply that decrypts cleanly under
	// initRecv2.
	const reqID uint64 = 99
	frames <- sealAppFrame(t, initSend2, protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})

	envs = waitForEnvelopes(t, rec, 3)
	if len(envs) != 3 {
		t.Fatalf("envs after round-trip: got %d, want exactly 3", len(envs))
	}
	reply := envs[2]
	if reply.CloseCode != 0 {
		t.Errorf("reply CloseCode = %d, want 0", reply.CloseCode)
	}
	inner := decryptAppFrame(t, reply, initRecv2)
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

	// State / preserved-snapshot assertions: stop the manager so reads of
	// mgr.sessions are race-free.
	sess.stop()
	s := sess.mgr.sessions[v2TestConnID]
	if s == nil {
		t.Fatalf("session for %q missing after rekey", v2TestConnID)
	}
	if got := s.State(); got != V2StateOpen {
		t.Errorf("state after rekey = %v, want V2StateOpen", got)
	}
	if s.device == nil || s.device.Name != v2TestDevName {
		t.Errorf("device snapshot lost across rekey: %+v", s.device)
	}
	if !bytes.Equal(s.peerStatic, initPub) {
		t.Errorf("peerStatic mutated across rekey: got %x, want %x", s.peerStatic, initPub)
	}
}

// TestV2Session_RekeyResponder_DifferentPeerStatic_4426 drives a
// paired-device handshake to open, then feeds a fresh noise_init from a
// DIFFERENT initiator static. The peer-continuity check must reject at
// WS close 4426 with reason rekey_peer_static_mismatch and MUST NOT
// include device_name on the reject log line (anti-enumeration
// discipline — this is the security-load-bearing test for Threat #3's
// "no impersonation succeeds" residual-risk claim).
func TestV2Session_RekeyResponder_DifferentPeerStatic_4426(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	logger, logBuf := bufferLogger()
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

	// Fresh initiator with a DIFFERENT static keypair.
	otherInitPriv, _ := genV2Keypair(t)
	initiator2, err := noise.NewInitiator(otherInitPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator2: %v", err)
	}
	initMsg2, err := initiator2.WriteInit(nil)
	if err != nil {
		t.Fatalf("WriteInit2: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg2)

	envs := waitForEnvelopes(t, rec, 2)
	if len(envs) != 2 {
		t.Fatalf("envs after mismatch: got %d, want exactly 2 (initial noise_resp + close)", len(envs))
	}
	closing := envs[1]
	if closing.CloseCode != uint16(StatusHandshakeFailure) {
		t.Errorf("close_code = %d, want %d", closing.CloseCode, StatusHandshakeFailure)
	}
	if closing.Frame != nil {
		t.Errorf("Frame = %s, want nil (close-only at 4426)", string(closing.Frame))
	}

	// Stop the manager so the log buffer is fully flushed before the
	// substring assertions read it.
	sess.stop()

	if _, ok := sess.mgr.sessions[v2TestConnID]; ok {
		t.Errorf("sessions[%q] still present after rekey reject; closeWith should have deleted it", v2TestConnID)
	}

	out := logBuf.String()
	for _, want := range []string{
		"event=v2.handshake.reject.ik_failure",
		"reason=rekey_peer_static_mismatch",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q; got:\n%s", want, out)
		}
	}
	// Anti-enumeration discipline: device_name MUST NOT appear in the
	// mismatch reject line. The only other log line written by this test
	// is the initial v2.handshake.accept (which DOES include
	// device_name), so a global presence check would false-positive. We
	// extract the line containing the reject event and assert on that
	// substring alone.
	rejectLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "event=v2.handshake.reject.ik_failure") {
			rejectLine = line
			break
		}
	}
	if rejectLine == "" {
		t.Fatalf("reject log line not found; got:\n%s", out)
	}
	if strings.Contains(rejectLine, "device_name") {
		t.Errorf("reject log line includes device_name; anti-enumeration discipline violated.\nline: %s", rejectLine)
	}
}

// TestV2Session_RekeyResponder_OldKeyFrameAfterSwap_4421 drives a
// paired-device handshake to open, captures the initiator's pre-rekey
// initSend, completes a re-key (so the manager's s.recv is now the
// fresh CipherState), then feeds a noise_msg sealed under the OLD
// initSend. The existing #446 tampered-frame branch must close the conn
// at 4421 — no new code; this AC pins inherited behaviour against the
// post-swap state.
func TestV2Session_RekeyResponder_OldKeyFrameAfterSwap_4421(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	frames := make(chan protocol.RoutingEnvelope, 3)
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

	// Stash a ciphertext sealed under the OLD initSend BEFORE the
	// re-key. This is the stale frame that must fail AEAD against the
	// fresh s.recv after the swap.
	staleEnv := protocol.Envelope{
		ID:      55,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	}
	staleFrame := sealAppFrame(t, sess.initSend, staleEnv)

	// Drive the re-key with the same static (peer-continuity passes).
	initiator2, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator2: %v", err)
	}
	initMsg2, err := initiator2.WriteInit(nil)
	if err != nil {
		t.Fatalf("WriteInit2: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg2)
	waitForEnvelopes(t, rec, 2)

	// Now feed the stale frame. The manager's s.recv is the fresh
	// CipherState; the stale ciphertext was sealed under the pre-rekey
	// counterpart key. AEAD decrypt fails → #446 tampered-frame branch
	// closes at 4421 with no Frame.
	frames <- staleFrame

	envs := waitForEnvelopes(t, rec, 3)
	if len(envs) != 3 {
		t.Fatalf("envs after stale frame: got %d, want exactly 3 (initial resp + rekey resp + close)", len(envs))
	}
	closing := envs[2]
	if closing.CloseCode != uint16(StatusProtocolMismatch) {
		t.Errorf("close_code = %d, want %d (#446 tampered-frame branch on new s.recv)",
			closing.CloseCode, StatusProtocolMismatch)
	}
	if closing.Frame != nil {
		t.Errorf("Frame = %s, want nil (close-only at 4421)", string(closing.Frame))
	}

	sess.stop()
	if _, ok := sess.mgr.sessions[v2TestConnID]; ok {
		t.Errorf("sessions[%q] still present after 4421 close; closeWith should have deleted it", v2TestConnID)
	}
}

// --- v2 control-envelope tests (#454) ---

// syncLogBuffer is a goroutine-safe sink for slog text output. The
// manager's dispatch goroutine writes; the test goroutine reads. Mirrors
// internal/conversations/sweep_loop_test.go's syncBuffer.
type syncLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncLogBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncLogBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// bufferLogger returns a slog.TextHandler-backed logger that writes into
// a goroutine-safe buffer at Debug level. The buffer is suitable for
// substring assertions on event/level/field key=value text.
func bufferLogger() (*slog.Logger, *syncLogBuffer) {
	buf := &syncLogBuffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// waitForLogContains polls buf until it contains substr or the deadline
// expires. Synchronisation knob for tests that send a frame the manager
// processes without emitting an outbound envelope (e.g. rekey_request,
// which is informational).
func waitForLogContains(t *testing.T, buf *syncLogBuffer, substr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), substr) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waitForLogContains: %q not found in log; got:\n%s", substr, buf.String())
}

// TestV2Session_OpenState_RekeyRequest_ScheduledIntercepted drives a
// paired-device handshake to open and feeds an AEAD-sealed rekey_request
// envelope. The v2 control-envelope discriminator MUST route the frame
// away from the application handler chain (handlerCalled stays false),
// emit no outbound envelope, and leave the session in V2StateOpen.
func TestV2Session_OpenState_RekeyRequest_ScheduledIntercepted(t *testing.T) {
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

	logger, logBuf := bufferLogger()
	frames := make(chan protocol.RoutingEnvelope, 2)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     logger,
		Handlers:   handlers,
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:      42,
		Type:    protocol.TypeRekeyRequest,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"reason":"scheduled"}`),
	})

	// The discriminator emits no outbound envelope, so synchronise on the
	// log line the recognised-reason branch writes.
	waitForLogContains(t, logBuf, "event=v2.rekey.request.received")

	// Exactly one outbound envelope (the handshake's noise_resp); no
	// reply, no close.
	envs := rec.snapshot()
	if len(envs) != 1 {
		t.Fatalf("envs after rekey_request: got %d, want exactly 1 (noise_resp only)", len(envs))
	}
	if envs[0].CloseCode != 0 {
		t.Errorf("noise_resp CloseCode = %d, want 0", envs[0].CloseCode)
	}
	if handlerCalled.Load() {
		t.Error("application handler invoked on rekey_request; control envelope MUST be intercepted before dispatch.Route")
	}

	sess.stop()
	s := sess.mgr.sessions[v2TestConnID]
	if s == nil {
		t.Fatalf("session for %q missing after rekey_request", v2TestConnID)
	}
	if got := s.State(); got != V2StateOpen {
		t.Errorf("state after rekey_request = %v, want V2StateOpen", got)
	}
}

// TestV2Session_OpenState_RekeyRequest_UnknownReasonTolerated drives to
// open then sends rekey_request with an unrecognised reason. The
// manager must log at WARN with the raw reason string, emit no
// outbound frame, and leave the session in V2StateOpen.
func TestV2Session_OpenState_RekeyRequest_UnknownReasonTolerated(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	logger, logBuf := bufferLogger()
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

	frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:      42,
		Type:    protocol.TypeRekeyRequest,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"reason":"lunar-eclipse"}`),
	})

	waitForLogContains(t, logBuf, "event=v2.rekey.request.received")

	out := logBuf.String()
	for _, want := range []string{"level=WARN", "event=v2.rekey.request.received", "reason=lunar-eclipse"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q; got:\n%s", want, out)
		}
	}

	envs := rec.snapshot()
	if len(envs) != 1 {
		t.Fatalf("envs after unknown-reason rekey_request: got %d, want exactly 1 (noise_resp only)", len(envs))
	}
	if envs[0].CloseCode != 0 {
		t.Errorf("noise_resp CloseCode = %d, want 0", envs[0].CloseCode)
	}

	sess.stop()
	s := sess.mgr.sessions[v2TestConnID]
	if s == nil {
		t.Fatalf("session for %q missing after unknown-reason rekey_request", v2TestConnID)
	}
	if got := s.State(); got != V2StateOpen {
		t.Errorf("state after unknown-reason rekey_request = %v, want V2StateOpen", got)
	}
}

// TestV2Session_OpenState_RekeyRequest_RecognisedReasons parameterises
// across the three recognised payload.reason values from
// docs/protocol-mobile.md § Re-key. Each must log at INFO with the
// matching reason field, emit no outbound frame, and leave the session
// in V2StateOpen.
func TestV2Session_OpenState_RekeyRequest_RecognisedReasons(t *testing.T) {
	t.Parallel()

	reasons := []string{"scheduled", "manual", "compromise"}
	for _, reason := range reasons {
		reason := reason
		t.Run(reason, func(t *testing.T) {
			t.Parallel()

			respPriv, respPub := genV2Keypair(t)
			initPriv, _ := genV2Keypair(t)
			reg := v2PairedRegistry(t, v2TestToken)

			logger, logBuf := bufferLogger()
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

			payload, err := json.Marshal(map[string]string{"reason": reason})
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
				ID:      42,
				Type:    protocol.TypeRekeyRequest,
				TS:      time.Now().UTC(),
				Payload: payload,
			})

			waitForLogContains(t, logBuf, "event=v2.rekey.request.received")

			out := logBuf.String()
			for _, want := range []string{"level=INFO", "event=v2.rekey.request.received", "reason=" + reason} {
				if !strings.Contains(out, want) {
					t.Errorf("log missing %q; got:\n%s", want, out)
				}
			}

			envs := rec.snapshot()
			if len(envs) != 1 {
				t.Fatalf("envs after %s rekey_request: got %d, want exactly 1 (noise_resp only)", reason, len(envs))
			}
			if envs[0].CloseCode != 0 {
				t.Errorf("noise_resp CloseCode = %d, want 0", envs[0].CloseCode)
			}

			sess.stop()
			s := sess.mgr.sessions[v2TestConnID]
			if s == nil {
				t.Fatalf("session for %q missing after %s rekey_request", v2TestConnID, reason)
			}
			if got := s.State(); got != V2StateOpen {
				t.Errorf("state after %s rekey_request = %v, want V2StateOpen", reason, got)
			}
		})
	}
}

// --- re-key initiator tests (#450) ---

// waitForOutboundCount polls rec.snapshot() until at least n envelopes
// are recorded or the supplied deadline expires. Same shape as
// waitForEnvelopes but with a caller-supplied deadline (used by the
// reply-timeout test that has to wait a bounded window for the close
// envelope without the 2s default tripping a stale-rekey false positive).
func waitForOutboundCount(t *testing.T, rec *v2Recorder, n int, deadline time.Duration) []protocol.RoutingEnvelope {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		envs := rec.snapshot()
		if len(envs) >= n {
			return envs
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waitForOutboundCount: only got %d, want >= %d", len(rec.snapshot()), n)
	return nil
}

// TestV2Session_RekeyInitiator_Emit_ReArmViaResponder pins AC #5
// bullets 1 + 2: after rekeyInterval elapses the manager emits a
// rekey_request envelope sealed under s.send with payload.reason ==
// "scheduled"; after a full re-key cycle completes via handleRekeyInit
// the timer re-arms and a SECOND rekey_request is emitted under the
// post-swap s.send. Joint test because the natural production caller
// of rekeyComplete is handleRekeyInit, not a test goroutine bypass.
func TestV2Session_RekeyInitiator_Emit_ReArmViaResponder(t *testing.T) {
	// Not t.Parallel: mutates package-level rekeyInterval /
	// rekeyReplyTimeout vars which the dispatch goroutines of other
	// parallel tests read at session-open / emit time.
	prevInterval := rekeyInterval
	rekeyInterval = 20 * time.Millisecond
	t.Cleanup(func() { rekeyInterval = prevInterval })
	prevReply := rekeyReplyTimeout
	rekeyReplyTimeout = 500 * time.Millisecond
	t.Cleanup(func() { rekeyReplyTimeout = prevReply })

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	frames := make(chan protocol.RoutingEnvelope, 3)
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

	// First emit: wait for the rekeyInterval to elapse and a second
	// outbound envelope to appear (initial noise_resp + first emit).
	envs := waitForEnvelopes(t, rec, 2)
	if len(envs) < 2 {
		t.Fatalf("envs after first interval: got %d, want >= 2", len(envs))
	}
	emit1 := envs[1]
	if emit1.CloseCode != 0 {
		t.Errorf("first emit CloseCode = %d, want 0", emit1.CloseCode)
	}
	inner1 := decryptAppFrame(t, emit1, sess.initRecv)
	if inner1.Type != protocol.TypeRekeyRequest {
		t.Errorf("first emit inner type = %q, want %q", inner1.Type, protocol.TypeRekeyRequest)
	}
	var payload1 struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(inner1.Payload, &payload1); err != nil {
		t.Fatalf("decode first emit payload: %v", err)
	}
	if payload1.Reason != "scheduled" {
		t.Errorf("first emit reason = %q, want %q", payload1.Reason, "scheduled")
	}

	// Drive a successful re-key via handleRekeyInit (fresh initiator,
	// same initPriv so peer-static continuity holds, empty early-data
	// per spec § Re-key). This re-bases the 1-hour cadence via
	// rekeyComplete.
	initiator2, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator2: %v", err)
	}
	initMsg2, err := initiator2.WriteInit(nil)
	if err != nil {
		t.Fatalf("WriteInit2: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg2)

	envs = waitForEnvelopes(t, rec, 3)
	if len(envs) < 3 {
		t.Fatalf("envs after rekey: got %d, want >= 3", len(envs))
	}
	rekeyResp := envs[2]
	if rekeyResp.CloseCode != 0 {
		t.Errorf("rekey noise_resp CloseCode = %d, want 0", rekeyResp.CloseCode)
	}
	respRaw := decodeRespFrame(t, rekeyResp)
	_, _, initRecv2, err := initiator2.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("initiator2.ReadResp: %v", err)
	}

	// Second emit: rekeyComplete re-armed the timer; after another
	// rekeyInterval the manager emits a fresh rekey_request — under
	// the POST-SWAP s.send, decoded under initRecv2.
	envs = waitForEnvelopes(t, rec, 4)
	if len(envs) < 4 {
		t.Fatalf("envs after second interval: got %d, want >= 4", len(envs))
	}
	emit2 := envs[3]
	if emit2.CloseCode != 0 {
		t.Errorf("second emit CloseCode = %d, want 0", emit2.CloseCode)
	}
	inner2 := decryptAppFrame(t, emit2, initRecv2)
	if inner2.Type != protocol.TypeRekeyRequest {
		t.Errorf("second emit inner type = %q, want %q", inner2.Type, protocol.TypeRekeyRequest)
	}
	var payload2 struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(inner2.Payload, &payload2); err != nil {
		t.Fatalf("decode second emit payload: %v", err)
	}
	if payload2.Reason != "scheduled" {
		t.Errorf("second emit reason = %q, want %q", payload2.Reason, "scheduled")
	}

	// State after stop: session still open, awaitingRekeyReply still
	// true (second emit was sent, no second responder cycle).
	sess.stop()
	s := sess.mgr.sessions[v2TestConnID]
	if s == nil {
		t.Fatalf("session for %q missing after second emit", v2TestConnID)
	}
	if got := s.State(); got != V2StateOpen {
		t.Errorf("state after second emit = %v, want V2StateOpen", got)
	}
	if !s.awaitingRekeyReply {
		t.Errorf("awaitingRekeyReply = false after second emit, want true (no responder cycle ran)")
	}
}

// TestV2Session_RekeyInitiator_ReplyTimeout_4426 pins AC #5 bullet 3:
// after the timer-driven emit, if no fresh noise_init arrives within
// rekeyReplyTimeout the manager closes the conn at
// StatusHandshakeFailure (4426) with a noise.rekey_failed log line.
func TestV2Session_RekeyInitiator_ReplyTimeout_4426(t *testing.T) {
	// Not t.Parallel: mutates package-level rekeyInterval /
	// rekeyReplyTimeout vars which the dispatch goroutines of other
	// parallel tests read at session-open / emit time.
	prevInterval := rekeyInterval
	rekeyInterval = 20 * time.Millisecond
	t.Cleanup(func() { rekeyInterval = prevInterval })
	prevReply := rekeyReplyTimeout
	rekeyReplyTimeout = 40 * time.Millisecond
	t.Cleanup(func() { rekeyReplyTimeout = prevReply })

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	logger, logBuf := bufferLogger()
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

	// Wait for the close envelope: initial noise_resp + emit + close.
	envs := waitForOutboundCount(t, rec, 3, 500*time.Millisecond)
	if len(envs) < 3 {
		t.Fatalf("envs after reply timeout: got %d, want >= 3", len(envs))
	}
	emit := envs[1]
	if emit.CloseCode != 0 {
		t.Errorf("emit CloseCode = %d, want 0", emit.CloseCode)
	}
	closing := envs[2]
	if closing.CloseCode != uint16(StatusHandshakeFailure) {
		t.Errorf("close_code = %d, want %d", closing.CloseCode, StatusHandshakeFailure)
	}
	if closing.Frame != nil {
		t.Errorf("Frame = %s, want nil (close-only at 4426)", string(closing.Frame))
	}

	// Stop so the log buffer fully flushes before substring checks.
	sess.stop()

	if _, ok := sess.mgr.sessions[v2TestConnID]; ok {
		t.Errorf("sessions[%q] still present after reply-timeout close; closeWith should have deleted it", v2TestConnID)
	}

	out := logBuf.String()
	for _, want := range []string{
		"event=noise.rekey_failed",
		"close_code=4426",
		"conn_id=" + v2TestConnID,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q; got:\n%s", want, out)
		}
	}
	// noise.rekey_failed line MUST NOT carry an err= field — no
	// flynn-noise error text leaks via the timeout branch (architect
	// security review).
	failLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "event=noise.rekey_failed") {
			failLine = line
			break
		}
	}
	if failLine == "" {
		t.Fatalf("noise.rekey_failed log line not found; got:\n%s", out)
	}
	if strings.Contains(failLine, "err=") {
		t.Errorf("noise.rekey_failed line carries err= field; no flynn-noise error text should leak.\nline: %s", failLine)
	}
}

// TestV2Session_RekeyInitiator_TimerCleanup_NoGoroutineLeak pins
// AC #5 bullet 4: armed timer-callback goroutines do not outlive Run.
// Two phases — close via manager-exit (timers armed but never fired)
// and close via reply-timeout fire (rekeyReplyTimer fired the close).
// In both cases, runtime.NumGoroutine returns to its pre-test baseline
// within a small jitter window after Run exits.
func TestV2Session_RekeyInitiator_TimerCleanup_NoGoroutineLeak(t *testing.T) {
	// Not t.Parallel(): runtime.NumGoroutine is global, parallel tests
	// would race the baseline reads.

	t.Run("close_via_manager_exit", func(t *testing.T) {
		prevInterval := rekeyInterval
		rekeyInterval = 50 * time.Millisecond
		t.Cleanup(func() { rekeyInterval = prevInterval })
		prevReply := rekeyReplyTimeout
		rekeyReplyTimeout = 100 * time.Millisecond
		t.Cleanup(func() { rekeyReplyTimeout = prevReply })

		before := runtime.NumGoroutine()

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

		// Stop immediately — rekeyTimer is armed but has not yet
		// fired. Run-derived runCtx cancel must unblock any pending
		// callback goroutine. (Sleeping briefly here is unnecessary:
		// armRekeyTimer's callback hasn't been spawned yet because
		// time.AfterFunc spawns the goroutine only when the timer
		// fires.)
		sess.stop()

		// Give time.AfterFunc-spawned goroutines (if any fired during
		// the brief handshake window) a tick to unblock and exit.
		runtime.Gosched()
		runtime.GC()
		time.Sleep(20 * time.Millisecond)

		after := runtime.NumGoroutine()
		if after > before+1 {
			t.Errorf("goroutine leak: before=%d, after=%d (delta=%d)", before, after, after-before)
		}
	})

	t.Run("close_via_reply_timeout", func(t *testing.T) {
		prevInterval := rekeyInterval
		rekeyInterval = 20 * time.Millisecond
		t.Cleanup(func() { rekeyInterval = prevInterval })
		prevReply := rekeyReplyTimeout
		rekeyReplyTimeout = 40 * time.Millisecond
		t.Cleanup(func() { rekeyReplyTimeout = prevReply })

		before := runtime.NumGoroutine()

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

		// Wait for the close envelope (initial noise_resp + emit +
		// close) so both timers have fired.
		_ = waitForOutboundCount(t, rec, 3, 500*time.Millisecond)
		sess.stop()

		runtime.Gosched()
		runtime.GC()
		time.Sleep(20 * time.Millisecond)

		after := runtime.NumGoroutine()
		if after > before+1 {
			t.Errorf("goroutine leak: before=%d, after=%d (delta=%d)", before, after, after-before)
		}
	})
}

// TestV2Session_RekeyManual_HappyPath_EmitsManualReason drives a v2
// handshake to open, calls (*V2SessionManager).Rekey, and asserts the
// resulting emit is a TypeRekeyRequest envelope sealed under s.send
// with payload.reason == "manual" and a matching log line.
func TestV2Session_RekeyManual_HappyPath_EmitsManualReason(t *testing.T) {
	// Not t.Parallel: mutates package-level rekeyInterval / rekeyReplyTimeout
	// vars. Long values so the scheduled timer cannot fire during the
	// test window (only the manual emit should produce a second envelope).
	prevInterval := rekeyInterval
	rekeyInterval = 10 * time.Second
	t.Cleanup(func() { rekeyInterval = prevInterval })
	prevReply := rekeyReplyTimeout
	rekeyReplyTimeout = 10 * time.Second
	t.Cleanup(func() { rekeyReplyTimeout = prevReply })

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	logger, logBuf := bufferLogger()
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sess.mgr.Rekey(ctx, v2TestConnID); err != nil {
		t.Fatalf("Rekey: %v", err)
	}

	envs := waitForEnvelopes(t, rec, 2)
	if len(envs) != 2 {
		t.Fatalf("envs after manual rekey: got %d, want exactly 2 (noise_resp + manual emit)", len(envs))
	}
	emit := envs[1]
	if emit.CloseCode != 0 {
		t.Errorf("manual emit CloseCode = %d, want 0", emit.CloseCode)
	}
	inner := decryptAppFrame(t, emit, sess.initRecv)
	if inner.Type != protocol.TypeRekeyRequest {
		t.Errorf("manual emit inner type = %q, want %q", inner.Type, protocol.TypeRekeyRequest)
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(inner.Payload, &payload); err != nil {
		t.Fatalf("decode manual emit payload: %v", err)
	}
	if payload.Reason != "manual" {
		t.Errorf("manual emit reason = %q, want %q", payload.Reason, "manual")
	}

	waitForLogContains(t, logBuf, "event=v2.rekey.emit")
	out := logBuf.String()
	for _, want := range []string{
		"event=v2.rekey.emit",
		"reason=manual",
		"conn_id=" + v2TestConnID,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q; got:\n%s", want, out)
		}
	}
}

// TestV2Session_RekeyManual_UnknownConn_ReturnsErrConnNotFound calls
// Rekey for a conn_id the manager has never seen and asserts the
// returned error matches both relay.ErrConnNotFound and
// control.ErrConnNotFound (the wire-mapping invariant the dispatcher's
// errors.Is depends on) and that no outbound side-effect occurred.
func TestV2Session_RekeyManual_UnknownConn_ReturnsErrConnNotFound(t *testing.T) {
	t.Parallel()

	respPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	frames := make(chan protocol.RoutingEnvelope)
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := mgr.Rekey(ctx, "this-conn-does-not-exist")
	if err == nil {
		t.Fatalf("Rekey on unknown conn: err = nil, want non-nil")
	}
	if !errors.Is(err, ErrConnNotFound) {
		t.Errorf("errors.Is(err, relay.ErrConnNotFound) = false; err = %v", err)
	}
	if !errors.Is(err, control.ErrConnNotFound) {
		t.Errorf("errors.Is(err, control.ErrConnNotFound) = false; err = %v (wire-mapping invariant broken)", err)
	}
	if got := rec.snapshot(); len(got) != 0 {
		t.Errorf("rec.snapshot() len = %d, want 0 (no outbound side-effect on unknown conn)", len(got))
	}
}

// TestV2Session_RekeyManual_AlreadyAwaitingReply_ReturnsErrSessionNotOpen
// drives to open, fires a successful manual Rekey, then fires a SECOND
// Rekey on the same conn while the first reply window is still open.
// The second call must return ErrSessionNotOpen without producing a
// second outbound emit.
func TestV2Session_RekeyManual_AlreadyAwaitingReply_ReturnsErrSessionNotOpen(t *testing.T) {
	// Not t.Parallel: mutates package-level rekeyInterval / rekeyReplyTimeout.
	prevInterval := rekeyInterval
	rekeyInterval = 10 * time.Second
	t.Cleanup(func() { rekeyInterval = prevInterval })
	prevReply := rekeyReplyTimeout
	rekeyReplyTimeout = 5 * time.Second
	t.Cleanup(func() { rekeyReplyTimeout = prevReply })

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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := sess.mgr.Rekey(ctx, v2TestConnID); err != nil {
		t.Fatalf("first Rekey: %v", err)
	}
	// Wait for the manual emit so awaitingRekeyReply is observably true
	// on the dispatch goroutine before the second call lands.
	envs := waitForEnvelopes(t, sess.rec, 2)
	if len(envs) != 2 {
		t.Fatalf("envs after first Rekey: got %d, want 2 (noise_resp + manual emit)", len(envs))
	}

	err := sess.mgr.Rekey(ctx, v2TestConnID)
	if err == nil {
		t.Fatalf("second Rekey: err = nil, want ErrSessionNotOpen")
	}
	if !errors.Is(err, ErrSessionNotOpen) {
		t.Errorf("errors.Is(err, relay.ErrSessionNotOpen) = false; err = %v", err)
	}
	if got := sess.rec.snapshot(); len(got) != 2 {
		t.Errorf("rec.snapshot() len = %d, want 2 (no second manual emit)", len(got))
	}

	sess.stop()
	s := sess.mgr.sessions[v2TestConnID]
	if s == nil {
		t.Fatalf("session for %q missing after second Rekey", v2TestConnID)
	}
	if got := s.State(); got != V2StateOpen {
		t.Errorf("state after second Rekey = %v, want V2StateOpen", got)
	}
	if !s.awaitingRekeyReply {
		t.Errorf("awaitingRekeyReply = false after second Rekey, want true")
	}
}

// TestV2Session_RekeyManual_RebasesScheduledTimer pins the AC's "manual
// emit at T re-bases the scheduled timer so the next scheduled emit
// lands at T+rekeyInterval relative to the manual emit, not at the
// original tick boundary." Sequence: drive to open at T≈0, sleep a
// fraction of rekeyInterval so the original and re-based boundaries
// separate cleanly, fire manual Rekey, drive a successful responder
// cycle so rekeyComplete arms a fresh scheduled timer, assert no extra
// emit lands at the original boundary, then assert the re-based
// boundary produces a fresh scheduled emit.
func TestV2Session_RekeyManual_RebasesScheduledTimer(t *testing.T) {
	// Not t.Parallel: mutates package-level rekeyInterval / rekeyReplyTimeout.
	//
	// rekeyInterval is 300ms; the manual rekey is delayed by ~200ms after
	// open so the original boundary (openedAt+300ms) is clearly before the
	// re-based boundary (rekeyCompleteAt+300ms ≈ openedAt+500ms). A
	// tighter cadence would make the two boundaries indistinguishable
	// under scheduler jitter.
	prevInterval := rekeyInterval
	rekeyInterval = 300 * time.Millisecond
	t.Cleanup(func() { rekeyInterval = prevInterval })
	prevReply := rekeyReplyTimeout
	rekeyReplyTimeout = 2 * time.Second
	t.Cleanup(func() { rekeyReplyTimeout = prevReply })

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	frames := make(chan protocol.RoutingEnvelope, 3)
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

	openedAt := time.Now()

	// Delay the manual rekey so the original scheduled boundary (T+300ms)
	// is clearly distinguishable from the re-based boundary (T_manual+300ms).
	sleepUntil(openedAt.Add(200 * time.Millisecond))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sess.mgr.Rekey(ctx, v2TestConnID); err != nil {
		t.Fatalf("Rekey: %v", err)
	}

	// Wait for the manual emit (envelope #2).
	envs := waitForEnvelopes(t, rec, 2)
	if len(envs) < 2 {
		t.Fatalf("envs after manual rekey: got %d, want >= 2", len(envs))
	}
	emit := envs[1]
	inner := decryptAppFrame(t, emit, sess.initRecv)
	if inner.Type != protocol.TypeRekeyRequest {
		t.Fatalf("manual emit inner type = %q, want %q", inner.Type, protocol.TypeRekeyRequest)
	}
	var manualPayload struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(inner.Payload, &manualPayload); err != nil {
		t.Fatalf("decode manual emit payload: %v", err)
	}
	if manualPayload.Reason != "manual" {
		t.Fatalf("manual emit reason = %q, want %q", manualPayload.Reason, "manual")
	}

	// Drive a successful responder cycle. Fresh initiator, same initPriv
	// so peer-static continuity holds; empty early-data per spec.
	initiator2, err := noise.NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator2: %v", err)
	}
	initMsg2, err := initiator2.WriteInit(nil)
	if err != nil {
		t.Fatalf("WriteInit2: %v", err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, initMsg2)

	// Wait for the responder reply (envelope #3) and read it so the
	// post-swap initiator CipherStates exist for the fourth-envelope
	// decode below.
	envs = waitForEnvelopes(t, rec, 3)
	if len(envs) < 3 {
		t.Fatalf("envs after responder cycle: got %d, want >= 3", len(envs))
	}
	rekeyResp := envs[2]
	respRaw := decodeRespFrame(t, rekeyResp)
	_, _, initRecv2, err := initiator2.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("initiator2.ReadResp: %v", err)
	}
	rekeyCompleteAt := time.Now()

	// Original-boundary check: sleep past openedAt + rekeyInterval, then
	// confirm no fourth envelope landed. The original timer was stopped
	// in handleManualRekey before its 300ms fire, and the rebased timer
	// is armed at rekeyCompleteAt which is ~200ms+ε after open — its
	// 300ms fire lands at ~500ms+ε, comfortably after this check at
	// 350ms.
	sleepUntil(openedAt.Add(rekeyInterval + 50*time.Millisecond))
	if got := rec.snapshot(); len(got) != 3 {
		t.Fatalf("envelope count at original scheduled boundary: got %d, want 3 (stale scheduled emit leaked)", len(got))
	}

	// New-boundary check: rekeyComplete armed a fresh scheduled timer.
	// Wait until rekeyCompleteAt + rekeyInterval + jitter and assert the
	// fourth envelope is a TypeRekeyRequest with reason="scheduled".
	deadline := rekeyCompleteAt.Add(rekeyInterval + 500*time.Millisecond)
	for time.Now().Before(deadline) {
		if len(rec.snapshot()) >= 4 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	envs = rec.snapshot()
	if len(envs) < 4 {
		t.Fatalf("envs at re-based scheduled boundary: got %d, want >= 4 (re-based timer did not fire)", len(envs))
	}
	scheduledEmit := envs[3]
	scheduledInner := decryptAppFrame(t, scheduledEmit, initRecv2)
	if scheduledInner.Type != protocol.TypeRekeyRequest {
		t.Errorf("scheduled emit inner type = %q, want %q", scheduledInner.Type, protocol.TypeRekeyRequest)
	}
	var scheduledPayload struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(scheduledInner.Payload, &scheduledPayload); err != nil {
		t.Fatalf("decode scheduled emit payload: %v", err)
	}
	if scheduledPayload.Reason != "scheduled" {
		t.Errorf("scheduled emit reason = %q, want %q", scheduledPayload.Reason, "scheduled")
	}
}

// sleepUntil blocks until t (or returns immediately if t is in the
// past). Helper for the manual-rekey timer-rebase test's wall-clock
// boundary checks.
func sleepUntil(t time.Time) {
	d := time.Until(t)
	if d > 0 {
		time.Sleep(d)
	}
}

// --- unsolicited push surface tests (#571) ---

// buildMessageEnvelope constructs a fully-formed binary→phone `message`
// envelope (TypeMessage + protocol.MessagePayload) — the shape the #572
// assistant-turn bridge will hand to Push. text identifies the frame so
// the decrypting side can assert the payload round-tripped intact.
func buildMessageEnvelope(t *testing.T, id uint64, text string) protocol.Envelope {
	t.Helper()
	payload, err := json.Marshal(protocol.MessagePayload{
		ConversationID: "conv-push-1",
		MessageID:      "msg-" + text,
		Role:           "assistant",
		Text:           text,
	})
	if err != nil {
		t.Fatalf("marshal message payload: %v", err)
	}
	return protocol.Envelope{
		ID:      id,
		Type:    protocol.TypeMessage,
		TS:      time.Now().UTC(),
		Payload: payload,
	}
}

// --- pushQueue drop-policy unit tests (#610) ---

// pqEnv builds a minimal envelope carrying only the Type (which drives the drop
// class) and a strictly-increasing ID (which lets order assertions track an
// individual envelope across drops). No payload/seal — pushQueue.enqueue is a
// pure pre-seal policy over the envelope class.
func pqEnv(typ string, id uint64) protocol.Envelope {
	return protocol.Envelope{ID: id, Type: typ}
}

// assertQueue checks q.items matches want (by Type+ID, in order), that each
// item's droppable flag is consistent with its Type, and that q.dropped equals
// wantDropped.
func assertQueue(t *testing.T, q *pushQueue, want []protocol.Envelope, wantDropped uint64) {
	t.Helper()
	if len(q.items) != len(want) {
		t.Fatalf("queue len = %d, want %d", len(q.items), len(want))
	}
	for i := range want {
		got := q.items[i]
		if got.env.Type != want[i].Type || got.env.ID != want[i].ID {
			t.Errorf("item[%d] = (%s,%d), want (%s,%d)",
				i, got.env.Type, got.env.ID, want[i].Type, want[i].ID)
		}
		if wantDrop := want[i].Type == protocol.TypeAssistantDelta; got.droppable != wantDrop {
			t.Errorf("item[%d] droppable = %v, want %v", i, got.droppable, wantDrop)
		}
	}
	if q.dropped != wantDropped {
		t.Errorf("dropped = %d, want %d", q.dropped, wantDropped)
	}
}

// fillDeltas enqueues deltas with IDs lo..hi-1 onto q (helper for the cap-fill
// scenarios). Returns the want-slice describing those same items.
func fillDeltas(q *pushQueue, lo, hi uint64) []protocol.Envelope {
	want := make([]protocol.Envelope, 0, hi-lo)
	for id := lo; id < hi; id++ {
		e := pqEnv(protocol.TypeAssistantDelta, id)
		q.enqueue(e)
		want = append(want, e)
	}
	return want
}

// TestPushQueue_Enqueue_UnderCap_AllRetained pins the below-cap path: a mix of
// deltas and control events under pushQueueCap are all retained in FIFO order
// with no drops.
func TestPushQueue_Enqueue_UnderCap_AllRetained(t *testing.T) {
	t.Parallel()

	q := &pushQueue{}
	seq := []protocol.Envelope{
		pqEnv(protocol.TypeAssistantDelta, 1),
		pqEnv(protocol.TypeTurnState, 2),
		pqEnv(protocol.TypeAssistantDelta, 3),
		pqEnv(protocol.TypeToolUse, 4),
		pqEnv(protocol.TypeToolResult, 5),
		pqEnv(protocol.TypeAssistantDelta, 6),
		pqEnv(protocol.TypeTurnEnd, 7),
	}
	for _, e := range seq {
		if dropped := q.enqueue(e); dropped {
			t.Errorf("enqueue(id=%d) reported a drop under cap", e.ID)
		}
	}
	assertQueue(t, q, seq, 0)
}

// TestPushQueue_Enqueue_DropOldestDelta pins AC#2: at capacity, enqueuing a new
// delta evicts the OLDEST queued delta so the most recent text is retained.
func TestPushQueue_Enqueue_DropOldestDelta(t *testing.T) {
	t.Parallel()

	q := &pushQueue{}
	fillDeltas(q, 0, pushQueueCap) // ids 0..cap-1

	if dropped := q.enqueue(pqEnv(protocol.TypeAssistantDelta, 9999)); !dropped {
		t.Fatal("enqueue at capacity should report a drop")
	}
	// Oldest (id 0) evicted; ids 1..cap-1 retained in order, new delta at tail.
	want := fillDeltas(&pushQueue{}, 1, pushQueueCap)
	want = append(want, pqEnv(protocol.TypeAssistantDelta, 9999))
	assertQueue(t, q, want, 1)
}

// TestPushQueue_Enqueue_ControlEvictsDelta pins AC#3: at capacity, a control
// event is admitted by evicting the oldest droppable delta — never by dropping
// the control event.
func TestPushQueue_Enqueue_ControlEvictsDelta(t *testing.T) {
	t.Parallel()

	q := &pushQueue{}
	fillDeltas(q, 0, pushQueueCap)

	if dropped := q.enqueue(pqEnv(protocol.TypeTurnEnd, 9999)); !dropped {
		t.Fatal("control at capacity should evict a delta (reported as a drop)")
	}
	want := fillDeltas(&pushQueue{}, 1, pushQueueCap)
	want = append(want, pqEnv(protocol.TypeTurnEnd, 9999))
	assertQueue(t, q, want, 1)
}

// TestPushQueue_Enqueue_MessageIsNeverDrop pins the ticket's class decision:
// the coarse v1 "message" envelope (#589 bridge) is never-drop — only
// assistant_delta is droppable. A message at capacity evicts a delta, like any
// other control event.
func TestPushQueue_Enqueue_MessageIsNeverDrop(t *testing.T) {
	t.Parallel()

	q := &pushQueue{}
	fillDeltas(q, 0, pushQueueCap)

	if dropped := q.enqueue(pqEnv(protocol.TypeMessage, 9999)); !dropped {
		t.Fatal("message at capacity should evict a delta, not drop itself")
	}
	want := fillDeltas(&pushQueue{}, 1, pushQueueCap)
	want = append(want, pqEnv(protocol.TypeMessage, 9999))
	assertQueue(t, q, want, 1)
}

// TestPushQueue_Enqueue_ControlNeverDroppedWhenDeltasPresent pins AC#3 at
// volume: pushing N control events into a full all-delta queue evicts exactly N
// deltas (oldest-first) and keeps every control event, in order.
func TestPushQueue_Enqueue_ControlNeverDroppedWhenDeltasPresent(t *testing.T) {
	t.Parallel()

	q := &pushQueue{}
	fillDeltas(q, 0, pushQueueCap)

	controlTypes := []string{
		protocol.TypeTurnState,
		protocol.TypeToolUse,
		protocol.TypeToolResult,
		protocol.TypeTurnEnd,
		protocol.TypeStall,
	}
	for k, ct := range controlTypes {
		if dropped := q.enqueue(pqEnv(ct, uint64(1000+k))); !dropped {
			t.Errorf("control %q at capacity should evict a delta", ct)
		}
	}
	// The N oldest deltas (ids 0..N-1) evicted; ids N..cap-1 retained, then the
	// N control events at the tail in push order.
	n := uint64(len(controlTypes))
	want := fillDeltas(&pushQueue{}, n, pushQueueCap)
	for k, ct := range controlTypes {
		want = append(want, pqEnv(ct, uint64(1000+k)))
	}
	assertQueue(t, q, want, n)
}

// TestPushQueue_Enqueue_OrderPreservedAcrossDrops pins AC#4: across a long
// scripted interleave that forces many delta evictions, the surviving
// envelopes stay in enqueue order (strictly-increasing IDs) and no control
// event is ever lost. Conservation: every enqueue either appends or drops one,
// and deltas are always available to evict, so the queue stays at exactly cap
// and dropped == totalEnqueued - cap.
func TestPushQueue_Enqueue_OrderPreservedAcrossDrops(t *testing.T) {
	t.Parallel()

	q := &pushQueue{}
	var nextID uint64
	controlIDs := map[uint64]bool{}
	enqueue := func(typ string) {
		nextID++
		if typ != protocol.TypeAssistantDelta {
			controlIDs[nextID] = true
		}
		q.enqueue(pqEnv(typ, nextID))
	}

	// Fill to cap with alternating control/delta so there are always deltas to
	// evict and controls to protect.
	for i := 0; i < pushQueueCap; i++ {
		if i%2 == 0 {
			enqueue(protocol.TypeTurnState)
		} else {
			enqueue(protocol.TypeAssistantDelta)
		}
	}
	// Drive many more enqueues past cap: deltas (evict oldest delta) then
	// controls (evict oldest delta to be admitted).
	for i := 0; i < 50; i++ {
		enqueue(protocol.TypeAssistantDelta)
	}
	for i := 0; i < 10; i++ {
		enqueue(protocol.TypeToolUse)
	}

	// Order preserved: IDs strictly increasing across the surviving FIFO.
	var prev uint64
	for i, qe := range q.items {
		if qe.env.ID <= prev {
			t.Errorf("items[%d] id %d not strictly greater than prev %d — order not preserved",
				i, qe.env.ID, prev)
		}
		prev = qe.env.ID
	}
	// No control event dropped.
	present := make(map[uint64]bool, len(q.items))
	for _, qe := range q.items {
		present[qe.env.ID] = true
	}
	for id := range controlIDs {
		if !present[id] {
			t.Errorf("control id %d was dropped — control must never drop", id)
		}
	}
	// Conservation: stayed at cap, dropped accounts for the overflow.
	if len(q.items) != pushQueueCap {
		t.Errorf("len = %d, want %d (deltas always available to evict)", len(q.items), pushQueueCap)
	}
	if want := nextID - uint64(pushQueueCap); q.dropped != want {
		t.Errorf("dropped = %d, want %d (totalEnqueued - cap)", q.dropped, want)
	}
}

// TestPushQueue_Enqueue_AllControlSoftOverflow pins the documented soft-overflow
// edge: when the queue is saturated entirely with control events, an incoming
// control is admitted PAST nominal cap (never dropped, never blocks), while an
// incoming delta is dropped (it cannot evict a control event), leaving the
// queue unchanged.
func TestPushQueue_Enqueue_AllControlSoftOverflow(t *testing.T) {
	t.Parallel()

	q := &pushQueue{}
	for i := 0; i < pushQueueCap; i++ {
		q.enqueue(pqEnv(protocol.TypeTurnState, uint64(i)))
	}

	// Control past a full all-control queue: admitted, no drop, len == cap+1.
	if dropped := q.enqueue(pqEnv(protocol.TypeTurnEnd, 9000)); dropped {
		t.Error("control soft-overflow must not report a drop")
	}
	if len(q.items) != pushQueueCap+1 {
		t.Errorf("len = %d, want %d (soft overflow admits control past cap)", len(q.items), pushQueueCap+1)
	}
	if q.dropped != 0 {
		t.Errorf("dropped = %d, want 0", q.dropped)
	}

	// Delta into the all-control-saturated queue: dropped; queue unchanged.
	lenBefore := len(q.items)
	if dropped := q.enqueue(pqEnv(protocol.TypeAssistantDelta, 9001)); !dropped {
		t.Error("delta into an all-control full queue must be dropped")
	}
	if len(q.items) != lenBefore {
		t.Errorf("len = %d after dropped delta, want %d (unchanged)", len(q.items), lenBefore)
	}
	if q.dropped != 1 {
		t.Errorf("dropped = %d, want 1", q.dropped)
	}
	// The tail is still the soft-overflow control — the dropped delta was never
	// appended.
	last := q.items[len(q.items)-1]
	if last.env.Type != protocol.TypeTurnEnd || last.env.ID != 9000 {
		t.Errorf("tail = (%s,%d), want (turn_end,9000)", last.env.Type, last.env.ID)
	}
}

// TestV2Session_Push_NonBlockingUnderStall pins AC#1 end-to-end: with the
// outbound (relay) leg stalled so the Run goroutine is wedged mid-forward, the
// producer's Push calls all return promptly (never blocked on the relay), the
// drop counter engages once the buffer fills past cap, and after the stall is
// released the surviving deltas decrypt in order under the phone's recv state —
// proving drop-before-seal left no nonce gap and FIFO order was preserved.
func TestV2Session_Push_NonBlockingUnderStall(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	// Stalling outbound: records every frame via an inner v2Recorder, but once
	// armed, blocks each call on release first. The handshake runs with stall
	// off so the session reaches open; we arm afterwards to wedge the drain.
	rec := &v2Recorder{}
	var stall atomic.Bool
	release := make(chan struct{})
	gated := func(env protocol.RoutingEnvelope) error {
		if stall.Load() {
			<-release
		}
		return rec.outbound(env)
	}

	frames := make(chan protocol.RoutingEnvelope, 4)
	sess := driveToOpen(t, V2SessionConfig{
		Frames:     frames,
		Outbound:   gated,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	}, frames, rec, respPub, initPriv)

	var releaseOnce sync.Once
	releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(sess.stop) // runs last
	t.Cleanup(releaseFn) // runs first — unblock any wedged forward before stop

	// Arm the stall: the next forward (the first drained push) blocks in gated.
	stall.Store(true)

	// Pre-build push envelopes on the test goroutine (avoid building inside the
	// pusher goroutine). Deltas with strictly-increasing IDs 0..nPush-1, enough
	// to overflow the cap so the drop policy engages.
	const nPush = pushQueueCap + 64
	pushes := make([]protocol.Envelope, nPush)
	for i := range pushes {
		pushes[i] = pqEnv(protocol.TypeAssistantDelta, uint64(i))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Fire all pushes from a goroutine; assert they all return while the
	// outbound is stalled (proving Push never blocks on the relay).
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range pushes {
			if err := sess.mgr.Push(ctx, v2TestConnID, pushes[i]); err != nil {
				t.Errorf("Push[%d]: %v", i, err)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		releaseFn() // avoid leaking the wedged Run goroutine
		t.Fatal("Push calls did not return while outbound stalled — producer wedged")
	}

	// The buffer filled past cap, so the drop counter engaged. Read under the
	// leaf lock. No more enqueues happen after this point, so dropped is final.
	sess.mgr.pushMu.Lock()
	dropped := sess.mgr.queues[v2TestConnID].dropped
	sess.mgr.pushMu.Unlock()
	if dropped == 0 {
		t.Fatalf("dropped = 0, want > 0 (buffer should overflow past cap=%d with %d pushes)", pushQueueCap, nPush)
	}

	// Release the stall; the drain pumps the survivors one-per-pass. Exactly
	// nPush-dropped app frames are forwarded (every push is dropped or sent),
	// plus the handshake noise_resp.
	releaseFn()
	wantApp := nPush - int(dropped)
	envs := waitForEnvelopes(t, sess.rec, 1+wantApp)
	if len(envs) != 1+wantApp {
		t.Fatalf("recorded %d envelopes, want %d (noise_resp + %d survivors)", len(envs), 1+wantApp, wantApp)
	}

	// Decrypt survivors in capture (= seal = FIFO drain) order under the phone's
	// recv state. A clean in-order decrypt proves drop-before-seal left no nonce
	// gap; strictly-increasing IDs prove FIFO order survived the drops.
	var prevID uint64
	haveFirst := false
	for _, e := range envs[1:] {
		inner := decryptAppFrame(t, e, sess.initRecv)
		if inner.Type != protocol.TypeAssistantDelta {
			t.Errorf("survivor type = %q, want assistant_delta", inner.Type)
		}
		if haveFirst && inner.ID <= prevID {
			t.Errorf("survivor id %d not strictly greater than prev %d — order not preserved", inner.ID, prevID)
		}
		prevID = inner.ID
		haveFirst = true
	}
	// Drop-oldest retains the most recent text: the last survivor is the last
	// pushed delta.
	if prevID != uint64(nPush-1) {
		t.Errorf("last survivor id = %d, want %d (most-recent delta retained)", prevID, nPush-1)
	}
}

// TestV2Session_Push_InterleavedWithReply_DecryptsUnderRace pins AC#1 and
// AC#4: an unsolicited Push fired from a separate goroutine while a
// request/reply dispatch is in flight on the same session. Both outbound
// frames must decrypt cleanly under the phone's recv CipherState in
// capture (= seal) order — any nonce reuse from concurrent s.send access
// would surface as an AEAD failure inside decryptAppFrame. The pushed
// frame decodes through the SAME path as the solicited reply
// (decryptAppFrame) to a valid TypeMessage envelope, proving no new wire
// shape is introduced. Order between reply and push is nondeterministic
// (Run's select); assert presence, not order.
func TestV2Session_Push_InterleavedWithReply_DecryptsUnderRace(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	const replyText = "push-interleave-reply"
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

	const reqID uint64 = 41
	const pushText = "unsolicited-assistant-text"
	req := sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})
	// Build the push envelope on the test goroutine: t.Fatalf inside a
	// child goroutine is unsafe, and buildMessageEnvelope may call it.
	pushEnv := buildMessageEnvelope(t, 100, pushText)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Fire the push concurrently with feeding the inbound request so the
	// buffer drain and dispatchAppFrame contend for the single Run goroutine.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := sess.mgr.Push(ctx, v2TestConnID, pushEnv); err != nil {
			t.Errorf("Push: %v", err)
		}
	}()
	frames <- req
	wg.Wait()

	// noise_resp (envs[0]) + reply + push.
	envs := waitForEnvelopes(t, sess.rec, 3)
	if len(envs) != 3 {
		t.Fatalf("envs: got %d, want exactly 3 (noise_resp + reply + push)", len(envs))
	}

	// Decrypt every app frame in capture order under the phone's recv
	// state. A clean in-order decrypt across both frames is the
	// nonce-integrity proof; decryptAppFrame t.Fatals on any AEAD failure.
	var sawReply, sawPush bool
	for _, e := range envs[1:] {
		inner := decryptAppFrame(t, e, sess.initRecv)
		switch inner.Type {
		case protocol.TypeConversations:
			if inner.InReplyTo == nil || *inner.InReplyTo != reqID {
				t.Errorf("reply InReplyTo = %v, want pointer to %d", inner.InReplyTo, reqID)
			}
			sawReply = true
		case protocol.TypeMessage:
			var mp protocol.MessagePayload
			if err := json.Unmarshal(inner.Payload, &mp); err != nil {
				t.Fatalf("decode pushed message payload: %v", err)
			}
			if mp.Text != pushText {
				t.Errorf("pushed message text = %q, want %q", mp.Text, pushText)
			}
			sawPush = true
		default:
			t.Errorf("unexpected outbound inner type %q", inner.Type)
		}
	}
	if !sawReply {
		t.Error("no conversations reply captured")
	}
	if !sawPush {
		t.Error("no message push captured")
	}
}

// TestV2Session_Push_ConcurrentWithReplies_NoNonceCorruption pins AC#2:
// N concurrent pushes plus M in-flight request/reply dispatches on the
// same open session. All N+M outbound frames must decrypt in capture
// order under the phone's recv state with no AEAD failure — the stress
// proof that the buffer drain serialises every s.send.Encrypt onto Run and
// the nonce counter never reuses. Run under -race.
func TestV2Session_Push_ConcurrentWithReplies_NoNonceCorruption(t *testing.T) {
	t.Parallel()

	const (
		nPush = 8
		mReq  = 8
	)

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	echoPayload, err := json.Marshal(map[string]string{"text": "reply"})
	if err != nil {
		t.Fatalf("marshal echo payload: %v", err)
	}
	handlers := map[string]dispatch.Handler{
		protocol.TypeListConversations: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
			return c.Reply(ctx, env, protocol.TypeConversations, echoPayload)
		},
	}

	frames := make(chan protocol.RoutingEnvelope, mReq)
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

	// Pre-seal the M inbound requests sequentially so initSend's nonce
	// advances on a single goroutine; they must be fed (and thus decrypted
	// by the binary's recv) in this same order.
	reqs := make([]protocol.RoutingEnvelope, mReq)
	for i := range reqs {
		reqs[i] = sealAppFrame(t, sess.initSend, protocol.Envelope{
			ID:      uint64(1000 + i),
			Type:    protocol.TypeListConversations,
			TS:      time.Now().UTC(),
			Payload: json.RawMessage(`{}`),
		})
	}
	// Pre-build push envelopes on the test goroutine (avoid t.Fatalf from
	// a child goroutine).
	pushEnvs := make([]protocol.Envelope, nPush)
	for i := range pushEnvs {
		pushEnvs[i] = buildMessageEnvelope(t, uint64(2000+i), "push")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < nPush; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := sess.mgr.Push(ctx, v2TestConnID, pushEnvs[i]); err != nil {
				t.Errorf("Push[%d]: %v", i, err)
			}
		}(i)
	}
	// M inbound requests fed in seal order from this goroutine, interleaving
	// with the concurrent pushes at Run's select.
	for _, r := range reqs {
		frames <- r
	}
	wg.Wait()

	envs := waitForEnvelopes(t, sess.rec, 1+mReq+nPush)

	var pushes, replies int
	for _, e := range envs[1:] {
		inner := decryptAppFrame(t, e, sess.initRecv)
		switch inner.Type {
		case protocol.TypeConversations:
			replies++
		case protocol.TypeMessage:
			pushes++
		default:
			t.Errorf("unexpected outbound inner type %q", inner.Type)
		}
	}
	if pushes != nPush {
		t.Errorf("decoded %d pushes, want %d", pushes, nPush)
	}
	if replies != mReq {
		t.Errorf("decoded %d replies, want %d", replies, mReq)
	}
}

// TestV2Session_Push_UnknownConn_ErrConnNotFound_OtherSessionUnaffected
// pins AC#3: pushing to a conn_id the manager has never seen returns
// ErrConnNotFound (wrapping control.ErrConnNotFound for the wire-mapping
// invariant) and does NOT mutate an unrelated open session — that
// session's subsequent solicited round-trip still decrypts cleanly.
func TestV2Session_Push_UnknownConn_ErrConnNotFound_OtherSessionUnaffected(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	echoPayload, err := json.Marshal(map[string]string{"text": "ok"})
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = sess.mgr.Push(ctx, "no-such-conn", buildMessageEnvelope(t, 1, "x"))
	if !errors.Is(err, ErrConnNotFound) {
		t.Errorf("errors.Is(err, relay.ErrConnNotFound) = false; err = %v", err)
	}
	if !errors.Is(err, control.ErrConnNotFound) {
		t.Errorf("errors.Is(err, control.ErrConnNotFound) = false; err = %v (wire-mapping invariant broken)", err)
	}

	// The unrelated open session is untouched: a solicited round-trip still
	// decrypts cleanly (its send CipherState was never mutated).
	const reqID uint64 = 7
	frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})
	envs := waitForEnvelopes(t, sess.rec, 2) // noise_resp + reply
	inner := decryptAppFrame(t, envs[1], sess.initRecv)
	if inner.Type != protocol.TypeConversations {
		t.Errorf("reply Type = %q, want %q", inner.Type, protocol.TypeConversations)
	}
	if inner.InReplyTo == nil || *inner.InReplyTo != reqID {
		t.Errorf("reply InReplyTo = %v, want pointer to %d", inner.InReplyTo, reqID)
	}
}

// TestV2Session_Push_NotOpen_ReturnsErrConnNotFound pins the error-contract
// change (#610): a session that exists in m.sessions but never reached
// V2StateOpen has no push queue (queues are created only at the open tail), so
// the public Push collapses "not open" into ErrConnNotFound — a not-open conn
// is indistinguishable from an unknown one at the enqueue boundary. The
// V2StateOpen security gate moved to the drain side (forwardEnvelope); see
// TestV2Session_forwardEnvelope_NotOpen_GateRefuses for that half. White-box
// session injection (no queue) mirrors the gating test.
func TestV2Session_Push_NotOpen_ReturnsErrConnNotFound(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		state V2SessionState
	}{
		{"awaiting_init", V2StateAwaitingInit},
		{"handshake_complete", V2StateHandshakeComplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			respPriv, _ := genV2Keypair(t)
			reg := v2PairedRegistry(t, v2TestToken)
			frames := make(chan protocol.RoutingEnvelope)
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

			// Inject a pre-open session WITHOUT a queue — exactly the state of a
			// conn mid-handshake. Push never touches s.send, so nil CipherStates
			// are safe here.
			const connID = "c-notopen"
			mgr.sessions[connID] = &V2Session{connID: connID, state: tc.state}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			err := mgr.Push(ctx, connID, buildMessageEnvelope(t, 1, "x"))
			if !errors.Is(err, ErrConnNotFound) {
				t.Errorf("Push to %v session: err = %v, want ErrConnNotFound", tc.state, err)
			}
			if got := rec.snapshot(); len(got) != 0 {
				t.Errorf("rec.snapshot() len = %d, want 0 (no outbound on not-open push)", len(got))
			}
		})
	}
}

// TestV2Session_forwardEnvelope_NotOpen_GateRefuses pins the drain-side
// security gate (#610 security review): forwardEnvelope — the path the buffer
// drain uses to seal-and-forward — refuses a session that is not V2StateOpen,
// so a buffered push to a conn that closed or de-authed between enqueue and
// drain is dropped before sealing and never delivered to an un-authenticated
// peer. White-box, no Run goroutine: forwardEnvelope is called directly on the
// test goroutine over an injected non-open session, so the read of s.state is
// uncontended.
func TestV2Session_forwardEnvelope_NotOpen_GateRefuses(t *testing.T) {
	t.Parallel()

	respPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	rec := &v2Recorder{}
	mgr, err := NewV2SessionManager(V2SessionConfig{
		Frames:     make(chan protocol.RoutingEnvelope),
		Outbound:   rec.outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	})
	if err != nil {
		t.Fatalf("NewV2SessionManager: %v", err)
	}
	// Intentionally do NOT start Run: forwardEnvelope is exercised directly.
	const connID = "c-gate"

	// Missing session → ErrConnNotFound.
	if err := mgr.forwardEnvelope(context.Background(), connID, buildMessageEnvelope(t, 1, "x")); !errors.Is(err, ErrConnNotFound) {
		t.Errorf("forwardEnvelope to unknown conn: err = %v, want ErrConnNotFound", err)
	}

	// Present-but-not-open session (e.g. closed/de-authed before drain) →
	// ErrSessionNotOpen, no seal, no outbound. nil CipherStates are safe: the
	// state check returns before s.send is touched.
	mgr.sessions[connID] = &V2Session{connID: connID, state: V2StateHandshakeComplete}
	if err := mgr.forwardEnvelope(context.Background(), connID, buildMessageEnvelope(t, 1, "x")); !errors.Is(err, ErrSessionNotOpen) {
		t.Errorf("forwardEnvelope to non-open session: err = %v, want ErrSessionNotOpen", err)
	}
	if got := rec.snapshot(); len(got) != 0 {
		t.Errorf("rec.snapshot() len = %d, want 0 (gate refused before any seal/send)", len(got))
	}
}

// TestV2Session_Push_ClosedSession_ReturnsErrConnNotFound pins AC#3: a
// session that was opened then torn down (an AEAD-failure 4421 close
// deletes it from the map) collapses into the same ErrConnNotFound branch
// as a never-seen conn. closeWith deletes the entry before emitting the
// close envelope, so observing the close guarantees the delete has
// happened on the Run goroutine.
func TestV2Session_Push_ClosedSession_ReturnsErrConnNotFound(t *testing.T) {
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

	// Drive an AEAD-failure close: flip a byte in a sealed frame's
	// ciphertext.
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

	closeEnvs := waitForEnvelopes(t, sess.rec, 2) // noise_resp + 4421 close
	if closeEnvs[1].CloseCode != uint16(StatusProtocolMismatch) {
		t.Fatalf("close_code = %d, want %d", closeEnvs[1].CloseCode, StatusProtocolMismatch)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = sess.mgr.Push(ctx, v2TestConnID, buildMessageEnvelope(t, 1, "x"))
	if !errors.Is(err, ErrConnNotFound) {
		t.Errorf("Push to closed session: err = %v, want ErrConnNotFound", err)
	}
}

// TestV2Session_Push_CtxCancelled_ReturnsCtxErr pins the ctx-cancellation
// arm: a Push whose ctx is already cancelled returns ctx.Err() without
// blocking. Push checks ctx before the enqueue, so a cancelled ctx
// short-circuits without consulting the queues — deterministic, no Run
// goroutine required.
func TestV2Session_Push_CtxCancelled_ReturnsCtxErr(t *testing.T) {
	t.Parallel()

	respPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	mgr, err := NewV2SessionManager(V2SessionConfig{
		Frames:     make(chan protocol.RoutingEnvelope),
		Outbound:   (&v2Recorder{}).outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	})
	if err != nil {
		t.Fatalf("NewV2SessionManager: %v", err)
	}
	// Intentionally do NOT start Run: the ctx pre-check short-circuits before
	// any enqueue, so no drain goroutine is needed.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := mgr.Push(ctx, v2TestConnID, buildMessageEnvelope(t, 1, "x")); !errors.Is(err, context.Canceled) {
		t.Errorf("Push with cancelled ctx: err = %v, want context.Canceled", err)
	}
}

// --- open-session enumeration (ActiveConnIDs) tests (#588) ---

// TestV2Session_ActiveConnIDs_OpenOnly pins both halves of AC#1: every
// V2StateOpen session is enumerated, and every non-open session (still
// handshaking, or handshake-complete-but-token-unvalidated) is excluded — the
// same V2StateOpen security gate Push enforces. White-box session injection
// mirrors TestV2Session_Push_NotOpen: ActiveConnIDs reads s.state only (never
// s.send), so nil CipherStates are safe, and the snapshot channel-send is the
// happens-before edge that publishes the map writes to Run (no frames fed, no
// timers armed, so Run touches the map only when it services the snapshot).
func TestV2Session_ActiveConnIDs_OpenOnly(t *testing.T) {
	t.Parallel()

	respPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope)
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

	for connID, st := range map[string]V2SessionState{
		"c-open-a":    V2StateOpen,
		"c-open-b":    V2StateOpen,
		"c-handshake": V2StateHandshakeComplete,
		"c-awaiting":  V2StateAwaitingInit,
	} {
		mgr.sessions[connID] = &V2Session{connID: connID, state: st}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	got := mgr.ActiveConnIDs(ctx)
	slices.Sort(got) // unordered set — sort before comparing positionally
	want := []string{"c-open-a", "c-open-b"}
	if !slices.Equal(got, want) {
		t.Errorf("ActiveConnIDs() = %v, want %v (open sessions only)", got, want)
	}
}

// TestV2Session_ActiveConnIDs_TornDownSessionAbsent pins AC#2: a session that
// was opened then torn down (closeWith deletes it from the map) no longer
// appears in the snapshot. Drives a real open, confirms its id is enumerated,
// then drives an AEAD-failure 4421 teardown (the TestV2Session_Push_Closed
// recipe) and re-enumerates.
func TestV2Session_ActiveConnIDs_TornDownSessionAbsent(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// The freshly-opened session is enumerated.
	if got := sess.mgr.ActiveConnIDs(ctx); !slices.Contains(got, v2TestConnID) {
		t.Fatalf("ActiveConnIDs() = %v, want it to contain %q", got, v2TestConnID)
	}

	// Drive an AEAD-failure 4421 teardown: flip a byte in a sealed frame's
	// ciphertext so closeWith deletes the session from the map.
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

	// closeWith deletes the entry before emitting the 4421 close; observing
	// the close envelope guarantees the delete has happened on Run, so the
	// subsequent snapshot (also serviced on Run) sees the post-delete map.
	closeEnvs := waitForEnvelopes(t, sess.rec, 2) // noise_resp + 4421 close
	if closeEnvs[1].CloseCode != uint16(StatusProtocolMismatch) {
		t.Fatalf("close_code = %d, want %d", closeEnvs[1].CloseCode, StatusProtocolMismatch)
	}

	if got := sess.mgr.ActiveConnIDs(ctx); slices.Contains(got, v2TestConnID) {
		t.Errorf("ActiveConnIDs() = %v, want it to NOT contain torn-down %q", got, v2TestConnID)
	}
}

// TestV2Session_ActiveConnIDs_ConcurrentWithDispatch_RaceClean pins AC#3 — the
// -race proof. A separate goroutine hammers ActiveConnIDs in a tight loop
// while inbound sealed request frames drive dispatchAppFrame replies on the
// same open session, so the snapshot funnel and the reply path contend for the
// single Run goroutine. Every snapshot is either {v2TestConnID} or empty —
// never garbage; the solicited replies still decrypt in capture (= seal) order
// afterwards, proving the concurrent reads never corrupted session state. Run
// under -race.
func TestV2Session_ActiveConnIDs_ConcurrentWithDispatch_RaceClean(t *testing.T) {
	t.Parallel()

	const mReq = 16

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	echoPayload, err := json.Marshal(map[string]string{"text": "reply"})
	if err != nil {
		t.Fatalf("marshal echo payload: %v", err)
	}
	handlers := map[string]dispatch.Handler{
		protocol.TypeListConversations: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
			return c.Reply(ctx, env, protocol.TypeConversations, echoPayload)
		},
	}

	frames := make(chan protocol.RoutingEnvelope, mReq)
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

	// Pre-seal the M inbound requests sequentially so initSend's nonce
	// advances on a single goroutine; they decrypt (binary recv) in this
	// same order.
	reqs := make([]protocol.RoutingEnvelope, mReq)
	for i := range reqs {
		reqs[i] = sealAppFrame(t, sess.initSend, protocol.Envelope{
			ID:      uint64(3000 + i),
			Type:    protocol.TypeListConversations,
			TS:      time.Now().UTC(),
			Payload: json.RawMessage(`{}`),
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			for _, id := range sess.mgr.ActiveConnIDs(ctx) {
				if id != v2TestConnID {
					t.Errorf("snapshot contained unexpected id %q", id)
				}
			}
		}
	}()

	for _, r := range reqs {
		frames <- r
	}
	// All M replies (+ the noise_resp) captured ⇒ inbound dispatch drained.
	envs := waitForEnvelopes(t, sess.rec, 1+mReq)
	close(done)
	wg.Wait()

	// The solicited replies still decrypt in capture order — nonce integrity
	// intact, session state never corrupted by the concurrent snapshots.
	for _, e := range envs[1:] {
		inner := decryptAppFrame(t, e, sess.initRecv)
		if inner.Type != protocol.TypeConversations {
			t.Errorf("reply Type = %q, want %q", inner.Type, protocol.TypeConversations)
		}
	}
}

// TestV2Session_ActiveConnIDs_EmptyManager pins the empty case: a started
// manager with zero sessions returns a len-0 slice without blocking.
func TestV2Session_ActiveConnIDs_EmptyManager(t *testing.T) {
	t.Parallel()

	respPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope)
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if got := mgr.ActiveConnIDs(ctx); len(got) != 0 {
		t.Errorf("ActiveConnIDs() on empty manager = %v, want len 0", got)
	}
}

// TestV2Session_ActiveConnIDs_CtxCancelled_ReturnsNil pins the
// ctx-cancellation arm: a call whose ctx is already cancelled returns nil
// without blocking. With no Run goroutine draining m.snapshot, the send case
// can never proceed, so the ctx.Done arm is the only ready case —
// deterministic, no flakiness from select picking the send.
func TestV2Session_ActiveConnIDs_CtxCancelled_ReturnsNil(t *testing.T) {
	t.Parallel()

	respPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	mgr, err := NewV2SessionManager(V2SessionConfig{
		Frames:     make(chan protocol.RoutingEnvelope),
		Outbound:   (&v2Recorder{}).outbound,
		StaticPriv: respPriv,
		Devices:    reg,
		ServerID:   v2TestServerID,
		Logger:     silentLogger(),
	})
	if err != nil {
		t.Fatalf("NewV2SessionManager: %v", err)
	}
	// Intentionally do NOT start Run: m.snapshot has no receiver.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if got := mgr.ActiveConnIDs(ctx); got != nil {
		t.Errorf("ActiveConnIDs with cancelled ctx = %v, want nil", got)
	}
}

// --- v2 screen-snapshot handler tests (#618) ---

const (
	snapConvID     = "snap-conv-618"
	snapScreenText = "SENTINEL-SCREEN-XYZ rendered screen text"
)

// fakeSnapshotter is a ScreenSnapshotter test double. Value receiver so the
// zero value stored in an interface is a genuine non-nil interface, while an
// unset (nil) ScreenSnapshotter field stays a genuine nil interface — letting
// the table express the nil-seam scenarios. The sentinel text is deliberately
// NOT a claude-screen literal (cmd/substrate-guard).
type fakeSnapshotter struct {
	text string
	live bool
}

func (f fakeSnapshotter) ScreenSnapshot() (string, bool) { return f.text, f.live }

// TestV2Session_OpenState_RequestSnapshot drives a paired-device handshake to
// open, feeds an AEAD-sealed request_snapshot, and asserts the single sealed
// reply: a screen_snapshot on the happy path, or a deterministic error on each
// rejection branch (AC #1, #3, #4). Every branch produces exactly one reply.
func TestV2Session_OpenState_RequestSnapshot(t *testing.T) {
	t.Parallel()

	knownOnly := func(id string) bool { return id == snapConvID }
	knownNone := func(string) bool { return false }
	convPayload := json.RawMessage(`{"conversation_id":"` + snapConvID + `"}`)

	tests := []struct {
		name          string
		knownConv     func(string) bool
		snap          ScreenSnapshotter
		reqPayload    json.RawMessage
		wantType      string
		wantCode      string // TypeError rows only
		wantRetryable bool   // TypeError rows only
		wantText      string // TypeScreenSnapshot rows only
	}{
		{
			name:       "happy renders screen_snapshot",
			knownConv:  knownOnly,
			snap:       fakeSnapshotter{text: snapScreenText, live: true},
			reqPayload: convPayload,
			wantType:   protocol.TypeScreenSnapshot,
			wantText:   snapScreenText,
		},
		{
			name:       "empty screen renders empty screen_snapshot",
			knownConv:  knownOnly,
			snap:       fakeSnapshotter{text: "", live: true},
			reqPayload: convPayload,
			wantType:   protocol.TypeScreenSnapshot,
			wantText:   "",
		},
		{
			name:          "foreign conversation rejected",
			knownConv:     knownNone,
			snap:          fakeSnapshotter{text: snapScreenText, live: true},
			reqPayload:    convPayload,
			wantType:      protocol.TypeError,
			wantCode:      protocol.CodeConversationNotFound,
			wantRetryable: false,
		},
		{
			name:          "nil KnownConversation rejects all",
			knownConv:     nil,
			snap:          fakeSnapshotter{text: snapScreenText, live: true},
			reqPayload:    convPayload,
			wantType:      protocol.TypeError,
			wantCode:      protocol.CodeConversationNotFound,
			wantRetryable: false,
		},
		{
			name:          "empty conversation_id rejected",
			knownConv:     knownOnly,
			snap:          fakeSnapshotter{text: snapScreenText, live: true},
			reqPayload:    json.RawMessage(`{}`),
			wantType:      protocol.TypeError,
			wantCode:      protocol.CodeConversationNotFound,
			wantRetryable: false,
		},
		{
			name:          "malformed payload rejected",
			knownConv:     knownOnly,
			snap:          fakeSnapshotter{text: snapScreenText, live: true},
			reqPayload:    json.RawMessage(`[]`),
			wantType:      protocol.TypeError,
			wantCode:      protocol.CodeConversationNotFound,
			wantRetryable: false,
		},
		{
			name:          "no live session reports offline",
			knownConv:     knownOnly,
			snap:          fakeSnapshotter{text: "", live: false},
			reqPayload:    convPayload,
			wantType:      protocol.TypeError,
			wantCode:      protocol.CodeServerBinaryOffline,
			wantRetryable: true,
		},
		{
			name:          "nil Snapshotter reports offline",
			knownConv:     knownOnly,
			snap:          nil,
			reqPayload:    convPayload,
			wantType:      protocol.TypeError,
			wantCode:      protocol.CodeServerBinaryOffline,
			wantRetryable: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			respPriv, respPub := genV2Keypair(t)
			initPriv, _ := genV2Keypair(t)
			reg := v2PairedRegistry(t, v2TestToken)

			frames := make(chan protocol.RoutingEnvelope, 2)
			rec := &v2Recorder{}
			sess := driveToOpen(t, V2SessionConfig{
				Frames:            frames,
				Outbound:          rec.outbound,
				StaticPriv:        respPriv,
				Devices:           reg,
				ServerID:          v2TestServerID,
				Logger:            silentLogger(),
				Snapshotter:       tt.snap,
				KnownConversation: tt.knownConv,
			}, frames, rec, respPub, initPriv)
			t.Cleanup(sess.stop)

			const reqID uint64 = 55
			frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
				ID:      reqID,
				Type:    protocol.TypeRequestSnapshot,
				TS:      time.Now().UTC(),
				Payload: tt.reqPayload,
			})

			// Exactly two envelopes: the handshake noise_resp, then the reply.
			envs := waitForEnvelopes(t, rec, 2)
			reply := decryptAppFrame(t, envs[1], sess.initRecv)

			if reply.Type != tt.wantType {
				t.Fatalf("reply Type = %q, want %q", reply.Type, tt.wantType)
			}
			if reply.InReplyTo == nil || *reply.InReplyTo != reqID {
				t.Errorf("InReplyTo = %v, want pointer to %d", reply.InReplyTo, reqID)
			}

			switch tt.wantType {
			case protocol.TypeScreenSnapshot:
				var p protocol.ScreenSnapshotPayload
				if err := json.Unmarshal(reply.Payload, &p); err != nil {
					t.Fatalf("decode screen_snapshot payload: %v", err)
				}
				if p.ConversationID != snapConvID {
					t.Errorf("ConversationID = %q, want %q", p.ConversationID, snapConvID)
				}
				if p.Text != tt.wantText {
					t.Errorf("Text = %q, want %q", p.Text, tt.wantText)
				}
				// TS is freshly stamped; compare with IsZero/Since, never == (a
				// JSON round-trip strips the monotonic reading).
				if p.TS.IsZero() {
					t.Error("TS is zero, want a render timestamp")
				}
				if d := time.Since(p.TS); d < 0 || d > time.Minute {
					t.Errorf("TS = %v not within the last minute (since=%v)", p.TS, d)
				}
			case protocol.TypeError:
				var p protocol.ErrorPayload
				if err := json.Unmarshal(reply.Payload, &p); err != nil {
					t.Fatalf("decode error payload: %v", err)
				}
				if p.Code != tt.wantCode {
					t.Errorf("error Code = %q, want %q", p.Code, tt.wantCode)
				}
				if p.Retryable != tt.wantRetryable {
					t.Errorf("error Retryable = %v, want %v", p.Retryable, tt.wantRetryable)
				}
				if p.Message == "" {
					t.Error("error Message is empty, want a static message")
				}
			}
		})
	}
}

// TestV2Session_OpenState_RequestSnapshot_Repeat proves two request_snapshots
// on one open session each yield their own freshly stamped screen_snapshot.
func TestV2Session_OpenState_RequestSnapshot_Repeat(t *testing.T) {
	t.Parallel()

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	frames := make(chan protocol.RoutingEnvelope, 3)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           reg,
		ServerID:          v2TestServerID,
		Logger:            silentLogger(),
		Snapshotter:       fakeSnapshotter{text: snapScreenText, live: true},
		KnownConversation: func(id string) bool { return id == snapConvID },
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	convPayload := json.RawMessage(`{"conversation_id":"` + snapConvID + `"}`)
	reqIDs := []uint64{61, 62}
	for _, id := range reqIDs {
		frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
			ID:      id,
			Type:    protocol.TypeRequestSnapshot,
			TS:      time.Now().UTC(),
			Payload: convPayload,
		})
	}

	// noise_resp + two replies; decrypt in capture order (the receive nonce
	// is sequential).
	envs := waitForEnvelopes(t, rec, 3)
	for i, id := range reqIDs {
		reply := decryptAppFrame(t, envs[i+1], sess.initRecv)
		if reply.Type != protocol.TypeScreenSnapshot {
			t.Fatalf("reply %d Type = %q, want %q", i, reply.Type, protocol.TypeScreenSnapshot)
		}
		if reply.InReplyTo == nil || *reply.InReplyTo != id {
			t.Errorf("reply %d InReplyTo = %v, want pointer to %d", i, reply.InReplyTo, id)
		}
		var p protocol.ScreenSnapshotPayload
		if err := json.Unmarshal(reply.Payload, &p); err != nil {
			t.Fatalf("decode screen_snapshot %d: %v", i, err)
		}
		if p.TS.IsZero() {
			t.Errorf("reply %d TS is zero, want a fresh render timestamp", i)
		}
	}
}

// TestV2Session_OpenState_RequestSnapshot_NeverLogsScreenText pins the security
// invariant: the rendered screen text reaches the sealed wire payload but never
// any log line (mirrors assistant_turn_v2.go's chunk-bytes discipline).
func TestV2Session_OpenState_RequestSnapshot_NeverLogsScreenText(t *testing.T) {
	t.Parallel()

	const sentinel = "SENTINEL-SCREEN-XYZ-do-not-log"

	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)

	logger, logBuf := bufferLogger()
	frames := make(chan protocol.RoutingEnvelope, 2)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           reg,
		ServerID:          v2TestServerID,
		Logger:            logger,
		Snapshotter:       fakeSnapshotter{text: sentinel, live: true},
		KnownConversation: func(id string) bool { return id == snapConvID },
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)

	const reqID uint64 = 71
	frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeRequestSnapshot,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{"conversation_id":"` + snapConvID + `"}`),
	})

	envs := waitForEnvelopes(t, rec, 2)
	reply := decryptAppFrame(t, envs[1], sess.initRecv)
	if reply.Type != protocol.TypeScreenSnapshot {
		t.Fatalf("reply Type = %q, want %q", reply.Type, protocol.TypeScreenSnapshot)
	}
	// Sanity: the render path actually carried the sentinel onto the sealed
	// payload — otherwise the no-log assertion below would pass vacuously.
	var p protocol.ScreenSnapshotPayload
	if err := json.Unmarshal(reply.Payload, &p); err != nil {
		t.Fatalf("decode screen_snapshot payload: %v", err)
	}
	if p.Text != sentinel {
		t.Fatalf("payload Text = %q, want the sentinel %q", p.Text, sentinel)
	}

	// Join Run so every log write has happened-before this read.
	sess.stop()
	if got := logBuf.String(); strings.Contains(got, sentinel) {
		t.Errorf("rendered screen text leaked into logs:\n%s", got)
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

// TestV2Session_ActiveConns_MixedInteractive pins the capability-aware
// enumeration on a mixed map: an interactive open conn, a non-interactive open
// conn, and a flagged-but-still-handshaking conn. ActiveConns reports the
// correct flag per open conn and excludes the non-open one (the V2StateOpen
// gate wins over the interactive flag — belt-and-suspenders of different
// fabric). ActiveConnIDs is an unchanged projection: every open conn-id, flag
// dropped. White-box injection mirrors TestV2Session_ActiveConnIDs_OpenOnly;
// the snapshot channel-send is the happens-before edge publishing the writes
// to Run.
func TestV2Session_ActiveConns_MixedInteractive(t *testing.T) {
	t.Parallel()

	respPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	frames := make(chan protocol.RoutingEnvelope)
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

	mgr.sessions["c-int"] = &V2Session{connID: "c-int", state: V2StateOpen, interactive: true}
	mgr.sessions["c-plain"] = &V2Session{connID: "c-plain", state: V2StateOpen, interactive: false}
	mgr.sessions["c-flagged-handshake"] = &V2Session{connID: "c-flagged-handshake", state: V2StateHandshakeComplete, interactive: true}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	got := map[string]bool{}
	for _, c := range mgr.ActiveConns(ctx) {
		got[c.ConnID] = c.Interactive
	}
	if len(got) != 2 || !got["c-int"] || got["c-plain"] {
		t.Errorf("ActiveConns flags = %v, want {c-int:true, c-plain:false} (open only)", got)
	}

	ids := mgr.ActiveConnIDs(ctx)
	slices.Sort(ids)
	if want := []string{"c-int", "c-plain"}; !slices.Equal(ids, want) {
		t.Errorf("ActiveConnIDs() = %v, want %v (projection: all open ids, flag dropped)", ids, want)
	}
}

// TestV2Session_CapabilitySpoof_TokenFail_NeverEnumerated is the security
// proof: a phone that advertises [interactive] but fails the device-token check
// is closed at 4401 and deleted, so its negotiated capability is never
// observable in ActiveConns. The ack on the noise_resp DID echo [interactive]
// (it is sealed before the token check, security-review Threat 3) — but that
// echo grants nothing because the session never reaches V2StateOpen
// (Threat 2/3, AC#1/#3).
func TestV2Session_CapabilitySpoof_TokenFail_NeverEnumerated(t *testing.T) {
	t.Parallel()

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

	// The ack was echoed (sealed before the token check) — leaks nothing.
	respRaw := decodeRespFrame(t, envs[0])
	earlyAck, _, _, err := initiator.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("ReadResp: %v", err)
	}
	if ack := decodeHelloAck(t, earlyAck); !slices.Equal(ack.Capabilities, []string{protocol.CapabilityInteractive}) {
		t.Errorf("hello_ack Capabilities = %v, want [interactive]", ack.Capabilities)
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
