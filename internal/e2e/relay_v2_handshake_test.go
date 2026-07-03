//go:build e2e

package e2e

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	t.Run("encrypted_echo_round_trip", testV2EncryptedEchoRoundTrip)
	t.Run("tampered_noise_msg_4421", testV2TamperedNoiseMsg_4421)
	t.Run("reload_picks_up_newly_paired", testV2ReloadPicksUpNewlyPaired)
	t.Run("reload_malformed_fails_closed", testV2ReloadMalformedFailsClosed)
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
func startV2Harness(t *testing.T, reg *devices.Registry, handlers map[string]dispatch.Handler, opts ...func(*relay.V2SessionConfig)) *v2Harness {
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

	// Block until the binary's relay connection has registered before any
	// phone dials — otherwise phone→binary routing would race against
	// binary-side WS upgrade completion.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !fr.WaitBinary(ctx, string(serverID)) {
		t.Fatal("binary connection not registered within 5s")
	}

	mgrCtx, mgrCancel := context.WithCancel(context.Background())
	t.Cleanup(mgrCancel)

	cfg := relay.V2SessionConfig{
		Frames:     conn.Frames(),
		Outbound:   conn.Send,
		StaticPriv: binPriv.Bytes(),
		Devices:    reg,
		ServerID:   string(serverID),
		Logger:     relayTestLogger(),
		Handlers:   handlers,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	mgr, err := relay.NewV2SessionManager(cfg)
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

// sendNoiseMsg wraps ciphertext as an InnerFrameV2(noise_msg) and sends
// it over the phone WS. Counterpart of sendNoiseInit for open-state
// dispatch tests.
func sendNoiseMsg(t *testing.T, phone *fakephone.Client, ciphertext []byte) {
	t.Helper()
	wireFrame, err := json.Marshal(protocol.InnerFrameV2{
		Version: protocol.V2Version,
		Type:    protocol.TypeNoiseMsg,
		Data:    base64.StdEncoding.EncodeToString(ciphertext),
	})
	if err != nil {
		t.Fatalf("marshal noise_msg wire frame: %v", err)
	}
	if err := phone.SendBytes(wireFrame); err != nil {
		t.Fatalf("phone send noise_msg: %v", err)
	}
}

// driveHandshakeToOpen runs a paired-device handshake from the phone
// side against h, returning the initiator's CipherStates ready for
// open-state application dispatch (initSend encrypts phone→binary,
// initRecv decrypts binary→phone).
func driveHandshakeToOpen(t *testing.T, h *v2Harness, phone *fakephone.Client, token string) (*noise.CipherState, *noise.CipherState) {
	t.Helper()
	initPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("phone keygen: %v", err)
	}
	initiator, err := noise.NewInitiator(initPriv.Bytes(), h.pubKey)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarly(t, token))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	sendNoiseInit(t, phone, initMsg)

	inner := readInnerFrame(t, phone, 3*time.Second)
	if inner.Type != protocol.TypeNoiseResp {
		t.Fatalf("handshake: got inner type %q, want %q", inner.Type, protocol.TypeNoiseResp)
	}
	respRaw, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatalf("decode noise_resp data: %v", err)
	}
	_, initSend, initRecv, err := initiator.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("initiator.ReadResp: %v", err)
	}
	return initSend, initRecv
}

// testV2EncryptedEchoRoundTrip drives a paired-device handshake to
// V2StateOpen, registers an echo handler keyed by TypeListConversations,
// AEAD-seals an envelope, sends it as noise_msg, and asserts the phone
// receives an AEAD-sealed reply whose decoded inner envelope matches
// the registered handler's response with InReplyTo pointing at the
// request id. Covers AC #1 end-to-end through the existing handler chain.
func testV2EncryptedEchoRoundTrip(t *testing.T) {
	const plainToken = "v2-e2e-dispatch-token-001"
	reg := &devices.Registry{}
	reg.Add(devices.Device{
		TokenHash: devices.HashToken(plainToken),
		Name:      "v2-e2e-phone",
		PairedAt:  time.Now().UTC(),
	})

	const replyText = "e2e-echo-payload"
	echoPayload, err := json.Marshal(map[string]string{"text": replyText})
	if err != nil {
		t.Fatalf("marshal echo payload: %v", err)
	}
	handlers := map[string]dispatch.Handler{
		protocol.TypeListConversations: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
			return c.Reply(ctx, env, protocol.TypeConversations, echoPayload)
		},
	}

	h := startV2Harness(t, reg, handlers)
	phone := h.dialPhone(t)
	initSend, initRecv := driveHandshakeToOpen(t, h, phone, plainToken)

	const reqID uint64 = 11
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("marshal request envelope: %v", err)
	}
	ciphertext, err := initSend.Encrypt(reqEnv)
	if err != nil {
		t.Fatalf("seal request envelope: %v", err)
	}
	sendNoiseMsg(t, phone, ciphertext)

	inner := readInnerFrame(t, phone, 3*time.Second)
	if inner.Type != protocol.TypeNoiseMsg {
		t.Fatalf("reply inner type = %q, want %q", inner.Type, protocol.TypeNoiseMsg)
	}
	replyCipher, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatalf("decode reply data: %v", err)
	}
	replyPlain, err := initRecv.Decrypt(replyCipher)
	if err != nil {
		t.Fatalf("phone decrypt reply: %v", err)
	}
	var replyEnv protocol.Envelope
	if err := json.Unmarshal(replyPlain, &replyEnv); err != nil {
		t.Fatalf("decode reply envelope: %v", err)
	}
	if replyEnv.Type != protocol.TypeConversations {
		t.Errorf("reply Type = %q, want %q", replyEnv.Type, protocol.TypeConversations)
	}
	if replyEnv.InReplyTo == nil || *replyEnv.InReplyTo != reqID {
		t.Errorf("InReplyTo = %v, want pointer to %d", replyEnv.InReplyTo, reqID)
	}
	var gotPayload map[string]string
	if err := json.Unmarshal(replyEnv.Payload, &gotPayload); err != nil {
		t.Fatalf("decode reply payload: %v", err)
	}
	if gotPayload["text"] != replyText {
		t.Errorf("reply payload text = %q, want %q", gotPayload["text"], replyText)
	}
}

