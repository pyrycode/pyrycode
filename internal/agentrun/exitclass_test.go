package agentrun

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// TestMain dispatches to the exit-helper before Go's test-binary flag parser
// runs. The helper exits with a specific code keyed by env so the predicate
// tests can capture a real *exec.ExitError shape (ExitCode() == 143, 137, 1)
// or a signal-killed shape (no handler installed) without depending on
// process-killing syscalls inside the test goroutine.
func TestMain(m *testing.M) {
	if mode := os.Getenv("GO_EXITCLASS_HELPER"); mode != "" {
		runExitHelper(mode)
		return
	}
	os.Exit(m.Run())
}

func runExitHelper(mode string) {
	switch mode {
	case "exit143":
		os.Exit(143)
	case "exit137":
		os.Exit(137)
	case "exit1":
		os.Exit(1)
	case "block":
		// Block indefinitely; the parent kills us with the configured signal.
		select {}
	default:
		fmt.Fprintf(os.Stderr, "unknown GO_EXITCLASS_HELPER: %q\n", mode)
		os.Exit(99)
	}
}

// exitErrFromHelper re-execs the test binary with GO_EXITCLASS_HELPER=mode
// and returns the *exec.ExitError surfaced by Wait(). Used to construct
// realistic ExitError values for the predicate cases.
func exitErrFromHelper(t *testing.T, mode string) *exec.ExitError {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "GO_EXITCLASS_HELPER="+mode)
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("exitErrFromHelper(%q): got %v (%T), want *exec.ExitError", mode, err, err)
	}
	return exitErr
}

// signalKilledExitErr spawns a blocking helper, sends it sig, and returns
// the resulting *exec.ExitError. The child has no signal handler, so the
// stdlib reports Signaled()=true with Signal()==sig and ExitCode()==-1.
func signalKilledExitErr(t *testing.T, sig syscall.Signal) *exec.ExitError {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "GO_EXITCLASS_HELPER=block")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal helper: %v", err)
	}
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("signalKilledExitErr(%v): got %v (%T), want *exec.ExitError", sig, err, err)
	}
	return exitErr
}

func TestExitErrIsBenign(t *testing.T) {
	exit143 := exitErrFromHelper(t, "exit143")
	exit137 := exitErrFromHelper(t, "exit137")
	exit1 := exitErrFromHelper(t, "exit1")
	sigtermKilled := signalKilledExitErr(t, syscall.SIGTERM)
	sigkillKilled := signalKilledExitErr(t, syscall.SIGKILL)

	cases := []struct {
		name string
		err  error
		want bool
	}{
		// Negative
		{"nil", nil, false},
		{"plain errors.New", errors.New("plain"), false},
		{"wrapped plain", fmt.Errorf("wrap: %w", errors.New("plain")), false},
		{"exit code 1", exit1, false},
		{"wrapped exit code 1", fmt.Errorf("ctx: %w", exit1), false},

		// Positive — sentinel errnos / os.ErrClosed
		{"ESRCH raw", syscall.ESRCH, true},
		{"ESRCH wrapped", fmt.Errorf("kill: %w", syscall.ESRCH), true},
		{"EPIPE raw", syscall.EPIPE, true},
		{"EPIPE wrapped", fmt.Errorf("write: %w", syscall.EPIPE), true},
		{"os.ErrClosed raw", os.ErrClosed, true},
		{"os.ErrClosed wrapped", fmt.Errorf("close: %w", os.ErrClosed), true},

		// Positive — *exec.ExitError exit codes
		{"exit 143 raw", exit143, true},
		{"exit 143 wrapped", fmt.Errorf("close: %w", exit143), true},
		{"exit 137 raw", exit137, true},
		{"exit 137 wrapped", fmt.Errorf("close: %w", exit137), true},

		// Positive — signal-killed branch (ExitCode() == -1, Signaled() == true)
		{"signal-killed SIGTERM", sigtermKilled, true},
		{"signal-killed SIGKILL", sigkillKilled, true},
		{"signal-killed SIGTERM wrapped", fmt.Errorf("close: %w", sigtermKilled), true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ExitErrIsBenign(tc.err); got != tc.want {
				t.Errorf("ExitErrIsBenign(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
