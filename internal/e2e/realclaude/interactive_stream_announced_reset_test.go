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
// WHICH SESSION THE CONVERSATION IS BOUND TO IS A PRECONDITION OF THAT HALF, not a
// setup detail. A note is only ever composed into a PER-SESSION appended prompt, so a
// conversation seeded onto the daemon's bootstrap session gets a reset that writes a
// note and a successor that is never handed one — with every frame on the wire
// reporting success, because handoff="written" describes the note store and not any
// later compose. This case therefore binds its conversation to a session of its own
// and proves that it did, deterministically, before it spends a wrap-up: see the
// constants' doc for the mechanism and assertPerSessionPrompt for the guard.
//
// THREAD DELIVERY. Reset progress is current state, not a replayable edge log.
// The supplied live-state contract suppresses overtaken revisions and clears each
// new generation. Require delivered progress, one retained rotation and an actual
// successor clear. Validate observed phases without demanding every transient
// revision. The successor's recall below independently proves the fresh handoff.
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
// CipherState. Runs in parallel through runParallel; liveHome keeps HOME out of the process environment. The reused
// setup skips cleanly (exit 0) when claude / creds are absent, exactly like every
// sibling stream spec; the actual green requires a live claude (needs-real-claude),
// and is read from the count of executed tests, never from the exit code.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/history"
	"os"
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
// siblings in internal/e2e/realclaude — so this file took the last two, 2 and 0, and
// clearSessUUID combines them rather than claiming a third. All three are valid
// UUIDv4 stems (version nibble 4, variant nibble 8) and all three match
// uuidStemPattern, which the transition's new_session_id is asserted against. Reuse
// across internal/e2e and internal/e2e/realclaude is harmless; the constraint is
// within the package.
//
// THE CONVERSATION IS BOUND TO clearSessUUID, NOT TO THE BOOTSTRAP, and that is what
// makes AC 2 reachable at all. The note is composed into a session's appended system
// prompt by Pool.writeComposedPrompt, which a rotation reaches through
// refreshSystemPromptForRotation — and that function RETURNS EARLY on a session whose
// systemPromptPath is empty. The bootstrap's is empty by construction:
// systemPromptPathFor hands the empty id the daemon-scoped <dataDir>/system-prompt.txt
// and states the reason, that the bootstrap "can never become a conversation's bound
// session" and so carries no per-conversation text. Its label is "" for the same
// reason, which handoffNoteFor independently refuses via conversations.ValidID. A
// conversation seeded onto the bootstrap therefore gets a reset that writes a note and
// a successor that is never composed one — the two halves of this case disagreeing
// with nothing on the wire saying so, because handoff="written" is a true report about
// the STORE and says nothing about any later compose.
//
// That is not hypothetical: the first two live runs of this case were seeded that way
// and both reddened on AC 2, the second with the successor answering that it had no
// access to prior sessions. assertPerSessionPrompt turns the mistake into an immediate
// failure that names itself, rather than into a recall miss a wrap-up later.
//
// clearSessUUID is deliberately ABSENT from the seeded sessions.json. An id the pool
// lacks is the daemon-restart shape, so the conversation's first message drives
// sessionRouter.revive → Pool.Revive → materialise → buildSession, which mints the
// session AT that id, with the conversation id as its label and a per-session prompt
// file under session-prompts/. That is the seam
// TestInteractiveConversationSystemPrompt_ReachesTheReply uses to put a composed
// prompt in front of a live claude, and the only one reachable without a write verb.
// The bootstrap is still seeded and still spawns — the daemon needs one — it simply is
// no longer what this conversation talks to.
const (
	clearBootstrapUUID = "22222222-2222-4222-8222-222222222222"
	clearSessUUID      = "00000000-2222-4222-8222-222222222222"
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
	runParallel(t)
	// Parallel only through runParallel; see liveHome.
	claudeBin, home := daemonProviderEnvironment(t)
	var err error

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
	// the daemon starts (the registry is loaded once at startup, no reload). The
	// binding names clearSessUUID, which sessions.json does NOT carry, so the first
	// message mints the conversation its own session — see the constants' doc for why
	// binding to the bootstrap instead makes AC 2 unreachable.
	seedBootstrapRegistry(t, home, clearBootstrapUUID)
	seedBoundConversation(t, home, clearConvID, clearSessUUID, workdir)

	d := spawnBootstrapDaemon(t, home, workdir, claudeBin, relayURL)
	t.Cleanup(func() {
		if t.Failed() {
			log := d.stderr.String()
			t.Logf("thread continuity diagnostic: baseline_required=%t producer_gap_retired=%t",
				strings.Contains(log, "event=daemon.thread.gap reason=baseline_required"),
				strings.Contains(log, "event=v2.push.producer_gap.teardown"))
		}
	})
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
	t.Cleanup(func() { finishDaemonRecording(t) })

	// A per-run nonce so reruns differ, defeating any cross-run caching that could
	// produce a false green. The token is contiguous uppercase hex — no internal
	// separators for claude to reformat — which is TestInteractiveStreamMultiTurnContinuity's
	// discipline for the same job.
	nonce := daemonRecordingNonce(t, time.Now().UnixNano())
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
	completedFacts1 := scenario.SourceSince(t, home, clearConvID)
	sealSendMessage(t, phone, initSend, plantReqID, clearConvID, "m-1",
		fmt.Sprintf("For the rest of this conversation, remember one fact about me: my build tag for this "+
			"run is %s. I have not written it down anywhere. Reply with just the word ok.", token))
	drainForCompletedTurn(t, phone, initRecv, clearConvID, perTurnReplyBudget, completedFacts1)

	// The precondition AC 2 rests on, checked deterministically and checked HERE. The
	// plant turn has just minted the conversation its own session; this asserts the
	// session is the kind that gets a composed prompt at all, seconds after the plant,
	// rather than leaving the answer to a recall miss on the far side of a
	// ninety-second wrap-up — where it reads like a claude fault and is not one. The
	// returned path is also the transition's previous_session_id anchor: the record it
	// comes from is the spawn of clearSessUUID, so finding it is what proves the revive
	// minted the session at the seeded id rather than taking some other one.
	promptPath := assertPerSessionPrompt(t, d, clearSessUUID, idSettleTimeout)
	idBefore := clearSessUUID
	// Daemon-authored, like the CAPTURE below: a path the daemon put in its own argv
	// record. Nothing claude wrote is in this line, and the note's bytes are not read.
	t.Logf("the conversation's session %s spawned with a per-session appended prompt at %q — "+
		"the compose the rotation recomposes into is this file", clearSessUUID, promptPath)

	// --- The /clear send: the window opens here --------------------------------
	// Ordinary send_message text, nothing else — the client verb, not the new_session
	// control frame. isClearCommand matches the first whitespace-delimited token
	// case-sensitively, and SendMessage's intercept sits below Route, so the id
	// crossing into the reset is this registry key with its live binding.
	w := newResetWindow()
	w.watchSource(t, home, clearConvID)
	lifecycleRecordingBeforeReset(t, home)
	sealSendMessage(t, phone, initSend, clearReqID, clearConvID, "m-clear", "/clear")

	// AC 1: three live reset edges and the retained rotation record.
	w.awaitReset(t, phone, initRecv, clearReqID, clearResetWindowBudget)

	w.assertProgress(t, clearConvID, protocol.ResetHandoffWritten)
	w.assertClear(t, clearConvID)
	tr := w.transitions[0]
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
	t.Logf("CAPTURE (#2485): reset progress=%+v, retained rotation=%+v, successor clears=%d",
		w.edges, tr.payload, len(w.clears))

	// --- AC 2: the note reached the successor ----------------------------------
	// The snapshot is taken BEFORE the send and names every turn THIS WINDOW has seen.
	// That is not every turn of the run: the window opens at the /clear send, so the
	// plant turn — drained to completion by drainForCompletedTurn, a different reader —
	// is not in it and does not need to be. What the window does hold is the
	// predecessor's wrap-up reply, which carries the planted fact by design, and a late
	// delta of that turn counted as the successor's is the one way this assertion could
	// pass vacuously.
	prior := w.snapshotTurnIDs()

	// THE PHRASING IS ALIGNED WITH handoffNoteLead, WHICH IS NOT WHAT WAS BROKEN. The
	// distinction is worth keeping, because the wrong half of it cost a live run.
	//
	// handoffNoteLead tells the successor to consult the note "when the user refers to
	// earlier work, or when you lack context the conversation seems to assume", and not
	// to act on it "unprompted". So the recall has to put the successor into one of
	// those two states, and naming the previous session while conceding the transcript
	// is empty does it — without naming the note and without supplying the token. That
	// is why the wording is what it is, and it should stay.
	//
	// It is NOT, however, why either of the first two live runs reddened. Run 1 asked
	// "Earlier in this conversation I told you my build tag" and got "I don't see a
	// build tag mentioned earlier in this conversation"; that reply was read as a
	// phrasing fault and the phrasing was repaired to what stands above. Run 2, with
	// the repair in, got "I don't have access to information from prior sessions or
	// conversations outside this transcript" — a second red, and the one that settled
	// it. The conversation was bound to the BOOTSTRAP session both times, so there was
	// no composed note behind either reply and no phrasing could have reached one. A
	// reply that is a correct answer to the question asked is weak evidence about the
	// question's wording when the thing being recalled is absent; the deterministic
	// check for absence is assertPerSessionPrompt, and it now runs before this send.
	//
	// The only place the tag exists for this child remains the note in its appended
	// system prompt, so a reply carrying it proves exactly what AC 2 asks.
	recallSource := scenario.SourceSince(t, home, clearConvID)
	sealSendMessage(t, phone, initSend, recallReqID, clearConvID, "m-2",
		"What is my build tag for this run? I told it to you before this session "+
			"started, so it is not in this transcript. Reply with only the tag "+
			"and nothing else.")
	reply := w.awaitRecallReply(t, phone, initRecv, clearConvID, recallReqID, prior, perTurnReplyBudget, recallSource)
	if !strings.Contains(strings.ToUpper(reply), token) {
		t.Fatalf("the successor's first reply does not carry the fact the predecessor was told. TWO READINGS, "+
			"and this test structurally cannot separate them: the reset reported handoff=%q, so a note WAS "+
			"stored — either the fact never entered it (the wrap-up prompt's last clause did not pull a fact "+
			"the user stated), or the successor did not use it (the note is composed into the appended system "+
			"prompt under handoffNoteLead, which tells the successor to consult it rather than obey it). "+
			"'The note was never composed into a prompt' is NOT a third reading HERE, and the exclusion is "+
			"earned rather than assumed: assertPerSessionPrompt already proved this conversation's session "+
			"spawned with a per-session file under %s/ (%q), which is the one precondition "+
			"refreshSystemPromptForRotation needs — it returns early only on an empty systemPromptPath, and a "+
			"minted session's is never empty. Above that guard, Pool.writeComposedPrompt targets the path "+
			"VERBATIM across a re-key and RotateForNewSession recomposes BEFORE it fires the transition this "+
			"test already observed. Without that guard the reading would be live, and it was: the first two "+
			"live runs bound this conversation to the BOOTSTRAP session, whose prompt file is daemon-scoped "+
			"and whose label is empty, so no compose could ever have happened. The note's bytes are not "+
			"observable from a client BY DESIGN — do not reach for the note file to tell the two readings "+
			"apart. Planted %q; reply was %q",
			protocol.ResetHandoffWritten, clearSessionPromptsDir, promptPath, token, reply)
	}
	t.Logf("AC 2: the successor's first reply (%d bytes) carried the planted fact, which this test never "+
		"repeated to it — the handoff note crossed the rotation (send_message #%d)", len(reply), recallReqID)

	// Current-state revisions may coalesce progress and clear each generation.
	// The durable rotation, rather than transient frame count, detects a second reset.
	w.assertProgress(t, clearConvID, protocol.ResetHandoffWritten)
	w.assertClear(t, clearConvID)
	if len(w.transitions) != 1 {
		t.Errorf("the source retained %d session_transition records across the whole window, want exactly 1: %+v — "+
			"more than 1 means something re-keyed a second time for the same reset",
			len(w.transitions), w.transitions)
	}
}

