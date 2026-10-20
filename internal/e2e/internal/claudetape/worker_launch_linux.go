package claudetape

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

func workerKernelIdentity(pid int) (workerProcess, error) {
	raw, err := runnerPrivateProcRead(fmt.Sprintf("/proc/%d/stat", pid))
	end := bytes.LastIndexByte(raw, ')')
	if err != nil || end < 0 || !bytes.HasPrefix(raw, []byte(strconv.Itoa(pid)+" (")) {
		return workerProcess{}, os.ErrInvalid
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return workerProcess{}, os.ErrInvalid
	}
	parent, e1 := strconv.Atoi(fields[1])
	group, e2 := strconv.Atoi(fields[2])
	start, e3 := strconv.ParseUint(fields[19], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || start == 0 || fields[0] == "Z" || fields[0] == "X" {
		return workerProcess{}, os.ErrInvalid
	}
	return workerProcess{PID: pid, Parent: parent, Group: group, Start: fields[19]}, nil
}

func (a *FallbackLaunchAudit) bindRoot(pid int) {
	var err error
	a.outer, err = workerKernelIdentity(os.Getpid())
	if err != nil || a.parent != os.Getpid() {
		a.invalid = true
		return
	}
	a.root, err = workerKernelIdentity(pid)
	a.invalid = err != nil || a.root.Parent != a.outer.PID || a.root.Group != pid
}

// ownDaemon proves the full current ancestry before any child is authorized.
// A control file selects a candidate; only the independently bound root owns it.
func (a *FallbackLaunchAudit) ownDaemon(pid int) bool {
	if a.invalid || a.root.PID <= 1 || pid == a.root.PID {
		return false
	}
	for _, member := range a.chain {
		current, err := workerKernelIdentity(member.PID)
		if err != nil {
			return false
		}
		if current != member {
			a.invalid = true
			return false
		}
	}
	chain := []workerProcess{}
	for len(chain) < 5 {
		current, err := workerKernelIdentity(pid)
		if err != nil {
			return false
		}
		chain = append(chain, current)
		if current.PID == a.root.PID {
			if current != a.root {
				a.invalid = true
				return false
			}
			chain = append(chain, a.outer)
			for _, member := range chain {
				after, err := workerKernelIdentity(member.PID)
				if err != nil || after != member {
					return false
				}
			}
			if len(a.chain) != 0 && !slices.Equal(chain, a.chain) {
				a.invalid = true
				return false
			}
			a.chain = chain
			return true
		}
		pid = current.Parent
		if pid <= 1 || pid == a.outer.PID {
			return false
		}
	}
	return false
}

func (a *FallbackLaunchAudit) sample() {
	if a.scenario != "session-resume" || a.image == nil {
		return
	}
	var daemon struct{ PID int }
	if ReadLifecycleControl(filepath.Join(a.home, "recording-worker-daemon.json"), 16384, &daemon) != nil || daemon.PID <= 1 || !a.ownDaemon(daemon.PID) {
		return
	}
	// A Go daemon can fork from any runtime thread.
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", daemon.PID))
	if err != nil {
		return
	}
	for _, task := range tasks {
		raw, err := runnerPrivateProcRead(fmt.Sprintf("/proc/%d/task/%s/children", daemon.PID, task.Name()))
		if err != nil {
			continue
		}
		for _, field := range strings.Fields(string(raw)) {
			pid, err := strconv.Atoi(field)
			if err != nil || pid <= 1 {
				continue
			}
			a.observe(pid, daemon.PID)
		}
	}
}

func runnerPrivateProcRead(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// proc reports size zero; cap the read independently of stat.
	var raw [16385]byte
	n, err := f.Read(raw[:])
	if n > 16384 {
		return nil, os.ErrInvalid
	}
	return raw[:n], err
}

func (a *FallbackLaunchAudit) observe(pid, daemon int) {
	if !a.ownDaemon(daemon) {
		return
	}
	root := fmt.Sprintf("/proc/%d/", pid)
	stat, err := runnerPrivateProcRead(root + "stat")
	if err != nil {
		return
	}
	fields := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
	if len(fields) < 20 || fields[1] != strconv.Itoa(daemon) || fields[2] != strconv.Itoa(pid) {
		return
	}
	image, err := os.Stat(root + "exe")
	if err != nil || !os.SameFile(a.image, image) {
		return
	}
	raw, err := runnerPrivateProcRead(root + "cmdline")
	if err != nil {
		return
	}
	argv := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	if len(argv) < 4 || argv[0] != a.binary || argv[1] != "-test.run=^TestLifecycleTapeHelperProcess$" || argv[2] != "--" || fallbackWorkerArgs(argv[3:]) != nil {
		return
	}
	cwd, err := os.Readlink(root + "cwd")
	if err != nil {
		return
	}
	directory, err := os.Lstat(cwd)
	if err != nil || !directory.IsDir() || directory.Mode().Perm() != 0700 || !strings.HasPrefix(filepath.Base(cwd), "pyry-reply-") || fallbackWorkerDirectory(cwd) != nil {
		return
	}
	registration := "absent"
	for i := 1; i <= LifecycleWorkerLimit(a.scenario); i++ {
		path := FallbackWorkerJobPath(a.home, i)
		var job FallbackWorkerJob
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		}
		if ReadLifecycleControl(path, 16384, &job) != nil {
			registration = "unknown"
			break
		}
		if job.OwnerPID == pid {
			registration = "registered"
			break
		}
	}
	// Recheck the kernel identity after all reads; a dying process is unknown.
	after, err := runnerPrivateProcRead(root + "stat")
	if err != nil {
		return
	}
	end := strings.Fields(string(after[bytes.LastIndexByte(after, ')')+1:]))
	group, err := syscall.Getpgid(pid)
	if err != nil || group != pid || len(end) < 20 || end[1] != fields[1] || end[19] != fields[19] || !a.ownDaemon(daemon) {
		return
	}
	launch := workerLaunch{PID: pid, DaemonPID: daemon, Start: fields[19], DaemonStart: a.chain[0].Start, Registration: registration}
	if previous, ok := a.launches[pid]; ok {
		if previous.Start != launch.Start || previous.DaemonPID != daemon || previous.DaemonStart != launch.DaemonStart {
			a.invalid = true
		}
		if previous.Registration == "unknown" || launch.Registration == "unknown" {
			launch.Registration = "unknown"
		} else if previous.Registration == "registered" {
			launch.Registration = "registered"
		}
	}
	if len(a.launches) >= LifecycleWorkerLimit(a.scenario) && a.launches[pid].PID == 0 {
		a.invalid = true
		return
	}
	a.launches[pid] = launch
}
