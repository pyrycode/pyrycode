package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestReplyFallbackHelperProcess(t *testing.T) {
	if os.Getenv("PYRY_REPLY_HELPER") != "1" {
		return
	}
	input, _ := io.ReadAll(os.Stdin)
	var sides map[string]string
	if json.Unmarshal(input, &sides) != nil || len(sides) != 2 {
		os.Exit(2)
	}
	if sides["user"] != os.Getenv("PYRY_REPLY_USER") || sides["assistant"] != os.Getenv("PYRY_REPLY_ASSISTANT") {
		os.Exit(3)
	}
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "selected" {
		os.Exit(4)
	}
	for _, key := range []string{"CLAUDE_CODE_SAFE_MODE", "CLAUDE_CODE_NO_MODEL_FALLBACK", "CLAUDE_CODE_DISABLE_CLAUDE_MDS", "CLAUDE_CODE_DISABLE_AUTO_MEMORY", "CLAUDE_CODE_DISABLE_ATTACHMENTS"} {
		if os.Getenv(key) != "1" {
			os.Exit(5)
		}
	}
	if os.Getenv("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "" || os.Getenv("CLAUDE_CODE_SIMPLE") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		os.Exit(6)
	}
	if os.Getenv("PYRY_REPLY_RAW_MODE") == "1" {
		_, _ = os.Stdout.WriteString(os.Getenv("PYRY_REPLY_RAW"))
	}
	if os.Getenv("PYRY_REPLY_HANG") == "1" {
		_ = os.WriteFile(os.Getenv("PYRY_REPLY_READY"), []byte(strconv.Itoa(os.Getpid())), 0600)
		for {
			time.Sleep(time.Second)
		}
	}
	if os.Getenv("PYRY_REPLY_RAW_MODE") != "1" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"result": os.Getenv("PYRY_REPLY_OUTPUT"), "is_error": os.Getenv("PYRY_REPLY_ERROR") == "1"})
	}
	code, _ := strconv.Atoi(os.Getenv("PYRY_REPLY_EXIT"))
	os.Exit(code)
}

func TestReplyFallbackProcess(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "ambient")
	t.Setenv("ANTHROPIC_API_KEY", "ambient-api")
	t.Setenv("CLAUDE_CODE_SIMPLE", "1")
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "expensive")
	t.Setenv("PYRY_REPLY_USER", "@/secret $(not-a-command) ü")
	t.Setenv("PYRY_REPLY_ASSISTANT", "final only")
	calls, reads := 0, 0
	f := replyFallback{binary: "configured-claude", account: func(context.Context) (string, streamsup.AccountTokenFailure, error) {
		reads++
		return "selected", "", nil
	}}
	f.command = func(ctx context.Context, bin string, args ...string) *exec.Cmd {
		calls++
		if bin != "configured-claude" {
			t.Fatal("wrong binary")
		}
		for flag, want := range map[string]string{"--model": "haiku", "--fallback-model": "", "--max-turns": "1", "--output-format": "json", "--tools": "", "--setting-sources": "", "--mcp-config": `{"mcpServers":{}}`, "--settings": `{"disableAllHooks":true,"autoMemoryEnabled":false}`} {
			found := false
			for i, arg := range args {
				if arg == flag && i+1 < len(args) && args[i+1] == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing isolation flag %s", flag)
			}
		}
		for _, value := range args {
			if strings.Contains(value, "secret") || strings.Contains(value, "final only") {
				t.Fatal("exchange in argv")
			}
		}
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReplyFallbackHelperProcess$")
		return cmd
	}
	t.Setenv("PYRY_REPLY_HELPER", "1")
	for _, tc := range []struct {
		name, out string
		invalid   bool
	}{
		{"trim", "  Run the tests. \n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PYRY_REPLY_OUTPUT", tc.out)
			got, err := f.run(context.Background(), os.Getenv("PYRY_REPLY_USER"), "final only")
			if (err != nil) != tc.invalid {
				t.Fatalf("invalid=%v err=%v", tc.invalid, err)
			}
			if err == nil && got != strings.TrimSpace(tc.out) {
				t.Fatal("reply changed")
			}
		})
	}
	t.Setenv("PYRY_REPLY_ERROR", "1")
	if _, err := f.run(context.Background(), os.Getenv("PYRY_REPLY_USER"), "final only"); err == nil {
		t.Fatal("accepted error result")
	}
	before := calls
	f.account = func(context.Context) (string, streamsup.AccountTokenFailure, error) {
		return "", streamsup.AccountTokenReadFailure, errors.New("secret refusal")
	}
	if _, err := f.run(context.Background(), "user", "assistant"); err == nil || calls != before {
		t.Fatal("refusal launched child")
	}
	if reads != before {
		t.Fatalf("provider reads=%d launches=%d", reads, before)
	}
	releaseLookup := make(chan struct{})
	defer close(releaseLookup)
	f.account = func(context.Context) (string, streamsup.AccountTokenFailure, error) {
		<-releaseLookup
		return "selected", "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := f.run(ctx, "user", "assistant"); err == nil || calls != before {
		t.Fatal("lookup timeout launched")
	}
	if validReplyFallback(string([]byte{0xff})) || validReplyFallback(strings.Repeat("😀", 257)) {
		t.Fatal("invalid UTF-8/bytes accepted")
	}
}

