package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// TestSpawnHelperProcess is the fake-child entry point used by SpawnPTY
// tests. The test binary re-execs itself with GO_SPAWN_HELPER=1 and the
// mode in GO_SPAWN_HELPER_MODE.
//
//   - "exit0":           exit immediately with code 0
//   - "sleep_default":   sleep 30s with no signal handling — Go's default
//                        is to terminate on SIGTERM, so this distinguishes
//                        SIGTERM (clean signal-exit, low latency) from the
//                        runtime default Kill (would also exit but masks
//                        the test signal-class assertion).
//   - "ignore_sigterm":  trap SIGTERM and ignore it; only SIGKILL ends it.
func TestSpawnHelperProcess(t *testing.T) {
	if os.Getenv("GO_SPAWN_HELPER") != "1" {
		return
	}
	switch os.Getenv("GO_SPAWN_HELPER_MODE") {
	case "exit0":
		os.Exit(0)
	case "sleep_default":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "ignore_sigterm":
		ch := make(chan os.Signal, 4)
		signal.Notify(ch, syscall.SIGTERM)
		// Drain forever until SIGKILL ends us.
		for range ch {
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown GO_SPAWN_HELPER_MODE: %q\n", os.Getenv("GO_SPAWN_HELPER_MODE"))
		os.Exit(99)
	}
}

func spawnHelperCfg(mode string) SpawnConfig {
	return SpawnConfig{
		Bin: os.Args[0],
		Args: []string{
			"-test.run=TestSpawnHelperProcess",
			"--",
		},
		Env: []string{
			"GO_SPAWN_HELPER=1",
			"GO_SPAWN_HELPER_MODE=" + mode,
		},
	}
}

func TestSpawnPTY_BasicExitsCleanly(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd, ptmx, err := SpawnPTY(ctx, spawnHelperCfg("exit0"))
	if err != nil {
		t.Fatalf("SpawnPTY: %v", err)
	}
	if ptmx == nil {
		t.Fatal("ptmx is nil")
	}
	// Drain the PTY so the child does not block on output buffer fill.
	go func() { _, _ = io.Copy(io.Discard, ptmx) }()

	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	_ = ptmx.Close()
}

// TestSpawnPTY_SIGTERMOnCancel pins that cmd.Cancel is SIGTERM (not Kill).
// A Go process with no SIGTERM handler exits via signal-termination cleanly
// — Wait returns a non-nil *exec.ExitError whose ProcessState reports the
// signal. The assertion: exit was driven by a signal, that signal is
// SIGTERM, and the exit happened well within WaitDelay (no SIGKILL).
func TestSpawnPTY_SIGTERMOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())

	cmd, ptmx, err := SpawnPTY(ctx, spawnHelperCfg("sleep_default"))
	if err != nil {
		t.Fatalf("SpawnPTY: %v", err)
	}
	go func() { _, _ = io.Copy(io.Discard, ptmx) }()
	defer func() { _ = ptmx.Close() }()

	// Override WaitDelay to a short interval so the test is fast in the
	// unhappy case where the child accidentally ignores SIGTERM.
	cmd.WaitDelay = 1 * time.Second

	// Give the child time to install signal handling (it's a Go program;
	// the runtime sets default dispositions before main() runs).
	time.Sleep(100 * time.Millisecond)
	cancel()

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case waitErr := <-waitDone:
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			t.Fatalf("Wait returned %v, want *exec.ExitError from signal", waitErr)
		}
		ws, ok := exitErr.Sys().(syscall.WaitStatus)
		if !ok {
			t.Fatalf("ProcessState.Sys() not syscall.WaitStatus: %T", exitErr.Sys())
		}
		if !ws.Signaled() {
			t.Fatalf("child did not exit via signal; ws = %+v", ws)
		}
		if ws.Signal() != syscall.SIGTERM {
			t.Errorf("child exit signal = %v, want SIGTERM", ws.Signal())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("child did not exit within 3s of ctx cancel")
	}
}

// TestSpawnPTY_SIGKILLAfterGrace pins the WaitDelay → SIGKILL contract. A
// child that traps and ignores SIGTERM survives ctx cancellation until the
// runtime forces SIGKILL after WaitDelay.
func TestSpawnPTY_SIGKILLAfterGrace(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())

	cmd, ptmx, err := SpawnPTY(ctx, spawnHelperCfg("ignore_sigterm"))
	if err != nil {
		t.Fatalf("SpawnPTY: %v", err)
	}
	go func() { _, _ = io.Copy(io.Discard, ptmx) }()
	defer func() { _ = ptmx.Close() }()

	// Shrink WaitDelay to keep the test fast — the production default of
	// 5s is verified at the package surface, not here.
	cmd.WaitDelay = 200 * time.Millisecond

	// Wait for the helper's signal handler to install. Without this, an
	// early SIGTERM lands before signal.Notify and the kernel's default
	// disposition (terminate) triggers — same exit shape as the SIGTERM
	// test, which would silently invalidate this case.
	time.Sleep(300 * time.Millisecond)
	cancel()

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case waitErr := <-waitDone:
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			t.Fatalf("Wait returned %v, want *exec.ExitError from signal", waitErr)
		}
		ws, ok := exitErr.Sys().(syscall.WaitStatus)
		if !ok {
			t.Fatalf("ProcessState.Sys() not syscall.WaitStatus: %T", exitErr.Sys())
		}
		if !ws.Signaled() {
			t.Fatalf("child did not exit via signal; ws = %+v", ws)
		}
		if ws.Signal() != syscall.SIGKILL {
			t.Errorf("child exit signal = %v, want SIGKILL", ws.Signal())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("child did not exit within 3s of ctx cancel (WaitDelay should have forced SIGKILL)")
	}
}
