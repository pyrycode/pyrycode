//go:build e2e

package e2e

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// TestRelayV2_Rekey is the WIRE coverage for the scheduled Noise_IK re-key
// (#920). Re-key was covered only in-process (internal/relay/v2session_test.go,
// at the V2SessionManager boundary); this suite drives it end-to-end over the
// fakerelay + fakephone harness so a rotation regression is caught at the wire,
// not in the field. The scheduled hourly re-key (ADR 024 § "1-hour re-key
// cadence") is the one v2 mechanism that fires on a timer against a live
// session — the rotation canary — so it is exactly the path that regresses
// silently without a wire test.
//
// The three new V2SessionConfig fields (RekeyInterval / RekeyReplyTimeout /
// RekeyRetryInterval) are the test-only seam that makes the timer-driven path
// triggerable from package e2e, which cannot mutate internal/relay's
// unexported timing vars the way the in-package tests do. Production leaves
// them zero (the package defaults), so behaviour is byte-identical.
//
// NON-VACUITY (the load-bearing property). A re-key test that stays green when
// no rotation happened would let a rotation regression ship silently. Every
// subtest pins rotation with a round-trip UNDER THE NEW CipherStates: sealing a
// request with the rotated send key and decrypting the reply with the rotated
// recv key. A responder that failed to swap CipherStates would reject the
// new-key frame at 4421, so a clean round-trip is proof the old keys were
// retired. This is the wire port of v2session_test.go's
// RoundTripUnderNewKeys assertion.
//
// AC coverage:
//   - scheduled_happy_path_and_rearm — AC1 (the timer emits rekey_request
//     {scheduled}, the phone re-handshakes, traffic continues under new keys)
//     + AC2 (a SECOND rekey_request fires at the shrunk interval, proving the
//     timer re-armed at rekeyComplete rather than firing once).
//   - phone_initiated_spontaneous — AC3 (a cold noise_init in the open state
//     with no preceding rekey_request is accepted and rotates keys).
//   - transport_down_at_boundary — AC4, POST-#912 shape. #912 gates the
//     scheduled emit behind transportDown(): while the relay leg is down the
//     daemon seals nothing, arms no reply window, and re-arms a short retry, so
//     the emit self-heals once the transport recovers. The spec was written
//     pre-#912 (it asserted the old 4426/rekey_failed teardown); #912 has since
//     landed on main, so this test asserts the survive-and-retry behaviour.
func TestRelayV2_Rekey(t *testing.T) {
	t.Run("scheduled_happy_path_and_rearm", testRekeyScheduledAndRearm)
	t.Run("phone_initiated_spontaneous", testRekeyPhoneInitiatedSpontaneous)
	t.Run("transport_down_at_boundary", testRekeyTransportDownAtBoundary)
}

// pairedRekeyRegistry returns a registry with a single paired device whose
// token is the given plaintext.
func pairedRekeyRegistry(t *testing.T, plainToken string) *devices.Registry {
	t.Helper()
	reg := &devices.Registry{}
	reg.Add(devices.Device{
		TokenHash: devices.HashToken(plainToken),
		Name:      "v2-e2e-phone",
		PairedAt:  time.Now().UTC(),
	})
	return reg
}

// rekeyTiming is a harness opt that shrinks the three re-key timers so the
// timer-driven path fires within a test's wall-clock instead of at the 1h / 30s
// / 1m production defaults.
func rekeyTiming(interval, reply, retry time.Duration) func(*relay.V2SessionConfig) {
	return func(cfg *relay.V2SessionConfig) {
		cfg.RekeyInterval = interval
		cfg.RekeyReplyTimeout = reply
		cfg.RekeyRetryInterval = retry
	}
}

// capturingLogger is a harness opt that routes the manager's slog to buf so a
// test can assert on emitted events (AC4). safeBuffer is the mutex-guarded
// buffer shared with auto_attach.go; slog.TextHandler is not concurrency-safe
// over a bare bytes.Buffer under -race.
func capturingLogger(buf *safeBuffer) func(*relay.V2SessionConfig) {
	return func(cfg *relay.V2SessionConfig) {
		cfg.Logger = slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
}

// downShim is a harness opt that makes the manager see the binary↔relay leg as
// down whenever down.Load() is true. The manager reads the leg's liveness only
// through cfg.Connected() (transportDown = Connected != nil && !Connected()),
// so flipping this atomic reproduces "leg down" at exactly the boundary the
// #912 gate consults — deterministically, without the transport reconnect
// backoff race that a real ForceClose would reintroduce.
func downShim(down *atomic.Bool) func(*relay.V2SessionConfig) {
	return func(cfg *relay.V2SessionConfig) {
		cfg.Connected = func() bool { return !down.Load() }
	}
}

// openRekeySession drives a fresh paired Noise_IK handshake to V2StateOpen and
// returns the initiator's static private key — needed to re-handshake under the
// peer-static continuity invariant — plus the open-state CipherStates
// (send encrypts phone→binary, recv decrypts binary→phone).
func openRekeySession(t *testing.T, h *v2Harness, phone *fakephone.Client, token string) (initPriv []byte, send, recv *noise.CipherState) {
	t.Helper()
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("phone keygen: %v", err)
	}
	initPriv = priv.Bytes()
	initiator, err := noise.NewInitiator(initPriv, h.pubKey)
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
	_, send, recv, err = initiator.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("initiator.ReadResp: %v", err)
	}
	return initPriv, send, recv
}

