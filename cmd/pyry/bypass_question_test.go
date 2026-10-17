package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

type bypassQuestionBroadcaster struct {
	mu           sync.Mutex
	inner        *fakeInteractiveBcast
	disconnected bool
}

func (b *bypassQuestionBroadcaster) ActiveConns(ctx context.Context) []relay.ActiveConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.disconnected {
		return nil
	}
	return b.inner.ActiveConns(ctx)
}

func (b *bypassQuestionBroadcaster) Push(ctx context.Context, connID string, env protocol.Envelope) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inner.Push(ctx, connID, env)
}

func (b *bypassQuestionBroadcaster) snapshot() []recordedPush {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.inner.pushes)
}

func TestBypassQuestionArgs(t *testing.T) {
	t.Parallel()
	for _, stdio := range []bool{false, true} {
		for _, operator := range []bool{false, true} {
			for _, prompt := range [][]string{nil, {"--permission-prompt-tool", "old"}, {"--permission-prompt-tool=old"}, {"--permission-prompt-tool"}, {"--permission-prompt-tool=old", "--permission-prompt-tool", "stdio"}} {
				t.Run(fmt.Sprintf("stdio=%v/operator=%v/prompt=%v", stdio, operator, prompt), func(t *testing.T) {
					mode := sessions.PermissionModeBypass
					base := []string{"--dangerously-skip-permissions"}
					if operator {
						mode = "plan"
						base = append(base, "--permission-mode", mode)
					}
					in := append(slices.Clone(base), prompt...)
					saved := slices.Clone(in)
					want := append(slices.Clone(base), "--permission-prompt-tool", "stdio")
					got := withApprovalArgs(in, "/daemon/mcp.json", mode, operator, stdio)
					if !slices.Equal(got, want) || !slices.Equal(in, saved) {
						t.Fatalf("args = %q, want %q; input = %q, originally %q", got, want, in, saved)
					}
					var rec recordingInstaller
					a := installerAdapter(&rec, "/daemon/mcp.json", stdio, operator, mode)
					a.Restart(in)
					a.SetSpawnArgs(in)
					if !slices.Equal(rec.restarted[0], want) || !slices.Equal(rec.swapped[0], want) {
						t.Fatal("restart/next-spawn shaping differs from construction")
					}
				})
			}
		}
	}
}

func TestNonBypassQuestionTransport(t *testing.T) {
	t.Parallel()
	for _, stdio := range []bool{false, true} {
		base := []string{"--dangerously-skip-permissions", "--permission-mode", "plan"}
		in := append(slices.Clone(base), "--permission-prompt-tool=old", "--permission-prompt-tool", "other")
		want := append(slices.Clone(base), "--permission-prompt-tool", approveToolRef, "--mcp-config", "/daemon/mcp.json", "--strict-mcp-config")
		if stdio {
			want[len(base)+1] = "stdio"
		}
		if got := withApprovalArgs(in, "/daemon/mcp.json", "plan", false, stdio); !slices.Equal(got, want) {
			t.Errorf("stdio=%v: args=%q, want %q", stdio, got, want)
		}
	}
}

