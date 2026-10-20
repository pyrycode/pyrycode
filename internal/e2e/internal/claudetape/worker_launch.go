package claudetape

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// FallbackLaunchAudit is an outer-owned observation capability. Its entries
// cannot be supplied by a Wait log, job file or native provider.
type FallbackLaunchAudit struct {
	home, scenario, binary string
	revision, sourceSHA    string
	parent                 int
	outer, root            workerProcess
	chain                  []workerProcess
	image                  os.FileInfo
	stop, done             chan struct{}
	once                   sync.Once
	launches               map[int]workerLaunch
	invalid                bool
}

type workerLaunch struct {
	PID, DaemonPID     int
	Start, DaemonStart string
	Registration       string
}

type workerProcess struct {
	PID, Parent, Group int
	Start              string
}

// StartFallbackLaunchAudit observes current helper launches without delaying or
// signalling production. Unobserved launches remain unknown, never absent.
// root is the actual command just started by parent, which must be this process.
func StartFallbackLaunchAudit(home, scenario, binary, revision, sourceSHA string, parent, root int) *FallbackLaunchAudit {
	a := &FallbackLaunchAudit{home: home, scenario: scenario, binary: binary, revision: revision, sourceSHA: sourceSHA, parent: parent, stop: make(chan struct{}), done: make(chan struct{}), launches: map[int]workerLaunch{}}
	a.image, _ = os.Stat(binary)
	a.bindRoot(root)
	go func() {
		defer close(a.done)
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			a.sample()
			select {
			case <-a.stop:
				return
			case <-tick.C:
			}
		}
	}()
	return a
}

// Stop joins the sampler before its immutable observations are read.
func (a *FallbackLaunchAudit) Stop() {
	a.once.Do(func() { close(a.stop) })
	<-a.done
}

func (a *FallbackLaunchAudit) cancelled(home, scenario, revision, sourceSHA string, parent, daemon int, observations []FallbackWorkerObservation) (map[int]bool, error) {
	a.Stop()
	if a.home != home || a.scenario != scenario || a.revision != revision || a.sourceSHA != sourceSHA || a.parent != parent || scenario != "session-resume" || a.invalid || len(a.launches) > LifecycleWorkerLimit(scenario) {
		return nil, os.ErrInvalid
	}
	joined := map[int]bool{}
	for pid, launch := range a.launches {
		if len(a.chain) < 3 || a.chain[0].PID != daemon || a.chain[len(a.chain)-1] != a.outer || a.chain[len(a.chain)-2] != a.root || a.outer.PID != parent || a.root.Parent != parent || launch.PID != pid || launch.DaemonPID != daemon || launch.DaemonStart != a.chain[0].Start || launch.Start == "" {
			return nil, os.ErrInvalid
		}
		registered := false
		for i := 1; i <= LifecycleWorkerLimit(scenario); i++ {
			var job FallbackWorkerJob
			path := FallbackWorkerJobPath(home, i)
			if _, err := os.Lstat(path); os.IsNotExist(err) {
				continue
			}
			if ReadLifecycleControl(path, 16384, &job) != nil {
				return nil, os.ErrInvalid
			}
			registered = registered || job.OwnerPID == pid
		}
		if registered {
			continue
		}
		if launch.Registration != "absent" {
			return nil, os.ErrInvalid
		}
		matches := 0
		for _, o := range observations {
			if o.OwnerPID != pid {
				continue
			}
			matches++
			if !o.GroupCancel || !o.WaitCompleted || !o.ExitObserved || o.Status != (FallbackWorkerStatus{ExitCode: -1, Signal: int(syscall.SIGKILL)}) {
				return nil, os.ErrInvalid
			}
		}
		if matches != 1 || syscall.Kill(pid, 0) != syscall.ESRCH || syscall.Kill(-pid, 0) != syscall.ESRCH {
			return nil, os.ErrInvalid
		}
		joined[pid] = true
	}
	return joined, nil
}

// Save retains only bounded current identity/registration and completed-Wait
// observations. It supplies no native tape, receipt or promoted coverage.
func (a *FallbackLaunchAudit) Save(destination string) error {
	a.Stop()
	var observations []FallbackWorkerObservation
	err := ReadLifecycleControl(filepath.Join(a.home, "recording-worker-observations.json"), 16384, &observations)
	raw, e := json.Marshal(struct {
		Revision, SourceBinarySHA256 string
		Launches                     map[int]workerLaunch
		OwnershipChain               []workerProcess
		Invalid                      bool
		WaitsKnown                   bool
		Waits                        []FallbackWorkerObservation
	}{a.revision, a.sourceSHA, a.launches, a.chain, a.invalid, err == nil, observations})
	if e != nil || len(raw) > 16384 {
		return os.ErrInvalid
	}
	return publishClosedFile(filepath.Dir(destination), filepath.Base(destination), raw)
}
