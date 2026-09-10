//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// The two tests below are ONE proof in two halves, on
// relay_v2_stream_rate_limit_test.go's terms and for its reason.
//
// internal/turnbridge's MapEvent gained its turnevent.SessionFacts arm in #2254 and
// cmd/pyry's interactive v2 emitter pushes the frame, so session_facts flows on the
// live turn lane today with nothing proving it ARRIVES. Every layer had its own
// coverage — the parser's construction of the variant, the mapper's arm, the
// emitter's push — and no test drove one claude-authored line through all of them to
// an encrypted frame on a connected client.
//
// THE VACUITY PROBLEM, which is why there are two tests. A count of one proves
// arrival only if a count of zero is reachable, and every one of these bugs would
// leave a lone positive test green for the wrong reason only if the count were read
// off a run that never completed — so the milestones are asserted FIRST and fatally
// in both. The rider-off half is what makes the positive count mean something
// narrower and truer: the frame arrived BECAUSE the rider fed a line, not because
// something else on the path emits one. It also fails loudly if a future change
// starts emitting session_facts from a second source.
//
// ONE INIT LINE, TWO FRAMES, and that is expected rather than a defect. streamsup's
// emitInitLine decodes the line once and calls both emitModelAnnounced and
// emitSessionFacts, so the capture-faithful line the rider feeds — which carries
// `model` — also produces a model_announced frame on the same run. The drain
// therefore filters BY FRAME TYPE and never asserts a frame total. model_announced
// keeps its own proof at the real-claude tier; these tests do not claim it.
const (
	// The init rider's env knob. Unset or empty ⟹ off ⟹ fakeclaude's stream is
	// byte-identical to its prior behaviour, which is what makes the negative half a
	// controlled comparison rather than a different experiment.
	sessionFactsRiderEnv = "PYRY_FAKE_CLAUDE_STREAM_SESSION_FACTS"

	// Any non-empty value turns the rider on; the value itself is not read. Spelled
	// as a word rather than "1" so a reader of a failing log sees which knob fired.
	sessionFactsRiderOn = "on"

	// The two values the fed line carries, transcribed from the committed capture
	// (internal/e2e/realclaude/testdata/effort_init_v2.1.259_sonnet_effort.json,
	// claude 2.1.259 — all three of its init lines agree). Duplicated as literals
	// because fakeclaude is a separate main package this file cannot import, the
	// discipline the rate-limit and unrecognized specs already follow.
	//
	// Both are far under the producer's per-field caps (streamsup's
	// maxClaudeVersionField and maxPermissionModeField, 256 each), so nothing on this
	// path truncates and truncated_fields must cross as null.
	sessionFactsFedVersion = "2.1.259"
	sessionFactsFedMode    = "default"

	sessionFactsInitialUUID = "55555555-5555-4555-8555-555555555555"
	sessionFactsConvID      = "66666666-6666-4666-8666-666666666666"
	sessionFactsUserText    = "e2e-sessionfacts:hello\n"
	sessionFactsEchoNeedle  = "e2e-sessionfacts:hello"
	sessionFactsSendReqID   = uint64(2315)
)

// sessionFactsObservation is what one driven turn put on the wire: every session_facts
// frame as RAW payload bytes, a count of the unrecognized lane, and the two positive
// milestones.
//
// The frames are kept RAW rather than decoded here because one assertion needs the
// payload's own KEY SET, not its decoded fields — see the arrival test. A
// protocol.SessionFactsPayload cannot answer "did a twenty-second key arrive": decoding
// into it discards anything it does not declare, which is precisely the thing under
// test.
//
// The milestones are VALUES rather than helper-side assertions for the rate-limit
// helper's stated reason: the rider-off half requires the silence to be asserted in a
// run whose milestones the same test asserts, and a helper that fataled on them would
// leave both tests' milestone checks dead code.
type sessionFactsObservation struct {
	sessionFacts []json.RawMessage
	unrecognized int
	sawEcho      bool
	sawTurnEnd   bool
}

