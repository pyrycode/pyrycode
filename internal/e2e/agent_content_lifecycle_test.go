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
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestRelayV2_AgentContentLifecycleNeutral(t *testing.T) {
	const sid = "37180000-0000-4000-8000-000000000001"
	const conv = "37180000-0000-4000-8000-000000000002"
	dir := t.TempDir()
	first, second, release := filepath.Join(dir, "first"), filepath.Join(dir, "second"), filepath.Join(dir, "release")
	fragments := map[string]string{
		first: `{"type":"assistant","message":{"id":"main","role":"assistant","content":[{"type":"text","text":"main reply"}]}}` + "\n" +
			`{"type":"result","subtype":"success","session_id":"fake-stream"}` + "\n",
		second: strings.Join([]string{
			bgTaskStartedLine,
			`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"buwavm27r","tool_use_id":"toolu_01RVFCkACkpA1cv8gmvXjKSj","status":"running"}]}`,
			`{"type":"assistant","parent_tool_use_id":"toolu_01RVFCkACkpA1cv8gmvXjKSj","message":{"id":"child","role":"assistant","content":[{"type":"text","text":"child reply"}]}}`,
			bgTaskNotificationLine,
			`{"type":"system","subtype":"background_tasks_changed","tasks":[]}`,
			bgTaskProgressLine2,
		}, "\n") + "\n",
	}
	for path, content := range fragments {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	paired, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: relayURL, DeviceName: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := base64.StdEncoding.DecodeString(paired.ServerStaticPubkey)
	if err != nil {
		t.Fatal(err)
	}
	seedBoundConversation(t, home, conv, sid)
	h := StartStreamInteractiveWithRelay(t, home, sid, relayURL,
		"PYRY_FAKE_CLAUDE_STREAM_REPLAY_FIRST="+first, "PYRY_FAKE_CLAUDE_STREAM_REPLAY_SECOND="+second, "PYRY_FAKE_CLAUDE_STREAM_REPLAY_RELEASE="+release)
	t.Cleanup(func() { h.Stop(t) })
	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	phone, err := fakephone.Dial(ctx, fr.URL(), serverID, paired.Token, "phone")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pub, paired.Token)
	source := scenario.NewSourceObserver(t, home)
	rosters, children, outcomes := 0, 0, 0
	source.OnEntry = func(entry history.Entry) {
		switch entry.Type {
		case protocol.TypeBackgroundTaskRoster:
			var p protocol.BackgroundTaskRosterPayload
			if err := json.Unmarshal(entry.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if rosters == 0 && (len(p.Tasks) != 1 || p.Tasks[0].TaskID != "buwavm27r") || rosters == 1 && len(p.Tasks) != 0 {
				t.Fatal("retained roster lost nonempty-to-empty lifecycle")
			}
			rosters++
		case protocol.TypeBackgroundTaskUpdated:
			outcomes++
		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(entry.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p.ParentToolUseID != "" && p.Text == "child reply" {
				children++
			}
		}
	}
	seal, next := sealedConnDriver(t, phone, "phone", send, recv, source)
	seal(protocol.Envelope{ID: 3718, Type: protocol.TypeSendMessage, TS: time.Now().UTC(), Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: conv, MessageID: "main", Text: "main"})})
	deadline := time.Now().Add(20 * time.Second)
	running := false
	for {
		env, ok := next(deadline)
		if !ok || env.Type == protocol.TypeError {
			t.Fatal("main turn did not run and return idle")
		}
		if env.Type != protocol.TypeTurnState {
			continue
		}
		var phase protocol.TurnStatePayload
		if err := json.Unmarshal(env.Payload, &phase); err != nil {
			t.Fatal(err)
		}
		if phase.ConversationID == conv {
			if phase.State == "idle" && running {
				break
			}
			running = phase.State != "idle"
		}
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(15 * time.Second)
	barrierSent := false
	for {
		env, ok := next(deadline)
		if !ok || env.Type == protocol.TypeError {
			t.Fatal("child/roster proof did not reach its ordered barrier")
		}
		if env.Type == protocol.TypeTurnState {
			t.Fatal("child/roster traffic emitted an actual main turn-state envelope")
		}
		if env.Type == protocol.TypeBackgroundTaskProgress && !barrierSent {
			seal(protocol.Envelope{ID: 3719, Type: protocol.TypeListConversations, TS: time.Now().UTC(), Payload: json.RawMessage(`{}`)})
			barrierSent = true
		}
		if env.Type == protocol.TypeConversations && env.InReplyTo != nil && *env.InReplyTo == 3719 {
			break
		}
	}
	if !barrierSent || rosters != 2 || children != 1 || outcomes != 1 {
		t.Fatalf("incomplete neutral traffic: barrier=%v rosters=%d children=%d outcomes=%d", barrierSent, rosters, children, outcomes)
	}
}
