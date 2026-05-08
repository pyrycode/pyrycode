package control

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestIsTransientStartupError(t *testing.T) {
	// Case 1: real *net.OpError wrapping *os.PathError{Err: ENOENT}, produced
	// by dialing a unix socket path that does not exist. Pins the real-world
	// shape end-to-end through the kernel.
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.sock")
	_, enoentErr := net.Dial("unix", missing)
	if enoentErr == nil {
		t.Fatalf("net.Dial against missing socket unexpectedly succeeded")
	}

	// Case 2: synthetic *net.OpError wrapping *os.SyscallError{Err: ECONNREFUSED}.
	// Synthetic because live construction is OS-dependent for unix sockets.
	econnrefusedErr := &net.OpError{
		Op:  "dial",
		Net: "unix",
		Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
	}

	// Case 5: synthetic *net.OpError wrapping a timeout.
	timeoutErr := &net.OpError{
		Op:  "dial",
		Net: "unix",
		Err: context.DeadlineExceeded,
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"ENOENT from real net.Dial", enoentErr, true},
		{"synthetic ECONNREFUSED", econnrefusedErr, true},
		{"nil", nil, false},
		{"io.EOF", io.EOF, false},
		{"timeout in OpError", timeoutErr, false},
		{"opaque string error", errors.New("kaboom"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTransientStartupError(tt.err)
			if got != tt.want {
				t.Errorf("isTransientStartupError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
