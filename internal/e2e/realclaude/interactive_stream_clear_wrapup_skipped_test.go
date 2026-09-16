//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamClearWrapUpSkipped is the #2486 deliverable, and the inverse
// of the case #2485 shipped: the real-claude proof that a wrap-up which CANNOT FINISH
// inside its bound still completes the reset — the three `resetting` edges with
// `skipped` on the phase change, the `clear` transition — and leaves the conversation's
// stored handoff note exactly as it found it.
//
// It is the failure path of a feature whose happy path is already live, so the two
// cases are deliberately built to differ in ONE variable: the daemon's wrap-up bound.
// Everything else — the seeded bootstrap, the conversation minted its own session, the
// stream-json runner, the window drain — is the sibling's, reused rather than rewritten.
//
// THE BOUND HAS TO COME FROM THE SUITE, which is the production half of this ticket.
// wrapUpDeadline is ninety seconds and conversationReset.deadline, the seam a unit test
// crosses it with, was in-process only: a daemon this suite spawns always had the full
// ninety, which is longer than a live case should sit and is not a bound the case can
// force. -pyry-wrapup-deadline is that seam reaching a spawned daemon. It is a FLAG and
// not an environment variable on purpose: `make e2e-realclaude` is a plain
// `go test -tags e2e_realclaude`, so a case gated on a variable the target never sets
// skips on every run while the suite still exits 0 — the false green #1168 shipped an
// unverified change through.
//
// THE FLAG MAY ONLY SHORTEN, and that is this ticket's security finding rather than a
// convenience. wrapUpDeadline's doc fixes ninety seconds "not operator-editable …  an
// operator-supplied bound would be a way to hold a child open"; an uncapped override
// would repeal that sentence, since a reset in flight holds the outgoing child and
// conversationReset.begin refuses the second reset that might have recovered the
// conversation. conversationReset.bound clamps instead, so both sentences stand.
//
// WHERE THE EXPIRY ACTUALLY LANDS, measured rather than assumed, because the ticket
// forecast otherwise. With the conversation idle — the `/clear` intercept in relay's
// send_message handler sits above the enqueue and above touchConversation, so it opens
// no turn — an already-expired context does NOT stop the prompt being written:
// turnBusyTracker.WaitIdle tests membership before its select and returns nil at once
// when the conversation is not busy, and streamsup's WriteTurn reads the context only
// for the turncommit gate, which is nil on this path. So the expiry lands in the reply
// wait, logs reset.wrapup.deadline, and answers false.
//
// That makes the case MORE faithful, not less: against a live child the write cannot
// fail on a deadline, so the reply wait is the only place production's own ninety
// seconds can land either. The wrap-up prompt does reach claude and its turn is cut
// short by startFreshRunner's teardown milliseconds later. Nothing below asserts the
// step or the log record — every skipped exit converges on the same observable outcome,
// which is what the assertions name.
//
// THE THREE `resetting` EDGES ARE ASSERTED IN ORDER; the transition only to arrive
// AFTER the `restarting` edge, with nothing asserted between the transition and the
// falling edge. Transcribed from the sibling's measurement, which stands here
// unchanged: the edges are pushed synchronously from activeSessionStarter.resetThenRotate's
// own goroutine into a per-conn FIFO, while the transition reaches the client through
// sessionTransitionEmitterV2 — a non-blocking send into a buffer drained on that
// emitter's own goroutine. Do not make the emitter synchronous to buy a tidier
// assertion: the non-blocking hand-off is #659's requirement.
//
// WHY A NOTE IS SEEDED BEFORE THE DAEMON STARTS. "The previous note stands" is only
// assertable if there was one, and proving a note is still ABSENT is the weaker claim.
// Seeding also costs nothing at run time: the alternative is a second live wrap-up turn
// spent only to create the note this one must leave alone. The store is one file per
// conversation under a handoff-notes directory beside the sessions.json the suite
// already seeds; both that directory's name and the derivation (handoffNotePathFor) are
// unexported, so the path is composed here by hand from a compile-time constant id.
//
// SECURITY. The seeded note is this file's own literal, but it is still read back
// without being logged: the green path records a byte count and the verdict, and the
// bytes appear only inside a failure message — where the OBSERVED side may be claude's
// wrap-up reply if the store were wrongly overwritten, and a human is already reading.
// Nothing is written outside the test's isolated HOME and no capture is committed,
// #2138's posture kept for #2138's reason.
//
// The spine down to the handshake is the sibling's with no deltas. ONE reader for the
// whole run — the receive nonce is sequential, so a second concurrent reader would
// desync the CipherState. No t.Parallel: WithWorktreeAuthenticated calls t.Setenv. The
// setup skips cleanly (exit 0) when claude / creds are absent; the actual green requires
// a live claude (needs-real-claude) and is read from the count of executed tests, never
// from the exit code.

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Fixed identifiers for the seeded state. The package's repeated-digit stem convention
// was exhausted by the time #2485 landed — it took the last two — so these follow the
// package's OTHER convention, the ticket-numbered stem (20390000-…, 16560000-… and
// siblings). All three are valid UUIDv4 stems (version nibble 4, variant nibble 8),
// which conversations.ValidID is what makes load-bearing: handoffNotePathFor gates the
// note path on it, so an id of any other shape would fail the store rather than the
// assertion.
//
// THE CONVERSATION IS BOUND TO skipSessUUID, NOT TO THE BOOTSTRAP, and skipSessUUID is
// deliberately ABSENT from the seeded sessions.json — an id the pool lacks is the
// daemon-restart shape, so the conversation's first message drives the revive that mints
// a session AT that id. Unlike the sibling, this case does not NEED a per-session
// composed prompt: the note store is keyed by conversation, not by session, so AC 3
// holds either way, and no assertPerSessionPrompt guard is re-run here. What binding to
// its own session buys is that the two cases differ in exactly one variable, and that a
// reset driven by this file never touches the daemon's shared bootstrap.
const (
	skipBootstrapUUID = "24860000-0000-4000-8000-000000000001"
	skipSessUUID      = "24860000-0000-4000-8000-000000000002"
	skipConvID        = "24860000-0000-4000-8000-000000000003"
)