// echoConversationsHandler returns a handler table whose TypeListConversations
// handler echoes a fixed reply, so an open-state round-trip can prove a
// session is genuinely accepted (a rejected token still receives a noise_resp,
// so the hello_ack alone does not distinguish accept from reject).
func echoConversationsHandler(t *testing.T, replyText string) map[string]dispatch.Handler {
	t.Helper()
	echoPayload, err := json.Marshal(map[string]string{"text": replyText})
	if err != nil {
		t.Fatalf("marshal echo payload: %v", err)
	}
	return map[string]dispatch.Handler{
		protocol.TypeListConversations: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
			return c.Reply(ctx, env, protocol.TypeConversations, echoPayload)
		},
	}
}

// assertOpenEcho sends one TypeListConversations request through an open v2
// session and asserts the reply decrypts to a TypeConversations envelope —
// decisive proof the hello token was accepted (an unpaired token's session is
// closed at 4401 after the noise_resp, so no encrypted echo comes back).
func assertOpenEcho(t *testing.T, phone *fakephone.Client, initSend, initRecv *noise.CipherState, reqID uint64) {
	t.Helper()
	reqEnv, err := json.Marshal(protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("marshal request envelope: %v", err)
	}
	ciphertext, err := initSend.Encrypt(reqEnv)
	if err != nil {
		t.Fatalf("seal request: %v", err)
	}
	sendNoiseMsg(t, phone, ciphertext)

	inner := readInnerFrame(t, phone, 3*time.Second)
	if inner.Type != protocol.TypeNoiseMsg {
		t.Fatalf("reply inner type = %q, want %q (token not accepted)", inner.Type, protocol.TypeNoiseMsg)
	}
	replyCipher, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatalf("decode reply data: %v", err)
	}
	replyPlain, err := initRecv.Decrypt(replyCipher)
	if err != nil {
		t.Fatalf("phone decrypt reply: %v", err)
	}
	var replyEnv protocol.Envelope
	if err := json.Unmarshal(replyPlain, &replyEnv); err != nil {
		t.Fatalf("decode reply envelope: %v", err)
	}
	if replyEnv.Type != protocol.TypeConversations {
		t.Fatalf("reply Type = %q, want %q (token not accepted)", replyEnv.Type, protocol.TypeConversations)
	}
}

// testV2ReloadPicksUpNewlyPaired is AC1 end-to-end: with DevicesPath wired,
// a device B appended to devices.json by a separate `pyry pair` process after
// startup authenticates on its next handshake with no daemon restart. The
// encrypted echo round-trip confirms B's session is genuinely open.
func testV2ReloadPicksUpNewlyPaired(t *testing.T) {
	const (
		tokenA = "v2-reload-token-a"
		tokenB = "v2-reload-token-b"
	)
	devA := devices.Device{
		TokenHash: devices.HashToken(tokenA),
		Name:      "device-a",
		PairedAt:  time.Now().UTC(),
	}
	reg := &devices.Registry{}
	reg.Add(devA)

	// Seed devices.json with [A] — the snapshot the daemon loaded at startup.
	path := filepath.Join(t.TempDir(), "devices.json")
	if err := reg.Save(path); err != nil {
		t.Fatalf("Save seed [A]: %v", err)
	}

	h := startV2Harness(t, reg, echoConversationsHandler(t, "reload-echo-ok"),
		func(cfg *relay.V2SessionConfig) { cfg.DevicesPath = path })

	// `pyry pair` (a separate process) appends B to the same file after
	// startup — B is on disk but absent from the in-memory snapshot.
	diskReg := &devices.Registry{}
	diskReg.Add(devA)
	diskReg.Add(devices.Device{
		TokenHash: devices.HashToken(tokenB),
		Name:      "device-b",
		PairedAt:  time.Now().UTC(),
	})
	if err := diskReg.Save(path); err != nil {
		t.Fatalf("Save [A,B]: %v", err)
	}

	// B handshakes with no restart: the per-handshake reload adopts B.
	phone := h.dialPhone(t)
	initSend, initRecv := driveHandshakeToOpen(t, h, phone, tokenB)
	assertOpenEcho(t, phone, initSend, initRecv, 21)
}

