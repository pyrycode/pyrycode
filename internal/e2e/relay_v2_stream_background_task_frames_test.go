//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// claude's system/task_started, task_updated, task_progress and task_notification lines
// become background_task_started, background_task_updated (two producers: task_updated
// fills patch, task_notification fills status and summary) and background_task_progress.
// Every seam on that path — streamsup's emitSystemSubtype, turnbridge.MapEvent, cmd/pyry's
// interactive v2 emitter — has its own unit coverage, and before #1533 nothing drove one
// of these lines through a real daemon to a connected client. The realclaude drains
// decrypt these frames and skip them, so make preship did not close the gap either. A
// regression in the drain gate, the emitter or envelope handling, or a drift between the
// turnevent and protocol shapes, would leave every unit test green while phones stopped
// showing background tasks.
//
// THE FEED IS fakeclaude's REPLAY RIDER (#2503), not a new rider: the first user turn's
// canned reply is replaced by bgTaskFramesFragment, written byte for byte. The five system
// lines are committed capture payloads, byte for byte, including the literal $SESSION_ID
// and $FIFO — the parser never decodes uuid or session_id, so no frame can show them, and
// $FIFO arriving unexpanded in description and summary is part of the proof.
//
// THE ORDERING IS WHAT MAKES THE COUNTS EXACT. Every fed line precedes the result line,
// and the parser consumes child stdout in order on one goroutine, so turn_end reaching
// the client implies every fed line has already been through the parser. These frames
// are not turn-scoped and carry the bound conversation id, so nothing else on the run
// can produce one: no sleep, no poll, no settle window.
//
// NO PAYLOAD IS EVER PRINTED on a failure path. description and summary are literal
// command lines by nature (#833). Here they are fixture bytes, which is what makes the
// got/want field messages below safe; that shape must not be copied into
// internal/e2e/realclaude, where the same frames carry the operator's real workspace.
const (
	// Distinct from every sibling spec's ids: they share the package, and a shared
	// literal would read as a shared fixture it is not.
	bgTaskFramesBootstrapUUID = "15330000-0000-4000-8000-000000000001"
	bgTaskFramesConvID        = "15330000-0000-4000-8000-000000000002"
	bgTaskFramesUserText      = "e2e-bgtask-frames:hello\n"
	bgTaskFramesSendReqID     = uint64(1533)

	// The reply the fragment carries in place of fakeclaude's echo. Deliberately NOT the
	// user text: an echo would prove the canned path ran, which is the opposite of what
	// the milestone has to show.
	bgTaskFramesNeedle = "e2e-bgtask-frames:replayed-reply"

	bgTaskFramesReplayEnv = "PYRY_FAKE_CLAUDE_STREAM_REPLAY_FIRST"

	// Capture payloads, byte for byte.
	//
	// task_started and task_notification: internal/e2e/realclaude/testdata/
	// task_notification_v2.1.259.json, frames[] with those subtypes.
	bgTaskStartedLine      = `{"type":"system","subtype":"task_started","task_id":"buwavm27r","tool_use_id":"toolu_01RVFCkACkpA1cv8gmvXjKSj","description":"cat $FIFO","is_backgrounded":false,"task_type":"local_bash","uuid":"6caee1dd-849b-4262-be8e-5e55fc3fc9ae","session_id":"$SESSION_ID"}`
	bgTaskNotificationLine = `{"type":"system","subtype":"task_notification","task_id":"buwavm27r","tool_use_id":"toolu_01RVFCkACkpA1cv8gmvXjKSj","status":"completed","output_file":"","summary":"cat $FIFO","uuid":"23918def-8963-446b-99b9-727233bb7491","session_id":"$SESSION_ID"}`
	// task_updated: internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json,
	// dropped_lines[] with that subtype.
	bgTaskUpdatedLine = `{"type":"system","subtype":"task_updated","task_id":"bybi8g8i8","patch":{"is_backgrounded":true},"uuid":"705d0c1b-91e9-43f0-9114-5da06d0e3ac6","session_id":"$SESSION_ID"}`
	// Both task_progress lines: internal/e2e/realclaude/testdata/
	// parent_tool_use_v2.1.259.json, frames[] with that subtype, in capture order.
	bgTaskProgressLine1 = `{"type":"system","subtype":"task_progress","task_id":"a8eec1cd5e109aa38","tool_use_id":"toolu_01LRXMtrx8W1mm14U6LywqAP","description":"Reading alpha.txt","subagent_type":"general-purpose","usage":{"total_tokens":16207,"tool_uses":1,"duration_ms":3839},"last_tool_name":"Read","uuid":"5339f616-c118-49fb-bb7f-6549e7050100","session_id":"$SESSION_ID"}`
	bgTaskProgressLine2 = `{"type":"system","subtype":"task_progress","task_id":"a8eec1cd5e109aa38","tool_use_id":"toolu_01LRXMtrx8W1mm14U6LywqAP","description":"Reading beta.txt","subagent_type":"general-purpose","usage":{"total_tokens":16246,"tool_uses":2,"duration_ms":4546},"last_tool_name":"Read","uuid":"d6700b72-44ae-457f-941d-365dbc538324","session_id":"$SESSION_ID"}`

	// The two task ids the background_task_updated frames are told apart by. The
	// task_updated line comes from a different capture than the notification, and
	// nothing in the daemon correlates task ids across frames, so mixing them is fine.
	bgTaskPatchTaskID  = "bybi8g8i8"
	bgTaskNotifyTaskID = "buwavm27r"
)

