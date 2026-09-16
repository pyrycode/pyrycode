//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamClearRunsDaemonReset is the #2485 deliverable: the
// real-claude proof that a `/clear` sent as ordinary message text through the
// production stream-json interactive runner runs the DAEMON'S conversation reset —
// the three `resetting` edges, the `clear` transition — and that the handoff note
// the reset writes reaches the successor, carried by a fact the successor could
// only have learned from it.
//
// WHY THE FILE KEEPS A NAME THAT NO LONGER DESCRIBES IT. This file landed as
// #2138's announced-reset proof: a real claude, handed `/clear` on its stdin,
// announcing
//
//	{"type":"conversation_reset","new_conversation_id":"…","uuid":"…"}
//
// on its own stdout for the daemon to follow. #2456 retired that premise for this
// input path — a client's `/clear` is now intercepted in SendMessage and runs the
// reset instead of reaching claude at all — so the case was asserting something the
// path can no longer produce. The TEST is renamed to say what it proves; the FILE
// is not, because docs/knowledge/features/e2e-realclaude.md names a child page after
// this path and #2486 extends the same drain here. The scope fence is worth
// restating: this retires the announced-reset premise from the LIVE suite only.
// streamsup's parser arm and the follower stand as they are, and whether any
// remaining input path can still produce a `conversation_reset` line is a separate
// question.
//
// WHAT THE SHIPPED PATH DOES, all on one goroutine
// (activeSessionStarter.resetThenRotate): the `wrapping_up` edge, the wrap-up turn
// (conversationReset.wrapUp), the `restarting` edge carrying `written` or `skipped`,
// then the rotation — which recomposes the successor's appended system prompt and
// fires the `clear` transition — then the falling edge.
//
// THE SUCCESSOR IS HANDED THE NOTE'S TEXT, NOT A PATH TO IT. #2474 measured that a
// Read outside the workspace raises a permission modal under the in-band `default`
// posture every daemon session runs in, so #2475 composes the note into the appended
// system prompt under a daemon-owned heading, inside sessions.FencedHandoffNote's
// fence. There is no tool call to observe. What a live run can prove is that a fact
// told to the PREDECESSOR reaches the SUCCESSOR's first reply — which is the second
// half of this case, and it rides the same reset as the first. Two cases would
// double the tokens the gate spends for one round trip's worth of evidence.
//
// THE ORDERING DECISION. The three `resetting` edges are asserted in order: they are
// pushed synchronously from resetThenRotate's own goroutine, and V2SessionManager.Push
// appends to a per-conn FIFO, so their order on the wire is the order they were
// emitted. The transition is asserted only to arrive AFTER the `restarting` edge, and
// nothing here asserts an order between the transition and the falling edge. That is
// measured, not conceded: Pool.notifyTransition calls its observer synchronously, but
// that observer is sessionTransitionEmitterV2.Enqueue — a non-blocking send into a
// buffer whose fan-out happens later on the emitter's own Run goroutine. So
// `restarting` before the transition holds structurally (its Push returns before
// startFreshRunner can enqueue anything) while the falling edge, pushed the moment
// startFreshRunner returns, races that queue's drain. Do not make the emitter
// synchronous to buy a tidier assertion: the non-blocking hand-off is #659's
// requirement, and a falling edge arriving first costs a client nothing.
//
// THE TURN-ID EXCLUSION REVERSES THIS FILE'S OWN EARLIER DECISION, deliberately. The
// version of this case that asserted only liveness after the reset documented having
// no exclusion set, on the grounds that any delta arriving after the re-key was
// already the property under test. That reasoning does not survive a CONTENT
// assertion. The predecessor's wrap-up reply reaches the client too — wrapUpCapture's
// sink forwards every event downstream unchanged — and that reply contains the
// planted fact BY DESIGN, since putting it there is what the wrap-up prompt asks for.
// A late wrap-up delta counted as the successor's would pass the recall assertion
// while proving nothing. So the window records every turn id it sees, the recall
// drain is handed a snapshot of that set taken before its send, and only a turn the
// set does not name can answer it.
//
// WHY THE WINDOW OWNS ONE READER. drainForCompletedTurn requires a non-empty
// assistant_delta then turn_state{idle} and cannot express "three edges and a
// transition, two of them unordered against each other"; drainForControlEvent
// returns the first session_transition but silently consumes every frame before it,
// which makes both the edge sequence and the window-wide negatives unassertable
// after the fact. So the window gets one local primitive (resetWindow.next) that
// every frame passes through, with two thin waits over it. #2486 asserts the
// `skipped` outcome through this same drain, which is why the edge collection lives
// on the window and not inline in the case body.
//
// SECURITY. The note is claude-authored text that crosses into the successor's system
// prompt inside the fence. ResettingPayload reports only WHETHER a note was made and
// never its bytes — resettingEmitterV2 holds no note store, no reply text and no
// conversationReset — so the edges are daemon-authored throughout and safe to log
// verbatim, which is what the capture below does. Claude's own words are not: the two
// replies this case reads are logged as a byte count on the green path and printed in
// full only inside a failure message, where a red run needs them and a human is
// already reading. Nothing is written to disk, #2138's posture kept for #2138's
// reason — a committed capture would pull this package's redaction apparatus
// (newInitControlRedactor, newDropcapScanner) in for no gain.
//
// The spine down to the handshake is transcribed from
// interactive_stream_new_session_test.go (#1174) with no deltas. One daemon, one
// seeded bound conversation, one encrypted channel, ONE reader for the whole run —
// the receive nonce is sequential, so a second concurrent reader would desync the
// CipherState. No t.Parallel: WithWorktreeAuthenticated calls t.Setenv. The reused
// setup skips cleanly (exit 0) when claude / creds are absent, exactly like every
// sibling stream spec; the actual green requires a live claude (needs-real-claude),
// and is read from the count of executed tests, never from the exit code.

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
// the transition's new_session_id is asserted against. Reuse across internal/e2e and
// internal/e2e/realclaude is harmless; the constraint is within the package.
// clearBootstrapUUID is the bootstrap session's POOL id (pinned via
// seedBootstrapRegistry, which also makes the stream runner spawn
// `claude --session-id <it>`); clearConvID is the driving conversation, bound to
// it via seedBoundConversation.
const (
	clearBootstrapUUID = "22222222-2222-4222-8222-222222222222"
	clearConvID        = "00000000-0000-4000-8000-000000000000"
)

