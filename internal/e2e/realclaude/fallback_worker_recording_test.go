//go:build e2e_realclaude

package realclaude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

// The standard live root keeps its original daemon, settings, 5-second idle
// timer and fallback. Recording adds only private launch/Wait observations.
func lifecycleRecordingWorkerDaemon(t *testing.T, home string, pid int, stderr *lockedBuffer) {
	t.Helper()
	c, ok := lifecycleRecordingCurrent(t)
	if !ok || claudetape.LifecycleWorkerLimit(c.Process.Scenario) == 0 {
		return
	}
	if home != c.Process.Home || pid <= 1 || claudetape.PublishLifecycleControl(filepath.Join(home, "recording-worker-daemon.json"), struct{ PID int }{pid}) != nil {
		t.Fatal("finite worker daemon identity rebound")
	}
	// Registered inside spawn, before the original caller registers daemon.stop.
	// LIFO cleanup therefore observes actual production Waits after that stop.
	t.Cleanup(func() {
		if t.Failed() {
			return
		}
		observations := []claudetape.FallbackWorkerObservation{}
		lines := strings.Split(stderr.String(), "\n")
		for _, line := range lines[:len(lines)-1] {
			fields := suggestLogScalars(line)
			if fields["msg"] != "reply_fallback.lifecycle" {
				continue
			}
			owner, e1 := strconv.Atoi(fields["pid"])
			code, e2 := strconv.Atoi(fields["exit_code"])
			signal, e3 := strconv.Atoi(fields["exit_signal"])
			if owner <= 1 || e1 != nil || e2 != nil || e3 != nil || fields["wait_completed"] != "true" || fields["exit_observed"] != "true" || (fields["group_cancel_requested"] != "true" && fields["group_cancel_requested"] != "false") {
				t.Fatal("finite worker lacks actual production Wait status")
			}
			observations = append(observations, claudetape.FallbackWorkerObservation{OwnerPID: owner, WaitCompleted: true, ExitObserved: true, GroupCancel: fields["group_cancel_requested"] == "true", Status: claudetape.FallbackWorkerStatus{ExitCode: code, Signal: signal}})
		}
		if claudetape.PublishLifecycleControl(filepath.Join(home, "recording-worker-observations.json"), observations) != nil {
			t.Fatal("finite worker Wait inventory rebound")
		}
	})
}
func lifecycleRecordingFiniteWorker(c lifecycleRecordingConfig, args []string) bool {
	print := false
	for _, arg := range args {
		print = print || arg == "--print"
	}
	if !print {
		return false
	}
	fail := func() {
		_ = os.WriteFile(filepath.Join(c.Process.Home, "recording-unexpected"), []byte("unowned lifecycle invocation kind=finite-print"), 0600)
		os.Exit(2)
	}
	if claudetape.LifecycleWorkerLimit(c.Process.Scenario) == 0 {
		fail()
	}
	var daemon struct{ PID int }
	if claudetape.ReadLifecycleControl(filepath.Join(c.Process.Home, "recording-worker-daemon.json"), 16384, &daemon) != nil || daemon.PID <= 1 || os.Getenv("HOME") != c.Process.Home {
		fail()
	}
	expectedParent := daemon.PID
	if c.Process.Scenario == "fallback-suggestion" {
		expectedParent = syscall.Getpgrp()
		if expectedParent == os.Getpid() {
			fail()
		}
	} else if syscall.Getpgrp() != os.Getpid() {
		fail()
	}
	if os.Getppid() != expectedParent {
		fail()
	}
	cwd, err := os.Getwd()
	if err != nil {
		fail()
	}
	jobPath := ""
	for i := 1; i <= claudetape.LifecycleWorkerLimit(c.Process.Scenario); i++ {
		path := claudetape.FallbackWorkerJobPath(c.Process.Home, i)
		if _, err := os.Lstat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			fail()
		}
		job := claudetape.FallbackWorkerJob{Version: 1, OwnerPID: os.Getpid(), GroupPID: syscall.Getpgrp(), DaemonPID: daemon.PID, BrokerParentPID: c.BrokerParentPID, Scenario: c.Process.Scenario, Home: c.Process.Home, WorkDir: cwd, Claude: c.Process.Claude, Path: strings.TrimSuffix(path, ".job") + ".json", Abort: strings.TrimSuffix(path, ".job") + ".abort", SourceBinarySHA256: c.SourceBinarySHA256, Args: args, Producer: c.Process.Producer}
		if c.Process.Mode == "replay" {
			if i > len(c.WorkerWaits) {
				fail()
			}
			job.ReplayWait = c.WorkerWaits[i-1]
		}
		if err := claudetape.PublishLifecycleControl(path, job); err == nil {
			jobPath = path
			break
		} else if _, err := os.Lstat(path); err != nil {
			fail()
		}
	}
	if jobPath == "" {
		fail()
	}
	status, err := claudetape.ConnectFallbackWorker(filepath.Join(c.Process.Home, "worker.sock"), jobPath, os.Environ())
	if err != nil {
		fail()
	}
	if status.Signal != 0 {
		// A canceled replay helper cannot reach here: its broker waits for actual
		// production cancellation first. Never manufacture a recorded cancellation.
		fail()
	}
	os.Exit(status.ExitCode)
	return true
}
func TestFallbackWorkerBrokerProcess(t *testing.T) {
	path := os.Getenv("PYRY_LIFECYCLE_RECORDING_CONFIG")
	if path == "" {
		return
	}
	c, err := readLifecycleRecordingConfig(path)
	if err != nil {
		os.Exit(2)
	}
	jobPath := ""
	for i, arg := range os.Args {
		if arg == "--" && i+2 == len(os.Args) {
			jobPath = os.Args[i+1]
		}
	}
	var job claudetape.FallbackWorkerJob
	if claudetape.ReadLifecycleControl(jobPath, 16384, &job) != nil || claudetape.ValidateFallbackWorkerLaunch(job, jobPath, job.OwnerPID, c.BrokerParentPID, c.Process, c.SourceBinarySHA256) != nil || os.Getppid() != c.BrokerParentPID {
		os.Exit(2)
	}
	if err := claudetape.RunFallbackWorkerBroker(job, c.Process.Mode); err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(struct{ WorkerError string }{err.Error()})
		os.Exit(2)
	}
	os.Exit(0)
}
