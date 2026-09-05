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

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// The model-list chain was built in layers — the ask on spawn (#1839), the
// per-session retention (#1840), the wire mapping (#1848), the conversation-keyed
// resolver (#1857), the connect-time reconcile (#1863) and the enumeration that
// fills its seam (#1867) — each proved at its own seam against its own doubles.
// This is the proof of the CHAIN across process boundaries for the client the
// reconcile exists FOR: one that was not connected when the daemon obtained the
// list. A closure built over the wrong registry, a capability gate that never
// opens, an envelope dropped before it is sealed — every one of those leaves the
// unit tests green.
//
// THIS IS THE INVERSE OF TestRelayV2_StreamModelListReachesConnectedPhone, and the
// inversion is the whole point. That spec proves the LIVE lane, which emits once
// per child spawn to whatever interactive conns exist AT THAT INSTANT, so it has to
// kill a child and observe the respawn's frame in order to see one at all. This
// spec proves the RECONCILE, so it must do the opposite: let the list exist FIRST,
// then connect, and observe the frame with the connecting client neither driving a
// turn nor sending a message. If this test ever needs a child to spawn while the
// observing client is connected it has become a second copy of that one and proves
// nothing about the enumeration.
//
// TWO CONNS, AND THAT IS THE WHOLE SEQUENCING STORY. reconcileModelLists fires once,
// on the success tail of a conn's interactive handshake — re-handshaking or re-using
// the minting conn does not re-trigger it. So the conversation is minted from phone
// A and the frame is observed on phone B, which handshakes afterwards.
//
// A CONVERSATION MUST EXIST FIRST. retainedModelLists enumerates the CONVERSATION
// registry, and the pool's bootstrap session has no conversation record at all —
// handlers.CreateConversation is what writes CurrentSessionID. A daemon that has
// minted nothing therefore retains nothing this path can see, and a test built on the
// bootstrap child alone would wait out its whole deadline for a frame that is
// correctly never sent. An over-the-wire create_conversation is a spawning
// Pool.Activate, so it both mints the record and produces the child whose initialize
// reply is retained.
const (
	// Distinct from the live-lane sibling's bootstrap UUID: the two specs run in the
	// same package and a shared literal would read as a shared fixture it is not.
	lateModelListBootstrapUUID = "18680000-0000-4000-8000-000000000001"
	lateModelListUserText      = "e2e-late-modellist:hello\n"
	lateModelListEchoNeedle    = "e2e-late-modellist:hello"
	lateModelListCreateReqID   = uint64(1868)
	lateModelListSendReqID     = uint64(1869)
)

// lateModelListObservation is what one wire-minted conversation, one driven turn and
// one LATER interactive handshake put on the observing client.
//
// The milestones are VALUES rather than helper-side assertions, modelListObservation's
// reason: a frame count read off a run where the turn never completed proves nothing,
// so the caller asserts them first and fatally, and a helper that fataled on them
// would leave those checks dead code.
type lateModelListObservation struct {
	convID  string // the server-minted id the frame must be stamped with
	sawEcho bool   // milestone: the minted child replied
	// milestone: the turn closed ⇒ the initialize ack was already through the parser
	sawTurnEnd bool
	// Diagnostic ONLY, never asserted. It should be zero: the live lane's emit for the
	// minted child fires at spawn, when the active-conversation cursor is still empty,
	// so interactiveTurnEmitterV2.Handle's empty-conversation early return drops it.
	// Worth logging because it separates "the reconcile did not fire" from "the live
	// lane fired instead" — but an assertion on it would redden this spec for a change
	// in a lane it does not own.
	listsOnMinter int
	list          protocol.ModelListPayload
	found         bool
}

