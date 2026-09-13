//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Four tickets built the connect-time background-task roster in layers — the
// per-session retention (#2077), the reconcile (#2078), the enumeration seam (#2079)
// and this proof. This is the proof of the CHAIN across process boundaries for the
// client the reconcile exists FOR: one that was not connected when claude reported the
// roster. A hold filled from the wrong sink, a closure built over the wrong registry, a
// capability gate that never opens, an envelope dropped before it is sealed — every one
// of those leaves the unit tests green.
//
// TWO PRODUCERS, WHICH IS WHY THE MUTANT IS THE ACCEPTANCE CRITERION. The live turn
// lane emits background_task_roster whenever claude changes the roster, and
// reconcileBackgroundTaskRosters emits the SAME variant on a handshake tail. A run that
// merely observed a frame could therefore have observed the live lane's and would pass
// against a completely dead reconcile. What separates them is the one-line mutant —
// leaving the RetainedBackgroundTaskRosters field of the V2 session config unset, on
// which reconcileBackgroundTaskRosters returns early on its nil guard — plus the count
// of frames on the MINTING conn, which is logged into every failure message here for
// exactly that reason: a miss then names WHICH of the two producers failed rather than
// leaving that to be guessed. The mutation is run via `go test -overlay` and never
// committed.
//
// THE LIVE LANE MUST NOT REACH THE OBSERVER, and the sequencing is what guarantees it:
// the turn that produces the roster is complete before phone-b dials, so the only path
// left to phone-b's socket is its own handshake tail. If this test ever needs a turn to
// run while the observing client is connected it has stopped proving the reconcile.
//
// A CONVERSATION MUST EXIST FIRST. retainedBackgroundTaskRosters enumerates the
// CONVERSATION registry, and the pool's bootstrap session has no conversation record at
// all. A daemon that has minted nothing therefore retains nothing this path can see, and
// a test built on the bootstrap child alone would wait out its whole deadline for a
// frame that is correctly never sent. An over-the-wire create_conversation mints the
// record; since #2085 it does NOT spawn, so the FIRST TURN below is what produces the
// child whose mid-turn roster line is retained.
//
// WHY THE EMPTY-REGISTRY ARM CANNOT FLAKE, argued rather than assumed, because a stray
// roster on that conn would redden AC 4 for a reason having nothing to do with the seam.
// Two independent facts prevent one: the rider fires per USER TURN and no turn is driven
// before that window closes, so nothing has been reported to any child yet; and the
// bootstrap session carries no conversation record, so the enumeration could not see a
// roster even if one were somehow held. A change that breaks either should show up here
// as a broken argument, not as a flake.
//
// WHAT THIS TEST IS EVIDENCE OF, AND WHAT IT IS NOT — three non-claims:
//
//   - THREE paired devices are a sequencing device, NOT a confinement proof. The late
//     observer reads a conversation minted by a DIFFERENT device, and that is the
//     enumerate-all seam working as designed: RetainedBackgroundTaskRosters takes no
//     argument, so a paired interactive conn sees every retained roster. Per-device
//     confinement is out of scope for the whole Mode B family and belongs to its
//     umbrella (#829), not to this test.
//   - The capability gate's REFUSED arm is not exercised. Only the granted arm is; the
//     refusal is pinned at the unit seam, where a negative costs no deadline. The
//     reconciled frame's absent event_id and the explicit empty-roster snapshot are
//     pinned there too, and deliberately get no arm here.
//   - dropped_tasks is asserted NON-ZERO, and that is a pass-through claim, not a cap
//     test. The cut is streamsup's (maxTaskRosterEntries, 8) and is pinned in its own
//     package; what is proven here is that the number survives four layers. A fixture
//     whose dropped_tasks were always 0 could not tell "carried" from "never populated",
//     which is the whole reason the rider is driven OVER the cap.
//
// NO DECODED PAYLOAD IS EVER PRINTED, on any path. All four strings a task row carries
// — task_id, task_type, description and every truncated_fields entry — are
// claude-authored untrusted text, and description is a literal command line for the
// local_bash task type (#833). Deadline diagnostics report counts and ids only, and the
// empty-registry and settle windows report a COUNT rather than the frame they did not
// expect. The row failures name a got and a want because both are fakeclaude's own
// canned literals — that is what makes the shape safe HERE and is exactly why it must
// not be copied into internal/e2e/realclaude, where the same payload carries the
// operator's real workspace. No pairing token is echoed on any failure path either.
const (
	// Distinct from every sibling spec's bootstrap UUID: they run in the same package
	// and a shared literal would read as a shared fixture it is not.
	lateRosterBootstrapUUID = "20800000-0000-4000-8000-000000000001"
	lateRosterUserText      = "e2e-late-roster:hello\n"
	lateRosterEchoNeedle    = "e2e-late-roster:hello"
	lateRosterCreateReqID   = uint64(2080)
	lateRosterSendReqID     = uint64(2081)

	// The roster rider's knob and the count it is driven at. Nine is one over
	// streamsup's eight-entry roster cap, which is what makes dropped_tasks non-zero —
	// see the header's third non-claim.
	rosterRiderEnv    = "PYRY_FAKE_CLAUDE_STREAM_ROSTER"
	rosterRiderTasks  = 9
	rosterWantTasks   = 8 // streamsup's maxTaskRosterEntries
	rosterWantDropped = rosterRiderTasks - rosterWantTasks

	// The capture's own row, which the rider writes as row 0. Duplicated as literals
	// because the fake is a separate main package this file cannot import (the same
	// discipline the rate-limit and bogus needles follow). The description is a literal
	// shell command line, asserted verbatim precisely to show the path carries it as
	// inert bytes.
	rosterCapturedTaskID      = "bybi8g8i8"
	rosterCapturedTaskType    = "local_bash"
	rosterCapturedDescription = "cat $FIFO"
	rosterSyntheticIDPrefix   = "e2e-roster-task-"
)

