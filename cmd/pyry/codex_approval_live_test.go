//go:build codex_capture

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TestCodexApprovalLive shows, against a real Codex, that a declined command
// does not run and an accepted one does. It requires the codex_capture build
// tag and PYRY_CODEX_CAPTURE_BIN and PYRY_CODEX_CAPTURE_HOME, as TestCaptureLive
// does, and it spends Codex allowance on gpt-6-luna at low effort.
func TestCodexApprovalLive(t *testing.T) {
	bin, home := os.Getenv("PYRY_CODEX_CAPTURE_BIN"), os.Getenv("PYRY_CODEX_CAPTURE_HOME")
	if bin == "" || home == "" {
		t.Skip("set PYRY_CODEX_CAPTURE_BIN and PYRY_CODEX_CAPTURE_HOME to run the live Codex approval test")
	}
	dir := t.TempDir()
	registry := permbridge.New()
	surface := &approvalSurfaceReport{show: func(req permbridge.Request) func() {
		// Only the exact accepted.txt touch is allowed; everything else is
		// declined, including declined.txt.
		if strings.Contains(req.Description, "touch accepted.txt") && !strings.Contains(req.Description, "declined") {
			go registry.Resolve(req.ToolUseID, permbridge.Allow(req.Input))
		} else {
			go registry.Resolve(req.ToolUseID, permbridge.Deny("declined by the live test"))
		}
		return func() {}
	}}
	events := make(chan turnevent.Event, 1024)
	r := newCodexRunner(codexRunnerConfig{
		Binary: bin, Home: home, Dir: dir,
		Tag:       newStreamSessionTag("live-1"),
		Sink:      func(ev turnevent.Event) { events <- ev },
		Model:     "gpt-6-luna",
		Effort:    "low",
		Approvals: newCodexApprovals(registry, time.Minute, surface, nil),
	})
	h := &codexHarnessT{r: r, events: events}
	h.run(t)
	h.bound(t, nil)
	for _, name := range []string{"declined.txt", "accepted.txt"} {
		if err := r.WriteUserTurn(context.Background(), "c-1", []byte("Use your shell tool to run `touch "+name+"` in the current directory, then reply done.")); err != nil {
			t.Fatalf("WriteUserTurn: %v", err)
		}
		select {
		case <-waitTurnEnd(events):
		case <-time.After(3 * time.Minute):
			t.Fatalf("turn for %s never ended", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "declined.txt")); !os.IsNotExist(err) {
		t.Errorf("declined.txt: stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "accepted.txt")); err != nil {
		t.Errorf("accepted.txt was not created: %v", err)
	}
}