// Fixture variables belong to the child, so output scenarios can run concurrently.
func replyFallbackTestCommand(ctx context.Context, env []string) *exec.Cmd {
	args := append([]string(nil), env...)
	args = append(args, os.Args[0], "-test.run=^TestReplyFallbackHelperProcess$")
	return exec.CommandContext(ctx, "/usr/bin/env", args...)
}

func TestReplyFallbackProcessOutput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, out string
		invalid   bool
	}{
		{"trim", "  Run the tests. \n", false}, {"blank", " \n", true},
		{"multiline", "a\nb", true}, {"control", "a\x1bb", true},
		{"separator", "a\u2028b", true}, {"paragraph", "a\u2029b", true},
		{"oversize", strings.Repeat("a", 241), true}, {"output cap", strings.Repeat("a", 8192), true}, {"unicode bound", strings.Repeat("😀", 240), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := []string{"PYRY_REPLY_HELPER=1", "PYRY_REPLY_USER=user", "PYRY_REPLY_ASSISTANT=final only", "PYRY_REPLY_OUTPUT=" + tc.out}
			f := replyFallback{
				account: func(context.Context) (string, streamsup.AccountTokenFailure, error) { return "selected", "", nil },
				command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd { return replyFallbackTestCommand(ctx, env) },
			}
			got, err := f.run(context.Background(), "user", "final only")
			if (err != nil) != tc.invalid {
				t.Fatalf("invalid=%v err=%v", tc.invalid, err)
			}
			if err == nil && got != strings.TrimSpace(tc.out) {
				t.Fatal("reply changed")
			}
		})
	}
}