// bgTaskFramesFragment is the whole replayed stdout of the first turn: the five capture
// lines, then a reply in the shape of fakeclaude's writeStreamResponse (writeAssistantEcho's
// assistant line, then outResult with streamSessionID). Both progress lines are fed, and
// the first sits below streamsup's minTaskToolCallsPerEvent, so it must produce no frame.
var bgTaskFramesFragment = strings.Join([]string{
	bgTaskStartedLine,
	bgTaskUpdatedLine,
	bgTaskProgressLine1,
	bgTaskProgressLine2,
	bgTaskNotificationLine,
	`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"` + bgTaskFramesNeedle + `"}]}}`,
	`{"type":"result","subtype":"success","session_id":"fake-stream"}`,
}, "\n") + "\n"

// bgTaskFramesObservation is what one driven turn put on the wire: every per-task
// background-task frame as RAW payload bytes, a count of the unrecognized lane, and the
// two milestones.
//
// RAW, for the session-facts spec's reason: the non-leak assertion needs each payload's
// own key set, and decoding into a protocol payload type discards any key it does not
// declare — precisely the thing that assertion is for.
//
// The milestones are VALUES rather than driver-side assertions so the test asserts them
// first and fatally in its own body; a count read off a run that never completed proves
// nothing.
type bgTaskFramesObservation struct {
	started      []json.RawMessage
	updated      []json.RawMessage
	progress     []json.RawMessage
	unrecognized int
	sawNeedle    bool
	sawTurnEnd   bool
}

// driveBackgroundTaskFramesTurn spawns a stream-interactive daemon whose fakeclaude
// replays bgTaskFramesFragment for the first user turn, drives that turn from a connected
// interactive v2 client, and returns what the turn put on the wire.
//
// It asserts no acceptance criterion. It t.Fatalf's only on setup, transport and decode
// faults and on an error envelope; on the drain deadline it logs the counts and returns
// with sawTurnEnd false, leaving the diagnosis to the caller's milestone assertions.
func driveBackgroundTaskFramesTurn(t *testing.T) bgTaskFramesObservation {
	t.Helper()

	// Written before the daemon starts: fakeclaude's loadStreamReplay reads the file once
	// at startup and an unreadable path fails the child, not the turn.
	fragmentPath := filepath.Join(t.TempDir(), "bgtask-frames.jsonl")
	if err := os.WriteFile(fragmentPath, []byte(bgTaskFramesFragment), 0o600); err != nil {
		t.Fatalf("write replay fragment: %v", err)
	}

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

	// Bind the conversation to the bootstrap session so the drain gate passes. The UUID
	// MUST equal the one handed to StartStreamInteractiveWithRelay: a mismatch drops
	// every event at the gate and presents as an unexplained drain timeout.
	seedBoundConversation(t, home, bgTaskFramesConvID, bgTaskFramesBootstrapUUID)

	h := StartStreamInteractiveWithRelay(t, home, bgTaskFramesBootstrapUUID, relayURL,
		bgTaskFramesReplayEnv+"="+fragmentPath)
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
	// Interactive, and load-bearing: these frames reach only a phone whose interactive
	// capability was granted in hello_ack. A non-interactive handshake yields zero frames
	// and a red that looks exactly like a dead emitter.
	sendA, recvA := driveHandshakeToOpenDaemonInteractive(t, phoneA, pubKey, payloadA.Token)
	sealSend, nextEnv := sealedConnDriver(t, phoneA, "phone A", sendA, recvA)

	sealSend(protocol.Envelope{
		ID:   bgTaskFramesSendReqID,
		Type: protocol.TypeSendMessage,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{
			ConversationID: bgTaskFramesConvID,
			MessageID:      "m-bgtask-frames-1",
			Text:           bgTaskFramesUserText,
		}),
	})

	// turn_end is the terminator rather than a frame count, so an interleaved broadcast
	// cannot make this flaky, and the fragment's ordering means every fed line is through
	// the parser by the time it lands.
	var obs bgTaskFramesObservation
	deadline := time.Now().Add(30 * time.Second)
	for !obs.sawTurnEnd {
		env, ok := nextEnv(deadline)
		if !ok {
			t.Logf("drain deadline reached: started=%d updated=%d progress=%d unrecognized=%d needle=%v turn_end=%v",
				len(obs.started), len(obs.updated), len(obs.progress), obs.unrecognized, obs.sawNeedle, obs.sawTurnEnd)
			break
		}
		switch env.Type {
		case protocol.TypeError:
			t.Fatalf("unexpected error envelope: %s", string(env.Payload))
		case protocol.TypeBackgroundTaskStarted:
			obs.started = append(obs.started, append(json.RawMessage(nil), env.Payload...))
		case protocol.TypeBackgroundTaskUpdated:
			obs.updated = append(obs.updated, append(json.RawMessage(nil), env.Payload...))
		case protocol.TypeBackgroundTaskProgress:
			obs.progress = append(obs.progress, append(json.RawMessage(nil), env.Payload...))
		case protocol.TypeUnrecognizedMessage:
			obs.unrecognized++
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if strings.Contains(p.Text, bgTaskFramesNeedle) {
				obs.sawNeedle = true
			}
		case protocol.TypeTurnEnd:
			obs.sawTurnEnd = true
		}
	}
	return obs
}

