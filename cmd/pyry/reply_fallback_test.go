package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/debugbundle"
	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestReplyFallbackHelperProcess(t *testing.T) {
	if os.Getenv("PYRY_REPLY_HELPER") != "1" {
		return
	}
	if os.Getenv("PYRY_REPLY_DESCENDANT") == "1" {
		_ = os.WriteFile(os.Getenv("PYRY_REPLY_DESC_READY"), []byte(strconv.Itoa(os.Getpid())), 0600)
		for {
			time.Sleep(time.Second)
		}
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
	for range 256 {
		_, _ = os.Stderr.WriteString(os.Getenv("PYRY_REPLY_STDERR_PREFIX"))
	}
	_, _ = os.Stderr.WriteString(os.Getenv("PYRY_REPLY_STDERR"))
	if path := os.Getenv("PYRY_REPLY_DESC_READY"); path != "" {
		child := exec.Command(os.Args[0], "-test.run=^TestReplyFallbackHelperProcess$")
		child.Env = append(os.Environ(), "PYRY_REPLY_DESCENDANT=1")
		child.Stderr = os.Stderr
		if os.Getenv("PYRY_REPLY_DESC_ESCAPE") == "1" {
			child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		}
		if child.Start() != nil {
			os.Exit(8)
		}
		for {
			if _, err := os.Stat(path); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	if os.Getenv("PYRY_REPLY_RAW_MODE") == "1" {
		_, _ = os.Stdout.WriteString(os.Getenv("PYRY_REPLY_RAW"))
	}
	if fd, err := strconv.Atoi(os.Getenv("PYRY_REPLY_READY_FD")); err == nil {
		ready := os.NewFile(uintptr(fd), "reply-ready")
		if _, err := ready.Write([]byte{1}); err != nil {
			os.Exit(9)
		}
		_ = ready.Close()
	}
	if os.Getenv("PYRY_REPLY_HANG") == "1" {
		_ = os.WriteFile(os.Getenv("PYRY_REPLY_READY"), []byte(strconv.Itoa(os.Getpid())), 0600)
		for {
			time.Sleep(time.Second)
		}
	}
	if delay, err := time.ParseDuration(os.Getenv("PYRY_REPLY_RESULT_DELAY")); err == nil {
		time.Sleep(delay)
	}
	if os.Getenv("PYRY_REPLY_RAW_MODE") != "1" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "result", "result": os.Getenv("PYRY_REPLY_OUTPUT"), "is_error": os.Getenv("PYRY_REPLY_ERROR") == "1"})
	}
	if delay, err := time.ParseDuration(os.Getenv("PYRY_REPLY_EXIT_DELAY")); err == nil {
		time.Sleep(delay)
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
		for flag, want := range map[string]string{"--model": "haiku", "--fallback-model": "", "--max-turns": "1", "--output-format": "stream-json", "--tools": "", "--setting-sources": "", "--mcp-config": `{"mcpServers":{}}`, "--settings": `{"disableAllHooks":true,"autoMemoryEnabled":false}`} {
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
		if !slices.Contains(args, "--verbose") {
			t.Fatal("missing verbose stream flag")
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
			raw := map[string]string{"zero": "", "partial": `{"type":"result","result":`, "invalid utf8": "\xff", "saturated": strings.Repeat(" ", 8192), "parent cancel": `{"type":"result","result":"private-generated-sentinel"}` + "\n"}
			env := []string{"PYRY_REPLY_HELPER=1", "PYRY_REPLY_USER=private-user-sentinel",
				"PYRY_REPLY_ASSISTANT=private-assistant-sentinel", "PYRY_REPLY_OUTPUT=private-generated-sentinel",
				"PRIVATE_VALUE=private-environment-sentinel", "PRIVATE_PATH=/private-path-sentinel"}
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
			base, cancel := context.WithCancelCause(context.Background())
			parent := testReplyFallbackContext{base}
			a := testStartReplyFallback(t, parent, func() { cancel(context.Canceled) }, env, mode == "own deadline", nil)
			if hold {
				a.awaitReady(t)
				if mode == "parent cancel" {
					cancel(context.Canceled)
				} else if mode == "parent deadline" {
					deadline, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
					defer stop()
					<-deadline.Done()
					cancel(deadline.Err())
				}
			}
			a.awaitReturn(t, replyFallbackBudget)
			if mode == "own deadline" {
				record := a.record(t)
				elapsed, ok := record["attempt_ms"].(float64)
				if !ok || elapsed < float64(replyFallbackDeadline.Milliseconds()) || elapsed >= float64(replyFallbackBudget.Milliseconds()) {
					t.Fatal("hung child exceeded the total budget or used the wrong own deadline")
				}
			}
			if mode == "success" {
				if a.err != nil || a.text != "private-generated-sentinel" {
					t.Fatal("success execution contract changed")
				}
			} else if a.text != "" || !errors.Is(a.err, errReplyFallback) {
				t.Fatal("failure execution contract changed")
			}
			record := a.record(t)
			if mode == "own deadline" && record["wait_completed"] != false {
				t.Fatal("own deadline observed held completion")
			}
			// Reaping is a separate proof from the immutable return-time snapshot.
			a.unhold()
			a.awaitWait(t)
			child := a.child
			if hold {
				status := child.ProcessState.Sys().(syscall.WaitStatus)
				if !status.Signaled() || status.Signal() != syscall.SIGKILL {
					t.Fatal("child survived group cancellation")
				}
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
				testReplyFallbackUnknownCompletion(t, record)
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
				encoded, _ := json.Marshal(map[string]any{"type": "result", "result": "private-generated-sentinel", "is_error": false})
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
	var stream replyFallbackStream
	_, _ = stream.Write([]byte(`{"type":"system","subtype":"init"}` + "\n"))
	fields := replyFallbackOutput(&stream, false, nil)
	progress := stream.progressFields()
	encoded, _ := json.Marshal(progress)
	if !bytes.Contains(encoded, []byte(`"init"`)) {
		t.Fatal("lost independent observation without Wait")
	}
	_ = replyFallbackOutput(nil, false, nil) // Completion path must not dereference an inaccessible writer.
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
		{"null preserves result", `{"type":"result","result":"private-text","result":null}`, true, true, true},
		{"null preserves error", `{"type":"result","result":"private-text","is_error":true,"is_error":null}`, true, false, true},
		{"duplicate result", `{"type":"result","result":"","result":"private-text"}`, true, true, true},
		{"null preserves subtype", `{"type":"result","result":"private-text","subtype":"failure","subtype":null}`, true, false, true},
		{"duplicate type error", `{"type":"result","result":7,"result":"private-text"}`, false, "unknown", "unknown"},
		{"text invalid", `{"type":"result","result":"private-text\nnext"}`, true, true, false},
		{"null envelope", `null`, false, "unknown", "unknown"},
		{"oversized envelope", `{"type":"result","result":"private-text"}` + strings.Repeat(" ", 8192), false, "unknown", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout replyFallbackStream
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

func TestReplyFallbackStream(t *testing.T) {
	result := `{"type":"result","result":"private-text"}` + "\n"
	for _, tc := range []struct {
		name, input, event, source string
		retries                    int
		result                     bool
	}{
		{"result", result, "result", "unknown", 0, true},
		{"long progress", strings.Repeat(`{"type":"system","subtype":"api_retry","attempt":99}`+"\n", 100) + result, "result", "unknown", 100, true},
		{"unknown", `{"type":"private-event"}` + "\n", "none", "unknown", 0, false},
		{"partial", `{"type":"system","subtype":"init"`, "none", "unknown", 0, false},
		{"invalid", "\xff\n{bad}\n" + result, "result", "unknown", 0, true},
		{"oversized", `{"type":"result","result":"` + strings.Repeat("x", 4096) + `"}` + "\n" + result, "result", "unknown", 0, true},
		{"oversized only", `{"type":"result","result":"` + strings.Repeat("x", 4096) + `"}` + "\n", "none", "unknown", 0, false},
		{"init", `{"type":"system","subtype":"init","apiKeySource":"none"}` + "\n", "init", "none", 0, false},
		{"source rejection", `{"type":"system","subtype":"init","apiKeySource":"private-secret"}` + "\n", "init", "unknown", 0, false},
		{"source missing", `{"type":"system","subtype":"init"}` + "\n", "init", "unknown", 0, false},
		{"source null", `{"type":"system","subtype":"init","apiKeySource":null}` + "\n", "init", "unknown", 0, false},
		{"source type", `{"type":"system","subtype":"init","apiKeySource":7}` + "\n", "init", "unknown", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, chunk := range []int{1, 17, len(tc.input)} {
				var stream replyFallbackStream
				for start := 0; start < len(tc.input); start += chunk {
					_, _ = stream.Write([]byte(tc.input[start:min(start+chunk, len(tc.input))]))
				}
				stream.finish()
				if stream.current.event != tc.event && !(tc.event == "none" && stream.current.event == "") || stream.current.source != tc.source && !(tc.source == "unknown" && stream.current.source == "") || stream.current.retries != tc.retries || stream.decoded != tc.result || len(stream.line) > 4096 {
					t.Fatal("incorrect bounded observation")
				}
				fields := append(replyFallbackOutput(&stream, true, nil), stream.progressFields()...)
				encoded, _ := json.Marshal(fields)
				if strings.Contains(string(encoded), "private-") {
					t.Fatal("sensitive evidence")
				}
			}
		})
	}
}

func TestReplyFallbackStreamFreeze(t *testing.T) {
	for _, before := range []bool{false, true} {
		var stream replyFallbackStream
		if before {
			_, _ = stream.Write([]byte(`{"type":"system","subtype":"init","apiKeySource":"ANTHROPIC_API_KEY"}` + "\n"))
		}
		stream.freeze()
		frozen := stream.frozen
		var workers sync.WaitGroup
		for range 4 {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for range 20 {
					stream.freeze()
					_, _ = stream.Write([]byte(`{"type":"system","subtype":"api_retry"}` + "\n"))
				}
			}()
		}
		workers.Wait()
		_, _ = stream.Write([]byte(`{"type":"result","result":"private-late"}` + "\n"))
		if stream.frozen != frozen || !stream.cancelFrozen || stream.current.retries != 80 {
			t.Fatal("frozen snapshot changed")
		}
		if before != frozen.init || frozen.retries != 0 || (!before && !frozen.at.IsZero()) {
			t.Fatal("invented pre-cancellation progress")
		}
	}
}

func TestReplyFallbackStderrTail(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", ""}, {"a\r\n", "a"}, {"0\n1\n2\n3\n4\n5\n6\r\n", "2\n3\n4\n5\n6"},
		{strings.Repeat("x", 4096) + "end", strings.Repeat("x", 1021) + "end"},
	} {
		for _, chunk := range []int{1, 1024, 8192} {
			var tail replyFallbackStderrTail
			for start := 0; start < len(tc.input); start += chunk {
				_, _ = tail.Write([]byte(tc.input[start:min(start+chunk, len(tc.input))]))
			}
			if tail.String() != tc.want || len(tail.buf) > 1024 || cap(tail.buf) > 1024 {
				t.Fatal("incorrect bounded suffix")
			}
		}
	}
	var tail replyFallbackStderrTail
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 100 {
				_, _ = tail.Write(bytes.Repeat([]byte("x"), 8192))
				_ = tail.String()
			}
		}()
	}
	workers.Wait()
	if len(tail.String()) != 1024 {
		t.Fatal("lost bounded concurrent tail")
	}
}

func TestReplyFallbackStderrCleanup(t *testing.T) {
	for _, mode := range []string{"failed start", "eof", "forced", "read error"} {
		t.Run(mode, func(t *testing.T) {
			cmd := &exec.Cmd{}
			c := captureReplyFallbackStderr(cmd, os.Pipe)
			if c.pr == nil {
				t.Fatal("pipe setup failed")
			}
			writer, err := syscall.Dup(int(c.pw.Fd()))
			if err != nil {
				t.Fatal("duplicate pipe failed")
			}
			w := os.NewFile(uintptr(writer), "test-stderr")
			defer w.Close()
			c.started(mode != "failed start")
			if mode == "failed start" {
				if _, err := c.pr.Read(make([]byte, 1)); err == nil {
					t.Fatal("failed Start retained reader")
				}
				return
			}
			_, _ = w.WriteString("private-cleanup-sentinel")
			if mode == "eof" {
				_ = w.Close()
				<-c.done
			}
			if mode == "read error" {
				_ = c.pr.Close()
				<-c.done
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			fields := c.finish(ctx)
			got := testReplyFields(fields)
			if got["stderr_observed"] != true || got["stderr_reader_done"] != true || got["stderr_eof"] != (mode == "eof") || got["stderr_partial"] != (mode != "eof") {
				t.Fatal("incorrect reader boundary")
			}
			select {
			case <-c.done:
			default:
				t.Fatal("capture reader not joined")
			}
			if _, err := c.pr.Read(make([]byte, 1)); err == nil {
				t.Fatal("reader descriptor remained open")
			}
		})
	}
}

func testReplyFields(fields []any) map[string]any {
	out := make(map[string]any)
	for i := 0; i < len(fields); i += 2 {
		out[fields[i].(string)] = fields[i+1]
	}
	return out
}

func TestReplyFallbackStderrProcess(t *testing.T) {
	for _, mode := range []string{"success", "nonzero", "empty", "unavailable", "cancel", "unobserved wait", "descendant", "cancel descendant"} {
		t.Run(mode, func(t *testing.T) {
			tail := "\nprivate-tail-sentinel \"quoted\" \\path\nmsg=reply_fallback.lifecycle pid=999 wait_completed=true\nline-three\nline-four\nlast\r\n"
			if mode == "empty" {
				tail = ""
			}
			ready, desc := t.TempDir()+"/ready", t.TempDir()+"/desc"
			env := []string{"PYRY_REPLY_HELPER=1", "PYRY_REPLY_USER=user", "PYRY_REPLY_ASSISTANT=assistant", "PYRY_REPLY_OUTPUT=reply", "PYRY_REPLY_STDERR=" + tail, "PYRY_REPLY_READY=" + ready}
			if mode == "success" {
				env = append(env, "PYRY_REPLY_STDERR_PREFIX="+strings.Repeat("private-dropped-sentinel\n", 100))
			}
			if mode == "nonzero" {
				env = append(env, "PYRY_REPLY_EXIT=7")
			}
			if strings.HasPrefix(mode, "cancel") || mode == "unobserved wait" {
				env = append(env, "PYRY_REPLY_HANG=1")
			}
			if strings.Contains(mode, "descendant") {
				env = append(env, "PYRY_REPLY_DESC_READY="+desc, "PYRY_REPLY_DESC_ESCAPE=1")
			}
			var primary bytes.Buffer
			ring := control.NewRingBuffer(10)
			var pr, pw *os.File
			waitDone := make(chan struct{})
			f := replyFallback{logger: slog.New(control.SlogTee(slog.NewTextHandler(&primary, nil), ring)),
				account: func(context.Context) (string, streamsup.AccountTokenFailure, error) { return "selected", "", nil },
				command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd { return replyFallbackTestCommand(ctx, env) },
				stderrPipe: func() (*os.File, *os.File, error) {
					if mode == "unavailable" {
						return nil, nil, errors.New("private-setup-sentinel")
					}
					var err error
					pr, pw, err = os.Pipe()
					return pr, pw, err
				}}
			if mode == "unobserved wait" {
				f.wait = func(cmd *exec.Cmd) error {
					defer close(waitDone)
					err := cmd.Wait()
					time.Sleep(300 * time.Millisecond)
					return err
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if strings.Contains(mode, "descendant") {
				defer func() {
					data, _ := os.ReadFile(desc)
					pid, _ := strconv.Atoi(string(data))
					if pid > 0 {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}()
			}
			done := make(chan error, 1)
			go func() { _, err := f.run(ctx, "user", "assistant"); done <- err }()
			var canceledAt time.Time
			if strings.HasPrefix(mode, "cancel") || mode == "unobserved wait" {
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(ready); err == nil {
						break
					}
					if time.Now().After(deadline) {
						cancel()
						<-done
						t.Fatal("helper not ready")
					}
					time.Sleep(time.Millisecond)
				}
				canceledAt = time.Now()
				cancel()
			}
			select {
			case err := <-done:
				if (err != nil) != (mode == "nonzero" || strings.HasPrefix(mode, "cancel") || mode == "unobserved wait") {
					t.Fatal("capture changed classification")
				}
			case <-time.After(3 * time.Second):
				cancel()
				<-done
				t.Fatal("stderr held return")
			}
			if mode == "cancel descendant" && time.Since(canceledAt) >= 200*time.Millisecond {
				t.Fatal("cancellation added drain grace")
			}
			if mode == "unobserved wait" {
				<-waitDone
			}
			local := primary.String()
			if strings.Count(local, "\n") != 1 {
				t.Fatal("tail forged a log record")
			}
			if mode == "unavailable" {
				if !strings.Contains(local, "stderr_observed=false") || strings.Contains(local, "stderr_tail=") {
					t.Fatal("unavailable capture invented content")
				}
			} else {
				if !strings.Contains(local, "stderr_tail="+strconv.Quote(strings.TrimPrefix(strings.TrimRight(tail, "\r\n"), "\n"))) || !strings.Contains(local, "stderr_reader_done=true") {
					t.Fatal("missing local bounded capture")
				}
				if strings.Contains(local, "private-dropped-sentinel") {
					t.Fatal("retained beginning instead of end")
				}
				if strings.Contains(mode, "descendant") && !strings.Contains(local, "stderr_partial=true") {
					t.Fatal("descendant inferred EOF")
				}
				if mode == "descendant" && (!strings.Contains(local, "stderr_partial=true") || !strings.Contains(local, "wait_ok=true")) {
					t.Fatal("descendant changed exit or EOF")
				}
			}
			if mode == "unobserved wait" && (!strings.Contains(local, "wait_completed=false") || !strings.Contains(local, "exit_code=unknown") || !strings.Contains(local, "output_observed=false")) {
				t.Fatal("invented Wait receipt")
			}
			if pr != nil {
				if _, err := pr.Read(make([]byte, 1)); err == nil {
					t.Fatal("return retained capture reader")
				}
				if _, err := pw.WriteString("x"); err == nil {
					t.Fatal("parent retained writer")
				}
			}
			exported := strings.Join(ring.Snapshot(), "\n")
			archive, _, err := debugbundle.Assemble(t.TempDir(), ring.Snapshot())
			if err != nil {
				t.Fatal("bundle assembly failed")
			}
			gz, err := gzip.NewReader(bytes.NewReader(archive))
			if err != nil {
				t.Fatal("bundle decompression failed")
			}
			defer gz.Close()
			tr := tar.NewReader(gz)
			for {
				_, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal("bundle member failed")
				}
				b, err := io.ReadAll(tr)
				if err != nil {
					t.Fatal("bundle read failed")
				}
				exported += string(b)
			}
			if strings.Contains(exported, "private-") || strings.Contains(exported, "pid=999") {
				t.Fatal("daemon-only content reached exports")
			}
			if mode != "unavailable" && !strings.Contains(exported, "(daemon log only)") {
				t.Fatal("ring marker missing")
			}
		})
	}
}

func TestReplyFallbackStderrFailedStart(t *testing.T) {
	var pr, pw *os.File
	f := replyFallback{binary: "/missing-reply-helper", stderrPipe: func() (*os.File, *os.File, error) { var err error; pr, pw, err = os.Pipe(); return pr, pw, err }}
	if _, err := f.run(context.Background(), "user", "assistant"); err == nil {
		t.Fatal("missing helper started")
	}
	if pr == nil {
		t.Fatal("capture not prepared")
	}
	if _, err := pr.Read(make([]byte, 1)); err == nil {
		t.Fatal("failed launch retained reader")
	}
	if _, err := pw.WriteString("x"); err == nil {
		t.Fatal("failed launch retained writer")
	}
}
