//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Seven tickets built the slash-command path in layers — the mapping (#2001), the frame
// bound (#2002), the turn-lane emit (#2003), the retention (#2004), the resolver
// (#2005), the connect-time reconcile (#2006) and its enumeration seam (#2007). This is
// the proof of the CHAIN across process boundaries for the client the reconcile exists
// FOR: one that was not connected when the daemon obtained the list. A closure built
// over the wrong registry, a capability gate that never opens, an envelope dropped
// before it is sealed — every one of those leaves the unit tests green.
//
// THIS IS THE INVERSE OF TestRelayV2_StreamSlashCommandListReachesConnectedPhone, and
// the inversion is the whole point. That spec proves the LIVE lane, which emits once per
// child spawn to whatever interactive conns exist AT THAT INSTANT, so it has to kill a
// child and observe the respawn's frame in order to see one at all. This spec proves the
// RECONCILE, so it must do the opposite: let the list exist FIRST, then connect, and
// observe the frame with the connecting client neither driving a turn nor sending a
// message. If this test ever needs a child to spawn while the observing client is
// connected it has become a second copy of that one and proves nothing about the
// enumeration.
//
// TWO PRODUCERS, WHICH IS WHY THE MUTANT IS THE ACCEPTANCE CRITERION. The turn lane
// emits the same variant, so a run that accidentally observed a live-lane frame would
// pass against a completely dead reconcile. What separates them is the one-line mutant —
// leaving the RetainedSlashCommandLists field of the V2 session config unset, on which
// reconcileSlashCommandLists returns early — plus the count of frames on the MINTING
// conn, which is logged into every failure message here for exactly that reason.
//
// A CONVERSATION MUST EXIST FIRST. retainedSlashCommandLists enumerates the CONVERSATION
// registry, and the pool's bootstrap session has no conversation record at all —
// handlers.CreateConversation is what writes CurrentSessionID. A daemon that has minted
// nothing therefore retains nothing this path can see, and a test built on the bootstrap
// child alone would wait out its whole deadline for a frame that is correctly never
// sent. An over-the-wire create_conversation mints the record; since #2085 it does NOT
// spawn, so the FIRST TURN below is what produces the child whose initialize reply is
// retained. That same pre-mint state is what the empty-registry conn below asserts
// against.
//
// WHAT THIS TEST IS EVIDENCE OF, AND WHAT IT IS NOT — three non-claims, because each is
// easy to misread off a green run:
//
//   - A command's name, argument hint, description and every alias string are
//     WORKSPACE-authored, a lower-trust origin than claude's own. The rows below assert
//     they arrive VERBATIM, which is the documented production posture: the path
//     forwards them unsanitised by design and the render boundary owing sanitisation is
//     the client's. So this file is evidence of FAITHFUL FORWARDING and is NOT evidence
//     that anything sanitises.
//   - THREE paired devices here are a sequencing device, NOT a confinement proof. The
//     late observer reads a conversation minted by a DIFFERENT device, and that is the
//     enumerate-all seam working as designed: RetainedSlashCommandLists takes no
//     argument, so a paired interactive conn sees every retained list. Per-device
//     confinement is out of scope for the whole Mode B family and belongs to its
//     umbrella (#829), not to this test.
//   - The capability gate's REFUSED arm is not exercised. Only the granted arm is; the
//     refusal is pinned at the unit seam, where a negative costs no deadline.
//
// NO DECODED PAYLOAD IS EVER PRINTED, on any path. The deadline diagnostics log counts
// and ids only, per the #833 posture that command strings are never logged, and the
// empty-registry window reports a COUNT rather than the frame it did not expect — that
// is the one path in this file with no legitimate reason to hold an inventory at all.
// The row failures name a got and a want because both are canned literals, but they do
// it FIELD BY FIELD rather than dumping the payload: against fakeclaude a dump is
// harmless, and in internal/e2e/realclaude the same shape carries the operator's real
// workspace.
const (
	// Distinct from both siblings' bootstrap UUIDs: the three specs run in the same
	// package and a shared literal would read as a shared fixture it is not.
	lateSlashCommandListBootstrapUUID = "20090000-0000-4000-8000-000000000001"
	lateSlashCommandListUserText      = "e2e-late-slashcommands:hello\n"
	lateSlashCommandListEchoNeedle    = "e2e-late-slashcommands:hello"
	lateSlashCommandListCreateReqID   = uint64(2009)
	lateSlashCommandListSendReqID     = uint64(2010)
)

