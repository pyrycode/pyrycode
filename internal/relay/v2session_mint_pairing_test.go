package relay

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #2127 inbound pairing-mint fixtures ---

// Sentinel values chosen so a substring scan of the log buffer or of a sealed
// frame cannot match them incidentally. The split is the one this verb's
// disclosure rules draw: the credential is emittable in EXACTLY ONE field of
// EXACTLY ONE frame and loggable nowhere, and the client's requested label is
// echoable and loggable nowhere at all.
const (
	mpTestCredential = "ZZ2127CREDENTIALZZ" // the seam's answer; one egress, never logged
	mpTestLabelMark  = "ZZ2127LABELZZ"      // a client-authored label; never echoed, never logged
	mpTestEnvID      = uint64(21270)
)

// mintCall captures one MintPairing call: which device the handler said was
// asking, and what label reached the seam.
type mintCall struct {
	requester  *devices.Device
	deviceName string
}

// fakePairingMinter is a relay-side test double for the PairingMinter seam. It
// answers a scripted outcome and records every call, so "the seam was never
// reached" is assertable as strongly as "it answered X" — which is what the
// gate-ordering claims here rest on.
type fakePairingMinter struct {
	mu sync.Mutex

	result PairingMintResult
	calls  []mintCall
}

func (f *fakePairingMinter) MintPairing(requester *devices.Device, deviceName string) PairingMintResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, mintCall{requester: requester, deviceName: deviceName})
	return f.result
}

func (f *fakePairingMinter) callSnapshot() []mintCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mintCall(nil), f.calls...)
}

// startMintConn stands up a manager with the mint seam wired (nil for the
// unwired posture), drives one paired handshake and returns the open session plus
// its log buffer. Every test here drives a real handshake and a real AEAD-sealed
// frame, so the interception is proven through dispatchAppFrame and the conn's
// appFrameWorker rather than by calling the handler directly.
func startMintConn(t *testing.T, minter PairingMinter) (*openSession, *syncLogBuffer) {
	t.Helper()
	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	logger, logBuf := bufferLogger()
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:      frames,
		Outbound:    rec.outbound,
		StaticPriv:  respPriv,
		Devices:     reg,
		ServerID:    v2TestServerID,
		Logger:      logger,
		PairingMint: minter,
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)
	return sess, logBuf
}

// sendMintPairing seals one mint_pairing envelope under the initiator's send
// state and hands it to the manager. The envelope ID IS load-bearing: both the
// reply and every reject correlate on it through in_reply_to, and the payload
// carries no request-id key.
func sendMintPairing(t *testing.T, sess *openSession, envID uint64, payload string) {
	t.Helper()
	sess.frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:      envID,
		Type:    protocol.TypeMintPairing,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(payload),
	})
}

// mintPayload builds a well-formed mint_pairing body carrying one label.
func mintPayload(deviceName string) string {
	p, err := json.Marshal(protocol.MintPairingPayload{DeviceName: deviceName})
	if err != nil {
		panic(err) // one string; cannot fail
	}
	return string(p)
}

// TestMintPairing_PermittedMintIsAnswered is the happy path: the seam's answer
// reaches the client verbatim, in the one field it belongs in, correlated to the
// request.
//
// IT ALSO PINS WHO THE REQUESTER IS. The handler must hand the seam the conn's
// AUTHENTICATED device — the record handleNoiseInit bound after the presented
// token validated — and nothing derived from the payload, which has no identity
// field at all. Asserting that here is what makes the seam's "requester is the
// only identity there is" contract checked rather than reviewed.
func TestMintPairing_PermittedMintIsAnswered(t *testing.T) {
	t.Parallel()

	minter := &fakePairingMinter{result: PairingMintResult{
		Outcome: PairingMintOK,
		Pairing: mpTestCredential,
	}}
	sess, logBuf := startMintConn(t, minter)

	sendMintPairing(t, sess, mpTestEnvID, mintPayload("Kitchen iPad"))
	reply := nextHistoryReply(t, sess, 0)

	if reply.Type != protocol.TypePairingMinted {
		t.Fatalf("reply type = %q, want %q", reply.Type, protocol.TypePairingMinted)
	}
	if reply.InReplyTo == nil || *reply.InReplyTo != mpTestEnvID {
		t.Errorf("in_reply_to = %v, want %d", reply.InReplyTo, mpTestEnvID)
	}
	var got protocol.PairingMintedPayload
	if err := json.Unmarshal(reply.Payload, &got); err != nil {
		t.Fatalf("decode pairing_minted payload: %v", err)
	}
	if got.Pairing != mpTestCredential {
		t.Errorf("pairing = %q, want the seam's answer %q", got.Pairing, mpTestCredential)
	}

	calls := minter.callSnapshot()
	if len(calls) != 1 {
		t.Fatalf("seam called %d times, want exactly 1", len(calls))
	}
	if calls[0].deviceName != "Kitchen iPad" {
		t.Errorf("label reaching the seam = %q, want the requested one", calls[0].deviceName)
	}
	if calls[0].requester == nil {
		t.Fatal("requester was nil; the handler must pass the conn's authenticated device")
	}
	if calls[0].requester.Name != v2TestDevName {
		t.Errorf("requester = %q, want the handshake-bound device %q", calls[0].requester.Name, v2TestDevName)
	}
	if calls[0].requester.TokenHash != devices.HashToken(v2TestToken) {
		t.Error("requester is not the device whose token opened this conn")
	}

	// The credential has EXACTLY ONE egress. It may appear in the pairing field
	// decoded above and nowhere else — not in any other frame, and not in one
	// character of the daemon's log.
	if strings.Contains(logBuf.String(), mpTestCredential) {
		t.Error("the minted pairing reached the log; it is a plaintext bearer credential")
	}
}

