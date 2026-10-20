package claudetape

import (
	"context"
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

func TestDarwinCancelledLaunch(t *testing.T) {
	for _, scenario := range []string{"session-resume", "composer-stop"} {
		t.Run(scenario, func(t *testing.T) { testFallbackCancelledLaunchBoundary(t, scenario) })
	}
}

func testFallbackCancelledLaunchBoundary(t *testing.T, scenario string) {
	t.Helper()
	home := t.TempDir()
	if os.Chmod(home, 0700) != nil || os.Mkdir(filepath.Join(home, "recording-workers"), 0700) != nil {
		t.Fatal("private home")
	}
	root, evidence, cancelWorker := testLaunchOwner(t)
	pid := evidence.Worker
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	producer := lifecycleSuiteControl().Producer
	audit := StartFallbackLaunchAudit(home, scenario, exe, producer.Revision, producer.BinarySHA256, os.Getpid(), root)
	if PublishLifecycleControl(filepath.Join(home, "recording-worker-daemon.json"), struct{ PID int }{evidence.Daemon}) != nil {
		t.Fatal("daemon")
	}
	testDarwinSampled(t, audit, pid)
	audit.Stop()
	if len(audit.launches) != 1 || audit.launches[pid].Registration != "absent" {
		t.Fatal("missing independent current launch/absent-registration observation")
	}
	launch := audit.launches[pid]
	changed := launch
	changed.Start = "foreign-start"
	audit.launches[pid] = changed
	audit.sample()
	if !audit.invalid {
		t.Fatal("reused PID accepted")
	}
	audit.invalid = false
	audit.launches[pid] = launch
	claimedWait := FallbackWorkerObservation{OwnerPID: pid, GroupCancel: true, WaitCompleted: true, ExitObserved: true, Status: FallbackWorkerStatus{ExitCode: -1, Signal: int(syscall.SIGKILL)}}
	if _, err := audit.cancelled(home, scenario, producer.Revision, producer.BinarySHA256, os.Getpid(), evidence.Daemon, []FallbackWorkerObservation{claimedWait}); err == nil {
		t.Fatal("live process/group accepted as dead")
	}
	observation := cancelWorker()
	path := filepath.Join(home, "recording-worker-observations.json")
	if PublishLifecycleControl(path, []FallbackWorkerObservation{observation}) != nil {
		t.Fatal("Wait")
	}
	read := func(a ...*FallbackLaunchAudit) error {
		workers, err := ReadLifecycleWorkers(home, scenario, "replay", producer.Revision, producer.BinarySHA256, os.Getpid(), nil, a...)
		if len(workers) != 0 {
			t.Fatal("invented a native exchange")
		}
		return err
	}
	withoutAudit := read()
	t.Logf("scenario=%s launches=%+v registered=0 completed_Waits=%+v unaudited_inventory=%v", scenario, audit.launches, observation, withoutAudit)
	if withoutAudit == nil || !strings.Contains(withoutAudit.Error(), "total-wait-cardinality") {
		t.Fatal("Wait alone authorized a launch", withoutAudit)
	}
	if err := read(audit); err != nil {
		t.Fatalf("proved cancelled launch rejected: %v", err)
	}
	for _, kind := range []string{"unknown-launch", "unknown-registration", "registered", "wrong-source", "pid-reuse", "duplicate", "foreign", "no-cancel", "no-wait", "no-exit", "wrong-signal", "extra-launch"} {
		t.Run(kind, func(t *testing.T) {
			launch := audit.launches[pid]
			candidate := &FallbackLaunchAudit{home: home, scenario: scenario, revision: producer.Revision, sourceSHA: producer.BinarySHA256, parent: os.Getpid(), outer: audit.outer, root: audit.root, chain: audit.chain, stop: make(chan struct{}), done: audit.done, launches: map[int]workerLaunch{pid: launch}}
			observations := []FallbackWorkerObservation{observation}
			switch kind {
			case "unknown-launch":
				candidate.launches = map[int]workerLaunch{}
			case "unknown-registration":
				launch.Registration = "unknown"
				candidate.launches[pid] = launch
			case "pid-reuse":
				candidate.invalid = true
			case "wrong-source":
				candidate.sourceSHA = "foreign"
			case "registered":
				launch.Registration = "registered"
				candidate.launches[pid] = launch
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
				candidate.launches[pid+1] = launch
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
	var survivor *exec.Cmd
	if os.Getenv("PYRY_LAUNCH_LIVE_GROUP") == "1" {
		survivor = exec.CommandContext(ctx, "/bin/sleep", "30")
		survivor.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: cmd.Process.Pid}
		if survivor.Start() != nil {
			t.Fatal("owned group member")
		}
	}
	evidence := launchOwnerEvidence{Daemon: os.Getpid(), Worker: cmd.Process.Pid}
	encoder := json.NewEncoder(os.Stdout)
	if encoder.Encode(evidence) != nil {
		t.Fatal("publish launch")
	}
	target := -cmd.Process.Pid
	if survivor != nil {
		target = cmd.Process.Pid
	}
	if _, err := io.ReadFull(os.Stdin, ready[:]); err != nil || syscall.Kill(target, syscall.SIGKILL) != nil || cmd.Wait() == nil {
		t.Fatal("cancel and Wait")
	}
	status := cmd.ProcessState.Sys().(syscall.WaitStatus)
	evidence.Wait = FallbackWorkerObservation{OwnerPID: cmd.Process.Pid, GroupCancel: true, WaitCompleted: true, ExitObserved: true, Status: FallbackWorkerStatus{ExitCode: cmd.ProcessState.ExitCode(), Signal: int(status.Signal())}}
	if !status.Signaled() || status.Signal() != syscall.SIGKILL || encoder.Encode(evidence) != nil {
		t.Fatal("publish actual Wait")
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	if survivor != nil {
		_ = survivor.Process.Kill() // actual owned command, never a recorded PID
		_ = survivor.Wait()
	}
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

func TestDarwinLaunchOwnership(t *testing.T) {
	for _, scenario := range []string{"session-resume", "composer-stop"} {
		t.Run(scenario, func(t *testing.T) { testFallbackCancelledLaunchOwnership(t, scenario) })
	}
}

func testFallbackCancelledLaunchOwnership(t *testing.T, scenario string) {
	t.Helper()
	for _, name := range []string{"owned", "live-group", "unrelated-daemon", "unknown-registration", "unknown-chain", "changed-root", "changed-daemon", "changed-outer"} {
		t.Run(name, func(t *testing.T) {
			if name == "live-group" {
				t.Setenv("PYRY_LAUNCH_LIVE_GROUP", "1")
			}
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
			if name == "unknown-registration" {
				if os.WriteFile(FallbackWorkerJobPath(home, 1), []byte("{"), 0600) != nil {
					t.Fatal("partial registration")
				}
			}
			audit := StartFallbackLaunchAudit(home, scenario, exe, producer.Revision, producer.BinarySHA256, os.Getpid(), root)
			if name != "unrelated-daemon" {
				testDarwinSampled(t, audit, evidence.Worker)
			}
			audit.Stop()
			if name == "unknown-registration" && audit.launches[evidence.Worker].Registration != "unknown" {
				t.Fatal("unreadable registration treated as absent")
			}
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
			if name == "live-group" && (syscall.Kill(wait.OwnerPID, 0) != syscall.ESRCH || syscall.Kill(-wait.OwnerPID, 0) != nil) {
				t.Fatal("expected reaped worker with a current owned group member")
			}
			t.Logf("root=%d daemon=%d worker=%d Wait=%+v chain=%+v launches=%+v", root, evidence.Daemon, evidence.Worker, wait, audit.chain, audit.launches)
			if PublishLifecycleControl(filepath.Join(home, "recording-worker-observations.json"), []FallbackWorkerObservation{wait}) != nil {
				t.Fatal("Wait control")
			}
			workers, err := ReadLifecycleWorkers(home, scenario, "replay", producer.Revision, producer.BinarySHA256, os.Getpid(), nil, audit)
			if len(workers) != 0 || (name == "owned") != (err == nil) {
				t.Fatalf("ownership %s workers=%d inventory=%v", name, len(workers), err)
			}
		})
	}
}

func testDarwinSampled(t *testing.T, audit *FallbackLaunchAudit, pid int) {
	t.Helper()
	t.Cleanup(audit.Stop)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		audit.mu.Lock()
		_, ok := audit.launches[pid]
		audit.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	audit.Stop()
	t.Fatal("normal sampler did not observe held owned helper")
}