// driveSessionFactsTurn spawns a stream-interactive daemon whose fakeclaude runs the
// init rider at riderValue, drives one ordinary turn from a connected interactive v2
// client, and returns what that turn put on the wire.
//
// It asserts none of the acceptance criteria. It t.Fatalf's only on transport and
// decode faults — an error envelope, a payload that will not decode, a receive error
// that is not the deadline. On the deadline it returns with sawTurnEnd false and logs
// the counts, leaving the diagnosis to the caller's milestone assertions.
func driveSessionFactsTurn(t *testing.T, riderValue string) sessionFactsObservation {
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
	// mismatch between the two seeds drops every event at the gate and hangs the drain
	// for the full deadline, which presents as an unexplained timeout rather than a
	// clean failure here.
	seedBoundConversation(t, home, sessionFactsConvID, sessionFactsInitialUUID)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	// The init rider is the ONLY env that distinguishes this from the plain send spec,
	// and its presence is the only thing distinguishing the two tests from each other.
	// Empty, fakeclaude is byte-identical to its prior behaviour.
	h := StartStreamInteractiveWithRelay(t, home, sessionFactsInitialUUID, fr.URL()+"/v2/server",
		sessionFactsRiderEnv+"="+riderValue)
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
	// non-interactive handshake would yield zero session_facts frames in BOTH tests,
	// making the rider-off half vacuously green — the arrival half is what catches it.
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
		ID:   sessionFactsSendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: sessionFactsConvID,
			MessageID:      "m-sessionfacts-1",
			Text:           sessionFactsUserText,
		}),
	})

	// Collect until the turn ends. turn_end is the terminator rather than a frame
	// count, so an extra interleaved broadcast — model_announced, for one, which the
	// same fed line necessarily produces — cannot make this flaky. The rider writes its
	// line BEFORE the reply, so by the time turn_end lands the fed line has necessarily
	// been through the parser: no sleep, no poll, no ordering race to tune.
	var obs sessionFactsObservation
	deadline := time.Now().Add(30 * time.Second)
	for !obs.sawTurnEnd {
		env, ok := nextEnv(deadline)
		if !ok {
			t.Logf("drain deadline reached with rider=%q: session_facts=%d unrecognized=%d echo=%v turn_end=%v",
				riderValue, len(obs.sessionFacts), obs.unrecognized, obs.sawEcho, obs.sawTurnEnd)
			break
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope: %s", string(env.Payload))
		case protocol.TypeSessionFacts:
			// Retained VERBATIM, not decoded: the key-set assertion downstream is the
			// only thing that can see a field the payload type does not declare.
			obs.sessionFacts = append(obs.sessionFacts, append(json.RawMessage(nil), env.Payload...))
		case protocol.TypeUnrecognizedMessage:
			obs.unrecognized++
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if strings.Contains(p.Text, sessionFactsEchoNeedle) {
				obs.sawEcho = true
			}
		case protocol.TypeTurnEnd:
			obs.sawTurnEnd = true
		}
	}
	return obs
}