// clearResetWindowBudget bounds the WHOLE reset — the `/clear` send to the last of
// the four frames — and it is sized against the daemon's own bound rather than
// borrowed from a sibling.
//
// IT MUST STAY WELL ABOVE wrapUpDeadline, which is the inverse of the margin rule a
// live budget usually states (#2416's constant argues for staying well UNDER the
// production timeout it proves early). Here the daemon's ninety seconds bound the
// idle wait, the wrap-up turn AND the note write, so the `restarting` edge can
// legitimately arrive a minute and a half after the `/clear`, with the rotation still
// to come. rotateBudget (45 s) expires before the daemon's own bound does, and that
// red would name a daemon fault that is really this test's budget. Sixty seconds of
// slack over the bound covers the rotation and leaves the failure meaningful: a
// window that misses THIS deadline missed a bound the daemon itself promises to keep.
const clearResetWindowBudget = 150 * time.Second

func TestInteractiveStreamClearRunsDaemonReset(t *testing.T) {
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
	// startup. conversationReset.wrapUp needs a runner that captures a reply, and
	// streamRunner is the only one that does.
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

	// A per-run nonce so reruns differ, defeating any cross-run caching that could
	// produce a false green. The token is contiguous uppercase hex — no internal
	// separators for claude to reformat — which is TestInteractiveStreamMultiTurnContinuity's
	// discipline for the same job.
	nonce := time.Now().UnixNano()
	token := fmt.Sprintf("PYRY%X", nonce)
	var reqID uint64 = 2
	plantReqID := reqID
	reqID++
	clearReqID := reqID
	reqID++
	recallReqID := reqID

	// --- Turn 1: a live child, a bound bridge, and the fact to be handed on ------
	// Load-bearing three ways. activeSessionStarter.start refuses a NAMED
	// conversation whose runner reports no child (State().ChildPID == 0), so without
	// this turn the `/clear` would be inert and every assertion below would fail for
	// the wrong reason. The fact must be something THE USER told the session, because
	// the clause that carries it into the note is wrapUpPromptText's last one
	// ("anything the user told you this session that you did not save yourself"). And
	// this is the one turn whose shape is not in question, so it takes the shared
	// drain's two standing alarms (unrecognized_message, rate_limited); everything
	// after it is the window, which owns its own negatives.
	sealSendMessage(t, phone, initSend, plantReqID, clearConvID, "m-1",
		fmt.Sprintf("For the rest of this conversation, remember one fact about me: my build tag for this "+
			"run is %s. I have not written it down anywhere. Reply with just the word ok.", token))
	drainForCompletedTurn(t, phone, initRecv, clearConvID, perTurnReplyBudget)

	// The transition's previous_session_id anchor, read from the registry rather than
	// assumed. A normal turn appends to the same transcript and never rotates, so this
	// is still the seeded value; asserting that here turns a seed drift into a failure
	// that names itself, rather than into a confusing previous_session_id mismatch
	// below.
	idBefore := waitBootstrapID(t, home, idSettleTimeout)
	if idBefore != clearBootstrapUUID {
		t.Fatalf("the bootstrap id before the /clear is %q, want the seeded %q — the session rotated for some "+
			"reason other than this test's /clear, and the previous_session_id anchor is not trustworthy",
			idBefore, clearBootstrapUUID)
	}

	// --- The /clear send: the window opens here --------------------------------
	// Ordinary send_message text, nothing else — the client verb, not the new_session
	// control frame. isClearCommand matches the first whitespace-delimited token
	// case-sensitively, and SendMessage's intercept sits below Route, so the id
	// crossing into the reset is this registry key with its live binding.
	w := newResetWindow()
	sealSendMessage(t, phone, initSend, clearReqID, clearConvID, "m-clear", "/clear")

	// --- AC 1: the four frames of the daemon-run reset -------------------------
	w.awaitReset(t, phone, initRecv, clearReqID, clearResetWindowBudget)

	assertResetEdge(t, "the first rising edge", w.edges[0], protocol.ResettingPayload{
		ConversationID: clearConvID,
		Active:         true,
		Phase:          protocol.ResetPhaseWrappingUp,
		Handoff:        protocol.ResetHandoffPending,
	})
	if w.edges[1].Handoff == protocol.ResetHandoffSkipped {
		t.Fatalf("the reset reported handoff=%q on its phase change: the wrap-up turn produced no note the "+
			"successor could be given, so the second half of this case has nothing to prove. `written` is "+
			"claude's outcome to produce, not the daemon's — conversationReset.wrapUp reports skipped on a "+
			"reply storeNote judges unusable (it asks sessions.FencedHandoffNote), on a wrap-up that misses "+
			"wrapUpDeadline, and on an idle wait that never settles. Read the daemon log's reset.wrapup.* "+
			"record to tell those apart and route the finding; do NOT relax this assertion to accept the "+
			"token. #2486 owns the skipped proof.", w.edges[1].Handoff)
	}
	assertResetEdge(t, "the phase change", w.edges[1], protocol.ResettingPayload{
		ConversationID: clearConvID,
		Active:         true,
		Phase:          protocol.ResetPhaseRestarting,
		Handoff:        protocol.ResetHandoffWritten,
	})
	assertResetEdge(t, "the falling edge", w.edges[2], protocol.ResettingPayload{
		ConversationID: clearConvID,
		Active:         false,
		Phase:          "",
		Handoff:        "",
	})

	// The transition arrives AFTER the `restarting` edge and is asserted against
	// nothing else — see the header's ordering decision for why the falling edge is
	// deliberately left unordered against it. edges[1] is the phase change, so two
	// edges having preceded the transition is exactly that claim.
	tr := w.transitions[0]
	if tr.edgesBefore < 2 {
		t.Errorf("the session_transition reached the client after %d resetting edge(s), want it after at least "+
			"the two rising ones: the rotation that fires the transition runs BELOW the `restarting` edge on "+
			"resetThenRotate's own goroutine, so an earlier arrival means the edges are no longer emitted from "+
			"there", tr.edgesBefore)
	}
	if tr.payload.Reason != "clear" {
		t.Errorf("session_transition reason = %q, want %q", tr.payload.Reason, "clear")
	}
	if tr.payload.ConversationID != clearConvID {
		t.Errorf("session_transition conversation_id = %q, want the driving conversation %q",
			tr.payload.ConversationID, clearConvID)
	}
	if tr.payload.PreviousSessionID != idBefore {
		t.Errorf("session_transition previous_session_id = %q, want %q — the id the session held before the "+
			"/clear send", tr.payload.PreviousSessionID, idBefore)
	}
	if tr.payload.NewSessionID == idBefore || !uuidStemPattern.MatchString(tr.payload.NewSessionID) {
		t.Errorf("session_transition new_session_id %q is not a fresh canonical UUIDv4 stem distinct from %q — "+
			"Pool.RotateForNewSession did not mint the successor's id the way the registry records it",
			tr.payload.NewSessionID, idBefore)
	}
	// THE CAPTURE, and the whole of it is daemon-authored: the three edges carry
	// tokens selected from protocol's two closed sets, and the transition carries ids
	// the daemon assigned. It goes in the gate's log, and from there into a ticket
	// comment — never to disk. Nothing claude wrote is in this line.
	t.Logf("CAPTURE (#2485): a live client /clear ran the daemon's reset — "+
		"resetting{active=%v phase=%q handoff=%q} → resetting{active=%v phase=%q handoff=%q} → "+
		"session_transition{reason=%q, previous_session_id=%q, new_session_id=%q, conversation_id=%q, "+
		"occurred_at=%s} (after %d edges) → resetting{active=%v phase=%q handoff=%q}",
		w.edges[0].Active, w.edges[0].Phase, w.edges[0].Handoff,
		w.edges[1].Active, w.edges[1].Phase, w.edges[1].Handoff,
		tr.payload.Reason, tr.payload.PreviousSessionID, tr.payload.NewSessionID,
		tr.payload.ConversationID, tr.payload.OccurredAt.Format(time.RFC3339Nano), tr.edgesBefore,
		w.edges[2].Active, w.edges[2].Phase, w.edges[2].Handoff)

	// --- AC 2: the note reached the successor ----------------------------------
	// The snapshot is taken BEFORE the send and names every turn the client has seen
	// so far — the plant turn and the predecessor's wrap-up reply among them. The
	// wrap-up reply contains the planted fact by design, so a late delta of that turn
	// answering this assertion is the one way it could pass vacuously.
	prior := w.snapshotTurnIDs()
	sealSendMessage(t, phone, initSend, recallReqID, clearConvID, "m-2",
		"Earlier in this conversation I told you my build tag for this run. What is it? "+
			"Reply with only the tag and nothing else.")
	reply := w.awaitRecallReply(t, phone, initRecv, clearConvID, recallReqID, prior, perTurnReplyBudget)
	if !strings.Contains(strings.ToUpper(reply), token) {
		t.Fatalf("the successor's first reply does not carry the fact the predecessor was told. TWO READINGS, "+
			"and this test structurally cannot separate them: the reset reported handoff=%q, so a note WAS "+
			"stored — either the fact never entered it (the wrap-up prompt's last clause did not pull a fact "+
			"the user stated), or the successor did not use it (the note is composed into the appended system "+
			"prompt under handoffNoteLead, which tells the successor to consult it rather than obey it). The "+
			"note's bytes are not observable from a client BY DESIGN — do not reach for the note file to tell "+
			"these apart. Planted %q; reply was %q",
			w.edges[1].Handoff, token, reply)
	}
	t.Logf("AC 2: the successor's first reply (%d bytes) carried the planted fact, which this test never "+
		"repeated to it — the handoff note crossed the rotation (send_message #%d)", len(reply), recallReqID)

	// --- The window-wide counts, over every frame rather than the survivors -----
	// The window ran from the /clear send to the recall turn's terminal frame and
	// every frame in it passed through resetWindow.next, so these counts are over all
	// of them rather than over whatever a later read had not already discarded. One
	// reset emits exactly two rising edges and one falling edge, and #2137 retired the
	// rotation watcher that was the second transition writer — so both counts are
	// structural, and this is what would catch a second writer being reintroduced or
	// one `/clear` starting two resets.
	if len(w.edges) != 3 {
		t.Errorf("the client saw %d resetting frames across the whole window, want exactly 3 "+
			"(wrapping_up, restarting, falling): %+v — a repeated active:true carrying a new phase is a phase "+
			"change, not a second reset, so a fourth frame means something started one", len(w.edges), w.edges)
	}
	if len(w.transitions) != 1 {
		t.Errorf("the client saw %d session_transition frames across the whole window, want exactly 1: %+v — "+
			"more than 1 means something re-keyed a second time for the same reset",
			len(w.transitions), w.transitions)
	}
}

