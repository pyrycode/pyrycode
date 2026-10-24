//go:build e2e && linux

package e2e

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// hostedFixtureSocket captures identities before helpers publish readiness.
// pidfds keep cleanup confined to these generations even after PID reuse.
func hostedFixtureSocket(t *testing.T) string {
	t.Helper()
	socket := filepath.Join(shortHome(t), "hosted.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	type identity struct{ pid, fd int }
	var owned []identity // Written by accept, read only after its join.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := listener.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					t.Error("hosted fixture accept:", err)
				}
				return
			}
			func() {
				defer c.Close()
				var pids [2]int32
				err := c.SetDeadline(time.Now().Add(termGrace))
				if err == nil {
					err = binary.Read(c, binary.LittleEndian, &pids)
				}
				if err != nil || pids[0] <= 1 || pids[1] <= 1 || pids[0] == pids[1] || int(pids[0]) == os.Getpid() || int(pids[1]) == os.Getpid() {
					t.Error("invalid hosted fixture registration:", err)
					return
				}
				// The helper waits for acknowledgement and has not reaped its sleep
				// child, so both identities remain pinned during this transfer.
				for _, pid := range pids {
					fd, err := unix.PidfdOpen(int(pid), 0)
					if err != nil {
						t.Error("pin hosted fixture child:", err)
						return
					}
					owned = append(owned, identity{int(pid), fd})
				}
				if _, err := c.Write([]byte{1}); err != nil {
					t.Error("acknowledge hosted fixture child:", err)
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
		defer func() {
			for _, p := range owned {
				_ = unix.Close(p.fd)
			}
		}()
		// Normal daemon shutdown already joined healthy services. This fallback
		// also covers helpers orphaned by forced shutdown and earlier generations.
		for i := len(owned) - 1; i >= 0; i-- {
			p := owned[i]
			if err := unix.PidfdSendSignal(p.fd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
				t.Errorf("retire hosted fixture pid=%d: %v", p.pid, err)
			}
		}
		deadline := time.Now().Add(killGrace)
		for {
			var remaining []int
			for _, p := range owned {
				if !hostedRetiredForCleanup(p.pid) {
					remaining = append(remaining, p.pid)
				}
			}
			if len(remaining) == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("hosted fixture children remain after retirement: %v", remaining)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	return socket
}

func hostedRetiredForCleanup(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		return false
	}
	// Parse after the final ')' because a process name may contain spaces.
	end := -1
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == ')' {
			end = i
			break
		}
	}
	if end < 0 || len(b) <= end+2 || b[end+2] != 'Z' {
		return false
	}
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	return err == nil && len(fds) == 0
}