// sealedConnDriver returns the two closures every interactive conn in this file
// needs: seal-and-send, and decrypt-the-next-envelope. One factory rather than two
// transcriptions, because this is the first spec in the family to drive TWO conns and
// each conn has its OWN CipherState pair. The receive nonce is a lockstep counter, so
// sharing one state across both phones — or filtering before decrypting — desyncs it
// and presents as a misleading decrypt error rather than as a clean failure. Pairing
// each phone with its own states at one call site makes that structural.
//
// nextEnv keeps driveModelListRespawn's two load-bearing properties unchanged: one
// deadline per collection window with every receive bounded by what is left of it
// (fakephone.Client.ReceiveBytes closes the underlying connection when its read
// context is cancelled, so a timed-out client cannot be reused and the first timeout
// must be terminal for the window), and non-noise_msg inner frames skipped WITHOUT
// decrypting while every noise_msg is decrypted in arrival order.
func sealedConnDriver(t *testing.T, phone *fakephone.Client, name string, send, recv *noise.CipherState) (
	func(protocol.Envelope), func(time.Time) (protocol.Envelope, bool)) {
	t.Helper()

	sealSend := func(env protocol.Envelope) {
		t.Helper()
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("%s marshal envelope: %v", name, err)
		}
		ciphertext, err := send.Encrypt(raw)
		if err != nil {
			t.Fatalf("%s seal envelope: %v", name, err)
		}
		sendNoiseMsg(t, phone, ciphertext)
	}

	nextEnv := func(deadline time.Time) (protocol.Envelope, bool) {
		t.Helper()
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return protocol.Envelope{}, false
			}
			raw, err := phone.ReceiveBytes(remaining)
			if err != nil {
				if errors.Is(err, fakephone.ErrReceiveTimeout) {
					return protocol.Envelope{}, false
				}
				t.Fatalf("%s receive: %v", name, err)
			}
			var inner protocol.InnerFrameV2
			if err := json.Unmarshal(raw, &inner); err != nil {
				t.Fatalf("%s decode inner frame: %v", name, err)
			}
			if inner.Type != protocol.TypeNoiseMsg {
				continue
			}
			return decryptInnerEnvelope(t, inner, recv), true
		}
	}

	return sealSend, nextEnv
}