// testV2ReloadMalformedFailsClosed is AC4 at the handshake: after
// devices.json is corrupted post-startup, the reload fails closed — an
// unpaired token is still rejected (4401, accept set not widened) and the
// startup-known device still authenticates against the retained set.
func testV2ReloadMalformedFailsClosed(t *testing.T) {
	const tokenA = "v2-reload-retained-token"
	reg := &devices.Registry{}
	reg.Add(devices.Device{
		TokenHash: devices.HashToken(tokenA),
		Name:      "device-a",
		PairedAt:  time.Now().UTC(),
	})

	path := filepath.Join(t.TempDir(), "devices.json")
	if err := reg.Save(path); err != nil {
		t.Fatalf("Save seed [A]: %v", err)
	}

	h := startV2Harness(t, reg, echoConversationsHandler(t, "retained-echo-ok"),
		func(cfg *relay.V2SessionConfig) { cfg.DevicesPath = path })

	// Corrupt devices.json after startup. Reload must fail closed: the
	// retained in-memory [A] is neither lost nor widened.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt devices.json: %v", err)
	}

	// An unpaired token is still rejected with 4401 (accept set not widened).
	rejectPhone := h.dialPhone(t)
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
	sendNoiseInit(t, rejectPhone, initMsg)

	// noise_resp, then the sealed error noise_msg, then the 4401 close.
	if first := readInnerFrame(t, rejectPhone, 3*time.Second); first.Type != protocol.TypeNoiseResp {
		t.Fatalf("first inner type %q, want noise_resp", first.Type)
	}
	if second := readInnerFrame(t, rejectPhone, 3*time.Second); second.Type != protocol.TypeNoiseMsg {
		t.Fatalf("second inner type %q, want noise_msg (sealed error)", second.Type)
	}
	if _, err := rejectPhone.ReceiveBytes(3 * time.Second); err == nil {
		t.Fatal("phone receive: nil err, want WS close")
	} else if errors.Is(err, fakephone.ErrReceiveTimeout) {
		t.Fatalf("phone receive timed out without seeing close: %v", err)
	}
	if code, ok := rejectPhone.LastCloseStatus(); !ok {
		t.Fatal("phone LastCloseStatus: not set")
	} else if int(code) != 4401 {
		t.Errorf("WS close code = %d, want 4401", int(code))
	}

	// The startup-known device still authenticates against the retained set.
	acceptPhone := h.dialPhone(t)
	initSend, initRecv := driveHandshakeToOpen(t, h, acceptPhone, tokenA)
	assertOpenEcho(t, acceptPhone, initSend, initRecv, 31)
}

// testV2TamperedNoiseMsg_4421 drives a paired-device handshake to
// V2StateOpen, sends a well-formed noise_msg whose ciphertext has one
// byte flipped, and asserts the phone observes a WS close with code
// 4421 (AC #2). The fresh-conn_id reconnect case from the spec is
// deliberately deferred to the unit test
// TestV2Session_OpenState_FreshNoiseInitAfterAEADClose — fakerelay
// assigns a new conn_id per dial, so "same conn_id" reconnect is not
// expressible at the e2e layer without invasive harness changes.
func testV2TamperedNoiseMsg_4421(t *testing.T) {
	const plainToken = "v2-e2e-dispatch-token-002"
	reg := &devices.Registry{}
	reg.Add(devices.Device{
		TokenHash: devices.HashToken(plainToken),
		Name:      "v2-e2e-phone",
		PairedAt:  time.Now().UTC(),
	})

	// No handlers registered: if the handler chain were reached on a
	// tampered frame, the unsupported-type reply path would still run
	// — but the test asserts a WS close instead, so neither path fires.
	h := startV2Harness(t, reg, nil)
	phone := h.dialPhone(t)
	initSend, _ := driveHandshakeToOpen(t, h, phone, plainToken)

	envBytes, err := json.Marshal(protocol.Envelope{
		ID:      1,
		Type:    protocol.TypeListConversations,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ciphertext, err := initSend.Encrypt(envBytes)
	if err != nil {
		t.Fatalf("seal envelope: %v", err)
	}
	ciphertext[0] ^= 0xff
	sendNoiseMsg(t, phone, ciphertext)

	if _, err := phone.ReceiveBytes(3 * time.Second); err == nil {
		t.Fatal("phone receive: nil err, want WS close")
	} else if errors.Is(err, fakephone.ErrReceiveTimeout) {
		t.Fatalf("phone receive timed out without seeing close: %v", err)
	}
	code, ok := phone.LastCloseStatus()
	if !ok {
		t.Fatal("phone LastCloseStatus: not set")
	}
	if int(code) != 4421 {
		t.Errorf("WS close code = %d, want 4421", int(code))
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
