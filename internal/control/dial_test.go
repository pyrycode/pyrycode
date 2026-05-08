package control

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

// fakeDialer drives dialWithRetry with a caller-controlled error
// sequence. A nil entry means "succeed and return a net.Pipe conn".
// Once seq is exhausted the dialer keeps returning ENOENT so a test
// that under-counts attempts doesn't accidentally succeed.
type fakeDialer struct {
	calls int
	seq   []error
}

func (f *fakeDialer) dial(_ context.Context, _ string) (net.Conn, error) {
	i := f.calls
	f.calls++
	if i >= len(f.seq) {
		return nil, syscall.ENOENT
	}
	if f.seq[i] == nil {
		c1, c2 := net.Pipe()
		_ = c2.Close()
		return c1, nil
	}
	return nil, f.seq[i]
}

func TestDialWithRetry(t *testing.T) {
	t.Run("recovers after N transient failures", func(t *testing.T) {
		const n = 3
		const interval = 10 * time.Millisecond
		const budget = 200 * time.Millisecond
		f := &fakeDialer{seq: []error{syscall.ENOENT, syscall.ENOENT, syscall.ENOENT, nil}}

		start := time.Now()
		conn, err := dialWithRetry(context.Background(), "/fake/path", f.dial, budget, interval)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("dialWithRetry returned err: %v", err)
		}
		if conn == nil {
			t.Fatalf("dialWithRetry returned nil conn on success")
		}
		_ = conn.Close()

		if f.calls != n+1 {
			t.Errorf("calls = %d, want %d", f.calls, n+1)
		}
		// +50ms slack for CI scheduling — modest enough that "no retry"
		// or "wrong budget" still fails, generous enough for a loaded CI.
		maxElapsed := n*interval + 50*time.Millisecond
		if elapsed > maxElapsed {
			t.Errorf("elapsed = %v, want <= %v", elapsed, maxElapsed)
		}
	})

	t.Run("always-transient exhausts budget", func(t *testing.T) {
		const interval = 10 * time.Millisecond
		const budget = 100 * time.Millisecond
		f := &fakeDialer{} // empty seq → always ENOENT

		start := time.Now()
		conn, err := dialWithRetry(context.Background(), "/fake/path", f.dial, budget, interval)
		elapsed := time.Since(start)
		if err == nil {
			_ = conn.Close()
			t.Fatalf("dialWithRetry succeeded against always-ENOENT dialer")
		}
		if conn != nil {
			t.Errorf("dialWithRetry returned non-nil conn on error")
		}
		if !errors.Is(err, syscall.ENOENT) {
			t.Errorf("err = %v, want errors.Is(err, ENOENT)", err)
		}
		if !strings.Contains(err.Error(), "dial /fake/path:") {
			t.Errorf("err = %q, want prefix %q", err.Error(), "dial /fake/path:")
		}
		if elapsed < budget {
			t.Errorf("elapsed = %v, want >= budget %v (proves retries actually ran)", elapsed, budget)
		}
		// +50ms slack for CI scheduling.
		maxElapsed := budget + 50*time.Millisecond
		if elapsed > maxElapsed {
			t.Errorf("elapsed = %v, want <= %v", elapsed, maxElapsed)
		}
	})

	t.Run("non-transient error fails immediately", func(t *testing.T) {
		f := &fakeDialer{seq: []error{io.EOF}}

		start := time.Now()
		conn, err := dialWithRetry(context.Background(), "/fake/path", f.dial, dialRetryBudget, dialRetryInterval)
		elapsed := time.Since(start)
		if err == nil {
			_ = conn.Close()
			t.Fatalf("dialWithRetry succeeded against EOF dialer")
		}
		if conn != nil {
			t.Errorf("dialWithRetry returned non-nil conn on error")
		}
		if f.calls != 1 {
			t.Errorf("calls = %d, want 1 (no retry on non-transient error)", f.calls)
		}
		if !errors.Is(err, io.EOF) {
			t.Errorf("err = %v, want errors.Is(err, io.EOF)", err)
		}
		if !strings.Contains(err.Error(), "dial /fake/path:") {
			t.Errorf("err = %q, want prefix %q", err.Error(), "dial /fake/path:")
		}
		if elapsed > 50*time.Millisecond {
			t.Errorf("elapsed = %v, want <= 50ms (sanity)", elapsed)
		}
	})
}
