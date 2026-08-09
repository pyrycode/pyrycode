//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// The two tests below are ONE proof in two halves, and neither half is worth much
// alone.
//
// claude emits its rate_limit_event line once per run whatever the state of the
// usage-limit window — the captured run reported status "allowed", no limit in
// force, and the line fired anyway. A 1:1 mapping would therefore put a "you are
// rate limited" row on every healthy turn. The daemon gates it upstream instead:
// the one measured-benign status is silent, any other non-empty status emits. That
// gate has unit coverage at the parser; what it lacked until #1411 is proof the
// SILENCE survives the whole path — parser → turnbridge.MapEvent → the interactive
// v2 emitter → an encrypted frame on a connected client.
//
// THE ZERO-ASSERTION PROBLEM. The nearest template
// (relay_v2_stream_unrecognized_test.go) asserts a POSITIVE, so arrival proves
// itself: fed lines that never reached the parser make the count wrong and the test
// red. Test 1 here inverts that to zero, and a zero does not prove its own input
// arrived. The reply's own milestones do not restore the proof either — an assistant
// delta and a turn end show THE REPLY flowed, not that THE FED LINE reached the
// gate. Every one of these bugs would leave test 1 green on its own:
//
//   - the rider's env var is misspelled at the test end, so the rider never fires;
//   - the rider writes the line but misspells "type", so the parser routes it to the
//     UNRECOGNIZED lane instead of the rate-limit gate — zero usage-limit frames,
//     and the test is not looking at the lane that did light up;
//   - the line is written but dropped before the parser ever sees it.
//
// Test 2 is what kills all three. It is the same rider on the same path through the
// same helper, differing in exactly one byte-string — the status the rider writes —
// and it demands exactly one frame. With it, test 1's zero means "the line arrived
// and the gate chose silence". Without it, zero means "nothing was observed", which
// is not the claim these tests exist to make. The second bug above is diagnosed
// rather than merely caught, because both tests also assert the unrecognized count
// is zero.
//
// Test 2 is not only a control: it is the first end-to-end proof of the EMIT
// direction. #1410 proved it at the mapper and the handler; nothing before this
// drove it through a real daemon to a phone.
const (
	// The rate-limit rider's env knob and the two statuses it is driven with. The
	// value IS the fed rate_limit_info.status — see fakeclaude's writeRateLimitEvent.
	rateLimitRiderEnv = "PYRY_FAKE_CLAUDE_STREAM_RATE_LIMIT"

	// The measured-benign value, transcribed from the capture
	// (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json), NOT derived
	// from streamsup.benignRateLimitStatus — that constant is unexported and must
	// stay uncoupled from this fixture. If it is ever renamed, test 1 SHOULD go red:
	// that is the alarm, not a maintenance burden.
	rateLimitBenignStatus = "allowed"

	// The non-benign control. Non-empty (so the gate's absent-container rung is not
	// taken), not the benign value (so the emit rung is), distinctive enough that the
	// assertion cannot pass on another frame's content, and obviously synthetic so no
	// reader mistakes it for a measured claude value. 15 bytes — far under the
	// producer's 256-byte per-field cap, so nothing here touches truncation.
	rateLimitControlStatus = "e2e-not-allowed"

	// The captured rate_limit_info values the rider writes verbatim. Duplicated as
	// literals because the fake is a separate main package (same discipline as the
	// bogus needles in relay_v2_stream_unrecognized_test.go).
	rateLimitFedLimitType = "five_hour"
	rateLimitFedResetsAt  = int64(1785699000)

	rateLimitInitialUUID = "11111111-1111-4111-8111-111111111111"
	rateLimitConvID      = "33333333-3333-4333-8333-333333333333"
	rateLimitUserText    = "e2e-ratelimit:hello\n"
	rateLimitEchoNeedle  = "e2e-ratelimit:hello"
	rateLimitSendReqID   = uint64(2101)
)

// rateLimitObservation is what one driven turn put on the wire: every usage-limit
// frame, a count of the unrecognized lane, and the two positive milestones. The
// milestones are VALUES rather than helper-side assertions on purpose — AC-1 requires
// the silence to be asserted in a run whose milestones the same test asserts, and a
// helper that fataled on them would leave the tests' milestone checks dead code.
type rateLimitObservation struct {
	rateLimited  []protocol.RateLimitedPayload
	unrecognized int
	sawEcho      bool
	sawTurnEnd   bool
}

