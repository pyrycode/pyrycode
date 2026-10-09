//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestConversationPost_E2E_UserTurns(t *testing.T) {
	home, _ := newRegistryHome(t)
	writeStreamInteractiveConfig(t, home)
	// Capture the actual launch arguments per process, then exec the existing
	// stream fake. The wrapper never interpolates message content.
	wrapper := filepath.Join(home, "claude-wrapper")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > '%s/argv.'$$\nexec '%s' \"$@\"\n", home, ensureFakeClaudeBuilt(t))
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	vocab := `{"models":[{"value":"sonnet","resolved_model":"sonnet-version","effort_levels":["low","max"]}]}`
	if err := os.WriteFile(filepath.Join(home, ".pyry", "test", "model_list.json"), []byte(vocab), 0o600); err != nil {
		t.Fatal(err)
	}
	stem := filepath.Join(home, "stdin")
	socket, cmd, stdout, stderr, done := spawnWith(t, home, spawnOpts{claudeBin: wrapper, claudeArgs: []string{}, extraFlags: []string{"-pyry-relay="}, extraEnv: []string{"PYRY_FAKE_CLAUDE_STREAM_JSON=1", "PYRY_FAKE_CLAUDE_STDIN_LOG=" + stem}})
	h := &Harness{SocketPath: socket, HomeDir: home, PID: cmd.Process.Pid, cmd: cmd, Stdout: stdout, Stderr: stderr, doneCh: done}
	t.Cleanup(func() { h.teardown(t) })
	if err := h.waitForReady(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"chat", "channel"} {
		t.Run(kind, func(t *testing.T) {
			r := runVerbIn(t, socket, home, home, "conversation", "new", "--type="+kind, "--model=sonnet", "--effort=max")
			if r.ExitCode != 0 {
				t.Fatalf("create: %+v", r)
			}
			id := strings.TrimSpace(string(r.Stdout))
			row := waitForConversation(t, home, id)
			if _, err := os.Stat(stem + "." + row.CurrentSessionID); !os.IsNotExist(err) {
				t.Fatal("fresh conversation already activated")
			}
			if kind == "chat" {
				// Check the rejected lookup before any turn starts, so legitimate
				// turn metadata writes cannot race with the registry comparison.
				registryPath := filepath.Join(home, ".pyry", "test", "conversations.json")
				before, err := os.ReadFile(registryPath)
				if err != nil {
					t.Fatal(err)
				}
				r := runVerb(t, socket, home, "conversation", "post", "--id=private-missing-id", "--text=private-message")
				after, err := os.ReadFile(registryPath)
				if err != nil {
					t.Fatal(err)
				}
				if r.ExitCode != 1 || len(r.Stdout) != 0 || !bytes.Contains(r.Stderr, []byte("unknown conversation")) || bytes.Contains(r.Stderr, []byte("private-")) || !bytes.Equal(before, after) {
					t.Fatalf("unknown: %+v", r)
				}
			}
			text := "  operator " + kind + "\n\tunchanged é  "
			// Instance selection remains usable and explicit socket wins.
			r = runVerb(t, socket, home, "conversation", "-pyry-name=other", "-pyry-socket="+socket, "post", "--id="+id, "--text", text)
			if r.ExitCode != 0 || len(r.Stdout) != 0 || len(r.Stderr) != 0 {
				t.Fatalf("post: %+v", r)
			}
			waitForHistoryType(t, home, id, protocol.TypeTurnEnd, 5*time.Second)
			waitForHistoryType(t, home, id, protocol.TypeMessage, 5*time.Second)
			users, ends := 0, 0
			var assistant strings.Builder
			for _, e := range readHistoryEntries(home, id) {
				switch e.Type {
				case protocol.TypeMessage:
					var p struct {
						Role, Text     string
						ConversationID string `json:"conversation_id"`
					}
					if err := json.Unmarshal(e.Payload, &p); err != nil {
						t.Fatal(err)
					}
					if p.Role == "user" {
						users++
						if p.Text != text || p.ConversationID != id {
							t.Fatalf("user history: %s", e.Payload)
						}
					}
				case protocol.TypeAssistantDelta:
					var p assistantDeltaBody
					if err := json.Unmarshal(e.Payload, &p); err != nil {
						t.Fatal(err)
					}
					assistant.WriteString(p.Text)
				case protocol.TypeTurnEnd:
					ends++
				}
			}
			if users != 1 || ends != 1 || assistant.String() != text {
				t.Fatalf("history users=%d ends=%d assistant=%q", users, ends, assistant.String())
			}
			raw, err := os.ReadFile(stem + "." + row.CurrentSessionID)
			if err != nil {
				t.Fatal(err)
			}
			turns := 0
			for _, line := range bytes.Split(raw, []byte("\n")) {
				var p struct {
					Type    string
					Message struct{ Content []struct{ Type, Text string } }
				}
				if json.Unmarshal(line, &p) == nil && p.Type == "user" {
					turns++
					if len(p.Message.Content) != 1 || p.Message.Content[0].Type != "text" || p.Message.Content[0].Text != text {
						t.Fatalf("delivered text: %s", line)
					}
				}
			}
			if turns != 1 {
				t.Fatalf("delivered %d turns", turns)
			}
			launches, _ := filepath.Glob(filepath.Join(home, "argv.*"))
			matched := false
			for _, path := range launches {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				args := string(raw)
				if strings.Contains(args, "\n"+row.CurrentSessionID+"\n") {
					matched = true
					if !strings.Contains(args, "--model\nsonnet\n") || !strings.Contains(args, "--effort\nmax\n") {
						t.Fatalf("launch settings: %s", raw)
					}
				}
			}
			if !matched {
				t.Fatal("bound child launch not captured")
			}
		})
	}

}