// assertResetEdge compares one observed edge against the whole payload the phase is
// specified to carry. ResettingPayload is three strings and a bool, so equality is
// the assertion — a field-by-field walk would let a field added later go unchecked,
// which on a frame whose two token sets are CLOSED is exactly the drift worth
// catching.
func assertResetEdge(t *testing.T, what string, got, want protocol.ResettingPayload) {
	t.Helper()
	if got != want {
		t.Errorf("%s: resetting = %+v, want %+v", what, got, want)
	}
}

// --- the window drain --------------------------------------------------------

// windowTransition is one observed session_transition plus the one thing about its
// ARRIVAL that is assertable: how many resetting edges had already reached the client
// when it landed. Recording the count rather than a timestamp is what keeps the
// assertion in the same terms as the claim — "after the restarting edge" — instead of
// turning an ordering question into a clock comparison.
type windowTransition struct {
	payload     protocol.SessionTransitionPayload
	edgesBefore int
}

// resetWindow is the state the reset window accumulates. Every frame between the
// `/clear` send and the recall turn's terminal turn_state{idle} passes through its
// next method, which is what makes the window-wide counts and the no-unrecognized_message
// negative structurally true rather than true of whatever a coarser drain happened
// not to have consumed already.
type resetWindow struct {
	edges       []protocol.ResettingPayload
	transitions []windowTransition
	// turnIDs names every turn the client has seen a delta for. Its only consumer is
	// the recall drain's exclusion set — see the header for why that set exists here
	// and did not before.
	turnIDs map[string]struct{}
	// acked names every in_reply_to the client has been answered on. It carries no
	// assertion; it exists so the window's own timeout can say whether the `/clear`
	// frame was accepted at all, which separates "the frame never landed" from "the
	// frame landed and the reset was inert".
	acked map[uint64]struct{}
}