// TestMintPairing_UnwiredSeamIsInertButConsuming pins the nil-seam posture: the
// frame is CONSUMED by the interception — so it no longer draws dispatch.Route's
// unknown-type reply — and nothing at all is parsed or answered.
//
// The second frame is what makes "no reply" checkable rather than a race: a
// request_snapshot behind it takes a Run-inline arm and must be answered, so the
// conn having sealed exactly one frame proves the mint produced none.
func TestMintPairing_UnwiredSeamIsInertButConsuming(t *testing.T) {
	t.Parallel()

	sess, _ := startMintConn(t, nil)

	sendMintPairing(t, sess, mpTestEnvID, mintPayload(mpTestLabelMark))
	sess.frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:   mpTestEnvID + 1,
		Type: protocol.TypeRequestSnapshot,
		TS:   time.Now().UTC(),
	})

	// CORRELATION IS THE OBSERVABLE, not the reply's type: the daemon renders no
	// screen and the snapshot itself answers a coded error, so "the first sealed
	// frame is an error" proves nothing. What proves inertness is that the first frame
	// off this conn answers the SNAPSHOT's envelope id rather than the mint's.
	reply := nextHistoryReply(t, sess, 0)
	if reply.Type == protocol.TypePairingMinted {
		t.Fatal("an unwired daemon minted a pairing; the frame must be inert")
	}
	if reply.InReplyTo == nil || *reply.InReplyTo != mpTestEnvID+1 {
		t.Errorf("first sealed frame correlates to %v, want the snapshot's %d — the mint answered something",
			reply.InReplyTo, mpTestEnvID+1)
	}
}

