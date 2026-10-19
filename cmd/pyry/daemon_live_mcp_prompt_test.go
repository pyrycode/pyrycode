package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

func TestMCPPromptHelperProcess(t *testing.T) {
	if os.Getenv("PYRY_TEST_MCP_PROMPT") != "1" {
		return
	}
	if err := runMCPApprove([]string{"-pyry-socket", os.Args[len(os.Args)-1]}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func testMCPPromptProcess(t *testing.T, socket, session string) func() []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPPromptHelperProcess$", "--", socket)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, envSessionID+"=") && !strings.HasPrefix(entry, "PYRY_TEST_MCP_PROMPT=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, envSessionID+"="+session, "PYRY_TEST_MCP_PROMPT=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	t.Cleanup(func() { cancel(); _ = stdin.Close(); <-done })
	// Tool arguments must never select the producing session.
	_, err = io.WriteString(stdin, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"approve","arguments":{"tool_name":"Bash","input":{"command":"true"},"tool_use_id":"background-prompt","session_id":"spoofed"}}}`+"\n")
	if err != nil {
		t.Fatal(err)
	}
	return func() []byte {
		var response json.RawMessage
		if err := json.NewDecoder(stdout).Decode(&response); err != nil {
			t.Fatalf("MCP response: %v", err)
		}
		_ = stdin.Close()
		<-done
		if waitErr != nil {
			t.Fatalf("MCP process: %v: %s", waitErr, stderr.String())
		}
		return response
	}
}

func TestMCPApprove_ProducerSession(t *testing.T) {
	for _, session := range []string{"a", ""} {
		t.Run("producer="+session, func(t *testing.T) {
			got := make(chan control.Request, 1)
			socket := startApprovePeer(t, replyPeer(control.Response{Approve: &control.ApproveResult{Behavior: "deny"}}, got))
			finish := testMCPPromptProcess(t, socket, session)
			finish()
			raw, err := json.Marshal((<-got).Approve)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			want := ""
			if session != "" {
				want = `"` + session + `"`
			}
			if string(payload["session_id"]) != want {
				t.Fatalf("process source=%q forwarded source=%s", session, payload["session_id"])
			}
		})
	}
}

func TestDaemonLiveRelayMCPPromptReconnect(t *testing.T) {
	for _, cursorMovedBefore := range []bool{false, true} {
		name := "cursor-moves-after-surface"
		if cursorMovedBefore {
			name = "different-active-conversation"
		}
		t.Run(name, func(t *testing.T) {
			b := testControlBindings(t, testConvID, "a")
			b.reg.Create(conversations.Conversation{ID: "other", CurrentSessionID: "b"})
			cursor := &activeConversation{}
			cursor.set(testConvID)
			if cursorMovedBefore {
				cursor.set("other")
			}
			bridge, modal, _, _ := testPromptBridge(t, nil, discardLogger())
			bridge.live, bridge.bcast = b, historyOnlyBroadcaster{}
			bridge.activeConv = cursor.CurrentConversation
			bridge.sessionConv = func(id string) (string, bool) { return conversationForSession(b.reg, id) }
			snapshotDone := make(chan struct{}, 3)
			w, mgr := testDaemonRelayWire(t, b, relayWiring{}, nil, func(cfg *relay.V2SessionConfig) {
				cfg.ModalResolver = modal
				cfg.OutstandingModals = bridge.modal.(*modalbridge.Registry).Snapshot
				capture := cfg.ThreadLiveState
				cfg.ThreadLiveState = func() func() (relay.LiveState, bool) {
					next := capture()
					return func() (relay.LiveState, bool) {
						r, ok := next()
						if !ok {
							snapshotDone <- struct{}{}
						}
						return r, ok
					}
				}
			})
			waitSnapshot := func() {
				t.Helper()
				select {
				case <-snapshotDone:
				case <-time.After(3 * time.Second):
					t.Fatal("reconnect snapshot did not drain")
				}
			}
			w.open("phone", false, protocol.CapabilityThread)
			waitSnapshot()
			w.frames <- protocol.RoutingEnvelope{ConnID: "phone", CloseCode: 1000}
			disconnected, stopWaiting := context.WithTimeout(t.Context(), 3*time.Second)
			defer stopWaiting()
			tick := time.NewTicker(time.Millisecond)
			defer tick.Stop()
			for len(mgr.ActiveConns(disconnected)) != 0 {
				select {
				case <-tick.C:
				case <-disconnected.Done():
					t.Fatal("phone did not disconnect before prompt creation")
				}
			}
			if disconnected.Err() != nil {
				t.Fatal("disconnect was not confirmed before prompt creation")
			}
			surfaced := make(chan struct{}, 1)
			socket := filepath.Join(shortTempDir(t), "prompt.sock")
			srv := control.NewServer(socket, poolResolver{}, nil, nil, discardLogger(), nil)
			srv.SetApprovalRegistry(bridge.perm, time.Minute)
			srv.SetApprovalSurfacer(func(req permbridge.Request) func() {
				retire := bridge.Surface(req)
				surfaced <- struct{}{}
				return retire
			})
			if err := srv.Listen(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- srv.Serve(ctx) }()
			t.Cleanup(func() { cancel(); _ = srv.Close(); <-done })
			finish := testMCPPromptProcess(t, socket, "a")
			select {
			case <-surfaced:
			case <-time.After(5 * time.Second):
				t.Fatal("MCP prompt did not surface")
			}
			cursor.set("other")
			defer bridge.perm.Resolve("background-prompt", permbridge.Deny("cleanup"))
			var retained relay.LiveState
			next := daemonRelaySnapshot(b.sink.live)()
			for r, ok := next(); ok; r, ok = next() {
				if r.Envelope.Type == protocol.TypeModalShown && !r.Envelope.SessionStateCleared {
					retained = r
				}
			}
			if retained.ReadingID == "" || retained.SessionGeneration == 0 || retained.Revision == 0 {
				t.Fatal("outstanding prompt absent from snapshot")
			}
			assertPrompt := func() {
				t.Helper()
				cleared := false
				var clear protocol.Envelope
				for {
					_, e := w.read()
					if e.Type != protocol.TypeModalShown {
						continue
					}
					if e.SessionStateCleared {
						if cleared || string(e.Payload) != `{}` {
							t.Fatal("invalid or repeated family clear")
						}
						cleared = true
						clear = e
						continue
					}
					if string(e.SessionID) != `"a"` {
						t.Fatalf("fresh modal lost producing session: %s", e.SessionID)
					}
					for _, frame := range []protocol.Envelope{clear, e} {
						if string(frame.SessionID) != `"a"` || string(frame.ConversationID) != `"`+testConvID+`"` {
							t.Fatalf("modal clear=%v rebound: conversation=%s session=%s", frame.SessionStateCleared, frame.ConversationID, frame.SessionID)
						}
					}
					var p protocol.ModalShownPayload
					if err := json.Unmarshal(e.Payload, &p); err != nil {
						t.Fatal(err)
					}
					if !cleared || p.ModalID != retained.ReadingID || p.ConversationID != testConvID || !bytes.Equal(e.Payload, retained.Envelope.Payload) || !e.TS.Equal(retained.Envelope.TS) {
						t.Fatal("fresh modal lost clear ordering, identity or captured bytes")
					}
					break
				}
			}
			for i := 0; i < 2; i++ {
				w.open("phone", false, protocol.CapabilityThread)
				assertPrompt()
				waitSnapshot()
				if _, ok := bridge.perm.Lookup("background-prompt"); !ok {
					t.Fatal("reconnect resolved outstanding prompt")
				}
				current := testLiveReadings(t, b.sink.live, testConvID)[liveReadingKey{protocol.TypeModalShown, retained.ReadingID}]
				if current.SessionGeneration != retained.SessionGeneration || current.Revision != retained.Revision {
					t.Fatal("reconnect changed captured ordering")
				}
				w.frames <- protocol.RoutingEnvelope{ConnID: "phone", CloseCode: 1000}
			}
			// An old snapshot must not restore a prompt after same-session-ID reuse.
			b.sink.queueBoundary(sessions.SessionTransition{ConversationID: testConvID, PreviousID: "a", NewID: "a", NextAgent: "claude"}, func() {}, nil)
			w.open("phone", false, protocol.CapabilityThread)
			waitSnapshot()
			if err := mgr.PushLiveState(t.Context(), "phone", retained); err != nil {
				t.Fatal(err)
			}
			if err := mgr.FlushPushes(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := mgr.Push(t.Context(), "phone", protocol.Envelope{ID: 900, Type: protocol.TypeError, Payload: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
			for {
				_, e := w.read()
				if e.Type == protocol.TypeModalShown && !e.SessionStateCleared {
					t.Fatal("retired generation prompt reappeared")
				}
				if e.Type == protocol.TypeError {
					break
				}
			}
			bridge.perm.Resolve("background-prompt", permbridge.Deny("test complete"))
			if raw := finish(); !bytes.Contains(raw, []byte(`\"behavior\":\"deny\"`)) {
				t.Fatalf("MCP verdict missing: %s", raw)
			}
		})
	}
}
