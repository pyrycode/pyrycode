//go:build e2e_realclaude

package realclaude

// TestInteractiveStreamHookBlockedBannerReachesTheClient is the #2320 deliverable:
// the real-claude proof that a prompt a UserPromptSubmit hook REFUSES reaches the
// connected client as a banner carrying the refusal's reason, with the daemon in
// the path — and that the session still answers the next unblocked prompt, so a
// refusal is proven not to wedge the turn.
//
// WHY IT EXISTS. The whole chain is shipped and hermetically proven. #2256 minted
// the wire frame, #2319 gave it its first producer (emitInformationalBanner, over
// claude's system/informational subtype) and replayed that against the committed
// capture in testdata/operator_system_lines_v2.1.259.json, and the bridge arm in
// internal/turnbridge (mapEvent's turnevent.Banner case) crosses all four values
// verbatim. Every one of those is inside `make check`.
//
// What none of them touches is whether the frame reaches a CLIENT on a LIVE turn.
// The capture was recorded through the capture rig's own direct streamsup spawn,
// never through the daemon, and the replay drives the parser directly. This file
// is the rung that puts the daemon in the path: it spawns claude through a running
// pyry, refuses a prompt with a real hook, and reads the banner off an encrypted
// phone connection. No production change is expected. If the banner does not
// arrive, that is the finding — not a missing wire to add.
//
// A RED RUN HAS FOUR READINGS, and the hook witness is what separates them. The
// first two are not claims about the banner path at all:
//
//	(a) The hook never ran — hookVerdicts is empty. The child never registered
//	    the rig's hook. See WHERE THE HOOK LIVES below; the failure prints the
//	    daemon's own spawn argv, so what the child WAS handed is legible. Route
//	    back. It says nothing about the banner.
//	(b) The hook ran and PASSED the marked prompt — the rig failed to provoke the
//	    thing it meant to measure. Also route back.
//	(c) The hook ran and BLOCKED, and no banner arrived. That is the finding this
//	    file exists to produce: the refusal did not reach the client on the live
//	    path. Note that a CHANGED informational shape looks exactly like this and
//	    NOT like an unrecognized_message — emitInformationalBanner consumes the
//	    line either way and emits nothing when the decode fails, so a shape change
//	    is silence. Read the daemon's stderr for its "dropping undecodable system
//	    line" record before concluding the mapping regressed.
//	(d) The daemon regressed on the delivery path.
//
// WHERE THE HOOK LIVES, AND WHY IT IS NOT A --settings PASS-THROUGH. This file's
// first live run (2026-09-10) did pass the rig's settings file through the
// daemon's pass-through argv, and reddened with an EMPTY witness — the hook never
// ran. Two readings settle it, and neither is a guess. The spawn record the
// failure printed shows the child receiving --settings TWICE, the rig's first and
// the daemon's second: pyry composes its own MCP settings pair AFTER the
// operator's pass-through, at both Pool.New and buildSession. And claude 2.1.259
// registers --settings as a plain single-valued option with no accumulating
// parser, where --plugin-dir one entry along in the same option table registers
// one explicitly and advertises itself as repeatable. A repeated single-valued
// option is last-wins, so the daemon's file won and the rig's hook was never
// registered.
//
// A test whose child the DAEMON spawns therefore cannot reach that child through
// --settings at all: whatever it passes is shadowed by construction. So the hook
// goes into the harness-minted HOME's USER settings, at <home>/.claude/settings
// .json, which is a settings source in its own right rather than a competitor for
// the same flag. claude's own retention diagnostic calls that source "disabled
// (--setting-sources)", so the flag is what turns it OFF, and pyry passes no such
// flag. The two files conflict on no key — writeMCPSettings' payload is two
// booleans and declares no hooks — so the daemon keeps the keys it sets and the
// user file supplies the only hooks in play. Planting configuration under the
// minted HOME for a daemon-spawned child to read is the route
// TestInteractiveStreamSkillInvocationIsSilent already takes for a skill.
//
// Nothing is passed through #2320's pass-through seam as a result, and the seed
// callback returns nil. The seam is still the harness capability the ticket asks
// for, and the one a caller with a flag pyry does NOT compose would use; this
// file simply has no such flag left to pass. Passing the shadowed one anyway, to
// stay faithful to the design that failed, would assert nothing and would risk
// registering the same hook twice if a later claude ever did merge both
// occurrences. spawnBootstrapDaemon's header carries the same warning at source.
//
// WHY NEITHER SHARED DRAIN FITS. drainForCompletedTurn requires a non-empty
// assistant_delta before a terminal idle, and a hook-blocked turn carries no
// assistant line at all — the committed capture's block phase held system/init,
// the informational line, then result, and nothing else. It would Fatal at the
// first milestone. drainForControlEvent returns the first frame of a type and
// silently consumes everything before it, which makes "exactly one banner" and "no
// unrecognized_message" unassertable after the fact, and makes the NEGATIVE — the
// banner did not arrive — inexpressible. So the window gets one local primitive
// (hookBannerWindow.next) that every frame passes through, with two thin waits
// over it. That is #2138's shape and its header states the same reason.
//
// A REFUSED TURN HAS NO CLIENT-VISIBLE BOUNDARY, so turn 2 is sent on the BANNER.
// The first draft of this file waited for the refused turn's own terminal
// turn_state{idle} before sending the unblocked prompt, reasoning from the
// committed capture's block phase terminating on a `result`. The second live run
// (2026-09-10) reddened there and nowhere else: the banner arrived carrying the
// reason, the hook witness read `blocked`, and no idle followed within 60s. The
// answer is in production and it is deliberate. In interactiveTurnEmitterV2.Handle
// the turnevent.Banner arm mutates no turn lifecycle at all — its own comment says
// so, and names wedging as the reason — so a refusal never OPENS a turn on the
// wire; and that arm's turnevent.TurnEnd peer DROPS a turn end arriving outside an
// open turn, logging "interactive_turn.turn_end_no_turn" and emitting nothing. A
// refused turn therefore produces exactly one client-visible frame, the banner
// itself, whose stops_turn field is how the client learns the prompt was refused.
// There is no boundary to wait for, and this file asserts that absence rather than
// merely stepping around it.
//
// THE ABSENCE IS MEASURED WITH A SLEEP, NOT WITH A READ DEADLINE, and that is
// forced rather than chosen. The THIRD live run (2026-09-10) reddened one step
// further on: the banner arrived, the absence held for its whole budget, and then
// the unblocked prompt could not be SENT — "use of closed network connection". The
// reason is documented on the client itself. coder/websocket closes the underlying
// connection when a Read context is canceled, so fakephone.Client.Receive's doc
// says in as many words that a timed-out client cannot be reused. A vigil that ENDS
// in a read timeout therefore destroys the phone it is about to need, and every
// green run would have ended that way: the timeout is the success path of a
// negative. So the budget is spent ASLEEP, before the unblocked prompt is sent.
// Nothing reads during it, so whatever the daemon emits queues on the socket in
// arrival order and is read afterwards — necessarily ahead of turn 2's own ack,
// which the daemon cannot write until the send that follows the sleep reaches it.
// That is what makes "arrived before the ack" mean "belongs to the refused turn"
// over a full budget instead of over a millisecond race, and awaitCompletedTurn is
// where the two readings are named.
//
// So #2138's ordering IS inherited after all, for a different reason than its own.
// That file sent turn 2 on the session_transition because whether its `/clear` turn
// ever closed was the unknown under test; this one sends on the banner because the
// close it would otherwise wait for does not exist by design.
//
// AND THE REFUSAL STILL DOES NOT WEDGE THE NEXT SEND, which is what makes AC 1's
// second half provable without a boundary. The frame is suppressed but the EVENT is
// not: turnMarkFor answers turnMarkClose for turnevent.TurnEnd, so the same `result`
// closes turnBusyTracker's mark at the drain's fan-in, independently of whether the
// emitter had a turn open to close on the wire. And the stream path's own
// streamsup.Runner.WriteUserTurn refuses a turn for a dead child, an armed rotation
// or an unconfirmed permission posture, and never for a turn in progress. The first
// draft's failure text claimed the opposite — that a missing idle would hold the
// next send forever — and that claim was wrong on this path.
//
// EVERY LOG SITE THAT TOUCHES claude-AUTHORED TEXT USES %q, NEVER %s. banner.text
// is prose claude composes, and the frame's own doc in docs/protocol-mobile.md is
// explicit that newlines, terminal escapes, markup and text impersonating daemon
// chrome all fit inside its 4 KiB bound. t.Logf writes to the operator's terminal,
// so this test IS the render boundary § Security model's threat 1 puts the
// stripping obligation on, and %q renders every control byte as an escape instead
// of executing it. The success path logs a SUMMARY rather than the prose; the full
// text is logged only where an assertion has already failed and a human is
// reading. Nothing is written to disk — a committed capture of claude-authored
// bytes produced under the operator's real credentials would need this package's
// redaction apparatus, which is #2138's answer to the same question.
//
// The spine is the #997 per-conversation harness, whose seed callback is where
// the rig and its user settings are written before anything spawns.
// One daemon, one conversation created over the wire, one encrypted channel, ONE
// reader for the whole window — the receive nonce is sequential, so a second
// concurrent reader would desync the CipherState and surface as an unrelated
// decrypt error many frames later. No t.Parallel: WithWorktreeAuthenticated calls
// t.Setenv. The harness skips cleanly (exit 0) when claude or creds are absent,
// exactly like every sibling stream spec; the actual green requires a live claude
// (needs-real-claude) and is read from the count of executed tests, never from the
// exit code.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// hookBannerRigDir is the rig directory's name under the harness-minted HOME.
// Deliberately NOT oslcapRigDirName: that one belongs to the capture this file
// borrows the rig BUILDER from, and two live specs writing the same path under
// different HOMEs is a coincidence waiting to read as a shared fixture.
const hookBannerRigDir = "hookbanner-rig-2320"