func newResetWindow() *resetWindow {
	return &resetWindow{
		turnIDs: make(map[string]struct{}),
		acked:   make(map[uint64]struct{}),
	}
}

// next reads one binary→phone envelope, applies the two window-wide negatives,
// records what the two waits below need, and returns the envelope for the caller's
// own state machine. It reports false when the deadline passes with nothing more
// read.
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
			t.Fatalf("phone receive (/clear window): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame (/clear window): %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue // does not advance the receive nonce — skip without decrypting
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data (/clear window): %v", err)
		}
		plain, err := cs.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope (/clear window): %v", err)
		}
		switch env.Type {
		case protocol.TypeUnrecognizedMessage:
			var p protocol.UnrecognizedMessagePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode unrecognized_message payload: %v", err)
			}
			t.Fatalf("the /clear window produced an unrecognized_message: site=%q type=%q truncated=%v\n"+
				"raw: %s\n\n"+
				"Since #2456 the `/clear` never reaches claude, so this is NOT the retired announced-reset "+
				"shape drifting. If message_type names conversation_reset, some OTHER input path is still "+
				"producing that line and that is a new ticket, not this one — record the raw line above. "+
				"Otherwise claude grew a message type streamsup.ignoredLineTypes has not measured, or the "+
				"wrap-up turn answered with something other than the prose its prompt asks for.",
				p.Site, p.MessageType, p.Truncated, p.Raw)
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope inside the /clear window: %s", string(env.Payload))
		case protocol.TypeAck:
			if env.InReplyTo != nil {
				w.acked[*env.InReplyTo] = struct{}{}
			}
		case protocol.TypeResetting:
			var p protocol.ResettingPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode resetting payload: %v", err)
			}
			w.edges = append(w.edges, p)
		case protocol.TypeSessionTransition:
			var p protocol.SessionTransitionPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode session_transition payload: %v", err)
			}
			w.transitions = append(w.transitions, windowTransition{payload: p, edgesBefore: len(w.edges)})
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if p.TurnID != "" {
				w.turnIDs[p.TurnID] = struct{}{}
			}
		}
		return env, true
	}
}

