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
// internal/turnbridge (MapEvent's turnevent.Banner case) crosses all four values
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
// silently consumes everything before it, which makes exact banner counts and "no
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
// decrypt error many frames later. Runs in parallel through runParallel; liveHome keeps HOME out of
// the process environment. The harness skips cleanly (exit 0) when claude or creds are absent,
// exactly like every sibling stream spec; the actual green requires a live claude
// (needs-real-claude) and is read from the count of executed tests, never from the
// exit code.

import (
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
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
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
// About three times the 1.5 seconds the committed capture measured for the entire
// block phase, and the child is already up by the time it is waited on — the cold
// spawn was paid for by the banner wait, which takes perTurnReplyBudget for exactly
// that reason. It was 10 s, about seven times; the margin was cut because the
// window is paid in full on every green run.
const hookBannerRefusedSettleBudget = 5 * time.Second

func TestInteractiveStreamHookBlockedBannerReachesTheClient(t *testing.T) {
	runParallel(t)
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

	scenario.HookBanner(t, sharedHarnessWire(t, h, reqID), scenario.HookBannerConfig{Home: h.home, ConversationID: convID, BlockPrompt: oslcapBlockPrompt(nonce), LivePrompt: oslcapLivenessPrompt(nonce), TurnBudget: perTurnReplyBudget, SettleBudget: hookBannerRefusedSettleBudget, CheckWitness: func(passed bool) {
		verdicts := oslcapHookVerdicts(rig.WitnessPath)
		if len(verdicts) == 0 || verdicts[0] != oslcapHookBlocked {
			t.Fatal("hook witness did not begin blocked")
		}
		if passed && !slices.Contains(verdicts, oslcapHookPassed) {
			t.Fatal("unblocked prompt did not pass the hook")
		}
	}})
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