func TestReplyFallbackProcessCancellation(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(strconv.FormatBool(timeout), func(t *testing.T) {
			t.Setenv("PYRY_REPLY_HELPER", "1")
			t.Setenv("PYRY_REPLY_HANG", "1")
			ready := t.TempDir() + "/ready"
			t.Setenv("PYRY_REPLY_READY", ready)
			t.Setenv("PYRY_REPLY_USER", "user")
			t.Setenv("PYRY_REPLY_ASSISTANT", "assistant")
			f := replyFallback{account: func(context.Context) (string, streamsup.AccountTokenFailure, error) { return "selected", "", nil }, command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReplyFallbackHelperProcess$")
			}}
			ctx, cancel := context.WithCancel(context.Background())
			if timeout {
				ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := f.run(ctx, "user", "assistant"); done <- err }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child not ready")
				}
				time.Sleep(time.Millisecond)
			}
			if !timeout {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancel succeeded")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancel hung")
			}
			data, _ := os.ReadFile(ready)
			pid, _ := strconv.Atoi(string(data))
			deadline = time.Now().Add(time.Second)
			for syscall.Kill(pid, 0) == nil {
				if time.Now().After(deadline) {
					t.Fatal("child survived cancellation")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestReplyFallbackProcessEvidence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "failure", "zero", "partial", "invalid utf8", "saturated", "parent cancel", "parent deadline", "own deadline"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			raw := map[string]string{"zero": "", "partial": `{"result":`, "invalid utf8": "\xff", "saturated": strings.Repeat(" ", 8192), "parent cancel": `{"result":"private-generated-sentinel"}`}
			ready := t.TempDir() + "/private-path-sentinel"
			env := []string{"PYRY_REPLY_HELPER=1", "PYRY_REPLY_USER=private-user-sentinel",
				"PYRY_REPLY_ASSISTANT=private-assistant-sentinel", "PYRY_REPLY_OUTPUT=private-generated-sentinel",
				"PRIVATE_VALUE=private-environment-sentinel", "PYRY_REPLY_READY=" + ready}
			if output, ok := raw[mode]; ok {
				env = append(env, "PYRY_REPLY_RAW_MODE=1", "PYRY_REPLY_RAW="+output)
			}
			hold := strings.Contains(mode, "deadline") || mode == "parent cancel"
			if hold {
				env = append(env, "PYRY_REPLY_HANG=1")
			}
			if mode == "failure" {
				env = append(env, "PYRY_REPLY_EXIT=7")
			}
			var logs bytes.Buffer
			var child *exec.Cmd
			f := replyFallback{logger: slog.New(slog.NewJSONHandler(&logs, nil)),
				account: func(context.Context) (string, streamsup.AccountTokenFailure, error) { return "selected", "", nil },
				command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
					child = replyFallbackTestCommand(ctx, env)
					return child
				}}
			parent, cancel := context.WithCancel(context.Background())
			if mode == "parent deadline" {
				parent, cancel = context.WithTimeout(context.Background(), 2*time.Second)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := f.run(parent, "private-user-sentinel", "private-assistant-sentinel"); done <- err }()
			if hold {
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(ready); err == nil {
						break
					}
					if time.Now().After(deadline) {
						cancel()
						<-done
						t.Fatal("child not ready")
					}
					time.Sleep(time.Millisecond)
				}
				if mode == "parent cancel" {
					cancel()
				}
			}
			select {
			case err := <-done:
				if (err == nil) != (mode == "success") {
					t.Fatal("execution contract changed")
				}
			case <-time.After(12 * time.Second):
				cancel()
				<-done
				t.Fatal("fallback hung")
			}
			var record map[string]any
			if json.Unmarshal(logs.Bytes(), &record) != nil || record["msg"] != "reply_fallback.lifecycle" || strings.Contains(logs.String(), "private-") || strings.Contains(logs.String(), "selected") {
				t.Fatal("missing or sensitive daemon evidence")
			}
			waited, ok := record["wait_completed"].(bool)
			if !ok || (!waited && !hold) {
				t.Fatal("invalid Wait evidence")
			}
			for key, want := range map[string]bool{
				"parent_canceled": mode == "parent cancel", "parent_deadline": mode == "parent deadline",
				"fallback_canceled": mode == "parent cancel", "fallback_deadline": strings.Contains(mode, "deadline"),
				"own_deadline_elapsed": mode == "own deadline", "group_cancel_requested": hold,
				"exit_observed": waited, "output_observed": waited,
			} {
				if record[key] != want {
					t.Fatalf("%s=%v want=%v", key, record[key], want)
				}
			}
			if !waited {
				for _, key := range []string{"exit_code", "exit_signal", "stdout_bytes", "stdout_cap_exceeded", "stdout_utf8_ok", "stdout_json_ok", "stdout_result_ok", "stdout_text_ok", "wait_ok", "wait_delay"} {
					if record[key] != "unknown" {
						t.Fatalf("invented %s without Wait", key)
					}
				}
				return // Bounded cancellation need not observe reaping inside its grace.
			}
			for key, want := range map[string]bool{
				"wait_ok": !hold && mode != "failure", "wait_delay": false,
				"stdout_cap_exceeded": mode == "saturated", "stdout_utf8_ok": mode != "invalid utf8",
			} {
				if record[key] != want {
					t.Fatalf("%s=%v want=%v", key, record[key], want)
				}
			}
			wantBytes := 0
			if output, ok := raw[mode]; ok {
				wantBytes = min(len(output), maxAccountTokenBytes+1)
			} else if !hold {
				encoded, _ := json.Marshal(map[string]any{"result": "private-generated-sentinel", "is_error": false})
				wantBytes = len(encoded) + 1
			}
			if record["stdout_bytes"] != float64(wantBytes) {
				t.Fatal("incorrect retained byte count")
			}
			decoded := mode == "success" || mode == "failure" || mode == "parent cancel"
			jsonWant := any(decoded)
			if mode == "invalid utf8" {
				jsonWant = "unknown"
			}
			if record["stdout_json_ok"] != jsonWant {
				t.Fatal("incorrect decode observation")
			}
			for _, key := range []string{"stdout_result_ok", "stdout_text_ok"} {
				want := any("unknown")
				if decoded {
					want = true
				}
				if record[key] != want {
					t.Fatalf("incorrect %s observation", key)
				}
			}
			code, signal := 0, 0
			if mode == "failure" {
				code = 7
			}
			if hold {
				code, signal = -1, int(syscall.SIGKILL)
			}
			if record["pid"] != float64(child.Process.Pid) || record["exit_code"] != float64(code) || record["exit_signal"] != float64(signal) {
				t.Fatal("wrong correlated child exit")
			}
			if attempt, ok := record["attempt_ms"].(float64); !ok || attempt < record["child_ms"].(float64) || record["child_ms"].(float64) < 0 {
				t.Fatal("invalid lifecycle timing")
			}
			if hold && syscall.Kill(child.Process.Pid, 0) == nil {
				t.Fatal("child survived group cancellation")
			}
		})
	}
}

