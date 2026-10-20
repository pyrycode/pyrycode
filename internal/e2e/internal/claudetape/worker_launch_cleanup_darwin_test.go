package claudetape

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

func testStartLaunchOwner(t *testing.T) (*exec.Cmd, io.WriteCloser, *os.File, func() error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.MkdirTemp("", "pyry-reply-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(cwd) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestFallbackLaunchOwnerProcess$")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "PYRY_LAUNCH_OWNER_CONTROL=root")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		t.Fatal("root start")
	}
	var once sync.Once
	var cleanupErr error
	cleanup := func() error {
		once.Do(func() {
			// EOF requests cancellation from the daemon that still owns the child.
			_ = input.Close()
			cleanupErr = cmd.Wait()
			cancel()
		})
		return cleanupErr
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error("cleanup root Wait", err)
		}
	})
	pipe := output.(*os.File)
	if pipe.SetReadDeadline(time.Now().Add(5*time.Second)) != nil {
		t.Fatal("evidence deadline")
	}
	return cmd, input, pipe, cleanup
}

func TestDarwinLaunchCleanup(t *testing.T) {
	for _, stage := range []string{"before-cancel", "after-reap"} {
		t.Run(stage, func(t *testing.T) {
			t.Setenv("PYRY_LAUNCH_LIVE_GROUP", "1")
			_, input, output, cleanup := testStartLaunchOwner(t)
			decoder := json.NewDecoder(output)
			var evidence launchOwnerEvidence
			if decoder.Decode(&evidence) != nil || evidence.Worker <= 1 || evidence.Daemon <= 1 {
				t.Fatal("owned launch evidence")
			}
			foreign := exec.Command("/bin/sleep", "30")
			foreign.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if foreign.Start() != nil {
				t.Fatal("unrelated group")
			}
			t.Cleanup(func() { _ = foreign.Process.Kill(); _ = foreign.Wait() })
			if stage == "after-reap" {
				if _, err := input.Write([]byte{1}); err != nil || decoder.Decode(&evidence) != nil || !evidence.Wait.WaitCompleted {
					t.Fatal("actual completed Wait")
				}
			}
			if output.SetReadDeadline(time.Now().Add(-time.Second)) != nil {
				t.Fatal("expire evidence decoder")
			}
			var missing launchOwnerEvidence
			if err := decoder.Decode(&missing); !os.IsTimeout(err) {
				t.Fatalf("expected evidence timeout, got %v", err)
			}
			if err := cleanup(); err != nil {
				t.Fatal("daemon cleanup after evidence timeout", err)
			}
			if syscall.Kill(evidence.Worker, 0) != syscall.ESRCH || syscall.Kill(-evidence.Worker, 0) != syscall.ESRCH || syscall.Kill(evidence.Daemon, 0) != syscall.ESRCH {
				t.Fatal("owned process or group survived cleanup")
			}
			if syscall.Kill(-foreign.Process.Pid, 0) != nil {
				t.Fatal("cleanup disturbed unrelated group")
			}
		})
	}
}
