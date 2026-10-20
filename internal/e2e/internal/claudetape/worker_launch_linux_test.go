package claudetape

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLifecycleTapeHelperProcess(t *testing.T) {
	if os.Getenv("PYRY_CANCELLED_LAUNCH_CONTROL") != "1" {
		return
	}
	_, _ = os.Stdout.Write([]byte{1})
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestFallbackCancelledLaunchBoundary(t *testing.T) {
	home := t.TempDir()
	if os.Chmod(home, 0700) != nil || os.Mkdir(filepath.Join(home, "recording-workers"), 0700) != nil {
		t.Fatal("private home")
	}
	cwd, err := os.MkdirTemp("", "pyry-reply-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(cwd) })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, append([]string{"-test.run=^TestLifecycleTapeHelperProcess$", "--"}, fallbackWorkerControlArgs()...)...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "PYRY_CANCELLED_LAUNCH_CONTROL=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Start() != nil {
		t.Fatal("launch")
	}
	t.Cleanup(func() {
		_ = input.Close()
		_ = cmd.Process.Kill()
		if cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
	})
	_ = output.(*os.File).SetReadDeadline(time.Now().Add(5 * time.Second))
	var ready [1]byte
	if _, err := io.ReadFull(output, ready[:]); err != nil || ready[0] != 1 {
		t.Fatal("helper not ready", err)
	}
	image, _ := os.Stat(exe)
	done := make(chan struct{})
	close(done)
	producer := lifecycleSuiteControl().Producer
	audit := &FallbackLaunchAudit{home: home, scenario: "session-resume", binary: exe, image: image, revision: producer.Revision, sourceSHA: producer.BinarySHA256, parent: os.Getpid(), stop: make(chan struct{}), done: done, launches: map[int]workerLaunch{}}
	audit.observe(cmd.Process.Pid, os.Getpid())
	if len(audit.launches) != 1 || audit.launches[cmd.Process.Pid].Registration != "absent" {
		t.Fatal("missing independent current launch/absent-registration observation")
	}
	launch := audit.launches[cmd.Process.Pid]
	changed := launch
	changed.Start = "foreign-start"
	audit.launches[cmd.Process.Pid] = changed
	audit.observe(cmd.Process.Pid, os.Getpid())
	if !audit.invalid {
		t.Fatal("reused PID accepted")
	}
	audit.invalid = false
	audit.launches[cmd.Process.Pid] = launch
	if syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) != nil {
		t.Fatal("cancel")
	}
	if cmd.Wait() == nil {
		t.Fatal("cancel did not kill")
	}
	status := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatal("wrong actual Wait")
	}
	observation := FallbackWorkerObservation{OwnerPID: cmd.Process.Pid, GroupCancel: true, WaitCompleted: true, ExitObserved: true, Status: FallbackWorkerStatus{ExitCode: cmd.ProcessState.ExitCode(), Signal: int(status.Signal())}}
	if PublishLifecycleControl(filepath.Join(home, "recording-worker-daemon.json"), struct{ PID int }{os.Getpid()}) != nil {
		t.Fatal("daemon")
	}
	path := filepath.Join(home, "recording-worker-observations.json")
	if PublishLifecycleControl(path, []FallbackWorkerObservation{observation}) != nil {
		t.Fatal("Wait")
	}
	read := func(a ...*FallbackLaunchAudit) error {
		workers, err := ReadLifecycleWorkers(home, "session-resume", "replay", producer.Revision, producer.BinarySHA256, os.Getpid(), nil, a...)
		if len(workers) != 0 {
			t.Fatal("invented a native exchange")
		}
		return err
	}
	if err := read(); err == nil || !strings.Contains(err.Error(), "total-wait-cardinality") {
		t.Fatal("Wait alone authorized a launch", err)
	}
	if err := read(audit); err != nil {
		t.Fatalf("proved cancelled launch rejected: %v", err)
	}
	for _, kind := range []string{"unknown-launch", "unknown-registration", "registered", "wrong-source", "pid-reuse", "duplicate", "foreign", "no-cancel", "no-wait", "no-exit", "wrong-signal", "extra-launch"} {
		t.Run(kind, func(t *testing.T) {
			launch := audit.launches[cmd.Process.Pid]
			candidate := &FallbackLaunchAudit{home: home, scenario: "session-resume", revision: producer.Revision, sourceSHA: producer.BinarySHA256, parent: os.Getpid(), stop: make(chan struct{}), done: done, launches: map[int]workerLaunch{cmd.Process.Pid: launch}}
			observations := []FallbackWorkerObservation{observation}
			switch kind {
			case "unknown-launch":
				candidate.launches = map[int]workerLaunch{}
			case "unknown-registration":
				launch.Registration = "unknown"
				candidate.launches[cmd.Process.Pid] = launch
			case "pid-reuse":
				candidate.invalid = true
			case "wrong-source":
				candidate.sourceSHA = "foreign"
			case "registered":
				launch.Registration = "registered"
				candidate.launches[cmd.Process.Pid] = launch
			case "duplicate":
				observations = append(observations, observation)
			case "foreign":
				observations[0].OwnerPID++
			case "no-cancel":
				observations[0].GroupCancel = false
			case "no-wait":
				observations[0].WaitCompleted = false
			case "no-exit":
				observations[0].ExitObserved = false
			case "wrong-signal":
				observations[0].Status.Signal = 15
			case "extra-launch":
				candidate.launches[cmd.Process.Pid+1] = launch
			}
			raw, _ := json.Marshal(observations)
			if os.WriteFile(path, raw, 0600) != nil {
				t.Fatal("control")
			}
			if read(candidate) == nil {
				t.Fatal("unproved launch accepted")
			}
		})
	}
}