// awaitReset reads the window forward until the reset's three resetting edges and its
// one session_transition have all arrived, in whatever interleaving the two
// goroutines that emit them produce. The order they arrived in is recorded, not
// enforced here; the case above is where the ordering claims are made.
func (w *resetWindow) awaitReset(t *testing.T, phone *fakephone.Client, cs *noise.CipherState,
	clearReqID uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for len(w.edges) < 3 || len(w.transitions) < 1 {
		if _, ok := w.next(t, phone, cs, deadline); !ok {
			_, acked := w.acked[clearReqID]
			t.Fatalf("the reset did not complete for the client within %s of the /clear send: %d of 3 resetting "+
				"edges and %d of 1 session_transition arrived (the /clear frame was acked: %v). The readings, "+
				"and only the last is a bug in the daemon's reset itself: (a) the /clear was never intercepted "+
				"— isClearCommand matches the first token case-sensitively, and an unacked frame means it did "+
				"not reach the handler at all; (b) the reset was refused inert — a named conversation whose "+
				"runner reports no live child, a reset already in progress, or an unwired resetter, all of "+
				"which log a v2.new_session.* or send_message.clear_* record and emit no edge; (c) the wrap-up "+
				"outran wrapUpDeadline, which this budget is sized to clear; (d) the emitter stopped reaching "+
				"interactive conns. Edges so far: %+v",
				timeout, len(w.edges), len(w.transitions), acked, w.edges)
		}
	}
}