// rekeyHandshake performs a re-key from the phone side: a fresh Noise_IK init
// reusing the SAME static private key (peer-static continuity) with empty
// early-data (ADR 024 § Re-key), reads the noise_resp, and returns the rotated
// CipherStates. Asserts the re-key noise_resp carries no early-data.
func rekeyHandshake(t *testing.T, phone *fakephone.Client, pubKey, initPriv []byte) (send, recv *noise.CipherState) {
	t.Helper()
	initiator, err := noise.NewInitiator(initPriv, pubKey)
	if err != nil {
		t.Fatalf("rekey NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(nil)
	if err != nil {
		t.Fatalf("rekey WriteInit: %v", err)
	}
	sendNoiseInit(t, phone, initMsg)

	inner := readInnerFrame(t, phone, 3*time.Second)
	if inner.Type != protocol.TypeNoiseResp {
		t.Fatalf("rekey: got inner type %q, want %q (responder rejected the rotation)", inner.Type, protocol.TypeNoiseResp)
	}
	respRaw, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatalf("decode rekey noise_resp data: %v", err)
	}
	earlyAck, send, recv, err := initiator.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("rekey initiator.ReadResp: %v", err)
	}
	if len(earlyAck) != 0 {
		t.Errorf("rekey noise_resp early-data len = %d, want 0 (ADR 024 § Re-key)", len(earlyAck))
	}
	return send, recv
}

// awaitScheduledRekeyRequest reads the next inner frame, decrypts it under
// recv, and asserts it is a rekey_request with reason "scheduled". recv's
// nonce must be in sync — the caller passes the CipherState under which the
// daemon sealed this frame.
func awaitScheduledRekeyRequest(t *testing.T, phone *fakephone.Client, recv *noise.CipherState, timeout time.Duration) {
	t.Helper()
	inner := readInnerFrame(t, phone, timeout)
	if inner.Type != protocol.TypeNoiseMsg {
		t.Fatalf("rekey_request inner type = %q, want %q", inner.Type, protocol.TypeNoiseMsg)
	}
	cipher, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatalf("decode rekey_request data: %v", err)
	}
	plain, err := recv.Decrypt(cipher)
	if err != nil {
		t.Fatalf("phone decrypt rekey_request: %v", err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(plain, &env); err != nil {
		t.Fatalf("decode rekey_request envelope: %v", err)
	}
	if env.Type != protocol.TypeRekeyRequest {
		t.Fatalf("envelope Type = %q, want %q", env.Type, protocol.TypeRekeyRequest)
	}
	var p struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("decode rekey_request payload: %v", err)
	}
	if p.Reason != "scheduled" {
		t.Errorf("rekey_request reason = %q, want %q", p.Reason, "scheduled")
	}
}

// waitForLog blocks until the capturing buffer contains substr or timeout
// elapses. Used by AC4 to observe the #912 deferral event without a wire
// receive that would close the phone conn on timeout.
func waitForLog(t *testing.T, buf *safeBuffer, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), substr) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected log substring %q within %s; log=\n%s", substr, timeout, buf.String())
}

// testRekeyScheduledAndRearm covers AC1 (scheduled emit → re-handshake →
// round-trip under new keys) and AC2 (a second scheduled emit at the shrunk
// interval proves the timer re-armed).
func testRekeyScheduledAndRearm(t *testing.T) {
	const token = "v2-rekey-token-001"
	const interval = 1 * time.Second
	reg := pairedRekeyRegistry(t, token)
	h := startV2Harness(t, reg, echoConversationsHandler(t, "rekey-echo-ok"),
		rekeyTiming(interval, 500*time.Millisecond, 250*time.Millisecond))
	phone := h.dialPhone(t)
	initPriv, _, recv := openRekeySession(t, h, phone, token)

	// AC1: the scheduled timer fires ~interval after open and the daemon emits
	// a rekey_request{scheduled}, sealed under the ORIGINAL send key.
	awaitScheduledRekeyRequest(t, phone, recv, 3*time.Second)
	t.Logf("AC1: phone observed the first scheduled rekey_request")

	// Phone re-handshakes, reusing the same static (peer-static continuity).
	send2, recv2 := rekeyHandshake(t, phone, h.pubKey, initPriv)

	// AC1 non-vacuity: round-trip UNDER THE ROTATED KEYS. A responder that
	// failed to swap CipherStates would 4421 this new-key frame; a clean reply
	// proves the old keys were retired and the new ones are live.
	assertOpenEcho(t, phone, send2, recv2, 101)
	t.Logf("AC1: round-trip under rotated keys succeeded — rotation proven")

	// AC2: rekeyComplete re-armed the timer, so a SECOND rekey_request fires at
	// the shrunk interval, this one sealed under the post-rotation send key.
	awaitScheduledRekeyRequest(t, phone, recv2, 3*time.Second)
	t.Logf("AC2: second scheduled rekey_request observed — the timer re-armed")
}

