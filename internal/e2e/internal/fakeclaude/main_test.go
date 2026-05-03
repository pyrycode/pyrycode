//go:build e2e

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"
)

func TestFakeClaude_OpensInitialAndRotatesOnTrigger(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	triggerPath := filepath.Join(tmp, "rotate.trigger")
	initialUUID := "11111111-1111-4111-8111-111111111111"
	binPath := filepath.Join(tmp, "fakeclaude")

	out, err := exec.Command("go", "build", "-o", binPath,
		"github.com/pyrycode/pyrycode/internal/e2e/internal/fakeclaude").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	cmd := exec.Command(binPath)
	cmd.Env = append(os.Environ(),
		"PYRY_FAKE_CLAUDE_SESSIONS_DIR="+sessionsDir,
		"PYRY_FAKE_CLAUDE_INITIAL_UUID="+initialUUID,
		"PYRY_FAKE_CLAUDE_TRIGGER="+triggerPath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	doneCh := make(chan error, 1)
	go func() { doneCh <- cmd.Wait() }()

	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			<-doneCh
		}
	})

	initialPath := filepath.Join(sessionsDir, initialUUID+".jsonl")
	if !waitForFile(initialPath, 3*time.Second) {
		t.Fatalf("initial JSONL not created within deadline: %s", initialPath)
	}

	if err := os.WriteFile(triggerPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	uuidStem := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	var rotatedUUID string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(sessionsDir)
		for _, e := range entries {
			name := e.Name()
			if filepath.Ext(name) != ".jsonl" {
				continue
			}
			stem := name[:len(name)-len(".jsonl")]
			if stem == initialUUID {
				continue
			}
			if !uuidStem.MatchString(stem) {
				continue
			}
			rotatedUUID = stem
			break
		}
		if rotatedUUID != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if rotatedUUID == "" {
		t.Fatalf("no rotated JSONL appeared in %s within deadline", sessionsDir)
	}

	if _, err := os.Stat(triggerPath); !os.IsNotExist(err) {
		t.Fatalf("trigger file still present after rotation: err=%v", err)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal SIGTERM: %v", err)
	}
	select {
	case waitErr := <-doneCh:
		if !signaledBy(cmd.ProcessState, syscall.SIGTERM) {
			t.Fatalf("unexpected exit: err=%v state=%+v", waitErr, cmd.ProcessState)
		}
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-doneCh
		t.Fatalf("did not exit within 3s of SIGTERM")
	}
}

func waitForFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func signaledBy(ps *os.ProcessState, sig syscall.Signal) bool {
	ws, ok := ps.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	return ws.Signaled() && ws.Signal() == sig
}
