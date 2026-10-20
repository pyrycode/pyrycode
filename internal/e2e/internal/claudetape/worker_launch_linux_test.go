package claudetape

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	audit.Stop()
	audit.observe(pid, evidence.Daemon)
	if len(audit.launches) != 1 || audit.launches[pid].Registration != "absent" {
		t.Fatal("missing independent current launch/absent-registration observation")
	}
	launch := audit.launches[pid]
	changed := launch
	changed.Start = "foreign-start"
	audit.launches[pid] = changed
	audit.observe(pid, evidence.Daemon)
	if !audit.invalid {
		t.Fatal("reused PID accepted")
	}
	audit.invalid = false
	audit.launches[pid] = launch
	observation := cancelWorker()
	if PublishLifecycleControl(filepath.Join(home, "recording-worker-daemon.json"), struct{ PID int }{evidence.Daemon}) != nil {
		t.Fatal("daemon")
	}
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
	if err := read(); err == nil || !strings.Contains(err.Error(), "total-wait-cardinality") {
		t.Fatal("Wait alone authorized a launch", err)
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