// handoffNotesDirName is the note directory's name under the daemon data dir,
// transcribed from sessions.handoffNotesDir, which is unexported. A SIBLING of
// session-prompts/ and session-settings/ rather than a tenant of either, which is the
// property that lets this file seed into it without disturbing what those two promise.
const handoffNotesDirName = "handoff-notes"

// skipSeededNote is the predecessor's note this case proves survives. Plain ASCII prose
// on purpose: it has to be admissible to sessions.FencedHandoffNote, because a seeded
// note is composed into the wrap-up prompt too (composeWrapUpPrompt), and an
// inadmissible one would silently take that function's other branch — leaving the case
// green while exercising a composition it did not mean to. No control characters, no
// fence marker, non-blank after trimming: the three things admissibleHandoffNote tests.
const skipSeededNote = "Working on the conversation reset's failure path.\n" +
	"State: the daemon is about to run a wrap-up under a bound it cannot meet, so no " +
	"successor note will be written and this one is expected to stand.\n" +
	"Next step: none. This note exists to be left alone.\n"

// wrapUpSkipBound is the bound the daemon is spawned with, and it makes the outcome
// STRUCTURAL rather than merely likely: the one way a wrap-up under it could reach
// storeNote is for claude to answer a whole turn within a millisecond of the routine
// starting, which no network round trip does. A bound in the seconds would trade that
// certainty for nothing — the case is not measuring how long claude takes.
const wrapUpSkipBound = 1 * time.Millisecond

// skippedResetWindowBudget bounds the whole reset, the `/clear` send to the last of the
// four frames. It INVERTS the sibling's argument rather than copying its number.
//
// clearResetWindowBudget has to stay well ABOVE wrapUpDeadline because the daemon's own
// ninety seconds sit inside the window it measures. Here they do not: the wrap-up is
// bounded at wrapUpSkipBound by construction, so what is left between the send and the
// falling edge is the rotation and the successor's spawn. Sized at 150 s a red would
// name nothing; sized here at rotateBudget plus slack, a window that misses it missed
// the rotation, which is the only thing still in it.
const skippedResetWindowBudget = 60 * time.Second

// skipWindowQuiet is how long the window is read on past the reset with nothing further
// expected. It is what keeps the window-wide counts below from being vacuous — awaitReset
// returns the instant the counts are met, so asserting them straight after would assert
// its own postcondition — and it is also what puts the killed wrap-up turn's tail and the
// successor's first frames through resetWindow.next's two standing negatives rather than
// leaving them unread. The successor has already spawned by then (the falling edge is
// pushed below startFreshRunner's return), so this waits on nothing and simply listens.
const skipWindowQuiet = 8 * time.Second

