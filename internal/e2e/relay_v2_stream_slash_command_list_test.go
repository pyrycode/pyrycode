//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Seven tickets built the slash-command path in layers — the mapping (#2001), the frame
// bound (#2002), the turn-lane emit (#2003), the retention (#2004), the resolver
// (#2005), the connect-time reconcile (#2006) and its enumeration seam (#2007) — and
// each is proved at its own seam against its own doubles. This is the first proof of
// the CHAIN, across process boundaries: a real pyry supervising a real child, a real
// client connection, no credentials and no network. It is the direct twin of
// relay_v2_stream_model_list_test.go, whose header carries the reasoning this file
// inherits rather than restates.
//
// THIS SLICE COVERS THE LIVE LANE ONLY. #2009 owns the late-connecting reconcile half,
// exactly as the model list has a separate reconcile test beside its live-lane one.
//
// WHY NOTHING PROVED THIS BEFORE. fakeclaude's writeInitializeAck answered with a
// models array and NO commands key, and internal/streamsup's emitSlashCommandList
// suppresses a zero-length list at its own precondition — so the whole hermetic e2e
// tier ran with this arm dark, emitting nothing to retain or send. Canning the array is
// what makes the round trip observable at all.
//
// THE SEQUENCING PROBLEM is driveModelListRespawn's unchanged, and the same route is
// the only one that delivers: drive one ordinary turn from the connected client to
// stamp the active-conversation cursor, then SIGKILL the supervised child and collect
// from the respawn's initialize round trip. The respawn keeps the session id, so its
// events still match the drain's session-tag gate. A new_session rotation must NOT be
// substituted — after a rotation the runner's sink tag stays on the OUTGOING session id
// while the conversation rebinds to the new one, so every event the fresh child
// produces is dropped at that gate; relay_v2_stream_new_session_test.go's own header
// records this. The pre-kill turn is what makes either route work at all:
// interactiveTurnEmitterV2.Handle returns before its type switch while the cursor is
// empty, which is also why the bootstrap child's own inventory can never reach a client
// by accident.
//
// WHAT THIS TEST IS EVIDENCE OF, AND WHAT IT IS NOT. A command's name, description,
// argument hint and every alias string are WORKSPACE-authored — a command defined in a
// repository was written by whoever wrote that repository, a lower-trust origin than
// claude's own strings. The rows below assert they arrive VERBATIM, which is the
// documented production posture: the path forwards them unsanitised by design and the
// render boundary owing sanitisation is the client's. So this file is evidence of
// FAITHFUL FORWARDING and is not evidence that anything sanitises.
//
// NO DECODED PAYLOAD IS EVER PRINTED, on any path. The deadline diagnostics below log
// counts, pids and discriminants only, per the #833 posture that command strings are
// never logged. The failure messages name a got and a want because both are this file's
// own canned literals — but they do it FIELD BY FIELD rather than dumping the payload
// the way the model-list twin dumps its Models, and that restraint is the part worth
// copying: against fakeclaude a dump is harmless, and in internal/e2e/realclaude the
// same payload carries the operator's real workspace.
const (
	slashCommandListInitialUUID = "20080000-0000-4000-8000-000000000001"
	slashCommandListConvID      = "20080000-0000-4000-8000-000000000002"
	slashCommandListUserText    = "e2e-slashcommands:hello\n"
	slashCommandListEchoNeedle  = "e2e-slashcommands:hello"
	slashCommandListSendReqID   = uint64(2008)
)

