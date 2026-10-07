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

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// A background Agent keeps emitting after the main result. None of that activity
// may hold queued conversation messages, even after repeated client interrupts.
// The interrupt-result rider would hide the defect and must stay unset.
func TestRelayV2_StreamParentActivityDoesNotHoldQueuedProbe(t *testing.T) {
	cases := []struct {
		name       string
		interrupts int
		thinking   string
	}{
		{name: "interrupts=0"},
		{name: "interrupts=1", interrupts: 1},
		{name: "interrupts=2", interrupts: 2},
		{name: "thinking-assistant", thinking: `{"type":"assistant","parent_tool_use_id":"toolu_2781_agent","message":{"id":"thought","content":[{"type":"thinking","thinking":"private subagent thought"}]}}`},
		{name: "thinking-delta", thinking: strings.Join([]string{
			`{"type":"stream_event","event":{"type":"message_start","message":{"id":"thought"}}}`,
			`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}}`,
			`{"type":"stream_event","parent_tool_use_id":"toolu_2781_agent","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"private subagent thought"}}}`,
		}, "\n")},
		{name: "thinking-progress", thinking: `{"type":"system","subtype":"thinking_tokens","parent_tool_use_id":"toolu_2781_agent","estimated_tokens":128,"estimated_tokens_delta":128}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			interrupts := tc.interrupts
			const (
				session = "27810000-0000-4000-8000-000000000001"
				conv    = "27810000-0000-4000-8000-000000000002"
				parent  = "toolu_2781_agent"
				child   = "toolu_2781_child"
				probe   = "e2e-2781-queued-probe"
			)
			if os.Getenv("PYRY_FAKE_CLAUDE_STREAM_INTERRUPT") != "" {
				t.Fatal("interrupt-result rider must be unset")
			}
			dir := t.TempDir()
			firstPath, secondPath := filepath.Join(dir, "first.jsonl"), filepath.Join(dir, "second.jsonl")
			releasePath, stdinStem := filepath.Join(dir, "release"), filepath.Join(dir, "stdin")
			first := strings.Join([]string{
				`{"type":"assistant","message":{"id":"main","role":"assistant","content":[{"type":"tool_use","id":"` + parent + `","name":"Agent","input":{"prompt":"work in background","run_in_background":true}}]}}`,
				`{"type":"result","subtype":"success","session_id":"` + session + `"}`,
			}, "\n") + "\n"
			// No child tool result, subagent completion or main result follows.
			second := `{"type":"assistant","parent_tool_use_id":"` + parent + `","message":{"id":"child","role":"assistant","content":[{"type":"text","text":"background work"},{"type":"tool_use","id":"` + child + `","name":"Bash","input":{"command":"still working"}}]}}` + "\n"
			if tc.thinking != "" {
				// The later tool_use is a processing barrier on the same FIFO
				// drain: observing it proves thinking reached both consumers.
				second = tc.thinking + "\n" + second
			}
			for path, body := range map[string]string{firstPath: first, secondPath: second} {
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatalf("write replay: %v", err)
				}
			}

			home := shortHome(t)
			fr := fakerelay.New(relayTestLogger())
			t.Cleanup(func() { _ = fr.Close() })
			relayURL := fr.URL() + "/v2/server"
			payload, err := paireddevice.Setup(paireddevice.Config{
				Home: home, InstanceName: "test", Relay: relayURL, DeviceName: "phone-a",
			})
			if err != nil {
				t.Fatalf("setup device: %v", err)
			}
			pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
			if err != nil {
				t.Fatalf("decode server key: %v", err)
			}
			seedBoundConversation(t, home, conv, session)
			h := StartStreamInteractiveWithRelay(t, home, session, relayURL,
				"PYRY_FAKE_CLAUDE_STREAM_REPLAY_FIRST="+firstPath,
				"PYRY_FAKE_CLAUDE_STREAM_REPLAY_SECOND="+secondPath,
				"PYRY_FAKE_CLAUDE_STREAM_REPLAY_RELEASE="+releasePath,
				"PYRY_FAKE_CLAUDE_STDIN_LOG="+stdinStem)
			t.Cleanup(func() { h.Stop(t) })
			serverID := readPersistedServerID(t, home)
			waitBinaryHello(t, fr, serverID)
			dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
			if err != nil {
				t.Fatalf("phone dial: %v", err)
			}
			t.Cleanup(func() { _ = phone.Close() })
			send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, payload.Token)
			sealSend, nextEnv := sealedConnDriver(t, phone, "phone", send, recv)
			firstStatus := waitForRunnerStatus(t, h, 15*time.Second, "bootstrap child running",
				func(s *control.StatusPayload) bool { return s.Phase == "running" && s.ChildPID != 0 })

			sendMessage := func(id uint64, messageID, text string) {
				t.Helper()
				sealSend(protocol.Envelope{ID: id, Type: protocol.TypeSendMessage, TS: time.Now().UTC(),
					Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: conv, MessageID: messageID, Text: text})})
			}
			await := func(deadline time.Time, matches func(protocol.Envelope) bool) {
				t.Helper()
				for {
					env, ok := nextEnv(deadline)
					if !ok {
						t.Fatalf("expected frame never arrived; daemon stderr:\n%s", stderrTail(h, 6000))
					}
					if env.Type == protocol.TypeError {
						t.Fatalf("unexpected protocol error: %s", env.Payload)
					}
					if matches(env) {
						return
					}
				}
			}
			sendMessage(27810, "start-agent", "start background agent")
			var sawAgent bool
			await(time.Now().Add(15*time.Second), func(env protocol.Envelope) bool {
				if env.Type == protocol.TypeToolUse {
					var p protocol.ToolUsePayload
					if err := json.Unmarshal(env.Payload, &p); err != nil {
						t.Fatalf("decode main tool: %v", err)
					}
					sawAgent = p.ConversationID == conv && p.ToolUseID == parent && p.ParentToolUseID == "" && p.Name == "Agent"
				}
				if env.Type != protocol.TypeTurnEnd {
					return false
				}
				if !sawAgent {
					t.Fatal("main turn ended without the top-level Agent call")
				}
				return true
			})
			if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
				t.Fatalf("release child activity: %v", err)
			}
			await(time.Now().Add(15*time.Second), func(env protocol.Envelope) bool {
				if env.Type == protocol.TypeTurnEnd {
					t.Fatal("unexpected turn end after main completion")
				}
				if env.Type != protocol.TypeToolUse {
					return false
				}
				var p protocol.ToolUsePayload
				if err := json.Unmarshal(env.Payload, &p); err != nil {
					t.Fatalf("decode child tool: %v", err)
				}
				return p.ConversationID == conv && p.ToolUseID == child && p.ParentToolUseID == parent
			})

			// Count distinct parsed control requests, not log substrings or dispatch
			// records: every requested interrupt must reach this child's stdin.
			readStdin := func() string {
				t.Helper()
				return mustReadFile(t, childStdinLog(stdinStem, session))
			}
			for n := 1; n <= interrupts; n++ {
				sealSend(protocol.Envelope{ID: uint64(27810 + n), Type: protocol.TypeInterrupt, TS: time.Now().UTC(),
					Payload: mustJSON(t, protocol.InterruptPayload{ConversationID: conv})})
				deadline := time.Now().Add(5 * time.Second)
				for {
					ids := make(map[string]bool)
					for _, line := range strings.Split(readStdin(), "\n") {
						var request struct {
							Type      string `json:"type"`
							RequestID string `json:"request_id"`
							Request   struct {
								Subtype string `json:"subtype"`
							} `json:"request"`
						}
						if json.Unmarshal([]byte(line), &request) == nil && request.Type == "control_request" && request.Request.Subtype == "interrupt" {
							ids[request.RequestID] = true
						}
					}
					if len(ids) == n && !ids[""] {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("interrupt %d did not reach child stdin: %v", n, ids)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}

			deadline := time.Now().Add(5 * time.Second)
			sendMessage(27820, "queued-probe", probe)
			await(deadline, func(env protocol.Envelope) bool {
				if env.Type == protocol.TypeTurnEnd || env.Type == protocol.TypeToolResult {
					t.Fatal("a result released the probe before its stdin receipt")
				}
				if env.Type != protocol.TypeAssistantDelta {
					return false
				}
				var p protocol.AssistantDeltaPayload
				if err := json.Unmarshal(env.Payload, &p); err != nil {
					t.Fatalf("decode probe echo: %v", err)
				}
				return p.ConversationID == conv && strings.Contains(p.Text, probe)
			})
			if !strings.Contains(readStdin(), probe) || time.Now().After(deadline) {
				t.Fatal("queued probe did not reach child stdin within five seconds")
			}
			lastStatus := statusOrFatal(t, h)
			if lastStatus.Phase != "running" || lastStatus.ChildPID != firstStatus.ChildPID || lastStatus.RestartCount != firstStatus.RestartCount {
				t.Fatalf("child continuity lost: before=%+v after=%+v", firstStatus, lastStatus)
			}
			logs := h.Stderr.String()
			if strings.Contains(logs, "claude exited") || strings.Count(logs, "spawning claude") != 1 {
				t.Fatalf("child exit or respawn could have cleared the hold:\n%s", logs)
			}
		})
	}
}