// TestRelayV2_StreamBackgroundTaskFramesReachConnectedPhone is the hermetic end-to-end
// proof that claude's per-task background-task lines reach a connected interactive v2
// client as exactly the frames the protocol promises: one background_task_started, two
// background_task_updated (one per producer), one background_task_progress (the second
// progress line only), each carrying the fed values verbatim, the bound conversation id,
// a null truncated_fields and no key its payload type does not declare.
func TestRelayV2_StreamBackgroundTaskFramesReachConnectedPhone(t *testing.T) {
	obs := driveBackgroundTaskFramesTurn(t)

	// Milestones first and fatally: without them every count below is uninterpretable.
	if !obs.sawNeedle {
		t.Fatalf("the replayed reply never arrived (no assistant_delta carrying %q) — the run did not complete, "+
			"so its counts prove nothing; started=%d updated=%d progress=%d unrecognized=%d turn_end=%v. "+
			"An echo of the user text instead means the replay env never reached fakeclaude",
			bgTaskFramesNeedle, len(obs.started), len(obs.updated), len(obs.progress), obs.unrecognized, obs.sawTurnEnd)
	}
	if !obs.sawTurnEnd {
		t.Fatalf("the replayed reply arrived but the turn never closed (no turn_end) — the run did not complete, "+
			"so its counts prove nothing; started=%d updated=%d progress=%d unrecognized=%d",
			len(obs.started), len(obs.updated), len(obs.progress), obs.unrecognized)
	}

	// No fed line may land in the unrecognized lane: system stays whole on the parser's
	// ignored types, so a non-zero here with a missing frame below says the line reached
	// the fallback rather than its task arm.
	if obs.unrecognized != 0 {
		t.Errorf("unrecognized_message frames: got %d, want 0", obs.unrecognized)
	}
	if len(obs.started) != 1 || len(obs.updated) != 2 || len(obs.progress) != 1 {
		t.Fatalf("frame counts: got started=%d updated=%d progress=%d, want 1/2/1 — one task_started line, "+
			"one task_updated plus one task_notification line, and two task_progress lines of which only "+
			"the second crosses minTaskToolCallsPerEvent",
			len(obs.started), len(obs.updated), len(obs.progress))
	}

	// Whole-struct comparisons, which pin every field verbatim at once. reflect.DeepEqual
	// tells a nil TruncatedFields from an empty non-nil one, and that distinction is the
	// point: every fed value sits far under its producer cap (the longest id is 30 bytes
	// against maxTaskFieldID's 256; description, patch and summary against 4096), so
	// nothing was cut and the wire value must be null, not []. If a fed value is ever
	// widened past a cap, these wants change with it.
	var started protocol.BackgroundTaskStartedPayload
	bgTaskDecode(t, "background_task_started", obs.started[0], &started)
	bgTaskAssertFrame(t, "background_task_started", started, protocol.BackgroundTaskStartedPayload{
		ConversationID: bgTaskFramesConvID,
		TaskID:         bgTaskNotifyTaskID,
		// claude's tool_use_id, under the daemon's own wire name.
		ToolCallID:  "toolu_01RVFCkACkpA1cv8gmvXjKSj",
		Description: "cat $FIFO",
		TaskType:    "local_bash",
	})

	// The two producers fill disjoint fields; task_id is what tells their frames apart.
	updated := make(map[string]protocol.BackgroundTaskUpdatedPayload, 2)
	for _, raw := range obs.updated {
		var p protocol.BackgroundTaskUpdatedPayload
		bgTaskDecode(t, "background_task_updated", raw, &p)
		if _, dup := updated[p.TaskID]; dup {
			t.Fatalf("two background_task_updated frames carry task_id %q — each producer's line must map to "+
				"its own frame", p.TaskID)
		}
		updated[p.TaskID] = p
	}
	for _, id := range []string{bgTaskPatchTaskID, bgTaskNotifyTaskID} {
		if _, ok := updated[id]; !ok {
			t.Fatalf("no background_task_updated frame carries task_id %q (got %d frames)", id, len(updated))
		}
	}
	// task_updated: claude's patch object carried whole as its serialized text.
	bgTaskAssertFrame(t, "background_task_updated (task_updated)", updated[bgTaskPatchTaskID],
		protocol.BackgroundTaskUpdatedPayload{
			ConversationID: bgTaskFramesConvID,
			TaskID:         bgTaskPatchTaskID,
			Patch:          `{"is_backgrounded":true}`,
		})
	// task_notification: the terminal state as its own fields, and no synthesized patch.
	bgTaskAssertFrame(t, "background_task_updated (task_notification)", updated[bgTaskNotifyTaskID],
		protocol.BackgroundTaskUpdatedPayload{
			ConversationID: bgTaskFramesConvID,
			TaskID:         bgTaskNotifyTaskID,
			Status:         "completed",
			Summary:        "cat $FIFO",
		})

	// The SECOND progress line's own values. The first (tool_uses 1) accumulates without
	// emitting; a frame carrying its values would mean the rate bound was bypassed.
	var progress protocol.BackgroundTaskProgressPayload
	bgTaskDecode(t, "background_task_progress", obs.progress[0], &progress)
	bgTaskAssertFrame(t, "background_task_progress", progress, protocol.BackgroundTaskProgressPayload{
		ConversationID: bgTaskFramesConvID,
		TaskID:         "a8eec1cd5e109aa38",
		Description:    "Reading beta.txt",
		SubagentType:   "general-purpose",
		LastToolName:   "Read",
		TotalTokens:    16246,
		ToolUses:       2,
		DurationMS:     4546,
	})

	// THE NON-LEAK ASSERTION. The fed lines carry session_id, uuid, output_file (a host
	// path by contract), is_backgrounded, usage and tool_use_id where the frame does not
	// declare it; a frame that grew any of them would pass every decoded comparison above
	// and fail only here.
	startedKeys := "conversation_id,description,task_id,task_type,tool_call_id,truncated_fields"
	updatedKeys := "conversation_id,patch,status,summary,task_id,truncated_fields"
	progressKeys := "conversation_id,description,duration_ms,last_tool_name,subagent_type,task_id,tool_uses," +
		"total_tokens,truncated_fields"
	bgTaskAssertKeys(t, "background_task_started", obs.started[0], startedKeys)
	for _, raw := range obs.updated {
		bgTaskAssertKeys(t, "background_task_updated", raw, updatedKeys)
	}
	bgTaskAssertKeys(t, "background_task_progress", obs.progress[0], progressKeys)
}