func TestConversationPost_E2E_SyntaxContentAndTransport(t *testing.T) {
	home, _ := newRegistryHome(t)
	socket := filepath.Join(home, "missing.sock")
	for _, args := range [][]string{
		{"post"}, {"post", "--id=", "--text=x"}, {"post", "--id=id"},
		{"post", "--id=id", "--text=x", "--file=x"},
		{"post", "--id"}, {"post", "--id", "--text=x"},
		{"post", "--id=id", "--text"}, {"post", "--id=id", "--file"},
		{"post", "--id=id", "--text", "--file=x"}, {"post", "--id=id", "--file", "--text=x"},
		{"post", "--id=id", "--text=x", "extra"}, {"post", "--id=id", "--text=x", "--unknown"},
		{"-pyry-name", "-pyry-socket", "post", "--id=id", "--text=x"},
	} {
		r := runVerb(t, socket, home, "conversation", args...)
		if r.ExitCode != 2 || len(r.Stdout) != 0 || !bytes.Contains(r.Stderr, []byte("usage: pyry conversation")) {
			t.Fatalf("syntax %q: %+v", args, r)
		}
	}
	for _, args := range [][]string{{"--text="}, {"--file="}, {"--file=" + filepath.Join(home, "absent")}, {"--text=" + strings.Repeat("é", control.MaxChannelPostBytes/2+1)}} {
		full := append([]string{"post", "--id=id"}, args...)
		r := runVerb(t, socket, home, "conversation", full...)
		if r.ExitCode != 1 || len(r.Stdout) != 0 || len(r.Stderr) == 0 {
			t.Fatalf("content: %+v", r)
		}
	}
	r := runVerb(t, socket, home, "conversation", "post", "--id=id", "--text=x")
	if r.ExitCode != 1 || len(r.Stdout) != 0 || len(r.Stderr) == 0 {
		t.Fatalf("transport: %+v", r)
	}
	r = RunBareIn(t, home, "help")
	if r.ExitCode != 0 || !bytes.Contains(r.Stdout, []byte("pyry conversation post --id ID")) {
		t.Fatalf("help: %+v", r)
	}
}

