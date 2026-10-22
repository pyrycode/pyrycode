package claudetape

import (
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestLifecycleServerTerminalProbe(t *testing.T) {
	if os.Getenv("PYRY_LIFECYCLE_SERVER_PROBE") != "1" {
		return
	}
	f := os.NewFile(9, "ready")
	_, err := f.Write([]byte{1})
	f.Close()
	if err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
func lifecycleServerPipes(t *testing.T, n int) ([]int, func()) {
	t.Helper()
	var files []*os.File
	var fds []int
	for i := range n {
		r, w, e := os.Pipe()
		if e != nil {
			t.Fatal(e)
		}
		files = append(files, r, w)
		if i == 0 || i == 4 || i == 5 {
			fds = append(fds, int(r.Fd()))
		} else {
			fds = append(fds, int(w.Fd()))
		}
	}
	return fds, func() {
		for _, f := range files {
			f.Close()
		}
	}
}
func TestLifecycleBrokerServerRejectsForeignPartialDuplicateTransfers(t *testing.T) {
	for _, kind := range []string{"unknown-job", "partial-job", "missing-rights", "six-rights", "excess-rights", "regular-file", "duplicate-rights", "wrong-pipe-direction", "trailing-payload", "foreign-owner", "duplicate-job", "failed-launch", "stalled-transfer"} {
		t.Run(kind, func(t *testing.T) {
			root, e := os.MkdirTemp("/tmp", "broker-control-")
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			socket := filepath.Join(root, "socket")
			job := filepath.Join(root, "owned.job")
			var calls atomic.Int32
			s, e := NewLifecycleBrokerServer(socket, []string{job}, func(path string, peer int) (*exec.Cmd, io.Closer, error) {
				calls.Add(1)
				if peer != os.Getpid() {
					t.Error("kernel peer identity changed")
				}
				if kind == "foreign-owner" || kind == "failed-launch" {
					return nil, nil, os.ErrInvalid
				}
				cmd := exec.Command(os.Args[0], "-test.run=^TestLifecycleServerTerminalProbe$")
				cmd.Env = append(os.Environ(), "PYRY_LIFECYCLE_SERVER_PROBE=1")
				return cmd, nil, nil
			})
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { _ = s.Join(time.Second) })
			connect := func() *net.UnixConn {
				c, e := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
				if e != nil {
					t.Fatal(e)
				}
				return c
			}
			c := connect()
			defer c.Close()
			if kind == "stalled-transfer" {
				// Dial can complete before Accept registers the peer. Join must exercise
				// a stalled handler rather than close an as-yet unaccepted connection.
				deadline := time.Now().Add(time.Second)
				for {
					s.mu.Lock()
					accepted := len(s.conns) == 1
					s.mu.Unlock()
					if accepted {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("stalled peer was not accepted")
					}
					time.Sleep(time.Millisecond)
				}
				if e = s.Join(20 * time.Millisecond); e == nil {
					t.Fatal("stalled peer accepted")
				}
				if calls.Load() != 0 {
					t.Fatal("stalled transfer reached launch callback", calls.Load())
				}
				return
			}
			count := 7
			if kind == "six-rights" {
				count = 6
			}
			if kind == "excess-rights" {
				count = 12
			}
			fds, closePipes := lifecycleServerPipes(t, count)
			defer closePipes()
			path := job
			if kind == "unknown-job" {
				path = job + "-foreign"
			}
			if kind == "partial-job" {
				path = job[:len(job)/2]
			}
			if kind == "duplicate-rights" {
				fds[1] = fds[0]
			}
			if kind == "wrong-pipe-direction" {
				fds[0], fds[1] = fds[1], fds[0]
			}
			rights := syscall.UnixRights(fds...)
			if kind == "missing-rights" {
				rights = nil
			}
			if kind == "regular-file" {
				f, e := os.CreateTemp(root, "regular")
				if e != nil {
					t.Fatal(e)
				}
				defer f.Close()
				fds[0] = int(f.Fd())
				rights = syscall.UnixRights(fds...)
			}
			if _, _, e = c.WriteMsgUnix([]byte(path), rights, nil); e != nil {
				t.Fatal(e)
			}
			if kind == "trailing-payload" {
				_, _ = c.Write([]byte("foreign"))
			}
			_ = c.CloseWrite()
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			var status [1]byte
			_, readErr := io.ReadFull(c, status[:])
			if kind == "duplicate-job" {
				if readErr != nil || status[0] != 1 {
					t.Fatal("valid transfer failed", readErr)
				}
				c.Close()
				c = connect()
				defer c.Close()
				if _, _, e = c.WriteMsgUnix([]byte(job), rights, nil); e != nil {
					t.Fatal(e)
				}
				_ = c.CloseWrite()
				_ = c.SetReadDeadline(time.Now().Add(time.Second))
				_, _ = io.ReadFull(c, status[:])
			} else if readErr == nil && status[0] == 1 {
				t.Fatal("invalid transfer launched broker")
			}
			if e = s.Join(2 * time.Second); e == nil {
				t.Fatal("invalid transfer accepted by final join")
			}
			expected := int32(0)
			if kind == "duplicate-job" || kind == "failed-launch" || kind == "foreign-owner" {
				expected = 1
			}
			if calls.Load() != expected {
				t.Fatal("invalid transfer reached launch callback", calls.Load())
			}
		})
	}
}
func TestLifecycleBrokerServerCancellationCannotStartLateChild(t *testing.T) {
	root, e := os.MkdirTemp("/tmp", "broker-cancel-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(root)
	socket, job := filepath.Join(root, "socket"), filepath.Join(root, "owned.job")
	entered, release := make(chan struct{}), make(chan struct{})
	command := exec.Command(os.Args[0], "-test.run=^TestLifecycleServerTerminalProbe$")
	s, e := NewLifecycleBrokerServer(socket, []string{job}, func(string, int) (*exec.Cmd, io.Closer, error) { close(entered); <-release; return command, nil, nil })
	if e != nil {
		t.Fatal(e)
	}
	c, e := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	fds, closePipes := lifecycleServerPipes(t, 7)
	defer closePipes()
	if _, _, e = c.WriteMsgUnix([]byte(job), syscall.UnixRights(fds...), nil); e != nil {
		t.Fatal(e)
	}
	_ = c.CloseWrite()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("launch validation never entered")
	}
	if e = s.Join(10 * time.Millisecond); e == nil {
		t.Fatal("in-flight launch joined early")
	}
	close(release)
	if e = s.Join(time.Second); e == nil {
		t.Fatal("cancelled launch accepted")
	}
	if command.Process != nil {
		t.Fatal("child started after server cancellation")
	}
}