// hookBannerRefusedSettleBudget bounds the window in which a turn-lifecycle frame
// for the REFUSED turn would have to appear and does not, which the header explains
// is production's designed behaviour rather than an accident to tolerate.
//
// It is spent ASLEEP rather than in a read, and the header gives the reason: a read
// whose deadline expires closes this phone for good, so a vigil cannot be followed
// by a send. Nothing reads during the sleep, so anything the daemon emits queues in
// arrival order and is read afterwards, ahead of the unblocked prompt's ack — which
// is the marker that attributes it to the refusal.
//
// It is spent in full on every green run, and that is the price of the negative
// rather than waste. An absence is evidence only if something waited for it; a
// snapshot taken the instant the banner was read would measure nothing but this
// file's own impatience.
//
// About seven times the 1.5 seconds the committed capture measured for the entire
// block phase, and the child is already up by the time it is waited on — the cold
// spawn was paid for by the banner wait, which takes perTurnReplyBudget for exactly
// that reason.
const hookBannerRefusedSettleBudget = 10 * time.Second

func TestInteractiveStreamHookBlockedBannerReachesTheClient(t *testing.T) {
	// The rig is written by the seed callback, which runs after the bootstrap
	// registry is seeded and BEFORE the daemon starts — the only point at which
	// the minted HOME exists and nothing has spawned yet. Both halves need that
	// window: the hook's own files, and the user settings file that registers it,
	// which has to be on disk before the first child reads its startup snapshot.
	var rig oslcapRig
	h := startPerConversationHarnessSeeded(t, func(home, workdir string) []string {
		var err error
		rig, err = oslcapWriteHookRig(filepath.Join(home, hookBannerRigDir))
		if err != nil {
			// Fatal rather than skipped, and load-bearing rather than
			// housekeeping: oslcapShellQuote REFUSES a value carrying a single
			// quote instead of sanitising it, so an error here can mean the
			// settings file names a script whose shell source was never written.
			// Continuing would spawn claude against a broken hook and read the
			// resulting silence as a finding about the banner.
			t.Fatalf("write the #2320 hook rig: %v", err)
		}
		// The rig builder's own settings file stays where it wrote it and is not
		// passed to anything — see the header. What the child reads is this copy
		// of the same JSON, installed as the HOME's user settings.
		if err := hookBannerInstallUserSettings(home, rig); err != nil {
			t.Fatalf("install the #2320 hook rig as user settings: %v", err)
		}
		// No extra pass-through claude arguments. The one flag this file wanted
		// to pass is shadowed by the daemon's own copy of it, so the seam has
		// nothing to carry here.
		return nil
	})

	// A per-run nonce so reruns differ, and so the marked prompt the hook matches
	// on is distinguishable between laps. reqID is a single monotonic counter;
	// only turn 2's is load-bearing (its ack correlates on InReplyTo).
	nonce := time.Now().UnixNano()
	var reqID uint64 = 2

	// Created over the wire, not seeded: this harness seeds no bound conversation.
	// Since #2085 the conversation's claude comes up on its FIRST message, so the
	// blocked prompt below is also the cold spawn — which is why its budget is the
	// full per-turn one.
	convID := createConversationViaPhone(t, h.phone, h.initSend, h.initRecv, reqID, nil)
	reqID++

	// --- The blocked send: the window opens here -------------------------------
	// oslcapBlockPrompt carries the rig marker the hook matches on. Its prose is
	// otherwise a trivial request, so a lap where the hook does NOT fire produces a
	// short ordinary turn rather than anything expensive — which is reading (b),
	// and the witness assertion below is what names it as such.
	var w hookBannerWindow
	blockReqID := reqID
	reqID++
	sealSendMessage(t, h.phone, h.initSend, blockReqID, convID, "m-blocked", oslcapBlockPrompt(nonce))

	// --- AC 1, first half: the refusal reaches the client ----------------------
	banner := w.awaitBanner(t, h, rig, perTurnReplyBudget)
	if banner.ConversationID != convID {
		t.Errorf("banner conversation_id = %q, want the driving conversation %q",
			banner.ConversationID, convID)
	}
	if !strings.Contains(banner.Text, oslcapBlockReason) {
		// The one place the prose itself is printed: the assertion has already
		// failed and a human is reading. %q, per this file's header.
		t.Errorf("the banner's text does not carry the hook's block reason %q — the frame arrived but the "+
			"refusal did not survive the path.\ntext (%d bytes): %q",
			oslcapBlockReason, len(banner.Text), banner.Text)
	}
	if banner.Truncated {
		t.Errorf("banner truncated = true for a %d-byte text, well under the producer's 4 KiB bound — either "+
			"claude's wrapper prose grew by three orders of magnitude or maxBannerText moved", len(banner.Text))
	}
	// level and stops_turn are LOGGED, not pinned. level is a documented open set
	// (docs/protocol-mobile.md § banner) and pinning it to the one observed value
	// would fail on a claude that legitimately picks another; stops_turn is
	// claude's own report, so asserting it would assert claude's choice rather
	// than the daemon's carriage of it. The capture is a summary — see the header.
	t.Logf("CAPTURE (#2320): a live claude's hook refusal reached the client as a banner — level=%q "+
		"text=%d bytes truncated=%v stops_turn=%v, and the text carries the hook's block reason. The daemon "+
		"was in the path: it spawned claude, parsed the system/informational line and delivered the frame",
		banner.Level, len(banner.Text), banner.Truncated, banner.StopsTurn)

	// --- The witness: which of the four readings this run is -------------------
	// Asserted on the GREEN path too, not only as a failure diagnostic. A banner
	// that arrived without the hook having blocked would mean the frame came from
	// something other than the refusal under test, and the whole assertion above
	// would be about a line this file did not provoke.
	verdicts := oslcapHookVerdicts(rig.WitnessPath)
	if len(verdicts) == 0 || verdicts[0] != oslcapHookBlocked {
		t.Fatalf("the hook witness reads %q, want it to open with %q. The rig did not refuse the marked "+
			"prompt, so whatever reached the client above was not this file's subject. An EMPTY witness means "+
			"the hook never ran at all — see the header's WHERE THE HOOK LIVES, and the spawn argv below.\n%s",
			verdicts, oslcapHookBlocked, hookBannerSpawnArgv(h))
	}

	// --- AC 1, second half: the session still answers the next prompt ----------
	// No wait for the refused turn to close, because it produces no client-visible
	// boundary — by production design, for the reason the header gives. The absence
	// is ASSERTED rather than stepped around: the refusal is given a full budget to
	// emit something with nothing reading, and awaitCompletedTurn below attributes
	// anything that arrives before turn 2's ack to it. Turn 2 is then sent on the
	// banner: #2138's ordering after all, arrived at for a different reason than
	// #2138's own.
	letRefusedTurnSettle(t, hookBannerRefusedSettleBudget)

	liveReqID := reqID
	reqID++
	sealSendMessage(t, h.phone, h.initSend, liveReqID, convID, "m-live", oslcapLivenessPrompt(nonce))
	w.awaitCompletedTurn(t, h, convID, liveReqID, perTurnReplyBudget)

	// The unblocked prompt must have PASSED the hook, which is what makes the
	// reply above evidence that the refusal was prompt-scoped rather than evidence
	// that the hook stopped running.
	// CONTAINS a pass, rather than a pass at index 1. The property is that the
	// unblocked prompt reached the hook and the hook let it through; pinning the
	// position would additionally assert how many times claude submits a prompt,
	// which is claude's business and not measured. The first entry is already
	// pinned to `blocked` above, so any pass here is one that followed it.
	verdicts = oslcapHookVerdicts(rig.WitnessPath)
	if !slices.Contains(verdicts, oslcapHookPassed) {
		t.Errorf("the hook witness reads %q and holds no %q — the unblocked prompt never passed the hook, so "+
			"its reply does not show that a refusal is prompt-scoped rather than session-wide",
			verdicts, oslcapHookPassed)
	}

	// --- Exactly one banner across the whole window ----------------------------
	// Over EVERY frame from the blocked send to turn 2's terminal frame, because
	// all of them passed through next — not over whatever a coarser drain had not
	// already discarded. More than one means the informational arm fired twice for
	// one refusal, or a second producer of turnevent.Banner landed (#2258 owns the
	// notification subtype and would be exactly that).
	if len(w.banners) != 1 {
		t.Fatalf("the client saw %d banner frames between the blocked send and the liveness turn's terminal "+
			"frame, want exactly 1 — a second means the informational arm fired twice for one refusal, or a "+
			"second producer of turnevent.Banner reached this path", len(w.banners))
	}
	t.Logf("AC 1: the session answered the next unblocked prompt after the refusal (send_message #%d), and "+
		"exactly one banner crossed the window", liveReqID)
}