// lateSlashCommandListObservation is what one empty-registry handshake, one wire-minted
// conversation, one driven turn and one LATER interactive handshake put on three clients.
//
// The milestones are VALUES rather than helper-side assertions, slashCommandListObservation's
// reason: a frame count read off a run where the turn never completed proves nothing, so
// the caller asserts them first and fatally, and a helper that fataled on them would
// leave those checks dead code.
type lateSlashCommandListObservation struct {
	// AC 3: frames on a conn that handshook while the daemon had retained nothing.
	emptyConnLists int
	convID         string // the server-minted id the frame must be stamped with
	sawEcho        bool   // milestone: the minted child replied
	// milestone: the turn closed ⇒ the initialize ack was already through the parser
	sawTurnEnd bool
	// Diagnostic ONLY, never asserted. It should be zero: the live lane's emit for the
	// minted child fires at spawn, when the active-conversation cursor is still empty, so
	// interactiveTurnEmitterV2.Handle's empty-conversation early return drops it. Worth
	// logging because it separates "the reconcile did not fire" from "the live lane fired
	// instead" — but an assertion on it would redden this spec for a change in a lane it
	// does not own.
	listsOnMinter int
	list          protocol.SlashCommandListPayload
	found         bool
	// AC 2's cardinality half: further frames on the observer after the first. Exactly
	// one conversation is retained, so exactly one envelope is pushed — a second would
	// mean a duplicated registry row or the bootstrap session leaking into the
	// enumeration, and a first-arrival-only wait could not redden for either.
	extraOnObserver int
}