// driveLateConnectModelList starts a stream-interactive daemon, mints a conversation
// over the wire from phone A and drives one turn on it so the list exists, THEN dials
// phone B and handshakes it interactive, and collects what B's own handshake puts on
// its socket. B sends nothing but that handshake.
//
// THE HAPPENS-BEFORE THAT REPLACES A SLEEP. fakeclaude's runStreamJSON is a single
// loop that writes every reply on the goroutine that read the line, in order. The
// initialize control request is written to the child's stdin at spawn, before any user
// turn can be routed, so the ack line precedes the turn's echo and result lines on that
// child's stdout. The daemon parses them in order and sessionModelHold.Sink stores the
// list BEFORE forwarding the event downstream. Therefore turn_end observed on phone A
// ⇒ the ack was parsed ⇒ the hold holds the list ⇒ retainedModelLists will enumerate
// it for the next handshake. No time.Sleep and no retry loop belongs anywhere in this
// test; if one appears, that ordering argument has been broken and the fix is to
// restore it, not to poll harder.
//
// It asserts none of the acceptance criteria. It t.Fatalf's only on transport, seal and
// decode faults, on an error envelope, and on the mint never completing — never on an
// acceptance criterion. On the turn or collection deadline it returns what it has and
// logs the counts, leaving the diagnosis to the caller.
func driveLateConnectModelList(t *testing.T) lateModelListObservation {
	t.Helper()

	home := shortHome(t)

	// TWO devices, both paired BEFORE the daemon starts — the daemon loads its
	// registries once at startup, seedBoundConversation's stated reason for its own
	// ordering. TestPairRevoke_E2E's "removes one of two" subtest is the precedent that
	// two pair runs into one home yield two usable devices.
	rA := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if rA.ExitCode != 0 {
		t.Fatalf("pyry pair phone-a exit=%d\nstdout:\n%s\nstderr:\n%s", rA.ExitCode, rA.Stdout, rA.Stderr)
	}
	rB := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-b")
	if rB.ExitCode != 0 {
		t.Fatalf("pyry pair phone-b exit=%d\nstdout:\n%s\nstderr:\n%s", rB.ExitCode, rB.Stdout, rB.Stderr)
	}
	payloadA := decodePairPayload(t, rA.Stdout)
	payloadB := decodePairPayload(t, rB.Stdout)
	// One server static keypair serves both devices — pyry mints a token per pair run,
	// but the static key is the daemon's and is the same in both payloads.
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// No seedBoundConversation: the conversation observed here is MINTED over the wire
	// below, so its binding is created by create_conversation rather than seeded —
	// TestRelayV2_StreamInterruptStopsRunningTurn's reasoning, inherited. No extra env
	// either: fakeclaude's runStreamJSON answers the initialize control request in both
	// of its modes and under no rider, so this spec adds no harness scaffolding at all.
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	h := StartStreamInteractiveWithRelay(t, home, lateModelListBootstrapUUID, fr.URL()+"/v2/server")
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	dialCtxA, cancelA := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelA()
	phoneA, err := fakephone.Dial(dialCtxA, fr.URL(), serverID, payloadA.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone A dial: %v", err)
	}
	t.Cleanup(func() { _ = phoneA.Close() })
	sendA, recvA := driveHandshakeToOpenDaemonInteractive(t, phoneA, pubKey, payloadA.Token)
	sealSendA, nextEnvA := sealedConnDriver(t, phoneA, "phone A", sendA, recvA)

	var obs lateModelListObservation

	// --- Mint the conversation over the wire. All-null create_conversation (server
	// defaults), the shape createConversationViaPhone and the interrupt spec both send.
	// Drained through this file's own nextEnv rather than through that helper because
	// the helper discards every non-matching frame, and listsOnMinter has to count
	// across BOTH of phone A's windows for the diagnostic to mean anything.
	sealSendA(protocol.Envelope{
		ID:      lateModelListCreateReqID,
		Type:    protocol.TypeCreateConversation,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.CreateConversationPayload{}),
	})
	createDeadline := time.Now().Add(15 * time.Second)
	for obs.convID == "" {
		env, ok := nextEnvA(createDeadline)
		if !ok {
			t.Fatalf("no conversation_created within 15s (model_list on the minter: %d) — the minted "+
				"stream-json session never came up. The mint is a spawning Pool.Activate, so suspect the "+
				"child rather than the reconcile seam", obs.listsOnMinter)
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope while awaiting conversation_created: %s", string(env.Payload))
		case protocol.TypeModelList:
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
	// ack has been parsed yet. Without this step phone B can win the race and see
	// nothing. turn_end is the terminator rather than a frame count, so an interleaved
	// broadcast cannot make it flaky.
	sealSendA(protocol.Envelope{
		ID:   lateModelListSendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: obs.convID,
			MessageID:      "m-late-modellist-1",
			Text:           lateModelListUserText,
		}),
	})
	turnDeadline := time.Now().Add(30 * time.Second)
	for !obs.sawTurnEnd {
		env, ok := nextEnvA(turnDeadline)
		if !ok {
			t.Logf("turn window deadline reached: conv=%s echo=%v turn_end=%v model_list on the minter=%d",
				obs.convID, obs.sawEcho, obs.sawTurnEnd, obs.listsOnMinter)
			break
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope during the turn: %s", string(env.Payload))
		case protocol.TypeModelList:
			obs.listsOnMinter++
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if strings.Contains(p.Text, lateModelListEchoNeedle) {
				obs.sawEcho = true
			}
		case protocol.TypeTurnEnd:
			obs.sawTurnEnd = true
		}
	}

	// --- Phone B connects only NOW, after the list exists. Its seal-and-send closure is
	// deliberately DISCARDED: B sends nothing but its handshake, and dropping the closure
	// on the floor makes that structural rather than a promise in a comment. Whatever the
	// daemon sends phone A from here on buffers unread on A's socket — the same posture
	// driveModelListRespawn takes across its kill window — so the two windows never
	// interleave and this stays one goroutine.
	dialCtxB, cancelB := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelB()
	phoneB, err := fakephone.Dial(dialCtxB, fr.URL(), serverID, payloadB.Token, "phone-b")
	if err != nil {
		t.Fatalf("phone B dial: %v", err)
	}
	t.Cleanup(func() { _ = phoneB.Close() })
	sendB, recvB := driveHandshakeToOpenDaemonInteractive(t, phoneB, pubKey, payloadB.Token)
	_, nextEnvB := sealedConnDriver(t, phoneB, "phone B", sendB, recvB)

	// handleNoiseInit's success tail records s.interactive, creates the push queue and
	// calls reconcileModelLists, which pushes one envelope per retained payload — and it
	// sends noise_resp BEFORE those pushes, which is what makes B's decrypt order
	// deterministic. driveHandshakeToOpenDaemonInteractive consumed exactly that
	// noise_resp and nothing after it, so this loop starts on a nonce that is in sequence.
	// First arrival plus content is the honest shape: nothing terminates this window the
	// way turn_end terminates a turn, so a total count would be a claim about timing
	// rather than about behaviour.
	collectDeadline := time.Now().Add(20 * time.Second)
	for !obs.found {
		env, ok := nextEnvB(collectDeadline)
		if !ok {
			t.Logf("phone B collection deadline reached: conv=%s model_list on the minter=%d",
				obs.convID, obs.listsOnMinter)
			break
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope on phone B: %s", string(env.Payload))
		case protocol.TypeModelList:
			if err := json.Unmarshal(env.Payload, &obs.list); err != nil {
				// A decode failure at this layer IS the defect, not a miss.
				t.Fatalf("decode model_list payload: %v\nraw: %s", err, string(env.Payload))
			}
			obs.found = true
		}
	}
	return obs
}