// slashCommandListWantRows is what fakeclaude's initializeCommands cans, transcribed as
// literals because the fake is a separate main package and nothing can be imported from
// it (the discipline relay_v2_stream_unrecognized_test.go's needles follow, for its
// reason). Every row is itself verbatim from the committed capture,
// internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json, in that capture's
// own relative order — which turnbridge.MapEvent is documented not to sort, so the
// comparison below is BY INDEX rather than by set membership.
//
// THE INTERLEAVING IS THE PROOF, not a transcription accident. Two alias-bearing rows
// with different values AND different counts, separated by a row that omits the key:
// a key-blind flat reading that sprayed every alias onto every entry reddens at
// `compact` and `model`, and a reading that attached aliases to the wrong owner reddens
// at `clear` and `config` on both the value and the count. One alias-bearing row could
// separate neither — it cannot tell "attached to the entry that owns them" from
// "present somewhere in the payload".
//
// A nil aliases field means the fake OMITS the key, which is a different INPUT from a
// published empty array and is the only shape claude has been observed to send: no
// entry in the capture's 51 publishes "aliases":[].
var slashCommandListWantRows = []struct {
	name         string
	argumentHint string
	description  string
	aliases      []string
}{
	{
		name:         "clear",
		argumentHint: "[name]",
		description:  "Start a new session with empty context; previous session stays on disk (resumable with /resume)",
		// Two, and non-alphabetical in the capture — so ORDER is observable, and the
		// comparison below is an ordered one rather than a set's.
		aliases: []string{"reset", "new"},
	},
	{
		name:         "compact",
		argumentHint: "<optional custom summarization instructions>",
		description:  "Free up context by summarizing the conversation so far",
	},
	{
		name:         "config",
		argumentHint: "key=value",
		description:  "Set a setting by key",
		aliases:      []string{"settings"},
	},
	{
		name:         "model",
		argumentHint: "<model>",
		description:  "Set the AI model for Claude Code",
	},
}

// slashCommandListObservation is what one driven turn plus one kill/respawn put on the
// wire: the first slash_command_list seen AFTER the kill, a count of the ones seen
// during the pre-kill turn, the two pre-kill milestones, and the two child pids.
//
// The milestones are VALUES rather than helper-side assertions, for
// modelListObservation's reason: a frame count read off a run where nothing completed
// proves nothing, so the caller asserts them first and fatally, and a helper that
// fataled on them would leave those checks dead code.
type slashCommandListObservation struct {
	list         protocol.SlashCommandListPayload
	found        bool
	preKillLists int
	sawEcho      bool
	sawTurnEnd   bool
	firstPID     int
	secondPID    int
}

