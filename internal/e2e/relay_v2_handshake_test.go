//go:build e2e

package e2e

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// TestRelayV2_Handshake covers the v2 binary-side handshake path
// end-to-end against fakerelay's /v2/server route. Three subtests share
// the same harness pattern: spin up a fakerelay, wire relay.Connect +
// V2SessionManager inline (no daemon), dial a phone, drive a Noise_IK
// handshake from the phone side, assert on the binary's reply.
func TestRelayV2_Handshake(t *testing.T) {
	t.Run("happy_path", testV2HappyPath)
	t.Run("bad_token_4401", testV2BadToken)
	t.Run("ik_reject_4426", testV2IKReject)
}

// v2Harness bundles the per-test wiring: fakerelay, binary↔relay
// connection, manager goroutine, and lifecycle cleanup.
type v2Harness struct {
	fr        *fakerelay.Server
	conn      *relay.Connection
	mgrErrCh  chan error
	mgrCancel context.CancelFunc
	serverID  identity.ServerID
	pubKey    []byte
}

// startV2Harness wires up a fakerelay + binary↔relay Connection +
// V2SessionManager for a single test. handlers may be nil — when nil the
// session manager has no app handlers registered and open-state app
// envelopes fall through to protocol.unsupported error replies. Used by
// #446's open-state dispatch tests, which register echo handlers to
// exercise the encrypted reply path.
func startV2Harness(t *testing.T, reg *devices.Registry, handlers map[string]dispatch.Handler) *v2Harness {
	t.Helper()

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	binPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate static key: %v", err)
	}
	serverID := identity.ServerID("v2-e2e-server")

	connCtx, connCancel := context.WithCancel(context.Background())
	t.Cleanup(connCancel)

	conn, err := relay.Connect(connCtx, relay.Config{
		ServerID:            serverID,
		RelayURL:            fr.URL() + "/v2/server",
		BinaryVersion:       "0.0.1-test",
		Logger:              relayTestLogger(),
		AllowInsecureScheme: true,
	})
	if err != nil {
		t.Fatalf("relay.Connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Block until the binary↔relay handshake has landed before any phone
	// dials — otherwise phone→binary routing would race against
	// binary-side WS upgrade completion.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := fr.LastBinaryHello(string(serverID)); ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := fr.LastBinaryHello(string(serverID)); !ok {
		t.Fatal("binary↔relay hello not observed within 5s")
	}

	mgrCtx, mgrCancel := context.WithCancel(context.Background())
	t.Cleanup(mgrCancel)

	mgr, err := relay.NewV2SessionManager(relay.V2SessionConfig{
		Frames:     conn.Frames(),
		Outbound:   conn.Send,
		StaticPriv: binPriv.Bytes(),
		Devices:    reg,
		ServerID:   string(serverID),
		Logger:     relayTestLogger(),
		Handlers:   handlers,
	})
	if err != nil {
		t.Fatalf("NewV2SessionManager: %v", err)
	}

	mgrErrCh := make(chan error, 1)
	go func() { mgrErrCh <- mgr.Run(mgrCtx) }()

	return &v2Harness{
		fr:        fr,
		conn:      conn,
		mgrErrCh:  mgrErrCh,
		mgrCancel: mgrCancel,
		serverID:  serverID,
		pubKey:    binPriv.PublicKey().Bytes(),
	}
}

