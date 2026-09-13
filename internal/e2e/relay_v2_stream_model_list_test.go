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

// Four tickets built the model-list path in layers — the ask on spawn (#1839),
// the per-session retention (#1840), the wire mapping (#1848), the emit on the
// live interactive lane (#1849) — and each is proved at its own seam against its
// own doubles. This is the first proof of the CHAIN, across process boundaries:
// a real pyry supervising a real child, a real client connection, no credentials
// and no network. The failure class it covers is the one every unit test stays
// green through — a sink wired to the wrong hold, a capability gate that never
// opens, an envelope dropped before it is sealed.
//
// THE SEQUENCING PROBLEM, and why this test kills a child. The live lane emits
// once per child spawn, to whatever interactive connections exist AT THAT
// INSTANT; there is no backfill for a client that connects later (#1846 owns
// that). The daemon spawns its bootstrap child eagerly at startup, so by the time
// a test can pair, dial and handshake, the ask, the reply and the emit have all
// already happened. A test that starts a daemon, connects a phone, drives a turn
// and waits for a model_list waits forever. The frame has to be made observable
// by making a child spawn WHILE the client is connected, and only one of the two
// candidate routes delivers:
//
//   - A kill and its respawn — this test's route. The respawned child keeps the
//     session id, so its events still match the drain's gate: startStreamTurnDrainV2
//     compares the runner's construction-time session tag against
//     boundSessionIDForActive, which resolves the active conversation's bound
//     session. Same id on both sides, before and after the kill.
//   - A new_session rotation — rejected. relay_v2_stream_new_session_test.go's own
//     header records that after a rotation the runner's sink tag stays on the
//     OUTGOING id while the conversation rebinds to the new one, so every event the
//     fresh child produces is dropped at that gate. That is why that test asserts
//     the post-rotation child's stdin rather than a phone-side frame; a model_list
//     observation built on it would be asserting into a lane that is dropping.
//
// Either route needs the gate to have something to compare. activeConversation.set
// is called only from sessionRouter.Route's success path, so at least one turn must
// be driven from the connected client BEFORE the kill — which is also why the
// bootstrap child's own model_list can never reach the phone by accident: the cursor
// is still empty when it is emitted.
const (
	modelListInitialUUID = "18450000-0000-4000-8000-000000000001"
	modelListConvID      = "18450000-0000-4000-8000-000000000002"
	modelListUserText    = "e2e-modellist:hello\n"
	modelListEchoNeedle  = "e2e-modellist:hello"
	modelListSendReqID   = uint64(1845)

	// The two canned rows fakeclaude's initializeModels answers an initialize
	// control_request with, transcribed as literals because the fake is a separate
	// main package (same discipline as the bogus needles in
	// relay_v2_stream_unrecognized_test.go). Their two key sets are the whole point
	// of asserting both: sonnet carries the capture's rich shape, haiku carries the
	// four-key one that omits supportedEffortLevels and supportsAutoMode ENTIRELY.
	// The daemon reads an absent key, a JSON null and a published empty array as one
	// reading (#1828), and a test that only ever saw the rich row would prove nothing
	// about the shape a real reply actually varies in.
	modelListRichValue    = "sonnet"
	modelListRichResolved = "claude-sonnet-5"
	modelListRichDisplay  = "Sonnet"
	modelListLeanValue    = "haiku"
	modelListLeanResolved = "claude-haiku-4-5-20251001"
	modelListLeanDisplay  = "Haiku"
)

// modelListWantEffortLevels is the rich row's supportedEffortLevels in claude's own
// order — measured non-alphabetical, and turnbridge.MapEvent is documented not to
// sort, so this is compared as an ORDERED slice rather than as a set.
var modelListWantEffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// modelListObservation is what one driven turn plus one kill/respawn put on the
// wire: the first model_list seen AFTER the kill, a count of the ones seen during
// the pre-kill turn, the two pre-kill milestones, and the two child pids.
//
// The milestones are VALUES rather than helper-side assertions for
// rateLimitObservation's reason: a frame count read off a run where nothing
// completed proves nothing, so the caller asserts them first and fatally, and a
// helper that fataled on them would leave those checks dead code.
type modelListObservation struct {
	list         protocol.ModelListPayload
	found        bool
	preKillLists int
	sawEcho      bool
	sawTurnEnd   bool
	firstPID     int
	secondPID    int
}