// bgTaskDecode decodes one frame's payload, failing WITHOUT printing its bytes: a decode
// failure at this layer is the defect, and the bytes carry command lines.
func bgTaskDecode(t *testing.T, name string, raw json.RawMessage, into any) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode %s payload: %v", name, err)
	}
}

// bgTaskAssertFrame compares a decoded frame with its want by reflect.DeepEqual, so a
// nil TruncatedFields and an empty one are different answers. %#v is what keeps that
// visible in the message: %v and %+v print both as [].
func bgTaskAssertFrame(t *testing.T, name string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got  %#v\n want %#v", name, got, want)
	}
}

// bgTaskAssertKeys asserts a payload's top-level key set, as a sorted comma-joined list.
// Only key names are printed on a miss, never values.
func bgTaskAssertKeys(t *testing.T, name string, raw json.RawMessage, want string) {
	t.Helper()
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keyed); err != nil {
		t.Fatalf("decode %s payload as an object: %v", name, err)
	}
	got := make([]string, 0, len(keyed))
	for k := range keyed {
		got = append(got, k)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != want {
		t.Errorf("%s payload key set: got %s, want %s — the frame must carry exactly what its payload type "+
			"declares and nothing the fed line carried besides", name, strings.Join(got, ","), want)
	}
}
