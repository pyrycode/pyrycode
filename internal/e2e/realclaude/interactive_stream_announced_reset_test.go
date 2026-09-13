//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamAnnouncedResetFollowsLiveClaude is the #2138 deliverable:
// the real-claude proof that a `/clear` sent as ordinary message text through the
// production stream-json interactive runner makes claude announce a conversation
// reset on its own stdout, that the daemon follows it, and that the client sees
// the delimiter AND keeps receiving a live stream afterwards.
//
// WHY IT EXISTS. The announced-reset path is shipped — #2134 (the parser arm),
// #2135 (the follower: re-key, delimiter, live sink tag) and #2136 (runner id
// adoption) are all merged — and it is proven hermetically against fakeclaude
// (TestRelayV2_StreamAnnouncedResetFollowsClaude, same family, `e2e` tag), which
// emits whatever shape we script it to emit. The premise underneath the whole
// family is that a REAL claude emits
//
//	{"type":"conversation_reset","new_conversation_id":"…","uuid":"…"}
//
// when a message whose text begins with `/clear` goes through the stream-json
// input. That premise rests on a single hand-observed capture (claude 2.1.259,
// 2026-09-04, recorded in #2134's spec with both id VALUES elided, and no capture
// file in the tree holds such a line). If claude changes the shape, or stops
// announcing on this input path, the fake keeps passing forever: the operator gets
// the frozen usage gauge and the unrecognized_message noise row back, and nothing
// reddens. This is the rung that would.
//
// A RED RUN HAS THREE READINGS and only one of them is a bug in this test's own
// subject:
//
//	(a) A real claude no longer announces on the stream-json input path. The
//	    premise is dead — route it back, do NOT weaken the test.
//	(b) It announces a different shape. That surfaces as an unrecognized_message
//	    whose message_type names conversation_reset (the parser has an arm for the
//	    type and nothing else does), which is a parser change and a NEW ticket.
//	(c) The daemon regressed.
//
// Whichever it is, the observed frame is the capture the whole family rests on:
// the transition is t.Logf'd in full where it is asserted, so the gate's own
// output carries it. Nothing is written to disk — see the spec's security review
// for why a committed capture would pull in this package's redaction apparatus
// (newInitControlRedactor, newDropcapScanner) for no gain.
//
// THE ORDERING DECISION, which is the one non-obvious thing in the file: turn 2 is
// sent the moment the session_transition arrives, NOT on any terminal frame of the
// `/clear` turn. That is forced rather than stylistic. The delivery seam
// (turnBusyTracker.waitIdleForDelivery) blocks a second message on the same
// conversation until that conversation has no open turn, and whether the `/clear`
// turn ever closes on its own is precisely this ticket's unknown — a slash command
// replying with nothing is a plausible shape. What unblocks it is the reset
// itself: transitionClearsTurn answers (successor, true) for ReasonClear, so the
// announced reset clears the turn mark and releases the delivery. Waiting for a
// terminal frame first would hang on exactly the shape under test.
//
// WHY NEITHER SHARED DRAIN FITS. drainForCompletedTurn requires a non-empty
// assistant_delta then turn_state{idle} and would Fatal on the `/clear` turn for
// the reason just given; asserting a phone-side delta for the `/clear` turn is
// what this test must not do. drainForControlEvent returns the first
// session_transition but silently consumes every frame before it, which makes both
// "exactly one" and "no unrecognized_message" unassertable after the fact. So the
// window gets one local primitive (resetWindow.next) that every frame passes
// through, with two thin waits over it — the fakeclaude twin's driveTurn is the
// precedent, and its header states the same reason: the transitions arrive
// unsolicited, interleaved with the turn's own frames, and a later read has
// already discarded them.
//
// The spine down to the handshake is transcribed from
// interactive_stream_new_session_test.go (#1174) with no deltas. #2135's tag
// rotation is what makes turn 2's phone-side delta assertable at all: before it, a
// turn issued after a rotation had its assistant_delta dropped at the drain's
// active-session gate, which is why that sibling asserts only an ack. One daemon,
// one seeded bound conversation, one encrypted channel, ONE reader for the whole
// window — the receive nonce is sequential, so a second concurrent reader would
// desync the CipherState. No t.Parallel: WithWorktreeAuthenticated calls t.Setenv.
// The reused setup skips cleanly (exit 0) when claude / creds are absent, exactly
// like every sibling stream spec; the actual green requires a live claude
// (needs-real-claude), and is read from the count of executed tests, never from
// the exit code.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Fixed identifiers for the seeded state. The package's repeated-digit stem
// convention was nearly exhausted when this file landed — 1 and 3–f are taken by
// siblings in internal/e2e/realclaude — so these are the last two: 2 for the
// seeded bootstrap session, 0 for the driving conversation. Both are valid UUIDv4
// stems (version nibble 4, variant nibble 8) and both match uuidStemPattern, which
// AC 1 asserts the ANNOUNCED id against. Reuse across internal/e2e and
// internal/e2e/realclaude is harmless; the constraint is within the package.
// clearBootstrapUUID is the bootstrap session's POOL id (pinned via
// seedBootstrapRegistry, which also makes the stream runner spawn
// `claude --session-id <it>`); clearConvID is the driving conversation, bound to
// it via seedBoundConversation.
const (
	clearBootstrapUUID = "22222222-2222-4222-8222-222222222222"
	clearConvID        = "00000000-0000-4000-8000-000000000000"
)