// hookBannerInstallUserSettings writes the rig's settings JSON into the minted
// HOME as claude's USER settings — the only settings source a test can reach on a
// child the DAEMON spawns, for the reason the header gives: every --settings the
// pass-through carries is shadowed by the copy pyry appends after it.
//
// The bytes are the rig builder's own rather than a second rendering of the same
// shape. oslcapSettingsJSON is pinned by TestOslcapSettingsFileIsTheShapeClaudeReads,
// which runs offline but NOT inside `make check`: this whole package sits behind the
// e2e_realclaude tag, so the hermetic gate never compiles it and the pin holds only
// under `make e2e-realclaude` or `make preship`. A hand-copied shape here would be
// pinned by nothing at all and would drift the first time claude's hooks schema moved.
//
// Modes are set with an explicit Chmod after the write rather than left to
// os.WriteFile's perm argument, which the runner's umask masks — oslcapWriteHookRig's
// discipline, for its reason: this file names a script claude runs AS THE OPERATOR,
// so its integrity is the property that matters, and a mode that depends on the
// umask is a mode no test can pin.
//
// An existing file is an error, never an overwrite, and O_EXCL states that as one
// operation rather than as a stat this code then acts on. Nothing else in this
// harness writes <home>/.claude/settings.json today — WithWorktreeAuthenticated
// seeds .claude.json, which is a different file — so one already there would mean a
// second writer had appeared and this rig would be competing with it for the
// child's hooks rather than supplying the only ones in play.
func hookBannerInstallUserSettings(home string, rig oslcapRig) error {
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the user settings dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod the user settings dir: %w", err)
	}
	path := filepath.Join(dir, "settings.json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists; this harness has grown a second writer of claude's user "+
				"settings and the hook rig would be competing with it", path)
		}
		return fmt.Errorf("create the user settings file: %w", err)
	}
	if _, err := f.WriteString(rig.Settings); err != nil {
		_ = f.Close()
		return fmt.Errorf("write the user settings file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close the user settings file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod the user settings file: %w", err)
	}
	return nil
}

