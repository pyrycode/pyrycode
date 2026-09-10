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
//	(a) The hook never ran — hookVerdicts is empty. Either claude did not honour
//	    the rig's --settings, or it honoured a DIFFERENT one. See THE SHADOWING
//	    RISK below; the failure prints the daemon's own spawn argv so the two are
//	    distinguishable. Route back. It says nothing about the banner.
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
// THE SHADOWING RISK, which is the one non-obvious hazard in this file and the
// reason the hook witness is asserted at all. The rig's --settings reaches claude
// through the daemon's pass-through argv (spawnBootstrapDaemon's #2320 tail), and
// pyry composes its OWN --settings after it: both Pool.New and buildSession append
// their MCP settings pair to the operator's pass-through, so what claude receives
// carries the flag twice, the rig's first. claude declares --settings as a
// single-valued option, so which one wins is claude's precedence to decide and is
// UNMEASURED here. If the daemon's wins, the rig's hook never registers and the
// run reds as reading (a) rather than as a false (c). The two files conflict on no
// key — writeMCPSettings' payload is two booleans and declares no hooks — so
// whichever wins, nothing else about the spawn moves, and both of those booleans
// suppress PTY-path startup dialogs this stream-json path does not raise.
//
// WHY NEITHER SHARED DRAIN FITS. drainForCompletedTurn requires a non-empty
// assistant_delta before a terminal idle, and a hook-blocked turn carries no
// assistant line at all — the committed capture's block phase held system/init,
// the informational line, then result, and nothing else. It would Fatal at the
// first milestone. drainForControlEvent returns the first frame of a type and
// silently consumes everything before it, which makes "exactly one banner" and "no
// unrecognized_message" unassertable after the fact, and makes the NEGATIVE — the
// banner did not arrive — inexpressible. So the window gets one local primitive
// (hookBannerWindow.next) that every frame passes through, with three thin waits
// over it. That is #2138's shape and its header states the same reason.
//
// WHAT IS DELIBERATELY NOT INHERITED FROM #2138: its ordering workaround. That
// file had to send turn 2 on the session_transition, because whether its `/clear`
// turn ever CLOSED was the unknown under test. Here the capture shows the blocked
// phase terminating on a `result`, so the turn closes on its own and the unblocked
// follow-up has an ordinary turn boundary to wait for. Nothing here needs to race
// a transition.
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
// The spine is the #997 per-conversation harness with #2320's pass-through seam.
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

// hookBannerTurnCloseBudget bounds the wait for the blocked turn's own terminal
// frame. Generous by two orders of magnitude on purpose: the committed capture
// measured that phase at about 1.5 seconds, and by the time it is waited on the
// child is already up — the cold spawn was paid for by the banner wait, which
// takes perTurnReplyBudget for exactly that reason.
const hookBannerTurnCloseBudget = 60 * time.Second

func TestInteractiveStreamHookBlockedBannerReachesTheClient(t *testing.T) {
	// The rig is written by the seed callback, which runs after the bootstrap
	// registry is seeded and BEFORE the daemon starts — the only point at which
	// the minted HOME exists and nothing has spawned yet. Its return value is the
	// pass-through argv (#2320's seam), which is why the settings path does not
	// have to be knowable at this call: it is not, and cannot be.
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
		return []string{"--settings", rig.SettingsPath}
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
	banner := w.awaitBanner(t, h, convID, rig, perTurnReplyBudget)
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
			"the hook never ran at all — see the header's shadowing risk, and the spawn argv below.\n%s",
			verdicts, oslcapHookBlocked, hookBannerSpawnArgv(h))
	}

	// --- AC 1, second half: the turn closes, then the session still answers -----
	// The blocked turn closes on its own — the committed capture's block phase
	// terminated on a `result` — so this waits for the ordinary boundary rather
	// than racing it. #2138 could not, and its header says why.
	w.awaitTurnIdle(t, h, convID, hookBannerTurnCloseBudget)

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

// hookBannerSpawnArgv renders the daemon's own "spawning claude" records, which is
// what makes the header's shadowing risk decidable from a failure rather than
// guessable. Each record carries the argv pyry handed one child, so the number and
// ORDER of --settings occurrences in it says whether the rig's file was the
// earlier of two.
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
	fmt.Fprintf(&b, "The daemon spawned %d child/children; each record's argv decides the shadowing "+
		"question (count the --settings occurrences and read their order):", len(records))
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
	// idles counts terminal turn_state{idle} frames for the driving conversation.
	// A count rather than a flag because awaitTurnIdle and awaitCompletedTurn each
	// wait for one and must not be satisfied by the other's.
	idles int
}