// lateRosterObservation is what one empty-registry handshake, one wire-minted
// conversation, one driven turn and one LATER interactive handshake put on three
// clients.
//
// The milestones are VALUES rather than helper-side assertions: a frame count read off a
// run where the turn never completed proves nothing, so the caller asserts them first
// and fatally, and a helper that fataled on them would leave those checks dead code.
type lateRosterObservation struct {
	// AC 4: frames on a conn that handshook while the daemon had retained nothing.
	emptyConnRosters int
	convID           string // the server-minted id the frame must be stamped with
	sawEcho          bool   // milestone: the minted child replied
	// milestone: the turn closed ⇒ the roster line was already through the parser
	sawTurnEnd bool
	// AC 2's diagnostic half, logged into every failure message and never asserted.
	// This frame's live-lane emit is MID-TURN rather than at spawn, so unlike the
	// spawn-time twins (#1868/#2009, whose minter counters are a genuine unordered
	// race) it should reliably be at least 1 — the cursor is stamped by the routing of
	// the very turn that produces the line. It stays a diagnostic anyway: asserting it
	// would redden this spec for a change in a lane it does not own. Its VALUE is what
	// tells the two producers apart on a miss — a red with minter>0 and observer=0 is
	// the reconcile, a red with both 0 is the rider or the retention.
	rostersOnMinter int
	roster          protocol.BackgroundTaskRosterPayload
	found           bool
	// AC 2's cardinality half: further frames on the observer after the first. Exactly
	// one conversation is retained, so exactly one envelope is pushed — a second would
	// mean a duplicated registry row or the bootstrap session leaking into the
	// enumeration, and a first-arrival-only wait could not redden for either.
	extraOnObserver int
	// AC 3: turn_state and turn_end frames on the observer, counted together. The
	// reconciled frame must drive no turn, and the observer sends nothing that could.
	turnFramesOnObserver int
}