// hookBannerSpawnArgv renders the daemon's own "spawning claude" records, which is
// what makes a red run's cause readable rather than guessable. Each record is the
// argv pyry handed one child, and what a reader checks in it is that the rig's
// settings file is ABSENT and that the single --settings there is the daemon's
// own — i.e. that this run took the user-settings route the header describes, so
// an empty witness is a hook that did not register rather than a pass-through the
// daemon shadowed. That shadowing is what the first live run measured.
//
// Only the extracted records are rendered, never h.daemon.stderr's whole buffer:
// that is the daemon's entire captured stderr, the harness already tees it to
// os.Stderr, and duplicating an unbounded unfiltered haystack into a failure
// message buys no diagnosis. %q for the same reason every other log site here uses
// it — a record embeds claude-side paths and the daemon's own formatting.
func hookBannerSpawnArgv(h *perConvHarness) string {
	records := sysPromptSpawnRecords(h.daemon.stderr.String())
	if len(records) == 0 {
		return "The daemon logged no \"spawning claude\" record at all, so no child was spawned through it."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The daemon spawned %d child/children; each record is the argv it handed one of them, "+
		"and the rig's settings file should appear in NONE of them:", len(records))
	for _, rec := range records {
		fmt.Fprintf(&b, "\n  %q", rec)
	}
	return b.String()
}

