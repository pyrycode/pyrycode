//go:build e2e_realclaude

package realclaude

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type backgroundReplyExecution struct {
	Operation string `json:"operation_id"`
	Tool      string `json:"tool_use_id"`
	PID       int    `json:"pid,omitempty"`
}

type backgroundReplyOperation struct{ dir, path, hook, operation string }

func backgroundReplyQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func newBackgroundReplyOperation(t *testing.T, work string) *backgroundReplyOperation {
	t.Helper()
	r := &backgroundReplyOperation{dir: filepath.Join(work, "hold-rig"), operation: rand.Text()}
	if err := os.MkdirAll(filepath.Join(r.dir, "witnesses"), 0o700); err != nil {
		t.Fatal("create hold rig")
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal("locate helper binary")
	}
	for _, mode := range []string{"hold", "hook"} {
		path := filepath.Join(r.dir, mode)
		script := "#!/bin/sh\nexec env PYRY_REPLY_HELPER=" + mode + " PYRY_REPLY_DIR=" + backgroundReplyQuote(r.dir) + " PYRY_REPLY_OPERATION=" + backgroundReplyQuote(r.operation) + " " + backgroundReplyQuote(bin) + " '-test.run=^TestBackgroundReplyHelperProcess$'\n"
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal("write hold helper")
		}
		if mode == "hold" {
			r.path = path
		} else {
			r.hook = path
		}
	}
	t.Cleanup(r.release)
	return r
}

func (r *backgroundReplyOperation) release() {
	f, err := os.OpenFile(filepath.Join(r.dir, "release"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, _ = f.WriteString("\n") // Best-effort cleanup release; normal release retains its token.
		_ = f.Close()
	} // Existing test-owned release must retain its private token.
}

func (r *backgroundReplyOperation) witness() *backgroundReplyExecution {
	files, err := filepath.Glob(filepath.Join(r.dir, "witnesses", "*.json"))
	if err != nil || len(files) != 1 {
		return nil
	}
	b, err := os.ReadFile(files[0])
	var w backgroundReplyExecution
	if err != nil || json.Unmarshal(b, &w) != nil || w.Operation != r.operation || w.Tool == "" || w.PID <= 0 {
		return nil
	}
	return &w
}

func (r *backgroundReplyOperation) held(w *backgroundReplyExecution) bool {
	if w == nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(r.dir, "release")); !os.IsNotExist(err) {
		return false
	}
	state, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(w.PID)).Output()
	s := strings.TrimSpace(string(state))
	return err == nil && s != "" && !strings.HasPrefix(s, "Z")
}

// The hook binds execution to the real tool ID without interpreting its command.
// Only the separate hold process publishes evidence, so hook arrival is insufficient.
func TestBackgroundReplyHelperProcess(t *testing.T) {
	mode := os.Getenv("PYRY_REPLY_HELPER")
	if mode == "" {
		return
	}
	if mode == "hook" {
		var in struct {
			Name  string         `json:"tool_name"`
			Tool  string         `json:"tool_use_id"`
			Input map[string]any `json:"tool_input"`
		}
		if json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&in) != nil || in.Name != "Bash" || in.Tool == "" {
			os.Exit(2)
		}
		command, ok := in.Input["command"].(string)
		if !ok {
			os.Exit(2)
		}
		in.Input["command"] = "export PYRY_REPLY_TOOL=" + backgroundReplyQuote(in.Tool) + "; " + command
		if json.NewEncoder(os.Stdout).Encode(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "updatedInput": in.Input}}) != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if mode != "hold" {
		os.Exit(2)
	}
	w := backgroundReplyExecution{os.Getenv("PYRY_REPLY_OPERATION"), os.Getenv("PYRY_REPLY_TOOL"), os.Getpid()}
	if w.Operation == "" || w.Tool == "" {
		os.Exit(2)
	}
	dir := os.Getenv("PYRY_REPLY_DIR")
	b, err := json.Marshal(w)
	if err != nil {
		os.Exit(2)
	}
	f, err := os.CreateTemp(filepath.Join(dir, "witnesses"), ".pending-")
	if err != nil {
		os.Exit(2)
	}
	if _, err := f.Write(b); err != nil {
		os.Exit(2)
	}
	if f.Close() != nil || os.Rename(f.Name(), f.Name()+".json") != nil {
		os.Exit(2)
	}
	for deadline := time.Now().Add(180 * time.Second); time.Now().Before(deadline); {
		if b, err := os.ReadFile(filepath.Join(dir, "release")); err == nil && len(b) > 0 && b[len(b)-1] == '\n' {
			_, _ = fmt.Print(string(b))
			os.Exit(0)
		}
		time.Sleep(20 * time.Millisecond)
	}
	os.Exit(2)
}