func TestInteractiveStreamClearWrapUpSkipped(t *testing.T) {
	// No t.Parallel: WithWorktreeAuthenticated calls t.Setenv.
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skipf("realclaude: claude not on PATH: %v", err)
	}
	home := WithWorktreeAuthenticated(t) // skips cleanly when no creds
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("realclaude: resolve claude: %v", err)
	}

	// Isolated workdir under the authenticated HOME: guarantees the fresh-daemon state
	// and sidesteps the shared-session-folder collision (#828) by construction.
	workdir := filepath.Join(home, "work")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("realclaude: mkdir workdir: %v", err)
	}

	// The stream-json interactive runner, flipped BEFORE the daemon spawns —
	// resolveConfigPath reads the config once at startup. conversationReset.wrapUp needs
	// a runner that captures a reply, and streamRunner is the only one that does. It is
	// needed even though this case's wrap-up never produces a note: without a capturer
	// wrapUp returns at reset.wrapup.no_capture, which is a DIFFERENT skipped exit and
	// would leave the bound untested.
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

	// Seeded BEFORE the daemon starts: both registries are loaded once at startup with
	// no reload, and the note has to predate the reset that must not disturb it.
	seedBootstrapRegistry(t, home, skipBootstrapUUID)
	seedBoundConversation(t, home, skipConvID, skipSessUUID, workdir)
	notePath := seedHandoffNote(t, home, skipConvID, skipSeededNote)

	d := spawnBootstrapDaemonWithWrapUpBound(t, home, workdir, claudeBin, relayURL, wrapUpSkipBound)
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

	var reqID uint64 = 2
	plantReqID := reqID
	reqID++
	clearReqID := reqID

	// --- Turn 1: a live bound child, which the reset is inert without ----------
	// activeSessionStarter.start refuses a NAMED conversation whose runner reports no
	// child (State().ChildPID == 0), and a refused reset emits NO EDGES AT ALL — so
	// without this turn every assertion below would fail for the wrong reason, and the
	// window would time out reporting a daemon fault that was really a missing
	// precondition. The content is irrelevant here: unlike the sibling, nothing of this
	// turn has to survive into a later reply.
	sealSendMessage(t, phone, initSend, plantReqID, skipConvID, "m-1",
		"Reply with just the word ok.")
	drainForCompletedTurn(t, phone, initRecv, skipConvID, perTurnReplyBudget)

	// --- The /clear send: the window opens here --------------------------------
	// Ordinary send_message text, the client verb rather than the new_session control
	// frame. isClearCommand matches the first whitespace-delimited token case-sensitively,
	// and the intercept sits below Route, so the id crossing into the reset is this
	// registry key with its live binding.
	w := newResetWindow()
	sealSendMessage(t, phone, initSend, clearReqID, skipConvID, "m-clear", "/clear")

	// --- AC 2: the four frames, with `skipped` on the phase change -------------
	w.awaitReset(t, phone, initRecv, clearReqID, skippedResetWindowBudget)

	assertResetEdge(t, "the first rising edge", w.edges[0], protocol.ResettingPayload{
		ConversationID: skipConvID,
		Active:         true,
		Phase:          protocol.ResetPhaseWrappingUp,
		Handoff:        protocol.ResetHandoffPending,
	})
	if w.edges[1].Handoff == protocol.ResetHandoffWritten {
		t.Fatalf("the reset reported handoff=%q on its phase change: the wrap-up produced a note DESPITE a "+
			"bound of %s, so this case proved nothing and AC 3 below is about to pass vacuously — the note it "+
			"finds unchanged would be the one just written. The readings, in the order worth checking: (a) the "+
			"daemon never saw -pyry-wrapup-deadline, which is what a missing pyryFlagValues entry does — the "+
			"flag and its value tip the argv split into claude territory and the daemon runs the full ninety "+
			"seconds; (b) conversationReset.bound stopped honouring a shorter deadline; (c) newConversationReset "+
			"no longer carries the flag's value into the field. Do NOT relax this assertion to accept the "+
			"token: #2485 owns the `written` proof and this case owns the other one.",
			w.edges[1].Handoff, wrapUpSkipBound)
	}
	assertResetEdge(t, "the phase change", w.edges[1], protocol.ResettingPayload{
		ConversationID: skipConvID,
		Active:         true,
		Phase:          protocol.ResetPhaseRestarting,
		Handoff:        protocol.ResetHandoffSkipped,
	})
	assertResetEdge(t, "the falling edge", w.edges[2], protocol.ResettingPayload{
		ConversationID: skipConvID,
		Active:         false,
		Phase:          "",
		Handoff:        "",
	})

	// The transition arrives AFTER the `restarting` edge and is asserted against nothing
	// else — see the header for why the falling edge is deliberately left unordered
	// against it. edges[1] is the phase change, so two edges having preceded the
	// transition is exactly that claim. The session ids are the sibling's assertion and
	// are not repeated: what this case adds is that the rotation still completes when the
	// wrap-up did not.
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
	if tr.payload.ConversationID != skipConvID {
		t.Errorf("session_transition conversation_id = %q, want the driving conversation %q",
			tr.payload.ConversationID, skipConvID)
	}

	// Read on with nothing further expected, so the counts below are over the whole
	// window rather than over awaitReset's own postcondition.
	settleResetWindow(t, w, phone, initRecv, skipWindowQuiet)

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

	// THE CAPTURE, and the whole of it is daemon-authored: the three edges carry tokens
	// selected from protocol's two closed sets, and the transition carries ids the daemon
	// assigned. Nothing claude wrote and no note byte is in this line. It goes in the
	// gate's log and from there into a ticket comment — never to disk.
	t.Logf("CAPTURE (#2486): a live /clear under -pyry-wrapup-deadline=%s ran the daemon's reset to "+
		"completion with the wrap-up abandoned — resetting{active=%v phase=%q handoff=%q} → "+
		"resetting{active=%v phase=%q handoff=%q} → session_transition{reason=%q, conversation_id=%q, "+
		"occurred_at=%s} (after %d edges) → resetting{active=%v phase=%q handoff=%q}",
		wrapUpSkipBound,
		w.edges[0].Active, w.edges[0].Phase, w.edges[0].Handoff,
		w.edges[1].Active, w.edges[1].Phase, w.edges[1].Handoff,
		tr.payload.Reason, tr.payload.ConversationID,
		tr.payload.OccurredAt.Format(time.RFC3339Nano), tr.edgesBefore,
		w.edges[2].Active, w.edges[2].Phase, w.edges[2].Handoff)

	// --- AC 3: the predecessor's note stands -----------------------------------
	// Read AFTER the falling edge and the quiet period, which is well past the only
	// point a note could have been stored: storeNote runs inside wrapUp, below the
	// `wrapping_up` edge and above the `restarting` one.
	got, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatalf("read the conversation's handoff note after the reset: %v — the file was seeded before the "+
			"daemon started and a skipped wrap-up must not remove it. An absent file is as much a failure as a "+
			"rewritten one: wrapUp's contract is that the PREVIOUS note stands, and nothing on the path is "+
			"permitted to unlink it", err)
	}
	if !bytes.Equal(got, []byte(skipSeededNote)) {
		t.Fatalf("the conversation's stored handoff note changed across a reset whose wrap-up reported "+
			"handoff=%q. Nothing may write the store on that path: every exit of conversationReset.wrapUp that "+
			"is not a completed write returns before storeNote is reached, which is what makes `skipped` and "+
			"`the previous note stands` the same fact. The observed bytes may be claude's wrap-up reply, which "+
			"is why they are printed here and nowhere else.\nseeded (%d bytes): %q\nfound  (%d bytes): %q",
			w.edges[1].Handoff, len(skipSeededNote), skipSeededNote, len(got), string(got))
	}
	t.Logf("AC 3: the conversation's stored handoff note is byte-identical across the reset (%d bytes, "+
		"unchanged) — the predecessor's note survived a wrap-up that could not finish", len(got))
}

