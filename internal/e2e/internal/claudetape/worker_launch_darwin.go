package claudetape

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func workerKernelIdentity(pid int) (workerProcess, error) {
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || p == nil || int(p.Proc.P_pid) != pid || p.Proc.P_stat == 5 || p.Proc.P_stat == 0 || p.Proc.P_starttime.Sec <= 0 {
		return workerProcess{}, os.ErrInvalid
	}
	return workerProcess{PID: pid, Parent: int(p.Eproc.Ppid), Group: int(p.Eproc.Pgid), Start: fmt.Sprintf("%d.%06d", p.Proc.P_starttime.Sec, p.Proc.P_starttime.Usec)}, nil
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

// workerProcInfo uses the fixed Darwin proc_pidinfo ABI. Short reads are unknown;
// native buffers and argv/environment never enter retained diagnostics.
func workerProcInfo(pid, flavor int, address uintptr, size int) ([]byte, error) {
	raw := make([]byte, size)
	if workerNativeProcInfo(pid, flavor, address, raw) != size {
		return nil, os.ErrInvalid
	}
	return raw, nil
}
func workerNativeString(raw []byte) string {
	end := bytes.IndexByte(raw, 0)
	if end < 0 {
		return ""
	}
	return string(raw[:end])
}

// workerVnodeMatches checks the kernel vnode rather than a replaceable path.
// Darwin vnode_info is 152 bytes followed by a 1024-byte path.
func workerVnodeMatches(raw []byte, image os.FileInfo) bool {
	stat, ok := image.Sys().(*syscall.Stat_t)
	return ok && len(raw) >= 1176 && binary.NativeEndian.Uint32(raw[:4]) == uint32(stat.Dev) && binary.NativeEndian.Uint64(raw[8:16]) == stat.Ino
}
func (a *FallbackLaunchAudit) sample() {
	if a.invalid || a.image == nil || (a.scenario != "session-resume" && a.scenario != "composer-stop") {
		return
	}
	var daemon struct{ PID int }
	if ReadLifecycleControl(filepath.Join(a.home, "recording-worker-daemon.json"), 16384, &daemon) != nil || daemon.PID <= 1 || !a.ownDaemon(daemon.PID) {
		return
	}
	// PROC_PPID_ONLY lists current children with a fixed memory bound. A full
	// buffer is ambiguous and cannot establish a closed observation.
	var children [256]int32
	n := workerNativeChildPIDs(daemon.PID, children[:])
	if n <= 0 || n >= len(children)*4 || n%4 != 0 {
		return
	}
	for _, child := range children[:n/4] {
		if child > 1 {
			a.observe(int(child), daemon.PID)
		}
	}
}
func (a *FallbackLaunchAudit) observe(pid, daemon int) {
	before, err := workerKernelIdentity(pid)
	if err != nil || before.Parent != daemon || before.Group != pid || !a.ownDaemon(daemon) {
		return
	}
	if !a.executableMatches(pid) {
		return
	}
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(raw) < 6 || len(raw) > 1<<20 {
		return
	}
	argc := int(binary.NativeEndian.Uint32(raw[:4]))
	if argc < 4 || argc > 128 {
		return
	}
	end := bytes.IndexByte(raw[4:], 0)
	if end <= 0 {
		return
	}
	rest := bytes.TrimLeft(raw[5+end:], "\x00")
	argv := make([]string, 0, argc)
	for range argc {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return
		}
		argv = append(argv, string(rest[:end]))
		rest = rest[end+1:]
	}
	if argv[0] != a.binary || argv[1] != "-test.run=^TestLifecycleTapeHelperProcess$" || argv[2] != "--" || fallbackWorkerArgs(argv[3:]) != nil {
		return
	}
	cwdInfo, err := workerProcInfo(pid, 9, 0, 2352)
	if err != nil {
		return
	}
	cwd := workerNativeString(cwdInfo[152:1176])
	directory, err := os.Lstat(cwd)
	if err != nil || !directory.IsDir() || directory.Mode().Perm() != 0700 || !strings.HasPrefix(filepath.Base(cwd), "pyry-reply-") || fallbackWorkerDirectory(cwd) != nil || !workerVnodeMatches(cwdInfo, directory) {
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

	after, err := workerKernelIdentity(pid)
	if err != nil || after != before || !a.executableMatches(pid) || !a.ownDaemon(daemon) {
		return
	}
	launch := workerLaunch{PID: pid, DaemonPID: daemon, Start: before.Start, DaemonStart: a.chain[0].Start, Registration: registration}
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

func (a *FallbackLaunchAudit) executableMatches(pid int) bool {
	// PROC_PIDPATHINFO reports the actual executable, independent of argv[0].
	var path [4096]byte
	if workerNativePath(pid, path[:]) <= 0 {
		return false
	}
	executable := workerNativeString(path[:])
	image, err := os.Stat(executable)
	if err != nil || !os.SameFile(a.image, image) {
		return false
	}
	// proc_regioninfo is 96 bytes followed by vnode_info_path (1176 bytes).
	mapped := false
	address := uintptr(0)
	for range 16 {
		region, err := workerProcInfo(pid, 8, address, 1272)
		if err != nil {
			return false
		}
		if workerVnodeMatches(region[96:], a.image) && workerNativeString(region[248:]) == executable {
			mapped = true
			break
		}
		base := binary.NativeEndian.Uint64(region[80:88])
		size := binary.NativeEndian.Uint64(region[88:96])
		next := uintptr(base + size)
		if next <= address {
			return false
		}
		address = next
	}
	return mapped
}
