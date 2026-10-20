//go:build darwin || linux

package claudetape

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFallbackObserverGroupRequiresActualParentChain(t *testing.T) {
	// The current test owns this shell and its entire new process group. Its
	// direct child stands in for the finite helper; no captured PID is a target.
	cmd := exec.Command("/bin/sh", "-c", "sleep 5 & echo $!; wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	file, ok := out.(*os.File)
	if !ok || file.SetReadDeadline(time.Now().Add(time.Second)) != nil {
		t.Fatal("bounded owner pipe")
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal("owned child identity unavailable")
	}
	peer, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || peer <= 1 {
		t.Fatal("invalid owned child identity")
	}
	job := FallbackWorkerJob{Scenario: "fallback-suggestion", OwnerPID: peer, GroupPID: cmd.Process.Pid, DaemonPID: os.Getpid()}
	if validateFallbackWorkerKernelGroup(job, peer) != nil {
		t.Fatal("current observer/helper chain rejected")
	}
	for _, mutation := range []string{"foreign-daemon", "foreign-peer", "direct-profile", "wrong-group", "observer-is-helper"} {
		t.Run(mutation, func(t *testing.T) {
			x := job
			claimed := peer
			switch mutation {
			case "foreign-daemon":
				x.DaemonPID = os.Getppid()
			case "foreign-peer":
				claimed = os.Getpid()
			case "direct-profile":
				x.Scenario = "session-resume"
			case "wrong-group":
				x.GroupPID = os.Getpid()
			case "observer-is-helper":
				x.GroupPID = peer
			}
			if validateFallbackWorkerKernelGroup(x, claimed) == nil {
				t.Fatal("foreign ownership chain accepted")
			}
		})
	}
}