// driveLateConnectBackgroundTaskRoster starts a stream-interactive daemon whose
// fakeclaude runs the roster rider, observes an interactive handshake against an EMPTY
// conversation registry, then mints a conversation over the wire from phone-a and drives
// one turn on it so a roster is reported and retained, THEN dials phone-b and handshakes
// it interactive, and collects what phone-b's own handshake puts on its socket. phone-b
// sends nothing but that handshake.
//
// THREE CONNS, AND THE THIRD ONE IS FORCED. Proving an absence means running a window to
// its deadline, and coder/websocket closes the underlying connection when a read context
// is cancelled (fakephone.Client.ReceiveBytes' own doc), so a timed-out client cannot be
// reused. The conn that proves AC 4 is therefore DEAD after its own window and cannot
// also be the minter. Its seal-and-send closure is discarded on the floor for that
// reason — as phone-b's is for a different one — so a later edit that tries to reuse
// either conn fails to compile rather than failing as a decrypt error that reads like a
// daemon bug.
//
// THE HAPPENS-BEFORE THAT REPLACES A SLEEP. fakeclaude's runStreamJSON is a single loop
// that writes every reply on the goroutine that read the line, in order, and the roster
// rider writes its line BEFORE that turn's echo and result. The daemon's parser consumes
// those lines in order on one goroutine, and the retention is a sink decorator on that
// same chain. Therefore turn_end observed on phone-a ⇒ the roster line's work is
// complete ⇒ the hold holds the roster ⇒ retainedBackgroundTaskRosters will enumerate it
// for the next handshake. That argument is about two different LINES read in order, so
// it holds whichever side of the decorator's delegation the record happens on. No
// time.Sleep and no retry loop belongs anywhere in this test; the two bounded windows
// that do exist are not polls but single drains whose result is a count.
//
// It asserts none of the acceptance criteria. It t.Fatalf's only on transport, seal and
// decode faults, on an error envelope, and on the mint never completing — never on an
// acceptance criterion. On the turn or collection deadline it returns what it has and
// logs the counts, leaving the diagnosis to the caller.
func driveLateConnectBackgroundTaskRoster(t *testing.T) lateRosterObservation {
	t.Helper()

	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	// THREE devices, all paired BEFORE the daemon starts — the daemon loads its
	// registries once at startup. paireddevice.Setup's repeat contract establishes
	// that repeated setup calls into one home yield independently usable devices.
	names := []string{"phone-empty", "phone-a", "phone-b"}
	tokens := make([]string, 0, len(names))
	var staticPub string
	for _, name := range names {
		p, err := paireddevice.Setup(paireddevice.Config{
			Home:                   home,
			InstanceName:           "test",
			Relay:                  relayURL,
			DeviceName:             name,
			AllowRemotePermissions: false,
		})
		if err != nil {
			t.Fatalf("setup paired device %s: %v", name, err)
		}
		tokens = append(tokens, p.Token)
		// The fixture mints a token per setup, but the static key is the DAEMON's and is
		// identical in all three payloads — so the last write wins harmlessly. The key
		// is public; the tokens are not, and nothing below ever prints one.
		staticPub = p.ServerStaticPubkey
	}
	pubKey, err := base64.StdEncoding.DecodeString(staticPub)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// No seedBoundConversation: the conversation observed here is MINTED over the wire
	// below, so its binding is created by create_conversation rather than seeded. A
	// seeded row would bind a conversation to the BOOTSTRAP session, which would both
	// make the empty-registry window vacuous and point the enumeration at a child that
	// never runs a turn and so never reports a roster.
	// The ONE piece of harness scaffolding this spec adds, and the part that does not
	// transfer from the slash-command twin: that frame rides the initialize control
	// reply, which fakeclaude answers under every rider, while a roster has a different
	// source entirely — claude's mid-turn system/background_tasks_changed line, which
	// nothing in the fake produced before #2080.
	h := StartStreamInteractiveWithRelay(t, home, lateRosterBootstrapUUID, relayURL,
		fmt.Sprintf("%s=%d", rosterRiderEnv, rosterRiderTasks))
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	// One factory for all three conns. Each gets its OWN CipherState pair: the receive
	// nonce is a lockstep counter, so sharing one across phones — or filtering before
	// decrypting — desyncs it and presents as a misleading decrypt error rather than as
	// a clean failure. Pairing each phone with its own states at one call site makes
	// that structural. Interactive is load-bearing rather than incidental: the fan-out
	// and the reconcile both gate on the capability, so a non-interactive handshake
	// yields zero frames and a vacuously red test that looks exactly like a missing
	// seam. The token is passed, never printed.
	dialInteractive := func(name, token string) (func(protocol.Envelope), func(time.Time) (protocol.Envelope, bool)) {
		t.Helper()
		dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, token, name)
		if err != nil {
			t.Fatalf("%s dial: %v", name, err)
		}
		t.Cleanup(func() { _ = phone.Close() })
		send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, token)
		return sealedConnDriver(t, phone, name, send, recv)
	}

	var obs lateRosterObservation

	// --- AC 4: a conn that handshakes while the daemon has retained NOTHING. Nothing
	// has been minted and no turn has run, so retainedBackgroundTaskRosters enumerates
	// nothing and reconcileBackgroundTaskRosters takes its len(retained) == 0 return.
	// The handshake completing normally is discharged by
	// driveHandshakeToOpenDaemonInteractive having RETURNED at all — it fatals on a
	// non-noise_resp inner frame, on a hello_ack that will not decode, and on a missing
	// interactive grant, so reaching this line is the whole of that half.
	//
	// The window is short on purpose: the reconcile's pushes happen synchronously on
	// the handshake's success tail, after noise_resp is sent, so a frame that is coming
	// at all is milliseconds away rather than seconds. This conn is dead once the
	// window times out and is never touched again.
	_, nextEnvEmpty := dialInteractive("phone-empty", tokens[0])
	emptyDeadline := time.Now().Add(2 * time.Second)
	for {
		env, ok := nextEnvEmpty(emptyDeadline)
		if !ok {
			break
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope on phone-empty: %s", string(env.Payload))
		case protocol.TypeBackgroundTaskRoster:
			// COUNTED, never decoded and never printed. This is the one path in the
			// file with no legitimate reason to hold an inventory, and the natural
			// spelling of its failure would echo a list of command lines.
			obs.emptyConnRosters++
		}
	}

	sealSendA, nextEnvA := dialInteractive("phone-a", tokens[1])

	// --- Mint the conversation over the wire. All-null create_conversation (server
	// defaults). Drained through this file's own nextEnv rather than through
	// createConversationViaPhone because that helper discards every non-matching frame,
	// and rostersOnMinter has to count across BOTH of phone-a's windows for the
	// diagnostic to mean anything.
	sealSendA(protocol.Envelope{
		ID:      lateRosterCreateReqID,
		Type:    protocol.TypeCreateConversation,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.CreateConversationPayload{}),
	})
	createDeadline := time.Now().Add(15 * time.Second)
	for obs.convID == "" {
		env, ok := nextEnvA(createDeadline)
		if !ok {
			t.Fatalf("no conversation_created within 15s (background_task_roster on the minter: %d) — the "+
				"conversation was never minted. Since #2085 the mint does not spawn, so suspect "+
				"the child rather than the reconcile seam", obs.rostersOnMinter)
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope while awaiting conversation_created: %s", string(env.Payload))
		case protocol.TypeBackgroundTaskRoster:
			obs.rostersOnMinter++
		case protocol.TypeConversationCreated:
			var p protocol.ConversationCreatedPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode conversation_created payload: %v", err)
			}
			if p.ID == "" {
				t.Fatal("conversation_created carried an empty id")
			}
			obs.convID = p.ID
		}
	}
	t.Logf("minted conversation %s (its own runner + child)", obs.convID)

	// --- Drive one turn on the minted conversation and wait for turn_end. This is what
	// makes the roster EXIST: the rider writes its line per user turn, so without a turn
	// nothing is ever reported and the retention stays empty. It is also the
	// SYNCHRONISATION — turn_end is the terminator rather than a frame count, so an
	// interleaved broadcast cannot make it flaky.
	sealSendA(protocol.Envelope{
		ID:   lateRosterSendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: obs.convID,
			MessageID:      "m-late-roster-1",
			Text:           lateRosterUserText,
		}),
	})
	turnDeadline := time.Now().Add(30 * time.Second)
	for !obs.sawTurnEnd {
		env, ok := nextEnvA(turnDeadline)
		if !ok {
			t.Logf("turn window deadline reached: conv=%s echo=%v turn_end=%v background_task_roster on the "+
				"minter=%d", obs.convID, obs.sawEcho, obs.sawTurnEnd, obs.rostersOnMinter)
			break
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope during the turn: %s", string(env.Payload))
		case protocol.TypeBackgroundTaskRoster:
			obs.rostersOnMinter++
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if strings.Contains(p.Text, lateRosterEchoNeedle) {
				obs.sawEcho = true
			}
		case protocol.TypeTurnEnd:
			obs.sawTurnEnd = true
		}
	}

	// --- phone-b connects only NOW, after the roster exists and the turn that produced
	// it has closed. Its seal-and-send closure is deliberately DISCARDED: it sends
	// nothing but its handshake, and dropping the closure on the floor makes that
	// structural rather than a promise in a comment. Whatever the daemon sends phone-a
	// from here on buffers unread on phone-a's socket, so the windows never interleave
	// and this stays one goroutine.
	//
	// handleNoiseInit's success tail records s.interactive, creates the push queue and
	// calls reconcileBackgroundTaskRosters, which pushes one envelope per retained
	// payload — and it sends noise_resp BEFORE those pushes, which is what makes
	// phone-b's decrypt order deterministic. driveHandshakeToOpenDaemonInteractive
	// consumed exactly that noise_resp and nothing after it, so this loop starts on a
	// nonce that is in sequence.
	_, nextEnvB := dialInteractive("phone-b", tokens[2])
	collectDeadline := time.Now().Add(20 * time.Second)
	for !obs.found {
		env, ok := nextEnvB(collectDeadline)
		if !ok {
			t.Logf("phone-b collection deadline reached: conv=%s background_task_roster on the minter=%d",
				obs.convID, obs.rostersOnMinter)
			break
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope on phone-b: %s", string(env.Payload))
		case protocol.TypeTurnState, protocol.TypeTurnEnd:
			obs.turnFramesOnObserver++
		case protocol.TypeBackgroundTaskRoster:
			if err := json.Unmarshal(env.Payload, &obs.roster); err != nil {
				// A decode failure at this layer IS the defect, not a miss. The raw
				// bytes are deliberately NOT printed: they carry the whole roster.
				t.Fatalf("decode background_task_roster payload: %v", err)
			}
			obs.found = true
		}
	}

	// --- The settle window, which is what makes the counts above EXACT claims rather
	// than at-least-ones. Only reachable when the loop above ended on a frame: had it
	// ended on its deadline, phone-b's client would already be closed. Short for the
	// empty-registry window's reason — every envelope this handshake produces was
	// pushed before noise_resp's successors, so a second one is milliseconds away or
	// not coming.
	if obs.found {
		settleDeadline := time.Now().Add(2 * time.Second)
		for {
			env, ok := nextEnvB(settleDeadline)
			if !ok {
				break
			}
			switch env.Type {
			case protocol.TypeError:
				t.Fatalf("unexpected error envelope on phone-b after the first roster: %s", string(env.Payload))
			case protocol.TypeTurnState, protocol.TypeTurnEnd:
				obs.turnFramesOnObserver++
			case protocol.TypeBackgroundTaskRoster:
				obs.extraOnObserver++ // counted, never decoded, never printed
			}
		}
	}
	return obs
}