// testRekeyPhoneInitiatedSpontaneous covers AC3: a cold noise_init in the open
// state, with no preceding rekey_request, is accepted and rotates keys. The
// scheduled interval is set large enough that the timer never fires during the
// subtest, so the only rotation is the phone-initiated one.
func testRekeyPhoneInitiatedSpontaneous(t *testing.T) {
	const token = "v2-rekey-token-002"
	reg := pairedRekeyRegistry(t, token)
	h := startV2Harness(t, reg, echoConversationsHandler(t, "spontaneous-echo-ok"),
		rekeyTiming(30*time.Second, 500*time.Millisecond, 250*time.Millisecond))
	phone := h.dialPhone(t)
	initPriv, send, recv := openRekeySession(t, h, phone, token)

	// Baseline: traffic flows under the ORIGINAL keys.
	assertOpenEcho(t, phone, send, recv, 201)

	// Spontaneous re-key: a cold fresh noise_init, no preceding rekey_request.
	send2, recv2 := rekeyHandshake(t, phone, h.pubKey, initPriv)

	// Non-vacuity: round-trip under the ROTATED keys.
	assertOpenEcho(t, phone, send2, recv2, 202)
	t.Logf("AC3: spontaneous phone-initiated re-key rotated keys and traffic continued")
}

// testRekeyTransportDownAtBoundary covers AC4 in its POST-#912 shape. When the
// relay leg is down at the moment the scheduled timer fires, #912's gate defers
// the emit (seals nothing, arms no reply window, re-arms a short retry) instead
// of the old nonce-burning teardown. When the leg recovers, the retry emits the
// deferred rekey_request and the session survives.
func testRekeyTransportDownAtBoundary(t *testing.T) {
	const token = "v2-rekey-token-003"
	const interval = 700 * time.Millisecond
	reg := pairedRekeyRegistry(t, token)
	var down atomic.Bool
	buf := &safeBuffer{}
	h := startV2Harness(t, reg, echoConversationsHandler(t, "down-echo-ok"),
		rekeyTiming(interval, 2*time.Second, 250*time.Millisecond),
		capturingLogger(buf),
		downShim(&down))
	phone := h.dialPhone(t)
	initPriv, _, recv := openRekeySession(t, h, phone, token)

	// Cut the binary↔relay leg just before the scheduled timer fires. The
	// sub-millisecond Store lands well inside the ~700ms margin.
	down.Store(true)

	// #912 deferral: while the leg is down the scheduled emit seals nothing,
	// arms no reply window, and re-arms a short retry — so nothing is sent and
	// the session is NOT torn down. Evidence it via the deferral log rather than
	// a wire receive: coder/websocket closes the phone conn on a read timeout
	// (fakephone.go), so a "no frame" receive would kill the conn we still need
	// for the recovery half. The deferral event proves the scheduled timer fired
	// and took the down-path (which returns before any send).
	waitForLog(t, buf, "v2.rekey.emit.deferred_transport_down", 3*time.Second)
	if strings.Contains(buf.String(), "noise.rekey_failed") {
		t.Fatalf("AC4: session torn down as rekey_failed while transport down — the pre-#912 defect; log=\n%s", buf.String())
	}
	t.Logf("AC4: scheduled emit deferred while down — no teardown")

	// Restore the leg. The bounded retry re-arm now finds the transport up and
	// emits the deferred rekey_request — the self-heal.
	down.Store(false)
	awaitScheduledRekeyRequest(t, phone, recv, 3*time.Second)

	// The phone completes the re-key; the session survives and traffic
	// continues under the rotated keys.
	send2, recv2 := rekeyHandshake(t, phone, h.pubKey, initPriv)
	assertOpenEcho(t, phone, send2, recv2, 301)
	if strings.Contains(buf.String(), "noise.rekey_failed") {
		t.Fatalf("AC4: unexpected rekey_failed after transport recovery; log=\n%s", buf.String())
	}
	t.Logf("AC4: transport recovered, deferred re-key self-healed, session survived under rotated keys")
}