func TestReplyFallbackLifecycle(t *testing.T) {
	for _, mode := range []string{"fallback", "native wait", "native pending", "late delivery", "invalid late delivery", "stale write", "stale session", "other conversation", "shutdown", "deleted", "blank final", "failed turn"} {
		t.Run(mode, func(t *testing.T) {
			e, s := newSuggestionEmitter(testConvID, suggestConvB)
			window := make(chan struct{})
			called := make(chan [2]string, 2)
			release := make(chan struct{})
			returned := make(chan struct{}, 1)
			s.waitNative = func(ctx context.Context) bool {
				select {
				case <-window:
					return true
				case <-ctx.Done():
					return false
				}
			}
			s.fallback = func(ctx context.Context, u, a string) (string, error) {
				called <- [2]string{u, a}
				<-release
				returned <- struct{}{}
				return "Fallback", nil
			}
			t.Cleanup(func() { s.stopFallbacks() })
			s.beginWrite(testConvID, 1)
			late := strings.Contains(mode, "late delivery")
			if !late {
				s.noteDelivered(testConvID, msgqueue.QueuedMessage{ID: 1, Text: "safe user"})
			}
			ctx := context.Background()
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "earlier", Text: "never send"})
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "final", Text: "Final "})
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "child", ParentToolCallID: "tool", Text: "secret child"})
			e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "final", Text: "answer"})
			if mode == "blank final" {
				e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "blank", Text: " "})
			}
			end := successEnd
			if mode == "failed turn" {
				end.IsError = true
			}
			e.HandleFor(ctx, testConvID, end)
			if mode == "native wait" {
				s.suggest(testConvID, "Native unchanged\n")
			}
			close(window)
			if late {
				deadline := time.Now().Add(time.Second)
				for {
					s.mu.Lock()
					expired := s.convs[testConvID].windowDone
					s.mu.Unlock()
					if expired {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("native window never elapsed")
					}
					time.Sleep(time.Millisecond)
				}
				if mode == "invalid late delivery" {
					s.accepted(testConvID, 2)
				}
				s.noteDelivered(testConvID, msgqueue.QueuedMessage{ID: 2, Text: "wrong user"})
				s.noteDelivered(testConvID, msgqueue.QueuedMessage{ID: 1, Text: "safe user"})
			}
			noCall := mode == "native wait" || mode == "invalid late delivery" || mode == "blank final" || mode == "failed turn"
			if noCall {
				s.stopFallbacks()
				select {
				case <-called:
					t.Fatal("unexpected fallback")
				default:
				}
				close(release)
			} else {
				select {
				case sides := <-called:
					if sides != [2]string{"safe user", "Final answer"} {
						t.Fatalf("wrong exchange: %q", sides)
					}
				case <-time.After(time.Second):
					t.Fatal("fallback missing")
				}
				switch mode {
				case "native pending":
					s.suggest(testConvID, "Native unchanged\n")
				case "stale write":
					s.beginWrite(testConvID, 2)
				case "stale session":
					s.bindSessions(func(string) (string, bool) { return "new-session", true })
				case "other conversation":
					s.invalidate(suggestConvB)
				case "shutdown":
					s.mu.Lock()
					s.stopped = true
					s.cancelLocked(s.convs[testConvID])
					s.mu.Unlock()
				case "deleted":
					s.forget(testConvID)
				}
				close(release)
				<-returned
				s.workers.Wait()
				s.stopFallbacks()
			}
			p, ok := suggestionFor(t, s, testConvID)
			want := ""
			switch mode {
			case "fallback", "late delivery", "other conversation":
				want = "Fallback"
			case "native wait", "native pending":
				want = "Native unchanged\n"
			}
			if want == "" {
				if ok && p.SuggestedReply != nil {
					t.Fatal("stale suggestion restored")
				}
			} else {
				if !ok || p.SuggestedReply == nil || *p.SuggestedReply != want {
					t.Fatalf("missing %q", want)
				}
				s.suggest(testConvID, "second")
				s.accepted(testConvID, 3)
				clear, _ := suggestionFor(t, s, testConvID)
				if clear.SuggestedReply != nil || clear.Revision <= p.Revision {
					t.Fatal("clear missing")
				}
			}
			select {
			case <-called:
				t.Fatal("second fallback")
			default:
			}
		})
	}
}