// --- the window drain (the only genuinely new code) --------------------------

// hookBannerWindow is the state the hook-refusal window accumulates. Every frame
// between the blocked send and the liveness turn's terminal turn_state{idle}
// passes through its next method, which is what makes "exactly one banner"
// structurally true over the whole window rather than true of whatever a coarser
// drain happened not to have consumed already.
type hookBannerWindow struct {
	banners []protocol.BannerPayload

	// readTimedOut records that a receive deadline expired, which on this client is
	// terminal rather than recoverable — see next's guard.
	readTimedOut bool
}

// next reads one binary→phone envelope, applies the window-wide negatives, records
// every banner, and returns the envelope for the caller's own state machine. It
// reports false when deadline passes with nothing more read.
//
// Banners are the only frames tallied here. turn_state is deliberately NOT counted,
// and the one wait that cares about one decodes the frame itself: a tally cannot say
// whether THIS frame is the one, which awaitCompletedTurn needs both in order not to
// let a leading `responding` close its turn and in order to name the state of a frame
// that reached the refused turn.
//
// A RECEIVE TIMEOUT IS TERMINAL, which is why the timeout branch below records it and
// the guard at the top refuses to read on. coder/websocket closes the connection under
// a canceled Read context, so a client that has timed out once is gone for every
// later read AND every later send. The next reader would see "use of closed network
// connection" many lines from the cause. This file's third live run lost a whole lap
// to exactly that, which is why the knowledge sits in code here rather than in prose.
//
// Receive-nonce discipline: every noise_msg is decrypted in arrival order and
// non-noise_msg control frames (e.g. rekey) are skipped WITHOUT decrypting, since
// they do not advance the nonce. There is exactly one reader for the whole window
// — a second concurrent one would desync the CipherState, and the failure would
// surface as an unrelated decrypt error many frames later.
//
// The negatives are Fatal rather than returned, deliberately: neither is a
// condition any caller could do something better with, and folding them in here is
// what lets the three waits below stay thin.
func (w *hookBannerWindow) next(t *testing.T, h *perConvHarness,
	deadline time.Time) (protocol.Envelope, bool) {
	t.Helper()
	for {
		if w.readTimedOut {
			t.Fatalf("the hook-refusal window tried to read past a receive timeout, and this phone has been " +
				"unusable since: coder/websocket closes the connection under a canceled Read context, so " +
				"fakephone.Client's own doc says a timed-out client cannot be reused. Whatever this read " +
				"was for is unmeasurable now. To measure an ABSENCE, sleep before the next send the way " +
				"letRefusedTurnSettle does — never read to a deadline you expect to expire")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return protocol.Envelope{}, false
		}
		raw, err := h.phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				w.readTimedOut = true
				continue // re-loop into the deadline check above
			}
			t.Fatalf("phone receive (hook-refusal window): %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame (hook-refusal window): %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue // does not advance the receive nonce — skip without decrypting
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data (hook-refusal window): %v", err)
		}
		plain, err := h.initRecv.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope (hook-refusal window): %v", err)
		}
		switch env.Type {
		case protocol.TypeUnrecognizedMessage:
			// NOT the arm a changed informational shape lands in, and saying so is
			// the point of this comment. emitInformationalBanner consumes the line
			// on every path and emits NOTHING when the decode fails, so a changed
			// shape is silence and shows up as a missing banner. What lands here is
			// the ordinary known-ignored-list staleness this suite's shared drain
			// also watches for: claude grew a message type streamsup's
			// ignoredLineTypes has not measured.
			var p protocol.UnrecognizedMessagePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode unrecognized_message payload: %v", err)
			}
			t.Fatalf("the hook-refusal window produced an unrecognized_message: site=%q type=%q truncated=%v\n"+
				"raw: %q\n\n"+
				"claude grew a message type streamsup.ignoredLineTypes has not measured. That is a parser "+
				"change and a NEW ticket, not this one. It is NOT how a changed system/informational shape "+
				"would surface — that arm drops the line silently, so it would fail as a missing banner "+
				"instead.", p.Site, p.MessageType, p.Truncated, p.Raw)
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope inside the hook-refusal window: %q", string(env.Payload))
		case protocol.TypeBanner:
			var p protocol.BannerPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode banner payload: %v", err)
			}
			w.banners = append(w.banners, p)
		}
		return env, true
	}
}

