package claudetape

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func (a *FallbackLaunchAudit) sample() {
	if a.scenario != "session-resume" || a.image == nil {
		return
	}
	var daemon struct{ PID int }
	if ReadLifecycleControl(filepath.Join(a.home, "recording-worker-daemon.json"), 16384, &daemon) != nil || daemon.PID <= 1 {
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
	if err != nil || group != pid || len(end) < 20 || end[1] != fields[1] || end[19] != fields[19] {
		return
	}
	launch := workerLaunch{pid, daemon, fields[19], registration}
	if previous, ok := a.launches[pid]; ok {
		if previous.Start != launch.Start || previous.DaemonPID != daemon {
			a.invalid = true
		}
		if previous.Registration == "absent" {
			launch.Registration = "absent"
		}
	}
	if len(a.launches) >= LifecycleWorkerLimit(a.scenario) && a.launches[pid].PID == 0 {
		a.invalid = true
		return
	}
	a.launches[pid] = launch
}