func TestInteractiveStreamAnnouncedResetFollowsLiveClaude(t *testing.T) {
	// No t.Parallel: WithWorktreeAuthenticated calls t.Setenv.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("realclaude: claude not on PATH: %v", err)
	}
	home := WithWorktreeAuthenticated(t) // skips cleanly when no creds
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("realclaude: resolve claude: %v", err)
	}

	// Isolated workdir under the authenticated HOME. Load-bearing twice: it
	// guarantees the fresh-daemon state (empty claude sessions dir) AND sidesteps
	// the shared-session-folder collision (#828) by construction.
	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir workdir: %v", err)
	}

	// Flip the production toggle to the stream-json interactive runner BEFORE the
	// daemon spawns — resolveConfigPath reads <home>/.pyry/config.json once at
	// startup. The announced-reset path exists only on this runner.
	writeStreamInteractiveConfig(t, home)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("paireddevice.Setup: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Seed the deterministic bootstrap id + the driving conversation binding BEFORE
	// the daemon starts (the registry is loaded once at startup, no reload).
	seedBootstrapRegistry(t, home, clearBootstrapUUID)
	seedBoundConversation(t, home, clearConvID, clearBootstrapUUID, workdir)

	d := spawnBootstrapDaemon(t, home, workdir, claudeBin, relayURL)
	t.Cleanup(func() { d.stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	initSend, initRecv := driveHandshakeInteractive(t, phone, pubKey, payload.Token)

	// A per-run nonce so reruns differ — enough to defeat any accidental caching,
	// without asserting on content. reqID is a single monotonic counter; only the
	// turn-2 id is load-bearing (the ack correlates on InReplyTo).
	nonce := time.Now().UnixNano()
	var reqID uint64 = 2

	// --- Turn 1: a live child, a bound bridge, and the pre-reset id -------------
	// Load-bearing three ways: `/clear` must reach a RUNNING claude, the reply
	// bridge must be bound so the reset's transition resolves to a conversation,
	// and this file gets the shared drain's two standing alarms
	// (unrecognized_message, rate_limited) on the one turn whose shape is not in
	// question. Everything after this point is the window, which owns its own
	// negatives.
	sealSendMessage(t, phone, initSend, reqID, clearConvID, "m-1",
		fmt.Sprintf("Reply with a single short word. run=%d turn=1", nonce))
	reqID++
	drainForCompletedTurn(t, phone, initRecv, clearConvID, perTurnReplyBudget)

	// AC 1's anchor: the id the session held BEFORE the `/clear` send, read from
	// the registry rather than assumed. A normal turn appends to the same
	// transcript and never rotates, so this is still the seeded value; asserting
	// that here turns a seed drift into a failure that names itself, rather than
	// into a confusing previous_session_id mismatch below.
	idBefore := waitBootstrapID(t, home, idSettleTimeout)
	if idBefore != clearBootstrapUUID {
		t.Fatalf("the bootstrap id before the /clear is %q, want the seeded %q — the session rotated for some "+
			"reason other than this test's /clear, and AC 1's previous_session_id anchor is not trustworthy",
			idBefore, clearBootstrapUUID)
	}

	// --- The /clear send: the window opens here --------------------------------
	// Ordinary send_message text, nothing else. msgqueue's Enqueue is
	// EnqueueDelivery with the queued text and the delivered payload equal, so a
	// message with no attachment reaches claude's stdin verbatim — claude receives
	// exactly `/clear` and runs it as a command.
	var w resetWindow
	clearReqID := reqID
	reqID++
	sealSendMessage(t, phone, initSend, clearReqID, clearConvID, "m-clear", "/clear")

	// --- AC 1: the delimiter ---------------------------------------------------
	transition := w.awaitTransition(t, phone, initRecv, rotateBudget)
	if transition.Reason != "clear" {
		t.Errorf("session_transition reason = %q, want %q", transition.Reason, "clear")
	}
	if transition.ConversationID != clearConvID {
		t.Errorf("session_transition conversation_id = %q, want the driving conversation %q",
			transition.ConversationID, clearConvID)
	}
	if transition.PreviousSessionID != idBefore {
		t.Errorf("session_transition previous_session_id = %q, want %q — the id the session held before the "+
			"/clear send", transition.PreviousSessionID, idBefore)
	}
	if transition.NewSessionID == idBefore || !uuidStemPattern.MatchString(transition.NewSessionID) {
		t.Errorf("session_transition new_session_id %q is not a fresh canonical UUIDv4 stem distinct from %q — "+
			"claude's announced new_conversation_id reached the client in a shape #2134's fixtures did not predict",
			transition.NewSessionID, idBefore)
	}
	// THE CAPTURE. #2134's fixtures were written from a captured SHAPE with
	// reconstructed id VALUES, and no capture file in the tree holds a
	// conversation_reset line; this is the family's first live observation of one.
	// It goes in the gate's log, and from there into a ticket comment — never to
	// disk, which would need this package's redaction pass for claude-authored
	// bytes produced under the operator's real credentials.
	t.Logf("CAPTURE (#2138): session_transition{reason=%q, previous_session_id=%q, new_session_id=%q, "+
		"conversation_id=%q, occurred_at=%s} — a live claude announced a reset on the stream-json input path",
		transition.Reason, transition.PreviousSessionID, transition.NewSessionID,
		transition.ConversationID, transition.OccurredAt.Format(time.RFC3339Nano))

	// --- AC 2: a turn after the reset still completes for the client -----------
	// Sent on the transition, not on a terminal frame of the `/clear` turn — see
	// the header's ordering decision. The ack proves Route resolved clearConvID to
	// the RE-KEYED pool entry; the delta and the terminal idle prove the sink tag
	// rotated with it, which is the regression whose failure is worse than the bug
	// #2135 fixed (every later event dropped at the active-session gate, and the
	// conversation dark until the daemon restarts).
	turn2ReqID := reqID
	reqID++
	sealSendMessage(t, phone, initSend, turn2ReqID, clearConvID, "m-2",
		fmt.Sprintf("Reply with a single short word. run=%d turn=2", nonce))
	w.awaitCompletedTurn(t, phone, initRecv, clearConvID, turn2ReqID, perTurnReplyBudget)

	// --- AC 2's other half: exactly one transition across the whole window ------
	// The window ran from the /clear send to turn 2's terminal frame and every
	// frame in it passed through resetWindow.next, so this count is over all of
	// them rather than over whatever a later read had not already discarded.
	// #2137 retired the rotation watcher, which was the second writer, so one is
	// now structural — the assertion is what would catch a second being
	// reintroduced, or claude announcing twice for one `/clear`.
	if len(w.transitions) != 1 {
		t.Fatalf("the client saw %d session_transition frames between the /clear send and turn 2's terminal "+
			"frame, want exactly 1: %+v — more than 1 means something re-keyed a second time for the same "+
			"reset (a second writer, or claude announcing twice)", len(w.transitions), w.transitions)
	}
	t.Logf("AC 2: turn 2 completed for the client after the reset, and exactly one session_transition crossed "+
		"the window (send_message #%d)", turn2ReqID)
}

// --- the window drain (the only genuinely new code) --------------------------

// resetWindow is the state the announced-reset window accumulates. Every frame
// between the `/clear` send and turn 2's terminal turn_state{idle} passes through
// its next method, which is what makes AC 2's "exactly one" and AC 3's "no
// unrecognized_message" structurally true rather than true of whatever a
// coarser drain happened not to have consumed already.
type resetWindow struct {
	transitions []protocol.SessionTransitionPayload
}

// next reads one binary→phone envelope, applies the two window-wide negatives and
// counts session_transition frames, and returns the envelope for the caller's own
// state machine. It reports false when deadline passes with nothing more read.
//
// Receive-nonce discipline: every noise_msg is decrypted in arrival order and
// non-noise_msg control frames (e.g. rekey) are skipped WITHOUT decrypting, since
// they do not advance the nonce. There is exactly one reader for the whole window
// — a second concurrent one would desync the CipherState, and the failure would
// surface as an unrelated decrypt error many frames later.
//
// The negatives are Fatal rather than returned, deliberately: neither is a
// condition any caller could do something better with, and folding them in here is
// what lets the two waits below stay thin.
func (w *resetWindow) next(t *testing.T, phone *fakephone.Client, cs *noise.CipherState,
	deadline time.Time) (protocol.Envelope, bool) {
	t.Helper()
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return protocol.Envelope{}, false
		}
		raw, err := phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				continue // re-loop into the deadline check above
			}
			t.Fatalf("phone receive (announced-reset window): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame (announced-reset window): %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue // does not advance the receive nonce — skip without decrypting
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data (announced-reset window): %v", err)
		}
		plain, err := cs.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope (announced-reset window): %v", err)
		}
		switch env.Type {
		case protocol.TypeUnrecognizedMessage:
			// AC 3, and the arm that carries reading (b) from the header. The
			// parser has an arm for conversation_reset and nothing else does, so a
			// changed announcement shape lands HERE naming that type rather than
			// producing silence. Any other message_type is the ordinary
			// known-ignored-list staleness this suite's shared drain also watches
			// for — read the payload before deciding which.
			var p protocol.UnrecognizedMessagePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode unrecognized_message payload: %v", err)
			}
			t.Fatalf("the /clear window produced an unrecognized_message: site=%q type=%q truncated=%v\n"+
				"raw: %s\n\n"+
				"If type names conversation_reset, claude changed the announcement's SHAPE: that is a parser "+
				"change and a NEW ticket, not this one — record the raw line above. Otherwise claude grew a "+
				"message type streamsup.ignoredLineTypes has not measured.",
				p.Site, p.MessageType, p.Truncated, p.Raw)
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope inside the /clear window: %s", string(env.Payload))
		case protocol.TypeSessionTransition:
			var p protocol.SessionTransitionPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode session_transition payload: %v", err)
			}
			w.transitions = append(w.transitions, p)
		}
		return env, true
	}
}