// dialPhone connects a fakephone to the harness's relay and returns the
// connected client (caller is responsible for sending the noise_init).
func (h *v2Harness) dialPhone(t *testing.T) *fakephone.Client {
	t.Helper()
	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Phone-side /v1/client is path-shared with v2 today (the routing
	// wire is unchanged). The token arg is irrelevant for v2 — the
	// binary ignores RoutingEnvelope.Token — but fakerelay requires a
	// non-empty header, so pass a placeholder.
	phone, err := fakephone.Dial(dialCtx, h.fr.URL(), string(h.serverID),
		"ignored-by-v2", "v2-e2e-phone")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	return phone
}

// sendNoiseInit serializes msg as an InnerFrameV2(noise_init) and writes
// it over the phone WS.
func sendNoiseInit(t *testing.T, phone *fakephone.Client, msg []byte) {
	t.Helper()
	wireFrame, err := json.Marshal(protocol.InnerFrameV2{
		Version: protocol.V2Version,
		Type:    protocol.TypeNoiseInit,
		Data:    base64.StdEncoding.EncodeToString(msg),
	})
	if err != nil {
		t.Fatalf("marshal noise_init wire frame: %v", err)
	}
	if err := phone.SendBytes(wireFrame); err != nil {
		t.Fatalf("phone send noise_init: %v", err)
	}
}

// readInnerFrame reads one WS text frame from the phone and decodes it
// as an InnerFrameV2.
func readInnerFrame(t *testing.T, phone *fakephone.Client, timeout time.Duration) protocol.InnerFrameV2 {
	t.Helper()
	raw, err := phone.ReceiveBytes(timeout)
	if err != nil {
		t.Fatalf("phone receive: %v", err)
	}
	var inner protocol.InnerFrameV2
	if err := json.Unmarshal(raw, &inner); err != nil {
		t.Fatalf("decode inner frame: %v", err)
	}
	return inner
}

func buildHelloEarly(t *testing.T, token string) []byte {
	t.Helper()
	payload, err := json.Marshal(protocol.HelloClientPayload{
		Role:             "client",
		DeviceName:       "v2-e2e-phone",
		ClientVersion:    "0.0.1-test",
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

// testV2HappyPath drives a paired-device handshake end-to-end and
// asserts the phone receives a noise_resp whose early-data decodes to
// hello_ack{ProtocolVersion=v2}.
func testV2HappyPath(t *testing.T) {
	const plainToken = "v2-e2e-paired-token-001"
	reg := &devices.Registry{}
	reg.Add(devices.Device{
		TokenHash: devices.HashToken(plainToken),
		Name:      "v2-e2e-phone",
		PairedAt:  time.Now().UTC(),
	})

	h := startV2Harness(t, reg, nil)
	phone := h.dialPhone(t)

	initPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("phone keygen: %v", err)
	}
	initiator, err := noise.NewInitiator(initPriv.Bytes(), h.pubKey)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarly(t, plainToken))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	sendNoiseInit(t, phone, initMsg)

	inner := readInnerFrame(t, phone, 3*time.Second)
	if inner.Type != protocol.TypeNoiseResp {
		t.Fatalf("got inner type %q, want %q", inner.Type, protocol.TypeNoiseResp)
	}
	respRaw, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatalf("decode noise_resp data: %v", err)
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
	var ack protocol.HelloAckPayload
	if err := json.Unmarshal(ackEnv.Payload, &ack); err != nil {
		t.Fatalf("decode hello_ack payload: %v", err)
	}
	if ack.ProtocolVersion != "v2" {
		t.Errorf("ProtocolVersion = %q, want %q", ack.ProtocolVersion, "v2")
	}
}

// testV2BadToken drives a handshake whose hello carries an unpaired
// token. The phone must read the AEAD-sealed error envelope, then
// observe a WS close with code 4401.
func testV2BadToken(t *testing.T) {
	reg := &devices.Registry{} // empty registry: every token rejects
	h := startV2Harness(t, reg, nil)
	phone := h.dialPhone(t)

	initPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("phone keygen: %v", err)
	}
	initiator, err := noise.NewInitiator(initPriv.Bytes(), h.pubKey)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarly(t, "definitely-not-paired"))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	sendNoiseInit(t, phone, initMsg)

	// Frame 1: noise_resp.
	first := readInnerFrame(t, phone, 3*time.Second)
	if first.Type != protocol.TypeNoiseResp {
		t.Fatalf("first inner type %q, want noise_resp", first.Type)
	}
	respRaw, err := base64.StdEncoding.DecodeString(first.Data)
	if err != nil {
		t.Fatalf("decode noise_resp data: %v", err)
	}
	_, _, initRecv, err := initiator.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("initiator.ReadResp: %v", err)
	}

	// Frame 2: noise_msg with AEAD-sealed auth.invalid_token error.
	second := readInnerFrame(t, phone, 3*time.Second)
	if second.Type != protocol.TypeNoiseMsg {
		t.Fatalf("second inner type %q, want noise_msg", second.Type)
	}
	cipher, err := base64.StdEncoding.DecodeString(second.Data)
	if err != nil {
		t.Fatalf("decode noise_msg data: %v", err)
	}
	plaintext, err := initRecv.Decrypt(cipher)
	if err != nil {
		t.Fatalf("phone decrypt error envelope: %v", err)
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

	// Frame 3: the WS close.
	_, err = phone.ReceiveBytes(3 * time.Second)
	if err == nil {
		t.Fatal("phone receive: nil err, want WS close")
	}
	if errors.Is(err, fakephone.ErrReceiveTimeout) {
		t.Fatalf("phone receive timed out without seeing close: %v", err)
	}
	code, ok := phone.LastCloseStatus()
	if !ok {
		t.Fatal("phone LastCloseStatus: not set")
	}
	if int(code) != 4401 {
		t.Errorf("WS close code = %d, want 4401", int(code))
	}
}

// testV2IKReject drives an invalid noise_init (random bytes) and asserts
// the phone observes a WS close with code 4426 and no prior application
// frame.
func testV2IKReject(t *testing.T) {
	reg := &devices.Registry{}
	h := startV2Harness(t, reg, nil)
	phone := h.dialPhone(t)

	garbage := make([]byte, 96)
	if _, err := rand.Read(garbage); err != nil {
		t.Fatalf("rand: %v", err)
	}
	sendNoiseInit(t, phone, garbage)

	// Expect a WS close with no prior frame from the binary.
	_, err := phone.ReceiveBytes(3 * time.Second)
	if err == nil {
		t.Fatal("phone receive: nil err, want WS close")
	}
	if errors.Is(err, fakephone.ErrReceiveTimeout) {
		t.Fatalf("phone receive timed out without seeing close: %v", err)
	}
	code, ok := phone.LastCloseStatus()
	if !ok {
		t.Fatal("phone LastCloseStatus: not set")
	}
	if int(code) != 4426 {
		t.Errorf("WS close code = %d, want 4426", int(code))
	}
}