// driveRateLimitTurn spawns a stream-interactive daemon whose fakeclaude runs the
// rate-limit rider at riderStatus, drives one ordinary turn from a connected
// interactive v2 client, and returns what that turn put on the wire.
//
// It asserts none of the acceptance criteria. It t.Fatalf's only on transport and
// decode faults — an error envelope, a payload that will not decode, a receive error
// that is not the deadline. On the deadline it returns with sawTurnEnd false and logs
// the counts, leaving the diagnosis to the caller's milestone assertions.
func driveRateLimitTurn(t *testing.T, riderStatus string) rateLimitObservation {
	t.Helper()

	home := shortHome(t)

	rA := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if rA.ExitCode != 0 {
		t.Fatalf("pyry pair phone-a exit=%d\nstdout:\n%s\nstderr:\n%s", rA.ExitCode, rA.Stdout, rA.Stderr)
	}
	payloadA := decodePairPayload(t, rA.Stdout)
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Bind the conversation to the bootstrap session so the drain gate passes. This
	// UUID MUST equal the one handed to StartStreamInteractiveWithRelay below: a
	// mismatch between the two seeds drops every event at the gate and hangs the
	// drain for the full deadline, which presents as an unexplained timeout.
	seedBoundConversation(t, home, rateLimitConvID, rateLimitInitialUUID)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// The rate-limit rider is the ONLY env that distinguishes this from the plain
	// send spec, and its VALUE is the only thing distinguishing the two tests from
	// each other. Unset, fakeclaude is byte-identical to its prior behaviour.
	h := StartStreamInteractiveWithRelay(t, home, rateLimitInitialUUID, fr.URL()+"/v2/server",
		rateLimitRiderEnv+"="+riderStatus)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phoneA, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payloadA.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone A dial: %v", err)
	}
	t.Cleanup(func() { _ = phoneA.Close() })
	// Interactive — and load-bearing, not incidental. This stream is capability-gated:
	// frames reach only a phone whose interactive capability was echoed in hello_ack
	// (docs/protocol-mobile.md § Interactive events (v2, capability-gated)). A
	// non-interactive handshake would yield zero rate_limited frames in BOTH tests,
	// making test 1 vacuously green — test 2 is what catches it.
	sendA, recvA := driveHandshakeToOpenDaemonInteractive(t, phoneA, pubKey, payloadA.Token)

	sealSend := func(env protocol.Envelope) {
		t.Helper()
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		ciphertext, err := sendA.Encrypt(raw)
		if err != nil {
			t.Fatalf("seal envelope: %v", err)
		}
		sendNoiseMsg(t, phoneA, ciphertext)
	}

	nextEnv := func(deadline time.Time) (protocol.Envelope, bool) {
		t.Helper()
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return protocol.Envelope{}, false
			}
			raw, err := phoneA.ReceiveBytes(remaining)
			if err != nil {
				if errors.Is(err, fakephone.ErrReceiveTimeout) {
					return protocol.Envelope{}, false
				}
				t.Fatalf("phone A receive: %v", err)
			}
			var inner protocol.InnerFrameV2
			if err := json.Unmarshal(raw, &inner); err != nil {
				t.Fatalf("phone A decode inner frame: %v", err)
			}
			// The receive nonce is sequential, so every noise_msg MUST be decrypted in
			// receive order — filter AFTER decrypting, never before, or the CipherState
			// desyncs and every later decrypt fails with a misleading error. A
			// non-noise_msg control frame does not advance the nonce and is skipped.
			if inner.Type != protocol.TypeNoiseMsg {
				continue
			}
			return decryptInnerEnvelope(t, inner, recvA), true
		}
	}

	sealSend(protocol.Envelope{
		ID:   rateLimitSendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: rateLimitConvID,
			MessageID:      "m-ratelimit-1",
			Text:           rateLimitUserText,
		}),
	})

	// Collect until the turn ends. turn_end is the terminator rather than a frame
	// count, so an extra interleaved broadcast cannot make this flaky. The rider
	// writes its line BEFORE the reply, so by the time turn_end lands the fed line
	// has necessarily been through the parser — no ordering race to tune.
	var obs rateLimitObservation
	deadline := time.Now().Add(30 * time.Second)
	for !obs.sawTurnEnd {
		env, ok := nextEnv(deadline)
		if !ok {
			t.Logf("drain deadline reached with rider=%q: rate_limited=%d unrecognized=%d echo=%v turn_end=%v",
				riderStatus, len(obs.rateLimited), obs.unrecognized, obs.sawEcho, obs.sawTurnEnd)
			break
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope: %s", string(env.Payload))
		case protocol.TypeRateLimited:
			var p protocol.RateLimitedPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode rate_limited payload: %v", err)
			}
			obs.rateLimited = append(obs.rateLimited, p)
		case protocol.TypeUnrecognizedMessage:
			obs.unrecognized++
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if strings.Contains(p.Text, rateLimitEchoNeedle) {
				obs.sawEcho = true
			}
		case protocol.TypeTurnEnd:
			obs.sawTurnEnd = true
		}
	}
	return obs
}