// awaitTransition reads the window forward to its first session_transition and
// returns that payload. Every frame before it — the /clear send's own ack, any
// turn_state, any delta — is read through next, so it is counted and checked
// rather than discarded.
func (w *resetWindow) awaitTransition(t *testing.T, phone *fakephone.Client, cs *noise.CipherState,
	timeout time.Duration) protocol.SessionTransitionPayload {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		env, ok := w.next(t, phone, cs, deadline)
		if !ok {
			t.Fatalf("no session_transition reached the client within %s of the /clear send. Three readings, "+
				"and only the last is a bug in the daemon: (a) a real claude no longer announces a "+
				"conversation_reset on the stream-json input path — the premise the whole #2134/#2135/#2136 "+
				"family rests on is dead, route this back rather than weakening the test; (b) the /clear never "+
				"reached the child at all; (c) the follower stopped re-keying. A changed announcement SHAPE "+
				"would have failed earlier as an unrecognized_message instead.", timeout)
		}
		if env.Type == protocol.TypeSessionTransition {
			// next already decoded and recorded it; the last one recorded is this.
			return w.transitions[len(w.transitions)-1]
		}
	}
}

// awaitCompletedTurn reads the window forward through turn 2: the ack correlated
// on reqID, then a non-empty assistant_delta for convID, then the terminal
// turn_state{idle} for convID. The three are ordered — a delta before the ack is
// ignored, a turn_state before the delta is ignored — which keeps a leading
// responding state, and any idle the reset's own turn-clear may produce, from
// closing the turn early. That mirrors drainForCompletedTurn's guard.
//
// No content assertion: real claude's words are non-deterministic. Nor is there a
// turn-id exclusion set to prove the delta belongs to turn 2 rather than to a
// late-streaming tail of the /clear turn, and that is a decision rather than an
// omission — every event after the re-key crosses the ROTATED sink tag, and before
// #2135 every post-reset event was dropped at the drain's active-session gate. Any
// delta arriving after the announced reset is already the property under test.
func (w *resetWindow) awaitCompletedTurn(t *testing.T, phone *fakephone.Client, cs *noise.CipherState,
	convID string, reqID uint64, timeout time.Duration) {
	t.Helper()
	sawAck, sawDelta := false, false
	deadline := time.Now().Add(timeout)
	for {
		env, ok := w.next(t, phone, cs, deadline)
		if !ok {
			switch {
			case !sawAck:
				t.Fatalf("no ack for send_message #%d within %s of the reset — the turn never routed to the "+
					"re-keyed pool entry (Route resolved %q to a session Pool.Lookup no longer holds), or the "+
					"delivery seam never found the conversation idle", reqID, timeout, convID)
			case !sawDelta:
				t.Fatalf("send_message #%d was acked but no non-empty assistant_delta for %q reached the client "+
					"within %s — this is the dark-conversation regression: the registry re-keyed without the "+
					"runner's stream session tag rotating with it, so every later event carries the retired id "+
					"and the drain's active-session gate drops all of them", reqID, convID, timeout)
			default:
				t.Fatalf("the post-reset turn for %q streamed a delta but never closed with a terminal "+
					"turn_state{idle} within %s", convID, timeout)
			}
		}
		switch env.Type {
		case protocol.TypeAck:
			if env.InReplyTo != nil && *env.InReplyTo == reqID {
				sawAck = true
				t.Logf("the rotated session accepted the post-reset turn (ack for send_message #%d)", reqID)
			}
		case protocol.TypeAssistantDelta:
			if !sawAck {
				continue // anything before the ack belongs to the /clear turn
			}
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if p.ConversationID != convID || strings.TrimSpace(p.Text) == "" {
				continue
			}
			if !sawDelta {
				sawDelta = true
				t.Logf("non-empty assistant_delta (turn_id=%s seq=%d, %d bytes) for %q after the reset",
					p.TurnID, p.Seq, len(p.Text), convID)
			}
		case protocol.TypeTurnState:
			if !sawDelta {
				continue // the leading responding state, and any idle the reset's turn-clear produced
			}
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			if st.State == "idle" && st.ConversationID == convID {
				t.Logf("terminal turn_state{idle} for %q — the post-reset turn closed", convID)
				return
			}
		}
	}
}