// clearSessionPromptsDir is the directory a MINTED session's appended system-prompt
// file lives in, transcribed from sessions.sessionPromptsDir, which is unexported.
// The bootstrap's file sits at the data dir's root instead, so the parent directory
// is the whole discriminator between the two.
const clearSessionPromptsDir = "session-prompts"

// assertPerSessionPrompt waits for the daemon's own "spawning claude" record for
// sessID and returns the --append-system-prompt-file path it names, failing unless
// that path is a per-session one under session-prompts/.
//
// IT IS THE DETERMINISTIC HALF OF AC 2's PROOF, and deliberately not a second
// assertion about claude. The recall below observes only whether a fact came back; it
// cannot separate a note that was never composed from one the successor declined to
// use, and the frames cannot either, because handoff="written" is a true report about
// the note STORE and says nothing about any later compose. This reads the daemon's own
// argv record instead and answers the one question the wire does not: whether this
// session is the kind that gets a composed prompt. systemPromptPathFor puts a minted
// session's file at <dataDir>/session-prompts/<id>.txt and the bootstrap's at
// <dataDir>/system-prompt.txt, so the parent directory settles it.
//
// Bounded rather than instant: the record is written when the child spawns and reaches
// this buffer through the daemon's piped stderr, so a poll is owed even though the
// plant turn's reply has already arrived.
func assertPerSessionPrompt(t *testing.T, d *bootstrapDaemon, sessID string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, rec := range sysPromptSpawnRecords(d.stderr.String()) {
			if !strings.Contains(rec, "--session-id "+sessID) {
				continue
			}
			path := sysPromptArgFromRecord(rec)
			if filepath.Base(filepath.Dir(path)) != clearSessionPromptsDir {
				t.Fatalf("the session bound to this conversation spawned with %s %q, which is not a per-session "+
					"file under %s/ — so it is the daemon-scoped bootstrap prompt and this conversation is "+
					"talking to the BOOTSTRAP session. refreshSystemPromptForRotation returns early on a session "+
					"whose systemPromptPath is empty, which the bootstrap's is by construction, so the reset "+
					"below would write a handoff note that is composed into nothing and AC 2 could not pass "+
					"however the recall is phrased. Check that seedBoundConversation names clearSessUUID and "+
					"that clearSessUUID is absent from the seeded sessions.json.",
					sysPromptFlag, path, clearSessionPromptsDir)
			}
			return path
		}
		if time.Now().After(deadline) {
			t.Fatalf("no \"spawning claude\" record names --session-id %s within %s — the conversation's own "+
				"session never spawned, so the revive seam did not fire and the note has no per-session prompt "+
				"to be composed into. seedBoundConversation must bind clearConvID to an id sessions.json does "+
				"NOT carry: an id the pool already holds is taken rather than minted.", sessID, timeout)
		}
		time.Sleep(25 * time.Millisecond)
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

// windowTransition retains one source boundary and counts actual live edges
// whose daemon timestamps precede its append. Source polling is not wire arrival.
type windowTransition struct {
	payload     protocol.SessionTransitionPayload
	edgesBefore int
	recordedAt  time.Time
}

// resetWindow is the state the reset window accumulates. Every frame between the
// `/clear` send and the recall turn's terminal turn_state{idle} passes through its
// next method, which is what makes the window-wide counts and the no-unrecognized_message
// negative structurally true rather than true of whatever a coarser drain happened
// not to have consumed already.
type resetWindow struct {
	receive     scenario.Next
	source      scenario.SourceFacts
	sourceConv  string
	sourceMark  uint64
	edgeTimes   []time.Time
	edgeFrames  []protocol.Envelope
	edges       []protocol.ResettingPayload
	clears      []protocol.Envelope
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

func (w *resetWindow) watchSource(t *testing.T, home, conv string) {
	w.source, w.sourceConv = scenario.SourceSnapshot(t, home), conv
	for _, entry := range w.source(conv) {
		w.sourceMark = entry.ID
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
		if w.receive == nil {
			w.receive = scenario.PollingReceive(t, phone, cs)
		}
		poll := time.Now().Add(100 * time.Millisecond)
		if deadline.Before(poll) {
			poll = deadline
		}
		env := w.receive(poll)
		if w.source != nil {
			for _, entry := range w.source(w.sourceConv) {
				if entry.ID > w.sourceMark {
					w.sourceMark = entry.ID
					switch entry.Type {
					case protocol.TypeUnrecognizedMessage:
						t.Fatal("reset produced unsupported source content")
					case protocol.TypeSessionTransition:
						var p protocol.SessionTransitionPayload
						if json.Unmarshal(entry.Payload, &p) != nil || p.ConversationID != w.sourceConv {
							t.Fatal("invalid reset source ownership")
						}
						w.transitions = append(w.transitions, windowTransition{payload: p, recordedAt: entry.TS})
					}
				}
				if entry.Type != protocol.TypeAssistantDelta || entry.Session == nil {
					continue
				}
				var p protocol.AssistantDeltaPayload
				if json.Unmarshal(entry.Payload, &p) != nil {
					t.Fatal("invalid reset source")
				}
				if p.ConversationID == w.sourceConv && p.TurnID != "" {
					w.turnIDs[entry.Session.SessionID+"\x00"+p.TurnID] = struct{}{}
				}
			}
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope inside the /clear window: %s", string(env.Payload))
		case protocol.TypeAck:
			if env.InReplyTo != nil {
				w.acked[*env.InReplyTo] = struct{}{}
			}
		case protocol.TypeResetting:
			if env.SessionStateCleared {
				var id string
				if json.Unmarshal(env.ConversationID, &id) != nil {
					t.Fatal("reset clear has invalid conversation metadata")
				}
				if id == w.sourceConv {
					w.clears = append(w.clears, env)
				}
				continue
			}
			var p protocol.ResettingPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode resetting payload: %v", err)
			}
			w.edges = append(w.edges, p)
			w.edgeTimes = append(w.edgeTimes, env.TS)
			w.edgeFrames = append(w.edgeFrames, env)
		case protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied, protocol.TypeTurnEnd, protocol.TypeSessionTransition, protocol.TypeModelRefusalFallback, protocol.TypeModelRefusalNoFallback, protocol.TypeCompactionBoundary, protocol.TypeUnrecognizedMessage:
			t.Fatal("retired raw content reached the reset window")
		}
		for i := range w.transitions {
			w.transitions[i].edgesBefore = 0
			for _, ts := range w.edgeTimes {
				if !ts.After(w.transitions[i].recordedAt) {
					w.transitions[i].edgesBefore++
				}
			}
		}
		return env, true
	}
}

// resetSettled requires an actual successor clear, never an initial or old-owner
// clear. Current singleton revisions may overtake intermediate progress phases.
func (w *resetWindow) resetSettled() bool {
	if len(w.transitions) != 1 || len(w.edges) == 0 {
		return false
	}
	tr := w.transitions[0].payload
	for _, clear := range w.clears {
		var conv, session string
		if clear.SessionStateCleared && json.Unmarshal(clear.ConversationID, &conv) == nil && conv == tr.ConversationID &&
			json.Unmarshal(clear.SessionID, &session) == nil && session == tr.NewSessionID && !clear.TS.Before(tr.OccurredAt) {
			return true
		}
	}
	return false
}

// awaitReset joins actual live progress and successor clearance with the retained
// rotation. Neither a source-only result nor a combined frame count can finish it.
func (w *resetWindow) awaitReset(t *testing.T, phone *fakephone.Client, cs *noise.CipherState,
	clearReqID uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !w.resetSettled() {
		if _, ok := w.next(t, phone, cs, deadline); !ok {
			_, acked := w.acked[clearReqID]
			t.Fatalf("reset did not deliver progress, one rotation and a successor clear within %s: active=%+v clears=%d transitions=%d acked=%v",
				timeout, w.edges, len(w.clears), len(w.transitions), acked)
		}
	}
}

func (w *resetWindow) assertClear(t *testing.T, conv string) {
	t.Helper()
	if !w.resetSettled() || w.transitions[0].payload.ConversationID != conv {
		t.Fatalf("reset lacks its owned successor clear: active=%d clears=%d transitions=%d", len(w.edges), len(w.clears), len(w.transitions))
	}
}

// Every delivered active phase remains truthful, even when later state overtakes
// a phase before delivery. Handoff content is independently proved by the caller.
func (w *resetWindow) assertProgress(t *testing.T, conv, handoff string) {
	t.Helper()
	if len(w.edges) == 0 || len(w.transitions) != 1 || len(w.edgeFrames) != len(w.edges) {
		t.Fatal("reset lacks observed progress or a unique rotation")
	}
	restarting := false
	for i, edge := range w.edges {
		var conversation, session string
		frame := w.edgeFrames[i]
		predecessor := w.transitions[0].payload.PreviousSessionID
		if json.Unmarshal(frame.ConversationID, &conversation) != nil || conversation != conv ||
			json.Unmarshal(frame.SessionID, &session) != nil || session == "" || predecessor == "" || session != predecessor {
			t.Fatal("active reset progress lacks predecessor ownership")
		}
		if edge.ConversationID != conv || !edge.Active || w.edgeTimes[i].After(w.transitions[0].payload.OccurredAt) {
			t.Fatalf("reset progress has wrong ownership or ordering: %+v", edge)
		}
		switch edge.Phase {
		case protocol.ResetPhaseWrappingUp:
			if restarting || edge.Handoff != protocol.ResetHandoffPending {
				t.Fatalf("invalid wrapping-up progress: %+v", edge)
			}
		case protocol.ResetPhaseRestarting:
			if edge.Handoff != handoff {
				t.Fatalf("invalid restarting outcome: %+v, want %q", edge, handoff)
			}
			restarting = true
		default:
			t.Fatalf("invalid reset phase: %+v", edge)
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

// awaitRecallReply requires the recall acknowledgement, fresh successor source
// identity and terminal live idle. The watermark excludes all pre-reset text.
func (w *resetWindow) awaitRecallReply(t *testing.T, phone *fakephone.Client, cs *noise.CipherState,
	convID string, reqID uint64, prior map[string]struct{}, timeout time.Duration, source scenario.SourceFacts) string {
	t.Helper()
	if len(w.transitions) != 1 {
		t.Fatal("recall requires one settled reset transition")
	}
	session := w.transitions[0].payload.NewSessionID
	checkedSource := func(conv string) []history.Entry {
		entries := source(conv)
		for _, e := range entries {
			switch e.Type {
			case protocol.TypeAssistantDelta, protocol.TypeToolUse, protocol.TypeToolResult, protocol.TypeToolDenied, protocol.TypeTurnEnd, protocol.TypeSessionTransition, protocol.TypeModelRefusalFallback, protocol.TypeModelRefusalNoFallback, protocol.TypeCompactionBoundary, protocol.TypeUnrecognizedMessage:
			default:
				continue
			}
			if e.Session == nil || e.Session.SessionID != session {
				t.Fatal("recall source belongs to the predecessor")
			}
			var p struct {
				Turn string `json:"turn_id"`
			}
			if json.Unmarshal(e.Payload, &p) != nil {
				t.Fatal("invalid recall source")
			}
			if _, stale := prior[session+"\x00"+p.Turn]; stale {
				t.Fatal("recall reused a predecessor source identity")
			}
		}
		return entries
	}
	sawAck := false
	next := func(deadline time.Time) protocol.Envelope {
		env, _ := w.next(t, phone, cs, deadline)
		if env.Type == protocol.TypeAck && env.InReplyTo != nil && *env.InReplyTo == reqID {
			sawAck = true
		}
		return env
	}
	text := scenario.CompletedTurn(t, next, convID, timeout, false, checkedSource)
	if !sawAck {
		t.Fatal("recall turn lost its matching acknowledgement")
	}
	return text
}
