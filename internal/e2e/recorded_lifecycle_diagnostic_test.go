//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

func TestRecordedLifecycleDiagnosticCleanup(t *testing.T) {
	for _, kind := range []string{"success", "start-failure", "launch-rejection", "cancel-before-start", "cancel-after-start"} {
		t.Run(kind, func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "diagnostic-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.v", "-test.run=^TestRecordedLifecycleDiagnosticCleanupProcess$")
			cmd.Env = append(os.Environ(), "PYRY_DIAGNOSTIC_CLEANUP="+kind, "PYRY_DIAGNOSTIC_ROOT="+root)
			out, err := cmd.CombinedOutput()
			if err == nil || ctx.Err() != nil || !bytes.Contains(out, []byte("original assertion failure")) || !bytes.Contains(out, []byte("lifetime verified")) || bytes.Contains(out, []byte("diagnostic lifetime:")) || bytes.Contains(out, []byte("DATA RACE")) {
				t.Fatalf("failure verdict/lifetime proof: %v\n%s", err, out)
			}
		})
	}
}

func TestRecordedLifecycleDiagnosticChild(t *testing.T) {
	if os.Getenv("PYRY_DIAGNOSTIC_CHILD") != "1" {
		return
	}
	_, err := os.Stdout.WriteString("child diagnostic\n")
	if err != nil {
		os.Exit(2)
	}
	if os.Getenv("PYRY_DIAGNOSTIC_CLEANUP") == "cancel-after-start" {
		ready := os.NewFile(9, "ready")
		if _, err := ready.Write([]byte{1}); err != nil {
			os.Exit(3)
		}
		_ = ready.Close()
		_, _ = io.Copy(io.Discard, os.NewFile(3, "held-input"))
	}
	os.Exit(0)
}

func TestRecordedLifecycleDiagnosticCleanupProcess(t *testing.T) {
	kind := os.Getenv("PYRY_DIAGNOSTIC_CLEANUP")
	if kind == "" {
		return
	}
	root := os.Getenv("PYRY_DIAGNOSTIC_ROOT")
	path := filepath.Join(root, "broker.diagnostic.json")
	var command *exec.Cmd
	var diagnostic *os.File
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	callbackErr := make(chan error, 1)
	var closes atomic.Int32
	ownerTest := t
	failed := !t.Run("failure", func(t *testing.T) {
		socket, job := filepath.Join(root, "socket"), filepath.Join(root, "owned.job")
		server, err := claudetape.NewLifecycleBrokerServer(socket, []string{job}, func(got string, peer int) (*exec.Cmd, io.Closer, error) {
			command = exec.Command(os.Args[0], "-test.run=^TestRecordedLifecycleDiagnosticChild$")
			command.Env = append(os.Environ(), "PYRY_DIAGNOSTIC_CHILD=1")
			if kind == "start-failure" {
				command.Path = filepath.Join(root, "missing-executable")
			}
			var err error
			diagnostic, err = recordedLifecycleDiagnostic(command, path)
			if err != nil {
				callbackErr <- err
				close(entered)
				return nil, nil, err
			}
			close(entered)
			owner := recordedDiagnosticCloser{diagnostic, &closes}
			select {
			case <-release:
			case <-time.After(20 * time.Second):
				callbackErr <- errors.New("callback release timed out")
				return nil, owner, os.ErrDeadlineExceeded
			}
			_, err = diagnostic.WriteString("callback diagnostic\n")
			if got != job || peer != os.Getpid() {
				err = errors.New("kernel peer/job changed")
			}
			callbackErr <- err
			if kind == "launch-rejection" {
				return command, owner, os.ErrInvalid
			}
			return command, owner, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			err := server.Join(5 * time.Second)
			if (err == nil) != (kind == "success") {
				t.Errorf("diagnostic lifetime: final join: %v", err)
			}
		})
		t.Cleanup(func() {
			if kind == "cancel-before-start" {
				// The callback remains held through both of Join's bounded waits.
				if err := server.Join(time.Millisecond); err == nil {
					t.Error("diagnostic lifetime: cancellation joined a held callback")
				}
			}
			if diagnostic == nil {
				t.Error("diagnostic lifetime: callback did not create diagnostic")
			} else if _, err := diagnostic.WriteString("outer cleanup diagnostic\n"); err != nil {
				t.Errorf("diagnostic lifetime: closed before handler completion: %v", err)
			}
			if kind == "cancel-after-start" {
				if err := server.Join(time.Millisecond); err == nil {
					t.Error("diagnostic lifetime: live child was not cancelled")
				}
			}
			unblock()
		})
		c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		var fds []int
		var ready *os.File
		for i := range 7 {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			ownerTest.Cleanup(func() { _ = r.Close(); _ = w.Close() })
			if i == 6 {
				ready = r
			}
			f := w
			if i == 0 || i == 4 || i == 5 {
				f = r
			}
			fds = append(fds, int(f.Fd()))
		}
		if _, _, err := c.WriteMsgUnix([]byte(job), syscall.UnixRights(fds...), nil); err != nil {
			t.Fatal(err)
		}
		if err := c.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("diagnostic lifetime: callback never entered")
		}
		if kind == "cancel-after-start" {
			unblock()
			if err := ready.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			var signal [1]byte
			if _, err := io.ReadFull(ready, signal[:]); err != nil || signal[0] != 1 {
				t.Fatalf("diagnostic lifetime: child never became ready: %v", err)
			}
		}
		t.Error("original assertion failure")
	})
	if !failed {
		t.Error("diagnostic lifetime: original failure lost")
	}
	select {
	case err := <-callbackErr:
		if err != nil {
			t.Errorf("diagnostic lifetime: callback: %v", err)
		}
	default:
		t.Fatal("diagnostic lifetime: callback did not finish")
	}
	if diagnostic == nil {
		t.Fatal("diagnostic lifetime: no file")
	}
	if _, err := diagnostic.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("diagnostic lifetime: descriptor not closed after handler: %v", err)
	}
	if got := closes.Load(); got != 1 {
		t.Errorf("diagnostic lifetime: owner closed %d times, want 1", got)
	}
	if kind == "success" {
		if command.ProcessState == nil || !command.ProcessState.Success() {
			t.Error("diagnostic lifetime: successful child not reaped")
		}
	} else if kind == "cancel-after-start" {
		if command.ProcessState == nil || command.ProcessState.Success() || !errors.Is(command.Process.Kill(), os.ErrProcessDone) {
			t.Error("diagnostic lifetime: cancelled child not killed and reaped")
		}
	} else if command.Process != nil {
		t.Error("diagnostic lifetime: rejected/cancelled child started")
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "callback diagnostic") || !strings.Contains(string(raw), "outer cleanup diagnostic") || (kind == "success" || kind == "cancel-after-start") && !strings.Contains(string(raw), "child diagnostic") {
		t.Errorf("diagnostic lifetime: retained content missing: %v %q", err, raw)
	}
	t.Log("lifetime verified")
}

type recordedDiagnosticCloser struct {
	file   *os.File
	closes *atomic.Int32
}

func (c recordedDiagnosticCloser) Close() error {
	c.closes.Add(1)
	return c.file.Close()
}