func TestReplyFallbackInputBounds(t *testing.T) {
	_, s := newSuggestionEmitter(testConvID)
	s.beginWrite(testConvID, 1)
	s.noteDelivered(testConvID, msgqueue.QueuedMessage{ID: 1, Text: strings.Repeat("😀", 3000)})
	s.noteAssistantText(testConvID, turnevent.TextChunk{MessageID: "m", Text: strings.Repeat("a", 8191) + "😀"})
	s.noteAssistantText(testConvID, turnevent.TextChunk{MessageID: "m", Text: "later"})
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.convs[testConvID]
	if len(c.userText) != 8192 || len(c.assistantText) != 8191 || !utf8.ValidString(c.userText) || !utf8.ValidString(c.assistantText) {
		t.Fatal("input not bounded at first UTF-8 prefix")
	}
}

func TestReplySuggestionEligibilityBeyondPrefix(t *testing.T) {
	t.Parallel()
	prefix := strings.Repeat(" ", replyExchangeBytes)
	for _, source := range []string{"native", "fallback"} {
		for _, side := range []string{"user", "assistant", "assistant chunks", "blank final", "subagent only"} {
			t.Run(source+"/"+side, func(t *testing.T) {
				e, s := newSuggestionEmitter(testConvID)
				calls := make(chan [2]string, 1)
				s.fallback = func(_ context.Context, user, assistant string) (string, error) {
					calls <- [2]string{user, assistant}
					return "Fallback", nil
				}
				s.waitNative = func(ctx context.Context) bool {
					if source == "native" {
						<-ctx.Done()
						return false
					}
					return true
				}
				t.Cleanup(s.stopFallbacks)
				user, assistant := "user", "answer"
				if side == "user" {
					user = prefix + "prose"
				}
				s.beginWrite(testConvID, 1)
				s.noteDelivered(testConvID, msgqueue.QueuedMessage{ID: 1, Text: user})
				if side != "user" {
					assistant = prefix
					if side != "assistant chunks" {
						assistant += "prose"
					} else {
						assistant += " " // Fill retention before the nonblank chunk arrives.
					}
				}
				ctx := context.Background()
				e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "final", Text: assistant})
				if side == "assistant chunks" {
					e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "final", Text: "prose"})
				}
				if side == "blank final" || side == "subagent only" {
					e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "blank", Text: prefix})
				}
				if side == "subagent only" {
					e.HandleFor(ctx, testConvID, turnevent.TextChunk{MessageID: "child", ParentToolCallID: "tool", Text: "prose"})
				}
				e.HandleFor(ctx, testConvID, successEnd)
				if source == "native" {
					s.suggest(testConvID, "Native unchanged\n")
					s.stopFallbacks()
				}
				s.workers.Wait()
				eligible := side != "blank final" && side != "subagent only"
				p, ok := suggestionFor(t, s, testConvID)
				if published := ok && p.SuggestedReply != nil; published != eligible {
					t.Fatalf("published=%v, eligible=%v", published, eligible)
				}
				if eligible && source == "native" && *p.SuggestedReply != "Native unchanged\n" {
					t.Fatal("native suggestion changed")
				}
				select {
				case exchange := <-calls:
					if source != "fallback" || !eligible || exchange != [2]string{replyExchangePrefix(user), replyExchangePrefix(assistant)} {
						t.Fatal("unexpected fallback exchange")
					}
				default:
					if source == "fallback" && eligible {
						t.Fatal("missing fallback")
					}
				}
			})
		}
	}
}