// TestRelayV2_StreamRateLimitBenignReachesNoPhone is the silence half: the captured
// status "allowed" line is fed through the daemon ahead of an ordinary reply, and the
// connected interactive v2 client sees ZERO usage-limit frames.
//
// The milestones are asserted FIRST and fatally. A zero read off a run where nothing
// completed says nothing at all, so the order is prescribed rather than incidental.
// See the file's doc comment for why the milestones alone are still not enough, and
// what TestRelayV2_StreamRateLimitNonBenignReachesPhone adds.
func TestRelayV2_StreamRateLimitBenignReachesNoPhone(t *testing.T) {
	obs := driveRateLimitTurn(t, rateLimitBenignStatus)

	if !obs.sawEcho {
		t.Fatalf("the reply never arrived (no assistant_delta carrying %q) — the run did not complete, "+
			"so its silence proves nothing; rate_limited=%d unrecognized=%d turn_end=%v "+
			"(a UUID mismatch between seedBootstrapRegistry and seedBoundConversation is the first suspect)",
			rateLimitEchoNeedle, len(obs.rateLimited), obs.unrecognized, obs.sawTurnEnd)
	}
	if !obs.sawTurnEnd {
		t.Fatalf("the reply arrived but the turn never closed (no turn_end) — the run did not complete, "+
			"so its silence proves nothing; rate_limited=%d unrecognized=%d",
			len(obs.rateLimited), obs.unrecognized)
	}

	if len(obs.rateLimited) != 0 {
		t.Errorf("usage-limit frames on a healthy run: got %d, want 0 — the fed status is the "+
			"measured-benign value, so the gate must answer it with silence all the way to the phone\n%+v",
			len(obs.rateLimited), obs.rateLimited)
	}
	// The fed line must not land in the unrecognized lane. This pins end-to-end the
	// structural guarantee emitRateLimit's doc claims — the type is claimed by a case
	// arm, so a malformed payload of it is dropped rather than surfaced — and it turns
	// "the test is not looking at the lane that did light up" from an inference into
	// an immediate diagnosis.
	if obs.unrecognized != 0 {
		t.Errorf("unrecognized_message frames: got %d, want 0 — the fed rate_limit_event reached the "+
			"unrecognized lane instead of the gate, so the zero above is not the gate's silence",
			obs.unrecognized)
	}
}

// TestRelayV2_StreamRateLimitNonBenignReachesPhone is the arrival control, and the
// first end-to-end proof of the emit direction. The same rider on the same path,
// driven with a non-benign status, must put EXACTLY ONE usage-limit frame on the same
// client.
//
// It fails if the rider never fires, if the fed line is malformed enough to miss the
// gate, or if the line never reaches the parser — the three bugs that would otherwise
// leave the silence test green for the wrong reason.
func TestRelayV2_StreamRateLimitNonBenignReachesPhone(t *testing.T) {
	obs := driveRateLimitTurn(t, rateLimitControlStatus)

	if !obs.sawEcho {
		t.Fatalf("the reply never arrived (no assistant_delta carrying %q); rate_limited=%d unrecognized=%d "+
			"turn_end=%v (a UUID mismatch between seedBootstrapRegistry and seedBoundConversation is the "+
			"first suspect)",
			rateLimitEchoNeedle, len(obs.rateLimited), obs.unrecognized, obs.sawTurnEnd)
	}
	if !obs.sawTurnEnd {
		t.Fatalf("the reply arrived but the turn never closed (no turn_end); rate_limited=%d unrecognized=%d",
			len(obs.rateLimited), obs.unrecognized)
	}

	if obs.unrecognized != 0 {
		t.Errorf("unrecognized_message frames: got %d, want 0 — the fed rate_limit_event reached the "+
			"unrecognized lane instead of the gate", obs.unrecognized)
	}
	if len(obs.rateLimited) != 1 {
		t.Fatalf("usage-limit frames: got %d, want exactly 1 — the rider fed one non-benign "+
			"rate_limit_event, so the gate must emit once and the frame must reach the phone\n%+v",
			len(obs.rateLimited), obs.rateLimited)
	}

	p := obs.rateLimited[0]
	// The conversation identity is the BRIDGE's contribution: the parser's event
	// carries none, so this proves the mapper supplied it rather than leaving it empty.
	if p.ConversationID != rateLimitConvID {
		t.Errorf("conversation_id: got %q, want %q", p.ConversationID, rateLimitConvID)
	}
	// Verbatim, across four layers. Status is the field that says WHY the frame fired.
	if p.Status != rateLimitControlStatus {
		t.Errorf("status: got %q, want %q (the fed value, verbatim)", p.Status, rateLimitControlStatus)
	}
	if p.LimitType != rateLimitFedLimitType {
		t.Errorf("limit_type: got %q, want %q (claude's rateLimitType, under the daemon's own wire name)",
			p.LimitType, rateLimitFedLimitType)
	}
	if p.ResetsAt != rateLimitFedResetsAt {
		t.Errorf("resets_at: got %d, want %d", p.ResetsAt, rateLimitFedResetsAt)
	}
	// A live assertion, not decoration: null decodes to nil and [] decodes to an empty
	// non-nil slice, so this distinguishes them. RateLimitedPayload deliberately has no
	// nil→[] MarshalJSON precisely so nothing-was-cut stays an ABSENCE on the wire
	// rather than becoming a positive "these fields were cut: none". Unit tests pin
	// that at the byte level; this is its first proof through a daemon to a phone.
	if p.TruncatedFields != nil {
		t.Errorf("truncated_fields: got %#v, want nil — nothing was truncated (the fed status is %d bytes, "+
			"an order of magnitude under the producer's cap), so the wire value must be null, not []",
			p.TruncatedFields, len(rateLimitControlStatus))
	}
}