// TestMintPairing_Rejects walks every refusal arm: the two the handler decides
// itself (an undecodable payload and a label that is not a safe display string)
// and the two the seam decides. Table-driven because the arms differ only in what
// goes in and which code comes out.
//
// THE RETRYABLE FLAG IS ASSERTED ON EVERY ARM, not just the retryable one: it is
// the field a client branches on, and pairing them wrong is the failure this
// table exists to catch.
func TestMintPairing_Rejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		payload   string
		outcome   PairingMintOutcome
		wantCode  string
		wantRetry bool
		wantSeam  bool // whether the seam should have been reached at all
	}{
		{
			name:     "payload is not an object",
			payload:  `"not-an-object-` + mpTestLabelMark + `"`,
			wantCode: protocol.CodeProtocolMalformed,
		},
		{
			name:     "device_name over the byte bound",
			payload:  mintPayload(mpTestLabelMark + strings.Repeat("x", protocol.MaxDeviceNameBytes)),
			wantCode: protocol.CodeProtocolMalformed,
		},
		{
			name:     "device_name carries a newline",
			payload:  mintPayload("kitchen\n" + mpTestLabelMark),
			wantCode: protocol.CodeProtocolMalformed,
		},
		{
			name:     "device_name carries a carriage return",
			payload:  mintPayload("kitchen\r" + mpTestLabelMark),
			wantCode: protocol.CodeProtocolMalformed,
		},
		{
			// BARE ESC, NOT A FULL CSI SEQUENCE, and deliberately: substrate-guard
			// bans the CSI introducer — ESC followed by an open bracket — in
			// pyrycode source outside its allowlist, in a comment as well as in a
			// literal, so spelling a colour run here would redden the build (it
			// did). Nothing is lost — mintLabelIsDisplaySafe refuses at the first
			// offending rune, and ESC is the byte every ANSI escape run begins
			// with.
			name:     "device_name carries ESC, an ANSI escape run's first byte",
			payload:  mintPayload("kitchen\x1b" + mpTestLabelMark),
			wantCode: protocol.CodeProtocolMalformed,
		},
		{
			name:     "device_name carries DEL",
			payload:  mintPayload("kitchen\x7f" + mpTestLabelMark),
			wantCode: protocol.CodeProtocolMalformed,
		},
		{
			// U+0085 as an ESCAPE, never the raw two bytes: staticcheck ST1018
			// fails the build on a control character spelled literally in source,
			// which is the whole table's convention above.
			name:     "device_name carries a C1 control",
			payload:  mintPayload("kitchen\u0085" + mpTestLabelMark),
			wantCode: protocol.CodeProtocolMalformed,
		},
		{
			name:      "requester is not privileged",
			payload:   mintPayload("Kitchen iPad"),
			outcome:   PairingMintUnauthorized,
			wantCode:  protocol.CodePairingNotPermitted,
			wantRetry: false,
			wantSeam:  true,
		},
		{
			name:      "the host could not complete the mint",
			payload:   mintPayload("Kitchen iPad"),
			outcome:   PairingMintFailed,
			wantCode:  protocol.CodePairingUnavailable,
			wantRetry: true,
			wantSeam:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			minter := &fakePairingMinter{result: PairingMintResult{
				Outcome: tc.outcome,
				// A credential on a refusing outcome, so "the handler read
				// Pairing without checking Outcome" is a detectable bug rather
				// than an invisible one.
				Pairing: mpTestCredential,
			}}
			sess, logBuf := startMintConn(t, minter)

			sendMintPairing(t, sess, mpTestEnvID, tc.payload)
			reply := nextHistoryReply(t, sess, 0)

			if reply.Type != protocol.TypeError {
				t.Fatalf("reply type = %q, want %q", reply.Type, protocol.TypeError)
			}
			if reply.InReplyTo == nil || *reply.InReplyTo != mpTestEnvID {
				t.Errorf("in_reply_to = %v, want %d", reply.InReplyTo, mpTestEnvID)
			}
			var ep protocol.ErrorPayload
			if err := json.Unmarshal(reply.Payload, &ep); err != nil {
				t.Fatalf("decode error payload: %v", err)
			}
			if ep.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", ep.Code, tc.wantCode)
			}
			if ep.Retryable != tc.wantRetry {
				t.Errorf("retryable = %v, want %v", ep.Retryable, tc.wantRetry)
			}

			gotSeam := len(minter.callSnapshot()) > 0
			if gotSeam != tc.wantSeam {
				t.Errorf("seam reached = %v, want %v — a gate the handler owns must fire before the seam",
					gotSeam, tc.wantSeam)
			}

			// Nothing the client sent is echoed, and nothing the seam returned on
			// a refusing outcome escapes: the label marker and the credential are
			// absent from the reply and from every log record.
			frame, err := json.Marshal(reply)
			if err != nil {
				t.Fatalf("re-marshal reply: %v", err)
			}
			for _, secret := range []string{mpTestLabelMark, mpTestCredential} {
				if strings.Contains(string(frame), secret) {
					t.Errorf("the reject echoed %q back to the client", secret)
				}
				if strings.Contains(logBuf.String(), secret) {
					t.Errorf("the reject path logged %q", secret)
				}
			}
		})
	}
}

// TestMintPairing_EmptyLabelReachesTheSeam pins the one label the gate must NOT
// refuse. An absent or empty device_name is "the client named no device", which
// the seam answers with `pyry pair`'s own fallback — so refusing it here would
// break the frame's only optional field.
func TestMintPairing_EmptyLabelReachesTheSeam(t *testing.T) {
	t.Parallel()

	minter := &fakePairingMinter{result: PairingMintResult{
		Outcome: PairingMintOK,
		Pairing: mpTestCredential,
	}}
	sess, _ := startMintConn(t, minter)

	// The absent-key form, not the explicit empty string: both decode to the same
	// zero value, and this is the one a client that never sets the field sends.
	sendMintPairing(t, sess, mpTestEnvID, `{}`)
	reply := nextHistoryReply(t, sess, 0)

	if reply.Type != protocol.TypePairingMinted {
		t.Fatalf("reply type = %q, want %q — an unnamed device is not a refusal", reply.Type, protocol.TypePairingMinted)
	}
	calls := minter.callSnapshot()
	if len(calls) != 1 {
		t.Fatalf("seam called %d times, want exactly 1", len(calls))
	}
	if calls[0].deviceName != "" {
		t.Errorf("label reaching the seam = %q, want the empty string the fallback keys on", calls[0].deviceName)
	}
}