// next reads one binary→phone envelope, applies the window-wide negatives, records
// the frames the waits below key on, and returns the envelope for the caller's own
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
// what lets the three waits below stay thin.
func (w *hookBannerWindow) next(t *testing.T, h *perConvHarness, convID string,
	deadline time.Time) (protocol.Envelope, bool) {
	t.Helper()
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return protocol.Envelope{}, false
		}
		raw, err := h.phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
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
		case protocol.TypeTurnState:
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			if st.State == "idle" && st.ConversationID == convID {
				w.idles++
			}
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
func (w *hookBannerWindow) awaitBanner(t *testing.T, h *perConvHarness, convID string,
	rig oslcapRig, timeout time.Duration) protocol.BannerPayload {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		env, ok := w.next(t, h, convID, deadline)
		if !ok {
			verdicts := oslcapHookVerdicts(rig.WitnessPath)
			t.Fatalf("no banner reached the client within %s of the refused send. The hook witness reads %q, "+
				"and it decides which of these this is:\n"+
				"  EMPTY — the hook never ran. Either claude did not honour the rig's --settings or it "+
				"honoured a different one; pyry composes its own --settings AFTER the pass-through, so the "+
				"rig's is the earlier of two occurrences of a single-valued flag. Says NOTHING about the "+
				"banner path. Route back.\n"+
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

// awaitTurnIdle reads the window forward to a terminal turn_state{idle} for convID
// — the blocked turn closing on its own, which the committed capture's `result`
// says it does.
//
// It returns immediately when one was ALREADY recorded before this call, which is
// not a weakening: the assertion is that the refused turn produced a terminal idle
// for this conversation, and where in the window that frame landed relative to the
// banner does not change it. Reading forward for a second one instead would wedge
// on an ordering claude is under no obligation to keep, and the delivery seam
// makes the wedge pointless anyway — turnBusyTracker holds a second message until
// the conversation has no open turn, so the next send is safe either way.
func (w *hookBannerWindow) awaitTurnIdle(t *testing.T, h *perConvHarness, convID string, timeout time.Duration) {
	t.Helper()
	if w.idles > 0 {
		t.Logf("the refused turn for %q had already closed with a terminal turn_state{idle} by the time the "+
			"banner was read", convID)
		return
	}
	deadline := time.Now().Add(timeout)
	for {
		if _, ok := w.next(t, h, convID, deadline); !ok {
			t.Fatalf("the refused turn for %q never closed with a terminal turn_state{idle} within %s. The "+
				"committed capture's block phase terminated on a `result`, so a blocked turn closes on its "+
				"own — either claude stopped emitting one for a refused prompt, or the daemon stopped "+
				"closing the turn on it. Either way the next send would be held by the delivery seam "+
				"forever, so this is the failure to fix rather than to wait out", convID, timeout)
		}
		if w.idles > 0 {
			t.Logf("terminal turn_state{idle} for %q — the refused turn closed on its own", convID)
			return
		}
	}
}

// awaitCompletedTurn reads the window forward through the unblocked turn: the ack
// correlated on reqID, then a non-empty assistant_delta for convID, then the
// terminal turn_state{idle}. The three are ordered — a delta before the ack is
// ignored, an idle before the delta is ignored — which keeps a leading responding
// state, and the refused turn's own idle, from closing this one early. That
// mirrors drainForCompletedTurn's guard.
//
// No content assertion: real claude's words are non-deterministic, and asserting
// them would risk the substrate guard. The property under test is that a turn
// AFTER a refusal completes at all.
func (w *hookBannerWindow) awaitCompletedTurn(t *testing.T, h *perConvHarness, convID string,
	reqID uint64, timeout time.Duration) {
	t.Helper()
	sawAck, sawDelta := false, false
	deadline := time.Now().Add(timeout)
	for {
		env, ok := w.next(t, h, convID, deadline)
		if !ok {
			switch {
			case !sawAck:
				t.Fatalf("no ack for send_message #%d within %s of the refusal — the unblocked prompt never "+
					"routed. The refused turn wedged the conversation, which is exactly what this half of "+
					"AC 1 exists to catch: the delivery seam never found %q idle, or the child died on the "+
					"refusal", reqID, timeout, convID)
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
				t.Logf("the session accepted the unblocked prompt after the refusal (ack for send_message #%d)",
					reqID)
			}
		case protocol.TypeAssistantDelta:
			if !sawAck {
				continue // anything before the ack belongs to the refused turn
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
				t.Logf("non-empty assistant_delta (turn_id=%s seq=%d, %d bytes) for %q after the refusal",
					p.TurnID, p.Seq, len(p.Text), convID)
			}
		case protocol.TypeTurnState:
			if !sawDelta {
				continue // the leading responding state, and the refused turn's own close
			}
			// Decoded again here rather than read off next's idle COUNTER, and the
			// difference is load-bearing: the counter cannot say whether THIS frame
			// is the idle. Gating on a counter that a frame earlier in the window
			// had already advanced would let the next turn_state of any state —
			// a `responding`, say — close this turn early.
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			if st.State == "idle" && st.ConversationID == convID {
				t.Logf("terminal turn_state{idle} for %q — the post-refusal turn closed", convID)
				return
			}
		}
	}
}