// driveLateConnectSlashCommandList starts a stream-interactive daemon, observes an
// interactive handshake against an EMPTY conversation registry, then mints a
// conversation over the wire from phone-a and drives one turn on it so the list exists,
// THEN dials phone-b and handshakes it interactive, and collects what phone-b's own
// handshake puts on its socket. phone-b sends nothing but that handshake.
//
// THREE CONNS, AND THE THIRD ONE IS FORCED. Proving an absence means running a window to
// its deadline, and coder/websocket closes the underlying connection when a read context
// is cancelled (fakephone.Client.ReceiveBytes' own doc), so a timed-out client cannot be
// reused. The conn that proves AC 3 is therefore DEAD after its own window and cannot
// also be the minter. Its seal-and-send closure is discarded on the floor for that
// reason — as phone-b's is for a different one — so a later edit that tries to reuse
// either conn fails to compile rather than failing as a decrypt error that reads like a
// daemon bug.
//
// THE HAPPENS-BEFORE THAT REPLACES A SLEEP. fakeclaude's runStreamJSON is a single loop
// that writes every reply on the goroutine that read the line, in order. The initialize
// control request is written to the child's stdin at spawn, before any user turn can be
// routed, so the ack line precedes the turn's echo and result lines on that child's
// stdout. The daemon parses them in order and the retention is written BEFORE the event
// is forwarded downstream. Therefore turn_end observed on phone-a ⇒ the ack was parsed ⇒
// the hold holds the list ⇒ retainedSlashCommandLists will enumerate it for the next
// handshake. No time.Sleep and no retry loop belongs anywhere in this test; the two
// bounded windows that do exist are not polls but single drains whose result is a count.
//
// It asserts none of the acceptance criteria. It t.Fatalf's only on transport, seal and
// decode faults, on an error envelope, and on the mint never completing — never on an
// acceptance criterion. On the turn or collection deadline it returns what it has and
// logs the counts, leaving the diagnosis to the caller.
func driveLateConnectSlashCommandList(t *testing.T) lateSlashCommandListObservation {
	t.Helper()

	home := shortHome(t)

	// THREE devices, all paired BEFORE the daemon starts — the daemon loads its
	// registries once at startup, seedBoundConversation's stated reason for its own
	// ordering. TestPairRevoke_E2E's "removes one of two" subtest is the precedent that
	// repeated pair runs into one home yield independently usable devices; the pairing
	// path caps nothing.
	names := []string{"phone-empty", "phone-a", "phone-b"}
	tokens := make([]string, 0, len(names))
	var staticPub string
	for _, name := range names {
		r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name="+name)
		if r.ExitCode != 0 {
			t.Fatalf("pyry pair %s exit=%d\nstdout:\n%s\nstderr:\n%s", name, r.ExitCode, r.Stdout, r.Stderr)
		}
		p := decodePairPayload(t, r.Stdout)
		tokens = append(tokens, p.Token)
		// pyry mints a token per pair run, but the static key is the DAEMON's and is
		// identical in all three payloads — so the last write wins harmlessly.
		staticPub = p.ServerStaticPubkey
	}
	pubKey, err := base64.StdEncoding.DecodeString(staticPub)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// No seedBoundConversation, and that is load-bearing twice over. The conversation
	// observed here is MINTED over the wire below, so its binding is created by
	// create_conversation rather than seeded — TestRelayV2_StreamInterruptStopsRunningTurn's
	// reasoning, inherited. And a seeded row would bind a conversation to the BOOTSTRAP
	// session, which would both make the empty-registry window below vacuous and hand
	// that conversation the shared bootstrap child's menu — the shortcut
	// retainedSlashCommandLists' double lookup exists to prevent. No extra env either:
	// fakeclaude's runStreamJSON answers the initialize control request in both of its
	// modes and under no rider, so this spec adds no harness scaffolding at all.
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	h := StartStreamInteractiveWithRelay(t, home, lateSlashCommandListBootstrapUUID, fr.URL()+"/v2/server")
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	// One factory for all three conns. Each gets its OWN CipherState pair: the receive
	// nonce is a lockstep counter, so sharing one across phones — or filtering before
	// decrypting — desyncs it and presents as a misleading decrypt error rather than as a
	// clean failure. Pairing each phone with its own states at one call site makes that
	// structural. Interactive is load-bearing rather than incidental: the fan-out and the
	// reconcile both gate on the capability, so a non-interactive handshake yields zero
	// frames and a vacuously red test that looks exactly like a missing seam.
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

	var obs lateSlashCommandListObservation

	// --- AC 3: a conn that handshakes while the daemon has retained NOTHING. The
	// registry is empty at this point (nothing seeded it and nothing has been minted), so
	// retainedSlashCommandLists enumerates nothing and reconcileSlashCommandLists takes
	// its len(retained) == 0 return. The handshake completing normally is discharged by
	// driveHandshakeToOpenDaemonInteractive having RETURNED at all — it fatals on a
	// non-noise_resp inner frame, on a hello_ack that will not decode, and on a missing
	// interactive grant, so reaching this line is the whole of that half.
	//
	// The window is short on purpose: the reconcile's pushes happen synchronously on the
	// handshake's success tail, after noise_resp is sent, so a frame that is coming at
	// all is milliseconds away rather than seconds. This conn is dead once the window
	// times out and is never touched again.
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
		case protocol.TypeSlashCommandList:
			// COUNTED, never decoded and never printed. This is the one path in the file
			// that has no legitimate reason to hold an inventory, and the natural
			// spelling of its failure would echo a whole workspace's command names.
			obs.emptyConnLists++
		}
	}

	sealSendA, nextEnvA := dialInteractive("phone-a", tokens[1])

	// --- Mint the conversation over the wire. All-null create_conversation (server
	// defaults), the shape createConversationViaPhone and the interrupt spec both send.
	// Drained through this file's own nextEnv rather than through that helper because the
	// helper discards every non-matching frame, and listsOnMinter has to count across
	// BOTH of phone-a's windows for the diagnostic to mean anything.
	sealSendA(protocol.Envelope{
		ID:      lateSlashCommandListCreateReqID,
		Type:    protocol.TypeCreateConversation,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.CreateConversationPayload{}),
	})
	createDeadline := time.Now().Add(15 * time.Second)
	for obs.convID == "" {
		env, ok := nextEnvA(createDeadline)
		if !ok {
			t.Fatalf("no conversation_created within 15s (slash_command_list on the minter: %d) — the minted "+
				"conversation was never minted. Since #2085 the mint does not spawn, so suspect the create "+
				"handler rather than the child — the child comes up on the turn driven below", obs.listsOnMinter)
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope while awaiting conversation_created: %s", string(env.Payload))
		case protocol.TypeSlashCommandList:
			obs.listsOnMinter++
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

	// --- Drive one turn on the minted conversation and wait for turn_end. This is the
	// SYNCHRONISATION, not a content assertion: the mint's reply is sent after the
	// handler's registry save, which says nothing about whether the child's initialize
	// ack has been parsed yet. Without this step phone-b can win the race and see
	// nothing. turn_end is the terminator rather than a frame count, so an interleaved
	// broadcast cannot make it flaky.
	sealSendA(protocol.Envelope{
		ID:   lateSlashCommandListSendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: obs.convID,
			MessageID:      "m-late-slashcommands-1",
			Text:           lateSlashCommandListUserText,
		}),
	})
	turnDeadline := time.Now().Add(30 * time.Second)
	for !obs.sawTurnEnd {
		env, ok := nextEnvA(turnDeadline)
		if !ok {
			t.Logf("turn window deadline reached: conv=%s echo=%v turn_end=%v slash_command_list on the minter=%d",
				obs.convID, obs.sawEcho, obs.sawTurnEnd, obs.listsOnMinter)
			break
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope during the turn: %s", string(env.Payload))
		case protocol.TypeSlashCommandList:
			obs.listsOnMinter++
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if strings.Contains(p.Text, lateSlashCommandListEchoNeedle) {
				obs.sawEcho = true
			}
		case protocol.TypeTurnEnd:
			obs.sawTurnEnd = true
		}
	}

	// --- phone-b connects only NOW, after the list exists. Its seal-and-send closure is
	// deliberately DISCARDED: it sends nothing but its handshake, and dropping the
	// closure on the floor makes that structural rather than a promise in a comment.
	// Whatever the daemon sends phone-a from here on buffers unread on phone-a's socket,
	// so the windows never interleave and this stays one goroutine.
	//
	// handleNoiseInit's success tail records s.interactive, creates the push queue and
	// calls reconcileSlashCommandLists, which pushes one envelope per retained payload —
	// and it sends noise_resp BEFORE those pushes, which is what makes phone-b's decrypt
	// order deterministic. driveHandshakeToOpenDaemonInteractive consumed exactly that
	// noise_resp and nothing after it, so this loop starts on a nonce that is in sequence.
	_, nextEnvB := dialInteractive("phone-b", tokens[2])
	collectDeadline := time.Now().Add(20 * time.Second)
	for !obs.found {
		env, ok := nextEnvB(collectDeadline)
		if !ok {
			t.Logf("phone-b collection deadline reached: conv=%s slash_command_list on the minter=%d",
				obs.convID, obs.listsOnMinter)
			break
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope on phone-b: %s", string(env.Payload))
		case protocol.TypeSlashCommandList:
			if err := json.Unmarshal(env.Payload, &obs.list); err != nil {
				// A decode failure at this layer IS the defect, not a miss. The raw bytes
				// are deliberately NOT printed: they carry the whole inventory.
				t.Fatalf("decode slash_command_list payload: %v", err)
			}
			obs.found = true
		}
	}

	// --- The settle window, which is what makes the count above an EXACT claim rather
	// than an at-least-one. Only reachable when the loop above ended on a frame: had it
	// ended on its deadline, phone-b's client would already be closed. Short for the
	// empty-registry window's reason — every envelope this handshake produces was pushed
	// before noise_resp's successors, so a second one is milliseconds away or not coming.
	if obs.found {
		settleDeadline := time.Now().Add(2 * time.Second)
		for {
			env, ok := nextEnvB(settleDeadline)
			if !ok {
				break
			}
			switch env.Type {
			case protocol.TypeError:
				t.Fatalf("unexpected error envelope on phone-b after the first list: %s", string(env.Payload))
			case protocol.TypeSlashCommandList:
				obs.extraOnObserver++ // counted, never decoded, never printed
			}
		}
	}
	return obs
}