// One socket request per invocation, including when a peer accepts the request
// but drops the connection before returning its acceptance result.
func TestConversationPost_E2E_SingleRequestAndFileBound(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		file, drop bool
		want       int
	}{
		{name: "text cap", body: strings.Repeat("é", control.MaxChannelPostBytes/2)},
		{name: "file cap", body: strings.Repeat("x", control.MaxChannelPostBytes), file: true},
		{name: "whitespace file", body: " \n\t ", file: true},
		{name: "file overflow", body: strings.Repeat("x", control.MaxChannelPostBytes+1), file: true, want: 1},
		{name: "empty file", file: true, want: 1},
		{name: "dropped response", body: "once", drop: true, want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home, _ := newRegistryHome(t)
			socket := shortSocketPath(t)
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			args := []string{"post", "--id=id", "--text", tt.body}
			if tt.file {
				path := filepath.Join(home, "message")
				if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
					t.Fatal(err)
				}
				args = []string{"post", "--id=id", "--file", path}
			}
			result := make(chan RunResult, 1)
			go func() { result <- runVerb(t, socket, home, "conversation", args...) }()
			expectRequest := tt.want == 0 || tt.drop
			if expectRequest {
				listener.SetDeadline(time.Now().Add(5 * time.Second))
				conn, err := listener.AcceptUnix()
				if err != nil {
					t.Fatal(err)
				}
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				var req control.Request
				if err := json.NewDecoder(conn).Decode(&req); err != nil {
					t.Fatal(err)
				}
				if req.Verb != control.VerbConversationPost || req.ConversationPost == nil || req.ConversationPost.ConversationID != "id" || req.ConversationPost.Text != tt.body {
					t.Fatalf("request: %+v", req)
				}
				if !tt.drop {
					if err := json.NewEncoder(conn).Encode(control.Response{OK: true}); err != nil {
						t.Fatal(err)
					}
				}
				conn.Close()
			}
			r := <-result
			if r.ExitCode != tt.want || len(r.Stdout) != 0 || tt.want == 0 && len(r.Stderr) != 0 || tt.want == 1 && len(r.Stderr) == 0 {
				t.Fatalf("result: %+v", r)
			}
			listener.SetDeadline(time.Now().Add(100 * time.Millisecond))
			conn, err := listener.AcceptUnix()
			if err == nil {
				conn.Close()
				t.Fatal("unexpected request/resubmission")
			}
		})
	}
}

func TestConversationPost_E2E_AcceptanceBeforeLaterFailure(t *testing.T) {
	home, _ := newRegistryHome(t)
	first, second, release := filepath.Join(home, "first"), filepath.Join(home, "second"), filepath.Join(home, "release")
	for path, body := range map[string]string{
		first:  `{"type":"assistant","message":{"id":"working","role":"assistant","content":[{"type":"text","text":"working"}]}}` + "\n",
		second: `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["later failure"],"session_id":"fake-stream"}` + "\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := StartStreamInteractiveWithRelay(t, home, "28860000-0000-4000-8000-000000000002", "",
		"PYRY_FAKE_CLAUDE_STREAM_REPLAY_FIRST="+first, "PYRY_FAKE_CLAUDE_STREAM_REPLAY_SECOND="+second, "PYRY_FAKE_CLAUDE_STREAM_REPLAY_RELEASE="+release)
	r := runVerbIn(t, h.SocketPath, home, home, "conversation", "new")
	if r.ExitCode != 0 {
		t.Fatalf("create: %+v", r)
	}
	id := strings.TrimSpace(string(r.Stdout))
	// The child cannot complete until we release its result. Waiting for completion
	// in the CLI would time out here rather than return accepted.
	accepted := runVerb(t, h.SocketPath, home, "conversation", "post", "--id="+id, "--text=task")
	if accepted.ExitCode != 0 || len(accepted.Stdout) != 0 || len(accepted.Stderr) != 0 {
		t.Fatalf("acceptance: %+v", accepted)
	}
	waitForHistoryType(t, home, id, protocol.TypeAssistantDelta, 5*time.Second)
	for _, e := range readHistoryEntries(home, id) {
		if e.Type == protocol.TypeTurnEnd {
			t.Fatal("completed before release")
		}
	}
	if err := os.WriteFile(release, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForHistoryType(t, home, id, protocol.TypeTurnEnd, 5*time.Second)
	foundFailure := false
	for _, e := range readHistoryEntries(home, id) {
		if e.Type == protocol.TypeTurnEnd {
			var p protocol.TurnEndPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p.Outcome == "error_during_execution" && p.IsError {
				foundFailure = true
			}
		}
	}
	if !foundFailure {
		t.Fatal("delayed failed completion not recorded")
	}
}
