//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestRelayV2_StopBackgroundTaskCompletesOnlyAddressedTask(t *testing.T) {
	const sid = "27960000-0000-4000-8000-000000000010"
	const convA = "27960000-0000-4000-8000-000000000011"
	const taskID = "bybi8g8i8" // writeBackgroundTaskRoster's held first row.
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	paired, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: relayURL, DeviceName: "phone-a"})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := base64.StdEncoding.DecodeString(paired.ServerStaticPubkey)
	if err != nil {
		t.Fatal(err)
	}
	seedBoundConversation(t, home, convA, sid)
	logStem := filepath.Join(t.TempDir(), "stdin")
	h := StartStreamInteractiveWithRelay(t, home, sid, relayURL, "PYRY_FAKE_CLAUDE_STREAM_ROSTER=1", "PYRY_FAKE_CLAUDE_STREAM_INTERRUPT=1", "PYRY_FAKE_CLAUDE_STDIN_LOG="+logStem)
	t.Cleanup(func() { h.Stop(t) })
	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(ctx, fr.URL(), serverID, paired.Token, "phone-a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pub, paired.Token)
	seal, next := sealedConnDriver(t, phone, "stop client", send, recv)
	seal(protocol.Envelope{ID: 27960, Type: protocol.TypeCreateConversation, TS: time.Now().UTC(), Payload: mustJSON(t, protocol.CreateConversationPayload{})})
	var convB string
	deadline := time.Now().Add(15 * time.Second)
	for convB == "" {
		env, ok := next(deadline)
		if !ok {
			t.Fatal("no created conversation")
		}
		if env.Type == protocol.TypeError {
			t.Fatal("create refused")
		}
		if env.Type == protocol.TypeConversationCreated {
			var p protocol.ConversationCreatedPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatal(err)
			}
			convB = p.ID
		}
	}
	// Both children have the same held id; B becomes the cursor, so stopping A proves named routing.
	for i, conv := range []string{convA, convB} {
		seal(protocol.Envelope{ID: uint64(27961 + i), Type: protocol.TypeSendMessage, TS: time.Now().UTC(), Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: conv, MessageID: conv, Text: "hold\n"})})
		deadline = time.Now().Add(15 * time.Second)
		for {
			env, ok := next(deadline)
			if !ok {
				t.Fatal("held roster never arrived")
			}
			if env.Type == protocol.TypeError {
				t.Fatal("turn refused")
			}
			if env.Type != protocol.TypeBackgroundTaskRoster {
				continue
			}
			var p protocol.BackgroundTaskRosterPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p.ConversationID == conv && len(p.Tasks) == 1 && p.Tasks[0].TaskID == taskID {
				break
			}
		}
	}
	stop := func(id uint64, task string) {
		seal(protocol.Envelope{ID: id, Type: protocol.TypeStopBackgroundTask, TS: time.Now().UTC(), Payload: mustJSON(t, protocol.StopBackgroundTaskPayload{ConversationID: convA, TaskID: task})})
	}
	stop(27963, "absent")
	refusals := 0
	deadline = time.Now().Add(5 * time.Second)
	for refusals == 0 {
		env, ok := next(deadline)
		if !ok {
			t.Fatal("missing refusal")
		}
		if env.Type != protocol.TypeError {
			continue
		}
		var p protocol.ErrorPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if env.InReplyTo == nil || *env.InReplyTo != 27963 || p.Code != "stop_background_task.refused" || p.Message != "background task stop refused" || p.Retryable || p.ConversationID != convA {
			t.Fatalf("wrong fixed refusal: %s", env.Payload)
		}
		refusals++
	}
	stop(27964, taskID)
	stopped, removed := false, false
	deadline = time.Now().Add(5 * time.Second)
	for !stopped || !removed {
		env, ok := next(deadline)
		if !ok {
			t.Fatal("no task completion")
		}
		switch env.Type {
		case protocol.TypeBackgroundTaskUpdated:
			var p protocol.BackgroundTaskUpdatedPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p.ConversationID == convA && p.TaskID == taskID && p.Status == "stopped" {
				stopped = true
			}
		case protocol.TypeBackgroundTaskRoster:
			var p protocol.BackgroundTaskRosterPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p.ConversationID == convA && len(p.Tasks) == 0 {
				removed = true
			}
		case protocol.TypeError:
			t.Fatal("accepted stop returned error")
		case protocol.TypeTurnEnd:
			t.Fatal("stop ended a running reply")
		}
	}
	// Drain queued frames so duplicate errors or an unwanted reply closure cannot hide.
	deadline = time.Now().Add(250 * time.Millisecond)
	for {
		env, ok := next(deadline)
		if !ok {
			break
		}
		if env.Type == protocol.TypeError || env.Type == protocol.TypeTurnEnd || env.InReplyTo != nil && *env.InReplyTo == 27964 {
			t.Fatal("unexpected stop reply or turn closure")
		}
	}
	logs, err := filepath.Glob(logStem + ".*")
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 {
		t.Fatalf("child logs = %d, want 2", len(logs))
	}
	total := 0
	for _, path := range logs {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		users := 0
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var p struct {
				Type    string
				Request struct {
					Subtype string
					TaskID  string `json:"task_id"`
				}
			}
			if err := json.Unmarshal([]byte(line), &p); err != nil {
				t.Fatal(err)
			}
			if p.Type == "user" {
				users++
			}
			if p.Request.Subtype == "interrupt" {
				t.Fatal("stop sent interrupt")
			}
			if p.Request.Subtype == "stop_task" {
				total++
				if path != logStem+"."+sid || p.Request.TaskID != taskID {
					t.Fatal("wrong child or task")
				}
			}
		}
		if users != 1 {
			t.Fatalf("user turns = %d, want 1", users)
		}
	}
	if total != 1 || refusals != 1 {
		t.Fatalf("stop requests/refusals = %d/%d, want 1/1", total, refusals)
	}
}