// TestRelayV2_StreamSlashCommandListReachesLateConnectingPhone is the hermetic end-to-end
// proof that the command inventory claude reports at initialize reaches a client that
// connected AFTER the daemon obtained it, without that client asking for anything — with
// every canned entry intact, each alias-bearing entry's aliases attached to THAT entry
// and to no other, and nothing at all sent to a conn that connected before there was
// anything to send.
//
// Every failure of this test is a timeout somewhere and they are hard to tell apart from
// a bare "want 1, got 0", so each message names its own suspect.
func TestRelayV2_StreamSlashCommandListReachesLateConnectingPhone(t *testing.T) {
	obs := driveLateConnectSlashCommandList(t)

	// AC 3, first: it is the only claim still interpretable if everything downstream
	// fails, since it is complete before the first conversation is minted. Its
	// "handshake completes normally" half was discharged inside the driver — reaching
	// any assertion at all means driveHandshakeToOpenDaemonInteractive returned rather
	// than fataling on the noise_resp, the hello_ack decode or a missing interactive
	// grant. The count is reported without the payload on purpose.
	if obs.emptyConnLists != 0 {
		t.Errorf("slash_command_list on the empty-registry conn: got %d, want 0 — a conn that handshook "+
			"while the daemon had retained nothing was unicast a menu anyway. reconcileSlashCommandLists "+
			"returns on len(retained) == 0, so suspect retainedSlashCommandLists enumerating a conversation "+
			"that should not exist yet: a seeded conversations.json would bind one to the BOOTSTRAP session "+
			"and hand it the shared bootstrap child's inventory", obs.emptyConnLists)
	}

	// Milestones next and fatally: without a completed turn the ordering argument never
	// engaged, so the retention cannot be assumed and an AC-1 result read off such a run
	// is uninterpretable rather than a failure.
	if !obs.sawEcho {
		t.Fatalf("the minted child never replied (no assistant_delta carrying %q for conversation %s) — the "+
			"turn did not run, so the initialize ack cannot be assumed parsed and nothing downstream is "+
			"diagnosable; turn_end=%v slash_command_list on the minter=%d",
			lateSlashCommandListEchoNeedle, obs.convID, obs.sawTurnEnd, obs.listsOnMinter)
	}
	if !obs.sawTurnEnd {
		t.Fatalf("the minted child replied but the turn never closed (no turn_end for conversation %s) — the "+
			"retention cannot be assumed, so the AC-1 result below would be uninterpretable; "+
			"slash_command_list on the minter=%d", obs.convID, obs.listsOnMinter)
	}

	// AC 1: the frame reached a conn that connected after the list existed and sent
	// nothing but its handshake.
	if !obs.found {
		t.Fatalf("no slash_command_list reached the LATE-connecting phone (conversation %s, turn complete, "+
			"slash_command_list on the minter=%d) — suspect in order: the RetainedSlashCommandLists field of "+
			"the V2 session config in startRelayV2 being unset or nil, because reconcileSlashCommandLists "+
			"returns early on a nil seam and that is exactly the one-line mutant this test exists to redden; "+
			"retainedSlashCommandLists enumerating nothing because the minted conversation resolved to no "+
			"retained list; and the interactive grant on this conn. What it is NOT is the droppable cap: "+
			"pushQueue.enqueue marks only TypeAssistantDelta droppable and the reconcile pushes straight onto "+
			"the conn's queue via Push, so a miss HERE points at the seam, while a drop on the live lane "+
			"would show up as a non-zero in the minter count above",
			obs.convID, obs.listsOnMinter)
	}

	// AC 2's cardinality half, and where this spec departs from its model-list twin. That
	// twin declined a count, reasoning a total buys a timing claim rather than a
	// behaviour one; the reasoning does not transfer here because the seam is
	// enumerate-all. Exactly one conversation exists, so exactly one payload is returned
	// and exactly one envelope is pushed — a second means a duplicated registry row or
	// the bootstrap session leaking into the enumeration, and neither has another home in
	// this test.
	if obs.extraOnObserver != 0 {
		t.Errorf("extra slash_command_list frames on the late-connecting phone: got %d, want 0 — one "+
			"conversation is retained, so the reconcile pushes exactly one envelope", obs.extraOnObserver)
	}

	// retainedSlashCommandLists' own contribution, and NOT inherited from the live-lane
	// sibling: the reconcile carries no turn context at all, so the id can only have come
	// from the daemon's registry record via resolveBoundSlashCommandList. The bootstrap
	// session contributes nothing (it has no conversation record), so exactly one payload
	// is expected and the first arrival is it.
	if obs.list.ConversationID != obs.convID {
		t.Errorf("conversation_id: got %q, want %q (the server-minted id)", obs.list.ConversationID, obs.convID)
	}
	// Four canned entries against a 128-entry count cap and a 64000-byte frame bound, so
	// nothing was cut and the list's true size is exactly what arrived. This is a
	// nothing-was-cut assertion and NOT a cap test — it is meaningful only because the
	// fixture sits orders of magnitude under both bounds.
	if obs.list.DroppedCommands != 0 {
		t.Errorf("dropped_commands: got %d, want 0 — the fake cans four entries, orders of magnitude "+
			"under both the producer's entry cap and the mapper's byte bound", obs.list.DroppedCommands)
	}
	if len(obs.list.Commands) != len(slashCommandListWantRows) {
		t.Fatalf("commands: got %d entries, want %d (claude's own order, which MapEvent is documented "+
			"not to sort)", len(obs.list.Commands), len(slashCommandListWantRows))
	}

	// The want table is the live-lane sibling's, reused rather than transcribed a second
	// time: one copy of fakeclaude's canned rows in this package, one place to update
	// when initializeCommands changes. If that file is ever deleted the compile break is
	// the signal you want, not a silently diverged second copy.
	//
	// BY INDEX, which is what makes this an ATTRIBUTION claim rather than a presence one.
	// No payload is dumped on any failure path — each row reports its own field.
	for i, want := range slashCommandListWantRows {
		got := obs.list.Commands[i]

		if got.Name != want.name {
			t.Errorf("commands[%d].name: got %q, want %q (claude's order, unsorted)", i, got.Name, want.name)
		}
		if got.ArgumentHint != want.argumentHint {
			t.Errorf("commands[%d].argument_hint: got %q, want %q", i, got.ArgumentHint, want.argumentHint)
		}
		if got.Description != want.description {
			t.Errorf("commands[%d].description: got %q, want %q", i, got.Description, want.description)
		}
		if got.TruncatedFields != nil {
			t.Errorf("commands[%d].truncated_fields: got %#v, want nil — nothing was cut", i, got.TruncatedFields)
		}

		if want.aliases == nil {
			// The "and to no other" half, and the only assertion that separates per-entry
			// attribution from a key-blind flat reading: a payload that carried the right
			// aliases somewhere but attached them to every row would pass every check
			// above and redden precisely here.
			if len(got.Aliases) != 0 {
				t.Errorf("commands[%d] (%q).aliases: got %q, want empty — the fake OMITS the key for "+
					"this entry, so aliases belonging to another entry have leaked onto it",
					i, want.name, got.Aliases)
			}
			// A live claim, not decoration: json.Unmarshal yields a nil slice for null and
			// an empty non-nil slice for [], so this is what distinguishes them — and here
			// it pins protocol.SlashCommand.MarshalJSON's nil→[] normalisation through
			// reconcileSlashCommandLists' OWN marshal, a call site the live-lane sibling
			// never exercises.
			if got.Aliases == nil {
				t.Errorf("commands[%d] (%q).aliases: got null, want [] — an omitted aliases key must "+
					"reach the wire as an empty array so no client has to branch on an optional array",
					i, want.name)
			}
			continue
		}

		// Ordered, not a set: the capture's own alias order is non-alphabetical, and
		// MapEvent is documented not to sort. The COUNT differing between the two
		// alias-bearing rows is what stops either from substituting for the other.
		if !slices.Equal(got.Aliases, want.aliases) {
			t.Errorf("commands[%d] (%q).aliases: got %q, want %q (claude's own order, and attached to "+
				"THIS entry — the desktop Actions menu's reset entry is an alias of clear rather than a "+
				"command name, so a path carrying names only greys out a command that works)",
				i, want.name, got.Aliases, want.aliases)
		}
	}
}