// seedHandoffNote writes text as convID's stored handoff note under home's "test"
// instance and returns the path, so the case can read the same bytes back afterwards.
//
// THE PATH IS COMPOSED HERE because the daemon's derivation is not reachable:
// sessions.handoffNotePathFor and sessions.handoffNotesDir are both unexported. What is
// transcribed is the whole rule — <dataDir>/handoff-notes/<conversation id>.txt, where
// dataDir is the directory holding sessions.json — and it is a transcription rather than
// a guess, which is why handoffNotesDirName carries its own note about the derivation.
//
// The modes are the store's own rather than a test's convenience: 0700 on the directory
// and 0600 on the file, matching writeHandoffNoteFile, so a seeded fixture can never
// leave the note store in a weaker posture than the daemon would have created. convID is
// a compile-time constant of the shape conversations.ValidID admits, so nothing
// caller-controlled reaches the path join.
func seedHandoffNote(t *testing.T, home, convID, text string) string {
	t.Helper()
	dir := filepath.Join(home, ".pyry", "test", handoffNotesDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("seed handoff note: mkdir %s: %v", handoffNotesDirName, err)
	}
	path := filepath.Join(dir, convID+".txt")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("seed handoff note: write: %v", err)
	}
	return path
}

// settleResetWindow reads the window forward for quiet with nothing expected to arrive,
// so the caller's window-wide counts describe the whole window rather than the moment
// awaitReset stopped reading.
//
// It asserts nothing itself, and it does not need to: every frame passes through
// resetWindow.next, which carries the window's two standing negatives
// (unrecognized_message and a bare error envelope) and accumulates the edges and
// transitions the caller then counts. Reading through next rather than opening a reader
// of its own is also the receive-nonce discipline — a second concurrent reader would
// desync the CipherState, and the failure would surface as an unrelated decrypt error
// many frames later.
func settleResetWindow(t *testing.T, w *resetWindow, phone *fakephone.Client, cs *noise.CipherState,
	quiet time.Duration) {
	t.Helper()
	deadline := time.Now().Add(quiet)
	for {
		if _, ok := w.next(t, phone, cs, deadline); !ok {
			return
		}
	}
}