// driveSlashCommandListRespawn spawns a stream-interactive daemon, drives one ordinary
// turn from a connected interactive v2 client so the drain's active-conversation cursor
// is stamped, SIGKILLs the supervised child, and collects what the respawn's initialize
// round trip puts on that same still-connected client.
//
// It asserts none of the acceptance criteria. It t.Fatalf's only on transport and
// decode faults — an error envelope, a payload that will not decode, a receive error
// that is not the deadline. On either collection deadline it returns with what it has
// and logs the counts, leaving the diagnosis to the caller's assertions.
func driveSlashCommandListRespawn(t *testing.T) slashCommandListObservation {
	t.Helper()

	home := shortHome(t)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	payloadA, err := paireddevice.Setup(paireddevice.Config{
		Home:         home,
		InstanceName: "test",
		Relay:        relayURL,
		DeviceName:   "phone-a",
	})
	if err != nil {
		t.Fatalf("setup phone-a: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Bind the conversation to the bootstrap session so the drain gate passes. This
	// UUID MUST equal the one handed to StartStreamInteractiveWithRelay below: a
	// mismatch between the two seeds drops every event at the gate and hangs the drain
	// for the full deadline, which presents as an unexplained timeout rather than a
	// clean seed-time failure. The call also has to sit BETWEEN paired-device setup
	// and daemon start — seedBoundConversation writes into <home>/.pyry/test/ without
	// creating it, and the daemon loads conversations.json once at startup.
	seedBoundConversation(t, home, slashCommandListConvID, slashCommandListInitialUUID)

	// No extra env: fakeclaude answers the initialize control_request in both of its
	// modes and under no rider, so this test adds no harness scaffolding at all.
	h := StartStreamInteractiveWithRelay(t, home, slashCommandListInitialUUID, relayURL)
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
	// Interactive — load-bearing, not incidental. The fan-out filters every frame on
	// the interactive capability, so a non-interactive handshake yields zero
	// slash_command_list frames and a vacuously red test that looks exactly like a
	// missing emitter arm.
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
			// One deadline per collection window, and every receive is bounded by what
			// is left of it — never a short fixed timeout in a poll loop.
			// coder/websocket closes the underlying connection when the read context is
			// cancelled (fakephone.Client.ReceiveBytes' own doc), so a timed-out client
			// cannot be reused and the first timeout must be terminal for the window.
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

	var obs slashCommandListObservation

	first := waitForRunnerStatus(t, h, 20*time.Second, "first child running",
		func(s *control.StatusPayload) bool { return s.Phase == "running" && s.ChildPID != 0 })
	obs.firstPID = first.ChildPID

	sealSend(protocol.Envelope{
		ID:   slashCommandListSendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: slashCommandListConvID,
			MessageID:      "m-slashcommands-1",
			Text:           slashCommandListUserText,
		}),
	})

	// Drive the turn to completion. Its purpose is the CURSOR, not its content:
	// sessionRouter.Route's success path is what stamps the active conversation, and
	// until it does the drain's gate fails closed on every event. turn_end is the
	// terminator rather than a frame count, so an interleaved broadcast cannot make
	// this flaky.
	preKillDeadline := time.Now().Add(30 * time.Second)
	for !obs.sawTurnEnd {
		env, ok := nextEnv(preKillDeadline)
		if !ok {
			t.Logf("pre-kill drain deadline reached: slash_command_list=%d echo=%v turn_end=%v",
				obs.preKillLists, obs.sawEcho, obs.sawTurnEnd)
			break
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope: %s", string(env.Payload))
		case protocol.TypeSlashCommandList:
			// Counted, never decoded and never printed: the expected count is zero (the
			// bootstrap child reports before any conversation is routed, so the
			// no-cursor return takes it), but a non-zero reading here is a claim about
			// that drop's TIMING rather than about this ticket's behaviour.
			obs.preKillLists++
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if strings.Contains(p.Text, slashCommandListEchoNeedle) {
				obs.sawEcho = true
			}
		case protocol.TypeTurnEnd:
			obs.sawTurnEnd = true
		}
	}

	// The crash the daemon must recover from, delivered without touching the daemon.
	// The phone is deliberately NOT read during this window — whatever the daemon sends
	// meanwhile buffers on the socket, and the control-plane poll and the phone receives
	// both run on this one goroutine, so they never interleave.
	killChild(t, obs.firstPID)
	second := waitForRunnerStatus(t, h, 20*time.Second, "a fresh child after the kill",
		func(s *control.StatusPayload) bool {
			return s.RestartCount >= 1 && s.ChildPID != 0 && s.ChildPID != first.ChildPID
		})
	obs.secondPID = second.ChildPID

	// The respawn fires one initialize ask on the fresh child's stdin (per spawn, from
	// streamsup's Config.RequestInitializeOnSpawn), so exactly one reply is in flight.
	// First arrival plus content is the honest shape here: nothing terminates this
	// window the way turn_end terminates a turn, so a total count would be a claim about
	// timing rather than about behaviour.
	postKillDeadline := time.Now().Add(30 * time.Second)
	for !obs.found {
		env, ok := nextEnv(postKillDeadline)
		if !ok {
			t.Logf("post-kill drain deadline reached: pre-kill slash_command_list=%d pids=%d→%d",
				obs.preKillLists, obs.firstPID, obs.secondPID)
			break
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope: %s", string(env.Payload))
		case protocol.TypeSlashCommandList:
			if err := json.Unmarshal(env.Payload, &obs.list); err != nil {
				// A decode failure at this layer IS the defect, not a miss. The raw
				// bytes are deliberately NOT printed: they carry the whole inventory,
				// and this shape is the one internal/e2e/realclaude siblings copy.
				t.Fatalf("decode slash_command_list payload: %v", err)
			}
			obs.found = true
		}
	}
	return obs
}