// driveModelListRespawn spawns a stream-interactive daemon, drives one ordinary
// turn from a connected interactive v2 client so the drain's active-conversation
// cursor is stamped, SIGKILLs the supervised child, and collects what the respawn's
// initialize round trip puts on that same still-connected client.
//
// It asserts none of the acceptance criteria. It t.Fatalf's only on transport and
// decode faults — an error envelope, a payload that will not decode, a receive error
// that is not the deadline. On either collection deadline it returns with what it
// has and logs the counts, leaving the diagnosis to the caller's assertions.
func driveModelListRespawn(t *testing.T) modelListObservation {
	t.Helper()

	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payloadA, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payloadA.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// Bind the conversation to the bootstrap session so the drain gate passes. This
	// UUID MUST equal the one handed to StartStreamInteractiveWithRelay below: a
	// mismatch between the two seeds drops every event at the gate and hangs the
	// drain for the full deadline, which presents as an unexplained timeout rather
	// than a clean seed-time failure. The call also has to sit BETWEEN paired-device setup
	// and the daemon start — seedBoundConversation writes into <home>/.pyry/test/
	// without creating it, and the daemon loads conversations.json once at startup.
	seedBoundConversation(t, home, modelListConvID, modelListInitialUUID)

	// No extra env: fakeclaude answers the initialize control_request in both of its
	// modes and under no rider, so this test adds no harness scaffolding at all.
	h := StartStreamInteractiveWithRelay(t, home, modelListInitialUUID, relayURL)
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
	// Interactive — load-bearing, not incidental. emit() filters every frame on the
	// interactive capability, so a non-interactive handshake yields zero model_list
	// frames and a vacuously red test that looks exactly like a missing emitter arm.
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
			// is left of it — never a short fixed timeout in a poll loop. coder/websocket
			// closes the underlying connection when the read context is cancelled
			// (fakephone.Client.ReceiveBytes' own doc), so a timed-out client cannot be
			// reused and the first timeout must be terminal for the window.
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

	var obs modelListObservation

	first := waitForRunnerStatus(t, h, 20*time.Second, "first child running",
		func(s *control.StatusPayload) bool { return s.Phase == "running" && s.ChildPID != 0 })
	obs.firstPID = first.ChildPID

	sealSend(protocol.Envelope{
		ID:   modelListSendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: modelListConvID,
			MessageID:      "m-modellist-1",
			Text:           modelListUserText,
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
			t.Logf("pre-kill drain deadline reached: model_list=%d echo=%v turn_end=%v",
				obs.preKillLists, obs.sawEcho, obs.sawTurnEnd)
			break
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope: %s", string(env.Payload))
		case protocol.TypeModelList:
			obs.preKillLists++
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if strings.Contains(p.Text, modelListEchoNeedle) {
				obs.sawEcho = true
			}
		case protocol.TypeTurnEnd:
			obs.sawTurnEnd = true
		}
	}

	// The crash the daemon must recover from, delivered without touching the daemon.
	// The phone is deliberately NOT read during this window — whatever the daemon
	// sends meanwhile buffers on the socket, and the control-plane poll and the phone
	// receives both run on this one goroutine, so they never interleave.
	killChild(t, obs.firstPID)
	second := waitForRunnerStatus(t, h, 20*time.Second, "a fresh child after the kill",
		func(s *control.StatusPayload) bool {
			return s.RestartCount >= 1 && s.ChildPID != 0 && s.ChildPID != first.ChildPID
		})
	obs.secondPID = second.ChildPID

	// The respawn fires one initialize ask on the fresh child's stdin (per spawn, from
	// streamsup's Config.RequestInitializeOnSpawn), so exactly one reply is in flight.
	// First arrival plus content is the honest shape here: nothing terminates this
	// window the way turn_end terminates a turn, so a total count would be a claim
	// about timing rather than about behaviour.
	postKillDeadline := time.Now().Add(30 * time.Second)
	for !obs.found {
		env, ok := nextEnv(postKillDeadline)
		if !ok {
			t.Logf("post-kill drain deadline reached: pre-kill model_list=%d pids=%d→%d",
				obs.preKillLists, obs.firstPID, obs.secondPID)
			break
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope: %s", string(env.Payload))
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

// TestRelayV2_StreamModelListReachesConnectedPhone is the hermetic end-to-end proof
// that the menu claude reports at initialize reaches a client that was already
// connected when the daemon obtained it, with both canned rows intact.
//
// The three ways this can go red are genuinely hard to tell apart from a bare
// "want 1, got 0", so each failure message names its own suspect: the pre-kill turn
// never completing (a seed mismatch), the respawn never happening
// (waitForRunnerStatus fatals with the daemon's stderr tail), or the respawn
// happening with no frame arriving — the AC-1 failure proper.
func TestRelayV2_StreamModelListReachesConnectedPhone(t *testing.T) {
	obs := driveModelListRespawn(t)

	// Milestones first and fatally: a missing frame read off a run where the turn
	// never completed says nothing, because the drain's gate is still closed.
	if !obs.sawEcho {
		t.Fatalf("the pre-kill reply never arrived (no assistant_delta carrying %q) — the turn did not "+
			"complete, so the active-conversation cursor was never stamped and the drain gate stayed "+
			"closed; model_list=%d turn_end=%v (a UUID mismatch between seedBootstrapRegistry and "+
			"seedBoundConversation is the first suspect)",
			modelListEchoNeedle, obs.preKillLists, obs.sawTurnEnd)
	}
	if !obs.sawTurnEnd {
		t.Fatalf("the pre-kill reply arrived but the turn never closed (no turn_end) — the run did not "+
			"complete; model_list=%d", obs.preKillLists)
	}

	if !obs.found {
		t.Fatalf("no model_list reached the phone after the respawn (pids %d→%d, pre-kill model_list=%d) — "+
			"the fresh child answered the initialize ask, so suspect first the emitter arm "+
			"(interactiveTurnEmitterV2.Handle's turnevent.ModelList case) or the mapper arm "+
			"(turnbridge.MapEvent's) not putting the frame on the wire, and second the upstream droppable "+
			"refusal: turnMarkFor answers turnMarkNone for ModelList, so streamTurnSink's sinkFor classes "+
			"it droppable and can refuse it at droppableCap under load — which a quiet hermetic run should "+
			"never approach, so a flaky red there is a signal about the sink rather than about this test",
			obs.firstPID, obs.secondPID, obs.preKillLists)
	}

	// The conversation identity is the BRIDGE's contribution: the parser's event
	// carries no conversation identity at all, so this proves the mapper supplied it
	// rather than leaving it empty.
	if obs.list.ConversationID != modelListConvID {
		t.Errorf("conversation_id: got %q, want %q", obs.list.ConversationID, modelListConvID)
	}
	// Two canned entries against streamsup's entry cap, so nothing was cut and the
	// list's true size is exactly what arrived.
	if obs.list.DroppedModels != 0 {
		t.Errorf("dropped_models: got %d, want 0 — the fake cans two entries, an order of magnitude "+
			"under the producer's entry cap", obs.list.DroppedModels)
	}
	if len(obs.list.Models) != 2 {
		t.Fatalf("models: got %d entries, want 2 (claude's own order, which MapEvent is documented not to "+
			"sort)\n%+v", len(obs.list.Models), obs.list.Models)
	}

	// The rich row: the capture's eight-key shape, all five levels in claude's own
	// order — measured non-alphabetical, hence an ordered comparison.
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

	// The lean row is the load-bearing one: claude omits supportedEffortLevels and
	// supportsAutoMode ENTIRELY for it, and the collapse of absent / null / [] onto one
	// reading (#1828) is what this proves end to end.
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
		t.Errorf("models[1].effort_levels: got %q, want empty — the fake's entry omits "+
			"supportedEffortLevels entirely", lean.EffortLevels)
	}
	// A live claim, not decoration: json.Unmarshal yields a nil slice for null and an
	// empty non-nil slice for [], so this is what distinguishes them — and it pins
	// ModelOption.MarshalJSON's nil→[] normalisation through a daemon to a phone, where
	// the unit tests pin it at the byte level.
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