// spawnBootstrapDaemonWithWrapUpBound is spawnBootstrapDaemon with its sole delta being
// -pyry-wrapup-deadline=<bound> among the pyry flags, so this case can force a wrap-up
// the daemon cannot finish.
//
// A SELF-CONTAINED FORK rather than a widening of the shared helper, which is the
// discipline spawnBootstrapDaemonWithIdle's own doc argues for and the reason it is a
// fork too: a signature change to spawnBootstrapDaemon fans out to every live caller in
// a package `make check` never compiles, so a mistake there surfaces only as the whole
// live suite failing at once. Its variadic tail is not the seam either — it appends
// AFTER the `--`, where a pyry flag reaches claude instead of pyry.
//
// bound is a time.Duration and not a string, which is where this departs from the idle
// variant: the value is formatted here rather than at the call site, so an unparsable
// literal cannot be written by a caller at all. flag.Duration would reject one on the
// daemon side, but only after the spawn — as a failure to start, several seconds and one
// confusing error away from the mistake.
func spawnBootstrapDaemonWithWrapUpBound(t *testing.T, home, workdir, claudeBin, relayURL string,
	bound time.Duration) *bootstrapDaemon {
	t.Helper()
	bin := ensurePyryBuilt(t) // builds with real HOME (warm cache); runs with isolated HOME
	socket := shortSocketPath(t)
	stderr := &lockedBuffer{}

	args := []string{
		"-pyry-socket=" + socket,
		"-pyry-name=test",
		"-pyry-claude=" + claudeBin,
		"-pyry-idle-timeout=0",
		"-pyry-workdir=" + workdir,
		"-pyry-relay=" + relayURL,
		"-pyry-wrapup-deadline=" + bound.String(),
		"--",
		"--model", "haiku",
		"--dangerously-skip-permissions",
	}
	cmd := exec.Command(bin, args...)
	// os.Environ() already carries the isolated HOME and the credential
	// (WithWorktreeAuthenticated t.Setenv's both). Add the relay switches.
	cmd.Env = append(os.Environ(), "PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_MOBILE_V2=1")
	cmd.Stderr = io.MultiWriter(os.Stderr, stderr) // DEBUG tee

	if err := cmd.Start(); err != nil {
		t.Fatalf("realclaude: pyry start: %v", err)
	}
	doneCh := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(doneCh)
	}()

	d := &bootstrapDaemon{socketPath: socket, cmd: cmd, doneCh: doneCh, stderr: stderr}
	if err := d.waitForReady(10 * time.Second); err != nil {
		t.Fatalf("realclaude: daemon not ready: %v\nstderr:\n%s", err, stderr.String())
	}
	return d
}
