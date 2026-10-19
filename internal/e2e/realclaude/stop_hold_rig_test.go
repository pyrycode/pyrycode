//go:build e2e_realclaude

package realclaude

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// stopHoldOperation witnesses execution of one private, argument-free operation.
// Its marker is independent of FIFO arrival and of model-authored text.
type stopHoldOperation struct {
	path, witness string
}

func newStopHoldOperation(t *testing.T, dir, fifo string) *stopHoldOperation {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal("create hold directory")
	}
	h := &stopHoldOperation{path: filepath.Join(dir, "stop-hold"), witness: filepath.Join(dir, "stop-hold.executed")}
	script := "#!/bin/sh\numask 077\nprintf x >> " + stopShellQuote(h.witness) + " || exit 1\nexec cat " + stopShellQuote(fifo) + "\n"
	if err := os.WriteFile(h.path, []byte(script), 0o700); err != nil {
		t.Fatal("create hold executable")
	}
	return h
}

func stopShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func stopDoubleQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(s) + `"`
}

var stopBarePath = regexp.MustCompile(`^[/a-zA-Z0-9._-]+$`)

// matches accepts only the executable token, with optional exec and terminator.
// Arguments and compound commands cannot borrow another invocation's witness.
func (h *stopHoldOperation) matches(command string) bool {
	command = strings.TrimSpace(command)
	command = strings.TrimSpace(strings.TrimSuffix(command, ";"))
	if strings.HasPrefix(command, "exec ") {
		command = strings.TrimSpace(strings.TrimPrefix(command, "exec "))
	}
	return (stopBarePath.MatchString(h.path) && command == h.path) || command == stopShellQuote(h.path) || command == stopDoubleQuote(h.path)
}

// mentioned only vetoes attribution; a text occurrence never proves execution.
func (h *stopHoldOperation) mentioned(command string) bool {
	return strings.Contains(command, h.path) || strings.Contains(command, stopShellQuote(h.path)) || strings.Contains(command, stopDoubleQuote(h.path))
}

func (h *stopHoldOperation) executed() bool {
	f, err := os.Open(h.witness)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 2))
	return err == nil && string(data) == "x"
}

func (h *stopHoldOperation) prompt() string {
	return fmt.Sprintf("Use Bash exactly once with run_in_background=true to run this test-owned hold executable: %s . The command must be only that executable, with no arguments or other operations. It waits for the test rig; do not inspect, stop, release or retry it. After starting it, reply briefly.", stopShellQuote(h.path))
}