// awaitBanner reads the window forward to its first banner and returns that
// payload. Every frame before it — the blocked send's own ack, any turn_state —
// is read through next, so it is counted and checked rather than discarded.
//
// The timeout's failure text is where readings (a) through (d) are made
// decidable, because a missing banner is the shape three of the four take.
func (w *hookBannerWindow) awaitBanner(t *testing.T, h *perConvHarness,
	rig oslcapRig, timeout time.Duration) protocol.BannerPayload {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		env, ok := w.next(t, h, deadline)
		if !ok {
			verdicts := oslcapHookVerdicts(rig.WitnessPath)
			t.Fatalf("no banner reached the client within %s of the refused send. The hook witness reads %q, "+
				"and it decides which of these this is:\n"+
				"  EMPTY — the hook never ran, so the child did not register it from <HOME>/.claude/"+
				"settings.json. Either claude stopped loading user settings on a spawn carrying its own "+
				"--settings, or it stopped running UserPromptSubmit hooks from that source. Says NOTHING "+
				"about the banner path, and it is NOT the --settings shadowing the first live run measured "+
				"— this file passes no --settings, and the argv below shows it. Route back; the follow-up is "+
				"a ticket about how a test rig reaches a daemon-spawned child's hooks at all.\n"+
				"  [%s …] — the rig failed to provoke a refusal at all. Route back.\n"+
				"  [%s …] — THE FINDING: claude refused the prompt and the refusal did not reach the client. "+
				"Before concluding the mapping regressed, read the daemon's stderr for a \"dropping "+
				"undecodable system line\" record — emitInformationalBanner drops a line whose shape changed "+
				"and emits nothing, which looks identical from here.\n%s",
				timeout, verdicts, oslcapHookPassed, oslcapHookBlocked, hookBannerSpawnArgv(h))
		}
		if env.Type == protocol.TypeBanner {
			// next already decoded and recorded it; the last one recorded is this.
			return w.banners[len(w.banners)-1]
		}
	}
}