func TestReplyFallbackRegistryRemovalCancellation(t *testing.T) {
	t.Parallel()
	for _, removal := range []string{"delete", "sweep"} {
		for _, phase := range []string{"native wait", "inference"} {
			t.Run(removal+"/"+phase, func(t *testing.T) {
				e, s := newSuggestionEmitter(testConvID, suggestConvB)
				reg := &conversations.Registry{}
				now := time.Now()
				reg.Create(conversations.Conversation{ID: testConvID, LastUsedAt: now.Add(-365 * 24 * time.Hour)})
				reg.Create(conversations.Conversation{ID: suggestConvB, LastUsedAt: now})
				ring := eventring.New(2)
				ring.Append(testConvID, "turn_state", nil, now)
				ring.Append(suggestConvB, "turn_state", nil, now)
				dropRingOnConversationDelete(reg, ring, s)
				suggestedTurn(e, s, suggestConvB)
				s.suggest(suggestConvB, "Keep this")
				ready, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				calls := make(chan struct{}, 1)
				s.waitNative = func(ctx context.Context) bool {
					if phase == "inference" {
						return true
					}
					close(ready)
					<-ctx.Done()
					close(cancelled)
					return false
				}
				s.fallback = func(ctx context.Context, _, _ string) (string, error) {
					calls <- struct{}{}
					close(ready)
					<-ctx.Done()
					close(cancelled)
					<-release
					return "Late fallback", nil
				}
				t.Cleanup(s.stopFallbacks)
				released := false
				t.Cleanup(func() {
					if !released {
						close(release)
					}
				})
				suggestedTurn(e, s, testConvID)
				select {
				case <-ready:
				case <-time.After(time.Second):
					t.Fatal("worker not ready")
				}
				if removal == "delete" {
					if !reg.Delete(testConvID) {
						t.Fatal("conversation not deleted")
					}
				} else if n := conversations.Sweep(reg, now); n != 1 {
					t.Fatalf("swept %d conversations", n)
				}
				select {
				case <-cancelled:
				case <-time.After(time.Second):
					t.Fatal("registry removal did not cancel worker")
				}
				close(release)
				released = true
				s.workers.Wait()
				if ring.NewestID(testConvID) != 0 || ring.NewestID(suggestConvB) == 0 {
					t.Fatal("removal did not preserve replay isolation")
				}
				s.mu.Lock()
				_, retained := s.convs[testConvID]
				kept := s.convs[suggestConvB].text
				s.mu.Unlock()
				if retained || kept == nil || *kept != "Keep this" {
					t.Fatal("removal did not forget only the deleted suggestion state")
				}
				if phase == "native wait" && len(calls) != 0 {
					t.Fatal("deleted conversation launched inference")
				}
			})
		}
	}
}

func TestReplyFallbackOutputUnknownWait(t *testing.T) {
	// A nil buffer makes an accidental read fail; an unobserved Wait owns it.
	fields := replyFallbackOutput(nil, false, nil)
	got := make(map[string]any)
	for i := 0; i < len(fields); i += 2 {
		got[fields[i].(string)] = fields[i+1]
	}
	if got["output_observed"] != false {
		t.Fatal("invented capture")
	}
	for _, key := range []string{"stdout_bytes", "stdout_cap_exceeded", "stdout_utf8_ok", "stdout_json_ok", "stdout_result_ok", "stdout_text_ok", "wait_ok", "wait_delay"} {
		if got[key] != "unknown" {
			t.Fatalf("invented %s", key)
		}
	}
}

func TestReplyFallbackOutputPredicates(t *testing.T) {
	for _, tc := range []struct {
		name, data         string
		json, result, text any
	}{
		{"null preserves result", `{"result":"private-text","result":null}`, true, true, true},
		{"null preserves error", `{"result":"private-text","is_error":true,"is_error":null}`, true, false, true},
		{"duplicate result", `{"result":"","result":"private-text"}`, true, true, true},
		{"null preserves subtype", `{"result":"private-text","subtype":"failure","subtype":null}`, true, false, true},
		{"duplicate type error", `{"result":7,"result":"private-text"}`, false, "unknown", "unknown"},
		{"text invalid", `{"result":"private-text\nnext"}`, true, true, false},
		{"null envelope", `null`, true, true, false},
		{"saturated decodable", `{"result":"private-text"}` + strings.Repeat(" ", 8192), true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout cappedBuffer
			_, _ = stdout.Write([]byte(tc.data))
			fields := replyFallbackOutput(&stdout, true, exec.ErrWaitDelay)
			got := make(map[string]any)
			for i := 0; i < len(fields); i += 2 {
				got[fields[i].(string)] = fields[i+1]
			}
			for key, want := range map[string]any{"stdout_json_ok": tc.json, "stdout_result_ok": tc.result, "stdout_text_ok": tc.text, "wait_delay": true, "wait_ok": false, "stdout_bytes": min(len(tc.data), 4097), "stdout_cap_exceeded": len(tc.data) > 4096} {
				if got[key] != want {
					t.Fatalf("incorrect %s predicate", key)
				}
			}
		})
	}
}