// TestRelayV2_StreamBackgroundTaskRosterReachesLateConnectingPhone is the hermetic
// end-to-end proof that the background-task roster claude reports mid-turn reaches a
// client that connected AFTER the daemon obtained it, without that client asking for
// anything — with every delivered row intact, the dropped-task count carried, no turn
// opened or closed on the receiving conn, and nothing at all sent to a conn that
// connected before there was anything to send.
//
// Every failure of this test is a timeout somewhere and they are hard to tell apart from
// a bare "want 1, got 0", so each message names its own suspect and carries the
// minter-side count.
func TestRelayV2_StreamBackgroundTaskRosterReachesLateConnectingPhone(t *testing.T) {
	obs := driveLateConnectBackgroundTaskRoster(t)

	// AC 4, first: it is the only claim still interpretable if everything downstream
	// fails, since it is complete before the first conversation is minted. Its
	// "handshake completes normally" half was discharged inside the driver — reaching
	// any assertion at all means driveHandshakeToOpenDaemonInteractive returned rather
	// than fataling on the noise_resp, the hello_ack decode or a missing interactive
	// grant. The count is reported without the payload on purpose.
	if obs.emptyConnRosters != 0 {
		t.Errorf("background_task_roster on the empty-registry conn: got %d, want 0 — a conn that handshook "+
			"while the daemon had retained nothing was unicast a roster anyway. "+
			"reconcileBackgroundTaskRosters returns on len(retained) == 0, so suspect "+
			"retainedBackgroundTaskRosters enumerating a conversation that should not exist yet: a seeded "+
			"conversations.json would bind one to the BOOTSTRAP session", obs.emptyConnRosters)
	}

	// Milestones next and fatally: without a completed turn the happens-before never
	// engaged, so nothing was necessarily reported and no retention can be assumed. An
	// AC-1 result read off such a run is uninterpretable rather than a failure.
	if !obs.sawEcho {
		t.Fatalf("the minted child never replied (no assistant_delta carrying %q for conversation %s) — the "+
			"turn did not run, so the rider's roster line cannot be assumed written and nothing downstream "+
			"is diagnosable; turn_end=%v background_task_roster on the minter=%d",
			lateRosterEchoNeedle, obs.convID, obs.sawTurnEnd, obs.rostersOnMinter)
	}
	if !obs.sawTurnEnd {
		t.Fatalf("the minted child replied but the turn never closed (no turn_end for conversation %s) — the "+
			"retention cannot be assumed, so the AC-1 result below would be uninterpretable; "+
			"background_task_roster on the minter=%d", obs.convID, obs.rostersOnMinter)
	}

	// AC 1: the frame reached a conn that connected after the roster existed and sent
	// nothing but its handshake. The minter count is what makes this diagnosable — see
	// lateRosterObservation.rostersOnMinter.
	if !obs.found {
		t.Fatalf("no background_task_roster reached the LATE-connecting phone (conversation %s, turn "+
			"complete, background_task_roster on the minter=%d) — read that minter count first: non-zero "+
			"means the rider fired and the LIVE lane delivered, so the reconcile is the suspect, and the "+
			"first place to look is the RetainedBackgroundTaskRosters field of the V2 session config in "+
			"startRelayV2 being unset or nil, on which reconcileBackgroundTaskRosters returns early — that "+
			"is exactly the one-line mutant this test exists to redden. Zero means the two producers failed "+
			"together, which points upstream instead: the rider env not reaching the child, the parser "+
			"dropping the line, or the hold never filling. What it is NOT is the droppable cap: "+
			"pushQueue.enqueue marks only TypeAssistantDelta droppable and the reconcile pushes straight "+
			"onto the conn's queue via Push", obs.convID, obs.rostersOnMinter)
	}

	// AC 3: the reconciled frame drove no turn. The observing client sent nothing that
	// could open one — its seal-and-send closure was discarded in the driver — so any
	// turn frame here means the reconcile's push took a turn-bearing path.
	if obs.turnFramesOnObserver != 0 {
		t.Errorf("turn frames (turn_state/turn_end) on the late-connecting phone: got %d, want 0 — a "+
			"connect-time control snapshot must neither open nor close a turn, and this conn sent nothing "+
			"that could", obs.turnFramesOnObserver)
	}

	// AC 2's cardinality half. Exactly one conversation exists, so exactly one payload
	// is returned and exactly one envelope is pushed — a second means a duplicated
	// registry row or the bootstrap session leaking into the enumeration, and a
	// first-arrival-only wait could not redden for either.
	if obs.extraOnObserver != 0 {
		t.Errorf("extra background_task_roster frames on the late-connecting phone: got %d, want 0 — one "+
			"conversation is retained, so the reconcile pushes exactly one envelope (minter=%d)",
			obs.extraOnObserver, obs.rostersOnMinter)
	}

	// retainedBackgroundTaskRosters' own contribution, and NOT inherited from the live
	// lane: the reconcile carries no turn context at all, so the id can only have come
	// from the daemon's registry record via resolveBoundBackgroundTaskRoster. The
	// bootstrap session contributes nothing (it has no conversation record), so exactly
	// one payload is expected and the first arrival is it.
	if obs.roster.ConversationID != obs.convID {
		t.Errorf("conversation_id: got %q, want %q (the server-minted id)", obs.roster.ConversationID, obs.convID)
	}

	// The two halves of the roster's own size arithmetic, which the published contract
	// tells a client to perform as len(tasks) + dropped_tasks. The rider cans nine rows
	// against streamsup's eight-entry cap, so BOTH numbers are non-default and neither
	// can be satisfied by a payload that simply never populated the field.
	if obs.roster.DroppedTasks != rosterWantDropped {
		t.Errorf("dropped_tasks: got %d, want %d — the rider cans %d rows against streamsup's "+
			"maxTaskRosterEntries (%d), so the remainder must be reported rather than defaulted. If the cap "+
			"itself moved, this red is the alarm and the fixture count is what to revisit",
			obs.roster.DroppedTasks, rosterWantDropped, rosterRiderTasks, rosterWantTasks)
	}
	if len(obs.roster.Tasks) != rosterWantTasks {
		t.Fatalf("tasks: got %d rows, want %d (streamsup's maxTaskRosterEntries, truncated from the tail so "+
			"claude's order is preserved)", len(obs.roster.Tasks), rosterWantTasks)
	}

	// Row 0 verbatim: the committed capture's own row, carried unaltered across the
	// child's stdout, the parser, the hold, the resolver, the reconcile and the sealed
	// envelope. The description is a literal shell command line, and asserting it byte
	// for byte is what shows every one of those layers treats it as inert text (#833).
	// Field by field, never a payload dump.
	got := obs.roster.Tasks[0]
	if got.TaskID != rosterCapturedTaskID {
		t.Errorf("tasks[0].task_id: got %q, want %q (claude's order, truncated from the tail only)",
			got.TaskID, rosterCapturedTaskID)
	}
	if got.TaskType != rosterCapturedTaskType {
		t.Errorf("tasks[0].task_type: got %q, want %q", got.TaskType, rosterCapturedTaskType)
	}
	if got.Description != rosterCapturedDescription {
		t.Errorf("tasks[0].description: got %q, want %q — the capture's own command line, which must "+
			"arrive as inert bytes and never shell-interpreted anywhere on the path",
			got.Description, rosterCapturedDescription)
	}

	// Every row's own truncation report. The rider's fields sit far under the daemon's
	// per-field caps (256 bytes for the two ids, 512 for a roster description), so
	// nothing was cut — which is what makes the dropped_tasks assertion above a claim
	// about the ENTRY cap alone rather than about two cutters at once. truncated_fields
	// is deliberately NOT normalised nil→[] the way tasks is, so null is the correct
	// wire form here and nil is the correct decode.
	for i, row := range obs.roster.Tasks {
		if row.TruncatedFields != nil {
			t.Errorf("tasks[%d].truncated_fields: got %d names, want null — no field the rider writes comes "+
				"near a per-field cap", i, len(row.TruncatedFields))
		}
	}

	// Attribution, not just presence: every row after the first must be one of the
	// rider's synthetic siblings, distinct from every other. A payload that duplicated
	// one row eight times, or that merged rows from two rosters, passes every check
	// above and reddens precisely here. Ids only — no other field is read, and none is
	// printed.
	seen := map[string]bool{obs.roster.Tasks[0].TaskID: true}
	for i, row := range obs.roster.Tasks[1:] {
		if seen[row.TaskID] {
			t.Errorf("tasks[%d].task_id is a duplicate of an earlier row — the roster carries one entry per "+
				"task", i+1)
		}
		seen[row.TaskID] = true
		if !strings.HasPrefix(row.TaskID, rosterSyntheticIDPrefix) {
			t.Errorf("tasks[%d].task_id does not carry the rider's synthetic prefix %q — this row came from "+
				"somewhere other than the roster this test reported", i+1, rosterSyntheticIDPrefix)
		}
	}

	t.Logf("late-connect roster delivered: conv=%s rows=%d dropped=%d (background_task_roster on the "+
		"minter=%d)", obs.convID, len(obs.roster.Tasks), obs.roster.DroppedTasks, obs.rostersOnMinter)
}