// letRefusedTurnSettle spends the settle budget with NOTHING reading the wire, which
// is how this file makes the negative the header states measurable: a refused turn
// produces no turn-lifecycle frame and no assistant text, because the banner arm
// opens no turn and the turn end that follows is dropped outside one.
//
// Sleeping is the mechanism, not an accident of convenience. Reading the wire to a
// deadline that is MEANT to expire — the obvious shape, and this file's first two
// drafts — closes the phone: coder/websocket closes the connection under a canceled
// Read context, so fakephone.Client cannot be reused after a receive timeout, and
// the unblocked prompt that follows could not be sent at all. Nothing is lost by
// sleeping instead. The socket buffers, so every frame the daemon emits during the
// budget is still read afterwards, in order, ahead of the ack for a send that had
// not yet happened — which is precisely what lets awaitCompletedTurn attribute a
// pre-ack frame to the refusal rather than to the turn that follows it.
//
// The assertions themselves therefore live in awaitCompletedTurn, one arm per
// reading, both Errorf rather than Fatalf: neither makes AC 1's second half
// unprovable, since the unblocked prompt can still be sent and answered whether or
// not the refused turn grew a frame it should not have.
func letRefusedTurnSettle(t *testing.T, timeout time.Duration) {
	t.Helper()
	time.Sleep(timeout)
}