// snapshotTurnIDs copies the turn ids seen so far, so a later read cannot widen the
// set a drain was handed. The copy is the point: w.turnIDs keeps growing while the
// recall turn streams, and the recall's own turn would otherwise exclude itself.
func (w *resetWindow) snapshotTurnIDs() map[string]struct{} {
	prior := make(map[string]struct{}, len(w.turnIDs))
	for id := range w.turnIDs {
		prior[id] = struct{}{}
	}
	return prior
}

// awaitRecallReply reads the window forward through the recall turn and returns that
// turn's assistant text: the ack correlated on reqID, then the first non-empty
// assistant_delta for convID whose turn id is NOT in prior, then that turn's deltas
// accumulated to the terminal turn_state{idle} for convID.
//
// The three are ordered — a delta before the ack is ignored, a turn_state before the
// delta is ignored — which keeps a leading responding state, and any idle the reset's
// own turn-clear may produce, from closing the turn early. That mirrors
// drainForCompletedTurn's guard.
//
// prior is the exclusion set, and it is what makes the caller's CONTENT assertion
// mean anything: the predecessor's wrap-up reply is on this wire and contains the
// planted fact by design, so a delta of a turn the window has already seen cannot be
// allowed to answer for the successor. Only the turn id is used — no content
// heuristic — because the successor's words are as non-deterministic as the
// predecessor's.
func (w *resetWindow) awaitRecallReply(t *testing.T, phone *fakephone.Client, cs *noise.CipherState,
	convID string, reqID uint64, prior map[string]struct{}, timeout time.Duration) string {
	t.Helper()
	sawAck := false
	successorTurn := ""
	var text strings.Builder
	deadline := time.Now().Add(timeout)
	for {
		env, ok := w.next(t, phone, cs, deadline)
		if !ok {
			switch {
			case !sawAck:
				t.Fatalf("no ack for send_message #%d within %s of the reset — the turn never routed to the "+
					"re-keyed pool entry (Route resolved %q to a session Pool.Lookup no longer holds), or the "+
					"delivery seam never found the conversation idle", reqID, timeout, convID)
			case successorTurn == "":
				t.Fatalf("send_message #%d was acked but no non-empty assistant_delta for %q on a turn the "+
					"window had not already seen reached the client within %s — either the successor never "+
					"answered, or every delta carried a turn id from before the reset, which is the "+
					"dark-conversation regression: the registry re-keyed without the runner's stream session "+
					"tag rotating with it", reqID, convID, timeout)
			default:
				t.Fatalf("the successor's turn for %q streamed a delta but never closed with a terminal "+
					"turn_state{idle} within %s", convID, timeout)
			}
		}
		switch env.Type {
		case protocol.TypeAck:
			if env.InReplyTo != nil && *env.InReplyTo == reqID {
				sawAck = true
				t.Logf("the rotated session accepted the recall turn (ack for send_message #%d)", reqID)
			}
		case protocol.TypeAssistantDelta:
			if !sawAck {
				// Anything before the ack belongs to an earlier turn, and this is the
				// FIRST of the two independent guards against the predecessor's wrap-up
				// reply answering for the successor. It rests on wire order: the ack is
				// pushed when the daemon receives the recall frame, so every delta
				// already in flight precedes it. prior below rests on turn identity
				// instead, and holds even where that ordering does not.
				continue
			}
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if p.ConversationID != convID || strings.TrimSpace(p.Text) == "" {
				continue
			}
			if _, seen := prior[p.TurnID]; seen {
				continue // a tail of the plant turn or the predecessor's wrap-up reply
			}
			if successorTurn == "" {
				successorTurn = p.TurnID
				t.Logf("the successor answered on turn_id=%s (seq=%d) for %q", p.TurnID, p.Seq, convID)
			}
			if p.TurnID != successorTurn {
				continue // a second new turn is not this one's text
			}
			text.WriteString(p.Text)
		case protocol.TypeTurnState:
			if successorTurn == "" {
				continue // the leading responding state, and any idle the reset's turn-clear produced
			}
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			if st.State == "idle" && st.ConversationID == convID {
				t.Logf("terminal turn_state{idle} for %q — the successor's turn closed", convID)
				return text.String()
			}
		}
	}
}