// TestRelayV2_StreamSessionFactsReachesConnectedPhone is the arrival half: one
// capture-faithful system/init line is fed through the daemon ahead of an ordinary
// reply, and the connected interactive v2 client receives EXACTLY ONE session_facts
// frame carrying the fed build and posture byte for byte.
//
// It fails if the rider never fires, if the fed line is malformed enough to miss the
// init arm, or if the line never reaches the parser.
func TestRelayV2_StreamSessionFactsReachesConnectedPhone(t *testing.T) {
	obs := driveSessionFactsTurn(t, sessionFactsRiderOn)

	if !obs.sawEcho {
		t.Fatalf("the reply never arrived (no assistant_delta carrying %q) — the run did not complete, "+
			"so its counts prove nothing; session_facts=%d unrecognized=%d turn_end=%v "+
			"(a UUID mismatch between seedBootstrapRegistry and seedBoundConversation is the first suspect)",
			sessionFactsEchoNeedle, len(obs.sessionFacts), obs.unrecognized, obs.sawTurnEnd)
	}
	if !obs.sawTurnEnd {
		t.Fatalf("the reply arrived but the turn never closed (no turn_end) — the run did not complete, "+
			"so its counts prove nothing; session_facts=%d unrecognized=%d",
			len(obs.sessionFacts), obs.unrecognized)
	}

	// The fed line must not land in the unrecognized lane. A session_facts count of
	// zero paired with a non-zero count here would say the line reached the parser's
	// FALLBACK rather than its init arm, which turns an inference into an immediate
	// diagnosis.
	if obs.unrecognized != 0 {
		t.Errorf("unrecognized_message frames: got %d, want 0 — the fed system/init line reached the "+
			"unrecognized lane instead of the init arm", obs.unrecognized)
	}
	if len(obs.sessionFacts) != 1 {
		t.Fatalf("session_facts frames: got %d, want exactly 1 — the rider fed one init line, so the "+
			"producer must emit once and the frame must reach the phone\n%s",
			len(obs.sessionFacts), obs.sessionFacts)
	}

	raw := obs.sessionFacts[0]
	var p protocol.SessionFactsPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode session_facts payload: %v\n%s", err, raw)
	}

	// The conversation identity is the BRIDGE's contribution: the parser's event
	// carries none, so this proves the mapper supplied it rather than leaving it empty.
	if p.ConversationID != sessionFactsConvID {
		t.Errorf("conversation_id: got %q, want %q", p.ConversationID, sessionFactsConvID)
	}
	// Verbatim, across four layers. claude's own build, unparsed and unnormalised.
	if p.ClaudeCodeVersion != sessionFactsFedVersion {
		t.Errorf("claude_code_version: got %q, want %q (the fed value, verbatim)",
			p.ClaudeCodeVersion, sessionFactsFedVersion)
	}
	// The fed spelling and the wire spelling differ BY DESIGN — the line carries
	// claude's permissionMode, the frame carries the daemon's permission_mode — so this
	// assertion pins the rename end-to-end as well as the value.
	if p.PermissionMode != sessionFactsFedMode {
		t.Errorf("permission_mode: got %q, want %q (claude's permissionMode, under the daemon's own "+
			"wire name)", p.PermissionMode, sessionFactsFedMode)
	}
	// A live assertion, not decoration: null decodes to nil and [] decodes to an empty
	// non-nil slice, so this distinguishes them. SessionFactsPayload deliberately owns
	// no MarshalJSON, precisely so nothing-was-cut stays an ABSENCE on the wire rather
	// than becoming a positive "these fields were cut: none".
	if p.TruncatedFields != nil {
		t.Errorf("truncated_fields: got %#v, want nil — neither fed value is near the producer's "+
			"256-byte caps, so the wire value must be null, not []", p.TruncatedFields)
	}

	// THE NON-LEAK ASSERTION, and the reason this test keeps the raw bytes. The rider
	// feeds all 24 top-level keys of the captured init line; twenty-one of them are
	// absent from streamsup's systemInitLine, four of those name the operator's
	// filesystem (cwd, memory_paths, messaging_socket_path) or claude's own session
	// identity (session_id). A frame that had grown any of them would satisfy every
	// assertion above and fail only here. Decoding into the payload type cannot make
	// this claim: it discards what it does not declare, which is exactly the thing
	// under test.
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keyed); err != nil {
		t.Fatalf("decode session_facts payload as an object: %v\n%s", err, raw)
	}
	got := make([]string, 0, len(keyed))
	for k := range keyed {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"claude_code_version", "conversation_id", "permission_mode", "truncated_fields"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("session_facts payload key set: got %v, want %v — the frame carries exactly what "+
			"SessionFactsPayload declares and nothing the fed line smuggled through", got, want)
	}
}

// TestRelayV2_StreamSessionFactsRiderOffReachesNoPhone is the non-vacuity control.
// The same helper, the same path, the same connected interactive client, with the
// rider off: ZERO session_facts frames.
//
// Without it the arrival count above would be one observation with no baseline, and a
// path that emitted session_facts for some reason other than the fed line would read
// as a pass. The milestones are asserted FIRST and fatally: a zero read off a run
// where nothing completed says nothing at all.
func TestRelayV2_StreamSessionFactsRiderOffReachesNoPhone(t *testing.T) {
	obs := driveSessionFactsTurn(t, "")

	if !obs.sawEcho {
		t.Fatalf("the reply never arrived (no assistant_delta carrying %q) — the run did not complete, "+
			"so its silence proves nothing; session_facts=%d unrecognized=%d turn_end=%v",
			sessionFactsEchoNeedle, len(obs.sessionFacts), obs.unrecognized, obs.sawTurnEnd)
	}
	if !obs.sawTurnEnd {
		t.Fatalf("the reply arrived but the turn never closed (no turn_end) — the run did not complete, "+
			"so its silence proves nothing; session_facts=%d unrecognized=%d",
			len(obs.sessionFacts), obs.unrecognized)
	}

	if len(obs.sessionFacts) != 0 {
		t.Errorf("session_facts frames with the rider off: got %d, want 0 — nothing but the fed init "+
			"line may produce this frame, so a non-zero here means the arrival count is not measuring "+
			"the rider\n%s", len(obs.sessionFacts), obs.sessionFacts)
	}
	if obs.unrecognized != 0 {
		t.Errorf("unrecognized_message frames: got %d, want 0 — the rider-off stream must be "+
			"byte-identical to the untouched path, which puts nothing in that lane", obs.unrecognized)
	}
}
