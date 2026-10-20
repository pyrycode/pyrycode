package claudetape

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

type launchOwnerEvidence struct {
	Daemon, Worker int
	Wait           FallbackWorkerObservation
}

func TestFallbackLaunchOwnerProcess(t *testing.T) {
	role := os.Getenv("PYRY_LAUNCH_OWNER_CONTROL")
	if role == "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestFallbackLaunchOwnerProcess$")
	cmd.Env = append(os.Environ(), "PYRY_LAUNCH_OWNER_CONTROL=daemon")
	cmd.Stdin, cmd.Stdout = os.Stdin, os.Stdout
	if role == "root" {
		if cmd.Run() != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	cmd = exec.CommandContext(ctx, exe, append([]string{"-test.run=^TestLifecycleTapeHelperProcess$", "--"}, fallbackWorkerControlArgs()...)...)
	cmd.Env = append(os.Environ(), "PYRY_CANCELLED_LAUNCH_CONTROL=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		t.Fatal("worker start")
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	var ready [1]byte
	if _, err := io.ReadFull(output, ready[:]); err != nil || ready[0] != 1 {
		t.Fatal("worker ready")
	}
	evidence := launchOwnerEvidence{Daemon: os.Getpid(), Worker: cmd.Process.Pid}
	encoder := json.NewEncoder(os.Stdout)
	if encoder.Encode(evidence) != nil {
		t.Fatal("publish launch")
	}
	if _, err := io.ReadFull(os.Stdin, ready[:]); err != nil || syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) != nil || cmd.Wait() == nil {
		t.Fatal("cancel and Wait")
	}
	status := cmd.ProcessState.Sys().(syscall.WaitStatus)
	evidence.Wait = FallbackWorkerObservation{OwnerPID: cmd.Process.Pid, GroupCancel: true, WaitCompleted: true, ExitObserved: true, Status: FallbackWorkerStatus{ExitCode: cmd.ProcessState.ExitCode(), Signal: int(status.Signal())}}
	if !status.Signaled() || status.Signal() != syscall.SIGKILL || encoder.Encode(evidence) != nil {
		t.Fatal("publish actual Wait")
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func testLaunchOwner(t *testing.T) (int, launchOwnerEvidence, func() FallbackWorkerObservation) {
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
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestFallbackLaunchOwnerProcess$")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "PYRY_LAUNCH_OWNER_CONTROL=root")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		t.Fatal("root start")
	}
	var evidence launchOwnerEvidence
	waited := false
	decoder := json.NewDecoder(output)
	t.Cleanup(func() {
		defer cancel()
		defer func() {
			if cmd.ProcessState == nil {
				if evidence.Worker > 1 {
					_ = syscall.Kill(-evidence.Worker, syscall.SIGKILL)
				}
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				_ = cmd.Wait()
			}
		}()
		if evidence.Worker > 1 && !waited {
			if _, err := input.Write([]byte{1}); err != nil || decoder.Decode(&evidence) != nil {
				t.Error("cleanup cancellation/Wait")
				return
			}
		}
		_ = input.Close()
		if cmd.Wait() != nil {
			t.Error("cleanup root Wait")
		}
	})
	_ = output.(*os.File).SetReadDeadline(time.Now().Add(5 * time.Second))
	if decoder.Decode(&evidence) != nil || evidence.Daemon <= 1 || evidence.Worker <= 1 {
		t.Fatal("current daemon/helper identities")
	}
	return cmd.Process.Pid, evidence, func() FallbackWorkerObservation {
		if _, err := input.Write([]byte{1}); err != nil || decoder.Decode(&evidence) != nil {
			t.Fatal("completed cancellation Wait")
		}
		waited = true
		return evidence.Wait
	}
}

func TestFallbackCancelledLaunchOwnership(t *testing.T) {
	for _, name := range []string{"owned", "unrelated-daemon", "unknown-chain", "changed-root", "changed-daemon", "changed-outer"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			if os.Chmod(home, 0700) != nil || os.Mkdir(filepath.Join(home, "recording-workers"), 0700) != nil {
				t.Fatal("private home")
			}
			root, evidence, cancelWorker := testLaunchOwner(t)
			if name == "unrelated-daemon" {
				_, evidence, cancelWorker = testLaunchOwner(t)
			}
			if PublishLifecycleControl(filepath.Join(home, "recording-worker-daemon.json"), struct{ PID int }{evidence.Daemon}) != nil {
				t.Fatal("daemon control")
			}
			exe, _ := os.Executable()
			producer := lifecycleSuiteControl().Producer
			audit := StartFallbackLaunchAudit(home, "session-resume", exe, producer.Revision, producer.BinarySHA256, os.Getpid(), root)
			audit.Stop()
			switch name {
			case "unknown-chain":
				audit.chain = nil
			case "changed-root":
				audit.root.Start = "stale"
				audit.sample()
			case "changed-daemon":
				audit.chain[0].Start = "stale"
				audit.sample()
			case "changed-outer":
				audit.outer.Start = "stale"
				audit.sample()
			}
			wait := cancelWorker()
			t.Logf("root=%d daemon=%d worker=%d Wait=%+v chain=%+v launches=%+v", root, evidence.Daemon, evidence.Worker, wait, audit.chain, audit.launches)
			if PublishLifecycleControl(filepath.Join(home, "recording-worker-observations.json"), []FallbackWorkerObservation{wait}) != nil {
				t.Fatal("Wait control")
			}
			workers, err := ReadLifecycleWorkers(home, "session-resume", "replay", producer.Revision, producer.BinarySHA256, os.Getpid(), nil, audit)
			if len(workers) != 0 || (name == "owned") != (err == nil) {
				t.Fatalf("ownership %s workers=%d inventory=%v", name, len(workers), err)
			}
		})
	}
}