// awaitCompletedTurn reads the window forward through the unblocked turn: the ack
// correlated on reqID, then a non-empty assistant_delta for convID, then the
// terminal turn_state{idle}. The three are ordered — an idle before the delta is
// ignored — which keeps this turn's own leading `responding` from closing it early.
// That mirrors drainForCompletedTurn's guard.
//
// It also carries the REFUSED turn's negative, because this is where that turn's
// frames would finally be read. Everything the daemon emitted during the settle
// budget queued on the socket while nothing read, and the ack below cannot have been
// written until after that budget elapsed and the next prompt reached the daemon. So
// anything for convID that arrives BEFORE the ack belongs to the refusal, and each
// of the two shapes it could take gets its own arm and its own reading. On the green
// path there is nothing before the ack at all, and the ack arm is where that is
// captured.
//
// No content assertion: real claude's words are non-deterministic, and asserting
// them would risk the substrate guard. The property under test is that a turn
// AFTER a refusal completes at all.
func (w *hookBannerWindow) awaitCompletedTurn(t *testing.T, h *perConvHarness, convID string,
	reqID uint64, timeout time.Duration) {
	t.Helper()
	sawAck, sawDelta := false, false
	refusalFrames := 0
	deadline := time.Now().Add(timeout)
	for {
		env, ok := w.next(t, h, deadline)
		if !ok {
			switch {
			case !sawAck:
				t.Fatalf("no ack for send_message #%d within %s of the refusal — the unblocked prompt never "+
					"routed. The refused turn wedged the conversation, which is exactly what this half of "+
					"AC 1 exists to catch: the delivery seam never found %q idle, meaning "+
					"turnBusyTracker's close feed did not fire on the refused turn's own `result` even "+
					"though turnMarkFor answers turnMarkClose for it — or the child died on the refusal",
					reqID, timeout, convID)
			case !sawDelta:
				t.Fatalf("send_message #%d was acked but no non-empty assistant_delta for %q reached the "+
					"client within %s — the turn routed and produced nothing. The hook passed this prompt "+
					"(no marker), so a refusal is not the explanation; the child came up for the refused "+
					"turn and did not survive it, or the reply stream never bound", reqID, convID, timeout)
			default:
				t.Fatalf("the post-refusal turn for %q streamed a delta but never closed with a terminal "+
					"turn_state{idle} within %s", convID, timeout)
			}
		}
		switch env.Type {
		case protocol.TypeAck:
			if env.InReplyTo != nil && *env.InReplyTo == reqID {
				sawAck = true
				if refusalFrames == 0 {
					t.Logf("CAPTURE (#2320): the refused turn produced no turn_state and no assistant text "+
						"for %q in the %s it was given before the next prompt was even sent. The refusal's "+
						"only client-visible frame is the banner itself, and its stops_turn field is how "+
						"the client learns the prompt was refused",
						convID, hookBannerRefusedSettleBudget)
				}
				t.Logf("the session accepted the unblocked prompt after the refusal (ack for send_message #%d)",
					reqID)
			}
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if p.ConversationID != convID || strings.TrimSpace(p.Text) == "" {
				continue
			}
			if !sawAck {
				// The refused turn's first reading. Errorf, so the liveness half is
				// still measured on the same run.
				refusalFrames++
				t.Errorf("a non-empty assistant_delta for %q reached the client BEFORE the unblocked prompt "+
					"was acked, so claude ANSWERED the prompt its own hook refused. This whole file's "+
					"premise has moved: a refused turn now carries assistant text, the banner is no longer "+
					"its only client-visible frame, and drainForCompletedTurn would fit the turn after all. "+
					"That is a new ticket about what a refusal means, not a fault in this one", convID)
				continue
			}
			if !sawDelta {
				sawDelta = true
				t.Logf("non-empty assistant_delta (turn_id=%s seq=%d, %d bytes) for %q after the refusal",
					p.TurnID, p.Seq, len(p.Text), convID)
			}
		case protocol.TypeTurnState:
			// Decoded here rather than tallied in next, which is the reason next
			// tallies no turn_state at all: a count cannot say whether THIS frame is
			// the idle, so gating on one an earlier frame had advanced would let the
			// next turn_state of ANY state — a `responding`, say — close this turn
			// early.
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			if st.ConversationID != convID {
				continue
			}
			if !sawAck {
				// The refused turn's second reading, and the one production is most
				// explicit about. Errorf for the same reason as the delta arm above.
				refusalFrames++
				t.Errorf("a turn_state of %q reached the client for %q BEFORE the unblocked prompt was "+
					"acked, so it belongs to the REFUSED turn — which production says cannot happen: "+
					"interactiveTurnEmitterV2.Handle's turnevent.Banner arm mutates no turn lifecycle, and "+
					"its turnevent.TurnEnd peer drops a turn end that arrives outside an open turn. A turn "+
					"OPENED on this path: the banner arm grew a startTurnIfNeeded, or a second producer "+
					"reached it. That is the wedge that arm's comment exists to prevent, since a producer "+
					"whose turn is never answered is followed by no turn end to close it", st.State, convID)
				continue
			}
			if !sawDelta {
				continue // the leading responding state this turn opens with
			}
			if st.State == "idle" {
				t.Logf("terminal turn_state{idle} for %q — the post-refusal turn closed", convID)
				return
			}
		}
	}
}