// TestRelayV2_StreamSlashCommandListReachesConnectedPhone is the hermetic end-to-end
// proof that the slash-command inventory claude reports at initialize reaches a client
// that was already connected when the daemon obtained it, with every canned entry
// intact and each alias-bearing entry's aliases attached to THAT entry and to no other.
//
// The three ways this can go red are genuinely hard to tell apart from a bare "want 1,
// got 0", so each failure message names its own suspect: the pre-kill turn never
// completing (a seed mismatch), the respawn never happening (waitForRunnerStatus fatals
// with the daemon's stderr tail), or the respawn happening with no frame arriving — the
// AC3 failure proper.
func TestRelayV2_StreamSlashCommandListReachesConnectedPhone(t *testing.T) {
	obs := driveSlashCommandListRespawn(t)

	// Milestones first and fatally: a missing frame read off a run where the turn never
	// completed says nothing, because the drain's gate is still closed.
	if !obs.sawEcho {
		t.Fatalf("the pre-kill reply never arrived (no assistant_delta carrying %q) — the turn did not "+
			"complete, so the active-conversation cursor was never stamped and the drain gate stayed "+
			"closed; slash_command_list=%d turn_end=%v (a UUID mismatch between seedBootstrapRegistry and "+
			"seedBoundConversation is the first suspect)",
			slashCommandListEchoNeedle, obs.preKillLists, obs.sawTurnEnd)
	}
	if !obs.sawTurnEnd {
		t.Fatalf("the pre-kill reply arrived but the turn never closed (no turn_end) — the run did not "+
			"complete; slash_command_list=%d", obs.preKillLists)
	}

	if !obs.found {
		t.Fatalf("no slash_command_list reached the phone after the respawn (pids %d→%d, pre-kill "+
			"slash_command_list=%d) — the fresh child answered the initialize ask, so suspect first that "+
			"the fake's canned answer carries no `commands` key at all, since internal/streamsup's "+
			"emitSlashCommandList suppresses a zero-length list at its own precondition and emits nothing "+
			"to map or send; then the emitter arm (interactiveTurnEmitterV2.Handle's "+
			"turnevent.SlashCommandList case) or the mapper arm (turnbridge.MapEvent's) not putting the "+
			"frame on the wire; and last the upstream droppable refusal: turnMarkFor answers turnMarkNone "+
			"for SlashCommandList, so streamTurnSink's sinkFor classes it droppable and can refuse it at "+
			"droppableCap under load — which a quiet hermetic run should never approach, so a flaky red "+
			"there is a signal about the sink rather than about this test",
			obs.firstPID, obs.secondPID, obs.preKillLists)
	}

	// The conversation identity is the BRIDGE's contribution: the parser's event carries
	// no conversation identity at all, so this proves the mapper supplied it rather than
	// leaving it empty.
	if obs.list.ConversationID != slashCommandListConvID {
		t.Errorf("conversation_id: got %q, want %q", obs.list.ConversationID, slashCommandListConvID)
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

	// BY INDEX, which is what makes this an ATTRIBUTION claim rather than a presence
	// one. No payload is dumped on any failure path — each row reports its own field.
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
			// The "and to no other" half, and the only assertion that separates
			// per-entry attribution from a key-blind flat reading: a payload that
			// carried the right aliases somewhere but attached them to every row would
			// pass every check above and redden precisely here.
			if len(got.Aliases) != 0 {
				t.Errorf("commands[%d] (%q).aliases: got %q, want empty — the fake OMITS the key for "+
					"this entry, so aliases belonging to another entry have leaked onto it",
					i, want.name, got.Aliases)
			}
			// A live claim, not decoration: json.Unmarshal yields a nil slice for null
			// and an empty non-nil slice for [], so this is what distinguishes them —
			// and it pins protocol.SlashCommand.MarshalJSON's nil→[] normalisation
			// through a daemon to a phone, where the unit tests pin it at the byte level.
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