// The helper asks on each process incarnation using the same child request IDs.
// Its receipt proves the response reached stdin, rather than only the surfacer.
func TestBypassQuestionHelperProcess(t *testing.T) {
	index := slices.Index(os.Args, "pyry-question-helper")
	if index < 0 {
		return
	}
	receipt := os.Args[index+1]
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var frame struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Request.Subtype == "initialize" {
			fmt.Printf("{\"type\":\"control_response\",\"response\":{\"subtype\":\"success\",\"request_id\":%q,\"response\":{}}}\n", frame.RequestID)
			fmt.Println(`{"type":"control_request","request_id":"same-request","request":{"subtype":"can_use_tool","tool_use_id":"same-tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Pick?","header":"Pick","multiSelect":false,"options":[{"label":"A","description":"a"},{"label":"B","description":"b"}]}]}}}`)
		}
		if frame.Type == "control_response" {
			if err := os.WriteFile(receipt, slices.Clone(scanner.Bytes()), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestStreamRunnerFactory_QuestionHandlerAvailable(t *testing.T) {
	for _, stdio := range []bool{false, true} {
		for _, operator := range []bool{false, true} {
			t.Run(fmt.Sprintf("stdio=%v/operator=%v", stdio, operator), func(t *testing.T) {
				f := newQuestionFixture(t, testConvID, discardLogger())
				bcast := &bypassQuestionBroadcaster{inner: f.bcast}
				f.bridge.bcast = bcast
				checked := make(chan bool, 8)
				f.perm.SetAnswerable(func(id string, req permbridge.Request) bool {
					answerable := f.bridge.ApprovalAnswerable(id, req)
					select {
					case checked <- answerable:
					default:
					}
					return answerable
				})
				shown := make(chan protocol.QuestionShownPayload, 1)
				askingSessions := make(chan string, 1)
				retired := make(chan struct{}, 1)
				surface := &approvalSurfaceReport{}
				surface.set(func(req permbridge.Request) func() {
					retire := f.bridge.Surface(req)
					askingSessions <- req.SessionID
					shown <- lastQuestionShown(t, bcast.snapshot())
					return func() { retire(); retired <- struct{}{} }
				})
				dir := t.TempDir()
				receipt := filepath.Join(dir, "receipt")
				child := filepath.Join(dir, "claude")
				launcher := fmt.Sprintf("#!/bin/sh\nexec '%s' -test.run='^TestBypassQuestionHelperProcess$' -- pyry-question-helper '%s' \"$@\"\n",
					strings.ReplaceAll(os.Args[0], "'", "'\\''"), strings.ReplaceAll(receipt, "'", "'\\''"))
				if err := os.WriteFile(child, []byte(launcher), 0o700); err != nil {
					t.Fatal(err)
				}
				args := []string{"--dangerously-skip-permissions"}
				factory := newStreamRunnerFactory(newStreamTurnSink(32, discardLogger()), "", nil,
					streamApprovalConfig{stdio: stdio, registry: f.perm, surface: surface, timeout: time.Second})
				// Start non-bypass/MCP too: later entering bypass must not require a new parser.
				r, err := factory(sessions.RunnerConfig{ClaudeBin: child, ClaudeArgs: args,
					WorkDir: dir, SessionID: "question-session", OperatorBypass: operator, Logger: discardLogger()})
				if err != nil {
					t.Fatal(err)
				}
				r.SetSpawnPermissionMode(sessions.PermissionModeBypass)
				r.SetSpawnArgs(args)
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() { done <- r.Run(ctx) }()
				defer func() {
					cancel()
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("runner did not stop")
					}
				}()
				awaitShown := func(sessionID string) protocol.QuestionShownPayload {
					select {
					case batch := <-shown:
						if got := <-askingSessions; got != sessionID {
							t.Fatalf("asking session = %q, want %q", got, sessionID)
						}
						return batch
					case <-time.After(5 * time.Second):
						t.Fatal("factory-installed handler did not surface a question")
						return protocol.QuestionShownPayload{}
					}
				}
				first := awaitShown("question-session")
				// Replace the session/child while its question is parked, then reuse
				// the child's request IDs under a different routing identity.
				r.RestartFresh("replacement-session")
				select {
				case <-retired:
				case <-time.After(5 * time.Second):
					t.Fatal("old question did not retire")
				}
				second := awaitShown("replacement-session")
				if first.QuestionBatchID == second.QuestionBatchID {
					t.Fatal("replacement reused the batch ID")
				}
				if f.bridge.AnswerQuestion(first.QuestionBatchID, []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: []string{"A"}}}) {
					t.Fatal("late answer resolved the retired child question")
				}
				if !f.bridge.RefuseQuestion(second.QuestionBatchID) {
					t.Fatal("replacement question could not be refused")
				}
				select {
				case <-retired:
				case <-time.After(5 * time.Second):
					t.Fatal("refusal did not retire")
				}
				deadline := time.Now().Add(5 * time.Second)
				for {
					raw, err := os.ReadFile(receipt)
					if err == nil && json.Valid(raw) && strings.Contains(string(raw), "deny") {
						requestID, behavior, _ := decodeStdioPermissionResponse(t, raw)
						if requestID != "same-request" || behavior != "deny" {
							t.Fatal("replacement received a miscorrelated response")
						}
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("replacement child received no refusal")
					}
					time.Sleep(10 * time.Millisecond)
				}
				if len(f.qreg.Snapshot()) != 0 || questionLen(f.bridge) != 0 {
					t.Fatal("retired question state leaked")
				}
				// The original child's backstop and the replacement's refusal each
				// emit exactly one terminal event, even though they reuse tool IDs.
				for {
					dismissals := make(map[string]int)
					for _, push := range bcast.snapshot() {
						if push.env.Type == protocol.TypeQuestionDismissed {
							var p protocol.QuestionDismissedPayload
							if err := json.Unmarshal(push.env.Payload, &p); err != nil {
								t.Fatal(err)
							}
							dismissals[p.QuestionBatchID]++
						}
					}
					if len(dismissals) == 2 {
						if dismissals[first.QuestionBatchID] != 1 || dismissals[second.QuestionBatchID] != 1 {
							t.Fatal("duplicate or miscorrelated dismissal")
						}
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("missing terminal dismissal")
					}
					time.Sleep(10 * time.Millisecond)
				}
				// A connected answerer keeps the next question parked past an expiry
				// check. Disconnecting then permits timeout to retire it fail-closed.
				for len(checked) > 0 {
					<-checked
				}
				r.Restart(args)
				third := awaitShown("replacement-session")
				select {
				case answerable := <-checked:
					if !answerable {
						t.Fatal("connected question became unanswerable")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("question expiry did not check answerer liveness")
				}
				if _, ok := f.qreg.Lookup(third.QuestionBatchID); !ok {
					t.Fatal("connected question expired")
				}
				bcast.mu.Lock()
				bcast.disconnected = true
				bcast.mu.Unlock()
				select {
				case <-retired:
				case <-time.After(5 * time.Second):
					t.Fatal("disconnected question did not time out")
				}
				if len(f.qreg.Snapshot()) != 0 || questionLen(f.bridge) != 0 {
					t.Fatal("timeout leaked question state")
				}
				if f.bridge.AnswerQuestion(third.QuestionBatchID, []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: []string{"B"}}}) {
					t.Fatal("late answer resolved a timed-out question")
				}
			})
		}
	}
}