// TestRelayV2_StreamModelListReachesLateConnectingPhone is the hermetic end-to-end
// proof that the menu claude reports at initialize reaches a client that connected
// AFTER the daemon obtained it, without that client asking for anything.
//
// Every failure of this test is a timeout somewhere and the three are hard to tell
// apart from a bare "want 1, got 0", so each message names its own suspect.
func TestRelayV2_StreamModelListReachesLateConnectingPhone(t *testing.T) {
	obs := driveLateConnectModelList(t)

	// Milestones first and fatally: without a completed turn the ordering argument
	// never engaged, so the retention cannot be assumed and an AC-1 result read off
	// such a run is uninterpretable rather than a failure.
	if !obs.sawEcho {
		t.Fatalf("the minted child never replied (no assistant_delta carrying %q for conversation %s) — the "+
			"turn did not run, so the initialize ack cannot be assumed parsed and nothing downstream is "+
			"diagnosable; turn_end=%v model_list on the minter=%d",
			lateModelListEchoNeedle, obs.convID, obs.sawTurnEnd, obs.listsOnMinter)
	}
	if !obs.sawTurnEnd {
		t.Fatalf("the minted child replied but the turn never closed (no turn_end for conversation %s) — the "+
			"retention cannot be assumed, so the AC-1 result below would be uninterpretable; model_list on "+
			"the minter=%d", obs.convID, obs.listsOnMinter)
	}

	// AC 1: the frame reached a conn that connected after the list existed and sent
	// nothing but its handshake.
	if !obs.found {
		t.Fatalf("no model_list reached the LATE-connecting phone (conversation %s, turn complete, "+
			"model_list on the minter=%d) — suspect in order: the RetainedModelLists field of the V2 "+
			"session config in startRelayV2 being unset or nil, because reconcileModelLists returns early "+
			"on a nil seam and that is exactly the one-line mutant this test exists to redden; "+
			"retainedModelLists enumerating nothing because the minted conversation resolved to no "+
			"retained list; and the interactive grant on this conn. What it is NOT is the droppable cap: "+
			"turnMarkFor answers turnMarkNone for this variant so streamTurnSink's sinkFor can refuse it at "+
			"droppableCap, but the reconcile pushes straight onto the conn's queue via Push and never "+
			"through that sink — so a miss HERE points at the seam, while a drop on the live lane would "+
			"show up as a zero in the minter count above",
			obs.convID, obs.listsOnMinter)
	}

	// retainedModelLists' own contribution, and NOT inherited from the live-lane
	// sibling: the reconcile carries no turn context at all, so the id can only have
	// come from the daemon's registry record via resolveBoundModelList. The bootstrap
	// session contributes no payload OF ITS OWN (it has no conversation record, so
	// Registry.List never yields it and there is no id to stamp), so exactly one
	// payload is expected and the first arrival is it. #2124 narrowed that sentence
	// and it is worth reading in full here: the bootstrap's RETAINED LIST is now
	// readable on another conversation's behalf, so this harness's single minted
	// conversation would still contribute exactly one payload even if its own child
	// had reported nothing — what the count pins is the number of registry ROWS, not
	// the number of sessions holding a list.
	if obs.list.ConversationID != obs.convID {
		t.Errorf("conversation_id: got %q, want %q (the server-minted id)", obs.list.ConversationID, obs.convID)
	}
	// Two canned entries against the producer's entry cap, so nothing was cut. This
	// also guards the resolver's explicit rule that the value rides through from the
	// decode and is never recomputed from len(Models) and never zeroed.
	if obs.list.DroppedModels != 0 {
		t.Errorf("dropped_models: got %d, want 0 — the fake cans two entries, an order of magnitude under "+
			"the producer's entry cap", obs.list.DroppedModels)
	}
	if len(obs.list.Models) != 2 {
		t.Fatalf("models: got %d entries, want 2 (claude's own order, which MapEvent is documented not to "+
			"sort)\n%+v", len(obs.list.Models), obs.list.Models)
	}

	// AC 2, the rich row: the capture's eight-key shape, all five levels in claude's own
	// order — measured non-alphabetical, hence an ordered comparison rather than a set one.
	rich := obs.list.Models[0]
	if rich.Value != modelListRichValue {
		t.Errorf("models[0].value: got %q, want %q (claude's order, unsorted)", rich.Value, modelListRichValue)
	}
	if rich.ResolvedModel != modelListRichResolved {
		t.Errorf("models[0].resolved_model: got %q, want %q", rich.ResolvedModel, modelListRichResolved)
	}
	if rich.DisplayName != modelListRichDisplay {
		t.Errorf("models[0].display_name: got %q, want %q", rich.DisplayName, modelListRichDisplay)
	}
	if !slices.Equal(rich.EffortLevels, modelListWantEffortLevels) {
		t.Errorf("models[0].effort_levels: got %q, want %q (claude's own order)",
			rich.EffortLevels, modelListWantEffortLevels)
	}
	if !rich.SupportsAutoMode {
		t.Errorf("models[0].supports_auto_mode: got false, want true — the row publishes the key explicitly")
	}
	if rich.TruncatedFields != nil {
		t.Errorf("models[0].truncated_fields: got %#v, want nil — nothing was cut, and this field is "+
			"deliberately NOT normalised the way effort_levels is, so the absence must reach the wire as null",
			rich.TruncatedFields)
	}

	// AC 2, the lean row — the load-bearing one: claude omits supportedEffortLevels and
	// supportsAutoMode ENTIRELY for it, and the collapse of absent / null / [] onto one
	// reading (#1828) is what this re-proves on the RECONCILE's marshal, which is a
	// different call site from the live lane's.
	lean := obs.list.Models[1]
	if lean.Value != modelListLeanValue {
		t.Errorf("models[1].value: got %q, want %q (claude's order, unsorted)", lean.Value, modelListLeanValue)
	}
	if lean.ResolvedModel != modelListLeanResolved {
		t.Errorf("models[1].resolved_model: got %q, want %q", lean.ResolvedModel, modelListLeanResolved)
	}
	if lean.DisplayName != modelListLeanDisplay {
		t.Errorf("models[1].display_name: got %q, want %q", lean.DisplayName, modelListLeanDisplay)
	}
	if len(lean.EffortLevels) != 0 {
		t.Errorf("models[1].effort_levels: got %q, want empty — the fake's entry omits supportedEffortLevels "+
			"entirely", lean.EffortLevels)
	}
	// A live claim, not decoration: json.Unmarshal yields a nil slice for null and an
	// empty non-nil slice for [], so this is what distinguishes them — and here it pins
	// ModelOption.MarshalJSON's nil→[] normalisation through reconcileModelLists' OWN
	// marshal, a call site the live-lane sibling never exercises.
	if lean.EffortLevels == nil {
		t.Errorf("models[1].effort_levels: got null, want [] — an absent supportedEffortLevels must reach " +
			"the wire as an empty array so no client has to branch on an optional array")
	}
	if lean.SupportsAutoMode {
		t.Errorf("models[1].supports_auto_mode: got true, want false — the row omits the key, and the field " +
			"is a permission GRANT whose unsafe inverse would be granting on silence")
	}
	if lean.TruncatedFields != nil {
		t.Errorf("models[1].truncated_fields: got %#v, want nil", lean.TruncatedFields)
	}
}
